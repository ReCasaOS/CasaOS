//go:build linux

package osmodule

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// The unpack on a real tree of a real file system: a directory made like a system with a merged /usr (lib is a link to usr/lib), and a Destination over the os package that does what the
// contract of Destination asks. The cases are the ones of the rules above, which the in-memory file system can only imitate.

// osDest is a Destination over a directory that stands for the root of the system.
type osDest struct{ root string }

var tempNumber atomic.Int64

func (d osDest) at(p string) string { return d.root + p }

func (d osDest) Lstat(p string) (fs.FileMode, error) {
	fi, err := os.Lstat(d.at(p))
	if err != nil {
		return 0, err
	}
	return fi.Mode(), nil
}

func (d osDest) Mkdir(p string, perm fs.FileMode) error {
	if err := os.Mkdir(d.at(p), perm); err != nil {
		return err
	}
	return os.Chmod(d.at(p), perm) // (exact: the umask has taken its share by now)
}

func (d osDest) tempFor(final string) string {
	return path.Join(path.Dir(final), fmt.Sprintf(".t%d", tempNumber.Add(1)))
}

type osFile struct {
	f             *os.File
	tmp, final    string
	perm          fs.FileMode
	replace, done bool
}

func (d osDest) NewFile(p string, perm fs.FileMode, replace bool) (File, error) {
	final := d.at(p)
	tmp := d.tempFor(final)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return &osFile{f: f, tmp: tmp, final: final, perm: perm, replace: replace}, nil
}

func (w *osFile) Write(b []byte) (int, error) { return w.f.Write(b) }

func (w *osFile) Commit() error {
	if err := w.f.Chmod(w.perm); err != nil {
		return err
	}
	if err := w.f.Close(); err != nil {
		return err
	}
	if w.replace {
		return os.Rename(w.tmp, w.final)
	}
	return unix.Renameat2(unix.AT_FDCWD, w.tmp, unix.AT_FDCWD, w.final, unix.RENAME_NOREPLACE)
}

func (w *osFile) Abort() {
	w.f.Close()
	os.Remove(w.tmp)
}

func (d osDest) Symlink(target, p string, replace bool) error {
	final := d.at(p)
	if !replace {
		return os.Symlink(target, final)
	}
	tmp := d.tempFor(final)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// newTree is a system: the directories a fresh Debian has, with /lib a link to usr/lib.
func newTree(t *testing.T) osDest {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"usr/bin", "usr/lib/systemd/system", "etc/casaos", "usr/share/casaos", "var/lib/casaos"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("usr/lib", filepath.Join(root, "lib")); err != nil {
		t.Fatal(err)
	}
	return osDest{root}
}

// snapshot is everything in the tree, by path: its type, its permissions, and its content or its target.
func snapshot(t *testing.T, d osDest) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(d.root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(filepath.ToSlash(p), filepath.ToSlash(d.root))
		desc := fmt.Sprintf("%v", fi.Mode())
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			desc += " -> " + target
		case fi.Mode().IsRegular():
			body, _ := os.ReadFile(p)
			desc += " " + string(body)
		}
		out[rel] = desc
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s: %s was %q, is %q", what, p, v, after[p])
		}
	}
	for p, v := range after {
		if _, ok := before[p]; !ok {
			t.Errorf("%s: %s is new: %q", what, p, v)
		}
	}
}

func unpackOn(t *testing.T, d osDest, own []string, ents ...tent) (Result, error) {
	t.Helper()
	return Unpack(bytes.NewReader(tarOf(t, ents...)), d, own, nil)
}

func TestOnARealTreeAnUnpackWritesTheExactModesAndTheRemoveLeavesNothing(t *testing.T) {
	old := syscall.Umask(0o077) // (the modes are the archive's whatever the umask says)
	defer syscall.Umask(old)
	d := newTree(t)
	before := snapshot(t, d)
	ents := append(goodEntries(), tent{name: sysroot + "lib/systemd/system/other.service", body: "[Service]\n"}, tent{name: sysroot + "var/lib/casaos/a/b/deep", mode: 0o600, body: "x"})
	res, err := unpackOn(t, d, nil, ents...)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]fs.FileMode{
		"/usr/bin/casaos-virt-management": 0o755, "/usr/share/casaos/virt/index.html": 0o644, "/etc/casaos/virt-management.conf.sample": 0o644,
		"/usr/lib/systemd/system/casaos-virt-management.service": 0o644, "/var/lib/casaos/a/b/deep": 0o600,
		"/var/lib/casaos/virt": fs.ModeDir | 0o750, "/var/lib/casaos/a": fs.ModeDir | 0o755, "/var/lib/casaos/a/b": fs.ModeDir | 0o755, "/usr/share/casaos/virt": fs.ModeDir | 0o755,
	} {
		if mode, err := d.Lstat(p); err != nil || mode != want {
			t.Errorf("%s: %v %v, want %v", p, mode, err, want)
		}
	}
	if target, err := os.Readlink(d.at("/usr/bin/virt")); err != nil || target != "casaos-virt-management" {
		t.Errorf("the link: %q %v", target, err)
	}
	if body, err := os.ReadFile(d.at("/usr/lib/systemd/system/other.service")); err != nil || string(body) != "[Service]\n" {
		t.Errorf("a unit written under lib: %q %v", body, err)
	}
	// no temporary file is left behind
	for p := range snapshot(t, d) {
		if strings.HasPrefix(path.Base(p), ".t") {
			t.Errorf("a temporary file: %s", p)
		}
	}
	// the remove: the files of the list, then the directories made, deepest first, as the unpack gave them
	for _, p := range res.Files {
		if err := os.Remove(d.at(p)); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	for _, p := range res.Dirs {
		if err := os.Remove(d.at(p)); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	sameTree(t, "after the remove", before, snapshot(t, d))
}

func TestOnARealTreeTheCoresOwnStateAndTheUnitDirectoriesAreNotWrittenTo(t *testing.T) {
	d := newTree(t)
	before := snapshot(t, d)
	for _, rel := range []string{
		"var/lib/casaos/os-upgrade/busy", "etc/casaos/os-upgrade.beta", "var/lib/casaos/modules/virt.files", "etc/casaos/modules/virt.json",
		"usr/lib/systemd/system/casaos-gateway.service.d/override.conf", "lib/systemd/system/casaos-gateway.service.d/override.conf",
		"usr/lib/systemd/system/multi-user.target.wants/x.service", "lib/systemd/system/multi-user.target.wants/x.service",
	} {
		_, err := unpackOn(t, d, nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/bin/fine", body: "f", mode: 0o755}, tent{name: sysroot + rel, body: "/usr/bin/casaos\n"})
		wantRefusedFor(t, rel, err, "unpack_path")
	}
	sameTree(t, "after the refusals", before, snapshot(t, d))
}

func TestOnARealTreeAFileThatAppearsAfterTheLastCheckIsNotOverwrittenAndNotListed(t *testing.T) {
	d := newTree(t)
	good := tarOf(t, goodEntries()...)
	sample := d.at("/etc/casaos/virt-management.conf.sample")
	res, err := Unpack(seekHook(good, func(n int, _ *hooked) {
		if n == 2 { // the program was copied, and the sample is next
			if err := os.WriteFile(sample, []byte("foreign"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}), d, nil, nil)
	wantRefused(t, err, "file_exists")
	if body, _ := os.ReadFile(sample); string(body) != "foreign" {
		t.Errorf("the file that appeared is now %q", body)
	}
	if fi, _ := os.Lstat(sample); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("its mode is %v", fi)
	}
	if !slices.Equal(res.Files, []string{"/usr/bin/casaos-virt-management", "/usr/bin/virt"}) {
		t.Errorf("files = %v", res.Files)
	}
	// nothing is left of the file that was being written
	entries, _ := os.ReadDir(d.at("/etc/casaos"))
	if len(entries) != 1 {
		t.Errorf("etc/casaos holds %d files", len(entries))
	}
	// the same with a link
	d = newTree(t)
	res, err = Unpack(seekHook(good, func(n int, _ *hooked) {
		if n == 1 {
			os.Symlink("/etc/passwd", d.at("/usr/bin/virt"))
		}
	}), d, nil, nil)
	wantRefused(t, err, "file_exists")
	if target, _ := os.Readlink(d.at("/usr/bin/virt")); target != "/etc/passwd" || !slices.Equal(res.Files, []string{"/usr/bin/casaos-virt-management"}) {
		t.Errorf("the link is %q, files %v", target, res.Files)
	}
	// a file of the module's own is replaced, with the mode of the archive
	d = newTree(t)
	os.WriteFile(d.at("/usr/bin/casaos-virt-management"), []byte("old"), 0o600)
	res, err = Unpack(bytes.NewReader(good), d, []string{"/usr/bin/casaos-virt-management"}, nil)
	if fi, serr := os.Lstat(d.at("/usr/bin/casaos-virt-management")); err != nil || serr != nil || fi.Mode().Perm() != 0o755 || fi.Size() != 1200 {
		t.Errorf("an own file: %v %v %v", err, fi, res.Files)
	}
}

func TestOnARealTreeWithAMergedUsrTheTwoNamesOfALibFileAreTheOneFile(t *testing.T) {
	d := newTree(t)
	os.WriteFile(d.at("/lib/systemd/system/a.service"), []byte("old"), 0o644) // (through the link)
	if body, err := os.ReadFile(d.at("/usr/lib/systemd/system/a.service")); err != nil || string(body) != "old" {
		t.Fatalf("the link is not one: %q %v", body, err)
	}
	// listed as /lib, written as /usr/lib: the same file, replaced
	res, err := unpackOn(t, d, []string{"/lib/systemd/system/a.service"}, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/lib/systemd/system/a.service", body: "new"})
	if body, _ := os.ReadFile(d.at("/lib/systemd/system/a.service")); err != nil || string(body) != "new" || !slices.Equal(res.Files, []string{"/usr/lib/systemd/system/a.service"}) {
		t.Errorf("%v %q %v", err, body, res.Files)
	}
	// and the other way
	res, err = unpackOn(t, d, []string{"/usr/lib/systemd/system/a.service"}, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "lib/systemd/system/a.service", body: "newer"})
	if body, _ := os.ReadFile(d.at("/usr/lib/systemd/system/a.service")); err != nil || string(body) != "newer" || !slices.Equal(res.Files, []string{"/lib/systemd/system/a.service"}) {
		t.Errorf("%v %q %v", err, body, res.Files)
	}
	// somebody else's file under either name is not overwritten
	before := snapshot(t, d)
	for _, rel := range []string{"lib/systemd/system/a.service", "usr/lib/systemd/system/a.service"} {
		_, err = unpackOn(t, d, nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + rel, body: "mine"})
		wantRefusedFor(t, rel, err, "file_exists")
	}
	sameTree(t, "after the refusals", before, snapshot(t, d))
	// an archive that has both names writes nothing
	_, err = unpackOn(t, d, []string{"/lib/systemd/system/a.service"}, tent{name: "module.json", body: "{}"},
		tent{name: sysroot + "lib/systemd/system/a.service", body: "1"}, tent{name: sysroot + "usr/lib/systemd/system/a.service", body: "2"})
	wantRefused(t, err, "unpack_path")
	sameTree(t, "after the archive with two names", before, snapshot(t, d))
}

func TestOnARealTreeTheModulesOwnLinkIsReplacedAndAnotherIsNot(t *testing.T) {
	d := newTree(t)
	ents := []tent{{name: "module.json", body: "{}"}, {name: sysroot + "usr/bin/virt", typ: tar.TypeSymlink, link: "new"}}
	os.Symlink("old", d.at("/usr/bin/virt"))
	res, err := unpackOn(t, d, []string{"/usr/bin/virt"}, ents...)
	if target, _ := os.Readlink(d.at("/usr/bin/virt")); err != nil || target != "new" || !slices.Equal(res.Files, []string{"/usr/bin/virt"}) {
		t.Errorf("%v %q %v", err, target, res.Files)
	}
	d = newTree(t)
	os.Symlink("/etc/passwd", d.at("/usr/bin/virt"))
	before := snapshot(t, d)
	_, err = unpackOn(t, d, nil, ents...)
	wantRefused(t, err, "unpack_path")
	sameTree(t, "after the refusal", before, snapshot(t, d))
}

func TestOnARealTreeTheDirectoriesAreCappedAndTheOnesMadeAreDeleted(t *testing.T) {
	d := newTree(t)
	before := snapshot(t, d)
	files := func(n int) []tent {
		ents := []tent{{name: "module.json", body: "{}"}}
		for i := 0; i < n; i++ {
			ents = append(ents, tent{name: fmt.Sprintf("%svar/lib/casaos/d%d/e/f", sysroot, i)})
		}
		return ents
	}
	_, err := unpackOn(t, d, nil, files(MaxDirs/2+1)...)
	wantRefused(t, err, "unpack_limits")
	sameTree(t, "after the refusal", before, snapshot(t, d))
	res, err := unpackOn(t, d, nil, files(MaxDirs/2)...)
	if err != nil || len(res.Dirs) != MaxDirs {
		t.Fatalf("%d directories: %v", len(res.Dirs), err)
	}
	for _, p := range res.Files {
		if err := os.Remove(d.at(p)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range res.Dirs {
		if err := os.Remove(d.at(p)); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	sameTree(t, "after the remove", before, snapshot(t, d))
	// a chain of directories deeper than the bound is not made
	deep := strings.Repeat("d/", MaxDepth)
	_, err = unpackOn(t, d, nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "var/lib/casaos/" + deep + "f"})
	wantRefused(t, err, "unpack_path")
	sameTree(t, "after the deep path", before, snapshot(t, d))
}
