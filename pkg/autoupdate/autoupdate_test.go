package autoupdate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/model"
)

func TestMain(m *testing.M) {
	// The check logs through CasaOS-Common's logger, nil until initialised.
	logger.LogInitConsoleOnly()
	m.Run()
}

// box is what an AutoUpdate sees of a box: the installed release, the latest
// one, casaos-update's state and AppManagement's answer. It records what was
// read, asked and started.
type box struct {
	current  string
	latest   model.Version
	unit     string // what systemctl is-active prints
	busy     bool
	startErr error
	onStart  func()

	started   []string
	commands  [][]string
	fetched   int // version.json read past the cache
	cached    int // version.json read through the cache
	askedApps int
}

func (b *box) GetCasaosVersion() model.Version   { b.cached++; return b.latest }
func (b *box) FetchCasaosVersion() model.Version { b.fetched++; return b.latest }

// newBox is a box at 03:30 in Paris, v0.5.7 installed, v0.5.8 out for three
// days, nothing running: with autoupdate.json on and 03:00 to 05:00, a check
// starts v0.5.8.
func newBox(t *testing.T) (*AutoUpdate, *box) {
	t.Helper()
	b := &box{
		current: "0.5.7",
		latest:  model.Version{Version: "v0.5.8", PublishedAt: "2026-09-21T10:00:00Z"},
		unit:    "inactive\n",
	}
	a := New(t.TempDir())
	a.Now = func() time.Time { return local("2026-09-24 03:30") }
	a.Current = func() string { return b.current }
	a.Releases = b
	a.Start = func(version string) error {
		b.started = append(b.started, version)
		if b.onStart != nil {
			b.onStart()
		}
		return b.startErr
	}
	a.Command = func(name string, args ...string) ([]byte, error) {
		b.commands = append(b.commands, append([]string{name}, args...))
		if strings.TrimSpace(b.unit) != "active" {
			return []byte(b.unit), errors.New("exit status 3") // as systemctl is-active does
		}
		return []byte(b.unit), nil
	}
	a.AppsBusy = func(context.Context) bool {
		b.askedApps++
		return b.busy
	}
	return a, b
}

func saveState(t *testing.T, a *AutoUpdate, s State) {
	t.Helper()
	if err := a.save(s); err != nil {
		t.Fatal(err)
	}
}

func stateFile(t *testing.T, a *AutoUpdate) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(a.Root, StateFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(data)
}

func on() State { return State{Enabled: true, WindowStart: "03:00", WindowEnd: "05:00"} }

func running(version string) *Last {
	return &Last{Version: version, StartedAt: time.Date(2026, 9, 24, 1, 5, 0, 0, time.UTC), Result: resultRunning}
}

func sameLast(a, b *Last) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Version == b.Version && a.StartedAt.Equal(b.StartedAt) && a.Result == b.Result
}

func TestACheckStartsTheButtonsUpdateWhenEverythingHolds(t *testing.T) {
	a, b := newBox(t)
	saveState(t, a, on())
	var atStart *Last
	b.onStart = func() { atStart = a.load().Last }

	a.Check(context.Background())

	if !reflect.DeepEqual(b.started, []string{"v0.5.8"}) {
		t.Fatalf("started %v, want the button's update to v0.5.8", b.started)
	}
	want := &Last{Version: "v0.5.8", StartedAt: time.Date(2026, 9, 24, 1, 30, 0, 0, time.UTC), Result: resultRunning}
	if !sameLast(atStart, want) {
		t.Fatalf("autoupdate.json when the update started: last = %+v, want %+v", atStart, want)
	}
	if got := a.load().Last; !sameLast(got, want) {
		t.Fatalf("last = %+v, want %+v", got, want)
	}
	if b.fetched != 1 || b.cached != 0 {
		t.Fatalf("version.json read %d times fresh and %d through the cache, want once fresh", b.fetched, b.cached)
	}
	// The update itself is the button's: this package only asks systemd.
	if want := [][]string{{"systemctl", "is-active", "casaos-update"}}; !reflect.DeepEqual(b.commands, want) {
		t.Fatalf("commands %v, want %v", b.commands, want)
	}
	if b.askedApps != 1 {
		t.Fatalf("AppManagement asked %d times, want once", b.askedApps)
	}
}

func TestAStartThatFailsIsFailedAtOnce(t *testing.T) {
	a, b := newBox(t)
	saveState(t, a, on())
	b.startErr = errors.New("start detached update: exit status 1")

	a.Check(context.Background())

	s := a.load()
	if s.Last == nil || s.Last.Result != resultFailed || s.Last.Version != "v0.5.8" {
		t.Fatalf("last = %+v, want v0.5.8 failed", s.Last)
	}
	if s.Failures == nil || *s.Failures != (Failures{Version: "v0.5.8", Count: 1}) {
		t.Fatalf("failures = %+v, want v0.5.8 once", s.Failures)
	}
}

func TestNothingIsReadOrAskedWhenOffOrOutsideTheWindow(t *testing.T) {
	attempted := on()
	attempted.Last = &Last{Version: "v0.5.8", StartedAt: time.Date(2026, 9, 24, 1, 5, 0, 0, time.UTC), Result: resultFailed}
	for _, tc := range []struct {
		name  string
		state *State
		now   string
	}{
		{"no autoupdate.json", nil, "2026-09-24 03:30"},
		{"turned off", &State{WindowStart: "03:00", WindowEnd: "05:00"}, "2026-09-24 03:30"},
		{"outside the window", func() *State { s := on(); return &s }(), "2026-09-24 12:00"},
		{"attempted tonight", &attempted, "2026-09-24 03:30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newBox(t)
			if tc.state != nil {
				saveState(t, a, *tc.state)
			}
			a.Now = func() time.Time { return local(tc.now) }
			before := stateFile(t, a)

			a.Check(context.Background())

			if b.fetched+b.cached != 0 || len(b.commands) != 0 || b.askedApps != 0 || len(b.started) != 0 {
				t.Fatalf("read version.json %d times, ran %v, asked AppManagement %d times, started %v: want nothing", b.fetched+b.cached, b.commands, b.askedApps, b.started)
			}
			if after := stateFile(t, a); after != before {
				t.Fatalf("autoupdate.json = %s, want it untouched", after)
			}
		})
	}
}

func TestSystemdAndAppManagementAreAskedOnlyWhenEverythingElseHolds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*box)
	}{
		{"up to date", func(b *box) { b.current = "0.5.8" }},
		{"too recent", func(b *box) { b.latest.PublishedAt = "2026-09-23T10:00:00Z" }},
		{"no published_at", func(b *box) { b.latest.PublishedAt = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newBox(t)
			saveState(t, a, on())
			tc.change(b)

			a.Check(context.Background())

			if len(b.commands) != 0 || b.askedApps != 0 || len(b.started) != 0 {
				t.Fatalf("ran %v, asked AppManagement %d times, started %v: want nothing", b.commands, b.askedApps, b.started)
			}
		})
	}
}

func TestARunningUpdateOrAnAppOperationHoldsTheStart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*box)
	}{
		{"casaos-update is active", func(b *box) { b.unit = "active\n" }},
		{"casaos-update is starting", func(b *box) { b.unit = "activating\n" }},
		{"systemctl did not answer", func(b *box) { b.unit = "" }},
		{"AppManagement is busy or silent", func(b *box) { b.busy = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newBox(t)
			saveState(t, a, on())
			tc.change(b)
			before := stateFile(t, a)

			a.Check(context.Background())

			if len(b.started) != 0 {
				t.Fatalf("started %v, want nothing", b.started)
			}
			if after := stateFile(t, a); after != before {
				t.Fatalf("autoupdate.json = %s, want it untouched: the next check tries again", after)
			}
		})
	}
}

func TestSettlingTheLastAttempt(t *testing.T) {
	cases := []struct {
		name         string
		current      string
		unit         string
		failures     *Failures
		wantResult   string
		wantFailures *Failures
	}{
		{"the release is installed: succeeded, failures cleared", "0.5.8", "inactive\n", &Failures{Version: "v0.5.8", Count: 1}, resultSucceeded, nil},
		{"installed while the unit still runs: still running, it may yet put it back", "0.5.8", "active\n", nil, resultRunning, nil},
		{"a newer release than attempted is installed: succeeded", "0.5.9", "inactive\n", &Failures{Version: "v0.5.8", Count: 1}, resultSucceeded, nil},
		{"an older release is installed: failed", "0.5.6", "inactive\n", nil, resultFailed, &Failures{Version: "v0.5.8", Count: 1}},
		{"still running", "0.5.7", "active\n", nil, resultRunning, nil},
		{"still starting", "0.5.7", "activating\n", nil, resultRunning, nil},
		{"systemctl did not answer: still running", "0.5.7", "", nil, resultRunning, nil},
		{"over without the release: failed once", "0.5.7", "inactive\n", nil, resultFailed, &Failures{Version: "v0.5.8", Count: 1}},
		{"the unit failed", "0.5.7", "failed\n", nil, resultFailed, &Failures{Version: "v0.5.8", Count: 1}},
		{"a second failure of the same release", "0.5.7", "inactive\n", &Failures{Version: "v0.5.8", Count: 1}, resultFailed, &Failures{Version: "v0.5.8", Count: 2}},
		{"a newer release starts its own count", "0.5.7", "inactive\n", &Failures{Version: "v0.5.6", Count: 2}, resultFailed, &Failures{Version: "v0.5.8", Count: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newBox(t)
			s := on()
			s.Last, s.Failures = running("v0.5.8"), tc.failures
			saveState(t, a, s)
			b.current, b.unit = tc.current, tc.unit
			before := stateFile(t, a)

			a.settle()

			got := a.load()
			if got.Last == nil || got.Last.Result != tc.wantResult || !got.Last.StartedAt.Equal(s.Last.StartedAt) {
				t.Fatalf("last = %+v, want v0.5.8 %s", got.Last, tc.wantResult)
			}
			if !reflect.DeepEqual(got.Failures, tc.wantFailures) {
				t.Fatalf("failures = %+v, want %+v", got.Failures, tc.wantFailures)
			}
			if tc.wantResult == resultRunning && stateFile(t, a) != before {
				t.Fatal("autoupdate.json was rewritten while nothing changed")
			}
		})
	}
}

// A check settles the last attempt before anything else: an update that ended
// without restarting the core is not left running until the core next starts.
// Settled, tonight's attempt still counts for tonight.
func TestACheckSettlesTheLastAttemptFirst(t *testing.T) {
	for _, tc := range []struct {
		name         string
		current      string
		failures     *Failures
		wantResult   string
		wantFailures *Failures
	}{
		{"over without the release: failed", "0.5.7", nil, resultFailed, &Failures{Version: "v0.5.8", Count: 1}},
		{"the release is installed: succeeded, failures cleared", "0.5.8", &Failures{Version: "v0.5.8", Count: 1}, resultSucceeded, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newBox(t)
			s := on()
			s.Last, s.Failures = running("v0.5.8"), tc.failures // started tonight at 03:05
			saveState(t, a, s)
			b.current = tc.current
			a.Now = func() time.Time { return local("2026-09-24 04:05") }

			a.Check(context.Background())

			got := a.load()
			if got.Last == nil || got.Last.Version != "v0.5.8" || got.Last.Result != tc.wantResult || !got.Last.StartedAt.Equal(s.Last.StartedAt) {
				t.Fatalf("last = %+v, want v0.5.8 %s", got.Last, tc.wantResult)
			}
			if !reflect.DeepEqual(got.Failures, tc.wantFailures) {
				t.Fatalf("failures = %+v, want %+v", got.Failures, tc.wantFailures)
			}
			if len(b.started) != 0 {
				t.Fatalf("started %v, want nothing: tonight's attempt is made", b.started)
			}
		})
	}
}

func TestASecondFailurePausesThatReleaseOnly(t *testing.T) {
	a, b := newBox(t)
	s := on()
	s.Last, s.Failures = running("v0.5.8"), &Failures{Version: "v0.5.8", Count: 1}
	saveState(t, a, s)

	a.settle()

	if status := a.Status(); status.State != statePaused || status.Next != nil || status.Last == nil || status.Last.Result != resultFailed {
		t.Fatalf("Status() = %+v, want paused on v0.5.8's failure", status)
	}
	a.Check(context.Background())
	if len(b.started) != 0 {
		t.Fatalf("started %v while paused", b.started)
	}

	b.latest.Version = "v0.5.9"
	a.Now = func() time.Time { return local("2026-09-25 03:30") }
	a.Check(context.Background())
	if !reflect.DeepEqual(b.started, []string{"v0.5.9"}) {
		t.Fatalf("started %v, want the newer v0.5.9", b.started)
	}
}

func TestNothingToSettle(t *testing.T) {
	a, b := newBox(t)
	s := on()
	s.Last = &Last{Version: "v0.5.8", StartedAt: time.Date(2026, 9, 24, 1, 5, 0, 0, time.UTC), Result: resultSucceeded}
	saveState(t, a, s)
	before := stateFile(t, a)

	a.settle()

	if len(b.commands) != 0 || stateFile(t, a) != before {
		t.Fatalf("ran %v, autoupdate.json %s: want nothing done", b.commands, stateFile(t, a))
	}
}

// The alerts hear how each attempt ended: succeeded, failed, or paused on its
// second failure; nothing while it runs.
func TestTheAlertsHearHowAnAttemptEnded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		current  string
		unit     string
		failures *Failures
		startErr error
		want     []string
	}{
		{"succeeded", "0.5.8", "inactive\n", nil, nil, []string{"succeeded v0.5.8"}},
		{"failed", "0.5.7", "inactive\n", nil, nil, []string{"failed v0.5.8"}},
		{"paused on the second failure", "0.5.7", "failed\n", &Failures{Version: "v0.5.8", Count: 1}, nil, []string{"paused v0.5.8"}},
		{"still running", "0.5.7", "active\n", nil, nil, nil},
		{"a start that fails", "0.5.7", "inactive\n", nil, errors.New("exit status 1"), []string{"failed v0.5.8"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newBox(t)
			var heard []string
			a.Notify = func(result, version string) { heard = append(heard, result+" "+version) }
			s := on()
			b.current, b.unit, b.startErr = tc.current, tc.unit, tc.startErr
			if tc.startErr != nil {
				saveState(t, a, s)
				a.Check(context.Background())
			} else {
				s.Last, s.Failures = running("v0.5.8"), tc.failures
				saveState(t, a, s)
				a.settle()
			}

			if !reflect.DeepEqual(heard, tc.want) {
				t.Fatalf("the alerts heard %q, want %q", heard, tc.want)
			}
		})
	}
}

func TestTheStatusReadsTheCacheAndOnlyWhenOn(t *testing.T) {
	a, b := newBox(t)
	want := Status{Window: Window{Start: "03:00", End: "05:00"}, State: stateOff}
	if got := a.Status(); !reflect.DeepEqual(got, want) || b.cached+b.fetched != 0 {
		t.Fatalf("Status() = %+v after %d reads of version.json, want %+v and none", got, b.cached+b.fetched, want)
	}

	saveState(t, a, on())
	got := a.Status()
	if got.State != stateWaiting || got.Next == nil || got.Next.Version != "v0.5.8" || !got.Next.NotBefore.Equal(local("2026-09-24 03:30")) {
		t.Fatalf("Status() = %+v, want v0.5.8 due now", got)
	}
	if b.cached != 1 || b.fetched != 0 || len(b.commands) != 0 || b.askedApps != 0 {
		t.Fatalf("read version.json %d times cached, %d fresh, ran %v, asked AppManagement %d times: want the cache once, nothing else", b.cached, b.fetched, b.commands, b.askedApps)
	}
}

func TestRunSettlesAtOnceThenChecksAtTheInterval(t *testing.T) {
	t.Run("settles before the start delay", func(t *testing.T) {
		t.Setenv(startDelayEnv, "1h")
		a, b := newBox(t)
		b.current = "0.5.8"
		s := on()
		s.Last = running("v0.5.8")
		saveState(t, a, s)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { a.Run(ctx); close(done) }()

		deadline := time.Now().Add(5 * time.Second)
		for a.load().Last.Result != resultSucceeded {
			if time.Now().After(deadline) {
				t.Fatal("the running attempt was not settled at start")
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		<-done
		if b.fetched+b.cached != 0 {
			t.Fatal("a check ran before the start delay")
		}
	})

	t.Run("checks at the interval", func(t *testing.T) {
		t.Setenv(startDelayEnv, "0s")
		t.Setenv(intervalEnv, "10ms")
		a, _ := newBox(t)
		checks := make(chan struct{}, 8)
		a.Now = func() time.Time {
			select {
			case checks <- struct{}{}:
			default:
			}
			return local("2026-09-24 03:30")
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { a.Run(ctx); close(done) }()

		for i := 0; i < 3; i++ {
			select {
			case <-checks:
			case <-time.After(5 * time.Second):
				t.Fatalf("%d checks, want 3 at a 10ms interval", i)
			}
		}
		cancel()
		<-done
	})
}

func TestUnitActive(t *testing.T) {
	for output, want := range map[string]bool{
		"active\n":       true,
		"activating\n":   true,
		"deactivating\n": true,
		"":               true, // no answer: when in doubt, nothing starts
		"inactive\n":     false,
		"failed\n":       false,
	} {
		a, b := newBox(t)
		b.unit = output
		if got := a.unitActive(); got != want {
			t.Errorf("unitActive() with %q = %v, want %v", output, got, want)
		}
	}
}

// AppManagement is reached as the core reaches the other services: at the
// address in the runtime path, from loopback, with this boot's secret.
func TestAppManagementIsAskedAsAnInternalRequest(t *testing.T) {
	runtimePath := t.TempDir()
	if err := external.WriteInternalSecret(runtimePath); err != nil {
		t.Fatal(err)
	}
	secret := external.InternalAuthorization(runtimePath)
	if secret == "" {
		t.Fatal("no internal secret")
	}
	serve := func(status int, body string) {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/v2/app_management/operations" || r.Header.Get("Authorization") != secret {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(server.Close)
		if err := os.WriteFile(filepath.Join(runtimePath, external.AppManageURLFilename), []byte(server.URL), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name   string
		status int
		body   string
		busy   bool
	}{
		{"nothing runs", http.StatusOK, `{"operations":[]}`, false},
		{"a backup runs", http.StatusOK, `{"operations":[{"app":"immich","kind":"backup"}]}`, true},
		{"an answer without the list", http.StatusOK, `{}`, true},
		{"an answer that is not JSON", http.StatusOK, `ok`, true},
		{"an AppManagement without the route", http.StatusNotFound, `{"operations":[]}`, true},
		{"an error", http.StatusInternalServerError, `{"operations":[]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serve(tc.status, tc.body)
			if got := appsBusy(context.Background(), runtimePath); got != tc.busy {
				t.Fatalf("appsBusy() = %v, want %v", got, tc.busy)
			}
		})
	}

	t.Run("no address file", func(t *testing.T) {
		if !appsBusy(context.Background(), t.TempDir()) {
			t.Fatal("appsBusy() = false with no app-management.url")
		}
	})
	t.Run("nobody listening", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		server.Close()
		if err := os.WriteFile(filepath.Join(runtimePath, external.AppManageURLFilename), []byte(server.URL), 0o644); err != nil {
			t.Fatal(err)
		}
		if !appsBusy(context.Background(), runtimePath) {
			t.Fatal("appsBusy() = false with AppManagement down")
		}
	})
}
