package service

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"go.uber.org/zap"
)

// The sandbox of the plan of an upgrade of the operating system (osplan.Sandbox): the only thing the plan writes with and runs
// commands with. Its directory is osplan.DirSandbox's, which the plan of a module shares; this adds the four commands of the
// allow-list (system_os_plan_sandbox_linux.go) and says in the core's log what could not be removed.

type osPlanSandbox struct {
	osplan.DirSandbox
	// searchPath is where apt-get is looked for: nil is the PATH of the sandbox's own environment (osplan.SandboxEnv), and a
	// test says where its stand-in is.
	searchPath []string
}

var _ osplan.Sandbox = (*osPlanSandbox)(nil)

// MakeDir is DirSandbox's, with a removal that fails said in the log: what is left is removed by the sweep at a start of the
// core once it is old enough (RemoveStaleOSPlanDirs), and LeftBehind says it for the state of the job.
func (s *osPlanSandbox) MakeDir() (string, func(), error) {
	s.Log = func(msg, dir string, err error) { logger.Error(msg, zap.String("dir", dir), zap.Error(err)) }
	return s.DirSandbox.MakeDir()
}

// searchDirs are the directories apt-get is looked for in.
func (s *osPlanSandbox) searchDirs(env []string) []string {
	if s.searchPath != nil {
		return s.searchPath
	}
	for _, kv := range env {
		if value, ok := strings.CutPrefix(kv, "PATH="); ok {
			return strings.Split(value, ":")
		}
	}
	return nil
}

// lookInPath is the first file called name in the directories that is a regular file somebody may run. It does not look in
// the core's PATH, which is the core's, and not the clean one that the command gets.
func lookInPath(name string, dirs []string) (string, error) {
	for _, dir := range dirs {
		if !path.IsAbs(dir) {
			continue
		}
		p := dir + "/" + name
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s is not in %s: %w", name, strings.Join(dirs, ":"), fs.ErrNotExist)
}

// sweepStaleSandboxes is osplan.SweepStale with what it leaves alone said in the log.
func sweepStaleSandboxes(parent string, now time.Time, olderThan time.Duration) ([]string, error) {
	return osplan.SweepStale(parent, now, olderThan, func(dir, why string) {
		logger.Warn("a directory named as the sandbox of a plan is not one the core made, and is left alone", zap.String("dir", dir), zap.String("why", why))
	})
}

// RemoveStaleOSPlanDirs is what the core does when it starts: the sandbox directories that an earlier core left in /var/tmp
// (it was killed, or the machine went down, while a plan was being made), or a plan of a module that was killed, and that are
// older than osplan.StaleAfter are removed.
func RemoveStaleOSPlanDirs() ([]string, error) {
	return sweepStaleSandboxes(osplan.SandboxParent, time.Now(), osplan.StaleAfter)
}
