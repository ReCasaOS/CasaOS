package osplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The driver (osrun.Driver) run in a box: a folder that holds the folder of a run, the backup folder, the commands of the machine (stubs, first in the PATH of the driver) and the
// boot id. What the driver names of the machine is its PATH, the boot id, the backup folders (of a hop and of a refresh) and the file of the state of the timers, and the box points
// them at the folder of the test by the changes of driverText, each of which must apply exactly once: the text that runs is the driver's own but for those, and for the lines
// that a test adds to make something happen at a moment (the hooks that kill the driver or write the owner's flag after a line of the log, and the gates that the abort tests
// make it wait at).
//
// A shell is cheap on Linux and dear on Windows with Git Bash (see the notes at the top of runbook_exec_test.go): the tests of the driver take a minute under dash on Linux, which is
// what CI runs, and the better part of an hour on Windows, on top of the rest of the package (go test -timeout 120m ./pkg/osplan, or -short, which skips them, or -run for one). A
// test that has a run to do for every line of the log, or every block, says what it does on Windows where it does less.
//
// Two kinds of box have this machinery: the box of stub blocks (newStubBox), which runs a driver that is given blocks that only leave the files that a block leaves, and the box of
// the real blocks of the unit's text (newFlowBox, driver_flow_test.go).

// A driverShell is a shell that the unit's /bin/sh may be.
type driverShell struct {
	name string
	path string
	args []string
}

// driverShells are the shells that the driver is run under: dash when the machine has it (the unit's /bin/sh on Debian and Ubuntu), and bash in its sh mode. With all false only the
// first is given, for the tests that run the real blocks, which are slow on Windows. The shell that the driver and its blocks used is logged.
func driverShells(t *testing.T, all bool) []driverShell {
	t.Helper()
	if testing.Short() {
		t.Skip("the tests that run the driver under a shell are skipped under -short: they start a shell for each block, which is some 8 s on Linux and some 480 s on Windows with Git Bash (see the notes at the top of runbook_exec_test.go)")
	}
	var out []driverShell
	if p, err := exec.LookPath("dash"); err == nil {
		out = append(out, driverShell{"dash", filepath.ToSlash(p), nil})
	}
	if p, err := exec.LookPath("bash"); err == nil {
		out = append(out, driverShell{"bash", filepath.ToSlash(p), []string{"--posix"}})
	}
	if len(out) == 0 {
		t.Skip("neither dash nor bash on this machine (Windows without Git Bash): the driver is proved on Linux")
	}
	if !all {
		out = out[:1]
	}
	return out
}

// eachDriverShell runs a test under each shell, as a subtest.
func eachDriverShell(t *testing.T, all bool, run func(t *testing.T, sh driverShell)) {
	t.Helper()
	for _, sh := range driverShells(t, all) {
		t.Run(sh.name, func(t *testing.T) {
			t.Logf("the driver and its blocks run under %s %s", sh.path, strings.Join(sh.args, " "))
			if p, err := filepath.EvalSymlinks("/bin/sh"); err == nil {
				t.Logf("/bin/sh of this machine is %s", p)
			}
			run(t, sh)
		})
	}
}

const (
	driverNonce  = "0123456789abcdef0123456789abcdef"
	driverRunID  = testRunID
	driverBootA  = "11111111-1111-4111-8111-111111111111"
	driverBootB  = "22222222-2222-4222-8222-222222222222"
	driverBootC  = "abcdefab-cdef-4bcd-8efa-bcdefabcdef0"
	driverPlanID = "p1-5787acb2f81006785a3076312615ae6c"
)

// A driverBox is the folder of a run, the backup folder and the machine's commands, and the driver that runs in them.
type driverBox struct {
	t    *testing.T
	sh   driverShell
	root string // slash form: the box, whose bin/ comes first in the PATH of the driver and whose bootid is the boot id that it reads
	dir  string // the folder of the run (CASAOS_OSU_DIR)
	bk   string // the backup folder that bk-path names
	// bkCase and refreshCase are the case patterns that stand where the driver has /var/backups/casaos-os-upgrade-* and /var/backups/casaos-os-upgrade-refresh-*
	bkCase, refreshCase string
	blocks              map[string]string // the text of blocks/<id>.sh
	extraEnv            []string
	// hooks is shell that runs in the driver right after the J-th line that the driver writes for the next attempt (the first is UNIT_START), made absolute when the attempt is
	// prepared: it is how a test kills the driver, or makes the owner's flag appear, at an exact moment
	hooks map[int]string
	// mayDie says that the next attempt is one that a test kills: its driver ends with a signal, and that is no fault
	mayDie    bool
	afterLine map[int]string
	// dropEnv names the variables of the unit's environment that a test takes away
	dropEnv []string
	// override gives a variable of the unit's environment another value
	override map[string]string
	// gateIdx is the place in the list of the block at which a test makes the driver wait, around the marker of the block and the read of the owner's flag (driver_abort_test.go): 0 is none
	gateIdx int
	kind    string
	runs    int
}

// newDriverBox makes the folder of a run in a box that exists (root), with the log of the core's first lines and no block yet.
func newDriverBox(t *testing.T, sh driverShell, root, dir, bk, bkCase, refreshCase, kind string) *driverBox {
	t.Helper()
	b := &driverBox{
		t: t, sh: sh, root: filepath.ToSlash(root), dir: filepath.ToSlash(dir), bk: filepath.ToSlash(bk), bkCase: bkCase, refreshCase: refreshCase,
		blocks: map[string]string{}, hooks: map[int]string{}, afterLine: map[int]string{}, kind: kind,
	}
	for _, d := range []string{b.dir + "/blocks", b.dir + "/lists", b.root + "/bin"} {
		if err := os.MkdirAll(filepath.FromSlash(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// the unit's /bin/sh, as the shell under test, for the blocks and the restore script that the driver starts with sh
	wrapper := "#!/bin/sh\nexec '" + b.sh.path + "' " + strings.Join(b.sh.args, " ") + " \"$@\"\n"
	b.write(b.root+"/bin/sh", wrapper, 0o755)
	// the box that is a copy of another already has the log of it
	if !b.exists(b.dir + "/markers") {
		b.append(osrun.Marker{Kind: osrun.MkQueued, At: time.Now().UTC()})
		b.append(osrun.Marker{Kind: osrun.MkRun, RunID: driverRunID, Hop: "debian-11-12", PlanID: driverPlanID, RunKind: kind, Boot: driverBootA})
	}
	return b
}

func (b *driverBox) write(path, text string, mode os.FileMode) {
	b.t.Helper()
	if err := os.WriteFile(filepath.FromSlash(path), []byte(text), mode); err != nil {
		b.t.Fatal(err)
	}
}

func (b *driverBox) read(path string) string {
	data, _ := os.ReadFile(filepath.FromSlash(path))
	return string(data)
}

func (b *driverBox) exists(path string) bool {
	_, err := os.Stat(filepath.FromSlash(path))
	return err == nil
}

// append writes a marker of the core to the log, as the core does: one line, at the end.
func (b *driverBox) append(m osrun.Marker) {
	b.t.Helper()
	if m.At.IsZero() {
		m.At = time.Now().UTC()
	}
	line, err := m.Line(driverNonce)
	if err != nil {
		b.t.Fatalf("marker %+v: %v", m, err)
	}
	f, err := os.OpenFile(filepath.FromSlash(b.dir+"/markers"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		b.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		b.t.Fatal(err)
	}
}

// replaceOnce is the change of the driver's text that must apply exactly once: a driver that was edited has to be looked at, not run with a change that did nothing.
func replaceOnce(t *testing.T, text, from, to string) string {
	t.Helper()
	if n := strings.Count(text, from); n != 1 {
		t.Fatalf("the driver has %d places that read %q, and a change of it for the box needs exactly one", n, from)
	}
	return strings.Replace(text, from, to, 1)
}

// driverText is the driver as this box runs it: the paths of the machine are the box's, and the lines that the test added are in.
func (b *driverBox) driverText() string {
	t := b.t
	text := osrun.Driver
	text = replaceOnce(t, text, "PATH=/usr/sbin:/usr/bin:/sbin:/bin; export PATH\n", "PATH=\"$(cd '"+b.root+"/bin' && pwd):/usr/sbin:/usr/bin:/sbin:/bin\"; export PATH\n")
	text = replaceOnce(t, text, "/proc/sys/kernel/random/boot_id", b.root+"/bootid")
	text = replaceOnce(t, text, "case \"$BK\" in /var/backups/casaos-os-upgrade-*) ;;", "case \"$BK\" in "+b.bkCase+") ;;")
	text = replaceOnce(t, text, "case \"$BK\" in /var/backups/casaos-os-upgrade-refresh-*) return 0 ;;", "case \"$BK\" in "+b.refreshCase+") return 0 ;;")
	text = replaceOnce(t, text, "/var/backups/casaos-os-upgrade-timers.state", b.root+"/timers.state")
	if len(b.afterLine) > 0 {
		var hooks strings.Builder
		keys := make([]int, 0, len(b.afterLine))
		for k := range b.afterLine {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			fmt.Fprintf(&hooks, " [ \"$(wc -l < \"$D/markers\")\" -ne %d ] || { %s; };", k, b.afterLine[k])
		}
		text = replaceOnce(t, text, `printf '%s\n' "$line" >> "$D/markers"; }`, `printf '%s\n' "$line" >> "$D/markers";`+hooks.String()+` }`)
	}
	if b.gateIdx > 0 {
		const start, flag = "  mark BLOCK_START \"$idx\" \"$id\" \"$(ts)\"\n", "  if [ -e \"$D/abort\" ] && ! pnr; then stopat \"$idx\" \"$id\" 0 abort; fi\n"
		if i, j := strings.Index(text, start), strings.Index(text, flag); i < 0 || j < 0 || strings.Count(text, start) != 1 || strings.Count(text, flag) != 1 {
			t.Fatal("the driver has no marker of the start of a block, or no read of the flag, where the gates of the test are put")
		} else {
			first, second := start, flag
			if j < i {
				first, second = flag, start
			}
			text = strings.Replace(text, first, "  gate P0 \"$idx\"\n"+first+"  gate P1 \"$idx\"\n", 1)
			text = strings.Replace(text, second, second+"  gate P2 \"$idx\"\n", 1)
		}
		text = replaceOnce(t, text, "mark UNIT_START ", "gate() { [ \"$2\" = \"$GATEIDX\" ] || return 0; : > \"$D/at-$1\"; while [ ! -e \"$D/go-$1\" ]; do sleep 0.02; done; }\nmark UNIT_START ")
	}
	return text
}

// seal writes what the core writes before it launches an attempt: the driver, the blocks, the name of the backup folder and the sums of them all, last.
func (b *driverBox) seal() {
	b.t.Helper()
	b.write(b.dir+"/driver.sh", b.driverText(), 0o644)
	b.write(b.dir+"/bk-path", b.bk+"\n", 0o644)
	for id, text := range b.blocks {
		b.write(b.dir+"/blocks/"+id+".sh", text, 0o644)
	}
	b.sums()
}

// sums writes SHA256SUMS for the files of the folder as they are: the driver, the name of the backup folder and the blocks.
func (b *driverBox) sums() {
	b.t.Helper()
	names := []string{"driver.sh", "bk-path"}
	for id := range b.blocks {
		names = append(names, "blocks/"+id+".sh")
	}
	slices.Sort(names)
	var sums strings.Builder
	for _, name := range names {
		sum := sha256.Sum256([]byte(b.read(b.dir + "/" + name)))
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	b.write(b.dir+"/SHA256SUMS", sums.String(), 0o644)
}

// prepare writes what the core writes for an attempt: its ATTEMPT marker, its list of blocks, and the sums.
func (b *driverBox) prepare(n int, phase string, list ...string) {
	b.t.Helper()
	b.append(osrun.Marker{Kind: osrun.MkAttempt, N: n, Phase: phase, Who: osrun.WhoAuto})
	text := ""
	if len(list) > 0 {
		text = strings.Join(list, "\n") + "\n"
	}
	b.write(b.dir+"/lists/"+strconv.Itoa(n)+".list", text, 0o644)
	b.afterLine = map[int]string{}
	base := b.lineCount()
	for j, cmd := range b.hooks {
		b.afterLine[base+j] = cmd
	}
	b.seal()
}

// lineCount is how many lines the log of markers has.
func (b *driverBox) lineCount() int { return strings.Count(b.read(b.dir+"/markers"), "\n") }

// hookAfter makes the driver run a command right after the j-th line that it writes for the attempt that is prepared next (the first is UNIT_START), and for that one only; two
// commands for one line run in the order that they were given.
func (b *driverBox) hookAfter(j int, cmd string) {
	if b.hooks[j] != "" {
		cmd = b.hooks[j] + "; " + cmd
	}
	b.hooks[j] = cmd
	b.mayDie = b.mayDie || strings.Contains(cmd, "kill -9")
}

// environ is the environment of the unit's driver, and the box's own words.
func (b *driverBox) environ(n int, phase string) []string {
	env := slices.DeleteFunc(os.Environ(), func(e string) bool { return strings.HasPrefix(e, "CASAOS_OSU_") || strings.HasPrefix(e, "GATEIDX=") })
	env = append(env,
		"CASAOS_OSU_NONCE="+driverNonce, "CASAOS_OSU_DIR="+b.dir, "CASAOS_OSU_PHASE="+phase, "CASAOS_OSU_ATTEMPT="+strconv.Itoa(n), "CASAOS_OSU_RUN="+driverRunID,
		"GATEIDX="+strconv.Itoa(b.gateIdx))
	env = append(env, b.extraEnv...)
	for k, v := range b.override {
		env = append(env, k+"="+v)
	}
	return slices.DeleteFunc(env, func(e string) bool {
		return slices.ContainsFunc(b.dropEnv, func(name string) bool { return strings.HasPrefix(e, name+"=") })
	})
}

// start starts the driver of an attempt, as the unit does, without waiting for it.
func (b *driverBox) start(n int, phase string) (*exec.Cmd, context.CancelFunc, *lockedBuffer) {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	cmd := exec.CommandContext(ctx, b.sh.path, append(slices.Clone(b.sh.args), b.dir+"/driver.sh")...)
	cmd.Dir = filepath.FromSlash(b.root)
	cmd.Env = b.environ(n, phase)
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		cancel()
		b.t.Fatalf("the driver did not start: %v", err)
	}
	b.runs++
	return cmd, cancel, out
}

// exitCode is how a driver ended: its status, and -1 for one that a signal killed.
func exitCode(err error) int {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	}
	return -2
}

// run runs the driver of an attempt to its end, and says its status and what it printed to the unit's own output (what it writes after it has taken the log is in output.log).
func (b *driverBox) run(n int, phase string) (int, string) {
	b.t.Helper()
	cmd, cancel, out := b.start(n, phase)
	defer cancel()
	code := exitCode(cmd.Wait())
	return code, out.String()
}

// launch is prepare and run: what the core does for an attempt.
func (b *driverBox) launch(n int, phase string, list ...string) int {
	b.t.Helper()
	b.prepare(n, phase, list...)
	died := b.mayDie
	b.hooks, b.mayDie = map[int]string{}, false
	code, out := b.run(n, phase)
	if code != 0 && !died {
		// a driver that is made to kill itself says so by a signal; any other status is a driver that did not do what it is meant to
		b.t.Errorf("attempt %d ended with status %d: %s", n, code, out)
	}
	return code
}

// markers are the markers of the log, as the core reads them.
func (b *driverBox) markers() []osrun.Marker {
	b.t.Helper()
	f, err := os.Open(filepath.FromSlash(b.dir + "/markers"))
	if err != nil {
		b.t.Fatal(err)
	}
	defer f.Close()
	nonce, ms, ignored, err := osrun.ParseMarkers(f)
	if err != nil || nonce != driverNonce || ignored != 0 {
		b.t.Fatalf("the log of markers does not read back: nonce %q, %d lines ignored, %v:\n%s", nonce, ignored, err, b.read(b.dir+"/markers"))
	}
	return ms
}

// short is a marker in a few words.
func short(m osrun.Marker) string {
	switch m.Kind {
	case osrun.MkUnitStart:
		return "UNIT_START " + m.Phase
	case osrun.MkBlockStart, osrun.MkBlockDone, osrun.MkBlockSkipped:
		return m.Kind + " " + strconv.Itoa(m.Index) + " " + m.ID
	case osrun.MkStop:
		return fmt.Sprintf("STOP %d %s rc=%d %s pnr=%v", m.Index, m.ID, m.RC, m.Class, m.PNR)
	case osrun.MkUnwound, osrun.MkUnitEnd:
		return m.Kind + " " + m.Result
	case osrun.MkRefreshDone:
		return "REFRESH_DONE " + m.Result + " " + m.Why
	case osrun.MkRebootPending, osrun.MkRebootNotNeeded:
		return m.Kind + " " + m.Kernel
	}
	return m.Kind
}

// story is what the driver wrote for an attempt, in a few words each.
func (b *driverBox) story(n int) []string {
	b.t.Helper()
	var out []string
	for _, m := range b.markers() {
		switch m.Kind {
		case osrun.MkQueued, osrun.MkRun, osrun.MkAttempt, osrun.MkStartFailed, osrun.MkRebootRequested, osrun.MkRebootFailed, osrun.MkTakeover, osrun.MkAck, osrun.MkBootSeen:
		default:
			if m.N == n {
				out = append(out, short(m))
			}
		}
	}
	return out
}

// files are the files of the run that the reducer reads.
func (b *driverBox) files() osrun.Files {
	has := func(name string) bool { return b.exists(b.bk + "/" + name) }
	f := osrun.Files{
		Begun: has("begun-upgrade"), DoneMinimal: has("done-minimal"), DoneFull: has("done-full"), DonePrereboot: has("done-prereboot"), DoneAfter: has("done-after"),
		DoneTimers: has("done-timers"), AptOrig: has("apt-orig"), AbortFlag: b.exists(b.dir + "/abort"),
	}
	first, _, _ := strings.Cut(b.read(b.bk+"/boot_id"), "\n")
	f.BootIDFile = strings.TrimSpace(first)
	return f
}

// input is what the core reads: the log, the files, a unit that answers and is not running, the boot that the machine is in, and the time two minutes after the last word of the unit.
func (b *driverBox) input() osrun.Input {
	b.t.Helper()
	ms := b.markers()
	var last time.Time
	for _, m := range ms {
		if m.At.After(last) {
			last = m.At
		}
	}
	boot := strings.TrimSpace(b.read(b.root + "/bootid"))
	return osrun.Input{
		Markers: ms, Files: b.files(), Unit: osrun.Unit{Answered: true}, Boot: osrun.Boot{Now: boot, OK: osrun.ValidUUID(boot)},
		Now: last.Add(2 * time.Minute), LastActivity: last,
	}
}

// state is the state of the run as the core reduces it now, and what the core would do next.
func (b *driverBox) state() (osrun.State, osrun.Action) {
	b.t.Helper()
	in := b.input()
	s := osrun.Reduce(in)
	return s, osrun.NextAction(s, in)
}

// checkInvariants are what a state is whatever came before: the point of no return is the files', busy is what the table says, and a state that is not busy has no half-done upgrade.
func checkInvariants(t *testing.T, where string, s osrun.State, files osrun.Files) {
	t.Helper()
	if s.PNR != files.PNR() || s.Reversible == s.PNR {
		t.Errorf("%s: pnr %v and reversible %v, the files say pnr %v: %+v", where, s.PNR, s.Reversible, files.PNR(), s)
	}
	if s.PNR && !files.DoneAfter && !s.Busy && s.Name != osrun.StateSucceeded && s.Name != osrun.StateSucceededWithFindings && s.Name != osrun.StateManualDone {
		t.Errorf("%s: the upgrade has begun, nothing has ended it, and the machine is not busy: %+v", where, s)
	}
	if !slices.Contains(osrun.States, s.Name) {
		t.Errorf("%s: %q is no state", where, s.Name)
	}
}

// lockedBuffer is a buffer that the driver's output goes to and a test reads: the driver writes from another process, and a test may read while it runs.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
