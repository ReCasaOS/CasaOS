package fleet

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func b64(pub ed25519.PublicKey) string { return base64.RawURLEncoding.EncodeToString(pub) }

// helloOf runs one member session of f against a fake master and returns the hello it sent.
func helloOf(t *testing.T, f *Fleet, st MemberState) frame {
	t.Helper()
	got := make(chan frame, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var hello frame
		if conn.ReadJSON(&hello) == nil {
			got <- hello
		}
		conn.ReadMessage() // until the member goes
	}))
	t.Cleanup(srv.Close)
	st.MasterURL, st.BoxID, st.Key = srv.URL, "b1", "k1"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.session(ctx, st); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case h := <-got:
		return h
	case <-done:
		t.Fatal("the session ended without a hello")
	}
	return frame{}
}

func TestHelloCarriesBoxKeyAndNodeID(t *testing.T) {
	f, n, _ := relayMember(t)
	n.after.NodeID, n.before.NodeID = "17", "17"
	n.up = true
	key, err := f.BoxKey()
	if err != nil {
		t.Fatal(err)
	}
	h := helloOf(t, f, MemberState{Relay: testRelay})
	if h.T != "hello" || h.BoxKey != b64(key.Public().(ed25519.PublicKey)) || h.NodeID != "17" {
		t.Fatalf("hello %+v", h)
	}
	if h := helloOf(t, f, MemberState{}); h.BoxKey != "" || h.NodeID != "" {
		t.Fatalf("a member with no relay sent %+v", h)
	}
	n.stateErr = errors.New("daemon down")
	if h := helloOf(t, f, MemberState{Relay: testRelay}); h.BoxKey != "" || h.NodeID != "" {
		t.Fatalf("with no node id the master must skip linking, got %+v", h)
	}
}

// relayMasterWithTunnel is a master over a fake relay whose tunnel is served by httptest.
func relayMasterWithTunnel(t *testing.T) (*Fleet, *httptest.Server, *fakeRelay, func() []string) {
	t.Helper()
	f, _, r, logs := relayMaster(t)
	f.backupFirst, f.backupEvery, f.backupSettle = time.Hour, time.Hour, time.Hour // no backup poll reaches the fake box
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/fleet/tunnel", tunnelHandler(t, f, func() {}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	return f, srv, r, logs
}

// hello dials the master as a new box and says a hello with the given key and node.
func helloBox(t *testing.T, f *Fleet, srv *httptest.Server, hello frame) string {
	t.Helper()
	box, code, err := f.AddBox("phone", "", false)
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := dialTunnel(srv, box.ID, code.Key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	hello.T = "hello"
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	return box.ID
}

func TestMasterLinksAMemberBoxKey(t *testing.T) {
	f, srv, r, logs := relayMasterWithTunnel(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	id := helloBox(t, f, srv, frame{BoxKey: b64(pub), NodeID: "17"})
	waitFor(t, "the key linked", func() bool { return boxStatus(t, f, id).ShareKey == "linked" })
	if p := r.puts(); len(p) != 1 || p[0] != (boxKeyPut{"17", b64(pub), "Bearer " + testSecret}) {
		t.Fatalf("relay saw %+v", p)
	}

	// a hello with no node id (its network was down) is not linked, and says nothing
	id2 := helloBox(t, f, srv, frame{BoxKey: b64(pub)})
	waitFor(t, "the second box online", online(f, id2))
	if len(r.puts()) != 1 || boxStatus(t, f, id2).ShareKey != "" {
		t.Fatalf("puts %+v, share %q", r.puts(), boxStatus(t, f, id2).ShareKey)
	}

	// another key already linked at the relay: the status says so, the log once
	r.set(func(r *fakeRelay) { r.keyConflict = true })
	id3 := helloBox(t, f, srv, frame{BoxKey: b64(pub), NodeID: "18"})
	waitFor(t, "the conflict", func() bool { return strings.HasPrefix(boxStatus(t, f, id3).ShareKey, "error: ") })
	if got := boxStatus(t, f, id3).ShareKey; got != "error: "+ErrBoxKeyLinked.Error() {
		t.Fatalf("share key %q", got)
	}
	waitFor(t, "the log line", func() bool { return countContaining(logs(), "box "+id3+": another key is linked") == 1 })
	if strings.Contains(strings.Join(logs(), "\n"), testSecret) {
		t.Fatal("the fleet secret reached the log")
	}
}

func countContaining(lines []string, s string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, s) {
			n++
		}
	}
	return n
}

func TestRegisterOwnBoxKey(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	n.after.NodeID = "3"
	if _, ok := f.ShareRelay(); ok {
		t.Fatal("a box with no relay shares through none")
	}
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if relay, ok := f.ShareRelay(); !ok || relay != r.URL {
		t.Fatalf("ShareRelay %q %v", relay, ok)
	}
	key, err := f.BoxKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.RegisterOwnBoxKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := r.puts(); len(p) != 1 || p[0] != (boxKeyPut{"3", b64(key.Public().(ed25519.PublicKey)), "Bearer " + testSecret}) {
		t.Fatalf("relay saw %+v", p)
	}

	m, _, _ := relayMember(t)
	if err := m.Join(relayCode().String()); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterOwnBoxKey(context.Background()); !errors.Is(err, ErrNotFleetOwner) {
		t.Fatalf("a member got %v", err)
	}
	if relay, ok := m.ShareRelay(); !ok || relay != testRelay {
		t.Fatalf("member ShareRelay %q %v", relay, ok)
	}
}

// hookProbe is an OnLeavingRelay that counts its calls and keeps what snap saw at each.
type hookProbe struct {
	mu    sync.Mutex
	calls int
	seen  [][]string
}

func (h *hookProbe) hook(snap func() []string) func() {
	return func() {
		s := snap()
		h.mu.Lock()
		defer h.mu.Unlock()
		h.calls++
		h.seen = append(h.seen, s)
	}
}

func (h *hookProbe) count() int { h.mu.Lock(); defer h.mu.Unlock(); return h.calls }

func (h *hookProbe) saw(call string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.ContainsFunc(h.seen, func(s []string) bool { return slices.Contains(s, call) })
}

func TestLeavingTheRelayCallsTheHookFirst(t *testing.T) {
	const delFleet = "DELETE /v1/fleets/fl1"
	ctx := context.Background()

	// DisableMaster: before the fleet is deleted
	f, _, r, _ := relayMaster(t)
	var p hookProbe
	f.OnLeavingRelay = p.hook(r.seenCalls)
	if err := f.EnableMasterRelay(ctx, r.URL); err != nil {
		t.Fatal(err)
	}
	if err := f.DisableMaster(); err != nil {
		t.Fatal(err)
	}
	if p.count() != 1 || p.saw(delFleet) || r.count(&r.deletes) != 1 {
		t.Fatalf("DisableMaster: hook %d times, saw the delete %v, deletes %d", p.count(), p.saw(delFleet), r.deletes)
	}

	// a member's Leave: before the logout
	m, n, _ := relayMember(t)
	var pm hookProbe
	m.OnLeavingRelay = pm.hook(func() []string {
		if n.called("down") {
			return []string{"down"}
		}
		return nil
	})
	if err := m.Join(relayCode().String()); err != nil {
		t.Fatal(err)
	}
	if err := m.Leave(); err != nil {
		t.Fatal(err)
	}
	if pm.count() != 1 || pm.saw("down") || !n.called("down") {
		t.Fatalf("Leave: hook %d times, saw the logout %v, logged out %v", pm.count(), pm.saw("down"), n.called("down"))
	}

	// a member without a relay has nothing to leave
	m2, _, _ := relayMember(t)
	var p2 hookProbe
	m2.OnLeavingRelay = p2.hook(func() []string { return nil })
	if err := m2.Join(Code{MasterURL: "https://master.example", BoxID: "b1", Key: "k1"}.String()); err != nil {
		t.Fatal(err)
	}
	if err := m2.Leave(); err != nil || p2.count() != 0 {
		t.Fatalf("Leave without relay: %v, hook %d times", err, p2.count())
	}

	// ChangeRelay: before the old fleet is deleted; an invalid address stops before the hook
	f3, n3, r3, _ := relayMaster(t)
	var p3 hookProbe
	f3.OnLeavingRelay = p3.hook(r3.seenCalls)
	if err := f3.EnableMasterRelay(ctx, r3.URL); err != nil {
		t.Fatal(err)
	}
	if err := f3.ChangeRelay(ctx, "ftp://nope"); err == nil || p3.count() != 0 {
		t.Fatalf("invalid relay: %v, hook %d times", err, p3.count())
	}
	r4 := newFakeRelay(t)
	if err := f3.ChangeRelay(ctx, r4.URL); err != nil {
		t.Fatal(err)
	}
	if p3.count() != 1 || p3.saw(delFleet) || r3.count(&r3.deletes) != 1 || !n3.called("down") {
		t.Fatalf("ChangeRelay: hook %d times, saw the delete %v, deletes %d", p3.count(), p3.saw(delFleet), r3.deletes)
	}
}
