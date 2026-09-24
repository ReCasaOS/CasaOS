package autoupdate

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
)

// The results of an attempt.
const (
	resultRunning   = "running"
	resultSucceeded = "succeeded"
	resultFailed    = "failed"
)

// ErrWindow is a window_start or window_end that is not HH:MM, or a window
// shorter than an hour.
var ErrWindow = errors.New("window_start and window_end are HH:MM, local time, at least an hour apart")

// State is autoupdate.json.
type State struct {
	Enabled     bool      `json:"enabled"`
	WindowStart string    `json:"window_start"`
	WindowEnd   string    `json:"window_end"`
	Last        *Last     `json:"last,omitempty"`
	Failures    *Failures `json:"failures,omitempty"`
}

// Last is the last automatic update started, in autoupdate.json and in the API.
type Last struct {
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
	Result    string    `json:"result"` // running, succeeded or failed
}

// Failures counts the failed attempts at one release; the second pauses it.
type Failures struct {
	Version string `json:"version"`
	Count   int    `json:"count"`
}

// defaults is a box that never chose: off, 03:00 to 05:00, nothing attempted.
func defaults() State {
	return State{WindowStart: "03:00", WindowEnd: "05:00"}
}

func (s State) window() window {
	w, _ := parseWindow(s.WindowStart, s.WindowEnd) // load and Update keep it valid
	return w
}

// attempted is when the last attempt started, or the zero time.
func (s State) attempted() time.Time {
	if s.Last == nil {
		return time.Time{}
	}
	return s.Last.StartedAt
}

// load reads autoupdate.json. No file is the defaults; a file that cannot be
// read or parsed, or whose window is not one, is the defaults too, so off:
// when in doubt, nothing starts. The next PUT rewrites it.
func (a *AutoUpdate) load() State {
	s := defaults()
	data, err := os.ReadFile(a.path())
	if err != nil || json.Unmarshal(data, &s) != nil {
		return defaults()
	}
	if _, valid := parseWindow(s.WindowStart, s.WindowEnd); !valid {
		return defaults()
	}
	return s
}

// save writes autoupdate.json atomically and 0600, like telemetry.json.
func (a *AutoUpdate) save(s State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return file.WriteFileAtomic(a.path(), data)
}

// modify loads, changes and saves autoupdate.json in one step, between the API
// and the check. change returns false to leave the file as it is.
func (a *AutoUpdate) modify(change func(*State) bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.load()
	if !change(&s) {
		return nil
	}
	return a.save(s)
}

// Status is what GET and PUT /v1/sys/autoupdate answer in data.
type Status struct {
	Enabled bool   `json:"enabled"`
	Window  Window `json:"window"`
	State   string `json:"state"` // off, up_to_date, waiting, updating or paused
	Next    *Next  `json:"next"`
	Last    *Last  `json:"last"`
}

// Window is the nightly window, HH:MM in the box's local time.
type Window struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// Next is the release aimed at and the earliest time it may start.
type Next struct {
	Version   string    `json:"version"`
	NotBefore time.Time `json:"not_before"`
}

// Status reads version.json through the cache the dashboard's update row
// shares, and only when automatic updates are on. It asks neither systemd nor
// AppManagement: a release they hold shows as waiting until it starts.
func (a *AutoUpdate) Status() Status {
	s := a.load()
	f := facts{now: a.Now(), state: s}
	if s.Enabled {
		f.current, f.latest = a.Current(), a.Releases.GetCasaosVersion()
	}
	v := decide(f)
	return Status{
		Enabled: s.Enabled,
		Window:  Window{Start: s.WindowStart, End: s.WindowEnd},
		State:   v.state,
		Next:    v.next,
		Last:    s.Last,
	}
}

// Change is PUT /v1/sys/autoupdate's body: each field optional, others ignored.
type Change struct {
	Enabled     *bool   `json:"enabled"`
	WindowStart *string `json:"window_start"`
	WindowEnd   *string `json:"window_end"`
	Resume      bool    `json:"resume"` // clears the failures, and so a pause
}

// Update applies c, rewriting a malformed autoupdate.json, and answers the new
// status. A window that is not one is ErrWindow, and nothing is written.
func (a *AutoUpdate) Update(c Change) (Status, error) {
	valid := false
	if err := a.modify(func(s *State) bool {
		if c.Enabled != nil {
			s.Enabled = *c.Enabled
		}
		if c.WindowStart != nil {
			s.WindowStart = *c.WindowStart
		}
		if c.WindowEnd != nil {
			s.WindowEnd = *c.WindowEnd
		}
		if c.Resume {
			s.Failures = nil
		}
		_, valid = parseWindow(s.WindowStart, s.WindowEnd)
		return valid
	}); err != nil {
		return Status{}, err
	}
	if !valid {
		return Status{}, ErrWindow
	}
	return a.Status(), nil
}
