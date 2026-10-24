package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

const imageManifest = `{"schema":1,"name":"NAME","title":"Virtual machines","description":"VMs.","component":"CasaOS-VirtManagement",` +
	`"unit":"casaos-NAME.service","gateway_route":"/v2/NAME","packages":{},"min_recasaos":"0.5.23","os":["debian:12"]}`

func imageEntry(name, source string) string {
	assets := ``
	if source != osmodule.SourceImage {
		assets = `"assets":{"amd64":{"url":"https://github.com/ReCasaOS/CasaOS-VirtManagement/releases/download/v0.1.0/linux-amd64-casaos-virt-management-v0.1.0.tar.gz","sha256":"` + strings.Repeat("0", 64) + `"}},`
	} else {
		assets = `"source":"image",`
	}
	return `{"name":"` + name + `","component":"CasaOS-VirtManagement","tier":"core","tag":"v0.1.0","commit":"` + strings.Repeat("a", 40) + `",` + assets +
		`"manifest":` + strings.ReplaceAll(imageManifest, "NAME", name) + `}`
}

// fakeSystemd answers systemctl: the units it has, which are active, what start and stop do
type fakeSystemd struct {
	mu       sync.Mutex
	loaded   map[string]bool
	active   map[string]bool
	startErr error
	stopErr  error
	hold     chan struct{} // a start waits for it, when set
	calls    []string
}

func (f *fakeSystemd) Run(ctx context.Context, c modulecmd.Cmd) (modulecmd.Output, error) {
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(c.Argv, " "))
	hold := f.hold
	f.mu.Unlock()
	verb, unit := c.Argv[1], c.Argv[len(c.Argv)-1]
	switch verb {
	case "show":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.loaded[unit] {
			return modulecmd.Output{Text: "loaded\n"}, nil
		}
		return modulecmd.Output{Text: "not-found\n"}, nil
	case "is-active":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.active[unit] {
			return modulecmd.Output{Text: "active\n"}, nil
		}
		return modulecmd.Output{Text: "inactive\n"}, errors.New("exit status 3")
	case "start":
		if hold != nil {
			<-hold
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.startErr != nil {
			return modulecmd.Output{}, f.startErr
		}
		f.active[unit] = true
	case "stop":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.stopErr != nil {
			return modulecmd.Output{}, f.stopErr
		}
		f.active[unit] = false
	}
	return modulecmd.Output{}, nil
}

type fakeRoutes struct{ answers func(route string) bool }

func (f fakeRoutes) Answers(_ context.Context, route string) (bool, error) {
	return f.answers(route), nil
}

func newTestImageModules(t *testing.T, entries ...string) (*imageModules, *fakeSystemd) {
	t.Helper()
	dir := t.TempDir()
	catalogue := filepath.Join(dir, "catalogue.json")
	if err := os.WriteFile(catalogue, []byte(`{"schema":1,"modules":[`+strings.Join(entries, ",")+`]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	systemd := &fakeSystemd{loaded: map[string]bool{"casaos-virt.service": true}, active: map[string]bool{}}
	m := newImageModules(catalogue)
	m.state = filepath.Join(dir, "state")
	m.env = modulecmd.Env{Runner: systemd, Gateway: fakeRoutes{func(route string) bool {
		systemd.mu.Lock()
		defer systemd.mu.Unlock()
		return systemd.active["casaos-"+strings.TrimPrefix(route, "/v2/")+".service"]
	}}}
	m.routeWait, m.routeTick = 2*time.Second, 10*time.Millisecond
	m.owner = os.Getuid() // the test's catalogue is the test user's
	return m, systemd
}

func installImageModule(t *testing.T, m *imageModules, name string) SystemModuleStatus {
	t.Helper()
	plan, err := m.PlanSystemModule(name)
	if err != nil || plan.Refused != "" {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	status, err := m.StartSystemModuleInstall(context.Background(), name, plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	m.jobs.Wait()
	return status
}

func TestTheImageListsItsModulesAndWhichAreEnabled(t *testing.T) {
	m, _ := newTestImageModules(t, imageEntry("virt", osmodule.SourceImage), imageEntry("media", osmodule.SourceImage))
	_ = os.MkdirAll(m.state, 0o755)
	_ = os.WriteFile(m.flag("virt"), nil, 0o644)
	_ = os.WriteFile(m.flag("gone"), nil, 0o644) // enabled, and the image no longer has it

	list, err := m.ListSystemModules()
	if err != nil || !list.Catalogue.OK {
		t.Fatalf("%+v %v", list, err)
	}
	byName := map[string]modulecmd.ListModule{}
	for _, mod := range list.Modules {
		byName[mod.Name] = mod
	}
	if v := byName["virt"]; v.Installed == nil || *v.Installed.Version != "v0.1.0" || !v.System.Supported || v.Update == nil || v.Update.Available {
		t.Errorf("virt: %+v", v)
	}
	if o := byName["media"]; o.Installed != nil || !o.Catalogue {
		t.Errorf("media: %+v", o)
	}
	if g := byName["gone"]; g.Installed == nil || g.Catalogue || len(g.System.Reasons) != 1 {
		t.Errorf("gone: %+v", g)
	}
}

func TestTheImageProviderReadsOnlyModulesOfTheImage(t *testing.T) {
	m, _ := newTestImageModules(t, imageEntry("virt", osmodule.SourceArchive))
	list, _ := m.ListSystemModules()
	if list.Catalogue.OK || list.Catalogue.Error != "invalid" || list.Catalogue.Detail != "modules.0.source value" || len(list.Modules) != 0 {
		t.Errorf("an archive in the image's catalogue: %+v", list)
	}
	m.catalogue = filepath.Join(t.TempDir(), "none.json")
	if list, _ := m.ListSystemModules(); list.Catalogue.Error != "none" {
		t.Errorf("no catalogue: %+v", list.Catalogue)
	}
}

func TestAnInstallEnablesAndStartsTheUnitOfTheImage(t *testing.T) {
	m, systemd := newTestImageModules(t, imageEntry("virt", osmodule.SourceImage))
	status := installImageModule(t, m, "virt")
	if status.Job.State != modulecmd.JobRunning || status.Job.Kind != modulecmd.KindInstall {
		t.Errorf("the answer of the start: %+v", status.Job)
	}
	if !m.enabled("virt") {
		t.Error("no flag")
	}
	got, err := m.GetSystemModuleStatus("virt")
	if err != nil {
		t.Fatal(err)
	}
	if got.Job.State != modulecmd.JobSucceeded || got.Job.Step != "done" || got.Job.EndedAt == "" {
		t.Errorf("job: %+v", got.Job)
	}
	if s := got.Status; !s.Installed || *s.Version != "v0.1.0" || !s.Unit.Active || !s.Unit.Enabled || !s.Route.Answers {
		t.Errorf("status: %+v", s)
	}
	if !strings.Contains(strings.Join(systemd.calls, "\n"), "systemctl start casaos-virt.service") {
		t.Errorf("calls: %v", systemd.calls)
	}
	// installed now: a second plan says so
	if plan, _ := m.PlanSystemModule("virt"); plan.Refused != osmodule.CheckInstalled {
		t.Errorf("plan of an installed module: %+v", plan)
	}
}

func TestAnInstallWhoseUnitDoesNotStartTakesTheFlagBack(t *testing.T) {
	m, systemd := newTestImageModules(t, imageEntry("virt", osmodule.SourceImage))
	systemd.startErr = errors.New("exit status 1")
	installImageModule(t, m, "virt")
	got, _ := m.GetSystemModuleStatus("virt")
	if got.Job.State != modulecmd.JobFailed || got.Job.Code != ModuleStartFailed || got.Job.Undo != modulecmd.UndoRolledBack {
		t.Errorf("job: %+v", got.Job)
	}
	if m.enabled("virt") {
		t.Error("the flag stayed: the unit would start at the next boot")
	}
}

func TestAnInstallWhoseRouteNeverAnswersSaysSo(t *testing.T) {
	m, _ := newTestImageModules(t, imageEntry("virt", osmodule.SourceImage))
	m.env.Gateway = fakeRoutes{func(string) bool { return false }}
	m.routeWait = 50 * time.Millisecond
	installImageModule(t, m, "virt")
	got, _ := m.GetSystemModuleStatus("virt")
	if got.Job.State != modulecmd.JobFailed || got.Job.Code != modulecmd.CodeRouteTimeout || !m.enabled("virt") {
		t.Errorf("job: %+v, enabled %v", got.Job, m.enabled("virt"))
	}
}

func TestThePlanSaysWhatTheImageDoesNotHave(t *testing.T) {
	m, systemd := newTestImageModules(t, imageEntry("virt", osmodule.SourceImage))
	if plan, _ := m.PlanSystemModule("nothing"); plan.Refused != osmodule.CheckCatalogue {
		t.Errorf("not in the catalogue: %+v", plan)
	}
	systemd.loaded = map[string]bool{}
	if plan, _ := m.PlanSystemModule("virt"); plan.Refused != osmodule.CheckCatalogue {
		t.Errorf("no unit in the image: %+v", plan)
	}
	if _, err := m.PlanSystemModule("../x"); err == nil {
		t.Error("a name that is not a module's")
	}
	if plan, _ := m.PlanSystemModule("virt"); len(plan.Packages.ToInstall) != 0 || !plan.Packages.Known || plan.Archive.URL != "" {
		t.Errorf("nothing to download or install: %+v", plan)
	}
}

func TestTheImageIsTheReleaseNoUpdate(t *testing.T) {
	m, _ := newTestImageModules(t, imageEntry("virt", osmodule.SourceImage))
	installImageModule(t, m, "virt")
	plan, err := m.PlanSystemModuleUpdate("virt")
	if err != nil || plan.Refused != osmodule.RefusedUpToDate || plan.Mode != osmodule.ModeUpdate {
		t.Fatalf("%+v %v", plan, err)
	}
	_, err = m.StartSystemModuleUpdate(context.Background(), "virt", plan.PlanID)
	var refusal *ModuleRefusal
	if !errors.As(err, &refusal) || refusal.Code != osmodule.RefusedUpToDate {
		t.Errorf("start of an update: %v", err)
	}
}

func TestARemoveStopsTheUnitAndTakesTheFlag(t *testing.T) {
	m, systemd := newTestImageModules(t, imageEntry("virt", osmodule.SourceImage))
	if _, err := m.StartSystemModuleRemove(context.Background(), "virt"); err == nil {
		t.Error("a remove of what is not installed")
	}
	installImageModule(t, m, "virt")
	if _, err := m.StartSystemModuleRemove(context.Background(), "virt"); err != nil {
		t.Fatal(err)
	}
	m.jobs.Wait()
	got, _ := m.GetSystemModuleStatus("virt")
	if got.Job.State != modulecmd.JobSucceeded || got.Job.Kind != modulecmd.KindRemove || got.Status.Installed || m.enabled("virt") || systemd.active["casaos-virt.service"] {
		t.Errorf("after the remove: %+v", got)
	}
}

func TestOneJobAtATimeAndARestartInterruptsIt(t *testing.T) {
	m, systemd := newTestImageModules(t, imageEntry("virt", osmodule.SourceImage))
	systemd.hold = make(chan struct{})
	plan, _ := m.PlanSystemModule("virt")
	if _, err := m.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID); err != nil {
		t.Fatal(err)
	}
	if plan, _ := m.PlanSystemModule("virt"); plan.Refused == "" {
		t.Error("a plan while a job runs")
	}
	var refusal *ModuleRefusal
	if _, err := m.StartSystemModuleRemove(context.Background(), "virt"); !errors.As(err, &refusal) || refusal.Code != ModuleBusy {
		t.Errorf("a second job: %v", err)
	}
	if got, _ := m.GetSystemModuleStatus("virt"); got.Job.State != modulecmd.JobRunning {
		t.Errorf("while it runs: %+v", got.Job)
	}
	// another core reads the job this one left running
	other := &imageModules{state: m.state, pid: m.pid + 1}
	if got := other.readJob("virt"); got.State != modulecmd.JobInterrupted {
		t.Errorf("after a restart: %+v", got)
	}
	close(systemd.hold)
	m.jobs.Wait()
}

func TestImageManagedPicksTheImageProvider(t *testing.T) {
	previous := *config.ServerInfo
	t.Cleanup(func() { *config.ServerInfo = previous })
	s := &systemService{}
	if _, ok := s.modules().(casaosModules); !ok {
		t.Errorf("default: %T", s.modules())
	}
	config.ServerInfo.ImageManaged = true
	config.ServerInfo.ModuleCatalogue = "/run/image/catalogue.json"
	if p, ok := s.modules().(*imageModules); !ok || p.catalogue != "/run/image/catalogue.json" {
		t.Errorf("image-managed: %T", s.modules())
	}
}

// a remove needs the catalogue to know which unit to stop: one that cannot be read refuses, the flag stays
func TestARemoveWithoutAReadableCatalogueChangesNothing(t *testing.T) {
	m, systemd := newTestImageModules(t, imageEntry("virt", osmodule.SourceImage))
	installImageModule(t, m, "virt")
	if err := os.WriteFile(m.catalogue, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	var refusal *ModuleRefusal
	if _, err := m.StartSystemModuleRemove(context.Background(), "virt"); !errors.As(err, &refusal) || refusal.Code != osmodule.CheckCatalogue {
		t.Fatalf("remove with an invalid catalogue: %v", err)
	}
	if !m.enabled("virt") || !systemd.active["casaos-virt.service"] {
		t.Error("the flag or the unit changed")
	}
	// the entry gone from a catalogue that is read: the flag alone
	if err := os.WriteFile(m.catalogue, []byte(`{"schema":1,"modules":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartSystemModuleRemove(context.Background(), "virt"); err != nil {
		t.Fatal(err)
	}
	m.jobs.Wait()
	if m.enabled("virt") {
		t.Error("the flag stayed")
	}
}

// the route is waited for with its own deadline, not the unit's
func TestTheRouteWaitHasItsOwnDeadline(t *testing.T) {
	m, _ := newTestImageModules(t, imageEntry("virt", osmodule.SourceImage))
	m.env.Gateway = fakeRoutes{func(string) bool { return false }}
	m.unitWait, m.routeWait = time.Hour, 50*time.Millisecond
	began := time.Now()
	installImageModule(t, m, "virt")
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("the route wait took %v", took)
	}
}

// the catalogue says which units root starts: an absolute path to a regular file, owned by root (here: the test user),
// writable by nobody else; a FIFO does not block the request
func TestTheCatalogueMustBeSafe(t *testing.T) {
	m, _ := newTestImageModules(t, imageEntry("virt", osmodule.SourceImage))
	good := m.catalogue
	detail := func() string {
		t.Helper()
		var list modulecmd.ListResult
		done := make(chan struct{})
		go func() { list, _ = m.ListSystemModules(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the list blocked")
		}
		return list.Catalogue.Error + " " + list.Catalogue.Detail
	}
	m.catalogue = "catalogue.json"
	if got := detail(); got != "unreadable catalogue path_not_absolute" {
		t.Errorf("relative: %q", got)
	}
	m.catalogue = good
	if err := os.Chmod(good, 0o664); err != nil {
		t.Fatal(err)
	}
	if got := detail(); got != "unreadable catalogue writable" {
		t.Errorf("group-writable: %q", got)
	}
	_ = os.Chmod(good, 0o644)
	m.owner = os.Getuid() + 1
	if got := detail(); got != "unreadable catalogue owner" {
		t.Errorf("another owner: %q", got)
	}
	m.owner = os.Getuid()
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	m.catalogue = fifo
	if got := detail(); got != "unreadable catalogue not_regular" {
		t.Errorf("a FIFO: %q", got)
	}
}

// the codes of a job are the module assistant's, which the Modules page says
func TestTheJobsSpeakTheAssistantsCodes(t *testing.T) {
	m, systemd := newTestImageModules(t, imageEntry("virt", osmodule.SourceImage))
	installImageModule(t, m, "virt")
	systemd.mu.Lock()
	systemd.stopErr = errors.New("exit status 1")
	systemd.mu.Unlock()
	if _, err := m.StartSystemModuleRemove(context.Background(), "virt"); err != nil {
		t.Fatal(err)
	}
	m.jobs.Wait()
	if got, _ := m.GetSystemModuleStatus("virt"); got.Job.Step != "stop" || got.Job.Code != "unit_not_stopped" {
		t.Errorf("a unit that does not stop: %+v", got.Job)
	}
}

// commands are found through PATH (the unit's), not in the assistant's fixed directories
func TestTheImageRunnerFindsThroughPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte("#!/bin/sh\necho \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	out, err := imageRunner{}.Run(context.Background(), modulecmd.Cmd{Argv: []string{"systemctl", "is-active", "x.service"}, Timeout: 5 * time.Second})
	if err != nil || strings.TrimSpace(out.Text) != "is-active x.service" {
		t.Fatalf("%q %v", out.Text, err)
	}
}
