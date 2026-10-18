package modulecmd

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
)

// What the box answers, as the plan reads it (the contract of osmodule.Box, which the plan's own tests state for a stand-in): the real box is held to the same three points.

func TestTheBoxNamesTheModulesByTheirNameAndNotByTheirFile(t *testing.T) {
	f := newFixture(t)
	f.writeFile("/etc/casaos/modules/virt.json", manifestJSON(t, testManifest()))
	f.writeFile("/etc/casaos/modules/broken.json", "{")
	f.writeFile("/etc/casaos/modules/README", "not a manifest")
	f.writeFile("/etc/casaos/modules/virt.json.bak", "x")
	bx := &box{ctx: context.Background(), env: &f.env}
	installed, broken, err := bx.InstalledManifests()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range installed {
		names = append(names, name)
	}
	if !slices.Equal(names, []string{"virt"}) || installed["virt"].Unit != testUnit {
		t.Errorf("installed: %v", names)
	}
	if !slices.Equal(broken, []string{"broken"}) {
		t.Errorf("broken: %q", broken)
	}
	// and so the plan sees a module whose manifest does not read as installed under its name
	f.writeFile("/etc/casaos/modules/virt.json", "{")
	_, broken, _ = bx.InstalledManifests()
	if !slices.Contains(broken, "virt") {
		t.Errorf("broken: %q", broken)
	}
	if got := f.json("plan", "virt")["refused"]; got != osmodule.CheckInstalled {
		t.Errorf("a broken manifest of the module: refused %v", got)
	}
}

func TestTheBoxGivesOneSpellingForEachFileOfAUnit(t *testing.T) {
	f := newFixture(t)
	bx := &box{ctx: context.Background(), env: &f.env}
	usr, lib, etc := "/usr/lib/systemd/system/"+testUnit, "/lib/systemd/system/"+testUnit, "/etc/systemd/system/"+testUnit
	if got, err := bx.UnitFiles(testUnit); err != nil || len(got) != 0 {
		t.Errorf("no unit: %v %v", got, err)
	}
	f.writeFile(usr, "[Unit]\n")
	if got, _ := bx.UnitFiles(testUnit); !slices.Equal(got, []string{usr}) {
		t.Errorf("one file: %v", got)
	}
	// on a system whose /lib is a directory of its own the other one is another file, and it has its own spelling
	f.writeFile(lib, "[Unit]\n")
	f.writeFile(etc, "[Unit]\n")
	if got, _ := bx.UnitFiles(testUnit); !slices.Equal(got, []string{usr, lib, etc}) {
		t.Errorf("three files: %v", got)
	}
	// a file of another name is not the unit
	f.writeFile("/usr/lib/systemd/system/other.service", "[Unit]\n")
	if got, _ := bx.UnitFiles(testUnit); len(got) != 3 {
		t.Errorf("another unit: %v", got)
	}
}

// the oldest release of ReCasaOS is compared with the version of the distribution, which is what the release file of the installer says, and not with the version of the core
func TestTheVersionTheAssistantRunsWithIsThatOfTheDistribution(t *testing.T) {
	f := newFixture(t)
	f.env.Version = "v0.5.29"
	bx := &box{ctx: context.Background(), env: &f.env}
	if bx.ReCasaOSVersion() != "v0.5.29" {
		t.Errorf("the version: %s", bx.ReCasaOSVersion())
	}
	if prod := Production(); prod.Version == common.VERSION {
		t.Errorf("production runs with the version of the core, %q", common.VERSION)
	}
}

// a package that apt would install under another name is a plan for something else
func TestAPackageThatAptDoesNotInstallUnderItsOwnNameRefusesThePlan(t *testing.T) {
	f := newFixture(t)
	f.sandbox.sim.Text = strings.ReplaceAll(aptSim, "qemu-utils", "qemu-utils-extra")
	f.sandbox.est.Text = strings.ReplaceAll(aptEst, "qemu-utils", "qemu-utils-extra")
	before := f.treeWithoutBookkeeping()
	if code := f.install("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+osmodule.RefusedPackageNotExact+"\n") {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if f.runner.called("apt-get", "install") != 0 {
		t.Error("apt installed something")
	}
	plan := f.json("plan", "virt")
	if plan["refused"] != osmodule.RefusedPackageNotExact || plan["plan_id"] != "" {
		t.Errorf("the plan: refused %v, id %v", plan["refused"], plan["plan_id"])
	}
}

// the sandbox is asked the two commands of the module, each with the log of the planner turned off, and the configuration it is given turns it off too
func TestTheSimulationAsksAptTheTwoCommandsOfAModuleWithTheLogOfThePlannerOff(t *testing.T) {
	f := newFixture(t)
	if got := f.json("plan", "virt"); got["refused"] != nil {
		t.Fatalf("refused: %v", got["refused"])
	}
	if len(f.sandbox.argvs) != 2 {
		t.Fatalf("the commands: %q", f.sandbox.argvs)
	}
	for _, argv := range f.sandbox.argvs {
		if !osplan.AllowedModuleArgv(argv, f.sandbox.dir) || !slices.Contains(argv, "Dir::Log::Planner=") {
			t.Errorf("a command with the log of the planner on, or one that is not a command of a module: %q", argv)
		}
	}
	if !slices.Contains(f.sandbox.argvs[0], "-s") || !slices.Contains(f.sandbox.argvs[1], "--print-uris") || slices.Contains(f.sandbox.argvs[1], "-s") {
		t.Errorf("the simulation and the estimate: %q", f.sandbox.argvs)
	}
	for _, argv := range f.sandbox.argvs {
		for _, a := range argv {
			if strings.Contains(a, "Trivial-Only") {
				t.Errorf("an estimate that installs: %q", argv)
			}
		}
	}
	// the configuration the sandbox is given has the log of the planner off as well: the only thing that stops it for every command
	if cfg := f.sandbox.content[f.sandbox.dir+"/apt-config"]; !strings.Contains(cfg, `Dir::Log::Planner "";`) {
		t.Errorf("the configuration of the sandbox: %q", cfg)
	}
}
