package modulecmd

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// A remove deletes nothing of a module whose unit is not known to be stopped: only "inactive" and "failed" (and so a unit systemd does not know, which says "inactive") are a unit that is not
// running. An answer that is anything else, an error, no answer and a run that was told to stop are not.
func TestARemoveDeletesNothingWhileTheUnitIsNotKnownToBeStopped(t *testing.T) {
	const isActive = "systemctl is-active"
	const disable = "systemctl disable --now"
	says := func(f *fixture, word string) {
		f.runner.fail[disable] = 1
		f.runner.fail[isActive] = 3
		f.runner.said[isActive] = word
	}
	for name, setup := range map[string]func(f *fixture){
		"it is active":                        func(f *fixture) { f.runner.fail[disable] = 1 },
		"it is activating":                    func(f *fixture) { says(f, "activating\n") },
		"it is deactivating":                  func(f *fixture) { says(f, "deactivating\n") },
		"it is reloading":                     func(f *fixture) { says(f, "reloading\n") },
		"systemd says something else":         func(f *fixture) { says(f, "banana\n") },
		"systemd says nothing":                func(f *fixture) { says(f, "") },
		"systemd says two words":              func(f *fixture) { says(f, "inactive\nactive\n") },
		"systemd does not answer":             func(f *fixture) { f.runner.fail[disable] = 1; f.runner.errs[isActive] = errors.New("bus down") },
		"nothing answers":                     func(f *fixture) { f.runner.errs["systemctl"] = errors.New("bus down") },
		"it was disabled and is still active": func(f *fixture) { f.runner.fail[isActive] = 3; f.runner.said[isActive] = "active\n" },
	} {
		t.Run(name, func(t *testing.T) {
			f := installed(t)
			setup(f)
			before := f.treeWithoutBookkeeping()
			if code := f.remove("--yes"); code != ExitFailed {
				t.Fatalf("exit %d:\n%s", code, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			out := f.out.String()
			if !strings.Contains(out, "failed at step stop: "+CodeUnitNotStopped+"\n") || strings.Contains(out, "removed: ") {
				t.Errorf("not said, or said removed:\n%s", out)
			}
			if got := markers(t, f.lines()); !slices.Equal(got, []string{"stop begin", "stop failed"}) {
				t.Errorf("markers: %q", got)
			}
			if !strings.Contains(out, resultMarker+" - virt failed stop "+CodeUnitNotStopped+" ") {
				t.Errorf("no result:\n%s", out)
			}
		})
	}
}

func TestAUnitThatIsNotRunningIsStopped(t *testing.T) {
	for name, state := range map[string]string{"inactive": "inactive\n", "failed": "failed\n"} {
		t.Run(name, func(t *testing.T) {
			f := installed(t)
			f.runner.fail["systemctl disable --now"] = 1 // (it could not be disabled, and is not running)
			f.runner.fail["systemctl is-active"] = 3
			f.runner.said["systemctl is-active"] = state
			if code := f.remove("--yes"); code != ExitOK {
				t.Fatalf("exit %d:\n%s", code, f.out)
			}
			if f.exists("/usr/bin/casaos-virt-management") || f.exists("/etc/casaos/modules/virt.json") {
				t.Error("not removed")
			}
		})
	}
}

// A run that was told to stop before it knew the unit was stopped deletes nothing: the question whether it is running is still asked, on a context of its own, and the answer is "active"
func TestARemoveThatIsToldToStopBeforeTheUnitIsStoppedDeletesNothing(t *testing.T) {
	f := installed(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := f.treeWithoutBookkeeping()
	if code := f.main(ctx, "remove", testName, "--yes"); code != ExitFailed {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if !f.runner.isEnabled(testUnit) || !strings.Contains(f.out.String(), "failed at step stop: "+CodeUnitNotStopped+"\n") {
		t.Errorf("the unit was stopped, or it is not said:\n%s", f.out)
	}
}

// ...and one that is told to stop when the unit is stopped already is a run that stops there, and nothing is deleted either: it was told to
func TestARemoveThatIsToldToStopWhileTheUnitIsStoppedDeletesNothingEither(t *testing.T) {
	f := installed(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.runner.onRun = func(_ context.Context, argv []string) {
		if len(argv) >= 4 && argv[1] == "disable" {
			f.runner.mu.Lock()
			delete(f.runner.enabled, argv[3])
			f.runner.mu.Unlock()
			cancel()
		}
	}
	before := f.treeWithoutBookkeeping()
	if code := f.main(ctx, "remove", testName, "--yes"); code != ExitFailed {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if !strings.Contains(f.out.String(), "failed at step stop: "+CodeInterrupted+"\n") {
		t.Errorf("not interrupted:\n%s", f.out)
	}
	// a run again finishes
	f.runner.onRun = nil
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("again: %d\n%s", code, f.out)
	}
}

// A daemon-reload that fails is a step that failed, and the lists stay, so that a run again does it again
func TestAFailingDaemonReloadIsAFailedStepAndAnotherRunDoesItAgain(t *testing.T) {
	f := installed(t)
	f.runner.fail["systemctl daemon-reload"] = 1
	if code := f.remove("--yes"); code != ExitFailed {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	out := f.out.String()
	got := markers(t, f.lines())
	if len(got) < 2 || !slices.Equal(got[len(got)-2:], []string{"reload begin", "reload failed"}) || strings.Contains(out, "reload done") || strings.Contains(out, "removed: ") {
		t.Errorf("markers %q:\n%s", got, out)
	}
	if !strings.Contains(out, resultMarker+" - virt failed reload "+CodeUnitsFailed+" ") {
		t.Errorf("no result:\n%s", out)
	}
	if !f.exists(filesList) {
		t.Error("the list is gone, and a run again cannot do the reload")
	}
	delete(f.runner.fail, "systemctl daemon-reload")
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("again: %d\n%s", code, f.out)
	}
	if f.exists(filesList) || f.exists(dirsList) {
		t.Error("the lists are left")
	}
}
