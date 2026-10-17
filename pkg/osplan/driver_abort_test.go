package osplan

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The owner's call to stop is a race between two programs that do not talk to each other: the core, which writes the flag and then reads the log of markers to see how far the
// unit is, and the driver, which writes the marker of the block that begins and then reads the flag. Each does its two steps in that order, and so one of the two always sees
// the other: when the core read the log before the driver wrote the marker, the driver reads the flag afterwards and sees it; when it did not, the core sees the marker of a block
// that it must not stop.
//
// The test puts the driver at three places around the two steps of its own (before the first, between them, after the second) with a text of the driver that waits there for the
// test, and the core's two steps at every pair of places that keeps their order: the six orders in which the four steps can happen. The driver is the driver's own text, run by a
// shell, and the log and the flag are files; the core's side is written here as the rule says (the flag is created and never replaced, the log is read through the reader of the
// core, the call is refused when the point of no return is behind or the block that began is the minimal upgrade or one after it or the unit is not running, and a refusal takes
// the flag away).

// abortCore is the core's side of the call to stop a run.
type abortCore struct{ b *driverBox }

// writeFlag is the first step: the flag is created, and an existing one is left alone (a call is already under way).
func (c abortCore) writeFlag() {
	f, err := os.OpenFile(filepath.FromSlash(c.b.dir+"/abort"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		c.b.t.Fatalf("the flag could not be created: %v", err)
	}
	f.Close()
}

// pastTheMinimalUpgrade is whether a block is the minimal upgrade or one of those that come after it.
func pastTheMinimalUpgrade(id string) bool {
	return slices.Contains([]string{osrun.BlockMinimal, osrun.BlockFull, osrun.BlockPrereboot, osrun.BlockAfter, osrun.BlockForward}, id)
}

// readLog is the second step: the log and the files are read, and the call is accepted (the unit is to be stopped) or refused with a reason, and a refusal takes the flag away.
func (c abortCore) readLog() (accepted bool, why string) {
	b := c.b
	ms := b.markers()
	files := b.files()
	phase, lastStart, ended := "", "", false
	attempt := 0
	for _, m := range ms {
		if m.Kind == osrun.MkAttempt && m.N > attempt {
			attempt, phase, lastStart, ended = m.N, m.Phase, "", false
		}
		if m.N == attempt {
			switch m.Kind {
			case osrun.MkBlockStart:
				lastStart = m.ID
			case osrun.MkUnitEnd:
				ended = true
			}
		}
	}
	switch {
	case files.PNR():
		why = "point_of_no_return"
	case pastTheMinimalUpgrade(lastStart):
		why = "past_block"
	case ended || !slices.Contains([]string{osrun.PhaseRefresh, osrun.PhaseRehearse, osrun.PhaseRun}, phase):
		why = "not_running"
	}
	if why != "" {
		os.Remove(filepath.FromSlash(b.dir + "/abort"))
		return false, why
	}
	return true, ""
}

// an outcome is how a run ended after one order of the four steps.
type abortOutcome struct {
	accepted bool
	why      string
	ran      []string // the blocks that ran
	story    []string
}

// interleave runs the driver on the stub blocks, with the core's two steps at the places given (0: before the first step of the driver, 1: between its two steps, 2: after the
// second), for the block that is the idx-th of the list.
func interleave(t *testing.T, sh driverShell, idx, c1At, c2At int) abortOutcome {
	t.Helper()
	b := newStubBox(t, sh, osrun.RunHop)
	b.gateIdx = idx
	b.prepare(1, osrun.PhaseRun, hopList...)
	cmd, cancel, out := b.start(1, osrun.PhaseRun)
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- exitCode(cmd.Wait()) }()
	core := abortCore{b}
	var res abortOutcome
	exited := false
	step := func(name string) {
		switch name {
		case "C1":
			core.writeFlag()
		case "C2":
			res.accepted, res.why = core.readLog()
		}
	}
	events := func(at int) {
		if c1At == at {
			step("C1")
		}
		if c2At == at {
			step("C2")
		}
	}
	for g := 0; g <= 2; g++ {
		gate := b.dir + "/at-P" + strconv.Itoa(g)
		reached := false
		for deadline := time.Now().Add(2 * time.Minute); !exited && !reached && time.Now().Before(deadline); {
			if b.exists(gate) {
				reached = true
				break
			}
			select {
			case code := <-done:
				exited = true
				if code != 0 {
					t.Errorf("the driver ended with %d: %s", code, out.String())
				}
			case <-time.After(3 * time.Millisecond):
			}
		}
		if !reached && !exited {
			t.Fatalf("the driver did not get to the place %d: %s", g, out.String())
		}
		events(g)
		if reached {
			b.write(b.dir+"/go-P"+strconv.Itoa(g), "", 0o644)
		}
	}
	if !exited {
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("the driver ended with %d: %s", code, out.String())
			}
		case <-time.After(2 * time.Minute):
			t.Fatalf("the driver did not end: %s", out.String())
		}
	}
	for _, l := range b.trace() {
		if id, ok := strings.CutPrefix(l, "ran "); ok {
			res.ran = append(res.ran, id)
		}
	}
	res.story = b.story(1)
	// whatever the order, what is left is a run that the core can reduce, and that the files allow
	s, _ := b.state()
	checkInvariants(t, fmt.Sprintf("C1 at %d, C2 at %d", c1At, c2At), s, b.files())
	return res
}

func TestTheCallToStopAndTheMarkerOfABlockAreNeverBothMissedWhateverTheOrder(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		minimal := slices.Index(hopList, osrun.BlockMinimal) + 1
		for _, at := range [][2]int{{0, 0}, {0, 1}, {0, 2}, {1, 1}, {1, 2}, {2, 2}} {
			res := interleave(t, sh, minimal, at[0], at[1])
			name := fmt.Sprintf("C1 at %d, C2 at %d", at[0], at[1])
			stopped := slices.Contains(res.story, "STOP 6 minimal rc=0 abort pnr=false")
			ranMinimal := slices.Contains(res.ran, osrun.BlockMinimal)
			// the minimal upgrade never runs when the core said yes to the call: the unit is then stopped by the call, and the flag is how the driver is too late to run it
			if res.accepted && ranMinimal {
				t.Errorf("%s: the core accepted the call and the minimal upgrade ran (%v)\n%q", name, res.why, res.story)
			}
			// the driver either stopped at the flag or ran the block, and the two are not both
			if stopped == ranMinimal {
				t.Errorf("%s: the driver stopped %v and the minimal upgrade ran %v\n%q", name, stopped, ranMinimal, res.story)
			}
			want := map[[2]int]struct {
				accepted bool
				stopped  bool
			}{
				{0, 0}: {true, true}, {0, 1}: {false, false}, {0, 2}: {false, true}, {1, 1}: {false, false}, {1, 2}: {false, true}, {2, 2}: {false, false},
			}[at]
			if res.accepted != want.accepted || stopped != want.stopped {
				t.Errorf("%s: accepted %v (%s), stopped at the flag %v: want %v and %v\n%q", name, res.accepted, res.why, stopped, want.accepted, want.stopped, res.story)
			}
		}
	})
}

// For a block that can still be undone, the call is accepted as long as the block that began is not the minimal upgrade, and what the driver does about it is stop at the first
// block that it announces after the flag is there: nothing past the minimal upgrade is ever run for it.
func TestTheCallToStopAtABlockBeforeThePointOfNoReturnStopsTheDriverByTheNextAnnouncedBlock(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		sources := slices.Index(hopList, osrun.BlockSources) + 1
		for _, at := range [][2]int{{0, 0}, {0, 1}, {0, 2}, {1, 1}, {1, 2}, {2, 2}} {
			res := interleave(t, sh, sources, at[0], at[1])
			name := fmt.Sprintf("C1 at %d, C2 at %d", at[0], at[1])
			if slices.Contains(res.ran, osrun.BlockMinimal) {
				t.Errorf("%s: the minimal upgrade ran: %v", name, res.ran)
			}
			var stop string
			for _, l := range res.story {
				if strings.HasPrefix(l, "STOP ") {
					stop = l
				}
			}
			if !strings.HasSuffix(stop, " rc=0 abort pnr=false") {
				t.Errorf("%s: the run did not stop at the flag: %q", name, res.story)
			}
			// the flag was there before the block that began was announced: it is not run; and when it came after, the next block is the first that is not
			if want := map[[2]int]string{{0, 0}: "STOP 4 sources", {0, 1}: "STOP 4 sources", {0, 2}: "STOP 4 sources", {1, 1}: "STOP 4 sources", {1, 2}: "STOP 4 sources", {2, 2}: "STOP 5 download"}[at]; !strings.HasPrefix(stop, want) {
				t.Errorf("%s: %q, want a stop that begins %q", name, stop, want)
			}
		}
	})
}
