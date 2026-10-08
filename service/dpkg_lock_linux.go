//go:build linux

package service

import (
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

// anyLockHeld asks the kernel, with F_GETLK, whether another process holds a lock on any
// of the files. apt and dpkg take POSIX record locks (fcntl), which flock(1) and flock(2)
// do not see: they are a different kind of lock. The probe opens the file read-only, takes
// nothing and creates nothing; a file that is not there, or cannot be opened, is not held.
// A read lock is asked for, which conflicts with the write lock they hold.
func anyLockHeld(paths []string) bool {
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		lock := syscall.Flock_t{Type: syscall.F_RDLCK, Whence: 0, Start: 0, Len: 0}
		err = syscall.FcntlFlock(file.Fd(), syscall.F_GETLK, &lock)
		_ = file.Close()
		if err == nil && lock.Type != syscall.F_UNLCK {
			return true
		}
	}
	return false
}
