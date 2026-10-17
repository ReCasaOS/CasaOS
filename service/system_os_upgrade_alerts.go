package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"go.uber.org/zap"
)

// The owner is told of an upgrade when it needs them, and of how it ended, through the hub of alerts that main hands the core (SetOSUpgradeNotifier). Each thing is told once: the
// core keeps a file of what it told, writes the key of a thing to it before the voice is asked, and so a run that ended while the core was down is told by the first look of the
// one that starts, and none is told twice by a start that comes at the wrong time. The file is written whether or not there is a voice: the owner who adds a channel next week is
// not told the news of last week. A machine that waits for its restart is told again after an hour, after a day, and every day, each time saying that backups and automatic updates
// stay paused while the upgrade is open.

// osUpgradeNotifiedFile is where the core keeps what it told, in the folder of the runs: one key a line.
const osUpgradeNotifiedFile = "notified"

const (
	// osUpgradeNotifiedMax is the most of the file that is read, and osUpgradeNotifiedKeep how many of its last keys are kept when it grows beyond osUpgradeNotifiedTrim.
	osUpgradeNotifiedMax  = 64 << 10
	osUpgradeNotifiedTrim = 16 << 10
	osUpgradeNotifiedKeep = 100
	// osUpgradeMuteAfter is how long the alerts about the apps' containers stay quiet after the upgrade lets go of the machine: the apps' watch is slow to say that a container is
	// back, and some are late.
	osUpgradeMuteAfter = 10 * time.Minute
)

// SetOSUpgradeNotifier sets what tells the owner: it is called with the key of a thing (one for each thing there is to say) and the sentence. It must return at once. It is called
// by the look of the settling, with the file that remembers already written.
func (s *systemService) SetOSUpgradeNotifier(notify func(key, sentence string)) {
	s.osUpgrade.notify.Store(&notify)
}

// osUpgradeAlertFor is what the owner is told of a state of a run, once: the key, which is the run, the state and the attempt (a stop that comes again after the owner resumed is
// another stop), and the sentence; false for a state that has nothing to tell: a unit that does what it should, a repair that is on its way, a run that the core will undo.
func osUpgradeAlertFor(state osrun.State) (key, sentence string, ok bool) {
	key = state.RunID + ":" + state.Name + ":" + strconv.Itoa(state.Attempt)
	switch state.Name {
	case osrun.StateAwaitingReboot:
		return key, "The upgrade of the operating system is ready for its restart. Restart the machine from the dashboard when it suits you; until then backups and automatic updates stay paused.", true
	case osrun.StateRebootFirst:
		return key, "The refresh of the operating system asks for a restart before the upgrade can be planned. Restart the machine from the dashboard when it suits you; until then backups and automatic updates stay paused.", true
	case osrun.StateRebootStalled:
		return key, "The machine was asked to restart a while ago and has not. Look at it, and ask again from the dashboard. Backups and automatic updates stay paused until the upgrade is closed.", true
	case osrun.StateStoppedAtWaypoint:
		return key, "The upgrade of the operating system stopped half way (" + osUpgradeWord(state.Class) + ") and the machine is half upgraded. Open the dashboard to go on.", true
	case osrun.StateInterrupted:
		if state.PNR {
			return key, "The upgrade of the operating system was interrupted half way and the machine is half upgraded. Open the dashboard to go on.", true
		}
	case osrun.StateFailed:
		switch {
		case state.Reason == osrun.ReasonVerify:
			return key, "The machine restarted into the new release, but the check afterwards found something that is not right. Open the dashboard to see what.", true
		case state.Reason == osrun.ReasonHalf:
			return key, "The upgrade of the operating system could not be finished by itself and the machine is half upgraded. Open the dashboard.", true
		case state.PNR:
			return key, "The upgrade of the operating system could not start its next step and the machine is half upgraded. Open the dashboard.", true
		case state.Kind == osrun.RunRefresh:
			// a refresh has no point of no return: it runs dist-upgrade of the release the machine has, and it may have upgraded packages before it stopped
			return key, "The refresh of the operating system stopped (" + osUpgradeWord(state.Reason) + "). Some packages of the release may have been upgraded; the sources and the timers of apt are back. Open the dashboard to see why.", true
		}
		return key, "The upgrade of the operating system stopped before it changed any package (" + osUpgradeWord(state.Reason) + "): the machine is as it was. Open the dashboard to see why.", true
	case osrun.StateNeedsAttention:
		return key, "The upgrade of the operating system needs you: what it is doing cannot be told. Open the dashboard.", true
	case osrun.StateSucceeded:
		return key, "The operating system was upgraded.", true
	case osrun.StateSucceededWithFindings:
		return key, "The operating system was upgraded. The check afterwards has things for you to look at; they are in the dashboard.", true
	case osrun.StateRehearsalDone:
		return key, "The rehearsal of the upgrade ended and the machine is as it was.", true
	}
	return "", "", false
}

// osUpgradeReminderSentence is what is said again of a machine that still waits for its restart.
func osUpgradeReminderSentence() string {
	return "The machine still waits for its restart. Until the upgrade is closed, backups and automatic updates stay paused."
}

// osUpgradeBlockedSentence is what is said of an upgrade that is done up to its restart, when something that stands before the restart is not well: the dashboard does not offer the
// restart then, and the owner is not told to restart.
const osUpgradeBlockedSentence = "The upgrade of the operating system is done, but something that stands before its restart is not well, and the dashboard does not offer the restart until it is mended. Open the dashboard to see what. Backups and automatic updates stay paused."

// osUpgradeWord is a word of the core's own (a class of stop, a reason), which is only ever lower case letters and underscores, and "" for anything else.
func osUpgradeWord(word string) string {
	if word == "" || strings.Trim(word, "abcdefghijklmnopqrstuvwxyz_") != "" {
		return "unknown"
	}
	return word
}

func osUpgradeNotifiedPath() string { return filepath.Join(osUpgradeRoot, osUpgradeNotifiedFile) }

// osUpgradeToldKeys are the keys that the file holds.
func osUpgradeToldKeys() map[string]bool {
	keys := map[string]bool{}
	data, err := readOSUpgradeFile(osUpgradeNotifiedPath(), osUpgradeNotifiedMax)
	if err != nil {
		return keys
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			keys[line] = true
		}
	}
	return keys
}

// osUpgradeRemember writes a key into the file, whole before it returns, and keeps the file short.
func osUpgradeRemember(key string) error {
	path := osUpgradeNotifiedPath()
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Size() > osUpgradeNotifiedTrim {
		if data, err := readOSUpgradeFile(path, osUpgradeNotifiedMax); err == nil {
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			if len(lines) > osUpgradeNotifiedKeep {
				lines = lines[len(lines)-osUpgradeNotifiedKeep:]
			}
			if err := osUpgradeWriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
				return err
			}
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(key + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// osUpgradeTell tells the owner of one thing, once: the key goes into the file, and only then is the voice asked. It is called by a look of the settling, which takes turns.
func (s *systemService) osUpgradeTell(key, sentence string) {
	e := &s.osUpgrade
	if e.told == nil {
		e.told = map[string]bool{}
	}
	if e.told[key] {
		return
	}
	e.told[key] = true
	if osUpgradeToldKeys()[key] {
		return
	}
	if err := osUpgradeRemember(key); err != nil {
		// told once for the life of the core: a start of the core may tell it again
		logger.Error("the core could not write down that it told the owner of an upgrade of the operating system", zap.String("key", key), zap.Error(err))
	}
	if notify := e.notify.Load(); notify != nil && *notify != nil {
		(*notify)(key, sentence)
	}
}

// osUpgradeTellOf tells the owner what a state asks to be told, once. A machine that waits for its restart is spoken of again at the times the reducer gives.
func (s *systemService) osUpgradeTellOf(state osrun.State, action osrun.Action, barrierRed bool) {
	if state.Name == osrun.StateAwaitingReboot && barrierRed {
		// the restart is not offered: the owner is not told to restart, and is told when it is
		s.osUpgradeTell(state.RunID+":awaiting_reboot_blocked:"+strconv.Itoa(state.Attempt), osUpgradeBlockedSentence)
	} else if key, sentence, ok := osUpgradeAlertFor(state); ok {
		s.osUpgradeTell(key, sentence)
	}
	if action.Kind == osrun.DoRemind {
		s.osUpgradeTell(state.RunID+":reminder:"+strconv.Itoa(action.N), osUpgradeReminderSentence())
	}
}

// OSUpgradeMuted says whether the alerts about the apps' containers are to be kept quiet: the upgrade holds the machine (its flag is there, or cannot be asked about), or it let
// go of it less than ten minutes ago. Containers stop and start during the hop, and each app's watch says so.
func (s *systemService) OSUpgradeMuted() bool {
	if open, _ := osUpgradeFlagOpen(); open {
		return true
	}
	return s.systemPackageUpdater().now().UnixNano() < s.osUpgrade.muteUntil.Load()
}

// osUpgradeQuietUntil makes the alerts quiet until a time, if that is later than they are already.
func (s *systemService) osUpgradeQuietUntil(t time.Time) {
	until := t.UnixNano()
	for {
		was := s.osUpgrade.muteUntil.Load()
		if until <= was || s.osUpgrade.muteUntil.CompareAndSwap(was, until) {
			return
		}
	}
}

// osUpgradeKeepQuiet makes the alerts quiet until ten minutes after a state that lets go of the machine was reached, for a core that is started after that and reads it from the
// run (the look that takes the flag away says ten minutes from then, which is later and nearer the truth).
func (s *systemService) osUpgradeKeepQuiet(state osrun.State) {
	if !osUpgradeHolds(state) && !state.Since.IsZero() {
		s.osUpgradeQuietUntil(state.Since.Add(osUpgradeMuteAfter))
	}
}
