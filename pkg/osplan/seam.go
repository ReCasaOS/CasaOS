// Package osplan answers, for the machine it runs on, "if I upgraded now, what exactly would happen?", and changes
// nothing of the machine to say it. It rewrites the box's apt sources to the next release in a temporary tree, has apt
// plan the upgrade against that tree in a sandbox (the real mirror metadata, a simulated transaction), parses and
// judges what apt said (what is removed, what is kept back, which protected packages are touched, how much disk and
// download it takes), gives the plan an id, and writes from it the runbook for doing the hop by hand.
//
// The package is pure: it decides every rule and touches nothing. It sees the machine through two seams, each as small
// as it can be (seam.go): Files, which only reads and is the Probe of the readiness report itself, and Sandbox, which is
// the only thing that writes and runs, and runs only what AllowedSandboxArgv accepts. The service that wires them to the
// machine (service/) adds the job, the clock and the HTTP shape; it decides nothing that is a rule.
//
// What this rung does not do: run the hop (that is the button of the next rungs), plan any hop but Debian 11 to 12
// (planHops is the one table of them), read deb822 (.sources) files, or keep a plan anywhere but in memory.
package osplan

import (
	"context"
	"io/fs"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// Files is what the plan reads of the machine: the Probe of the readiness report (ReadFile, ReadDir, Lstat, FreeBytes,
// Now, and the few reading commands its allow-list has), and no second way to read the box. The plan runs its commands
// through osreadiness.Guard, so the allow-list of the report holds for it too.
type Files = osreadiness.Probe

// Output is what a command printed (its standard output and its error output, in English, cut to MaxOutputBytes), and
// how it ended. A command that ran and failed is not an error: it is an ExitCode that is not 0.
type Output = osreadiness.Output

// Sandbox is the only thing the plan writes with and runs commands with. Its production implementation lives in
// service/. It owes the rules below, and the fake of the tests records that they are kept.
type Sandbox interface {
	// MakeDir makes the private directory of this plan (owned by the user the core runs as; os.MkdirTemp makes it 0700, and it
	// is opened to be searched, 0711, and no more: apt drops to its own user, _apt, to fetch, and that user has to walk down to
	// the directories apt gives it, which the measurement of the hop did with 0755) under SandboxParent, named SandboxPrefix
	// and an alphanumeric suffix, and gives its path and the function that removes it.
	// remove is called once, on every exit (success, failure, timeout, cancellation, panic), and is safe to call more than
	// once. It removes the directory only after its Lstat says it is a directory (not a symbolic link) whose name matches.
	MakeDir() (path string, remove func(), err error)
	// WriteFile writes a file inside the directory MakeDir made, and makes the directories above it (mode 0711, as the
	// directory itself is) when they are not there. A mode with fs.ModeDir set makes the directory with the permissions of
	// the mode (the sticky bit too), whatever the umask is, and content is ignored. It refuses a path that is not inside that
	// directory, one that goes through a link, and a file that is there already.
	WriteFile(path string, content []byte, mode fs.FileMode) error
	// Run runs argv, which the caller has checked with AllowedSandboxArgv and which the implementation checks again: no
	// shell, the standard input from /dev/null, the environment SandboxEnv(dir) and nothing else, in a process group that
	// is killed as a group when ctx ends, its output bounded to MaxOutputBytes (Output.Truncated says it was cut). A
	// command that ends with a status is an Output; one that could not be started, or was killed with ctx, is an error.
	Run(ctx context.Context, argv []string) (Output, error)
}

// The sandbox directory, where it is, and how it is named: the name pattern is strict and part of the allow-list.
const (
	// SandboxParent is the directory the sandbox directories are made in.
	SandboxParent = "/var/tmp"
	// SandboxPrefix starts the name of a sandbox directory.
	SandboxPrefix = "casaos-os-plan."
	// StaleAfter is how old a sandbox directory is before the core, at its start, removes it as left behind by a core that
	// did not get to remove it.
	StaleAfter = 10 * time.Minute
)

// The bounds of a plan.
const (
	// MaxOutputBytes is the most of a command's output that is kept (8 MiB): more is a plan that cannot be parsed.
	MaxOutputBytes = 8 << 20
	// MinFreeBytes is what /var/tmp must have free to make a plan: the measured sandbox of the Debian 11 to 12 plan was
	// 214 MiB (the lists of the target, with deb-src). A decision, to be re-tared by the CI numbers.
	MinFreeBytes = 640 << 20
	// PlanLifetime is how long a plan is good for, from the time it was made.
	PlanLifetime = 24 * time.Hour
)

// The phases of a plan, in order: each is a named phase in the job's state and a time in the result.
const (
	PhaseSources     = "sources"
	PhaseUpdate      = "update"
	PhaseUpgrade     = "upgrade"
	PhaseDistUpgrade = "dist-upgrade"
	PhaseEstimate    = "estimate"
	PhaseParse       = "parse"
)

// Phases are the phases of a plan, in order.
var Phases = []string{PhaseSources, PhaseUpdate, PhaseUpgrade, PhaseDistUpgrade, PhaseEstimate, PhaseParse}

// How long each command of the sandbox has: it is killed as a group at the end of its time.
const (
	UpdateTimeout = 10 * time.Minute
	SolverTimeout = 2 * time.Minute
)

// Timeouts are the times the commands have; the zero value is what the service uses, and a test shortens them.
type Timeouts struct {
	Update, Solver time.Duration
}

func (t Timeouts) update() time.Duration {
	if t.Update > 0 {
		return t.Update
	}
	return UpdateTimeout
}

func (t Timeouts) solver() time.Duration {
	if t.Solver > 0 {
		return t.Solver
	}
	return SolverTimeout
}
