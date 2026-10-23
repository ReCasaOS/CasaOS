//go:build linux

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
	t.Cleanup(roots.Close)
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

func TestOpenRefusesARootThatIsALinkOrMissing(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	_ = os.Mkdir(base+"/real", 0o755)
	_ = os.Symlink(base+"/real", base+"/link")
	for _, root := range []string{base + "/link", base + "/missing"} {
		if _, err := Open([]string{root}); !errors.Is(err, ErrInvalidRoot) {
			t.Errorf("Open(%q) = %v, want ErrInvalidRoot", root, err)
		}
	}
}

func TestTheWayToTheRoots(t *testing.T) {
	roots := &Roots{paths: []string{"/var/lib/casaos/files", "/DATA"}}
	for path, want := range map[string]bool{"/": true, "/var": true, "/var/lib": true, "/DATA": false, "/etc": false, "/DATA/x": false} {
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
