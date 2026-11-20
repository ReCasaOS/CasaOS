//go:build linux

package v1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/fileroots"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
)

// The operations of an account, called on the root of its folder with no check before:
// a link out of the folder anywhere on the way (one an app made, or one swapped in at the
// last moment) leads nowhere outside.
func hostileFolder(t *testing.T) (folder, outside string, root *os.Root) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	folder, outside = filepath.Join(base, "Kids"), filepath.Join(base, "outside")
	for _, d := range []string{folder + "/docs", outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(outside+"/passwd", []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(folder+"/docs/a.txt", []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	// mid-path links out, relative and absolute
	if err := os.Symlink("../outside", folder+"/rel"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, folder+"/abs"); err != nil {
		t.Fatal(err)
	}
	root, err = fileroots.OpenFolder(folder)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return folder, outside, root
}

func TestAnAccountsChangesNeverLeaveTheRoot(t *testing.T) {
	_, outside, root := hostileFolder(t)

	for _, link := range []string{"rel", "abs"} {
		if code := accountMkdir(root, link+"/made"); code == common_err.SUCCESS {
			t.Errorf("mkdir through %s: made", link)
		}
		if code := accountCreate(root, link+"/new"); code == common_err.SUCCESS {
			t.Errorf("create through %s: made", link)
		}
		if err := accountSave(root, link+"/passwd", []byte("owned")); err == nil {
			t.Errorf("save through %s: saved", link)
		}
		if code := accountRename(root, "docs/a.txt", link+"/a.txt"); code == common_err.SUCCESS {
			t.Errorf("rename into %s: renamed", link)
		}
		if err := root.RemoveAll(link + "/passwd"); err == nil {
			// RemoveAll of a path through a link out must fail, not delete outside
			if _, statErr := os.Stat(outside + "/passwd"); statErr != nil {
				t.Errorf("remove through %s: removed outside", link)
			}
		}
	}
	for _, p := range []string{outside + "/made", outside + "/new", outside + "/a.txt"} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s was made outside", p)
		}
	}
	if b, _ := os.ReadFile(outside + "/passwd"); string(b) != "root" {
		t.Errorf("outside changed: %q", b)
	}
	// deleting the link itself is fine, and takes nothing outside
	if err := root.RemoveAll("rel"); err != nil {
		t.Errorf("remove the link: %v", err)
	}
	if _, err := os.Stat(outside + "/passwd"); err != nil {
		t.Error("removing a link took what it pointed to")
	}

	// inside: as asked
	if code := accountMkdir(root, "docs/new/deeper"); code != common_err.SUCCESS {
		t.Errorf("mkdir inside: %d", code)
	}
	if err := accountSave(root, "docs/a.txt", []byte("b")); err != nil {
		t.Errorf("save inside: %v", err)
	}
}

// A folder whose own path goes through a link is not opened as a root.
func TestAFolderSpelledThroughALinkIsNotARoot(t *testing.T) {
	folder, _, _ := hostileFolder(t)
	if _, err := fileroots.OpenFolder(folder + "/rel"); err == nil {
		t.Error("a folder spelled through a link was opened")
	}
}
