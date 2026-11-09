package share

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeManagement is the Gateway's management API: it records the calls it answers with a success, and
// answers the next fail calls 502.
type fakeManagement struct {
	mu    sync.Mutex
	calls []sent
	fail  int
	onPut func() // when set, runs at each PUT before it is answered
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
	m.calls = append(m.calls, sent{r.Method, r.URL.Path, strings.TrimSpace(string(b))})
	if r.Method == http.MethodPut {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
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
	f := newFixture(t, claim)
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
	if err := f.s.SetName(ctx, "other", true); !errors.Is(err, ErrNameSet) || len(*f.relay) != 1 {
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
	if d := time.Since(start); d > time.Second {
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
	if d := first.Sub(start); d > time.Second {
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
