package modulecmd

import (
	"strings"
	"testing"
)

// A process that is killed right after the lists were written, before the first file is in place, leaves a list that names paths that were never written: a file that somebody puts at one of
// them afterwards survives the remove.
func TestAFileOfSomebodyAtAPathAKilledInstallNeverWroteSurvivesARemove(t *testing.T) {
	b := newRealBox(t)
	cmd := helperCmd(t, "install", b.helperEnv("unpack-planned", "kill")...)
	_ = cmd.Run()
	if b.exists("/usr/bin/casaos-virt-management") || !b.exists(filesList) {
		t.Fatal("the process was not killed between the lists and the first file")
	}
	b.writeFile("/usr/bin/casaos-virt-management", "somebody's program")
	code, out := b.call("remove", testName, "--yes")
	if code != ExitOK || !strings.Contains(out, "left: /usr/bin/casaos-virt-management (changed)\n") {
		t.Fatalf("remove: %d\n%s", code, out)
	}
	if got := b.read("/usr/bin/casaos-virt-management"); got != "somebody's program" {
		t.Errorf("the file of somebody else is now %q", got)
	}
	// and an install over the same state does not replace it either
	if code, out := b.call("install", testName, "--yes"); code == ExitOK {
		t.Fatalf("installed over the file of somebody else:\n%s", out)
	}
}

// A file of the module that an install replaces is the module's whichever of its two contents the kill found: a remove deletes it
func TestAFileBeingReplacedWhenTheKillCameIsDeletedByARemove(t *testing.T) {
	for _, at := range []string{"unpack-planned", "unpack-first", "unpack-mid"} {
		t.Run(at, func(t *testing.T) {
			b := newRealBox(t)
			before, dirsBefore := b.state(), b.dirs()
			b.writeFile("/usr/bin/casaos-virt-management", "an earlier version")
			b.writeList(fileEntry("/usr/bin/casaos-virt-management", "an earlier version"))
			cmd := helperCmd(t, "install", b.helperEnv(at, "kill")...)
			_ = cmd.Run()
			if code, out := b.call("remove", testName, "--yes"); code != ExitOK || strings.Contains(out, "left:") {
				t.Fatalf("remove: %d\n%s", code, out)
			}
			sameTree(t, before, b.state())
			sameDirs(t, dirsBefore, b.dirs())
		})
	}
}
