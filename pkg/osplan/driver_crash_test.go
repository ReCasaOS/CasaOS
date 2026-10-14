package osplan

import (
	"flag"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The driver is killed (as a power cut or a kill of the unit does it: with no word, no trap, nothing run after) right after each line that it writes, in a hop on the real
// blocks that goes well, in each hop that stops at a block, and in each hop whose owner calls it off before a block; and in the middle of the blocks that change the machine.
// The machine that is left is what the files say, the log says what it says, and the core reduces them to a state and does the next thing that it would do: the unit that undoes,
// before the point of no return, and the owner's click on resume, after it. Whatever the moment, the run ends in a state that the files allow.

// fullCrash is whether the driver is killed after every line of every sequence. A line costs a shell for each block that ran before it, which is some 50 ms on Linux and some
// 4 s on Windows with Git Bash, so that the whole matrix is a minute on Linux (and is what CI runs) and the better part of an hour on Windows: there, three sequences are enough
// to see that the machinery works, and -fullcrash runs the rest.
var fullCrash = flag.Bool("fullcrash", runtime.GOOS != "windows", "kill the driver after every line of every sequence of a hop (the default except on Windows)")

// cutLastMarker takes the last line of the log away: the file that a block leaves is there, and the line that says that the block ended is not.
func (f *flowBox) cutLastMarker() {
	f.t.Helper()
	lines := strings.SplitAfter(f.read(f.dir+"/markers"), "\n")
	lines = lines[:len(lines)-2]
	f.write(f.dir+"/markers", strings.Join(lines, ""), 0o600)
}

// driverLines is how many lines the driver has written in the log.
func (f *flowBox) driverLines() int {
	f.t.Helper()
	n := 0
	for _, m := range f.markers() {
		switch m.Kind {
		case osrun.MkQueued, osrun.MkRun, osrun.MkAttempt, osrun.MkStartFailed, osrun.MkRebootRequested, osrun.MkRebootFailed, osrun.MkTakeover, osrun.MkAck, osrun.MkBootSeen:
		default:
			n++
		}
	}
	return n
}

// killAtCall makes a command of the box kill the unit (the driver and the block that called it, the driver first) when the call comes that the shell test match accepts for the
// nth time: before the command does what it does, or, with after, once it has. The block never sees the end of the call.
func (f *flowBox) killAtCall(cmd, match string, nth int, after bool) {
	f.t.Helper()
	real := f.root + "/bin/" + cmd
	body := f.read(real)
	if body == "" {
		f.t.Fatalf("the box has no stand-in for %s", cmd)
	}
	f.write(real+"-box", body, 0o755)
	f.mayDie = true
	run := ""
	if after {
		run = "\"$PWD/bin/" + cmd + "-box\" \"$@\"\n"
	}
	f.write(real, "#!/bin/sh\nmatch() { "+match+"; }\nif match \"$@\"; then\n  n=$(cat \"$FAKE/kill-count\" 2> /dev/null || echo 0); n=$((n + 1)); echo \"$n\" > \"$FAKE/kill-count\"\n"+
		"  if [ \"$n\" -eq "+itoa(nth)+" ]; then\n    "+run+"    pid=$(awk '$1 == \"CASAOS_OSU_UNIT_START\" { p = $6 } END { print p }' \"$PWD/markers\")\n"+
		"    kill -9 \"$pid\"\n    kill -9 \"$PPID\"\n    exit 137\n  fi\nfi\nexec \"$PWD/bin/"+cmd+"-box\" \"$@\"\n", 0o755)
}

// afterTheKill is what the core does with a run whose unit was lost, and what must be true of the machine afterwards. fix takes away what made a block stop (when one did), for
// the repair that the owner asks for after the point of no return.
func (f *flowBox) afterTheKill(t *testing.T, name string, power bool, fix func()) {
	t.Helper()
	st, act := f.state()
	files := f.files()
	checkInvariants(t, name, st, files)
	cause := osrun.CauseKilled
	if power {
		cause = osrun.CausePower
	}
	switch {
	case files.DonePrereboot:
		// the last block ended, and the restart is what is left: the files say it whatever the log got to say
		want := osrun.StateAwaitingReboot
		if power {
			want = osrun.StateResuming
		}
		if st.Name != want || !st.PNR || st.HalfUpgraded {
			t.Errorf("%s: %+v", name, st)
		}
	case files.PNR():
		// the upgrade has begun: the unit is lost, nothing is undone, and the core waits for the owner
		if st.Name != osrun.StateInterrupted || st.Cause != cause || !st.Busy || !st.HalfUpgraded || !st.Resumable || act.Kind != osrun.DoAlert {
			t.Errorf("%s: %+v, %+v", name, st, act)
			return
		}
		if f.exists(f.dir + "/bk-aborted") {
			t.Errorf("%s: the folder of an upgrade that has begun was put aside", name)
		}
		// the owner's click: the repair block and the blocks that did not end, from what the files say. The repair block comes first when the block that did not end is one
		// of the two that change packages
		if fix != nil {
			fix()
		}
		list := osrun.BlockList(osrun.PhaseRecover, files)
		forward := osrun.FirstMissing(files) != osrun.BlockPrereboot
		if len(list) == 0 || (list[0] == osrun.BlockForward) != forward {
			t.Errorf("%s: the list of the repair is %v", name, list)
			return
		}
		before := f.aptUpgrades()
		f.launch(2, osrun.PhaseRecover, list...)
		got := f.story(2)
		if got[0] != "UNIT_START recover" || got[len(got)-2] != "REBOOT_PENDING 6.1.0-53-cloud-amd64" || got[len(got)-1] != "UNIT_END ok" || got[1] != "BLOCK_START 1 "+list[0] {
			t.Errorf("%s: the repair: %q", name, got)
		}
		// the minimal upgrade is run again only when the block that has it did not end
		want := before
		if slices.Contains(list, osrun.BlockMinimal) {
			want++
		}
		if n := f.aptUpgrades(); n != want || f.exists(f.bk+"/recover-2.txt") != forward {
			t.Errorf("%s: apt upgrade was run %d times (before the repair: %d), and the repair left %v", name, n, before, f.exists(f.bk+"/recover-2.txt"))
		}
		wantState(t, f.driverBox, name+", repaired", osrun.StateAwaitingReboot, "", true, osrun.DoBarrier)
	default:
		// nothing has been changed that cannot be put back: the core starts the unit that does
		if st.Name != osrun.StateInterrupted || st.Cause != cause || !st.Busy || st.Resumable || act.Kind != osrun.DoUnwind {
			t.Errorf("%s: %+v, %+v", name, st, act)
			return
		}
		// why the run failed: the class of the stop that the unit wrote before it was lost, else the owner's call, else how the unit was lost
		reason := osrun.ReasonInterruptedKilled
		if power {
			reason = osrun.ReasonInterruptedPower
		}
		for _, m := range f.markers() {
			if m.Kind == osrun.MkStop && !m.PNR {
				reason = m.Class
			}
		}
		if (reason == osrun.ReasonInterruptedKilled || reason == osrun.ReasonInterruptedPower) && files.AbortFlag {
			reason = osrun.ClassAbort
		}
		// the timers are started again, and their state is gone, when the folder stopped them or was about to (the marker that block 1 writes before the first stop, or its done
		// file); else the state is what it was
		hadState, upgrades := f.exists(f.root+"/timers.state"), f.aptUpgrades()
		stoppedThem := files.DoneTimers || f.exists(f.bk+"/timers-stopping")
		f.launch(2, osrun.PhaseUnwind)
		equalStory(t, name+": the unwind", f.story(2), []string{"UNIT_START unwind", "UNWOUND ok", "UNIT_END ok"})
		timersRun := f.exists(f.root+"/fake/active-apt-daily.timer") && f.exists(f.root+"/fake/active-unattended-upgrades.service")
		stateStays := hadState && !stoppedThem
		if !f.sourcesAreTheOriginals() || !timersRun || f.exists(f.root+"/timers.state") != stateStays || f.exists(f.bk) || !f.exists(f.dir+"/bk-aborted") || f.aptUpgrades() != upgrades {
			t.Errorf("%s: the sources are the original ones %v, the timers run %v, the state of the timers is there %v (want %v), the folder is there %v, apt upgrade ran %d more times",
				name, f.sourcesAreTheOriginals(), timersRun, f.exists(f.root+"/timers.state"), stateStays, f.exists(f.bk), f.aptUpgrades()-upgrades)
		}
		wantState(t, f.driverBox, name+", undone", osrun.StateFailed, reason, false, osrun.DoNone)
	}
}

// A crashSequence is a way for a hop to go, in which the driver is then killed at each moment: well, stopping at a block, or with the owner's call to stop written before a block.
type crashSequence struct {
	name string
	set  func(f *flowBox) (fix func())
}

func crashSequences() []crashSequence {
	seqs := []crashSequence{{"a hop that goes well", func(f *flowBox) func() { return nil }}}
	for _, tc := range stopsOfTheBlocks() {
		seqs = append(seqs, crashSequence{"a hop that stops: " + tc.name, func(f *flowBox) func() { return tc.set(f.sb) }})
	}
	for i := range hopList {
		// the flag is written when block i has ended (when the unit has begun, for the first block), so that the next block that the driver announces is the i+1-th
		after := 1
		if i > 0 {
			after = 2*i + 1
		}
		seqs = append(seqs, crashSequence{fmt.Sprintf("a hop that the owner calls off before block %d (%s)", i+1, hopList[i]), func(f *flowBox) func() {
			f.hookAfter(after, `: > "$D/abort"`)
			return nil
		}})
	}
	return seqs
}

func TestTheDriverKilledAfterEveryMarkerOfAHopLeavesARunThatTheCoreUndoesOrKeeps(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		cases := 0
		seqs := crashSequences()
		if !*fullCrash {
			// the hop that goes well, a hop that stops before its point of no return and has the sources to put back, and one that the owner calls off just before it
			seqs = slices.DeleteFunc(seqs, func(s crashSequence) bool {
				return s.name != "a hop that goes well" && !strings.HasSuffix(s.name, "apt-get update does not go through") && !strings.Contains(s.name, "before block 6")
			})
			t.Logf("-fullcrash is off: %d sequences of %d", len(seqs), len(crashSequences()))
		}
		for _, seq := range seqs {
			// a run that is not killed says how many lines the driver writes in this sequence
			probe := newFlowBox(t, sh)
			seq.set(probe)
			probe.launch(1, osrun.PhaseRun, hopList...)
			total := probe.driverLines()
			well := seq.name == "a hop that goes well"
			for line := 1; line <= total; line++ {
				// the file of a block is there and the line that says that the block ended is not: for a line that says it, in the hop that goes well
				blockDone := well && line >= 3 && line <= 17 && line%2 == 1
				for _, variant := range []string{"", "cut", "power"} {
					if variant == "cut" && !blockDone || variant == "power" && !(well && slices.Contains([]int{1, 12, 13, 16, 17, total}, line)) {
						continue
					}
					f := newFlowBox(t, sh)
					fix := seq.set(f)
					f.hookAfter(line, "kill -9 $$")
					f.launch(1, osrun.PhaseRun, hopList...)
					name := fmt.Sprintf("%s, killed after line %d of %d", seq.name, line, total)
					switch variant {
					case "cut":
						f.cutLastMarker()
						name += " (and the line cut)"
					case "power":
						f.write(f.root+"/bootid", driverBootC+"\n", 0o644)
						name += " on another boot"
					}
					// a run that ended before the kill is the ordinary end of the sequence, which the other tests look at
					if ms := f.markers(); ms[len(ms)-1].Kind == osrun.MkUnitEnd && !f.files().DonePrereboot {
						continue
					}
					cases++
					f.afterTheKill(t, name, variant == "power", fix)
				}
			}
		}
		if cases < 150 && *fullCrash || cases < 40 {
			t.Errorf("%d cases", cases)
		}
		t.Logf("%d cases", cases)
	})
}

// The same, in the middle of the blocks that change the machine, at the command that changes it: the sources that are half written, the download, the marker of the point of
// no return that is written and nothing else, the packages that are half unpacked, the checks before the restart.
func TestTheDriverKilledInTheMiddleOfABlockLeavesARunThatTheCoreUndoesOrKeeps(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		for _, tc := range []struct {
			name       string
			cmd, match string
			nth        int
			after      bool
		}{
			{"block 1, as the first timer is being stopped", "systemctl", `[ "$1" = stop ]`, 1, false},
			{"block 1, with the first timer stopped", "systemctl", `[ "$1" = stop ]`, 1, true},
			{"block 1, as the second timer is being stopped", "systemctl", `[ "$1" = stop ]`, 2, false},
			{"block 1, with the second timer stopped", "systemctl", `[ "$1" = stop ]`, 2, true},
			{"block 3, as apt-get update begins, with the sources replaced", "apt-get", `case " $* " in *" update "*) return 0 ;; esac; return 1`, 1, false},
			{"block 4, as the download begins", "apt-get", `case "$*" in *--download-only*) return 0 ;; esac; return 1`, 1, false},
			{"block 4, with the download done", "apt-get", `case "$*" in *--download-only*) return 0 ;; esac; return 1`, 1, true},
			{"the sync of the driver, before the minimal upgrade", "sync", `return 0`, 1, false},
			{"block 5, right after its marker (its own sync)", "sync", `return 0`, 2, false},
			{"block 5, in apt, with dpkg's records touched", "apt-get", `case " $* " in *" -s "*|*--download-only*) return 1 ;; *" upgrade "*) return 0 ;; esac; return 1`, 1, true},
			{"block 6, in the full upgrade, with dpkg's records touched", "apt-get", `case " $* " in *" -s "*|*--download-only*) return 1 ;; *" dist-upgrade "*) return 0 ;; esac; return 1`, 1, true},
			{"block 6, in the pass that looks for what is left", "apt-get", `case " $* " in *" -s dist-upgrade "*) return 0 ;; esac; return 1`, 3, false},
			{"block 7, as the initrd is opened", "lsinitramfs", `return 0`, 1, false},
			{"block 7, as sshd looks at its configuration", "sshd", `[ "$1" = -t ]`, 1, false},
		} {
			for _, power := range []bool{false, true} {
				f := newFlowBox(t, sh)
				f.sb.write("fake/upgrade-touches-status", "")
				f.sb.write("fake/upgrade-leaves-journal", "")
				f.killAtCall(tc.cmd, tc.match, tc.nth, tc.after)
				f.launch(1, osrun.PhaseRun, hopList...)
				name := "killed " + tc.name
				if power {
					name += " on another boot"
					f.write(f.root+"/bootid", driverBootC+"\n", 0o644)
				}
				if got := f.read(f.root + "/fake/kill-count"); got == "" || f.story(1)[len(f.story(1))-1] == "UNIT_END ok" {
					t.Errorf("%s: the unit was not killed (%q)", name, got)
					continue
				}
				f.afterTheKill(t, name, power, nil)
			}
		}
	})
}

// The owner's flag appears at every moment of a hop (after each line of the log in turn: it is written by the core, and the driver sees it at the next block that it announces):
// before the minimal upgrade the unit stops at the next block and undoes what was done, and after it the flag is not read.
func TestTheFlagOfTheOwnersCallAtEveryBlockOfARealHopStopsTheUnitBeforeThePointOfNoReturnOnly(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		for i := 1; i <= len(hopList); i++ {
			f := newFlowBox(t, sh)
			// the flag is written when block i-1 has ended (when the unit has begun, for the first block)
			after := 1
			if i > 1 {
				after = 2*(i-1) + 1
			}
			f.hookAfter(after, `: > "$D/abort"`)
			f.launch(1, osrun.PhaseRun, hopList...)
			name := fmt.Sprintf("the flag before block %d (%s)", i, hopList[i-1])
			got := f.story(1)
			if i <= slices.Index(hopList, osrun.BlockMinimal)+1 {
				want := append(append([]string{"UNIT_START run"}, blockStory(1, hopList[:i-1]...)...), "BLOCK_START "+itoa(i)+" "+hopList[i-1], "STOP "+itoa(i)+" "+hopList[i-1]+" rc=0 abort pnr=false", "UNWOUND ok", "UNIT_END stopped")
				equalStory(t, name, got, want)
				timersRun := f.exists(f.root+"/fake/active-apt-daily.timer") && f.exists(f.root+"/fake/active-unattended-upgrades.service")
				if !f.sourcesAreTheOriginals() || !timersRun || f.exists(f.root+"/timers.state") || f.aptUpgrades() != 0 || f.exists(f.bk) {
					t.Errorf("%s: the sources are the original ones %v, the timers run %v, the minimal upgrade ran %d times", name, f.sourcesAreTheOriginals(), timersRun, f.aptUpgrades())
				}
				wantState(t, f.driverBox, name, osrun.StateFailed, osrun.ClassAbort, false, osrun.DoNone)
				continue
			}
			// the upgrade has begun: the flag is not read, and the hop goes on to the restart
			if got[len(got)-2] != "REBOOT_PENDING 6.1.0-53-cloud-amd64" || got[len(got)-1] != "UNIT_END ok" || f.aptUpgrades() != 1 {
				t.Errorf("%s: %q", name, got)
			}
			wantState(t, f.driverBox, name, osrun.StateAwaitingReboot, "", true, osrun.DoBarrier)
		}
	})
}
