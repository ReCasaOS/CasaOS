package osrun

import (
	"slices"
	"testing"
	"time"
)

// What the log says is progress only where the files say it too; and the word of a stop is its class even when the unit that said
// it was cut short a moment later.

func TestANeedsAttentionRunIsClosedWithTheWordBeforeThePointOfNoReturnToo(t *testing.T) {
	// The unwind could not put the sources back: the sources of the next release may be in place, with nothing changed in packages.
	l := hopInBlock(t, BlockSources).stop(1, 4, BlockSources, 1, ClassNetwork, false).unwound(1, "partial").end(1, "stopped")
	s := reduced(t, l.input())
	is(t, s, StateNeedsAttention, ReasonUnwindPartial)
	if s.PNR || s.HalfUpgraded || !s.Reversible {
		t.Fatalf("nothing of packages was changed: %+v", s)
	}
	if !s.NeedsAckWord() {
		t.Errorf("a machine whose sources may be the next release's is not closed with a click: %+v", s)
	}
	// A run whose files are not the ones that were written is the same.
	in := hopInBlock(t, BlockDownload).input()
	in.Damaged = true
	s = reduced(t, in)
	is(t, s, StateNeedsAttention, ReasonRunDamaged)
	if !s.NeedsAckWord() || !slices.Contains(ActionsFor(s), ActionAcknowledge) {
		t.Errorf("%+v", s)
	}
	// What is not in need of attention is not asked for the word when it was not half upgraded.
	if s := reduced(t, hopAtReboot(t).input()); s.NeedsAckWord() {
		t.Errorf("%+v", s)
	}
	if s := reduced(t, refreshDone(t, false).input()); s.NeedsAckWord() {
		t.Errorf("%+v", s)
	}
}

func TestAStopThatHasNoEndKeepsItsClass(t *testing.T) {
	gone := func(l *runLog) Input { return quiet(l.input(), time.Minute) }
	for _, c := range []struct {
		class     string
		resumable bool
		want      string
	}{
		{ClassNetwork, true, ClassNetwork},
		{ClassBusy, true, ClassBusy},
		{ClassDisk, true, ClassDisk},
		{ClassGuard, false, ClassGuard},
		{ClassConverge, false, ClassConverge},
		{ClassUnit, false, ClassUnit},
		{ClassIntegrity, false, ClassIntegrity},
		// a plan that no longer holds, when packages are being changed, is the guard's word
		{ClassPlan, false, ClassGuard},
	} {
		// The unit said why it stopped, after the point of no return, and was cut short before it ended.
		l := hopInBlock(t, BlockFull).stop(1, 7, BlockFull, 1, c.class, true)
		s := reduced(t, gone(l))
		is(t, s, StateInterrupted, CauseKilled)
		if s.Class != c.want || s.Resumable != c.resumable || s.Block.ID != BlockFull || s.RC != 1 {
			t.Errorf("%s: %+v", c.class, s)
		}
		if got := slices.Contains(ActionsFor(s), ActionResume); got != c.resumable {
			t.Errorf("%s: the resume button is %v", c.class, got)
		}
		// With its end the same stop says the same.
		ended := reduced(t, gone(hopInBlock(t, BlockFull).stop(1, 7, BlockFull, 1, c.class, true).end(1, "stopped")))
		is(t, ended, StateStoppedAtWaypoint, "")
		if ended.Class != s.Class || ended.Resumable != s.Resumable {
			t.Errorf("%s: with its end %+v, without %+v", c.class, ended, s)
		}
		// And a machine that is cut at a stop is not repaired by a launch that the class does not allow.
		if act := NextAction(s, gone(l)); act.Kind == DoLaunchRecover {
			t.Errorf("%s: %+v", c.class, act)
		}
	}
	// Before the point of no return the stop is the class of what is undone, and nothing is resumed.
	l := hopInBlock(t, BlockDownload).stop(1, 5, BlockDownload, 100, ClassNetwork, false)
	s := reduced(t, gone(l))
	is(t, s, StateInterrupted, CauseKilled)
	if s.Class != ClassNetwork || s.Resumable {
		t.Errorf("%+v", s)
	}
	if act := NextAction(s, gone(l)); act.Kind != DoUnwind {
		t.Errorf("%+v", act)
	}
	// A check that is cut after its stop is made again.
	v := hopRestarted(t).blockStart(2, 1, BlockAfter).stop(2, 1, BlockAfter, 1, ClassBarrierKernel, true)
	in := gone(v)
	in.Boot.Now = bootB
	s = reduced(t, in)
	is(t, s, StateInterrupted, CauseKilled)
	if !s.Resumable {
		t.Errorf("%+v", s)
	}
	if act := NextAction(s, in); act.Kind != DoLaunchVerify {
		t.Errorf("%+v", act)
	}
}

func TestAResultOfTheLogWithoutTheFileOfItsBlockIsNotProgress(t *testing.T) {
	// The log says the last block before the restart ended and its file is not there: the restart is not offered.
	l := hopAtReboot(t)
	l.files.DonePrereboot = false
	in := l.input()
	s := reduced(t, in)
	is(t, s, StateAwaitingReboot, "")
	if !s.RebootBlocked || slices.Contains(ActionsFor(s), ActionReboot) {
		t.Errorf("%+v %v", s, ActionsFor(s))
	}
	in.Barrier = &Barrier{OK: true}
	if s := reduced(t, in); !s.RebootBlocked || slices.Contains(ActionsFor(s), ActionReboot) {
		t.Errorf("a green barrier does not make the file that is not there: %+v", s)
	}
	// With the file it is as it was.
	l.files.DonePrereboot = true
	in = l.input()
	in.Barrier = &Barrier{OK: true}
	if s := reduced(t, in); s.RebootBlocked || !slices.Contains(ActionsFor(s), ActionReboot) {
		t.Errorf("%+v", s)
	}
	// A restart that was asked for, and did not happen, is not made offerable either.
	l.files.DonePrereboot = false
	l.add(Marker{Kind: MkRebootRequested})
	in = quiet(l.input(), time.Hour)
	in.Barrier = &Barrier{OK: true}
	s = reduced(t, in)
	is(t, s, StateRebootStalled, "")
	if !s.RebootBlocked {
		t.Errorf("%+v", s)
	}
}

func TestACheckThatSaysItEndedWithoutTheFileOfItsBlockIsNoVerdict(t *testing.T) {
	l := hopVerified(t)
	l.files.DoneAfter = false
	for name, verify := range map[string]*VerifyResult{"not judged": nil, "judged clean": {}, "judged with findings": {Warnings: 1}} {
		in := l.input()
		in.Boot.Now = bootB
		in.Verify = verify
		s := reduced(t, in)
		is(t, s, StateNeedsAttention, ReasonRunDamaged)
		if !s.Busy {
			t.Errorf("%s: the flag of a machine that is not known to be well is kept: %+v", name, s)
		}
		if act := NextAction(s, in); act.Kind != DoNone {
			t.Errorf("%s: %+v", name, act)
		}
	}
	// With its file it is a verdict.
	l.files.DoneAfter = true
	in := l.input()
	in.Boot.Now = bootB
	in.Verify = &VerifyResult{}
	is(t, reduced(t, in), StateSucceeded, "")
}
