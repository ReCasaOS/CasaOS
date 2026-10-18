package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func lastRefreshOf(t *testing.T, s OSUpgradeStatus) (string, bool) {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	v, ok := m["last_refresh_at"].(string)
	return v, ok
}

func TestTheStatusTellsTheTimeOfTheLastRefreshOnlyWhenItCanBeTrusted(t *testing.T) {
	box := newUpgradeBox(t)
	if err := os.MkdirAll(osUpgradeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(osUpgradeRoot, osUpgradeLastRefreshFile)
	base := box.GetSystemOSUpgradeStatus()
	if _, ok := lastRefreshOf(t, base); ok {
		t.Fatal("a time is made up when there is no file")
	}
	now := box.now().UTC()
	good := now.Add(-time.Hour).Format(time.RFC3339)
	cases := []struct {
		name  string
		setup func(t *testing.T)
		want  string
	}{
		{"a valid time", func(t *testing.T) { rewrite(t, file, good+"\n") }, good},
		{"another zone is told in UTC", func(t *testing.T) { rewrite(t, file, now.Add(-time.Hour).In(time.FixedZone("x", 7200)).Format(time.RFC3339)) }, good},
		{"a day ahead is still a time", func(t *testing.T) { rewrite(t, file, now.Add(23*time.Hour).Format(time.RFC3339)) }, now.Add(23 * time.Hour).Format(time.RFC3339)},
		{"a time past tomorrow is by hand", func(t *testing.T) { rewrite(t, file, now.Add(25*time.Hour).Format(time.RFC3339)) }, ""},
		{"a malformed time", func(t *testing.T) { rewrite(t, file, "yesterday\n") }, ""},
		{"an oversized file", func(t *testing.T) { rewrite(t, file, good+strings.Repeat(" ", 300)) }, ""},
		{"a link", func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "elsewhere")
			rewrite(t, target, good)
			if err := os.Symlink(target, file); err != nil {
				t.Skip(err)
			}
		}, ""},
		{"a folder", func(t *testing.T) {
			if err := os.Mkdir(file, 0o755); err != nil {
				t.Fatal(err)
			}
		}, ""},
		{"a pipe", func(t *testing.T) {
			if err := syscall.Mkfifo(file, 0o644); err != nil {
				t.Skip(err)
			}
		}, ""},
		{"a file that cannot be read", func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Skip("root reads anything")
			}
			rewrite(t, file, good)
			if err := os.Chmod(file, 0); err != nil {
				t.Fatal(err)
			}
		}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_ = os.RemoveAll(file)
			c.setup(t)
			got := box.GetSystemOSUpgradeStatus()
			v, ok := lastRefreshOf(t, got)
			if c.want == "" && ok || c.want != "" && v != c.want {
				t.Errorf("last_refresh_at is %q (%v), want %q", v, ok, c.want)
			}
			got.LastRefreshAt = nil
			if !reflectEqualJSON(t, got, base) {
				t.Errorf("the rest of the status changed: %+v", got)
			}
		})
	}
}

func TestTheTimeOfTheLastRefreshSurvivesARestartOfTheService(t *testing.T) {
	box := newUpgradeBox(t)
	good := box.now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if err := os.MkdirAll(osUpgradeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	rewrite(t, filepath.Join(osUpgradeRoot, osUpgradeLastRefreshFile), good+"\n")
	fresh := &systemService{packageUpdates: box.updater}
	if v, ok := lastRefreshOf(t, fresh.GetSystemOSUpgradeStatus()); !ok || v != good {
		t.Errorf("a fresh service says %q (%v)", v, ok)
	}
}

func reflectEqualJSON(t *testing.T, a, b OSUpgradeStatus) bool {
	t.Helper()
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
