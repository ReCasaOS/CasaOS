//go:build linux

package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osplan/boxtest"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// An upgrade from the owner's click to the closing of the run, with nothing of the unit's side written by the test: the core writes the folder of the run and launches the unit as
// it does on a machine, and the unit is the driver of the core's own text, run by dash, with the blocks that the core wrote for the plan, on the machine of the tests of the blocks
// (pkg/osplan/boxtest: the commands of a Debian 11 box that answer what a test says, and the files the blocks read). What the test says of the machine is the machine; every state
// it asserts is the one that the core's reducer gives of the files and the log that the driver and the blocks really left.
//
// The unit is not a systemd unit here: the call of systemd-run is a script that takes the folder of the run, points the paths of the machine that the driver and the blocks name at the
// folder of the test (as the tests of the blocks do; the sums of the files are written again from the core's own writer, and the files as the core wrote them are kept beside), and runs the driver with
// the environment that systemd-run was given. It runs it to the end before it returns, where systemd-run would only queue it. What is not proved here, and cannot be without a
// booted machine, is that systemd runs it that way: the proof of that is the one on a real machine.

const (
	e2eHostname = "box1"
	// the kernel that the upgrade installs, and the one the machine runs after its restart
	e2eNewKernel = "6.1.0-53-cloud-amd64"
	// the file where the blocks keep the state of the timers, which is one for the whole machine
	e2eTimersState = "/var/backups/casaos-os-upgrade-timers.state"
)

// e2eSources is the machine's own sources of apt.
var e2eSources = map[string]string{
	"sources.list": "deb http://deb.debian.org/debian bullseye main\ndeb http://deb.debian.org/debian bullseye-updates main\ndeb http://security.debian.org/debian-security bullseye-security main\n",
}

// e2eDecider says that every source we have is Debian's own.
type e2eDecider struct{}

func (e2eDecider) Suite(context.Context, string, string) (bool, bool, error) { return true, true, nil }

func e2eShell(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("the end to end run starts a shell for every block: skipped under -short")
	}
	for _, name := range []string{"dash", "sh"} {
		if p, err := exec.LookPath(name); err == nil {
			t.Logf("the unit's shell is %s", p)
			return p
		}
	}
	t.Skip("no shell on this machine")
	return ""
}

// An e2eUnit is the fake systemd-run of the box, and the machine that the unit runs on.
type e2eUnit struct {
	t     *testing.T
	box   *upgradeBox
	shell string
	plan  *osplan.Plan
	// launched are the unit command lines that the core gave, one for each launch
	launched [][]string
	// ended are the exit statuses of the drivers
	ended []int
}

// e2ePlan is a plan for the sources of the machine, as the plan job makes one: the tree, the hashes, a kernel that the upgrade installs.
func e2ePlan(t *testing.T, box *upgradeBox) *osplan.Plan {
	t.Helper()
	hop, ok := osplan.PlannedHop(osreadiness.HopDebian11to12)
	if !ok {
		t.Fatal("no hop")
	}
	var files []osreadiness.SourceFile
	for name, content := range e2eSources {
		files = append(files, osreadiness.SourceFile{Name: name, Content: []byte(content)})
	}
	rw, err := osplan.RewriteSources(context.Background(), hop, files, e2eDecider{})
	if err != nil {
		t.Fatal(err)
	}
	return box.anUpgradePlan(osplan.VerdictOK, func(p *osplan.Plan) {
		p.Sources = osplan.SourcesPlan{Files: rw.Files, Tree: rw.Tree, HashCurrent: rw.HashCurrent, HashGenerated: rw.HashGenerated}
		p.Paused = rw.Paused
		p.NewPackages = []string{"linux-image-" + e2eNewKernel}
	})
}

// e2eGuardTheMachine fails the test that changed the sources of apt of the machine it runs on: everything the unit does is done in the folder of the test, and a unit that was
// not pointed at it (the restore script that block 3 writes names /etc/apt itself) would change the real files of whoever runs the tests as root.
func e2eGuardTheMachine(t *testing.T) {
	t.Helper()
	read := func() string {
		var b strings.Builder
		_ = filepath.WalkDir("/etc/apt", func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			data, _ := os.ReadFile(path)
			b.WriteString(path + "\x00" + string(data) + "\x00")
			return nil
		})
		return b.String()
	}
	before := read()
	t.Cleanup(func() {
		if read() != before {
			t.Error("the sources of apt of the machine that runs this test were changed by it")
		}
	})
}

// e2eBox is the box of the start, with the unit in place of systemd-run.
func e2eBox(t *testing.T) (*upgradeBox, *e2eUnit, *startStubs) {
	t.Helper()
	shell := e2eShell(t)
	e2eGuardTheMachine(t)
	box := newUpgradeBox(t)
	box.switchOn()
	st := box.stubStart()
	st.hostname = e2eHostname
	theBarrierSees(t)
	u := &e2eUnit{t: t, box: box, shell: shell}
	u.plan = e2ePlan(t, box)
	box.updater.start = u.systemdRun
	return box, u, st
}

// folderOf is where the unit's machine is: the folder of the run.
func (u *e2eUnit) folderOf(runID string) string { return filepath.Join(u.box.root, runID) }

// backupOf is the backup folder that the core reads and the blocks write.
func (u *e2eUnit) backupOf() string {
	return filepath.Join(u.box.backups, "casaos-os-upgrade-"+u.plan.PlanID)
}

// systemdRun is the fake systemd-run: it takes the command line that the core gave, and runs the unit that it names to its end.
func (u *e2eUnit) systemdRun(path, unit string, args ...string) ([]byte, error) {
	t := u.t
	t.Helper()
	u.launched = append(u.launched, append([]string{path, unit}, args...))
	env := map[string]string{}
	var command []string
	for i, a := range args {
		if a == "--" {
			command = args[i+1:]
			break
		}
		if kv, ok := strings.CutPrefix(a, "--setenv="); ok {
			k, v, _ := strings.Cut(kv, "=")
			env[k] = v
		}
	}
	if len(command) < 3 || command[len(command)-2] != "/bin/sh" || filepath.Base(command[len(command)-1]) != "driver.sh" {
		return nil, errors.New("the command line of the unit is not systemd-inhibit and the driver: " + strings.Join(command, " "))
	}
	if command[0] != "systemd-inhibit" {
		return nil, errors.New("the unit is not run under systemd-inhibit")
	}
	unitDir := filepath.Dir(command[len(command)-1])
	runID := filepath.Base(unitDir)
	if want := osUpgradeUnitRoot + "/" + runID; unitDir != want || env["CASAOS_OSU_DIR"] != want {
		return nil, errors.New("the unit's folder is not the run's: " + unitDir)
	}
	dir := u.folderOf(runID)
	u.pointAtTheBox(dir, runID)

	cmd := exec.Command(u.shell, filepath.Join(dir, "driver.sh"))
	cmd.Dir = dir
	var environ []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "CASAOS_OSU_") {
			environ = append(environ, e)
		}
	}
	env["CASAOS_OSU_DIR"] = dir
	env["FAKE"] = "./fake"
	// The restore script that block 3 writes names /etc/apt, the file of the state of the timers and apt-get itself, and puts its own PATH in front: the three variables below are what
	// point it at the box (a root that runs this test would otherwise put the sources of the machine back from a copy of the box's). The unit does not start without them.
	env["APT_ETC"] = filepath.Join(dir, "etc-apt")
	env["CASAOS_NO_UPDATE"] = "1"
	env["CASAOS_TIMERS_STATE"] = filepath.Join(dir, "timers.state")
	for _, name := range []string{"APT_ETC", "CASAOS_TIMERS_STATE"} {
		if !strings.HasPrefix(env[name], u.box.root+string(filepath.Separator)) {
			t.Fatalf("%s=%s is not in the folder of the test", name, env[name])
		}
	}
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	cmd.Env = environ
	u.box.setUnit("active")
	// the clock of the box is the time of the lines that the unit writes
	u.box.syncClock()
	out, err := cmd.CombinedOutput()
	u.box.setUnit("inactive")
	u.box.syncClock()
	code := 0
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		t.Fatalf("the driver did not start: %v", err)
	}
	u.ended = append(u.ended, code)
	if code != 0 {
		t.Errorf("the driver ended with %d (its status is always 0): %s", code, out)
	}
	return []byte("queued"), nil
}

// syncClock puts the clock of the box on the time of the machine that the tests run on, when that is later: the unit's lines are stamped by the real date.
func (b *upgradeBox) syncClock() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now := time.Now().UTC(); now.After(b.clock) {
		b.clock = now
	}
}

// pointAtTheBox makes the folder of the run the machine of the box, once, and points the files that the core wrote at it, every time. The files as the core wrote them are kept in
// .core, and what the unit runs is made from them again at each launch.
func (u *e2eUnit) pointAtTheBox(dir, runID string) {
	t := u.t
	t.Helper()
	core := filepath.Join(dir, ".core")
	if _, err := os.Stat(core); err != nil {
		u.installTheMachine(dir)
		if err := os.MkdirAll(filepath.Join(core, "blocks"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"driver.sh", "bk-path"} {
			copyFile(t, filepath.Join(dir, name), filepath.Join(core, name))
		}
		entries, err := os.ReadDir(filepath.Join(dir, "blocks"))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			copyFile(t, filepath.Join(dir, "blocks", e.Name()), filepath.Join(core, "blocks", e.Name()))
		}
	}
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(core, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	bk := u.backupOf()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("driver.sh", boxtest.Driver(t, read("driver.sh"), boxtest.DriverVars{Root: dir, BKCase: "*/casaos-os-upgrade-*", RefreshCase: "*/casaos-os-upgrade-refresh-*"}))
	write("bk-path", bk+"\n")
	vars := boxtest.PathVars{PlanID: u.plan.PlanID, BK: bk, TimersState: e2eTimersState, Cgroup: "./cgroup"}
	entries, err := os.ReadDir(filepath.Join(core, "blocks"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		write("blocks/"+e.Name(), boxtest.Paths(read("blocks/"+e.Name()), vars))
	}
	run, err := readOSUpgradeRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	sums, err := osUpgradeSums(dir, run.Meta.Kind)
	if err != nil {
		t.Fatal(err)
	}
	write("SHA256SUMS", string(sums))
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// installTheMachine makes the folder of the run the machine that the blocks go through: Debian 11 with the machine's own sources, a kernel to start, room, and the backup folder
// that the core reads at the place where the blocks write (./bk is a link to it).
func (u *e2eUnit) installTheMachine(dir string) {
	t := u.t
	t.Helper()
	boxtest.Install(t, dir, u.box.now().Add(time.Hour).Unix())
	boxtest.Flow(t, dir, boxBoot1)
	boxtest.Fake(t, dir, "hostname", "echo "+e2eHostname)
	boxtest.Write(t, dir, "os-release", "PRETTY_NAME=\"Debian GNU/Linux 11 (bullseye)\"\nVERSION_CODENAME=bullseye\nID=debian\n")
	for name, content := range e2eSources {
		boxtest.Write(t, dir, "etc-apt/"+name, content)
	}
	if err := os.Remove(filepath.Join(dir, "bk")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(u.backupOf(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(u.backupOf(), filepath.Join(dir, "bk")); err != nil {
		t.Fatal(err)
	}
}

// restarted makes the box the machine after its restart: another boot for the core and for the machine, Debian 12, the kernel that block 7 looked at.
func (u *e2eUnit) restarted(runID string) {
	u.t.Helper()
	boxtest.Restarted(u.t, u.folderOf(runID), boxBoot2)
	u.box.setBoot(boxBoot2)
}

func (u *e2eUnit) sourcesAreTheMachinesOwn(runID string) bool {
	u.t.Helper()
	dir := u.folderOf(runID)
	for name, want := range e2eSources {
		data, err := os.ReadFile(filepath.Join(dir, "etc-apt", name))
		if err != nil || string(data) != want {
			return false
		}
	}
	m, _ := filepath.Glob(filepath.Join(dir, "etc-apt", "sources.list.d", "*"))
	return len(m) == 0
}

func (u *e2eUnit) calls(runID string) []string {
	data, _ := os.ReadFile(filepath.Join(u.folderOf(runID), "fake", "calls.log"))
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (u *e2eUnit) timersRun(runID string) bool {
	dir := filepath.Join(u.folderOf(runID), "fake")
	for _, unit := range []string{"apt-daily.timer", "unattended-upgrades.service"} {
		if _, err := os.Stat(filepath.Join(dir, "active-"+unit)); err != nil {
			return false
		}
	}
	return true
}

func e2eKinds(run *osUpgradeRun) []string {
	var out []string
	for _, m := range run.Markers {
		out = append(out, m.Kind)
	}
	return out
}

// countOf is how many of the markers of a run are of the kind.
func countOf(run *osUpgradeRun, kind string) int { return len(markersOfKind(run, kind)) }

// A rehearsal through the core, the driver and the blocks: the unit runs the reversible half, the machine holds the sources of the next release, and the owner's word puts it back
// by a unit of its own.
func TestARehearsalThroughTheCoreTheDriverAndTheRealBlocks(t *testing.T) {
	box, u, _ := e2eBox(t)
	status, err := box.StartSystemOSUpgrade(OSUpgradeStartRequest{PlanID: u.plan.PlanID, Rehearsal: true, Hostname: e2eHostname, Acks: []string{osUpgradeAckNoInterruption}})
	if err != nil {
		t.Fatal(err)
	}
	runID := status.RunID
	run := box.read(runID)
	if !slices.Equal(u.ended, []int{0}) || len(u.launched) != 1 {
		t.Fatalf("the unit ended with %v after %d launches", u.ended, len(u.launched))
	}
	if status.State != osrun.StateRehearsed || !status.Busy || !box.flagIsThere() {
		t.Fatalf("after the rehearsal's unit: %q (busy %v), flag %v; the markers are %v\nthe log of the unit:\n%s", status.State, status.Busy, box.flagIsThere(), e2eKinds(run), box.outputOf(run))
	}
	// the markers are the ones of the driver, in order: the five blocks of the reversible half, then the word that the rehearsal is ready
	wantKinds := []string{osrun.MkQueued, osrun.MkRun, osrun.MkAttempt, osrun.MkUnitStart}
	for range osrun.BlockList(osrun.PhaseRehearse, osrun.Files{}) {
		wantKinds = append(wantKinds, osrun.MkBlockStart, osrun.MkBlockDone)
	}
	wantKinds = append(wantKinds, osrun.MkRehearsalReady, osrun.MkUnitEnd)
	if got := e2eKinds(run); !slices.Equal(got, wantKinds) {
		t.Errorf("the markers are\n%v\nwant\n%v", got, wantKinds)
	}
	// the machine: the sources of the next release are in place, the timers are stopped, the copy of /etc/apt is in the backup folder
	if u.sourcesAreTheMachinesOwn(runID) || u.timersRun(runID) {
		t.Error("the machine is not holding the sources of the next release with its timers stopped")
	}
	for _, name := range []string{"apt-orig", "restore-sources.sh", "done-download", "timers-stopping"} {
		if _, err := os.Stat(filepath.Join(u.backupOf(), name)); err != nil {
			t.Errorf("the backup folder has no %s", name)
		}
	}
	if got := status.Actions; !slices.Equal(got, []string{osrun.ActionDiscard}) {
		t.Errorf("the buttons are %v", got)
	}

	// the owner drops it: the unit of the unwind puts everything back
	status, err = box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckDiscard})
	if err != nil {
		t.Fatal(err)
	}
	run = box.read(runID)
	if !slices.Equal(u.ended, []int{0, 0}) || status.State != osrun.StateRehearsalDone || status.Busy {
		t.Fatalf("after the unwind: %q (busy %v), %v units; the markers are %v\n%s", status.State, status.Busy, len(u.launched), e2eKinds(run), box.outputOf(run))
	}
	if !u.sourcesAreTheMachinesOwn(runID) || !u.timersRun(runID) {
		list, _ := filepath.Glob(filepath.Join(u.folderOf(runID), "etc-apt", "*"))
		list2, _ := filepath.Glob(filepath.Join(u.folderOf(runID), "etc-apt", "sources.list.d", "*"))
		data, _ := os.ReadFile(filepath.Join(u.folderOf(runID), "etc-apt", "sources.list"))
		t.Errorf("the machine is not as it was: sources %v, timers %v; etc-apt %v %v\n%s\nthe log of the unit:\n%s", u.sourcesAreTheMachinesOwn(runID), u.timersRun(runID), list, list2, data, box.outputOf(run))
	}
	if _, err := os.Stat(u.backupOf()); err == nil {
		t.Error("the backup folder of the rehearsal is still where the blocks of the next run would stop at it")
	}
	if _, err := os.Stat(filepath.Join(u.folderOf(runID), "bk-aborted", "apt-orig.restored."+"1")); err == nil {
		t.Log("(the copy of the sources is kept in the folder of the run)")
	}
	// the next look of the core lets go of the machine, and the owner closes the run
	if box.tick(); box.flagIsThere() {
		t.Error("the flag of the open upgrade is still there")
	}
	if status, err = box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckAcknowledge}); err != nil || status.State != osrun.StateIdle {
		t.Fatalf("closing: %q, %v", status.State, err)
	}
}

// outputOf is the log of the unit of a run, for the message of a test that failed.
func (b *upgradeBox) outputOf(run *osUpgradeRun) string {
	data, _ := os.ReadFile(filepath.Join(run.Dir, osUpgradeOutputFile))
	return string(data)
}

// A hop through the core, the driver and the blocks, from the owner's start to the closing of the run: the unit goes through the blocks to the restart, the barrier is green, the owner
// restarts the machine, the machine comes back in another boot, the core launches the check by itself, judges the machine, and the owner closes the run.
func TestAHopThroughTheCoreTheDriverAndTheRealBlocksFromTheStartToTheClosing(t *testing.T) {
	box, u, st := e2eBox(t)
	told := box.aMachineThatRestartsWith(nil)

	status, err := box.StartSystemOSUpgrade(box.aGoodRequest(u.plan))
	if err != nil {
		t.Fatal(err)
	}
	runID := status.RunID
	run := box.read(runID)
	if !slices.Equal(u.ended, []int{0}) {
		t.Fatalf("the unit ended with %v", u.ended)
	}
	if got := countOf(run, osrun.MkBlockDone); got != 8 || countOf(run, osrun.MkStop) != 0 {
		t.Fatalf("%d blocks ended, %d stops; the markers are %v\nthe log of the unit:\n%s", got, countOf(run, osrun.MkStop), e2eKinds(run), box.outputOf(run))
	}
	tick := box.tick()
	if tick.State != osrun.StateAwaitingReboot {
		t.Fatalf("the hop is %q after its unit, not awaiting its restart\n%s", tick.State, box.outputOf(run))
	}
	status = box.GetSystemOSUpgradeStatus()
	if !status.PNR || !status.Busy || status.HalfUpgraded || status.Barrier == nil || !status.Barrier.OK || !slices.Equal(status.Actions, []string{osrun.ActionReboot, osrun.ActionTakeover}) {
		t.Fatalf("the status is %+v", status)
	}
	// what the blocks left, which the core reads: the backup folder holds the marks of the eight blocks, the kernel that block 8 will look for, and the boot of the unit
	for _, name := range []string{"done-verify", "done-timers", "done-snapshot", "done-sources", "done-download", "done-minimal", "done-full", "done-prereboot", "kernel", "boot_id", "begun-upgrade", "expected-removed.txt"} {
		if _, err := os.Stat(filepath.Join(u.backupOf(), name)); err != nil {
			t.Errorf("the backup folder has no %s", name)
		}
	}
	if u.sourcesAreTheMachinesOwn(runID) {
		t.Error("the sources of the machine are still Debian 11's")
	}

	// the owner restarts the machine: the core writes its word first, and the machine comes back in another boot, as Debian 12
	if status, err = box.RebootSystemOSUpgrade(); err != nil || status.State != osrun.StateRebooting {
		t.Fatalf("the restart: %q, %v", status.State, err)
	}
	if got := told.all(); !slices.Equal(got, []string{"sync", "reboot"}) {
		t.Errorf("the machine was told %v", got)
	}
	u.restarted(runID)
	box.advance(time.Minute)

	// the core sees the new boot, waits for the machine to be ready, and launches the check by itself
	st.mu.Lock()
	st.facts = osverify.Facts{Taken: true, At: box.now(), Kernel: e2eNewKernel, System: "running", DpkgQuiet: true, DNS: true, Listening: []string{"0.0.0.0:22"}}
	st.mu.Unlock()
	takesFacts(t, st.facts)
	tick = box.tick()
	run = box.read(runID)
	if len(u.launched) != 2 || len(markersOfKind(run, osrun.MkBootSeen)) != 1 || tick.Action != osrun.DoLaunchVerify {
		t.Fatalf("after the restart: %d launches, %d boots seen, the look did %+v; the markers are %v", len(u.launched), len(markersOfKind(run, osrun.MkBootSeen)), tick, e2eKinds(run))
	}
	if got := box.attemptsOf(run); len(got) != 2 || got[1].Phase != osrun.PhaseVerify || got[1].Who != osrun.WhoAuto {
		t.Fatalf("the attempts are %+v", got)
	}
	if !slices.Equal(u.ended, []int{0, 0}) {
		t.Fatalf("the units ended with %v", u.ended)
	}
	if _, err := os.Stat(filepath.Join(u.backupOf(), "done-after")); err != nil {
		t.Fatalf("block 8 did not end: %v\n%s", err, box.outputOf(run))
	}
	if !u.timersRun(runID) {
		t.Error("the timers of apt were not started again by block 8")
	}
	// the check ended: the core judges the machine at its next look
	tick = box.tick()
	if tick.State != osrun.StateVerifying && tick.State != osrun.StateSucceeded {
		t.Fatalf("after the check: %+v", tick)
	}
	status = box.GetSystemOSUpgradeStatus()
	if status.State != osrun.StateSucceeded || status.Busy || len(status.Findings) != 0 {
		t.Fatalf("the status is %q, busy %v, findings %+v\n%s", status.State, status.Busy, status.Findings, box.outputOf(box.read(runID)))
	}
	if !slices.Equal(status.Actions, []string{osrun.ActionAcknowledge}) {
		t.Errorf("the buttons are %v", status.Actions)
	}

	// the owner closes the run: the backup folder is put away where the run reads it
	if status, err = box.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckAcknowledge}); err != nil || status.State != osrun.StateIdle {
		t.Fatalf("closing: %q, %v", status.State, err)
	}
	if _, err := os.Stat(u.backupOf()); err == nil {
		t.Error("the backup folder of the hop that ended is still where the blocks of the next hop would stop at it")
	}
	if _, err := os.Stat(filepath.Join(u.folderOf(runID), osUpgradeBKDone, "done-after")); err != nil {
		t.Errorf("the backup folder was not put away in the run folder: %v", err)
	}
	if box.flagIsThere() {
		t.Error("the flag of the open upgrade is still there")
	}
}

// A machine that comes back on the kernel it had before the upgrade (the boot menu started the old one) is stopped by block 8, and the stop is the owner's to mend and to try again
// from the button that the status offers: the click launches the check again, as an attempt of its own, and the machine that is then well is judged and succeeds.
func TestACheckThatBlock8StoppedIsMadeAgainThroughTheCoreAndTheDriver(t *testing.T) {
	box, u, _ := e2eBox(t)
	box.aMachineThatRestartsWith(nil)
	status, err := box.StartSystemOSUpgrade(box.aGoodRequest(u.plan))
	if err != nil {
		t.Fatal(err)
	}
	runID := status.RunID
	if tick := box.tick(); tick.State != osrun.StateAwaitingReboot {
		t.Fatalf("the hop is %q after its unit\n%s", tick.State, box.outputOf(box.read(runID)))
	}
	if _, err := box.RebootSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	// the machine starts on the old kernel
	u.restarted(runID)
	boxtest.Write(t, u.folderOf(runID), "fake/uname-r", "5.10.0-46-cloud-amd64\n")
	box.advance(time.Minute)
	takesFacts(t, osverify.Facts{Taken: true, At: box.now(), Kernel: e2eNewKernel, System: "running", DpkgQuiet: true, DNS: true, Listening: []string{"0.0.0.0:22"}})
	box.tick()
	run := box.read(runID)
	if len(u.launched) != 2 || !slices.Equal(u.ended, []int{0, 0}) {
		t.Fatalf("%d launches, the units ended with %v; the markers are %v", len(u.launched), u.ended, e2eKinds(run))
	}
	status = box.GetSystemOSUpgradeStatus()
	if status.State != osrun.StateFailed || status.Reason != osrun.ReasonVerify || status.Failure == nil || status.Failure.Class != osrun.ClassBarrierKernel || !status.Busy {
		t.Fatalf("the status is %q %q busy %v, failure %+v\n%s", status.State, status.Reason, status.Busy, status.Failure, box.outputOf(run))
	}
	if !slices.Contains(status.Actions, osrun.ActionResume) {
		t.Fatalf("the buttons are %v: the check cannot be made again", status.Actions)
	}

	// the owner starts the right kernel, and clicks
	boxtest.Write(t, u.folderOf(runID), "fake/uname-r", e2eNewKernel+"\n")
	if status, err = box.ResumeSystemOSUpgrade(); err != nil {
		t.Fatalf("the click that the status offers was refused: %v", err)
	}
	run = box.read(runID)
	if got := box.attemptsOf(run); len(u.launched) != 3 || len(got) != 3 || got[2].Phase != osrun.PhaseVerify || got[2].Who != osrun.WhoOwner {
		t.Fatalf("%d launches; the attempts are %+v", len(u.launched), got)
	}
	box.tick()
	status = box.GetSystemOSUpgradeStatus()
	if status.State != osrun.StateSucceeded || status.Busy {
		t.Fatalf("after the second check: %q (busy %v)\n%s", status.State, status.Busy, box.outputOf(box.read(runID)))
	}
}
