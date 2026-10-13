package service

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

func launchKind(t *testing.T, err error) string {
	t.Helper()
	var launch *osUpgradeLaunchError
	if !errors.As(err, &launch) {
		t.Fatalf("err = %v, want an *osUpgradeLaunchError", err)
	}
	return launch.Kind
}

func (b *upgradeBox) state(run *osUpgradeRun) osrun.State {
	b.t.Helper()
	run = b.read(run.ID)
	return osrun.Reduce(b.osUpgradeInputWith(run, osrun.Unit{Active: b.unit == "active", Answered: b.unit != ""}))
}

// A run is launched with its first attempt: the list of its blocks, then the line that says the number is taken, then the unit.
func TestTheFirstAttemptIsListedLoggedAndThenStarted(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	box.advance(time.Second)

	if err := box.launchAttempt(run.ID, osrun.PhaseRun, osrun.WhoOwner); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, filepath.Join(run.Dir, "lists", "1.list")); got != "verify\ntimers\nsnapshot\nsources\ndownload\nminimal\nfull\nprereboot\n" {
		t.Errorf("lists/1.list = %q", got)
	}
	run = box.read(run.ID)
	last := run.Markers[len(run.Markers)-1]
	if last.Kind != osrun.MkAttempt || last.N != 1 || last.Phase != osrun.PhaseRun || last.Who != osrun.WhoOwner || last.From != "" || !last.At.Equal(box.now()) {
		t.Errorf("the last line of the log is %+v", last)
	}
	started := box.startedUnits()
	if len(started) != 1 {
		t.Fatalf("%d units were started", len(started))
	}
	want, err := osUpgradeUnitArgs(osrun.Attempt{RunID: run.ID, Nonce: run.Nonce, Phase: osrun.PhaseRun, N: 1}, osUpgradeUnitRoot+"/"+run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started[0][0] != "/usr/bin/systemd-run" || started[0][1] != systemOSUpgradeUnit || !reflect.DeepEqual(started[0][2:], want) {
		t.Errorf("systemd-run was called with %q", started[0])
	}
	if st := box.state(run); st.Name != osrun.StateRunning || st.Attempt != 1 || !st.Busy {
		t.Errorf("the run is %+v: its unit is on its way", st)
	}
}

func TestEveryPhaseHasItsList(t *testing.T) {
	// the rehearsal stops before the sources are the ones of the point of no return; the repair begins with the block that mends
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunRehearsal)
	if err := box.launchAttempt(run.ID, osrun.PhaseRehearse, osrun.WhoOwner); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, filepath.Join(run.Dir, "lists", "1.list")); got != "verify\ntimers\nsnapshot\nsources\ndownload\n" {
		t.Errorf("the list of the rehearsal is %q", got)
	}

	box = newUpgradeBox(t)
	run = box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	run = box.say(run, osrun.Marker{Kind: osrun.MkAttempt, N: 1, Phase: osrun.PhaseRun, Who: osrun.WhoOwner},
		osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRun, Boot: boxBoot1, PID: 77},
		osrun.Marker{Kind: osrun.MkBlockStart, N: 1, Index: 6, ID: osrun.BlockFull},
		osrun.Marker{Kind: osrun.MkStop, N: 1, Index: 6, ID: osrun.BlockFull, RC: 1, Class: osrun.ClassNetwork, PNR: true},
		osrun.Marker{Kind: osrun.MkUnitEnd, N: 1, Result: "stopped"})
	run = box.leave(run, "begun-upgrade", "done-minimal")
	box.advance(time.Minute)
	if err := box.launchAttempt(run.ID, osrun.PhaseRecover, osrun.WhoOwner); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, filepath.Join(run.Dir, "lists", "2.list")); got != "forward\nfull\nprereboot\n" {
		t.Errorf("the list of the repair is %q: the repair block, then the block that did not end and what follows it", got)
	}
	run = box.read(run.ID)
	if a := run.Markers[len(run.Markers)-1]; a.Kind != osrun.MkAttempt || a.N != 2 || a.From != osrun.BlockFull || a.Who != osrun.WhoOwner {
		t.Errorf("the attempt is %+v: it begins with the first block that is not done", a)
	}

	// the check after the restart, and the undoing, which has no blocks
	box = newUpgradeBox(t)
	run = box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	run = box.say(run, osrun.Marker{Kind: osrun.MkAttempt, N: 1, Phase: osrun.PhaseRun, Who: osrun.WhoOwner},
		osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRun, Boot: boxBoot1, PID: 77},
		osrun.Marker{Kind: osrun.MkRebootPending, N: 1, Boot: boxBoot1, Kernel: "6.1.0-18-amd64"},
		osrun.Marker{Kind: osrun.MkUnitEnd, N: 1, Result: "ok"})
	run = box.leave(run, "begun-upgrade", "done-minimal", "done-full", "done-prereboot")
	box.setBoot(boxBoot2)
	box.advance(time.Minute)
	if err := box.launchAttempt(run.ID, osrun.PhaseVerify, osrun.WhoAuto); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, filepath.Join(run.Dir, "lists", "2.list")); got != "after\n" {
		t.Errorf("the list of the check is %q", got)
	}

	box = newUpgradeBox(t)
	run = box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	run = box.say(run, osrun.Marker{Kind: osrun.MkAttempt, N: 1, Phase: osrun.PhaseRun, Who: osrun.WhoOwner},
		osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRun, Boot: boxBoot1, PID: 77})
	box.advance(time.Hour)
	if err := box.launchAttempt(run.ID, osrun.PhaseUnwind, osrun.WhoAuto); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, filepath.Join(run.Dir, "lists", "2.list")); got != "" {
		t.Errorf("the list of the undoing is %q: it has no block", got)
	}
}

func TestNothingIsStartedWhileAUnitMayBeRunning(t *testing.T) {
	for name, c := range map[string]struct {
		unit string
		want string
	}{
		"the unit is active":       {"active", launchUnitBusy},
		"the unit is activating":   {"activating", launchUnitBusy},
		"the unit is deactivating": {"deactivating", launchUnitBusy},
		"systemd does not answer":  {"", launchUnitBusy},
	} {
		t.Run(name, func(t *testing.T) {
			box := newUpgradeBox(t)
			run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
			box.setUnit(c.unit)
			if err := box.launchAttempt(run.ID, osrun.PhaseRun, osrun.WhoOwner); launchKind(t, err) != c.want {
				t.Errorf("err = %v", err)
			}
			if n := len(box.startedUnits()); n != 0 {
				t.Errorf("%d units were started", n)
			}
			if got := box.read(run.ID); len(got.Markers) != len(run.Markers) {
				t.Errorf("the log has %d lines, it had %d: a number was taken for a unit that did not start", len(got.Markers), len(run.Markers))
			}
		})
	}
}

// A number of attempt is launched at most once: while the unit of the first is on its way, a second request starts nothing.
func TestAnAttemptThatIsOnItsWayIsNotLaunchedAgain(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	if err := box.launchAttempt(run.ID, osrun.PhaseRun, osrun.WhoOwner); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{osrun.PhaseRun, osrun.PhaseRecover, osrun.PhaseUnwind, osrun.PhaseVerify} {
		if err := box.launchAttempt(run.ID, phase, osrun.WhoAuto); launchKind(t, err) != launchUnitBusy {
			t.Errorf("%s: err = %v", phase, err)
		}
	}
	if n := len(box.startedUnits()); n != 1 {
		t.Fatalf("%d units were started for one attempt", n)
	}
	if got := box.read(run.ID); countKind(got.Markers, osrun.MkAttempt) != 1 {
		t.Errorf("the log has %d attempts", countKind(got.Markers, osrun.MkAttempt))
	}
}

func countKind(ms []osrun.Marker, kind string) int {
	n := 0
	for _, m := range ms {
		if m.Kind == kind {
			n++
		}
	}
	return n
}

// Several requests at once start one unit: the lock is what makes the number of an attempt one.
func TestAtMostOneUnitIsStartedWhateverTheRequestsAtOnce(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	var wg sync.WaitGroup
	var succeeded atomic.Int32
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if box.launchAttempt(run.ID, osrun.PhaseRun, osrun.WhoOwner) == nil {
				succeeded.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if succeeded.Load() != 1 || len(box.startedUnits()) != 1 {
		t.Fatalf("%d launches went through, and %d units were started", succeeded.Load(), len(box.startedUnits()))
	}
	if got := box.read(run.ID); countKind(got.Markers, osrun.MkAttempt) != 1 || got.Ignored != 0 {
		t.Errorf("the log has %d attempts and %d lines that are not markers", countKind(got.Markers, osrun.MkAttempt), got.Ignored)
	}
}

func TestAnAttemptThatTheLogWouldNotTakeIsNotStarted(t *testing.T) {
	for name, c := range map[string]struct {
		kind  string
		phase string
	}{
		"a repair as the first attempt":         {osrun.RunHop, osrun.PhaseRecover},
		"a check as the first attempt":          {osrun.RunHop, osrun.PhaseVerify},
		"an undoing as the first attempt":       {osrun.RunHop, osrun.PhaseUnwind},
		"a refresh in a hop":                    {osrun.RunHop, osrun.PhaseRefresh},
		"a hop in a rehearsal":                  {osrun.RunRehearsal, osrun.PhaseRun},
		"a rehearsal in a hop":                  {osrun.RunHop, osrun.PhaseRehearse},
		"a phase that is not one":               {osrun.RunHop, "reboot"},
		"a phase with an argument in it":        {osrun.RunHop, "run --property=x"},
		"nothing":                               {osrun.RunHop, ""},
		"the check as the first of a rehearsal": {osrun.RunRehearsal, osrun.PhaseVerify},
	} {
		t.Run(name, func(t *testing.T) {
			box := newUpgradeBox(t)
			run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), c.kind)
			if err := box.launchAttempt(run.ID, c.phase, osrun.WhoOwner); launchKind(t, err) != launchNotAllowed {
				t.Errorf("err = %v", err)
			}
			if n := len(box.startedUnits()); n != 0 {
				t.Errorf("%d units were started", n)
			}
			if got := box.read(run.ID); len(got.Markers) != len(run.Markers) || got.Ignored != 0 {
				t.Errorf("the log was written: %d lines (%d), it had %d", len(got.Markers), got.Ignored, len(run.Markers))
			}
		})
	}
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	for _, who := range []string{"", "root", "AUTO"} {
		if err := box.launchAttempt(run.ID, osrun.PhaseRun, who); launchKind(t, err) != launchNotAllowed {
			t.Errorf("who %q: err = %v", who, err)
		}
	}
	if err := box.launchAttempt("../etc", osrun.PhaseRun, osrun.WhoOwner); err == nil {
		t.Error("a run that is not an id is launched")
	}
	if err := box.launchAttempt("ffffffff-6c1e-4d0a-9a53-7f1c2d4e8b19", osrun.PhaseRun, osrun.WhoOwner); err == nil {
		t.Error("a run that is not there is launched")
	}
}

func TestNothingIsStartedForARunTheOwnerClosedOrTookOver(t *testing.T) {
	for name, marker := range map[string]osrun.Marker{
		"taken over": {Kind: osrun.MkTakeover},
		"closed":     {Kind: osrun.MkAck, Result: "acknowledge"},
	} {
		t.Run(name, func(t *testing.T) {
			box := newUpgradeBox(t)
			run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
			box.say(run, marker)
			if err := box.launchAttempt(run.ID, osrun.PhaseRun, osrun.WhoAuto); launchKind(t, err) != launchNotAllowed {
				t.Errorf("err = %v", err)
			}
			if n := len(box.startedUnits()); n != 0 {
				t.Errorf("%d units were started", n)
			}
		})
	}
}

func TestAnAttemptWithNoBlockToRunIsNotStarted(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	run = box.say(run, osrun.Marker{Kind: osrun.MkAttempt, N: 1, Phase: osrun.PhaseRun, Who: osrun.WhoOwner},
		osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRun, Boot: boxBoot1, PID: 77},
		osrun.Marker{Kind: osrun.MkStop, N: 1, Index: 7, ID: osrun.BlockPrereboot, RC: 1, Class: osrun.ClassNetwork, PNR: true},
		osrun.Marker{Kind: osrun.MkUnitEnd, N: 1, Result: "stopped"})
	run = box.leave(run, "begun-upgrade", "done-minimal", "done-full", "done-prereboot")
	box.advance(time.Hour)
	if err := box.launchAttempt(run.ID, osrun.PhaseRecover, osrun.WhoOwner); launchKind(t, err) != launchNothing {
		t.Errorf("err = %v: every block of the hop is done", err)
	}
	if n := len(box.startedUnits()); n != 0 {
		t.Errorf("%d units were started", n)
	}
}

// A folder that is not what the core wrote runs nothing, and the run needs attention: the log says so.
func TestAFolderThatIsNotWhatWasWrittenStartsNothing(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	appendTo(t, filepath.Join(run.Dir, "blocks", "minimal.sh"), "reboot\n")

	if err := box.launchAttempt(run.ID, osrun.PhaseRun, osrun.WhoOwner); launchKind(t, err) != launchDamaged {
		t.Fatalf("err = %v", err)
	}
	if n := len(box.startedUnits()); n != 0 {
		t.Fatalf("%d units were started from a folder that does not pass its sums", n)
	}
	if _, err := os.Stat(filepath.Join(run.Dir, "lists", "1.list")); err == nil {
		t.Error("a list was written for an attempt that was refused")
	}
	run = box.read(run.ID)
	if tail := run.Markers[len(run.Markers)-2:]; tail[0].Kind != osrun.MkAttempt || tail[1].Kind != osrun.MkStartFailed || tail[1].N != 1 {
		t.Errorf("the log ends with %+v", tail)
	}
	if st := box.state(run); st.Name != osrun.StateNeedsAttention || st.Reason != osrun.ReasonRunDamaged || !st.Busy {
		t.Errorf("the run is %+v", st)
	}
}

func TestAUnitThatSystemdDidNotStartIsSaidInTheLog(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	box.startErr = errors.New("exit status 1")
	err := box.launchAttempt(run.ID, osrun.PhaseRun, osrun.WhoOwner)
	if launchKind(t, err) != launchStartFailed {
		t.Fatalf("err = %v", err)
	}
	run = box.read(run.ID)
	if tail := run.Markers[len(run.Markers)-2:]; tail[0].Kind != osrun.MkAttempt || tail[1].Kind != osrun.MkStartFailed || tail[1].N != 1 {
		t.Errorf("the log ends with %+v", tail)
	}
	st := box.state(run)
	if st.Name != osrun.StateFailed || st.Reason != osrun.ReasonStart || st.Busy || !st.Reversible {
		t.Errorf("the run is %+v: nothing began, and it can be started again", st)
	}
	// the next attempt is number 2
	box.startErr = nil
	box.advance(time.Minute)
	if err := box.launchAttempt(run.ID, osrun.PhaseRun, osrun.WhoOwner); launchKind(t, err) != launchNotAllowed {
		t.Fatalf("err = %v: a hop is not run twice", err)
	}
}

func TestAMachineThatCannotStartAUnitIsNotWrittenFor(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	box.updater.getEUID = func() int { return 1000 }
	if err := box.launchAttempt(run.ID, osrun.PhaseRun, osrun.WhoOwner); launchKind(t, err) != launchUnsupported {
		t.Fatalf("err = %v", err)
	}
	if got := box.read(run.ID); len(got.Markers) != len(run.Markers) {
		t.Error("the log was written")
	}
}

func TestTheInputOfTheReducerIsWhatTheBoxSays(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	rewrite(t, filepath.Join(run.Dir, "output.log"), "apt says something\n")
	when := box.now().Add(-90 * time.Second)
	if err := os.Chtimes(filepath.Join(run.Dir, "output.log"), when, when); err != nil {
		t.Fatal(err)
	}
	box.setUnit("active")
	box.setBoot(boxBoot2)
	run = box.say(run, osrun.Marker{Kind: osrun.MkBootSeen, Boot: boxBoot2})
	rewrite(t, filepath.Join(run.Dir, "barrier.json"), `{"ok":false,"checks":[{"code":"fstab_unresolved","ok":false,"severity":"refuse"}]}`)
	rewrite(t, filepath.Join(run.Dir, "verify.json"), `{"findings":[{"code":"ports_missing","severity":"warn"},{"code":"docker_source_paused","severity":"info"}]}`)

	in := box.osUpgradeInput(run)
	if !in.Unit.Active || !in.Unit.Answered || in.Boot.Now != boxBoot2 || !in.Boot.OK || !in.Now.Equal(box.now()) || !in.LastActivity.Equal(when) {
		t.Errorf("the input is %+v", in)
	}
	if in.Damaged || in.Ignored != 0 || len(in.Markers) != len(run.Markers) {
		t.Errorf("the input is damaged or has lost lines: %+v", in)
	}
	if in.Barrier == nil || in.Barrier.OK || len(in.Barrier.Checks) != 1 || in.Barrier.Checks[0].Code != "fstab_unresolved" {
		t.Errorf("the barrier is %+v", in.Barrier)
	}
	if in.Verify == nil || in.Verify.Warnings != 1 || in.Verify.Errors != 0 {
		t.Errorf("the verdict is %+v: information is not counted", in.Verify)
	}
	if !in.BootSeenAt.Equal(box.now()) {
		t.Errorf("the boot was seen at %v", in.BootSeenAt)
	}

	// ... and what is not there is not made up
	rewrite(t, filepath.Join(run.Dir, "barrier.json"), "not json")
	os.Remove(filepath.Join(run.Dir, "verify.json"))
	box.setUnit("")
	box.setBoot("")
	in = box.osUpgradeInput(run)
	if in.Barrier != nil || in.Verify != nil || in.Unit.Answered || in.Boot.OK || !in.BootSeenAt.IsZero() {
		t.Errorf("the input is %+v", in)
	}
}

func TestARunWhoseLogIsLongerThanItIsReadIsDamaged(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	appendTo(t, filepath.Join(run.Dir, "markers"), strings.Repeat("x", 2<<20))
	run = box.read(run.ID)
	if !run.TooLarge || !box.osUpgradeInput(run).Damaged {
		t.Errorf("TooLarge = %v: a log that cannot be read is not one to trust", run.TooLarge)
	}
}

func TestTheFilesOfTheBlocksAreReadWhereTheyAre(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	if run.BK != "" || (run.Files != osrun.Files{}) {
		t.Fatalf("a hop that has not begun: %q, %+v", run.BK, run.Files)
	}
	run = box.leave(run, "done-timers", "apt-orig")
	if run.Files != (osrun.Files{DoneTimers: true, AptOrig: true}) || run.BK != filepath.Join(box.backups, "casaos-os-upgrade-"+run.Meta.PlanID) {
		t.Errorf("after the first blocks: %q, %+v", run.BK, run.Files)
	}
	rewrite(t, filepath.Join(run.Dir, "abort"), "")
	run = box.leave(run, "begun-upgrade")
	if !run.Files.Begun || !run.Files.AbortFlag || !run.Files.PNR() {
		t.Errorf("the point of no return and the flag: %+v", run.Files)
	}
	rewrite(t, filepath.Join(run.BK, "boot_id"), boxBoot1+"\nsecond line\n")
	if got := box.read(run.ID).Files.BootIDFile; got != boxBoot1 {
		t.Errorf("the boot id of the file is %q", got)
	}

	// when the unit has put the folder aside, the files are read in it, and not in a folder of the same name that another run made
	aborted := filepath.Join(run.Dir, "bk-aborted")
	if err := os.Rename(run.BK, aborted); err != nil {
		t.Fatal(err)
	}
	other := box.backupOf(run)
	rewrite(t, filepath.Join(other, "done-after"), "")
	got := box.read(run.ID)
	if got.BK != aborted || got.Files.DoneAfter || !got.Files.Begun {
		t.Errorf("the run that was put aside is read as %q, %+v", got.BK, got.Files)
	}
}

// A file that cannot be asked about is taken for one that says the upgrade began, never for one that says it did not.
func TestAFileThatCannotBeAskedAboutIsTakenForThePointOfNoReturn(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	bk := box.backupOf(run)
	// a name that is below a file: the folder says "not a directory", not "no such file"
	rewrite(t, filepath.Join(bk, "begun-upgrade"), "x")
	f := osUpgradeFiles(&osUpgradeRun{Dir: run.Dir, BK: filepath.Join(bk, "begun-upgrade")})
	if !f.Begun || !f.DoneMinimal || f.DoneFull || f.DonePrereboot || f.DoneAfter {
		t.Errorf("the files are %+v", f)
	}
}

func TestARunThatCannotBeReadIsAnError(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	for name, damage := range map[string]func(){
		"no identity": func() { remove(t, filepath.Join(run.Dir, "run.json")) },
		"not json":    func() { rewrite(t, filepath.Join(run.Dir, "run.json"), "x") },
		"another run's": func() {
			rewrite(t, filepath.Join(run.Dir, "run.json"), `{"run_id":"`+strings.Replace(run.ID, "0b8f", "ffff", 1)+`","kind":"hop","hop":"x","bk_path":"/var/backups/casaos-os-upgrade-x"}`)
		},
		"a kind that isn't": func() {
			rewrite(t, filepath.Join(run.Dir, "run.json"), `{"run_id":"`+run.ID+`","kind":"reboot","hop":"x","bk_path":"/var/backups/casaos-os-upgrade-x"}`)
		},
		"a backup folder that climbs": func() {
			rewrite(t, filepath.Join(run.Dir, "run.json"), `{"run_id":"`+run.ID+`","kind":"hop","hop":"x","bk_path":"/var/backups/casaos-os-upgrade-../../etc"}`)
		},
	} {
		saved := readText(t, filepath.Join(run.Dir, "run.json"))
		damage()
		if _, err := readOSUpgradeRun(run.ID); err == nil {
			t.Errorf("%s: the run is read", name)
		}
		rewrite(t, filepath.Join(run.Dir, "run.json"), saved)
	}
}

// What the settling writes, the status and the reducer read back; and a facts file that is lost is the zero value.
func TestTheResultsOfTheSettlingAreWrittenWholeAndReadBack(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	if err := writeOSUpgradeBarrier(run, osUpgradeBarrierDoc{OK: false, Checks: []OSUpgradeBarrierItem{{Code: "fstab_unresolved", OK: false, Severity: "refuse"}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeOSUpgradeVerify(run, osUpgradeVerifyDoc{At: box.now(), Findings: []osverify.Finding{{Code: osverify.CodePortsMissing, Severity: osverify.SeverityWarn, Arg: "445"}}}); err != nil {
		t.Fatal(err)
	}
	in := box.osUpgradeInput(run)
	if in.Barrier == nil || in.Barrier.OK || len(in.Barrier.Checks) != 1 || in.Verify == nil || in.Verify.Warnings != 1 {
		t.Errorf("the input is %+v, %+v", in.Barrier, in.Verify)
	}
	if runtime.GOOS == "linux" {
		for _, name := range []string{"barrier.json", "verify.json"} {
			if info, err := os.Stat(filepath.Join(run.Dir, name)); err != nil || info.Mode().Perm()&0o077 != 0 {
				t.Errorf("%s is %v, %v", name, info, err)
			}
		}
	}

	facts := readOSUpgradeFacts(run)
	if !facts.Taken || !facts.DpkgQuiet {
		t.Errorf("the facts are %+v", facts)
	}
	for name, damage := range map[string]func(){
		"lost":    func() { remove(t, filepath.Join(run.Dir, "facts-before.json")) },
		"garbage": func() { rewrite(t, filepath.Join(run.Dir, "facts-before.json"), "not json") },
		"a link": func() {
			remove(t, filepath.Join(run.Dir, "facts-before.json"))
			os.Symlink(filepath.Join(run.Dir, "run.json"), filepath.Join(run.Dir, "facts-before.json"))
		},
	} {
		damage()
		if got := readOSUpgradeFacts(run); got.Taken || got.Kernel != "" {
			t.Errorf("%s: the facts are %+v: nothing was taken", name, got)
		}
	}
}

// The ids that the driver accepts are the ids that the core writes lists of, covers with sums and keeps in run.json: one set.
func TestTheBlocksOfTheDriverAreTheBlocksOfTheRun(t *testing.T) {
	_, rest, found := strings.Cut(osrun.Driver, `case "$id" in `)
	if !found {
		t.Fatal("the driver has no case of the ids of the blocks")
	}
	list, _, _ := strings.Cut(rest, ")")
	ids := strings.Split(list, "|")
	slices.Sort(ids)
	var run []string
	for _, kind := range []string{osrun.RunHop, osrun.RunRefresh} {
		run = append(run, osUpgradeBlockIDs(kind)...)
	}
	slices.Sort(run)
	run = slices.Compact(run)
	if !reflect.DeepEqual(ids, run) {
		t.Errorf("the driver accepts %v, the core writes %v", ids, run)
	}
}
