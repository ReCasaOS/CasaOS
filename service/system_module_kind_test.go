package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
)

// The kind of a job: the page cannot tell an install from a remove by the steps (they have `manifest` and `done` in common), so the core says which it started, in the first line of the log,
// and the job carries it for as long as the log is there.

// startJob starts the job of a verb for the module virt, on a box that is ready for it, and gives the answer of the start.
func startJob(t *testing.T, b *moduleBox, verb string) SystemModuleStatus {
	t.Helper()
	var status SystemModuleStatus
	var err error
	switch verb {
	case modulecmd.KindInstall:
		status, err = b.system.StartSystemModuleInstall(context.Background(), "virt", b.planned("virt").PlanID)
	default:
		b.install("virt")
		status, err = b.system.StartSystemModuleRemove(context.Background(), "virt")
	}
	if err != nil {
		t.Fatalf("%s: %v", verb, err)
	}
	return status
}

func TestAnInstallAndARemoveSayWhatTheyAreInTheirLogAndInTheirJob(t *testing.T) {
	for _, kind := range []string{modulecmd.KindInstall, modulecmd.KindRemove} {
		t.Run(kind, func(t *testing.T) {
			b := newModuleBox(t)
			answer := startJob(t, b, kind)
			nonce := b.nonceOfLog("virt")
			if want := modulecmd.QueuedLine("virt", nonce, kind, b.now()); b.read("/var/log/casaos/module-virt.log") != want {
				t.Errorf("the log is %q, want %q", b.read("/var/log/casaos/module-virt.log"), want)
			}
			if answer.Job.Kind != kind || answer.Job.State != modulecmd.JobRunning {
				t.Errorf("the answer of the start: %+v", answer.Job)
			}
			status, err := b.system.GetSystemModuleStatus("virt")
			if err != nil || status.Job.Kind != kind || status.Job.State != modulecmd.JobRunning {
				t.Errorf("the status: %+v, %v", status.Job, err)
			}
			// the unit that was started runs the verb that the log says
			started := b.startedUnits()
			if len(started) != 1 || verbOfUnit(started[0]) != kind {
				t.Errorf("the unit: %q", started)
			}
		})
	}
}

// verbOfUnit is the verb that a unit started by systemd-run runs: the word after `module` in its command.
func verbOfUnit(argv []string) string {
	for i, a := range argv {
		if a == "module" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// The job keeps its kind at every stage, and through a core that was restarted (a service that has nothing of the one before but the box): while it runs, when it is over whatever the
// way, and years after.
func TestTheKindOfAJobSurvivesARestartOfTheCoreAtEveryStage(t *testing.T) {
	n := func(b *moduleBox) string { return b.nonceOfLog("virt") }
	stages := map[string]struct {
		unit  func(b *moduleBox)
		later time.Duration
		state string
	}{
		"queued": {func(b *moduleBox) {}, 0, modulecmd.JobRunning},
		"running": {func(b *moduleBox) {
			b.setUnit("casaos-module-virt.service", "active")
			b.unitSays("virt", b.step(n(b), "virt", "manifest", "begin", 0), "| writing")
		}, time.Second, modulecmd.JobRunning},
		"succeeded": {func(b *moduleBox) {
			b.unitSays("virt", b.step(n(b), "virt", "done", "done", 0), b.result(n(b), "virt", "ok", "done", "-", "-", 0))
		}, time.Second, modulecmd.JobSucceeded},
		"failed at a step the other kind has too": {func(b *moduleBox) {
			b.unitSays("virt", b.step(n(b), "virt", "manifest", "failed", 0), "kept_packages: none", b.result(n(b), "virt", "failed", "manifest", "manifest_invalid", "rolled_back", 0))
		}, time.Second, modulecmd.JobFailed},
		"refused": {func(b *moduleBox) {
			b.unitSays("virt", "refused: busy", b.result(n(b), "virt", "refused", "-", "busy", "-", 0))
		}, time.Second, modulecmd.JobRefused},
		"interrupted": {func(b *moduleBox) {
			b.unitSays("virt", b.step(n(b), "virt", "manifest", "begin", 0))
		}, 2 * time.Minute, modulecmd.JobInterrupted},
		"the unit never came": {func(b *moduleBox) {}, 2 * time.Minute, modulecmd.JobInterrupted},
		"succeeded, years ago": {func(b *moduleBox) {
			b.unitSays("virt", b.step(n(b), "virt", "done", "done", 0), b.result(n(b), "virt", "ok", "done", "-", "-", 0))
		}, 900 * 24 * time.Hour, modulecmd.JobSucceeded},
		"failed, years ago": {func(b *moduleBox) {
			b.unitSays("virt", b.result(n(b), "virt", "failed", "-", "internal_error", "-", 0))
		}, 900 * 24 * time.Hour, modulecmd.JobFailed},
	}
	for _, kind := range []string{modulecmd.KindInstall, modulecmd.KindRemove} {
		for name, stage := range stages {
			t.Run(kind+", "+name, func(t *testing.T) {
				b := newModuleBox(t)
				startJob(t, b, kind)
				stage.unit(b)
				b.advance(stage.later)
				for _, core := range []*systemService{b.system, b.restarted()} {
					status, err := core.GetSystemModuleStatus("virt")
					if err != nil || status.Job.Kind != kind || status.Job.State != stage.state {
						t.Errorf("%+v, %v", status.Job, err)
					}
				}
			})
		}
	}
}

// A job whose unit systemd would not start is a whole run in the log, and says what it was.
func TestAJobThatSystemdWouldNotStartHasItsKind(t *testing.T) {
	for _, kind := range []string{modulecmd.KindInstall, modulecmd.KindRemove} {
		t.Run(kind, func(t *testing.T) {
			b := newModuleBox(t)
			b.startErr = errors.New("exit status 1")
			b.startOut = "Failed to connect to bus\n"
			var err error
			if kind == modulecmd.KindInstall {
				_, err = b.system.StartSystemModuleInstall(context.Background(), "virt", b.planned("virt").PlanID)
			} else {
				b.install("virt")
				_, err = b.system.StartSystemModuleRemove(context.Background(), "virt")
			}
			if refusal := moduleRefusalOf(t, err); refusal.Code != ModuleStartFailed {
				t.Fatalf("%+v", refusal)
			}
			if log := b.read("/var/log/casaos/module-virt.log"); !strings.HasPrefix(log, "CASAOS_MODULE_QUEUED "+b.nonceOfLog("virt")+" virt "+kind+" ") {
				t.Errorf("the log begins with %q", log)
			}
			for _, core := range []*systemService{b.system, b.restarted()} {
				status, err := core.GetSystemModuleStatus("virt")
				if err != nil || status.Job.State != modulecmd.JobFailed || status.Job.Code != "start_failed" || status.Job.Kind != kind {
					t.Errorf("%+v, %v", status.Job, err)
				}
			}
		})
	}
}

// When systemd-run reports an error and the unit may be there all the same, the job the click is told is the one of the verb, whatever the log says yet.
func TestAnErrorOfSystemdRunStillTellsWhatTheJobIs(t *testing.T) {
	// (what systemd says is changed by the call, once the box has been looked at: the call is what queues the unit, or fails to reach the bus)
	for name, afterTheCall := range map[string]func(b *moduleBox){
		"the unit is there": func(b *moduleBox) { b.setUnit("casaos-module-virt.service", "activating") },
		"systemd is silent": func(b *moduleBox) {
			b.mu.Lock()
			b.silent = true
			b.mu.Unlock()
		},
		// (the log is not there to be read: the job is the one the core queued)
		"the log is gone": func(b *moduleBox) {
			b.setUnit("casaos-module-virt.service", "inactive")
			if err := os.Remove(b.logPath("virt")); err != nil {
				b.t.Error(err)
			}
		},
	} {
		for _, kind := range []string{modulecmd.KindInstall, modulecmd.KindRemove} {
			t.Run(name+", "+kind, func(t *testing.T) {
				b := newModuleBox(t)
				plan := b.planned("virt")
				if kind == modulecmd.KindRemove {
					b.install("virt")
				}
				b.startErr = errors.New("signal: killed")
				b.onStart = func([]string) { afterTheCall(b) }
				var status SystemModuleStatus
				var err error
				if kind == modulecmd.KindInstall {
					status, err = b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
				} else {
					status, err = b.system.StartSystemModuleRemove(context.Background(), "virt")
				}
				if err != nil || status.Job.Kind != kind || status.Job.State != modulecmd.JobRunning {
					t.Errorf("%+v, %v", status.Job, err)
				}
			})
		}
	}
}

// A log that does not begin with the core's line says no kind: a job started by hand with a nonce, a run by hand, a line that is not as the core writes it, a line that was cut.
func TestALogThatDoesNotSayWhatItIsHasNoKindWhateverItHolds(t *testing.T) {
	const n = unitSightNonce
	for name, content := range map[string]func(b *moduleBox) string{
		"the lines of a unit only": func(b *moduleBox) string { return b.step(n, "virt", "manifest", "begin", 0) + "\n" },
		"a run by hand":            func(b *moduleBox) string { return "CASAOS_MODULE_STEP - virt manifest begin " + b.stamp(0) + "\n" },
		"the old grammar": func(b *moduleBox) string {
			return "CASAOS_MODULE_QUEUED " + n + " virt " + b.stamp(0) + "\n" + b.step(n, "virt", "manifest", "begin", 0) + "\n"
		},
		"a kind that is not one": func(b *moduleBox) string {
			return b.queuedLineOf("virt", n, "upgrade", 0) + "\n" + b.step(n, "virt", "manifest", "begin", 0) + "\n"
		},
		"a kind in capitals": func(b *moduleBox) string {
			return b.queuedLineOf("virt", n, "REMOVE", 0) + "\n" + b.step(n, "virt", "manifest", "begin", 0) + "\n"
		},
		"a queued line, then a field": func(b *moduleBox) string {
			return b.queuedLineOf("virt", n, "remove", 0) + " x\n" + b.step(n, "virt", "manifest", "begin", 0) + "\n"
		},
		"a queued line, cut":            func(b *moduleBox) string { return b.queuedLineOf("virt", n, "remove", 0)[:60] },
		"a queued line without its end": func(b *moduleBox) string { return b.queuedLineOf("virt", n, "remove", 0) },
		"a queued line with a NUL": func(b *moduleBox) string {
			return strings.Replace(b.queuedLineOf("virt", n, "remove", 0), "remove", "rem\x00ve", 1) + "\n"
		},
		"a queued line of a tool": func(b *moduleBox) string { return "| " + b.queuedLineOf("virt", n, "remove", 0) + "\n" },
		"a queued line of a step later": func(b *moduleBox) string {
			return b.step(n, "virt", "manifest", "begin", 0) + "\n" + b.queuedLineOf("virt", n, "remove", 1) + "\n"
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := newModuleBox(t)
			b.write("/var/log/casaos/module-virt.log", content(b))
			b.touchLog("virt", 0)
			status, err := b.restarted().GetSystemModuleStatus("virt")
			if err != nil || status.Job.Kind != "" {
				t.Errorf("%+v, %v", status.Job, err)
			}
		})
	}
	// and a module that never had a job has none
	b := newModuleBox(t)
	if status, err := b.system.GetSystemModuleStatus("virt"); err != nil || status.Job.Kind != "" || status.Job.State != modulecmd.JobIdle {
		t.Errorf("%+v, %v", status.Job, err)
	}
}
