package service

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"go.uber.org/zap"
)

// The reasons that a unit was not started (osUpgradeLaunchError.Kind).
const (
	// launchUnitBusy: a unit of the upgrade runs, or is on its way, or systemd does not answer: nothing is started while one may be running.
	launchUnitBusy = "unit_busy"
	// launchDamaged: the files of the run are not the ones that the core wrote. Nothing is started from them again.
	launchDamaged = "run_damaged"
	// launchNothing: the attempt would have no block to run.
	launchNothing = "nothing_to_run"
	// launchNotAllowed: the log of this run would not take such an attempt (a phase that does not belong to its kind or to where it is).
	launchNotAllowed = "not_allowed"
	// launchUnsupported: this machine cannot start a unit (not root, no systemd-run).
	launchUnsupported = "unsupported"
	// launchStartFailed: systemd-run did not start the unit; START_FAILED is in the log.
	launchStartFailed = "start_failed"
	// launchIO: a file of the run could not be read or written.
	launchIO = "io"
)

// osUpgradeLaunchError says why no unit was started for an attempt. Detail is English and short; the cause is for the log.
type osUpgradeLaunchError struct {
	Kind   string
	Detail string
	Err    error
}

func (e *osUpgradeLaunchError) Error() string {
	if e.Err != nil {
		return e.Kind + ": " + e.Detail + ": " + e.Err.Error()
	}
	return e.Kind + ": " + e.Detail
}

func (e *osUpgradeLaunchError) Unwrap() error { return e.Err }

func launchFailure(kind, detail string, err error) *osUpgradeLaunchError {
	return &osUpgradeLaunchError{Kind: kind, Detail: detail, Err: err}
}

// launchAttempt starts the unit of the next attempt of an open run, and writes the log of it first: the core takes osUpgradeMu, reads the run again, and
// only then
//
//  1. asks systemd, and refuses while a unit of the upgrade runs or is on its way, or systemd does not answer;
//  2. works out the number of the attempt (one more than the log counts: a number is launched at most once) and refuses a phase that the log would not take;
//  3. checks SHA256SUMS against the files, and refuses a run that is damaged;
//  4. writes the list of blocks of the attempt, from the files of the backup folder, and appends ATTEMPT to the log;
//  5. calls systemd-run, and appends START_FAILED if it fails.
//
// from is the block that a repair begins with; who is osrun.WhoAuto or osrun.WhoOwner. An *osUpgradeLaunchError is returned for what did not start a unit.
func (s *systemService) launchAttempt(runID, phase, who string) error {
	osUpgradeMu.Lock()
	defer osUpgradeMu.Unlock()
	return s.launchAttemptLocked(runID, phase, who)
}

// launchAttemptLocked is launchAttempt for a caller that holds osUpgradeMu.
func (s *systemService) launchAttemptLocked(runID, phase, who string) error {
	if !osrun.ValidPhase(phase) || (who != osrun.WhoAuto && who != osrun.WhoOwner) {
		return launchFailure(launchNotAllowed, "not a phase or not a caller of one", nil)
	}
	run, err := readOSUpgradeRun(runID)
	if err != nil {
		return launchFailure(launchIO, "the run cannot be read", err)
	}
	if !osrun.ValidNonce(run.Nonce) || len(run.Markers) < 2 {
		return launchFailure(launchIO, "the log of the run has no nonce or no RUN line", nil)
	}
	u := s.systemPackageUpdater()
	support := u.support()
	if !support.supported {
		return launchFailure(launchUnsupported, support.reason, nil)
	}

	// 1. nothing is started while a unit of the upgrade may be running: systemd is asked now, and the reducer says whether an attempt is on its way
	active, answered := u.unitState(systemOSUpgradeUnit)
	in := s.osUpgradeInputWith(run, osrun.Unit{Active: active, Answered: answered})
	if !answered || active {
		return launchFailure(launchUnitBusy, "a unit of the upgrade runs, or systemd does not answer", nil)
	}
	state := osrun.Reduce(in)
	switch {
	case live(state.Name):
		return launchFailure(launchUnitBusy, "an attempt of the run is on its way", nil)
	case state.Closed || state.Name == osrun.StateManual || state.Name == osrun.StateManualDone:
		return launchFailure(launchNotAllowed, "the owner closed the run or took it over", nil)
	}

	// 2. the number, and whether the log would take this attempt
	n := osrun.NextAttempt(in)
	from := ""
	if phase == osrun.PhaseRecover {
		from = osrun.FirstMissing(run.Files)
	}
	attempt := osrun.Marker{Kind: osrun.MkAttempt, N: n, Phase: phase, From: from, Who: who, At: u.now().UTC()}
	if n > 999 || osrun.NextAttempt(osrun.Input{Markers: append(slices.Clone(run.Markers), attempt)}) != n+1 {
		return launchFailure(launchNotAllowed, "the log of the run takes no attempt of this phase now", nil)
	}

	// 3. the files that the unit would run
	if err := verifyOSUpgradeRun(run.Dir, run.Meta.Kind); err != nil {
		logger.Error("the files of an upgrade are not the ones the core wrote: nothing is started from them", zap.String("run", runID), zap.Error(err))
		if appendErr := s.appendAttemptThatFailed(run, attempt); appendErr != nil {
			logger.Error("the log of an upgrade could not say that its files are damaged", zap.String("run", runID), zap.Error(appendErr))
		}
		return launchFailure(launchDamaged, "the files of the run are not the ones that were written", err)
	}

	// 4. the blocks of the attempt, from the files, and the line that says the number is taken
	list := osrun.BlockList(phase, run.Files)
	if len(list) == 0 && phase != osrun.PhaseUnwind {
		return launchFailure(launchNothing, "there is no block left to run", nil)
	}
	var listText strings.Builder
	for _, id := range list {
		if !osrun.ValidBlock(id) || !slices.Contains(osUpgradeBlockIDs(run.Meta.Kind), id) {
			return launchFailure(launchNotAllowed, "a block that this run does not have", nil)
		}
		listText.WriteString(id + "\n")
	}
	if err := osUpgradeWriteFile(run.Dir+"/"+osUpgradeListsDir+"/"+strconv.Itoa(n)+".list", []byte(listText.String()), 0o600); err != nil {
		return launchFailure(launchIO, "the list of blocks cannot be written", err)
	}
	if err := appendOSUpgradeMarker(run.Dir, run.Nonce, attempt); err != nil {
		return launchFailure(launchIO, "the log cannot be written", err)
	}

	// 5. the unit
	args, err := osUpgradeUnitArgs(osrun.Attempt{RunID: run.ID, Nonce: run.Nonce, Phase: phase, N: n}, osUpgradeUnitRoot+"/"+run.ID)
	if err == nil {
		var output []byte
		output, err = u.start(support.systemdPath, systemOSUpgradeUnit, args...)
		if err != nil {
			err = errors.Join(err, errors.New(osplan.SanitizeLine(trimSystemPackageOutput(output))))
		}
	}
	if err != nil {
		failed := osrun.Marker{Kind: osrun.MkStartFailed, N: n, At: u.now().UTC()}
		if appendErr := appendOSUpgradeMarker(run.Dir, run.Nonce, failed); appendErr != nil {
			logger.Error("the log of an upgrade could not say that its unit did not start", zap.String("run", runID), zap.Error(appendErr))
		}
		return launchFailure(launchStartFailed, "systemd-run did not start the unit", err)
	}
	return nil
}

// appendAttemptThatFailed writes an attempt that was refused, and that never began, to the log: the run is damaged, and the log says so.
func (s *systemService) appendAttemptThatFailed(run *osUpgradeRun, attempt osrun.Marker) error {
	if err := appendOSUpgradeMarker(run.Dir, run.Nonce, attempt); err != nil {
		return err
	}
	return appendOSUpgradeMarker(run.Dir, run.Nonce, osrun.Marker{Kind: osrun.MkStartFailed, N: attempt.N, At: attempt.At})
}

// live says whether a state is that of an attempt that is on its way or runs.
func live(state string) bool {
	switch state {
	case osrun.StateRefreshing, osrun.StateRehearsing, osrun.StateRunning, osrun.StateRecovering, osrun.StateVerifying, osrun.StateUnwinding:
		return true
	}
	return false
}
