package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// The lock of the upgrade is held to read a run again, to decide and to append a marker, and never across a question to systemd, a call of systemd-run, or any wait: a systemd that is
// busy in a re-execution (it answers in ten seconds, or twenty-five) must not stop every button of the page.

// A lockWatch says whether the lock was held when the machine was asked something.
type lockWatch struct {
	mu   sync.Mutex
	held []string
	seen int
}

func (w *lockWatch) check(what string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen++
	if osUpgradeMu.TryLock() {
		osUpgradeMu.Unlock()
		return
	}
	w.held = append(w.held, what)
}

// watchTheLock makes every question to systemd and every call of systemd-run of the box look at the lock.
func (b *upgradeBox) watchTheLock() *lockWatch {
	b.t.Helper()
	w := &lockWatch{}
	command, start := b.updater.command, b.updater.start
	b.updater.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		w.check(name + " " + strings.Join(args, " "))
		return command(ctx, name, args...)
	}
	b.updater.start = func(path, unit string, args ...string) ([]byte, error) {
		w.check("systemd-run " + unit)
		return start(path, unit, args...)
	}
	return w
}

func (w *lockWatch) assertFree(t *testing.T, what string) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen == 0 {
		t.Errorf("%s: the machine was asked nothing: the test sees nothing", what)
	}
	if len(w.held) > 0 {
		t.Errorf("%s: the machine was asked with the lock held: %q", what, w.held)
	}
}

func TestNoButtonAsksSystemdOrCallsSystemdRunWithTheLockHeld(t *testing.T) {
	cases := map[string]func(t *testing.T, box *upgradeBox){
		"a start": func(t *testing.T, box *upgradeBox) {
			st, plan := box.aMachineThatCanStart()
			_ = st
			if _, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan)); err != nil {
				t.Fatal(err)
			}
		},
		"a start of a rehearsal": func(t *testing.T, box *upgradeBox) {
			_, plan := box.aMachineThatCanStart()
			req := box.aGoodRequest(plan)
			req.Rehearsal = true
			if _, err := box.StartSystemOSUpgrade(req); err != nil {
				t.Fatal(err)
			}
		},
		"a resume": func(t *testing.T, box *upgradeBox) {
			box.readyToResume()
			run := box.aHopThatStoppedHalfWay(osrun.ClassNetwork)
			box.own(run, osrun.StateStoppedAtWaypoint)
			if _, err := box.ResumeSystemOSUpgrade(); err != nil {
				t.Fatal(err)
			}
		},
		"a resume that judges again": func(t *testing.T, box *upgradeBox) {
			box.readyToResume()
			run := box.aHopThatWasChecked()
			box.theFactsBefore(run, factsOfAMachine(box))
			after := factsOfAMachine(box)
			after.DNS = false
			takesFacts(t, after)
			box.tick()
			if state := box.state(run); state.Name != osrun.StateSucceededWithFindings {
				t.Fatalf("the run is %+v", state)
			}
			if _, err := box.ResumeSystemOSUpgrade(); err != nil {
				t.Fatal(err)
			}
		},
		"an acknowledgement": func(t *testing.T, box *upgradeBox) {
			run := box.aRefresh()
			box.own(run, osrun.StateRefreshed)
			if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckAcknowledge}); err != nil {
				t.Fatal(err)
			}
		},
		"the discard of a rehearsal": func(t *testing.T, box *upgradeBox) {
			run := box.aRehearsal()
			box.own(run, osrun.StateRehearsed)
			if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckDiscard}); err != nil {
				t.Fatal(err)
			}
		},
		"a takeover": func(t *testing.T, box *upgradeBox) {
			box.readyToResume()
			run := box.aHopThatStoppedHalfWay(osrun.ClassNetwork)
			box.own(run, osrun.StateStoppedAtWaypoint)
			if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckManual}); err != nil {
				t.Fatal(err)
			}
		},
		"the restart": func(t *testing.T, box *upgradeBox) {
			box.greenBarrier()
			run := box.aHopAtItsRestart()
			box.own(run, osrun.StateAwaitingReboot)
			box.tick()
			box.aMachineThatRestarts(run, errors.New("refused"))
			if _, err := box.RebootSystemOSUpgrade(); err == nil {
				t.Fatal("the restart was refused by the machine and not said")
			}
		},
		"a call off": func(t *testing.T, box *upgradeBox) {
			run := box.aHopAtItsDownload()
			box.own(run, osrun.StateRunning)
			box.setUnit("active")
			if _, err := box.AbortSystemOSUpgrade(); err != nil {
				t.Fatal(err)
			}
		},
		"a look at a run that waits": func(t *testing.T, box *upgradeBox) {
			box.greenBarrier()
			run := box.aHopAtItsRestart()
			box.own(run, osrun.StateAwaitingReboot)
			box.tick()
			box.advance(time.Minute)
			box.tick()
		},
		"a look that undoes a run that was lost": func(t *testing.T, box *upgradeBox) {
			run := box.aHopThatWasLost()
			box.own(run, osrun.StateRunning)
			box.tick()
		},
		"a look that finds the flag of a run that is closed": func(t *testing.T, box *upgradeBox) {
			run := box.aRefresh()
			box.own(run, osrun.StateRefreshing)
			box.tick()
		},
		"the switch turned off": func(t *testing.T, box *upgradeBox) {
			box.switchOn()
			run := box.aHopThatStoppedHalfWay(osrun.ClassNetwork)
			box.point(run)
			if err := box.SetSystemOSUpgradeBeta(false); err == nil {
				t.Fatal("the switch was turned off with a run open")
			}
		},
	}
	for name, do := range cases {
		t.Run(name, func(t *testing.T) {
			box := newUpgradeBox(t)
			w := box.watchTheLock()
			do(t, box)
			w.assertFree(t, name)
		})
	}
}

// A handler that panics (the router recovers it and answers) leaves the engine as it was: the lock is let go of, and the buttons that come after are answered.
func TestAPanicInAHandlerDoesNotWedgeTheEngine(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	was := osUpgradeWriteFile
	osUpgradeWriteFile = func(path string, data []byte, mode os.FileMode) error {
		if strings.HasSuffix(path, osUpgradePlanJSON) {
			panic("a file that cannot be written")
		}
		return was(path, data, mode)
	}
	t.Cleanup(func() { osUpgradeWriteFile = was })

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not come through")
			}
		}()
		box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	}()
	osUpgradeWriteFile = was

	done := make(chan error, 3)
	go func() { done <- box.SetSystemOSUpgradeBeta(false) }()
	go func() {
		_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckAcknowledge})
		done <- err
	}()
	go func() {
		done <- box.launchAttempt("0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19", osrun.PhaseRun, osrun.WhoOwner)
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the engine is wedged: a button waits for the lock of a handler that panicked")
		}
	}
	if osUpgradeMu.TryLock() {
		osUpgradeMu.Unlock()
	} else {
		t.Error("the lock is still held")
	}
}

// systemd-run is called with a time of its own: a bus that does not answer holds on to nothing for long.
func TestTheCallOfSystemdRunHasATimeOfItsOwn(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("there is no sleep to stand in for a systemd-run that does not answer")
	}
	was := systemdRunTimeout
	systemdRunTimeout = 150 * time.Millisecond
	t.Cleanup(func() { systemdRunTimeout = was })
	u := newSystemPackageUpdater()
	begin := time.Now()
	_, err = u.start(sleep, systemOSUpgradeUnit, "30")
	if err == nil {
		t.Error("a call that was cut short is not an error")
	}
	if took := time.Since(begin); took > 5*time.Second {
		t.Errorf("the call took %v", took)
	}
	systemdRunTimeout = 5 * time.Second
	if out, err := u.start(sleep, systemOSUpgradeUnit, "0"); err != nil {
		t.Errorf("a call that ends in its time is an error: %v %s", err, out)
	}
}

// factsOfAMachine are the facts of a machine that is well, as the box says it.
func factsOfAMachine(b *upgradeBox) osverify.Facts {
	return osverify.Facts{Taken: true, At: b.now(), Kernel: "6.1.0-18-amd64", System: "running", DpkgQuiet: true, DNS: true}
}

// ---- a systemd-run that reports an error, and a unit that is there all the same -------------------------------------------------------

// aMachineThatStartsALaunch is a run whose first attempt is launched, with systemd-run that says what the test tells it.
func (b *upgradeBox) aRunToLaunch() *osUpgradeRun {
	b.t.Helper()
	return b.aRun(b.anUpgradePlan("ok", nil), osrun.RunHop)
}

func TestAnErrorOfSystemdRunWithAUnitThatIsThereLeavesTheLogAsItIs(t *testing.T) {
	for name, set := range map[string]func(box *upgradeBox){
		"the unit is running":     func(box *upgradeBox) { box.setUnit("active") },
		"the unit is coming up":   func(box *upgradeBox) { box.setUnit("activating") },
		"the unit is loaded":      func(box *upgradeBox) { box.setUnit("inactive"); box.loaded = true },
		"systemd does not answer": func(box *upgradeBox) { box.setUnit("") },
	} {
		t.Run(name, func(t *testing.T) {
			box := newUpgradeBox(t)
			box.startErr = errors.New("Failed to start transient service unit: Connection timed out")
			run := box.aRunToLaunch()
			// systemd says that the unit is not there when it is asked before the launch, and what the test says after the error of systemd-run
			wasStart := box.updater.start
			box.updater.start = func(path, unit string, args ...string) ([]byte, error) {
				out, err := wasStart(path, unit, args...)
				set(box)
				return out, err
			}
			if err := box.launchAttempt(run.ID, osrun.PhaseRun, osrun.WhoOwner); err != nil {
				t.Fatalf("a launch that systemd-run reported an error for, and whose unit is there, is %v", err)
			}
			if got := markersOfKind(box.read(run.ID), osrun.MkStartFailed); len(got) != 0 {
				t.Errorf("START_FAILED was written for a unit that is there: %+v", got)
			}
			if len(box.startedUnits()) != 1 {
				t.Errorf("%d units were started", len(box.startedUnits()))
			}
		})
	}
}

func TestAnErrorOfSystemdRunWithNoUnitIsAStartThatFailed(t *testing.T) {
	box := newUpgradeBox(t)
	box.startErr = errors.New("Failed to connect to bus")
	run := box.aRunToLaunch()
	err := box.launchAttempt(run.ID, osrun.PhaseRun, osrun.WhoOwner)
	if launchKind(t, err) != launchStartFailed {
		t.Fatalf("err = %v", err)
	}
	if got := markersOfKind(box.read(run.ID), osrun.MkStartFailed); len(got) != 1 || got[0].N != 1 {
		t.Errorf("START_FAILED lines: %+v", got)
	}
	if state := box.state(run); state.Name != osrun.StateFailed || state.Reason != osrun.ReasonStart || state.Busy {
		t.Errorf("the run is %+v", state)
	}
}

// A unit that begins after the core was told that systemd-run failed is the run's: it is looked at as running, nothing is closed over it, and nothing is launched.
func TestAUnitThatBeginsAfterTheCoreTookTheLaunchForFailedHoldsTheMachine(t *testing.T) {
	box := newUpgradeBox(t)
	box.readyToResume()
	box.startErr = errors.New("Failed to connect to bus")
	run := box.aRunToLaunch()
	if err := box.launchAttempt(run.ID, osrun.PhaseRun, osrun.WhoOwner); launchKind(t, err) != launchStartFailed {
		t.Fatalf("err = %v", err)
	}
	box.own(run, osrun.StateRunning)
	// the unit starts all the same
	box.setUnit("active")
	run = box.say(box.read(run.ID), osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRun, Boot: boxBoot1, PID: 77})
	run = box.block(run, 1, 1, osrun.BlockVerify, false)

	status := box.GetSystemOSUpgradeStatus()
	if status.State != osrun.StateRunning || !status.Busy {
		t.Fatalf("the run is %q, busy %v", status.State, status.Busy)
	}
	if tick := box.tick(); len(tick.Done) != 0 || !box.flagIsThere() {
		t.Errorf("the look did %+v, the flag is there: %v", tick, box.flagIsThere())
	}
	// the owner cannot close it, whatever the state looks like
	for _, action := range []string{OSUpgradeAckAcknowledge, OSUpgradeAckDiscard, OSUpgradeAckManual} {
		_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: action})
		if code := refusalOf(t, err).Code; code != OSUpgradeNotTerminal && code != OSUpgradeUnitActive {
			t.Errorf("%s: refused with %q", action, code)
		}
	}
	if !box.flagIsThere() || len(markersOfKind(box.read(run.ID), osrun.MkAck)) != 0 {
		t.Error("the run was closed over a unit that runs")
	}
}
