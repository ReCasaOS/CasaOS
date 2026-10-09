package service

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// systemctlAsked is how many times the updater asked systemd about anything.
func systemctlAsked(box *aptBox) int {
	n := 0
	for _, call := range box.calls {
		if strings.HasPrefix(call, "systemctl ") {
			n++
		}
	}
	return n
}

// endedAgo writes the log of a run that ended that long before the updater's clock, as the
// terminal line says.
func (s *statusBox) endedAgo(t *testing.T, age time.Duration, terminal string, fields ...string) {
	t.Helper()
	at := s.updater.now().Add(-age).UTC().Format(time.RFC3339)
	s.write(t, ownersRun(mark(terminal, append([]string{at}, fields...)...))...)
}

func logText(t *testing.T, u *systemPackageUpdater) string {
	t.Helper()
	data, err := os.ReadFile(u.dockerLogPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDockerAlertsAreQuietWhileTheUnitRunsAndForTenMinutesAfter(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*testing.T, *statusBox)
		want bool
	}{
		"no run at all": {func(*testing.T, *statusBox) {}, false},
		"the unit runs": {func(t *testing.T, s *statusBox) {
			s.box.activeUnits[systemDockerUpdateUnit] = true
			s.write(t, ownersRun()...)
		}, true},
		"the unit runs and the log is not ours": {func(t *testing.T, s *statusBox) { s.box.activeUnits[systemDockerUpdateUnit] = true }, true},
		"the unit is gone and its last line may be on its way": {func(t *testing.T, s *statusBox) {
			s.write(t, ownersRun()...)
			s.logAge(5 * time.Second)
		}, true},
		"the unit stopped without a result":  {func(t *testing.T, s *statusBox) { s.write(t, ownersRun()...) }, false},
		"succeeded a moment ago":             {func(t *testing.T, s *statusBox) { s.endedAgo(t, time.Second, "SUCCESS") }, true},
		"succeeded 9 minutes 59 ago":         {func(t *testing.T, s *statusBox) { s.endedAgo(t, 10*time.Minute-time.Second, "SUCCESS") }, true},
		"succeeded 10 minutes ago":           {func(t *testing.T, s *statusBox) { s.endedAgo(t, 10*time.Minute, "SUCCESS") }, false},
		"succeeded an hour ago":              {func(t *testing.T, s *statusBox) { s.endedAgo(t, time.Hour, "SUCCESS") }, false},
		"restart pending 5 minutes ago":      {func(t *testing.T, s *statusBox) { s.endedAgo(t, 5*time.Minute, "RESTART_PENDING") }, true},
		"restart pending 11 minutes ago":     {func(t *testing.T, s *statusBox) { s.endedAgo(t, 11*time.Minute, "RESTART_PENDING") }, false},
		"failed 5 minutes ago":               {func(t *testing.T, s *statusBox) { s.endedAgo(t, 5*time.Minute, "FAILED", "daemon") }, true},
		"failed 11 minutes ago":              {func(t *testing.T, s *statusBox) { s.endedAgo(t, 11*time.Minute, "FAILED", "daemon") }, false},
		"a last line a minute ahead of time": {func(t *testing.T, s *statusBox) { s.endedAgo(t, -time.Minute, "SUCCESS") }, true},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStatusBox(t)
			tc.set(t, s)
			if got := s.updater.dockerMuted(); got != tc.want {
				t.Errorf("dockerMuted() = %v, want %v (the status says %#v)", got, tc.want, s.updater.dockerStatus())
			}
		})
	}
}

// The question is asked for each container event: it costs a look at systemd and the log at most
// once in a few seconds, and never waits for the updater's lock, which a slow update of packages
// can hold for minutes.
func TestDockerMutedIsCheapAndFree(t *testing.T) {
	s := newStatusBox(t)
	start := s.updater.now()
	clock := start
	s.updater.now = func() time.Time { return clock }
	s.endedAgo(t, time.Minute, "SUCCESS")

	for i := 0; i < 20; i++ {
		if !s.updater.dockerMuted() {
			t.Fatal("not muted a minute after the end of a run")
		}
	}
	if n := systemctlAsked(s.box); n != 1 {
		t.Errorf("systemd was asked %d times for 20 questions", n)
	}

	// the answer is kept for a few seconds, whatever the box says meanwhile
	clock = start.Add(dockerMuteCache - time.Second)
	s.endedAgo(t, time.Hour, "SUCCESS")
	if !s.updater.dockerMuted() || systemctlAsked(s.box) != 1 {
		t.Errorf("the answer of a moment ago was not kept (systemd asked %d times)", systemctlAsked(s.box))
	}
	// and the box is looked at again after them
	clock = start.Add(dockerMuteCache)
	if s.updater.dockerMuted() || systemctlAsked(s.box) != 2 {
		t.Errorf("the box was not looked at again (systemd asked %d times)", systemctlAsked(s.box))
	}

	// a clock that went back does not keep an answer for hours
	clock = start.Add(-2 * time.Hour)
	s.write(t, ownersRun()...)
	s.box.activeUnits[systemDockerUpdateUnit] = true
	if !s.updater.dockerMuted() {
		t.Error("the answer from the future was kept")
	}

	// and a lock held by the update of packages is not waited for
	s.updater.mu.Lock()
	defer s.updater.mu.Unlock()
	clock = clock.Add(time.Hour)
	s.box.activeUnits[systemDockerUpdateUnit] = false
	answered := make(chan bool, 1)
	go func() { answered <- s.updater.dockerMuted() }()
	select {
	case <-answered:
	case <-time.After(10 * time.Second):
		t.Fatal("dockerMuted waited for the updater's lock")
	}
}

func TestTheServiceAnswersForTheAlerts(t *testing.T) {
	s := newStatusBox(t)
	system := &systemService{packageUpdates: s.updater}
	s.endedAgo(t, time.Minute, "FAILED", "daemon")

	if !system.DockerUpdateMuted() {
		t.Error("DockerUpdateMuted() = false a minute after a run")
	}
	status, nonce := system.DockerUpdateRun()
	if nonce != scriptNonce || status.State != "failed" || status.Outcome != "failed" || status.ErrorCode != "daemon" {
		t.Errorf("DockerUpdateRun() = %#v, %q", status, nonce)
	}
	if again := system.DockerUpdateStatus(); again.State != status.State || again.CompletedAt != status.CompletedAt {
		t.Errorf("the run is not the status: %#v and %#v", status, again)
	}

	// no run in the log: no nonce, whatever the unit does
	s = newStatusBox(t)
	s.box.activeUnits[systemDockerUpdateUnit] = true
	system = &systemService{packageUpdates: s.updater}
	if status, nonce := system.DockerUpdateRun(); nonce != "" || status.State != "running" {
		t.Errorf("DockerUpdateRun() = %#v, %q", status, nonce)
	}
}

// What the notification needs of a run: its nonce, how it ended, the version that runs, and the
// containers that did not come back.
func TestTheRunCarriesWhatTheNotificationSays(t *testing.T) {
	s := newStatusBox(t)
	system := &systemService{packageUpdates: s.updater}
	s.write(t, ownersRun(
		mark("DAEMON", "29.8.0"),
		mark("NOTRETURNED", "job", "no"),
		mark("SUCCESS", "2026-08-13T01:05:00Z"),
	)...)

	status, nonce := system.DockerUpdateRun()

	if nonce != scriptNonce || status.Outcome != "success" || status.To != "29.8.0" || len(status.NotReturned) != 1 || status.NotReturned[0].Name != "job" {
		t.Fatalf("DockerUpdateRun() = %#v, %q", status, nonce)
	}
	if !dockerpkg.ValidNonce(nonce) {
		t.Errorf("nonce = %q", nonce)
	}
}

// The alerts are told that a run was queued once its first line is in the log, whatever becomes of
// the unit; a start that did not happen tells them nothing.
func TestTheAlertsAreToldOfARunWhenItsLogIsWritten(t *testing.T) {
	s := newStartBox(t, nil)
	system := &systemService{packageUpdates: s.updater}
	var told, logged int
	system.OnDockerUpdateQueued(func() {
		told++
		if dockerpkg.ParseRun(logText(t, s.updater)).Nonce != "" {
			logged++
		}
	})
	if _, err := system.StartDockerUpdate(s.planID); err != nil {
		t.Fatal(err)
	}
	if told != 1 || logged != 1 {
		t.Fatalf("told %d times, %d with the log written", told, logged)
	}

	// systemd-run refuses: the log is a whole run that ended, and the alerts hear of it too
	f := newStartBox(t, nil)
	f.updater.start = func(string, string, ...string) ([]byte, error) {
		return []byte("Failed to start"), errors.New("exit status 1")
	}
	told = 0
	system = &systemService{packageUpdates: f.updater}
	system.OnDockerUpdateQueued(func() { told++ })
	if _, err := system.StartDockerUpdate(f.planID); err == nil || told != 1 {
		t.Fatalf("error = %v, told %d times", err, told)
	}
	if run := dockerpkg.ParseRun(logText(t, f.updater)); run.Terminal != dockerpkg.TerminalFailed {
		t.Errorf("run = %#v", run)
	}

	// refused: nothing is told, nothing was written
	r := newStartBox(t, nil)
	told = 0
	system = &systemService{packageUpdates: r.updater}
	system.OnDockerUpdateQueued(func() { told++ })
	for _, planID := range []string{strings.Repeat("0", 64), "nonsense"} {
		if _, err := system.StartDockerUpdate(planID); err == nil {
			t.Fatalf("plan %q was started", planID)
		}
	}
	if told != 0 {
		t.Errorf("told %d times of a start that did not happen", told)
	}
	r.noLog(t)

	// and with no one to tell the update starts all the same
	n := newStartBox(t, nil)
	system = &systemService{packageUpdates: n.updater}
	if _, err := system.StartDockerUpdate(n.planID); err != nil {
		t.Errorf("error = %v", err)
	}
}
