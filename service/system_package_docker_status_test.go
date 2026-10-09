package service

import (
	"context"
	"encoding/json"
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
	if got := string(encoded); got != `{"supported":true,"state":"idle","outcome":"","error":"","error_code":"","exit_code":null,"started_at":"","completed_at":"","from":"","to":"","not_returned":[],"rollback_command":"","log":""}` {
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

func TestDockerStatusMatchesTheReaderOfTheGenericLog(t *testing.T) {
	// the marker for a log that was cut is the generic reader's, which this reads
	dir := t.TempDir()
	path := dir + "/long.log"
	if err := os.WriteFile(path, []byte(strings.Repeat("x\n", systemPackageLogMaxBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
	log, err := readBoundedSystemPackageLog(path, systemPackageLogMaxBytes)
	if err != nil || !strings.HasPrefix(log, dockerLogOmitted) {
		t.Fatalf("the generic reader's prefix is no longer %q: %q, %v", dockerLogOmitted, log[:40], err)
	}
}

func TestDockerStatusReadsTheFirstLineOfALogOnlyAsFarAsItGoes(t *testing.T) {
	path := t.TempDir() + "/first.log"
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 10000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if head, err := readDockerLogHead(path); err != nil || len(head) != systemDockerHeadBytes {
		t.Errorf("head = %d bytes, %v", len(head), err)
	}
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if head, err := readDockerLogHead(path); err != nil || head != "one" {
		t.Errorf("head = %q, %v", head, err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if head, err := readDockerLogHead(path); err != nil || head != "" {
		t.Errorf("head = %q, %v", head, err)
	}
	if _, err := readDockerLogHead(path + ".none"); err == nil {
		t.Error("a missing file has a first line")
	}
}
