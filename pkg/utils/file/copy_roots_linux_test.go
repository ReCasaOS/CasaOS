package file

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/fileroots"
)

// [file] ManagementRoots: a link inside a copied folder is copied as what it
// points to, so one that leads out is skipped (the copy says so at its end);
// one that stays inside is copied
func TestCopyDirSkipsALinkOutOfTheRoots(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, secret := filepath.Join(base, "data"), filepath.Join(base, "secret")
	src := filepath.Join(data, "Photos")
	for _, dir := range []string{src, filepath.Join(data, "Backup"), secret} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(secret, "shadow"), "secret")
	writeTestFile(t, filepath.Join(src, "a.jpg"), "a")
	for link, target := range map[string]string{"shadow": "../../secret/shadow", "secrets": "../../secret", "same": "a.jpg"} {
		if err := os.Symlink(target, filepath.Join(src, link)); err != nil {
			t.Fatal(err)
		}
	}
	roots, err := fileroots.Open([]string{data})
	if err != nil {
		t.Fatal(err)
	}
	previous := fileroots.Default
	fileroots.Default = roots
	t.Cleanup(func() { fileroots.Default = previous })

	dst := filepath.Join(data, "Backup")
	if err := CopyDir(src, dst, "overwrite"); !errors.Is(err, ErrLinkOutside) {
		t.Errorf("CopyDir() = %v, want ErrLinkOutside", err)
	}
	for _, out := range []string{"shadow", "secrets"} {
		if _, err := os.Lstat(filepath.Join(dst, "Photos", out)); err == nil {
			t.Errorf("%s was copied: what it points to came inside", out)
		}
	}
	for _, in := range []string{"a.jpg", "same"} {
		if b, err := os.ReadFile(filepath.Join(dst, "Photos", in)); err != nil || string(b) != "a" {
			t.Errorf("%s: %q %v, want copied", in, b, err)
		}
	}
}

// unconfined: as before (a link is copied as what it points to)
func TestCopyDirUnconfinedFollowsLinks(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "Photos")
	_ = os.MkdirAll(src, 0o755)
	writeTestFile(t, filepath.Join(base, "outside"), "o")
	if err := os.Symlink("../outside", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := CopyDir(src, dst, "overwrite"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "Photos", "link")); err != nil || string(b) != "o" {
		t.Errorf("link: %q %v", b, err)
	}
}

// a link at a chunk's name (one not yet uploaded) is not read into the uploaded file
func TestSpliceReadsNoLink(t *testing.T) {
	dir, out := t.TempDir(), filepath.Join(t.TempDir(), "big.iso")
	writeTestFile(t, filepath.Join(dir, "1"), "one")
	secret := filepath.Join(t.TempDir(), "secret")
	writeTestFile(t, secret, "secret")
	if err := os.Symlink(secret, filepath.Join(dir, "2")); err != nil {
		t.Fatal(err)
	}
	if err := SpliceFiles(dir, out, 2, 1); err == nil {
		t.Error("SpliceFiles read through a link")
	}
	if b, _ := os.ReadFile(out); string(b) == "onesecret" {
		t.Error("the secret is in the uploaded file")
	}

	_ = os.Remove(filepath.Join(dir, "2"))
	writeTestFile(t, filepath.Join(dir, "2"), "two")
	if err := SpliceFiles(dir, out, 2, 1); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "onetwo" {
		t.Errorf("spliced: %q", b)
	}
}

// An account's copy keeps a link only below its folders too: a link in its folder to
// another folder of the box is not copied as what it points to, even unconfined.
func TestCopyOfAnAccountSkipsALinkOutOfItsFolders(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	kids, other := filepath.Join(base, "Kids"), filepath.Join(base, "Other")
	src := filepath.Join(kids, "Photos")
	for _, dir := range []string{src, filepath.Join(kids, "Copy"), other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(other, "private"), "private")
	writeTestFile(t, filepath.Join(src, "a.jpg"), "a")
	if err := os.Symlink("../../Other/private", filepath.Join(src, "leak")); err != nil {
		t.Fatal(err)
	}
	account, err := fileroots.Open([]string{kids})
	if err != nil {
		t.Fatal(err)
	}
	previous := fileroots.Default
	fileroots.Default = nil
	t.Cleanup(func() { fileroots.Default = previous })

	dst := filepath.Join(kids, "Copy")
	if err := CopyDirContext(WithLinkRoots(context.Background(), account), src, dst, "overwrite"); !errors.Is(err, ErrLinkOutside) {
		t.Errorf("copy = %v, want ErrLinkOutside", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "Photos", "leak")); err == nil {
		t.Error("the link was copied: a file of another folder came into the account's")
	}
	if b, err := os.ReadFile(filepath.Join(dst, "Photos", "a.jpg")); err != nil || string(b) != "a" {
		t.Errorf("a.jpg: %q %v", b, err)
	}
}
