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

// mountForTest mounts source on target for the test, or skips it where the tests may not mount.
func mountForTest(t *testing.T, source, target, fstype string, flags uintptr) {
	t.Helper()
	if err := syscall.Mount(source, target, fstype, flags, ""); err != nil {
		t.Skipf("cannot mount here: %v", err)
	}
	t.Cleanup(func() { syscall.Unmount(target, syscall.MNT_DETACH) })
}

// Cut Downloads/Movies/Movies, go up to Downloads and paste (overwrite is the default): the entry the move writes, Downloads/Movies, holds the source.
func TestAMoveIntoAFolderWhoseEntryHoldsTheSourceRemovesNothing(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "Downloads", "Movies")
	writeOperateFile(t, filepath.Join(outer, "Movies", "a.mkv"), "a")
	writeOperateFile(t, filepath.Join(outer, "other.mkv"), "o")

	runOperation(t, move(filepath.Join(outer, "Movies"), filepath.Join(root, "Downloads")))

	for _, p := range []string{filepath.Join(outer, "Movies", "a.mkv"), filepath.Join(outer, "other.mkv")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("lost: %v", err)
		}
	}
}

func TestAMoveOrCopyIntoALinkToTheSourceFolderRemovesNothing(t *testing.T) {
	for _, kind := range []string{"move", "copy"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			src := filepath.Join(root, "Photos")
			writeOperateFile(t, filepath.Join(src, "a.jpg"), "a")
			writeOperateFile(t, filepath.Join(src, "b.jpg"), "b")
			link := filepath.Join(root, "link")
			if err := os.Symlink(root, link); err != nil {
				t.Fatal(err)
			}

			runOperation(t, model.FileOperate{Type: kind, Item: []model.FileItem{{From: src}}, To: link, Style: "overwrite"})

			for _, name := range []string{"a.jpg", "b.jpg"} {
				if got, err := os.ReadFile(filepath.Join(src, name)); err != nil || len(got) != 1 {
					t.Errorf("lost %s: %q, %v", name, got, err)
				}
			}
		})
	}
}

// A bind mount of the source's folder is the same folder under another name, which no path comparison sees.
func TestAMoveOrCopyIntoABindMountOfTheSourceFolderRemovesNothing(t *testing.T) {
	for _, kind := range []string{"move", "copy"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			src := filepath.Join(root, "data", "Photos")
			writeOperateFile(t, filepath.Join(src, "a.jpg"), "a")
			other := filepath.Join(root, "other")
			if err := os.Mkdir(other, 0o755); err != nil {
				t.Fatal(err)
			}
			mountForTest(t, filepath.Join(root, "data"), other, "", syscall.MS_BIND)

			runOperation(t, model.FileOperate{Type: kind, Item: []model.FileItem{{From: src}}, To: other, Style: "overwrite"})

			if got, err := os.ReadFile(filepath.Join(src, "a.jpg")); err != nil || string(got) != "a" {
				t.Errorf("lost a.jpg: %q, %v", got, err)
			}
		})
	}
}

// The folder a move overwrites holds a mounted disk: the files on that disk are not the folder's to remove.
func TestAMoveDoesNotEmptyADiskMountedInTheFolderItOverwrites(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Photos")
	writeOperateFile(t, filepath.Join(src, "a.jpg"), "a")
	to := filepath.Join(root, "Archive")
	usb := filepath.Join(to, "Photos", "usb")
	if err := os.MkdirAll(usb, 0o755); err != nil {
		t.Fatal(err)
	}
	mountForTest(t, "tmpfs", usb, "tmpfs", 0)
	writeOperateFile(t, filepath.Join(usb, "keep.jpg"), "k")

	runOperation(t, move(src, to))

	if _, err := os.Stat(filepath.Join(usb, "keep.jpg")); err != nil {
		t.Errorf("the mounted disk lost keep.jpg: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "a.jpg")); err != nil {
		t.Errorf("the source lost a.jpg: %v", err)
	}
}

// The progress of a waiting task counts what is already at its destination: that does not mean it ran.
func TestAQueuedTaskWhoseProgressWasCountedStillRuns(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.jpg")
	writeOperateFile(t, src, "a")
	to := filepath.Join(root, "Archive")
	if err := os.Mkdir(to, 0o755); err != nil {
		t.Fatal(err)
	}

	runOperation(t, model.FileOperate{Type: "copy", Item: []model.FileItem{{From: src}}, To: to, Style: "overwrite", TotalSize: 1, ProcessedSize: 1})

	if got, err := os.ReadFile(filepath.Join(to, "a.jpg")); err != nil || string(got) != "a" {
		t.Errorf("not copied: %q, %v", got, err)
	}
}

// The destination does not exist yet, and a link on its way leads into the source: the copy would go into itself until the disk is full.
func TestACopyThroughALinkIntoTheSourceWritesNothingThere(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Photos")
	writeOperateFile(t, filepath.Join(src, "a.jpg"), "a")
	if err := os.Mkdir(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(src, "sub"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	runOperation(t, model.FileOperate{Type: "copy", Item: []model.FileItem{{From: src}}, To: filepath.Join(root, "link", "new"), Style: "overwrite"})

	if _, err := os.Lstat(filepath.Join(src, "sub", "new")); !os.IsNotExist(err) {
		t.Errorf("the copy wrote into its source (%v)", err)
	}
}

// Across disks a folder is copied before its source is removed: when the source holds a mounted disk it cannot be removed, so it is not copied either.
func TestAMoveAcrossDisksOfAFolderHoldingAMountedDiskCopiesNothing(t *testing.T) {
	acrossDisks(t)
	root := t.TempDir()
	src := filepath.Join(root, "Photos")
	writeOperateFile(t, filepath.Join(src, "a.jpg"), "a")
	usb := filepath.Join(src, "usb")
	if err := os.Mkdir(usb, 0o755); err != nil {
		t.Fatal(err)
	}
	mountForTest(t, "tmpfs", usb, "tmpfs", 0)
	writeOperateFile(t, filepath.Join(usb, "keep.jpg"), "k")
	to := filepath.Join(root, "other-disk")
	if err := os.Mkdir(to, 0o755); err != nil {
		t.Fatal(err)
	}

	runOperation(t, move(src, to))

	if _, err := os.Lstat(filepath.Join(to, "Photos")); !os.IsNotExist(err) {
		t.Errorf("a copy was left at the destination (%v)", err)
	}
	for _, p := range []string{filepath.Join(src, "a.jpg"), filepath.Join(usb, "keep.jpg")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("lost: %v", err)
		}
	}
}
