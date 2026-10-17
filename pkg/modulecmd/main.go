package modulecmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/ReCasaOS/CasaOS/pkg/utils/version"
)

// Run is `casaos module ...` as the core's main runs it: args are what follows "module". It ends a run on SIGTERM and SIGINT the way a failure ends it (the step it is in fails with
// "interrupted" and is rolled back), and a second signal ends the process at once.
func Run(args []string, env Env) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // the default again: a second signal is not waited for
	}()
	return Main(ctx, args, env)
}

// Main runs one command and returns its exit code (ExitOK, ExitRefused, ExitFailed).
func Main(ctx context.Context, args []string, env Env) int {
	env = env.withDefaults()
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

// Production is the Env of a box: the real file system, commands, network and clock.
func Production() Env {
	env := Env{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Getenv: os.Getenv, EUID: os.Geteuid,
		// the release of the distribution (the one min_recasaos is a number of), not the version of this component
		Arch: runtime.GOARCH, Version: version.CurrentVersion(),
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
