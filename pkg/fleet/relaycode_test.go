package fleet

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
)

// relayOn is relayMaster with the master enabled over its fake relay.
func relayOn(t *testing.T) (*Fleet, *fakeNet, *fakeRelay, func() []string) {
	t.Helper()
	f, n, r, logs := relayMaster(t)
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	return f, n, r, logs
}

// said records the hostname a box gave in its hello.
func said(t *testing.T, f *Fleet, id, hostname string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.modifyBox(id, func(b *Box) error { b.Hostname = hostname; return nil }); err != nil {
		t.Fatal(err)
	}
}

func boxStatus(t *testing.T, f *Fleet, id string) BoxStatus {
	t.Helper()
	st, _ := f.MasterStatus()
	for _, b := range st.Boxes {
		if b.ID == id {
			return b
		}
	}
	t.Fatalf("no box %s", id)
	return BoxStatus{}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAddBoxRelayCodeCarriesJoinKey(t *testing.T) {
	f, _, r, _ := relayOn(t)
	_, code, err := f.AddBox("box a", "", false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseCode(code.String())
	if err != nil {
		t.Fatal(err)
	}
	if c.Relay != r.URL || c.JoinKey != testJoinKey || c.MasterURL != "https://100.64.0.7" {
		t.Fatalf("%+v", c)
	}
	for _, path := range []string{f.masterPath(), f.relayPath()} {
		if strings.Contains(readFile(t, path), testJoinKey) {
			t.Fatalf("%s keeps the join key", path)
		}
	}
	// this box itself is already on the network: no join key is spent on it
	keys := r.count(&r.keys)
	if _, self, err := f.AddBox("this box", "", true); err != nil || self.Relay != "" || self.JoinKey != "" || r.count(&r.keys) != keys {
		t.Fatalf("self: %+v %v, keys %d", self, err, r.keys)
	}
}

func TestAdvancedAddBoxUnchanged(t *testing.T) {
	f, _ := newMaster(t)
	f.RelayAPI = nil // any relay call would panic
	box, code, err := f.AddBox("box a", "", false)
	if err != nil || code.Relay != "" || code.JoinKey != "" {
		t.Fatalf("%+v %v", code, err)
	}
	if _, err := ParseCode(code.String()); err != nil {
		t.Fatal(err)
	}
	if code, err = f.RotateCode(box.ID); err != nil || code.Relay != "" || code.JoinKey != "" {
		t.Fatalf("%+v %v", code, err)
	}
	said(t, f, box.ID, "box-1")
	if err := f.RemoveBox(box.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.ChangeRelay(context.Background(), "https://relay.example"); err == nil {
		t.Fatal("an advanced master changed relay")
	}
}

func TestAddBoxRelayQuotaIsReadable(t *testing.T) {
	f, _, r, _ := relayOn(t)
	box, _, err := f.AddBox("box a", "", false)
	if err != nil {
		t.Fatal(err)
	}
	hash := readFile(t, f.masterPath())
	r.set(func(r *fakeRelay) { r.keyStatus = http.StatusConflict })
	_, _, err = f.AddBox("box b", "", false)
	if !errors.Is(err, ErrFleetFull) || !errors.Is(err, ErrRelayQuota) || !strings.Contains(err.Error(), "full") {
		t.Fatalf("got %v", err)
	}
	if st, _ := f.MasterStatus(); len(st.Boxes) != 1 {
		t.Fatalf("a box was added: %+v", st.Boxes)
	}
	if _, err := f.RotateCode(box.ID); !errors.Is(err, ErrFleetFull) {
		t.Fatalf("got %v", err)
	}
	if readFile(t, f.masterPath()) != hash {
		t.Fatal("a failed rotation changed the box's key")
	}
}

func TestAddBoxRelayFleetGoneIsReadable(t *testing.T) {
	f, _, r, _ := relayOn(t)
	r.set(func(r *fakeRelay) { r.keyStatus = http.StatusNotFound })
	_, _, err := f.AddBox("box a", "", false)
	if !errors.Is(err, ErrFleetGone) || !errors.Is(err, ErrRelayFleetGone) || !strings.Contains(err.Error(), "enable") {
		t.Fatalf("got %v", err)
	}
	if st, _ := f.MasterStatus(); len(st.Boxes) != 0 {
		t.Fatalf("a box was added: %+v", st.Boxes)
	}
}

func TestRemoveBoxRemovesRelayNode(t *testing.T) {
	f, _, r, _ := relayOn(t)
	ids := map[string]string{}
	for _, name := range []string{"a", "quiet", "liar", "twin", "twin2"} {
		box, _, err := f.AddBox(name, "", false)
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = box.ID
	}
	self, _, err := f.AddBox("this box", "", true)
	if err != nil {
		t.Fatal(err)
	}
	said(t, f, ids["a"], "Box_1")     // removed by the name it joined with
	said(t, f, ids["liar"], "MAISON") // the master's own hostname
	said(t, f, ids["twin"], "box-2")
	said(t, f, ids["twin2"], "Box.2") // the same name on the network
	said(t, f, self.ID, "maison")
	for _, id := range []string{ids["a"], ids["quiet"], ids["liar"], ids["twin"], self.ID} {
		if err := f.RemoveBox(id); err != nil {
			t.Fatal(err)
		}
	}
	r.mu.Lock()
	removed := strings.Join(r.removed, ",")
	r.mu.Unlock()
	// no hello, the master's name, a name another enrolled box still uses, and this box itself: left alone
	if removed != "box-1" {
		t.Fatalf("removed at the relay: %q", removed)
	}
}

func TestRemoveBoxRelayDownStillRemoves(t *testing.T) {
	f, _, r, logs := relayOn(t)
	box, _, err := f.AddBox("box a", "", false)
	if err != nil {
		t.Fatal(err)
	}
	said(t, f, box.ID, "box-1")
	r.Close()
	if err := f.RemoveBox(box.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.MasterStatus(); len(st.Boxes) != 0 {
		t.Fatalf("%+v", st.Boxes)
	}
	if !strings.Contains(strings.Join(logs(), "\n"), "cannot remove the box from the relay") {
		t.Fatalf("not logged: %v", logs())
	}
}

func TestChangeRelayReissuesCodes(t *testing.T) {
	f, n, r, _ := relayOn(t)
	box, _, err := f.AddBox("box a", "", false)
	if err != nil {
		t.Fatal(err)
	}
	self, _, err := f.AddBox("this box", "", true)
	if err != nil {
		t.Fatal(err)
	}
	hash := readFile(t, f.masterPath())
	n.mu.Lock()
	n.after.Addr = "100.64.0.8" // the new network gives another address
	n.mu.Unlock()
	r2 := newFakeRelay(t)
	if err := f.ChangeRelay(context.Background(), r2.URL+"/"); err != nil {
		t.Fatal(err)
	}
	st, _ := f.MasterStatus()
	if st.Relay != r2.URL || st.Address != "https://100.64.0.8" || st.NetAddr != "100.64.0.8" {
		t.Fatalf("%+v", st)
	}
	if !strings.Contains(readFile(t, f.relayPath()), r2.URL) || r.count(&r.deletes) != 1 {
		t.Fatalf("relay.json %s, old deletes %d", readFile(t, f.relayPath()), r.deletes)
	}
	if !n.called("down") || !n.called("up "+r2.URL+" "+testJoinKey+" maison") {
		t.Fatalf("calls %v", n.calls)
	}
	if !boxStatus(t, f, box.ID).NeedsRecode || boxStatus(t, f, self.ID).NeedsRecode {
		t.Fatalf("%+v", st.Boxes)
	}
	if strings.Contains(hash, "needs_recode") || !strings.Contains(readFile(t, f.masterPath()), `"needs_recode":true`) {
		t.Fatal("needs_recode not persisted as said")
	}
	code, err := f.RotateCode(box.ID)
	if err != nil {
		t.Fatal(err)
	}
	if code.Relay != r2.URL || code.JoinKey != testJoinKey || code.MasterURL != "https://100.64.0.8" || boxStatus(t, f, box.ID).NeedsRecode {
		t.Fatalf("%+v", code)
	}
	// the same relay again is a new fleet there (how a forgotten one is replaced): new codes again
	if err := f.ChangeRelay(context.Background(), r2.URL); err != nil || r2.count(&r2.creates) != 2 || !boxStatus(t, f, box.ID).NeedsRecode {
		t.Fatalf("%v, creates %d", err, r2.creates)
	}
}

func TestChangeRelayRelayDownLeavesStateIntact(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	if err := f.ChangeRelay(context.Background(), r.URL); err == nil {
		t.Fatal("changed the relay of a master that is off")
	}
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.AddBox("box a", "", false); err != nil {
		t.Fatal(err)
	}
	master, relay := readFile(t, f.masterPath()), readFile(t, f.relayPath())
	n.calls = nil
	dead := newFakeRelay(t)
	dead.Close()
	if err := f.ChangeRelay(context.Background(), dead.URL); !errors.Is(err, ErrRelayUnreachable) {
		t.Fatalf("got %v", err)
	}
	// a new relay that creates the fleet then refuses a join key: its fleet is dropped
	r2 := newFakeRelay(t)
	r2.set(func(r *fakeRelay) { r.keyStatus = http.StatusServiceUnavailable })
	if err := f.ChangeRelay(context.Background(), r2.URL); !errors.Is(err, ErrRelayBusy) || r2.count(&r2.deletes) != 1 {
		t.Fatalf("got %v, deletes %d", err, r2.deletes)
	}
	if err := f.ChangeRelay(context.Background(), "http://evil.example"); err == nil {
		t.Fatal("a plain-http relay was used")
	}
	if len(n.calls) != 0 || r.count(&r.deletes) != 0 || readFile(t, f.masterPath()) != master || readFile(t, f.relayPath()) != relay {
		t.Fatalf("something changed: calls %v, old deletes %d", n.calls, r.deletes)
	}
}

// Up failing once the old network was left: the box is off any private network,
// the state still names the old relay, the new fleet is dropped, and the error says
// to change the relay again, which then works.
func TestChangeRelayUpFailsAfterDown(t *testing.T) {
	f, n, r, _ := relayOn(t)
	box, _, err := f.AddBox("box a", "", false)
	if err != nil {
		t.Fatal(err)
	}
	master, relay := readFile(t, f.masterPath()), readFile(t, f.relayPath())
	n.upErr = errors.New("tailscale up: boom")
	r2 := newFakeRelay(t)
	err = f.ChangeRelay(context.Background(), r2.URL)
	if err == nil || !strings.Contains(err.Error(), "off the private network") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
	if readFile(t, f.masterPath()) != master || readFile(t, f.relayPath()) != relay {
		t.Fatal("the state changed")
	}
	if r2.count(&r2.deletes) != 1 || r.count(&r.deletes) != 0 || !n.called("down") {
		t.Fatalf("new deletes %d, old deletes %d, calls %v", r2.deletes, r.deletes, n.calls)
	}
	n.upErr = nil
	if err := f.ChangeRelay(context.Background(), r2.URL); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.MasterStatus(); st.Relay != r2.URL || !boxStatus(t, f, box.ID).NeedsRecode {
		t.Fatalf("%+v", st)
	}
}

func TestNoSecretOrJoinKeyInLogs(t *testing.T) {
	f, _, r, logs := relayOn(t)
	var errs []string
	box, _, err := f.AddBox("box a", "", false)
	if err != nil {
		t.Fatal(err)
	}
	said(t, f, box.ID, "box-1")
	r.Close()
	if err := f.RemoveBox(box.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.AddBox("box b", "", false); err != nil {
		errs = append(errs, err.Error())
	}
	r2 := newFakeRelay(t)
	if err := f.ChangeRelay(context.Background(), r2.URL); err != nil { // the old fleet cannot be deleted: logged
		t.Fatal(err)
	}
	all := strings.Join(append(logs(), errs...), "\n")
	if len(logs()) < 2 || len(errs) != 1 {
		t.Fatalf("logs %v, errors %v", logs(), errs)
	}
	for _, s := range []string{testSecret, testJoinKey, "Bearer"} {
		if strings.Contains(all, s) {
			t.Fatalf("%q leaked: %s", s, all)
		}
	}
}
