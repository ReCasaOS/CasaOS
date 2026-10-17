package osrun

import (
	"slices"
	"time"
)

// Unit is what systemd says of the upgrade's unit. Answered is false when systemd did not answer within the bound the
// caller gave it: nothing is then inferred from the unit, and nothing is launched. SilentSince is when systemd was first seen
// not answering, in the run of unanswered questions that is still going on; it is zero when the caller does not know, and the
// time since the last sign of life of the run stands for it then.
type Unit struct {
	Active, Answered bool
	SilentSince      time.Time
}

// Boot is the id of the boot the machine is in now; OK is false when it could not be read.
type Boot struct {
	Now string
	OK  bool
}

// A BarrierCheck is one of the read-only checks that stand between a finished hop and the restart.
type BarrierCheck struct {
	Code     string
	OK       bool
	Severity string
}

// Barrier is the answer of the checks that run while the machine waits for its restart.
type Barrier struct {
	OK     bool
	Checks []BarrierCheck
}

// VerifyResult is what the judge found after the restart: how many findings of each severity that count. Findings that
// are only information never change a state, and are not here.
type VerifyResult struct {
	Errors, Warnings int
}

// Input is everything the reducer reads. It is all data: the reducer has no clock, opens no file and asks no one.
type Input struct {
	// Markers are the valid lines of the log, in order, as ParseMarkers gives them (the QUEUED line first); Ignored is
	// how many lines it did not take, which the status shows and the reducer does not use.
	Markers []Marker
	Ignored int
	Files   Files
	Unit    Unit
	Boot    Boot
	// Now is the time of the reading; LastActivity is the later of the last marker's time and the unit's output file's: a
	// time that is after Now counts as Now, and a missing one is made up from the markers.
	Now, LastActivity time.Time
	// Barrier is nil until it has been computed; Verify is nil until the judge has run.
	Barrier *Barrier
	Verify  *VerifyResult
	// Ready is the core's reading of the machine that has just restarted (the system is running or degraded, without
	// waiting for it to be); BootSeenAt is when the core noted the new boot.
	Ready      bool
	BootSeenAt time.Time
	// Reminded is how many reminders of a machine that waits for its restart have been sent, from the core's own record.
	Reminded int
	// Damaged says that the run cannot be trusted, which the log does not say: the files of the run are not the ones the
	// core wrote, or the log is longer than it can be read.
	Damaged bool
	// Timing is DefaultTiming, or what a test sets. The zero value is taken for DefaultTiming.
	Timing Timing
}

// BlockRef is a block of an attempt's list: its place in it, from 1, and which one it is. The zero value is none.
type BlockRef struct {
	Index int
	ID    string
}

// State is where a run is. Name is one of States; the other strings are set only where the state has them.
type State struct {
	Name string
	// Reason is why a failed run failed (a class, or one of the Reason constants) or why a run needs attention; Cause is
	// how an interrupted run was interrupted; Class is the class of the stop behind a failed or stopped state.
	Reason, Cause, Class string
	// Phase and Attempt are the phase and the number of the latest attempt, Block the block it is in (for a running state)
	// or the block that stopped (for a stopped one), and RC the status that block ended with.
	Phase   string
	Attempt int
	Block   BlockRef
	RC      int
	// Kind, RunID, Hop and PlanID are the run's own, from its RUN marker.
	Kind, RunID, Hop, PlanID string
	// PNR is whether packages are being or have been changed; Reversible is the same thing the other way round.
	PNR, Reversible bool
	// Busy is whether the machine must be left alone: backups and automatic updates stay away, and so does the owner.
	Busy bool
	// HalfUpgraded is whether the point of no return is behind and the last block before the restart did not end.
	HalfUpgraded bool
	// Resumable is whether the owner can ask for the run to go on.
	Resumable bool
	// Abortable is whether the owner can still call the run off by stopping its unit.
	Abortable bool
	// RebootBlocked is whether the restart must not be offered: the checks that come before it are not computed or not green.
	RebootBlocked bool
	// BootUnknown is whether the boot id could not be read where it was needed, so that nothing was inferred from it;
	// Stalled is whether a restart that was asked for did not happen in time.
	BootUnknown, Stalled bool
	// Closed is whether the owner has closed the run.
	Closed bool
	// Since is when the run came to this state, as far as the log and the files say; ExpiresAt is when a rehearsal is undone.
	Since     time.Time
	ExpiresAt *time.Time
}

// busyOf is whether a state keeps the machine busy: every one but those where nothing is going on and nothing is half
// done, and a failed run that had not passed the point of no return.
func busyOf(name string, pnr bool) bool {
	switch name {
	case StateIdle, StateRefreshed, StateRehearsalDone, StateSucceeded, StateSucceededWithFindings, StateManualDone:
		return false
	case StateFailed:
		return pnr
	}
	return true
}

// NeedsAckWord is whether closing the run takes the typed word: a machine that is half upgraded, one that needs attention, and one
// that the owner took over and did not finish (a run taken over from needs_attention is the same machine, with the same sources of
// the next release perhaps in place and the same packages perhaps half configured, and the click that takes it over must not
// be a way round the word), whether or not the point of no return was passed: closing the run drops the flag that keeps backups
// and automatic updates away from it.
func (s State) NeedsAckWord() bool {
	return (s.HalfUpgraded || s.Name == StateNeedsAttention || s.Name == StateManual) && slices.Contains(ActionsFor(s), ActionAcknowledge)
}

// An attemptView is what the log says of one attempt.
type attemptView struct {
	N                int
	Phase, From, Who string
	At               time.Time
	StartFailed      bool
	Started          bool
	StartAt          time.Time
	StartBoot        string
	Ended            bool
	EndAt            time.Time
	// Block is the block that began and has not ended; LastStart is the one that began last, ended or not.
	Block     BlockRef
	LastStart string
	Stop      *Marker
	Unwound   string
	Result    *Marker
	events    int
}

// open is whether the attempt has not ended. A START_FAILED does not end it: the core writes it when it could not tell that the
// unit was started, and a unit that exists after all is the unit's own word, whenever it comes.
func (a *attemptView) open() bool { return !a.Ended }

// maxBlockEvents is how many block markers an attempt can have: three for each of the blocks of its list, with room to spare.
const maxBlockEvents = 64

// A view is the log of a run, reduced to the markers that can belong to it. A marker that cannot be (an attempt that
// was not asked for, a word of a unit that is not running, a second one of a kind that comes once) is dropped, and so is
// everything that comes after the run was decided.
type view struct {
	valid   bool
	queued  time.Time
	run     *Marker
	attempt []*attemptView
	// m is the latest result marker of the run, whichever attempt wrote it, and mAttempt that attempt.
	m        *Marker
	mAttempt *attemptView
	// reqAt is when the restart was asked for in the current wait, zero when it was not or the request failed.
	reqAt          time.Time
	rehearsalReady bool
	takeover       bool
	acked          bool
	sealed         bool
	// lastAt is the time of the latest marker of the core's launch and of the unit.
	lastAt time.Time
}

func (v *view) last() *attemptView {
	if len(v.attempt) == 0 {
		return nil
	}
	return v.attempt[len(v.attempt)-1]
}

func (v *view) count(phase string) int {
	n := 0
	for _, a := range v.attempt {
		if a.Phase == phase {
			n++
		}
	}
	return n
}

// phaseFits is whether a run of this kind can have an attempt of this phase as its next one.
func phaseFits(kind, phase string, first bool) bool {
	switch kind {
	case RunRefresh:
		return phase == PhaseRefresh && first || phase == PhaseUnwind && !first
	case RunRehearsal:
		return phase == PhaseRehearse && first || phase == PhaseUnwind && !first
	case RunHop:
		return phase == PhaseRun && first || !first && (phase == PhaseRecover || phase == PhaseVerify || phase == PhaseUnwind)
	}
	return false
}

// scan reads the markers in order into a view. Reading stops taking markers once the run is decided for good (it was
// taken over, or it ended in a state that nothing can leave but the owner's closing it), so that a late or forged line
// cannot move it.
func scan(in Input, tm Timing) *view {
	v := &view{}
	if len(in.Markers) == 0 || in.Markers[0].Kind != MkQueued || in.Markers[0].N != 0 {
		return v
	}
	v.valid, v.queued = true, in.Markers[0].At
	for _, m := range in.Markers[1:] {
		switch {
		case v.run == nil:
			if m.Kind == MkRun {
				run := m
				v.run = &run
			}
		case m.Kind == MkAck:
			v.acked = true
		case v.sealed:
		default:
			v.add(m, in, tm)
		}
	}
	return v
}

func (v *view) touch(t time.Time) {
	if t.After(v.lastAt) {
		v.lastAt = t
	}
}

// add takes a marker of a run that has been opened and is not decided.
func (v *view) add(m Marker, in Input, tm Timing) {
	a := v.last()
	switch m.Kind {
	case MkQueued, MkRun, MkBootSeen:
	case MkTakeover:
		v.takeover, v.sealed = true, true
	case MkRebootRequested:
		v.reqAt = m.At
	case MkRebootFailed:
		v.reqAt = time.Time{}
	case MkAttempt:
		if m.N == len(v.attempt)+1 && phaseFits(v.run.RunKind, m.Phase, len(v.attempt) == 0) {
			v.attempt = append(v.attempt, &attemptView{N: m.N, Phase: m.Phase, From: m.From, Who: m.Who, At: m.At})
			v.touch(m.At)
		}
	case MkStartFailed:
		if a != nil && m.N == a.N && !a.Started && !a.StartFailed {
			a.StartFailed = true
		}
	case MkUnitStart:
		// A unit that begins after the core said it did not start is the unit that was started: the word of the unit is the later one.
		if a != nil && m.N == a.N && !a.Started && m.Phase == a.Phase {
			a.Started, a.StartFailed, a.StartAt, a.StartBoot = true, false, m.At, m.Boot
			v.touch(m.At)
		}
	default:
		if a == nil || m.N != a.N || !a.Started || a.Ended {
			return
		}
		v.addUnitMarker(a, m, in, tm)
	}
}

// addUnitMarker takes a word of the unit of the latest attempt, which has begun and not ended.
func (v *view) addUnitMarker(a *attemptView, m Marker, in Input, tm Timing) {
	switch m.Kind {
	case MkBlockStart, MkBlockDone, MkBlockSkipped:
		if a.events++; a.events > maxBlockEvents {
			return
		}
		ref := BlockRef{Index: m.Index, ID: m.ID}
		switch m.Kind {
		case MkBlockStart:
			a.Block, a.LastStart = ref, m.ID
		default:
			if a.Block == ref {
				a.Block = BlockRef{}
			}
		}
	case MkStop:
		if a.Stop == nil && a.Result == nil {
			stop := m
			a.Stop = &stop
		}
	case MkUnwound:
		if a.Unwound == "" && (a.Phase == PhaseUnwind || a.Stop != nil && !a.Stop.PNR) {
			a.Unwound = m.Result
		}
	case MkRefreshDone, MkRehearsalReady, MkRebootPending, MkRebootNotNeeded, MkVerifyDone:
		if a.Stop != nil || a.Result != nil || !resultOfPhase(m.Kind, a.Phase) {
			return
		}
		result := m
		a.Result, v.m, v.mAttempt, v.reqAt = &result, &result, a, time.Time{}
		v.rehearsalReady = v.rehearsalReady || m.Kind == MkRehearsalReady
	case MkUnitEnd:
		a.Ended, a.EndAt = true, m.At
		v.touch(m.At)
		v.sealed = sealedBy(decide(v, in, tm))
	default:
		return
	}
	v.touch(m.At)
}

// resultOfPhase is whether a phase can end with this result.
func resultOfPhase(kind, phase string) bool {
	switch kind {
	case MkRefreshDone:
		return phase == PhaseRefresh
	case MkRehearsalReady:
		return phase == PhaseRehearse
	case MkRebootPending, MkRebootNotNeeded:
		return phase == PhaseRun || phase == PhaseRecover
	case MkVerifyDone:
		return phase == PhaseVerify
	}
	return false
}

// sealedBy is whether a state is one that the log decides for good: a run that ended, whatever the owner does next, and that
// the machine and the clock cannot change. Nothing that is read after it can move it, but the owner's closing it.
func sealedBy(s State) bool {
	switch s.Name {
	case StateSucceeded, StateSucceededWithFindings:
		// The files and the verdict are today's: an earlier unit that ended with the restart to come agrees with them, and is not the
		// attempt that decided the run. It is the check's end that does.
		return s.Phase == PhaseVerify
	case StateRefreshed, StateRehearsalDone:
		return true
	case StateFailed:
		return !s.PNR && s.Reason != ReasonStart
	}
	return false
}

// A ctx is a reading of the run: its view, the input and what is worked out from them once.
type ctx struct {
	v   *view
	in  Input
	tm  Timing
	a   *attemptView
	pnr bool
	// la is the time of the last sign of life.
	la time.Time
}

func timingOf(in Input) Timing {
	if in.Timing == (Timing{}) {
		return DefaultTiming
	}
	return in.Timing
}

func newCtx(in Input) *ctx {
	tm := timingOf(in)
	return newCtxFrom(scan(in, tm), in, tm)
}

func newCtxFrom(v *view, in Input, tm Timing) *ctx {
	c := &ctx{v: v, in: in, tm: tm, a: v.last(), pnr: in.Files.PNR()}
	c.la = in.LastActivity
	if v.lastAt.After(c.la) {
		c.la = v.lastAt
	}
	if c.la.After(in.Now) {
		c.la = in.Now
	}
	return c
}

// age is how long ago a time was, never less than nothing.
func (c *ctx) age(t time.Time) time.Duration {
	if d := c.in.Now.Sub(t); d > 0 {
		return d
	}
	return 0
}

// Reduce is the state of a run, from what the log and the files of the run say and what systemd and the kernel say now.
// It is a function of its input and of nothing else.
func Reduce(in Input) State {
	tm := timingOf(in)
	return decide(scan(in, tm), in, tm)
}

func decide(v *view, in Input, tm Timing) State {
	if !v.valid || v.run == nil {
		// A log that cannot place the run says nothing of whether something is going on, and the files and the unit do: packages
		// that are being changed, or a unit that runs, with no run to explain them, are a machine that is not free.
		if pnr := in.Files.PNR(); pnr || in.Unit.Active || in.Damaged {
			return State{
				Name: StateNeedsAttention, Reason: ReasonRunDamaged, PNR: pnr, Reversible: !pnr, Busy: true,
				HalfUpgraded: pnr && !in.Files.DonePrereboot,
			}
		}
		return State{Name: StateIdle, PNR: in.Files.PNR(), Reversible: !in.Files.PNR()}
	}
	return newCtxFrom(v, in, tm).decide()
}

// NextAttempt is the number the next attempt of the run takes: one more than the attempts that the reducer counts.
func NextAttempt(in Input) int {
	return len(scan(in, timingOf(in)).attempt) + 1
}

// st is a state of this run with what every state has.
func (c *ctx) st(name string, since time.Time) State {
	s := State{
		Name: name, Kind: c.v.run.RunKind, RunID: c.v.run.RunID, Hop: c.v.run.Hop, PlanID: c.v.run.PlanID,
		PNR: c.pnr, Reversible: !c.pnr, HalfUpgraded: c.pnr && !c.in.Files.DonePrereboot, Since: since,
	}
	if c.a != nil {
		s.Phase, s.Attempt = c.a.Phase, c.a.N
	}
	s.Busy = busyOf(name, c.pnr)
	return s
}

// stopped fills in what a state that is the end of a stop says of it.
func (c *ctx) stopped(s State, stop *Marker, class string) State {
	s.Class, s.RC = class, stop.RC
	s.Block = BlockRef{Index: stop.Index, ID: stop.ID}
	return s
}

func (c *ctx) decide() State {
	v := c.v
	if v.acked {
		s := c.st(StateIdle, v.lastAt)
		s.Closed = true
		return s
	}
	if v.takeover {
		if c.in.Files.DoneAfter {
			return c.st(StateManualDone, c.la)
		}
		return c.st(StateManual, c.la)
	}
	if c.in.Damaged {
		return c.attention(ReasonRunDamaged)
	}
	a := c.a
	if a == nil {
		// No attempt that this run could have: packages that are being changed, or a unit that runs, are not the run's own and
		// say that its log and its files do not agree.
		if c.pnr || c.in.Unit.Active {
			return c.attention(ReasonRunDamaged)
		}
		// The core wrote the run and no attempt: it was lost before it launched anything.
		if c.age(v.queued) >= c.tm.StartLost {
			s := c.st(StateFailed, v.queued)
			s.Reason = ReasonStart
			return s
		}
		return c.st(StateIdle, v.queued)
	}
	if s, ok := c.live(); ok {
		return s
	}
	s := c.settled()
	if s.Busy && s.Name != StateRebooting && s.Name != StateNeedsAttention && c.silentForLong() {
		// Nothing runs and the machine is held, and systemd has not answered for as long as the bound says: whatever the state
		// would be, nothing can be launched or judged while it is silent, and the way on is the owner's.
		return c.attention(ReasonSystemdSilent)
	}
	return s
}

// settled is the state of a run whose latest attempt is not running: it is lost, it ended, or its launch never came to anything.
func (c *ctx) settled() State {
	a := c.a
	if a.Started && a.Phase == PhaseVerify && a.Stop == nil && c.in.Files.DoneAfter {
		return c.byResult()
	}
	if !a.Started {
		return c.notStarted()
	}
	if !a.Ended {
		if c.canFinishFromFiles() {
			return c.byResult()
		}
		return c.lost()
	}
	if a.Phase == PhaseUnwind {
		return c.unwound()
	}
	if a.Stop != nil {
		if !c.pnr && a.Phase != PhaseVerify {
			return c.stoppedBefore()
		}
		return c.stoppedAfter()
	}
	return c.byResult()
}

func (c *ctx) attention(reason string) State {
	s := c.st(StateNeedsAttention, c.la)
	s.Reason = reason
	return s
}

// systemdSilent is whether the unit's state cannot be had and nothing has been heard of it for long enough that the run is
// not to be judged.
func (c *ctx) systemdSilent() bool {
	if c.in.Unit.Answered || c.age(c.la) < c.tm.SystemdSilence {
		return false
	}
	// A run that has been quiet for a day and finds systemd not answering for a moment is not one whose systemd is silent.
	since := c.in.Unit.SilentSince
	return since.IsZero() || c.age(since) >= c.tm.SystemdSilence
}

// silentForLong is systemdSilent for a run that has no unit that runs, where the sign of life of the run is old whatever
// systemd does: a machine that waits for its restart for days is not one whose systemd has been silent for days. It is the time
// that systemd has been silent that counts, when the caller says it, and otherwise the time since the run last spoke.
func (c *ctx) silentForLong() bool {
	if c.in.Unit.Answered {
		return false
	}
	if since := c.in.Unit.SilentSince; !since.IsZero() {
		return c.age(since) >= c.tm.SystemdSilence
	}
	return c.age(c.la) >= c.tm.SystemdSilence
}

// active is whether the latest attempt is effectively running: its unit is, or the unit cannot be asked and has given a sign
// of life lately, or it is gone but spoke a moment ago (systemd can say so before the last line lands). A unit that systemd says
// is running is running whatever the log says of how it was started; an attempt that was told never to have started is not
// running on anything less.
func (c *ctx) active() bool {
	a := c.a
	if a == nil || !a.open() {
		return false
	}
	if c.in.Unit.Active {
		return true
	}
	if a.StartFailed {
		return false
	}
	return !c.in.Unit.Answered || a.Started && c.age(c.la) < c.tm.GraceQuiet
}

// startLost is whether the attempt that never said it began has been waited for long enough, or was told to have failed.
func (c *ctx) startLost() bool {
	a := c.a
	if a.StartFailed {
		return true
	}
	age := c.age(a.At)
	return c.in.Unit.Answered && !c.in.Unit.Active && age >= c.tm.StartLost || age >= c.tm.StartLostSilent
}

// inFlight is whether the latest attempt was written and its unit is on its way.
func (c *ctx) inFlight() bool {
	return c.a != nil && !c.a.Started && !c.a.StartFailed && !c.startLost()
}

// live is the state of an attempt whose unit is running, or is being started.
func (c *ctx) live() (State, bool) {
	a := c.a
	if !a.open() {
		return State{}, false
	}
	if c.systemdSilent() {
		return c.attention(ReasonSystemdSilent), true
	}
	// A launch that no unit ever answered, and that systemd cannot be asked about, is not waited for without end.
	if !a.Started && !c.in.Unit.Active && c.age(a.At) >= c.tm.StartLostSilent {
		return State{}, false
	}
	if c.active() {
		return c.running(), true
	}
	if c.inFlight() {
		if a.Phase == PhaseVerify {
			// The restarted machine waits for its check until the unit says it has begun.
			if r, ok := c.result(); ok && (r.kind == MkRebootPending || r.kind == MkRebootNotNeeded) {
				return c.byResult(), true
			}
		}
		return c.running(), true
	}
	return State{}, false
}

// running is the state of the phase of the latest attempt.
func (c *ctx) running() State {
	a := c.a
	name := map[string]string{
		PhaseRefresh: StateRefreshing, PhaseRehearse: StateRehearsing, PhaseRun: StateRunning, PhaseRecover: StateRecovering,
		PhaseVerify: StateVerifying, PhaseUnwind: StateUnwinding,
	}[a.Phase]
	since := a.At
	if a.Started {
		since = a.StartAt
	}
	s := c.st(name, since)
	s.Block = a.Block
	switch name {
	case StateRefreshing, StateRehearsing, StateRunning:
		// A unit that has said it stopped, or that it is done, is on its way out (undoing, or about to end): stopping it now cuts that
		// short.
		s.Abortable = !c.pnr && !pastAbort(a.LastStart) && a.Stop == nil && a.Result == nil && a.Unwound == ""
	}
	return s
}

// pastAbort is whether the last block that began is one that a run cannot be called off in: the minimal upgrade or one after
// it, and in a refresh the block that upgrades packages (stopping dpkg half way is what the point of no return is there to
// keep from), the block that puts the sources back, and the repair.
func pastAbort(id string) bool {
	switch id {
	case BlockMinimal, BlockFull, BlockPrereboot, BlockAfter, BlockForward, BlockRUpgrade, BlockRDone:
		return true
	}
	return false
}

// notStarted is the state of an attempt whose unit never said it began and is not on its way any more.
func (c *ctx) notStarted() State {
	a := c.a
	if a.Phase == PhaseUnwind {
		return c.interrupted(CauseKilled, false)
	}
	s := c.st(StateFailed, a.At)
	s.Reason = ReasonStart
	s.Resumable = c.pnr
	return s
}

// canFinishFromFiles is whether a unit that died with the last block of the hop done had in fact done what it was to do: the
// files say it, and the restart is all that is left.
func (c *ctx) canFinishFromFiles() bool {
	a := c.a
	return (a.Phase == PhaseRun || a.Phase == PhaseRecover) && a.Stop == nil && c.in.Files.DonePrereboot
}

// lost is the state of an attempt that began, did not end and whose unit is gone.
func (c *ctx) lost() State {
	a := c.a
	cause, unknown := CauseKilled, !c.in.Boot.OK || a.StartBoot == ""
	if !unknown && a.StartBoot != c.in.Boot.Now {
		cause = CausePower
	}
	return c.interrupted(cause, unknown)
}

// interrupted is a run whose unit did not finish. The undoing of a run that is not past the point of no return is the
// core's to start again, a few times; after that the machine is left to the owner.
func (c *ctx) interrupted(cause string, bootUnknown bool) State {
	a := c.a
	if a.Phase == PhaseUnwind && c.v.count(PhaseUnwind) >= c.tm.MaxUnwindAttempts {
		return c.attention(ReasonUnwindPartial)
	}
	s := c.st(StateInterrupted, c.la)
	s.Cause, s.BootUnknown, s.Resumable = cause, bootUnknown, c.pnr
	if a.Stop != nil && a.Phase != PhaseUnwind {
		// The unit said why it stopped and was cut short before it ended: the word is still the class of the stop, and a class that
		// says the repair is not to be trusted is not resumed because the unit was lost a moment later.
		class := c.stopClass(a.Stop)
		s = c.stopped(s, a.Stop, class)
		if c.pnr && a.Phase != PhaseVerify {
			s.Resumable = ClassBehavior(class).Resumable
		}
	}
	return s
}

// stopClass is the class of a stop as the run takes it: a plan that no longer holds, when packages are already being changed,
// is the guard's word.
func (c *ctx) stopClass(stop *Marker) string {
	if stop.Class == ClassPlan && c.pnr {
		return ClassGuard
	}
	return stop.Class
}

// stoppedBefore is a stop that the unit ended after, before the point of no return.
func (c *ctx) stoppedBefore() State {
	a := c.a
	class := a.Stop.Class
	switch {
	case a.Unwound == "ok":
		s := c.st(StateFailed, a.EndAt)
		s.Reason = class
		return c.stopped(s, a.Stop, class)
	case a.Unwound == "partial":
		return c.attention(ReasonUnwindPartial)
	case class == ClassIntegrity:
		// The driver stopped before it ran anything, and so before it changed anything.
		s := c.st(StateFailed, a.EndAt)
		s.Reason = class
		return c.stopped(s, a.Stop, class)
	}
	// The unit ended after its stop without undoing what it did: it was cut short.
	return c.interrupted(CauseKilled, false)
}

// stoppedAfter is a stop after the point of no return (or in the check after the restart): nothing is undone.
func (c *ctx) stoppedAfter() State {
	a := c.a
	class := a.Stop.Class
	if class == ClassPlan {
		// A plan that no longer holds, when packages are already being changed, is the guard's word.
		class = ClassGuard
	}
	if a.Phase == PhaseVerify {
		s := c.stopped(c.st(StateFailed, a.EndAt), a.Stop, class)
		s.Reason, s.Resumable = ReasonVerify, true
		return s
	}
	resumable := ClassBehavior(class).Resumable
	if c.tm.AutoRecoverTries > 0 && c.autoTries() >= c.tm.AutoRecoverTries && Retryable(class, c.tm.AutoRecoverWave) {
		s := c.stopped(c.st(StateFailed, a.EndAt), a.Stop, class)
		s.Reason, s.Resumable = ReasonHalf, resumable
		return s
	}
	s := c.stopped(c.st(StateStoppedAtWaypoint, a.EndAt), a.Stop, class)
	s.Resumable = resumable
	return s
}

// autoTries is how many times the core has repaired the hop by itself since the owner last asked for it.
func (c *ctx) autoTries() int {
	n := 0
	for _, a := range c.v.attempt {
		switch {
		case a.Phase != PhaseRecover:
		case a.Who == WhoOwner:
			n = 0
		default:
			n++
		}
	}
	return n
}

// unwound is the state of an unwind attempt that ended.
func (c *ctx) unwound() State {
	a := c.a
	if a.Stop != nil && c.pnr {
		// The unwind found that packages have been changed: it does nothing then.
		return c.stoppedAfter()
	}
	switch a.Unwound {
	case "ok":
		return c.unwoundOK()
	case "partial":
		return c.attention(ReasonUnwindPartial)
	}
	// It said nothing of having put anything back: its own checks failed, or it was cut short. It is started again.
	return c.interrupted(CauseKilled, false)
}

// unwoundOK is the end of a run that was put back: a rehearsal that was undone, or a failure that can be tried again.
func (c *ctx) unwoundOK() State {
	a, v := c.a, c.v
	if v.run.RunKind == RunRehearsal && v.rehearsalReady {
		return c.st(StateRehearsalDone, a.EndAt)
	}
	s := c.st(StateFailed, a.EndAt)
	for i := len(v.attempt) - 1; i >= 0; i-- {
		if stop := v.attempt[i].Stop; stop != nil && !stop.PNR {
			s.Reason = stop.Class
			return c.stopped(s, stop, stop.Class)
		}
	}
	s.Reason = ReasonInterruptedKilled
	if c.in.Files.AbortFlag {
		s.Reason = ClassAbort
	} else if i := len(v.attempt) - 2; i >= 0 {
		before := v.attempt[i]
		if before.StartBoot != "" && a.StartBoot != "" && before.StartBoot != a.StartBoot {
			s.Reason = ReasonInterruptedPower
		}
	}
	return s
}

// result is the latest word of the unit that says how an attempt ended, or the files' word for it when the unit died after
// doing what it was to.
type result struct {
	kind    string
	boot    string
	yes     bool
	at      time.Time
	attempt *attemptView
}

func (c *ctx) result() (result, bool) {
	f := c.in.Files
	if m := c.v.m; m != nil && !(f.DoneAfter && m.Kind != MkVerifyDone) {
		return result{kind: m.Kind, boot: m.Boot, yes: m.Result == "yes", at: m.At, attempt: c.v.mAttempt}, true
	}
	switch {
	case f.DoneAfter:
		// The file of the last block is the truth, whatever the markers got to say.
		return result{kind: MkVerifyDone, at: c.la}, true
	case f.DonePrereboot && f.BootIDFile != "":
		// A boot id that is not one cannot be compared with the machine's, and so is not read as a restart.
		boot := f.BootIDFile
		if !ValidUUID(boot) {
			boot = ""
		}
		return result{kind: MkRebootPending, boot: boot, at: c.la}, true
	case f.DonePrereboot:
		// The last block found the machine restarted already, and wrote no boot id.
		return result{kind: MkRebootNotNeeded, at: c.la}, true
	}
	return result{}, false
}

// byResult is the state of a run from the latest thing the unit said it had finished.
func (c *ctx) byResult() State {
	r, ok := c.result()
	if !ok {
		// An attempt ended and said nothing of how: the check can be done again, nothing else can be assumed.
		if c.a.Phase == PhaseVerify {
			return c.interrupted(CauseKilled, false)
		}
		return c.attention(ReasonRunDamaged)
	}
	switch r.kind {
	case MkRefreshDone:
		unknown := !c.in.Boot.OK || r.attempt == nil || r.attempt.StartBoot == ""
		if r.yes && (unknown || c.in.Boot.Now == r.attempt.StartBoot) {
			s := c.st(StateRebootFirst, r.at)
			s.BootUnknown = unknown
			return s
		}
		return c.st(StateRefreshed, r.at)
	case MkRehearsalReady:
		s := c.st(StateRehearsed, r.at)
		exp := r.at.Add(c.tm.RehearsalTTL)
		s.ExpiresAt = &exp
		return s
	case MkRebootPending:
		return c.waiting(r)
	case MkRebootNotNeeded:
		return c.st(StateResuming, r.at)
	}
	if !c.in.Files.DoneAfter {
		// The check says it ended and the file of its last block is not there: the files are the truth, and no verdict is made of that.
		return c.attention(ReasonRunDamaged)
	}
	return c.verdict(r)
}

// waiting is a hop that is done up to the restart: waiting for it, restarting, or restarted.
func (c *ctx) waiting(r result) State {
	unknown := !c.in.Boot.OK || r.boot == ""
	if !unknown && c.in.Boot.Now != r.boot {
		return c.st(StateResuming, r.at)
	}
	var s State
	switch req := c.v.reqAt; {
	case req.IsZero():
		s = c.st(StateAwaitingReboot, r.at)
	case c.age(req) < c.tm.RebootStall:
		s = c.st(StateRebooting, req)
	default:
		s = c.st(StateRebootStalled, req)
		s.Stalled = true
	}
	s.BootUnknown = unknown
	if s.Name != StateRebooting && c.v.run.RunKind == RunHop {
		// The files are the truth of whether the last block before the restart ended: a log that says it did, and a folder that
		// does not, is a machine that is not to be restarted on the log's word.
		s.RebootBlocked = c.in.Barrier == nil || !c.in.Barrier.OK || !c.in.Files.DonePrereboot
	}
	return s
}

// verdict is the state of a machine that has been checked after its restart.
func (c *ctx) verdict(r result) State {
	switch {
	case c.in.Verify == nil:
		return c.st(StateVerifying, r.at)
	case c.in.Verify.Errors > 0:
		s := c.st(StateFailed, r.at)
		s.Reason, s.Resumable = ReasonVerify, true
		return s
	case c.in.Verify.Warnings > 0:
		s := c.st(StateSucceededWithFindings, r.at)
		s.Resumable = true
		return s
	}
	return c.st(StateSucceeded, r.at)
}
