package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// ---- the start that goes through ---------------------------------------------------------------------------------------

func TestAStartThatTheChecksLetThroughWritesTheRunAndStartsItsUnit(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	box.setBoot(boxBoot1)

	status, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if err != nil {
		t.Fatal(err)
	}
	runID := status.RunID
	if !osrun.ValidUUID(runID) || status.State != osrun.StateRunning || !status.Busy || status.Kind != osrun.RunHop || status.Phase != osrun.PhaseRun || status.Attempt != 1 || !status.BetaEnabled {
		t.Fatalf("the status is %+v", status)
	}
	run := box.read(runID)
	if got, _ := currentOSUpgrade(); got != runID {
		t.Errorf("the latest run is %q", got)
	}
	if busy, err := osUpgradeFlagOpen(); !busy || err != nil {
		t.Errorf("the flag of the open upgrade is %v, %v", busy, err)
	}
	var flag osUpgradeBusyFlag
	if err := json.Unmarshal([]byte(readText(t, osUpgradeBusyFile)), &flag); err != nil || flag.RunID != runID || flag.State != osrun.StateRunning {
		t.Errorf("the flag is %+v, %v", flag, err)
	}

	// the log: QUEUED, RUN with the boot, ATTEMPT 1 launched by the owner, and nothing the unit has to say yet
	if len(run.Markers) != 3 || run.Ignored != 0 {
		t.Fatalf("the log has %+v (%d ignored)", run.Markers, run.Ignored)
	}
	queued, rm, attempt := run.Markers[0], run.Markers[1], run.Markers[2]
	if queued.Kind != osrun.MkQueued || rm.Kind != osrun.MkRun || rm.RunID != runID || rm.Hop != "debian-11-12" || rm.PlanID != plan.PlanID || rm.RunKind != osrun.RunHop || rm.Boot != boxBoot1 {
		t.Errorf("QUEUED and RUN are %+v and %+v", queued, rm)
	}
	if attempt.Kind != osrun.MkAttempt || attempt.N != 1 || attempt.Phase != osrun.PhaseRun || attempt.Who != osrun.WhoOwner || attempt.From != "" {
		t.Errorf("the attempt is %+v", attempt)
	}
	if !osrun.ValidNonce(run.Nonce) {
		t.Errorf("the nonce is %q", run.Nonce)
	}

	// the unit
	started := box.startedUnits()
	if len(started) != 1 {
		t.Fatalf("%d units were started", len(started))
	}
	want, err := osUpgradeUnitArgs(osrun.Attempt{RunID: runID, Nonce: run.Nonce, Phase: osrun.PhaseRun, N: 1}, osUpgradeUnitRoot+"/"+runID)
	if err != nil || !reflect.DeepEqual(started[0][2:], want) {
		t.Errorf("the unit was started with %q", started[0][2:])
	}

	// the run folder is the one that the plan is written as for the unit and the owner, with the owner's words of the plan, and the name of this machine
	auto, err := osplan.BuildAuto(*plan, osplan.AutoParams{RunID: runID, Hostname: "box1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, blk := range auto.Blocks {
		if got := readText(t, filepath.Join(run.Dir, "blocks", blk.ID+".sh")); got != blk.Text {
			t.Errorf("blocks/%s.sh is not the text that the plan makes for this run", blk.ID)
		}
	}
	if run.Meta.Kind != osrun.RunHop || run.Meta.SafetyNet != "snapshot" || run.Meta.PlanID != plan.PlanID || len(run.Meta.Blocks) != 10 || run.Meta.Blocks[0].ID != "verify" || run.Meta.Blocks[0].Title == "" {
		t.Errorf("run.json is %+v", run.Meta)
	}
	var facts map[string]any
	if err := json.Unmarshal([]byte(readText(t, filepath.Join(run.Dir, "facts-before.json"))), &facts); err != nil || facts["taken"] != true || facts["kernel"] != "5.10.0-28-amd64" {
		t.Errorf("facts-before.json is %v, %v", facts, err)
	}
	var stored osplan.Plan
	if err := json.Unmarshal([]byte(readText(t, filepath.Join(run.Dir, "plan.json"))), &stored); err != nil || stored.PlanID != plan.PlanID || len(stored.Sources.Tree) != 1 {
		t.Errorf("plan.json is %+v, %v: it has the plan with its tree", stored.PlanID, err)
	}
	if got := readText(t, filepath.Join(run.Dir, "lists", "1.list")); got != "verify\ntimers\nsnapshot\nsources\ndownload\nminimal\nfull\nprereboot\n" {
		t.Errorf("the list is %q", got)
	}
	if err := verifyOSUpgradeRun(run.Dir, osrun.RunHop); err != nil {
		t.Errorf("the folder does not pass its sums: %v", err)
	}
}

func TestARehearsalIsARunOfItsOwnKindThatStopsBeforeThePointOfNoReturn(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	req := box.aGoodRequest(plan)
	// a rehearsal goes back: it needs neither a safety net nor the word that there is no way back
	req.Rehearsal, req.SafetyNet, req.Acks = true, "", []string{"no_interruption"}
	status, err := box.StartSystemOSUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	if status.Kind != osrun.RunRehearsal || status.Phase != osrun.PhaseRehearse || status.State != osrun.StateRehearsing || !status.Busy {
		t.Fatalf("the status is %+v", status)
	}
	run := box.read(status.RunID)
	if got := readText(t, filepath.Join(run.Dir, "lists", "1.list")); got != "verify\ntimers\nsnapshot\nsources\ndownload\n" {
		t.Errorf("the list of the rehearsal is %q", got)
	}
	if run.Markers[1].RunKind != osrun.RunRehearsal || run.Markers[2].Phase != osrun.PhaseRehearse {
		t.Errorf("the log is %+v", run.Markers)
	}
	var flag osUpgradeBusyFlag
	if err := json.Unmarshal([]byte(readText(t, osUpgradeBusyFile)), &flag); err != nil || flag.State != osrun.StateRehearsing {
		t.Errorf("the flag is %+v, %v", flag, err)
	}
	// ... but it is still held to what it is: a safety net that is not one is refused, and no_interruption is still asked
	box2 := newUpgradeBox(t)
	_, plan2 := box2.aMachineThatCanStart()
	req2 := box2.aGoodRequest(plan2)
	req2.Rehearsal, req2.SafetyNet = true, "a good feeling"
	if _, err := box2.StartSystemOSUpgrade(req2); refusalOf(t, err).Code != OSUpgradeBadSafetyNet {
		t.Errorf("a rehearsal with a safety net that is not one: %v", err)
	}
	req2.SafetyNet, req2.Acks = "", nil
	if _, err := box2.StartSystemOSUpgrade(req2); refusalOf(t, err).Code != OSUpgradeAcksMissing {
		t.Errorf("a rehearsal with no acknowledgement: %v", err)
	}
}

func TestAStartThatTheMachineAcceptsUsesTheRemovalsOfThePlanAndNotTheOwnersText(t *testing.T) {
	box := newUpgradeBox(t)
	box.switchOn()
	box.stubStart()
	plan := box.aPlanWithThingsToAccept()
	req := box.aGoodRequest(plan)
	req.AcceptRemovals = []string{"fuse", "fuse"} // the same name twice is the same name
	status, err := box.StartSystemOSUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	block := readText(t, filepath.Join(box.root, status.RunID, "blocks", "sources.sh"))
	if !strings.Contains(block, "ACCEPT_REMOVALS='fuse'") {
		t.Errorf("the block does not carry the removal of the plan:\n%s", cutText(block, 400))
	}
	if !strings.Contains(readText(t, filepath.Join(box.root, status.RunID, "blocks", "verify.sh")), "TYPED_HOSTNAME='box1'") {
		t.Error("the block that checks the machine does not carry the name that was typed")
	}
}

// ---- every refusal ----------------------------------------------------------------------------------------------------

// A refusal changes nothing: no folder, no flag, no pointer, no unit, no line of any log.
func (b *upgradeBox) assertNothingChanged(before []string) {
	b.t.Helper()
	if now := b.runFolders(); !reflect.DeepEqual(now, before) {
		b.t.Errorf("the folders of the runs were %v and are %v", before, now)
	}
	if n := len(b.startedUnits()); n != 0 {
		b.t.Errorf("%d units were started", n)
	}
}

type startRefusalCase struct {
	code    string
	arrange func(b *upgradeBox, st *startStubs, req *OSUpgradeStartRequest)
	data    func(t *testing.T, data map[string]any)
}

func asStrings(t *testing.T, v any) []string {
	t.Helper()
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		var out []string
		for _, item := range list {
			out = append(out, item.(string))
		}
		return out
	}
	t.Fatalf("%#v is not a list of names", v)
	return nil
}

func TestEveryRefusalOfTheStartHasItsOwnCodeAndChangesNothing(t *testing.T) {
	failure := func(code string) func(*upgradeBox, *startStubs, *OSUpgradeStartRequest) {
		return func(_ *upgradeBox, st *startStubs, _ *OSUpgradeStartRequest) {
			st.plannable = func() *osplan.Failure {
				return &osplan.Failure{Code: code, Detail: "said by osplan", Data: map[string]any{"state": "x"}}
			}
		}
	}
	cases := []startRefusalCase{
		{OSUpgradeBadRequest, func(_ *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) { req.PlanID = "plan" }, func(t *testing.T, d map[string]any) {
			if d["field"] != "plan_id" {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradeBetaOff, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) { os.Remove(osUpgradeBetaFile) }, nil},
		{osplan.CodeNotRoot, failure(osplan.CodeNotRoot), func(t *testing.T, d map[string]any) {
			if d["state"] != "x" {
				t.Errorf("data = %v", d)
			}
		}},
		{osplan.CodeIdentityUnsupported, failure(osplan.CodeIdentityUnsupported), nil},
		{osplan.CodeHopNotPlanned, failure(osplan.CodeHopNotPlanned), nil},
		{OSUpgradeRunOpen, func(b *upgradeBox, st *startStubs, req *OSUpgradeStartRequest) {
			if _, err := b.StartSystemOSUpgrade(*req); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, d map[string]any) {
			if d["state"] != osrun.StateRunning || !osrun.ValidUUID(d["run_id"].(string)) {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradeManualRunFound, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) {
			if err := os.MkdirAll(filepath.Join(b.backups, "casaos-os-upgrade-p1-1111", "apt-orig"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, d map[string]any) {
			if d["folder"] != "casaos-os-upgrade-p1-1111" {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradeNoPlan, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) {
			b.osPlan.state.Store(&osPlanState{state: osPlanRunning})
		}, func(t *testing.T, d map[string]any) {
			if d["reason"] != "running" {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradePlanChanged, func(b *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) {
			req.PlanID = "p1-" + strings.Repeat("0", 32)
		}, func(t *testing.T, d map[string]any) {
			if d["current"] == nil || d["current"] == "" {
				t.Errorf("data = %v: it says which plan is the live one", d)
			}
		}},
		{OSUpgradePlanBlocked, func(b *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) {
			req.PlanID = b.anUpgradePlan(osplan.VerdictBlocked, nil).PlanID
		}, func(t *testing.T, d map[string]any) {
			if problems, ok := d["problems"].([]osplan.Problem); !ok || len(problems) != 1 || problems[0].Code != osplan.ProblemDistKeptBack {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradePlanOlderThanRefresh, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) {
			b.putInRoot("last-refresh", b.now().Add(time.Minute).UTC().Format(time.RFC3339)+"\n")
		}, nil},
		{OSUpgradeBadSafetyNet, func(_ *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) { req.SafetyNet = "good luck" }, nil},
		{OSUpgradeHostnameMismatch, func(_ *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) { req.Hostname = "otherbox" }, nil},
		{OSUpgradeAcksMissing, func(_ *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) { req.Acks = []string{"no_interruption"} }, func(t *testing.T, d map[string]any) {
			if !reflect.DeepEqual(asStrings(t, d["missing"]), []string{"no_way_back"}) {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradeRemovalsMismatch, func(b *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) {
			plan := b.aPlanWithThingsToAccept()
			*req = b.aGoodRequest(plan)
			req.AcceptRemovals = nil
		}, func(t *testing.T, d map[string]any) {
			if !reflect.DeepEqual(asStrings(t, d["missing"]), []string{"fuse"}) || len(asStrings(t, d["extra"])) != 0 {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradePausedMismatch, func(b *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) {
			plan := b.aPlanWithThingsToAccept()
			*req = b.aGoodRequest(plan)
			req.PausedSources = []string{"docker.list", "other.list"}
		}, func(t *testing.T, d map[string]any) {
			if !reflect.DeepEqual(asStrings(t, d["missing"]), []string{"sources.list:12"}) || !reflect.DeepEqual(asStrings(t, d["extra"]), []string{"other.list"}) {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradeSourcesChanged, func(_ *upgradeBox, st *startStubs, _ *OSUpgradeStartRequest) {
			st.rewritten = func(osplan.HopSpec) (*osplan.Rewritten, error) {
				return &osplan.Rewritten{HashCurrent: strings.Repeat("c", 64), HashGenerated: strings.Repeat("b", 64)}, nil
			}
		}, nil},
		{OSUpgradeReadinessRefuses, func(_ *upgradeBox, st *startStubs, _ *OSUpgradeStartRequest) {
			st.readiness = func(context.Context, osreadiness.Probe) osreadiness.Report {
				return osreadiness.Report{State: osreadiness.StateBlocked, Checks: []osreadiness.Finding{
					{Code: "disk_root", Severity: osreadiness.Warn}, {Code: "broken_dpkg", Severity: osreadiness.Refuse, Action: osreadiness.ActionFinishDpkg},
				}}
			}
		}, func(t *testing.T, d map[string]any) {
			checks, _ := d["checks"].([]map[string]any)
			if len(checks) != 1 || checks[0]["code"] != "broken_dpkg" || checks[0]["action"] != "finish_dpkg" {
				t.Errorf("data = %v: only what the report refuses is in it", d)
			}
		}},
		{OSUpgradeBusy, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) {
			b.updater.dpkgLocked = func() bool { return true }
		}, func(t *testing.T, d map[string]any) {
			if reason, _ := d["reason"].(string); !strings.Contains(reason, "dpkg") {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradeAppsBusy, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) {
			b.setApps(func(context.Context) ([]string, error) { return []string{"photos"}, nil })
		}, func(t *testing.T, d map[string]any) {
			if !reflect.DeepEqual(asStrings(t, d["apps"]), []string{"photos"}) {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradeNoRoom, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) {
			b.updater.freeBytes = func(path string) (uint64, error) {
				if path == osUpgradeBackups {
					return 100 << 20, nil
				}
				return 50 << 30, nil
			}
		}, func(t *testing.T, d map[string]any) {
			if d["need"] != uint64(144<<20) || d["free"] != uint64(100<<20) || d["path"] != osUpgradeBackups {
				t.Errorf("data = %v", d)
			}
		}},
		{OSUpgradeRunbookUnsafe, func(b *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) {
			plan := b.anUpgradePlan(osplan.VerdictOK, func(p *osplan.Plan) {
				p.Sources.Tree = []osplan.TreeFile{{Path: "sources.list", Content: "deb http://x bookworm main\n# CASAOS_EOF\n"}}
			})
			*req = b.aGoodRequest(plan)
		}, nil},
		{OSUpgradeRunbookTooLarge, func(b *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) {
			plan := b.anUpgradePlan(osplan.VerdictOK, func(p *osplan.Plan) {
				p.Sources.Tree = []osplan.TreeFile{{Path: "sources.list", Content: strings.Repeat("# a comment of the owner\n", 12000)}}
			})
			*req = b.aGoodRequest(plan)
		}, nil},
		{OSUpgradeStartFailed, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) {
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
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			box := newUpgradeBox(t)
			st, plan := box.aMachineThatCanStart()
			req := box.aGoodRequest(plan)
			c.arrange(box, st, &req)
			before := box.runFolders()
			startedBefore := len(box.startedUnits())
			_, err := box.StartSystemOSUpgrade(req)
			refusal := refusalOf(t, err)
			seen[c.code] = true
			if refusal.Code != c.code || refusal.Detail == "" || refusal.Data == nil {
				t.Fatalf("the refusal is %+v, want %s", refusal, c.code)
			}
			if c.data != nil {
				c.data(t, refusal.Data)
			}
			if c.code == OSUpgradeRunOpen {
				// the run that was open is as it was
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
	for _, code := range OSUpgradeStartCodes {
		if !seen[code] {
			t.Errorf("the code %s has no test: it is in OSUpgradeStartCodes", code)
		}
	}
	if len(seen) != len(OSUpgradeStartCodes) {
		t.Errorf("%d codes were tested, %d are listed", len(seen), len(OSUpgradeStartCodes))
	}
}

// The checks are made cheapest first, and the first that fails is the answer.
func TestTheChecksOfTheStartAreMadeInTheirOrder(t *testing.T) {
	order := []struct {
		code    string
		arrange func(b *upgradeBox, st *startStubs, req *OSUpgradeStartRequest)
	}{
		{OSUpgradeBadRequest, func(_ *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) { req.Hostname = "not a name!" }},
		{OSUpgradeBetaOff, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) { os.Remove(osUpgradeBetaFile) }},
		{osplan.CodeNotRoot, func(_ *upgradeBox, st *startStubs, _ *OSUpgradeStartRequest) {
			st.plannable = func() *osplan.Failure { return &osplan.Failure{Code: osplan.CodeNotRoot, Detail: "x"} }
		}},
		{OSUpgradeManualRunFound, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) {
			os.MkdirAll(filepath.Join(b.backups, "casaos-os-upgrade-p1-1111", "begun-upgrade"), 0o700)
		}},
		{OSUpgradeNoPlan, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) { b.osPlan.state.Store(nil) }},
		{OSUpgradePlanChanged, func(_ *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) {
			req.PlanID = "p1-" + strings.Repeat("1", 32)
		}},
		{OSUpgradePlanOlderThanRefresh, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) {
			b.putInRoot("last-refresh", b.now().Add(time.Hour).UTC().Format(time.RFC3339))
		}},
		{OSUpgradeBadSafetyNet, func(_ *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) { req.SafetyNet = "" }},
		{OSUpgradeHostnameMismatch, func(_ *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) { req.Hostname = "otherbox" }},
		{OSUpgradeAcksMissing, func(_ *upgradeBox, _ *startStubs, req *OSUpgradeStartRequest) { req.Acks = nil }},
		{OSUpgradeSourcesChanged, func(_ *upgradeBox, st *startStubs, _ *OSUpgradeStartRequest) {
			st.rewritten = func(osplan.HopSpec) (*osplan.Rewritten, error) { return nil, errors.New("no sources") }
		}},
		{OSUpgradeReadinessRefuses, func(_ *upgradeBox, st *startStubs, _ *OSUpgradeStartRequest) {
			st.readiness = func(context.Context, osreadiness.Probe) osreadiness.Report {
				return osreadiness.Report{State: osreadiness.StateBlocked, Checks: []osreadiness.Finding{{Code: "held", Severity: osreadiness.Refuse}}}
			}
		}},
		{OSUpgradeBusy, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) {
			b.updater.dpkgLocked = func() bool { return true }
		}},
		{OSUpgradeAppsBusy, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) {
			b.setApps(func(context.Context) ([]string, error) { return nil, errors.New("AppManagement does not answer") })
		}},
		{OSUpgradeNoRoom, func(b *upgradeBox, _ *startStubs, _ *OSUpgradeStartRequest) {
			b.updater.freeBytes = func(string) (uint64, error) { return 1 << 20, nil }
		}},
	}
	// every check is the one that speaks while every check after it is also failing
	for i, c := range order {
		t.Run(c.code, func(t *testing.T) {
			box := newUpgradeBox(t)
			st, plan := box.aMachineThatCanStart()
			req := box.aGoodRequest(plan)
			// the ones that come later are arranged first, so that what an earlier one sets is the last word on a field that both set
			for j := len(order) - 1; j >= i; j-- {
				order[j].arrange(box, st, &req)
			}
			_, err := box.StartSystemOSUpgrade(req)
			if got := refusalOf(t, err).Code; got != c.code {
				t.Errorf("the refusal is %s, want %s", got, c.code)
			}
		})
	}
}

func TestTheFieldsOfARequestAreHeldToTheirShapeBeforeAnythingIsLookedAt(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	for field, damage := range map[string]func(*OSUpgradeStartRequest){
		"plan_id":         func(r *OSUpgradeStartRequest) { r.PlanID = "p1-" + strings.Repeat("G", 32) },
		"hostname":        func(r *OSUpgradeStartRequest) { r.Hostname = "box 1" },
		"accept_removals": func(r *OSUpgradeStartRequest) { r.AcceptRemovals = []string{"Fuse; reboot"} },
		"paused_sources":  func(r *OSUpgradeStartRequest) { r.PausedSources = []string{"../docker.list"} },
		"acks":            func(r *OSUpgradeStartRequest) { r.Acks = []string{"no_interruption", "yes"} },
	} {
		req := box.aGoodRequest(plan)
		damage(&req)
		_, err := box.StartSystemOSUpgrade(req)
		if refusal := refusalOf(t, err); refusal.Code != OSUpgradeBadRequest || refusal.Data["field"] != field {
			t.Errorf("%s: %+v", field, refusal)
		}
	}
	long := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "pkg"
		}
		return out
	}
	req := box.aGoodRequest(plan)
	req.AcceptRemovals = long(201)
	if _, err := box.StartSystemOSUpgrade(req); refusalOf(t, err).Data["field"] != "accept_removals" {
		t.Errorf("201 removals: %v", err)
	}
	req = box.aGoodRequest(plan)
	req.PausedSources = long(201)
	if _, err := box.StartSystemOSUpgrade(req); refusalOf(t, err).Data["field"] != "paused_sources" {
		t.Errorf("201 sources: %v", err)
	}
	for _, name := range []string{"sources.list:12", "docker.list", "a_b-c.d.list", "sources.list:999999"} {
		if !osUpgradePausedPattern.MatchString(name) {
			t.Errorf("%s is refused", name)
		}
	}
	for _, name := range []string{"sources.list:", "x:1:2", "a b", "", "sources.list:1234567", strings.Repeat("a", 201)} {
		if osUpgradePausedPattern.MatchString(name) {
			t.Errorf("%q is let through", name)
		}
	}
}

// What the owner accepted is the plan's set: no more, no less, said in both directions.
func TestWhatTheOwnerAcceptedIsComparedAsASetWithThePlans(t *testing.T) {
	for name, c := range map[string]struct {
		removals []string
		paused   []string
		code     string
	}{
		"a subset of the removals":      {[]string{}, []string{"docker.list", "sources.list:12"}, OSUpgradeRemovalsMismatch},
		"a superset of the removals":    {[]string{"fuse", "libsemanage1"}, []string{"docker.list", "sources.list:12"}, OSUpgradeRemovalsMismatch},
		"another removal":               {[]string{"libsemanage1"}, []string{"docker.list", "sources.list:12"}, OSUpgradeRemovalsMismatch},
		"a subset of the sources":       {[]string{"fuse"}, []string{"docker.list"}, OSUpgradePausedMismatch},
		"a superset of the sources":     {[]string{"fuse"}, []string{"docker.list", "sources.list:12", "x.list"}, OSUpgradePausedMismatch},
		"the line of another file":      {[]string{"fuse"}, []string{"docker.list", "other.list:12"}, OSUpgradePausedMismatch},
		"the line of the sources moved": {[]string{"fuse"}, []string{"docker.list", "sources.list:13"}, OSUpgradePausedMismatch},
		"the whole file for a line":     {[]string{"fuse"}, []string{"docker.list", "sources.list"}, OSUpgradePausedMismatch},
		"exactly the plan's":            {[]string{"fuse"}, []string{"sources.list:12", "docker.list"}, ""},
		"the plan's with a name twice":  {[]string{"fuse", "fuse"}, []string{"docker.list", "docker.list", "sources.list:12"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			box := newUpgradeBox(t)
			box.switchOn()
			box.stubStart()
			plan := box.aPlanWithThingsToAccept()
			req := box.aGoodRequest(plan)
			req.AcceptRemovals, req.PausedSources = c.removals, c.paused
			_, err := box.StartSystemOSUpgrade(req)
			if c.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if refusalOf(t, err).Code != c.code {
				t.Errorf("err = %v, want %s", err, c.code)
			}
		})
	}
}

func TestTheAcknowledgementsThatAPlanAsksForAreTheOnesItNeeds(t *testing.T) {
	box := newUpgradeBox(t)
	box.switchOn()
	box.stubStart()
	plain := box.anUpgradePlan(osplan.VerdictOK, nil)
	rich := box.aPlanWithThingsToAccept()
	for name, c := range map[string]struct {
		plan      *osplan.Plan
		rehearsal bool
		want      []string
	}{
		"a hop of a plan that has nothing to accept":       {plain, false, []string{"no_interruption", "no_way_back"}},
		"a rehearsal of a plan that has nothing to accept": {plain, true, []string{"no_interruption"}},
		"a hop of a plan with things to accept":            {rich, false, []string{"no_interruption", "no_way_back", "sources_paused", "removals_read"}},
		"a rehearsal of a plan with things to accept":      {rich, true, []string{"no_interruption", "sources_paused", "removals_read"}},
	} {
		if got := osUpgradeRequiredAcks(c.plan, c.rehearsal); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	// every one that is asked for and missing is said
	box.osPlan.state.Store(&osPlanState{state: osPlanDone, plan: rich})
	req := box.aGoodRequest(rich)
	req.Acks = nil
	_, err := box.StartSystemOSUpgrade(req)
	refusal := refusalOf(t, err)
	if refusal.Code != OSUpgradeAcksMissing || !reflect.DeepEqual(asStrings(t, refusal.Data["missing"]), []string{"no_interruption", "no_way_back", "sources_paused", "removals_read"}) {
		t.Errorf("the refusal is %+v", refusal)
	}
}

// The name of the machine is not said back, whoever it is: not the one that was typed, not the one that the machine has.
func TestTheNameOfTheMachineIsNeverEchoed(t *testing.T) {
	box := newUpgradeBox(t)
	st, plan := box.aMachineThatCanStart()
	st.hostname = "the-real-name-of-the-box"
	req := box.aGoodRequest(plan)
	req.Hostname = "the-typed-name-of-another"
	_, err := box.StartSystemOSUpgrade(req)
	refusal := refusalOf(t, err)
	if refusal.Code != OSUpgradeHostnameMismatch {
		t.Fatalf("err = %v", err)
	}
	data, merr := json.Marshal(refusal)
	if merr != nil {
		t.Fatal(merr)
	}
	for _, name := range []string{"the-real-name-of-the-box", "the-typed-name-of-another"} {
		if strings.Contains(string(data), name) || strings.Contains(refusal.Error(), name) {
			t.Errorf("the refusal says %q: %s", name, data)
		}
	}
}

// ---- the run that was there ---------------------------------------------------------------------------------------------

func TestAStartTakesTheRunBeforeItWhenItEndedOrWasUndoneAndNotWhenItIsOpen(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	old := box.aRun(plan, osrun.RunHop)
	old = box.say(old,
		osrun.Marker{Kind: osrun.MkAttempt, N: 1, Phase: osrun.PhaseRun, Who: osrun.WhoOwner},
		osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRun, Boot: boxBoot1, PID: 1},
		osrun.Marker{Kind: osrun.MkStop, N: 1, Index: 1, ID: osrun.BlockVerify, RC: 1, Class: osrun.ClassBusy},
		osrun.Marker{Kind: osrun.MkUnwound, N: 1, Result: "ok"},
		osrun.Marker{Kind: osrun.MkUnitEnd, N: 1, Result: "stopped"})
	box.pointTo(old)
	if err := setOSUpgradeBusy(old.ID, "unwinding", box.now()); err != nil {
		t.Fatal(err)
	}
	box.advance(time.Minute)

	status, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if err != nil {
		t.Fatalf("a run that was undone keeps a new one from starting: %v", err)
	}
	if status.RunID == old.ID || status.State != osrun.StateRunning {
		t.Fatalf("the status is %+v", status)
	}
	closed := box.read(old.ID)
	if last := closed.Markers[len(closed.Markers)-1]; last.Kind != osrun.MkAck || last.Result != "acknowledge" {
		t.Errorf("the run before ends with %+v: it is closed", last)
	}
	if got, _ := currentOSUpgrade(); got != status.RunID {
		t.Errorf("the latest run is %q", got)
	}
	var flag osUpgradeBusyFlag
	if err := json.Unmarshal([]byte(readText(t, osUpgradeBusyFile)), &flag); err != nil || flag.RunID != status.RunID {
		t.Errorf("the flag is %+v, %v", flag, err)
	}
}

func TestARunThatEndedWellAndWasNotClosedKeepsANewOneFromStarting(t *testing.T) {
	for _, verdict := range []string{`{"findings":[]}`, `{"findings":[{"code":"ports_missing","severity":"warn"}]}`} {
		box := newUpgradeBox(t)
		_, plan := box.aMachineThatCanStart()
		old := box.aHopThatReachedItsRestart()
		box.setBoot(boxBoot2)
		old = box.say(old,
			osrun.Marker{Kind: osrun.MkAttempt, N: 2, Phase: osrun.PhaseVerify, Who: osrun.WhoAuto},
			osrun.Marker{Kind: osrun.MkUnitStart, N: 2, Phase: osrun.PhaseVerify, Boot: boxBoot2, PID: 2},
			osrun.Marker{Kind: osrun.MkVerifyDone, N: 2, Boot: boxBoot2},
			osrun.Marker{Kind: osrun.MkUnitEnd, N: 2, Result: "ok"})
		box.leave(old, "done-after")
		rewrite(t, filepath.Join(old.Dir, "verify.json"), verdict)
		if st := box.GetSystemOSUpgradeStatus().State; st != osrun.StateSucceeded && st != osrun.StateSucceededWithFindings {
			t.Fatalf("the run is %s", st)
		}
		_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
		if refusal := refusalOf(t, err); refusal.Code != OSUpgradeRunOpen {
			t.Errorf("the refusal is %+v", refusal)
		}
	}
}

func TestARunThatIsOpenInAnyBusyStateKeepsANewOneFromStarting(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	run := box.aHopThatReachedItsRestart()
	for name, arrange := range map[string]func(){
		"waiting for its restart":  func() {},
		"restarted, to be checked": func() { box.setBoot(boxBoot2) },
		"taken over by the owner":  func() { box.say(box.read(run.ID), osrun.Marker{Kind: osrun.MkTakeover}) },
	} {
		arrange()
		_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
		if refusal := refusalOf(t, err); refusal.Code != OSUpgradeRunOpen || refusal.Data["run_id"] != run.ID {
			t.Errorf("%s: %+v", name, refusal)
		}
	}
}

func TestARunThatCannotBeReadKeepsANewOneFromStarting(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	run := box.aRun(plan, osrun.RunHop)
	box.pointTo(run)
	remove(t, filepath.Join(run.Dir, "run.json"))
	_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeRunOpen || refusal.Data["state"] != osrun.StateNeedsAttention {
		t.Errorf("a run that cannot be read: %v", err)
	}
	rewrite(t, filepath.Join(box.root, "current"), "somebody's words\n")
	_, err = box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeRunOpen {
		t.Errorf("a pointer that says nothing: %v", err)
	}
}

func TestTheFoldersOfBackupsThatAreNotAHopsAreLookedAtOneByOne(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	mk := func(name string, files ...string) string {
		dir := filepath.Join(box.backups, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			rewrite(t, filepath.Join(dir, f), "x")
		}
		return dir
	}
	// what is not a half done hop: a folder that has neither mark, one that ended, the file of the timers, a folder of anything else, a link
	mk("casaos-os-upgrade-p1-aaaa", "done-verify")
	mk("casaos-os-upgrade-p1-bbbb", "apt-orig", "begun-upgrade", "done-after")
	rewrite(t, filepath.Join(box.backups, "casaos-os-upgrade-timers.state"), "apt-daily.timer active\n")
	mk("something-else", "apt-orig")
	if folder, err := osUpgradeManualFolder(nil); folder != "" || err != nil {
		t.Fatalf("the folder is %q, %v", folder, err)
	}
	if _, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan)); err != nil {
		t.Fatalf("a start with only what is not a half done hop in the folder of backups: %v", err)
	}
	// ... and the folder of the run that is the latest is not one either, whatever it holds
	box2 := newUpgradeBox(t)
	_, plan2 := box2.aMachineThatCanStart()
	run := box2.aRun(plan2, osrun.RunHop)
	box2.pointTo(run)
	box2.leave(run, "apt-orig", "done-timers")
	if folder, err := osUpgradeManualFolder(run); folder != "" || err != nil {
		t.Errorf("the folder of the latest run is %q, %v", folder, err)
	}
	if folder, _ := osUpgradeManualFolder(nil); folder == "" {
		t.Error("the folder of a run that is not the latest is not found")
	}
	// a folder of backups that cannot be read is not a folder with nothing in it
	rewrite(t, filepath.Join(box2.etc, "afile"), "x")
	osUpgradeBackups = filepath.Join(box2.etc, "afile")
	if _, err := osUpgradeManualFolder(nil); err == nil {
		t.Error("a folder of backups that is a file is empty")
	}
}

func TestTheTimeOfTheLastRefreshIsReadAsItIs(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	path := filepath.Join(box.root, "last-refresh")
	if err := ensureOSUpgradeRoot(); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		text string
		want bool
	}{
		"a refresh before the plan":   {plan.MadeAt.Add(-time.Hour).Format(time.RFC3339), false},
		"a refresh at the time of it": {plan.MadeAt.Format(time.RFC3339), false},
		"a refresh after the plan":    {plan.MadeAt.Add(time.Second).Format(time.RFC3339), true},
		"a time with a zone":          {plan.MadeAt.Add(time.Hour).In(time.FixedZone("x", 7200)).Format(time.RFC3339), true},
		"a time that is not one":      {"yesterday", false},
		"nothing":                     {"", false},
	} {
		rewrite(t, path, c.text+"\n")
		if got := osUpgradeNewerRefresh(plan) != nil; got != c.want {
			t.Errorf("%s: refused = %v", name, got)
		}
	}
	os.Remove(path)
	if osUpgradeNewerRefresh(plan) != nil {
		t.Error("no refresh at all keeps a start away")
	}
}

// ---- the start and the machine -------------------------------------------------------------------------------------------

// The start is not kept away by its own unit or its own flag, and is by every other: the probe says which unit is the caller's.
func TestTheStartIsNotKeptAwayByItsOwnUnitOrFlag(t *testing.T) {
	box := newUpgradeBox(t)
	box.stubStart()
	box.putInRoot("busy", "")
	box.setUnit("active")
	probe := newOSUpgradeProbe(box.updater)
	if reason, busy, err := probe.MaintenanceBusy(context.Background()); busy || err != nil {
		t.Errorf("the start is kept away by itself: %q, %v, %v", reason, busy, err)
	}
	// ... but a unit of another kind of update does
	box.setUnit("inactive")
	box.updater.command = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 1 && args[1] == systemPackageUpdateUnit {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), nil
	}
	if _, busy, _ := probe.MaintenanceBusy(context.Background()); !busy {
		t.Error("the start is not kept away by an update of packages")
	}
	// ... and the plain probe of the readiness report counts both
	box.updater.command = box.command
	box.setUnit("active")
	if _, busy, _ := (osReadinessProbe{updater: box.updater, command: probe.command}).MaintenanceBusy(context.Background()); !busy {
		t.Error("the report of the readiness is not kept away by the unit of the upgrade")
	}
}

func TestAUnitOfTheUpgradeThatRunsOrASystemdThatIsSilentRefusesTheStart(t *testing.T) {
	for name, state := range map[string]string{"a unit that runs": "active", "a systemd that does not answer": ""} {
		box := newUpgradeBox(t)
		_, plan := box.aMachineThatCanStart()
		box.setUnit(state)
		_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
		if refusal := refusalOf(t, err); refusal.Code != OSUpgradeBusy {
			t.Errorf("%s: %+v", name, refusal)
		}
		box.assertNothingChanged(nil)
	}
}

func TestTheRoomThatTheStartNeedsIsWhatTheBackupWillTake(t *testing.T) {
	for name, c := range map[string]struct {
		etc       int64
		etcErr    error
		backups   uint64
		lib       uint64
		backupsFn error
		ok        bool
		needs     uint64
	}{
		"plenty":                                {40 << 20, nil, 50 << 30, 50 << 30, nil, true, 0},
		"a small /etc, the least that is asked": {1 << 20, nil, 128 << 20, 128 << 20, nil, true, 0},
		"a small /etc, a little under":          {1 << 20, nil, 128<<20 - 1, 50 << 30, nil, false, 128 << 20},
		"a big /etc needs twice its size":       {200 << 20, nil, 2*(200<<20) + 64<<20 - 1, 50 << 30, nil, false, 2*(200<<20) + 64<<20},
		"a big /etc, exactly enough":            {200 << 20, nil, 2*(200<<20) + 64<<20, 50 << 30, nil, true, 0},
		"the folder of the runs is short":       {1 << 20, nil, 50 << 30, 127 << 20, nil, false, 128 << 20},
		"/etc cannot be counted":                {0, errors.New("walk"), 50 << 30, 50 << 30, nil, false, 0},
		"the room cannot be read":               {1 << 20, nil, 0, 0, errors.New("statfs"), false, 0},
	} {
		t.Run(name, func(t *testing.T) {
			box := newUpgradeBox(t)
			st, plan := box.aMachineThatCanStart()
			st.etcSize, st.etcErr = c.etc, c.etcErr
			box.updater.freeBytes = func(path string) (uint64, error) {
				if c.backupsFn != nil {
					return 0, c.backupsFn
				}
				if path == osUpgradeBackups {
					return c.backups, nil
				}
				return c.lib, nil
			}
			_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
			if c.ok {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			refusal := refusalOf(t, err)
			if refusal.Code != OSUpgradeNoRoom {
				t.Fatalf("err = %v", err)
			}
			if c.needs != 0 && refusal.Data["need"] != c.needs {
				t.Errorf("the room needed is %v, want %d", refusal.Data["need"], c.needs)
			}
		})
	}
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	box.updater.freeBytes = nil
	if _, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan)); refusalOf(t, err).Code != OSUpgradeNoRoom {
		t.Errorf("a machine that cannot read the room: %v", err)
	}
}

func TestTheReportIsAskedWithTheStartsOwnProbe(t *testing.T) {
	box := newUpgradeBox(t)
	st, plan := box.aMachineThatCanStart()
	var asked osReadinessProbe
	st.readiness = func(_ context.Context, p osreadiness.Probe) osreadiness.Report {
		asked, _ = p.(osReadinessProbe)
		return osreadiness.Report{State: osreadiness.StateReady, Checks: []osreadiness.Finding{}}
	}
	if _, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan)); err != nil {
		t.Fatal(err)
	}
	if asked.except != systemOSUpgradeUnit {
		t.Errorf("the report is asked with the probe of %q", asked.except)
	}
}

// ---- the plan that changes while the report is made -----------------------------------------------------------------------

func TestANewerPlanThatReplacedTheOneTheOwnerReadWhileTheReportRanIsSaid(t *testing.T) {
	box := newUpgradeBox(t)
	st, plan := box.aMachineThatCanStart()
	var newer *osplan.Plan
	st.readiness = func(context.Context, osreadiness.Probe) osreadiness.Report {
		newer = box.anUpgradePlan(osplan.VerdictOK, func(p *osplan.Plan) { p.Holds = []string{"docker-ce"} })
		return osreadiness.Report{State: osreadiness.StateReady, Checks: []osreadiness.Finding{}}
	}
	req := box.aGoodRequest(plan)
	_, err := box.StartSystemOSUpgrade(req)
	refusal := refusalOf(t, err)
	if refusal.Code != OSUpgradePlanChanged || refusal.Data["current"] != newer.PlanID || newer.PlanID == plan.PlanID {
		t.Errorf("the refusal is %+v", refusal)
	}
	box.assertNothingChanged(nil)

	// a plan that expired in the meantime is no plan
	box2 := newUpgradeBox(t)
	st2, plan2 := box2.aMachineThatCanStart()
	st2.readiness = func(context.Context, osreadiness.Probe) osreadiness.Report {
		box2.advance(osplan.PlanLifetime + time.Hour)
		return osreadiness.Report{State: osreadiness.StateReady, Checks: []osreadiness.Finding{}}
	}
	_, err = box2.StartSystemOSUpgrade(box2.aGoodRequest(plan2))
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeNoPlan || refusal.Data["reason"] != "expired" {
		t.Errorf("the refusal is %+v", refusal)
	}
}

func TestThePlanThatHasNoBlockedProblemButIsNotGoodIsNotStarted(t *testing.T) {
	box := newUpgradeBox(t)
	_, _ = box.aMachineThatCanStart()
	for name, c := range map[string]struct {
		state  *osPlanState
		reason string
	}{
		"no job at all":      {nil, "idle"},
		"a job that runs":    {&osPlanState{state: osPlanRunning}, "running"},
		"a job that failed":  {&osPlanState{state: osPlanFailed}, "failed"},
		"a job that is idle": {&osPlanState{state: osPlanIdle}, "idle"},
	} {
		box.osPlan.state.Store(c.state)
		_, err := box.StartSystemOSUpgrade(box.aGoodRequest(&osplan.Plan{PlanID: "p1-" + strings.Repeat("a", 32)}))
		if refusal := refusalOf(t, err); refusal.Code != OSUpgradeNoPlan || refusal.Data["reason"] != c.reason {
			t.Errorf("%s: %+v", name, refusal)
		}
	}
}

// ---- what a start leaves when it fails ---------------------------------------------------------------------------------

// The order of what is written: the facts first, then the files of the run with SHA256SUMS the last, then the flag, then the pointer.
func TestTheEffectsOfAStartComeInTheirOrder(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	var mu sync.Mutex
	var written []string
	original := osUpgradeWriteFile
	t.Cleanup(func() { osUpgradeWriteFile = original })
	osUpgradeWriteFile = func(path string, data []byte, mode os.FileMode) error {
		mu.Lock()
		written = append(written, filepath.Base(path))
		mu.Unlock()
		return original(path, data, mode)
	}
	if _, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan)); err != nil {
		t.Fatal(err)
	}
	n := len(written)
	if written[0] != "facts-before.json" || n < 6 || written[n-4] != "SHA256SUMS" || written[n-3] != "busy" || written[n-2] != "current" || written[n-1] != "1.list" {
		t.Errorf("the writes were %v", written)
	}
}

// A write that fails, wherever it fails, leaves nothing: no folder, no flag, no pointer, and the run that was there is as it was.
func TestAStartThatCannotWriteLeavesNothingWhereverItFails(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	original := osUpgradeWriteFile
	t.Cleanup(func() { osUpgradeWriteFile = original })
	count := 0
	osUpgradeWriteFile = func(path string, data []byte, mode os.FileMode) error {
		count++
		return original(path, data, mode)
	}
	if _, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan)); err != nil {
		t.Fatal(err)
	}
	total := count
	for failAt := 1; failAt <= total; failAt++ {
		other := newUpgradeBox(t)
		_, plan := other.aMachineThatCanStart()
		count = 0
		osUpgradeWriteFile = func(path string, data []byte, mode os.FileMode) error {
			count++
			if count == failAt {
				return errors.New("no room")
			}
			return original(path, data, mode)
		}
		_, err := other.StartSystemOSUpgrade(other.aGoodRequest(plan))
		if refusal := refusalOf(t, err); refusal.Code != OSUpgradeStartFailed {
			t.Fatalf("write %d of %d: %v", failAt, total, err)
		}
		if folders := other.runFolders(); len(folders) != 0 {
			t.Errorf("write %d: the folders %v were left", failAt, folders)
		}
		if busy, _ := osUpgradeFlagOpen(); busy {
			t.Errorf("write %d: the flag of an open upgrade was left", failAt)
		}
		if _, state := currentOSUpgrade(); state != osUpgradeNoRun {
			t.Errorf("write %d: the pointer to a run was left", failAt)
		}
		if n := len(other.startedUnits()); n != 0 {
			t.Errorf("write %d: %d units were started", failAt, n)
		}
	}
}

// A unit that systemd did not start leaves the story of it and a machine that is free: the log says START_FAILED, the flag is gone, and a new start takes its place.
func TestAUnitThatSystemdDidNotStartLeavesAFreeMachineAndTheStoryOfIt(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	box.startErr = errors.New("exit status 1")
	_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeStartFailed {
		t.Fatalf("err = %v", err)
	}
	if busy, _ := osUpgradeFlagOpen(); busy {
		t.Error("the flag of an open upgrade is there for a unit that did not start")
	}
	folders := box.runFolders()
	if len(folders) != 1 {
		t.Fatalf("the folders are %v", folders)
	}
	run := box.read(folders[0])
	if tail := run.Markers[len(run.Markers)-1]; tail.Kind != osrun.MkStartFailed || tail.N != 1 {
		t.Errorf("the log ends with %+v", tail)
	}
	status := box.GetSystemOSUpgradeStatus()
	if status.State != osrun.StateFailed || status.Reason != osrun.ReasonStart || status.Busy || !status.Reversible {
		t.Errorf("the status is %+v", status)
	}
	// the next start takes the place of it
	box.startErr = nil
	box.advance(time.Minute)
	status, err = box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if err != nil {
		t.Fatal(err)
	}
	if status.RunID == run.ID || status.State != osrun.StateRunning || len(box.runFolders()) != 2 {
		t.Errorf("the status is %+v, the folders %v", status, box.runFolders())
	}
}

// Two starts at once make one run: whichever is second finds the first open.
func TestTwoStartsAtOnceMakeOneRun(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	var wg sync.WaitGroup
	var made, refused atomic.Int32
	gate := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
			switch {
			case err == nil:
				made.Add(1)
			case err.(*OSUpgradeRefusal).Code == OSUpgradeRunOpen:
				refused.Add(1)
			default:
				t.Errorf("err = %v", err)
			}
		}()
	}
	close(gate)
	wg.Wait()
	if made.Load() != 1 || refused.Load() != 7 || len(box.runFolders()) != 1 || len(box.startedUnits()) != 1 {
		t.Fatalf("%d runs were made, %d refused, folders %v, %d units", made.Load(), refused.Load(), box.runFolders(), len(box.startedUnits()))
	}
}

func TestTheOldRunsAreKeptToTheLastThree(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	var ids []string
	for i := 0; i < 5; i++ {
		status, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, status.RunID)
		// the unit is lost, and the run is undone, which a later start takes the place of
		run := box.read(status.RunID)
		box.say(run,
			osrun.Marker{Kind: osrun.MkUnitStart, N: 1, Phase: osrun.PhaseRun, Boot: boxBoot1, PID: 1},
			osrun.Marker{Kind: osrun.MkStop, N: 1, Index: 1, ID: osrun.BlockVerify, RC: 1, Class: osrun.ClassBusy},
			osrun.Marker{Kind: osrun.MkUnwound, N: 1, Result: "ok"},
			osrun.Marker{Kind: osrun.MkUnitEnd, N: 1, Result: "stopped"})
		box.advance(time.Hour)
		if err := os.Chtimes(filepath.Join(run.Dir, "run.json"), box.now(), box.now()); err != nil {
			t.Fatal(err)
		}
	}
	folders := box.runFolders()
	if len(folders) != 3 || !slices.Contains(folders, ids[4]) || !slices.Contains(folders, ids[3]) || !slices.Contains(folders, ids[2]) {
		t.Errorf("the folders are %v, the runs were %v", folders, ids)
	}
}

// ---- the switch ---------------------------------------------------------------------------------------------------------

func TestTheSwitchIsAFileThatGatesWhatBeginsAndNothingElse(t *testing.T) {
	box := newUpgradeBox(t)
	if box.GetSystemOSUpgradeStatus().BetaEnabled {
		t.Fatal("the switch is on from the start")
	}
	if err := box.SetSystemOSUpgradeBeta(true); err != nil {
		t.Fatal(err)
	}
	if !box.GetSystemOSUpgradeStatus().BetaEnabled {
		t.Error("the switch is not on")
	}
	if runtime.GOOS == "linux" {
		if info, err := os.Stat(osUpgradeBetaFile); err != nil || info.Mode().Perm() != 0o644 {
			t.Errorf("the file is %v, %v", info, err)
		}
	}
	if err := box.SetSystemOSUpgradeBeta(true); err != nil {
		t.Errorf("a switch that is on is turned on: %v", err)
	}
	if err := box.SetSystemOSUpgradeBeta(false); err != nil {
		t.Fatal(err)
	}
	if err := box.SetSystemOSUpgradeBeta(false); err != nil {
		t.Errorf("a switch that is off is turned off: %v", err)
	}
	if box.GetSystemOSUpgradeStatus().BetaEnabled {
		t.Error("the switch is still on")
	}
}

func TestTheSwitchIsNotTurnedOffWhileARunIsOpen(t *testing.T) {
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	status, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if err != nil {
		t.Fatal(err)
	}
	err = box.SetSystemOSUpgradeBeta(false)
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeRunOpen || refusal.Data["run_id"] != status.RunID {
		t.Fatalf("err = %v", err)
	}
	if !osUpgradeBetaOn() {
		t.Error("the switch was turned off")
	}
	// a run that is open goes on with the switch off: the status says so, and still says what it must
	os.Remove(osUpgradeBetaFile)
	if got := box.GetSystemOSUpgradeStatus(); got.BetaEnabled || got.State != osrun.StateRunning || !got.Busy {
		t.Errorf("the status is %+v", got)
	}
}

func TestAStartThatTheSwitchWasTurnedOffUnderIsRefusedAtTheEnd(t *testing.T) {
	box := newUpgradeBox(t)
	st, plan := box.aMachineThatCanStart()
	st.readiness = func(context.Context, osreadiness.Probe) osreadiness.Report {
		os.Remove(osUpgradeBetaFile)
		return osreadiness.Report{State: osreadiness.StateReady, Checks: []osreadiness.Finding{}}
	}
	_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
	if refusal := refusalOf(t, err); refusal.Code != OSUpgradeBetaOff {
		t.Errorf("err = %v", err)
	}
	box.assertNothingChanged(nil)
}

// What the machine cannot say is a no: a lock that cannot be looked at, an AppManagement that does not answer, sources that cannot be read again, and a hash of
// either of the two that is not the plan's.
func TestWhatTheMachineCannotSayIsNotAYes(t *testing.T) {
	arrange := map[string]func(b *upgradeBox, st *startStubs){
		"a lock that cannot be looked at": func(b *upgradeBox, _ *startStubs) { b.updater.dpkgLocked = nil },
		"an AppManagement that is silent": func(b *upgradeBox, _ *startStubs) {
			b.setApps(func(context.Context) ([]string, error) { return nil, errors.New("no answer") })
		},
		"sources that cannot be read": func(_ *upgradeBox, st *startStubs) {
			st.rewritten = func(osplan.HopSpec) (*osplan.Rewritten, error) { return nil, errors.New("no sources") }
		},
		"no tree at all": func(_ *upgradeBox, st *startStubs) {
			st.rewritten = func(osplan.HopSpec) (*osplan.Rewritten, error) { return nil, nil }
		},
		"the files are not the plan's": func(b *upgradeBox, st *startStubs) {
			st.rewritten = func(osplan.HopSpec) (*osplan.Rewritten, error) {
				plan := b.livePlan()
				return &osplan.Rewritten{HashCurrent: strings.Repeat("e", 64), HashGenerated: plan.Sources.HashGenerated}, nil
			}
		},
		"the tree is not the plan's": func(b *upgradeBox, st *startStubs) {
			st.rewritten = func(osplan.HopSpec) (*osplan.Rewritten, error) {
				plan := b.livePlan()
				return &osplan.Rewritten{HashCurrent: plan.Sources.HashCurrent, HashGenerated: strings.Repeat("e", 64)}, nil
			}
		},
	}
	want := map[string]string{
		"a lock that cannot be looked at": OSUpgradeBusy, "an AppManagement that is silent": OSUpgradeAppsBusy, "sources that cannot be read": OSUpgradeSourcesChanged,
		"no tree at all": OSUpgradeSourcesChanged, "the files are not the plan's": OSUpgradeSourcesChanged, "the tree is not the plan's": OSUpgradeSourcesChanged,
	}
	for name, do := range arrange {
		box := newUpgradeBox(t)
		st, plan := box.aMachineThatCanStart()
		do(box, st)
		_, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan))
		if refusal := refusalOf(t, err); refusal.Code != want[name] {
			t.Errorf("%s: %+v", name, refusal)
		}
		box.assertNothingChanged(nil)
	}
	// the one that is the plan's is let through
	box := newUpgradeBox(t)
	_, plan := box.aMachineThatCanStart()
	if _, err := box.StartSystemOSUpgrade(box.aGoodRequest(plan)); err != nil {
		t.Fatal(err)
	}
}
