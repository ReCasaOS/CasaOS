package service

import (
	"sync"
	"time"
)

// What the alerts need to know of the update of Docker: whether to keep quiet about the apps'
// containers, and how a run ended. Both are read from the log of the run and from systemd, as
// the status is; nothing is kept in memory but the last answer, for a few seconds.

const (
	// dockerMuteAfter is how long the alerts stay quiet after a run ends: the apps' watch is slow
	// to say that a container is back, and some are late.
	dockerMuteAfter = 10 * time.Minute
	// dockerMuteCache is how long the answer to "quiet?" is reused. It is asked for each event of
	// a container, and a Docker restart sends one per container; the answer costs a look at
	// systemd and the log.
	dockerMuteCache = 5 * time.Second
)

// dockerMute is the last answer of dockerMuted. It has a lock of its own: the updater's is held
// for minutes by a long update of packages, and an event must not wait for that.
type dockerMute struct {
	mu    sync.Mutex
	at    time.Time
	muted bool
}

// DockerUpdateRun is DockerUpdateStatus and the nonce of the run it is about, "" when the log
// has none: what tells one run from the next, for a notification that is sent once.
func (s *systemService) DockerUpdateRun() (SystemDockerUpdateStatus, string) {
	return s.systemPackageUpdater().dockerRun()
}

// DockerUpdateMuted says whether the alerts about the apps' containers are to be kept quiet: the
// update of Docker runs, or ended less than ten minutes ago.
func (s *systemService) DockerUpdateMuted() bool {
	return s.systemPackageUpdater().dockerMuted()
}

// OnDockerUpdateQueued sets what is called when the log of a new run was written, whether or not
// systemd takes the unit after it. It is called with the updater's lock held and must return at
// once, without calling back into the update.
func (s *systemService) OnDockerUpdateQueued(queued func()) {
	updater := s.systemPackageUpdater()
	updater.mu.Lock()
	defer updater.mu.Unlock()
	updater.onDockerQueued = queued
}

// dockerMuted is true while the unit runs (or its last line may be on its way) and for ten
// minutes after the run's own last line says it ended. A run that stopped without one has no
// end to count from: it is not muted. It takes no updater lock, and looks at systemd and the log
// once in a few seconds at most, whatever the number of questions.
func (u *systemPackageUpdater) dockerMuted() bool {
	u.mute.mu.Lock()
	defer u.mute.mu.Unlock()
	now := u.now()
	// a clock that went back makes the age negative: the answer is not trusted then
	if age := now.Sub(u.mute.at); !u.mute.at.IsZero() && age >= 0 && age < dockerMuteCache {
		return u.mute.muted
	}
	u.mute.at, u.mute.muted = now, dockerRunMutes(u.dockerStatus(), now)
	return u.mute.muted
}

func dockerRunMutes(status SystemDockerUpdateStatus, now time.Time) bool {
	switch status.State {
	case systemPackageUpdateStateRunning, systemPackageUpdateStateFinalizing:
		return true
	}
	completed, err := time.Parse(time.RFC3339, status.CompletedAt)
	return err == nil && now.Sub(completed) < dockerMuteAfter
}
