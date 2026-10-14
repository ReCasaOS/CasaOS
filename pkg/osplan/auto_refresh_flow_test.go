package osplan

import (
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The driver on the real blocks of a refresh: the text of the unit's refresh, run by the driver, in the box of the tests of the blocks (refreshBox, with its stub commands), so
// that every file that a block leaves is the real one. The folder of the run is the box itself, and the backup folder is its bk, which the driver takes for the folder of a
// refresh. What the page and the core read of it (the state, the next thing to do) is read back by the reducer, as in the other tests of the driver.

// A refreshFlow is a driver over the blocks of a refresh, with the box that its files and commands are.
type refreshFlow struct {
	*driverBox
	rb *refreshBox
}

func newRefreshFlow(t *testing.T, sh driverShell, accept bool) *refreshFlow {
	t.Helper()
	rb := newRefreshBox(t, accept)
	rb.write("bootid", driverBootA+"\n")
	// the box has the folder of the backups already, empty (the first block of a refresh makes it where it is not there: mkdir -m of a folder that is new is not something that
	// every file system of a machine that runs these tests allows)
	b := newDriverBox(t, sh, rb.dir, rb.dir, rb.dir+"/bk", "*/bk", "*/bk", osrun.RunRefresh)
	for _, blk := range everyAutoBlock(rb.ar) {
		b.blocks[blk.ID] = strings.ReplaceAll(rb.paths(blk.Text, "refresh-"+testRunID), "/var/run/reboot-required", "./reboot-required")
	}
	b.extraEnv = []string{"FAKE=./fake", "APT_ETC=" + b.root + "/etc-apt", "CASAOS_NO_UPDATE=1", "CASAOS_TIMERS_STATE=" + b.root + "/timers.state"}
	return &refreshFlow{driverBox: b, rb: rb}
}

// timersAreBack says that the timers are as the box had them: two of the three run, and nobody remembers their state any more.
func (f *refreshFlow) timersAreBack() bool {
	return f.rb.exists("fake/active-apt-daily.timer") && f.rb.exists("fake/active-unattended-upgrades.service") && !f.rb.exists("fake/active-apt-daily-upgrade.timer") && !f.rb.exists("timers.state")
}

func contains(list []string, s string) bool { return slices.Contains(list, s) }

func stopOf(story []string) string {
	for _, l := range story {
		if strings.HasPrefix(l, "STOP ") {
			return l
		}
	}
	return ""
}

func TestTheDriverRunsTheRefreshToItsEndAndTheFolderIsPutAway(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		for _, tc := range []struct {
			name, kernels string
			reboot        bool
			result, state string
		}{
			{"nothing to restart for", "", false, "REFRESH_DONE no none", osrun.StateRefreshed},
			{"a new kernel", "ii  linux-image-5.10.0-47-cloud-amd64 5.10.216-1\n", false, "REFRESH_DONE yes kernel", osrun.StateRebootFirst},
			{"the packages say so", "", true, "REFRESH_DONE yes reboot-required", osrun.StateRebootFirst},
		} {
			f := newRefreshFlow(t, sh, false)
			if tc.kernels != "" {
				// what dpkg lists once the full upgrade has run: the list that R0 wrote down is the one of the box
				f.rb.write("fake/kernels-after.txt", tc.kernels)
				f.rb.fake("dpkg-query", `if [ -f "$FAKE/upgraded" ]; then cat "$FAKE/kernels-after.txt"; else cat "$FAKE/kernel-list.txt"; fi`)
				f.rb.fake("apt-get", `echo "apt-get $*" >> "$FAKE/calls.log"
case "$*" in
  *' -s dist-upgrade'*) cat "$FAKE/sim.txt" ;;
  *--download-only*) ;;
  *' dist-upgrade'*) : > "$FAKE/upgraded" ;;
esac
exit 0`)
			}
			if tc.reboot {
				f.rb.write("reboot-required", "")
			}
			f.launch(1, osrun.PhaseRefresh, refreshList...)
			want := append(append([]string{"UNIT_START refresh"}, blockStory(1, refreshList...)...), tc.result, "UNIT_END ok")
			equalStory(t, tc.name, f.story(1), want)
			if t.Failed() {
				t.Logf("output.log:\n%s", f.read(f.dir+"/output.log"))
			}
			// the folder is in the folder of the run, whole, and the machine is as it was: its sources, Docker's among them, and its timers
			if f.exists(f.bk) || !f.exists(f.dir+"/bk-done/done-rdone") || !f.exists(f.dir+"/bk-done/done-timers") || f.exists(f.dir+"/bk-aborted") {
				t.Errorf("%s: the folder is not where the end of a refresh puts it", tc.name)
			}
			if !f.rb.sourcesAre(originalSources(t)) || !f.timersAreBack() {
				t.Errorf("%s: the machine is not as it was: sources %v, timers %v", tc.name, f.rb.sourcesAre(originalSources(t)), f.timersAreBack())
			}
			s, _ := f.state()
			if s.Name != tc.state || s.PNR {
				t.Errorf("%s: %+v", tc.name, s)
			}
		}
	})
}

// A refresh that stops puts the machine back: the sources of Debian 11 as the machine had them, Docker's included, and the timers that ran. It is the unit's own driver that does
// it, from the files that the blocks leave, whatever the block that stopped.
func TestARefreshThatStopsPutsTheSourcesAndTheTimersBack(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		for _, tc := range []struct {
			name, stopsAt, class string
			set                  func(f *refreshFlow)
			sourcesLeft          bool // the sources are not the machine's own when the run ends: it is what the stop says
		}{
			{"the machine is not quiet", osrun.BlockRCheck, osrun.ClassDpkgBroken, func(f *refreshFlow) { f.rb.write("fake/check-fails", "") }, false},
			{"the update does not go through", osrun.BlockRSources, osrun.ClassNetwork, func(f *refreshFlow) { f.rb.write("fake/update-fails", "") }, false},
			{"the download does not go through", osrun.BlockRUpgrade, osrun.ClassNetwork, func(f *refreshFlow) { f.rb.write("fake/download-fails", "") }, false},
			{"the old release wants to remove a package", osrun.BlockRUpgrade, osrun.ClassPlan, func(f *refreshFlow) { f.rb.write("fake/sim.txt", resimOf("fuse")) }, false},
			{"the upgrade fails and so does the repair", osrun.BlockRUpgrade, osrun.ClassAptFailed, func(f *refreshFlow) {
				f.rb.write("fake/dist-fails-1", "")
				f.rb.write("fake/fixsim-fails", "")
			}, false},
			{"the copy of the sources that is put back is not what the machine had", osrun.BlockRDone, osrun.ClassIntegrity, func(f *refreshFlow) {
				// the copy that R1 made is damaged after R2 ended, as R3 begins: it puts it back, and finds that it is not what it was
				f.hookAfter(1+2*3+1, "echo damaged > "+f.bk+"/apt-orig/sources.list")
			}, true},
		} {
			f := newRefreshFlow(t, sh, false)
			tc.set(f)
			f.launch(1, osrun.PhaseRefresh, refreshList...)
			got := f.story(1)
			stop := stopOf(got)
			if !strings.Contains(stop, " "+tc.stopsAt+" ") || !strings.HasSuffix(stop, " "+tc.class+" pnr=false") || !contains(got, "UNWOUND ok") || got[len(got)-1] != "UNIT_END stopped" {
				t.Errorf("%s: the markers: %q (the stop should be at %s, class %s)", tc.name, got, tc.stopsAt, tc.class)
			}
			// the machine is as it was, and what the refresh made is put away where an unwound run leaves its folder
			if f.rb.sourcesAre(originalSources(t)) == tc.sourcesLeft {
				t.Errorf("%s: the sources of the machine are its own: %v, and they should be: %v", tc.name, !tc.sourcesLeft, !tc.sourcesLeft)
			}
			if !f.timersAreBack() {
				t.Errorf("%s: the timers are not back: %q", tc.name, f.rb.calls())
			}
			if f.exists(f.bk) || f.exists(f.dir+"/bk-done") || !f.exists(f.dir+"/bk-aborted") {
				t.Errorf("%s: the folder is not where an unwound run leaves it", tc.name)
			}
			wantState(t, f.driverBox, tc.name, osrun.StateFailed, tc.class, false, osrun.DoNone)
		}
	})
}

// A unit that is killed in the middle of a refresh is unwound from the files that its blocks left: the core starts the unwind, and the sources and the timers come back. The
// timers are the case that holds done-timers to its place: R0 writes it before it stops the first of them, and the unwind starts them only when it finds it.
func TestARefreshThatIsKilledIsUnwoundAndTheTimersComeBack(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		// the driver is killed after the line that says that R2 began: UNIT_START, then the lines of the first block, of the second, and the start of the third
		f := newRefreshFlow(t, sh, false)
		f.hookAfter(1+2*2+1, "kill -9 $$")
		f.launch(1, osrun.PhaseRefresh, refreshList...)
		if f.rb.sourcesAre(originalSources(t)) || f.timersAreBack() {
			t.Fatal("the sources or the timers are as they were before the unwind, so the kill did not come where it should")
		}
		s, act := f.state()
		if s.Name != osrun.StateInterrupted || act.Kind != osrun.DoUnwind || !s.Busy {
			t.Fatalf("%+v, %+v", s, act)
		}
		f.launch(2, osrun.PhaseUnwind)
		equalStory(t, "the unwind", f.story(2), []string{"UNIT_START unwind", "UNWOUND ok", "UNIT_END ok"})
		if !f.rb.sourcesAre(originalSources(t)) || !f.timersAreBack() {
			t.Errorf("the machine is not as it was: sources %v, timers %v", f.rb.sourcesAre(originalSources(t)), f.timersAreBack())
		}
		wantState(t, f.driverBox, "a refresh that was killed and unwound", osrun.StateFailed, osrun.ReasonInterruptedKilled, false, osrun.DoNone)
	})
}

// When dpkg says that the refresh left something half done, the unwind runs the block that mends it, once the sources and the timers are back: it is the one place where a
// refresh runs F. A repair that wants to remove anything is refused there (the refresh removed nothing, and the repair of it removes nothing), and the unwind is then partial.
func TestTheUnwindOfARefreshRunsTheBlockThatMendsWhatDpkgLeftHalfDone(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		half := func(f *refreshFlow) {
			// dpkg is quiet until the first full upgrade fails and leaves a package half configured; its first configure fails (the ladder of R2), and the next one (F's) mends it
			f.rb.write("fake/dist-fails-1", "")
			f.rb.fake("apt-get", `echo "apt-get $*" >> "$FAKE/calls.log"
case "$*" in
  *' -s dist-upgrade'*) cat "$FAKE/sim.txt" ;;
  *' -s -f install'*) if [ -f "$FAKE/fixsim.txt" ]; then cat "$FAKE/fixsim.txt"; else cat "$FAKE/sim.txt"; fi ;;
  *--download-only*) ;;
  *' dist-upgrade'*) printf 'half configured\n' > "$FAKE/audit.txt"; exit 100 ;;
esac
exit 0`)
			f.rb.fake("dpkg", `echo "dpkg $*" >> "$FAKE/calls.log"
case "$1" in
  --audit) cat "$FAKE/audit.txt" 2> /dev/null || true ;;
  --configure) n=$(cat "$FAKE/configure.n" 2> /dev/null || echo 0); n=$((n + 1)); echo $n > "$FAKE/configure.n"; [ "$n" -gt 1 ] || exit 1; rm -f "$FAKE/audit.txt" ;;
esac
exit 0`)
		}
		realRepairs := func(f *refreshFlow) int {
			n := 0
			for _, l := range f.rb.calls() {
				if strings.HasPrefix(l, "apt-get ") && strings.HasSuffix(l, " -f install") && !strings.Contains(l, " -s -f install") {
					n++
				}
			}
			return n
		}
		f := newRefreshFlow(t, sh, false)
		half(f)
		f.launch(1, osrun.PhaseRefresh, refreshList...)
		got := f.story(1)
		if !contains(got, "STOP 3 rupgrade rc=1 dpkg_broken pnr=false") || !contains(got, "UNWOUND ok") {
			t.Fatalf("the markers: %q", got)
		}
		// the order of the unwind: the sources, the timers, then dpkg is asked, then F
		calls := f.rb.calls()
		at := func(prefix string, from int) int {
			for i := from; i < len(calls); i++ {
				if strings.HasPrefix(calls[i], prefix) {
					return i
				}
			}
			return -1
		}
		first := at("dpkg --configure -a", 0)
		start := at("systemctl start apt-daily.timer", first)
		audit := at("dpkg --audit", start)
		second := at("dpkg --configure -a", audit)
		repair := at("apt-get", second)
		if first < 0 || start < first || audit < start || second < audit || repair < second || !strings.Contains(calls[repair], " -s -f install") || realRepairs(f) != 1 {
			t.Errorf("the order of the unwind: %q", calls)
		}
		if !f.rb.sourcesAre(originalSources(t)) || !f.timersAreBack() {
			t.Error("the machine is not as it was")
		}
		wantState(t, f.driverBox, "a refresh that stopped and was mended", osrun.StateFailed, osrun.ClassDpkgBroken, false, osrun.DoNone)

		// a repair that would remove something is not run, and the unwind is partial: the owner is told, and the machine is held
		f = newRefreshFlow(t, sh, false)
		half(f)
		f.rb.write("fake/fixsim.txt", resimOf("fuse"))
		f.launch(1, osrun.PhaseRefresh, refreshList...)
		got = f.story(1)
		if !contains(got, "UNWOUND partial") {
			t.Fatalf("the markers: %q", got)
		}
		if realRepairs(f) != 0 {
			t.Errorf("the repair ran: %q", f.rb.calls())
		}
		wantState(t, f.driverBox, "a repair that removes", osrun.StateNeedsAttention, osrun.ReasonUnwindPartial, true, osrun.DoNone)
	})
}
