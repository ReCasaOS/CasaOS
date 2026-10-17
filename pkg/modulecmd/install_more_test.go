package modulecmd

import (
	"os"
	"strings"
	"testing"
)

// the state of the core and of the assistant is nobody's to write, whatever an archive holds: a path of it is refused when the unpack reaches it (and the files before it are rolled back)
func TestAnArchiveThatHoldsTheStateOfTheCoreOrOfTheAssistantWritesNone(t *testing.T) {
	for _, p := range []string{
		"var/lib/casaos/modules/other.files", "etc/casaos/modules/other.json", "var/lib/casaos/modules/virt.version", "var/lib/casaos/os-upgrade/busy", "etc/casaos/os-upgrade.beta",
	} {
		t.Run(p, func(t *testing.T) {
			f := newFixture(t)
			f.setEntries(append(moduleEntries(manifestJSON(t, testManifest())), tent{name: "build/sysroot/" + p, body: "{}"}))
			before := f.treeWithoutBookkeeping()
			if code := f.install("--yes"); code != ExitFailed {
				t.Fatalf("exit %d\n%s", code, f.out)
			}
			if !strings.Contains(f.out.String(), "failed at step unpack: ") {
				t.Errorf("not refused at the unpack:\n%s", f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			if f.exists("/" + p) {
				t.Errorf("/%s was written", p)
			}
		})
	}
}

// a log is one run: the log of an earlier run is replaced
func TestTheLogIsOneRun(t *testing.T) {
	f := newFixture(t)
	f.runner.fail["systemctl enable --now"] = 1
	if code := f.install("--yes"); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(f.read("/var/log/casaos/module-virt.log"), "start_failed") {
		t.Fatal("the first log")
	}
	delete(f.runner.fail, "systemctl enable --now")
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	log := f.read("/var/log/casaos/module-virt.log")
	if strings.Contains(log, "start_failed") || strings.Count(log, resultMarker) != 1 {
		t.Errorf("the log has more than the last run:\n%s", log)
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("remove %d", code)
	}
	if log := f.read("/var/log/casaos/module-virt.log"); strings.Contains(log, "installed:") || strings.Count(log, resultMarker) != 1 {
		t.Errorf("the log of the remove:\n%s", log)
	}
}

// a file that is not the archive is deleted when it is found out, before the run ends (not only with the area of the download at the end)
type watchWriter struct {
	out   *strings.Builder
	onSay func(line string)
}

func (w *watchWriter) Write(p []byte) (int, error) {
	for _, l := range strings.Split(string(p), "\n") {
		if l != "" {
			w.onSay(l)
		}
	}
	return w.out.Write(p)
}

func TestAFileThatIsNotTheArchiveIsDeletedAtOnce(t *testing.T) {
	f := newFixture(t)
	f.dl.body = append([]byte{}, f.dl.body...)
	f.dl.body[3] ^= 1
	var seenAtFailure []os.DirEntry
	f.env.Stdout = &watchWriter{out: &strings.Builder{}, onSay: func(line string) {
		if strings.Contains(line, " verify failed ") {
			seenAtFailure, _ = os.ReadDir(f.env.Roots.tmp())
		}
	}}
	if code := f.install("--yes"); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if len(seenAtFailure) != 0 {
		t.Errorf("at the failure the area of the download held %v", seenAtFailure)
	}
}
