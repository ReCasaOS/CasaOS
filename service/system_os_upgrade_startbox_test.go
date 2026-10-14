package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// startStubs are the answers of the machine to the questions of a start that a test can change: who the machine is, what the report says, the sources as they are now.
// What several goroutines touch has its lock.
type startStubs struct {
	mu        sync.Mutex
	plannable func() *osplan.Failure
	readiness func(ctx context.Context, p osreadiness.Probe) osreadiness.Report
	rewritten func(hop osplan.HopSpec) (*osplan.Rewritten, error)
	etcSize   int64
	etcErr    error
	hostname  string
	facts     osverify.Facts
	runs      int
	askedFor  []string
}

// stubStart makes the box a machine that a start goes through: root, a Debian 11 that has a plan, a report that is ready, sources that are the plan's, room, no app
// at work, and a name. Everything that a test changes is a field of what it gives back.
func (b *upgradeBox) stubStart() *startStubs {
	b.t.Helper()
	st := &startStubs{hostname: "box1", etcSize: 40 << 20, facts: osverify.Facts{Taken: true, At: b.now(), Kernel: "5.10.0-28-amd64", DpkgQuiet: true}}
	originals := struct {
		plannable func(context.Context, osplan.Files) (osplan.HopSpec, *osplan.Failure)
		readiness func(context.Context, osreadiness.Probe, osreadiness.Options) osreadiness.Report
		hostname  func() (string, error)
		runID     func() string
		facts     func(context.Context, osreadiness.Probe) osverify.Facts
		rewrite   func(context.Context, osplan.HopSpec, osplan.Files) (*osplan.Rewritten, error)
		probe     func(*systemPackageUpdater) osReadinessProbe
		etc       func(context.Context) (int64, error)
	}{plannableOSPlan, runOSReadiness, osUpgradeHostname, osUpgradeNewRunID, osUpgradeTakeFacts, osUpgradeRewrite, newOSUpgradeProbe, osUpgradeEtcSize}
	b.t.Cleanup(func() {
		plannableOSPlan, runOSReadiness, osUpgradeHostname, osUpgradeNewRunID = originals.plannable, originals.readiness, originals.hostname, originals.runID
		osUpgradeTakeFacts, osUpgradeRewrite, newOSUpgradeProbe, osUpgradeEtcSize = originals.facts, originals.rewrite, originals.probe, originals.etc
	})
	plannableOSPlan = func(context.Context, osplan.Files) (osplan.HopSpec, *osplan.Failure) {
		st.mu.Lock()
		f := st.plannable
		st.mu.Unlock()
		hop, _ := osplan.PlannedHop(osreadiness.HopDebian11to12)
		if f != nil {
			if failure := f(); failure != nil {
				return osplan.HopSpec{}, failure
			}
		}
		return hop, nil
	}
	runOSReadiness = func(ctx context.Context, p osreadiness.Probe, _ osreadiness.Options) osreadiness.Report {
		st.mu.Lock()
		f := st.readiness
		st.mu.Unlock()
		if f != nil {
			return f(ctx, p)
		}
		return osreadiness.Report{State: osreadiness.StateReady, Checks: []osreadiness.Finding{}}
	}
	osUpgradeHostname = func() (string, error) {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.hostname, nil
	}
	osUpgradeNewRunID = func() string {
		st.mu.Lock()
		defer st.mu.Unlock()
		st.runs++
		return fmt.Sprintf("%08x-6c1e-4d0a-9a53-7f1c2d4e8b19", st.runs)
	}
	osUpgradeTakeFacts = func(context.Context, osreadiness.Probe) osverify.Facts {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.facts
	}
	osUpgradeRewrite = func(_ context.Context, hop osplan.HopSpec, _ osplan.Files) (*osplan.Rewritten, error) {
		st.mu.Lock()
		f := st.rewritten
		st.mu.Unlock()
		if f != nil {
			return f(hop)
		}
		plan := b.livePlan()
		return &osplan.Rewritten{HashCurrent: plan.Sources.HashCurrent, HashGenerated: plan.Sources.HashGenerated}, nil
	}
	osUpgradeEtcSize = func(context.Context) (int64, error) {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.etcSize, st.etcErr
	}
	newOSUpgradeProbe = func(u *systemPackageUpdater) osReadinessProbe {
		return osReadinessProbe{updater: u, except: systemOSUpgradeUnit, command: func(ctx context.Context, name string, args ...string) ([]byte, bool, error) {
			out, err := u.command(ctx, name, args...)
			return out, false, err
		}}
	}
	b.updater.dpkgLocked = func() bool { return false }
	none := AppOperationsFunc(func(context.Context) ([]string, error) { return nil, nil })
	b.updater.appOperations = none
	b.updater.appOps.Store(&none)
	return st
}

func (b *upgradeBox) livePlan() *osplan.Plan {
	st := b.osPlan.state.Load()
	if st == nil || st.plan == nil {
		b.t.Fatal("there is no plan")
	}
	return st.plan
}

func (b *upgradeBox) setApps(list AppOperationsFunc) {
	b.updater.appOperations = list
	b.updater.appOps.Store(&list)
}

// aPlanWithThingsToAccept is a plan whose verdict needs the owner's word: a package that it removes, and two sources that it puts aside.
func (b *upgradeBox) aPlanWithThingsToAccept() *osplan.Plan {
	return b.anUpgradePlan(osplan.VerdictNeedsAcceptance, func(p *osplan.Plan) {
		p.Problems = []osplan.Problem{{Code: osplan.ProblemRemovalManual, Severity: osplan.SeverityAcceptance, Data: map[string]any{"name": "fuse", "version": "2.9.9-5"}}}
		p.Removals = []osplan.Removal{{Name: "fuse", Version: "2.9.9-5", Manual: true}}
		p.Paused = []osplan.Paused{
			{File: "docker.list", Hosts: []string{"https://download.docker.com"}, Docker: true},
			{File: "sources.list", Line: 12, Hosts: []string{"http://ppa.example.org"}},
		}
	})
}

// aGoodRequest is the start that the plan asks for, and nothing more.
func (b *upgradeBox) aGoodRequest(plan *osplan.Plan) OSUpgradeStartRequest {
	req := OSUpgradeStartRequest{PlanID: plan.PlanID, SafetyNet: "snapshot", Hostname: "box1", Acks: []string{"no_interruption", "no_way_back"}, AcceptRemovals: []string{}, PausedSources: []string{}}
	req.AcceptRemovals = append(req.AcceptRemovals, osUpgradeManualRemovals(plan)...)
	req.PausedSources = append(req.PausedSources, osUpgradePausedNames(plan)...)
	if len(req.PausedSources) > 0 {
		req.Acks = append(req.Acks, "sources_paused")
	}
	if len(req.AcceptRemovals) > 0 {
		req.Acks = append(req.Acks, "removals_read")
	}
	return req
}

// switchOn puts the owner's switch on.
func (b *upgradeBox) switchOn() {
	b.t.Helper()
	if err := os.MkdirAll(filepath.Dir(osUpgradeBetaFile), 0o755); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(osUpgradeBetaFile, nil, 0o644); err != nil {
		b.t.Fatal(err)
	}
}

// aMachineThatCanStart is the box with all of it: the switch on, the stubs, and a plan that is good.
func (b *upgradeBox) aMachineThatCanStart() (*startStubs, *osplan.Plan) {
	b.t.Helper()
	b.switchOn()
	st := b.stubStart()
	return st, b.anUpgradePlan(osplan.VerdictOK, nil)
}

func refusalOf(t *testing.T, err error) *OSUpgradeRefusal {
	t.Helper()
	refusal, ok := err.(*OSUpgradeRefusal)
	if !ok || refusal == nil {
		t.Fatalf("err = %#v, want an *OSUpgradeRefusal", err)
	}
	return refusal
}

// runFolders are the folders of the runs that there are.
func (b *upgradeBox) runFolders() []string {
	b.t.Helper()
	entries, err := os.ReadDir(b.root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		b.t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// putInRoot writes a file in the folder of the runs, which is made if it is not there.
func (b *upgradeBox) putInRoot(name, text string) {
	b.t.Helper()
	if err := ensureOSUpgradeRoot(); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.root, name), []byte(text), 0o644); err != nil {
		b.t.Fatal(err)
	}
}
