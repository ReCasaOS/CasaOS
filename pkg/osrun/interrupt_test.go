package osrun

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// The cells below are the ways a run can be interrupted, and what the core does of each: a row for every cell of the table of
// events (the core restarts, the unit is killed, the power is cut, a lock is held, the network is down, the disk is full) against the phases (before the point of no return,
// after it, waiting for the restart, restarting, checking), and for every step of a hop at which the power can go.

var (
	unwindAuto  = Action{Kind: DoUnwind, Phase: PhaseUnwind, Who: WhoAuto}
	verifyAuto  = Action{Kind: DoLaunchVerify, Phase: PhaseVerify, Who: WhoAuto}
	alertOnly   = Action{Kind: DoAlert}
	nothingToDo = Action{Kind: DoNone}
)

// Where the unit was, as the table has the phases of a run.
const (
	beforePNR = "before the point of no return"
	afterPNR  = "after the point of no return"
	waiting   = "waiting for the restart"
	checking  = "the check"
)

// scenario is a hop in one of the four places of the table, with its unit lost and the machine in the boot the unit began in.
func scenario(t *testing.T, where string) *runLog {
	switch where {
	case beforePNR:
		return hopInBlock(t, BlockDownload)
	case afterPNR:
		return hopInBlock(t, BlockFull)
	case waiting:
		return hopAtReboot(t)
	case checking:
		return hopRestarted(t).blockStart(2, 1, BlockAfter)
	}
	t.Fatalf("no such place: %s", where)
	return nil
}

// lostAfter is the input a moment after the unit was lost, on the boot of the machine's choice.
func lostAfter(l *runLog, boot string) Input {
	in := afterLast(l.input(), time.Minute)
	in.Boot.Now = boot
	return in
}

func TestWhenTheCoreRestartsTheNewCoreReadsTheSameFiles(t *testing.T) {
	for _, where := range []string{beforePNR, afterPNR, waiting, checking} {
		for _, unitIsThere := range []bool{true, false} {
			l := scenario(t, where)
			in := l.input()
			in.Unit.Active = unitIsThere
			if where == checking {
				in.Boot.Now = bootB
			}
			s := reduced(t, in)
			a := NextAction(s, in)
			// The new core has the same bytes, read again, and a clock that has not moved.
			again := l.clone().input()
			again.Unit, again.Boot, again.Now = in.Unit, in.Boot, in.Now
			s2 := reduced(t, again)
			if !reflect.DeepEqual(s, s2) || NextAction(s2, again) != a {
				t.Errorf("%s, unit %v: %+v then %+v", where, unitIsThere, s, s2)
			}
		}
	}
}

func TestWhenTheUnitIsKilledOnTheSameBoot(t *testing.T) {
	// Before the point of no return: undone, and the run has failed and can be tried again.
	l := scenario(t, beforePNR)
	s, a := next(t, lostAfter(l, bootA))
	is(t, s, StateInterrupted, CauseKilled)
	wantAction(t, a, unwindAuto)
	l.wait(time.Minute).attempt(2, PhaseUnwind, "", WhoAuto).start(2, PhaseUnwind, bootA).unwound(2, "ok").end(2, "ok")
	s = reduced(t, l.input())
	is(t, s, StateFailed, ReasonInterruptedKilled)
	if s.Busy || !s.Reversible {
		t.Errorf("after the unwind: %+v", s)
	}

	// After it: the shipped timing waits for the owner, who asks for the repair; a wave of repairs by itself would not before
	// the second wave, since a unit that was lost has no class to be trusted with.
	l = scenario(t, afterPNR)
	in := lostAfter(l, bootA)
	s, a = next(t, in)
	is(t, s, StateInterrupted, CauseKilled)
	if !s.Busy || !s.Resumable || !s.PNR {
		t.Errorf("%+v", s)
	}
	wantAction(t, a, alertOnly)
	in.Timing = repairing(2, 1)
	_, a = next(t, in)
	wantAction(t, a, alertOnly)
	in.Timing = repairing(2, 2)
	_, a = next(t, in)
	wantAction(t, a, nothingToDo)
	_, a = next(t, afterLast(in, 2*time.Minute))
	wantAction(t, a, Action{Kind: DoLaunchRecover, Phase: PhaseRecover, From: BlockFull, Who: WhoAuto})
	// The owner's resume is the same phase, asked for by the owner, and the run is recovering.
	l.wait(time.Minute).attempt(2, PhaseRecover, BlockFull, WhoOwner)
	s = reduced(t, afterLast(l.input(), time.Second))
	is(t, s, StateRecovering, "")

	// Waiting for the restart: there is no unit to lose.
	s, a = next(t, scenario(t, waiting).input())
	is(t, s, StateAwaitingReboot, "")
	wantAction(t, a, Action{Kind: DoBarrier})

	// The check: run again.
	s, a = next(t, lostAfter(scenario(t, checking), bootB))
	is(t, s, StateInterrupted, CauseKilled)
	wantAction(t, a, verifyAuto)
}

func TestWhenThePowerIsCutOrTheMachineIsReset(t *testing.T) {
	// Before the point of no return: the unit that unwinds begins in the new boot, and the run says why it failed.
	l := scenario(t, beforePNR)
	s, a := next(t, lostAfter(l, bootB))
	is(t, s, StateInterrupted, CausePower)
	wantAction(t, a, unwindAuto)
	l.wait(time.Minute).attempt(2, PhaseUnwind, "", WhoAuto).start(2, PhaseUnwind, bootB).unwound(2, "ok").end(2, "ok")
	in := l.input()
	in.Boot.Now = bootB
	is(t, reduced(t, in), StateFailed, ReasonInterruptedPower)

	// After it: it waits for the owner, who will run the repair block first.
	l = scenario(t, afterPNR)
	s, a = next(t, lostAfter(l, bootB))
	is(t, s, StateInterrupted, CausePower)
	wantAction(t, a, alertOnly)
	if got := strings.Join(BlockList(PhaseRecover, l.files), " "); got != "forward full prereboot" {
		t.Errorf("the repair runs %q", got)
	}

	// Waiting for the restart: a different boot is the restart, whoever made it.
	in = scenario(t, waiting).input()
	in.Boot.Now = bootB
	s, a = next(t, in)
	is(t, s, StateResuming, "")
	wantAction(t, a, nothingToDo)
	in.Ready = true
	_, a = next(t, in)
	wantAction(t, a, verifyAuto)

	// The check: block 8 is repeatable, and runs again.
	in = lostAfter(scenario(t, checking), bootC)
	s, a = next(t, in)
	is(t, s, StateInterrupted, CausePower)
	wantAction(t, a, verifyAuto)
}

// stopWith is a stop of a block of the class, in the place of the table.
func stopWith(t *testing.T, where, class string) *runLog {
	switch where {
	case beforePNR:
		l := hopInBlock(t, BlockDownload)
		return l.stop(1, 5, BlockDownload, 100, class, false).unwound(1, "ok").end(1, "stopped")
	case afterPNR:
		l := hopInBlock(t, BlockFull)
		return l.stop(1, 7, BlockFull, 100, class, true).end(1, "stopped")
	}
	t.Fatalf("no stop in %s", where)
	return nil
}

func TestWhenAnAptOrDpkgLockIsHeld(t *testing.T) {
	// Before: the update fails at once, the block says busy, and the run is undone.
	s, a := next(t, stopWith(t, beforePNR, ClassBusy).input())
	is(t, s, StateFailed, ClassBusy)
	if s.Busy || !s.Reversible {
		t.Errorf("%+v", s)
	}
	wantAction(t, a, nothingToDo)
	// After: the stop waits at the waypoint, and the owner resumes it later.
	in := afterLast(stopWith(t, afterPNR, ClassBusy).input(), time.Hour)
	s, a = next(t, in)
	is(t, s, StateStoppedAtWaypoint, "")
	if !s.Resumable || s.Class != ClassBusy {
		t.Errorf("%+v", s)
	}
	wantAction(t, a, alertOnly)
	// ... or the first wave of repairs does, after its backoff.
	in.Timing = repairing(2, 1)
	_, a = next(t, in)
	wantAction(t, a, Action{Kind: DoLaunchRecover, Phase: PhaseRecover, From: BlockFull, Who: WhoAuto})
}

func TestWhenTheNetworkIsDown(t *testing.T) {
	s, _ := next(t, stopWith(t, beforePNR, ClassNetwork).input())
	is(t, s, StateFailed, ClassNetwork)
	if s.Busy || !s.Reversible {
		t.Errorf("%+v", s)
	}
	// After the point of no return the machine is a mix of two releases: it is resumed when the network is back.
	in := afterLast(stopWith(t, afterPNR, ClassNetwork).input(), 3*time.Minute)
	s, a := next(t, in)
	is(t, s, StateStoppedAtWaypoint, "")
	if !s.Resumable || !s.HalfUpgraded {
		t.Errorf("%+v", s)
	}
	wantAction(t, a, alertOnly)
	in.Timing = repairing(2, 1)
	_, a = next(t, in)
	wantAction(t, a, Action{Kind: DoLaunchRecover, Phase: PhaseRecover, From: BlockFull, Who: WhoAuto})
	// The check does not stop for it: a finding is the judge's, not a state of the run.
}

func TestWhenTheDiskIsFull(t *testing.T) {
	// Before: the floors of the first block refuse, or a block fails at run time. Either way it is undone.
	l := hopInBlock(t, BlockVerify)
	l.stop(1, 1, BlockVerify, 1, ClassDisk, false).unwound(1, "ok").end(1, "stopped")
	s, _ := next(t, l.input())
	is(t, s, StateFailed, ClassDisk)
	s, _ = next(t, stopWith(t, beforePNR, ClassDisk).input())
	is(t, s, StateFailed, ClassDisk)
	// After: room is made and the owner resumes; nothing is ever repaired by itself for it.
	in := afterLast(stopWith(t, afterPNR, ClassDisk).input(), 24*time.Hour)
	in.Timing = repairing(5, 2)
	s, a := next(t, in)
	is(t, s, StateStoppedAtWaypoint, "")
	if !s.Resumable {
		t.Errorf("%+v", s)
	}
	wantAction(t, a, alertOnly)
}

// A power cut at each step of a hop. The machine comes back in another boot, so each of these is a unit that was lost with the
// boot changed, and each has the files its step leaves.
func TestAPowerCutAtEachStepOfAHop(t *testing.T) {
	// hop is the unit of the hop lost in a block, with the files the steps before it left and these on top.
	hop := func(id string, files Files) *runLog {
		l := hopInBlock(t, id)
		l.files = Files{DoneTimers: l.files.DoneTimers, AptOrig: l.files.AptOrig, DoneMinimal: l.files.DoneMinimal, DoneFull: l.files.DoneFull}
		if files.AptOrig {
			l.files.AptOrig = true
		}
		l.files.Begun = files.Begun
		l.files.DoneMinimal = files.DoneMinimal
		l.files.DoneFull = files.DoneFull
		return l
	}
	undone := func(t *testing.T, l *runLog) {
		t.Helper()
		l.wait(time.Minute).attempt(2, PhaseUnwind, "", WhoAuto).start(2, PhaseUnwind, bootB).unwound(2, "ok").end(2, "ok")
		in := l.input()
		in.Boot.Now = bootB
		s := reduced(t, in)
		is(t, s, StateFailed, ReasonInterruptedPower)
		if s.Busy || !s.Reversible {
			t.Errorf("after the unwind: %+v", s)
		}
	}
	for _, c := range []struct {
		step  string
		log   func() *runLog
		undo  bool
		list  string
		state string
	}{
		{"block 1, the timers", func() *runLog { return hop(BlockTimers, Files{}) }, true, "", StateInterrupted},
		{"block 0", func() *runLog { return hop(BlockVerify, Files{}) }, true, "", StateInterrupted},
		{"block 2, the snapshot", func() *runLog { return hop(BlockSnapshot, Files{}) }, true, "", StateInterrupted},
		{"block 3, the sources", func() *runLog { return hop(BlockSources, Files{AptOrig: true}) }, true, "", StateInterrupted},
		{"block 4, the download", func() *runLog { return hop(BlockDownload, Files{AptOrig: true}) }, true, "", StateInterrupted},
		{"block 5, before its marker", func() *runLog { return hop(BlockMinimal, Files{AptOrig: true}) }, true, "", StateInterrupted},
		{"block 5, the marker written and nothing changed", func() *runLog { return hop(BlockMinimal, Files{AptOrig: true, Begun: true}) }, false,
			"forward minimal full prereboot", StateInterrupted},
		{"block 5, in dpkg", func() *runLog { return hop(BlockMinimal, Files{AptOrig: true, Begun: true}) }, false,
			"forward minimal full prereboot", StateInterrupted},
		{"block 6, in dpkg", func() *runLog { return hop(BlockFull, Files{AptOrig: true, Begun: true, DoneMinimal: true}) }, false,
			"forward full prereboot", StateInterrupted},
		{"block 6, converged and not written", func() *runLog { return hop(BlockFull, Files{AptOrig: true, Begun: true, DoneMinimal: true}) }, false,
			"forward full prereboot", StateInterrupted},
		{"after block 6, before block 7", func() *runLog {
			return hop(BlockPrereboot, Files{AptOrig: true, Begun: true, DoneMinimal: true, DoneFull: true})
		}, false,
			"prereboot", StateInterrupted},
	} {
		l := c.log()
		in := lostAfter(l, bootB)
		s, a := next(t, in)
		if s.Name != c.state || s.Cause != CausePower {
			t.Errorf("%s: %+v", c.step, s)
			continue
		}
		if c.undo {
			if s.PNR || s.Resumable {
				t.Errorf("%s: %+v", c.step, s)
			}
			wantAction(t, a, unwindAuto)
			undone(t, l)
			continue
		}
		if !s.PNR || !s.Resumable || !s.HalfUpgraded {
			t.Errorf("%s: %+v", c.step, s)
		}
		wantAction(t, a, alertOnly)
		if got := strings.Join(BlockList(PhaseRecover, in.Files), " "); got != c.list {
			t.Errorf("%s: the repair runs %q, want %q", c.step, got, c.list)
		}
	}
}

func TestAPowerCutInARefresh(t *testing.T) {
	for _, id := range []string{BlockRCheck, BlockRSources, BlockRUpgrade, BlockRDone} {
		l := newLog(t, RunRefresh).attempt(1, PhaseRefresh, "", WhoAuto).start(1, PhaseRefresh, bootA)
		for i, b := range BlockList(PhaseRefresh, Files{}) {
			l.blockStart(1, i+1, b)
			if b == id {
				break
			}
			l.blockDone(1, i+1, b)
		}
		s, a := next(t, lostAfter(l, bootB))
		is(t, s, StateInterrupted, CausePower)
		if s.PNR || s.Resumable {
			t.Errorf("%s: %+v", id, s)
		}
		wantAction(t, a, unwindAuto)
		l.wait(time.Minute).attempt(2, PhaseUnwind, "", WhoAuto).start(2, PhaseUnwind, bootB).unwound(2, "ok").end(2, "ok")
		in := l.input()
		in.Boot.Now = bootB
		is(t, reduced(t, in), StateFailed, ReasonInterruptedPower)
	}
}

func TestAPowerCutAfterTheLastBlockOfTheHopHasWhatTheFilesSay(t *testing.T) {
	// Block 7 ended and the unit did not write that the machine is to restart: the files have it, on the boot that wrote them...
	l := hopLaunched(t)
	for i, b := range BlockList(PhaseRun, Files{}) {
		l.blockStart(1, i+1, b)
		l.blockFiles(b, true)
		l.blockDone(1, i+1, b)
	}
	in := lostAfter(l, bootA)
	s, a := next(t, in)
	is(t, s, StateAwaitingReboot, "")
	wantAction(t, a, Action{Kind: DoBarrier})
	// ... and on another one it is the restart.
	in.Boot.Now = bootB
	s, a = next(t, in)
	is(t, s, StateResuming, "")
	wantAction(t, a, nothingToDo)
}

func TestAShutdownThatIsUnderWay(t *testing.T) {
	// The restart was asked for, the unit is gone, and the machine is on its way down: the boot is the old one until it is not.
	l := hopAtReboot(t).add(Marker{Kind: MkRebootRequested})
	in := afterLast(l.input(), 30*time.Second)
	s, a := next(t, in)
	is(t, s, StateRebooting, "")
	wantAction(t, a, nothingToDo)
	// Back, in another boot, and the check is waited for until the machine is ready.
	in.Boot.Now = bootB
	in.BootSeenAt = in.Now
	s, a = next(t, in)
	is(t, s, StateResuming, "")
	wantAction(t, a, nothingToDo)
	in.Ready = true
	_, a = next(t, in)
	wantAction(t, a, verifyAuto)
}

func TestAPowerCutInTheCheckRunsItAgain(t *testing.T) {
	// Block 8 did not end: the file is not there and the unit said nothing.
	in := lostAfter(scenario(t, checking), bootC)
	s, a := next(t, in)
	is(t, s, StateInterrupted, CausePower)
	wantAction(t, a, verifyAuto)
	// Block 8 ended and the unit was lost before it said so: the file wins, and the judge is next.
	l := scenario(t, checking)
	l.files.DoneAfter = true
	in = lostAfter(l, bootC)
	s, a = next(t, in)
	is(t, s, StateVerifying, "")
	wantAction(t, a, Action{Kind: DoJudge})
}
