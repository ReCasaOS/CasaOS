package modulecmd

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// stubsIn writes the stand-ins for systemctl and apt-get in a directory of the box other than usr/bin: they find their root from the path they are run by.
func stubsIn(t *testing.T, root, dir string) {
	t.Helper()
	for name, text := range map[string]string{"systemctl": systemctlStub, "apt-get": aptStub} {
		text = strings.Replace(text, "${0%/usr/bin/"+name+"}", "${0%/"+dir+"/"+name+"}", 1)
		p := filepath.Join(root, dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(root, "stub")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"sim.txt": aptSim, "est.txt": aptEst} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const shadow = "#!/bin/sh\n: > \"$(dirname \"$0\")/../../stub/shadow-ran\"\nexit 0\n"

// A box whose systemctl is in /bin and not in /usr/bin (a system whose /bin is not /usr/bin): an archive that ships usr/bin/systemctl is refused, and never run as root. (It used to be
// accepted by the allow-list of the unpack, and the assistant then found it before the real one, and ran it, and said installed.)
func TestAnArchiveThatShipsOneOfTheProgramsTheAssistantRunsIsRefusedAndNeverRun(t *testing.T) {
	for _, name := range []string{"systemctl", "apt-get", "dpkg"} {
		t.Run(name, func(t *testing.T) {
			b := newRealBox(t)
			// the layout of the box: the programs are in /bin, there is nothing of the name in /usr/bin
			if err := os.RemoveAll(filepath.Join(b.root, "usr")); err != nil {
				t.Fatal(err)
			}
			stubsIn(t, b.root, "bin")
			b.setEntries(append(moduleEntries(manifestJSON(t, testManifest())), tent{name: "build/sysroot/usr/bin/" + name, mode: 0o755, body: shadow}))
			before := b.state()
			code, out := b.call("install", testName, "--yes")
			if code != ExitFailed || !strings.Contains(out, "failed at step unpack: "+osmodule.UnpackPath+"\n") {
				t.Fatalf("exit %d\n%s", code, out)
			}
			if exists(stubFile(b.root, "shadow-ran")) || b.exists("/usr/bin/"+name) {
				t.Error("the program of the archive was written, or run")
			}
			sameTree(t, before, b.state())
		})
	}
}

// The programs are found once, when the runner is made: one that appears in a directory of the path afterwards is not the one that is run
func TestAProgramThatAppearsAfterTheRunnerWasMadeIsNotTheOneThatIsRun(t *testing.T) {
	root := t.TempDir()
	stubsIn(t, root, "bin")
	r := newRunner(Roots{Prefix: root})
	if err := os.MkdirAll(filepath.Join(root, "usr/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "usr/bin/systemctl"), []byte(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(t.Context(), Cmd{Argv: []string{"systemctl", "daemon-reload"}}); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, "stub/shadow-ran")) || !exists(filepath.Join(root, "stub/systemctl.log")) {
		t.Error("the program that appeared was run, or the one that was there was not")
	}
}

func TestOnlyTheProgramsOfTheAssistantAreRun(t *testing.T) {
	root := t.TempDir()
	stubsIn(t, root, "usr/bin")
	r := newRunner(Roots{Prefix: root})
	if _, err := r.Run(t.Context(), Cmd{Argv: []string{"rm", "-rf", "/"}}); !errors.Is(err, osreadiness.ErrNotAllowed) {
		t.Errorf("rm: %v", err)
	}
	if _, err := r.Run(t.Context(), Cmd{Argv: []string{"journalctl"}}); !errors.Is(err, osreadiness.ErrToolMissing) {
		t.Errorf("a program of the assistant that the box does not have: %v", err)
	}
	if _, err := r.Run(t.Context(), Cmd{Argv: []string{"systemctl", "is-active", "x"}}); err != nil {
		t.Errorf("systemctl: %v", err)
	}
}

var _ = tar.TypeReg
