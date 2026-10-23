package service

import (
	"os"
	"path/filepath"
	"testing"
)

// A mount point is removed after its unmount only when it is an empty folder that is no longer mounted:
// a mount that is still there (busy, or its unmount failed) keeps what it holds, which on a network share
// are the server's files, and a file is never removed.
func TestAMountPointIsRemovedOnlyEmptyAndUnmounted(t *testing.T) {
	root := t.TempDir()
	empty, full, stillMounted := filepath.Join(root, "Old"), filepath.Join(root, "Photos"), filepath.Join(root, "Busy")
	aFile := filepath.Join(root, "notes.txt")
	for _, dir := range []string{empty, full, stillMounted} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{filepath.Join(full, "a.jpg"), aFile} {
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	previous := isMountPoint
	isMountPoint = func(dir string) (bool, error) { return dir == stillMounted, nil }
	t.Cleanup(func() { isMountPoint = previous })

	for _, dir := range []string{empty, full, stillMounted, aFile} {
		_ = RemoveMountPoint(dir)
	}

	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Errorf("the empty unmounted folder is still there: %v", err)
	}
	for _, kept := range []string{filepath.Join(full, "a.jpg"), stillMounted, aFile} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s is gone: %v", kept, err)
		}
	}
}
