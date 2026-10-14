package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"go.uber.org/zap"
)

// Beginning a run: what a start and a refresh share once their checks are made. It is done in the order below, and the lock is held only where the log or the flag of the open
// upgrade is changed, never across a question to systemd, a call of systemd-run, or the question to AppManagement.
//
//  1. under the lock: the switch and the latest run are looked at again, the finished backup of the run before is put away (it stays where the run before reads it), the run
//     folder is written, the flag of the open upgrade is put up, the pointer to the run is written, and the first two lines of the log;
//  2. AppManagement is asked again which operations it has: one that began between the first question and the flag is not seen by anything that comes after, and is refused
//     here (apps_busy), with the flag taken away again;
//  3. under the lock: the attempt is written to the log, and the run before is closed in its own log;
//  4. systemd-run is called, with no lock held.
//
// A start that breaks before step 3 is over closes nothing: the run before is as the owner left it.

// osUpgradeAppsAgainLimit is how long AppManagement has to answer the second question: the run has a first attempt, or is lost as a start that did not come, in thirty seconds.
const osUpgradeAppsAgainLimit = 10 * time.Second

func (s *systemService) osUpgradeStartRun(ctx context.Context, probe osReadinessProbe, spec osUpgradeRunSpec, nonce, phase, busyState string) (OSUpgradeStatus, error) {
	runID := spec.Meta.RunID
	unit := s.osUpgradeUnitNow()
	var previous *osUpgradeRun
	err := withOSUpgradeLock(func() (err error) {
		previous, err = s.osUpgradeOpenRun(spec, nonce, busyState, unit)
		return err
	})
	if err != nil {
		return OSUpgradeStatus{}, err
	}

	if refusal := osUpgradeAppsRefusal(ctx, probe, osUpgradeAppsAgainLimit); refusal != nil {
		s.osUpgradeAbandon(runID, previous)
		return OSUpgradeStatus{}, refusal
	}

	unit = s.osUpgradeUnitNow()
	var launch *osUpgradeLaunch
	err = withOSUpgradeLock(func() (err error) {
		launch, err = s.launchPrepareLocked(runID, phase, osrun.WhoOwner, unit)
		if err == nil && previous != nil {
			if closeErr := s.osUpgradeCloseLog(previous, "acknowledge"); closeErr != nil {
				// the new run is open, and the pointer names it: the run before is left as it is, and the owner's way back to it is the log
				logger.Error("the run before could not be closed in its log", zap.String("run", previous.ID), zap.Error(closeErr))
			}
		}
		return err
	})
	if err != nil {
		s.osUpgradeAbandon(runID, previous)
		var launchErr *osUpgradeLaunchError
		if errors.As(err, &launchErr) && launchErr.Kind == launchUnitBusy {
			return OSUpgradeStatus{}, refuseUpgrade(OSUpgradeBusy, "a unit of the upgrade runs, or systemd does not answer", map[string]any{"reason": launchErr.Kind})
		}
		return OSUpgradeStatus{}, osUpgradeStartFailed("the unit could not be started", err)
	}
	if err := s.launchRun(launch); err != nil {
		// systemd answered that it has no unit: the log says that the unit did not start, and nothing was changed. The run stays as the story of it, and the machine is free.
		if clearErr := withOSUpgradeLock(clearOSUpgradeBusy); clearErr != nil {
			logger.Error("the flag of an upgrade that did not start could not be taken away", zap.String("run", runID), zap.Error(clearErr))
		}
		return OSUpgradeStatus{}, osUpgradeStartFailed("the unit could not be started", err)
	}
	if err := pruneOSUpgradeRuns(osUpgradeKeepRuns); err != nil {
		logger.Error("the old runs of the upgrade could not be removed", zap.Error(err))
	}
	s.nudgeOSUpgradeSettle()
	return s.GetSystemOSUpgradeStatus(), nil
}

// osUpgradeOpenRun is step 1. The caller holds osUpgradeMu and has asked systemd. It gives the run before, which the pointer named when it began.
func (s *systemService) osUpgradeOpenRun(spec osUpgradeRunSpec, nonce, busyState string, unit osrun.Unit) (*osUpgradeRun, error) {
	u := s.systemPackageUpdater()
	if !osUpgradeBetaOn() {
		return nil, refuseUpgrade(OSUpgradeBetaOff, "the upgrade of the operating system is not switched on", nil)
	}
	previous, _, refusal := s.osUpgradeLatestWith(unit)
	if refusal != nil {
		return nil, refusal
	}
	runID := spec.Meta.RunID
	if previous != nil {
		// a plan made before a refresh that the loop has not looked at is not to be started: the end of the refresh is written down before the run is closed
		s.osUpgradeNoteRefresh(previous)
		if err := s.osUpgradePutAwayBackup(previous); err != nil {
			return nil, osUpgradeStartFailed("the backup of the run before could not be put away", err)
		}
	}
	if err := writeOSUpgradeRun(spec); err != nil {
		return nil, osUpgradeStartFailed("the folder of the run could not be written", err)
	}
	fail := func(detail string, err error) error {
		s.osUpgradeAbandonLocked(runID, previous)
		return osUpgradeStartFailed(detail, err)
	}
	if err := setOSUpgradeBusy(runID, busyState, u.now()); err != nil {
		return nil, fail("the flag of the open upgrade could not be written", err)
	}
	if err := setOSUpgradeCurrent(runID); err != nil {
		return nil, fail("the pointer to the run could not be written", err)
	}
	dir := osUpgradeRunFolder(runID)
	boot, _ := osUpgradeBootID()
	if err := createOSUpgradeMarkers(dir, nonce, u.now().UTC()); err != nil {
		return nil, fail("the log of the run could not be made", err)
	}
	if err := appendOSUpgradeMarker(dir, nonce, osrun.Marker{Kind: osrun.MkRun, RunID: runID, Hop: spec.Meta.Hop, PlanID: spec.Meta.PlanID, RunKind: spec.Meta.Kind, Boot: boot}); err != nil {
		return nil, fail("the log of the run could not be written", err)
	}
	return previous, nil
}

// osUpgradeAbandon takes back a run that was begun and is not to go on: its folder, its flag and its pointer go, and the pointer names the run before again.
func (s *systemService) osUpgradeAbandon(runID string, previous *osUpgradeRun) {
	_ = withOSUpgradeLock(func() error {
		s.osUpgradeAbandonLocked(runID, previous)
		return nil
	})
}

func (s *systemService) osUpgradeAbandonLocked(runID string, previous *osUpgradeRun) {
	if err := os.RemoveAll(osUpgradeRunFolder(runID)); err != nil {
		logger.Error("the folder of a run that was abandoned could not be removed", zap.String("run", runID), zap.Error(err))
	}
	if err := clearOSUpgradeBusy(); err != nil {
		logger.Error("the flag of a run that was abandoned could not be taken away", zap.String("run", runID), zap.Error(err))
	}
	var err error
	if previous != nil {
		err = setOSUpgradeCurrent(previous.ID)
	} else {
		err = clearOSUpgradeCurrent()
	}
	if err != nil {
		logger.Error("the pointer of a run that was abandoned could not be put back", zap.String("run", runID), zap.Error(err))
	}
}

// closeOSUpgradeRun closes a run: the owner has no more to do with it. The backup of a hop that ended is put away in the run folder first, because the blocks of a later run stop at
// any folder that says that a hop ended, and only then is the ACK appended; then the pointer to the latest run and the flag of the open upgrade are taken away. A backup folder that
// is not finished stays where it is, for the owner who finishes it by hand. The caller holds osUpgradeMu.
func (s *systemService) closeOSUpgradeRun(run *osUpgradeRun, how string) error {
	if err := s.osUpgradeCloseLog(run, how); err != nil {
		return err
	}
	if err := clearOSUpgradeCurrent(); err != nil {
		return err
	}
	return clearOSUpgradeBusy()
}

// osUpgradeCloseLog is the part of closing that is the run's own: its finished backup is put away, and the ACK is appended after it, so that a run is never said to be closed
// whose backup still stands where the blocks of the next run would stop at it.
func (s *systemService) osUpgradeCloseLog(run *osUpgradeRun, how string) error {
	if err := s.osUpgradePutAwayBackup(run); err != nil {
		return err
	}
	if run.Nonce != "" {
		ack := osrun.Marker{Kind: osrun.MkAck, Result: how, At: s.systemPackageUpdater().now().UTC()}
		return appendOSUpgradeMarker(run.Dir, run.Nonce, ack)
	}
	return nil
}

// osUpgradePutAwayBackup moves the backup folder of a hop that ended into its run folder, where the run goes on reading it. Nothing is done for a backup that is not finished.
func (s *systemService) osUpgradePutAwayBackup(run *osUpgradeRun) error {
	if run.BK == "" || !run.Files.DoneAfter || filepath.Dir(run.BK) == run.Dir {
		return nil
	}
	return moveOSUpgradeFolder(run.BK, filepath.Join(run.Dir, osUpgradeBKDone))
}

// osUpgradeRename is how a folder is moved, a var for the test that makes it fail.
var osUpgradeRename = os.Rename

// moveOSUpgradeFolder moves a folder, as mv does: a rename, and when that cannot be done (the two folders are on two file systems, as they are when /var/backups has a mount of its
// own) a copy and then the removal of the first. A copy that did not end is taken away again, so that the folder is whole in one place at least.
func moveOSUpgradeFolder(from, to string) error {
	err := osUpgradeRename(from, to)
	if err == nil {
		return nil
	}
	if copyErr := copyOSUpgradeFolder(from, to); copyErr != nil {
		os.RemoveAll(to)
		return fmt.Errorf("the folder could not be renamed (%v) and could not be copied: %w", err, copyErr)
	}
	if rmErr := os.RemoveAll(from); rmErr != nil {
		return fmt.Errorf("the folder was copied and the first could not be removed: %w", rmErr)
	}
	return nil
}

// copyOSUpgradeFolder copies a folder of a backup: its folders, its regular files (with their mode) and its links. Anything else in it is not a backup, and stops the copy.
func copyOSUpgradeFolder(from, to string) error {
	if err := os.RemoveAll(to); err != nil {
		return err
	}
	return filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyOSUpgradeFile(path, target, info.Mode().Perm())
		}
		return fmt.Errorf("%s is not a file of a backup", rel)
	})
}

func copyOSUpgradeFile(from, to string, mode fs.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
