package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// The unit that updates Docker runs a script that is a constant: nothing that comes from the
// page, from apt or from Docker is ever put into its text. What varies travels in the unit's
// environment (systemd-run --setenv), after the core validated it, and the script treats it as
// data: it is quoted, or split into the words it was validated to be made of, and never eval'd.
//
// The owner's word to go ahead of a hold on the engine's packages travels the same way
// (CASAOS_DU_ALLOW_HELD, 1 or nothing), and is the only thing that puts apt's flag on a call. It comes
// with the names of the engine's packages that the core saw on hold (CASAOS_DU_HELD), and never
// without them: the flag lets apt change a held package, and dpkg then drops the hold of every package it
// unpacks (apt passes it nothing of the holds, and dpkg sets the selection of a package it unpacks back
// to install, flag or no flag), so the unit puts the hold back, for those names and no others. The flag
// is global to the three calls, so a package among the names the unit may touch that is on hold when the
// unit starts and that the core did not name is a run the unit refuses (GUARD plan), before it asks apt
// for anything: it could not put that hold back.
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
	systemDockerReturnTimeout = dockerpkg.ReturnWaitSeconds
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

hold_marked=
held_names=
held_start=-
install_ran=
holds_back=

ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
mark() { printf 'CASAOS_DOCKER_UPDATE_%s %s%s\n' "$1" "$CASAOS_DU_NONCE" "${2:+ $2}"; }
# Once the install was started the holds are put back, and once the holds are told at the start they
# are told at the end, whatever the end is.
fail() { restore_holds; [ -z "$hold_marked" ] || mark HOLDS "$(held_list)"; mark FAILED "$(ts) $1"; exit 1; }
# A docker command waits for ever on a socket whose service will not start: bound them all.
bound() { n=$1; shift; if command -v timeout >/dev/null 2>&1; then timeout "$n" "$@"; else "$@"; fi; }

# engine_name says whether a name is one of the engine's packages (dockerpkg.EngineNames, which a test
# holds this list to).
engine_name() {
	case $1 in
	containerd.io | docker-buildx-plugin | docker-ce | docker-ce-cli | docker-ce-rootless-extras | docker-compose-plugin | docker-model-plugin) return 0 ;;
	esac
	return 1
}

# held_among prints, one to a line, the packages among the names it is given that dpkg has on hold and
# installed: its abbreviation of a package's state starts with an "h", and is not the "hn" of a package
# that is held and not installed, or the "hc" of one that only has its configuration left. dpkg-query is
# asked for those names alone, and only a name of that list is taken from what it prints (and only text of
# the shape of a package's name: a line that holds two names of the list is not one of them). It is given at
# least one: with none it would list every package there is.
held_among() {
	dpkg-query -W -f='${db:Status-Abbrev} ${Package}\n' "$@" 2>/dev/null | while read -r st pkg; do
		case $st in
		h[!nc]*)
			case " $* " in *" $pkg "*) printf '%s\n' "$pkg" ;; esac
			;;
		esac
	done | grep -E '^[a-z0-9][a-z0-9+.-]*$'
}

# held_lines is that, for the packages the core saw on hold (held_names): the ones that are put back.
held_lines() {
	[ -n "$held_names" ] || return 0
	held_among $held_names
}

# held_list is those names on one line, or - for none: what the HELD and HOLDS markers say.
held_list() {
	list=$(held_lines | tr '\n' ' ')
	list=${list% }
	printf '%s' "${list:--}"
}

# restore_holds puts the holds back once the install was started: dpkg drops the hold of every package it
# unpacks, with or without the flag, and the owner's hold is on Docker's packages until the owner lifts
# it. It holds the packages that were on hold when the unit started (held_start) and no others, with the
# tool the owner holds with, and without the flag. dpkg may be locked for a moment when the install
# returns: up to ten tries, three seconds apart. It says nothing: HOLDS says whether it worked, and it
# does not fail the run. It is done once it worked; a try that did not is made again once the run is
# over, by fail for a run that fails and just before the holds are said for one that goes through.
restore_holds() {
	[ -n "$install_ran" ] || return 0
	[ -z "$holds_back" ] || return 0
	[ "$held_start" != - ] || return 0
	tries=0
	while :; do
		if apt-mark hold $held_start >/dev/null 2>&1; then
			holds_back=1
			return 0
		fi
		tries=$((tries + 1))
		[ "$tries" -lt 10 ] || return 0
		sleep 3
	done
}

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

# The owner's word, given in the page's confirmation, that apt may go ahead of a hold on the engine's
# packages: 1, or nothing, and with it the names of those the core saw on hold, which are the engine's, and
# never one without the other. The flag is the terminal command's, and is on the three calls of apt
# below that look at or change what is held, and on no other. Anything else is a unit that the core did
# not make: a word that is not 1, a word without names or names without the word, a name that is not
# made of what a package's name is, one twice, or one that is neither among the names the unit may touch
# nor one of the engine's packages (the names are put on the command line of dpkg-query, and of apt-mark
# when the holds are put back). The names are kept as the words they were validated to be.
bad=
case $(printf '%s' "$CASAOS_DU_HELD" | tr -d ' ') in *[!a-z0-9+.-]*) bad=1 ;; esac
seen=' '
held_names=
for name in $CASAOS_DU_HELD; do
	case $name in [!a-z0-9]*) bad=1 ;; esac
	case $seen in *" $name "*) bad=1 ;; esac
	seen="$seen$name "
	case " $CASAOS_DU_NAMES " in *" $name "*) ;; *) engine_name "$name" || bad=1 ;; esac
	held_names="$held_names $name"
done
held_names=${held_names# }
hold_flag=
case ${CASAOS_DU_ALLOW_HELD:-} in
'') [ -z "$held_names" ] || bad=1 ;;
1)
	hold_flag=--allow-change-held-packages
	[ -n "$held_names" ] || bad=1
	;;
*) bad=1 ;;
esac
if [ -n "$bad" ]; then
	mark GUARD plan
	fail guard
fi

# The flag is global to the three calls of apt: it lets apt change every package among the names that is on
# hold, and dpkg drops the hold of each one it unpacks. The unit puts back the holds of the packages the core
# listed and no others, so a package among the names that is on hold now and that is not one of them (the
# owner held it after the core looked) is a hold it would lose without a word: the run is refused, and
# nothing has been asked of apt yet.
if [ -n "$hold_flag" ]; then
	for name in $(held_among $CASAOS_DU_NAMES); do
		case " $held_names " in *" $name "*) ;; *) bad=1 ;; esac
	done
	if [ -n "$bad" ]; then
		mark GUARD plan
		fail guard
	fi
fi

# What is on hold now, said here and again before the end, so that the log shows whether the holds are
# in place again. They are the ones put back after the install: the packages dpkg drops the hold of
# are those it unpacks, and the owner's hold is on Docker's packages until the owner lifts it.
held_start=$(held_list)
mark HELD "$held_start"
hold_marked=1

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
sim=$("$CASAOS_DU_APT" -s --no-remove -o Debug::NoLocking=true -o Dpkg::Use-Pty=0 install --only-upgrade --no-install-recommends $hold_flag $CASAOS_DU_PINS 2>&1) || ok=0
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
"$CASAOS_DU_APT" -y --no-remove --download-only -o Dpkg::Use-Pty=0 -o DPkg::Lock::Timeout=120 install --only-upgrade --no-install-recommends $hold_flag $CASAOS_DU_PINS || fail download
mark DOWNLOADED "$(ts)"

install_ran=1
"$CASAOS_DU_APT" -y --no-remove -o Dpkg::Use-Pty=0 -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=120 install --only-upgrade --no-install-recommends $hold_flag $CASAOS_DU_PINS
install_rc=$?
# whatever the install did, the first thing after it is the hold: dpkg dropped it from every package it
# unpacked, and a failure may have unpacked some
restore_holds
if [ "$install_rc" -ne 0 ]; then
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

# the put back that was made right after the install may not have worked, and the waits above took as long
# as they took: once more before the holds are said (nothing, when it worked)
restore_holds
mark HOLDS "$(held_list)"
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
// and the engine version expected. held are the engine's packages the core saw on hold, which the
// owner allowed the update to go ahead of: there are none for a plan that has no hold to go ahead of.
// They are an environment variable of their own, sorted, and so is the word that goes with them
// (CASAOS_DU_ALLOW_HELD, which is there exactly when they are): the unit holds them again when the
// install is over, and refuses to start when another package it may touch is on hold. The script checks
// both again.
func dockerUpdateArgs(nonce, logPath, aptPath string, pins, names []string, to string, held []string) ([]string, error) {
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
	// a hold is on one of the engine's packages, named as the box has it (the unit takes no other
	// shape, and no architecture: IsEngineName alone would let containerd.io:armhf through), once each,
	// which is at most one for each of dockerpkg.EngineNames
	heldSeen := map[string]bool{}
	for _, name := range held {
		if strings.Contains(name, ":") || !dockerpkg.IsEngineName(name) || heldSeen[name] {
			return nil, fmt.Errorf("%q is not a package of the engine that can be on hold", name)
		}
		heldSeen[name] = true
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
	args := []string{
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
	}
	if len(held) > 0 {
		sorted := append([]string{}, held...)
		sort.Strings(sorted)
		args = append(args, "--setenv=CASAOS_DU_HELD="+strings.Join(sorted, " "), "--setenv=CASAOS_DU_ALLOW_HELD=1")
	}
	return append(args,
		"--setenv=CASAOS_DU_DAEMON_TIMEOUT="+strconv.Itoa(systemDockerDaemonTimeout),
		"--setenv=CASAOS_DU_RETURN_TIMEOUT="+strconv.Itoa(systemDockerReturnTimeout),
		"--setenv=CASAOS_DU_POLL="+strconv.Itoa(systemDockerPoll),
		"/bin/sh",
		"-c",
		dockerUpdateScriptArg(),
	), nil
}
