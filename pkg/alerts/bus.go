package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/codegen/message_bus"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// busSource is where the events an alert comes from are published.
const busSource = "app-management"

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

// busEvents are the events of AppManagement this package subscribes to.
func busEvents() []string {
	names := []string{"backup:error", "app:container-died", "app:container-unhealthy", "app:container-restarting", "app:container-healthy"}
	for operation := range operations {
		names = append(names, "app:"+operation+"-error")
	}
	slices.Sort(names)
	return names
}

// followBus subscribes to AppManagement's events on the message bus until ctx
// is done, subscribing again when the subscription is lost or refused: after
// a second, then twice as long each time up to a minute, and after a second
// again once a subscription held.
func (h *Hub) followBus(ctx context.Context) {
	wait := time.Second
	for {
		if h.subscribe(ctx) {
			wait = time.Second
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
		wait = min(2*wait, time.Minute)
	}
}

// subscribe subscribes the way this box's services do: at the address the bus
// leaves in the runtime path, with this boot's secret, to the events of
// busEvents AppManagement registered (an older AppManagement has fewer, and
// the bus refuses a subscription to one it does not know). It reads them
// until the subscription is lost, and reports whether it held.
func (h *Hub) subscribe(ctx context.Context) bool {
	runtimePath := h.RuntimePath()
	address, err := external.GetMessageBusAddress(runtimePath)
	if err != nil {
		return false
	}
	names, err := registered(ctx, address, runtimePath)
	if err != nil || len(names) == 0 {
		return false
	}
	// The same editor as the bus's HTTP clients: the secret goes to loopback only.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return false
	}
	_ = external.InternalRequestEditor(runtimePath)(ctx, request) // never fails
	wsURL := "ws" + strings.TrimPrefix(address, "http") + "/event/" + busSource + "?" + url.Values{"names": names}.Encode()
	connection, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, request.Header)
	if err != nil {
		logger.Info("alerts: cannot subscribe to the message bus", zap.Error(err))
		return false
	}
	defer connection.Close()
	defer context.AfterFunc(ctx, func() { connection.Close() })()
	logger.Info("alerts: subscribed to the message bus", zap.Strings("events", names))
	if len(names) < len(busEvents()) {
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
				if more, err := registered(watch, address, runtimePath); err == nil && len(more) > len(names) {
					logger.Info("alerts: AppManagement registered more events, subscribing again", zap.Strings("events", more))
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

// registered is the events of busEvents AppManagement registered with the bus.
func registered(ctx context.Context, address, runtimePath string) ([]string, error) {
	client, err := message_bus.NewClientWithResponses(address, message_bus.WithRequestEditorFn(external.InternalRequestEditor(runtimePath)))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := client.GetEventTypesBySourceIDWithResponse(ctx, busSource)
	if err != nil {
		return nil, err
	}
	if response.JSON200 == nil {
		return nil, fmt.Errorf("the message bus answered %s", response.Status())
	}
	var names []string
	for _, eventType := range *response.JSON200 {
		if slices.Contains(busEvents(), eventType.Name) {
			names = append(names, eventType.Name)
		}
	}
	return names, nil
}

// onEvent raises or resolves the alert of an event.
func (h *Hub) onEvent(name string, properties map[string]string) {
	a, resolved, ok := fromEvent(name, properties)
	switch {
	case !ok:
	case resolved:
		h.resolve(a)
	default:
		h.raise(a)
	}
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
	}
	operation, isError := strings.CutSuffix(strings.TrimPrefix(name, "app:"), "-error")
	if doing, known := operations[operation]; isError && known {
		// The error itself is left out: a compose file's output can hold an
		// environment value. The dashboard has it.
		return alert{key: "app:" + p["app:name"] + ":" + operation, category: Apps, sentence: doing + " " + app + " failed."}, false, true
	}
	return alert{}, false, false
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
