package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// The "Update Docker" button: start the unit that updates the engine, and say how it is going.
// Both share the System packages updater's lock, its seams and its way of knowing about a run:
// nothing is kept in memory, the log of the run and systemd are read each time, so that a restart
// of the core in the middle of the update changes nothing.

const systemDockerUpdateLog = "docker-update.log"

// The reasons the update does not start for, after the static ones of dockerUpdatePreflight
// (DockerRefusalOrigin and the others): things that change from one minute to the next.
const (
	// DockerRefusalRunning is the update of Docker, or a System packages update, already running.
	DockerRefusalRunning = "running"
	// DockerRefusalMaintenance is another update or a package manager busy on the box.
	DockerRefusalMaintenance = "maintenance"
	// DockerRefusalApps is an app operation in progress, or AppManagement that cannot say.
	DockerRefusalApps = "apps"
	// DockerRefusalChanged is a plan that is not the one the owner confirmed.
	DockerRefusalChanged = "changed"
	// DockerRefusalNothing is an engine with nothing to update.
	DockerRefusalNothing = "nothing"
)

// ErrDockerUpdateBadPlanID is a plan id that is not 64 lower case hex digits.
var ErrDockerUpdateBadPlanID = errors.New("the plan id is not the id of a plan")

// DockerUpdateRefusal is why the update did not start. The page turns the Code into its own words
// and shows Detail (names the core validated) with them; Reason is English, for a log or a client
// that does not know the code.
type DockerUpdateRefusal struct {
	Code   string
	Detail []string
	Reason string
}

func (r *DockerUpdateRefusal) Error() string { return r.Reason }

var dockerRefusalReasons = map[string]string{
	DockerRefusalOrigin:      "Docker was not installed from Docker's own repository, so it cannot be updated from here.",
	DockerRefusalHeld:        "Docker's packages are on hold. The hold is yours to release; this will not override it.",
	DockerRefusalDaemon:      "Docker does not answer, so there is nothing to check the update against.",
	DockerRefusalSwarm:       "This node is part of a Docker swarm, which this update does not handle.",
	DockerRefusalPlan:        "The update would do more than upgrade Docker's own packages and install what they need.",
	DockerRefusalDisk:        "There is not enough free disk space for the update.",
	DockerRefusalRunning:     "An update is already running.",
	DockerRefusalApps:        "An app is being installed, updated, backed up or restored.",
	DockerRefusalChanged:     "A newer version appeared: confirm the update again.",
	DockerRefusalNothing:     "There is nothing to update.",
	DockerRefusalMaintenance: "Another update or package operation is running on this box.",
}

// The states and outcomes of a run, as the status says them. The states are the System packages
// update's own.
const (
	systemDockerOutcomeSuccess        = "success"
	systemDockerOutcomeRestartPending = "restart_pending"
	systemDockerOutcomeFailed         = "failed"

	// systemDockerErrorNoResult is a unit that ended without a terminal marker.
	systemDockerErrorNoResult = "no_result"

	// systemDockerStartTimeout bounds what the start does before it starts the unit.
	systemDockerStartTimeout = time.Minute
	// systemDockerUnitTimeout bounds a question to systemd.
	systemDockerUnitTimeout = 10 * time.Second
	// systemDockerHeadBytes is read from the start of a log that was cut: its first line is the
	// run's nonce.
	systemDockerHeadBytes = 256

	// dockerLogOmitted is what readBoundedSystemPackageLog puts in front of a log it cut.
	dockerLogOmitted = "[Earlier log output omitted]\n"
)

var dockerFailMessages = map[string]string{
	dockerpkg.FailGuard:    "The update was no longer what you confirmed, so nothing was installed.",
	dockerpkg.FailDownload: "The packages could not be downloaded. Nothing was changed.",
	dockerpkg.FailInstall:  "The packages could not be installed.",
	dockerpkg.FailDaemon:   "Docker did not come back after the update.",
	dockerpkg.FailStart:    "The update could not be started. Nothing was changed.",
}

// SystemDockerUpdateStatus is how the update of Docker is going, or how it went: the same
// object comes back from the start and from the status.
type SystemDockerUpdateStatus struct {
	Supported bool `json:"supported"`
	// State is idle, running, finalizing, succeeded or failed.
	State string `json:"state"`
	// Phase is the step a run that is going on is at: preparing, downloading (the apps run),
	// installing (Docker restarts), waiting_docker (for the daemon), waiting_containers (for the
	// apps). It is "" for a run that has ended and when nothing runs: see dockerpkg.Run.Phase.
	Phase string `json:"phase"`
	// Outcome is success or restart_pending (State succeeded), failed (State failed), or "".
	Outcome string `json:"outcome"`
	Error   string `json:"error"`
	// ErrorCode is guard, download, install, daemon, start or no_result for a run that failed,
	// and the code of the refusal for a start that was refused.
	ErrorCode string `json:"error_code"`
	// RefusalDetail goes with a refusal: the names of the packages or of the apps it is about.
	RefusalDetail []string `json:"refusal_detail,omitempty"`
	// ExitCode is there for the page's sake and is null: the unit reports in its log.
	ExitCode    *int   `json:"exit_code"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	// From and To are the engine's versions before and after, from the log. To is there when
	// the new engine runs.
	From string `json:"from"`
	To   string `json:"to"`
	// NotReturned are the containers that were running before and were not after.
	NotReturned []dockerpkg.NotReturned `json:"not_returned"`
	// RollbackCommand is what to type to put the packages back, for a run that failed.
	RollbackCommand string `json:"rollback_command"`
	// Log is the end of the unit's log, at most 128 KiB.
	Log string `json:"log"`
}

// AppOperationsFunc says which apps have an operation in progress (an install, an update, a
// backup, a restore), by name. An error is no answer. main sets it from AppManagement.
type AppOperationsFunc func(ctx context.Context) ([]string, error)

func (s *systemService) StartDockerUpdate(planID string) (SystemDockerUpdateStatus, error) {
	return s.systemPackageUpdater().startDockerUpdate(planID)
}

func (s *systemService) DockerUpdateStatus() SystemDockerUpdateStatus {
	return s.systemPackageUpdater().dockerStatus()
}

func (s *systemService) SetAppOperations(list AppOperationsFunc) {
	updater := s.systemPackageUpdater()
	updater.mu.Lock()
	defer updater.mu.Unlock()
	updater.appOperations = list
}

var (
	dockerPlanIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// dockerAppNamePattern is what the name of an app may be before it is shown.
	dockerAppNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

func (u *systemPackageUpdater) dockerLogPath() string {
	return filepath.Join(filepath.Dir(u.logPath()), systemDockerUpdateLog)
}

// refused is the status and the error of a start that did not happen. A refusal leaves the page
// the reason in the data as well as in the message: it clears its own error when it asks again.
func (u *systemPackageUpdater) dockerRefused(support systemPackageSupport, code string, detail []string, reason string) (SystemDockerUpdateStatus, error) {
	status := SystemDockerUpdateStatus{Supported: support.supported, State: systemPackageUpdateStateIdle, NotReturned: []dockerpkg.NotReturned{}}
	if code == DockerRefusalRunning && u.unitActive(systemDockerUpdateUnit) {
		status = u.dockerStatus()
	}
	if reason == "" {
		reason = dockerRefusalReasons[code]
	}
	status.Error, status.ErrorCode = reason, code
	if len(detail) > 0 {
		status.RefusalDetail = detail
	}
	return status, &DockerUpdateRefusal{Code: code, Detail: detail, Reason: reason}
}

// dockerBlocked is the refusal, when something else is running that the update must not start
// beside: itself or a System packages update, then the ReCasaOS update and dpkg's lock. It is
// nil when nothing is.
func (u *systemPackageUpdater) dockerBlocked(support systemPackageSupport) (SystemDockerUpdateStatus, error) {
	if u.unitActive(systemDockerUpdateUnit) || u.isRunning() {
		return u.dockerRefused(support, DockerRefusalRunning, nil, "")
	}
	if reason, busy := u.maintenanceBusy(context.Background(), systemDockerUpdateUnit); busy {
		return u.dockerRefused(support, DockerRefusalMaintenance, nil, fmt.Sprintf("%s: %s", dockerRefusalReasons[DockerRefusalMaintenance], reason))
	}
	return SystemDockerUpdateStatus{}, nil
}

// startDockerUpdate starts the unit that updates the engine to the plan the owner confirmed.
// It checks what the page's check checked, again, and what only matters now: nothing else is
// running, no app is busy, and the plan is still that plan. It writes the first line of the
// log, and returns as soon as systemd has the unit.
func (u *systemPackageUpdater) startDockerUpdate(planID string) (SystemDockerUpdateStatus, error) {
	if !dockerPlanIDPattern.MatchString(planID) {
		return SystemDockerUpdateStatus{State: systemPackageUpdateStateIdle, NotReturned: []dockerpkg.NotReturned{}}, ErrDockerUpdateBadPlanID
	}
	support := u.support()
	if !support.supported {
		status := SystemDockerUpdateStatus{State: systemPackageUpdateStateIdle, NotReturned: []dockerpkg.NotReturned{}, Error: support.reason, ErrorCode: DockerRefusalUnsupported}
		return status, ErrSystemPackageUpdatesUnsupported
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if status, err := u.dockerBlocked(support); err != nil {
		return status, err
	}

	// The simulation and the questions take a while on a small box: not with the lock held, so that
	// the status and the check can still answer, and the box is looked at again afterwards.
	u.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), systemDockerStartTimeout)
	update, plan := u.dockerUpdatePreflight(ctx, support)
	var operations []string
	var asked error
	if update.Refusal == "" && update.PlanID == planID {
		// the last question before the lock is taken again, so that the answer is as fresh as can be
		operations, asked = u.dockerAppOperations(ctx)
	}
	cancel()
	u.mu.Lock()
	if status, err := u.dockerBlocked(support); err != nil {
		return status, err
	}

	switch {
	case update.Refusal != "":
		return u.dockerRefused(support, update.Refusal, update.RefusalDetail, "")
	case len(plan.Packages) == 0:
		return u.dockerRefused(support, DockerRefusalNothing, nil, "")
	case update.PlanID != planID:
		return u.dockerRefused(support, DockerRefusalChanged, nil, "")
	case asked != nil:
		return u.dockerRefused(support, DockerRefusalApps, nil, "AppManagement did not say whether an app is busy, so nothing was started.")
	case len(operations) > 0:
		return u.dockerRefused(support, DockerRefusalApps, operations, "")
	}

	_, to := plan.Engine()
	nonce, err := dockerpkg.NewNonce()
	if err != nil {
		return u.dockerFailedToStart(support, "", fmt.Sprintf("prepare Docker update: %v", err), err)
	}
	args, err := dockerUpdateArgs(nonce, u.dockerLogPath(), support.aptPath, plan.Pins(), plan.Names(), to)
	if err != nil {
		// the plan passed Offending, so this is a bug; the unit would run as root
		return u.dockerRefused(support, DockerRefusalPlan, nil, "")
	}
	logPath := u.dockerLogPath()
	if err := u.mkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return u.dockerFailedToStart(support, "", fmt.Sprintf("prepare Docker update log: %v", err), err)
	}
	queuedAt := u.now().UTC()
	queued := dockerpkg.QueuedMarker(nonce, queuedAt)
	if err := u.writeFile(logPath, []byte(queued), 0o644); err != nil {
		return u.dockerFailedToStart(support, "", fmt.Sprintf("prepare Docker update log: %v", err), err)
	}
	// From here the log is a run's, whatever systemd says next: whoever waits for the end is told.
	if u.onDockerQueued != nil {
		u.onDockerQueued()
	}
	if output, err := u.start(support.systemdPath, systemDockerUpdateUnit, args...); err != nil {
		// The log is a whole run all the same: the first line, what systemd said, a last line.
		failure := queued + trimSystemPackageOutput(output) + "\n" + dockerpkg.FailedMarker(nonce, u.now(), dockerpkg.FailStart)
		_ = u.writeFile(logPath, []byte(failure), 0o644)
		return u.dockerFailedToStart(support, nonce, fmt.Sprintf("start Docker update: %s", trimSystemPackageOutput(output)), fmt.Errorf("start Docker update: %w", err))
	}

	return SystemDockerUpdateStatus{
		Supported:   true,
		State:       systemPackageUpdateStateRunning,
		Phase:       dockerpkg.PhasePreparing,
		StartedAt:   queuedAt.Format(time.RFC3339),
		NotReturned: []dockerpkg.NotReturned{},
		Log:         queued,
	}, nil
}

// dockerFailedToStart is the answer to a start that broke on the box's side. When the log is a
// whole run (nonce is its) the status is what the status will say of it.
func (u *systemPackageUpdater) dockerFailedToStart(support systemPackageSupport, nonce, message string, err error) (SystemDockerUpdateStatus, error) {
	status := SystemDockerUpdateStatus{Supported: support.supported, State: systemPackageUpdateStateFailed, Outcome: systemDockerOutcomeFailed, NotReturned: []dockerpkg.NotReturned{}}
	if nonce != "" {
		status = u.dockerStatus()
	}
	status.Error = message
	if status.CompletedAt == "" {
		status.CompletedAt = u.now().UTC().Format(time.RFC3339)
	}
	return status, err
}

// dockerAppOperations are the apps with an operation in progress, valid names only, sorted and
// once each; the error is that AppManagement could not be asked, or did not answer.
func (u *systemPackageUpdater) dockerAppOperations(ctx context.Context) ([]string, error) {
	if u.appOperations == nil {
		return nil, errors.New("the box cannot ask AppManagement")
	}
	apps, err := u.appOperations(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, app := range apps {
		if !dockerAppNamePattern.MatchString(app) {
			app = dockerpkg.InvalidName
		}
		seen[app] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// unitActive says whether a systemd unit is running, starting or stopping. Systemd not
// answering is not running: the log has the last word then.
func (u *systemPackageUpdater) unitActive(unit string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), systemDockerUnitTimeout)
	defer cancel()
	output, err := u.command(ctx, "systemctl", "show", unit, "--property=ActiveState", "--value")
	if err != nil {
		return false
	}
	switch strings.TrimSpace(string(output)) {
	case "active", "activating", "deactivating":
		return true
	}
	return false
}

// dockerStatus says how the update of Docker is going. It takes no lock and asks Docker nothing:
// the log, whose first line holds the run's nonce, and systemd are all it reads.
func (u *systemPackageUpdater) dockerStatus() SystemDockerUpdateStatus {
	status, _ := u.dockerRun()
	return status
}

// dockerRun is dockerStatus and the nonce of the run it describes, "" when the log holds none.
func (u *systemPackageUpdater) dockerRun() (SystemDockerUpdateStatus, string) {
	support := u.support()
	status := SystemDockerUpdateStatus{Supported: support.supported, State: systemPackageUpdateStateIdle, NotReturned: []dockerpkg.NotReturned{}}
	if !support.supported {
		return status, ""
	}

	logPath := u.dockerLogPath()
	log, err := u.readLog(logPath, systemPackageLogMaxBytes)
	if err == nil {
		status.Log = log
	}
	// A log that was cut has lost its first line, and with it the nonce: it is put back.
	parsed := log
	if strings.HasPrefix(log, dockerLogOmitted) {
		if head, err := readDockerLogHead(logPath); err == nil {
			parsed = head + "\n" + log
		}
	}
	run := dockerpkg.ParseRun(parsed)
	active := u.unitActive(systemDockerUpdateUnit)

	if run.Nonce == "" {
		// no run of ours in the log: the unit, if there is one, is not ours to describe
		if active {
			status.State = systemPackageUpdateStateRunning
			if info, err := u.stat(logPath); err == nil {
				status.StartedAt = info.ModTime().UTC().Format(time.RFC3339)
			}
		}
		return status, ""
	}

	status.StartedAt = formatDockerTime(run.StartedAt, run.QueuedAt)
	status.NotReturned = append(status.NotReturned, run.NotReturned...)
	from := ""
	for _, pin := range run.Previous {
		if name, version, _ := strings.Cut(pin, "="); name == "docker-ce" {
			from = dockerpkg.EngineVersion(version)
		}
	}
	status.From = from

	switch run.Terminal {
	case dockerpkg.TerminalSuccess:
		status.State, status.Outcome = systemPackageUpdateStateSuccess, systemDockerOutcomeSuccess
		status.To = dockerpkg.EngineVersion(run.DaemonVersion)
		if status.From == "" {
			// docker-ce was not in the plan: the engine is the one that runs
			status.From = status.To
		}
	case dockerpkg.TerminalRestartPending:
		// the packages are installed and the daemon is still the old one
		status.State, status.Outcome = systemPackageUpdateStateSuccess, systemDockerOutcomeRestartPending
	case dockerpkg.TerminalFailed:
		status.State, status.Outcome = systemPackageUpdateStateFailed, systemDockerOutcomeFailed
		status.ErrorCode = run.FailReason
		status.Error = dockerFailMessages[run.FailReason]
		status.RollbackCommand = dockerpkg.RollbackCommand(run.Previous)
	case "":
		switch {
		case active:
			status.State, status.Phase = systemPackageUpdateStateRunning, run.Phase()
		case u.isWithinResultGrace(logPath):
			// systemd can say the unit is gone just before the last line reached the log
			status.State, status.Phase = systemPackageUpdateStateFinalizing, run.Phase()
		default:
			status.State, status.Outcome = systemPackageUpdateStateFailed, systemDockerOutcomeFailed
			status.ErrorCode = systemDockerErrorNoResult
			status.Error = "The Docker update stopped before it reported a result."
			// it stopped somewhere: what was there before is in the log
			status.RollbackCommand = dockerpkg.RollbackCommand(run.Previous)
		}
	}
	if !run.CompletedAt.IsZero() {
		status.CompletedAt = run.CompletedAt.Format(time.RFC3339)
	}
	return status, run.Nonce
}

func formatDockerTime(times ...time.Time) string {
	for _, t := range times {
		if !t.IsZero() {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// readDockerLogHead is the first line of the log.
func readDockerLogHead(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	buf := make([]byte, systemDockerHeadBytes)
	n, err := io.ReadFull(file, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", err
	}
	head, _, _ := strings.Cut(string(buf[:n]), "\n")
	return head, nil
}
