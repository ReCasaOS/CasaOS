package osreadiness

import (
	"io/fs"
	"strings"
	"testing"
)

// virtModule is a manifest that passes, with the section for an upgrade of the operating system.
const virtModule = `{"schema":1,"name":"virt","title":"Virtual machines","component":"CasaOS-VirtManagement","unit":"casaos-virt-management.service","gateway_route":"/v2/virt_management",` +
	`"packages":{"common":["qemu-utils","libvirt-daemon-system"]},"min_recasaos":"0.5.23","os":["debian:12"],` +
	`"os_upgrade":{"stop_units":["casaos-virt-management.service"],"stop_timeout_seconds":300,"maintenance":{"prepare":"/v2/virt_management/maintenance/prepare",` +
	`"status":"/v2/virt_management/maintenance/status","resume":"/v2/virt_management/maintenance/resume"}}}`

func TestAModuleStandsInTheWayOfTheUpgradeAndIsNamed(t *testing.T) {
	module := func(name, text string) func(*fakeProbe) {
		return func(f *fakeProbe) { f.set(modulesDir+"/"+name, text) }
	}
	runScenarios(t, "modules", []scenario{
		{name: "no folder of modules", severity: OK},
		{name: "an empty folder", setup: func(f *fakeProbe) { f.dirs[modulesDir] = true }, severity: OK},
		{name: "a folder with what is not a manifest", setup: module("README", "hello"), severity: OK},
		{name: "a manifest with a field that is not known", setup: module("virt.json", strings.Replace(virtModule, `"os_upgrade"`, `"os_upgrade_x"`, 1)),
			severity: Refuse, data: D{"modules": []string{}, "invalid": []string{"virt"}, "reasons": []string{"invalid"}}, title: "module"},
		{name: "a module that declares an upgrade the engine does not make", setup: module("virt.json", virtModule),
			severity: Refuse, data: D{"modules": []string{"virt"}, "reasons": []string{"calls_not_run_yet"}}, action: ActionNone, title: "module"},
		{name: "a module with no section for an upgrade", setup: func(f *fakeProbe) {
			f.set(modulesDir+"/virt.json", withoutUpgrade(virtModule))
		}, severity: Refuse, data: D{"modules": []string{"virt"}, "reasons": []string{"no_os_upgrade"}}},
		{name: "a manifest that is not json", setup: module("virt.json", "not json"), severity: Refuse, data: D{"invalid": []string{"virt"}, "reasons": []string{"invalid"}}},
		{name: "a manifest whose name is another", setup: module("other.json", virtModule), severity: Refuse, data: D{"invalid": []string{"other"}, "reasons": []string{"invalid"}}},
		{name: "a file whose name cannot be repeated", setup: module("Bad Name.json", virtModule), severity: Refuse, data: D{"invalid": []string{"unnamed"}, "reasons": []string{"invalid"}}},
		{name: "a manifest that is a link", setup: func(f *fakeProbe) {
			f.set("/srv/x.json", virtModule)
			f.link(modulesDir+"/virt.json", "/srv/x.json")
		}, severity: Refuse, data: D{"invalid": []string{"virt"}, "reasons": []string{"invalid"}}},
		{name: "a manifest that is larger than the bound", setup: module("virt.json", virtModule+strings.Repeat(" ", 64<<10)), severity: Refuse, data: D{"invalid": []string{"virt"}}},
		{name: "a manifest that cannot be read", setup: func(f *fakeProbe) {
			f.set(modulesDir+"/virt.json", virtModule)
			f.fail(modulesDir+"/virt.json", fs.ErrPermission)
		}, severity: Refuse, data: D{"invalid": []string{"virt"}, "reasons": []string{"invalid"}}},
		{name: "a folder that cannot be read", setup: func(f *fakeProbe) { f.fail(modulesDir, fs.ErrPermission) }, severity: Refuse, unknown: ReasonUnreadable},
		{name: "two modules, sorted, and why", setup: func(f *fakeProbe) {
			f.set(modulesDir+"/virt.json", virtModule)
			f.set(modulesDir+"/zeta.json", strings.NewReplacer(`"name":"virt"`, `"name":"zeta"`).Replace(withoutUpgrade(virtModule)))
		}, severity: Refuse, data: D{"modules": []string{"virt", "zeta"}, "reasons": []string{"calls_not_run_yet", "no_os_upgrade"}}},
		// what an install puts down besides the manifest (the lists, the version): a module whose install or remove stopped half way is still on the box
		{name: "a module that has its list of files and no manifest", setup: func(f *fakeProbe) { f.set(stateDir+"/virt.files", "casaos-module-files 1\n") },
			severity: Refuse, data: D{"modules": []string{}, "invalid": []string{"virt"}, "reasons": []string{"invalid"}}},
		{name: "a module that has its version and no manifest", setup: func(f *fakeProbe) { f.set(stateDir+"/virt.version", "v0.1.0\n") },
			severity: Refuse, data: D{"modules": []string{}, "invalid": []string{"virt"}, "reasons": []string{"invalid"}}},
		{name: "a module that has its list of directories and no manifest", setup: func(f *fakeProbe) { f.set(stateDir+"/virt.dirs", "") },
			severity: Refuse, data: D{"modules": []string{}, "invalid": []string{"virt"}, "reasons": []string{"invalid"}}},
		{name: "a module with its manifest and its lists is one module", setup: func(f *fakeProbe) {
			f.set(modulesDir+"/virt.json", virtModule)
			f.set(stateDir+"/virt.files", "casaos-module-files 1\n")
			f.set(stateDir+"/virt.version", "v0.1.0\n")
		}, severity: Refuse, data: D{"modules": []string{"virt"}, "reasons": []string{"calls_not_run_yet"}}},
		{name: "the lock and the temporary folder of the assistant are no module", setup: func(f *fakeProbe) {
			f.set(stateDir+"/.lock", "123\n")
			f.set(stateDir+"/tmp/archive.tar", "x")
		}, severity: OK},
		{name: "a folder of the lists that cannot be read", setup: func(f *fakeProbe) { f.fail(stateDir, fs.ErrPermission) }, severity: Refuse, unknown: ReasonUnreadable},
		{name: "more manifests than are read", setup: func(f *fakeProbe) {
			for i := 0; i < 33; i++ {
				f.set(modulesDir+"/m"+strings.Repeat("a", i%20+1)+string(rune('a'+i/20))+".json", virtModule)
			}
		}, severity: Refuse, unknown: ReasonTooLarge},
	})
}

// withoutUpgrade is the manifest of a module with no section for an upgrade.
func withoutUpgrade(text string) string {
	i := strings.Index(text, `,"os_upgrade":`)
	return text[:i] + "}"
}

// While the engine does not make the calls that a module declares, the check refuses a module that declares them; the day it does, that is the change that flips the constant and the
// scenario above that names it.
func TestTheEngineDoesNotMakeTheCallsOfAModuleYet(t *testing.T) {
	if engineRunsModuleCalls {
		t.Error("the engine makes the calls of a module: the check must accept a module that has declared them, and this test and the scenario with it must say so")
	}
}
