package service

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

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
	// launchStartFailed: systemd-run did not start the unit, and systemd says that there is none; START_FAILED is in the log.
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

// withOSUpgradeLock runs fn with osUpgradeMu held, and lets go of it whatever fn does: a handler that panics (the router recovers it) must not leave the engine locked for
// good. The lock is held to read a run again, to decide, and to append a marker, and never across a question to systemd, a call of systemd-run, or any wait.
func withOSUpgradeLock(fn func() error) error {
	osUpgradeMu.Lock()
	defer osUpgradeMu.Unlock()
	return fn()
}

// osUpgradeUnitNow is what systemd says of the unit of the upgrade, asked now and not through the question that requests share. It takes no lock: a caller asks it before it
// takes the lock, and decides under the lock from the log and from this answer.
func (s *systemService) osUpgradeUnitNow() osrun.Unit {
	return s.systemPackageUpdater().askOSUpgradeUnit()
}

// osUpgradeUnitIfRun is osUpgradeUnitNow for a caller that has a run to look at when there is one, and that asks systemd of nothing when there is none: a machine that has no
// upgrade is not asked about its unit.
func (s *systemService) osUpgradeUnitIfRun() osrun.Unit {
	if _, current := currentOSUpgrade(); current == osUpgradeNoRun {
		return osrun.Unit{Answered: true}
	}
	return s.osUpgradeUnitNow()
}

// askOSUpgradeUnit asks systemd about the unit of the upgrade, and keeps since when it has not answered: a run that has waited for days is not one whose systemd has been silent
// for days, and the reducer is told which of the two it is.
func (u *systemPackageUpdater) askOSUpgradeUnit() osrun.Unit {
	active, answered := u.unitState(systemOSUpgradeUnit)
	unit := osrun.Unit{Active: active, Answered: answered}
	if answered {
		u.unitSilentSince.Store(0)
		return unit
	}
	now := u.now()
	u.unitSilentSince.CompareAndSwap(0, now.UnixNano())
	unit.SilentSince = time.Unix(0, u.unitSilentSince.Load()).UTC()
	return unit
}

// unitProperty is one property of a unit as systemd says it, and whether it said one.
func (u *systemPackageUpdater) unitProperty(unit, property string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), systemDockerUnitTimeout)
	defer cancel()
	out, err := u.command(ctx, "systemctl", "show", unit, "--property="+property, "--value")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// unitExists says whether systemd has a unit by this name, loaded or running, and whether it answered: a unit that systemd-run queued before it reported an error is there, and
// will say so itself.
func (u *systemPackageUpdater) unitExists(unit string) (exists, answered bool) {
	active, answered := u.unitState(unit)
	if !answered {
		return false, false
	}
	if active {
		return true, true
	}
	load, ok := u.unitProperty(unit, "LoadState")
	if !ok {
		return false, false
	}
	return load == "loaded", true
}

// An osUpgradeLaunch is an attempt that was written to the log and whose unit has not been started: what launchRun needs.
type osUpgradeLaunch struct {
	run         *osUpgradeRun
	attempt     osrun.Marker
	args        []string
	systemdPath string
}

// launchAttempt starts the unit of the next attempt of an open run, and writes the log of it first. systemd is asked before the lock is taken; under the lock the run is read again, and
//
//  1. a unit of the upgrade that runs, or is on its way, or a systemd that does not answer, refuses the launch;
//  2. the number of the attempt is worked out (one more than the log counts: a number is launched at most once) and a phase that the log would not take is refused;
//  3. SHA256SUMS is checked against the files, and a run that is damaged is refused;
//  4. the list of blocks of the attempt is written, from the files of the backup folder, and ATTEMPT is appended to the log.
//
// The lock is let go of, and then systemd-run is called (launchRun). A log that says the attempt is on its way is what keeps a second launch away meanwhile.
// who is osrun.WhoAuto or osrun.WhoOwner. An *osUpgradeLaunchError is returned for what did not start a unit.
func (s *systemService) launchAttempt(runID, phase, who string) error {
	unit := s.osUpgradeUnitNow()
	var launch *osUpgradeLaunch
	err := withOSUpgradeLock(func() (err error) {
		launch, err = s.launchPrepareLocked(runID, phase, who, unit)
		return err
	})
	if err != nil {
		return err
	}
	return s.launchRun(launch)
}

// launchPrepareLocked is steps 1 to 4 of launchAttempt, for a caller that holds osUpgradeMu and has asked systemd (unit).
func (s *systemService) launchPrepareLocked(runID, phase, who string, unit osrun.Unit) (*osUpgradeLaunch, error) {
	if !osrun.ValidPhase(phase) || (who != osrun.WhoAuto && who != osrun.WhoOwner) {
		return nil, launchFailure(launchNotAllowed, "not a phase or not a caller of one", nil)
	}
	run, err := readOSUpgradeRun(runID)
	if err != nil {
		return nil, launchFailure(launchIO, "the run cannot be read", err)
	}
	if !osrun.ValidNonce(run.Nonce) || len(run.Markers) < 2 {
		return nil, launchFailure(launchIO, "the log of the run has no nonce or no RUN line", nil)
	}
	u := s.systemPackageUpdater()
	support := u.support()
	if !support.supported {
		return nil, launchFailure(launchUnsupported, support.reason, nil)
	}

	// 1. nothing is started while a unit of the upgrade may be running: systemd was asked, and the reducer says whether an attempt is on its way
	in := s.osUpgradeInputWith(run, unit)
	if !unit.Answered || unit.Active {
		return nil, launchFailure(launchUnitBusy, "a unit of the upgrade runs, or systemd does not answer", nil)
	}
	state := osrun.Reduce(in)
	switch {
	case live(state.Name):
		return nil, launchFailure(launchUnitBusy, "an attempt of the run is on its way", nil)
	case state.Closed || state.Name == osrun.StateManual || state.Name == osrun.StateManualDone:
		return nil, launchFailure(launchNotAllowed, "the owner closed the run or took it over", nil)
	}

	// 2. the number, and whether the log would take this attempt: read as it is read, with the files that say where the run stands
	n := osrun.NextAttempt(in)
	from := ""
	if phase == osrun.PhaseRecover {
		from = osrun.FirstMissing(run.Files)
	}
	attempt := osrun.Marker{Kind: osrun.MkAttempt, N: n, Phase: phase, From: from, Who: who, At: u.now().UTC()}
	check := in
	check.Markers = append(slices.Clone(run.Markers), attempt)
	if n > 999 || osrun.NextAttempt(check) != n+1 {
		return nil, launchFailure(launchNotAllowed, "the log of the run takes no attempt of this phase now", nil)
	}

	// 3. the files that the unit would run
	if err := verifyOSUpgradeRun(run.Dir, run.Meta.Kind); err != nil {
		logger.Error("the files of an upgrade are not the ones the core wrote: nothing is started from them", zap.String("run", runID), zap.Error(err))
		if appendErr := s.appendAttemptThatFailed(run, attempt); appendErr != nil {
			logger.Error("the log of an upgrade could not say that its files are damaged", zap.String("run", runID), zap.Error(appendErr))
		}
		return nil, launchFailure(launchDamaged, "the files of the run are not the ones that were written", err)
	}

	// 4. the blocks of the attempt, from the files, and the line that says the number is taken
	list := osrun.BlockList(phase, run.Files)
	if len(list) == 0 && phase != osrun.PhaseUnwind {
		return nil, launchFailure(launchNothing, "there is no block left to run", nil)
	}
	var listText strings.Builder
	for _, id := range list {
		if !osrun.ValidBlock(id) || !slices.Contains(osUpgradeBlockIDs(run.Meta.Kind), id) {
			return nil, launchFailure(launchNotAllowed, "a block that this run does not have", nil)
		}
		listText.WriteString(id + "\n")
	}
	if err := osUpgradeWriteFile(run.Dir+"/"+osUpgradeListsDir+"/"+strconv.Itoa(n)+".list", []byte(listText.String()), 0o600); err != nil {
		return nil, launchFailure(launchIO, "the list of blocks cannot be written", err)
	}
	args, err := osUpgradeUnitArgs(osrun.Attempt{RunID: run.ID, Nonce: run.Nonce, Phase: phase, N: n}, osUpgradeUnitRoot+"/"+run.ID)
	if err != nil {
		return nil, launchFailure(launchNotAllowed, "the command line of the unit is not one that is allowed", err)
	}
	if err := appendOSUpgradeMarker(run.Dir, run.Nonce, attempt); err != nil {
		return nil, launchFailure(launchIO, "the log cannot be written", err)
	}
	return &osUpgradeLaunch{run: run, attempt: attempt, args: args, systemdPath: support.systemdPath}, nil
}

// launchRun calls systemd-run for an attempt that is in the log, with no lock held, and says what came of it. When it reports an error, systemd is asked whether it has the unit all the
// same: a bus that was too slow can say it failed after the job was queued, and the unit then begins and says so itself, and the log is left as it is. START_FAILED is written only when
// systemd answered and has no such unit; when it did not answer nothing is written, and the attempt is waited for as long as the reducer waits for a launch that no unit has answered.
func (s *systemService) launchRun(launch *osUpgradeLaunch) error {
	u := s.systemPackageUpdater()
	output, err := u.start(launch.systemdPath, systemOSUpgradeUnit, launch.args...)
	if err == nil {
		return nil
	}
	err = errors.Join(err, errors.New(osplan.SanitizeLine(trimSystemPackageOutput(output))))
	runID := launch.run.ID
	exists, answered := u.unitExists(systemOSUpgradeUnit)
	switch {
	case exists:
		logger.Warn("systemd-run reported an error and the unit of an upgrade of the operating system is there: its own words will say how it goes", zap.String("run", runID), zap.Error(err))
		return nil
	case !answered:
		logger.Warn("systemd-run reported an error and systemd does not say whether the unit of an upgrade of the operating system is there: the launch is waited for", zap.String("run", runID), zap.Error(err))
		return nil
	}
	failed := osrun.Marker{Kind: osrun.MkStartFailed, N: launch.attempt.N, At: u.now().UTC()}
	if appendErr := withOSUpgradeLock(func() error { return appendOSUpgradeMarker(launch.run.Dir, launch.run.Nonce, failed) }); appendErr != nil {
		logger.Error("the log of an upgrade could not say that its unit did not start", zap.String("run", runID), zap.Error(appendErr))
	}
	return launchFailure(launchStartFailed, "systemd-run did not start the unit", err)
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
