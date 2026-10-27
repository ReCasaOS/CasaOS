//go:build linux

package trash

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// disk is a temporary folder that the package takes for a mount point.
func disk(t *testing.T) string {
	t.Helper()
	top, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	saved := mountPoints
	mountPoints = func() ([]mount, error) { return []mount{{point: top, local: true}}, nil }
	t.Cleanup(func() { mountPoints = saved })
	return top
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func only(t *testing.T) Item {
	t.Helper()
	items := List()
	if len(items) != 1 {
		t.Fatalf("%d items in the trash: %+v", len(items), items)
	}
	return items[0]
}

func TestAFileGoesToTheTrashOfItsDiskAndComesBack(t *testing.T) {
	top := disk(t)
	path := filepath.Join(top, "Documents", "note.txt")
	write(t, path, "hello")
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.Local)

	if err := Put(path, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("still there: %v", err)
	}
	trashed := filepath.Join(top, dirName(), "files", "note.txt")
	if read(t, trashed) != "hello" {
		t.Fatal("not in the trash")
	}
	info := read(t, filepath.Join(top, dirName(), "info", "note.txt.trashinfo"))
	if !strings.Contains(info, "Path=Documents/note.txt\n") || !strings.Contains(info, "DeletionDate=2026-10-27T10:00:00\n") {
		t.Fatalf("info: %q", info)
	}
	if st, _ := os.Stat(filepath.Join(top, dirName())); st.Mode().Perm() != 0o700 {
		t.Errorf("trash folder mode %v", st.Mode().Perm())
	}

	item := only(t)
	if item.Trash != top || item.Name != "note.txt" || item.Path != path || item.Size != 5 || item.IsDir || !item.DeletedAt.Equal(now) {
		t.Fatalf("item %+v", item)
	}

	got, err := Restore(top, "note.txt", nil, nil)
	if err != nil || got != path || read(t, path) != "hello" {
		t.Fatalf("restored to %q: %v", got, err)
	}
	if len(List()) != 0 {
		t.Fatal("the trash is not empty")
	}
	if _, err := os.Lstat(filepath.Join(top, dirName(), "info", "note.txt.trashinfo")); !os.IsNotExist(err) {
		t.Fatal("the info stayed")
	}
}

func TestTwoItemsWithOneNameKeepTheirOwnPlaces(t *testing.T) {
	top := disk(t)
	a, b := filepath.Join(top, "a", "x.txt"), filepath.Join(top, "b", "x.txt")
	write(t, a, "a")
	write(t, b, "bb")
	if err := Put(a, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := Put(b, time.Now()); err != nil {
		t.Fatal(err)
	}
	items := List()
	if len(items) != 2 {
		t.Fatalf("%+v", items)
	}
	for _, it := range items {
		if _, err := Restore(top, it.Name, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if read(t, a) != "a" || read(t, b) != "bb" {
		t.Fatal("not back in their own folders")
	}
}

func TestARestoreToATakenNameOrAGoneFolder(t *testing.T) {
	top := disk(t)
	file := filepath.Join(top, "photo.jpg")
	folder := filepath.Join(top, "gone", "album")
	write(t, file, "old")
	write(t, filepath.Join(folder, "1.jpg"), "1")
	if err := Put(file, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := Put(folder, time.Now()); err != nil {
		t.Fatal(err)
	}
	write(t, file, "new")
	if err := os.RemoveAll(filepath.Join(top, "gone")); err != nil {
		t.Fatal(err)
	}

	got, err := Restore(top, "photo.jpg", nil, nil)
	if err != nil || got != filepath.Join(top, "photo (restored).jpg") || read(t, got) != "old" || read(t, file) != "new" {
		t.Fatalf("restored to %q: %v", got, err)
	}
	got, err = Restore(top, "album", nil, nil)
	if err != nil || got != folder || read(t, filepath.Join(folder, "1.jpg")) != "1" {
		t.Fatalf("restored to %q: %v", got, err)
	}
}

func TestAFolderSizeIsItsFiles(t *testing.T) {
	top := disk(t)
	folder := filepath.Join(top, "f")
	write(t, filepath.Join(folder, "a"), "123")
	write(t, filepath.Join(folder, "sub", "b"), "4567")
	if err := Put(folder, time.Now()); err != nil {
		t.Fatal(err)
	}
	if item := only(t); !item.IsDir || item.Size != 7 {
		t.Fatalf("%+v", item)
	}
}

func TestADeleteRemovesTheTreeButNotWhatALinkLeadsTo(t *testing.T) {
	top := disk(t)
	outside := filepath.Join(t.TempDir(), "keep.txt")
	write(t, outside, "keep")
	folder := filepath.Join(top, "f")
	write(t, filepath.Join(folder, "sub", "a"), "a")
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(folder, "link")); err != nil {
		t.Fatal(err)
	}
	if err := Put(folder, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := Delete(top, "f", nil); err != nil {
		t.Fatal(err)
	}
	if read(t, outside) != "keep" {
		t.Fatal("the link was followed")
	}
	entries, _ := os.ReadDir(filepath.Join(top, dirName(), "files"))
	infos, _ := os.ReadDir(filepath.Join(top, dirName(), "info"))
	if len(entries)+len(infos) != 0 {
		t.Fatalf("left %v %v", entries, infos)
	}
	if err := Delete(top, "f", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a second delete: %v", err)
	}
}

func TestPurgeRemovesWhatIsOlderThanTheCutoff(t *testing.T) {
	top := disk(t)
	old, recent := filepath.Join(top, "old.txt"), filepath.Join(top, "recent.txt")
	write(t, old, "o")
	write(t, recent, "r")
	now := time.Now()
	if err := Put(old, now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := Put(recent, now); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(top, dirName(), "info", "lost.trashinfo")
	write(t, orphan, string(infoText("lost", now.Add(-time.Hour))))
	if err := os.Chtimes(orphan, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Put writes the info before it moves the item: a fresh info without its item is kept, even empty
	moving := filepath.Join(top, dirName(), "info", "moving.trashinfo")
	write(t, moving, "")

	n, err := Purge(now.Add(-Retention))
	if err != nil || n != 1 {
		t.Fatalf("purged %d: %v", n, err)
	}
	if item := only(t); item.Name != "recent.txt" {
		t.Fatalf("kept %+v", item)
	}
	if _, err := os.Lstat(orphan); !os.IsNotExist(err) {
		t.Fatal("an info without its item stayed")
	}
	if _, err := os.Lstat(moving); err != nil {
		t.Fatal("the info of an item being moved was removed")
	}
}

func TestEmptyRemovesOnlyWhatIsAllowed(t *testing.T) {
	top := disk(t)
	mine, theirs := filepath.Join(top, "mine", "a"), filepath.Join(top, "theirs", "b")
	write(t, mine, "a")
	write(t, theirs, "b")
	for _, p := range []string{mine, theirs} {
		if err := Put(p, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	allowed := func(p string) bool { return inside(p, filepath.Join(top, "mine")) }
	if n, err := Empty(allowed); err != nil || n != 1 {
		t.Fatalf("emptied %d: %v", n, err)
	}
	if item := only(t); item.Path != theirs {
		t.Fatalf("kept %+v", item)
	}
	if _, err := Restore(top, "b", allowed, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restored what is not allowed: %v", err)
	}
	if err := Delete(top, "b", allowed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted what is not allowed: %v", err)
	}
	if n, err := Empty(nil); err != nil || n != 1 || len(List()) != 0 {
		t.Fatalf("emptied %d: %v", n, err)
	}
}

func TestATrashFolderThatIsALinkIsNotUsed(t *testing.T) {
	top := disk(t)
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(top, dirName())); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(top, "a.txt")
	write(t, path, "a")
	if err := Put(path, time.Now()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Put: %v", err)
	}
	if read(t, path) != "a" {
		t.Fatal("moved")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("wrote through the link: %v", entries)
	}
}

func TestAPathThatIsGoneSaysSo(t *testing.T) {
	top := disk(t)
	if err := Put(filepath.Join(top, "nothing"), time.Now()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Put: %v", err)
	}
}

func TestWhatIsInTheTrashIsNotPutThereAgain(t *testing.T) {
	top := disk(t)
	path := filepath.Join(top, "a.txt")
	write(t, path, "a")
	if err := Put(path, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(top, dirName(), "files", "a.txt"), filepath.Join(top, dirName())} {
		if err := Put(p, time.Now()); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Put(%s): %v", p, err)
		}
	}
}

func TestAnInfoThatLeadsOffTheDiskIsNotRestored(t *testing.T) {
	top := disk(t)
	path := filepath.Join(top, "a.txt")
	write(t, path, "a")
	if err := Put(path, time.Now()); err != nil {
		t.Fatal(err)
	}
	infoPath := filepath.Join(top, dirName(), "info", "a.txt.trashinfo")
	escape := strings.Repeat("../", strings.Count(top, "/")) + "tmp/" + fmt.Sprint(time.Now().UnixNano())
	write(t, infoPath, string(infoText(escape, time.Now())))
	if _, err := Restore(top, "a.txt", nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Restore: %v", err)
	}
	if read(t, filepath.Join(top, dirName(), "files", "a.txt")) != "a" {
		t.Fatal("moved")
	}
}

func TestACheckThatRefusesStopsTheRestore(t *testing.T) {
	top := disk(t)
	path := filepath.Join(top, "a.txt")
	write(t, path, "a")
	if err := Put(path, time.Now()); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("outside the roots")
	if _, err := Restore(top, "a.txt", nil, func(string) (string, error) { return "", refused }); !errors.Is(err, refused) {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("restored")
	}
}

func TestOnlyAMountPointWithATrashIsOpened(t *testing.T) {
	top := disk(t)
	if _, err := Restore(filepath.Join(top, "sub"), "a", nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a folder that is not a mount point: %v", err)
	}
	if _, err := Restore(top, "../a", nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a name that is a path: %v", err)
	}
}

func TestARestoreDoesNotFollowALinkPutInThePlaceOfAFolder(t *testing.T) {
	top := disk(t)
	path := filepath.Join(top, "share", "doc.txt")
	write(t, path, "secret")
	if err := Put(path, time.Now()); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(top, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(top, "share")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(top, "share")); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(top, "doc.txt", nil, nil); !errors.Is(err, ErrOutsideDisk) {
		t.Fatalf("Restore: %v", err)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("restored through the link: %v", entries)
	}
	if read(t, filepath.Join(top, dirName(), "files", "doc.txt")) != "secret" {
		t.Fatal("not in the trash any more")
	}
}

func TestAnItemDeletedThroughALinkRemembersItsRealFolder(t *testing.T) {
	top := disk(t)
	real := filepath.Join(top, "real", "a.txt")
	write(t, real, "a")
	if err := os.Symlink(filepath.Join(top, "real"), filepath.Join(top, "link")); err != nil {
		t.Fatal(err)
	}
	if err := Put(filepath.Join(top, "link", "a.txt"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if item := only(t); item.Path != real {
		t.Fatalf("%+v", item)
	}
}

func TestANetworkOrSystemMountGetsNoTrash(t *testing.T) {
	top := disk(t)
	network, system := filepath.Join(top, "nfs"), filepath.Join(top, "sys")
	write(t, filepath.Join(network, "a"), "a")
	write(t, filepath.Join(system, "b"), "b")
	mountPoints = func() ([]mount, error) {
		return []mount{{point: top, local: true}, {point: network, local: false}, {point: system, local: true}}, nil
	}
	saved := skipped
	skipped = append(append([]string{}, skipped...), system)
	t.Cleanup(func() { skipped = saved })
	for _, p := range []string{filepath.Join(network, "a"), filepath.Join(system, "b")} {
		if err := Put(p, time.Now()); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Put(%s): %v", p, err)
		}
		if _, err := os.Lstat(filepath.Join(filepath.Dir(p), dirName())); !os.IsNotExist(err) {
			t.Errorf("a trash was made in %s", filepath.Dir(p))
		}
	}
	for _, m := range []string{"nfs4", "cifs", "fuse.rclone", "fuse.sshfs", "autofs"} {
		if isLocal(m) {
			t.Errorf("%s is local", m)
		}
	}
	for _, m := range []string{"ext4", "btrfs", "xfs", "vfat", "exfat", "fuseblk", "fuse.mergerfs", "tmpfs"} {
		if !isLocal(m) {
			t.Errorf("%s is not local", m)
		}
	}
}

func TestAnItemThatTookAPurgedNameIsNotRemoved(t *testing.T) {
	top := disk(t)
	path := filepath.Join(top, "a.txt")
	write(t, path, "old")
	if err := Put(path, time.Now()); err != nil {
		t.Fatal(err)
	}
	d, err := open(top)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	stale := d.entries()[0]
	if _, err := Restore(top, "a.txt", nil, nil); err != nil {
		t.Fatal(err)
	}
	// kept aside, so that the new file cannot get its inode number back
	if err := os.Rename(path, path+".kept"); err != nil {
		t.Fatal(err)
	}
	write(t, path, "new")
	if err := Put(path, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.remove(stale); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove: %v", err)
	}
	if read(t, filepath.Join(top, dirName(), "files", "a.txt")) != "new" {
		t.Fatal("the new item was removed")
	}
}

func TestAnEntryThatSaysItCameFromAnotherDiskIsNotShown(t *testing.T) {
	top := disk(t)
	path := filepath.Join(top, "a.txt")
	write(t, path, "a")
	if err := Put(path, time.Now().Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(top, dirName(), "info", "a.txt.trashinfo"), string(infoText("../../../../../../../../DATA/a.txt", time.Now())))
	if items := List(); len(items) != 0 {
		t.Fatalf("listed %+v", items)
	}
	if err := Delete(top, "a.txt", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete: %v", err)
	}
	if n, err := Purge(time.Now()); err != nil || n != 0 {
		t.Fatalf("purged %d: %v", n, err)
	}
}
