package modulecmd

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A box in a temporary directory whose commands are the stubs, and that the helpers and the test both work on.
type realBox struct {
	*fixture
	env map[string]string
}

func newRealBox(t *testing.T, opts ...fixtureOpt) *realBox {
	t.Helper()
	f := newFixture(t, opts...)
	writeStubs(t, f.root)
	dl := filepath.Join(f.root, "dl")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dl, "archive.tar.gz"), f.archive, 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := os.ReadFile(f.path(pathCatalogue))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dl, "catalogue.json"), cat, 0o644); err != nil {
		t.Fatal(err)
	}
	// the catalogue the helpers read is the one of the tests, and the box has none
	if err := os.Remove(f.path(pathCatalogue)); err != nil {
		t.Fatal(err)
	}
	return &realBox{fixture: f, env: map[string]string{
		"CASAOS_MODULE_TEST": "1", "CASAOS_MODULE_CATALOGUE": filepath.Join(dl, "catalogue.json"), "CASAOS_MODULE_ARCHIVE_FILE": filepath.Join(dl, "archive.tar.gz"),
	}}
}

func (b *realBox) getenv(k string) string { return b.env[k] }

// state is every file of the box but what the stubs and the test keep for themselves.
func (b *realBox) state() map[string]string {
	t := b.treeWithoutBookkeeping()
	for p := range t {
		if strings.HasPrefix(p, "/stub/") || strings.HasPrefix(p, "/dl/") {
			delete(t, p)
		}
	}
	return t
}

// env is the environment of a helper: this one's, with the test's variables and a secret that must not reach a command.
func (b *realBox) helperEnv(at, mode string) []string {
	env := []string{"MODULECMD_ROOT=" + b.root, "MODULECMD_AT=" + at, "MODULECMD_MODE=" + mode, "SECRET_OF_THE_TEST=hunter2"}
	for k, v := range b.env {
		env = append(env, k+"="+v)
	}
	return env
}

func (b *realBox) call(args ...string) (int, string) {
	b.t.Helper()
	var out bytes.Buffer
	env := realEnv(b.root, b.getenv, &out)
	return Main(context.Background(), args, env), out.String()
}

func waitFor(t *testing.T, path string) {
	t.Helper()
	for range 200 {
		if exists(path) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not come", path)
}

var killPoints = append(slices.Clone(allSteps), "unpack-planned", "unpack-first", "unpack-mid")

// a process that is killed with the one signal nobody can catch, at each step, leaves a box that remove makes as it was, and that an install can then be made on
func TestAKillAtEachStepLeavesAStateThatRemoveCleans(t *testing.T) {
	for _, at := range killPoints {
		t.Run(at, func(t *testing.T) {
			b := newRealBox(t)
			before, dirsBefore := b.state(), b.dirs()
			cmd := helperCmd(t, "install", b.helperEnv(at, "kill")...)
			cmd.Stdout, cmd.Stderr = nil, nil
			err := cmd.Run()
			ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
			if err == nil || !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
				t.Fatalf("not killed: %v", err)
			}
			// the box is a half state: whatever of it there is, remove takes away; with nothing to take away it says so
			wasThere := false
			for _, p := range []string{"/var/lib/casaos/modules/virt.files", "/var/lib/casaos/modules/virt.dirs", "/var/lib/casaos/modules/virt.version", "/etc/casaos/modules/virt.json"} {
				wasThere = wasThere || b.exists(p)
			}
			wasThere = wasThere || b.runnerEnabled()
			code, out := b.call("remove", testName, "--yes")
			switch {
			case wasThere && code != ExitOK:
				t.Fatalf("remove: exit %d\n%s", code, out)
			case !wasThere && (code != ExitRefused || !strings.Contains(out, "refused: "+CodeNotInstalled)):
				t.Fatalf("remove of nothing: exit %d\n%s", code, out)
			}
			sameTree(t, before, b.state())
			sameDirs(t, dirsBefore, b.dirs())
			if b.runnerEnabled() {
				t.Error("the unit is still enabled")
			}
			for _, p := range []string{"/var/lib/casaos/modules/virt.files", "/var/lib/casaos/modules/virt.dirs", "/var/lib/casaos/modules/virt.version", "/etc/casaos/modules/virt.json"} {
				if b.exists(p) {
					t.Errorf("%s is left", p)
				}
			}
			// the next run empties the area of the download the killed one left, and installs
			if code, out := b.call("install", testName, "--yes"); code != ExitOK {
				t.Fatalf("install again: exit %d\n%s", code, out)
			}
			if entries, _ := os.ReadDir(b.env2tmp()); len(entries) != 0 {
				t.Errorf("the area of the download: %v", entries)
			}
		})
	}
}

func (b *realBox) env2tmp() string { return b.fixture.env.Roots.tmp() }
func (b *realBox) runnerEnabled() bool {
	return exists(stubFile(b.root, "enabled-"+testUnit))
}

func sameState(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// a process that is told to stop with SIGTERM, at each step, ends the step as a failure, rolls back, empties the area of the download and exits 3
func TestASigtermAtEachStepIsARollback(t *testing.T) {
	for _, at := range killPoints {
		t.Run(at, func(t *testing.T) {
			b := newRealBox(t)
			before, dirsBefore := b.state(), b.dirs()
			cmd := helperCmd(t, "install", b.helperEnv(at, "block")...)
			var out bytes.Buffer
			cmd.Stdout = &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, stubFile(b.root, "reached-"+at))
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("it did not end:\n%s", out.String())
			}
			if cmd.ProcessState.ExitCode() != ExitFailed {
				t.Fatalf("exit %d:\n%s", cmd.ProcessState.ExitCode(), out.String())
			}
			step := at
			if strings.HasPrefix(at, "unpack-") {
				step = StepUnpack
			}
			if !strings.Contains(out.String(), "failed at step "+step+": "+CodeInterrupted+"\n") {
				t.Errorf("not interrupted at %s:\n%s", step, out.String())
			}
			kept := keptAll
			if step == StepPackages {
				kept = "unknown"
			}
			if !regexp.MustCompile(`kept_packages: ` + kept + `\n` + resultMarker + `[^\n]*\n$`).MatchString(out.String()) {
				t.Errorf("the last line:\n%s", out.String())
			}
			sameTree(t, before, b.state())
			sameDirs(t, dirsBefore, b.dirs())
			if b.runnerEnabled() {
				t.Error("the unit is enabled")
			}
			if entries, _ := os.ReadDir(b.env2tmp()); len(entries) != 0 {
				t.Errorf("the area of the download: %v", entries)
			}
		})
	}
}

// the command that was running when the signal came is ended with the run, and so is what it started
func TestASigtermEndsTheCommandThatIsRunning(t *testing.T) {
	for _, c := range []struct{ name, flag, reached, step string }{
		{"apt-get", "block-apt", "reached-apt", StepPackages},
		{"systemctl daemon-reload", "block-daemon-reload", "reached-daemon-reload", StepUnits},
		{"systemctl enable", "block-enable", "reached-enable", StepStart},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newRealBox(t)
			before := b.state()
			touch(t, stubFile(b.root, c.flag))
			cmd := helperCmd(t, "install", b.helperEnv("none", "none")...)
			var out bytes.Buffer
			cmd.Stdout = &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, stubFile(b.root, c.reached))
			started := time.Now()
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_ = cmd.Wait()
			if time.Since(started) > 10*time.Second {
				t.Errorf("the run took %v to end: the command was waited for", time.Since(started))
			}
			if cmd.ProcessState.ExitCode() != ExitFailed || !strings.Contains(out.String(), "failed at step "+c.step+": "+CodeInterrupted) {
				t.Fatalf("exit %d:\n%s", cmd.ProcessState.ExitCode(), out.String())
			}
			_ = os.Remove(stubFile(b.root, c.flag))
			sameTree(t, before, b.state())
			if b.runnerEnabled() {
				t.Error("the unit is enabled")
			}
			// no sleeper of the stubs is left running: each wrote its number down, and none of them is a process now
			sleepers, err := os.ReadFile(stubFile(b.root, "sleepers"))
			if err != nil || len(strings.Fields(string(sleepers))) == 0 {
				t.Fatalf("the stub did not say who sleeps: %v", err)
			}
			for _, pid := range strings.Fields(string(sleepers)) {
				if stillAlive(atoi(pid)) {
					t.Errorf("the process %s of the stub is left running", pid)
				}
			}
		})
	}
}

// a real run, end to end, with the commands run for real as the stubs: what they are given and what they are not
func TestARealInstallRunsTheCommandsWithAnEnvironmentOfTheirOwn(t *testing.T) {
	b := newRealBox(t)
	cmd := helperCmd(t, "install", b.helperEnv("none", "none")...)
	cmd.Env = append(cmd.Env, "CASAOS_MODULE_NONCE="+strings.Repeat("ab", 16))
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v:\n%s", err, out.String())
	}
	apt, _ := os.ReadFile(stubFile(b.root, "apt-get.env"))
	line := strings.TrimSpace(string(apt))
	for _, want := range []string{"LC_ALL=C", "LANG=C", "FRONTEND=noninteractive", "SECRET=", "NONCE=", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"} {
		if !strings.Contains(line, want) {
			t.Errorf("the environment of apt-get lacks %q: %s", want, line)
		}
	}
	for _, not := range []string{"hunter2", strings.Repeat("ab", 16)} {
		if strings.Contains(line, not) {
			t.Errorf("the environment of apt-get has %q: %s", not, line)
		}
	}
	if got, _ := os.ReadFile(stubFile(b.root, "apt-installed")); !strings.Contains(string(got), "install --no-download -y -q --no-install-recommends --no-remove") || !strings.HasSuffix(strings.TrimSpace(string(got)), "libvirt-clients qemu-utils") {
		t.Errorf("apt-get install: %q", got)
	}
	// the log has the output of the commands behind "| " and the markers with the nonce
	log, err := os.ReadFile(b.path("/var/log/casaos/module-virt.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "| Setting up the packages ...\n") || !strings.Contains(string(log), stepMarker+" "+strings.Repeat("ab", 16)+" virt route done ") {
		t.Errorf("the log:\n%s", log)
	}
	// what the apt stub says that looks like a marker or a result is behind "| ", and the only lines of the log that start like a marker are the assistant's own, in the one grammar
	results := 0
	for _, l := range strings.Split(string(log), "\n") {
		switch {
		case strings.HasPrefix(l, "| "):
		case strings.HasPrefix(l, stepMarker):
			if !strings.HasPrefix(l, stepMarker+" "+strings.Repeat("ab", 16)+" virt ") || !stepRe.MatchString(l) {
				t.Errorf("a marker that is not the assistant's: %q", l)
			}
		case strings.HasPrefix(l, resultMarker):
			results++
			if !strings.Contains(l, strings.Repeat("ab", 16)) {
				t.Errorf("a result that is not the assistant's: %q", l)
			}
		}
	}
	if results != 1 {
		t.Errorf("%d results in the log:\n%s", results, log)
	}
	if !strings.Contains(string(log), "| "+resultMarker+" - virt ok done - - 2000-01-01T00:00:00Z\n") || !strings.Contains(string(log), "| "+stepMarker+" - virt route done 2000-01-01T00:00:00Z\n") {
		t.Errorf("the stub's own lines that look like markers are not in the log, behind \"| \":\n%s", log)
	}
}

// stillAlive says whether a process is running two seconds after it was told to end: a signal takes a moment to be delivered, and a zombie that nobody has waited for yet is gone
func stillAlive(pid int) bool {
	for range 40 {
		if !alive(pid) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

// alive says whether a process is running (a zombie is not)
func alive(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// the state is the first field after the name, which is in brackets
	rest := raw[bytes.LastIndexByte(raw, ')')+1:]
	fields := strings.Fields(string(rest))
	return len(fields) > 0 && fields[0] != "Z" && fields[0] != "X"
}

// dirs are the directories of the box, but for the ones the assistant keeps for itself (its state, its log, its manifests) and the tests' own.
func (b *realBox) dirs() []string {
	b.t.Helper()
	var out []string
	err := filepath.WalkDir(b.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := b.fixture.env.Roots.Slash(p)
		if !d.IsDir() || p == b.root {
			return nil
		}
		// (the directories above the state, the log and the manifests are the assistant's too, since it makes them for its own)
		if slices.Contains([]string{"/var", "/var/lib", "/var/lib/casaos", "/var/log"}, rel) {
			return nil
		}
		for _, own := range []string{"/stub", "/dl", "/var/log/casaos", "/var/lib/casaos/modules", "/etc/casaos/modules"} {
			if rel == own || strings.HasPrefix(rel, own+"/") {
				return nil
			}
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		b.t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func sameDirs(t *testing.T, before, after []string) {
	t.Helper()
	if !slices.Equal(before, after) {
		t.Errorf("the directories are not as they were:\n before %q\n after  %q", before, after)
	}
}

// A run that is killed in the middle of its rollback has said no result: the lines it left say the rollback began and did not end, which is a run that was interrupted. (The result used to be
// said before the rollback, and was left on a box that was half installed.) A remove then finishes the work.
func TestARunKilledInTheMiddleOfItsRollbackHasNoResult(t *testing.T) {
	b := newRealBox(t)
	before, dirsBefore := b.state(), b.dirs()
	touch(t, stubFile(b.root, "fail-enable")) // the unit does not start: the install fails at the start and is rolled back
	cmd := helperCmd(t, "install", b.helperEnv("rollback_files", "kill")...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); err == nil || !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("not killed: %v\n%s", err, out.String())
	}
	log, _ := os.ReadFile(b.path("/var/log/casaos/module-virt.log"))
	for name, text := range map[string]string{"the output": out.String(), "the log": string(log)} {
		if regexp.MustCompile(`(?m)^` + resultMarker).MatchString(text) { // (the stub prints a line that looks like one, behind "| ")
			t.Errorf("%s has a result:\n%s", name, text)
		}
		for _, want := range []string{" virt start failed ", " virt rollback_stop done ", " virt rollback_files begin "} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q:\n%s", name, want, text)
			}
		}
		if strings.Contains(text, " virt rollback_files done ") || strings.Contains(text, " virt rollback_files failed ") {
			t.Errorf("%s says the step it was killed in ended:\n%s", name, text)
		}
	}
	_ = os.Remove(stubFile(b.root, "fail-enable"))
	if code, o := b.call("remove", testName, "--yes"); code != ExitOK {
		t.Fatalf("remove: %d\n%s", code, o)
	}
	sameTree(t, before, b.state())
	sameDirs(t, dirsBefore, b.dirs())
}
