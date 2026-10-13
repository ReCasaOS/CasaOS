package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"go.uber.org/zap"
)

// ResumeSystemOSUpgrade goes on with a hop that stopped after its point of no return: the owner's click, which starts the same repair that the core would start by itself if
// the shipped setting allowed it (it does not: nothing repairs a machine that is half upgraded without a word). The repair is the repair block and then the block that did not end
// and the ones after it, as the owner's attempt, which also gives the automatic repairs a new count. A check after the restart that failed, or could not be started, is made
// again by the same click, as a check; the verdict of the one before is taken away so that the new one is not taken for it.
//
// It is valid where osrun.ActionsFor says so, with no unit that runs and no package manager at work.
func (s *systemService) ResumeSystemOSUpgrade() (OSUpgradeStatus, error) {
	run, state, refusal := s.osUpgradeCurrent()
	if refusal != nil {
		return OSUpgradeStatus{}, refusal
	}
	if !slices.Contains(osrun.ActionsFor(state), osrun.ActionResume) {
		return OSUpgradeStatus{}, refuseUpgrade(OSUpgradeNotResumable, "the upgrade cannot be gone on with from where it is", map[string]any{"state": state.Name, "class": state.Class})
	}
	if lock := s.osUpgradeLockRefusal(context.Background()); lock != nil {
		return OSUpgradeStatus{}, refuseUpgrade(OSUpgradeBusy, lock.Detail, lock.Data)
	}

	phase := osrun.PhaseRecover
	if state.Phase == osrun.PhaseVerify {
		phase = osrun.PhaseVerify
	}
	osUpgradeMu.Lock()
	err := s.launchAttemptLocked(run.ID, phase, osrun.WhoOwner)
	if err == nil && phase == osrun.PhaseVerify {
		if rmErr := os.Remove(filepath.Join(run.Dir, osUpgradeVerifyFile)); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			logger.Error("the verdict of the check before could not be taken away", zap.String("run", run.ID), zap.Error(rmErr))
		}
	}
	osUpgradeMu.Unlock()
	if err != nil {
		return OSUpgradeStatus{}, osUpgradeLaunchRefusal(err, state)
	}
	logger.Info("the owner goes on with an upgrade of the operating system", zap.String("run", run.ID), zap.String("phase", phase))
	s.nudgeOSUpgradeSettle()
	return s.GetSystemOSUpgradeStatus(), nil
}

// osUpgradeLaunchRefusal is what a unit that was not started is said as, to a button: a unit that runs or systemd that does not answer, a run that has nothing left to run or that
// the log would not take an attempt of, files that are not the ones that were written, or a unit that systemd did not start.
func osUpgradeLaunchRefusal(err error, state osrun.State) error {
	var launch *osUpgradeLaunchError
	if !errors.As(err, &launch) {
		return err
	}
	switch launch.Kind {
	case launchUnitBusy:
		return refuseUpgrade(OSUpgradeUnitActive, "a unit of the upgrade runs, or systemd does not answer", nil)
	case launchNothing, launchNotAllowed:
		return refuseUpgrade(OSUpgradeNotResumable, "the upgrade has nothing to go on with from where it is", map[string]any{"state": state.Name, "class": state.Class})
	case launchDamaged:
		return refuseUpgrade(OSUpgradeNotResumable, "the files of the upgrade are not the ones that were written", map[string]any{"state": state.Name, "class": state.Class, "reason": launch.Kind})
	}
	return osUpgradeStartFailed("the unit could not be started", err)
}
