package modulecmd

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
)

// checkScenario says what the plan must make of what the real apt says in each scenario of the chroots (see realapt_linux_test.go).
func checkScenario(t *testing.T, scenario string, res osmodule.AptResult) {
	t.Helper()
	switch scenario {
	case "fresh":
		if res.Failure != "" || !slices.Equal(res.ToInstall, []string{"casaos-mod-a", "casaos-mod-b"}) || len(res.ToUpgrade) != 0 || !res.Known || res.DownloadBytes <= 0 || res.ExtraBytes <= 0 || len(res.Held)+len(res.Removes)+len(res.Downgrades) != 0 {
			t.Errorf("%+v", res)
		}
	case "upgrade":
		if res.Failure != "" || !slices.Equal(res.ToInstall, []string{"casaos-mod-a"}) || !slices.Equal(res.ToUpgrade, []string{"casaos-mod-b"}) || !res.Known {
			t.Errorf("%+v", res)
		}
	case "held":
		// the module asks for a package that needs a held one changed
		if res.Failure != osmodule.RefusedHeld {
			t.Errorf("a held package is not seen: %+v", res)
		}
	case "held-named":
		// the module asks for the held package itself
		if res.Failure == "" && !slices.Contains(res.Held, "casaos-mod-b") || res.Failure != "" && res.Failure != osmodule.RefusedHeld {
			t.Errorf("a held package is not seen: %+v", res)
		}
	case "removal":
		if res.Failure != "" || !slices.Equal(res.Removes, []string{"casaos-mod-e"}) {
			t.Errorf("%+v", res)
		}
	case "missing":
		if res.Failure != osmodule.RefusedAptFailed || res.Detail == "" {
			t.Errorf("%+v", res)
		}
	case "nothing":
		if res.Failure != "" || len(res.ToInstall) != 0 || len(res.ToUpgrade) != 0 || !res.Known || res.DownloadBytes != 0 || res.ExtraBytes != 0 {
			t.Errorf("%+v", res)
		}
	}
}

var scenarios = []string{"fresh", "upgrade", "held", "held-named", "removal", "missing", "nothing"}

// the packages that each scenario asks about
var scenarioPackages = map[string][]string{
	"fresh": {"casaos-mod-a"}, "upgrade": {"casaos-mod-a"}, "held": {"casaos-mod-a"}, "held-named": {"casaos-mod-b"}, "removal": {"casaos-mod-d"}, "missing": {"casaos-mod-zzz"}, "nothing": {"casaos-mod-a"},
}

// the outputs of the real apt, as the chroots of Debian 11 (apt 2.2.4) and Debian 12 (apt 2.6.1) gave them, read as the plan reads them
func TestTheRealOutputsOfAptAreUnderstood(t *testing.T) {
	for _, chroot := range []string{"debian11", "debian12"} {
		for _, scenario := range scenarios {
			t.Run(chroot+"/"+scenario, func(t *testing.T) {
				read := func(name string) osplan.Output {
					text, err := os.ReadFile(filepath.Join("testdata", "apt", chroot, scenario+"."+name+".txt"))
					if err != nil {
						t.Fatal(err)
					}
					rc, err := os.ReadFile(filepath.Join("testdata", "apt", chroot, scenario+"."+name+".rc"))
					if err != nil {
						t.Fatal(err)
					}
					code, err := strconv.Atoi(strings.TrimSpace(string(rc)))
					if err != nil {
						t.Fatal(err)
					}
					return osplan.Output{Text: strings.ReplaceAll(string(text), "\r\n", "\n"), ExitCode: code}
				}
				checkScenario(t, scenario, heldFromSolver(osplan.ParseModuleSimulation(read("sim"), read("est"), scenarioPackages[scenario])))
			})
		}
	}
}

// a held package that a dependency needs changed refuses the plan with the code of a held package, from the words of the real apt
func TestAHeldDependencyRefusesThePlan(t *testing.T) {
	read := func(name string) string {
		text, err := os.ReadFile(filepath.Join("testdata", "apt", "debian12", name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(text), "\r\n", "\n")
	}
	f := newFixture(t)
	f.sandbox.sim = osplan.Output{Text: read("held.sim.txt"), ExitCode: 100}
	f.sandbox.est = osplan.Output{Text: read("held.est.txt"), ExitCode: 100}
	before := f.treeWithoutBookkeeping()
	if code := f.install("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+osmodule.RefusedHeld+"\n") {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
}
