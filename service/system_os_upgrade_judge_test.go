package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// aHopThatWasChecked is a hop whose machine restarted and whose check ran: the last block ended, the unit said so, and the judge has not been asked yet.
func (b *upgradeBox) aHopThatWasChecked() *osUpgradeRun {
	b.t.Helper()
	run := b.aHopThatRestarted()
	run = b.attempt(run, 2, osrun.PhaseVerify, osrun.WhoAuto)
	run = b.block(run, 2, 1, osrun.BlockAfter, true)
	run = b.leave(run, "done-after")
	run = b.say(run, osrun.Marker{Kind: osrun.MkVerifyDone, N: 2, Boot: boxBoot2})
	run = b.end(run, 2, "ok")
	b.setUnit("inactive")
	b.advance(time.Minute)
	b.point(run)
	return b.read(run.ID)
}

// takesFacts makes the facts of the machine after the hop what a test says, and counts how many times they were taken.
func takesFacts(t *testing.T, after osverify.Facts) *atomic.Int32 {
	t.Helper()
	var taken atomic.Int32
	was := osUpgradeTakeFacts
	osUpgradeTakeFacts = func(context.Context, osreadiness.Probe) osverify.Facts {
		taken.Add(1)
		return after
	}
	t.Cleanup(func() { osUpgradeTakeFacts = was })
	return &taken
}

func (b *upgradeBox) verdict(run *osUpgradeRun) *osUpgradeVerifyDoc {
	b.t.Helper()
	return readOSUpgradeVerify(b.read(run.ID))
}

// A machine that restarted into the next release and is as it was is judged by the core, once the unit that checked it is over: the finding list is empty, the run
// succeeded.
func TestTheCoreJudgesAMachineThatIsAsItWas(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	before := osverify.Facts{Taken: true, At: box.now(), Kernel: "5.10.0-28-amd64", System: "running", DpkgQuiet: true, DNS: true}
	box.theFactsBefore(run, before)
	after := before
	after.Kernel = "6.1.0-18-amd64"
	taken := takesFacts(t, after)
	box.leave(run, "kernel")
	if err := os.WriteFile(filepath.Join(box.backupOf(run), "kernel"), []byte("6.1.0-18-amd64\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tick := box.tick()
	doc := box.verdict(run)
	if tick.Action != osrun.DoJudge || len(tick.Done) != 1 || doc == nil || len(doc.Findings) != 0 || taken.Load() != 1 {
		t.Fatalf("the look did %+v, the verdict is %+v, the facts were taken %d times", tick, doc, taken.Load())
	}
	if !doc.At.Equal(box.now()) {
		t.Errorf("the verdict is dated %v", doc.At)
	}
	if state := box.state(run); state.Name != osrun.StateSucceeded {
		t.Errorf("the run is %q", state.Name)
	}
}

// The judge asks for the facts once for a run: a verdict that is written is not made again by the next look.
func TestTheCoreJudgesOnce(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	taken := takesFacts(t, osverify.Facts{Taken: true, At: box.now(), DpkgQuiet: true})

	box.tick()
	box.advance(30 * time.Second)
	box.tick()
	box.advance(30 * time.Second)
	box.tick()
	if taken.Load() != 1 {
		t.Errorf("the facts were taken %d times for one verdict", taken.Load())
	}
	if box.verdict(run) == nil {
		t.Error("no verdict")
	}
}

// A machine that is not what the hop promised is a run that failed its check, with the finding that says what: the kernel that runs is not the one that was installed.
func TestTheCoreJudgesAMachineThatRunsAnotherKernel(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	if err := os.WriteFile(filepath.Join(box.backupOf(run), "kernel"), []byte("6.1.0-18-amd64\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	box.theFactsBefore(run, osverify.Facts{Taken: true, At: box.now(), Kernel: "5.10.0-28-amd64", DpkgQuiet: true})
	takesFacts(t, osverify.Facts{Taken: true, At: box.now(), Kernel: "5.10.0-28-amd64", System: "running", DpkgQuiet: true})

	box.tick()
	doc := box.verdict(run)
	if doc == nil || len(doc.Findings) == 0 || doc.Findings[0].Code != osverify.CodeKernelUnexpected || doc.Findings[0].Severity != osverify.SeverityError {
		t.Fatalf("the verdict is %+v", doc)
	}
	state := box.state(run)
	if state.Name != osrun.StateFailed || state.Reason != osrun.ReasonVerify {
		t.Errorf("the run is %+v", state)
	}
}

// What is only a warning ends the run as a success with findings.
func TestTheCoreJudgesAMachineThatLostItsRoute(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	box.theFactsBefore(run, osverify.Facts{Taken: true, At: box.now(), Route: "enp3s0", DpkgQuiet: true})
	takesFacts(t, osverify.Facts{Taken: true, At: box.now(), Route: "", System: "running", DpkgQuiet: true})

	box.tick()
	doc := box.verdict(run)
	if doc == nil || len(doc.Findings) != 1 || doc.Findings[0].Code != osverify.CodeRouteMissing {
		t.Fatalf("the verdict is %+v", doc)
	}
	if state := box.state(run); state.Name != osrun.StateSucceededWithFindings {
		t.Errorf("the run is %q", state.Name)
	}
}

// The facts of before that were lost are the zero facts, and the machine is judged by what is known of it.
func TestTheCoreJudgesWithTheFactsOfBeforeLost(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	if err := os.Remove(filepath.Join(run.Dir, osUpgradeFactsFile)); err != nil {
		t.Fatal(err)
	}
	takesFacts(t, osverify.Facts{Taken: true, At: box.now(), System: "running", DpkgQuiet: true})

	box.tick()
	if doc := box.verdict(run); doc == nil || len(doc.Findings) != 0 {
		t.Errorf("the verdict is %+v", doc)
	}
}

// A machine that is not asked to be judged is not: the judge runs for a run that was checked, and for nothing else.
func TestTheCoreJudgesNothingBeforeTheCheckEnded(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatRestarted()
	box.point(run)
	taken := takesFacts(t, osverify.Facts{Taken: true})

	box.tick()
	if taken.Load() != 0 || box.verdict(run) != nil {
		t.Errorf("the machine was judged before the check: %d facts taken", taken.Load())
	}
}

// A request for the status never judges: it reads, and the judge is the settling's.
func TestTheStatusNeverJudges(t *testing.T) {
	box := newUpgradeBox(t)
	box.aHopThatWasChecked()
	taken := takesFacts(t, osverify.Facts{Taken: true})

	status := box.GetSystemOSUpgradeStatus()
	if taken.Load() != 0 || status.State != osrun.StateVerifying {
		t.Errorf("the status judged: %d facts taken, state %q", taken.Load(), status.State)
	}
}

// A verdict that could not be written is made again at the next look.
func TestAVerdictThatCouldNotBeWrittenIsMadeAgain(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	taken := takesFacts(t, osverify.Facts{Taken: true, At: box.now(), System: "running", DpkgQuiet: true})

	was := osUpgradeWriteFile
	failing := true
	osUpgradeWriteFile = func(path string, data []byte, mode os.FileMode) error {
		if failing && filepath.Base(path) == osUpgradeVerifyFile {
			return errors.New("the disk is full")
		}
		return was(path, data, mode)
	}
	t.Cleanup(func() { osUpgradeWriteFile = was })

	box.tick()
	if box.verdict(run) != nil {
		t.Fatal("a verdict was written on a disk that is full")
	}
	failing = false
	box.advance(30 * time.Second)
	box.tick()
	if box.verdict(run) == nil || taken.Load() != 2 {
		t.Errorf("verdict %+v after %d takes", box.verdict(run), taken.Load())
	}
}

// The files that the blocks left are read where the core keeps them, by the name that the unit was told.
func TestTheJudgeReadsTheBackupFolderWhereTheCoreKeepsIt(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	dir := box.backupOf(run)
	if err := os.WriteFile(filepath.Join(dir, "docker-source-note"), []byte("unreachable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	takesFacts(t, osverify.Facts{Taken: true, At: box.now(), System: "running", DpkgQuiet: true})

	box.tick()
	doc := box.verdict(run)
	if doc == nil || len(doc.Findings) != 1 || doc.Findings[0].Code != osverify.CodeDockerSourceUnreachable {
		t.Errorf("the verdict is %+v: the note of block 8 was not read", doc)
	}
}
