package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
	"go.uber.org/zap"
)

// The settling is the core's look at an upgrade that is open: at its start, every half minute while a run is open or the flag of one is there, and when a button has changed
// something. It reads the run (the log, the files, what systemd and the kernel say), says what the run is, keeps the flag of the open upgrade as the state says, and does the
// one thing that the reducer says is next. It never runs apt, dpkg or a repair by itself: the units do, and the owner's click starts a repair. It holds osUpgradeMu only to append
// a marker and to start a unit, and never across a wait, and a read of the status takes no part in it.

// osUpgradeTiming is what the reducer and the settling decide with: the shipped numbers, which a test changes (a short wait, the automatic repairs that the shipped setting has off).
var osUpgradeTiming = osrun.DefaultTiming

// osUpgradeProcNetRoute is where the table of routes is, for the wait for a machine that has restarted to have its network back. A var, for the tests.
var osUpgradeProcNetRoute = "/proc/net/route"

// osUpgradeLastRefreshFile says when the old release was last brought up to date, in the folder of the runs: a plan that was made before it is not good for a start.
const osUpgradeLastRefreshFile = "last-refresh"

// osUpgradeAptUnits are the units that change packages by themselves, as the owner's own text stops them: they come back with a boot, and a run that is open does not have apt
// changing under it.
var osUpgradeAptUnits = []string{"apt-daily.timer", "apt-daily-upgrade.timer", "apt-daily.service", "apt-daily-upgrade.service", "unattended-upgrades.service"}

// osUpgradeEngine is what the core keeps of the upgrade in memory. None of it is what the upgrade is: that is in the run folder, and a core that is restarted at any moment
// reads it again.
type osUpgradeEngine struct {
	// loopMu guards the loop's own state; tickMu makes the looks take turns.
	loopMu sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	nudge  chan struct{}
	tickMu sync.Mutex
	// unreadAt is when a run that cannot be read was last said in the log: the loop looks every half minute.
	unreadAt atomic.Int64
}

// osUpgradeTick is what one look did, for the log and the tests.
type osUpgradeTick struct {
	RunID string
	State string
	// Action is what the reducer said to do next (osrun.Do...), and Done what was done of it: nothing, or the one thing.
	Action string
	Done   []string
}

// StartSystemOSUpgradeSettle starts the loop of the settling, once: the first look is made at once, and then one every osrun.Timing.SettleEvery. It returns at once. The core
// calls it when it is ready to serve, and not before: a look may start a unit.
func (s *systemService) StartSystemOSUpgradeSettle(parent context.Context) {
	e := &s.osUpgrade
	e.loopMu.Lock()
	defer e.loopMu.Unlock()
	if e.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	done, nudge := make(chan struct{}), make(chan struct{}, 1)
	e.cancel, e.done, e.nudge = cancel, done, nudge
	go func() {
		defer func() {
			close(done)
			e.loopMu.Lock()
			if e.done == done {
				e.cancel, e.done, e.nudge = nil, nil, nil
			}
			e.loopMu.Unlock()
		}()
		s.osUpgradeLoop(ctx, nudge)
	}()
}

// StopSystemOSUpgradeSettle ends the loop and waits, for no longer than wait, for the look that is being made to be over.
func (s *systemService) StopSystemOSUpgradeSettle(wait time.Duration) {
	e := &s.osUpgrade
	e.loopMu.Lock()
	cancel, done := e.cancel, e.done
	e.loopMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(wait):
	}
}

// SettleSystemOSUpgrade makes one look at once, and waits for it: what the buttons and the tests ask for.
func (s *systemService) SettleSystemOSUpgrade() { s.osUpgradeSettleOnce(context.Background()) }

// nudgeOSUpgradeSettle asks the loop for a look now, after a button changed something. With no loop it does nothing: the next one that is started looks first of all.
func (s *systemService) nudgeOSUpgradeSettle() {
	e := &s.osUpgrade
	e.loopMu.Lock()
	nudge := e.nudge
	e.loopMu.Unlock()
	if nudge == nil {
		return
	}
	select {
	case nudge <- struct{}{}:
	default:
	}
}

func (s *systemService) osUpgradeLoop(ctx context.Context, nudge <-chan struct{}) {
	every := osUpgradeTiming.SettleEvery
	if every <= 0 {
		every = osrun.DefaultTiming.SettleEvery
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-nudge:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		s.osUpgradeSettleOnce(ctx)
		timer.Reset(every)
	}
}

// logUnread says in the log, once a minute at most, that a run that is open cannot be read.
func (e *osUpgradeEngine) logUnread(message string, err error) {
	if now := time.Now().UnixNano(); now-e.unreadAt.Load() >= int64(time.Minute) {
		e.unreadAt.Store(now)
		logger.Error(message, zap.Error(err))
	}
}

// osUpgradeSettleOnce is one look. The steps are in this order: the flag as the state says (a flag that has no run behind it goes), a boot that the log has not seen, what the
// reducer says to do next (one thing), and what is only a reading of the machine (the checks before the restart, the alerts).
func (s *systemService) osUpgradeSettleOnce(ctx context.Context) (tick osUpgradeTick) {
	e := &s.osUpgrade
	e.tickMu.Lock()
	defer e.tickMu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			logger.Error("a look at the upgrade of the operating system went wrong", zap.Any("panic", r))
		}
	}()

	id, current := currentOSUpgrade()
	switch current {
	case osUpgradeNoRun:
		s.osUpgradeDropStrayFlag()
		return tick
	case osUpgradeBadRun:
		// the machine is not known to be free of it: its flag stays
		e.logUnread("the pointer to the latest upgrade of the operating system cannot be read: the upgrade is taken for open", errors.New("the pointer says nothing that can be used"))
		return tick
	}
	run, err := readOSUpgradeRun(id)
	if err != nil {
		e.logUnread("the latest upgrade of the operating system cannot be read: the upgrade is taken for open", err)
		return tick
	}
	tick.RunID = id
	in := s.osUpgradeInput(run)
	state := osrun.Reduce(in)
	s.osUpgradeKeepFlag(run, state)

	// a boot that the log has not seen, while the run holds the machine
	if boot, ok := osUpgradeBootID(); ok && state.Busy && !osrun.BootKnown(run.Markers, boot) {
		s.osUpgradeSeeBoot(ctx, run, boot)
		if again, err := readOSUpgradeRun(id); err == nil {
			run, in = again, s.osUpgradeInput(again)
			state = osrun.Reduce(in)
		}
	}
	tick.State = state.Name
	s.osUpgradeNoteRefresh(run)

	if state.Name == osrun.StateResuming {
		in.Ready = s.osUpgradeReady(ctx, run)
	}
	action := osrun.NextAction(state, in)
	tick.Action = action.Kind
	if done := s.osUpgradeDo(ctx, run, state, action); done != "" {
		tick.Done = append(tick.Done, done)
	}
	return tick
}

// osUpgradeDropStrayFlag takes away the flag of an upgrade that has no run: the pointer is read again under the lock, since a start that is being written has put its flag
// down before its pointer.
func (s *systemService) osUpgradeDropStrayFlag() {
	if open, _ := osUpgradeFlagOpen(); !open {
		return
	}
	osUpgradeMu.Lock()
	defer osUpgradeMu.Unlock()
	if _, current := currentOSUpgrade(); current != osUpgradeNoRun {
		return
	}
	if err := clearOSUpgradeBusy(); err != nil {
		logger.Error("the flag of an upgrade of the operating system that is not open could not be taken away", zap.Error(err))
	}
}

// osUpgradeHolds says whether a state of a run holds the machine: it is busy, or it is a start that was written and has not had its first attempt yet.
func osUpgradeHolds(state osrun.State) bool {
	return state.Busy || (state.Name == osrun.StateIdle && !state.Closed && state.RunID != "")
}

// osUpgradeKeepFlag makes the flag of the open upgrade what the state says: there while the run holds the machine, gone when it does not. A run that is not the latest any
// more by the time the lock is taken has no say. A flag is taken away only after the run has been read again under the lock: a start that wrote its pointer while this look was
// reading is on its way, and its flag is not this look's to remove. A flag that already says the state is not written again.
func (s *systemService) osUpgradeKeepFlag(run *osUpgradeRun, state osrun.State) {
	osUpgradeMu.Lock()
	defer osUpgradeMu.Unlock()
	if id, current := currentOSUpgrade(); current != osUpgradeHasRun || id != run.ID {
		return
	}
	if !osUpgradeHolds(state) {
		fresh, err := readOSUpgradeRun(run.ID)
		if err != nil || osUpgradeHolds(osrun.Reduce(s.osUpgradeInput(fresh))) {
			return
		}
		if err := clearOSUpgradeBusy(); err != nil {
			logger.Error("the flag of an upgrade of the operating system could not be taken away", zap.String("run", run.ID), zap.Error(err))
		}
		return
	}
	since := run.Meta.CreatedAt
	if flag, ok := readOSUpgradeBusyFlag(); ok && flag.RunID == run.ID {
		if flag.State == state.Name {
			return
		}
		since = flag.Since
	}
	if err := setOSUpgradeBusy(run.ID, state.Name, since); err != nil {
		logger.Error("the flag of an upgrade of the operating system could not be written", zap.String("run", run.ID), zap.Error(err))
	}
}

// readOSUpgradeBusyFlag is what the flag says, and false when it is not there or says nothing that can be used.
func readOSUpgradeBusyFlag() (osUpgradeBusyFlag, bool) {
	var flag osUpgradeBusyFlag
	data, err := readOSUpgradeFile(osUpgradeBusyFile, 4<<10)
	if err != nil || json.Unmarshal(data, &flag) != nil || !osrun.ValidUUID(flag.RunID) {
		return flag, false
	}
	return flag, true
}

// osUpgradeSeeBoot is the first thing the core does of a boot that the run has not seen, and which holds the machine: the units that change packages by themselves come back with
// a boot, and are stopped again, and the boot is written down once. The units are stopped first: a look that is cut short between the two does both again.
func (s *systemService) osUpgradeSeeBoot(ctx context.Context, run *osUpgradeRun, boot string) {
	s.osUpgradeStopAptUnits(ctx)
	if !osrun.ValidNonce(run.Nonce) {
		return
	}
	osUpgradeMu.Lock()
	defer osUpgradeMu.Unlock()
	seen := osrun.Marker{Kind: osrun.MkBootSeen, Boot: boot, At: s.systemPackageUpdater().now().UTC()}
	if err := appendOSUpgradeMarker(run.Dir, run.Nonce, seen); err != nil {
		logger.Error("the log of an upgrade of the operating system could not say that a boot was seen", zap.String("run", run.ID), zap.Error(err))
		return
	}
	logger.Info("a boot of the machine was seen by an upgrade of the operating system that is open", zap.String("run", run.ID))
}

// osUpgradeStopAptUnits stops the units of apt that run by themselves, each only if it is active: a unit that is not there is not a failure.
func (s *systemService) osUpgradeStopAptUnits(ctx context.Context) {
	u := s.systemPackageUpdater()
	for _, unit := range osUpgradeAptUnits {
		askCtx, cancel := context.WithTimeout(ctx, systemDockerUnitTimeout)
		out, _ := u.command(askCtx, "systemctl", "is-active", unit)
		cancel()
		if state := strings.TrimSpace(string(out)); state != "active" && state != "activating" {
			continue
		}
		stopCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if _, err := u.command(stopCtx, "systemctl", "stop", unit); err != nil {
			logger.Error("a unit of apt could not be stopped for an upgrade of the operating system", zap.String("unit", unit), zap.Error(err))
		}
		cancel()
	}
}

// osUpgradeReady is the core's reading, without waiting, of a machine that has restarted: systemd says the system is running or degraded, and what ran before the hop and is
// needed to say anything of it runs again (Docker, if it ran, and a default route, if there was one). A machine that never gets ready is checked when the time it was given is
// over, and the check says what is not as it was.
func (s *systemService) osUpgradeReady(ctx context.Context, run *osUpgradeRun) bool {
	u := s.systemPackageUpdater()
	ask := func(args ...string) string {
		askCtx, cancel := context.WithTimeout(ctx, systemDockerUnitTimeout)
		defer cancel()
		out, _ := u.command(askCtx, "systemctl", args...)
		first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		return strings.TrimSpace(first)
	}
	if state := ask("is-system-running"); state != "running" && state != "degraded" {
		return false
	}
	before := readOSUpgradeFacts(run)
	if before.Knows(osverify.FactDocker) && before.Docker.State == osverify.DockerUp && ask("is-active", "docker.service") != "active" {
		return false
	}
	if before.Knows(osverify.FactRoute) && before.Route != "" {
		data, err := readOSUpgradeFile(osUpgradeProcNetRoute, 1<<20)
		if err != nil || osverify.DefaultRoute(string(data)) == "" {
			return false
		}
	}
	return true
}

// osUpgradeDo carries out the one thing that the reducer says is next, and says what it was: nothing for waiting, for the owner's click, and for what is only told (the
// alerts are another step of the look).
func (s *systemService) osUpgradeDo(ctx context.Context, run *osUpgradeRun, state osrun.State, action osrun.Action) string {
	switch action.Kind {
	case osrun.DoUnwind, osrun.DoLaunchVerify, osrun.DoLaunchRecover:
		err := s.launchAttempt(run.ID, action.Phase, action.Who)
		var launch *osUpgradeLaunchError
		switch {
		case err == nil:
			logger.Info("the core started a unit for an upgrade of the operating system", zap.String("run", run.ID), zap.Int("attempt", state.Attempt+1), zap.String("action", action.Kind))
			return action.Kind
		case errors.As(err, &launch) && launch.Kind == launchUnitBusy:
			// a unit that is on its way, or systemd that does not answer: the next look
		default:
			logger.Error("the core could not start a unit for an upgrade of the operating system", zap.String("run", run.ID), zap.String("action", action.Kind), zap.Error(err))
		}
	case osrun.DoCleanup:
		if err := pruneOSUpgradeRuns(osUpgradeKeepRuns); err != nil {
			logger.Error("the old runs of the upgrade could not be removed", zap.Error(err))
		}
		return action.Kind
	}
	return ""
}

// osUpgradeNoteRefresh writes down when a refresh ended, for the plans that are made before it and are started after: only a time that is later than the one that is there.
func (s *systemService) osUpgradeNoteRefresh(run *osUpgradeRun) {
	if run.Meta.Kind != osrun.RunRefresh {
		return
	}
	var ended time.Time
	for _, m := range run.Markers {
		if m.Kind == osrun.MkRefreshDone {
			ended = m.At
		}
	}
	if ended.IsZero() {
		return
	}
	path := osUpgradeRoot + "/" + osUpgradeLastRefreshFile
	if data, err := readOSUpgradeFile(path, 256); err == nil {
		if at, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data))); err == nil && !at.Before(ended) {
			return
		}
	}
	if err := osUpgradeWriteFile(path, []byte(ended.UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		logger.Error("the time of the end of the refresh could not be written down", zap.Error(err))
	}
}
