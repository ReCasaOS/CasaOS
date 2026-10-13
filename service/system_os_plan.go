package service

import (
	"context"
	"errors"
	"os"
	"runtime/debug"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/singleflight"
	"go.uber.org/zap"
)

// The plan of the upgrade of the operating system, as the dashboard asks for it: what apt would do if the box were upgraded now,
// and the text with which to do the hop by hand, without changing the machine (pkg/osplan decides every rule of it; this is the
// job, the clock and the shape of the answers).
//
// A plan is made by a job in this process, one at a time, and lives in memory only: a core that restarts loses it, and the page
// asks again. The state of the job is one immutable value that the job replaces as it goes, and that a request reads without
// any lock: the status of a plan waits for nothing, and takes none of the package updater's locks, nor does the job (the
// sandbox has no locking of its own: Debug::NoLocking).
//
// A job that is started REPLACES what was there at once, a plan that has not expired included: while it runs there is no plan
// and no runbook (no_plan, reason running), and a job that fails leaves a failure and no plan. The plan of a day ago is not
// kept for the case that the next one fails: it is the plan of a machine that may have changed, and a new request is the page
// saying that it wants another.

const (
	osPlanIdle    = "idle"
	osPlanRunning = "running"
	osPlanDone    = "done"
	osPlanFailed  = "failed"

	// osPlanJobTimeout is what a job has in all: its commands have sixteen minutes (ten for the update, two for each of the
	// three simulations), and it reads the box for a few seconds around them.
	osPlanJobTimeout = 20 * time.Minute
	// osPlanAskTimeout is what a question to the box that a request waits for has (who it is, whether it is busy): the
	// readiness report gives its checks twenty seconds in all.
	osPlanAskTimeout = 30 * time.Second
)

// The codes of what the runbook refuses (osplan's failure codes are the others), and of why there is no plan to write it from.
const (
	OSPlanNoPlan    = "no_plan"
	OSPlanNoRunbook = "no_runbook"
)

// osPlanState is the state of the job, as one value that is never changed once it is stored: the job stores another.
type osPlanState struct {
	state      string
	phase      string
	startedAt  time.Time
	finishedAt time.Time
	failure    *osplan.Failure
	// plan has the tree of the sources, which the plan endpoint does not say and the runbook is written from.
	plan *osplan.Plan
	// leftBehind is the sandbox directory that the job could not remove, when it could not (it is removed at the next start of
	// the core, once it is ten minutes old).
	leftBehind string
	// cancel and done are the running job's: the first ends it, the second is closed when it has ended and removed its directory.
	cancel context.CancelFunc
	done   chan struct{}
}

// osPlanJob is the job that makes the plan: its state, and nothing else of its own.
type osPlanJob struct {
	state atomic.Pointer[osPlanState]
}

// What the job calls, as vars for a test to put its own in the place of: osplan's, and a sandbox for each job.
var (
	preflightOSPlan  = osplan.Preflight
	plannableOSPlan  = osplan.Plannable
	runOSPlan        = osplan.Run
	newOSPlanSandbox = func() osplan.Sandbox { return &osPlanSandbox{} }
)

// SystemOSPlanStatus is how the plan of the upgrade is going, or how it went: what GET /v1/sys/os/upgrade/plan says, and the
// answer to the POST that starts it.
type SystemOSPlanStatus struct {
	// Plannable says whether a plan can be made for this box; when it cannot, Reason is why (not_root, identity_unsupported,
	// hop_not_planned). Unknown says that it could not be told (a question to the machine that went wrong or took too long): it
	// is not a reason, there is none, and the page asks again.
	Plannable bool   `json:"plannable"`
	Reason    string `json:"reason,omitempty"`
	Unknown   bool   `json:"unknown,omitempty"`
	// State is idle (no plan, or one that expired), running, done or failed; Phase is the phase of a job that runs, or the one a
	// job that failed was in.
	State      string     `json:"state"`
	Phase      string     `json:"phase,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// ExpiresAt is when the plan of a job that is done expires, or expired (Expired, with the state idle).
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Expired   bool       `json:"expired,omitempty"`
	// AlreadyRunning is said by the answer to a request that found a job running, and started none.
	AlreadyRunning bool            `json:"already_running,omitempty"`
	Error          *osplan.Failure `json:"error,omitempty"`
	// SandboxLeftBehind is the directory of the sandbox that a job that is over could not remove: its whole path, like
	// "/var/tmp/casaos-os-plan.481516", and nothing else (nothing of what was in it).
	SandboxLeftBehind string `json:"sandbox_left_behind,omitempty"`
	// Plan is the plan of a job that is done, without the tree of the sources (the runbook has it).
	Plan *osplan.Plan `json:"plan,omitempty"`
}

// OSPlanRefusal is a request that is refused at once, with a code: the POST that would start a job (the codes of osplan that
// the preflight gives: not_root, hop_not_planned, identity_unsupported, busy, no_room) and the runbook that cannot be given
// (no_plan, no_runbook). The page says it by Code; Detail is English, for whoever reads the raw answer.
type OSPlanRefusal struct {
	Code   string
	Detail string
	Data   map[string]any
}

func (r *OSPlanRefusal) Error() string { return r.Detail }

func utc(t time.Time) *time.Time {
	u := t.UTC().Truncate(time.Second)
	return &u
}

// OSPlanStopWait is how long the core's end waits for a plan that is being made to be canceled and to remove its directory.
const OSPlanStopWait = 10 * time.Second

// OSPlanStopSignals are the signals that tell the core to stop and that it listens to, to end the plan that is being made
// first: SIGTERM, and SIGINT unless it was ignored when the core started (what ignored it, a shell that put the core in the
// background, meant it to go on), since to listen to a signal is to stop ignoring it.
func OSPlanStopSignals(sigintIgnored bool) []os.Signal {
	if sigintIgnored {
		return []os.Signal{syscall.SIGTERM}
	}
	return []os.Signal{syscall.SIGTERM, syscall.SIGINT}
}

// osPlanProbe is the Probe of the readiness report, the updater's own: the plan reads the box through it, and through no
// second implementation of reading it.
func (c *systemService) osPlanProbe() osReadinessProbe {
	return osReadinessProbe{updater: c.systemPackageUpdater()}
}

// statusOfOSPlan is what the state says, at the time given: a plan that expired is idle.
func statusOfOSPlan(st *osPlanState, now time.Time) SystemOSPlanStatus {
	status := SystemOSPlanStatus{State: osPlanIdle}
	if st == nil {
		return status
	}
	switch st.state {
	case osPlanRunning:
		status.State, status.Phase, status.StartedAt = osPlanRunning, st.phase, utc(st.startedAt)
	case osPlanFailed:
		status.State, status.Phase, status.StartedAt, status.FinishedAt = osPlanFailed, st.phase, utc(st.startedAt), utc(st.finishedAt)
		status.Error, status.SandboxLeftBehind = st.failure, st.leftBehind
	case osPlanDone:
		status.ExpiresAt = utc(st.plan.ExpiresAt)
		if st.plan.Expired(now) {
			status.Expired = true
			return status
		}
		plan := st.plan.WithoutTree()
		status.State, status.StartedAt, status.FinishedAt, status.Plan = osPlanDone, utc(st.startedAt), utc(st.finishedAt), &plan
		status.SandboxLeftBehind = st.leftBehind
	}
	return status
}

// osPlanGateFlight shares the question "can a plan be made for this box" among the requests that come while it is being asked:
// it runs the identity checks of the readiness report, which run commands (dpkg --print-architecture among them) and may take
// seconds, and a page that asks again and again must not make the box run them again and again.
var osPlanGateFlight singleflight.Group[*osplan.Failure]

// GetSystemOSPlan says how the plan is going, or how it went, and whether one can be made. It runs no apt and takes no lock
// of the package updater: it reads the state, and asks who the box is (a few commands of the readiness report, the same
// answer for the requests that come while it is asked) only when no job runs. When that question itself could not be
// answered (a command that failed or took too long), the status says Unknown and no reason: a glitch is not a box that cannot
// be planned.
func (c *systemService) GetSystemOSPlan() SystemOSPlanStatus {
	probe := c.osPlanProbe()
	status := statusOfOSPlan(c.osPlan.state.Load(), probe.Now())
	if status.State == osPlanRunning {
		// a job that runs was started because a plan could be made
		status.Plannable = true
		return status
	}
	f, _, _ := osPlanGateFlight.Do("plannable", func() (*osplan.Failure, error) {
		// not the context of the request that started it: the others wait for it too
		ctx, cancel := context.WithTimeout(context.Background(), osPlanAskTimeout)
		defer cancel()
		_, f := plannableOSPlan(ctx, probe)
		return f, nil
	})
	switch {
	case f == nil:
		status.Plannable = true
	case f.Code == osplan.CodeIdentityUnsupported && f.Data["unknown"] == true:
		status.Unknown = true
	default:
		status.Reason = f.Code
	}
	return status
}

// StartSystemOSPlan starts the job that makes the plan, and says it has begun; a job that runs is the answer to a request that
// finds one (AlreadyRunning), and none is started. A *OSPlanRefusal says why no job was started: the box is not one a plan can be
// made for, or something else changes its packages, or there is no room for the sandbox: what was there stays as it was. A job
// that IS started replaces what was there, a plan that is still good too, and stays the only thing there until it ends (see the
// top of this file).
func (c *systemService) StartSystemOSPlan() (SystemOSPlanStatus, error) {
	probe := c.osPlanProbe()
	preflighted := false
	for {
		prev := c.osPlan.state.Load()
		if prev != nil && prev.state == osPlanRunning {
			status := statusOfOSPlan(prev, probe.Now())
			status.Plannable, status.AlreadyRunning = true, true
			return status, nil
		}
		if !preflighted {
			ctx, cancel := context.WithTimeout(context.Background(), osPlanAskTimeout)
			_, f := preflightOSPlan(ctx, probe)
			cancel()
			if f != nil {
				return SystemOSPlanStatus{}, &OSPlanRefusal{Code: f.Code, Detail: f.Detail, Data: f.Data}
			}
			preflighted = true
		}
		jobCtx, cancel := context.WithTimeout(context.Background(), osPlanJobTimeout)
		running := &osPlanState{state: osPlanRunning, phase: osplan.PhaseSources, startedAt: probe.Now(), cancel: cancel, done: make(chan struct{})}
		if !c.osPlan.state.CompareAndSwap(prev, running) {
			// another request, or the end of the job, got there first: look again
			cancel()
			continue
		}
		go c.makeOSPlan(jobCtx, running, probe)
		status := statusOfOSPlan(running, probe.Now())
		status.Plannable = true
		return status, nil
	}
}

// makeOSPlan is the job: it runs osplan.Run, which removes the sandbox directory on every way out, and stores how it ended. A panic
// is a failure that says internal, and the job's slot is free after it. What really went wrong with an internal failure (the
// error of the machine, the panic and its stack) is in the log of the core and never in what the page is told.
func (c *systemService) makeOSPlan(ctx context.Context, running *osPlanState, probe osReadinessProbe) {
	defer close(running.done)
	defer running.cancel()
	phase := osplan.PhaseSources
	var plan *osplan.Plan
	var failure *osplan.Failure
	sb := newOSPlanSandbox()
	func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("the plan of the upgrade of the operating system panicked", zap.String("phase", phase), zap.Any("panic", r), zap.ByteString("stack", debug.Stack()))
				plan, failure = nil, &osplan.Failure{Code: osplan.CodeInternal, Phase: phase, Detail: "an unexpected error"}
			}
		}()
		plan, failure = runOSPlan(ctx, probe, sb, osplan.Options{Progress: func(next string) {
			phase = next
			// the job's state as it was with the phase changed and nothing else: what it has to be ended with (the cancel, and the
			// end to wait for) is not a thing that can be left out of a copy
			state := *running
			state.phase = next
			c.osPlan.state.Store(&state)
		}})
	}()
	if plan == nil && failure == nil {
		failure = &osplan.Failure{Code: osplan.CodeInternal, Phase: phase, Detail: "no plan was made, and no reason was given"}
	}
	if failure != nil && (failure.Code == osplan.CodeInternal || failure.Cause != nil) {
		logger.Error("the plan of the upgrade of the operating system failed inside", zap.String("code", failure.Code), zap.String("phase", failure.Phase), zap.String("detail", failure.Detail), zap.Error(failure.Cause))
	}
	final := &osPlanState{startedAt: running.startedAt, finishedAt: probe.Now()}
	if left, ok := sb.(interface{ LeftBehind() string }); ok {
		final.leftBehind = left.LeftBehind()
	}
	if failure != nil {
		if failure.Phase == "" {
			// the phase it was in, as every failure of osplan says it
			named := *failure
			named.Phase = phase
			failure = &named
		}
		final.state, final.phase, final.failure = osPlanFailed, failure.Phase, failure
	} else {
		final.state, final.plan = osPlanDone, plan
	}
	c.osPlan.state.Store(final)
}

// StopSystemOSPlan is for the core's end: it cancels the job that runs, and waits (no longer than wait) for it to end, which is
// to have removed the directory it made. It does nothing when no job runs.
func (c *systemService) StopSystemOSPlan(wait time.Duration) {
	st := c.osPlan.state.Load()
	if st == nil || st.state != osPlanRunning || st.cancel == nil {
		return
	}
	st.cancel()
	select {
	case <-st.done:
	case <-time.After(wait):
	}
}

// GetSystemOSRunbook is the text with which to do the hop by hand, written from the plan that is there. An *OSPlanRefusal says
// why there is none: no_plan (reason: idle, running, failed, expired, or blocked, which has no runbook, its problems say why),
// or no_runbook (reason: runbook_unsafe, runbook_too_large).
func (c *systemService) GetSystemOSRunbook() (*osplan.Runbook, error) {
	st := c.osPlan.state.Load()
	reason := "idle"
	switch {
	case st == nil:
	case st.state == osPlanRunning:
		reason = "running"
	case st.state == osPlanFailed:
		reason = "failed"
	case st.plan.Expired(c.osPlanProbe().Now()):
		reason = "expired"
	case st.plan.Verdict == osplan.VerdictBlocked:
		reason = "blocked"
	default:
		reason = ""
	}
	if reason != "" {
		return nil, &OSPlanRefusal{Code: OSPlanNoPlan, Detail: "there is no plan to write a runbook from (" + reason + ")", Data: map[string]any{"reason": reason}}
	}
	runbook, err := osplan.BuildRunbook(*st.plan)
	switch {
	case err == nil:
		return runbook, nil
	case errors.Is(err, osplan.ErrRunbookUnsafe):
		return nil, &OSPlanRefusal{Code: OSPlanNoRunbook, Detail: err.Error(), Data: map[string]any{"reason": "runbook_unsafe"}}
	case errors.Is(err, osplan.ErrRunbookTooLarge):
		return nil, &OSPlanRefusal{Code: OSPlanNoRunbook, Detail: err.Error(), Data: map[string]any{"reason": "runbook_too_large"}}
	}
	return nil, &OSPlanRefusal{Code: OSPlanNoPlan, Detail: err.Error(), Data: map[string]any{"reason": "unavailable"}}
}
