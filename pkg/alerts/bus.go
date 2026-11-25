package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/codegen/message_bus"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// A feed is one source on the message bus and the events of it that this package follows: the
// bus filters a subscription by source, so each source is followed on its own.
type feed struct {
	source string
	events func() []string
}

// virtSource is the virt module's SourceID on the message bus.
// ponytail: not verified against CasaOS-VirtManagement; a wrong value costs only that module's
// alerts, its feed finds nothing registered and retries at the one-minute cap.
const virtSource = "virt-management"

// vulnSource is the scan module's SourceID on the message bus (common.ServiceName in
// ReCasaOS-VulnScan), and vulnEvents the one event it publishes: at the end of a scan, the
// apps with a critical vulnerability not told yet that an update of the app fixes.
const vulnSource = "vuln-scan"

var vulnEvents = []string{"vuln:found"}

// feeds are the sources followed: AppManagement's events, the virt module's resource events, and
// the scan module's findings.
var feeds = []feed{
	{"app-management", busEvents},
	{virtSource, func() []string { return resourceEvents }},
	{vulnSource, func() []string { return vulnEvents }},
}

// operations are the app operations whose failure is an alert, by the name of
// their error event, app:<operation>-error, and what the sentence says.
var operations = map[string]string{
	"install":       "Installing",
	"update":        "Updating",
	"start":         "Starting",
	"stop":          "Stopping",
	"restart":       "Restarting",
	"uninstall":     "Uninstalling",
	"apply-changes": "Applying the changes to",
	"git-build":     "Building",
	"git-deploy":    "Deploying",
}

// recheckRegistered is how often a subscription that lacks some of busEvents
// asks whether AppManagement has registered them since: on an update the core
// may subscribe before AppManagement has declared its new events. A var, so
// tests can shorten it.
var recheckRegistered = time.Minute

// containerEvents are the events of AppManagement's watch of the containers. They are the ones a
// Docker update stirs up, and so the only ones Hub.Muted silences.
var containerEvents = []string{"app:container-died", "app:container-unhealthy", "app:container-restarting", "app:container-healthy"}

// appEvents are passed to Hub.OnAppEvent; they raise no alert.
var appEvents = []string{"app:install-end", "app:uninstall-end", "app:apply-changes-end"}

// boxEvents are what the box did by itself: the night's automatic updates, the newer
// versions of pinned images, the weekly cleanup (CasaOS#170).
var boxEvents = []string{"auto-update:night-end", "newer-tags:found", "cleanup:end", "cleanup:error"}

// busEvents are the events of AppManagement this package subscribes to.
func busEvents() []string {
	names := append([]string{"backup:error", "backup:check-error"}, containerEvents...)
	names = append(names, boxEvents...)
	names = append(names, appEvents...)
	names = append(names, resourceEvents...)
	for operation := range operations {
		names = append(names, "app:"+operation+"-error")
	}
	slices.Sort(names)
	return names
}

// followBus subscribes to the events of each feed on the message bus until ctx is done, each on
// its own, subscribing again when a subscription is lost or refused: after a second, then twice
// as long each time up to a minute, and after a second again once a subscription held. A source
// the bus does not know yet (a module not installed) only waits.
func (h *Hub) followBus(ctx context.Context) {
	var loops sync.WaitGroup
	for _, f := range feeds {
		loops.Add(1)
		go func() {
			defer loops.Done()
			wait := time.Second
			for {
				if h.subscribe(ctx, f) {
					wait = time.Second
				}
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					return
				}
				wait = min(2*wait, time.Minute)
			}
		}()
	}
	loops.Wait()
}

// subscribe subscribes the way this box's services do: at the address the bus
// leaves in the runtime path, with this boot's secret, to the events of the
// feed its source registered (an older one has fewer, and the bus refuses a
// subscription to one it does not know). It reads them until the subscription
// is lost, and reports whether it held.
func (h *Hub) subscribe(ctx context.Context, f feed) bool {
	runtimePath := h.RuntimePath()
	address, err := external.GetMessageBusAddress(runtimePath)
	if err != nil {
		return false
	}
	names, err := registered(ctx, address, runtimePath, f)
	if err != nil || len(names) == 0 {
		return false
	}
	// The same editor as the bus's HTTP clients: the secret goes to loopback only.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return false
	}
	_ = external.InternalRequestEditor(runtimePath)(ctx, request) // never fails
	wsURL := "ws" + strings.TrimPrefix(address, "http") + "/event/" + f.source + "?" + url.Values{"names": names}.Encode()
	connection, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, request.Header)
	if err != nil {
		logger.Info("alerts: cannot subscribe to the message bus", zap.Error(err))
		return false
	}
	defer connection.Close()
	defer context.AfterFunc(ctx, func() { connection.Close() })()
	logger.Info("alerts: subscribed to the message bus", zap.String("source", f.source), zap.Strings("events", names))
	if len(names) < len(f.events()) {
		watch, stop := context.WithCancel(ctx)
		defer stop()
		interval := recheckRegistered // read here: the goroutine may outlive this call
		go func() {
			for {
				select {
				case <-time.After(interval):
				case <-watch.Done():
					return
				}
				if more, err := registered(watch, address, runtimePath, f); err == nil && len(more) > len(names) {
					logger.Info("alerts: more events registered, subscribing again", zap.String("source", f.source), zap.Strings("events", more))
					connection.Close()
					return
				}
			}
		}()
	}
	for {
		_, data, err := connection.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				logger.Info("alerts: message bus subscription lost", zap.Error(err))
			}
			return true
		}
		var event message_bus.Event
		if json.Unmarshal(data, &event) == nil {
			h.onEvent(event.Name, event.Properties)
		}
	}
}

// registered is the events of the feed that its source registered with the bus.
func registered(ctx context.Context, address, runtimePath string, f feed) ([]string, error) {
	client, err := message_bus.NewClientWithResponses(address, message_bus.WithRequestEditorFn(external.InternalRequestEditor(runtimePath)))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := client.GetEventTypesBySourceIDWithResponse(ctx, f.source)
	if err != nil {
		return nil, err
	}
	if response.JSON200 == nil {
		return nil, fmt.Errorf("the message bus answered %s", response.Status())
	}
	var names []string
	for _, eventType := range *response.JSON200 {
		if slices.Contains(f.events(), eventType.Name) {
			names = append(names, eventType.Name)
		}
	}
	return names, nil
}

// onEvent raises or resolves the alert of an event. While Docker is being updated, and just
// after, a container event is dropped, raise or resolve: it is Docker restarting. Dropped is
// dropped, the Hub's memory of what it sent is not touched, so that no "resolved" follows an
// alert that was never sent.
func (h *Hub) onEvent(name string, properties map[string]string) {
	if h.OnAppEvent != nil && slices.Contains(appEvents, name) {
		h.OnAppEvent(name, properties)
	}
	if (slices.Contains(containerEvents, name) || isMutedResource(name, properties)) && h.Muted() {
		return
	}
	from := fromEvent
	if slices.Contains(resourceEvents, name) {
		from = fromResource
	}
	a, resolved, ok := from(name, properties)
	switch {
	case !ok:
	case resolved:
		h.resolve(a)
	case slices.Contains(logsEvents, name):
		m, b, _ := h.MasterLink()
		h.raiseWith(a, logsLink(m, b, h.Address(), properties["app:name"], h.Now()))
	default:
		h.raise(a)
	}
}

// logsEvents are the alerts that end with a link to the app's logs.
var logsEvents = []string{"app:container-died", "app:container-unhealthy", "app:container-restarting"}

// logsLink opens the Logs page on app at the minute of the event: on the fleet master when the box
// is a member, else on the box's own dashboard, else "".
func logsLink(masterURL, boxID, address, app string, at time.Time) string {
	query := "app=" + url.QueryEscape(app) + "&at=" + at.UTC().Format(time.RFC3339)
	switch {
	case app == "":
		return ""
	case masterURL != "":
		return strings.TrimSuffix(masterURL, "/") + "/#/logs?box=" + url.QueryEscape(boxID) + "&" + query
	case address != "":
		return strings.TrimSuffix(address, "/") + "/#/logs?" + query
	}
	return ""
}

// fromEvent is the alert of one of AppManagement's events, and whether the
// event resolves it rather than raises it.
func fromEvent(name string, p map[string]string) (a alert, resolved, ok bool) {
	app := appName(p)
	container := ""
	if c := p["docker:container:name"]; c != "" {
		container = " (container " + c + ")"
	}
	runtime := "app:" + p["app:name"] + ":runtime"
	switch name {
	case "backup:error":
		kind, direction := "backup", "to"
		if p["backup:kind"] == "restore" {
			kind, direction = "restore", "from"
		}
		if p["app:name"] != "" {
			kind += " of " + app
		}
		sentence := fmt.Sprintf("The %s %s %q failed", kind, direction, p["backup:destination"])
		if reason := plain(p["message"]); reason != "" {
			sentence += ": " + reason
		}
		return alert{key: "backup:" + p["app:name"] + ":" + p["backup:destination"], category: Backups, sentence: sentence + "."}, false, true
	case "backup:check-error":
		// a repository kept with restic that does not check out: no app, and not a backup
		// that failed but the copies that are already there
		sentence := fmt.Sprintf("The restic repository at %q does not check out", p["backup:destination"])
		if reason := plain(p["message"]); reason != "" {
			sentence += ": " + reason
		}
		return alert{key: "backup-check:" + p["backup:destination"], category: Backups, sentence: sentence + "."}, false, true
	case "app:container-died":
		sentence := app + " stopped unexpectedly" + container
		if code := p["docker:container:exit-code"]; code != "" {
			sentence += ", exit code " + code
		}
		return alert{key: runtime, category: Apps, sentence: sentence + "."}, false, true
	case "app:container-unhealthy":
		return alert{key: runtime, category: Apps, sentence: app + " is unhealthy" + container + "."}, false, true
	case "app:container-restarting":
		return alert{key: runtime, category: Apps, sentence: app + " keeps restarting" + container + "."}, false, true
	case "app:container-healthy":
		return alert{key: runtime, category: Apps, sentence: "Resolved: " + app + " runs normally again."}, true, true
	case "vuln:found":
		return vulnAlert(p)
	case "auto-update:night-end":
		return nightAlert(p)
	case "newer-tags:found":
		return newerTagsAlert(p)
	case "cleanup:end":
		count, _ := strconv.Atoi(p["cleanup:count"])
		size, _ := strconv.ParseInt(p["cleanup:size"], 10, 64)
		sentence := "The weekly cleanup freed " + bytesText(size)
		if count > 0 {
			sentence += fmt.Sprintf(": %d old image versions and the build cache", count)
		}
		return alert{key: "cleanup:end:" + p["cleanup:count"] + ":" + p["cleanup:size"], category: Cleanup, sentence: sentence + "."}, false, true
	case "cleanup:error":
		sentence := "The weekly cleanup failed"
		if reason := plain(p["message"]); reason != "" {
			sentence += ": " + reason
		}
		return alert{key: "cleanup:error", category: Cleanup, sentence: sentence + "."}, false, true
	}
	operation, isError := strings.CutSuffix(strings.TrimPrefix(name, "app:"), "-error")
	if doing, known := operations[operation]; isError && known {
		// The error itself is left out: a compose file's output can hold an
		// environment value. The dashboard has it.
		return alert{key: "app:" + p["app:name"] + ":" + operation, category: Apps, sentence: doing + " " + app + " failed."}, false, true
	}
	return alert{}, false, false
}

// nightAlert is what the night's automatic updates did: the apps updated, and those whose
// update failed (each runs the version it had), and those held back because the new image has
// more critical vulnerabilities than the installed one.
func nightAlert(p map[string]string) (alert, bool, bool) {
	updated, failed := names(p["auto-update:updated"]), names(p["auto-update:failed"])
	blocked := names(p["auto-update:blocked"])
	var parts []string
	if len(updated) > 0 {
		parts = append(parts, "updated "+strings.Join(updated, ", "))
	}
	if len(failed) > 0 {
		parts = append(parts, "failed for "+strings.Join(failed, ", ")+" (each runs the version it had)")
	}
	if len(blocked) > 0 {
		parts = append(parts, "blocked for "+strings.Join(blocked, ", ")+" (the new version has more critical vulnerabilities)")
	}
	if len(parts) == 0 {
		return alert{}, false, false
	}
	sentence := truncate("Tonight's automatic updates: "+strings.Join(parts, "; "), 400)
	return alert{key: "auto-update-night:" + p["auto-update:updated"] + "|" + p["auto-update:failed"] + "|" + p["auto-update:blocked"], category: AppUpdates, sentence: sentence + "."}, false, true
}

// newerTagsAlert is the newer versions of images pinned to one, found since they were last
// told: the image, the version it runs and the ones it can move to.
func newerTagsAlert(p map[string]string) (alert, bool, bool) {
	var found []struct {
		App     string `json:"app"`
		Image   string `json:"image"`
		Current string `json:"current"`
		Tag     string `json:"tag"`
		Level   string `json:"level"`
	}
	if json.Unmarshal([]byte(p["newer-tags:found"]), &found) != nil || len(found) == 0 {
		return alert{}, false, false
	}
	levels := map[string]string{"patch": "bug fixes", "minor": "minor", "major": "major"}
	var parts []string
	for _, f := range found {
		repository, _, _ := strings.Cut(f.Image, ":")
		parts = append(parts, fmt.Sprintf("%s (%s) %s → %s, %s", f.App, repository, f.Current, f.Tag, levels[f.Level]))
	}
	sentence := truncate("Newer versions are out: "+strings.Join(parts, "; "), 400)
	return alert{key: "newer-tags:" + p["newer-tags:found"], category: AppUpdates, sentence: sentence + ". Choose one in the app's settings."}, false, true
}

// vulnAlert is the apps in whose images the scan module found a critical vulnerability that an
// update of the app fixes. The module tells each vulnerability once, so the sentence says what
// to do and not how many there are: a vulnerability two apps share is counted for each.
func vulnAlert(p map[string]string) (alert, bool, bool) {
	apps := names(plain(p["apps"]))
	if len(apps) == 0 {
		return alert{}, false, false
	}
	list := truncate(strings.Join(apps, ", "), 300)

	sentence, them := "Critical vulnerabilities are fixed by an update of "+list, "them"
	if p["count"] == "1" {
		sentence = "A critical vulnerability is fixed by an update of " + list
	}
	if len(apps) == 1 {
		them = "it"
	}
	return alert{key: "vuln:found:" + list + ":" + p["count"], category: Vulnerabilities, sentence: sentence + ". Update " + them + " from the dashboard."}, false, true
}

// names is a comma-separated list, without its empty items.
func names(list string) []string {
	var out []string
	for _, name := range strings.Split(list, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// bytesText is a size as the dashboard writes it: 1.2 GB.
func bytesText(n int64) string {
	units := []string{"bytes", "KB", "MB", "GB", "TB"}
	size, unit := float64(n), 0
	for size >= 1000 && unit < len(units)-1 {
		size, unit = size/1000, unit+1
	}
	if unit == 0 {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%.1f %s", size, units[unit])
}

// appName is the app's English title when the event has it, else its name.
func appName(p map[string]string) string {
	var titles map[string]string
	if json.Unmarshal([]byte(p["app:title"]), &titles) == nil && titles["en_us"] != "" {
		return titles["en_us"]
	}
	if p["app:name"] != "" {
		return p["app:name"]
	}
	return "an app"
}

var (
	homePath = regexp.MustCompile(`(/home/|/root/)[^\s"':]*`)
	userInfo = regexp.MustCompile(`://[^/\s@]+@`)
)

// plain is an error message fit for a notification: its first line, without
// the paths under a user's home or the credentials of a URL, 200 characters
// at most.
func plain(message string) string {
	message, _, _ = strings.Cut(strings.TrimSpace(message), "\n")
	message = homePath.ReplaceAllString(message, "${1}…")
	message = userInfo.ReplaceAllString(message, "://")
	return truncate(strings.TrimRight(strings.TrimSpace(message), "."), 200)
}
