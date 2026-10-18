package modulecmd

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"golang.org/x/sys/unix"
)

// The processes the tests run for real: the test binary itself, run again with MODULECMD_HELPER set, which does one thing and ends. A kill and a signal are tested on a process that is
// not the test's.

func helperCmd(t *testing.T, mode string, env ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.count=1")
	cmd.Env = append(append(os.Environ(), "MODULECMD_HELPER="+mode), env...)
	return cmd
}

// startHolding starts a helper that holds a lock and says "ready"; it lets go when its standard input closes.
func startHolding(t *testing.T, mode, path string) (stop func()) {
	t.Helper()
	cmd := helperCmd(t, mode, "MODULECMD_ARG="+path)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "ready\n" {
		_ = cmd.Process.Kill()
		t.Fatalf("the helper: %q, %v", line, err)
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			_ = in.Close()
			_ = cmd.Wait()
		})
	}
	t.Cleanup(stop)
	return stop
}

// realEnv is the Env of the runs that are real: the commands are the stubs in the root, the lock and the file system are Linux's. The gateway answers, apt says what the tests ask it to.
func realEnv(root string, getenv func(string) string, stdout io.Writer) Env {
	roots := Roots{Prefix: root}
	sb := newFakeSandbox()
	return Env{
		Roots: roots, Stdout: stdout, Stderr: io.Discard,
		Getenv: getenv, EUID: func() int { return 0 }, Arch: "amd64", Version: "0.5.29",
		Runner: newRunner(roots), Sandbox: func() osplan.Sandbox { return sb },
		Download: nil, Gateway: &fakeGateway{}, Clock: realClock{}, Terminal: func() bool { return false },
		Host: newHost(),
	}
}

var helperCtx struct {
	sync.Mutex
	ctx context.Context
}

// TestHelperProcess is not a test: it is what the helpers run.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("MODULECMD_HELPER")
	if mode == "" {
		return
	}
	arg := os.Getenv("MODULECMD_ARG")
	switch mode {
	case "dpkglock":
		f, err := os.OpenFile(arg, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			os.Exit(2)
		}
		lock := unix.Flock_t{Type: unix.F_WRLCK}
		if err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lock); err != nil {
			os.Exit(3)
		}
		_, _ = os.Stdout.WriteString("ready\n")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "flock":
		release, err := newHost().Lock(arg)
		if err != nil {
			os.Exit(3)
		}
		_, _ = os.Stdout.WriteString("ready\n")
		_, _ = io.Copy(io.Discard, os.Stdin)
		release()
		os.Exit(0)
	case "install":
		os.Exit(helperInstall())
	}
	os.Exit(4)
}

// helperInstall runs `casaos module install` as the core's main does (Run, with the signals), in a root that the test made, and stops it where the test asks:
//
//	MODULECMD_AT    the step ("packages"... "route"), "unpack-planned" (the lists are written and the directories made, no file is in place), "unpack-first" (the first file of the unpack
//	                is in place) or "unpack-mid" (two files of it are)
//	MODULECMD_UMASK the umask of the process, in octal
//	MODULECMD_MODE  "kill": SIGKILL to itself there; "block": say it has got there (<root>/stub/reached-<at>) and wait for the signal that ends the run
//	                "panic": panic there; "waitfile": wait there for <root>/stub/go to be made
//	MODULECMD_VERB  "remove" to run a remove in place of the install
func helperInstall() int {
	root := os.Getenv("MODULECMD_ROOT")
	at, mode := os.Getenv("MODULECMD_AT"), os.Getenv("MODULECMD_MODE")
	if u := os.Getenv("MODULECMD_UMASK"); u != "" {
		n, _ := strconv.ParseUint(u, 8, 32)
		syscall.Umask(int(n))
	}
	env := realEnv(root, os.Getenv, os.Stdout)
	reach := func() {
		switch mode {
		case "kill":
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			time.Sleep(time.Minute)
		case "panic":
			panic("a panic of the test")
		case "selfterm":
			// the signal that ends the run comes here, and the run goes on
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
			time.Sleep(300 * time.Millisecond)
		case "waitfile":
			// go on when the test makes <root>/stub/go: it has closed what reads our output by then
			for range 600 {
				if _, err := os.Stat(filepath.Join(root, "stub", "go")); err == nil {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		case "block":
			_ = os.WriteFile(filepath.Join(root, "stub", "reached-"+at), nil, 0o644)
			helperCtx.Lock()
			ctx := helperCtx.ctx
			helperCtx.Unlock()
			select {
			case <-ctx.Done():
			case <-time.After(time.Minute):
			}
		}
	}
	env.StepHook = func(ctx context.Context, step, phase string) {
		helperCtx.Lock()
		helperCtx.ctx = ctx
		helperCtx.Unlock()
		if step == at && phase == "begin" {
			reach()
		}
	}
	if strings.HasPrefix(at, "unpack-") {
		after := map[string]int{"unpack-planned": 0, "unpack-first": 1, "unpack-mid": 2}[at]
		env.Host = &hostOverride{Host: newHost(), free: env.Host.FreeBytes, wrapDest: func(d osmodule.Destination) osmodule.Destination {
			return &reachingDest{Destination: d, after: after, reach: reach}
		}}
	}
	args := []string{"install", testName, "--yes"}
	if id := os.Getenv("MODULECMD_PLAN_ID"); id != "" {
		args = append(args, "--plan-id", id)
	}
	if os.Getenv("MODULECMD_VERB") == "remove" {
		args = []string{"remove", testName, "--yes"}
	}
	return Run(args, env)
}

// reachingDest calls reach when the file after the n-th is asked for.
type reachingDest struct {
	osmodule.Destination
	after, files int
	reach        func()
}

func (d *reachingDest) NewFile(p string, perm os.FileMode, replace bool) (osmodule.File, error) {
	if d.files++; d.files > d.after {
		d.reach()
		return nil, os.ErrDeadlineExceeded // (a run that was told to stop does not go on writing)
	}
	return d.Destination.NewFile(p, perm, replace)
}

func itoa(n int) string { return strconv.Itoa(n) }
