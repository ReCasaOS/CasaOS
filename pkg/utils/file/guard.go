package file

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/moby/sys/mountinfo"
)

// Overlap reports whether copying or moving from into the folder to would write over from or into it: the entry it makes, to/<name of from>, is from or
// holds it, or to is from or inside it. Paths are compared with their links resolved, and as files where they exist: a bind mount gives a folder a
// second name that no path comparison sees.
func Overlap(from, to string) bool {
	realFrom := filepath.Join(realPath(filepath.Dir(from)), filepath.Base(from))
	realTo := realPath(to)
	target := filepath.Join(realTo, filepath.Base(from))
	return isSameOrInside(realTo, realFrom) || isSameOrInside(realFrom, target) || holds(target, from) || holds(from, to)
}

// holds reports whether dir exists and is path or one of its parents, as a file.
func holds(dir, path string) bool {
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return false
	}
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if info, err := os.Stat(p); err == nil && os.SameFile(dirInfo, info) {
			return true
		}
		if p == filepath.Dir(p) {
			return false
		}
	}
}

// realPath is path with its links resolved, or only cleaned when it does not exist.
func realPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// isSameOrInside reports whether path is dir or under it, by whole names: /DATA/Photo2 is not inside /DATA/Photo. Paths it cannot compare count as inside.
func isSameOrInside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err != nil || (rel != ".." && !strings.HasPrefix(rel, "../"))
}

// ErrHoldsMountPoint is the error of RemoveAllNoCross when a disk or a share is mounted in the tree.
var ErrHoldsMountPoint = errors.New("a disk or a share is mounted there")

// mountPointsUnder lists the mount points at dir or under it, a var for the tests.
var mountPointsUnder = func(dir string) ([]string, error) {
	mounts, err := mountinfo.GetMounts(mountinfo.PrefixFilter(dir))
	if err != nil {
		return nil, err
	}
	points := []string{}
	for _, m := range mounts {
		points = append(points, m.Mountpoint)
	}
	return points, nil
}

// RemoveAllNoCross is os.RemoveAll that never goes into a mounted disk or share: when one is mounted at path or under it, it removes nothing.
func RemoveAllNoCross(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() { // a link is removed, not followed
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		points, err := mountPointsUnder(real)
		if err != nil {
			return err
		}
		if len(points) > 0 {
			return fmt.Errorf("%s: %w (%s)", path, ErrHoldsMountPoint, points[0])
		}
	}
	return os.RemoveAll(path)
}
