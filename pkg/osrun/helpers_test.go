package osrun

import (
	"strings"
	"testing"
	"time"
)

const (
	testNonce = "0123456789abcdef0123456789abcdef"
	testRun   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	bootA     = "11111111-1111-4111-8111-111111111111"
	bootB     = "22222222-2222-4222-8222-222222222222"
	bootC     = "33333333-3333-4333-8333-333333333333"
)

var t0 = time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)

// A runLog builds the log of a run the way the core and a driver write it: a marker at a time, one second after the last
// one, through Line and ParseMarkers so that what the reducer reads is what a reader of the file would give it.
type runLog struct {
	t     *testing.T
	kind  string
	clock time.Time
	text  strings.Builder
	files Files
}

func newLog(t *testing.T, kind string) *runLog {
	l := &runLog{t: t, kind: kind, clock: t0}
	l.add(Marker{Kind: MkQueued})
	l.add(Marker{Kind: MkRun, RunID: testRun, Hop: "debian-11-12", PlanID: "p1-0123456789abcdef0123456789abcdef", RunKind: kind, Boot: bootA})
	return l
}

// add writes a marker at the clock, which then moves one second on.
func (l *runLog) add(m Marker) *runLog {
	l.t.Helper()
	if m.At.IsZero() {
		m.At = l.clock
	}
	l.clock = m.At.Add(time.Second)
	line, err := m.Line(testNonce)
	if err != nil {
		l.t.Fatalf("marker %+v: %v", m, err)
	}
	l.text.WriteString(line)
	return l
}

// wait moves the clock on.
func (l *runLog) wait(d time.Duration) *runLog {
	l.clock = l.clock.Add(d)
	return l
}

func (l *runLog) attempt(n int, phase, from, who string) *runLog {
	return l.add(Marker{Kind: MkAttempt, N: n, Phase: phase, From: from, Who: who})
}

func (l *runLog) start(n int, phase, boot string) *runLog {
	return l.add(Marker{Kind: MkUnitStart, N: n, Phase: phase, Boot: boot, PID: 4242})
}

func (l *runLog) blockStart(n, idx int, id string) *runLog {
	return l.add(Marker{Kind: MkBlockStart, N: n, Index: idx, ID: id})
}

func (l *runLog) blockDone(n, idx int, id string) *runLog {
	return l.add(Marker{Kind: MkBlockDone, N: n, Index: idx, ID: id})
}

func (l *runLog) block(n, idx int, id string) *runLog {
	return l.blockStart(n, idx, id).blockDone(n, idx, id)
}

func (l *runLog) stop(n, idx int, id string, rc int, class string, pnr bool) *runLog {
	return l.add(Marker{Kind: MkStop, N: n, Index: idx, ID: id, RC: rc, Class: class, PNR: pnr})
}

func (l *runLog) unwound(n int, result string) *runLog {
	return l.add(Marker{Kind: MkUnwound, N: n, Result: result})
}

func (l *runLog) end(n int, how string) *runLog {
	return l.add(Marker{Kind: MkUnitEnd, N: n, Result: how})
}

// input is the input the reducer is given: the log parsed back, the files, a unit that answers and is not running, and the
// boot the run began in. Now is the clock. A test changes what it needs.
func (l *runLog) input() Input {
	l.t.Helper()
	nonce, ms, ignored, err := ParseMarkers(strings.NewReader(l.text.String()))
	if err != nil || nonce != testNonce || ignored != 0 {
		l.t.Fatalf("the log did not read back: nonce %q, ignored %d, err %v", nonce, ignored, err)
	}
	return Input{Markers: ms, Files: l.files, Unit: Unit{Answered: true}, Boot: Boot{Now: bootA, OK: true}, Now: l.clock}
}

// The attempts below are the whole of what a unit writes for a phase, so that a test can build a run out of them.

// hopTo writes the unit of a hop (attempt n) as far as the block of this id has ended, the blocks before it ended, and the
// files the blocks leave. The id that follows is not started.
func (l *runLog) hopTo(n int, last string) *runLog {
	l.start(n, PhaseRun, bootA)
	order := BlockList(PhaseRun, Files{})
	for i, id := range order {
		l.blockStart(n, i+1, id)
		l.blockFiles(id, true)
		l.blockDone(n, i+1, id)
		if id == last {
			break
		}
	}
	return l
}

// blockFiles sets the files that a block leaves when it begins (begun) or ends.
func (l *runLog) blockFiles(id string, done bool) {
	switch id {
	case BlockTimers:
		l.files.DoneTimers = done
	case BlockSources:
		l.files.AptOrig = true
	case BlockMinimal:
		l.files.Begun = true
		l.files.DoneMinimal = done
	case BlockFull:
		l.files.DoneFull = done
	case BlockPrereboot:
		l.files.DonePrereboot = done
		l.files.BootIDFile = bootA
	case BlockAfter:
		l.files.DoneAfter = done
	}
}

// stateOf reduces the run as it stands.
func (l *runLog) stateOf() State { return Reduce(l.input()) }

func wantState(t *testing.T, got State, name string) {
	t.Helper()
	if got.Name != name {
		t.Fatalf("state %q (reason %q, cause %q, class %q), want %q", got.Name, got.Reason, got.Cause, got.Class, name)
	}
}

func assertInvariants(t *testing.T, s State) {
	t.Helper()
	if !isOneOf(States, s.Name) {
		t.Errorf("state %q is not one of the states", s.Name)
	}
	if s.Reversible == s.PNR {
		t.Errorf("state %q: reversible %v and pnr %v are not each other's opposite", s.Name, s.Reversible, s.PNR)
	}
	if s.Busy != busyOf(s.Name, s.PNR) {
		t.Errorf("state %q: busy %v", s.Name, s.Busy)
	}
}

// afterLast puts the reading d after the last marker of the log, which is what a time of quiet is counted from.
func afterLast(in Input, d time.Duration) Input {
	var last time.Time
	for _, m := range in.Markers {
		if m.At.After(last) {
			last = m.At
		}
	}
	in.Now = last.Add(d)
	return in
}

// clone is a copy of the log that a test can go on writing to without changing the original.
func (l *runLog) clone() *runLog {
	c := &runLog{t: l.t, kind: l.kind, clock: l.clock, files: l.files}
	c.text.WriteString(l.text.String())
	return c
}
