package file

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The progress of a copy measures the destination every few seconds, from a
// goroutine nothing recovers: a size that cannot be read must come back as an
// error, never stop the process.
func TestDirSizeBSaysAFolderIsNotThere(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone") + "/"
	if _, err := DirSizeB(gone); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("DirSizeB of a folder that is not there: %v", err)
	}
}

// A copy writes each file under another name and renames it into place, so the
// folder being measured loses names between the moment it is listed and the
// moment each name is read.
func TestDirSizeBMeasuresAFolderWhoseFilesComeAndGo(t *testing.T) {
	dir := t.TempDir()
	const files = 200
	name := func(i int) string { return filepath.Join(dir, strconv.Itoa(i)) }
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			for i := 0; i < files; i++ {
				_ = os.WriteFile(name(i), []byte("x"), 0o644)
			}
			for i := 0; i < files; i++ {
				_ = os.Remove(name(i))
			}
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	defer func() { close(stop); <-done }()

	for i := 0; i < 300; i++ {
		if size, err := DirSizeB(dir + "/"); err != nil {
			t.Fatalf("round %d: %v", i, err)
		} else if size < 0 || size > files {
			t.Fatalf("round %d: %d bytes in a folder of at most %d", i, size, files)
		}
	}
}
