package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// Nothing is closed, dropped or taken over while a unit of the upgrade may run, in whatever state the run is in: it is not only the busy states that look at the unit.
func TestNothingIsClosedWhileAUnitMayRunInAnyState(t *testing.T) {
	runs := map[string]func(b *upgradeBox) *osUpgradeRun{
		"a run that failed before it changed anything": func(b *upgradeBox) *osUpgradeRun { return b.aRunThatFailedBeforeItChangedAnything() },
		"a refresh that ended":                         func(b *upgradeBox) *osUpgradeRun { return b.own(b.aRefresh(), osrun.StateRefreshed) },
		"a success":                                    func(b *upgradeBox) *osUpgradeRun { return b.aHopThatSucceeded() },
		"a rehearsal that was undone": func(b *upgradeBox) *osUpgradeRun {
			run := b.attempt(b.aRehearsal(), 2, osrun.PhaseUnwind, osrun.WhoOwner)
			run = b.say(run, osrun.Marker{Kind: osrun.MkUnwound, N: 2, Result: "ok"})
			run = b.end(run, 2, "ok")
			return b.own(run, osrun.StateRehearsalDone)
		},
	}
	for name, setup := range runs {
		for unit, label := range map[string]string{"active": "a unit that runs", "activating": "a unit that is coming up", "": "a systemd that does not answer"} {
			actions := []string{OSUpgradeAckAcknowledge}
			if name == "a run that failed before it changed anything" {
				// the one that can be dropped: its plan was made for a machine that the run touched
				actions = append(actions, OSUpgradeAckDiscard)
			}
			for _, action := range actions {
				box := newUpgradeBox(t)
				run := setup(box)
				box.setUnit(unit)
				_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: action})
				if refusal := refusalOf(t, err); refusal.Code != OSUpgradeUnitActive {
					t.Errorf("%s with %s, %s: refused with %q", name, label, action, refusal.Code)
				}
				if got := markersOfKind(box.read(run.ID), osrun.MkAck); len(got) != 0 || !box.currentIs(run.ID) {
					t.Errorf("%s with %s, %s: the run was closed", name, label, action)
				}
				// and when the unit is gone the same click works
				box.setUnit("inactive")
				if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: action}); err != nil {
					t.Errorf("%s, %s: %v", name, action, err)
				}
			}
		}
	}
}

// A run that needs attention is closed with the word before the point of no return as after it: the sources of the next release may be in place.
func TestARunThatNeedsAttentionIsClosedWithTheWordWhateverThePointOfNoReturn(t *testing.T) {
	for name, c := range map[string]struct {
		setup func(b *upgradeBox) *osUpgradeRun
		pnr   bool
	}{
		"an unwind that was partial": {func(b *upgradeBox) *osUpgradeRun { return theRunsOfEveryState()["an unwind that was partial"](b) }, false},
		"files that are not the ones that were written": {func(b *upgradeBox) *osUpgradeRun {
			return theRunsOfEveryState()["a run whose files are not the ones that were written"](b)
		}, true},
	} {
		box := newUpgradeBox(t)
		run := c.setup(box)
		status := box.GetSystemOSUpgradeStatus()
		if status.State != osrun.StateNeedsAttention || status.PNR != c.pnr || status.AckWord != osrun.AckWord {
			t.Fatalf("%s: the status is %q pnr %v, word %q", name, status.State, status.PNR, status.AckWord)
		}
		for _, c := range []struct{ word, code string }{{"", OSUpgradeWordRequired}, {"I-KNOW", OSUpgradeBadWord}} {
			_, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckAcknowledge, Word: c.word})
			if refusal := refusalOf(t, err); refusal.Code != c.code {
				t.Errorf("%s: word %q: refused with %q", name, c.word, refusal.Code)
			}
		}
		if !box.currentIs(run.ID) || !box.flagIsThere() {
			t.Errorf("%s: the run was let go of without the word", name)
		}
		if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckAcknowledge, Word: osrun.AckWord}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if box.currentIs(run.ID) || box.flagIsThere() {
			t.Errorf("%s: the run is not closed with the word", name)
		}
	}
}

// A run that needs attention can be taken over with no word: the owner who does it finishes by hand.
func TestARunThatNeedsAttentionCanBeTakenOverWithNoWord(t *testing.T) {
	box := newUpgradeBox(t)
	run := theRunsOfEveryState()["an unwind that was partial"](box)
	if _, err := box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckManual}); err != nil {
		t.Fatal(err)
	}
	if got := markersOfKind(box.read(run.ID), osrun.MkTakeover); len(got) != 1 {
		t.Errorf("%+v", got)
	}
	if status := box.GetSystemOSUpgradeStatus(); status.State != osrun.StateManual || !status.Busy {
		t.Errorf("the status is %q busy %v", status.State, status.Busy)
	}
}

// A check that a block of stopped is run again by the click that the status offers: the log takes the attempt, with the files that say that the point of no return is behind.
func TestAFailedCheckThatABlockOfItStoppedIsResumed(t *testing.T) {
	box := newUpgradeBox(t)
	box.readyToResume()
	run := theRunsOfEveryState()["a check that a block of it stopped"](box)
	status := box.GetSystemOSUpgradeStatus()
	if status.State != osrun.StateFailed || status.Reason != osrun.ReasonVerify || status.Failure == nil || status.Failure.Class != osrun.ClassBarrierKernel {
		t.Fatalf("the status is %q %q %+v", status.State, status.Reason, status.Failure)
	}
	if len(status.Actions) == 0 || status.Actions[0] != osrun.ActionResume {
		t.Fatalf("the buttons are %v", status.Actions)
	}
	if _, err := box.ResumeSystemOSUpgrade(); err != nil {
		t.Fatalf("the click that was offered was refused: %v", err)
	}
	attempts := box.attemptsOf(run)
	if len(attempts) != 3 || attempts[2].Phase != osrun.PhaseVerify || attempts[2].Who != osrun.WhoOwner {
		t.Errorf("the attempts are %+v", attempts)
	}
	if got := readText(t, filepath.Join(run.Dir, "lists", "3.list")); got != "after\n" {
		t.Errorf("the list of the check is %q", got)
	}
	if len(box.startedUnits()) != 1 {
		t.Errorf("%d units were started", len(box.startedUnits()))
	}
}

// A check that was cut after the stop of a block of it is run again as well.
func TestACheckThatWasCutAfterItsStopIsRunAgainByTheCore(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.attempt(box.aHopThatRestarted(), 2, osrun.PhaseVerify, osrun.WhoAuto)
	run = box.block(run, 2, 1, osrun.BlockAfter, false)
	run = box.stop(run, 2, 1, osrun.BlockAfter, osrun.ClassBarrierKernel, true)
	box.setUnit("inactive")
	box.advance(time.Minute)
	box.own(run, osrun.StateVerifying)
	if tick := box.tick(); len(tick.Done) != 1 || tick.Done[0] != osrun.DoLaunchVerify {
		t.Fatalf("the look did %+v", tick)
	}
	if attempts := box.attemptsOf(run); len(attempts) != 3 || attempts[2].Phase != osrun.PhaseVerify || attempts[2].Who != osrun.WhoAuto {
		t.Errorf("the attempts are %+v", attempts)
	}
}
