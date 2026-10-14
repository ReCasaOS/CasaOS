package osrun

import (
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A sim makes a run happen the way the core and a unit would make it, at random: the unit writes its markers and leaves
// the files its blocks leave, it is stopped, killed or the power goes, and the core does whatever NextAction says, and the
// owner whatever the buttons allow. After every marker, wait and file the run is reduced, and what the reducer says is held to
// what must be true of any run.

// simRuns and simSeed are what the test runs with; OSRUN_SIM_RUNS and OSRUN_SIM_SEED make it go further when a rule is changed.
func simParams() (runs int, seed uint64) {
	runs, seed = 2500, 20261013
	if n, err := strconv.Atoi(os.Getenv("OSRUN_SIM_RUNS")); err == nil && n > 0 {
		runs = n
	}
	if n, err := strconv.ParseUint(os.Getenv("OSRUN_SIM_SEED"), 10, 64); err == nil {
		seed = n
	}
	return runs, seed
}

type sim struct {
	t       *testing.T
	rng     *rand.Rand
	kind    string
	ms      []Marker
	clock   time.Time
	files   Files
	unit    Unit
	boot    Boot
	verify  *VerifyResult
	barrier *Barrier
	ready   bool
	seen    time.Time
	remind  int
	seenID  string
	done    bool
	prev    *State
	log     []string
	snaps   int
	sealed  int
	cover   map[string]int
	// silentSince and silentUntil are a stretch in which systemd does not answer: the reducer is asked with a unit that does not,
	// and says since when. unplaced is a run whose log has no RUN line: nothing places it, whatever the machine does.
	silentSince, silentUntil time.Time
	unplaced                 bool
}

func newSim(t *testing.T, rng *rand.Rand, kind string) *sim {
	s := &sim{t: t, rng: rng, kind: kind, clock: t0, unit: Unit{Answered: true}, boot: Boot{Now: bootA, OK: true}, seenID: bootA}
	s.write(Marker{Kind: MkQueued})
	if rng.IntN(40) == 0 {
		s.unplaced = true
		return s
	}
	s.write(Marker{Kind: MkRun, RunID: testRun, Hop: "debian-11-12", PlanID: "p1-0123456789abcdef0123456789abcdef", RunKind: kind, Boot: bootA})
	return s
}

// silent says whether systemd does not answer now.
func (s *sim) silent() bool { return s.clock.Before(s.silentUntil) }

// maybeSilence begins a stretch in which systemd does not answer, now and then, for a minute to a couple of hours.
func (s *sim) maybeSilence() {
	if !s.silent() && s.rng.IntN(30) == 0 {
		s.silentSince = s.clock
		s.silentUntil = s.clock.Add(time.Duration(1+s.rng.IntN(150)) * time.Minute)
	}
}

// unitNow is what systemd says now.
func (s *sim) unitNow() Unit {
	if s.silent() {
		return Unit{SilentSince: s.silentSince}
	}
	return s.unit
}

func (s *sim) write(m Marker) {
	if m.At.IsZero() {
		m.At = s.clock
	}
	s.clock = m.At.Add(time.Second)
	line, err := m.Line(testNonce)
	if err != nil {
		s.t.Fatalf("%+v: %v", m, err)
	}
	read, _, ok := parseMarker(strings.TrimSuffix(line, "\n"))
	if !ok {
		s.t.Fatalf("%q does not read back", line)
	}
	s.ms = append(s.ms, read)
	s.log = append(s.log, strings.TrimSuffix(line, "\n"))
}

func (s *sim) wait(d time.Duration) { s.clock = s.clock.Add(d) }

func (s *sim) input() Input {
	return Input{
		Markers: s.ms[:len(s.ms):len(s.ms)], Files: s.files, Unit: s.unitNow(), Boot: s.boot, Now: s.clock,
		Verify: s.verify, Barrier: s.barrier, Ready: s.ready, BootSeenAt: s.seen, Reminded: s.remind,
	}
}

func cloneInput(in Input) Input {
	out := in
	out.Markers = slices.Clone(in.Markers)
	for i := range out.Markers {
		out.Markers[i].At = in.Markers[i].At
	}
	if in.Verify != nil {
		v := *in.Verify
		out.Verify = &v
	}
	if in.Barrier != nil {
		b := *in.Barrier
		b.Checks = slices.Clone(in.Barrier.Checks)
		out.Barrier = &b
	}
	return out
}

func (s *sim) fail(format string, args ...any) {
	s.t.Helper()
	s.t.Fatalf("%s\nthe log:\n  %s\nfiles %+v unit %+v boot %+v now %v", fmt.Sprintf(format, args...), strings.Join(s.log, "\n  "), s.files, s.unit, s.boot, s.clock.Sub(t0))
}

// legal says whether a run can go from one state to the next between two readings of it.
func legal(prev, cur State) bool {
	if prev.Name == cur.Name {
		return true
	}
	to := func(names ...string) bool { return slices.Contains(names, cur.Name) }
	if cur.Name == StateManual || cur.Name == StateManualDone {
		// The owner took over, from any run that was not already decided.
		return !decided(prev) || prev.Name == StateManual
	}
	if cur.Name == StateIdle {
		// The owner's close, or the run that never got its first attempt.
		return cur.Closed || prev.Name == StateIdle
	}
	switch prev.Name {
	case StateIdle:
		return to(StateRefreshing, StateRehearsing, StateRunning, StateFailed)
	case StateRefreshing:
		return to(StateRefreshed, StateRebootFirst, StateInterrupted, StateFailed, StateNeedsAttention, StateManual)
	case StateRehearsing:
		return to(StateRehearsed, StateInterrupted, StateFailed, StateNeedsAttention, StateManual)
	case StateRunning:
		return to(StateAwaitingReboot, StateResuming, StateInterrupted, StateStoppedAtWaypoint, StateFailed, StateNeedsAttention, StateManual)
	case StateUnwinding:
		return to(StateFailed, StateRehearsalDone, StateInterrupted, StateStoppedAtWaypoint, StateNeedsAttention, StateManual)
	case StateRecovering:
		return to(StateAwaitingReboot, StateResuming, StateInterrupted, StateStoppedAtWaypoint, StateFailed, StateNeedsAttention, StateManual)
	case StateVerifying:
		return to(StateSucceeded, StateSucceededWithFindings, StateInterrupted, StateFailed, StateNeedsAttention, StateManual, StateResuming)
	case StateRehearsed:
		return to(StateUnwinding, StateInterrupted, StateFailed, StateManual)
	case StateRebootFirst:
		return to(StateRefreshed, StateManual)
	case StateAwaitingReboot, StateRebooting, StateRebootStalled:
		return to(StateAwaitingReboot, StateRebooting, StateRebootStalled, StateResuming, StateManual)
	case StateResuming:
		return to(StateVerifying, StateInterrupted, StateFailed, StateNeedsAttention, StateManual, StateSucceeded, StateSucceededWithFindings)
	case StateInterrupted:
		// Undone by the core (before the point of no return), repaired by the owner, checked again, or let go of: not a state that a
		// run reaches the end of by itself.
		return to(StateUnwinding, StateRecovering, StateVerifying, StateResuming, StateFailed, StateNeedsAttention, StateManual)
	case StateStoppedAtWaypoint:
		return to(StateRecovering, StateVerifying, StateInterrupted, StateFailed, StateNeedsAttention, StateManual, StateAwaitingReboot, StateResuming)
	case StateFailed:
		if !prev.PNR {
			// Failed with nothing changed: only the owner's close, or the start that is waited for.
			return prev.Reason == ReasonStart && to(StateRunning, StateRefreshing, StateRehearsing, StateFailed, StateInterrupted)
		}
		return to(StateRecovering, StateVerifying, StateResuming, StateManual, StateInterrupted, StateStoppedAtWaypoint, StateNeedsAttention, StateSucceeded, StateSucceededWithFindings)
	case StateNeedsAttention:
		return to(StateManual, StateFailed, StateInterrupted, StateRunning, StateRehearsing, StateRefreshing, StateUnwinding, StateRecovering, StateVerifying)
	case StateManual:
		return to(StateManualDone)
	}
	// What has ended, and the owner's close.
	return false
}

// decided is whether a state is one that the log decides for good.
func decided(st State) bool {
	switch st.Name {
	case StateSucceeded, StateSucceededWithFindings, StateRefreshed, StateRehearsalDone, StateManual, StateManualDone:
		return true
	case StateFailed:
		return !st.PNR && st.Reason != ReasonStart
	}
	return false
}

// snap reduces the run as it stands and holds the answer to what is true of every run.
func (s *sim) snap() (Input, State) {
	s.t.Helper()
	s.snaps++
	s.maybeSilence()
	in := s.input()
	st := Reduce(in)
	if s.cover != nil {
		s.cover[st.Name]++
	}
	act := NextAction(st, in)

	// The answer is a state, and a state is consistent.
	if !slices.Contains(States, st.Name) {
		s.fail("state %q", st.Name)
	}
	if st.Reversible == st.PNR {
		s.fail("P5: reversible %v and pnr %v in %+v", st.Reversible, st.PNR, st)
	}
	if st.PNR != in.Files.PNR() {
		s.fail("the point of no return of %+v is not the files'", st)
	}
	if st.Busy != busyOf(st.Name, st.PNR) {
		s.fail("busy %v in %+v", st.Busy, st)
	}
	if st.PNR && !in.Files.DoneAfter && !st.Closed && !st.Busy {
		s.fail("P4: a machine past the point of no return, not done, is not busy: %+v", st)
	}
	if msg := busyWhereItHasToBe(in, st); msg != "" {
		s.fail("P6: %s: %+v", msg, st)
	}
	if st.HalfUpgraded != (st.PNR && !in.Files.DonePrereboot) {
		s.fail("half upgraded %v in %+v", st.HalfUpgraded, st)
	}
	if !s.unplaced && st.Name == StateIdle && st.Closed != (len(s.log) > 0 && strings.Contains(strings.Join(s.log, "\n"), "_ACK ")) {
		s.fail("closed %v in %+v", st.Closed, st)
	}
	buttons := ActionsFor(st)
	for _, b := range buttons {
		switch {
		case b == ActionResume && !st.Resumable, b == ActionAbort && (!st.Abortable || st.PNR), b == ActionReboot && st.RebootBlocked:
			s.fail("button %s in %+v", b, st)
		}
	}
	if st.NeedsAckWord() && !slices.Contains(buttons, ActionAcknowledge) {
		s.fail("a word for a button that is not there: %+v", st)
	}

	// Pure: the same answer from a copy, and the input is not touched.
	if s.snaps%7 == 0 {
		cp := cloneInput(in)
		if again := Reduce(cp); !reflect.DeepEqual(again, st) || NextAction(again, cp) != act {
			s.fail("P3: %+v then %+v", st, again)
		}
		if !reflect.DeepEqual(cp, cloneInput(in)) || len(in.Markers) != len(s.ms) {
			s.fail("P3: the input was changed")
		}
	}

	// A machine that is held, with nothing that runs, and a systemd that has been silent for as long as the bound says, is asked
	// about: it is not left to wait for a systemd that does not come back.
	if s.silent() && s.clock.Sub(s.silentSince) >= DefaultTiming.SystemdSilence && st.Busy && !s.unplaced {
		var last time.Time
		for _, m := range s.ms {
			if m.At.After(last) {
				last = m.At
			}
		}
		if s.clock.Sub(last) >= DefaultTiming.SystemdSilence && st.Name != StateRebooting && st.Name != StateManual && st.Name != StateNeedsAttention {
			s.fail("P7: systemd silent for %v and the run is %+v", s.clock.Sub(s.silentSince), st)
		}
	}

	// What it asks the core to do is allowed.
	if act.Kind == DoJudge && !in.Unit.Answered {
		s.fail("a verdict while systemd does not answer: %+v", act)
	}
	switch act.Kind {
	case DoUnwind, DoLaunchVerify, DoLaunchRecover:
		if !in.Unit.Answered || in.Unit.Active {
			s.fail("a launch with unit %+v: %+v", in.Unit, act)
		}
		switch st.Name {
		case StateRefreshing, StateRehearsing, StateRunning, StateRecovering, StateUnwinding, StateManual, StateManualDone:
			s.fail("a launch in %s: %+v", st.Name, act)
		}
		if act.Who != WhoAuto || !ValidPhase(act.Phase) {
			s.fail("a launch with %+v", act)
		}
	}

	// A systemd that does not answer is a reading, not a step, and the steps that the run takes meanwhile are not seen: the first
	// reading after it is not compared with the one before.
	if s.silent() {
		s.prev = nil
	} else if !s.unplaced {
		if s.prev != nil && !legal(*s.prev, st) {
			s.fail("P2: %s (%s %s %s) to %s (%s %s %s) is not a step a run can take", s.prev.Name, s.prev.Reason, s.prev.Cause, s.prev.Class, st.Name, st.Reason, st.Cause, st.Class)
		}
		cur := st
		s.prev = &cur
	}

	// P1: once a run is decided, nothing that is written after it moves it.
	if decided(st) && (s.sealed < 3 || s.rng.IntN(9) == 0) {
		s.sealed++
		extra := strays()
		for _, one := range append([][]Marker{extra}, chunk(s.rng, extra, 4)...) {
			in2 := cloneInput(in)
			for _, m := range one {
				m.At = in.Now.Add(-time.Second)
				in2.Markers = append(in2.Markers, m)
			}
			if got := Reduce(in2); !reflect.DeepEqual(got, st) {
				s.fail("P1: %+v moved to %+v when %d markers were written after it", st, got, len(one))
			}
		}
	}
	return in, st
}

// chunk picks a few small groups of the markers at random.
func chunk(rng *rand.Rand, all []Marker, n int) [][]Marker {
	var out [][]Marker
	for i := 0; i < n; i++ {
		var group []Marker
		for j := 0; j < 1+rng.IntN(5); j++ {
			group = append(group, all[rng.IntN(len(all))])
		}
		out = append(out, group)
	}
	return out
}

func (s *sim) newBoot() string {
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", s.rng.Uint32(), s.rng.Uint64()&0xffffffffffff)
}

// restart is the machine restarting, on purpose or not: the unit is gone, and the core sees another boot a little later.
func (s *sim) restart() {
	s.unit.Active = false
	s.wait(time.Duration(10+s.rng.IntN(170)) * time.Second)
	s.boot.Now = s.newBoot()
	s.ready = false
	s.snap()
}

// settleBoot is what the core does when it first sees a boot that is new to it.
func (s *sim) settleBoot() {
	if s.boot.Now != s.seenID {
		s.seenID = s.boot.Now
		s.write(Marker{Kind: MkBootSeen, Boot: s.boot.Now})
		s.seen = s.clock
	}
	if !s.ready && s.rng.IntN(3) == 0 {
		s.ready = true
	}
}

// startEffects and doneEffects are the files that the blocks leave.
func (s *sim) startEffects(id string) {
	if id == BlockMinimal {
		s.files.Begun = true
	}
}

func (s *sim) doneEffects(id string) {
	switch id {
	case BlockTimers:
		s.files.DoneTimers = true
	case BlockSources:
		s.files.AptOrig = true
	case BlockMinimal:
		s.files.DoneMinimal = true
	case BlockFull:
		s.files.DoneFull = true
	case BlockPrereboot:
		s.files.DonePrereboot = true
		s.files.BootIDFile = s.boot.Now
	case BlockAfter:
		s.files.DoneAfter = true
	}
}

func (s *sim) blockDone(id string) bool {
	switch id {
	case BlockTimers:
		return s.files.DoneTimers
	case BlockMinimal:
		return s.files.DoneMinimal
	case BlockFull:
		return s.files.DoneFull
	case BlockPrereboot:
		return s.files.DonePrereboot
	case BlockAfter:
		return s.files.DoneAfter
	}
	return false
}

// putBack is the backup folder moved aside: what the blocks left is gone.
func (s *sim) putBack() {
	abort := s.files.AbortFlag
	s.files = Files{AbortFlag: abort}
}

// launch is the core writing an attempt and the unit that it starts running to its end, or to the fault that stops it.
func (s *sim) launch(phase, who, from string) {
	n := NextAttempt(s.input())
	s.write(Marker{Kind: MkAttempt, N: n, Phase: phase, From: from, Who: who})
	s.snap()
	switch r := s.rng.IntN(100); {
	case r < 4:
		// systemd-run said that it failed. Half of the time the unit exists after all, and says so as it begins.
		s.unit.Active = false
		s.write(Marker{Kind: MkStartFailed, N: n})
		s.snap()
		if s.rng.IntN(2) == 0 {
			return
		}
		s.unit.Active = true
		s.snap()
		s.write(Marker{Kind: MkUnitStart, N: n, Phase: phase, Boot: s.boot.Now, PID: 1000 + n})
		s.snap()
		s.program(n, phase)
		return
	case r < 6:
		// The core went away before systemd-run, or the unit never wrote a word.
		s.unit.Active = false
		s.wait(time.Minute)
		s.snap()
		return
	}
	s.unit.Active = true
	s.write(Marker{Kind: MkUnitStart, N: n, Phase: phase, Boot: s.boot.Now, PID: 1000 + n})
	s.snap()
	s.program(n, phase)
}

type fault int

const (
	noFault fault = iota
	stopped
	killed
	powerCut
	calledOff
)

func (s *sim) fault(phase string) fault {
	r := s.rng.IntN(100)
	switch {
	case r < 7:
		return stopped
	case r < 11:
		return killed
	case r < 13:
		return powerCut
	case r < 15 && phase != PhaseVerify && !s.files.PNR():
		return calledOff
	}
	return noFault
}

func (s *sim) end(n int, how string) {
	s.write(Marker{Kind: MkUnitEnd, N: n, Result: how})
	s.unit.Active = false
	s.snap()
}

// halt is a driver whose block ended with a status that is not 0: the stop, the unwind before the point of no return, the end.
func (s *sim) halt(n, idx int, id, class string, rc int, phase string) {
	pnr := s.files.PNR()
	s.write(Marker{Kind: MkStop, N: n, Index: idx, ID: id, RC: rc, Class: class, PNR: pnr})
	s.snap()
	if !pnr && phase != PhaseVerify {
		result := "ok"
		if s.rng.IntN(8) == 0 {
			result = "partial"
		}
		if result == "ok" {
			s.putBack()
		}
		s.write(Marker{Kind: MkUnwound, N: n, Result: result})
		s.snap()
	}
	s.end(n, "stopped")
}

func (s *sim) program(n int, phase string) {
	ids := BlockList(phase, s.files)
	for i, id := range ids {
		idx := i + 1
		if s.blockDone(id) {
			s.write(Marker{Kind: MkBlockSkipped, N: n, Index: idx, ID: id})
			s.snap()
			continue
		}
		s.write(Marker{Kind: MkBlockStart, N: n, Index: idx, ID: id})
		s.snap()
		s.startEffects(id)
		switch f := s.fault(phase); f {
		case stopped:
			classes := slices.DeleteFunc(slices.Clone(Classes), func(c string) bool { return c == ClassAbort })
			s.halt(n, idx, id, classes[s.rng.IntN(len(classes))], 1+s.rng.IntN(120), phase)
			return
		case calledOff:
			s.files.AbortFlag = true
			s.halt(n, idx, id, ClassAbort, 0, phase)
			return
		case killed, powerCut:
			s.unit.Active = false
			s.wait(time.Duration(5+s.rng.IntN(120)) * time.Second)
			if f == powerCut {
				s.boot.Now = s.newBoot()
				s.ready = false
			}
			s.snap()
			return
		}
		s.doneEffects(id)
		s.write(Marker{Kind: MkBlockDone, N: n, Index: idx, ID: id})
		s.snap()
	}
	switch phase {
	case PhaseRefresh:
		answer := "no"
		if s.rng.IntN(2) == 0 {
			answer = "yes"
		}
		s.putBack()
		s.write(Marker{Kind: MkRefreshDone, N: n, Result: answer, Why: "kernel"})
		s.snap()
		s.end(n, "ok")
	case PhaseRehearse:
		s.write(Marker{Kind: MkRehearsalReady, N: n})
		s.snap()
		s.end(n, "ok")
	case PhaseRun, PhaseRecover:
		if s.rng.IntN(7) == 0 {
			s.files.BootIDFile = ""
			s.write(Marker{Kind: MkRebootNotNeeded, N: n, Kernel: "6.1.0-37-amd64"})
		} else {
			s.write(Marker{Kind: MkRebootPending, N: n, Boot: s.files.BootIDFile, Kernel: "6.1.0-37-amd64"})
		}
		s.snap()
		s.end(n, "ok")
	case PhaseVerify:
		s.write(Marker{Kind: MkVerifyDone, N: n, Boot: s.boot.Now})
		s.snap()
		s.end(n, "ok")
	case PhaseUnwind:
		if s.files.PNR() {
			s.write(Marker{Kind: MkStop, N: n, Index: 0, ID: StopUnwind, RC: 1, Class: ClassGuard, PNR: true})
			s.snap()
			s.end(n, "stopped")
			return
		}
		result := "ok"
		if s.rng.IntN(8) == 0 {
			result = "partial"
		} else {
			s.putBack()
		}
		s.write(Marker{Kind: MkUnwound, N: n, Result: result})
		s.snap()
		s.end(n, "ok")
	}
}

// owner is what the person at the dashboard does when the run waits for them.
func (s *sim) owner(st State, in Input) {
	buttons := ActionsFor(st)
	if s.silent() && !(st.Name == StateNeedsAttention && st.Reason == ReasonSystemdSilent) {
		// The routes ask the unit first, and a systemd that does not answer refuses them all.
		buttons = nil
	}
	has := func(b string) bool { return slices.Contains(buttons, b) }
	pick := s.rng.IntN(100)
	switch {
	case has(ActionReboot) && pick < 70:
		s.write(Marker{Kind: MkRebootRequested})
		s.snap()
		switch s.rng.IntN(8) {
		case 0:
			s.write(Marker{Kind: MkRebootFailed})
			s.snap()
		case 1, 2:
			// The machine does not go: it is waited for, and asked again, or it comes up late.
		default:
			s.restart()
		}
	case has(ActionResume) && pick < 60:
		phase, from := PhaseRecover, FirstMissing(s.files)
		if st.Phase == PhaseVerify && (st.Name == StateFailed || st.Name == StateInterrupted) || from == "" {
			phase, from = PhaseVerify, ""
		}
		s.launch(phase, WhoOwner, from)
	case has(ActionDiscard) && st.Name == StateRehearsed && pick < 60:
		s.launch(PhaseUnwind, WhoOwner, "")
	case has(ActionTakeover) && pick < 80:
		s.write(Marker{Kind: MkTakeover})
		s.snap()
	case has(ActionAcknowledge) && pick < 95:
		s.write(Marker{Kind: MkAck, Result: "acknowledge"})
		s.snap()
		s.done = true
	default:
		s.wait(time.Duration(1+s.rng.IntN(7200)) * time.Second)
	}
}

// drive is the core's loop and the owner's: until the run is closed, or has gone as far as it goes.
func (s *sim) drive() {
	for step := 0; step < 120 && !s.done; step++ {
		s.settleBoot()
		in, st := s.snap()
		act := NextAction(st, in)
		switch act.Kind {
		case DoUnwind:
			s.launch(PhaseUnwind, WhoAuto, act.From)
		case DoLaunchVerify:
			s.launch(PhaseVerify, WhoAuto, act.From)
		case DoLaunchRecover:
			s.launch(PhaseRecover, WhoAuto, act.From)
		case DoJudge:
			v := VerifyResult{}
			switch s.rng.IntN(4) {
			case 0:
				v.Errors = 1
			case 1:
				v.Warnings = 1 + s.rng.IntN(3)
			}
			s.verify = &v
			s.snap()
		case DoRemind:
			s.remind = act.N
		case DoBarrier:
			ok := s.rng.IntN(5) != 0
			s.barrier = &Barrier{OK: ok}
			s.snap()
			s.owner(st, in)
		case DoCleanup:
			s.done = true
		default:
			switch st.Name {
			case StateManual:
				if s.rng.IntN(2) == 0 {
					// The owner finishes the hop by hand.
					s.files.DoneMinimal, s.files.DoneFull, s.files.DonePrereboot, s.files.DoneAfter = true, true, true, true
					s.snap()
				}
				s.wait(time.Minute)
				s.owner(st, in)
			case StateRefreshing, StateRehearsing, StateRunning, StateRecovering, StateVerifying, StateUnwinding:
				// A unit that is going: let it.
				s.wait(time.Duration(1+s.rng.IntN(90)) * time.Second)
			default:
				s.wait(time.Duration(1+s.rng.IntN(1800)) * time.Second)
				if (st.Name == StateRebooting || st.Name == StateRebootStalled) && s.rng.IntN(3) == 0 {
					s.restart()
					break
				}
				s.owner(st, in)
			}
		}
	}
}

func TestRunsThatHappenAtRandomKeepEveryPropertyOfTheReducer(t *testing.T) {
	simRuns, seed := simParams()
	rng := rand.New(rand.NewPCG(seed, 7))
	kinds := []string{RunHop, RunHop, RunHop, RunRefresh, RunRehearsal}
	reached := map[string]int{}
	for run := 0; run < simRuns; run++ {
		kind := kinds[rng.IntN(len(kinds))]
		s := newSim(t, rng, kind)
		s.cover = reached
		first := map[string]string{RunHop: PhaseRun, RunRefresh: PhaseRefresh, RunRehearsal: PhaseRehearse}[kind]
		s.launch(first, WhoAuto, "")
		s.drive()
		s.snap()
		for _, m := range s.ms {
			reached["marker "+m.Kind]++
		}
	}
	// The runs go everywhere: a property that is never exercised proves nothing.
	for _, name := range States {
		if reached[name] == 0 {
			t.Errorf("no run ended in %s", name)
		}
	}
	for _, kind := range MarkerKinds {
		if reached["marker "+kind] == 0 {
			t.Errorf("no run had a %s marker", kind)
		}
	}
	t.Logf("%d runs: %v", simRuns, reached)
}
