package modulecmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The tests of the update that run for real: the assistant is a process of its own on a box in a temporary directory, with the file system, the lock and the signals of Linux and the
// stubs for systemctl and apt-get, and it is killed (with the one signal nobody can catch) or told to stop at each step.

// updatableRealBox is a real box with v0.1.0 installed and the downloads of the helpers (dl/) naming v0.1.1.
func updatableRealBox(t *testing.T) *realBox {
	t.Helper()
	b := newRealBox(t, withEntries(v1Entries(t)))
	if code, out := b.call("install", testName, "--yes"); code != ExitOK {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	b.fixture.publish(newTag, testManifest(), v2Entries(t, testManifest()))
	cat, err := os.ReadFile(b.path(pathCatalogue))
	if err != nil {
		t.Fatal(err)
	}
	dl := filepath.Join(b.root, "dl")
	for name, raw := range map[string][]byte{"catalogue.json": cat, "archive.tar.gz": b.fixture.archive} {
		if err := os.WriteFile(filepath.Join(dl, name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(b.path(pathCatalogue)); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *realBox) updateEnv(at, mode string) []string {
	return append(b.helperEnv(at, mode), "MODULECMD_VERB=update")
}

// updated is the state of a box once the update has been made, by a process that nothing ends.
func updatedState(t *testing.T) (map[string]string, []string) {
	t.Helper()
	b := updatableRealBox(t)
	var out bytes.Buffer
	cmd := helperCmd(t, "install", b.updateEnv("none", "none")...)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v:\n%s", err, out.String())
	}
	return b.state(), b.dirs()
}

func (b *realBox) running() bool {
	return exists(stubFile(b.root, "enabled-"+testUnit)) && !exists(stubFile(b.root, "stopped-"+testUnit))
}

func TestARealUpdatePutsTheNewVersionOnTheBoxWithTheModesOfTheArchive(t *testing.T) {
	b := updatableRealBox(t)
	var out bytes.Buffer
	cmd := helperCmd(t, "install", b.updateEnv("none", "none")...)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v:\n%s", err, out.String())
	}
	for p, want := range map[string]os.FileMode{progPath: 0o755, toolPath: 0o755, samplePath: 0o644, unitPath: 0o644} {
		info, err := os.Lstat(b.path(p))
		if err != nil || info.Mode().Perm() != want || b.read(p) != wantAfter[p] {
			t.Errorf("%s: %v, %v, %q", p, info, err, b.read(p))
		}
	}
	if exists(b.path(helperPath)) || !b.running() {
		t.Error("the helper is there, or the unit is not running")
	}
	// no temporary name is left in the directories the update wrote to
	for _, dir := range []string{"/usr/bin", "/etc/casaos", "/usr/lib/systemd/system", "/var/lib/casaos/modules", "/etc/casaos/modules"} {
		entries, _ := os.ReadDir(b.path(dir))
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".cos-") {
				t.Errorf("%s/%s is left", dir, e.Name())
			}
		}
	}
	if entries, _ := os.ReadDir(b.env2tmp()); len(entries) != 0 {
		t.Errorf("the area of the download: %v", entries)
	}
}

// a process that is killed at each point of an update leaves a box that the next update finishes, and that a remove takes away
func TestAKillAtEachPointOfAnUpdateLeavesAStateThatTheNextRunFinishes(t *testing.T) {
	wantUpdated, wantDirs := updatedState(t)
	fresh := newRealBox(t, withEntries(v1Entries(t)))
	wantNothing, wantNoDirs := fresh.state(), fresh.dirs()
	for _, at := range killPointsOfUpdate {
		kill := func(t *testing.T) *realBox {
			b := updatableRealBox(t)
			cmd := helperCmd(t, "install", b.updateEnv(at, "kill")...)
			cmd.Stdout, cmd.Stderr = nil, nil
			err := cmd.Run()
			ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
			if err == nil || !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
				t.Fatalf("not killed: %v", err)
			}
			return b
		}
		t.Run("update after a kill at "+at, func(t *testing.T) {
			b := kill(t)
			if code, out := b.call("update", testName, "--yes"); code != ExitOK {
				t.Fatalf("update again: exit %d\n%s", code, out)
			}
			sameTree(t, wantUpdated, b.state())
			sameDirs(t, wantDirs, b.dirs())
			if !b.running() {
				t.Error("the unit is not running")
			}
		})
		t.Run("remove after a kill at "+at, func(t *testing.T) {
			b := kill(t)
			if code, out := b.call("remove", testName, "--yes"); code != ExitOK {
				t.Fatalf("remove: exit %d\n%s", code, out)
			}
			sameTree(t, wantNothing, b.state())
			sameDirs(t, wantNoDirs, b.dirs())
			if b.runnerEnabled() {
				t.Error("the unit is still enabled")
			}
		})
	}
}

// a process that is told to stop at each step ends the step as a failure, puts the old version back and exits 3
func TestASigtermAtEachStepOfAnUpdateIsARollback(t *testing.T) {
	for _, at := range append(append([]string{}, updateSteps...), "swap-1", "swap-2", "swap-3") {
		t.Run(at, func(t *testing.T) {
			b := updatableRealBox(t)
			before, dirsBefore := b.state(), b.dirs()
			cmd := helperCmd(t, "install", b.updateEnv(at, "block")...)
			var out bytes.Buffer
			cmd.Stdout = &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, stubFile(b.root, "reached-"+at))
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("it did not end:\n%s", out.String())
			}
			step := at
			if strings.HasPrefix(at, "swap-") {
				step = StepSwap
			}
			if cmd.ProcessState.ExitCode() != ExitFailed || !strings.Contains(out.String(), "failed at step "+step+": "+CodeInterrupted+"\n") {
				t.Fatalf("exit %d:\n%s", cmd.ProcessState.ExitCode(), out.String())
			}
			if !strings.Contains(out.String(), " rolled_back ") {
				t.Errorf("not rolled back:\n%s", out.String())
			}
			sameTree(t, before, b.state())
			sameDirs(t, dirsBefore, b.dirs())
			if !b.running() {
				t.Error("the old unit is not running")
			}
			if entries, _ := os.ReadDir(b.env2tmp()); len(entries) != 0 {
				t.Errorf("the area of the download: %v", entries)
			}
		})
	}
}

// the command that was running when the signal came is ended with the run
func TestASigtermEndsTheCommandOfAnUpdateThatIsRunning(t *testing.T) {
	for _, c := range []struct{ name, flag, reached, step string }{
		{"systemctl stop", "block-stop", "reached-stop", StepStop},
		{"systemctl daemon-reload", "block-daemon-reload", "reached-daemon-reload", StepUnits},
		{"systemctl start", "block-start", "reached-start", StepStart},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := updatableRealBox(t)
			before := b.state()
			touch(t, stubFile(b.root, c.flag))
			cmd := helperCmd(t, "install", b.updateEnv("none", "none")...)
			var out bytes.Buffer
			cmd.Stdout = &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, stubFile(b.root, c.reached))
			started := time.Now()
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_ = cmd.Wait()
			if time.Since(started) > 10*time.Second {
				t.Errorf("the run took %v to end: the command was waited for", time.Since(started))
			}
			if cmd.ProcessState.ExitCode() != ExitFailed || !strings.Contains(out.String(), "failed at step "+c.step+": "+CodeInterrupted) {
				t.Fatalf("exit %d:\n%s", cmd.ProcessState.ExitCode(), out.String())
			}
			_ = os.Remove(stubFile(b.root, c.flag))
			sameTree(t, before, b.state())
			if !b.running() {
				t.Error("the old unit is not running")
			}
		})
	}
}
