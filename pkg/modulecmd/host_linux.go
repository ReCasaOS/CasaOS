//go:build linux

package modulecmd

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"golang.org/x/sys/unix"
)

// linuxHost is what only Linux has.
type linuxHost struct{}

func newHost() Host { return linuxHost{} }

// Lock takes an exclusive flock on the file at path, not waiting. The kernel drops the lock when the process that holds it ends, however it ends, so a lock left by a process that is dead
// is no lock: the file stays where it is and nothing has to be decided about it. The descriptor is not inherited by the commands the process runs.
func (linuxHost) Lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLockHeld
		}
		return nil, err
	}
	// who has it, for whoever looks
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return func() { _ = f.Close() }, nil
}

func (linuxHost) FreeBytes(path string) (int64, error) {
	for {
		var st unix.Statfs_t
		err := unix.Statfs(path, &st)
		if err == nil {
			return int64(st.Bavail) * int64(st.Bsize), nil //nolint:unconvert // the widths are the platform's
		}
		if !errors.Is(err, fs.ErrNotExist) || path == "/" || path == "." {
			return 0, err
		}
		path = filepath.Dir(path)
	}
}

// DpkgLockHeld asks the kernel, with F_GETLK, whether another process holds a lock on any of the files. apt and dpkg take POSIX record locks, which flock does not see. The files are
// opened to read and nothing is taken; a file that is not there is not held; one that is there and cannot be asked about is an error, unless another is seen held.
func (linuxHost) DpkgLockHeld(paths []string) (bool, error) {
	var first error
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) && first == nil {
				first = err
			}
			continue
		}
		lock := unix.Flock_t{Type: unix.F_RDLCK}
		err = unix.FcntlFlock(f.Fd(), unix.F_GETLK, &lock)
		_ = f.Close()
		switch {
		case err != nil:
			if first == nil {
				first = &fs.PathError{Op: "fcntl", Path: p, Err: err}
			}
		case lock.Type != unix.F_UNLCK:
			return true, nil
		}
	}
	return false, first
}

func (linuxHost) NewDest(prefix string) osmodule.Destination { return linuxDest{prefix: prefix} }

func (linuxHost) IsTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}
