package service

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// aVoice is what tells the owner: it records what it was told, and what the file that remembers it held when it was told.
type aVoice struct {
	mu       sync.Mutex
	notices  []notice
	notified string // the file that remembers what was told
}

type notice struct {
	key, sentence    string
	rememberedBefore bool
}

func (v *aVoice) say(key, sentence string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	data, _ := os.ReadFile(v.notified)
	v.notices = append(v.notices, notice{key: key, sentence: sentence, rememberedBefore: strings.Contains(string(data), key+"\n")})
}

func (v *aVoice) told() []notice {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]notice(nil), v.notices...)
}

func (b *upgradeBox) withAVoice() *aVoice {
	b.t.Helper()
	v := &aVoice{notified: filepath.Join(b.root, osUpgradeNotifiedFile)}
	b.SetOSUpgradeNotifier(v.say)
	return v
}

// ---- what is said of a state ---------------------------------------------------------------------------------------------------------

// The states that the owner is told of, and those that they are not: a unit that is doing what it should, a repair that is on its way, and a run that will be undone say nothing.
func TestTheOwnerIsToldOfTheStatesThatNeedThemAndOfNoOther(t *testing.T) {
	told := []osrun.State{
		{Name: osrun.StateAwaitingReboot}, {Name: osrun.StateRebootFirst}, {Name: osrun.StateRebootStalled}, {Name: osrun.StateStoppedAtWaypoint, Class: osrun.ClassBusy, PNR: true},
		{Name: osrun.StateInterrupted, PNR: true}, {Name: osrun.StateFailed, Reason: osrun.ReasonVerify, PNR: true}, {Name: osrun.StateFailed, Reason: osrun.ReasonHalf, PNR: true},
		{Name: osrun.StateFailed, Reason: osrun.ReasonStart, PNR: true}, {Name: osrun.StateFailed, Reason: osrun.ClassNetwork}, {Name: osrun.StateNeedsAttention, Reason: osrun.ReasonSystemdSilent},
		{Name: osrun.StateSucceeded}, {Name: osrun.StateSucceededWithFindings}, {Name: osrun.StateRehearsalDone},
	}
	for _, state := range told {
		state.RunID, state.Attempt = "0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19", 2
		key, sentence, ok := osUpgradeAlertFor(state)
		if !ok || sentence == "" || !strings.HasPrefix(key, state.RunID+":"+state.Name+":2") {
			t.Errorf("%s (%s): key %q, sentence %q, told %v", state.Name, state.Reason, key, sentence, ok)
		}
	}
	for _, name := range []string{
		osrun.StateIdle, osrun.StateRefreshing, osrun.StateRefreshed, osrun.StateRehearsing, osrun.StateRehearsed, osrun.StateRunning, osrun.StateUnwinding,
		osrun.StateRecovering, osrun.StateRebooting, osrun.StateResuming, osrun.StateVerifying, osrun.StateManual, osrun.StateManualDone,
	} {
		if key, sentence, ok := osUpgradeAlertFor(osrun.State{Name: name, RunID: "0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19", Attempt: 1}); ok {
			t.Errorf("%s: told %q (%q)", name, key, sentence)
		}
	}
	// a run that was lost before its point of no return is undone by the core, and said nothing of until it is a failure
	if _, _, ok := osUpgradeAlertFor(osrun.State{Name: osrun.StateInterrupted, PNR: false}); ok {
		t.Error("an interruption that will be undone was told")
	}
}

// The restart that waits says what it costs: the backups and the automatic updates stay paused while the upgrade is open.
func TestTheNoticeOfAMachineThatWaitsSaysWhatItCosts(t *testing.T) {
	_, sentence, _ := osUpgradeAlertFor(osrun.State{Name: osrun.StateAwaitingReboot, RunID: "0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19", Attempt: 1})
	for _, word := range []string{"backups", "automatic updates", "paused"} {
		if !strings.Contains(sentence, word) {
			t.Errorf("the notice %q does not say %q", sentence, word)
		}
	}
	if sentence := osUpgradeReminderSentence(); !strings.Contains(sentence, "backups") || !strings.Contains(sentence, "paused") {
		t.Errorf("the reminder %q does not say what it costs", sentence)
	}
}

// What is said holds no word of the owner's machine but the class of a stop: nothing that apt printed and nothing of the sources.
func TestTheNoticesHoldNothingOfTheMachine(t *testing.T) {
	for _, state := range []osrun.State{
		{Name: osrun.StateStoppedAtWaypoint, Class: osrun.ClassNetwork, PNR: true}, {Name: osrun.StateFailed, Reason: osrun.ClassDisk}, {Name: osrun.StateNeedsAttention, Reason: osrun.ReasonRunDamaged},
	} {
		_, sentence, ok := osUpgradeAlertFor(state)
		if !ok || strings.ContainsAny(sentence, "\n\r\t/") {
			t.Errorf("%+v: %q", state, sentence)
		}
	}
}

// ---- once ----------------------------------------------------------------------------------------------------------------------------

// The owner is told once that the machine waits for its restart: the next looks say nothing, and neither does a core that is started again, which knows from its file.
func TestTheOwnerIsToldOnceThatTheMachineWaits(t *testing.T) {
	box := newUpgradeBox(t)
	theBarrierSees(t)
	voice := box.withAVoice()
	run := box.aHopAtItsRestart()
	box.point(run)

	box.tick()
	box.advance(30 * time.Second)
	box.tick()
	got := voice.told()
	if len(got) != 1 || got[0].key != run.ID+":awaiting_reboot:1" || !strings.Contains(got[0].sentence, "restart") {
		t.Fatalf("the owner was told %+v", got)
	}

	// a core that was started again: its memory is gone, and its file is not
	again := &systemService{packageUpdates: box.updater}
	voice2 := &aVoice{notified: voice.notified}
	again.SetOSUpgradeNotifier(voice2.say)
	again.osUpgradeSettleOnce(t.Context())
	if got := voice2.told(); len(got) != 0 {
		t.Errorf("a core that was started again told %+v", got)
	}
}

// A run is told before it is spoken: the file that remembers has the key when the voice is asked, so that no start of the core, however badly timed, tells it twice.
func TestWhatIsToldIsRememberedBeforeItIsSaid(t *testing.T) {
	box := newUpgradeBox(t)
	voice := box.withAVoice()
	run := box.aHopThatSucceeded()
	box.point(run)
	box.tick()

	got := voice.told()
	if len(got) != 1 || !got[0].rememberedBefore {
		t.Errorf("the owner was told %+v: the key was not in the file when it was said", got)
	}
}

// A run that ended while the core was down is told by the first look of the core that is started: nothing is lost, and nothing is told twice.
func TestARunThatEndedWhileTheCoreWasDownIsTold(t *testing.T) {
	box := newUpgradeBox(t)
	voice := box.withAVoice()
	run := box.aHopThatSucceeded()
	box.point(run)
	if err := os.Remove(filepath.Join(box.root, osUpgradeNotifiedFile)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	box.tick()
	got := voice.told()
	if len(got) != 1 || !strings.HasPrefix(got[0].key, run.ID+":succeeded:") || !strings.Contains(got[0].sentence, "upgraded") {
		t.Errorf("the owner was told %+v", got)
	}
}

// A voice that is not there yet does not lose what was told: it is remembered all the same, as the owner who adds a channel next week is not told the news of last week.
func TestWhatIsToldWithNoVoiceIsRemembered(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatSucceeded()
	box.point(run)
	box.tick()
	if got := readText(t, filepath.Join(box.root, osUpgradeNotifiedFile)); !strings.Contains(got, run.ID+":succeeded:") {
		t.Errorf("the file that remembers holds %q", got)
	}
	voice := box.withAVoice()
	box.tick()
	if got := voice.told(); len(got) != 0 {
		t.Errorf("the owner was told of last week: %+v", got)
	}
}

// A file that cannot be written is told once for the life of the core, and not at every look.
func TestWhatCannotBeRememberedIsToldOnceForTheLifeOfTheCore(t *testing.T) {
	box := newUpgradeBox(t)
	voice := box.withAVoice()
	run := box.aHopThatSucceeded()
	box.point(run)
	if err := os.MkdirAll(filepath.Join(box.root, osUpgradeNotifiedFile), 0o700); err != nil {
		t.Fatal(err)
	}
	box.tick()
	box.advance(30 * time.Second)
	box.tick()
	if got := voice.told(); len(got) != 1 {
		t.Errorf("the owner was told %d times", len(got))
	}
}

// A stop that comes again after the owner resumed is told again: it is another stop.
func TestAStopThatComesAgainIsTold(t *testing.T) {
	box := newUpgradeBox(t)
	voice := box.withAVoice()
	run := box.aHopThatStoppedHalfWay(osrun.ClassNetwork)
	box.own(run, osrun.StateStoppedAtWaypoint)
	box.tick()

	run = box.attempt(box.read(run.ID), 2, osrun.PhaseRecover, osrun.WhoOwner)
	run = box.block(run, 2, 2, osrun.BlockFull, false)
	run = box.stop(run, 2, 2, osrun.BlockFull, osrun.ClassNetwork, true)
	box.end(run, 2, "stopped")
	box.setUnit("inactive")
	box.advance(time.Minute)
	box.tick()

	got := voice.told()
	if len(got) != 2 || got[0].key == got[1].key {
		t.Errorf("the owner was told %+v", got)
	}
}

// ---- the reminders -----------------------------------------------------------------------------------------------------------------------

// A machine that waits for its restart is spoken of again after an hour, after a day, and every day: each reminder once, whatever the number of looks, and a core that was down
// for days says it once and not for each day it missed.
func TestAMachineThatWaitsIsSpokenOfAgainOnTheDays(t *testing.T) {
	box := newUpgradeBox(t)
	theBarrierSees(t)
	voice := box.withAVoice()
	run := box.aHopAtItsRestart()
	box.point(run)
	box.tick()
	if len(voice.told()) != 1 {
		t.Fatalf("the first look told %+v", voice.told())
	}

	box.advance(osUpgradeTiming.Reminders[0])
	box.tick()
	box.tick()
	if got := voice.told(); len(got) != 2 || got[1].key != run.ID+":reminder:1" {
		t.Fatalf("after an hour the owner was told %+v", got)
	}
	box.advance(osUpgradeTiming.Reminders[1])
	box.tick()
	if got := voice.told(); len(got) != 3 || got[2].key != run.ID+":reminder:2" {
		t.Fatalf("after a day the owner was told %+v", got)
	}
	box.advance(osUpgradeTiming.Reminders[2])
	box.tick()
	if got := voice.told(); len(got) != 4 || got[3].key != run.ID+":reminder:3" {
		t.Fatalf("after another day the owner was told %+v", got)
	}

	// the core was down for a week: one reminder, the one that is due
	box.advance(7 * 24 * time.Hour)
	box.tick()
	box.tick()
	if got := voice.told(); len(got) != 5 {
		t.Errorf("after a week the owner was told %d things: %+v", len(got), got)
	}
}

// The reminders are for the machine that waits, and for none of the others.
func TestTheRemindersAreForTheMachineThatWaits(t *testing.T) {
	box := newUpgradeBox(t)
	voice := box.withAVoice()
	run := box.aHopThatStoppedHalfWay(osrun.ClassBusy)
	box.own(run, osrun.StateStoppedAtWaypoint)
	box.tick()
	box.advance(48 * time.Hour)
	box.tick()
	if got := voice.told(); len(got) != 1 {
		t.Errorf("the owner was told %+v", got)
	}
}

// ---- keeping the quiet ----------------------------------------------------------------------------------------------------------------

// The alerts about the apps' containers are kept quiet while the upgrade holds the machine, and for ten minutes after: containers stop and start during the hop.
func TestTheContainerAlertsAreQuietWhileTheUpgradeHoldsTheMachineAndAfter(t *testing.T) {
	box := newUpgradeBox(t)
	if box.OSUpgradeMuted() {
		t.Fatal("quiet with no upgrade")
	}
	run := box.aHopAtItsRestart()
	box.own(run, osrun.StateAwaitingReboot)
	if !box.OSUpgradeMuted() {
		t.Error("not quiet while the upgrade holds the machine")
	}

	// the check passed: the machine is let go of, and the apps' own watch is slow to say that the containers are back
	run = box.aHopThatSucceededOn(run)
	box.tick()
	if box.flagIsThere() {
		t.Fatal("the machine is still held")
	}
	if !box.OSUpgradeMuted() {
		t.Error("not quiet just after the upgrade let go of the machine")
	}
	box.advance(8 * time.Minute)
	if !box.OSUpgradeMuted() {
		t.Error("not quiet nine minutes after the check ended")
	}
	box.advance(3 * time.Minute)
	if box.OSUpgradeMuted() {
		t.Error("still quiet twelve minutes after the check ended")
	}
}

// A core that is started in the ten minutes after an upgrade ended reads it from the run and is quiet for what is left of them.
func TestACoreThatStartsJustAfterAnUpgradeIsQuiet(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatSucceeded()
	box.point(run)
	if err := clearOSUpgradeBusy(); err != nil {
		t.Fatal(err)
	}
	box.advance(2 * time.Minute)

	again := &systemService{packageUpdates: box.updater}
	if again.OSUpgradeMuted() {
		t.Fatal("quiet before it has looked")
	}
	again.osUpgradeSettleOnce(t.Context())
	if !again.OSUpgradeMuted() {
		t.Error("not quiet in the ten minutes after the upgrade")
	}
}

// A flag that cannot be asked about is a machine that is held: the alerts are quiet, as every reader of the flag is closed.
func TestAFlagThatCannotBeAskedAboutIsQuiet(t *testing.T) {
	box := newUpgradeBox(t)
	file := filepath.Join(t.TempDir(), "a file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	was := osUpgradeBusyFile
	osUpgradeBusyFile = filepath.Join(file, "busy")
	t.Cleanup(func() { osUpgradeBusyFile = was })
	if !box.OSUpgradeMuted() {
		t.Error("not quiet with a flag that cannot be asked about")
	}
}

// aHopThatSucceededOn is the run that was judged, after its check, with nothing to say against the machine.
func (b *upgradeBox) aHopThatSucceededOn(run *osUpgradeRun) *osUpgradeRun {
	b.t.Helper()
	run = b.attempt(b.read(run.ID), 2, osrun.PhaseVerify, osrun.WhoAuto)
	run = b.block(run, 2, 1, osrun.BlockAfter, true)
	run = b.leave(run, "done-after")
	run = b.say(run, osrun.Marker{Kind: osrun.MkVerifyDone, N: 2, Boot: boxBoot1})
	run = b.end(run, 2, "ok")
	if err := writeOSUpgradeVerify(run, osUpgradeVerifyDoc{At: b.now(), Findings: []osverify.Finding{}}); err != nil {
		b.t.Fatal(err)
	}
	b.setUnit("inactive")
	b.advance(time.Minute)
	b.point(run)
	return b.read(run.ID)
}

// A refresh that stopped may have upgraded packages: it is not told as an upgrade that stopped before it changed any.
func TestAFailedRefreshDoesNotSayThatNoPackageWasChanged(t *testing.T) {
	_, sentence, ok := osUpgradeAlertFor(osrun.State{Name: osrun.StateFailed, Reason: osrun.ClassNetwork, Kind: osrun.RunRefresh, RunID: "0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19"})
	if !ok || !strings.Contains(sentence, "refresh") || strings.Contains(sentence, "before it changed any package") || strings.Contains(sentence, "as it was") {
		t.Errorf("a failed refresh is told as %q, %v", sentence, ok)
	}
}
