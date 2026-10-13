package service

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// ---- the flag of the open upgrade ------------------------------------------------------------------------------------------------

// A flag with no run behind it is a stale one, and goes: a core that was killed between taking a run away and taking its flag away leaves one, and it keeps the backups and the
// updates of the box away for ever.
func TestTheSettlingTakesAStrayFlagAway(t *testing.T) {
	box := newUpgradeBox(t)
	if err := setOSUpgradeBusy("0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19", osrun.StateRunning, box.now()); err != nil {
		t.Fatal(err)
	}
	box.tick()
	if box.flagIsThere() {
		t.Fatal("the flag of an upgrade that is not there is still there")
	}
}

// A pointer that is there and says nothing that can be used is a run that is not known to be over: its flag stays, and so does the machine's refusal to be backed up.
func TestTheSettlingLeavesTheFlagWhenTheRunCannotBeRead(t *testing.T) {
	box := newUpgradeBox(t)
	if err := setOSUpgradeBusy("0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19", osrun.StateRunning, box.now()); err != nil {
		t.Fatal(err)
	}
	box.putInRoot(osUpgradeCurrentFile, "this is not a run id\n")
	box.tick()
	if !box.flagIsThere() {
		t.Fatal("the flag was taken away with a pointer that cannot be read")
	}

	// the same for a pointer that names a run whose folder is not there
	box.putInRoot(osUpgradeCurrentFile, "0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19\n")
	box.tick()
	if !box.flagIsThere() {
		t.Fatal("the flag was taken away from a run that cannot be read")
	}
}

// The flag is there while the run holds the machine, and it says which run and which state.
func TestTheSettlingKeepsTheFlagOfARunThatHoldsTheMachine(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopAtItsRestart()
	box.point(run)

	tick := box.tick()
	if !box.flagIsThere() {
		t.Fatal("a run that waits for its restart has no flag")
	}
	flag := readText(t, osUpgradeBusyFile)
	if !strings.Contains(flag, `"run_id":"`+run.ID+`"`) || !strings.Contains(flag, `"state":"awaiting_reboot"`) || tick.State != osrun.StateAwaitingReboot {
		t.Errorf("the flag is %q, the tick saw %+v", flag, tick)
	}
}

// What a run says its state is, the flag says too: a run that was running and is now waiting for the restart changes it.
func TestTheSettlingWritesTheFlagAgainWhenTheStateChanges(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopAtItsRestart()
	box.own(run, osrun.StateRunning)

	box.tick()
	if flag := readText(t, osUpgradeBusyFile); !strings.Contains(flag, `"state":"awaiting_reboot"`) {
		t.Errorf("the flag is %q", flag)
	}
}

// The flag was the owner's start's, and a run that lost it (a core that was killed) gets it back: nothing else keeps the packages of the machine from being read as they change.
func TestTheSettlingPutsTheFlagBackForARunThatLostIt(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopAtItsRestart()
	box.point(run)
	if box.flagIsThere() {
		t.Fatal("the flag was there to begin with")
	}
	box.tick()
	if !box.flagIsThere() {
		t.Fatal("the run holds the machine and has no flag")
	}
}

// A flag that says what the state says is not written again: the settling looks every half minute, and a disk that is made to write that often has a reason to complain.
func TestTheSettlingDoesNotWriteTheFlagThatSaysTheSame(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopAtItsRestart()
	box.point(run)
	box.tick()

	var writes atomic.Int32
	was := osUpgradeWriteFile
	osUpgradeWriteFile = func(path string, data []byte, mode os.FileMode) error {
		writes.Add(1)
		return was(path, data, mode)
	}
	t.Cleanup(func() { osUpgradeWriteFile = was })
	box.advance(30 * time.Second)
	box.tick()
	box.advance(30 * time.Second)
	box.tick()
	if writes.Load() != 0 {
		t.Errorf("the flag was written %d times for a state that did not change", writes.Load())
	}
}

// A run that has let go of the machine (it ended well, or failed before it changed anything) takes its flag away; it stays the latest run until the owner closes it.
func TestTheSettlingTakesTheFlagAwayWhenTheRunLetsGoOfTheMachine(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan("ok", nil), osrun.RunHop)
	run = box.attempt(run, 1, osrun.PhaseRun, osrun.WhoOwner)
	run = box.block(run, 1, 4, osrun.BlockSources, false)
	run = box.stop(run, 1, 4, osrun.BlockSources, osrun.ClassNetwork, false)
	run = box.say(run, osrun.Marker{Kind: osrun.MkUnwound, N: 1, Result: "ok"})
	run = box.end(run, 1, "stopped")
	box.own(run, osrun.StateRunning)

	tick := box.tick()
	if box.flagIsThere() || tick.State != osrun.StateFailed {
		t.Errorf("the flag is there: %v, the state is %q", box.flagIsThere(), tick.State)
	}
	if id, state := currentOSUpgrade(); state != osUpgradeHasRun || id != run.ID {
		t.Errorf("the pointer is %q (%d): the run stays the latest until it is closed", id, state)
	}
}

// A start that wrote its folder and its flag and has not yet written its first attempt is on its way, and its flag is not the settling's to take away.
func TestTheSettlingLeavesAStartThatIsOnItsWayAlone(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan("ok", nil), osrun.RunHop)
	box.own(run, osrun.StateRunning)

	box.tick()
	if !box.flagIsThere() {
		t.Fatal("the flag of a start that is on its way was taken away")
	}
	if got := box.attemptsOf(run); len(got) != 0 {
		t.Errorf("%d attempts were launched for a start that is still being written", len(got))
	}

	// a start that never got to its attempt is closed after the half minute, and its flag goes with it
	box.advance(time.Minute)
	box.tick()
	if box.flagIsThere() {
		t.Error("a start that was lost still holds the machine")
	}
}

// The settling reads the run and then takes the lock to write the flag: a run that is not the latest any more by then has no say about the flag of the one that is.
func TestTheFlagOfAnotherRunIsNotMoved(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopAtItsRestart()
	other := "7c1d9e44-2b6f-4a8e-8d35-1e0f6a9b3c27"
	if err := setOSUpgradeBusy(other, osrun.StateRunning, box.now()); err != nil {
		t.Fatal(err)
	}
	box.putInRoot(osUpgradeCurrentFile, other+"\n")

	box.osUpgradeKeepFlag(run, osrun.State{Name: osrun.StateAwaitingReboot, Busy: true})
	if flag := readText(t, osUpgradeBusyFile); !strings.Contains(flag, other) {
		t.Errorf("the flag of the latest run was written over: %q", flag)
	}
	box.osUpgradeKeepFlag(run, osrun.State{Name: osrun.StateSucceeded})
	if !box.flagIsThere() {
		t.Error("the flag of the latest run was taken away by another's state")
	}
}

// The state that the look read may be out of date by the time the flag is written: a flag is taken away only on what the run says under the lock, and a run that holds the
// machine keeps it.
func TestAFlagIsNotTakenAwayOnAStateThatIsOutOfDate(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopAtItsRestart()
	box.own(run, osrun.StateAwaitingReboot)

	box.osUpgradeKeepFlag(run, osrun.State{Name: osrun.StateIdle, RunID: ""})
	if !box.flagIsThere() {
		t.Fatal("the flag of a run that waits for its restart was taken away on a state that was read before the run began")
	}
}

// ---- what the core does about a run -----------------------------------------------------------------------------------------------

// A run that was lost before its point of no return is undone, by the core and with no word of the owner: one ATTEMPT of the unwind, and the unit that runs it.
func TestTheSettlingUnwindsARunThatWasLostBeforeThePointOfNoReturn(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasLost()
	box.own(run, osrun.StateRunning)

	tick := box.tick()
	attempts := box.attemptsOf(run)
	if len(attempts) != 2 || attempts[1].N != 2 || attempts[1].Phase != osrun.PhaseUnwind || attempts[1].Who != osrun.WhoAuto {
		t.Fatalf("the attempts are %+v", attempts)
	}
	if len(box.startedUnits()) != 1 || tick.Action != osrun.DoUnwind {
		t.Errorf("%d units were started, the tick did %q", len(box.startedUnits()), tick.Action)
	}
	if got := readText(t, filepath.Join(run.Dir, "lists", "2.list")); got != "" {
		t.Errorf("the unwind has a list of %q: it runs no block", got)
	}
}

// A unit that was started is not started again by the next look, whether it has said it began or not: a number of attempt is launched once.
func TestTheSettlingLaunchesOnceForOneAttempt(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasLost()
	box.own(run, osrun.StateRunning)

	box.tick()
	box.advance(time.Second)
	box.tick()
	box.advance(time.Second)
	box.tick()
	if got := box.attemptsOf(run); len(got) != 2 {
		t.Errorf("%d attempts: the unit that was started was started again", len(got))
	}
	if len(box.startedUnits()) != 1 {
		t.Errorf("%d units were started", len(box.startedUnits()))
	}
}

// Looks that come together, from the loop and from a button, start one unit for one thing to do.
func TestLooksThatComeTogetherLaunchOnce(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasLost()
	box.own(run, osrun.StateRunning)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			box.tick()
		}()
	}
	wg.Wait()
	if got := box.attemptsOf(run); len(got) != 2 || len(box.startedUnits()) != 1 {
		t.Errorf("%d attempts and %d units for one thing to do", len(got), len(box.startedUnits()))
	}
}

// Nothing is started while systemd does not answer: a unit may be running, and nobody knows.
func TestTheSettlingLaunchesNothingWhileSystemdDoesNotAnswer(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasLost()
	box.own(run, osrun.StateRunning)
	box.setUnit("")

	tick := box.tick()
	if got := box.attemptsOf(run); len(got) != 1 || len(box.startedUnits()) != 0 || tick.Action != osrun.DoNone {
		t.Errorf("%d attempts and %d units while systemd did not answer, and the look was to do %q", len(got), len(box.startedUnits()), tick.Action)
	}
}

// One thing is done at a look, the one that the reducer says.
func TestTheSettlingDoesOneThingAtALook(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasLost()
	box.own(run, osrun.StateRunning)

	tick := box.tick()
	if tick.Action != osrun.DoUnwind || len(tick.Done) != 1 {
		t.Errorf("the look did %v (%q)", tick.Done, tick.Action)
	}
}

// The owner took the run over: the core never touches it again, whatever the log says.
func TestTheSettlingDoesNothingAfterTheOwnerTookOver(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasLost()
	box.own(run, osrun.StateRunning)
	box.say(run, osrun.Marker{Kind: osrun.MkTakeover})

	box.tick()
	if got := box.attemptsOf(run); len(got) != 1 || len(box.startedUnits()) != 0 {
		t.Errorf("%d attempts and %d units after the takeover", len(got), len(box.startedUnits()))
	}
}

// A run that was closed is not looked at: its pointer is gone, and so is its flag.
func TestTheSettlingDoesNotLookAtARunThatWasClosed(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasLost()
	box.say(run, osrun.Marker{Kind: osrun.MkAck, Result: "acknowledge"})
	if err := setOSUpgradeBusy(run.ID, osrun.StateRunning, box.now()); err != nil {
		t.Fatal(err)
	}
	box.tick()
	if got := box.attemptsOf(run); len(got) != 1 || box.flagIsThere() {
		t.Errorf("%d attempts, flag %v", len(got), box.flagIsThere())
	}
}

const (
	noRoute      = "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n"
	defaultRoute = noRoute + "enp3s0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n"
)

// The machine that restarted is checked when it is ready, or when the time it was given is over, whichever comes first.
func TestTheSettlingChecksARestartedMachineWhenItIsReady(t *testing.T) {
	box := newUpgradeBox(t)
	route := filepath.Join(t.TempDir(), "route")
	was := osUpgradeProcNetRoute
	osUpgradeProcNetRoute = route
	t.Cleanup(func() { osUpgradeProcNetRoute = was })
	if err := os.WriteFile(route, []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := box.aHopThatRestarted()
	box.theFactsBefore(run, osverify.Facts{Taken: true, At: box.now(), Route: "enp3s0", Docker: osverify.Docker{State: osverify.DockerUp}, DpkgQuiet: true})
	box.point(run)

	// the machine is starting: nothing starts the check
	box.setSystem("starting")
	box.tick()
	if got := box.attemptsOf(run); len(got) != 1 {
		t.Fatalf("the check started while the system was starting: %+v", got)
	}

	// the system runs and the network is back, and Docker is not: Docker ran before, and is waited for
	box.setSystem("running")
	if err := os.WriteFile(route, []byte(defaultRoute), 0o644); err != nil {
		t.Fatal(err)
	}
	box.tick()
	if got := box.attemptsOf(run); len(got) != 1 {
		t.Fatalf("the check started with no Docker: %+v", got)
	}

	// Docker is back and the network is not: there was a route before, and it is waited for
	box.setActive("docker.service", "active")
	if err := os.WriteFile(route, []byte(noRoute), 0o644); err != nil {
		t.Fatal(err)
	}
	box.tick()
	if got := box.attemptsOf(run); len(got) != 1 {
		t.Fatalf("the check started with no route: %+v", got)
	}

	// and now both
	if err := os.WriteFile(route, []byte(defaultRoute), 0o644); err != nil {
		t.Fatal(err)
	}
	tick := box.tick()
	attempts := box.attemptsOf(run)
	if len(attempts) != 2 || attempts[1].Phase != osrun.PhaseVerify || attempts[1].Who != osrun.WhoAuto || tick.Action != osrun.DoLaunchVerify {
		t.Errorf("the attempts are %+v, the look did %q", attempts, tick.Action)
	}
}

// What the machine did not have before the hop it is not waited for: a box with no Docker and no network is ready when systemd says it is.
func TestTheSettlingDoesNotWaitForWhatTheMachineDidNotHave(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatRestarted()
	box.theFactsBefore(run, osverify.Facts{Taken: true, At: box.now(), DpkgQuiet: true})
	box.point(run)
	box.setSystem("degraded")

	box.tick()
	if got := box.attemptsOf(run); len(got) != 2 || got[1].Phase != osrun.PhaseVerify {
		t.Errorf("the attempts are %+v", got)
	}
}

// A machine that is never ready is checked all the same when the time is over: the check says what is wrong.
func TestTheSettlingChecksAMachineThatIsNeverReadyWhenTheTimeIsOver(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatRestarted()
	box.point(run)
	box.setSystem("starting")

	box.tick() // notes the boot
	box.advance(osUpgradeTiming.ResumeWait - time.Minute)
	box.tick()
	if got := box.attemptsOf(run); len(got) != 1 {
		t.Fatalf("the check started before the time was over: %+v", got)
	}
	box.advance(2 * time.Minute)
	box.tick()
	if got := box.attemptsOf(run); len(got) != 2 || got[1].Phase != osrun.PhaseVerify {
		t.Errorf("the attempts are %+v", got)
	}
}

// A rehearsal is put back when its time is over, and not before.
func TestTheSettlingUnwindsARehearsalThatLivedPastItsTime(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRehearsal()
	box.own(run, osrun.StateRehearsed)

	box.advance(osUpgradeTiming.RehearsalTTL - time.Minute)
	box.tick()
	if got := box.attemptsOf(run); len(got) != 1 {
		t.Fatalf("the rehearsal was put back before its time: %+v", got)
	}
	box.advance(2 * time.Minute)
	box.tick()
	if got := box.attemptsOf(run); len(got) != 2 || got[1].Phase != osrun.PhaseUnwind || got[1].Who != osrun.WhoAuto {
		t.Errorf("the attempts are %+v: a rehearsal lives for its time and no longer", got)
	}
}

// A machine that restarted while it held the sources of the next release is put back at once: the timers are back with the boot, and the sources are not the machine's.
func TestTheSettlingUnwindsARehearsalThatWasRestarted(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRehearsal()
	box.own(run, osrun.StateRehearsed)
	box.setBoot(boxBoot2)

	box.tick()
	if got := box.attemptsOf(run); len(got) != 2 || got[1].Phase != osrun.PhaseUnwind {
		t.Errorf("the attempts are %+v", got)
	}
}

// ---- a hop that stopped after the point of no return -----------------------------------------------------------------------------

// The shipped setting never repairs a half upgraded machine by itself: the owner's click is the same repair.
func TestTheShippedSettingNeverRepairsByItself(t *testing.T) {
	if osUpgradeTiming.AutoRecoverTries != 0 {
		t.Fatalf("the core would repair a machine that is half upgraded by itself, %d times", osUpgradeTiming.AutoRecoverTries)
	}
	box := newUpgradeBox(t)
	run := box.aHopThatStoppedHalfWay(osrun.ClassBusy)
	box.own(run, osrun.StateStoppedAtWaypoint)
	box.advance(time.Hour)

	box.tick()
	if got := box.attemptsOf(run); len(got) != 1 || len(box.startedUnits()) != 0 {
		t.Errorf("%d attempts and %d units: a machine that is half upgraded was repaired without a word", len(got), len(box.startedUnits()))
	}
}

// The code of the automatic repair is there, behind the zero: it waits for the first wave, and for the time after the stop.
func TestTheSettlingRepairsWhatTheWaveAllowsWhenItIsSwitchedOn(t *testing.T) {
	withTiming(t, func(tm *osrun.Timing) { tm.AutoRecoverTries = 2 })
	box := newUpgradeBox(t)
	run := box.aHopThatStoppedHalfWay(osrun.ClassBusy)
	box.own(run, osrun.StateStoppedAtWaypoint)

	box.tick() // a minute after the stop: the first wait is two
	if got := box.attemptsOf(run); len(got) != 1 {
		t.Fatalf("the repair came before its time: %+v", got)
	}
	box.advance(2 * time.Minute)
	tick := box.tick()
	attempts := box.attemptsOf(run)
	if len(attempts) != 2 || attempts[1].Phase != osrun.PhaseRecover || attempts[1].Who != osrun.WhoAuto || attempts[1].From != osrun.BlockFull || tick.Action != osrun.DoLaunchRecover {
		t.Errorf("the attempts are %+v, the look did %q", attempts, tick.Action)
	}
	if got := readText(t, filepath.Join(run.Dir, "lists", "2.list")); got != "forward\nfull\nprereboot\n" {
		t.Errorf("the list of the repair is %q", got)
	}
}

// A stop that says the repair itself is not to be trusted is never repaired by the core, whatever the number of tries.
func TestTheSettlingNeverRepairsAStopOfTheGuard(t *testing.T) {
	withTiming(t, func(tm *osrun.Timing) { tm.AutoRecoverTries = 2; tm.AutoRecoverWave = 2 })
	box := newUpgradeBox(t)
	run := box.aHopThatStoppedHalfWay(osrun.ClassGuard)
	box.own(run, osrun.StateStoppedAtWaypoint)
	box.advance(time.Hour)

	box.tick()
	if got := box.attemptsOf(run); len(got) != 1 {
		t.Errorf("the guard's stop was repaired by the core: %+v", got)
	}
}

// ---- the boot that the log does not know -------------------------------------------------------------------------------------------

var aptUnits = []string{"apt-daily.timer", "apt-daily-upgrade.timer", "apt-daily.service", "apt-daily-upgrade.service", "unattended-upgrades.service"}

// A boot that the log has not seen, while the run holds the machine, is written down once, and the five units of apt that came back with the boot are stopped: the timers are the
// machine's own, and the run stopped them for itself.
func TestTheSettlingNotesANewBootOnceAndStopsTheAptUnits(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatRestarted()
	box.point(run)
	box.setSystem("starting")
	for _, unit := range aptUnits[:3] {
		box.setActive(unit, "active")
	}

	box.tick()
	seen := markersOfKind(box.read(run.ID), osrun.MkBootSeen)
	if len(seen) != 1 || seen[0].Boot != boxBoot2 || seen[0].N != 0 {
		t.Fatalf("the log says %+v", seen)
	}
	stops := box.commandsWith("systemctl stop")
	if len(stops) != 3 {
		t.Fatalf("the units that were stopped: %q, want the three that were active", stops)
	}
	for i, unit := range aptUnits[:3] {
		if stops[i] != "systemctl stop "+unit {
			t.Errorf("stop %d is %q", i, stops[i])
		}
	}

	// and not again, whatever has come back since
	box.setActive(aptUnits[4], "active")
	box.advance(30 * time.Second)
	box.tick()
	if got := markersOfKind(box.read(run.ID), osrun.MkBootSeen); len(got) != 1 {
		t.Errorf("the boot was written down %d times", len(got))
	}
	if got := box.commandsWith("systemctl stop"); len(got) != 3 {
		t.Errorf("the units were stopped again: %q", got)
	}
}

// A machine that the run does not hold (it ended well) has its timers back, and the settling leaves them: they are the owner's again.
func TestTheSettlingLeavesTheTimersOfAMachineThatTheRunLetGoOf(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatRestarted()
	run = box.attempt(run, 2, osrun.PhaseVerify, osrun.WhoAuto)
	run = box.leave(run, "done-after")
	run = box.say(run, osrun.Marker{Kind: osrun.MkVerifyDone, N: 2, Boot: boxBoot2})
	run = box.end(run, 2, "ok")
	if err := writeOSUpgradeVerify(box.read(run.ID), osUpgradeVerifyDoc{At: box.now(), Findings: []osverify.Finding{}}); err != nil {
		t.Fatal(err)
	}
	box.point(run)
	box.setActive("apt-daily.timer", "active")
	box.setBoot("99999999-7777-4888-9999-aaaaaaaaaaaa")

	tick := box.tick()
	if tick.State != osrun.StateSucceeded {
		t.Fatalf("the run is %q", tick.State)
	}
	if got := box.commandsWith("systemctl stop"); len(got) != 0 || len(markersOfKind(box.read(run.ID), osrun.MkBootSeen)) != 0 {
		t.Errorf("the timers of a machine that is the owner's again were stopped: %q", got)
	}
}

// Looks that come together write a boot down once.
func TestLooksThatComeTogetherNoteABootOnce(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatRestarted()
	box.point(run)
	box.setSystem("starting")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			box.tick()
		}()
	}
	wg.Wait()
	if got := markersOfKind(box.read(run.ID), osrun.MkBootSeen); len(got) != 1 {
		t.Errorf("the boot was written down %d times", len(got))
	}
}

// A boot id that cannot be read is no boot that anyone has seen.
func TestTheSettlingNotesNoBootItCannotRead(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatRestarted()
	box.point(run)
	box.setBoot("")
	box.setActive(aptUnits[0], "active")

	box.tick()
	if got := markersOfKind(box.read(run.ID), osrun.MkBootSeen); len(got) != 0 || len(box.commandsWith("systemctl stop")) != 0 {
		t.Errorf("a boot that could not be read was noted: %+v", got)
	}
}

// ---- the refresh -------------------------------------------------------------------------------------------------------------------

// A refresh that ended says so for the plans that follow: a plan is of the machine as it was, and the settling writes down when the old release was last brought up to date.
func TestTheSettlingWritesDownWhenTheRefreshEnded(t *testing.T) {
	box := newUpgradeBox(t)
	refresh := box.aRefresh()
	box.point(refresh)

	box.tick()
	got := strings.TrimSpace(readText(t, filepath.Join(box.root, "last-refresh")))
	want := refresh.Markers[len(refresh.Markers)-2].At.UTC().Format(time.RFC3339)
	if got != want {
		t.Errorf("last-refresh is %q, want the time of the end of the refresh, %q", got, want)
	}

	// and an older one is not written over a newer one
	later := box.now().Add(time.Hour).UTC().Format(time.RFC3339)
	box.putInRoot("last-refresh", later+"\n")
	box.tick()
	if got := strings.TrimSpace(readText(t, filepath.Join(box.root, "last-refresh"))); got != later {
		t.Errorf("last-refresh is %q: an older refresh was written over a newer one", got)
	}
}

// ---- the loop ----------------------------------------------------------------------------------------------------------------------

// The core looks at the upgrade at its start and then every so often, on its own, and stops when it is told to.
func TestTheSettlingRunsOnItsOwnAndStops(t *testing.T) {
	withTiming(t, func(tm *osrun.Timing) { tm.SettleEvery = 5 * time.Millisecond })
	box := newUpgradeBox(t)
	run := box.aHopThatWasLost()
	box.own(run, osrun.StateRunning)

	box.StartSystemOSUpgradeSettle(t.Context())
	box.StartSystemOSUpgradeSettle(t.Context()) // a second start is no second loop
	deadline := time.Now().Add(5 * time.Second)
	for len(box.attemptsOf(run)) < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := box.attemptsOf(run); len(got) != 2 || got[1].Phase != osrun.PhaseUnwind {
		t.Fatalf("the loop did not unwind the run: %+v", got)
	}
	box.StopSystemOSUpgradeSettle(5 * time.Second)

	// once stopped it looks no more: the flag is put back by nothing
	if err := clearOSUpgradeBusy(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if box.flagIsThere() {
		t.Error("the loop went on after it was stopped")
	}
}

// A look that panics does not end the loop: the core looks again.
func TestALookThatPanicsDoesNotEndTheLoop(t *testing.T) {
	withTiming(t, func(tm *osrun.Timing) { tm.SettleEvery = 5 * time.Millisecond })
	box := newUpgradeBox(t)
	var looks atomic.Int32
	was := osUpgradeBootID
	osUpgradeBootID = func() (string, bool) {
		if looks.Add(1) == 1 {
			panic("a look that goes wrong")
		}
		return boxBoot1, true
	}
	t.Cleanup(func() { osUpgradeBootID = was })
	run := box.aHopThatWasLost()
	box.own(run, osrun.StateRunning)

	box.StartSystemOSUpgradeSettle(t.Context())
	deadline := time.Now().Add(5 * time.Second)
	for looks.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	box.StopSystemOSUpgradeSettle(5 * time.Second)
	if looks.Load() < 3 {
		t.Fatalf("the loop looked %d times", looks.Load())
	}
}
