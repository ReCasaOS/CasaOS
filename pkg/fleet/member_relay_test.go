package fleet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testRelay = "https://relay.example"

// relayCode is a code for a master over testRelay.
func relayCode() Code {
	return Code{MasterURL: "https://100.64.0.1", BoxID: "b1", Key: "k1", Relay: testRelay, JoinKey: testJoinKey}
}

// relayMember is a box with no role yet, a fake private network that gives it
// 100.64.0.9 on testRelay, and its log kept. No loop runs.
func relayMember(t *testing.T) (*Fleet, *fakeNet, func() []string) {
	t.Helper()
	f := New(t.TempDir())
	f.Hostname = func() (string, error) { return "My_Box", nil }
	var mu sync.Mutex
	var logs []string
	f.Log = func(msg string, err error) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprint(msg, " ", err))
	}
	n := &fakeNet{after: NetState{Connected: true, Addr: "100.64.0.9", LoginServer: testRelay}}
	f.Net = n
	return f, n, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), logs...) }
}

func memberJSON(f *Fleet) string { return filepath.Join(f.Root, StateDir, "member.json") }

func TestJoinWithRelayCodeBringsNetworkUp(t *testing.T) {
	f, n, _ := relayMember(t)
	if err := f.Join(relayCode().String()); err != nil {
		t.Fatal(err)
	}
	if len(n.calls) != 1 || n.calls[0] != "up "+testRelay+" "+testJoinKey+" b-b1" {
		t.Fatalf("net calls %q", n.calls)
	}
	st, ok := f.loadMember()
	if !ok || st.Relay != testRelay || st.BoxID != "b1" {
		t.Fatalf("member.json %+v %v", st, ok)
	}
	ms, _ := f.MemberStatus()
	if ms.Relay != testRelay || !ms.NetConnected || ms.NetError != "" {
		t.Fatalf("status %+v", ms)
	}
}

func TestJoinKeyIsNotPersisted(t *testing.T) {
	f, n, logs := relayMember(t)
	if err := f.Join(relayCode().String()); err != nil {
		t.Fatal(err)
	}
	var all []string
	filepath.Walk(f.Root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			all = append(all, readFile(t, p))
		}
		return nil
	})
	if len(all) == 0 {
		t.Fatal("no state written")
	}
	// a Net that puts the key in its error
	n.up, n.upErr = false, errors.New("up failed for "+testJoinKey)
	err := f.Join(relayCode().String())
	if err == nil {
		t.Fatal("joined though up failed")
	}
	all = append(all, err.Error())
	// up, no address in time, and a network that cannot be left
	oldWait, oldStep := addrWait, addrStep
	t.Cleanup(func() { addrWait, addrStep = oldWait, oldStep })
	addrWait, addrStep = 20*time.Millisecond, 5*time.Millisecond
	n.upErr, n.downErr, n.after.Addr = nil, errors.New("down failed"), ""
	if err := f.Join(relayCode().String()); err == nil {
		t.Fatal("joined with no address")
	}
	all = append(all, logs()...)
	if len(logs()) == 0 {
		t.Fatal("the failed down was not logged")
	}
	for _, s := range all {
		if strings.Contains(s, testJoinKey) {
			t.Fatalf("join key leaked: %s", s)
		}
	}
}

func TestJoinRelayOtherNetworkSavesNothing(t *testing.T) {
	f, n, _ := relayMember(t)
	n.upErr = ErrOtherNetwork
	if err := f.Join(relayCode().String()); !errors.Is(err, ErrOtherNetwork) {
		t.Fatalf("err %v", err)
	}
	if exists(t, memberJSON(f)) || n.called("down") {
		t.Fatalf("member.json saved or the other network left: %q", n.calls)
	}
}

// Up undoes its own failure (Net's contract): Join leaves the network only
// once Up succeeded and a later step failed.
func TestJoinRelayUpFailsSavesNothing(t *testing.T) {
	f, n, _ := relayMember(t)
	n.upErr = errors.New("tailscale up: timeout")
	if err := f.Join(relayCode().String()); err == nil {
		t.Fatal("joined")
	}
	if exists(t, memberJSON(f)) || n.called("down") {
		t.Fatalf("saved, or a down after a failed up: %q", n.calls)
	}
	// State unreadable: Up ran nothing, nothing to undo
	n.upErr, n.calls = errors.New("tailscale status: unreadable output"), nil
	if err := f.Join(relayCode().String()); err == nil || n.called("down") {
		t.Fatalf("err %v, calls %q", err, n.calls)
	}

	// up, but no address in time
	oldWait, oldStep := addrWait, addrStep
	t.Cleanup(func() { addrWait, addrStep = oldWait, oldStep })
	addrWait, addrStep = 50*time.Millisecond, 5*time.Millisecond
	f, n, _ = relayMember(t)
	n.after.Addr = ""
	if err := f.Join(relayCode().String()); err == nil || !strings.Contains(err.Error(), "no address") {
		t.Fatalf("err %v", err)
	}
	if exists(t, memberJSON(f)) || !n.called("down") {
		t.Fatalf("saved, or no down: %q", n.calls)
	}

	// no private network, or this box is a master: nothing asked of the network
	f, n, _ = relayMember(t)
	f.Net = nil
	if err := f.Join(relayCode().String()); !errors.Is(err, ErrNoPrivateNet) {
		t.Fatalf("err %v", err)
	}
	f.Net = n
	if err := f.EnableMaster("https://maison.example"); err != nil {
		t.Fatal(err)
	}
	if err := f.Join(relayCode().String()); !errors.Is(err, errIsMaster) || len(n.calls) != 0 {
		t.Fatalf("err %v, calls %q", err, n.calls)
	}
}

// A code without a join key (made while the box was online) joins only a box
// already on the relay's network; any other is told to ask for a new code.
func TestJoinCodeWithoutJoinKey(t *testing.T) {
	c := relayCode()
	c.JoinKey = ""
	f, n, _ := relayMember(t)
	if err := f.Join(c.String()); err == nil || !strings.Contains(err.Error(), "new code") {
		t.Fatalf("off the network: %v", err)
	}
	if exists(t, memberJSON(f)) || len(n.calls) != 0 {
		t.Fatalf("saved, or calls %q", n.calls)
	}
	n.before = NetState{Connected: true, Addr: "100.64.0.9", LoginServer: testRelay}
	if err := f.Join(c.String()); err != nil {
		t.Fatal(err)
	}
	if st, ok := f.loadMember(); !ok || st.Relay != testRelay || len(n.calls) != 0 {
		t.Fatalf("member.json %+v, calls %q", st, n.calls)
	}
}

func TestJoinOldCodeNeverTouchesNet(t *testing.T) {
	f, n, _ := relayMember(t)
	c := relayCode()
	c.Relay, c.JoinKey = "", ""
	if err := f.Join(c.String()); err != nil {
		t.Fatal(err)
	}
	ms, _ := f.MemberStatus()
	if err := f.Leave(); err != nil {
		t.Fatal(err)
	}
	if len(n.calls) != 0 || n.states != 0 || ms.Relay != "" || ms.NetError != "" {
		t.Fatalf("calls %q, states %d, status %+v", n.calls, n.states, ms)
	}
	if strings.Contains(readFileOr(memberJSON(f)), "relay") {
		t.Fatal("member.json names a relay")
	}
}

func readFileOr(p string) string { b, _ := os.ReadFile(p); return string(b) }

func TestJoinOtherRelayLeavesTheOldNetwork(t *testing.T) {
	f, n, _ := relayMember(t)
	if err := f.Join(relayCode().String()); err != nil {
		t.Fatal(err)
	}
	c := relayCode()
	c.Relay = "https://relay2.example" // the master changed relay
	if err := f.Join(c.String()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(n.calls, ",") != "up "+testRelay+" "+testJoinKey+" b-b1,down,up "+c.Relay+" "+testJoinKey+" b-b1" {
		t.Fatalf("calls %q", n.calls)
	}
}

func TestLeaveBringsNetworkDown(t *testing.T) {
	f, n, _ := relayMember(t)
	if err := f.Join(relayCode().String()); err != nil {
		t.Fatal(err)
	}
	if err := f.Leave(); err != nil {
		t.Fatal(err)
	}
	if !n.called("down") || exists(t, memberJSON(f)) {
		t.Fatalf("calls %q", n.calls)
	}
}

func TestLeaveRelayDownStillLeaves(t *testing.T) {
	f, n, logs := relayMember(t)
	if err := f.Join(relayCode().String()); err != nil {
		t.Fatal(err)
	}
	n.downErr = errors.New("tailscale logout: daemon not responding")
	if err := f.Leave(); err != nil {
		t.Fatal(err)
	}
	if exists(t, memberJSON(f)) || len(logs()) != 1 {
		t.Fatalf("member.json kept, or logs %q", logs())
	}
	if _, ok := f.MemberStatus(); ok {
		t.Fatal("still a member")
	}
}

func TestMemberStatusCachesNetState(t *testing.T) {
	f, n, _ := relayMember(t)
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	f.Now = func() time.Time { return now }
	if err := f.Join(relayCode().String()); err != nil {
		t.Fatal(err)
	}
	n.states = 0
	for range 3 {
		f.MemberStatus()
	}
	if n.states != 1 {
		t.Fatalf("State asked %d times", n.states)
	}
	now = now.Add(netEvery)
	n.mu.Lock()
	n.up = false // stopped after a reboot, say
	n.mu.Unlock()
	ms, _ := f.MemberStatus()
	if n.states != 2 || ms.NetConnected || !strings.Contains(ms.NetError, "new code") {
		t.Fatalf("states %d, status %+v", n.states, ms)
	}
	if len(n.calls) != 1 {
		t.Fatalf("the network was repaired behind the owner's back: %q", n.calls)
	}
}

// relayJoin enrols member with master over a fake relay network and waits for its tunnel.
func relayJoin(t *testing.T, master, member *Fleet, n *fakeNet) Code {
	t.Helper()
	code := addBox(t, master, "Maison")
	code.Relay, code.JoinKey = testRelay, testJoinKey
	member.Net = n
	if err := member.Join(code.String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "online", online(master, code.BoxID))
	return code
}

func TestRelayDownKeepsMemberRetrying(t *testing.T) {
	shortWaits(t)
	master, srv := newMaster(t)
	member := newMember(t, &fakeGateway{status: 200})
	n := &fakeNet{after: NetState{Connected: true, Addr: "100.64.0.9", LoginServer: testRelay}}
	code := relayJoin(t, master, member, n)

	n.mu.Lock()
	n.stateErr = errors.New("tailscale status: relay unreachable")
	n.mu.Unlock()
	master.dropTunnels()
	waitFor(t, "online again, the network unreadable", online(master, code.BoxID))
	ms, _ := member.MemberStatus()
	if ms.NetConnected || !strings.Contains(ms.NetError, "relay unreachable") {
		t.Fatalf("status %+v", ms)
	}

	srv.Close()
	master.dropTunnels()
	last := time.Time{}
	for i := range 3 {
		waitFor(t, fmt.Sprint("try ", i), func() bool {
			st, ok := member.MemberStatus()
			if ok && st.LastTry.After(last) && st.LastError != "" && !st.Connected {
				last = st.LastTry
				return true
			}
			return false
		})
	}
	if _, ok := member.loadMember(); !ok {
		t.Fatal("the member forgot its master")
	}
}

// A member takes the name "b-<box id>" on the relay's network, the name the
// master removes it by; a code whose box id is no DNS label falls back to the
// box's hostname.
func TestMemberNodeIsNamedByBoxID(t *testing.T) {
	for id, want := range map[string]string{
		"6f1c2d3e4a5b6c7d": "b-6f1c2d3e4a5b6c7d", "B_1": "my-box", strings.Repeat("a", 41): "my-box",
	} {
		f, n, _ := relayMember(t)
		c := relayCode()
		c.BoxID = id
		if err := f.Join(c.String()); err != nil {
			t.Fatal(err)
		}
		if n.calls[0] != "up "+testRelay+" "+testJoinKey+" "+want {
			t.Errorf("%q: net calls %q, want %s", id, n.calls, want)
		}
	}
}

func TestNetHostname(t *testing.T) {
	long := strings.Repeat("a", 62) + "_b"
	for in, want := range map[string]string{
		"maison": "maison", "My Box_2": "my-box-2", "-Café-": "caf", "": "recasaos-box", "__": "recasaos-box",
		long: strings.Repeat("a", 62), strings.Repeat("x", 70): strings.Repeat("x", 63),
	} {
		if got := netHostname(in); got != want {
			t.Errorf("netHostname(%q) = %q, want %q", in, got, want)
		}
	}
}
