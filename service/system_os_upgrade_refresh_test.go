package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The start of the refresh of the release that the machine runs: the checks that it makes, the order of them, and the run that it writes. The machine is the one of the tests of
// the start, with no plan at all (a refresh does not need one), and sources that the rewrite of a refresh gives.

// theSourcesOfARefresh are the sources of the machine as a refresh has them: its own, with Docker's put aside. It is the hop of a refresh that it is asked for.
func theSourcesOfARefresh(hop osplan.HopSpec) (*osplan.Rewritten, error) {
	if !hop.Refresh {
		return nil, errors.New("the sources are rewritten for the next release, and the refresh keeps the release that is left")
	}
	return &osplan.Rewritten{
		Tree:        []osplan.TreeFile{{Path: "sources.list", Content: "deb http://deb.debian.org/debian bullseye main\n"}},
		Paused:      []osplan.Paused{{File: "docker.list", Hosts: []string{"https://download.docker.com"}, Docker: true}},
		HashCurrent: strings.Repeat("a", 64), HashGenerated: strings.Repeat("b", 64),
	}, nil
}

// aMachineThatCanRefresh is the box with the switch on and the stubs of a start, and the sources of a refresh.
func (b *upgradeBox) aMachineThatCanRefresh() *startStubs {
	b.t.Helper()
	b.switchOn()
	st := b.stubStart()
	st.rewritten = theSourcesOfARefresh
	return st
}

func TestARefreshThatTheChecksLetThroughWritesTheRunAndStartsItsUnit(t *testing.T) {
	box := newUpgradeBox(t)
	box.aMachineThatCanRefresh()
	box.setBoot(boxBoot1)

	status, err := box.StartSystemOSUpgradeRefresh(OSUpgradeRefreshRequest{})
	if err != nil {
		t.Fatal(err)
	}
	runID := status.RunID
	if !osrun.ValidUUID(runID) || status.State != osrun.StateRefreshing || !status.Busy || status.Kind != osrun.RunRefresh || status.Phase != osrun.PhaseRefresh || status.Attempt != 1 || status.PNR || status.PlanID != "" {
		t.Fatalf("the status is %+v", status)
	}
	if status.Manual.NextBlock != "" || len(status.Manual.BlocksDone) != 0 || status.Manual.BKPath != osplan.RefreshFolderPrefix+runID {
		t.Errorf("the refresh says where the owner stands on the blocks of a hop: %+v", status.Manual)
	}
	run := box.read(runID)
	if got, _ := currentOSUpgrade(); got != runID {
		t.Errorf("the latest run is %q", got)
	}
	var flag osUpgradeBusyFlag
	if err := json.Unmarshal([]byte(readText(t, osUpgradeBusyFile)), &flag); err != nil || flag.RunID != runID || flag.State != osrun.StateRefreshing {
		t.Errorf("the flag is %+v, %v", flag, err)
	}

	// the log: QUEUED, RUN as a refresh, with no plan and the boot, ATTEMPT 1 of the phase of the refresh launched by the owner
	if len(run.Markers) != 3 || run.Ignored != 0 {
		t.Fatalf("the log has %+v (%d ignored)", run.Markers, run.Ignored)
	}
	rm, attempt := run.Markers[1], run.Markers[2]
	if rm.Kind != osrun.MkRun || rm.RunID != runID || rm.Hop != "debian-11-12" || rm.PlanID != "" || rm.RunKind != osrun.RunRefresh || rm.Boot != boxBoot1 {
		t.Errorf("RUN is %+v", rm)
	}
	if attempt.Kind != osrun.MkAttempt || attempt.N != 1 || attempt.Phase != osrun.PhaseRefresh || attempt.Who != osrun.WhoOwner || attempt.From != "" {
		t.Errorf("the attempt is %+v", attempt)
	}

	// the unit
	started := box.startedUnits()
	if len(started) != 1 {
		t.Fatalf("%d units were started", len(started))
	}
	want, err := osUpgradeUnitArgs(osrun.Attempt{RunID: runID, Nonce: run.Nonce, Phase: osrun.PhaseRefresh, N: 1}, osUpgradeUnitRoot+"/"+runID)
	if err != nil || !reflect.DeepEqual(started[0][2:], want) {
		t.Errorf("the unit was started with %q", started[0][2:])
	}

	// the folder: the four blocks and the one that mends, as osplan writes them for this run and this machine, and the files that the sums cover
	rewritten, _ := theSourcesOfARefresh(mustPlannedHop(t).ForRefresh())
	text, err := osplan.BuildRefresh(osplan.RefreshInputs{Hop: mustPlannedHop(t).ForRefresh(), Rewritten: rewritten, RunID: runID, Hostname: "box1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, blk := range append(append([]osplan.AutoBlock{}, text.Blocks...), text.Forward) {
		if got := readText(t, filepath.Join(run.Dir, "blocks", blk.ID+".sh")); got != blk.Text {
			t.Errorf("blocks/%s.sh is not the text that is made for this run", blk.ID)
		}
	}
	if got := readText(t, filepath.Join(run.Dir, "lists", "1.list")); got != "rcheck\nrsources\nrupgrade\nrdone\n" {
		t.Errorf("the list is %q", got)
	}
	if run.Meta.Kind != osrun.RunRefresh || run.Meta.PlanID != "" || run.Meta.BK != osplan.RefreshFolderPrefix+runID || len(run.Meta.Blocks) != 5 || run.Meta.Blocks[0].ID != "rcheck" || run.Meta.Blocks[4].ID != "forward" {
		t.Errorf("run.json is %+v", run.Meta)
	}
	if err := verifyOSUpgradeRun(run.Dir, osrun.RunRefresh); err != nil {
		t.Errorf("the folder does not pass its sums: %v", err)
	}
	if _, err := os.Stat(filepath.Join(run.Dir, "blocks", "verify.sh")); err == nil {
		t.Error("a block of the hop is in the folder of a refresh")
	}
	// plan.json holds the sources that the refresh was made from (the owner's lines, which the page does not show), and manual-runbook.sh says that there is no road by hand
	var record osUpgradeRefreshRecord
	if err := json.Unmarshal([]byte(readText(t, filepath.Join(run.Dir, "plan.json"))), &record); err != nil || record.Hop != "debian-11-12" || len(record.Tree) != 1 || len(record.Paused) != 1 || record.HashCurrent != strings.Repeat("a", 64) || record.AcceptReleaseInfo {
		t.Errorf("plan.json is %+v, %v", record, err)
	}
	if got := readText(t, filepath.Join(run.Dir, "manual-runbook.sh")); got != osUpgradeRefreshManual {
		t.Errorf("manual-runbook.sh is %q", got)
	}
	// ... and the runbook of the run is not asked for text that it has not got
	if _, err := box.GetSystemOSUpgradeRunbook(); refusalOf(t, err).Code != OSUpgradeNoRun {
		t.Errorf("the runbook of a refresh: %v", err)
	}
}

func mustPlannedHop(t *testing.T) osplan.HopSpec {
	t.Helper()
	hop, ok := osplan.PlannedHop(osreadiness.HopDebian11to12)
	if !ok {
		t.Fatal("no hop")
	}
	return hop
}

// The word of the owner about a change of release information is in the update of R1 when it was said, and the rewrite that the unit is made from is the refresh's.
func TestARefreshHasTheOptionThatTheOwnerAcceptedAndOnlyThen(t *testing.T) {
	for _, accept := range []bool{false, true} {
		box := newUpgradeBox(t)
		box.aMachineThatCanRefresh()
		status, err := box.StartSystemOSUpgradeRefresh(OSUpgradeRefreshRequest{AcceptReleaseInfoChange: accept})
		if err != nil {
			t.Fatal(err)
		}
		run := box.read(status.RunID)
		text := readText(t, filepath.Join(run.Dir, "blocks", "rsources.sh"))
		if has := strings.Contains(text, "--allow-releaseinfo-change"); has != accept {
			t.Errorf("accepted %v: the option is in the update: %v", accept, has)
		}
		var record osUpgradeRefreshRecord
		if err := json.Unmarshal([]byte(readText(t, filepath.Join(run.Dir, "plan.json"))), &record); err != nil || record.AcceptReleaseInfo != accept {
			t.Errorf("accepted %v: plan.json is %+v, %v", accept, record, err)
		}
	}
}

func TestEveryRefusalOfTheRefreshHasItsOwnCodeAndChangesNothing(t *testing.T) {
	type arrange func(b *upgradeBox, st *startStubs)
	failure := func(code string) arrange {
		return func(_ *upgradeBox, st *startStubs) {
			st.plannable = func() *osplan.Failure {
				return &osplan.Failure{Code: code, Detail: "said by osplan", Data: map[string]any{"state": "x"}}
			}
		}
	}
	refuses := func(codes ...string) arrange {
		return func(_ *upgradeBox, st *startStubs) {
			st.readiness = func(context.Context, osreadiness.Probe) osreadiness.Report {
				var checks []osreadiness.Finding
				for _, code := range codes {
					checks = append(checks, osreadiness.Finding{Code: code, Severity: osreadiness.Refuse, Action: osreadiness.ActionFinishDpkg})
				}
				return osreadiness.Report{State: osreadiness.StateBlocked, Checks: checks}
			}
		}
	}
	cases := []struct {
		code    string
		arrange arrange
		data    func(t *testing.T, d map[string]any)
	}{
		{OSUpgradeBetaOff, func(b *upgradeBox, _ *startStubs) { os.Remove(osUpgradeBetaFile) }, nil},
		{osplan.CodeNotRoot, failure(osplan.CodeNotRoot), nil},
		{osplan.CodeIdentityUnsupported, failure(osplan.CodeIdentityUnsupported), nil},
		{osplan.CodeHopNotPlanned, failure(osplan.CodeHopNotPlanned), nil},
		{OSUpgradeRunOpen, func(b *upgradeBox, st *startStubs) {
			if _, err := b.StartSystemOSUpgradeRefresh(OSUpgradeRefreshRequest{}); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, d map[string]any) {
			if d["state"] != osrun.StateRefreshing || !osrun.ValidUUID(d["run_id"].(string)) {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradeManualRunFound, func(b *upgradeBox, _ *startStubs) {
			if err := os.MkdirAll(filepath.Join(b.backups, "casaos-os-upgrade-p1-1111", "apt-orig"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, d map[string]any) {
			if d["folder"] != "casaos-os-upgrade-p1-1111" {
				t.Errorf("data = %v", d)
			}
		}},
		// the checks of the report that a refresh stands on, each as a refusal of its own, and the one that it does not
		{OSUpgradeReadinessRefuses, refuses("broken_dpkg"), func(t *testing.T, d map[string]any) {
			if checks, _ := d["checks"].([]map[string]any); len(checks) != 1 || checks[0]["code"] != "broken_dpkg" {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradeReadinessRefuses, refuses("held"), nil},
		{OSUpgradeReadinessRefuses, refuses("disk_boot"), nil},
		{OSUpgradeReadinessRefuses, refuses("sources_alias"), nil},
		{OSUpgradeReadinessRefuses, refuses("sources_third_party"), nil},
		{OSUpgradeReadinessRefuses, refuses("apps"), nil},
		{OSUpgradeBusy, func(b *upgradeBox, _ *startStubs) { b.updater.dpkgLocked = func() bool { return true } }, nil},
		{OSUpgradeAppsBusy, func(b *upgradeBox, _ *startStubs) {
			b.setApps(func(context.Context) ([]string, error) { return []string{"photos"}, nil })
		}, func(t *testing.T, d map[string]any) {
			if !reflect.DeepEqual(asStrings(t, d["apps"]), []string{"photos"}) {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradeNoRoom, func(b *upgradeBox, _ *startStubs) {
			b.updater.freeBytes = func(path string) (uint64, error) {
				if path == osUpgradeBackups {
					return 100 << 20, nil
				}
				return 50 << 30, nil
			}
		}, func(t *testing.T, d map[string]any) {
			if d["path"] != osUpgradeBackups || d["free"] != uint64(100<<20) {
				t.Errorf("data = %v", d)
			}
		}},
		// the sources that the report could not tell, in the plan's own words, with the file and the line
		{OSUpgradeSourceUnrecognized, func(_ *upgradeBox, st *startStubs) {
			st.rewritten = func(osplan.HopSpec) (*osplan.Rewritten, error) {
				return nil, &osplan.Failure{Code: osplan.CodeSourcesFormat, Detail: "sources.list line 3: run apt-get update first", Data: map[string]any{"file": "sources.list", "line": 3, "reason": "undecided", "secret": "x"}}
			}
		}, func(t *testing.T, d map[string]any) {
			if d["file"] != "sources.list" || d["line"] != 3 || d["reason"] != "undecided" || d["code"] != osplan.CodeSourcesFormat || d["secret"] != nil {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradeRunbookUnsafe, func(_ *upgradeBox, st *startStubs) {
			st.rewritten = func(hop osplan.HopSpec) (*osplan.Rewritten, error) {
				r, _ := theSourcesOfARefresh(hop)
				r.Tree = []osplan.TreeFile{{Path: "sources.list", Content: "deb http://x bullseye main\n# CASAOS_EOF\n"}}
				return r, nil
			}
		}, nil},
		{OSUpgradeRunbookTooLarge, func(_ *upgradeBox, st *startStubs) {
			st.rewritten = func(hop osplan.HopSpec) (*osplan.Rewritten, error) {
				r, _ := theSourcesOfARefresh(hop)
				r.Tree = []osplan.TreeFile{{Path: "sources.list", Content: strings.Repeat("# a comment of the owner\n", 12000)}}
				return r, nil
			}
		}, nil},
		{OSUpgradeStartFailed, func(b *upgradeBox, _ *startStubs) {
			original := osUpgradeWriteFile
			t.Cleanup(func() { osUpgradeWriteFile = original })
			osUpgradeWriteFile = func(path string, data []byte, mode os.FileMode) error {
				if filepath.Base(path) == "bk-path" {
					return errors.New("no room")
				}
				return original(path, data, mode)
			}
		}, func(t *testing.T, d map[string]any) {
			if _, ok := d["detail"].(string); !ok {
				t.Errorf("data = %v", d)
			}
		}},
	}
	seen := map[string]bool{}
	for i, c := range cases {
		t.Run(c.code+"/"+itoa(i), func(t *testing.T) {
			box := newUpgradeBox(t)
			st := box.aMachineThatCanRefresh()
			c.arrange(box, st)
			before := box.runFolders()
			startedBefore := len(box.startedUnits())
			_, err := box.StartSystemOSUpgradeRefresh(OSUpgradeRefreshRequest{})
			refusal := refusalOf(t, err)
			seen[c.code] = true
			if refusal.Code != c.code || refusal.Detail == "" || refusal.Data == nil {
				t.Fatalf("the refusal is %+v, want %s", refusal, c.code)
			}
			if c.data != nil {
				c.data(t, refusal.Data)
			}
			if c.code == OSUpgradeRunOpen {
				if got := box.runFolders(); len(got) != 1 || len(box.startedUnits()) != 1 {
					t.Errorf("the folders are %v, %d units", got, len(box.startedUnits()))
				}
				return
			}
			box.assertNothingChanged(before)
			if len(box.startedUnits()) != startedBefore {
				t.Error("a unit was started")
			}
			if busy, _ := osUpgradeFlagOpen(); busy {
				t.Error("the flag of an open upgrade is there after a refusal")
			}
			if id, state := currentOSUpgrade(); state != osUpgradeNoRun {
				t.Errorf("a run is the latest after a refusal: %q", id)
			}
		})
	}
	for _, code := range OSUpgradeRefreshCodes {
		if code != OSUpgradeBadRequest && !seen[code] {
			t.Errorf("the code %s has no test: it is in OSUpgradeRefreshCodes", code)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// A rewrite that fails for a reason of the machine's (a file that cannot be read, the time that ran out) is no word about the sources: it is a start that broke, and nothing was changed.
func TestASourceThatCannotBeReadIsAStartThatBrokeAndNotASourceThatIsNotRecognised(t *testing.T) {
	box := newUpgradeBox(t)
	st := box.aMachineThatCanRefresh()
	st.rewritten = func(osplan.HopSpec) (*osplan.Rewritten, error) {
		return nil, errors.New("open /etc/apt/sources.list: permission denied")
	}
	before := box.runFolders()
	_, err := box.StartSystemOSUpgradeRefresh(OSUpgradeRefreshRequest{})
	refusal := refusalOf(t, err)
	if refusal.Code != OSUpgradeStartFailed || strings.Contains(refusal.Detail, "permission") || strings.Contains(refusal.Detail, "/etc/apt") {
		t.Errorf("the refusal is %+v", refusal)
	}
	box.assertNothingChanged(before)
}

// What a refresh stands on from the report is a fixed table, and the checks that are the hop's alone do not stop it: a restart that waits, the boot loader, the file systems and
// the age of the backup are not in the way of bringing the old release up to date.
func TestTheChecksOfTheReportThatARefreshDoesNotStandOnDoNotStopIt(t *testing.T) {
	for _, code := range []string{"reboot_first", "bootloader", "fstab_unresolved", "backup_age", "snapshot_reminder", "containers_policy", "root_luks", "unproven_arch", "foreign_arch"} {
		box := newUpgradeBox(t)
		st := box.aMachineThatCanRefresh()
		st.readiness = func(context.Context, osreadiness.Probe) osreadiness.Report {
			return osreadiness.Report{State: osreadiness.StateBlocked, Checks: []osreadiness.Finding{{Code: code, Severity: osreadiness.Refuse}}}
		}
		if _, err := box.StartSystemOSUpgradeRefresh(OSUpgradeRefreshRequest{}); err != nil {
			t.Errorf("%s: %v", code, err)
		}
	}
	for _, code := range osRefreshChecks {
		if !osRefreshStands(code) {
			t.Errorf("%s is in the table and is not a stand", code)
		}
	}
	for _, code := range []string{"sources_alias", "sources_format", "sources_cdrom", "sources_third_party", "sources_pin"} {
		if !osRefreshStands(code) {
			t.Errorf("%s: every check of the sources stands", code)
		}
	}
	if osRefreshStands("reboot_first") || osRefreshStands("sourcesx") || osRefreshStands("") {
		t.Error("a code that is not the table's, or the sources', stands")
	}
	// a refusal of the report that is not a refusal (a warning) is nothing, whatever the check
	box := newUpgradeBox(t)
	st := box.aMachineThatCanRefresh()
	st.readiness = func(context.Context, osreadiness.Probe) osreadiness.Report {
		return osreadiness.Report{State: osreadiness.StateAttention, Checks: []osreadiness.Finding{{Code: "held", Severity: osreadiness.Warn}}}
	}
	if _, err := box.StartSystemOSUpgradeRefresh(OSUpgradeRefreshRequest{}); err != nil {
		t.Errorf("a warning of a check that a refresh stands on: %v", err)
	}
}

// The checks are made cheapest first, and the first that fails is the answer.
func TestTheChecksOfTheRefreshAreMadeInTheirOrder(t *testing.T) {
	order := []struct {
		code    string
		arrange func(b *upgradeBox, st *startStubs)
	}{
		{OSUpgradeBetaOff, func(b *upgradeBox, _ *startStubs) { os.Remove(osUpgradeBetaFile) }},
		{osplan.CodeNotRoot, func(_ *upgradeBox, st *startStubs) {
			st.plannable = func() *osplan.Failure { return &osplan.Failure{Code: osplan.CodeNotRoot, Detail: "x"} }
		}},
		{OSUpgradeManualRunFound, func(b *upgradeBox, _ *startStubs) {
			os.MkdirAll(filepath.Join(b.backups, "casaos-os-upgrade-p1-1111", "begun-upgrade"), 0o700)
		}},
		{OSUpgradeReadinessRefuses, func(_ *upgradeBox, st *startStubs) {
			st.readiness = func(context.Context, osreadiness.Probe) osreadiness.Report {
				return osreadiness.Report{State: osreadiness.StateBlocked, Checks: []osreadiness.Finding{{Code: "held", Severity: osreadiness.Refuse}}}
			}
		}},
		{OSUpgradeBusy, func(b *upgradeBox, _ *startStubs) { b.updater.dpkgLocked = func() bool { return true } }},
		{OSUpgradeAppsBusy, func(b *upgradeBox, _ *startStubs) {
			b.setApps(func(context.Context) ([]string, error) { return nil, errors.New("AppManagement does not answer") })
		}},
		{OSUpgradeNoRoom, func(b *upgradeBox, _ *startStubs) {
			b.updater.freeBytes = func(string) (uint64, error) { return 1 << 20, nil }
		}},
		{OSUpgradeSourceUnrecognized, func(_ *upgradeBox, st *startStubs) {
			st.rewritten = func(osplan.HopSpec) (*osplan.Rewritten, error) {
				return nil, &osplan.Failure{Code: osplan.CodeSourcesFormat, Detail: "sources.list line 2: run apt-get update first", Data: map[string]any{"file": "sources.list", "line": 2, "reason": "undecided"}}
			}
		}},
	}
	for i, c := range order {
		t.Run(c.code, func(t *testing.T) {
			box := newUpgradeBox(t)
			st := box.aMachineThatCanRefresh()
			for j := len(order) - 1; j >= i; j-- {
				order[j].arrange(box, st)
			}
			_, err := box.StartSystemOSUpgradeRefresh(OSUpgradeRefreshRequest{})
			if got := refusalOf(t, err).Code; got != c.code {
				t.Errorf("the refusal is %s, want %s", got, c.code)
			}
		})
	}
}

// A refresh that ended, was undone or is waiting for its restart is a run that the next start takes the place of; one that is open is not.
func TestARefreshThatEndedIsClosedByTheStartThatFollowsAndAnOpenOneKeepsItAway(t *testing.T) {
	box := newUpgradeBox(t)
	box.aMachineThatCanRefresh()
	first, err := box.StartSystemOSUpgradeRefresh(OSUpgradeRefreshRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.StartSystemOSUpgradeRefresh(OSUpgradeRefreshRequest{}); refusalOf(t, err).Code != OSUpgradeRunOpen {
		t.Errorf("a second refresh while the first runs: %v", err)
	}
	// the unit says that it is done (and that the machine does not want a restart): the state is refreshed, which does not keep a new one away
	run := box.read(first.RunID)
	run = box.say(run, osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRefresh, Boot: boxBoot1, PID: 101}, osrun.Marker{Kind: osrun.MkRefreshDone, N: 1, Result: "no", Why: "none"})
	box.end(run, 1, "ok")
	box.setUnit("inactive")
	if state := box.GetSystemOSUpgradeStatus().State; state != osrun.StateRefreshed {
		t.Fatalf("the state is %s", state)
	}
	second, err := box.StartSystemOSUpgradeRefresh(OSUpgradeRefreshRequest{})
	if err != nil {
		t.Fatalf("a refresh after a refresh that ended: %v", err)
	}
	if second.RunID == first.RunID || len(box.startedUnits()) != 2 {
		t.Errorf("the second is %q (the first %q), %d units", second.RunID, first.RunID, len(box.startedUnits()))
	}
}
