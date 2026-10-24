package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"go.uber.org/zap"
)

// The modules of an image-built host ([server] ImageManaged): every module that can run is in the system image already, named in a catalogue the image ships ([server] ModuleCatalogue)
// with source "image". Its unit is in the image too, started at boot only where the module is enabled (ConditionPathExists= on the flag, as units cannot be enabled at runtime on a
// read-only /etc):
//   - install: the flag, then the unit started, then the gateway route waited for
//   - remove: the unit stopped, then no flag; the module's data stays
//   - update: none, the image is the release (up_to_date)
//
// The results are those of the module assistant (casaosModules), so the routes and the Modules page are the same. A job is a goroutine of the core, its state a file next to the flag:
// one that says running and was not this core's is a job a restart interrupted.

const (
	// imageModuleCatalogue is the catalogue where [server] ModuleCatalogue says none (the module assistant's path)
	imageModuleCatalogue = "/usr/share/casaos/modules/catalogue.json"
	// imageModuleState holds <name>.enabled (the flag the unit's condition reads) and <name>.image-job.json
	imageModuleState = "/var/lib/casaos/modules"
	// imageModuleCheckUnit is the plan's check that the module's unit is in the image
	imageModuleCheckUnit = "unit"
)

type imageModules struct {
	catalogue string // the catalogue's path
	state     string // where the flags and the jobs are
	// env is the module assistant's world for what is used of it: Runner (systemctl), Gateway (the route)
	env       modulecmd.Env
	now       func() time.Time
	routeWait time.Duration // how long an install waits for the module's route
	routeTick time.Duration
	unitWait  time.Duration // how long systemctl start / stop may take
	pid       int           // this core

	mu   sync.Mutex
	busy string // the module whose job runs, "" when none: one job at a time
	jobs sync.WaitGroup
}

func newImageModules(catalogue string) *imageModules {
	if catalogue == "" {
		catalogue = imageModuleCatalogue
	}
	return &imageModules{
		catalogue: catalogue, state: imageModuleState, env: modulecmd.Production(), now: time.Now,
		routeWait: time.Minute, routeTick: time.Second, unitWait: 2 * time.Minute, pid: os.Getpid(),
	}
}

// imageJob is the job file: the job as the routes give it, and the core that ran it.
type imageJob struct {
	Job modulecmd.Job `json:"job"`
	PID int           `json:"pid"`
}

func (m *imageModules) flag(name string) string { return filepath.Join(m.state, name+".enabled") }
func (m *imageModules) jobFile(name string) string {
	return filepath.Join(m.state, name+".image-job.json")
}

func (m *imageModules) enabled(name string) bool {
	info, err := os.Lstat(m.flag(name))
	return err == nil && info.Mode().IsRegular()
}

// load reads the catalogue as the module assistant does (a whole, or nothing and why), with one rule more: every entry is a module of the image.
func (m *imageModules) load() (osmodule.Catalogue, modulecmd.CatalogueInfo) {
	f, err := os.Open(m.catalogue)
	if errors.Is(err, fs.ErrNotExist) {
		return osmodule.Catalogue{}, modulecmd.CatalogueInfo{Error: "none"}
	}
	if err != nil {
		return osmodule.Catalogue{}, modulecmd.CatalogueInfo{Error: "unreadable"}
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, osmodule.MaxCatalogueBytes+1))
	if err != nil {
		return osmodule.Catalogue{}, modulecmd.CatalogueInfo{Error: "unreadable"}
	}
	cat, err := osmodule.ParseCatalogue(raw)
	var bad *osmodule.Invalid
	switch {
	case errors.As(err, &bad):
		return osmodule.Catalogue{}, modulecmd.CatalogueInfo{Error: "invalid", Detail: bad.Field + " " + bad.Reason}
	case err != nil:
		return osmodule.Catalogue{}, modulecmd.CatalogueInfo{Error: "invalid"}
	}
	for i, e := range cat.Modules {
		if e.Source != osmodule.SourceImage {
			// an archive to download: the module assistant's, never on an image-built host
			return osmodule.Catalogue{}, modulecmd.CatalogueInfo{Error: "invalid", Detail: "modules." + strconv.Itoa(i) + ".source " + osmodule.ReasonValue}
		}
	}
	return cat, modulecmd.CatalogueInfo{OK: true}
}

func (m *imageModules) ListSystemModules() (modulecmd.ListResult, error) {
	cat, info := m.load()
	res := modulecmd.ListResult{Catalogue: info, Modules: []modulecmd.ListModule{}}
	listed := map[string]bool{}
	for _, e := range cat.Modules {
		tag := e.Tag
		mod := modulecmd.ListModule{
			Name: e.Name, Title: e.Manifest.Title, Description: e.Manifest.Description, Tier: e.Tier, Tag: e.Tag, Catalogue: true,
			// built into the image: for this system
			System: modulecmd.SystemInfo{Supported: true, Reasons: []string{}},
		}
		if m.enabled(e.Name) {
			mod.Installed = &modulecmd.Installed{Version: &tag}
			mod.Update = &modulecmd.UpdateInfo{Installed: tag, Latest: tag}
		}
		res.Modules = append(res.Modules, mod)
		listed[e.Name] = true
	}
	// enabled, and the image no longer has it
	for _, name := range m.enabledNames() {
		if !listed[name] {
			res.Modules = append(res.Modules, modulecmd.ListModule{Name: name, Title: name, Installed: &modulecmd.Installed{}, System: modulecmd.SystemInfo{Reasons: []string{osmodule.CheckCatalogue}}})
		}
	}
	return res, nil
}

func (m *imageModules) enabledNames() []string {
	entries, _ := os.ReadDir(m.state)
	names := []string{}
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".enabled"); ok && osmodule.ValidName(name) && m.enabled(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// plan is the plan of an install or of an update (verb) of the module called name: checks only (nothing to download, nothing for apt), the first that says no is the refusal.
func (m *imageModules) plan(name, verb string) (osmodule.PlanResult, osmodule.Entry, error) {
	if !osmodule.ValidName(name) {
		return osmodule.PlanResult{}, osmodule.Entry{}, badModuleRequest("name")
	}
	cat, _ := m.load()
	entry, found := cat.Find(name)
	plan := osmodule.PlanResult{
		Name: name, Tag: entry.Tag, Tier: entry.Tier, Checks: []osmodule.Check{},
		Packages: osmodule.PlanPackages{ToInstall: []string{}, ToUpgrade: []string{}, Known: true},
	}
	check := func(code string, ok bool, detail string) {
		plan.Checks = append(plan.Checks, osmodule.Check{Code: code, OK: ok, Detail: detail})
		if !ok && plan.Refused == "" {
			plan.Refused = osmodule.Refusal(code)
		}
	}
	check(osmodule.CheckCatalogue, found, map[bool]string{true: "in the image", false: "not in the catalogue of the image"}[found])
	if verb == moduleVerbUpdate {
		plan.Mode = osmodule.ModeUpdate
		installed := m.enabled(name)
		check(osmodule.RefusedNotInstalled, installed, map[bool]string{true: "installed", false: "not_installed"}[installed])
		// the image is the release: what it has is the latest there is
		check(osmodule.RefusedUpToDate, false, "up_to_date")
		plan.Update = &osmodule.UpdateInfo{Installed: entry.Tag, Latest: entry.Tag, Added: []string{}, Dropped: []string{}}
		plan.PlanID = osmodule.UpdatePlanID(name, entry.Tag, entry.Tag, "", "image", "", nil)
		return plan, entry, nil
	}
	check(osmodule.CheckInstalled, !m.enabled(name), "")
	unit := found && m.unitLoaded(entry.Manifest.Unit)
	check(imageModuleCheckUnit, unit, map[bool]string{true: entry.Manifest.Unit, false: "the unit of the module is not in the image"}[unit])
	m.mu.Lock()
	busy := m.busy
	m.mu.Unlock()
	check(osmodule.CheckBusy, busy == "", map[bool]string{true: "", false: "a job of the module " + busy + " is running"}[busy == ""])
	plan.PlanID = osmodule.PlanID(name, entry.Tag, "", "image", "", nil)
	return plan, entry, nil
}

func (m *imageModules) PlanSystemModule(name string) (osmodule.PlanResult, error) {
	plan, _, err := m.plan(name, moduleVerbInstall)
	return plan, err
}

func (m *imageModules) PlanSystemModuleUpdate(name string) (osmodule.PlanResult, error) {
	plan, _, err := m.plan(name, moduleVerbUpdate)
	return plan, err
}

func (m *imageModules) StartSystemModuleInstall(ctx context.Context, name, planID string) (SystemModuleStatus, error) {
	return m.startPlanned(name, planID, moduleVerbInstall)
}

func (m *imageModules) StartSystemModuleUpdate(ctx context.Context, name, planID string) (SystemModuleStatus, error) {
	return m.startPlanned(name, planID, moduleVerbUpdate)
}

func (m *imageModules) startPlanned(name, planID, verb string) (SystemModuleStatus, error) {
	if !modulePlanIDPattern.MatchString(planID) {
		return SystemModuleStatus{}, badModuleRequest("plan_id")
	}
	plan, entry, err := m.plan(name, verb)
	switch {
	case err != nil:
		return SystemModuleStatus{}, err
	case plan.Refused != "":
		return SystemModuleStatus{}, refusalOfPlan(plan, verb)
	case plan.PlanID != planID:
		return SystemModuleStatus{}, refuseModule(ModulePlanChanged, "The plan is no longer the one that was read: read the new plan and confirm again.", nil)
	}
	// (an update is always refused above: up_to_date)
	return m.start(name, modulecmd.KindInstall, func(ctx context.Context, job *modulecmd.Job) {
		m.install(ctx, job, entry)
	})
}

func (m *imageModules) StartSystemModuleRemove(ctx context.Context, name string) (SystemModuleStatus, error) {
	if !osmodule.ValidName(name) {
		return SystemModuleStatus{}, badModuleRequest("name")
	}
	if !m.enabled(name) {
		return SystemModuleStatus{}, refuseModule(ModuleNotInstalled, "There is nothing of this module to remove.", nil)
	}
	cat, _ := m.load()
	entry, found := cat.Find(name)
	return m.start(name, modulecmd.KindRemove, func(ctx context.Context, job *modulecmd.Job) {
		m.remove(ctx, job, entry, found)
	})
}

// start runs the job in a goroutine, one at a time, and gives it as it begins.
func (m *imageModules) start(name, kind string, run func(ctx context.Context, job *modulecmd.Job)) (SystemModuleStatus, error) {
	m.mu.Lock()
	if m.busy != "" {
		busy := m.busy
		m.mu.Unlock()
		return SystemModuleStatus{}, busyModule("A job of the module "+busy+" is running.", "a job of the module "+busy+" is running")
	}
	job := modulecmd.Job{Name: name, Kind: kind, State: modulecmd.JobRunning, StartedAt: m.stamp(), LogTail: []string{}}
	if err := m.writeJob(job); err != nil {
		m.mu.Unlock()
		logger.Error("the job of a module of the image could not be written", zap.String("module", name), zap.Error(err))
		return SystemModuleStatus{}, refuseModule(ModuleStartFailed, "The job could not be written. Nothing was started.", nil)
	}
	m.busy = name
	m.mu.Unlock()

	m.jobs.Add(1)
	go func() {
		defer m.jobs.Done()
		ctx, cancel := context.WithTimeout(context.Background(), m.unitWait+m.routeWait)
		defer cancel()
		done := job
		run(ctx, &done)
		done.EndedAt = m.stamp()
		m.mu.Lock()
		defer m.mu.Unlock()
		if err := m.writeJob(done); err != nil {
			logger.Error("the end of the job of a module of the image could not be written", zap.String("module", name), zap.Error(err))
		}
		m.busy = ""
	}()
	return SystemModuleStatus{Job: job, Status: m.status(name)}, nil
}

func (m *imageModules) install(ctx context.Context, job *modulecmd.Job, entry osmodule.Entry) {
	fail := func(step, code string, undo bool) {
		job.State, job.Step, job.Code = modulecmd.JobFailed, step, code
		if undo {
			job.Undo = modulecmd.UndoRolledBack
			if err := os.Remove(m.flag(entry.Name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				job.Undo = modulecmd.UndoIncomplete
			}
		}
	}
	if err := os.MkdirAll(m.state, 0o755); err != nil {
		fail("enable", "enable_failed", false)
		return
	}
	if err := os.WriteFile(m.flag(entry.Name), nil, 0o644); err != nil {
		fail("enable", "enable_failed", false)
		return
	}
	if err := m.systemctl(ctx, "start", entry.Manifest.Unit); err != nil {
		logger.Error("the unit of a module of the image did not start", zap.String("module", entry.Name), zap.Error(err))
		fail("start", ModuleStartFailed, true)
		return
	}
	for {
		if answers, err := m.env.Gateway.Answers(ctx, entry.Manifest.GatewayRoute); err == nil && answers {
			job.State, job.Step = modulecmd.JobSucceeded, "done"
			return
		}
		select {
		case <-ctx.Done():
			// the unit runs and stays enabled: the route may come later; said as the module assistant says it
			job.State, job.Step, job.Code = modulecmd.JobFailed, modulecmd.StepRoute, modulecmd.CodeRouteTimeout
			return
		case <-time.After(m.routeTick):
		}
	}
}

func (m *imageModules) remove(ctx context.Context, job *modulecmd.Job, entry osmodule.Entry, found bool) {
	if found {
		if err := m.systemctl(ctx, "stop", entry.Manifest.Unit); err != nil {
			logger.Error("the unit of a module of the image did not stop", zap.String("module", entry.Name), zap.Error(err))
			job.State, job.Step, job.Code = modulecmd.JobFailed, "stop", "stop_failed"
			return
		}
	}
	if err := os.Remove(m.flag(job.Name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		job.State, job.Step, job.Code = modulecmd.JobFailed, "disable", "disable_failed"
		return
	}
	job.State, job.Step = modulecmd.JobSucceeded, "done"
}

func (m *imageModules) GetSystemModuleStatus(name string) (SystemModuleStatus, error) {
	if !osmodule.ValidName(name) {
		return SystemModuleStatus{}, badModuleRequest("name")
	}
	return SystemModuleStatus{Job: m.readJob(name), Status: m.status(name)}, nil
}

// status is the module as it is: enabled (the flag), its unit active, its route answering.
func (m *imageModules) status(name string) modulecmd.StatusResult {
	res := modulecmd.StatusResult{Name: name}
	if !m.enabled(name) {
		return res
	}
	res.Installed = true
	cat, _ := m.load()
	entry, found := cat.Find(name)
	if !found {
		return res
	}
	tag := entry.Tag
	res.Version = &tag
	ctx, cancel := context.WithTimeout(context.Background(), systemModuleReadTimeout)
	defer cancel()
	res.Unit = modulecmd.UnitState{Active: m.unitSays(ctx, "is-active", entry.Manifest.Unit, "active"), Enabled: true}
	if answers, err := m.env.Gateway.Answers(ctx, entry.Manifest.GatewayRoute); err == nil {
		res.Route.Answers = answers
	}
	return res
}

func (m *imageModules) readJob(name string) modulecmd.Job {
	idle := modulecmd.Job{Name: name, State: modulecmd.JobIdle, LogTail: []string{}}
	raw, err := os.ReadFile(m.jobFile(name))
	if err != nil {
		return idle
	}
	var saved imageJob
	if json.Unmarshal(raw, &saved) != nil || saved.Job.Name != name {
		return idle
	}
	m.mu.Lock()
	running := m.busy == name
	m.mu.Unlock()
	if saved.Job.State == modulecmd.JobRunning && (saved.PID != m.pid || !running) {
		// a core that restarted in the middle of the job
		saved.Job.State = modulecmd.JobInterrupted
	}
	if saved.Job.LogTail == nil {
		saved.Job.LogTail = []string{}
	}
	return saved.Job
}

func (m *imageModules) writeJob(job modulecmd.Job) error {
	if err := os.MkdirAll(m.state, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(imageJob{Job: job, PID: m.pid})
	if err != nil {
		return err
	}
	tmp := m.jobFile(job.Name) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.jobFile(job.Name))
}

func (m *imageModules) stamp() string { return m.now().UTC().Format(time.RFC3339) }

func (m *imageModules) systemctl(ctx context.Context, verb, unit string) error {
	_, err := m.env.Runner.Run(ctx, modulecmd.Cmd{Argv: []string{"systemctl", verb, unit}, Timeout: m.unitWait})
	return err
}

func (m *imageModules) unitSays(ctx context.Context, verb, unit, word string) bool {
	out, err := m.env.Runner.Run(ctx, modulecmd.Cmd{Argv: []string{"systemctl", verb, unit}, Timeout: 10 * time.Second})
	return err == nil && strings.TrimSpace(out.Text) == word
}

// unitLoaded: systemd has the unit (the image ships it), whatever its state.
func (m *imageModules) unitLoaded(unit string) bool {
	if unit == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := m.env.Runner.Run(ctx, modulecmd.Cmd{Argv: []string{"systemctl", "show", "--property=LoadState", "--value", unit}, Timeout: 10 * time.Second})
	return err == nil && strings.TrimSpace(out.Text) == "loaded"
}
