package osrun

import (
	"slices"
	"testing"
	"time"
)

func next(t *testing.T, in Input) (State, Action) {
	t.Helper()
	s := reduced(t, in)
	a := NextAction(s, in)
	if !isOneOf([]string{DoNone, DoUnwind, DoLaunchVerify, DoLaunchRecover, DoJudge, DoBarrier, DoRemind, DoCleanup, DoAlert}, a.Kind) {
		t.Fatalf("action %+v is not one of the actions", a)
	}
	return s, a
}

func wantAction(t *testing.T, got, want Action) {
	t.Helper()
	if got != want {
		t.Fatalf("action %+v, want %+v", got, want)
	}
}

func TestARunThatWasLostBeforeThePointOfNoReturnIsUndoneByItself(t *testing.T) {
	in := quiet(hopInBlock(t, BlockDownload).input(), time.Minute)
	s, a := next(t, in)
	is(t, s, StateInterrupted, CauseKilled)
	wantAction(t, a, Action{Kind: DoUnwind, Phase: PhaseUnwind, Who: WhoAuto})

	// An unwind that is lost is started again, three times in all.
	l := hopInBlock(t, BlockDownload).wait(time.Minute)
	for n := 2; n <= 4; n++ {
		l.attempt(n, PhaseUnwind, "", WhoAuto).start(n, PhaseUnwind, bootA)
		in = quiet(l.input(), time.Minute)
		s, a = next(t, in)
		if n < 4 {
			is(t, s, StateInterrupted, CauseKilled)
			wantAction(t, a, Action{Kind: DoUnwind, Phase: PhaseUnwind, Who: WhoAuto})
		} else {
			is(t, s, StateNeedsAttention, ReasonUnwindPartial)
			wantAction(t, a, Action{Kind: DoNone})
		}
	}
}

func TestAFailureToStartBeforeThePointOfNoReturnNeedsNothing(t *testing.T) {
	l := newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto).add(Marker{Kind: MkStartFailed, N: 1})
	s, a := next(t, l.input())
	is(t, s, StateFailed, ReasonStart)
	wantAction(t, a, Action{Kind: DoNone})
}

func TestARehearsalIsUndoneWhenItsTimeIsUpOrTheMachineRestarted(t *testing.T) {
	l := rehearsalReady(t)
	in := l.input()
	s, a := next(t, in)
	is(t, s, StateRehearsed, "")
	wantAction(t, a, Action{Kind: DoNone})
	unwind := Action{Kind: DoUnwind, Phase: PhaseUnwind, Who: WhoAuto}

	_, a = next(t, quiet(in, 29*time.Minute))
	wantAction(t, a, Action{Kind: DoNone})
	_, a = next(t, afterLast(in, 30*time.Minute))
	wantAction(t, a, unwind)

	other := in
	other.Boot.Now = bootB
	_, a = next(t, other)
	wantAction(t, a, unwind)
	// A boot that cannot be read is not a restart.
	other.Boot = Boot{}
	_, a = next(t, other)
	wantAction(t, a, Action{Kind: DoNone})

	// And while the unwind is on its way, or running, the core does not start it again.
	l.attempt(2, PhaseUnwind, "", WhoAuto)
	// (One that never began is lost, and is started again like any unwind that was lost.)
	late := afterLast(l.input(), time.Hour)
	s, a = next(t, late)
	is(t, s, StateInterrupted, CauseKilled)
	wantAction(t, a, unwind)
	soon := afterLast(l.input(), 5*time.Second)
	s, a = next(t, soon)
	is(t, s, StateUnwinding, "")
	wantAction(t, a, Action{Kind: DoNone})
}

func TestAMachineThatHasRestartedIsCheckedWhenItIsReadyOrWhenItHasHadTimeEnough(t *testing.T) {
	l := hopAtReboot(t)
	in := l.input()
	in.Boot.Now = bootB
	in.BootSeenAt = in.Now
	verify := Action{Kind: DoLaunchVerify, Phase: PhaseVerify, Who: WhoAuto}

	s, a := next(t, in)
	is(t, s, StateResuming, "")
	wantAction(t, a, Action{Kind: DoNone})

	ready := in
	ready.Ready = true
	_, a = next(t, ready)
	wantAction(t, a, verify)

	_, a = next(t, quiet(in, 9*time.Minute+59*time.Second))
	wantAction(t, a, Action{Kind: DoNone})
	_, a = next(t, quiet(in, 10*time.Minute))
	wantAction(t, a, verify)

	// With no word of when the machine was seen, only its being ready counts.
	unseen := quiet(in, time.Hour)
	unseen.BootSeenAt = time.Time{}
	_, a = next(t, unseen)
	wantAction(t, a, Action{Kind: DoNone})

	// Not while systemd does not answer.
	silent := ready
	silent.Unit = Unit{}
	if got := NextAction(s, silent); got.Kind != DoNone {
		t.Errorf("a launch while systemd does not answer: %+v", got)
	}

	// Not while the launch that was written is on its way.
	l.attempt(2, PhaseVerify, "", WhoAuto)
	flight := l.input()
	flight.Boot.Now, flight.Ready = bootB, true
	s, a = next(t, quiet(flight, 5*time.Second))
	is(t, s, StateResuming, "")
	wantAction(t, a, Action{Kind: DoNone})
}

func TestACheckThatWasLostIsRunAgainAFewTimes(t *testing.T) {
	verify := Action{Kind: DoLaunchVerify, Phase: PhaseVerify, Who: WhoAuto}
	l := hopRestarted(t)
	l.blockStart(2, 1, BlockAfter)
	in := quiet(l.input(), time.Minute)
	in.Boot.Now = bootB
	s, a := next(t, in)
	is(t, s, StateInterrupted, "")
	wantAction(t, a, verify)
	// A second restart before the check ended is a power cut for it, and it is run again all the same.
	in.Boot.Now = bootC
	s, a = next(t, in)
	is(t, s, StateInterrupted, CausePower)
	wantAction(t, a, verify)

	for n := 3; n <= 4; n++ {
		l.attempt(n, PhaseVerify, "", WhoAuto).start(n, PhaseVerify, bootB).blockStart(n, 1, BlockAfter)
		in = quiet(l.input(), time.Minute)
		in.Boot.Now = bootB
		s, a = next(t, in)
		is(t, s, StateInterrupted, "")
		if n < 3 {
			wantAction(t, a, verify)
		}
	}
	// Three checks that did not end: the owner is told, the core does not go on.
	wantAction(t, a, Action{Kind: DoAlert})

	// A check that never began is run again as well.
	l = hopAtReboot(t)
	l.add(Marker{Kind: MkRebootRequested}).attempt(2, PhaseVerify, "", WhoAuto).add(Marker{Kind: MkStartFailed, N: 2})
	in = l.input()
	in.Boot.Now = bootB
	s, a = next(t, in)
	is(t, s, StateFailed, ReasonStart)
	wantAction(t, a, verify)
}

func TestACheckThatIsDoneWithNoVerdictIsJudged(t *testing.T) {
	in := hopVerified(t).input()
	in.Boot.Now = bootB
	s, a := next(t, in)
	is(t, s, StateVerifying, "")
	wantAction(t, a, Action{Kind: DoJudge})
	// The verdict is a state of its own: nothing more to do.
	in.Verify = &VerifyResult{}
	s, a = next(t, in)
	is(t, s, StateSucceeded, "")
	wantAction(t, a, Action{Kind: DoNone})
	// While the unit that checks is running there is nothing to judge yet.
	l := hopRestarted(t)
	l.blockStart(2, 1, BlockAfter)
	in = running(l.input())
	in.Boot.Now = bootB
	s, a = next(t, in)
	is(t, s, StateVerifying, "")
	wantAction(t, a, Action{Kind: DoNone})
}

func TestAMachineThatWaitsForItsRestartIsCheckedAndSpokenOfAgain(t *testing.T) {
	l := hopAtReboot(t)
	in := l.input()
	_, a := next(t, in)
	wantAction(t, a, Action{Kind: DoBarrier})
	waits := func(d time.Duration, reminded int) Action {
		x := afterLast(in, d)
		x.Reminded = reminded
		_, a := next(t, x)
		return a
	}
	for _, c := range []struct {
		waited   time.Duration
		reminded int
		want     Action
	}{
		{59 * time.Minute, 0, Action{Kind: DoBarrier}},
		{time.Hour, 0, Action{Kind: DoRemind, N: 1}},
		{time.Hour, 1, Action{Kind: DoBarrier}},
		{23 * time.Hour, 1, Action{Kind: DoBarrier}},
		{24 * time.Hour, 1, Action{Kind: DoRemind, N: 2}},
		{24 * time.Hour, 2, Action{Kind: DoBarrier}},
		{47 * time.Hour, 2, Action{Kind: DoBarrier}},
		{48 * time.Hour, 2, Action{Kind: DoRemind, N: 3}},
		{10 * 24 * time.Hour, 3, Action{Kind: DoRemind, N: 11}},
	} {
		// The wait is counted from the unit's word that it was done, a second before the last marker.
		x := afterLast(in, c.waited-time.Second)
		x.Reminded = c.reminded
		_, got := next(t, x)
		if got != c.want {
			t.Errorf("waited %v, %d sent: %+v, want %+v", c.waited, c.reminded, got, c.want)
		}
	}
	_ = waits
	// Asked for and not happening, it is checked and spoken of in the same way.
	l.add(Marker{Kind: MkRebootRequested})
	x := afterLast(l.input(), 20*time.Minute)
	s, a := next(t, x)
	is(t, s, StateRebootStalled, "")
	wantAction(t, a, Action{Kind: DoBarrier})
	// A run that is waiting for a restart of a refresh is not checked: it has no barrier.
	r := refreshDone(t, true).input()
	s, a = next(t, r)
	is(t, s, StateRebootFirst, "")
	wantAction(t, a, Action{Kind: DoNone})
}

func TestAClosedRunIsToBeCleanedUp(t *testing.T) {
	in := hopVerified(t).add(Marker{Kind: MkAck, Result: "acknowledge"}).input()
	s, a := next(t, in)
	if !s.Closed {
		t.Fatalf("%+v", s)
	}
	wantAction(t, a, Action{Kind: DoCleanup})
	// What has no run has nothing to clean up.
	s, a = next(t, Input{})
	wantAction(t, a, Action{Kind: DoNone})
}

func TestNothingIsLaunchedAfterATakeoverOrWhileAUnitRuns(t *testing.T) {
	for name, l := range map[string]*runLog{
		"a takeover of a hop that was lost": hopInBlock(t, BlockFull).add(Marker{Kind: MkTakeover}),
		"a takeover of an unwind":           hopInBlock(t, BlockDownload).add(Marker{Kind: MkTakeover}),
	} {
		in := quiet(l.input(), time.Hour)
		if s, a := next(t, in); a.Kind != DoNone || s.Name != StateManual {
			t.Errorf("%s: %+v %+v", name, s, a)
		}
	}
	for _, id := range []string{BlockDownload, BlockFull} {
		in := running(hopInBlock(t, id).input())
		if _, a := next(t, in); a.Kind != DoNone {
			t.Errorf("a unit that runs in %s: %+v", id, a)
		}
	}
}

func TestNothingIsLaunchedWhileSystemdDoesNotAnswer(t *testing.T) {
	// The states that call for a launch, read with systemd answering, and then handed the unit's silence.
	cases := map[string]Input{
		"a hop lost before the point of no return": quiet(hopInBlock(t, BlockDownload).input(), time.Minute),
		"a check lost": quiet(hopRestarted(t).input(), time.Minute),
	}
	for name, in := range cases {
		in.Boot.Now = bootA
		s := reduced(t, in)
		silent := in
		silent.Unit = Unit{}
		if got := NextAction(s, silent); got.Kind == DoUnwind || got.Kind == DoLaunchVerify || got.Kind == DoLaunchRecover {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func recoverable(t *testing.T, class string) *runLog {
	l := hopInBlock(t, BlockMinimal).stop(1, 6, BlockMinimal, 1, class, true).end(1, "stopped")
	return l
}

func TestAHopThatStoppedAfterThePointOfNoReturnWaitsForTheOwnerWhateverTheClass(t *testing.T) {
	for _, class := range Classes {
		in := quiet(recoverable(t, class).input(), 3*time.Hour)
		s, a := next(t, in)
		is(t, s, StateStoppedAtWaypoint, "")
		wantAction(t, a, Action{Kind: DoAlert})
	}
	// A unit that was lost, and one that never began, say nothing of why.
	in := quiet(hopInBlock(t, BlockFull).input(), 3*time.Hour)
	s, a := next(t, in)
	is(t, s, StateInterrupted, "")
	wantAction(t, a, Action{Kind: DoAlert})
	l := newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto).add(Marker{Kind: MkStartFailed, N: 1})
	in = l.input()
	in.Files = Files{Begun: true}
	s, a = next(t, in)
	is(t, s, StateFailed, ReasonStart)
	wantAction(t, a, Action{Kind: DoAlert})
}

func TestTheShippedTimingNeverRepairsByItself(t *testing.T) {
	if DefaultTiming.AutoRecoverTries != 0 {
		t.Fatalf("AutoRecoverTries is %d: nothing repairs a hop without a click until the repair has been seen to work", DefaultTiming.AutoRecoverTries)
	}
	for _, class := range Classes {
		for _, ago := range []time.Duration{time.Minute, time.Hour, 100 * time.Hour} {
			in := quiet(recoverable(t, class).input(), ago)
			in.Timing = DefaultTiming
			if _, a := next(t, in); a.Kind == DoLaunchRecover {
				t.Errorf("class %s, %v after the stop: %+v", class, ago, a)
			}
		}
	}
}

func repairing(tries, wave int) Timing {
	tm := DefaultTiming
	tm.AutoRecoverTries, tm.AutoRecoverWave = tries, wave
	return tm
}

func TestARepairByItselfStartsFromTheFirstBlockThatIsNotDone(t *testing.T) {
	launch := func(from string) Action {
		return Action{Kind: DoLaunchRecover, Phase: PhaseRecover, From: from, Who: WhoAuto}
	}
	for _, c := range []struct {
		id, from string
	}{{BlockMinimal, BlockMinimal}, {BlockFull, BlockFull}, {BlockPrereboot, BlockPrereboot}} {
		l := hopInBlock(t, c.id).stop(1, 7, c.id, 1, ClassNetwork, true).end(1, "stopped")
		in := afterLast(l.input(), 2*time.Minute)
		in.Timing = repairing(2, 1)
		s, a := next(t, in)
		is(t, s, StateStoppedAtWaypoint, "")
		wantAction(t, a, launch(c.from))
	}
	// The same for a unit that was lost: it said no class and is told as an unknown one, which is the second wave's.
	l := hopInBlock(t, BlockFull)
	in := afterLast(l.input(), 10*time.Minute)
	in.Timing = repairing(2, 1)
	if _, a := next(t, in); a.Kind != DoAlert {
		t.Errorf("a lost unit in the first wave: %+v", a)
	}
	in.Timing = repairing(2, 2)
	_, a := next(t, in)
	wantAction(t, a, launch(BlockFull))
}

func TestWhichClassesAreRepairedByItselfDependsOnTheWave(t *testing.T) {
	for _, class := range Classes {
		for wave := 0; wave <= 3; wave++ {
			l := recoverable(t, class)
			in := afterLast(l.input(), time.Hour)
			in.Timing = repairing(2, wave)
			s, a := next(t, in)
			want := Retryable(class, wave)
			if class == ClassPlan {
				want = false
			}
			if got := a.Kind == DoLaunchRecover; got != want {
				t.Errorf("class %s, wave %d (state %s class %s): repair by itself is %v", class, wave, s.Name, s.Class, got)
			}
		}
	}
}

func TestARepairByItselfWaitsAndGivesUp(t *testing.T) {
	build := func(tries int, who string) *runLog {
		l := recoverable(t, ClassNetwork)
		for i := 0; i < tries; i++ {
			l.attempt(2+i, PhaseRecover, BlockMinimal, who).start(2+i, PhaseRecover, bootA)
			l.blockStart(2+i, 2, BlockMinimal).stop(2+i, 2, BlockMinimal, 1, ClassNetwork, true).end(2+i, "stopped")
		}
		return l
	}
	at := func(l *runLog, d time.Duration) Action {
		in := afterLast(l.input(), d)
		in.Timing = repairing(2, 1)
		_, a := next(t, in)
		return a
	}
	// Two minutes after the first stop, ten after the second, and then it is the owner's.
	if a := at(build(0, WhoAuto), 119*time.Second); a.Kind != DoNone {
		t.Errorf("before the first backoff: %+v", a)
	}
	if a := at(build(0, WhoAuto), 120*time.Second); a.Kind != DoLaunchRecover {
		t.Errorf("after the first backoff: %+v", a)
	}
	if a := at(build(1, WhoAuto), 9*time.Minute+59*time.Second); a.Kind != DoNone {
		t.Errorf("before the second backoff: %+v", a)
	}
	if a := at(build(1, WhoAuto), 10*time.Minute); a.Kind != DoLaunchRecover {
		t.Errorf("after the second backoff: %+v", a)
	}
	// The tries spent, the run is half done and the core goes no further: it has said so, and the owner decides.
	spent := afterLast(build(2, WhoAuto).input(), 24*time.Hour)
	spent.Timing = repairing(2, 1)
	if s, a := next(t, spent); s.Name != StateFailed || s.Reason != ReasonHalf || a.Kind != DoNone {
		t.Errorf("the tries are spent: %+v %+v", s, a)
	}
	// The owner's own repair gives the tries back.
	if a := at(build(3, WhoOwner), 2*time.Minute); a.Kind != DoLaunchRecover {
		t.Errorf("after the owner's repair: %+v", a)
	}
	// A repair that is on its way is not started again.
	l := build(0, WhoAuto)
	l.attempt(2, PhaseRecover, BlockMinimal, WhoAuto)
	in := afterLast(l.input(), 5*time.Second)
	in.Timing = repairing(2, 1)
	if s, a := next(t, in); s.Name != StateRecovering || a.Kind != DoNone {
		t.Errorf("%+v %+v", s, a)
	}
}

func TestNothingIsRepairedWhileSystemdDoesNotAnswer(t *testing.T) {
	in := afterLast(recoverable(t, ClassNetwork).input(), time.Hour)
	in.Timing = repairing(2, 2)
	s := reduced(t, in)
	in.Unit = Unit{}
	if a := NextAction(s, in); a.Kind != DoNone {
		t.Errorf("%+v", a)
	}
}

func TestEveryStateThatNeedsNoActionSaysNone(t *testing.T) {
	for name, in := range map[string]Input{
		"a refresh that is done": refreshDone(t, false).input(),
		"a failure that is undone": func() Input {
			l := hopInBlock(t, BlockDownload)
			l.stop(1, 5, BlockDownload, 1, ClassNetwork, false).unwound(1, "ok").end(1, "stopped")
			return l.input()
		}(),
		"a needs-attention": func() Input { in := hopInBlock(t, BlockDownload).input(); in.Damaged = true; return in }(),
		"a succeeded": func() Input {
			in := hopVerified(t).input()
			in.Boot.Now = bootB
			in.Verify = &VerifyResult{}
			return in
		}(),
		"a succeeded with findings": func() Input {
			in := hopVerified(t).input()
			in.Boot.Now = bootB
			in.Verify = &VerifyResult{Warnings: 1}
			return in
		}(),
		"a failed check": func() Input {
			in := hopVerified(t).input()
			in.Boot.Now = bootB
			in.Verify = &VerifyResult{Errors: 1}
			return in
		}(),
		"a machine that is restarting":    func() Input { return hopAtReboot(t).add(Marker{Kind: MkRebootRequested}).input() }(),
		"a refresh that waits to restart": refreshDone(t, true).input(),
	} {
		s := reduced(t, in)
		if a := NextAction(s, in); a.Kind != DoNone {
			t.Errorf("%s (%s): %+v", name, s.Name, a)
		}
	}
}

func TestEveryNextActionWithAnInputIsAFunctionOfIt(t *testing.T) {
	in := quiet(hopInBlock(t, BlockDownload).input(), time.Minute)
	s := reduced(t, in)
	first := NextAction(s, in)
	for i := 0; i < 5; i++ {
		if got := NextAction(s, in); got != first {
			t.Fatalf("%+v then %+v", first, got)
		}
	}
}

func TestTheButtonsOfEveryState(t *testing.T) {
	for _, c := range []struct {
		name string
		s    State
		want []string
	}{
		{"refreshing", State{Name: StateRefreshing, Abortable: true}, []string{ActionAbort}},
		{"rehearsing", State{Name: StateRehearsing, Abortable: true}, []string{ActionAbort}},
		{"running before the point of no return", State{Name: StateRunning, Abortable: true}, []string{ActionAbort}},
		{"running past the minimal upgrade", State{Name: StateRunning}, nil},
		{"running past the point of no return", State{Name: StateRunning, PNR: true}, nil},
		{"unwinding", State{Name: StateUnwinding}, nil},
		{"recovering", State{Name: StateRecovering, PNR: true}, nil},
		{"resuming", State{Name: StateResuming, PNR: true}, nil},
		{"verifying", State{Name: StateVerifying, PNR: true}, nil},
		{"rebooting", State{Name: StateRebooting, PNR: true}, nil},
		{"reboot_first", State{Name: StateRebootFirst}, []string{ActionReboot, ActionAcknowledge}},
		{"refreshed", State{Name: StateRefreshed}, []string{ActionAcknowledge}},
		{"rehearsal_done", State{Name: StateRehearsalDone}, []string{ActionAcknowledge}},
		{"succeeded", State{Name: StateSucceeded, PNR: true}, []string{ActionAcknowledge}},
		{"succeeded_with_findings", State{Name: StateSucceededWithFindings, PNR: true}, []string{ActionAcknowledge}},
		{"a failure that can be tried again", State{Name: StateFailed, Reason: ClassNetwork}, []string{ActionAcknowledge, ActionDiscard}},
		{"a failure to start before the point of no return", State{Name: StateFailed, Reason: ReasonStart}, []string{ActionAcknowledge, ActionDiscard}},
		{"a failure to start after it", State{Name: StateFailed, Reason: ReasonStart, PNR: true, Resumable: true}, []string{ActionResume, ActionTakeover}},
		{"a failed check", State{Name: StateFailed, Reason: ReasonVerify, PNR: true, Resumable: true}, []string{ActionResume, ActionAcknowledge, ActionTakeover}},
		{"a repair given up on", State{Name: StateFailed, Reason: ReasonHalf, PNR: true, Resumable: true}, []string{ActionResume, ActionAcknowledge, ActionTakeover}},
		{"a repair given up on that cannot be resumed", State{Name: StateFailed, Reason: ReasonHalf, PNR: true}, []string{ActionAcknowledge, ActionTakeover}},
		{"rehearsed", State{Name: StateRehearsed}, []string{ActionDiscard}},
		{"awaiting_reboot with a green barrier", State{Name: StateAwaitingReboot, PNR: true}, []string{ActionReboot, ActionTakeover}},
		{"awaiting_reboot with a barrier that is not", State{Name: StateAwaitingReboot, PNR: true, RebootBlocked: true}, []string{ActionTakeover}},
		{"reboot_stalled", State{Name: StateRebootStalled, PNR: true, Stalled: true}, []string{ActionReboot, ActionTakeover}},
		{"reboot_stalled with a red barrier", State{Name: StateRebootStalled, PNR: true, RebootBlocked: true}, []string{ActionTakeover}},
		{"interrupted before the point of no return", State{Name: StateInterrupted}, nil},
		{"interrupted after it", State{Name: StateInterrupted, PNR: true, Resumable: true}, []string{ActionResume, ActionAcknowledge, ActionTakeover}},
		{"a stop of a class that can be resumed", State{Name: StateStoppedAtWaypoint, PNR: true, Resumable: true}, []string{ActionResume, ActionAcknowledge, ActionTakeover}},
		{"a stop of a class that cannot", State{Name: StateStoppedAtWaypoint, PNR: true}, []string{ActionAcknowledge, ActionTakeover}},
		{"needs_attention", State{Name: StateNeedsAttention}, []string{ActionAcknowledge, ActionTakeover}},
		{"manual", State{Name: StateManual, PNR: true}, []string{ActionAcknowledge}},
		{"manual_done", State{Name: StateManualDone, PNR: true}, []string{ActionAcknowledge}},
		{"idle", State{Name: StateIdle}, nil},
		{"closed", State{Name: StateIdle, Closed: true}, nil},
	} {
		got := ActionsFor(c.s)
		if got == nil {
			t.Errorf("%s: no list at all, and a page wants []", c.name)
		}
		if !slices.Equal(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		for _, a := range got {
			if !isOneOf(ActionNames, a) {
				t.Errorf("%s: %q is not a button", c.name, a)
			}
		}
	}
	// Every state of the list has an answer, and the answer is in the order of the buttons.
	for _, name := range States {
		got := ActionsFor(State{Name: name, PNR: true, Resumable: true, Abortable: true})
		if !slices.IsSortedFunc(got, func(a, b string) int { return slices.Index(ActionNames, a) - slices.Index(ActionNames, b) }) {
			t.Errorf("%s: %v is not in the order of the buttons", name, got)
		}
	}
}

func TestTheTypedWordIsNeededOnlyToCloseAHalfUpgradedMachine(t *testing.T) {
	half := func(name string) State {
		return State{Name: name, PNR: true, HalfUpgraded: true, Resumable: true}
	}
	for _, name := range []string{StateInterrupted, StateStoppedAtWaypoint, StateNeedsAttention, StateManual} {
		if !half(name).NeedsAckWord() {
			t.Errorf("%s: closing a half upgraded machine takes the word", name)
		}
		s := half(name)
		s.HalfUpgraded = false
		// a machine that needs attention may have the sources of the next release in place, with or without the point of no return, and a run taken over from it is the same machine:
		// the click that takes it over is no way round the word
		if want := name == StateNeedsAttention || name == StateManual; s.NeedsAckWord() != want {
			t.Errorf("%s: a machine that is not half upgraded needs the word: %v, want %v", name, s.NeedsAckWord(), want)
		}
		s.PNR = false
		if want := name == StateNeedsAttention || name == StateManual; s.NeedsAckWord() != want {
			t.Errorf("%s: before the point of no return the word is needed: %v, want %v", name, s.NeedsAckWord(), want)
		}
	}
	// No button to close, no word: a failed start after the point of no return is resumed or taken over.
	s := State{Name: StateFailed, Reason: ReasonStart, PNR: true, HalfUpgraded: true, Resumable: true}
	if s.NeedsAckWord() {
		t.Errorf("%+v", s)
	}
	// The word is the one the page shows.
	if AckWord != "I-KNOW-THIS-MACHINE-IS-HALF-UPGRADED" {
		t.Errorf("AckWord = %q", AckWord)
	}
	// And a real half upgraded run says so in its state.
	in := quiet(hopInBlock(t, BlockFull).input(), time.Minute)
	if s := reduced(t, in); !s.NeedsAckWord() {
		t.Errorf("%+v", s)
	}
	in = hopAtReboot(t).input()
	if s := reduced(t, in); s.NeedsAckWord() {
		t.Errorf("a hop that is done up to the restart is not half upgraded: %+v", s)
	}
}

func TestNothingIsUndoneOnceThePackagesAreBeingChanged(t *testing.T) {
	// The files of a rehearsal say that packages are being changed, which a rehearsal does not do: the unit that undoes refuses to touch
	// such a machine, so the core does not start it, whether the rehearsal's time is up or the machine restarted.
	l := rehearsalReady(t)
	l.files.Begun = true
	in := afterLast(l.input(), DefaultTiming.RehearsalTTL)
	s, a := next(t, in)
	is(t, s, StateRehearsed, "")
	wantAction(t, a, Action{Kind: DoNone})
	restarted := l.input()
	restarted.Boot.Now = bootB
	_, a = next(t, restarted)
	wantAction(t, a, Action{Kind: DoNone})
	// Nor for a state that was read from files that said otherwise.
	lost := quiet(hopInBlock(t, BlockDownload).input(), time.Minute)
	lost.Files.Begun = true
	wantAction(t, NextAction(State{Name: StateInterrupted, Cause: CauseKilled}, lost), Action{Kind: DoNone})
}

func TestAMachineThatNeededNoRestartIsCheckedWhenItHasHadTimeEnoughToGetReady(t *testing.T) {
	// It is still in the boot the run began in, so the core has no new boot to note and nothing to count the time from but the unit's word
	// that no restart was needed.
	l := hopLaunched(t)
	for i, b := range BlockList(PhaseRun, Files{}) {
		l.blockStart(1, i+1, b)
		l.blockFiles(b, true)
		l.blockDone(1, i+1, b)
	}
	l.files.BootIDFile = ""
	l.add(Marker{Kind: MkRebootNotNeeded, N: 1, Kernel: "6.1.0-37-amd64"}).end(1, "ok")
	var said time.Time
	for _, m := range l.input().Markers {
		if m.Kind == MkRebootNotNeeded {
			said = m.At
		}
	}
	verify := Action{Kind: DoLaunchVerify, Phase: PhaseVerify, Who: WhoAuto}
	in := l.input()
	for d, want := range map[time.Duration]Action{
		time.Minute: {Kind: DoNone},
		DefaultTiming.ResumeWait - time.Nanosecond: {Kind: DoNone},
		DefaultTiming.ResumeWait:                   verify,
		DefaultTiming.ResumeWait + 24*time.Hour:    verify,
	} {
		in.Now = said.Add(d)
		s, a := next(t, in)
		is(t, s, StateResuming, "")
		if a != want {
			t.Errorf("%v after the word: %+v, want %+v", d, a, want)
		}
	}
	// A machine that is ready is not made to wait, and one that systemd does not answer for is not launched on.
	in.Now = said.Add(time.Minute)
	in.Ready = true
	_, a := next(t, in)
	wantAction(t, a, verify)
	in.Now, in.Ready, in.Unit = said.Add(time.Hour), false, Unit{}
	if got := NextAction(Reduce(in), in); got.Kind == DoLaunchVerify {
		t.Errorf("a launch while systemd does not answer: %+v", got)
	}
}
