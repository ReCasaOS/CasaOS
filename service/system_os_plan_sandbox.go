package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"go.uber.org/zap"
)

// The sandbox of the plan of an upgrade of the operating system (osplan.Sandbox): the only thing the plan writes with and runs
// commands with. It makes one private directory in /var/tmp, writes inside it and nowhere else, runs the four commands of the
// allow-list in it (system_os_plan_sandbox_linux.go) and removes it. What is a rule is osplan's (the names, the allow-list, the
// files); this is only the machine's side of them.

// osPlanDirMode is the mode of the directories the sandbox makes above a file, and of its own. apt gives the directories it
// downloads into (lists/partial) to its own user (_apt), which has to walk down to them: it may search every directory above
// them, and list none. Nothing in the sandbox is private by its mode but the files, which are 0600: the sources and the
// configuration of apt, in which a proxy's password may be. The measurement of the hop made its directories 0755 for the same
// reason.
const osPlanDirMode fs.FileMode = 0o711

type osPlanSandbox struct {
	mu  sync.Mutex
	dir string
	// left is the directory that could not be removed, when it could not be (see remove)
	left string
	// searchPath is where apt-get is looked for: nil is the PATH of the sandbox's own environment (osplan.SandboxEnv), and a
	// test says where its stand-in is.
	searchPath []string
}

var _ osplan.Sandbox = (*osPlanSandbox)(nil)

// What removes a directory, the wait before it is tried a second time, and who the core is (the owner of a directory it made):
// vars for a test to put its own in their place.
var (
	removeOSPlanDir  = removeSandboxDir
	osPlanRetryAfter = time.Second
	osPlanEUID       = os.Geteuid
)

// directory is the directory MakeDir made, "" before it did.
func (s *osPlanSandbox) directory() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dir
}

// MakeDir makes the directory (os.MkdirTemp: 0700, owned by the user the core runs as, which is root) and opens it to be
// searched, as osPlanDirMode says. The function it gives back removes it, once and whatever is in it, and does nothing the
// second time.
func (s *osPlanSandbox) MakeDir() (string, func(), error) {
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
	if err := os.Chmod(dir, osPlanDirMode); err != nil {
		_ = os.Remove(dir)
		return "", nil, err
	}
	s.dir = dir
	name := path.Base(dir)
	var once sync.Once
	return dir, func() { once.Do(func() { s.remove(name) }) }, nil
}

// remove removes the directory of the sandbox by its name. It tries a second time after a moment when the first fails, says
// both failures in the log, and keeps the name of what it could not remove, for the state of the job to say: what is left is
// removed by the sweep at a start of the core once it is old enough (RemoveStaleOSPlanDirs).
func (s *osPlanSandbox) remove(name string) {
	dir := osplan.SandboxParent + "/" + name
	err := removeOSPlanDir(osplan.SandboxParent, name)
	if err == nil {
		return
	}
	logger.Error("the directory of a plan could not be removed: trying once more", zap.String("dir", dir), zap.Error(err))
	time.Sleep(osPlanRetryAfter)
	if err = removeOSPlanDir(osplan.SandboxParent, name); err == nil {
		return
	}
	logger.Error("the directory of a plan could not be removed: it is left for a later start of the core", zap.String("dir", dir), zap.Error(err))
	s.mu.Lock()
	s.left = dir
	s.mu.Unlock()
}

// LeftBehind is the directory that the sandbox could not remove, "" when it removed it, or has not yet.
func (s *osPlanSandbox) LeftBehind() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.left
}

// WriteFile writes a file in the sandbox's directory, and makes the directories above it (osPlanDirMode); a mode that says
// directory makes the directory, with the permissions of the mode (and its sticky bit). A path that is not inside the directory is refused (the
// same check osplan makes before it writes anything), and so is one that goes through a link or over a file: the files of the
// plan are written once each, and the directories are the ones this made.
func (s *osPlanSandbox) WriteFile(p string, content []byte, mode fs.FileMode) error {
	dir := s.directory()
	if !osplan.InsideSandbox(dir, p) {
		return errors.New("a file outside the sandbox directory is not written")
	}
	if mode&fs.ModeDir != 0 {
		// the permissions with the sticky bit, which the one directory that anyone may write in has
		return mkdirBelow(dir, p, mode&(fs.ModePerm|fs.ModeSticky))
	}
	if err := mkdirBelow(dir, path.Dir(p), osPlanDirMode); err != nil {
		return err
	}
	// O_EXCL: a link, or a file, that is there is not written through or over
	file, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// mkdirBelow makes target, and every directory between dir and it that is not there: those with osPlanDirMode, it with perm
// (permission bits and the sticky bit), whatever the umask is. A path that is there and is not a directory (a link is not one:
// it is not followed) is an error.
func mkdirBelow(dir, target string, perm fs.FileMode) error {
	if target == dir {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(target, dir+"/"), "/")
	current := dir
	for i, part := range parts {
		current += "/" + part
		mode := osPlanDirMode
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

// removeSandboxDir removes a sandbox directory of parent, by its name: only a name that is one (osplan.IsSandboxDirName), and
// only after Lstat says it is a directory and not a link, whatever is in it. One that is not there is removed already.
func removeSandboxDir(parent, name string) error {
	if !osplan.IsSandboxDirName(name) {
		return fmt.Errorf("%q is not the name of a sandbox directory", name)
	}
	p := parent + "/" + name
	info, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.IsDir():
		return fmt.Errorf("%s is not a directory", p)
	}
	return os.RemoveAll(p)
}

// notTheCores says why a directory that is named as a sandbox's is not one that this core made, "" when it is: it is the
// core's user's (the core makes it, as that user), nobody else may write in it (0711 when it is made), and it is on the file
// system of its parent (a mount point is somebody's, whatever its name). The name is a pattern anyone can use: it is not proof.
func notTheCores(info, parent fs.FileInfo) string {
	uid, dev, ok := ownerAndDevice(info)
	_, parentDev, parentOK := ownerAndDevice(parent)
	switch {
	case !ok || !parentOK:
		return "who owns it, and where it is, cannot be seen"
	case int64(uid) != int64(osPlanEUID()):
		return "it belongs to another user"
	case info.Mode().Perm()&0o022 != 0:
		return "others may write in it"
	case dev != parentDev:
		return "it is on another file system than /var/tmp"
	}
	return ""
}

// sweepStaleSandboxes removes the sandbox directories of parent that are older than olderThan: a core that did not live to
// remove its own left them. Only a directory (never a link) with the name of one that the core's own user owns, that nobody
// else may write in and that is on the file system of its parent (notTheCores); one that is not is left alone and said in the
// log. What it removed is returned, and the first error with it.
func sweepStaleSandboxes(parent string, now time.Time, olderThan time.Duration) ([]string, error) {
	entries, err := os.ReadDir(parent)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return nil, err
	}
	var removed []string
	var first error
	for _, entry := range entries {
		name := entry.Name()
		if !osplan.IsSandboxDirName(name) {
			continue
		}
		info, err := os.Lstat(parent + "/" + name)
		if err != nil || !info.IsDir() || now.Sub(info.ModTime()) < olderThan {
			continue
		}
		if why := notTheCores(info, parentInfo); why != "" {
			logger.Warn("a directory named as the sandbox of a plan is not one the core made, and is left alone", zap.String("dir", parent+"/"+name), zap.String("why", why))
			continue
		}
		if err := removeSandboxDir(parent, name); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		removed = append(removed, parent+"/"+name)
	}
	return removed, first
}

// RemoveStaleOSPlanDirs is what the core does when it starts: the sandbox directories that an earlier core left in /var/tmp
// (it was killed, or the machine went down, while a plan was being made) and that are older than ten minutes are removed.
func RemoveStaleOSPlanDirs() ([]string, error) {
	return sweepStaleSandboxes(osplan.SandboxParent, time.Now(), osplan.StaleAfter)
}
