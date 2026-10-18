package modulecmd

import (
	"strings"
	"syscall"
	"testing"
)

// The real thing: SIGTERM while `systemctl disable --now` runs ends the command with the run; the unit is still running; nothing is deleted and the run says so (it used to read "no
// answer" as "not running", delete everything and say removed)
func TestASigtermWhileTheUnitIsBeingStoppedDeletesNothing(t *testing.T) {
	b := newRealBox(t)
	if code, out := b.call("install", testName, "--yes"); code != ExitOK {
		t.Fatalf("install: %d\n%s", code, out)
	}
	before := b.state()
	touch(t, stubFile(b.root, "block-disable"))
	cmd := helperCmd(t, "install", append(b.helperEnv("none", "none"), "MODULECMD_VERB=remove")...)
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, stubFile(b.root, "reached-disable"))
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = cmd.Wait()
	if cmd.ProcessState.ExitCode() != ExitFailed {
		t.Fatalf("exit %d:\n%s", cmd.ProcessState.ExitCode(), out.String())
	}
	if !b.runnerEnabled() {
		t.Error("the stub unit was stopped: the test did not interrupt the stop")
	}
	if strings.Contains(out.String(), "removed: ") || !strings.Contains(out.String(), "failed at step stop: "+CodeUnitNotStopped+"\n") {
		t.Errorf("said:\n%s", out.String())
	}
	sameTree(t, before, b.state())
}
