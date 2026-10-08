//go:build linux

package service

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
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
