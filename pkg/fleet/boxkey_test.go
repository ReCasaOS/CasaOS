package fleet

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBoxKeyCreatedOnceAndReloaded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fleet")
	first, err := loadBoxKey(dir)
	if err != nil || len(first) != 64 {
		t.Fatalf("loadBoxKey = %d bytes, %v", len(first), err)
	}
	second, err := loadBoxKey(dir)
	if err != nil || !first.Equal(second) {
		t.Fatalf("reloaded a different key: %v", err)
	}
	if other, _ := loadBoxKey(t.TempDir()); other.Equal(first) {
		t.Fatal("two boxes, one key")
	}
	if info, err := os.Stat(filepath.Join(dir, boxKeyFile)); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}
}

func TestBoxKeyCorruptIsNotRegenerated(t *testing.T) {
	for name, content := range map[string]string{
		"not json":     "{",
		"empty":        "",
		"no key":       `{}`,
		"wrong length": `{"private_key":"AAAA"}`,
		"padded":       `{"private_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`,
		"not base64":   `{"private_key":"!!!"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, boxKeyFile)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if key, err := loadBoxKey(dir); err == nil {
				t.Fatalf("loaded %d bytes", len(key))
			}
			if got, _ := os.ReadFile(path); string(got) != content {
				t.Fatalf("the damaged file was rewritten: %q", got)
			}
		})
	}
}

func TestBoxKeyNeedsADirectory(t *testing.T) {
	if _, err := loadBoxKey(""); err == nil {
		t.Fatal("an empty dir would write boxkey.json in the working directory")
	}
}
