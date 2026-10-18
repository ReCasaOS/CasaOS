package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// nonceOfArgs is the nonce that a systemd-run is given for the unit of a job.
func nonceOfArgs(args []string) string {
	for _, a := range args {
		if nonce, ok := strings.CutPrefix(a, "--setenv=CASAOS_MODULE_NONCE="); ok {
			return nonce
		}
	}
	return ""
}

// ---- systemd-run that reports an error ---------------------------------------------------------------------------------------

// systemd-run can report an error after it has queued the unit: its wait for the bus ends, or the reply is lost. The unit then begins, and the log is its word, not the core's.
func TestAnErrorOfSystemdRunAfterTheUnitWasQueuedLeavesTheLogAsItIs(t *testing.T) {
	const unit = "casaos-module-virt.service"
	for name, tc := range map[string]struct {
		onStart func(b *moduleBox, args []string)
		state   string
	}{
		"the unit ran to its end and is gone": {func(b *moduleBox, args []string) {
			n := nonceOfArgs(args)
			b.unitSays("virt", b.step(n, "virt", "done", "done", 0), b.result(n, "virt", "ok", "done", "-", "-", 0))
		}, modulecmd.JobSucceeded},
		"the unit runs": {func(b *moduleBox, args []string) {
			b.unitSays("virt", b.step(nonceOfArgs(args), "virt", "packages", "begin", 0))
			b.setUnit(unit, "active")
		}, modulecmd.JobRunning},
		"the unit is on its way":             {func(b *moduleBox, _ []string) { b.setUnit(unit, "activating") }, modulecmd.JobRunning},
		"the unit is loaded and not running": {func(b *moduleBox, _ []string) { b.setUnit(unit, "inactive") }, modulecmd.JobRunning},
		"the unit holds the lock of the assistant and systemd does not show it yet": {func(b *moduleBox, _ []string) {
			release, err := b.env.Host.Lock(b.env.Roots.Path("/var/lib/casaos/modules/.lock"))
			if err != nil {
				b.t.Error(err)
				return
			}
			b.t.Cleanup(release)
		}, modulecmd.JobRunning},
		"systemd does not answer": {func(b *moduleBox, _ []string) {
			b.mu.Lock()
			b.silent = true
			b.mu.Unlock()
		}, modulecmd.JobRunning},
	} {
		for _, verb := range []string{"install", "remove"} {
			t.Run(name+", "+verb, func(t *testing.T) {
				b := newModuleBox(t)
				plan := b.planned("virt")
				if verb == "remove" {
					b.install("virt")
				}
				b.startErr = errors.New("signal: killed")
				b.startOut = "Failed to start transient service unit: Connection timed out\n"
				b.onStart = func(args []string) { tc.onStart(b, args) }
				var status SystemModuleStatus
				var err error
				if verb == "install" {
					status, err = b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
				} else {
					status, err = b.system.StartSystemModuleRemove(context.Background(), "virt")
				}
				if err != nil {
					t.Fatalf("the unit may be there, and the click is told so: %v", err)
				}
				if status.Job.State != tc.state || status.Job.Name != "virt" {
					t.Errorf("the job: %+v, want %s", status.Job, tc.state)
				}
				log := b.read("/var/log/casaos/module-virt.log")
				if strings.Contains(log, "start_failed") || strings.Contains(log, "failed:") {
					t.Errorf("the core wrote over the log: %q", log)
				}
				if len(b.startedUnits()) != 1 {
					t.Errorf("%d units were started", len(b.startedUnits()))
				}
			})
		}
	}
}

// When the unit that systemd-run did not report is the one that wrote the log, what the log says is what the click is told, and the job is the unit's.
func TestAnAbandonedLogIsNeverReplacedByALogThatTheUnitWrote(t *testing.T) {
	b := newModuleBox(t)
	plan := b.planned("virt")
	b.startErr = errors.New("signal: killed")
	var nonce string
	b.onStart = func(args []string) {
		nonce = nonceOfArgs(args)
		b.unitSays("virt", b.step(nonce, "virt", "packages", "begin", 0), b.step(nonce, "virt", "packages", "done", 0), b.result(nonce, "virt", "ok", "done", "-", "-", 0))
	}
	if _, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID); err != nil {
		t.Fatal(err)
	}
	st, err := b.system.GetSystemModuleStatus("virt")
	if err != nil || st.Job.State != modulecmd.JobSucceeded || st.Job.Code != "" {
		t.Errorf("%+v, %v", st.Job, err)
	}
}

// ---- the gate fails closed ----------------------------------------------------------------------------------------------------

// A job whose log has been quiet for minutes while systemd does not answer is a job that may be running: the status says so, and so does the gate.
func TestASilentSystemdKeepsAJobAwayWhoseLogHasGoneQuiet(t *testing.T) {
	for name, tc := range map[string]struct {
		module string // whose log it is
		age    time.Duration
		lines  func(b *moduleBox, n string) []string
		want   string // the state the status reads
	}{
		"another module's job, five minutes quiet": {"tool", 5 * time.Minute, func(b *moduleBox, n string) []string {
			return []string{b.queuedLine("tool", n, 0), b.step(n, "tool", "packages", "begin", 0)}
		}, modulecmd.JobRunning},
		"this module's job, five minutes quiet": {"virt", 5 * time.Minute, func(b *moduleBox, n string) []string {
			return []string{b.queuedLine("virt", n, 0), b.step(n, "virt", "packages", "begin", 0)}
		}, modulecmd.JobRunning},
		"a job that has been quiet for longer than systemd is given to be silent": {"tool", 45 * time.Minute, func(b *moduleBox, n string) []string {
			return []string{b.queuedLine("tool", n, 0), b.step(n, "tool", "packages", "begin", 0)}
		}, modulecmd.JobInterrupted},
	} {
		for _, verb := range []string{"install", "remove"} {
			t.Run(name+", "+verb, func(t *testing.T) {
				b := newModuleBox(t)
				b.setCatalogue("virt", "tool")
				plan := b.planned("virt")
				if verb == "remove" {
					b.install("virt")
				}
				b.unitSays(tc.module, tc.lines(b, unitSightNonce)...)
				b.advance(tc.age)
				b.mu.Lock()
				b.silent = true
				b.mu.Unlock()
				if st, err := b.system.GetSystemModuleStatus(tc.module); err != nil || st.Job.State != tc.want {
					t.Fatalf("the status reads %+v, %v; want %s", st.Job, err, tc.want)
				}
				before := b.tree()
				var err error
				if verb == "install" {
					_, err = b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
				} else {
					_, err = b.system.StartSystemModuleRemove(context.Background(), "virt")
				}
				refusal := moduleRefusalOf(t, err)
				if reason, _ := refusal.Data["reason"].(string); refusal.Code != ModuleBusy || !strings.Contains(reason, tc.module) {
					t.Errorf("refused with %+v", refusal)
				}
				if len(b.startedUnits()) != 0 {
					t.Errorf("a unit was started: %q", b.startedUnits())
				}
				b.assertNothingWritten(before)
			})
		}
	}
}

// What systemd's silence does not hide is a job that ended: its log says so, whatever systemd says, and it keeps nothing away.
func TestASilentSystemdDoesNotKeepAwayWhatTheLogSaysIsOver(t *testing.T) {
	b := newModuleBox(t)
	b.setCatalogue("virt", "tool")
	plan := b.planned("virt")
	n := unitSightNonce
	b.unitSays("tool", b.queuedLine("tool", n, 0), b.step(n, "tool", "done", "done", 0), b.result(n, "tool", "ok", "done", "-", "-", 0))
	b.unitSays("hand", "CASAOS_MODULE_STEP - hand packages begin "+b.stamp(0))
	b.advance(2 * time.Hour)
	// the silence begins when the start has looked once (the plan is made) and the gate looks again: systemd answers the first look, and not the second
	var asked atomic.Int64
	b.updater.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 1 && args[1] == "casaos-module-tool.service" {
			asked.Add(1)
			return nil, os.ErrDeadlineExceeded
		}
		return b.command(ctx, name, args...)
	}
	if _, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID); err != nil {
		t.Fatalf("a job that ended keeps a start away because systemd is silent: %v", err)
	}
	if asked.Load() == 0 {
		t.Error("the gate did not ask systemd about the unit of the job whose log it read")
	}
}

// ---- the second look has a time of its own -----------------------------------------------------------------------------------

func TestTheSecondLookAfterThePlanHasATimeOfItsOwn(t *testing.T) {
	original := systemModuleStartTimeout
	systemModuleStartTimeout = 150 * time.Millisecond
	t.Cleanup(func() { systemModuleStartTimeout = original })
	b := newModuleBox(t)
	// how many questions the plan puts to the assistant's runner
	var calls atomic.Int64
	inner := b.env.Runner
	b.env.Runner = hookRunner{inner: inner, calls: &calls, fn: func() {}}
	plan := b.planned("virt")
	last := calls.Load()
	// the plan outlives the time that a start has for its questions, and a package update starts during its last one
	calls.Store(0)
	b.env.Runner = hookRunner{inner: inner, calls: &calls, at: last, fn: func() {
		time.Sleep(3 * systemModuleStartTimeout)
		b.setUnit(systemPackageUpdateUnit, "active")
	}}
	before := b.tree()
	_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
	refusal := moduleRefusalOf(t, err)
	if reason, _ := refusal.Data["reason"].(string); refusal.Code != ModuleBusy || !strings.Contains(reason, systemPackageUpdateUnitName()) {
		t.Fatalf("refused with %+v, %v", refusal, refusal.Data)
	}
	if len(b.startedUnits()) != 0 {
		t.Error("a unit was started")
	}
	b.assertNothingWritten(before)
}

func systemPackageUpdateUnitName() string {
	return strings.TrimSuffix(systemPackageUpdateUnit, ".service")
}

// ---- the program of the unit ----------------------------------------------------------------------------------------------------

func TestAJobIsNotStartedWhenTheProgramOfTheUnitIsNotThere(t *testing.T) {
	for name, damage := range map[string]func(b *moduleBox){
		"it is gone":           func(b *moduleBox) { mustDo(t, os.Remove(b.binary)) },
		"it is not executable": func(b *moduleBox) { mustDo(t, os.Chmod(b.binary, 0o644)) },
		"it is a folder": func(b *moduleBox) {
			mustDo(t, os.Remove(b.binary))
			mustDo(t, os.Mkdir(b.binary, 0o755))
		},
		"it is a link to nothing": func(b *moduleBox) {
			mustDo(t, os.Remove(b.binary))
			mustDo(t, os.Symlink(filepath.Join(filepath.Dir(b.binary), "nothing"), b.binary))
		},
	} {
		for _, verb := range []string{"install", "remove"} {
			t.Run(name+", "+verb, func(t *testing.T) {
				b := newModuleBox(t)
				plan := b.planned("virt")
				if verb == "remove" {
					b.install("virt")
				}
				damage(b)
				before := b.tree()
				var err error
				if verb == "install" {
					_, err = b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
				} else {
					_, err = b.system.StartSystemModuleRemove(context.Background(), "virt")
				}
				refusal := moduleRefusalOf(t, err)
				if refusal.Code != ModuleStartFailed || !strings.Contains(refusal.Detail, systemModuleBinary) {
					t.Errorf("refused with %+v", refusal)
				}
				if len(b.startedUnits()) != 0 {
					t.Error("a unit was started")
				}
				b.assertNothingWritten(before)
			})
		}
	}
	// a link to the program that is there is the program
	b := newModuleBox(t)
	link := filepath.Join(t.TempDir(), "casaos-link")
	if err := os.Symlink(b.binary, link); err != nil {
		t.Skip("a link cannot be made here")
	}
	b.binary = link
	if _, err := b.system.StartSystemModuleInstall(context.Background(), "virt", b.planned("virt").PlanID); err != nil {
		t.Fatal(err)
	}
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// ---- the starts of the other changes ------------------------------------------------------------------------------------------

// The start of a package update and the start of an update of Docker check the box and start their unit holding the lock of the updater: a start of a job of a module does the same, so that
// none of them checks while another is between its check and its unit.
func TestAStartOfAModuleChecksAndLaunchesHoldingTheLockOfTheUpdater(t *testing.T) {
	for _, verb := range []string{"install", "remove"} {
		t.Run(verb, func(t *testing.T) {
			b := newModuleBox(t)
			plan := b.planned("virt")
			if verb == "remove" {
				b.install("virt")
			}
			b.updater.mu.Lock()
			done := make(chan error, 1)
			go func() {
				var err error
				if verb == "install" {
					_, err = b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
				} else {
					_, err = b.system.StartSystemModuleRemove(context.Background(), "virt")
				}
				done <- err
			}()
			select {
			case err := <-done:
				b.updater.mu.Unlock()
				t.Fatalf("the start went on while the lock of the updater was held: %v", err)
			case <-time.After(300 * time.Millisecond):
			}
			if len(b.startedUnits()) != 0 || b.exists("/var/log/casaos/module-virt.log") {
				t.Error("a unit was started or a log was written while the lock of the updater was held")
			}
			b.updater.mu.Unlock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the start did not go on when the lock was let go")
			}
			if len(b.startedUnits()) != 1 {
				t.Errorf("%d units were started", len(b.startedUnits()))
			}
		})
	}
}

// The lock of the updater is let go of before the answer is made, and the other way round the starts do not wait for each other in a circle.
func TestTheStartsOfTheModulesAndOfTheUpdatesDoNotWaitForEachOtherInACircle(t *testing.T) {
	b := newModuleBox(t)
	b.setCatalogue("virt", "tool")
	plans := map[string]string{"virt": b.planned("virt").PlanID, "tool": b.planned("tool").PlanID}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			_, _ = b.system.StartSystemModuleInstall(context.Background(), "virt", plans["virt"])
		}()
		go func() {
			defer wg.Done()
			_ = b.system.GetSystemPackageUpdateStatus()
		}()
		go func() {
			defer wg.Done()
			_, _ = b.system.GetSystemModuleStatus("tool")
		}()
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("the starts wait for each other")
	}
	if len(b.startedUnits()) != 1 {
		t.Errorf("%d units were started", len(b.startedUnits()))
	}
}

// A start waits for the start before it no longer than the request that made it: a plan that hangs does not queue every click for the time of a plan.
func TestAStartWaitsForTheStartsBeforeItNoLongerThanItsRequest(t *testing.T) {
	original := systemModulePlanTimeout
	systemModulePlanTimeout = 2 * time.Second
	t.Cleanup(func() { systemModulePlanTimeout = original })
	b := newModuleBox(t)
	plan := b.planned("virt")
	b.mu.Lock()
	b.hang = true
	b.mu.Unlock()
	first := make(chan error, 1)
	go func() {
		_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
		first <- err
	}()
	// (the first start has the turn, and its plan hangs)
	time.Sleep(300 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	begun := time.Now()
	_, err := b.system.StartSystemModuleInstall(ctx, "virt", plan.PlanID)
	waited := time.Since(begun)
	refusal := moduleRefusalOf(t, err)
	if refusal.Code != ModuleBusy || refusal.Data["reason"] == nil || refusal.Data["reason"] == "" {
		t.Errorf("refused with %+v", refusal)
	}
	if waited > time.Second {
		t.Errorf("the second start waited %v with a request of 150 ms", waited)
	}
	select {
	case err := <-first:
		t.Fatalf("the first start ended before the second gave up: %v", err)
	default:
	}
	// a request that is over starts nothing, whoever has the turn
	if err := <-first; moduleRefusalOf(t, err).Code != ModulePlanTimeout {
		t.Errorf("the first start: %v", err)
	}
	b.mu.Lock()
	b.hang = false
	b.mu.Unlock()
	over, cancelOver := context.WithCancel(context.Background())
	cancelOver()
	before := b.tree()
	for i := 0; i < 20; i++ { // (a turn that is free and a request that is over are both ready: the choice must not be left to chance)
		if _, err := b.system.StartSystemModuleInstall(over, "virt", plan.PlanID); moduleRefusalOf(t, err).Code != ModuleBusy {
			t.Fatalf("a request that is over: %v", err)
		}
		if _, err := b.system.StartSystemModuleRemove(over, "virt"); moduleRefusalOf(t, err).Code != ModuleBusy {
			t.Fatalf("a request that is over: %v", err)
		}
	}
	if len(b.startedUnits()) != 0 {
		t.Error("a unit was started")
	}
	b.assertNothingWritten(before)
}

// Every busy says what it is about: a plan that is refused because the box is busy as well. The plan sees what the gate did not (a package manager that took the lock of dpkg meanwhile).
func TestAPlanThatIsRefusedBecauseTheBoxIsBusySaysWhatWith(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(b *moduleBox)
		want  string
	}{
		"a package manager": {func(b *moduleBox) { b.dpkg = true }, "package manager"},
		"a lock that cannot be looked at": {func(b *moduleBox) {
			b.dpkgErr = os.ErrPermission
		}, "could not be seen"},
		"the upgrade of the operating system": {func(b *moduleBox) { b.write("/var/lib/casaos/os-upgrade/busy", "") }, "upgrade of the operating system"},
	} {
		t.Run(name, func(t *testing.T) {
			b := newModuleBox(t)
			plan := b.planned("virt")
			// the gate of the core does not see it, and the plan does
			b.updater.dpkgLockState = func() (bool, error) { return false, nil }
			tc.setup(b)
			before := b.tree()
			_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
			refusal := moduleRefusalOf(t, err)
			reason, _ := refusal.Data["reason"].(string)
			if refusal.Code != ModuleBusy || !strings.Contains(reason, tc.want) {
				t.Errorf("refused with %+v, %v", refusal, refusal.Data)
			}
			if _, ok := refusal.Data["checks"].([]osmodule.Check); !ok {
				t.Errorf("the checks are not in the data: %v", refusal.Data)
			}
			if len(b.startedUnits()) != 0 {
				t.Error("a unit was started")
			}
			b.assertNothingWritten(before)
		})
	}
}

// ---- what a status says ----------------------------------------------------------------------------------------------------------

// The nonce guards the log of the job: it is in no answer of the core, the answer of a start and the statuses that follow included.
func TestNoAnswerOfTheModulesHasTheNonceOfTheJob(t *testing.T) {
	b := newModuleBox(t)
	plan := b.planned("virt")
	started, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	nonce := b.nonceOfLog("virt")
	answers := map[string]SystemModuleStatus{"the answer of the start": started}
	b.setUnit("casaos-module-virt.service", "active")
	b.unitSays("virt", b.step(nonce, "virt", "packages", "begin", 0), "| Unpacking libvirt", b.step(nonce, "virt", "packages", "done", 0), b.step(nonce, "virt", "download", "begin", 0))
	running, err := b.system.GetSystemModuleStatus("virt")
	if err != nil {
		t.Fatal(err)
	}
	answers["a status while it runs"] = running
	b.unitSays("virt", b.step(nonce, "virt", "route", "failed", 0), "failed at step route: route_timeout", "kept_packages: none", b.result(nonce, "virt", "failed", "route", "route_timeout", "rolled_back", 0))
	b.setUnit("casaos-module-virt.service", "inactive")
	failed, err := b.system.GetSystemModuleStatus("virt")
	if err != nil {
		t.Fatal(err)
	}
	answers["a status when it failed"] = failed
	for name, answer := range answers {
		raw, err := json.Marshal(answer)
		if err != nil || strings.Contains(string(raw), nonce) || strings.Contains(string(raw), "CASAOS_MODULE_") {
			t.Errorf("%s: %s, %v", name, raw, err)
		}
	}
	if got := running.Job.LogTail; len(got) != 1 || got[0] != "| Unpacking libvirt" {
		t.Errorf("the lines of the tools are kept: %q", got)
	}
	if got := failed.Job.LogTail; len(got) != 2 || got[0] != "failed at step route: route_timeout" {
		t.Errorf("the sentences of the assistant are kept: %q", got)
	}
}

// ---- the log of a unit that has not said a word -----------------------------------------------------------------------------------

// The unit truncates the log when it starts and says its first marker when its plan is made, which takes up to a minute: a start in that time finds the log empty, and it is a job.
func TestAnEmptyLogIsAJobThatIsStartingAndKeepsAnotherAway(t *testing.T) {
	for name, tc := range map[string]struct {
		age       time.Duration // how long ago the log was emptied
		unit      string
		silent    bool
		want      string // the state of the job
		keepsAway bool
	}{
		"a moment ago, the unit is not seen":      {5 * time.Second, "", false, modulecmd.JobRunning, true},
		"a moment ago, the unit runs":             {5 * time.Second, "active", false, modulecmd.JobRunning, true},
		"a moment ago, systemd does not answer":   {5 * time.Second, "", true, modulecmd.JobRunning, true},
		"a minute ago, the unit runs":             {time.Minute, "active", false, modulecmd.JobRunning, true},
		"a minute ago, the unit is gone":          {time.Minute, "", false, modulecmd.JobInterrupted, false},
		"an hour ago, the unit is gone":           {time.Hour, "", false, modulecmd.JobInterrupted, false},
		"20 minutes ago, systemd does not answer": {20 * time.Minute, "", true, modulecmd.JobRunning, true},
		"an hour ago, systemd does not answer":    {time.Hour, "", true, modulecmd.JobInterrupted, true},
	} {
		t.Run(name, func(t *testing.T) {
			b := newModuleBox(t)
			b.setCatalogue("virt", "tool")
			plan := b.planned("virt")
			b.write("/var/log/casaos/module-tool.log", "")
			b.touchLog("tool", -tc.age)
			if tc.unit != "" {
				b.setUnit("casaos-module-tool.service", tc.unit)
			}
			b.mu.Lock()
			b.silent = tc.silent
			b.mu.Unlock()
			if st, err := b.system.GetSystemModuleStatus("tool"); err != nil || st.Job.State != tc.want {
				t.Fatalf("the status reads %+v, %v; want %s", st.Job, err, tc.want)
			}
			_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
			if tc.keepsAway {
				if refusal := moduleRefusalOf(t, err); refusal.Code != ModuleBusy {
					t.Errorf("refused with %+v", refusal)
				}
				if len(b.startedUnits()) != 0 {
					t.Error("a unit was started")
				}
			} else if err != nil {
				t.Errorf("a log that nothing is writing keeps a start away: %v", err)
			}
		})
	}
}

// A box that is busy is told so at the first look: the plan, which takes the time of apt, is not made for a start that is refused anyway.
func TestABusyBoxIsToldSoBeforeThePlanIsMade(t *testing.T) {
	b := newModuleBox(t)
	plan := b.planned("virt")
	var calls atomic.Int64
	b.env.Runner = hookRunner{inner: b.env.Runner, calls: &calls, fn: func() {}}
	b.setUnit(systemPackageUpdateUnit, "active")
	_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
	if refusal := moduleRefusalOf(t, err); refusal.Code != ModuleBusy {
		t.Fatalf("refused with %+v", refusal)
	}
	if calls.Load() != 0 {
		t.Errorf("the plan asked the assistant's runner %d times for a start that the first look refused", calls.Load())
	}
}
