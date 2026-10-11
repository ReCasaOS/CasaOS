//go:build linux

package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The process of a command whose time has ended is killed, and not left to run behind the report.
func TestACommandThatRunsOutOfTimeIsKilled(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	probe, _ := readinessProbe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = probe.Run(ctx, sh, "-c", "echo $$ > '"+pidFile+"'; exec sleep 30")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the command did not start in time to say who it is: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the process %d still runs after the end of its time: %v", pid, err)
	}
}

// A FIFO that a file of the sources could be would be waited for for ever by an open: it is not read.
func TestAFileThatIsNotARegularOneIsNotOpened(t *testing.T) {
	probe, _ := readinessProbe(t)
	fifo := filepath.Join(t.TempDir(), "sources.list")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("no FIFO here: %v", err)
	}
	answered := make(chan error, 1)
	go func() {
		_, err := probe.ReadFile(context.Background(), fifo)
		answered <- err
	}()
	select {
	case err := <-answered:
		if err == nil {
			t.Fatal("a FIFO was read as a file")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the read of a FIFO waits")
	}
}
