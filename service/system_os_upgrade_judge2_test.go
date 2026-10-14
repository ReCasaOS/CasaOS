package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// The judge never turns what it could not see into a good word.

// A core that is told to stop while it looks at a machine writes no verdict: what was not taken is not a machine that is well, and the next look judges again.
func TestTheJudgeWritesNothingWhenTheCoreIsToldToStopWhileItLooks(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	box.theFactsBefore(run, factsOfAMachine(box))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// the facts are taken as the product takes them: a cancelled context takes none of them
	err := box.osUpgradeJudge(ctx, run, time.Time{})
	if !errors.Is(err, errOSUpgradeNotJudged) {
		t.Fatalf("err = %v", err)
	}
	if box.verdict(run) != nil {
		t.Error("a verdict was written for a machine that was not looked at")
	}
	if state := box.state(run); state.Name != osrun.StateVerifying {
		t.Errorf("the run is %+v: it is not judged", state)
	}
	// the next look, with the core going on, judges it
	takesFacts(t, factsOfAMachine(box))
	if tick := box.tick(); len(tick.Done) != 1 || box.verdict(run) == nil {
		t.Errorf("the next look did %+v", tick)
	}
}

// A core that is told to stop after every fact was taken, while the backup folder is read, writes no verdict either: the guard is the context, and not only the facts that are missing.
func TestTheJudgeWritesNothingWhenTheCoreIsToldToStopAfterTheFactsWereTaken(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	box.theFactsBefore(run, factsOfAMachine(box))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	was := osUpgradeTakeFacts
	osUpgradeTakeFacts = func(context.Context, osreadiness.Probe) osverify.Facts {
		facts := factsOfAMachine(box)
		cancel()
		return facts
	}
	t.Cleanup(func() { osUpgradeTakeFacts = was })
	err := box.osUpgradeJudge(ctx, run, time.Time{})
	if !errors.Is(err, errOSUpgradeNotJudged) || box.verdict(run) != nil {
		t.Errorf("err %v, verdict %+v", err, box.verdict(run))
	}
}

func TestTheJudgeDoesNotJudgeWhileSystemdDoesNotAnswer(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	taken := takesFacts(t, factsOfAMachine(box))
	box.setUnit("")
	err := box.osUpgradeJudge(context.Background(), run, time.Time{})
	if !errors.Is(err, errOSUpgradeNotJudged) || taken.Load() != 0 || box.verdict(run) != nil {
		t.Errorf("err %v, facts taken %d, verdict %+v", err, taken.Load(), box.verdict(run))
	}
	// and the loop does not ask it to
	if tick := box.tick(); tick.Action == osrun.DoJudge || taken.Load() != 0 {
		t.Errorf("the look did %+v", tick)
	}
}

func TestTheJudgeDoesNotJudgeWhenSystemdDidNotSayWhatStateTheSystemIsIn(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	facts := factsOfAMachine(box)
	facts.System = ""
	facts.Unknown = []string{osverify.FactSystem}
	takesFacts(t, facts)
	err := box.osUpgradeJudge(context.Background(), run, time.Time{})
	if !errors.Is(err, errOSUpgradeNotJudged) || box.verdict(run) != nil {
		t.Errorf("err %v, verdict %+v", err, box.verdict(run))
	}
}

func TestTheJudgeDoesNotJudgeBeforeTheLastBlockOfTheCheckEnded(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	if err := os.Remove(filepath.Join(box.backupOf(run), "done-after")); err != nil {
		t.Fatal(err)
	}
	taken := takesFacts(t, factsOfAMachine(box))
	err := box.osUpgradeJudge(context.Background(), box.read(run.ID), time.Time{})
	if !errors.Is(err, errOSUpgradeNotJudged) || taken.Load() != 0 || box.verdict(run) != nil {
		t.Errorf("err %v, facts taken %d", err, taken.Load())
	}
}

// What could not be taken is in the verdict, and the run is not plain green: it succeeded with findings, and the owner can ask again.
func TestAMachineThatWasNotLookedAtInFullSucceededWithFindings(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	before := factsOfAMachine(box)
	box.theFactsBefore(run, before)
	after := factsOfAMachine(box)
	after.DNS = false
	after.Unknown = []string{osverify.FactDNS}
	takesFacts(t, after)

	box.tick()
	doc := box.verdict(run)
	if doc == nil || len(doc.Findings) != 1 || doc.Findings[0].Code != osverify.CodeFactsIncomplete || doc.Findings[0].Arg != "dns" || !slices.Equal(doc.NotJudged, []string{"dns"}) {
		t.Fatalf("the verdict is %+v", doc)
	}
	status := box.GetSystemOSUpgradeStatus()
	if status.State != osrun.StateSucceededWithFindings || !slices.Equal(status.NotJudged, []string{"dns"}) || !slices.Equal(status.Actions, []string{osrun.ActionResume, osrun.ActionAcknowledge}) {
		t.Errorf("the status is %q, not judged %v, actions %v", status.State, status.NotJudged, status.Actions)
	}
	// the owner asks again, and the machine answers this time
	if _, err := box.ResumeSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	if box.verdict(run) != nil {
		t.Error("the verdict is still there")
	}
	takesFacts(t, before)
	box.tick()
	if state := box.state(run); state.Name != osrun.StateSucceeded {
		t.Errorf("the run is %+v", state)
	}
}

// Containers that have a restart policy and are not up are waited for: Docker brings them back some time after it is itself up.
func TestContainersThatHaveARestartPolicyAreWaitedForBeforeTheyAreSaidNotToHaveReturned(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	before := factsOfAMachine(box)
	before.Docker = osverify.Docker{State: osverify.DockerUp, Version: "24.0.7", Containers: []osverify.Container{{Name: "photos", Policy: "always"}}}
	box.theFactsBefore(run, before)
	if err := os.WriteFile(filepath.Join(box.backupOf(run), "containers.txt"), []byte("/photos policy=always state=running\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	up := factsOfAMachine(box)
	up.Docker = osverify.Docker{State: osverify.DockerUp, Version: "24.0.7"}
	taken := takesFacts(t, up)

	// a minute after the check ended: the container is waited for
	tick := box.tick()
	if len(tick.Done) != 0 || box.verdict(run) != nil || taken.Load() != 1 {
		t.Fatalf("the look did %+v, the verdict is %+v", tick, box.verdict(run))
	}
	if state := box.state(run); state.Name != osrun.StateVerifying {
		t.Errorf("the run is %+v", state)
	}
	// it comes back
	back := up
	back.Docker.Containers = []osverify.Container{{Name: "photos", Policy: "always"}}
	takesFacts(t, back)
	box.advance(time.Minute)
	box.tick()
	if doc := box.verdict(run); doc == nil || len(doc.Findings) != 0 {
		t.Fatalf("the verdict is %+v", doc)
	}
}

func TestAContainerThatDoesNotComeBackInTimeIsSaidSo(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	before := factsOfAMachine(box)
	before.Docker = osverify.Docker{State: osverify.DockerUp, Version: "24.0.7", Containers: []osverify.Container{{Name: "photos", Policy: "always"}}}
	box.theFactsBefore(run, before)
	if err := os.WriteFile(filepath.Join(box.backupOf(run), "containers.txt"), []byte("/photos policy=always state=running\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	up := factsOfAMachine(box)
	up.Docker = osverify.Docker{State: osverify.DockerUp, Version: "24.0.7"}
	takesFacts(t, up)

	box.advance(osUpgradeTiming.ContainersWait)
	box.tick()
	doc := box.verdict(run)
	if doc == nil || len(doc.Findings) != 1 || doc.Findings[0].Code != osverify.CodeContainerNotReturned || doc.Findings[0].Arg != "photos" {
		t.Fatalf("the verdict is %+v", doc)
	}
	if state := box.state(run); state.Name != osrun.StateSucceededWithFindings {
		t.Errorf("the run is %+v", state)
	}
	// the owner asks for the check again, and the container is up
	if _, err := box.ResumeSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	back := up
	back.Docker.Containers = []osverify.Container{{Name: "photos", Policy: "always"}}
	takesFacts(t, back)
	box.tick()
	if state := box.state(run); state.Name != osrun.StateSucceeded {
		t.Errorf("the run is %+v", state)
	}
}

// What the owner stopped between the hop and the restart is not what the restart did: the facts are taken when the restart is asked for, and the judge tells the difference.
func TestTheFactsAreTakenWhenTheRestartIsAskedForAndTheJudgeTellsWhatTheOwnerStopped(t *testing.T) {
	box := newUpgradeBox(t)
	box.greenBarrier()
	run := box.aHopAtItsRestart()
	box.own(run, osrun.StateAwaitingReboot)
	box.tick()
	before := factsOfAMachine(box)
	before.Docker = osverify.Docker{State: osverify.DockerUp, Version: "24.0.7", Containers: []osverify.Container{{Name: "photos", Policy: "always"}, {Name: "files", Policy: "always"}}}
	box.theFactsBefore(run, before)
	if err := os.WriteFile(filepath.Join(box.backupOf(run), "containers.txt"), []byte("/photos policy=always state=running\n/files policy=always state=running\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// the owner stopped one of them in the days that the machine waited
	atTheRestart := factsOfAMachine(box)
	atTheRestart.Docker = osverify.Docker{State: osverify.DockerUp, Version: "24.0.7", Containers: []osverify.Container{{Name: "files", Policy: "always"}}}
	takesFacts(t, atTheRestart)
	box.aMachineThatRestarts(run, nil)
	if _, err := box.RebootSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	var got osverify.Facts
	if err := readJSON(filepath.Join(run.Dir, osUpgradeRestartFactsFile), &got); err != nil || !got.Taken || len(got.Docker.Containers) != 1 || got.Docker.Containers[0].Name != "files" {
		t.Fatalf("the facts at the restart are %+v, %v", got, err)
	}

	// the machine comes back, with the one that ran at the restart and not the other
	box.setBoot(boxBoot2)
	box.advance(time.Minute)
	run = box.read(run.ID)
	run = box.attempt(run, 2, osrun.PhaseVerify, osrun.WhoAuto)
	run = box.block(run, 2, 1, osrun.BlockAfter, true)
	run = box.leave(run, "done-after")
	run = box.say(run, osrun.Marker{Kind: osrun.MkVerifyDone, N: 2, Boot: boxBoot2})
	box.end(run, 2, "ok")
	box.setUnit("inactive")
	box.advance(osUpgradeTiming.ContainersWait)
	takesFacts(t, atTheRestart)
	box.tick()
	doc := box.verdict(run)
	if doc == nil || len(doc.Findings) != 1 || doc.Findings[0].Code != osverify.CodeChangedSinceSnapshot || doc.Findings[0].Arg != "container:photos" || doc.Findings[0].Severity != osverify.SeverityInfo {
		t.Fatalf("the verdict is %+v", doc)
	}
	if state := box.state(run); state.Name != osrun.StateSucceeded {
		t.Errorf("the run is %+v: what the owner stopped is information", state)
	}
}

// A file of the backup folder that is there and cannot be read is said: it is not a list that was not written.
func TestAFileOfTheBackupFolderThatCannotBeReadIsSaidInTheVerdict(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	box.theFactsBefore(run, factsOfAMachine(box))
	takesFacts(t, factsOfAMachine(box))
	path := filepath.Join(box.backupOf(run), "listening.txt")
	if err := os.WriteFile(path, make([]byte, 2<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	box.tick()
	doc := box.verdict(run)
	if doc == nil || len(doc.Findings) != 1 || doc.Findings[0].Code != osverify.CodeFactsIncomplete || doc.Findings[0].Arg != "bk:listening.txt" {
		t.Fatalf("the verdict is %+v", doc)
	}
}
