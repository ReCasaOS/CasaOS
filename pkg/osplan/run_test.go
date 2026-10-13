package osplan

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// ---- a sandbox that records what is asked of it ----------------------------------------------------------------------

type fakeSandbox struct {
	mu    sync.Mutex
	files *fakeFiles
	dir   string

	made, removed int
	ran           [][]string
	writes        []string
	// outside are the paths written that are not inside the directory handed out: there must be none
	outside []string
	// disallowed are the commands that AllowedSandboxArgv does not accept: there must be none
	disallowed [][]string

	outputs map[string]Output
	errs    map[string]error
	// block holds the command of a verb until its context ends
	block map[string]bool
	// panicOn makes the command of a verb panic
	panicOn  string
	makeErr  error
	writeErr error
	badDir   string
	// onRun is called when a command starts
	onRun func(verb string)
	// onMake is called when the directory has been made: the sandbox takes room on the disk from then on
	onMake func()
}

func newSandbox(files *fakeFiles, outputs map[string]Output) *fakeSandbox {
	return &fakeSandbox{files: files, dir: "/var/tmp/casaos-os-plan.481516", outputs: outputs, errs: map[string]error{}, block: map[string]bool{}}
}

func (s *fakeSandbox) MakeDir() (string, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.makeErr != nil {
		return "", nil, s.makeErr
	}
	s.made++
	dir := s.dir
	if s.badDir != "" {
		dir = s.badDir
	}
	s.files.dirs[dir] = true
	if s.onMake != nil {
		s.onMake()
	}
	return dir, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.removed++
		delete(s.files.dirs, dir)
		for p := range s.files.files {
			if strings.HasPrefix(p, dir+"/") {
				delete(s.files.files, p)
			}
		}
	}, nil
}

func (s *fakeSandbox) WriteFile(p string, content []byte, mode fs.FileMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	if !strings.HasPrefix(p, s.dir+"/") || path.Clean(p) != p {
		s.outside = append(s.outside, p)
	}
	s.writes = append(s.writes, p)
	if mode&fs.ModeDir != 0 {
		s.files.dirs[p] = true
		return nil
	}
	s.files.files[p] = string(content)
	return nil
}

func verbOf(argv []string) string {
	switch tail := strings.Join(argv[len(argv)-3:], " "); {
	case strings.HasSuffix(tail, "update --error-on=any"):
		return "update"
	case strings.HasSuffix(tail, "-s upgrade"):
		return "upgrade"
	case strings.HasSuffix(tail, "-s dist-upgrade"):
		return "dist-upgrade"
	case strings.Contains(tail, "Trivial-Only=true dist-upgrade"):
		return "estimate"
	}
	return "?"
}

func (s *fakeSandbox) Run(ctx context.Context, argv []string) (Output, error) {
	s.mu.Lock()
	s.ran = append(s.ran, slices.Clone(argv))
	if !AllowedSandboxArgv(argv, s.dir) {
		s.disallowed = append(s.disallowed, slices.Clone(argv))
	}
	verb := verbOf(argv)
	onRun, block, panicOn, err, out := s.onRun, s.block[verb], s.panicOn, s.errs[verb], s.outputs[verb]
	s.mu.Unlock()
	if onRun != nil {
		onRun(verb)
	}
	if panicOn == verb {
		panic("the sandbox panicked")
	}
	if block {
		<-ctx.Done()
		return Output{}, ctx.Err()
	}
	if err != nil {
		return Output{}, err
	}
	return out, nil
}

func (s *fakeSandbox) verbs() []string {
	var out []string
	for _, a := range s.ran {
		out = append(out, verbOf(a))
	}
	return out
}

// ---- the box -----------------------------------------------------------------------------------------------------------

const debian11OSRelease = `PRETTY_NAME="Debian GNU/Linux 11 (bullseye)"
NAME="Debian GNU/Linux"
VERSION_ID="11"
VERSION="11 (bullseye)"
VERSION_CODENAME=bullseye
ID=debian
HOME_URL="https://www.debian.org/"
`

const holdFormat = "-f=${db:Status-Abbrev}\t${Package}\t${Architecture}\t${Version}\n"

type testBox struct {
	*fakeFiles
	// installed is what dpkg-query lists: "ii \tname\tamd64\tversion\n" a line, "hi " for a hold
	installed string
	// facts are the lines of dpkg-query for the removed packages, by name: "name\tversion\tessential\tpriority"
	facts map[string]string
	// dpkgExit is the status that dpkg-query ends with when it is not 0
	dpkgExit int
}

func newBox(t *testing.T) *testBox {
	t.Helper()
	f := newFiles()
	b := &testBox{fakeFiles: f, installed: "ii \tbash\tamd64\t5.1-2+deb11u1\n", facts: map[string]string{
		"fuse":         "fuse\t2.9.9-5\t\toptional",
		"libsemanage1": "libsemanage1\t3.1-1+b2\t\toptional",
	}}
	parts := splitFixture(realFile(t, "d11-12-sources.txt"))
	f.files["/etc/os-release"] = debian11OSRelease
	f.files["/etc/apt/sources.list"] = parts["/etc/apt/sources.list"]
	f.files["/etc/apt/sources.list.d/docker.list"] = parts["/etc/apt/sources.list.d/docker.list"]
	f.files["/etc/apt/apt.conf.d/15update-stamp"] = "APT::Update::Post-Invoke-Success {\n\t\"touch /var/lib/apt/periodic/update-success-stamp 2>/dev/null || true\";\n};\n"
	f.files["/etc/apt/apt.conf.d/99proxy"] = "Acquire::http::Proxy \"http://cache.example.lan:3142\";\n"
	f.files[extendedStates] = "Package: libsemanage1\nArchitecture: amd64\nAuto-Installed: 1\n\nPackage: fuse\nArchitecture: amd64\nAuto-Installed: 0\n\nPackage: bash\nAuto-Installed: 0\n"
	f.free["/var/tmp"] = 10 << 30
	f.free["/var/cache/apt/archives"] = 8 << 30
	f.free["/usr"] = 8 << 30
	f.free["/"] = 20 << 30
	f.on("Docker version 28.0.4, build b8034c0\n", 0, "docker", "--version")
	f.fallback = func(argv []string) (osreadiness.Output, error, bool) {
		if argv[0] != "dpkg-query" || len(argv) < 3 {
			return osreadiness.Output{}, nil, false
		}
		switch {
		case argv[2] == holdFormat && len(argv) == 3:
			return osreadiness.Output{Text: b.installed}, nil, true
		case argv[2] == dpkgFormat:
			var text string
			for _, name := range argv[3:] {
				if line, ok := b.facts[name]; ok {
					text += line + "\n"
				}
			}
			code := 0
			if text == "" {
				code = 1
			}
			if b.dpkgExit != 0 {
				code = b.dpkgExit
			}
			return osreadiness.Output{Text: text, ExitCode: code}, nil, true
		}
		return osreadiness.Output{}, nil, false
	}
	return b
}

func realOutputs(t *testing.T) map[string]Output {
	t.Helper()
	return map[string]Output{
		"update":       realOutput(t, "sandbox-paused-update.txt"),
		"upgrade":      realOutput(t, "sandbox-paused-upgrade.txt"),
		"dist-upgrade": realOutput(t, "sandbox-paused-dist-upgrade.txt"),
		"estimate":     estimateOutput(t, "Need to get 0 B/140 MB of archives.", "After this operation, 300 MB of additional disk space will be used."),
	}
}

func runPlan(t *testing.T, b *testBox, sb *fakeSandbox, opts Options) (*Plan, *Failure) {
	t.Helper()
	return Run(context.Background(), b, sb, opts)
}

func mustPlan(t *testing.T, b *testBox, sb *fakeSandbox) *Plan {
	t.Helper()
	p, f := runPlan(t, b, sb, Options{})
	if f != nil {
		t.Fatalf("no plan: %v", f)
	}
	return p
}

func problemCodes(p *Plan) []string {
	var out []string
	for _, pr := range p.Problems {
		out = append(out, pr.Code)
	}
	return out
}

// ---- the tests ---------------------------------------------------------------------------------------------------------

func TestThePlanOfTheRealBox(t *testing.T) {
	b := newBox(t)
	b.step = 2 * time.Second
	sb := newSandbox(b.fakeFiles, realOutputs(t))
	var phases []string
	p, f := runPlan(t, b, sb, Options{Progress: func(phase string) { phases = append(phases, phase) }})
	if f != nil {
		t.Fatal(f)
	}
	if !slices.Equal(phases, Phases) {
		t.Errorf("phases = %v", phases)
	}
	// fuse was installed by hand, libsemanage1 by apt: the plan wants the owner to accept the one
	if p.Verdict != VerdictNeedsAcceptance || !slices.Equal(problemCodes(p), []string{ProblemRemovalManual}) {
		t.Fatalf("verdict %s, problems %+v", p.Verdict, p.Problems)
	}
	if p.Problems[0].Data["name"] != "fuse" || p.Problems[0].Data["version"] != "2.9.9-5" || p.Problems[0].Severity != SeverityAcceptance {
		t.Errorf("problem = %+v", p.Problems[0])
	}
	if p.Hop != "debian-11-12" || !ValidPlanID(p.PlanID) {
		t.Errorf("hop %q, id %q", p.Hop, p.PlanID)
	}
	if want := b.fakeFiles.now; p.MadeAt.IsZero() || !p.ExpiresAt.Equal(p.MadeAt.Add(24*time.Hour)) || p.MadeAt.After(want.Add(time.Hour)) {
		t.Errorf("made %v, expires %v", p.MadeAt, p.ExpiresAt)
	}
	if p.Counts.DistUpgrade != (Count{Upgraded: 384, New: 75, Removed: 2, KeptBack: 0}) || p.Counts.Upgrade != (Count{Upgraded: 274, New: 0, Removed: 0, KeptBack: 111}) {
		t.Errorf("counts = %+v", p.Counts)
	}
	wantRemovals := []Removal{
		{Name: "fuse", Version: "2.9.9-5", Manual: true},
		{Name: "libsemanage1", Version: "3.1-1+b2", Manual: false},
	}
	if !slices.Equal(p.Removals, wantRemovals) {
		t.Errorf("removals = %+v", p.Removals)
	}
	if len(p.NewPackages) != 75 || p.NewTruncated || len(p.KeptBack) != 0 || len(p.Autoremovable) != 18 || !slices.IsSorted(p.NewPackages) {
		t.Errorf("new %d (truncated %v), kept back %d, autoremovable %d", len(p.NewPackages), p.NewTruncated, len(p.KeptBack), len(p.Autoremovable))
	}
	// the sources: the tree of the measurement, Docker's file put aside and said so
	if want := splitFixture(realFile(t, "sandbox-paused-sources.txt"))["sources.list"]; len(p.Sources.Tree) != 1 || p.Sources.Tree[0].Content != want {
		t.Errorf("tree = %+v", p.Sources.Tree)
	}
	if len(p.Paused) != 1 || p.Paused[0].File != "docker.list" || !p.Paused[0].Docker || !p.Docker.SourcePaused || !p.Docker.Present || len(p.Docker.PackagesHeld) != 0 {
		t.Errorf("paused %+v, docker %+v", p.Paused, p.Docker)
	}
	if len(p.Holds) != 0 {
		t.Errorf("holds = %v", p.Holds)
	}
	// the sizes and the disk
	if !p.Estimates.Known || *p.Estimates.DownloadBytes != 140e6 || *p.Estimates.ExtraBytes != 300e6 {
		t.Errorf("estimates = %+v", p.Estimates)
	}
	wantDisk := []DiskRow{
		{Path: "/var/cache/apt/archives", Need: 690870912, Free: 8 << 30, OK: true},
		{Path: "/usr", Need: 1523741824, Free: 8 << 30, OK: true},
		{Path: "/", Need: 536870912, Free: 20 << 30, OK: true},
	}
	if !slices.Equal(p.Disk, wantDisk) {
		t.Errorf("disk = %+v", p.Disk)
	}
	// the hook of the box is left out of the sandbox, and named with its reason; the proxy is kept
	if !slices.Equal(p.AptConfLeftOut, []AptConfLeftOut{{File: "15update-stamp", Reason: ConfNotSimple}}) {
		t.Errorf("apt conf left out = %v", p.AptConfLeftOut)
	}
	for path := range b.files {
		if strings.Contains(path, "casaos-os-plan") {
			t.Errorf("the sandbox directory left %s", path)
		}
	}
	// the cost of the plan: what the sandbox weighed (the walk saw the files, and they are gone now), the seconds of each phase
	if p.Sandbox.Bytes <= 0 {
		t.Errorf("sandbox bytes = %d", p.Sandbox.Bytes)
	}
	for _, phase := range Phases {
		if _, ok := p.Sandbox.SecondsByPhase[phase]; !ok {
			t.Errorf("no seconds for %s: %v", phase, p.Sandbox.SecondsByPhase)
		}
	}
	// the sandbox was asked the four commands, and no other, in this order, and wrote only inside its directory
	if !slices.Equal(sb.verbs(), []string{"update", "upgrade", "dist-upgrade", "estimate"}) {
		t.Errorf("commands = %v", sb.verbs())
	}
	if len(sb.disallowed) != 0 || len(sb.outside) != 0 {
		t.Errorf("not allowed: %v; outside: %v", sb.disallowed, sb.outside)
	}
	if sb.made != 1 || sb.removed != 1 {
		t.Errorf("made %d, removed %d", sb.made, sb.removed)
	}
	// the files of the sandbox are what the spec says
	wrote := map[string]bool{}
	for _, w := range sb.writes {
		wrote[strings.TrimPrefix(w, sb.dir+"/")] = true
	}
	for _, want := range []string{"sources.list", "apt-config", "apt.conf.d/99proxy", "parts", "lists/partial", "cache/archives/partial", "apt.conf.d", "tmp"} {
		if !wrote[want] {
			t.Errorf("%s was not written (%v)", want, sb.writes)
		}
	}
	if wrote["apt.conf.d/15update-stamp"] || wrote["parts/docker.list"] {
		t.Errorf("a hook or a paused file was written: %v", sb.writes)
	}
	// the box was only read: every command it was asked is on the allow-list of the report
	for _, argv := range b.ran {
		if !osreadiness.Allowed(argv) {
			t.Errorf("the box was asked a command that is not on the allow-list: %q", argv)
		}
	}
}

func TestThePlanOfTheBoxWithEverythingFine(t *testing.T) {
	b := newBox(t)
	b.files[extendedStates] = "Package: libsemanage1\nAuto-Installed: 1\n\nPackage: fuse\nAuto-Installed: 1\n"
	p := mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
	if p.Verdict != VerdictOK || len(p.Problems) != 0 {
		t.Fatalf("verdict %s, problems %+v", p.Verdict, p.Problems)
	}
	for _, r := range p.Removals {
		if r.Manual {
			t.Errorf("%s is manual", r.Name)
		}
	}
}

func TestWhenTheFileOfAutomaticPackagesCannotBeReadEveryRemovalIsManual(t *testing.T) {
	for name, set := range map[string]func(b *testBox){
		"absent":    func(b *testBox) { delete(b.files, extendedStates) },
		"too large": func(b *testBox) { b.files[extendedStates] = strings.Repeat("x", osreadiness.MaxFileBytes+1) },
		"denied": func(b *testBox) {
			b.fsErr[extendedStates] = &fs.PathError{Op: "open", Path: extendedStates, Err: fs.ErrPermission}
		},
	} {
		b := newBox(t)
		set(b)
		p := mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
		if p.Verdict != VerdictNeedsAcceptance || !slices.Equal(problemCodes(p), []string{ProblemRemovalManual, ProblemRemovalManual}) || !p.Removals[0].Manual || !p.Removals[1].Manual {
			t.Errorf("%s: verdict %s, problems %v, removals %+v", name, p.Verdict, problemCodes(p), p.Removals)
		}
	}
}

func TestAProtectedRemovalBlocksThePlan(t *testing.T) {
	// names that the real run does not also install again: a package that is removed and installed is not removed
	for _, tc := range []struct{ name, pattern string }{
		{"docker-ce", "docker-ce"}, {"docker-buildx-plugin", "docker-*-plugin"}, {"samba-ctdb", "samba*"}, {"linux-image-5.10.0-35-cloud-amd64", "linux-image*"}, {"grub-legacy", "grub*"}, {"libc6:i386", "libc6"},
	} {
		b := newBox(t)
		outputs := realOutputs(t)
		dist := outputs["dist-upgrade"]
		dist.Text = strings.ReplaceAll(dist.Text, "Remv fuse [2.9.9-5] []", "Remv "+tc.name+" [2.9.9-5] []")
		dist.Text = strings.Replace(dist.Text, "The following packages will be REMOVED:\n  fuse libsemanage1\n", "The following packages will be REMOVED:\n  "+tc.name+" libsemanage1\n", 1)
		outputs["dist-upgrade"] = dist
		b.facts[tc.name] = baseName(tc.name) + "\t1.0\t\toptional"
		arch := ""
		if _, a, ok := strings.Cut(tc.name, ":"); ok {
			arch = "\nArchitecture: " + a
		}
		b.files[extendedStates] = "Package: " + baseName(tc.name) + arch + "\nAuto-Installed: 1\n\nPackage: libsemanage1\nAuto-Installed: 1\n"
		p := mustPlan(t, b, newSandbox(b.fakeFiles, outputs))
		if p.Verdict != VerdictBlocked || !slices.Equal(problemCodes(p), []string{ProblemRemovalProtected}) {
			t.Errorf("%s: verdict %s, problems %+v", tc.name, p.Verdict, p.Problems)
			continue
		}
		if d := p.Problems[0].Data; d["name"] != tc.name || d["pattern"] != tc.pattern || p.Problems[0].Severity != SeverityBlocked {
			t.Errorf("%s: problem %+v", tc.name, p.Problems[0])
		}
	}
}

func TestAnEssentialOrRequiredRemovalBlocksThePlanWhateverItsName(t *testing.T) {
	for name, line := range map[string]string{
		"essential": "libsemanage1\t3.1-1+b2\tyes\toptional",
		"required":  "libsemanage1\t3.1-1+b2\t\trequired",
	} {
		b := newBox(t)
		b.facts["libsemanage1"] = line
		p := mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
		if p.Verdict != VerdictBlocked || !slices.Contains(problemCodes(p), ProblemRemovalEssential) {
			t.Errorf("%s: verdict %s, problems %+v", name, p.Verdict, p.Problems)
		}
		if !p.Removals[1].Essential || p.Removals[1].Protected {
			t.Errorf("%s: removal %+v", name, p.Removals[1])
		}
	}
}

func TestAProtectedPackageThatIsUpgradedOrNewIsNormal(t *testing.T) {
	// the real run upgrades apt, systemd, libc6 and installs systemd-shared libraries and kernels: not a problem
	b := newBox(t)
	p := mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
	for _, pr := range p.Problems {
		if pr.Code == ProblemRemovalProtected {
			t.Errorf("a protected package that is not removed is a problem: %+v", pr)
		}
	}
}

func TestSomethingStillKeptBackAfterTheFullUpgradeBlocksThePlan(t *testing.T) {
	b := newBox(t)
	outputs := realOutputs(t)
	// the minimal upgrade's output, as the full one: 274 upgraded and 111 kept back
	outputs["dist-upgrade"] = outputs["upgrade"]
	outputs["estimate"] = Output{}
	p := mustPlan(t, b, newSandbox(b.fakeFiles, outputs))
	if p.Verdict != VerdictBlocked || !slices.Contains(problemCodes(p), ProblemDistKeptBack) {
		t.Fatalf("verdict %s, problems %v", p.Verdict, problemCodes(p))
	}
	for _, pr := range p.Problems {
		if pr.Code == ProblemDistKeptBack {
			if names, _ := pr.Data["names"].([]string); len(names) != 111 || !slices.Contains(names, "apt") {
				t.Errorf("kept back = %v", pr.Data["names"])
			}
		}
	}
	if len(p.KeptBack) != 111 {
		t.Errorf("kept_back = %d", len(p.KeptBack))
	}
}

func TestTooLittleRoomBlocksThePlanAndSaysWhere(t *testing.T) {
	b := newBox(t)
	b.files[extendedStates] = "Package: libsemanage1\nAuto-Installed: 1\n\nPackage: fuse\nAuto-Installed: 1\n"
	b.free["/usr"] = 1 << 30
	b.free["/var/cache/apt/archives"] = 690870912
	p := mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
	if p.Verdict != VerdictBlocked || !slices.Equal(problemCodes(p), []string{ProblemDiskShort}) {
		t.Fatalf("verdict %s, problems %+v", p.Verdict, p.Problems)
	}
	d := p.Problems[0].Data
	if d["path"] != "/usr" || d["need"] != uint64(1523741824) || d["free"] != uint64(1<<30) {
		t.Errorf("problem = %+v", d)
	}
	if p.Disk[0].OK != true || p.Disk[1].OK != false || !p.Disk[2].OK {
		t.Errorf("disk = %+v", p.Disk)
	}
	// a path that cannot be read counts as none
	b.free["/usr"] = 8 << 30
	delete(b.free, "/")
	p = mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
	if p.Verdict != VerdictBlocked || !p.Disk[2].Unknown || p.Disk[2].OK {
		t.Errorf("verdict %s, disk %+v", p.Verdict, p.Disk)
	}
}

// apt checks the room of the sandbox's own cache before it stops at the trivial operation, and says so with a line of its own
// after the two sentences of sizes: the sizes are true, and a box that is short of room is blocked by its floors as it is with
// the usual end (it was reported clean: no rows, no disk_short).
func TestAnEstimateThatEndsForLackOfRoomInTheSandboxIsStillAnEstimateAndBlocksAShortBox(t *testing.T) {
	b := newBox(t)
	b.files[extendedStates] = "Package: libsemanage1\nAuto-Installed: 1\n\nPackage: fuse\nAuto-Installed: 1\n"
	b.free["/usr"] = 1 << 30
	b.free["/var/cache/apt/archives"] = 690870912
	outputs := realOutputs(t)
	sb := newSandbox(b.fakeFiles, outputs)
	est := outputs["estimate"]
	if !strings.Contains(est.Text, trivialOnlyError) {
		t.Fatal("the estimate of the test has not the usual end")
	}
	est.Text = strings.Replace(est.Text, trivialOnlyError, lowRoomError(sb.dir), 1)
	outputs["estimate"] = est
	p := mustPlan(t, b, sb)
	if !p.Estimates.Known || *p.Estimates.DownloadBytes != 140e6 || *p.Estimates.ExtraBytes != 300e6 || p.DiskNote != "" {
		t.Fatalf("estimates = %+v, note %q", p.Estimates, p.DiskNote)
	}
	if p.Verdict != VerdictBlocked || !slices.Equal(problemCodes(p), []string{ProblemDiskShort}) {
		t.Fatalf("verdict %s, problems %+v", p.Verdict, p.Problems)
	}
	if d := p.Problems[0].Data; d["path"] != "/usr" || d["need"] != uint64(1523741824) || d["free"] != uint64(1<<30) {
		t.Errorf("problem = %+v", d)
	}
	if len(p.Disk) != 3 || !p.Disk[0].OK || p.Disk[1].OK || !p.Disk[2].OK {
		t.Errorf("disk = %+v", p.Disk)
	}
	// any other line of apt's that ends the estimate is still no estimate
	est.Text = strings.Replace(est.Text, lowRoomError(sb.dir), "E: Unable to correct problems, you have held broken packages.", 1)
	outputs["estimate"] = est
	if p := mustPlan(t, b, newSandbox(b.fakeFiles, outputs)); p.Estimates.Known || len(p.Disk) != 0 {
		t.Errorf("another error: estimates %+v, disk %+v", p.Estimates, p.Disk)
	}
}

func TestWithoutSizesThereAreNoFloors(t *testing.T) {
	for name, est := range map[string]Output{
		"another end":  {Text: "anything", ExitCode: 0},
		"cut":          {Text: realOutputs(t)["estimate"].Text, ExitCode: 100, Truncated: true},
		"another form": estimateOutput(t, "Need to get lots of archives.", "After this operation, a lot will be used."),
	} {
		b := newBox(t)
		b.free["/usr"] = 1 // would block, if there were a floor
		outputs := realOutputs(t)
		outputs["estimate"] = est
		p := mustPlan(t, b, newSandbox(b.fakeFiles, outputs))
		if p.Estimates.Known || p.Estimates.DownloadBytes != nil || p.Estimates.ExtraBytes != nil || len(p.Disk) != 0 {
			t.Errorf("%s: estimates %+v, disk %+v", name, p.Estimates, p.Disk)
		}
		if p.Verdict == VerdictBlocked {
			t.Errorf("%s: blocked by a floor that is not known: %+v", name, p.Problems)
		}
	}
	// a command that cannot be run is the same
	b := newBox(t)
	sb := newSandbox(b.fakeFiles, realOutputs(t))
	sb.errs["estimate"] = errors.New("exec: apt-get: not found")
	if p := mustPlan(t, b, sb); p.Estimates.Known {
		t.Error("estimates are known")
	}
}

func TestDockerPackagesOnHoldAreInfoAndEveryHoldIsListed(t *testing.T) {
	b := newBox(t)
	b.installed = "hi \tdocker-ce\tamd64\t5:28.0.4\nhi \tcontainerd.io\tamd64\t1.7.27\nhi \tcurl\tamd64\t7.74\nii \tbash\tamd64\t5.1\n"
	p := mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
	if !slices.Equal(p.Holds, []string{"containerd.io", "curl", "docker-ce"}) || !slices.Equal(p.Docker.PackagesHeld, []string{"containerd.io", "docker-ce"}) {
		t.Errorf("holds %v, docker %+v", p.Holds, p.Docker)
	}
	var info *Problem
	for i := range p.Problems {
		if p.Problems[i].Code == ProblemDockerHeld {
			info = &p.Problems[i]
		}
	}
	if info == nil || info.Severity != SeverityInfo || !slices.Equal(info.Data["names"].([]string), []string{"containerd.io", "docker-ce"}) {
		t.Fatalf("problems = %+v", p.Problems)
	}
	// info does not weigh: the verdict is the one of the removals
	if p.Verdict != VerdictNeedsAcceptance {
		t.Errorf("verdict = %s", p.Verdict)
	}
}

func TestEveryRefusalBeforeTheJobIsItsCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(b *testBox)
		code string
		data map[string]any
	}{
		{"not root", func(b *testBox) { b.euid = 1000 }, CodeNotRoot, nil},
		{"Debian 12", func(b *testBox) {
			b.files["/etc/os-release"] = "ID=debian\nVERSION_ID=\"12\"\nVERSION_CODENAME=bookworm\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n"
		}, CodeHopNotPlanned, map[string]any{"hop": "debian-12-13"}},
		{"Ubuntu 22.04", func(b *testBox) {
			b.files["/etc/os-release"] = "ID=ubuntu\nVERSION_ID=\"22.04\"\nVERSION_CODENAME=jammy\nPRETTY_NAME=\"Ubuntu 22.04.5 LTS\"\n"
		}, CodeHopNotPlanned, map[string]any{"hop": "ubuntu-22.04-24.04"}},
		{"Debian 13, the latest", func(b *testBox) {
			b.files["/etc/os-release"] = "ID=debian\nVERSION_ID=\"13\"\nVERSION_CODENAME=trixie\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n"
		}, CodeIdentityUnsupported, nil},
		{"Fedora", func(b *testBox) { b.files["/etc/os-release"] = "ID=fedora\nVERSION_ID=\"40\"\n" }, CodeIdentityUnsupported, nil},
		{"no os-release", func(b *testBox) { delete(b.files, "/etc/os-release") }, CodeIdentityUnsupported, nil},
		{"a variant", func(b *testBox) { b.files["/usr/bin/raspi-config"] = "x"; b.files["/etc/rpi-issue"] = "x" }, CodeIdentityUnsupported, nil},
		{"not amd64", func(b *testBox) { b.arch = "arm64" }, CodeIdentityUnsupported, nil},
		{"busy", func(b *testBox) { b.busy = "unattended-upgrades is running" }, CodeBusy, nil},
		{"busy without being able to see", func(b *testBox) { b.busyErr = errors.New("the lock cannot be looked at") }, CodeBusy, map[string]any{"unknown": true}},
		{"no room in /var/tmp", func(b *testBox) { b.free["/var/tmp"] = 639 << 20 }, CodeNoRoom, nil},
		{"/var/tmp cannot be read", func(b *testBox) { delete(b.free, "/var/tmp") }, CodeNoRoom, map[string]any{"unknown": true}},
	} {
		b := newBox(t)
		tc.set(b)
		sb := newSandbox(b.fakeFiles, realOutputs(t))
		_, f := Preflight(context.Background(), b)
		if f == nil || f.Code != tc.code {
			t.Errorf("%s: Preflight = %v, want %s", tc.name, f, tc.code)
			continue
		}
		for k, v := range tc.data {
			if f.Data[k] != v {
				t.Errorf("%s: data[%s] = %v, want %v", tc.name, k, f.Data[k], v)
			}
		}
		_, rf := runPlan(t, b, sb, Options{})
		if rf == nil || rf.Code != tc.code || rf.Phase != PhaseSources {
			t.Errorf("%s: Run = %v, want %s in %s", tc.name, rf, tc.code, PhaseSources)
		}
		if sb.made != 0 || len(sb.ran) != 0 {
			t.Errorf("%s: the sandbox was used", tc.name)
		}
	}
	// the quick question does not ask apt's lock or the room: busy and no_room are the POST's, not the GET's
	b := newBox(t)
	b.busy = "something"
	b.free["/var/tmp"] = 0
	if hop, f := Plannable(context.Background(), b); f != nil || hop.ID != "debian-11-12" {
		t.Errorf("Plannable = %+v, %v", hop, f)
	}
}

func TestBusyIsLookedAtAgainRightBeforeTheUpdate(t *testing.T) {
	b := newBox(t)
	b.busySeq = []string{"", "dpkg holds its lock"}
	sb := newSandbox(b.fakeFiles, realOutputs(t))
	_, f := runPlan(t, b, sb, Options{})
	if f == nil || f.Code != CodeBusy || f.Phase != PhaseUpdate {
		t.Fatalf("failure = %v", f)
	}
	if len(sb.ran) != 0 {
		t.Errorf("the sandbox ran %v", sb.verbs())
	}
	if sb.made != 1 || sb.removed != 1 {
		t.Errorf("made %d, removed %d", sb.made, sb.removed)
	}
}

func TestEverySourcesRefusalEndsTheJobBeforeTheSandbox(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		code  string
	}{
		{"deb822", map[string]string{"/etc/apt/sources.list.d/debian.sources": "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bullseye\n"}, CodeSourcesFormat},
		{"a disc", map[string]string{"/etc/apt/sources.list": "deb cdrom:[Debian 11]/ bullseye main\n"}, CodeSourcesCDROM},
		{"an alias", map[string]string{"/etc/apt/sources.list": "deb http://deb.debian.org/debian stable main\n"}, CodeSourcesAlias},
		{"already bookworm", map[string]string{"/etc/apt/sources.list": "deb http://deb.debian.org/debian bookworm main\n"}, CodeSourcesTarget},
		{"a mixed file", map[string]string{
			"/etc/apt/sources.list.d/mix.list": "deb http://deb.debian.org/debian bullseye main\ndeb https://vendor.example.com/x stable main\n",
			// apt has kept the Release file of the vendor's suite: it is known not to be the distribution's
			"/var/lib/apt/lists/vendor.example.com_x_dists_stable_InRelease": "Origin: Vendor\n",
		}, CodeSourcesMixedFile},
		{"a third party that apt has no Release file of", map[string]string{"/etc/apt/sources.list": "deb http://deb.debian.org/debian bullseye main\ndeb https://vendor.example.com/x stable main\n"}, CodeSourcesFormat},
	} {
		b := newBox(t)
		for p, c := range tc.files {
			b.files[p] = c
		}
		sb := newSandbox(b.fakeFiles, realOutputs(t))
		_, f := runPlan(t, b, sb, Options{})
		if f == nil || f.Code != tc.code || f.Phase != PhaseSources {
			t.Errorf("%s: failure = %v, want %s", tc.name, f, tc.code)
		}
		if sb.made != 0 {
			t.Errorf("%s: a sandbox was made", tc.name)
		}
	}
}

func TestAFailedUpdateIsSaidWithItsKindAndTheSandboxIsRemoved(t *testing.T) {
	b := newBox(t)
	outputs := realOutputs(t)
	outputs["update"] = Output{Text: "Err:1 http://deb.debian.org/debian bookworm InRelease\n  The following signatures couldn't be verified because the public key is not available: NO_PUBKEY 0E98404D386FA1D9\nE: The repository 'http://deb.debian.org/debian bookworm InRelease' is not signed.\n", ExitCode: 100}
	sb := newSandbox(b.fakeFiles, outputs)
	_, f := runPlan(t, b, sb, Options{})
	if f == nil || f.Code != CodeSandboxUpdateFailed || f.Phase != PhaseUpdate || f.Data["kind"] != KindNoPubkey {
		t.Fatalf("failure = %+v", f)
	}
	if !slices.Equal(sb.verbs(), []string{"update"}) || sb.made != 1 || sb.removed != 1 {
		t.Errorf("commands %v, made %d, removed %d", sb.verbs(), sb.made, sb.removed)
	}
}

func TestASolverThatFailsEndsInItsPhase(t *testing.T) {
	for _, verb := range []string{"upgrade", "dist-upgrade"} {
		b := newBox(t)
		outputs := realOutputs(t)
		outputs[verb] = Output{Text: "E: Unable to correct problems, you have held broken packages.\n", ExitCode: 100}
		sb := newSandbox(b.fakeFiles, outputs)
		_, f := runPlan(t, b, sb, Options{})
		if f == nil || f.Code != CodeSandboxSolverFailed || f.Phase != verb {
			t.Errorf("%s: failure = %+v", verb, f)
		}
		if sb.made != 1 || sb.removed != 1 {
			t.Errorf("%s: made %d, removed %d", verb, sb.made, sb.removed)
		}
	}
}

func TestAnOutputThatCannotBeReadIsPlanUnparsedInTheParsePhase(t *testing.T) {
	b := newBox(t)
	outputs := realOutputs(t)
	outputs["dist-upgrade"] = Output{Text: strings.Replace(outputs["dist-upgrade"].Text, "384 upgraded", "385 upgraded", 1)}
	sb := newSandbox(b.fakeFiles, outputs)
	_, f := runPlan(t, b, sb, Options{})
	if f == nil || f.Code != CodePlanUnparsed || f.Phase != PhaseParse {
		t.Fatalf("failure = %+v", f)
	}
	if sb.removed != 1 {
		t.Errorf("removed %d", sb.removed)
	}
}

func TestADpkgThatDoesNotKnowARemovedPackageIsNotAPlan(t *testing.T) {
	b := newBox(t)
	delete(b.facts, "fuse")
	_, f := runPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)), Options{})
	if f == nil || f.Code != CodePlanUnparsed || f.Data["name"] != "fuse" {
		t.Fatalf("failure = %+v", f)
	}
}

// What the sandbox did not get of the box's apt configuration (a brace-style proxy, for one) is the first thing to look at when apt cannot reach its mirror, or cannot work out
// a plan: the failures that it can explain carry the plan's own list (the same entries and reasons, in the same order, cut at the same place), and the others do not.
func TestAFailureThatTheLeftOutConfigurationCanExplainCarriesTheList(t *testing.T) {
	wantList := []AptConfLeftOut{{File: "15update-stamp", Reason: ConfNotSimple}, {File: "99proxy", Reason: ConfNotSimple}}
	for _, tc := range []struct {
		name string
		set  func(b *testBox, sb *fakeSandbox)
		code string
		with bool
	}{
		{"the update cannot reach a mirror", func(_ *testBox, sb *fakeSandbox) {
			sb.outputs["update"] = Output{Text: "Err:1 http://deb.debian.org/debian bookworm InRelease\n  Could not resolve 'deb.debian.org'\nE: Failed to fetch\n", ExitCode: 100}
		}, CodeSandboxUpdateFailed, true},
		{"the update cannot be run", func(_ *testBox, sb *fakeSandbox) { sb.errs["update"] = errors.New("fork/exec: no such file") }, CodeSandboxUpdateFailed, true},
		{"the update ends in a shape that is not known", func(_ *testBox, sb *fakeSandbox) {
			sb.outputs["update"] = Output{Text: "Get:1 http://x InRelease\n", Truncated: true}
		}, CodePlanUnparsed, true},
		{"the minimal upgrade fails", func(_ *testBox, sb *fakeSandbox) {
			sb.outputs["upgrade"] = Output{Text: "E: Unable to correct problems.\n", ExitCode: 100}
		}, CodeSandboxSolverFailed, true},
		{"the full upgrade cannot be run", func(_ *testBox, sb *fakeSandbox) { sb.errs["dist-upgrade"] = errors.New("fork/exec: no such file") }, CodeSandboxSolverFailed, true},
		{"an output that is not understood", func(_ *testBox, sb *fakeSandbox) {
			sb.outputs["dist-upgrade"] = Output{Text: strings.Replace(sb.outputs["dist-upgrade"].Text, "384 upgraded", "385 upgraded", 1)}
		}, CodePlanUnparsed, true},
		{"dpkg does not know a package that apt removes", func(b *testBox, _ *fakeSandbox) { delete(b.facts, "fuse") }, CodePlanUnparsed, true},
		// a time that is over, a failure that is the core's own: no configuration explains them
		{"a time that is over", func(_ *testBox, sb *fakeSandbox) { sb.block["upgrade"] = true }, CodeTimeout, false},
		{"a panic", func(_ *testBox, sb *fakeSandbox) { sb.panicOn = "dist-upgrade" }, CodeInternal, false},
	} {
		b := newBox(t)
		b.files["/etc/apt/apt.conf.d/99proxy"] = "Acquire { http { Proxy \"http://cache.example.lan:3142\"; }; };\n"
		sb := newSandbox(b.fakeFiles, realOutputs(t))
		tc.set(b, sb)
		_, f := runPlan(t, b, sb, Options{Timeouts: Timeouts{Solver: 50 * time.Millisecond}})
		if f == nil || f.Code != tc.code {
			t.Errorf("%s: failure %+v, want %s", tc.name, f, tc.code)
			continue
		}
		got, has := f.Data[DataConfLeftOut]
		if has != tc.with {
			t.Errorf("%s: the list is there: %v, want %v", tc.name, has, tc.with)
		}
		if tc.with {
			if list, _ := got.([]AptConfLeftOut); !slices.Equal(list, wantList) {
				t.Errorf("%s: the list is %#v, want %v", tc.name, got, wantList)
			}
			if _, cut := f.Data[DataConfLeftOutTruncated]; cut {
				t.Errorf("%s: the list is said to be cut", tc.name)
			}
			// and it goes out as JSON as the plan's own does
			raw, err := json.Marshal(f)
			if err != nil || !strings.Contains(string(raw), `"apt_conf_left_out":[{"file":"15update-stamp","reason":"not_simple"},{"file":"99proxy","reason":"not_simple"}]`) {
				t.Errorf("%s: %s (%v)", tc.name, raw, err)
			}
		}
	}
	// a box that has nothing left out has no list to say (the data of a failure does not carry an empty one)
	b := newBox(t)
	delete(b.files, "/etc/apt/apt.conf.d/15update-stamp")
	sb := newSandbox(b.fakeFiles, realOutputs(t))
	sb.outputs["update"] = Output{Text: "E: x\n", ExitCode: 100}
	if _, f := runPlan(t, b, sb, Options{}); f == nil || f.Code != CodeSandboxUpdateFailed || len(f.Data) != 2 {
		t.Errorf("nothing left out: %+v", f)
	} else if _, has := f.Data[DataConfLeftOut]; has {
		t.Errorf("nothing left out, and a list: %+v", f.Data)
	}
	// more than MaxLeftOut: the list is cut where the plan's is, and the cut is said
	b = newBox(t)
	for i := 0; i < MaxLeftOut+5; i++ {
		b.files["/etc/apt/apt.conf.d/50f"+strconv.Itoa(1000+i)] = "Acquire { http { Proxy \"x\"; }; };\n"
	}
	sb = newSandbox(b.fakeFiles, realOutputs(t))
	sb.outputs["update"] = Output{Text: "E: x\n", ExitCode: 100}
	_, f := runPlan(t, b, sb, Options{})
	if f == nil {
		t.Fatal("no failure")
	}
	if list, _ := f.Data[DataConfLeftOut].([]AptConfLeftOut); len(list) != MaxLeftOut || f.Data[DataConfLeftOutTruncated] != true {
		t.Errorf("%d entries, cut %v", len(list), f.Data[DataConfLeftOutTruncated])
	}
}

// each way out of Run removes the sandbox directory, once
func TestTheSandboxDirectoryIsRemovedOnEveryWayOut(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(b *testBox, sb *fakeSandbox)
		code string
		made int
		// cause is what the log of the core is to have of an internal failure (the page has none of it)
		cause string
	}{
		{"success", func(*testBox, *fakeSandbox) {}, "", 1, ""},
		{"an update that fails", func(_ *testBox, sb *fakeSandbox) { sb.outputs["update"] = Output{Text: "E: x\n", ExitCode: 100} }, CodeSandboxUpdateFailed, 1, ""},
		{"an apt that cannot be run", func(_ *testBox, sb *fakeSandbox) { sb.errs["upgrade"] = errors.New("fork/exec: no such file") }, CodeSandboxSolverFailed, 1, ""},
		{"a panic in a command", func(_ *testBox, sb *fakeSandbox) { sb.panicOn = "dist-upgrade" }, CodeInternal, 1, "the sandbox panicked"},
		{"a panic in the update", func(_ *testBox, sb *fakeSandbox) { sb.panicOn = "update" }, CodeInternal, 1, "the sandbox panicked"},
		{"a directory that is not named as one", func(_ *testBox, sb *fakeSandbox) { sb.badDir = "/tmp/elsewhere" }, CodeInternal, 1, "/tmp/elsewhere"},
		{"a file that cannot be written", func(_ *testBox, sb *fakeSandbox) { sb.writeErr = errors.New("disk full") }, CodeInternal, 1, "disk full"},
		{"a time that is over", func(_ *testBox, sb *fakeSandbox) { sb.block["upgrade"] = true }, CodeTimeout, 1, ""},
		{"a directory that cannot be made", func(_ *testBox, sb *fakeSandbox) { sb.makeErr = errors.New("no space") }, CodeInternal, 0, "no space"},
	} {
		b := newBox(t)
		sb := newSandbox(b.fakeFiles, realOutputs(t))
		tc.set(b, sb)
		_, f := runPlan(t, b, sb, Options{Timeouts: Timeouts{Solver: 50 * time.Millisecond}})
		switch {
		case tc.code == "" && f != nil:
			t.Errorf("%s: failure %v", tc.name, f)
		case tc.code != "" && (f == nil || f.Code != tc.code):
			t.Errorf("%s: failure %v, want %s", tc.name, f, tc.code)
		}
		if sb.made != tc.made || sb.removed != tc.made {
			t.Errorf("%s: made %d, removed %d", tc.name, sb.made, sb.removed)
		}
		for p := range b.files {
			if strings.Contains(p, "casaos-os-plan") {
				t.Errorf("%s: %s is left", tc.name, p)
			}
		}
		// a directory that is not a sandbox's is not written to, and no command is run in it
		if tc.name == "a directory that is not named as one" && (len(sb.writes) != 0 || len(sb.ran) != 0) {
			t.Errorf("%s: written %v, ran %v", tc.name, sb.writes, sb.ran)
		}
		if f != nil && strings.Contains(f.Detail, "goroutine") {
			t.Errorf("%s: a stack in the detail: %q", tc.name, f.Detail)
		}
		// an internal failure keeps what went wrong for the log, and never in what the page is told
		if tc.cause != "" && (f == nil || f.Cause == nil || !strings.Contains(f.Cause.Error(), tc.cause)) {
			t.Errorf("%s: failure %+v, want a cause that says %q", tc.name, f, tc.cause)
		}
		if f != nil && tc.cause != "" && strings.Contains(f.Detail, tc.cause) {
			t.Errorf("%s: the detail says the cause: %q", tc.name, f.Detail)
		}
		if raw, _ := json.Marshal(f); f != nil && strings.Contains(string(raw), "cause") {
			t.Errorf("%s: the cause is in the JSON: %s", tc.name, raw)
		}
		if tc.name == "a panic in a command" && !strings.Contains(f.Cause.Error(), "goroutine") {
			t.Errorf("%s: the cause of a panic has no stack: %v", tc.name, f.Cause)
		}
		if tc.cause == "" && f != nil && f.Cause != nil {
			t.Errorf("%s: a failure that is not internal has a cause: %v", tc.name, f.Cause)
		}
	}
}

func TestCancelingThePlanStopsTheCommandAndRemovesTheSandbox(t *testing.T) {
	b := newBox(t)
	sb := newSandbox(b.fakeFiles, realOutputs(t))
	sb.block["update"] = true
	ctx, cancel := context.WithCancel(context.Background())
	sb.onRun = func(verb string) {
		if verb == "update" {
			go func() { time.Sleep(20 * time.Millisecond); cancel() }()
		}
	}
	_, f := Run(ctx, b, sb, Options{})
	if f == nil || f.Code != CodeCanceled || f.Phase != PhaseUpdate {
		t.Fatalf("failure = %+v", f)
	}
	if sb.made != 1 || sb.removed != 1 {
		t.Errorf("made %d, removed %d", sb.made, sb.removed)
	}
}

func TestTheUpdateHasTenMinutesAndTheSolverTwo(t *testing.T) {
	b := newBox(t)
	sb := newSandbox(b.fakeFiles, realOutputs(t))
	seen := map[string]time.Duration{}
	sb2 := &deadlineSandbox{fakeSandbox: sb, seen: seen}
	if _, f := Run(context.Background(), b, sb2, Options{}); f != nil {
		t.Fatal(f)
	}
	if got := seen["update"]; got < 9*time.Minute+30*time.Second || got > 10*time.Minute {
		t.Errorf("update has %v", got)
	}
	for _, verb := range []string{"upgrade", "dist-upgrade", "estimate"} {
		if got := seen[verb]; got < time.Minute+50*time.Second || got > 2*time.Minute {
			t.Errorf("%s has %v", verb, got)
		}
	}
}

type deadlineSandbox struct {
	*fakeSandbox
	seen map[string]time.Duration
}

func (d *deadlineSandbox) Run(ctx context.Context, argv []string) (Output, error) {
	if dl, ok := ctx.Deadline(); ok {
		d.fakeSandbox.mu.Lock()
		d.seen[verbOf(argv)] = time.Until(dl)
		d.fakeSandbox.mu.Unlock()
	}
	return d.fakeSandbox.Run(ctx, argv)
}

func TestARunThatIsNotOnTheAllowListNeverReachesTheSandbox(t *testing.T) {
	b := newBox(t)
	sb := newSandbox(b.fakeFiles, realOutputs(t))
	// the directory is valid for the sandbox, so every command is: the check is the one of runInSandbox
	_, f := runInSandbox(context.Background(), sb, sb.dir, []string{"apt-get", "-y", "install", "curl"}, time.Second, PhaseUpdate)
	if f == nil || f.Code != CodeInternal || len(sb.ran) != 0 {
		t.Fatalf("failure = %+v, ran %v", f, sb.ran)
	}
}

func TestTheSandboxIsTheOnlyThingWrittenAndTheBoxIsOnlyRead(t *testing.T) {
	b := newBox(t)
	before := map[string]string{}
	for p, c := range b.files {
		before[p] = c
	}
	sb := newSandbox(b.fakeFiles, realOutputs(t))
	mustPlan(t, b, sb)
	for p, c := range before {
		if got, ok := b.files[p]; !ok || got != c {
			t.Errorf("%s changed", p)
		}
	}
	if len(b.files) != len(before) {
		t.Errorf("%d files before, %d after", len(before), len(b.files))
	}
}

func TestAFileOutsideTheSandboxIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	b := newBox(t)
	sb := newSandbox(b.fakeFiles, realOutputs(t))
	files := []SandboxFile{
		{Path: sb.dir + "/sources.list", Content: []byte("x"), Mode: 0o600},
		{Path: sb.dir + "/../escape", Content: []byte("x"), Mode: 0o600},
	}
	if f := writeSandbox(context.Background(), sb, sb.dir, files); f == nil || f.Code != CodeInternal {
		t.Fatalf("failure = %v", f)
	}
	if len(sb.writes) != 0 {
		t.Errorf("written before the refusal: %v", sb.writes)
	}
	for _, p := range []string{"/etc/passwd", sb.dir, sb.dir + "/", sb.dir + "/a/../b", "/var/tmp/casaos-os-plan.481516x/a", "relative"} {
		if f := writeSandbox(context.Background(), sb, sb.dir, []SandboxFile{{Path: p}}); f == nil {
			t.Errorf("%q is accepted", p)
		}
	}
	if f := writeSandbox(context.Background(), sb, sb.dir, []SandboxFile{{Path: sb.dir + "/lists/partial", Mode: 0o700}}); f != nil {
		t.Errorf("a path inside is refused: %v", f)
	}
}

func TestAPlanWithNothingToSayHasAListOfProblemsAndNotNull(t *testing.T) {
	b := newBox(t)
	b.files[extendedStates] = "Package: libsemanage1\nAuto-Installed: 1\n\nPackage: fuse\nAuto-Installed: 1\n"
	p := mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"problems":[]`) {
		t.Errorf("problems in %s", raw)
	}
}

func TestATimeOrACancelDuringTheEstimateIsAnEndAndNotAnUnknownSize(t *testing.T) {
	b := newBox(t)
	sb := newSandbox(b.fakeFiles, realOutputs(t))
	sb.block["estimate"] = true
	_, f := runPlan(t, b, sb, Options{Timeouts: Timeouts{Solver: 50 * time.Millisecond}})
	if f == nil || f.Code != CodeTimeout || f.Phase != PhaseEstimate {
		t.Fatalf("failure = %+v", f)
	}
	if sb.made != 1 || sb.removed != 1 {
		t.Errorf("made %d, removed %d", sb.made, sb.removed)
	}
	b = newBox(t)
	sb = newSandbox(b.fakeFiles, realOutputs(t))
	sb.block["estimate"] = true
	ctx, cancel := context.WithCancel(context.Background())
	sb.onRun = func(verb string) {
		if verb == "estimate" {
			go func() { time.Sleep(20 * time.Millisecond); cancel() }()
		}
	}
	_, f = Run(ctx, b, sb, Options{})
	if f == nil || f.Code != CodeCanceled || f.Phase != PhaseEstimate {
		t.Fatalf("failure = %+v", f)
	}
}

// A box with nothing to upgrade, as real apt says it (a Debian 11 chroot with an empty list of sources: testdata/real/chroot-d11-nothing-*): the estimate is the one that ends with
// status 0, and the plan has its sizes, known and zero, and the rows of the disk that the floors make of them: it does not say that the sizes are not known.
func TestAPlanOfABoxWithNothingToDoHasSizesThatAreKnownAndZero(t *testing.T) {
	b := newBox(t)
	outputs := realOutputs(t)
	for verb, file := range map[string]string{"update": "update", "upgrade": "upgrade", "dist-upgrade": "dist-upgrade", "estimate": "size"} {
		outputs[verb] = realOutput(t, "chroot-d11-nothing-"+file+".txt")
	}
	p := mustPlan(t, b, newSandbox(b.fakeFiles, outputs))
	if !p.Estimates.Known || p.Estimates.DownloadBytes == nil || p.Estimates.ExtraBytes == nil || *p.Estimates.DownloadBytes != 0 || *p.Estimates.ExtraBytes != 0 {
		t.Fatalf("estimates = %+v", p.Estimates)
	}
	if p.DiskNote != "" || len(p.Disk) != len(diskPaths) {
		t.Errorf("disk note %q, %d rows", p.DiskNote, len(p.Disk))
	}
	if p.Counts.DistUpgrade != (Count{}) {
		t.Errorf("counts = %+v", p.Counts)
	}
	// The floors of zero sizes are the constants of the formulas (512 MiB for the packages, 1 GiB in /usr, 512 MiB in /), and they judge the box: a roomy one is fine ...
	floors := []uint64{512 << 20, 1 << 30, 512 << 20}
	for i, row := range p.Disk {
		if row.Path != diskPaths[i] || row.Need != floors[i] || !row.OK {
			t.Errorf("roomy box, row %d = %+v, want %s needing %d, with room", i, row, diskPaths[i], floors[i])
		}
	}
	if p.Verdict != VerdictOK || len(p.Problems) != 0 {
		t.Errorf("a roomy box with nothing to do: verdict %s, problems %+v", p.Verdict, p.Problems)
	}
	// ... and a tight one is blocked by them, in each of the three places, with what each needed and what there was: no size, and no floor, would let it through
	b.free["/usr"] = 900 << 20
	b.free["/var/cache/apt/archives"] = 400 << 20
	b.free["/"] = 400 << 20
	p = mustPlan(t, b, newSandbox(b.fakeFiles, outputs))
	if p.Verdict != VerdictBlocked || !slices.Equal(problemCodes(p), []string{ProblemDiskShort, ProblemDiskShort, ProblemDiskShort}) {
		t.Fatalf("a tight box with nothing to do: verdict %s, problems %+v", p.Verdict, p.Problems)
	}
	free := []uint64{400 << 20, 900 << 20, 400 << 20}
	for i, pr := range p.Problems {
		if pr.Data["path"] != diskPaths[i] || pr.Data["need"] != floors[i] || pr.Data["free"] != free[i] {
			t.Errorf("problem %d = %+v, want %s needing %d with %d free", i, pr.Data, diskPaths[i], floors[i], free[i])
		}
		if p.Disk[i].Need != floors[i] || p.Disk[i].OK {
			t.Errorf("row %d = %+v", i, p.Disk[i])
		}
	}
}
