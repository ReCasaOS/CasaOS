package osrun

import (
	"testing"
	"time"
)

// The core writes START_FAILED when systemd-run did not say the unit was started. A unit that exists after all speaks for
// itself: what systemd says of it and the unit's own words outrank the core's doubt.

func TestAUnitThatRunsIsRunningWhateverTheCoreWroteOfItsStart(t *testing.T) {
	// The unit came up after systemd-run was taken for a failure: its words are the run's again.
	l := newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto).add(Marker{Kind: MkStartFailed, N: 1})
	l.start(1, PhaseRun, bootA).blockStart(1, 1, BlockVerify)
	s := reduced(t, running(l.input()))
	is(t, s, StateRunning, "")
	if !s.Busy || s.Block.ID != BlockVerify {
		t.Errorf("a unit that runs holds the machine, in its block: %+v", s)
	}
	if len(ActionsFor(s)) != 1 || ActionsFor(s)[0] != ActionAbort {
		t.Errorf("a run that goes can be called off and nothing else: %v", ActionsFor(s))
	}
	// Past the point of no return it is not a failure to start either.
	l.blockDone(1, 1, BlockVerify)
	hop := hopInBlock(t, BlockMinimal)
	hop.attempt(2, PhaseRecover, BlockMinimal, WhoOwner)
	hop.add(Marker{Kind: MkStartFailed, N: 2})
	hop.start(2, PhaseRecover, bootA)
	is(t, reduced(t, running(hop.input())), StateRecovering, "")
}

func TestAUnitThatSystemdSaysRunsHoldsTheMachineBeforeItSaysItsFirstWord(t *testing.T) {
	// START_FAILED, and a unit that systemd has active, with nothing yet from the unit: it is on its way, not lost.
	l := newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto).add(Marker{Kind: MkStartFailed, N: 1})
	in := quiet(running(l.input()), time.Hour)
	s := reduced(t, in)
	is(t, s, StateRunning, "")
	if !s.Busy {
		t.Errorf("%+v", s)
	}
	if act := NextAction(s, in); act.Kind != DoNone {
		t.Errorf("nothing is launched over a unit that runs: %+v", act)
	}
	// The same attempt with no unit is the failure to start that it always was.
	is(t, reduced(t, l.input()), StateFailed, ReasonStart)
	// A systemd that does not answer says nothing for the unit: the core's word stands.
	silent := l.input()
	silent.Unit = Unit{}
	is(t, reduced(t, silent), StateFailed, ReasonStart)
}

func TestAWordOfTheUnitAfterTheCoresDoubtIsTakenAndTheDoubtIsDropped(t *testing.T) {
	l := hopInBlock(t, BlockDownload)
	base := l.clone()
	// The same run with the doubt written between the attempt and the start: the same state, whichever way the unit runs.
	doubt := newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto).add(Marker{Kind: MkStartFailed, N: 1})
	doubt.start(1, PhaseRun, bootA)
	for i, b := range BlockList(PhaseRun, Files{}) {
		doubt.blockStart(1, i+1, b)
		doubt.blockFiles(b, true)
		if b == BlockDownload {
			break
		}
		doubt.blockDone(1, i+1, b)
	}
	doubt.files = base.files
	for name, tweak := range map[string]func(*Input){
		"a unit that runs": func(in *Input) { in.Unit.Active = true },
		"a unit that is gone and quiet": func(in *Input) {
			in.Now = in.Now.Add(time.Minute)
		},
	} {
		in1, in2 := base.input(), doubt.input()
		tweak(&in1)
		tweak(&in2)
		if s1, s2 := Reduce(in1), Reduce(in2); s1.Name != s2.Name || s1.Block != s2.Block || s1.Cause != s2.Cause || s1.Reason != s2.Reason {
			t.Errorf("%s: %+v with the doubt, %+v without", name, s2, s1)
		}
	}
	// Once the unit has begun, a START_FAILED written after it does not unbegin it.
	late := base.clone().add(Marker{Kind: MkStartFailed, N: 1})
	is(t, reduced(t, running(late.input())), StateRunning, "")
	is(t, reduced(t, quiet(late.input(), time.Minute)), StateInterrupted, CauseKilled)
}

func TestACoreThatDoubtedTheStartOfAnUnwindLaunchesNothingOverARunningUnit(t *testing.T) {
	l := hopInBlock(t, BlockSources)
	l.attempt(2, PhaseUnwind, "", WhoAuto).add(Marker{Kind: MkStartFailed, N: 2})
	in := running(l.input())
	s := reduced(t, in)
	is(t, s, StateUnwinding, "")
	if act := NextAction(s, in); act.Kind != DoNone {
		t.Errorf("%+v", act)
	}
}
