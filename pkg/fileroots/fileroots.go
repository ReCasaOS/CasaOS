// SPDX-License-Identifier: Apache-2.0
//
// Derived from EdmundFu-233/ReCasaOS pkg/filesecurity (managed roots),
// Copyright 2026 EdmundFu-233, Apache License 2.0. Changed: reduced to the
// path check (no descriptor-relative operations), roots opened per check.

// Package fileroots confines the file API to configured directories
// ([file] ManagementRoots). The core runs as root: without a boundary, a
// signed-in session reads, writes and deletes anything on the machine.
//
// Ported from EdmundFu-233/ReCasaOS pkg/filesecurity (managed roots, Apache-2.0):
// strict path spelling; on every check the root is opened (no symlink in its
// path) and the client path resolved with openat2(RESOLVE_BENEATH|
// RESOLVE_NO_MAGICLINKS) below it, so neither `..` nor a symlink leads out. It
// is a check before the operation: the operation itself still works on the
// path (see Check).
package fileroots

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

var (
	// ErrOutside: the path is not below a root, or resolves out of it.
	ErrOutside = errors.New("the path is outside the managed file roots")
	// ErrInvalidRoot: a root in the configuration is not usable.
	ErrInvalidRoot = errors.New("invalid managed file root")
	// ErrUnsupported: confinement needs Linux openat2.
	ErrUnsupported = errors.New("managed file roots need Linux openat2")
	// ErrRootUnavailable: the root cannot be opened now (not mounted yet, gone,
	// replaced by a symlink).
	ErrRootUnavailable = errors.New("the managed file root is not available")
	// ErrRoot: a root itself, or a folder above one, is not deleted or renamed.
	ErrRoot = errors.New("a managed file root is not deleted or renamed")
)

// Parse reads the comma-separated setting. Empty: no roots, the file API is
// not confined (unchanged behaviour). The filesystem root is refused: it would
// make every check a no-op.
func Parse(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	roots := []string{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		root, err := canonical(item)
		if err != nil || root == "/" {
			return nil, fmt.Errorf("%w: %q", ErrInvalidRoot, item)
		}
		if !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	// the most specific root first: nested roots match deterministically
	sort.Slice(roots, func(i, j int) bool {
		if len(roots[i]) == len(roots[j]) {
			return roots[i] < roots[j]
		}
		return len(roots[i]) > len(roots[j])
	})
	return roots, nil
}

// canonical accepts an absolute path spelled once: a trailing slash is
// dropped, but `//`, `.` and `..` components are refused rather than cleaned
// (a cleaned attacker path is still the attacker's path).
func canonical(value string) (string, error) {
	if value == "" || strings.IndexByte(value, 0) >= 0 || !filepath.IsAbs(value) {
		return "", ErrOutside
	}
	trimmed := strings.TrimRight(value, "/")
	if trimmed == "" {
		return "/", nil
	}
	if filepath.Clean(trimmed) != trimmed {
		return "", ErrOutside
	}
	return trimmed, nil
}

// match maps an absolute path to its root and the path below it ("." for the
// root itself), lexically.
func match(roots []string, path string) (root, relative string, err error) {
	for _, root := range roots {
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
			continue
		}
		return root, relative, nil
	}
	return "", "", ErrOutside
}

// Roots is the configuration. The zero value and nil are unconfined.
type Roots struct {
	paths []string
	// refused: the configuration is not usable; every check fails with it
	// (fail closed: never silently unconfined, and no exit either)
	refused error
}

// Refusing: Roots that refuse every path with err (a configuration that cannot
// be used).
func Refusing(err error) *Roots { return &Roots{refused: err} }

// Confined: there are roots to enforce, or every path is refused.
func (r *Roots) Confined() bool { return r != nil && (len(r.paths) > 0 || r.refused != nil) }

// Paths: the configured roots, most specific first.
func (r *Roots) Paths() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.paths...)
}

// IsAncestor: path is a directory above a root ("/" above "/DATA") and not
// itself below one (with /DATA and /DATA/sub, /DATA is managed). Listing it
// shows only the way down to the roots (Leads).
func (r *Roots) IsAncestor(path string) bool {
	path, err := canonical(path)
	if err != nil || !r.Confined() {
		return false
	}
	if _, _, err := match(r.paths, path); err == nil {
		return false
	}
	return r.holdsRoot(path)
}

// holdsRoot: path is a root or a folder above one (canonical path).
func (r *Roots) holdsRoot(path string) bool {
	for _, root := range r.paths {
		if path == root || path == "/" || strings.HasPrefix(root, path+"/") {
			return true
		}
	}
	return false
}

// CheckEntry is Check for what deletes or renames the entry itself, not what
// it leads to: its folder must be below a root, the entry need not resolve
// there (a link that leads out is removed or renamed as a link). A root, or a
// folder above one, is refused (ErrRoot). Unconfined: the path as given.
func (r *Roots) CheckEntry(path string) (string, error) {
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
	if r.holdsRoot(clean) {
		return "", ErrRoot
	}
	dir, err := r.Check(filepath.Dir(clean))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(clean)), nil
}

// CheckEntries: CheckEntry for each *path, replaced with its canonical
// spelling; the first error.
func (r *Roots) CheckEntries(paths ...*string) error {
	for _, p := range paths {
		c, err := r.CheckEntry(*p)
		if err != nil {
			return err
		}
		*p = c
	}
	return nil
}

// Leads: the entry name of directory dir is a root or on the way to one.
func (r *Roots) Leads(dir, name string) bool {
	child := filepath.Join(dir, name)
	for _, root := range r.Paths() {
		if child == root || strings.HasPrefix(root, child+"/") {
			return true
		}
	}
	return false
}

// CheckAll: Check for every path; the canonical paths, or the first error.
func (r *Roots) CheckAll(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		c, err := r.Check(p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Default: the roots of this process ([file] ManagementRoots), set at start.
// nil: unconfined.
var Default *Roots

// CheckEach replaces each *path with its canonical spelling, or returns the
// first error.
func (r *Roots) CheckEach(paths ...*string) error {
	for _, p := range paths {
		c, err := r.Check(*p)
		if err != nil {
			return err
		}
		*p = c
	}
	return nil
}
