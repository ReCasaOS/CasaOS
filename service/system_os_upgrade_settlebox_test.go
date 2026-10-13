package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// The helpers that the tests of the settling and of the owner's buttons tell a run's story with: what the core and the unit write, in the words of the log, at the time of the
// box's clock.

// attempt is the core's ATTEMPT for a phase and the unit's UNIT_START, in the boot that the box is in.
func (b *upgradeBox) attempt(run *osUpgradeRun, n int, phase, who string) *osUpgradeRun {
	b.t.Helper()
	b.mu.Lock()
	boot := b.boot
	b.mu.Unlock()
	return b.say(run,
		osrun.Marker{Kind: osrun.MkAttempt, N: n, Phase: phase, Who: who},
		osrun.Marker{Kind: osrun.MkUnitStart, N: n, Phase: phase, Boot: boot, PID: 100 + n})
}

// block is a block of the attempt that began, and ended when done is true.
func (b *upgradeBox) block(run *osUpgradeRun, n, index int, id string, done bool) *osUpgradeRun {
	b.t.Helper()
	markers := []osrun.Marker{{Kind: osrun.MkBlockStart, N: n, Index: index, ID: id}}
	if done {
		markers = append(markers, osrun.Marker{Kind: osrun.MkBlockDone, N: n, Index: index, ID: id})
	}
	return b.say(run, markers...)
}

func (b *upgradeBox) end(run *osUpgradeRun, n int, result string) *osUpgradeRun {
	b.t.Helper()
	return b.say(run, osrun.Marker{Kind: osrun.MkUnitEnd, N: n, Result: result})
}

// stop is a STOP of the unit in a block, with the class and whether the point of no return was behind it.
func (b *upgradeBox) stop(run *osUpgradeRun, n, index int, id, class string, pnr bool) *osUpgradeRun {
	b.t.Helper()
	return b.say(run, osrun.Marker{Kind: osrun.MkStop, N: n, Index: index, ID: id, RC: 1, Class: class, PNR: pnr})
}

// aHopThatWasLost is a hop whose first attempt began, got as far as block 3 and was lost: the unit is gone, nothing ended it, and a minute has gone by. Nothing has been
// changed that cannot be put back: the point of no return is not behind it.
func (b *upgradeBox) aHopThatWasLost() *osUpgradeRun {
	b.t.Helper()
	run := b.aRun(b.anUpgradePlan("ok", nil), osrun.RunHop)
	run = b.attempt(run, 1, osrun.PhaseRun, osrun.WhoOwner)
	run = b.block(run, 1, 4, osrun.BlockSources, false)
	b.setUnit("inactive")
	b.advance(time.Minute)
	return b.read(run.ID)
}

// aHopAtItsRestart is a hop that did everything up to the restart, which the machine waits for: the blocks left their files, the unit said so and ended.
func (b *upgradeBox) aHopAtItsRestart() *osUpgradeRun {
	b.t.Helper()
	run := b.aRun(b.anUpgradePlan("ok", nil), osrun.RunHop)
	run = b.attempt(run, 1, osrun.PhaseRun, osrun.WhoOwner)
	run = b.leave(run, "begun-upgrade", "done-minimal", "done-full", "done-prereboot")
	if err := os.WriteFile(filepath.Join(b.backupOf(run), "boot_id"), []byte(boxBoot1+"\n"), 0o600); err != nil {
		b.t.Fatal(err)
	}
	run = b.say(run, osrun.Marker{Kind: osrun.MkRebootPending, N: 1, Boot: boxBoot1, Kernel: "6.1.0-18-amd64"})
	run = b.end(run, 1, "ok")
	b.setUnit("inactive")
	b.advance(time.Minute)
	return b.read(run.ID)
}

// aHopThatRestarted is a hop at its restart, after it: the machine is in another boot.
func (b *upgradeBox) aHopThatRestarted() *osUpgradeRun {
	b.t.Helper()
	run := b.aHopAtItsRestart()
	b.setBoot(boxBoot2)
	b.advance(time.Minute)
	return b.read(run.ID)
}

// aHopThatStoppedHalfWay is a hop that stopped in the full upgrade with the class that it is given, after its point of no return.
func (b *upgradeBox) aHopThatStoppedHalfWay(class string) *osUpgradeRun {
	b.t.Helper()
	run := b.aRun(b.anUpgradePlan("ok", nil), osrun.RunHop)
	run = b.attempt(run, 1, osrun.PhaseRun, osrun.WhoOwner)
	run = b.leave(run, "begun-upgrade", "done-minimal")
	run = b.block(run, 1, 7, osrun.BlockFull, false)
	run = b.stop(run, 1, 7, osrun.BlockFull, class, true)
	run = b.end(run, 1, "stopped")
	b.setUnit("inactive")
	b.advance(time.Minute)
	return b.read(run.ID)
}

// aRehearsal is a rehearsal that reached its end: the sources of the next release are in place, and the machine waits to be put back.
func (b *upgradeBox) aRehearsal() *osUpgradeRun {
	b.t.Helper()
	run := b.aRun(b.anUpgradePlan("ok", nil), osrun.RunRehearsal)
	run = b.attempt(run, 1, osrun.PhaseRehearse, osrun.WhoOwner)
	run = b.leave(run, "apt-orig", "done-timers", "done-download")
	run = b.say(run, osrun.Marker{Kind: osrun.MkRehearsalReady, N: 1})
	run = b.end(run, 1, "ok")
	b.setUnit("inactive")
	return b.read(run.ID)
}

// aRefresh is a refresh that ended: the old release is as new as it gets, and the machine does not need a restart for it.
func (b *upgradeBox) aRefresh() *osUpgradeRun { return b.aRefreshWith("no", "none") }

// aRefreshWith is a refresh that ended and says whether the machine needs a restart, and why.
func (b *upgradeBox) aRefreshWith(restart, why string) *osUpgradeRun {
	b.t.Helper()
	runID := "5a1d9e44-2b6f-4a8e-8d35-1e0f6a9b3c28"
	var blocks []osplan.AutoBlock
	for _, id := range osUpgradeRefreshBlock {
		blocks = append(blocks, osplan.AutoBlock{ID: id, Title: id, Text: "# casaos-os-upgrade " + id + " " + runID + "\n"})
	}
	forward := osplan.AutoBlock{ID: osrun.BlockForward, Title: "forward", Text: "# casaos-os-upgrade forward " + runID + "\n"}
	spec := osUpgradeRunSpec{
		Meta: osUpgradeRunMeta{
			RunID: runID, Kind: osrun.RunRefresh, Hop: "debian-11-12",
			BK: osUpgradeUnitBackups + "/casaos-os-upgrade-refresh-" + runID, CreatedAt: b.now(), Blocks: osUpgradeBlockInfos(blocks, forward),
		},
		Driver: osrun.Driver, Blocks: blocks, Forward: forward, Plan: []byte("{}"), Manual: "", Facts: osverify.Facts{Taken: true, At: b.now()},
	}
	if err := writeOSUpgradeRun(spec); err != nil {
		b.t.Fatal(err)
	}
	nonce := "0123456789abcdef0123456789abcdef"
	dir := osUpgradeRunFolder(runID)
	if err := createOSUpgradeMarkers(dir, nonce, b.now()); err != nil {
		b.t.Fatal(err)
	}
	if err := appendOSUpgradeMarker(dir, nonce, osrun.Marker{Kind: osrun.MkRun, RunID: runID, Hop: "debian-11-12", RunKind: osrun.RunRefresh, Boot: boxBoot1}); err != nil {
		b.t.Fatal(err)
	}
	run := b.read(runID)
	run = b.attempt(run, 1, osrun.PhaseRefresh, osrun.WhoOwner)
	b.advance(time.Minute)
	run = b.say(run, osrun.Marker{Kind: osrun.MkRefreshDone, N: 1, Result: restart, Why: why})
	run = b.end(run, 1, "ok")
	b.setUnit("inactive")
	return b.read(run.ID)
}

// actuallyBusy says whether the flag of the open upgrade is there.
func (b *upgradeBox) flagIsThere() bool {
	b.t.Helper()
	_, err := os.Lstat(osUpgradeBusyFile)
	if err != nil && !os.IsNotExist(err) {
		b.t.Fatal(err)
	}
	return err == nil
}

func (b *upgradeBox) point(run *osUpgradeRun) {
	b.t.Helper()
	if err := setOSUpgradeCurrent(run.ID); err != nil {
		b.t.Fatal(err)
	}
}

// own writes the pointer to the run and the flag as a start leaves them.
func (b *upgradeBox) own(run *osUpgradeRun, state string) *osUpgradeRun {
	b.t.Helper()
	b.point(run)
	if err := setOSUpgradeBusy(run.ID, state, b.now()); err != nil {
		b.t.Fatal(err)
	}
	return run
}

// tick is one look of the core at the upgrade.
func (b *upgradeBox) tick() osUpgradeTick {
	b.t.Helper()
	return b.osUpgradeSettleOnce(context.Background())
}

// attemptsOf are the ATTEMPT lines of a run.
func (b *upgradeBox) attemptsOf(run *osUpgradeRun) []osrun.Marker {
	return markersOfKind(b.read(run.ID), osrun.MkAttempt)
}

func markersOfKind(run *osUpgradeRun, kind string) []osrun.Marker {
	var out []osrun.Marker
	for _, m := range run.Markers {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

// theFactsBefore are what the start took of the machine, put in the run's folder in the place of what the box wrote.
func (b *upgradeBox) theFactsBefore(run *osUpgradeRun, facts osverify.Facts) {
	b.t.Helper()
	data, err := json.Marshal(facts)
	if err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.Dir, osUpgradeFactsFile), data, 0o600); err != nil {
		b.t.Fatal(err)
	}
}

// withTiming runs a test with the settling's numbers changed.
func withTiming(t *testing.T, change func(*osrun.Timing)) {
	t.Helper()
	was := osUpgradeTiming
	t.Cleanup(func() { osUpgradeTiming = was })
	change(&osUpgradeTiming)
}

// pruneDone says whether a look has been made: the first of them looks after the folders of the old runs.
func (e *osUpgradeEngine) pruneDone() bool {
	e.tickMu.Lock()
	defer e.tickMu.Unlock()
	return e.pruned
}
