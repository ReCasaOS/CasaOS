package modulecmd

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// dpkg --audit as dpkg 1.21 says it for packages that a kill left half done
const auditText = `The following packages have been unpacked but not yet configured.
They must be configured using dpkg --configure or the configuration
menu option in dselect for them to work:
 qemu-utils           QEMU utilities

The following packages are only half configured, probably due to problems
configuring them the first time.  The configuration should be retried using
dpkg --configure <package> or the configure menu option in dselect:
 libvirt-clients      Programs for the libvirt library
`

// the commands that were run, as lines
func (r *fakeRunner) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// resultCode is the "outcome step code" of the RESULT line of the run
func (f *fixture) resultCode() string {
	f.t.Helper()
	for _, l := range f.lines() {
		if m := resultRe.FindStringSubmatch(l); m != nil {
			return m[2] + " " + m[3] + " " + m[4]
		}
	}
	f.t.Fatalf("no result line:\n%s", f.out)
	return ""
}

func TestParseAuditReadsThePackagesAndNothingElse(t *testing.T) {
	if got := parseAudit(auditText); !slices.Equal(got, []string{"qemu-utils", "libvirt-clients"}) {
		t.Errorf("%q", got)
	}
	if got := parseAudit(""); len(got) != 0 {
		t.Errorf("a clean dpkg: %q", got)
	}
	// text that is not a list of packages (a warning, a translation) lists none
	if got := parseAudit("dpkg: warning: something\nLes paquets suivants sont en desordre\n"); len(got) != 0 {
		t.Errorf("%q", got)
	}
}

func TestParseAuditIsBoundedAndNeverReturnsAnythingButNames(t *testing.T) {
	var b strings.Builder
	for range 100000 {
		b.WriteString(" evil\x1b[31m\x00name  description\n")
	}
	b.WriteString(" ok-name  fine\n \x07\x07\n \u00e9t\u00e9 translated\n " + strings.Repeat("x", 100000) + "\n")
	got := parseAudit(b.String())
	if len(got) != maxAuditNames {
		t.Fatalf("%d names", len(got))
	}
	for _, n := range got {
		if n != "?" {
			t.Fatalf("a name that is not a package's got through: %q", n)
		}
	}
	// a short hostile list keeps the good names and marks the others
	if got := parseAudit(" ok-name x\n \u00e9t\u00e9 y\n ok2 z\n"); !slices.Equal(got, []string{"ok-name", "?", "ok2"}) {
		t.Errorf("%q", got)
	}
}

// the install asks the audit three times when it repairs: the plan it makes after it has the lock, the audit of the step, and the audit that checks the repair
func TestAnInstallOverAnInterruptedDpkgFinishesItBeforeApt(t *testing.T) {
	f := newFixture(t)
	f.runner.audits = []string{auditText, auditText, ""}
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	var got []string
	for _, l := range f.runner.lines() {
		if strings.HasPrefix(l, "dpkg") || strings.HasPrefix(l, "apt-get install") || strings.HasPrefix(l, "apt-get --download-only") {
			got = append(got, strings.Join(strings.Fields(l)[:2], " "))
		}
	}
	if want := []string{"dpkg --audit", "dpkg --audit", "dpkg --configure", "dpkg --audit", "apt-get --download-only", "apt-get install"}; !slices.Equal(got, want) {
		t.Errorf("commands: %q", got)
	}
	if !slices.Contains(f.lines(), "dpkg_repaired: qemu-utils libvirt-clients") {
		t.Errorf("the repair is not in the log:\n%s", f.out)
	}
	c := f.runner.last["dpkg --configure -a --force-confold"]
	env := strings.Join(c.Env, " ")
	for _, want := range []string{"DEBIAN_FRONTEND=noninteractive", "LC_ALL=C"} {
		if !strings.Contains(env, want) {
			t.Errorf("dpkg --configure -a ran without %s: %s", want, env)
		}
	}
	if c.Stdout == nil {
		t.Error("the repair: its output is not in the log")
	}
	if a := f.runner.last["dpkg --audit"]; a.Timeout <= 0 || a.Stdout != nil {
		t.Errorf("the audit: %+v", a)
	}
}

func TestAnInstallOverACleanDpkgAsksAndRepairsNothing(t *testing.T) {
	f := newFixture(t)
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if n := f.runner.called("dpkg", "--configure"); n != 0 {
		t.Errorf("dpkg --configure ran %d times", n)
	}
	if strings.Contains(f.out.String(), "dpkg_repaired") {
		t.Error("a repair that did not happen")
	}
}

// a dpkg that the repair does not mend stops the install before apt, with a code of its own and the reason in the log
func TestAnInstallFailsWithDpkgBrokenWhenTheRepairDoesNotMendDpkg(t *testing.T) {
	cases := map[string]func(f *fixture){
		"the configure fails": func(f *fixture) {
			f.runner.audits = []string{"", auditText, auditText}
			f.runner.fail["dpkg --configure"] = 1
		},
		"the configure ends well and the audit still lists": func(f *fixture) { f.runner.audits = []string{"", auditText, auditText} },
		"the configure cannot be run": func(f *fixture) {
			f.runner.audits = []string{"", auditText}
			f.runner.errs["dpkg --configure"] = errors.New("exec format error")
		},
		"the audit that checks the repair gives no answer": func(f *fixture) {
			f.runner.audits = []string{"", auditText}
			f.env.StepHook = func(_ context.Context, step, phase string) {
				if step == StepPackages && phase == "begin" {
					f.runner.onRun = func(_ context.Context, argv []string) {
						if argv[0] == "dpkg" && argv[1] == "--configure" {
							f.runner.mu.Lock()
							f.runner.errs["dpkg --audit"] = errors.New("boom")
							f.runner.mu.Unlock()
						}
					}
				}
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			setup(f)
			code := f.install("--yes")
			if code != ExitFailed || f.resultCode() != "failed packages dpkg_broken" {
				t.Fatalf("exit %d, result %q:\n%s", code, f.resultCode(), f.out)
			}
			if f.runner.called("apt-get", "install") != 0 {
				t.Error("apt-get ran on a dpkg that is not mended")
			}
			if !strings.Contains(f.out.String(), "kept_packages: unknown") || !strings.Contains(f.out.String(), "dpkg") {
				t.Errorf("the log:\n%s", f.out)
			}
		})
	}
}

// a dpkg that cannot be asked is not the install's reason to stop (the plan refuses the one that errs, before): apt says what it finds
func TestAnInstallGoesOnWhenDpkgCannotBeAsked(t *testing.T) {
	for name, err := range map[string]error{"no dpkg": osreadiness.ErrToolMissing, "a failed audit": errors.New("boom")} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			// the plan is made with a dpkg that answers, the step with one that does not
			f.env.StepHook = func(_ context.Context, step, phase string) {
				if step == StepPackages && phase == "begin" {
					f.runner.mu.Lock()
					f.runner.errs["dpkg --audit"] = err
					f.runner.mu.Unlock()
				}
			}
			if code := f.install("--yes"); code != ExitOK {
				t.Fatalf("exit %d:\n%s", code, f.out)
			}
			if f.runner.called("dpkg", "--configure") != 0 || f.runner.called("apt-get", "install") != 1 {
				t.Errorf("commands: %q", f.runner.lines())
			}
		})
	}
}

func TestAnInstallEndedWhileDpkgIsBeingMendedIsInterruptedAndAptDoesNotRun(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.runner.audits = []string{"", auditText}
	f.runner.onRun = func(_ context.Context, argv []string) {
		if argv[0] == "dpkg" && argv[1] == "--configure" {
			cancel()
		}
	}
	if code := f.main(ctx, "install", testName, "--yes"); code != ExitFailed || f.resultCode() != "failed packages interrupted" {
		t.Fatalf("exit %d, %q:\n%s", code, f.resultCode(), f.out)
	}
	if f.runner.called("apt-get", "install") != 0 {
		t.Error("apt-get ran")
	}
}

// apt-get is asked to finish when the install is ended, and the time it is given fits the time the unit gives the assistant to stop
func TestAptIsGivenTimeToFinishWithinTheStopOfTheUnit(t *testing.T) {
	f := newFixture(t)
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	var finish time.Duration
	for cmd, c := range f.runner.last {
		if strings.HasPrefix(cmd, "apt-get install") {
			finish = c.Finish
		}
	}
	if finish != aptFinish {
		t.Errorf("apt-get install is given %v to finish", finish)
	}
	if aptFinish+killGrace > StopWithin {
		t.Errorf("%v and the grace do not fit in %v, which the unit's TimeoutStopSec is built from", aptFinish, StopWithin)
	}
}

// the plan says an interrupted dpkg and is not refused for it, and one whose audit cannot be had is refused
func TestThePlanSaysAnInterruptedDpkgAndRefusesOneThatCannotBeAsked(t *testing.T) {
	f := newFixture(t)
	f.runner.audits = []string{auditText}
	v := f.json("plan", "virt")
	if v["refused"] != nil || v["plan_id"] == "" {
		t.Fatalf("refused %v id %v", v["refused"], v["plan_id"])
	}
	seen := false
	for _, c := range v["checks"].([]any) {
		if m := c.(map[string]any); m["code"] == "dpkg_state" {
			seen = true
			if m["ok"] != false || m["detail"] != "interrupted: qemu-utils libvirt-clients" {
				t.Errorf("%v", m)
			}
		}
	}
	if !seen {
		t.Error("no check")
	}
	f = newFixture(t)
	f.runner.errs["dpkg --audit"] = errors.New("boom")
	if v := f.json("plan", "virt"); v["refused"] != "dpkg_state" {
		t.Errorf("refused %v", v["refused"])
	}
	f = newFixture(t)
	f.runner.errs["dpkg --audit"] = osreadiness.ErrToolMissing
	if v := f.json("plan", "virt"); v["refused"] != nil {
		t.Errorf("refused %v", v["refused"])
	}
}

// ...and so is one that is ended while the audit that checks the repair runs (the audit's own error is not a dpkg that is broken)
func TestAnInstallEndedWhileTheRepairIsCheckedIsInterrupted(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.runner.audits = []string{"", auditText}
	audits := 0
	f.runner.onRun = func(_ context.Context, argv []string) {
		if argv[0] == "dpkg" && argv[1] == "--audit" {
			if audits++; audits == 3 {
				cancel()
			}
		}
	}
	if code := f.main(ctx, "install", testName, "--yes"); code != ExitFailed || f.resultCode() != "failed packages interrupted" {
		t.Fatalf("exit %d, %q:\n%s", code, f.resultCode(), f.out)
	}
}

// The timer of the assistant never ends apt-get or dpkg while dpkg may be at work: the download, which changes nothing, has its time; the install and the repair of dpkg have none, and
// a stop that is asked for still asks them to finish (Cmd.Finish). A slow mirror and a slow postinst are not a reason to leave packages unpacked or half-configured.
func TestALongDownloadThenALongInstallAreNotEndedByATimer(t *testing.T) {
	f := newFixture(t)
	f.runner.takes["apt-get --download-only install"] = aptTimeout - time.Minute
	f.runner.takes["apt-get install"] = 3 * aptTimeout
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	var apt []string
	for _, l := range f.runner.lines() {
		if strings.HasPrefix(l, "apt-get") && !strings.Contains(l, " -s ") {
			apt = append(apt, l)
		}
	}
	if len(apt) != 2 || !strings.HasPrefix(apt[0], "apt-get --download-only install -y ") || apt[1] != strings.Replace(apt[0], "--download-only ", "", 1) {
		t.Fatalf("apt-get: %q", apt)
	}
	download, install := f.runner.last[apt[0]], f.runner.last[apt[1]]
	if download.Timeout != aptTimeout || install.Timeout != 0 || install.Finish != aptFinish {
		t.Errorf("download: timeout %v; install: timeout %v, finish %v", download.Timeout, install.Timeout, install.Finish)
	}
}

func TestADownloadLongerThanItsTimeEndsTheStepBeforeDpkgRuns(t *testing.T) {
	f := newFixture(t)
	f.runner.takes["apt-get --download-only install"] = aptTimeout + time.Minute
	if code := f.install("--yes"); code != ExitFailed || f.resultCode() != "failed packages packages_failed" {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if f.runner.called("apt-get", "install") != 0 {
		t.Error("apt-get installed after a download that did not end")
	}
}

func TestTheRepairOfDpkgIsNotEndedByATimer(t *testing.T) {
	f := newFixture(t)
	f.runner.audits = []string{auditText, auditText, ""}
	f.runner.takes["dpkg --configure"] = time.Hour
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if c := f.runner.last["dpkg --configure -a --force-confold"]; c.Timeout != 0 || c.Finish != aptFinish {
		t.Errorf("dpkg --configure -a: timeout %v, finish %v", c.Timeout, c.Finish)
	}
}
