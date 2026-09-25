package alerts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/gorilla/websocket"
)

func TestEachEventsAlert(t *testing.T) {
	immich := map[string]string{"app:name": "immich", "app:title": `{"en_us":"Immich","fr_fr":"Immich FR"}`}
	with := func(p map[string]string, more ...string) map[string]string {
		merged := map[string]string{}
		for k, v := range p {
			merged[k] = v
		}
		for i := 0; i+1 < len(more); i += 2 {
			merged[more[i]] = more[i+1]
		}
		return merged
	}
	for _, tc := range []struct {
		event      string
		properties map[string]string
		want       alert
		resolved   bool
	}{
		{
			"backup:error", with(immich, "backup:destination", "offsite", "backup:kind", "backup", "message", "rclone: 403 Forbidden\nstack trace"),
			alert{"backup:immich:offsite", Backups, `The backup of Immich to "offsite" failed: rclone: 403 Forbidden.`}, false,
		},
		{
			"backup:error", with(immich, "backup:destination", "usb", "backup:kind", "restore", "message", "no space left on /home/gary/restore/x."),
			alert{"backup:immich:usb", Backups, `The restore of Immich from "usb" failed: no space left on /home/….`}, false,
		},
		{
			"backup:error", map[string]string{"backup:destination": "nas", "backup:kind": "backup"},
			alert{"backup::nas", Backups, `The backup to "nas" failed.`}, false,
		},
		{"app:install-error", with(immich, "message", "SECRET=hunter2 is invalid"), alert{"app:immich:install", Apps, "Installing Immich failed."}, false},
		{"app:update-error", with(immich), alert{"app:immich:update", Apps, "Updating Immich failed."}, false},
		{"app:start-error", with(immich), alert{"app:immich:start", Apps, "Starting Immich failed."}, false},
		{"app:stop-error", with(immich), alert{"app:immich:stop", Apps, "Stopping Immich failed."}, false},
		{"app:restart-error", with(immich), alert{"app:immich:restart", Apps, "Restarting Immich failed."}, false},
		{"app:uninstall-error", with(immich), alert{"app:immich:uninstall", Apps, "Uninstalling Immich failed."}, false},
		{"app:apply-changes-error", map[string]string{"app:name": "immich"}, alert{"app:immich:apply-changes", Apps, "Applying the changes to immich failed."}, false},
		{"app:git-build-error", with(immich), alert{"app:immich:git-build", Apps, "Building Immich failed."}, false},
		{"app:git-deploy-error", with(immich), alert{"app:immich:git-deploy", Apps, "Deploying Immich failed."}, false},
		{
			"app:container-died", with(immich, "docker:container:name", "immich-server", "docker:container:exit-code", "1"),
			alert{"app:immich:runtime", Apps, "Immich stopped unexpectedly (container immich-server), exit code 1."}, false,
		},
		{
			"app:container-unhealthy", with(immich, "docker:container:name", "immich-server"),
			alert{"app:immich:runtime", Apps, "Immich is unhealthy (container immich-server)."}, false,
		},
		{
			"app:container-restarting", map[string]string{"app:name": "immich", "docker:container:name": "immich-server"},
			alert{"app:immich:runtime", Apps, "immich keeps restarting (container immich-server)."}, false,
		},
		{"app:container-healthy", with(immich), alert{"app:immich:runtime", Apps, "Resolved: Immich runs normally again."}, true},
	} {
		t.Run(tc.event, func(t *testing.T) {
			got, resolved, ok := fromEvent(tc.event, tc.properties)
			if !ok || got != tc.want || resolved != tc.resolved {
				t.Fatalf("fromEvent() = %+v, resolved %v, ok %v; want %+v, resolved %v", got, resolved, ok, tc.want, tc.resolved)
			}
		})
	}
	for _, event := range []string{"app:install-begin", "app:install-end", "backup:end", "app:container-created", "app-store:register-error", "docker:image:pull-error"} {
		if a, _, ok := fromEvent(event, immich); ok {
			t.Errorf("fromEvent(%q) = %+v, want no alert", event, a)
		}
	}
}

func TestPlainKeepsAnErrorFitForANotification(t *testing.T) {
	for message, want := range map[string]string{
		"":                                 "",
		"timeout.":                         "timeout",
		"copy /root/.config/rclone failed": "copy /root/… failed",
		"GET https://bob:s3cret@dav.example.com/x: 401": "GET https://dav.example.com/x: 401",
		"first line\nsecond line":                       "first line",
		strings.Repeat("a", 250):                        strings.Repeat("a", 200) + "…",
	} {
		if got := plain(message); got != want {
			t.Errorf("plain(%q) = %q, want %q", message, got, want)
		}
	}
}

// bus plays the message bus: it lists AppManagement's registered events, and
// takes a subscription only with this boot's secret, to events it knows. Each
// subscription gets the events of the channel, and ends when it closes.
type bus struct {
	server        *httptest.Server
	subscriptions chan []string // the names of each subscription
	events        chan string   // the events to send, as JSON
	drop          chan struct{} // ends the current subscription
}

func newBus(t *testing.T, runtimePath string, registered ...string) *bus {
	t.Helper()
	if err := external.WriteInternalSecret(runtimePath); err != nil {
		t.Fatal(err)
	}
	secret := external.InternalAuthorization(runtimePath)
	b := &bus{subscriptions: make(chan []string, 10), events: make(chan string, 10), drop: make(chan struct{}, 10)}
	upgrader := websocket.Upgrader{}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != secret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v2/message_bus/event_type/app-management":
			var types []map[string]any
			for _, name := range registered {
				types = append(types, map[string]any{"sourceID": "app-management", "name": name, "propertyTypeList": []any{}})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(types)
		case "/v2/message_bus/event/app-management":
			names := r.URL.Query()["names"]
			for _, name := range names {
				if !slices.Contains(registered, name) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
			}
			connection, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer connection.Close()
			gone := make(chan struct{})
			go func() {
				defer close(gone)
				for {
					if _, _, err := connection.ReadMessage(); err != nil {
						return
					}
				}
			}()
			b.subscriptions <- names
			for {
				select {
				case <-gone:
					return
				case event := <-b.events:
					if connection.WriteMessage(websocket.TextMessage, []byte(event)) != nil {
						return
					}
				case <-b.drop:
					return
				}
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(b.server.Close)
	if err := os.WriteFile(filepath.Join(runtimePath, external.MessageBusAddressFilename), []byte(b.server.URL), 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *bus) subscribed(t *testing.T) []string {
	t.Helper()
	select {
	case names := <-b.subscriptions:
		return names
	case <-time.After(10 * time.Second):
		t.Fatal("no subscription")
		return nil
	}
}

// The core subscribes like one of this box's services, to the events an older
// AppManagement registered too, raises their alerts, and subscribes again when
// the bus drops it.
func TestTheBusIsFollowedAsAnInternalService(t *testing.T) {
	h, o, _ := newHub(t)
	runtimePath := t.TempDir()
	h.RuntimePath = func() string { return runtimePath }
	b := newBus(t, runtimePath, "app:install-begin", "app:install-error", "backup:error", "backup:end")
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { h.followBus(ctx); close(stopped) }()
	defer func() {
		cancel()
		<-stopped
	}()

	if got := b.subscribed(t); !reflect.DeepEqual(got, []string{"app:install-error", "backup:error"}) {
		t.Fatalf("subscribed to %v, want the registered events alerts come from", got)
	}
	b.events <- `{"sourceID":"app-management","name":"app:install-error","properties":{"app:name":"immich"},"uuid":"1"}`
	b.drop <- struct{}{}
	b.subscribed(t)
	b.events <- `{"sourceID":"app-management","name":"backup:error","properties":{"app:name":"immich","backup:destination":"offsite","backup:kind":"backup"},"uuid":"2"}`

	want := []string{"Installing immich failed.\nhttp://192.168.1.20", `The backup of immich to "offsite" failed.` + "\nhttp://192.168.1.20"}
	deadline := time.Now().Add(10 * time.Second)
	for len(o.texts(phone)) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := o.texts(phone)
	slices.Sort(got) // each alert is sent on its own
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sent %q, want %q", got, want)
	}
}

func TestNoSubscriptionWithoutAnEventToFollow(t *testing.T) {
	h, _, _ := newHub(t)
	runtimePath := t.TempDir()
	h.RuntimePath = func() string { return runtimePath }

	if h.subscribe(context.Background()) {
		t.Fatal("subscribed with no bus address")
	}
	newBus(t, runtimePath, "app:install-begin") // an AppManagement not started yet registers nothing we follow
	if h.subscribe(context.Background()) {
		t.Fatal("subscribed to nothing")
	}
}
