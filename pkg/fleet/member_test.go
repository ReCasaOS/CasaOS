package fleet

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
)

// shortWaits makes the member's backoff fit a test.
func shortWaits(t *testing.T) {
	t.Helper()
	fw, mw, rw := firstWait, maxWait, refusedWait
	t.Cleanup(func() { firstWait, maxWait, refusedWait = fw, mw, rw })
	firstWait, maxWait, refusedWait = 50*time.Millisecond, 200*time.Millisecond, 500*time.Millisecond
}

// seen is one request the fake gateway received.
type seen struct {
	Method, Path, Query, Authorization, Accept, ContentType string
	Body                                                    []byte
}

// fakeGateway records every request and answers what the test says.
type fakeGateway struct {
	mu     sync.Mutex
	got    []seen
	status int
	ctype  string
	body   []byte
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := readAll(r)
	g.mu.Lock()
	g.got = append(g.got, seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Accept"), r.Header.Get("Content-Type"), body})
	status, ctype, answer := g.status, g.ctype, g.body
	g.mu.Unlock()
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.WriteHeader(status)
	w.Write(answer)
}

func (g *fakeGateway) requests() []seen {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]seen(nil), g.got...)
}

func readAll(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

// newMember is a member whose gateway is gw (served by httptest) and whose
// runtime path holds the internal secret "s3cret". Its loop runs until the test ends.
func newMember(t *testing.T, gw http.Handler) *Fleet {
	t.Helper()
	f := New(t.TempDir())
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	f.Gateway = func() (GatewayInfo, error) { return GatewayInfo{Public: mustURL(t, srv.URL)}, nil }
	runtime := t.TempDir()
	if err := os.WriteFile(filepath.Join(runtime, external.InternalSecretFilename), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.RuntimePath = func() string { return runtime }
	f.Hostname = func() (string, error) { return "box-7", nil }
	f.Version = func() string { return "v1.2.3" }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return f
}

func join(t *testing.T, master, member *Fleet, name string) Code {
	t.Helper()
	code := addBox(t, master, name)
	if err := member.Join(code.String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "online", online(master, code.BoxID))
	return code
}

// dropTunnels cuts every tunnel as a master that died would.
func (f *Fleet) dropTunnels() {
	f.mu.Lock()
	ts := f.tunnels
	f.tunnels = map[string]*tunnel{}
	f.mu.Unlock()
	for _, t := range ts {
		t.close()
	}
}

// rawRelay sends any frame down a box's tunnel, as a misbehaving master would.
func (f *Fleet) rawRelay(boxID string, fr frame) (frame, error) {
	f.mu.Lock()
	t := f.tunnels[boxID]
	f.mu.Unlock()
	if t == nil {
		return frame{}, ErrOffline
	}
	id, ch, err := t.register()
	if err != nil {
		return frame{}, err
	}
	defer t.unregister(id)
	fr.T, fr.ID = "req", id
	if err := t.write(fr); err != nil {
		return frame{}, err
	}
	select {
	case res := <-ch:
		return res, nil
	case <-time.After(2 * time.Second):
		return frame{}, ErrTimeout
	}
}

func TestMemberServesARelayedRequestWithItsInternalIdentity(t *testing.T) {
	shortWaits(t)
	master, _ := newMaster(t)
	gw := &fakeGateway{status: 200, ctype: "application/json; charset=utf-8", body: []byte(`{"current_version":"v1"}`)}
	member := newMember(t, gw)
	code := join(t, master, member, "Maison")
	res, err := master.Relay(context.Background(), code.BoxID, "GET", "/v1/sys/version/check", "token=x&y=1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 || res.ContentType != "application/json; charset=utf-8" || string(res.Body) != `{"current_version":"v1"}` {
		t.Fatalf("%+v", res)
	}
	got := gw.requests()
	if len(got) != 1 || got[0].Method != "GET" || got[0].Path != "/v1/sys/version/check" || got[0].Query != "y=1" || got[0].Authorization != "Internal s3cret" || got[0].Accept == "" {
		t.Fatalf("%+v", got)
	}
	res, err = master.Relay(context.Background(), code.BoxID, "PUT", "/v2/app_management/compose/plex/status", "", "application/json", []byte(`"stop"`))
	if err != nil || res.Status != 200 {
		t.Fatalf("%+v %v", res, err)
	}
	got = gw.requests()
	if len(got) != 2 || got[1].Method != "PUT" || got[1].ContentType != "application/json" || string(got[1].Body) != `"stop"` {
		t.Fatalf("%+v", got[1])
	}
	st, ok := member.MemberStatus()
	if !ok || !st.Connected || st.Since.IsZero() || st.LastError != "" || st.MasterURL == "" {
		t.Fatalf("%+v %v", st, ok)
	}
}

func TestMemberRefusesOutsideTheTableEvenIfTheMasterAsks(t *testing.T) {
	shortWaits(t)
	master, _ := newMaster(t)
	gw := &fakeGateway{status: 200}
	member := newMember(t, gw)
	code := join(t, master, member, "Maison")
	res, err := master.rawRelay(code.BoxID, frame{Method: "GET", Path: "/v1/file/content", Query: "path=/etc/shadow"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 403 || !strings.Contains(string(res.Body), "this route is not relayed") {
		t.Fatalf("%+v", res)
	}
	if got := gw.requests(); len(got) != 0 {
		t.Fatalf("the gateway saw %+v", got)
	}
}

func TestMemberReconnectsAfterTheMasterRestarts(t *testing.T) {
	shortWaits(t)
	master := New(t.TempDir())
	leaf, _ := makeCert(t, "localhost", false, nil, nil)
	certFile := filepath.Join(t.TempDir(), "gateway.crt")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}), 0o600)
	master.Gateway = func() (GatewayInfo, error) {
		return GatewayInfo{Public: mustURL(t, "http://127.0.0.1:80"), CertFile: certFile}, nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/fleet/tunnel", master.HandleTunnel)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	if err := master.EnableMaster("http://" + addr); err != nil {
		t.Fatal(err)
	}
	member := newMember(t, &fakeGateway{status: 200})
	code := join(t, master, member, "Maison")
	srv.Close()
	master.dropTunnels() // httptest leaves hijacked connections alone: a dead master would not
	waitFor(t, "offline", func() bool { return !online(master, code.BoxID)() })
	var ln2 net.Listener
	waitFor(t, "the address to be free again", func() bool { ln2, err = net.Listen("tcp", addr); return err == nil })
	srv2 := httptest.NewUnstartedServer(mux)
	srv2.Listener = ln2
	srv2.Start()
	t.Cleanup(srv2.Close)
	waitFor(t, "online again", online(master, code.BoxID))
}

func TestRevokedMemberStopsHammering(t *testing.T) {
	shortWaits(t)
	var dials atomic.Int32
	master, _ := newMasterHook(t, func() { dials.Add(1) })
	member := newMember(t, &fakeGateway{status: 200})
	code := join(t, master, member, "Maison")
	if err := master.RemoveBox(code.BoxID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "refused", func() bool {
		st, _ := member.MemberStatus()
		return st.LastError == "refused by the master" && !st.Connected
	})
	n := dials.Load()
	time.Sleep(refusedWait / 2)
	if m := dials.Load(); m != n {
		t.Fatalf("dialled %d times during the refused wait", m-n)
	}
	waitFor(t, "one more try after the refused wait", func() bool { return dials.Load() > n })
}

func TestLeaveClosesAndForgets(t *testing.T) {
	shortWaits(t)
	master, _ := newMaster(t)
	member := newMember(t, &fakeGateway{status: 200})
	code := join(t, master, member, "Maison")
	if err := member.Leave(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "offline", func() bool { return !online(master, code.BoxID)() })
	if _, ok := member.MemberStatus(); ok {
		t.Fatal("still a member")
	}
	if _, err := os.Stat(filepath.Join(member.Root, StateDir, "member.json")); !os.IsNotExist(err) {
		t.Fatalf("member.json: %v", err)
	}
	time.Sleep(3 * firstWait)
	if online(master, code.BoxID)() {
		t.Fatal("the member came back after leaving")
	}
}

func TestJoinRefusesABadCodeAndAMasterBox(t *testing.T) {
	master, _ := newMaster(t)
	if err := master.Join("!!!"); err == nil {
		t.Fatal("bad code accepted")
	}
	code := Code{MasterURL: "https://other.example", BoxID: "b", Key: "k"}
	if err := master.Join(code.String()); err == nil {
		t.Fatal("a master joined another fleet")
	}
}

// One Fleet self-enrolled: the master and the member in the same struct. The
// gateway is the one httptest server: the tunnel on its path, everything else
// a fake gateway.
func selfFleet(t *testing.T) (*Fleet, *fakeGateway) {
	t.Helper()
	f := New(t.TempDir())
	gw := &fakeGateway{status: 200, ctype: "application/json", body: []byte(`{"ok":true}`)}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/fleet/tunnel", f.HandleTunnel)
	mux.Handle("/", gw)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	leaf, _ := makeCert(t, "localhost", false, nil, nil)
	certFile := filepath.Join(t.TempDir(), "gateway.crt")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}), 0o600)
	f.Gateway = func() (GatewayInfo, error) { return GatewayInfo{Public: mustURL(t, srv.URL), CertFile: certFile}, nil }
	runtime := t.TempDir()
	os.WriteFile(filepath.Join(runtime, external.InternalSecretFilename), []byte("s3cret"), 0o600)
	f.RuntimePath = func() string { return runtime }
	if err := f.EnableMaster(srv.URL); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return f, gw
}

func TestDisableClosesTunnelsAndKeepsMemberIdle(t *testing.T) {
	shortWaits(t)
	f, gw := selfFleet(t)
	box, _, err := f.AddBox("this box", "", true)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "self online", online(f, box.ID))
	if res, err := f.Relay(context.Background(), box.ID, "GET", "/v1/sys/os", "", "", nil); err != nil || res.Status != 200 || len(gw.requests()) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if err := f.DisableMaster(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.Root, StateDir, "master.json")); !os.IsNotExist(err) {
		t.Fatalf("master.json: %v", err)
	}
	if _, ok := f.loadMember(); !ok {
		t.Fatal("member.json gone")
	}
	waitFor(t, "member refused", func() bool {
		st, ok := f.MemberStatus()
		return ok && !st.Connected && st.LastError == "refused by the master"
	})
}

func TestMemberHelloCarriesHostnameAndVersion(t *testing.T) {
	shortWaits(t)
	master, _ := newMaster(t)
	member := newMember(t, &fakeGateway{status: 200})
	code := join(t, master, member, "Maison")
	st, _ := master.MasterStatus()
	for _, b := range st.Boxes {
		if b.ID == code.BoxID && (b.Hostname != "box-7" || b.Version != "v1.2.3") {
			t.Fatalf("%+v", b)
		}
	}
}

func TestBodyLimitOnTheMemberSide(t *testing.T) {
	shortWaits(t)
	master, _ := newMaster(t)
	member := newMember(t, &fakeGateway{status: 200, body: bytes.Repeat([]byte("x"), MaxBody+1)})
	code := join(t, master, member, "Maison")
	res, err := master.Relay(context.Background(), code.BoxID, "GET", "/v1/sys/os", "", "", nil)
	if err != nil || res.Status != 502 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestMemberSaysWhenItsGatewayCannotBeReached(t *testing.T) {
	shortWaits(t)
	master, _ := newMaster(t)
	member := newMember(t, &fakeGateway{status: 200})
	member.Gateway = func() (GatewayInfo, error) {
		return GatewayInfo{}, errors.New("the gateway listens on 192.168.1.5, not on this box's loopback")
	}
	code := join(t, master, member, "Maison")
	st, _ := member.MemberStatus()
	if !st.Connected || !strings.Contains(st.LastError, "192.168.1.5") {
		t.Fatalf("%+v", st)
	}
	res, err := master.Relay(context.Background(), code.BoxID, "GET", "/v1/sys/os", "", "", nil)
	if err != nil || res.Status != 502 || !strings.Contains(string(res.Body), "192.168.1.5") {
		t.Fatalf("%+v %v", res, err)
	}
}
