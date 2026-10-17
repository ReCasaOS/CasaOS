package osplan

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// What the driver does when a block stops, when the unit is lost, when the owner calls it off, and when what it is given is not what the core wrote: each run is read back by the
// core's reader and reduced by the core's reducer, so that the markers and the files are held to the states that they are meant to give.

// wantState reduces the run as it stands and holds it to a state: its name, why (a reason, a class or a cause, whichever the state has), whether the machine is busy, and the
// next thing that the core would do.
func wantState(t *testing.T, b *driverBox, where, name, why string, busy bool, next string) osrun.State {
	t.Helper()
	s, act := b.state()
	checkInvariants(t, where, s, b.files())
	got := s.Reason
	switch {
	case s.Name == osrun.StateStoppedAtWaypoint:
		got = s.Class
	case s.Name == osrun.StateInterrupted:
		got = s.Cause
	}
	if s.Name != name || got != why || s.Busy != busy || act.Kind != next {
		t.Errorf("%s: the state is %s (%q), busy %v, next %s: want %s (%q), busy %v, next %s\n%+v", where, s.Name, got, s.Busy, act.Kind, name, why, busy, next, s)
	}
	return s
}

func globOf(b *driverBox, pattern string) []string {
	m, _ := filepath.Glob(filepath.FromSlash(b.dir + "/" + pattern))
	return m
}

func TestAStopBeforeThePointOfNoReturnUndoesWhatTheBlocksDidAndMovesTheFolder(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		b := newStubBox(t, sh, osrun.RunHop)
		b.fail(osrun.BlockDownload, 7, osrun.ClassNetwork)
		b.launch(1, osrun.PhaseRun, hopList...)
		want := append([]string{"UNIT_START run"}, blockStory(1, hopList[:4]...)...)
		want = append(want, "BLOCK_START 5 download", "STOP 5 download rc=7 network pnr=false", "UNWOUND ok", "UNIT_END stopped")
		equalStory(t, "the markers of a stop before the point of no return", b.story(1), want)
		// the restore script ran, then the timers that ran before are started and the one that did not stays stopped, and no block after the stop ran
		equalStory(t, "what ran", b.trace(), []string{"ran verify", "ran timers", "ran snapshot", "ran sources", "ran download", "restore", "systemctl start apt-daily.timer", "systemctl start unattended-upgrades.service"})
		if b.exists(b.root+"/timers.state") || b.exists(b.bk) || !b.exists(b.dir+"/bk-aborted/done-snapshot") || len(globOf(b, "bk-aborted/apt-orig.restored.*")) != 1 {
			t.Errorf("the state of the timers is %v, the folder is %v, bk-aborted has %v", b.exists(b.root+"/timers.state"), b.exists(b.bk), globOf(b, "bk-aborted/*"))
		}
		wantState(t, b, "a stop before the point of no return", osrun.StateFailed, osrun.ClassNetwork, false, osrun.DoNone)
	})
}

func TestAStopAfterThePointOfNoReturnUndoesNothing(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		b := newStubBox(t, sh, osrun.RunHop)
		b.fail(osrun.BlockFull, 1, osrun.ClassNetwork)
		b.launch(1, osrun.PhaseRun, hopList...)
		want := append([]string{"UNIT_START run"}, blockStory(1, hopList[:6]...)...)
		want = append(want, "BLOCK_START 7 full", "STOP 7 full rc=1 network pnr=true", "UNIT_END stopped")
		equalStory(t, "the markers of a stop after the point of no return", b.story(1), want)
		for _, l := range b.trace() {
			if l == "restore" || strings.HasPrefix(l, "systemctl") {
				t.Errorf("%q ran after the point of no return", l)
			}
		}
		if !b.exists(b.bk+"/begun-upgrade") || !b.exists(b.bk+"/apt-orig") || !b.exists(b.root+"/timers.state") || b.exists(b.dir+"/bk-aborted") {
			t.Error("something was undone, or the folder was moved")
		}
		s := wantState(t, b, "a stop after the point of no return", osrun.StateStoppedAtWaypoint, osrun.ClassNetwork, true, osrun.DoAlert)
		if !s.Resumable || !s.HalfUpgraded {
			t.Errorf("%+v", s)
		}
	})
}

// The minimal upgrade is the block that begins the upgrade: where it stops decides, and what decides is the file that it wrote, not the word of the block.
func TestAStopInTheMinimalUpgradeIsBeforeOrAfterThePointOfNoReturnByTheFileItLeft(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		// the block takes its marker back, as block 5 does when apt failed before it changed any package
		b := newStubBox(t, sh, osrun.RunHop)
		b.blocks[osrun.BlockMinimal] = "BK='" + b.bk + "'\nprintf '%s\\n' apt_failed > \"$CASAOS_OSU_DIR/stop-class\"\nexit 100\n"
		b.launch(1, osrun.PhaseRun, hopList...)
		got := b.story(1)
		if want := "STOP 6 minimal rc=100 apt_failed pnr=false"; got[len(got)-3] != want || got[len(got)-2] != "UNWOUND ok" || got[len(got)-1] != "UNIT_END stopped" {
			t.Errorf("the minimal upgrade that did not begin: %q", got[len(got)-4:])
		}
		wantState(t, b, "the minimal upgrade that did not begin", osrun.StateFailed, osrun.ClassAptFailed, false, osrun.DoNone)
		// the block stops after its marker is written
		b = newStubBox(t, sh, osrun.RunHop)
		b.fail(osrun.BlockMinimal, 100, osrun.ClassAptFailed)
		b.launch(1, osrun.PhaseRun, hopList...)
		got = b.story(1)
		if want := "STOP 6 minimal rc=100 apt_failed pnr=true"; got[len(got)-2] != want || got[len(got)-1] != "UNIT_END stopped" || b.exists(b.dir+"/bk-aborted") {
			t.Errorf("the minimal upgrade that began: %q", got[len(got)-2:])
		}
		wantState(t, b, "the minimal upgrade that began", osrun.StateStoppedAtWaypoint, osrun.ClassAptFailed, true, osrun.DoAlert)
	})
}

// The timers are started again by a folder that was about to stop them, or did: the state of the timers is one file for the whole machine, and a manual run may have written it. A
// folder that is only at the block before has not stopped anything, and leaves the state alone.
func TestTheUnwindStartsTheTimersOnlyWhenItsOwnFolderWasAboutToStopThem(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		// the block that stops the timers writes the state and the marker that it is about to stop them, and stops: the first of them may be stopped, and all that ran are started again
		b := newStubBox(t, sh, osrun.RunHop)
		b.fail(osrun.BlockTimers, 1, osrun.ClassBusy)
		b.launch(1, osrun.PhaseRun, hopList...)
		started := 0
		for _, l := range b.trace() {
			if strings.HasPrefix(l, "systemctl start ") {
				started++
			}
		}
		if got := b.trace(); started != 2 || !slices.Contains(got, "systemctl start apt-daily.timer") || !slices.Contains(got, "systemctl start unattended-upgrades.service") {
			t.Errorf("the timers that ran were not all started again: %q", got)
		}
		if b.exists(b.root + "/timers.state") {
			t.Error("the state of the timers is left behind after they were started again")
		}
		wantState(t, b, "the timers block that stopped", osrun.StateFailed, osrun.ClassBusy, false, osrun.DoNone)

		// another run's state, and a folder that did not get to the timers
		b = newStubBox(t, sh, osrun.RunHop)
		b.write(b.root+"/timers.state", "apt-daily.timer active\n", 0o644)
		b.fail(osrun.BlockVerify, 1, osrun.ClassIdentity)
		b.launch(1, osrun.PhaseRun, hopList...)
		if got := b.trace(); len(got) != 1 || got[0] != "ran verify" || !b.exists(b.root+"/timers.state") {
			t.Errorf("the state of another run was used: %q", got)
		}

		// the folder stopped them, and nothing was running: nothing to start, and the state is not left behind
		b = newStubBox(t, sh, osrun.RunHop)
		b.blocks[osrun.BlockTimers] = "BK='" + b.bk + "'\nprintf 'apt-daily.timer inactive\\n' > '" + b.root + "/timers.state'\n: > \"$BK/done-timers\"\n"
		b.fail(osrun.BlockSnapshot, 1, osrun.ClassDisk)
		b.launch(1, osrun.PhaseRun, hopList...)
		for _, l := range b.trace() {
			if strings.HasPrefix(l, "systemctl") {
				t.Errorf("a timer that did not run was started: %q", l)
			}
		}
		wantState(t, b, "no timer was running", osrun.StateFailed, osrun.ClassDisk, false, osrun.DoNone)

		// a unit that is killed between the marker and the end of the block (some of the timers stopped) is unwound by the unit that the core starts: the timers come back
		b = newStubBox(t, sh, osrun.RunHop)
		b.blocks[osrun.BlockTimers] = "BK='" + b.bk + "'\nprintf 'apt-daily.timer active\\nunattended-upgrades.service active\\n' > '" + b.root + "/timers.state'\n: > \"$BK/timers-stopping\"\nkill -9 $PPID\n"
		b.mayDie = true
		b.launch(1, osrun.PhaseRun, hopList...)
		b.launch(2, osrun.PhaseUnwind)
		equalStory(t, "the unwind", b.story(2), []string{"UNIT_START unwind", "UNWOUND ok", "UNIT_END ok"})
		if got := b.trace(); !slices.Contains(got, "systemctl start apt-daily.timer") || !slices.Contains(got, "systemctl start unattended-upgrades.service") || b.exists(b.root+"/timers.state") {
			t.Errorf("the timers of a block that was killed were not started again: %q", got)
		}
	})
}

// What could not be put back is said, and the run is not called failed: the owner looks at the machine. The folder that holds the copy of /etc/apt stays where it was, so that there
// is something to look at and to put back from; only a folder whose contents were all put back is put aside.
func TestAnUnwindThatCouldNotPutEverythingBackSaysPartialAndTheRunNeedsAttention(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		for _, tc := range []struct {
			name  string
			setup func(b *driverBox)
			// the state of the timers, which stays when a timer could not be started
			timersStay bool
			// the copy of /etc/apt, which is still in the folder when the sources were not put back from it
			copyStays bool
		}{
			{"the restore script fails", func(b *driverBox) { b.write(b.root+"/restore-fails", "", 0o644) }, false, true},
			{"a timer cannot be started", func(b *driverBox) { b.write(b.root+"/systemctl-fails-apt-daily.timer", "", 0o644) }, true, false},
			// apt-orig is there, and the script that puts it back is not
			{"there is no restore script", func(b *driverBox) {
				b.blocks[osrun.BlockSources] = "BK='" + b.bk + "'\nmkdir -p \"$BK/apt-orig\"\n: > \"$BK/done-sources\"\n"
			}, false, true},
			{"the folder cannot be put aside", func(b *driverBox) { b.write(b.root+"/mv-fails", "", 0o644) }, false, false},
		} {
			b := newStubBox(t, sh, osrun.RunHop)
			b.write(b.root+"/bin/mv", "#!/bin/sh\n[ ! -f '"+b.root+"/mv-fails' ] || case \"$*\" in *bk-aborted*) exit 1 ;; esac\nexec /bin/mv \"$@\"\n", 0o755)
			tc.setup(b)
			b.fail(osrun.BlockDownload, 7, osrun.ClassNetwork)
			b.launch(1, osrun.PhaseRun, hopList...)
			got := b.story(1)
			if got[len(got)-2] != "UNWOUND partial" || got[len(got)-1] != "UNIT_END stopped" {
				t.Errorf("%s: %q", tc.name, got[len(got)-3:])
			}
			if b.exists(b.root+"/timers.state") != tc.timersStay {
				t.Errorf("%s: the state of the timers is there: %v", tc.name, b.exists(b.root+"/timers.state"))
			}
			// the folder stays where it was, with what is left to look at and to put back from
			if !b.exists(b.bk) || b.exists(b.dir+"/bk-aborted") || b.exists(b.bk+"/apt-orig") != tc.copyStays {
				t.Errorf("%s: the folder was put aside (%v), or the copy of /etc/apt is there: %v, and it should be: %v", tc.name, !b.exists(b.bk), b.exists(b.bk+"/apt-orig"), tc.copyStays)
			}
			wantState(t, b, tc.name, osrun.StateNeedsAttention, osrun.ReasonUnwindPartial, true, osrun.DoNone)
		}
	})
}

// A unit that is lost before the point of no return is undone by the unit that the core starts for the purpose, which has no block to run.
func TestTheUnwindPhaseUndoesTheFolderOfALostUnitAndRefusesAfterThePointOfNoReturn(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		// the unit is killed as it announces block 5: the lines are UNIT_START, then two for each block that ended, then the announcement
		b := newStubBox(t, sh, osrun.RunHop)
		b.hookAfter(1+2*4+1, "kill -9 $$")
		b.launch(1, osrun.PhaseRun, hopList...)
		equalStory(t, "the unit that was killed", b.story(1), append(append([]string{"UNIT_START run"}, blockStory(1, hopList[:4]...)...), "BLOCK_START 5 download"))
		wantState(t, b, "the unit that was killed", osrun.StateInterrupted, osrun.CauseKilled, true, osrun.DoUnwind)
		if code := b.launch(2, osrun.PhaseUnwind); code != 0 {
			t.Fatalf("the unwind ended with %d", code)
		}
		equalStory(t, "the unwind", b.story(2), []string{"UNIT_START unwind", "UNWOUND ok", "UNIT_END ok"})
		equalStory(t, "what ran", b.trace(), []string{"ran verify", "ran timers", "ran snapshot", "ran sources", "restore", "systemctl start apt-daily.timer", "systemctl start unattended-upgrades.service"})
		wantState(t, b, "the unwind that ended", osrun.StateFailed, osrun.ReasonInterruptedKilled, false, osrun.DoNone)

		// killed as it announces block 7, after the point of no return: the unwind does nothing, says so, and the owner is told
		b = newStubBox(t, sh, osrun.RunHop)
		b.hookAfter(1+2*6+1, "kill -9 $$")
		b.launch(1, osrun.PhaseRun, hopList...)
		b.launch(2, osrun.PhaseUnwind)
		equalStory(t, "the unwind after the point of no return", b.story(2), []string{"UNIT_START unwind", "STOP 0 unwind rc=1 guard pnr=true", "UNIT_END stopped"})
		if !b.exists(b.bk+"/begun-upgrade") || b.exists(b.dir+"/bk-aborted") {
			t.Error("the unwind moved a folder that is past its point of no return")
		}
		for _, l := range b.trace() {
			if l == "restore" || strings.HasPrefix(l, "systemctl") {
				t.Errorf("%q ran", l)
			}
		}
		wantState(t, b, "the unwind that found the point of no return", osrun.StateStoppedAtWaypoint, osrun.ClassGuard, true, osrun.DoAlert)

		// an unwind that is given blocks to run is not run: it undoes, and stops with the class of a list that is not the core's
		b = newStubBox(t, sh, osrun.RunHop)
		b.hookAfter(1+2*4+1, "kill -9 $$")
		b.launch(1, osrun.PhaseRun, hopList...)
		b.launch(2, osrun.PhaseUnwind, osrun.BlockVerify)
		equalStory(t, "the unwind with a list", b.story(2), []string{"UNIT_START unwind", "STOP 0 list rc=1 integrity pnr=false", "UNWOUND ok", "UNIT_END stopped"})
		ran := 0
		for _, l := range b.trace() {
			if l == "ran verify" {
				ran++
			}
		}
		if ran != 1 {
			t.Errorf("block verify ran %d times: a block ran in the unwind phase", ran)
		}
	})
}

// The owner's call to stop is a file, read after the driver says that a block begins and before it runs it, and not at all once the point of no return is behind.
func TestTheOwnersCallToStopIsReadAfterTheBlockIsAnnouncedAndIgnoredAfterThePointOfNoReturn(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		// the flag is there from the start: the first block is announced and not run
		b := newStubBox(t, sh, osrun.RunHop)
		b.write(b.dir+"/abort", "", 0o644)
		b.launch(1, osrun.PhaseRun, hopList...)
		equalStory(t, "the flag from the start", b.story(1), []string{"UNIT_START run", "BLOCK_START 1 verify", "STOP 1 verify rc=0 abort pnr=false", "UNWOUND ok", "UNIT_END stopped"})
		if len(b.trace()) != 0 {
			t.Errorf("a block or a command ran: %q", b.trace())
		}
		wantState(t, b, "the flag from the start", osrun.StateFailed, osrun.ClassAbort, false, osrun.DoNone)

		// the flag appears while block 2 runs: block 3 is announced, and not run, and what block 2 did is undone
		b = newStubBox(t, sh, osrun.RunHop)
		b.blocks[osrun.BlockTimers] += ": > \"$CASAOS_OSU_DIR/abort\"\n"
		b.launch(1, osrun.PhaseRun, hopList...)
		equalStory(t, "the flag during a block", b.story(1), append(append([]string{"UNIT_START run"}, blockStory(1, hopList[:2]...)...), "BLOCK_START 3 snapshot", "STOP 3 snapshot rc=0 abort pnr=false", "UNWOUND ok", "UNIT_END stopped"))
		equalStory(t, "what ran", b.trace(), []string{"ran verify", "ran timers", "systemctl start apt-daily.timer", "systemctl start unattended-upgrades.service"})
		wantState(t, b, "the flag during a block", osrun.StateFailed, osrun.ClassAbort, false, osrun.DoNone)

		// the flag appears while block 5 runs: the minimal upgrade is announced, and not run (no sync either)
		b = newStubBox(t, sh, osrun.RunHop)
		b.blocks[osrun.BlockDownload] += ": > \"$CASAOS_OSU_DIR/abort\"\n"
		b.launch(1, osrun.PhaseRun, hopList...)
		got := b.story(1)
		if got[len(got)-4] != "BLOCK_START 6 minimal" || got[len(got)-3] != "STOP 6 minimal rc=0 abort pnr=false" {
			t.Errorf("the flag before the minimal upgrade: %q", got[len(got)-5:])
		}
		for _, l := range b.trace() {
			if l == "ran minimal" || strings.HasPrefix(l, "sync") {
				t.Errorf("%q ran", l)
			}
		}
		if b.exists(b.bk) || b.exists(b.dir+"/bk-aborted/begun-upgrade") {
			t.Error("the upgrade begun")
		}

		// the flag appears while the minimal upgrade runs: the point of no return is behind, and it is not read
		b = newStubBox(t, sh, osrun.RunHop)
		b.blocks[osrun.BlockMinimal] += ": > \"$CASAOS_OSU_DIR/abort\"\n"
		b.launch(1, osrun.PhaseRun, hopList...)
		got = b.story(1)
		if got[len(got)-2] != "REBOOT_PENDING 6.1.0-53-cloud-amd64" || got[len(got)-1] != "UNIT_END ok" {
			t.Errorf("the flag after the point of no return: %q", got[len(got)-3:])
		}
		s, _ := b.state()
		if s.Name != osrun.StateAwaitingReboot {
			t.Errorf("%+v", s)
		}
	})
}

// A folder that is not what the core sealed is not run: the driver says so with the class of integrity and ends, with nothing undone (a driver that cannot trust its own files
// does not run the restore script that they name).
func TestAFolderThatDoesNotMatchItsSumsStopsTheDriverBeforeItRunsAnything(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		for _, tc := range []struct {
			name   string
			change func(b *driverBox)
			pnr    bool
		}{
			{"a block that was changed", func(b *driverBox) {
				b.write(b.dir+"/blocks/sources.sh", b.blocks[osrun.BlockSources]+"echo changed\n", 0o644)
			}, false},
			{"the driver itself", func(b *driverBox) {
				b.write(b.dir+"/driver.sh", b.read(b.dir+"/driver.sh")+"\n# one more line\n", 0o644)
			}, false},
			{"the name of the backup folder", func(b *driverBox) { b.write(b.dir+"/bk-path", b.bk+"-x\n", 0o644) }, false},
			{"no sums", func(b *driverBox) { os.Remove(filepath.FromSlash(b.dir + "/SHA256SUMS")) }, false},
			{"a block that is gone", func(b *driverBox) { os.Remove(filepath.FromSlash(b.dir + "/blocks/verify.sh")) }, false},
			{"a name of a folder that is not a backup folder of the upgrade, with sums that hold", func(b *driverBox) {
				b.write(b.dir+"/bk-path", "/etc\n", 0o644)
				b.sums()
			}, false},
			{"no name of a folder, with sums that hold", func(b *driverBox) {
				b.write(b.dir+"/bk-path", "\n", 0o644)
				b.sums()
			}, false},
			{"a block that was changed after the point of no return", func(b *driverBox) {
				b.write(b.bk+"/begun-upgrade", "", 0o644)
				b.write(b.dir+"/blocks/full.sh", "echo changed\n", 0o644)
			}, true},
		} {
			b := newStubBox(t, sh, osrun.RunHop)
			b.prepare(1, osrun.PhaseRun, hopList...)
			tc.change(b)
			if code, out := b.run(1, osrun.PhaseRun); code != 0 {
				t.Errorf("%s: the driver ended with %d: %s", tc.name, code, out)
			}
			want := []string{"UNIT_START run", "STOP 0 driver rc=1 integrity pnr=" + map[bool]string{false: "false", true: "true"}[tc.pnr], "UNIT_END stopped"}
			equalStory(t, tc.name, b.story(1), want)
			if len(b.trace()) != 0 || !b.exists(b.bk) || b.exists(b.dir+"/bk-aborted") {
				t.Errorf("%s: something ran or was moved: %q", tc.name, b.trace())
			}
			if tc.pnr {
				wantState(t, b, tc.name, osrun.StateStoppedAtWaypoint, osrun.ClassIntegrity, true, osrun.DoAlert)
			} else {
				wantState(t, b, tc.name, osrun.StateFailed, osrun.ClassIntegrity, false, osrun.DoNone)
			}
		}
	})
}

// The list of blocks is the one file that the sums do not cover, and the driver checks it against its own closed set, and against its own count of lines.
func TestAListThatIsNotWhatTheCoreWritesStopsTheDriver(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		unwound := func(first ...string) []string {
			return append(append([]string{"UNIT_START run"}, first...), "UNWOUND ok", "UNIT_END stopped")
		}
		for _, tc := range []struct {
			name string
			list func(b *driverBox) // what is in the file lists/1.list
			want []string
		}{
			{"no list", func(b *driverBox) { os.Remove(filepath.FromSlash(b.dir + "/lists/1.list")) }, unwound("STOP 0 list rc=1 integrity pnr=false")},
			{"an empty list", func(b *driverBox) { b.write(b.dir+"/lists/1.list", "", 0o644) }, unwound("STOP 0 list rc=1 integrity pnr=false")},
			{"a word that is no block", func(b *driverBox) { b.write(b.dir+"/lists/1.list", "verify\nbogus\ntimers\n", 0o644) },
				unwound("BLOCK_START 1 verify", "BLOCK_DONE 1 verify", "STOP 2 list rc=1 integrity pnr=false")},
			{"a path", func(b *driverBox) { b.write(b.dir+"/lists/1.list", "../blocks/verify\n", 0o644) }, unwound("STOP 1 list rc=1 integrity pnr=false")},
			{"two words in a line", func(b *driverBox) { b.write(b.dir+"/lists/1.list", "verify timers\n", 0o644) }, unwound("STOP 1 list rc=1 integrity pnr=false")},
			{"a word and a command", func(b *driverBox) { b.write(b.dir+"/lists/1.list", "verify;reboot\n", 0o644) }, unwound("STOP 1 list rc=1 integrity pnr=false")},
			{"an empty line", func(b *driverBox) { b.write(b.dir+"/lists/1.list", "verify\n\ntimers\n", 0o644) },
				unwound("BLOCK_START 1 verify", "BLOCK_DONE 1 verify", "STOP 2 list rc=1 integrity pnr=false")},
			{"a carriage return", func(b *driverBox) { b.write(b.dir+"/lists/1.list", "verify\r\ntimers\r\n", 0o644) }, unwound("STOP 1 list rc=1 integrity pnr=false")},
			{"a last line that is not ended", func(b *driverBox) { b.write(b.dir+"/lists/1.list", "verify\ntimers", 0o644) },
				unwound("BLOCK_START 1 verify", "BLOCK_DONE 1 verify", "STOP 1 list rc=1 integrity pnr=false")},
		} {
			b := newStubBox(t, sh, osrun.RunHop)
			b.prepare(1, osrun.PhaseRun, hopList...)
			tc.list(b)
			if code, out := b.run(1, osrun.PhaseRun); code != 0 {
				t.Errorf("%s: the driver ended with %d: %s", tc.name, code, out)
			}
			equalStory(t, tc.name, b.story(1), tc.want)
			for _, l := range b.trace() {
				if strings.HasPrefix(l, "ran ") && l != "ran verify" {
					t.Errorf("%s: %q ran", tc.name, l)
				}
			}
			wantState(t, b, tc.name, osrun.StateFailed, osrun.ClassIntegrity, false, osrun.DoNone)
		}
	})
}
