package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withOSUpgradeBusyFile puts the flag of an open upgrade in a folder of the test, and says where it is.
func withOSUpgradeBusyFile(t *testing.T) string {
	t.Helper()
	original := osUpgradeBusyFile
	path := filepath.Join(t.TempDir(), "busy")
	osUpgradeBusyFile = path
	t.Cleanup(func() { osUpgradeBusyFile = original })
	return path
}

func TestTheUnitOfAnUpgradeIsOneOfTheUnitsThatChangeTheBox(t *testing.T) {
	found := false
	for _, unit := range systemMaintenanceUnits {
		found = found || unit == systemOSUpgradeUnit
	}
	if !found || systemOSUpgradeUnit != "casaos-os-upgrade.service" {
		t.Fatalf("the unit %q is not among %v", systemOSUpgradeUnit, systemMaintenanceUnits)
	}
}

// With no flag and no unit the answer is what it was before the upgrade existed.
func TestWithNoOpenUpgradeTheBoxIsNotBusyForIt(t *testing.T) {
	withOSUpgradeBusyFile(t)
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{}).command
	if reason, busy := updater.maintenanceBusy(context.Background(), ""); busy {
		t.Fatalf("an idle box is busy: %q", reason)
	}
}

func TestAnOpenUpgradeKeepsEveryReaderOfMaintenanceStateAway(t *testing.T) {
	path := withOSUpgradeBusyFile(t)
	if err := os.WriteFile(path, []byte(`{"run_id":"x","state":"running"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{}).command

	reason, busy := updater.maintenanceBusy(context.Background(), "")
	if !busy || !strings.Contains(reason, "operating system upgrade") {
		t.Fatalf("maintenanceBusy() = %q, %v: an open upgrade does not keep the box busy", reason, busy)
	}
	// ... for the units that are not its own: the ReCasaOS update, the packages window, the Docker update
	for _, except := range []string{"casaos-update.service", systemPackageUpdateUnit, systemDockerUpdateUnit} {
		if _, busy := updater.maintenanceBusy(context.Background(), except); !busy {
			t.Errorf("an open upgrade lets the caller %s through", except)
		}
	}
	// ... and for the report of whether the box could be upgraded, and the update of Docker
	probe := osReadinessProbe{updater: updater, command: func(ctx context.Context, name string, args ...string) ([]byte, bool, error) {
		out, err := updater.command(ctx, name, args...)
		return out, false, err
	}}
	if reason, busy, err := probe.MaintenanceBusy(context.Background()); !busy || err != nil {
		t.Errorf("the readiness report is told %q, %v, %v", reason, busy, err)
	}
	if status, err := updater.dockerBlocked(systemPackageSupport{supported: true}); err == nil {
		t.Errorf("the update of Docker is not refused: %+v", status)
	}
	// the engine's own start does not count the flag against itself
	if _, busy := updater.maintenanceBusy(context.Background(), systemOSUpgradeUnit); busy {
		t.Error("the upgrade's own unit is kept away by its own flag")
	}
}

func TestAnOpenUpgradeRefusesTheReCasaOSUpdate(t *testing.T) {
	path := withOSUpgradeBusyFile(t)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{}).command
	system := &systemService{packageUpdates: updater}
	if err := system.UpdateSystemVersion("v0.0.0-test"); !errors.Is(err, ErrSystemMaintenanceBusy) {
		t.Fatalf("UpdateSystemVersion() error = %v, want ErrSystemMaintenanceBusy", err)
	}
	if !system.MaintenanceBusy() {
		t.Error("the automatic updater is not kept away")
	}
}

func TestTheUnitOfAnUpgradeThatRunsKeepsTheBoxBusy(t *testing.T) {
	withOSUpgradeBusyFile(t)
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{activeUnits: map[string]bool{systemOSUpgradeUnit: true}}).command
	reason, busy := updater.maintenanceBusy(context.Background(), "")
	if !busy || !strings.Contains(reason, "casaos-os-upgrade") {
		t.Fatalf("maintenanceBusy() = %q, %v", reason, busy)
	}
}

// Only a flag that is not there is no flag: a folder that cannot be read says nothing of whether the upgrade is open.
func TestTheFlagOfAnOpenUpgradeIsReadFailClosed(t *testing.T) {
	original := osUpgradeBusyFile
	t.Cleanup(func() { osUpgradeBusyFile = original })
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{}).command

	for name, path := range map[string]string{
		"a parent that is a file":   filepath.Join(file, "busy"),
		"a name with a NUL":         filepath.Join(dir, "bu\x00sy"),
		"a link that leads nowhere": filepath.Join(dir, "dangling"),
	} {
		if name == "a link that leads nowhere" {
			if err := os.Symlink(filepath.Join(dir, "nowhere"), path); err != nil {
				t.Skipf("no symbolic links here: %v", err)
			}
		}
		osUpgradeBusyFile = path
		if reason, busy := updater.maintenanceBusy(context.Background(), ""); !busy || reason == "" {
			t.Errorf("%s: busy = %v, reason = %q: a flag that cannot be read was taken for no flag", name, busy, reason)
		}
	}

	osUpgradeBusyFile = filepath.Join(dir, "no-such-folder", "busy")
	if reason, busy := updater.maintenanceBusy(context.Background(), ""); busy {
		t.Errorf("a flag that is not there is busy: %q", reason)
	}
}

func TestTheBusyFlagReaderSaysWhatItSaw(t *testing.T) {
	path := withOSUpgradeBusyFile(t)
	if busy, err := osUpgradeFlagOpen(); busy || err != nil {
		t.Fatalf("no flag: %v, %v", busy, err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if busy, err := osUpgradeFlagOpen(); !busy || err != nil {
		t.Fatalf("a flag: %v, %v", busy, err)
	}
	osUpgradeBusyFile = filepath.Join(path, "below")
	if busy, err := osUpgradeFlagOpen(); !busy || err == nil {
		t.Fatalf("a flag that cannot be read: %v, %v: it is open, and the error is there for the log", busy, err)
	}
}
