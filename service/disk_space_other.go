//go:build !linux

package service

import "errors"

// freeDiskBytes: only a Linux host has the room to look at; elsewhere it cannot be read.
func freeDiskBytes(string) (uint64, error) {
	return 0, errors.New("free disk space is read on Linux only")
}
