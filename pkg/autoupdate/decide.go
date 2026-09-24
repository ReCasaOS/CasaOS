package autoupdate

import (
	"time"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/utils/version"
)

// minAge is how old a release must be before it installs itself: time for a bad
// one to be noticed and withdrawn. Fixed, not a setting.
const minAge = 48 * time.Hour

// The states the dashboard shows.
const (
	stateOff      = "off"
	stateUpToDate = "up_to_date"
	stateWaiting  = "waiting" // a newer release, too recent, outside the window or held
	stateUpdating = "updating"
	statePaused   = "paused" // the latest release failed twice
)

// facts are what a check knows of the box.
type facts struct {
	now        time.Time // in the box's local time, which the window is read in
	state      State
	current    string        // the installed release, fork-release
	latest     model.Version // the latest release; empty when version.json could not be read
	unitActive bool          // casaos-update runs: the button's update or ours
	appsBusy   bool          // AppManagement listed an operation, or did not answer
}

// verdict is the dashboard's state, the release aimed at and whether to start it now.
type verdict struct {
	state string
	next  *Next
	start bool
}

// decide holds every condition of an automatic update, as a pure function of
// the facts: the check starts on it, and the dashboard shows it.
func decide(f facts) verdict {
	s := f.state
	switch {
	case !s.Enabled:
		return verdict{state: stateOff}
	case s.Last != nil && s.Last.Result == resultRunning:
		return verdict{state: stateUpdating}
	case !version.IsVersionNewer(f.latest.Version, f.current): // the update button's comparison
		return verdict{state: stateUpToDate}
	case s.Failures != nil && s.Failures.Version == f.latest.Version && s.Failures.Count >= 2:
		return verdict{state: statePaused}
	}
	published, err := time.Parse(time.RFC3339, f.latest.PublishedAt)
	if err != nil {
		// Of unknown age: left to the button, never installed by itself.
		return verdict{state: stateWaiting}
	}
	from := f.now
	if ready := published.Add(minAge).In(f.now.Location()); ready.After(from) {
		from = ready
	}
	notBefore := s.window().earliest(from, s.attempted())
	return verdict{
		state: stateWaiting,
		next:  &Next{Version: f.latest.Version, NotBefore: notBefore},
		start: !notBefore.After(f.now) && !f.unitActive && !f.appsBusy,
	}
}

// window is the nightly window in minutes after local midnight. An end at or
// before the start is on the next day.
type window struct{ start, end int }

// parseWindow reads window_start and window_end: HH:MM, at least an hour
// apart, possibly across midnight.
func parseWindow(start, end string) (window, bool) {
	s, startOK := parseClock(start)
	e, endOK := parseClock(end)
	return window{s, e}, startOK && endOK && (e-s+24*60)%(24*60) >= 60
}

func parseClock(value string) (int, bool) {
	t, err := time.Parse("15:04", value)
	if err != nil || len(value) != len("15:04") {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// occurrence is the window that opens on day's date moved by days, in day's
// location. It is read on each date's wall clock, never in steps of 24 hours,
// so it keeps its hours across daylight saving changes.
func (w window) occurrence(day time.Time, days int) (opens, closes time.Time) {
	y, m, d := day.Date()
	opens = time.Date(y, m, d+days, w.start/60, w.start%60, 0, 0, day.Location())
	if w.end <= w.start {
		days++
	}
	closes = time.Date(y, m, d+days, w.end/60, w.end%60, 0, 0, day.Location())
	return opens, closes
}

// earliest is the first time from t on inside an occurrence of the window in
// which no attempt started: at most one attempt a night. It ends within a few
// occurrences: only yesterday's and today's can be over, and attempted skips
// one more at most.
func (w window) earliest(t, attempted time.Time) time.Time {
	for days := -1; ; days++ {
		opens, closes := w.occurrence(t, days)
		if !closes.After(t) || (!attempted.Before(opens) && attempted.Before(closes)) {
			continue
		}
		if opens.After(t) {
			return opens
		}
		return t
	}
}
