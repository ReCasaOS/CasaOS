package osrun

import (
	"slices"
	"testing"
	"time"
)

// A systemd that does not answer is waited for as long as a unit speaks. Where no unit runs there is nothing to wait for: nothing
// can be launched or judged, and the owner is asked.

// heldWithNoUnit are the runs that hold the machine and have no unit that runs, each with what makes them read as the state they
// read as.
func heldWithNoUnit(t *testing.T) map[string]struct {
	l     *runLog
	tweak func(*Input)
	want  string
} {
	restarted := func(in *Input) { in.Boot.Now = bootB }
	stopped := hopInBlock(t, BlockFull).stop(1, 7, BlockFull, 1, ClassNetwork, true).end(1, "stopped")
	startLost := newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto).add(Marker{Kind: MkStartFailed, N: 1})
	startLost.files.Begun = true
	checked := hopVerified(t)
	checked.files.DoneAfter = true
	return map[string]struct {
		l     *runLog
		tweak func(*Input)
		want  string
	}{
		"awaiting the restart":             {hopAtReboot(t), nil, StateAwaitingReboot},
		"a restarted machine":              {hopAtReboot(t), restarted, StateResuming},
		"a unit lost before the point":     {hopInBlock(t, BlockDownload), nil, StateInterrupted},
		"a unit lost after the point":      {hopInBlock(t, BlockFull), nil, StateInterrupted},
		"a stop after the point":           {stopped, nil, StateStoppedAtWaypoint},
		"a start that failed after":        {startLost, nil, StateFailed},
		"a check waiting for the judge":    {checked, func(in *Input) { in.Boot.Now = bootB }, StateVerifying},
		"a refresh that wants its restart": {refreshDone(t, true), nil, StateRebootFirst},
		"a rehearsal that is ready":        {rehearsalReady(t), nil, StateRehearsed},
	}
}

func TestSystemdThatDoesNotAnswerIsNotWaitedForWhereNoUnitRuns(t *testing.T) {
	for name, c := range heldWithNoUnit(t) {
		// an attempt that has not ended may still have its unit, which nobody can ask: it is a running one for as long as it speaks
		before := c.want
		if c.want == StateInterrupted {
			before = StateRunning
		}
		base := c.l.input()
		if c.tweak != nil {
			c.tweak(&base)
		}
		base = quiet(base, time.Minute)
		if s := reduced(t, base); s.Name != c.want || !s.Busy {
			t.Fatalf("%s: %+v, want the busy state %s", name, s, c.want)
		}
		silent := base
		silent.Unit = Unit{}
		// Told by the time since the run last spoke, when the caller says nothing else.
		is(t, reduced(t, quiet(silent, 28*time.Minute)), before, "")
		s := reduced(t, quiet(silent, 40*time.Minute))
		is(t, s, StateNeedsAttention, ReasonSystemdSilent)
		if !s.Busy || !s.NeedsAckWord() {
			t.Errorf("%s: %+v", name, s)
		}
		// The buttons are those that work with no systemd: the owner's word, and nothing that asks the unit.
		if buttons := ActionsFor(s); !slices.Equal(buttons, []string{ActionAcknowledge}) {
			t.Errorf("%s: %v", name, buttons)
		}
		if act := NextAction(s, quiet(silent, 40*time.Minute)); act.Kind != DoNone {
			t.Errorf("%s: %+v", name, act)
		}
		// Told by how long systemd has been silent, when the caller says: a machine that waits for days is not one whose systemd
		// has been silent for days.
		for d, want := range map[time.Duration]string{time.Minute: before, 29 * time.Minute: before, 30 * time.Minute: StateNeedsAttention} {
			in := quiet(silent, 72*time.Hour)
			in.Unit.SilentSince = in.Now.Add(-d)
			if got := reduced(t, in); got.Name != want {
				t.Errorf("%s: silent for %v: %s, want %s", name, d, got.Name, want)
			}
		}
		// A systemd that answers is not silent whatever a stale mark says.
		answers := quiet(base, 72*time.Hour)
		answers.Unit.SilentSince = answers.Now.Add(-time.Hour)
		if got := reduced(t, answers); got.Name == StateNeedsAttention {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestARestartThatIsUnderWayIsWaitedForWhateverSystemdSays(t *testing.T) {
	l := hopAtReboot(t).wait(45 * time.Minute).add(Marker{Kind: MkRebootRequested})
	in := l.input()
	in.Unit = Unit{}
	s := reduced(t, in)
	is(t, s, StateRebooting, "")
	// And a restart that never happened is a stalled one, which holds the machine and is asked about like the rest.
	stalled := reduced(t, quiet(in, 40*time.Minute))
	is(t, stalled, StateNeedsAttention, ReasonSystemdSilent)
}

func TestWhatDoesNotHoldTheMachineIsNeverSilent(t *testing.T) {
	for name, c := range map[string]struct {
		l    *runLog
		want string
	}{
		"a success": {hopVerified(t), StateSucceeded},
		"a refresh": {refreshDone(t, false), StateRefreshed},
	} {
		in := quiet(c.l.input(), 5*time.Hour)
		in.Unit = Unit{}
		in.Boot.Now = bootB
		in.Verify = &VerifyResult{}
		if s := reduced(t, in); s.Name == StateNeedsAttention || s.Busy {
			t.Errorf("%s: %+v", name, s)
		}
	}
}

func TestNoVerdictIsMadeWhileSystemdDoesNotAnswer(t *testing.T) {
	l := hopVerified(t)
	in := l.input()
	in.Boot.Now = bootB
	// The check ended and nothing has judged it: with systemd answering the judge is next, with it silent nothing is.
	s := reduced(t, in)
	is(t, s, StateVerifying, "")
	if act := NextAction(s, in); act.Kind != DoJudge {
		t.Fatalf("%+v", act)
	}
	for _, d := range []time.Duration{0, time.Minute, 10 * time.Minute, 2 * time.Hour} {
		silent := quiet(in, d)
		silent.Unit = Unit{}
		s := reduced(t, silent)
		if act := NextAction(s, silent); act.Kind == DoJudge {
			t.Errorf("silent for %v: %+v for %+v", d, act, s)
		}
	}
}

func TestARefreshThatWantsItsRestartIsSpokenOfAgainAsAHopIs(t *testing.T) {
	l := refreshDone(t, true)
	in := l.input()
	s := reduced(t, in)
	is(t, s, StateRebootFirst, "")
	for _, c := range []struct {
		after    time.Duration
		reminded int
		want     int
	}{
		{30 * time.Minute, 0, 0}, {time.Hour, 0, 1}, {time.Hour, 1, 0}, {25 * time.Hour, 1, 2}, {25 * time.Hour, 2, 0}, {50 * time.Hour, 2, 3},
	} {
		at := afterLast(in, c.after)
		at.Reminded = c.reminded
		act := NextAction(reduced(t, at), at)
		if c.want == 0 {
			if act.Kind != DoNone {
				t.Errorf("%v after, %d told: %+v", c.after, c.reminded, act)
			}
			continue
		}
		if act.Kind != DoRemind || act.N != c.want {
			t.Errorf("%v after, %d told: %+v, want reminder %d", c.after, c.reminded, act, c.want)
		}
	}
}
func TestAMachineThatCanBeCheckedAgainOffersTheCheckWhenItHadFindings(t *testing.T) {
	in := hopVerified(t).input()
	in.Boot.Now = bootB
	in.Verify = &VerifyResult{Warnings: 1}
	s := reduced(t, in)
	is(t, s, StateSucceededWithFindings, "")
	if want := []string{ActionResume, ActionAcknowledge}; !slices.Equal(ActionsFor(s), []string{ActionResume, ActionAcknowledge}) {
		t.Errorf("%v, want %v", ActionsFor(s), want)
	}
	if s.Busy {
		t.Errorf("%+v", s)
	}
	// A success has nothing to check again.
	in.Verify = &VerifyResult{}
	if s := reduced(t, in); slices.Contains(ActionsFor(s), ActionResume) {
		t.Errorf("%+v", s)
	}
	// A check that failed is offered again, as it was.
	in.Verify = &VerifyResult{Errors: 1}
	if s := reduced(t, in); !slices.Contains(ActionsFor(s), ActionResume) {
		t.Errorf("%+v", s)
	}
}
