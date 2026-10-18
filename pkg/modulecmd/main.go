package modulecmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
)

// Run is `casaos module ...` as the core's main runs it: args are what follows "module". It ends a run on SIGTERM, SIGINT and SIGHUP the way a failure ends it (the step it is in fails
// with "interrupted" and is rolled back), and a second signal ends the process at once.
func Run(args []string, env Env) int {
	// A write to an output that was closed (`| head`, a pager that was quit) is an error of the write and not the end of a root install: the log is the record. SIGPIPE is taken (not
	// ignored): an ignored signal stays ignored in the commands that are run, and they are to have it as every program does.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
	// SIGHUP is a dropped session (ssh): the run ends as it does for SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // the default again: a second signal is not waited for
	}()
	return Main(ctx, args, env)
}

// Main runs one command and returns its exit code (ExitOK, ExitRefused, ExitFailed).
func Main(ctx context.Context, args []string, env Env) (code int) {
	env = env.withDefaults()
	// (install and remove turn their own panics into a result line; this is for the rest: a panic is a failure, never the status 2 of "nothing changed")
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(env.Stderr, "casaos module: internal error: %s\n", printable(fmt.Sprint(r), 200))
			code = ExitFailed
		}
	}()
	c, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(env.Stderr, "casaos module: %v\n%s\n", err, usage)
		return ExitRefused
	}
	switch c.verb {
	case "list":
		res, err := List(ctx, env)
		return emit(env, res, err)
	case "plan":
		return emit(env, PlanOf(ctx, env, c.name), nil)
	case "status":
		if c.name == "" {
			res, err := statusAll(ctx, env)
			return emit(env, res, err)
		}
		res, err := StatusOf(ctx, env, c.name)
		return emit(env, res, err)
	case "install":
		return runInstall(ctx, env, c)
	default: // remove
		return runRemove(ctx, env, c)
	}
}

// emit prints the one JSON object of a command that only reads, and nothing else on the standard output.
func emit(env Env, v any, err error) int {
	if err != nil {
		fmt.Fprintf(env.Stderr, "casaos module: %v\n", printable(err.Error(), 200))
		return ExitFailed
	}
	if err := printJSON(env.Stdout, v); err != nil {
		fmt.Fprintf(env.Stderr, "casaos module: %v\n", err)
		return ExitFailed
	}
	return ExitOK
}

// distributionVersion is the release of the distribution that is installed, as the installer wrote it to /var/lib/casaos/fork-release, and "" when the file is not there, is not a regular
// file, is longer than a release is or is blank: a release that is not known is not made up (version.CurrentVersion() falls back to the release this binary was built for, which says a
// box that has no marker is one that has just been installed with the release of the binary, and an assistant that installs a module on a box must not guess). What is in the file is not
// judged here: the plan takes a release that it cannot read for an unknown one.
func distributionVersion(roots Roots) string {
	raw, err := readRegular(roots.Path(pathForkRelease), 64)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// Production is the Env of a box: the real file system, commands, network and clock.
func Production() Env {
	env := Env{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Getenv: os.Getenv, EUID: os.Geteuid,
		// the release of the distribution (the one min_recasaos is a number of), not the version of this component, and not the release that this binary was built for either
		Arch: runtime.GOARCH, Version: distributionVersion(Roots{}),
		Download: NewHTTPDownloader(),
		Clock:    realClock{},
		Host:     newHost(),
	}
	env.Runner = newRunner(env.Roots)
	env.Sandbox = newSandbox(env.Roots)
	env.Gateway = newGateway(env.Roots)
	env.Terminal = func() bool { return env.Host.IsTerminal(os.Stdin) }
	return env
}
