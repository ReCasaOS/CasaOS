//go:build linux

package service

import (
	"path/filepath"
	"testing"
)

func TestFreeDiskBytesReadsTheFilesystem(t *testing.T) {
	// through the updater, so that the wiring of the real reader is tested as well
	free, err := newSystemPackageUpdater().freeBytes(t.TempDir())
	if err != nil || free == 0 {
		t.Fatalf("freeBytes() = %d, %v: a temporary directory has room", free, err)
	}
	if _, err := freeDiskBytes(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("freeDiskBytes() read a path that is not there")
	}
}
