package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// aHopThatSucceeded is a hop whose check passed: the verdict is written and says nothing against the machine.
func (b *upgradeBox) aHopThatSucceeded() *osUpgradeRun {
	b.t.Helper()
	run := b.aHopThatWasChecked()
	if err := writeOSUpgradeVerify(run, osUpgradeVerifyDoc{At: b.now(), Findings: []osverify.Finding{}}); err != nil {
		b.t.Fatal(err)
	}
	return b.own(run, osrun.StateVerifying)
}

func (b *upgradeBox) currentIs(runID string) bool {
	id, state := currentOSUpgrade()
	return state == osUpgradeHasRun && id == runID
}

// ---- acknowledge -------------------------------------------------------------------------------------------------------------------

// The owner closes a hop that succeeded: the word is in the log, the pointer to the run and the flag are gone, and the backup folder, which the blocks of a later run would stop at
// for its mark of an end, is moved into the run folder.
func TestTheOwnerClosesARunThatSucceeded(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatSucceeded()
	bk := box.backupOf(run)

	status, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge"})
	if err != nil {
		t.Fatal(err)
	}
	if got := markersOfKind(box.read(run.ID), osrun.MkAck); len(got) != 1 || got[0].Result != "acknowledge" {
		t.Errorf("the log says %+v", got)
	}
	if box.currentIs(run.ID) || box.flagIsThere() {
		t.Errorf("the run is still the latest (%v) or holds the machine (%v)", box.currentIs(run.ID), box.flagIsThere())
	}
	if _, err := os.Lstat(bk); !os.IsNotExist(err) {
		t.Errorf("the backup folder is still where the blocks of the next run would stop at it: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(run.Dir, osUpgradeBKDone, "done-after")); err != nil {
		t.Errorf("the backup folder was not moved into the run folder: %v", err)
	}
	if status.State != osrun.StateIdle || status.Busy {
		t.Errorf("the status is %q (busy %v)", status.State, status.Busy)
	}
}

// What ended well or was put back is closed with one word, and needs no typed one.
func TestTheOwnerClosesWhatEndedWithOneWord(t *testing.T) {
	cases := map[string]func(box *upgradeBox) *osUpgradeRun{
		"a refresh": func(box *upgradeBox) *osUpgradeRun { return box.aRefresh() },
		"a rehearsal that was put back": func(box *upgradeBox) *osUpgradeRun {
			run := box.aRehearsal()
			run = box.attempt(run, 2, osrun.PhaseUnwind, osrun.WhoAuto)
			run = box.say(run, osrun.Marker{Kind: osrun.MkUnwound, N: 2, Result: "ok"})
			return box.end(run, 2, "ok")
		},
		"a run that failed before it changed anything": func(box *upgradeBox) *osUpgradeRun {
			run := box.aRun(box.anUpgradePlan("ok", nil), osrun.RunHop)
			run = box.attempt(run, 1, osrun.PhaseRun, osrun.WhoOwner)
			run = box.stop(run, 1, 2, osrun.BlockTimers, osrun.ClassBusy, false)
			run = box.say(run, osrun.Marker{Kind: osrun.MkUnwound, N: 1, Result: "ok"})
			return box.end(run, 1, "stopped")
		},
		"a check that failed after the restart": func(box *upgradeBox) *osUpgradeRun {
			run := box.aHopThatWasChecked()
			if err := writeOSUpgradeVerify(run, osUpgradeVerifyDoc{At: box.now(), Findings: []osverify.Finding{{Code: osverify.CodeDockerDown, Severity: osverify.SeverityError}}}); err != nil {
				t.Fatal(err)
			}
			return run
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			box := newUpgradeBox(t)
			run := set(box)
			box.setUnit("inactive")
			box.own(run, osrun.StateRunning)
			if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge"}); err != nil {
				t.Fatal(err)
			}
			if box.currentIs(run.ID) || box.flagIsThere() {
				t.Error("the run is still open")
			}
		})
	}
}

// A machine that is half upgraded is let go of only with the word that says so: closing the run drops the flag that keeps the backups and the automatic updates from a machine that
// dpkg has begun to change. Without the word, with a wrong one, nothing is closed.
func TestAMachineThatIsHalfUpgradedIsLetGoOfOnlyWithTheWord(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatStoppedHalfWay(osrun.ClassNetwork)
	box.own(run, osrun.StateStoppedAtWaypoint)

	status := box.GetSystemOSUpgradeStatus()
	if status.AckWord != osrun.AckWord {
		t.Fatalf("the status does not give the word: %+v", status)
	}
	for _, c := range []struct {
		word, code string
	}{{"", OSUpgradeWordRequired}, {"yes", OSUpgradeBadWord}, {"i-know-this-machine-is-half-upgraded", OSUpgradeBadWord}} {
		_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge", Word: c.word})
		if refusal := refusalOf(t, err); refusal.Code != c.code {
			t.Errorf("word %q: refused with %q, want %q", c.word, refusal.Code, c.code)
		}
		if !box.currentIs(run.ID) || !box.flagIsThere() {
			t.Fatalf("word %q: the run was closed", c.word)
		}
	}
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge", Word: osrun.AckWord}); err != nil {
		t.Fatal(err)
	}
	if box.currentIs(run.ID) || box.flagIsThere() {
		t.Error("the run was not closed with the word")
	}
	// the backup folder that did not end stays where the owner who finishes it by hand will look
	if _, err := os.Lstat(osUpgradeBackupFolder(run.Meta.BK)); err != nil {
		t.Errorf("the backup folder of a hop that did not end was taken away: %v", err)
	}
}

// The check that found the machine not as it was is not a machine that is half upgraded: the last block before the restart ended.
func TestACheckThatFailedNeedsNoWord(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatWasChecked()
	if err := writeOSUpgradeVerify(run, osUpgradeVerifyDoc{At: box.now(), Findings: []osverify.Finding{{Code: osverify.CodeDockerDown, Severity: osverify.SeverityError}}}); err != nil {
		t.Fatal(err)
	}
	if status := box.GetSystemOSUpgradeStatus(); status.AckWord != "" {
		t.Errorf("the status asks for the word: %+v", status)
	}
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge"}); err != nil {
		t.Fatal(err)
	}
}

// What is not at its end is not closed: the answer says which state it is in.
func TestARunThatIsNotAtItsEndIsNotClosed(t *testing.T) {
	cases := []struct {
		name  string
		state string
		set   func(box *upgradeBox) *osUpgradeRun
	}{
		{"running", osrun.StateRunning, func(box *upgradeBox) *osUpgradeRun { return box.aHopAtItsDownload() }},
		{"waiting for the restart", osrun.StateAwaitingReboot, func(box *upgradeBox) *osUpgradeRun { return box.aHopAtItsRestart() }},
		{"checked, and not judged", osrun.StateVerifying, func(box *upgradeBox) *osUpgradeRun { return box.aHopThatWasChecked() }},
		{"rehearsed", osrun.StateRehearsed, func(box *upgradeBox) *osUpgradeRun { return box.aRehearsal() }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			box := newUpgradeBox(t)
			run := c.set(box)
			box.point(run)
			_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge", Word: osrun.AckWord})
			refusal := refusalOf(t, err)
			if refusal.Code != OSUpgradeNotTerminal || refusal.Data["state"] != c.state {
				t.Errorf("refused with %q %v, want %q in %q", refusal.Code, refusal.Data, OSUpgradeNotTerminal, c.state)
			}
			if !box.currentIs(run.ID) {
				t.Error("the run was closed")
			}
		})
	}
}

// A run is not closed while a unit may be running: the flag that keeps the backups away would go while the unit changes the machine.
func TestARunIsNotClosedWhileAUnitMayRun(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatStoppedHalfWay(osrun.ClassNetwork)
	box.own(run, osrun.StateStoppedAtWaypoint)
	box.setUnit("active")

	_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge", Word: osrun.AckWord})
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeUnitActive {
		t.Errorf("refused with %q", refusal.Code)
	}
	if !box.currentIs(run.ID) || !box.flagIsThere() {
		t.Error("the run was closed under a unit that runs")
	}
}

func TestThereIsNothingToCloseWithoutARun(t *testing.T) {
	box := newUpgradeBox(t)
	_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge"})
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeNoRun {
		t.Errorf("refused with %q", refusal.Code)
	}
}

// What the body says is a word of a closed set and a word that is a word: anything else is a bad request, with the field.
func TestTheWordsOfTheBodyAreHeldToTheirShape(t *testing.T) {
	box := newUpgradeBox(t)
	for _, c := range []struct {
		req   OSUpgradeAckRequest
		field string
	}{
		{OSUpgradeAckRequest{Action: "close"}, "action"},
		{OSUpgradeAckRequest{Action: ""}, "action"},
		{OSUpgradeAckRequest{Action: "acknowledge", Word: "a b"}, "word"},
		{OSUpgradeAckRequest{Action: "acknowledge", Word: "x\nI-KNOW"}, "word"},
		{OSUpgradeAckRequest{Action: "acknowledge", Word: strings.Repeat("a", 65)}, "word"},
	} {
		_, err := box.AckSystemOSUpgrade(c.req)
		if refusal := refusalOf(t, err); refusal.Code != OSUpgradeBadRequest || refusal.Data["field"] != c.field {
			t.Errorf("%+v: refused with %q %v", c.req, refusal.Code, refusal.Data)
		}
	}
}

// A refresh that ended is written down before it is closed: a plan that was made before it must not be started, and a core that did not look in the half minute since must not
// lose it.
func TestTheEndOfARefreshIsWrittenDownBeforeItIsClosed(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRefresh()
	box.point(run)
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge"}); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, filepath.Join(box.root, osUpgradeLastRefreshFile)); got == "" {
		t.Error("the end of the refresh was not written down")
	}
}

// ---- a run that cannot be read -------------------------------------------------------------------------------------------------------

// A pointer to a run that cannot be read is a machine that is not known to be free of it, and the owner can still let go of it, with the word: nothing else can be done with it.
func TestARunThatCannotBeReadIsLetGoOfWithTheWord(t *testing.T) {
	box := newUpgradeBox(t)
	box.putInRoot(osUpgradeCurrentFile, "0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19\n")
	if err := setOSUpgradeBusy("0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19", osrun.StateRunning, box.now()); err != nil {
		t.Fatal(err)
	}

	status := box.GetSystemOSUpgradeStatus()
	if status.AckWord != osrun.AckWord {
		t.Errorf("the status does not give the word: %+v", status.AckWord)
	}
	for _, action := range []string{"manual", "discard"} {
		_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: action})
		if refusal := refusalOf(t, err); refusal.Code != OSUpgradeRunUnreadable {
			t.Errorf("%s: refused with %q", action, refusal.Code)
		}
	}
	_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge"})
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeWordRequired {
		t.Errorf("refused with %q", refusal.Code)
	}
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge", Word: osrun.AckWord}); err != nil {
		t.Fatal(err)
	}
	if _, state := currentOSUpgrade(); state != osUpgradeNoRun || box.flagIsThere() {
		t.Error("the run that could not be read is still open")
	}
}

// ---- discard -----------------------------------------------------------------------------------------------------------------------------

// A run that failed before it changed anything is dropped with its plan: the plan was made for a machine that the run touched, and the owner asks for another.
func TestTheOwnerDropsARunThatFailedAndItsPlan(t *testing.T) {
	box := newUpgradeBox(t)
	box.stubStart()
	box.anUpgradePlan("ok", nil)
	run := box.aRun(box.livePlan(), osrun.RunHop)
	run = box.attempt(run, 1, osrun.PhaseRun, osrun.WhoOwner)
	run = box.stop(run, 1, 2, osrun.BlockTimers, osrun.ClassBusy, false)
	run = box.say(run, osrun.Marker{Kind: osrun.MkUnwound, N: 1, Result: "ok"})
	run = box.end(run, 1, "stopped")
	box.setUnit("inactive")
	box.own(run, osrun.StateRunning)
	if !box.osUpgradePlanAlive() {
		t.Fatal("the plan was not alive to begin with")
	}

	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "discard"}); err != nil {
		t.Fatal(err)
	}
	if box.osUpgradePlanAlive() {
		t.Error("the plan is still there")
	}
	if got := markersOfKind(box.read(run.ID), osrun.MkAck); len(got) != 1 || got[0].Result != "discard" {
		t.Errorf("the log says %+v", got)
	}
	if box.currentIs(run.ID) {
		t.Error("the run is still the latest")
	}
}

// Dropping a rehearsal puts the machine back: the sources of the next release are not the machine's, and the unwind is started, as the owner's, with no word in the log yet.
func TestTheOwnerDropsARehearsalAndTheMachineIsPutBack(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRehearsal()
	box.own(run, osrun.StateRehearsed)

	status, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "discard"})
	if err != nil {
		t.Fatal(err)
	}
	attempts := box.attemptsOf(run)
	if len(attempts) != 2 || attempts[1].Phase != osrun.PhaseUnwind || attempts[1].Who != osrun.WhoOwner || len(box.startedUnits()) != 1 {
		t.Fatalf("the attempts are %+v, %d units", attempts, len(box.startedUnits()))
	}
	if got := markersOfKind(box.read(run.ID), osrun.MkAck); len(got) != 0 {
		t.Errorf("the run was closed before the machine was put back: %+v", got)
	}
	if !box.currentIs(run.ID) || status.State != osrun.StateUnwinding {
		t.Errorf("the run is %q", status.State)
	}

	// and when it is put back it is a rehearsal that is done, which the owner closes
	run = box.say(box.read(run.ID), osrun.Marker{Kind: osrun.MkUnitStart, N: 2, Phase: osrun.PhaseUnwind, Boot: boxBoot1, PID: 300},
		osrun.Marker{Kind: osrun.MkUnwound, N: 2, Result: "ok"}, osrun.Marker{Kind: osrun.MkUnitEnd, N: 2, Result: "ok"})
	box.setUnit("inactive")
	box.advance(time.Minute)
	if state := box.state(run); state.Name != osrun.StateRehearsalDone || state.Busy {
		t.Errorf("the run is %+v", state)
	}
}

// What a discard is not for: a run that a plan is not behind any more and that the owner wants closed has acknowledge, and a run that holds a machine that is half upgraded has no
// discard at all.
func TestADiscardIsRefusedWhereTheStateDoesNotHaveIt(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatStoppedHalfWay(osrun.ClassNetwork)
	box.own(run, osrun.StateStoppedAtWaypoint)
	_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "discard", Word: osrun.AckWord})
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeNotTerminal {
		t.Errorf("refused with %q", refusal.Code)
	}
}

// ---- the takeover --------------------------------------------------------------------------------------------------------------------------

// The owner takes a machine that waits for its restart over: the core writes that it did, and from then on it never starts anything for the run, whatever the log says.
func TestTheOwnerTakesARunOver(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopAtItsRestart()
	box.point(run)

	status, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if got := markersOfKind(box.read(run.ID), osrun.MkTakeover); len(got) != 1 {
		t.Errorf("the log says %+v", got)
	}
	if status.State != osrun.StateManual || !status.Busy || !box.currentIs(run.ID) {
		t.Errorf("the status is %q (busy %v)", status.State, status.Busy)
	}
	box.tick()
	if !box.flagIsThere() {
		t.Error("the machine that the owner is finishing by hand is not held any more")
	}
}

// A run that was taken over is not repaired by the core, even when the shipped setting is changed and the reducer would have: the owner's hands are on it.
func TestARunThatWasTakenOverIsNeverRepairedByTheCore(t *testing.T) {
	withTiming(t, func(tm *osrun.Timing) { tm.AutoRecoverTries = 2 })
	box := newUpgradeBox(t)
	run := box.aHopThatStoppedHalfWay(osrun.ClassBusy)
	box.own(run, osrun.StateStoppedAtWaypoint)
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "manual"}); err != nil {
		t.Fatal(err)
	}
	box.advance(time.Hour)
	box.tick()
	if got := box.attemptsOf(run); len(got) != 1 || len(box.startedUnits()) != 0 {
		t.Errorf("%d attempts, %d units after the takeover", len(got), len(box.startedUnits()))
	}
}

// A run is not taken over under a unit that may run.
func TestARunIsNotTakenOverUnderAUnit(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopAtItsRestart()
	box.point(run)
	box.setUnit("active")
	_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "manual"})
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeUnitActive {
		t.Errorf("refused with %q", refusal.Code)
	}
	if got := markersOfKind(box.read(run.ID), osrun.MkTakeover); len(got) != 0 {
		t.Errorf("the log says %+v", got)
	}
}

// Only where the state has it: a run that is running, or one that ended well, is not taken over.
func TestARunIsTakenOverOnlyWhereTheStateHasIt(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatSucceeded()
	_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "manual"})
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeNotTerminal {
		t.Errorf("refused with %q", refusal.Code)
	}
	if got := markersOfKind(box.read(run.ID), osrun.MkTakeover); len(got) != 0 {
		t.Errorf("the log says %+v", got)
	}
}

// The owner who gave up the manual road and the one who finished it: the first needs the word, the second does not.
func TestTheManualRoadIsClosedWithTheWordUnlessItIsFinished(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopAtItsRestart()
	box.point(run)
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "manual"}); err != nil {
		t.Fatal(err)
	}

	// the blocks that the owner has done by hand: the machine is half upgraded until the check after the restart is done
	box.leave(run, "done-after")
	status := box.GetSystemOSUpgradeStatus()
	if status.State != osrun.StateManualDone || status.AckWord != "" {
		t.Fatalf("the status is %q (word %q)", status.State, status.AckWord)
	}
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge"}); err != nil {
		t.Fatal(err)
	}
}

func TestTheManualRoadThatIsGivenUpNeedsTheWord(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatStoppedHalfWay(osrun.ClassNetwork)
	box.own(run, osrun.StateStoppedAtWaypoint)
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "manual"}); err != nil {
		t.Fatal(err)
	}
	_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge"})
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeWordRequired {
		t.Errorf("refused with %q", refusal.Code)
	}
}

// With the switch off, everything above works: a machine that is half upgraded is not trapped behind it.
func TestTheWordsOfTheOwnerWorkWithTheSwitchOff(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopThatSucceeded()
	if osUpgradeBetaOn() {
		t.Fatal("the switch was on")
	}
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: "acknowledge"}); err != nil {
		t.Fatal(err)
	}
	if box.currentIs(run.ID) {
		t.Error("the run is still open")
	}
}
