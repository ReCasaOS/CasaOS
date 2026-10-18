//go:build linux

package modulecmd

import (
	"context"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// a file that is not a regular file is not opened: a pipe in its place would hold the assistant for as long as nobody wrote to it
func TestAnOSReleaseThatIsAPipeIsNotWaitedFor(t *testing.T) {
	f := newFixture(t)
	bareOS(f)
	f.writeFile("/usr/lib/os-release", debian12)
	if err := unix.Mkfifo(f.path("/etc/os-release"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		id, v := (&box{ctx: context.Background(), env: &f.env}).OS()
		done <- id + v
	}()
	select {
	case got := <-done:
		if got != "" {
			t.Errorf("a pipe is a box: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the assistant waits for a pipe")
	}
}
