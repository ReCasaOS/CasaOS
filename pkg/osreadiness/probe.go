// Package osreadiness says whether this machine could be upgraded to the next release of its
// operating system, and why not. It is a report and nothing else: it changes nothing on the
// machine. It writes nothing under /etc, /var/lib/dpkg, /var/lib/apt or /var/cache/apt, takes no
// lock, starts and stops no service, and makes no request to the network. One tool leaves a
// transient trace that is said here and in the allow-list: dkms 3.x makes and removes a file in
// $TMPDIR while "dkms status" runs.
//
// That is a property of the seam, not a promise of the checks: a check sees the machine only
// through a Probe, which has no method that writes, creates, removes or starts anything, and the
// commands it may run are an allow-list (see Allowed) that the engine enforces whatever the Probe
// is. A check that errors, times out, panics or has no tool to ask is a refusal that says it does
// not know (Result.Data["unknown"]), never an ok.
//
// Checks are a table. A group of checks is a file that registers its rows from init (see register
// and Check); Run runs the rows that apply to the box's hop, four at a time, within 20 seconds.
package osreadiness

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"
)

// The bounds of what the seam gives back: nothing a check reads is unbounded.
const (
	// MaxFileBytes is the most ReadFile returns; a larger file is ErrTooLarge.
	MaxFileBytes = 1 << 20
	// MaxOutputBytes is the most of a command's output that Run returns; the rest is cut and
	// Output.Truncated says so.
	MaxOutputBytes = 1 << 20
)

var (
	// ErrToolMissing is the error of Run for a command that is not installed.
	ErrToolMissing = errors.New("the tool is not installed")
	// ErrTooLarge is the error of ReadFile for a file larger than MaxFileBytes.
	ErrTooLarge = errors.New("the file is too large")
	// ErrNotAllowed is the error of Run for a command that is not on the allow-list.
	ErrNotAllowed = errors.New("the command is not on the allow-list")
)

// Output is what a command printed (its standard output and its error output, in English, cut to
// MaxOutputBytes) and how it ended. A command that ran and failed is not an error of Run: it is
// an ExitCode that is not 0.
type Output struct {
	Text      string
	ExitCode  int
	Truncated bool
}

// FileInfo is what a check may know of a file.
type FileInfo struct {
	Size    int64
	Mode    fs.FileMode
	ModTime time.Time
}

// IsDir says whether the file is a directory.
func (f FileInfo) IsDir() bool { return f.Mode.IsDir() }

// IsSymlink says whether the file is a symbolic link (only Lstat tells: Stat follows it).
func (f FileInfo) IsSymlink() bool { return f.Mode&fs.ModeSymlink != 0 }

// Probe is the seam: everything a check may know of the machine, and nothing else. It has no
// method that writes, and a test pins that (TestTheProbeCannotWrite). A path that does not exist
// is an error that satisfies errors.Is(err, fs.ErrNotExist); a command that is not installed is
// ErrToolMissing. Every method that can wait is given the context of the check, which ends at the
// check's timeout.
type Probe interface {
	// ReadFile is the content of a file, at most MaxFileBytes (ErrTooLarge beyond), of a regular
	// file only.
	ReadFile(ctx context.Context, path string) ([]byte, error)
	// ReadHead is the first max bytes of a regular file (all of it when it is shorter, and never more than
	// MaxFileBytes), with no error for a file that is larger: what a file as large as a Release file says at its
	// start (its Origin) is read without reading the file.
	ReadHead(ctx context.Context, path string, max int) ([]byte, error)
	// ReadDir is the names of the entries of a directory, sorted.
	ReadDir(ctx context.Context, path string) ([]string, error)
	// Stat describes a path, following symbolic links; Lstat does not follow them.
	Stat(ctx context.Context, path string) (FileInfo, error)
	Lstat(ctx context.Context, path string) (FileInfo, error)
	// Readlink is the target of a symbolic link, as written.
	Readlink(ctx context.Context, path string) (string, error)
	// Run runs a command: argv, no shell, in English, with its output bounded. Only what Allowed
	// says is ever run: the engine hands a check a Probe that refuses the rest (ErrNotAllowed).
	Run(ctx context.Context, argv ...string) (Output, error)
	// FreeBytes is the room a program that is not root has left on the filesystem of a path.
	FreeBytes(ctx context.Context, path string) (uint64, error)
	// MaintenanceBusy says whether something else is changing the box's packages or its ReCasaOS
	// release now (one of the maintenance units runs, or a package manager holds dpkg's lock),
	// and says what, in a few words. An error says it could not ask (dpkg's lock cannot be looked at,
	// the box is not one that has a lock to look at): a lock that cannot be seen is not one that
	// nobody holds, and the check that asks says it does not know. A busy box is busy whatever else
	// could not be looked at, and has no error.
	MaintenanceBusy(ctx context.Context) (reason string, busy bool, err error)
	// AppOperations are the apps that have an operation in progress (an install, an update, a
	// backup, a restore), by name. An error is no answer: AppManagement cannot be asked.
	AppOperations(ctx context.Context) ([]string, error)
	// Now is the clock.
	Now() time.Time
	// EUID is the user the core runs as; Arch is the architecture it runs on (GOARCH).
	EUID() int
	Arch() string
}

// guarded is a Probe that runs only what Allowed says. The engine wraps every Probe in it, so that
// the allow-list holds for the production wiring, for a fake and for a check that was written
// carelessly alike.
type guarded struct{ Probe }

func (g guarded) Run(ctx context.Context, argv ...string) (Output, error) {
	if !Allowed(argv) {
		name := ""
		if len(argv) > 0 {
			name = argv[0]
		}
		return Output{}, fmt.Errorf("%w: %q", ErrNotAllowed, name)
	}
	return g.Probe.Run(ctx, argv...)
}
