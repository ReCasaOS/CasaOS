//go:build linux

package modulecmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Cmd.Finish: a command that is ended is first asked, alone, to finish what it is doing (SIGINT, which apt-get answers by finishing its dpkg run: it is what leaves dpkg in a state it can go
// on from), and is waited for for as long as Finish says, whatever its group does; only then it is stopped as every command is (SIGTERM to the group, the grace, SIGKILL).
//
// The stand-ins are scripts of POSIX sh that note every signal they get in <dir>/sigs and say ready when their traps are set. The seconds are scaled down from the real ones (the dpkg run
// that needs a minute to leave is three seconds here, the five minutes are ten seconds).

// finishScript is a command whose leader, when it gets SIGINT, takes leaveAfter seconds to finish (it writes done and exits), and which ignores SIGTERM when ignoreTerm is set or notes it and
// leaves at once otherwise. With ignoreInt set it ignores SIGINT as well.
func finishScript(t *testing.T, leaveAfter int, ignoreTerm, ignoreInt bool) (exe, dir string) {
	t.Helper()
	dir = t.TempDir()
	term := "echo TERM >> '" + dir + "/sigs'; exit 0"
	if ignoreTerm {
		term = "echo TERM >> '" + dir + "/sigs'"
	}
	intr := "echo INT >> '" + dir + "/sigs'; sleep " + strconv.Itoa(leaveAfter) + "; echo finished > '" + dir + "/done'; exit 0"
	if ignoreInt {
		intr = "echo INT >> '" + dir + "/sigs'"
	}
	script := "#!/bin/sh\n" +
		"trap \"" + term + "\" TERM\n" +
		"trap \"" + intr + "\" INT\n" +
		": > '" + dir + "/ready'\n" +
		"while :; do sleep 30 >/dev/null 2>&1 & wait $!; done\n"
	exe = filepath.Join(dir, "leader")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe, dir
}

func sigs(dir string) string {
	raw, _ := os.ReadFile(filepath.Join(dir, "sigs"))
	return strings.Join(strings.Fields(string(raw)), " ")
}

// cancelWhenReady ends the context once the command has set its traps
func cancelWhenReady(t *testing.T, dir string, cancel context.CancelFunc) {
	t.Helper()
	go func() {
		for i := 0; i < 200; i++ {
			if exists(filepath.Join(dir, "ready")) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
	}()
}

// apt-get that needs a few seconds to leave after SIGINT is waited for, and is neither asked to stop (SIGTERM) nor killed meanwhile
func TestACommandThatNeedsTimeToFinishIsGivenIt(t *testing.T) {
	exe, dir := finishScript(t, 3, false, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenReady(t, dir, cancel)
	started := time.Now()
	_, err := runProcess(ctx, exe, Cmd{Argv: []string{"leader"}, Finish: 10 * time.Second}, graceOf20)
	took := time.Since(started)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the error: %v", err)
	}
	if !exists(filepath.Join(dir, "done")) {
		t.Errorf("it was ended before it had finished (after %v); signals: %s", took, sigs(dir))
	}
	if got := sigs(dir); got != "INT" {
		t.Errorf("the signals it got: %q, only SIGINT, to the leader, is sent while it finishes", got)
	}
	if took < 3*time.Second || took > 8*time.Second {
		t.Errorf("took %v, it needs three seconds", took)
	}
}

// ...and one that does not leave in the time is asked to stop as every command is, at the end of it, and killed at the end of the grace when it ignores that too
func TestACommandThatDoesNotFinishInTheTimeIsStoppedAsAnyOther(t *testing.T) {
	exe, dir := finishScript(t, 0, true, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenReady(t, dir, cancel)
	started := time.Now()
	_, err := runProcess(ctx, exe, Cmd{Argv: []string{"leader"}, Finish: 2 * time.Second}, 1500*time.Millisecond)
	took := time.Since(started)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the error: %v", err)
	}
	if got := sigs(dir); got != "INT TERM" {
		t.Errorf("the signals: %q, SIGINT first, SIGTERM when the time is up", got)
	}
	if took < 3500*time.Millisecond || took > 6*time.Second {
		t.Errorf("took %v: the two seconds, then the grace of one and a half", took)
	}
}

// apt-get leaves when dpkg has: a leader that has gone while the time lasts is the end of the wait, and what its group still holds is stopped at once as for any command (SIGTERM, the grace)
func TestWhatTheGroupHoldsWhenTheLeaderHasGoneIsStoppedAtOnce(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"( trap 'echo TERM >> \"" + dir + "/child\"; exit 0' TERM; while :; do sleep 1; done ) >/dev/null 2>&1 &\n" +
		"trap 'exit 0' INT\n" +
		": > '" + dir + "/ready'\n" +
		"while :; do sleep 30 >/dev/null 2>&1 & wait $!; done\n"
	exe := filepath.Join(dir, "leader")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenReady(t, dir, cancel)
	started := time.Now()
	_, err := runProcess(ctx, exe, Cmd{Argv: []string{"leader"}, Finish: 20 * time.Second}, graceOf20)
	took := time.Since(started)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the error: %v", err)
	}
	if took > 6*time.Second {
		t.Errorf("took %v: the group was waited for, and the time of Finish was not for it", took)
	}
	if !exists(filepath.Join(dir, "child")) {
		t.Error("the child of the leader was not asked to stop")
	}
}

// a command that asked for no Finish is not sent SIGINT: it is stopped as before, with SIGTERM
func TestACommandWithNoFinishIsOnlySentSigterm(t *testing.T) {
	exe, dir := finishScript(t, 3, false, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenReady(t, dir, cancel)
	_, err := runProcess(ctx, exe, Cmd{Argv: []string{"leader"}}, graceOf20)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the error: %v", err)
	}
	if got := sigs(dir); got != "TERM" {
		t.Errorf("the signals: %q", got)
	}
}

// a command that ends on its own is not sent anything, with a Finish or without
func TestACommandThatEndsOnItsOwnIsNotSentSigintWhateverItsFinish(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "quick")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runProcess(context.Background(), exe, Cmd{Argv: []string{"quick"}, Finish: time.Minute}, graceOf20)
	if err != nil || out.ExitCode != 7 {
		t.Errorf("%v %+v", err, out)
	}
}
