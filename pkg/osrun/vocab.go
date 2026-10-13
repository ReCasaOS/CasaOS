// Package osrun is what the dashboard knows of an operating system upgrade that runs in a unit of its own: the words
// it and the unit use (phases, blocks, classes, states, markers), the reader of the unit's log, and the pure reducer
// that turns that log, a few files of the run and what systemd says into the state of the run and into the one thing
// to do next.
//
// Nothing here touches the machine: no clock, no file, no systemd, no network. The core reads the box and calls in;
// the unit (a shell driver the core writes) only ever appends marker lines. The package imports the standard library
// and nothing of the rest of the core, so that the upgrade's unit and its alerts agree on one vocabulary.
package osrun

import "slices"

// What a unit does, in the order a hop goes through them (Attempt.Phase, the PHASE of the unit's environment).
const (
	// PhaseRefresh brings the current release to its latest state (no point of no return).
	PhaseRefresh = "refresh"
	// PhaseRehearse runs the reversible half of a hop and stops, with the new release's sources in place.
	PhaseRehearse = "rehearse"
	// PhaseRun is the hop itself, up to the point where the machine has to restart.
	PhaseRun = "run"
	// PhaseRecover finishes a hop that stopped after its point of no return.
	PhaseRecover = "recover"
	// PhaseVerify is the check of the machine after the restart.
	PhaseVerify = "verify"
	// PhaseUnwind puts back what a run that stopped before its point of no return had changed.
	PhaseUnwind = "unwind"
)

// Phases are the phases a unit can be started with.
var Phases = []string{PhaseRefresh, PhaseRehearse, PhaseRun, PhaseRecover, PhaseVerify, PhaseUnwind}

// What a run is for (the kind on its RUN marker).
const (
	RunRefresh   = "refresh"
	RunHop       = "hop"
	RunRehearsal = "rehearsal"
)

// RunKinds are the kinds of run.
var RunKinds = []string{RunRefresh, RunHop, RunRehearsal}

// The blocks a driver can run: the nine blocks of the hop, the block that repairs a hop that stopped after its point
// of no return, and the four blocks of a refresh.
const (
	BlockVerify    = "verify"
	BlockTimers    = "timers"
	BlockSnapshot  = "snapshot"
	BlockSources   = "sources"
	BlockDownload  = "download"
	BlockMinimal   = "minimal"
	BlockFull      = "full"
	BlockPrereboot = "prereboot"
	BlockAfter     = "after"
	BlockForward   = "forward"
	BlockRCheck    = "rcheck"
	BlockRSources  = "rsources"
	BlockRUpgrade  = "rupgrade"
	BlockRDone     = "rdone"
)

// BlockIDs are the blocks a driver can run; the driver's own list of them is the same set.
var BlockIDs = []string{
	BlockVerify, BlockTimers, BlockSnapshot, BlockSources, BlockDownload, BlockMinimal, BlockFull, BlockPrereboot, BlockAfter,
	BlockForward, BlockRCheck, BlockRSources, BlockRUpgrade, BlockRDone,
}

// The ids a STOP marker can carry that are not a block's: the driver's own checks, a list it cannot read, and the unwind.
const (
	StopDriver = "driver"
	StopList   = "list"
	StopUnwind = "unwind"
)

// How a block that stopped is told (STOP's class, State.Class and the reason of a failed run). The page has a sentence for each.
const (
	ClassBusy            = "busy"
	ClassNetwork         = "network"
	ClassAptFailed       = "apt_failed"
	ClassDpkgBroken      = "dpkg_broken"
	ClassUnknown         = "unknown"
	ClassDisk            = "disk"
	ClassPlan            = "plan"
	ClassGuard           = "guard"
	ClassConverge        = "converge"
	ClassBarrierKernel   = "barrier_kernel"
	ClassBarrierInitrd   = "barrier_initrd"
	ClassBarrierGrub     = "barrier_grub"
	ClassBarrierSSHD     = "barrier_sshd"
	ClassBarrierResolver = "barrier_resolver"
	ClassBarrierOther    = "barrier_other"
	ClassIdentity        = "identity"
	ClassTools           = "tools"
	ClassUnit            = "unit"
	ClassIntegrity       = "integrity"
	ClassAbort           = "abort"
)

// Classes are the classes of a stop. Every one is lower case letters and underscores, which is what the driver lets
// through from a block's own word.
var Classes = []string{
	ClassBusy, ClassNetwork, ClassAptFailed, ClassDpkgBroken, ClassUnknown, ClassDisk, ClassPlan, ClassGuard, ClassConverge,
	ClassBarrierKernel, ClassBarrierInitrd, ClassBarrierGrub, ClassBarrierSSHD, ClassBarrierResolver, ClassBarrierOther,
	ClassIdentity, ClassTools, ClassUnit, ClassIntegrity, ClassAbort,
}

// The states of a run (State.Name).
const (
	StateIdle                  = "idle"
	StateRefreshing            = "refreshing"
	StateRebootFirst           = "reboot_first"
	StateRefreshed             = "refreshed"
	StateRehearsing            = "rehearsing"
	StateRehearsed             = "rehearsed"
	StateRehearsalDone         = "rehearsal_done"
	StateRunning               = "running"
	StateUnwinding             = "unwinding"
	StateRecovering            = "recovering"
	StateAwaitingReboot        = "awaiting_reboot"
	StateRebooting             = "rebooting"
	StateRebootStalled         = "reboot_stalled"
	StateResuming              = "resuming"
	StateVerifying             = "verifying"
	StateSucceeded             = "succeeded"
	StateSucceededWithFindings = "succeeded_with_findings"
	StateFailed                = "failed"
	StateInterrupted           = "interrupted"
	StateStoppedAtWaypoint     = "stopped_at_waypoint"
	StateNeedsAttention        = "needs_attention"
	StateManual                = "manual"
	StateManualDone            = "manual_done"
)

// States are the states of a run.
var States = []string{
	StateIdle, StateRefreshing, StateRebootFirst, StateRefreshed, StateRehearsing, StateRehearsed, StateRehearsalDone, StateRunning,
	StateUnwinding, StateRecovering, StateAwaitingReboot, StateRebooting, StateRebootStalled, StateResuming, StateVerifying,
	StateSucceeded, StateSucceededWithFindings, StateFailed, StateInterrupted, StateStoppedAtWaypoint, StateNeedsAttention,
	StateManual, StateManualDone,
}

// Why a failed run failed, besides a class: what the run was doing when the machine or the unit gave out, and the
// checks after the restart (State.Reason of a failed state).
const (
	ReasonStart             = "start"
	ReasonVerify            = "verify"
	ReasonHalf              = "half"
	ReasonInterruptedPower  = "interrupted_power"
	ReasonInterruptedKilled = "interrupted_killed"
)

// Why a run needs attention (State.Reason of a needs_attention state).
const (
	ReasonSystemdSilent = "systemd_silent"
	ReasonUnwindPartial = "unwind_partial"
	ReasonRunDamaged    = "run_damaged"
)

// How a run was interrupted (State.Cause).
const (
	CausePower  = "power"
	CauseKilled = "killed"
)

// The buttons the server can offer, in the order it lists them (ActionsFor).
const (
	ActionAbort       = "abort"
	ActionReboot      = "reboot"
	ActionResume      = "resume"
	ActionAcknowledge = "acknowledge"
	ActionDiscard     = "discard"
	ActionTakeover    = "takeover"
)

// ActionNames are the buttons, in the order ActionsFor lists them.
var ActionNames = []string{ActionAbort, ActionReboot, ActionResume, ActionAcknowledge, ActionDiscard, ActionTakeover}

// AckWord is what the owner types to close a machine that is half upgraded: closing the run drops the flag that keeps
// backups and automatic updates away from a machine that dpkg has begun to change.
const AckWord = "I-KNOW-THIS-MACHINE-IS-HALF-UPGRADED"

// Who launched an attempt (ATTEMPT's who).
const (
	WhoAuto  = "auto"
	WhoOwner = "owner"
)

func isOneOf(list []string, s string) bool { return slices.Contains(list, s) }

// ValidPhase reports whether s is a phase.
func ValidPhase(s string) bool { return isOneOf(Phases, s) }

// ValidBlock reports whether s is the id of a block.
func ValidBlock(s string) bool { return isOneOf(BlockIDs, s) }

// ValidClass reports whether s is a class of stop.
func ValidClass(s string) bool { return isOneOf(Classes, s) }

// Files is the part of the run's folder and of the backup folder that the reducer reads. The point of no return is
// read from these files and never from a marker: a unit that died before it wrote its marker is still classified right.
type Files struct {
	// Begun is the marker file that the minimal upgrade's block writes just before apt is let to change a package;
	// DoneMinimal, DoneFull, DonePrereboot and DoneAfter are the files the blocks of the same name end with.
	Begun, DoneMinimal, DoneFull, DonePrereboot, DoneAfter bool
	// DoneTimers says that the block that stops the timers of apt has ended; AptOrig that the copy of the box's sources
	// is in the backup folder; AbortFlag that the core has written the owner's call to stop.
	DoneTimers, AptOrig, AbortFlag bool
	// BootIDFile is the first line of the backup folder's boot_id file, the id of the boot that the last block of the hop
	// ran in: "" when there is none, which is also how a restart that already happened shows.
	BootIDFile string
}

// PNR is the point of no return: packages are being or have been changed, and the old release can no longer be put back.
func (f Files) PNR() bool { return f.Begun || f.DoneMinimal }

// BlockList is what a driver is asked to run for a phase, from the files of the run: the blocks, in order, as the driver
// takes them (a block that already has its done file is skipped by the driver, not left out here, except for recover).
// For a repair it is the repair block (when the first block of the hop that is not done is the minimal or the full
// upgrade: the machine may be half way through dpkg) and then that block and the ones after it, up to the last one
// before the restart; nothing when they are all done. The unwind is run with no block at all.
func BlockList(phase string, f Files) []string {
	switch phase {
	case PhaseRefresh:
		return []string{BlockRCheck, BlockRSources, BlockRUpgrade, BlockRDone}
	case PhaseRehearse:
		return []string{BlockVerify, BlockTimers, BlockSnapshot, BlockSources, BlockDownload}
	case PhaseRun:
		return []string{BlockVerify, BlockTimers, BlockSnapshot, BlockSources, BlockDownload, BlockMinimal, BlockFull, BlockPrereboot}
	case PhaseVerify:
		return []string{BlockAfter}
	case PhaseRecover:
		first := FirstMissing(f)
		switch first {
		case "":
			return nil
		case BlockPrereboot:
			return []string{BlockPrereboot}
		case BlockFull:
			return []string{BlockForward, BlockFull, BlockPrereboot}
		}
		return []string{BlockForward, BlockMinimal, BlockFull, BlockPrereboot}
	}
	return nil
}

// FirstMissing is the first block among the minimal upgrade, the full upgrade and the last block before the restart that has
// no done file, "" when all three have one.
func FirstMissing(f Files) string {
	switch {
	case !f.DoneMinimal:
		return BlockMinimal
	case !f.DoneFull:
		return BlockFull
	case !f.DonePrereboot:
		return BlockPrereboot
	}
	return ""
}
