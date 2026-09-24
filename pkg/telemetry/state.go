package telemetry

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
)

// State is telemetry.json.
type State struct {
	Enabled    bool      `json:"enabled"`
	ID         string    `json:"id"`
	LastSent   time.Time `json:"last_sent,omitzero"`
	NoticeSeen bool      `json:"notice_seen"`
}

// load reads telemetry.json. No file is the default: enabled, no id yet, never
// sent, notice not seen. A file that cannot be read or parsed is disabled: when
// in doubt, send nothing. The next PUT rewrites it.
func (t *Telemetry) load() State {
	data, err := os.ReadFile(t.path(StateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return State{Enabled: true}
	}
	var s State
	if err != nil || json.Unmarshal(data, &s) != nil {
		return State{}
	}
	return s
}

// save writes telemetry.json atomically and 0600: after a power cut the file is
// the old state or the new one, never an empty one that load would read as
// disabled.
func (t *Telemetry) save(s State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return file.WriteFileAtomic(t.path(StateFile), data)
}

// modify loads, changes and saves telemetry.json in one step, between the API
// and the check.
func (t *Telemetry) modify(change func(*State)) (State, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.load()
	change(&s)
	return s, t.save(s)
}

// ApplyOffMarker folds the installer's telemetry-off marker into telemetry.json:
// statistics off, the id kept, the marker deleted. main runs it at startup,
// before the API serves and before the first check.
func (t *Telemetry) ApplyOffMarker() error {
	marker := t.path(offMarkerFile)
	if _, err := os.Stat(marker); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if _, err := t.modify(func(s *State) { s.Enabled = false }); err != nil {
		return err
	}
	return os.Remove(marker)
}
