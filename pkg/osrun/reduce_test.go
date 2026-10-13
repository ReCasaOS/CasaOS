package osrun

import (
	"strings"
	"testing"
	"time"
)

// reduced reduces an input and holds the answer to what every state must keep.
func reduced(t *testing.T, in Input) State {
	t.Helper()
	s := Reduce(in)
	assertInvariants(t, s)
	return s
}

func is(t *testing.T, s State, name, reasonOrCause string) {
	t.Helper()
	if s.Name != name || (reasonOrCause != "" && s.Reason != reasonOrCause && s.Cause != reasonOrCause) {
		t.Fatalf("state %q (reason %q, cause %q, class %q), want %q %q", s.Name, s.Reason, s.Cause, s.Class, name, reasonOrCause)
	}
}

// Rule 1.
func TestNoRunIsIdle(t *testing.T) {
	l := newLog(t, RunHop)
	full := l.input()
	for name, in := range map[string]Input{
		"no input":                            {},
		"no marker":                           {Unit: Unit{Answered: true}},
		"the queued line only":                {Markers: full.Markers[:1], Now: t0},
		"a log with no queued line":           {Markers: full.Markers[1:], Now: t0},
		"a log that begins with the run line": {Markers: []Marker{full.Markers[1], full.Markers[0]}, Now: t0},
	} {
		s := reduced(t, in)
		if s.Name != StateIdle || s.Busy || s.Closed {
			t.Errorf("%s: %+v", name, s)
		}
	}
}

func TestARunWithNoAttemptIsLostAfterAMoment(t *testing.T) {
	in := newLog(t, RunHop).input()
	in.Now = t0.Add(5 * time.Second)
	is(t, reduced(t, in), StateIdle, "")
	in.Now = t0.Add(30 * time.Second)
	s := reduced(t, in)
	is(t, s, StateFailed, ReasonStart)
	if s.Busy || !s.Reversible {
		t.Errorf("a run that never launched anything changed nothing: %+v", s)
	}
}

// Rule 2.
func TestTheOwnersCloseEndsTheRunWhateverItWasDoing(t *testing.T) {
	for name, l := range map[string]*runLog{
		"while the unit runs":       hopInBlock(t, BlockDownload),
		"half upgraded":             hopInBlock(t, BlockFull),
		"after a success":           hopVerified(t),
		"after a failure to start":  newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto).add(Marker{Kind: MkStartFailed, N: 1}),
		"with a takeover before it": hopInBlock(t, BlockFull).add(Marker{Kind: MkTakeover}),
	} {
		in := running(l.add(Marker{Kind: MkAck, Result: "acknowledge"}).input())
		s := reduced(t, in)
		if s.Name != StateIdle || !s.Closed || s.Busy {
			t.Errorf("%s: %+v", name, s)
		}
	}
	// A close that comes before the run is not one.
	l := newLog(t, RunHop)
	text := l.text.String()
	l.text.Reset()
	l.add(Marker{Kind: MkQueued})
	l.add(Marker{Kind: MkAck, Result: "discard"})
	_ = text
	if s := reduced(t, l.input()); s.Closed {
		t.Errorf("a close with no run to close: %+v", s)
	}
}

// Rule 3.
func TestTheOwnersTakeoverIsTheEndOfTheEngine(t *testing.T) {
	l := hopInBlock(t, BlockFull).add(Marker{Kind: MkTakeover})
	s := reduced(t, running(l.input()))
	is(t, s, StateManual, "")
	if !s.Busy || !s.PNR || !s.HalfUpgraded {
		t.Errorf("a half upgraded machine taken over by hand stays busy: %+v", s)
	}
	// What the unit says after it is not the run's any more.
	l.stop(1, 7, BlockFull, 1, ClassNetwork, true).end(1, "stopped")
	l.attempt(2, PhaseRecover, BlockFull, WhoOwner).start(2, PhaseRecover, bootA)
	is(t, reduced(t, l.input()), StateManual, "")

	l.files.DoneAfter, l.files.DonePrereboot = true, true
	s = reduced(t, l.input())
	is(t, s, StateManualDone, "")
	if s.Busy {
		t.Errorf("the hop was done by hand: %+v", s)
	}
}

// Rule 4.
func TestSystemdThatDoesNotAnswerIsNotWaitedForForEver(t *testing.T) {
	l := hopInBlock(t, BlockDownload)
	in := l.input()
	in.Unit = Unit{}
	for d, want := range map[time.Duration]string{
		time.Minute:                     StateRunning,
		29*time.Minute + 59*time.Second: StateRunning,
		30 * time.Minute:                StateNeedsAttention,
		3 * time.Hour:                   StateNeedsAttention,
	} {
		s := reduced(t, afterLast(in, d))
		if s.Name != want {
			t.Errorf("silent for %v: %q", d, s.Name)
		}
		if want == StateNeedsAttention && (s.Reason != ReasonSystemdSilent || !s.Busy) {
			t.Errorf("silent for %v: %+v", d, s)
		}
	}
	// The unit's own output counts as a sign of life.
	live := quiet(in, time.Hour)
	live.LastActivity = live.Now.Add(-time.Minute)
	is(t, reduced(t, live), StateRunning, "")
}

func TestAnAttemptWithNoUnitAndASystemdThatDoesNotAnswerNeedsAttention(t *testing.T) {
	l := newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto)
	in := l.input()
	in.Unit = Unit{}
	is(t, reduced(t, quiet(in, 10*time.Minute)), StateRunning, "")
	is(t, reduced(t, quiet(in, 30*time.Minute)), StateNeedsAttention, ReasonSystemdSilent)
}

// Rule 5.
func TestARunningUnitIsTheStateOfItsPhase(t *testing.T) {
	recover := hopInBlock(t, BlockMinimal).stop(1, 6, BlockMinimal, 1, ClassNetwork, true).end(1, "stopped")
	recover.attempt(2, PhaseRecover, BlockMinimal, WhoOwner).start(2, PhaseRecover, bootA)
	unwind := hopInBlock(t, BlockSources)
	unwind.attempt(2, PhaseUnwind, "", WhoAuto).start(2, PhaseUnwind, bootA)
	rehearse := newLog(t, RunRehearsal).attempt(1, PhaseRehearse, "", WhoAuto).start(1, PhaseRehearse, bootA)
	refresh := newLog(t, RunRefresh).attempt(1, PhaseRefresh, "", WhoAuto).start(1, PhaseRefresh, bootA)
	for name, c := range map[string]struct {
		l    *runLog
		want string
	}{
		"a hop":       {hopInBlock(t, BlockDownload), StateRunning},
		"a repair":    {recover, StateRecovering},
		"a check":     {hopRestarted(t), StateVerifying},
		"an unwind":   {unwind, StateUnwinding},
		"a rehearsal": {rehearse, StateRehearsing},
		"a refresh":   {refresh, StateRefreshing},
	} {
		in := c.l.input()
		if c.want == StateVerifying {
			in.Boot.Now = bootB
		}
		s := reduced(t, running(in))
		if s.Name != c.want || !s.Busy {
			t.Errorf("%s: %+v", name, s)
		}
		if s.Kind == "" || s.RunID != testRun || s.Hop != "debian-11-12" || s.Attempt != c.l.attemptsWritten() {
			t.Errorf("%s: the run's own words are missing: %+v", name, s)
		}
	}
}

// attemptsWritten is the number of the last attempt in the log, which is the one the state is about.
func (l *runLog) attemptsWritten() int {
	n := 0
	for _, line := range strings.Split(l.text.String(), "\n") {
		if strings.HasPrefix(line, "CASAOS_OSU_ATTEMPT ") {
			n++
		}
	}
	return n
}

func TestARunningStateHasTheBlockThatBeganAndIsAbortableUntilTheMinimalUpgrade(t *testing.T) {
	for _, c := range []struct {
		id        string
		abortable bool
	}{
		{BlockVerify, true}, {BlockTimers, true}, {BlockSnapshot, true}, {BlockSources, true}, {BlockDownload, true},
		{BlockMinimal, false}, {BlockFull, false}, {BlockPrereboot, false},
	} {
		s := reduced(t, running(hopInBlock(t, c.id).input()))
		is(t, s, StateRunning, "")
		if s.Block.ID != c.id || s.Block.Index == 0 {
			t.Errorf("%s: block %+v", c.id, s.Block)
		}
		if s.Abortable != c.abortable {
			t.Errorf("%s: abortable %v", c.id, s.Abortable)
		}
		if got := isOneOf(ActionsFor(s), ActionAbort); got != c.abortable {
			t.Errorf("%s: the abort button is %v", c.id, got)
		}
	}
	// Between two blocks there is none that began and has not ended, and the owner can still call it off.
	l := hopLaunched(t)
	s := reduced(t, running(l.input()))
	if s.Block != (BlockRef{}) || !s.Abortable {
		t.Errorf("before the first block: %+v", s)
	}
	l = hopInBlock(t, BlockDownload)
	l.blockFiles(BlockDownload, true)
	l.blockDone(1, 5, BlockDownload)
	if s := reduced(t, running(l.input())); s.Block != (BlockRef{}) || !s.Abortable {
		t.Errorf("between the download and the minimal upgrade: %+v", s)
	}
	// Once the minimal upgrade has begun it is not, even if it is between blocks.
	l = hopInBlock(t, BlockMinimal)
	l.blockDone(1, 6, BlockMinimal)
	if s := reduced(t, running(l.input())); s.Abortable {
		t.Errorf("after the minimal upgrade: %+v", s)
	}
	// A refresh and a rehearsal can be called off while they run, a repair cannot.
	for _, c := range []struct {
		l    *runLog
		want bool
	}{
		{newLog(t, RunRefresh).attempt(1, PhaseRefresh, "", WhoAuto).start(1, PhaseRefresh, bootA).blockStart(1, 3, BlockRUpgrade), true},
		{newLog(t, RunRehearsal).attempt(1, PhaseRehearse, "", WhoAuto).start(1, PhaseRehearse, bootA).blockStart(1, 5, BlockDownload), true},
	} {
		if s := reduced(t, running(c.l.input())); s.Abortable != c.want {
			t.Errorf("%s: abortable %v", s.Name, s.Abortable)
		}
	}
}

func TestAUnitThatSystemdSaysIsGoneIsQuietBeforeItIsLost(t *testing.T) {
	in := hopInBlock(t, BlockDownload).input()
	is(t, reduced(t, in), StateRunning, "")
	is(t, reduced(t, afterLast(in, 29*time.Second)), StateRunning, "")
	s := reduced(t, afterLast(in, 30*time.Second))
	if s.Name != StateInterrupted {
		t.Errorf("30 seconds after its last word: %+v", s)
	}
	// The unit's output file is a sign of life as well.
	alive := quiet(in, time.Hour)
	alive.LastActivity = alive.Now.Add(-5 * time.Second)
	is(t, reduced(t, alive), StateRunning, "")
}

func TestALastActivityThatIsInTheFutureCountsAsNow(t *testing.T) {
	in := quiet(hopInBlock(t, BlockDownload).input(), time.Hour)
	in.LastActivity = in.Now.Add(24 * time.Hour)
	is(t, reduced(t, in), StateRunning, "")
}

// Rule 5, for a launch that is on its way: the unit has not said it began.
func TestALaunchThatIsOnItsWayIsTheStateOfItsPhase(t *testing.T) {
	for name, c := range map[string]struct {
		l    *runLog
		want string
	}{
		"a hop":       {newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto), StateRunning},
		"a refresh":   {newLog(t, RunRefresh).attempt(1, PhaseRefresh, "", WhoAuto), StateRefreshing},
		"a rehearsal": {newLog(t, RunRehearsal).attempt(1, PhaseRehearse, "", WhoAuto), StateRehearsing},
		"an unwind":   {hopInBlock(t, BlockSources).attempt(2, PhaseUnwind, "", WhoAuto), StateUnwinding},
		"a repair":    {hopInBlock(t, BlockMinimal).stop(1, 6, BlockMinimal, 1, ClassBusy, true).end(1, "stopped").attempt(2, PhaseRecover, BlockMinimal, WhoOwner), StateRecovering},
	} {
		in := c.l.input()
		is(t, reduced(t, quiet(in, 5*time.Second)), c.want, "")
		if s := reduced(t, quiet(in, 5*time.Second)); !s.Busy {
			t.Errorf("%s: %+v", name, s)
		}
	}
	// The check after the restart, on its way, is still the restart's.
	l := hopAtReboot(t).attempt(2, PhaseVerify, "", WhoAuto)
	in := l.input()
	in.Boot.Now = bootB
	is(t, reduced(t, quiet(in, 5*time.Second)), StateResuming, "")
	// ... as is one that is run again after a failed check.
	l = hopVerified(t)
	l.files.DoneAfter = false
	l.attempt(3, PhaseVerify, "", WhoOwner)
	in = l.input()
	in.Boot.Now = bootB
	is(t, reduced(t, quiet(in, 5*time.Second)), StateVerifying, "")
}

// Rule 6.
func TestALaunchThatNeverBeganIsAFailureToStart(t *testing.T) {
	// Before the point of no return nothing was changed, and the failure is not busy.
	l := newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto)
	l.add(Marker{Kind: MkStartFailed, N: 1})
	s := reduced(t, l.input())
	is(t, s, StateFailed, ReasonStart)
	if s.Busy || !s.Reversible || s.Resumable {
		t.Errorf("%+v", s)
	}
	// After it, it is the owner's.
	in := l.input()
	in.Files = Files{Begun: true}
	s = reduced(t, in)
	is(t, s, StateFailed, ReasonStart)
	if !s.Busy || s.Reversible || !s.Resumable {
		t.Errorf("%+v", s)
	}
}

func TestALaunchWithNoWordFromItsUnitIsWaitedForThirtySecondsOrThirtyMinutes(t *testing.T) {
	in := newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto).input()
	is(t, reduced(t, afterLast(in, 29*time.Second)), StateRunning, "")
	is(t, reduced(t, afterLast(in, 30*time.Second)), StateFailed, ReasonStart)

	// If systemd cannot be asked and nothing else shows the unit is there, it is thirty minutes.
	silent := in
	silent.Unit = Unit{}
	silent.LastActivity = silent.Now.Add(-time.Minute)
	is(t, reduced(t, afterLast(silent, 29*time.Minute)), StateRunning, "")
	later := afterLast(silent, 31*time.Minute)
	later.LastActivity = later.Now.Add(-time.Minute)
	is(t, reduced(t, later), StateFailed, ReasonStart)
	// A unit that systemd has active is not given up on.
	later.Unit = Unit{Active: true, Answered: true}
	is(t, reduced(t, later), StateRunning, "")
}

func TestAnUnwindThatNeverBeganIsInterruptedAndThenNeedsAttention(t *testing.T) {
	l := hopInBlock(t, BlockSources)
	l.attempt(2, PhaseUnwind, "", WhoAuto).add(Marker{Kind: MkStartFailed, N: 2})
	is(t, reduced(t, l.input()), StateInterrupted, CauseKilled)
	l.attempt(3, PhaseUnwind, "", WhoAuto).add(Marker{Kind: MkStartFailed, N: 3})
	is(t, reduced(t, l.input()), StateInterrupted, CauseKilled)
	l.attempt(4, PhaseUnwind, "", WhoAuto).add(Marker{Kind: MkStartFailed, N: 4})
	is(t, reduced(t, l.input()), StateNeedsAttention, ReasonUnwindPartial)
}

// Rule 7.
func TestAUnitThatIsGoneWithoutItsEndIsInterrupted(t *testing.T) {
	for name, c := range map[string]struct {
		in    func() Input
		cause string
		unk   bool
	}{
		"killed on the same boot": {func() Input { return hopInBlock(t, BlockDownload).input() }, CauseKilled, false},
		"after a restart": {func() Input {
			in := hopInBlock(t, BlockDownload).input()
			in.Boot.Now = bootB
			return in
		}, CausePower, false},
		"on a boot nobody could read": {func() Input {
			in := hopInBlock(t, BlockDownload).input()
			in.Boot = Boot{}
			return in
		}, CauseKilled, true},
		"from a unit that did not know its boot": {func() Input {
			l := newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto).start(1, PhaseRun, "").blockStart(1, 5, BlockDownload)
			in := l.input()
			in.Boot.Now = bootB
			return in
		}, CauseKilled, true},
	} {
		s := reduced(t, quiet(c.in(), time.Minute))
		if s.Name != StateInterrupted || s.Cause != c.cause || s.BootUnknown != c.unk || !s.Busy {
			t.Errorf("%s: %+v", name, s)
		}
	}
}

func TestAnInterruptedRunIsResumableOnlyAfterThePointOfNoReturn(t *testing.T) {
	in := quiet(hopInBlock(t, BlockDownload).input(), time.Minute)
	if s := reduced(t, in); s.Resumable || s.PNR || !s.Reversible {
		t.Errorf("before: %+v", s)
	}
	in = quiet(hopInBlock(t, BlockFull).input(), time.Minute)
	if s := reduced(t, in); !s.Resumable || !s.PNR || !s.HalfUpgraded || s.Reversible {
		t.Errorf("after: %+v", s)
	}
}

func TestAUnitThatDiedAfterItsLastBlockHasWhatItWasToDo(t *testing.T) {
	// The unit did the last block of the hop and was lost before it said so: the files have it, and the restart is all that is left.
	l := hopLaunched(t)
	for i, b := range BlockList(PhaseRun, Files{}) {
		l.blockStart(1, i+1, b)
		l.blockFiles(b, true)
		l.blockDone(1, i+1, b)
	}
	in := quiet(l.input(), time.Minute)
	s := reduced(t, in)
	is(t, s, StateAwaitingReboot, "")
	if s.HalfUpgraded {
		t.Errorf("the hop is done: %+v", s)
	}
	// The restart already happened: the file has no boot to wait for.
	in.Files.BootIDFile = ""
	is(t, reduced(t, in), StateResuming, "")
	// And on another boot than the one the last block recorded, it is the restart's.
	in.Files.BootIDFile = bootA
	in.Boot.Now = bootB
	is(t, reduced(t, in), StateResuming, "")
	// A unit that did not get that far is interrupted.
	in = quiet(hopInBlock(t, BlockPrereboot).input(), time.Minute)
	is(t, reduced(t, in), StateInterrupted, "")
}

// Rule 8.
func TestAStopBeforeThePointOfNoReturnIsUndoneAndFailed(t *testing.T) {
	l := hopInBlock(t, BlockDownload)
	l.stop(1, 5, BlockDownload, 100, ClassNetwork, false).unwound(1, "ok").end(1, "stopped")
	s := reduced(t, l.input())
	is(t, s, StateFailed, ClassNetwork)
	if s.Class != ClassNetwork || s.Busy || !s.Reversible || s.RC != 100 || s.Block != (BlockRef{Index: 5, ID: BlockDownload}) {
		t.Errorf("%+v", s)
	}
	if !isOneOf(ActionsFor(s), ActionAcknowledge) || !isOneOf(ActionsFor(s), ActionDiscard) || isOneOf(ActionsFor(s), ActionResume) {
		t.Errorf("actions %v", ActionsFor(s))
	}
	// The owner's call to stop is a failure of its own kind.
	l = hopInBlock(t, BlockDownload)
	l.stop(1, 5, BlockDownload, 0, ClassAbort, false).unwound(1, "ok").end(1, "stopped")
	l.files.AbortFlag = true
	is(t, reduced(t, l.input()), StateFailed, ClassAbort)
}

func TestAStopThatCouldNotBeUndoneNeedsAttention(t *testing.T) {
	l := hopInBlock(t, BlockDownload)
	l.stop(1, 5, BlockDownload, 100, ClassNetwork, false).unwound(1, "partial").end(1, "stopped")
	s := reduced(t, l.input())
	is(t, s, StateNeedsAttention, ReasonUnwindPartial)
	if !s.Busy {
		t.Errorf("a machine that is not put back is not free: %+v", s)
	}
}

func TestAStopOfTheDriversOwnChecksChangedNothing(t *testing.T) {
	l := newLog(t, RunHop).attempt(1, PhaseRun, "", WhoAuto).start(1, PhaseRun, bootA)
	l.stop(1, 0, StopDriver, 1, ClassIntegrity, false).end(1, "stopped")
	s := reduced(t, l.input())
	is(t, s, StateFailed, ClassIntegrity)
	if s.Busy || !s.Reversible {
		t.Errorf("%+v", s)
	}
}

func TestAUnitThatEndedAfterAStopWithoutUndoingIsInterrupted(t *testing.T) {
	// The driver died between its stop and its unwind, or the files were put right by hand: the unwind is still to do.
	l := hopInBlock(t, BlockDownload).stop(1, 5, BlockDownload, 100, ClassNetwork, false).end(1, "stopped")
	is(t, reduced(t, l.input()), StateInterrupted, CauseKilled)
}

func TestAStopThatThePointOfNoReturnOvertookIsTheNewStates(t *testing.T) {
	// The driver wrote that the point was not passed, and the files say it was: the files are the truth.
	l := hopInBlock(t, BlockMinimal).stop(1, 6, BlockMinimal, 100, ClassAptFailed, false).end(1, "stopped")
	l.files.Begun = true
	is(t, reduced(t, l.input()), StateStoppedAtWaypoint, "")
	// And the other way round: it wrote that it was, and the files say that nothing was changed (the block took its marker back).
	l = hopInBlock(t, BlockMinimal).stop(1, 6, BlockMinimal, 100, ClassBusy, true).end(1, "stopped")
	l.files = Files{DoneTimers: true, AptOrig: true}
	is(t, reduced(t, l.input()), StateInterrupted, CauseKilled)
}

// Rule 9.
func TestAStopAfterThePointOfNoReturnWaitsAtTheWaypoint(t *testing.T) {
	for _, class := range Classes {
		l := hopInBlock(t, BlockFull)
		l.stop(1, 7, BlockFull, 100, class, true).end(1, "stopped")
		s := reduced(t, l.input())
		is(t, s, StateStoppedAtWaypoint, "")
		wantClass := class
		if class == ClassPlan {
			wantClass = ClassGuard
		}
		if s.Class != wantClass || s.RC != 100 || s.Block != (BlockRef{Index: 7, ID: BlockFull}) || !s.Busy || !s.PNR || !s.HalfUpgraded || s.Reversible {
			t.Errorf("%s: %+v", class, s)
		}
		if want := ClassBehavior(wantClass).Resumable; s.Resumable != want || isOneOf(ActionsFor(s), ActionResume) != want {
			t.Errorf("%s: resumable %v, actions %v", class, s.Resumable, ActionsFor(s))
		}
	}
}

func TestAPlanThatStopsAfterThePointOfNoReturnIsTheGuards(t *testing.T) {
	l := hopInBlock(t, BlockFull).stop(1, 7, BlockFull, 1, ClassPlan, true).end(1, "stopped")
	s := reduced(t, l.input())
	if s.Class != ClassGuard || s.Resumable {
		t.Errorf("a stop of the plan after packages changed is the guard's, and cannot be resumed: %+v", s)
	}
	// Before the point of no return it is the plan's, and is undone.
	l = hopInBlock(t, BlockDownload).stop(1, 5, BlockDownload, 1, ClassPlan, false).unwound(1, "ok").end(1, "stopped")
	is(t, reduced(t, l.input()), StateFailed, ClassPlan)
}

func TestAStopOfTheCheckAfterTheRestartIsFailedVerify(t *testing.T) {
	l := hopRestarted(t)
	l.blockStart(2, 1, BlockAfter).stop(2, 1, BlockAfter, 1, ClassBarrierKernel, true).end(2, "stopped")
	s := reduced(t, l.input())
	is(t, s, StateFailed, ReasonVerify)
	if !s.Busy || s.HalfUpgraded || !s.Resumable || s.Class != ClassBarrierKernel {
		t.Errorf("%+v", s)
	}
	if got := ActionsFor(s); len(got) != 3 || got[0] != ActionResume || got[1] != ActionAcknowledge || got[2] != ActionTakeover {
		t.Errorf("actions %v", got)
	}
}

func TestARepairThatIsTriedAndStopsAgainIsHalfDoneOnlyWithAutomaticRepairs(t *testing.T) {
	build := func(who string, n int) Input {
		l := hopInBlock(t, BlockMinimal).stop(1, 6, BlockMinimal, 1, ClassNetwork, true).end(1, "stopped")
		for i := 0; i < n; i++ {
			l.attempt(2+i, PhaseRecover, BlockMinimal, who).start(2+i, PhaseRecover, bootA)
			l.blockStart(2+i, 2, BlockMinimal).stop(2+i, 2, BlockMinimal, 1, ClassNetwork, true).end(2+i, "stopped")
		}
		return l.input()
	}
	// The shipped timing never repairs by itself and so never gives up.
	is(t, reduced(t, build(WhoAuto, 5)), StateStoppedAtWaypoint, "")

	tm := DefaultTiming
	tm.AutoRecoverTries = 2
	for _, c := range []struct {
		who  string
		n    int
		want string
	}{
		{WhoAuto, 0, StateStoppedAtWaypoint}, {WhoAuto, 1, StateStoppedAtWaypoint}, {WhoAuto, 2, StateFailed},
		// The owner's repair gives the core its tries back.
		{WhoOwner, 5, StateStoppedAtWaypoint},
	} {
		in := build(c.who, c.n)
		in.Timing = tm
		s := reduced(t, in)
		if s.Name != c.want {
			t.Errorf("%d repairs by %s: %+v", c.n, c.who, s)
		}
		if s.Name == StateFailed && (s.Reason != ReasonHalf || !s.Busy || !s.Resumable) {
			t.Errorf("%+v", s)
		}
	}
	// A class that is never repaired by itself is not given up on.
	l := hopInBlock(t, BlockMinimal).stop(1, 6, BlockMinimal, 1, ClassDisk, true).end(1, "stopped")
	for i := 0; i < 3; i++ {
		l.attempt(2+i, PhaseRecover, BlockMinimal, WhoAuto).start(2+i, PhaseRecover, bootA)
		l.stop(2+i, 2, BlockMinimal, 1, ClassDisk, true).end(2+i, "stopped")
	}
	in := l.input()
	in.Timing = tm
	is(t, reduced(t, in), StateStoppedAtWaypoint, "")
}

// Rule 10.
func TestAnUnwindThatEndedPutsTheRunBackToFailed(t *testing.T) {
	killed := func() *runLog {
		l := hopInBlock(t, BlockDownload)
		l.wait(time.Minute)
		return l
	}
	unwind := func(l *runLog, boot string) *runLog {
		l.attempt(2, PhaseUnwind, "", WhoAuto).start(2, PhaseUnwind, boot).unwound(2, "ok").end(2, "ok")
		return l
	}
	s := reduced(t, unwind(killed(), bootA).input())
	is(t, s, StateFailed, ReasonInterruptedKilled)
	if s.Busy || !s.Reversible {
		t.Errorf("%+v", s)
	}
	// The unit that did the unwind began in another boot than the one that was lost: the power went.
	in := unwind(killed(), bootB).input()
	in.Boot.Now = bootB
	is(t, reduced(t, in), StateFailed, ReasonInterruptedPower)

	// A stop of the run, before the unit was lost, is the reason.
	l := hopInBlock(t, BlockDownload).stop(1, 5, BlockDownload, 1, ClassDisk, false).end(1, "stopped")
	is(t, reduced(t, unwind(l, bootA).input()), StateFailed, ClassDisk)

	// The owner's call to stop, when the unit was stopped without a word of its own.
	l = unwind(killed(), bootA)
	l.files.AbortFlag = true
	is(t, reduced(t, l.input()), StateFailed, ClassAbort)
}

func TestARehearsalThatWasUndoneIsDone(t *testing.T) {
	l := rehearsalReady(t)
	l.attempt(2, PhaseUnwind, "", WhoAuto).start(2, PhaseUnwind, bootA).unwound(2, "ok").end(2, "ok")
	s := reduced(t, l.input())
	is(t, s, StateRehearsalDone, "")
	if s.Busy {
		t.Errorf("the sources are back: %+v", s)
	}
	// One that did not reach its end is a failure like any other.
	l = newLog(t, RunRehearsal).attempt(1, PhaseRehearse, "", WhoAuto).start(1, PhaseRehearse, bootA).blockStart(1, 3, BlockSnapshot)
	l.attempt(2, PhaseUnwind, "", WhoAuto).start(2, PhaseUnwind, bootA).unwound(2, "ok").end(2, "ok")
	is(t, reduced(t, l.input()), StateFailed, ReasonInterruptedKilled)
}

func TestAnUnwindThatPutBackOnlyPartIsStillNeedingAttention(t *testing.T) {
	l := rehearsalReady(t)
	l.attempt(2, PhaseUnwind, "", WhoAuto).start(2, PhaseUnwind, bootA).unwound(2, "partial").end(2, "ok")
	s := reduced(t, l.input())
	is(t, s, StateNeedsAttention, ReasonUnwindPartial)
	if !s.Busy || s.Name == StateFailed {
		t.Errorf("%+v", s)
	}
}

func TestAnUnwindThatSaidNothingIsTriedAgainAFewTimes(t *testing.T) {
	l := hopInBlock(t, BlockDownload).wait(time.Minute)
	for n := 2; n <= 4; n++ {
		l.attempt(n, PhaseUnwind, "", WhoAuto).start(n, PhaseUnwind, bootA).end(n, "ok")
		s := reduced(t, l.input())
		if n < 4 {
			is(t, s, StateInterrupted, CauseKilled)
		} else {
			is(t, s, StateNeedsAttention, ReasonUnwindPartial)
		}
	}
	// An unwind that was killed has the same fate.
	l = hopInBlock(t, BlockDownload).wait(time.Minute)
	for n := 2; n <= 4; n++ {
		l.attempt(n, PhaseUnwind, "", WhoAuto).start(n, PhaseUnwind, bootA)
	}
	in := quiet(l.input(), time.Minute)
	is(t, reduced(t, in), StateNeedsAttention, ReasonUnwindPartial)
}

func TestAnUnwindThatFoundThePointOfNoReturnPassedStopsAtTheWaypoint(t *testing.T) {
	l := hopInBlock(t, BlockFull).wait(time.Minute)
	l.attempt(2, PhaseUnwind, "", WhoAuto).start(2, PhaseUnwind, bootA).stop(2, 0, StopUnwind, 1, ClassGuard, true).end(2, "stopped")
	s := reduced(t, l.input())
	is(t, s, StateStoppedAtWaypoint, "")
	if s.Resumable {
		t.Errorf("%+v", s)
	}
}

// Rule 11.
func TestARefreshThatEndedIsRefreshedOrWaitsForItsRestart(t *testing.T) {
	is(t, reduced(t, refreshDone(t, false).input()), StateRefreshed, "")
	s := reduced(t, refreshDone(t, true).input())
	is(t, s, StateRebootFirst, "")
	if !s.Busy {
		t.Errorf("a machine that wants its restart is still busy: %+v", s)
	}
	in := refreshDone(t, true).input()
	in.Boot.Now = bootB
	s = reduced(t, in)
	is(t, s, StateRefreshed, "")
	if s.Busy {
		t.Errorf("%+v", s)
	}
	in.Boot = Boot{}
	s = reduced(t, in)
	is(t, s, StateRebootFirst, "")
	if !s.BootUnknown {
		t.Errorf("nothing is inferred from a boot id that cannot be read: %+v", s)
	}
}

// Rule 12.
func TestARehearsalThatEndedIsRehearsedUntilItIsUndone(t *testing.T) {
	l := rehearsalReady(t)
	s := reduced(t, l.input())
	is(t, s, StateRehearsed, "")
	ready := time.Time{}
	for _, m := range l.input().Markers {
		if m.Kind == MkRehearsalReady {
			ready = m.At
		}
	}
	if !s.Busy || s.ExpiresAt == nil || !s.ExpiresAt.Equal(ready.Add(30*time.Minute)) {
		t.Errorf("%+v", s)
	}
	if got := ActionsFor(s); len(got) != 1 || got[0] != ActionDiscard {
		t.Errorf("actions %v", got)
	}
}

// Rule 13.
func TestAMachineThatIsDoneWaitsForItsRestart(t *testing.T) {
	l := hopAtReboot(t)
	s := reduced(t, l.input())
	is(t, s, StateAwaitingReboot, "")
	if !s.Busy || s.HalfUpgraded || !s.PNR || s.Resumable || s.Stalled {
		t.Errorf("%+v", s)
	}
	// Until the checks before the restart are computed and green it is not offered.
	if !s.RebootBlocked || isOneOf(ActionsFor(s), ActionReboot) {
		t.Errorf("reboot offered with no barrier: %v", ActionsFor(s))
	}
	in := l.input()
	in.Barrier = &Barrier{OK: false, Checks: []BarrierCheck{{Code: "fstab_unresolved", OK: false, Severity: "error"}}}
	if s := reduced(t, in); !s.RebootBlocked || isOneOf(ActionsFor(s), ActionReboot) {
		t.Errorf("reboot offered with a red barrier: %v", ActionsFor(s))
	}
	in.Barrier = &Barrier{OK: true}
	s = reduced(t, in)
	if s.RebootBlocked || !isOneOf(ActionsFor(s), ActionReboot) || !isOneOf(ActionsFor(s), ActionTakeover) {
		t.Errorf("reboot not offered with a green barrier: %v", ActionsFor(s))
	}
}

func TestARestartThatWasAskedForIsRestartingThenStalled(t *testing.T) {
	l := hopAtReboot(t)
	l.add(Marker{Kind: MkRebootRequested})
	in := l.input()
	s := reduced(t, quiet(in, 14*time.Minute))
	is(t, s, StateRebooting, "")
	if len(ActionsFor(s)) != 0 || s.Stalled {
		t.Errorf("%+v", s)
	}
	s = reduced(t, quiet(in, 15*time.Minute+2*time.Second))
	is(t, s, StateRebootStalled, "")
	if !s.Stalled || !s.Busy {
		t.Errorf("%+v", s)
	}
	in.Barrier = &Barrier{OK: true}
	if !isOneOf(ActionsFor(reduced(t, quiet(in, time.Hour))), ActionReboot) {
		t.Error("a restart that did not happen can be asked for again")
	}
	// A request that failed is not one.
	l.add(Marker{Kind: MkRebootFailed})
	is(t, reduced(t, quiet(l.input(), time.Hour)), StateAwaitingReboot, "")
	// ... and a later one is.
	l.add(Marker{Kind: MkRebootRequested})
	is(t, reduced(t, l.input()), StateRebooting, "")
}

func TestAMachineThatStaysOnItsBootIsNeverGivenUpOn(t *testing.T) {
	// A restart that was asked for and does not come is stalled, and stays so until the boot changes: nothing else ends it.
	l := hopAtReboot(t).add(Marker{Kind: MkRebootRequested})
	for _, d := range []time.Duration{16 * time.Minute, 5 * time.Hour, 30 * 24 * time.Hour} {
		s := reduced(t, afterLast(l.input(), d))
		is(t, s, StateRebootStalled, "")
		if !s.Stalled || !s.Busy || !s.PNR {
			t.Errorf("after %v: %+v", d, s)
		}
	}
}

func TestADiskSnapshotThatIsRestoredIsAnotherBootOfTheSameFiles(t *testing.T) {
	// The files are those of the moment the snapshot was taken, and the boot is new: the restart is read, only earlier than it was.
	in := hopAtReboot(t).input()
	in.Boot.Now = bootC
	s, a := next(t, in)
	is(t, s, StateResuming, "")
	wantAction(t, a, Action{Kind: DoNone})
	in.Ready = true
	_, a = next(t, in)
	wantAction(t, a, Action{Kind: DoLaunchVerify, Phase: PhaseVerify, Who: WhoAuto})
}

func TestAnotherBootIsTheRestartWhoeverMadeIt(t *testing.T) {
	// Asked for, not asked for, stalled: another boot id is what ends the wait.
	l := hopAtReboot(t)
	in := l.input()
	in.Boot.Now = bootB
	is(t, reduced(t, in), StateResuming, "")
	l.add(Marker{Kind: MkRebootRequested})
	in = l.input()
	in.Boot.Now = bootB
	is(t, reduced(t, in), StateResuming, "")
	is(t, reduced(t, quiet(in, time.Hour)), StateResuming, "")
}

func TestABootIdThatCannotBeReadInfersNothing(t *testing.T) {
	l := hopAtReboot(t)
	in := l.input()
	in.Boot = Boot{}
	s := reduced(t, in)
	is(t, s, StateAwaitingReboot, "")
	if !s.BootUnknown {
		t.Errorf("%+v", s)
	}
	l.add(Marker{Kind: MkRebootRequested})
	in = l.input()
	in.Boot = Boot{}
	s = reduced(t, in)
	is(t, s, StateRebooting, "")
	if !s.BootUnknown {
		t.Errorf("%+v", s)
	}
	is(t, reduced(t, quiet(in, time.Hour)), StateRebootStalled, "")
	// A restart that the unit could not name is not one that anyone can see.
	l = hopAtReboot(t)
	in = l.input()
	for i, m := range in.Markers {
		if m.Kind == MkRebootPending {
			in.Markers[i].Boot = ""
		}
	}
	in.Boot.Now = bootB
	if s := reduced(t, in); s.Name != StateAwaitingReboot || !s.BootUnknown {
		t.Errorf("%+v", s)
	}
}

func TestTheStateOfAMachineThatWaitsForItsRestartTakesTheTimeItBegan(t *testing.T) {
	l := hopAtReboot(t)
	var pending time.Time
	for _, m := range l.input().Markers {
		if m.Kind == MkRebootPending {
			pending = m.At
		}
	}
	if s := reduced(t, l.input()); !s.Since.Equal(pending) {
		t.Errorf("since %v, want %v", s.Since, pending)
	}
}

// Rule 14.
func TestARestartThatWasNotNeededGoesStraightOn(t *testing.T) {
	l := hopLaunched(t)
	for i, b := range BlockList(PhaseRun, Files{}) {
		l.blockStart(1, i+1, b)
		l.blockFiles(b, true)
		l.blockDone(1, i+1, b)
	}
	l.files.BootIDFile = ""
	l.add(Marker{Kind: MkRebootNotNeeded, N: 1, Kernel: "6.1.0-37-amd64"}).end(1, "ok")
	s := reduced(t, l.input())
	is(t, s, StateResuming, "")
	if !s.Busy {
		t.Errorf("%+v", s)
	}
}

// Rule 15.
func TestTheCheckAfterTheRestartIsJudged(t *testing.T) {
	l := hopVerified(t)
	in := l.input()
	in.Boot.Now = bootB
	s := reduced(t, in)
	is(t, s, StateVerifying, "")
	if !s.Busy {
		t.Errorf("a machine that is not judged yet is busy: %+v", s)
	}
	for name, c := range map[string]struct {
		v    VerifyResult
		want string
		busy bool
	}{
		"nothing found": {VerifyResult{}, StateSucceeded, false},
		"a warning":     {VerifyResult{Warnings: 2}, StateSucceededWithFindings, false},
		"an error":      {VerifyResult{Errors: 1}, StateFailed, true},
		"both":          {VerifyResult{Errors: 1, Warnings: 3}, StateFailed, true},
	} {
		v := c.v
		in.Verify = &v
		s := reduced(t, in)
		if s.Name != c.want || s.Busy != c.busy {
			t.Errorf("%s: %+v", name, s)
		}
		if c.want == StateFailed && (s.Reason != ReasonVerify || !s.Resumable || s.HalfUpgraded) {
			t.Errorf("%s: %+v", name, s)
		}
	}
}

func TestTheFileOfTheLastBlockWinsOverTheMarkers(t *testing.T) {
	// The unit did block 8 and was lost before it said so: the file is there, and the check is done.
	l := hopRestarted(t)
	l.blockStart(2, 1, BlockAfter)
	l.files.DoneAfter = true
	in := quiet(l.input(), time.Minute)
	in.Boot.Now = bootB
	is(t, reduced(t, in), StateVerifying, "")
	in.Verify = &VerifyResult{}
	is(t, reduced(t, in), StateSucceeded, "")
	// A unit that did not get to it is interrupted, and runs the check again.
	l = hopRestarted(t)
	l.blockStart(2, 1, BlockAfter)
	in = quiet(l.input(), time.Minute)
	in.Boot.Now = bootB
	is(t, reduced(t, in), StateInterrupted, "")
}

func TestAJudgmentIsKeptOnlyAsLongAsTheCheckIsTheLatest(t *testing.T) {
	// A check that is run again after a failure is not judged by the last verdict.
	l := hopVerified(t)
	l.attempt(3, PhaseVerify, "", WhoOwner).start(3, PhaseVerify, bootB)
	in := running(l.input())
	in.Boot.Now = bootB
	in.Verify = &VerifyResult{Errors: 1}
	is(t, reduced(t, in), StateVerifying, "")
}

func TestARunThatCannotBeTrustedNeedsAttention(t *testing.T) {
	in := hopInBlock(t, BlockDownload).input()
	in.Damaged = true
	s := reduced(t, running(in))
	is(t, s, StateNeedsAttention, ReasonRunDamaged)
	if !s.Busy {
		t.Errorf("%+v", s)
	}
	// The owner's word is still the last.
	l := hopInBlock(t, BlockDownload).add(Marker{Kind: MkTakeover})
	in = l.input()
	in.Damaged = true
	is(t, reduced(t, in), StateManual, "")
	in = l.add(Marker{Kind: MkAck, Result: "acknowledge"}).input()
	in.Damaged = true
	is(t, reduced(t, in), StateIdle, "")
}

func TestAnAttemptThatEndedAndSaidNothingIsNotMadeUp(t *testing.T) {
	l := hopInBlock(t, BlockDownload).end(1, "ok")
	is(t, reduced(t, l.input()), StateNeedsAttention, ReasonRunDamaged)
	// ... except the check, which is the restart's again.
	l = hopRestarted(t).end(2, "ok")
	in := l.input()
	in.Boot.Now = bootB
	is(t, reduced(t, in), StateResuming, "")
}

func TestTheTimingOfAnInputThatHasNoneIsTheShippedOne(t *testing.T) {
	in := hopInBlock(t, BlockDownload).input()
	in.Timing = Timing{}
	zero := reduced(t, quiet(in, 20*time.Second))
	in.Timing = DefaultTiming
	if def := reduced(t, quiet(in, 20*time.Second)); def != zero {
		t.Errorf("%+v is not %+v", zero, def)
	}
	is(t, zero, StateRunning, "")
	// And a timing that is given is the one that counts.
	in.Timing.GraceQuiet = 5 * time.Second
	is(t, reduced(t, quiet(in, 20*time.Second)), StateInterrupted, "")
}
