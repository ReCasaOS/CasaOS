package service

import (
	"context"
	"reflect"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"go.uber.org/zap"
)

// The barrier is what stands between a hop that is done and the restart of the machine: the checks of the report that say whether the machine will come back from it. A line of
// /etc/fstab that names a disk that is not there, and does not say nofail, stops the machine in emergency mode at its next start, on a box that may have no screen; the core says
// so before the owner restarts it, and does not offer the restart while it does. The checks are asked of the machine again at every look while it waits, so that the owner who
// mends the line has nothing to ask for. They only read, and nothing is ever fixed by the core: the page shows what is wrong.

// osUpgradeBarrierChecks are the checks of the report that are asked, by their codes, in the order they are listed in.
var osUpgradeBarrierChecks = []string{"fstab_unresolved", "fstab_network", "network_names"}

// osUpgradeBarrierProbe is the probe that the barrier looks at the machine through: the core's own, and not a second way of reading the machine. A var, for the tests.
var osUpgradeBarrierProbe = func(u *systemPackageUpdater) osreadiness.Probe { return newOSUpgradeProbe(u) }

// osUpgradeBarrierLimit is what the checks have in all.
const osUpgradeBarrierLimit = osreadiness.DefaultBudget

// osUpgradeBarrier asks the checks and says what they found. A check that is not in the table, or could not be made (a refusal that says it does not know), is red: the machine is
// restarted only when everything that stands before it is known to be well.
func (s *systemService) osUpgradeBarrier(ctx context.Context) osUpgradeBarrierDoc {
	askCtx, cancel := context.WithTimeout(ctx, osUpgradeBarrierLimit)
	defer cancel()
	probe := osUpgradeBarrierProbe(s.systemPackageUpdater())
	doc := osUpgradeBarrierDoc{OK: true, Checks: []OSUpgradeBarrierItem{}}
	for _, code := range osUpgradeBarrierChecks {
		item := OSUpgradeBarrierItem{Code: code, Severity: string(osreadiness.Refuse)}
		if finding, ok := osreadiness.CheckByCode(askCtx, probe, code); ok {
			item.Severity = string(finding.Severity)
			item.OK = finding.Severity != osreadiness.Refuse
		}
		doc.OK = doc.OK && item.OK
		doc.Checks = append(doc.Checks, item)
	}
	return doc
}

// osUpgradeKeepBarrier makes barrier.json what the machine says now, for a run that waits for its restart; what it already says is not written again.
func (s *systemService) osUpgradeKeepBarrier(ctx context.Context, run *osUpgradeRun, state osrun.State) {
	if state.Name != osrun.StateAwaitingReboot && state.Name != osrun.StateRebootStalled {
		return
	}
	doc := s.osUpgradeBarrier(ctx)
	if was := readOSUpgradeBarrier(run); was != nil && reflect.DeepEqual(*was, doc) {
		return
	}
	if err := writeOSUpgradeBarrier(run, doc); err != nil {
		logger.Error("the barrier of an upgrade of the operating system could not be written", zap.String("run", run.ID), zap.Error(err))
		return
	}
	logger.Info("the barrier before the restart of an upgrade of the operating system changed", zap.String("run", run.ID), zap.Bool("ok", doc.OK))
}
