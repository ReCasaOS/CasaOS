package service

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// A rehearsal from the owner's start to its closing: the sources of the next release are put in place and held for the time they are given, the owner drops it, the machine is put
// back, and the run is closed. The unit is the box's: what its driver would write is written by the test, in the order the driver writes it.
func TestARehearsalFromItsStartToItsClosing(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	voice := box.withAVoice()

	status, err := box.StartSystemOSUpgrade(OSUpgradeStartRequest{PlanID: plan.PlanID, Rehearsal: true, Hostname: "box1", Acks: []string{osUpgradeAckNoInterruption}})
	if err != nil {
		t.Fatal(err)
	}
	run := box.read(status.RunID)
	if status.State != osrun.StateRehearsing || !status.Busy || !box.flagIsThere() || len(box.startedUnits()) != 1 {
		t.Fatalf("after the start: %q (busy %v), flag %v, %d units", status.State, status.Busy, box.flagIsThere(), len(box.startedUnits()))
	}

	// the unit runs the reversible half and leaves the sources of the next release in place
	box.setUnit("active")
	run = box.say(run, osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRehearse, Boot: boxBoot1, PID: 101})
	run = box.leave(run, "apt-orig", "done-timers", "done-download")
	run = box.say(run, osrun.Marker{Kind: osrun.MkRehearsalReady, N: 1}, osrun.Marker{Kind: osrun.MkUnitEnd, N: 1, Result: "ok"})
	box.setUnit("inactive")
	box.advance(time.Minute)

	tick := box.tick()
	if tick.State != osrun.StateRehearsed || !box.flagIsThere() {
		t.Fatalf("the rehearsal is %q, flag %v: the machine holds the sources of the next release and is held", tick.State, box.flagIsThere())
	}
	if got := box.GetSystemOSUpgradeStatus(); !reflect.DeepEqual(got.Actions, []string{osrun.ActionDiscard}) || got.ExpiresAt == nil {
		t.Errorf("the status says %+v", got)
	}
	if !box.OSUpgradeMuted() {
		t.Error("the apps' alerts are not quiet while the machine is held")
	}

	// the owner drops it: the machine is put back, by a unit of its own
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "discard"}); err != nil {
		t.Fatal(err)
	}
	if got := box.attemptsOf(run); len(got) != 2 || got[1].Phase != osrun.PhaseUnwind || len(box.startedUnits()) != 2 {
		t.Fatalf("attempts %+v, %d units", got, len(box.startedUnits()))
	}
	box.setUnit("active")
	run = box.say(box.read(run.ID), osrun.Marker{Kind: osrun.MkUnitStart, N: 2, Phase: osrun.PhaseUnwind, Boot: boxBoot1, PID: 102})
	if err := os.Rename(osUpgradeBackupFolder(run.Meta.BK), filepath.Join(run.Dir, osUpgradeBKAborted)); err != nil {
		t.Fatal(err)
	}
	box.say(run, osrun.Marker{Kind: osrun.MkUnwound, N: 2, Result: "ok"}, osrun.Marker{Kind: osrun.MkUnitEnd, N: 2, Result: "ok"})
	box.setUnit("inactive")
	box.advance(time.Minute)

	tick = box.tick()
	if tick.State != osrun.StateRehearsalDone || box.flagIsThere() {
		t.Fatalf("the rehearsal is %q, flag %v: the machine was put back and is let go of", tick.State, box.flagIsThere())
	}
	told := voice.told()
	if len(told) != 1 || !strings.Contains(told[0].sentence, "rehearsal") {
		t.Errorf("the owner was told %+v", told)
	}

	// the plan is as good as it was: the owner makes a real start with it
	if !box.osUpgradePlanAlive() {
		t.Error("the plan was dropped with the rehearsal")
	}
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge"}); err != nil {
		t.Fatal(err)
	}
	if _, state := currentOSUpgrade(); state != osUpgradeNoRun || box.flagIsThere() {
		t.Error("the rehearsal is not closed")
	}
}

// A hop from the owner's start to its end, as the core sees it: the unit does the blocks up to the restart, the barrier is green, the owner restarts the machine, the machine comes
// back in another boot, the core checks it when it is ready, judges it, tells the owner, and the owner closes the run.
func TestAHopFromItsStartToItsClosing(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	theBarrierSees(t)
	voice := box.withAVoice()
	told := box.aMachineThatRestartsWith(nil)

	status, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if err != nil {
		t.Fatal(err)
	}
	run := box.read(status.RunID)
	if status.State != osrun.StateRunning || !box.flagIsThere() {
		t.Fatalf("after the start: %q, flag %v", status.State, box.flagIsThere())
	}

	// the unit goes through the blocks, and ends when the machine waits for its restart
	box.setUnit("active")
	run = box.say(run, osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRun, Boot: boxBoot1, PID: 101})
	for i, id := range osrun.BlockList(osrun.PhaseRun, osrun.Files{}) {
		run = box.block(run, 1, i+1, id, true)
	}
	run = box.leave(run, "begun-upgrade", "done-minimal", "done-full", "done-prereboot")
	if err := os.WriteFile(filepath.Join(box.backupOf(run), "boot_id"), []byte(boxBoot1+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(box.backupOf(run), "kernel"), []byte("6.1.0-18-amd64\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run = box.say(run, osrun.Marker{Kind: osrun.MkRebootPending, N: 1, Boot: boxBoot1, Kernel: "6.1.0-18-amd64"}, osrun.Marker{Kind: osrun.MkUnitEnd, N: 1, Result: "ok"})
	box.setUnit("inactive")
	box.advance(time.Minute)

	// the machine waits: the barrier is green, the owner is told, and the restart is offered
	if tick := box.tick(); tick.State != osrun.StateAwaitingReboot {
		t.Fatalf("the hop is %q", tick.State)
	}
	if got := box.GetSystemOSUpgradeStatus(); !reflect.DeepEqual(got.Actions, []string{osrun.ActionReboot, osrun.ActionTakeover}) || got.AfterRebootHint == nil {
		t.Fatalf("the status says %+v", got)
	}
	if got := voice.told(); len(got) != 1 || !strings.Contains(got[0].key, ":awaiting_reboot:") {
		t.Fatalf("the owner was told %+v", got)
	}

	// the owner restarts it; it comes back in another boot
	if _, err := box.RebootSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	if got := told.all(); len(got) != 2 {
		t.Errorf("the machine was told %q", got)
	}
	box.setBoot(boxBoot2)
	box.setSystem("running")
	box.advance(time.Minute)

	// the core sees the new boot, stops what apt does by itself, and starts the check when the machine is ready
	box.setActive("apt-daily.timer", "active")
	if tick := box.tick(); tick.State != osrun.StateResuming || tick.Action != osrun.DoLaunchVerify {
		t.Fatalf("the look says %+v", tick)
	}
	if got := box.attemptsOf(run); len(got) != 2 || got[1].Phase != osrun.PhaseVerify || got[1].Who != osrun.WhoAuto {
		t.Fatalf("attempts %+v", got)
	}
	if got := box.commandsWith("systemctl stop apt-daily.timer"); len(got) != 1 {
		t.Errorf("the timers of apt were not stopped at the new boot: %q", box.commandsWith("systemctl stop"))
	}

	// the check runs, and ends
	box.setUnit("active")
	run = box.say(box.read(run.ID), osrun.Marker{Kind: osrun.MkUnitStart, N: 2, Phase: osrun.PhaseVerify, Boot: boxBoot2, PID: 102})
	run = box.block(run, 2, 1, osrun.BlockAfter, true)
	run = box.leave(run, "done-after")
	run = box.say(run, osrun.Marker{Kind: osrun.MkVerifyDone, N: 2, Boot: boxBoot2}, osrun.Marker{Kind: osrun.MkUnitEnd, N: 2, Result: "ok"})
	box.setUnit("inactive")
	box.advance(time.Minute)

	// the core judges the machine: it is as it was, and for the kernel it is what the hop installed
	before := osverify.Facts{Taken: true, At: box.now(), Kernel: "5.10.0-28-amd64", System: "running", DpkgQuiet: true}
	box.theFactsBefore(run, before)
	after := before
	after.Kernel = "6.1.0-18-amd64"
	takesFacts(t, after)
	if tick := box.tick(); tick.Action != osrun.DoJudge || len(tick.Done) != 1 {
		t.Fatalf("the look says %+v", tick)
	}
	box.advance(30 * time.Second)
	tick := box.tick()
	if tick.State != osrun.StateSucceeded || box.flagIsThere() {
		t.Fatalf("the hop is %q, flag %v", tick.State, box.flagIsThere())
	}
	if got := voice.told(); len(got) != 2 || !strings.Contains(got[1].key, ":succeeded:") || !strings.Contains(got[1].sentence, "upgraded") {
		t.Errorf("the owner was told %+v", got)
	}

	// the owner closes it: the folder of the backup of a hop that ended is moved into the run, and the next hop's blocks do not stop at it
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(run.Dir, osUpgradeBKDone, "done-after")); err != nil {
		t.Errorf("the backup folder was not moved into the run: %v", err)
	}
	if _, state := currentOSUpgrade(); state != osUpgradeNoRun || box.flagIsThere() {
		t.Error("the hop is not closed")
	}
	if status := box.GetSystemOSUpgradeStatus(); status.State != osrun.StateIdle {
		t.Errorf("the status is %q", status.State)
	}
}
