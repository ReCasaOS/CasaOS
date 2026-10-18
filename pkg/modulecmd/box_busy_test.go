package modulecmd

import (
	"context"
	"slices"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The units that say a package manager is at work are the ones the readiness report of the upgrade asks about, and the units that update ReCasaOS itself.
func TestEveryUnitOfAPackageManagerMakesThePlanBusy(t *testing.T) {
	units := []string{
		"casaos-update.service", "casaos-package-update.service", "casaos-docker-update.service", "casaos-os-upgrade.service",
		"apt-daily.service", "apt-daily-upgrade.service",
	}
	for _, unit := range units {
		for _, state := range []string{"active", "activating", "deactivating"} {
			t.Run(unit+"/"+state, func(t *testing.T) {
				f := newFixture(t)
				f.runner.fail["systemctl show "+unit] = 0
				f.runner.said["systemctl show "+unit] = state + "\n"
				plan := f.json("plan", "virt")
				if plan["refused"] != osmodule.CheckBusy {
					t.Fatalf("refused: %v", plan["refused"])
				}
				if detail := checkOf(plan, osmodule.CheckBusy)["detail"]; detail != "package_manager" {
					t.Errorf("the detail: %v", detail)
				}
			})
		}
	}
	// a unit that is not at work is no busy box
	for _, state := range []string{"inactive", "failed", "", "reloading\n"} {
		f := newFixture(t)
		f.runner.fail["systemctl show apt-daily.service"] = 0
		f.runner.said["systemctl show apt-daily.service"] = state
		if plan := f.json("plan", "virt"); plan["refused"] != nil {
			t.Errorf("%q: refused %v", state, plan["refused"])
		}
	}
}

// the readiness report and the assistant ask about the same units: one list
func TestTheUnitsAreThoseOfTheReadinessReport(t *testing.T) {
	f := newFixture(t)
	bx := &box{ctx: context.Background(), env: &f.env}
	if _, err := bx.PackageManagerBusy(); err != nil {
		t.Fatal(err)
	}
	var asked []string
	for _, c := range f.runner.calls {
		if len(c) > 2 && c[0] == "systemctl" && c[1] == "show" {
			asked = append(asked, c[2])
		}
	}
	if len(osreadiness.BusyUnits()) == 0 {
		t.Fatal("no units")
	}
	for _, unit := range osreadiness.BusyUnits() {
		if !slices.Contains(asked, unit) {
			t.Errorf("%s is not asked about (asked: %v)", unit, asked)
		}
	}
	// and what is handed out is a copy
	osreadiness.BusyUnits()[0] = "x"
	if osreadiness.BusyUnits()[0] == "x" {
		t.Error("the list can be changed from outside")
	}
}

func checkOf(plan map[string]any, code string) map[string]any {
	for _, c := range plan["checks"].([]any) {
		if m := c.(map[string]any); m["code"] == code {
			return m
		}
	}
	return nil
}
