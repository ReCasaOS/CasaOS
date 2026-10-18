package modulecmd

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

func (f *fixture) remove(args ...string) int {
	f.t.Helper()
	return f.main(context.Background(), append([]string{"remove", testName}, args...)...)
}

func installed(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("install: exit %d\n%s", code, f.out)
	}
	return f
}

func TestARemoveUndoesTheInstallAndSaysWhatIsLeft(t *testing.T) {
	f := newFixture(t)
	before := f.treeWithoutBookkeeping()
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("install: %d", code)
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if f.runner.isEnabled(testUnit) {
		t.Error("the unit is still enabled")
	}
	want := []string{"stop begin", "stop done", "files begin", "files done", "manifest begin", "manifest done", "reload begin", "reload done", "done done"}
	if got := markers(t, f.lines()); !slices.Equal(got, want) {
		t.Errorf("markers:\n got %q\nwant %q", got, want)
	}
	out := f.out.String()
	for _, s := range []string{"/DATA/VM", "the libvirt network and pool", "the machines", "the packages libvirt-clients qemu-utils", "apt-get purge libvirt-clients qemu-utils", "nothing above was run"} {
		if !strings.Contains(out, s) {
			t.Errorf("the output does not say %q:\n%s", s, out)
		}
	}
	if !strings.Contains(out, resultMarker+" - virt ok done - ") {
		t.Errorf("no result:\n%s", out)
	}
	// nothing was purged, and no command that deletes data was run
	for _, c := range f.runner.calls {
		if c[0] == "apt-get" && f.runner.called("apt-get", "install") != 1 {
			t.Errorf("apt ran: %q", c)
		}
	}
	// a module can be installed again afterwards
	if code := f.install("--yes"); code != ExitOK {
		t.Errorf("install again: %d\n%s", code, f.out)
	}
}

func TestARemoveOfWhatIsNotThereIsRefused(t *testing.T) {
	f := newFixture(t)
	before := f.treeWithoutBookkeeping()
	if code := f.remove("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+CodeNotInstalled+"\n") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if f.runner.called("systemctl", "disable") != 0 {
		t.Error("a unit was touched")
	}
}

func TestARemoveIsRefusedWithoutRootWithoutConfirmationAndWhileThePackagesAreChanging(t *testing.T) {
	for name, c := range map[string]struct {
		setup func(f *fixture)
		args  []string
		code  string
	}{
		"not root":        {func(f *fixture) { f.env.EUID = func() int { return 1 } }, []string{"--yes"}, CodeNotRoot},
		"no confirmation": {func(f *fixture) {}, nil, CodeConfirmationNeeded},
		"declined":        {func(f *fixture) { f.isTTY, f.input = true, "no\n" }, nil, CodeDeclined},
		"a package manager": {func(f *fixture) {
			f.runner.busyUnit = "casaos-update.service"
		}, []string{"--yes"}, CodeBusy},
		"dpkg's lock":              {func(f *fixture) { f.host.dpkg = func([]string) (bool, error) { return true, nil } }, []string{"--yes"}, CodeBusy},
		"an upgrade of the system": {func(f *fixture) { f.writeFile(pathOSBusy, "{}") }, []string{"--yes"}, CodeBusy},
	} {
		t.Run(name, func(t *testing.T) {
			f := installed(t)
			c.setup(f)
			before := f.treeWithoutBookkeeping()
			if code := f.remove(c.args...); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+c.code+"\n") {
				t.Fatalf("exit %d:\n%s", code, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			if !f.runner.isEnabled(testUnit) {
				t.Error("the unit was stopped")
			}
		})
	}
	f := installed(t)
	f.isTTY, f.input = true, "yes\n"
	if code := f.remove(); code != ExitOK || !strings.Contains(f.out.String(), "Go on? [y/N]") {
		t.Errorf("a terminal that says yes: %d\n%s", code, f.out)
	}
}

func TestARemoveOfAModuleWhoseFilesAreHalfGoneCompletes(t *testing.T) {
	f := newFixture(t)
	before := f.treeWithoutBookkeeping()
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("install: %d", code)
	}
	for _, p := range []string{"/usr/bin/casaos-virt-management", "/usr/lib/systemd/system/" + testUnit} {
		if err := os.Remove(f.path(p)); err != nil {
			t.Fatal(err)
		}
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
}

// what a run leaves when it is killed: the list of the files and what was written, and no manifest
func TestARemoveFinishesAnInstallThatWasKilled(t *testing.T) {
	for name, drop := range map[string][]string{
		"no manifest":           {"/etc/casaos/modules/virt.json", "/var/lib/casaos/modules/virt.version"},
		"only the list":         {"/etc/casaos/modules/virt.json", "/var/lib/casaos/modules/virt.version", "/usr/bin/casaos-virt-management", "/etc/casaos/virt-management.conf.sample"},
		"a temporary file left": {},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			before := f.treeWithoutBookkeeping()
			if code := f.install("--yes"); code != ExitOK {
				t.Fatalf("install: %d", code)
			}
			for _, p := range drop {
				if err := os.Remove(f.path(p)); err != nil {
					t.Fatal(err)
				}
			}
			if name == "a temporary file left" {
				if err := os.WriteFile(tempName(f.path("/usr/bin/casaos-virt-management")), []byte("half"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if code := f.remove("--yes"); code != ExitOK {
				t.Fatalf("exit %d:\n%s", code, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
		})
	}
}

func TestARemoveNeverDeletesAFileAnotherModulesListOwns(t *testing.T) {
	f := installed(t)
	// another module that lists a file of this one, and has files of its own
	f.writeFile("/var/lib/casaos/modules/other.files", "/etc/casaos/virt-management.conf.sample\n/usr/bin/other\n")
	f.writeFile("/usr/bin/other", "the other module's")
	f.writeFile("/etc/casaos/modules/other.json", "{}")
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if !f.exists("/etc/casaos/virt-management.conf.sample") {
		t.Error("a file the list of another module owns was deleted")
	}
	if !f.exists("/usr/bin/other") || !f.exists("/var/lib/casaos/modules/other.files") || !f.exists("/etc/casaos/modules/other.json") {
		t.Error("something of the other module was deleted")
	}
	if !strings.Contains(f.out.String(), "kept, it is the list of other too: /etc/casaos/virt-management.conf.sample") {
		t.Errorf("not said:\n%s", f.out)
	}
	for _, p := range []string{"/usr/bin/casaos-virt-management", "/usr/lib/systemd/system/" + testUnit, "/etc/casaos/modules/virt.json", "/var/lib/casaos/modules/virt.files"} {
		if f.exists(p) {
			t.Errorf("%s is left", p)
		}
	}
}

func TestARemoveThatCannotFinishLeavesWhatIsNeededToFinishIt(t *testing.T) {
	t.Run("a file is a directory now", func(t *testing.T) {
		f := installed(t)
		p := f.path("/etc/casaos/virt-management.conf.sample")
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if code := f.remove("--yes"); code != ExitFailed {
			t.Fatalf("exit %d:\n%s", code, f.out)
		}
		for _, p := range []string{"/etc/casaos/modules/virt.json", "/var/lib/casaos/modules/virt.files"} {
			if !f.exists(p) {
				t.Errorf("%s was deleted while a file was not", p)
			}
		}
		lines := f.lines()
		if !slices.Contains(lines, "failed at step files: "+CodeRemoveIncomplete) {
			t.Errorf("not said:\n%s", f.out)
		}
		// once it is mended, a run again finishes
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if code := f.remove("--yes"); code != ExitOK {
			t.Fatalf("again: %d\n%s", code, f.out)
		}
	})
	t.Run("the unit does not stop", func(t *testing.T) {
		f := installed(t)
		f.runner.fail["systemctl disable --now"] = 1
		f.runner.onRun = nil
		before := f.treeWithoutBookkeeping()
		if code := f.remove("--yes"); code != ExitFailed {
			t.Fatalf("exit %d:\n%s", code, f.out)
		}
		sameTree(t, before, f.treeWithoutBookkeeping()) // (nothing of the module was deleted while it runs)
		if !slices.Contains(f.lines(), "failed at step stop: "+CodeRemoveIncomplete) {
			t.Errorf("not said:\n%s", f.out)
		}
	})
	t.Run("a list that cannot be read", func(t *testing.T) {
		f := installed(t)
		f.writeFile("/var/lib/casaos/modules/virt.files", "/etc/passwd\n")
		before := f.treeWithoutBookkeeping()
		if code := f.remove("--yes"); code != ExitFailed {
			t.Fatalf("exit %d:\n%s", code, f.out)
		}
		sameTree(t, before, f.treeWithoutBookkeeping())
		if !f.exists("/etc/passwd") && false {
			t.Error("unreachable")
		}
	})
}

// the list is the only thing that decides what is deleted: a path in it that is not a path an install writes is no list at all
func TestARemoveDeletesOnlyWhatAValidListNames(t *testing.T) {
	for _, bad := range []string{
		"/etc/passwd\n", "/usr/bin/../../etc/passwd\n", "usr/bin/x\n", "/usr/bin/x\n/usr/bin/x\n", "/usr/bin/x", "/usr/bin/\n",
		// the state of the core and of the assistant is nobody's to delete
		"/usr/bin/casaos-virt-management\n/var/lib/casaos/modules/virt.files\n", "/usr/bin/casaos-virt-management\n/etc/casaos/modules/other.json\n",
		"/usr/bin/casaos-virt-management\n/var/lib/casaos/os-upgrade/busy\n", "/usr/bin/casaos-virt-management\n/etc/casaos/os-upgrade.beta\n",
		"/usr/bin/casaos-virt-management\n/var/lib/casaos/alerts.json\n", "/usr/bin/casaos-virt-management\n/usr/share/casaos/modules/catalogue.json\n",
		"/usr/bin/casaos-virt-management\n/etc/casaos/gateway.ini\n", "/usr/bin/casaos-virt-management\n/usr/lib/systemd/system/casaos-gateway.service\n/usr/lib/systemd/system/x.timer\n",
	} {
		f := installed(t)
		f.writeFile("/var/lib/casaos/modules/virt.files", bad)
		f.writeFile("/etc/passwd", "root\n")
		if code := f.remove("--yes"); code != ExitFailed {
			t.Errorf("%q: exit %d", bad, code)
		}
		if !f.exists("/etc/passwd") || !f.exists("/usr/bin/casaos-virt-management") {
			t.Errorf("%q: something was deleted", bad)
		}
	}
	_ = osmodule.MaxEntries
}

// a module that has no list: its manifest goes, no file is guessed at
func TestARemoveWithoutAListDeletesNoFile(t *testing.T) {
	f := installed(t)
	if err := os.Remove(f.path("/var/lib/casaos/modules/virt.files")); err != nil {
		t.Fatal(err)
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if !f.exists("/usr/bin/casaos-virt-management") || f.exists("/etc/casaos/modules/virt.json") {
		t.Error("a file was guessed at, or the manifest is left")
	}
	if !strings.Contains(f.out.String(), "there is no list of the files of virt: no file is deleted") {
		t.Errorf("not said:\n%s", f.out)
	}
}

// the directories the install made are taken away with it, the deepest first, and only those that are empty and only those
func TestARemoveDeletesTheDirectoriesTheInstallMadeAndNoOther(t *testing.T) {
	// the directories of the box before the install stay (usr/bin, etc/casaos), those the install made go
	f := installed(t)
	for _, d := range madeDirs {
		if !f.exists(d) {
			t.Fatalf("%s was not made", d)
		}
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	for _, d := range madeDirs {
		if f.exists(d) {
			t.Errorf("the directory %s is left", d)
		}
	}
	for _, d := range []string{"/usr/bin", "/etc/casaos", "/usr", "/etc"} {
		if !f.exists(d) {
			t.Errorf("the directory %s was removed", d)
		}
	}
	if f.exists("/var/lib/casaos/modules/virt.dirs") {
		t.Error("the list of the directories is left")
	}

	// a directory that holds something of somebody else stays, and so do the ones above it; the remove is done
	f = installed(t)
	f.writeFile("/usr/lib/systemd/system/another.service", "another")
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	for _, d := range madeDirs {
		if !f.exists(d) {
			t.Errorf("the directory %s that holds a file was removed", d)
		}
	}
	if !f.exists("/usr/lib/systemd/system/another.service") || f.exists("/var/lib/casaos/modules/virt.dirs") || f.exists("/var/lib/casaos/modules/virt.files") {
		t.Error("the file of somebody else was deleted, or the lists are left")
	}

	// what is at the place of a directory and is not one is not deleted
	f = installed(t)
	for _, p := range []string{"/usr/lib/systemd/system/" + testUnit, "/usr/lib/systemd/system", "/usr/lib/systemd", "/usr/lib"} {
		if err := os.Remove(f.path(p)); err != nil {
			t.Fatal(err)
		}
	}
	f.writeFile("/usr/lib", "a file where a directory of the install was")
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if f.read("/usr/lib") != "a file where a directory of the install was" {
		t.Error("a file was deleted for a directory")
	}

	// a list of directories that names what no install makes is no list: nothing is deleted
	f = installed(t)
	f.writeFile("/var/lib/casaos/modules/virt.dirs", "/usr/share\n")
	before := f.treeWithoutBookkeeping()
	if code := f.remove("--yes"); code != ExitFailed {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	for _, d := range []string{"/usr/share"} {
		if !f.exists(d) {
			t.Errorf("%s was removed", d)
		}
	}
	if !f.runner.isEnabled(testUnit) {
		t.Error("the unit was stopped though the list could not be read")
	}
}
