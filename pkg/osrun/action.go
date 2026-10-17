package osrun

import (
	"errors"
	"strings"
	"time"
)

// What the core does next about a run, one thing at a time (Action.Kind).
const (
	// DoNone is waiting.
	DoNone = "none"
	// DoUnwind starts the unit that puts back what a run that did not pass its point of no return had changed.
	DoUnwind = "unwind"
	// DoLaunchVerify starts the check of the machine after its restart, or again.
	DoLaunchVerify = "launch_verify"
	// DoLaunchRecover starts the repair of a hop that stopped after its point of no return.
	DoLaunchRecover = "launch_recover"
	// DoJudge compares the machine with what it was before the hop.
	DoJudge = "judge"
	// DoBarrier computes the checks that stand between the hop and the restart again.
	DoBarrier = "barrier"
	// DoRemind speaks again of a machine that waits for its restart; Action.N is which reminder.
	DoRemind = "remind"
	// DoCleanup is a run that was closed: its folder can go when there are too many of them.
	DoCleanup = "cleanup"
	// DoAlert tells the owner that a run waits for them. It comes for as long as the run does, the hub that sends it
	// says it once.
	DoAlert = "alert"
)

// An Action is the one thing to do next. Phase is the phase of the attempt to launch, From the block it is to begin with
// (a repair), Who is who asked for it, and N is which reminder is due.
type Action struct {
	Kind, Phase, From, Who string
	N                      int
}

// Behavior is what a class of stop means for a run.
type Behavior struct {
	// Resumable is whether the owner can ask for the run to go on from a stop of this class after the point of no return.
	// Before the point of no return every class is undone, and there is nothing to resume.
	Resumable bool
	// Wave is the first wave of automatic repairs that may repair a stop of this class by itself: 1 or 2, and 0 for a class
	// that is never repaired without the owner.
	Wave int
}

// ClassBehavior is what a class of stop means after the point of no return. A class that is not one of Classes is
// not resumable and never automatic: only the owner can say what to do with a machine in a state nobody foresaw.
func ClassBehavior(class string) Behavior {
	switch class {
	case ClassBusy, ClassNetwork:
		return Behavior{Resumable: true, Wave: 1}
	case ClassAptFailed, ClassDpkgBroken, ClassUnknown:
		return Behavior{Resumable: true, Wave: 2}
	case ClassDisk, ClassBarrierKernel, ClassBarrierInitrd, ClassBarrierGrub, ClassBarrierSSHD, ClassBarrierResolver,
		ClassBarrierOther, ClassIdentity, ClassTools, ClassAbort:
		return Behavior{Resumable: true}
	}
	// plan, guard, converge, unit, integrity: a stop that says the repair itself is not to be trusted
	return Behavior{}
}

// Retryable is whether the core may repair a stop of this class by itself, in a wave of repairs: wave 1 has the classes
// that are about the world being busy or away, wave 2 adds the ones that are about the packages, and no wave has the others.
func Retryable(class string, wave int) bool {
	w := ClassBehavior(class).Wave
	return w != 0 && wave >= w
}

// HintOf is a word for what the owner can do about the line that explains a failure, "" when there is none. The one there
// is: apt refused to take a repository whose release information changed, and the owner can say the change is wanted.
// It is read from apt's fixed English text.
func HintOf(detailLine string) string {
	if strings.Contains(detailLine, "must be accepted explicitly before updates for this repository can be applied") {
		return HintReleaseInfoChange
	}
	if i := strings.Index(detailLine, "changed its '"); i >= 0 {
		rest := detailLine[i+len("changed its '"):]
		for _, field := range []string{"Origin'", "Label'", "Suite'", "Version'", "Codename'"} {
			if strings.HasPrefix(rest, field+" value") {
				return HintReleaseInfoChange
			}
		}
	}
	return ""
}

// HintReleaseInfoChange is the hint for a repository whose release information changed.
const HintReleaseInfoChange = "releaseinfo_change"

// ActionsFor is the buttons that are valid for a state now, in the order of ActionNames. The server decides, and the page
// shows exactly these; whether the feature is switched on does not change them. A button that the state alone cannot
// decide (dropping a plan that is still alive) is left to the caller to take away.
func ActionsFor(s State) []string {
	want := map[string]bool{}
	on := func(name string, ok bool) { want[name] = want[name] || ok }
	switch s.Name {
	case StateRefreshing, StateRehearsing, StateRunning:
		on(ActionAbort, s.Abortable)
	case StateRebootFirst:
		on(ActionReboot, true)
		on(ActionAcknowledge, true)
	case StateRefreshed, StateRehearsalDone, StateSucceeded, StateManual, StateManualDone:
		on(ActionAcknowledge, true)
	case StateSucceededWithFindings:
		// The check can be made again: what the owner mended, or what was slow to come back, is looked at anew.
		on(ActionAcknowledge, true)
		on(ActionResume, s.Resumable)
	case StateRehearsed:
		on(ActionDiscard, true)
	case StateFailed:
		// A run that failed before its point of no return is closed or dropped; after it, the owner decides what the machine is.
		on(ActionAcknowledge, !s.PNR || s.Reason != ReasonStart)
		on(ActionDiscard, !s.PNR)
		on(ActionResume, s.PNR && s.Resumable)
		on(ActionTakeover, s.PNR)
	case StateAwaitingReboot, StateRebootStalled:
		on(ActionReboot, !s.RebootBlocked)
		on(ActionTakeover, true)
	case StateInterrupted, StateStoppedAtWaypoint:
		// An interrupted run that did not pass its point of no return is undone by the core, with no word from the owner.
		forward := s.Name == StateStoppedAtWaypoint || s.PNR
		on(ActionResume, forward && s.Resumable)
		on(ActionTakeover, forward)
		on(ActionAcknowledge, forward)
	case StateNeedsAttention:
		// A systemd that does not answer is a machine where only the owner's word works: taking over asks the unit, and the
		// owner has nothing to do with a run they cannot see the unit of but to let go of it.
		on(ActionTakeover, s.Reason != ReasonSystemdSilent)
		on(ActionAcknowledge, true)
	}
	out := []string{}
	for _, name := range ActionNames {
		if want[name] {
			out = append(out, name)
		}
	}
	return out
}

// remindersDue is how many reminders of a wait that has lasted this long are due: one at each of the first two times, and
// then one every third interval.
func remindersDue(waited time.Duration, r [3]time.Duration) int {
	switch {
	case waited < r[0]:
		return 0
	case waited < r[1]:
		return 1
	case r[2] <= 0:
		return 2
	}
	return 2 + int((waited-r[1])/r[2])
}

// NextAction is the one thing to do about a run now, from its state and the input it was reduced from. It never starts a
// unit while systemd does not answer, while one is running or on its way, after the owner took over or closed the run, nor
// for an attempt number that has been launched.
func NextAction(s State, in Input) Action {
	act := nextAction(s, in)
	// Packages that are being changed cannot be put back: the unit that undoes refuses such a machine, so it is not started for one.
	if act.Kind == DoUnwind && in.Files.PNR() {
		return Action{Kind: DoNone}
	}
	return act
}

func nextAction(s State, in Input) Action {
	c := newCtx(in)
	if c.v.run == nil || !c.v.valid {
		return Action{Kind: DoNone}
	}
	if s.Closed {
		return Action{Kind: DoCleanup}
	}
	a := c.a
	switch s.Name {
	case StateInterrupted:
		return c.afterInterruption(s)
	case StateRehearsed:
		if c.canLaunch() && (s.ExpiresAt != nil && !c.in.Now.Before(*s.ExpiresAt) || c.rehearsalMachineRestarted()) {
			return Action{Kind: DoUnwind, Phase: PhaseUnwind, Who: WhoAuto}
		}
	case StateResuming:
		if c.canLaunch() && (c.in.Ready || c.resumeWaited()) {
			return c.relaunchVerify()
		}
	case StateFailed:
		if s.PNR && s.Reason == ReasonStart {
			if a != nil && a.Phase == PhaseVerify {
				return c.relaunchVerify()
			}
			return c.afterStop(s)
		}
	case StateStoppedAtWaypoint:
		return c.afterStop(s)
	case StateVerifying:
		// No verdict is made while systemd does not answer: the facts that come from it are unknown then, and a machine that
		// nothing was asked of is not one that is well.
		if c.in.Unit.Answered && !c.active() && !c.inFlight() {
			return Action{Kind: DoJudge}
		}
	case StateAwaitingReboot, StateRebootStalled:
		if due := remindersDue(c.age(s.Since), c.tm.Reminders); due > c.in.Reminded {
			return Action{Kind: DoRemind, N: due}
		}
		return Action{Kind: DoBarrier}
	case StateRebootFirst:
		// A refresh that asked for a restart holds the machine as a hop does, and is spoken of as one: nobody comes to a machine
		// that nobody told. It has no barrier to compute.
		if due := remindersDue(c.age(s.Since), c.tm.Reminders); due > c.in.Reminded {
			return Action{Kind: DoRemind, N: due}
		}
	}
	return Action{Kind: DoNone}
}

// canLaunch is whether a unit can be started: systemd answers, none is running, and none that was written is on its way.
func (c *ctx) canLaunch() bool {
	return c.in.Unit.Answered && !c.in.Unit.Active && !c.inFlight()
}

// resumeWaited is whether a machine on its way to its check has had the time it is given to get ready. The time is counted from when
// the core noted the new boot; a restart that was not needed has no new boot to note (the machine is in the boot the run began in),
// and the time is counted from the unit's word that none was.
func (c *ctx) resumeWaited() bool {
	from := c.in.BootSeenAt
	if from.IsZero() {
		if r, ok := c.result(); ok && r.kind == MkRebootNotNeeded {
			from = r.at
		}
	}
	return !from.IsZero() && c.age(from) >= c.tm.ResumeWait
}

// rehearsalMachineRestarted is whether the machine has restarted since the rehearsal's unit began: the sources of the next
// release have been left in place across a restart, and the timers are back, so the rehearsal is undone.
func (c *ctx) rehearsalMachineRestarted() bool {
	for _, a := range c.v.attempt {
		if a.Phase == PhaseRehearse && a.Started && a.StartBoot != "" {
			return c.in.Boot.OK && c.in.Boot.Now != a.StartBoot
		}
	}
	return false
}

// relaunchVerify is the check after the restart, started or started again, a few times by itself.
func (c *ctx) relaunchVerify() Action {
	if !c.canLaunch() {
		return Action{Kind: DoNone}
	}
	if c.v.count(PhaseVerify) >= c.tm.MaxVerifyAttempts {
		return Action{Kind: DoAlert}
	}
	return Action{Kind: DoLaunchVerify, Phase: PhaseVerify, Who: WhoAuto}
}

// afterInterruption is what follows a unit that was lost: its check again, its undoing, or the owner's word.
func (c *ctx) afterInterruption(s State) Action {
	if c.a == nil || !c.canLaunch() {
		return Action{Kind: DoNone}
	}
	switch {
	case c.a.Phase == PhaseVerify:
		return c.relaunchVerify()
	case !s.PNR:
		if c.v.count(PhaseUnwind) >= c.tm.MaxUnwindAttempts {
			return Action{Kind: DoAlert}
		}
		return Action{Kind: DoUnwind, Phase: PhaseUnwind, Who: WhoAuto}
	}
	return c.afterStop(s)
}

// afterStop is what the core does about a hop that stopped after its point of no return: with no automatic repairs (the
// shipped setting) it tells the owner and waits for their click; with them, it repairs what its wave allows, a few times, a
// while after each stop.
func (c *ctx) afterStop(s State) Action {
	if !c.canLaunch() {
		return Action{Kind: DoNone}
	}
	class := s.Class
	if class == "" {
		// A unit that was lost says nothing of why.
		class = ClassUnknown
	}
	tries := c.autoTries()
	if c.tm.AutoRecoverTries <= 0 || tries >= c.tm.AutoRecoverTries || !Retryable(class, c.tm.AutoRecoverWave) {
		return Action{Kind: DoAlert}
	}
	if c.age(c.la) < c.tm.Backoff[min(tries, len(c.tm.Backoff)-1)] {
		return Action{Kind: DoNone}
	}
	from := FirstMissing(c.in.Files)
	if from == "" {
		return Action{Kind: DoAlert}
	}
	return Action{Kind: DoLaunchRecover, Phase: PhaseRecover, From: from, Who: WhoAuto}
}

// An Attempt is one launch of the unit: what the core needs to say to start it. The nonce is only ever in the unit's own
// environment.
type Attempt struct {
	RunID string
	Nonce string
	Phase string
	// N is the number of the attempt, from 1 to 999.
	N int
}

// Validate says whether every part of the attempt is of the shape a unit is started with.
func (a Attempt) Validate() error {
	switch {
	case !ValidUUID(a.RunID):
		return errors.New("the run id is not a lower case UUID")
	case !ValidNonce(a.Nonce):
		return errors.New("the nonce is not 32 lower case hex digits")
	case !ValidPhase(a.Phase):
		return errors.New("the phase is not one of the phases")
	case a.N < 1 || a.N > 999:
		return errors.New("the attempt number is not from 1 to 999")
	}
	return nil
}
