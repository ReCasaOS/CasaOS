package file

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A move removes its source once the copy says it is done: a copy that fails halfway (a full disk, a file it cannot read) must say so.
func TestCopyDirStopsAtTheFirstErrorAndReportsIt(t *testing.T) {
	src := filepath.Join(t.TempDir(), "Photos")
	if err := os.MkdirAll(filepath.Join(src, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(src, "a.jpg"), "a")
	// a link to nothing cannot be read: the copy of the folder b fails
	if err := os.Symlink(filepath.Join(src, "missing"), filepath.Join(src, "b", "broken.jpg")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(src, "c.jpg"), "c")
	dst := t.TempDir()

	if err := CopyDir(src, dst, "overwrite"); err == nil {
		t.Fatal("CopyDir() = nil, want the error of b/broken.jpg")
	}
	if Exists(filepath.Join(dst, "Photos", "c.jpg")) {
		t.Error("CopyDir() went on after the error")
	}
}

func TestCopyDirReportsTheErrorOfASingleFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.jpg")
	writeTestFile(t, src, "a")

	if err := CopyDir(src, filepath.Join(t.TempDir(), "missing"), "overwrite"); err == nil {
		t.Fatal("CopyDir() = nil, want the error of a destination that does not exist")
	}
}
