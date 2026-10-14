package osplan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The log of markers is where the core reads what the unit did. A driver whose log takes no line does nothing that the core could not follow: the unit says nothing and runs no block.
func TestTheDriverRunsNoBlockWhenItsLogTakesNoLine(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("there is no /dev/full to stand in for a log on a disk that is full")
	}
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		b := newStubBox(t, sh, osrun.RunHop)
		b.prepare(1, osrun.PhaseRun, hopList...)
		markers := filepath.FromSlash(b.dir + "/markers")
		if err := os.Remove(markers); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/dev/full", markers); err != nil {
			t.Fatal(err)
		}
		if code, out := b.run(1, osrun.PhaseRun); code != 0 {
			t.Errorf("the driver ended with %d: %s", code, out)
		}
		if ran := b.trace(); len(ran) != 0 {
			t.Errorf("blocks ran with a log that takes no line: %v", ran)
		}
	})
}

// A block that ran when the log stopped taking lines is the last that runs: the next is not started, and before the point of no return what the blocks changed is put back.
func TestTheDriverStopsAtTheBlockAfterTheOneWhoseEndTheLogCouldNotSay(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("there is no /dev/full to stand in for a log on a disk that is full")
	}
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		b := newStubBox(t, sh, osrun.RunHop)
		// the first block takes the log away from the driver as it ends
		b.blocks[hopList[0]] += "rm -f \"$CASAOS_OSU_DIR/markers\"; ln -s /dev/full \"$CASAOS_OSU_DIR/markers\"\n"
		b.prepare(1, osrun.PhaseRun, hopList...)
		if code, out := b.run(1, osrun.PhaseRun); code != 0 {
			t.Errorf("the driver ended with %d: %s", code, out)
		}
		var ran []string
		for _, l := range b.trace() {
			if id, ok := strings.CutPrefix(l, "ran "); ok {
				ran = append(ran, id)
			}
		}
		if len(ran) != 1 || ran[0] != hopList[0] {
			t.Errorf("the blocks that ran: %v", ran)
		}
	})
}

// What a command prints on its error output is in the log of the unit as much as what it prints on its output: apt says why it stopped on the second.
func TestWhatABlockPrintsOnItsErrorOutputIsInTheLogOfTheUnit(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		b := newStubBox(t, sh, osrun.RunHop)
		b.blocks[hopList[0]] += "echo 'E: from apt, on the error output' >&2\necho 'on the output'\n"
		b.launch(1, osrun.PhaseRun, hopList...)
		if log := b.read(b.dir + "/output.log"); !strings.Contains(log, "E: from apt, on the error output") || !strings.Contains(log, "on the output") {
			t.Errorf("the log of the unit: %q", log)
		}
	})
}

// The time of a marker is UTC whatever zone the machine is in: a rehearsal lives from the time of its marker, and a zone that is read as UTC makes it live hours too short or too long.
func TestTheTimeOfAMarkerIsUTCWhateverTheZoneOfTheMachine(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		b := newStubBox(t, sh, osrun.RunRehearsal)
		b.extraEnv = append(b.extraEnv, "TZ=America/New_York")
		b.launch(1, osrun.PhaseRehearse, rehearseList...)
		var seen int
		for _, m := range b.markers() {
			if m.Kind != osrun.MkUnitStart && m.Kind != osrun.MkRehearsalReady {
				continue
			}
			seen++
			if d := time.Since(m.At); d < -time.Minute || d > time.Minute {
				t.Errorf("%s says %v, %v from now", m.Kind, m.At, d)
			}
		}
		if seen != 2 {
			t.Errorf("%d markers with a time", seen)
		}
	})
}

// The driver names the file of the state of the timers on its own, and the blocks write it: the two are the one file, or the timers stay stopped with the unwind saying that it put
// them back.
func TestTheDriverAndTheBlocksKeepTheStateOfTheTimersInTheSameFile(t *testing.T) {
	if !strings.Contains(osrun.Driver, "\n  TS="+timersState+"\n") {
		t.Errorf("the driver does not name %s", timersState)
	}
}
