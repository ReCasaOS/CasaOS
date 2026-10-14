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
// and the ones after it, as the owner's attempt, which also gives the automatic repairs a new count. A check after the restart that did not end (a block of it stopped, or its
// unit was lost) is made again by the same click, as a check; the verdict of the one before is taken away so that the new one is not taken for it.
//
// A machine that was checked, and whose verdict has findings or errors, is checked again by the same click, and what that asks for is the judge only: the verdict is taken away and
// the core's next look makes it again, from the machine as it is then (what was slow to come back, what the owner mended). No unit is started for it.
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
	if osUpgradeJudged(state) {
		return s.osUpgradeJudgeAgain(run.ID, state)
	}
	if lock := s.osUpgradeLockRefusal(context.Background()); lock != nil {
		return OSUpgradeStatus{}, refuseUpgrade(OSUpgradeBusy, lock.Detail, lock.Data)
	}

	phase := osrun.PhaseRecover
	if state.Phase == osrun.PhaseVerify {
		phase = osrun.PhaseVerify
	}
	unit := s.osUpgradeUnitNow()
	var launch *osUpgradeLaunch
	err := withOSUpgradeLock(func() (err error) {
		launch, err = s.launchPrepareLocked(run.ID, phase, osrun.WhoOwner, unit)
		if err == nil && phase == osrun.PhaseVerify {
			removeOSUpgradeVerdict(run)
		}
		return err
	})
	if err == nil {
		err = s.launchRun(launch)
	}
	if err != nil {
		return OSUpgradeStatus{}, osUpgradeLaunchRefusal(err, state)
	}
	logger.Info("the owner goes on with an upgrade of the operating system", zap.String("run", run.ID), zap.String("phase", phase))
	s.nudgeOSUpgradeSettle()
	return s.GetSystemOSUpgradeStatus(), nil
}

// osUpgradeJudged says whether a state is the one of a machine that was checked and judged, and whose verdict has something in it: the check that is asked for again is the judge's.
func osUpgradeJudged(state osrun.State) bool {
	return state.Name == osrun.StateSucceededWithFindings || state.Name == osrun.StateFailed && state.Reason == osrun.ReasonVerify && state.Class == ""
}

// osUpgradeJudgeAgain takes the verdict away, under the lock and after the run has been looked at again, so that the next look judges the machine anew.
func (s *systemService) osUpgradeJudgeAgain(runID string, was osrun.State) (OSUpgradeStatus, error) {
	unit := s.osUpgradeUnitNow()
	err := withOSUpgradeLock(func() error {
		run, state, refusal := s.osUpgradeCurrentWith(unit)
		switch {
		case refusal != nil:
			return refusal
		case run.ID != runID || !osUpgradeJudged(state):
			return refuseUpgrade(OSUpgradeNotResumable, "the upgrade cannot be gone on with from where it is", map[string]any{"state": state.Name, "class": state.Class})
		}
		removeOSUpgradeVerdict(run)
		return nil
	})
	if err != nil {
		return OSUpgradeStatus{}, err
	}
	logger.Info("the owner asks for the machine to be judged again after an upgrade of the operating system", zap.String("run", runID), zap.String("state", was.Name))
	s.nudgeOSUpgradeSettle()
	return s.GetSystemOSUpgradeStatus(), nil
}

// removeOSUpgradeVerdict takes verify.json of a run away: a verdict that is not there has not been made.
func removeOSUpgradeVerdict(run *osUpgradeRun) {
	if err := os.Remove(filepath.Join(run.Dir, osUpgradeVerifyFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Error("the verdict of the check before could not be taken away", zap.String("run", run.ID), zap.Error(err))
	}
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
