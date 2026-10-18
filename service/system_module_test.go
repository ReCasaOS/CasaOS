package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

const unitSightNonce = "cccccccccccccccccccccccccccccccc"

func moduleRefusalOf(t *testing.T, err error) *ModuleRefusal {
	t.Helper()
	var refusal *ModuleRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("the error is %v, not a refusal of a module", err)
	}
	return refusal
}

// planned is the plan of installing a module, which has to be a plan that is not refused.
func (b *moduleBox) planned(name string) osmodule.PlanResult {
	b.t.Helper()
	plan, err := b.system.PlanSystemModule(name)
	if err != nil {
		b.t.Fatal(err)
	}
	if plan.Refused != "" || !modulePlanIDPattern.MatchString(plan.PlanID) {
		b.t.Fatalf("the plan is refused or has no id: %+v", plan)
	}
	return plan
}

// queuedLine is the first line of the log of an install that was written by the core at a time of the box.
func (b *moduleBox) queuedLine(name, nonce string, d time.Duration) string {
	return b.queuedLineOf(name, nonce, modulecmd.KindInstall, d)
}

// queuedLineOf is the first line of the log of a job of a kind.
func (b *moduleBox) queuedLineOf(name, nonce, kind string, d time.Duration) string {
	return "CASAOS_MODULE_QUEUED " + nonce + " " + name + " " + kind + " " + b.stamp(d)
}

// ---- the start --------------------------------------------------------------------------------------------------------------

func TestAnInstallStartsTheUnitOfTheModuleForThePlanThatWasRead(t *testing.T) {
	b := newModuleBox(t)
	plan := b.planned("virt")
	b.advance(time.Minute)
	status, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Job.State != modulecmd.JobRunning || status.Job.Name != "virt" || status.Job.StartedAt != b.stamp(0) || status.Job.Step != "" {
		t.Errorf("the job: %+v", status.Job)
	}
	if status.Status.Name != "virt" || status.Status.Installed {
		t.Errorf("the status: %+v", status.Status)
	}
	started := b.startedUnits()
	if len(started) != 1 {
		t.Fatalf("%d units were started: %q", len(started), started)
	}
	nonce := b.nonceOfLog("virt")
	if !dockerpkg.ValidNonce(nonce) {
		t.Fatalf("the nonce %q", nonce)
	}
	want := []string{
		"/usr/bin/systemd-run", "casaos-module-virt.service",
		"--quiet", "--no-block", "--collect", "--unit=casaos-module-virt.service", "--property=Type=exec", "--property=KillMode=mixed", "--property=TimeoutStopSec=420",
		"--setenv=CASAOS_MODULE_NONCE=" + nonce,
		"--", "/usr/bin/casaos", "module", "install", "virt", "--yes", "--plan-id", plan.PlanID,
	}
	if !reflect.DeepEqual(started[0], want) {
		t.Errorf("got  %q\nwant %q", started[0], want)
	}
	// the log is the record: its first line is the job's, and nothing else was written but the lock of the assistant and the folders
	if got, want := b.read("/var/log/casaos/module-virt.log"), modulecmd.QueuedLine("virt", nonce, "install", b.now()); got != want {
		t.Errorf("the log is %q, want %q", got, want)
	}
	for p, v := range b.tree() {
		switch filepath.ToSlash(p) {
		case "etc/os-release", "usr/share/casaos/modules/catalogue.json", "var/log/casaos/module-virt.log":
		default:
			if !strings.HasSuffix(p, "/") && !strings.HasSuffix(p, ".lock") {
				t.Errorf("the start wrote %s (%q)", p, v)
			}
		}
	}
}

func TestARemoveStartsTheUnitOfTheModule(t *testing.T) {
	b := newModuleBox(t)
	b.install("virt")
	status, err := b.system.StartSystemModuleRemove(context.Background(), "virt")
	if err != nil {
		t.Fatal(err)
	}
	if status.Job.State != modulecmd.JobRunning || !status.Status.Installed {
		t.Errorf("%+v", status)
	}
	nonce := b.nonceOfLog("virt")
	want := []string{
		"/usr/bin/systemd-run", "casaos-module-virt.service",
		"--quiet", "--no-block", "--collect", "--unit=casaos-module-virt.service", "--property=Type=exec", "--property=KillMode=mixed", "--property=TimeoutStopSec=420",
		"--setenv=CASAOS_MODULE_NONCE=" + nonce,
		"--", "/usr/bin/casaos", "module", "remove", "virt", "--yes",
	}
	if started := b.startedUnits(); len(started) != 1 || !reflect.DeepEqual(started[0], want) {
		t.Errorf("started %q\nwant    %q", started, want)
	}
}

func TestEveryJobHasANonceOfItsOwn(t *testing.T) {
	b := newModuleBox(t)
	seen := map[string]bool{}
	for i := 0; i < 30; i++ {
		plan := b.planned("virt")
		if _, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID); err != nil {
			t.Fatal(err)
		}
		nonce := b.nonceOfLog("virt")
		if !dockerpkg.ValidNonce(nonce) || seen[nonce] {
			t.Fatalf("the nonce %q is not valid or came before", nonce)
		}
		seen[nonce] = true
		// the job ends, as the unit says
		b.unitSays("virt", b.step(nonce, "virt", "done", "done", 0), b.result(nonce, "virt", "ok", "done", "-", "-", 0))
	}
}

func TestTwoStartsAtOnceAreOneJob(t *testing.T) {
	installVirt := func(b *moduleBox, plans map[string]string) error {
		_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plans["virt"])
		return err
	}
	for name, tc := range map[string]struct {
		installed bool // tool is on the box
		run       func(b *moduleBox, plans map[string]string, i int) error
	}{
		"the same module": {false, func(b *moduleBox, plans map[string]string, _ int) error { return installVirt(b, plans) }},
		"two modules": {false, func(b *moduleBox, plans map[string]string, i int) error {
			name := []string{"virt", "tool"}[i%2]
			_, err := b.system.StartSystemModuleInstall(context.Background(), name, plans[name])
			return err
		}},
		"an install and a remove": {true, func(b *moduleBox, plans map[string]string, i int) error {
			if i%2 == 0 {
				return installVirt(b, plans)
			}
			_, err := b.system.StartSystemModuleRemove(context.Background(), "tool")
			return err
		}},
	} {
		t.Run(name, func(t *testing.T) {
			b := newModuleBox(t)
			b.setCatalogue("virt", "tool")
			plans := map[string]string{"virt": b.planned("virt").PlanID, "tool": b.planned("tool").PlanID}
			if tc.installed {
				b.install("tool")
			}
			var wg sync.WaitGroup
			errs := make([]error, 16)
			start := make(chan struct{})
			for i := range errs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					errs[i] = tc.run(b, plans, i)
				}()
			}
			close(start)
			wg.Wait()
			ok := 0
			for _, err := range errs {
				if err == nil {
					ok++
				} else if refusal := moduleRefusalOf(t, err); refusal.Code != ModuleBusy {
					t.Errorf("refused with %s", refusal.Code)
				}
			}
			if ok != 1 || len(b.startedUnits()) != 1 {
				t.Errorf("%d starts were let through and %d units were started", ok, len(b.startedUnits()))
			}
		})
	}
}

func TestAJobIsRefusedWhileTheBoxIsBeingChanged(t *testing.T) {
	cases := map[string]func(b *moduleBox){
		"an upgrade of the operating system holds the machine": func(b *moduleBox) {
			if err := os.WriteFile(b.busyFile, nil, 0o644); err != nil {
				b.t.Fatal(err)
			}
		},
		"the flag of the upgrade cannot be looked at": func(b *moduleBox) {
			// a folder that is a file: anything but "no such file" says nothing of whether the upgrade is open
			if err := os.WriteFile(filepath.Join(b.root, "notadir"), nil, 0o644); err != nil {
				b.t.Fatal(err)
			}
			osUpgradeBusyFile = filepath.Join(b.root, "notadir", "busy")
		},
		"the unit of the upgrade runs":      func(b *moduleBox) { b.setUnit(systemOSUpgradeUnit, "active") },
		"a package update runs":             func(b *moduleBox) { b.setUnit(systemPackageUpdateUnit, "active") },
		"an update of Docker runs":          func(b *moduleBox) { b.setUnit(systemDockerUpdateUnit, "activating") },
		"the update of ReCasaOS runs":       func(b *moduleBox) { b.setUnit(common.UPDATE_UNIT+".service", "active") },
		"the unit of another module runs":   func(b *moduleBox) { b.setUnit("casaos-module-tool.service", "active") },
		"the unit of this module runs":      func(b *moduleBox) { b.setUnit("casaos-module-virt.service", "deactivating") },
		"a package manager holds dpkg":      func(b *moduleBox) { b.dpkg = true },
		"the lock of dpkg cannot be looked": func(b *moduleBox) { b.dpkgErr = os.ErrPermission },
		"the logs of the modules cannot be listed": func(b *moduleBox) {
			// the folder of the logs is a link that leads to itself
			if err := os.MkdirAll(filepath.Dir(b.logPath("x")), 0o755); err != nil {
				b.t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Dir(b.logPath("x"))); err != nil {
				b.t.Fatal(err)
			}
			if err := os.Symlink("casaos", filepath.Dir(b.logPath("x"))); err != nil {
				b.t.Skip("a link cannot be made here")
			}
		},
		"the log of a module cannot be read": func(b *moduleBox) {
			// (a folder where a log should be: what cannot be seen is not taken for a job that is over)
			if err := os.MkdirAll(b.logPath("tool"), 0o755); err != nil {
				b.t.Fatal(err)
			}
		},
		"a job of another module was queued": func(b *moduleBox) {
			b.unitSays("tool", b.queuedLine("tool", unitSightNonce, -5*time.Second))
		},
		"a job of this module was queued": func(b *moduleBox) {
			b.unitSays("virt", b.queuedLine("virt", unitSightNonce, -5*time.Second))
		},
		"a job of another module is going on, and its unit is not there yet": func(b *moduleBox) {
			b.unitSays("tool", b.queuedLine("tool", unitSightNonce, -20*time.Second), b.step(unitSightNonce, "tool", "packages", "begin", -19*time.Second))
		},
	}
	for name, setup := range cases {
		for _, verb := range []string{"install", "remove"} {
			t.Run(name+", "+verb, func(t *testing.T) {
				b := newModuleBox(t)
				b.setCatalogue("virt", "tool")
				plan := b.planned("virt")
				if verb == "remove" {
					b.install("virt")
				}
				setup(b)
				before := b.tree()
				var err error
				if verb == "install" {
					_, err = b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
				} else {
					_, err = b.system.StartSystemModuleRemove(context.Background(), "virt")
				}
				if refusal := moduleRefusalOf(t, err); refusal.Code != ModuleBusy || refusal.Data["reason"] == "" || refusal.Data["reason"] == nil {
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

func TestWhatIsOverDoesNotKeepAJobAway(t *testing.T) {
	b := newModuleBox(t)
	b.setCatalogue("virt", "tool")
	n := strings.Repeat("a", 32)
	// another module's job that ended; one that was interrupted a long time ago; a run by hand; a file that is none of the modules'
	b.unitSays("tool", b.step(n, "tool", "done", "done", -time.Hour), b.result(n, "tool", "ok", "done", "-", "-", -time.Hour))
	b.unitSays("old", b.queuedLine("old", n, -48*time.Hour))
	b.touchLog("old", -48*time.Hour)
	b.unitSays("hand", "CASAOS_MODULE_STEP - hand packages begin "+b.stamp(-time.Second))
	// one that was interrupted ten minutes ago: its unit is gone, and systemd answers
	b.unitSays("stale", b.queuedLine("stale", n, -10*time.Minute), b.step(n, "stale", "packages", "begin", -10*time.Minute))
	b.touchLog("stale", -10*time.Minute)
	b.unitSays("virt", b.queuedLine("virt", n, -time.Hour), b.result(n, "virt", "failed", "-", "start_failed", "-", -time.Hour))
	b.write("/var/log/casaos/casaos.log", b.queuedLine("virt", n, -time.Second)+"\n")
	// units that are not running
	b.setUnit("casaos-module-tool.service", "inactive")
	b.setUnit(systemPackageUpdateUnit, "failed")
	if _, err := b.system.StartSystemModuleInstall(context.Background(), "virt", b.planned("virt").PlanID); err != nil {
		t.Fatal(err)
	}
}

func TestAQueuedJobWhoseUnitNeverCameIsOverAfterThirtySeconds(t *testing.T) {
	b := newModuleBox(t)
	b.install("virt")
	b.unitSays("virt", b.queuedLine("virt", unitSightNonce, 0))
	if _, err := b.system.StartSystemModuleRemove(context.Background(), "virt"); moduleRefusalOf(t, err).Code != ModuleBusy {
		t.Fatalf("a job that was queued a moment ago: %v", err)
	}
	b.advance(modulecmd.InterruptedAfter - time.Second)
	if _, err := b.system.StartSystemModuleRemove(context.Background(), "virt"); moduleRefusalOf(t, err).Code != ModuleBusy {
		t.Fatalf("a job that was queued 29 seconds ago: %v", err)
	}
	b.advance(time.Second)
	if _, err := b.system.StartSystemModuleRemove(context.Background(), "virt"); err != nil {
		t.Fatalf("thirty seconds later: %v", err)
	}
}

func TestAStartLooksAtTheBoxAgainAfterThePlan(t *testing.T) {
	b := newModuleBox(t)
	// how many questions the plan puts to the assistant's runner
	var calls atomic.Int64
	b.env.Runner = hookRunner{inner: b.env.Runner, calls: &calls, fn: func() {}}
	plan := b.planned("virt")
	last := calls.Load()
	if last == 0 {
		t.Fatal("the plan asks the runner nothing")
	}
	// a package update starts with the last of them
	calls.Store(0)
	b.env.Runner = hookRunner{inner: b.env.Runner.(hookRunner).inner, calls: &calls, at: last, fn: func() { b.setUnit(systemPackageUpdateUnit, "active") }}
	before := b.tree()
	_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
	if refusal := moduleRefusalOf(t, err); refusal.Code != ModuleBusy {
		t.Fatalf("%v", err)
	}
	if len(b.startedUnits()) != 0 {
		t.Error("a unit was started")
	}
	b.assertNothingWritten(before)
}

func TestTheClickBindsToThePlanThatWasRead(t *testing.T) {
	b := newModuleBox(t)
	b.setCatalogue("virt", "tool")
	plan, other := b.planned("virt"), b.planned("tool")
	for name, id := range map[string]string{
		"an id that no plan made":         "m1-" + strings.Repeat("0", 32),
		"the id of another module's plan": other.PlanID,
	} {
		before := b.tree()
		_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", id)
		if refusal := moduleRefusalOf(t, err); refusal.Code != ModulePlanChanged {
			t.Errorf("%s: refused with %+v", name, refusal)
		}
		b.assertNothingWritten(before)
	}
	// the catalogue changes between the plan and the click: another archive
	b.entries[0].Assets.AMD64.SHA256 = strings.Repeat("f", 64)
	b.writeCatalogue()
	before := b.tree()
	_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
	if refusal := moduleRefusalOf(t, err); refusal.Code != ModulePlanChanged {
		t.Fatalf("the archive changed: %v", err)
	}
	b.assertNothingWritten(before)
	// another tag
	b.setCatalogue("virt", "tool")
	good := b.planned("virt")
	b.entries[0].Tag = "v0.2.0"
	b.entries[0].Assets.AMD64.URL = strings.Replace(b.entries[0].Assets.AMD64.URL, "v0.1.0", "v0.2.0", -1)
	b.writeCatalogue()
	if _, err := b.system.StartSystemModuleInstall(context.Background(), "virt", good.PlanID); moduleRefusalOf(t, err).Code != ModulePlanChanged {
		t.Fatalf("the tag changed: %v", err)
	}
	// the system changes: the box is another
	b.setCatalogue("virt", "tool")
	b.entries[0].Manifest.OS = []string{"debian:12", "ubuntu:24.04"}
	b.writeCatalogue()
	again := b.planned("virt")
	b.write("/etc/os-release", "ID=ubuntu\nVERSION_ID=\"24.04\"\n")
	if _, err := b.system.StartSystemModuleInstall(context.Background(), "virt", again.PlanID); moduleRefusalOf(t, err).Code != ModulePlanChanged {
		t.Fatalf("the system changed: %v", err)
	}
	if len(b.startedUnits()) != 0 {
		t.Error("a unit was started")
	}
	// and the plan that is made now is good
	if _, err := b.system.StartSystemModuleInstall(context.Background(), "virt", b.planned("virt").PlanID); err != nil {
		t.Fatal(err)
	}
}

func TestAPlanThatIsRefusedRefusesTheStartWithItsOwnCode(t *testing.T) {
	cases := map[string]struct {
		code  string
		setup func(b *moduleBox)
	}{
		"no catalogue":                        {osmodule.CheckCatalogue, func(b *moduleBox) { _ = os.Remove(b.env.Roots.Path("/usr/share/casaos/modules/catalogue.json")) }},
		"another module":                      {osmodule.CheckCatalogue, func(b *moduleBox) { b.setCatalogue("tool") }},
		"a system that is not supported":      {osmodule.CheckOS, func(b *moduleBox) { b.write("/etc/os-release", "ID=debian\nVERSION_ID=\"11\"\n") }},
		"an architecture that has no archive": {osmodule.CheckArch, func(b *moduleBox) { b.env.Arch = "arm64" }},
		"a release that is too old":           {osmodule.CheckReCasaOS, func(b *moduleBox) { b.env.Version = "0.5.1" }},
		"a module that is there":              {osmodule.CheckInstalled, func(b *moduleBox) { b.install("virt") }},
		"a unit of that name that is not the module's": {osmodule.CheckForeignUnit, func(b *moduleBox) {
			b.write("/etc/systemd/system/casaos-virt.service", "[Unit]\n")
		}},
		"no room":                    {osmodule.RefusedDiskShort, func(b *moduleBox) { b.freeBytes = 1 << 20 }},
		"a room that cannot be told": {osmodule.RefusedDiskUnknown, func(b *moduleBox) { b.freeErr = os.ErrPermission }},
		"no apt-get": {osmodule.RefusedAptMissing, func(b *moduleBox) {
			b.entries[0].Manifest.Packages.Common = []string{"qemu-utils"}
			b.writeCatalogue()
		}},
		"an apt that gives no answer": {osmodule.RefusedAptFailed, func(b *moduleBox) {
			b.entries[0].Manifest.Packages.Common = []string{"qemu-utils"}
			b.writeCatalogue()
			b.write("/usr/bin/apt-get", "apt")
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := newModuleBox(t)
			plan := b.planned("virt")
			c.setup(b)
			// the plan says it, as an object
			got, err := b.system.PlanSystemModule("virt")
			if err != nil || string(got.Refused) != c.code {
				t.Fatalf("the plan: %+v, %v", got, err)
			}
			before := b.tree()
			_, err = b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
			refusal := moduleRefusalOf(t, err)
			if refusal.Code != c.code {
				t.Errorf("refused with %q, want %q", refusal.Code, c.code)
			}
			checks, _ := refusal.Data["checks"].([]osmodule.Check)
			if len(checks) == 0 {
				t.Errorf("the checks are not in the data: %v", refusal.Data)
			}
			if len(b.startedUnits()) != 0 {
				t.Error("a unit was started")
			}
			b.assertNothingWritten(before)
		})
	}
}

func TestARequestThatIsNotValidIsRefusedBeforeAnythingIsLookedAt(t *testing.T) {
	b := newModuleBox(t)
	plan := b.planned("virt")
	before, asked := b.tree(), b.commands.Load()
	for _, name := range []string{"", "Virt", "vi rt", "../virt", "virt/x", "-virt", "1virt", "vi$rt", "vi%rt", "virt\n", "virt\x00", strings.Repeat("a", 33), "vïrt", "a.b"} {
		for what, call := range map[string]func() error{
			"plan": func() error { _, err := b.system.PlanSystemModule(name); return err },
			"install": func() error {
				_, err := b.system.StartSystemModuleInstall(context.Background(), name, plan.PlanID)
				return err
			},
			"remove": func() error { _, err := b.system.StartSystemModuleRemove(context.Background(), name); return err },
			"status": func() error { _, err := b.system.GetSystemModuleStatus(name); return err },
		} {
			refusal := moduleRefusalOf(t, call())
			if refusal.Code != ModuleBadRequest || refusal.Data["field"] != "name" {
				t.Errorf("%s of %q: %+v", what, name, refusal)
			}
		}
	}
	for _, id := range []string{"", "m1-", "m2-" + strings.Repeat("0", 32), "m1-" + strings.Repeat("0", 31), "m1-" + strings.Repeat("0", 33), "m1-" + strings.Repeat("A", 32), strings.Repeat("0", 64), plan.PlanID + "\n", " " + plan.PlanID} {
		_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", id)
		if refusal := moduleRefusalOf(t, err); refusal.Code != ModuleBadRequest || refusal.Data["field"] != "plan_id" {
			t.Errorf("the id %q: %+v", id, refusal)
		}
	}
	if b.commands.Load() != asked || len(b.startedUnits()) != 0 {
		t.Errorf("something was asked of systemd (%d commands more) or started", b.commands.Load()-asked)
	}
	b.assertNothingWritten(before)
}

func TestAJobIsStartedOnlyByRootOnASystemThatCanRunIt(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(b *moduleBox)
		code  string
	}{
		"a core that is not root": {func(b *moduleBox) { b.updater.getEUID = func() int { return 1000 } }, ModuleNotRoot},
		"a system that is not Debian": {func(b *moduleBox) {
			b.updater.readOSRelease = func() (map[string]string, error) { return map[string]string{"ID": "arch"}, nil }
		}, ModuleUnsupported},
		"no systemd-run": {func(b *moduleBox) {
			b.updater.lookPath = func(n string) (string, error) {
				if n == "systemd-run" {
					return "", os.ErrNotExist
				}
				return "/usr/bin/" + n, nil
			}
		}, ModuleUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			b := newModuleBox(t)
			plan := b.planned("virt")
			b.install("tool")
			tc.setup(b)
			before := b.tree()
			for what, call := range map[string]func() error{
				"install": func() error {
					_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
					return err
				},
				"remove": func() error { _, err := b.system.StartSystemModuleRemove(context.Background(), "tool"); return err },
			} {
				if refusal := moduleRefusalOf(t, call()); refusal.Code != tc.code {
					t.Errorf("%s: %+v", what, refusal)
				}
			}
			if len(b.startedUnits()) != 0 {
				t.Error("a unit was started")
			}
			b.assertNothingWritten(before)
			// what only reads still answers
			if _, err := b.system.PlanSystemModule("virt"); err != nil {
				t.Errorf("the plan: %v", err)
			}
			if _, err := b.system.ListSystemModules(); err != nil {
				t.Errorf("the list: %v", err)
			}
			if _, err := b.system.GetSystemModuleStatus("virt"); err != nil {
				t.Errorf("the status: %v", err)
			}
		})
	}
}

func TestARemoveOfWhatIsNotThereIsRefused(t *testing.T) {
	b := newModuleBox(t)
	before := b.tree()
	_, err := b.system.StartSystemModuleRemove(context.Background(), "virt")
	if refusal := moduleRefusalOf(t, err); refusal.Code != ModuleNotInstalled {
		t.Fatalf("%+v", refusal)
	}
	b.assertNothingWritten(before)
	// what an install that was rolled back half way leaves is something to remove
	b.write("/var/lib/casaos/modules/virt.files", "casaos-module-files 1\n")
	if _, err := b.system.StartSystemModuleRemove(context.Background(), "virt"); err != nil {
		t.Fatalf("a module that left its list: %v", err)
	}
	// and a module that the catalogue no longer has is removed all the same
	b.setCatalogue("tool")
	b.install("virt")
	b.unitSays("virt", b.queuedLine("virt", unitSightNonce, -time.Hour), b.result(unitSightNonce, "virt", "ok", "done", "-", "-", -time.Hour))
	if _, err := b.system.StartSystemModuleRemove(context.Background(), "virt"); err != nil {
		t.Fatalf("a module that is not in the catalogue: %v", err)
	}
}

// ---- a unit that does not start, and a lock that is held -------------------------------------------------------------

func TestAUnitThatSystemdWillNotStartLeavesAWholeRunInTheLog(t *testing.T) {
	b := newModuleBox(t)
	plan := b.planned("virt")
	b.startErr = errors.New("exit status 1")
	b.startOut = "Failed to connect to bus: No such file or directory https://user:pw@example.org/x\n"
	b.advance(time.Minute)
	_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
	if refusal := moduleRefusalOf(t, err); refusal.Code != ModuleStartFailed {
		t.Fatalf("%+v", refusal)
	}
	st, err := b.system.GetSystemModuleStatus("virt")
	if err != nil {
		t.Fatal(err)
	}
	job := st.Job
	if job.State != modulecmd.JobFailed || job.Code != "start_failed" || job.Step != "" || job.Undo != "" || job.StartedAt != b.stamp(0) {
		t.Fatalf("%+v", job)
	}
	text := strings.Join(job.LogTail, "\n")
	if !strings.Contains(text, "Failed to connect to bus") || strings.Contains(text, "pw") {
		t.Errorf("what systemd said, as text: %q", text)
	}
	// and nothing is in the way of the next one
	b.startErr = nil
	if _, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID); err != nil {
		t.Fatalf("the next start: %v", err)
	}
}

func TestAStartDoesNotTouchTheLogOfARunThatHoldsTheLock(t *testing.T) {
	b := newModuleBox(t)
	plan := b.planned("virt")
	b.install("tool")
	b.write("/var/log/casaos/module-virt.log", "| the log of a run by hand\n")
	b.write("/var/log/casaos/module-tool.log", "| the log of another\n")
	state := b.env.Roots.Path("/var/lib/casaos/modules")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	release, err := b.env.Host.Lock(filepath.Join(state, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	before := b.tree()
	_, err = b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
	if refusal := moduleRefusalOf(t, err); refusal.Code != ModuleBusy || refusal.Data["reason"] == nil || refusal.Data["reason"] == "" {
		t.Errorf("install: %+v, %v", refusal, refusal.Data)
	}
	_, err = b.system.StartSystemModuleRemove(context.Background(), "tool")
	if refusal := moduleRefusalOf(t, err); refusal.Code != ModuleBusy || refusal.Data["reason"] == nil || refusal.Data["reason"] == "" {
		t.Errorf("remove: %+v, %v", refusal, refusal.Data)
	}
	if len(b.startedUnits()) != 0 {
		t.Error("a unit was started")
	}
	b.assertNothingWritten(before)
}

// ---- the status --------------------------------------------------------------------------------------------------------

func TestTheStatusOfAModuleThatNeverHadAJobIsIdle(t *testing.T) {
	b := newModuleBox(t)
	st, err := b.system.GetSystemModuleStatus("virt")
	if err != nil || st.Job.State != modulecmd.JobIdle || st.Job.Name != "virt" || st.Job.LogTail == nil || st.Status.Name != "virt" || st.Status.Installed {
		t.Errorf("%+v, %v", st, err)
	}
	b.install("virt")
	if st, _ := b.system.GetSystemModuleStatus("virt"); !st.Status.Installed || st.Job.State != modulecmd.JobIdle {
		t.Errorf("%+v", st)
	}
}

func TestTheStatusFollowsTheLogAndTheUnit(t *testing.T) {
	n := unitSightNonce
	for name, tc := range map[string]struct {
		unit  string // "" is a systemd that does not answer
		lines func(b *moduleBox) []string
		age   time.Duration // how long ago the log was written, and how far the box's clock has gone
		want  string
		step  string
	}{
		"queued, the unit is not there yet": {"inactive", func(b *moduleBox) []string { return []string{b.queuedLine("virt", n, 0)} }, 5 * time.Second, modulecmd.JobRunning, ""},
		"queued, the unit never came":       {"inactive", func(b *moduleBox) []string { return []string{b.queuedLine("virt", n, 0)} }, 40 * time.Second, modulecmd.JobInterrupted, ""},
		"going on": {"active", func(b *moduleBox) []string {
			return []string{b.queuedLine("virt", n, 0), b.step(n, "virt", "download", "begin", 0)}
		}, time.Hour, modulecmd.JobRunning, "download"},
		"activating": {"activating", func(b *moduleBox) []string { return []string{b.queuedLine("virt", n, 0)} }, time.Hour, modulecmd.JobRunning, ""},
		"gone in the middle of apt": {"inactive", func(b *moduleBox) []string {
			return []string{b.queuedLine("virt", n, 0), b.step(n, "virt", "packages", "begin", 0)}
		}, time.Minute, modulecmd.JobInterrupted, "packages"},
		"systemd silent, a log that is fresh": {"", func(b *moduleBox) []string {
			return []string{b.queuedLine("virt", n, 0), b.step(n, "virt", "packages", "begin", 0)}
		}, 10 * time.Minute, modulecmd.JobRunning, "packages"},
		"systemd silent, a log that is old": {"", func(b *moduleBox) []string {
			return []string{b.queuedLine("virt", n, 0), b.step(n, "virt", "packages", "begin", 0)}
		}, 40 * time.Minute, modulecmd.JobInterrupted, "packages"},
		"done": {"inactive", func(b *moduleBox) []string {
			return []string{b.queuedLine("virt", n, 0), b.step(n, "virt", "done", "done", 0), b.result(n, "virt", "ok", "done", "-", "-", 0)}
		}, time.Minute, modulecmd.JobSucceeded, "done"},
	} {
		t.Run(name, func(t *testing.T) {
			b := newModuleBox(t)
			if tc.unit == "" {
				b.silent = true
			} else {
				b.setUnit("casaos-module-virt.service", tc.unit)
			}
			b.unitSays("virt", tc.lines(b)...)
			b.advance(tc.age)
			st, err := b.system.GetSystemModuleStatus("virt")
			if err != nil || st.Job.State != tc.want || st.Job.Step != tc.step {
				t.Errorf("%+v, %v", st.Job, err)
			}
		})
	}
}

// The core is restarted in the middle of a job, and after it: a service that has nothing of the one before finds the job again from the log and the unit, and starts nothing.
func TestARestartOfTheCoreFindsTheJobAgain(t *testing.T) {
	b := newModuleBox(t)
	plan := b.planned("virt")
	if _, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID); err != nil {
		t.Fatal(err)
	}
	nonce := b.nonceOfLog("virt")
	core := b.restarted()
	// the core went down between the queued line and the unit
	st, err := core.GetSystemModuleStatus("virt")
	if err != nil || st.Job.State != modulecmd.JobRunning {
		t.Fatalf("queued: %+v, %v", st.Job, err)
	}
	// the unit is on: it truncates the log and goes on with the nonce it was given
	b.setUnit("casaos-module-virt.service", "active")
	b.advance(2 * time.Second)
	b.unitSays("virt", b.step(nonce, "virt", "packages", "begin", 0), "| Unpacking libvirt")
	if st, err = b.restarted().GetSystemModuleStatus("virt"); err != nil || st.Job.State != modulecmd.JobRunning || st.Job.Step != "packages" {
		t.Fatalf("running: %+v, %v", st.Job, err)
	}
	// and it is not started again, by this core or by the next
	if _, err := core.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID); moduleRefusalOf(t, err).Code != ModuleBusy {
		t.Fatalf("a job that runs: %v", err)
	}
	// the unit ends
	b.advance(time.Minute)
	b.unitSays("virt", b.step(nonce, "virt", "packages", "begin", -time.Minute), b.step(nonce, "virt", "done", "done", 0), "installed: virt v0.1.0", b.result(nonce, "virt", "ok", "done", "-", "-", 0))
	b.setUnit("casaos-module-virt.service", "inactive")
	if st, err = b.restarted().GetSystemModuleStatus("virt"); err != nil || st.Job.State != modulecmd.JobSucceeded || st.Job.EndedAt != b.stamp(0) {
		t.Fatalf("done: %+v, %v", st.Job, err)
	}
	if got := len(b.startedUnits()); got != 1 {
		t.Errorf("%d units were started", got)
	}
}

func TestACoreThatRestartsNeverStartsAJobThatWasInterrupted(t *testing.T) {
	b := newModuleBox(t)
	plan := b.planned("virt")
	if _, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID); err != nil {
		t.Fatal(err)
	}
	nonce := b.nonceOfLog("virt")
	b.setUnit("casaos-module-virt.service", "active")
	b.unitSays("virt", b.step(nonce, "virt", "packages", "begin", 0))
	// the machine is rebooted: the unit is gone
	b.setUnit("casaos-module-virt.service", "inactive")
	b.advance(10 * time.Second)
	core := b.restarted()
	if st, _ := core.GetSystemModuleStatus("virt"); st.Job.State != modulecmd.JobRunning {
		t.Errorf("10 seconds: %+v", st.Job)
	}
	b.advance(time.Minute)
	for i := 0; i < 3; i++ {
		st, err := core.GetSystemModuleStatus("virt")
		if err != nil || st.Job.State != modulecmd.JobInterrupted || st.Job.Step != "packages" || st.Job.EndedAt != "" {
			t.Fatalf("interrupted: %+v, %v", st.Job, err)
		}
	}
	if got := len(b.startedUnits()); got != 1 {
		t.Errorf("%d units were started: the core started the job again", got)
	}
}

func TestAJobThatEndedLongAgoIsWhatItWas(t *testing.T) {
	n := unitSightNonce
	for name, tc := range map[string]struct {
		lines func(b *moduleBox) []string
		want  modulecmd.Job
	}{
		"done": {func(b *moduleBox) []string {
			return []string{b.queuedLine("virt", n, 0), b.result(n, "virt", "ok", "done", "-", "-", time.Second)}
		}, modulecmd.Job{State: modulecmd.JobSucceeded, Step: "done"}},
		"failed": {func(b *moduleBox) []string {
			return []string{b.queuedLine("virt", n, 0), b.step(n, "virt", "route", "failed", 0), "kept_packages: qemu-utils", b.result(n, "virt", "failed", "route", "route_timeout", "rolled_back", time.Second)}
		}, modulecmd.Job{State: modulecmd.JobFailed, Step: "route", Code: "route_timeout", Undo: "rolled_back", KeptPackages: []string{"qemu-utils"}}},
		"refused": {func(b *moduleBox) []string {
			return []string{b.queuedLine("virt", n, 0), b.result(n, "virt", "refused", "-", "disk_short", "-", time.Second)}
		}, modulecmd.Job{State: modulecmd.JobRefused, Code: "disk_short"}},
	} {
		t.Run(name, func(t *testing.T) {
			b := newModuleBox(t)
			b.unitSays("virt", tc.lines(b)...)
			started := b.stamp(0)
			b.advance(800 * 24 * time.Hour)
			st, err := b.restarted().GetSystemModuleStatus("virt")
			if err != nil {
				t.Fatal(err)
			}
			job := st.Job
			job.LogTail = nil
			want := tc.want
			want.Name, want.Kind, want.StartedAt, want.EndedAt = "virt", modulecmd.KindInstall, started, time.Date(2026, 10, 18, 12, 0, 1, 0, time.UTC).Format(time.RFC3339)
			if !reflect.DeepEqual(job, want) {
				t.Errorf("got  %+v\nwant %+v", job, want)
			}
		})
	}
}

// ---- nothing to offer, and what only reads ----------------------------------------------------------------------------------

func TestWithNoCatalogueTheModulesOfferNothingAndNothingChanges(t *testing.T) {
	b := newModuleBox(t)
	if err := os.Remove(b.env.Roots.Path("/usr/share/casaos/modules/catalogue.json")); err != nil {
		t.Fatal(err)
	}
	before := b.tree()
	list, err := b.system.ListSystemModules()
	if err != nil || list.Catalogue.OK || list.Catalogue.Error != "none" || list.Modules == nil || len(list.Modules) != 0 {
		t.Errorf("the list: %+v, %v", list, err)
	}
	plan, err := b.system.PlanSystemModule("virt")
	if err != nil || plan.Refused != osmodule.CheckCatalogue || plan.PlanID != "" {
		t.Errorf("the plan: %+v, %v", plan, err)
	}
	if _, err := b.system.StartSystemModuleInstall(context.Background(), "virt", "m1-"+strings.Repeat("0", 32)); moduleRefusalOf(t, err).Code != osmodule.CheckCatalogue {
		t.Errorf("install: %v", err)
	}
	if _, err := b.system.StartSystemModuleRemove(context.Background(), "virt"); moduleRefusalOf(t, err).Code != ModuleNotInstalled {
		t.Errorf("remove: %v", err)
	}
	st, err := b.system.GetSystemModuleStatus("virt")
	if err != nil || st.Job.State != modulecmd.JobIdle || st.Status.Installed {
		t.Errorf("status: %+v, %v", st, err)
	}
	if len(b.startedUnits()) != 0 {
		t.Error("a unit was started")
	}
	b.assertNothingWritten(before)

	// a catalogue that does not pass is none at all
	b.write("/usr/share/casaos/modules/catalogue.json", `{"schema":1,"modules":[{"name":"virt"}]}`)
	list, err = b.system.ListSystemModules()
	if err != nil || list.Catalogue.OK || list.Catalogue.Error != "invalid" || len(list.Modules) != 0 {
		t.Errorf("an invalid catalogue: %+v, %v", list, err)
	}
	if plan, _ := b.system.PlanSystemModule("virt"); plan.Refused != osmodule.CheckCatalogue {
		t.Errorf("the plan of an invalid catalogue: %+v", plan)
	}
}

func TestWhatOnlyReadsWritesNothing(t *testing.T) {
	b := newModuleBox(t)
	b.install("virt")
	b.setCatalogue("virt", "tool")
	b.unitSays("virt", b.queuedLine("virt", unitSightNonce, -time.Hour))
	before := b.tree()
	list, err := b.system.ListSystemModules()
	if err != nil || !list.Catalogue.OK || len(list.Modules) != 2 {
		t.Fatalf("%+v, %v", list, err)
	}
	var virt, tool *modulecmd.ListModule
	for i := range list.Modules {
		switch list.Modules[i].Name {
		case "virt":
			virt = &list.Modules[i]
		case "tool":
			tool = &list.Modules[i]
		}
	}
	if virt == nil || tool == nil || virt.Installed == nil || tool.Installed != nil || !virt.System.Supported || virt.Title != "Module virt" {
		t.Errorf("%+v", list.Modules)
	}
	for _, name := range []string{"virt", "tool", "nothing"} {
		if _, err := b.system.PlanSystemModule(name); err != nil {
			t.Fatal(err)
		}
		if _, err := b.system.GetSystemModuleStatus(name); err != nil {
			t.Fatal(err)
		}
	}
	b.assertNothingWritten(before)
	if len(b.startedUnits()) != 0 {
		t.Error("a unit was started")
	}
}

func TestAPlanHasTheTimeItHasAndPlansTakeTurns(t *testing.T) {
	original := systemModulePlanTimeout
	systemModulePlanTimeout = 300 * time.Millisecond
	t.Cleanup(func() { systemModulePlanTimeout = original })
	b := newModuleBox(t)
	plan := b.planned("virt")

	b.mu.Lock()
	b.hang = true
	b.mu.Unlock()
	before := b.tree()
	begun := time.Now()
	_, err := b.system.PlanSystemModule("virt")
	if refusal := moduleRefusalOf(t, err); refusal.Code != ModulePlanTimeout {
		t.Fatalf("%v", err)
	}
	if took := time.Since(begun); took > 5*time.Second {
		t.Errorf("a plan with 300 ms took %v", took)
	}
	// a start whose plan hangs is refused the same, and starts nothing
	if _, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID); moduleRefusalOf(t, err).Code != ModulePlanTimeout {
		t.Fatalf("the start: %v", err)
	}
	// several at once: all end, each within its time
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := b.system.PlanSystemModule("virt")
			if refusal := moduleRefusalOf(t, err); refusal.Code != ModulePlanTimeout {
				t.Errorf("%v", err)
			}
		}()
	}
	wg.Wait()
	b.mu.Lock()
	b.hang = false
	b.mu.Unlock()
	if len(b.startedUnits()) != 0 {
		t.Error("a unit was started")
	}
	b.assertNothingWritten(before)
	// and once it is released the slot is free
	if _, err := b.system.PlanSystemModule("virt"); err != nil {
		t.Fatal(err)
	}
}

// slowRunner holds every command of the assistant for a moment and counts how many are held at once.
type slowRunner struct {
	inner    modulecmd.Runner
	inFlight *atomic.Int64
	most     *atomic.Int64
}

func (r slowRunner) Run(ctx context.Context, c modulecmd.Cmd) (modulecmd.Output, error) {
	n := r.inFlight.Add(1)
	defer r.inFlight.Add(-1)
	for {
		m := r.most.Load()
		if n <= m || r.most.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(3 * time.Millisecond)
	return r.inner.Run(ctx, c)
}

func TestPlansTakeTurns(t *testing.T) {
	b := newModuleBox(t)
	var inFlight, most atomic.Int64
	b.env.Runner = slowRunner{inner: b.env.Runner, inFlight: &inFlight, most: &most}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if plan, err := b.system.PlanSystemModule("virt"); err != nil || plan.Refused != "" {
				t.Errorf("%+v, %v", plan, err)
			}
		}()
	}
	wg.Wait()
	if most.Load() != 1 {
		t.Errorf("%d commands of the plans were held at once", most.Load())
	}
}

// Two starts that both found the box free are one job when the first has not yet written its first line: the second one waits for it.
func TestTwoStartsThatBothFoundTheBoxFreeAreStillOneJob(t *testing.T) {
	for name, tc := range map[string]struct {
		installed bool
		start     func(b *moduleBox, plan string) error
	}{
		"install": {false, func(b *moduleBox, plan string) error {
			_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan)
			return err
		}},
		"remove": {true, func(b *moduleBox, _ string) error {
			_, err := b.system.StartSystemModuleRemove(context.Background(), "virt")
			return err
		}},
	} {
		t.Run(name, func(t *testing.T) {
			b := newModuleBox(t)
			plan := b.planned("virt").PlanID
			if tc.installed {
				b.install("virt")
			}
			var arrived atomic.Int32
			b.mu.Lock()
			b.onLock = func() {
				if arrived.Add(1) == 1 {
					// the first waits to see whether a second comes to write beside it
					for deadline := time.Now().Add(400 * time.Millisecond); arrived.Load() < 2 && time.Now().Before(deadline); {
						time.Sleep(time.Millisecond)
					}
					return
				}
				// the second comes after the first has started its unit
				for deadline := time.Now().Add(5 * time.Second); len(b.startedUnits()) < 1 && time.Now().Before(deadline); {
					time.Sleep(time.Millisecond)
				}
			}
			b.mu.Unlock()
			var wg sync.WaitGroup
			errs := make([]error, 2)
			for i := range errs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs[i] = tc.start(b, plan)
				}()
			}
			wg.Wait()
			if ok := btoi(errs[0] == nil) + btoi(errs[1] == nil); ok != 1 || len(b.startedUnits()) != 1 {
				t.Errorf("%d starts were let through and %d units were started: %v", ok, len(b.startedUnits()), errs)
			}
		})
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// A remove is refused when the assistant itself finds the box busy, even if the core does not.
func TestARemoveIsRefusedWhenTheAssistantFindsTheBoxBusy(t *testing.T) {
	b := newModuleBox(t)
	b.install("virt")
	b.write("/var/lib/casaos/os-upgrade/busy", "")
	before := b.tree()
	_, err := b.system.StartSystemModuleRemove(context.Background(), "virt")
	if refusal := moduleRefusalOf(t, err); refusal.Code != ModuleBusy || refusal.Data["reason"] == nil || refusal.Data["reason"] == "" {
		t.Fatalf("%+v, %v", refusal, refusal.Data)
	}
	if len(b.startedUnits()) != 0 {
		t.Error("a unit was started")
	}
	b.assertNothingWritten(before)
}

// The module that the answer of a start is about is named even when the assistant cannot say more of it.
func TestTheAnswerOfAStartNamesTheModuleWhateverTheAssistantSaysOfIt(t *testing.T) {
	b := newModuleBox(t)
	// the folder of the manifests is a link that leads to itself: the assistant cannot list what is installed
	dir := b.env.Roots.Path("/etc/casaos/modules")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("modules", dir); err != nil {
		t.Skip("a link cannot be made here")
	}
	status, err := b.system.StartSystemModuleRemove(context.Background(), "virt")
	if err != nil {
		t.Fatal(err)
	}
	if status.Status.Name != "virt" || status.Job.State != modulecmd.JobRunning {
		t.Errorf("%+v", status)
	}
}
