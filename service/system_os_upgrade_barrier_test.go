package service

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// aMachineForTheBarrier is a machine in a map, for the checks that stand before the restart: the files that they read, and nothing that runs.
type aMachineForTheBarrier struct {
	mu    sync.Mutex
	files map[string]string
	fail  map[string]error
}

func (m *aMachineForTheBarrier) set(path, content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = content
}

func (m *aMachineForTheBarrier) breakAt(path string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail[path] = err
}

func (m *aMachineForTheBarrier) ReadFile(_ context.Context, path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail[path]; err != nil {
		return nil, err
	}
	if content, ok := m.files[path]; ok {
		return []byte(content), nil
	}
	return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
}

func (m *aMachineForTheBarrier) ReadHead(ctx context.Context, path string, _ int) ([]byte, error) {
	return m.ReadFile(ctx, path)
}

func (m *aMachineForTheBarrier) ReadDir(_ context.Context, path string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for p := range m.files {
		if dir, name := filepath.Split(p); strings.TrimSuffix(dir, "/") == path {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	sort.Strings(names)
	return names, nil
}

func (m *aMachineForTheBarrier) Stat(_ context.Context, path string) (osreadiness.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[path]; ok {
		return osreadiness.FileInfo{}, nil
	}
	return osreadiness.FileInfo{}, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
}

func (m *aMachineForTheBarrier) Lstat(ctx context.Context, path string) (osreadiness.FileInfo, error) {
	return m.Stat(ctx, path)
}

func (m *aMachineForTheBarrier) Readlink(context.Context, string) (string, error) {
	return "", fs.ErrInvalid
}

func (m *aMachineForTheBarrier) Run(context.Context, ...string) (osreadiness.Output, error) {
	return osreadiness.Output{}, osreadiness.ErrToolMissing
}

func (m *aMachineForTheBarrier) FreeBytes(context.Context, string) (uint64, error) { return 0, nil }

func (m *aMachineForTheBarrier) MaintenanceBusy(context.Context) (string, bool, error) {
	return "", false, nil
}

func (m *aMachineForTheBarrier) AppOperations(context.Context) ([]string, error) { return nil, nil }
func (m *aMachineForTheBarrier) Now() time.Time                                  { return time.Time{} }
func (m *aMachineForTheBarrier) EUID() int                                       { return 0 }
func (m *aMachineForTheBarrier) Arch() string                                    { return "amd64" }

// theBarrierSees makes the barrier look at a machine in a map: its fstab names a disk that is there.
func theBarrierSees(t *testing.T) *aMachineForTheBarrier {
	t.Helper()
	m := &aMachineForTheBarrier{files: map[string]string{
		"/etc/fstab":                  "UUID=1234-ABCD /data ext4 defaults 0 2\n",
		"/dev/disk/by-uuid/1234-ABCD": "",
	}, fail: map[string]error{}}
	was := osUpgradeBarrierProbe
	osUpgradeBarrierProbe = func(*systemPackageUpdater) osreadiness.Probe { return m }
	t.Cleanup(func() { osUpgradeBarrierProbe = was })
	return m
}

func (b *upgradeBox) barrierOf(run *osUpgradeRun) *osUpgradeBarrierDoc {
	b.t.Helper()
	return readOSUpgradeBarrier(b.read(run.ID))
}

// While the machine waits for its restart the core asks the three checks that stand before it, and says so in a file of the run: every one of them, in order, and whether the
// machine may be restarted.
func TestTheBarrierIsComputedWhileTheMachineWaitsForItsRestart(t *testing.T) {
	box := newUpgradeBox(t)
	theBarrierSees(t)
	run := box.aHopAtItsRestart()
	box.point(run)

	if got := box.barrierOf(run); got != nil {
		t.Fatalf("a barrier that was never computed is there: %+v", got)
	}
	tick := box.tick()
	got := box.barrierOf(run)
	want := &osUpgradeBarrierDoc{OK: true, Checks: []OSUpgradeBarrierItem{
		{Code: "fstab_unresolved", OK: true, Severity: "ok"}, {Code: "fstab_network", OK: true, Severity: "ok"}, {Code: "network_names", OK: true, Severity: "info"},
	}}
	if !reflect.DeepEqual(got, want) || tick.State != osrun.StateAwaitingReboot {
		t.Errorf("the barrier is %+v in state %q, want %+v", got, tick.State, want)
	}
	// and the button is offered, which it is not before the barrier is known
	if status := box.GetSystemOSUpgradeStatus(); !reflect.DeepEqual(status.Actions, []string{osrun.ActionReboot, osrun.ActionTakeover}) || status.Barrier == nil || !status.Barrier.OK {
		t.Errorf("the status says %+v", status)
	}
}

// A machine that would stop in emergency mode at its next start is not restarted by a click: the barrier is red, and the restart is not offered.
func TestARedBarrierTakesTheRestartAway(t *testing.T) {
	box := newUpgradeBox(t)
	machine := theBarrierSees(t)
	machine.set("/etc/fstab", "UUID=DEAD-BEEF /backup ext4 defaults 0 2\n")
	run := box.aHopAtItsRestart()
	box.point(run)

	box.tick()
	got := box.barrierOf(run)
	if got == nil || got.OK || got.Checks[0].Code != "fstab_unresolved" || got.Checks[0].OK || got.Checks[0].Severity != "refuse" {
		t.Fatalf("the barrier is %+v", got)
	}
	status := box.GetSystemOSUpgradeStatus()
	if reflect.DeepEqual(status.Actions, []string{osrun.ActionReboot, osrun.ActionTakeover}) || status.Barrier == nil || status.Barrier.OK {
		t.Errorf("the status says %+v: the restart is offered with a red barrier", status)
	}
	for _, action := range status.Actions {
		if action == osrun.ActionReboot {
			t.Errorf("actions %v offer the restart", status.Actions)
		}
	}
}

// The barrier is asked again at every look, so that the owner who fixes the line only waits for the next one: there is no route to ask it again.
func TestTheBarrierIsAskedAgainAtEveryLook(t *testing.T) {
	box := newUpgradeBox(t)
	machine := theBarrierSees(t)
	machine.set("/etc/fstab", "UUID=DEAD-BEEF /backup ext4 defaults 0 2\n")
	run := box.aHopAtItsRestart()
	box.point(run)
	box.tick()
	if got := box.barrierOf(run); got == nil || got.OK {
		t.Fatalf("the barrier is %+v", got)
	}

	machine.set("/etc/fstab", "UUID=DEAD-BEEF /backup ext4 defaults,nofail 0 2\n")
	box.advance(30 * time.Second)
	box.tick()
	if got := box.barrierOf(run); got == nil || !got.OK {
		t.Errorf("the barrier is %+v after the line was mended", got)
	}
}

// A check that could not be made is red: a barrier that cannot be read is not one that is green.
func TestABarrierThatCannotBeReadIsRed(t *testing.T) {
	box := newUpgradeBox(t)
	machine := theBarrierSees(t)
	machine.breakAt("/etc/fstab", &fs.PathError{Op: "open", Path: "/etc/fstab", Err: fs.ErrPermission})
	run := box.aHopAtItsRestart()
	box.point(run)

	box.tick()
	if got := box.barrierOf(run); got == nil || got.OK || got.Checks[0].OK {
		t.Errorf("the barrier is %+v", got)
	}
}

// A check that the table of the report does not have is not one that passes: the barrier is red until it can be asked.
func TestACheckThatTheReportDoesNotHaveMakesTheBarrierRed(t *testing.T) {
	box := newUpgradeBox(t)
	theBarrierSees(t)
	was := osUpgradeBarrierChecks
	osUpgradeBarrierChecks = append(append([]string(nil), was...), "no_such_check")
	t.Cleanup(func() { osUpgradeBarrierChecks = was })
	run := box.aHopAtItsRestart()
	box.point(run)

	box.tick()
	got := box.barrierOf(run)
	if got == nil || got.OK || len(got.Checks) != 4 || got.Checks[3].Code != "no_such_check" || got.Checks[3].OK {
		t.Errorf("the barrier is %+v", got)
	}
}

// What the barrier says again is not written again: the settling looks every half minute, and for days.
func TestTheBarrierThatSaysTheSameIsNotWrittenAgain(t *testing.T) {
	box := newUpgradeBox(t)
	theBarrierSees(t)
	run := box.aHopAtItsRestart()
	box.point(run)
	box.tick()

	was := osUpgradeWriteFile
	var writes []string
	osUpgradeWriteFile = func(path string, data []byte, mode os.FileMode) error {
		writes = append(writes, filepath.Base(path))
		return was(path, data, mode)
	}
	t.Cleanup(func() { osUpgradeWriteFile = was })
	box.advance(30 * time.Second)
	box.tick()
	for _, name := range writes {
		if name == osUpgradeBarrierFile {
			t.Errorf("the barrier was written again: %v", writes)
		}
	}
}

// The barrier is for a machine that waits for its restart and for no other state: nothing is asked of the machine while the unit runs, once the restart was asked, or for a
// rehearsal.
func TestTheBarrierIsAskedOnlyWhileTheMachineWaits(t *testing.T) {
	box := newUpgradeBox(t)
	theBarrierSees(t)
	run := box.aHopAtItsRestart()
	box.point(run)
	box.say(run, osrun.Marker{Kind: osrun.MkRebootRequested})

	if tick := box.tick(); tick.State != osrun.StateRebooting {
		t.Fatalf("the state is %q", tick.State)
	}
	if got := box.barrierOf(run); got != nil {
		t.Errorf("the barrier was computed for a machine that is restarting: %+v", got)
	}
}

func TestTheBarrierIsNotAskedForARehearsal(t *testing.T) {
	box := newUpgradeBox(t)
	theBarrierSees(t)
	run := box.aRehearsal()
	box.own(run, osrun.StateRehearsed)
	box.tick()
	if got := box.barrierOf(run); got != nil {
		t.Errorf("a rehearsal has a barrier: %+v", got)
	}
}

// The probe that the barrier looks through is the one of the core's, by default, and not a second way of reading the machine.
func TestTheBarrierLooksThroughTheCoresOwnProbe(t *testing.T) {
	box := newUpgradeBox(t)
	probe := osUpgradeBarrierProbe(box.updater)
	if _, ok := probe.(osReadinessProbe); !ok {
		t.Errorf("the probe is a %T", probe)
	}
}

// What block 7 of the unit warned of (the initrd is small, the boot loader cannot be looked at) is shown beside the restart, once each, as a check that passes: the owner is told, and
// the button is still offered. A word that is not a name, and a file that is not there, show nothing.
func TestWhatBlock7WarnedOfIsShownBesideTheRestartAndDoesNotBlockIt(t *testing.T) {
	box := newUpgradeBox(t)
	theBarrierSees(t)
	run := box.aHopAtItsRestart()
	box.point(run)
	if err := os.WriteFile(filepath.Join(box.backupOf(run), "warnings"), []byte("initrd_small\ngrub_cfg_missing\ninitrd_small\n$(reboot)\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	box.tick()
	got := box.barrierOf(run)
	if got == nil || !got.OK || len(got.Checks) != 5 {
		t.Fatalf("the barrier is %+v", got)
	}
	warned := got.Checks[3:]
	want := []OSUpgradeBarrierItem{
		{Code: "prereboot_warning", OK: true, Severity: "warn", Arg: "grub_cfg_missing"}, {Code: "prereboot_warning", OK: true, Severity: "warn", Arg: "initrd_small"},
	}
	if !reflect.DeepEqual(warned, want) {
		t.Errorf("the warnings are %+v, want %+v", warned, want)
	}
	if status := box.GetSystemOSUpgradeStatus(); !reflect.DeepEqual(status.Actions, []string{osrun.ActionReboot, osrun.ActionTakeover}) {
		t.Errorf("the buttons are %v", status.Actions)
	}
	// and the judge says it after the restart: see the findings of the judge
}
