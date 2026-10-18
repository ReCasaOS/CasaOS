package modulecmd

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// What an earlier install of the module that did not finish left (its lists, and what they name) is the module's own: an install over it replaces the files it writes again, keeps the rest in
// its lists, and a remove, or the rollback of an install that fails, takes all of it away.

const (
	leftoverFile = "/usr/bin/casaos-virt-old"
	filesList    = "/var/lib/casaos/modules/virt.files"
	dirsList     = "/var/lib/casaos/modules/virt.dirs"
)

// leave puts the state of a killed install on the box: a file of an earlier version of the module that this archive does not have, the files of this one, the directories, and the lists.
func leave(f *fixture) {
	f.writeFile(leftoverFile, "an earlier version")
	f.writeFile("/usr/bin/casaos-virt-management", "an earlier version")
	f.writeFile("/usr/lib/systemd/system/another.service", "not the module's") // (the directory is not empty)
	f.writeFile(filesList, leftoverFile+"\n/usr/bin/casaos-virt-management\n")
	f.writeFile(dirsList, strings.Join(madeDirs, "\n")+"\n")
}

func TestTheFilesOfAnEarlierInstallThatDidNotFinishStayInTheListAndAreReplacedOrRemoved(t *testing.T) {
	f := newFixture(t)
	leave(f)
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	list, err := osmodule.ParseFiles([]byte(f.read(filesList)))
	if err != nil || !slices.Equal(sorted(list), sorted(append([]string{leftoverFile}, wantFiles...))) {
		t.Errorf("the list: %v %v", list, err)
	}
	if got := f.read("/usr/bin/casaos-virt-management"); got != "#!/bin/sh\nexit 0\n" {
		t.Errorf("the file of the earlier version was not replaced: %q", got)
	}
	// the directories it made stay in the list of the directories though this install made none
	dirs, err := osmodule.ParseDirs([]byte(f.read(dirsList)))
	if err != nil || !slices.Equal(sorted(dirs), sorted(madeDirs)) {
		t.Errorf("the directories: %v %v", dirs, err)
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("remove: %d\n%s", code, f.out)
	}
	if f.exists(leftoverFile) || f.exists("/usr/bin/casaos-virt-management") {
		t.Error("a file of the module is left")
	}
	// the directory that holds a file of somebody else stays, with the ones above it
	if !f.exists("/usr/lib/systemd/system/another.service") {
		t.Error("the file of somebody else was deleted")
	}
}

func TestAnInstallThatFailsOverTheStateOfAnEarlierOneTakesAwayWhatIsTheModulesAndNoMore(t *testing.T) {
	f := newFixture(t)
	leave(f)
	f.runner.fail["systemctl enable --now"] = 1
	if code := f.install("--yes"); code != ExitFailed {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	for _, p := range []string{leftoverFile, "/usr/bin/casaos-virt-management", "/etc/casaos/virt-management.conf.sample", filesList, dirsList, "/etc/casaos/modules/virt.json"} {
		if f.exists(p) {
			t.Errorf("%s is left", p)
		}
	}
	if !f.exists("/usr/lib/systemd/system/another.service") {
		t.Error("the file of somebody else was deleted")
	}
}

// a list that has nothing in it is a list that was cut short and not a module with no files: it is not taken, by the install over it nor by the remove
func TestAListThatHasNothingInItIsNotAModuleWithNoFiles(t *testing.T) {
	f := newFixture(t)
	f.writeFile(filesList, "")
	before := f.treeWithoutBookkeeping()
	if code := f.install("--yes"); code == ExitOK {
		t.Fatalf("installed over a list that is cut:\n%s", f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	f = installed(t)
	f.writeFile(filesList, "")
	before = f.treeWithoutBookkeeping()
	if code := f.remove("--yes"); code != ExitFailed {
		t.Fatalf("remove: exit %d\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if !f.exists("/usr/bin/casaos-virt-management") {
		t.Error("the files were deleted for a list that says nothing")
	}
}

// with nothing to make there is no list of directories, and a list of directories left by an earlier run is not kept when it names none
func TestThereIsNoListOfDirectoriesWhenTheInstallMadeNone(t *testing.T) {
	f := newFixture(t)
	f.writeFile("/usr/lib/systemd/system/another.service", "not the module's")
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	if f.exists(dirsList) {
		t.Errorf("a list of directories: %s", f.read(dirsList))
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("remove: %d\n%s", code, f.out)
	}
	if !f.exists("/usr/lib/systemd/system/another.service") || !f.exists("/usr/lib/systemd/system") {
		t.Error("a directory that was not made by the install was removed")
	}
}

// the leftover temporary names of the files the unpack writes are taken away by the remove, and nothing else of the directory is
func TestARemoveTakesAwayTheTemporaryNamesOfTheFilesOfTheModuleAndNoOtherFile(t *testing.T) {
	f := installed(t)
	temp := tempName(f.path("/usr/bin/casaos-virt-management"))
	if err := os.WriteFile(temp, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := tempName(f.path("/usr/bin/not-the-modules"))
	if err := os.WriteFile(other, []byte("somebody's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("remove: %d\n%s", code, f.out)
	}
	if _, err := os.Lstat(temp); err == nil {
		t.Error("the temporary file is left")
	}
	if _, err := os.Lstat(other); err != nil {
		t.Error("the temporary name of another file was deleted")
	}
}

// a directory that somebody made after the unpack looked is not the install's: it is not in the list of the directories, and the remove leaves it
func TestADirectoryThatAppearsBeforeItIsMadeIsNotInTheListOfDirectories(t *testing.T) {
	f := newFixture(t)
	f.host.wrapDest = func(d osmodule.Destination) osmodule.Destination {
		return &hookedDest{Destination: d, onDir: func(p string) {
			if p == "/usr/lib/systemd" {
				if err := os.MkdirAll(f.path(p), 0o755); err != nil {
					t.Error(err)
				}
			}
		}}
	}
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	dirs, err := osmodule.ParseDirs([]byte(f.read(dirsList)))
	if err != nil || !slices.Equal(sorted(dirs), sorted([]string{"/usr/lib/systemd/system", "/usr/lib"})) {
		t.Errorf("the directories: %v %v", dirs, err)
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("remove: %d\n%s", code, f.out)
	}
	if !f.exists("/usr/lib/systemd") || f.exists("/usr/lib/systemd/system") {
		t.Error("the directory of somebody else was removed, or the one of the install is left")
	}
	// every directory made by somebody else: the install made none, and there is no list
	f = newFixture(t)
	f.host.wrapDest = func(d osmodule.Destination) osmodule.Destination {
		return &hookedDest{Destination: d, onDir: func(p string) {
			if err := os.MkdirAll(f.path(p), 0o755); err != nil {
				t.Error(err)
			}
		}}
	}
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	if f.exists(dirsList) {
		t.Errorf("a list of directories: %s", f.read(dirsList))
	}
}

// the lists of an earlier install are on the disk, with what the new one adds to them, before the new one changes anything
func TestTheListsOfAnEarlierInstallAreInTheListsBeforeTheNewOneChangesAnything(t *testing.T) {
	f := newFixture(t)
	leave(f)
	looked := false
	f.host.wrapDest = func(d osmodule.Destination) osmodule.Destination {
		return &hookedDest{Destination: d, onFile: func() {
			if looked {
				return
			}
			looked = true
			files, err := osmodule.ParseFiles([]byte(f.read(filesList)))
			if err != nil || !slices.Equal(sorted(files), sorted(append([]string{leftoverFile}, wantFiles...))) {
				t.Errorf("the files, before the first is written: %v %v", files, err)
			}
			dirs, err := osmodule.ParseDirs([]byte(f.read(dirsList)))
			if err != nil || !slices.Equal(sorted(dirs), sorted(madeDirs)) {
				t.Errorf("the directories, before the first file is written: %v %v", dirs, err)
			}
		}}
	}
	if code := f.install("--yes"); code != ExitOK || !looked {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
}

// a module that has only the list of its directories is a module to remove: the run is not refused for want of a manifest or of a list of files
func TestAModuleThatHasOnlyTheListOfItsDirectoriesIsRemoved(t *testing.T) {
	f := installed(t)
	for _, p := range []string{filesList, "/var/lib/casaos/modules/virt.version", "/etc/casaos/modules/virt.json"} {
		if err := os.Remove(f.path(p)); err != nil {
			t.Fatal(err)
		}
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	if f.exists(dirsList) {
		t.Error("the list of the directories is left")
	}
}
