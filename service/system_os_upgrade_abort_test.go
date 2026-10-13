package service

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// aHopAtItsDownload is a hop whose unit runs: the first five blocks ended, and the next is the minimal upgrade, where the point of no return is.
func (b *upgradeBox) aHopAtItsDownload() *osUpgradeRun {
	b.t.Helper()
	run := b.aRun(b.anUpgradePlan("ok", nil), osrun.RunHop)
	run = b.attempt(run, 1, osrun.PhaseRun, osrun.WhoOwner)
	for i, id := range []string{osrun.BlockVerify, osrun.BlockTimers, osrun.BlockSnapshot, osrun.BlockSources, osrun.BlockDownload} {
		run = b.block(run, 1, i+1, id, true)
	}
	run = b.leave(run, "apt-orig", "done-timers", "done-download")
	b.setUnit("active")
	b.advance(time.Second)
	return b.own(run, osrun.StateRunning)
}

// theUnitsDriver is what the unit's driver does at the one place where the owner's call to stop and the point of no return meet, in the order that the driver does it: it writes
// that the minimal upgrade begins, and then reads the owner's flag; with the flag it stops, without it the block runs and writes the file of the point of no return.
type theUnitsDriver struct {
	box *upgradeBox
	run *osUpgradeRun

	mu      sync.Mutex
	events  []string
	began   bool
	stopped bool
	running bool
}

func (d *theUnitsDriver) log(event string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, event)
}

func (d *theUnitsDriver) happened(event string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range d.events {
		if e == event {
			return true
		}
	}
	return false
}

// writesTheBlock is the driver's write of the start of the minimal upgrade.
func (d *theUnitsDriver) writesTheBlock() {
	d.box.say(d.box.read(d.run.ID), osrun.Marker{Kind: osrun.MkBlockStart, N: 1, Index: 6, ID: osrun.BlockMinimal})
	d.mu.Lock()
	d.began = true
	d.mu.Unlock()
	d.log("block starts")
}

// readsTheFlag is the driver's read of the flag, after the write.
func (d *theUnitsDriver) readsTheFlag() {
	run := d.box.read(d.run.ID)
	if _, err := os.Lstat(filepath.Join(run.Dir, osUpgradeAbortFile)); err == nil {
		d.box.say(run, osrun.Marker{Kind: osrun.MkStop, N: 1, Index: 6, ID: osrun.BlockMinimal, RC: 0, Class: osrun.ClassAbort, PNR: false})
		d.mu.Lock()
		d.stopped = true
		d.mu.Unlock()
		d.log("driver stops")
		return
	}
	d.box.leave(run, "begun-upgrade")
	d.mu.Lock()
	d.running = true
	d.mu.Unlock()
	d.log("block runs")
}

func (d *theUnitsDriver) minimalRuns() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running
}

// ---- the abort ---------------------------------------------------------------------------------------------------------------------

// The owner calls off a run that has not reached its point of no return: the flag is written for the driver, the unit is stopped, and the core's next look undoes what was done.
func TestTheOwnerCallsOffARunBeforeItsPointOfNoReturn(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopAtItsDownload()

	status, err := box.AbortSystemOSUpgrade()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(run.Dir, osUpgradeAbortFile)); err != nil {
		t.Errorf("the flag for the driver is not there: %v", err)
	}
	if stops := box.commandsWith("systemctl stop"); len(stops) != 1 || stops[0] != "systemctl stop "+systemOSUpgradeUnit {
		t.Errorf("the unit was stopped like this: %q", stops)
	}
	if status.RunID != run.ID {
		t.Errorf("the status is %+v", status)
	}

	// the unit is gone, and the core says what it was doing: it is undone, and the run ends as a call that the owner made
	box.advance(time.Minute)
	tick := box.tick()
	attempts := box.attemptsOf(run)
	if len(attempts) != 2 || attempts[1].Phase != osrun.PhaseUnwind || tick.Action != osrun.DoUnwind {
		t.Fatalf("the attempts are %+v, the look did %q", attempts, tick.Action)
	}
	run = box.say(box.read(run.ID), osrun.Marker{Kind: osrun.MkUnitStart, N: 2, Phase: osrun.PhaseUnwind, Boot: boxBoot1, PID: 200},
		osrun.Marker{Kind: osrun.MkUnwound, N: 2, Result: "ok"}, osrun.Marker{Kind: osrun.MkUnitEnd, N: 2, Result: "ok"})
	box.setUnit("inactive")
	box.advance(time.Minute)
	if state := box.state(run); state.Name != osrun.StateFailed || state.Reason != osrun.ClassAbort || state.PNR {
		t.Errorf("the run is %+v", state)
	}
}

// The feature's switch is for what begins something: a run that is open is called off with it off.
func TestARunIsCalledOffWithTheSwitchOff(t *testing.T) {
	box := newUpgradeBox(t)
	box.aHopAtItsDownload()
	if osUpgradeBetaOn() {
		t.Fatal("the switch was on")
	}
	if _, err := box.AbortSystemOSUpgrade(); err != nil {
		t.Fatalf("a run that is open cannot be called off with the switch off: %v", err)
	}
}

// A rehearsal is called off the same way: it is a run before its point of no return.
func TestARehearsalIsCalledOffTheSameWay(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan("ok", nil), osrun.RunRehearsal)
	run = box.attempt(run, 1, osrun.PhaseRehearse, osrun.WhoOwner)
	run = box.block(run, 1, 5, osrun.BlockDownload, false)
	box.setUnit("active")
	box.own(run, osrun.StateRehearsing)

	if _, err := box.AbortSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	if len(box.commandsWith("systemctl stop")) != 1 {
		t.Errorf("the unit was stopped %d times", len(box.commandsWith("systemctl stop")))
	}
}

// The refusals say why: the point of no return is behind the run, the block that comes after it has begun, or there is nothing that runs. The flag is not left behind by
// a refusal, since the driver would stop at its next block on it.
func TestTheOwnerCannotCallOffWhatCannotBeCalledOff(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		set    func(box *upgradeBox) *osUpgradeRun
	}{
		{"the point of no return is behind it", "point_of_no_return", func(box *upgradeBox) *osUpgradeRun {
			run := box.aHopAtItsDownload()
			run = box.block(run, 1, 6, osrun.BlockMinimal, false)
			return box.leave(run, "begun-upgrade")
		}},
		{"the minimal upgrade has begun", "past_block", func(box *upgradeBox) *osUpgradeRun {
			run := box.aHopAtItsDownload()
			return box.block(run, 1, 6, osrun.BlockMinimal, false)
		}},
		{"the repair block runs", "past_block", func(box *upgradeBox) *osUpgradeRun {
			run := box.aHopAtItsDownload()
			return box.block(run, 1, 6, osrun.BlockForward, false)
		}},
		{"the unit is not running: the machine waits for its restart", "not_running", func(box *upgradeBox) *osUpgradeRun {
			run := box.aHopAtItsRestart()
			return box.own(run, osrun.StateAwaitingReboot)
		}},
		{"the run failed", "not_running", func(box *upgradeBox) *osUpgradeRun {
			run := box.aRun(box.anUpgradePlan("ok", nil), osrun.RunHop)
			run = box.attempt(run, 1, osrun.PhaseRun, osrun.WhoOwner)
			run = box.stop(run, 1, 2, osrun.BlockTimers, osrun.ClassBusy, false)
			run = box.say(run, osrun.Marker{Kind: osrun.MkUnwound, N: 1, Result: "ok"})
			run = box.end(run, 1, "stopped")
			box.setUnit("inactive")
			return box.own(run, osrun.StateRunning)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			box := newUpgradeBox(t)
			run := c.set(box)
			_, err := box.AbortSystemOSUpgrade()
			refusal := refusalOf(t, err)
			if refusal.Code != OSUpgradeNotAbortable || refusal.Data["reason"] != c.reason {
				t.Errorf("refused with %q %v, want %q %q", refusal.Code, refusal.Data, OSUpgradeNotAbortable, c.reason)
			}
			if _, err := os.Lstat(filepath.Join(run.Dir, osUpgradeAbortFile)); err == nil {
				t.Error("the flag was left behind by a refusal")
			}
			if len(box.commandsWith("systemctl stop")) != 0 {
				t.Errorf("the unit was stopped: %q", box.commandsWith("systemctl stop"))
			}
		})
	}
}

func TestThereIsNothingToCallOffWithoutARun(t *testing.T) {
	box := newUpgradeBox(t)
	_, err := box.AbortSystemOSUpgrade()
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeNoRun {
		t.Errorf("refused with %q", refusal.Code)
	}
}

// A second call while one is under way is the same call: the answer is the status, and the unit is not stopped again.
func TestACallThatIsUnderWayIsNotMadeAgain(t *testing.T) {
	box := newUpgradeBox(t)
	box.aHopAtItsDownload()
	if _, err := box.AbortSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	box.setUnit("active") // the unit has not gone yet
	if _, err := box.AbortSystemOSUpgrade(); err != nil {
		t.Fatal(err)
	}
	if got := box.commandsWith("systemctl stop"); len(got) != 1 {
		t.Errorf("the unit was stopped %d times", len(got))
	}
}

// A unit that cannot be stopped (systemd that is slow, a unit that is not there yet) is no reason to take the call back: the driver stops at its next block on the flag, and
// the answer is the status.
func TestACallWhoseStopFailsStandsOnTheFlag(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aHopAtItsDownload()
	box.mu.Lock()
	box.stopErr = os.ErrDeadlineExceeded
	box.mu.Unlock()

	if _, err := box.AbortSystemOSUpgrade(); err != nil {
		t.Fatalf("a call whose stop failed was refused: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(run.Dir, osUpgradeAbortFile)); err != nil {
		t.Errorf("the flag is not there: %v", err)
	}
}

// ---- the call and the driver are never out of step -----------------------------------------------------------------------------------

// The driver writes that the minimal upgrade begins and then reads the flag, and the core writes the flag and then reads the log. One of the two always sees the other, in every
// order that the steps of the two can come in: the minimal upgrade never runs after the core said that the call was taken, and the unit is never stopped while it runs. (The
// positions are the places of the abort where the driver can move: before it begins, between its steps, and after.)
func TestTheCallAndTheDriverAreNeverOutOfStep(t *testing.T) {
	// the number of the places that the abort of this run goes through, counted on a run that it takes
	places := 0
	{
		box := newUpgradeBox(t)
		box.aHopAtItsDownload()
		was := osUpgradeAbortStep
		osUpgradeAbortStep = func(string) { places++ }
		_, err := box.AbortSystemOSUpgrade()
		osUpgradeAbortStep = was
		if err != nil || places < 3 {
			t.Fatalf("the abort went through %d places (err %v)", places, err)
		}
	}

	for first := 0; first <= places+1; first++ {
		for second := first; second <= places+1; second++ {
			t.Run("D1 at "+strconv.Itoa(first)+" and D2 at "+strconv.Itoa(second), func(t *testing.T) {
				box := newUpgradeBox(t)
				run := box.aHopAtItsDownload()
				d := &theUnitsDriver{box: box, run: run}
				box.mu.Lock()
				box.onStop = func() {
					if d.minimalRuns() {
						d.log("STOP WHILE THE BLOCK RUNS")
					}
					d.log("unit stopped")
				}
				box.mu.Unlock()

				position := 0
				at := func(n int) {
					if first == n {
						d.writesTheBlock()
					}
					if second == n {
						d.readsTheFlag()
					}
				}
				was := osUpgradeAbortStep
				osUpgradeAbortStep = func(string) {
					position++
					at(position)
				}
				t.Cleanup(func() { osUpgradeAbortStep = was })

				at(0)
				_, err := box.AbortSystemOSUpgrade()
				for position++; position <= places+1; position++ {
					at(position)
				}
				osUpgradeAbortStep = was

				accepted := err == nil
				if d.happened("STOP WHILE THE BLOCK RUNS") {
					t.Errorf("the unit was stopped while the minimal upgrade ran: %v", d.events)
				}
				if accepted && d.minimalRuns() {
					t.Errorf("the call was taken and the minimal upgrade ran all the same: %v", d.events)
				}
				if !accepted {
					if refusal := refusalOf(t, err); refusal.Code != OSUpgradeNotAbortable {
						t.Errorf("refused with %q", refusal.Code)
					}
					// a refusal that leaves its flag would stop the driver at the next block, after the owner was told that it could not be
					if _, statErr := os.Lstat(filepath.Join(run.Dir, osUpgradeAbortFile)); statErr == nil && !d.stopped {
						t.Errorf("the call was refused and its flag is still there: %v", d.events)
					}
				}
				if d.stopped && box.read(run.ID).Files.PNR() {
					t.Errorf("the driver stopped on the flag and the point of no return was written: %v", d.events)
				}
			})
		}
	}
}
