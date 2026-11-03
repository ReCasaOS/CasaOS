package fleet

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTS stands in for the tailscale command and for systemctl, and records
// every call. tailscaled is active (or enabled) when said; once started by
// systemctl, status answers startedStatus when that is set.
type fakeTS struct {
	status, prefs   string
	statusErr       error
	upErr           error
	logoutErr       error
	sysErr          error
	active, enabled bool
	startedStatus   string
	started         bool
	calls           []string
}

func (f *fakeTS) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, "tailscale "+strings.Join(args, " "))
	switch args[0] {
	case "status":
		if f.started && f.startedStatus != "" {
			return []byte(f.startedStatus), nil
		}
		return []byte(f.status), f.statusErr
	case "debug":
		return []byte(f.prefs), nil
	case "up":
		return nil, f.upErr
	case "logout":
		return nil, f.logoutErr
	}
	return nil, errors.New("unexpected command")
}

func (f *fakeTS) systemctl(_ context.Context, args ...string) error {
	f.calls = append(f.calls, "systemctl "+strings.Join(args, " "))
	inactive := errors.New("exit status 3")
	switch {
	case args[0] == "is-active" && !f.active, args[0] == "is-enabled" && !f.enabled:
		return inactive
	case args[0] == "is-active", args[0] == "is-enabled":
		return nil
	case args[0] == "enable" && f.sysErr == nil:
		f.started = true
	}
	return f.sysErr
}

func (f *fakeTS) net() Net {
	n := NewTailscale(f.run).(*tailscale)
	n.systemctl = f.systemctl
	return n
}

func (f *fakeTS) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// tailscaled models the daemon behind the real tailscale Net: the command
// answers only while it is active, login is the server it is logged in to
// (kept on disk while it is stopped), and up refuses another server, as
// tailscale does without --force-reauth.
type tailscaled struct {
	mu              sync.Mutex
	active, enabled bool
	login           string
	calls           []string
}

func (d *tailscaled) run(_ context.Context, args ...string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "tailscale "+strings.Join(args, " "))
	if !d.active {
		return nil, errors.New("exit status 1: failed to connect to local tailscaled")
	}
	switch args[0] {
	case "status":
		if d.login == "" {
			return []byte(`{"BackendState":"NeedsLogin"}`), nil
		}
		return []byte(runningJSON), nil
	case "debug":
		return []byte(prefsFor(d.login)), nil
	case "logout":
		d.login = ""
		return nil, nil
	case "up":
		server := strings.TrimPrefix(args[1], "--login-server=")
		if d.login != "" && !sameURL(d.login, server) {
			return nil, errors.New("can't change --login-server without --force-reauth")
		}
		d.login = server
		return nil, nil
	}
	return nil, errors.New("unexpected command")
}

func (d *tailscaled) systemctl(_ context.Context, args ...string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "systemctl "+strings.Join(args, " "))
	switch {
	case args[0] == "is-active" && !d.active, args[0] == "is-enabled" && !d.enabled:
		return errors.New("exit status 3")
	case args[0] == "enable":
		d.active, d.enabled = true, true
	case args[0] == "disable":
		d.active, d.enabled = false, false
	case args[0] == "stop":
		d.active = false
	}
	return nil
}

func (d *tailscaled) net() Net {
	n := NewTailscale(d.run).(*tailscale)
	n.systemctl = d.systemctl
	return n
}

// ups is every `tailscale up` run, in order.
func (d *tailscaled) ups() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var ups []string
	for _, c := range d.calls {
		if strings.HasPrefix(c, "tailscale up ") {
			ups = append(ups, c)
		}
	}
	return ups
}

const runningJSON = `{"BackendState":"Running","Self":{"TailscaleIPs":["fd7a:115c:a1e0::1","100.64.0.7"]}}`

func prefsFor(url string) string { return `{"ControlURL":"` + url + `","WantRunning":true}` }

func TestStateParsesRunningAndStopped(t *testing.T) {
	for _, c := range []struct {
		name, status, prefs string
		statusErr           error
		want                NetState
		wantErr             bool
	}{
		{"running", runningJSON, prefsFor("https://relay.example/"), nil,
			NetState{Connected: true, Addr: "100.64.0.7", LoginServer: "https://relay.example"}, false},
		{"running, only v6", `{"BackendState":"Running","Self":{"TailscaleIPs":["fd7a::1"]}}`, prefsFor("https://r"), nil,
			NetState{Connected: true, Addr: "fd7a::1", LoginServer: "https://r"}, false},
		{"running, default control", runningJSON, `{"ControlURL":""}`, nil,
			NetState{Connected: true, Addr: "100.64.0.7", LoginServer: defaultControlURL}, false},
		{"stopped, logged in elsewhere", `{"BackendState":"Stopped"}`, prefsFor(defaultControlURL), nil,
			NetState{LoginServer: defaultControlURL}, false},
		{"starting", `{"BackendState":"Starting"}`, prefsFor("https://relay.example/"), nil,
			NetState{LoginServer: "https://relay.example"}, false},
		{"stopped, prefs unreadable", `{"BackendState":"Stopped"}`, "nope", nil, NetState{}, true},
		{"no state", `{"BackendState":"NoState"}`, "", nil, NetState{}, false},
		{"needs login", `{"BackendState":"NeedsLogin"}`, "", nil, NetState{}, false},
		{"daemon down", "", "", errors.New("exit status 1"), NetState{}, false},
		{"not installed", "", "", exec.ErrNotFound, NetState{}, true},
		{"garbage", "not json", "", nil, NetState{}, true},
		{"running, prefs unreadable", runningJSON, "nope", nil, NetState{}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeTS{status: c.status, prefs: c.prefs, statusErr: c.statusErr}
			got, err := f.net().State(context.Background())
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, want error %v", err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestEnableRefusesAnotherTailnet(t *testing.T) {
	f := &fakeTS{status: runningJSON, prefs: prefsFor("https://controlplane.tailscale.com")}
	err := f.net().Up(context.Background(), "https://relay.example", "tskey-secret-0123", "box")
	if !errors.Is(err, ErrOtherNetwork) {
		t.Fatalf("err = %v, want ErrOtherNetwork", err)
	}
	if f.called("tailscale up") || f.called("systemctl") {
		t.Errorf("a box in another network must be left untouched, calls: %v", f.calls)
	}
}

func TestUpPassesLoginServerAndKey(t *testing.T) {
	f := &fakeTS{status: `{"BackendState":"NeedsLogin"}`}
	if err := f.net().Up(context.Background(), "https://relay.example/", "tskey-secret-0123", "box1"); err != nil {
		t.Fatal(err)
	}
	if want := "systemctl enable --now tailscaled"; len(f.calls) < 3 || f.calls[len(f.calls)-3] != want {
		t.Fatalf("tailscaled must be started, then asked again, before up; calls: %v", f.calls)
	}
	up := f.calls[len(f.calls)-1]
	for _, want := range []string{"up ", "--login-server=https://relay.example/", "--authkey=tskey-secret-0123", "--hostname=box1", "--accept-dns=false", "--accept-routes=false"} {
		if !strings.Contains(up, want) {
			t.Errorf("up call %q lacks %q", up, want)
		}
	}
}

func TestUpErrorNeverContainsAuthKey(t *testing.T) {
	const key = "tskey-secret-0123"
	f := &fakeTS{status: `{"BackendState":"NeedsLogin"}`, upErr: errors.New("up --authkey=" + key + " failed: bad key " + key)}
	err := f.net().Up(context.Background(), "https://relay.example", key, "box")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("error leaks the key: %v", err)
	}
}

// A tailscale the owner logged into another network, then stopped, is still
// theirs: nothing is run on it.
func TestUpLeavesAStoppedTailscaleOfAnotherNetwork(t *testing.T) {
	for _, state := range []string{"Stopped", "Starting"} {
		f := &fakeTS{active: true, status: `{"BackendState":"` + state + `"}`, prefs: prefsFor(defaultControlURL)}
		if err := f.net().Up(context.Background(), "https://relay.example", testJoinKey, "box"); !errors.Is(err, ErrOtherNetwork) {
			t.Fatalf("%s: err = %v", state, err)
		}
		if f.called("tailscale up") || f.called("tailscale logout") || f.called("systemctl") {
			t.Errorf("%s: calls %v", state, f.calls)
		}
	}
}

// tailscaled stopped, but logged into another network: it shows only once
// started. Up puts it back as it was and runs nothing else.
func TestUpRestoresTailscaledWhenItOnlyNowAnswers(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		f := &fakeTS{enabled: enabled, statusErr: errors.New("exit status 1"), prefs: prefsFor(defaultControlURL),
			startedStatus: `{"BackendState":"Stopped"}`}
		if err := f.net().Up(context.Background(), "https://relay.example", testJoinKey, "box"); !errors.Is(err, ErrOtherNetwork) {
			t.Fatalf("enabled %v: err = %v", enabled, err)
		}
		undo := "systemctl disable --now tailscaled"
		if enabled {
			undo = "systemctl stop tailscaled"
		}
		if !f.called("systemctl enable --now tailscaled") || f.calls[len(f.calls)-1] != undo {
			t.Errorf("enabled %v: tailscaled not put back, calls %v", enabled, f.calls)
		}
		if f.called("tailscale up") || f.called("tailscale logout") || enabled && f.called("systemctl disable") {
			t.Errorf("enabled %v: calls %v", enabled, f.calls)
		}
	}
}

// A failed up undoes what Up did, and only that.
func TestUpFailureUndoesOnlyWhatItDid(t *testing.T) {
	boom := errors.New("timeout")
	// a box with tailscaled off: logged out again, tailscaled off again
	f := &fakeTS{statusErr: errors.New("exit status 1"), startedStatus: `{"BackendState":"NeedsLogin"}`, upErr: boom}
	if err := f.net().Up(context.Background(), "https://relay.example", testJoinKey, "box"); err == nil {
		t.Fatal("no error")
	}
	if n := len(f.calls); n < 2 || f.calls[n-2] != "tailscale logout" || f.calls[n-1] != "systemctl disable --now tailscaled" {
		t.Errorf("calls %v", f.calls)
	}
	// tailscaled already running, logged in nowhere: logged out again, left running
	f = &fakeTS{active: true, status: `{"BackendState":"NeedsLogin"}`, upErr: boom}
	if err := f.net().Up(context.Background(), "https://relay.example", testJoinKey, "box"); err == nil {
		t.Fatal("no error")
	}
	if !f.called("tailscale logout") || f.called("systemctl enable") || f.called("systemctl disable") || f.called("systemctl stop") {
		t.Errorf("calls %v", f.calls)
	}
	// already logged in to this very network, stopped: its login is kept
	f = &fakeTS{active: true, status: `{"BackendState":"Stopped"}`, prefs: prefsFor("https://relay.example"), upErr: boom}
	if err := f.net().Up(context.Background(), "https://relay.example", testJoinKey, "box"); err == nil {
		t.Fatal("no error")
	}
	if !f.called("tailscale up") || f.called("tailscale logout") || f.called("systemctl disable") {
		t.Errorf("calls %v", f.calls)
	}
}

// A box without systemctl says so, not that tailscale is missing.
func TestUpWithoutSystemctl(t *testing.T) {
	f := &fakeTS{status: `{"BackendState":"NeedsLogin"}`}
	n := f.net().(*tailscale)
	n.systemctl = func(context.Context, ...string) error {
		return fmt.Errorf("systemctl is-active: %w", exec.ErrNotFound)
	}
	err := n.Up(context.Background(), "https://relay.example", testJoinKey, "box")
	if !errors.Is(err, ErrNoSystemctl) || errors.Is(err, exec.ErrNotFound) || f.called("tailscale up") {
		t.Fatalf("err %v, calls %v", err, f.calls)
	}
}

func TestUpRefusesAKeyTailscaleWouldRead(t *testing.T) {
	for _, key := range []string{"file:/etc/shadow", "--x", "a key with spaces", strings.Repeat("k", 300)} {
		f := &fakeTS{status: `{"BackendState":"NeedsLogin"}`}
		err := f.net().Up(context.Background(), "https://relay.example", key, "box")
		if err == nil || strings.Contains(err.Error(), key) {
			t.Errorf("%q: err = %v", key, err)
		}
		if len(f.calls) != 0 {
			t.Errorf("%q: calls %v", key, f.calls)
		}
	}
}

func TestUpIdempotentSameNetwork(t *testing.T) {
	f := &fakeTS{status: runningJSON, prefs: prefsFor("https://Relay.Example/")}
	if err := f.net().Up(context.Background(), "https://relay.example", testJoinKey, "box"); err != nil {
		t.Fatal(err)
	}
	if f.called("tailscale up") || f.called("systemctl") {
		t.Errorf("already joined: nothing to run, calls: %v", f.calls)
	}
}

// A stopped tailscaled cannot log out: Down starts it first, or the login to
// our relay stays on disk for the next Up to find (I-1).
func TestDownLogsOut(t *testing.T) {
	f := &fakeTS{}
	if err := f.net().Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "systemctl is-active --quiet tailscaled,systemctl is-enabled --quiet tailscaled,systemctl enable --now tailscaled,tailscale logout,systemctl disable --now tailscaled"
	if got := strings.Join(f.calls, ","); got != want {
		t.Errorf("stopped: calls %v", f.calls)
	}
	f = &fakeTS{active: true}
	if err := f.net().Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls, ","); got != "systemctl is-active --quiet tailscaled,tailscale logout,systemctl disable --now tailscaled" {
		t.Errorf("running: calls %v", f.calls)
	}
}

func TestDownErrors(t *testing.T) {
	boom := errors.New("boom")
	// One step failing must not stop the caller's local cleanup.
	f := &fakeTS{logoutErr: boom}
	if err := f.net().Down(context.Background()); err != nil || f.calls[len(f.calls)-1] != "systemctl disable --now tailscaled" {
		t.Errorf("logout alone failing: %v, calls %v", err, f.calls)
	}
	if err := (&fakeTS{sysErr: boom}).net().Down(context.Background()); err != nil {
		t.Errorf("systemctl alone failing: %v", err)
	}
	if err := (&fakeTS{logoutErr: boom, sysErr: boom}).net().Down(context.Background()); err == nil {
		t.Error("both failing must be reported")
	}
}

func TestPrivateNetNil(t *testing.T) {
	f := New(t.TempDir())
	if _, err := f.PrivateNet(); !errors.Is(err, ErrNoPrivateNet) {
		t.Errorf("err = %v", err)
	}
}

// A caller's longer deadline (a route's detached two minutes) does not lift
// the cap of one tailscale call.
func TestNetTimeoutCapsALongerDeadline(t *testing.T) {
	var left time.Duration
	n := NewTailscale(func(ctx context.Context, _ ...string) ([]byte, error) {
		d, _ := ctx.Deadline()
		left = time.Until(d)
		return []byte(`{"BackendState":"NeedsLogin"}`), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := n.State(ctx); err != nil {
		t.Fatal(err)
	}
	if left > netTimeout {
		t.Fatalf("a call may run %v", left)
	}
}
