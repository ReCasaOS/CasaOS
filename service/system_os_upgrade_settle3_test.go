package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// A boot is written down only once the units of apt that came back with it are stopped: a stop that failed is tried at the next look, and the timers of apt do not run through the
// check with a log that says they were dealt with.
func TestABootIsNotWrittenDownWhileAUnitOfAptCannotBeStopped(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatRestarted()
	box.point(run)
	box.setSystem("starting")
	box.setActive("apt-daily.timer", "active")
	box.stopErr = errors.New("systemctl stop: timed out")

	box.tick()
	if got := markersOfKind(box.read(run.ID), osrun.MkBootSeen); len(got) != 0 {
		t.Fatalf("the boot was written down with a timer of apt that runs: %+v", got)
	}

	box.stopErr = nil
	box.advance(30 * time.Second)
	box.tick()
	if got := markersOfKind(box.read(run.ID), osrun.MkBootSeen); len(got) != 1 {
		t.Fatalf("the boot was written down %d times once the timer could be stopped", len(got))
	}
	if got := box.commandsWith("systemctl stop apt-daily.timer"); len(got) != 2 {
		t.Errorf("the timer was stopped by %q, want two tries", got)
	}
}

// A systemd that says nothing of a unit of apt is not one that says it is stopped.
func TestABootIsNotWrittenDownWhileSystemdSaysNothingOfTheUnitsOfApt(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatRestarted()
	box.point(run)
	box.setSystem("starting")
	was := box.updater.command
	box.updater.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) == 2 && args[0] == "is-active" && args[1] == "apt-daily.timer" {
			return nil, errors.New("timed out")
		}
		return was(ctx, name, args...)
	}

	box.tick()
	if got := markersOfKind(box.read(run.ID), osrun.MkBootSeen); len(got) != 0 {
		t.Errorf("the boot was written down with a unit that was not answered about: %+v", got)
	}
}

// The restart is the next thing to do only when the status offers it: a log that says the hop is at its restart, with the file of the check before it gone, offers the owner the
// hand-over and not the restart, and the call does not restart the machine.
func TestTheRestartIsNotMadeWhenTheStateDoesNotOfferIt(t *testing.T) {
	box := newUpgradeBox(t)
	box.greenBarrier()
	run := box.aHopAtItsRestart()
	box.point(run)
	if err := os.Remove(filepath.Join(box.backupOf(run), "done-prereboot")); err != nil {
		t.Fatal(err)
	}
	box.tick()
	if status := box.GetSystemOSUpgradeStatus(); !reflect.DeepEqual(status.Actions, []string{osrun.ActionTakeover}) {
		t.Fatalf("the actions are %v", status.Actions)
	}
	told := box.aMachineThatRestarts(run, nil)

	_, err := box.RebootSystemOSUpgrade()
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeNotAwaitingReboot {
		t.Errorf("refused with %q", refusal.Code)
	}
	if got := told.all(); len(got) != 0 {
		t.Errorf("the machine was told %q", got)
	}
}

// A start that was written and never got its run line, because the core stopped between the two, says nothing of itself and has no button: after the time a start that did not come
// is waited for, the loop forgets it, and the owner can start again.
func TestAStartThatNeverGotItsRunLineIsForgotten(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	spec := box.specFor(plan, osrun.RunHop)
	if err := writeOSUpgradeRun(spec); err != nil {
		t.Fatal(err)
	}
	if err := createOSUpgradeMarkers(osUpgradeRunFolder(spec.Meta.RunID), "0123456789abcdef0123456789abcdef", box.now()); err != nil {
		t.Fatal(err)
	}
	if err := setOSUpgradeCurrent(spec.Meta.RunID); err != nil {
		t.Fatal(err)
	}
	if err := setOSUpgradeBusy(spec.Meta.RunID, osrun.StateRunning, box.now()); err != nil {
		t.Fatal(err)
	}

	box.tick()
	if _, current := currentOSUpgrade(); current != osUpgradeHasRun || !box.flagIsThere() {
		t.Fatal("a start that was just written was forgotten")
	}

	box.advance(time.Minute)
	box.tick()
	if _, current := currentOSUpgrade(); current != osUpgradeNoRun || box.flagIsThere() {
		t.Errorf("the start that never began still holds the machine: pointer %v, flag %v", current, box.flagIsThere())
	}
	if status, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan)); err != nil || status.RunID == spec.Meta.RunID {
		t.Errorf("the owner could not start again: %+v, %v", status, err)
	}
}

// The page's restart and shutdown stay refused while the unit is seen to run, whatever the log says of the run: a run found damaged is not a run whose dpkg is gone.
func TestThePowerButtonsAreRefusedWhileTheUnitRunsEvenForARunFoundDamaged(t *testing.T) {
	box := newUpgradeBox(t)
	box.anUpgradeAt(osrun.StateRunning)
	id, _ := currentOSUpgrade()
	run := box.read(id)
	if err := os.WriteFile(filepath.Join(run.Dir, osUpgradeBlocksDir, "stray"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if state := box.state(run); state.Name != osrun.StateNeedsAttention {
		t.Fatalf("the run reads as %q, want a run found damaged", state.Name)
	}
	power := aPowerButton(t, nil)

	refusal := refusalOf(t, box.SystemReboot())
	if refusal.Code != OSUpgradeOpen {
		t.Errorf("refused with %q", refusal.Code)
	}
	if got := power.asked(); len(got) != 0 {
		t.Errorf("the machine was asked to %q", got)
	}

	box.setUnit("inactive")
	if err := box.SystemReboot(); err != nil {
		t.Errorf("refused with the unit gone: %v", err)
	}
}
