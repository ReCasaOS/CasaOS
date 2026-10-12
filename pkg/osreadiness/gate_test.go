package osreadiness

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"sync"
	"testing"
	"time"
)

// What the report says of a box that is not one the upgrade is for, or has nothing to upgrade to:
// the reason only, and no other check is run.
func TestABoxTheUpgradeIsNotForIsReportedWithItsReasonsOnly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeProbe)
		codes []string
	}{
		{"an unknown system", func(f *fakeProbe) {
			f.set("/etc/os-release", "ID=fedora\nVERSION_ID=\"41\"\n")
		}, []string{"unsupported_os=refuse"}},
		{"a derivative that says what it is built on", func(f *fakeProbe) {
			f.set("/etc/os-release", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"21.3\"\nVERSION_CODENAME=virginia\n")
		}, []string{"unsupported_os=refuse"}},
		{"Debian testing, which has no VERSION_ID", func(f *fakeProbe) {
			f.set("/etc/os-release", "ID=debian\nPRETTY_NAME=\"Debian GNU/Linux forky/sid\"\n")
		}, []string{"unsupported_os=refuse"}},
		{"a release that has no upgrade from here", func(f *fakeProbe) {
			f.set("/etc/os-release", osRelease("ubuntu", "20.04", "focal", "Ubuntu 20.04.6 LTS"))
		}, []string{"unsupported_os=refuse"}},
		{"Raspberry Pi OS, which says Debian", func(f *fakeProbe) {
			f.set("/etc/rpi-issue", "Raspberry Pi reference 2024-03-15")
		}, []string{"vendor_os=refuse"}},
		{"a Proxmox Backup Server, which says Debian", func(f *fakeProbe) { f.set("/usr/sbin/proxmox-backup-manager", "") }, []string{"vendor_os=refuse"}},
		{"a Proxmox Mail Gateway, which says Debian", func(f *fakeProbe) { f.set("/usr/bin/pmgversion", "") }, []string{"vendor_os=refuse"}},
		{"a TrueNAS SCALE, which says Debian", func(f *fakeProbe) { f.set("/usr/bin/middlewared", "") }, []string{"vendor_os=refuse"}},
		{"an arm64 machine", func(f *fakeProbe) { f.arch = "arm64" }, []string{"unproven_arch=refuse"}},
		{"a Raspberry Pi 64-bit, which is a variant and arm64", func(f *fakeProbe) {
			f.set("/etc/rpi-issue", "x")
			f.arch = "arm64"
		}, []string{"unproven_arch=refuse", "vendor_os=refuse"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBox(t)
			tc.setup(f)
			report := Run(context.Background(), f, Options{})
			if report.State != StateUnsupported || report.Hop != nil {
				t.Fatalf("state, hop = %s, %v; findings %v", report.State, report.Hop, codesOf(report))
			}
			got := codesOf(report)
			slices.Sort(got)
			if !slices.Equal(got, tc.codes) {
				t.Fatalf("findings = %v, want %v", got, tc.codes)
			}
			if report.Counts.Refuse != len(tc.codes) {
				t.Fatalf("counts = %+v", report.Counts)
			}
			// nothing past the identity was asked of the machine
			for _, argv := range f.attemptedCommands() {
				if !slices.Equal(argv, argvArch) {
					t.Fatalf("a check that is not of the identity ran %v", argv)
				}
			}
		})
	}
}

// A gate that merely cannot answer (a read that fails, a tool that takes too long) is not a box the
// upgrade is not for: the report is blocked by that refusal, and says what the other checks found.
// Only a gate that says no, and knows, makes the state "unsupported".
func TestAGateThatCannotAnswerDoesNotMakeTheBoxUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*fakeProbe)
		gate   string
		reason string
	}{
		{"dpkg is too slow to say the architecture", func(f *fakeProbe) { f.hang(argvArch) }, "unproven_arch", ReasonTimeout},
		{"os-release cannot be read", func(f *fakeProbe) { f.fail("/etc/os-release", errors.New("input/output error")) }, "unsupported_os", ReasonFailed},
		{"os-release is not allowed to be read", func(f *fakeProbe) {
			f.fail("/etc/os-release", &fs.PathError{Op: "open", Path: "/etc/os-release", Err: fs.ErrPermission})
		}, "unsupported_os", ReasonUnreadable},
		{"the clock is a day before the table", func(f *fakeProbe) { f.now = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) }, "unsupported_os", ReasonClock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBox(t)
			tc.setup(f)
			report := Run(context.Background(), f, Options{Checks: quick(150 * time.Millisecond)})
			if report.State != StateBlocked {
				t.Fatalf("state = %s: %v", report.State, codesOf(report))
			}
			gate := findingOf(t, report, tc.gate)
			if gate.Severity != Refuse || gate.Data["unknown"] != true || gate.Data["reason"] != tc.reason {
				t.Fatalf("%s = %+v", tc.gate, gate)
			}
			// the others were run, and said what they found: a box that is well otherwise says so
			for _, code := range []string{"held", "broken_dpkg", "disk_root", "sources_cdrom", "no_systemd", "not_root"} {
				if got := findingOf(t, report, code); got.Data["unknown"] == true {
					t.Errorf("%s = %+v", code, got)
				}
			}
			if len(report.Checks) < len(Checks())/2 {
				t.Errorf("only %d findings: %v", len(report.Checks), codesOf(report))
			}
		})
	}
}

// A gate that says no and knows still makes the box unsupported, with the gates that could not answer
// listed beside it: they are refusals too.
func TestAGateThatSaysNoMakesTheBoxUnsupportedWhateverTheOthersSay(t *testing.T) {
	f := newBox(t)
	f.set("/etc/rpi-issue", "x")
	f.hang(argvArch)
	report := Run(context.Background(), f, Options{Checks: quick(150 * time.Millisecond)})
	got := codesOf(report)
	slices.Sort(got)
	if report.State != StateUnsupported || !slices.Equal(got, []string{"unproven_arch=refuse", "vendor_os=refuse"}) {
		t.Fatalf("state %s, findings %v", report.State, got)
	}
	if findingOf(t, report, "vendor_os").Data["unknown"] != false || findingOf(t, report, "unproven_arch").Data["unknown"] != true {
		t.Fatalf("findings = %+v", report.Checks)
	}
}

func TestTheLatestReleaseIsLatestWhateverAGateSays(t *testing.T) {
	f := newBox(t)
	f.set("/etc/os-release", osRelease("debian", "13", "trixie", "Debian GNU/Linux 13 (trixie)"))
	f.hang(argvArch)
	report := Run(context.Background(), f, Options{Checks: quick(150 * time.Millisecond)})
	if report.State != StateLatest || len(report.Checks) != 0 {
		t.Fatalf("state %s, findings %v", report.State, codesOf(report))
	}
}

// The identity of the box is read under the rules of every check: it has its time and the time of the
// report, and what it does not answer, or fails at, is a refusal that does not know. One read that
// hangs for ever (a disk that is gone under /boot, an automount that does not come) must not make the
// report wait, nor the next ones with it.
type stuckIdentityProbe struct {
	*fakeProbe
	// stuck are the paths whose reading and looking do not stop for the end of their time
	stuck   map[string]bool
	release chan struct{}
}

func (p *stuckIdentityProbe) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if p.stuck[path] {
		<-p.release
	}
	return p.fakeProbe.ReadFile(ctx, path)
}

func (p *stuckIdentityProbe) Stat(ctx context.Context, path string) (FileInfo, error) {
	if p.stuck[path] {
		<-p.release
	}
	return p.fakeProbe.Stat(ctx, path)
}

func TestAnIdentityThatDoesNotAnswerIsARefusalThatSaysSoAndTheReportEndsInTime(t *testing.T) {
	for name, path := range map[string]string{"os-release": "/etc/os-release", "a marker of a variant": "/boot/dietpi.txt"} {
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			probe := &stuckIdentityProbe{fakeProbe: newBox(t), stuck: map[string]bool{path: true}, release: release}
			started := time.Now()
			report := Run(context.Background(), probe, Options{Checks: quick(150 * time.Millisecond), IdentityTimeout: 150 * time.Millisecond, Budget: 10 * time.Second})
			if elapsed := time.Since(started); elapsed > 3*time.Second {
				t.Fatalf("the report took %v: the identity hung it", elapsed)
			}
			for _, code := range []string{"unsupported_os", "vendor_os"} {
				if got := findingOf(t, report, code); got.Severity != Refuse || got.Data["unknown"] != true || got.Data["reason"] != ReasonTimeout {
					t.Errorf("%s = %+v", code, got)
				}
			}
			if report.State != StateBlocked || report.Hop != nil {
				t.Errorf("state %s, hop %+v", report.State, report.Hop)
			}
		})
	}
}

// The time the identity has is the report's too: when that is shorter, it ends with it, and the gates
// that have no time left to look say that the report ran out of it.
func TestTheIdentityHasNoMoreTimeThanTheReport(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	probe := &stuckIdentityProbe{fakeProbe: newBox(t), stuck: map[string]bool{"/etc/os-release": true}, release: release}
	started := time.Now()
	report := Run(context.Background(), probe, Options{Budget: 200 * time.Millisecond})
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the report took %v for a budget of 200ms", elapsed)
	}
	if got := findingOf(t, report, "unsupported_os"); got.Severity != Refuse || got.Data["unknown"] != true || got.Data["reason"] != ReasonNotFinished {
		t.Fatalf("unsupported_os = %+v", got)
	}
}

// A report that is asked twice, as the page does, makes two: a first one that hangs in its identity
// does not keep the second one waiting.
func TestAReportAfterOneThatHungInItsIdentityIsMadeInTime(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	hung := &stuckIdentityProbe{fakeProbe: newBox(t), stuck: map[string]bool{"/etc/os-release": true}, release: release}
	Run(context.Background(), hung, Options{Checks: quick(100 * time.Millisecond), IdentityTimeout: 100 * time.Millisecond})
	started := time.Now()
	report := Run(context.Background(), newBox(t), Options{})
	if time.Since(started) > 3*time.Second || report.State != StateReady {
		t.Fatalf("state %s after %v", report.State, time.Since(started))
	}
}

// deadlineProbe says the time os-release is given to be read in.
type deadlineProbe struct {
	*fakeProbe
	mu       sync.Mutex
	deadline time.Time
}

func (p *deadlineProbe) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if path == "/etc/os-release" {
		p.mu.Lock()
		p.deadline, _ = ctx.Deadline()
		p.mu.Unlock()
	}
	return p.fakeProbe.ReadFile(ctx, path)
}

func TestTheIdentityHasTheTimeOfACheckAndNoMoreThanTheReport(t *testing.T) {
	for _, tc := range []struct {
		name         string
		opts         Options
		atLeast, max time.Duration
	}{
		{"nobody says", Options{}, 4 * time.Second, 6 * time.Second},
		{"the time is said", Options{IdentityTimeout: 2 * time.Second}, time.Second, 3 * time.Second},
		{"the report has less", Options{Budget: time.Second}, 500 * time.Millisecond, 1100 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &deadlineProbe{fakeProbe: newBox(t)}
			started := time.Now()
			tc.opts.Checks = []Check{answering("c", pass())}
			Run(context.Background(), probe, tc.opts)
			probe.mu.Lock()
			defer probe.mu.Unlock()
			if got := probe.deadline.Sub(started); got < tc.atLeast || got > tc.max {
				t.Fatalf("os-release is given %v, want between %v and %v", got, tc.atLeast, tc.max)
			}
		})
	}
}

// panickingIdentityProbe breaks the way a bug in a probe does.
type panickingIdentityProbe struct{ *fakeProbe }

func (p panickingIdentityProbe) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if path == "/etc/os-release" {
		panic("boom")
	}
	return p.fakeProbe.ReadFile(ctx, path)
}

func TestAnIdentityThatPanicsIsARefusalThatSaysSo(t *testing.T) {
	report := Run(context.Background(), panickingIdentityProbe{newBox(t)}, Options{Checks: quick(150 * time.Millisecond)})
	got := findingOf(t, report, "unsupported_os")
	if got.Severity != Refuse || got.Data["unknown"] != true || got.Data["reason"] != ReasonPanic || report.State != StateBlocked {
		t.Fatalf("state %s, unsupported_os = %+v", report.State, got)
	}
}

func TestTheLatestReleaseHasNothingToUpgradeTo(t *testing.T) {
	f := newBox(t)
	f.set("/etc/os-release", osRelease("debian", "13", "trixie", "Debian GNU/Linux 13 (trixie)"))
	report := Run(context.Background(), f, Options{})
	if report.State != StateLatest || len(report.Checks) != 0 || report.Hop != nil || report.Counts != (Counts{}) {
		t.Fatalf("report = %+v", report)
	}
	if report.Checks == nil {
		t.Fatal("the checks of a report are a list, never null")
	}
	for _, argv := range f.attemptedCommands() {
		if !slices.Equal(argv, argvArch) {
			t.Fatalf("a check that is not of the identity ran %v", argv)
		}
	}
}

func TestAMarkerThatCannotBeLookedAtIsNotAnAnswer(t *testing.T) {
	f := newBox(t)
	f.fail("/etc/rpi-issue", &fs.PathError{Op: "stat", Path: "/etc/rpi-issue", Err: fs.ErrPermission})
	report := Run(context.Background(), f, Options{})
	finding := findingOf(t, report, "vendor_os")
	if report.State != StateBlocked || finding.Data["unknown"] != true || finding.Data["reason"] != ReasonUnreadable {
		t.Fatalf("state %s, vendor_os %+v", report.State, finding)
	}
}
