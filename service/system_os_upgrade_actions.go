package service

import (
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The buttons of an upgrade that is open: what the owner can ask of it once it is running. The server decides which of them are valid in a state (osrun.ActionsFor), and each of
// them looks again under the lock before it acts, since the log may have moved since it was read. None of them looks at the owner's switch: the switch is for what begins
// something, and a machine that is half upgraded is never trapped behind it.

// The codes that the buttons refuse with (OSUpgradeRefusal.Code), besides OSUpgradeBusy and OSUpgradeBadRequest.
const (
	// OSUpgradeNoRun: there is no upgrade to act on. OSUpgradeRunUnreadable: there is one, and it cannot be read.
	OSUpgradeNoRun         = "no_run"
	OSUpgradeRunUnreadable = "run_unreadable"
	// OSUpgradeNotAbortable says why the run cannot be called off (Data["reason"]): point_of_no_return, past_block or not_running.
	OSUpgradeNotAbortable = "not_abortable"
	// OSUpgradeNotAwaitingReboot: the machine does not wait for its restart (Data["state"]). OSUpgradeBarrierFailed: what stands before the restart is red
	// (Data["checks"]). OSUpgradeUnitActive: a unit of the upgrade runs, or systemd does not answer. OSUpgradeDpkgLocked: something else changes the packages.
	OSUpgradeNotAwaitingReboot = "not_awaiting_reboot"
	OSUpgradeBarrierFailed     = "barrier_failed"
	OSUpgradeUnitActive        = "unit_active"
	OSUpgradeDpkgLocked        = "dpkg_locked"
	// OSUpgradeRebootFailed: the restart could not be asked of systemd.
	OSUpgradeRebootFailed = "reboot_failed"
	// OSUpgradeNotResumable: the run cannot be gone on with from where it is (Data["state"], Data["class"]).
	OSUpgradeNotResumable = "not_resumable"
	// OSUpgradeNotTerminal: the run is not in a state that the word of the owner ends or leaves (Data["state"]). OSUpgradeWordRequired and OSUpgradeBadWord are for the typed word
	// that a machine that is half upgraded needs to be let go of.
	OSUpgradeNotTerminal  = "not_terminal"
	OSUpgradeWordRequired = "word_required"
	OSUpgradeBadWord      = "bad_word"
	// OSUpgradeCloseFailed: the run could not be closed (its backup could not be put away, a file could not be removed): it is as it was.
	OSUpgradeCloseFailed = "close_failed"
)

// OSUpgradeActionCodes are the codes that the buttons can refuse with, besides the ones that every request can be refused with.
var OSUpgradeActionCodes = []string{
	OSUpgradeNoRun, OSUpgradeRunUnreadable, OSUpgradeNotAbortable, OSUpgradeNotAwaitingReboot, OSUpgradeBarrierFailed, OSUpgradeUnitActive, OSUpgradeDpkgLocked,
	OSUpgradeRebootFailed, OSUpgradeNotResumable, OSUpgradeNotTerminal, OSUpgradeWordRequired, OSUpgradeBadWord, OSUpgradeCloseFailed, OSUpgradeBusy,
}

// osUpgradeCurrent is the latest run and its state, for a button: no_run when there is none, and run_unreadable when there is one that cannot be read.
func (s *systemService) osUpgradeCurrent() (*osUpgradeRun, osrun.State, *OSUpgradeRefusal) {
	return s.osUpgradeCurrentWith(s.osUpgradeSharedUnit())
}

// osUpgradeSharedUnit is what systemd says of the unit, through the question that requests share, and nothing is asked when there is no run.
func (s *systemService) osUpgradeSharedUnit() osrun.Unit {
	if _, current := currentOSUpgrade(); current == osUpgradeNoRun {
		return osrun.Unit{Answered: true}
	}
	return s.systemPackageUpdater().osUpgradeUnit()
}

// osUpgradeCurrentWith is osUpgradeCurrent with the answer of systemd that the caller has: a caller that holds the lock asked before it took it.
func (s *systemService) osUpgradeCurrentWith(unit osrun.Unit) (*osUpgradeRun, osrun.State, *OSUpgradeRefusal) {
	id, current := currentOSUpgrade()
	switch current {
	case osUpgradeNoRun:
		return nil, osrun.State{}, refuseUpgrade(OSUpgradeNoRun, "there is no upgrade of the operating system to act on", nil)
	case osUpgradeBadRun:
		return nil, osrun.State{}, refuseUpgrade(OSUpgradeRunUnreadable, "the upgrade that is open cannot be read", map[string]any{"run_id": ""})
	}
	run, err := readOSUpgradeRun(id)
	if err != nil {
		return nil, osrun.State{}, refuseUpgrade(OSUpgradeRunUnreadable, "the upgrade that is open cannot be read", map[string]any{"run_id": id})
	}
	return run, osrun.Reduce(s.osUpgradeInputWith(run, unit)), nil
}
