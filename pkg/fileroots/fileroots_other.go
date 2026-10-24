//go:build !linux

// SPDX-License-Identifier: Apache-2.0
//
// Derived from EdmundFu-233/ReCasaOS pkg/filesecurity (managed roots),
// Copyright 2026 EdmundFu-233, Apache License 2.0. Changed: reduced to the
// path check (no descriptor-relative operations), roots opened per check.

package fileroots

// Open: confinement needs openat2; no roots stays unconfined.
func Open(paths []string) (*Roots, error) {
	if len(paths) > 0 {
		return nil, ErrUnsupported
	}
	return &Roots{}, nil
}

// Unavailable: every root (none can be checked here).
func (r *Roots) Unavailable() []string { return r.Paths() }

func (r *Roots) Check(path string) (string, error) {
	if !r.Confined() {
		return path, nil
	}
	if r.refused != nil {
		return "", r.refused
	}
	return "", ErrUnsupported
}
