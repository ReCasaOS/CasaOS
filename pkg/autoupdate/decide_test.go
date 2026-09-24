package autoupdate

import (
	"testing"
	"time"
	_ "time/tzdata" // Europe/Paris on any test machine

	"github.com/ReCasaOS/CasaOS/model"
)

// paris has daylight saving time, as most boxes' local time does.
var paris = func() *time.Location {
	location, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		panic(err)
	}
	return location
}()

// local is a wall-clock time in Paris, "2006-01-02 15:04".
func local(value string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", value, paris)
	if err != nil {
		panic(err)
	}
	return t
}

// ready are the facts of a box on which every condition holds: turned on,
// 03:30 inside the 03:00-05:00 window, v0.5.7 installed, v0.5.8 out for three
// days, nothing paused, attempted or running.
func ready() facts {
	return facts{
		now:     local("2026-09-24 03:30"),
		state:   State{Enabled: true, WindowStart: "03:00", WindowEnd: "05:00"},
		current: "0.5.7",
		latest:  model.Version{Version: "v0.5.8", PublishedAt: "2026-09-21T10:00:00Z"},
	}
}

func TestTheDecision(t *testing.T) {
	now := local("2026-09-24 03:30")
	aim := func(version string, notBefore time.Time) *Next { return &Next{Version: version, NotBefore: notBefore} }
	cases := []struct {
		name   string
		change func(*facts)
		state  string
		next   *Next
		start  bool
	}{
		{"every condition holds: start", func(*facts) {}, stateWaiting, aim("v0.5.8", now), true},

		{"1. turned off", func(f *facts) { f.state.Enabled = false }, stateOff, nil, false},

		{"2. before the window opens", func(f *facts) { f.now = local("2026-09-24 02:59") }, stateWaiting, aim("v0.5.8", local("2026-09-24 03:00")), false},
		{"2. once it has closed", func(f *facts) { f.now = local("2026-09-24 05:00") }, stateWaiting, aim("v0.5.8", local("2026-09-25 03:00")), false},
		{"2. the minute it opens", func(f *facts) { f.now = local("2026-09-24 03:00") }, stateWaiting, aim("v0.5.8", local("2026-09-24 03:00")), true},

		{"3. the installed release", func(f *facts) { f.latest.Version = "v0.5.7" }, stateUpToDate, nil, false},
		{"3. an older release", func(f *facts) { f.latest.Version = "v0.5.6" }, stateUpToDate, nil, false},
		{"3. version.json could not be read", func(f *facts) { f.latest = model.Version{} }, stateUpToDate, nil, false},

		{"4. 47 hours old: waits for its 48th, inside the window", func(f *facts) { f.latest.PublishedAt = "2026-09-22T02:00:00Z" }, stateWaiting, aim("v0.5.8", local("2026-09-24 04:00")), false},
		{"4. 48 hours old after tonight's window: tomorrow night", func(f *facts) { f.latest.PublishedAt = "2026-09-22T10:00:00Z" }, stateWaiting, aim("v0.5.8", local("2026-09-25 03:00")), false},
		{"4. exactly 48 hours old", func(f *facts) { f.latest.PublishedAt = "2026-09-22T01:30:00Z" }, stateWaiting, aim("v0.5.8", now), true},
		{"4. no published_at: never by itself", func(f *facts) { f.latest.PublishedAt = "" }, stateWaiting, nil, false},
		{"4. a published_at that is not a time", func(f *facts) { f.latest.PublishedAt = "last week" }, stateWaiting, nil, false},

		{"5. failed twice: paused", func(f *facts) { f.state.Failures = &Failures{Version: "v0.5.8", Count: 2} }, statePaused, nil, false},
		{"5. failed once: tried again", func(f *facts) { f.state.Failures = &Failures{Version: "v0.5.8", Count: 1} }, stateWaiting, aim("v0.5.8", now), true},
		{"5. a newer release than the paused one is tried", func(f *facts) {
			f.state.Failures = &Failures{Version: "v0.5.8", Count: 2}
			f.latest.Version = "v0.5.9"
		}, stateWaiting, aim("v0.5.9", now), true},

		{"6. casaos-update runs", func(f *facts) { f.unitActive = true }, stateWaiting, aim("v0.5.8", now), false},

		{"7. attempted tonight: tomorrow night", func(f *facts) {
			f.state.Last = &Last{Version: "v0.5.8", StartedAt: local("2026-09-24 03:05"), Result: resultFailed}
			f.state.Failures = &Failures{Version: "v0.5.8", Count: 1}
		}, stateWaiting, aim("v0.5.8", local("2026-09-25 03:00")), false},
		{"7. attempted last night: again tonight", func(f *facts) {
			f.state.Last = &Last{Version: "v0.5.8", StartedAt: local("2026-09-23 03:05"), Result: resultFailed}
			f.state.Failures = &Failures{Version: "v0.5.8", Count: 1}
		}, stateWaiting, aim("v0.5.8", now), true},
		{"7. updated tonight, then a newer release: tomorrow night", func(f *facts) {
			f.state.Last = &Last{Version: "v0.5.7", StartedAt: local("2026-09-24 03:05"), Result: resultSucceeded}
		}, stateWaiting, aim("v0.5.8", local("2026-09-25 03:00")), false},

		{"8. AppManagement lists an operation, or did not answer", func(f *facts) { f.appsBusy = true }, stateWaiting, aim("v0.5.8", now), false},

		{"an update running", func(f *facts) {
			f.state.Last = &Last{Version: "v0.5.8", StartedAt: local("2026-09-24 03:05"), Result: resultRunning}
		}, stateUpdating, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ready()
			tc.change(&f)
			got := decide(f)
			if got.state != tc.state || got.start != tc.start || !sameNext(got.next, tc.next) {
				t.Fatalf("decide() = {%s %s start=%v}, want {%s %s start=%v}", got.state, describe(got.next), got.start, tc.state, describe(tc.next), tc.start)
			}
		})
	}
}

func TestAWindowAcrossMidnight(t *testing.T) {
	cases := []struct {
		name      string
		now       string
		attempted string
		notBefore string
	}{
		{"before midnight", "2026-09-23 23:30", "", "2026-09-23 23:30"},
		{"after midnight, same night", "2026-09-24 00:30", "", "2026-09-24 00:30"},
		{"attempted before midnight: that night is done", "2026-09-24 00:30", "2026-09-23 23:10", "2026-09-24 23:00"},
		{"attempted the night before: tonight is free", "2026-09-24 00:30", "2026-09-23 00:10", "2026-09-24 00:30"},
		{"closed at 01:00", "2026-09-24 01:00", "", "2026-09-24 23:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ready()
			f.now = local(tc.now)
			f.state.WindowStart, f.state.WindowEnd = "23:00", "01:00"
			if tc.attempted != "" {
				f.state.Last = &Last{Version: "v0.5.8", StartedAt: local(tc.attempted), Result: resultFailed}
			}
			got := decide(f)
			if got.next == nil || !got.next.NotBefore.Equal(local(tc.notBefore)) {
				t.Fatalf("not_before = %s, want %s", describe(got.next), tc.notBefore)
			}
			if want := tc.now == tc.notBefore; got.start != want {
				t.Fatalf("start = %v, want %v", got.start, want)
			}
		})
	}
}

// The window is read on each night's wall clock, never in steps of 24 hours:
// 03:00 is 03:00 on the night the clocks change too.
func TestTheWindowAcrossDaylightSavingChanges(t *testing.T) {
	f := ready()
	f.latest.PublishedAt = "2026-03-01T10:00:00Z"
	f.now = local("2026-03-28 12:00") // CET; the clocks go forward at 02:00 tonight
	got := decide(f)
	if want := time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC); got.next == nil || !got.next.NotBefore.Equal(want) {
		t.Fatalf("spring: not_before = %s, want 03:00 CEST (%s)", describe(got.next), want)
	}

	f.now = time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC).In(paris) // 03:00 CEST
	if got := decide(f); !got.start {
		t.Fatalf("spring: at 03:00 CEST, decide() = %+v, want start", got)
	}

	// The clocks go back at 03:00 CEST, and 02:00 to 03:00 happens twice.
	f.latest.PublishedAt = "2026-10-01T10:00:00Z"
	f.state.WindowStart, f.state.WindowEnd = "02:00", "04:00"
	for _, now := range []time.Time{
		time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC), // 02:30 CET, the second one
		time.Date(2026, 10, 25, 2, 30, 0, 0, time.UTC), // 03:30 CET
	} {
		f.now = now.In(paris)
		if got := decide(f); !got.start {
			t.Errorf("autumn: at %s, decide() = %+v, want start", f.now, got)
		}
	}
	f.now = time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC).In(paris) // 04:00 CET
	if got := decide(f); got.start {
		t.Errorf("autumn: at 04:00 CET, decide() = %+v, want the window closed", got)
	}
}

func TestParseWindow(t *testing.T) {
	cases := []struct {
		start, end string
		valid      bool
	}{
		{"03:00", "05:00", true},
		{"23:00", "01:00", true},
		{"23:30", "00:30", true},
		{"00:00", "23:59", true},
		{"03:00", "03:59", false},
		{"03:00", "03:00", false},
		{"05:00", "04:30", true},
		{"3:00", "05:00", false},
		{"03:00", "24:00", false},
		{"03:60", "05:00", false},
		{"", "05:00", false},
		{"03:00:00", "05:00", false},
	}
	for _, tc := range cases {
		if _, valid := parseWindow(tc.start, tc.end); valid != tc.valid {
			t.Errorf("parseWindow(%q, %q) valid = %v, want %v", tc.start, tc.end, valid, tc.valid)
		}
	}
}

func sameNext(a, b *Next) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Version == b.Version && a.NotBefore.Equal(b.NotBefore)
}

func describe(next *Next) string {
	if next == nil {
		return "next=null"
	}
	return "next=" + next.Version + " after " + next.NotBefore.In(paris).Format("2006-01-02 15:04 MST")
}
