package autoupdate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

// stateOnDisk is autoupdate.json as JSON values, the way another program reads it.
func stateOnDisk(t *testing.T, root string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("autoupdate.json is not JSON: %v\n%s", err, data)
	}
	return state
}

func TestNoStateFileMeansOffBetween3And5NothingAttempted(t *testing.T) {
	want := State{WindowStart: "03:00", WindowEnd: "05:00"}
	if got := New(t.TempDir()).load(); !reflect.DeepEqual(got, want) {
		t.Fatalf("load() with no file = %+v, want %+v", got, want)
	}
}

func TestAStateFileThatCannotBeReadMeansOff(t *testing.T) {
	for _, content := range []string{
		"",
		"{",
		"enabled",
		`{"enabled": "yes"}`,
		`{"enabled": true, "window_start": "25:00"}`,
		`{"enabled": true, "window_start": "04:30", "window_end": "05:00"}`,
		`{"enabled": true, "last": {"version": "v0.5.8", "started_at": "last night", "result": "running"}}`,
		`{"enabled": true, "failures": {"version": "v0.5.8", "count": "two"}}`,
	} {
		root := t.TempDir()
		write(t, root, StateFile, content)
		if s := New(root).load(); s.Enabled || s.Last != nil || s.Failures != nil {
			t.Errorf("load() of %q = %+v, want off and nothing attempted", content, s)
		}
	}
}

func TestAStateFileWithoutAWindowHasTheDefaultOne(t *testing.T) {
	root := t.TempDir()
	write(t, root, StateFile, `{"enabled":true}`)
	if s := New(root).load(); !s.Enabled || s.WindowStart != "03:00" || s.WindowEnd != "05:00" {
		t.Fatalf("load() = %+v, want on, 03:00 to 05:00", s)
	}
}

func TestTheStateIsSavedAsA0600JSONFile(t *testing.T) {
	root := t.TempDir()
	// A file made by hand, readable by all: saving makes it 0600 again.
	write(t, root, StateFile, `{"enabled":false}`)
	a := New(root)
	want := State{
		Enabled:     true,
		WindowStart: "23:00",
		WindowEnd:   "01:00",
		Last:        &Last{Version: "v0.5.8", StartedAt: time.Date(2026, 9, 24, 1, 5, 0, 0, time.UTC), Result: resultFailed},
		Failures:    &Failures{Version: "v0.5.8", Count: 1},
	}
	if err := a.save(want); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(root, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("autoupdate.json mode = %v, want 0600", info.Mode().Perm())
	}
	wantOnDisk := map[string]any{
		"enabled":      true,
		"window_start": "23:00",
		"window_end":   "01:00",
		"last":         map[string]any{"version": "v0.5.8", "started_at": "2026-09-24T01:05:00Z", "result": "failed"},
		"failures":     map[string]any{"version": "v0.5.8", "count": float64(1)},
	}
	if got := stateOnDisk(t, root); !reflect.DeepEqual(got, wantOnDisk) {
		t.Fatalf("autoupdate.json = %v, want %v", got, wantOnDisk)
	}
	if got := a.load(); !reflect.DeepEqual(got, want) {
		t.Fatalf("load() after save = %+v, want %+v", got, want)
	}
	// The rename leaves nothing behind.
	entries, err := os.ReadDir(filepath.Dir(filepath.Join(root, StateFile)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d files next to autoupdate.json, want it alone", len(entries))
	}
}

func TestNothingAttemptedIsNeitherLastNorFailures(t *testing.T) {
	root := t.TempDir()
	if err := New(root).save(defaults()); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"enabled": false, "window_start": "03:00", "window_end": "05:00"}
	if got := stateOnDisk(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("autoupdate.json = %v, want %v", got, want)
	}
}

func TestUpdateChangesWhatIsGivenAndKeepsTheRest(t *testing.T) {
	a, _ := newBox(t)
	saveState(t, a, State{
		WindowStart: "03:00",
		WindowEnd:   "05:00",
		Last:        &Last{Version: "v0.5.8", StartedAt: time.Date(2026, 9, 23, 1, 5, 0, 0, time.UTC), Result: resultFailed},
		Failures:    &Failures{Version: "v0.5.8", Count: 2},
	})
	on, start := true, "01:00"

	status, err := a.Update(Change{Enabled: &on, WindowStart: &start})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || status.Window != (Window{Start: "01:00", End: "05:00"}) || status.State != statePaused || status.Next != nil {
		t.Fatalf("Update() = %+v, want on, 01:00 to 05:00, still paused", status)
	}
	if s := a.load(); s.Last == nil || s.Failures == nil || s.Failures.Count != 2 {
		t.Fatalf("autoupdate.json = %+v, want the attempt and the pause kept", s)
	}

	status, err = a.Update(Change{Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != stateWaiting || status.Next == nil || status.Next.Version != "v0.5.8" {
		t.Fatalf("Update(resume) = %+v, want v0.5.8 waiting again", status)
	}
	if s := a.load(); s.Failures != nil || s.Last == nil || !s.Enabled {
		t.Fatalf("autoupdate.json = %+v, want the failures cleared and the rest kept", s)
	}
}

func TestUpdateRefusesAWindowThatIsNotOne(t *testing.T) {
	for _, tc := range []struct{ start, end string }{
		{"25:00", "05:00"},
		{"3:00", "05:00"},
		{"03:00", "3:30"},
		{"04:30", "05:00"}, // under an hour
		{"05:00", "05:00"},
	} {
		a, _ := newBox(t)
		start, end := tc.start, tc.end
		if _, err := a.Update(Change{WindowStart: &start, WindowEnd: &end}); !errors.Is(err, ErrWindow) {
			t.Errorf("Update(%s to %s) error = %v, want ErrWindow", tc.start, tc.end, err)
		}
		if _, err := os.Stat(filepath.Join(a.Root, StateFile)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Update(%s to %s) wrote autoupdate.json", tc.start, tc.end)
		}
	}

	// A single end that would make the window too short with the kept start.
	a, _ := newBox(t)
	end := "03:30"
	if _, err := a.Update(Change{WindowEnd: &end}); !errors.Is(err, ErrWindow) {
		t.Errorf("Update(end 03:30) with 03:00 kept: error = %v, want ErrWindow", err)
	}
}

func TestUpdateRewritesAStateFileThatCannotBeRead(t *testing.T) {
	a, _ := newBox(t)
	write(t, a.Root, StateFile, "{")
	on := true
	if _, err := a.Update(Change{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	if s := a.load(); !s.Enabled || s.WindowStart != "03:00" || s.WindowEnd != "05:00" {
		t.Fatalf("autoupdate.json = %+v, want on with the default window", s)
	}
}
