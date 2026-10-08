//go:build !linux

package service

// dpkgLockHeld: only a Linux host has dpkg's lock to look at.
func dpkgLockHeld() bool { return false }
