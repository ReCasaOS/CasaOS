package share

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fakeManagement is the Gateway's management API: it records the calls it answers with a success, and
// answers the next fail calls 502. guard, when set, is the answer to every /v1/gateway/guard call. It keeps the
// hosts it serves, and answers GET /v1/gateway/hosts with them without recording that call.
type fakeManagement struct {
	mu       sync.Mutex
	calls    []sent
	fail     int
	guard    int
	status   string // when set, the body of GET /v1/gateway/guard
	putGuard int    // when set, the answer to PUT /v1/gateway/guard
	putMsg   string // when set, the message of that answer
	hosts    map[string]string
	onPut    func() // when set, runs at each PUT before it is answered
}

func (m *fakeManagement) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	onPut := m.onPut
	m.mu.Unlock()
	if onPut != nil && r.Method == http.MethodPut {
		onPut()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail > 0 {
		m.fail--
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	isGuard := strings.HasPrefix(r.URL.Path, "/v1/gateway/guard")
	if isGuard && m.guard != 0 {
		w.WriteHeader(m.guard)
		return
	}
	if r.Method == http.MethodPut && r.URL.Path == "/v1/gateway/guard" && m.putGuard != 0 {
		w.WriteHeader(m.putGuard)
		if m.putMsg != "" {
			json.NewEncoder(w).Encode(map[string]string{"message": m.putMsg})
		}
		return
	}
	if m.hosts == nil {
		m.hosts = map[string]string{}
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/gateway/guard" && m.status != "" {
		io.WriteString(w, m.status)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/gateway/hosts" {
		routes := []map[string]string{}
		for h, target := range m.hosts {
			routes = append(routes, map[string]string{"host": h, "target": target})
		}
		json.NewEncoder(w).Encode(routes)
		return
	}
	m.calls = append(m.calls, sent{r.Method, r.URL.Path, strings.TrimSpace(string(b))})
	if r.Method == http.MethodPut && r.URL.Path == "/v1/gateway/hosts" {
		var route struct{ Host, Target string }
		json.Unmarshal(b, &route)
		m.hosts[route.Host] = route.Target
	}
	if h, ok := strings.CutPrefix(r.URL.Path, "/v1/gateway/hosts/"); ok && r.Method == http.MethodDelete {
		delete(m.hosts, h)
	}
	if r.Method == http.MethodPut && !isGuard {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (m *fakeManagement) setGuard(status int) { m.mu.Lock(); m.guard = status; m.mu.Unlock() }

func (m *fakeManagement) registered() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for h, t := range m.hosts {
		out[h] = t
	}
	return out
}

func (m *fakeManagement) forget() { m.mu.Lock(); m.calls = nil; m.mu.Unlock() }

// pushes are the guard configs received, in order.
func (m *fakeManagement) pushes(t *testing.T) []GatewayGuardConfig {
	t.Helper()
	var out []GatewayGuardConfig
	for _, c := range m.seen() {
		if c.method == "PUT" && c.path == "/v1/gateway/guard" {
			var cfg GatewayGuardConfig
			if err := json.Unmarshal([]byte(c.body), &cfg); err != nil {
				t.Fatal(err)
			}
			out = append(out, cfg)
		}
	}
	return out
}

// order is the calls as "METHOD path".
func (m *fakeManagement) order() []string {
	var out []string
	for _, c := range m.seen() {
		out = append(out, c.method+" "+c.path)
	}
	return out
}

func (m *fakeManagement) count(c sent) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, x := range m.calls {
		if x == c {
			n++
		}
	}
	return n
}

func (m *fakeManagement) total() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.calls) }

// seen is a copy of the calls, for failure messages.
func (m *fakeManagement) seen() []sent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sent(nil), m.calls...)
}

// fakeCompose is AppManagement's list of installed apps.
type fakeCompose struct {
	mu    sync.Mutex
	apps  map[string]Installed
	lists int // how many times it was asked
}

func (c *fakeCompose) asked() int { c.mu.Lock(); defer c.mu.Unlock(); return c.lists }

func (c *fakeCompose) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lists++
	data := map[string]any{}
	for name, a := range c.apps {
		data[name] = map[string]any{"store_info": map[string]string{"port_map": a.Port, "scheme": a.Scheme}}
	}
	json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (c *fakeCompose) set(name, port, scheme string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apps[name] = Installed{Name: name, Port: port, Scheme: scheme}
}

func (c *fakeCompose) remove(name string) { c.mu.Lock(); defer c.mu.Unlock(); delete(c.apps, name) }

// fixture is a Service wired to fakes: a Gateway, AppManagement with jellyfin installed, a scripted relay
// API, a tunnel relay, a fake ACME, and a clock that moves only when the test says so.
type fixture struct {
	s       *Service
	mgmt    *fakeManagement
	compose *fakeCompose
	tunnel  *fakeTunnelRelay
	relay   *[]sent
	state   string
	runtime string

	mu  sync.Mutex
	now time.Time
}

func newFixture(t *testing.T, replies ...reply) *fixture {
	t.Helper()
	f := &fixture{mgmt: &fakeManagement{}, compose: &fakeCompose{apps: map[string]Installed{}},
		now: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	f.compose.set("jellyfin", "8096", "http")
	mgmt := httptest.NewServer(f.mgmt)
	t.Cleanup(mgmt.Close)
	compose := httptest.NewServer(f.compose)
	t.Cleanup(compose.Close)
	f.runtime = fakeRuntime(t, "management.url", mgmt)()
	for name, content := range map[string]string{"app-management.url": compose.URL, "share.addr": newEchoGateway(t).Addr} {
		if err := os.WriteFile(filepath.Join(f.runtime, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.tunnel = newFakeTunnelRelay(t, nil)
	key := testKey(t)

	s := New(t.TempDir())
	relay, calls := fakeRelay(replies...)
	s.Relay.Signed, f.relay = relay.Signed, calls
	s.RuntimePath = func() string { return f.runtime }
	s.RelayURL = func() (string, bool) { return f.tunnel.URL, true }
	s.BoxKey = func() (ed25519.PrivateKey, error) { return key, nil }
	s.Now = func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
	log := newCallLog()
	s.ACME.newClient, s.ACME.TXT = newFakeACME(log).newClient, newFakeTXT(log)
	s.backoffMin, s.replacedWait, s.retryMin = 10*time.Millisecond, 50*time.Millisecond, 20*time.Millisecond
	f.s = s
	f.state = filepath.Join(s.Root, "var/lib/casaos/fleet", StateFile)
	return f
}

func (f *fixture) advance(d time.Duration) { f.mu.Lock(); f.now = f.now.Add(d); f.mu.Unlock() }

// named gives the box its name; the fixture's first scripted reply must be the relay's claim.
func (f *fixture) named(t *testing.T) {
	t.Helper()
	if err := f.s.SetName(context.Background(), "mybox", true); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) share(t *testing.T, app string) {
	t.Helper()
	if err := f.s.Share(context.Background(), app); err != nil {
		t.Fatal(err)
	}
}

// run runs the Service until the test ends.
func (f *fixture) run(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.s.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (f *fixture) saved(t *testing.T) State {
	t.Helper()
	st, ok := loadState(f.state)
	if !ok {
		t.Fatal("no share.json")
	}
	return st
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %s", what)
		}
	}
}

func appOf(st Status, name string) AppStatus {
	for _, a := range st.Apps {
		if a.Name == name {
			return a
		}
	}
	return AppStatus{}
}

var (
	claim     = reply{status: 201, body: infoBody}
	putJelly  = sent{"PUT", "/v1/gateway/hosts", `{"host":"jellyfin.mybox.box.example.org","target":"http://127.0.0.1:8096"}`}
	dropJelly = sent{"DELETE", "/v1/gateway/hosts/jellyfin.mybox.box.example.org", ""}
)

func TestNeedsRemoteAccess(t *testing.T) {
	ctx := context.Background()
	st := New(t.TempDir()).Status(ctx) // nothing wired
	data, _ := json.Marshal(st)
	for _, part := range []string{`"supported":false`, `"reason":"remote-access"`, `"apps":[]`, `"not_after":null`, `"tunnel":{"connected":false,"error":""}`, `"tos_url":"` + TOSURL} {
		if !strings.Contains(string(data), part) {
			t.Errorf("%s lacks %s", data, part)
		}
	}

	f := newFixture(t)
	f.s.RelayURL = func() (string, bool) { return "", false }
	if st := f.s.Status(ctx); st.Supported || st.Reason != "remote-access" {
		t.Fatalf("%+v", st)
	}
	if err := f.s.SetName(ctx, "mybox", true); !errors.Is(err, ErrNoRelay) {
		t.Fatalf("SetName: %v", err)
	}
	if err := f.s.Share(ctx, "jellyfin"); !errors.Is(err, ErrNoRelay) {
		t.Fatalf("Share: %v", err)
	}
	if len(*f.relay) != 0 {
		t.Fatalf("relay calls %+v", *f.relay)
	}
}

func TestSetNameClaimsAndSaves(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, claim, reply{status: 200, body: infoBody})
	if err := f.s.SetName(ctx, "MyBox", true); !errors.Is(err, ErrBadName) {
		t.Fatalf("capitals: %v", err)
	}
	if err := f.s.SetName(ctx, "mybox", false); !errors.Is(err, ErrTOS) {
		t.Fatalf("no TOS: %v", err)
	}
	f.named(t)
	if len(*f.relay) != 1 || (*f.relay)[0] != (sent{"PUT", "/v1/share/name", `{"name":"mybox"}`}) {
		t.Fatalf("relay calls %+v", *f.relay)
	}
	st := f.saved(t)
	if st.Name != "mybox" || st.Host != "mybox.box.example.org" || st.Domain != "box.example.org" || !st.TOSAcceptedAt.Equal(f.now) {
		t.Fatalf("state %+v", st)
	}
	if err := f.s.SetName(ctx, "other", true); !errors.Is(err, ErrNameSet) || len(*f.relay) != 2 {
		t.Fatalf("second: %v, %d calls", err, len(*f.relay))
	}
}

func TestShareRegistersHostAndSavesState(t *testing.T) {
	ctx := context.Background()
	if err := newFixture(t).s.Share(ctx, "jellyfin"); !errors.Is(err, ErrNoName) {
		t.Fatalf("no name: %v", err)
	}
	f := newFixture(t, claim)
	f.compose.set("noport", "", "http")
	f.named(t)
	f.share(t, "jellyfin")
	if f.mgmt.count(putJelly) != 1 {
		t.Fatalf("gateway saw %+v", f.mgmt.seen())
	}
	st := f.s.Status(ctx)
	a := appOf(st, "jellyfin")
	if !st.Supported || st.Name != "mybox" || st.Host != "mybox.box.example.org" || st.TOSURL != TOSURL ||
		a != (AppStatus{Name: "jellyfin", Label: "jellyfin", Shared: true, URL: "https://jellyfin.mybox.box.example.org", Port: "8096", Scheme: "http"}) {
		t.Fatalf("status %+v", st)
	}
	if a := appOf(st, "noport"); a.Shared || a.Error != ErrNoPort.Error() {
		t.Fatalf("noport %+v", a)
	}
	if got := f.saved(t).Apps; len(got) != 1 || got["jellyfin"] != (App{Port: "8096", Scheme: "http"}) {
		t.Fatalf("state apps %+v", got)
	}
	if err := f.s.Share(ctx, "nope"); !errors.Is(err, ErrUnknownApp) {
		t.Fatalf("unknown: %v", err)
	}
	if err := f.s.Share(ctx, "noport"); !errors.Is(err, ErrNoPort) {
		t.Fatalf("noport: %v", err)
	}
}

func TestLabelCollisionIsRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, claim)
	f.compose.set("My App", "8080", "http")
	f.compose.set("my-app", "8081", "http")
	f.named(t)
	f.share(t, "My App")
	var taken ErrLabelTaken
	if err := f.s.Share(ctx, "my-app"); !errors.As(err, &taken) || taken.Other != "My App" {
		t.Fatalf("got %v", err)
	}
	if a := appOf(f.s.Status(ctx), "my-app"); a.Shared || !strings.Contains(a.Error, "My App") {
		t.Fatalf("status %+v", a)
	}
}

func TestUnshareRemovesHost(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, claim)
	f.named(t)
	f.share(t, "jellyfin")
	for range 2 {
		if err := f.s.Unshare(ctx, "jellyfin"); err != nil {
			t.Fatal(err)
		}
	}
	if f.mgmt.count(dropJelly) != 1 || len(f.saved(t).Apps) != 0 {
		t.Fatalf("gateway %+v, state %+v", f.mgmt.seen(), f.saved(t))
	}
}

func TestUninstallEventUnshares(t *testing.T) {
	f := newFixture(t, claim)
	f.named(t)
	f.share(t, "jellyfin")
	f.s.OnAppEvent("app:uninstall-end", map[string]string{"app:name": "jellyfin"})
	eventually(t, "unshared", func() bool { return f.mgmt.count(dropJelly) == 1 && len(f.saved(t).Apps) == 0 })
}

func TestResyncDropsAVanishedApp(t *testing.T) {
	f := newFixture(t, claim)
	f.compose.set("other", "8080", "http") // the list stays non-empty: an empty one is not an uninstall
	f.named(t)
	f.share(t, "jellyfin")
	f.run(t)
	eventually(t, "the start's resync", func() bool { return f.mgmt.count(putJelly) == 2 })
	f.compose.remove("jellyfin")
	f.s.OnAppEvent("app:install-end", map[string]string{"app:name": "other"})
	eventually(t, "unshared", func() bool { return f.mgmt.count(dropJelly) == 1 && len(f.saved(t).Apps) == 0 })
}

func TestResyncKeepsSharesOnAnEmptyAppList(t *testing.T) {
	f := newFixture(t)
	f.compose.remove("jellyfin") // AppManagement still loading: {"data":{}}
	if err := saveState(f.state, State{Name: "mybox", Host: "mybox.box.example.org", Domain: "box.example.org",
		Apps: map[string]App{"jellyfin": {Port: "8096", Scheme: "http"}}}); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	eventually(t, "a retry after the first resync", func() bool { return f.compose.asked() >= 2 })
	if f.mgmt.count(dropJelly) != 0 || f.saved(t).Apps["jellyfin"] != (App{Port: "8096", Scheme: "http"}) {
		t.Fatalf("gateway %+v, state %+v", f.mgmt.seen(), f.saved(t))
	}
	f.compose.set("jellyfin", "8096", "http")
	eventually(t, "the host registered", func() bool { return f.mgmt.count(putJelly) == 1 })
	if f.mgmt.count(dropJelly) != 0 {
		t.Fatalf("gateway %+v", f.mgmt.seen())
	}
}

func TestShareDuringResetReportsNoName(t *testing.T) {
	f := newFixture(t, claim, reply{status: 204})
	f.named(t)
	f.mgmt.mu.Lock()
	f.mgmt.onPut = func() { f.s.Reset(context.Background()) } // Reset lands while Share waits on the Gateway
	f.mgmt.mu.Unlock()
	if err := f.s.Share(context.Background(), "jellyfin"); !errors.Is(err, ErrNoName) {
		t.Fatalf("got %v", err)
	}
	if f.mgmt.count(dropJelly) != 1 || len(f.mgmt.registered()) != 0 { // the host does not outlive the share
		t.Fatalf("gateway %+v", f.mgmt.seen())
	}
	if _, err := os.Stat(f.state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("share.json: %v", err)
	}
}

func TestASecondRunReturnsAtOnce(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	eventually(t, "running", func() bool { f.s.mu.Lock(); defer f.s.mu.Unlock(); return f.s.running })
	done := make(chan struct{})
	go func() { f.s.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a second Run did not return")
	}
}

func TestPortChangeFollows(t *testing.T) {
	f := newFixture(t, claim)
	f.named(t)
	f.share(t, "jellyfin")
	f.run(t)
	eventually(t, "the start's resync", func() bool { return f.mgmt.count(putJelly) == 2 })
	f.compose.set("jellyfin", "8097", "https")
	f.s.OnAppEvent("app:apply-changes-end", map[string]string{"app:name": "jellyfin"})
	moved := sent{"PUT", "/v1/gateway/hosts", `{"host":"jellyfin.mybox.box.example.org","target":"https://127.0.0.1:8097"}`}
	eventually(t, "moved", func() bool {
		return f.mgmt.count(moved) == 1 && f.saved(t).Apps["jellyfin"] == (App{Port: "8097", Scheme: "https"})
	})
}

func TestResetReleasesAndClears(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, claim, reply{status: 204})
	f.compose.set("nextcloud", "7580", "https")
	f.named(t)
	f.run(t)
	f.share(t, "jellyfin")
	f.share(t, "nextcloud")
	y := f.tunnel.Next()
	eventually(t, "the certificate", func() bool { return !f.s.ACME.Status().NotAfter.IsZero() }) // Share woke the ACME loop
	f.s.Reset(ctx)
	if len(*f.relay) != 2 || (*f.relay)[1] != (sent{"DELETE", "/v1/share/name", ""}) {
		t.Fatalf("relay calls %+v", *f.relay)
	}
	if f.mgmt.count(dropJelly) != 1 || f.mgmt.count(sent{"DELETE", "/v1/gateway/hosts/nextcloud.mybox.box.example.org", ""}) != 1 {
		t.Fatalf("gateway %+v", f.mgmt.seen())
	}
	if _, err := os.Stat(f.state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("share.json: %v", err)
	}
	eventually(t, "the tunnel closed", y.IsClosed)
	for _, p := range []string{f.s.ACME.KeyFile, f.s.ACME.CertFile} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}

	// a relay that fails: the same local cleanup
	f = newFixture(t, claim, reply{err: errors.New("relay down")})
	f.named(t)
	f.share(t, "jellyfin")
	f.s.Reset(ctx)
	if _, err := os.Stat(f.state); !errors.Is(err, os.ErrNotExist) || f.mgmt.count(dropJelly) != 1 {
		t.Fatalf("share.json: %v, gateway %+v", err, f.mgmt.seen())
	}

	// without a name: nothing to do
	f = newFixture(t)
	f.s.Reset(ctx)
	if len(*f.relay) != 0 || f.mgmt.total() != 0 {
		t.Fatalf("relay %+v, gateway %+v", *f.relay, f.mgmt.seen())
	}
}

func TestHostsAreRegisteredAtStart(t *testing.T) {
	f := newFixture(t)
	if err := saveState(f.state, State{Name: "mybox", Host: "mybox.box.example.org", Domain: "box.example.org",
		Apps: map[string]App{"jellyfin": {Port: "8096", Scheme: "http"}}}); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, f.state)
	f.mgmt.fail = 2
	start := time.Now()
	f.run(t)
	eventually(t, "the host registered", func() bool { return f.mgmt.count(putJelly) == 1 })
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("registered after %v", d)
	}
	if !bytes.Equal(mustRead(t, f.state), before) {
		t.Fatal("share.json changed")
	}
}

func TestStatusProbesTheRelayOncePerMinute(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t,
		reply{status: 404, body: "404 page not found\n"},
		reply{status: 403, body: `{"error":"box key not linked"}`},
		reply{status: 404, body: `{"error":"no name"}`},
		reply{err: errors.New("unreachable")})
	for range 2 {
		if st := f.s.Status(ctx); st.Supported || st.Reason != "relay" {
			t.Fatalf("%+v", st)
		}
	}
	if len(*f.relay) != 1 || (*f.relay)[0] != (sent{"GET", "/v1/share", ""}) {
		t.Fatalf("relay calls %+v", *f.relay)
	}
	f.advance(61 * time.Second)
	if st := f.s.Status(ctx); st.Supported || st.Reason != "master" {
		t.Fatalf("%+v", st)
	}
	for range 2 { // no name is an answer; then an unreachable relay changes nothing
		f.advance(61 * time.Second)
		if st := f.s.Status(ctx); !st.Supported || st.Reason != "" {
			t.Fatalf("%+v", st)
		}
	}
	if len(*f.relay) != 4 {
		t.Fatalf("relay calls %+v", *f.relay)
	}
	os.Remove(filepath.Join(f.runtime, "share.addr"))
	if st := f.s.Status(ctx); st.Supported || st.Reason != "gateway" {
		t.Fatalf("%+v", st)
	}
}

func TestTunnelRunsOnlyWhileSharing(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, claim)
	f.named(t)
	f.run(t)
	time.Sleep(300 * time.Millisecond)
	if n := f.tunnel.Upgrades(); n != 0 {
		t.Fatalf("%d dials with no app shared", n)
	}
	start := time.Now()
	f.share(t, "jellyfin")
	f.tunnel.Next()
	first := time.Now()
	if d := first.Sub(start); d > 3*time.Second {
		t.Errorf("dialled after %v", d)
	}
	eventually(t, "connected", func() bool { return f.s.Status(ctx).Tunnel.Connected })

	f.tunnel.Drop() // redialled, but never within a second of the last dial
	y := f.tunnel.Next()
	if d := time.Since(first); d < 900*time.Millisecond {
		t.Errorf("redialled after %v", d)
	}

	if err := f.s.StopAll(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the tunnel closed", y.IsClosed)
	eventually(t, "disconnected", func() bool { return !f.s.Status(ctx).Tunnel.Connected })
	if f.saved(t).Name != "mybox" {
		t.Fatal("StopAll dropped the name")
	}
}

func TestRevokedCloseCodeStopsTheTunnel(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, claim)
	f.named(t)
	f.run(t)
	f.share(t, "jellyfin")
	f.tunnel.Next()
	f.tunnel.CloseWith(4403)
	eventually(t, "revoked", func() bool {
		st := f.s.Status(ctx)
		return !st.Tunnel.Connected && strings.Contains(st.Tunnel.Error, "revoked by the relay operator")
	})
	time.Sleep(1500 * time.Millisecond) // past the 1 s spacing and the test's backoff: any redial would be there
	if n := f.tunnel.Upgrades(); n != 1 {
		t.Fatalf("%d dials after a revocation", n)
	}
	f.share(t, "jellyfin") // the owner acts
	f.tunnel.Next()
}

func TestSetNameReclaimsWhenTheRelayForgotTheName(t *testing.T) {
	ctx := context.Background()
	other := reply{status: 201, body: `{"name":"other","host":"other.box.example.org","domain":"box.example.org"}`}
	f := newFixture(t, claim, reply{status: 404, body: `{"error":"no name"}`}, other)
	f.named(t)
	f.run(t)
	f.share(t, "jellyfin")
	y := f.tunnel.Next()
	if err := f.s.SetName(ctx, "other", true); err != nil {
		t.Fatal(err)
	}
	if len(*f.relay) != 3 || (*f.relay)[1] != (sent{"GET", "/v1/share", ""}) || (*f.relay)[2] != (sent{"PUT", "/v1/share/name", `{"name":"other"}`}) {
		t.Fatalf("relay calls %+v", *f.relay)
	}
	if st := f.saved(t); st.Name != "other" || len(st.Apps) != 0 || f.mgmt.count(dropJelly) != 1 {
		t.Fatalf("state %+v, gateway %+v", st, f.mgmt.seen())
	}
	eventually(t, "the tunnel closed", y.IsClosed)

	// the relay still knows it, or does not answer: the name stays
	for _, rp := range []reply{{status: 200, body: infoBody}, {err: errors.New("unreachable")}, {status: 502}} {
		f := newFixture(t, claim, rp)
		f.named(t)
		if err := f.s.SetName(ctx, "other", true); !errors.Is(err, ErrNameSet) || len(*f.relay) != 2 || f.saved(t).Name != "mybox" {
			t.Fatalf("%+v: %v, relay calls %+v", rp, err, *f.relay)
		}
	}
}

func TestResetDeletesTheHostsBeforeTheRelay(t *testing.T) {
	f := newFixture(t, claim)
	f.compose.set("nextcloud", "7580", "https")
	f.named(t)
	f.share(t, "jellyfin")
	f.share(t, "nextcloud")
	f.s.Relay.Signed = func(ctx context.Context, _, _ string, _ []byte) (int, []byte, time.Duration, error) {
		<-ctx.Done() // a relay that does not answer
		return 0, nil, 0, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	f.s.Reset(ctx)
	if f.mgmt.count(dropJelly) != 1 || f.mgmt.count(sent{"DELETE", "/v1/gateway/hosts/nextcloud.mybox.box.example.org", ""}) != 1 {
		t.Fatalf("gateway %+v", f.mgmt.seen())
	}
	if _, err := os.Stat(f.state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("share.json: %v", err)
	}
}

func TestAShareEndsTheTunnelBackoff(t *testing.T) {
	f := newFixture(t, claim)
	var dials atomic.Int32
	f.tunnel = newFakeTunnelRelay(t, func(w http.ResponseWriter, r *http.Request) bool {
		if dials.Add(1) == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return true
		}
		return false
	})
	f.s.RelayURL = func() (string, bool) { return f.tunnel.URL, true }
	f.s.backoffMin = 20 * time.Second
	f.named(t)
	f.run(t)
	f.share(t, "jellyfin")
	eventually(t, "the first dial refused", func() bool { f.s.mu.Lock(); defer f.s.mu.Unlock(); return f.s.tunnelErr != "" })
	start := time.Now()
	f.share(t, "jellyfin") // the owner acts again
	f.tunnel.Next()
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("dialled after %v", d)
	}
}

func TestABadPortOrSchemeIsNotShared(t *testing.T) {
	f := newFixture(t, claim)
	f.compose.set("badport", "80a", "http")
	f.compose.set("bigport", "70000", "http")
	f.compose.set("badscheme", "8080", "ftp")
	f.named(t)
	for _, app := range []string{"badport", "bigport", "badscheme"} {
		if err := f.s.Share(context.Background(), app); !errors.Is(err, ErrNoPort) {
			t.Fatalf("%s: %v", app, err)
		}
	}
	if f.mgmt.total() != 0 {
		t.Fatalf("gateway %+v", f.mgmt.seen())
	}
}

const (
	jellyHost = "jellyfin.mybox.box.example.org"
	nextHost  = "nextcloud.mybox.box.example.org"
)

var (
	putNext  = sent{"PUT", "/v1/gateway/hosts", `{"host":"nextcloud.mybox.box.example.org","target":"https://127.0.0.1:7580"}`}
	dropNext = sent{"DELETE", "/v1/gateway/hosts/nextcloud.mybox.box.example.org", ""}
)

// twoShared is a fixture whose share.json holds jellyfin and nextcloud, both installed, with guard as share-guard.json (nil: none).
func twoShared(t *testing.T, guard *GuardState, replies ...reply) *fixture {
	t.Helper()
	f := newFixture(t, replies...)
	f.compose.set("nextcloud", "7580", "https")
	if err := saveState(f.state, State{Name: "mybox", Host: "mybox.box.example.org", Domain: "box.example.org",
		Apps: map[string]App{"jellyfin": {Port: "8096", Scheme: "http"}, "nextcloud": {Port: "7580", Scheme: "https"}}}); err != nil {
		t.Fatal(err)
	}
	if guard != nil {
		if err := saveGuard(f.s.guardPath(), *guard); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// jellyCode is a guard state where jellyfin asks for a code and nextcloud is open.
func jellyCode() *GuardState {
	return &GuardState{Secret: make([]byte, 32), Box: DefaultPolicy(), Apps: map[string]GuardApp{"jellyfin": {Access: "code"}}}
}

func TestResyncPushesGuardBeforeHosts(t *testing.T) {
	f := twoShared(t, nil)
	if err := f.s.resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.mgmt.order()
	if len(got) != 3 || got[0] != "PUT /v1/gateway/guard" || got[1] != "PUT /v1/gateway/hosts" || got[2] != "PUT /v1/gateway/hosts" ||
		f.mgmt.count(putJelly) != 1 || f.mgmt.count(putNext) != 1 {
		t.Fatalf("gateway %v", got)
	}
}

func TestShareAppPushesGuardFirst(t *testing.T) {
	f := newFixture(t, claim)
	f.named(t)
	f.share(t, "jellyfin")
	got := f.mgmt.seen()
	if len(got) != 2 || got[0].method != "PUT" || got[0].path != "/v1/gateway/guard" || got[1] != putJelly {
		t.Fatalf("gateway %+v", got)
	}
	if a, ok := f.mgmt.pushes(t)[0].Apps[jellyHost]; !ok || a.App != "jellyfin" || a.Access != "open" {
		t.Fatalf("pushed %+v", f.mgmt.pushes(t))
	}
}

func TestUnshareDropsAppFromPeople(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, claim)
	f.compose.set("nextcloud", "7580", "https")
	f.named(t)
	f.share(t, "jellyfin")
	f.share(t, "nextcloud")
	f.s.mu.Lock()
	g, _ := f.s.guardLocked()
	g.Apps["jellyfin"] = GuardApp{Access: "code"}
	g.People = []Person{{ID: "p_1", Name: "Maman", Gen: 1, CodeHash: strings.Repeat("a", 64), Apps: []string{"jellyfin", "nextcloud"}}}
	f.s.mu.Unlock()
	f.mgmt.forget()
	if err := f.s.Unshare(ctx, "jellyfin"); err != nil {
		t.Fatal(err)
	}
	if got := f.mgmt.order(); len(got) != 2 || got[0] != "PUT /v1/gateway/guard" || f.mgmt.count(dropJelly) != 1 {
		t.Fatalf("gateway %v", got)
	}
	// pushed before the delete: nobody may enter jellyfin any more, and its host is still held by its code
	pushed := f.mgmt.pushes(t)[0]
	if !slices.Equal(pushed.People[0].Apps, []string{"nextcloud"}) || pushed.Apps[jellyHost].Access != "code" {
		t.Fatalf("pushed %+v", pushed)
	}
	saved, corrupt := loadGuard(f.s.guardPath())
	if _, ok := saved.Apps["jellyfin"]; corrupt || ok || len(saved.People) != 1 || !slices.Equal(saved.People[0].Apps, []string{"nextcloud"}) {
		t.Fatalf("saved %+v", saved)
	}
	if _, ok := f.saved(t).Apps["jellyfin"]; ok {
		t.Fatal("still shared")
	}
}

func TestUpgradedBoxKeepsAppsOpen(t *testing.T) {
	f := twoShared(t, nil)
	if err := f.s.resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	pushed := f.mgmt.pushes(t)
	if len(pushed) != 1 || pushed[0].Protection || len(pushed[0].Apps) != 2 || len(pushed[0].Secret) != 32 {
		t.Fatalf("pushed %+v", pushed)
	}
	for host, a := range pushed[0].Apps {
		if a.Access != "open" || a.Policy.WAF != "off" {
			t.Fatalf("%s: %+v", host, a)
		}
	}
	if f.s.GuardReset() {
		t.Fatal("no guard file is not a reset")
	}
	// the secret is kept from the first start on
	if saved, corrupt := loadGuard(f.s.guardPath()); corrupt || !bytes.Equal(saved.Secret, pushed[0].Secret) {
		t.Fatalf("saved %+v", saved)
	}
}

func TestOldGatewayStillServesHosts(t *testing.T) {
	f := twoShared(t, nil)
	f.mgmt.setGuard(http.StatusNotFound)
	if err := f.s.resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.mgmt.count(putJelly) != 1 || f.mgmt.count(putNext) != 1 {
		t.Fatalf("gateway %+v", f.mgmt.seen())
	}
	if _, ok, err := f.s.Gateway.GuardStatus(context.Background()); ok || err != nil {
		t.Fatalf("supported %v, %v", ok, err)
	}
	// a share goes through too
	f2 := newFixture(t, claim)
	f2.mgmt.setGuard(http.StatusNotFound)
	f2.named(t)
	f2.share(t, "jellyfin")
	if f2.mgmt.count(putJelly) != 1 {
		t.Fatalf("gateway %+v", f2.mgmt.seen())
	}

	// back on an old Gateway with a code app (a downgrade): its host stays off, the open one is served
	f3 := twoShared(t, jellyCode())
	if err := f3.s.resync(context.Background()); err != nil { // the code host registered under a guard
		t.Fatal(err)
	}
	f3.mgmt.setGuard(http.StatusNotFound)
	f3.mgmt.forget()
	if err := f3.s.resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f3.mgmt.count(putJelly) != 0 || f3.mgmt.count(dropJelly) != 1 || f3.mgmt.count(putNext) != 1 {
		t.Fatalf("gateway %+v", f3.mgmt.seen())
	}
	f3.mgmt.forget()
	// sharing the code app again is refused: the old Gateway cannot ask for its code
	if err := f3.s.Share(context.Background(), "jellyfin"); !errors.Is(err, ErrGatewayNoGuard) || f3.mgmt.count(putJelly) != 0 {
		t.Fatalf("share: %v, gateway %+v", err, f3.mgmt.seen())
	}
	if err := f3.s.Unshare(context.Background(), "jellyfin"); err != nil {
		t.Fatal(err)
	}
	f3.s.mu.Lock()
	g, _ := f3.s.guardLocked()
	g.Apps["jellyfin"] = GuardApp{Access: "code"} // a stale entry of the app, no longer shared
	f3.s.mu.Unlock()
	// shared anew, the app starts open: the old Gateway serves it
	if err := f3.s.Share(context.Background(), "jellyfin"); err != nil || f3.mgmt.count(putJelly) != 1 {
		t.Fatalf("share: %v, gateway %+v", err, f3.mgmt.seen())
	}
}

func TestFailedGuardPushHoldsCodeHosts(t *testing.T) {
	ctx := context.Background()
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError} {
		f := twoShared(t, jellyCode())
		f.mgmt.setGuard(status)
		if err := f.s.resync(ctx); err == nil {
			t.Fatalf("%d: no error", status)
		}
		if f.mgmt.count(putJelly) != 0 || f.mgmt.count(dropJelly) != 1 || f.mgmt.count(putNext) != 1 || f.mgmt.count(dropNext) != 0 {
			t.Fatalf("%d: gateway %+v", status, f.mgmt.seen())
		}

		f.mgmt.setGuard(0)
		f.mgmt.forget()
		if err := f.s.resync(ctx); err != nil {
			t.Fatal(err)
		}
		if got := f.mgmt.order(); len(got) != 3 || got[0] != "PUT /v1/gateway/guard" || f.mgmt.count(putJelly) != 1 {
			t.Fatalf("%d: gateway %v", status, got)
		}

		// a code host already registered is taken off again
		f.mgmt.setGuard(status)
		f.mgmt.forget()
		if err := f.s.resync(ctx); err == nil || f.mgmt.count(dropJelly) != 1 || f.mgmt.count(putJelly) != 0 || f.mgmt.count(putNext) != 1 {
			t.Fatalf("%d: %v, gateway %+v", status, err, f.mgmt.seen())
		}
		// a share is refused, rather than registered without its config
		f.compose.set("other", "8080", "http")
		f.mgmt.forget()
		if err := f.s.Share(ctx, "other"); err == nil || f.mgmt.count(sent{"PUT", "/v1/gateway/hosts", `{"host":"other.mybox.box.example.org","target":"http://127.0.0.1:8080"}`}) != 0 {
			t.Fatalf("%d: %v, gateway %+v", status, err, f.mgmt.seen())
		}
	}
}

func TestEveryHostInGuardApps(t *testing.T) {
	f := twoShared(t, jellyCode())
	if err := f.s.resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	apps := f.mgmt.pushes(t)[0].Apps
	if len(apps) != 2 || apps[jellyHost].Access != "code" || apps[nextHost].Access != "open" || apps[nextHost].App != "nextcloud" {
		t.Fatalf("apps %+v", apps)
	}
}

func TestBuildGatewayGuardHosts(t *testing.T) {
	f := newFixture(t, claim)
	f.compose.set("My App", "8080", "http")
	f.named(t)
	f.share(t, "My App")
	var registered struct{ Host string }
	for _, c := range f.mgmt.seen() {
		if c.path == "/v1/gateway/hosts" {
			json.Unmarshal([]byte(c.body), &registered)
		}
	}
	if _, ok := f.mgmt.pushes(t)[0].Apps[registered.Host]; !ok || registered.Host != "my-app.mybox.box.example.org" {
		t.Fatalf("registered %q, pushed %+v", registered.Host, f.mgmt.pushes(t))
	}

	// the policy is the effective one; a person keeps only the apps that are shared
	waf := "block"
	g := GuardState{Secret: make([]byte, 32), Protection: true, Box: DefaultPolicy(),
		Apps:   map[string]GuardApp{"My App": {Access: "code", Override: &PolicyOverride{WAF: &waf, Allow: []string{"192.0.2.0/24"}}}, "gone": {Access: "code"}},
		People: []Person{{ID: "p_1", Name: "Maman", Gen: 2, CodeHash: strings.Repeat("b", 64), Apps: []string{"gone", "My App"}}}}
	cfg := buildGatewayGuard(State{Host: "MyBox.box.example.org.", Apps: map[string]App{"My App": {}}}, g)
	a, ok := cfg.Apps["my-app.mybox.box.example.org"]
	if !ok || len(cfg.Apps) != 1 || a.Access != "code" || a.App != "My App" || a.Policy.WAF != "block" ||
		!slices.Equal(a.Policy.Allow, []string{"192.0.2.0/24"}) || !cfg.Protection ||
		!reflect.DeepEqual(cfg.People, []GatewayPerson{{ID: "p_1", Gen: 2, CodeHash: strings.Repeat("b", 64), Apps: []string{"My App"}}}) {
		t.Fatalf("cfg %+v", cfg)
	}
}

func TestCorruptGuardClosesSharedApps(t *testing.T) {
	f := twoShared(t, nil)
	if err := os.MkdirAll(filepath.Dir(f.s.guardPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.s.guardPath(), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.s.resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	for host, a := range f.mgmt.pushes(t)[0].Apps {
		if a.Access != "code" {
			t.Fatalf("%s: %+v", host, a)
		}
	}
	if !f.s.GuardReset() {
		t.Fatal("not reported")
	}
	saved, corrupt := loadGuard(f.s.guardPath())
	if corrupt || saved.Apps["jellyfin"].Access != "code" || saved.Apps["nextcloud"].Access != "code" {
		t.Fatalf("saved %+v", saved)
	}
}

func TestUnsavedGuardResetHoldsEveryHost(t *testing.T) {
	ctx := context.Background()
	f := twoShared(t, jellyCode())
	if err := f.s.resync(ctx); err != nil { // both served: nextcloud open, jellyfin with its code
		t.Fatal(err)
	}
	f.s.guard = nil // the core restarts and finds the file unusable
	path := f.s.guardPath()
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	saveGuardFile = func(string, GuardState) error { return errors.New("disk full") }
	t.Cleanup(func() { saveGuardFile = saveGuard })
	f.mgmt.forget()
	if err := f.s.resync(ctx); err == nil {
		t.Fatal("no error")
	}
	if len(f.mgmt.pushes(t)) != 0 || len(f.mgmt.registered()) != 0 || f.mgmt.count(putJelly)+f.mgmt.count(putNext) != 0 {
		t.Fatalf("gateway %+v, serving %v", f.mgmt.seen(), f.mgmt.registered())
	}
	if raw, _ := os.ReadFile(path); string(raw) != "{nope" {
		t.Fatalf("file %q", raw)
	}
	if aside, _ := filepath.Glob(path + ".corrupt-*"); len(aside) != 0 {
		t.Fatalf("aside %v", aside)
	}

	// the next start can write: the same reset, saved, and the bytes kept aside
	saveGuardFile = saveGuard
	s2 := New(f.s.Root)
	s2.RuntimePath = f.s.RuntimePath
	if err := s2.resync(ctx); err != nil {
		t.Fatal(err)
	}
	pushed := f.mgmt.pushes(t)
	if len(pushed) != 1 || pushed[0].Apps[jellyHost].Access != "code" || pushed[0].Apps[nextHost].Access != "code" || !s2.GuardReset() {
		t.Fatalf("pushed %+v", pushed)
	}
	if saved, corrupt := loadGuard(path); corrupt || saved.Apps["nextcloud"].Access != "code" {
		t.Fatalf("saved %+v", saved)
	}
	aside, _ := filepath.Glob(path + ".corrupt-*")
	if len(aside) != 1 {
		t.Fatalf("aside %v", aside)
	}
	if raw, _ := os.ReadFile(aside[0]); string(raw) != "{nope" {
		t.Fatalf("aside %q", raw)
	}
}

func TestResyncDropsStrayHosts(t *testing.T) {
	f := twoShared(t, nil)
	f.mgmt.mu.Lock()
	f.mgmt.hosts = map[string]string{
		"old.mybox.box.example.org":   "http://127.0.0.1:1",  // left by a best-effort delete
		"app.oldname.box.example.org": "http://127.0.0.1:2",  // left under a former name
		"casaos.local":                "http://127.0.0.1:80", // not the share's
	}
	f.mgmt.mu.Unlock()
	if err := f.s.resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.mgmt.registered()
	if len(got) != 3 || got["casaos.local"] == "" || got[jellyHost] == "" || got[nextHost] == "" {
		t.Fatalf("serving %v", got)
	}
	// deleted before the push: never served while missing from the config
	if order := f.mgmt.order(); len(order) != 5 || order[2] != "PUT /v1/gateway/guard" {
		t.Fatalf("gateway %v", order)
	}
}

func TestUnreadableGuardHoldsEveryHostAndKeepsTheFile(t *testing.T) {
	ctx := context.Background()
	g := jellyCode()
	g.People = []Person{{ID: "p_1", Name: "Maman", Gen: 1, CodeHash: strings.Repeat("a", 64), Apps: []string{"jellyfin"}}}
	f := twoShared(t, g)
	path := f.s.guardPath()
	before := mustRead(t, path)
	readGuardFile = func(string) ([]byte, error) { return nil, syscall.EIO }
	t.Cleanup(func() { readGuardFile = os.ReadFile })
	if err := f.s.resync(ctx); err == nil {
		t.Fatal("no error")
	}
	if len(f.mgmt.pushes(t)) != 0 || len(f.mgmt.registered()) != 0 || f.s.GuardReset() {
		t.Fatalf("gateway %+v, serving %v", f.mgmt.seen(), f.mgmt.registered())
	}
	if aside, _ := filepath.Glob(path + ".corrupt-*"); len(aside) != 0 || !bytes.Equal(mustRead(t, path), before) {
		t.Fatalf("file changed, aside %v", aside)
	}

	// the read works again: the owner's settings, people included
	readGuardFile = os.ReadFile
	if err := f.s.resync(ctx); err != nil {
		t.Fatal(err)
	}
	pushed := f.mgmt.pushes(t)
	if len(pushed) != 1 || pushed[0].Apps[jellyHost].Access != "code" || pushed[0].Apps[nextHost].Access != "open" ||
		len(pushed[0].People) != 1 || !slices.Equal(pushed[0].People[0].Apps, []string{"jellyfin"}) || len(f.mgmt.registered()) != 2 {
		t.Fatalf("pushed %+v, serving %v", pushed, f.mgmt.registered())
	}
	if saved, corrupt := loadGuard(path); corrupt || len(saved.People) != 1 {
		t.Fatalf("saved %+v", saved)
	}
}

type alertLog struct {
	mu  sync.Mutex
	got []string
}

func (a *alertLog) hook(key, sentence string, problem bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, fmt.Sprintf("%s|%v|%s", key, problem, sentence))
}

func (a *alertLog) take() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.got
	a.got = nil
	return out
}

func (f *fixture) setStatus(body string) { f.mgmt.mu.Lock(); f.mgmt.status = body; f.mgmt.mu.Unlock() }

func TestResyncRaisesWave(t *testing.T) {
	f := twoShared(t, jellyCode())
	log := &alertLog{}
	f.s.Alert = log.hook
	f.setStatus(`{"version":1,"configured":true,"geo":{"country":true,"asn":true},"waves":[{"app":"jellyfin","bans":23}]}`)
	if err := f.s.resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "share:wave:jellyfin|true|The shared app jellyfin is under attack: 23 addresses were banned in 10 minutes."
	if got := log.take(); !slices.Contains(got, want) {
		t.Fatalf("%q", got)
	}
	f.setStatus(`{"version":1,"configured":true,"geo":{"country":true,"asn":true},"waves":[]}`)
	if err := f.s.resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	want = "share:wave:jellyfin|false|Resolved: the attack on the shared app jellyfin has calmed down."
	if got := log.take(); !slices.Contains(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestGeoAlertOnlyWhenNeeded(t *testing.T) {
	const noCountry = `{"version":1,"configured":true,"geo":{"country":false,"asn":false},"waves":[]}`
	raised := func(log *alertLog) bool {
		for _, a := range log.take() {
			if strings.HasPrefix(a, "share:geo|true|Country filtering of shared apps is not working: the country database could not be downloaded.") {
				return true
			}
		}
		return false
	}
	g := jellyCode()
	g.Protection = true
	f := twoShared(t, g)
	log := &alertLog{}
	f.s.Alert = log.hook
	f.setStatus(noCountry)
	if err := f.s.resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if raised(log) {
		t.Fatal("raised with countries none")
	}
	g.Box.Countries = Countries{Mode: "block", List: []string{"RU"}}
	g.Protection = false
	f1 := twoShared(t, g)
	f1.s.Alert = log.hook
	f1.setStatus(noCountry)
	if err := f1.s.resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if raised(log) {
		t.Fatal("raised with protection off")
	}
	g.Protection = true
	f2 := twoShared(t, g)
	f2.s.Alert = log.hook
	f2.setStatus(noCountry)
	if err := f2.s.resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !raised(log) {
		t.Fatal("not raised")
	}
	f2.setStatus(`{"version":1,"configured":true,"geo":{"country":true,"asn":false},"waves":[]}`)
	if err := f2.s.resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := log.take(); !slices.Contains(got, "share:geo|false|Resolved: country filtering of shared apps works again.") {
		t.Fatalf("%q", got)
	}
}

func TestGuardResetAlertRaisedThenResolvedByTheNextWrite(t *testing.T) {
	f := twoShared(t, nil)
	if err := os.MkdirAll(filepath.Dir(f.s.guardPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.s.guardPath(), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := &alertLog{}
	f.s.Alert = log.hook
	f.setStatus(`{"version":1,"configured":true,"geo":{"country":true,"asn":true},"waves":[]}`)
	if err := f.s.resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	raise := "share:guard-reset|true|The protection settings of shared apps could not be read: every shared app now asks for an access code. Check the people and the apps in Remote access."
	if got := log.take(); !slices.Contains(got, raise) {
		t.Fatalf("%q", got)
	}
	if _, err := f.s.SetProtection(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := log.take(); !slices.Contains(got, "share:guard-reset|false|Resolved: the protection settings of shared apps were saved again.") || f.s.GuardReset() {
		t.Fatalf("%q", got)
	}
	if saved, _ := loadGuard(f.s.guardPath()); !saved.ResetAt.IsZero() {
		t.Fatalf("saved %+v", saved)
	}
}

const guardResetRaised = "share:guard-reset|true|" + guardResetSentence

// corruptGuard puts an unusable share-guard.json under root.
func corruptGuard(t *testing.T, s *Service) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(s.guardPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.guardPath(), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGuardResetSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	f := twoShared(t, nil)
	corruptGuard(t, f.s)
	if err := f.s.resync(ctx); err != nil {
		t.Fatal(err)
	}
	s2 := New(f.s.Root) // the core restarts: the file now reads fine
	s2.RuntimePath = f.s.RuntimePath
	log := &alertLog{}
	s2.Alert = log.hook
	if v, err := s2.GuardView(ctx); err != nil || !v.Reset {
		t.Fatalf("%+v %v", v, err)
	}
	if err := s2.resync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := log.take(); !slices.Contains(got, guardResetRaised) {
		t.Fatalf("%q", got)
	}

	// with no app shared: raised at the reset, and again after a restart
	s3 := New(t.TempDir())
	corruptGuard(t, s3)
	s3.Alert = log.hook
	if !s3.GuardReset() {
		t.Fatal("not reset")
	}
	if got := log.take(); !slices.Contains(got, guardResetRaised) {
		t.Fatalf("at the reset: %q", got)
	}
	s4 := New(s3.Root)
	s4.Alert = log.hook
	if err := s4.resync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := log.take(); !slices.Contains(got, guardResetRaised) {
		t.Fatalf("after a restart: %q", got)
	}
}

func TestGuardResetKeptUntilAPushIsTaken(t *testing.T) {
	ctx := context.Background()
	f := twoShared(t, nil)
	corruptGuard(t, f.s)
	if err := f.s.resync(ctx); err != nil {
		t.Fatal(err)
	}
	log := &alertLog{}
	f.s.Alert = log.hook
	f.mgmt.mu.Lock()
	f.mgmt.putGuard = http.StatusInternalServerError
	f.mgmt.mu.Unlock()
	if _, err := f.s.SetProtection(ctx, true); !errors.Is(err, ErrGuardPush) {
		t.Fatal(err)
	}
	if saved, _ := loadGuard(f.s.guardPath()); saved.ResetAt.IsZero() || !saved.Protection || !f.s.GuardReset() {
		t.Fatalf("saved %+v", saved)
	}
	if got := log.take(); slices.ContainsFunc(got, func(a string) bool { return strings.HasPrefix(a, "share:guard-reset|false|") }) {
		t.Fatalf("resolved: %q", got)
	}
	f.mgmt.mu.Lock()
	f.mgmt.putGuard = 0
	f.mgmt.mu.Unlock()
	v, err := f.s.SetProtection(ctx, true)
	if err != nil || v.Reset {
		t.Fatalf("%+v %v", v, err)
	}
	if saved, _ := loadGuard(f.s.guardPath()); !saved.ResetAt.IsZero() {
		t.Fatalf("saved %+v", saved)
	}
	if got := log.take(); !slices.Contains(got, "share:guard-reset|false|Resolved: the protection settings of shared apps were saved again.") {
		t.Fatalf("%q", got)
	}
}

func TestResyncHoldsCodeHostsBeforeTheAppList(t *testing.T) {
	ctx := context.Background()
	f := twoShared(t, jellyCode())
	if err := f.s.resync(ctx); err != nil {
		t.Fatal(err)
	}
	// downgraded to an old Gateway while AppManagement still loads
	f.mgmt.setGuard(http.StatusNotFound)
	f.compose.remove("jellyfin")
	f.compose.remove("nextcloud")
	f.mgmt.forget()
	if err := f.s.resync(ctx); err == nil {
		t.Fatal("no error")
	}
	if f.mgmt.count(dropJelly) != 1 || f.mgmt.count(dropNext) != 0 {
		t.Fatalf("gateway %+v", f.mgmt.seen())
	}
}

func TestReshareStartsOpenWithNobody(t *testing.T) {
	ctx := context.Background()
	f := twoShared(t, nil)
	f.s.mu.Lock()
	g, _ := f.s.guardLocked()
	// stale entries: the app is not shared, but its settings and a person's right to it survived
	g.Apps["other"] = GuardApp{Access: "code"}
	g.People = []Person{{ID: "p_1", Name: "Maman", Gen: 1, CodeHash: strings.Repeat("a", 64), Apps: []string{"other", "jellyfin"}}}
	f.s.mu.Unlock()
	f.compose.set("other", "8080", "http")
	if err := f.s.Share(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	pushed := f.mgmt.pushes(t)
	if a := pushed[0].Apps["other.mybox.box.example.org"]; a.Access != "open" || !slices.Equal(pushed[0].People[0].Apps, []string{"jellyfin"}) {
		t.Fatalf("pushed %+v", pushed[0])
	}
	saved, _ := loadGuard(f.s.guardPath())
	if _, ok := saved.Apps["other"]; ok || !slices.Equal(saved.People[0].Apps, []string{"jellyfin"}) {
		t.Fatalf("saved %+v", saved)
	}
}

func TestReshareRefusedWhenTheGuardCannotBeSaved(t *testing.T) {
	f := twoShared(t, nil)
	f.s.mu.Lock()
	g, _ := f.s.guardLocked()
	g.Apps["other"] = GuardApp{Access: "code"}
	f.s.mu.Unlock()
	f.compose.set("other", "8080", "http")
	saveGuardFile = func(string, GuardState) error { return errors.New("disk full") }
	t.Cleanup(func() { saveGuardFile = saveGuard })
	if err := f.s.Share(context.Background(), "other"); err == nil || len(f.mgmt.pushes(t)) != 0 {
		t.Fatalf("%v, gateway %+v", err, f.mgmt.seen())
	}
}

func TestPushRejectedWith400RollsBack(t *testing.T) {
	f := twoShared(t, jellyCode())
	f.mgmt.mu.Lock()
	f.mgmt.putGuard = http.StatusBadRequest
	f.mgmt.putMsg = `invalid guard config: apps["jellyfin.mybox.box.example.org"].policy.waf: "nope", want "off", "observe" or "block"`
	f.mgmt.mu.Unlock()
	_, err := f.s.SetProtection(context.Background(), true)
	wantGuardErr(t, err, `bad_policy:apps["jellyfin.mybox.box.example.org"].policy.waf`)
	if saved, _ := loadGuard(f.s.guardPath()); saved.Protection {
		t.Fatalf("saved %+v", saved)
	}
	f.s.mu.Lock()
	on := f.s.guard.Protection
	f.s.mu.Unlock()
	if on || len(f.s.resyncCh) != 1 { // the resync brings back the code host the failed push took off
		t.Fatalf("memory %v, woken %d", on, len(f.s.resyncCh))
	}
}

func TestGuardWriteDropsStrayHosts(t *testing.T) {
	f := twoShared(t, nil)
	f.mgmt.mu.Lock()
	f.mgmt.hosts = map[string]string{"old.mybox.box.example.org": "http://127.0.0.1:1"}
	f.mgmt.mu.Unlock()
	if _, err := f.s.SetProtection(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	// after the status check, the stray goes before the push
	if order := f.mgmt.order(); len(order) < 3 || order[1] != "DELETE /v1/gateway/hosts/old.mybox.box.example.org" || order[2] != "PUT /v1/gateway/guard" {
		t.Fatalf("gateway %v", order)
	}
}

func TestCorruptGuardNotOverwrittenWithoutItsCopy(t *testing.T) {
	ctx := context.Background()
	f := twoShared(t, jellyCode())
	if err := f.s.resync(ctx); err != nil {
		t.Fatal(err)
	}
	f.s.guard = nil // the core restarts and finds the file unusable
	path := f.s.guardPath()
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	copyGuardFile = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
	t.Cleanup(func() { copyGuardFile = os.WriteFile })
	f.mgmt.forget()
	if err := f.s.resync(ctx); err == nil {
		t.Fatal("no error")
	}
	if raw, _ := os.ReadFile(path); string(raw) != "{nope" {
		t.Fatalf("file %q", raw)
	}
	if len(f.mgmt.pushes(t)) != 0 || len(f.mgmt.registered()) != 0 || f.s.GuardReset() {
		t.Fatalf("gateway %+v, serving %v", f.mgmt.seen(), f.mgmt.registered())
	}
	copyGuardFile = os.WriteFile
	if err := f.s.resync(ctx); err != nil {
		t.Fatal(err)
	}
	if aside, _ := filepath.Glob(path + ".corrupt-*"); len(aside) != 1 || !f.s.GuardReset() {
		t.Fatalf("aside %v", aside)
	}
}

func TestNeverSharedBoxWritesNoGuardFile(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	s.Geo.BaseURL = "http://127.0.0.1:1"
	s.geoStep(ctx)
	s.GuardView(ctx)
	s.resync(ctx)
	if s.GuardReset() {
		t.Fatal("reset")
	}
	if _, err := os.Stat(s.guardPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("share-guard.json: %v", err)
	}
	// the box gets a name: the secret held so far is the one saved
	s.mu.Lock()
	secret := slices.Clone(s.guard.Secret)
	s.mu.Unlock()
	if err := saveState(s.statePath(), State{Name: "mybox", Host: "mybox.box.example.org"}); err != nil {
		t.Fatal(err)
	}
	s.GuardReset()
	if saved, corrupt := loadGuard(s.guardPath()); corrupt || !bytes.Equal(saved.Secret, secret) {
		t.Fatalf("saved %+v", saved)
	}
}
