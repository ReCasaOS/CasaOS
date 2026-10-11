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
	"sort"
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
# apt run with -y refuses to change what is on hold unless it is told it may
refuses_held() {
	case " $* " in *" --allow-change-held-packages "*) return 1 ;; esac
	for f in "$d"/status/*; do
		[ -f "$f" ] || continue
		case $(cat "$f") in h[!nc]*) return 0 ;; esac
	done
	return 1
}
case " $* " in
*" -s "*)
	cat "$d/plan.txt" 2>/dev/null
	exit "$(cat "$d/sim.rc" 2>/dev/null || echo 0)" ;;
*" --download-only "*)
	if refuses_held "$@"; then
		echo "E: Held packages were changed and -y was used without --allow-change-held-packages."
		exit 100
	fi
	# the owner holds a package while the packages download (download.holds: its names)
	for n in $(cat "$d/download.holds" 2>/dev/null); do echo hi > "$d/status/$n"; done
	exit "$(cat "$d/download.rc" 2>/dev/null || echo 0)" ;;
*" update "*)
	exit 0 ;;
esac
if refuses_held "$@"; then
	echo "E: Held packages were changed and -y was used without --allow-change-held-packages."
	exit 100
fi
rc=$(cat "$d/install.rc" 2>/dev/null || echo 0)
# dpkg sets the selection of every package it unpacks back to install, held or not, with the flag or
# without it (apt passes it nothing of the holds): on an install that goes well that is every package
# that was named, and on one that fails the packages dpkg got to before it did (install.unpacks)
unpacked=$(cat "$d/install.unpacks" 2>/dev/null)
if [ "$rc" -eq 0 ]; then
	for a; do
		case $a in -* | *::*) continue ;; *=*) unpacked="$unpacked ${a%%=*}" ;; esac
	done
fi
for n in $unpacked; do
	if [ -f "$d/status/$n" ]; then echo ii > "$d/status/$n"; fi
done
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
# printf and not echo: dash's echo turns the \n of dpkg-query's format into a line break, busybox's does not
printf '%s\n' "dpkg-query $*" >> "$d/calls.log"
case "$*" in
*Status-Abbrev*)
	# the state of each package that was asked for, as dpkg abbreviates it: want and state, and a
	# blank for the error flag; "hi" is on hold. status.extra is what a hostile dpkg-query might add.
	for n in "$@"; do
		case $n in -*) continue ;; esac
		st=$(cat "$d/status/$n" 2>/dev/null)
		if [ -z "$st" ] && [ -f "$d/dpkg/$n" ]; then st=ii; fi
		if [ -n "$st" ]; then printf '%s   %s\n' "$st" "$n"; fi
	done
	cat "$d/status.extra" 2>/dev/null
	exit 0 ;;
esac
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

// apt-mark records what it was asked, and holds what it is asked to hold: dpkg's selection is the
// status file of the package. It fails as dpkg does while its database is locked: the first
// apt-mark.fails calls, or every call when that is "all".
const fakeAptMark = `#!/bin/sh
d=$FAKE_DIR
echo "apt-mark $*" >> "$d/calls.log"
n=$(cat "$d/apt-mark.calls" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > "$d/apt-mark.calls"
fails=$(cat "$d/apt-mark.fails" 2>/dev/null || echo 0)
if [ "$fails" = all ] || [ "$n" -le "$fails" ]; then
	echo "E: Could not get lock /var/lib/dpkg/lock-frontend" >&2
	exit 100
fi
[ "$1" = hold ] || exit 0
shift
for name; do
	if [ -f "$d/dpkg/$name" ]; then echo hi > "$d/status/$name"; fi
done
exit 0
`

const fakeSleep = `#!/bin/sh
d=$FAKE_DIR
echo "sleep $*" >> "$d/calls.log"
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
		"systemctl": fakeSystemctl, "sleep": fakeSleep, "date": fakeDate, "timeout": fakeTimeout, "apt-mark": fakeAptMark,
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

// run writes the first line the core writes, then runs the script as systemd-run would: with the
// argument the core gives it, after what systemd does to the words of a command.
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
	// the shell is given what systemd makes of the argument, as the unit's environment holds it
	// (see systemd_env_test.go): the script as it is written, if the argument is the right one
	argv := systemdExpandArgv([]string{"/bin/sh", "-c", dockerUpdateScriptArg()}, env)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
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
	if got, want := strings.Join(markerKinds(r.log), " "), "STARTED HELD PREVIOUS DOWNLOADED INSTALLED DAEMON NOTRETURNED HOLDS SUCCESS"; got != want {
		t.Errorf("markers = %s, want %s", got, want)
	}
	// nothing is held, and the unit says so: no hold to say is kept, no word to go ahead of one
	if r.run.Held == nil || len(r.run.Held) != 0 || r.run.Holds == nil || len(r.run.Holds) != 0 || r.run.HoldsKept() {
		t.Errorf("held = %#v, holds = %#v", r.run.Held, r.run.Holds)
	}
	if n := r.mentions("allow-change-held-packages"); n != 0 {
		t.Errorf("apt was told it may change what is held, on a box that holds nothing: %v", r.calls)
	}
	// nothing was on hold, so there is nothing to put back and dpkg is not asked what is held: given no
	// name, dpkg-query would answer for every package on the box
	if n := r.called("apt-mark") + r.mentions("Status-Abbrev"); n != 0 {
		t.Errorf("the unit looked at holds, or held, on a box that holds nothing: %v", r.calls)
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
	if got, want := strings.Join(markerKinds(r.log), " "), "STARTED HELD PREVIOUS DOWNLOADED INSTALLED DAEMON NOTRETURNED HOLDS SUCCESS"; got != want {
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

// The plan line is the core's word, written before the unit exists: whether the update is a major
// jump is decided by the core and not by what the unit prints.
func TestTheScriptNeverWritesAPlanLine(t *testing.T) {
	if strings.Contains(dockerUpdateScript, "PLAN") {
		t.Error("the script mentions a PLAN marker, which only the core writes")
	}
}

// ---- packages on hold ------------------------------------------------------------------------
//
// The unit goes ahead of a hold when it is told to (CASAOS_DU_ALLOW_HELD=1, with the names of the engine's
// packages that the core saw on hold in CASAOS_DU_HELD), exactly as the terminal command with
// --allow-change-held-packages would: that flag is on the guard's simulation, the download and the
// install, and nowhere else. dpkg then drops the hold of every package it unpacks (the fake apt-get does
// what dpkg does, by default: it sets the package back to "ii"), so the unit puts the hold back with
// apt-mark hold, for the packages that were on hold when it started and for those alone, as soon as the
// install call has returned, whatever it said. It says in the log which packages were on hold at its start
// (HELD) and which are at its end (HOLDS).

// hold puts those packages on hold, as dpkg abbreviates it ("hi": want hold, state installed).
func (b *scriptBox) hold(names ...string) {
	b.t.Helper()
	for _, name := range names {
		b.set("status/"+name, "hi\n")
	}
}

// goAheadOfHolds puts those packages on hold and gives the unit what the core gives it for them: their
// names, and the owner's word.
func (b *scriptBox) goAheadOfHolds(names ...string) {
	b.t.Helper()
	b.hold(names...)
	b.env["CASAOS_DU_HELD"] = strings.Join(names, " ")
	b.env["CASAOS_DU_ALLOW_HELD"] = "1"
}

// dpkgSays is dpkg's abbreviation of a package at the end of the run: "hi" is on hold, "ii" is not, and
// "" is a package nothing ever held.
func (b *scriptBox) dpkgSays(name string) string {
	data, err := os.ReadFile(filepath.Join(b.dir, "status", name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

const (
	heldFlag     = "--allow-change-held-packages"
	ownersPins   = "containerd.io=2.1.4-1 docker-ce=" + debian29 + " docker-ce-cli=" + debian29
	simulateHead = "apt-get -s --no-remove -o Debug::NoLocking=true -o Dpkg::Use-Pty=0 install --only-upgrade --no-install-recommends "
	downloadHead = "apt-get -y --no-remove --download-only -o Dpkg::Use-Pty=0 -o DPkg::Lock::Timeout=120 install --only-upgrade --no-install-recommends "
	installHead  = "apt-get -y --no-remove -o Dpkg::Use-Pty=0 -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=120 install --only-upgrade --no-install-recommends "
)

func (r scriptRun) aptCalls() []string {
	var calls []string
	for _, call := range r.calls {
		if strings.HasPrefix(call, "apt-get ") {
			calls = append(calls, call)
		}
	}
	return calls
}

// holdCalls are the calls to apt-mark.
func (r scriptRun) holdCalls() []string {
	var calls []string
	for _, call := range r.calls {
		if strings.HasPrefix(call, "apt-mark ") {
			calls = append(calls, call)
		}
	}
	return calls
}

// indexOfCall is the position of the first (or last) call that has the text in it, -1 for none.
func (r scriptRun) indexOfCall(text string, last bool) int {
	found := -1
	for i, call := range r.calls {
		if strings.Contains(call, text) {
			found = i
			if !last {
				return i
			}
		}
	}
	return found
}

// putBackBeforeAnythingElse: the one thing the unit does after the install call, before it waits for the
// daemon or asks systemd to start it, is to put the hold back.
func (r scriptRun) putBackBeforeAnythingElse(t *testing.T) {
	t.Helper()
	install, put := r.indexOfCall("--force-confold", false), r.indexOfCall("apt-mark", false)
	if install < 0 || put < install {
		t.Errorf("the hold was put back (call %d) before the install call (%d): %v", put, install, r.calls)
		return
	}
	for _, later := range []string{"docker version", "systemctl start"} {
		if i := r.indexOfCall(later, false); i >= 0 && i < put {
			t.Errorf("%q (call %d) came before the hold was put back (call %d): %v", later, i, put, r.calls)
		}
	}
}

func holdLine(kind string, names string) string {
	return "CASAOS_DOCKER_UPDATE_" + kind + " " + scriptNonce + " " + names
}

func TestTheUnitGoesAheadOfAHoldItWasToldToAndPutsItBack(t *testing.T) {
	b := newScriptBox(t)
	b.goAheadOfHolds("docker-ce", "docker-ce-cli")
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess || r.run.DaemonVersion != "29.8.0" {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}

	// the flag is on the three calls of apt that look at or change what is held, and on them alone
	want := []string{
		simulateHead + heldFlag + " " + ownersPins,
		downloadHead + heldFlag + " " + ownersPins,
		installHead + heldFlag + " " + ownersPins,
	}
	if got := r.aptCalls(); !reflect.DeepEqual(got, want) {
		t.Errorf("apt-get calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, call := range r.calls {
		if !strings.HasPrefix(call, "apt-get ") && strings.Contains(call, heldFlag) {
			t.Errorf("the flag is on %q, which is not apt's", call)
		}
	}
	if n := r.mentions(heldFlag); n != 3 {
		t.Errorf("the flag is on %d calls, want exactly the three of apt-get", n)
	}
	if n := r.called("apt-get update"); n != 0 {
		t.Error("the script refreshed the index")
	}

	// dpkg dropped the hold of what it unpacked, and the unit put it back: once, with the tool the owner
	// holds with, for the packages that were held, and without the flag
	if want := []string{"apt-mark hold docker-ce docker-ce-cli"}; !reflect.DeepEqual(r.holdCalls(), want) {
		t.Errorf("apt-mark calls = %v, want %v", r.holdCalls(), want)
	}
	r.putBackBeforeAnythingElse(t)
	for _, name := range []string{"docker-ce", "docker-ce-cli"} {
		if got := b.dpkgSays(name); got != "hi" {
			t.Errorf("dpkg says %q of %s at the end, want hi: the hold is not back", got, name)
		}
	}

	// what the log says: the holds at the start, the holds at the end, the holds in place again
	if got, want := strings.Join(markerKinds(r.log), " "), "STARTED HELD PREVIOUS DOWNLOADED INSTALLED DAEMON NOTRETURNED HOLDS SUCCESS"; got != want {
		t.Errorf("markers = %s, want %s", got, want)
	}
	for _, line := range []string{holdLine("HELD", "docker-ce docker-ce-cli"), holdLine("HOLDS", "docker-ce docker-ce-cli")} {
		if !strings.Contains(r.log, "\n"+line+"\n") {
			t.Errorf("the log lacks %q:\n%s", line, r.log)
		}
	}
	if !reflect.DeepEqual(r.run.Held, []string{"docker-ce", "docker-ce-cli"}) || !reflect.DeepEqual(r.run.Holds, []string{"docker-ce", "docker-ce-cli"}) || !r.run.HoldsKept() {
		t.Errorf("held = %v, holds = %v, kept = %v", r.run.Held, r.run.Holds, r.run.HoldsKept())
	}

	// the unit asked dpkg, for the names the core gave it, before it did anything and after it was done
	query := "dpkg-query -W -f=${db:Status-Abbrev} ${Package}\\n docker-ce docker-ce-cli"
	if r.indexOfCall(query, false) < 0 {
		t.Fatalf("no query of the holds: %v", r.calls)
	}
	if first, apt := r.indexOfCall("Status-Abbrev", false), r.indexOfCall("apt-get", false); first > apt {
		t.Errorf("the holds were first looked at after apt ran: %v", r.calls)
	}
	if last, apt := r.indexOfCall("Status-Abbrev", true), r.indexOfCall("apt-get -y --no-remove -o", true); last < apt {
		t.Errorf("the holds were last looked at before the install: %v", r.calls)
	}
}

// What the fake apt-get does, dpkg does: without the unit's help the hold of what is unpacked is gone.
func TestTheFakeDpkgDropsTheHoldOfWhatItUnpacks(t *testing.T) {
	b := newScriptBox(t)
	b.goAheadOfHolds("docker-ce", "docker-ce-cli")
	writeFile(t, filepath.Join(b.dir, "bin", "apt-mark"), "#!/bin/sh\necho \"apt-mark $*\" >> \"$FAKE_DIR/calls.log\"\nexit 0\n", 0o755)
	r := b.run()
	if r.exit != 0 || b.dpkgSays("docker-ce") != "ii" || b.dpkgSays("docker-ce-cli") != "ii" {
		t.Errorf("exit = %d, dpkg says %q and %q: apt-get -y --allow-change-held-packages leaves nothing on hold", r.exit, b.dpkgSays("docker-ce"), b.dpkgSays("docker-ce-cli"))
	}
}

func TestTheUnitDoesNotChangeAHoldItWasNotToldToGoAheadOf(t *testing.T) {
	// the core gives the unit no names and no word for a box that holds nothing, and the box holds
	// something that came after: apt -y refuses to change a held package, and the update is a download
	// that failed, with nothing changed and the hold where it was
	b := newScriptBox(t)
	b.hold("docker-ce")
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 1 || r.run.Terminal != dockerpkg.TerminalFailed || r.run.FailReason != dockerpkg.FailDownload {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	if n := r.mentions(heldFlag); n != 0 {
		t.Errorf("apt was told it may change what is held without the word: %v", r.calls)
	}
	if changes := r.aptChanges(); len(changes) != 1 || !strings.Contains(changes[0], "--download-only") {
		t.Errorf("apt-get changes = %v: only the download that was refused", changes)
	}
	if _, err := os.Stat(filepath.Join(b.dir, "installed")); err == nil {
		t.Error("something was installed")
	}
	if got, want := strings.Join(markerKinds(r.log), " "), "STARTED HELD PREVIOUS HOLDS FAILED"; got != want {
		t.Errorf("markers = %s, want %s", got, want)
	}
	// it was not told of a hold, so it says none, and it holds nothing: the install was never started
	if r.run.Held == nil || len(r.run.Held) != 0 || r.run.HoldsKept() || len(r.holdCalls()) != 0 || b.dpkgSays("docker-ce") != "hi" {
		t.Errorf("held = %v, holds = %v, apt-mark calls = %v, dpkg says %q", r.run.Held, r.run.Holds, r.holdCalls(), b.dpkgSays("docker-ce"))
	}
}

func TestTheUnitSaysWhenAHoldCouldNotBePutBack(t *testing.T) {
	b := newScriptBox(t)
	b.goAheadOfHolds("docker-ce", "docker-ce-cli")
	b.set("apt-mark.fails", "all")
	r := b.run()
	checkLogShape(t, r.log)
	// the update went through, and what it did to the holds is told: the names are not in HOLDS
	if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	if !strings.Contains(r.log, "\n"+holdLine("HELD", "docker-ce docker-ce-cli")+"\n") || !strings.Contains(r.log, "\n"+holdLine("HOLDS", "-")+"\n") {
		t.Errorf("the log:\n%s", r.log)
	}
	if r.run.HoldsKept() || len(r.run.Held) != 2 || r.run.Holds == nil || len(r.run.Holds) != 0 {
		t.Errorf("held = %v, holds = %v, kept = %v", r.run.Held, r.run.Holds, r.run.HoldsKept())
	}
	// it tried ten times right after the install and ten times once more before saying the holds, three
	// seconds apart, and let go, without a word of its own in the log
	if n := r.called("apt-mark hold docker-ce docker-ce-cli"); n != 20 || len(r.holdCalls()) != 20 {
		t.Errorf("apt-mark was called %d times (%v), want twenty tries", n, r.holdCalls())
	}
	if n := r.called("sleep 3"); n != 18 {
		t.Errorf("the unit paused %d times between the tries, want 18", n)
	}
	if strings.Contains(r.log, "lock-frontend") || strings.Contains(r.log, "apt-mark") {
		t.Errorf("the log says what apt-mark said:\n%s", r.log)
	}
	if got := b.dpkgSays("docker-ce"); got != "ii" {
		t.Errorf("dpkg says %q of docker-ce", got)
	}
}

func TestTheUnitPutsTheHoldBackWhenDpkgIsLockedForAMoment(t *testing.T) {
	b := newScriptBox(t)
	b.goAheadOfHolds("docker-ce", "docker-ce-cli")
	b.set("apt-mark.fails", "2")
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	if n := len(r.holdCalls()); n != 3 {
		t.Errorf("apt-mark was called %d times, want the two that failed and the one that did not", n)
	}
	if n := r.called("sleep 3"); n != 2 {
		t.Errorf("the unit paused %d times, want 2", n)
	}
	if !r.run.HoldsKept() || b.dpkgSays("docker-ce") != "hi" || b.dpkgSays("docker-ce-cli") != "hi" {
		t.Errorf("held = %v, holds = %v, dpkg says %q", r.run.Held, r.run.Holds, b.dpkgSays("docker-ce"))
	}
}

func TestTheUnitPutsTheHoldBackWhenTheInstallFailsAfterDpkgUnpackedSome(t *testing.T) {
	b := newScriptBox(t)
	b.goAheadOfHolds("docker-ce", "docker-ce-cli")
	b.set("install.rc", "100")
	// dpkg got as far as docker-ce, and the daemon is down: it is up again once systemd is asked to start it
	b.set("install.unpacks", "docker-ce")
	b.set("broken", "")
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 1 || r.run.Terminal != dockerpkg.TerminalFailed || r.run.FailReason != dockerpkg.FailInstall {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	if want := []string{"apt-mark hold docker-ce docker-ce-cli"}; !reflect.DeepEqual(r.holdCalls(), want) {
		t.Errorf("apt-mark calls = %v, want %v", r.holdCalls(), want)
	}
	// first thing after the install call, before the daemon is brought back
	r.putBackBeforeAnythingElse(t)
	if r.called("systemctl start") != 1 {
		t.Errorf("systemctl start was not called once: %v", r.calls)
	}
	if got, want := strings.Join(markerKinds(r.log), " "), "STARTED HELD PREVIOUS DOWNLOADED HOLDS FAILED"; got != want {
		t.Errorf("markers = %s, want %s", got, want)
	}
	if !r.run.HoldsKept() || !reflect.DeepEqual(r.run.Holds, []string{"docker-ce", "docker-ce-cli"}) || b.dpkgSays("docker-ce") != "hi" || b.dpkgSays("docker-ce-cli") != "hi" {
		t.Errorf("held = %v, holds = %v, dpkg says %q and %q", r.run.Held, r.run.Holds, b.dpkgSays("docker-ce"), b.dpkgSays("docker-ce-cli"))
	}
}

func TestTheUnitPutsTheHoldBackWhenTheDaemonNeverComesBack(t *testing.T) {
	b := newScriptBox(t)
	b.goAheadOfHolds("docker-ce", "docker-ce-cli")
	b.set("broken", "")
	// systemctl start does not help (a daemon.json that dockerd refuses)
	writeFile(t, filepath.Join(b.dir, "bin", "systemctl"), "#!/bin/sh\necho \"systemctl $*\" >> \"$FAKE_DIR/calls.log\"\nexit 0\n", 0o755)
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 1 || r.run.FailReason != dockerpkg.FailDaemon {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	// the install went through, so the hold was put back at once; fail does not do it a second time
	if want := []string{"apt-mark hold docker-ce docker-ce-cli"}; !reflect.DeepEqual(r.holdCalls(), want) {
		t.Errorf("apt-mark calls = %v, want %v", r.holdCalls(), want)
	}
	r.putBackBeforeAnythingElse(t)
	if got, want := strings.Join(markerKinds(r.log), " "), "STARTED HELD PREVIOUS DOWNLOADED INSTALLED HOLDS FAILED"; got != want {
		t.Errorf("markers = %s, want %s", got, want)
	}
	if !r.run.HoldsKept() || b.dpkgSays("docker-ce") != "hi" || b.dpkgSays("docker-ce-cli") != "hi" {
		t.Errorf("held = %v, holds = %v, dpkg says %q", r.run.Held, r.run.Holds, b.dpkgSays("docker-ce"))
	}
}

// A hold that could not be put back right after the install is tried again by fail, which ends the run:
// dpkg's database may have been locked for longer than ten tries, and is free by the time the daemon
// has been waited for.
func TestTheUnitTriesTheHoldAgainWhenARunEndsThatCouldNotPutItBack(t *testing.T) {
	b := newScriptBox(t)
	b.goAheadOfHolds("docker-ce", "docker-ce-cli")
	b.set("install.rc", "100")
	b.set("install.unpacks", "docker-ce docker-ce-cli")
	b.set("apt-mark.fails", "10")
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 1 || r.run.FailReason != dockerpkg.FailInstall {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	if n := len(r.holdCalls()); n != 11 {
		t.Errorf("apt-mark was called %d times, want the ten that failed after the install and the one from the end of the run", n)
	}
	if !r.run.HoldsKept() || b.dpkgSays("docker-ce") != "hi" || b.dpkgSays("docker-ce-cli") != "hi" {
		t.Errorf("held = %v, holds = %v, dpkg says %q", r.run.Held, r.run.Holds, b.dpkgSays("docker-ce"))
	}
}

// Whatever ends the run, the holds are said before the last line, and they are on hold: the page tells
// the owner whether the hold is in place after a failure too. A run that ended before the install was
// started has not changed a hold, and puts nothing back.
func TestTheUnitSaysTheHoldsWhateverEndsTheRun(t *testing.T) {
	brokenDaemon := func(b *scriptBox) {
		b.set("broken", "")
		writeFile(b.t, filepath.Join(b.dir, "bin", "systemctl"), "#!/bin/sh\necho \"systemctl $*\" >> \"$FAKE_DIR/calls.log\"\nexit 0\n", 0o755)
	}
	cases := map[string]struct {
		setup func(*scriptBox)
		kinds string
		why   string
		// putBack is whether the install call was started, which is when the holds are put back
		putBack bool
	}{
		"the guard refuses":  {func(b *scriptBox) { b.set("plan.txt", ownerContaind+ownerDockerCE+ownerCLI+simLibc) }, "STARTED HELD PREVIOUS GUARD HOLDS FAILED", dockerpkg.FailGuard, false},
		"the download fails": {func(b *scriptBox) { b.set("download.rc", "100") }, "STARTED HELD PREVIOUS HOLDS FAILED", dockerpkg.FailDownload, false},
		"the install fails": {func(b *scriptBox) {
			b.set("install.rc", "100")
			b.set("install.unpacks", "docker-ce docker-ce-cli")
		}, "STARTED HELD PREVIOUS DOWNLOADED HOLDS FAILED", dockerpkg.FailInstall, true},
		"the daemon does not come back": {brokenDaemon, "STARTED HELD PREVIOUS DOWNLOADED INSTALLED HOLDS FAILED", dockerpkg.FailDaemon, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := newScriptBox(t)
			b.goAheadOfHolds("docker-ce", "docker-ce-cli")
			c.setup(b)
			r := b.run()
			checkLogShape(t, r.log)
			if r.exit != 1 || r.run.FailReason != c.why {
				t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
			}
			if got := strings.Join(markerKinds(r.log), " "); got != c.kinds {
				t.Errorf("markers = %s, want %s", got, c.kinds)
			}
			if !r.run.HoldsKept() || !reflect.DeepEqual(r.run.Holds, []string{"docker-ce", "docker-ce-cli"}) || b.dpkgSays("docker-ce") != "hi" || b.dpkgSays("docker-ce-cli") != "hi" {
				t.Errorf("held = %v, holds = %v, dpkg says %q and %q", r.run.Held, r.run.Holds, b.dpkgSays("docker-ce"), b.dpkgSays("docker-ce-cli"))
			}
			if n := len(r.holdCalls()); (n == 1) != c.putBack || n > 1 {
				t.Errorf("apt-mark was called %d times, want %v", n, map[bool]string{true: "once", false: "never"}[c.putBack])
			}
		})
	}
}

// The holds that are put back are the ones the unit saw when it started, and no other: a package that
// was not on hold is never put on hold by the update.
func TestTheUnitPutsBackOnlyTheHoldsItStartedWith(t *testing.T) {
	b := newScriptBox(t)
	// the core saw all three on hold, and the box has two of them on hold by the time the unit looks
	b.hold("docker-ce", "docker-ce-cli")
	b.env["CASAOS_DU_HELD"] = "containerd.io docker-ce docker-ce-cli"
	b.env["CASAOS_DU_ALLOW_HELD"] = "1"
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	if want := []string{"apt-mark hold docker-ce docker-ce-cli"}; !reflect.DeepEqual(r.holdCalls(), want) {
		t.Errorf("apt-mark calls = %v, want %v", r.holdCalls(), want)
	}
	if got := b.dpkgSays("containerd.io"); got != "" {
		t.Errorf("dpkg says %q of containerd.io, which was not on hold: the unit put a hold on it", got)
	}
	for _, line := range []string{holdLine("HELD", "docker-ce docker-ce-cli"), holdLine("HOLDS", "docker-ce docker-ce-cli")} {
		if !strings.Contains(r.log, "\n"+line+"\n") {
			t.Errorf("the log lacks %q:\n%s", line, r.log)
		}
	}
}

// The check reports every installed package of the engine that is on hold, the ones the plan does not
// touch too (an up to date containerd.io): the unit tells and keeps those the same.
func TestTheUnitSaysAndKeepsTheHeldPackagesThatThePlanDoesNotTouch(t *testing.T) {
	b := newScriptBox(t)
	b.set("plan.txt", ownerDockerCE+ownerCLI)
	b.env["CASAOS_DU_PINS"] = "docker-ce=" + debian29 + " docker-ce-cli=" + debian29
	b.env["CASAOS_DU_NAMES"] = "docker-ce docker-ce-cli"
	b.goAheadOfHolds("containerd.io", "docker-ce")
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	for _, line := range []string{holdLine("HELD", "containerd.io docker-ce"), holdLine("HOLDS", "containerd.io docker-ce")} {
		if !strings.Contains(r.log, "\n"+line+"\n") {
			t.Errorf("the log lacks %q:\n%s", line, r.log)
		}
	}
	if want := []string{"apt-mark hold containerd.io docker-ce"}; !reflect.DeepEqual(r.holdCalls(), want) {
		t.Errorf("apt-mark calls = %v, want %v", r.holdCalls(), want)
	}
	if !r.run.HoldsKept() || b.dpkgSays("containerd.io") != "hi" || b.dpkgSays("docker-ce") != "hi" {
		t.Errorf("held = %v, holds = %v, dpkg says %q and %q", r.run.Held, r.run.Holds, b.dpkgSays("containerd.io"), b.dpkgSays("docker-ce"))
	}
}

func TestTheUnitSaysNothingOfHoldsItWasNotAskedAboutBeforeItTrustsItsLists(t *testing.T) {
	// a list that is not trusted is refused before anything is looked at: no HELD, and no HOLDS to go with it
	b := newScriptBox(t)
	b.goAheadOfHolds("docker-ce")
	b.env["CASAOS_DU_PINS"] = "docker-ce=5:29.8.0;touch${IFS}x"
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 1 || r.run.FailReason != dockerpkg.FailGuard || strings.Contains(r.log, "_HELD") || strings.Contains(r.log, "_HOLDS") {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	if r.called("dpkg-query") != 0 || r.called("apt-get") != 0 || r.called("apt-mark") != 0 {
		t.Errorf("something ran: %v", r.calls)
	}
}

// The two words about the holds, which the core gives together (the names, and the owner's word) or not
// at all: anything else is a unit that the core did not make, and nothing of it is looked at or run.
func TestTheUnitRefusesHoldWordsThatTheCoreDoesNotMake(t *testing.T) {
	engine := "containerd.io docker-buildx-plugin docker-ce docker-ce-cli docker-ce-rootless-extras docker-compose-plugin docker-model-plugin"
	cases := map[string]map[string]string{
		"the word and no names":                      {"CASAOS_DU_ALLOW_HELD": "1"},
		"the word and empty names":                   {"CASAOS_DU_ALLOW_HELD": "1", "CASAOS_DU_HELD": ""},
		"the word and names that are only spaces":    {"CASAOS_DU_ALLOW_HELD": "1", "CASAOS_DU_HELD": "   "},
		"names and no word":                          {"CASAOS_DU_HELD": "docker-ce"},
		"names and an empty word":                    {"CASAOS_DU_HELD": "docker-ce", "CASAOS_DU_ALLOW_HELD": ""},
		"a name that is a command":                   {"CASAOS_DU_HELD": "docker-ce;touch${IFS}x", "CASAOS_DU_ALLOW_HELD": "1"},
		"a command after a name":                     {"CASAOS_DU_HELD": "docker-ce ;touch x", "CASAOS_DU_ALLOW_HELD": "1"},
		"a name with a substitution":                 {"CASAOS_DU_HELD": "docker-ce$(touch x)", "CASAOS_DU_ALLOW_HELD": "1"},
		"a substitution that is a name":              {"CASAOS_DU_HELD": "$(reboot)", "CASAOS_DU_ALLOW_HELD": "1"},
		"a name with a variable":                     {"CASAOS_DU_HELD": "${CASAOS_DU_NONCE}", "CASAOS_DU_ALLOW_HELD": "1"},
		"a name with a quote":                        {"CASAOS_DU_HELD": "docker-ce'x", "CASAOS_DU_ALLOW_HELD": "1"},
		"a glob":                                     {"CASAOS_DU_HELD": "docker-*", "CASAOS_DU_ALLOW_HELD": "1"},
		"a glob that is every package":               {"CASAOS_DU_HELD": "*", "CASAOS_DU_ALLOW_HELD": "1"},
		"a question mark":                            {"CASAOS_DU_HELD": "docker-c?", "CASAOS_DU_ALLOW_HELD": "1"},
		"a class":                                    {"CASAOS_DU_HELD": "docker-[c]e", "CASAOS_DU_ALLOW_HELD": "1"},
		"a name in upper case":                       {"CASAOS_DU_HELD": "Docker-CE", "CASAOS_DU_ALLOW_HELD": "1"},
		"a name that is a flag":                      {"CASAOS_DU_HELD": "--help", "CASAOS_DU_ALLOW_HELD": "1"},
		"a flag after a name":                        {"CASAOS_DU_HELD": "docker-ce -oAPT::Foo=1", "CASAOS_DU_ALLOW_HELD": "1"},
		"a name that starts with a dot":              {"CASAOS_DU_HELD": ".docker-ce", "CASAOS_DU_ALLOW_HELD": "1"},
		"a name with an architecture":                {"CASAOS_DU_HELD": "docker-ce:amd64", "CASAOS_DU_ALLOW_HELD": "1"},
		"a pin":                                      {"CASAOS_DU_HELD": "docker-ce=5:29.8.0", "CASAOS_DU_ALLOW_HELD": "1"},
		"a package that is not the engine's":         {"CASAOS_DU_HELD": "libc6", "CASAOS_DU_ALLOW_HELD": "1"},
		"a package that is the engine's and another": {"CASAOS_DU_HELD": "docker-ce libc6", "CASAOS_DU_ALLOW_HELD": "1"},
		"a distribution's docker":                    {"CASAOS_DU_HELD": "docker.io", "CASAOS_DU_ALLOW_HELD": "1"},
		"a name that only holds an engine's":         {"CASAOS_DU_HELD": "docker-ce-cli-extra", "CASAOS_DU_ALLOW_HELD": "1"},
		"a name twice":                               {"CASAOS_DU_HELD": "docker-ce docker-ce", "CASAOS_DU_ALLOW_HELD": "1"},
		"more names than the engine has packages":    {"CASAOS_DU_HELD": engine + " libc6", "CASAOS_DU_ALLOW_HELD": "1"},
		"names on two lines":                         {"CASAOS_DU_HELD": "docker-ce\ndocker-ce-cli", "CASAOS_DU_ALLOW_HELD": "1"},
		"names with a tab":                           {"CASAOS_DU_HELD": "docker-ce\tdocker-ce-cli", "CASAOS_DU_ALLOW_HELD": "1"},
		"a new line":                                 {"CASAOS_DU_HELD": "\n", "CASAOS_DU_ALLOW_HELD": "1"},
		// a name among those the unit may touch is not a name the unit holds, unless it is made of what a
		// package's name is (the names it may touch are checked for what a pin may hold, which is more)
		"a name that may be touched, in upper case":        {"CASAOS_DU_HELD": "Nftables", "CASAOS_DU_ALLOW_HELD": "1", "CASAOS_DU_NAMES": depsNames + " Nftables"},
		"a name that may be touched, with an architecture": {"CASAOS_DU_HELD": "nftables:amd64", "CASAOS_DU_ALLOW_HELD": "1", "CASAOS_DU_NAMES": depsNames + " nftables:amd64"},
		"a name that may be touched, with a version":       {"CASAOS_DU_HELD": "nftables=1", "CASAOS_DU_ALLOW_HELD": "1", "CASAOS_DU_NAMES": depsNames + " nftables=1"},
		"a name that may be touched and is a flag":         {"CASAOS_DU_HELD": "-oAPT", "CASAOS_DU_ALLOW_HELD": "1", "CASAOS_DU_NAMES": depsNames + " -oAPT"},
		"a name that may be touched, with a leading dot":   {"CASAOS_DU_HELD": ".nft", "CASAOS_DU_ALLOW_HELD": "1", "CASAOS_DU_NAMES": depsNames + " .nft"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			b := newScriptBox(t)
			// a box that does hold docker-ce: a unit that took the words would go ahead
			b.hold("docker-ce")
			b.env = env
			r := b.run()
			checkLogShape(t, r.log)
			if r.exit != 1 || r.run.Terminal != dockerpkg.TerminalFailed || r.run.FailReason != dockerpkg.FailGuard {
				t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
			}
			if !strings.Contains(r.log, "\nCASAOS_DOCKER_UPDATE_GUARD "+scriptNonce+" plan\n") {
				t.Errorf("no GUARD marker:\n%s", r.log)
			}
			// nothing was looked at or run, and no word was taken for a name
			if r.called("apt-get") != 0 || r.called("dpkg-query") != 0 || r.called("docker") != 0 || r.called("apt-mark") != 0 || strings.Contains(r.log, "_HELD") || strings.Contains(r.log, "_HOLDS") {
				t.Errorf("the script ran something: %v\n%s", r.calls, r.log)
			}
			if _, err := os.Stat(filepath.Join(b.dir, "x")); err == nil {
				t.Error("a command in a name ran")
			}
		})
	}
}

func TestTheUnitRefusesAWordThatIsNotOneAndRunsNothing(t *testing.T) {
	for _, word := range []string{"0", "yes", "true", "11", " 1", "1 ", "--allow-change-held-packages", "1\n1", "$(reboot)", "1;touch x", "${CASAOS_DU_NONCE}"} {
		b := newScriptBox(t)
		b.hold("docker-ce")
		b.env["CASAOS_DU_HELD"] = "docker-ce"
		b.env["CASAOS_DU_ALLOW_HELD"] = word
		r := b.run()
		checkLogShape(t, r.log)
		if r.exit != 1 || r.run.Terminal != dockerpkg.TerminalFailed || r.run.FailReason != dockerpkg.FailGuard {
			t.Errorf("%q: exit = %d, run = %#v\n%s", word, r.exit, r.run, r.log)
			continue
		}
		if !strings.Contains(r.log, "\nCASAOS_DOCKER_UPDATE_GUARD "+scriptNonce+" plan\n") {
			t.Errorf("%q: no GUARD marker:\n%s", word, r.log)
		}
		// nothing was looked at, and no word was taken for one
		if r.called("apt-get") != 0 || r.called("dpkg-query") != 0 || r.called("docker") != 0 || strings.Contains(r.log, "_HELD") {
			t.Errorf("%q: the script ran something: %v", word, r.calls)
		}
	}

	// no word and no names, empty or not there at all, is the start it has always been; so is a list of
	// names that is only spaces, which says none and is not asked about
	for _, env := range []map[string]string{
		{"CASAOS_DU_ALLOW_HELD": "", "CASAOS_DU_HELD": ""},
		{"CASAOS_DU_ALLOW_HELD": unsetEnv, "CASAOS_DU_HELD": unsetEnv},
		{"CASAOS_DU_HELD": "  "},
	} {
		b := newScriptBox(t)
		b.env = env
		r := b.run()
		if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess || r.mentions(heldFlag) != 0 || r.called("apt-mark") != 0 || r.mentions("Status-Abbrev") != 0 {
			t.Errorf("%v: exit = %d, run = %#v, calls = %v", env, r.exit, r.run, r.calls)
		}
	}
}

// The names the unit takes are the engine's, and those among the names it may touch.
func TestTheUnitTakesAHeldNameThatIsOneItMayTouch(t *testing.T) {
	b := newDependenciesBox(t)
	b.env["CASAOS_DU_HELD"] = "nftables"
	b.env["CASAOS_DU_ALLOW_HELD"] = "1"
	r := b.run()
	checkLogShape(t, r.log)
	// nftables is a new package: not installed, so not on hold, and nothing to put back
	if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess || r.mentions(heldFlag) != 3 || r.called("apt-mark") != 0 {
		t.Fatalf("exit = %d, run = %#v, calls = %v\n%s", r.exit, r.run, r.calls, r.log)
	}
}

// The engine's names, which the script cannot read from the core, are the core's.
func TestTheScriptsEngineNamesAreTheEngines(t *testing.T) {
	m := regexp.MustCompile(`(?s)engine_name\(\) \{\n\tcase \$1 in\n\t([^)]*)\) return 0 ;;\n\tesac`).FindStringSubmatch(dockerUpdateScript)
	if m == nil {
		t.Fatal("the script has no engine_name")
	}
	got := strings.Split(m[1], " | ")
	want := append([]string{}, dockerpkg.EngineNames...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the script's engine names are %v, the engine's are %v", got, want)
	}
}

func TestTheUnitCountsOnlyWhatIsOnHoldAndInstalledAmongTheNamesTheCoreSaw(t *testing.T) {
	// a package that is held and not installed ("hn", "hc") is not a hold the update can lose; a package
	// that is installed and not held ("ii", "iF") is not a hold at all
	b := newScriptBox(t)
	b.set("status/containerd.io", "hn\n")
	b.set("status/docker-ce", "hc\n")
	b.set("status/docker-ce-cli", "iF\n")
	b.env["CASAOS_DU_HELD"] = "containerd.io docker-ce docker-ce-cli"
	b.env["CASAOS_DU_ALLOW_HELD"] = "1"
	r := b.run()
	checkLogShape(t, r.log)
	if !strings.Contains(r.log, "\n"+holdLine("HELD", "-")+"\n") || !strings.Contains(r.log, "\n"+holdLine("HOLDS", "-")+"\n") {
		t.Errorf("the log:\n%s", r.log)
	}
	// the core said so, so the flag is there; there is nothing to put back
	if r.mentions(heldFlag) != 3 || r.called("apt-mark") != 0 {
		t.Errorf("flag on %d calls, apt-mark called %d times: %v", r.mentions(heldFlag), r.called("apt-mark"), r.calls)
	}
}

func TestTheUnitSaysHoldsOnlyAsNamesThatAreItsOwn(t *testing.T) {
	b := newScriptBox(t)
	forged := holdLine("FAILED", "2026-10-09T10:00:00Z daemon")
	b.set("status.extra", strings.Join([]string{
		"hi  evil;reboot",
		"hi  $(reboot)",
		"hi  Docker-CE",
		"hi  -x",
		"hi  libc6", // a package it was not asked about
		"hi  docker-ce docker-ce-cli",
		"hi  docker-ce:armhf",
		"hi  ",
		"hi  " + forged,
		forged,
		"hi",
		"",
	}, "\n"))
	b.goAheadOfHolds("docker-ce")
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	for _, line := range []string{holdLine("HELD", "docker-ce"), holdLine("HOLDS", "docker-ce")} {
		if !strings.Contains(r.log, "\n"+line+"\n") {
			t.Errorf("the log lacks %q:\n%s", line, r.log)
		}
	}
	for _, bad := range []string{"reboot", "libc6", "Docker-CE", "armhf"} {
		if strings.Contains(r.log, bad) {
			t.Errorf("the log holds %q:\n%s", bad, r.log)
		}
	}
	// and what is put back is what was asked about, and nothing it was told by what dpkg-query printed
	if want := []string{"apt-mark hold docker-ce"}; !reflect.DeepEqual(r.holdCalls(), want) {
		t.Errorf("apt-mark calls = %v, want %v", r.holdCalls(), want)
	}
	if !r.run.HoldsKept() || !reflect.DeepEqual(r.run.Held, []string{"docker-ce"}) {
		t.Errorf("held = %v, holds = %v", r.run.Held, r.run.Holds)
	}
}

// The put back that was made right after the install may not have worked (dpkg was locked for longer than
// ten tries). The daemon and the containers are waited for next, which takes as long as it takes, and a run
// that fails has a second go at the hold from fail: a run that goes through has one too, just before it says
// the holds, or the owner's hold is lost for good on a run that is reported a success.
func TestTheUnitPutsTheHoldBackOnceMoreBeforeItSaysTheHoldsOfARunThatWentThrough(t *testing.T) {
	b := newScriptBox(t)
	b.goAheadOfHolds("docker-ce", "docker-ce-cli")
	b.set("apt-mark.fails", "10")
	r := b.run()
	checkLogShape(t, r.log)
	if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess || r.run.DaemonVersion != "29.8.0" {
		t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
	}
	// the ten tries that failed after the install, and the one of the end of the run that did not
	hold := "apt-mark hold docker-ce docker-ce-cli"
	if n := len(r.holdCalls()); n != 11 || r.called(hold) != 11 {
		t.Errorf("apt-mark was called %d times (%v), want the ten that failed and the one that worked", n, r.holdCalls())
	}
	if n := r.called("sleep 3"); n != 9 {
		t.Errorf("the unit paused %d times between the tries, want 9: the second go is not made again after it worked", n)
	}
	// the last one comes after the containers were waited for, and the holds are said after it
	if last, looked := r.indexOfCall("apt-mark", true), r.indexOfCall("docker ps --format", true); last < looked {
		t.Errorf("the last put back (call %d) was before the containers were looked at (call %d): %v", last, looked, r.calls)
	}
	if got, want := strings.Join(markerKinds(r.log), " "), "STARTED HELD PREVIOUS DOWNLOADED INSTALLED DAEMON NOTRETURNED HOLDS SUCCESS"; got != want {
		t.Errorf("markers = %s, want %s", got, want)
	}
	if !strings.Contains(r.log, "\n"+holdLine("HOLDS", "docker-ce docker-ce-cli")+"\n") {
		t.Errorf("the log does not say the holds are back:\n%s", r.log)
	}
	if !r.run.HoldsKept() || b.dpkgSays("docker-ce") != "hi" || b.dpkgSays("docker-ce-cli") != "hi" {
		t.Errorf("held = %v, holds = %v, dpkg says %q and %q", r.run.Held, r.run.Holds, b.dpkgSays("docker-ce"), b.dpkgSays("docker-ce-cli"))
	}
}

// The flag is global to the three calls of apt: it lets apt change every package among the names that is on
// hold, and dpkg drops the hold of each one it unpacks. A package that is on hold and that the core did not
// list (the owner held it between the check and the start) is one the unit would not put back, and would
// not say: the run is refused, before apt is asked for anything, and the log says nothing of holds.
func TestTheUnitRefusesToGoAheadOfAHoldItWouldNotPutBack(t *testing.T) {
	notListed := func(b *scriptBox, held []string, listed string) {
		b.hold(held...)
		b.env["CASAOS_DU_HELD"] = listed
		b.env["CASAOS_DU_ALLOW_HELD"] = "1"
	}
	cases := map[string]func(*scriptBox){
		"a pinned package on hold, and not listed": func(b *scriptBox) { notListed(b, []string{"docker-ce", "docker-ce-cli"}, "docker-ce") },
		"the one that is listed is not the one on hold": func(b *scriptBox) {
			notListed(b, []string{"docker-ce-cli"}, "docker-ce")
		},
		"the listed package is not on hold, a pinned one is": func(b *scriptBox) {
			notListed(b, []string{"docker-ce"}, "containerd.io")
		},
		"a new dependency that is installed, on hold, and not listed": func(b *scriptBox) {
			b.set("plan.txt", ownerNewDependencies+ownerContaind+ownerDockerCE+ownerCLI)
			b.env["CASAOS_DU_NAMES"] = depsNames
			b.set("dpkg/nftables", "nftables=0.9.8-3.1\n")
			notListed(b, []string{"docker-ce", "nftables"}, "docker-ce")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			b := newScriptBox(t)
			setup(b)
			r := b.run()
			checkLogShape(t, r.log)
			if r.exit != 1 || r.run.Terminal != dockerpkg.TerminalFailed || r.run.FailReason != dockerpkg.FailGuard {
				t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
			}
			if got, want := strings.Join(markerKinds(r.log), " "), "STARTED GUARD FAILED"; got != want {
				t.Errorf("markers = %s, want %s: a run refused before it says the holds says none", got, want)
			}
			if !strings.Contains(r.log, "\nCASAOS_DOCKER_UPDATE_GUARD "+scriptNonce+" plan\n") {
				t.Errorf("no GUARD marker:\n%s", r.log)
			}
			if r.called("apt-get") != 0 || r.called("apt-mark") != 0 || r.called("docker") != 0 || r.called("systemctl") != 0 {
				t.Errorf("the unit asked apt, docker or systemd for something: %v", r.calls)
			}
			if _, err := os.Stat(filepath.Join(b.dir, "installed")); err == nil {
				t.Error("something was installed")
			}
			if got := b.dpkgSays("docker-ce"); got != "hi" && got != "" {
				t.Errorf("dpkg says %q of docker-ce: the hold was touched", got)
			}
		})
	}
}

// The look at what is on hold is taken twice: before apt is asked for anything, and again once the download
// is done and nothing has been installed yet. The download takes as long as the link takes, and the owner may
// hold another package among the names in the meantime. The flag is global to the install call, so apt would
// change that package, dpkg would drop its hold, and the unit, which puts back only the holds it started
// with, would not put it back and would not say. Refused at the second look the run is as clean as at the
// first: nothing is installed, and there is nothing to put back (the fake apt-get sets download.holds while
// it downloads).
func TestTheUnitRefusesAHoldThatWasSetWhileTheDownloadRan(t *testing.T) {
	refused := []string{
		simulateHead + heldFlag + " " + ownersPins,
		downloadHead + heldFlag + " " + ownersPins,
	}
	for _, c := range []struct {
		name  string
		setup func(*scriptBox)
		kinds string
		apt   []string
	}{
		{"set before the download: refused at the first look, as it always was", func(b *scriptBox) {
			b.goAheadOfHolds("docker-ce")
			b.hold("docker-ce-cli")
		}, "STARTED GUARD FAILED", nil},
		{"set during the download, on a package the core did not list", func(b *scriptBox) {
			b.goAheadOfHolds("docker-ce")
			b.set("download.holds", "docker-ce-cli\n")
		}, "STARTED HELD PREVIOUS DOWNLOADED GUARD HOLDS FAILED", refused},
		// the core listed it, and it was not on hold when the unit started: it is not among the holds the
		// unit puts back, and listed or not it is one it would lose
		{"set during the download, on a package the core listed that was not on hold at the start", func(b *scriptBox) {
			b.goAheadOfHolds("docker-ce")
			b.env["CASAOS_DU_HELD"] = "docker-ce docker-ce-cli"
			b.set("download.holds", "docker-ce-cli\n")
		}, "STARTED HELD PREVIOUS DOWNLOADED GUARD HOLDS FAILED", refused},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newScriptBox(t)
			c.setup(b)
			r := b.run()
			checkLogShape(t, r.log)
			if r.exit != 1 || r.run.Terminal != dockerpkg.TerminalFailed || r.run.FailReason != dockerpkg.FailGuard {
				t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
			}
			if got := strings.Join(markerKinds(r.log), " "); got != c.kinds {
				t.Errorf("markers = %s, want %s", got, c.kinds)
			}
			if !strings.Contains(r.log, "\nCASAOS_DOCKER_UPDATE_GUARD "+scriptNonce+" plan\n") {
				t.Errorf("no GUARD marker:\n%s", r.log)
			}
			if got, want := strings.Join(r.aptCalls(), "\n"), strings.Join(c.apt, "\n"); got != want {
				t.Errorf("apt-get calls:\n%s\nwant:\n%s", got, want)
			}
			// the install never started: nothing installed, nothing to put back, the daemon left alone
			if r.mentions("--force-confold") != 0 || r.called("apt-mark") != 0 || r.called("docker version") != 0 || r.called("systemctl") != 0 {
				t.Errorf("the unit went on with the install: %v", r.calls)
			}
			if _, err := os.Stat(filepath.Join(b.dir, "installed")); err == nil {
				t.Error("something was installed")
			}
			if got := b.dpkgSays("docker-ce"); got != "hi" {
				t.Errorf("dpkg says %q of docker-ce: the hold was touched", got)
			}
		})
	}
}

// The other side of it: the unit goes ahead when every package among the names that is on hold (and
// installed) is one the core listed, whatever else the box holds.
func TestTheUnitGoesAheadWhenEveryHoldAmongTheNamesIsListed(t *testing.T) {
	cases := map[string]func(*scriptBox){
		"all of them, all of them listed": func(b *scriptBox) { b.goAheadOfHolds("containerd.io", "docker-ce", "docker-ce-cli") },
		"one of them, listed with one that is not on hold": func(b *scriptBox) {
			b.hold("docker-ce")
			b.env["CASAOS_DU_HELD"] = "containerd.io docker-ce"
			b.env["CASAOS_DU_ALLOW_HELD"] = "1"
		},
		// not installed is not a hold the update can lose
		"one that is on hold and not installed": func(b *scriptBox) {
			b.set("status/docker-ce-cli", "hn\n")
			b.goAheadOfHolds("docker-ce")
		},
		// a package the plan does not name is not changed by it, on hold or not
		"a package that is not among the names, on hold": func(b *scriptBox) {
			b.hold("libc6", "docker-buildx-plugin")
			b.goAheadOfHolds("docker-ce")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			b := newScriptBox(t)
			setup(b)
			r := b.run()
			checkLogShape(t, r.log)
			if r.exit != 0 || r.run.Terminal != dockerpkg.TerminalSuccess || !r.run.HoldsKept() {
				t.Fatalf("exit = %d, run = %#v\n%s", r.exit, r.run, r.log)
			}
		})
	}
}
