//go:build !linux

package service

import "errors"

// dpkgLockHeld: only a Linux host has dpkg's lock to look at.
func dpkgLockHeld() bool { return false }

// dpkgLockStatus says it could not look.
func dpkgLockStatus() (bool, error) {
	return false, errors.New("dpkg's lock can only be looked at on Linux")
}
