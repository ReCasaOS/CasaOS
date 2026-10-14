package service

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// OSUpgradeStatus is how an upgrade of the operating system is going, as the page reads it. Every text of it is a code, which the page puts in words: a line of the
// unit's output is only ever detail, and is never compared. It is the answer of the status route, and of every route that changes a run. The field names are the page's.
type OSUpgradeStatus struct {
	BetaEnabled bool   `json:"beta_enabled"`
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	Cause       string `json:"cause,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Hop         string `json:"hop"`
	PlanID      string `json:"plan_id,omitempty"`
	Phase       string `json:"phase,omitempty"`
	Attempt     int    `json:"attempt"`
	// Block is the block that runs, or the one that stopped.
	Block        *OSUpgradeBlock `json:"block,omitempty"`
	PNR          bool            `json:"pnr"`
	Reversible   bool            `json:"reversible"`
	HalfUpgraded bool            `json:"half_upgraded"`
	// Busy says that backups and automatic updates are kept away, and the owner is asked to leave the machine alone.
	Busy           bool       `json:"busy"`
	Since          *time.Time `json:"since"`
	LastActivityAt *time.Time `json:"last_activity_at"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	// Failure is what stopped a run: the block, the class (the page has a sentence for each), the status, and the line of the unit's output that said it.
	Failure *OSUpgradeFailure `json:"failure,omitempty"`
	// Hint is what the owner can do at the console about a unit that runs and has printed nothing for a long time.
	Hint        *OSUpgradeHint         `json:"hint,omitempty"`
	NotJudged   []string               `json:"not_judged,omitempty"`
	Reboot      *OSUpgradeReboot       `json:"reboot,omitempty"`
	Barrier     *OSUpgradeBarrier      `json:"barrier,omitempty"`
	Findings    []OSUpgradeFinding     `json:"findings,omitempty"`
	NotReturned []OSUpgradeNotReturned `json:"not_returned,omitempty"`
	Manual      OSUpgradeManual        `json:"manual"`
	// Actions are the buttons that are valid now: the server's table, which the page shows as it is.
	Actions []string `json:"actions"`
	// AckWord is the word that the owner types to close a run that left the machine half upgraded; present only when it is needed.
	AckWord string `json:"ack_word,omitempty"`
	// AfterRebootHint is what the owner needs if the machine does not come back, sent before the restart, since the machine is away when it is needed.
	AfterRebootHint *OSUpgradeAfterRebootHint `json:"after_reboot_hint,omitempty"`
	BootUnknown     bool                      `json:"boot_unknown"`
	IgnoredMarkers  int                       `json:"ignored_markers"`
	LogTail         []string                  `json:"log_tail"`
}

// OSUpgradeBlock is a block of a run: its place in the list of the attempt, from 1, its id and its title.
type OSUpgradeBlock struct {
	Index int    `json:"index"`
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// OSUpgradeFailure is what stopped a run.
type OSUpgradeFailure struct {
	Stage      string `json:"stage"`
	Class      string `json:"class"`
	RC         int    `json:"rc"`
	DetailLine string `json:"detail_line,omitempty"`
	// Hint is a word for what the owner can do about it, when there is one (osrun.HintOf).
	Hint string `json:"hint,omitempty"`
}

// OSUpgradeHint is a word for what the owner can do at the console. Code is "unit_quiet": the unit of the upgrade runs and has printed nothing for QuietSeconds. There is no button for it:
// stopping a unit that changes the packages is the owner's own act at the console, and with the unit stopped the run says where it stands and is closed, or gone on with, as any run is
// (with the typed word, when the machine is half upgraded).
type OSUpgradeHint struct {
	Code         string   `json:"code"`
	QuietSeconds int      `json:"quiet_seconds"`
	Commands     []string `json:"commands"`
}

// OSUpgradeReboot is a restart that was asked for.
type OSUpgradeReboot struct {
	RequestedAt time.Time `json:"requested_at"`
	Stalled     bool      `json:"stalled"`
}

// OSUpgradeBarrier says whether the machine may be restarted, and which of the checks that stand before it do not pass.
type OSUpgradeBarrier struct {
	OK     bool                   `json:"ok"`
	Checks []OSUpgradeBarrierItem `json:"checks"`
}

// OSUpgradeBarrierItem is one check of the barrier.
type OSUpgradeBarrierItem struct {
	Code     string `json:"code"`
	OK       bool   `json:"ok"`
	Severity string `json:"severity"`
}

// OSUpgradeFinding is something that the judge found after the restart.
type OSUpgradeFinding struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"`
	Arg      string   `json:"arg,omitempty"`
	Apps     []string `json:"apps,omitempty"`
}

// OSUpgradeNotReturned is a container that ran before the upgrade and does not run after it.
type OSUpgradeNotReturned struct {
	Container string `json:"container"`
	Policy    string `json:"policy"`
}

// OSUpgradeManual is where the owner stands if they take the run over: the blocks that ended, the one to run next, whether the point of no return is behind, and the
// backup folder.
type OSUpgradeManual struct {
	BlocksDone []string `json:"blocks_done"`
	NextBlock  string   `json:"next_block,omitempty"`
	PNR        bool     `json:"pnr"`
	BKPath     string   `json:"bk_path"`
}

// OSUpgradeAfterRebootHint is what the owner keeps for the machine that does not come back.
type OSUpgradeAfterRebootHint struct {
	BKPath   string   `json:"bk_path"`
	Log      string   `json:"log"`
	Commands []string `json:"commands"`
	// Block8 is the owner's own text of the block that checks the machine after the restart, to run at a console.
	Block8 string `json:"block8"`
}

// osUpgradeBetaOn says whether the switch of the feature is on: the flag file is there. It gates what begins something, and only that. Any answer but "there" is off.
func osUpgradeBetaOn() bool {
	_, err := os.Lstat(osUpgradeBetaFile)
	return err == nil
}

// GetSystemOSUpgradeStatus is how the upgrade is going: the files of the latest run, what systemd says of its unit, the boot, and the end of its output. It takes
// no lock and runs no apt, and a poll every few seconds costs a few small reads and one question to systemd that the requests that come together share.
func (s *systemService) GetSystemOSUpgradeStatus() OSUpgradeStatus {
	status := OSUpgradeStatus{BetaEnabled: osUpgradeBetaOn()}
	id, current := currentOSUpgrade()
	switch current {
	case osUpgradeNoRun:
		return s.osUpgradeStatusOf(status, nil, osrun.Input{}, osrun.Reduce(osrun.Input{}))
	case osUpgradeBadRun:
		return s.osUpgradeDamaged(status, "")
	}
	run, err := readOSUpgradeRun(id)
	if err != nil {
		return s.osUpgradeDamaged(status, id)
	}
	in := s.osUpgradeInput(run)
	return s.osUpgradeStatusOf(status, run, in, osrun.Reduce(in))
}

// osUpgradeDamaged is the status of a run that cannot be read: the machine is not known to be free of it, so it is busy and may be half upgraded, and the owner is asked to look.
// The one thing that can be done with such a run is to let go of it, with the typed word: there is nothing of it to take over.
func (s *systemService) osUpgradeDamaged(status OSUpgradeStatus, runID string) OSUpgradeStatus {
	state := osrun.State{Name: osrun.StateNeedsAttention, Reason: osrun.ReasonRunDamaged, RunID: runID, Busy: true, HalfUpgraded: true}
	status = s.osUpgradeStatusOf(status, nil, osrun.Input{}, state)
	status.Actions = []string{osrun.ActionAcknowledge}
	return status
}

// osUpgradeStatusOf makes the status of a run from its state. run is nil for no run, or for one that cannot be read.
func (s *systemService) osUpgradeStatusOf(status OSUpgradeStatus, run *osUpgradeRun, in osrun.Input, state osrun.State) OSUpgradeStatus {
	status.State, status.Reason, status.Cause, status.RunID, status.Kind = state.Name, state.Reason, state.Cause, state.RunID, state.Kind
	status.Hop, status.PlanID, status.Phase, status.Attempt = state.Hop, state.PlanID, state.Phase, state.Attempt
	status.PNR, status.Reversible, status.HalfUpgraded, status.Busy = state.PNR, state.Reversible, state.HalfUpgraded, state.Busy
	status.BootUnknown, status.IgnoredMarkers = state.BootUnknown, in.Ignored
	status.Actions = osrun.ActionsFor(state)
	status.Manual = OSUpgradeManual{BlocksDone: []string{}}
	status.LogTail = []string{}
	if !state.Since.IsZero() {
		since := state.Since.UTC()
		status.Since = &since
	}
	if state.ExpiresAt != nil {
		expires := state.ExpiresAt.UTC()
		status.ExpiresAt = &expires
	}
	if state.NeedsAckWord() {
		status.AckWord = osrun.AckWord
	}
	if state.Name == osrun.StateFailed && !s.osUpgradePlanAlive() {
		// dropping the plan is what the button does, and there is none to drop
		status.Actions = slices.DeleteFunc(status.Actions, func(a string) bool { return a == osrun.ActionDiscard })
	}
	if run == nil {
		return status
	}
	if status.RunID == "" {
		status.RunID = run.ID
	}
	if status.Kind == "" {
		status.Kind = run.Meta.Kind
	}
	if status.Hop == "" {
		status.Hop = run.Meta.Hop
	}
	if last := osUpgradeLastActivity(run, in); !last.IsZero() {
		status.LastActivityAt = &last
	}
	titles := map[string]string{}
	for _, b := range run.Meta.Blocks {
		titles[b.ID] = b.Title
	}
	if state.Block.ID != "" {
		status.Block = &OSUpgradeBlock{Index: state.Block.Index, ID: state.Block.ID, Title: titles[state.Block.ID]}
	}
	status.Manual = osUpgradeManualOf(run, state)
	tail := readOSUpgradeLogTail(run, osrun.DefaultTiming.LogTail)
	status.LogTail = tail
	if state.Name == osrun.StateFailed || state.Name == osrun.StateStoppedAtWaypoint {
		status.Failure = osUpgradeFailureOf(state, tail)
	}
	status.Hint = osUpgradeHintOf(run, in, state)
	if at, ok := osUpgradeRebootRequested(run); ok && (state.Name == osrun.StateRebooting || state.Name == osrun.StateRebootStalled) {
		status.Reboot = &OSUpgradeReboot{RequestedAt: at, Stalled: state.Stalled}
	}
	switch state.Name {
	case osrun.StateAwaitingReboot, osrun.StateRebooting, osrun.StateRebootStalled:
		if barrier := readOSUpgradeBarrier(run); barrier != nil {
			status.Barrier = &OSUpgradeBarrier{OK: barrier.OK, Checks: []OSUpgradeBarrierItem{}}
			for _, c := range barrier.Checks {
				status.Barrier.Checks = append(status.Barrier.Checks, c)
			}
		}
		status.AfterRebootHint = osUpgradeAfterRebootHint(run)
	case osrun.StateVerifying, osrun.StateSucceeded, osrun.StateSucceededWithFindings, osrun.StateFailed:
		if verify := readOSUpgradeVerify(run); verify != nil {
			status.Findings, status.NotReturned = osUpgradeFindings(verify.Findings)
			status.NotJudged = verify.NotJudged
		}
	}
	return status
}

// osUpgradePlanAlive says whether the plan job has a plan that is still good.
func (s *systemService) osUpgradePlanAlive() bool {
	st := s.osPlan.state.Load()
	return st != nil && st.state == osPlanDone && st.plan != nil && !st.plan.Expired(s.systemPackageUpdater().now())
}

// osUpgradeLastActivity is the latest sign of life of a run: its last line of the log, or the last write of the unit's output.
func osUpgradeLastActivity(run *osUpgradeRun, in osrun.Input) time.Time {
	last := run.OutputAt
	for _, m := range run.Markers {
		if m.At.After(last) {
			last = m.At
		}
	}
	if !in.Now.IsZero() && last.After(in.Now) {
		last = in.Now
	}
	return last.UTC()
}

// osUpgradeManualOf is where the blocks of the run stand, from the files that they leave: what the owner needs to take the run over.
func osUpgradeManualOf(run *osUpgradeRun, state osrun.State) OSUpgradeManual {
	manual := OSUpgradeManual{BlocksDone: []string{}, PNR: state.PNR, BKPath: run.Meta.BK}
	if run.Meta.Kind == osrun.RunRefresh {
		// a refresh has no text for the owner to run by hand, and none of the blocks of a hop: there is no block to say that comes next
		return manual
	}
	if run.BK == "" {
		manual.NextBlock = osrun.BlockVerify
		return manual
	}
	for _, id := range osrun.BlockIDs[:9] {
		if _, err := os.Lstat(filepath.Join(run.BK, "done-"+id)); err == nil {
			manual.BlocksDone = append(manual.BlocksDone, id)
			continue
		}
		if manual.NextBlock == "" {
			manual.NextBlock = id
		}
	}
	return manual
}

// osUpgradeFailureOf is what stopped a run that failed or stopped: the line of the output that says it is the last STOP line of the tail.
func osUpgradeFailureOf(state osrun.State, tail []string) *OSUpgradeFailure {
	failure := &OSUpgradeFailure{Stage: state.Block.ID, Class: state.Class, RC: state.RC}
	if failure.Class == "" {
		failure.Class = state.Reason
	}
	if failure.Stage == "" {
		failure.Stage = state.Phase
	}
	if state.Class == "" {
		// nothing stopped: a run that did not start has no line of its own, and the one that the output has is of an attempt before it
		return failure
	}
	for i := len(tail) - 1; i >= 0; i-- {
		if strings.HasPrefix(tail[i], "STOP:") {
			failure.DetailLine = tail[i]
			failure.Hint = osrun.HintOf(tail[i])
			break
		}
	}
	return failure
}

// osUpgradeHintOf is the hint for a unit that systemd says runs and that has printed nothing for as long as osrun.Timing.HungQuiet: stopping it is the owner's own act at the
// console. No hint is made for a unit that is not asked about.
func osUpgradeHintOf(run *osUpgradeRun, in osrun.Input, state osrun.State) *OSUpgradeHint {
	if !live(state.Name) || !in.Unit.Active || !in.Unit.Answered {
		return nil
	}
	quiet := in.Now.Sub(osUpgradeLastActivity(run, in))
	if limit := osUpgradeTiming.HungQuiet; limit <= 0 || quiet < limit {
		return nil
	}
	return &OSUpgradeHint{
		Code: "unit_quiet", QuietSeconds: int(quiet / time.Second),
		Commands: []string{"tail -n 60 " + osUpgradeUnitRoot + "/" + run.ID + "/" + osUpgradeOutputFile, "systemctl status " + systemOSUpgradeUnit, "systemctl stop " + systemOSUpgradeUnit},
	}
}

// osUpgradeRebootRequested is when the restart was last asked for. Whether the request stands (a restart that failed is taken back) is the state's to say, and the
// status shows this time only for a state that is that of a restart that is under way.
func osUpgradeRebootRequested(run *osUpgradeRun) (time.Time, bool) {
	var at time.Time
	for _, m := range run.Markers {
		if m.Kind == osrun.MkRebootRequested {
			at = m.At
		}
	}
	return at.UTC(), !at.IsZero()
}

// osUpgradeFindings are the findings of the judge as the page shows them, and the containers that did not come back, which the page lists on their own.
func osUpgradeFindings(findings []osverify.Finding) (out []OSUpgradeFinding, notReturned []OSUpgradeNotReturned) {
	for _, f := range findings {
		out = append(out, OSUpgradeFinding{Code: f.Code, Severity: f.Severity, Arg: f.Arg, Apps: f.Apps})
		if f.Code == osverify.CodeContainerNotReturned {
			notReturned = append(notReturned, OSUpgradeNotReturned{Container: f.Arg, Policy: f.Policy})
		}
	}
	return out, notReturned
}

// osUpgradeBlock8 is the owner's text of the block that checks the machine after the restart, taken out of the text that the run kept of it.
func osUpgradeBlock8(manual string) string {
	_, rest, found := strings.Cut(manual, "# >>> BLOCK 8 ")
	if !found {
		return ""
	}
	_, rest, _ = strings.Cut(rest, "\n")
	body, _, found := strings.Cut(rest, "# <<< END BLOCK 8")
	if !found {
		return ""
	}
	return strings.TrimSpace(body)
}

// osUpgradeAfterRebootHint is what the owner keeps in case the machine does not come back.
func osUpgradeAfterRebootHint(run *osUpgradeRun) *OSUpgradeAfterRebootHint {
	log := osUpgradeUnitRoot + "/" + run.ID + "/" + osUpgradeOutputFile
	hint := &OSUpgradeAfterRebootHint{
		BKPath: run.Meta.BK, Log: log,
		Commands: []string{"uname -r", "systemctl --failed --no-pager", "dpkg --audit", "ls " + run.Meta.BK, "tail -n 60 " + log},
	}
	if data, err := readOSUpgradeFile(filepath.Join(run.Dir, osUpgradeManualFile), osplan.MaxRunbookBytes); err == nil {
		hint.Block8 = osUpgradeBlock8(string(data))
	}
	return hint
}

// readOSUpgradeLogTail is the end of the unit's output: at most max bytes, cut to the last 200 lines, each through the sanitizer of the plan (the addresses that apt prints
// are the box's own sources, which carry their credentials). A file that is not there has no tail.
func readOSUpgradeLogTail(run *osUpgradeRun, max int) []string {
	lines := []string{}
	f, err := os.Open(filepath.Join(run.Dir, osUpgradeOutputFile))
	if err != nil {
		return lines
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return lines
	}
	cut := info.Size() > int64(max)
	if cut {
		if _, err := f.Seek(info.Size()-int64(max), io.SeekStart); err != nil {
			return lines
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(max)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return lines
	}
	text := strings.ReplaceAll(string(data), "\r", "\n")
	parts := strings.Split(text, "\n")
	if cut && len(parts) > 0 {
		// the first line of a cut is not a whole one
		parts = parts[1:]
	}
	for _, line := range parts {
		if line = osplan.SanitizeLine(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > osUpgradeTailLines {
		lines = lines[len(lines)-osUpgradeTailLines:]
	}
	return lines
}

// osUpgradeTailLines is how many lines of the unit's output the status shows.
const osUpgradeTailLines = 200
