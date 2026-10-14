package osrun

import (
	"strings"
	"testing"
	"time"
)

// A log that cannot place a run (no good first line, no RUN line) says nothing of what is going on, and the files and the unit
// do. The machine is not free of a run that it cannot read.

func TestALogThatCannotPlaceTheRunIsNeverIdleWhenSomethingIsGoingOn(t *testing.T) {
	full := newLog(t, RunHop).input()
	logs := map[string][]Marker{
		"no log":                              nil,
		"a first line that is not QUEUED":     full.Markers[1:],
		"the queued line only":                full.Markers[:1],
		"a log that begins with the run line": {full.Markers[1], full.Markers[0]},
	}
	busyFiles := map[string]Files{
		"begun":                 {Begun: true},
		"minimal done":          {DoneMinimal: true},
		"all done but the last": {Begun: true, DoneMinimal: true, DoneFull: true, DonePrereboot: true},
	}
	for name, ms := range logs {
		for fname, files := range busyFiles {
			in := Input{Markers: ms, Files: files, Unit: Unit{Answered: true}, Now: t0.Add(time.Hour)}
			s := reduced(t, in)
			is(t, s, StateNeedsAttention, ReasonRunDamaged)
			if !s.Busy || !s.PNR || s.Reversible {
				t.Errorf("%s with %s: packages are being changed and nothing says which run it is: %+v", name, fname, s)
			}
			if act := NextAction(s, in); act.Kind != DoNone {
				t.Errorf("%s with %s: %+v", name, fname, act)
			}
		}
		// A unit that runs is not nothing either, and nor is a run whose files are not the ones that were written.
		for fname, in := range map[string]Input{
			"a unit that runs": {Markers: ms, Unit: Unit{Active: true, Answered: true}, Now: t0.Add(time.Hour)},
			"damaged files":    {Markers: ms, Unit: Unit{Answered: true}, Damaged: true, Now: t0.Add(time.Hour)},
		} {
			s := reduced(t, in)
			is(t, s, StateNeedsAttention, ReasonRunDamaged)
			if !s.Busy {
				t.Errorf("%s with %s: %+v", name, fname, s)
			}
		}
		// Nothing going on is no run.
		s := reduced(t, Input{Markers: ms, Unit: Unit{Answered: true}, Now: t0.Add(time.Hour)})
		if s.Name != StateIdle || s.Busy {
			t.Errorf("%s: %+v", name, s)
		}
	}
}

func TestAnAttemptThatTheKindOfTheRunCannotHaveIsNotAnExplanationForAUnitThatRuns(t *testing.T) {
	// The log says a refresh and the unit runs the phase of a hop: the attempt is not one of the refresh's, and is not taken.
	l := newLog(t, RunRefresh).attempt(1, PhaseRun, "", WhoAuto).start(1, PhaseRun, bootA).blockStart(1, 1, BlockVerify)
	in := running(quiet(l.input(), 5*time.Second))
	s := reduced(t, in)
	is(t, s, StateNeedsAttention, ReasonRunDamaged)
	if !s.Busy {
		t.Errorf("%+v", s)
	}
	// A unit that does not run and files that say nothing are a run that was lost before it launched anything.
	idle := reduced(t, quiet(l.input(), 5*time.Second))
	is(t, idle, StateIdle, "")
	is(t, reduced(t, quiet(l.input(), time.Minute)), StateFailed, ReasonStart)
	// The point of no return is the files' word, whatever the log says.
	pnr := quiet(l.input(), time.Minute)
	pnr.Files = Files{Begun: true}
	s = reduced(t, pnr)
	is(t, s, StateNeedsAttention, ReasonRunDamaged)
	if !s.Busy || !s.PNR {
		t.Errorf("%+v", s)
	}
}

// busyWhereItHasToBe is what every reading of a run must keep: where something is going on, the run holds the machine, unless
// the owner has closed it.
func busyWhereItHasToBe(in Input, st State) string {
	tm := timingOf(in)
	v := scan(in, tm)
	if st.Closed || v.valid && (v.acked || v.takeover) {
		return ""
	}
	placed := v.valid && v.run != nil
	switch {
	case !placed && (in.Files.PNR() || in.Unit.Active || in.Damaged) && !st.Busy:
		return "a machine that is going on, with no run placed, is idle"
	case placed && in.Unit.Active && v.last() != nil && !v.last().Ended && !st.Busy:
		return "a unit that runs and has not ended is not busy"
	case in.Files.PNR() && !in.Files.DoneAfter && !st.Busy && !decidedForGood(st):
		return "a machine past the point of no return is not busy"
	}
	return ""
}

func TestTheHandBuiltLogsKeepTheMachineBusyWhereSomethingIsGoingOn(t *testing.T) {
	logs := map[string]*runLog{
		"a hop in a block":    hopInBlock(t, BlockDownload),
		"a hop at the end":    hopAtReboot(t),
		"a launch on its way": newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto),
		"a doubted launch":    newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto).add(Marker{Kind: MkStartFailed, N: 1}),
		"a refresh":           newLog(t, RunRefresh).attempt(1, PhaseRefresh, "", WhoAuto).start(1, PhaseRefresh, bootA),
	}
	for name, l := range logs {
		for _, active := range []bool{false, true} {
			for _, d := range []time.Duration{0, 10 * time.Second, time.Minute, time.Hour} {
				in := quiet(l.input(), d)
				in.Unit.Active = active
				if msg := busyWhereItHasToBe(in, Reduce(in)); msg != "" {
					t.Errorf("%s, unit active %v, %v later: %s: %+v", name, active, d, msg, Reduce(in))
				}
			}
		}
	}
	if !strings.Contains(busyWhereItHasToBe(Input{Unit: Unit{Active: true}}, State{Name: StateIdle}), "idle") {
		t.Errorf("the check does not see an idle state with a unit that runs")
	}
}
