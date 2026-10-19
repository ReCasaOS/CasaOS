package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"go.uber.org/zap"
)

// The modules, as the dashboard installs and removes them: the core runs the module assistant (`casaos module ...`, pkg/modulecmd) in its own process for what only reads (the list,
// the plan, the status), and as a transient unit for what changes the box (install and remove). The state of the job is never kept in memory: the log of the module, whose first line the
// core writes and whose other lines the unit writes, and the unit itself are read each time, so that a restart of the core in the middle of a job changes nothing.

// The codes that a request about a module is refused with (ModuleRefusal.Code); the plan's own codes (osmodule.Check*, osmodule.Refused*: catalogue, os, arch, recasaos,
// already_installed, foreign_unit, busy, dpkg_state, disk_short, disk_unknown, apt_missing, apt_failed, apt_removes, apt_downgrades, held_package, package_not_exact) are said as they are.
const (
	ModuleBadRequest   = "bad_request"
	ModuleNotRoot      = "not_root"
	ModuleUnsupported  = "unsupported"
	ModuleBusy         = "busy"
	ModulePlanChanged  = "plan_changed"
	ModuleNotInstalled = "not_installed"
	ModulePlanTimeout  = "plan_timeout"
	ModuleStartFailed  = "start_failed"
)

// the bounds of what the core asks in its own process; vars, for the tests
var (
	// systemModulePlanTimeout bounds a plan, which asks apt what installing the packages would do.
	systemModulePlanTimeout = time.Minute
	// systemModuleReadTimeout bounds the questions that only read: the list, the status, the removal's pre-check.
	systemModuleReadTimeout = 30 * time.Second
	// systemModuleLaunchStatusTimeout bounds the look at the module that the answer of a start has, which is no reason to keep the click waiting.
	systemModuleLaunchStatusTimeout = 5 * time.Second
	// systemModuleStartTimeout bounds what a start asks before it starts the unit, the plan not counted.
	systemModuleStartTimeout = time.Minute
)

// ModuleRefusal is a request about a module that was refused, with a code that the page puts in words. Detail is English, for whoever reads the raw answer; Data is what makes the code
// precise. The route says bad_request and start_failed with their own statuses and the others with 409.
type ModuleRefusal struct {
	Code   string
	Detail string
	Data   map[string]any
}

func (r *ModuleRefusal) Error() string { return r.Detail }

func refuseModule(code, detail string, data map[string]any) *ModuleRefusal {
	if data == nil {
		data = map[string]any{}
	}
	return &ModuleRefusal{Code: code, Detail: detail, Data: data}
}

func badModuleRequest(field string) *ModuleRefusal {
	return refuseModule(ModuleBadRequest, "a field of the request is not valid: "+field, map[string]any{"field": field})
}

// SystemModuleStatus is how a module is: the job that the core started for it, if there is one (or the last that was), and what the module assistant says of the module.
type SystemModuleStatus struct {
	Job    modulecmd.Job          `json:"job"`
	Status modulecmd.StatusResult `json:"status"`
}

func (s *systemService) ListSystemModules() (modulecmd.ListResult, error) {
	return s.systemPackageUpdater().listModules()
}

func (s *systemService) PlanSystemModule(name string) (osmodule.PlanResult, error) {
	return s.systemPackageUpdater().planModule(name)
}

func (s *systemService) StartSystemModuleInstall(ctx context.Context, name, planID string) (SystemModuleStatus, error) {
	return s.systemPackageUpdater().startModuleInstall(ctx, name, planID)
}

// PlanSystemModuleUpdate and StartSystemModuleUpdate are PlanSystemModule and StartSystemModuleInstall for the update of a module that is installed to the tag the catalogue names.
func (s *systemService) PlanSystemModuleUpdate(name string) (osmodule.PlanResult, error) {
	return s.systemPackageUpdater().planModuleMode(name, moduleVerbUpdate)
}

func (s *systemService) StartSystemModuleUpdate(ctx context.Context, name, planID string) (SystemModuleStatus, error) {
	return s.systemPackageUpdater().startModulePlanned(ctx, moduleVerbUpdate, name, planID)
}

func (s *systemService) StartSystemModuleRemove(ctx context.Context, name string) (SystemModuleStatus, error) {
	return s.systemPackageUpdater().startModuleRemove(ctx, name)
}

func (s *systemService) GetSystemModuleStatus(name string) (SystemModuleStatus, error) {
	return s.systemPackageUpdater().moduleStatus(name)
}

// moduleWorld is the world that the module assistant is run in when the core runs it in its own process: the box's. A field of the updater, for the tests.
func (u *systemPackageUpdater) moduleWorld() modulecmd.Env {
	if u.moduleEnv != nil {
		return u.moduleEnv()
	}
	return modulecmd.Production()
}

// listModules is the list of the modules of this release and what the box says of each. A catalogue that is not there or does not pass is an empty list that says so (catalogue.ok is false).
func (u *systemPackageUpdater) listModules() (modulecmd.ListResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), systemModuleReadTimeout)
	defer cancel()
	return modulecmd.List(ctx, u.moduleWorld())
}

// A turnstile lets one in at a time, and lets one that waits give up when its context ends. The zero value is ready.
type turnstile struct {
	once sync.Once
	slot chan struct{}
}

// enter is the turn, given back by the function that is returned; ok is false when ctx ended before the turn came (and for a ctx that has ended, whoever has the turn).
func (t *turnstile) enter(ctx context.Context) (leave func(), ok bool) {
	t.once.Do(func() { t.slot = make(chan struct{}, 1) })
	if ctx.Err() != nil {
		return nil, false
	}
	select {
	case t.slot <- struct{}{}:
		return func() { <-t.slot }, true
	case <-ctx.Done():
		return nil, false
	}
}

// makePlan is the plan of installing the module called name, made now, within ctx. A plan that apt was ended in for the time it has is a plan_timeout, not a plan that says apt gave no answer.
func (u *systemPackageUpdater) makePlan(ctx context.Context, name, verb string) (osmodule.PlanResult, error) {
	release, ok := u.modulePlan.enter(ctx)
	if !ok {
		return osmodule.PlanResult{}, refuseModule(ModulePlanTimeout, "the plan could not be made in the time it has", nil)
	}
	defer release()
	var plan osmodule.PlanResult
	if verb == moduleVerbUpdate {
		plan = modulecmd.PlanUpdateOf(ctx, u.moduleWorld(), name)
	} else {
		plan = modulecmd.PlanOf(ctx, u.moduleWorld(), name)
	}
	if ctx.Err() != nil {
		return osmodule.PlanResult{}, refuseModule(ModulePlanTimeout, "the plan could not be made in the time it has", nil)
	}
	return plan, nil
}

// planModule is the plan of installing the module called name: what the box says of it, with the checks in their order and the packages apt would install. It changes nothing; a plan that
// is refused is still a plan (its refused says why), and it is the start that refuses.
func (u *systemPackageUpdater) planModule(name string) (osmodule.PlanResult, error) {
	return u.planModuleMode(name, moduleVerbInstall)
}

// planModuleMode is planModule for an install (moduleVerbInstall) or for the update of an installed module (moduleVerbUpdate).
func (u *systemPackageUpdater) planModuleMode(name, verb string) (osmodule.PlanResult, error) {
	if !osmodule.ValidName(name) {
		return osmodule.PlanResult{}, badModuleRequest("name")
	}
	ctx, cancel := context.WithTimeout(context.Background(), systemModulePlanTimeout)
	defer cancel()
	return u.makePlan(ctx, name, verb)
}

// moduleSight is what systemd says of the unit of the job of a module, as the reducer takes it.
func (u *systemPackageUpdater) moduleSight(name string) modulecmd.UnitSight {
	switch active, answered := u.unitState(systemModuleUnit(name)); {
	case !answered:
		return modulecmd.UnitUnknown
	case active:
		return modulecmd.UnitActive
	}
	return modulecmd.UnitGone
}

// moduleStatus is the job of the module called name and what the assistant says of the module. The unit is looked at before the log is read.
func (u *systemPackageUpdater) moduleStatus(name string) (SystemModuleStatus, error) {
	if !osmodule.ValidName(name) {
		return SystemModuleStatus{}, badModuleRequest("name")
	}
	env := u.moduleWorld()
	sight := u.moduleSight(name)
	job, err := modulecmd.ReadJob(env, name, sight, u.now())
	if err != nil {
		return SystemModuleStatus{}, fmt.Errorf("the log of the module %s: %w", name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), systemModuleReadTimeout)
	defer cancel()
	status, err := modulecmd.StatusOf(ctx, env, name)
	if err != nil {
		return SystemModuleStatus{}, fmt.Errorf("the status of the module %s: %w", name, err)
	}
	return SystemModuleStatus{Job: job, Status: status}, nil
}

// moduleSupport says whether this box can run a job of a module: root, on a Debian-family system that has apt-get and systemd-run.
func (u *systemPackageUpdater) moduleSupport() (systemPackageSupport, error) {
	if u.getEUID() != 0 {
		return systemPackageSupport{}, refuseModule(ModuleNotRoot, "CasaOS must run as root to install a module.", nil)
	}
	support := u.support()
	if !support.supported {
		return support, refuseModule(ModuleUnsupported, support.reason, nil)
	}
	return support, nil
}

// moduleProgram refuses a start when the program that the unit runs is not there as a program: a regular file that can be run (a link to one is one). systemd-run would queue the unit
// all the same, and the job would read as running for 30 seconds and then as interrupted, with nothing to say why.
func (u *systemPackageUpdater) moduleProgram() error {
	if info, err := u.stat(systemModuleBinary); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
		return nil
	}
	return refuseModule(ModuleStartFailed, "The module assistant, "+systemModuleBinary+", is not there as a program that can be run. Nothing was started.", nil)
}

// busyModule is a refusal because something else is at work, with what it is (data.reason): every busy says it.
func busyModule(detail, reason string) *ModuleRefusal {
	return refuseModule(ModuleBusy, detail, map[string]any{"reason": reason})
}

// moduleBusy is busyModule for a box that is being changed by something else.
func moduleBusy(reason string) *ModuleRefusal {
	return busyModule("Another job or package operation is running on this box: "+reason, reason)
}

// moduleBlocked is the refusal, when something else is at work that a job of a module must not start beside; nil when nothing is. It is fail closed: what cannot be seen is taken for
// busy. Something else is: a job of a module whose log says it has not ended and that was seen alive a moment ago (a job whose unit systemd does not have yet is one), or whose unit
// systemd would not say anything of and whose log does not say it ended, a unit that changes the box (another job of a module, a package update, an update of Docker, the update of
// ReCasaOS, the upgrade of the operating system) or the flag of one, or a package manager that holds dpkg's lock, or a lock of dpkg that cannot be looked at.
func (u *systemPackageUpdater) moduleBlocked(ctx context.Context, env modulecmd.Env) error {
	names, err := modulecmd.JobLogs(env)
	if err != nil {
		return moduleBusy("the logs of the jobs of the modules cannot be read")
	}
	for _, name := range names {
		sight := u.moduleSight(name)
		job, err := modulecmd.ReadJob(env, name, sight, u.now())
		switch {
		case err != nil:
			return moduleBusy("the log of the module " + name + " cannot be read")
		case job.State == modulecmd.JobRunning:
			return moduleBusy("a job of the module " + name + " is running")
		case job.State == modulecmd.JobInterrupted && sight == modulecmd.UnitUnknown:
			// (a unit that is gone is a job that is over; one that systemd would not say anything of may be at work in a quiet apt)
			return moduleBusy("systemd does not say whether the unit of the job of the module " + name + " has ended")
		}
	}
	reason, isBusy, err := u.maintenanceState(ctx, "", u.command)
	switch {
	case isBusy:
		return moduleBusy(reason)
	case err != nil:
		return moduleBusy("whether another package manager is at work could not be seen")
	}
	return nil
}

// moduleTurn is the turn to start a job, which is waited for no longer than the request that asks it: a start that waits behind a plan that hangs gives up with its request.
func (u *systemPackageUpdater) moduleTurn(ctx context.Context) (leave func(), err error) {
	leave, ok := u.moduleStart.enter(ctx)
	if !ok {
		return nil, busyModule("Another job of a module is being started.", "another job of a module is being started")
	}
	return leave, nil
}

// startModuleInstall starts the unit that installs the module called name for the plan that the user read (planID). It checks again what the plan checked and what only matters now:
// nothing else is at work, the plan the box makes now is the one that was read, and it is not refused. It writes the first line of the log and returns as soon as systemd has the unit; an
// *ModuleRefusal says why nothing was started. reqCtx is the request's: the start waits for the starts before it no longer than that.
func (u *systemPackageUpdater) startModuleInstall(reqCtx context.Context, name, planID string) (SystemModuleStatus, error) {
	return u.startModulePlanned(reqCtx, moduleVerbInstall, name, planID)
}

// startModulePlanned is startModuleInstall for an install or for an update (verb): the unit runs the verb of the plan that the user read.
func (u *systemPackageUpdater) startModulePlanned(reqCtx context.Context, verb, name, planID string) (SystemModuleStatus, error) {
	switch {
	case !osmodule.ValidName(name):
		return SystemModuleStatus{}, badModuleRequest("name")
	case !modulePlanIDPattern.MatchString(planID):
		return SystemModuleStatus{}, badModuleRequest("plan_id")
	}
	support, err := u.moduleSupport()
	if err != nil {
		return SystemModuleStatus{}, err
	}
	if err := u.moduleProgram(); err != nil {
		return SystemModuleStatus{}, err
	}
	leave, err := u.moduleTurn(reqCtx)
	if err != nil {
		return SystemModuleStatus{}, err
	}
	defer leave()
	env := u.moduleWorld()
	// a first look, before the plan: a box that is busy is told so without waiting for apt
	look, cancelLook := context.WithTimeout(context.Background(), systemModuleStartTimeout)
	err = u.moduleBlocked(look, env)
	cancelLook()
	if err != nil {
		return SystemModuleStatus{}, err
	}
	// the plan has its own time, and is not counted in the start's
	planCtx, cancelPlan := context.WithTimeout(context.Background(), systemModulePlanTimeout)
	plan, err := u.makePlan(planCtx, name, verb)
	cancelPlan()
	if err != nil {
		return SystemModuleStatus{}, err
	}
	switch {
	case plan.Refused != "":
		return SystemModuleStatus{}, refusalOfPlan(plan, verb)
	case plan.PlanID != planID:
		return SystemModuleStatus{}, refuseModule(ModulePlanChanged, "The plan is no longer the one that was read: read the new plan and confirm again.", nil)
	}
	job, err := u.launchChecked(support, env, verb, name, planID, nil)
	if err != nil {
		return SystemModuleStatus{}, err
	}
	return SystemModuleStatus{Job: job, Status: u.statusAfterLaunch(env, name)}, nil
}

// refusalOfPlan is the refusal of a start whose plan is refused: the code of the plan, and its checks. A plan that is refused because the box is busy says what with, as every busy does.
func refusalOfPlan(plan osmodule.PlanResult, verb string) *ModuleRefusal {
	refusal := refuseModule(string(plan.Refused), "The module cannot be "+map[string]string{moduleVerbInstall: "installed", moduleVerbUpdate: "updated"}[verb]+": "+string(plan.Refused)+".", map[string]any{"checks": plan.Checks})
	if plan.Refused == osmodule.CheckBusy {
		reason := "the plan found something else at work"
		for _, c := range plan.Checks {
			if c.Code == osmodule.CheckBusy {
				switch c.Detail {
				case "unreadable":
					reason = "whether the upgrade of the operating system or a package manager is at work could not be seen"
				case "os_upgrade":
					reason = "the upgrade of the operating system is open or at work"
				case "package_manager":
					reason = "a package manager is at work"
				}
			}
		}
		refusal.Data["reason"] = reason
	}
	return refusal
}

// startModuleRemove starts the unit that removes the module called name.
func (u *systemPackageUpdater) startModuleRemove(reqCtx context.Context, name string) (SystemModuleStatus, error) {
	if !osmodule.ValidName(name) {
		return SystemModuleStatus{}, badModuleRequest("name")
	}
	support, err := u.moduleSupport()
	if err != nil {
		return SystemModuleStatus{}, err
	}
	if err := u.moduleProgram(); err != nil {
		return SystemModuleStatus{}, err
	}
	leave, err := u.moduleTurn(reqCtx)
	if err != nil {
		return SystemModuleStatus{}, err
	}
	defer leave()
	env := u.moduleWorld()
	job, err := u.launchChecked(support, env, moduleVerbRemove, name, "", func(ctx context.Context) error {
		switch modulecmd.RemoveRefusal(ctx, env, name) {
		case "":
			return nil
		case modulecmd.CodeNotInstalled:
			return refuseModule(ModuleNotInstalled, "There is nothing of this module to remove.", nil)
		}
		return moduleBusy("a package manager or the upgrade of the operating system is at work")
	})
	if err != nil {
		return SystemModuleStatus{}, err
	}
	return SystemModuleStatus{Job: job, Status: u.statusAfterLaunch(env, name)}, nil
}

// launchChecked looks at the box once more and starts the job, holding the lock of the updater as the starts of a package update and of an update of Docker do: none of them looks at the
// box while another is between its look and its unit. The look has a time of its own, since what was done before it (a plan) took the time it took. check, when it is given, is asked after
// the look and holding the lock, and may refuse. The lock is not held for the plan, which takes a minute, nor for the answer.
func (u *systemPackageUpdater) launchChecked(support systemPackageSupport, env modulecmd.Env, verb, name, planID string, check func(ctx context.Context) error) (modulecmd.Job, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), systemModuleStartTimeout)
	defer cancel()
	if err := u.moduleBlocked(ctx, env); err != nil {
		return modulecmd.Job{}, err
	}
	if check != nil {
		if err := check(ctx); err != nil {
			return modulecmd.Job{}, err
		}
	}
	return u.launchModule(support, env, verb, name, planID)
}

// queuedJob is the job as the first line of its log says it is: queued, and its unit on its way.
func queuedJob(name, nonce, kind string, queuedAt time.Time) modulecmd.Job {
	return modulecmd.ReduceLines(name, nonce, []string{strings.TrimSuffix(modulecmd.QueuedLine(name, nonce, kind, queuedAt), "\n")}, modulecmd.UnitActive, queuedAt, time.Time{})
}

// statusAfterLaunch is what the assistant says of the module, for the answer of a start: no reason to keep the click waiting for it, and the module is named whatever comes of it.
func (u *systemPackageUpdater) statusAfterLaunch(env modulecmd.Env, name string) modulecmd.StatusResult {
	ctx, cancel := context.WithTimeout(context.Background(), systemModuleLaunchStatusTimeout)
	defer cancel()
	if s, err := modulecmd.StatusOf(ctx, env, name); err == nil {
		return s
	}
	return modulecmd.StatusResult{Name: name}
}

// launchModule writes the first line of the log of the job, starts its unit and gives the job. A unit that systemd would not start is a log that says so, and nothing was changed. When
// systemd-run reports an error the unit may be there all the same (a bus that was too slow can say it failed after the job was queued, and its reply can be lost): systemd is asked, and
// the log is left as it is, and the job is the one the log says, unless systemd answered that it has no such unit, and the log is still the first line the core wrote.
func (u *systemPackageUpdater) launchModule(support systemPackageSupport, env modulecmd.Env, verb, name, planID string) (modulecmd.Job, error) {
	failed := func(detail string, err error) (modulecmd.Job, error) {
		if err != nil {
			logger.Error("a job of a module could not be started", zap.String("module", name), zap.String("verb", verb), zap.Error(err))
		}
		return modulecmd.Job{}, refuseModule(ModuleStartFailed, detail, nil)
	}
	nonce, err := dockerpkg.NewNonce()
	if err != nil {
		return failed("The job could not be prepared.", err)
	}
	args, err := systemModuleUnitArgs(verb, name, planID, nonce)
	if err != nil {
		// the name and the id were checked: this is a bug, and the unit would run as root
		return failed("The job could not be prepared.", err)
	}
	queuedAt := u.now().UTC()
	if err := modulecmd.Queue(env, name, nonce, verb, queuedAt); err != nil {
		if errors.Is(err, modulecmd.ErrLockHeld) {
			return modulecmd.Job{}, busyModule("Another run of the module assistant is at work.", "the lock of the module assistant is held")
		}
		return failed("The log of the job could not be written. Nothing was started.", err)
	}
	output, err := u.start(support.systemdPath, systemModuleUnit(name), args...)
	if err == nil {
		// what the first line of the log says, without reading it back: the job is queued and its unit is on its way
		return queuedJob(name, nonce, verb, queuedAt), nil
	}
	said := trimSystemPackageOutput(output)
	err = fmt.Errorf("start the job: %s: %w", said, err)
	exists, answered := u.unitExists(systemModuleUnit(name))
	switch {
	case exists:
		logger.Warn("systemd-run reported an error and the unit of a job of a module is there: its own words will say how it goes", zap.String("module", name), zap.Error(err))
		return u.jobAfterAnError(env, name, nonce, verb, queuedAt), nil
	case !answered:
		logger.Warn("systemd-run reported an error and systemd does not say whether the unit of a job of a module is there: the job is waited for", zap.String("module", name), zap.Error(err))
		return u.jobAfterAnError(env, name, nonce, verb, queuedAt), nil
	}
	switch abandonErr := modulecmd.Abandon(env, name, nonce, verb, queuedAt, u.now().UTC(), strings.Split(said, "\n")); {
	case errors.Is(abandonErr, modulecmd.ErrLockHeld) || errors.Is(abandonErr, modulecmd.ErrLogChanged):
		// the unit began between the answer of systemd and now: it holds the lock, or has written its own lines
		logger.Warn("systemd-run reported an error and the unit of a job of a module began all the same", zap.String("module", name), zap.Error(err))
		return u.jobAfterAnError(env, name, nonce, verb, queuedAt), nil
	case abandonErr != nil:
		logger.Error("the log of a job that was not started could not be closed", zap.String("module", name), zap.Error(abandonErr))
	}
	return failed("The job could not be started. Nothing was changed.", err)
}

// jobAfterAnError is the job whose unit may be there although systemd-run said it was not: what its log says, which is the first line the core wrote when the unit has not yet begun.
func (u *systemPackageUpdater) jobAfterAnError(env modulecmd.Env, name, nonce, verb string, queuedAt time.Time) modulecmd.Job {
	if job, err := modulecmd.ReadJob(env, name, u.moduleSight(name), u.now()); err == nil && job.State != modulecmd.JobIdle {
		return job
	}
	return queuedJob(name, nonce, verb, queuedAt)
}
