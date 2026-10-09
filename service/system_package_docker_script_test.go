package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// The unit's script is run as it is, by /bin/sh, against fakes of the programs it calls
// (apt-get, docker, dpkg-query, systemctl, sleep, date) on a PATH of their own. The clock is the
// fakes' too: sleep moves it, so that a daemon that never comes back is waited for in no time.

const (
	scriptNonce = "0123456789abcdef0123456789abcdef"
	// unsetEnv as the value of a variable in scriptBox.env takes it out of the environment
	unsetEnv = "\x00unset"
)

// the fakes read their answers from files in $FAKE_DIR, and write down what they were asked
const fakeAptGet = `#!/bin/sh
d=$FAKE_DIR
echo "apt-get $*" >> "$d/calls.log"
case " $* " in
*" -s "*)
	cat "$d/plan.txt" 2>/dev/null
	exit "$(cat "$d/sim.rc" 2>/dev/null || echo 0)" ;;
*" --download-only "*)
	exit "$(cat "$d/download.rc" 2>/dev/null || echo 0)" ;;
*" update "*)
	exit 0 ;;
esac
rc=$(cat "$d/install.rc" 2>/dev/null || echo 0)
if [ "$rc" -eq 0 ]; then
	: > "$d/installed"
	cp "$d/down.after.install" "$d/down.left" 2>/dev/null
	# the packages apt brings in with the ones it was asked for: installed by the transaction
	for n in $(cat "$d/install.adds" 2>/dev/null); do echo "$n=1.0.0-1" > "$d/dpkg/$n"; done
fi
exit "$rc"
`

const fakeDocker = `#!/bin/sh
d=$FAKE_DIR
echo "docker $*" >> "$d/calls.log"
case "$*" in
"ps -q") cat "$d/ids.txt" 2>/dev/null ;;
"inspect "*) cat "$d/inspect.txt" 2>/dev/null ;;
"ps --format"*)
	n=$(cat "$d/ps.count" 2>/dev/null || echo 0)
	n=$((n + 1))
	echo "$n" > "$d/ps.count"
	f="$d/ps.$n"
	[ -f "$f" ] || f="$d/ps.last"
	cat "$f" 2>/dev/null ;;
"version "*)
	if [ -f "$d/broken" ] && [ ! -f "$d/started" ]; then exit 1; fi
	left=$(cat "$d/down.left" 2>/dev/null || echo 0)
	if [ "$left" -gt 0 ]; then
		echo $((left - 1)) > "$d/down.left"
		exit 1
	fi
	if [ -f "$d/installed" ] && [ ! -f "$d/stale" ]; then cat "$d/version.after"; else cat "$d/version.before"; fi ;;
esac
exit 0
`

const fakeDpkgQuery = `#!/bin/sh
d=$FAKE_DIR
echo "dpkg-query $*" >> "$d/calls.log"
for last; do :; done
cat "$d/dpkg/$last" 2>/dev/null || exit 1
`

const fakeSystemctl = `#!/bin/sh
d=$FAKE_DIR
echo "systemctl $*" >> "$d/calls.log"
case "$*" in
*docker.service*) : > "$d/started" ;;
esac
exit 0
`

const fakeSleep = `#!/bin/sh
d=$FAKE_DIR
n=${1:-1}
[ "$n" -ge 1 ] 2>/dev/null || n=1
t=$(cat "$d/clock" 2>/dev/null || echo 0)
echo $((t + n)) > "$d/clock"
`

// timeout runs what it is given, and writes it down: every call to docker must have been bounded
const fakeTimeout = `#!/bin/sh
echo "timeout $*" >> "$FAKE_DIR/calls.log"
shift
exec "$@"
`

const fakeDate = `#!/bin/sh
d=$FAKE_DIR
t=$(cat "$d/clock" 2>/dev/null || echo 0)
case "$*" in
*+%s*) echo $((1790000000 + t)) ;;
*) printf '2026-10-09T%02d:%02d:%02dZ\n' $((t / 3600 % 24)) $((t / 60 % 60)) $((t % 60)) ;;
esac
`

type scriptBox struct {
	t   *testing.T
	dir string
	env map[string]string
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// newScriptBox is the owner's box: 28.0.4 installed, 29.8.0 confirmed, three containers running.
func newScriptBox(t *testing.T) *scriptBox {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the unit's script is for a Linux shell")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("the unit's script is run by /bin/sh, which this host does not have")
	}
	dir := t.TempDir()
	b := &scriptBox{t: t, dir: dir, env: map[string]string{}}
	for name, body := range map[string]string{
		"apt-get": fakeAptGet, "docker": fakeDocker, "dpkg-query": fakeDpkgQuery,
		"systemctl": fakeSystemctl, "sleep": fakeSleep, "date": fakeDate, "timeout": fakeTimeout,
	} {
		writeFile(t, filepath.Join(dir, "bin", name), body, 0o755)
	}
	b.set("plan.txt", ownerContaind+ownerDockerCE+ownerCLI)
	b.set("dpkg/containerd.io", "containerd.io=1.7.27-1\n")
	b.set("dpkg/docker-ce", "docker-ce="+debian28+"\n")
	b.set("dpkg/docker-ce-cli", "docker-ce-cli="+debian28+"\n")
	b.set("ids.txt", "aaaaaaaaaaaa\nbbbbbbbbbbbb\ncccccccccccc\n")
	b.set("inspect.txt", "/web always\n/db unless-stopped\n/job no\n")
	// the daemon is down for two looks after the install, then it is the new one; the containers
	// that have a policy come back one look after the other, the one that has none never does
	b.set("version.before", "28.0.4\n")
	b.set("version.after", "29.8.0\n")
	b.set("down.after.install", "2\n")
	b.set("ps.1", "web\n")
	b.set("ps.last", "web\ndb\n")
	return b
}

func (b *scriptBox) set(name, content string) {
	b.t.Helper()
	writeFile(b.t, filepath.Join(b.dir, name), content, 0o644)
}

func (b *scriptBox) logPath() string { return filepath.Join(b.dir, "docker-update.log") }

type scriptRun struct {
	log   string
	exit  int
	calls []string
	run   dockerpkg.Run
}

func (b *scriptBox) env0() map[string]string {
	return map[string]string{
		"PATH":                     filepath.Join(b.dir, "bin") + ":/usr/bin:/bin",
		"FAKE_DIR":                 b.dir,
		"CASAOS_DU_NONCE":          scriptNonce,
		"CASAOS_DU_LOG":            b.logPath(),
		"CASAOS_DU_APT":            filepath.Join(b.dir, "bin", "apt-get"),
		"CASAOS_DU_PINS":           "containerd.io=2.1.4-1 docker-ce=" + debian29 + " docker-ce-cli=" + debian29,
		"CASAOS_DU_NAMES":          "containerd.io docker-ce docker-ce-cli",
		"CASAOS_DU_TO":             "29.8.0",
		"CASAOS_DU_DAEMON_TIMEOUT": "10",
		"CASAOS_DU_RETURN_TIMEOUT": "10",
		"CASAOS_DU_POLL":           "0",
		"DEBIAN_FRONTEND":          "noninteractive",
	}
}

// run writes the first line the core writes, then runs the script as systemd-run would.
func (b *scriptBox) run() scriptRun {
	b.t.Helper()
	writeFile(b.t, b.logPath(), dockerpkg.QueuedMarker(scriptNonce, time.Date(2026, 10, 9, 9, 59, 0, 0, time.UTC)), 0o644)
	env := b.env0()
	for k, v := range b.env {
		if v == unsetEnv {
			delete(env, k)
		} else {
			env[k] = v
		}
	}
	var environ []string
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", dockerUpdateScript)
	cmd.Env = environ
	cmd.Dir = b.dir
	out, err := cmd.CombinedOutput()
	var result scriptRun
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		result.exit = exitErr.ExitCode()
	default:
		b.t.Fatalf("the script did not run: %v", err)
	}
	if ctx.Err() != nil {
		b.t.Fatalf("the script did not end: %s", out)
	}
	data, _ := os.ReadFile(b.logPath())
	result.log = string(data)
	result.run = dockerpkg.ParseRun(result.log)
	calls, _ := os.ReadFile(filepath.Join(b.dir, "calls.log"))
	result.calls = strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(out) > 0 {
		// everything goes to the log: what reaches the unit's own output is a script that failed to start
		b.t.Logf("the unit's own output: %s", out)
	}
	return result
}

// called counts the calls of a program that begin with prefix ("docker ps", "systemctl").
func (r scriptRun) called(prefix string) int {
	var n int
	for _, call := range r.calls {
		if strings.HasPrefix(call, prefix) {
			n++
		}
	}
	return n
}

// mentions counts the calls that have the text anywhere in them.
func (r scriptRun) mentions(text string) int {
	var n int
	for _, call := range r.calls {
		if strings.Contains(call, text) {
			n++
		}
	}
	return n
}

// unbounded are the calls to docker and the start of the daemon that did not go through timeout:
// a docker command waits for ever on a socket whose service will not start.
func (r scriptRun) unbounded() []string {
	var calls []string
	for _, call := range r.calls {
		if strings.HasPrefix(call, "docker ") && !r.hasBounded(call) {
			calls = append(calls, call)
		}
		if strings.HasPrefix(call, "systemctl start") && !r.hasBounded(call) {
			calls = append(calls, call)
		}
	}
	return calls
}

// hasBounded: the call was made by `timeout <seconds> <call>`, which wrote itself down first.
func (r scriptRun) hasBounded(call string) bool {
	return r.called("timeout 20 "+call)+r.called("timeout 120 "+call) > 0
}

// aptInstalls are the calls that downloaded or installed something: not a simulation.
func (r scriptRun) aptChanges() []string {
	var calls []string
	for _, call := range r.calls {
		if strings.HasPrefix(call, "apt-get ") && !strings.Contains(call, " -s ") {
			calls = append(calls, call)
		}
	}
	return calls
}

var terminalLine = regexp.MustCompile(`^CASAOS_DOCKER_UPDATE_(SUCCESS|RESTART_PENDING|FAILED)( |$)`)

// checkLogShape holds of every run: each line the unit speaks with has the nonce as its first
// field, and there is one terminal marker and it is the last line.
func checkLogShape(t *testing.T, log string) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	var terminals int
	for i, line := range lines {
		if i == 0 {
			continue
		}
		if strings.HasPrefix(line, "CASAOS_DOCKER_UPDATE_") {
			if fields := strings.Fields(line); len(fields) < 2 || fields[1] != scriptNonce {
				t.Errorf("a marker without the nonce: %q", line)
			}
		}
		if terminalLine.MatchString(line) {
			terminals++
			if i != len(lines)-1 {
				t.Errorf("the terminal marker %q is not the last line (line %d of %d)", line, i+1, len(lines))
			}
		}
	}
	if terminals != 1 {
		t.Errorf("%d terminal markers, want exactly one:\n%s", terminals, log)
	}
}

// markerKinds are the kinds of the unit's marker lines, in the order written.
func markerKinds(log string) []string {
	var kinds []string
	for _, line := range strings.Split(log, "\n")[1:] {
		if rest, ok := strings.CutPrefix(line, "CASAOS_DOCKER_UPDATE_"); ok {
			kind, _, _ := strings.Cut(rest, " ")
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func TestTheScriptIsPOSIXShAndAConstant(t *testing.T) {
	// no shell of this list can be run by dash or busybox ash
	for _, bashism := range []string{"[[", "<<<", "pipefail", "local ", "eval ", "function ", "declare ", "source ", "${!", "&>", "<("} {
		if strings.Contains(dockerUpdateScript, bashism) {
			t.Errorf("the script has %q, which is not POSIX sh", bashism)
		}
	}
	// nothing in it restarts Docker or refreshes the index
	for _, forbidden := range []string{"docker restart", "docker stop", "docker start", "docker kill", "docker rm", "systemctl restart", "systemctl stop", "systemctl reload", "service docker", "apt-get"} {
		if strings.Contains(dockerUpdateScript, forbidden) {
			t.Errorf("the script has %q", forbidden)
		}
	}
	if strings.Contains(dockerUpdateScript, "`") || strings.Contains(dockerUpdateScript, "\r") {
		t.Error("the script has a backtick or a carriage return")
	}
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/bin/sh"); err == nil {
			// sh -n reads the whole script and runs none of it
			cmd := exec.Command("/bin/sh", "-n", "-c", dockerUpdateScript)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("sh -n: %v: %s", err, out)
			}
		}
	}
}

func TestTheUnitUpdatesTheOwnersDocker(t *testing.T) {
	b := newScriptBox(t)
	r := b.run()
	checkLogShape(t, r.log)

	if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess || r.run.FailReason != "" {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	wantPrevious := []string{"containerd.io=1.7.27-1", "docker-ce=" + debian28, "docker-ce-cli=" + debian28}
	if !reflect.DeepEqual(r.run.Previous, wantPrevious) {
		t.Errorf("previous = %#v, want %#v", r.run.Previous, wantPrevious)
	}
	if r.run.DaemonVersion != "29.8.0" || r.run.StartedAt.IsZero() || r.run.InstalledAt.IsZero() || r.run.CompletedAt.IsZero() {
		t.Errorf("run = %#v", r.run)
	}
	// the container that has no policy is the one that did not come back
	if want := []dockerpkg.NotReturned{{Name: "job", RestartPolicy: "no"}}; !reflect.DeepEqual(r.run.NotReturned, want) {
		t.Errorf("not returned = %#v, want %#v", r.run.NotReturned, want)
	}
	for _, line := range []string{"container: web always", "container: db unless-stopped", "container: job no"} {
		if !strings.Contains(r.log, "\n"+line+"\n") {
			t.Errorf("the log lacks %q:\n%s", line, r.log)
		}
	}
	if got, want := strings.Join(markerKinds(r.log), " "), "STARTED PREVIOUS DOWNLOADED INSTALLED DAEMON NOTRETURNED SUCCESS"; got != want {
		t.Errorf("markers = %s, want %s", got, want)
	}

	// apt was asked: the simulation of the guard, the download, the install, in that order, with
	// the confirmed pins; and nothing else
	pins := "containerd.io=2.1.4-1 docker-ce=" + debian29 + " docker-ce-cli=" + debian29
	apt := filepath.Join(b.dir, "bin", "apt-get")
	var aptCalls []string
	for _, call := range r.calls {
		if strings.HasPrefix(call, "apt-get ") {
			aptCalls = append(aptCalls, call)
		}
	}
	wantApt := []string{
		"apt-get -s --no-remove -o Debug::NoLocking=true -o Dpkg::Use-Pty=0 install --only-upgrade --no-install-recommends " + pins,
		"apt-get -y --no-remove --download-only -o Dpkg::Use-Pty=0 -o DPkg::Lock::Timeout=120 install --only-upgrade --no-install-recommends " + pins,
		"apt-get -y --no-remove -o Dpkg::Use-Pty=0 -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=120 install --only-upgrade --no-install-recommends " + pins,
	}
	if !reflect.DeepEqual(aptCalls, wantApt) {
		t.Errorf("apt-get calls (%s):\n%s\nwant:\n%s", apt, strings.Join(aptCalls, "\n"), strings.Join(wantApt, "\n"))
	}
	// Docker is never restarted, stopped or started by the script; it only waits for it
	for _, verb := range []string{"restart", "stop", "start", "kill", "rm", "run", "exec", "pause"} {
		if n := r.called("docker " + verb); n != 0 {
			t.Errorf("docker %s was called %d times", verb, n)
		}
	}
	if n := r.called("systemctl"); n != 0 {
		t.Errorf("systemctl was called %d times for an update that went well", n)
	}
	if n := r.called("apt-get update"); n != 0 {
		t.Error("the script refreshed the index")
	}
	if calls := r.unbounded(); len(calls) != 0 {
		t.Errorf("calls that were not bounded by timeout: %v", calls)
	}
}

func TestTheUnitSaysRestartPendingWhenTheDaemonIsStillTheOldOne(t *testing.T) {
	b := newScriptBox(t)
	b.set("stale", "")
	b.set("down.after.install", "0\n")
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalRestartPending || r.run.DaemonVersion != "28.0.4" {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	if n := r.called("systemctl"); n != 0 {
		t.Errorf("systemctl was called %d times: nothing restarts Docker", n)
	}
	for _, verb := range []string{"restart", "stop", "start"} {
		if n := r.called("docker " + verb); n != 0 {
			t.Errorf("docker %s was called", verb)
		}
	}
}

func TestTheUnitHasNoVersionToCheckWhenDockerCEIsNotInThePlan(t *testing.T) {
	b := newScriptBox(t)
	b.env["CASAOS_DU_TO"] = ""
	b.set("stale", "")
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
}

func TestTheUnitDoesNotWaitForAContainerThatHasNoPolicy(t *testing.T) {
	b := newScriptBox(t)
	// two containers, neither of which comes back, and neither of which is waited for
	b.set("ids.txt", "aaaaaaaaaaaa\nbbbbbbbbbbbb\n")
	b.set("inspect.txt", "/web no\n/old \n")
	b.set("ps.1", "")
	b.set("ps.last", "")
	r := b.run()
	checkLogShape(t, r.log)
	if r.run.Terminal != dockerpkg.TerminalSuccess || len(r.run.NotReturned) != 2 {
		t.Fatalf("run = %#v\n%s", r.run, r.log)
	}
	// "no" and no policy at all both end up in the report, in the order of the snapshot
	if want := []dockerpkg.NotReturned{{Name: "web", RestartPolicy: "no"}, {Name: "old"}}; !reflect.DeepEqual(r.run.NotReturned, want) {
		t.Errorf("not returned = %#v, want %#v", r.run.NotReturned, want)
	}
	if looks := r.called("docker ps --format"); looks != 1 {
		t.Errorf("the containers were looked at %d times: a container that never comes back is not waited for", looks)
	}
}

func TestTheUnitWaitsForTheContainersThatComeBack(t *testing.T) {
	b := newScriptBox(t)
	b.set("inspect.txt", "/web always\n/db unless-stopped\n")
	b.set("ids.txt", "aaaaaaaaaaaa\nbbbbbbbbbbbb\n")
	b.set("ps.1", "")
	b.set("ps.2", "web\n")
	b.set("ps.3", "web\n")
	b.set("ps.last", "web\ndb\n")
	r := b.run()
	checkLogShape(t, r.log)
	if r.run.Terminal != dockerpkg.TerminalSuccess || len(r.run.NotReturned) != 0 {
		t.Fatalf("run = %#v\n%s", r.run, r.log)
	}
	if looks := r.called("docker ps --format"); looks != 4 {
		t.Errorf("the containers were looked at %d times, want 4", looks)
	}
	// ... and a container that does not come back in time is told, with its policy
	b = newScriptBox(t)
	b.set("inspect.txt", "/web always\n/db on-failure\n")
	b.set("ids.txt", "aaaaaaaaaaaa\nbbbbbbbbbbbb\n")
	b.set("ps.last", "web\n")
	r = b.run()
	checkLogShape(t, r.log)
	if want := []dockerpkg.NotReturned{{Name: "db", RestartPolicy: "on-failure"}}; r.run.Terminal != dockerpkg.TerminalSuccess || !reflect.DeepEqual(r.run.NotReturned, want) {
		t.Fatalf("run = %#v\n%s", r.run, r.log)
	}
}

func TestTheUnitInstallsNothingWhenTheGuardRefuses(t *testing.T) {
	cases := map[string]struct {
		plan string
		rc   string
	}{
		"another package":                  {ownerContaind + ownerDockerCE + ownerCLI + simLibc, ""},
		"another package alone":            {simLibc, ""},
		"a removal":                        {ownerContaind + ownerDockerCE + ownerCLI + "Remv docker-compose-v2 [2.1.0]\n", ""},
		"a purge":                          {ownerContaind + ownerDockerCE + ownerCLI + "Purg docker-compose-v2 [2.1.0]\n", ""},
		"a new package that is not a name": {ownerContaind + ownerDockerCE + ownerCLI + "Inst libnewdep (1.0 Debian:11/stable [amd64])\n", ""},
		"a new docker.io":                  {ownerContaind + ownerDockerCE + ownerCLI + "Inst docker.io (26.1.5 Debian:11/stable [amd64])\n", ""},
		"a package of another arch":        {ownerContaind + ownerDockerCE + ownerCLI + "Inst containerd.io:armhf [1.7.27-1] (2.1.4-1 Docker CE [armhf])\n", ""},
		"a simulation that fails":          {ownerContaind + ownerDockerCE + ownerCLI, "100"},
		"a simulation that fails, bare":    {"E: Unable to correct problems, you have held broken packages.\n", "100"},
		"a name that only looks like one":  {ownerContaind + ownerDockerCE + ownerCLI + "Inst docker-ce-cli-extra [1] (2 x [amd64])\n", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := newScriptBox(t)
			b.set("plan.txt", c.plan)
			if c.rc != "" {
				b.set("sim.rc", c.rc)
			}
			r := b.run()
			checkLogShape(t, r.log)
			if r.exit != 1 || r.run.Terminal != dockerpkg.TerminalFailed || r.run.FailReason != dockerpkg.FailGuard {
				t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
			}
			if !strings.Contains(r.log, "\nCASAOS_DOCKER_UPDATE_GUARD "+scriptNonce+" plan\n") {
				t.Errorf("no GUARD marker:\n%s", r.log)
			}
			if changes := r.aptChanges(); len(changes) != 0 {
				t.Errorf("apt-get changed the box after the guard refused: %v", changes)
			}
			if _, err := os.Stat(filepath.Join(b.dir, "installed")); err == nil {
				t.Error("something was installed")
			}
			// the guard is the only thing the daemon was not asked about
			if r.called("docker version") != 0 {
				t.Error("the daemon was waited for after a refusal")
			}
		})
	}
}

// The owner's Docker 29 needs nftables and its libraries, which the box does not have: they are
// among the names the core approved, and not among the pins.
const (
	depsNames = "containerd.io docker-ce docker-ce-cli libedit2 libjansson4 libnftables1 nftables"
	depsAdds  = "libedit2\nlibjansson4\nlibnftables1\nnftables\n"
)

func newDependenciesBox(t *testing.T) *scriptBox {
	b := newScriptBox(t)
	b.set("plan.txt", ownerNewDependencies+ownerContaind+ownerDockerCE+ownerCLI)
	b.set("install.adds", depsAdds)
	b.env["CASAOS_DU_NAMES"] = depsNames
	return b
}

func TestTheUnitInstallsTheNewDependenciesOfTheNewEngine(t *testing.T) {
	b := newDependenciesBox(t)
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess || r.run.FailReason != "" {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	// apt is told to upgrade the pins and nothing is named beside them: the new packages come
	// with them as dependencies, which only the simulation of the guard has seen
	pins := "containerd.io=2.1.4-1 docker-ce=" + debian29 + " docker-ce-cli=" + debian29
	var aptCalls []string
	for _, call := range r.calls {
		if strings.HasPrefix(call, "apt-get ") {
			aptCalls = append(aptCalls, call)
		}
	}
	wantApt := []string{
		"apt-get -s --no-remove -o Debug::NoLocking=true -o Dpkg::Use-Pty=0 install --only-upgrade --no-install-recommends " + pins,
		"apt-get -y --no-remove --download-only -o Dpkg::Use-Pty=0 -o DPkg::Lock::Timeout=120 install --only-upgrade --no-install-recommends " + pins,
		"apt-get -y --no-remove -o Dpkg::Use-Pty=0 -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=120 install --only-upgrade --no-install-recommends " + pins,
	}
	if !reflect.DeepEqual(aptCalls, wantApt) {
		t.Errorf("apt-get calls:\n%s\nwant:\n%s", strings.Join(aptCalls, "\n"), strings.Join(wantApt, "\n"))
	}
	// the transaction installed them (the fake apt does when the real install runs)
	for _, name := range []string{"nftables", "libnftables1", "libjansson4", "libedit2"} {
		if _, err := os.Stat(filepath.Join(b.dir, "dpkg", name)); err != nil {
			t.Errorf("%s was not installed by the transaction: %v", name, err)
		}
	}
	// ... and what the rollback puts back is what was there: they were not
	wantPrevious := []string{"containerd.io=1.7.27-1", "docker-ce=" + debian28, "docker-ce-cli=" + debian28}
	if !reflect.DeepEqual(r.run.Previous, wantPrevious) {
		t.Errorf("previous = %#v, want %#v", r.run.Previous, wantPrevious)
	}
	if rollback := dockerpkg.RollbackCommand(r.run.Previous); strings.Contains(rollback, "nftables") {
		t.Errorf("rollback = %q: a package that was not there cannot be put back", rollback)
	}
	if got, want := strings.Join(markerKinds(r.log), " "), "STARTED PREVIOUS DOWNLOADED INSTALLED DAEMON NOTRETURNED SUCCESS"; got != want {
		t.Errorf("markers = %s, want %s", got, want)
	}
}

func TestTheUnitInstallsNothingWhenTheGuardRefusesBesideTheDependencies(t *testing.T) {
	deps := ownerNewDependencies + ownerContaind + ownerDockerCE + ownerCLI
	cases := map[string]struct {
		plan  string
		names string
		rc    string
	}{
		"a dependency that is not among the names":            {deps, "containerd.io docker-ce docker-ce-cli libedit2 libjansson4 libnftables1", ""},
		"no dependency among the names":                       {deps, "containerd.io docker-ce docker-ce-cli", ""},
		"a dependency that was not there when it was planned": {deps + "Inst libnewdep (1.0 Debian:11/stable [amd64])\n", depsNames, ""},
		// installed since the plan was made, so that apt would upgrade it: not what was confirmed
		"a dependency that is installed now":              {strings.Replace(deps, "Inst nftables (0.9.8-3.1+deb11u2", "Inst nftables [0.9.7-1] (0.9.8-3.1+deb11u2", 1), depsNames, ""},
		"an upgrade of a library the plan installs":       {deps + "Inst libedit2 [3.1-1] (3.1-20191231-2+b1 Debian:11/stable [amd64])\n", depsNames, ""},
		"a dependency of another architecture":            {deps + "Inst nftables:armhf (0.9.8 Debian:11/stable [armhf])\n", depsNames, ""},
		"a name that is the start of a dependency's":      {deps + "Inst libnftables (0.9.8 Debian:11/stable [amd64])\n", depsNames, ""},
		"a name that holds a dependency's":                {deps + "Inst xnftables (0.9.8 Debian:11/stable [amd64])\n", depsNames, ""},
		"an upgrade of another package":                   {deps + simLibc, depsNames, ""},
		"a removal":                                       {deps + "Remv iptables-persistent [1.0]\n", depsNames, ""},
		"a purge":                                         {deps + "Purg iptables-persistent [1.0]\n", depsNames, ""},
		"a docker.io":                                     {deps + "Inst docker.io (26.1.5 Debian:11/stable [amd64])\n", depsNames, ""},
		"a simulation that fails":                         {deps, depsNames, "100"},
		"a simulation that fails, with a plan of its own": {deps + "E: Unable to correct problems, you have held broken packages.\n", depsNames, "100"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := newDependenciesBox(t)
			b.set("plan.txt", c.plan)
			b.env["CASAOS_DU_NAMES"] = c.names
			if c.rc != "" {
				b.set("sim.rc", c.rc)
			}
			r := b.run()
			checkLogShape(t, r.log)
			if r.exit != 1 || r.run.Terminal != dockerpkg.TerminalFailed || r.run.FailReason != dockerpkg.FailGuard {
				t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
			}
			if !strings.Contains(r.log, "\nCASAOS_DOCKER_UPDATE_GUARD "+scriptNonce+" plan\n") {
				t.Errorf("no GUARD marker:\n%s", r.log)
			}
			if changes := r.aptChanges(); len(changes) != 0 {
				t.Errorf("apt-get changed the box after the guard refused: %v", changes)
			}
			for _, path := range []string{"installed", "dpkg/nftables"} {
				if _, err := os.Stat(filepath.Join(b.dir, path)); err == nil {
					t.Errorf("%s: something was installed", path)
				}
			}
		})
	}
}

func TestTheGuardGoesByTheNamesForAnInstThatAptPrintsAsNew(t *testing.T) {
	// the rule is the names': an Inst of one of them goes through with or without the version it
	// replaces (and an upgrade has to be of a package that was pinned, which the names alone do not say)
	b := newDependenciesBox(t)
	b.set("plan.txt", ownerNewDependencies+ownerContaind+ownerCLI+"Inst docker-ce (5:29.8.0-1~debian.11~bullseye Docker CE:bullseye [amd64])\n")
	if r := b.run(); r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess {
		t.Fatalf("a pinned package that is new: exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
}

func TestTheUnitRefusesAListItCannotTrust(t *testing.T) {
	cases := map[string]map[string]string{
		"a pin with a command in it":      {"CASAOS_DU_PINS": "docker-ce=5:29.8.0;touch${IFS}x"},
		"a pin with a quote":              {"CASAOS_DU_PINS": "docker-ce=5:29.8.0'x"},
		"a name with a glob":              {"CASAOS_DU_NAMES": "docker-*"},
		"a new package with a glob":       {"CASAOS_DU_NAMES": depsNames + " lib*"},
		"a new package that is a command": {"CASAOS_DU_NAMES": depsNames + " nft;touch${IFS}x"},
		"a new package with a quote":      {"CASAOS_DU_NAMES": depsNames + " nft'x"},
		"a new line in the pins":          {"CASAOS_DU_PINS": "docker-ce=1\ndocker-ce-cli=2"},
		"no pins":                         {"CASAOS_DU_PINS": ""},
		"no names":                        {"CASAOS_DU_NAMES": "", "CASAOS_DU_PINS": ""},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			b := newScriptBox(t)
			b.env = env
			r := b.run()
			checkLogShape(t, r.log)
			if r.exit != 1 || r.run.FailReason != dockerpkg.FailGuard {
				t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
			}
			// nothing was run at all: not even a look at what is installed
			if r.called("apt-get") != 0 || r.called("dpkg-query") != 0 || r.called("docker") != 0 {
				t.Errorf("the script ran something with a list it does not trust: %v", r.calls)
			}
			if _, err := os.Stat(filepath.Join(b.dir, "x")); err == nil {
				t.Error("a command in a pin ran")
			}
		})
	}
}

func TestTheUnitDownloadsBeforeItChangesAnything(t *testing.T) {
	b := newScriptBox(t)
	b.set("download.rc", "100")
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 1 || r.run.Terminal != dockerpkg.TerminalFailed || r.run.FailReason != dockerpkg.FailDownload {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	if got := len(r.aptChanges()); got != 1 || !strings.Contains(r.aptChanges()[0], "--download-only") {
		t.Errorf("apt-get changes = %v: only the download, which changes nothing, may have run", r.aptChanges())
	}
	if _, err := os.Stat(filepath.Join(b.dir, "installed")); err == nil {
		t.Error("something was installed")
	}
	if r.run.InstalledAt != (time.Time{}) || strings.Contains(r.log, "_DOWNLOADED") || r.called("systemctl") != 0 {
		t.Errorf("the log or the calls say more than a failed download:\n%s\n%v", r.log, r.calls)
	}
	// the owner's rollback has what was there before
	if len(r.run.Previous) != 3 {
		t.Errorf("previous = %#v", r.run.Previous)
	}
}

func TestTheUnitTriesOnceToBringDockerBackWhenTheInstallFails(t *testing.T) {
	b := newScriptBox(t)
	b.set("install.rc", "100")
	// dpkg stopped the daemon and left it down: it is up again once systemd is asked to start it
	b.set("broken", "")
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 1 || r.run.Terminal != dockerpkg.TerminalFailed || r.run.FailReason != dockerpkg.FailInstall {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	if n := r.called("systemctl start docker.socket docker.service"); n != 1 {
		t.Errorf("systemctl start was called %d times, want exactly once: %v", n, r.calls)
	}
	if n := r.called("systemctl"); n != 1 {
		t.Errorf("systemctl was called %d times, want only the one start: %v", n, r.calls)
	}
	if calls := r.unbounded(); len(calls) != 0 {
		t.Errorf("calls that were not bounded by timeout: %v", calls)
	}
	if strings.Contains(r.log, "_INSTALLED") || strings.Contains(r.log, "_DAEMON") {
		t.Errorf("the log says the install went through or the daemon is back:\n%s", r.log)
	}

	// a daemon that does not come back is still an install that failed, and is asked for once
	b = newScriptBox(t)
	b.set("install.rc", "100")
	b.set("broken", "")
	// systemctl start does nothing here: the daemon stays down
	writeFile(t, filepath.Join(b.dir, "bin", "systemctl"), "#!/bin/sh\necho \"systemctl $*\" >> \"$FAKE_DIR/calls.log\"\nexit 0\n", 0o755)
	r = b.run()
	checkLogShape(t, r.log)
	if r.exit != 1 || r.run.FailReason != dockerpkg.FailInstall || r.called("systemctl start") != 1 {
		t.Fatalf("exit = %d, run = %#v, calls = %v\n%s", r.exit, r.run, r.calls, r.log)
	}
}

func TestTheUnitSaysDaemonWhenDockerNeverComesBack(t *testing.T) {
	b := newScriptBox(t)
	b.set("broken", "")
	// systemctl start does not help (a daemon.json that dockerd refuses)
	writeFile(t, filepath.Join(b.dir, "bin", "systemctl"), "#!/bin/sh\necho \"systemctl $*\" >> \"$FAKE_DIR/calls.log\"\nexit 0\n", 0o755)
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 1 || r.run.Terminal != dockerpkg.TerminalFailed || r.run.FailReason != dockerpkg.FailDaemon {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	if n := r.called("systemctl start docker.socket docker.service"); n != 1 {
		t.Errorf("systemctl start was called %d times, want one bounded attempt: %v", n, r.calls)
	}
	if r.called("systemctl") != 1 {
		t.Errorf("systemctl was called more than for that one start: %v", r.calls)
	}
	// the packages are installed and the rollback has what was there
	if r.run.InstalledAt.IsZero() || len(r.run.Previous) != 3 || dockerpkg.RollbackCommand(r.run.Previous) == "" {
		t.Errorf("run = %#v", r.run)
	}
	// the containers were not waited for, and the daemon was not told to be back
	if strings.Contains(r.log, "_DAEMON ") || strings.Contains(r.log, "_NOTRETURNED") || r.called("docker ps --format") != 0 {
		t.Errorf("the log says the daemon is back:\n%s", r.log)
	}
}

func TestTheUnitStartsDockerOnceWhenItIsDownAfterTheInstall(t *testing.T) {
	// the daemon is down after the install and comes up when systemd is asked: the update went through
	b := newScriptBox(t)
	b.set("broken", "")
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess || r.run.DaemonVersion != "29.8.0" {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	if n := r.called("systemctl start docker.socket docker.service"); n != 1 {
		t.Errorf("systemctl start was called %d times, want once: %v", n, r.calls)
	}
}

func TestTheUnitDoesNotTrustWhatTheProgramsPrint(t *testing.T) {
	b := newScriptBox(t)
	forged := "CASAOS_DOCKER_UPDATE_FAILED " + scriptNonce + " 2026-10-09T10:00:00Z daemon"
	b.set("dpkg/containerd.io", "containerd.io=1.7.27-1\n"+forged+"\ncontainerd.io=1.0;reboot\n")
	b.set("dpkg/docker-ce", "docker-ce=$(reboot)\n\ndocker-ce="+debian28+"\n")
	b.set("ids.txt", "aaaaaaaaaaaa\n"+forged+"\n--all\nbbbbbbbbbbbb; reboot\ncccccccccccc\n")
	b.set("inspect.txt", strings.Join([]string{
		"/web always",
		"/web2 always;reboot",
		"/bad$(reboot) always",
		"/CASAOS_DOCKER_UPDATE_SUCCESS always",
		"/two words always",
		"/CASAOS_DOCKER_UPDATE_FAILED " + scriptNonce + " 2026-10-09T10:00:00Z daemon",
		"/-dash always",
		"/UPPER Always",
		"/ok.name_1-x unless-stopped",
	}, "\n")+"\n")
	b.set("ps.last", "web\nok.name_1-x\n")
	r := b.run()

	if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	checkLogShape(t, r.log)
	// a forged FAILED in what a program printed is no marker, and neither is a hostile name
	if want := []string{"containerd.io=1.7.27-1", "docker-ce=" + debian28, "docker-ce-cli=" + debian28}; !reflect.DeepEqual(r.run.Previous, want) {
		t.Errorf("previous = %#v, want %#v", r.run.Previous, want)
	}
	for _, line := range strings.Split(r.log, "\n") {
		if strings.HasPrefix(line, "container: ") {
			name := strings.Fields(line)[1]
			if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(name) {
				t.Errorf("a container named %q was logged", name)
			}
		}
	}
	for _, bad := range []string{"reboot", "$(", "two words", "web2", "UPPER", "-dash"} {
		if strings.Contains(strings.ReplaceAll(r.log, forged, ""), bad) {
			t.Errorf("the log holds %q:\n%s", bad, r.log)
		}
	}
	// the container named like a marker is a container, and is told as one
	want := []dockerpkg.NotReturned{{Name: "CASAOS_DOCKER_UPDATE_SUCCESS", RestartPolicy: "always"}}
	if !reflect.DeepEqual(r.run.NotReturned, want) {
		t.Errorf("not returned = %#v, want %#v", r.run.NotReturned, want)
	}
	if n := r.mentions("--all") + r.mentions("reboot"); n != 0 {
		t.Errorf("a hostile id reached docker inspect: %v", r.calls)
	}
}

func TestTheUnitDoesNotReportAVersionThatIsNotOne(t *testing.T) {
	b := newScriptBox(t)
	b.set("version.after", "29.8.0; reboot\n")
	r := b.run()
	checkLogShape(t, r.log)
	if r.run.Terminal != dockerpkg.TerminalFailed || r.run.FailReason != dockerpkg.FailDaemon || strings.Contains(r.log, "reboot") {
		t.Fatalf("run = %#v\n%s", r.run, r.log)
	}
}

func TestTheScriptNeedsItsNonceItsLogAndApt(t *testing.T) {
	for _, name := range []string{"CASAOS_DU_NONCE", "CASAOS_DU_LOG", "CASAOS_DU_APT"} {
		b := newScriptBox(t)
		b.env[name] = unsetEnv
		r := b.run()
		if r.exit == 0 || r.called("apt-get") != 0 || strings.Contains(r.log, "_STARTED") {
			t.Errorf("%s unset: exit = %d, calls = %v, log = %q", name, r.exit, r.calls, r.log)
		}
	}
}

func TestTheUnitsDefaultsAreTheWaitsOfTheSpec(t *testing.T) {
	// no timeout in the environment: 300 s for the daemon, 90 for the containers
	b := newScriptBox(t)
	b.set("broken", "")
	writeFile(t, filepath.Join(b.dir, "bin", "systemctl"), "#!/bin/sh\necho \"systemctl $*\" >> \"$FAKE_DIR/calls.log\"\nexit 0\n", 0o755)
	for _, name := range []string{"CASAOS_DU_DAEMON_TIMEOUT", "CASAOS_DU_RETURN_TIMEOUT", "CASAOS_DU_POLL"} {
		b.env[name] = unsetEnv
	}
	r := b.run()
	if r.run.FailReason != dockerpkg.FailDaemon {
		t.Fatalf("run = %#v", r.run)
	}
	clock, _ := os.ReadFile(filepath.Join(b.dir, "clock"))
	var seconds int
	if _, err := fmt.Sscan(strings.TrimSpace(string(clock)), &seconds); err != nil || seconds < 600 || seconds > 700 {
		t.Errorf("the unit waited %q virtual seconds, want two waits of 300 s (a poll of 2 s)", clock)
	}
}
