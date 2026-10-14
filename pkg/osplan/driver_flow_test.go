package osplan

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osplan/boxtest"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The driver on the real blocks: the unit's text of the real plan, run by the driver, in the box of the tests of the blocks (shellBox, with its stub commands), so that every file
// that a block leaves is the real one. The folder of the run is the box itself (the blocks run in it, with the paths that shellBox gives them), and the backup folder is its bk.

// A flowBox is a driverBox of the real blocks, with the shellBox that its files and commands are, and the text of the plan that its blocks are.
type flowBox struct {
	*driverBox
	sb *shellBox
	ar *AutoRunbook
}

// flowSetup makes the box a machine that the nine blocks of the real plan go through, one after the other: Debian 11 with the sources of the plan, two of the three timers
// running, the simulations that the plan expects, room, a kernel to start, and the files that block 8 compares.
func flowSetup(t *testing.T, sb *shellBox) {
	t.Helper()
	setupVerify(t, sb)
	boxtest.Flow(t, sb.dir, driverBootA)
	// blocks 3 and 5 simulate the full upgrade and find the removals of the plan; block 6 finds nothing left
	sb.write("fake/sim1.txt", resimOf("fuse", "libsemanage1"))
	sb.write("fake/sim2.txt", resimOf("fuse", "libsemanage1"))
}

// restartFlowBox makes the box the machine after its restart: another boot, Debian 12, the kernel that block 7 looked at.
func restartFlowBox(sb *shellBox) { boxtest.Restarted(sb.t, sb.dir, driverBootB) }

// flowOn makes the driver's box over a shellBox that has the blocks' machine in it. The folder of the log of markers is the one that the box already has when it is the copy of
// another box (the log is copied with it).
func flowOn(t *testing.T, sh driverShell, sb *shellBox, ar *AutoRunbook) *flowBox {
	t.Helper()
	b := newDriverBox(t, sh, sb.dir, sb.dir, sb.dir+"/bk", "*/bk", "*/refresh-bk", osrun.RunHop)
	for _, blk := range everyAutoBlock(ar) {
		b.blocks[blk.ID] = sb.paths(blk.Text, ar.PlanID)
	}
	// what the blocks of the unit read from their environment is the box's, and the restore script that block 3 writes takes the paths of the box from its environment too
	b.extraEnv = []string{"FAKE=./fake", "APT_ETC=" + b.root + "/etc-apt", "CASAOS_NO_UPDATE=1", "CASAOS_TIMERS_STATE=" + b.root + "/timers.state"}
	return &flowBox{driverBox: b, sb: sb, ar: ar}
}

// newFlowBox is a box that the real blocks go through, in a shell, with the machine of flowSetup.
func newFlowBox(t *testing.T, sh driverShell) *flowBox {
	t.Helper()
	sb := newShellBox(t)
	sb.auto = true
	flowSetup(t, sb)
	return flowOn(t, sh, sb, autoOf(t, *realPlan(t), AutoParams{RunID: testRunID, Hostname: testHostname, CgroupFile: "./cgroup"}))
}

// clone is a copy of the box as it is, with its log, that a test can go on with by another road.
func (f *flowBox) clone() *flowBox {
	f.t.Helper()
	dir := f.t.TempDir()
	copyDir(f.t, f.sb.dir, dir)
	sb := &shellBox{t: f.t, dir: dir, auto: true}
	return flowOn(f.t, f.sh, sb, f.ar)
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		dst := filepath.Join(to, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(dst, info.Mode().Perm()|0o700)
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, src)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// manual is the owner's text of the real plan, with the three words typed that the owner types, for the box to run a block of it.
func manualOf(t *testing.T) *Runbook {
	t.Helper()
	rb, err := BuildRunbook(*realPlan(t))
	if err != nil {
		t.Fatal(err)
	}
	rb = typed(rb, "TYPED_HOSTNAME", testHostname)
	rb = typed(rb, "ACCEPT_REMOVALS", "fuse")
	return typed(rb, "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON")
}

// manualBlock runs a block of the owner's text on the box as it is, by hand: no unit, no run, no folder of a run.
func (f *flowBox) manualBlock(rb *Runbook, id string) (string, int) {
	f.t.Helper()
	sb := &shellBox{t: f.t, dir: f.sb.dir, env: f.sb.env}
	return sb.run(rb, id)
}

// restart makes the box the machine after its restart, and the log what the core writes then: the request, and the note of the boot.
func (f *flowBox) restart() {
	f.t.Helper()
	restartFlowBox(f.sb)
	f.append(osrun.Marker{Kind: osrun.MkRebootRequested})
	f.append(osrun.Marker{Kind: osrun.MkBootSeen, Boot: driverBootB})
}

// originalSources are the files of /etc/apt that the plan was made from, by name, for a test to compare the sources of the box with.
func originalSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range d11Box(t) {
		name := f.Name
		if f.InParts {
			name = "sources.list.d/" + name
		}
		out[name] = string(f.Content)
	}
	return out
}

// sourcesAre says whether /etc/apt of the box holds exactly the sources of the plan's machine (true), or something else.
func (f *flowBox) sourcesAreTheOriginals() bool {
	for name, want := range originalSources(f.t) {
		if f.read(f.root+"/etc-apt/"+name) != want {
			return false
		}
	}
	m, _ := filepath.Glob(filepath.FromSlash(f.root + "/etc-apt/sources.list.d/*"))
	return len(m) == len(originalSources(f.t))-1
}

func (f *flowBox) aptUpgrades() int {
	return strings.Count(f.read(f.root+"/fake/begun-at-upgrade"), "\n")
}
