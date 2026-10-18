package modulecmd

import (
	"archive/tar"
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// fileEntry is the entry of a list for a file that holds the content; linkEntry for a link to the target.
func fileEntry(path, content string) osmodule.FileEntry {
	return osmodule.FileEntry{Path: path, Want: []string{sum([]byte(content))}}
}

func linkEntry(path, target string) osmodule.FileEntry {
	return osmodule.FileEntry{Path: path, Link: true, Want: []string{target}}
}

// writeList puts a list of the files on the box, in the format that records the contents.
func (f *fixture) writeList(entries ...osmodule.FileEntry) {
	f.t.Helper()
	raw, err := osmodule.FormatFilesList(entries)
	if err != nil {
		f.t.Fatal(err)
	}
	f.writeFile(filesList, string(raw))
}

// the entries of the files of the archive of the tests, as an install lists them (the unit last)
func archiveEntries() []osmodule.FileEntry {
	var program, sample, unit string
	for _, e := range moduleEntries("") {
		switch {
		case strings.HasSuffix(e.name, "/casaos-virt-management"):
			program = e.body
		case strings.HasSuffix(e.name, ".sample"):
			sample = e.body
		case strings.HasSuffix(e.name, ".service"):
			unit = e.body
		}
	}
	return []osmodule.FileEntry{
		fileEntry("/usr/bin/casaos-virt-management", program),
		fileEntry("/etc/casaos/virt-management.conf.sample", sample),
		fileEntry("/usr/lib/systemd/system/"+testUnit, unit),
	}
}

func (f *fixture) listed() []osmodule.FileEntry {
	f.t.Helper()
	l, err := osmodule.ParseFilesList([]byte(f.read(filesList)))
	if err != nil || !l.Verified {
		f.t.Fatalf("the list: %+v %v", l, err)
	}
	return l.Entries
}

// What the list of a killed install names and was never written is not the module's: a file that somebody puts there afterwards is not replaced by an install and not deleted by a remove.
// (The list used to name paths and nothing else, and an install took the file at a planned path for its own, and a remove deleted it.)
func TestAFileOfSomebodyAtAPlannedPathThatWasNeverWrittenIsLeftAlone(t *testing.T) {
	newState := func() *fixture {
		f := newFixture(t)
		f.writeList(archiveEntries()...) // (what was recorded before the first file was put in place)
		f.writeFile(dirsList, strings.Join(madeDirs, "\n")+"\n")
		f.writeFile("/usr/bin/casaos-virt-management", "somebody's program")
		return f
	}
	t.Run("an install does not replace it", func(t *testing.T) {
		f := newState()
		if code := f.install("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), "failed at step unpack: "+osmodule.FileExists+"\n") {
			t.Fatalf("exit %d\n%s", code, f.out)
		}
		if got := f.read("/usr/bin/casaos-virt-management"); got != "somebody's program" {
			t.Errorf("the file of somebody else is now %q", got)
		}
		if f.exists("/etc/casaos/virt-management.conf.sample") || f.exists("/usr/lib/systemd/system/"+testUnit) {
			t.Error("something was written")
		}
	})
	t.Run("a remove does not delete it", func(t *testing.T) {
		f := newState()
		if code := f.remove("--yes"); code != ExitOK {
			t.Fatalf("exit %d\n%s", code, f.out)
		}
		if got := f.read("/usr/bin/casaos-virt-management"); got != "somebody's program" {
			t.Errorf("the file of somebody else is now %q", got)
		}
		if !strings.Contains(f.out.String(), "left: /usr/bin/casaos-virt-management (changed)\n") {
			t.Errorf("not said:\n%s", f.out)
		}
		if f.exists(filesList) || f.exists(dirsList) {
			t.Error("the lists are left")
		}
	})
}

// ...and a path that was planned and never written is not in the list that the install ends with: the list is exactly what was written
func TestTheListOfAnInstallThatEndsIsExactlyWhatWasWritten(t *testing.T) {
	f := newFixture(t)
	// the list of a killed install: the three files of the archive, one more of an earlier version that was never there, and one that is there and is the module's
	f.writeFile("/usr/bin/casaos-virt-old", "an earlier version")
	entries := append(archiveEntries(), fileEntry("/usr/bin/casaos-virt-never", "was never written"), fileEntry("/usr/bin/casaos-virt-old", "an earlier version"))
	f.writeList(entries...)
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	var got []string
	for _, e := range f.listed() {
		got = append(got, e.Path)
	}
	want := append(slices.Clone(wantFiles), "/usr/bin/casaos-virt-old")
	if !slices.Equal(sorted(got), sorted(want)) {
		t.Errorf("the list: %v\nwant %v", got, want)
	}
	for _, e := range f.listed() {
		if len(e.Want) != 1 {
			t.Errorf("%s holds %v: one content is what an install that ended leaves", e.Path, e.Want)
		}
	}
}

// A remove, and the rollback of an install, delete a path only if it still holds what the list says; anything else is left, and said
func TestARemoveDeletesOnlyWhatStillHoldsWhatTheListSays(t *testing.T) {
	f := installed(t)
	f.writeFile("/usr/bin/casaos-virt-management", "changed since")
	if err := os.Remove(f.path("/etc/casaos/virt-management.conf.sample")); err != nil { // (gone: nothing to delete, nothing to say)
		t.Fatal(err)
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	if got := f.read("/usr/bin/casaos-virt-management"); got != "changed since" {
		t.Errorf("a file that was changed: %q", got)
	}
	if f.exists("/usr/lib/systemd/system/" + testUnit) {
		t.Error("the unit is left")
	}
	out := f.out.String()
	if !strings.Contains(out, "left: /usr/bin/casaos-virt-management (changed)\n") || strings.Contains(out, "virt-management.conf.sample") {
		t.Errorf("said:\n%s", out)
	}
}

func TestALinkIsDeletedByARemoveOnlyIfItStillPointsWhereItDid(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(f *fixture)
		left   bool
	}{
		"it points where it did": {func(f *fixture) {}, false},
		"it points elsewhere": {func(f *fixture) {
			p := f.path("/usr/bin/virt")
			_ = os.Remove(p)
			if err := os.Symlink("other", p); err != nil {
				f.t.Skip("no links here")
			}
		}, true},
		"it is a file now": {func(f *fixture) {
			_ = os.Remove(f.path("/usr/bin/virt"))
			f.writeFile("/usr/bin/virt", "a file")
		}, true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.setEntries(append(moduleEntries(manifestJSON(t, testManifest())), tent{name: "build/sysroot/usr/bin/virt", typ: tar.TypeSymlink, link: "casaos-virt-management"}))
			if code := f.install("--yes"); code != ExitOK {
				t.Skipf("install: %d\n%s", code, f.out)
			}
			tc.change(f)
			if code := f.remove("--yes"); code != ExitOK {
				t.Fatalf("exit %d\n%s", code, f.out)
			}
			_, err := os.Lstat(f.path("/usr/bin/virt"))
			left := err == nil
			if left != tc.left || left != strings.Contains(f.out.String(), "left: /usr/bin/virt (changed)\n") {
				t.Errorf("left %v, want %v:\n%s", left, tc.left, f.out)
			}
		})
	}
}

// A list of the earlier format names paths and nothing to check them against: a remove deletes none of its files, and says which they are
func TestARemoveDeletesNoFileOfAListOfTheEarlierFormat(t *testing.T) {
	f := installed(t)
	f.writeFile(filesList, strings.Join(wantFiles, "\n")+"\n")
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	for _, p := range wantFiles {
		if !f.exists(p) {
			t.Errorf("%s was deleted", p)
		}
		if !strings.Contains(f.out.String(), "left: "+p+" (the list is of an earlier format") {
			t.Errorf("%s is not named:\n%s", p, f.out)
		}
	}
	if f.exists(filesList) || f.exists("/etc/casaos/modules/virt.json") {
		t.Error("the list or the manifest is left")
	}
}

// ...and an install over one does not take its files for its own, says which they are, and ends with a list of what it wrote
func TestAnInstallOverAListOfTheEarlierFormatLeavesItsFilesAlone(t *testing.T) {
	f := newFixture(t)
	f.writeFile("/usr/bin/casaos-virt-old", "an earlier version")
	f.writeFile(filesList, "/usr/bin/casaos-virt-old\n")
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	if !f.exists("/usr/bin/casaos-virt-old") || !strings.Contains(f.out.String(), "left: /usr/bin/casaos-virt-old (the list is of an earlier format)\n") {
		t.Errorf("not left, or not said:\n%s", f.out)
	}
	var got []string
	for _, e := range f.listed() {
		got = append(got, e.Path)
	}
	if !slices.Equal(sorted(got), sorted(wantFiles)) {
		t.Errorf("the list: %v", got)
	}
}

// The rollback of an install deletes what it wrote and only that: a file that was changed since is left
func TestTheRollbackLeavesAFileThatWasChangedSinceItWasWritten(t *testing.T) {
	f := newFixture(t)
	f.runner.fail["systemctl enable --now"] = 1
	f.env.StepHook = func(_ context.Context, step, phase string) {
		if step == StepStart && phase == "begin" {
			f.writeFile("/usr/bin/casaos-virt-management", "changed since")
		}
	}
	if code := f.install("--yes"); code != ExitFailed {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	if got := f.read("/usr/bin/casaos-virt-management"); got != "changed since" {
		t.Errorf("the file that was changed: %q", got)
	}
	if !strings.Contains(f.out.String(), "left: /usr/bin/casaos-virt-management (changed)\n") || f.exists("/etc/casaos/virt-management.conf.sample") {
		t.Errorf("said, or not deleted:\n%s", f.out)
	}
}

// A file that an install replaces is the module's whichever of the two contents is on the disk: the list holds both until the install is over
func TestAFileBeingReplacedIsInTheListWithBothContents(t *testing.T) {
	f := newFixture(t)
	f.writeFile("/usr/bin/casaos-virt-management", "an earlier version")
	f.writeList(fileEntry("/usr/bin/casaos-virt-management", "an earlier version"))
	checked := false
	f.host.wrapDest = func(d osmodule.Destination) osmodule.Destination {
		return &hookedDest{Destination: d, onFile: func() {
			if checked {
				return
			}
			checked = true
			var program osmodule.FileEntry
			for _, e := range f.listed() {
				if e.Path == "/usr/bin/casaos-virt-management" {
					program = e
				}
			}
			old, current := sum([]byte("an earlier version")), archiveEntries()[0].Want[0]
			if len(program.Want) != 2 || !slices.Contains(program.Want, old) || !slices.Contains(program.Want, current) {
				t.Errorf("the program holds %v before the first file is written", program.Want)
			}
		}}
	}
	if code := f.install("--yes"); code != ExitOK || !checked {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	if e := f.listed()[0]; len(e.Want) != 1 {
		t.Errorf("the list at the end: %+v", e)
	}
}
