package modulecmd

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"golang.org/x/sys/unix"
)

// SIGINT, which a terminal sends, ends a run as SIGTERM, which systemd sends, does
func TestASigintIsARollbackToo(t *testing.T) {
	for _, at := range []string{"unpack-mid", "route"} {
		t.Run(at, func(t *testing.T) {
			b := newRealBox(t)
			before := b.state()
			cmd := helperCmd(t, "install", b.helperEnv(at, "block")...)
			var out bytes.Buffer
			cmd.Stdout = &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, stubFile(b.root, "reached-"+at))
			if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("it did not end:\n%s", out.String())
			}
			if cmd.ProcessState.ExitCode() != ExitFailed || !strings.Contains(out.String(), CodeInterrupted+"\n") {
				t.Fatalf("exit %d:\n%s", cmd.ProcessState.ExitCode(), out.String())
			}
			sameTree(t, before, b.state())
			if b.runnerEnabled() {
				t.Error("the unit is enabled")
			}
		})
	}
}

// what a run that was killed left, while it has not got to the manifest, is installed over by the next install (its files are in the list, and are the module's own); from the manifest on the
// module is installed, and remove is what finishes it
func TestTheStateOfAKilledInstallIsInstalledOverWithoutARemoveWhileTheManifestIsNotThere(t *testing.T) {
	for _, at := range []string{"unpack-mid", "units", "manifest"} {
		t.Run(at, func(t *testing.T) {
			b := newRealBox(t)
			cmd := helperCmd(t, "install", b.helperEnv(at, "kill")...)
			_ = cmd.Run()
			if code, out := b.call("install", testName, "--yes"); code != ExitOK {
				t.Fatalf("exit %d\n%s", code, out)
			}
			list, err := readFilesList(b.path("/var/lib/casaos/modules/virt.files"))
			if err != nil || len(list) != len(wantFiles) {
				t.Errorf("the list: %v %v", list, err)
			}
			for _, p := range wantFiles {
				if !b.exists(p) {
					t.Errorf("%s is not there", p)
				}
			}
			if !b.exists("/etc/casaos/modules/virt.json") || !b.runnerEnabled() {
				t.Error("not installed")
			}
		})
	}
	// ...but once the manifest is there the module is installed, and remove is what finishes it
	for _, at := range []string{"start", "route"} {
		t.Run(at, func(t *testing.T) {
			b := newRealBox(t)
			_ = helperCmd(t, "install", b.helperEnv(at, "kill")...).Run()
			code, out := b.call("install", testName, "--yes")
			if code != ExitRefused || !strings.Contains(out, "refused: "+osmodule.CheckInstalled+"\n") {
				t.Fatalf("exit %d\n%s", code, out)
			}
		})
	}
}

func TestOnlyATerminalIsATerminal(t *testing.T) {
	h := newHost()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	null, _ := os.Open("/dev/null") // (a character device that is not a terminal, which is what a unit with no input has)
	defer null.Close()
	regular, _ := os.Open(os.Args[0])
	defer regular.Close()
	for name, f := range map[string]*os.File{"a pipe": r, "/dev/null": null, "a file": regular} {
		if h.IsTerminal(f) {
			t.Errorf("%s is taken for a terminal", name)
		}
	}
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skip("no pseudo-terminal here")
	}
	defer master.Close()
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Skip(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Skip(err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skip("no /dev/pts here: ", err)
	}
	defer slave.Close()
	if !h.IsTerminal(slave) {
		t.Error("a terminal is not taken for one")
	}
}
