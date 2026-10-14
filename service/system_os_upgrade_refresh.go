package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// The refresh of the release that the machine runs: the owner's click that brings it to its last state before the plan is made. It is a run of its own kind (osrun.RunRefresh), with
// a phase and four blocks of its own and no point of no return, that the unit runs as it runs the rest; what the core does for it is what it does for a start: the checks, the run
// folder, the flag of the open upgrade, the first attempt.

// OSUpgradeSourceUnrecognized is the one code that only a refresh says: the sources of the machine are in a form that the refresh cannot write. A repository that the report cannot
// tell is the usual one ("run apt-get update first"), and the plan says the same of it in its own words.
const OSUpgradeSourceUnrecognized = "source_unrecognized"

// OSUpgradeRefreshCodes are the codes that a refresh can be refused with, in the order of its checks, the three of the plan's included.
var OSUpgradeRefreshCodes = []string{
	OSUpgradeBadRequest, OSUpgradeBetaOff, osplan.CodeNotRoot, osplan.CodeIdentityUnsupported, osplan.CodeHopNotPlanned, OSUpgradeRunOpen, OSUpgradeManualRunFound,
	OSUpgradeReadinessRefuses, OSUpgradeBusy, OSUpgradeAppsBusy, OSUpgradeNoRoom, OSUpgradeSourceUnrecognized, OSUpgradeRunbookUnsafe, OSUpgradeRunbookTooLarge, OSUpgradeStartFailed,
}

// OSUpgradeRefreshRequest is the body of the start of a refresh. AcceptReleaseInfoChange is the owner's word that a repository whose release information changed ("stable" that
// became "oldstable" when Debian 12 came out) is to be taken: apt refuses it unless it is told, and the refresh that stopped for it says so (failure.hint, releaseinfo_change).
type OSUpgradeRefreshRequest struct {
	AcceptReleaseInfoChange bool `json:"accept_releaseinfo_change"`
}

// osRefreshChecks are the checks of the readiness report that stand in the way of a refresh, by their codes, and the sources' (every code that begins with sources_). The others
// are the hop's: a restart that waits, the boot loader, the file systems, the age of the backup. A refresh does not look at them, and does not refuse for them.
var osRefreshChecks = []string{"busy", "apps", "broken_dpkg", "held", "disk_root", "disk_archives", "disk_boot"}

func osRefreshStands(code string) bool {
	return slices.Contains(osRefreshChecks, code) || strings.HasPrefix(code, "sources_")
}

// osUpgradeRefreshManual is what the owner who takes a refresh over is given: there is nothing to run by hand (a refresh is apt-get update and apt-get dist-upgrade, and it ends by
// putting the sources and the timers back, which the unit does whether it ended or stopped).
const osUpgradeRefreshManual = "# This run is a refresh of the old release: apt-get update and apt-get dist-upgrade, with the sources of Debian that the machine has and nothing else.\n# It has no text for the owner to run by hand. When it ends, or stops, the unit puts the sources and the timers of apt back as they were.\n"

// osUpgradeRefreshRecord is plan.json of a refresh: there is no plan, and what the run kept of the sources is what it needs to be read again, the owner's own lines included.
type osUpgradeRefreshRecord struct {
	Hop               string            `json:"hop"`
	HashCurrent       string            `json:"hash_current"`
	HashGenerated     string            `json:"hash_generated"`
	AcceptReleaseInfo bool              `json:"accept_releaseinfo_change"`
	Tree              []osplan.TreeFile `json:"tree"`
	Paused            []osplan.Paused   `json:"paused"`
}

// StartSystemOSUpgradeRefresh starts the refresh of the release that the machine runs. Every check that can refuse is made before anything is changed, the cheapest first, and the
// first that fails is the answer (*OSUpgradeRefusal): the switch, who the machine is, whether a run is open or a hop by hand was left half done, the checks of the report that a
// refresh stands on (osRefreshChecks), whether something else changes the packages or an app has an operation, the room, and last the sources, which are read and rewritten
// as a refresh has them (the machine's own, with what is not Debian's put aside) and which the report may not have been able to tell. Then, in this order, as for a start: the run
// folder (SHA256SUMS last), the flag of the open upgrade, the pointer to the run, the first lines of the log, the attempt, and the unit.
func (s *systemService) StartSystemOSUpgradeRefresh(req OSUpgradeRefreshRequest) (OSUpgradeStatus, error) {
	u := s.systemPackageUpdater()
	if !osUpgradeBetaOn() {
		return OSUpgradeStatus{}, refuseUpgrade(OSUpgradeBetaOff, "the upgrade of the operating system is not switched on", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), osUpgradeAskLimit)
	defer cancel()
	probe := newOSUpgradeProbe(u)

	askCtx, askCancel := context.WithTimeout(ctx, osPlanAskTimeout)
	hop, failure := plannableOSPlan(askCtx, probe)
	askCancel()
	if failure != nil {
		return OSUpgradeStatus{}, refuseUpgrade(failure.Code, failure.Detail, failure.Data)
	}
	latest, _, refusal := s.osUpgradeLatest()
	if refusal != nil {
		return OSUpgradeStatus{}, refusal
	}
	folder, err := osUpgradeManualFolder(latest)
	if err != nil {
		return OSUpgradeStatus{}, refuseUpgrade(OSUpgradeManualRunFound, "the folder of backups cannot be read", map[string]any{"folder": "", "unknown": true})
	}
	if folder != "" {
		return OSUpgradeStatus{}, refuseUpgrade(OSUpgradeManualRunFound, "an upgrade that was run by hand was left half done", map[string]any{"folder": folder})
	}
	if refusal := s.osUpgradeCheckMachine(ctx, probe, osRefreshStands); refusal != nil {
		return OSUpgradeStatus{}, refusal
	}
	hop = hop.ForRefresh()
	rewriteCtx, rewriteCancel := context.WithTimeout(ctx, osPlanAskTimeout)
	rewritten, err := osUpgradeRewrite(rewriteCtx, hop, probe)
	rewriteCancel()
	if err != nil || rewritten == nil {
		return OSUpgradeStatus{}, osUpgradeSourceRefusal(err)
	}

	runID := osUpgradeNewRunID()
	hostname, err := osUpgradeHostname()
	if err != nil {
		return OSUpgradeStatus{}, osUpgradeStartFailed("the name of the machine cannot be read", err)
	}
	auto, err := osplan.BuildRefresh(osplan.RefreshInputs{Hop: hop, Rewritten: rewritten, AcceptReleaseInfo: req.AcceptReleaseInfoChange, RunID: runID, Hostname: hostname})
	if err != nil {
		return OSUpgradeStatus{}, osUpgradeTextRefusal(err)
	}
	record, err := json.Marshal(osUpgradeRefreshRecord{
		Hop: string(hop.ID), HashCurrent: rewritten.HashCurrent, HashGenerated: rewritten.HashGenerated, AcceptReleaseInfo: req.AcceptReleaseInfoChange,
		Tree: rewritten.Tree, Paused: rewritten.Paused,
	})
	if err != nil {
		return OSUpgradeStatus{}, osUpgradeStartFailed("the sources of the refresh cannot be written", err)
	}
	nonce, err := dockerpkg.NewNonce()
	if err != nil {
		return OSUpgradeStatus{}, osUpgradeStartFailed("no nonce could be made", err)
	}

	spec := osUpgradeRunSpec{
		Meta: osUpgradeRunMeta{
			RunID: runID, Kind: osrun.RunRefresh, Hop: string(hop.ID), BK: osplan.RefreshFolderPrefix + runID, CreatedAt: u.now().UTC(),
			Blocks: osUpgradeBlockInfos(auto.Blocks, auto.Forward),
		},
		// no facts: the judge compares a machine with itself across a restart, and a refresh has nothing to judge
		Driver: osrun.Driver, Blocks: auto.Blocks, Forward: auto.Forward, Plan: record, Manual: osUpgradeRefreshManual, Facts: osverify.Facts{},
	}
	return s.osUpgradeStartRun(ctx, probe, spec, nonce, osrun.PhaseRefresh, osrun.StateRefreshing)
}

// osUpgradeSourceRefusal is the refusal for sources that the refresh cannot write: what the rewrite says of the file and the line (the plan's own words for them), or, for an error
// that is not the rewrite's, a start that broke on the machine's side.
func osUpgradeSourceRefusal(err error) *OSUpgradeRefusal {
	var f *osplan.Failure
	if !errors.As(err, &f) {
		return osUpgradeStartFailed("the sources of apt could not be read", err)
	}
	data := map[string]any{"code": f.Code}
	for _, key := range []string{"file", "line", "reason"} {
		if v, ok := f.Data[key]; ok {
			data[key] = v
		}
	}
	return refuseUpgrade(OSUpgradeSourceUnrecognized, f.Detail, data)
}
