package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
)

// The steps and codes of a job are the module assistant's, so the Modules page says them: install manifest (the flag),
// start, route; remove stop, manifest (no flag). A unit the image does not have is the catalogue's check (not offered).

type imageModules struct {
	catalogue string // the catalogue's path
	state     string // where the flags and the jobs are
	// env: Runner (systemctl, found through PATH: imageRunner) and Gateway (the route), the module
	// assistant's own for the rest
	env modulecmd.Env
	// owner is who must own the catalogue (root): it says which units root starts and stops
	owner     int
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
	env := modulecmd.Production()
	// the assistant finds systemctl in /usr/sbin, /usr/bin, /sbin, /bin only (an archive must not put one in front of
	// it); an image-built host has it elsewhere (NixOS: /run/current-system/sw/bin), and nothing is unpacked here
	env.Runner = imageRunner{}
	return &imageModules{
		catalogue: catalogue, state: imageModuleState, env: env, owner: 0, now: time.Now,
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
//
// The catalogue says which units root starts and stops: an absolute path to a regular file owned by root and writable by
// nobody else, else unreadable (with why in Detail). Opened without blocking (a FIFO there would hold the request).
func (m *imageModules) load() (osmodule.Catalogue, modulecmd.CatalogueInfo) {
	unsafe := func(why string) (osmodule.Catalogue, modulecmd.CatalogueInfo) {
		return osmodule.Catalogue{}, modulecmd.CatalogueInfo{Error: "unreadable", Detail: "catalogue " + why}
	}
	if !filepath.IsAbs(m.catalogue) {
		return unsafe("path_not_absolute")
	}
	f, err := os.OpenFile(m.catalogue, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return osmodule.Catalogue{}, modulecmd.CatalogueInfo{Error: "none"}
	}
	if err != nil {
		return osmodule.Catalogue{}, modulecmd.CatalogueInfo{Error: "unreadable"}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return osmodule.Catalogue{}, modulecmd.CatalogueInfo{Error: "unreadable"}
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !info.Mode().IsRegular():
		return unsafe("not_regular")
	case !ok || int(st.Uid) != m.owner:
		return unsafe("owner")
	case info.Mode().Perm()&0o022 != 0:
		return unsafe("writable")
	}
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
	// code: the check (the page's CHECKS), refusal: the code it refuses with (its REASONS)
	check := func(code, refusal string, ok bool, detail string) {
		plan.Checks = append(plan.Checks, osmodule.Check{Code: code, OK: ok, Detail: detail})
		if !ok && plan.Refused == "" {
			plan.Refused = osmodule.Refusal(refusal)
		}
	}
	// offered: in the catalogue, and its unit in the image
	offered, detail := found, "in the image"
	switch {
	case !found:
		detail = "not in the catalogue of the image"
	case verb == moduleVerbInstall && !m.unitLoaded(entry.Manifest.Unit):
		offered, detail = false, "the unit of the module is not in the image"
	}
	check(osmodule.CheckCatalogue, osmodule.CheckCatalogue, offered, detail)
	if verb == moduleVerbUpdate {
		plan.Mode = osmodule.ModeUpdate
		installed := m.enabled(name)
		check("installed", osmodule.RefusedNotInstalled, installed, map[bool]string{true: "", false: osmodule.RefusedNotInstalled}[installed])
		// the image is the release: what it has is the latest there is
		check("newer", osmodule.RefusedUpToDate, false, osmodule.RefusedUpToDate)
		plan.Update = &osmodule.UpdateInfo{Installed: entry.Tag, Latest: entry.Tag, Added: []string{}, Dropped: []string{}}
		plan.PlanID = osmodule.UpdatePlanID(name, entry.Tag, entry.Tag, "", "image", "", nil)
		return plan, entry, nil
	}
	check(osmodule.CheckInstalled, osmodule.CheckInstalled, !m.enabled(name), "")
	busy := m.running()
	check(osmodule.CheckBusy, osmodule.CheckBusy, busy == "", map[bool]string{true: "", false: "a job of the module " + busy + " is running"}[busy == ""])
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
	return m.start(name, modulecmd.KindInstall, func(job *modulecmd.Job) {
		m.install(job, entry)
	})
}

func (m *imageModules) StartSystemModuleRemove(ctx context.Context, name string) (SystemModuleStatus, error) {
	if !osmodule.ValidName(name) {
		return SystemModuleStatus{}, badModuleRequest("name")
	}
	// a job at work first: its flag may not be written yet
	if busy := m.running(); busy != "" {
		return SystemModuleStatus{}, busyModule("A job of the module "+busy+" is running.", "a job of the module "+busy+" is running")
	}
	if !m.enabled(name) {
		return SystemModuleStatus{}, refuseModule(ModuleNotInstalled, "There is nothing of this module to remove.", nil)
	}
	// which unit to stop is the catalogue's: one that cannot be read refuses (the unit would keep running with
	// no flag); only an entry gone from a catalogue that is read is the flag alone
	cat, info := m.load()
	if !info.OK {
		return SystemModuleStatus{}, refuseModule(osmodule.CheckCatalogue, "The catalogue of the system cannot be read ("+info.Error+"), so the service of the module cannot be told. Nothing was changed.", map[string]any{"catalogue": info})
	}
	entry, found := cat.Find(name)
	return m.start(name, modulecmd.KindRemove, func(job *modulecmd.Job) {
		m.remove(job, entry, found)
	})
}

// running is the module whose job runs, "" when none.
func (m *imageModules) running() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy
}

// start runs the job in a goroutine, one at a time, and gives it as it begins.
func (m *imageModules) start(name, kind string, run func(job *modulecmd.Job)) (SystemModuleStatus, error) {
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
		done := job
		run(&done)
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

// install: the flag (step manifest: the module recorded), the unit started (start), its route waited for (route), each
// with its own deadline.
func (m *imageModules) install(job *modulecmd.Job, entry osmodule.Entry) {
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
		fail("manifest", "manifest_write_failed", false)
		return
	}
	if err := os.WriteFile(m.flag(entry.Name), nil, 0o644); err != nil {
		fail("manifest", "manifest_write_failed", false)
		return
	}
	if err := m.systemctl("start", entry.Manifest.Unit); err != nil {
		logger.Error("the unit of a module of the image did not start", zap.String("module", entry.Name), zap.Error(err))
		fail("start", ModuleStartFailed, true)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.routeWait)
	defer cancel()
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

// remove: the unit stopped (step stop), then the flag (manifest: the module forgotten).
func (m *imageModules) remove(job *modulecmd.Job, entry osmodule.Entry, found bool) {
	if found {
		if err := m.systemctl("stop", entry.Manifest.Unit); err != nil {
			logger.Error("the unit of a module of the image did not stop", zap.String("module", entry.Name), zap.Error(err))
			job.State, job.Step, job.Code = modulecmd.JobFailed, "stop", "unit_not_stopped"
			return
		}
	}
	if err := os.Remove(m.flag(job.Name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		job.State, job.Step, job.Code = modulecmd.JobFailed, "manifest", "remove_incomplete"
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
	// under the lock the job's end is written with
	m.mu.Lock()
	running := m.busy == name
	raw, err := os.ReadFile(m.jobFile(name))
	m.mu.Unlock()
	if err != nil {
		return idle
	}
	var saved imageJob
	if json.Unmarshal(raw, &saved) != nil || saved.Job.Name != name {
		return idle
	}
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

func (m *imageModules) systemctl(verb, unit string) error {
	ctx, cancel := context.WithTimeout(context.Background(), m.unitWait)
	defer cancel()
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

// imageRunner runs a command found through PATH (the core's unit sets it: the session's tools), bounded by Cmd.Timeout,
// its output kept up to 64 KiB.
type imageRunner struct{}

func (imageRunner) Run(ctx context.Context, c modulecmd.Cmd) (modulecmd.Output, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	program, err := exec.LookPath(c.Argv[0])
	if err != nil {
		return modulecmd.Output{}, err
	}
	cmd := exec.CommandContext(ctx, program, c.Argv[1:]...)
	out, err := cmd.CombinedOutput()
	if len(out) > 64<<10 {
		out = out[len(out)-64<<10:]
	}
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return modulecmd.Output{Text: string(out)}, err
}
