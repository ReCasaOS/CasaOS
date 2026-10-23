//go:build linux

package service

import (
	"context"
	"os"
	"os/exec"
	"syscall"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// Run runs one of the four commands of the plan (osplan.AllowedSandboxArgv, which it checks again, for the directory this
// sandbox made): no shell, the standard input from /dev/null, the environment osplan.SandboxEnv says and nothing else of the
// core's, in the sandbox directory, as a process group that is killed as a group when ctx ends, with its output bounded to
// osplan.MaxOutputBytes (Output.Truncated says it was cut). A command that ends with a status is an Output, whatever the
// status is; one that could not be run, or that was killed with ctx, is an error (see finishRun).
func (s *osPlanSandbox) Run(ctx context.Context, argv []string) (osplan.Output, error) {
	dir := s.Dir()
	if dir == "" || !osplan.AllowedSandboxArgv(argv, dir) {
		return osplan.Output{}, osreadiness.ErrNotAllowed
	}
	env := osplan.SandboxEnv(dir)
	exe, err := lookInPath(argv[0], s.searchDirs(env))
	if err != nil {
		return osplan.Output{}, osreadiness.ErrToolMissing
	}
	// the absolute path: exec.Command looks a bare name up in the core's PATH, which is not the clean one
	cmd := exec.CommandContext(ctx, exe, argv[1:]...)
	cmd.Args[0] = argv[0]
	cmd.Env = env
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// the group, which apt's methods are in: not only the process that apt-get is
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	text, truncated, err := runBounded(ctx, cmd, osplan.MaxOutputBytes)
	return finishRun(ctx, text, truncated, err)
}
