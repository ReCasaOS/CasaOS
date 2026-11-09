package share

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet", StateFile)
	if _, ok := loadState(path); ok {
		t.Fatal("missing file loaded")
	}
	want := State{Name: "mybox", Host: "mybox.box.example.org", Domain: "box.example.org",
		TOSAcceptedAt: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC),
		Apps:          map[string]App{"jellyfin": {Port: "8096", Scheme: "http"}}}
	if err := saveState(path, want); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, err %v", info.Mode(), err)
		}
	}
	got, ok := loadState(path)
	if !ok || got.Name != want.Name || got.Host != want.Host || got.Domain != want.Domain ||
		!got.TOSAcceptedAt.Equal(want.TOSAcceptedAt) || got.Apps["jellyfin"] != want.Apps["jellyfin"] {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	if err := clearState(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadState(path); ok {
		t.Fatal("cleared file loaded")
	}
	if err := clearState(path); err != nil {
		t.Fatalf("clearing a missing file: %v", err)
	}
}

func TestLabel(t *testing.T) {
	for in, want := range map[string]string{"Jellyfin": "jellyfin", "Home Assistant": "home-assistant", "my_app.v2": "my-app-v2", "a": "a"} {
		if got, err := Label(in); err != nil || got != want {
			t.Errorf("Label(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"---", "", strings.Repeat("a", 64)} {
		if _, err := Label(in); !errors.Is(err, ErrBadLabel) {
			t.Errorf("Label(%q) err = %v", in, err)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"mybox", "my-box-01", "abc", strings.Repeat("a", 32)} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"ab", "-abc", "abc-", "ABC", strings.Repeat("a", 33)} {
		if err := ValidName(bad); !errors.Is(err, ErrBadName) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}
