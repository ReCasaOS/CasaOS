package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

func readJSON(path string, into any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}

// press is the owner's click on a button, as the route of the button runs it, with the word that the status asks for.
func (b *upgradeBox) press(button string, status OSUpgradeStatus) error {
	b.t.Helper()
	var err error
	switch button {
	case osrun.ActionAbort:
		_, err = b.AbortSystemOSUpgrade()
	case osrun.ActionReboot:
		_, err = b.RebootSystemOSUpgrade()
	case osrun.ActionResume:
		_, err = b.ResumeSystemOSUpgrade()
	case osrun.ActionAcknowledge:
		_, err = b.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckAcknowledge, Word: status.AckWord})
	case osrun.ActionDiscard:
		_, err = b.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckDiscard, Word: status.AckWord})
	case osrun.ActionTakeover:
		_, err = b.AckSystemOSUpgrade(OSUpgradeAckRequest{Action: OSUpgradeAckManual})
	default:
		b.t.Fatalf("a button that is not one: %q", button)
	}
	return err
}

// The runs of every state that there is, each as the files and the log of a machine make it. A setup gives the run, with the pointer and the flag as the core leaves them.
func theRunsOfEveryState() map[string]func(b *upgradeBox) *osUpgradeRun {
	judged := func(b *upgradeBox, spoil func(f *osverify.Facts)) *osUpgradeRun {
		run := b.aHopThatWasChecked()
		b.theFactsBefore(run, factsOfAMachine(b))
		after := factsOfAMachine(b)
		spoil(&after)
		takesFacts(b.t, after)
		b.tick()
		return b.read(run.ID)
	}
	return map[string]func(b *upgradeBox) *osUpgradeRun{
		"running before the point of no return": func(b *upgradeBox) *osUpgradeRun { return b.aHopAtItsDownload() },
		"running after it":                      func(b *upgradeBox) *osUpgradeRun { return b.aHopInTheFullUpgrade() },
		"a refresh at its first block": func(b *upgradeBox) *osUpgradeRun {
			run := b.block(b.aRefreshAtWork(), 1, 1, osrun.BlockRCheck, false)
			b.setUnit("active")
			return b.own(run, osrun.StateRefreshing)
		},
		"a refresh at its upgrade": func(b *upgradeBox) *osUpgradeRun {
			run := b.block(b.aRefreshAtWork(), 1, 3, osrun.BlockRUpgrade, false)
			b.setUnit("active")
			return b.own(run, osrun.StateRefreshing)
		},
		"a rehearsal at work": func(b *upgradeBox) *osUpgradeRun {
			run := b.aRun(b.anUpgradePlan("ok", nil), osrun.RunRehearsal)
			run = b.attempt(run, 1, osrun.PhaseRehearse, osrun.WhoOwner)
			run = b.block(run, 1, 4, osrun.BlockSources, false)
			b.setUnit("active")
			return b.own(run, osrun.StateRehearsing)
		},
		"a rehearsal that is ready": func(b *upgradeBox) *osUpgradeRun { return b.own(b.aRehearsal(), osrun.StateRehearsed) },
		"a rehearsal that was undone": func(b *upgradeBox) *osUpgradeRun {
			run := b.attempt(b.aRehearsal(), 2, osrun.PhaseUnwind, osrun.WhoOwner)
			run = b.say(run, osrun.Marker{Kind: osrun.MkUnwound, N: 2, Result: "ok"})
			run = b.end(run, 2, "ok")
			b.setUnit("inactive")
			b.advance(time.Minute)
			return b.own(b.read(run.ID), osrun.StateRehearsalDone)
		},
		"a hop that waits for its restart": func(b *upgradeBox) *osUpgradeRun {
			run := b.own(b.aHopAtItsRestart(), osrun.StateAwaitingReboot)
			b.tick() // what stands before the restart is computed at every look
			return run
		},
		"a hop whose restart is not offered": func(b *upgradeBox) *osUpgradeRun {
			m := theBarrierSees(b.t)
			aFstabThatDoesNotResolve(m, false)
			run := b.own(b.aHopAtItsRestart(), osrun.StateAwaitingReboot)
			b.tick()
			return run
		},
		"a restart that was asked for": func(b *upgradeBox) *osUpgradeRun {
			run := b.say(b.aHopAtItsRestart(), osrun.Marker{Kind: osrun.MkRebootRequested})
			return b.own(run, osrun.StateRebooting)
		},
		"a restart that did not happen": func(b *upgradeBox) *osUpgradeRun {
			run := b.say(b.aHopAtItsRestart(), osrun.Marker{Kind: osrun.MkRebootRequested})
			b.advance(time.Hour)
			b.own(run, osrun.StateRebootStalled)
			b.tick()
			return run
		},
		"a machine that restarted":          func(b *upgradeBox) *osUpgradeRun { return b.own(b.aHopThatRestarted(), osrun.StateResuming) },
		"a machine that waits to be judged": func(b *upgradeBox) *osUpgradeRun { return b.own(b.aHopThatWasChecked(), osrun.StateVerifying) },
		"a success":                         func(b *upgradeBox) *osUpgradeRun { return b.aHopThatSucceeded() },
		"a success with findings": func(b *upgradeBox) *osUpgradeRun {
			return judged(b, func(f *osverify.Facts) { f.DNS = false })
		},
		"a check that the judge failed": func(b *upgradeBox) *osUpgradeRun {
			run := b.aHopThatWasChecked()
			if err := os.WriteFile(filepath.Join(b.backupOf(run), "kernel"), []byte("6.1.0-99-amd64\n"), 0o600); err != nil {
				b.t.Fatal(err)
			}
			b.theFactsBefore(run, factsOfAMachine(b))
			takesFacts(b.t, factsOfAMachine(b))
			b.tick()
			return b.read(run.ID)
		},
		"a check that a block of it stopped": func(b *upgradeBox) *osUpgradeRun {
			run := b.attempt(b.aHopThatRestarted(), 2, osrun.PhaseVerify, osrun.WhoAuto)
			run = b.block(run, 2, 1, osrun.BlockAfter, false)
			run = b.stop(run, 2, 1, osrun.BlockAfter, osrun.ClassBarrierKernel, true)
			run = b.end(run, 2, "stopped")
			b.setUnit("inactive")
			b.advance(time.Minute)
			return b.own(b.read(run.ID), osrun.StateFailed)
		},
		"a check that was lost": func(b *upgradeBox) *osUpgradeRun {
			run := b.attempt(b.aHopThatRestarted(), 2, osrun.PhaseVerify, osrun.WhoAuto)
			run = b.block(run, 2, 1, osrun.BlockAfter, false)
			b.setUnit("inactive")
			b.advance(time.Minute)
			return b.own(b.read(run.ID), osrun.StateInterrupted)
		},
		"a hop that stopped on a class that is repaired": func(b *upgradeBox) *osUpgradeRun {
			return b.own(b.aHopThatStoppedHalfWay(osrun.ClassNetwork), osrun.StateStoppedAtWaypoint)
		},
		"a hop that stopped on a class that is not": func(b *upgradeBox) *osUpgradeRun {
			return b.own(b.aHopThatStoppedHalfWay(osrun.ClassGuard), osrun.StateStoppedAtWaypoint)
		},
		"a stop that was cut short": func(b *upgradeBox) *osUpgradeRun {
			run := b.aRun(b.anUpgradePlan("ok", nil), osrun.RunHop)
			run = b.attempt(run, 1, osrun.PhaseRun, osrun.WhoOwner)
			run = b.leave(run, "begun-upgrade", "done-minimal")
			run = b.block(run, 1, 7, osrun.BlockFull, false)
			run = b.stop(run, 1, 7, osrun.BlockFull, osrun.ClassGuard, true)
			b.setUnit("inactive")
			b.advance(time.Minute)
			return b.own(b.read(run.ID), osrun.StateInterrupted)
		},
		"a hop lost before the point of no return": func(b *upgradeBox) *osUpgradeRun { return b.own(b.aHopThatWasLost(), osrun.StateRunning) },
		"a hop lost after it": func(b *upgradeBox) *osUpgradeRun {
			run := b.aHopInTheFullUpgrade()
			b.setUnit("inactive")
			b.advance(time.Minute)
			return b.own(b.read(run.ID), osrun.StateRunning)
		},
		"a start that did not come": func(b *upgradeBox) *osUpgradeRun {
			run := b.aRun(b.anUpgradePlan("ok", nil), osrun.RunHop)
			run = b.say(run, osrun.Marker{Kind: osrun.MkAttempt, N: 1, Phase: osrun.PhaseRun, Who: osrun.WhoOwner}, osrun.Marker{Kind: osrun.MkStartFailed, N: 1})
			return b.own(run, osrun.StateRunning)
		},
		"a start that did not come after the point of no return": func(b *upgradeBox) *osUpgradeRun {
			run := b.aHopThatStoppedHalfWay(osrun.ClassNetwork)
			run = b.say(run, osrun.Marker{Kind: osrun.MkAttempt, N: 2, Phase: osrun.PhaseRecover, Who: osrun.WhoOwner}, osrun.Marker{Kind: osrun.MkStartFailed, N: 2})
			return b.own(run, osrun.StateFailed)
		},
		"a run that failed before it changed anything": func(b *upgradeBox) *osUpgradeRun { return b.aRunThatFailedBeforeItChangedAnything() },
		"an unwind that was partial": func(b *upgradeBox) *osUpgradeRun {
			run := b.aRun(b.anUpgradePlan("ok", nil), osrun.RunHop)
			run = b.attempt(run, 1, osrun.PhaseRun, osrun.WhoOwner)
			run = b.leave(run, "apt-orig", "done-timers")
			run = b.block(run, 1, 4, osrun.BlockSources, false)
			run = b.stop(run, 1, 4, osrun.BlockSources, osrun.ClassNetwork, false)
			run = b.say(run, osrun.Marker{Kind: osrun.MkUnwound, N: 1, Result: "partial"})
			run = b.end(run, 1, "stopped")
			b.setUnit("inactive")
			b.advance(time.Minute)
			return b.own(b.read(run.ID), osrun.StateNeedsAttention)
		},
		"a systemd that has been silent": func(b *upgradeBox) *osUpgradeRun {
			run := b.own(b.aHopAtItsRestart(), osrun.StateAwaitingReboot)
			b.setUnit("")
			b.systemPackageUpdater().askOSUpgradeUnit()
			b.advance(time.Hour)
			return run
		},
		"a run whose files are not the ones that were written": func(b *upgradeBox) *osUpgradeRun {
			run := b.own(b.aHopAtItsRestart(), osrun.StateAwaitingReboot)
			if err := os.WriteFile(filepath.Join(run.Dir, "blocks", "verify.sh"), []byte("echo changed\n"), 0o600); err != nil {
				b.t.Fatal(err)
			}
			return run
		},
		"a run that was taken over": func(b *upgradeBox) *osUpgradeRun {
			run := b.say(b.aHopThatStoppedHalfWay(osrun.ClassNetwork), osrun.Marker{Kind: osrun.MkTakeover})
			return b.own(run, osrun.StateManual)
		},
		"a run that was finished by hand": func(b *upgradeBox) *osUpgradeRun {
			run := b.say(b.aHopThatStoppedHalfWay(osrun.ClassNetwork), osrun.Marker{Kind: osrun.MkTakeover})
			run = b.leave(run, "done-full", "done-prereboot", "done-after")
			return b.own(run, osrun.StateManual)
		},
		"a refresh that ended": func(b *upgradeBox) *osUpgradeRun { return b.own(b.aRefresh(), osrun.StateRefreshed) },
		"a refresh that wants its restart": func(b *upgradeBox) *osUpgradeRun {
			return b.own(b.aRefreshWith("yes", "kernel"), osrun.StateRebootFirst)
		},
	}
}

// Every button that the status offers is one that the route of it accepts, and every one that it does not offer is refused: the page shows what the server's table says, and the
// routes hold to the same table. A click on a button that the status showed is never answered with "not now": that is what a launch that the log would not take did to a check that a
// block of it stopped.
func TestEveryButtonThatIsOfferedWorksAndEveryOneThatIsNotIsRefused(t *testing.T) {
	for name, setup := range theRunsOfEveryState() {
		probe := newUpgradeBox(t)
		probe.readyToResume()
		probe.aMachineThatRestartsWith(nil)
		setup(probe)
		offered := probe.GetSystemOSUpgradeStatus().Actions
		for _, button := range osrun.ActionNames {
			t.Run(name+"/"+button, func(t *testing.T) {
				box := newUpgradeBox(t)
				box.readyToResume()
				box.greenBarrier()
				box.aMachineThatRestartsWith(nil)
				setup(box)
				status := box.GetSystemOSUpgradeStatus()
				if !slices.Equal(status.Actions, offered) {
					t.Fatalf("the buttons of the same run differ between two readings: %v and %v", status.Actions, offered)
				}
				err := box.press(button, status)
				switch {
				case slices.Contains(status.Actions, button) && err != nil:
					t.Errorf("the state is %q %q and the button %s is offered, and it was refused: %v", status.State, status.Reason, button, err)
				case !slices.Contains(status.Actions, button) && err == nil && !(button == osrun.ActionDiscard && status.State == osrun.StateFailed):
					// a failed run is dropped with discard whether or not a plan is alive: there is only nothing for it to drop
					t.Errorf("the state is %q %q and the button %s is not offered, and it was accepted", status.State, status.Reason, button)
				}
			})
		}
	}
}
