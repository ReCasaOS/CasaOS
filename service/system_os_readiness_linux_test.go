//go:build linux

package service

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

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
