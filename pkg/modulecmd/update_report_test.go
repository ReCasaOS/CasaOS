package modulecmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// What a run says it did is what it did: a remove that went by lists that are not the new version's names what of the new version it left; an update whose backup folder still says it is
// complete does not end ok; a rollback that left files says so in the word of its result.

// A remove that goes on past a backup that could not be put back takes away the files and the directories that only the new version has, and names the ones it could not.
func TestARemovePastAnUnfinishedRecoveryLeavesNothingOfTheNewVersionUnnamed(t *testing.T) {
	bare := newFixture(t).treeWithoutBookkeeping()
	damaged := func(t *testing.T) *fixture {
		f := updatable(t)
		f.killed("swap-2") // the program and the tool are the new ones; the lists are the old
		if err := os.Remove(f.path(backupVirt + "/copies/0")); err != nil {
			t.Fatal(err)
		}
		return f
	}
	t.Run("all of it is taken away", func(t *testing.T) {
		f := damaged(t)
		if code := f.remove("--yes"); code != ExitOK {
			t.Fatalf("exit %d:\n%s", code, f.out)
		}
		for p := range f.treeWithoutBookkeeping() {
			if _, ok := bare[p]; !ok && !strings.HasPrefix(p, backupVirt) {
				t.Errorf("left, and not named: %s", p)
			}
		}
		if f.exists(toolPath) || f.exists(progPath) {
			t.Error("files of the new version are left")
		}
		checkGrammar(t, f, ExitOK, true, false, "")
	})
	t.Run("what was changed is named, and the result says so", func(t *testing.T) {
		f := damaged(t)
		f.writeFile(toolPath, "the administrator's tool")
		code := f.remove("--yes")
		out := f.out.String()
		if code != ExitFailed || !strings.Contains(out, "left: "+toolPath+" (of the new version)\n") || !strings.Contains(out, " virt failed leftovers remove_incomplete - ") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		for p := range f.treeWithoutBookkeeping() {
			if _, ok := bare[p]; !ok && !strings.HasPrefix(p, backupVirt) && !strings.Contains(out, "left: "+p+" ") {
				t.Errorf("left, and not named: %s", p)
			}
		}
		checkGrammar(t, f, ExitFailed, true, false, "")
		// what the drop of the backup by the next install takes away is nothing that was not named
		if code := f.install("--yes"); !strings.Contains(f.out.String(), "was of a module that is not on the box any more") || f.exists(backupVirt) {
			t.Errorf("the backup was not dropped by the install (%d):\n%s", code, f.out)
		}
	})
	t.Run("the directories the swap made", func(t *testing.T) {
		v1 := slices.DeleteFunc(slices.Clone(v1Entries(t)), func(e tent) bool { return strings.Contains(e.name, "/system/") })
		f := newFixture(t, withEntries(v1))
		if code := f.install("--yes"); code != ExitOK {
			t.Fatal(f.out.String())
		}
		f.publish(newTag, testManifest(), v2Entries(t, testManifest()))
		f.killed("swap-1")
		if !f.exists("/usr/lib/systemd/system") {
			t.Fatal("the swap made no directory")
		}
		if err := os.Remove(f.path(backupVirt + "/copies/0")); err != nil {
			t.Fatal(err)
		}
		if code := f.remove("--yes"); code != ExitOK || f.exists("/usr/lib") {
			t.Fatalf("exit %d, the directories are left: %v\n%s", code, f.exists("/usr/lib"), f.out)
		}
	})
	t.Run("a directory that is not empty is named", func(t *testing.T) {
		v1 := slices.DeleteFunc(slices.Clone(v1Entries(t)), func(e tent) bool { return strings.Contains(e.name, "/system/") })
		f := newFixture(t, withEntries(v1))
		f.install("--yes")
		f.publish(newTag, testManifest(), v2Entries(t, testManifest()))
		f.killed("swap-1")
		_ = os.Remove(f.path(backupVirt + "/copies/0"))
		f.writeFile("/usr/lib/systemd/system/somebody.service", "x")
		if code := f.remove("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), "left: /usr/lib/systemd/system (a directory of the new version, not empty)") {
			t.Fatalf("exit %d:\n%s", code, f.out)
		}
	})
}

// A remove that is not past an unfinished recovery has no such step.
func TestARemoveThatIsNotPastARecoveryHasNoStepOfLeftovers(t *testing.T) {
	f := installed(t)
	if code := f.remove("--yes"); code != ExitOK || slices.Contains(markers(t, f.lines()), "leftovers begin") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
}

// An update whose backup folder still says it is complete, when it is over, does not end ok.
func TestAnUpdateWhoseBackupCannotBeDroppedIsMarkedDroppedAndEndsOk(t *testing.T) {
	clean := updatable(t)
	clean.update("--yes")
	wantUpdated := clean.treeWithoutBookkeeping()
	f := updatable(t)
	oldRemove := removeFile
	t.Cleanup(func() { removeFile = oldRemove })
	removeFile = func(p string) error {
		if filepath.Base(p) == backupComplete {
			return errors.New("read-only file system")
		}
		return os.Remove(p)
	}
	if code := f.update("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	removeFile = oldRemove
	// the folder is marked: no run puts the old version back over the new
	for _, verb := range []string{"update", "install"} {
		var code int
		if verb == "update" {
			code = f.update("--yes")
		} else {
			code = f.install("--yes")
		}
		if code != ExitRefused {
			t.Fatalf("%s: %d\n%s", verb, code, f.out)
		}
		sameTree(t, wantUpdated, f.treeWithoutBookkeeping())
	}
}

func TestAnUpdateWhoseBackupCannotBeDroppedNorMarkedEndsFailedAndSaysWhatIsTrue(t *testing.T) {
	f := updatable(t)
	oldRemove, oldMark := removeFile, writeTombstone
	t.Cleanup(func() { removeFile, writeTombstone = oldRemove, oldMark })
	removeFile = func(p string) error {
		if filepath.Base(p) == backupComplete {
			return errors.New("read-only file system")
		}
		return os.Remove(p)
	}
	writeTombstone = func(string) error { return errors.New("read-only file system") }
	f.envVars["CASAOS_MODULE_NONCE"] = strings.Repeat("ab12", 8)
	code := f.update("--yes")
	out := f.out.String()
	if code != ExitFailed || !strings.Contains(out, " virt failed - "+CodeBackupNotDropped+" - ") ||
		!strings.Contains(out, "is installed, but the backup folder "+backupVirt+" could not be dropped, and the next casaos module install, update or remove will put the old version back over it unless the folder is deleted by hand") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if f.read(progPath) != wantAfter[progPath] || !f.exists(backupVirt+"/COMPLETE") {
		t.Error("the update is not installed, or the folder is not as it was")
	}
	job, err := ReadJob(f.env, testName, UnitGone, f.clock.Now().Add(time.Second))
	if err != nil || job.State != JobFailed || job.Code != CodeBackupNotDropped {
		t.Errorf("%+v, %v", job, err)
	}
	checkGrammar(t, f, ExitFailed, false, false, "")
}

// The word of a rollback that left files is a word of the grammar: the reducer, the job and the markers take it where they take rolled_back.
func TestTheResultOfARollbackThatLeftFilesIsAWordOfTheGrammar(t *testing.T) {
	f := updatable(t)
	f.envVars["CASAOS_MODULE_NONCE"] = strings.Repeat("ab12", 8)
	f.gw.never = true
	f.env.StepHook = func(_ context.Context, step, phase string) {
		if step == "rollback_restore" && phase == "begin" {
			f.writeFile(progPath, "the administrator's program")
		}
	}
	if code := f.update("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), "rolled back, but these files were left as they are, and the old version is not whole: "+progPath) {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	job, err := ReadJob(f.env, testName, UnitGone, f.clock.Now())
	if err != nil || job.State != JobFailed || job.Undo != UndoRolledBackWithLeft || job.Code != CodeRouteTimeout {
		t.Errorf("%+v, %v", job, err)
	}
	if _, ok := parseMarker("CASAOS_MODULE_RESULT "+strings.Repeat("ab12", 8)+" virt failed route route_timeout rolled_back_with_left 2026-10-18T12:00:00Z", "virt"); !ok {
		t.Error("the marker is refused")
	}
	_ = osmodule.CheckInstalled
}

// The mark that the folder is dropped holds when the rest of it cannot be deleted either: no run puts the old version back over the new from a folder that is marked.
func TestAFolderMarkedDroppedWhoseRestCannotBeDeletedIsNotPutBack(t *testing.T) {
	clean := updatable(t)
	clean.update("--yes")
	wantUpdated := clean.treeWithoutBookkeeping()
	f := updatable(t)
	oldRemove, oldAll := removeFile, removeAll
	t.Cleanup(func() { removeFile, removeAll = oldRemove, oldAll })
	removeFile = func(p string) error {
		if filepath.Base(p) == backupComplete {
			return errors.New("read-only file system")
		}
		return os.Remove(p)
	}
	removeAll = func(string) error { return errors.New("read-only file system") }
	if code := f.update("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	removeFile, removeAll = oldRemove, oldAll
	if !f.exists(backupVirt+"/COMPLETE") || !f.exists(backupVirt+"/"+backupDropped) {
		t.Fatal("the folder is not as the test needs it")
	}
	if code := f.update("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+osmodule.RefusedUpToDate+"\n") {
		t.Fatalf("update: %d\n%s", code, f.out)
	}
	sameTree(t, wantUpdated, f.treeWithoutBookkeeping())
}
