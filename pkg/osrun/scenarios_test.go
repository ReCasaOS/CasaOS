package osrun

import (
	"testing"
	"time"
)

// The runs below are the ones the reducer is asked about again and again: a log, the files the blocks leave, and what
// the machine and systemd say. Each is a function so that a test can go on from where it stops.

// hopLaunched is a hop whose first attempt was written and whose unit has begun.
func hopLaunched(t *testing.T) *runLog {
	l := newLog(t, RunHop)
	l.attempt(1, PhaseRun, "", WhoAuto)
	l.start(1, PhaseRun, bootA)
	return l
}

// hopInBlock is a hop whose unit is in the block of this id: the ones before it ended, with their files, and this one began.
func hopInBlock(t *testing.T, id string) *runLog {
	l := hopLaunched(t)
	for i, b := range BlockList(PhaseRun, Files{}) {
		l.blockStart(1, i+1, b)
		if b == id {
			if id == BlockMinimal {
				l.files.Begun = true
			}
			return l
		}
		l.blockFiles(b, true)
		l.blockDone(1, i+1, b)
	}
	t.Fatalf("no block %q in a hop", id)
	return nil
}

// hopAtReboot is a hop whose first unit ended with everything done and waited for the restart.
func hopAtReboot(t *testing.T) *runLog {
	return hopLaunched(t).hopDone()
}

// hopDone completes the blocks of attempt 1 and ends it with the word that the machine is to restart.
func (l *runLog) hopDone() *runLog {
	for i, b := range BlockList(PhaseRun, Files{}) {
		l.blockStart(1, i+1, b)
		l.blockFiles(b, true)
		l.blockDone(1, i+1, b)
	}
	l.files.Begun, l.files.AptOrig = true, true
	l.add(Marker{Kind: MkRebootPending, N: 1, Boot: bootA, Kernel: "6.1.0-37-amd64"})
	return l.end(1, "ok")
}

// hopRestarted is a hop whose machine restarted into bootB, with a check attempt launched and begun.
func hopRestarted(t *testing.T) *runLog {
	l := hopAtReboot(t)
	l.add(Marker{Kind: MkRebootRequested})
	l.attempt(2, PhaseVerify, "", WhoAuto)
	l.start(2, PhaseVerify, bootB)
	return l
}

// hopVerified is a hop whose check ended: block 8's file is there and the unit said so.
func hopVerified(t *testing.T) *runLog {
	l := hopRestarted(t)
	l.blockStart(2, 1, BlockAfter)
	l.files.DoneAfter = true
	l.blockDone(2, 1, BlockAfter)
	l.add(Marker{Kind: MkVerifyDone, N: 2, Boot: bootB})
	return l.end(2, "ok")
}

// refreshDone is a refresh that ended and asks for a restart or not.
func refreshDone(t *testing.T, restart bool) *runLog {
	l := newLog(t, RunRefresh)
	l.attempt(1, PhaseRefresh, "", WhoAuto).start(1, PhaseRefresh, bootA)
	for i, b := range BlockList(PhaseRefresh, Files{}) {
		l.block(1, i+1, b)
	}
	answer := "no"
	if restart {
		answer = "yes"
	}
	l.add(Marker{Kind: MkRefreshDone, N: 1, Result: answer, Why: "kernel"})
	return l.end(1, "ok")
}

// rehearsalReady is a rehearsal that reached the end of its blocks.
func rehearsalReady(t *testing.T) *runLog {
	l := newLog(t, RunRehearsal)
	l.attempt(1, PhaseRehearse, "", WhoAuto).start(1, PhaseRehearse, bootA)
	for i, b := range BlockList(PhaseRehearse, Files{}) {
		l.block(1, i+1, b)
		l.blockFiles(b, true)
	}
	l.add(Marker{Kind: MkRehearsalReady, N: 1})
	return l.end(1, "ok")
}

// quiet moves the time on, as when the reading is later.
func quiet(in Input, d time.Duration) Input {
	in.Now = in.Now.Add(d)
	return in
}

// running says that systemd has the unit active.
func running(in Input) Input {
	in.Unit.Active = true
	return in
}
