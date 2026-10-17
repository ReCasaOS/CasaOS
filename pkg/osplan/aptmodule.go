package osplan

import (
	"regexp"
	"slices"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The simulation of installing the packages of a module: "apt-get -s install --no-install-recommends <packages>", and the same transaction with the trivial-only option for the sizes (a
// simulation prints none). They run in the same sandbox as the plan of the upgrade, in English, with the same environment (SandboxEnv) and the same discipline: the commands are built here,
// compared by equality, and nothing else is handed to the sandbox. The difference is the lists: the install that follows runs on the lists the box has, with no update, so the simulation reads
// them (and the sources) as they are, and only apt's cache is moved into the sandbox.

// moduleMaxPackages is the most packages of a module (the manifest has 40 at most).
const moduleMaxPackages = 40

func moduleOptions(dir string) []string {
	return []string{"-o", "Dir::Cache=" + dir + "/cache", "-o", "Debug::NoLocking=true"}
}

// moduleHead is the command up to the packages: the verb and its options, all fixed.
func moduleHead(dir string, estimate bool) []string {
	tail := []string{"-s", "install", "--no-install-recommends"}
	if estimate {
		tail = []string{"-o", "APT::Get::Trivial-Only=true", "install", "--no-install-recommends"}
	}
	return slices.Concat([]string{"apt-get"}, moduleOptions(dir), tail)
}

// packagesOK says whether a list of packages may be handed to apt: some, not more than moduleMaxPackages, none twice, each a name that apt reads as a package and as nothing else (not an
// option, not an order to remove one, not a version, not a path).
func packagesOK(packages []string) bool {
	if len(packages) == 0 || len(packages) > moduleMaxPackages {
		return false
	}
	for i, p := range packages {
		if !osmodule.ValidPackageName(p) || slices.Index(packages, p) != i {
			return false
		}
	}
	return true
}

func moduleArgv(dir string, estimate bool, packages []string) ([]string, error) {
	if !ValidSandboxDir(dir) {
		return nil, failf(CodeInternal, nil, "not a sandbox directory")
	}
	if !packagesOK(packages) {
		return nil, failf(CodeInternal, nil, "a list of packages that apt is not asked about")
	}
	return slices.Concat(moduleHead(dir, estimate), packages), nil
}

// ModuleSimArgv is "apt-get -s install --no-install-recommends" of the packages, in the sandbox directory dir.
func ModuleSimArgv(dir string, packages []string) ([]string, error) {
	return moduleArgv(dir, false, packages)
}

// ModuleEstimateArgv is the same transaction, not simulated, that apt stops at the question of whether to go on, after it has said what it downloads and what it uses: it is how the sizes
// are read (the sandbox's apt-get with Trivial-Only, as EstimateArgv is for the upgrade).
func ModuleEstimateArgv(dir string, packages []string) ([]string, error) {
	return moduleArgv(dir, true, packages)
}

// AllowedModuleArgv says whether argv is one of the two commands of a module for the sandbox directory dir, and nothing else: the head is pinned word for word, and every word after it is a
// package that is valid. AllowedSandboxArgv does not take these, nor this the commands of the plan of the upgrade.
func AllowedModuleArgv(argv []string, dir string) bool {
	if !ValidSandboxDir(dir) {
		return false
	}
	for _, estimate := range []bool{false, true} {
		head := moduleHead(dir, estimate)
		if len(argv) > len(head) && slices.Equal(argv[:len(head)], head) {
			return packagesOK(argv[len(head):])
		}
	}
	return false
}

var (
	// what apt prints on top of the lists of what it does when it installs, and that an upgrade does not: the dependencies that come with the packages (they are in the list of the new ones
	// again), what is suggested and what is recommended. Their names are not read: they are not what happens.
	installNotes = []string{"The following additional packages will be installed:", "Suggested packages:", "Recommended packages:"}
	// the held packages that apt would change; the names are read
	heldHeader    = "The following held packages will be changed:"
	alreadyNewest = regexp.MustCompile(`^[a-z0-9][a-z0-9+.:~-]* is already the newest version \([^()]+\)\.$`)
)

// heldError is what apt says, and stops at, when a package is held and it was told to go on without being asked.
const heldError = "E: Held packages were changed and -y was used without --allow-change-held-packages."

// withoutInstallNotes takes out of the output of a simulation of an install what ParseSolver does not know, which an upgrade never prints, and gives the held packages that the output names.
// A line it does not know stays in, for ParseSolver to refuse. Nothing is guessed: a block is its exact header and the lines indented under it.
func withoutInstallNotes(text string) (string, []string, *Failure) {
	var kept, held []string
	block := ""
	for _, l := range splitOutput(text) {
		switch {
		case block != "" && strings.HasPrefix(l, "  "):
			if block == heldHeader {
				for _, name := range strings.Fields(l) {
					if !packageName.MatchString(name) {
						return "", nil, unparsedAt("bad_name", name)
					}
					held = append(held, name)
				}
			}
			continue
		case l == heldHeader || slices.Contains(installNotes, l):
			block = l
			continue
		case alreadyNewest.MatchString(l):
			block = ""
			continue
		}
		block = ""
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n"), held, nil
}

// ParseModuleSimulation reads the outputs of ModuleSimArgv (sim) and ModuleEstimateArgv (estimate), run in the sandbox directory dir, as what the plan of a module needs of apt: the packages
// to install and to upgrade, those it would remove or downgrade or finds held, and the sizes. It reads the simulation with ParseSolver, which it shares with the plan of the upgrade, and the
// sizes with ParseEstimate: anything that is not understood is a Failure of the result, never a partial answer. The sizes are Known only if the estimate is the same transaction as the
// simulation. The text of apt is English (SandboxEnv sets LC_ALL=C); a translated one is not understood and is a failure.
func ParseModuleSimulation(sim, estimate Output, dir string) osmodule.AptResult {
	failed := func(f *Failure) osmodule.AptResult {
		return osmodule.AptResult{Failure: osmodule.RefusedAptFailed, Detail: f.Error()}
	}
	if sim.ExitCode != 0 && slices.Contains(splitOutput(sim.Text), heldError) {
		return osmodule.AptResult{Failure: osmodule.RefusedHeld, Detail: heldError}
	}
	text, held, f := withoutInstallNotes(sim.Text)
	if f != nil {
		return failed(f)
	}
	run, f := ParseSolver(Output{Text: text, ExitCode: sim.ExitCode, Truncated: sim.Truncated})
	if f != nil {
		return failed(f)
	}
	sorted := func(in []string) []string { s := slices.Clone(in); slices.Sort(s); return s }
	res := osmodule.AptResult{
		ToInstall: sorted(run.New), ToUpgrade: sorted(run.Upgraded), Removes: sorted(run.Removals()), Downgrades: sorted(run.Downgraded), Held: sorted(held),
	}
	if est := ParseEstimate(estimate, run.Summary, dir); est.Known {
		res.Known, res.DownloadBytes, res.ExtraBytes = true, *est.DownloadBytes, *est.ExtraBytes
	}
	return res
}
