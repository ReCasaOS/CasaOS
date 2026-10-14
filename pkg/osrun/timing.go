package osrun

import "time"

// Timing holds every number that the reducer, the core's settle loop and the marker reader decide with. They are
// estimates that nobody has timed on a real machine yet: they are to be tared from the first real runs, and so they
// live in one place and are tested by value.
type Timing struct {
	// GraceQuiet is how long a unit that systemd calls gone may still be quiet before the run is called interrupted:
	// systemd can say "gone" before the last line of the unit lands.
	GraceQuiet time.Duration
	// StartLost is how long an attempt with no word from its unit is waited for when systemd says the unit is not there;
	// StartLostSilent is the same when systemd does not answer.
	StartLost, StartLostSilent time.Duration
	// SystemdSilence is how long a unit that has not ended and a systemd that does not answer are waited for, as long as
	// the unit shows signs of life, before the run needs attention.
	SystemdSilence time.Duration
	// RebootStall is how long a restart that was asked for is waited for before it is called stalled.
	RebootStall time.Duration
	// RehearsalTTL is how long the machine is left with the next release's sources in place after a rehearsal.
	RehearsalTTL time.Duration
	// ResumeWait is how long a machine that has restarted is given to get ready before the check starts anyway.
	ResumeWait time.Duration
	// SettleEvery is how often the core looks at a run that is open.
	SettleEvery time.Duration
	// MaxVerifyAttempts and MaxUnwindAttempts bound how many times the check after the restart, and the undoing of a run
	// that stopped before the point of no return, are started again by themselves.
	MaxVerifyAttempts, MaxUnwindAttempts int
	// AutoRecoverTries is how many times the core may repair a hop that stopped after its point of no return, by itself,
	// before it leaves it to the owner; AutoRecoverWave is which classes of stop it may do that for (see Retryable).
	// With zero tries nothing is ever repaired without a click: the repair ladder has not run on a real machine yet.
	AutoRecoverTries, AutoRecoverWave int
	// Backoff is how long the core waits after a stop before its first and its second automatic repair; the last one
	// counts for every one after.
	Backoff [2]time.Duration
	// Reminders are when a machine that waits for its restart is spoken of again, counted from when it began to wait:
	// the first, the second, and then every third interval.
	Reminders [3]time.Duration
	// HungQuiet is how long a unit that systemd says runs may print nothing before the status says that it may be stuck, and what
	// the owner can do at the console about it.
	HungQuiet time.Duration
	// MarkersMax is how much of the log of markers is read, MarkerLineMax how long a line of it may be (without its
	// newline), and LogTail how much of the unit's output the status shows.
	MarkersMax, MarkerLineMax, LogTail int
}

// DefaultTiming is what the core runs with. Tests take a copy and change it.
var DefaultTiming = Timing{
	GraceQuiet:        30 * time.Second,
	StartLost:         30 * time.Second,
	StartLostSilent:   30 * time.Minute,
	SystemdSilence:    30 * time.Minute,
	RebootStall:       15 * time.Minute,
	RehearsalTTL:      30 * time.Minute,
	ResumeWait:        10 * time.Minute,
	SettleEvery:       30 * time.Second,
	MaxVerifyAttempts: 3,
	MaxUnwindAttempts: 3,
	AutoRecoverTries:  0,
	AutoRecoverWave:   1,
	Backoff:           [2]time.Duration{2 * time.Minute, 10 * time.Minute},
	Reminders:         [3]time.Duration{time.Hour, 24 * time.Hour, 24 * time.Hour},
	HungQuiet:         30 * time.Minute,
	MarkersMax:        markersMax,
	MarkerLineMax:     markerLineMax,
	LogTail:           128 << 10,
}

// markersMax and markerLineMax are what the reader of the markers uses: the file is the unit's and the core's, and the
// limits are not something a test or a caller should be able to loosen by mistake.
const (
	markersMax    = 1 << 20
	markerLineMax = 256
)
