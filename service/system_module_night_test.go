package service

import (
	"context"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/autoupdate"
	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// nightProvider is a provider with virt v0.2.4 installed and v0.2.5 in the catalogue; its job ends as jobs says, one
// status a look, the last one repeated.
type nightProvider struct {
	ModuleProvider // the methods the night does not use
	planLatest     string
	planErr        error
	planRefused    osmodule.Refusal
	startErr       error
	jobs           []modulecmd.Job
	started        []string
}

func (p *nightProvider) ListSystemModules() (modulecmd.ListResult, error) {
	version := "v0.2.4"
	return modulecmd.ListResult{
		Catalogue: modulecmd.CatalogueInfo{OK: true},
		Modules: []modulecmd.ListModule{
			{Name: "virt", Installed: &modulecmd.Installed{Version: &version}, Update: &modulecmd.UpdateInfo{Available: true, Installed: "v0.2.4", Latest: "v0.2.5"}},
			{Name: "scan", Installed: &modulecmd.Installed{Version: &version}, Update: &modulecmd.UpdateInfo{Installed: "v0.2.4", Latest: "v0.2.4"}},
			{Name: "mcp"},
		},
	}, nil
}

func (p *nightProvider) PlanSystemModuleUpdate(name string) (osmodule.PlanResult, error) {
	return osmodule.PlanResult{Name: name, PlanID: "plan-1", Mode: "update", Refused: p.planRefused, Update: &osmodule.UpdateInfo{Installed: "v0.2.4", Latest: p.planLatest}}, p.planErr
}

func (p *nightProvider) StartSystemModuleUpdate(_ context.Context, name, planID string) (SystemModuleStatus, error) {
	if p.startErr != nil {
		return SystemModuleStatus{}, p.startErr
	}
	p.started = append(p.started, name+" "+planID)
	return SystemModuleStatus{}, nil
}

func (p *nightProvider) GetSystemModuleStatus(string) (SystemModuleStatus, error) {
	job := p.jobs[0]
	if len(p.jobs) > 1 {
		p.jobs = p.jobs[1:]
	}
	return SystemModuleStatus{Job: job}, nil
}

func TestTheNightUpdatesAModuleAsTheDashboardDoes(t *testing.T) {
	poll, bound := nightModulePoll, nightModuleBound
	nightModulePoll, nightModuleBound = time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { nightModulePoll, nightModuleBound = poll, bound })
	running := modulecmd.Job{State: modulecmd.JobRunning}

	failed := modulecmd.Job{State: modulecmd.JobFailed, Code: "apt_failed", Undo: modulecmd.UndoRolledBack}
	for _, c := range []struct {
		name    string
		p       nightProvider
		outcome autoupdate.Outcome
		detail  string
		started int
	}{
		{"succeeded", nightProvider{planLatest: "v0.2.5", jobs: []modulecmd.Job{running, running, {State: modulecmd.JobSucceeded}}}, autoupdate.Updated, "", 1},
		{"failed and put back", nightProvider{planLatest: "v0.2.5", jobs: []modulecmd.Job{running, failed}}, autoupdate.Failed, "put back", 1},
		{"failed before changing anything", nightProvider{planLatest: "v0.2.5", jobs: []modulecmd.Job{{State: modulecmd.JobFailed, Code: "download_failed"}}}, autoupdate.Failed, "failed: download_failed", 1},
		{"refused inside the unit", nightProvider{planLatest: "v0.2.5", jobs: []modulecmd.Job{running, {State: modulecmd.JobRefused, Code: "plan_changed"}}}, autoupdate.NotStarted, "not started: plan_changed", 1},
		{"never ends", nightProvider{planLatest: "v0.2.5", jobs: []modulecmd.Job{running}}, autoupdate.Failed, "still running after 200ms", 1},
		{"a tag the night did not wait for", nightProvider{planLatest: "v0.2.6"}, autoupdate.NotStarted, "not started: the catalogue changed", 0},
		{"the plan is refused", nightProvider{planLatest: "v0.2.5", planRefused: osmodule.CheckBusy}, autoupdate.NotStarted, "not started: busy", 0},
		{"the plan times out", nightProvider{planLatest: "v0.2.5", planErr: refuseModule(ModulePlanTimeout, "the plan could not be made in the time it has", nil)}, autoupdate.NotStarted, "not started: the plan could not be made in the time it has", 0},
		{"the start is refused as busy", nightProvider{planLatest: "v0.2.5", startErr: moduleBusy("a package manager is at work")}, autoupdate.NotStarted, "not started: Another job or package operation is running on this box: a package manager is at work", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &c.p
			n := &NightModules{Provider: p}
			list, err := n.List(context.Background())
			if err != nil || len(list) != 1 || list[0] != (autoupdate.ModuleUpdate{Name: "virt", Installed: "v0.2.4", Latest: "v0.2.5"}) {
				t.Fatalf("List() = %+v, %v: want virt alone", list, err)
			}
			outcome, detail := n.Update(context.Background(), "virt")
			if outcome != c.outcome || detail != c.detail || len(p.started) != c.started {
				t.Fatalf("Update() = %v, %q, started %v: want %v, %q", outcome, detail, p.started, c.outcome, c.detail)
			}
		})
	}
}

func TestTheNightUpdatesNothingItDidNotList(t *testing.T) {
	p := &nightProvider{planLatest: "v0.2.5"}
	if outcome, detail := (&NightModules{Provider: p}).Update(context.Background(), "virt"); outcome != autoupdate.NotStarted || len(p.started) != 0 {
		t.Fatalf("Update() before List = %v, %q, started %v", outcome, detail, p.started)
	}
}
