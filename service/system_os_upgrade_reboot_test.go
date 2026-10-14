package service

import (
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// whatTheMachineWasToldToDo is the restart as the machine hears it: in the order the calls come, and with what the log of the run said at each.
type whatTheMachineWasToldToDo struct {
	mu     sync.Mutex
	events []string
}

func (w *whatTheMachineWasToldToDo) add(event string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, event)
}

func (w *whatTheMachineWasToldToDo) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.events...)
}

// aMachineThatRestarts puts the restart and the writing to the disks in the place of the real ones: they say what the log of the run holds when they are called, and the restart
// fails with the error that it is given.
func (b *upgradeBox) aMachineThatRestarts(run *osUpgradeRun, failWith error) *whatTheMachineWasToldToDo {
	b.t.Helper()
	told := &whatTheMachineWasToldToDo{}
	wasSync, wasPower := osUpgradeSync, osUpgradePowerAction
	osUpgradeSync = func() { told.add("sync after " + lastWordOf(b.read(run.ID))) }
	osUpgradePowerAction = func(action string) error {
		told.add(action + " after " + lastWordOf(b.read(run.ID)))
		return failWith
	}
	b.t.Cleanup(func() { osUpgradeSync, osUpgradePowerAction = wasSync, wasPower })
	b.updater.dpkgLocked = func() bool { return false }
	return told
}

// aMachineThatRestartsWith is aMachineThatRestarts that only says what it was told, and not what the log held when it was.
func (b *upgradeBox) aMachineThatRestartsWith(failWith error) *whatTheMachineWasToldToDo {
	b.t.Helper()
	told := &whatTheMachineWasToldToDo{}
	wasSync, wasPower := osUpgradeSync, osUpgradePowerAction
	osUpgradeSync = func() { told.add("sync") }
	osUpgradePowerAction = func(action string) error {
		told.add(action)
		return failWith
	}
	b.t.Cleanup(func() { osUpgradeSync, osUpgradePowerAction = wasSync, wasPower })
	b.updater.dpkgLocked = func() bool { return false }
	return told
}

func lastWordOf(run *osUpgradeRun) string {
	return run.Markers[len(run.Markers)-1].Kind
}

func (b *upgradeBox) greenBarrier() {
	b.t.Helper()
	theBarrierSees(b.t)
}

// The restart is asked for when the machine waits for it and what stands before it is green: the owner's word is in the log first, then the disks are written, then systemd is
// asked.
func TestTheRestartIsAskedForInThatOrder(t *testing.T) {
	box := newUpgradeBox(t)
	box.greenBarrier()
	run := box.aHopAtItsRestart()
	box.point(run)
	box.tick()
	told := box.aMachineThatRestarts(run, nil)

	status, err := box.RebootSystemOSUpgrade()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sync after " + osrun.MkRebootRequested, "reboot after " + osrun.MkRebootRequested}
	if got := told.all(); !reflect.DeepEqual(got, want) {
		t.Errorf("the machine was told %q, want %q", got, want)
	}
	if status.State != osrun.StateRebooting || status.Reboot == nil || !status.Reboot.RequestedAt.Equal(box.now()) {
		t.Errorf("the status is %+v", status)
	}
	if got := markersOfKind(box.read(run.ID), osrun.MkRebootRequested); len(got) != 1 || got[0].N != 0 {
		t.Errorf("the log says %+v", got)
	}
}

// The restart is the core's own: it does not go through the call that the owner's page makes to restart the machine, which an upgrade that is open refuses.
func TestTheRestartIsTheCoresOwnAndNotThePublicOne(t *testing.T) {
	// the seam is the internal function, so that the public ones can refuse while an upgrade runs
	if reflect.ValueOf(osUpgradePowerAction).Pointer() != reflect.ValueOf(runSystemctlPowerAction).Pointer() {
		t.Error("the restart of an upgrade is not the internal function")
	}
}

// A red barrier is not restarted through: the click is refused with the checks, and nothing was written or asked.
func TestTheRestartIsRefusedWithARedBarrier(t *testing.T) {
	box := newUpgradeBox(t)
	machine := theBarrierSees(t)
	machine.set("/etc/fstab", "UUID=DEAD-BEEF /backup ext4 defaults 0 2\n")
	run := box.aHopAtItsRestart()
	box.point(run)
	told := box.aMachineThatRestarts(run, nil)

	_, err := box.RebootSystemOSUpgrade()
	refusal := refusalOf(t, err)
	checks, _ := refusal.Data["checks"].([]OSUpgradeBarrierItem)
	if refusal.Code != OSUpgradeBarrierFailed || len(checks) != 2 || checks[0].OK {
		t.Errorf("refused with %q %+v", refusal.Code, refusal.Data)
	}
	if got := told.all(); len(got) != 0 {
		t.Errorf("the machine was told %q", got)
	}
	if got := markersOfKind(box.read(run.ID), osrun.MkRebootRequested); len(got) != 0 {
		t.Errorf("the log says %+v", got)
	}
}

// The barrier is asked again at the click: a machine that was green a half minute ago and is not now is not restarted on the word of a file.
func TestTheBarrierIsAskedAgainAtTheClick(t *testing.T) {
	box := newUpgradeBox(t)
	machine := theBarrierSees(t)
	run := box.aHopAtItsRestart()
	box.point(run)
	box.tick()
	if got := box.barrierOf(run); got == nil || !got.OK {
		t.Fatalf("the barrier is %+v", got)
	}
	told := box.aMachineThatRestarts(run, nil)
	machine.set("/etc/fstab", "UUID=DEAD-BEEF /backup ext4 defaults 0 2\n")

	_, err := box.RebootSystemOSUpgrade()
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeBarrierFailed {
		t.Errorf("refused with %q", refusal.Code)
	}
	if got := told.all(); len(got) != 0 {
		t.Errorf("the machine was told %q", got)
	}
	if got := box.barrierOf(run); got == nil || got.OK {
		t.Errorf("the barrier that the click found is not kept: %+v", got)
	}
}

// A barrier that was not asked yet is asked by the click, and a green one lets the restart go.
func TestTheClickAsksABarrierThatWasNotAskedYet(t *testing.T) {
	box := newUpgradeBox(t)
	box.greenBarrier()
	run := box.aHopAtItsRestart()
	box.point(run)
	told := box.aMachineThatRestarts(run, nil)

	if _, err := box.RebootSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	if len(told.all()) != 2 {
		t.Errorf("the machine was told %q", told.all())
	}
}

// The states in which the machine does not wait for its restart say so, with the state: running, a restart that is under way, a machine that was restarted already.
func TestTheRestartIsOnlyForAMachineThatWaitsForIt(t *testing.T) {
	cases := []struct {
		name  string
		state string
		set   func(box *upgradeBox) *osUpgradeRun
	}{
		{"the unit runs", osrun.StateRunning, func(box *upgradeBox) *osUpgradeRun { return box.aHopAtItsDownload() }},
		{"the restart is under way", osrun.StateRebooting, func(box *upgradeBox) *osUpgradeRun {
			run := box.aHopAtItsRestart()
			return box.say(run, osrun.Marker{Kind: osrun.MkRebootRequested})
		}},
		{"the machine restarted", osrun.StateResuming, func(box *upgradeBox) *osUpgradeRun { return box.aHopThatRestarted() }},
		{"it stopped half way", osrun.StateStoppedAtWaypoint, func(box *upgradeBox) *osUpgradeRun { return box.aHopThatStoppedHalfWay(osrun.ClassBusy) }},
		{"it is a rehearsal", osrun.StateRehearsed, func(box *upgradeBox) *osUpgradeRun { return box.aRehearsal() }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			box := newUpgradeBox(t)
			box.greenBarrier()
			run := c.set(box)
			box.point(run)
			told := box.aMachineThatRestarts(run, nil)

			_, err := box.RebootSystemOSUpgrade()
			refusal := refusalOf(t, err)
			if refusal.Code != OSUpgradeNotAwaitingReboot || refusal.Data["state"] != c.state {
				t.Errorf("refused with %q %v, want %q for %q", refusal.Code, refusal.Data, OSUpgradeNotAwaitingReboot, c.state)
			}
			if len(told.all()) != 0 {
				t.Errorf("the machine was told %q", told.all())
			}
		})
	}
}

// A restart that was asked for a long time ago and did not happen is asked for again by the same button.
func TestARestartThatStalledIsAskedForAgain(t *testing.T) {
	box := newUpgradeBox(t)
	box.greenBarrier()
	run := box.aHopAtItsRestart()
	box.point(run)
	box.say(run, osrun.Marker{Kind: osrun.MkRebootRequested})
	box.advance(osUpgradeTiming.RebootStall + time.Minute)
	told := box.aMachineThatRestarts(run, nil)

	status, err := box.RebootSystemOSUpgrade()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != osrun.StateRebooting || len(told.all()) != 2 {
		t.Errorf("state %q, the machine was told %q", status.State, told.all())
	}
}

// A refresh that wants a restart has no barrier: the restart is the owner's, and it is asked for at once.
func TestARefreshThatWantsARestartIsRestartedWithoutABarrier(t *testing.T) {
	box := newUpgradeBox(t)
	machine := theBarrierSees(t)
	machine.set("/etc/fstab", "UUID=DEAD-BEEF /backup ext4 defaults 0 2\n")
	run := box.aRefreshThatWantsARestart()
	box.point(run)
	told := box.aMachineThatRestarts(run, nil)

	if _, err := box.RebootSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	if len(told.all()) != 2 {
		t.Errorf("the machine was told %q", told.all())
	}
}

// Nothing is restarted under a unit that runs, or while systemd does not say whether one does.
func TestTheRestartIsRefusedWhileAUnitMayRun(t *testing.T) {
	for _, unit := range []string{"active", ""} {
		box := newUpgradeBox(t)
		box.greenBarrier()
		run := box.aHopAtItsRestart()
		box.point(run)
		box.setUnit(unit)
		told := box.aMachineThatRestarts(run, nil)

		_, err := box.RebootSystemOSUpgrade()
		if refusal := refusalOf(t, err); refusal.Code != OSUpgradeUnitActive {
			t.Errorf("unit %q: refused with %q", unit, refusal.Code)
		}
		if len(told.all()) != 0 {
			t.Errorf("unit %q: the machine was told %q", unit, told.all())
		}
	}
}

// Nothing is restarted while a package manager works, or while whether one does cannot be seen: the restart would cut dpkg.
func TestTheRestartIsRefusedWhileDpkgIsLocked(t *testing.T) {
	box := newUpgradeBox(t)
	box.greenBarrier()
	run := box.aHopAtItsRestart()
	box.point(run)
	told := box.aMachineThatRestarts(run, nil)

	box.updater.dpkgLocked = func() bool { return true }
	_, err := box.RebootSystemOSUpgrade()
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeDpkgLocked {
		t.Errorf("refused with %q", refusal.Code)
	}

	box.updater.dpkgLocked = nil
	_, err = box.RebootSystemOSUpgrade()
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeDpkgLocked || refusal.Data["unknown"] != true {
		t.Errorf("refused with %q %v, want the lock that could not be seen", refusal.Code, refusal.Data)
	}
	if len(told.all()) != 0 {
		t.Errorf("the machine was told %q", told.all())
	}
}

// A restart that systemd refuses is taken back in the log, and the owner is told that it failed: the machine still waits.
func TestARestartThatSystemdRefusesIsTakenBack(t *testing.T) {
	box := newUpgradeBox(t)
	box.greenBarrier()
	run := box.aHopAtItsRestart()
	box.point(run)
	box.aMachineThatRestarts(run, errors.New("Failed to connect to bus"))

	_, err := box.RebootSystemOSUpgrade()
	refusal := refusalOf(t, err)
	if refusal.Code != OSUpgradeRebootFailed {
		t.Errorf("refused with %q", refusal.Code)
	}
	run = box.read(run.ID)
	if got := markersOfKind(run, osrun.MkRebootFailed); len(got) != 1 {
		t.Fatalf("the log says %+v", run.Markers)
	}
	if state := box.state(run); state.Name != osrun.StateAwaitingReboot {
		t.Errorf("the machine is %q: it still waits", state.Name)
	}
}

func TestThereIsNothingToRestartWithoutARun(t *testing.T) {
	box := newUpgradeBox(t)
	_, err := box.RebootSystemOSUpgrade()
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeNoRun {
		t.Errorf("refused with %q", refusal.Code)
	}
}

// A second click while the first is under way is no second restart.
func TestAClickThatComesTwiceRestartsOnce(t *testing.T) {
	box := newUpgradeBox(t)
	box.greenBarrier()
	run := box.aHopAtItsRestart()
	box.point(run)
	told := box.aMachineThatRestarts(run, nil)

	if _, err := box.RebootSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	_, err := box.RebootSystemOSUpgrade()
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeNotAwaitingReboot {
		t.Errorf("the second click was refused with %q", refusal.Code)
	}
	if len(told.all()) != 2 {
		t.Errorf("the machine was told %q", told.all())
	}
}

// The restart works with the switch off: a machine that waits for it is not trapped behind the switch.
func TestTheRestartWorksWithTheSwitchOff(t *testing.T) {
	box := newUpgradeBox(t)
	box.greenBarrier()
	run := box.aHopAtItsRestart()
	box.point(run)
	box.aMachineThatRestarts(run, nil)
	if _, err := os.Lstat(osUpgradeBetaFile); err == nil || !os.IsNotExist(err) {
		t.Fatalf("the switch is on: %v", err)
	}
	if _, err := box.RebootSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
}

// aRefreshThatWantsARestart is a refresh that ended and says that the machine needs a restart, on the boot that it ran in.
func (b *upgradeBox) aRefreshThatWantsARestart() *osUpgradeRun {
	return b.aRefreshWith("yes", "kernel")
}
