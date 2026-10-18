package modulecmd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
)

// apt says "you have held broken packages" for a package that is held and for one that is not installable at all. A plan is refused for a held package only when one is named in the
// block of unmet dependencies.

func readApt(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "apt", dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

const dpkgStatusSample = `Package: libc6
Status: install ok installed
Priority: required
Version: 2.36-9+deb12u7
Description: GNU C Library: Shared libraries
 Also contains the "hold ok installed" of something that is not a status.

Package: casaos-mod-b
Status: hold ok installed
Architecture: amd64
Version: 0.9

Package: libssl3
Architecture: amd64
Status: hold ok installed
Multi-Arch: same

Package: gone
Status: deinstall ok config-files
Version: 1

Package: waiting
Status: hold ok not-installed

Package: held-but-in-the-description
Status: install ok installed
Description: Status: hold ok installed
 Status: hold ok installed
`

func TestTheHeldPackagesOfTheBoxAreThoseDpkgHolds(t *testing.T) {
	f := newFixture(t)
	bx := &box{ctx: context.Background(), env: &f.env}
	if held, err := bx.heldPackages(); err == nil {
		t.Errorf("no status file: %v", held)
	}
	f.writeFile("/var/lib/dpkg/status", dpkgStatusSample)
	held, err := bx.heldPackages()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(held)
	if want := []string{"casaos-mod-b", "libssl3", "waiting"}; !slices.Equal(held, want) {
		t.Errorf("held: %q, want %q", held, want)
	}
	f.writeFile("/var/lib/dpkg/status", "")
	if held, err := bx.heldPackages(); err != nil || len(held) != 0 {
		t.Errorf("an empty status file: %q %v", held, err)
	}
	// a status file that is too large is not read, and nor is one with a line that no status file has
	f.writeFile("/var/lib/dpkg/status", dpkgStatusSample)
	if held, err := bx.heldPackagesWithin(int64(len(dpkgStatusSample)) - 1); err == nil {
		t.Errorf("a status file that is over the limit: %q", held)
	}
	if held, err := bx.heldPackagesWithin(int64(len(dpkgStatusSample))); err != nil || len(held) != 3 {
		t.Errorf("a status file that is at the limit: %q %v", held, err)
	}
	f.writeFile("/var/lib/dpkg/status", "Package: x\nStatus: hold ok installed\nDescription: "+strings.Repeat("d", 2<<20)+"\n")
	if held, err := bx.heldPackages(); err == nil {
		t.Errorf("a line of 2 MiB: %q", held)
	}
	// a directory in its place
	_ = os.Remove(f.path("/var/lib/dpkg/status"))
	if err := os.MkdirAll(f.path("/var/lib/dpkg/status"), 0o755); err != nil {
		t.Fatal(err)
	}
	if held, err := bx.heldPackages(); err == nil {
		t.Errorf("a directory: %q", held)
	}
}

func progressSim(unmet string) string {
	return aptProgress + "Some packages could not be installed. This may mean that you have\nrequested an impossible situation or if you are using the unstable\n" +
		"distribution that some required packages have not yet been created\nor been moved out of Incoming.\nThe following information may help to resolve the situation:\n\n" +
		"The following packages have unmet dependencies:\n " + unmet + "\nE: Unable to correct problems, you have held broken packages.\n"
}

func TestAHeldPackageIsNamedInTheBlockOfUnmetDependenciesOrItIsNotTheReason(t *testing.T) {
	unmetHeld := progressSim("rvm-needs : Depends: rvm-lib (>= 2) but 1.0 is to be installed")
	unmetMissing := progressSim("rvm-bad : Depends: rvm-nothere but it is not installable")
	failed := osmodule.AptResult{Failure: osmodule.RefusedAptFailed, Detail: "unmet dependencies: x (plan_unparsed: you have held broken packages)"}
	for name, c := range map[string]struct {
		text string
		held []string
		want string
	}{
		"what is needed is held":                                {unmetHeld, []string{"rvm-lib"}, osmodule.RefusedHeld},
		"the package itself is held":                            {unmetHeld, []string{"rvm-needs"}, osmodule.RefusedHeld},
		"another package is held":                               {unmetHeld, []string{"other"}, osmodule.RefusedAptFailed},
		"nothing is held":                                       {unmetHeld, nil, osmodule.RefusedAptFailed},
		"a dependency that is not installable":                  {unmetMissing, nil, osmodule.RefusedAptFailed},
		"a dependency that is not installable, holds elsewhere": {unmetMissing, []string{"rvm-lib", "libc6"}, osmodule.RefusedAptFailed},
		"a held package whose name is part of another":          {unmetHeld, []string{"rvm", "rvm-li", "lib", "rvm-lib2"}, osmodule.RefusedAptFailed},
		"a held package called like a word of apt":              {unmetHeld, []string{"but", "is", "to", "be", "installed", "depends"}, osmodule.RefusedAptFailed},
		"a held package named by its architecture":              {progressSim("rvm-needs : Depends: libc6:i386 (>= 2) but 1.0 is to be installed"), []string{"libc6"}, osmodule.RefusedHeld},
		"a name on a line of the block that goes on":            {progressSim("rvm-needs : Depends: a but it is not installable or\n                  rvm-lib but 1 is to be installed"), []string{"rvm-lib"}, osmodule.RefusedHeld},
	} {
		got := heldFromSolver(failed, c.text, c.held)
		if got.Failure != c.want {
			t.Errorf("%s: %q, want %q", name, got.Failure, c.want)
		}
		if got.Detail != failed.Detail {
			t.Errorf("%s: the detail is %q", name, got.Detail)
		}
	}
	// what is not "you have held broken packages" is not looked at, and what is not a failure is as it is
	other := osmodule.AptResult{Failure: osmodule.RefusedAptFailed, Detail: "unmet dependencies: x"}
	if got := heldFromSolver(other, unmetHeld, []string{"rvm-lib"}); got.Failure != osmodule.RefusedAptFailed {
		t.Errorf("a failure that is not about a held package: %q", got.Failure)
	}
	ok := osmodule.AptResult{ToInstall: []string{"a"}}
	if got := heldFromSolver(ok, unmetHeld, []string{"rvm-lib"}); got.Failure != "" || !slices.Equal(got.ToInstall, ok.ToInstall) {
		t.Errorf("an answer: %+v", got)
	}
	notExact := osmodule.AptResult{Failure: osmodule.RefusedPackageNotExact, Detail: "package_not_exact: x you have held broken packages"}
	if got := heldFromSolver(notExact, unmetHeld, []string{"rvm-lib"}); got.Failure != osmodule.RefusedPackageNotExact {
		t.Errorf("another failure: %q", got.Failure)
	}
}

// through the plan: the output of the real apt, and what the box has held
func TestAPlanIsRefusedForAHeldPackageOnlyWhenOneIsHeld(t *testing.T) {
	for name, c := range map[string]struct {
		status string
		want   string
	}{
		"the package the other needs is held": {"Package: casaos-mod-b\nStatus: hold ok installed\n", osmodule.RefusedHeld},
		"nothing is held":                     {"Package: casaos-mod-b\nStatus: install ok installed\n", osmodule.RefusedAptFailed},
		"another package is held":             {"Package: libc6\nStatus: hold ok installed\n", osmodule.RefusedAptFailed},
		"the status cannot be read":           {"", osmodule.RefusedAptFailed},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.sandbox.sim = osplan.Output{Text: readApt(t, "debian12", "held.sim.txt"), ExitCode: 100}
			f.sandbox.est = osplan.Output{Text: readApt(t, "debian12", "held.est.txt"), ExitCode: 100}
			if c.status != "" {
				f.writeFile("/var/lib/dpkg/status", c.status)
			}
			plan := f.json("plan", "virt")
			if plan["refused"] != c.want {
				t.Fatalf("refused: %v, want %s", plan["refused"], c.want)
			}
			detail, _ := checkOf(plan, osmodule.CheckApt)["detail"].(string)
			if !strings.Contains(detail, "casaos-mod-a : Depends: casaos-mod-b") {
				t.Errorf("the check does not say what could not be met: %q", detail)
			}
		})
	}
}
