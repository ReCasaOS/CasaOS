package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The owner who takes a run over has the text of the run: the one that was written when it began, which does not need the plan to be alive, with its blocks.
func TestTheOwnerGetsTheTextOfTheRunThatIsOpen(t *testing.T) {
	box := newUpgradeBox(t)
	plan := box.anUpgradePlan("ok", nil)
	run := box.aRun(plan, osrun.RunHop)
	box.own(run, osrun.StateRunning)
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.Dir, osUpgradePlanJSON), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// the file of the run is the truth, and not what the plan would give again
	file := filepath.Join(run.Dir, osUpgradeManualFile)
	if err := os.WriteFile(file, []byte(readText(t, file)+"# a line that is only in the file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// the plan is gone: the text of the run is not
	box.osPlan.state.Store(&osPlanState{state: osPlanIdle})
	box.advance(48 * time.Hour)

	got, err := box.GetSystemOSUpgradeRunbook()
	if err != nil {
		t.Fatal(err)
	}
	want, err := osplan.BuildRunbook(*plan)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != readText(t, file) || got.Text == want.Text || got.PlanID != plan.PlanID || got.Hop != plan.Hop || !got.FromRun || len(got.Blocks) != len(want.Blocks) || len(got.Blocks) != 9 {
		t.Errorf("the text is %d bytes, plan %q, blocks %d, from the run %v", len(got.Text), got.PlanID, len(got.Blocks), got.FromRun)
	}
}

func TestThereIsNoTextWithoutARunOrWithARunThatIsNotWhole(t *testing.T) {
	box := newUpgradeBox(t)
	if _, err := box.GetSystemOSUpgradeRunbook(); refusalOf(t, err).Code != OSUpgradeNoRun {
		t.Error("a text was given with no run")
	}
	plan := box.anUpgradePlan("ok", nil)
	run := box.aRun(plan, osrun.RunHop)
	box.own(run, osrun.StateRunning)
	// the plan of aRun is a stub that is not a plan: the text cannot be given with its blocks
	if _, err := box.GetSystemOSUpgradeRunbook(); refusalOf(t, err).Code != OSUpgradeRunUnreadable {
		t.Error("a text was given for a run whose plan is not whole")
	}
	if err := os.Remove(filepath.Join(run.Dir, osUpgradeManualFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := box.GetSystemOSUpgradeRunbook(); refusalOf(t, err).Code != OSUpgradeRunUnreadable {
		t.Error("a text was given for a run that has none")
	}
}
