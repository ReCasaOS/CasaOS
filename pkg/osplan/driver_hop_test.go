package osplan

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The driver on the real blocks of the real plan: a hop from its first block to the restart and its check, and what the unit leaves when it stops or is lost at every moment of it.

func TestAHopThroughTheDriverRunsTheRealBlocksToTheRestartAndTheCheckAfterIt(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		f := newFlowBox(t, sh)
		f.launch(1, osrun.PhaseRun, hopList...)
		want := append(append([]string{"UNIT_START run"}, blockStory(1, hopList...)...), "REBOOT_PENDING 6.1.0-53-cloud-amd64", "UNIT_END ok")
		equalStory(t, "the markers of a hop on the real blocks", f.story(1), want)
		// the sources are Debian 12's, Docker's is put aside, the timers that ran are stopped, and the upgrade began once
		if src := f.read(f.root + "/etc-apt/sources.list"); !strings.Contains(src, "bookworm") || strings.Contains(src, "bullseye") ||
			!f.exists(f.root+"/etc-apt/sources.list.d/docker.list.casaos-paused") || f.exists(f.root+"/etc-apt/sources.list.d/docker.list") {
			t.Errorf("the sources after the hop: %q", src)
		}
		if f.exists(f.root+"/fake/active-apt-daily.timer") || f.exists(f.root+"/fake/active-unattended-upgrades.service") || !f.exists(f.root+"/timers.state") {
			t.Error("the timers that ran are not stopped, or their state is not kept")
		}
		if f.aptUpgrades() != 1 || f.read(f.root+"/fake/begun-at-upgrade") != "yes\n" {
			t.Errorf("the minimal upgrade was run %q", f.read(f.root+"/fake/begun-at-upgrade"))
		}
		if f.read(f.bk+"/boot_id") != driverBootA+"\n" || f.read(f.bk+"/kernel") != "6.1.0-53-cloud-amd64\n" {
			t.Error("block 7 did not write the boot and the kernel that the driver says")
		}
		s := wantState(t, f.driverBox, "a hop that ended", osrun.StateAwaitingReboot, "", true, osrun.DoBarrier)
		if s.Attempt != 1 || s.HalfUpgraded {
			t.Errorf("%+v", s)
		}

		// the machine restarts, and the check runs block 8 in the unit that the core starts for it
		f.restart()
		f.launch(2, osrun.PhaseVerify, osrun.BlockAfter)
		equalStory(t, "the check after the restart", f.story(2), []string{"UNIT_START verify", "BLOCK_START 1 after", "BLOCK_DONE 1 after", "VERIFY_DONE", "UNIT_END ok"})
		if got := f.read(f.root + "/etc-apt/sources.list.d/docker.list"); !strings.Contains(got, "bookworm") || f.exists(f.root+"/etc-apt/sources.list.d/docker.list.casaos-paused") {
			t.Errorf("Docker's source after the check: %q", got)
		}
		if !f.exists(f.root+"/fake/active-apt-daily.timer") || !f.exists(f.root+"/fake/active-unattended-upgrades.service") || f.exists(f.root+"/timers.state") || !f.exists(f.bk+"/done-after") {
			t.Error("the timers are not back, or the check did not end")
		}
		wantState(t, f.driverBox, "a check that ended", osrun.StateVerifying, "", true, osrun.DoJudge)
	})
}

// What apt gets from the unit: the environment of the blocks has no nonce, however far down the commands that they start. A stand-in for apt-get that prints its environment is
// run by every block that runs apt.
func TestNoCommandOfARealBlockSeesTheNonceOfTheRun(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		f := newFlowBox(t, sh)
		aptGet := f.read(f.root + "/bin/apt-get")
		if aptGet == "" {
			t.Fatal("the box has no stand-in for apt-get")
		}
		f.write(f.root+"/bin/apt-get-box", aptGet, 0o755)
		f.write(f.root+"/bin/apt-get", "#!/bin/sh\nenv >> \"$FAKE/apt-env.log\"\nexec \"$PWD/bin/apt-get-box\" \"$@\"\n", 0o755)
		f.launch(1, osrun.PhaseRun, hopList...)
		env := f.read(f.root + "/fake/apt-env.log")
		if strings.Count(env, "CASAOS_OSU_RUN="+testRunID+"\n") < 3 {
			t.Fatalf("apt-get did not run under the driver as the unit has it: %q", env)
		}
		if strings.Contains(env, "CASAOS_OSU_NONCE") || strings.Contains(env, driverNonce) {
			t.Error("the nonce of the run is in the environment of apt-get")
		}
		// what apt-get printed is the unit's output, and no line of it is a marker
		if out := f.read(f.dir + "/output.log"); strings.Contains(f.read(f.dir+"/markers"), "Reading package lists") || !strings.Contains(out, "Block 6 done.") {
			t.Errorf("the unit's output and the log of markers: %q", out)
		}
	})
}

// A stop in each block of the real plan, with the box that makes that block stop by the way that it does in the tests of the blocks: where it stops before the point of no return
// the sources, the timers and the folder are put back by the driver, and where it stops after, nothing is.
func TestARealBlockThatStopsIsUndoneBeforeThePointOfNoReturnAndLeftAsItIsAfter(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		for _, tc := range stopsOfTheBlocks() {
			f := newFlowBox(t, sh)
			tc.set(f.sb)
			f.launch(1, osrun.PhaseRun, hopList...)
			var stop string
			for _, l := range f.story(1) {
				if strings.HasPrefix(l, "STOP ") {
					stop = l
				}
			}
			idx := slices.Index(hopList, tc.id) + 1
			if want := "STOP " + itoa(idx) + " " + tc.id + " rc=1 " + tc.class + " pnr=" + map[bool]string{false: "false", true: "true"}[tc.pnr]; stop != want {
				t.Errorf("%s: %q, want %q\n%q", tc.name, stop, want, f.story(1))
				continue
			}
			got := f.story(1)
			unwound := got[len(got)-2] == "UNWOUND ok"
			if unwound == tc.pnr {
				t.Errorf("%s: unwound %v, point of no return %v", tc.name, unwound, tc.pnr)
			}
			if !tc.pnr {
				if !f.sourcesAreTheOriginals() || !f.exists(f.root+"/fake/active-apt-daily.timer") || !f.exists(f.root+"/fake/active-unattended-upgrades.service") || f.exists(f.bk) || !f.exists(f.dir+"/bk-aborted") {
					t.Errorf("%s: the sources are the original ones %v, the timers are back %v, the folder is %v", tc.name, f.sourcesAreTheOriginals(), f.exists(f.root+"/fake/active-apt-daily.timer"), f.exists(f.bk))
				}
				wantState(t, f.driverBox, tc.name, osrun.StateFailed, tc.class, false, osrun.DoNone)
			} else {
				if f.sourcesAreTheOriginals() || !f.exists(f.bk+"/begun-upgrade") || f.exists(f.dir+"/bk-aborted") {
					t.Errorf("%s: something was undone after the point of no return", tc.name)
				}
				wantState(t, f.driverBox, tc.name, osrun.StateStoppedAtWaypoint, tc.class, true, osrun.DoAlert)
			}
			if f.aptUpgrades() > 1 {
				t.Errorf("%s: the minimal upgrade ran %d times", tc.name, f.aptUpgrades())
			}
		}
	})
}

// stopOfABlock is a way to make a block of the real plan stop, and what the unit says of it. set makes the machine one that the block stops on, and gives back what takes the
// cause away again.
type stopOfABlock struct {
	name  string
	id    string // the block
	class string
	pnr   bool // whether the block that stops has begun the upgrade
	set   func(sb *shellBox) (fix func())
}

// removing is a cause that goes with the file that makes it.
func removing(sb *shellBox, files ...string) func() {
	for _, f := range files {
		sb.write(f, "")
	}
	return func() {
		for _, f := range files {
			os.Remove(filepath.Join(sb.dir, filepath.FromSlash(f)))
		}
	}
}

// stopsOfTheBlocks is one stop for each of the eight blocks, taken from the ones that the tests of the blocks make: the machine that a real block stops on.
func stopsOfTheBlocks() []stopOfABlock {
	return []stopOfABlock{
		{"dpkg is not quiet", osrun.BlockVerify, osrun.ClassDpkgBroken, false, func(sb *shellBox) func() {
			sb.write("fake/audit.txt", "half\n")
			return func() { os.Remove(filepath.Join(sb.dir, "fake", "audit.txt")) }
		}},
		{"a timer stays up", osrun.BlockTimers, osrun.ClassBusy, false, func(sb *shellBox) func() {
			was := sb.read("bin/systemctl")
			sb.fake("systemctl", "echo \"systemctl $*\" >> \"$FAKE/calls.log\"; case \"$1\" in is-active) echo active;; esac; exit 0")
			return func() { sb.write("bin/systemctl", was); os.Chmod(filepath.Join(sb.dir, "bin", "systemctl"), 0o755) }
		}},
		{"tar cannot write", osrun.BlockSnapshot, osrun.ClassDisk, false, func(sb *shellBox) func() {
			sb.fake("tar", "exit 2")
			return func() { os.Remove(filepath.Join(sb.dir, "bin", "tar")) }
		}},
		{"apt-get update does not go through", osrun.BlockSources, osrun.ClassNetwork, false, func(sb *shellBox) func() { return removing(sb, "fake/update-fails") }},
		{"the download fails", osrun.BlockDownload, osrun.ClassNetwork, false, func(sb *shellBox) func() { return removing(sb, "fake/download-fails") }},
		{"apt fails before it changed a package", osrun.BlockMinimal, osrun.ClassAptFailed, false, func(sb *shellBox) func() { return removing(sb, "fake/upgrade-fails") }},
		{"apt fails after it changed a package", osrun.BlockMinimal, osrun.ClassAptFailed, true, func(sb *shellBox) func() {
			return removing(sb, "fake/upgrade-fails", "fake/upgrade-touches-status")
		}},
		{"the upgrade and the repair fail", osrun.BlockFull, osrun.ClassAptFailed, true, func(sb *shellBox) func() { return removing(sb, "fake/dist-fails-1", "fake/fix-fails") }},
		{"there is no kernel to start", osrun.BlockPrereboot, osrun.ClassBarrierKernel, true, func(sb *shellBox) func() {
			os.Remove(filepath.Join(sb.dir, "boot", "vmlinuz-6.1.0-53-cloud-amd64"))
			return func() { sb.write("boot/vmlinuz-6.1.0-53-cloud-amd64", "k") }
		}},
	}
}
