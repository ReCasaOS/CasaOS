package osmodule

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// manifestWithUnit is the manifest of the tests with another unit, and the units that the upgrade stops following it (they can only be the module's own).
func manifestWithUnit(t *testing.T, unit string) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(virtManifest), &doc); err != nil {
		t.Fatal(err)
	}
	doc["unit"] = unit
	doc["os_upgrade"].(map[string]any)["stop_units"] = []string{unit}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// A module's unit is started and stopped by the install and the remove of the module, and the core changes the box with units of its own: the jobs of the modules, the updates and the
// upgrade, which the rest of the box waits for while they run, and the services of the core. A module whose unit is one of these would keep the box busy for ever, stop its own job when it
// is removed, or take the core's place.
func TestTheUnitOfAModuleIsNoUnitOfTheCoreOrOfAJob(t *testing.T) {
	refused := map[string][]string{
		"the units of the jobs of the modules": {
			"casaos-module-virt.service", "casaos-module-a.service", "casaos-module-" + strings.Repeat("a", 32) + ".service", "casaos-module-.service", "casaos-module-x@y.service", "casaos-module-casaos-module-x.service",
		},
		"the units that the maintenance check lists": {
			"casaos-update.service", "casaos-package-update.service", "casaos-docker-update.service", "casaos-os-upgrade.service",
		},
		"the services of the core and of the distribution": {
			"casaos.service", "casaos-gateway.service", "casaos-user-service.service", "casaos-message-bus.service", "casaos-local-storage.service", "casaos-app-management.service",
			"rclone.service", "casaos-wsdd.service", "usb-mount@.service", "usb-mount@sda1.service",
		},
		"the units of the measures of the upgrade":  {"casaos-os-upgrade-measure.service"},
		"the units of apt, which make the box busy": {"apt-daily.service", "apt-daily-upgrade.service"},
	}
	for group, units := range refused {
		for _, unit := range units {
			t.Run(group+": "+unit, func(t *testing.T) {
				if ValidUnit(unit) {
					t.Errorf("ValidUnit(%q)", unit)
				}
				_, err := Parse("virt.json", []byte(manifestWithUnit(t, unit)))
				var inv *Invalid
				if !errors.As(err, &inv) || inv.Field != "unit" || inv.Reason != ReasonValue {
					t.Errorf("the manifest with the unit %q: %v", unit, err)
				}
				// and so there is no catalogue that offers it
				entry := strings.Replace(entryText("virt"), virtManifest, manifestWithUnit(t, unit), 1)
				if _, err := ParseCatalogue([]byte(catalogueText(entry))); err == nil {
					t.Errorf("a catalogue offers a module whose unit is %q", unit)
				}
			})
		}
	}
}

// What is near them is the module's own: a name is refused whole, and the pattern is of the jobs' alone (a prefix with its dash).
func TestTheUnitsThatAreNearTheCoresAreTheModulesOwn(t *testing.T) {
	for _, unit := range []string{
		"casaos-virt-management.service", "casaos-module.service", "casaos-modules-virt.service", "my-casaos-module-x.service", "casaos-updates.service", "casaos-update-x.service",
		"casaos-gateway2.service", "casaos-os-upgrade-x.service", "Casaos.service", "casaos@x.service", "rclone2.service", "usb-mount.service", "apt-daily-x.service", "virt.service", "x.service",
	} {
		if !ValidUnit(unit) {
			t.Errorf("ValidUnit(%q) is false", unit)
		}
		if _, err := Parse("virt.json", []byte(manifestWithUnit(t, unit))); err != nil {
			t.Errorf("the manifest with the unit %q: %v", unit, err)
		}
	}
	// the rule of the pattern is still there
	for _, unit := range []string{"", "x", "x.timer", "-x.service", "a b.service", "a/b.service", strings.Repeat("u", 65) + ".service"} {
		if ValidUnit(unit) {
			t.Errorf("ValidUnit(%q)", unit)
		}
	}
}
