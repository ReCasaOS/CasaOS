package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// ---- the order of a start ------------------------------------------------------------------------------------------------

// The pointer is what makes a run the latest, and it is written last: the log it names is whole, so a core that stops between the two writes leaves a folder that nothing names
// and a flag that the loop takes away, and never a held machine that the owner has no word for.
func TestThePointerToARunIsWrittenOnlyOnceItsLogIsWhole(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	was := osUpgradeWriteFile
	t.Cleanup(func() { osUpgradeWriteFile = was })
	var seen *osUpgradeRun
	var seenErr error
	osUpgradeWriteFile = func(path string, data []byte, mode os.FileMode) error {
		if filepath.Base(path) == osUpgradeCurrentFile {
			seen, seenErr = readOSUpgradeRun(string(data[:len(data)-1]))
		}
		return was(path, data, mode)
	}
	status, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if err != nil {
		t.Fatal(err)
	}
	if seenErr != nil || seen == nil || seen.ID != status.RunID || len(seen.Markers) < 2 {
		t.Fatalf("when the pointer was written the run read as %+v, %v", seen, seenErr)
	}
}

func TestAStartThatCannotWriteThePointerLeavesNothing(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	was := osUpgradeWriteFile
	t.Cleanup(func() { osUpgradeWriteFile = was })
	osUpgradeWriteFile = func(path string, data []byte, mode os.FileMode) error {
		if filepath.Base(path) == osUpgradeCurrentFile {
			return os.ErrPermission
		}
		return was(path, data, mode)
	}
	_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeStartFailed {
		t.Fatalf("refused with %q", refusal.Code)
	}
	box.assertNothingWasBegun()
}

// A core that stopped after the log and before the pointer leaves a folder and a flag that no pointer names: the loop takes the flag away, and the owner can start again.
func TestAFlagThatNoPointerNamesIsTakenAwayAndTheOwnerCanStartAgain(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	run := box.aRun(plan, osrun.RunHop)
	if err := setOSUpgradeBusy(run.ID, osrun.StateRunning, box.now()); err != nil {
		t.Fatal(err)
	}
	box.advance(time.Minute)
	box.tick()
	if box.flagIsThere() {
		t.Fatal("the flag of a run that nothing names is still up")
	}
	if status, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan)); err != nil || status.RunID == run.ID {
		t.Fatalf("the owner could not start again: %+v, %v", status, err)
	}
}

// ---- a start that waited ---------------------------------------------------------------------------------------------------

// A start that waits for AppManagement longer than an attempt is waited for reads as a start that did not come, and another start can go in and close it. When the first one goes
// on it takes back its own folder and leaves the pointer, the flag and the unit of the other.
func TestAStartThatWaitedLongAndFindsAnotherInItsPlaceLeavesTheOtherAlone(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	asked := 0
	var second OSUpgradeStatus
	var secondErr error
	box.setApps(func(context.Context) ([]string, error) {
		if asked++; asked == 2 {
			box.advance(40 * time.Second)
			box.tick()
			second, secondErr = box.StartSystemOSUpgrade(box.aGoodRequest(plan))
		}
		return nil, nil
	})
	_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if err == nil {
		t.Fatal("the start that was taken the place of went through")
	}
	if secondErr != nil {
		t.Fatalf("the second start was refused: %v", secondErr)
	}
	if id, _ := currentOSUpgrade(); id != second.RunID || !box.flagIsThere() {
		t.Errorf("the pointer names %q (the second run is %q), flag %v", id, second.RunID, box.flagIsThere())
	}
	if units := box.startedUnits(); len(units) != 1 {
		t.Errorf("%d units were started", len(units))
	}
	if folders := box.runFolders(); len(folders) != 1 || folders[0] != second.RunID {
		t.Errorf("the run folders are %v", folders)
	}
	if status := box.GetSystemOSUpgradeStatus(); status.RunID != second.RunID || !status.Busy {
		t.Errorf("the second run is not what the status shows: %+v", status)
	}
}

// While systemd-run has not answered for an attempt, the log does not yet say that the attempt is lost, and a second start is not admitted over it even when the clock says that an
// attempt that was not heard from is lost.
func TestAStartIsNotAdmittedWhileAUnitIsBeingStarted(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	run := box.updater.start
	var nested error
	box.updater.start = func(path, unit string, args ...string) ([]byte, error) {
		box.advance(40 * time.Second)
		_, nested = box.StartSystemOSUpgrade(box.aGoodRequest(plan))
		return run(path, unit, args...)
	}
	status, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if err != nil {
		t.Fatal(err)
	}
	if refusal := refusalOf(t, nested); refusal.Code != OSUpgradeBusy {
		t.Errorf("the second start was refused with %q", refusal.Code)
	}
	if id, _ := currentOSUpgrade(); id != status.RunID || len(box.startedUnits()) != 1 {
		t.Errorf("the pointer names %q, %d units", id, len(box.startedUnits()))
	}
	if osUpgradeLaunching.Load() != 0 {
		t.Errorf("%d launches are counted after the start", osUpgradeLaunching.Load())
	}
}

// ---- a plan that ran out while the facts were taken -----------------------------------------------------------------------

func TestAPlanThatRanOutWhileTheFactsWereTakenIsNotStarted(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	taken := osUpgradeTakeFacts
	t.Cleanup(func() { osUpgradeTakeFacts = taken })
	osUpgradeTakeFacts = func(ctx context.Context, p osreadiness.Probe) osverify.Facts {
		box.advance(25 * time.Hour)
		return taken(ctx, p)
	}
	_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeNoPlan || refusal.Data["reason"] != "expired" {
		t.Fatalf("refused with %q %v", refusal.Code, refusal.Data)
	}
	box.assertNothingWasBegun()
}
