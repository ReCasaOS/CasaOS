// Package modulecmd is the module assistant: `casaos module list|plan|install|remove|status`. It is the one place a module is installed and removed; the sha256 check, the rules of what an
// archive may write and the reading of manifests and catalogues are the code of osmodule, and this package is what runs them against a box: it asks the box what it is (plan), puts a
// module on it in steps and takes it off again (install, remove).
//
// Everything the commands touch of the world comes through an Env: the file system (a Roots), the commands they run (a Runner), the network (a Downloader), the gateway, the clock, the
// terminal, and the few calls that only Linux has (a Host). Production wires the real ones (Production); a test wires a temporary directory and stand-ins. The package compiles everywhere
// and runs for real on Linux only.
package modulecmd

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
)

// The exit codes of the commands.
const (
	ExitOK      = 0
	ExitRefused = 2 // a code, and nothing was changed
	ExitFailed  = 3 // the work began, failed at a step and was rolled back as said
)

// Output is what a command printed (its output and error output, in English, cut) and how it ended.
type Output = osplan.Output

// A Cmd is a command to run: no shell. Env is the whole environment (nil is the clean one of the install); Dir the directory it runs in ("" is the root). Stdout, when set, takes the
// output as it comes (and Output.Text is empty); when nil the output is kept, bounded, in Output.Text. Timeout, when set, ends the command with its context error.
type Cmd struct {
	Argv    []string
	Env     []string
	Dir     string
	Stdout  io.Writer
	Timeout time.Duration
}

// A Runner runs commands (apt-get, systemctl). A command that ran and ended with a status is an Output, whatever the status is; one that could not be run, or that was ended by its
// context or its timeout, is an error.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Output, error)
}

// A Downloader fetches an address into dst and says how many bytes it wrote. An error that retrying cannot mend (a refused address, a refused redirect, too many bytes, a status that
// is not 200) is a *PermanentError.
type Downloader interface {
	Get(ctx context.Context, url string, dst io.Writer) (int64, error)
}

// A PermanentError is a download that is not tried again.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// A Gateway says whether the gateway answers on a route.
type Gateway interface {
	Answers(ctx context.Context, route string) (bool, error)
}

// A Clock is the time and the wait.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// A Host is what only the machine has: a lock that dies with its holder, the room on a file system, the question of whether a package manager holds dpkg's lock, and the file
// system the unpack writes to.
type Host interface {
	// Lock takes the lock at path, which is not waited for: ErrLockHeld when another process has it. The lock is the process's, and is gone when the process is.
	Lock(path string) (release func(), err error)
	// FreeBytes is the room left, for a program that is not root, on the file system that holds the path (the nearest directory that exists).
	FreeBytes(path string) (int64, error)
	// DpkgLockHeld says whether another process holds a lock on any of the files; an error says it could not be told.
	DpkgLockHeld(paths []string) (bool, error)
	// NewDest is the file system an archive is unpacked onto, with prefix in front of every path.
	NewDest(prefix string) osmodule.Destination
	// IsTerminal says whether the file is a terminal.
	IsTerminal(f *os.File) bool
}

var (
	// ErrLockHeld is the error of Lock for a lock that another process holds.
	ErrLockHeld = errors.New("the lock is held")
	// ErrUnsupported is what a host that is not Linux says.
	ErrUnsupported = errors.New("this system cannot run the module assistant")
)

// An Env is the world of a run of the assistant.
type Env struct {
	Roots  Roots
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	// EUID is the user the assistant runs as; Arch the architecture as the archives name it ("amd64", "arm64"); Version the ReCasaOS release that is running.
	EUID    func() int
	Arch    string
	Version string

	Runner Runner
	// Sandbox makes the sandbox the simulation of apt runs in (osplan.Sandbox): the production one is a directory in /var/tmp.
	Sandbox  func() osplan.Sandbox
	Download Downloader
	Gateway  Gateway
	Clock    Clock
	// Terminal says whether there is someone to ask.
	Terminal func() bool
	Host     Host
	// StepHook, when set, is called at the start and at the end of every step of an install and a remove, with the step and "begin" or "done": a test uses it to stop the run at a
	// chosen point. Production leaves it nil.
	StepHook func(ctx context.Context, step, phase string)
}

type noRunner struct{}

func (noRunner) Run(context.Context, Cmd) (Output, error) { return Output{}, ErrUnsupported }

type noHost struct{}

func (noHost) Lock(string) (func(), error)         { return nil, ErrUnsupported }
func (noHost) FreeBytes(string) (int64, error)     { return 0, ErrUnsupported }
func (noHost) DpkgLockHeld([]string) (bool, error) { return false, ErrUnsupported }
func (noHost) NewDest(string) osmodule.Destination { return refusingDest{} }
func (noHost) IsTerminal(*os.File) bool            { return false }

func (e Env) withDefaults() Env {
	if e.Stdin == nil {
		e.Stdin = eofReader{}
	}
	if e.Stdout == nil {
		e.Stdout = io.Discard
	}
	if e.Stderr == nil {
		e.Stderr = io.Discard
	}
	if e.Getenv == nil {
		e.Getenv = func(string) string { return "" }
	}
	if e.EUID == nil {
		e.EUID = func() int { return -1 }
	}
	if e.Clock == nil {
		e.Clock = realClock{}
	}
	if e.Terminal == nil {
		e.Terminal = func() bool { return false }
	}
	if e.Host == nil {
		e.Host = noHost{}
	}
	if e.Runner == nil {
		e.Runner = noRunner{}
	}
	return e
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

// testMode says whether the test hooks are on: exactly CASAOS_MODULE_TEST=1, and nothing else. The shipped unit never sets it.
func (e Env) testMode() bool { return e.Getenv("CASAOS_MODULE_TEST") == "1" }
