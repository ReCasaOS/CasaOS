//go:build linux

package trash

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
	"github.com/moby/sys/mountinfo"
	"golang.org/x/sys/unix"
)

// skipped: mount points never searched for a trash, and no trash made there
// (kernel, runtime and container file systems).
var skipped = []string{"/proc", "/sys", "/dev", "/run", "/var/lib/docker", "/snap"}

// mount is a mount point; local is false for a network or virtual file
// system, which gets no trash: it can hang when opened, and a trash there
// would keep deleted files on the remote side.
type mount struct {
	point string
	local bool
}

// remote are the file system types that get no trash. FUSE ones do not
// either, but for mergerfs (a pool of local disks) and fuseblk (NTFS or exFAT
// disks).
var remote = map[string]bool{
	"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true, "9p": true, "ceph": true,
	"glusterfs": true, "davfs": true, "afs": true, "autofs": true, "fuse": true,
}

func isLocal(fsType string) bool {
	if fsType == "fuse.mergerfs" || fsType == "fuseblk" {
		return true
	}
	return !remote[fsType] && !strings.HasPrefix(fsType, "fuse.")
}

// mountPoints lists the mount points in mount order, a var for the tests.
var mountPoints = func() ([]mount, error) {
	mounts, err := mountinfo.GetMounts(nil)
	if err != nil {
		return nil, err
	}
	points := make([]mount, 0, len(mounts))
	for _, m := range mounts {
		points = append(points, mount{point: m.Mountpoint, local: isLocal(m.FSType)})
	}
	return points, nil
}

func unavailable(err error) error { return fmt.Errorf("%w: %v", ErrUnavailable, err) }

var (
	// errAnotherDisk: another disk is mounted on the way (a btrfs subvolume): no trash across it.
	errAnotherDisk = errors.New("another disk is mounted there")
	// errLinkInWay: a folder of the way is a link or a file now: the path changed under us.
	errLinkInWay = errors.New("a link or a file stands where a folder was")
)

// mountOf is the mount point that holds path (a real path): the deepest one,
// the last mounted when several are at the same place. A network or virtual
// file system, or a skipped place, has no trash.
func mountOf(path string) (string, error) {
	points, err := mountPoints()
	if err != nil {
		return "", err
	}
	var best *mount
	for i, p := range points {
		if inside(path, p.point) && (best == nil || len(p.point) >= len(best.point)) {
			best = &points[i]
		}
	}
	switch {
	case best == nil:
		return "", unavailable(fmt.Errorf("%s is on no mount point", path))
	case !best.local:
		return "", unavailable(fmt.Errorf("%s is a network or virtual file system", best.point))
	case isSkipped(best.point):
		return "", unavailable(fmt.Errorf("%s is a system folder", best.point))
	}
	return best.point, nil
}

// trashes are the local mount points that hold a trash, each trash once (a
// disk mounted twice shows the same one).
//
// shortcut: one disk mounted whole at two places lists its items under the
// first place in mount order, even those deleted under the other; give each
// item the place it was deleted under if such a box shows up.
func trashes() []*dir {
	points, err := mountPoints()
	if err != nil {
		return nil
	}
	seen := map[[2]uint64]bool{}
	var found []*dir
	for _, m := range points {
		if !m.local || isSkipped(m.point) {
			continue
		}
		d, err := openDir(m.point, false)
		if err != nil {
			continue
		}
		if seen[d.id] {
			d.close()
			continue
		}
		seen[d.id] = true
		found = append(found, d)
	}
	return found
}

func isSkipped(top string) bool {
	for _, s := range skipped {
		if inside(top, s) {
			return true
		}
	}
	return false
}

// dir is an open trash: its mount point, its files and info folders, held by
// descriptor so that nothing swapped in their path afterwards is followed.
type dir struct {
	top   string
	topFD int
	files int
	info  int
	dev   uint64
	id    [2]uint64 // device and inode of the trash folder
}

func (d *dir) close() {
	unix.Close(d.topFD)
	unix.Close(d.files)
	unix.Close(d.info)
}

// openDir opens the trash at the mount point top, made when create. A trash
// folder that is a link, is not ours or sits on another disk is refused
// (ErrUnavailable); a missing one is fs.ErrNotExist when !create.
func openDir(top string, create bool) (*dir, error) {
	topFD, err := unix.Open(top, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, unavailable(err)
	}
	d, err := openIn(topFD, create)
	if err != nil {
		unix.Close(topFD)
		return nil, err
	}
	d.top = top
	return d, nil
}

func openIn(topFD int, create bool) (*dir, error) {
	var st unix.Stat_t
	if err := unix.Fstat(topFD, &st); err != nil {
		return nil, unavailable(err)
	}
	base, err := sub(topFD, dirName(), create, st.Dev)
	if err != nil {
		return nil, err
	}
	defer unix.Close(base)
	var bst unix.Stat_t
	if err := unix.Fstat(base, &bst); err != nil {
		return nil, unavailable(err)
	}
	if bst.Mode&0o077 != 0 {
		_ = unix.Fchmod(base, 0o700) // best effort: a FAT disk has no modes
	}
	files, err := sub(base, "files", create, st.Dev)
	if err != nil {
		return nil, err
	}
	info, err := sub(base, "info", create, st.Dev)
	if err != nil {
		unix.Close(files)
		return nil, err
	}
	return &dir{topFD: topFD, files: files, info: info, dev: st.Dev, id: [2]uint64{bst.Dev, bst.Ino}}, nil
}

// sub opens (makes when create) the folder name of parent: ours, on device dev,
// not a link.
func sub(parent int, name string, create bool, dev uint64) (int, error) {
	if create {
		if err := unix.Mkdirat(parent, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, unavailable(err)
		}
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) && !create {
		return -1, fs.ErrNotExist
	}
	if err != nil {
		return -1, unavailable(err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, unavailable(err)
	}
	if st.Uid != uint32(os.Getuid()) || st.Dev != dev {
		unix.Close(fd)
		return -1, unavailable(fmt.Errorf("%s is not a trash folder of this disk", name))
	}
	return fd, nil
}

// openBelow opens the folder relative (to the folder topFD), one name at a
// time: no link is followed and no other disk entered. With create, the
// missing folders are made.
func openBelow(topFD int, relative string, dev uint64, create bool) (int, error) {
	fd, err := unix.Openat(topFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, name := range strings.Split(relative, "/") {
		if name == "" || name == "." {
			continue
		}
		if name == ".." {
			unix.Close(fd)
			return -1, ErrOutsideDisk
		}
		if create {
			if err := unix.Mkdirat(fd, name, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
				unix.Close(fd)
				return -1, err
			}
		}
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return -1, fmt.Errorf("%s: %w: %w", name, ErrOutsideDisk, errLinkInWay)
		}
		if err != nil {
			return -1, err
		}
		var st unix.Stat_t
		if err := unix.Fstat(next, &st); err != nil || st.Dev != dev {
			unix.Close(next)
			if err == nil {
				err = fmt.Errorf("%s: %w: %w", name, ErrOutsideDisk, errAnotherDisk)
			}
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

// rename moves without replacing: EEXIST when to is taken.
func rename(fromDir int, from string, toDir int, to string) error {
	err := unix.Renameat2(fromDir, from, toDir, to, unix.RENAME_NOREPLACE)
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	// a file system without the flag: a file is linked, which refuses a taken name, then unlinked
	var st unix.Stat_t
	if err := unix.Fstatat(fromDir, from, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		err := unix.Linkat(fromDir, from, toDir, to, 0)
		if err == nil {
			if err := unix.Unlinkat(fromDir, from, 0); err != nil {
				_ = unix.Unlinkat(toDir, to, 0) // not two names for one item
				return err
			}
			return nil
		}
		if errors.Is(err, unix.EEXIST) {
			return err
		}
	}
	// a folder, or no links on this file system (FAT): look, then move
	if unix.Fstatat(toDir, to, &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
		return unix.EEXIST
	}
	return unix.Renameat(fromDir, from, toDir, to)
}

// move is what Put needs, ready: the trash of the disk and the folder of path,
// both open.
type move struct {
	*dir
	parent int
	real   string // the real path of the item's folder
	base   string
}

func (m *move) close() {
	unix.Close(m.parent)
	m.dir.close()
}

func prepare(path string) (*move, error) {
	path = filepath.Clean(path)
	base := filepath.Base(path)
	if base == "/" || base == "." || base == ".." {
		return nil, unavailable(errors.New("not an entry"))
	}
	if err := file.CheckNoMountPoint(path); err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	top, err := mountOf(real)
	if err != nil {
		return nil, err
	}
	if inside(filepath.Join(real, base), filepath.Join(top, dirName())) {
		return nil, unavailable(errors.New("it is in the trash"))
	}
	d, err := openDir(top, true)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(top, real)
	if err != nil {
		d.close()
		return nil, unavailable(err)
	}
	// from the mount point, not by path: a folder swapped for a link since EvalSymlinks is refused
	parent, err := openBelow(d.topFD, relative, d.dev, false)
	if err != nil {
		d.close()
		if errors.Is(err, errAnotherDisk) {
			return nil, unavailable(err)
		}
		return nil, err // a link swapped in: not something to offer a permanent delete of
	}
	m := &move{dir: d, parent: parent, real: real, base: base}
	var st unix.Stat_t
	if err := unix.Fstatat(parent, base, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		m.close()
		return nil, &fs.PathError{Op: "stat", Path: path, Err: err}
	}
	return m, nil
}

// Put moves path into the trash of its disk, as deleted at now.
func Put(path string, now time.Time) error {
	m, err := prepare(path)
	if err != nil {
		return err
	}
	defer m.close()
	relative, err := filepath.Rel(m.top, filepath.Join(m.real, m.base))
	if err != nil {
		return unavailable(err)
	}
	for n := 1; n <= 10000; n++ {
		name := trashName(m.base, n)
		fd, err := unix.Openat(m.info, name+".trashinfo", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return unavailable(err)
		}
		_, err = unix.Write(fd, infoText(relative, now))
		unix.Close(fd)
		if err == nil {
			err = rename(m.parent, m.base, m.files, name)
		}
		if err == nil {
			return nil
		}
		_ = unix.Unlinkat(m.info, name+".trashinfo", 0)
		if errors.Is(err, unix.EEXIST) { // an entry left without its info: next name
			continue
		}
		if errors.Is(err, unix.EXDEV) || errors.Is(err, unix.EROFS) || errors.Is(err, unix.ENOSPC) {
			return unavailable(err)
		}
		return err
	}
	return unavailable(errors.New("no free name in the trash"))
}

// entry is an item of an open trash with its info.
type entry struct {
	Item
	readErr error // the info cannot be read, the entry cannot be looked at, or it says it came from another disk
	st      unix.Stat_t
	hasFile bool
	gone    bool      // the entry is surely not there (ENOENT), not just unreadable now
	written time.Time // when the info was written
}

// entries reads every info of d.
func (d *dir) entries() []entry {
	fd, err := unix.Openat(d.info, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	f := os.NewFile(uintptr(fd), "info")
	names, _ := f.Readdirnames(-1)
	f.Close()
	var out []entry
	for _, n := range names {
		name, ok := strings.CutSuffix(n, ".trashinfo")
		if !ok || !validName(name) {
			continue
		}
		e := entry{Item: Item{Trash: d.top, Name: name}}
		var ist unix.Stat_t
		if unix.Fstatat(d.info, n, &ist, unix.AT_SYMLINK_NOFOLLOW) == nil {
			e.written = time.Unix(ist.Mtim.Unix())
		}
		data, err := d.readInfo(name)
		if err == nil {
			var p string
			p, e.DeletedAt, err = parseInfo(data)
			e.Path = origin(d.top, p)
			if err == nil && (!inside(e.Path, d.top) || e.Path == d.top) {
				err = ErrOutsideDisk
			}
		}
		e.readErr = err
		switch err := unix.Fstatat(d.files, name, &e.st, unix.AT_SYMLINK_NOFOLLOW); {
		case err == nil:
			e.hasFile = true
		case errors.Is(err, unix.ENOENT):
			e.gone = true
		case e.readErr == nil:
			e.readErr = err
		}
		e.IsDir = e.st.Mode&unix.S_IFMT == unix.S_IFDIR
		out = append(out, e)
	}
	return out
}

func (d *dir) readInfo(name string) ([]byte, error) {
	fd, err := unix.Openat(d.info, name+".trashinfo", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 64<<10))
}

var (
	sizesMu sync.Mutex
	sizes   = map[[2]uint64]int64{} // folders in the trash by device and inode: they do not change there
)

// List is every item of every trash, the last deleted first.
func List() []Item {
	sizesMu.Lock()
	defer sizesMu.Unlock()
	kept := map[[2]uint64]int64{}
	var items []Item
	for _, d := range trashes() {
		for _, e := range d.entries() {
			if e.readErr != nil || !e.hasFile {
				continue
			}
			e.Size = e.st.Size
			if e.IsDir {
				key := [2]uint64{e.st.Dev, e.st.Ino}
				size, ok := sizes[key]
				if !ok {
					size = treeSize(filepath.Join(d.top, dirName(), "files", e.Name))
				}
				kept[key], e.Size = size, size
			}
			items = append(items, e.Item)
		}
		d.close()
	}
	sizes = kept
	sort.SliceStable(items, func(i, j int) bool { return items[i].DeletedAt.After(items[j].DeletedAt) })
	return items
}

// treeSize adds up the files under a folder; links count as themselves.
func treeSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, de fs.DirEntry, err error) error {
		if err == nil && !de.IsDir() {
			if info, err := de.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// open is the trash at the mount point top, which must be one that List shows.
func open(top string) (*dir, error) {
	var found *dir
	for _, d := range trashes() {
		if d.top == top && found == nil {
			found = d
			continue
		}
		d.close()
	}
	if found == nil {
		return nil, ErrNotFound
	}
	return found, nil
}

// find is the entry name of the trash at top, allowed.
func find(top, name string, allowed Allowed) (*dir, entry, error) {
	if !validName(name) {
		return nil, entry{}, ErrNotFound
	}
	d, err := open(top)
	if err != nil {
		return nil, entry{}, err
	}
	for _, e := range d.entries() {
		if e.Name != name {
			continue
		}
		if e.readErr != nil || !e.hasFile || (allowed != nil && !allowed(e.Path)) {
			break
		}
		return d, e, nil
	}
	d.close()
	return nil, entry{}, ErrNotFound
}

// Restore moves an item back where it was, and returns where it is now: under
// another name when that one is taken ("photo (restored).jpg"), its folder
// made again when it is gone. check vets the place first (the file API's
// roots), and may give it its canonical spelling. The way there is followed
// from the mount point without links: a folder replaced by a link is refused.
func Restore(top, name string, allowed Allowed, check func(path string) (string, error)) (string, error) {
	d, e, err := find(top, name, allowed)
	if err != nil {
		return "", err
	}
	defer d.close()
	dest := e.Path
	if check != nil {
		if dest, err = check(dest); err != nil {
			return "", err
		}
	}
	folder := filepath.Dir(dest)
	relative, err := filepath.Rel(top, folder)
	if err != nil || !inside(dest, top) || dest == top {
		return "", ErrOutsideDisk
	}
	parent, err := openBelow(d.topFD, relative, d.dev, true)
	if err != nil {
		return "", err
	}
	defer unix.Close(parent)
	base := filepath.Base(dest)
	for n := 0; n <= 1000; n++ {
		target := base
		if n > 0 {
			target = restoredName(base, e.IsDir, n)
		}
		if !d.same(name, e) {
			return "", ErrNotFound
		}
		err := rename(d.files, name, parent, target)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", err
		}
		_ = unix.Unlinkat(d.info, name+".trashinfo", 0)
		return filepath.Join(folder, target), nil
	}
	return "", errors.New("no free name to restore to")
}

// same reports whether the entry name is still the one e was read from (not
// restored and replaced by another item with the same name meanwhile).
func (d *dir) same(name string, e entry) bool {
	var st unix.Stat_t
	return unix.Fstatat(d.files, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Dev == e.st.Dev && st.Ino == e.st.Ino
}

// Delete removes an item for good.
func Delete(top, name string, allowed Allowed) error {
	d, e, err := find(top, name, allowed)
	if err != nil {
		return err
	}
	defer d.close()
	return d.remove(e)
}

// remove deletes the entry of e and then its info; ErrNotFound when another
// item has taken its name since it was read.
func (d *dir) remove(e entry) error {
	if !d.same(e.Name, e) {
		return ErrNotFound
	}
	if err := removeAt(d.files, e.Name, d.dev); err != nil {
		return err
	}
	if err := unix.Unlinkat(d.info, e.Name+".trashinfo", 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

// removeAt deletes name in the folder dirFD and what it holds, through
// descriptors: no link is followed, and no other disk entered
// (file.ErrHoldsMountPoint).
func removeAt(dirFD int, name string, dev uint64) error {
	err := unix.Unlinkat(dirFD, name, 0)
	if err == nil || errors.Is(err, unix.ENOENT) {
		return nil
	}
	if !errors.Is(err, unix.EISDIR) && !errors.Is(err, unix.EPERM) {
		return err
	}
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Dev != dev {
		return file.ErrHoldsMountPoint
	}
	names, err := f.Readdirnames(-1) // all first: removing while reading can skip names
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := removeAt(fd, n, dev); err != nil {
			return err
		}
	}
	return unix.Unlinkat(dirFD, name, unix.AT_REMOVEDIR)
}

// Empty removes every allowed item of every trash. With allowed nil, entries
// left without their info go too.
func Empty(allowed Allowed) (int, error) {
	removed := 0
	var first error
	for _, d := range trashes() {
		for _, e := range d.entries() {
			if e.readErr != nil || !e.hasFile || (allowed != nil && !allowed(e.Path)) {
				continue
			}
			if err := d.remove(e); err != nil {
				if first == nil && !errors.Is(err, ErrNotFound) {
					first = err
				}
				continue
			}
			removed++
		}
		if allowed == nil {
			fd, err := unix.Openat(d.files, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			if err == nil {
				f := os.NewFile(uintptr(fd), "files")
				names, _ := f.Readdirnames(-1)
				f.Close()
				for _, n := range names {
					if err := removeAt(d.files, n, d.dev); err != nil && first == nil {
						first = err
					}
				}
			}
		}
		d.close()
	}
	return removed, first
}

// infoGrace is how long an info may stay without its item, from when it was
// written: Put writes the info before it moves the item.
const infoGrace = 10 * time.Minute

// Purge removes the items deleted before cutoff from every trash, and the
// infos whose item is gone. An item without a readable date stays.
func Purge(cutoff time.Time) (int, error) {
	removed := 0
	var first error
	for _, d := range trashes() {
		for _, e := range d.entries() {
			switch {
			case e.gone:
				if time.Since(e.written) > infoGrace {
					_ = unix.Unlinkat(d.info, e.Name+".trashinfo", 0)
				}
			case e.readErr != nil: // unreadable now, or from another disk: kept
			case !e.DeletedAt.IsZero() && e.DeletedAt.Before(cutoff):
				if err := d.remove(e); err != nil {
					if first == nil && !errors.Is(err, ErrNotFound) {
						first = err
					}
					continue
				}
				removed++
			}
		}
		d.close()
	}
	return removed, first
}
