package service

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// moduleSystemd is a fake of one command of systemd, `systemctl show <name or pattern> --property=ActiveState --value`, and of nothing else of it. state maps the units that systemd has
// loaded to their ActiveState. It models exactly this, which has not been seen on a real system (the proof on a guest checks it: "To be proved on a real guest" in docs/os-modules.md):
//
//   - a name: the unit's ActiveState on a line, and "inactive" for a unit that is not loaded;
//   - a pattern (a word with a *, a ? or a [): the ActiveState of every loaded unit whose name the pattern matches as a shell glob (path.Match), in the order of the names, each followed by
//     an empty line, as systemctl separates the blocks of several units;
//   - a pattern that matches no unit: an empty output, and no error (exit status 0).
//
// What is read of the answer is its words (strings.Fields), so the reading does not depend on the empty lines; the matching of the pattern and the empty output are what the fake assumes.
func moduleSystemd(state map[string]string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "systemctl" || len(args) < 2 || args[0] != "show" {
			return nil, nil
		}
		if !strings.ContainsAny(args[1], "*?[") {
			if s, ok := state[args[1]]; ok {
				return []byte(s + "\n"), nil
			}
			return []byte("inactive\n"), nil
		}
		var out strings.Builder
		// (in the order of the names: a systemd says its units in an order, and a test must not depend on a map's)
		units := make([]string, 0, len(state))
		for unit := range state {
			units = append(units, unit)
		}
		sort.Strings(units)
		for _, unit := range units {
			s := state[unit]
			if ok, _ := path.Match(args[1], unit); ok {
				out.WriteString(s + "\n\n")
			}
		}
		return []byte(out.String()), nil
	}
}

func TestTheUnitsOfTheModulesAreAmongTheUnitsThatChangeTheBox(t *testing.T) {
	found := false
	for _, unit := range systemMaintenanceUnits {
		found = found || unit == "casaos-module-*.service"
	}
	if !found {
		t.Fatalf("the units of the modules are not among %v", systemMaintenanceUnits)
	}
	// and the name of the unit of a job is one that the pattern matches
	if ok, _ := path.Match("casaos-module-*.service", systemModuleUnit("virt")); !ok {
		t.Errorf("the unit %q is not matched by the pattern", systemModuleUnit("virt"))
	}
}

func TestAModuleJobKeepsEveryOtherChangeOfTheBoxAway(t *testing.T) {
	ctx := context.Background()
	for name, state := range map[string]map[string]string{
		"a unit that runs":                  {"casaos-module-virt.service": "active"},
		"a unit that starts":                {"casaos-module-virt.service": "activating"},
		"a unit that stops":                 {"casaos-module-virt.service": "deactivating"},
		"two, one of them idle":             {"casaos-module-a.service": "inactive", "casaos-module-b.service": "active"},
		"a unit of a module of a long name": {"casaos-module-" + strings.Repeat("a", 32) + ".service": "active"},
	} {
		t.Run(name, func(t *testing.T) {
			updater := newTestSystemPackageUpdater(t)
			updater.command = moduleSystemd(state)
			updater.dpkgLockState = func() (bool, error) { return false, nil }
			reason, busy := updater.maintenanceBusy(ctx, "")
			if !busy || !strings.Contains(reason, "module") {
				t.Errorf("maintenanceBusy() = %q, %v", reason, busy)
			}
			system := &systemService{packageUpdates: updater}
			if !system.MaintenanceBusy() {
				t.Error("the automatic updates are not kept away")
			}
			if err := system.UpdateSystemVersion("v0.0.0-test"); !errors.Is(err, ErrSystemMaintenanceBusy) {
				t.Errorf("UpdateSystemVersion() = %v", err)
			}
			var started bool
			updater.start = func(string, string, ...string) ([]byte, error) { started = true; return nil, nil }
			if _, err := updater.startUpdate(); !errors.Is(err, ErrSystemMaintenanceBusy) || started {
				t.Errorf("startUpdate() = %v, started = %v", err, started)
			}
			// the report of whether the machine could be upgraded, and so the start of the upgrade, see it
			probe := osReadinessProbe{updater: updater, command: func(ctx context.Context, name string, args ...string) ([]byte, bool, error) {
				out, err := updater.command(ctx, name, args...)
				return out, false, err
			}}
			var _ osreadiness.Probe = probe
			if _, busy, err := probe.MaintenanceBusy(ctx); !busy || err != nil {
				t.Errorf("the readiness report: busy = %v, err = %v", busy, err)
			}
		})
	}
}

func TestWhatIsNotAModuleJobDoesNotKeepTheBoxAway(t *testing.T) {
	ctx := context.Background()
	for name, state := range map[string]map[string]string{
		"nothing":                  {},
		"an idle unit":             {"casaos-module-virt.service": "inactive"},
		"a failed unit":            {"casaos-module-virt.service": "failed"},
		"a unit of another family": {"casaos-modules-virt.service": "active", "casaos-module.service": "active", "casaos-virt.service": "active", "my-casaos-module-x.service": "active"},
	} {
		t.Run(name, func(t *testing.T) {
			updater := newTestSystemPackageUpdater(t)
			updater.command = moduleSystemd(state)
			updater.dpkgLockState = func() (bool, error) { return false, nil }
			if reason, busy := updater.maintenanceBusy(ctx, ""); busy {
				t.Errorf("maintenanceBusy() = %q", reason)
			}
		})
	}
	// systemd that does not answer is not a reason to refuse, as for every unit
	updater := newTestSystemPackageUpdater(t)
	updater.command = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no bus") }
	if _, busy := updater.maintenanceBusy(ctx, ""); busy {
		t.Error("a systemd that is silent counted as busy")
	}
}

// The upgrade of the operating system, the update of Docker and the update of packages do not start while a job of a module runs.
func TestTheStartsOfTheOtherChangesWaitForAJobOfAModule(t *testing.T) {
	// the upgrade
	box := newUpgradeBox(t)
	box.stubStart()
	_, plan := box.aMachineThatCanStart()
	box.updater.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 1 && args[0] == "show" && args[1] == systemModuleUnits {
			return []byte("active\n"), nil
		}
		return box.command(ctx, name, args...)
	}
	_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeBusy {
		t.Errorf("the upgrade: %+v", refusal)
	}
	box.assertNothingChanged(nil)

	// the update of Docker
	updater := newTestSystemPackageUpdater(t)
	updater.command = moduleSystemd(map[string]string{"casaos-module-virt.service": "active"})
	updater.dpkgLockState = func() (bool, error) { return false, nil }
	updater.dpkgLocked = func() bool { return false }
	var started bool
	updater.start = func(string, string, ...string) ([]byte, error) { started = true; return nil, nil }
	_, err = updater.startDockerUpdate(strings.Repeat("a", 64), false)
	var refusal *DockerUpdateRefusal
	if !errors.As(err, &refusal) || refusal.Code != DockerRefusalMaintenance || started {
		t.Errorf("the update of Docker: %v, started = %v", err, started)
	}
}

// A run of the module assistant started by hand (`casaos module install` in a terminal) has no unit, and holds the assistant's lock for as long as it works: every change of the box sees it
// and keeps away, the start of the upgrade of the operating system among them. The lock is looked at, not kept: the run that wants it next is not refused for the look.
func TestARunOfTheAssistantByHandKeepsEveryOtherChangeOfTheBoxAway(t *testing.T) {
	ctx := context.Background()
	b := newModuleBox(t)
	b.write("/var/lib/casaos/modules/.keep", "")
	lock := b.env.Roots.Path("/var/lib/casaos/modules/.lock")
	release, err := b.env.Host.Lock(lock)
	if err != nil {
		t.Fatal(err)
	}
	reason, busy := b.updater.maintenanceBusy(ctx, "")
	if !busy || !strings.Contains(reason, "module assistant") {
		t.Errorf("maintenanceBusy() = %q, %v", reason, busy)
	}
	probe := osReadinessProbe{updater: b.updater, except: systemOSUpgradeUnit, command: func(ctx context.Context, name string, args ...string) ([]byte, bool, error) {
		out, err := b.updater.command(ctx, name, args...)
		return out, false, err
	}}
	if _, busy, err := probe.MaintenanceBusy(ctx); !busy || err != nil {
		t.Errorf("the start of the upgrade: busy = %v, err = %v", busy, err)
	}
	release()
	if reason, busy := b.updater.maintenanceBusy(ctx, ""); busy {
		t.Errorf("maintenanceBusy() = %q once the run has ended", reason)
	}
	again, err := b.env.Host.Lock(lock)
	if err != nil {
		t.Fatalf("the lock is kept by the look: %v", err)
	}
	again()
}

// A lock of the assistant that cannot be looked at does not hide dpkg's: a package manager that holds dpkg's lock is busy, and the error is said only when nothing is seen busy.
func TestALockOfTheAssistantThatCannotBeLookedAtDoesNotHideDpkgs(t *testing.T) {
	ctx := context.Background()
	b := newModuleBox(t)
	b.write("/var/lib/casaos/modules/.lock/in-the-way", "")
	b.mu.Lock()
	b.dpkg = true
	b.mu.Unlock()
	if reason, busy := b.updater.maintenanceBusy(ctx, ""); !busy || !strings.Contains(reason, "dpkg") {
		t.Errorf("maintenanceBusy() = %q, %v", reason, busy)
	}
	b.mu.Lock()
	b.dpkg = false
	b.mu.Unlock()
	if _, busy, err := b.updater.maintenanceState(ctx, "", b.updater.command); busy || err == nil {
		t.Errorf("nothing busy and a lock that cannot be looked at: busy = %v, err = %v", busy, err)
	}
}
