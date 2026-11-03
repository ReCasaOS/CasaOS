package fleet

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// fakeTS stands in for the tailscale command and for systemctl, and records
// every call.
type fakeTS struct {
	status, prefs string
	statusErr     error
	upErr         error
	logoutErr     error
	sysErr        error
	calls         []string
}

func (f *fakeTS) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, "tailscale "+strings.Join(args, " "))
	switch args[0] {
	case "status":
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
		{"stopped", `{"BackendState":"Stopped"}`, "", nil, NetState{}, false},
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
	f := &fakeTS{status: `{"BackendState":"Stopped"}`}
	if err := f.net().Up(context.Background(), "https://relay.example/", "tskey-secret-0123", "box1"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) < 3 || f.calls[1] != "systemctl enable --now tailscaled" {
		t.Fatalf("tailscaled must be started before up, calls: %v", f.calls)
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
	f := &fakeTS{status: `{"BackendState":"Stopped"}`, upErr: errors.New("up --authkey=" + key + " failed: bad key " + key)}
	err := f.net().Up(context.Background(), "https://relay.example", key, "box")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("error leaks the key: %v", err)
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
	f := &fakeTS{status: runningJSON, prefs: prefsFor("https://relay.example/")}
	if err := f.net().Up(context.Background(), "https://relay.example", testJoinKey, "box"); err != nil {
		t.Fatal(err)
	}
	if f.called("tailscale up") || f.called("systemctl") {
		t.Errorf("already joined: nothing to run, calls: %v", f.calls)
	}
}

func TestDownLogsOut(t *testing.T) {
	f := &fakeTS{}
	if err := f.net().Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || f.calls[0] != "tailscale logout" || f.calls[1] != "systemctl disable --now tailscaled" {
		t.Errorf("calls: %v", f.calls)
	}
}

func TestDownErrors(t *testing.T) {
	boom := errors.New("boom")
	// One step failing must not stop the caller's local cleanup.
	if err := (&fakeTS{logoutErr: boom}).net().Down(context.Background()); err != nil {
		t.Errorf("logout alone failing: %v", err)
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
