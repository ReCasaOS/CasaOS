package service

import (
	"encoding/json"
	"path/filepath"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
)

// OSUpgradeRunbook is the text for the owner who takes a run over (or who looks at where it stands): the owner's own text of the hop of the run that is open, as it was written
// when the run began, with its blocks. It does not need the plan to be alive: the run kept it.
type OSUpgradeRunbook struct {
	PlanID string                `json:"plan_id"`
	Hop    string                `json:"hop"`
	Text   string                `json:"text"`
	Blocks []osplan.RunbookBlock `json:"blocks"`
	// FromRun says that the text is the one that a run kept, and not the one that the live plan would give now.
	FromRun bool `json:"from_run"`
}

// GetSystemOSUpgradeRunbook is the owner's text of the run that is open. It holds the owner's sources, credentials included, and is for the page that asks with the owner's own
// token. no_run says that there is none, and run_unreadable that there is one that is not whole.
func (s *systemService) GetSystemOSUpgradeRunbook() (*OSUpgradeRunbook, error) {
	run, _, refusal := s.osUpgradeCurrent()
	if refusal != nil {
		return nil, refusal
	}
	unreadable := refuseUpgrade(OSUpgradeRunUnreadable, "the text of the upgrade cannot be read", map[string]any{"run_id": run.ID})
	text, err := readOSUpgradeFile(filepath.Join(run.Dir, osUpgradeManualFile), osplan.MaxRunbookBytes)
	if err != nil {
		return nil, unreadable
	}
	planJSON, err := readOSUpgradeFile(filepath.Join(run.Dir, osUpgradePlanJSON), 8<<20)
	if err != nil {
		return nil, unreadable
	}
	var plan osplan.Plan
	if json.Unmarshal(planJSON, &plan) != nil {
		return nil, unreadable
	}
	built, err := osplan.BuildRunbook(plan)
	if err != nil {
		return nil, unreadable
	}
	return &OSUpgradeRunbook{PlanID: run.Meta.PlanID, Hop: run.Meta.Hop, Text: string(text), Blocks: built.Blocks, FromRun: true}, nil
}
