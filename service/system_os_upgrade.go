package service

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
	"github.com/google/uuid"
)

// The upgrade of the operating system, as the page runs it: the core checks, writes the run folder, and starts the unit that runs its blocks; it reads the unit's log
// and the files of the blocks to say where the run is, and never runs apt or dpkg itself. This file has what the routes of it share: the codes that a request is
// refused with, the shape of the start, and the switch of the feature. The rest is in system_os_upgrade_*.go: the start, the run folder and its launches, the
// reading of a run, the status.

// The codes of what the upgrade refuses (OSUpgradeRefusal.Code). not_root, identity_unsupported and hop_not_planned are the plan's own words, said again.
const (
	OSUpgradeBadRequest           = "bad_request"
	OSUpgradeBetaOff              = "beta_off"
	OSUpgradeRunOpen              = "run_open"
	OSUpgradeManualRunFound       = "manual_run_found"
	OSUpgradeNoPlan               = "no_plan"
	OSUpgradePlanChanged          = "plan_changed"
	OSUpgradePlanBlocked          = "plan_blocked"
	OSUpgradePlanOlderThanRefresh = "plan_older_than_refresh"
	OSUpgradeBadSafetyNet         = "bad_safety_net"
	OSUpgradeHostnameMismatch     = "hostname_mismatch"
	OSUpgradeAcksMissing          = "acks_missing"
	OSUpgradeRemovalsMismatch     = "removals_mismatch"
	OSUpgradePausedMismatch       = "paused_mismatch"
	OSUpgradeSourcesChanged       = "sources_changed"
	OSUpgradeReadinessRefuses     = "readiness_refuses"
	OSUpgradeBusy                 = "busy"
	OSUpgradeAppsBusy             = "apps_busy"
	OSUpgradeNoRoom               = "no_room"
	OSUpgradeRunbookUnsafe        = "runbook_unsafe"
	OSUpgradeRunbookTooLarge      = "runbook_too_large"
	OSUpgradeStartFailed          = "start_failed"
)

// OSUpgradeStartCodes are the codes that a start can be refused with, in the order of its checks, the three of the plan's included.
var OSUpgradeStartCodes = []string{
	OSUpgradeBadRequest, OSUpgradeBetaOff, osplan.CodeNotRoot, osplan.CodeIdentityUnsupported, osplan.CodeHopNotPlanned, OSUpgradeRunOpen,
	OSUpgradeManualRunFound, OSUpgradeNoPlan, OSUpgradePlanChanged, OSUpgradePlanBlocked, OSUpgradePlanOlderThanRefresh, OSUpgradeBadSafetyNet,
	OSUpgradeHostnameMismatch, OSUpgradeAcksMissing, OSUpgradeRemovalsMismatch, OSUpgradePausedMismatch, OSUpgradeSourcesChanged,
	OSUpgradeReadinessRefuses, OSUpgradeBusy, OSUpgradeAppsBusy, OSUpgradeNoRoom, OSUpgradeRunbookUnsafe, OSUpgradeRunbookTooLarge, OSUpgradeStartFailed,
}

// OSUpgradeRefusal is a request that was refused, with a code that the page puts in words. Detail is English, for whoever reads the raw answer; Data is what makes
// the code precise (never the name that the owner typed for the machine, nor the machine's own). The route says bad_request and start_failed with their own
// statuses and the others with 409.
type OSUpgradeRefusal struct {
	Code   string
	Detail string
	Data   map[string]any
}

func (r *OSUpgradeRefusal) Error() string { return r.Detail }

func refuseUpgrade(code, detail string, data map[string]any) *OSUpgradeRefusal {
	if data == nil {
		data = map[string]any{}
	}
	return &OSUpgradeRefusal{Code: code, Detail: detail, Data: data}
}

// The acknowledgements that the owner gives with a start, and the safety nets that they name.
const (
	osUpgradeAckNoInterruption = "no_interruption"
	osUpgradeAckNoWayBack      = "no_way_back"
	osUpgradeAckSourcesPaused  = "sources_paused"
	osUpgradeAckRemovalsRead   = "removals_read"
)

var (
	osUpgradeAcks       = []string{osUpgradeAckNoInterruption, osUpgradeAckNoWayBack, osUpgradeAckSourcesPaused, osUpgradeAckRemovalsRead}
	osUpgradeSafetyNets = []string{"snapshot", "console_and_backup", "reinstall_ok"}
)

// OSUpgradeStartRequest is the body of the start of an upgrade, which is checked field by field before anything is done: the plan that the owner read, what they
// said stands between them and a machine that does not come back, the name of the machine, the removals and the sources put aside that they accepted, and the
// acknowledgements that the plan asks for.
type OSUpgradeStartRequest struct {
	PlanID         string   `json:"plan_id"`
	Rehearsal      bool     `json:"rehearsal"`
	SafetyNet      string   `json:"safety_net"`
	Hostname       string   `json:"hostname"`
	AcceptRemovals []string `json:"accept_removals"`
	PausedSources  []string `json:"paused_sources"`
	Acks           []string `json:"acks"`
}

// What the start calls, as vars for a test to put its own in the place of.
var (
	// osUpgradeHostname is the name of the machine; osUpgradeNewRunID makes the id of a run.
	osUpgradeHostname = os.Hostname
	osUpgradeNewRunID = func() string { return uuid.NewString() }
	// osUpgradeTakeFacts takes the facts of the machine before the hop, for the judge after it.
	osUpgradeTakeFacts = func(ctx context.Context, p osreadiness.Probe) osverify.Facts { return osverify.Take(ctx, p) }
	// osUpgradeRewrite rewrites the sources that are on the box now, for the check that they are the ones that the plan was made from.
	osUpgradeRewrite = func(ctx context.Context, hop osplan.HopSpec, files osplan.Files) (*osplan.Rewritten, error) {
		safe := osreadiness.Guard(files)
		sources, err := osreadiness.ReadSourceFiles(ctx, safe)
		if err != nil {
			return nil, err
		}
		return osplan.RewriteSources(ctx, hop, sources, osreadiness.NewDistroDecider(safe))
	}
	// newOSUpgradeProbe is the probe that the start runs the report and its questions through: the one of the plan, with the unit of the upgrade as the caller, so that
	// the start is not kept away by its own unit or its own flag.
	newOSUpgradeProbe = func(u *systemPackageUpdater) osReadinessProbe {
		return osReadinessProbe{updater: u, except: systemOSUpgradeUnit}
	}
)

// SetSystemOSUpgradeBeta turns the feature on or off: the flag file that it is gated by (/etc/casaos/os-upgrade.beta). It gates what begins something, and nothing
// else: a run that is open goes on when it is turned off, so that a machine that is half upgraded is never trapped behind a switch; and so it is not turned off
// while one is open (run_open).
func (s *systemService) SetSystemOSUpgradeBeta(enabled bool) error {
	osUpgradeMu.Lock()
	defer osUpgradeMu.Unlock()
	if !enabled {
		if refusal := s.osUpgradeOpenRefusal(); refusal != nil {
			return refusal
		}
		if err := os.Remove(osUpgradeBetaFile); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(osUpgradeBetaFile), 0o755); err != nil {
		return err
	}
	return osUpgradeWriteFile(osUpgradeBetaFile, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

// osUpgradeOpenRefusal is run_open when a run is open: busy, or ended well and not yet closed, or one that cannot be read; nil when there is none or the latest has
// been closed or left (a refresh, a rehearsal, a failure before the point of no return).
func (s *systemService) osUpgradeOpenRefusal() *OSUpgradeRefusal {
	_, _, refusal := s.osUpgradeLatest()
	return refusal
}

// osUpgradeBlocksStart is whether a state of a run keeps a new one from starting: the machine is busy, or the run ended well and the owner has not closed it.
func osUpgradeBlocksStart(state osrun.State) bool {
	switch state.Name {
	case osrun.StateSucceeded, osrun.StateSucceededWithFindings:
		return true
	case osrun.StateIdle:
		// a run that was begun and has not had its first attempt is on its way; one with no run in its log is what a crash left of a start
		return !state.Closed && state.RunID != ""
	}
	return state.Busy
}
