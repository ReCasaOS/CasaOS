package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

func (b *upgradeBox) readyToResume() {
	b.t.Helper()
	b.updater.dpkgLocked = func() bool { return false }
}

// The owner's click repairs a hop that stopped after its point of no return: the repair block, then the block that did not end and the ones after it, as the owner's own.
func TestTheOwnerResumesAHopThatStoppedHalfWay(t *testing.T) {
	box := newUpgradeBox(t)
	box.readyToResume()
	run := box.aHopThatStoppedHalfWay(osrun.ClassNetwork)
	box.own(run, osrun.StateStoppedAtWaypoint)

	status, err := box.ResumeSystemOSUpgrade()
	if err != nil {
		t.Fatal(err)
	}
	attempts := box.attemptsOf(run)
	if len(attempts) != 2 || attempts[1].Phase != osrun.PhaseRecover || attempts[1].Who != osrun.WhoOwner || attempts[1].From != osrun.BlockFull {
		t.Fatalf("the attempts are %+v", attempts)
	}
	if got := readText(t, filepath.Join(run.Dir, "lists", "2.list")); got != "forward\nfull\nprereboot\n" {
		t.Errorf("the list of the repair is %q", got)
	}
	if status.State != osrun.StateRecovering || len(box.startedUnits()) != 1 {
		t.Errorf("the status is %q, %d units were started", status.State, len(box.startedUnits()))
	}
}

// The repair begins where the hop stopped: with the minimal upgrade when it is the first that did not end, and with only the last block when the rest are done.
func TestTheRepairBeginsWhereTheHopStopped(t *testing.T) {
	box := newUpgradeBox(t)
	box.readyToResume()
	run := box.aRun(box.anUpgradePlan("ok", nil), osrun.RunHop)
	run = box.attempt(run, 1, osrun.PhaseRun, osrun.WhoOwner)
	run = box.leave(run, "begun-upgrade", "done-minimal", "done-full")
	run = box.block(run, 1, 8, osrun.BlockPrereboot, false)
	run = box.stop(run, 1, 8, osrun.BlockPrereboot, osrun.ClassBarrierKernel, true)
	run = box.end(run, 1, "stopped")
	box.setUnit("inactive")
	box.advance(time.Minute)
	box.own(run, osrun.StateStoppedAtWaypoint)

	if _, err := box.ResumeSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, filepath.Join(run.Dir, "lists", "2.list")); got != "prereboot\n" {
		t.Errorf("the list of the repair is %q", got)
	}
}

// What the owner's own click does not change: a stop that says the repair itself is not to be trusted, a run that is not stopped, a machine that waits for its restart.
func TestAResumeIsRefusedWhereThereIsNothingToResume(t *testing.T) {
	cases := []struct {
		name  string
		state string
		class string
		set   func(box *upgradeBox) *osUpgradeRun
	}{
		{"the guard stopped it", osrun.StateStoppedAtWaypoint, osrun.ClassGuard, func(box *upgradeBox) *osUpgradeRun { return box.aHopThatStoppedHalfWay(osrun.ClassGuard) }},
		{"the run is not stopped", osrun.StateRunning, "", func(box *upgradeBox) *osUpgradeRun { return box.aHopAtItsDownload() }},
		{"the machine waits for its restart", osrun.StateAwaitingReboot, "", func(box *upgradeBox) *osUpgradeRun { return box.aHopAtItsRestart() }},
		{"it was lost before its point of no return", osrun.StateInterrupted, "", func(box *upgradeBox) *osUpgradeRun { return box.aHopThatWasLost() }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			box := newUpgradeBox(t)
			box.readyToResume()
			run := c.set(box)
			box.point(run)

			_, err := box.ResumeSystemOSUpgrade()
			refusal := refusalOf(t, err)
			if refusal.Code != OSUpgradeNotResumable || refusal.Data["state"] != c.state || (c.class != "" && refusal.Data["class"] != c.class) {
				t.Errorf("refused with %q %v, want %q in %q", refusal.Code, refusal.Data, OSUpgradeNotResumable, c.state)
			}
			if got := box.attemptsOf(run); len(got) > 1 || len(box.startedUnits()) != 0 {
				t.Errorf("%d attempts, %d units", len(got), len(box.startedUnits()))
			}
		})
	}
}

func TestThereIsNothingToResumeWithoutARun(t *testing.T) {
	box := newUpgradeBox(t)
	_, err := box.ResumeSystemOSUpgrade()
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeNoRun {
		t.Errorf("refused with %q", refusal.Code)
	}
}

// Nothing is resumed under a unit that may run, or while a package manager works: the repair is dpkg's own work, and the click is refused while someone else is at it.
func TestAResumeWaitsForTheUnitAndForDpkg(t *testing.T) {
	for _, unit := range []string{"active", ""} {
		box := newUpgradeBox(t)
		box.readyToResume()
		run := box.aHopThatStoppedHalfWay(osrun.ClassNetwork)
		box.own(run, osrun.StateStoppedAtWaypoint)
		box.setUnit(unit)
		_, err := box.ResumeSystemOSUpgrade()
		if refusal := refusalOf(t, err); refusal.Code != OSUpgradeUnitActive {
			t.Errorf("unit %q: refused with %q", unit, refusal.Code)
		}
		if got := box.attemptsOf(run); len(got) != 1 {
			t.Errorf("unit %q: %d attempts", unit, len(got))
		}
	}

	box := newUpgradeBox(t)
	box.updater.dpkgLocked = func() bool { return true }
	run := box.aHopThatStoppedHalfWay(osrun.ClassNetwork)
	box.own(run, osrun.StateStoppedAtWaypoint)
	_, err := box.ResumeSystemOSUpgrade()
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeBusy {
		t.Errorf("refused with %q", refusal.Code)
	}
	if got := box.attemptsOf(run); len(got) != 1 || len(box.startedUnits()) != 0 {
		t.Errorf("%d attempts, %d units while a package manager worked", len(got), len(box.startedUnits()))
	}
}

// A click that comes twice is one repair: the second finds the first on its way.
func TestAResumeThatComesTwiceRepairsOnce(t *testing.T) {
	box := newUpgradeBox(t)
	box.readyToResume()
	run := box.aHopThatStoppedHalfWay(osrun.ClassNetwork)
	box.own(run, osrun.StateStoppedAtWaypoint)

	if _, err := box.ResumeSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	_, err := box.ResumeSystemOSUpgrade()
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeNotResumable {
		t.Errorf("the second click was refused with %q", refusal.Code)
	}
	if got := box.attemptsOf(run); len(got) != 2 || len(box.startedUnits()) != 1 {
		t.Errorf("%d attempts, %d units", len(got), len(box.startedUnits()))
	}
}

// A check that found the machine not as it was is made again when the owner has mended what it found: the click starts the check, and the verdict that was written is taken away
// so that the new one is not taken for the old.
func TestTheOwnerChecksTheMachineAgainAfterAVerdictThatFailed(t *testing.T) {
	box := newUpgradeBox(t)
	box.readyToResume()
	run := box.aHopThatWasChecked()
	if err := os.WriteFile(filepath.Join(box.backupOf(run), "kernel"), []byte("6.1.0-18-amd64\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	takesFacts(t, osverify.Facts{Taken: true, At: box.now(), Kernel: "5.10.0-28-amd64", System: "running", DpkgQuiet: true})
	box.tick()
	if state := box.state(run); state.Name != osrun.StateFailed || state.Reason != osrun.ReasonVerify {
		t.Fatalf("the run is %+v", state)
	}

	if _, err := box.ResumeSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	attempts := box.attemptsOf(run)
	if len(attempts) != 3 || attempts[2].Phase != osrun.PhaseVerify || attempts[2].Who != osrun.WhoOwner {
		t.Fatalf("the attempts are %+v", attempts)
	}
	if got := readText(t, filepath.Join(run.Dir, "lists", "3.list")); got != "after\n" {
		t.Errorf("the list of the check is %q", got)
	}
	if box.verdict(run) != nil {
		t.Error("the verdict of the check before is still there: the new one would be taken for it")
	}
}

// A check that could not be started is started again by the same click, as a check and not as a repair.
func TestAResumeOfACheckThatDidNotStartIsACheck(t *testing.T) {
	box := newUpgradeBox(t)
	box.readyToResume()
	run := box.aHopThatRestarted()
	run = box.say(run, osrun.Marker{Kind: osrun.MkAttempt, N: 2, Phase: osrun.PhaseVerify, Who: osrun.WhoAuto}, osrun.Marker{Kind: osrun.MkStartFailed, N: 2})
	box.point(run)
	if state := box.state(run); state.Name != osrun.StateFailed || state.Reason != osrun.ReasonStart || !state.PNR {
		t.Fatalf("the run is %+v", state)
	}

	if _, err := box.ResumeSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	attempts := box.attemptsOf(run)
	if len(attempts) != 3 || attempts[2].Phase != osrun.PhaseVerify || attempts[2].Who != osrun.WhoOwner {
		t.Errorf("the attempts are %+v", attempts)
	}
}

// A unit that could not be started is said, and the log says that it did not start.
func TestAResumeWhoseUnitDoesNotStartIsSaid(t *testing.T) {
	box := newUpgradeBox(t)
	box.readyToResume()
	run := box.aHopThatStoppedHalfWay(osrun.ClassNetwork)
	box.own(run, osrun.StateStoppedAtWaypoint)
	box.mu.Lock()
	box.startErr = errors.New("exit status 1")
	box.mu.Unlock()

	_, err := box.ResumeSystemOSUpgrade()
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeStartFailed {
		t.Errorf("refused with %q", refusal.Code)
	}
	if got := markersOfKind(box.read(run.ID), osrun.MkStartFailed); len(got) != 1 {
		t.Errorf("the log says %+v", box.read(run.ID).Markers)
	}
}

// The click works with the switch off.
func TestAResumeWorksWithTheSwitchOff(t *testing.T) {
	box := newUpgradeBox(t)
	box.readyToResume()
	run := box.aHopThatStoppedHalfWay(osrun.ClassNetwork)
	box.own(run, osrun.StateStoppedAtWaypoint)
	if osUpgradeBetaOn() {
		t.Fatal("the switch was on")
	}
	if _, err := box.ResumeSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
}
