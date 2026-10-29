package hoststats

import (
	"path/filepath"
	"testing"
)

func TestDirForDBPath(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "stats", "core")
	if got := DirForDBPath(dir); got != want {
		t.Fatalf("DirForDBPath(%q) = %q, want %q", dir, got, want)
	}
}
