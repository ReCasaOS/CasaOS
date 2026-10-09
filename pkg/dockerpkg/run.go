package dockerpkg

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// The Docker update runs in a unit of its own and reports in a log file. Everything the core
// shows of a run, it reads back from that file; and everything in that file is suspect, since
// apt, dpkg, the packages' scripts and Docker all write to it. A line counts as the unit's
// word only if it has the shape of a marker and carries the run's nonce, which only the
// core's first line and the unit's environment hold.

const markerPrefix = "CASAOS_DOCKER_UPDATE_"

// How a run ended (Run.Terminal).
const (
	// TerminalSuccess is an engine that was installed, runs, and answers.
	TerminalSuccess = "success"
	// TerminalRestartPending is an engine installed whose daemon is still the old one.
	TerminalRestartPending = "restart_pending"
	// TerminalFailed is a run that stopped, with a reason (Run.FailReason).
	TerminalFailed = "failed"
)

// Why a run failed (Run.FailReason).
const (
	FailGuard    = "guard"
	FailDownload = "download"
	FailInstall  = "install"
	FailDaemon   = "daemon"
	// FailStart is a unit that systemd could not start: the core writes it, in the last line of
	// the log, since the unit never wrote anything. Nothing was changed.
	FailStart = "start"
)

// ReturnWaitSeconds is how long the unit waits, after the daemon is back, for the containers that
// start again by themselves. A container with a restart policy that is still not running after
// it is reported all the same, with its policy.
const ReturnWaitSeconds = 90

// Where a run is (Run.Phase). The page tells the owner what the apps are going through from it.
const (
	// PhasePreparing is the start of the unit, up to the snapshot of what is installed.
	PhasePreparing = "preparing"
	// PhaseDownloading is the check of the plan and the download of the packages: nothing is
	// changed, and the apps run.
	PhaseDownloading = "downloading"
	// PhaseInstalling is the install, which restarts the daemon and stops the containers.
	PhaseInstalling = "installing"
	// PhaseWaitingDocker is the wait for the daemon to answer after the install.
	PhaseWaitingDocker = "waiting_docker"
	// PhaseWaitingContainers is the wait for the containers to come back.
	PhaseWaitingContainers = "waiting_containers"
)

// NewNonce is 16 random bytes from the system's source, in lower case hex.
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ValidNonce reports whether s is 32 lower case hex digits, what NewNonce makes.
func ValidNonce(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// QueuedMarker is the first line of the log, which the core writes (and so truncates the log
// with) before it starts the unit: the nonce is read from it, and from no other line.
func QueuedMarker(nonce string, at time.Time) string {
	return markerPrefix + "QUEUED " + nonce + " " + at.UTC().Format(time.RFC3339) + "\n"
}

// NotReturned is a container that was running before the update and is not running after it.
type NotReturned struct {
	Name          string `json:"name"`
	RestartPolicy string `json:"restart_policy"`
}

// Run is what a log says of a Docker update. Its zero value is "no run": the log has no valid
// first line. Previous and NotReturned are nil when the log has none.
type Run struct {
	Nonce string
	// QueuedAt is the time on the first line; StartedAt the unit's first word; DownloadedAt the
	// end of the download and InstalledAt the end of the install, zero if it never got there;
	// CompletedAt the terminal marker's.
	QueuedAt, StartedAt, DownloadedAt, InstalledAt, CompletedAt time.Time
	// PreviousSeen is whether the unit wrote its snapshot of the installed packages, which
	// Previous may be empty of.
	PreviousSeen bool
	// Terminal is TerminalSuccess, TerminalRestartPending, TerminalFailed, or "" while the
	// log has no valid terminal marker. The last valid one counts.
	Terminal string
	// FailReason is FailGuard, FailDownload, FailInstall, FailDaemon or FailStart, when Terminal
	// is TerminalFailed.
	FailReason string
	// Previous are the engine's packages as they were installed before the update, as
	// "name=version", each validated: what a rollback would install.
	Previous []string
	// DaemonVersion is the version the daemon reported once it was back, "" if it never did.
	DaemonVersion string
	// NotReturned are the containers that were running before and were not after.
	NotReturned []NotReturned
}

// markerOf splits a line that is a marker of this run: "CASAOS_DOCKER_UPDATE_<KIND> <nonce>"
// and, if there is more, a space and the fields. Anything else (a prefix in the middle of a
// line, another nonce, another case) is not one.
func markerOf(line, nonce string) (kind string, fields []string, ok bool) {
	rest, found := strings.CutPrefix(line, markerPrefix)
	if !found {
		return "", nil, false
	}
	kind, rest, found = strings.Cut(rest, " ")
	if !found {
		return "", nil, false
	}
	given, rest, _ := strings.Cut(rest, " ")
	if given != nonce {
		return "", nil, false
	}
	return kind, strings.Fields(rest), true
}

func parseMarkerTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// validPin is "name=version" for a package of the engine and a version that passes
// ValidVersion: the only thing that may be put on an apt command line.
func validPin(pin string) bool {
	name, version, found := strings.Cut(pin, "=")
	return found && ValidName(name) && IsEngineName(name) && ValidVersion(version)
}

// ParseRun reads the log of a Docker update. The nonce is the one on line 1, which must be
// exactly the core's QUEUED marker; with no such line it is the zero Run. After that a line
// is read only if it is "CASAOS_DOCKER_UPDATE_<KIND> <that nonce>" with the fields its KIND
// takes; every field is validated again; and what is not a marker is ignored, whatever it
// looks like: apt's output, a container named like a marker, a forged nonce. The last
// terminal marker wins, the first STARTED, INSTALLED and PREVIOUS, the last DAEMON.
//
// A caller that reads only the end of a long log must put line 1 back in front of it.
func ParseRun(log string) Run {
	head, tail, _ := strings.Cut(log, "\n")
	first := strings.Split(strings.TrimRight(head, "\r"), " ")
	if len(first) != 3 || first[0] != markerPrefix+"QUEUED" || !ValidNonce(first[1]) {
		return Run{}
	}
	queued, ok := parseMarkerTime(first[2])
	if !ok {
		return Run{}
	}
	run := Run{Nonce: first[1], QueuedAt: queued}
	for _, line := range strings.Split(tail, "\n") {
		kind, fields, ok := markerOf(strings.TrimRight(line, "\r"), run.Nonce)
		if !ok {
			continue
		}
		switch kind {
		case "STARTED":
			if t, ok := oneTime(fields); ok && run.StartedAt.IsZero() {
				run.StartedAt = t
			}
		case "DOWNLOADED":
			if t, ok := oneTime(fields); ok && run.DownloadedAt.IsZero() {
				run.DownloadedAt = t
			}
		case "INSTALLED":
			if t, ok := oneTime(fields); ok && run.InstalledAt.IsZero() {
				run.InstalledAt = t
			}
		case "PREVIOUS":
			run.PreviousSeen = true
			if run.Previous == nil {
				run.Previous = validPins(fields)
			}
		case "DAEMON":
			if len(fields) == 1 && validDaemonVersion(fields[0]) {
				run.DaemonVersion = fields[0]
			}
		case "NOTRETURNED":
			if container, ok := notReturned(fields); ok {
				run.NotReturned = append(run.NotReturned, container)
			}
		case "SUCCESS", "RESTART_PENDING":
			if t, ok := oneTime(fields); ok {
				run.Terminal, run.FailReason, run.CompletedAt = TerminalSuccess, "", t
				if kind == "RESTART_PENDING" {
					run.Terminal = TerminalRestartPending
				}
			}
		case "FAILED":
			if len(fields) == 2 && validFailReason(fields[1]) {
				if t, ok := parseMarkerTime(fields[0]); ok {
					run.Terminal, run.FailReason, run.CompletedAt = TerminalFailed, fields[1], t
				}
			}
		}
	}
	return run
}

// Phase is the step a run is at, from the markers it has written so far: PhasePreparing until the
// snapshot of what is installed, then PhaseDownloading until DOWNLOADED, PhaseInstalling until
// INSTALLED, PhaseWaitingDocker until DAEMON, and PhaseWaitingContainers until the terminal
// marker. It is "" for no run and for a run that has ended. The guard writes nothing when it
// passes, so it is part of the downloading: it is a few seconds, and the apps run through it.
func (r Run) Phase() string {
	switch {
	case r.Nonce == "" || r.Terminal != "":
		return ""
	case r.DaemonVersion != "":
		return PhaseWaitingContainers
	case !r.InstalledAt.IsZero():
		return PhaseWaitingDocker
	case !r.DownloadedAt.IsZero():
		return PhaseInstalling
	case r.PreviousSeen:
		return PhaseDownloading
	}
	return PhasePreparing
}

// oneTime is the fields of a marker that holds a time and nothing else.
func oneTime(fields []string) (time.Time, bool) {
	if len(fields) != 1 {
		return time.Time{}, false
	}
	return parseMarkerTime(fields[0])
}

func validFailReason(reason string) bool {
	switch reason {
	case FailGuard, FailDownload, FailInstall, FailDaemon, FailStart:
		return true
	}
	return false
}

// validPins keeps the valid "name=version" fields, once each, in order; nil when none is.
func validPins(fields []string) []string {
	var pins []string
	seen := map[string]struct{}{}
	for _, pin := range fields {
		if _, dup := seen[pin]; dup || !validPin(pin) {
			continue
		}
		seen[pin] = struct{}{}
		pins = append(pins, pin)
	}
	return pins
}

// notReturned reads the fields of a NOTRETURNED marker: a container's name and its restart
// policy, which is empty when Docker had none to tell.
func notReturned(fields []string) (NotReturned, bool) {
	if len(fields) == 0 || len(fields) > 2 {
		return NotReturned{}, false
	}
	container := NotReturned{Name: fields[0]}
	if len(fields) == 2 {
		container.RestartPolicy = fields[1]
	}
	if !validContainerName(container.Name) || !validRestartPolicy(container.RestartPolicy) {
		return NotReturned{}, false
	}
	return container, true
}

// RollbackCommand is the command that puts the engine back as it was, from the "name=version"
// pins of Run.Previous: "sudo apt-get install --allow-downgrades name=version ...". It is
// built from the pins that pass validation, and only from those, so that no shell character
// is in it; "" when there is none. Nothing runs it: it is shown to the owner.
func RollbackCommand(previous []string) string {
	var pins []string
	for _, pin := range previous {
		if validPin(pin) {
			pins = append(pins, pin)
		}
	}
	if len(pins) == 0 {
		return ""
	}
	return "sudo apt-get install --allow-downgrades " + strings.Join(pins, " ")
}
