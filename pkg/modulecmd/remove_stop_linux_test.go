package modulecmd

import (
	"os"
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

// A SIGTERM that comes before anything is deleted, when the unit is stopped already, ends the run there: it was told to stop, and nothing is deleted
func TestASigtermBeforeAnythingIsDeletedEndsARemoveThere(t *testing.T) {
	b := newRealBox(t)
	if code, out := b.call("install", testName, "--yes"); code != ExitOK {
		t.Fatalf("install: %d\n%s", code, out)
	}
	if err := os.Remove(stubFile(b.root, "enabled-"+testUnit)); err != nil { // (the unit is stopped already)
		t.Fatal(err)
	}
	before := b.state()
	cmd := helperCmd(t, "install", append(b.helperEnv(StepStop, "block"), "MODULECMD_VERB=remove")...)
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, stubFile(b.root, "reached-"+StepStop))
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = cmd.Wait()
	if cmd.ProcessState.ExitCode() != ExitFailed || !strings.Contains(out.String(), "failed at step stop: "+CodeInterrupted+"\n") {
		t.Fatalf("exit %d:\n%s", cmd.ProcessState.ExitCode(), out.String())
	}
	sameTree(t, before, b.state())
}

// ...and one that comes after the unit is stopped, in the middle of the deletion, does not leave the module half deleted: the deletion is done whole
func TestASigtermInTheMiddleOfTheDeletionOfARemoveDoesNotLeaveTheModuleHalfDeleted(t *testing.T) {
	b := newRealBox(t)
	before := b.state()
	if code, out := b.call("install", testName, "--yes"); code != ExitOK {
		t.Fatalf("install: %d\n%s", code, out)
	}
	cmd := helperCmd(t, "install", append(b.helperEnv(StepFiles, "selfterm"), "MODULECMD_VERB=remove")...)
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v:\n%s", err, out.String())
	}
	sameTree(t, before, b.state())
	if b.runnerEnabled() || !strings.Contains(out.String(), "removed: virt\n") {
		t.Errorf("not removed:\n%s", out.String())
	}
}
