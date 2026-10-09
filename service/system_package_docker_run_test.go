package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// ownerPlanID is the id of the plan the owner's box offers: what the page echoes back.
func ownerPlanID(t *testing.T) string {
	t.Helper()
	update := checkBox(t, ownerBox(t), nil).Docker.Update
	if update == nil || update.PlanID == "" {
		t.Fatalf("the owner's box has no plan: %#v", update)
	}
	return update.PlanID
}

// started is what the updater handed to systemd-run.
type started struct {
	count int
	path  string
	unit  string
	args  []string
}

type startedBox struct {
	updater *systemPackageUpdater
	box     *aptBox
	started *started
	planID  string
}

// newStartBox is the owner's box with nothing running and no app busy, and a systemd-run that
// takes the unit and records it.
func newStartBox(t *testing.T, tweak func(*aptBox)) *startedBox {
	t.Helper()
	box := ownerBox(t)
	if tweak != nil {
		tweak(box)
	}
	updater := newTestSystemPackageUpdater(t)
	updater.command = box.command
	updater.appOperations = func(context.Context) ([]string, error) { return []string{}, nil }
	s := &started{}
	updater.start = func(path, unit string, args ...string) ([]byte, error) {
		s.count++
		s.path, s.unit, s.args = path, unit, args
		return nil, nil
	}
	return &startedBox{updater: updater, box: box, started: s, planID: ownerPlanID(t)}
}

func (s *startedBox) env(name string) string {
	for _, arg := range s.started.args {
		if value, ok := strings.CutPrefix(arg, "--setenv="+name+"="); ok {
			return value
		}
	}
	return "\x00missing"
}

func (s *startedBox) noLog(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(s.updater.dockerLogPath()); !os.IsNotExist(err) {
		t.Errorf("a log was written for an update that was not started: %v", err)
	}
}

func TestStartDockerUpdateOwnersCase(t *testing.T) {
	s := newStartBox(t, nil)
	status, err := s.updater.startDockerUpdate(s.planID)
	if err != nil {
		t.Fatalf("startDockerUpdate() error = %v", err)
	}
	if status.State != "running" || !status.Supported || status.StartedAt != "2026-08-13T01:02:03Z" || status.Error != "" || status.NotReturned == nil {
		t.Fatalf("status = %#v", status)
	}

	// the log's first line is the core's, with a nonce that only that line and the unit have
	log, err := os.ReadFile(s.updater.dockerLogPath())
	if err != nil {
		t.Fatal(err)
	}
	run := dockerpkg.ParseRun(string(log))
	if run.Nonce == "" || !dockerpkg.ValidNonce(run.Nonce) || string(log) != dockerpkg.QueuedMarker(run.Nonce, s.updater.now()) {
		t.Fatalf("log = %q", log)
	}
	if info, err := os.Stat(s.updater.dockerLogPath()); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("the log's mode = %v, %v: the System packages update's log has 0644", info, err)
	}
	if got, want := s.updater.dockerLogPath(), filepath.Join(filepath.Dir(s.updater.logPath()), "docker-update.log"); got != want {
		t.Errorf("log path = %q, want %q", got, want)
	}

	// systemd-run is given the unit, detached, and every parameter as an environment variable
	if s.started.count != 1 || s.started.path != "/usr/bin/systemd-run" || s.started.unit != "casaos-docker-update.service" {
		t.Fatalf("started = %#v", s.started)
	}
	wantArgs := []string{
		"--quiet", "--no-block", "--collect",
		"--unit=casaos-docker-update.service",
		"--property=Type=exec",
		"--description=CasaOS Docker update",
		"--setenv=DEBIAN_FRONTEND=noninteractive",
		"--setenv=CASAOS_DU_NONCE=" + run.Nonce,
		"--setenv=CASAOS_DU_LOG=" + s.updater.dockerLogPath(),
		"--setenv=CASAOS_DU_APT=/usr/bin/apt-get",
		"--setenv=CASAOS_DU_PINS=containerd.io=2.1.4-1 docker-ce-cli=" + debian29 + " docker-ce=" + debian29,
		"--setenv=CASAOS_DU_NAMES=containerd.io docker-ce docker-ce-cli",
		"--setenv=CASAOS_DU_TO=29.8.0",
		"--setenv=CASAOS_DU_DAEMON_TIMEOUT=300",
		"--setenv=CASAOS_DU_RETURN_TIMEOUT=90",
		"--setenv=CASAOS_DU_POLL=2",
		"/bin/sh", "-c", dockerUpdateScript,
	}
	if !reflect.DeepEqual(s.started.args, wantArgs) {
		t.Errorf("systemd-run arguments:\n%s\nwant:\n%s", strings.Join(s.started.args, "\n"), strings.Join(wantArgs, "\n"))
	}
	// nothing that varies is in the script's text
	script := s.started.args[len(s.started.args)-1]
	for _, data := range []string{run.Nonce, debian28, debian29, "29.8.0", "docker-ce", "containerd", s.updater.dockerLogPath(), "/usr/bin/apt-get"} {
		if strings.Contains(script, data) {
			t.Errorf("the script holds %q", data)
		}
	}

	// the plan was looked at again, and nothing else changed on the box
	if len(engineSimulations(s.box)) != 1 {
		t.Errorf("simulations = %#v", engineSimulations(s.box))
	}
	for _, call := range s.box.calls {
		if strings.Contains(call, "apt-get") && !strings.Contains(call, " -s ") && !strings.HasSuffix(call, "apt-get update") {
			t.Errorf("the start ran %q", call)
		}
	}
}

func TestStartDockerUpdateIsTheSameThroughTheService(t *testing.T) {
	s := newStartBox(t, nil)
	s.updater.appOperations = nil
	system := &systemService{packageUpdates: s.updater}
	// the owner's box can be asked once main tells how
	system.SetAppOperations(func(context.Context) ([]string, error) { return nil, nil })
	if _, err := system.StartDockerUpdate(s.planID); err != nil || s.started.count != 1 {
		t.Fatalf("StartDockerUpdate() error = %v, started %d", err, s.started.count)
	}
	logAgeOf(s.updater, time.Second)
	if status := system.DockerUpdateStatus(); status.State != "finalizing" || status.StartedAt != "2026-08-13T01:02:03Z" {
		t.Errorf("DockerUpdateStatus() = %#v", status)
	}
}

func TestStartDockerUpdateRefusesWhatTheCheckRefuses(t *testing.T) {
	for _, c := range refusalCases() {
		t.Run(c.name, func(t *testing.T) {
			s := newStartBox(t, nil)
			// the box changes after the page's check: the refusal is the start's own look
			if c.change != nil {
				c.change(s.box)
			}
			if c.tweak != nil {
				c.tweak(s.updater)
			}
			status, err := s.updater.startDockerUpdate(s.planID)
			var refusal *DockerUpdateRefusal
			if !errors.As(err, &refusal) || refusal.Code != c.code {
				t.Fatalf("error = %v, want a refusal %q", err, c.code)
			}
			want := c.detail
			if want == nil {
				want = []string{}
			}
			if len(refusal.Detail) != len(want) || (len(want) > 0 && !reflect.DeepEqual(refusal.Detail, want)) {
				t.Errorf("detail = %#v, want %#v", refusal.Detail, want)
			}
			// the reason travels in the data as well as in the error
			if status.ErrorCode != c.code || status.Error == "" || status.Error != refusal.Error() || status.State != "idle" {
				t.Errorf("status = %#v", status)
			}
			if s.started.count != 0 {
				t.Error("a unit was started")
			}
			s.noLog(t)
		})
	}
}

func TestStartDockerUpdateReasonsAreEnglishAndHaveNoDataInThem(t *testing.T) {
	s := newStartBox(t, func(b *aptBox) { b.enginePlans["docker-ce-cli"] += "Remv evil$(reboot) [1]\n" })
	_, err := s.updater.startDockerUpdate(s.planID)
	var refusal *DockerUpdateRefusal
	if !errors.As(err, &refusal) || refusal.Code != "plan" || !reflect.DeepEqual(refusal.Detail, []string{dockerpkg.InvalidName}) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(refusal.Reason, "reboot") || refusal.Reason == "" {
		t.Errorf("reason = %q", refusal.Reason)
	}
}

func TestStartDockerUpdateRefusesABadPlanID(t *testing.T) {
	good := strings.Repeat("a", 64)
	for name, id := range map[string]string{
		"empty":     "",
		"short":     good[:63],
		"long":      good + "a",
		"upper":     strings.Repeat("A", 64),
		"not hex":   strings.Repeat("g", 64),
		"a command": "x; reboot " + good[:54],
		"new line":  good[:63] + "\n",
	} {
		s := newStartBox(t, nil)
		calls := len(s.box.calls)
		_, err := s.updater.startDockerUpdate(id)
		if !errors.Is(err, ErrDockerUpdateBadPlanID) || s.started.count != 0 || len(s.box.calls) != calls {
			t.Errorf("%s: error = %v, started %d, calls %v", name, err, s.started.count, s.box.calls[calls:])
		}
	}
}

func TestStartDockerUpdateSaysChangedForAPlanThatIsNotTheConfirmedOne(t *testing.T) {
	// the page confirmed a plan, and a newer Docker appeared since
	s := newStartBox(t, nil)
	s.box.enginePlans["docker-ce"] = instEngine("docker-ce", debian28, "5:29.9.0-1~debian.11~bullseye")
	status, err := s.updater.startDockerUpdate(s.planID)
	var refusal *DockerUpdateRefusal
	if !errors.As(err, &refusal) || refusal.Code != "changed" || status.ErrorCode != "changed" {
		t.Fatalf("error = %v, status = %#v", err, status)
	}
	if s.started.count != 0 {
		t.Error("a unit was started")
	}
	s.noLog(t)

	// a plan id of the right shape that is nobody's
	s = newStartBox(t, nil)
	if _, err := s.updater.startDockerUpdate(strings.Repeat("0", 64)); !errors.As(err, &refusal) || refusal.Code != "changed" {
		t.Fatalf("error = %v, want changed", err)
	}
}

func TestStartDockerUpdateWithNothingToUpdate(t *testing.T) {
	s := newStartBox(t, nil)
	s.box.enginePlans = map[string]string{}
	s.box.upgradeSimulation = simLibc
	status, err := s.updater.startDockerUpdate(s.planID)
	var refusal *DockerUpdateRefusal
	if !errors.As(err, &refusal) || refusal.Code != "nothing" || status.ErrorCode != "nothing" {
		t.Fatalf("error = %v", err)
	}
	if s.started.count != 0 {
		t.Error("a unit was started")
	}
	s.noLog(t)
}

func TestStartDockerUpdateSaysUnsupportedOnAHostThatCannotUpdate(t *testing.T) {
	s := newStartBox(t, nil)
	s.updater.getEUID = func() int { return 1000 }
	before := len(s.box.calls)
	status, err := s.updater.startDockerUpdate(s.planID)
	if !errors.Is(err, ErrSystemPackageUpdatesUnsupported) || status.Supported || status.ErrorCode != "unsupported" || status.Error == "" {
		t.Fatalf("error = %v, status = %#v", err, status)
	}
	if len(s.box.calls) != before || s.started.count != 0 {
		t.Errorf("something ran: %v", s.box.calls[before:])
	}
	s.noLog(t)
	if got := s.updater.dockerStatus(); got.Supported || got.State != "idle" {
		t.Errorf("status = %#v", got)
	}
}

func TestStartDockerUpdateWaitsForOtherWork(t *testing.T) {
	cases := map[string]struct {
		active map[string]bool
		locked bool
		code   string
	}{
		"the update of Docker":      {map[string]bool{systemDockerUpdateUnit: true}, false, "running"},
		"a System packages update":  {map[string]bool{systemPackageUpdateUnit: true}, false, "running"},
		"a ReCasaOS update":         {map[string]bool{common.UPDATE_UNIT + ".service": true}, false, "maintenance"},
		"a package manager's lock":  {nil, true, "maintenance"},
		"the update of Docker, too": {map[string]bool{systemDockerUpdateUnit: true, common.UPDATE_UNIT + ".service": true}, true, "running"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStartBox(t, func(b *aptBox) { b.activeUnits = c.active })
			s.updater.dpkgLocked = func() bool { return c.locked }
			calls := len(s.box.calls)
			status, err := s.updater.startDockerUpdate(s.planID)
			var refusal *DockerUpdateRefusal
			if !errors.As(err, &refusal) || refusal.Code != c.code || status.ErrorCode != c.code || status.Error == "" {
				t.Fatalf("error = %v, status = %#v, want %s", err, status, c.code)
			}
			if s.started.count != 0 {
				t.Error("a unit was started")
			}
			// refused before the slow part: the plan was not simulated
			for _, call := range s.box.calls[calls:] {
				if strings.Contains(call, " -s ") {
					t.Errorf("the plan was simulated for an update that cannot start: %s", call)
				}
			}
			s.noLog(t)
			if c.code == "running" && c.active[systemDockerUpdateUnit] && status.State != "running" {
				t.Errorf("a Docker update runs and the status says %q", status.State)
			}
		})
	}
}

// Something starts while the plan is simulated: the start looks again once it has the lock.
func TestStartDockerUpdateLooksAgainAfterTheSlowPart(t *testing.T) {
	cases := map[string]struct {
		change func(*startedBox)
		code   string
	}{
		"the update of Docker":     {func(s *startedBox) { s.box.activeUnits[systemDockerUpdateUnit] = true }, "running"},
		"a System packages update": {func(s *startedBox) { s.box.activeUnits[systemPackageUpdateUnit] = true }, "running"},
		"a ReCasaOS update":        {func(s *startedBox) { s.box.activeUnits[common.UPDATE_UNIT+".service"] = true }, "maintenance"},
		"a package manager":        {func(s *startedBox) { s.updater.dpkgLocked = func() bool { return true } }, "maintenance"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStartBox(t, func(b *aptBox) { b.activeUnits = map[string]bool{} })
			simulated := false
			s.box.onInstallSimulation = func() {
				if !simulated {
					simulated = true
					c.change(s)
				}
			}
			_, err := s.updater.startDockerUpdate(s.planID)
			var refusal *DockerUpdateRefusal
			if !errors.As(err, &refusal) || refusal.Code != c.code {
				t.Fatalf("error = %v, want %s", err, c.code)
			}
			if s.started.count != 0 {
				t.Error("a unit was started")
			}
			s.noLog(t)
		})
	}
}

func TestStartDockerUpdateFailsClosedOnApps(t *testing.T) {
	cases := map[string]struct {
		list   AppOperationsFunc
		detail []string
		reason string
	}{
		"apps with an operation": {func(context.Context) ([]string, error) { return []string{"immich", "casaos-system", "immich"}, nil }, []string{"casaos-system", "immich"}, ""},
		"hostile names": {func(context.Context) ([]string, error) {
			return []string{"a b", "x$(reboot)", "<script>", "ok-1", ""}, nil
		}, []string{dockerpkg.InvalidName, "ok-1"}, ""},
		"AppManagement is down": {func(context.Context) ([]string, error) { return nil, errors.New("connection refused") }, nil, "AppManagement did not say"},
		"nobody to ask":         {nil, nil, "AppManagement did not say"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStartBox(t, nil)
			s.updater.appOperations = c.list
			status, err := s.updater.startDockerUpdate(s.planID)
			var refusal *DockerUpdateRefusal
			if !errors.As(err, &refusal) || refusal.Code != "apps" || status.ErrorCode != "apps" || status.State != "idle" {
				t.Fatalf("error = %v, status = %#v", err, status)
			}
			if !reflect.DeepEqual(refusal.Detail, c.detail) || !reflect.DeepEqual(status.RefusalDetail, c.detail) {
				t.Errorf("detail = %#v, %#v, want %#v", refusal.Detail, status.RefusalDetail, c.detail)
			}
			if !strings.Contains(refusal.Reason, c.reason) || strings.Contains(refusal.Reason, "reboot") {
				t.Errorf("reason = %q", refusal.Reason)
			}
			encoded, _ := json.Marshal(status)
			if strings.Contains(string(encoded), "reboot") || strings.Contains(string(encoded), "<script>") {
				t.Errorf("status = %s", encoded)
			}
			if s.started.count != 0 {
				t.Error("a unit was started")
			}
			s.noLog(t)
		})
	}
}

// AppManagement is asked after the plan was simulated, and only for a start that could go on.
func TestStartDockerUpdateAsksTheAppsLast(t *testing.T) {
	s := newStartBox(t, nil)
	var asked int
	s.updater.appOperations = func(context.Context) ([]string, error) {
		asked++
		if n := len(engineSimulations(s.box)); n != 1 {
			t.Errorf("apps asked after %d simulations, want after the one", n)
		}
		return nil, nil
	}
	if _, err := s.updater.startDockerUpdate(s.planID); err != nil || asked != 1 {
		t.Fatalf("error = %v, asked %d", err, asked)
	}

	// a plan that changed, or a refusal, makes the question pointless
	s = newStartBox(t, nil)
	asked = 0
	s.updater.appOperations = func(context.Context) ([]string, error) { asked++; return nil, nil }
	_, _ = s.updater.startDockerUpdate(strings.Repeat("0", 64))
	s.box.dockerCEStatus = "hi"
	_, _ = s.updater.startDockerUpdate(s.planID)
	if asked != 0 {
		t.Errorf("AppManagement was asked %d times for starts that could not go on", asked)
	}
}

func TestStartDockerUpdateWritesAWholeRunWhenSystemdRunFails(t *testing.T) {
	s := newStartBox(t, nil)
	s.updater.start = func(string, string, ...string) ([]byte, error) {
		return []byte("Failed to start transient service unit: Access denied\n"), errors.New("exit status 1")
	}
	status, err := s.updater.startDockerUpdate(s.planID)
	var refusal *DockerUpdateRefusal
	if err == nil || errors.As(err, &refusal) || !strings.Contains(err.Error(), "start Docker update") {
		t.Fatalf("error = %v", err)
	}
	// the log has the core's first line, what systemd said, and a last line with the same nonce
	log, _ := os.ReadFile(s.updater.dockerLogPath())
	run := dockerpkg.ParseRun(string(log))
	if run.Nonce == "" || run.Terminal != dockerpkg.TerminalFailed || run.FailReason != dockerpkg.FailGuard {
		t.Fatalf("run = %#v, log = %q", run, log)
	}
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "CASAOS_DOCKER_UPDATE_QUEUED "+run.Nonce) || !strings.Contains(lines[1], "Access denied") || !strings.HasPrefix(lines[2], "CASAOS_DOCKER_UPDATE_FAILED "+run.Nonce+" 2026-08-13T01:02:03Z guard") {
		t.Errorf("log = %q", log)
	}
	if status.State != "failed" || status.Outcome != "failed" || !strings.Contains(status.Error, "Access denied") || status.CompletedAt == "" {
		t.Errorf("status = %#v", status)
	}
	// and the status that follows says the same
	if again := s.updater.dockerStatus(); again.State != "failed" || again.ErrorCode != "guard" {
		t.Errorf("the status that follows = %#v", again)
	}
}

func TestStartDockerUpdateDoesNotStartWhenTheLogCannotBeWritten(t *testing.T) {
	s := newStartBox(t, nil)
	s.updater.writeFile = func(string, []byte, os.FileMode) error { return errors.New("read-only file system") }
	status, err := s.updater.startDockerUpdate(s.planID)
	if err == nil || s.started.count != 0 || status.State != "failed" || !strings.Contains(status.Error, "read-only") {
		t.Fatalf("error = %v, status = %#v, started %d", err, status, s.started.count)
	}

	s = newStartBox(t, nil)
	s.updater.mkdirAll = func(string, os.FileMode) error { return errors.New("no space left") }
	status, err = s.updater.startDockerUpdate(s.planID)
	if err == nil || s.started.count != 0 || status.State != "failed" {
		t.Fatalf("error = %v, status = %#v, started %d", err, status, s.started.count)
	}
}

// The lock is not held while the plan is simulated, so that the status and the check still
// answer, and it is held from the second look to the start, so that nothing slips in between.
func TestStartDockerUpdateHoldsTheLockOnlyWhereItMust(t *testing.T) {
	s := newStartBox(t, nil)
	var freeWhileSimulating, freeWhileStarting bool
	s.box.onInstallSimulation = func() {
		if freeWhileSimulating = s.updater.mu.TryLock(); freeWhileSimulating {
			s.updater.mu.Unlock()
		}
	}
	s.updater.start = func(string, string, ...string) ([]byte, error) {
		if freeWhileStarting = s.updater.mu.TryLock(); freeWhileStarting {
			s.updater.mu.Unlock()
		}
		return nil, nil
	}
	if _, err := s.updater.startDockerUpdate(s.planID); err != nil {
		t.Fatal(err)
	}
	if !freeWhileSimulating {
		t.Error("the lock was held while the plan was simulated")
	}
	if freeWhileStarting {
		t.Error("the lock was free when the unit was started")
	}
	if !s.updater.mu.TryLock() {
		t.Fatal("the lock is still held after the start")
	}
	s.updater.mu.Unlock()
}

func TestStartDockerUpdateTwiceStartsOnce(t *testing.T) {
	// systemd takes the unit: the second start finds it, and so does the System packages update
	s := newStartBox(t, nil)
	s.updater.start = func(path, unit string, args ...string) ([]byte, error) {
		s.started.count++
		s.box.activeUnits = map[string]bool{unit: true}
		return nil, nil
	}
	if _, err := s.updater.startDockerUpdate(s.planID); err != nil {
		t.Fatal(err)
	}
	_, err := s.updater.startDockerUpdate(s.planID)
	var refusal *DockerUpdateRefusal
	if !errors.As(err, &refusal) || refusal.Code != "running" || s.started.count != 1 {
		t.Fatalf("error = %v, started %d", err, s.started.count)
	}
	if _, err := s.updater.startUpdate(); !errors.Is(err, ErrSystemMaintenanceBusy) {
		t.Fatalf("the System packages update while Docker is updating: error = %v", err)
	}
}

func TestDockerUpdateArgsRefuseWhatIsNotValidated(t *testing.T) {
	nonce := "0123456789abcdef0123456789abcdef"
	pins := []string{"containerd.io=2.1.4-1", "docker-ce-cli=" + debian29, "docker-ce=" + debian29}
	names := []string{"containerd.io", "docker-ce", "docker-ce-cli"}
	if _, err := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", pins, names, "29.8.0"); err != nil {
		t.Fatalf("valid arguments: %v", err)
	}
	if _, err := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", pins[:1], names[:1], ""); err != nil {
		t.Fatalf("no docker-ce in the plan: %v", err)
	}
	for name, c := range map[string]struct {
		nonce, log, apt, to string
		pins, names         []string
	}{
		"a nonce that is not one":        {"abc", "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, names},
		"an upper case nonce":            {strings.ToUpper(nonce), "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, names},
		"a relative log":                 {nonce, "x.log", "/usr/bin/apt-get", "29.8.0", pins, names},
		"a log with a space":             {nonce, "/var/log/x y.log", "/usr/bin/apt-get", "29.8.0", pins, names},
		"a log with a new line":          {nonce, "/var/log/x\n.log", "/usr/bin/apt-get", "29.8.0", pins, names},
		"a log that is not clean":        {nonce, "/var/log/../x.log", "/usr/bin/apt-get", "29.8.0", pins, names},
		"an apt that is not a path":      {nonce, "/var/log/x.log", "apt-get", "29.8.0", pins, names},
		"an engine that is a command":    {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0;reboot", pins, names},
		"no pins":                        {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", nil, nil},
		"pins and names that differ":     {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, names[:2]},
		"a name without a pin":           {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, []string{"containerd.io", "docker-ce", "docker-buildx-plugin"}},
		"a name twice":                   {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, []string{"containerd.io", "docker-ce", "docker-ce"}},
		"a pin that is not the engine's": {nonce, "/var/log/x.log", "/usr/bin/apt-get", "", []string{"libc6=2.35"}, []string{"libc6"}},
		"a pin with a command":           {nonce, "/var/log/x.log", "/usr/bin/apt-get", "", []string{"docker-ce=5:29;reboot"}, []string{"docker-ce"}},
		"a pin with a space":             {nonce, "/var/log/x.log", "/usr/bin/apt-get", "", []string{"docker-ce=5:29 docker-ce-cli=5:29"}, []string{"docker-ce"}},
		"a pin twice":                    {nonce, "/var/log/x.log", "/usr/bin/apt-get", "", []string{"docker-ce=5:29", "docker-ce=5:30"}, []string{"docker-ce", "docker-ce"}},
		"a name with a glob":             {nonce, "/var/log/x.log", "/usr/bin/apt-get", "", []string{"docker-ce=5:29"}, []string{"docker-*"}},
	} {
		if args, err := dockerUpdateArgs(c.nonce, c.log, c.apt, c.pins, c.names, c.to); err == nil {
			t.Errorf("%s: dockerUpdateArgs() = %v, want an error", name, args)
		}
	}
}
