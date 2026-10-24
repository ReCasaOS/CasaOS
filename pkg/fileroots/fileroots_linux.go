//go:build linux

// SPDX-License-Identifier: Apache-2.0
//
// Derived from EdmundFu-233/ReCasaOS pkg/filesecurity (managed roots),
// Copyright 2026 EdmundFu-233, Apache License 2.0. Changed: reduced to the
// path check (no descriptor-relative operations), roots opened per check.

package fileroots

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const resolve = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS

// Open takes the roots (Parse) and makes sure the kernel has openat2. The roots
// are not opened here but on every check: one mounted later (a disk, a USB
// stick) is the one checked, and one missing now is refused until it is there
// (Unavailable). No roots: unconfined.
func Open(paths []string) (*Roots, error) {
	if len(paths) == 0 {
		return &Roots{}, nil
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, "/", &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_MAGICLINKS,
	})
	if errors.Is(err, unix.ENOSYS) {
		return nil, ErrUnsupported
	}
	if err == nil {
		_ = unix.Close(fd)
	}
	return &Roots{paths: paths}, nil
}

// Unavailable: the roots that cannot be opened now (for a warning at start).
func (r *Roots) Unavailable() []string {
	missing := []string{}
	for _, root := range r.Paths() {
		fd, err := openRoot(root)
		if err != nil {
			missing = append(missing, root)
			continue
		}
		_ = unix.Close(fd)
	}
	return missing
}

// openRoot: the root as a directory, spelled as its real path (no symlink
// anywhere in it).
func openRoot(root string) (int, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, root, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return -1, fmt.Errorf("%w: %s: %v", ErrRootUnavailable, root, err)
	}
	return fd, nil
}

// Check returns the canonical spelling of a client path that resolves below
// its root, symlinks followed and kept beneath. The root is opened for the
// check: not there (not mounted yet), or a symlink now → ErrRootUnavailable. A
// path that does not exist yet (an upload, a new folder, a rename's target) is
// checked through its nearest existing ancestor; a dangling link on the way is
// refused (writing through it could land anywhere). Relative links that stay
// below the root are followed; absolute links are refused, even to inside
// (RESOLVE_BENEATH). Unconfined: the path as given.
//
// It is a check before the operation: a component swapped for a symlink
// between the check and the operation is not caught here.
func (r *Roots) Check(path string) (string, error) {
	if !r.Confined() {
		return path, nil
	}
	if r.refused != nil {
		return "", r.refused
	}
	clean, err := canonical(path)
	if err != nil {
		return "", ErrOutside
	}
	root, relative, err := match(r.paths, clean)
	if err != nil {
		return "", err
	}
	fd, err := openRoot(root)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
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
