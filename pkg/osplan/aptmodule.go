package osplan

import (
	"regexp"
	"slices"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The simulation of installing the packages of a module: "apt-get -s install --no-install-recommends <packages>", and the same transaction with --print-uris for the sizes (a simulation
// prints none). They run in the same sandbox as the plan of the upgrade, in English, with the same environment (SandboxEnv) and the same discipline: the commands are built here,
// compared by equality, and nothing else is handed to the sandbox. The difference is the lists: the install that follows runs on the lists the box has, with no update, so the simulation reads
// them (and the sources) as they are, and only apt's cache is moved into the sandbox.
//
// The command that says the sizes must be one that cannot install. "-o APT::Get::Trivial-Only=true" is not: apt counts a transaction as trivial when the number of packages it changes is the
// number of names it was given, and goes on with it (a module of one package was installed, with its maintainer scripts, by the plan: testdata/realmodule/d11-trivial-trivialonly.txt). With
// --print-uris apt says what it would download and use, prints the addresses, and ends with status 0 before it fetches or installs anything (testdata/realmodule/capture.sh runs it
// for every case on apt 2.2.4 and apt 2.6.1 and finds the status of dpkg, its history and the log of the planner as they were).

// moduleMaxPackages is the most packages of a module (the manifest has 40 at most).
const moduleMaxPackages = 40

// moduleOptions are the options of both commands, in this order: the cache of apt in the sandbox directory, no locking, no log of the planner, and the list of the packages that are
// installed as dependencies in a file of the sandbox's. The log is what the file that APT_CONFIG names says too (AptConfigText), and is said again here because the planner writes
// /var/log/apt/eipp.log.xz, as root, for every "-s install" that is not told otherwise (apt 2.2.4 and 2.6.1), and that log is the one of the last real transaction of the box. The list
// is the one apt rewrites, as root and for a simulation as well, when it is asked to install a package that is installed as a dependency: it marks it as installed by hand, says so and
// writes /var/lib/apt/extended_states (testdata/realmodule/capture.sh, the cases "auto" and "auto-marked").
func moduleOptions(dir string) []string {
	return []string{"-o", "Dir::Cache=" + dir + "/cache", "-o", "Debug::NoLocking=true", "-o", "Dir::Log::Planner=", "-o", "Dir::State::extended_states=" + ModuleStatesFile(dir)}
}

// ModuleStatesFile is the file in the sandbox directory dir that the commands of a module are told to keep apt's list of the automatically installed packages in. It is made empty before they
// run: every package of the sandbox is installed by hand, and none is marked, so that nothing is written but in the sandbox.
func ModuleStatesFile(dir string) string { return dir + "/extended_states" }

// moduleHead is the command up to the packages: the verb and its options, all fixed.
func moduleHead(dir string, estimate bool) []string {
	tail := []string{"-s", "install", "--no-install-recommends"}
	if estimate {
		tail = []string{"--print-uris", "install", "--no-install-recommends"}
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

// ModuleEstimateArgv is the same transaction with --print-uris: apt says what it downloads and what it uses, prints the addresses of the archives and ends, installing nothing and fetching
// nothing. It is how the sizes are read (ParseURIEstimate).
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
	// the line apt says, before the summary, for a package that is installed as a dependency and that it is asked to install: it is installed by hand from then on. It says nothing of
	// the transaction.
	markedByHand = regexp.MustCompile(`^[a-z0-9][a-z0-9+.:~-]* set to manually installed\.$`)
)

// heldError is what apt says, and stops at, when a package is held and it was told to go on without being asked.
const heldError = "E: Held packages were changed and -y was used without --allow-change-held-packages."

// withoutInstallNotes takes out of the output of a simulation of an install what ParseSolver does not know, which an upgrade never prints, and gives the held packages that the output names
// and the packages that it says are already the newest version. A line it does not know stays in, for ParseSolver to refuse. Nothing is guessed: a block is its exact header and the lines
// indented under it.
func withoutInstallNotes(text string) (string, []string, []string, *Failure) {
	var kept, held, newest []string
	block := ""
	for _, l := range splitOutput(text) {
		switch {
		case block != "" && strings.HasPrefix(l, "  "):
			if block == heldHeader {
				for _, name := range strings.Fields(l) {
					if !packageName.MatchString(name) {
						return "", nil, nil, unparsedAt("bad_name", name)
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
			newest = append(newest, strings.Fields(l)[0])
			continue
		case markedByHand.MatchString(l):
			block = ""
			continue
		}
		block = ""
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n"), held, newest, nil
}

// unmetHeader starts the block in which apt names what it could not meet when the packages cannot be installed together ("pkg : Depends: other (>= 2) but 1.0 is to be installed"),
// where the last line it ends with ("you have held broken packages") says it for a conflict as well as for a hold.
const unmetHeader = "The following packages have unmet dependencies:"

// unmetDependencies are the lines of that block, as they may be shown: at most maxDetailLines of them, each printable ASCII, trimmed and cut.
func unmetDependencies(text string) []string {
	var out []string
	in := false
	for _, l := range splitOutput(text) {
		switch {
		case l == unmetHeader:
			in = true
		case in && strings.HasPrefix(l, " "):
			if s := sanitizeLine(l); s != "" && len(out) < maxDetailLines {
				out = append(out, s)
			}
		default:
			in = false
		}
	}
	return out
}

// ParseModuleSimulation reads the outputs of ModuleSimArgv (sim) and ModuleEstimateArgv (estimate) for the packages that were asked about, as what the plan of a module needs of apt: the
// packages to install and to upgrade, those it would remove or downgrade or finds held, and the sizes. It reads the simulation with ParseSolver, which it shares with the plan of the
// upgrade, and the sizes with ParseURIEstimate: anything that is not understood is a Failure of the result, never a partial answer. The sizes are Known only if the estimate is the same
// transaction as the simulation. The text of apt is English (SandboxEnv sets LC_ALL=C); a translated one is not understood and is a failure.
//
// Every package that was asked about must be in the answer under the name it was asked for: an Inst line of that name, or the line that says it is already the newest version. apt reads a
// name with a "." or a "+" in it as a regular expression when no package has the name (mod.lean selects modclean), and a name that a package provides as that package; the simulation of
// such a name is the simulation of something else, and is RefusedPackageNotExact.
func ParseModuleSimulation(sim, estimate Output, packages []string) osmodule.AptResult {
	failed := func(f *Failure) osmodule.AptResult {
		return osmodule.AptResult{Failure: osmodule.RefusedAptFailed, Detail: f.Error()}
	}
	if !packagesOK(packages) {
		return failed(failf(CodeInternal, nil, "a list of packages that apt was not asked about"))
	}
	if sim.ExitCode != 0 && slices.Contains(splitOutput(sim.Text), heldError) {
		return osmodule.AptResult{Failure: osmodule.RefusedHeld, Detail: heldError}
	}
	text, held, newest, f := withoutInstallNotes(sim.Text)
	if f != nil {
		return failed(f)
	}
	run, f := ParseSolver(Output{Text: text, ExitCode: sim.ExitCode, Truncated: sim.Truncated})
	if f != nil {
		res := failed(f)
		if unmet := unmetDependencies(sim.Text); sim.ExitCode != 0 && len(unmet) > 0 {
			res.Detail = "unmet dependencies: " + strings.Join(unmet, "; ") + " (" + f.Error() + ")"
		}
		return res
	}
	var named []string
	for _, in := range run.Inst {
		named = append(named, in.Name)
	}
	named = append(named, newest...)
	if missing := slices.DeleteFunc(slices.Clone(packages), func(p string) bool { return slices.Contains(named, p) }); len(missing) > 0 {
		return osmodule.AptResult{Failure: osmodule.RefusedPackageNotExact, Detail: "package_not_exact: " + strings.Join(missing, " ")}
	}
	sorted := func(in []string) []string { s := slices.Clone(in); slices.Sort(s); return s }
	res := osmodule.AptResult{
		ToInstall: sorted(run.New), ToUpgrade: sorted(run.Upgraded), Removes: sorted(run.Removals()), Downgrades: sorted(run.Downgraded), Held: sorted(held),
	}
	if est := ParseURIEstimate(estimate, run.Summary); est.Known {
		res.Known, res.DownloadBytes, res.ExtraBytes = true, *est.DownloadBytes, *est.ExtraBytes
	}
	return res
}
