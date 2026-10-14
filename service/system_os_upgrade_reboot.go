package service

import (
	"context"
	"slices"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"go.uber.org/zap"
)

// What the restart of an upgrade calls, as vars for a test to put its own in their place. The restart is the internal one (runSystemctlPowerAction): the public ones, which the
// owner's page uses to restart the machine, refuse while an upgrade runs, and the upgrade's own restart is not that.
var (
	osUpgradeSync        = syncDisks
	osUpgradePowerAction = runSystemctlPowerAction
)

// osUpgradeUnitQuiet says whether no unit of the upgrade runs and systemd said so: asked now, and not through the question that requests share.
func (s *systemService) osUpgradeUnitQuiet() bool {
	active, answered := s.systemPackageUpdater().unitState(systemOSUpgradeUnit)
	return answered && !active
}

// osUpgradeLockRefusal is dpkg_locked when something else changes the packages of the machine (a package manager holds dpkg's lock, another unit that changes packages runs), or
// when whether one does cannot be seen: a lock that cannot be seen is not one that nobody holds. The unit of the upgrade and its flag are not counted against it.
func (s *systemService) osUpgradeLockRefusal(ctx context.Context) *OSUpgradeRefusal {
	u := s.systemPackageUpdater()
	askCtx, cancel := context.WithTimeout(ctx, osPlanAskTimeout)
	defer cancel()
	reason, busy, err := u.maintenanceState(askCtx, systemOSUpgradeUnit, u.command)
	switch {
	case busy:
		return refuseUpgrade(OSUpgradeDpkgLocked, "something else is changing the packages of the machine", map[string]any{"reason": reason})
	case err != nil:
		return refuseUpgrade(OSUpgradeDpkgLocked, "whether something else is changing the packages could not be seen", map[string]any{"unknown": true})
	}
	return nil
}

// RebootSystemOSUpgrade restarts the machine that waits for it (or a refresh that asks for it): the owner's click. It is valid in awaiting_reboot, reboot_stalled and
// reboot_first, with no unit of the upgrade running, no package manager at work, and for a hop with what stands before the restart green, asked again now: a file that said green a
// half minute ago is not what the machine says. The owner's word goes in the log first, then what the machine holds in memory is written to its disks, and then systemd is asked;
// a restart that systemd refuses is taken back in the log and said. It does not wait for the machine to go.
func (s *systemService) RebootSystemOSUpgrade() (OSUpgradeStatus, error) {
	ctx := context.Background()
	run, state, refusal := s.osUpgradeCurrent()
	if refusal != nil {
		return OSUpgradeStatus{}, refusal
	}
	if !osUpgradeCanRestart(state) {
		return OSUpgradeStatus{}, refuseUpgrade(OSUpgradeNotAwaitingReboot, "the machine does not wait for its restart", map[string]any{"state": state.Name})
	}
	if doc := s.osUpgradeKeepBarrier(ctx, run, state); doc != nil && !doc.OK {
		return OSUpgradeStatus{}, refuseUpgrade(OSUpgradeBarrierFailed, "something that stands before the restart is not well", map[string]any{"checks": doc.Checks})
	}
	if !s.osUpgradeUnitQuiet() {
		return OSUpgradeStatus{}, refuseUpgrade(OSUpgradeUnitActive, "a unit of the upgrade runs, or systemd does not answer", nil)
	}
	if refusal := s.osUpgradeLockRefusal(ctx); refusal != nil {
		return OSUpgradeStatus{}, refusal
	}

	u := s.systemPackageUpdater()
	if run.Meta.Kind == osrun.RunHop {
		// the machine as it is when the restart is asked for: what the owner changed since the hop is not what the restart did to it
		s.osUpgradeTakeRestartFacts(ctx, run)
	}
	unit := s.osUpgradeUnitNow()
	err := withOSUpgradeLock(func() error {
		var refusal *OSUpgradeRefusal
		run, state, refusal = s.osUpgradeCurrentWith(unit)
		switch {
		case refusal != nil:
			return refusal
		case !osUpgradeCanRestart(state):
			return refuseUpgrade(OSUpgradeNotAwaitingReboot, "the machine does not wait for its restart", map[string]any{"state": state.Name})
		}
		if err := appendOSUpgradeMarker(run.Dir, run.Nonce, osrun.Marker{Kind: osrun.MkRebootRequested, At: u.now().UTC()}); err != nil {
			return osUpgradeStartFailed("the log of the upgrade could not say that the restart was asked for", err)
		}
		return nil
	})
	if err != nil {
		return OSUpgradeStatus{}, err
	}

	osUpgradeSync()
	logger.Info("the owner restarts the machine for an upgrade of the operating system", zap.String("run", run.ID))
	if err := osUpgradePowerAction("reboot"); err != nil {
		logger.Error("the machine could not be restarted for an upgrade of the operating system", zap.String("run", run.ID), zap.Error(err))
		appendErr := withOSUpgradeLock(func() error {
			return appendOSUpgradeMarker(run.Dir, run.Nonce, osrun.Marker{Kind: osrun.MkRebootFailed, At: u.now().UTC()})
		})
		if appendErr != nil {
			logger.Error("the log of the upgrade could not say that the restart failed", zap.String("run", run.ID), zap.Error(appendErr))
		}
		return OSUpgradeStatus{}, refuseUpgrade(OSUpgradeRebootFailed, "the machine could not be asked to restart", map[string]any{"detail": "systemd refused the restart"})
	}
	s.nudgeOSUpgradeSettle()
	return s.GetSystemOSUpgradeStatus(), nil
}

// osUpgradeCanRestart says whether the restart is the next thing in this state: the machine waits for it, asked a while ago and nothing came of it, or a refresh wants it.
func osUpgradeCanRestart(state osrun.State) bool {
	return slices.Contains([]string{osrun.StateAwaitingReboot, osrun.StateRebootStalled, osrun.StateRebootFirst}, state.Name)
}
