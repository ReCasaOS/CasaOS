package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
// already_installed, foreign_unit, busy, disk_short, disk_unknown, apt_missing, apt_failed, apt_removes, apt_downgrades, held_package, package_not_exact) are said as they are.
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

func (s *systemService) StartSystemModuleInstall(name, planID string) (SystemModuleStatus, error) {
	return s.systemPackageUpdater().startModuleInstall(name, planID)
}

func (s *systemService) StartSystemModuleRemove(name string) (SystemModuleStatus, error) {
	return s.systemPackageUpdater().startModuleRemove(name)
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

// planSlot makes the plans take turns: each is a few processes of apt, and a plan is not worth waiting more for than the time a plan has. The slot is given back by the function that is
// returned; ok is false when ctx ended first.
func (u *systemPackageUpdater) planSlot(ctx context.Context) (release func(), ok bool) {
	u.modulePlanOnce.Do(func() { u.modulePlanSlot = make(chan struct{}, 1) })
	select {
	case u.modulePlanSlot <- struct{}{}:
		return func() { <-u.modulePlanSlot }, true
	case <-ctx.Done():
		return nil, false
	}
}

// makePlan is the plan of installing the module called name, made now, within ctx. A plan that apt was ended in for the time it has is a plan_timeout, not a plan that says apt gave no answer.
func (u *systemPackageUpdater) makePlan(ctx context.Context, name string) (osmodule.PlanResult, error) {
	release, ok := u.planSlot(ctx)
	if !ok {
		return osmodule.PlanResult{}, refuseModule(ModulePlanTimeout, "the plan could not be made in the time it has", nil)
	}
	defer release()
	plan := modulecmd.PlanOf(ctx, u.moduleWorld(), name)
	if ctx.Err() != nil {
		return osmodule.PlanResult{}, refuseModule(ModulePlanTimeout, "the plan could not be made in the time it has", nil)
	}
	return plan, nil
}

// planModule is the plan of installing the module called name: what the box says of it, with the checks in their order and the packages apt would install. It changes nothing; a plan that
// is refused is still a plan (its refused says why), and it is the start that refuses.
func (u *systemPackageUpdater) planModule(name string) (osmodule.PlanResult, error) {
	if !osmodule.ValidName(name) {
		return osmodule.PlanResult{}, badModuleRequest("name")
	}
	ctx, cancel := context.WithTimeout(context.Background(), systemModulePlanTimeout)
	defer cancel()
	return u.makePlan(ctx, name)
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

// moduleBlocked is the refusal, when something else is at work that a job of a module must not start beside; nil when nothing is. It is fail closed: what cannot be seen is taken for
// busy. Something else is: a job of a module whose log says it has not ended and that was seen alive a moment ago (a job whose unit systemd does not have yet is one), a unit that changes
// the box (another job of a module, a package update, an update of Docker, the update of ReCasaOS, the upgrade of the operating system) or the flag of one, or a package manager that holds
// dpkg's lock, or a lock of dpkg that cannot be looked at.
func (u *systemPackageUpdater) moduleBlocked(ctx context.Context, env modulecmd.Env) error {
	busy := func(reason string) error {
		return refuseModule(ModuleBusy, "Another job or package operation is running on this box: "+reason, map[string]any{"reason": reason})
	}
	names, err := modulecmd.JobLogs(env)
	if err != nil {
		return busy("the logs of the jobs of the modules cannot be read")
	}
	for _, name := range names {
		job, err := modulecmd.ReadJob(env, name, modulecmd.UnitGone, u.now())
		if err != nil {
			return busy("the log of the module " + name + " cannot be read")
		}
		if job.State == modulecmd.JobRunning {
			return busy("a job of the module " + name + " is running")
		}
	}
	reason, isBusy, err := u.maintenanceState(ctx, "", u.command)
	switch {
	case isBusy:
		return busy(reason)
	case err != nil:
		return busy("whether another package manager is at work could not be seen")
	}
	return nil
}

// startModuleInstall starts the unit that installs the module called name for the plan that the user read (planID). It checks again what the plan checked and what only matters now:
// nothing else is at work, the plan the box makes now is the one that was read, and it is not refused. It writes the first line of the log and returns as soon as systemd has the unit; an
// *ModuleRefusal says why nothing was started.
func (u *systemPackageUpdater) startModuleInstall(name, planID string) (SystemModuleStatus, error) {
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
	u.moduleMu.Lock()
	defer u.moduleMu.Unlock()
	env := u.moduleWorld()
	ctx, cancel := context.WithTimeout(context.Background(), systemModuleStartTimeout)
	defer cancel()
	if err := u.moduleBlocked(ctx, env); err != nil {
		return SystemModuleStatus{}, err
	}
	// the plan has its own time, and is not counted in the start's
	planCtx, cancelPlan := context.WithTimeout(context.Background(), systemModulePlanTimeout)
	plan, err := u.makePlan(planCtx, name)
	cancelPlan()
	if err != nil {
		return SystemModuleStatus{}, err
	}
	switch {
	case plan.Refused != "":
		return SystemModuleStatus{}, refuseModule(string(plan.Refused), "The module cannot be installed: "+string(plan.Refused)+".", map[string]any{"checks": plan.Checks})
	case plan.PlanID != planID:
		return SystemModuleStatus{}, refuseModule(ModulePlanChanged, "The plan is no longer the one that was read: read the new plan and confirm again.", nil)
	}
	// a plan takes a while: something may have started meanwhile
	if err := u.moduleBlocked(ctx, env); err != nil {
		return SystemModuleStatus{}, err
	}
	return u.launchModule(support, env, moduleVerbInstall, name, planID)
}

// startModuleRemove starts the unit that removes the module called name.
func (u *systemPackageUpdater) startModuleRemove(name string) (SystemModuleStatus, error) {
	if !osmodule.ValidName(name) {
		return SystemModuleStatus{}, badModuleRequest("name")
	}
	support, err := u.moduleSupport()
	if err != nil {
		return SystemModuleStatus{}, err
	}
	u.moduleMu.Lock()
	defer u.moduleMu.Unlock()
	env := u.moduleWorld()
	ctx, cancel := context.WithTimeout(context.Background(), systemModuleStartTimeout)
	defer cancel()
	if err := u.moduleBlocked(ctx, env); err != nil {
		return SystemModuleStatus{}, err
	}
	switch modulecmd.RemoveRefusal(ctx, env, name) {
	case "":
	case modulecmd.CodeNotInstalled:
		return SystemModuleStatus{}, refuseModule(ModuleNotInstalled, "There is nothing of this module to remove.", nil)
	default:
		return SystemModuleStatus{}, refuseModule(ModuleBusy, "A package manager or the upgrade of the operating system is at work.", nil)
	}
	return u.launchModule(support, env, moduleVerbRemove, name, "")
}

// launchModule writes the first line of the log of the job, starts its unit and says the job is running. A unit that systemd would not start is a log that says so, and nothing was changed.
func (u *systemPackageUpdater) launchModule(support systemPackageSupport, env modulecmd.Env, verb, name, planID string) (SystemModuleStatus, error) {
	failed := func(detail string, err error) (SystemModuleStatus, error) {
		if err != nil {
			logger.Error("a job of a module could not be started", zap.String("module", name), zap.String("verb", verb), zap.Error(err))
		}
		return SystemModuleStatus{}, refuseModule(ModuleStartFailed, detail, nil)
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
	if err := modulecmd.Queue(env, name, nonce, queuedAt); err != nil {
		if errors.Is(err, modulecmd.ErrLockHeld) {
			return SystemModuleStatus{}, refuseModule(ModuleBusy, "Another run of the module assistant is at work.", map[string]any{"reason": "the lock of the module assistant is held"})
		}
		return failed("The log of the job could not be written. Nothing was started.", err)
	}
	if output, err := u.start(support.systemdPath, systemModuleUnit(name), args...); err != nil {
		said := strings.Split(trimSystemPackageOutput(output), "\n")
		if abandonErr := modulecmd.Abandon(env, name, nonce, queuedAt, u.now().UTC(), said); abandonErr != nil {
			logger.Error("the log of a job that was not started could not be closed", zap.String("module", name), zap.Error(abandonErr))
		}
		return failed("The job could not be started. Nothing was changed.", fmt.Errorf("start the job: %s: %w", trimSystemPackageOutput(output), err))
	}
	// what the first line of the log says, without reading it back: the job is queued and its unit is on its way
	job := modulecmd.ReduceLines(name, nonce, []string{strings.TrimSuffix(modulecmd.QueuedLine(name, nonce, queuedAt), "\n")}, modulecmd.UnitActive, queuedAt, time.Time{})
	status := modulecmd.StatusResult{Name: name}
	ctx, cancel := context.WithTimeout(context.Background(), systemModuleLaunchStatusTimeout)
	defer cancel()
	if s, err := modulecmd.StatusOf(ctx, env, name); err == nil {
		status = s
	}
	return SystemModuleStatus{Job: job, Status: status}, nil
}
