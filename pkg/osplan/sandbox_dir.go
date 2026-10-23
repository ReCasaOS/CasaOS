package osplan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

// The machine's side of the Sandbox, which the plan of an upgrade (service/) and the plan of a module (pkg/modulecmd/) share: one private directory in SandboxParent, files written
// inside it and nowhere else, and the directory removed on every exit; and the sweep of the directories that a plan that was killed left behind. Each caller adds its own Run, which
// checks its own allow-list and runs the command its own way.

// SandboxDirMode is the mode of the sandbox directory and of the directories made above a file in it. apt gives the directories it downloads into (lists/partial) to its own user
// (_apt), which has to walk down to them: it may search every directory above them, and list none. Nothing in the sandbox is private by its mode but the files, which are 0600: the
// sources and the configuration of apt, in which a proxy's password may be.
const SandboxDirMode fs.FileMode = 0o711

// DirSandbox makes, writes and removes the directory of a Sandbox; the zero value is ready, and makes one directory.
type DirSandbox struct {
	// Log, when set, is told what went wrong with the removal of the directory: the first failure, which is tried again after a moment, and the second, which leaves it.
	Log  func(msg, dir string, err error)
	mu   sync.Mutex
	dir  string
	left string
}

// What removes a directory, the wait before it is tried a second time, and who the sandbox's user is (the owner of a directory it made): vars for a test to put its own in their place.
var (
	removeDir        = RemoveSandboxDir
	removeRetryAfter = time.Second
	sandboxEUID      = os.Geteuid
)

// Dir is the directory MakeDir made, "" before it did.
func (s *DirSandbox) Dir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dir
}

// MakeDir makes the directory (os.MkdirTemp: 0700, owned by the user that runs, which is root) and opens it to be searched, as SandboxDirMode says. The function it gives back removes
// it, once and whatever is in it, and does nothing the second time.
func (s *DirSandbox) MakeDir() (string, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir != "" {
		return "", nil, errors.New("the sandbox has its directory already")
	}
	dir, err := os.MkdirTemp(SandboxParent, SandboxPrefix)
	if err != nil {
		return "", nil, err
	}
	if !ValidSandboxDir(dir) {
		_ = os.Remove(dir)
		return "", nil, fmt.Errorf("%s is not named as a sandbox directory is", dir)
	}
	if err := os.Chmod(dir, SandboxDirMode); err != nil {
		_ = os.Remove(dir)
		return "", nil, err
	}
	s.dir = dir
	name := path.Base(dir)
	var once sync.Once
	return dir, func() { once.Do(func() { s.remove(name) }) }, nil
}

// remove removes the directory of the sandbox by its name. It tries a second time after a moment when the first fails, says both failures to Log, and keeps the name of what it could
// not remove, for LeftBehind: what is left is removed by a later sweep once it is old enough (SweepStale).
func (s *DirSandbox) remove(name string) {
	dir := SandboxParent + "/" + name
	err := removeDir(SandboxParent, name)
	if err == nil {
		return
	}
	s.log("the directory of a plan could not be removed: trying once more", dir, err)
	time.Sleep(removeRetryAfter)
	if err = removeDir(SandboxParent, name); err == nil {
		return
	}
	s.log("the directory of a plan could not be removed: it is left for a later start of the core", dir, err)
	s.mu.Lock()
	s.left = dir
	s.mu.Unlock()
}

func (s *DirSandbox) log(msg, dir string, err error) {
	if s.Log != nil {
		s.Log(msg, dir, err)
	}
}

// LeftBehind is the directory that the sandbox could not remove, "" when it removed it, or has not yet.
func (s *DirSandbox) LeftBehind() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.left
}

// WriteFile writes a file in the sandbox's directory, and makes the directories above it (SandboxDirMode); a mode that says directory makes the directory, with the permissions of the
// mode (and its sticky bit). A path that is not inside the directory is refused (InsideSandbox), and so is one that goes through a link or over a file: the files of a plan are
// written once each, and the directories are the ones this made.
func (s *DirSandbox) WriteFile(p string, content []byte, mode fs.FileMode) error {
	dir := s.Dir()
	if !InsideSandbox(dir, p) {
		return errors.New("a file outside the sandbox directory is not written")
	}
	if mode&fs.ModeDir != 0 {
		// the permissions with the sticky bit, which the one directory that anyone may write in has
		return mkdirBelow(dir, p, mode&(fs.ModePerm|fs.ModeSticky))
	}
	if err := mkdirBelow(dir, path.Dir(p), SandboxDirMode); err != nil {
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

// mkdirBelow makes target, and every directory between dir and it that is not there: those with SandboxDirMode, it with perm (permission bits and the sticky bit), whatever the umask
// is. A path that is there and is not a directory (a link is not one: it is not followed) is an error.
func mkdirBelow(dir, target string, perm fs.FileMode) error {
	if target == dir {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(target, dir+"/"), "/")
	current := dir
	for i, part := range parts {
		current += "/" + part
		mode := SandboxDirMode
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

// RemoveSandboxDir removes a sandbox directory of parent, by its name: only a name that is one (IsSandboxDirName), and only after Lstat says it is a directory and not a link, whatever
// is in it. One that is not there is removed already.
func RemoveSandboxDir(parent, name string) error {
	if !IsSandboxDirName(name) {
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

// notOurs says why a directory that is named as a sandbox's is not one that the sandbox made, "" when it is: it is the sandbox's user's, nobody else may write in it (0711 when it is
// made), and it is on the file system of its parent (a mount point is somebody's, whatever its name). The name is a pattern anyone can use: it is not proof.
func notOurs(info, parent fs.FileInfo) string {
	uid, dev, ok := ownerAndDevice(info)
	_, parentDev, parentOK := ownerAndDevice(parent)
	switch {
	case !ok || !parentOK:
		return "who owns it, and where it is, cannot be seen"
	case int64(uid) != int64(sandboxEUID()):
		return "it belongs to another user"
	case info.Mode().Perm()&0o022 != 0:
		return "others may write in it"
	case dev != parentDev:
		return "it is on another file system than /var/tmp"
	}
	return ""
}

// SweepStale removes the sandbox directories of parent that are older than olderThan, whichever of the two callers made them: a plan that was killed, or a core that did not live to
// remove its own, left them. Only a directory (never a link) with the name of one that the sandbox's user owns, that nobody else may write in and that is on the file system of its
// parent (notOurs); one that is not is left alone and said to leftAlone, when it is set. What it removed is returned, and the first error with it. Both callers sweep with StaleAfter,
// which is longer than either of them lives, so that neither removes the directory of a plan of the other that is still at work.
func SweepStale(parent string, now time.Time, olderThan time.Duration, leftAlone func(dir, why string)) ([]string, error) {
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
		if !IsSandboxDirName(name) {
			continue
		}
		info, err := os.Lstat(parent + "/" + name)
		if err != nil || !info.IsDir() || now.Sub(info.ModTime()) < olderThan {
			continue
		}
		if why := notOurs(info, parentInfo); why != "" {
			if leftAlone != nil {
				leftAlone(parent+"/"+name, why)
			}
			continue
		}
		if err := RemoveSandboxDir(parent, name); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		removed = append(removed, parent+"/"+name)
	}
	return removed, first
}
