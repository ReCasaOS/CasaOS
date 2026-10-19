package modulecmd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A remove is never kept from taking the module away by an update that could not be put back; and the way out that the log gives is a true one.

const backupVirt = "/var/lib/casaos/modules/tmp/backup-virt"

// A folder that has COMPLETE and cannot be read cannot be put back from: an update says which folder and what is true; a remove takes the module away by the lists that are on the box and
// leaves the folder as it is; an install of the module afterwards drops the folder, which is of a version that is gone.
func TestACompleteBackupWithoutItsManifestIsKeptAndARemoveTakesTheModuleAway(t *testing.T) {
	f := updatable(t)
	f.killed("units")
	if err := os.Remove(f.path(backupVirt + "/old.json")); err != nil {
		t.Fatal(err)
	}
	before := f.treeWithoutBookkeeping()
	code := f.update("--yes")
	out := f.out.String()
	if code != ExitFailed || !strings.Contains(out, "the backup in "+backupVirt+" cannot be read") || !strings.Contains(out, "casaos module remove virt takes the module away") ||
		!strings.Contains(out, "Deleting the folder alone keeps the files as they are on the box") {
		t.Fatalf("update: exit %d:\n%s", code, out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	// the remove goes by the lists of the box, and says in its last lines that the folder was not touched
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("remove: exit %d:\n%s", code, f.out)
	}
	lines := f.lines()
	if last := lines[len(lines)-1]; !strings.Contains(last, "the backup in "+backupVirt+" was not touched") {
		t.Errorf("the last line: %q", last)
	}
	for _, p := range []string{progPath, toolPath, unitPath, "/var/lib/casaos/modules/virt.files", "/etc/casaos/modules/virt.json"} {
		if f.exists(p) {
			t.Errorf("%s is left", p)
		}
	}
	if !f.exists(backupVirt + "/COMPLETE") {
		t.Error("the backup was touched")
	}
	// a remove of what is not there leaves the folder alone too
	if code := f.remove("--yes"); code != ExitRefused || !f.exists(backupVirt+"/COMPLETE") {
		t.Errorf("a second remove: %d", code)
	}
	// the module installed again does not have the old files put over it: the folder goes
	f.publish(testTag, testManifest(), v1Entries(t))
	if code := f.install("--yes"); code != ExitOK || f.exists(backupVirt) || !strings.Contains(f.out.String(), "was of a module that is not on the box any more") {
		t.Fatalf("install: %d\n%s", code, f.out)
	}
}

// An unrecoverable copy: nothing is changed, the lists stay the box's, the unit is started, the way out is said and is true, and a remove works.
func TestARollbackThatCannotPutBackAFileLeavesTheListsAloneStartsTheUnitAndAllowsARemove(t *testing.T) {
	f := updatable(t)
	f.gw.never = true
	listBefore := ""
	f.env.StepHook = func(_ context.Context, step, phase string) {
		if step == StepRoute && phase == "begin" {
			listBefore = f.read("/var/lib/casaos/modules/virt.files")
		}
		if step == "rollback_restore" && phase == "begin" {
			_ = os.Remove(f.path(backupVirt + "/copies/0")) // a copy is gone
		}
	}
	if code := f.update("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), " virt failed route route_timeout rollback_incomplete ") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	out := f.out.String()
	if !strings.Contains(out, "casaos module remove virt takes the module away") || !strings.Contains(out, "Deleting the folder alone keeps the files as they are on the box") || !strings.Contains(out, backupVirt) {
		t.Errorf("the way out is not said:\n%s", out)
	}
	if got := f.read("/var/lib/casaos/modules/virt.files"); got != listBefore || !strings.Contains(got, toolPath) {
		t.Errorf("the list of files was changed:\n%s", got)
	}
	if f.read("/var/lib/casaos/modules/virt.version") != newTag+"\n" {
		t.Errorf("the version was put back: %q", f.read("/var/lib/casaos/modules/virt.version"))
	}
	if !f.runner.isRunning(testUnit) || !f.exists(backupVirt+"/COMPLETE") || !slices.Contains(markers(t, f.lines()), "rollback_start done") {
		t.Errorf("the unit was not started, or the backup was not kept:\n%s", out)
	}
	// a run again says the same and changes nothing
	before := f.treeWithoutBookkeeping()
	f.env.StepHook = nil
	if code := f.update("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), "casaos module remove virt takes the module away") {
		t.Fatalf("again: %d\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	// and the clean way out works
	if code := f.remove("--yes"); code != ExitOK || f.exists(progPath) || f.exists(toolPath) || f.exists("/etc/casaos/modules/virt.json") {
		t.Fatalf("remove: %d\n%s", code, f.out)
	}
	if !f.exists(backupVirt + "/COMPLETE") {
		t.Error("the backup was touched")
	}
}

// A file that its administrator edited while the update ran is the administrator's: the rollback leaves it, says so, puts back everything else, and ends as a rollback, with the code of the
// failure; and a remove after it works.
func TestAFileTheAdministratorEditedWhileTheUpdateRanDoesNotKeepTheRollbackFromBeingMade(t *testing.T) {
	f := updatable(t)
	f.env.StepHook = func(_ context.Context, step, phase string) {
		if step == StepStop && phase == "done" {
			f.writeFile(samplePath, "edited while the update ran") // (after the backup copied it)
		}
	}
	before := f.treeWithoutBookkeeping()
	delete(before, samplePath)
	if code := f.update("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), " virt failed swap file_exists rolled_back_with_left ") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	out := f.out.String()
	if !strings.Contains(out, "left: "+samplePath+" (it is neither the old file nor the new one: it stays as it was left)\n") {
		t.Errorf("not said:\n%s", out)
	}
	after := f.treeWithoutBookkeeping()
	if f.read(samplePath) != "edited while the update ran" {
		t.Error("the file of the administrator was replaced")
	}
	delete(after, samplePath)
	sameTree(t, before, after)
	if f.exists(backupVirt) || !f.runner.isRunning(testUnit) {
		t.Error("the backup is left, or the unit is not running")
	}
	checkGrammar(t, f, ExitFailed, false, false, "")
	// the old lists are back: a remove leaves the file that is the administrator's
	if code := f.remove("--yes"); code != ExitOK || f.exists(progPath) || f.read(samplePath) != "edited while the update ran" {
		t.Fatalf("remove: %d\n%s", code, f.out)
	}
}

// A unit that systemd says is activating (or reloading) is on its way to running: it is stopped before the swap and started after it, as a running one is.
func TestAUnitThatIsActivatingIsStoppedAndStartedLikeARunningOne(t *testing.T) {
	for _, word := range []string{"activating", "reloading"} {
		f := updatable(t)
		f.runner.activeWord = map[string]string{testUnit: word}
		stops := f.count("systemctl", "stop")
		if code := f.update("--yes"); code != ExitOK {
			t.Fatalf("%s: exit %d\n%s", word, code, f.out)
		}
		if f.count("systemctl", "stop") != stops+1 || !f.runner.isRunning(testUnit) || slices.Contains(markers(t, f.lines()), "start skipped") {
			t.Errorf("%s: stops %d (was %d), running %v, markers %q", word, f.count("systemctl", "stop"), stops, f.runner.isRunning(testUnit), markers(t, f.lines()))
		}
	}
}

// A record of the units that is not there, is cut short or is of another format does not keep the files from being put back: the module's unit is started, and the run says so.
func TestAnUnreadableRecordOfTheUnitsDoesNotKeepTheFilesFromBeingPutBack(t *testing.T) {
	for name, damage := range map[string]func(f *fixture){
		"cut short":  func(f *fixture) { f.writeFile(backupVirt+"/units.state", "casaos-module-units 1\ncasaos-virt-man") },
		"old format": func(f *fixture) { f.writeFile(backupVirt+"/units.state", testUnit+"\n") },
		"missing":    func(f *fixture) { _ = os.Remove(f.path(backupVirt + "/units.state")) },
	} {
		t.Run(name, func(t *testing.T) {
			f := updatable(t)
			before := f.treeWithoutBookkeeping()
			f.killed("swap-2")
			damage(f)
			if code := f.install("--yes"); code != ExitRefused {
				t.Fatalf("install: %d\n%s", code, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			if !strings.Contains(f.out.String(), "the record of the units in the backup cannot be read: the units of the module are started") || !f.runner.isRunning(testUnit) || f.exists(backupVirt) {
				t.Errorf("running %v:\n%s", f.runner.isRunning(testUnit), f.out)
			}
		})
	}
}

// The folder is deleted in this order: COMPLETE, made durable, and then the rest.
func TestTheDropOfABackupTakesCompleteAwayAndMakesItDurableBeforeItDeletesAnything(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{backupComplete, "old.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var events []string
	oldSync, oldRemove := syncDir, removeAll
	t.Cleanup(func() { syncDir, removeAll = oldSync, oldRemove })
	syncDir = func(d string) {
		if d == dir {
			_, err := os.Lstat(filepath.Join(dir, backupComplete))
			events = append(events, "sync, without COMPLETE: "+strconv.FormatBool(err != nil))
		}
	}
	removeAll = func(d string) error {
		events = append(events, "remove the rest")
		return os.RemoveAll(d)
	}
	if err := removeBackup(dir); err != nil {
		t.Fatal(err)
	}
	if want := []string{"sync, without COMPLETE: true", "remove the rest"}; !slices.Equal(events, want) {
		t.Errorf("%q, want %q", events, want)
	}
}
