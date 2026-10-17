package service

import (
	"errors"
	"regexp"
	"slices"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"go.uber.org/zap"
)

// The three things that the owner can say of a run that is not running: close it (acknowledge), drop it (discard: close it, and drop the plan that it was made for, or put a
// rehearsal back), or take it over (manual: the core stops acting, and the owner finishes the hop by hand from the text of the run).
const (
	OSUpgradeAckAcknowledge = "acknowledge"
	OSUpgradeAckDiscard     = "discard"
	OSUpgradeAckManual      = "manual"
)

// OSUpgradeAckRequest is the body of the owner's word. Word is the typed one that a machine that is half upgraded needs to be let go of (osrun.AckWord).
type OSUpgradeAckRequest struct {
	Action string `json:"action"`
	Word   string `json:"word"`
}

// osUpgradeWordPattern is what a typed word may be made of; the word that is asked for is one of them.
var osUpgradeWordPattern = regexp.MustCompile(`^[A-Za-z0-9-]{0,64}$`)

func badAckField(field string) *OSUpgradeRefusal {
	return refuseUpgrade(OSUpgradeBadRequest, "a field of the request is not valid: "+field, map[string]any{"field": field})
}

// AckSystemOSUpgrade is the owner's word on a run that is not running. What each of them is valid for is what osrun.ActionsFor says of the state, looked at again under the lock;
// a machine that is half upgraded, or that needs attention, is let go of only with the typed word, since closing the run drops the flag that keeps the backups and the automatic
// updates away from a machine that dpkg has begun to change; and nothing is closed or taken over while a unit may run, in whatever state the run is: systemd is asked first, and
// it is only a systemd that has not answered for half an hour, in a run that says so, that the word alone is enough with.
//
// A run that cannot be read is a machine that is not known to be free of it: it can be let go of, with the word, and nothing else can be done with it.
func (s *systemService) AckSystemOSUpgrade(req OSUpgradeAckRequest) (OSUpgradeStatus, error) {
	if !slices.Contains([]string{OSUpgradeAckAcknowledge, OSUpgradeAckDiscard, OSUpgradeAckManual}, req.Action) {
		return OSUpgradeStatus{}, badAckField("action")
	}
	if !osUpgradeWordPattern.MatchString(req.Word) {
		return OSUpgradeStatus{}, badAckField("word")
	}
	unit := s.osUpgradeUnitIfRun()
	var launch *osUpgradeLaunch
	var state osrun.State
	err := withOSUpgradeLock(func() (err error) {
		launch, state, err = s.osUpgradeAckLocked(req, unit)
		return err
	})
	if err != nil {
		return OSUpgradeStatus{}, err
	}
	if launch != nil {
		if err := s.launchRun(launch); err != nil {
			return OSUpgradeStatus{}, osUpgradeLaunchRefusal(err, state)
		}
	}
	if err := pruneOSUpgradeRuns(osUpgradeKeepRuns); err != nil {
		logger.Error("the old runs of the upgrade could not be removed", zap.Error(err))
	}
	s.nudgeOSUpgradeSettle()
	return s.GetSystemOSUpgradeStatus(), nil
}

// osUpgradeCheckWord is word_required or bad_word for a state that needs the typed word, and nil for one that does not or whose word is the right one.
func osUpgradeCheckWord(needed bool, word string) *OSUpgradeRefusal {
	switch {
	case !needed || word == osrun.AckWord:
		return nil
	case word == "":
		return refuseUpgrade(OSUpgradeWordRequired, "the machine is half upgraded or needs attention: closing the upgrade needs the typed word", nil)
	}
	return refuseUpgrade(OSUpgradeBadWord, "the typed word is not the one that is asked for", nil)
}

// osUpgradeAckLocked is the owner's word, for a caller that holds the lock and has asked systemd. A rehearsal that is discarded gives the launch of its unwind, which the caller
// starts when it has let go of the lock.
func (s *systemService) osUpgradeAckLocked(req OSUpgradeAckRequest, unit osrun.Unit) (*osUpgradeLaunch, osrun.State, error) {
	run, state, refusal := s.osUpgradeCurrentWith(unit)
	if refusal != nil {
		if refusal.Code == OSUpgradeRunUnreadable && req.Action == OSUpgradeAckAcknowledge {
			return nil, state, s.osUpgradeLetGoOfUnreadable(req.Word, unit)
		}
		return nil, state, refusal
	}
	want := map[string]string{OSUpgradeAckAcknowledge: osrun.ActionAcknowledge, OSUpgradeAckDiscard: osrun.ActionDiscard, OSUpgradeAckManual: osrun.ActionTakeover}[req.Action]
	if !slices.Contains(osrun.ActionsFor(state), want) {
		return nil, state, refuseUpgrade(OSUpgradeNotTerminal, "the upgrade is not in a state that this word is for", map[string]any{"state": state.Name})
	}
	u := s.systemPackageUpdater()

	if req.Action == OSUpgradeAckDiscard && state.Name == osrun.StateRehearsed {
		// the machine holds the sources of the next release: it is put back first, and the rehearsal is closed after, as a rehearsal that is done
		launch, err := s.launchPrepareLocked(run.ID, osrun.PhaseUnwind, osrun.WhoOwner, unit)
		if err != nil {
			return nil, state, osUpgradeLaunchRefusal(err, state)
		}
		return launch, state, nil
	}
	if !unit.Answered || unit.Active {
		// Nothing is closed or taken over while a unit may run. A systemd that has not answered for as long as the run says is the one case that the word is enough for: the unit
		// cannot be asked, and the owner who stops it at the console and types the word is the way out.
		silent := state.Name == osrun.StateNeedsAttention && state.Reason == osrun.ReasonSystemdSilent && !unit.Answered && req.Action == OSUpgradeAckAcknowledge
		if !silent {
			return nil, state, refuseUpgrade(OSUpgradeUnitActive, "a unit of the upgrade runs, or systemd does not answer", nil)
		}
		if refusal := osUpgradeCheckWord(true, req.Word); refusal != nil {
			return nil, state, refusal
		}
	}
	switch req.Action {
	case OSUpgradeAckManual:
		if err := appendOSUpgradeMarker(run.Dir, run.Nonce, osrun.Marker{Kind: osrun.MkTakeover, At: u.now().UTC()}); err != nil {
			return nil, state, osUpgradeStartFailed("the log of the upgrade could not say that the owner took it over", err)
		}
		logger.Info("the owner took an upgrade of the operating system over", zap.String("run", run.ID))
		return nil, state, nil
	}
	if refusal := osUpgradeCheckWord(state.NeedsAckWord(), req.Word); refusal != nil {
		return nil, state, refusal
	}
	// the end of a refresh is written down before the run is closed: a plan that was made before it must not be started
	s.osUpgradeNoteRefresh(run)
	s.osUpgradeKeepQuiet(state)
	if req.Action == OSUpgradeAckDiscard {
		s.osUpgradeDropPlan()
	}
	if err := s.closeOSUpgradeRun(run, req.Action); err != nil {
		return nil, state, osUpgradeCloseFailed("the upgrade could not be closed", err)
	}
	logger.Info("the owner closed an upgrade of the operating system", zap.String("run", run.ID), zap.String("how", req.Action), zap.String("state", state.Name))
	return nil, state, nil
}

// osUpgradeLetGoOfUnreadable closes a run that cannot be read, on the typed word: the pointer to it and the flag are taken away, and nothing else of it is touched. The word is
// always asked for, since nothing says that the machine is not half upgraded.
func (s *systemService) osUpgradeLetGoOfUnreadable(word string, unit osrun.Unit) error {
	if refusal := osUpgradeCheckWord(true, word); refusal != nil {
		return refusal
	}
	if !unit.Answered || unit.Active {
		return refuseUpgrade(OSUpgradeUnitActive, "a unit of the upgrade runs, or systemd does not answer", nil)
	}
	err := errors.Join(clearOSUpgradeCurrent(), clearOSUpgradeBusy())
	if err != nil {
		return osUpgradeCloseFailed("the upgrade could not be closed", err)
	}
	logger.Info("the owner let go of an upgrade of the operating system that could not be read")
	return nil
}

// osUpgradeCloseFailed is a close that broke on the machine's side: the run is as it was, and the cause is in the log of the core and not in the answer.
func osUpgradeCloseFailed(detail string, cause error) *OSUpgradeRefusal {
	logger.Error("an upgrade of the operating system could not be closed", zap.String("detail", detail), zap.Error(cause))
	return refuseUpgrade(OSUpgradeCloseFailed, detail, map[string]any{"detail": detail})
}

// osUpgradeDropPlan drops the plan that is live, if one is: the plan of a run that failed was made for a machine that the run touched. A job that runs is not dropped.
func (s *systemService) osUpgradeDropPlan() {
	if st := s.osPlan.state.Load(); st != nil && st.state == osPlanDone {
		s.osPlan.state.CompareAndSwap(st, &osPlanState{state: osPlanIdle})
	}
}
