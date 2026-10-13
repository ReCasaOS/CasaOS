package osplan

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// realPlan is the plan of the measured box (see newBox), made on a clock that does not move.
func realPlan(t *testing.T) *Plan {
	t.Helper()
	b := newBox(t)
	return mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
}

// goldenPlanID is the id of the plan of the measured box. If this test fails, something that the id is made of changed:
// either the canonical form of the id (a plan made yesterday no longer has the id it was given: say so, and change the
// version at the start of the id, "p1-"), or what the box's plan says. It is not to be edited to make the test pass.
const goldenPlanID = "p1-5787acb2f81006785a3076312615ae6c"

func TestThePlanIDOfTheRealBoxIsPinned(t *testing.T) {
	p := realPlan(t)
	if p.PlanID != goldenPlanID {
		t.Errorf("plan_id = %s\nwant       %s", p.PlanID, goldenPlanID)
	}
	if !ValidPlanID(p.PlanID) || len(p.PlanID) != 3+32 {
		t.Errorf("shape of %q", p.PlanID)
	}
	// the text it is the hash of: the fields in this order, the lists sorted, no map
	text := string(canonicalIDInput(*p))
	want := `{"hop":"debian-11-12","hash_current":"` + p.Sources.HashCurrent + `","hash_generated":"` + p.Sources.HashGenerated +
		`","paused":[{"file":"docker.list","line":0,"hosts":["https://download.docker.com"],"docker":true}],"holds":[],"removals":["fuse","libsemanage1"],` +
		`"verdict":"needs_acceptance","problems":["removal_manual"],"docker":{"present":true,"source_paused":true,"held":[]}}`
	if text != want {
		t.Errorf("canonical form:\n got %s\nwant %s", text, want)
	}
}

func TestEveryInputOfThePlanIDChangesIt(t *testing.T) {
	base := *realPlan(t)
	id := base.PlanID
	clone := func() Plan {
		p := base
		p.Paused = slices.Clone(base.Paused)
		p.Holds = slices.Clone(base.Holds)
		p.Removals = slices.Clone(base.Removals)
		p.Problems = slices.Clone(base.Problems)
		p.Docker.PackagesHeld = slices.Clone(base.Docker.PackagesHeld)
		return p
	}
	for name, mutate := range map[string]func(p *Plan){
		"the hop":                      func(p *Plan) { p.Hop = "debian-12-13" },
		"the sources as they are":      func(p *Plan) { p.Sources.HashCurrent = strings.Repeat("0", 64) },
		"the sources as generated":     func(p *Plan) { p.Sources.HashGenerated = strings.Repeat("0", 64) },
		"a paused file":                func(p *Plan) { p.Paused[0].File = "other.list" },
		"a paused line":                func(p *Plan) { p.Paused[0].Line = 3 },
		"the hosts of a paused file":   func(p *Plan) { p.Paused[0].Hosts = []string{"https://other.example.com"} },
		"the Docker flag of a pause":   func(p *Plan) { p.Paused[0].Docker = false },
		"one more paused":              func(p *Plan) { p.Paused = append(p.Paused, Paused{File: "z.list", Hosts: []string{}}) },
		"a hold":                       func(p *Plan) { p.Holds = []string{"curl"} },
		"a removal":                    func(p *Plan) { p.Removals = p.Removals[:1] },
		"a removal's name":             func(p *Plan) { p.Removals[0].Name = "fuse3" },
		"the verdict":                  func(p *Plan) { p.Verdict = VerdictBlocked },
		"a problem":                    func(p *Plan) { p.Problems = nil },
		"a problem of another code":    func(p *Plan) { p.Problems[0].Code = ProblemDiskShort },
		"Docker's presence":            func(p *Plan) { p.Docker.Present = false },
		"Docker's source being paused": func(p *Plan) { p.Docker.SourcePaused = false },
		"a Docker package on hold":     func(p *Plan) { p.Docker.PackagesHeld = []string{"docker-ce"} },
	} {
		p := clone()
		mutate(&p)
		if got := PlanID(p); got == id {
			t.Errorf("%s does not change the id", name)
		}
	}
	// what drifts between two reads of the same mirror is not in it
	for name, mutate := range map[string]func(p *Plan){
		"the counts":         func(p *Plan) { p.Counts.DistUpgrade.Upgraded += 3; p.Counts.Upgrade.KeptBack++ },
		"the sizes":          func(p *Plan) { n := int64(1); p.Estimates = Estimates{Known: true, DownloadBytes: &n, ExtraBytes: &n} },
		"the time":           func(p *Plan) { p.MadeAt = p.MadeAt.Add(time.Hour); p.ExpiresAt = p.ExpiresAt.Add(time.Hour) },
		"the disk":           func(p *Plan) { p.Disk = nil },
		"the new packages":   func(p *Plan) { p.NewPackages = append(p.NewPackages, "x") },
		"the sandbox":        func(p *Plan) { p.Sandbox.Bytes = 1; p.Sandbox.SecondsByPhase = nil },
		"the conf left out":  func(p *Plan) { p.AptConfLeftOut = []AptConfLeftOut{{File: "x", Reason: ConfNotSimple}} },
		"a removal's flags":  func(p *Plan) { p.Removals[0].Manual = !p.Removals[0].Manual; p.Removals[0].Version = "9" },
		"the shown lines":    func(p *Plan) { p.Sources.Files = nil },
		"the plan id itself": func(p *Plan) { p.PlanID = "x" },
		"the autoremovable":  func(p *Plan) { p.Autoremovable = nil },
	} {
		p := clone()
		mutate(&p)
		if got := PlanID(p); got != id {
			t.Errorf("%s changes the id", name)
		}
	}
	// reordering the inputs does not change it
	p := clone()
	p.Holds = []string{"zz", "aa", "mm"}
	q := clone()
	q.Holds = []string{"mm", "aa", "zz"}
	if PlanID(p) != PlanID(q) {
		t.Error("the order of the holds changes the id")
	}
	p, q = clone(), clone()
	p.Removals = []Removal{{Name: "b"}, {Name: "a"}}
	q.Removals = []Removal{{Name: "a"}, {Name: "b"}}
	if PlanID(p) != PlanID(q) {
		t.Error("the order of the removals changes the id")
	}
	p, q = clone(), clone()
	p.Paused = []Paused{{File: "b.list", Hosts: []string{"https://y", "https://x"}}, {File: "a.list", Line: 2}, {File: "a.list", Line: 1}}
	q.Paused = []Paused{{File: "a.list", Line: 1}, {File: "a.list", Line: 2}, {File: "b.list", Hosts: []string{"https://x", "https://y"}}}
	if PlanID(p) != PlanID(q) {
		t.Error("the order of the paused sources changes the id")
	}
	p, q = clone(), clone()
	p.Problems = []Problem{{Code: ProblemDiskShort}, {Code: ProblemDockerHeld}}
	q.Problems = []Problem{{Code: ProblemDockerHeld}, {Code: ProblemDiskShort}}
	if PlanID(p) != PlanID(q) {
		t.Error("the order of the problems changes the id")
	}
	// nothing in a plan is a map that the id depends on: two plans of the same box on a clock that moved have the same id
	b := newBox(t)
	b.step = 7 * time.Second
	later := mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
	if later.PlanID != id || later.MadeAt.Equal(base.MadeAt) && later.Sandbox.SecondsByPhase["update"] == base.Sandbox.SecondsByPhase["update"] {
		t.Errorf("a second plan: %s (%v), want %s", later.PlanID, later.Sandbox.SecondsByPhase, id)
	}
}

func TestAHoldChangesTheIDOfAPlanMadeTwice(t *testing.T) {
	b := newBox(t)
	first := mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
	second := mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
	if first.PlanID != second.PlanID {
		t.Fatalf("two plans back to back: %s, %s", first.PlanID, second.PlanID)
	}
	b.installed = "hi \tbash\tamd64\t5.1\n"
	held := mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
	if held.PlanID == first.PlanID || !slices.Equal(held.Holds, []string{"bash"}) {
		t.Errorf("held plan: %s, holds %v", held.PlanID, held.Holds)
	}
	// and so does a changed sources file
	b.installed = "ii \tbash\tamd64\t5.1\n"
	b.files["/etc/apt/sources.list"] += "# a comment\n"
	edited := mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
	if edited.PlanID == first.PlanID || edited.Sources.HashCurrent == first.Sources.HashCurrent || edited.Sources.HashGenerated == first.Sources.HashGenerated {
		t.Errorf("edited plan: %s", edited.PlanID)
	}
}

func TestThePlanIsJSONWithTheFieldsTheClientsUse(t *testing.T) {
	p := realPlan(t)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"plan_id", "hop", "made_at", "expires_at", "verdict", "problems", "counts", "removals", "new_packages", "kept_back", "autoremovable",
		"sources", "paused", "holds", "docker", "estimates", "disk", "sandbox", "apt_conf_left_out"} {
		if _, ok := m[k]; !ok {
			t.Errorf("no %q in the plan", k)
		}
	}
	for _, k := range []string{"new_truncated", "kept_back_truncated", "autoremovable_truncated"} {
		if _, ok := m[k]; ok {
			t.Errorf("%q is there when nothing was cut", k)
		}
	}
	counts := m["counts"].(map[string]any)
	if _, ok := counts["dist_upgrade"].(map[string]any)["kept_back"]; !ok {
		t.Errorf("counts = %v", counts)
	}
	sources := m["sources"].(map[string]any)
	for _, k := range []string{"files", "tree", "hash_current", "hash_generated", "dropped_backports", "firmware_added"} {
		if _, ok := sources[k]; !ok {
			t.Errorf("no sources.%s", k)
		}
	}
	// lists are lists, never null, so that a client does not test for it
	for _, k := range []string{"problems", "removals", "new_packages", "kept_back", "autoremovable", "paused", "holds", "disk", "apt_conf_left_out"} {
		if _, ok := m[k].([]any); !ok {
			t.Errorf("%s = %v is not a list", k, m[k])
		}
	}
	if d := m["docker"].(map[string]any); d["packages_held"] == nil {
		t.Error("docker.packages_held is null")
	}
	// the plan endpoint has no tree, and the runbook has it
	without := p.WithoutTree()
	raw, _ = json.Marshal(without)
	if strings.Contains(string(raw), `"tree"`) || without.Sources.HashGenerated != p.Sources.HashGenerated {
		t.Error("WithoutTree leaves the tree, or loses the rest")
	}
	if len(p.Sources.Tree) == 0 {
		t.Error("WithoutTree took the tree out of the plan itself")
	}
}

func TestAnEstimateThatIsNotKnownHasNoByteFieldsInJSON(t *testing.T) {
	raw, _ := json.Marshal(Estimates{})
	if string(raw) != `{"known":false}` {
		t.Errorf("estimates = %s", raw)
	}
	n := int64(0)
	raw, _ = json.Marshal(Estimates{Known: true, DownloadBytes: &n, ExtraBytes: &n})
	if string(raw) != `{"known":true,"download_bytes":0,"extra_bytes":0}` {
		t.Errorf("estimates = %s", raw)
	}
}

func TestThePlanExpiresAfterADay(t *testing.T) {
	p := realPlan(t)
	if p.Expired(p.MadeAt.Add(24*time.Hour - time.Second)) {
		t.Error("expired too early")
	}
	if !p.Expired(p.MadeAt.Add(24 * time.Hour)) {
		t.Error("not expired after a day")
	}
}

func TestLongListsAreCutAndSaid(t *testing.T) {
	var names []string
	for i := 0; i < MaxListed+25; i++ {
		names = append(names, "pkg"+strings.Repeat("a", i%7)+string(rune('a'+i%26))+strings.Repeat("b", i/26))
	}
	list, truncated := capped(names)
	if len(list) != MaxListed || !truncated || !slices.IsSorted(list) {
		t.Errorf("%d names, truncated %v", len(list), truncated)
	}
	if list, truncated := capped(names[:10]); len(list) != 10 || truncated {
		t.Errorf("%d names, truncated %v", len(list), truncated)
	}
	if list, _ := capped(nil); list == nil {
		t.Error("an empty list is null")
	}
}

func TestTheDiskFloorsAreTheOwnersFormulas(t *testing.T) {
	est := func(download, extra int64) Estimates {
		return Estimates{Known: true, DownloadBytes: &download, ExtraBytes: &extra}
	}
	free := map[string]FreeRead{"/var/cache/apt/archives": {Bytes: 1 << 40, Known: true}, "/usr": {Bytes: 1 << 40, Known: true}, "/": {Bytes: 1 << 40, Known: true}}
	// download 100 MB: 1.25 x 100e6 + 256 MiB = 393435456; 1.1 x 100e6 + 512 MiB = 646870912: the second
	rows := diskRows(est(100e6, 200e6), free)
	if rows[0].Need != 646870912 {
		t.Errorf("archives need %d", rows[0].Need)
	}
	// download 4 GB: 1.25 x 4e9 + 256 MiB = 5268435456; 1.1 x 4e9 + 512 MiB = 4936870912: the first
	if rows = diskRows(est(4e9, 0), free); rows[0].Need != 5268435456 {
		t.Errorf("archives need %d", rows[0].Need)
	}
	// /usr: 1.5 x 200e6 + 1 GiB = 1373741824; freed space needs only the GiB
	if rows = diskRows(est(1, 200e6), free); rows[1].Need != 1373741824 {
		t.Errorf("/usr need %d", rows[1].Need)
	}
	if rows = diskRows(est(1, -50e6), free); rows[1].Need != 1<<30 || rows[2].Need != 512<<20 {
		t.Errorf("/usr need %d, / need %d", rows[1].Need, rows[2].Need)
	}
	// exactly enough is enough, one byte less is not
	tight := map[string]FreeRead{"/var/cache/apt/archives": {Bytes: 646870912, Known: true}, "/usr": {Bytes: 1<<30 + 1, Known: true}, "/": {Bytes: 512<<20 - 1, Known: true}}
	rows = diskRows(est(100e6, 0), tight)
	if !rows[0].OK || !rows[1].OK || rows[2].OK {
		t.Errorf("rows = %+v", rows)
	}
	if rows = diskRows(Estimates{}, free); len(rows) != 0 || rows == nil {
		t.Errorf("rows without sizes = %v", rows)
	}
}

// realInputs are the inputs of the plan of the measured box, made of the real outputs of the measurement.
func realInputs(t *testing.T) Inputs {
	t.Helper()
	dist, f := ParseSolver(realOutput(t, "sandbox-paused-dist-upgrade.txt"))
	if f != nil {
		t.Fatal(f)
	}
	upgrade, f := ParseSolver(realOutput(t, "sandbox-paused-upgrade.txt"))
	if f != nil {
		t.Fatal(f)
	}
	big := FreeRead{Bytes: 1 << 40, Known: true}
	return Inputs{
		Hop: bullseyeToBookworm, Rewritten: mustRewrite(t, orderLikeTheBox(d11Box(t))...), Dist: dist, Upgrade: upgrade,
		Estimates:     ParseEstimate(estimateOutput(t, "Need to get 0 B/140 MB of archives.", "After this operation, 300 MB of additional disk space will be used."), dist.Summary, testDir),
		Facts:         map[string]PackageFacts{"fuse": {Version: "2.9.9-5", Priority: "optional"}, "libsemanage1": {Version: "3.1-1+b2", Priority: "optional"}},
		AutoInstalled: map[string]bool{"fuse": true, "libsemanage1": true}, AutoKnown: true,
		Free: map[string]FreeRead{"/var/cache/apt/archives": big, "/usr": big, "/": big}, DockerPresent: true,
		Now: time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC),
	}
}

func TestEveryRemovalIsManualWhenTheFileOfAutomaticPackagesWasNotRead(t *testing.T) {
	in := realInputs(t)
	p := Assemble(in)
	if p.Verdict != VerdictOK || len(p.Problems) != 0 || p.Removals[0].Manual || p.Removals[1].Manual {
		t.Fatalf("both are automatic: verdict %s, problems %+v", p.Verdict, p.Problems)
	}
	// the file was not read: what it would have said (both automatic) is not believed
	in.AutoKnown = false
	p = Assemble(in)
	if p.Verdict != VerdictNeedsAcceptance || !p.Removals[0].Manual || !p.Removals[1].Manual || len(p.Problems) != 2 {
		t.Errorf("verdict %s, removals %+v, problems %+v", p.Verdict, p.Removals, p.Problems)
	}
	// an architecture does not make another package of it
	in = realInputs(t)
	in.AutoInstalled = map[string]bool{"fuse": true}
	if p = Assemble(in); !p.Removals[1].Manual || p.Removals[0].Manual {
		t.Errorf("removals %+v", p.Removals)
	}
}

// The files of the box's apt configuration that the sandbox did not get are a list, sorted by file and then by reason (a fragment
// can be called apt.conf too), whatever order they were read in, and the list that was given is not touched.
func TestTheAptConfLeftOutIsASortedListOfFilesAndReasons(t *testing.T) {
	in := realInputs(t)
	in.AptConfLeftOut = []AptConfLeftOut{{"b", ConfNotSimple}, {"apt.conf", ConfTooLarge}, {"apt.conf", ConfHasDirective}, {"a", ConfNotRegular}}
	given := slices.Clone(in.AptConfLeftOut)
	p := Assemble(in)
	want := []AptConfLeftOut{{"a", ConfNotRegular}, {"apt.conf", ConfHasDirective}, {"apt.conf", ConfTooLarge}, {"b", ConfNotSimple}}
	if !slices.Equal(p.AptConfLeftOut, want) {
		t.Errorf("left out = %v, want %v", p.AptConfLeftOut, want)
	}
	if !slices.Equal(in.AptConfLeftOut, given) {
		t.Errorf("the list that was given was sorted in place: %v", in.AptConfLeftOut)
	}
	raw, err := json.Marshal(p.AptConfLeftOut)
	if err != nil || string(raw) != `[{"file":"a","reason":"not_regular"},{"file":"apt.conf","reason":"has_directive"},{"file":"apt.conf","reason":"too_large"},{"file":"b","reason":"not_simple"}]` {
		t.Errorf("json = %s, %v", raw, err)
	}
	in.AptConfLeftOut = nil
	if p := Assemble(in); p.AptConfLeftOut == nil || len(p.AptConfLeftOut) != 0 {
		t.Errorf("none is %#v: a list is a list, never null", p.AptConfLeftOut)
	}
}

// The list is cut at MaxLeftOut, after it is sorted (so it is the first files by name that are named), and the plan says that it was cut: a box with thousands of
// fragments does not put some 200 KB in every answer. At the limit it is not cut, and the field is not in the JSON when it is not.
func TestTheAptConfLeftOutIsCutAtTheLimitAndSaysSo(t *testing.T) {
	in := realInputs(t)
	var list []AptConfLeftOut
	for i := MaxLeftOut + 50; i > 0; i-- {
		list = append(list, AptConfLeftOut{File: "f" + strconv.Itoa(1000+i), Reason: ConfTooLarge})
	}
	for n, wantCut := range map[int]bool{MaxLeftOut - 1: false, MaxLeftOut: false, MaxLeftOut + 1: true, len(list): true} {
		in.AptConfLeftOut = list[:n]
		p := Assemble(in)
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.AptConfLeftOut) != min(n, MaxLeftOut) || p.AptConfLeftOutTruncated != wantCut || strings.Contains(string(raw), `"apt_conf_left_out_truncated":true`) != wantCut {
			t.Errorf("%d files: %d named, cut %v", n, len(p.AptConfLeftOut), p.AptConfLeftOutTruncated)
		}
		if n > MaxLeftOut {
			// the first by name, whatever order they came in
			sorted := slices.Clone(list[:n])
			slices.SortFunc(sorted, compareLeftOut)
			if !slices.Equal(p.AptConfLeftOut, sorted[:MaxLeftOut]) {
				t.Errorf("%d files: the list runs from %s to %s, not the first %d by name", n, p.AptConfLeftOut[0].File, p.AptConfLeftOut[MaxLeftOut-1].File, MaxLeftOut)
			}
		}
	}
}
