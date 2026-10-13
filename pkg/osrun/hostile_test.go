package osrun

import (
	"reflect"
	"testing"
	"time"
)

// stays says that what is added to a run changes nothing of its state: the markers are written in the second of the last
// one, so that the reading is the same.
func stays(t *testing.T, name string, base *runLog, tweak func(*Input), strays ...Marker) {
	t.Helper()
	l := base.clone()
	for _, m := range strays {
		m.At = base.clock.Add(-time.Second)
		l.add(m)
	}
	in1, in2 := base.input(), l.input()
	in2.Now = in1.Now
	if tweak != nil {
		tweak(&in1)
		tweak(&in2)
	}
	s1, s2 := Reduce(in1), Reduce(in2)
	if !reflect.DeepEqual(s1, s2) {
		t.Errorf("%s: the state was %+v and is %+v", name, s1, s2)
	}
	if a1, a2 := NextAction(s1, in1), NextAction(s2, in2); a1 != a2 {
		t.Errorf("%s: the next action was %+v and is %+v", name, a1, a2)
	}
}

func TestWhatCannotBelongToARunIsLeftOut(t *testing.T) {
	// A hop whose unit is in the download.
	base := hopInBlock(t, BlockDownload)
	for name, m := range map[string]Marker{
		"an attempt that skips a number":            {Kind: MkAttempt, N: 3, Phase: PhaseRecover, Who: WhoOwner},
		"an attempt of a phase that is not a hop's": {Kind: MkAttempt, N: 2, Phase: PhaseRefresh, Who: WhoAuto},
		"an attempt that is the first again":        {Kind: MkAttempt, N: 1, Phase: PhaseRun, Who: WhoAuto},
		"a second run line":                         {Kind: MkRun, RunID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Hop: "other", RunKind: RunRefresh},
		"a second start of the same unit":           {Kind: MkUnitStart, N: 1, Phase: PhaseRun, Boot: bootB, PID: 9},
		"a word of an attempt that is not there":    {Kind: MkStop, N: 7, Index: 1, ID: BlockTimers, RC: 1, Class: ClassDisk},
		"a block of an attempt that is not there":   {Kind: MkBlockStart, N: 2, Index: 1, ID: BlockTimers},
		"an unwind that nothing stopped":            {Kind: MkUnwound, N: 1, Result: "ok"},
		"the result of a refresh in a hop":          {Kind: MkRefreshDone, N: 1, Result: "no", Why: "none"},
		"the result of a rehearsal in a hop":        {Kind: MkRehearsalReady, N: 1},
		"the result of a check in a hop":            {Kind: MkVerifyDone, N: 1, Boot: bootA},
		"a block that ends and never began":         {Kind: MkBlockDone, N: 1, Index: 4, ID: BlockDownload},
		"a boot seen":                               {Kind: MkBootSeen, Boot: bootB},
	} {
		stays(t, name, base, func(in *Input) { in.Unit.Active = true }, m)
	}
	// The same for a unit that is lost, which reads its boot from the markers.
	stays(t, "a second start of a lost unit", base, func(in *Input) { in.Boot.Now = bootB; in.Now = in.Now.Add(time.Hour) },
		Marker{Kind: MkUnitStart, N: 1, Phase: PhaseRun, Boot: bootB, PID: 9})
	stays(t, "an attempt that is not the next", base, func(in *Input) { in.Now = in.Now.Add(time.Hour) },
		Marker{Kind: MkAttempt, N: 9, Phase: PhaseUnwind, Who: WhoAuto})
}

func TestADuplicateOrALateWordOfAnAttemptChangesNothing(t *testing.T) {
	// A hop that is done up to the restart.
	base := hopAtReboot(t)
	for name, m := range map[string]Marker{
		"a second end":                {Kind: MkUnitEnd, N: 1, Result: "stopped"},
		"a second result":             {Kind: MkRebootPending, N: 1, Boot: bootB, Kernel: "x"},
		"a result that is not needed": {Kind: MkRebootNotNeeded, N: 1, Kernel: "x"},
		"a stop after the result":     {Kind: MkStop, N: 1, Index: 8, ID: BlockPrereboot, RC: 1, Class: ClassDisk, PNR: true},
		"a block after the end":       {Kind: MkBlockStart, N: 1, Index: 9, ID: BlockAfter},
		"an unwind after the end":     {Kind: MkUnwound, N: 1, Result: "ok"},
	} {
		stays(t, name, base, nil, m)
	}
	// A result after a stop is not one.
	stopped := hopInBlock(t, BlockFull).stop(1, 7, BlockFull, 1, ClassNetwork, true)
	stays(t, "a result after a stop", stopped, nil,
		Marker{Kind: MkRebootPending, N: 1, Boot: bootA, Kernel: "x"}, Marker{Kind: MkStop, N: 1, Index: 7, ID: BlockFull, RC: 2, Class: ClassDisk, PNR: true})
}

func TestAWordThatTheUnitCouldNotHaveWrittenYetOrAnymoreChangesNothing(t *testing.T) {
	// A hop whose unit did the last block, was lost, and is read a minute later: the files have what it was to do.
	base := hopLaunched(t)
	for i, b := range BlockList(PhaseRun, Files{}) {
		base.blockStart(1, i+1, b)
		base.blockFiles(b, true)
		base.blockDone(1, i+1, b)
	}
	late := func(in *Input) { in.Now = in.Now.Add(time.Minute) }
	s := reduced(t, afterLast(base.input(), time.Minute))
	is(t, s, StateAwaitingReboot, "")
	// A result that is not a hop's is not the run's, and nor is a stop of a unit that has said it was done.
	for name, m := range map[string]Marker{
		"the result of a refresh":   {Kind: MkRefreshDone, N: 1, Result: "yes", Why: "kernel"},
		"the result of a rehearsal": {Kind: MkRehearsalReady, N: 1},
		"the result of a check":     {Kind: MkVerifyDone, N: 1, Boot: bootA},
	} {
		stays(t, name, base, late, m)
	}
	done := base.clone()
	done.add(Marker{Kind: MkRebootPending, N: 1, Boot: bootA, Kernel: "k"})
	stays(t, "a stop after a result", done, late, Marker{Kind: MkStop, N: 1, Index: 8, ID: BlockPrereboot, RC: 1, Class: ClassDisk, PNR: true})
}

func TestAHandfulOfBlockWordsIsKeptAndAFloodIsNot(t *testing.T) {
	l := hopInBlock(t, BlockDownload)
	for i := 0; i < 5000; i++ {
		l.blockStart(1, 5, BlockDownload)
	}
	in := running(l.input())
	s := reduced(t, in)
	is(t, s, StateRunning, "")
	if s.Block.ID != BlockDownload {
		t.Errorf("%+v", s)
	}
}

// What the log has decided for good: the markers after it are not the run's.
func decidedRuns(t *testing.T) map[string]struct {
	l     *runLog
	tweak func(*Input)
	want  string
} {
	judged := func(v VerifyResult) func(*Input) {
		return func(in *Input) { in.Boot.Now = bootB; in.Verify = &v }
	}
	failed := hopInBlock(t, BlockDownload)
	failed.stop(1, 5, BlockDownload, 100, ClassNetwork, false).unwound(1, "ok").end(1, "stopped")
	aborted := hopInBlock(t, BlockDownload)
	aborted.stop(1, 5, BlockDownload, 0, ClassAbort, false).unwound(1, "ok").end(1, "stopped")
	lost := hopInBlock(t, BlockDownload).wait(time.Minute)
	lost.attempt(2, PhaseUnwind, "", WhoAuto).start(2, PhaseUnwind, bootA).unwound(2, "ok").end(2, "ok")
	undone := rehearsalReady(t)
	undone.attempt(2, PhaseUnwind, "", WhoAuto).start(2, PhaseUnwind, bootA).unwound(2, "ok").end(2, "ok")
	byHand := hopInBlock(t, BlockFull).add(Marker{Kind: MkTakeover})
	byHand.files.DoneAfter = true
	restarted := func(in *Input) { in.Boot.Now = bootB }
	return map[string]struct {
		l     *runLog
		tweak func(*Input)
		want  string
	}{
		"a failure that was undone":       {failed, nil, StateFailed},
		"a failure by the owner's call":   {aborted, nil, StateFailed},
		"a failure after a lost unit":     {lost, nil, StateFailed},
		"a refresh that needs no restart": {refreshDone(t, false), nil, StateRefreshed},
		"a refresh that was restarted":    {refreshDone(t, true), restarted, StateRefreshed},
		"a rehearsal that was undone":     {undone, nil, StateRehearsalDone},
		"a success":                       {hopVerified(t), judged(VerifyResult{}), StateSucceeded},
		"a success with findings":         {hopVerified(t), judged(VerifyResult{Warnings: 2}), StateSucceededWithFindings},
		"a hop done by hand":              {byHand, nil, StateManualDone},
	}
}

// strays is one marker of every kind there is, with every attempt number and phase it might carry: all the things that
// could be written after a run was decided, by a unit that is not the one that ended it or by a hand that is not the core's.
func strays() []Marker {
	var out []Marker
	for _, m := range samples() {
		switch {
		case m.Kind == MkQueued || m.Kind == MkAck:
		case m.N == 0:
			out = append(out, m)
		default:
			for n := 1; n <= 6; n++ {
				c := m
				c.N = n
				out = append(out, c)
			}
		}
	}
	for n := 1; n <= 6; n++ {
		for _, phase := range Phases {
			for _, who := range []string{WhoAuto, WhoOwner} {
				out = append(out,
					Marker{Kind: MkAttempt, N: n, Phase: phase, From: BlockMinimal, Who: who},
					Marker{Kind: MkUnitStart, N: n, Phase: phase, Boot: bootC, PID: 1},
				)
			}
		}
	}
	for _, class := range Classes {
		out = append(out, Marker{Kind: MkStop, N: 2, Index: 3, ID: BlockSources, RC: 1, Class: class}, Marker{Kind: MkStop, N: 3, Index: 3, ID: BlockSources, RC: 1, Class: class, PNR: true})
	}
	return out
}

// The log of a run that is decided is the end of it: nothing that is added to it moves it, but the owner's close.
func TestWhatIsWrittenAfterARunWasDecidedChangesNothing(t *testing.T) {
	all := strays()
	for name, c := range decidedRuns(t) {
		in := c.l.input()
		if c.tweak != nil {
			c.tweak(&in)
		}
		s := reduced(t, in)
		if s.Name != c.want {
			t.Fatalf("%s: the run is %+v, which is not the state it is meant to be decided in", name, s)
		}
		for _, m := range all {
			stays(t, name+" and "+m.Kind, c.l, c.tweak, m)
		}
		// The whole lot, in one go, is no different.
		stays(t, name+" and all of them", c.l, c.tweak, all...)

		// The owner's close is.
		closed := c.l.clone().add(Marker{Kind: MkAck, Result: "acknowledge"})
		in = closed.input()
		if c.tweak != nil {
			c.tweak(&in)
		}
		if s := reduced(t, in); s.Name != StateIdle || !s.Closed {
			t.Errorf("%s closed: %+v", name, s)
		}
		// And one that came before the strays still wins after them.
		l := c.l.clone()
		for _, m := range all {
			m.At = l.clock
			l.add(m)
		}
		l.add(Marker{Kind: MkAck, Result: "acknowledge"})
		in = l.input()
		if c.tweak != nil {
			c.tweak(&in)
		}
		if s := reduced(t, in); s.Name != StateIdle || !s.Closed {
			t.Errorf("%s closed after the strays: %+v", name, s)
		}
	}
}

// A run that is not decided is not sealed by the rule above: the owner's next move is read.
func TestARunThatIsNotDecidedGoesOnWhenTheNextAttemptIsWritten(t *testing.T) {
	for name, c := range map[string]struct {
		l    *runLog
		next func(l *runLog)
		want string
		tw   func(*Input)
	}{
		"a rehearsal is undone": {rehearsalReady(t), func(l *runLog) { l.attempt(2, PhaseUnwind, "", WhoAuto) }, StateUnwinding, nil},
		"a stop is repaired": {
			hopInBlock(t, BlockFull).stop(1, 7, BlockFull, 1, ClassNetwork, true).end(1, "stopped"),
			func(l *runLog) { l.attempt(2, PhaseRecover, BlockFull, WhoOwner) }, StateRecovering, nil,
		},
		"a lost unit is undone": {
			hopInBlock(t, BlockDownload).wait(time.Minute),
			func(l *runLog) { l.attempt(2, PhaseUnwind, "", WhoAuto) }, StateUnwinding, nil,
		},
		"a failed check is run again": {
			hopVerified(t), func(l *runLog) { l.files.DoneAfter = false; l.attempt(3, PhaseVerify, "", WhoOwner) }, StateVerifying,
			func(in *Input) { in.Boot.Now = bootB; in.Verify = &VerifyResult{Errors: 1} },
		},
		"a machine that waits is checked": {
			hopAtReboot(t), func(l *runLog) { l.attempt(2, PhaseVerify, "", WhoAuto) }, StateResuming,
			func(in *Input) { in.Boot.Now = bootB },
		},
		"a hop is taken over": {hopInBlock(t, BlockFull), func(l *runLog) { l.add(Marker{Kind: MkTakeover}) }, StateManual, nil},
	} {
		l := c.l.clone()
		in := l.input()
		if c.tw != nil {
			c.tw(&in)
		}
		before := reduced(t, in)
		c.next(l)
		in = l.input()
		if c.tw != nil {
			c.tw(&in)
		}
		in.Now = in.Now.Add(time.Second)
		s := reduced(t, in)
		if s.Name != c.want || s.Name == before.Name && c.want != before.Name {
			t.Errorf("%s: %q after %q, want %q", name, s.Name, before.Name, c.want)
		}
	}
}
