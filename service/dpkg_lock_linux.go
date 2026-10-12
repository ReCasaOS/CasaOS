//go:build linux

package service

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// dpkgLockFiles are the files apt and dpkg lock while they work: the frontend lock held for
// a whole apt run, dpkg's own, and the lists' lock held while the index is refreshed.
var dpkgLockFiles = []string{"/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock", "/var/lib/apt/lists/lock"}

// dpkgLockHeld says whether a package manager is working: one of its lock files is locked
// by another process.
func dpkgLockHeld() bool {
	return anyLockHeld(dpkgLockFiles)
}

// dpkgLockStatus is dpkgLockHeld that says when it could not look.
func dpkgLockStatus() (bool, error) {
	return lockStatus(dpkgLockFiles)
}

// anyLockHeld asks the kernel, with F_GETLK, whether another process holds a lock on any
// of the files. apt and dpkg take POSIX record locks (fcntl), which flock(1) and flock(2)
// do not see: they are a different kind of lock. The probe opens the file read-only, takes
// nothing and creates nothing; a file that is not there, or cannot be opened, is not held (the
// update of ReCasaOS refuses nothing for what it cannot see: lockStatus is for who must know).
// A read lock is asked for, which conflicts with the write lock they hold.
func anyLockHeld(paths []string) bool {
	held, _ := lockStatus(paths)
	return held
}

// getLock is F_GETLK: a variable for a test to say that the kernel will not (a filesystem that does
// not do locks, a network one without its daemon).
var getLock = func(fd uintptr, lock *syscall.Flock_t) error {
	return syscall.FcntlFlock(fd, syscall.F_GETLK, lock)
}

// lockStatus is anyLockHeld that says what it could not look at: a file that is there and cannot be
// opened, or that the kernel will not say the lock of, is an error (the first one), unless another
// file is seen held, which is an answer. A file that is not there is not an error: it has no lock.
func lockStatus(paths []string) (held bool, err error) {
	for _, path := range paths {
		file, openErr := os.Open(path)
		if openErr != nil {
			if !errors.Is(openErr, fs.ErrNotExist) && err == nil {
				err = openErr
			}
			continue
		}
		lock := syscall.Flock_t{Type: syscall.F_RDLCK, Whence: 0, Start: 0, Len: 0}
		lockErr := getLock(file.Fd(), &lock)
		_ = file.Close()
		switch {
		case lockErr != nil:
			if err == nil {
				err = &fs.PathError{Op: "fcntl", Path: path, Err: lockErr}
			}
		case lock.Type != syscall.F_UNLCK:
			return true, nil
		}
	}
	return false, err
}
