package fleet

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testSecret  = "fleet-secret-8f3a"
	testJoinKey = "join-key-77c1"
	testLogin   = "https://hs.relay.example"
)

// fakeNet stands in for the private network: State is before until Up, after
// once Up ran, and the zero state once Down ran. Every call is recorded.
type fakeNet struct {
	mu            sync.Mutex
	before, after NetState
	upErr         error
	stateErr      error // when set, what State fails with
	downErr       error
	states        int // how many times State was asked
	up            bool
	calls         []string
}

func (n *fakeNet) State(context.Context) (NetState, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.states++
	if n.stateErr != nil {
		return NetState{}, n.stateErr
	}
	if n.up {
		return n.after, nil
	}
	return n.before, nil
}

func (n *fakeNet) Up(_ context.Context, loginServer, authKey, hostname string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, "up "+loginServer+" "+authKey+" "+hostname)
	if n.upErr != nil {
		return n.upErr
	}
	n.up = true
	return nil
}

func (n *fakeNet) Down(context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, "down")
	n.up, n.before = false, NetState{}
	return n.downErr
}

func (n *fakeNet) called(prefix string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, c := range n.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// fakeRelay is a recasaos-relay with one fleet to hand out, counting each call.
type fakeRelay struct {
	*httptest.Server
	mu                     sync.Mutex
	creates, keys, deletes int
	keyStatus              int      // when set, what a join key request is answered
	loginServer            string   // what a fleet creation names as login server; the relay's own URL when empty
	removed                []string // the hostnames removed from the fleet
}

// set changes the fake's behaviour under its lock.
func (r *fakeRelay) set(change func(*fakeRelay)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change(r)
}

func (r *fakeRelay) count(n *int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *n
}

func newFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	r := &fakeRelay{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		authed := req.Header.Get("Authorization") == "Bearer "+testSecret
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/v1/fleets":
			r.creates++
			login := r.loginServer
			if login == "" {
				login = r.URL
			}
			jsonReply(w, http.StatusCreated, map[string]any{"fleet_id": "fl1", "secret": testSecret, "login_server": login, "expires_in_days_idle": 30})
		case req.Method == http.MethodPost && req.URL.Path == "/v1/fleets/fl1/keys" && authed:
			r.keys++
			if r.keyStatus != 0 {
				w.WriteHeader(r.keyStatus)
				return
			}
			jsonReply(w, http.StatusCreated, map[string]any{"key": testJoinKey, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case req.Method == http.MethodDelete && strings.HasPrefix(req.URL.Path, "/v1/fleets/fl1/nodes/") && authed:
			r.removed = append(r.removed, strings.TrimPrefix(req.URL.Path, "/v1/fleets/fl1/nodes/"))
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodDelete && req.URL.Path == "/v1/fleets/fl1" && authed:
			r.deletes++
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(r.Close)
	return r
}

// relayMaster is a box with the master role off, a fake private network that
// gives it 100.64.0.7 on the relay's login server, a fake relay, and its log kept.
func relayMaster(t *testing.T) (*Fleet, *fakeNet, *fakeRelay, func() []string) {
	t.Helper()
	f := New(t.TempDir())
	f.Gateway = func() (GatewayInfo, error) {
		return GatewayInfo{Public: mustURL(t, "http://127.0.0.1:80"), HTTPS: mustURL(t, "https://127.0.0.1:443")}, nil
	}
	f.Hostname = func() (string, error) { return "maison", nil }
	var mu sync.Mutex
	var logs []string
	f.Log = func(msg string, err error) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprint(msg, " ", err))
	}
	n := &fakeNet{after: NetState{Connected: true, Addr: "100.64.0.7", LoginServer: testLogin}}
	f.Net = n
	r := newFakeRelay(t)
	f.RelayAPI.HTTP = r.Client()
	return f, n, r, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), logs...) }
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func TestEnableMasterRelayStoresTailnetAddress(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	if err := f.EnableMasterRelay(context.Background(), r.URL+"/"); err != nil {
		t.Fatal(err)
	}
	st, ok := f.MasterStatus()
	if !ok || st.Address != "https://100.64.0.7" || st.Relay != r.URL || st.NetAddr != "100.64.0.7" {
		t.Fatalf("%+v %v", st, ok)
	}
	if !n.called("up " + r.URL + " " + testJoinKey + " maison") {
		t.Fatalf("calls: %v", n.calls)
	}
	if err := f.DisableMaster(); err != nil {
		t.Fatal(err)
	}
	if r.count(&r.deletes) != 1 || !n.called("down") || exists(t, f.relayPath()) {
		t.Fatalf("disable: deletes %d, calls %v", r.deletes, n.calls)
	}
}

func TestRelayAddressUsesGatewayHTTPSPort(t *testing.T) {
	for _, c := range []struct {
		info GatewayInfo
		addr string
		want string
	}{
		{GatewayInfo{Public: mustURL(t, "http://127.0.0.1:80"), HTTPS: mustURL(t, "https://127.0.0.1:443")}, "100.64.0.7", "https://100.64.0.7"},
		{GatewayInfo{Public: mustURL(t, "http://127.0.0.1:80"), HTTPS: mustURL(t, "https://127.0.0.1:8443")}, "100.64.0.7", "https://100.64.0.7:8443"},
		{GatewayInfo{Public: mustURL(t, "https://127.0.0.1:9443")}, "fd7a:115c:a1e0::1", "https://[fd7a:115c:a1e0::1]:9443"},
		{GatewayInfo{Public: mustURL(t, "http://127.0.0.1:80"), HTTPS: mustURL(t, "https://127.0.0.1:443")}, "fd7a:115c:a1e0::1", "https://[fd7a:115c:a1e0::1]"},
	} {
		port, err := httpsPort(c.info)
		if err != nil {
			t.Fatal(err)
		}
		if got := relayAddress(c.addr, port); got != c.want || ValidMasterURL(got) != nil {
			t.Fatalf("%s: got %q, want %q", c.addr, got, c.want)
		}
	}
	if _, err := httpsPort(GatewayInfo{Public: mustURL(t, "http://127.0.0.1:80")}); err == nil {
		t.Fatal("a gateway with no HTTPS cannot be a relay master")
	}
}

func TestDisableMasterRelayDownStillDisables(t *testing.T) {
	f, n, r, logs := relayMaster(t)
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if err := f.DisableMaster(); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.MasterStatus(); ok || exists(t, f.masterPath()) {
		t.Fatal("the master is still on")
	}
	if exists(t, f.relayPath()) || !n.called("down") {
		t.Fatalf("relay.json left or no Down: %v", n.calls)
	}
	if !strings.Contains(strings.Join(logs(), "\n"), "idle") {
		t.Fatalf("the log does not say the relay will clean up: %v", logs())
	}
}

func TestEnableMasterRelayKeepsSecretOutOfMasterJSON(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	master, err := os.ReadFile(f.masterPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(master), testSecret) || strings.Contains(string(master), testJoinKey) {
		t.Fatalf("master.json holds a secret: %s", master)
	}
	relay, err := os.ReadFile(f.relayPath())
	if err != nil || !strings.Contains(string(relay), testSecret) || !strings.Contains(string(relay), `"fl1"`) {
		t.Fatalf("relay.json: %s %v", relay, err)
	}
	if strings.Contains(string(relay), testJoinKey) {
		t.Fatal("the master's own join key is kept")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(f.relayPath()); fi.Mode().Perm() != 0o600 {
			t.Fatalf("relay.json is %v", fi.Mode().Perm())
		}
	}
}

func TestEnableMasterRelayRefusesAnotherNetwork(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	n.before = NetState{Connected: true, Addr: "100.100.1.1", LoginServer: "https://controlplane.tailscale.com"}
	if err := f.EnableMasterRelay(context.Background(), r.URL); !errors.Is(err, ErrOtherNetwork) {
		t.Fatalf("got %v", err)
	}
	if r.count(&r.creates) != 0 || n.called("up") || n.called("down") || exists(t, f.relayPath()) || exists(t, f.masterPath()) {
		t.Fatalf("something was done: creates %d, calls %v", r.creates, n.calls)
	}
	f.Net = nil
	if err := f.EnableMasterRelay(context.Background(), r.URL); !errors.Is(err, ErrNoPrivateNet) || r.count(&r.creates) != 0 {
		t.Fatalf("got %v", err)
	}
	if err := f.EnableMasterRelay(context.Background(), "http://evil.example"); err == nil || r.count(&r.creates) != 0 {
		t.Fatalf("a plain-http relay was used: %v", err)
	}
}

func TestEnableMasterRelayRollsBackWhenUpFails(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	n.upErr = errors.New("tailscale up: boom")
	if err := f.EnableMasterRelay(context.Background(), r.URL); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
	if r.count(&r.deletes) != 1 || exists(t, f.relayPath()) || exists(t, f.masterPath()) {
		t.Fatalf("not rolled back: deletes %d", r.deletes)
	}
	// an Up that refused because of another network ran nothing: it is not undone with a Down
	n.calls, n.upErr = nil, ErrOtherNetwork
	if err := f.EnableMasterRelay(context.Background(), r.URL); !errors.Is(err, ErrOtherNetwork) {
		t.Fatalf("got %v", err)
	}
	if n.called("down") || r.count(&r.deletes) != 2 || exists(t, f.relayPath()) {
		t.Fatalf("calls %v, deletes %d", n.calls, r.deletes)
	}
}

// A code carries the relay's URL only, and members join with it as login server:
// a relay naming another one is refused, and rolled back like any failure.
func TestEnableMasterRelayRefusesDifferentLoginServer(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	r.set(func(r *fakeRelay) { r.loginServer = testLogin })
	if err := f.EnableMasterRelay(context.Background(), r.URL); err == nil || !strings.Contains(err.Error(), "different login server") {
		t.Fatalf("got %v", err)
	}
	if r.count(&r.deletes) != 1 || r.count(&r.keys) != 0 || n.called("up") || exists(t, f.relayPath()) || exists(t, f.masterPath()) {
		t.Fatalf("not rolled back: deletes %d keys %d calls %v", r.deletes, r.keys, n.calls)
	}
	// a trailing slash or the host's case is the same server
	if !sameURL("https://Relay.Example/", "https://relay.example") || sameURL("https://relay.example", testLogin) || sameURL("https://relay.example/a", "https://relay.example/A") {
		t.Fatal("sameURL")
	}
	r.set(func(r *fakeRelay) { r.loginServer = r.URL + "/" })
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
}

func TestEnableMasterRelayIdempotent(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	for range 2 {
		if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
			t.Fatal(err)
		}
	}
	if r.count(&r.creates) != 1 {
		t.Fatalf("%d fleets created", r.creates)
	}
	other := newFakeRelay(t)
	if err := f.EnableMasterRelay(context.Background(), other.URL); err == nil || !strings.Contains(err.Error(), "change") || other.count(&other.creates) != 0 {
		t.Fatalf("another relay: %v", err)
	}
	// a relay.json gone while the master runs over the relay is not silently replaced
	os.WriteFile(f.relayPath(), []byte("{damaged"), 0o600)
	if err := f.EnableMasterRelay(context.Background(), r.URL); err == nil || r.count(&r.creates) != 1 {
		t.Fatalf("a damaged relay.json was replaced: %v", err)
	}
}

func TestEnableMasterRelayCleansALeftover(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	// a crash after relay.json was saved, before the master was on, with the network up
	if err := f.saveRelay(RelayState{Relay: r.URL, FleetID: "fl1", Secret: testSecret}); err != nil {
		t.Fatal(err)
	}
	n.before = NetState{Connected: true, Addr: "100.64.0.9", LoginServer: testLogin}
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if r.count(&r.deletes) != 1 || r.count(&r.creates) != 1 || !n.called("down") {
		t.Fatalf("deletes %d creates %d calls %v", r.deletes, r.creates, n.calls)
	}
	if st, _ := f.MasterStatus(); st.NetAddr != "100.64.0.7" {
		t.Fatalf("%+v", st)
	}
}

func TestEnableMasterRelayTimesOutWaitingForAddr(t *testing.T) {
	oldWait, oldStep := addrWait, addrStep
	t.Cleanup(func() { addrWait, addrStep = oldWait, oldStep })
	addrWait, addrStep = 50*time.Millisecond, 5*time.Millisecond
	f, n, r, _ := relayMaster(t)
	n.after.Addr = ""
	if err := f.EnableMasterRelay(context.Background(), r.URL); err == nil || !strings.Contains(err.Error(), "address") {
		t.Fatalf("got %v", err)
	}
	if !n.called("down") || r.count(&r.deletes) != 1 || exists(t, f.relayPath()) || exists(t, f.masterPath()) {
		t.Fatalf("not rolled back: calls %v deletes %d", n.calls, r.deletes)
	}
}

func TestAdvancedEnableRefusedWhileRelaySet(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if err := f.EnableMaster("https://me.example"); err == nil {
		t.Fatal("an address typed by hand replaced the relay")
	}
	if st, _ := f.MasterStatus(); st.Address != "https://100.64.0.7" || st.Relay != r.URL {
		t.Fatalf("%+v", st)
	}
}

func TestNoSecretInLogs(t *testing.T) {
	f, _, r, logs := relayMaster(t)
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if err := f.DisableMaster(); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(logs(), "\n")
	if all == "" {
		t.Fatal("nothing was logged")
	}
	for _, s := range []string{testSecret, testJoinKey, "Bearer"} {
		if strings.Contains(all, s) {
			t.Fatalf("the log holds %q: %s", s, all)
		}
	}
}

func TestLoadRelayRefusesADamagedFile(t *testing.T) {
	f := New(t.TempDir())
	var logged int
	f.Log = func(string, error) { logged++ }
	if _, ok := f.loadRelay(); ok || logged != 0 {
		t.Fatal("no file is no relay, said nowhere")
	}
	os.MkdirAll(filepath.Dir(f.relayPath()), 0o755)
	for _, body := range []string{"{damaged", `{"relay":"http://evil.example","fleet_id":"a","secret":"b"}`, `{"relay":"https://r.example","fleet_id":"a"}`} {
		logged = 0
		os.WriteFile(f.relayPath(), []byte(body), 0o600)
		if _, ok := f.loadRelay(); ok || logged != 1 {
			t.Fatalf("%s: ok %v, logged %d", body, ok, logged)
		}
	}
}
