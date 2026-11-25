package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/autoupdate"
	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
)

// how the night waits for the job of a module's update; vars, for the tests
var (
	nightModulePoll = 10 * time.Second
	// nightModuleBound is past the job's own limits (download 30 min, apt's end 5, systemctl 2, rollback 5) with room
	// for the install, which apt does not bound: a job still running then is told as a failure, never waited on for good.
	nightModuleBound = 2 * time.Hour
)

// NightModules is pkg/autoupdate's Modules on a module provider: the dashboard's own list, update plan, start and
// status. It updates a module only to the tag its last List gave, so the night never installs a tag it did not wait for.
type NightModules struct {
	Provider ModuleProvider

	mu     sync.Mutex
	latest map[string]string // the tag each module was listed with
}

// List is the installed modules whose update the catalogue has (never a version it withdrew: the list does not offer
// one), a foreign one with whether its owner allowed the night to update it (the list's auto_update, read from modulecmd.AutoAllowed). A catalogue that cannot be read is an error, not an
// empty list: the night then changes nothing.
func (n *NightModules) List(context.Context) ([]autoupdate.ModuleUpdate, error) {
	result, err := n.Provider.ListSystemModules()
	if err != nil {
		return nil, err
	}
	if !result.Catalogue.OK {
		return nil, fmt.Errorf("the catalogue of the modules cannot be read: %s", result.Catalogue.Error)
	}
	list, latest := []autoupdate.ModuleUpdate{}, map[string]string{}
	for _, m := range result.Modules {
		if m.Installed == nil || m.Update == nil || !m.Update.Available {
			continue
		}
		allowed := m.AutoUpdate != nil && *m.AutoUpdate
		list = append(list, autoupdate.ModuleUpdate{Name: m.Name, Installed: m.Update.Installed, Latest: m.Update.Latest, Foreign: m.Foreign, Allowed: allowed})
		if m.Foreign && !allowed {
			continue // the night leaves it alone; Update refuses it by name too
		}
		latest[m.Name] = m.Update.Latest
	}
	n.mu.Lock()
	n.latest = latest
	n.mu.Unlock()
	return list, nil
}

// Update plans the update of the module, starts it as the dashboard does and waits for its job to end, at most
// nightModuleBound. What is refused before the unit is launched is NotStarted (a start error means systemd has no
// unit: launchModule says a unit that may be there as a job), and so is a job the unit refused; a job that ended other
// than succeeded or refused is Failed.
func (n *NightModules) Update(ctx context.Context, name string) (autoupdate.Outcome, string) {
	n.mu.Lock()
	want, listed := n.latest[name]
	n.mu.Unlock()
	if !listed {
		return autoupdate.NotStarted, "not started: not listed"
	}
	plan, err := n.Provider.PlanSystemModuleUpdate(name)
	switch {
	case err != nil:
		return autoupdate.NotStarted, "not started: " + err.Error()
	case plan.Refused != "":
		return autoupdate.NotStarted, "not started: " + string(plan.Refused)
	case plan.Update == nil || plan.Update.Latest != want:
		return autoupdate.NotStarted, "not started: the catalogue changed"
	}
	if _, err := n.Provider.StartSystemModuleUpdate(ctx, name, plan.PlanID); err != nil {
		return autoupdate.NotStarted, "not started: " + err.Error()
	}
	deadline := time.Now().Add(nightModuleBound)
	for {
		// a status that cannot be read is asked again, until the bound
		if status, err := n.Provider.GetSystemModuleStatus(name); err == nil && status.Job.State != modulecmd.JobRunning {
			return nightOutcome(status.Job)
		}
		if !time.Now().Before(deadline) {
			return autoupdate.Failed, "still running after " + nightModuleBound.String()
		}
		select {
		case <-ctx.Done():
			return autoupdate.Failed, "not waited for to its end"
		case <-time.After(nightModulePoll):
		}
	}
}

// nightOutcome is the job's end as the night tells it.
func nightOutcome(job modulecmd.Job) (autoupdate.Outcome, string) {
	switch {
	case job.State == modulecmd.JobSucceeded:
		return autoupdate.Updated, ""
	case job.State == modulecmd.JobRefused:
		// the unit refused before changing anything (the plan changed, the box became busy): the tag is not spent
		return autoupdate.NotStarted, "not started: " + job.Code
	case job.Undo == modulecmd.UndoRolledBack:
		return autoupdate.Failed, "put back"
	case job.Undo != "":
		return autoupdate.Failed, "not fully put back: " + job.Undo
	case job.Code != "":
		return autoupdate.Failed, job.State + ": " + job.Code
	}
	return autoupdate.Failed, job.State
}

// Withdrawn is the installed modules at a version the catalogue withdrew, as the list says them.
func (n *NightModules) Withdrawn(context.Context) ([]autoupdate.Withdrawn, error) {
	result, err := n.Provider.ListSystemModules()
	if err != nil {
		return nil, err
	}
	var list []autoupdate.Withdrawn
	for _, m := range result.Modules {
		if m.Installed != nil && m.Withdrawn != nil {
			list = append(list, autoupdate.Withdrawn{Name: m.Name, Tag: m.Withdrawn.Tag, Reason: m.Withdrawn.Reason})
		}
	}
	return list, nil
}
