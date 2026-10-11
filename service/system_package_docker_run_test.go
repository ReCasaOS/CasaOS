package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
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
	status, err := s.updater.startDockerUpdate(s.planID, false)
	if err != nil {
		t.Fatalf("startDockerUpdate() error = %v", err)
	}
	if status.State != "running" || !status.Supported || status.StartedAt != "2026-08-13T01:02:03Z" || status.Error != "" || status.NotReturned == nil {
		t.Fatalf("status = %#v", status)
	}
	// the unit has not written anything yet: it is getting ready, as the status says of it
	if status.Phase != "preparing" {
		t.Errorf("phase = %q", status.Phase)
	}

	// the log's first line is the core's, with a nonce that only that line and the unit have; the
	// second is the plan: Docker 28 to 29 is a major jump
	log, err := os.ReadFile(s.updater.dockerLogPath())
	if err != nil {
		t.Fatal(err)
	}
	run := dockerpkg.ParseRun(string(log))
	if run.Nonce == "" || !dockerpkg.ValidNonce(run.Nonce) || string(log) != dockerpkg.QueuedMarker(run.Nonce, s.updater.now())+dockerpkg.PlanMarker(run.Nonce, "28.0.4", "29.8.0", true) {
		t.Fatalf("log = %q", log)
	}
	if status.Log != string(log) || !status.MajorJump || status.From != "28.0.4" || status.To != "29.8.0" || status.RollbackCommand != "" {
		t.Errorf("the status of the start = %#v: want the log as it is, a major jump, 28.0.4 to 29.8.0", status)
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
		"/bin/sh", "-c", dockerUpdateScriptArg(),
	}
	if !reflect.DeepEqual(s.started.args, wantArgs) {
		t.Errorf("systemd-run arguments:\n%s\nwant:\n%s", strings.Join(s.started.args, "\n"), strings.Join(wantArgs, "\n"))
	}
	// nothing that varies is in the script's text: not the nonce, a version, a path, nor the name of a
	// package, except in engine_name, which is the engine's own list of names (the unit checks the names of
	// the holds it is given against it, and a test holds that list to dockerpkg.EngineNames)
	script := s.started.args[len(s.started.args)-1]
	bare := regexp.MustCompile(`(?s)engine_name\(\) \{.*?\n\}\n`).ReplaceAllString(script, "")
	if bare == script {
		t.Error("the script has no engine_name")
	}
	for _, data := range []string{run.Nonce, debian28, debian29, "29.8.0", "docker-ce", "containerd", s.updater.dockerLogPath(), "/usr/bin/apt-get"} {
		if strings.Contains(bare, data) {
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

// newStartBoxWithDependencies is the owner's box as it really is, where Docker 29 brings nftables
// and its libraries, with the plan the page confirmed.
func newStartBoxWithDependencies(t *testing.T) *startedBox {
	t.Helper()
	s := newStartBox(t, func(b *aptBox) {
		b.upgradeSimulation = simLibc
		b.enginePlans["containerd.io"] = ownerNewDependencies + ownerContaind
	})
	update := checkBox(t, ownerBoxWithDependencies(t), nil).Docker.Update
	if update == nil || update.PlanID == "" || update.PlanID == s.planID {
		t.Fatalf("update = %#v: want a plan of its own", update)
	}
	s.planID = update.PlanID
	return s
}

func TestStartDockerUpdateWithTheNewDependencies(t *testing.T) {
	s := newStartBoxWithDependencies(t)
	if _, err := s.updater.startDockerUpdate(s.planID, false); err != nil {
		t.Fatalf("startDockerUpdate() error = %v", err)
	}
	if s.started.count != 1 {
		t.Fatalf("started %d units", s.started.count)
	}
	// the pins are the upgrades: apt is never told to install a package that is not on the box
	if got, want := s.env("CASAOS_DU_PINS"), "containerd.io=2.1.4-1 docker-ce-cli="+debian29+" docker-ce="+debian29; got != want {
		t.Errorf("CASAOS_DU_PINS = %q, want %q", got, want)
	}
	// the names are every package that may be installed, the new ones too, sorted
	if got, want := s.env("CASAOS_DU_NAMES"), "containerd.io docker-ce docker-ce-cli libedit2 libjansson4 libnftables1 nftables"; got != want {
		t.Errorf("CASAOS_DU_NAMES = %q, want %q", got, want)
	}
	if got := s.env("CASAOS_DU_TO"); got != "29.8.0" {
		t.Errorf("CASAOS_DU_TO = %q", got)
	}
	// the script is still the constant one, as systemd is to be given it
	if script := s.started.args[len(s.started.args)-1]; script != dockerUpdateScriptArg() {
		t.Error("the script is not the constant")
	}
}

func TestStartDockerUpdateSaysChangedWhenTheDependenciesAreNotTheConfirmedOnes(t *testing.T) {
	var refusal *DockerUpdateRefusal

	// confirmed without nftables, and Docker now needs it: the owner reconfirms
	s := newStartBoxWithDependencies(t)
	s.planID = ownerPlanID(t)
	status, err := s.updater.startDockerUpdate(s.planID, false)
	if !errors.As(err, &refusal) || refusal.Code != "changed" || status.ErrorCode != "changed" || s.started.count != 0 {
		t.Fatalf("a dependency appeared: error = %v, status = %#v, started %d", err, status, s.started.count)
	}
	s.noLog(t)

	// confirmed with the dependencies, and one is gone
	s = newStartBoxWithDependencies(t)
	s.box.enginePlans["containerd.io"] = strings.Replace(ownerNewDependencies, "Inst libedit2 (3.1-20191231-2+b1 Debian:11.11/oldstable [amd64])\n", "", 1) + ownerContaind
	if _, err := s.updater.startDockerUpdate(s.planID, false); !errors.As(err, &refusal) || refusal.Code != "changed" || s.started.count != 0 {
		t.Fatalf("a dependency went: error = %v, started %d", err, s.started.count)
	}

	// ... and one has a newer version
	s = newStartBoxWithDependencies(t)
	s.box.enginePlans["containerd.io"] = strings.Replace(ownerNewDependencies, "Inst nftables (0.9.8-3.1+deb11u2", "Inst nftables (0.9.8-3.1+deb11u3", 1) + ownerContaind
	if _, err := s.updater.startDockerUpdate(s.planID, false); !errors.As(err, &refusal) || refusal.Code != "changed" || s.started.count != 0 {
		t.Fatalf("a dependency moved: error = %v, started %d", err, s.started.count)
	}

	// and a dependency that Docker's update must not bring is a refusal of the plan, not a change
	s = newStartBoxWithDependencies(t)
	s.box.enginePlans["containerd.io"] += "Inst docker.io (26.1.5 Debian:11/stable [amd64])\n"
	if _, err := s.updater.startDockerUpdate(s.planID, false); !errors.As(err, &refusal) || refusal.Code != "plan" || !reflect.DeepEqual(refusal.Detail, []string{"docker.io"}) || s.started.count != 0 {
		t.Fatalf("a docker.io: error = %v, started %d", err, s.started.count)
	}
}

func TestStartDockerUpdateIsTheSameThroughTheService(t *testing.T) {
	s := newStartBox(t, nil)
	s.updater.appOperations = nil
	system := &systemService{packageUpdates: s.updater}
	// the owner's box can be asked once main tells how
	system.SetAppOperations(func(context.Context) ([]string, error) { return nil, nil })
	if _, err := system.StartDockerUpdate(s.planID, false); err != nil || s.started.count != 1 {
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
			status, err := s.updater.startDockerUpdate(s.planID, false)
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
	_, err := s.updater.startDockerUpdate(s.planID, false)
	var refusal *DockerUpdateRefusal
	if !errors.As(err, &refusal) || refusal.Code != "plan" || !reflect.DeepEqual(refusal.Detail, []string{dockerpkg.InvalidName}) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(refusal.Reason, "reboot") || refusal.Reason == "" {
		t.Errorf("reason = %q", refusal.Reason)
	}
}

func TestStartDockerUpdateRefusesAnUnfinishedPackageOperationUntilItIsFinished(t *testing.T) {
	const reason = "A previous package operation was left unfinished."
	for name, c := range map[string]struct {
		change func(*aptBox)
		tweak  func(*systemPackageUpdater)
	}{
		"dpkg --audit has something to say": {change: func(b *aptBox) { b.dpkgAudit = dpkgAuditHalfConfigured }},
		"dpkg's journal holds a number":     {tweak: dpkgJournal("0001")},
	} {
		// the page's check saw a clean box, and the box was interrupted since
		s := newStartBox(t, nil)
		if c.change != nil {
			c.change(s.box)
		}
		if c.tweak != nil {
			c.tweak(s.updater)
		}
		status, err := s.updater.startDockerUpdate(s.planID, false)
		var refusal *DockerUpdateRefusal
		if !errors.As(err, &refusal) || refusal.Code != "dpkg" || refusal.Reason != reason || len(refusal.Detail) != 0 {
			t.Errorf("%s: error = %v, want the refusal dpkg with its reason", name, err)
			continue
		}
		// the reason travels in the data too, and there is no detail to show
		if status.ErrorCode != "dpkg" || status.Error != reason || len(status.RefusalDetail) != 0 || status.State != "idle" {
			t.Errorf("%s: status = %#v", name, status)
		}
		if s.started.count != 0 {
			t.Errorf("%s: a unit was started", name)
		}
		s.noLog(t)

		// finished (dpkg --configure -a): the same plan starts, with no other change
		s.box.dpkgAudit = ""
		s.updater.readDir = nil
		if _, err := s.updater.startDockerUpdate(s.planID, false); err != nil || s.started.count != 1 {
			t.Errorf("%s: after the repair: error = %v, started %d", name, err, s.started.count)
		}
	}
}

func TestStartDockerUpdateSaysMaintenanceAndNotDpkgWhileAPackageManagerWorks(t *testing.T) {
	// what a running apt shows to dpkg --audit is not an interrupted operation
	s := newStartBox(t, nil)
	s.box.dpkgAudit = dpkgAuditHalfConfigured
	s.updater.dpkgLocked = func() bool { return true }
	_, err := s.updater.startDockerUpdate(s.planID, false)
	var refusal *DockerUpdateRefusal
	if !errors.As(err, &refusal) || refusal.Code != "maintenance" || s.started.count != 0 {
		t.Fatalf("error = %v, started %d, want the refusal maintenance", err, s.started.count)
	}
	s.noLog(t)
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
		_, err := s.updater.startDockerUpdate(id, false)
		if !errors.Is(err, ErrDockerUpdateBadPlanID) || s.started.count != 0 || len(s.box.calls) != calls {
			t.Errorf("%s: error = %v, started %d, calls %v", name, err, s.started.count, s.box.calls[calls:])
		}
	}
}

func TestStartDockerUpdateSaysChangedForAPlanThatIsNotTheConfirmedOne(t *testing.T) {
	// the page confirmed a plan, and a newer Docker appeared since
	s := newStartBox(t, nil)
	s.box.enginePlans["docker-ce"] = instEngine("docker-ce", debian28, "5:29.9.0-1~debian.11~bullseye")
	status, err := s.updater.startDockerUpdate(s.planID, false)
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
	if _, err := s.updater.startDockerUpdate(strings.Repeat("0", 64), false); !errors.As(err, &refusal) || refusal.Code != "changed" {
		t.Fatalf("error = %v, want changed", err)
	}
}

func TestStartDockerUpdateWithNothingToUpdate(t *testing.T) {
	s := newStartBox(t, nil)
	s.box.enginePlans = map[string]string{}
	s.box.upgradeSimulation = simLibc
	status, err := s.updater.startDockerUpdate(s.planID, false)
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
	status, err := s.updater.startDockerUpdate(s.planID, false)
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
			status, err := s.updater.startDockerUpdate(s.planID, false)
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
			_, err := s.updater.startDockerUpdate(s.planID, false)
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
			status, err := s.updater.startDockerUpdate(s.planID, false)
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
	if _, err := s.updater.startDockerUpdate(s.planID, false); err != nil || asked != 1 {
		t.Fatalf("error = %v, asked %d", err, asked)
	}

	// a plan that changed, or a refusal, makes the question pointless
	s = newStartBox(t, nil)
	asked = 0
	s.updater.appOperations = func(context.Context) ([]string, error) { asked++; return nil, nil }
	_, _ = s.updater.startDockerUpdate(strings.Repeat("0", 64), false)
	s.box.dockerCEStatus = "hi"
	_, _ = s.updater.startDockerUpdate(s.planID, false)
	if asked != 0 {
		t.Errorf("AppManagement was asked %d times for starts that could not go on", asked)
	}
}

func TestStartDockerUpdateWritesAWholeRunWhenSystemdRunFails(t *testing.T) {
	s := newStartBox(t, nil)
	s.updater.start = func(string, string, ...string) ([]byte, error) {
		return []byte("Failed to start transient service unit: Access denied\n"), errors.New("exit status 1")
	}
	status, err := s.updater.startDockerUpdate(s.planID, false)
	var refusal *DockerUpdateRefusal
	if err == nil || errors.As(err, &refusal) || !strings.Contains(err.Error(), "start Docker update") {
		t.Fatalf("error = %v", err)
	}
	// the log has the core's first line, what systemd said, and a last line with the same nonce
	log, _ := os.ReadFile(s.updater.dockerLogPath())
	run := dockerpkg.ParseRun(string(log))
	if run.Nonce == "" || run.Terminal != dockerpkg.TerminalFailed || run.FailReason != dockerpkg.FailStart {
		t.Fatalf("run = %#v, log = %q", run, log)
	}
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "CASAOS_DOCKER_UPDATE_QUEUED "+run.Nonce) || lines[1] != "CASAOS_DOCKER_UPDATE_PLAN "+run.Nonce+" 28.0.4 29.8.0 1" || !strings.Contains(lines[2], "Access denied") || !strings.HasPrefix(lines[3], "CASAOS_DOCKER_UPDATE_FAILED "+run.Nonce+" 2026-08-13T01:02:03Z start") {
		t.Errorf("log = %q", log)
	}
	// the plan is still the run's: the page is told what the update was to be, and no way back
	if !status.MajorJump || status.From != "28.0.4" || status.To != "29.8.0" || status.RollbackCommand != "" {
		t.Errorf("major_jump = %v, from %q, to %q, rollback %q", status.MajorJump, status.From, status.To, status.RollbackCommand)
	}
	// a unit that systemd did not start is not a plan that changed: the code says so, and what
	// systemd said is the error
	if status.State != "failed" || status.Outcome != "failed" || status.ErrorCode != "start" || !strings.Contains(status.Error, "Access denied") || status.CompletedAt == "" || status.Phase != "" {
		t.Errorf("status = %#v", status)
	}
	// and the status that follows says the same
	again := s.updater.dockerStatus()
	if again.State != "failed" || again.ErrorCode != "start" || strings.Contains(again.Error, "confirmed") || again.Error == "" {
		t.Errorf("the status that follows = %#v", again)
	}
	if again.RollbackCommand != "" {
		t.Errorf("a rollback is offered for an update that never started: %q", again.RollbackCommand)
	}
}

func TestStartDockerUpdateDoesNotStartWhenTheLogCannotBeWritten(t *testing.T) {
	s := newStartBox(t, nil)
	s.updater.writeFile = func(string, []byte, os.FileMode) error { return errors.New("read-only file system") }
	status, err := s.updater.startDockerUpdate(s.planID, false)
	if err == nil || s.started.count != 0 || status.State != "failed" || !strings.Contains(status.Error, "read-only") {
		t.Fatalf("error = %v, status = %#v, started %d", err, status, s.started.count)
	}

	s = newStartBox(t, nil)
	s.updater.mkdirAll = func(string, os.FileMode) error { return errors.New("no space left") }
	status, err = s.updater.startDockerUpdate(s.planID, false)
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
	if _, err := s.updater.startDockerUpdate(s.planID, false); err != nil {
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

// A check can sit in apt-get update for minutes (a small box, a slow mirror). The start must not
// wait behind it: the page gives a request a minute, then says the update could not be started,
// and a start that waited would still happen afterwards, with nobody watching.
func TestStartDockerUpdateDoesNotWaitForACheck(t *testing.T) {
	s := newStartBox(t, nil)
	inCheck, release := make(chan struct{}), make(chan struct{})
	var boxMu sync.Mutex
	base := s.updater.command
	s.updater.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasSuffix(name, "apt-get") && len(args) > 0 && args[0] == "update" {
			close(inCheck)
			<-release
			return nil, nil
		}
		boxMu.Lock()
		defer boxMu.Unlock()
		return base(ctx, name, args...)
	}
	checked := make(chan error, 1)
	go func() { _, err := s.updater.check(); checked <- err }()
	<-inCheck

	// the check holds nothing that the start, the status or the other update need
	if s.updater.mu.TryLock() {
		s.updater.mu.Unlock()
	} else {
		t.Error("the check holds the updater's lock while it waits for apt-get update")
	}

	started := make(chan error, 1)
	begin := time.Now()
	go func() { _, err := s.updater.startDockerUpdate(s.planID, false); started <- err }()
	var err error
	timedOut := false
	select {
	case err = <-started:
	case <-time.After(time.Second):
		timedOut = true
	}
	took := time.Since(begin)
	close(release)
	if timedOut {
		t.Error("the start waits for the check")
		err = <-started
	}
	if err != nil || s.started.count != 1 {
		t.Errorf("startDockerUpdate() error = %v, started %d (after %v)", err, s.started.count, took)
	}
	if err := <-checked; err != nil {
		t.Errorf("the check: %v", err)
	}
}

// Two checks do not run side by side: they would both run apt-get update.
func TestChecksTakeTurns(t *testing.T) {
	box := ownerBox(t)
	updater := newTestSystemPackageUpdater(t)
	var boxMu sync.Mutex
	var updates, running, most int
	updater.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		boxMu.Lock()
		defer boxMu.Unlock()
		if strings.HasSuffix(name, "apt-get") && len(args) > 0 && args[0] == "update" {
			updates++
			running++
			most = max(most, running)
			boxMu.Unlock()
			time.Sleep(50 * time.Millisecond)
			boxMu.Lock()
			running--
			return nil, nil
		}
		return box.command(ctx, name, args...)
	}
	var all sync.WaitGroup
	for range 4 {
		all.Add(1)
		go func() {
			defer all.Done()
			if _, err := updater.check(); err != nil {
				t.Errorf("check() error = %v", err)
			}
		}()
	}
	all.Wait()
	if updates != 4 || most != 1 {
		t.Errorf("%d updates of the index, at most %d at once; want 4, one at a time", updates, most)
	}
}

func TestStartDockerUpdateTwiceStartsOnce(t *testing.T) {
	// systemd takes the unit: the second start finds it, and so does the System packages update
	s := newStartBox(t, nil)
	s.updater.start = func(path, unit string, args ...string) ([]byte, error) {
		s.started.count++
		s.box.activeUnits = map[string]bool{unit: true}
		return nil, nil
	}
	if _, err := s.updater.startDockerUpdate(s.planID, false); err != nil {
		t.Fatal(err)
	}
	_, err := s.updater.startDockerUpdate(s.planID, false)
	var refusal *DockerUpdateRefusal
	if !errors.As(err, &refusal) || refusal.Code != "running" || s.started.count != 1 {
		t.Fatalf("error = %v, started %d", err, s.started.count)
	}
	if _, err := s.updater.startUpdate(); !errors.Is(err, ErrSystemMaintenanceBusy) {
		t.Fatalf("the System packages update while Docker is updating: error = %v", err)
	}
}

// systemd queues the start of a unit, and the unit reads inactive until it takes it up: a second
// start in that moment would rewrite the log, whose first line holds the nonce of the first run,
// and the run that is going on would be read as one that failed. The log of a run that has not
// ended says there is one.
func TestStartDockerUpdateTwiceBeforeSystemdHasTheUnitStartsOnce(t *testing.T) {
	s := newStartBox(t, nil)
	if _, err := s.updater.startDockerUpdate(s.planID, false); err != nil {
		t.Fatal(err)
	}
	if s.box.activeUnits[systemDockerUpdateUnit] {
		t.Fatal("the unit is active: this test is about the moment it is not")
	}
	first, err := os.ReadFile(s.updater.dockerLogPath())
	if err != nil {
		t.Fatal(err)
	}
	logAgeOf(s.updater, time.Second)

	status, err := s.updater.startDockerUpdate(s.planID, false)
	var refusal *DockerUpdateRefusal
	if !errors.As(err, &refusal) || refusal.Code != "running" || status.ErrorCode != "running" || s.started.count != 1 {
		t.Fatalf("error = %v, status = %#v, started %d", err, status, s.started.count)
	}
	// the run that goes on is the first one's, and the answer says how it goes
	if after, _ := os.ReadFile(s.updater.dockerLogPath()); string(after) != string(first) {
		t.Errorf("the log was rewritten:\n%q\nwas\n%q", after, first)
	}
	if status.State != "running" && status.State != "finalizing" || status.StartedAt != "2026-08-13T01:02:03Z" {
		t.Errorf("status = %#v", status)
	}
	if again := s.updater.dockerStatus(); again.State != "finalizing" || again.ErrorCode != "" {
		t.Errorf("the status that follows = %#v", again)
	}
}

// A log that says a run ended, or that is too old for the last line to be on its way, is no run.
func TestStartDockerUpdateStartsAfterARunThatIsOver(t *testing.T) {
	for name, c := range map[string]struct {
		lines []string
		age   time.Duration
	}{
		"succeeded a moment ago":      {ownersRun(mark("DAEMON", "29.8.0"), mark("SUCCESS", "2026-08-13T01:05:00Z")), time.Second},
		"restart pending":             {ownersRun(mark("DAEMON", "28.0.4"), mark("RESTART_PENDING", "2026-08-13T01:05:00Z")), time.Second},
		"failed a moment ago":         {ownersRun(mark("FAILED", "2026-08-13T01:03:00Z", "install")), time.Second},
		"a unit that was not started": {[]string{logQueued, "Failed to start transient service unit", mark("FAILED", "2026-08-13T01:03:00Z", "start")}, time.Second},
		"lost, long ago":              {ownersRun(), time.Hour},
		"not a run":                   {[]string{"Reading package lists..."}, time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStartBox(t, nil)
			if err := os.WriteFile(s.updater.dockerLogPath(), []byte(strings.Join(c.lines, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			logAgeOf(s.updater, c.age)
			if _, err := s.updater.startDockerUpdate(s.planID, false); err != nil || s.started.count != 1 {
				t.Fatalf("error = %v, started %d", err, s.started.count)
			}
		})
	}
}

func TestDockerUpdateArgsRefuseWhatIsNotValidated(t *testing.T) {
	nonce := "0123456789abcdef0123456789abcdef"
	pins := []string{"containerd.io=2.1.4-1", "docker-ce-cli=" + debian29, "docker-ce=" + debian29}
	names := []string{"containerd.io", "docker-ce", "docker-ce-cli"}
	if _, err := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", pins, names, "29.8.0", nil); err != nil {
		t.Fatalf("valid arguments: %v", err)
	}
	if _, err := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", pins[:1], names[:1], "", nil); err != nil {
		t.Fatalf("no docker-ce in the plan: %v", err)
	}
	// the new packages are names and no pins
	withNew := append(append([]string{}, names...), "libedit2", "libjansson4", "libnftables1", "nftables")
	args, err := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", pins, withNew, "29.8.0", nil)
	if err != nil {
		t.Fatalf("new packages: %v", err)
	}
	var gotPins, gotNames string
	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, "--setenv=CASAOS_DU_PINS="); ok {
			gotPins = value
		}
		if value, ok := strings.CutPrefix(arg, "--setenv=CASAOS_DU_NAMES="); ok {
			gotNames = value
		}
	}
	if gotPins != strings.Join(pins, " ") || gotNames != strings.Join(withNew, " ") {
		t.Errorf("pins = %q, names = %q", gotPins, gotNames)
	}
	// ten new packages is the most
	ten := append([]string{}, names...)
	for i := 1; i <= dockerpkg.MaxNewPackages; i++ {
		ten = append(ten, fmt.Sprintf("libdep%d", i))
	}
	if _, err := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", pins, ten, "29.8.0", nil); err != nil {
		t.Fatalf("ten new packages: %v", err)
	}
	allSeven := []string{"containerd.io=2.1.4-1", "docker-buildx-plugin=0.30.1-1", "docker-ce-cli=" + debian29, "docker-ce-rootless-extras=" + debian29, "docker-ce=" + debian29, "docker-compose-plugin=5.0.0-1", "docker-model-plugin=1.0.0-1"}
	sevenNames := []string{"containerd.io", "docker-buildx-plugin", "docker-ce", "docker-ce-cli", "docker-ce-rootless-extras", "docker-compose-plugin", "docker-model-plugin"}
	if _, err := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", allSeven, append(append([]string{}, sevenNames...), ten[len(names):]...), "29.8.0", nil); err != nil {
		t.Fatalf("seven upgrades and ten new packages: %v", err)
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
		// the names of the new packages are checked too: this is the last stop before a root shell
		"a new package with a glob":                  {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, append(append([]string{}, names...), "lib*")},
		"a new package that is a command":            {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, append(append([]string{}, names...), "nftables;reboot")},
		"a new package that is a flag":               {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, append(append([]string{}, names...), "-oAPT::Foo=1")},
		"a new package with a space":                 {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, append(append([]string{}, names...), "nftables libedit2")},
		"a new package with an upper case name":      {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, append(append([]string{}, names...), "NFTables")},
		"an empty name":                              {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, append(append([]string{}, names...), "")},
		"a new package twice":                        {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, append(append([]string{}, names...), "nftables", "nftables")},
		"a new docker.io":                            {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, append(append([]string{}, names...), "docker.io")},
		"a new containerd":                           {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, append(append([]string{}, names...), "containerd")},
		"a new docker-compose-v2":                    {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, append(append([]string{}, names...), "docker-compose-v2")},
		"a new docker-buildx":                        {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, append(append([]string{}, names...), "docker-buildx")},
		"more than ten new packages":                 {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, append(append([]string{}, names...), "a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10", "a11")},
		"more names than seven upgrades and ten new": {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, append(append([]string{}, names...), "a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10", "a11", "a12", "a13", "a14", "a15")},
		// seven upgrades and ten new packages are the most there can be: an eighth upgrade (the same
		// package of another architecture) is a plan that is not one of Docker's
		"eight upgrades and ten new packages": {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", append(append([]string{}, allSeven...), "containerd.io:armhf=2.1.4-1"), append(append(append([]string{}, sevenNames...), "containerd.io:armhf"), ten[len(names):]...)},
		"a pin that is not among the names":   {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", pins, []string{"containerd.io", "docker-ce", "nftables"}},
		"names and no pin":                    {nonce, "/var/log/x.log", "/usr/bin/apt-get", "29.8.0", nil, []string{"nftables"}},
	} {
		if args, err := dockerUpdateArgs(c.nonce, c.log, c.apt, c.pins, c.names, c.to, nil); err == nil {
			t.Errorf("%s: dockerUpdateArgs() = %v, want an error", name, args)
		}
	}
}

// The plan is the second line, and it is there before the unit is: the status of a unit that has
// not written a word yet already says where it goes, and a start that systemd then refuses leaves a
// log that still says it.
func TestStartDockerUpdateWritesTheQueuedLineAndThePlanInOneWrite(t *testing.T) {
	s := newStartBox(t, nil)
	var writes []string
	write := s.updater.writeFile
	s.updater.writeFile = func(path string, data []byte, mode os.FileMode) error {
		if path == s.updater.dockerLogPath() {
			writes = append(writes, string(data))
		}
		return write(path, data, mode)
	}
	var seenByTheUnit string
	start := s.updater.start
	s.updater.start = func(path, unit string, args ...string) ([]byte, error) {
		log, _ := os.ReadFile(s.updater.dockerLogPath())
		seenByTheUnit = string(log)
		return start(path, unit, args...)
	}
	if _, err := s.updater.startDockerUpdate(s.planID, false); err != nil {
		t.Fatalf("startDockerUpdate() error = %v", err)
	}
	if len(writes) != 1 || strings.Count(writes[0], "\n") != 2 || seenByTheUnit != writes[0] {
		t.Fatalf("writes = %q, the log when the unit was started = %q: want the two lines in one write, before the start", writes, seenByTheUnit)
	}
	first, second, _ := strings.Cut(writes[0], "\n")
	if !strings.HasPrefix(first, "CASAOS_DOCKER_UPDATE_QUEUED ") || !strings.HasPrefix(second, "CASAOS_DOCKER_UPDATE_PLAN ") {
		t.Errorf("log = %q", writes[0])
	}
}

func TestStartDockerUpdateWritesThePlanOfWhatItStarts(t *testing.T) {
	patch := func(b *aptBox) {
		const current, candidate = "5:29.8.1-1~debian.11~bullseye", "5:29.8.2-1~debian.11~bullseye"
		engine, client := instEngine("docker-ce", current, candidate), instEngine("docker-ce-cli", current, candidate)
		b.upgradeSimulation = engine + client
		b.dockerCE = current
		b.packages = map[string]string{"docker-ce-cli": current}
		b.policy = strings.NewReplacer(debian28, current, debian29, candidate).Replace(ownerPolicy)
		b.enginePlans = map[string]string{"docker-ce": engine, "docker-ce-cli": client}
	}
	pluginAlone := func(b *aptBox) {
		const plugin, newer = "2.40.0-1~debian.11~bullseye", "2.40.1-1~debian.11~bullseye"
		b.upgradeSimulation = instEngine("docker-compose-plugin", plugin, newer)
		b.packages["docker-compose-plugin"] = plugin
		b.policy = strings.NewReplacer(debian29, debian28).Replace(ownerPolicy) // the engine is current
		b.enginePlans = map[string]string{"docker-compose-plugin": instEngine("docker-compose-plugin", plugin, newer)}
	}
	withDependencies := func(b *aptBox) {
		b.upgradeSimulation = simLibc
		b.enginePlans["containerd.io"] = ownerNewDependencies + ownerContaind
	}
	for _, c := range []struct {
		name     string
		tweak    func(*aptBox)
		from, to string
		jump     bool
	}{
		{"the owner's box", nil, "28.0.4", "29.8.0", true},
		{"the owner's box, with the packages Docker 29 needs", withDependencies, "28.0.4", "29.8.0", true},
		{"a patch of the same major", patch, "29.8.1", "29.8.2", false},
		{"a plugin alone, the engine does not move", pluginAlone, "28.0.4", "28.0.4", false},
	} {
		s := newStartBox(t, c.tweak)
		fresh := ownerBox(t)
		if c.tweak != nil {
			c.tweak(fresh)
		}
		s.planID = checkBox(t, fresh, nil).Docker.Update.PlanID
		status, err := s.updater.startDockerUpdate(s.planID, false)
		if err != nil {
			t.Fatalf("%s: startDockerUpdate() error = %v", c.name, err)
		}
		log, _ := os.ReadFile(s.updater.dockerLogPath())
		run := dockerpkg.ParseRun(string(log))
		if want := dockerpkg.QueuedMarker(run.Nonce, s.updater.now()) + dockerpkg.PlanMarker(run.Nonce, c.from, c.to, c.jump); run.Nonce == "" || string(log) != want {
			t.Errorf("%s: log = %q, want %q", c.name, log, want)
		}
		if run.From != c.from || run.To != c.to || run.MajorJump != c.jump {
			t.Errorf("%s: the run says %q to %q, major jump %v", c.name, run.From, run.To, run.MajorJump)
		}
		// the status of the start and the status that follows say the same of it
		again := s.updater.dockerStatus()
		for _, got := range []SystemDockerUpdateStatus{status, again} {
			if got.From != c.from || got.To != c.to || got.MajorJump != c.jump {
				t.Errorf("%s: status = %#v, want %q to %q, major jump %v", c.name, got, c.from, c.to, c.jump)
			}
		}
	}
}

// ---- packages on hold ------------------------------------------------------------------------

const heldReason = "Docker's packages are on hold. Confirm that the update may go ahead anyway; the hold is put back afterwards."

// newHeldStartBox is the owner's box with those packages of the engine on hold, and the plan the
// page confirmed: the one its check gave, which says the hold.
func newHeldStartBox(t *testing.T, held ...string) *startedBox {
	t.Helper()
	s := newStartBox(t, func(b *aptBox) {
		h := heldBox(t, held...)
		b.dockerCEStatus, b.statuses, b.packages = h.dockerCEStatus, h.statuses, h.packages
	})
	update := checkBox(t, heldBox(t, held...), nil).Docker.Update
	if update == nil || update.PlanID == "" || update.PlanID == ownerPlanID(t) {
		t.Fatalf("update = %#v: want a plan of its own", update)
	}
	s.planID = update.PlanID
	return s
}

func TestStartDockerUpdateRefusesAHeldPlanUntilTheOwnerAllowsIt(t *testing.T) {
	s := newHeldStartBox(t, "docker-ce-cli", "docker-ce")
	status, err := s.updater.startDockerUpdate(s.planID, false)
	var refusal *DockerUpdateRefusal
	if !errors.As(err, &refusal) || refusal.Code != "held" || refusal.Reason != heldReason || !reflect.DeepEqual(refusal.Detail, []string{"docker-ce", "docker-ce-cli"}) {
		t.Fatalf("error = %v (%#v), want the refusal held, with the names", err, refusal)
	}
	// the reason and the names travel in the data, as for every refusal
	if status.State != "idle" || status.ErrorCode != "held" || status.Error != heldReason || !reflect.DeepEqual(status.RefusalDetail, []string{"docker-ce", "docker-ce-cli"}) {
		t.Errorf("status = %#v", status)
	}
	if s.started.count != 0 {
		t.Error("a unit was started")
	}
	s.noLog(t)
	// what the page reads of it, and what the route sends as the data of the 409
	encoded, _ := json.Marshal(status)
	wantJSON := `{"supported":true,"state":"idle","phase":"","outcome":"","error":"` + heldReason + `","error_code":"held","refusal_detail":["docker-ce","docker-ce-cli"],"exit_code":null,"started_at":"","completed_at":"","from":"","to":"","major_jump":false,"held":false,"held_packages":[],"holds_kept":false,"not_returned":[],"rollback_command":"","log":""}`
	if string(encoded) != wantJSON {
		t.Errorf("JSON = %s\nwant   %s", encoded, wantJSON)
	}
	// it is refused before the plan is simulated: no word of the owner, no use in the slow part
	if calls := engineSimulations(s.box); len(calls) != 0 {
		t.Errorf("simulations = %#v", calls)
	}

	// said again with the owner's word it starts, and the unit is told
	status, err = s.updater.startDockerUpdate(s.planID, true)
	if err != nil || s.started.count != 1 || status.State != "running" {
		t.Fatalf("error = %v, started %d, status = %#v", err, s.started.count, status)
	}
	if got := s.env("CASAOS_DU_ALLOW_HELD"); got != "1" {
		t.Errorf("CASAOS_DU_ALLOW_HELD = %q, want 1", got)
	}
	// the unit is told which packages are on hold: it puts the hold back on those
	if got := s.env("CASAOS_DU_HELD"); got != "docker-ce docker-ce-cli" {
		t.Errorf("CASAOS_DU_HELD = %q, want the names that are held, sorted", got)
	}
	// the unit's own environment is the rest of what it always was, in the same order, with the names and
	// the word added
	wantEnv := []string{"DEBIAN_FRONTEND", "CASAOS_DU_NONCE", "CASAOS_DU_LOG", "CASAOS_DU_APT", "CASAOS_DU_PINS", "CASAOS_DU_NAMES", "CASAOS_DU_TO", "CASAOS_DU_HELD", "CASAOS_DU_ALLOW_HELD", "CASAOS_DU_DAEMON_TIMEOUT", "CASAOS_DU_RETURN_TIMEOUT", "CASAOS_DU_POLL"}
	var gotEnv []string
	for _, arg := range s.started.args {
		if setenv, ok := strings.CutPrefix(arg, "--setenv="); ok {
			name, _, _ := strings.Cut(setenv, "=")
			gotEnv = append(gotEnv, name)
		}
	}
	if !reflect.DeepEqual(gotEnv, wantEnv) {
		t.Errorf("environment = %v, want %v", gotEnv, wantEnv)
	}
	if got, want := s.env("CASAOS_DU_PINS"), "containerd.io=2.1.4-1 docker-ce-cli="+debian29+" docker-ce="+debian29; got != want {
		t.Errorf("CASAOS_DU_PINS = %q, want %q", got, want)
	}
	if script := s.started.args[len(s.started.args)-1]; script != dockerUpdateScriptArg() {
		t.Error("the script is not the constant")
	}
	// the plan is the same plan: the log says what it said, and nothing of a hold
	log, _ := os.ReadFile(s.updater.dockerLogPath())
	run := dockerpkg.ParseRun(string(log))
	if want := dockerpkg.QueuedMarker(run.Nonce, s.updater.now()) + dockerpkg.PlanMarker(run.Nonce, "28.0.4", "29.8.0", true); string(log) != want {
		t.Errorf("log = %q, want %q", log, want)
	}
}

func TestStartDockerUpdateIgnoresTheOwnersWordWhenNothingIsHeld(t *testing.T) {
	s := newStartBox(t, nil)
	if _, err := s.updater.startDockerUpdate(s.planID, true); err != nil || s.started.count != 1 {
		t.Fatalf("error = %v, started %d", err, s.started.count)
	}
	// no word is given to the unit that nothing needs it for: apt is not told it may change what is held
	for _, arg := range s.started.args[:len(s.started.args)-1] {
		if strings.Contains(arg, "ALLOW_HELD") || strings.Contains(arg, "CASAOS_DU_HELD") || strings.Contains(arg, "allow-change-held") {
			t.Errorf("the unit was given %q", arg)
		}
	}
	// ... and it is the start it has always been
	wantEnv := s.env("CASAOS_DU_NAMES")
	if wantEnv != "containerd.io docker-ce docker-ce-cli" {
		t.Errorf("CASAOS_DU_NAMES = %q", wantEnv)
	}
}

func TestStartDockerUpdateSaysChangedWhenTheHoldIsNotTheConfirmedOne(t *testing.T) {
	var refusal *DockerUpdateRefusal
	released := func(b *aptBox) { b.dockerCEStatus, b.statuses = "", nil }

	// confirmed with the hold, and the owner released it since
	s := newHeldStartBox(t, "docker-ce")
	released(s.box)
	if _, err := s.updater.startDockerUpdate(s.planID, true); !errors.As(err, &refusal) || refusal.Code != "changed" || s.started.count != 0 {
		t.Fatalf("a hold that went: error = %v, started %d", err, s.started.count)
	}
	s.noLog(t)

	// confirmed without it, and a hold has been set since: the owner has not been told of it, and the
	// word that was given is not for it
	s = newStartBox(t, nil)
	s.box.dockerCEStatus = "hi"
	if _, err := s.updater.startDockerUpdate(s.planID, true); !errors.As(err, &refusal) || refusal.Code != "changed" || s.started.count != 0 {
		t.Fatalf("a hold that came: error = %v, started %d", err, s.started.count)
	}
	s.noLog(t)
	// ... and with no word at all it is the hold that is said, which is what the page re-checks on
	if _, err := s.updater.startDockerUpdate(s.planID, false); !errors.As(err, &refusal) || refusal.Code != "held" || s.started.count != 0 {
		t.Fatalf("a hold that came, with no word: error = %v, started %d", err, s.started.count)
	}

	// confirmed with a hold on one package and it is on another now
	s = newHeldStartBox(t, "docker-ce")
	released(s.box)
	s.box.statuses = map[string]string{"docker-ce-cli": "hi"}
	if _, err := s.updater.startDockerUpdate(s.planID, true); !errors.As(err, &refusal) || refusal.Code != "changed" || s.started.count != 0 {
		t.Fatalf("a hold that moved: error = %v, started %d", err, s.started.count)
	}
}

func TestStartDockerUpdateDoesNotGoAheadOfAHeldPackageThatIsNotDockers(t *testing.T) {
	s := newHeldStartBox(t, "docker-ce")
	s.box.enginePlans["docker-ce-cli"] += "Inst libnftables1 [0.9.7-1] (0.9.8-3.1 Debian:11/stable [amd64])\n"
	s.box.packages["libnftables1"] = "0.9.7-1"
	s.box.statuses["libnftables1"] = "hi"
	status, err := s.updater.startDockerUpdate(s.planID, true)
	var refusal *DockerUpdateRefusal
	if !errors.As(err, &refusal) || refusal.Code != "plan" || !reflect.DeepEqual(refusal.Detail, []string{"libnftables1"}) || status.ErrorCode != "plan" || s.started.count != 0 {
		t.Fatalf("error = %v, status = %#v, started %d", err, status, s.started.count)
	}
	s.noLog(t)
}

// What the check refuses for it refuses on a box that is held too, once the owner has allowed the hold.
func TestStartDockerUpdateRefusesWhatTheCheckRefusesOnAHeldBoxToo(t *testing.T) {
	for _, c := range refusalCases() {
		t.Run(c.name, func(t *testing.T) {
			s := newHeldStartBox(t, "docker-ce")
			if c.change != nil {
				c.change(s.box)
			}
			if c.tweak != nil {
				c.tweak(s.updater)
			}
			_, err := s.updater.startDockerUpdate(s.planID, true)
			var refusal *DockerUpdateRefusal
			if !errors.As(err, &refusal) || refusal.Code != c.code {
				t.Fatalf("error = %v, want a refusal %q", err, c.code)
			}
			if s.started.count != 0 {
				t.Error("a unit was started")
			}
			s.noLog(t)
		})
	}
}

func TestStartDockerUpdateIsTheSameThroughTheServiceWithAHold(t *testing.T) {
	s := newHeldStartBox(t, "docker-ce")
	system := &systemService{packageUpdates: s.updater}
	system.SetAppOperations(func(context.Context) ([]string, error) { return nil, nil })
	if _, err := system.StartDockerUpdate(s.planID, false); err == nil || s.started.count != 0 {
		t.Fatalf("StartDockerUpdate() error = %v, started %d: want the refusal", err, s.started.count)
	}
	if _, err := system.StartDockerUpdate(s.planID, true); err != nil || s.started.count != 1 || s.env("CASAOS_DU_ALLOW_HELD") != "1" {
		t.Fatalf("StartDockerUpdate() error = %v, started %d", err, s.started.count)
	}
}

func TestDockerUpdateArgsCarryTheHeldNamesAndTheOwnersWordTogetherOrNotAtAll(t *testing.T) {
	nonce := "0123456789abcdef0123456789abcdef"
	pins := []string{"containerd.io=2.1.4-1", "docker-ce-cli=" + debian29, "docker-ce=" + debian29}
	names := []string{"containerd.io", "docker-ce", "docker-ce-cli"}
	for _, none := range [][]string{nil, {}} {
		plain, err := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", pins, names, "29.8.0", none)
		if err != nil {
			t.Fatal(err)
		}
		for _, arg := range plain[:len(plain)-1] { // the script is the last, and names the variables
			if strings.Contains(arg, "ALLOW_HELD") || strings.Contains(arg, "CASAOS_DU_HELD") {
				t.Errorf("the arguments of a start that has nothing on hold hold %q", arg)
			}
		}
	}
	plain, _ := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", pins, names, "29.8.0", nil)

	// the names, and the word that goes with them, are there exactly when something is held; sorted, whatever
	// the order they were given in; and the arguments are the same ones otherwise, in the same order
	for _, held := range [][]string{{"docker-ce"}, {"docker-ce-cli", "containerd.io", "docker-ce"}, {"docker-buildx-plugin"}} {
		allowed, err := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", pins, names, "29.8.0", held)
		if err != nil {
			t.Fatalf("%v: %v", held, err)
		}
		sorted := append([]string{}, held...)
		sort.Strings(sorted)
		wantNames := "--setenv=CASAOS_DU_HELD=" + strings.Join(sorted, " ")
		var without []string
		for _, arg := range allowed {
			if arg != wantNames && arg != "--setenv=CASAOS_DU_ALLOW_HELD=1" {
				without = append(without, arg)
			}
		}
		if len(allowed) != len(plain)+2 || !reflect.DeepEqual(without, plain) {
			t.Errorf("%v: allowed:\n%s\nplain:\n%s", held, strings.Join(allowed, "\n"), strings.Join(plain, "\n"))
		}
		i, j, to := indexOf(allowed, wantNames), indexOf(allowed, "--setenv=CASAOS_DU_ALLOW_HELD=1"), indexOf(allowed, "--setenv=CASAOS_DU_TO=29.8.0")
		if to < 0 || i != to+1 || j != i+1 {
			t.Errorf("%v: the names and the word are not after CASAOS_DU_TO, in that order: %v", held, allowed)
		}
	}
	// the caller's list is not reordered
	given := []string{"docker-ce-cli", "containerd.io"}
	if _, err := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", pins, names, "29.8.0", given); err != nil || !reflect.DeepEqual(given, []string{"docker-ce-cli", "containerd.io"}) {
		t.Errorf("error = %v, the list of the holds is now %v", err, given)
	}
	// all seven are the most there are
	all := append([]string{}, dockerpkg.EngineNames...)
	if _, err := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", pins, names, "29.8.0", all); err != nil {
		t.Errorf("seven packages on hold: %v", err)
	}
}

// The names of the holds are put in a root shell's environment, and the unit takes them for the names of
// packages it holds: this is the last stop before it.
func TestDockerUpdateArgsRefuseHeldNamesThatAreNotTheEngines(t *testing.T) {
	nonce := "0123456789abcdef0123456789abcdef"
	pins := []string{"containerd.io=2.1.4-1", "docker-ce-cli=" + debian29, "docker-ce=" + debian29}
	names := []string{"containerd.io", "docker-ce", "docker-ce-cli"}
	seven := append([]string{}, dockerpkg.EngineNames...)
	for name, held := range map[string][]string{
		"a package that is not the engine's":         {"libc6"},
		"a package that is the engine's and another": {"docker-ce", "libc6"},
		"a new dependency":                           {"nftables"},
		"a distribution's docker":                    {"docker.io"},
		"a name that is a command":                   {"docker-ce;reboot"},
		"a name with a substitution":                 {"docker-ce$(reboot)"},
		"a name with a space":                        {"docker-ce docker-ce-cli"},
		"a name with a new line":                     {"docker-ce\ndocker-ce-cli"},
		"a name with a glob":                         {"docker-*"},
		"a flag":                                     {"--help"},
		"a name in upper case":                       {"Docker-CE"},
		"an empty name":                              {""},
		"a name with an architecture":                {"containerd.io:armhf"},
		"a pin":                                      {"docker-ce=5:29.8.0"},
		"a name twice":                               {"docker-ce", "docker-ce"},
		"a name twice, in two spellings":             {"docker-ce", "docker-ce:amd64"},
		"eight, which is more than the engine has":   append(append([]string{}, seven...), "containerd.io:armhf"),
		"eight, with the same one twice":             append(append([]string{}, seven...), "docker-ce"),
		"eight, with one that is not the engine's":   append(append([]string{}, seven...), "libc6"),
	} {
		if args, err := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", pins, names, "29.8.0", held); err == nil {
			t.Errorf("%s: dockerUpdateArgs() = %v, want an error", name, args)
		}
	}
}

// A held package that the plan does not touch is still on hold, and the check says so: the unit is told of
// it too, so that HELD, HOLDS, the status and the check describe the same packages.
func TestStartDockerUpdateGivesTheUnitEveryHeldPackageOfTheEngineAndNotOnlyThePlans(t *testing.T) {
	s := newHeldStartBox(t, "docker-buildx-plugin", "docker-ce")
	if _, err := s.updater.startDockerUpdate(s.planID, true); err != nil || s.started.count != 1 {
		t.Fatalf("error = %v, started %d", err, s.started.count)
	}
	// docker-buildx-plugin has no update in the plan: it is not among the names the unit may touch
	if got := s.env("CASAOS_DU_HELD"); got != "docker-buildx-plugin docker-ce" {
		t.Errorf("CASAOS_DU_HELD = %q", got)
	}
	if got := s.env("CASAOS_DU_NAMES"); got != "containerd.io docker-ce docker-ce-cli" {
		t.Errorf("CASAOS_DU_NAMES = %q: the held package that the plan does not touch is not one to touch", got)
	}
	if got := s.env("CASAOS_DU_ALLOW_HELD"); got != "1" {
		t.Errorf("CASAOS_DU_ALLOW_HELD = %q", got)
	}
}

// What the page reads of the status is a list, never null, whichever way the status gets to it: the 200
// body of the start, the 501 of a host that cannot update, and the 500 of a start that failed on the box
// before the log had a nonce.
func TestTheStatusThatReachesThePageHasNoNullHeldPackages(t *testing.T) {
	check := func(name string, status SystemDockerUpdateStatus) {
		t.Helper()
		encoded, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"held":false,"held_packages":[],"holds_kept":false`, `"not_returned":[]`} {
			if !strings.Contains(string(encoded), want) {
				t.Errorf("%s: the status is %s, want it to hold %s", name, encoded, want)
			}
		}
	}

	s := newStartBox(t, nil)
	status, err := s.updater.startDockerUpdate(s.planID, false)
	if err != nil || status.State != "running" {
		t.Fatalf("error = %v, status = %#v", err, status)
	}
	check("the 200 body of the start", status)

	s = newStartBox(t, nil)
	s.updater.getEUID = func() int { return 1000 }
	status, err = s.updater.startDockerUpdate(s.planID, false)
	if !errors.Is(err, ErrSystemPackageUpdatesUnsupported) || status.ErrorCode != "unsupported" {
		t.Fatalf("error = %v, status = %#v", err, status)
	}
	check("the 501 status of a host that cannot update", status)

	s = newStartBox(t, nil)
	s.updater.writeFile = func(string, []byte, os.FileMode) error { return errors.New("read-only file system") }
	status, err = s.updater.startDockerUpdate(s.planID, false)
	if err == nil || status.State != "failed" || !strings.Contains(status.Error, "read-only") {
		t.Fatalf("error = %v, status = %#v", err, status)
	}
	check("the 500 status of a start that failed before it had a nonce", status)
}
