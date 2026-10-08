package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/common"
)

// aptBox answers the commands the System packages updater runs, from its fields.
type aptBox struct {
	upgradeSimulation string // apt-get -s upgrade
	installSimulation string // apt-get -s install --only-upgrade <names>
	activeUnits       map[string]bool
	dpkgLocked        bool
	flockMissing      bool
	dockerCE          string // the dpkg version of docker-ce, "" when it is not installed
	dockerIO          string
	policy            string
	snap              bool
	calls             []string
}

func (b *aptBox) command(_ context.Context, name string, args ...string) ([]byte, error) {
	b.calls = append(b.calls, name+" "+strings.Join(args, " "))
	switch name {
	case "systemctl":
		if len(args) > 1 && b.activeUnits[args[1]] {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), nil
	case "flock":
		if b.flockMissing {
			return nil, &exec.Error{Name: "flock", Err: exec.ErrNotFound}
		}
		if b.dpkgLocked {
			return nil, &exec.ExitError{}
		}
		return nil, nil
	case "dpkg-query":
		pkg := args[len(args)-1]
		if pkg == "docker-ce" && b.dockerCE != "" {
			return []byte("ii \t" + b.dockerCE + "\n"), nil
		}
		if pkg == "docker.io" && b.dockerIO != "" {
			return []byte("ii \t" + b.dockerIO + "\n"), nil
		}
		return nil, errors.New("dpkg-query: no packages found matching " + pkg)
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
			return []byte(b.installSimulation), nil
		case strings.Contains(joined, " -s "):
			return []byte(b.upgradeSimulation), nil
		}
	}
	return nil, nil
}

const (
	simLibc    = "Inst libc6 [2.35-0ubuntu3.10] (2.35-0ubuntu3.11 Ubuntu:22.04/jammy-updates [amd64])\n"
	simZlib    = "Inst zlib1g [1:1.2.11] (1:1.2.12 Ubuntu:22.04/jammy-updates [amd64])\n"
	simDockerC = "Inst docker-ce [5:29.8.1-1~ubuntu.22.04~jammy] (5:29.8.2-1~ubuntu.22.04~jammy Docker CE:jammy [amd64])\n"
	simContain = "Inst containerd.io [2.3.5-1~ubuntu.22.04~jammy] (2.3.6-1~ubuntu.22.04~jammy Docker CE:jammy [amd64])\n"
	dockerRepo = "docker-ce:\n  Installed: 5:29.8.1-1~ubuntu.22.04~jammy\n  Candidate: 5:29.8.2-1~ubuntu.22.04~jammy\n  Version table:\n     5:29.8.2-1~ubuntu.22.04~jammy 500\n        500 https://download.docker.com/linux/ubuntu jammy/stable amd64 Packages\n *** 5:29.8.1-1~ubuntu.22.04~jammy 100\n        100 /var/lib/dpkg/status\n"
)

func TestSystemPackageCheckListsDockerOnALineOfItsOwn(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	box := &aptBox{
		upgradeSimulation: simLibc + simDockerC + simContain + simZlib,
		dockerCE:          "5:29.8.1-1~ubuntu.22.04~jammy",
		policy:            dockerRepo,
	}
	updater.command = box.command

	got, err := updater.check()
	if err != nil {
		t.Fatalf("check() error = %v", err)
	}
	if got.Count != 2 || len(got.Updates) != 2 || got.Updates[0].Name != "libc6" || got.Updates[1].Name != "zlib1g" {
		t.Fatalf("the update list holds %#v, want libc6 and zlib1g only", got.Updates)
	}
	if got.Docker == nil || !got.Docker.Installed || got.Docker.Version != "29.8.1" || got.Docker.Origin != "docker-repository" {
		t.Fatalf("Docker = %#v", got.Docker)
	}
	if len(got.Docker.Updates) != 2 || got.Docker.Updates[0].Name != "containerd.io" || got.Docker.Updates[1].Name != "docker-ce" {
		t.Fatalf("Docker's updates = %#v", got.Docker.Updates)
	}
	if !strings.Contains(got.Docker.ManualCommand, "--only-upgrade docker-ce docker-ce-cli containerd.io") {
		t.Fatalf("manual command = %q", got.Docker.ManualCommand)
	}
}

func TestSystemPackageCheckHasNoDockerLineWithoutDocker(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	box := &aptBox{upgradeSimulation: simLibc}
	updater.command = box.command

	got, err := updater.check()
	if err != nil || got.Count != 1 || got.Docker != nil {
		t.Fatalf("check() = %#v, %v", got, err)
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
	}
	for name, c := range cases {
		box := c.box
		updater := newTestSystemPackageUpdater(t)
		updater.command = box.command
		got, err := updater.check()
		if err != nil || got.Docker == nil || got.Docker.Origin != c.origin {
			t.Errorf("%s: check() = %#v, %v; want origin %q", name, got.Docker, err, c.origin)
		}
	}
}

func TestSystemPackageCheckShowsDockerPendingEvenWhenItIsNotInstalledByDpkg(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	box := &aptBox{upgradeSimulation: simContain}
	updater.command = box.command

	got, err := updater.check()
	if err != nil || got.Count != 0 || got.Docker == nil || len(got.Docker.Updates) != 1 {
		t.Fatalf("check() = %#v, %v", got, err)
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
		upgradeSimulation: simLibc + simDockerC + simContain + simZlib + "Inst brand-new-dependency (1 Ubuntu:22.04 [amd64])\n",
		installSimulation: simLibc + simZlib,
	}
	updater.command = box.command

	started := startedCommand(t, updater)
	for _, want := range []string{
		"install --only-upgrade --no-install-recommends 'libc6' 'zlib1g';",
		"-o DPkg::Lock::Timeout=120",
		"--no-remove",
	} {
		if !strings.Contains(started, want) {
			t.Errorf("the unit's command lacks %q: %s", want, started)
		}
	}
	for _, unwanted := range []string{"docker", "containerd", "brand-new-dependency", " upgrade;"} {
		if strings.Contains(started, unwanted) {
			t.Errorf("the unit's command has %q: %s", unwanted, started)
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

func TestSystemPackageUpdateWithOnlyDockerLeftIsRefusedAndWritesNothing(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{upgradeSimulation: simDockerC + simContain}).command
	var started bool
	updater.start = func(string, string, ...string) ([]byte, error) { started = true; return nil, nil }

	_, err := updater.startUpdate()
	if !errors.Is(err, ErrSystemPackageNothingToUpdate) {
		t.Fatalf("startUpdate() error = %v, want ErrSystemPackageNothingToUpdate", err)
	}
	if started {
		t.Fatal("a unit was started with nothing to install")
	}
	if _, statErr := os.Stat(updater.logPath()); !os.IsNotExist(statErr) {
		t.Fatalf("a log was written: %v", statErr)
	}
}

func TestSystemPackageUpdateRefusesAListThatWouldChangeDocker(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{
		upgradeSimulation: simLibc,
		// something in the list depends on a newer docker-ce
		installSimulation: simLibc + simDockerC,
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

func TestSystemPackageUpdateRefusesAListThatWouldRemoveSomething(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{upgradeSimulation: simLibc, installSimulation: simLibc + "Remv oldthing [1.0]\n"}).command
	_, err := updater.startUpdate()
	if err == nil || !strings.Contains(err.Error(), "oldthing") {
		t.Fatalf("startUpdate() error = %v, want a refusal naming oldthing", err)
	}
}

func TestSystemPackageUpdateNeverPutsAFunnyNameInAShellLine(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{upgradeSimulation: "Inst foo;rm [1] (2 Ubuntu [amd64])\n"}).command
	var started bool
	updater.start = func(string, string, ...string) ([]byte, error) { started = true; return nil, nil }

	_, err := updater.startUpdate()
	if err == nil || started {
		t.Fatalf("startUpdate() error = %v, started = %v: a name with a semicolon got through", err, started)
	}
	if _, err := systemPackageUpdateCommand("/usr/bin/apt-get", "/var/log/x.log", []string{"zlib1g", "x; rm -rf /"}); err == nil {
		t.Fatal("systemPackageUpdateCommand() accepted a name with a semicolon")
	}
	if _, err := systemPackageUpdateCommand("/usr/bin/apt-get", "/var/log/x.log", nil); !errors.Is(err, ErrSystemPackageNothingToUpdate) {
		t.Fatalf("systemPackageUpdateCommand(nil) error = %v", err)
	}
}

func TestSystemPackageUpdateWaitsForOtherMaintenance(t *testing.T) {
	cases := map[string]aptBox{
		"a ReCasaOS update":        {activeUnits: map[string]bool{common.UPDATE_UNIT + ".service": true}},
		"an update of Docker":      {activeUnits: map[string]bool{systemDockerUpdateUnit: true}},
		"a package manager's lock": {dpkgLocked: true},
	}
	for name, c := range cases {
		box := c
		box.upgradeSimulation = simLibc
		box.installSimulation = simLibc
		updater := newTestSystemPackageUpdater(t)
		updater.command = box.command
		var started bool
		updater.start = func(string, string, ...string) ([]byte, error) { started = true; return nil, nil }

		_, err := updater.startUpdate()
		if !errors.Is(err, ErrSystemMaintenanceBusy) || started {
			t.Errorf("%s: startUpdate() error = %v, started = %v", name, err, started)
		}
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
	for name, box := range map[string]*aptBox{
		"nothing running":      {},
		"flock not installed":  {flockMissing: true},
		"lock held":            {dpkgLocked: true},
		"another unit stopped": {activeUnits: map[string]bool{}},
	} {
		updater.command = box.command
		_, busy := updater.maintenanceBusy(context.Background(), "")
		if want := box.dpkgLocked; busy != want {
			t.Errorf("%s: busy = %v, want %v", name, busy, want)
		}
	}
	// systemd that does not answer is not a reason to refuse
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
