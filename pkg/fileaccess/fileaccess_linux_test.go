//go:build linux

package fileaccess

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/pkg/fileroots"
)

// a box: /data/Kids (read and write), /data/Media (read), /data/Private, and a link out
func box(t *testing.T, role string, ok bool, given []external.Folder) (data string, s *Scope, err error) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data = filepath.Join(base, "data")
	for _, d := range []string{"Kids", "Media", "Private"} {
		if err := os.MkdirAll(filepath.Join(data, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(data, d, "f"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../Private", filepath.Join(data, "Kids", "out")); err != nil {
		t.Fatal(err)
	}
	for i := range given {
		given[i].Path = filepath.Join(data, given[i].Path)
	}
	wasAccount, wasFolders, wasDefault := AccountOf, FoldersOf, fileroots.Default
	t.Cleanup(func() { AccountOf, FoldersOf, fileroots.Default = wasAccount, wasFolders, wasDefault })
	AccountOf = func(string, string) (string, bool, bool) { return role, false, ok }
	FoldersOf = func(string, string) []external.Folder { return given }
	fileroots.Default = nil

	s, err = For("", "4", false)
	return data, s, err
}

func ptr(s string) *string { return &s }

func TestAnAccountReachesItsFoldersOnly(t *testing.T) {
	data, s, err := box(t, "user", true, []external.Folder{{Path: "Kids", Write: true}, {Path: "Media"}})
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"Kids/f", "Media/f", "Kids"} {
		if err := s.Check(Read, ptr(filepath.Join(data, p))); err != nil {
			t.Errorf("read %s: %v", p, err)
		}
	}
	if err := s.Check(Write, ptr(filepath.Join(data, "Kids/new"))); err != nil {
		t.Errorf("write in Kids: %v", err)
	}
	for _, p := range []string{"Private/f", "Kids/out/f", "Kids/../Private/f", ""} {
		if err := s.Check(Read, ptr(filepath.Join(data, p))); err == nil {
			t.Errorf("read %q: allowed", p)
		}
	}
	if err := s.Check(Write, ptr(filepath.Join(data, "Media/f"))); !errors.Is(err, ErrReadOnly) {
		t.Errorf("write in Media: %v, want ErrReadOnly", err)
	}
	// the folder itself is not deleted nor renamed; what is in a writable one is
	if err := s.CheckEntries(ptr(filepath.Join(data, "Kids"))); err == nil {
		t.Error("the folder itself: allowed")
	}
	if err := s.CheckEntries(ptr(filepath.Join(data, "Kids/f"))); err != nil {
		t.Errorf("entry in Kids: %v", err)
	}
	if err := s.CheckEntries(ptr(filepath.Join(data, "Media/f"))); !errors.Is(err, ErrReadOnly) {
		t.Errorf("entry in Media: %v, want ErrReadOnly", err)
	}
	// above its folders, only the way down
	if !s.Listing().IsAncestor(data) || !s.Listing().Leads(data, "Kids") || s.Listing().Leads(data, "Private") {
		t.Error("the way down to the folders")
	}
}

// An empty list of roots is unconfined: an account with no folder must reach nothing.
func TestNoFolderReachesNothing(t *testing.T) {
	data, s, err := box(t, "user", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"Kids/f", "Private/f", "/etc/passwd"} {
		path := p
		if p[0] != '/' {
			path = filepath.Join(data, p)
		}
		if err := s.Check(Read, &path); !errors.Is(err, ErrNoFolder) {
			t.Errorf("read %s: %v, want ErrNoFolder", p, err)
		}
	}
	if s.Listing().IsAncestor("/") {
		t.Error("a way down with no folder")
	}
}

func TestAReadOnlyAccountWritesNothing(t *testing.T) {
	data, s, _ := box(t, "user", true, []external.Folder{{Path: "Media"}})
	if err := s.Check(Write, ptr(filepath.Join(data, "Media/new"))); !errors.Is(err, ErrReadOnly) {
		t.Errorf("write: %v", err)
	}
	if err := s.Check(Write, ptr(filepath.Join(data, "Private/new"))); err == nil {
		t.Error("write outside: allowed")
	}
}

// A role user-service has not given yet is no owner's.
func TestAnUnknownRoleIsRefusedAndTheOwnerReachesTheBox(t *testing.T) {
	if _, _, err := box(t, "", false, nil); !errors.Is(err, ErrRoleUnknown) {
		t.Errorf("unknown role: %v", err)
	}
	_, s, err := box(t, "admin", true, nil)
	if err != nil || s.User() {
		t.Fatalf("admin: %v %v", s, err)
	}
	if err := s.Check(Write, ptr("/etc/passwd")); err != nil { // unconfined box: as before
		t.Errorf("owner: %v", err)
	}
	if s, err := For("", "", false); err != nil || s.User() {
		t.Errorf("no account (an internal call): %v %v", s, err)
	}
}
