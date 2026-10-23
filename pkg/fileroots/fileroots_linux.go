//go:build linux

package fileroots

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const resolve = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS

// Open pins the roots: each must exist, be a directory, and be spelled as its
// real path (no symlink anywhere in it). No roots: unconfined.
func Open(paths []string) (*Roots, error) {
	r := &Roots{paths: paths, fds: map[string]int{}}
	for _, root := range paths {
		fd, err := unix.Openat2(unix.AT_FDCWD, root, &unix.OpenHow{
			Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
		if err != nil {
			r.Close()
			if errors.Is(err, unix.ENOSYS) {
				return nil, ErrUnsupported
			}
			return nil, fmt.Errorf("%w: %s: %v", ErrInvalidRoot, root, err)
		}
		r.fds[root] = fd
	}
	return r, nil
}

// Close releases the pinned roots.
func (r *Roots) Close() {
	if r == nil {
		return
	}
	for root, fd := range r.fds {
		_ = unix.Close(fd)
		delete(r.fds, root)
	}
}

// Check returns the canonical spelling of a client path that resolves below
// its root, symlinks followed and kept beneath. A path that does not exist yet
// (an upload, a new folder, a rename's target) is checked through its nearest
// existing ancestor; a dangling link on the way is refused (writing through
// it could land anywhere). Relative links that stay below the root are
// followed; absolute links are refused, even to inside (RESOLVE_BENEATH).
// Unconfined: the path as given.
//
// It is a check before the operation: a component swapped for a symlink
// between the check and the operation is not caught here.
func (r *Roots) Check(path string) (string, error) {
	if !r.Confined() {
		return path, nil
	}
	clean, err := canonical(path)
	if err != nil {
		return "", ErrOutside
	}
	root, relative, err := match(r.paths, clean)
	if err != nil {
		return "", err
	}
	fd := r.fds[root]
	for relative != "." {
		err := probe(fd, relative, 0)
		if err == nil {
			return clean, nil
		}
		if !errors.Is(err, unix.ENOENT) {
			return "", ErrOutside
		}
		// missing: not even a link may sit there
		if probe(fd, relative, unix.O_NOFOLLOW) == nil {
			return "", ErrOutside
		}
		relative = filepath.Dir(relative)
	}
	return clean, nil
}

func probe(rootFD int, relative string, extra int) error {
	fd, err := unix.Openat2(rootFD, relative, &unix.OpenHow{
		Flags:   uint64(unix.O_PATH | unix.O_CLOEXEC | extra),
		Resolve: resolve,
	})
	if err != nil {
		return err
	}
	return unix.Close(fd)
}
