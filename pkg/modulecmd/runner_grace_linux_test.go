package modulecmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// What dpkg needs to leave when it is asked to stop, and the grace the runner gives it. The first is six seconds, the second twenty: the tests below use both numbers as they are.
const (
	slowLeave = 6 * time.Second
	graceOf20 = 20 * time.Second
)

// leaderAtOnce is a command whose leader ends at once when it is asked to stop and whose child, in the same group, needs six seconds to leave: dpkg under apt-get. The child writes done when
// it has finished. holdsOutput says whether the child keeps the standard output open (it does when it is dpkg).
func leaderAtOnce(t *testing.T, holdsOutput bool) (exe, doneFile string) {
	t.Helper()
	root := t.TempDir()
	doneFile = filepath.Join(root, "done")
	pidFile := filepath.Join(root, "child.pid")
	redirect := " >/dev/null 2>&1"
	if holdsOutput {
		redirect = ""
	}
	script := "#!/bin/sh\n" +
		"( trap 'sleep 6; echo finished > \"" + doneFile + "\"; exit 0' TERM; while :; do sleep 1; done )" + redirect + " &\n" +
		"echo $! > \"" + pidFile + "\"\n" +
		"trap 'exit 0' TERM\n" +
		"wait\n"
	exe = filepath.Join(root, "leader")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe, doneFile
}

// A command that needs six seconds to leave after it is asked to stop is given them, though its leader goes at once and the grace is twenty: it is not killed before the grace is over, and
// dpkg is not left interrupted. (The runner used to cut the grace to two seconds and to sweep the group as soon as the leader was gone.)
func TestTheRunnerGivesTheWholeGroupItsGraceToLeave(t *testing.T) {
	for name, holds := range map[string]bool{"the child holds the output": true, "the child does not": false} {
		t.Run(name, func(t *testing.T) {
			exe, done := leaderAtOnce(t, holds)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			time.AfterFunc(700*time.Millisecond, cancel) // (the child has set its trap by then)
			started := time.Now()
			_, err := runProcess(ctx, exe, Cmd{Argv: []string{"leader"}}, graceOf20)
			took := time.Since(started)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("the error: %v", err)
			}
			if !exists(done) {
				t.Errorf("the child was killed before it had left (after %v)", took)
			}
			if took < slowLeave-time.Second || took > graceOf20-time.Second {
				t.Errorf("took %v: not the six seconds the child needs, and not the twenty of the grace", took)
			}
		})
	}
}

// ...and a group that does not leave in the grace is killed at its end, the leader's children with it
func TestTheRunnerKillsTheGroupThatDoesNotLeaveInTheGrace(t *testing.T) {
	exe, done := leaderAtOnce(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(700*time.Millisecond, cancel)
	started := time.Now()
	_, err := runProcess(ctx, exe, Cmd{Argv: []string{"leader"}}, 3*time.Second) // (the child needs six)
	took := time.Since(started)
	if !errors.Is(err, context.Canceled) || exists(done) {
		t.Errorf("%v, the child finished: %v", err, exists(done))
	}
	if took < 3*time.Second || took > 6*time.Second {
		t.Errorf("took %v, the grace is 3s", took)
	}
}

// What a command that ended on its own left running in its group is ended with it: apt's methods are in the group, and a program that is not waited for would go on as root
func TestTheRunnerEndsWhatACommandLeftRunningWhenItEndedOnItsOwn(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "sleeper.pid")
	script := "#!/bin/sh\nsleep 30 >/dev/null 2>&1 &\necho $! > \"" + pidFile + "\"\nexit 0\n"
	exe := filepath.Join(root, "leaver")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runProcess(t.Context(), exe, Cmd{Argv: []string{"leaver"}}, graceOf20)
	if err != nil || out.ExitCode != 0 {
		t.Fatalf("%v %+v", err, out)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	if pid := atoi(strings.TrimSpace(string(raw))); alive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the process %d that the command left is running", pid)
	}
}
