package file

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

// Copying a file into its own folder is copying it onto itself: the "old copy" it would remove first is the file.
func TestCopyDirNeverRemovesTheFileItCopies(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.jpg")
	writeTestFile(t, src, "a")

	if err := CopyDir(src, dir, "overwrite"); err == nil {
		t.Error("CopyDir() = nil, want an error: the file is its own destination")
	}
	if got, err := os.ReadFile(src); err != nil || string(got) != "a" {
		t.Errorf("the file holds %q (%v), want a", got, err)
	}
}

func TestIsSameOrInsideGoesByWholeNames(t *testing.T) {
	for _, c := range []struct {
		path, dir string
		want      bool
	}{
		{"/DATA/Photo", "/DATA/Photo", true},
		{"/DATA/Photo/2024", "/DATA/Photo", true},
		{"/DATA/Photo2", "/DATA/Photo", false},
		{"/DATA", "/DATA/Photo", false},
		{"/DATA/..Photo", "/DATA", true},
		{"/DATA", "/", true},
		{"DATA/Photo", "/DATA", true}, // cannot be compared: refused
	} {
		if got := isSameOrInside(c.path, c.dir); got != c.want {
			t.Errorf("isSameOrInside(%q, %q) = %v, want %v", c.path, c.dir, got, c.want)
		}
	}
}

func TestOverlapIsTheSourceOrItsFolderUnderTheEntryOrTheDestination(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing") // paths compared as names
	for _, c := range []struct {
		from, to string
		want     bool
	}{
		{"/DATA/Photos", "/DATA/Archive", false},
		{"/DATA/Photos", "/DATA/Photos2", false},
		{"/DATA/Photos2", "/DATA/Photos", false},
		{"/DATA/Photos", "/DATA", true},             // the entry is the source
		{"/DATA/Photos", "/DATA/Photos", true},      // into itself
		{"/DATA/Photos", "/DATA/Photos/2024", true}, // into a folder of its own
		{"/DATA/Movies/Movies", "/DATA", true},      // the entry DATA/Movies holds the source
		{"/DATA/a/b/a", "/DATA", true},              // the entry DATA/a holds it, two levels up
		{"/DATA/Movies/Films/x", "/DATA", false},    // the entry DATA/x does not
		{"/DATA/Movies2/Movies", "/DATA", false},    // nor DATA/Movies, by whole names
	} {
		if got := Overlap(root+c.from, root+c.to); got != c.want {
			t.Errorf("Overlap(%q, %q) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func fakeMountPoints(t *testing.T, points ...string) {
	t.Helper()
	was := mountPointsUnder
	mountPointsUnder = func(dir string) ([]string, error) {
		under := []string{}
		for _, p := range points {
			if isSameOrInside(p, dir) {
				under = append(under, p)
			}
		}
		return under, nil
	}
	t.Cleanup(func() { mountPointsUnder = was })
}

func TestRemoveAllNoCrossRemovesNothingOfATreeThatHoldsAMountPoint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "folder")
	if err := os.MkdirAll(filepath.Join(dir, "usb"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "a.txt"), "a")
	writeTestFile(t, filepath.Join(dir, "usb", "keep.txt"), "k")
	fakeMountPoints(t, filepath.Join(dir, "usb"))

	if err := RemoveAllNoCross(dir); !errors.Is(err, ErrHoldsMountPoint) {
		t.Errorf("RemoveAllNoCross() = %v, want ErrHoldsMountPoint", err)
	}
	for _, name := range []string{"a.txt", "usb/keep.txt"} {
		if !Exists(filepath.Join(dir, name)) {
			t.Errorf("%s was removed", name)
		}
	}

	if err := RemoveAllNoCross(filepath.Join(dir, "a.txt")); err != nil || Exists(filepath.Join(dir, "a.txt")) {
		t.Errorf("RemoveAllNoCross(a file next to the mount point) = %v, exists %v", err, Exists(filepath.Join(dir, "a.txt")))
	}
	if err := RemoveAllNoCross(filepath.Join(dir, "usb")); !errors.Is(err, ErrHoldsMountPoint) {
		t.Errorf("RemoveAllNoCross(the mount point) = %v, want ErrHoldsMountPoint", err)
	}
}

func TestRemoveAllNoCrossRemovesATreeWithoutMountPoint(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "folder")
	writeTestFile(t, filepath.Join(root, "folder2"), "next to it") // a name that only starts like it
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "sub", "a.txt"), "a")
	fakeMountPoints(t, filepath.Join(root, "folder2"))

	if err := RemoveAllNoCross(dir); err != nil {
		t.Fatal(err)
	}
	if Exists(dir) {
		t.Error("the folder is still there")
	}
	if err := RemoveAllNoCross(dir); err != nil {
		t.Errorf("RemoveAllNoCross(a missing path) = %v, want nil as os.RemoveAll", err)
	}
}

func TestWriteFileAtomicFromReplacesTheFileWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	writeTestFile(t, path, "an older and longer file")

	if err := WriteFileAtomicFrom(path, strings.NewReader("new"), 0o640); err != nil {
		t.Fatal(err)
	}

	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Errorf("the file holds %q, want new", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode() != 0o640 {
		t.Errorf("mode %v (%v), want -rw-r-----", info.Mode(), err)
	}
}

func TestOverlapSeesThroughLinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Photos", "2024"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "Photos", "2024"), filepath.Join(root, "inside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "up")); err != nil {
		t.Fatal(err)
	}

	if !Overlap(filepath.Join(root, "Photos"), filepath.Join(root, "inside")) {
		t.Error("Overlap(Photos, a link to Photos/2024) = false: the copy goes into itself")
	}
	if !Overlap(filepath.Join(root, "Photos"), filepath.Join(root, "up")) {
		t.Error("Overlap(Photos, a link to its folder) = false: the entry is the source")
	}
	if Overlap(filepath.Join(root, "Photos", "2024"), filepath.Join(root, "up")) {
		t.Error("Overlap(Photos/2024, a link to the folder above Photos) = true")
	}
}

func TestCopyDirKeepsTheOldCopyWhenTheSourceCannotBeRead(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Photos")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(src, "a.jpg")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "backup")
	if err := os.MkdirAll(filepath.Join(dst, "Photos"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dst, "Photos", "a.jpg"), "old")

	if err := CopyDir(src, dst, "overwrite"); err == nil {
		t.Error("CopyDir() = nil, want the error of a.jpg")
	}
	if got, err := os.ReadFile(filepath.Join(dst, "Photos", "a.jpg")); err != nil || string(got) != "old" {
		t.Errorf("the old copy holds %q (%v), want old", got, err)
	}
}

// A link on the way to the destination leads into the source: the copy meets the folder it made and stops there.
func TestCopyDirNeverCopiesIntoItself(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Photos")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(src, "a.jpg"), "a")
	if err := os.Symlink(filepath.Join(src, "sub"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if err := CopyDir(src, filepath.Join(root, "link", "new"), "overwrite"); err == nil {
		t.Error("CopyDir() = nil, want an error: the copy goes into itself")
	}
	if Exists(filepath.Join(src, "sub", "new", "Photos", "sub", "new", "Photos")) {
		t.Error("the copy went on into what it had copied")
	}
}

func TestCopyDirContextStopsWhenCancelled(t *testing.T) {
	src := filepath.Join(t.TempDir(), "Photos")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(src, "a.jpg"), "a")
	dst := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := CopyDirContext(ctx, src, dst, "overwrite"); !errors.Is(err, context.Canceled) {
		t.Errorf("CopyDirContext() = %v, want context.Canceled", err)
	}
	if Exists(filepath.Join(dst, "Photos", "a.jpg")) {
		t.Error("a.jpg was copied after the cancel")
	}
}

// The destination does not exist yet: the links of its deepest folder that does are resolved.
func TestOverlapResolvesTheLinksOfADestinationToBe(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Photos", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "Photos", "sub"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if !Overlap(filepath.Join(root, "Photos"), filepath.Join(root, "link", "new", "deeper")) {
		t.Error("Overlap(Photos, link-into-Photos/new/deeper) = false: the copy goes into itself")
	}
	if Overlap(filepath.Join(root, "Photos", "sub"), filepath.Join(root, "elsewhere", "new")) {
		t.Error("Overlap(Photos/sub, a destination to be elsewhere) = true")
	}
}

// The copy of a file over an older one is cancelled: the older one stays as it was, whole.
func TestACancelledOverwriteKeepsTheOldFile(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	writeTestFile(t, src, "the new content")
	dst := filepath.Join(root, "dst")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dst, "a.txt"), "the old content")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // as it is seen inside the copy of the file

	if err := copyFile(ctx, src, dst, "overwrite"); !errors.Is(err, context.Canceled) {
		t.Errorf("copyFile() = %v, want context.Canceled", err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "a.txt")); err != nil || string(got) != "the old content" {
		t.Errorf("the old file holds %q (%v), want it as it was", got, err)
	}
	if entries, _ := os.ReadDir(dst); len(entries) != 1 {
		t.Errorf("%d files in the destination, want the old one alone", len(entries))
	}
}

// A FIFO would hold the copy at its open until a writer came (a device or a socket is no file to copy either): it is skipped, the rest is copied,
// and the copy says so.
func TestCopyDirSkipsWhatIsNotARegularFile(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(src, "a.txt"), "a")
	fifo := filepath.Join(src, "b.fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fifo, filepath.Join(src, "c.link")); err != nil { // a link is followed, to what it leads to
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(src, "d.txt"), "d")
	t.Cleanup(func() { // lets a copy held at the open of the FIFO (as it was) go on
		if f, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil {
			f.Close()
		}
	})
	dst := filepath.Join(root, "dst")
	done := make(chan error, 1)

	go func() { done <- CopyDir(src, dst, "overwrite") }()

	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Errorf("CopyDir() = %v, want ErrNotRegular", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the copy is held at the open of the FIFO")
	}
	for name, want := range map[string]bool{"a.txt": true, "b.fifo": false, "c.link": false, "d.txt": true} {
		if got := Exists(filepath.Join(dst, "src", name)); got != want {
			t.Errorf("%s copied: %v, want %v", name, got, want)
		}
	}
}

// A name of 250 bytes (83 characters of Chinese) is a name a disk takes, and so is it for the temporary file a copy or a save writes first.
func TestCopiesAndSavesTakeALongName(t *testing.T) {
	root := t.TempDir()
	name := strings.Repeat("长", 83) + "a" // 3*83+1 bytes
	src := filepath.Join(root, "src", name)
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, src, "new")
	dst := filepath.Join(root, "dst")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dst, name), "old")

	if err := CopyDir(src, dst, "overwrite"); err != nil {
		t.Errorf("CopyDir() = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, name)); string(got) != "new" {
		t.Errorf("the copy holds %q, want new", got)
	}
	if err := WriteFileAtomic(filepath.Join(root, name), []byte("saved")); err != nil {
		t.Errorf("WriteFileAtomic() = %v", err)
	}
	if err := WriteFileAtomicFrom(filepath.Join(root, "x"+name[1:]), strings.NewReader("sent"), 0o644); err != nil {
		t.Errorf("WriteFileAtomicFrom() = %v", err)
	}
}
