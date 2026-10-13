package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"go.uber.org/zap/zapcore"
)

// ---- stand-ins for what the job calls ---------------------------------------------------------------------------------

// planStubs put the job's own functions in the place of osplan's: what they are asked, and what they say.
type planStubs struct {
	mu        sync.Mutex
	preflight func() *osplan.Failure
	plannable func() *osplan.Failure
	run       func(ctx context.Context, files osplan.Files, sb osplan.Sandbox, opts osplan.Options) (*osplan.Plan, *osplan.Failure)

	preflights, plannables, runs, sandboxes int
	sandboxList                             []osplan.Sandbox
	files                                   []osplan.Files
	contexts                                []context.Context
}

func stubOSPlan(t *testing.T) *planStubs {
	t.Helper()
	s := &planStubs{}
	s.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		return nil, &osplan.Failure{Code: osplan.CodeInternal, Detail: "no plan was stubbed"}
	}
	originalPreflight, originalPlannable, originalRun, originalSandbox := preflightOSPlan, plannableOSPlan, runOSPlan, newOSPlanSandbox
	t.Cleanup(func() {
		preflightOSPlan, plannableOSPlan, runOSPlan, newOSPlanSandbox = originalPreflight, originalPlannable, originalRun, originalSandbox
	})
	preflightOSPlan = func(context.Context, osplan.Files) (osplan.HopSpec, *osplan.Failure) {
		s.mu.Lock()
		s.preflights++
		f := s.preflight
		s.mu.Unlock()
		if f != nil {
			return osplan.HopSpec{}, f()
		}
		return osplan.HopSpec{}, nil
	}
	plannableOSPlan = func(context.Context, osplan.Files) (osplan.HopSpec, *osplan.Failure) {
		s.mu.Lock()
		s.plannables++
		f := s.plannable
		s.mu.Unlock()
		if f != nil {
			return osplan.HopSpec{}, f()
		}
		return osplan.HopSpec{}, nil
	}
	runOSPlan = func(ctx context.Context, files osplan.Files, sb osplan.Sandbox, opts osplan.Options) (*osplan.Plan, *osplan.Failure) {
		s.mu.Lock()
		s.runs++
		s.files = append(s.files, files)
		s.contexts = append(s.contexts, ctx)
		run := s.run
		s.mu.Unlock()
		return run(ctx, files, sb, opts)
	}
	newOSPlanSandbox = func() osplan.Sandbox {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.sandboxes++
		sb := &osPlanSandbox{}
		s.sandboxList = append(s.sandboxList, sb)
		return sb
	}
	return s
}

func (s *planStubs) count() (preflights, plannables, runs, sandboxes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preflights, s.plannables, s.runs, s.sandboxes
}

// planBox is a box that makes plans: its clock is the updater's, which a test moves.
type planBox struct {
	*systemService
	updater *systemPackageUpdater
	mu      sync.Mutex
	clock   time.Time
}

func newPlanBox(t *testing.T) *planBox {
	t.Helper()
	b := &planBox{updater: newTestSystemPackageUpdater(t), clock: time.Date(2026, time.October, 12, 10, 0, 0, 123456789, time.UTC)}
	b.updater.now = func() time.Time {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.clock
	}
	b.systemService = &systemService{packageUpdates: b.updater}
	return b
}

func (b *planBox) advance(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clock = b.clock.Add(d)
}

func (b *planBox) now() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.clock
}

// aPlan is a plan as osplan makes them, as far as the job and the runbook care: made at the given time.
func aPlan(made time.Time, verdict string, tree ...osplan.TreeFile) *osplan.Plan {
	if len(tree) == 0 {
		tree = []osplan.TreeFile{{Path: "sources.list", Content: "deb http://deb.debian.org/debian bookworm main\n"}}
	}
	p := &osplan.Plan{
		Hop:       "debian-11-12",
		MadeAt:    made.UTC().Truncate(time.Second),
		ExpiresAt: made.UTC().Truncate(time.Second).Add(osplan.PlanLifetime),
		Verdict:   verdict,
		Problems:  []osplan.Problem{},
		Removals:  []osplan.Removal{},
		Sources: osplan.SourcesPlan{
			Files: []osplan.FileChange{}, Tree: tree,
			HashCurrent: strings.Repeat("a", 64), HashGenerated: strings.Repeat("b", 64),
		},
		NewPackages: []string{}, KeptBack: []string{}, Autoremovable: []string{}, Paused: []osplan.Paused{}, Holds: []string{},
		Docker:    osplan.DockerState{PackagesHeld: []string{}},
		Disk:      []osplan.DiskRow{},
		Sandbox:   osplan.SandboxInfo{SecondsByPhase: map[string]float64{}},
		Estimates: osplan.Estimates{},
	}
	if verdict == osplan.VerdictBlocked {
		p.Problems = []osplan.Problem{{Code: osplan.ProblemDistKeptBack, Severity: osplan.SeverityBlocked, Data: map[string]any{"names": []string{"x"}}}}
	}
	p.PlanID = osplan.PlanID(*p)
	return p
}

// waitDone waits for a job to be over: its state is done or failed.
func (b *planBox) waitDone(limit time.Duration) (SystemOSPlanStatus, bool) {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if st := b.GetSystemOSPlan(); st.State == osPlanDone || st.State == osPlanFailed {
			return st, true
		}
	}
	return b.GetSystemOSPlan(), false
}

func (b *planBox) await(t *testing.T) SystemOSPlanStatus {
	t.Helper()
	st, ok := b.waitDone(10 * time.Second)
	if !ok {
		t.Fatalf("the job is not over: %+v", st)
	}
	return st
}

func refusalCode(t *testing.T, err error) *OSPlanRefusal {
	t.Helper()
	var refusal *OSPlanRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	return refusal
}

// ---- the job ----------------------------------------------------------------------------------------------------------

func TestAPlanIsMadeInTheBackgroundAndItsPhasesAreSaid(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	step := make(chan string)
	release := make(chan struct{})
	made := box.now()
	stubs.run = func(ctx context.Context, files osplan.Files, sb osplan.Sandbox, opts osplan.Options) (*osplan.Plan, *osplan.Failure) {
		for _, phase := range osplan.Phases {
			opts.Progress(phase)
			step <- phase
			<-release
		}
		return aPlan(made, osplan.VerdictOK), nil
	}

	started, err := box.StartSystemOSPlan()
	if err != nil {
		t.Fatal(err)
	}
	if started.State != osPlanRunning || started.Phase != osplan.PhaseSources || started.AlreadyRunning || !started.Plannable || started.StartedAt == nil {
		t.Fatalf("started = %+v", started)
	}
	if !started.StartedAt.Equal(made.Truncate(time.Second)) || started.StartedAt.Location() != time.UTC {
		t.Errorf("started_at = %v, want %v in UTC, to the second", started.StartedAt, made.Truncate(time.Second))
	}
	for _, phase := range osplan.Phases {
		if got := <-step; got != phase {
			t.Fatalf("phase = %q, want %q", got, phase)
		}
		if st := box.GetSystemOSPlan(); st.State != osPlanRunning || st.Phase != phase || st.Plan != nil || st.Error != nil || !st.Plannable || st.FinishedAt != nil {
			t.Fatalf("while %s: %+v", phase, st)
		}
		if _, err := box.GetSystemOSRunbook(); err == nil {
			t.Fatalf("while %s: a runbook was written for a plan that is not made", phase)
		} else if r := refusalCode(t, err); r.Code != "no_plan" || r.Data["reason"] != "running" {
			t.Fatalf("while %s: refusal = %+v", phase, r)
		}
		box.advance(time.Second)
		release <- struct{}{}
	}
	done := box.await(t)
	if done.State != osPlanDone || done.Plan == nil || done.Error != nil || done.Phase != "" {
		t.Fatalf("done = %+v", done)
	}
	if done.Plan.Sources.Tree != nil {
		t.Error("the plan endpoint says the tree of the sources: it is the runbook's")
	}
	if done.StartedAt == nil || done.FinishedAt == nil || !done.FinishedAt.After(*done.StartedAt) || done.ExpiresAt == nil || !done.ExpiresAt.Equal(made.Truncate(time.Second).Add(24*time.Hour)) {
		t.Errorf("times: started %v, finished %v, expires %v", done.StartedAt, done.FinishedAt, done.ExpiresAt)
	}
	if done.Expired || done.AlreadyRunning || !done.Plannable {
		t.Errorf("done = %+v", done)
	}
	// the stored plan still has its tree, which the runbook is written from
	rb, err := box.GetSystemOSRunbook()
	if err != nil {
		t.Fatal(err)
	}
	if rb.PlanID != done.Plan.PlanID || rb.Hop != "debian-11-12" || !strings.Contains(rb.Text, "deb http://deb.debian.org/debian bookworm main\n") || len(rb.Blocks) == 0 {
		t.Errorf("runbook = %+v", rb)
	}
}

func TestASecondRequestWhileOneRunsAnswersTheRunningJobAndStartsNoOther(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	gate := make(chan struct{})
	stubs.run = func(ctx context.Context, _ osplan.Files, _ osplan.Sandbox, opts osplan.Options) (*osplan.Plan, *osplan.Failure) {
		opts.Progress(osplan.PhaseUpdate)
		<-gate
		return aPlan(box.now(), osplan.VerdictOK), nil
	}
	first, err := box.StartSystemOSPlan()
	if err != nil {
		t.Fatal(err)
	}
	box.advance(5 * time.Second)
	// a refusal is not for a request that finds a job: the machine may have less room now than before it began
	stubs.mu.Lock()
	stubs.preflight = func() *osplan.Failure { return &osplan.Failure{Code: osplan.CodeNoRoom} }
	stubs.mu.Unlock()
	for i := 0; i < 3; i++ {
		second, err := box.StartSystemOSPlan()
		if err != nil {
			t.Fatalf("a second request: %v", err)
		}
		if !second.AlreadyRunning || second.State != osPlanRunning || second.StartedAt == nil || !second.StartedAt.Equal(*first.StartedAt) {
			t.Fatalf("second = %+v, first = %+v", second, first)
		}
	}
	close(gate)
	if st := box.await(t); st.State != osPlanDone {
		t.Fatalf("state = %+v", st)
	}
	if preflights, _, runs, sandboxes := stubs.count(); runs != 1 || preflights != 1 || sandboxes != 1 {
		t.Errorf("preflights = %d, runs = %d, sandboxes = %d: one job was started", preflights, runs, sandboxes)
	}
}

func TestOnlyOneOfManyRequestsAtOnceStartsAJob(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	gate := make(chan struct{})
	stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		<-gate
		return aPlan(box.now(), osplan.VerdictOK), nil
	}
	var wg sync.WaitGroup
	var started, already atomic.Int32
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := box.StartSystemOSPlan()
			switch {
			case err != nil:
				t.Errorf("err = %v", err)
			case st.AlreadyRunning:
				already.Add(1)
			default:
				started.Add(1)
			}
		}()
	}
	wg.Wait()
	close(gate)
	box.await(t)
	if _, _, runs, _ := stubs.count(); runs != 1 || started.Load() != 1 || already.Load() != 23 {
		t.Errorf("runs = %d, started = %d, already running = %d", runs, started.Load(), already.Load())
	}
}

// What is refused at once is refused with its code and starts nothing; the plan that was there stays.
func TestARefusalStartsNothingAndKeepsThePlanThatWasThere(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		return aPlan(box.now(), osplan.VerdictOK), nil
	}
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	before := box.await(t)

	for _, f := range []*osplan.Failure{
		{Code: osplan.CodeNotRoot, Detail: "the core does not run as root"},
		{Code: osplan.CodeHopNotPlanned, Detail: "no plan is made for this hop yet", Data: map[string]any{"hop": "debian-12-13"}},
		{Code: osplan.CodeIdentityUnsupported, Detail: "not one that a plan can be made for", Data: map[string]any{"state": "unsupported"}},
		{Code: osplan.CodeBusy, Detail: "another package manager is at work: x"},
		{Code: osplan.CodeNoRoom, Detail: "there is not room", Data: map[string]any{"path": "/var/tmp", "need": uint64(osplan.MinFreeBytes), "free": uint64(1)}},
	} {
		stubs.mu.Lock()
		stubs.preflight = func() *osplan.Failure { return f }
		stubs.mu.Unlock()
		_, _, runsBefore, sandboxesBefore := stubs.count()
		st, err := box.StartSystemOSPlan()
		if err == nil {
			t.Fatalf("%s: a job was started: %+v", f.Code, st)
		}
		r := refusalCode(t, err)
		if r.Code != f.Code || r.Detail != f.Detail || !slices.Equal(mapKeys(r.Data), mapKeys(f.Data)) || r.Error() != f.Detail {
			t.Errorf("%s: refusal = %+v", f.Code, r)
		}
		if _, _, runs, sandboxes := stubs.count(); runs != runsBefore || sandboxes != sandboxesBefore {
			t.Errorf("%s: something was started", f.Code)
		}
		if after := box.GetSystemOSPlan(); after.State != osPlanDone || after.Plan == nil || after.Plan.PlanID != before.Plan.PlanID {
			t.Errorf("%s: the plan that was there is gone: %+v", f.Code, after)
		}
	}
}

func mapKeys(m map[string]any) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// A failure that does not say where it was is said to be in the phase the job was in.
func TestAFailureThatSaysNoPhaseIsInThePhaseTheJobWasIn(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	failure := &osplan.Failure{Code: osplan.CodeTimeout, Detail: "the plan took too long"}
	stubs.run = func(_ context.Context, _ osplan.Files, _ osplan.Sandbox, opts osplan.Options) (*osplan.Plan, *osplan.Failure) {
		opts.Progress(osplan.PhaseUpgrade)
		return nil, failure
	}
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	st := box.await(t)
	if st.State != osPlanFailed || st.Phase != osplan.PhaseUpgrade || st.Error == nil || st.Error.Phase != osplan.PhaseUpgrade || st.Error.Code != osplan.CodeTimeout {
		t.Errorf("st = %+v, error = %+v", st, st.Error)
	}
	if failure.Phase != "" {
		t.Error("the failure that the plan gave was changed")
	}
}

func TestAJobThatFailsSaysItsCodeItsPhaseAndWhy(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	failure := &osplan.Failure{Code: osplan.CodeSandboxUpdateFailed, Phase: osplan.PhaseUpdate, Detail: "E: could not resolve", Data: map[string]any{"kind": osplan.KindNetwork}}
	stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		return nil, failure
	}
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	st := box.await(t)
	if st.State != osPlanFailed || st.Plan != nil || st.Error == nil || st.Error.Code != failure.Code || st.Error.Phase != osplan.PhaseUpdate || st.Error.Detail != failure.Detail || st.Error.Data["kind"] != osplan.KindNetwork {
		t.Fatalf("st = %+v, error = %+v", st, st.Error)
	}
	if st.Phase != osplan.PhaseUpdate || st.FinishedAt == nil || st.ExpiresAt != nil || !st.Plannable {
		t.Errorf("st = %+v", st)
	}
	if _, err := box.GetSystemOSRunbook(); err == nil {
		t.Fatal("a runbook for a plan that failed")
	} else if r := refusalCode(t, err); r.Code != "no_plan" || r.Data["reason"] != "failed" {
		t.Errorf("refusal = %+v", r)
	}
	// and the job is over: the next request starts another
	stubs.mu.Lock()
	stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		return aPlan(box.now(), osplan.VerdictOK), nil
	}
	stubs.mu.Unlock()
	if st, err := box.StartSystemOSPlan(); err != nil || st.AlreadyRunning {
		t.Fatalf("after a failure: %+v, %v", st, err)
	}
	if st := box.await(t); st.State != osPlanDone {
		t.Errorf("st = %+v", st)
	}
}

// A job that panics, in what it calls or in what is called back, is a failure that says internal; the core is not taken down
// and the next request starts another job.
func TestAJobThatPanicsEndsAsAnInternalFailureAndFreesTheSlot(t *testing.T) {
	for name, run := range map[string]func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure){
		"in the plan": func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
			panic("the plan panicked")
		},
		"after it began a phase": func(_ context.Context, _ osplan.Files, _ osplan.Sandbox, opts osplan.Options) (*osplan.Plan, *osplan.Failure) {
			opts.Progress(osplan.PhaseDistUpgrade)
			panic("the plan panicked")
		},
		"with nothing and nothing": func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
			return nil, nil
		},
	} {
		stubs := stubOSPlan(t)
		box := newPlanBox(t)
		stubs.run = run
		if _, err := box.StartSystemOSPlan(); err != nil {
			t.Fatal(err)
		}
		st := box.await(t)
		if st.State != osPlanFailed || st.Error == nil || st.Error.Code != osplan.CodeInternal || strings.Contains(st.Error.Detail, "panicked") {
			t.Errorf("%s: st = %+v, error = %+v: a panic is internal, and its text is not said", name, st, st.Error)
		}
		stubs.mu.Lock()
		stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
			return aPlan(box.now(), osplan.VerdictOK), nil
		}
		stubs.mu.Unlock()
		if st, err := box.StartSystemOSPlan(); err != nil || st.AlreadyRunning {
			t.Errorf("%s: the slot is not free: %+v, %v", name, st, err)
		}
		box.await(t)
	}
}

func TestAJobThatPanicsSaysThePhaseItWasIn(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	stubs.run = func(_ context.Context, _ osplan.Files, _ osplan.Sandbox, opts osplan.Options) (*osplan.Plan, *osplan.Failure) {
		opts.Progress(osplan.PhaseDistUpgrade)
		panic("x")
	}
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	if st := box.await(t); st.Error == nil || st.Error.Phase != osplan.PhaseDistUpgrade {
		t.Errorf("error = %+v", st.Error)
	}
}

// The end of the core cancels the job and waits for it to have removed its directory, whether the job has said a phase yet or not:
// the real plan says its first at once, and from then on the state that Stop reads is the one that the job stored as it went (a
// state that forgot the way to cancel, or the end to wait for, would let a SIGTERM leave the directory and apt running).
func TestTheCoresEndCancelsTheJobAndWaitsForItToRemoveItsDirectory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		phases []string
	}{
		{"before the job said a phase", nil},
		{"after the first phase", []string{osplan.PhaseSources}},
		{"after several phases", []string{osplan.PhaseSources, osplan.PhaseUpdate, osplan.PhaseUpgrade}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubs := stubOSPlan(t)
			box := newPlanBox(t)
			began := make(chan struct{})
			removed := make(chan struct{})
			stubs.run = func(ctx context.Context, _ osplan.Files, _ osplan.Sandbox, opts osplan.Options) (*osplan.Plan, *osplan.Failure) {
				for _, phase := range tc.phases {
					opts.Progress(phase)
				}
				close(began)
				<-ctx.Done()
				// what osplan.Run does: its own deferred removal, then the failure
				time.Sleep(50 * time.Millisecond)
				close(removed)
				return nil, &osplan.Failure{Code: osplan.CodeCanceled, Phase: osplan.PhaseUpdate, Detail: "the plan was canceled"}
			}
			// nothing runs: nothing to wait for
			took := time.Now()
			box.StopSystemOSPlan(10 * time.Second)
			if time.Since(took) > time.Second {
				t.Error("Stop waited for a job that is not there")
			}
			if _, err := box.StartSystemOSPlan(); err != nil {
				t.Fatal(err)
			}
			<-began
			if len(tc.phases) > 0 {
				if st := box.GetSystemOSPlan(); st.State != osPlanRunning || st.Phase != tc.phases[len(tc.phases)-1] {
					t.Fatalf("while it runs: %+v", st)
				}
			}
			took = time.Now()
			box.StopSystemOSPlan(OSPlanStopWait)
			select {
			case <-removed:
			default:
				t.Fatal("Stop came back before the job removed what it had made")
			}
			// it waits for the end of the job, which it hears, and not for the whole of the time it was given
			if d := time.Since(took); d > OSPlanStopWait/2 {
				t.Errorf("Stop took %v: it did not hear the end of the job", d)
			}
			st := box.GetSystemOSPlan()
			if st.State != osPlanFailed || st.Error == nil || st.Error.Code != osplan.CodeCanceled {
				t.Errorf("st = %+v", st)
			}
		})
	}
}

func TestStopDoesNotWaitLongerThanItWasToldTo(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	began, hold := make(chan struct{}), make(chan struct{})
	stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		close(began)
		<-hold // a job that does not hear the end
		return nil, &osplan.Failure{Code: osplan.CodeCanceled}
	}
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	<-began
	took := time.Now()
	box.StopSystemOSPlan(100 * time.Millisecond)
	if d := time.Since(took); d < 90*time.Millisecond || d > 5*time.Second {
		t.Errorf("Stop took %v", d)
	}
	close(hold)
	box.await(t)
}

func TestAJobHasTheTimeOfItsCommandsAndNoMore(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		return aPlan(box.now(), osplan.VerdictOK), nil
	}
	before := time.Now()
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	box.await(t)
	stubs.mu.Lock()
	ctx := stubs.contexts[0]
	stubs.mu.Unlock()
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Sub(before) > osPlanJobTimeout+time.Minute || deadline.Sub(before) < osPlanJobTimeout-time.Minute {
		t.Errorf("deadline = %v (%v from the start), want about %v", deadline, deadline.Sub(before), osPlanJobTimeout)
	}
	// the sixteen minutes of the commands (10 + 2 + 2 + 2) fit in it
	if osPlanJobTimeout <= osplan.UpdateTimeout+3*osplan.SolverTimeout {
		t.Errorf("the job has %v, and its commands have %v", osPlanJobTimeout, osplan.UpdateTimeout+3*osplan.SolverTimeout)
	}
	// released as the job ends: the job's own function says it, a moment after it has said the end
	released := false
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until) && !released; time.Sleep(time.Millisecond) {
		released = ctx.Err() != nil
	}
	if !released {
		t.Error("the context of a job that is over was not released")
	}
}

// ---- the plan, and when it is no longer one ------------------------------------------------------------------------

func TestAPlanExpiresAfterADayAndASecondOneReplacesTheFirst(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	var made []*osplan.Plan
	stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		p := aPlan(box.now(), osplan.VerdictOK, osplan.TreeFile{Path: "sources.list", Content: "deb http://deb.debian.org/debian bookworm main # " + box.now().Format(time.RFC3339) + "\n"})
		stubs.mu.Lock()
		made = append(made, p)
		stubs.mu.Unlock()
		return p, nil
	}
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	first := box.await(t)

	box.advance(24*time.Hour - 2*time.Second)
	if st := box.GetSystemOSPlan(); st.State != osPlanDone || st.Plan == nil || st.Expired {
		t.Fatalf("a plan one second before it expires: %+v", st)
	}
	if _, err := box.GetSystemOSRunbook(); err != nil {
		t.Fatalf("its runbook: %v", err)
	}
	box.advance(2 * time.Second)
	st := box.GetSystemOSPlan()
	if st.State != osPlanIdle || !st.Expired || st.Plan != nil || st.Error != nil || st.ExpiresAt == nil || !st.ExpiresAt.Equal(*first.ExpiresAt) {
		t.Fatalf("an expired plan is idle with expired: %+v", st)
	}
	if _, err := box.GetSystemOSRunbook(); err == nil {
		t.Fatal("a runbook for a plan that is expired")
	} else if r := refusalCode(t, err); r.Code != "no_plan" || r.Data["reason"] != "expired" {
		t.Errorf("refusal = %+v", r)
	}

	// a newer plan replaces the older, and is the one the runbook is written from
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatalf("a request when the plan has expired: %v", err)
	}
	second := box.await(t)
	if second.Expired || second.Plan == nil || second.StartedAt.Equal(*first.StartedAt) {
		t.Fatalf("second = %+v", second)
	}
	rb, err := box.GetSystemOSRunbook()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rb.Text, box.now().Format(time.RFC3339)) || strings.Contains(rb.Text, made[0].MadeAt.Format(time.RFC3339)+"\n") {
		t.Errorf("the runbook is not the newer plan's")
	}
	// a plan that is done is replaced by a request too, before it has expired
	box.advance(time.Hour)
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	box.await(t)
	if len(made) != 3 {
		t.Errorf("%d plans were made", len(made))
	}
}

func TestABlockedPlanIsShownAndHasNoRunbook(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		return aPlan(box.now(), osplan.VerdictBlocked), nil
	}
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	st := box.await(t)
	if st.State != osPlanDone || st.Plan == nil || st.Plan.Verdict != osplan.VerdictBlocked || len(st.Plan.Problems) != 1 {
		t.Fatalf("st = %+v", st)
	}
	_, err := box.GetSystemOSRunbook()
	if r := refusalCode(t, err); r.Code != "no_plan" || r.Data["reason"] != "blocked" || r.Detail == "" {
		t.Errorf("refusal = %+v", r)
	}
}

func TestAPlanWhoseSourcesCouldNotBeSaidSafelyHasNoRunbook(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		return aPlan(box.now(), osplan.VerdictOK, osplan.TreeFile{Path: "sources.list", Content: "deb http://x/y bookworm main # CASAOS_EOF\n"}), nil
	}
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	if st := box.await(t); st.State != osPlanDone || st.Plan == nil {
		t.Fatalf("st = %+v", st)
	}
	_, err := box.GetSystemOSRunbook()
	if r := refusalCode(t, err); r.Code != "no_runbook" || r.Data["reason"] != "runbook_unsafe" || r.Detail == "" {
		t.Errorf("refusal = %+v", r)
	}
}

func TestThereIsNoRunbookWhenNothingWasAskedFor(t *testing.T) {
	stubOSPlan(t)
	box := newPlanBox(t)
	_, err := box.GetSystemOSRunbook()
	if r := refusalCode(t, err); r.Code != "no_plan" || r.Data["reason"] != "idle" {
		t.Errorf("refusal = %+v", r)
	}
}

// ---- whether a plan can be made -------------------------------------------------------------------------------------------

func TestWhetherAPlanCanBeMadeIsSaidWithItsReasonAndAsksNothingWhileOneRuns(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	if st := box.GetSystemOSPlan(); !st.Plannable || st.Reason != "" || st.State != osPlanIdle || st.Phase != "" || st.StartedAt != nil || st.Plan != nil {
		t.Fatalf("idle: %+v", st)
	}
	for _, code := range []string{osplan.CodeNotRoot, osplan.CodeHopNotPlanned, osplan.CodeIdentityUnsupported} {
		stubs.mu.Lock()
		stubs.plannable = func() *osplan.Failure { return &osplan.Failure{Code: code, Detail: "x"} }
		stubs.mu.Unlock()
		if st := box.GetSystemOSPlan(); st.Plannable || st.Reason != code || st.State != osPlanIdle {
			t.Errorf("%s: %+v", code, st)
		}
	}
	// a job that runs was started because a plan could be made; the report is not made again each time the page asks
	gate := make(chan struct{})
	stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		<-gate
		return aPlan(box.now(), osplan.VerdictOK), nil
	}
	stubs.mu.Lock()
	stubs.plannable = nil
	stubs.mu.Unlock()
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	_, before, _, _ := stubs.count()
	for i := 0; i < 5; i++ {
		if st := box.GetSystemOSPlan(); !st.Plannable || st.State != osPlanRunning {
			t.Fatalf("st = %+v", st)
		}
	}
	if _, after, _, _ := stubs.count(); after != before {
		t.Errorf("the box was asked who it is %d times while a job ran", after-before)
	}
	close(gate)
	box.await(t)
}

// ---- what it is made of ---------------------------------------------------------------------------------------------------

// The plan reads the box through the Probe of the readiness report, the updater's own, and writes through a sandbox of its own
// for each job.
func TestThePlanReadsTheBoxThroughTheReadinessProbeAndEachJobHasItsOwnSandbox(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	stubs.run = func(_ context.Context, _ osplan.Files, sb osplan.Sandbox, _ osplan.Options) (*osplan.Plan, *osplan.Failure) {
		return aPlan(box.now(), osplan.VerdictOK), nil
	}
	for i := 0; i < 2; i++ {
		if _, err := box.StartSystemOSPlan(); err != nil {
			t.Fatal(err)
		}
		box.await(t)
	}
	stubs.mu.Lock()
	defer stubs.mu.Unlock()
	if len(stubs.files) != 2 || len(stubs.sandboxList) != 2 || stubs.sandboxList[0] == stubs.sandboxList[1] {
		t.Fatalf("files = %d, sandboxes = %d", len(stubs.files), len(stubs.sandboxList))
	}
	for _, f := range stubs.files {
		probe, ok := f.(osReadinessProbe)
		if !ok || probe.updater != box.updater {
			t.Errorf("the plan reads the box through %T", f)
		}
	}
}

// The job takes no lock of the package updater, and the package updater none of the job's: a check or an update that holds
// its locks does not hold the plan up, nor a plan the check.
func TestTheJobTakesNoLockOfThePackageUpdater(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	stubs.run = func(_ context.Context, _ osplan.Files, _ osplan.Sandbox, opts osplan.Options) (*osplan.Plan, *osplan.Failure) {
		opts.Progress(osplan.PhaseUpdate)
		return aPlan(box.now(), osplan.VerdictOK), nil
	}
	box.updater.mu.Lock()
	box.updater.checkMu.Lock()
	defer box.updater.mu.Unlock()
	defer box.updater.checkMu.Unlock()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		if _, err := box.StartSystemOSPlan(); err != nil {
			t.Errorf("start: %v", err)
		}
		if st, ok := box.waitDone(10 * time.Second); !ok || st.State != osPlanDone {
			t.Errorf("st = %+v", st)
		}
		if _, err := box.GetSystemOSRunbook(); err != nil {
			t.Errorf("runbook: %v", err)
		}
		box.StopSystemOSPlan(time.Second)
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the plan waits for a lock of the package updater")
	}
}

// ---- the log of the core, the glitches, and what the job leaves behind ------------------------------------------------------

func init() { logger.LogInitConsoleOnly() }

// logCapture keeps what the core's logger says while a test runs.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logCapture) Sync() error { return nil }

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func captureLog(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	logger.LogInitWithWriterSyncers(c)
	t.Cleanup(func() { logger.LogInitWithWriterSyncers(zapcore.AddSync(os.Stdout)) })
	return c
}

// What really went wrong with a job is in the log of the core, with the stack of a panic; what the page is told is generic.
func TestAnInternalFailureAndAPanicAreInTheLogAndNotInTheAnswer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		run     func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure)
		inLog   []string
		inReply string
	}{
		{"a failure with its cause", func(_ context.Context, _ osplan.Files, _ osplan.Sandbox, opts osplan.Options) (*osplan.Plan, *osplan.Failure) {
			opts.Progress(osplan.PhaseSources)
			return nil, &osplan.Failure{Code: osplan.CodeInternal, Phase: osplan.PhaseSources, Detail: "the sandbox directory could not be made", Cause: errors.New("mkdir /var/tmp/x: read-only file system")}
		}, []string{"read-only file system", "the sandbox directory could not be made", `"phase": "sources"`}, "read-only"},
		{"a panic of the plan, which the plan recovers and gives a stack with", func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
			return nil, (&osplan.Failure{Code: osplan.CodeInternal, Phase: osplan.PhaseParse, Detail: "an unexpected error", Cause: errors.New("a panic: boom\ngoroutine 7 [running]:\nmain.x()")})
		}, []string{"a panic: boom", "goroutine 7"}, "boom"},
		{"a panic of the plan that nothing recovered", func(_ context.Context, _ osplan.Files, _ osplan.Sandbox, opts osplan.Options) (*osplan.Plan, *osplan.Failure) {
			opts.Progress(osplan.PhaseDistUpgrade)
			panic("a value that the job must say")
		}, []string{"a value that the job must say", "goroutine", `"phase": "dist-upgrade"`}, "must say"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubs := stubOSPlan(t)
			box := newPlanBox(t)
			log := captureLog(t)
			stubs.run = tc.run
			if _, err := box.StartSystemOSPlan(); err != nil {
				t.Fatal(err)
			}
			st := box.await(t)
			if st.State != osPlanFailed || st.Error == nil || st.Error.Code != osplan.CodeInternal {
				t.Fatalf("st = %+v", st)
			}
			raw, err := json.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), tc.inReply) || strings.Contains(string(raw), "goroutine") || strings.Contains(string(raw), "cause") {
				t.Errorf("the answer says what went wrong inside: %s", raw)
			}
			for _, want := range tc.inLog {
				if !strings.Contains(log.String(), want) {
					t.Errorf("the log does not say %q:\n%s", want, log.String())
				}
			}
		})
	}
}

// A failure with a cause is in the log whatever its code, and a failure that has none and is not internal is not logged as one
// that went wrong inside: a failed update is the box's, not the core's.
func TestAFailureThatIsNotInternalIsNotLoggedAsOne(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	log := captureLog(t)
	stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		return nil, &osplan.Failure{Code: osplan.CodeSandboxUpdateFailed, Phase: osplan.PhaseUpdate, Detail: "E: could not resolve"}
	}
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	box.await(t)
	if got := log.String(); strings.Contains(got, "failed inside") {
		t.Errorf("logged: %s", got)
	}
	stubs.mu.Lock()
	stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		return nil, &osplan.Failure{Code: osplan.CodeTimeout, Phase: osplan.PhaseUpdate, Cause: errors.New("a cause that is not an internal failure's")}
	}
	stubs.mu.Unlock()
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	box.await(t)
	if got := log.String(); !strings.Contains(got, "a cause that is not an internal failure's") {
		t.Errorf("a failure that has a cause is not in the log: %s", got)
	}
}

// A job that is started replaces what was there, a plan that is still good included, and a job that fails leaves no plan: the doc
// comment of StartSystemOSPlan says so, and the runbook of the plan that was there is not served while the next one is made.
func TestAJobThatIsStartedReplacesAPlanThatIsStillGoodAndAFailureLeavesNone(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		return aPlan(box.now(), osplan.VerdictOK), nil
	}
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	if st := box.await(t); st.Plan == nil {
		t.Fatalf("st = %+v", st)
	}
	if _, err := box.GetSystemOSRunbook(); err != nil {
		t.Fatalf("the runbook of the plan: %v", err)
	}

	gate := make(chan struct{})
	stubs.mu.Lock()
	stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
		<-gate
		return nil, &osplan.Failure{Code: osplan.CodeSandboxUpdateFailed, Phase: osplan.PhaseUpdate, Detail: "E: could not resolve", Data: map[string]any{"kind": osplan.KindNetwork}}
	}
	stubs.mu.Unlock()
	box.advance(time.Hour) // the plan has 23 hours to live
	if _, err := box.StartSystemOSPlan(); err != nil {
		t.Fatal(err)
	}
	if st := box.GetSystemOSPlan(); st.State != osPlanRunning || st.Plan != nil || st.ExpiresAt != nil {
		t.Errorf("while the new job runs: %+v", st)
	}
	if _, err := box.GetSystemOSRunbook(); err == nil {
		t.Error("the runbook of the earlier plan is served while a job runs")
	} else if r := refusalCode(t, err); r.Code != OSPlanNoPlan || r.Data["reason"] != "running" {
		t.Errorf("refusal = %+v", r)
	}
	close(gate)
	st := box.await(t)
	if st.State != osPlanFailed || st.Plan != nil {
		t.Errorf("after a job that failed: %+v", st)
	}
	if _, err := box.GetSystemOSRunbook(); err == nil {
		t.Error("a runbook after a job that failed")
	} else if r := refusalCode(t, err); r.Data["reason"] != "failed" {
		t.Errorf("refusal = %+v", r)
	}
}

// leftSandbox is a sandbox that says that it could not remove its directory.
type leftSandbox struct {
	osPlanSandbox
	left string
}

func (s *leftSandbox) LeftBehind() string { return s.left }

// The directory that the sandbox could not remove is said in the state of the job, a job that failed or one that is done.
func TestADirectoryThatCouldNotBeRemovedIsSaidInTheStateOfTheJob(t *testing.T) {
	for _, tc := range []struct {
		name   string
		left   string
		failed bool
	}{
		{"a plan, and the directory removed", "", false},
		{"a plan, and a directory left", "/var/tmp/casaos-os-plan.77", false},
		{"a failure, and a directory left", "/var/tmp/casaos-os-plan.78", true},
		{"a failure, and the directory removed", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubs := stubOSPlan(t)
			box := newPlanBox(t)
			newOSPlanSandbox = func() osplan.Sandbox { return &leftSandbox{left: tc.left} }
			stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
				if tc.failed {
					return nil, &osplan.Failure{Code: osplan.CodeTimeout, Phase: osplan.PhaseUpdate}
				}
				return aPlan(box.now(), osplan.VerdictOK), nil
			}
			if _, err := box.StartSystemOSPlan(); err != nil {
				t.Fatal(err)
			}
			st := box.await(t)
			if st.SandboxLeftBehind != tc.left || (st.State == osPlanFailed) != tc.failed {
				t.Errorf("st = %+v", st)
			}
			raw, _ := json.Marshal(st)
			if has := strings.Contains(string(raw), "sandbox_left_behind"); has != (tc.left != "") {
				t.Errorf("the key is there: %v, left %q: %s", has, tc.left, raw)
			}
		})
	}
}

// ---- whether a plan can be made, asked of many ---------------------------------------------------------------------------

// The requests that come while the box is asked who it is share the answer: the identity checks run commands, and a page that
// asks again and again does not make the box run them again and again.
func TestManyRequestsAtOnceAskTheBoxWhoItIsOnce(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	gate := make(chan struct{})
	stubs.mu.Lock()
	stubs.plannable = func() *osplan.Failure { <-gate; return nil }
	stubs.mu.Unlock()
	const n = 20
	var called, plannable atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			called.Add(1)
			if st := box.GetSystemOSPlan(); st.Plannable {
				plannable.Add(1)
			}
		}()
	}
	for deadline := time.Now().Add(10 * time.Second); called.Load() < n && time.Now().Before(deadline); time.Sleep(time.Millisecond) {
	}
	time.Sleep(100 * time.Millisecond) // the others are in their call to the question
	close(gate)
	wg.Wait()
	if _, asked, _, _ := stubs.count(); asked > 3 || plannable.Load() != n {
		t.Errorf("the box was asked who it is %d times for %d requests; %d were told it can be planned", asked, n, plannable.Load())
	}
}

// A command of the question that goes wrong is not a box that cannot be planned: the answer says unknown, and no reason.
func TestAGlitchOfTheQuestionIsUnknownAndNotAReason(t *testing.T) {
	stubs := stubOSPlan(t)
	box := newPlanBox(t)
	for _, tc := range []struct {
		name    string
		failure *osplan.Failure
		reason  string
		unknown bool
	}{
		{"a box that is not supported", &osplan.Failure{Code: osplan.CodeIdentityUnsupported, Data: map[string]any{"state": "unsupported", "checks": []string{"unsupported_os"}}}, osplan.CodeIdentityUnsupported, false},
		{"the question went wrong", &osplan.Failure{Code: osplan.CodeIdentityUnsupported, Data: map[string]any{"state": "blocked", "unknown": true}}, "", true},
		{"unknown is not true", &osplan.Failure{Code: osplan.CodeIdentityUnsupported, Data: map[string]any{"unknown": false}}, osplan.CodeIdentityUnsupported, false},
		{"another reason that has the word", &osplan.Failure{Code: osplan.CodeNotRoot, Data: map[string]any{"unknown": true}}, osplan.CodeNotRoot, false},
		{"no data at all", &osplan.Failure{Code: osplan.CodeHopNotPlanned}, osplan.CodeHopNotPlanned, false},
	} {
		stubs.mu.Lock()
		stubs.plannable = func() *osplan.Failure { return tc.failure }
		stubs.mu.Unlock()
		st := box.GetSystemOSPlan()
		if st.Plannable || st.Reason != tc.reason || st.Unknown != tc.unknown {
			t.Errorf("%s: %+v", tc.name, st)
		}
		raw, _ := json.Marshal(st)
		if has := strings.Contains(string(raw), `"unknown":true`); has != tc.unknown {
			t.Errorf("%s: %s", tc.name, raw)
		}
	}
}

// ---- the runbook that cannot be given -----------------------------------------------------------------------------------

// A runbook that is larger than it may be, and a plan that has nothing to write one from, are refusals with their shape: the
// page reads the code and the reason.
func TestARunbookThatCannotBeWrittenIsRefusedWithItsReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		plan   func(*planBox) *osplan.Plan
		code   string
		reason string
	}{
		{"too large", func(b *planBox) *osplan.Plan {
			return aPlan(b.now(), osplan.VerdictOK, osplan.TreeFile{Path: "sources.list", Content: strings.Repeat("deb http://deb.debian.org/debian bookworm main\n", 6000)})
		}, OSPlanNoRunbook, "runbook_too_large"},
		{"nothing to write it from", func(b *planBox) *osplan.Plan {
			p := aPlan(b.now(), osplan.VerdictOK)
			p.Sources.Tree = nil
			return p
		}, OSPlanNoPlan, "unavailable"},
		{"a hop that has no runbook", func(b *planBox) *osplan.Plan {
			p := aPlan(b.now(), osplan.VerdictOK)
			p.Hop = "debian-12-13"
			return p
		}, OSPlanNoPlan, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubs := stubOSPlan(t)
			box := newPlanBox(t)
			stubs.run = func(context.Context, osplan.Files, osplan.Sandbox, osplan.Options) (*osplan.Plan, *osplan.Failure) {
				return tc.plan(box), nil
			}
			if _, err := box.StartSystemOSPlan(); err != nil {
				t.Fatal(err)
			}
			if st := box.await(t); st.State != osPlanDone || st.Plan == nil {
				t.Fatalf("st = %+v", st)
			}
			rb, err := box.GetSystemOSRunbook()
			if rb != nil || err == nil {
				t.Fatalf("a runbook: %v, %v", rb, err)
			}
			r := refusalCode(t, err)
			if r.Code != tc.code || r.Data["reason"] != tc.reason || r.Detail == "" || len(r.Data) != 1 {
				t.Errorf("refusal = %+v", r)
			}
			// a runbook that is too large says what to cut (the page has no retry for it: it cannot work)
			if tc.reason == "runbook_too_large" && !strings.Contains(r.Detail, "shorten the comments of the sources and the number of third-party files") {
				t.Errorf("the detail does not say what to cut: %q", r.Detail)
			}
		})
	}
}

// ---- the end of the core ---------------------------------------------------------------------------------------------------

// The removal of 214 MiB, and a second try of it a second later, with room to spare, and well within what systemd waits for.
func TestTheCoreGivesThePlanTimeToRemoveItsDirectory(t *testing.T) {
	if OSPlanStopWait < 5*time.Second || OSPlanStopWait > 30*time.Second {
		t.Errorf("the core waits %v for the plan to end", OSPlanStopWait)
	}
}

// The core listens to SIGTERM, and to SIGINT unless it was started ignoring it: to listen to a signal that was ignored is to stop
// ignoring it, and a core that a shell put in the background would then stop at the interrupt that was meant for the shell.
func TestTheCoreListensToSIGINTOnlyWhenItWasNotIgnored(t *testing.T) {
	if got := OSPlanStopSignals(false); !slices.Equal(got, []os.Signal{syscall.SIGTERM, syscall.SIGINT}) {
		t.Errorf("SIGINT not ignored: %v", got)
	}
	if got := OSPlanStopSignals(true); !slices.Equal(got, []os.Signal{syscall.SIGTERM}) {
		t.Errorf("SIGINT ignored: %v", got)
	}
}
