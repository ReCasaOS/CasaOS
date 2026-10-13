//go:build !linux

package service

// syncFolder: a folder cannot be synced where upgrades are not run; the file itself was.
func syncFolder(string) error { return nil }
