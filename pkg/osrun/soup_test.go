package osrun

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"
)

// randomMarker is a marker of a kind chosen at random, with valid fields chosen at random, for an attempt number chosen at random.
func randomMarker(rng *rand.Rand) Marker {
	pick := func(list []string) string { return list[rng.IntN(len(list))] }
	m := Marker{Kind: pick(MarkerKinds[1:]), At: t0.Add(time.Duration(rng.IntN(7200)) * time.Second)}
	if !schemas[m.Kind].ofRun {
		m.N = 1 + rng.IntN(5)
	}
	boot := []string{"", bootA, bootB, bootC}
	switch m.Kind {
	case MkRun:
		m.RunID, m.Hop, m.PlanID, m.RunKind, m.Boot = testRun, "debian-11-12", pick([]string{"", "p1-abc"}), pick(RunKinds), pick(boot)
	case MkAttempt:
		m.Phase, m.From, m.Who = pick(Phases), pick(append([]string{""}, BlockIDs...)), pick([]string{WhoAuto, WhoOwner})
	case MkAck:
		m.Result = pick([]string{"discard", "acknowledge"})
	case MkBootSeen, MkVerifyDone:
		m.Boot = pick(boot)
	case MkUnitStart:
		m.Phase, m.Boot, m.PID = pick(Phases), pick(boot), 1+rng.IntN(5000)
	case MkBlockStart, MkBlockDone, MkBlockSkipped:
		m.Index, m.ID = 1+rng.IntN(9), pick(BlockIDs)
	case MkStop:
		m.Index, m.ID, m.RC, m.Class, m.PNR = rng.IntN(9), pick(append([]string{StopDriver, StopList, StopUnwind}, BlockIDs...)), rng.IntN(256), pick(Classes), rng.IntN(2) == 0
	case MkUnwound:
		m.Result = pick([]string{"ok", "partial"})
	case MkRefreshDone:
		m.Result, m.Why = pick([]string{"yes", "no"}), "none"
	case MkRebootPending:
		m.Boot, m.Kernel = pick(boot), "k"
	case MkRebootNotNeeded:
		m.Kernel = "k"
	case MkUnitEnd:
		m.Result = pick([]string{"ok", "stopped"})
	}
	return m
}

// Whatever the log holds, in whatever order, and whatever the machine says, the reducer answers with a state that is consistent
// with itself, from a copy of its input as from the input, and without panicking.
func TestAnyMarkersInAnyOrderGiveAConsistentState(t *testing.T) {
	runs, seed := simParams()
	rng := rand.New(rand.NewPCG(seed, 1))
	boots := []string{bootA, bootB, bootC, ""}
	for run := 0; run < 12*runs; run++ {
		var ms []Marker
		first := []Marker{{Kind: MkQueued, At: t0}}
		if rng.IntN(20) > 0 {
			first = append(first, Marker{Kind: MkRun, RunID: testRun, Hop: "debian-11-12", RunKind: RunKinds[rng.IntN(3)], Boot: bootA})
		}
		ms = append(ms, first...)
		for i, n := 0, rng.IntN(25); i < n; i++ {
			// A run's own order is mostly kept, so that the interesting ones are reached, and sometimes not.
			m := randomMarker(rng)
			if prev := ms[len(ms)-1]; rng.IntN(3) == 0 && prev.N > 0 && !schemas[m.Kind].ofRun {
				m.N = prev.N
			}
			line, err := m.Line(testNonce)
			if err != nil {
				t.Fatalf("%+v: %v", m, err)
			}
			read, _, ok := parseMarker(line[:len(line)-1])
			if !ok {
				t.Fatalf("%q", line)
			}
			ms = append(ms, read)
		}
		b := boots[rng.IntN(4)]
		in := Input{
			Markers: ms,
			Files: Files{
				Begun: rng.IntN(3) == 0, DoneMinimal: rng.IntN(3) == 0, DoneFull: rng.IntN(3) == 0, DonePrereboot: rng.IntN(3) == 0,
				DoneAfter: rng.IntN(4) == 0, DoneTimers: rng.IntN(2) == 0, AptOrig: rng.IntN(2) == 0, AbortFlag: rng.IntN(5) == 0,
				BootIDFile: boots[rng.IntN(4)],
			},
			Unit:       Unit{Active: rng.IntN(2) == 0, Answered: rng.IntN(5) > 0},
			Boot:       Boot{Now: b, OK: b != "" && rng.IntN(8) > 0},
			Now:        t0.Add(time.Duration(rng.IntN(20000)) * time.Second),
			Ready:      rng.IntN(2) == 0,
			BootSeenAt: t0.Add(time.Duration(rng.IntN(20000)) * time.Second),
			Reminded:   rng.IntN(4),
			Damaged:    rng.IntN(30) == 0,
		}
		if rng.IntN(2) == 0 {
			in.LastActivity = t0.Add(time.Duration(rng.IntN(30000)) * time.Second)
		}
		if rng.IntN(2) == 0 {
			in.Verify = &VerifyResult{Errors: rng.IntN(2), Warnings: rng.IntN(2)}
		}
		if rng.IntN(2) == 0 {
			in.Barrier = &Barrier{OK: rng.IntN(2) == 0}
		}
		if rng.IntN(4) == 0 {
			in.Timing = repairing(rng.IntN(4), rng.IntN(4))
		}
		st := Reduce(in)
		act := NextAction(st, in)
		buttons := ActionsFor(st)

		switch {
		case !slices.Contains(States, st.Name), st.Reversible == st.PNR, st.Busy != busyOf(st.Name, st.PNR):
			t.Fatalf("an inconsistent state %+v for %+v", st, in)
		case st.Closed && st.Name != StateIdle, st.Class != "" && !slices.Contains(Classes, st.Class):
			t.Fatalf("an inconsistent state %+v for %+v", st, in)
		case st.PNR && !in.Files.DoneAfter && !st.Busy && !st.Closed && !decidedForGood(st):
			t.Fatalf("P4: %+v for %+v", st, in)
		case busyWhereItHasToBe(in, st) != "":
			t.Fatalf("P6: %s: %+v for %+v", busyWhereItHasToBe(in, st), st, in)
		case slices.Contains(buttons, ActionResume) && !st.Resumable, slices.Contains(buttons, ActionAbort) && (st.PNR || !st.Abortable),
			slices.Contains(buttons, ActionReboot) && st.RebootBlocked:
			t.Fatalf("buttons %v for %+v", buttons, st)
		case (act.Kind == DoUnwind || act.Kind == DoLaunchVerify || act.Kind == DoLaunchRecover) && (!in.Unit.Answered || in.Unit.Active):
			t.Fatalf("action %+v for %+v with unit %+v", act, st, in.Unit)
		case st.Name == StateManual && act.Kind != DoNone, st.Closed && act.Kind != DoCleanup:
			t.Fatalf("action %+v for %+v", act, st)
		}
		cp := cloneInput(in)
		if again := Reduce(cp); !reflect.DeepEqual(again, st) || NextAction(again, cp) != act || !reflect.DeepEqual(ActionsFor(again), buttons) {
			t.Fatalf("P3: %+v then %+v", st, again)
		}
	}
}

// decidedForGood is a state that a run can be in with the point of no return passed, and the machine free: the run is over.
func decidedForGood(st State) bool {
	switch st.Name {
	case StateSucceeded, StateSucceededWithFindings, StateManualDone:
		return true
	}
	return false
}
