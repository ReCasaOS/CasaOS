package modulecmd

import (
	"strings"
	"testing"
)

// A list of an earlier install that cannot be read (cut short, or naming what an install never writes) stops the install before it writes anything, with a refusal that says so: the
// assistant does not guess what is the module's, and the list is for the administrator to look at.
func TestAListOfAnEarlierInstallThatCannotBeReadIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	refused := "refused: " + CodeFilesUnreadable + "\n"
	for name, c := range map[string]struct{ path, content string }{
		"a list of files with nothing in it":       {filesList, ""},
		"a list of files that names a state file":  {filesList, "/var/lib/casaos/alerts.json\n"},
		"a list of files with no new line":         {filesList, "/usr/bin/casaos-virt-management"},
		"a list of files cut after its first line": {filesList, "casaos-module-files 1\n"},
		"a list of files with a line cut":          {filesList, "casaos-module-files 1\nf aaaa /usr/bin/casaos-virt-management\n"},
		"a list of directories with nothing in":    {dirsList, ""},
		"a list of directories that names a file":  {dirsList, "/usr/bin/casaos-virt-management\n"},
		"a list of directories that names /usr":    {dirsList, "/usr/share\n"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.writeFile(c.path, c.content)
			before := f.treeWithoutBookkeeping()
			if code := f.install("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), refused) {
				t.Fatalf("exit %d\n%s", code, f.out)
			}
			if !strings.Contains(f.out.String(), "the list "+c.path+" cannot be read") {
				t.Errorf("the list is not named:\n%s", f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			if f.exists("/usr/bin/casaos-virt-management") || f.runner.isEnabled(testUnit) || f.runner.called("apt-get", "install") != 0 {
				t.Error("something was done")
			}
		})
	}
}
