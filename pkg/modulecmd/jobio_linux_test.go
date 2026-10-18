//go:build linux

package modulecmd

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// A log that is a fifo is not read: a read of it would wait for a writer, or say nothing, and neither is a log.
func TestAFifoIsNotALog(t *testing.T) {
	env := jobBox(t)
	path := env.Roots.logFile(jName)
	if err := os.MkdirAll(env.Roots.logs(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0o644); err != nil {
		t.Skip("a fifo cannot be made here")
	}
	if job, err := ReadJob(env, jName, UnitGone, at(0)); err == nil {
		t.Errorf("a fifo was read: %+v", job)
	}
}
