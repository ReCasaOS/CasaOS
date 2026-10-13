package osplan

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// A plan is judged from what apt said and what the box says of itself: these are the rules that turn them into problems.

func problemOf(p *Plan, code string) *Problem {
	for i := range p.Problems {
		if p.Problems[i].Code == code {
			return &p.Problems[i]
		}
	}
	return nil
}

func removalOf(p *Plan, name string) *Removal {
	for i := range p.Removals {
		if p.Removals[i].Name == name {
			return &p.Removals[i]
		}
	}
	return nil
}

// ---- essential packages, as apt says them ------------------------------------------------------------------------

// dpkg says that none of the three is essential (a box whose database says otherwise is not the case here): the warning of apt
// alone makes login and util-linux essential, and not mount, which apt does not name in it.
func TestTheWarningOfAptAloneMakesARemovedPackageEssential(t *testing.T) {
	in := realInputs(t)
	in.Dist = parseReal(t, "chroot-d11-remove-login.txt")
	in.Estimates = Estimates{}
	in.Facts = map[string]PackageFacts{
		"login": {Version: "1:4.8.1-1", Priority: "optional"}, "mount": {Version: "2.36.1-8+deb11u2", Priority: "optional"}, "util-linux": {Version: "2.36.1-8+deb11u2", Priority: "optional"},
	}
	in.AutoInstalled = map[string]bool{"login": true, "mount": true, "util-linux": true}
	p := assembled(in)
	if p.Verdict != VerdictBlocked {
		t.Fatalf("verdict %s, problems %+v", p.Verdict, p.Problems)
	}
	var names []string
	for _, pr := range p.Problems {
		if pr.Code == ProblemRemovalEssential {
			names = append(names, pr.Data["name"].(string))
		}
	}
	if !slices.Equal(names, []string{"login", "util-linux"}) {
		t.Errorf("essential problems = %v: %+v", names, p.Problems)
	}
	for name, want := range map[string]bool{"login": true, "mount": false, "util-linux": true} {
		if r := removalOf(p, name); r == nil || r.Essential != want {
			t.Errorf("%s: %+v, want essential %v", name, r, want)
		}
	}
	// the warning of the minimal upgrade counts as much as the full one's
	in = realInputs(t)
	in.Upgrade = parseReal(t, "chroot-d11-remove-login.txt")
	in.Facts = map[string]PackageFacts{"fuse": {Priority: "optional"}, "libsemanage1": {Priority: "optional"}, "login": {Priority: "optional"}, "mount": {Priority: "optional"}, "util-linux": {Priority: "optional"}}
	in.AutoInstalled = map[string]bool{"fuse": true, "libsemanage1": true, "login": true, "mount": true, "util-linux": true}
	if p := assembled(in); p.Verdict != VerdictBlocked || problemOf(p, ProblemRemovalEssential) == nil {
		t.Errorf("the warning of the minimal upgrade: verdict %s, problems %+v", p.Verdict, p.Problems)
	}
}

// The same through the whole of Run: the output of apt that has the warning is a plan that is blocked, and not one that is not
// understood.
func TestARunWhoseDistUpgradeWarnsOfEssentialPackagesIsBlockedAndNotUnparsed(t *testing.T) {
	b := newBox(t)
	b.facts["login"] = "login\t1:4.8.1-1\t\toptional"
	b.facts["mount"] = "mount\t2.36.1-8+deb11u2\t\toptional"
	b.facts["util-linux"] = "util-linux\t2.36.1-8+deb11u2\t\toptional"
	b.files[extendedStates] = "Package: login\nAuto-Installed: 1\n\nPackage: mount\nAuto-Installed: 1\n\nPackage: util-linux\nAuto-Installed: 1\n"
	outputs := realOutputs(t)
	outputs["dist-upgrade"] = realOutput(t, "chroot-d11-remove-login.txt")
	// (a minimal upgrade that does nothing: the measured box's keeps 111 packages back, which this full upgrade does not name)
	outputs["upgrade"] = Output{Text: "0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"}
	outputs["estimate"] = Output{}
	p := mustPlan(t, b, newSandbox(b.fakeFiles, outputs))
	if p.Verdict != VerdictBlocked || !slices.Equal(problemCodes(p), []string{ProblemRemovalEssential, ProblemRemovalEssential}) {
		t.Fatalf("verdict %s, problems %+v", p.Verdict, p.Problems)
	}
}

func TestAnEssentialPackageOfAnotherArchitectureIsEssentialByTheNameDpkgKnowsItBy(t *testing.T) {
	in := realInputs(t)
	in.Dist = &SolverRun{Removed: []string{"libfoo:i386"}, Summary: Summary{Remove: 1}}
	in.Facts = map[string]PackageFacts{"libfoo": {Version: "1", Essential: true}}
	in.AutoInstalled = map[string]bool{"libfoo:i386": true}
	p := assembled(in)
	if r := removalOf(p, "libfoo:i386"); r == nil || !r.Essential || r.Version != "1" {
		t.Errorf("removal = %+v", r)
	}
	if p.Verdict != VerdictBlocked || problemOf(p, ProblemRemovalEssential) == nil {
		t.Errorf("verdict %s, problems %+v", p.Verdict, p.Problems)
	}
}

// ---- what is removed by the minimal upgrade is judged as what the full one removes ---------------------------------------

func TestAProtectedPackageThatOnlyTheMinimalUpgradeRemovesBlocksThePlan(t *testing.T) {
	b := newBox(t)
	b.facts["docker-ce"] = "docker-ce\t5:28.0.4\t\toptional"
	b.files[extendedStates] = "Package: docker-ce\nAuto-Installed: 1\n\nPackage: libsemanage1\nAuto-Installed: 1\n\nPackage: fuse\nAuto-Installed: 1\n"
	outputs := realOutputs(t)
	up := outputs["upgrade"]
	line := "274 upgraded, 0 newly installed, 0 to remove and 111 not upgraded.\n"
	if strings.Count(up.Text, line) != 1 {
		t.Fatal("the summary of the real upgrade is not where it was")
	}
	up.Text = strings.Replace(up.Text, line, "The following packages will be REMOVED:\n  docker-ce\n274 upgraded, 0 newly installed, 1 to remove and 111 not upgraded.\nRemv docker-ce [5:28.0.4]\n", 1)
	outputs["upgrade"] = up
	p := mustPlan(t, b, newSandbox(b.fakeFiles, outputs))
	if p.Verdict != VerdictBlocked || !slices.Equal(problemCodes(p), []string{ProblemRemovalProtected}) {
		t.Fatalf("verdict %s, problems %+v", p.Verdict, p.Problems)
	}
	// dpkg was asked of it, with the others: the plan says its version
	if r := removalOf(p, "docker-ce"); r == nil || r.Version != "5:28.0.4" || !r.Protected || r.Pattern != "docker-ce" {
		t.Errorf("removal = %+v", r)
	}
	if len(p.Removals) != 3 {
		t.Errorf("removals = %+v", p.Removals)
	}
}

func dpkgQueries(b *testBox) [][]string {
	var out [][]string
	for _, argv := range b.ran {
		if len(argv) > 2 && argv[0] == "dpkg-query" && argv[2] == dpkgFormat {
			out = append(out, argv[3:])
		}
	}
	return out
}

func manyRemovals(n int) Output {
	var names, remv []string
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("r%03d", i)
		names = append(names, name)
		remv = append(remv, "Remv "+name+" [1]")
	}
	text := headLines + "The following packages will be REMOVED:\n  " + strings.Join(names, " ") + "\n" +
		fmt.Sprintf("0 upgraded, 0 newly installed, %d to remove and 0 not upgraded.\n", n) + strings.Join(remv, "\n") + "\n"
	return Output{Text: text}
}

func TestDpkgIsAskedOfTheRemovedPackagesTwoHundredAtATime(t *testing.T) {
	for _, tc := range []struct {
		removed int
		calls   []int
	}{{1, []int{1}}, {199, []int{199}}, {200, []int{200}}, {201, []int{200, 1}}, {400, []int{200, 200}}, {401, []int{200, 200, 1}}} {
		b := newBox(t)
		for i := 0; i < tc.removed; i++ {
			name := fmt.Sprintf("r%03d", i)
			b.facts[name] = name + "\t1\t\toptional"
		}
		outputs := realOutputs(t)
		outputs["dist-upgrade"] = manyRemovals(tc.removed)
		outputs["estimate"] = Output{}
		p := mustPlan(t, b, newSandbox(b.fakeFiles, outputs))
		if len(p.Removals) != tc.removed {
			t.Errorf("%d removed: %d removals", tc.removed, len(p.Removals))
		}
		var got []int
		for _, names := range dpkgQueries(b) {
			got = append(got, len(names))
		}
		if !slices.Equal(got, tc.calls) {
			t.Errorf("%d removed: dpkg-query asked of %v names at a time, want %v", tc.removed, got, tc.calls)
		}
	}
}

func TestADpkgThatEndsWithMoreThanOneIsNotAPlan(t *testing.T) {
	for _, exit := range []int{2, 3, 127} {
		b := newBox(t)
		b.dpkgExit = exit
		sb := newSandbox(b.fakeFiles, realOutputs(t))
		_, f := runPlan(t, b, sb, Options{})
		if f == nil || f.Code != CodeInternal || f.Phase != PhaseParse {
			t.Errorf("exit %d: failure = %+v", exit, f)
		}
		if sb.removed != 1 {
			t.Errorf("exit %d: the sandbox was not removed", exit)
		}
	}
}

// ---- what is still not upgraded after the full upgrade -----------------------------------------------------------------

func TestOneNameKeptBackAfterTheFullUpgradeBlocksThePlan(t *testing.T) {
	in := realInputs(t)
	in.Dist = &SolverRun{KeptBack: []string{"apt"}, Summary: Summary{NotUpgraded: 1}}
	in.Upgrade = &SolverRun{}
	p := assembled(in)
	pr := problemOf(p, ProblemDistKeptBack)
	if p.Verdict != VerdictBlocked || pr == nil || !slices.Equal(pr.Data["names"].([]string), []string{"apt"}) {
		t.Fatalf("verdict %s, problems %+v", p.Verdict, p.Problems)
	}
	if _, ok := pr.Data["unlisted"]; ok {
		t.Errorf("one named and one counted: unlisted = %v", pr.Data["unlisted"])
	}
}

// A package on hold whose candidate is a downgrade is counted by apt as not upgraded and not named (measured): the plan is
// blocked all the same, and says that apt did not name it.
func TestNotUpgradedThatAptDoesNotNameBlocksThePlanAndIsCounted(t *testing.T) {
	b := newBox(t)
	outputs := realOutputs(t)
	outputs["upgrade"] = realOutput(t, "chroot-d11-downgrade-held-upgrade.txt")
	outputs["dist-upgrade"] = realOutput(t, "chroot-d11-downgrade-held-dist-upgrade.txt")
	outputs["estimate"] = Output{}
	p := mustPlan(t, b, newSandbox(b.fakeFiles, outputs))
	pr := problemOf(p, ProblemDistKeptBack)
	if p.Verdict != VerdictBlocked || pr == nil {
		t.Fatalf("verdict %s, problems %+v", p.Verdict, p.Problems)
	}
	if names := pr.Data["names"].([]string); len(names) != 0 || pr.Data["unlisted"] != 1 {
		t.Errorf("data = %+v", pr.Data)
	}
	if p.Counts.DistUpgrade.KeptBack != 1 || p.Counts.Upgrade.KeptBack != 1 {
		t.Errorf("counts = %+v", p.Counts)
	}
	raw, _ := json.Marshal(pr.Data)
	if string(raw) != `{"names":[],"unlisted":1}` {
		t.Errorf("data = %s", raw)
	}
	// some named and some not
	in := realInputs(t)
	in.Dist = &SolverRun{KeptBack: []string{"apt", "curl"}, Summary: Summary{NotUpgraded: 5}}
	in.Upgrade = &SolverRun{}
	if pr := problemOf(assembled(in), ProblemDistKeptBack); pr == nil || pr.Data["unlisted"] != 3 || !slices.Equal(pr.Data["names"].([]string), []string{"apt", "curl"}) {
		t.Errorf("problem = %+v", pr)
	}
	// the minimal upgrade's not upgraded are not a problem: apt keeps back packages there, that the full upgrade does not
	in = realInputs(t)
	in.Upgrade = &SolverRun{KeptBack: []string{"apt"}, Summary: Summary{NotUpgraded: 4}}
	if p := assembled(in); problemOf(p, ProblemDistKeptBack) != nil {
		t.Errorf("a problem of the minimal upgrade: %+v", p.Problems)
	}
}

// Real apt 2.2.4: a 1 is installed, and a 2 depends on a version of c that no repository has. The minimal upgrade names a under the
// packages kept back; the full upgrade prints nothing of it and counts it nowhere, and "apt list --upgradable" still shows it. The
// hop does not reach its target for it, and a plan that did not look for it would say "ok".
func TestAPackageThatTheFullUpgradeLeavesBehindWithoutAWordBlocksThePlanAndIsNamed(t *testing.T) {
	b := newBox(t)
	outputs := realOutputs(t)
	outputs["upgrade"] = realOutput(t, "chroot-d11-silent-upgrade.txt")
	outputs["dist-upgrade"] = realOutput(t, "chroot-d11-silent-dist-upgrade.txt")
	outputs["estimate"] = Output{}
	p := mustPlan(t, b, newSandbox(b.fakeFiles, outputs))
	pr := problemOf(p, ProblemDistKeptBack)
	if p.Verdict != VerdictBlocked || pr == nil || !slices.Equal(problemCodes(p), []string{ProblemDistKeptBack}) {
		t.Fatalf("verdict %s, problems %+v", p.Verdict, p.Problems)
	}
	if raw, _ := json.Marshal(pr.Data); string(raw) != `{"names":["osp-k"]}` {
		t.Errorf("data = %s", raw)
	}
	// apt itself counts none of it
	if p.Counts.DistUpgrade != (Count{}) || p.Counts.Upgrade.KeptBack != 1 || len(p.KeptBack) != 0 {
		t.Errorf("counts = %+v, kept back %v", p.Counts, p.KeptBack)
	}
}

func TestOnlyWhatTheFullUpgradeNamesInNoListIsLeftBehindAndItIsNamedOnceAndSorted(t *testing.T) {
	// a package that the minimal upgrade keeps back and that the full upgrade names, in whichever list, is not left behind
	for list, set := range map[string]func(*SolverRun, []string){
		"upgraded":   func(r *SolverRun, n []string) { r.Upgraded = n },
		"new":        func(r *SolverRun, n []string) { r.New = n },
		"removed":    func(r *SolverRun, n []string) { r.Removed = n },
		"downgraded": func(r *SolverRun, n []string) { r.Downgraded = n },
		"kept back":  func(r *SolverRun, n []string) { r.KeptBack = n; r.Summary.NotUpgraded = len(n) },
	} {
		in := realInputs(t)
		in.Dist = &SolverRun{}
		in.Upgrade = &SolverRun{KeptBack: []string{"osp-k"}, Summary: Summary{NotUpgraded: 1}}
		set(in.Dist, []string{"osp-k"})
		pr := problemOf(assembled(in), ProblemDistKeptBack)
		switch list {
		case "kept back":
			if pr == nil || !slices.Equal(pr.Data["names"].([]string), []string{"osp-k"}) {
				t.Errorf("%s: problem = %+v", list, pr)
			}
		default:
			if pr != nil {
				t.Errorf("%s: a package that the full upgrade names is left behind: %+v", list, pr)
			}
		}
	}
	// named in none: left behind, with the ones that the full upgrade keeps back, once each, sorted
	in := realInputs(t)
	in.Dist = &SolverRun{KeptBack: []string{"zlib1g", "curl"}, Upgraded: []string{"bash"}, Summary: Summary{Upgraded: 1, NotUpgraded: 2}}
	in.Upgrade = &SolverRun{KeptBack: []string{"osp-k", "curl", "bash", "apt", "osp-k"}, Summary: Summary{NotUpgraded: 5}}
	pr := problemOf(assembled(in), ProblemDistKeptBack)
	if pr == nil || !slices.Equal(pr.Data["names"].([]string), []string{"apt", "curl", "osp-k", "zlib1g"}) {
		t.Fatalf("problem = %+v", pr)
	}
	if _, ok := pr.Data["unlisted"]; ok {
		t.Errorf("unlisted = %v: the two that the full upgrade counts are the two that it names", pr.Data["unlisted"])
	}
	// the measured pairs of the box: there is none
	if got := leftBehind(realInputs(t)); len(got) != 0 {
		t.Errorf("left behind on the measured pair: %v", got)
	}
	kept := realInputs(t)
	kept.Dist, kept.Upgrade = parseReal(t, "sandbox-kept-dist-upgrade.txt"), parseReal(t, "sandbox-kept-upgrade.txt")
	if got := leftBehind(kept); len(got) != 0 {
		t.Errorf("left behind on the other measured pair: %v", got)
	}
}

// ---- a downgrade ------------------------------------------------------------------------------------------------------

func TestAPackageThatAptWouldDowngradeBlocksThePlanAndIsNamed(t *testing.T) {
	b := newBox(t)
	outputs := realOutputs(t)
	outputs["upgrade"] = realOutput(t, "chroot-d11-downgrade-upgrade.txt")
	outputs["dist-upgrade"] = realOutput(t, "chroot-d11-downgrade-dist-upgrade.txt")
	outputs["estimate"] = realOutput(t, "chroot-d11-downgrade-size.txt")
	p := mustPlan(t, b, newSandbox(b.fakeFiles, outputs))
	pr := problemOf(p, ProblemDowngrade)
	if p.Verdict != VerdictBlocked || pr == nil || pr.Severity != SeverityBlocked || !slices.Equal(pr.Data["names"].([]string), []string{"docker-ce"}) {
		t.Fatalf("verdict %s, problems %+v", p.Verdict, p.Problems)
	}
	if !slices.Equal(problemCodes(p), []string{ProblemDowngrade}) {
		t.Errorf("problems = %v", problemCodes(p))
	}
	// the sizes of a transaction with a downgrade are read as the others' are
	if !p.Estimates.Known || *p.Estimates.DownloadBytes != 724 {
		t.Errorf("estimates = %+v", p.Estimates)
	}
	if !ValidPlanID(p.PlanID) {
		t.Errorf("plan id = %q", p.PlanID)
	}
}

func TestADowngradeOfEitherSimulationBlocksAndTheNamesAreOnceEachAndSorted(t *testing.T) {
	in := realInputs(t)
	in.Dist.Downgraded = []string{"zlib1g", "bash"}
	in.Upgrade.Downgraded = []string{"bash", "apt"}
	pr := problemOf(assembled(in), ProblemDowngrade)
	if pr == nil || !slices.Equal(pr.Data["names"].([]string), []string{"apt", "bash", "zlib1g"}) {
		t.Fatalf("problem = %+v", pr)
	}
	in = realInputs(t)
	in.Upgrade.Downgraded = []string{"bash"}
	if pr := problemOf(assembled(in), ProblemDowngrade); pr == nil {
		t.Error("a downgrade of the minimal upgrade only is not a problem")
	}
	in = realInputs(t)
	in.Dist.Downgraded = []string{"bash"}
	if pr := problemOf(assembled(in), ProblemDowngrade); pr == nil {
		t.Error("a downgrade of the full upgrade only is not a problem")
	}
	// the code is one of the plan's
	if !slices.Contains(ProblemCodes, ProblemDowngrade) {
		t.Error("downgrade is not in ProblemCodes")
	}
}

// ---- the room on the disk ----------------------------------------------------------------------------------------------

func TestRoomThatCouldNotBeReadIsSaidAsUnknownAndNotAsNone(t *testing.T) {
	b := newBox(t)
	delete(b.free, "/")
	b.free["/usr"] = 1 << 30 // read, and short
	p := mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
	var short, unknown *Problem
	for i := range p.Problems {
		if p.Problems[i].Code != ProblemDiskShort {
			continue
		}
		switch p.Problems[i].Data["path"] {
		case "/usr":
			short = &p.Problems[i]
		case "/":
			unknown = &p.Problems[i]
		}
	}
	if short == nil || unknown == nil {
		t.Fatalf("problems = %+v", p.Problems)
	}
	if short.Data["free"] != uint64(1<<30) || short.Data["unknown"] != nil || short.Data["need"] != uint64(1523741824) {
		t.Errorf("a room that was read: %+v", short.Data)
	}
	if _, has := unknown.Data["free"]; has || unknown.Data["unknown"] != true || unknown.Data["need"] != uint64(512<<20) {
		t.Errorf("a room that was not read: %+v", unknown.Data)
	}
	raw, _ := json.Marshal(unknown.Data)
	if string(raw) != `{"need":536870912,"path":"/","unknown":true}` {
		t.Errorf("json = %s", raw)
	}
	if !p.Disk[2].Unknown || p.Disk[1].Unknown {
		t.Errorf("disk = %+v", p.Disk)
	}
}

// The sandbox lives in /var/tmp, which is on / by default, and takes some 214 MiB there: the room that the plan says is the
// box's, and not what is left of it while the plan is made.
func TestTheRoomIsReadBeforeTheSandboxIsWritten(t *testing.T) {
	b := newBox(t)
	sb := newSandbox(b.fakeFiles, realOutputs(t))
	sb.onMake = func() {
		for _, p := range []string{"/", "/usr", "/var/cache/apt/archives"} {
			b.free[p] -= 214 << 20
		}
	}
	p := mustPlan(t, b, sb)
	want := []DiskRow{
		{Path: "/var/cache/apt/archives", Need: 690870912, Free: 8 << 30, OK: true},
		{Path: "/usr", Need: 1523741824, Free: 8 << 30, OK: true},
		{Path: "/", Need: 536870912, Free: 20 << 30, OK: true},
	}
	if !slices.Equal(p.Disk, want) {
		t.Errorf("disk = %+v, want %+v", p.Disk, want)
	}
	// a box that is within the size of the sandbox of a floor is not short for it
	b = newBox(t)
	b.free["/"] = 512<<20 + 100<<20
	sb = newSandbox(b.fakeFiles, realOutputs(t))
	sb.onMake = func() { b.free["/"] -= 214 << 20 }
	if p := mustPlan(t, b, sb); !p.Disk[2].OK || p.Verdict == VerdictBlocked {
		t.Errorf("disk = %+v, problems %+v", p.Disk, p.Problems)
	}
}

func TestAPlanWithNoSizesSaysSoAndOneWithSizesDoesNot(t *testing.T) {
	b := newBox(t)
	outputs := realOutputs(t)
	outputs["estimate"] = Output{Text: "anything", ExitCode: 0}
	p := mustPlan(t, b, newSandbox(b.fakeFiles, outputs))
	if p.DiskNote != "estimates_unknown" || len(p.Disk) != 0 || p.Estimates.Known {
		t.Errorf("note %q, disk %+v, estimates %+v", p.DiskNote, p.Disk, p.Estimates)
	}
	raw, _ := json.Marshal(p)
	if !strings.Contains(string(raw), `"disk_note":"estimates_unknown"`) || !strings.Contains(string(raw), `"disk":[]`) {
		t.Errorf("json has no note: %s", raw)
	}
	// it does not weigh on the verdict or on the id
	q := mustPlan(t, newBox(t), newSandbox(b.fakeFiles, realOutputs(t)))
	if q.DiskNote != "" || len(q.Disk) != 3 {
		t.Errorf("note %q, disk %+v", q.DiskNote, q.Disk)
	}
	raw, _ = json.Marshal(q)
	if strings.Contains(string(raw), "disk_note") {
		t.Errorf("a note for known sizes: %s", raw)
	}
	if p.PlanID != q.PlanID || p.Verdict != q.Verdict {
		t.Errorf("the note changes the plan: %s %s, %s %s", p.PlanID, p.Verdict, q.PlanID, q.Verdict)
	}
	// sizes that are known in half are not known
	in := realInputs(t)
	in.Estimates = Estimates{Known: true}
	if p := assembled(in); p.DiskNote != DiskNoteEstimatesUnknown || len(p.Disk) != 0 {
		t.Errorf("half of the sizes: note %q, disk %+v", p.DiskNote, p.Disk)
	}
}

func TestTheRoomForTheSandboxIsExactlySixHundredFortyMebibytes(t *testing.T) {
	for _, tc := range []struct {
		free uint64
		ok   bool
	}{{671088639, false}, {671088640, true}, {671088641, true}} {
		b := newBox(t)
		b.free["/var/tmp"] = tc.free
		_, f := Preflight(context.Background(), b)
		if (f == nil) != tc.ok || f != nil && (f.Code != CodeNoRoom || f.Data["need"] != uint64(671088640) || f.Data["free"] != tc.free) {
			t.Errorf("%d bytes free: %v", tc.free, f)
		}
	}
}

// ---- the lists of a plan ----------------------------------------------------------------------------------------------

func TestTheListsOfAPlanAreCutAtSixHundredNames(t *testing.T) {
	names := func(n int) []string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("p%04d", i))
		}
		return out
	}
	for _, tc := range []struct {
		n         int
		truncated bool
	}{{599, false}, {600, false}, {601, true}} {
		in := realInputs(t)
		in.Dist = &SolverRun{New: names(tc.n), KeptBack: names(tc.n), AutoRemovable: names(tc.n)}
		in.Upgrade = &SolverRun{}
		p := assembled(in)
		want := min(tc.n, 600)
		if len(p.NewPackages) != want || p.NewTruncated != tc.truncated || len(p.KeptBack) != want || p.KeptBackTruncated != tc.truncated ||
			len(p.Autoremovable) != want || p.AutoremovableTruncated != tc.truncated {
			t.Errorf("%d names: %d, %d, %d (%v %v %v)", tc.n, len(p.NewPackages), len(p.KeptBack), len(p.Autoremovable), p.NewTruncated, p.KeptBackTruncated, p.AutoremovableTruncated)
		}
		if pr := problemOf(p, ProblemDistKeptBack); tc.n > 0 && (pr == nil || len(pr.Data["names"].([]string)) != want) {
			t.Errorf("%d names: problem = %+v", tc.n, pr)
		}
	}
	if MaxListed != 600 {
		t.Errorf("MaxListed = %d", MaxListed)
	}
}

// ---- the time ---------------------------------------------------------------------------------------------------------

// A plan lives a day from the second it was made, and not from the clock's own reading of it (the id and the plan are said in
// seconds): a clock with nanoseconds, or in another zone, does not make a plan that lives a fraction of a second longer.
func TestAPlanLivesADayFromTheSecondItWasMadeInUTC(t *testing.T) {
	for name, now := range map[string]time.Time{
		"nanoseconds": time.Date(2026, 10, 12, 9, 30, 0, 999_000_000, time.UTC),
		"a zone":      time.Date(2026, 10, 12, 11, 30, 0, 123, time.FixedZone("CEST", 2*3600)),
	} {
		in := realInputs(t)
		in.Now = now
		p := assembled(in)
		if !p.MadeAt.Equal(time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)) || p.MadeAt.Location() != time.UTC || !p.ExpiresAt.Equal(time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)) {
			t.Errorf("%s: made %v, expires %v", name, p.MadeAt, p.ExpiresAt)
		}
		if p.Expired(time.Date(2026, 10, 13, 9, 29, 59, 999_999_999, time.UTC)) || !p.Expired(time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)) {
			t.Errorf("%s: the day is not a day", name)
		}
	}
}

// ---- which packages apt installed by itself, and which by name ----------------------------------------------------------

func TestTheFileOfAutomaticPackagesIsReadByNameAndArchitecture(t *testing.T) {
	text := "Package: libssl1.1\nArchitecture: amd64\nAuto-Installed: 1\n\n" +
		"Package: libfoo\nArchitecture: i386\nAuto-Installed: 1\n\n" +
		"Package: docs\nArchitecture: all\nAuto-Installed: 1\n\n" +
		"Package: bare\nAuto-Installed: 1\n\n" +
		"Package: mine\nArchitecture: amd64\nAuto-Installed: 0\n\n" +
		"Package: twice\nArchitecture: amd64\nAuto-Installed: 1\n\n" +
		"Package: twice\nArchitecture: amd64\nAuto-Installed: 0\n\n" +
		"Package: other\nArchitecture: amd64\nAuto-Installed: 0\n\n" +
		"Package: other\nArchitecture: amd64\nAuto-Installed: 1\n\n" +
		"Package: two:arch\nArchitecture: i386\nAuto-Installed: 1\n"
	got := parseExtendedStates(strings.ReplaceAll(text, "\n", "\r\n"), "amd64")
	want := map[string]bool{"libssl1.1": true, "libfoo:i386": true, "docs": true, "bare": true, "two:arch:i386": true}
	if len(got) != len(want) {
		t.Errorf("auto = %v, want %v", got, want)
	}
	for name := range want {
		if !got[name] {
			t.Errorf("%s is not automatic in %v", name, got)
		}
	}
	// the copy of another architecture of an automatic package is not one that apt installed by itself
	if got["libssl1.1:i386"] || got["libfoo"] || got["twice"] || got["other"] || got["mine"] {
		t.Errorf("auto = %v", got)
	}
	// the native architecture is the one of the box
	if got := parseExtendedStates("Package: libfoo\nArchitecture: i386\nAuto-Installed: 1\n", "i386"); !got["libfoo"] || got["libfoo:i386"] {
		t.Errorf("auto = %v", got)
	}
}

func TestAManualCopyOfAnotherArchitectureIsNotMadeAutomaticByTheNativeOne(t *testing.T) {
	auto := parseExtendedStates("Package: libssl1.1\nArchitecture: amd64\nAuto-Installed: 1\n", "amd64")
	in := realInputs(t)
	in.Dist = &SolverRun{Removed: []string{"libssl1.1", "libssl1.1:i386"}, Summary: Summary{Remove: 2}}
	in.Upgrade = &SolverRun{}
	in.Facts = map[string]PackageFacts{"libssl1.1": {Version: "1.1.1w", Priority: "optional"}}
	in.AutoInstalled = auto
	p := assembled(in)
	if r := removalOf(p, "libssl1.1"); r == nil || r.Manual {
		t.Errorf("the native one: %+v", r)
	}
	if r := removalOf(p, "libssl1.1:i386"); r == nil || !r.Manual {
		t.Errorf("the i386 copy that was installed by hand: %+v", r)
	}
	if pr := problemOf(p, ProblemRemovalManual); p.Verdict != VerdictNeedsAcceptance || pr == nil || pr.Data["name"] != "libssl1.1:i386" {
		t.Errorf("verdict %s, problems %+v", p.Verdict, p.Problems)
	}
	// and the copy that apt did install by itself is one
	in.AutoInstalled = parseExtendedStates("Package: libssl1.1\nArchitecture: i386\nAuto-Installed: 1\n", "amd64")
	if r := removalOf(assembled(in), "libssl1.1:i386"); r == nil || r.Manual {
		t.Errorf("an automatic i386 copy: %+v", r)
	}
}

func assembled(in Inputs) *Plan {
	p := Assemble(in)
	return &p
}
