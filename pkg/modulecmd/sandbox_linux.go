//go:build linux

package modulecmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The sandbox of the simulation of apt (osplan.Sandbox): one private directory in /var/tmp, files written inside it and nowhere else, only the two commands of
// osplan.AllowedModuleArgv run in it, and the directory removed on every exit.
//
// ponytail: the directory is where the plan of the upgrade's is, /var/tmp, whatever the Roots say: the allow-list of osplan has no other.

// sandboxDirMode is the mode of the directories of the sandbox: apt drops to its own user to fetch, and that user has to walk down to the directories apt gives it.
const sandboxDirMode fs.FileMode = 0o711

type aptSandbox struct {
	roots Roots
	mu    sync.Mutex
	dir   string
}

func newSandbox(roots Roots) func() osplan.Sandbox {
	return func() osplan.Sandbox { return &aptSandbox{roots: roots} }
}

func (s *aptSandbox) directory() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dir
}

func (s *aptSandbox) MakeDir() (string, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir != "" {
		return "", nil, errors.New("the sandbox has its directory already")
	}
	dir, err := os.MkdirTemp(osplan.SandboxParent, osplan.SandboxPrefix)
	if err != nil {
		return "", nil, err
	}
	if !osplan.ValidSandboxDir(dir) {
		_ = os.Remove(dir)
		return "", nil, fmt.Errorf("%s is not named as a sandbox directory is", dir)
	}
	if err := os.Chmod(dir, sandboxDirMode); err != nil {
		_ = os.Remove(dir)
		return "", nil, err
	}
	s.dir = dir
	var once sync.Once
	return dir, func() {
		once.Do(func() {
			// only a directory, not a link, with the name of a sandbox's
			if info, err := os.Lstat(dir); err == nil && info.IsDir() && osplan.IsSandboxDirName(path.Base(dir)) {
				_ = os.RemoveAll(dir)
			}
		})
	}, nil
}

func (s *aptSandbox) WriteFile(p string, content []byte, mode fs.FileMode) error {
	dir := s.directory()
	if !osplan.InsideSandbox(dir, p) {
		return errors.New("a file outside the sandbox directory is not written")
	}
	if mode&fs.ModeDir != 0 {
		return mkdirBelow(dir, p, mode&(fs.ModePerm|fs.ModeSticky))
	}
	if err := mkdirBelow(dir, path.Dir(p), sandboxDirMode); err != nil {
		return err
	}
	// O_EXCL: a link, or a file, that is there is not written through or over
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// mkdirBelow makes target and every directory between dir and it that is not there (those with sandboxDirMode, target with perm, whatever the umask is).
func mkdirBelow(dir, target string, perm fs.FileMode) error {
	if target == dir {
		return nil
	}
	current := dir
	parts := strings.Split(strings.TrimPrefix(target, dir+"/"), "/")
	for i, part := range parts {
		current += "/" + part
		mode := sandboxDirMode
		if i == len(parts)-1 {
			mode = perm
		}
		switch err := os.Mkdir(current, mode); {
		case err == nil:
			if err := os.Chmod(current, mode); err != nil {
				return err
			}
		case errors.Is(err, fs.ErrExist):
			if info, err := os.Lstat(current); err != nil || !info.IsDir() {
				return fmt.Errorf("%s is there and is not a directory", current)
			}
		default:
			return err
		}
	}
	return nil
}

func (s *aptSandbox) Run(ctx context.Context, argv []string) (osplan.Output, error) {
	dir := s.directory()
	if dir == "" || !osplan.AllowedModuleArgv(argv, dir) {
		return osplan.Output{}, osreadiness.ErrNotAllowed
	}
	exe, err := lookIn(argv[0], s.roots.searchDirs())
	if err != nil {
		return osplan.Output{}, err
	}
	// the environment of the sandbox and nothing of the process's; ended with SIGKILL, as a simulation holds nothing that needs to be left in order
	return runProcess(ctx, exe, Cmd{Argv: argv, Env: osplan.SandboxEnv(dir), Dir: dir}, 0)
}
