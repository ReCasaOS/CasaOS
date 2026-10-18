//go:build linux

package modulecmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// a /lib that cannot be looked at is not a /lib that is a directory
func TestALibThatCannotBeLookedAtIsAnError(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(prefix, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := Env{Roots: Roots{Prefix: prefix}}.withDefaults()
	if merged, err := (&box{ctx: context.Background(), env: &env}).MergedUsr(); err == nil {
		t.Errorf("a file for a root: %v", merged)
	}
}
