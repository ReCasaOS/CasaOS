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
	"sync"
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

// sourceBus is what the bus holds for one source: the events it registered, and the channels of
// its subscriptions.
type sourceBus struct {
	registered    []string
	subscriptions chan []string // the names of each subscription
	events        chan string   // the events to send, as JSON
	drop          chan struct{} // ends the current subscription
}

func newSourceBus(registered ...string) *sourceBus {
	return &sourceBus{registered: registered, subscriptions: make(chan []string, 10), events: make(chan string, 10), drop: make(chan struct{}, 10)}
}

// bus plays the message bus: it lists the events each source registered, and takes a subscription
// only with this boot's secret, to events of that source it knows. The source "app-management" is
// the one the fields subscriptions, events and drop are about; a source nobody registered is a 404.
type bus struct {
	mu            sync.Mutex
	sources       map[string]*sourceBus
	server        *httptest.Server
	subscriptions chan []string
	events        chan string
	drop          chan struct{}
}

func newBus(t *testing.T, runtimePath string, registered ...string) *bus {
	t.Helper()
	if err := external.WriteInternalSecret(runtimePath); err != nil {
		t.Fatal(err)
	}
	secret := external.InternalAuthorization(runtimePath)
	app := newSourceBus(registered...)
	b := &bus{sources: map[string]*sourceBus{"app-management": app}, subscriptions: app.subscriptions, events: app.events, drop: app.drop}
	upgrader := websocket.Upgrader{}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != secret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if source, ok := strings.CutPrefix(r.URL.Path, "/v2/message_bus/event_type/"); ok {
			if b.source(source) == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var types []map[string]any
			for _, name := range b.known(source) {
				types = append(types, map[string]any{"sourceID": source, "name": name, "propertyTypeList": []any{}})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(types)
			return
		}
		source, ok := strings.CutPrefix(r.URL.Path, "/v2/message_bus/event/")
		s := b.source(source)
		if !ok || s == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		names := r.URL.Query()["names"]
		for _, name := range names {
			if !slices.Contains(b.known(source), name) {
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
		s.subscriptions <- names
		for {
			select {
			case <-gone:
				return
			case event := <-s.events:
				if connection.WriteMessage(websocket.TextMessage, []byte(event)) != nil {
					return
				}
			case <-s.drop:
				return
			}
		}
	}))
	t.Cleanup(b.server.Close)
	if err := os.WriteFile(filepath.Join(runtimePath, external.MessageBusAddressFilename), []byte(b.server.URL), 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *bus) source(name string) *sourceBus {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sources[name]
}

// addSource is another source, such as a module, registering its events with the bus.
func (b *bus) addSource(source string, names ...string) *sourceBus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sources[source] = newSourceBus(names...)
	return b.sources[source]
}

// known is what a source has registered so far.
func (b *bus) known(source string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.sources[source].registered)
}

// register is AppManagement declaring more events, as it does when it starts.
func (b *bus) register(names ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	app := b.sources["app-management"]
	app.registered = append(app.registered, names...)
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

// On an update the core may subscribe before AppManagement has declared its
// new events: it subscribes again once they are, without waiting for the bus to
// drop it.
func TestEventsRegisteredLaterAreFollowed(t *testing.T) {
	previous := recheckRegistered
	recheckRegistered = 20 * time.Millisecond
	t.Cleanup(func() { recheckRegistered = previous })
	h, _, _ := newHub(t)
	runtimePath := t.TempDir()
	h.RuntimePath = func() string { return runtimePath }
	b := newBus(t, runtimePath, "backup:error")
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { h.followBus(ctx); close(stopped) }()
	defer func() {
		cancel()
		<-stopped
	}()

	if got := b.subscribed(t); !reflect.DeepEqual(got, []string{"backup:error"}) {
		t.Fatalf("first subscription to %v", got)
	}
	b.register("app:container-died", "app:container-healthy")
	if got := b.subscribed(t); !reflect.DeepEqual(sorted(got), []string{"app:container-died", "app:container-healthy", "backup:error"}) {
		t.Fatalf("subscribed again to %v, want the events registered since", got)
	}
}

func TestNoSubscriptionWithoutAnEventToFollow(t *testing.T) {
	h, _, _ := newHub(t)
	runtimePath := t.TempDir()
	h.RuntimePath = func() string { return runtimePath }

	if h.subscribe(context.Background(), feeds[0]) {
		t.Fatal("subscribed with no bus address")
	}
	newBus(t, runtimePath, "app:install-begin") // an AppManagement not started yet registers nothing we follow
	if h.subscribe(context.Background(), feeds[0]) {
		t.Fatal("subscribed to nothing")
	}
}

const virtResourceEvent = `{"sourceID":"virt-management","name":"resource:alert-firing","properties":{"item":"vm:abc","name":"web","kind":"vm","situation":"vm_crashed"}}`

// Each source is followed on its own: the bus filters a subscription by source.
func TestEachSourceIsFollowedOnItsOwn(t *testing.T) {
	h, o, _ := newHub(t)
	runtimePath := t.TempDir()
	h.RuntimePath = func() string { return runtimePath }
	b := newBus(t, runtimePath, "resource:alert-firing")
	virt := b.addSource("virt-management", "resource:alert-firing", "resource:alert-resolved")
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { h.followBus(ctx); close(stopped) }()
	defer func() {
		cancel()
		<-stopped
	}()

	if got := b.subscribed(t); !reflect.DeepEqual(got, []string{"resource:alert-firing"}) {
		t.Fatalf("app-management subscription to %v", got)
	}
	select {
	case got := <-virt.subscriptions:
		if !reflect.DeepEqual(sorted(got), []string{"resource:alert-firing", "resource:alert-resolved"}) {
			t.Fatalf("virt subscription to %v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no subscription to the virt module")
	}
	virt.events <- virtResourceEvent
	waitForTexts(t, o, 1)
	if got := o.texts(phone); !reflect.DeepEqual(got, []string{"web crashed.\nhttp://192.168.1.20"}) {
		t.Fatalf("sent %q", got)
	}
}

// A box without the virt module has nothing registered under its source: that feed only waits, and
// AppManagement's goes on, and followBus ends with its context.
func TestAnAbsentVirtModuleDoesNotDisturbAppManagementsFeed(t *testing.T) {
	h, o, _ := newHub(t)
	runtimePath := t.TempDir()
	h.RuntimePath = func() string { return runtimePath }
	b := newBus(t, runtimePath, "app:install-error")
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { h.followBus(ctx); close(stopped) }()

	if got := b.subscribed(t); !reflect.DeepEqual(got, []string{"app:install-error"}) {
		t.Fatalf("subscribed to %v", got)
	}
	b.events <- `{"sourceID":"app-management","name":"app:install-error","properties":{"app:name":"immich"},"uuid":"1"}`
	waitForTexts(t, o, 1)
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("followBus did not stop")
	}
}

// Resource events AppManagement registers after the first subscription are followed too.
func TestResourceEventsRegisteredLaterAreFollowed(t *testing.T) {
	previous := recheckRegistered
	recheckRegistered = 20 * time.Millisecond
	t.Cleanup(func() { recheckRegistered = previous })
	h, _, _ := newHub(t)
	runtimePath := t.TempDir()
	h.RuntimePath = func() string { return runtimePath }
	b := newBus(t, runtimePath, "backup:error")
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { h.followBus(ctx); close(stopped) }()
	defer func() {
		cancel()
		<-stopped
	}()

	if got := b.subscribed(t); !reflect.DeepEqual(got, []string{"backup:error"}) {
		t.Fatalf("first subscription to %v", got)
	}
	b.register("resource:alert-firing")
	if got := b.subscribed(t); !reflect.DeepEqual(sorted(got), []string{"backup:error", "resource:alert-firing"}) {
		t.Fatalf("subscribed again to %v, want the resource event registered since", got)
	}
}

// waitForTexts waits until n messages reached the phone.
func waitForTexts(t *testing.T, o *outbox, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(o.texts(phone)) < n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(o.texts(phone)); got != n {
		t.Fatalf("%d messages, want %d", got, n)
	}
}

// sorted is names in order, whatever order the bus registered them in.
func sorted(names []string) []string {
	names = slices.Clone(names)
	slices.Sort(names)
	return names
}
