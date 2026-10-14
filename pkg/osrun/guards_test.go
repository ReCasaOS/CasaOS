package osrun

import (
	"testing"
	"time"
)

// A word that the unit could not have written yet, or any more, or twice, is not the run's: each of the guards of the reducer that
// say so has a test here that fails when the guard is taken away.

func TestBlockWordsPastTheCapDoNotMoveTheBlock(t *testing.T) {
	l := hopInBlock(t, BlockDownload)
	// More words of a block that is already there than the unit can have written, and then the word of the minimal upgrade: past the
	// cap it is not heard, so the run is still in the download and can still be called off.
	for i := 0; i < 5000; i++ {
		l.blockStart(1, 5, BlockDownload)
	}
	l.blockStart(1, 6, BlockMinimal)
	s := reduced(t, running(l.input()))
	is(t, s, StateRunning, "")
	if s.Block.ID != BlockDownload || !s.Abortable {
		t.Errorf("a flood of words moved the block: %+v", s)
	}
	// Within the cap the same word is heard.
	l = hopInBlock(t, BlockDownload)
	l.blockStart(1, 6, BlockMinimal)
	if s := reduced(t, running(l.input())); s.Block.ID != BlockMinimal || s.Abortable {
		t.Errorf("%+v", s)
	}
}

func TestOnlyTheFirstStopOfAnAttemptCounts(t *testing.T) {
	l := hopInBlock(t, BlockFull).stop(1, 7, BlockFull, 3, ClassNetwork, true)
	l.stop(1, 7, BlockFull, 9, ClassDisk, true).end(1, "stopped")
	s := reduced(t, quiet(l.input(), time.Minute))
	is(t, s, StateStoppedAtWaypoint, "")
	if s.Class != ClassNetwork || s.RC != 3 {
		t.Errorf("a second stop moved the first: %+v", s)
	}
	// ... which is also the one that decides before the point of no return
	before := hopInBlock(t, BlockDownload).stop(1, 5, BlockDownload, 3, ClassNetwork, false)
	before.stop(1, 5, BlockDownload, 9, ClassDisk, false).unwound(1, "ok").end(1, "stopped")
	s = reduced(t, quiet(before.input(), time.Minute))
	is(t, s, StateFailed, ClassNetwork)
	if s.Class != ClassNetwork {
		t.Errorf("%+v", s)
	}
}

func TestAResultAfterAStopIsNotTheUnitsResult(t *testing.T) {
	// A hop whose unit stopped past the point of no return, and then said that it was done up to the restart, which it was not.
	l := hopInBlock(t, BlockFull).stop(1, 7, BlockFull, 1, ClassNetwork, true)
	l.add(Marker{Kind: MkRebootPending, N: 1, Boot: bootA, Kernel: "6.1.0-37-amd64"}).end(1, "stopped")
	is(t, reduced(t, quiet(l.input(), time.Minute)), StateStoppedAtWaypoint, "")
	// The repair that the owner asks for ends with no word and with no file of the last block: the word of the unit before it is not
	// one to stand on, and the run says that it cannot be told.
	l.attempt(2, PhaseRecover, BlockFull, WhoOwner).start(2, PhaseRecover, bootA).blockStart(2, 1, BlockForward).blockDone(2, 1, BlockForward).end(2, "ok")
	s := reduced(t, quiet(l.input(), time.Minute))
	is(t, s, StateNeedsAttention, ReasonRunDamaged)
	// A rehearsal that stopped is not made a finished one by a word after its stop.
	r := newLog(t, RunRehearsal).attempt(1, PhaseRehearse, "", WhoAuto).start(1, PhaseRehearse, bootA)
	r.blockStart(1, 1, BlockVerify).stop(1, 1, BlockVerify, 1, ClassBusy, false)
	r.add(Marker{Kind: MkRehearsalReady, N: 1}).unwound(1, "ok").end(1, "stopped")
	is(t, reduced(t, quiet(r.input(), time.Minute)), StateFailed, ClassBusy)
}

func TestAnUnwoundThatTheStopDidNotMakeLeavesTheRunAsItWas(t *testing.T) {
	// The unit says its stop was past the point of no return, and says it undid it: it did not, and the log is not believed.
	l := hopInBlock(t, BlockDownload).stop(1, 5, BlockDownload, 1, ClassNetwork, true).unwound(1, "ok").end(1, "stopped")
	in := quiet(l.input(), time.Minute)
	s := reduced(t, in)
	is(t, s, StateInterrupted, CauseKilled)
	if act := NextAction(s, in); act.Kind != DoUnwind {
		t.Errorf("%+v", act)
	}
}

func TestAUnitThatBeginsAnotherPhaseThanItWasLaunchedForIsNotTheAttempts(t *testing.T) {
	l := newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto)
	l.add(Marker{Kind: MkUnitStart, N: 1, Phase: PhaseVerify, Boot: bootA, PID: 4})
	// Thirty seconds on, with no unit, the launch is the failure it was.
	s := reduced(t, quiet(l.input(), time.Minute))
	is(t, s, StateFailed, ReasonStart)
}

func TestARehearsalIsUndoneAtTheSecondItsTimeIsUp(t *testing.T) {
	l := rehearsalReady(t)
	var ready time.Time
	for _, m := range l.input().Markers {
		if m.Kind == MkRehearsalReady {
			ready = m.At
		}
	}
	unwind := Action{Kind: DoUnwind, Phase: PhaseUnwind, Who: WhoAuto}
	in := l.input()
	for d, want := range map[time.Duration]Action{
		DefaultTiming.RehearsalTTL - time.Nanosecond: {Kind: DoNone},
		DefaultTiming.RehearsalTTL:                   unwind,
		DefaultTiming.RehearsalTTL + time.Nanosecond: unwind,
	} {
		in.Now = ready.Add(d)
		s := reduced(t, in)
		if got := NextAction(s, in); got != want {
			t.Errorf("%v after it was ready: %+v, want %+v", d, got, want)
		}
	}
	if s := reduced(t, in); s.ExpiresAt == nil || !s.ExpiresAt.Equal(ready.Add(DefaultTiming.RehearsalTTL)) {
		t.Errorf("%+v", s)
	}
}
