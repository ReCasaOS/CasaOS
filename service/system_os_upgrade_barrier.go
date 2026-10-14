package service

import (
	"context"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
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
func (s *systemService) osUpgradeBarrier(ctx context.Context, run *osUpgradeRun) osUpgradeBarrierDoc {
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
	// what block 7 warned of is shown beside the restart as a check that passes: the owner is told, and the restart is still theirs
	for _, word := range osUpgradeWarningsOf(run) {
		doc.Checks = append(doc.Checks, OSUpgradeBarrierItem{Code: osverify.CodePrerebootWarning, OK: true, Severity: string(osreadiness.Warn), Arg: word})
	}
	return doc
}

// osUpgradeWarningsOf are the warnings that block 7 of the unit wrote down, once each and sorted: lower case words, and nothing else is taken from a file that the unit wrote.
func osUpgradeWarningsOf(run *osUpgradeRun) []string {
	if run.BK == "" {
		return nil
	}
	data, err := readOSUpgradeFile(filepath.Join(run.BK, osverify.BKWarnings), 4096)
	if err != nil {
		return nil
	}
	var words []string
	for _, line := range strings.Split(string(data), "\n") {
		if osUpgradeWarningWord.MatchString(line) && !slices.Contains(words, line) {
			words = append(words, line)
		}
	}
	slices.Sort(words)
	return words
}

var osUpgradeWarningWord = regexp.MustCompile(`^[a-z][a-z_]{0,39}$`)

// osUpgradeKeepBarrier makes barrier.json what the machine says now, for a run that waits for its restart, and gives what it says; what the file already says is not written
// again. For a state in which the barrier is not asked it does nothing, and gives nil.
func (s *systemService) osUpgradeKeepBarrier(ctx context.Context, run *osUpgradeRun, state osrun.State) *osUpgradeBarrierDoc {
	if state.Name != osrun.StateAwaitingReboot && state.Name != osrun.StateRebootStalled {
		return nil
	}
	doc := s.osUpgradeBarrier(ctx, run)
	if was := readOSUpgradeBarrier(run); was != nil && reflect.DeepEqual(*was, doc) {
		return &doc
	}
	if err := writeOSUpgradeBarrier(run, doc); err != nil {
		logger.Error("the barrier of an upgrade of the operating system could not be written", zap.String("run", run.ID), zap.Error(err))
		return &doc
	}
	logger.Info("the barrier before the restart of an upgrade of the operating system changed", zap.String("run", run.ID), zap.Bool("ok", doc.OK))
	return &doc
}
