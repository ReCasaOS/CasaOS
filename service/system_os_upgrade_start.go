package service

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"go.uber.org/zap"
)

var (
	osUpgradeHostPattern   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	osUpgradeNamePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9+.:~-]{0,127}$`)
	osUpgradePausedPattern = regexp.MustCompile(`^[A-Za-z0-9_:.-]{1,200}$`)
)

const (
	osUpgradeMaxListed = 200
	// the room that a run needs in the folder of the runs and in the folder of backups, and what it adds to the size of /etc, of which the backup keeps a copy
	// and the blocks a second one (the sources of apt)
	osUpgradeMinRoom   = 128 << 20
	osUpgradeEtcExtra  = 64 << 20
	osUpgradeEtcFactor = 2
	osUpgradeAskLimit  = 5 * time.Minute
)

// osUpgradeEtcSize is the size of what /etc holds, which the backup copies. A var, for the tests.
var osUpgradeEtcSize = func(ctx context.Context) (int64, error) { return sizeOfFolder(ctx, "/etc") }

// sizeOfFolder is the sum of the sizes of the regular files below a folder: links are not followed, and a file that goes away as it is counted is not counted.
func sizeOfFolder(ctx context.Context, root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			// a file that goes away as it is counted is not a reason to say that /etc cannot be counted
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}

// osUpgradeBadField is the first field of a request that is not of the shape it has: a name that is none, a list that is too long, an acknowledgement that does not
// exist. What a field says is checked later, against the plan; this is only that it is a name and not something else.
func osUpgradeBadField(req OSUpgradeStartRequest) string {
	list := func(items []string, pattern *regexp.Regexp) bool {
		if len(items) > osUpgradeMaxListed {
			return false
		}
		for _, item := range items {
			if !pattern.MatchString(item) {
				return false
			}
		}
		return true
	}
	switch {
	case !osplan.ValidPlanID(req.PlanID):
		return "plan_id"
	case !osUpgradeHostPattern.MatchString(req.Hostname):
		return "hostname"
	case !list(req.AcceptRemovals, osUpgradeNamePattern):
		return "accept_removals"
	case !list(req.PausedSources, osUpgradePausedPattern):
		return "paused_sources"
	case len(req.Acks) > len(osUpgradeAcks)*4 || slices.ContainsFunc(req.Acks, func(a string) bool { return !slices.Contains(osUpgradeAcks, a) }):
		return "acks"
	}
	return ""
}

// osUpgradeLatest is the latest run, as the checks of a start and of the switch read it: the run, what the reducer says of it, and run_open when it keeps a new one
// from starting. A pointer that says nothing, and a run that cannot be read, are a run that is open and needs attention: the machine is not known to be free of it.
func (s *systemService) osUpgradeLatest() (*osUpgradeRun, osrun.State, *OSUpgradeRefusal) {
	return s.osUpgradeLatestWith(s.osUpgradeSharedUnit())
}

// osUpgradeLatestWith is osUpgradeLatest with the answer of systemd that the caller has, so that a caller that holds the lock does not ask.
func (s *systemService) osUpgradeLatestWith(unit osrun.Unit) (*osUpgradeRun, osrun.State, *OSUpgradeRefusal) {
	id, current := currentOSUpgrade()
	switch current {
	case osUpgradeNoRun:
		return nil, osrun.State{Name: osrun.StateIdle}, nil
	case osUpgradeBadRun:
		return nil, osrun.State{Name: osrun.StateNeedsAttention}, refuseUpgrade(OSUpgradeRunOpen, "a run is open and cannot be read", map[string]any{"state": osrun.StateNeedsAttention, "run_id": ""})
	}
	run, err := readOSUpgradeRun(id)
	if err != nil {
		return nil, osrun.State{Name: osrun.StateNeedsAttention}, refuseUpgrade(OSUpgradeRunOpen, "a run is open and cannot be read", map[string]any{"state": osrun.StateNeedsAttention, "run_id": id})
	}
	state := osrun.Reduce(s.osUpgradeInputWith(run, unit))
	if osUpgradeBlocksStart(state) {
		return run, state, refuseUpgrade(OSUpgradeRunOpen, "an upgrade is open: it has to be finished or closed first", map[string]any{"state": state.Name, "run_id": run.ID})
	}
	return run, state, nil
}

// osUpgradePlan is the plan that the owner read, when it is the live one and is good for a start: no_plan with the reason when there is none (the state of the job,
// or that it expired), plan_changed when another one has replaced it, and plan_blocked when it has a problem that nothing can accept.
func (s *systemService) osUpgradePlan(planID string) (*osplan.Plan, *OSUpgradeRefusal) {
	st := s.osPlan.state.Load()
	reason := ""
	switch {
	case st == nil:
		reason = "idle"
	case st.state == osPlanRunning:
		reason = "running"
	case st.state == osPlanFailed:
		reason = "failed"
	case st.state != osPlanDone || st.plan == nil:
		reason = "idle"
	case st.plan.Expired(s.systemPackageUpdater().now()):
		reason = "expired"
	}
	if reason != "" {
		return nil, refuseUpgrade(OSUpgradeNoPlan, "there is no plan to start from ("+reason+")", map[string]any{"reason": reason})
	}
	plan := st.plan
	if plan.PlanID != planID {
		return nil, refuseUpgrade(OSUpgradePlanChanged, "the plan is not the one that was read", map[string]any{"current": plan.PlanID})
	}
	if plan.Verdict == osplan.VerdictBlocked {
		var problems []osplan.Problem
		for _, p := range plan.Problems {
			if p.Severity == osplan.SeverityBlocked {
				problems = append(problems, p)
			}
		}
		return nil, refuseUpgrade(OSUpgradePlanBlocked, "the plan has problems that nothing can accept", map[string]any{"problems": problems})
	}
	return plan, nil
}

// hasDoneFile says whether a folder holds the mark of a block that ended (done-<block>): a block that the owner ran by hand and that went no further leaves nothing else, and a run
// that began in its folder would skip the blocks that those marks name.
func hasDoneFile(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "done-") {
			return true
		}
	}
	return false
}

// osUpgradeManualFolder is a backup folder that a hop that is not this engine's left half done: it has the copy of the sources, the marker of the point of no
// return or the mark of a block that ended, and no mark that the hop ended. The latest run's own folder is not one. The error is a folder of backups that cannot be read.
func osUpgradeManualFolder(current *osUpgradeRun) (string, error) {
	entries, err := os.ReadDir(osUpgradeBackups)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	own := ""
	if current != nil {
		own = osUpgradeBackupFolder(current.Meta.BK)
	}
	for _, e := range entries {
		dir := filepath.Join(osUpgradeBackups, e.Name())
		if !strings.HasPrefix(e.Name(), "casaos-os-upgrade-") || dir == own {
			continue
		}
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
			continue
		}
		has := func(name string) bool { _, err := os.Lstat(filepath.Join(dir, name)); return err == nil }
		if (has("apt-orig") || has("begun-upgrade") || hasDoneFile(dir)) && !has("done-after") {
			return e.Name(), nil
		}
	}
	return "", nil
}

// osUpgradeManualRemovals are the removals that the plan asks the owner to accept, sorted.
func osUpgradeManualRemovals(p *osplan.Plan) []string {
	var names []string
	for _, pr := range p.Problems {
		if pr.Code == osplan.ProblemRemovalManual {
			if name, _ := pr.Data["name"].(string); name != "" {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return slices.Compact(names)
}

// osUpgradePausedNames are the sources that the hop puts aside, as the owner is shown them: the file, or sources.list:<line>.
func osUpgradePausedNames(p *osplan.Plan) []string {
	var names []string
	for _, paused := range p.Paused {
		if paused.Line > 0 {
			names = append(names, paused.File+":"+strconv.Itoa(paused.Line))
		} else {
			names = append(names, paused.File)
		}
	}
	sort.Strings(names)
	return slices.Compact(names)
}

// sameSet compares what the owner accepted with what the plan has, as sets: what the plan has that the owner did not take, and what the owner took that the plan does not
// have. Both are empty when they are the same.
func sameSet(plan, owner []string) (missing, extra []string) {
	owner = slices.Compact(sortedCopy(owner))
	for _, name := range plan {
		if !slices.Contains(owner, name) {
			missing = append(missing, name)
		}
	}
	for _, name := range owner {
		if !slices.Contains(plan, name) {
			extra = append(extra, name)
		}
	}
	return missing, extra
}

func sortedCopy(list []string) []string {
	out := slices.Clone(list)
	sort.Strings(out)
	return out
}

// osUpgradeRequiredAcks are the acknowledgements that a start of this plan needs: no interruption always, no way back for a hop (a rehearsal goes back), the sources
// that are put aside when there are some, and the removals when the plan has some to accept.
func osUpgradeRequiredAcks(p *osplan.Plan, rehearsal bool) []string {
	required := []string{osUpgradeAckNoInterruption}
	if !rehearsal {
		required = append(required, osUpgradeAckNoWayBack)
	}
	if len(p.Paused) > 0 {
		required = append(required, osUpgradeAckSourcesPaused)
	}
	if len(osUpgradeManualRemovals(p)) > 0 {
		required = append(required, osUpgradeAckRemovalsRead)
	}
	return required
}

// StartSystemOSUpgrade starts an upgrade of the operating system, or its rehearsal (the reversible half of it, which stops with the new release's sources in place), for
// the plan that the owner read. Every check that can refuse is made before anything is changed, the cheapest first, and the first that fails is the answer
// (*OSUpgradeRefusal); the core does not run the sandbox again, since the block that changes the sources simulates the upgrade against the real files and a
// stop there is undone. The readiness report and the questions to the machine are asked outside osUpgradeMu, which is taken again afterwards and the plan read again.
// Then, in this order: the facts of the machine before, the run folder (SHA256SUMS last), the flag of the open upgrade, the pointer to the run, the first lines of the
// log, the attempt, and the unit.
func (s *systemService) StartSystemOSUpgrade(req OSUpgradeStartRequest) (OSUpgradeStatus, error) {
	u := s.systemPackageUpdater()
	if field := osUpgradeBadField(req); field != "" {
		return OSUpgradeStatus{}, refuseUpgrade(OSUpgradeBadRequest, "a field of the request is not valid: "+field, map[string]any{"field": field})
	}
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
	plan, refusal := s.osUpgradePlan(req.PlanID)
	if refusal != nil {
		return OSUpgradeStatus{}, refusal
	}
	if latest != nil {
		// a refresh that ended and has not been looked at yet by the loop: its end is written down now, so that a plan made before it is refused
		s.osUpgradeNoteRefresh(latest)
	}
	if refusal := osUpgradeNewerRefresh(plan); refusal != nil {
		return OSUpgradeStatus{}, refusal
	}
	if refusal := s.osUpgradeCheckBody(req, plan); refusal != nil {
		return OSUpgradeStatus{}, refusal
	}
	if refusal := s.osUpgradeCheckSources(ctx, hop, plan, probe); refusal != nil {
		return OSUpgradeStatus{}, refusal
	}
	if refusal := s.osUpgradeCheckMachine(ctx, probe, nil); refusal != nil {
		return OSUpgradeStatus{}, refusal
	}
	// the report took a while: a newer plan may have replaced the one that the owner read
	plan, refusal = s.osUpgradePlan(req.PlanID)
	if refusal != nil {
		return OSUpgradeStatus{}, refusal
	}

	runID := osUpgradeNewRunID()
	auto, err := osplan.BuildAuto(*plan, osplan.AutoParams{RunID: runID, Hostname: req.Hostname})
	if err != nil {
		return OSUpgradeStatus{}, osUpgradeTextRefusal(err)
	}
	manual, err := osplan.BuildRunbook(*plan)
	if err != nil {
		return OSUpgradeStatus{}, osUpgradeTextRefusal(err)
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return OSUpgradeStatus{}, osUpgradeStartFailed("the plan cannot be written", err)
	}
	nonce, err := dockerpkg.NewNonce()
	if err != nil {
		return OSUpgradeStatus{}, osUpgradeStartFailed("no nonce could be made", err)
	}
	factsCtx, factsCancel := context.WithTimeout(ctx, 2*time.Minute)
	facts := osUpgradeTakeFacts(factsCtx, probe)
	factsCancel()
	// the facts took a while: a plan that ran out meanwhile is not started
	if _, refusal := s.osUpgradePlan(req.PlanID); refusal != nil {
		return OSUpgradeStatus{}, refusal
	}

	phase, kind, busyState := osrun.PhaseRun, osrun.RunHop, osrun.StateRunning
	if req.Rehearsal {
		phase, kind, busyState = osrun.PhaseRehearse, osrun.RunRehearsal, osrun.StateRehearsing
	}
	spec := osUpgradeRunSpec{
		Meta: osUpgradeRunMeta{
			RunID: runID, Kind: kind, Hop: plan.Hop, PlanID: plan.PlanID, SafetyNet: req.SafetyNet,
			BK: osUpgradeUnitBackups + "/casaos-os-upgrade-" + plan.PlanID, CreatedAt: u.now().UTC(),
			Blocks: osUpgradeBlockInfos(auto.Blocks, auto.Forward),
		},
		Driver: osrun.Driver, Blocks: auto.Blocks, Forward: auto.Forward, Plan: planJSON, Manual: manual.Text, Facts: facts,
	}

	return s.osUpgradeStartRun(ctx, probe, spec, nonce, phase, busyState)
}

// osUpgradeNewerRefresh is plan_older_than_refresh: the old release was brought to its latest state after the plan was made, and a plan is of the machine as it was.
// A time that cannot be read is no time: the refresh writes it, and a plan that is stale is stopped by the first block that simulates.
func osUpgradeNewerRefresh(plan *osplan.Plan) *OSUpgradeRefusal {
	data, err := readOSUpgradeFile(filepath.Join(osUpgradeRoot, osUpgradeLastRefreshFile), 256)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logger.Error("the time of the last refresh could not be read", zap.Error(err))
		}
		return nil
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil || !at.After(plan.MadeAt) {
		return nil
	}
	return refuseUpgrade(OSUpgradePlanOlderThanRefresh, "the old release was refreshed after the plan was made", nil)
}

// osUpgradeCheckBody holds what the owner said to what the plan has: the safety net, the name of the machine, the acknowledgements, the removals and the sources put
// aside. The names of the machine and of the removals are the owner's: the one is never echoed, and the unit is written with the plan's own removals, never theirs.
func (s *systemService) osUpgradeCheckBody(req OSUpgradeStartRequest, plan *osplan.Plan) *OSUpgradeRefusal {
	if !(req.Rehearsal && req.SafetyNet == "") && !slices.Contains(osUpgradeSafetyNets, req.SafetyNet) {
		return refuseUpgrade(OSUpgradeBadSafetyNet, "the safety net is not one of the three", map[string]any{"allowed": osUpgradeSafetyNets})
	}
	if name, err := osUpgradeHostname(); err != nil || name != req.Hostname {
		return refuseUpgrade(OSUpgradeHostnameMismatch, "the name typed is not the name of this machine", nil)
	}
	var missing []string
	for _, ack := range osUpgradeRequiredAcks(plan, req.Rehearsal) {
		if !slices.Contains(req.Acks, ack) {
			missing = append(missing, ack)
		}
	}
	if len(missing) > 0 {
		return refuseUpgrade(OSUpgradeAcksMissing, "the owner has not acknowledged what the plan asks", map[string]any{"missing": missing})
	}
	if missing, extra := sameSet(osUpgradeManualRemovals(plan), req.AcceptRemovals); len(missing)+len(extra) > 0 {
		return refuseUpgrade(OSUpgradeRemovalsMismatch, "the removals that were accepted are not the plan's", map[string]any{"missing": orEmpty(missing), "extra": orEmpty(extra)})
	}
	if missing, extra := sameSet(osUpgradePausedNames(plan), req.PausedSources); len(missing)+len(extra) > 0 {
		return refuseUpgrade(OSUpgradePausedMismatch, "the sources that were put aside are not the plan's", map[string]any{"missing": orEmpty(missing), "extra": orEmpty(extra)})
	}
	return nil
}

func orEmpty(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// osUpgradeCheckSources is sources_changed: the files of apt that are on the box now give the tree that the plan was made with, byte for byte (its two hashes).
func (s *systemService) osUpgradeCheckSources(ctx context.Context, hop osplan.HopSpec, plan *osplan.Plan, probe osReadinessProbe) *OSUpgradeRefusal {
	rewriteCtx, cancel := context.WithTimeout(ctx, osPlanAskTimeout)
	defer cancel()
	rewritten, err := osUpgradeRewrite(rewriteCtx, hop, probe)
	if err != nil || rewritten == nil || rewritten.HashCurrent != plan.Sources.HashCurrent || rewritten.HashGenerated != plan.Sources.HashGenerated {
		if err != nil {
			logger.Info("the sources could not be rewritten again for the start of an upgrade", zap.Error(err))
		}
		return refuseUpgrade(OSUpgradeSourcesChanged, "the sources of apt are not the ones that the plan was made from", nil)
	}
	return nil
}

// osUpgradeCheckMachine asks the machine: the readiness report (which is where the pending restart, a broken dpkg, held packages, the boot loader, the room and the
// age of the backup speak), whether something else changes the packages, whether an app has an operation, and whether there is room for the run folder and the backup.
// The unit of the upgrade is the caller's own, which is not counted against it. stands says which checks of the report are refusals for the caller: nil is all of them, and a
// refresh gives the few that it stands on (osRefreshStands).
func (s *systemService) osUpgradeCheckMachine(ctx context.Context, probe osReadinessProbe, stands func(code string) bool) *OSUpgradeRefusal {
	u := probe.updater
	report := runOSReadiness(ctx, probe, osreadiness.Options{})
	var checks []map[string]any
	for _, f := range report.Checks {
		if f.Severity == osreadiness.Refuse && (stands == nil || stands(f.Code)) {
			checks = append(checks, map[string]any{"code": f.Code, "action": string(f.Action)})
		}
	}
	if len(checks) > 0 || report.State == osreadiness.StateUnsupported || report.State == osreadiness.StateLatest {
		return refuseUpgrade(OSUpgradeReadinessRefuses, "the machine is not ready for the upgrade", map[string]any{"checks": orEmptyChecks(checks), "state": report.State})
	}

	busyCtx, cancel := context.WithTimeout(ctx, osPlanAskTimeout)
	reason, busy, err := probe.MaintenanceBusy(busyCtx)
	cancel()
	switch {
	case busy:
		return refuseUpgrade(OSUpgradeBusy, "something else is changing the packages of the machine", map[string]any{"reason": reason})
	case err != nil:
		return refuseUpgrade(OSUpgradeBusy, "whether something else is changing the packages could not be seen", map[string]any{"reason": "unknown"})
	}
	if unit := u.osUpgradeUnit(); !unit.Answered || unit.Active {
		return refuseUpgrade(OSUpgradeBusy, "a unit of the upgrade runs, or systemd does not answer", map[string]any{"reason": "unit"})
	}

	if refusal := osUpgradeAppsRefusal(ctx, probe, osPlanAskTimeout); refusal != nil {
		return refusal
	}
	return s.osUpgradeCheckRoom(ctx, u)
}

// osUpgradeAppsRefusal is apps_busy when AppManagement has an operation, or does not say whether it has, within the time it is given.
func osUpgradeAppsRefusal(ctx context.Context, probe osReadinessProbe, limit time.Duration) *OSUpgradeRefusal {
	appsCtx, cancel := context.WithTimeout(ctx, limit)
	apps, err := probe.AppOperations(appsCtx)
	cancel()
	switch {
	case err != nil:
		return refuseUpgrade(OSUpgradeAppsBusy, "AppManagement did not say whether an app has an operation", map[string]any{"apps": []string{}, "unknown": true})
	case len(apps) > 0:
		return refuseUpgrade(OSUpgradeAppsBusy, "an app has an operation in progress", map[string]any{"apps": apps})
	}
	return nil
}

func orEmptyChecks(checks []map[string]any) []map[string]any {
	if checks == nil {
		return []map[string]any{}
	}
	return checks
}

// osUpgradeCheckRoom is no_room: the folder of the runs and the folder of backups each have at least 128 MiB, and the backup volume has twice the size of /etc and
// 64 MiB, the copy of /etc that the backup keeps and room for the sources.
func (s *systemService) osUpgradeCheckRoom(ctx context.Context, u *systemPackageUpdater) *OSUpgradeRefusal {
	etc, etcErr := osUpgradeEtcSize(ctx)
	needBackups := uint64(osUpgradeMinRoom)
	if want := uint64(max(etc, 0))*osUpgradeEtcFactor + osUpgradeEtcExtra; want > needBackups {
		needBackups = want
	}
	for _, place := range []struct {
		path string
		need uint64
	}{{filepath.Dir(osUpgradeRoot), osUpgradeMinRoom}, {osUpgradeBackups, needBackups}} {
		var free uint64
		var err error
		if u.freeBytes != nil {
			free, err = u.freeBytes(place.path)
		} else {
			err = errors.New("the room left cannot be read here")
		}
		if err != nil || (place.path == osUpgradeBackups && etcErr != nil) {
			return refuseUpgrade(OSUpgradeNoRoom, "the room left on the machine could not be read", map[string]any{"path": place.path, "need": place.need, "unknown": true})
		}
		if free < place.need {
			return refuseUpgrade(OSUpgradeNoRoom, "there is not room for the upgrade", map[string]any{"path": place.path, "need": place.need, "free": free})
		}
	}
	return nil
}

// osUpgradeTextRefusal is what a plan that cannot be written as text for the unit is refused with.
func osUpgradeTextRefusal(err error) error {
	switch {
	case errors.Is(err, osplan.ErrRunbookTooLarge):
		return refuseUpgrade(OSUpgradeRunbookTooLarge, err.Error(), map[string]any{"reason": "runbook_too_large"})
	case errors.Is(err, osplan.ErrRunbookUnsafe):
		return refuseUpgrade(OSUpgradeRunbookUnsafe, err.Error(), map[string]any{"reason": "runbook_unsafe"})
	case errors.Is(err, osplan.ErrNoRunbook):
		return refuseUpgrade(OSUpgradeNoPlan, "there is no runbook for this plan", map[string]any{"reason": "unavailable"})
	}
	return osUpgradeStartFailed("the text of the unit could not be written", err)
}

// osUpgradeStartFailed is a start that broke on the machine's side: nothing was changed, and the cause is in the log of the core and not in the answer.
func osUpgradeStartFailed(detail string, cause error) *OSUpgradeRefusal {
	logger.Error("the upgrade of the operating system could not be started", zap.String("detail", detail), zap.Error(cause))
	return refuseUpgrade(OSUpgradeStartFailed, detail, map[string]any{"detail": detail})
}
