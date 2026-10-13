package osplan

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// What each phase of a unit ends with, how the driver tells the class of a stop, and what of the driver reaches a block.

var (
	refreshList  = osrun.BlockList(osrun.PhaseRefresh, osrun.Files{})
	rehearseList = osrun.BlockList(osrun.PhaseRehearse, osrun.Files{})
)

func TestTheRefreshEndsWithTheWordOfItsLastBlock(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		for _, tc := range []struct {
			name, file string // what the last block left in refresh-reboot, "-" for no file
			want       string
			state      string
		}{
			{"a restart wanted for a kernel", "yes kernel\n", "REFRESH_DONE yes kernel", osrun.StateRebootFirst},
			{"a restart wanted by the file of the packages", "yes reboot-required\n", "REFRESH_DONE yes reboot-required", osrun.StateRebootFirst},
			{"no restart", "no none\n", "REFRESH_DONE no none", osrun.StateRefreshed},
			{"no file", "-", "REFRESH_DONE no none", osrun.StateRefreshed},
			{"an empty file", "", "REFRESH_DONE no none", osrun.StateRefreshed},
			{"a restart and no reason", "yes\n", "REFRESH_DONE yes none", osrun.StateRebootFirst},
			{"a file that does not end", "yes kernel", "REFRESH_DONE yes kernel", osrun.StateRebootFirst},
			{"a word that is neither", "maybe kernel\n", "REFRESH_DONE no kernel", osrun.StateRefreshed},
			{"a reason that no marker holds", "yes kernel;reboot\n", "REFRESH_DONE yes none", osrun.StateRebootFirst},
			{"a reason that is too long for a marker", "yes " + strings.Repeat("a", 65) + "\n", "REFRESH_DONE yes none", osrun.StateRebootFirst},
		} {
			b := newStubBox(t, sh, osrun.RunRefresh)
			if tc.file != "-" {
				b.write(b.root+"/refresh-reboot", tc.file, 0o644)
			}
			b.launch(1, osrun.PhaseRefresh, refreshList...)
			want := append(append([]string{"UNIT_START refresh"}, blockStory(1, refreshList...)...), tc.want, "UNIT_END ok")
			equalStory(t, tc.name, b.story(1), want)
			// the folder of the refresh is put in the folder of the run when the refresh is done, and nothing was undone
			if b.exists(b.bk) || !b.exists(b.dir+"/bk-done/done-rdone") || b.exists(b.dir+"/bk-aborted") {
				t.Errorf("%s: the folder is not where the end of a refresh puts it", tc.name)
			}
			for _, l := range b.trace() {
				if l == "restore" || strings.HasPrefix(l, "systemctl") {
					t.Errorf("%s: %q ran", tc.name, l)
				}
			}
			s, _ := b.state()
			if s.Name != tc.state || s.PNR {
				t.Errorf("%s: %+v", tc.name, s)
			}
		}
	})
}

// A refresh that stops puts the sources and the timers back, and finishes what dpkg left half done when dpkg says that it did leave something: block F, which is run by the
// unwind of a refresh and by no other.
func TestTheUnwindOfARefreshFinishesWhatDpkgLeftAndTheUnwindOfAHopDoesNot(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		for _, tc := range []struct {
			name        string
			kind        string
			audit       string
			unwind      bool // the unwind is a unit of its own, after a unit that was killed
			forwardRuns bool
		}{
			{"a refresh that stops, and dpkg is not quiet", osrun.RunRefresh, "half\n", false, true},
			{"a refresh that stops, and dpkg is quiet", osrun.RunRefresh, "", false, false},
			{"a refresh that is killed, and dpkg is not quiet", osrun.RunRefresh, "half\n", true, true},
			{"a refresh that is killed, and dpkg is quiet", osrun.RunRefresh, "", true, false},
			{"a hop that stops, and dpkg is not quiet", osrun.RunHop, "half\n", false, false},
		} {
			b := newStubBox(t, sh, tc.kind)
			b.write(b.root+"/audit.txt", tc.audit, 0o644)
			phase, list := osrun.PhaseRefresh, refreshList
			stopAt := osrun.BlockRUpgrade
			if tc.kind == osrun.RunHop {
				phase, list, stopAt = osrun.PhaseRun, hopList, osrun.BlockDownload
			}
			if tc.unwind {
				b.hookAfter(1+2*2+1, "kill -9 $$")
				b.launch(1, phase, list...)
				b.launch(2, osrun.PhaseUnwind)
			} else {
				b.fail(stopAt, 1, osrun.ClassAptFailed)
				b.launch(1, phase, list...)
			}
			ran := slices.Contains(b.trace(), "ran forward")
			if ran != tc.forwardRuns {
				t.Errorf("%s: block F ran: %v\n%q", tc.name, ran, b.trace())
			}
			if tc.forwardRuns {
				// the sources and the timers first, then dpkg is asked, then F, then the folder is moved
				at := func(s string) int { return slices.Index(b.trace(), s) }
				if !(at("restore") >= 0 && at("restore") < at("systemctl start apt-daily.timer") && at("systemctl start apt-daily.timer") < at("dpkg --audit") && at("dpkg --audit") < at("ran forward")) {
					t.Errorf("%s: the order of the unwind: %q", tc.name, b.trace())
				}
			}
			s, _ := b.state()
			if s.Name != osrun.StateFailed || s.Busy || b.exists(b.bk) || !b.exists(b.dir+"/bk-aborted") {
				t.Errorf("%s: %+v", tc.name, s)
			}
		}
		// F that does not end leaves the unwind partial, and the owner is told
		b := newStubBox(t, sh, osrun.RunRefresh)
		b.write(b.root+"/audit.txt", "half\n", 0o644)
		b.fail(osrun.BlockRUpgrade, 1, osrun.ClassAptFailed)
		b.fail(osrun.BlockForward, 1, osrun.ClassAptFailed)
		b.launch(1, osrun.PhaseRefresh, refreshList...)
		got := b.story(1)
		if got[len(got)-2] != "UNWOUND partial" {
			t.Errorf("F that failed: %q", got)
		}
		wantState(t, b, "F that failed", osrun.StateNeedsAttention, osrun.ReasonUnwindPartial, true, osrun.DoNone)
	})
}

func TestTheRehearsalEndsReadyAndLeavesItsFolderWithTheSourcesOfTheNextReleaseInPlace(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		b := newStubBox(t, sh, osrun.RunRehearsal)
		b.launch(1, osrun.PhaseRehearse, rehearseList...)
		want := append(append([]string{"UNIT_START rehearse"}, blockStory(1, rehearseList...)...), "REHEARSAL_READY", "UNIT_END ok")
		equalStory(t, "the markers of a rehearsal", b.story(1), want)
		if !b.exists(b.bk+"/apt-orig") || !b.exists(b.bk+"/done-download") || b.exists(b.dir+"/bk-aborted") {
			t.Error("the folder of a rehearsal is not where it stays, with its copy of the sources")
		}
		for _, l := range b.trace() {
			if l == "restore" || strings.HasPrefix(l, "systemctl") || strings.HasPrefix(l, "sync") || l == "ran minimal" {
				t.Errorf("%q ran", l)
			}
		}
		s := wantState(t, b, "a rehearsal that is ready", osrun.StateRehearsed, "", true, osrun.DoNone)
		if s.ExpiresAt == nil {
			t.Error("a rehearsal that does not expire")
		}
		// the same rehearsal that stops is undone as a hop is
		b = newStubBox(t, sh, osrun.RunRehearsal)
		b.fail(osrun.BlockDownload, 7, osrun.ClassNetwork)
		b.launch(1, osrun.PhaseRehearse, rehearseList...)
		got := b.story(1)
		if got[len(got)-3] != "STOP 5 download rc=7 network pnr=false" || got[len(got)-2] != "UNWOUND ok" {
			t.Errorf("a rehearsal that stops: %q", got)
		}
		wantState(t, b, "a rehearsal that stopped", osrun.StateFailed, osrun.ClassNetwork, false, osrun.DoNone)
		// and one that was ready is undone by the unwind that the core starts: the sources are back, and the run is done
		b = newStubBox(t, sh, osrun.RunRehearsal)
		b.launch(1, osrun.PhaseRehearse, rehearseList...)
		b.launch(2, osrun.PhaseUnwind)
		equalStory(t, "the unwind of a rehearsal", b.story(2), []string{"UNIT_START unwind", "UNWOUND ok", "UNIT_END ok"})
		if b.exists(b.bk) || len(globOf(b, "bk-aborted/apt-orig.restored.*")) != 1 {
			t.Error("the rehearsal was not undone")
		}
		wantState(t, b, "a rehearsal that was undone", osrun.StateRehearsalDone, "", false, osrun.DoNone)
	})
}

// restarted is a hop that ended and whose machine was restarted: the log has the wait for the restart, the request, and the core's note of the new boot.
func restarted(t *testing.T, sh driverShell) *driverBox {
	t.Helper()
	b := newStubBox(t, sh, osrun.RunHop)
	b.launch(1, osrun.PhaseRun, hopList...)
	b.append(osrun.Marker{Kind: osrun.MkRebootRequested})
	b.write(b.root+"/bootid", driverBootB+"\n", 0o644)
	b.append(osrun.Marker{Kind: osrun.MkBootSeen, Boot: driverBootB})
	return b
}

func TestTheCheckAfterTheRestartSaysTheBootItRanInAndIsNeverUndone(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		b := restarted(t, sh)
		b.launch(2, osrun.PhaseVerify, osrun.BlockAfter)
		equalStory(t, "the check after the restart", b.story(2), []string{"UNIT_START verify", "BLOCK_START 1 after", "BLOCK_DONE 1 after", "VERIFY_DONE", "UNIT_END ok"})
		for _, m := range b.markers() {
			if m.N == 2 && (m.Kind == osrun.MkUnitStart || m.Kind == osrun.MkVerifyDone) && m.Boot != driverBootB {
				t.Errorf("%s says the boot %q", m.Kind, m.Boot)
			}
		}
		wantState(t, b, "a check that ended", osrun.StateVerifying, "", true, osrun.DoJudge)

		// a check that stops is the end of it: the machine is half upgraded no longer, and there is nothing to put back
		b = restarted(t, sh)
		b.fail(osrun.BlockAfter, 1, osrun.ClassDpkgBroken)
		b.launch(2, osrun.PhaseVerify, osrun.BlockAfter)
		equalStory(t, "a check that stops", b.story(2), []string{"UNIT_START verify", "BLOCK_START 1 after", "STOP 1 after rc=1 dpkg_broken pnr=true", "UNIT_END stopped"})
		if !b.exists(b.bk+"/begun-upgrade") || b.exists(b.dir+"/bk-aborted") {
			t.Error("the check undid something")
		}
		wantState(t, b, "a check that stopped", osrun.StateFailed, osrun.ReasonVerify, true, osrun.DoNone)

		// whatever the folder looks like: a check does not run the restore script
		b = newStubBox(t, sh, osrun.RunHop)
		b.write(b.bk+"/restore-sources.sh", "#!/bin/sh\nprintf '%s\\n' restore >> '"+b.root+"/trace.log'\n", 0o644)
		if err := os.MkdirAll(filepath.FromSlash(b.bk+"/apt-orig"), 0o755); err != nil {
			t.Fatal(err)
		}
		b.fail(osrun.BlockAfter, 1, osrun.ClassDpkgBroken)
		b.launch(1, osrun.PhaseVerify, osrun.BlockAfter)
		for _, l := range b.trace() {
			if l == "restore" || strings.HasPrefix(l, "systemctl") {
				t.Errorf("a check ran %q", l)
			}
		}
		if got := b.story(1); got[len(got)-1] != "UNIT_END stopped" || slices.Contains(got, "UNWOUND ok") || !b.exists(b.bk) {
			t.Errorf("a check that stopped with no point of no return behind it: %q", got)
		}
	})
}

// A repair takes the list that the core made from the files, and a block that has its done file is not run again: the repair block comes first and is never skipped.
func TestARepairSkipsWhatIsDoneAndRunsTheRepairBlockFirst(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		for _, withMinimal := range []bool{false, true} {
			b := newStubBox(t, sh, osrun.RunHop)
			b.fail(osrun.BlockFull, 1, osrun.ClassNetwork)
			b.launch(1, osrun.PhaseRun, hopList...)
			os.Remove(filepath.FromSlash(b.root + "/fail-full"))
			list := osrun.BlockList(osrun.PhaseRecover, b.files())
			if want := []string{osrun.BlockForward, osrun.BlockFull, osrun.BlockPrereboot}; !slices.Equal(list, want) {
				t.Fatalf("the list of the repair: %v", list)
			}
			want := []string{"UNIT_START recover", "BLOCK_START 1 forward", "BLOCK_DONE 1 forward"}
			if withMinimal {
				// the list of a core that does not look at the files: the block that ended is passed by
				list = []string{osrun.BlockForward, osrun.BlockMinimal, osrun.BlockFull, osrun.BlockPrereboot}
				want = append(want, "BLOCK_SKIPPED 2 minimal", "BLOCK_START 3 full", "BLOCK_DONE 3 full", "BLOCK_START 4 prereboot", "BLOCK_DONE 4 prereboot")
			} else {
				want = append(want, "BLOCK_START 2 full", "BLOCK_DONE 2 full", "BLOCK_START 3 prereboot", "BLOCK_DONE 3 prereboot")
			}
			b.launch(2, osrun.PhaseRecover, list...)
			equalStory(t, "the repair", b.story(2), append(want, "REBOOT_PENDING 6.1.0-53-cloud-amd64", "UNIT_END ok"))
			n := 0
			for _, l := range b.trace() {
				if l == "ran minimal" {
					n++
				}
				if strings.HasPrefix(l, "sync") && withMinimal && n > 1 {
					t.Errorf("sync ran for a minimal upgrade that was passed by")
				}
			}
			if n != 1 {
				t.Errorf("the minimal upgrade ran %d times", n)
			}
			if got := slices.Index(b.trace(), "ran forward"); got < 0 {
				t.Error("the repair block did not run")
			}
			wantState(t, b, "a repair that ended", osrun.StateAwaitingReboot, "", true, osrun.DoBarrier)
		}
	})
}

// rawStop is the fields of the STOP line of the log, as the driver wrote them.
func rawStop(b *driverBox) []string {
	for _, l := range strings.Split(b.read(b.dir+"/markers"), "\n") {
		if strings.HasPrefix(l, "CASAOS_OSU_STOP ") {
			return strings.Fields(l)
		}
	}
	return nil
}

// The class of a stop is the word that the block left in the folder of the run, if it is a word that a marker can hold; and what an earlier block left is not read.
func TestTheClassOfAStopIsTheWordTheBlockLeftIfAMarkerCanHoldIt(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		for _, tc := range []struct {
			name string
			// what the block does about its class
			body string
			want string
		}{
			{"a class", `printf '%s\n' network > "$CASAOS_OSU_DIR/stop-class"; exit 3`, "network"},
			{"a class that is not one of the classes, in the right letters", `printf '%s\n' made_up > "$CASAOS_OSU_DIR/stop-class"; exit 3`, "made_up"},
			{"capitals", `printf '%s\n' Network > "$CASAOS_OSU_DIR/stop-class"; exit 3`, "unknown"},
			{"a hyphen", `printf '%s\n' net-work > "$CASAOS_OSU_DIR/stop-class"; exit 3`, "unknown"},
			{"two words", `printf '%s\n' 'net work' > "$CASAOS_OSU_DIR/stop-class"; exit 3`, "unknown"},
			{"two lines", `printf '%s\n%s\n' network busy > "$CASAOS_OSU_DIR/stop-class"; exit 3`, "unknown"},
			{"32 letters", `printf '%s\n' ` + strings.Repeat("a", 32) + ` > "$CASAOS_OSU_DIR/stop-class"; exit 3`, strings.Repeat("a", 32)},
			{"33 letters", `printf '%s\n' ` + strings.Repeat("a", 33) + ` > "$CASAOS_OSU_DIR/stop-class"; exit 3`, "unknown"},
			{"an empty file", `: > "$CASAOS_OSU_DIR/stop-class"; exit 3`, "unknown"},
			{"no file", `exit 3`, "unknown"},
			{"a command in the word", `printf '%s\n' '$(reboot)' > "$CASAOS_OSU_DIR/stop-class"; exit 3`, "unknown"},
		} {
			b := newStubBox(t, sh, osrun.RunHop)
			b.blocks[osrun.BlockVerify] = tc.body + "\n"
			b.launch(1, osrun.PhaseRun, hopList...)
			if got := rawStop(b); len(got) != 9 || got[6] != tc.want || got[5] != "3" {
				t.Errorf("%s: %q", tc.name, got)
			}
		}
		// a class that an earlier attempt left is not the class of a block that is killed and leaves none
		b := newStubBox(t, sh, osrun.RunHop)
		b.write(b.dir+"/stop-class", "network\n", 0o644)
		b.blocks[osrun.BlockVerify] = "kill -9 $$\n"
		b.launch(1, osrun.PhaseRun, hopList...)
		if got := rawStop(b); len(got) != 9 || got[6] != "unknown" || got[5] == "0" {
			t.Errorf("a block that was killed: %q", got)
		}
		// and the class of a block that stopped is not the class of the one after it, in another attempt
		b = newStubBox(t, sh, osrun.RunHop)
		b.fail(osrun.BlockFull, 1, osrun.ClassNetwork)
		b.launch(1, osrun.PhaseRun, hopList...)
		os.Remove(filepath.FromSlash(b.root + "/fail-full"))
		b.blocks[osrun.BlockForward] = "exit 5\n"
		b.launch(2, osrun.PhaseRecover, osrun.BlockForward, osrun.BlockFull, osrun.BlockPrereboot)
		stops := 0
		for _, l := range strings.Split(b.read(b.dir+"/markers"), "\n") {
			if f := strings.Fields(l); len(f) == 9 && f[1] == driverNonce && f[0] == "CASAOS_OSU_STOP" && f[2] == "2" {
				stops++
				if f[6] != "unknown" || f[5] != "5" {
					t.Errorf("the second stop is %q", f)
				}
			}
		}
		if stops != 1 {
			t.Errorf("%d stops in attempt 2", stops)
		}
	})
}

// Of the driver, a block has the folder, the run and the attempt in its environment, a standard input that is empty, and a standard output that is the log of the unit: it has no
// nonce, and what it prints cannot be a marker, though it say it is.
func TestNothingOfTheDriversOwnReachesItsBlocks(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		b := newStubBox(t, sh, osrun.RunHop)
		forged := func(kind, rest string) string {
			return "printf '%s\\n' 'CASAOS_OSU_" + kind + " " + driverNonce + " 1 " + rest + "'\n"
		}
		b.blocks[osrun.BlockVerify] = "BK='" + b.bk + "'\nenv > '" + b.root + "/env.log'\ncat > '" + b.root + "/stdin.log'\n" + forged("BLOCK_DONE", "9 prereboot 2026-10-13T10:00:00Z") +
			forged("STOP", "9 prereboot 1 plan 0 2026-10-13T10:00:00Z") + forged("UNIT_END", "ok 2026-10-13T10:00:00Z") + "echo hello from a block\n: > \"$BK/done-verify\"\n"
		b.launch(1, osrun.PhaseRun, hopList...)
		env := b.read(b.root + "/env.log")
		if strings.Contains(env, "CASAOS_OSU_NONCE") || strings.Contains(env, driverNonce) {
			t.Errorf("the nonce is in the environment of a block:\n%s", env)
		}
		for _, want := range []string{"CASAOS_OSU_DIR=" + b.dir, "CASAOS_OSU_RUN=" + driverRunID, "CASAOS_OSU_ATTEMPT=1", "LC_ALL=C"} {
			if !strings.Contains(env, want+"\n") {
				t.Errorf("%q is not in the environment of a block", want)
			}
		}
		if got := b.read(b.root + "/stdin.log"); got != "" {
			t.Errorf("a block read the standard input: %q", got)
		}
		// the whole run went on, and the log of markers holds what the driver wrote and nothing that a block printed
		want := append(append([]string{"UNIT_START run"}, blockStory(1, hopList...)...), "REBOOT_PENDING 6.1.0-53-cloud-amd64", "UNIT_END ok")
		equalStory(t, "the markers of a run in which a block says what it likes", b.story(1), want)
		out := b.read(b.dir + "/output.log")
		if !strings.Contains(out, "hello from a block\n") || strings.Count(out, "CASAOS_OSU_") != 3 {
			t.Errorf("the output of the blocks is not in the log of the unit: %q", out)
		}
		if b.read(b.dir+"/markers") == out {
			t.Error("the log of markers and the log of the unit are one file")
		}
	})
}

// The driver runs sync once, before the minimal upgrade and after it is announced, and not for any other block.
func TestSyncRunsOnceBeforeTheMinimalUpgrade(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		b := newStubBox(t, sh, osrun.RunHop)
		b.launch(1, osrun.PhaseRun, hopList...)
		tr := b.trace()
		sync, before, after := slices.Index(tr, "sync begun=no"), slices.Index(tr, "ran download"), slices.Index(tr, "ran minimal")
		if sync < 0 || !(before < sync && sync < after) || slices.Index(tr[sync+1:], "sync begun=no") >= 0 || slices.Index(tr[sync+1:], "sync begun=yes") >= 0 {
			t.Errorf("sync does not run once between the download and the minimal upgrade: %q", tr)
		}
	})
}

// An environment that is not the unit's makes the driver leave at once, with nothing written: a bad phase or attempt is the core's mistake, and exits with 0; a missing word is
// the shell's own error.
func TestAnEnvironmentThatIsNotTheUnitsMakesTheDriverLeaveAtOnce(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		for _, tc := range []struct {
			name  string
			phase string
			at    string
			drop  []string
			zero  bool
		}{
			{"a phase that is none", "bogus", "1", nil, true},
			{"a phase and a command", "run;reboot", "1", nil, true},
			{"no phase word", "", "1", nil, false},
			{"an attempt of 0", osrun.PhaseRun, "0", nil, true},
			{"an attempt of 1000", osrun.PhaseRun, "1000", nil, true},
			{"an attempt that is not a number", osrun.PhaseRun, "x", nil, true},
			{"an attempt with a leading zero", osrun.PhaseRun, "01", nil, true},
			{"an attempt that is a path", osrun.PhaseRun, "../1", nil, true},
			{"no nonce", osrun.PhaseRun, "1", []string{"CASAOS_OSU_NONCE"}, false},
			{"no folder", osrun.PhaseRun, "1", []string{"CASAOS_OSU_DIR"}, false},
			{"no run", osrun.PhaseRun, "1", []string{"CASAOS_OSU_RUN"}, false},
		} {
			b := newStubBox(t, sh, osrun.RunHop)
			b.prepare(1, osrun.PhaseRun, hopList...)
			before := b.read(b.dir + "/markers")
			b.override, b.dropEnv = map[string]string{"CASAOS_OSU_PHASE": tc.phase, "CASAOS_OSU_ATTEMPT": tc.at}, tc.drop
			code, out := b.run(1, osrun.PhaseRun)
			if (code == 0) != tc.zero {
				t.Errorf("%s: the driver ended with %d: %s", tc.name, code, out)
			}
			if b.read(b.dir+"/markers") != before || b.exists(b.dir+"/output.log") || len(b.trace()) != 0 {
				t.Errorf("%s: something was written or run", tc.name)
			}
		}
	})
}
