//go:build linux

// SPDX-License-Identifier: Apache-2.0
//
// Derived from EdmundFu-233/ReCasaOS pkg/filesecurity (managed roots),
// Copyright 2026 EdmundFu-233, Apache License 2.0. Changed: reduced to the
// path check (no descriptor-relative operations), roots opened per check.

package fileroots

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	roots, err := Parse(" /DATA, /mnt/disk ,/DATA/ ")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/mnt/disk", "/DATA"}; !reflect.DeepEqual(roots, want) {
		t.Fatalf("Parse = %v, want %v (most specific first, deduplicated)", roots, want)
	}
	if roots, err := Parse(""); err != nil || roots != nil {
		t.Fatalf("empty: %v %v, want unconfined", roots, err)
	}
	for _, bad := range []string{"/", "DATA", "/DATA/../etc", "/DATA//x", "/DATA,", "/./DATA"} {
		if _, err := Parse(bad); !errors.Is(err, ErrInvalidRoot) {
			t.Errorf("Parse(%q) = %v, want ErrInvalidRoot", bad, err)
		}
	}
}

// a box: root /…/data with a file, a folder, links inside, out and dangling;
// a secret beside it
func box(t *testing.T) (*Roots, string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, outside := filepath.Join(base, "data"), filepath.Join(base, "secret")
	for _, dir := range []string{data + "/Media", outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, target := range map[string]string{
		data + "/Media/in":       data + "/Media",
		data + "/Media/relative": "../Media",
		data + "/out":            outside,
		data + "/up":             "../secret",
		data + "/dangling":       outside + "/new",
	} {
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(data+"/Media/a.txt", []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots, err := Open([]string{data})
	if err != nil {
		t.Fatal(err)
	}
	return roots, data, outside
}

func TestCheck(t *testing.T) {
	roots, data, outside := box(t)
	for _, ok := range []string{
		data, data + "/", data + "/Media", data + "/Media/a.txt",
		data + "/Media/relative/a.txt",  // a relative one too
		data + "/Media/new.txt",         // an upload
		data + "/Media/new/deeper/file", // a new folder tree
	} {
		if _, err := roots.Check(ok); err != nil {
			t.Errorf("Check(%q) = %v, want allowed", ok, err)
		}
	}
	for _, bad := range []string{
		"/etc/shadow", "/", outside, outside + "/x",
		data + "/../secret",            // spelled with ..
		data + "//Media",               // spelled twice
		data + "/out", data + "/out/x", // a link out
		data + "/up/x",           // a relative link out
		data + "/Media/in/a.txt", // an absolute link, even to inside (openat2 RESOLVE_BENEATH)
		data + "/dangling",       // writing through it would create outside
		data + "/dangling/x",
		"relative/path", "",
	} {
		if _, err := roots.Check(bad); !errors.Is(err, ErrOutside) {
			t.Errorf("Check(%q) = %v, want ErrOutside", bad, err)
		}
	}
	if got, _ := roots.Check(data + "/Media/"); got != data+"/Media" {
		t.Errorf("canonical spelling = %q", got)
	}
}

func TestUnconfinedIsUnchanged(t *testing.T) {
	var roots *Roots
	if got, err := roots.Check("/etc/shadow"); err != nil || got != "/etc/shadow" {
		t.Fatalf("unconfined Check = %q %v", got, err)
	}
	empty, err := Open(nil)
	if err != nil || empty.Confined() {
		t.Fatalf("Open(nil) = %v %v", empty, err)
	}
}

// a root is opened on every check: one missing at start (disk not mounted yet)
// is refused until it is there, one replaced by a link is refused, one mounted
// over (replaced) is the one checked
func TestARootIsOpenedOnEveryCheck(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	_ = os.Mkdir(base+"/real", 0o755)
	_ = os.Symlink(base+"/real", base+"/link")
	roots, err := Open([]string{base + "/link", base + "/disk"})
	if err != nil {
		t.Fatalf("Open: %v, want the roots taken (checked later)", err)
	}
	problems := roots.Unavailable()
	if len(problems) != 2 || !errors.Is(problems[base+"/link"], ErrRootNotReal) ||
		!errors.Is(problems[base+"/disk"], ErrRootUnavailable) || errors.Is(problems[base+"/disk"], ErrRootNotReal) {
		t.Errorf("Unavailable = %v: want the link as not real, the disk as not there", problems)
	}
	for _, path := range []string{base + "/link/x", base + "/disk/x", base + "/disk"} {
		if _, err := roots.Check(path); !errors.Is(err, ErrRootUnavailable) {
			t.Errorf("Check(%q) = %v, want ErrRootUnavailable", path, err)
		}
	}

	// the disk comes: allowed from now on
	_ = os.Mkdir(base+"/disk", 0o755)
	if _, err := roots.Check(base + "/disk/x"); err != nil {
		t.Errorf("Check after the root came: %v", err)
	}

	// the folder at the root's path is replaced (a mount over it): its content counts
	_ = os.Symlink("/etc", base+"/disk/x")
	if _, err := roots.Check(base + "/disk/x"); !errors.Is(err, ErrOutside) {
		t.Fatalf("Check of a link out: %v", err)
	}
	_ = os.Rename(base+"/disk", base+"/old")
	_ = os.Mkdir(base+"/disk", 0o755)
	_ = os.WriteFile(base+"/disk/x", nil, 0o644)
	if _, err := roots.Check(base + "/disk/x"); err != nil {
		t.Errorf("Check after the root was replaced: %v (a stale root?)", err)
	}

	// replaced by a link: refused
	_ = os.Remove(base + "/disk/x")
	_ = os.Remove(base + "/disk")
	_ = os.Symlink(base+"/real", base+"/disk")
	if _, err := roots.Check(base + "/disk/x"); !errors.Is(err, ErrRootUnavailable) {
		t.Errorf("Check through a root that is a link now: %v, want ErrRootUnavailable", err)
	}
}

// a configuration that cannot be used refuses every path, and nothing else
func TestRefusingRefusesEverything(t *testing.T) {
	roots := Refusing(ErrInvalidRoot)
	if !roots.Confined() {
		t.Fatal("Refusing is not confined")
	}
	if _, err := roots.Check("/DATA/x"); !errors.Is(err, ErrInvalidRoot) {
		t.Errorf("Check = %v", err)
	}
	if _, err := roots.CheckEntry("/DATA/x"); !errors.Is(err, ErrInvalidRoot) {
		t.Errorf("CheckEntry = %v", err)
	}
	if roots.IsAncestor("/") {
		t.Error("IsAncestor(/): nothing to lead to")
	}
}

// delete and rename work on the entry: a link out is removed as a link; a
// root, or a folder above one, is not removed at all
func TestCheckEntry(t *testing.T) {
	roots, data, outside := box(t)
	for _, ok := range []string{data + "/out", data + "/dangling", data + "/Media/a.txt", data + "/Media/in", data + "/Media/new"} {
		if got, err := roots.CheckEntry(ok); err != nil || got != ok {
			t.Errorf("CheckEntry(%q) = %q %v, want allowed", ok, got, err)
		}
	}
	for path, want := range map[string]error{
		data:                ErrRoot,
		data + "/":          ErrRoot,
		filepath.Dir(data):  ErrRoot,
		"/":                 ErrRoot,
		outside + "/x":      ErrOutside,
		data + "/out/x":     ErrOutside, // in a folder that leads out
		data + "/../secret": ErrOutside,
	} {
		if _, err := roots.CheckEntry(path); !errors.Is(err, want) {
			t.Errorf("CheckEntry(%q) = %v, want %v", path, err, want)
		}
	}
	nested := &Roots{paths: []string{data + "/Media", data}}
	if _, err := nested.CheckEntry(data + "/Media"); !errors.Is(err, ErrRoot) {
		t.Errorf("a nested root: %v, want ErrRoot", err)
	}
}

func TestTheWayToTheRoots(t *testing.T) {
	roots := &Roots{paths: []string{"/var/lib/casaos/files", "/DATA/sub", "/DATA"}}
	for path, want := range map[string]bool{
		"/": true, "/var": true, "/var/lib": true, "/etc": false, "/DATA/x": false,
		"/DATA": false, // a root with one nested in it: managed, listed as it is
	} {
		if got := roots.IsAncestor(path); got != want {
			t.Errorf("IsAncestor(%q) = %v, want %v", path, got, want)
		}
	}
	for entry, want := range map[[2]string]bool{
		{"/", "DATA"}: true, {"/", "var"}: true, {"/", "etc"}: false,
		{"/var", "lib"}: true, {"/var", "log"}: false, {"/var/lib/casaos", "files"}: true,
	} {
		if got := roots.Leads(entry[0], entry[1]); got != want {
			t.Errorf("Leads(%q, %q) = %v, want %v", entry[0], entry[1], got, want)
		}
	}
}
