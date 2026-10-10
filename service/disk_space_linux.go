//go:build linux

package service

import "syscall"

// freeDiskBytes is the room left on the filesystem of path for a program that is not root
// (what the filesystem keeps for root is not counted).
func freeDiskBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
