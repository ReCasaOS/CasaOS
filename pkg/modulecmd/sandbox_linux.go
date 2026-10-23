//go:build linux

package modulecmd

import (
	"context"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The sandbox of the simulation of apt (osplan.Sandbox): osplan.DirSandbox, the directory in /var/tmp that the plan of an upgrade uses as well, whatever the Roots say (the allow-list
// of osplan has no other), and only the two commands of osplan.AllowedModuleArgv run in it.

// sandboxParent is the directory that is swept for the stale sandboxes before one is made: /var/tmp. A test points it elsewhere.
var sandboxParent = osplan.SandboxParent

type aptSandbox struct {
	osplan.DirSandbox
	tools toolPaths
}

func newSandbox(roots Roots) func() osplan.Sandbox {
	tools := findTools(roots)
	return func() osplan.Sandbox { return &aptSandbox{tools: tools} }
}

// MakeDir clears the remains of the plans that were killed (SIGKILL is not caught) before it makes its own directory.
func (s *aptSandbox) MakeDir() (string, func(), error) {
	_, _ = osplan.SweepStale(sandboxParent, time.Now(), osplan.StaleAfter, nil)
	return s.DirSandbox.MakeDir()
}

func (s *aptSandbox) Run(ctx context.Context, argv []string) (osplan.Output, error) {
	dir := s.Dir()
	if dir == "" || !osplan.AllowedModuleArgv(argv, dir) {
		return osplan.Output{}, osreadiness.ErrNotAllowed
	}
	exe, err := s.tools.path(argv[0])
	if err != nil {
		return osplan.Output{}, err
	}
	// the environment of the sandbox and nothing of the process's; ended with SIGKILL, as a simulation holds nothing that needs to be left in order
	return runProcess(ctx, exe, Cmd{Argv: argv, Env: osplan.SandboxEnv(dir), Dir: dir}, 0)
}
