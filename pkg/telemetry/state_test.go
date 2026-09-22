package telemetry

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// write creates name, an absolute path on a box, under root.
func write(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stateOnDisk is telemetry.json as JSON values, the way the other components read it.
func stateOnDisk(t *testing.T, root string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("telemetry.json is not JSON: %v\n%s", err, data)
	}
	return state
}

// exists reports whether name, an absolute path on a box, exists under root.
func exists(t *testing.T, root, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, name))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func TestNoStateFileMeansEnabledNeverSentNoticeNotSeen(t *testing.T) {
	s := New(t.TempDir()).load()
	if !s.Enabled || s.ID != "" || !s.LastSent.IsZero() || s.NoticeSeen {
		t.Fatalf("load() with no file = %+v, want enabled, no id, never sent, notice not seen", s)
	}
}

func TestAMalformedStateFileMeansDisabled(t *testing.T) {
	for _, content := range []string{
		"",
		"{",
		"enabled",
		`{"enabled": "yes"}`,
		`{"enabled": true, "last_sent": "yesterday"}`,
	} {
		root := t.TempDir()
		write(t, root, StateFile, content)
		if s := New(root).load(); s.Enabled {
			t.Errorf("load() of %q = %+v, want disabled", content, s)
		}
	}
}

func TestTheStateIsSavedAsA0600JSONFile(t *testing.T) {
	root := t.TempDir()
	// A file made by hand, readable by all: saving makes it 0600 again.
	write(t, root, StateFile, `{"enabled":true}`)
	tel := New(root)
	want := State{
		Enabled:    true,
		ID:         "0b0f4d8e-6d7a-4f55-9a53-5b8c1f3e2a10",
		LastSent:   time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC),
		NoticeSeen: true,
	}
	if err := tel.save(want); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(root, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("telemetry.json mode = %v, want 0600", info.Mode().Perm())
	}
	onDisk := stateOnDisk(t, root)
	if onDisk["enabled"] != true || onDisk["id"] != want.ID || onDisk["last_sent"] != "2026-09-22T10:00:00Z" || onDisk["notice_seen"] != true {
		t.Fatalf("telemetry.json = %v", onDisk)
	}
	if got := tel.load(); !got.Enabled || got.ID != want.ID || !got.LastSent.Equal(want.LastSent) || !got.NoticeSeen {
		t.Fatalf("load() after save = %+v, want %+v", got, want)
	}
	// The rename leaves nothing behind.
	entries, err := os.ReadDir(filepath.Dir(filepath.Join(root, StateFile)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d files next to telemetry.json, want it alone", len(entries))
	}
}

func TestANeverSentStateHasNoLastSentAndAnEmptyID(t *testing.T) {
	root := t.TempDir()
	if err := New(root).save(State{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	onDisk := stateOnDisk(t, root)
	if _, ok := onDisk["last_sent"]; ok {
		t.Fatalf("telemetry.json = %v, want no last_sent", onDisk)
	}
	if id, ok := onDisk["id"]; !ok || id != "" {
		t.Fatalf("telemetry.json = %v, want an empty id until the first send", onDisk)
	}
}

func TestTheOffMarkerTurnsStatisticsOffAtStartup(t *testing.T) {
	cases := []struct {
		name, state, wantID string
	}{
		{"a box that had a state keeps its id", `{"enabled":true,"id":"kept-id","last_sent":"2026-09-21T10:00:00Z","notice_seen":true}`, "kept-id"},
		{"a box with no state yet", "", ""},
		{"a box with a malformed state", "{", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.state != "" {
				write(t, root, StateFile, tc.state)
			}
			write(t, root, offMarkerFile, "")

			if err := New(root).ApplyOffMarker(); err != nil {
				t.Fatal(err)
			}

			onDisk := stateOnDisk(t, root)
			if onDisk["enabled"] != false || onDisk["id"] != tc.wantID {
				t.Fatalf("telemetry.json = %v, want enabled false and id %q", onDisk, tc.wantID)
			}
			if exists(t, root, offMarkerFile) {
				t.Fatal("the telemetry-off marker is still there")
			}
		})
	}
}

func TestNoOffMarkerLeavesTheStateAlone(t *testing.T) {
	root := t.TempDir()
	if err := New(root).ApplyOffMarker(); err != nil {
		t.Fatal(err)
	}
	if exists(t, root, StateFile) {
		t.Fatal("telemetry.json was written with no marker")
	}
}
