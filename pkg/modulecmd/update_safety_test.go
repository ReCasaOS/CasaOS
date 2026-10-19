package modulecmd

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The ways an update can be ended or disturbed that leave the box with two versions in it, and what keeps them from being left.

// partialRemoveAll is a removal of the backup folder that does its work in order and is ended after `after` files: by an error, or (kill) as a process is ended, without a word.
func partialRemoveAll(after int, kill bool) func(string) error {
	return func(dir string) error {
		var files []string
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files = append(files, p)
			}
			return nil
		})
		for i, p := range files {
			if i == after {
				if kill {
					runtime.Goexit()
				}
				return errors.New("the disk went away")
			}
			_ = os.Remove(p)
		}
		if after == len(files) {
			if kill {
				runtime.Goexit()
			}
			return errors.New("the disk went away")
		}
		return os.RemoveAll(dir)
	}
}

// ended runs the update the way a process that is killed ends: its deferred calls run, and nothing else is said.
func (f *fixture) endedAs(args ...string) {
	f.t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.update(append([]string{"--yes"}, args...)...)
	}()
	<-done
}

func TestTheDropOfABackupThatIsEndedAtAnyPointLeavesNothingThatPutsTheOldVersionOverTheNew(t *testing.T) {
	clean := updatable(t)
	if code := clean.update("--yes"); code != ExitOK {
		t.Fatal(clean.out.String())
	}
	wantUpdated := clean.treeWithoutBookkeeping()
	bare := newFixture(t)
	wantNothing := bare.treeWithoutBookkeeping()
	// how many files the backup has
	files := 0
	removeAll = func(dir string) error {
		_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files++
			}
			return nil
		})
		return os.RemoveAll(dir)
	}
	probe := updatable(t)
	probe.update("--yes")
	removeAll = os.RemoveAll
	if files < 8 {
		t.Fatalf("the backup has %d files", files)
	}
	t.Cleanup(func() { removeAll = os.RemoveAll })
	for after := 0; after <= files; after++ {
		for _, kill := range []bool{false, true} {
			for _, then := range []string{"update", "remove", "install"} {
				name := "update ended at " + strconv.Itoa(after) + " then " + then
				if kill {
					name = "update killed at " + strconv.Itoa(after) + " then " + then
				}
				t.Run(name, func(t *testing.T) {
					f := updatable(t)
					removeAll = partialRemoveAll(after, kill)
					if kill {
						f.endedAs()
					} else if code := f.update("--yes"); code != ExitOK {
						t.Fatalf("an update whose backup is not all deleted: %d\n%s", code, f.out)
					}
					removeAll = os.RemoveAll
					if f.exists("/var/lib/casaos/modules/tmp/backup-virt") && f.exists("/var/lib/casaos/modules/tmp/backup-virt/COMPLETE") {
						t.Fatal("a backup that is not all there says it is complete")
					}
					switch then {
					case "update":
						if code := f.update("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+osmodule.RefusedUpToDate+"\n") {
							t.Fatalf("update: %d\n%s", code, f.out)
						}
						sameTree(t, wantUpdated, f.treeWithoutBookkeeping())
					case "install":
						if code := f.install("--yes"); code != ExitRefused {
							t.Fatalf("install: %d\n%s", code, f.out)
						}
						sameTree(t, wantUpdated, f.treeWithoutBookkeeping())
					default:
						if code := f.remove("--yes"); code != ExitOK {
							t.Fatalf("remove: %d\n%s", code, f.out)
						}
						for p := range f.treeWithoutBookkeeping() {
							if _, ok := wantNothing[p]; !ok {
								t.Errorf("left after the remove: %s", p)
							}
						}
					}
					if then != "remove" && !f.runner.isRunning(testUnit) {
						t.Error("the unit is not running")
					}
					if f.exists("/var/lib/casaos/modules/tmp/backup-virt") {
						t.Error("the backup is left")
					}
				})
			}
		}
	}
}

// The same for the backup of an update that was rolled back: the old version stays, and the next update goes on from it.
func TestTheDropOfTheBackupOfARolledBackUpdateThatIsEndedLeavesTheOldVersion(t *testing.T) {
	clean := updatable(t)
	clean.update("--yes")
	wantUpdated := clean.treeWithoutBookkeeping()
	t.Cleanup(func() { removeAll = os.RemoveAll })
	for after := 0; after <= 13; after++ {
		t.Run("ended at "+strconv.Itoa(after), func(t *testing.T) {
			f := updatable(t)
			before := f.treeWithoutBookkeeping()
			f.gw.never = true
			removeAll = partialRemoveAll(after, true)
			f.endedAs()
			removeAll = os.RemoveAll
			if f.exists("/var/lib/casaos/modules/tmp/backup-virt/COMPLETE") {
				t.Fatal("a backup that is not all there says it is complete")
			}
			f.gw.never = false
			if code := f.install("--yes"); code != ExitRefused {
				t.Fatalf("install: %d\n%s", code, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			if code := f.update("--yes"); code != ExitOK {
				t.Fatalf("update: %d\n%s", code, f.out)
			}
			sameTree(t, wantUpdated, f.treeWithoutBookkeeping())
		})
	}
}

// A folder that says it is complete and is not (it was not left by this assistant) cannot be put back from: the run says which folder and what to do, changes nothing, and the way out works.
func TestACompleteBackupWithoutItsManifestIsKeptAndTheWayOutIsSaid(t *testing.T) {
	f := updatable(t)
	f.killed("units")
	if err := os.Remove(f.path("/var/lib/casaos/modules/tmp/backup-virt/old.json")); err != nil {
		t.Fatal(err)
	}
	before := f.treeWithoutBookkeeping()
	for _, run := range []func() int{func() int { return f.update("--yes") }, func() int { return f.remove("--yes") }} {
		code := run()
		out := f.out.String()
		if code != ExitFailed || !strings.Contains(out, "/var/lib/casaos/modules/tmp/backup-virt cannot be read") && !strings.Contains(out, "the backup in /var/lib/casaos/modules/tmp/backup-virt cannot be read") ||
			!strings.Contains(out, "delete the folder /var/lib/casaos/modules/tmp/backup-virt by hand") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		sameTree(t, before, f.treeWithoutBookkeeping())
	}
	// the new version is there (the update was over but for the end of its work), so the way out is to delete the folder
	if err := os.RemoveAll(f.path("/var/lib/casaos/modules/tmp/backup-virt")); err != nil {
		t.Fatal(err)
	}
	if code := f.update("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+osmodule.RefusedUpToDate) {
		t.Errorf("after the way out: %d\n%s", code, f.out)
	}
}

// When something cannot be put back the lists, the manifest and the version stay the ones of the box, the unit is started all the same, the backup is kept, and the way out is said.
func TestARollbackThatCannotPutBackAFileLeavesTheListsAloneStartsTheUnitAndSaysTheWayOut(t *testing.T) {
	f := updatable(t)
	f.gw.never = true
	listBefore := ""
	f.env.StepHook = func(_ context.Context, step, phase string) {
		if step == StepRoute && phase == "begin" {
			listBefore = f.read("/var/lib/casaos/modules/virt.files")
		}
		if step == "rollback_restore" && phase == "begin" {
			f.writeFile(progPath, "somebody else's program")
		}
	}
	if code := f.update("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), " virt failed route route_timeout rollback_incomplete ") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	out := f.out.String()
	if !strings.Contains(out, "to go back by hand put them back and delete the folder") || !strings.Contains(out, "/var/lib/casaos/modules/tmp/backup-virt") {
		t.Errorf("the way out is not said:\n%s", out)
	}
	// nothing of the old lists, manifest or version was written
	if got := f.read("/var/lib/casaos/modules/virt.files"); got != listBefore || !strings.Contains(got, toolPath) {
		t.Errorf("the list of files was changed:\n%s", got)
	}
	if f.read("/var/lib/casaos/modules/virt.version") != newTag+"\n" {
		t.Errorf("the version was put back: %q", f.read("/var/lib/casaos/modules/virt.version"))
	}
	if f.read(progPath) != "somebody else's program" || f.read(toolPath) != wantAfter[toolPath] {
		t.Error("a file was changed though the rollback could not be made")
	}
	// the unit was started all the same, and the folder is kept whole
	if !f.runner.isRunning(testUnit) || !f.exists("/var/lib/casaos/modules/tmp/backup-virt/COMPLETE") {
		t.Error("the unit is not running, or the backup was not kept")
	}
	if !slices.Contains(markers(t, f.lines()), "rollback_start done") {
		t.Errorf("the unit was not started:\n%s", out)
	}
	// a run again says the same and changes nothing
	before := f.treeWithoutBookkeeping()
	f.env.StepHook = nil
	if code := f.update("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), "to go back by hand") {
		t.Fatalf("again: %d\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	// and the way out works: the folder is deleted, and the box is the new version with a file that is somebody's
	if err := os.RemoveAll(f.path("/var/lib/casaos/modules/tmp/backup-virt")); err != nil {
		t.Fatal(err)
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("remove after the way out: %d\n%s", code, f.out)
	}
	if f.read(progPath) != "somebody else's program" {
		t.Error("the file of somebody else was deleted")
	}
}

// A copy of the backup that is not what was saved is found before anything is changed.
func TestACopyThatIsNotWhatWasSavedChangesNothing(t *testing.T) {
	f := updatable(t)
	f.killed("units") // the swap is over: the files are the new ones and so are the lists
	f.writeFile("/var/lib/casaos/modules/tmp/backup-virt/copies/0", "a copy that was changed")
	leftovers := []string{tempName(f.path(toolPath)), tempName(f.path("/var/lib/casaos/modules/virt.files")), tempName(f.path("/etc/casaos/modules/virt.json"))}
	for _, p := range leftovers {
		if err := os.WriteFile(p, []byte("half a write"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := f.treeWithoutBookkeeping()
	for _, p := range leftovers {
		delete(before, f.env.Roots.Slash(p))
	}
	if code := f.update("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), "cannot be put back: "+progPath+" (its copy in the backup is not what was saved)") ||
		!strings.Contains(f.out.String(), " virt failed recover_restore "+CodeRecoveryIncomplete+" rollback_incomplete ") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if f.read(progPath) != wantAfter[progPath] || f.read("/var/lib/casaos/modules/virt.version") != newTag+"\n" {
		t.Error("a part of the old version was put back")
	}
	if !f.runner.isRunning(testUnit) {
		t.Error("the unit is not running")
	}
	checkGrammar(t, f, ExitFailed, false, false, "")
}

// The temporary name of a write that was ended is taken away, whichever version the write was for: the paths that only the new version has included.
func TestARecoveryTakesAwayTheTemporaryNamesOfThePathsOfBothVersions(t *testing.T) {
	temps := func(f *fixture) []string {
		return []string{
			tempName(f.path(toolPath)),                             // only the new version has it
			tempName(f.path(progPath)),                             // both do
			tempName(f.path(helperPath)),                           // only the old one has it
			tempName(f.path("/var/lib/casaos/modules/virt.files")), // the list
			tempName(f.path("/etc/casaos/modules/virt.json")),      // the manifest
			tempName(f.path("/var/lib/casaos/modules/virt.dirs")),  // the list of directories (the old version has none)
		}
	}
	left := func(f *fixture, names []string) (out []string) {
		for _, n := range names {
			if exists(n) {
				out = append(out, n)
			}
		}
		return out
	}
	for _, then := range []string{"install", "update", "remove"} {
		t.Run(then, func(t *testing.T) {
			f := updatable(t)
			f.killed("swap-2")
			for _, p := range temps(f) {
				if err := os.WriteFile(p, []byte("half a write"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			switch then {
			case "install":
				f.install("--yes")
			case "update":
				// (the update writes its own files under these names; a path it does not write is where the leftover shows)
				f.env.StepHook = func(_ context.Context, step, phase string) {
					if step == StepPackages && phase == "begin" {
						if got := left(f, temps(f)); len(got) != 0 {
							t.Errorf("left when the update begins: %v", got)
						}
					}
				}
				f.update("--yes")
			default:
				if code := f.remove("--yes"); code != ExitOK {
					t.Fatalf("remove: %d\n%s", code, f.out)
				}
			}
			if got := left(f, temps(f)); len(got) != 0 {
				t.Errorf("temporary names are left: %v", got)
			}
		})
	}
}

// ---- the state of the units ---------------------------------------------------------------------------------------------------------------------------------------------------

func (f *fixture) count(prefix ...string) int { return f.runner.called(prefix...) }

// A module that its administrator had stopped is still stopped when the update is over: nothing is stopped, started or waited for, and the markers say the steps were not made.
func TestAnUpdateOfAModuleThatWasStoppedLeavesItStopped(t *testing.T) {
	f := updatable(t)
	f.envVars["CASAOS_MODULE_NONCE"] = strings.Repeat("ab12", 8)
	f.runner.stopped[testUnit] = true
	gateway := f.gw.calls
	if code := f.update("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	got := markers(t, f.lines())
	for _, want := range []string{"start skipped", "route skipped"} {
		if !slices.Contains(got, want) {
			t.Errorf("no %q in %q", want, got)
		}
	}
	for _, not := range []string{"start begin", "route begin"} {
		if slices.Contains(got, not) {
			t.Errorf("%q in %q", not, got)
		}
	}
	if f.count("systemctl", "stop") != 0 || f.count("systemctl", "start") != 0 || f.gw.calls != gateway || f.runner.isRunning(testUnit) {
		t.Errorf("the unit was touched: stops %d, starts %d, gateway %d, running %v", f.count("systemctl", "stop"), f.count("systemctl", "start"), f.gw.calls-gateway, f.runner.isRunning(testUnit))
	}
	if f.read(progPath) != wantAfter[progPath] {
		t.Error("the update was not made")
	}
	checkGrammar(t, f, ExitOK, false, false, "")
	// the job reads as succeeded, and its step as done
	job, err := ReadJob(f.env, testName, UnitGone, f.clock.Now())
	if err != nil || job.State != JobSucceeded {
		t.Errorf("%+v, %v", job, err)
	}
}

// Nor does a rollback start it.
func TestARollbackOfAModuleThatWasStoppedLeavesItStopped(t *testing.T) {
	f := updatable(t)
	f.runner.stopped[testUnit] = true
	failNewFile(f, 6)
	before := f.treeWithoutBookkeeping()
	if code := f.update("--yes"); code != ExitFailed {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if f.count("systemctl", "stop") != 0 || f.count("systemctl", "start") != 0 || f.runner.isRunning(testUnit) {
		t.Errorf("the unit was touched: %q", f.runner.calls)
	}
}

// An interruption between the backup and the stop changes nothing, so the rollback has nothing to stop and the unit that runs is not stopped and started.
func TestAnInterruptionBeforeTheStopDoesNotStopAndStartTheUnit(t *testing.T) {
	f := updatable(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.env.StepHook = func(_ context.Context, step, phase string) {
		if step == StepStop && phase == "begin" {
			cancel()
		}
	}
	before := f.treeWithoutBookkeeping()
	if code := f.main(ctx, "update", testName, "--yes"); code != ExitFailed || !slices.Contains(f.lines(), "failed at step stop: "+CodeInterrupted) {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if f.count("systemctl", "stop") != 0 || f.count("systemctl", "start") != 0 || !f.runner.isRunning(testUnit) {
		t.Errorf("the unit that runs was stopped or started: %q", f.runner.calls)
	}
}

// The other units of the old list are stopped and started as the units they were: the ones that ran are started, the ones that did not are not, and none is enabled by the update.
func TestTheOtherUnitsOfTheOldListAreStartedOnlyWhenTheyRan(t *testing.T) {
	const extra = "casaos-virt-extra.service"
	const extraPath = "/usr/lib/systemd/system/" + extra
	for _, ran := range []bool{true, false} {
		f := updatable(t)
		f.writeFile(extraPath, "[Service]\n")
		list, err := osmodule.ParseFilesList([]byte(f.read("/var/lib/casaos/modules/virt.files")))
		if err != nil {
			t.Fatal(err)
		}
		list.Entries = append([]osmodule.FileEntry{{Path: extraPath, Want: []string{sum([]byte("[Service]\n"))}}}, list.Entries...)
		raw, err := osmodule.FormatFilesList(list.Entries)
		if err != nil {
			t.Fatal(err)
		}
		f.writeFile("/var/lib/casaos/modules/virt.files", string(raw))
		if ran {
			f.runner.enabled[extra] = true
		}
		enables := f.count("systemctl", "enable")
		if code := f.update("--yes"); code != ExitOK {
			t.Fatalf("exit %d:\n%s", code, f.out)
		}
		started := slices.ContainsFunc(f.runner.calls, func(c []string) bool { return slices.Equal(c, []string{"systemctl", "start", extra}) })
		stopped := slices.ContainsFunc(f.runner.calls, func(c []string) bool { return slices.Equal(c, []string{"systemctl", "stop", extra}) })
		if started != ran || stopped != ran || f.count("systemctl", "enable") != enables {
			t.Errorf("ran %v: started %v, stopped %v, enables %d (was %d)", ran, started, stopped, f.count("systemctl", "enable"), enables)
		}
		if f.exists(extraPath) {
			t.Error("the unit file that the new version does not have is still there")
		}
	}
}

// A unit that was enabled and is not any more once the update is over is enabled again, and a rollback does the same; nothing else is enabled.
func TestAUnitThatWasEnabledAndIsNotAnyMoreIsEnabledAgain(t *testing.T) {
	for _, fail := range []bool{false, true} {
		f := updatable(t)
		f.gw.never = fail
		disabled := false
		f.env.StepHook = func(_ context.Context, step, phase string) {
			if step == StepSwap && phase == "done" && !disabled {
				disabled = true
				delete(f.runner.enabled, testUnit) // (a script of a package disabled it)
			}
		}
		enables := f.count("systemctl", "enable")
		code := f.update("--yes")
		if (code == ExitFailed) != fail || (!fail && code != ExitOK) {
			t.Fatalf("fail %v: exit %d\n%s", fail, code, f.out)
		}
		if !f.runner.isEnabled(testUnit) || !f.runner.isRunning(testUnit) || f.count("systemctl", "enable") != enables+1 {
			t.Errorf("fail %v: enabled %v, running %v, enables %d (was %d)", fail, f.runner.isEnabled(testUnit), f.runner.isRunning(testUnit), f.count("systemctl", "enable"), enables)
		}
	}
}

// ---- the swap, where it meets other hands --------------------------------------------------------------------------------------------------------------------------------------

// A file of the module that was changed after the archive was checked is not written over, whatever it was when the archive was checked.
func TestAFileOfTheModuleChangedAfterTheArchiveWasCheckedIsNotWrittenOver(t *testing.T) {
	f := updatable(t)
	f.env.StepHook = func(_ context.Context, step, phase string) {
		if step == StepStop && phase == "done" {
			f.writeFile(progPath, "changed while the update ran")
		}
	}
	if code := f.update("--yes"); code != ExitFailed || !slices.Contains(f.lines(), "failed at step swap: "+osmodule.FileExists) {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if f.read(progPath) != "changed while the update ran" {
		t.Errorf("the file was written over: %q", f.read(progPath))
	}
	if f.read("/var/lib/casaos/modules/virt.version") != testTag+"\n" {
		t.Error("the version moved")
	}
	if f.exists(toolPath) {
		t.Error("a file of the new version was put down")
	}
}

// A path that was free and is not when the swap gets to it is not written over either, and what the swap did before is put back.
func TestAFileThatAppearsAtAPathOfTheNewVersionIsNotWrittenOver(t *testing.T) {
	f := updatable(t)
	f.env.StepHook = func(_ context.Context, step, phase string) {
		if step == StepStop && phase == "done" {
			f.writeFile(toolPath, "somebody else's tool")
		}
	}
	// (the plan and the check of the archive are made before: the tool was not there then)
	if code := f.update("--yes"); code != ExitFailed {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if !slices.Contains(f.lines(), "failed at step swap: "+osmodule.FileExists) {
		t.Errorf("not refused as a file in the way:\n%s", f.out)
	}
	if f.read(toolPath) != "somebody else's tool" {
		t.Errorf("the file was written over: %q", f.read(toolPath))
	}
	if f.read(progPath) != "#!/bin/sh\nexit 0\n" {
		t.Errorf("what the swap had done was not put back: %q", f.read(progPath))
	}
}

// The files of the old version that the new one does not have are not deleted from under another module whose list has them too.
func TestAFileThatAnotherModulesListHasToIsNotDeleted(t *testing.T) {
	f := updatable(t)
	f.writeFile("/var/lib/casaos/modules/other.files", "casaos-module-files 1\nf "+sum([]byte("helper v1\n"))+" "+helperPath+"\n")
	if code := f.update("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if f.read(helperPath) != "helper v1\n" || !strings.Contains(f.out.String(), "kept, it is the list of other too: "+helperPath+"\n") {
		t.Errorf("the file of the other module:\n%s", f.out)
	}
}

// onRead calls do the first time it is read, and then reads from r.
type onRead struct {
	do   func()
	r    io.Reader
	done bool
}

func (o *onRead) Read(p []byte) (int, error) {
	if !o.done {
		o.done = true
		o.do()
	}
	return o.r.Read(p)
}

// What the administrator said yes to is the plan that was shown: a catalogue that changes while the question is asked is another plan, and the update is not made.
func TestAnUpdateWhosePlanChangesWhileTheQuestionIsAskedIsNotMade(t *testing.T) {
	f := updatable(t)
	f.isTTY = true
	f.env.Stdin = &onRead{r: strings.NewReader("y\n"), do: func() { f.publish("v0.1.2", testManifest(), v2Entries(t, testManifest())) }}
	f.out.Reset()
	before := f.treeWithoutBookkeeping()
	if code := Main(context.Background(), []string{"update", testName}, f.env); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+CodePlanChanged+"\n") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	delete(before, "/usr/share/casaos/modules/catalogue.json")
	after := f.treeWithoutBookkeeping()
	delete(after, "/usr/share/casaos/modules/catalogue.json")
	sameTree(t, before, after)
}

// A file that fails to be put back, after every path was looked at and could be, leaves the lists, the manifest and the version as they are: the new version's, which the files that were put
// back are not.
func TestARollbackThatFailsWhilePuttingFilesBackLeavesTheListsAlone(t *testing.T) {
	f := updatable(t)
	f.gw.never = true
	failNewFile(f, 9) // the staging writes four files, the swap four, and the first the rollback puts back is the ninth
	if code := f.update("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), " virt failed route route_timeout rollback_incomplete ") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	out := f.out.String()
	if !strings.Contains(out, "were left as they are on the box") || !strings.Contains(out, "not put back: "+progPath) {
		t.Errorf("not said:\n%s", out)
	}
	if f.read("/var/lib/casaos/modules/virt.version") != newTag+"\n" || !strings.Contains(f.read("/var/lib/casaos/modules/virt.files"), toolPath) {
		t.Error("the old lists, manifest or version were written")
	}
	if !f.exists("/var/lib/casaos/modules/tmp/backup-virt/COMPLETE") || !f.runner.isRunning(testUnit) {
		t.Error("the backup was not kept, or the unit is not running")
	}
}
