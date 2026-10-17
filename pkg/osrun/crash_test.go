package osrun

import (
	"testing"
	"time"
)

// step is one marker of a happy hop, with the files that the unit has left once it has written it, and the files it has left in
// the middle of the block that the marker begins.
type step struct {
	m          Marker
	files, mid Files
}

// happyHop is every marker a hop writes from its launch to the end of its check, with the files that each block leaves on its way.
// The files of a block are written after its BLOCK_START and before its BLOCK_DONE, so that the file of a step can be there with
// the marker of it not.
func happyHop() []step {
	var steps []step
	var files Files
	add := func(m Marker, mid Files) { steps = append(steps, step{m, files, mid}) }
	add(Marker{Kind: MkAttempt, N: 1, Phase: PhaseRun, Who: WhoAuto}, files)
	add(Marker{Kind: MkUnitStart, N: 1, Phase: PhaseRun, Boot: bootA, PID: 10}, files)
	for i, id := range BlockList(PhaseRun, Files{}) {
		started := files
		if id == BlockMinimal {
			started.Begun = true
		}
		add(Marker{Kind: MkBlockStart, N: 1, Index: i + 1, ID: id}, started)
		files = started
		switch id {
		case BlockTimers:
			files.DoneTimers = true
		case BlockSources:
			files.AptOrig = true
		case BlockMinimal:
			files.DoneMinimal = true
		case BlockFull:
			files.DoneFull = true
		case BlockPrereboot:
			files.DonePrereboot, files.BootIDFile = true, bootA
		}
		add(Marker{Kind: MkBlockDone, N: 1, Index: i + 1, ID: id}, files)
	}
	add(Marker{Kind: MkRebootPending, N: 1, Boot: bootA, Kernel: "6.1.0-37-amd64"}, files)
	add(Marker{Kind: MkUnitEnd, N: 1, Result: "ok"}, files)
	add(Marker{Kind: MkRebootRequested}, files)
	add(Marker{Kind: MkBootSeen, Boot: bootB}, files)
	add(Marker{Kind: MkAttempt, N: 2, Phase: PhaseVerify, Who: WhoAuto}, files)
	add(Marker{Kind: MkUnitStart, N: 2, Phase: PhaseVerify, Boot: bootB, PID: 20}, files)
	files.DoneAfter = true
	add(Marker{Kind: MkBlockStart, N: 2, Index: 1, ID: BlockAfter}, files)
	add(Marker{Kind: MkBlockDone, N: 2, Index: 1, ID: BlockAfter}, files)
	add(Marker{Kind: MkVerifyDone, N: 2, Boot: bootB}, files)
	add(Marker{Kind: MkUnitEnd, N: 2, Result: "ok"}, files)
	return steps
}

// The unit is killed, or the power goes, after every marker of a hop in turn, with the files of that moment, of the middle of the
// block, or of the next step (the file there and the marker not): whatever is read must be a state the files allow, and the
// core must undo what is not past the point of no return, and nothing else.
func TestAUnitLostAfterEveryMarkerOfAHop(t *testing.T) {
	steps := happyHop()
	checked := 0
	for k := 1; k <= len(steps); k++ {
		variants := map[string]Files{"": steps[k-1].files, "with the files of the middle of the block": steps[k-1].mid}
		if k < len(steps) {
			variants["with the file of the next step there"] = steps[k].files
		}
		for variant, files := range variants {
			for _, powerCut := range []bool{false, true} {
				l := newLog(t, RunHop)
				seen := false
				for _, s := range steps[:k] {
					l.add(s.m)
					seen = seen || s.m.Kind == MkBootSeen
				}
				in := afterLast(l.input(), time.Minute)
				in.Files = files
				in.Boot.Now = bootA
				if seen {
					in.Boot.Now = bootB
				}
				if powerCut {
					in.Boot.Now = bootC
				}
				last := steps[k-1].m
				name := last.Kind + " " + last.ID + " " + variant
				if powerCut {
					name += " on a new boot"
				}

				st := reduced(t, in)
				act := NextAction(st, in)
				checked++
				if st.PNR != files.PNR() {
					t.Errorf("after %s: pnr %v, files say %v", name, st.PNR, files.PNR())
				}
				switch {
				case act.Kind == DoUnwind && (st.PNR || st.Name != StateInterrupted):
					t.Errorf("after %s: an unwind of %+v", name, st)
				case st.Name == StateInterrupted && !st.PNR && act.Kind != DoUnwind:
					t.Errorf("after %s: a run that did not pass the point of no return was left: %+v", name, act)
				case act.Kind == DoLaunchRecover:
					t.Errorf("after %s: a repair by itself", name)
				case st.Name == StateInterrupted && st.PNR && st.Phase != PhaseVerify && act.Kind != DoAlert:
					t.Errorf("after %s: %+v for %+v", name, act, st)
				case st.Name == StateInterrupted && st.Phase == PhaseVerify && act.Kind != DoLaunchVerify:
					t.Errorf("after %s: the check was not run again: %+v", name, act)
				}
				if last.Kind == MkUnitEnd && last.N == 1 && variant == "" {
					switch st.Name {
					case StateAwaitingReboot, StateRebooting, StateRebootStalled, StateResuming:
					default:
						t.Errorf("after %s: %+v", name, st)
					}
				}
				if last.Kind == MkUnitEnd && last.N == 2 && st.Name != StateVerifying {
					t.Errorf("after %s: %+v", name, st)
				}
			}
		}
	}
	if checked < 150 {
		t.Errorf("%d cases", checked)
	}
}

// A block stops, in every block of the hop, with a status of every class: the run is undone and failed before the point of
// no return, and waits at the waypoint after it, whatever the class.
func TestAStopInEveryBlockOfAHopForEveryClass(t *testing.T) {
	for i, id := range BlockList(PhaseRun, Files{}) {
		for _, class := range Classes {
			l := hopLaunched(t)
			var files Files
			for j, before := range BlockList(PhaseRun, Files{})[:i] {
				l.blockStart(1, j+1, before)
				l.blockFiles(before, true)
				l.blockDone(1, j+1, before)
			}
			files = l.files
			l.blockStart(1, i+1, id)
			if id == BlockMinimal {
				l.files.Begun = true
				files = l.files
			}
			pnr := files.PNR()
			l.stop(1, i+1, id, 1, class, pnr)
			if !pnr {
				l.unwound(1, "ok")
			}
			l.end(1, "stopped")
			st := reduced(t, l.input())
			act := NextAction(st, l.input())
			if pnr {
				if st.Name != StateStoppedAtWaypoint || !st.Busy || !st.HalfUpgraded || st.Block.ID != id || act.Kind == DoUnwind {
					t.Errorf("block %s, class %s: %+v, %+v", id, class, st, act)
				}
				continue
			}
			if st.Name != StateFailed || st.Busy || !st.Reversible || st.Reason != class || st.Block.ID != id || act.Kind != DoNone {
				t.Errorf("block %s, class %s: %+v, %+v", id, class, st, act)
			}
		}
	}
}
