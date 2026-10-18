package modulecmd

import (
	"bufio"
	"bytes"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// SIGHUP, which a dropped ssh session sends, ends a run as SIGTERM does: the step fails as interrupted, the install is rolled back, the run says its result
func TestASighupIsARollbackToo(t *testing.T) {
	for _, at := range []string{"unpack-mid", "route"} {
		t.Run(at, func(t *testing.T) {
			b := newRealBox(t)
			before := b.state()
			cmd := helperCmd(t, "install", b.helperEnv(at, "block")...)
			var out bytes.Buffer
			cmd.Stdout = &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, stubFile(b.root, "reached-"+at))
			if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("it did not end:\n%s", out.String())
			}
			if cmd.ProcessState.ExitCode() != ExitFailed || !strings.Contains(out.String(), CodeInterrupted+"\n") || !strings.Contains(out.String(), resultMarker+" - virt failed ") {
				t.Fatalf("exit %d:\n%s", cmd.ProcessState.ExitCode(), out.String())
			}
			sameTree(t, before, b.state())
			if b.runnerEnabled() {
				t.Error("the unit is enabled")
			}
		})
	}
}

// What reads the output of a root install closes it half way (`| head`, a pager that was quit): the run goes on, whole, and the log is the record. (A write to a closed output used to end
// the process with SIGPIPE, with no rollback and no result.)
func TestAnInstallWhoseOutputIsClosedHalfWayGoesOnToTheEnd(t *testing.T) {
	b := newRealBox(t)
	cmd := helperCmd(t, "install", b.helperEnv(StepUnits, "waitfile")...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	rd := bufio.NewReader(out)
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("no 'unpack done' (%v)", err)
		}
		if strings.Contains(line, " virt unpack done ") {
			break
		}
	}
	_ = out.Close()
	touch(t, stubFile(b.root, "go"))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("it did not end")
	}
	if code := cmd.ProcessState.ExitCode(); code != ExitOK {
		t.Fatalf("exit %d (%v)", code, cmd.ProcessState)
	}
	log, err := os.ReadFile(b.path("/var/log/casaos/module-virt.log"))
	if err != nil || !strings.Contains(string(log), resultMarker+" - virt ok done ") {
		t.Errorf("the log: %v\n%s", err, log)
	}
	if !b.runnerEnabled() || !b.exists("/etc/casaos/modules/virt.json") {
		t.Error("it is not installed")
	}
}

// The commands the assistant runs get SIGPIPE as every program does: the assistant does not ignore it for them (an ignored signal stays ignored across exec)
func TestTheCommandsOfTheAssistantDoNotInheritAnIgnoredSigpipe(t *testing.T) {
	b := newRealBox(t)
	cmd := helperCmd(t, "install", b.helperEnv("none", "none")...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	raw, err := os.ReadFile(stubFile(b.root, "apt-get.sigign"))
	if err != nil {
		t.Fatal(err)
	}
	// SigIgn: <mask in hex>; SIGPIPE is signal 13, bit 12
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		t.Fatalf("the stub says %q", raw)
	}
	var mask uint64
	for _, c := range fields[1] {
		mask = mask<<4 | uint64(strings.IndexRune("0123456789abcdef", c))
	}
	if mask&(1<<12) != 0 {
		t.Errorf("SIGPIPE is ignored in the commands: SigIgn %s", fields[1])
	}
}

// A panic in the middle of the unpack, right after the first file was put in place, is a rollback and a result and the exit status of a failure; it is never the exit status 2 that Go
// gives a panic, which says that nothing was changed.
func TestAPanicAfterTheFirstRenameIsARollbackAndExit3(t *testing.T) {
	b := newRealBox(t)
	before, dirsBefore := b.state(), b.dirs()
	cmd := helperCmd(t, "install", b.helperEnv("unpack-first", "panic")...)
	var out bytes.Buffer
	cmd.Stdout = &out
	_ = cmd.Run()
	if code := cmd.ProcessState.ExitCode(); code != ExitFailed {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	if last := lastLine(out.String()); !strings.HasPrefix(last, resultMarker+" - virt failed unpack "+CodeInternal+" rolled_back ") {
		t.Errorf("no result line:\n%s", out.String())
	}
	sameTree(t, before, b.state())
	sameDirs(t, dirsBefore, b.dirs())
	if b.runnerEnabled() {
		t.Error("the unit is enabled")
	}
}

// lastLine is the last line of a text.
func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	return s[strings.LastIndex(s, "\n")+1:]
}
