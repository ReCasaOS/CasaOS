package service

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
	"go.uber.org/zap"
)

// The judge is the core's comparison of the machine after the restart with the machine before the hop: the facts that the start took, the facts that are taken now, and what the
// blocks of the hop wrote down in the backup folder. It runs in the settling, once the unit that checked the machine is over, and never in the answer to a request. It only
// reads: nothing is repaired, no unit is restarted and no container is started because the judge found it down.
//
// It never turns what it could not see into a good word. It does not judge while systemd does not answer, nor when the core is told to stop while it looks, nor before the check's
// last block ended; what it could not take within its time is listed in the verdict (facts_incomplete, and not_judged), and the owner can ask for the check again.

// osUpgradeFactsLimit is what taking the facts of the machine has in all; each question of it has a time of its own (osrun.Timing.FactCommand).
const osUpgradeFactsLimit = 2 * time.Minute

// osUpgradeRestartFactsLimit is what the facts taken when the owner asks for the restart have: the click waits for them.
const osUpgradeRestartFactsLimit = 30 * time.Second

// osUpgradeRestartFactsFile holds the facts of the machine as the owner asked for its restart, in the run folder.
const osUpgradeRestartFactsFile = "facts-restart.json"

// osUpgradeBKProbe is a probe that finds the backup folder of a hop where the core keeps it: the unit is told the one name that its driver accepts, and the core reads the same
// folder under its own idea of where backups are.
type osUpgradeBKProbe struct{ osreadiness.Probe }

func (p osUpgradeBKProbe) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if rest, ok := strings.CutPrefix(path, osUpgradeUnitBackups+"/"); ok {
		path = filepath.Join(osUpgradeBackups, rest)
	}
	return p.Probe.ReadFile(ctx, path)
}

var (
	// errOSUpgradeNotJudged is a verdict that was not made, for a reason that the look says.
	errOSUpgradeNotJudged = errors.New("the machine is not judged now")
	// errOSUpgradeWaitsForContainers is a verdict that is not made yet: containers that have a restart policy are not up, and are waited for.
	errOSUpgradeWaitsForContainers = errors.New("containers that have a restart policy are waited for")
)

// osUpgradeJudge takes the facts of the machine now, judges them against the ones of before and the files of the backup folder, and writes the verdict into the run folder, whole or
// not at all. since is when the check ended. An error is a verdict that was not made, and is made again at the next look; errOSUpgradeWaitsForContainers is a verdict that waits for the
// containers of the machine that have a restart policy to come back, for as long as the constants say.
func (s *systemService) osUpgradeJudge(ctx context.Context, run *osUpgradeRun, since time.Time) error {
	u := s.systemPackageUpdater()
	if !run.Files.DoneAfter {
		return errors.Join(errOSUpgradeNotJudged, errors.New("the last block of the check did not end"))
	}
	if unit := s.osUpgradeUnitNow(); !unit.Answered {
		return errors.Join(errOSUpgradeNotJudged, errors.New("systemd does not answer"))
	}
	probe := newOSUpgradeProbe(u)
	factsCtx, cancel := context.WithTimeout(ctx, osUpgradeFactsLimit)
	defer cancel()
	after := osUpgradeTakeFacts(factsCtx, probe)
	bk, unreadable := osverify.ReadBKUnreadable(factsCtx, osUpgradeBKProbe{probe}, run.Meta.BK)
	if err := ctx.Err(); err != nil {
		// the core was told to stop while it looked: what was not taken is not a machine that is well, and nothing is written
		return errors.Join(errOSUpgradeNotJudged, err)
	}
	if !after.Knows(osverify.FactSystem) {
		return errors.Join(errOSUpgradeNotJudged, errors.New("systemd did not say what state the system is in"))
	}
	for _, name := range unreadable {
		after.Unknown = append(after.Unknown, osverify.UnreadablePrefix+name)
	}
	findings := osverify.JudgeWith(readOSUpgradeFacts(run), after, bk, osverify.Options{Restart: readOSUpgradeRestartFacts(run)})
	if !since.IsZero() && u.now().Sub(since) < osUpgradeTiming.ContainersWait {
		for _, f := range findings {
			if f.Code == osverify.CodeContainerNotReturned {
				return errOSUpgradeWaitsForContainers
			}
		}
	}
	return writeOSUpgradeVerify(run, osUpgradeVerifyDoc{At: u.now().UTC(), Findings: findings, NotJudged: osUpgradeNotJudged(findings)})
}

// osUpgradeNotJudged is what the verdict says it could not judge, from its findings.
func osUpgradeNotJudged(findings []osverify.Finding) []string {
	for _, f := range findings {
		if f.Code == osverify.CodeFactsIncomplete {
			return slices.DeleteFunc(strings.Split(f.Arg, ","), func(name string) bool { return name == "" })
		}
	}
	return nil
}

// osUpgradeTakeRestartFacts takes the facts of the machine when the owner asks for its restart, within a short time, and keeps them in the run folder: the machine may have waited for
// days, and what the owner changed meanwhile is not what the restart did. A machine that cannot be asked leaves no file, and the judge goes without.
func (s *systemService) osUpgradeTakeRestartFacts(ctx context.Context, run *osUpgradeRun) {
	factsCtx, cancel := context.WithTimeout(ctx, osUpgradeRestartFactsLimit)
	defer cancel()
	facts := osUpgradeTakeFacts(factsCtx, newOSUpgradeProbe(s.systemPackageUpdater()))
	if err := writeOSUpgradeResult(run, osUpgradeRestartFactsFile, facts); err != nil {
		logger.Error("the facts of the machine at its restart could not be kept", zap.String("run", run.ID), zap.Error(err))
	}
}

// readOSUpgradeRestartFacts is what the machine was when the owner asked for its restart: the zero Facts when it was not taken.
func readOSUpgradeRestartFacts(run *osUpgradeRun) osverify.Facts {
	return readOSUpgradeFactsFile(run, osUpgradeRestartFactsFile)
}
