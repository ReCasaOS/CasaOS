//go:build !linux

package modulecmd

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// portableHost is the host of the tests where there is no Linux to run on: a lock that is kept in the process, and a file system that does with the calls of os. What is Linux's own (the
// flock, the unpack onto a real tree with its links and modes, the signals) is tested on Linux.
type portableHost struct{}

func baseHost() Host { return portableHost{} }

var (
	portableLocks   = map[string]bool{}
	portableLocksMu sync.Mutex
)

func (portableHost) Lock(path string) (func(), error) {
	portableLocksMu.Lock()
	defer portableLocksMu.Unlock()
	if portableLocks[path] {
		return nil, ErrLockHeld
	}
	portableLocks[path] = true
	return func() { portableLocksMu.Lock(); delete(portableLocks, path); portableLocksMu.Unlock() }, nil
}

func (portableHost) FreeBytes(string) (int64, error)     { return 1 << 40, nil }
func (portableHost) DpkgLockHeld([]string) (bool, error) { return false, nil }
func (portableHost) IsTerminal(*os.File) bool            { return false }
func (portableHost) NewDest(prefix string) osmodule.Destination {
	return portableDest{roots: Roots{Prefix: prefix}}
}

type portableDest struct{ roots Roots }

func (d portableDest) Lstat(p string) (fs.FileMode, error) {
	info, err := os.Lstat(d.roots.Path(p))
	if err != nil {
		return 0, err
	}
	return info.Mode(), nil
}
func (d portableDest) Mkdir(p string, perm fs.FileMode) error {
	return os.Mkdir(d.roots.Path(p), 0o755)
}
func (d portableDest) Symlink(target, p string, replace bool) error {
	real := d.roots.Path(p)
	if replace {
		if err := os.Remove(real); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return os.Symlink(target, real) // (it fails with fs.ErrExist when anything is there)
}
func (d portableDest) NewFile(p string, perm fs.FileMode, replace bool) (osmodule.File, error) {
	real := d.roots.Path(p)
	f, err := os.OpenFile(tempName(real), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	return &portableFile{f: f, tmp: tempName(real), real: real, replace: replace}, nil
}

type portableFile struct {
	f         *os.File
	tmp, real string
	replace   bool
}

func (w *portableFile) Write(b []byte) (int, error) { return w.f.Write(b) }
func (w *portableFile) Commit() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	if !w.replace {
		if _, err := os.Lstat(w.real); err == nil {
			return fs.ErrExist
		}
	} else if err := os.Remove(w.real); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(w.tmp, w.real)
}
func (w *portableFile) Abort() { _ = w.f.Close(); _ = os.Remove(w.tmp) }

var _ = filepath.Join
