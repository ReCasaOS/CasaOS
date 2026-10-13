package service

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// The judge is the core's comparison of the machine after the restart with the machine before the hop: the facts that the start took, the facts that are taken now, and what the
// blocks of the hop wrote down in the backup folder. It runs in the settling, once the unit that checked the machine is over, and never in the answer to a request. It only
// reads: nothing is repaired, no unit is restarted and no container is started because the judge found it down.

// osUpgradeFactsLimit is what taking the facts of the machine has in all.
const osUpgradeFactsLimit = 2 * time.Minute

// osUpgradeBKProbe is a probe that finds the backup folder of a hop where the core keeps it: the unit is told the one name that its driver accepts, and the core reads the same
// folder under its own idea of where backups are.
type osUpgradeBKProbe struct{ osreadiness.Probe }

func (p osUpgradeBKProbe) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if rest, ok := strings.CutPrefix(path, osUpgradeUnitBackups+"/"); ok {
		path = filepath.Join(osUpgradeBackups, rest)
	}
	return p.Probe.ReadFile(ctx, path)
}

// osUpgradeJudge takes the facts of the machine now, judges them against the ones of before and the files of the backup folder, and writes the verdict into the run folder,
// whole or not at all. A verdict that could not be written is made again at the next look.
func (s *systemService) osUpgradeJudge(ctx context.Context, run *osUpgradeRun) error {
	u := s.systemPackageUpdater()
	probe := newOSUpgradeProbe(u)
	factsCtx, cancel := context.WithTimeout(ctx, osUpgradeFactsLimit)
	defer cancel()
	after := osUpgradeTakeFacts(factsCtx, probe)
	bk := osverify.ReadBK(factsCtx, osUpgradeBKProbe{probe}, run.Meta.BK)
	findings := osverify.Judge(readOSUpgradeFacts(run), after, bk)
	return writeOSUpgradeVerify(run, osUpgradeVerifyDoc{At: u.now().UTC(), Findings: findings})
}
