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
	return osmodule.PlanResult{Name: name, PlanID: "plan-1", Mode: "update", Update: &osmodule.UpdateInfo{Installed: "v0.2.4", Latest: p.planLatest}}, nil
}

func (p *nightProvider) StartSystemModuleUpdate(_ context.Context, name, planID string) (SystemModuleStatus, error) {
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

	for _, c := range []struct {
		name       string
		planLatest string
		jobs       []modulecmd.Job
		ok         bool
		detail     string
		started    int
	}{
		{"succeeded", "v0.2.5", []modulecmd.Job{running, running, {State: modulecmd.JobSucceeded}}, true, "", 1},
		{"failed and put back", "v0.2.5", []modulecmd.Job{running, {State: modulecmd.JobFailed, Code: "apt_failed", Undo: modulecmd.UndoRolledBack}}, false, "put back", 1},
		{"failed before changing anything", "v0.2.5", []modulecmd.Job{{State: modulecmd.JobFailed, Code: "download_failed"}}, false, "failed: download_failed", 1},
		{"never ends", "v0.2.5", []modulecmd.Job{running}, false, "still running after 200ms", 1},
		{"a tag the night did not wait for", "v0.2.6", nil, false, "not started: the catalogue changed", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &nightProvider{planLatest: c.planLatest, jobs: c.jobs}
			n := &NightModules{Provider: p}
			list, err := n.List(context.Background())
			if err != nil || len(list) != 1 || list[0] != (autoupdate.ModuleUpdate{Name: "virt", Installed: "v0.2.4", Latest: "v0.2.5"}) {
				t.Fatalf("List() = %+v, %v: want virt alone", list, err)
			}
			ok, detail := n.Update(context.Background(), "virt")
			if ok != c.ok || detail != c.detail || len(p.started) != c.started {
				t.Fatalf("Update() = %v, %q, started %v: want %v, %q", ok, detail, p.started, c.ok, c.detail)
			}
		})
	}
}

func TestTheNightUpdatesNothingItDidNotList(t *testing.T) {
	p := &nightProvider{planLatest: "v0.2.5"}
	if ok, detail := (&NightModules{Provider: p}).Update(context.Background(), "virt"); ok || len(p.started) != 0 {
		t.Fatalf("Update() before List = %v, %q, started %v", ok, detail, p.started)
	}
}
