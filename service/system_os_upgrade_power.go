package service

import (
	"context"
	"slices"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// OSUpgradeOpen is the refusal of the owner's page to restart or to switch off the machine while an upgrade of the operating system works on it (Data["state"]): the restart
// would cut dpkg short and leave the machine half upgraded with nothing to say what is done. The upgrade's own restart is another call (osUpgradePowerAction), and is not refused.
const OSUpgradeOpen = "os_upgrade_open"

// systemPowerAction is what the owner's page calls to restart or to switch off the machine ("reboot" or "poweroff"). A var, for the tests, which must never be the real one.
var systemPowerAction = runSystemctlPowerAction

// osUpgradeWorkStates are the states in which the unit works on the machine, or is about to start one that does. Every other state lets the machine be restarted: it waits for
// its restart, or for the owner, or the unit is gone and what stopped it is a click away.
var osUpgradeWorkStates = []string{
	osrun.StateRefreshing, osrun.StateRehearsing, osrun.StateRunning, osrun.StateUnwinding, osrun.StateRecovering, osrun.StateVerifying, osrun.StateResuming,
}

// osUpgradeKeepsPower says whether a state keeps the restart and the shutdown of the owner's page away.
func osUpgradeKeepsPower(state osrun.State) bool {
	return slices.Contains(osUpgradeWorkStates, state.Name)
}

// osUpgradePowerRefusal is os_upgrade_open when the latest run is in a state that keeps the page's restart and shutdown away, and nil for no run, for one that is in another
// state, and for one that cannot be read (that run says nothing of whether it works, and the owner's way out of it is to close it with the typed word). It reads the run and
// takes no lock: a request that comes at the very moment that a start launches its unit meets the machine as it was a moment before.
func (s *systemService) osUpgradePowerRefusal() *OSUpgradeRefusal {
	id, current := currentOSUpgrade()
	if current != osUpgradeHasRun {
		return nil
	}
	run, err := readOSUpgradeRun(id)
	if err != nil {
		return nil
	}
	in := s.osUpgradeInput(run)
	state := osrun.Reduce(in)
	if unit := in.Unit; unit.Answered && unit.Active {
		// a state that says nothing of the work (a run found damaged) does not say that the unit is gone: dpkg may be at work
		return refuseUpgrade(OSUpgradeOpen, "an upgrade of the operating system is changing the machine: it cannot be restarted or switched off until the upgrade stops or asks for it", map[string]any{"state": state.Name})
	}
	if !osUpgradeKeepsPower(state) {
		// The machine that waits for its restart is restarted by the owner's click, and the checks that stand before that restart bind this click too: a page that restarts a machine
		// that would stop in emergency mode at its next start is the same mistake as the button that is not offered.
		if doc := s.osUpgradeKeepBarrier(context.Background(), run, state); doc != nil && !doc.OK {
			return refuseUpgrade(OSUpgradeBarrierFailed, "something that stands before the restart of the upgrade is not well", map[string]any{"checks": doc.Checks})
		}
		return nil
	}
	return refuseUpgrade(OSUpgradeOpen, "an upgrade of the operating system is changing the machine: it cannot be restarted or switched off until the upgrade stops or asks for it", map[string]any{"state": state.Name})
}

// systemPower restarts or switches off the machine on the owner's word, unless an upgrade works on it.
func (s *systemService) systemPower(action string) error {
	if refusal := s.osUpgradePowerRefusal(); refusal != nil {
		return refusal
	}
	return systemPowerAction(action)
}
