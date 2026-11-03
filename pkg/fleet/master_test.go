package fleet

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// newMaster is a master served by httptest, its gateway stubbed to the default
// layout (plain port, https on 443, the default self-signed certificate).
func newMaster(t *testing.T) (*Fleet, *httptest.Server) {
	t.Helper()
	return newMasterHook(t, func() {})
}

// newMasterHook is newMaster with onDial run at every tunnel dial.
func newMasterHook(t *testing.T, onDial func()) (*Fleet, *httptest.Server) {
	t.Helper()
	f := New(t.TempDir())
	leaf, _ := makeCert(t, "localhost", false, nil, nil)
	certFile := filepath.Join(t.TempDir(), "gateway.crt")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	f.Gateway = func() (GatewayInfo, error) {
		return GatewayInfo{Public: mustURL(t, "http://127.0.0.1:80"), HTTPS: mustURL(t, "https://127.0.0.1:443"), CertFile: certFile}, nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/fleet/tunnel", tunnelHandler(t, f, onDial))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if err := f.EnableMaster(srv.URL); err != nil {
		t.Fatal(err)
	}
	return f, srv
}

// tunnelHandler mounts f.HandleTunnel for a test. Its cleanup, registered after
// the test's temp dirs and so run before they are removed, closes every tunnel
// and waits for the handlers: none writes master.json into a directory being
// deleted. Register it before the server's own Close, which then runs first and
// stops new dials.
func tunnelHandler(t *testing.T, f *Fleet, onDial func()) http.HandlerFunc {
	t.Helper()
	var handlers sync.WaitGroup
	t.Cleanup(func() { f.dropTunnels(); handlers.Wait() })
	return func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		onDial()
		f.HandleTunnel(w, r)
	}
}

// dialTunnel dials the master's tunnel as a box would; the response tells why when it fails.
func dialTunnel(srv *httptest.Server, id, key string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{"Authorization": {"Fleet " + id + ":" + key}}
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/fleet/tunnel", h)
}

// fakeMember dials, says hello and answers each req frame with answer(req);
// a nil answer never answers. Every frame it receives is also sent to got.
func fakeMember(t *testing.T, srv *httptest.Server, code Code, answer func(frame) frame) (*websocket.Conn, chan frame) {
	t.Helper()
	conn, resp, err := dialTunnel(srv, code.BoxID, code.Key)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial: %v (status %d)", err, status)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.WriteJSON(frame{T: "hello", Hostname: "box-1", Version: "v0.9.0"}); err != nil {
		t.Fatal(err)
	}
	got := make(chan frame, 64)
	closed := make(chan struct{})
	readerExits.Store(conn, closed)
	var wmu sync.Mutex
	go func() {
		// the only reader of conn: closedWithin waits on closed instead of reading
		defer close(closed)
		for {
			var req frame
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			got <- req
			if answer == nil || req.T != "req" {
				continue
			}
			res := answer(req)
			res.T, res.ID = "res", req.ID
			wmu.Lock()
			conn.WriteJSON(res)
			wmu.Unlock()
		}
	}()
	return conn, got
}

func okJSON(frame) frame {
	return frame{Status: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: []byte(`{"ok":true}`)}
}

// waitFor polls cond until it holds. The deadline is a bound, not a duration: CI runs every package
// at once under -race, where a tunnel can take seconds to come up.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func online(f *Fleet, id string) func() bool {
	return func() bool {
		st, _ := f.MasterStatus()
		for _, b := range st.Boxes {
			if b.ID == id {
				return b.Online
			}
		}
		return false
	}
}

func addBox(t *testing.T, f *Fleet, name string) Code {
	t.Helper()
	_, code, err := f.AddBox(name, "", false)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// readerExits maps a fakeMember conn to the channel its reader closes when the
// connection goes; a second reader on a websocket.Conn would be a data race.
var readerExits sync.Map

// closedWithin says whether the far end closed a fakeMember conn within d.
func closedWithin(conn *websocket.Conn, d time.Duration) bool {
	v, ok := readerExits.Load(conn)
	if !ok {
		panic("closedWithin needs a conn from fakeMember")
	}
	select {
	case <-v.(chan struct{}):
		return true
	case <-time.After(d):
		return false
	}
}

func TestTunnelRefusesUnknownKeyAndRevokedBox(t *testing.T) {
	f, srv := newMaster(t)
	code := addBox(t, f, "Maison")
	if _, resp, err := dialTunnel(srv, code.BoxID, strings.Repeat("00", 32)); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("wrong key: err %v resp %v", err, resp)
	}
	if _, resp, err := dialTunnel(srv, "nobody", code.Key); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("unknown box: err %v resp %v", err, resp)
	}
	if err := f.RemoveBox(code.BoxID); err != nil {
		t.Fatal(err)
	}
	if _, resp, err := dialTunnel(srv, code.BoxID, code.Key); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("revoked: err %v resp %v", err, resp)
	}
	if err := f.RemoveBox("nobody"); !errors.Is(err, ErrNoBox) {
		t.Fatalf("RemoveBox(nobody) = %v", err)
	}
}

func TestRelayRoundTrip(t *testing.T) {
	f, srv := newMaster(t)
	code := addBox(t, f, "Maison")
	_, got := fakeMember(t, srv, code, okJSON)
	waitFor(t, "online", online(f, code.BoxID))
	res, err := f.Relay(context.Background(), code.BoxID, "PUT", "/v2/app_management/compose/plex/status", "a=1&token=x", "application/json", []byte(`"stop"`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 || res.ContentType != "application/json" || string(res.Body) != `{"ok":true}` {
		t.Fatalf("%+v", res)
	}
	req := <-got
	if req.T != "req" || req.ID == "" || req.Method != "PUT" || req.Path != "/v2/app_management/compose/plex/status" || req.Query != "a=1" || string(req.Body) != `"stop"` {
		t.Fatalf("%+v", req)
	}
	if len(req.Headers) != 2 || req.Headers["Content-Type"] != "application/json" || req.Headers["Accept"] == "" {
		t.Fatalf("headers %v", req.Headers)
	}
}

func TestRelayRefusesOutsideTheTable(t *testing.T) {
	f, srv := newMaster(t)
	code := addBox(t, f, "Maison")
	_, got := fakeMember(t, srv, code, okJSON)
	waitFor(t, "online", online(f, code.BoxID))
	if _, err := f.Relay(context.Background(), code.BoxID, "GET", "/v1/file/content", "", "", nil); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("err = %v", err)
	}
	select {
	case fr := <-got:
		t.Fatalf("a frame was sent: %+v", fr)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRelayWhenOffline(t *testing.T) {
	f, _ := newMaster(t)
	code := addBox(t, f, "Maison")
	if _, err := f.Relay(context.Background(), code.BoxID, "GET", "/v1/sys/os", "", "", nil); !errors.Is(err, ErrOffline) {
		t.Fatalf("err = %v", err)
	}
	if _, err := f.Relay(context.Background(), "nobody", "GET", "/v1/sys/os", "", "", nil); !errors.Is(err, ErrNoBox) {
		t.Fatalf("err = %v", err)
	}
}

func TestRelayTimeout(t *testing.T) {
	old := timeouts
	t.Cleanup(func() { timeouts = old })
	timeouts = func(bool) time.Duration { return 100 * time.Millisecond }
	f, srv := newMaster(t)
	code := addBox(t, f, "Maison")
	fakeMember(t, srv, code, nil)
	waitFor(t, "online", online(f, code.BoxID))
	if _, err := f.Relay(context.Background(), code.BoxID, "GET", "/v1/sys/os", "", "", nil); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
}

func TestRelayCapsRequestsInFlight(t *testing.T) {
	f, srv := newMaster(t)
	code := addBox(t, f, "Maison")
	fakeMember(t, srv, code, nil)
	waitFor(t, "online", online(f, code.BoxID))
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Relay(ctx, code.BoxID, "GET", "/v1/sys/os", "", "", nil)
		}()
	}
	waitFor(t, "16 in flight", func() bool { return f.inFlight(code.BoxID) == 16 })
	if _, err := f.Relay(ctx, code.BoxID, "GET", "/v1/sys/os", "", "", nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("17th: %v", err)
	}
	cancel()
	wg.Wait()
	if n := f.inFlight(code.BoxID); n != 0 {
		t.Fatalf("%d still in flight after cancel", n)
	}
}

func TestASecondTunnelReplacesTheFirst(t *testing.T) {
	f, srv := newMaster(t)
	code := addBox(t, f, "Maison")
	first, _ := fakeMember(t, srv, code, func(frame) frame { return frame{Status: 200, Body: []byte("first")} })
	waitFor(t, "online", online(f, code.BoxID))
	fakeMember(t, srv, code, func(frame) frame { return frame{Status: 200, Body: []byte("second")} })
	if !closedWithin(first, 10*time.Second) {
		t.Fatal("the first tunnel was not closed")
	}
	waitFor(t, "second online", online(f, code.BoxID))
	res, err := f.Relay(context.Background(), code.BoxID, "GET", "/v1/sys/os", "", "", nil)
	if err != nil || string(res.Body) != "second" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestOversizedFramesAreDroppedWithTheConnection(t *testing.T) {
	f, srv := newMaster(t)
	code := addBox(t, f, "Maison")
	fakeMember(t, srv, code, func(frame) frame { return frame{Status: 200, Body: bytes.Repeat([]byte("x"), maxFrame+1)} })
	waitFor(t, "online", online(f, code.BoxID))
	if _, err := f.Relay(context.Background(), code.BoxID, "GET", "/v1/sys/os", "", "", nil); !errors.Is(err, ErrOffline) {
		t.Fatalf("err = %v", err)
	}
	waitFor(t, "offline", func() bool { return !online(f, code.BoxID)() })
	fakeMember(t, srv, code, okJSON)
	waitFor(t, "online again", online(f, code.BoxID))
}

func TestAddBoxSelfWritesMemberJSON(t *testing.T) {
	f, _ := newMaster(t)
	box, code, err := f.AddBox("this box", "", true)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := f.Gateway()
	want, _ := CertFingerprint(info.CertFile)
	if !box.Self || code.MasterURL != "https://127.0.0.1:443" || code.CertSHA256 != want || code.BoxID != box.ID {
		t.Fatalf("box %+v code %+v", box, code)
	}
	m, ok := f.loadMember()
	if !ok || m.BoxID != box.ID || m.Key != code.Key || m.MasterURL != code.MasterURL || m.MasterCertSHA256 != want {
		t.Fatalf("member.json %+v %v", m, ok)
	}
	st, _ := f.MasterStatus()
	if len(st.Boxes) != 1 || !st.Boxes[0].Self {
		t.Fatalf("%+v", st)
	}
}

func TestAddBoxRefusesWhenOffOrNameless(t *testing.T) {
	f, _ := newMaster(t)
	if _, _, err := f.AddBox("", "", false); err == nil {
		t.Fatal("nameless accepted")
	}
	if _, _, err := f.AddBox("x", "not a url", false); err == nil {
		t.Fatal("bad url accepted")
	}
	if err := f.DisableMaster(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.AddBox("x", "", false); !errors.Is(err, ErrMasterOff) {
		t.Fatalf("err = %v", err)
	}
}

func TestRemoveBoxAndDisableCloseTunnels(t *testing.T) {
	f, srv := newMaster(t)
	c1, c2 := addBox(t, f, "one"), addBox(t, f, "two")
	conn1, _ := fakeMember(t, srv, c1, okJSON)
	conn2, _ := fakeMember(t, srv, c2, okJSON)
	waitFor(t, "both online", func() bool { return online(f, c1.BoxID)() && online(f, c2.BoxID)() })
	if err := f.RemoveBox(c1.BoxID); err != nil {
		t.Fatal(err)
	}
	if !closedWithin(conn1, 10*time.Second) {
		t.Fatal("tunnel one still open")
	}
	if _, err := f.Relay(context.Background(), c1.BoxID, "GET", "/v1/sys/os", "", "", nil); !errors.Is(err, ErrNoBox) {
		t.Fatalf("relay to a removed box: %v", err)
	}
	if !online(f, c2.BoxID)() {
		t.Fatal("tunnel two was closed too")
	}
	if err := f.DisableMaster(); err != nil {
		t.Fatal(err)
	}
	if !closedWithin(conn2, 10*time.Second) {
		t.Fatal("tunnel two still open after disable")
	}
	if _, err := os.Stat(filepath.Join(f.Root, StateDir, "master.json")); !os.IsNotExist(err) {
		t.Fatalf("master.json: %v", err)
	}
	if _, ok := f.MasterStatus(); ok {
		t.Fatal("still master")
	}
}

func TestTheMasterNeverExecutesAFrameFromAMember(t *testing.T) {
	f, srv := newMaster(t)
	code := addBox(t, f, "Maison")
	conn, got := fakeMember(t, srv, code, okJSON)
	waitFor(t, "online", online(f, code.BoxID))
	if err := conn.WriteJSON(frame{T: "req", ID: "evil", Method: "GET", Path: "/v1/sys/os"}); err != nil {
		t.Fatal(err)
	}
	select {
	case fr := <-got:
		t.Fatalf("the master answered: %+v", fr)
	case <-time.After(200 * time.Millisecond):
	}
	// the tunnel is still up and still works
	if res, err := f.Relay(context.Background(), code.BoxID, "GET", "/v1/sys/os", "", "", nil); err != nil || res.Status != 200 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestMasterStatus(t *testing.T) {
	f, srv := newMaster(t)
	code := addBox(t, f, "Maison")
	st, ok := f.MasterStatus()
	if !ok || st.Address != srv.URL || len(st.Boxes) != 1 || st.Boxes[0].Online || st.Boxes[0].Name != "Maison" {
		t.Fatalf("%+v %v", st, ok)
	}
	conn, _ := fakeMember(t, srv, code, okJSON)
	waitFor(t, "online", online(f, code.BoxID))
	st, _ = f.MasterStatus()
	b := st.Boxes[0]
	if !b.Online || b.LastSeen.IsZero() || b.Hostname != "box-1" || b.Version != "v0.9.0" || b.Self {
		t.Fatalf("%+v", b)
	}
	conn.Close()
	waitFor(t, "offline", func() bool { return !online(f, code.BoxID)() })
	st, _ = f.MasterStatus()
	if st.Boxes[0].LastSeen.IsZero() || st.Boxes[0].Hostname != "box-1" {
		t.Fatalf("after close: %+v", st.Boxes[0])
	}
	if err := f.EnableMaster("https://new.example"); err != nil {
		t.Fatal(err)
	}
	if st, _ = f.MasterStatus(); st.Address != "https://new.example" || len(st.Boxes) != 1 {
		t.Fatalf("re-enable lost the boxes: %+v", st)
	}
}

func TestRenameAndRotate(t *testing.T) {
	f, srv := newMaster(t)
	code := addBox(t, f, "Maison")
	conn, _ := fakeMember(t, srv, code, okJSON)
	waitFor(t, "online", online(f, code.BoxID))
	name, link := "Bureau", "https://bureau.example"
	if err := f.RenameBox(code.BoxID, &name, &link); err != nil {
		t.Fatal(err)
	}
	st, _ := f.MasterStatus()
	if st.Boxes[0].Name != "Bureau" || st.Boxes[0].URL != "https://bureau.example" {
		t.Fatalf("%+v", st.Boxes[0])
	}
	if err := f.RenameBox(code.BoxID, nil, nil); err != nil {
		t.Fatal(err)
	}
	empty := ""
	if err := f.RenameBox(code.BoxID, &empty, nil); err == nil {
		t.Fatal("empty name accepted")
	}
	fresh, err := f.RotateCode(code.BoxID)
	if err != nil || fresh.Key == code.Key || fresh.BoxID != code.BoxID || fresh.MasterURL != srv.URL || fresh.CertSHA256 != code.CertSHA256 {
		t.Fatalf("%+v %v", fresh, err)
	}
	if !closedWithin(conn, 10*time.Second) {
		t.Fatal("old tunnel still open after rotation")
	}
	if _, resp, err := dialTunnel(srv, code.BoxID, code.Key); err == nil || resp.StatusCode != 401 {
		t.Fatal("old key still accepted")
	}
	fakeMember(t, srv, fresh, okJSON)
	waitFor(t, "online with the new key", online(f, code.BoxID))
	if _, err := f.RotateCode("nobody"); !errors.Is(err, ErrNoBox) {
		t.Fatalf("%v", err)
	}
}

// The only thing the master knows without the page open: a box gone for ten minutes.
func TestOfflineWatch(t *testing.T) {
	f, srv := newMaster(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	f.Now = func() time.Time { return now }
	type call struct {
		id, name string
		off      bool
	}
	var calls []call
	f.Offline = func(id, name string, off bool) { calls = append(calls, call{id, name, off}) }
	seen, never := addBox(t, f, "Maison"), addBox(t, f, "Bureau")
	f.checkOffline() // the master starts: the grace period begins
	conn, _ := fakeMember(t, srv, seen, okJSON)
	waitFor(t, "online", online(f, seen.BoxID))
	conn.Close()
	waitFor(t, "offline", func() bool { return !online(f, seen.BoxID)() })
	for _, after := range []time.Duration{time.Minute, offlineAfter - time.Minute} {
		now = now.Add(after - now.Sub(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)))
		f.checkOffline()
	}
	if len(calls) != 0 {
		t.Fatalf("too early: %+v", calls)
	}
	now = now.Add(time.Minute)
	f.checkOffline()
	f.checkOffline()
	if len(calls) != 1 || calls[0] != (call{seen.BoxID, "Maison", true}) {
		t.Fatalf("%+v", calls)
	}
	fakeMember(t, srv, seen, okJSON)
	waitFor(t, "online again", online(f, seen.BoxID))
	f.checkOffline()
	if len(calls) != 2 || calls[1] != (call{seen.BoxID, "Maison", false}) {
		t.Fatalf("%+v", calls)
	}
	// a box that never connected is not "gone": nothing, however long
	now = now.Add(24 * time.Hour)
	f.checkOffline()
	if len(calls) != 2 {
		t.Fatalf("never-connected box alerted: %+v", calls)
	}
	_ = never
}

// A master that restarts says nothing about its boxes during the grace period.
func TestOfflineWatchGraceAfterStart(t *testing.T) {
	f, _ := newMaster(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	f.Now = func() time.Time { return now }
	var calls int
	f.Offline = func(string, string, bool) { calls++ }
	code := addBox(t, f, "Maison")
	f.mu.Lock()
	f.modifyBox(code.BoxID, func(b *Box) error { b.LastSeen = now.Add(-48 * time.Hour); return nil })
	f.mu.Unlock()
	f.checkOffline()
	now = now.Add(startGrace - time.Minute)
	f.checkOffline()
	if calls != 0 {
		t.Fatal("alerted during the grace period")
	}
	now = now.Add(time.Minute)
	f.checkOffline()
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	// removing the box stops its tracking and says nothing more: it did not
	// come back, the owner removed it, and a resolution would carry no name
	f.RemoveBox(code.BoxID)
	f.checkOffline()
	if calls != 1 {
		t.Fatalf("calls = %d after removal, want no further call", calls)
	}
	if _, tracked := f.offline[code.BoxID]; tracked {
		t.Fatal("a removed box is still tracked")
	}
}

// A key checked at the handshake can be revoked before the hello arrives (the
// hello has up to helloWait to come). The tunnel must not come up on a key that
// is no longer the box's, whether it was rotated, the box removed, or the master
// disabled.
func TestARevokedKeyCannotOpenATunnelAfterTheHandshake(t *testing.T) {
	dialThenRevoke := func(t *testing.T, revoke func(*Fleet, Code)) {
		f, srv := newMaster(t)
		code := addBox(t, f, "Maison")
		conn, resp, err := dialTunnel(srv, code.BoxID, code.Key) // authorised and upgraded; hello not sent yet
		if err != nil {
			t.Fatalf("dial: %v (resp %v)", err, resp)
		}
		defer conn.Close()
		revoke(f, code)
		conn.WriteJSON(frame{T: "hello", Hostname: "thief"})
		// the connection is closed by the master and never serves a relay
		if closedWithinRaw(conn, 2*time.Second) == false {
			t.Fatal("the connection stayed open after the key was revoked")
		}
		if online(f, code.BoxID)() {
			t.Fatal("the box came online on a revoked key")
		}
	}
	t.Run("rotated", func(t *testing.T) {
		dialThenRevoke(t, func(f *Fleet, code Code) {
			if _, err := f.RotateCode(code.BoxID); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("removed", func(t *testing.T) {
		dialThenRevoke(t, func(f *Fleet, code Code) {
			if err := f.RemoveBox(code.BoxID); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("master disabled", func(t *testing.T) {
		dialThenRevoke(t, func(f *Fleet, code Code) {
			if err := f.DisableMaster(); err != nil {
				t.Fatal(err)
			}
		})
	})
}

// closedWithinRaw reads conn directly (for a conn not owned by a fakeMember reader).
func closedWithinRaw(conn *websocket.Conn, d time.Duration) bool {
	conn.SetReadDeadline(time.Now().Add(d))
	_, _, err := conn.NextReader()
	return err != nil && !strings.Contains(err.Error(), "timeout")
}

// A box that is a member of another fleet is not its own master: EnableMaster
// refuses, so AddBox(self) cannot silently overwrite the master it belongs to.
func TestABoxCannotMasterWhileAMemberOfAnother(t *testing.T) {
	f := New(t.TempDir())
	if err := f.saveMember(MemberState{MasterURL: "https://other.example", BoxID: "b", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := f.EnableMaster("https://me.example"); err == nil {
		t.Fatal("a member of another fleet became a master")
	}
	if st := f.Status(); st.Role != "member" {
		t.Fatalf("role = %q, want member", st.Role)
	}
	// once it leaves, it can
	if err := f.Leave(); err != nil {
		t.Fatal(err)
	}
	if err := f.EnableMaster("https://me.example"); err != nil {
		t.Fatalf("after leaving: %v", err)
	}
}

// A box can answer a status no HTTP response carries (0, 1000). Relay must
// hand back a sane status, not one that makes the route layer panic.
func TestRelayClampsAnImpossibleStatus(t *testing.T) {
	for _, bad := range []int{0, 1000, -1, 99, 600} {
		f, srv := newMaster(t)
		code := addBox(t, f, "Maison")
		fakeMember(t, srv, code, func(frame) frame { return frame{Status: bad, Body: []byte("x")} })
		waitFor(t, "online", online(f, code.BoxID))
		res, err := f.Relay(t.Context(), code.BoxID, "GET", "/v1/sys/os", "", "", nil)
		if err != nil || res.Status != 502 {
			t.Fatalf("status %d: relay returned %d, %v, want 502", bad, res.Status, err)
		}
	}
}

// Review Focus #3: a hello with a 10 000-character hostname and version is cut,
// and does not take the master down.
func TestAHugeHelloIsCutNotFatal(t *testing.T) {
	f, srv := newMaster(t)
	code := addBox(t, f, "Maison")
	conn, _, err := dialTunnel(srv, code.BoxID, code.Key)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	big := strings.Repeat("a", 10000)
	if err := conn.WriteJSON(frame{T: "hello", Hostname: big, Version: big}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "online", online(f, code.BoxID))
	st, _ := f.MasterStatus()
	if n := len(st.Boxes[0].Hostname); n == 0 || n > maxName {
		t.Fatalf("hostname kept at %d bytes, want 1..%d", n, maxName)
	}
	if n := len(st.Boxes[0].Version); n > maxName {
		t.Fatalf("version kept at %d bytes", n)
	}
	// the master still serves a fresh dial afterwards
	fakeMember(t, srv, addBox(t, f, "Bureau"), okJSON)
}

// A gateway with HTTPS off has no certificate to pin: the code carries none and
// the chain check alone decides (a master behind a TLS-terminating proxy).
func TestAddBoxWithoutACertificate(t *testing.T) {
	f := New(t.TempDir())
	f.Gateway = func() (GatewayInfo, error) {
		return GatewayInfo{Public: mustURL(t, "http://127.0.0.1:80"), CertFile: filepath.Join(t.TempDir(), "gateway.crt")}, nil
	}
	if err := f.EnableMaster("http://127.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	self, code, err := f.AddBox("this box", "", true)
	if err != nil {
		t.Fatalf("self: %v", err)
	}
	if code.MasterURL != "http://127.0.0.1:80" || code.CertSHA256 != "" {
		t.Fatalf("self code %+v", code)
	}
	if m, ok := f.loadMember(); !ok || m.BoxID != self.ID || m.MasterCertSHA256 != "" {
		t.Fatalf("member.json %+v %v", m, ok)
	}
	_, other, err := f.AddBox("box b", "", false)
	if err != nil {
		t.Fatalf("other: %v", err)
	}
	if other.MasterURL != "http://127.0.0.1:8080" || other.CertSHA256 != "" {
		t.Fatalf("other code %+v", other)
	}
}

// Only a missing certificate is let go: one that is there and unreadable is
// still an error, so a broken gateway setup is not hidden behind an empty pin.
func TestAddBoxRefusesACertificateThatIsNotOne(t *testing.T) {
	f := New(t.TempDir())
	certFile := filepath.Join(t.TempDir(), "gateway.crt")
	if err := os.WriteFile(certFile, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.Gateway = func() (GatewayInfo, error) {
		return GatewayInfo{Public: mustURL(t, "http://127.0.0.1:80"), CertFile: certFile}, nil
	}
	if err := f.EnableMaster("http://127.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.AddBox("this box", "", true); err == nil {
		t.Fatal("a file with no certificate in it was accepted")
	}
}
