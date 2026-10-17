package modulecmd

import (
	"os"
	"strings"
	"testing"
)

// a remove has the same one-job-at-a-time rule as an install, and does not touch the log of the job that runs
func TestARemoveWhileAnotherRunsIsRefusedBusy(t *testing.T) {
	f := installed(t)
	release, err := f.host.Host.Lock(f.env.Roots.lock())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	f.writeFile("/var/log/casaos/module-virt.log", "the log of the job that runs\n")
	before := f.treeWithoutBookkeeping()
	if code := f.remove("--yes"); code != ExitRefused || f.out.String() != "refused: busy\n" {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if f.read("/var/log/casaos/module-virt.log") != "the log of the job that runs\n" {
		t.Error("the log of the job that runs was touched")
	}
	if !f.runner.isEnabled(testUnit) {
		t.Error("the unit was stopped")
	}
}

// the version of an installed module is what the installer wrote, or nothing
func TestTheVersionOfAnInstalledModuleIsATagOrNull(t *testing.T) {
	f := installed(t)
	for text, want := range map[string]any{
		"v0.1.0\n": "v0.1.0", "v12.34.56": "v12.34.56", "": nil, "v0.1": nil, "0.1.0": nil, "v0.1.0 && reboot": nil, "<script>": nil, "v0.1.0\nv0.2.0": nil, strings.Repeat("v", 100): nil,
	} {
		f.writeFile("/var/lib/casaos/modules/virt.version", text)
		m := f.json("list")["modules"].([]any)[0].(map[string]any)
		got := m["installed"].(map[string]any)["version"]
		if got != want {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
	_ = os.Remove(f.path("/var/lib/casaos/modules/virt.version"))
	if m := f.json("list")["modules"].([]any)[0].(map[string]any); m["installed"].(map[string]any)["version"] != nil {
		t.Errorf("no file: %v", m["installed"])
	}
}
