package service

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ReCasaOS/CasaOS/model"
)

func writeOperateFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runOperation queues one task the way the route does and runs it.
func runOperation(t *testing.T, op model.FileOperate) {
	t.Helper()
	FileQueue.Store(t.Name(), op)
	t.Cleanup(func() { FileQueue.Delete(t.Name()) })
	FileOperate(t.Name())
}

func move(from, to string) model.FileOperate {
	return model.FileOperate{Type: "move", Item: []model.FileItem{{From: from}}, To: to, Style: "overwrite"}
}

func TestAMoveOnTheSameDiskIsARename(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Photos")
	writeOperateFile(t, filepath.Join(src, "a.jpg"), "a")
	before, err := os.Stat(filepath.Join(src, "a.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(root, "Archive")
	if err := os.Mkdir(to, 0o755); err != nil {
		t.Fatal(err)
	}

	runOperation(t, move(src, to))

	after, err := os.Stat(filepath.Join(to, "Photos", "a.jpg"))
	if err != nil {
		t.Fatalf("the moved file: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Error("the file was copied and removed, not renamed")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("the source is still there (%v)", err)
	}
}

// Moving a folder into a folder of its own cannot be done; nothing of it may be removed.
func TestAMoveIntoItsOwnFolderKeepsTheSource(t *testing.T) {
	src := filepath.Join(t.TempDir(), "Photos")
	writeOperateFile(t, filepath.Join(src, "a.jpg"), "a")
	to := filepath.Join(src, "2024")
	if err := os.Mkdir(to, 0o755); err != nil {
		t.Fatal(err)
	}

	runOperation(t, move(src, to))

	if got, err := os.ReadFile(filepath.Join(src, "a.jpg")); err != nil || string(got) != "a" {
		t.Errorf("the source lost a.jpg: %q, %v", got, err)
	}
}

// acrossDisks makes the rename of a move fail as it does between two file systems.
func acrossDisks(t *testing.T) {
	t.Helper()
	was := fileMoveRename
	fileMoveRename = func(from, to string) error {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { fileMoveRename = was })
}

func TestAMoveAcrossDisksCopiesThenRemovesTheSource(t *testing.T) {
	acrossDisks(t)
	root := t.TempDir()
	src := filepath.Join(root, "Photos")
	writeOperateFile(t, filepath.Join(src, "2024", "a.jpg"), "a")
	to := filepath.Join(root, "usb")
	if err := os.Mkdir(to, 0o755); err != nil {
		t.Fatal(err)
	}

	runOperation(t, move(src, to))

	if got, err := os.ReadFile(filepath.Join(to, "Photos", "2024", "a.jpg")); err != nil || string(got) != "a" {
		t.Errorf("the copy holds %q (%v), want a", got, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("the source is still there (%v)", err)
	}
}

// The disk of the destination fills up halfway: the source stays whole, and the half copy goes.
func TestAMoveAcrossDisksKeepsTheSourceWhenTheCopyFails(t *testing.T) {
	acrossDisks(t)
	root := t.TempDir()
	src := filepath.Join(root, "Photos")
	writeOperateFile(t, filepath.Join(src, "a.jpg"), "a")
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(src, "b.jpg")); err != nil { // cannot be read: the copy fails there
		t.Fatal(err)
	}
	writeOperateFile(t, filepath.Join(src, "c.jpg"), "c")
	to := filepath.Join(root, "usb")
	if err := os.Mkdir(to, 0o755); err != nil {
		t.Fatal(err)
	}

	runOperation(t, move(src, to))

	for _, name := range []string{"a.jpg", "c.jpg"} {
		if _, err := os.Stat(filepath.Join(src, name)); err != nil {
			t.Errorf("the source lost %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(to, "Photos")); !os.IsNotExist(err) {
		t.Errorf("the half copy is still there (%v)", err)
	}
}
