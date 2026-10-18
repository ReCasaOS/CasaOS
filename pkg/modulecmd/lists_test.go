package modulecmd

import (
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// A list of an earlier install that cannot be read (cut short, or naming what an install never writes) stops the install before it writes anything: the assistant does not guess what is the
// module's. A list of files is read by the plan, which refuses (it cannot say whose the unit is), and a list of directories by the unpack, which fails before it writes.
func TestAListOfAnEarlierInstallThatCannotBeReadStopsTheInstallBeforeAnythingIsWritten(t *testing.T) {
	refusedForeign := "refused: " + osmodule.CheckForeignUnit + "\n"
	unreadable := "failed at step unpack: " + CodeFilesUnreadable + "\n"
	for name, c := range map[string]struct {
		path, content string
		exit          int
		line          string
	}{
		"a list of files with nothing in it":      {filesList, "", ExitRefused, refusedForeign},
		"a list of files that names a state file": {filesList, "/var/lib/casaos/alerts.json\n", ExitRefused, refusedForeign},
		"a list of files with no new line":        {filesList, "/usr/bin/casaos-virt-management", ExitRefused, refusedForeign},
		"a list of directories with nothing in":   {dirsList, "", ExitFailed, unreadable},
		"a list of directories that names a file": {dirsList, "/usr/bin/casaos-virt-management\n", ExitFailed, unreadable},
		"a list of directories that names /usr":   {dirsList, "/usr/share\n", ExitFailed, unreadable},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.writeFile(c.path, c.content)
			before := f.treeWithoutBookkeeping()
			if code := f.install("--yes"); code != c.exit || !strings.Contains(f.out.String(), c.line) {
				t.Fatalf("exit %d\n%s", code, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			if f.exists("/usr/bin/casaos-virt-management") || f.runner.isEnabled(testUnit) {
				t.Error("something was written")
			}
		})
	}
}
