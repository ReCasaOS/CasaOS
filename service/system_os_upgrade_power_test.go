package service

import (
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// theMachinePowered records what the owner's page asked of the machine: the restart and the shutdown that it makes, which an upgrade that works refuses.
type theMachinePowered struct {
	mu      sync.Mutex
	actions []string
	err     error
}

func (p *theMachinePowered) asked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.actions...)
}

// aPowerButton puts a recorder in the place of the call that restarts or switches off the machine, which must never be the real one in a test. It says err when it is called.
func aPowerButton(t *testing.T, err error) *theMachinePowered {
	t.Helper()
	p := &theMachinePowered{err: err}
	was := systemPowerAction
	systemPowerAction = func(action string) error {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.actions = append(p.actions, action)
		return p.err
	}
	t.Cleanup(func() { systemPowerAction = was })
	return p
}

// anUpgradeAt makes the box hold a run in the state that is named, as its log and its unit would leave it, and says that the reducer reads it so.
func (b *upgradeBox) anUpgradeAt(state string) {
	b.t.Helper()
	plan := b.anUpgradePlan("ok", nil)
	var run *osUpgradeRun
	switch state {
	case osrun.StateRunning:
		run = b.attempt(b.aRun(plan, osrun.RunHop), 1, osrun.PhaseRun, osrun.WhoOwner)
		run = b.block(run, 1, 1, osrun.BlockVerify, false)
		b.setUnit("active")
	case osrun.StateRehearsing:
		run = b.attempt(b.aRun(plan, osrun.RunRehearsal), 1, osrun.PhaseRehearse, osrun.WhoOwner)
		run = b.block(run, 1, 1, osrun.BlockVerify, false)
		b.setUnit("active")
	case osrun.StateRefreshing:
		run = b.aRefreshAtWork()
		b.setUnit("active")
	case osrun.StateUnwinding:
		run = b.attempt(b.aHopThatWasLost(), 2, osrun.PhaseUnwind, osrun.WhoAuto)
		b.setUnit("active")
	case osrun.StateRecovering:
		run = b.attempt(b.aHopThatStoppedHalfWay(osrun.ClassBusy), 2, osrun.PhaseRecover, osrun.WhoOwner)
		b.setUnit("active")
	case osrun.StateVerifying:
		run = b.attempt(b.aHopThatRestarted(), 2, osrun.PhaseVerify, osrun.WhoAuto)
		b.setUnit("active")
	case osrun.StateResuming:
		run = b.aHopThatRestarted()
	case osrun.StateAwaitingReboot:
		run = b.aHopAtItsRestart()
	case osrun.StateRebooting:
		run = b.say(b.aHopAtItsRestart(), osrun.Marker{Kind: osrun.MkRebootRequested})
	case osrun.StateRebootStalled:
		run = b.say(b.aHopAtItsRestart(), osrun.Marker{Kind: osrun.MkRebootRequested})
		b.advance(16 * time.Minute)
	case osrun.StateRebootFirst:
		run = b.aRefreshWith("yes", "kernel")
	case osrun.StateRehearsed:
		run = b.aRehearsal()
	case osrun.StateInterrupted:
		run = b.aHopThatWasLost()
	case osrun.StateStoppedAtWaypoint:
		run = b.aHopThatStoppedHalfWay(osrun.ClassBusy)
	case osrun.StateFailed:
		run = b.attempt(b.aRun(plan, osrun.RunHop), 1, osrun.PhaseRun, osrun.WhoOwner)
		run = b.block(run, 1, 5, osrun.BlockDownload, false)
		run = b.stop(run, 1, 5, osrun.BlockDownload, osrun.ClassNetwork, false)
		run = b.say(run, osrun.Marker{Kind: osrun.MkUnwound, N: 1, Result: "ok"})
		run = b.end(run, 1, "stopped")
		b.setUnit("inactive")
		b.advance(time.Minute)
	case osrun.StateManual:
		run = b.say(b.aHopThatStoppedHalfWay(osrun.ClassBusy), osrun.Marker{Kind: osrun.MkTakeover})
	case osrun.StateNeedsAttention:
		// a pointer to a run whose folder is not there: the run that is open cannot be read
		if err := setOSUpgradeCurrent("0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19"); err != nil {
			b.t.Fatal(err)
		}
		return
	default:
		b.t.Fatalf("no story for the state %q", state)
	}
	b.point(run)
	if got := osrun.Reduce(b.osUpgradeInput(b.read(run.ID))).Name; got != state {
		b.t.Fatalf("the box was made to hold %q and the reducer says %q", state, got)
	}
}

// What is refused is exactly the states in which the unit works on the machine, or is about to: the page's restart and shutdown would cut dpkg short. Every other state of the
// run lets the machine be restarted, and it is the owner's to say when.
func TestAnUpgradeKeepsThePowerButtonsAwayInTheStatesThatChangeTheMachineAndInNoOther(t *testing.T) {
	refused := map[string]bool{
		osrun.StateRefreshing: true, osrun.StateRehearsing: true, osrun.StateRunning: true, osrun.StateUnwinding: true,
		osrun.StateRecovering: true, osrun.StateVerifying: true, osrun.StateResuming: true,
	}
	for _, name := range osrun.States {
		if got := osUpgradeKeepsPower(osrun.State{Name: name}); got != refused[name] {
			t.Errorf("%s: refused %v, want %v", name, got, refused[name])
		}
	}
	if len(refused) != 7 {
		t.Errorf("%d states are expected to be refused", len(refused))
	}
}

// Through the box, with the log and the unit of each state: the restart and the shutdown are refused with os_upgrade_open and the state, and nothing is asked of systemd; and in the
// states that let go they are made, and what the call says is what the owner is told.
func TestThePowerButtonsAreRefusedWhileTheUpgradeWorksAndMadeWhenItDoesNot(t *testing.T) {
	for _, state := range []string{
		osrun.StateRunning, osrun.StateRehearsing, osrun.StateRefreshing, osrun.StateUnwinding, osrun.StateRecovering, osrun.StateVerifying, osrun.StateResuming,
		osrun.StateAwaitingReboot, osrun.StateRebooting, osrun.StateRebootStalled, osrun.StateRebootFirst, osrun.StateRehearsed, osrun.StateInterrupted,
		osrun.StateStoppedAtWaypoint, osrun.StateFailed, osrun.StateManual, osrun.StateNeedsAttention,
	} {
		for _, button := range []struct {
			name, action string
			press        func(*upgradeBox) error
		}{
			{"restart", "reboot", func(b *upgradeBox) error { return b.SystemReboot() }},
			{"shutdown", "poweroff", func(b *upgradeBox) error { return b.SystemShutdown() }},
		} {
			t.Run(state+" "+button.name, func(t *testing.T) {
				box := newUpgradeBox(t)
				box.anUpgradeAt(state)
				power := aPowerButton(t, nil)
				err := button.press(box)
				if osUpgradeKeepsPower(osrun.State{Name: state}) {
					refusal := refusalOf(t, err)
					if refusal.Code != OSUpgradeOpen || refusal.Data["state"] != state || refusal.Detail == "" {
						t.Errorf("refused with %+v", refusal)
					}
					if got := power.asked(); len(got) != 0 {
						t.Errorf("the machine was asked to %q", got)
					}
					return
				}
				if err != nil {
					t.Errorf("refused: %v", err)
				}
				if got := power.asked(); !reflect.DeepEqual(got, []string{button.action}) {
					t.Errorf("the machine was asked to %q, want %q", got, button.action)
				}
			})
		}
	}
}

// With no upgrade on the machine the buttons are what they were: the call is made, and what it says is passed on as it is. A machine that never used the feature asks systemd
// nothing and reads one file.
func TestWithNoUpgradeThePowerButtonsAreAsTheyWere(t *testing.T) {
	box := newUpgradeBox(t)
	broken := errors.New("systemctl reboot: exit status 1")
	power := aPowerButton(t, broken)
	if err := box.SystemReboot(); err != broken {
		t.Errorf("the restart said %v, want the error of the call as it is", err)
	}
	if err := box.SystemShutdown(); err != broken {
		t.Errorf("the shutdown said %v, want the error of the call as it is", err)
	}
	if got := power.asked(); !reflect.DeepEqual(got, []string{"reboot", "poweroff"}) {
		t.Errorf("the machine was asked to %q", got)
	}
	if got := box.commandsWith(""); len(got) != 0 {
		t.Errorf("systemd was asked %q", got)
	}
	if _, err := os.Lstat(box.root); !os.IsNotExist(err) {
		t.Errorf("the folder of the runs was made: %v", err)
	}
}
