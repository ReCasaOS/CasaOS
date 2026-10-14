package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// ---- AppManagement is asked again once the flag is up ---------------------------------------------------------------------

// An app that begins an operation between the question of the checks and the flag of the open upgrade is not seen by what comes after: the start asks again once the flag is written.
func TestAnAppThatBeginsAnOperationAfterTheChecksIsFoundOnceTheFlagIsUp(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	asked, flagUp := 0, false
	box.setApps(func(context.Context) ([]string, error) {
		asked++
		if asked == 1 {
			return nil, nil
		}
		flagUp, _ = osUpgradeFlagOpen()
		return []string{"photos"}, nil
	})
	_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	refusal := refusalOf(t, err)
	if refusal.Code != OSUpgradeAppsBusy || refusal.Data["apps"].([]string)[0] != "photos" {
		t.Fatalf("refused with %q %v", refusal.Code, refusal.Data)
	}
	if asked != 2 || !flagUp {
		t.Errorf("AppManagement was asked %d times, and the flag was up at the second question: %v", asked, flagUp)
	}
	box.assertNothingWasBegun()
}

// What AppManagement does not say is not a machine with no operation.
func TestAnAppManagementThatDoesNotAnswerTheSecondQuestionRefusesTheStart(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	asked := 0
	box.setApps(func(context.Context) ([]string, error) {
		if asked++; asked == 1 {
			return nil, nil
		}
		return nil, errors.New("AppManagement does not answer")
	})
	_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeAppsBusy || refusal.Data["unknown"] != true {
		t.Fatalf("refused with %q %v", refusal.Code, refusal.Data)
	}
	box.assertNothingWasBegun()
}

// assertNothingWasBegun says that a start that was refused left nothing: no run folder, no flag, no pointer, no unit.
func (b *upgradeBox) assertNothingWasBegun() {
	b.t.Helper()
	if folders := b.runFolders(); len(folders) != 0 {
		b.t.Errorf("the run folders are %v", folders)
	}
	if b.flagIsThere() {
		b.t.Error("the flag of the open upgrade is still up")
	}
	if id, current := currentOSUpgrade(); current != osUpgradeNoRun {
		b.t.Errorf("the pointer names %q", id)
	}
	if units := b.startedUnits(); len(units) != 0 {
		b.t.Errorf("%d units were started", len(units))
	}
}

// ---- a start that breaks closes nothing ------------------------------------------------------------------------------------

// aRunThatFailedBeforeItChangedAnything is a hop that was undone after a stop, and that the owner has not closed: its failure is what the page shows.
func (b *upgradeBox) aRunThatFailedBeforeItChangedAnything() *osUpgradeRun {
	b.t.Helper()
	run := b.aRun(b.anUpgradePlan("ok", nil), osrun.RunHop)
	run = b.attempt(run, 1, osrun.PhaseRun, osrun.WhoOwner)
	run = b.block(run, 1, 4, osrun.BlockSources, false)
	run = b.stop(run, 1, 4, osrun.BlockSources, osrun.ClassBusy, false)
	run = b.say(run, osrun.Marker{Kind: osrun.MkUnwound, N: 1, Result: "ok"})
	run = b.end(run, 1, "stopped")
	b.setUnit("inactive")
	b.advance(time.Minute)
	b.point(run)
	return b.read(run.ID)
}

func TestAStartThatCannotWriteItsFolderLeavesTheRunBeforeAsItWas(t *testing.T) {
	box := newUpgradeBox(t)
	previous := box.aRunThatFailedBeforeItChangedAnything()
	_, plan := box.aMachineThatCanStart()
	was := osUpgradeWriteFile
	osUpgradeWriteFile = func(path string, data []byte, mode os.FileMode) error {
		if filepath.Base(path) == osUpgradePlanJSON {
			return errors.New("the disk is full")
		}
		return was(path, data, mode)
	}
	t.Cleanup(func() { osUpgradeWriteFile = was })

	_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeStartFailed {
		t.Fatalf("refused with %q", refusal.Code)
	}
	osUpgradeWriteFile = was
	if id, current := currentOSUpgrade(); current != osUpgradeHasRun || id != previous.ID {
		t.Errorf("the pointer names %q", id)
	}
	if got := markersOfKind(box.read(previous.ID), osrun.MkAck); len(got) != 0 {
		t.Errorf("the run before was closed: %+v", got)
	}
	if status := box.GetSystemOSUpgradeStatus(); status.State != osrun.StateFailed || status.Failure == nil || status.Failure.Class != osrun.ClassBusy || status.RunID != previous.ID {
		t.Errorf("the owner's failure report is gone: %+v", status)
	}
	if folders := box.runFolders(); len(folders) != 1 || folders[0] != previous.ID {
		t.Errorf("the run folders are %v", folders)
	}
	if box.flagIsThere() || len(box.startedUnits()) != 0 {
		t.Error("a flag or a unit is left")
	}
}

// A start that is refused after the folder was written puts the pointer back on the run before.
func TestAStartThatIsRefusedAfterItsFolderPutsThePointerBackOnTheRunBefore(t *testing.T) {
	box := newUpgradeBox(t)
	previous := box.aRunThatFailedBeforeItChangedAnything()
	_, plan := box.aMachineThatCanStart()
	asked := 0
	box.setApps(func(context.Context) ([]string, error) {
		if asked++; asked == 1 {
			return nil, nil
		}
		return []string{"photos"}, nil
	})
	_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeAppsBusy {
		t.Fatalf("refused with %q", refusal.Code)
	}
	if id, _ := currentOSUpgrade(); id != previous.ID {
		t.Errorf("the pointer names %q", id)
	}
	if got := markersOfKind(box.read(previous.ID), osrun.MkAck); len(got) != 0 || box.flagIsThere() {
		t.Errorf("the run before was closed (%+v) or a flag is up", got)
	}
	if folders := box.runFolders(); len(folders) != 1 {
		t.Errorf("the run folders are %v", folders)
	}
}

// A start that goes through closes the run before, in its own log, once the new one is begun.
func TestAStartThatGoesThroughClosesTheRunBeforeAfterTheNewOneIsBegun(t *testing.T) {
	box := newUpgradeBox(t)
	previous := box.aRunThatFailedBeforeItChangedAnything()
	_, plan := box.aMachineThatCanStart()
	status, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if err != nil {
		t.Fatal(err)
	}
	if status.RunID == previous.ID {
		t.Fatal("the run before is the new one")
	}
	if got := markersOfKind(box.read(previous.ID), osrun.MkAck); len(got) != 1 || got[0].Result != "acknowledge" {
		t.Errorf("the run before was not closed in its log: %+v", got)
	}
	if id, _ := currentOSUpgrade(); id != status.RunID || !box.flagIsThere() {
		t.Errorf("the pointer names %q", id)
	}
}

// ---- a plan that is older than a refresh ----------------------------------------------------------------------------------

func TestAPlanMadeBeforeARefreshIsRefusedBeforeTheLoopHasLookedAtTheRefresh(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	box.advance(time.Hour)
	refresh := box.aRefresh()
	box.point(refresh)
	if _, err := os.Stat(filepath.Join(box.root, osUpgradeLastRefreshFile)); err == nil {
		t.Fatal("the end of the refresh is written down already")
	}
	_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradePlanOlderThanRefresh {
		t.Fatalf("refused with %q", refusal.Code)
	}
	if text := readText(t, filepath.Join(box.root, osUpgradeLastRefreshFile)); text == "" {
		t.Error("the end of the refresh was not written down by the start")
	}
	box.assertNoNewRun(refresh.ID)
}

// A plan that is made after the refresh is not refused, and a start that closes the refresh writes its end down first.
func TestAStartThatClosesARefreshWritesItsEndDown(t *testing.T) {
	box := newUpgradeBox(t)
	refresh := box.aRefresh()
	box.point(refresh)
	box.advance(time.Hour)
	_, plan := box.aMachineThatCanStart()
	if _, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan)); err != nil {
		t.Fatal(err)
	}
	if text := readText(t, filepath.Join(box.root, osUpgradeLastRefreshFile)); text == "" {
		t.Error("the end of the refresh was not written down")
	}
}

func (b *upgradeBox) assertNoNewRun(known ...string) {
	b.t.Helper()
	for _, folder := range b.runFolders() {
		found := false
		for _, id := range known {
			found = found || id == folder
		}
		if !found {
			b.t.Errorf("a run folder %s was made", folder)
		}
	}
}

// ---- the backup of a run that ended, and the file systems -----------------------------------------------------------------

// When /var/backups has a file system of its own, the rename of the backup folder into the run folder cannot be done: the folder is copied, the first is removed, and the run is
// closed after that.
func TestARunIsClosedWhenItsBackupHasToMoveToAnotherFileSystem(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatSucceeded()
	bk := osUpgradeBackupFolder(run.Meta.BK)
	if err := os.WriteFile(filepath.Join(bk, "packages.tsv"), []byte("a\tb\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bk, "apt-orig", "sources.list.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bk, "apt-orig", "sources.list.d", "x.list"), []byte("deb x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	was := osUpgradeRename
	osUpgradeRename = func(from, to string) error {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { osUpgradeRename = was })

	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckAcknowledge}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(bk); !os.IsNotExist(err) {
		t.Errorf("the backup folder is still where the blocks of the next run would stop at it: %v", err)
	}
	moved := filepath.Join(run.Dir, osUpgradeBKDone)
	if got := readText(t, filepath.Join(moved, "packages.tsv")); got != "a\tb\n" {
		t.Errorf("packages.tsv is %q", got)
	}
	if got := readText(t, filepath.Join(moved, "apt-orig", "sources.list.d", "x.list")); got != "deb x\n" {
		t.Errorf("a file of the copy of the sources is %q", got)
	}
	if info, err := os.Stat(filepath.Join(moved, "packages.tsv")); err != nil || info.Mode().Perm() != 0o640 {
		t.Errorf("the mode of a file was not kept: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(moved, "done-after")); err != nil {
		t.Errorf("done-after is not in the folder that the run reads: %v", err)
	}
	last := box.read(run.ID).Markers
	if last[len(last)-1].Kind != osrun.MkAck {
		t.Errorf("the run was not closed: %+v", last[len(last)-1])
	}
	if _, current := currentOSUpgrade(); current != osUpgradeNoRun || box.flagIsThere() {
		t.Error("the pointer or the flag is left")
	}
}

// A backup that cannot be put away leaves the run open and says so with a code of its own: nothing is written to the log before the folder is where the next run wants it.
func TestARunWhoseBackupCannotBePutAwayIsNotClosed(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatSucceeded()
	bk := osUpgradeBackupFolder(run.Meta.BK)
	// a fifo is not a file of a backup, and the copy refuses it
	if err := syscall.Mkfifo(filepath.Join(bk, "odd"), 0o600); err != nil {
		t.Skipf("no fifo here: %v", err)
	}
	was := osUpgradeRename
	osUpgradeRename = func(from, to string) error {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { osUpgradeRename = was })

	_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckAcknowledge})
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeCloseFailed {
		t.Fatalf("refused with %q", refusal.Code)
	}
	if _, err := os.Stat(filepath.Join(bk, "done-after")); err != nil {
		t.Errorf("the backup folder was touched: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(run.Dir, osUpgradeBKDone)); !os.IsNotExist(err) {
		t.Errorf("a half copy is left: %v", err)
	}
	if got := markersOfKind(box.read(run.ID), osrun.MkAck); len(got) != 0 {
		t.Errorf("the run was closed: %+v", got)
	}
	if id, current := currentOSUpgrade(); current != osUpgradeHasRun || id != run.ID {
		t.Errorf("the pointer names %q", id)
	}
	// and with the rename working again the same click closes it
	osUpgradeRename = was
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckAcknowledge}); err != nil {
		t.Fatal(err)
	}
}

// ---- the RUN line of the log is the run's -----------------------------------------------------------------------------------

func TestALogWhoseRunLineIsOfAnotherRunIsNotTrusted(t *testing.T) {
	for name, line := range map[string]osrun.Marker{
		"another run":  {Kind: osrun.MkRun, RunID: "7c1d9e44-2b6f-4a8e-8d35-1e0f6a9b3c27", Hop: "debian-11-12", RunKind: osrun.RunHop},
		"another kind": {Kind: osrun.MkRun, RunID: "0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19", Hop: "debian-11-12", RunKind: osrun.RunRehearsal},
		"another hop":  {Kind: osrun.MkRun, RunID: "0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19", Hop: "debian-12-13", RunKind: osrun.RunHop},
		"another plan": {Kind: osrun.MkRun, RunID: "0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19", Hop: "debian-11-12", RunKind: osrun.RunHop, PlanID: "p1-ffffffffffffffffffffffffffffffff"},
	} {
		t.Run(name, func(t *testing.T) {
			box := newUpgradeBox(t)
			plan := box.anUpgradePlan(osplan.VerdictOK, nil)
			run := box.aRun(plan, osrun.RunHop)
			if line.PlanID == "" {
				line.PlanID = plan.PlanID
			}
			queued, err := osrun.Marker{Kind: osrun.MkQueued, At: box.now()}.Line(run.Nonce)
			if err != nil {
				t.Fatal(err)
			}
			runLine, err := line.Line(run.Nonce)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(run.Dir, osUpgradeMarkersFile), []byte(queued+runLine), 0o600); err != nil {
				t.Fatal(err)
			}
			box.leave(run, "begun-upgrade")
			box.point(run)
			status := box.GetSystemOSUpgradeStatus()
			if status.State != osrun.StateNeedsAttention || status.Reason != osrun.ReasonRunDamaged || !status.Busy {
				t.Errorf("the status is %q %q busy %v", status.State, status.Reason, status.Busy)
			}
			if tick := box.tick(); !box.flagIsThere() {
				t.Errorf("the look took the flag away of a run that cannot be trusted: %+v", tick)
			}
		})
	}
	// the run's own line is trusted
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	if osUpgradeRunLineDiffers(run) {
		t.Error("the line of a run that was begun by the core differs from it")
	}
}
