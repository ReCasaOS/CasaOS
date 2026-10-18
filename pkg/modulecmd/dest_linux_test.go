package modulecmd

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// The file system the archive is written to, on a real directory: what the contract of osmodule.Destination asks of it.

func newDest(t *testing.T) (linuxDest, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "usr/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	return linuxDest{prefix: root}, root
}

// tempLeftovers are the temporary names of the directory, of which there should be none
func tempLeftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".cos-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func writeThrough(t *testing.T, d linuxDest, p string, perm fs.FileMode, replace bool, body string) error {
	t.Helper()
	f, err := d.NewFile(p, perm, replace)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(body)); err != nil {
		f.Abort()
		t.Fatal(err)
	}
	if err := f.Commit(); err != nil {
		f.Abort()
		return err
	}
	return nil
}

func TestTheDestinationMakesOneDirectoryWithTheExactModeAndNeverItsParents(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	d, root := newDest(t)
	if err := d.Mkdir("/usr/lib", 0o755); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(root, "usr/lib")); err != nil || info.Mode() != fs.ModeDir|0o755 {
		t.Errorf("the mode under a umask of 077: %v %v", info, err)
	}
	if err := d.Mkdir("/usr/lib", 0o755); !errors.Is(err, fs.ErrExist) {
		t.Errorf("a directory that is there: %v", err)
	}
	if err := d.Mkdir("/usr/missing/below", 0o755); err == nil || exists(filepath.Join(root, "usr/missing")) {
		t.Errorf("a parent was made: %v", err)
	}
	// a file where the directory goes is not a directory made
	if err := os.WriteFile(filepath.Join(root, "usr/afile"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.Mkdir("/usr/afile", 0o755); !errors.Is(err, fs.ErrExist) {
		t.Errorf("a file: %v", err)
	}
}

func TestTheDestinationPutsAFileInPlaceWithTheExactModeAndNeverReplacesWhatIsThereUnlessAsked(t *testing.T) {
	for name, fallback := range map[string]bool{"renameat2": false, "link": true} {
		t.Run(name, func(t *testing.T) {
			if fallback {
				old := renameNoReplace
				renameNoReplace = func(string, string) error { return unix.EINVAL }
				defer func() { renameNoReplace = old }()
			}
			old := syscall.Umask(0o077)
			defer syscall.Umask(old)
			d, root := newDest(t)
			bin := filepath.Join(root, "usr/bin")
			if err := writeThrough(t, d, "/usr/bin/prog", 0o755, false, "new"); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Lstat(filepath.Join(bin, "prog")); err != nil || info.Mode() != 0o755 || info.Size() != 3 {
				t.Errorf("the file: %v %v", info, err)
			}
			if l := tempLeftovers(t, bin); len(l) != 0 {
				t.Errorf("left: %v", l)
			}
			// a file that is there is not replaced, and what is there is as it was
			if err := writeThrough(t, d, "/usr/bin/prog", 0o644, false, "other"); !errors.Is(err, fs.ErrExist) {
				t.Errorf("over a file: %v", err)
			}
			if raw, _ := os.ReadFile(filepath.Join(bin, "prog")); string(raw) != "new" {
				t.Errorf("replaced: %q", raw)
			}
			// ...nor a link, nor a link that points nowhere, nor a directory
			if err := os.Symlink("/nowhere", filepath.Join(bin, "dangling")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(bin, "dir"), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{"dangling", "dir"} {
				if err := writeThrough(t, d, "/usr/bin/"+p, 0o644, false, "x"); !errors.Is(err, fs.ErrExist) {
					t.Errorf("over %s: %v", p, err)
				}
			}
			if target, err := os.Readlink(filepath.Join(bin, "dangling")); err != nil || target != "/nowhere" {
				t.Errorf("the link was touched: %q %v", target, err)
			}
			if l := tempLeftovers(t, bin); len(l) != 0 {
				t.Errorf("left after the refusals: %v", l)
			}
			// when it is asked, it replaces the file, and a link too (the link, not what it points to)
			if err := writeThrough(t, d, "/usr/bin/prog", 0o700, true, "replaced"); err != nil {
				t.Fatal(err)
			}
			if raw, _ := os.ReadFile(filepath.Join(bin, "prog")); string(raw) != "replaced" {
				t.Errorf("not replaced: %q", raw)
			}
			if info, _ := os.Lstat(filepath.Join(bin, "prog")); info.Mode() != 0o700 {
				t.Errorf("the mode of the replacement: %v", info.Mode())
			}
			victim := filepath.Join(root, "victim")
			if err := os.WriteFile(victim, []byte("not the module's"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, filepath.Join(bin, "link")); err != nil {
				t.Fatal(err)
			}
			if err := writeThrough(t, d, "/usr/bin/link", 0o644, true, "file"); err != nil {
				t.Fatal(err)
			}
			if raw, _ := os.ReadFile(victim); string(raw) != "not the module's" {
				t.Errorf("written through a link: %q", raw)
			}
			if info, _ := os.Lstat(filepath.Join(bin, "link")); !info.Mode().IsRegular() {
				t.Errorf("the link is still a link: %v", info.Mode())
			}
			// a file that is dropped is not there, and leaves nothing
			f, err := d.NewFile("/usr/bin/dropped", 0o644, false)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.Write([]byte("x"))
			f.Abort()
			if exists(filepath.Join(bin, "dropped")) || len(tempLeftovers(t, bin)) != 0 {
				t.Errorf("a dropped file: %v", tempLeftovers(t, bin))
			}
		})
	}
}

func TestTheDestinationMakesALinkAndNeverReplacesWhatIsThereUnlessAsked(t *testing.T) {
	for name, fallback := range map[string]bool{"renameat2": false, "link": true} {
		t.Run(name, func(t *testing.T) {
			if fallback {
				old := renameNoReplace
				renameNoReplace = func(string, string) error { return unix.EINVAL }
				defer func() { renameNoReplace = old }()
			}
			d, root := newDest(t)
			bin := filepath.Join(root, "usr/bin")
			if err := d.Symlink("target", "/usr/bin/l", false); err != nil {
				t.Fatal(err)
			}
			if target, err := os.Readlink(filepath.Join(bin, "l")); err != nil || target != "target" {
				t.Errorf("the link: %q %v", target, err)
			}
			if err := d.Symlink("other", "/usr/bin/l", false); !errors.Is(err, fs.ErrExist) {
				t.Errorf("over a link: %v", err)
			}
			if target, _ := os.Readlink(filepath.Join(bin, "l")); target != "target" {
				t.Errorf("the link was replaced: %q", target)
			}
			if err := os.WriteFile(filepath.Join(bin, "f"), []byte("a file"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := d.Symlink("other", "/usr/bin/f", false); !errors.Is(err, fs.ErrExist) {
				t.Errorf("over a file: %v", err)
			}
			if raw, _ := os.ReadFile(filepath.Join(bin, "f")); string(raw) != "a file" {
				t.Errorf("the file was replaced: %q", raw)
			}
			if l := tempLeftovers(t, bin); len(l) != 0 {
				t.Errorf("left: %v", l)
			}
			if err := d.Symlink("other", "/usr/bin/l", true); err != nil {
				t.Fatal(err)
			}
			if target, _ := os.Readlink(filepath.Join(bin, "l")); target != "other" {
				t.Errorf("not replaced: %q", target)
			}
			if err := d.Symlink("other", "/usr/bin/f", true); err != nil {
				t.Fatal(err)
			}
			if info, _ := os.Lstat(filepath.Join(bin, "f")); info.Mode()&fs.ModeSymlink == 0 {
				t.Errorf("a file is not replaced by a link: %v", info.Mode())
			}
			if l := tempLeftovers(t, bin); len(l) != 0 {
				t.Errorf("left: %v", l)
			}
		})
	}
}

// the temporary name is short and in the directory of the file: a file whose name is as long as a name can be is written all the same
func TestTheTemporaryNameIsShortAndInTheDirectoryOfTheFile(t *testing.T) {
	d, root := newDest(t)
	long := strings.Repeat("a", 255)
	if err := writeThrough(t, d, "/usr/bin/"+long, 0o755, false, "x"); err != nil {
		t.Fatalf("a name of 255 bytes: %v", err)
	}
	if err := d.Symlink("x", "/usr/bin/"+strings.Repeat("b", 255), false); err != nil {
		t.Fatalf("a link of 255 bytes: %v", err)
	}
	if got := tempName(filepath.Join(root, "usr/bin", long)); filepath.Dir(got) != filepath.Join(root, "usr/bin") || len(filepath.Base(got)) > 20 || got == tempName(filepath.Join(root, "usr/bin", "b")) {
		t.Errorf("the temporary name: %s", got)
	}
	// the same file has the same temporary name, whatever the run: a temporary file that a kill left is found again
	if tempName("/usr/bin/x") != tempName("/usr/bin/x") || tempName("/usr/bin/x") == tempName("/usr/bin/y") {
		t.Error("the temporary name is not a function of the name")
	}
	if l := tempLeftovers(t, filepath.Join(root, "usr/bin")); len(l) != 0 {
		t.Errorf("left: %v", l)
	}
}

// a temporary name that is a link, or a file that a kill left, is never written through
func TestTheTemporaryNameThatIsALinkIsNotWrittenThrough(t *testing.T) {
	d, root := newDest(t)
	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("not the module's"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, tempName(filepath.Join(root, "usr/bin/prog"))); err != nil {
		t.Fatal(err)
	}
	if err := writeThrough(t, d, "/usr/bin/prog", 0o755, false, "new"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(victim); string(raw) != "not the module's" {
		t.Errorf("written through a link: %q", raw)
	}
	// and one that is a half-written file of a run that was killed
	if err := os.WriteFile(tempName(filepath.Join(root, "usr/bin/two")), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeThrough(t, d, "/usr/bin/two", 0o755, false, "whole"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, "usr/bin/two")); string(raw) != "whole" {
		t.Errorf("%q", raw)
	}
}
