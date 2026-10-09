package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// aptBox answers the commands the System packages updater runs, from its fields.
type aptBox struct {
	upgradeSimulation string // apt-get -s upgrade
	installSimulation string // apt-get -s install --only-upgrade <names>
	installFailure    string // when set, that simulation exits non-zero with this output
	activeUnits       map[string]bool
	dockerCE          string // the dpkg version of docker-ce, "" when it is not installed
	dockerCEStatus    string // dpkg's abbreviation, "ii" unless set
	dockerIO          string
	policy            string
	snap              bool
	// onInstallSimulation runs when the list is simulated: something else may start meanwhile.
	onInstallSimulation func()
	calls               []string

	// packages are the other packages on the box, with their dpkg version, and statuses their
	// dpkg abbreviation ("ii" unless set; "hi" is on hold).
	packages map[string]string
	statuses map[string]string
	// enginePlans, when set, makes the simulation of an install answer from the names it was
	// asked for: the lines of each of them, in the order they were asked, and nothing for a name
	// that is not in it. Without it that simulation answers installSimulation whatever it is asked.
	enginePlans map[string]string
	// The docker command. daemonInfo is what `docker info` prints (a healthy daemon, not in a
	// swarm, when empty); noDaemon makes it fail as with a stopped daemon; daemonHangs makes it
	// wait for its deadline. runningIDs are what `docker ps -q` prints, inspected what
	// `docker inspect` prints and inspectFailure that it exits non-zero.
	daemonInfo     string
	noDaemon       bool
	daemonHangs    bool
	runningIDs     []string
	inspected      string
	inspectFailure bool
	// dpkgAudit is what `dpkg --audit` prints: nothing, for a box whose packages are all right.
	dpkgAudit string
	// strict, when set, reports a command that nothing here answers (a real install, a
	// systemctl start, a df...) as a test failure and as a failed command, instead of letting it
	// succeed with no output, which would hide a check the code forgot to make.
	strict interface{ Errorf(string, ...any) }
}

const healthyDaemon = `{"ServerVersion":"28.0.4","SwarmState":"inactive"}` + "\n"

// unexpected is the answer to a command that nothing here answers.
func (b *aptBox) unexpected(name string, args []string) ([]byte, error) {
	if b.strict == nil {
		return nil, nil
	}
	b.strict.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
	return nil, errors.New("unexpected command " + name)
}

var versionParts = regexp.MustCompile(`\d+|\D+`)

func compareNumbers(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

// compareDpkgVersions orders two dpkg versions the way dpkg does for the versions of these
// tests: the epoch first, then the number runs as numbers (29.10 is after 29.9) and the rest as
// text. It does not know that "~" sorts before everything.
func compareDpkgVersions(a, b string) int {
	epoch := func(v string) (string, string) {
		if e, rest, found := strings.Cut(v, ":"); found && regexp.MustCompile(`^\d+$`).MatchString(e) {
			return e, rest
		}
		return "0", v
	}
	ea, ra := epoch(a)
	eb, rb := epoch(b)
	if c := compareNumbers(ea, eb); c != 0 {
		return c
	}
	pa, pb := versionParts.FindAllString(ra, -1), versionParts.FindAllString(rb, -1)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		c := strings.Compare(pa[i], pb[i])
		if pa[i][0] >= '0' && pa[i][0] <= '9' && pb[i][0] >= '0' && pb[i][0] <= '9' {
			c = compareNumbers(pa[i], pb[i])
		}
		if c != 0 {
			return c
		}
	}
	return len(pa) - len(pb)
}

func (b *aptBox) command(ctx context.Context, name string, args ...string) ([]byte, error) {
	b.calls = append(b.calls, name+" "+strings.Join(args, " "))
	switch name {
	case "systemctl":
		if len(args) == 0 || args[0] != "show" {
			return b.unexpected(name, args)
		}
		if len(args) > 1 && b.activeUnits[args[1]] {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), nil
	case "dpkg-query":
		pkg := args[len(args)-1]
		status := b.dockerCEStatus
		if status == "" {
			status = "ii"
		}
		if pkg == "docker-ce" && b.dockerCE != "" {
			return []byte(status + " \t" + b.dockerCE + "\n"), nil
		}
		if pkg == "docker.io" && b.dockerIO != "" {
			return []byte("ii \t" + b.dockerIO + "\n"), nil
		}
		if version, ok := b.packages[pkg]; ok {
			status := b.statuses[pkg]
			if status == "" {
				status = "ii"
			}
			return []byte(status + " \t" + version + "\n"), nil
		}
		return nil, errors.New("dpkg-query: no packages found matching " + pkg)
	case "dpkg":
		if len(args) == 1 && args[0] == "--audit" {
			return []byte(b.dpkgAudit), nil
		}
		// dpkg --compare-versions <candidate> gt <installed>: true when the candidate is later
		if len(args) == 4 && args[0] == "--compare-versions" && args[2] == "gt" && compareDpkgVersions(args[1], args[3]) > 0 {
			return nil, nil
		}
		return nil, errors.New("exit status 1")
	case "apt-cache":
		return []byte(b.policy), nil
	case "snap":
		if b.snap {
			return nil, nil
		}
		return nil, errors.New("snap: not installed")
	}
	if strings.HasSuffix(name, "apt-get") {
		joined := " " + strings.Join(args, " ") + " "
		switch {
		case len(args) > 0 && args[0] == "update":
			return nil, nil
		case strings.Contains(joined, " -s ") && strings.Contains(joined, " install "):
			if b.onInstallSimulation != nil {
				b.onInstallSimulation()
			}
			if b.installFailure != "" {
				return []byte(b.installFailure), errors.New("exit status 100")
			}
			if b.enginePlans != nil {
				var plan strings.Builder
				for _, asked := range args[indexOf(args, "install")+1:] {
					if !strings.HasPrefix(asked, "-") {
						plan.WriteString(b.enginePlans[asked])
					}
				}
				return []byte(plan.String()), nil
			}
			return []byte(b.installSimulation), nil
		case strings.Contains(joined, " -s "):
			return []byte(b.upgradeSimulation), nil
		}
		return b.unexpected(name, args)
	}
	if strings.HasSuffix(name, "docker") && len(args) > 0 {
		switch args[0] {
		case "info":
			switch {
			case b.daemonHangs:
				// the command is run with a deadline, and ends when it does
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
					return b.unexpected(name, args)
				}
				return nil, context.DeadlineExceeded
			case b.noDaemon:
				return []byte("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?\n"), errors.New("exit status 1")
			case b.daemonInfo != "":
				return []byte(b.daemonInfo), nil
			}
			return []byte(healthyDaemon), nil
		case "ps":
			return []byte(strings.Join(b.runningIDs, "\n") + "\n"), nil
		case "inspect":
			if b.inspectFailure {
				return []byte(b.inspected), errors.New("exit status 1")
			}
			return []byte(b.inspected), nil
		}
	}
	return b.unexpected(name, args)
}

func indexOf(list []string, want string) int {
	for i, item := range list {
		if item == want {
			return i
		}
	}
	return -1
}

const (
	simLibc    = "Inst libc6 [2.35-0ubuntu3.10] (2.35-0ubuntu3.11 Ubuntu:22.04/jammy-updates [amd64])\n"
	simZlib    = "Inst zlib1g [1:1.2.11] (1:1.2.12 Ubuntu:22.04/jammy-updates [amd64])\n"
	simDockerC = "Inst docker-ce [5:29.8.1-1~ubuntu.22.04~jammy] (5:29.8.2-1~ubuntu.22.04~jammy Docker CE:jammy [amd64])\n"
	simRootles = "Inst docker-ce-rootless-extras [5:29.8.1-1~ubuntu.22.04~jammy] (5:29.8.2-1~ubuntu.22.04~jammy Docker CE:jammy [amd64])\n"
	simPlugin  = "Inst docker-compose-plugin [2.40.0-1~ubuntu.22.04~jammy] (2.40.1-1~ubuntu.22.04~jammy Docker CE:jammy [amd64])\n"
	simContain = "Inst containerd.io [2.3.5-1~ubuntu.22.04~jammy] (2.3.6-1~ubuntu.22.04~jammy Docker CE:jammy [amd64])\n"
	simNew     = "Inst brand-new-dependency (1 Ubuntu:22.04 [amd64])\n"
	dockerRepo = "docker-ce:\n  Installed: 5:29.8.1-1~ubuntu.22.04~jammy\n  Candidate: 5:29.8.2-1~ubuntu.22.04~jammy\n  Version table:\n     5:29.8.2-1~ubuntu.22.04~jammy 500\n        500 https://download.docker.com/linux/ubuntu jammy/stable amd64 Packages\n *** 5:29.8.1-1~ubuntu.22.04~jammy 100\n        100 /var/lib/dpkg/status\n"
)

const dockerCE = "5:29.8.1-1~ubuntu.22.04~jammy"

// the engine is at the newest version its source offers: only a plugin is behind
const currentPolicy = "docker-ce:\n  Installed: 5:29.8.1-1~ubuntu.22.04~jammy\n  Candidate: 5:29.8.1-1~ubuntu.22.04~jammy\n  Version table:\n *** 5:29.8.1-1~ubuntu.22.04~jammy 500\n        500 https://download.docker.com/linux/ubuntu jammy/stable amd64 Packages\n"

func TestSystemPackageCheckListsDockerOnALineOfItsOwn(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	box := &aptBox{
		upgradeSimulation: simLibc + simDockerC + simContain + simRootles + simZlib + simNew,
		dockerCE:          dockerCE,
		policy:            dockerRepo,
	}
	updater.command = box.command

	got, err := updater.check()
	if err != nil {
		t.Fatalf("check() error = %v", err)
	}
	if got.Count != 2 || len(got.Updates) != 2 || got.Updates[0].Name != "libc6" || got.Updates[1].Name != "zlib1g" {
		t.Fatalf("the update list holds %#v, want libc6 and zlib1g only: no Docker, and no package that is not an upgrade", got.Updates)
	}
	if got.Docker == nil || !got.Docker.Installed || got.Docker.Version != "29.8.1" || got.Docker.Origin != "docker-repository" || !got.Docker.RestartsDocker {
		t.Fatalf("Docker = %#v", got.Docker)
	}
	if len(got.Docker.Updates) != 3 {
		t.Fatalf("Docker's updates = %#v", got.Docker.Updates)
	}
	// the command updates everything the line lists, so that running it clears the line
	for _, name := range []string{"docker-ce", "containerd.io", "docker-ce-rootless-extras"} {
		if !strings.Contains(got.Docker.ManualCommand, name) {
			t.Errorf("manual command %q lacks %s", got.Docker.ManualCommand, name)
		}
	}
}

func TestSystemPackageCheckSaysWhenDockerRestartsAndWhenItDoesNot(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{upgradeSimulation: simPlugin, dockerCE: dockerCE, policy: currentPolicy}).command

	got, err := updater.check()
	if err != nil || got.Docker == nil || len(got.Docker.Updates) != 1 || got.Docker.RestartsDocker {
		t.Fatalf("check() = %#v, %v: a plugin does not restart Docker", got.Docker, err)
	}
}

func TestSystemPackageCheckHasNoDockerLineWithoutDocker(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{upgradeSimulation: simLibc}).command

	got, err := updater.check()
	if err != nil || got.Count != 1 || got.Docker != nil {
		t.Fatalf("check() = %#v, %v", got, err)
	}
}

func TestSystemPackageCheckLeavesContainerdAloneOnABoxWithoutDocker(t *testing.T) {
	// a Kubernetes node: containerd is an ordinary package there
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{upgradeSimulation: simLibc + simContain}).command

	got, err := updater.check()
	if err != nil || got.Count != 2 || got.Docker != nil {
		t.Fatalf("check() = %#v, %v: containerd.io belongs in the list when there is no Docker engine", got, err)
	}
}

func TestSystemPackageCheckKnowsWhereDockerCameFrom(t *testing.T) {
	cases := map[string]struct {
		box    aptBox
		origin string
	}{
		"distribution": {aptBox{dockerIO: "26.1.5+dfsg1-9+deb13u1"}, "distribution"},
		"snap":         {aptBox{snap: true}, "snap"},
		"by hand":      {aptBox{dockerCE: "5:24.0.5-1", policy: "docker-ce:\n  Version table:\n *** 5:24.0.5-1 100\n        100 /var/lib/dpkg/status\n"}, "unknown"},
		"held":         {aptBox{dockerCE: dockerCE, dockerCEStatus: "hi", policy: dockerRepo}, "docker-repository"},
	}
	for name, c := range cases {
		box := c.box
		updater := newTestSystemPackageUpdater(t)
		updater.command = box.command
		got, err := updater.check()
		if err != nil || got.Docker == nil || got.Docker.Origin != c.origin || !got.Docker.Installed {
			t.Errorf("%s: check() = %#v, %v; want origin %q", name, got.Docker, err, c.origin)
		}
	}
}

func TestInstalledVersionReadsDpkgStatuses(t *testing.T) {
	for status, want := range map[string]string{"ii": "1.0", "hi": "1.0", "iF": "1.0", "it": "1.0", "rc": "", "un": "", "pn": "", "": ""} {
		updater := newTestSystemPackageUpdater(t)
		updater.command = (&aptBox{dockerCE: "1.0", dockerCEStatus: status}).command
		if status == "" {
			updater.command = (&aptBox{}).command
		}
		if got := updater.installedVersion(context.Background(), "docker-ce"); got != want {
			t.Errorf("status %q: installedVersion = %q, want %q", status, got, want)
		}
	}
}

func startedCommand(t *testing.T, updater *systemPackageUpdater) string {
	t.Helper()
	var started string
	updater.start = func(_ string, _ string, args ...string) ([]byte, error) {
		started = strings.Join(args, " ")
		return nil, nil
	}
	if _, err := updater.startUpdate(); err != nil {
		t.Fatalf("startUpdate() error = %v", err)
	}
	return started
}

func TestSystemPackageUpdateInstallsTheListAndLeavesDockerOut(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	box := &aptBox{
		upgradeSimulation: simLibc + simDockerC + simContain + simZlib + simNew,
		installSimulation: simLibc + simZlib,
		dockerCE:          dockerCE,
		policy:            dockerRepo,
	}
	updater.command = box.command

	started := startedCommand(t, updater)
	for _, want := range []string{
		"install --only-upgrade --no-install-recommends 'libc6' 'zlib1g'; status=$?; fi;",
		"-o DPkg::Lock::Timeout=120",
		"--no-remove",
		"CASAOS_PACKAGE_UPDATE_GUARD",
	} {
		if !strings.Contains(started, want) {
			t.Errorf("the unit's command lacks %q: %s", want, started)
		}
	}
	// Docker's names appear only in the guard's pattern, which is what keeps them out
	withoutGuard := strings.Replace(started, dockerpkg.ContractPattern(true), "", -1)
	for _, unwanted := range []string{"docker-ce", "containerd", "brand-new-dependency", " upgrade;"} {
		if strings.Contains(withoutGuard, unwanted) {
			t.Errorf("the unit's command has %q: %s", unwanted, withoutGuard)
		}
	}
	// the list was simulated as it will be installed, before it was started
	var simulated bool
	for _, call := range box.calls {
		if strings.Contains(call, "-s") && strings.Contains(call, "install --only-upgrade --no-install-recommends libc6 zlib1g") {
			simulated = true
		}
	}
	if !simulated {
		t.Errorf("the explicit list was not simulated: %#v", box.calls)
	}
}

func TestSystemPackageUpdateWithoutDockerKeepsEverythingInTheList(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{upgradeSimulation: simLibc + simContain, installSimulation: simLibc + simContain}).command

	started := startedCommand(t, updater)
	if !strings.Contains(started, "'containerd.io'") || !strings.Contains(started, "'libc6'") {
		t.Errorf("a box with no Docker engine lost containerd.io from its update: %s", started)
	}
}

func TestSystemPackageUpdateWithOnlyDockerLeftIsRefusedAndWritesNothing(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{upgradeSimulation: simDockerC + simContain, dockerCE: dockerCE, policy: dockerRepo}).command
	var started bool
	updater.start = func(string, string, ...string) ([]byte, error) { started = true; return nil, nil }

	_, err := updater.startUpdate()
	if !errors.Is(err, ErrSystemPackageNothingToUpdate) || !strings.Contains(err.Error(), "Docker") {
		t.Fatalf("startUpdate() error = %v, want ErrSystemPackageNothingToUpdate naming Docker", err)
	}
	if started {
		t.Fatal("a unit was started with nothing to install")
	}
	if _, statErr := os.Stat(updater.logPath()); !os.IsNotExist(statErr) {
		t.Fatalf("a log was written: %v", statErr)
	}
}

func TestSystemPackageUpdateWithNothingLeftDoesNotBlameDocker(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{}).command

	_, err := updater.startUpdate()
	if !errors.Is(err, ErrSystemPackageNothingToUpdate) || strings.Contains(err.Error(), "Docker") {
		t.Fatalf("startUpdate() error = %v, want a plain 'nothing to update'", err)
	}
}

func TestSystemPackageUpdateRefusesAListThatWouldChangeDocker(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{
		upgradeSimulation: simLibc,
		// something in the list depends on a newer docker-ce
		installSimulation: simLibc + simDockerC,
		dockerCE:          dockerCE,
	}).command
	var started bool
	updater.start = func(string, string, ...string) ([]byte, error) { started = true; return nil, nil }

	_, err := updater.startUpdate()
	if !errors.Is(err, ErrSystemPackageTouchesDocker) || !strings.Contains(err.Error(), "docker-ce") {
		t.Fatalf("startUpdate() error = %v, want ErrSystemPackageTouchesDocker naming docker-ce", err)
	}
	if started {
		t.Fatal("a unit was started for a list that changes Docker")
	}
}

func TestSystemPackageUpdateRefusesAListThatIsNotTheCheckedUpgrades(t *testing.T) {
	cases := map[string]aptBox{
		"a removal":                 {installSimulation: simLibc + "Remv oldthing [1.0]\n"},
		"apt refusing to remove":    {installFailure: "E: Packages need to be removed but remove is disabled."},
		"a package that is not new": {installSimulation: simLibc + simNew},
	}
	for name, c := range cases {
		box := c
		box.upgradeSimulation = simLibc
		updater := newTestSystemPackageUpdater(t)
		updater.command = box.command
		var started bool
		updater.start = func(string, string, ...string) ([]byte, error) { started = true; return nil, nil }

		_, err := updater.startUpdate()
		if !errors.Is(err, ErrSystemPackageListChanged) || started {
			t.Errorf("%s: startUpdate() error = %v, started = %v", name, err, started)
		}
	}
}

func TestSystemPackageUpdateLeavesAFunnyNameOutAndGoesOn(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{upgradeSimulation: "Inst foo;rm [1] (2 Ubuntu [amd64])\n" + simLibc, installSimulation: simLibc}).command

	started := startedCommand(t, updater)
	if strings.Contains(started, "foo") || !strings.Contains(started, "'libc6'") {
		t.Fatalf("the command = %s: a name with a semicolon must be left out, the others kept", started)
	}
	if _, err := systemPackageUpdateCommand("/usr/bin/apt-get", "/var/log/x.log", []string{"zlib1g", "x; rm -rf /"}, true); err == nil {
		t.Fatal("systemPackageUpdateCommand() accepted a name with a semicolon")
	}
	if _, err := systemPackageUpdateCommand("/usr/bin/apt-get", "/var/log/x.log", nil, true); !errors.Is(err, ErrSystemPackageNothingToUpdate) {
		t.Fatalf("systemPackageUpdateCommand(nil) error = %v", err)
	}
}

func TestSystemPackageUpdateWaitsForOtherMaintenance(t *testing.T) {
	cases := map[string]struct {
		box    aptBox
		locked bool
	}{
		"a ReCasaOS update":        {aptBox{activeUnits: map[string]bool{common.UPDATE_UNIT + ".service": true}}, false},
		"an update of Docker":      {aptBox{activeUnits: map[string]bool{systemDockerUpdateUnit: true}}, false},
		"a package manager's lock": {aptBox{}, true},
	}
	for name, c := range cases {
		box := c.box
		box.upgradeSimulation = simLibc
		box.installSimulation = simLibc
		updater := newTestSystemPackageUpdater(t)
		updater.command = box.command
		updater.dpkgLocked = func() bool { return c.locked }
		var started bool
		updater.start = func(string, string, ...string) ([]byte, error) { started = true; return nil, nil }

		_, err := updater.startUpdate()
		if !errors.Is(err, ErrSystemMaintenanceBusy) || started {
			t.Errorf("%s: startUpdate() error = %v, started = %v", name, err, started)
		}
	}
}

func TestSystemPackageUpdateLooksAgainRightBeforeItStarts(t *testing.T) {
	// a ReCasaOS update starts while the list is being simulated
	updater := newTestSystemPackageUpdater(t)
	box := &aptBox{upgradeSimulation: simLibc, installSimulation: simLibc, activeUnits: map[string]bool{}}
	box.onInstallSimulation = func() { box.activeUnits[common.UPDATE_UNIT+".service"] = true }
	updater.command = box.command
	var started bool
	updater.start = func(string, string, ...string) ([]byte, error) { started = true; return nil, nil }

	_, err := updater.startUpdate()
	if !errors.Is(err, ErrSystemMaintenanceBusy) || started {
		t.Fatalf("startUpdate() error = %v, started = %v: the box changed during the simulations and nothing looked again", err, started)
	}
	if _, statErr := os.Stat(updater.logPath()); !os.IsNotExist(statErr) {
		t.Fatalf("a log was written for an update that was not started: %v", statErr)
	}
}

func TestMaintenanceBusy(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	for _, unit := range systemMaintenanceUnits {
		box := &aptBox{activeUnits: map[string]bool{unit: true}}
		updater.command = box.command
		if reason, busy := updater.maintenanceBusy(context.Background(), ""); !busy || reason == "" {
			t.Errorf("%s active: busy = %v, reason = %q", unit, busy, reason)
		}
		// the caller's own unit is its own to judge
		if _, busy := updater.maintenanceBusy(context.Background(), unit); busy {
			t.Errorf("%s active but excepted: busy", unit)
		}
	}

	updater.command = (&aptBox{}).command
	if _, busy := updater.maintenanceBusy(context.Background(), ""); busy {
		t.Error("an idle box is busy")
	}

	// dpkg's lock, only on a host that has dpkg
	updater.dpkgLocked = func() bool { return true }
	if reason, busy := updater.maintenanceBusy(context.Background(), ""); !busy || !strings.Contains(reason, "dpkg") {
		t.Errorf("a held lock: busy = %v, reason = %q", busy, reason)
	}
	updater.readOSRelease = func() (map[string]string, error) { return map[string]string{"ID": "arch"}, nil }
	if _, busy := updater.maintenanceBusy(context.Background(), ""); busy {
		t.Error("a host without dpkg was refused for a lock it cannot have: the ReCasaOS update would never start there")
	}

	// systemd that does not answer is not a reason to refuse
	updater.readOSRelease = func() (map[string]string, error) { return map[string]string{"ID": "debian"}, nil }
	updater.dpkgLocked = nil
	updater.command = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemctl" {
			return nil, errors.New("systemctl: not found")
		}
		return nil, nil
	}
	if _, busy := updater.maintenanceBusy(context.Background(), ""); busy {
		t.Error("an unanswering systemctl counted as busy")
	}
}

func TestAReCasaOSUpdateWaitsForAPackageUpdate(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{activeUnits: map[string]bool{systemPackageUpdateUnit: true}}).command
	system := &systemService{packageUpdates: updater}

	err := system.UpdateSystemVersion("v0.0.0-test")
	if !errors.Is(err, ErrSystemMaintenanceBusy) {
		t.Fatalf("UpdateSystemVersion() error = %v, want ErrSystemMaintenanceBusy", err)
	}
	if !system.MaintenanceBusy() {
		t.Fatal("MaintenanceBusy() = false with a package update running")
	}
	updater.command = (&aptBox{}).command
	if system.MaintenanceBusy() {
		t.Fatal("MaintenanceBusy() = true on an idle box")
	}
}

// The unit's own shell line is run, with a fake apt-get, to see what it does and does not do.
func runUnitCommand(t *testing.T, plan string, protectDocker bool) (log string, installed bool) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the unit's command is for a Linux shell")
	}
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("the unit's command is run by bash, which this host does not have")
	}
	dir := t.TempDir()
	plans := filepath.Join(dir, "plan.txt")
	real := filepath.Join(dir, "real-install.txt")
	if err := os.WriteFile(plans, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	apt := filepath.Join(dir, "apt-get")
	script := "#!/bin/sh\ncase \" $* \" in\n*\" -s \"*) cat \"" + plans + "\" ;;\n*) echo \"$@\" >> \"" + real + "\" ;;\nesac\n"
	if err := os.WriteFile(apt, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "package-update.log")
	command, err := systemPackageUpdateCommand(apt, logPath, []string{"libc6", "zlib1g"}, protectDocker)
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("/bin/bash", "-o", "pipefail", "-c", command).Run(); err != nil {
		t.Logf("the unit's command ended with %v", err)
	}
	data, _ := os.ReadFile(logPath)
	_, statErr := os.Stat(real)
	return string(data), statErr == nil
}

func TestTheUnitInstallsWhenTheListIsStillClean(t *testing.T) {
	log, installed := runUnitCommand(t, simLibc+simZlib, true)
	if !installed || !strings.Contains(log, "CASAOS_PACKAGE_UPDATE_SUCCESS") || strings.Contains(log, "GUARD") {
		t.Fatalf("installed = %v, log = %q", installed, log)
	}
}

func TestTheUnitInstallsNothingWhenTheListNowTouchesDockerOrRemoves(t *testing.T) {
	for name, plan := range map[string]string{
		"docker-ce":   simLibc + simDockerC,
		"containerd":  simLibc + simContain,
		"a removal":   simLibc + "Remv oldthing [1.0]\n",
		"docker-ce:a": "Inst containerd.io:armhf [2.3.5] (2.3.6 Docker CE [armhf])\n",
	} {
		log, installed := runUnitCommand(t, plan, true)
		if installed || !strings.Contains(log, "CASAOS_PACKAGE_UPDATE_GUARD") || !strings.Contains(log, "CASAOS_PACKAGE_UPDATE_FAILED") || strings.Contains(log, "SUCCESS") {
			t.Errorf("%s: installed = %v, log = %q", name, installed, log)
		}
	}
}

func TestTheUnitOfABoxWithoutDockerStillInstallsContainerd(t *testing.T) {
	// the guard protects a Docker engine; on a box that has none, containerd.io is an ordinary package
	log, installed := runUnitCommand(t, simLibc+simContain, false)
	if !installed || !strings.Contains(log, "CASAOS_PACKAGE_UPDATE_SUCCESS") || strings.Contains(log, "GUARD") {
		t.Fatalf("installed = %v, log = %q", installed, log)
	}
	// ... but a removal still stops it
	log, installed = runUnitCommand(t, simLibc+"Remv oldthing [1.0]\n", false)
	if installed || !strings.Contains(log, "CASAOS_PACKAGE_UPDATE_GUARD") {
		t.Fatalf("installed = %v, log = %q", installed, log)
	}
}

const behindPolicy = "docker-ce:\n  Installed: 5:28.0.4-1~ubuntu.22.04~jammy\n  Candidate: 5:29.8.2-1~ubuntu.22.04~jammy\n  Version table:\n     5:29.8.2-1~ubuntu.22.04~jammy 500\n        500 https://download.docker.com/linux/ubuntu jammy/stable amd64 Packages\n *** 5:28.0.4-1~ubuntu.22.04~jammy 100\n        100 /var/lib/dpkg/status\n"

func TestSystemPackageCheckDoesNotCallABehindDockerUpToDate(t *testing.T) {
	// a Docker that is on hold, or that apt keeps back: no Inst line, and yet a newer version exists
	for name, status := range map[string]string{"on hold": "hi", "kept back": "ii"} {
		updater := newTestSystemPackageUpdater(t)
		updater.command = (&aptBox{upgradeSimulation: simLibc, dockerCE: "5:28.0.4-1~ubuntu.22.04~jammy", dockerCEStatus: status, policy: behindPolicy}).command

		got, err := updater.check()
		if err != nil || got.Docker == nil || len(got.Docker.Updates) != 0 {
			t.Fatalf("%s: check() = %#v, %v", name, got.Docker, err)
		}
		if got.Docker.Candidate != "29.8.2" || got.Docker.Held != (status == "hi") || !got.Docker.RestartsDocker {
			t.Errorf("%s: Docker = %#v, want candidate 29.8.2, held %v", name, got.Docker, status == "hi")
		}
		if !strings.Contains(got.Docker.ManualCommand, "docker-ce") || (status == "hi") != strings.Contains(got.Docker.ManualCommand, "--allow-change-held-packages") {
			t.Errorf("%s: manual command = %q", name, got.Docker.ManualCommand)
		}
	}
}

func TestSystemPackageCheckSaysNothingIsBehindWhenTheCandidateIsTheInstalledVersion(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	policy := "docker-ce:\n  Installed: 5:29.8.2-1~u\n  Candidate: 5:29.8.2-1~u\n  Version table:\n *** 5:29.8.2-1~u 500\n        500 https://download.docker.com/linux/ubuntu jammy/stable amd64 Packages\n"
	updater.command = (&aptBox{upgradeSimulation: simLibc, dockerCE: "5:29.8.2-1~u", policy: policy}).command

	got, err := updater.check()
	if err != nil || got.Docker == nil || got.Docker.Candidate != "" || got.Docker.Held {
		t.Fatalf("check() = %#v, %v: nothing is behind", got.Docker, err)
	}
}
