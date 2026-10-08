package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	commonfile "github.com/ReCasaOS/CasaOS-Common/utils/file"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

const (
	systemPackageManagerAPT = "apt"
	systemPackageUpdateUnit = "casaos-package-update.service"
	systemPackageUpdateLog  = "package-update.log"

	systemPackageUpdateStateIdle       = "idle"
	systemPackageUpdateStateRunning    = "running"
	systemPackageUpdateStateFinalizing = "finalizing"
	systemPackageUpdateStateSuccess    = "succeeded"
	systemPackageUpdateStateFailed     = "failed"

	systemPackageCheckTimeout      = 5 * time.Minute
	systemPackageResultGracePeriod = 30 * time.Second
	systemPackageLogMaxBytes       = 128 * 1024
	systemPackageErrorMaxSize      = 4096
)

var (
	ErrSystemPackageUpdateRunning      = errors.New("system package update is already running")
	ErrSystemPackageUpdatesUnsupported = errors.New("system package updates are not supported on this host")
)

type SystemPackageUpdate struct {
	Name             string `json:"name"`
	CurrentVersion   string `json:"current_version"`
	CandidateVersion string `json:"candidate_version"`
}

type SystemPackageUpdates struct {
	Supported      bool                  `json:"supported"`
	Manager        string                `json:"manager,omitempty"`
	Reason         string                `json:"reason,omitempty"`
	Updates        []SystemPackageUpdate `json:"updates"`
	Count          int                   `json:"count"`
	CheckedAt      *time.Time            `json:"checked_at,omitempty"`
	RebootRequired bool                  `json:"reboot_required"`
	// Docker is shown on a line of its own: the update never installs it.
	Docker *SystemPackageDocker `json:"docker,omitempty"`
}

type SystemPackageUpdateStatus struct {
	Supported      bool       `json:"supported"`
	Manager        string     `json:"manager,omitempty"`
	State          string     `json:"state"`
	Log            string     `json:"log,omitempty"`
	Error          string     `json:"error,omitempty"`
	ExitCode       *int       `json:"exit_code,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	RebootRequired bool       `json:"reboot_required"`
}

type systemPackageSupport struct {
	supported   bool
	manager     string
	reason      string
	aptPath     string
	systemdPath string
}

type systemPackageUpdater struct {
	mu sync.Mutex

	command       func(context.Context, string, ...string) ([]byte, error)
	start         func(string, string, ...string) ([]byte, error)
	lookPath      func(string) (string, error)
	readOSRelease func() (map[string]string, error)
	getEUID       func() int
	readLog       func(string, int64) (string, error)
	writeFile     func(string, []byte, os.FileMode) error
	mkdirAll      func(string, os.FileMode) error
	stat          func(string) (os.FileInfo, error)
	now           func() time.Time
	// dpkgLocked says whether a package manager holds dpkg's lock; nil means never.
	dpkgLocked func() bool
}

func newSystemPackageUpdater() *systemPackageUpdater {
	return &systemPackageUpdater{
		command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		start: func(path string, _ string, args ...string) ([]byte, error) {
			return exec.Command(path, args...).CombinedOutput()
		},
		lookPath:      exec.LookPath,
		readOSRelease: commonfile.ReadOSRelease,
		getEUID:       os.Geteuid,
		readLog:       readBoundedSystemPackageLog,
		writeFile:     os.WriteFile,
		mkdirAll:      os.MkdirAll,
		stat:          os.Stat,
		now:           time.Now,
		dpkgLocked:    dpkgLockHeld,
	}
}

func (s *systemService) systemPackageUpdater() *systemPackageUpdater {
	if s.packageUpdates == nil {
		s.packageUpdates = newSystemPackageUpdater()
	}
	return s.packageUpdates
}

func (s *systemService) GetSystemPackageUpdates() (SystemPackageUpdates, error) {
	return s.systemPackageUpdater().check()
}

func (s *systemService) StartSystemPackageUpdate() (SystemPackageUpdateStatus, error) {
	return s.systemPackageUpdater().startUpdate()
}

func (s *systemService) GetSystemPackageUpdateStatus() SystemPackageUpdateStatus {
	return s.systemPackageUpdater().status()
}

func (u *systemPackageUpdater) support() systemPackageSupport {
	if u.getEUID() != 0 {
		return systemPackageSupport{reason: "CasaOS must run as root to update system packages."}
	}

	release, err := u.readOSRelease()
	if err != nil || !isDebianFamily(release) {
		return systemPackageSupport{reason: "System package updates are supported on Debian-family Linux systems only."}
	}

	aptPath, err := u.lookPath("apt-get")
	if err != nil {
		return systemPackageSupport{reason: "apt-get was not found on this host."}
	}
	systemdPath, err := u.lookPath("systemd-run")
	if err != nil {
		return systemPackageSupport{reason: "systemd-run was not found on this host."}
	}

	return systemPackageSupport{
		supported:   true,
		manager:     systemPackageManagerAPT,
		aptPath:     aptPath,
		systemdPath: systemdPath,
	}
}

func isDebianFamily(release map[string]string) bool {
	values := strings.Fields(strings.ToLower(release["ID"] + " " + release["ID_LIKE"]))
	for _, value := range values {
		switch value {
		case "debian", "ubuntu", "raspbian":
			return true
		}
	}
	return false
}

func (u *systemPackageUpdater) check() (SystemPackageUpdates, error) {
	support := u.support()
	result := SystemPackageUpdates{
		Supported: support.supported,
		Manager:   support.manager,
		Reason:    support.reason,
		Updates:   []SystemPackageUpdate{},
	}
	if !support.supported {
		return result, nil
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.isRunning() {
		return result, ErrSystemPackageUpdateRunning
	}

	ctx, cancel := context.WithTimeout(context.Background(), systemPackageCheckTimeout)
	defer cancel()

	if output, err := u.command(ctx, support.aptPath, "update"); err != nil {
		return result, fmt.Errorf("apt package index update failed: %s", trimSystemPackageOutput(output))
	}

	output, err := u.command(ctx, support.aptPath,
		"-s",
		"--no-remove",
		"-o", "Debug::NoLocking=true",
		"-o", "Dpkg::Use-Pty=0",
		"upgrade",
	)
	if err != nil {
		return result, fmt.Errorf("apt package update check failed: %s", trimSystemPackageOutput(output))
	}

	var dockerUpdates []SystemPackageUpdate
	result.Updates, dockerUpdates = splitDockerUpdates(parseAPTUpgradeSimulation(string(output)), u.engineInstalled(ctx))
	result.Count = len(result.Updates)
	result.Docker = u.dockerInfo(ctx, dockerUpdates)
	now := u.now().UTC()
	result.CheckedAt = &now
	result.RebootRequired = u.rebootRequired()
	return result, nil
}

func (u *systemPackageUpdater) startUpdate() (SystemPackageUpdateStatus, error) {
	support := u.support()
	status := SystemPackageUpdateStatus{
		Supported:      support.supported,
		Manager:        support.manager,
		State:          systemPackageUpdateStateIdle,
		RebootRequired: u.rebootRequired(),
	}
	if !support.supported {
		status.Error = support.reason
		return status, ErrSystemPackageUpdatesUnsupported
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.isRunning() {
		status.State = systemPackageUpdateStateRunning
		return u.statusLocked(support), ErrSystemPackageUpdateRunning
	}

	if reason, busy := u.maintenanceBusy(context.Background(), systemPackageUpdateUnit); busy {
		status.Error = reason
		return status, fmt.Errorf("%w: %s", ErrSystemMaintenanceBusy, reason)
	}

	// The two simulations take a while on a small box: not with the lock held, so that the
	// status and the check can still answer, and the box is looked at again afterwards.
	u.mu.Unlock()
	listCtx, cancelList := context.WithTimeout(context.Background(), systemPackageCheckTimeout)
	names, err := u.upgradeNames(listCtx, support)
	cancelList()
	u.mu.Lock()
	if err != nil {
		status.Error = err.Error()
		return status, err
	}
	if u.isRunning() {
		status.State = systemPackageUpdateStateRunning
		return u.statusLocked(support), ErrSystemPackageUpdateRunning
	}
	if reason, busy := u.maintenanceBusy(context.Background(), systemPackageUpdateUnit); busy {
		status.Error = reason
		return status, fmt.Errorf("%w: %s", ErrSystemMaintenanceBusy, reason)
	}

	logPath := u.logPath()
	args, err := systemPackageUpdateArgs(support.aptPath, logPath, names)
	if err != nil {
		status.Error = err.Error()
		return status, err
	}
	if err := u.mkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		markSystemPackageUpdateFailed(&status, fmt.Sprintf("prepare package update log: %v", err), u.now)
		return status, err
	}
	queuedAt := u.now().UTC()
	queuedLog := fmt.Sprintf("CASAOS_PACKAGE_UPDATE_QUEUED %s\n", queuedAt.Format(time.RFC3339))
	if err := u.writeFile(logPath, []byte(queuedLog), 0o644); err != nil {
		markSystemPackageUpdateFailed(&status, fmt.Sprintf("prepare package update log: %v", err), u.now)
		return status, err
	}
	if output, err := u.start(support.systemdPath, systemPackageUpdateUnit, args...); err != nil {
		failure := fmt.Sprintf("CASAOS_PACKAGE_UPDATE_FAILED %s 1\n%s\n", u.now().UTC().Format(time.RFC3339), trimSystemPackageOutput(output))
		_ = u.writeFile(logPath, []byte(failure), 0o644)
		markSystemPackageUpdateFailed(&status, fmt.Sprintf("start package update: %s", trimSystemPackageOutput(output)), u.now)
		return status, fmt.Errorf("start package update: %w", err)
	}

	status.State = systemPackageUpdateStateRunning
	status.StartedAt = &queuedAt
	return status, nil
}

func markSystemPackageUpdateFailed(status *SystemPackageUpdateStatus, message string, now func() time.Time) {
	status.State = systemPackageUpdateStateFailed
	status.Error = message
	completedAt := now().UTC()
	status.CompletedAt = &completedAt
}

func (u *systemPackageUpdater) status() SystemPackageUpdateStatus {
	support := u.support()
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.statusLocked(support)
}

func (u *systemPackageUpdater) statusLocked(support systemPackageSupport) SystemPackageUpdateStatus {
	status := SystemPackageUpdateStatus{
		Supported:      support.supported,
		Manager:        support.manager,
		State:          systemPackageUpdateStateIdle,
		RebootRequired: u.rebootRequired(),
	}

	logPath := u.logPath()
	log, err := u.readLog(logPath, systemPackageLogMaxBytes)
	if err == nil {
		status.Log = log
	}
	parsed := parseSystemPackageUpdateLog(log)
	if parsed.state != "" {
		status.State = parsed.state
	}
	status.Error = parsed.err
	status.ExitCode = parsed.exitCode
	status.StartedAt = parsed.startedAt
	status.CompletedAt = parsed.completedAt
	status.RebootRequired = status.RebootRequired || parsed.rebootRequired

	if parsed.state == systemPackageUpdateStateIdle && parsed.startedAt != nil {
		if u.isRunning() {
			status.State = systemPackageUpdateStateRunning
		} else if u.isWithinResultGrace(logPath) {
			// systemd can report the transient unit as inactive just before the
			// shell has flushed its terminal result marker to the log. Keep this
			// state non-terminal briefly so callers can reconcile the result.
			status.State = systemPackageUpdateStateFinalizing
		} else {
			status.State = systemPackageUpdateStateFailed
			status.Error = "The package update stopped before it reported a result."
		}
	} else if parsed.state == systemPackageUpdateStateIdle && u.isRunning() {
		status.State = systemPackageUpdateStateRunning
		if info, statErr := u.stat(logPath); statErr == nil {
			startedAt := info.ModTime().UTC()
			status.StartedAt = &startedAt
		}
	}

	return status
}

func (u *systemPackageUpdater) isRunning() bool {
	output, err := u.command(context.Background(), "systemctl", "show", systemPackageUpdateUnit, "--property=ActiveState", "--value")
	if err != nil {
		return false
	}
	switch strings.TrimSpace(string(output)) {
	case "active", "activating", "deactivating":
		return true
	default:
		return false
	}
}

func (u *systemPackageUpdater) isWithinResultGrace(logPath string) bool {
	info, err := u.stat(logPath)
	if err != nil {
		return false
	}

	age := u.now().UTC().Sub(info.ModTime().UTC())
	return age >= -systemPackageResultGracePeriod && age <= systemPackageResultGracePeriod
}

func (u *systemPackageUpdater) rebootRequired() bool {
	_, err := u.stat("/var/run/reboot-required")
	return err == nil
}

func (u *systemPackageUpdater) logPath() string {
	logDir := config.AppInfo.LogPath
	if strings.TrimSpace(logDir) == "" {
		logDir = "/var/log/casaos"
	}
	return filepath.Join(logDir, systemPackageUpdateLog)
}

var aptSimulationInstallPattern = regexp.MustCompile(`^Inst\s+(\S+)(?:\s+\[([^\]]*)\])?\s+\((\S+)`)

func parseAPTUpgradeSimulation(output string) []SystemPackageUpdate {
	updates := make(map[string]SystemPackageUpdate)
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		matches := aptSimulationInstallPattern.FindStringSubmatch(strings.TrimSpace(scanner.Text()))
		if len(matches) != 4 {
			continue
		}
		updates[matches[1]] = SystemPackageUpdate{
			Name:             matches[1],
			CurrentVersion:   matches[2],
			CandidateVersion: matches[3],
		}
	}

	result := make([]SystemPackageUpdate, 0, len(updates))
	for _, update := range updates {
		result = append(result, update)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func systemPackageUpdateArgs(aptPath, logPath string, names []string) ([]string, error) {
	command, err := systemPackageUpdateCommand(aptPath, logPath, names)
	if err != nil {
		return nil, err
	}
	return []string{
		"--quiet",
		"--no-block",
		"--collect",
		"--unit=" + systemPackageUpdateUnit,
		"--property=Type=exec",
		"--setenv=DEBIAN_FRONTEND=noninteractive",
		"--description=CasaOS system package update",
		"/bin/bash",
		"-o",
		"pipefail",
		"-c",
		command,
	}, nil
}

// systemPackageUpdateCommand installs exactly the packages in names, as upgrades: a name
// that is not a package name is refused, never put into the shell line.
func systemPackageUpdateCommand(aptPath, logPath string, names []string) (string, error) {
	if len(names) == 0 {
		return "", ErrSystemPackageNothingToUpdate
	}
	quotedNames := make([]string, 0, len(names))
	for _, name := range names {
		if !dockerpkg.ValidName(name) {
			return "", fmt.Errorf("%q is not a package name", name)
		}
		quotedNames = append(quotedNames, shellQuote(name))
	}
	quotedLogPath := shellQuote(logPath)
	quotedAPTPath := shellQuote(aptPath)
	installArgs := "install --only-upgrade --no-install-recommends " + strings.Join(quotedNames, " ")
	// The check made when the update was asked for may be minutes old by the time the unit
	// has the lock (an index refreshed since can make a listed package want a newer Docker):
	// the same simulation is made again just before the install, and nothing is installed if
	// it now touches Docker's packages or removes one.
	guarded := "plan=\"$(" + quotedAPTPath + " -s --no-remove -o Debug::NoLocking=true -o Dpkg::Use-Pty=0 " + installArgs + " 2>&1)\"; " +
		"if printf '%s\\n' \"$plan\" | grep -Eq " + shellQuote(dockerpkg.ContractPattern()) + "; then " +
		"printf 'CASAOS_PACKAGE_UPDATE_GUARD the list now changes Docker or removes a package: nothing was installed\\n'; status=1; else " +
		quotedAPTPath + " -y --no-remove -o Dpkg::Use-Pty=0 -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=120 " + installArgs + "; status=$?; fi; "
	return "set -o pipefail; exec >> " + quotedLogPath + " 2>&1; printf 'CASAOS_PACKAGE_UPDATE_STARTED %s\\n' \"$(/bin/date -u +%Y-%m-%dT%H:%M:%SZ)\"; " + guarded + "if [ -f /var/run/reboot-required ]; then printf 'CASAOS_PACKAGE_UPDATE_REBOOT_REQUIRED\\n'; fi; if [ \"$status\" -eq 0 ]; then printf 'CASAOS_PACKAGE_UPDATE_SUCCESS %s\\n' \"$(/bin/date -u +%Y-%m-%dT%H:%M:%SZ)\"; else printf 'CASAOS_PACKAGE_UPDATE_FAILED %s %s\\n' \"$(/bin/date -u +%Y-%m-%dT%H:%M:%SZ)\" \"$status\"; fi; exit \"$status\"", nil
}

type parsedSystemPackageUpdateLog struct {
	state          string
	err            string
	exitCode       *int
	startedAt      *time.Time
	completedAt    *time.Time
	rebootRequired bool
}

func parseSystemPackageUpdateLog(log string) parsedSystemPackageUpdateLog {
	parsed := parsedSystemPackageUpdateLog{state: systemPackageUpdateStateIdle}
	for _, line := range strings.Split(log, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "CASAOS_PACKAGE_UPDATE_QUEUED":
			if parsed.startedAt == nil && len(fields) > 1 {
				parsed.startedAt = parseSystemPackageTimestamp(fields[1])
			}
		case "CASAOS_PACKAGE_UPDATE_STARTED":
			if len(fields) > 1 {
				parsed.startedAt = parseSystemPackageTimestamp(fields[1])
			}
		case "CASAOS_PACKAGE_UPDATE_REBOOT_REQUIRED":
			parsed.rebootRequired = true
		case "CASAOS_PACKAGE_UPDATE_SUCCESS":
			parsed.state = systemPackageUpdateStateSuccess
			if len(fields) > 1 {
				parsed.completedAt = parseSystemPackageTimestamp(fields[1])
			}
		case "CASAOS_PACKAGE_UPDATE_FAILED":
			parsed.state = systemPackageUpdateStateFailed
			parsed.err = "The package update failed. See the update log for details."
			if len(fields) > 1 {
				parsed.completedAt = parseSystemPackageTimestamp(fields[1])
			}
			if len(fields) > 2 {
				if code, err := strconv.Atoi(fields[2]); err == nil {
					parsed.exitCode = &code
				}
			}
		}
	}
	return parsed
}

func parseSystemPackageTimestamp(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func readBoundedSystemPackageLog(path string, maxBytes int64) (string, error) {
	if maxBytes <= 0 {
		return "", nil
	}

	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	start := int64(0)
	truncated := false
	prefix := "[Earlier log output omitted]\n"
	readBytes := maxBytes
	if info.Size() > maxBytes {
		truncated = true
		if maxBytes <= int64(len(prefix)) {
			return prefix[:int(maxBytes)], nil
		}
		readBytes = maxBytes - int64(len(prefix))
		if readBytes < 0 {
			readBytes = 0
		}
		start = info.Size() - readBytes
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(file, readBytes))
	if err != nil {
		return "", err
	}
	if truncated {
		return prefix + string(data), nil
	}
	return string(data), nil
}

func trimSystemPackageOutput(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if len(trimmed) <= systemPackageErrorMaxSize {
		return trimmed
	}
	return "..." + trimmed[len(trimmed)-systemPackageErrorMaxSize+3:]
}
