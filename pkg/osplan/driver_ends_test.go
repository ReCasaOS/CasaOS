package osplan

import (
	"slices"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The end of a phase is the end of its last block: the driver does not say that a hop is ready for its restart, that a rehearsal is ready, that a refresh is done or that the check
// has ended because the list that it was given came to its end. It says what is the case, which is that the list was not the core's.
func TestAPhaseThatIsGivenTooFewBlocksIsNotAnnouncedAsDone(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		for _, tc := range []struct {
			name  string
			kind  string
			phase string
			list  []string
			// what the folder is like before: the point of no return behind it or not
			begun bool
			want  []string
		}{
			{"a hop that stops before block 7", osrun.RunHop, osrun.PhaseRun, hopList[:3], false,
				append(append([]string{"UNIT_START run"}, blockStory(1, hopList[:3]...)...), "STOP 3 list rc=1 integrity pnr=false", "UNWOUND ok", "UNIT_END stopped")},
			{"a rehearsal that stops before the download", osrun.RunRehearsal, osrun.PhaseRehearse, rehearseList[:2], false,
				append(append([]string{"UNIT_START rehearse"}, blockStory(1, rehearseList[:2]...)...), "STOP 2 list rc=1 integrity pnr=false", "UNWOUND ok", "UNIT_END stopped")},
			{"a refresh that stops before its last block", osrun.RunRefresh, osrun.PhaseRefresh, refreshList[:1], false,
				append(append([]string{"UNIT_START refresh"}, blockStory(1, refreshList[:1]...)...), "STOP 1 list rc=1 integrity pnr=false", "UNWOUND ok", "UNIT_END stopped")},
			{"a repair that is the repair block alone", osrun.RunHop, osrun.PhaseRun, []string{osrun.BlockForward}, true,
				[]string{"UNIT_START run", "BLOCK_START 1 forward", "BLOCK_DONE 1 forward", "STOP 1 list rc=1 integrity pnr=true", "UNIT_END stopped"}},
		} {
			b := newStubBox(t, sh, tc.kind)
			if tc.begun {
				b.write(b.bk+"/begun-upgrade", "", 0o644)
			}
			b.launch(1, tc.phase, tc.list...)
			equalStory(t, tc.name, b.story(1), tc.want)
			for _, m := range b.markers() {
				switch m.Kind {
				case osrun.MkRebootPending, osrun.MkRebootNotNeeded, osrun.MkRehearsalReady, osrun.MkRefreshDone, osrun.MkVerifyDone:
					t.Errorf("%s: the driver wrote %s", tc.name, m.Kind)
				}
			}
		}
		// the check after the restart that is given no block to run, and one that is given another one: it does not say that it ended, and it undoes nothing
		for _, list := range [][]string{{osrun.BlockForward}, {osrun.BlockVerify}} {
			b := restarted(t, sh)
			b.launch(2, osrun.PhaseVerify, list...)
			got := b.story(2)
			if !slices.Contains(got, "STOP 1 list rc=1 integrity pnr=true") || got[len(got)-1] != "UNIT_END stopped" || slices.Contains(got, "VERIFY_DONE") || slices.Contains(got, "UNWOUND ok") {
				t.Errorf("the check with the list %v: %q", list, got)
			}
		}
	})
}

// A list that was cut short is not taken for a complete one, even when what is left of it has nothing to do: the count of its lines is what the driver was given, and a last line
// that is not ended, or no line at all, is not the core's list whether or not the files say that the hop is over.
func TestAListThatWasCutShortIsNotTakenForTheListOfAHopThatIsOver(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		for _, tc := range []struct {
			name string
			list string
			want []string
		}{
			{"a last line that is not ended", "prereboot\nforward", []string{"UNIT_START recover", "BLOCK_SKIPPED 1 prereboot", "STOP 1 list rc=1 integrity pnr=true", "UNIT_END stopped"}},
			{"no line", "", []string{"UNIT_START recover", "STOP 0 list rc=1 integrity pnr=true", "UNIT_END stopped"}},
		} {
			b := newStubBox(t, sh, osrun.RunHop)
			for _, name := range []string{"begun-upgrade", "done-minimal", "done-full", "done-prereboot"} {
				b.write(b.bk+"/"+name, "", 0o644)
			}
			b.write(b.bk+"/kernel", "6.1.0-53-cloud-amd64\n", 0o644)
			b.write(b.bk+"/boot_id", driverBootA+"\n", 0o644)
			b.prepare(1, osrun.PhaseRecover)
			b.write(b.dir+"/lists/1.list", tc.list, 0o644)
			if code, out := b.run(1, osrun.PhaseRecover); code != 0 {
				t.Errorf("%s: the driver ended with %d: %s", tc.name, code, out)
			}
			equalStory(t, tc.name, b.story(1), tc.want)
		}
	})
}
