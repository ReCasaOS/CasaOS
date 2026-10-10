package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// statusBox is an updater whose systemd, log and clock are the test's, and whose docker command
// fails the test: the status never asks Docker anything.
type statusBox struct {
	updater *systemPackageUpdater
	box     *aptBox
}

func newStatusBox(t *testing.T) *statusBox {
	t.Helper()
	box := ownerBox(t)
	box.activeUnits = map[string]bool{}
	updater := newTestSystemPackageUpdater(t)
	updater.command = box.command
	s := &statusBox{updater: updater, box: box}
	s.logAge(time.Hour)
	return s
}

// logAge makes the log as old as that, by the updater's clock.
func (s *statusBox) logAge(age time.Duration) { logAgeOf(s.updater, age) }

// logAgeOf is that for any updater: the test's clock is in the past, and the files are new.
func logAgeOf(u *systemPackageUpdater, age time.Duration) {
	now := u.now()
	logPath := u.dockerLogPath()
	u.stat = func(path string) (os.FileInfo, error) {
		if path == logPath {
			return &testFileInfo{modTime: now.Add(-age)}, nil
		}
		if path == "/var/run/reboot-required" {
			return nil, os.ErrNotExist
		}
		return os.Stat(path)
	}
}

func (s *statusBox) write(t *testing.T, lines ...string) {
	t.Helper()
	if err := os.WriteFile(s.updater.dockerLogPath(), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (s *statusBox) status(t *testing.T) SystemDockerUpdateStatus {
	t.Helper()
	calls := len(s.box.calls)
	status := s.updater.dockerStatus()
	for _, call := range s.box.calls[calls:] {
		// systemd is all the status asks: the unit, whose name has "docker" in it
		if !strings.HasPrefix(call, "systemctl show casaos-docker-update.service ") {
			t.Errorf("the status asked %q", call)
		}
	}
	return status
}

func mark(kind string, fields ...string) string {
	return strings.TrimSpace("CASAOS_DOCKER_UPDATE_" + kind + " " + scriptNonce + " " + strings.Join(fields, " "))
}

const (
	logQueued    = "CASAOS_DOCKER_UPDATE_QUEUED " + scriptNonce + " 2026-08-13T01:00:00Z"
	pinContainer = "containerd.io=1.7.27-1"
)

var (
	logStarted  = mark("STARTED", "2026-08-13T01:00:05Z")
	logPrevious = mark("PREVIOUS", pinContainer, "docker-ce="+debian28, "docker-ce-cli="+debian28)
)

func ownersRun(tail ...string) []string {
	return append([]string{
		logQueued, logStarted, logPrevious,
		"container: web always",
		mark("DOWNLOADED", "2026-08-13T01:00:30Z"),
		mark("INSTALLED", "2026-08-13T01:02:00Z"),
	}, tail...)
}

func TestDockerStatusWithoutARun(t *testing.T) {
	s := newStatusBox(t)
	status := s.status(t)
	want := SystemDockerUpdateStatus{Supported: true, State: "idle", NotReturned: []dockerpkg.NotReturned{}}
	if !reflect.DeepEqual(status, want) {
		t.Errorf("status = %#v, want %#v", status, want)
	}
	// ... and what the page reads
	encoded, _ := json.Marshal(status)
	if got := string(encoded); got != `{"supported":true,"state":"idle","phase":"","outcome":"","error":"","error_code":"","exit_code":null,"started_at":"","completed_at":"","from":"","to":"","not_returned":[],"rollback_command":"","log":""}` {
		t.Errorf("JSON = %s", got)
	}
}

func TestDockerStatusOfAUnitThatHasNoRunInTheLog(t *testing.T) {
	// a unit by that name that the core did not start: running, and no more is said of it
	s := newStatusBox(t)
	s.box.activeUnits[systemDockerUpdateUnit] = true
	if status := s.status(t); status.State != "running" || status.Outcome != "" || status.ErrorCode != "" {
		t.Errorf("status = %#v", status)
	}
}

func TestDockerStatusRunning(t *testing.T) {
	s := newStatusBox(t)
	s.box.activeUnits[systemDockerUpdateUnit] = true
	s.write(t, logQueued)
	if status := s.status(t); status.State != "running" || status.StartedAt != "2026-08-13T01:00:00Z" || status.From != "" || status.Error != "" {
		t.Errorf("queued: status = %#v", status)
	}
	s.write(t, ownersRun()...)
	status := s.status(t)
	if status.State != "running" || status.StartedAt != "2026-08-13T01:00:05Z" || status.From != "28.0.4" || status.To != "" || status.Outcome != "" || status.RollbackCommand != "" {
		t.Errorf("running: status = %#v", status)
	}
	if !strings.Contains(status.Log, "container: web always") {
		t.Errorf("log = %q", status.Log)
	}
}

// The page says "your apps keep running" while the packages are downloaded, and "Docker is
// restarting" once they are installed: the phase is where the markers the unit has written say it
// is, and nothing once the run is over.
func TestDockerStatusSaysWhichStepTheRunIsAt(t *testing.T) {
	s := newStatusBox(t)
	s.box.activeUnits[systemDockerUpdateUnit] = true
	for _, step := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"queued", []string{logQueued}, "preparing"},
		{"started", []string{logQueued, logStarted}, "preparing"},
		{"the snapshot is taken, the guard is on", []string{logQueued, logStarted, logPrevious, "container: web always"}, "downloading"},
		{"downloaded", []string{logQueued, logStarted, logPrevious, "container: web always", mark("DOWNLOADED", "2026-08-13T01:00:30Z")}, "installing"},
		{"installed", ownersRun(), "waiting_docker"},
		{"the daemon is back", ownersRun(mark("DAEMON", "29.8.0")), "waiting_containers"},
		{"some containers are not", ownersRun(mark("DAEMON", "29.8.0"), mark("NOTRETURNED", "db", "always")), "waiting_containers"},
	} {
		s.write(t, step.lines...)
		status := s.status(t)
		if status.State != "running" || status.Phase != step.want {
			t.Errorf("%s: state %q, phase %q, want running in %q", step.name, status.State, status.Phase, step.want)
		}
	}

	// the unit is gone and its last line may be on its way: still in the step it was in
	s.box.activeUnits[systemDockerUpdateUnit] = false
	s.write(t, ownersRun(mark("DAEMON", "29.8.0"))...)
	s.logAge(5 * time.Second)
	if status := s.status(t); status.State != "finalizing" || status.Phase != "waiting_containers" {
		t.Errorf("finalizing: state %q, phase %q", status.State, status.Phase)
	}

	// over, one way or another: no step
	s.logAge(time.Hour)
	for name, lines := range map[string][]string{
		"succeeded":       ownersRun(mark("DAEMON", "29.8.0"), mark("SUCCESS", "2026-08-13T01:05:00Z")),
		"restart pending": ownersRun(mark("DAEMON", "28.0.4"), mark("RESTART_PENDING", "2026-08-13T01:05:00Z")),
		"failed":          ownersRun(mark("FAILED", "2026-08-13T01:03:00Z", "install")),
		"lost":            ownersRun(mark("DAEMON", "29.8.0")),
	} {
		s.write(t, lines...)
		if status := s.status(t); status.Phase != "" {
			t.Errorf("%s: phase %q (state %q)", name, status.Phase, status.State)
		}
	}

	// a unit of that name that the core did not start has no step to tell
	if err := os.Remove(s.updater.dockerLogPath()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	s.box.activeUnits[systemDockerUpdateUnit] = true
	if status := s.status(t); status.State != "running" || status.Phase != "" {
		t.Errorf("a unit that is not ours: state %q, phase %q", status.State, status.Phase)
	}
}

func TestDockerStatusFinalizesBeforeItReportsNoResult(t *testing.T) {
	s := newStatusBox(t)
	s.write(t, ownersRun()...)

	// the unit is gone and the log was written a moment ago: the last line may be on its way
	s.logAge(5 * time.Second)
	if status := s.status(t); status.State != "finalizing" || status.Error != "" || status.ErrorCode != "" || status.Outcome != "" {
		t.Errorf("within the grace: status = %#v", status)
	}
	s.logAge(systemPackageResultGracePeriod + time.Second)
	status := s.status(t)
	if status.State != "failed" || status.Outcome != "failed" || status.ErrorCode != "no_result" || status.Error == "" {
		t.Errorf("after the grace: status = %#v", status)
	}
	// a rollback is offered for a run that stopped: what was there is in the log
	if !strings.HasPrefix(status.RollbackCommand, "sudo apt-get install --allow-downgrades containerd.io=1.7.27-1 docker-ce="+debian28) {
		t.Errorf("rollback = %q", status.RollbackCommand)
	}
	// and while the unit runs it is running, however old the log
	s.box.activeUnits[systemDockerUpdateUnit] = true
	if status := s.status(t); status.State != "running" {
		t.Errorf("a unit that runs: status = %#v", status)
	}
}

// A systemctl that fails (PID 1 busy, a fork that failed, ten seconds of nothing) says nothing about
// the unit. The unit is quiet in the log for minutes (the wait for the daemon, the wait for the
// containers), so a status that took the failure for "the unit is gone" would report a run that is
// going on as lost, and the page and the push stop at the first end they read.
func TestDockerStatusDoesNotCallARunLostBecauseSystemdDidNotAnswer(t *testing.T) {
	s := newStatusBox(t)
	s.write(t, ownersRun(mark("DAEMON", "29.8.0"))...)
	deaf := true
	answer := s.updater.command
	s.updater.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && deaf {
			return nil, errors.New("Failed to connect to bus: Resource temporarily unavailable")
		}
		return answer(ctx, name, args...)
	}

	// the log has been quiet for 45 seconds, past the grace for a last line, and systemd does not say
	s.logAge(45 * time.Second)
	status := s.status(t)
	if status.State != "running" || status.Phase != "waiting_containers" || status.Outcome != "" || status.ErrorCode != "" || status.Error != "" || status.RollbackCommand != "" {
		t.Errorf("systemd does not answer, the log is 45 seconds old: status = %#v", status)
	}
	if !dockerRunMutes(status, s.updater.now()) {
		t.Error("the alerts about the apps' containers are not kept quiet for a run that goes on")
	}
	s.logAge(systemDockerSilenceLimit - time.Minute)
	if status := s.status(t); status.State != "running" {
		t.Errorf("systemd does not answer, the log is %v old: status = %#v", systemDockerSilenceLimit-time.Minute, status)
	}

	// systemd does not answer for ever, and the log has been quiet for as long: the run is lost
	s.logAge(systemDockerSilenceLimit + time.Minute)
	if status := s.status(t); status.State != "failed" || status.ErrorCode != "no_result" {
		t.Errorf("systemd does not answer, the log is old: status = %#v", status)
	}

	// systemd answers that the unit is gone, and the log has been quiet: the run is lost, as ever
	deaf = false
	s.logAge(45 * time.Second)
	if status := s.status(t); status.State != "failed" || status.ErrorCode != "no_result" {
		t.Errorf("the unit is gone: status = %#v", status)
	}
	// ... and one that answers that it runs is running, as ever
	s.box.activeUnits[systemDockerUpdateUnit] = true
	if status := s.status(t); status.State != "running" {
		t.Errorf("the unit runs: status = %#v", status)
	}
}

func TestDockerStatusSucceeded(t *testing.T) {
	s := newStatusBox(t)
	s.write(t, ownersRun(
		mark("DAEMON", "29.8.0"),
		mark("NOTRETURNED", "job", "no"),
		mark("NOTRETURNED", "scratch"),
		mark("SUCCESS", "2026-08-13T01:05:00Z"),
	)...)
	status := s.status(t)
	want := SystemDockerUpdateStatus{
		Supported: true, State: "succeeded", Outcome: "success",
		StartedAt: "2026-08-13T01:00:05Z", CompletedAt: "2026-08-13T01:05:00Z",
		From: "28.0.4", To: "29.8.0",
		NotReturned: []dockerpkg.NotReturned{{Name: "job", RestartPolicy: "no"}, {Name: "scratch"}},
		Log:         status.Log,
	}
	if !reflect.DeepEqual(status, want) {
		t.Errorf("status = %#v\nwant %#v", status, want)
	}
	// finished: the unit may still be on its way out, the log has the last word
	s.box.activeUnits[systemDockerUpdateUnit] = true
	if got := s.status(t); got.State != "succeeded" {
		t.Errorf("with the unit still active: %#v", got)
	}
}

func TestDockerStatusRestartPendingKeepsTheOldDaemonToItself(t *testing.T) {
	s := newStatusBox(t)
	s.write(t, ownersRun(mark("DAEMON", "28.0.4"), mark("RESTART_PENDING", "2026-08-13T01:05:00Z"))...)
	status := s.status(t)
	// the daemon that runs is the old one: it is not the version the update went to
	if status.State != "succeeded" || status.Outcome != "restart_pending" || status.From != "28.0.4" || status.To != "" || status.RollbackCommand != "" || status.ErrorCode != "" {
		t.Errorf("status = %#v", status)
	}
}

func TestDockerStatusOfAPlanWithoutDockerCE(t *testing.T) {
	s := newStatusBox(t)
	s.write(t, logQueued, logStarted, mark("PREVIOUS", pinContainer), mark("DAEMON", "28.0.4"), mark("SUCCESS", "2026-08-13T01:05:00Z"))
	if status := s.status(t); status.From != "28.0.4" || status.To != "28.0.4" || status.Outcome != "success" {
		t.Errorf("status = %#v: the engine did not move, and that is the one that runs", status)
	}
}

func TestDockerStatusFailed(t *testing.T) {
	for reason, message := range map[string]string{
		"guard":    "nothing was installed",
		"download": "Nothing was changed",
		"install":  "could not be installed",
		"daemon":   "did not come back",
		"start":    "could not be started",
	} {
		s := newStatusBox(t)
		s.write(t, ownersRun(mark("FAILED", "2026-08-13T01:03:00Z", reason))...)
		status := s.status(t)
		if status.State != "failed" || status.Outcome != "failed" || status.ErrorCode != reason || !strings.Contains(status.Error, message) || status.CompletedAt != "2026-08-13T01:03:00Z" {
			t.Errorf("%s: status = %#v", reason, status)
		}
		// the owner's way back: a fixed shape, from the pins that were installed
		if want := "sudo apt-get install --allow-downgrades containerd.io=1.7.27-1 docker-ce=" + debian28 + " docker-ce-cli=" + debian28; status.RollbackCommand != want {
			t.Errorf("%s: rollback = %q, want %q", reason, status.RollbackCommand, want)
		}
	}
}

func TestDockerStatusOffersARollbackOnlyForAFailedRunAndOnlyFromValidPins(t *testing.T) {
	s := newStatusBox(t)
	// no PREVIOUS: nothing to go back to
	s.write(t, logQueued, logStarted, mark("FAILED", "2026-08-13T01:03:00Z", "guard"))
	if status := s.status(t); status.State != "failed" || status.RollbackCommand != "" {
		t.Errorf("no previous: status = %#v", status)
	}
	// a pin that is not a pin of the engine's is left out, and a hostile one is not a command
	s.write(t, logQueued, logStarted,
		mark("PREVIOUS", "docker-ce="+debian28, "docker-ce-cli=1;reboot", "libc6=2.35", "containerd.io=$(reboot)"),
		mark("FAILED", "2026-08-13T01:03:00Z", "install"))
	status := s.status(t)
	if want := "sudo apt-get install --allow-downgrades docker-ce=" + debian28; status.RollbackCommand != want {
		t.Errorf("rollback = %q, want %q", status.RollbackCommand, want)
	}
	// ... and none of the pins is valid: none is offered
	s.write(t, logQueued, logStarted, mark("PREVIOUS", "docker-ce=1;reboot", "x=1"), mark("FAILED", "2026-08-13T01:03:00Z", "install"))
	if status := s.status(t); status.RollbackCommand != "" {
		t.Errorf("rollback = %q", status.RollbackCommand)
	}
	// a run that went well offers none
	s.write(t, ownersRun(mark("DAEMON", "29.8.0"), mark("SUCCESS", "2026-08-13T01:05:00Z"))...)
	if status := s.status(t); status.RollbackCommand != "" {
		t.Errorf("rollback = %q after a success", status.RollbackCommand)
	}
}

func TestDockerStatusIgnoresWhatIsNotTheUnitsWord(t *testing.T) {
	forged := "CASAOS_DOCKER_UPDATE_SUCCESS 0000000000000000000000000000000f 2026-08-13T01:05:00Z"
	cases := map[string][]string{
		"another nonce":                    append(ownersRun(), forged),
		"a nonce that is not one":          append(ownersRun(), "CASAOS_DOCKER_UPDATE_SUCCESS abc 2026-08-13T01:05:00Z"),
		"no nonce":                         append(ownersRun(), "CASAOS_DOCKER_UPDATE_SUCCESS 2026-08-13T01:05:00Z"),
		"in the middle of a line":          append(ownersRun(), "dpkg: "+mark("SUCCESS", "2026-08-13T01:05:00Z")),
		"indented":                         append(ownersRun(), " "+mark("SUCCESS", "2026-08-13T01:05:00Z")),
		"another case":                     append(ownersRun(), strings.ToLower(mark("SUCCESS", "2026-08-13T01:05:00Z"))),
		"a kind it does not know":          append(ownersRun(), mark("DONE", "2026-08-13T01:05:00Z")),
		"a time that is not one":           append(ownersRun(), mark("SUCCESS", "yesterday")),
		"a failure with a reason it lacks": append(ownersRun(), mark("FAILED", "2026-08-13T01:05:00Z", "oops")),
		"a container named like a marker":  append(ownersRun(), "container: CASAOS_DOCKER_UPDATE_SUCCESS always"),
		"apt output":                       append(ownersRun(), "Setting up docker-ce ("+debian29+") ...", "CASAOS_DOCKER_UPDATE_SUCCESS"),
	}
	for name, lines := range cases {
		s := newStatusBox(t)
		s.box.activeUnits[systemDockerUpdateUnit] = true
		s.write(t, lines...)
		if status := s.status(t); status.State != "running" || status.Outcome != "" || status.CompletedAt != "" {
			t.Errorf("%s: status = %#v, want the run still running", name, status)
		}
	}

	// the nonce is the first line's and only that: a log that does not start with the core's line is no run
	s := newStatusBox(t)
	s.write(t, ownersRun(mark("SUCCESS", "2026-08-13T01:05:00Z"))[1:]...)
	if status := s.status(t); status.State != "idle" {
		t.Errorf("a log without the core's first line: status = %#v", status)
	}
	s.write(t, "CASAOS_DOCKER_UPDATE_QUEUED 0123 2026-08-13T01:00:00Z", mark("SUCCESS", "2026-08-13T01:05:00Z"))
	if status := s.status(t); status.State != "idle" {
		t.Errorf("a first line with a nonce that is not one: status = %#v", status)
	}
	// a QUEUED line in the middle does not give the run another nonce
	s.write(t, append(ownersRun(), "CASAOS_DOCKER_UPDATE_QUEUED 0000000000000000000000000000000f 2026-08-13T01:00:00Z", "CASAOS_DOCKER_UPDATE_SUCCESS 0000000000000000000000000000000f 2026-08-13T01:05:00Z")...)
	if status := s.status(t); status.State == "succeeded" {
		t.Errorf("a second QUEUED line changed the run: %#v", status)
	}
}

func TestDockerStatusKeepsTheLastTerminalMarker(t *testing.T) {
	s := newStatusBox(t)
	s.write(t, ownersRun(mark("DAEMON", "29.8.0"), mark("SUCCESS", "2026-08-13T01:05:00Z"), mark("FAILED", "2026-08-13T01:06:00Z", "daemon"))...)
	if status := s.status(t); status.State != "failed" || status.ErrorCode != "daemon" {
		t.Errorf("status = %#v", status)
	}
}

func TestDockerStatusDoesNotShowWhatIsNotAContainer(t *testing.T) {
	s := newStatusBox(t)
	s.write(t, ownersRun(
		mark("DAEMON", "29.8.0"),
		mark("NOTRETURNED", "good", "always"),
		mark("NOTRETURNED", "bad;name", "always"),
		mark("NOTRETURNED", "x", "Always"),
		mark("NOTRETURNED", "x", "always", "extra"),
		mark("NOTRETURNED"),
		mark("SUCCESS", "2026-08-13T01:05:00Z"),
	)...)
	if got := s.status(t).NotReturned; !reflect.DeepEqual(got, []dockerpkg.NotReturned{{Name: "good", RestartPolicy: "always"}}) {
		t.Errorf("not returned = %#v", got)
	}
}

func TestDockerStatusTakesNoLockAndIsTheSameAfterARestart(t *testing.T) {
	s := newStatusBox(t)
	// a core that has just started: another updater, with the same files, and nothing in memory
	fresh := newTestSystemPackageUpdater(t)
	fresh.command = s.box.command
	logAgeOf(fresh, time.Hour)
	s.write(t, ownersRun(mark("DAEMON", "29.8.0"), mark("SUCCESS", "2026-08-13T01:05:00Z"))...)

	// the lock is held by a check or a start that is simulating: the status answers all the same
	s.updater.mu.Lock()
	done := make(chan SystemDockerUpdateStatus, 1)
	go func() { done <- s.updater.dockerStatus() }()
	var held SystemDockerUpdateStatus
	select {
	case held = <-done:
	case <-time.After(5 * time.Second):
		s.updater.mu.Unlock()
		t.Fatal("the status waits for the updater's lock")
	}
	s.updater.mu.Unlock()

	if again := fresh.dockerStatus(); !reflect.DeepEqual(again, held) || again.State != "succeeded" {
		t.Errorf("after a restart: %#v, want %#v", again, held)
	}
}

func TestDockerStatusAnswersWhateverTheCheckIsDoing(t *testing.T) {
	// the lock is the System packages check's: apt-get update takes minutes on a small box
	s := newStatusBox(t)
	release := make(chan struct{})
	inCheck := make(chan struct{})
	s.updater.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasSuffix(name, "apt-get") && len(args) > 0 && args[0] == "update" {
			close(inCheck)
			<-release
		}
		return s.box.command(ctx, name, args...)
	}
	checked := make(chan struct{})
	go func() { _, _ = s.updater.check(); close(checked) }()
	<-inCheck
	got := make(chan SystemDockerUpdateStatus, 1)
	go func() { got <- s.updater.dockerStatus() }()
	select {
	case status := <-got:
		if status.State != "idle" {
			t.Errorf("status = %#v", status)
		}
	case <-time.After(5 * time.Second):
		t.Error("the status waits for the check")
	}
	close(release)
	<-checked
}

func TestDockerStatusReadsTheEndOfALongLogAndKeepsTheRunsFirstLine(t *testing.T) {
	s := newStatusBox(t)
	noise := strings.Repeat("Unpacking docker-ce (5:29.8.0-1~debian.11~bullseye) over (5:28.0.4-1~debian.11~bullseye) ...\n", 4000)
	lines := strings.Join(ownersRun(), "\n") + "\n" + noise + strings.Join([]string{mark("DAEMON", "29.8.0"), mark("SUCCESS", "2026-08-13T01:05:00Z")}, "\n") + "\n"
	if err := os.WriteFile(s.updater.dockerLogPath(), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(lines) < 2*systemPackageLogMaxBytes {
		t.Fatalf("the log is %d bytes", len(lines))
	}
	status := s.status(t)
	if status.State != "succeeded" || status.Outcome != "success" || status.To != "29.8.0" {
		t.Errorf("status = %#v", status)
	}
	if len(status.Log) > systemPackageLogMaxBytes || !strings.HasPrefix(status.Log, "[Earlier log output omitted]\n") || !strings.HasSuffix(status.Log, mark("SUCCESS", "2026-08-13T01:05:00Z")+"\n") {
		t.Errorf("log: %d bytes, starts %q, ends %q", len(status.Log), status.Log[:40], status.Log[len(status.Log)-80:])
	}

	// the first line of a log that was cut is the core's, not whatever is at the start of the tail
	forged := strings.Replace(lines, "CASAOS_DOCKER_UPDATE_QUEUED "+scriptNonce, "CASAOS_DOCKER_UPDATE_QUEUED 0000000000000000000000000000000f", 1)
	if err := os.WriteFile(s.updater.dockerLogPath(), []byte(forged), 0o644); err != nil {
		t.Fatal(err)
	}
	if status := s.status(t); status.State == "succeeded" {
		t.Errorf("a log whose first line has another nonce succeeded: %#v", status)
	}
}

// writeRaw puts text in the log as it is.
func (s *statusBox) writeRaw(t *testing.T, text string) {
	t.Helper()
	if err := os.WriteFile(s.updater.dockerLogPath(), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lines is the lines, each ended by a new line, as the log holds them.
func lines(list ...string) string { return strings.Join(list, "\n") + "\n" }

// aptNoise is what apt and dpkg print while they unpack and configure: about 3.4 MiB of it, far more
// than the 128 KiB of the log that the page is shown.
var aptNoise = strings.Repeat("Setting up docker-ce (5:29.8.0-1~debian.11~bullseye) ...\n", 60000)

// The markers the page and the rollback rest on (the snapshot of what was installed, the end of the
// download) are written before the install, and what apt prints comes after them: a noisy install
// must not lose them, or a failed install would have no command to put the packages back, no version
// to come from, and a run that is installing would be called preparing.
func TestDockerStatusKeepsTheMarkersOfANoisyInstall(t *testing.T) {
	if len(aptNoise) < 3<<20 {
		t.Fatalf("the noise is %d bytes", len(aptNoise))
	}
	beforeInstall := lines(logQueued, logStarted, logPrevious, "container: web always", mark("DOWNLOADED", "2026-08-13T01:00:30Z"))
	rollback := "sudo apt-get install --allow-downgrades " + pinContainer + " docker-ce=" + debian28 + " docker-ce-cli=" + debian28

	for _, c := range []struct {
		name   string
		active bool
		log    string
		check  func(SystemDockerUpdateStatus) string
	}{
		{"an install that failed", false, beforeInstall + aptNoise + lines(mark("FAILED", "2026-08-13T01:03:00Z", "install")), func(st SystemDockerUpdateStatus) string {
			if st.State != "failed" || st.ErrorCode != "install" || st.From != "28.0.4" || st.RollbackCommand != rollback || st.Phase != "" {
				return "want failed at install, from 28.0.4, with the rollback command"
			}
			return ""
		}},
		{"an install that was lost", false, beforeInstall + aptNoise, func(st SystemDockerUpdateStatus) string {
			if st.State != "failed" || st.ErrorCode != "no_result" || st.From != "28.0.4" || st.RollbackCommand != rollback {
				return "want failed with no result, from 28.0.4, with the rollback command"
			}
			return ""
		}},
		{"an install that goes on", true, beforeInstall + aptNoise, func(st SystemDockerUpdateStatus) string {
			if st.State != "running" || st.Phase != "installing" || st.From != "28.0.4" || st.RollbackCommand != "" {
				return "want running, installing, from 28.0.4"
			}
			return ""
		}},
		{"a wait for the daemon", true, beforeInstall + aptNoise + lines(mark("INSTALLED", "2026-08-13T01:02:00Z")) + aptNoise, func(st SystemDockerUpdateStatus) string {
			if st.State != "running" || st.Phase != "waiting_docker" || st.From != "28.0.4" {
				return "want running, waiting_docker, from 28.0.4"
			}
			return ""
		}},
		{"a run that succeeded, with noise everywhere", false, beforeInstall + aptNoise + lines(mark("INSTALLED", "2026-08-13T01:02:00Z")) + aptNoise +
			lines(mark("DAEMON", "29.8.0"), mark("NOTRETURNED", "db", "always")) + aptNoise + lines(mark("SUCCESS", "2026-08-13T01:05:00Z")), func(st SystemDockerUpdateStatus) string {
			if st.State != "succeeded" || st.Outcome != "success" || st.From != "28.0.4" || st.To != "29.8.0" || st.RollbackCommand != "" ||
				!reflect.DeepEqual(st.NotReturned, []dockerpkg.NotReturned{{Name: "db", RestartPolicy: "always"}}) {
				return "want succeeded, from 28.0.4 to 29.8.0, with db not returned"
			}
			return ""
		}},
	} {
		s := newStatusBox(t)
		s.box.activeUnits[systemDockerUpdateUnit] = c.active
		s.writeRaw(t, c.log)
		status := s.status(t)
		if why := c.check(status); why != "" {
			t.Errorf("%s: %s: status = %#v", c.name, why, status)
		}
		// the log that is shown is still the end of it
		if len(status.Log) > systemPackageLogMaxBytes {
			t.Errorf("%s: the log is %d bytes", c.name, len(status.Log))
		}
	}
}

// The scan has a bound (16 MiB). A log that goes beyond it is read by its start and by its end, the
// two places the unit writes its markers.
func TestDockerStatusReadsAHugeLogByItsStartAndItsEnd(t *testing.T) {
	s := newStatusBox(t)
	noise := strings.Repeat(strings.Repeat("x", 1023)+"\n", 20<<10) // 20 MiB
	log := lines(logQueued, logStarted, logPrevious, "container: web always", mark("DOWNLOADED", "2026-08-13T01:00:30Z")) + noise +
		lines(mark("INSTALLED", "2026-08-13T01:02:00Z"), mark("FAILED", "2026-08-13T01:03:00Z", "daemon"))
	if int64(len(log)) <= systemDockerMarkerScanBytes {
		t.Fatalf("the log is %d bytes, not beyond the bound of %d", len(log), systemDockerMarkerScanBytes)
	}
	s.writeRaw(t, log)
	status := s.status(t)
	want := "sudo apt-get install --allow-downgrades " + pinContainer + " docker-ce=" + debian28 + " docker-ce-cli=" + debian28
	if status.State != "failed" || status.ErrorCode != "daemon" || status.From != "28.0.4" || status.RollbackCommand != want {
		t.Errorf("status = %#v", status)
	}
}

// A marker is a short line. A line that goes on for kilobytes is apt's, or worse, and is neither read
// whole nor allowed to hide what comes after it.
func TestDockerStatusDoesNotReadALineLongerThanAMarkerCanBe(t *testing.T) {
	s := newStatusBox(t)
	end := lines(mark("DAEMON", "29.8.0"))

	// a marker with room for a lot of nothing after it is not one
	s.writeRaw(t, lines(ownersRun()...)+end+mark("SUCCESS", "2026-08-13T01:05:00Z")+strings.Repeat(" ", 3*systemDockerMarkerLineBytes)+"\n")
	if status := s.status(t); status.State == "succeeded" {
		t.Errorf("a marker longer than a marker can be was read: %#v", status)
	}

	// megabytes with no new line, then what the unit wrote
	s.writeRaw(t, lines(ownersRun()...)+strings.Repeat("y", 3<<20)+"\n"+end+lines(mark("SUCCESS", "2026-08-13T01:05:00Z")))
	if status := s.status(t); status.State != "succeeded" || status.To != "29.8.0" {
		t.Errorf("a long line hid the end of the run: %#v", status)
	}

	// ... and the end of one that is the last thing in the log
	s.writeRaw(t, lines(ownersRun()...)+end+lines(mark("SUCCESS", "2026-08-13T01:05:00Z"))+strings.Repeat("z", 3<<20))
	if status := s.status(t); status.State != "succeeded" {
		t.Errorf("a long last line hid the end of the run: %#v", status)
	}

	// what a long line goes on with is the line's, even if it looks like a marker where it starts
	s.writeRaw(t, lines(ownersRun()...)+end+strings.Repeat("a", systemDockerMarkerLineBytes)+lines(mark("SUCCESS", "2026-08-13T01:05:00Z")))
	if status := s.status(t); status.State == "succeeded" {
		t.Errorf("the rest of a long line was read as a line: %#v", status)
	}
}

// The nonce is the first line's, and the first line is the log's, whatever it says: a QUEUED line
// further down does not become the first by skipping over the lines that are not markers.
func TestDockerStatusTakesTheFirstLineOfTheLogAndNotTheFirstMarker(t *testing.T) {
	s := newStatusBox(t)
	s.write(t, append([]string{"not the core's line"}, ownersRun(mark("DAEMON", "29.8.0"), mark("SUCCESS", "2026-08-13T01:05:00Z"))...)...)
	if status := s.status(t); status.State != "idle" {
		t.Errorf("a log whose first line is something else: status = %#v", status)
	}
}

func TestReadDockerMarkersReadsWholeLinesOnly(t *testing.T) {
	old := systemDockerMarkerScanBytes
	systemDockerMarkerScanBytes = 2048
	t.Cleanup(func() { systemDockerMarkerScanBytes = old })
	half := int(systemDockerMarkerScanBytes / 2)

	// exactly n bytes of lines that are not markers
	filler := func(n int) string { return strings.Repeat("\n", n%2) + strings.Repeat("n\n", n/2) }
	final := lines(mark("SUCCESS", "2026-08-13T01:05:00Z"))
	daemon := lines(mark("DAEMON", "29.8.0"))
	previous := lines(mark("PREVIOUS", pinContainer))
	start := lines(logQueued, logStarted)

	// The window of the start ends in the middle of PREVIOUS, after "containerd.io=1.7": a pin that
	// would pass for one, with a version that is not the package's.
	cut := len(previous) - len(".27-1\n")
	head := start + filler(half-cut-len(start)) + previous
	if !strings.HasSuffix(head[:half], "containerd.io=1.7") {
		t.Fatalf("the window of the start ends in %q", head[:half][half-40:])
	}
	middle := filler(1500)

	for _, c := range []struct {
		name string
		tail func() string
		// kept and dropped are what the markers read must and must not hold
		kept, dropped []string
	}{
		{"the window of the end starts on a line", func() string {
			// the window starts on the first byte of DAEMON, the byte before it being a new line
			return daemon + filler(half-len(daemon)-len(final)) + final
		}, []string{logQueued, logStarted, mark("DAEMON", "29.8.0"), mark("SUCCESS", "2026-08-13T01:05:00Z")}, []string{"PREVIOUS"}},
		{"the window of the end starts in a line", func() string {
			// the window starts one byte into a line that holds what would be a marker from its
			// second byte on... and the first byte it is read from is the marker's own
			return "zz" + daemon + filler(half+1-len(daemon)-len(final)) + final
		}, []string{logQueued, logStarted, mark("SUCCESS", "2026-08-13T01:05:00Z")}, []string{"PREVIOUS", "DAEMON"}},
	} {
		path := t.TempDir() + "/docker-update.log"
		text := head + middle + c.tail()
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		if int64(len(text)) <= systemDockerMarkerScanBytes {
			t.Fatalf("%s: the log is %d bytes", c.name, len(text))
		}
		read, err := readDockerMarkers(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range c.kept {
			if !strings.Contains(read, want) {
				t.Errorf("%s: %q is not in what was read:\n%s", c.name, want, read)
			}
		}
		for _, bad := range c.dropped {
			if strings.Contains(read, bad) {
				t.Errorf("%s: %q is in what was read:\n%s", c.name, bad, read)
			}
		}
	}

	// a log that fits is read to its end, with or without the last new line
	path := t.TempDir() + "/small.log"
	if err := os.WriteFile(path, []byte(start+strings.TrimSuffix(final, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if read, err := readDockerMarkers(path); err != nil || !strings.Contains(read, "SUCCESS") || !strings.HasPrefix(read, logQueued+"\n") {
		t.Errorf("read = %q, %v", read, err)
	}

	// the first line is kept whatever it is, and a log with nothing in it, or none, is no run
	if err := os.WriteFile(path, []byte("hello\r\n"+start), 0o644); err != nil {
		t.Fatal(err)
	}
	if read, err := readDockerMarkers(path); err != nil || !strings.HasPrefix(read, "hello\n"+logQueued) {
		t.Errorf("read = %q, %v", read, err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if read, err := readDockerMarkers(path); err != nil || dockerpkg.ParseRun(read).Nonce != "" {
		t.Errorf("read = %q, %v", read, err)
	}
	if _, err := readDockerMarkers(path + ".none"); err == nil {
		t.Error("a log that is not there was read")
	}
}
