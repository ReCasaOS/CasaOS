package modulecmd

import (
	"archive/tar"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The command from end to end on a real tree, as the box runs it: an archive of the layout of a release (build/sysroot with a program, the unit, the sample of a configuration, the manifest and
// the licence), the commands of the box replaced by stubs, the file system and the lock Linux's. An install in a process of its own, with a umask that would spoil the modes, and a remove.

const e2eProgram = "virt-x"

func e2eEntries(t *testing.T) []tent {
	t.Helper()
	return []tent{
		{name: "module.json", body: manifestJSON(t, testManifest())},
		{name: "LICENSE", body: "licence\n"},
		{name: "build/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/usr/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/usr/bin/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/usr/bin/" + e2eProgram, mode: 0o4755, body: "#!/bin/sh\nexit 0\n"},
		{name: "build/sysroot/usr/bin/virt", typ: tar.TypeSymlink, link: e2eProgram},
		{name: "build/sysroot/etc/casaos/" + e2eProgram + ".conf.sample", mode: 0o666, body: "[libvirt]\nURI = test:///default\n"},
		{name: "build/sysroot/usr/lib/systemd/system/" + testUnit, mode: 0o664, body: "[Service]\nExecStart=/usr/bin/" + e2eProgram + "\n"},
	}
}

// setEntries puts the archive in the place the helper reads it from, and the catalogue that names it.
func (b *realBox) setEntries(ents []tent) {
	b.t.Helper()
	b.fixture.setEntries(ents)
	cat, err := os.ReadFile(b.path(pathCatalogue))
	if err != nil {
		b.t.Fatal(err)
	}
	dl := filepath.Join(b.root, "dl")
	if err := os.WriteFile(filepath.Join(dl, "catalogue.json"), cat, 0o644); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dl, "archive.tar.gz"), b.fixture.archive, 0o644); err != nil {
		b.t.Fatal(err)
	}
	if err := os.Remove(b.path(pathCatalogue)); err != nil {
		b.t.Fatal(err)
	}
}

func (b *realBox) helperEnvUmask(at, mode, umask string) []string {
	return append(b.helperEnv(at, mode), "MODULECMD_UMASK="+umask)
}

func e2eFiles() []string {
	return []string{"/usr/bin/" + e2eProgram, "/usr/bin/virt", "/etc/casaos/" + e2eProgram + ".conf.sample", "/usr/lib/systemd/system/" + testUnit}
}

func TestAnInstallOfARealArchiveOnARealTreeWritesTheExactModesAndARemoveTakesAwayExactlyThat(t *testing.T) {
	b := newRealBox(t)
	b.setEntries(e2eEntries(t))
	before, dirsBefore := b.state(), b.dirs()
	cmd := helperCmd(t, "install", b.helperEnvUmask("none", "none", "077")...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	for p, want := range map[string]os.FileMode{
		"/usr/bin/" + e2eProgram: 0o755, "/etc/casaos/" + e2eProgram + ".conf.sample": 0o644, "/usr/lib/systemd/system/" + testUnit: 0o644,
		"/usr/lib/systemd/system": os.ModeDir | 0o755, "/usr/lib/systemd": os.ModeDir | 0o755, "/usr/lib": os.ModeDir | 0o755,
	} {
		if info, err := os.Lstat(b.path(p)); err != nil || info.Mode() != want {
			t.Errorf("%s: %v %v, want %v", p, info, err, want)
		}
	}
	if target, err := os.Readlink(b.path("/usr/bin/virt")); err != nil || target != e2eProgram {
		t.Errorf("the link: %q %v", target, err)
	}
	// the list has the files the install put down, the unit last, and the one of the directories has those it made
	list, err := readFilesList(b.path("/var/lib/casaos/modules/virt.files"))
	if err != nil || !slices.Equal(list, e2eFiles()) {
		t.Errorf("the files: %v %v", list, err)
	}
	dirs, err := readDirsList(b.path("/var/lib/casaos/modules/virt.dirs"))
	if err != nil || !slices.Equal(sorted(dirs), sorted(madeDirs)) {
		t.Errorf("the directories: %v %v", dirs, err)
	}
	if !b.runnerEnabled() {
		t.Error("the unit is not enabled")
	}
	if entries, _ := os.ReadDir(b.env2tmp()); len(entries) != 0 {
		t.Errorf("the area of the download: %v", entries)
	}
	// remove deletes exactly the files of the list and the directories the install made
	if code, out := b.call("remove", testName, "--yes"); code != ExitOK {
		t.Fatalf("remove: exit %d\n%s", code, out)
	}
	sameTree(t, before, b.state())
	sameDirs(t, dirsBefore, b.dirs())
	if b.runnerEnabled() {
		t.Error("the unit is still enabled")
	}
	for _, p := range []string{"/var/lib/casaos/modules/virt.files", "/var/lib/casaos/modules/virt.dirs", "/var/lib/casaos/modules/virt.version", "/etc/casaos/modules/virt.json"} {
		if b.exists(p) {
			t.Errorf("%s is left", p)
		}
	}
}

// A process killed with the one signal nobody can catch right after the first file was put in place leaves the lists of everything it was going to put down, and what it had made: a remove
// afterwards takes all of it away.
func TestARemoveAfterAKillRightAfterTheFirstRenameCleansEverythingTheKillLeft(t *testing.T) {
	for _, at := range []string{"unpack-planned", "unpack-first", "unpack-mid"} {
		t.Run(at, func(t *testing.T) {
			b := newRealBox(t)
			b.setEntries(e2eEntries(t))
			before, dirsBefore := b.state(), b.dirs()
			cmd := helperCmd(t, "install", b.helperEnvUmask(at, "kill", "077")...)
			err := cmd.Run()
			if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); err == nil || !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
				t.Fatalf("not killed: %v", err)
			}
			// the lists name everything the install was going to put down, and the files that are there are among them
			list, err := readFilesList(b.path("/var/lib/casaos/modules/virt.files"))
			if err != nil || !slices.Equal(list, e2eFiles()) {
				t.Fatalf("the files: %v %v", list, err)
			}
			dirs, err := readDirsList(b.path("/var/lib/casaos/modules/virt.dirs"))
			if err != nil || !slices.Equal(sorted(dirs), sorted(madeDirs)) {
				t.Fatalf("the directories: %v %v", dirs, err)
			}
			there := 0
			for _, p := range e2eFiles() {
				if b.exists(p) {
					there++
				}
			}
			// (the links are not counted by the point where the process is killed, which is the n-th file: the link of the program is in place after the first)
			if want := map[string]int{"unpack-planned": 0, "unpack-first": 2, "unpack-mid": 3}[at]; there != want {
				t.Errorf("%d of the files are in place, want %d", there, want)
			}
			if code, out := b.call("remove", testName, "--yes"); code != ExitOK {
				t.Fatalf("remove: exit %d\n%s", code, out)
			}
			sameTree(t, before, b.state())
			sameDirs(t, dirsBefore, b.dirs())
			for _, p := range []string{"/var/lib/casaos/modules/virt.files", "/var/lib/casaos/modules/virt.dirs"} {
				if b.exists(p) {
					t.Errorf("%s is left", p)
				}
			}
			// no temporary name of the unpack is left either
			_ = filepath.WalkDir(b.root, func(p string, d os.DirEntry, err error) error {
				if err == nil && strings.HasPrefix(d.Name(), ".cos-") {
					t.Errorf("left: %s", p)
				}
				return nil
			})
		})
	}
}

var _ = osmodule.MaxEntries
