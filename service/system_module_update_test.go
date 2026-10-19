package service

import (
	"context"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The update of a module that is installed: the same routes and the same start as an install's, for the plan of an update and a unit that runs the verb update.

// updatable puts a module on the box as an install leaves it (its manifest, its version and its list of files) at the tag installed, and a catalogue that names latest.
func (b *moduleBox) updatable(name, installed, latest string) {
	b.t.Helper()
	b.install(name)
	b.write("/var/lib/casaos/modules/"+name+".version", installed+"\n")
	b.write("/var/lib/casaos/modules/"+name+".files", "casaos-module-files 1\nf "+boxModuleSHA+" /usr/bin/casaos-"+name+"\n")
	b.entries = []osmodule.Entry{b.entry(name, latest)}
	b.writeCatalogue()
}

func (b *moduleBox) updatePlanned(name string) osmodule.PlanResult {
	b.t.Helper()
	plan, err := b.system.PlanSystemModuleUpdate(name)
	if err != nil {
		b.t.Fatal(err)
	}
	if plan.Refused != "" || !modulePlanIDPattern.MatchString(plan.PlanID) || plan.Mode != osmodule.ModeUpdate {
		b.t.Fatalf("the plan is refused, has no id or is not an update's: %+v", plan)
	}
	return plan
}

func TestAnUpdateIsPlannedAndStartedAsAnUpdate(t *testing.T) {
	b := newModuleBox(t)
	b.updatable("virt", "v0.1.0", "v0.1.1")
	before := b.tree()
	plan := b.updatePlanned("virt")
	if plan.Tag != "v0.1.1" || plan.Update == nil || plan.Update.Installed != "v0.1.0" || plan.Update.Latest != "v0.1.1" {
		t.Errorf("%+v %+v", plan, plan.Update)
	}
	// the plan reads and writes nothing
	b.assertNothingWritten(before)
	status, err := b.system.StartSystemModuleUpdate(context.Background(), "virt", plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Job.Kind != modulecmd.KindUpdate || status.Job.State != modulecmd.JobRunning {
		t.Errorf("the answer of the start: %+v", status.Job)
	}
	started := b.startedUnits()
	if len(started) != 1 {
		t.Fatalf("units: %q", started)
	}
	want := []string{"module", "update", "virt", "--yes", "--plan-id", plan.PlanID}
	args := started[0]
	if len(args) < len(want) || strings.Join(args[len(args)-len(want):], " ") != strings.Join(want, " ") {
		t.Errorf("the unit: %q", args)
	}
	nonce := b.nonceOfLog("virt")
	if want := modulecmd.QueuedLine("virt", nonce, modulecmd.KindUpdate, b.now()); b.read("/var/log/casaos/module-virt.log") != want {
		t.Errorf("the log is %q, want %q", b.read("/var/log/casaos/module-virt.log"), want)
	}
	st, err := b.system.GetSystemModuleStatus("virt")
	if err != nil || st.Job.Kind != modulecmd.KindUpdate || st.Job.State != modulecmd.JobRunning {
		t.Errorf("the status: %+v, %v", st.Job, err)
	}
}

func TestAnUpdateIsRefusedWithTheCodeOfItsPlanAndNothingIsStarted(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(b *moduleBox)
		code  string
	}{
		{"the same version", func(b *moduleBox) { b.updatable("virt", "v0.1.1", "v0.1.1") }, osmodule.RefusedUpToDate},
		{"an installed version that is later", func(b *moduleBox) { b.updatable("virt", "v0.2.0", "v0.1.1") }, osmodule.RefusedDowngrade},
		{"v0.1.10 against v0.1.9", func(b *moduleBox) { b.updatable("virt", "v0.1.10", "v0.1.9") }, osmodule.RefusedDowngrade},
		{"nothing installed", func(b *moduleBox) { b.entries = []osmodule.Entry{b.entry("virt", "v0.1.1")}; b.writeCatalogue() }, osmodule.RefusedNotInstalled},
		{"a version that cannot be read", func(b *moduleBox) {
			b.updatable("virt", "v0.1.0", "v0.1.1")
			b.write("/var/lib/casaos/modules/virt.version", "latest\n")
		}, osmodule.RefusedVersionUnknown},
		{"a list of files of the earlier format", func(b *moduleBox) {
			b.updatable("virt", "v0.1.0", "v0.1.1")
			b.write("/var/lib/casaos/modules/virt.files", "/usr/bin/casaos-virt\n")
		}, osmodule.RefusedFilesUnverified},
		{"no list of files", func(b *moduleBox) {
			b.updatable("virt", "v0.1.0", "v0.1.1")
			b.write("/var/lib/casaos/modules/virt.files", "")
		}, osmodule.RefusedFilesUnreadable},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newModuleBox(t)
			c.setup(b)
			plan, err := b.system.PlanSystemModuleUpdate("virt")
			if err != nil || string(plan.Refused) != c.code || plan.PlanID != "" {
				t.Fatalf("the plan: %q, %q, %v", plan.Refused, plan.PlanID, err)
			}
			before := b.tree()
			_, err = b.system.StartSystemModuleUpdate(context.Background(), "virt", unitPlanID)
			refusal := moduleRefusalOf(t, err)
			if refusal.Code != c.code || refusal.Data["checks"] == nil || !strings.Contains(refusal.Detail, "updated") {
				t.Errorf("refused with %+v", refusal)
			}
			if len(b.startedUnits()) != 0 {
				t.Errorf("a unit was started: %q", b.startedUnits())
			}
			b.assertNothingWritten(before)
		})
	}
}

func TestAnUpdateWhosePlanIsNoLongerTheOneThatWasReadIsRefused(t *testing.T) {
	b := newModuleBox(t)
	b.updatable("virt", "v0.1.0", "v0.1.1")
	plan := b.updatePlanned("virt")
	b.updatable("virt", "v0.1.0", "v0.1.2") // another release came in the meantime
	before := b.tree()
	_, err := b.system.StartSystemModuleUpdate(context.Background(), "virt", plan.PlanID)
	if refusal := moduleRefusalOf(t, err); refusal.Code != ModulePlanChanged {
		t.Errorf("refused with %+v", refusal)
	}
	if len(b.startedUnits()) != 0 {
		t.Error("a unit was started")
	}
	b.assertNothingWritten(before)
	// the id of the plan of an install is not the id of the plan of an update
	b2 := newModuleBox(t)
	installPlan := b2.planned("virt")
	b2.updatable("virt", "v0.1.0", boxModuleTag+"1")
	_, err = b2.system.StartSystemModuleUpdate(context.Background(), "virt", installPlan.PlanID)
	if refusal := moduleRefusalOf(t, err); refusal.Code != ModulePlanChanged {
		t.Errorf("refused with %+v", refusal)
	}
}

func TestAnInstallOfAModuleThatIsInstalledIsStillRefused(t *testing.T) {
	b := newModuleBox(t)
	plan := b.planned("virt")
	b.updatable("virt", "v0.1.0", "v0.1.1")
	_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
	if refusal := moduleRefusalOf(t, err); refusal.Code != osmodule.CheckInstalled {
		t.Errorf("refused with %+v", refusal)
	}
	if got, err := b.system.PlanSystemModule("virt"); err != nil || got.Refused != osmodule.CheckInstalled || got.Mode != "" {
		t.Errorf("the plan of an install: %q %q, %v", got.Refused, got.Mode, err)
	}
}

func TestAnUpdateKeepsToTheRulesOfAStart(t *testing.T) {
	b := newModuleBox(t)
	b.updatable("virt", "v0.1.0", "v0.1.1")
	plan := b.updatePlanned("virt")
	for name, c := range map[string]struct {
		module, id string
		code       string
		field      any
	}{
		"a name that is not one":  {"Virt", plan.PlanID, ModuleBadRequest, "name"},
		"an id that is not one":   {"virt", "m1-xyz", ModuleBadRequest, "plan_id"},
		"an empty id":             {"virt", "", ModuleBadRequest, "plan_id"},
		"an id of an install key": {"virt", strings.ToUpper(plan.PlanID), ModuleBadRequest, "plan_id"},
	} {
		_, err := b.system.StartSystemModuleUpdate(context.Background(), c.module, c.id)
		refusal := moduleRefusalOf(t, err)
		if refusal.Code != c.code || refusal.Data["field"] != c.field {
			t.Errorf("%s: %+v", name, refusal)
		}
	}
	if _, err := b.system.PlanSystemModuleUpdate("Virt"); moduleRefusalOf(t, err).Code != ModuleBadRequest {
		t.Error("a plan for a name that is not one")
	}
	// something else at work
	b.setUnit(systemPackageUpdateUnit, "active")
	_, err := b.system.StartSystemModuleUpdate(context.Background(), "virt", plan.PlanID)
	if refusal := moduleRefusalOf(t, err); refusal.Code != ModuleBusy {
		t.Errorf("busy: %+v", refusal)
	}
	b.setUnit(systemPackageUpdateUnit, "inactive")
	// not root
	b.updater.getEUID = func() int { return 1000 }
	_, err = b.system.StartSystemModuleUpdate(context.Background(), "virt", plan.PlanID)
	if refusal := moduleRefusalOf(t, err); refusal.Code != ModuleNotRoot {
		t.Errorf("not root: %+v", refusal)
	}
	if len(b.startedUnits()) != 0 {
		t.Errorf("a unit was started: %q", b.startedUnits())
	}
}
