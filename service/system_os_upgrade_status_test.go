package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// pointTo makes the box say that the run is the latest one.
func (b *upgradeBox) pointTo(run *osUpgradeRun) {
	b.t.Helper()
	if err := setOSUpgradeCurrent(run.ID); err != nil {
		b.t.Fatal(err)
	}
}

// aHopThatReachedItsRestart is a hop whose blocks all ended and whose unit said that the machine waits for its restart.
func (b *upgradeBox) aHopThatReachedItsRestart() *osUpgradeRun {
	b.t.Helper()
	run := b.aRun(b.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	run = b.say(run,
		osrun.Marker{Kind: osrun.MkAttempt, N: 1, Phase: osrun.PhaseRun, Who: osrun.WhoOwner},
		osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRun, Boot: boxBoot1, PID: 77},
		osrun.Marker{Kind: osrun.MkRebootPending, N: 1, Boot: boxBoot1, Kernel: "6.1.0-18-amd64"},
		osrun.Marker{Kind: osrun.MkUnitEnd, N: 1, Result: "ok"})
	run = b.leave(run, "begun-upgrade", "done-verify", "done-timers", "done-snapshot", "done-sources", "done-download", "done-minimal", "done-full", "done-prereboot")
	rewrite(b.t, filepath.Join(run.BK, "boot_id"), boxBoot1+"\n")
	b.pointTo(run)
	b.advance(time.Minute)
	return b.read(run.ID)
}

func TestWithNoRunTheStatusIsIdleAndSaysWhetherTheFeatureIsOn(t *testing.T) {
	box := newUpgradeBox(t)
	got := box.GetSystemOSUpgradeStatus()
	if got.BetaEnabled || got.State != osrun.StateIdle || got.Busy || got.RunID != "" || got.Attempt != 0 {
		t.Fatalf("the status is %+v", got)
	}
	if got.Actions == nil || got.LogTail == nil || got.Manual.BlocksDone == nil || got.Since != nil {
		t.Errorf("the lists are null, or a time is made up: %+v", got)
	}
	rewrite(t, osUpgradeBetaFile, "")
	if status := box.GetSystemOSUpgradeStatus(); !status.BetaEnabled || status.State != osrun.StateIdle {
		t.Errorf("with the switch on: %+v", status)
	}
}

// The names of the fields are the page's, and what is not there is not null.
func TestTheStatusHasTheFieldsThatThePageReads(t *testing.T) {
	box := newUpgradeBox(t)
	read := func(status OSUpgradeStatus) map[string]any {
		data, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	keys := func(m map[string]any) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	idle := read(box.GetSystemOSUpgradeStatus())
	want := []string{"actions", "attempt", "beta_enabled", "boot_unknown", "busy", "half_upgraded", "hop", "ignored_markers", "last_activity_at", "log_tail", "manual", "pnr", "reversible", "since", "state"}
	if got := keys(idle); !reflect.DeepEqual(got, want) {
		t.Errorf("an idle status has the fields %v, want %v", got, want)
	}
	if idle["actions"].([]any) == nil || idle["log_tail"].([]any) == nil {
		t.Error("a list is null")
	}

	run := box.aHopThatReachedItsRestart()
	rewrite(t, filepath.Join(run.Dir, "barrier.json"), `{"ok":true,"checks":[]}`)
	full := keys(read(box.GetSystemOSUpgradeStatus()))
	for _, k := range []string{"run_id", "kind", "plan_id", "phase", "barrier", "after_reboot_hint", "manual", "actions"} {
		if !slices.Contains(full, k) {
			t.Errorf("an awaiting status lacks %s: %v", k, full)
		}
	}
	for _, k := range []string{"reason", "cause", "block", "failure", "findings", "not_returned", "ack_word", "expires_at", "reboot"} {
		if slices.Contains(full, k) {
			t.Errorf("an awaiting status has %s, which it has nothing to say with", k)
		}
	}
}

func TestARunThatRunsIsSaidWithItsBlock(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	box.pointTo(run)
	run = box.say(run,
		osrun.Marker{Kind: osrun.MkAttempt, N: 1, Phase: osrun.PhaseRun, Who: osrun.WhoOwner},
		osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRun, Boot: boxBoot1, PID: 77},
		osrun.Marker{Kind: osrun.MkBlockStart, N: 1, Index: 4, ID: osrun.BlockSources})
	box.setUnit("active")
	box.advance(10 * time.Second)
	rewrite(t, filepath.Join(run.Dir, "output.log"), "block four is at work\n")

	got := box.GetSystemOSUpgradeStatus()
	if got.State != osrun.StateRunning || !got.Busy || got.Phase != osrun.PhaseRun || got.Attempt != 1 || !got.Reversible || got.PNR {
		t.Fatalf("the status is %+v", got)
	}
	if got.Block == nil || got.Block.Index != 4 || got.Block.ID != "sources" || got.Block.Title == "" {
		t.Errorf("the block is %+v: its title is the text's own", got.Block)
	}
	if got.RunID != run.ID || got.Kind != osrun.RunHop || got.Hop != "debian-11-12" || got.PlanID != run.Meta.PlanID {
		t.Errorf("the run is %q %q %q %q", got.RunID, got.Kind, got.Hop, got.PlanID)
	}
	if !reflect.DeepEqual(got.Actions, []string{osrun.ActionAbort}) {
		t.Errorf("the buttons are %v: a run that is before its point of no return can be called off", got.Actions)
	}
	if got.Since == nil || got.LastActivityAt == nil || got.LastActivityAt.Before(*got.Since) {
		t.Errorf("since %v, last activity %v", got.Since, got.LastActivityAt)
	}
	if !reflect.DeepEqual(got.LogTail, []string{"block four is at work"}) || got.AckWord != "" || got.Failure != nil || got.IgnoredMarkers != 0 {
		t.Errorf("tail %v, word %q, failure %+v", got.LogTail, got.AckWord, got.Failure)
	}
	// past the first blocks the owner cannot call it off, and has no word to say
	box.leave(run, "begun-upgrade")
	if got := box.GetSystemOSUpgradeStatus(); len(got.Actions) != 0 || !got.PNR || got.Reversible {
		t.Errorf("after the point of no return: %v, pnr %v", got.Actions, got.PNR)
	}
}

func TestAStopAfterThePointOfNoReturnSaysWhatStoppedAndWhatTheOwnerCanDo(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	box.pointTo(run)
	run = box.say(run,
		osrun.Marker{Kind: osrun.MkAttempt, N: 1, Phase: osrun.PhaseRun, Who: osrun.WhoOwner},
		osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRun, Boot: boxBoot1, PID: 77},
		osrun.Marker{Kind: osrun.MkBlockStart, N: 1, Index: 7, ID: osrun.BlockFull},
		osrun.Marker{Kind: osrun.MkStop, N: 1, Index: 7, ID: osrun.BlockFull, RC: 1, Class: osrun.ClassNetwork, PNR: true},
		osrun.Marker{Kind: osrun.MkUnitEnd, N: 1, Result: "stopped"})
	box.leave(run, "begun-upgrade", "done-minimal")
	rewrite(t, filepath.Join(run.Dir, "output.log"), strings.Join([]string{
		"Get:1 http://user:secret@deb.example.org/debian bookworm InRelease",
		"STOP: the download of the full upgrade did not go through: see http://user:secret@deb.example.org/debian/x?token=abc",
		"",
	}, "\n"))
	box.advance(time.Minute)

	got := box.GetSystemOSUpgradeStatus()
	if got.State != osrun.StateStoppedAtWaypoint || !got.Busy || !got.PNR || !got.HalfUpgraded {
		t.Fatalf("the status is %+v", got)
	}
	if got.Failure == nil || got.Failure.Stage != "full" || got.Failure.Class != osrun.ClassNetwork || got.Failure.RC != 1 || got.Failure.Hint != "" {
		t.Fatalf("the failure is %+v", got.Failure)
	}
	if strings.Contains(got.Failure.DetailLine, "secret") || strings.Contains(got.Failure.DetailLine, "token") || !strings.HasPrefix(got.Failure.DetailLine, "STOP: the download of the full upgrade") {
		t.Errorf("the line is %q: the address of the box's own source is said without its credentials", got.Failure.DetailLine)
	}
	for _, line := range got.LogTail {
		if strings.Contains(line, "secret") || strings.Contains(line, "token") {
			t.Errorf("the tail says %q", line)
		}
	}
	if !reflect.DeepEqual(got.Actions, []string{osrun.ActionResume, osrun.ActionAcknowledge, osrun.ActionTakeover}) || got.AckWord != osrun.AckWord {
		t.Errorf("the buttons are %v and the word %q: a machine that is half upgraded is closed by a word", got.Actions, got.AckWord)
	}
	if got.Manual.PNR != true || got.Manual.BKPath != "/var/backups/casaos-os-upgrade-"+run.Meta.PlanID || got.Manual.NextBlock != "verify" {
		t.Errorf("the manual road is %+v", got.Manual)
	}
}

func TestAStopThatAptSaysItsRepositoryChangedCarriesTheHint(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	box.pointTo(run)
	run = box.say(run,
		osrun.Marker{Kind: osrun.MkAttempt, N: 1, Phase: osrun.PhaseRun, Who: osrun.WhoOwner},
		osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRun, Boot: boxBoot1, PID: 77},
		osrun.Marker{Kind: osrun.MkBlockStart, N: 1, Index: 4, ID: osrun.BlockSources},
		osrun.Marker{Kind: osrun.MkStop, N: 1, Index: 4, ID: osrun.BlockSources, RC: 1, Class: osrun.ClassNetwork},
		osrun.Marker{Kind: osrun.MkUnwound, N: 1, Result: "ok"},
		osrun.Marker{Kind: osrun.MkUnitEnd, N: 1, Result: "stopped"})
	rewrite(t, filepath.Join(run.Dir, "output.log"), "E: Repository 'http://deb.example.org/debian bookworm InRelease' changed its 'Suite' value from 'stable' to 'oldstable'\nSTOP: apt-get update does not go through\n")
	box.advance(time.Minute)
	got := box.GetSystemOSUpgradeStatus()
	if got.State != osrun.StateFailed || got.Failure == nil || got.Failure.Class != osrun.ClassNetwork {
		t.Fatalf("the status is %+v, %+v", got, got.Failure)
	}
	if got.Failure.Hint != "" {
		t.Errorf("the hint is %q: the line that says it is not the STOP line", got.Failure.Hint)
	}
	rewrite(t, filepath.Join(run.Dir, "output.log"), "STOP: E: Repository 'http://deb.example.org/debian bookworm InRelease' changed its 'Suite' value from 'stable' to 'oldstable'\n")
	if got := box.GetSystemOSUpgradeStatus(); got.Failure == nil || got.Failure.Hint != osrun.HintReleaseInfoChange {
		t.Errorf("the failure is %+v", got.Failure)
	}
}

func TestAFailedRunThatCanBeDroppedSaysSoOnlyWhenThereIsAPlanToDrop(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	box.pointTo(run)
	box.say(run,
		osrun.Marker{Kind: osrun.MkAttempt, N: 1, Phase: osrun.PhaseRun, Who: osrun.WhoOwner},
		osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRun, Boot: boxBoot1, PID: 77},
		osrun.Marker{Kind: osrun.MkStop, N: 1, Index: 1, ID: osrun.BlockVerify, RC: 1, Class: osrun.ClassBusy},
		osrun.Marker{Kind: osrun.MkUnwound, N: 1, Result: "ok"},
		osrun.Marker{Kind: osrun.MkUnitEnd, N: 1, Result: "stopped"})
	box.advance(time.Minute)

	got := box.GetSystemOSUpgradeStatus()
	if got.State != osrun.StateFailed || got.Busy || !got.Reversible || got.Reason != osrun.ClassBusy {
		t.Fatalf("the status is %+v", got)
	}
	if !reflect.DeepEqual(got.Actions, []string{osrun.ActionAcknowledge, osrun.ActionDiscard}) {
		t.Errorf("with a live plan the buttons are %v", got.Actions)
	}
	box.osPlan.state.Store(&osPlanState{state: osPlanIdle})
	if got := box.GetSystemOSUpgradeStatus(); !reflect.DeepEqual(got.Actions, []string{osrun.ActionAcknowledge}) {
		t.Errorf("with no plan the buttons are %v: there is none to drop", got.Actions)
	}
	box.advance(osplan.PlanLifetime + time.Hour)
	box.anUpgradePlan(osplan.VerdictOK, nil)
	box.advance(osplan.PlanLifetime + time.Hour)
	if got := box.GetSystemOSUpgradeStatus(); slices.Contains(got.Actions, osrun.ActionDiscard) {
		t.Errorf("with a plan that expired the buttons are %v", got.Actions)
	}
}

func TestAMachineThatWaitsForItsRestartCanBeRestartedWhenTheBarrierIsGreen(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatReachedItsRestart()

	got := box.GetSystemOSUpgradeStatus()
	if got.State != osrun.StateAwaitingReboot || !got.Busy || !got.PNR || got.HalfUpgraded {
		t.Fatalf("the status is %+v", got)
	}
	if slices.Contains(got.Actions, osrun.ActionReboot) || got.Barrier != nil {
		t.Errorf("the buttons are %v and the barrier %+v before the checks have been made", got.Actions, got.Barrier)
	}
	rewrite(t, filepath.Join(run.Dir, "barrier.json"), `{"ok":false,"checks":[{"code":"fstab_unresolved","ok":false,"severity":"refuse"},{"code":"fstab_network","ok":true,"severity":"ok"}]}`)
	got = box.GetSystemOSUpgradeStatus()
	if slices.Contains(got.Actions, osrun.ActionReboot) || got.Barrier == nil || got.Barrier.OK || len(got.Barrier.Checks) != 2 || got.Barrier.Checks[0].Code != "fstab_unresolved" {
		t.Errorf("with a red barrier the buttons are %v and the barrier %+v", got.Actions, got.Barrier)
	}
	rewrite(t, filepath.Join(run.Dir, "barrier.json"), `{"ok":true,"checks":[{"code":"fstab_network","ok":true,"severity":"ok"}]}`)
	got = box.GetSystemOSUpgradeStatus()
	if !reflect.DeepEqual(got.Actions, []string{osrun.ActionReboot, osrun.ActionTakeover}) || got.Barrier == nil || !got.Barrier.OK {
		t.Errorf("with a green barrier the buttons are %v and the barrier %+v", got.Actions, got.Barrier)
	}
	if got.Reboot != nil {
		t.Errorf("a restart that nobody asked for: %+v", got.Reboot)
	}

	// what the owner keeps for the machine that does not come back
	hint := got.AfterRebootHint
	if hint == nil || hint.BKPath != run.Meta.BK || hint.Log != "/var/lib/casaos/os-upgrade/"+run.ID+"/output.log" || len(hint.Commands) == 0 {
		t.Fatalf("the hint is %+v", hint)
	}
	manual, err := osplan.BuildRunbook(*box.anUpgradePlan(osplan.VerdictOK, nil))
	if err != nil {
		t.Fatal(err)
	}
	if hint.Block8 == "" || !strings.Contains(manual.Text, hint.Block8) || !strings.Contains(hint.Block8, "done-after") || strings.Contains(hint.Block8, "BLOCK 7") {
		t.Errorf("the text of block 8 is %q", cutText(hint.Block8, 200))
	}
}

func TestARestartThatWasAskedForIsSaidAndStalls(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatReachedItsRestart()
	rewrite(t, filepath.Join(run.Dir, "barrier.json"), `{"ok":true,"checks":[]}`)
	asked := box.now()
	box.say(run, osrun.Marker{Kind: osrun.MkRebootRequested})
	box.advance(2 * time.Minute)
	got := box.GetSystemOSUpgradeStatus()
	if got.State != osrun.StateRebooting || got.Reboot == nil || !got.Reboot.RequestedAt.Equal(asked) || got.Reboot.Stalled || len(got.Actions) != 0 || got.AfterRebootHint == nil {
		t.Fatalf("the status is %+v, reboot %+v", got, got.Reboot)
	}
	box.advance(20 * time.Minute)
	got = box.GetSystemOSUpgradeStatus()
	if got.State != osrun.StateRebootStalled || got.Reboot == nil || !got.Reboot.Stalled || !slices.Contains(got.Actions, osrun.ActionReboot) {
		t.Fatalf("a restart that did not happen: %+v, reboot %+v", got, got.Reboot)
	}
	// a restart that failed is taken back
	box.say(run, osrun.Marker{Kind: osrun.MkRebootFailed})
	got = box.GetSystemOSUpgradeStatus()
	if got.State != osrun.StateAwaitingReboot || got.Reboot != nil {
		t.Errorf("a restart that failed: %+v", got)
	}
}

func TestAMachineThatCameBackIsToBeCheckedAndThenJudged(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatReachedItsRestart()
	box.setBoot(boxBoot2)
	got := box.GetSystemOSUpgradeStatus()
	if got.State != osrun.StateResuming || !got.Busy || got.AfterRebootHint != nil || got.Barrier != nil {
		t.Fatalf("a machine that restarted: %+v", got)
	}
	run = box.say(run,
		osrun.Marker{Kind: osrun.MkAttempt, N: 2, Phase: osrun.PhaseVerify, Who: osrun.WhoAuto},
		osrun.Marker{Kind: osrun.MkUnitStart, N: 2, Phase: osrun.PhaseVerify, Boot: boxBoot2, PID: 12},
		osrun.Marker{Kind: osrun.MkVerifyDone, N: 2, Boot: boxBoot2},
		osrun.Marker{Kind: osrun.MkUnitEnd, N: 2, Result: "ok"})
	box.leave(run, "done-after")
	if got := box.GetSystemOSUpgradeStatus(); got.State != osrun.StateVerifying || len(got.Findings) != 0 {
		t.Fatalf("before the judge: %+v", got)
	}

	rewrite(t, filepath.Join(run.Dir, "verify.json"), `{"findings":[
		{"code":"docker_source_paused","severity":"info"},
		{"code":"docker_version_changed","severity":"warn","arg":"28.0.4>29.1.0"},
		{"code":"container_not_returned","severity":"warn","arg":"db","policy":"always"},
		{"code":"mounts_missing","severity":"error","arg":"/mnt/pool","apps":["photos","files"]}]}`)
	got = box.GetSystemOSUpgradeStatus()
	if got.State != osrun.StateFailed || got.Reason != osrun.ReasonVerify || !got.Busy {
		t.Fatalf("with an error: %+v", got)
	}
	if len(got.Findings) != 4 || got.Findings[1].Arg != "28.0.4>29.1.0" || !reflect.DeepEqual(got.Findings[3].Apps, []string{"photos", "files"}) {
		t.Errorf("the findings are %+v", got.Findings)
	}
	if !reflect.DeepEqual(got.NotReturned, []OSUpgradeNotReturned{{Container: "db", Policy: "always"}}) {
		t.Errorf("not returned: %+v", got.NotReturned)
	}

	// information never changes the state of a run: a Docker box normally keeps its source paused
	rewrite(t, filepath.Join(run.Dir, "verify.json"), `{"findings":[{"code":"docker_source_paused","severity":"info"}]}`)
	if got := box.GetSystemOSUpgradeStatus(); got.State != osrun.StateSucceeded || got.Busy || len(got.Findings) != 1 {
		t.Errorf("with only information: %+v", got)
	}
	rewrite(t, filepath.Join(run.Dir, "verify.json"), `{"findings":[{"code":"ports_missing","severity":"warn","arg":"445"}]}`)
	if got := box.GetSystemOSUpgradeStatus(); got.State != osrun.StateSucceededWithFindings || got.Busy || !reflect.DeepEqual(got.Actions, []string{osrun.ActionResume, osrun.ActionAcknowledge}) {
		t.Errorf("with a warning: %+v", got)
	}
}

func TestTheTailOfTheOutputIsBoundedCutToWholeLinesAndSanitized(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	box.pointTo(run)
	log := filepath.Join(run.Dir, "output.log")

	// more than the tail holds: the end of it, with no line that was cut
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&b, "line %05d of the output of apt\n", i)
	}
	rewrite(t, log, b.String())
	got := box.GetSystemOSUpgradeStatus().LogTail
	if len(got) != 200 || got[199] != "line 19999 of the output of apt" || got[0] != "line 19800 of the output of apt" {
		t.Fatalf("the tail has %d lines, from %q to %q", len(got), got[0], got[len(got)-1])
	}

	// a file of a few very long lines is cut at the bound, and the first line of the cut is dropped
	rewrite(t, log, strings.Repeat("x", 300000)+"\nwhole line\n")
	got = box.GetSystemOSUpgradeStatus().LogTail
	if !reflect.DeepEqual(got, []string{"whole line"}) {
		t.Errorf("the tail of a file with a long line is %v", got)
	}

	// what apt prints: progress with carriage returns, control characters, the addresses of the box's own sources
	rewrite(t, log, "Reading package lists... 10%\rReading package lists... 100%\n\x1b[1mbold\x1b[0m \x00 \xe9\ndeb http://user:pw@host.example/debian?x=1 bookworm\n   \n")
	got = box.GetSystemOSUpgradeStatus().LogTail
	want := []string{"Reading package lists... 10%", "Reading package lists... 100%", "?[1mbold?[0m ? ?", "deb http://host.example bookworm"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the tail is %q, want %q", got, want)
	}

	// no output yet, and an output that is not a file
	os.Remove(log)
	if got := box.GetSystemOSUpgradeStatus().LogTail; got == nil || len(got) != 0 {
		t.Errorf("no output: %v", got)
	}
	if err := os.Mkdir(log, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := box.GetSystemOSUpgradeStatus().LogTail; len(got) != 0 {
		t.Errorf("an output that is a folder: %v", got)
	}
}

func TestTheStatusSaysWhereTheBlocksStandForAnOwnerWhoTakesOver(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	box.pointTo(run)
	got := box.GetSystemOSUpgradeStatus().Manual
	if len(got.BlocksDone) != 0 || got.NextBlock != "verify" || got.PNR || got.BKPath != run.Meta.BK {
		t.Errorf("before any block: %+v", got)
	}
	box.leave(run, "done-verify", "done-timers", "done-snapshot")
	got = box.GetSystemOSUpgradeStatus().Manual
	if !reflect.DeepEqual(got.BlocksDone, []string{"verify", "timers", "snapshot"}) || got.NextBlock != "sources" || got.PNR {
		t.Errorf("after three blocks: %+v", got)
	}
	box.leave(run, "done-sources", "done-download", "begun-upgrade", "done-minimal", "done-full", "done-prereboot", "done-after")
	got = box.GetSystemOSUpgradeStatus().Manual
	if len(got.BlocksDone) != 9 || got.NextBlock != "" || !got.PNR {
		t.Errorf("after all of them: %+v", got)
	}
}

// A run that cannot be read is not one that is not there: the machine may be half upgraded, and the owner is told to look.
func TestARunThatCannotBeReadIsBusyAndNeedsAttention(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	box.pointTo(run)
	remove(t, filepath.Join(run.Dir, "run.json"))
	got := box.GetSystemOSUpgradeStatus()
	if got.State != osrun.StateNeedsAttention || got.Reason != osrun.ReasonRunDamaged || !got.Busy || got.RunID != run.ID {
		t.Errorf("a run with no identity: %+v", got)
	}
	// there is nothing of it to take over: it can be let go of, with the word that is asked for a machine that may be half upgraded
	if !reflect.DeepEqual(got.Actions, []string{osrun.ActionAcknowledge}) || got.AckWord != osrun.AckWord {
		t.Errorf("the buttons are %v (word %q)", got.Actions, got.AckWord)
	}

	rewrite(t, filepath.Join(box.root, "current"), "the latest one\n")
	got = box.GetSystemOSUpgradeStatus()
	if got.State != osrun.StateNeedsAttention || !got.Busy || got.RunID != "" {
		t.Errorf("a pointer that says nothing: %+v", got)
	}

	// ... and a pointer to a run whose folder was removed
	if err := setOSUpgradeCurrent("ffffffff-6c1e-4d0a-9a53-7f1c2d4e8b19"); err != nil {
		t.Fatal(err)
	}
	if got := box.GetSystemOSUpgradeStatus(); got.State != osrun.StateNeedsAttention || !got.Busy || got.RunID != "ffffffff-6c1e-4d0a-9a53-7f1c2d4e8b19" {
		t.Errorf("a pointer to nothing: %+v", got)
	}
}

func TestARunWhoseFilesAreNotWhatWasWrittenIsDamagedInTheStatus(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	box.pointTo(run)
	appendTo(t, filepath.Join(run.Dir, "blocks", "sources.sh"), "echo hello\n")
	got := box.GetSystemOSUpgradeStatus()
	if got.State != osrun.StateNeedsAttention || got.Reason != osrun.ReasonRunDamaged || !got.Busy {
		t.Errorf("the status is %+v", got)
	}
	// markers of a unit that are not markers are counted, not read
	box = newUpgradeBox(t)
	run = box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	box.pointTo(run)
	appendTo(t, filepath.Join(run.Dir, "markers"), "CASAOS_OSU_TAKEOVER ffffffffffffffffffffffffffffffff 0 2026-10-13T10:00:00Z\napt says hello\n")
	if got := box.GetSystemOSUpgradeStatus(); got.IgnoredMarkers != 2 || got.State == osrun.StateManual {
		t.Errorf("a forged line was read: %+v", got)
	}
}

// The status of a run is asked every few seconds and waits for nothing: not for the lock that a launch holds.
func TestTheStatusDoesNotWaitForTheLockOfALaunch(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	box.pointTo(run)
	osUpgradeMu.Lock()
	done := make(chan OSUpgradeStatus, 1)
	go func() { done <- box.GetSystemOSUpgradeStatus() }()
	select {
	case got := <-done:
		osUpgradeMu.Unlock()
		if got.RunID != run.ID {
			t.Errorf("the status is %+v", got)
		}
	case <-time.After(5 * time.Second):
		osUpgradeMu.Unlock()
		t.Fatal("the status waits for the lock of a launch")
	}
}

func TestTheStatusAsksSystemdAboutTheUnitOfTheUpgradeAndNothingElse(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	box.pointTo(run)
	box.GetSystemOSUpgradeStatus()
	box.mu.Lock()
	defer box.mu.Unlock()
	if len(box.commands) != 1 || !strings.HasPrefix(box.commands[0], "systemctl show casaos-os-upgrade.service") {
		t.Errorf("the commands that the status ran are %q", box.commands)
	}
}
