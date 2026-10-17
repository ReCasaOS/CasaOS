package modulecmd

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// ---- the lock ------------------------------------------------------------------------------------------------------------------------------------------------------------------

func TestTheLockIsHeldByOneProcessAtATime(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	h := newHost()
	release, err := h.Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	// the same process cannot take it twice, nor can another
	if _, err := h.Lock(path); !errors.Is(err, ErrLockHeld) {
		t.Errorf("twice: %v", err)
	}
	cmd := helperCmd(t, "flock", "MODULECMD_ARG="+path)
	if out, err := cmd.Output(); err == nil || len(out) != 0 {
		t.Errorf("another process took it: %v %q", err, out)
	}
	release()
	// let go, it is free; and the file stays, which means nothing
	if !exists(path) {
		t.Error("the lock file is gone")
	}
	release2, err := h.Lock(path)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	release2()
}

// a lock from a process that is dead blocks nobody: the kernel lets go of it with the process, however it died
func TestALockOfAProcessThatWasKilledIsNoLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	cmd := helperCmd(t, "flock", "MODULECMD_ARG="+path)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 6)
	if _, err := out.Read(buf); err != nil || string(buf) != "ready\n" {
		t.Fatalf("%q %v", buf, err)
	}
	h := newHost()
	if _, err := h.Lock(path); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("while it lives: %v", err)
	}
	_ = cmd.Process.Signal(syscall.SIGKILL)
	_ = cmd.Wait()
	_ = in.Close()
	release, err := h.Lock(path)
	if err != nil {
		t.Fatalf("after the kill: %v", err)
	}
	// the file holds the pid of the one who has it, as a help to whoever looks
	raw, _ := os.ReadFile(path)
	if strings.TrimSpace(string(raw)) != itoa(os.Getpid()) {
		t.Errorf("the lock file: %q", raw)
	}
	release()
}

// the commands a run starts do not hold its lock: it is let go of when the run is, whatever is still running that it started
func TestTheCommandsOfARunDoNotInheritItsLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	h := newHost()
	release, err := h.Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	sleeper := exec.Command("sh", "-c", "sleep 20")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() }()
	release()
	r, err := h.Lock(path)
	if err != nil {
		t.Fatalf("a child kept the lock: %v", err)
	}
	r()
}

// ---- dpkg's lock, the room ------------------------------------------------------------------------------------------------------------------------------------

func TestDpkgLockIsAskedOfTheKernelWithoutTakingIt(t *testing.T) {
	dir := t.TempDir()
	held, free, missing := filepath.Join(dir, "lock-frontend"), filepath.Join(dir, "lock"), filepath.Join(dir, "none")
	touch(t, free)
	h := newHost()
	if got, err := h.DpkgLockHeld([]string{free, missing}); got || err != nil {
		t.Errorf("free: %v %v", got, err)
	}
	if got, err := h.DpkgLockHeld(nil); got || err != nil {
		t.Errorf("none: %v %v", got, err)
	}
	stop := startHolding(t, "dpkglock", held)
	if got, err := h.DpkgLockHeld([]string{free, held}); !got || err != nil {
		t.Errorf("held: %v %v", got, err)
	}
	if got, err := h.DpkgLockHeld([]string{held, missing}); !got || err != nil {
		t.Errorf("held, first: %v %v", got, err)
	}
	// asking took nothing: the process that held it ends, and the lock is free
	stop()
	if got, err := h.DpkgLockHeld([]string{held}); got || err != nil {
		t.Errorf("after: %v %v", got, err)
	}
	// a directory is no lock file one can ask about
	if got, err := h.DpkgLockHeld([]string{dir}); got || err == nil {
		t.Logf("a directory: %v %v", got, err)
	}
}

// the lock of dpkg that is held keeps an install away, with the real kernel asked
func TestAnInstallIsRefusedWhileDpkgHoldsItsLock(t *testing.T) {
	b := newRealBox(t)
	lock := b.path("/var/lib/dpkg/lock-frontend")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	stop := startHolding(t, "dpkglock", lock)
	before := b.state()
	code, out := b.call("install", testName, "--yes")
	if code != ExitRefused || !strings.Contains(out, "refused: busy\n") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	sameTree(t, before, b.state())
	stop()
	if code, out := b.call("install", testName, "--yes"); code != ExitOK {
		t.Fatalf("once it is free: %d\n%s", code, out)
	}
}

func TestTheRoomIsThatOfTheFileSystemOfTheNearestDirectoryThatExists(t *testing.T) {
	h := newHost()
	dir := t.TempDir()
	here, err := h.FreeBytes(dir)
	if err != nil || here <= 0 {
		t.Fatalf("%d %v", here, err)
	}
	deeper, err := h.FreeBytes(filepath.Join(dir, "a", "b", "c"))
	if err != nil || deeper <= 0 {
		t.Errorf("a path that is not there: %d %v", deeper, err)
	}
	if _, err := h.FreeBytes(filepath.Join(dir, "x", "..", "..", "..", "nowhere", "at", "all")); err != nil {
		t.Logf("%v", err)
	}
}

// ---- the file system the archive is written to -----------------------------------------------------------------------------------------------------------

func TestTheUnpackWritesExactModesWhateverTheUmaskIs(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	f := newFixture(t)
	ents := moduleEntries(manifestJSON(t, testManifest()))
	ents = append(ents,
		tent{name: "build/sysroot/usr/bin/setuid", mode: 0o4755, body: "x"},
		tent{name: "build/sysroot/usr/bin/groupwrite", mode: 0o666, body: "x"},
		tent{name: "build/sysroot/usr/share/casaos/virt/deep/er/file", mode: 0o755, body: "x"},
	)
	f.setEntries(ents)
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	for p, want := range map[string]fs.FileMode{
		"/usr/bin/casaos-virt-management": 0o755, "/usr/bin/setuid": 0o755, "/usr/bin/groupwrite": 0o644, "/etc/casaos/virt-management.conf.sample": 0o644,
		"/usr/lib/systemd/system/" + testUnit: 0o644, "/usr/share/casaos/virt/deep/er/file": 0o755,
	} {
		info, err := os.Lstat(f.path(p))
		if err != nil || info.Mode() != want {
			t.Errorf("%s: %v %v, want %v", p, info.Mode(), err, want)
		}
	}
	// the directories that were made are 0755 too
	for _, p := range []string{"/usr/share/casaos/virt", "/usr/share/casaos/virt/deep", "/usr/share/casaos/virt/deep/er"} {
		info, err := os.Lstat(f.path(p))
		if err != nil || info.Mode().Perm() != 0o755 || !info.IsDir() {
			t.Errorf("%s: %v %v", p, info.Mode(), err)
		}
	}
	// and the directories of the state of the assistant are 0755 and the area of the download 0700
	for p, want := range map[string]fs.FileMode{"/var/lib/casaos/modules": 0o755, "/etc/casaos/modules": 0o755, "/var/log/casaos": 0o755, "/var/lib/casaos/modules/tmp": 0o700} {
		if info, err := os.Lstat(f.path(p)); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %v", p, info.Mode(), err, want)
		}
	}
	if info, err := os.Lstat(f.path("/var/lib/casaos/modules/virt.files")); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("the list: %v %v", info, err)
	}
	if info, err := os.Lstat(f.env.Roots.tmp()); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the area of the download: %v %v", info, err)
	}
}

func TestTheUnpackNeverWritesThroughALinkAndLeavesNoTemporaryFile(t *testing.T) {
	f := newFixture(t)
	victim := f.path("/etc/victim")
	f.writeFile("/etc/victim", "not the module's")
	// a link at the temporary name of a file of the module
	if err := os.MkdirAll(f.path("/usr/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, f.path("/usr/bin/.casaos-virt-management.casaos-tmp")); err != nil {
		t.Fatal(err)
	}
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	if got := f.read("/etc/victim"); got != "not the module's" {
		t.Errorf("written through a link: %q", got)
	}
	if info, err := os.Lstat(f.path("/usr/bin/casaos-virt-management")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("the file: %v %v", info, err)
	}
	// no temporary file is left anywhere under the root
	_ = filepath.WalkDir(f.root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".casaos-tmp") {
			t.Errorf("left: %s", p)
		}
		return nil
	})
	// a link where a file of the module goes is refused, and nothing is written
	g := newFixture(t)
	g.writeFile("/etc/victim", "not the module's")
	if err := os.MkdirAll(g.path("/usr/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(g.path("/etc/victim"), g.path("/usr/bin/casaos-virt-management")); err != nil {
		t.Fatal(err)
	}
	before := g.treeWithoutBookkeeping()
	if code := g.install("--yes"); code != ExitFailed || !strings.Contains(g.out.String(), "failed at step unpack: "+osmodule.UnpackPath) {
		t.Fatalf("exit %d\n%s", code, g.out)
	}
	sameTree(t, before, g.treeWithoutBookkeeping())
	if g.read("/etc/victim") != "not the module's" {
		t.Error("written through a link")
	}
}

func TestALinkOfTheArchiveIsMadeAndRemoved(t *testing.T) {
	f := newFixture(t)
	f.setEntries(append(moduleEntries(manifestJSON(t, testManifest())), tent{name: "build/sysroot/usr/bin/virt", typ: tar.TypeSymlink, link: "casaos-virt-management"}))
	before := f.treeWithoutBookkeeping()
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	if target, err := os.Readlink(f.path("/usr/bin/virt")); err != nil || target != "casaos-virt-management" {
		t.Errorf("the link: %q %v", target, err)
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("remove %d", code)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
}

// on a system with a merged /usr, /lib is a link to usr/lib: a unit that the list spells under /lib is found under /usr/lib too, and is one file, spelled as the list spells it
func TestAMergedUsrIsOneSpellingOfAUnit(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.path("/usr/lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("usr/lib", f.path("/lib")); err != nil {
		t.Fatal(err)
	}
	ents := moduleEntries(manifestJSON(t, testManifest()))
	for i := range ents {
		ents[i].name = strings.Replace(ents[i].name, "build/sysroot/usr/lib/systemd/system/", "build/sysroot/lib/systemd/system/", 1)
	}
	f.setEntries(ents)
	bx := &box{ctx: context.Background(), env: &f.env}
	// a unit that is nobody's, found once, spelled as the unpack would
	f.writeFile("/usr/lib/systemd/system/"+testUnit, "[Unit]\n")
	if got, _ := bx.UnitFiles(testUnit); !slices.Equal(got, []string{"/usr/lib/systemd/system/" + testUnit}) {
		t.Errorf("one file twice: %v", got)
	}
	_ = os.Remove(f.path("/usr/lib/systemd/system/" + testUnit))
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	list, err := readFilesList(f.path("/var/lib/casaos/modules/virt.files"))
	if err != nil || !slices.Contains(list, "/lib/systemd/system/"+testUnit) {
		t.Fatalf("the list: %v %v", list, err)
	}
	got, err := bx.UnitFiles(testUnit)
	if err != nil || !slices.Equal(got, []string{"/lib/systemd/system/" + testUnit}) {
		t.Errorf("the unit of the module: %v %v", got, err)
	}
	// it is the module's own: the check of a foreign unit passes for it, with the module installed or not
	v := f.json("plan", "virt")
	for _, c := range v["checks"].([]any) {
		if m := c.(map[string]any); m["code"] == "foreign_unit" && m["ok"] != true {
			t.Errorf("foreign: %v", m)
		}
	}
	if code := f.remove("--yes"); code != ExitOK || f.exists("/usr/lib/systemd/system/"+testUnit) {
		t.Errorf("remove: %d", code)
	}
}

// ---- the commands ----------------------------------------------------------------------------------------------------------------------------------------------------------

func TestTheRunnerRunsACommandWithItsOwnEnvironmentAndNoStandardInput(t *testing.T) {
	root := t.TempDir()
	writeStubs(t, root)
	script := "#!/bin/sh\nprintf '%s\\n' \"args=$*\" \"home=$HOME\" \"secret=$SECRET_OF_THE_TEST\" \"path=$PATH\" \"cwd=$(pwd)\"\nread line && printf '%s\\n' \"stdin=$line\"\nexit 7\n"
	if err := os.WriteFile(filepath.Join(root, "usr/bin/printer"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SECRET_OF_THE_TEST", "hunter2")
	t.Setenv("HOME", "/home/someone")
	r := newRunner(Roots{Prefix: root})
	out, err := r.Run(context.Background(), Cmd{Argv: []string{"printer", "a", "b c"}, Env: []string{"PATH=/x", "LC_ALL=C"}})
	if err != nil || out.ExitCode != 7 {
		t.Fatalf("%v %+v", err, out)
	}
	if !strings.Contains(out.Text, "args=a b c\n") || !strings.Contains(out.Text, "path=/x\n") || !strings.Contains(out.Text, "secret=\n") || !strings.Contains(out.Text, "home=\n") || !strings.Contains(out.Text, "cwd=/\n") || strings.Contains(out.Text, "stdin=") {
		t.Errorf("the command saw:\n%s", out.Text)
	}
	// a command that is not there
	if _, err := r.Run(context.Background(), Cmd{Argv: []string{"nothing"}}); !errors.Is(err, osreadiness.ErrToolMissing) {
		t.Errorf("missing: %v", err)
	}
	// the command is looked for in the directories of the box and not in the PATH of the process
	if _, err := r.Run(context.Background(), Cmd{Argv: []string{"sh", "-c", "exit 0"}}); !errors.Is(err, osreadiness.ErrToolMissing) {
		t.Errorf("sh is found in the PATH of the process: %v", err)
	}
	// the output goes where it is asked to go, and not into the result
	var streamed bytes.Buffer
	out, err = r.Run(context.Background(), Cmd{Argv: []string{"printer"}, Stdout: &streamed})
	if err != nil || out.Text != "" || !strings.Contains(streamed.String(), "args=\n") {
		t.Errorf("streamed: %v %q %q", err, out.Text, streamed.String())
	}
}

func TestTheRunnerEndsACommandAndWhatItStartedWhenTheContextEnds(t *testing.T) {
	root := t.TempDir()
	writeStubs(t, root)
	pidFile := filepath.Join(root, "child.pid")
	// a command that starts a child and waits; asked to stop, it says so and goes
	script := "#!/bin/sh\ntrap 'printf \"%s\\n\" asked > \"" + root + "/asked\"; exit 0' TERM\nsleep 60 &\nprintf '%s\\n' \"$!\" > \"" + pidFile + "\"\nwait\n"
	if err := os.WriteFile(filepath.Join(root, "usr/bin/waiter"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := newRunner(Roots{Prefix: root})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, Cmd{Argv: []string{"waiter"}}); done <- err }()
	waitFor(t, pidFile)
	time.Sleep(100 * time.Millisecond)
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("it did not end")
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("took %v", time.Since(started))
	}
	if !exists(filepath.Join(root, "asked")) {
		t.Error("it was killed before it was asked to stop")
	}
	// and what it started is gone with it
	raw, _ := os.ReadFile(pidFile)
	if err := syscall.Kill(atoi(strings.TrimSpace(string(raw))), 0); err == nil {
		t.Errorf("the child %s is running", raw)
	}
	// a timeout is the same ending, with the error of the timeout
	_, err := r.Run(context.Background(), Cmd{Argv: []string{"waiter"}, Timeout: 300 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout: %v", err)
	}
}

// a command that does not stop when it is asked is killed after the grace
func TestTheRunnerKillsACommandThatWillNotStop(t *testing.T) {
	root := t.TempDir()
	script := "#!/bin/sh\ntrap '' TERM\nwhile :; do sleep 1; done\n"
	if err := os.MkdirAll(filepath.Join(root, "usr/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "usr/bin/stubborn"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runProcess(ctx, filepath.Join(root, "usr/bin/stubborn"), Cmd{Argv: []string{"stubborn"}}, 500*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 5*time.Second {
		t.Errorf("%v after %v", err, time.Since(started))
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
