// Package autoupdate installs new ReCasaOS releases by itself, for an owner who
// turned it on: at night, inside a window of the box's local time, once a
// release is 48 hours old, and never while an update, a backup or an app
// operation runs. It starts exactly the update the dashboard's button starts,
// and keeps its choice and its last attempt in autoupdate.json for the
// dashboard. An attempt that fails is tried once more the next night; after a
// second failure that release is paused until the owner resumes it or a newer
// one comes out.
//
// Four environment variables are for the install check only, never for a box:
// CASAOS_AUTOUPDATE_START_DELAY and CASAOS_AUTOUPDATE_INTERVAL (Go durations,
// e.g. 0s) replace the random start delay and the hour between checks, and
// CASAOS_AUTOUPDATE_VERSION_URL and CASAOS_AUTOUPDATE_INSTALLER_URL, read by
// the update button's own code in service, replace version.json and install.sh.
package autoupdate

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/utils/version"
	"go.uber.org/zap"
)

const (
	// StateFile holds the choice, the window and the last attempt (root, 0600).
	StateFile = "/var/lib/casaos/autoupdate.json"

	startDelayEnv = "CASAOS_AUTOUPDATE_START_DELAY"
	intervalEnv   = "CASAOS_AUTOUPDATE_INTERVAL"

	// operationsPath is AppManagement's list of the app operations in progress.
	operationsPath = "/v2/app_management/operations"
)

// Releases reads version.json the way the update button does: service's
// CasaService, through its 20-minute cache or past it.
type Releases interface {
	GetCasaosVersion() model.Version
	FetchCasaosVersion() model.Version
}

// AutoUpdate is the automatic updates of one box.
type AutoUpdate struct {
	// Root is where autoupdate.json is kept: "/" on a box, a temporary
	// directory in tests.
	Root string
	Now  func() time.Time
	// Current is the installed release, read as the update button reads it.
	Current func() string
	// Releases and Start are the update button's: main sets them to
	// service.MyService.Casa() and System().UpdateSystemVersion, the detached
	// casaos-update unit, its installer and its log.
	Releases Releases
	Start    func(version string) error
	// Command runs a program and returns its standard output: systemctl.
	Command func(name string, args ...string) ([]byte, error)
	// AppsBusy reports whether AppManagement lists an operation in progress,
	// or did not answer.
	AppsBusy func(ctx context.Context) bool

	mu sync.Mutex // serialises the read-modify-writes of autoupdate.json
}

// Default is the box's own: the API answers from it and main runs its checks.
var Default = New("/")

// New keeps autoupdate.json under root. Releases and Start are left to the caller.
func New(root string) *AutoUpdate {
	return &AutoUpdate{
		Root:    root,
		Now:     time.Now,
		Current: version.CurrentVersion,
		Command: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).Output()
		},
		AppsBusy: func(ctx context.Context) bool {
			return appsBusy(ctx, config.CommonInfo.RuntimePath)
		},
	}
}

func (a *AutoUpdate) path() string {
	return filepath.Join(a.Root, StateFile)
}

// Run settles the last attempt at once, since an update restarts the core, then
// checks after a random delay of up to ten minutes, which spreads the boxes a
// power cut restarted together, and every hour until ctx is done.
func (a *AutoUpdate) Run(ctx context.Context) {
	a.settle()
	delay := time.NewTimer(fromEnv(startDelayEnv, rand.N(10*time.Minute)))
	defer delay.Stop()
	select {
	case <-delay.C:
	case <-ctx.Done():
		return
	}
	ticker := time.NewTicker(max(fromEnv(intervalEnv, time.Hour), time.Millisecond))
	defer ticker.Stop()
	for {
		a.Check(ctx)
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

// fromEnv is the Go duration the install check sets in env, or fallback.
func fromEnv(env string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(env)); err == nil && d >= 0 {
		return d
	}
	return fallback
}

// Check is one hourly pass: it settles the last attempt, then starts the
// latest release when every condition of decide holds. Nothing is read or
// asked when off or outside the window, and systemd and AppManagement only
// once everything else holds. A release held is not logged: the next check
// tries again while the window lasts.
func (a *AutoUpdate) Check(ctx context.Context) {
	a.settle()
	now := a.Now()
	s := a.load()
	if !s.Enabled || s.window().earliest(now, s.attempted()).After(now) {
		return
	}
	f := facts{now: now, state: s, current: a.Current(), latest: a.Releases.FetchCasaosVersion()}
	if !decide(f).start {
		return
	}
	f.unitActive, f.appsBusy = a.unitActive(), a.AppsBusy(ctx)
	if v := decide(f); v.start {
		a.start(v.next.Version, now)
	}
}

// start records the attempt, then starts the button's update. The attempt is
// saved first: a core stopped in between still finds it and settles it. A start
// that fails is failed at once.
func (a *AutoUpdate) start(release string, now time.Time) {
	last := Last{Version: release, StartedAt: now.UTC().Truncate(time.Second), Result: resultRunning}
	if err := a.modify(func(s *State) bool { s.Last = &last; return true }); err != nil {
		logger.Info("automatic update: cannot save autoupdate.json, not starting", zap.String("version", release), zap.Error(err))
		return
	}
	logger.Info("automatic update: starting", zap.String("version", release))
	if err := a.Start(release); err != nil {
		logger.Info("automatic update: cannot start", zap.String("version", release), zap.Error(err))
		if err := a.modify(func(s *State) bool {
			if s.Last == nil || s.Last.Result != resultRunning {
				return false
			}
			fail(s)
			return true
		}); err != nil {
			logger.Info("automatic update: cannot save autoupdate.json", zap.Error(err))
		}
	}
}

// settle concludes the last attempt while it runs: succeeded once fork-release
// is its release, failed once casaos-update no longer runs without it.
func (a *AutoUpdate) settle() {
	if err := a.modify(func(s *State) bool {
		if s.Last == nil || s.Last.Result != resultRunning {
			return false
		}
		// The unit first: while the installer runs, fork-release may still be
		// put back by a run that fails after its copy. Once it is over, the
		// release it attempted, or a newer one it found at download time, counts
		// as installed.
		if a.unitActive() {
			return false // still running
		}
		installed, attempted := strings.TrimPrefix(a.Current(), "v"), strings.TrimPrefix(s.Last.Version, "v")
		if installed == attempted || version.IsVersionNewer(installed, attempted) {
			s.Last.Result, s.Failures = resultSucceeded, nil
			logger.Info("automatic update: succeeded", zap.String("version", s.Last.Version), zap.String("installed", installed))
		} else {
			fail(s)
		}
		return true
	}); err != nil {
		logger.Info("automatic update: cannot save autoupdate.json", zap.Error(err))
	}
}

// fail marks the last attempt failed and counts it against its release, whose
// second failure pauses it; a failure of another release starts a new count.
func fail(s *State) {
	s.Last.Result = resultFailed
	if s.Failures == nil || s.Failures.Version != s.Last.Version {
		s.Failures = &Failures{Version: s.Last.Version}
	}
	s.Failures.Count++
	logger.Info("automatic update: failed", zap.String("version", s.Last.Version), zap.Int("failures", s.Failures.Count))
	if s.Failures.Count >= 2 {
		logger.Info("automatic update: paused until resumed or a newer release", zap.String("version", s.Last.Version))
	}
}

// unitActive reports whether casaos-update runs. systemctl is-active prints the
// state and exits non-zero unless active, so the output is the answer; no
// answer counts as active: when in doubt, nothing starts.
func (a *AutoUpdate) unitActive() bool {
	output, _ := a.Command("systemctl", "is-active", common.UPDATE_UNIT)
	switch strings.TrimSpace(string(output)) {
	case "inactive", "failed", "unknown":
		return false
	}
	return true
}

// appsBusy asks AppManagement whether an app operation runs, the way the core
// reaches the other services: at the address it leaves in the runtime path,
// with this boot's secret. No answer, or any operation listed, is busy.
func appsBusy(ctx context.Context, runtimePath string) bool {
	address, err := os.ReadFile(filepath.Join(runtimePath, external.AppManageURLFilename))
	if err != nil {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(strings.TrimSpace(string(address)), "/")+operationsPath, nil)
	if err != nil {
		return true
	}
	_ = external.InternalRequestEditor(runtimePath)(ctx, request) // never fails
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return true
	}
	defer response.Body.Close()
	var body struct {
		Operations *[]json.RawMessage `json:"operations"`
	}
	return response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&body) != nil ||
		body.Operations == nil || len(*body.Operations) > 0
}
