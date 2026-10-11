//go:build linux

package service

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

// TestHoldDpkgLockHelper is not a test: the test below runs it in another process, which
// takes the write lock apt and dpkg take (fcntl, a POSIX record lock) and holds it until it
// is killed. A lock held by the probing process itself would not show: locks belong to a
// process, and F_GETLK never reports one's own.
func TestHoldDpkgLockHelper(t *testing.T) {
	path := os.Getenv("CASAOS_TEST_HOLD_LOCK")
	if path == "" {
		t.Skip("run by TestAnyLockHeldSeesAnotherProcessesLock")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	lock := syscall.Flock_t{Type: syscall.F_WRLCK}
	if err := syscall.FcntlFlock(file.Fd(), syscall.F_SETLK, &lock); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("locked\n")
	time.Sleep(time.Minute)
}

// A lock file that cannot be looked at is not a lock nobody holds: lockStatus says so, where
// anyLockHeld (the question of the update, which refuses nothing for what it cannot see) does not.
func TestLockStatusSaysWhatItCouldNotLookAt(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "not-there")
	// a path under a file is not a path that is not there: it is one that cannot be opened
	broken := filepath.Join(file, "lock")

	if held, err := lockStatus(nil); held || err != nil {
		t.Errorf("no file: held = %v, err = %v", held, err)
	}
	if held, err := lockStatus([]string{missing}); held || err != nil {
		t.Errorf("a file that is not there: held = %v, err = %v", held, err)
	}
	if held, err := lockStatus([]string{file}); held || err != nil {
		t.Errorf("a file nobody holds: held = %v, err = %v", held, err)
	}
	if held, err := lockStatus([]string{broken}); held || err == nil {
		t.Errorf("a file that cannot be opened: held = %v, err = %v", held, err)
	}
	if held, err := lockStatus([]string{missing, broken, file}); held || err == nil {
		t.Errorf("one that cannot be opened among others: held = %v, err = %v", held, err)
	}
	if anyLockHeld([]string{broken}) {
		t.Error("the question of the update says held for a file it cannot open")
	}
	// the first thing that could not be looked at is the one said, and it says which file
	other := filepath.Join(file, "other")
	var pathError *fs.PathError
	if _, err := lockStatus([]string{broken, other}); !errors.As(err, &pathError) || pathError.Path != broken {
		t.Errorf("two files that cannot be opened: err = %v, want the first, %s", err, broken)
	}

	// a kernel that will not say is not a lock that is not held, either
	original := getLock
	t.Cleanup(func() { getLock = original })
	getLock = func(uintptr, *syscall.Flock_t) error { return syscall.EOPNOTSUPP }
	if held, err := lockStatus([]string{file}); held || !errors.Is(err, syscall.EOPNOTSUPP) {
		t.Errorf("a kernel that will not say: held = %v, err = %v", held, err)
	} else if !errors.As(err, &pathError) || pathError.Path != file || pathError.Op != "fcntl" {
		t.Errorf("a kernel that will not say, and does not say which file: %v", err)
	}
	if held, err := lockStatus([]string{missing, file}); held || err == nil {
		t.Errorf("a kernel that will not say, after a file that is not there: held = %v, err = %v", held, err)
	}
	second := filepath.Join(dir, "second")
	if err := os.WriteFile(second, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lockStatus([]string{file, second}); !errors.As(err, &pathError) || pathError.Path != file {
		t.Errorf("a kernel that will not say, for two files: err = %v, want the first, %s", err, file)
	}
	if anyLockHeld([]string{file}) {
		t.Error("the question of the update says held for a lock the kernel will not say")
	}
	getLock = original

	holder := exec.Command(os.Args[0], "-test.run=^TestHoldDpkgLockHelper$")
	holder.Env = append(os.Environ(), "CASAOS_TEST_HOLD_LOCK="+file)
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill(); _ = holder.Wait() })
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("the lock holder said %q, %v", line, err)
	}
	// a lock that is held is seen, whatever else could not be looked at
	if held, err := lockStatus([]string{broken, file}); !held || err != nil {
		t.Errorf("a held lock and a file that cannot be opened: held = %v, err = %v", held, err)
	}
	if held, err := lockStatus([]string{file, broken}); !held || err != nil {
		t.Errorf("a held lock and a file that cannot be opened, the other way: held = %v, err = %v", held, err)
	}
}

// The files a package manager locks: apt's frontend lock, dpkg's own, and the lists' lock.
func TestTheLockFilesAreThoseOfAptAndDpkg(t *testing.T) {
	want := []string{"/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock", "/var/lib/apt/lists/lock"}
	if !reflect.DeepEqual(dpkgLockFiles, want) {
		t.Fatalf("dpkgLockFiles = %v, want %v", dpkgLockFiles, want)
	}
}

func TestAnyLockHeldSeesAnotherProcessesLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock-frontend")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "not-there")
	if anyLockHeld([]string{missing, path}) {
		t.Fatal("a lock nobody holds, and a file that is not there, were reported held")
	}

	holder := exec.Command(os.Args[0], "-test.run=^TestHoldDpkgLockHelper$")
	holder.Env = append(os.Environ(), "CASAOS_TEST_HOLD_LOCK="+path)
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill(); _ = holder.Wait() })
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("the lock holder died before it locked: %v", err)
		}
		if line == "locked\n" {
			break
		}
	}

	if !anyLockHeld([]string{missing, path}) {
		t.Fatal("a write lock held by another process was not seen")
	}
	// the probe took nothing and created nothing: it can be asked again, and the file is as it was
	if !anyLockHeld([]string{path}) {
		t.Fatal("the second look at a held lock did not see it")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("the probe created %s: %v", missing, err)
	}

	_ = holder.Process.Kill()
	_ = holder.Wait()
	for i := 0; i < 50 && anyLockHeld([]string{path}); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if anyLockHeld([]string{path}) {
		t.Fatal("the lock is still reported after its holder is gone")
	}
}
