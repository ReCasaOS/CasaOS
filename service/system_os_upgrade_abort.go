package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"go.uber.org/zap"
)

// osUpgradeAbortStep is called at each place of the abort where the unit's driver can move (the tests make it do so, in every order): after the run was looked at, after the flag
// was written, after the log was read again and the call decided, and after the unit was stopped.
var osUpgradeAbortStep = func(string) {}

// osUpgradeNotAbortable says why a run cannot be called off, "" when it can: it must be running (a refresh, a rehearsal or a hop), before its point of no return, and before the
// block that has it.
func osUpgradeNotAbortable(state osrun.State) string {
	switch {
	case state.Name != osrun.StateRefreshing && state.Name != osrun.StateRehearsing && state.Name != osrun.StateRunning:
		return "not_running"
	case state.PNR:
		return "point_of_no_return"
	case !state.Abortable:
		return "past_block"
	}
	return ""
}

func refuseNotAbortable(reason string) *OSUpgradeRefusal {
	return refuseUpgrade(OSUpgradeNotAbortable, "the upgrade cannot be called off now", map[string]any{"reason": reason})
}

// AbortSystemOSUpgrade calls off a run that is running and has not reached the point of no return: the unit is stopped and the core's next look undoes what the run did.
//
// The order of what it does is what keeps the call and the unit's driver in step. The core writes the flag in the run folder, and only then reads the log and the files. The
// driver writes that a block begins, and only then reads the flag. So one of the two always sees the other: either the driver sees the flag and stops before the minimal upgrade, or
// the core sees that the minimal upgrade has begun (or the point of no return) and takes the call back, removing the flag. The unit is never stopped while the minimal upgrade runs.
// A second call while one is under way answers with the status, and does nothing.
func (s *systemService) AbortSystemOSUpgrade() (OSUpgradeStatus, error) {
	run, state, refusal := s.osUpgradeCurrent()
	if refusal != nil {
		return OSUpgradeStatus{}, refusal
	}
	if reason := osUpgradeNotAbortable(state); reason != "" {
		return OSUpgradeStatus{}, refuseNotAbortable(reason)
	}
	osUpgradeAbortStep("checked")

	flag := filepath.Join(run.Dir, osUpgradeAbortFile)
	f, err := os.OpenFile(flag, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	switch {
	case errors.Is(err, fs.ErrExist):
		return s.GetSystemOSUpgradeStatus(), nil
	case err != nil:
		return OSUpgradeStatus{}, fmt.Errorf("the flag of the call could not be written: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(flag)
		return OSUpgradeStatus{}, fmt.Errorf("the flag of the call could not be written: %w", err)
	}
	f.Close()
	osUpgradeAbortStep("flag")

	fresh, err := readOSUpgradeRun(run.ID)
	if err != nil {
		os.Remove(flag)
		return OSUpgradeStatus{}, fmt.Errorf("the upgrade could not be read again: %w", err)
	}
	state = osrun.Reduce(s.osUpgradeInput(fresh))
	osUpgradeAbortStep("decided")
	if reason := osUpgradeNotAbortable(state); reason != "" {
		os.Remove(flag)
		return OSUpgradeStatus{}, refuseNotAbortable(reason)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := s.systemPackageUpdater().command(stopCtx, "systemctl", "stop", systemOSUpgradeUnit); err != nil {
		// the flag stands: the driver stops at its next block on it
		logger.Error("the unit of an upgrade of the operating system could not be stopped; the driver will stop at its next block", zap.String("run", run.ID), zap.Error(err))
	}
	osUpgradeAbortStep("stopped")
	logger.Info("an upgrade of the operating system was called off", zap.String("run", run.ID), zap.Int("attempt", state.Attempt))
	s.nudgeOSUpgradeSettle()
	return s.GetSystemOSUpgradeStatus(), nil
}
