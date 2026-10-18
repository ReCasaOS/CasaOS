package modulecmd

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// A remove over a list that cannot be read still stops the unit (the manifest names it), deletes nothing by the list, names the list to delete by hand, fails the step of the files, and does
// not say that a run again will finish: it would read the same list.
func TestARemoveOverAListThatCannotBeReadStopsTheUnitAndNamesTheListToDelete(t *testing.T) {
	for name, c := range map[string]struct{ path, content string }{
		"a list of files with nothing in it":        {filesList, ""},
		"a list of files that names a file of /etc": {filesList, "/etc/passwd\n"},
		"a list of files cut short":                 {filesList, "casaos-module-files 1\nf aaaa"},
		"a list of directories with nothing in":     {dirsList, ""},
		"a list of directories that names /usr":     {dirsList, "/usr/share\n"},
	} {
		t.Run(name, func(t *testing.T) {
			f := installed(t)
			f.writeFile("/etc/passwd", "root\n")
			f.writeFile(c.path, c.content)
			before := f.treeWithoutBookkeeping()
			if code := f.remove("--yes"); code != ExitFailed {
				t.Fatalf("exit %d\n%s", code, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			if f.runner.isEnabled(testUnit) || f.runner.called("systemctl", "disable", "--now", testUnit) != 1 {
				t.Error("the unit was not stopped")
			}
			out := f.out.String()
			if got := markers(t, f.lines()); !slices.Equal(got, []string{"stop begin", "stop done", "files begin", "files failed"}) {
				t.Errorf("markers: %q", got)
			}
			for _, want := range []string{"delete " + c.path + " by hand", "failed at step files: " + CodeFilesUnreadable + "\n", resultMarker + " - virt failed files " + CodeFilesUnreadable + " - "} {
				if !strings.Contains(out, want) {
					t.Errorf("the output does not say %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "again to finish") {
				t.Errorf("it promises that a run again will finish:\n%s", out)
			}
			// once the list is gone, a run finishes: no file is deleted by a list that is not there
			if err := os.Remove(f.path(c.path)); err != nil { // (the owner looked at it and deleted it)
				t.Fatal(err)
			}
			if code := f.remove("--yes"); code != ExitOK {
				t.Fatalf("after the list is deleted: exit %d\n%s", code, f.out)
			}
			if !f.exists("/etc/passwd") {
				t.Error("a file outside the allowed places was deleted")
			}
		})
	}
}

// The unit that does not stop is the first thing a remove says, whatever the lists are
func TestAUnitThatDoesNotStopComesBeforeAListThatCannotBeRead(t *testing.T) {
	f := installed(t)
	f.writeFile(filesList, "")
	f.runner.fail["systemctl disable --now"] = 1
	if code := f.remove("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), "failed at step stop: "+CodeUnitNotStopped+"\n") {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
}
