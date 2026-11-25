package service

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/autoupdate"
	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

const withdrawnModuleReason = "It loses the data of its disks."

// withdraw flags a tag of a module in the online catalogue foreignCatalogue wrote.
func (b *moduleBox) withdraw(name, tag string) {
	b.t.Helper()
	path := "/var/lib/casaos/modules/catalogue.json"
	b.write(path, strings.Replace(b.read(path), `"withdrawn":[]`, `"withdrawn":[{"name":"`+name+`","tag":"`+tag+`","reason":"`+withdrawnModuleReason+`"}]`, 1))
}

func checkWithdrawnRefusal(t *testing.T, b *moduleBox, err error) {
	t.Helper()
	refusal := moduleRefusalOf(t, err)
	if refusal.Code != osmodule.RefusedWithdrawn || refusal.Data["reason"] != withdrawnModuleReason {
		t.Errorf("refused with %q, %v", refusal.Code, refusal.Data)
	}
	if checks, _ := refusal.Data["checks"].([]osmodule.Check); len(checks) < 2 || checks[1].Code != osmodule.CheckWithdrawn || checks[1].Detail != withdrawnModuleReason {
		t.Errorf("the checks: %+v", refusal.Data["checks"])
	}
	if len(b.startedUnits()) != 0 {
		t.Error("a unit was started")
	}
}

func TestAnInstallOfAWithdrawnModuleVersionIsRefused(t *testing.T) {
	withOnlineCatalogueSetting(t, true)
	b := newModuleBox(t)
	b.foreignCatalogue("virt", "someone/CasaOS-Virt")
	plan := b.planned("virt")
	b.withdraw("virt", onlineModuleTag)
	if got, err := b.system.PlanSystemModule("virt"); err != nil || got.Refused != osmodule.RefusedWithdrawn {
		t.Fatalf("the plan: %+v, %v", got, err)
	}
	before := b.tree()
	_, err := b.system.StartSystemModuleInstall(context.Background(), "virt", plan.PlanID)
	checkWithdrawnRefusal(t, b, err)
	b.assertNothingWritten(before)
}

func TestAnUpdateToAWithdrawnModuleVersionIsRefused(t *testing.T) {
	withOnlineCatalogueSetting(t, true)
	b := newModuleBox(t)
	b.updatable("virt", boxModuleTag, boxModuleTag)
	b.foreignCatalogue("virt", "someone/CasaOS-Virt")
	plan := b.updatePlanned("virt")
	b.withdraw("virt", onlineModuleTag)
	if got, err := b.system.PlanSystemModuleUpdate("virt"); err != nil || got.Refused != osmodule.RefusedWithdrawn {
		t.Fatalf("the plan: %+v, %v", got, err)
	}
	before := b.tree()
	_, err := b.system.StartSystemModuleUpdate(context.Background(), "virt", plan.PlanID)
	checkWithdrawnRefusal(t, b, err)
	b.assertNothingWritten(before)
}

// The night hears of the installed modules at a version the catalogue withdrew, whether they have an update or not, and only of those.
func TestTheNightIsToldOfTheInstalledVersionsWithdrawn(t *testing.T) {
	version := "v1.0.0"
	installed := &modulecmd.Installed{Version: &version}
	why := &modulecmd.WithdrawnInfo{Tag: version, Reason: "Broken."}
	p := &nightProvider{planLatest: "v0.2.5", more: []modulecmd.ListModule{
		{Name: "flagged", Installed: installed, Withdrawn: why},
		{Name: "delisted", Installed: installed, Withdrawn: why, System: modulecmd.SystemInfo{Reasons: []string{"catalogue"}}},
		{Name: "absent", Withdrawn: why}, // (the list never says it of a module not installed; the night does not trust it to)
	}}
	got, err := (&NightModules{Provider: p}).Withdrawn(context.Background())
	want := []autoupdate.Withdrawn{{Name: "flagged", Tag: version, Reason: "Broken."}, {Name: "delisted", Tag: version, Reason: "Broken."}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Withdrawn() = %+v, %v\nwant %+v", got, err, want)
	}
}

// A module whose latest tag is withdrawn is listed so, and never updated by name.
func TestTheNightNeverUpdatesToAWithdrawnVersion(t *testing.T) {
	version := "v1.0.0"
	p := &nightProvider{planLatest: "v1.1.0", more: []modulecmd.ListModule{
		{Name: "flagged", Installed: &modulecmd.Installed{Version: &version}, Update: &modulecmd.UpdateInfo{Available: true, Installed: version, Latest: "v1.1.0"}, LatestWithdrawn: true},
	}}
	n := &NightModules{Provider: p}
	list, err := n.List(context.Background())
	if err != nil || len(list) != 2 || list[1] != (autoupdate.ModuleUpdate{Name: "flagged", Installed: version, Latest: "v1.1.0", Withdrawn: true}) {
		t.Fatalf("List() = %+v, %v", list, err)
	}
	if outcome, detail := n.Update(context.Background(), "flagged"); outcome != autoupdate.NotStarted || detail != "not started: not listed" || len(p.started) != 0 {
		t.Fatalf("Update() = %v, %q, started %v", outcome, detail, p.started)
	}
}
