package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

const boxBoot3 = "99999999-aaaa-4bbb-8ccc-dddddddddddd"

// ---- the timers of apt, at a boot that the run has not seen --------------------------------------------------------------------

func (b *upgradeBox) aptIsAtWork() {
	b.t.Helper()
	for _, unit := range osUpgradeAptUnits {
		b.setActive(unit, "active")
	}
}

// A boot that the run has not seen stops the units that change packages by themselves, while the run holds the timers.
func TestABootThatTheRunHasNotSeenStopsTheUnitsOfAptWhileTheRunHoldsTheTimers(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopAtItsRestart()
	box.own(run, osrun.StateAwaitingReboot)
	box.setBoot(boxBoot2)
	box.aptIsAtWork()
	box.tick()
	if stops := box.commandsWith("systemctl stop"); len(stops) != len(osUpgradeAptUnits) {
		t.Errorf("the units were stopped %q", stops)
	}
	if !osrun.BootKnown(box.read(run.ID).Markers, boxBoot2) {
		t.Error("the boot was not written down")
	}
}

// A check that failed and was left alone for days is not a run that holds the timers: the block that checks the machine put them back, and a boot a week later does not stop them
// again, for the owner to find automatic security updates off.
func TestAFailedCheckThatIsLeftAloneDoesNotStopTheTimersAtEveryBoot(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	if err := os.WriteFile(filepath.Join(box.backupOf(run), "kernel"), []byte("6.1.0-18-amd64\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	box.theFactsBefore(run, factsOfAMachine(box))
	takesFacts(t, osverify.Facts{Taken: true, At: box.now(), Kernel: "5.10.0-28-amd64", System: "running", DpkgQuiet: true})
	box.tick()
	if state := box.state(run); state.Name != osrun.StateFailed || state.Reason != osrun.ReasonVerify || !state.Busy {
		t.Fatalf("the run is %+v", state)
	}

	box.aptIsAtWork()
	box.advance(7 * 24 * time.Hour)
	box.setBoot(boxBoot3)
	box.tick()
	if stops := box.commandsWith("systemctl stop"); len(stops) != 0 {
		t.Errorf("the timers were stopped again at a boot a week later: %q", stops)
	}
	if !osrun.BootKnown(box.read(run.ID).Markers, boxBoot3) {
		t.Error("the boot was not written down")
	}
}

// ---- what the owner is told ---------------------------------------------------------------------------------------------------

func aFstabThatDoesNotResolve(m *aMachineForTheBarrier, good bool) {
	if good {
		m.files["/etc/fstab"] = "UUID=1234-ABCD /data ext4 defaults 0 2\n"
		return
	}
	m.files["/etc/fstab"] = "UUID=dead-beef /data ext4 defaults 0 2\n"
}

// A machine that waits for its restart and whose restart is not offered is not told to restart: the owner is told that something is to be mended, and told that it is ready when it is.
func TestTheOwnerIsNotToldToRestartWhileTheRestartIsNotOffered(t *testing.T) {
	box := newUpgradeBox(t)
	m := theBarrierSees(t)
	aFstabThatDoesNotResolve(m, false)
	voice := box.withAVoice()
	run := box.aHopAtItsRestart()
	box.own(run, osrun.StateAwaitingReboot)

	box.tick()
	if doc := box.barrierOf(run); doc == nil || doc.OK {
		t.Fatalf("the barrier is %+v", doc)
	}
	told := voice.told()
	if len(told) != 1 || !strings.Contains(told[0].key, ":awaiting_reboot_blocked:") || strings.Contains(told[0].sentence, "Restart the machine from the dashboard") {
		t.Fatalf("the owner was told %+v", told)
	}
	// the same look again says nothing more
	box.advance(time.Minute)
	box.tick()
	if got := voice.told(); len(got) != 1 {
		t.Errorf("told again: %+v", got)
	}
	// mended: the restart is offered, and it is said
	aFstabThatDoesNotResolve(m, true)
	box.advance(time.Minute)
	box.tick()
	got := voice.told()
	if len(got) != 2 || !strings.Contains(got[1].key, ":awaiting_reboot:") || !strings.Contains(got[1].sentence, "Restart the machine from the dashboard") {
		t.Errorf("the owner was told %+v", got)
	}
}

// A refresh that asks for a restart holds the machine as a hop does, and is told and spoken of again as one.
func TestARefreshThatWantsItsRestartIsToldAndSpokenOfAgain(t *testing.T) {
	box := newUpgradeBox(t)
	voice := box.withAVoice()
	run := box.aRefreshWith("yes", "kernel")
	box.own(run, osrun.StateRebootFirst)

	box.tick()
	told := voice.told()
	if len(told) != 1 || !strings.Contains(told[0].key, ":reboot_first:") || !strings.Contains(told[0].sentence, "restart") {
		t.Fatalf("the owner was told %+v", told)
	}
	box.advance(osUpgradeTiming.Reminders[0])
	box.tick()
	box.advance(osUpgradeTiming.Reminders[1])
	box.tick()
	got := voice.told()
	if len(got) != 3 || !strings.Contains(got[1].key, ":reminder:1") || !strings.Contains(got[2].key, ":reminder:2") {
		t.Errorf("the owner was told %+v", got)
	}
}

// ---- the unit and systemd -------------------------------------------------------------------------------------------------------

func TestTheCoreKeepsSinceWhenSystemdHasBeenSilent(t *testing.T) {
	box := newUpgradeBox(t)
	u := box.systemPackageUpdater()
	box.setUnit("")
	first := u.askOSUpgradeUnit()
	if first.Answered || !first.SilentSince.Equal(box.now()) {
		t.Fatalf("the first question: %+v", first)
	}
	box.advance(10 * time.Minute)
	if again := u.askOSUpgradeUnit(); again.Answered || !again.SilentSince.Equal(first.SilentSince) {
		t.Errorf("the second question: %+v, silent since %v", again, first.SilentSince)
	}
	box.setUnit("inactive")
	if answered := u.askOSUpgradeUnit(); !answered.Answered || !answered.SilentSince.IsZero() {
		t.Errorf("an answer: %+v", answered)
	}
	box.setUnit("")
	box.advance(time.Hour)
	if second := u.askOSUpgradeUnit(); !second.SilentSince.Equal(box.now()) {
		t.Errorf("a new run of silence began at %v, not %v", second.SilentSince, box.now())
	}
	// what requests share is the same question
	box.setUnit("")
	if shared := u.osUpgradeUnit(); shared.Answered {
		t.Errorf("%+v", shared)
	}
}

// A systemd that is silent for a moment is not one that has been for half an hour, however long the machine has waited for its restart.
func TestAMachineThatHasWaitedForDaysIsNotSilentBecauseSystemdMissedOneQuestion(t *testing.T) {
	box := newUpgradeBox(t)
	theBarrierSees(t)
	run := box.aHopAtItsRestart()
	box.own(run, osrun.StateAwaitingReboot)
	box.advance(72 * time.Hour)
	box.setUnit("")
	if status := box.GetSystemOSUpgradeStatus(); status.State != osrun.StateAwaitingReboot {
		t.Fatalf("one question that systemd did not answer made the run %q", status.State)
	}
	box.advance(29 * time.Minute)
	if status := box.GetSystemOSUpgradeStatus(); status.State != osrun.StateAwaitingReboot {
		t.Fatalf("29 minutes of silence made the run %q", status.State)
	}
	box.advance(2 * time.Minute)
	status := box.GetSystemOSUpgradeStatus()
	if status.State != osrun.StateNeedsAttention || status.Reason != osrun.ReasonSystemdSilent || len(status.Actions) != 1 || status.Actions[0] != osrun.ActionAcknowledge || status.AckWord == "" {
		t.Errorf("after half an hour the status is %q %q %v %q", status.State, status.Reason, status.Actions, status.AckWord)
	}
	// the owner lets go of it with the word, and nothing asks systemd
	for _, c := range []struct {
		word string
		code string
	}{{"", OSUpgradeWordRequired}, {"yes", OSUpgradeBadWord}} {
		_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckAcknowledge, Word: c.word})
		if refusal := refusalOf(t, err); refusal.Code != c.code {
			t.Errorf("word %q: refused with %q", c.word, refusal.Code)
		}
	}
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckManual}); refusalOf(t, err).Code != OSUpgradeNotTerminal {
		t.Errorf("a takeover is not what a silent systemd leaves: %v", err)
	}
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckAcknowledge, Word: osrun.AckWord}); err != nil {
		t.Fatal(err)
	}
	if _, current := currentOSUpgrade(); current != osUpgradeNoRun || box.flagIsThere() {
		t.Error("the run is not closed")
	}
}

// ---- a unit that runs and says nothing ---------------------------------------------------------------------------------------

func (b *upgradeBox) aHopInTheFullUpgrade() *osUpgradeRun {
	b.t.Helper()
	run := b.aRun(b.anUpgradePlan("ok", nil), osrun.RunHop)
	run = b.attempt(run, 1, osrun.PhaseRun, osrun.WhoOwner)
	run = b.leave(run, "begun-upgrade", "done-minimal")
	run = b.block(run, 1, 7, osrun.BlockFull, false)
	b.setUnit("active")
	b.own(run, osrun.StateRunning)
	return b.read(run.ID)
}

func TestAUnitThatRunsAndHasSaidNothingForALongTimeGetsAHintAndNoButton(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopInTheFullUpgrade()
	box.advance(osUpgradeTiming.HungQuiet - time.Minute)
	if status := box.GetSystemOSUpgradeStatus(); status.Hint != nil || status.State != osrun.StateRunning {
		t.Fatalf("a unit that is quiet for less than the time has a hint: %+v", status.Hint)
	}
	box.advance(2 * time.Minute)
	status := box.GetSystemOSUpgradeStatus()
	if status.Hint == nil || status.Hint.Code != "unit_quiet" || status.Hint.QuietSeconds < int(osUpgradeTiming.HungQuiet/time.Second) {
		t.Fatalf("the hint is %+v", status.Hint)
	}
	if got := strings.Join(status.Hint.Commands, "\n"); !strings.Contains(got, "systemctl stop casaos-os-upgrade.service") || !strings.Contains(got, osUpgradeUnitRoot+"/"+run.ID+"/output.log") {
		t.Errorf("the commands are %q", got)
	}
	if len(status.Actions) != 0 {
		t.Errorf("the hint has a button: %v", status.Actions)
	}
	// a unit that is gone has no hint, and one that has said something lately has none
	box.setUnit("inactive")
	if status := box.GetSystemOSUpgradeStatus(); status.Hint != nil {
		t.Errorf("a unit that is not there has a hint: %+v", status.Hint)
	}
	box.setUnit("active")
	box.say(box.read(run.ID), osrun.Marker{Kind: osrun.MkBlockStart, N: 1, Index: 7, ID: osrun.BlockFull})
	if status := box.GetSystemOSUpgradeStatus(); status.Hint != nil {
		t.Errorf("a unit that spoke a moment ago has a hint: %+v", status.Hint)
	}
}

// ---- the line of the unit's output that says why ------------------------------------------------------------------------------

func TestTheDetailLineOfAFailureToStartIsNotAnOldStop(t *testing.T) {
	box := newUpgradeBox(t)
	box.readyToResume()
	run := box.aHopThatStoppedHalfWay(osrun.ClassAptFailed)
	if err := os.WriteFile(filepath.Join(run.Dir, osUpgradeOutputFile), []byte("Reading package lists...\nSTOP: apt could not repair the packages\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	box.own(run, osrun.StateStoppedAtWaypoint)
	if status := box.GetSystemOSUpgradeStatus(); status.Failure == nil || status.Failure.DetailLine != "STOP: apt could not repair the packages" || status.Failure.Class != osrun.ClassAptFailed {
		t.Fatalf("the stop says %+v", status.Failure)
	}

	box.startErr = errors.New("Failed to connect to bus")
	if _, err := box.ResumeSystemOSUpgrade(); refusalOf(t, err).Code != OSUpgradeStartFailed {
		t.Fatalf("the resume was %v", err)
	}
	status := box.GetSystemOSUpgradeStatus()
	if status.State != osrun.StateFailed || status.Reason != osrun.ReasonStart || status.Failure == nil {
		t.Fatalf("the status is %q %q %+v", status.State, status.Reason, status.Failure)
	}
	if status.Failure.DetailLine != "" || status.Failure.Hint != "" || status.Failure.Class != osrun.ReasonStart {
		t.Errorf("the failure to start has the line of a stop before it: %+v", status.Failure)
	}
}

// ---- a refresh at the block that upgrades packages ---------------------------------------------------------------------------

func TestARefreshCannotBeCalledOffOnceItUpgradesPackages(t *testing.T) {
	for id, abortable := range map[string]bool{osrun.BlockRCheck: true, osrun.BlockRSources: true, osrun.BlockRUpgrade: false, osrun.BlockRDone: false} {
		box := newUpgradeBox(t)
		run := box.aRefreshAtWork()
		run = box.block(run, 1, 1, id, false)
		box.setUnit("active")
		box.own(run, osrun.StateRefreshing)
		status := box.GetSystemOSUpgradeStatus()
		if got := len(status.Actions) == 1 && status.Actions[0] == osrun.ActionAbort; got != abortable {
			t.Errorf("%s: the buttons are %v", id, status.Actions)
		}
		_, err := box.AbortSystemOSUpgrade()
		if abortable {
			if err != nil {
				t.Errorf("%s: %v", id, err)
			}
			continue
		}
		if refusal := refusalOf(t, err); refusal.Code != OSUpgradeNotAbortable || refusal.Data["reason"] != "past_block" {
			t.Errorf("%s: refused with %q %v", id, refusal.Code, refusal.Data)
		}
		if len(box.commandsWith("systemctl stop casaos-os-upgrade")) != 0 {
			t.Errorf("%s: the unit was stopped", id)
		}
	}
}

// A log that cannot place the run is told of by the run's own folder, and not as "no run": what is told of a run is kept by its id.
func TestARunThatTheLogCannotPlaceIsToldOfByItsOwnId(t *testing.T) {
	box := newUpgradeBox(t)
	voice := box.withAVoice()
	run := box.aRun(box.anUpgradePlan("ok", nil), osrun.RunHop)
	if err := os.WriteFile(filepath.Join(run.Dir, osUpgradeMarkersFile), []byte("what is not a log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	box.leave(run, "begun-upgrade")
	box.own(run, osrun.StateRunning)

	box.tick()
	told := voice.told()
	if len(told) != 1 || !strings.HasPrefix(told[0].key, run.ID+":needs_attention:") {
		t.Fatalf("the owner was told %+v", told)
	}
	if !box.flagIsThere() {
		t.Error("the flag of a machine whose packages are being changed was taken away")
	}
}

// A unit that could not put everything back leaves the folder with the copy of /etc/apt where it was, and the status says where it is and what to do at the console: the owner is
// not left with a state that has a word for it and no way to act.
func TestAnUnwindThatWasPartialSaysWhereTheCopyOfTheSourcesIsAndWhatToDo(t *testing.T) {
	box := newUpgradeBox(t)
	run := theRunsOfEveryState()["an unwind that was partial"](box)
	status := box.GetSystemOSUpgradeStatus()
	if status.State != osrun.StateNeedsAttention || status.Reason != osrun.ReasonUnwindPartial || status.Hint == nil || status.Hint.Code != "unwind_partial" {
		t.Fatalf("the status is %q %q with the hint %+v", status.State, status.Reason, status.Hint)
	}
	if status.Manual.BKPath != run.Meta.BK {
		t.Errorf("the status names %q as the folder, not %q", status.Manual.BKPath, run.Meta.BK)
	}
	joined := strings.Join(status.Hint.Commands, "\n")
	for _, want := range []string{"ls -la " + run.Meta.BK, "sh " + run.Meta.BK + "/restore-sources.sh", "systemctl start apt-daily.timer", run.ID + "/" + osUpgradeOutputFile} {
		if !strings.Contains(joined, want) {
			t.Errorf("the commands do not name %q:\n%s", want, joined)
		}
	}
	// the hint is for that state alone
	other := newUpgradeBox(t)
	other.aRunThatFailedBeforeItChangedAnything()
	if status := other.GetSystemOSUpgradeStatus(); status.Hint != nil {
		t.Errorf("a run that failed has the hint %+v", status.Hint)
	}
}
