package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// An upgradeBox is a machine for the tests of the upgrade of the operating system: folders of its own where the core keeps the runs and where the blocks keep
// their backups, a clock that a test moves, a systemd that says what the test tells it and that records every unit that is started, and the boot the box is in.
// Everything that several goroutines touch has its lock.
type upgradeBox struct {
	*systemService
	t       *testing.T
	updater *systemPackageUpdater
	root    string // where the run folders are
	backups string // where the backup folders of the hops are
	etc     string

	mu       sync.Mutex
	clock    time.Time
	unit     string // what systemctl says of the unit's ActiveState; "" means that it does not answer
	started  [][]string
	startErr error
	boot     string
	commands []string
	// what systemctl says of the units it is asked about: is-active for a unit that is not here is "inactive", and is-system-running is "running" unless a test says another word
	active map[string]string
	system string
	// stopErr is what stopping a unit says, and onStop is called (with the box's lock held) when the unit of the upgrade is stopped
	stopErr error
	onStop  func()
}

const (
	boxBoot1 = "11111111-2222-4333-8444-555555555555"
	boxBoot2 = "66666666-7777-4888-9999-aaaaaaaaaaaa"
)

func newUpgradeBox(t *testing.T) *upgradeBox {
	t.Helper()
	b := &upgradeBox{t: t, unit: "inactive", boot: boxBoot1, clock: time.Date(2026, time.October, 13, 10, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	b.root, b.backups, b.etc = filepath.Join(dir, "lib", "os-upgrade"), filepath.Join(dir, "backups"), filepath.Join(dir, "etc")
	for _, d := range []string{b.backups, b.etc} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b.updater = newTestSystemPackageUpdater(t)
	b.updater.now = b.now
	b.updater.command = b.command
	b.updater.start = func(path, unit string, args ...string) ([]byte, error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.started = append(b.started, append([]string{path, unit}, args...))
		return []byte("systemd-run said something"), b.startErr
	}
	b.systemService = &systemService{packageUpdates: b.updater}

	originals := struct {
		root, backups, busy, beta string
		boot                      func() (string, bool)
	}{osUpgradeRoot, osUpgradeBackups, osUpgradeBusyFile, osUpgradeBetaFile, osUpgradeBootID}
	osUpgradeRoot, osUpgradeBackups = b.root, b.backups
	osUpgradeBusyFile, osUpgradeBetaFile = filepath.Join(b.root, "busy"), filepath.Join(b.etc, "os-upgrade.beta")
	osUpgradeBootID = func() (string, bool) {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.boot, b.boot != ""
	}
	t.Cleanup(func() {
		osUpgradeRoot, osUpgradeBackups, osUpgradeBusyFile, osUpgradeBetaFile, osUpgradeBootID = originals.root, originals.backups, originals.busy, originals.beta, originals.boot
	})
	return b
}

func (b *upgradeBox) now() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.clock
}

func (b *upgradeBox) advance(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clock = b.clock.Add(d)
}

func (b *upgradeBox) setUnit(state string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.unit = state
}

func (b *upgradeBox) setBoot(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.boot = id
}

// setActive says that systemd calls a unit active (or another word) from now on.
func (b *upgradeBox) setActive(unit, state string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active == nil {
		b.active = map[string]string{}
	}
	b.active[unit] = state
}

func (b *upgradeBox) setSystem(state string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.system = state
}

// commandsWith are the commands that were run and begin with these words, in order.
func (b *upgradeBox) commandsWith(prefix string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, c := range b.commands {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func (b *upgradeBox) startedUnits() [][]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([][]string(nil), b.started...)
}

// command is the box's systemd: it answers about the unit of the upgrade with what the test set, and says that any other unit is not running.
func (b *upgradeBox) command(_ context.Context, name string, args ...string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.commands = append(b.commands, name+" "+strings.Join(args, " "))
	if name == "systemctl" && len(args) >= 2 && args[0] == "show" {
		if args[1] == systemOSUpgradeUnit {
			if b.unit == "" {
				return nil, os.ErrDeadlineExceeded
			}
			return []byte(b.unit + "\n"), nil
		}
		return []byte("inactive\n"), nil
	}
	if name == "systemctl" && len(args) >= 2 && args[0] == "is-active" {
		if state, ok := b.active[args[1]]; ok {
			return []byte(state + "\n"), nil
		}
		return []byte("inactive\n"), errors.New("exit status 3")
	}
	if name == "systemctl" && len(args) >= 1 && args[0] == "is-system-running" {
		if b.system == "" {
			return []byte("running\n"), nil
		}
		return []byte(b.system + "\n"), errors.New("exit status 1")
	}
	if name == "systemctl" && len(args) >= 2 && args[0] == "stop" {
		if b.stopErr != nil {
			return nil, b.stopErr
		}
		if args[1] == systemOSUpgradeUnit {
			if b.onStop != nil {
				b.onStop()
			}
			b.unit = "inactive"
		}
		delete(b.active, args[1])
		return nil, nil
	}
	return nil, nil
}

// anUpgradePlan is a live plan of the box: the job is done, and the plan is the one that the owner would confirm.
func (b *upgradeBox) anUpgradePlan(verdict string, edit func(*osplan.Plan)) *osplan.Plan {
	b.t.Helper()
	plan := aPlan(b.now(), verdict)
	if edit != nil {
		edit(plan)
		plan.PlanID = osplan.PlanID(*plan)
	}
	b.osPlan.state.Store(&osPlanState{state: osPlanDone, startedAt: b.now(), finishedAt: b.now(), plan: plan})
	return plan
}

// aRun writes the folder of a run for a plan, as a start does (its log has QUEUED and RUN), and gives the run as the core reads it.
func (b *upgradeBox) aRun(plan *osplan.Plan, kind string) *osUpgradeRun {
	b.t.Helper()
	runID := "0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19"
	if kind == osrun.RunRehearsal {
		runID = "7c1d9e44-2b6f-4a8e-8d35-1e0f6a9b3c27"
	}
	auto, err := osplan.BuildAuto(*plan, osplan.AutoParams{RunID: runID, Hostname: "box1"})
	if err != nil {
		b.t.Fatal(err)
	}
	manual, err := osplan.BuildRunbook(*plan)
	if err != nil {
		b.t.Fatal(err)
	}
	planJSON := []byte(`{"plan_id":"` + plan.PlanID + `"}`)
	spec := osUpgradeRunSpec{
		Meta: osUpgradeRunMeta{
			RunID: runID, Kind: kind, Hop: plan.Hop, PlanID: plan.PlanID, SafetyNet: "snapshot",
			BK: osUpgradeUnitBackups + "/casaos-os-upgrade-" + plan.PlanID, CreatedAt: b.now(), Blocks: osUpgradeBlockInfos(auto.Blocks, auto.Forward),
		},
		Driver: osrun.Driver, Blocks: auto.Blocks, Forward: auto.Forward, Plan: planJSON, Manual: manual.Text,
		Facts: osverify.Facts{Taken: true, At: b.now(), DpkgQuiet: true},
	}
	if err := writeOSUpgradeRun(spec); err != nil {
		b.t.Fatal(err)
	}
	nonce := "0123456789abcdef0123456789abcdef"
	dir := osUpgradeRunFolder(runID)
	if err := createOSUpgradeMarkers(dir, nonce, b.now()); err != nil {
		b.t.Fatal(err)
	}
	run := osrun.Marker{Kind: osrun.MkRun, RunID: runID, Hop: plan.Hop, PlanID: plan.PlanID, RunKind: kind, Boot: boxBoot1}
	if err := appendOSUpgradeMarker(dir, nonce, run); err != nil {
		b.t.Fatal(err)
	}
	return b.read(runID)
}

func (b *upgradeBox) read(runID string) *osUpgradeRun {
	b.t.Helper()
	run, err := readOSUpgradeRun(runID)
	if err != nil {
		b.t.Fatal(err)
	}
	return run
}

// say appends markers to the log of a run as the unit or the core would, at the time of the box's clock.
func (b *upgradeBox) say(run *osUpgradeRun, markers ...osrun.Marker) *osUpgradeRun {
	b.t.Helper()
	for _, m := range markers {
		if m.At.IsZero() {
			m.At = b.now()
		}
		if err := appendOSUpgradeMarker(run.Dir, run.Nonce, m); err != nil {
			b.t.Fatal(err)
		}
	}
	return b.read(run.ID)
}

// backupOf is the backup folder of a run, made if it is not there; leave puts files in it, as the blocks do.
func (b *upgradeBox) backupOf(run *osUpgradeRun) string {
	b.t.Helper()
	dir := osUpgradeBackupFolder(run.Meta.BK)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		b.t.Fatal(err)
	}
	return dir
}

func (b *upgradeBox) leave(run *osUpgradeRun, names ...string) *osUpgradeRun {
	b.t.Helper()
	dir := b.backupOf(run)
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o600); err != nil {
			b.t.Fatal(err)
		}
	}
	return b.read(run.ID)
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
