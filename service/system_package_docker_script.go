package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// The unit that updates Docker runs a script that is a constant: nothing that comes from the
// page, from apt or from Docker is ever put into its text. What varies travels in the unit's
// environment (systemd-run --setenv), after the core validated it, and the script treats it as
// data: it is quoted, or split into the words it was validated to be made of, and never eval'd.
//
// Two lists travel: the pins (name=version) of the packages to upgrade, which are all the
// script names to apt, and the names of every package apt may install, which are the pins' and
// those of the dependencies of the new engine that the box does not have yet (nftables, for
// Docker 29). The pins bound what is upgraded, the names bound what is installed.

const (
	// systemDockerDaemonTimeout is how long the unit waits for the daemon after the install.
	systemDockerDaemonTimeout = 300
	// systemDockerReturnTimeout is how long it waits for the containers that start again by
	// themselves.
	systemDockerReturnTimeout = 90
	// systemDockerPoll is the seconds between two looks.
	systemDockerPoll = 2
)

// dockerUpdateScript is run as `/bin/sh -c <this>`. It is POSIX sh, for dash as well as busybox
// ash: no arrays, no [[ ]], no <<<, no local, no pipefail. It is a raw string, which Go
// reads without the carriage returns of a CRLF checkout. It is not handed to systemd as it is:
// see dockerUpdateScriptArg.
//
// Every line it writes in the log that is the unit's word is "CASAOS_DOCKER_UPDATE_<KIND>
// <nonce> ...", one KIND of dockerpkg.ParseRun, and the last line is the one terminal marker.
// What apt, dpkg or Docker print is never given such a start.
const dockerUpdateScript = `set -f
: "${CASAOS_DU_NONCE:?}" "${CASAOS_DU_LOG:?}" "${CASAOS_DU_APT:?}"
exec >> "$CASAOS_DU_LOG" 2>&1

poll=${CASAOS_DU_POLL:-2}
daemon_timeout=${CASAOS_DU_DAEMON_TIMEOUT:-300}
return_timeout=${CASAOS_DU_RETURN_TIMEOUT:-90}

ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
mark() { printf 'CASAOS_DOCKER_UPDATE_%s %s%s\n' "$1" "$CASAOS_DU_NONCE" "${2:+ $2}"; }
fail() { mark FAILED "$(ts) $1"; exit 1; }
# A docker command waits for ever on a socket whose service will not start: bound them all.
bound() { n=$1; shift; if command -v timeout >/dev/null 2>&1; then timeout "$n" "$@"; else "$@"; fi; }

# wait_daemon sets $running to the version the daemon reports; it fails after the timeout.
wait_daemon() {
	deadline=$(( $(date +%s) + daemon_timeout ))
	while :; do
		running=$(bound 20 docker version --format '{{.Server.Version}}' 2>/dev/null)
		case $running in
		'' | [!0-9A-Za-z]* | *[!0-9A-Za-z.+~_-]*) ;;
		*) return 0 ;;
		esac
		[ "$(date +%s)" -lt "$deadline" ] || return 1
		sleep "$poll"
	done
}

mark STARTED "$(ts)"

# The lists below are split into words: there must be some, and nothing in them but what a pin
# or a name is made of. Any doubt refuses, before anything is looked at.
pins=$(printf '%s' "$CASAOS_DU_PINS" | tr -d ' ')
names=$(printf '%s' "$CASAOS_DU_NAMES" | tr -d ' ')
case "$pins$names" in *[!A-Za-z0-9.+~:=-]*) pins= ;; esac
if [ -z "$pins" ] || [ -z "$names" ]; then
	mark GUARD plan
	fail guard
fi

# What is installed now (what a rollback would put back) and what runs now. Only text of the
# shape of a package pin or of a container's name and policy gets into the log.
previous=
for name in $CASAOS_DU_NAMES; do
	for pin in $(dpkg-query -W -f='${Package}=${Version}\n' "$name" 2>/dev/null | grep -E '^[a-z0-9][a-z0-9+.-]*=([0-9]+:)?[0-9][A-Za-z0-9.+~-]*$'); do
		previous="$previous $pin"
	done
done
mark PREVIOUS "${previous# }"

containers=
ids=$(bound 20 docker ps -q 2>/dev/null | grep -E '^[0-9a-f]{12,64}$')
if [ -n "$ids" ]; then
	containers=$(bound 20 docker inspect --format '{{.Name}} {{.HostConfig.RestartPolicy.Name}}' $ids 2>/dev/null | sed 's#^/##' | grep -E '^[A-Za-z0-9][A-Za-z0-9_.-]* [a-z-]*$')
fi
while read -r name policy; do
	[ -n "$name" ] && printf 'container: %s %s\n' "$name" "$policy"
done <<EOF
$containers
EOF

# The guard: what apt would do now, with the pins the owner confirmed, must be an upgrade of
# the pinned packages, and the installation of the new packages the owner was told of (the names
# the core approved), and nothing else. A simulation that fails says nothing, and refuses.
ok=1
sim=$("$CASAOS_DU_APT" -s --no-remove -o Debug::NoLocking=true -o Dpkg::Use-Pty=0 install --only-upgrade --no-install-recommends $CASAOS_DU_PINS 2>&1) || ok=0
upgrades=
for pin in $CASAOS_DU_PINS; do
	upgrades="$upgrades ${pin%%=*}"
done
while read -r kind pkg rest; do
	case $kind in
	Inst)
		case " $CASAOS_DU_NAMES " in *" $pkg "*) ;; *) ok=0 ;; esac
		# an upgrade names the version it replaces, and is of a package that was pinned; a
		# package that is new does not, and is any of the names (a package that was new when the
		# plan was made and is installed now is a plan that changed)
		case $rest in
		'['*) case "$upgrades " in *" $pkg "*) ;; *) ok=0 ;; esac ;;
		esac
		;;
	Remv | Purg) ok=0 ;;
	esac
done <<EOF
$sim
EOF
if [ "$ok" -ne 1 ]; then
	printf '%s\n' "$sim"
	mark GUARD plan
	fail guard
fi

# Download first, while the apps run: a failure here has changed nothing.
"$CASAOS_DU_APT" -y --no-remove --download-only -o Dpkg::Use-Pty=0 -o DPkg::Lock::Timeout=120 install --only-upgrade --no-install-recommends $CASAOS_DU_PINS || fail download
mark DOWNLOADED "$(ts)"

if ! "$CASAOS_DU_APT" -y --no-remove -o Dpkg::Use-Pty=0 -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=120 install --only-upgrade --no-install-recommends $CASAOS_DU_PINS; then
	# dpkg may have stopped the daemon and left it down: one bounded try to bring it back
	bound 120 systemctl start docker.socket docker.service
	wait_daemon
	fail install
fi
mark INSTALLED "$(ts)"

if ! wait_daemon; then
	bound 120 systemctl start docker.socket docker.service
	wait_daemon || fail daemon
fi
mark DAEMON "$running"
# Packages installed and a daemon that is still the old one (policy-rc.d, say): the owner
# restarts it. Nothing here restarts Docker.
outcome=SUCCESS
if [ -n "$CASAOS_DU_TO" ] && [ "$running" != "$CASAOS_DU_TO" ]; then
	outcome=RESTART_PENDING
fi

# The containers that were running: wait for those that start again by themselves (a policy
# of "no" or none does not come back, and is not waited for), then say which are missing.
deadline=$(( $(date +%s) + return_timeout ))
while :; do
	missing=
	waiting=
	up=$(bound 20 docker ps --format '{{.Names}}' 2>/dev/null)
	while read -r name policy; do
		[ -n "$name" ] || continue
		printf '%s\n' "$up" | grep -Fxq -- "$name" && continue
		missing="$missing $name:$policy"
		case $policy in '' | no) ;; *) waiting=1 ;; esac
	done <<EOF
$containers
EOF
	[ -n "$waiting" ] || break
	[ "$(date +%s)" -lt "$deadline" ] || break
	sleep "$poll"
done
for pair in $missing; do
	name=${pair%%:*}
	policy=${pair#*:}
	mark NOTRETURNED "$name${policy:+ $policy}"
done

mark "$outcome" "$(ts)"
exit 0
`

// dockerUpdateScriptArg is the script as the argument of `sh -c`. systemd expands the words of a
// unit's command line before it runs it: a ${NAME} becomes the value of NAME in the unit's
// environment, or nothing at all when there is none, and only a doubled $$ is left as a $. The
// script is full of dollars (${Package}, ${previous# }, ${pin%%=*}, ...), so every one of them is
// doubled, and the shell is given the script as it was written. `systemd-run
// --expand-environment=no` would do, but it only exists from systemd 254, and the boxes run 247,
// 249 and 252. systemd_env_test.go holds a port of what systemd does, and checks the round trip.
func dockerUpdateScriptArg() string {
	return strings.ReplaceAll(dockerUpdateScript, "$", "$$")
}

var (
	// dockerUpdateToPattern is the engine version the unit expects the daemon to report.
	dockerUpdateToPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)
	// dockerUpdatePathPattern is what a path in the unit's environment is made of: absolute, no
	// control character and no space, since the script holds it in quotes and nothing else.
	dockerUpdatePathPattern = regexp.MustCompile(`^/[^\x00-\x20\x7f]*$`)
)

// dockerUpdateArgs are the arguments of systemd-run that start the unit. Everything the script
// needs goes in as --setenv, and is checked here first: the nonce, the paths, the pins (the
// upgrades, of the engine's packages) and the names (the pins' and the new packages', which
// dockerpkg already vetted: this asks again, since it is the last stop before a root shell),
// and the engine version expected.
func dockerUpdateArgs(nonce, logPath, aptPath string, pins, names []string, to string) ([]string, error) {
	switch {
	case !dockerpkg.ValidNonce(nonce):
		return nil, errors.New("the Docker update's nonce is not a nonce")
	case !dockerUpdatePathPattern.MatchString(logPath) || filepath.Clean(logPath) != logPath:
		return nil, fmt.Errorf("%q is not a path for the Docker update's log", logPath)
	case !dockerUpdatePathPattern.MatchString(aptPath):
		return nil, fmt.Errorf("%q is not a path for apt-get", aptPath)
	case to != "" && !dockerUpdateToPattern.MatchString(to):
		return nil, fmt.Errorf("%q is not an engine version", to)
	case len(pins) == 0:
		return nil, errors.New("the Docker update has no packages to upgrade")
	case len(names) > len(dockerpkg.EngineNames)+dockerpkg.MaxNewPackages:
		return nil, errors.New("the Docker update has more packages than it allows")
	}
	pinned := map[string]bool{}
	for _, pin := range pins {
		name, version, found := strings.Cut(pin, "=")
		if !found || !dockerpkg.ValidName(name) || !dockerpkg.IsEngineName(name) || !dockerpkg.ValidVersion(version) || pinned[name] {
			return nil, fmt.Errorf("%q is not a pin of the engine's packages", pin)
		}
		pinned[name] = true
	}
	// every pinned package is a name, and the names that are not are the new packages: valid, at
	// most dockerpkg.MaxNewPackages of them, none of them a distribution's Docker
	seen := map[string]bool{}
	var fresh int
	for _, name := range names {
		if seen[name] || !dockerpkg.ValidName(name) {
			return nil, fmt.Errorf("%q is not a package the Docker update may install", name)
		}
		seen[name] = true
		if !pinned[name] {
			if fresh++; fresh > dockerpkg.MaxNewPackages || dockerpkg.IsDistributionDocker(name) {
				return nil, fmt.Errorf("%q is not a package the Docker update may install", name)
			}
		}
	}
	for name := range pinned {
		if !seen[name] {
			return nil, fmt.Errorf("%q is pinned and is not among the names", name)
		}
	}
	return []string{
		"--quiet",
		"--no-block",
		"--collect",
		"--unit=" + systemDockerUpdateUnit,
		"--property=Type=exec",
		"--description=CasaOS Docker update",
		"--setenv=DEBIAN_FRONTEND=noninteractive",
		"--setenv=CASAOS_DU_NONCE=" + nonce,
		"--setenv=CASAOS_DU_LOG=" + logPath,
		"--setenv=CASAOS_DU_APT=" + aptPath,
		"--setenv=CASAOS_DU_PINS=" + strings.Join(pins, " "),
		"--setenv=CASAOS_DU_NAMES=" + strings.Join(names, " "),
		"--setenv=CASAOS_DU_TO=" + to,
		"--setenv=CASAOS_DU_DAEMON_TIMEOUT=" + strconv.Itoa(systemDockerDaemonTimeout),
		"--setenv=CASAOS_DU_RETURN_TIMEOUT=" + strconv.Itoa(systemDockerReturnTimeout),
		"--setenv=CASAOS_DU_POLL=" + strconv.Itoa(systemDockerPoll),
		"/bin/sh",
		"-c",
		dockerUpdateScriptArg(),
	}, nil
}
