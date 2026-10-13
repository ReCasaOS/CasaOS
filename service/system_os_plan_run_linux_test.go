//go:build linux

package service

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The plan of the box of the measurement, made by osplan.Run through the production sandbox (its real directory in /var/tmp, its
// real files, its real process groups), with a stand-in for apt-get that prints, for the update and the two simulations, what apt
// printed on the real guest of the measurement, and a box that is a map of files (the sandbox's own directories, which the plan
// walks to say what they weigh, are the real ones). What the stand-in prints for the estimate (the Trivial-Only run) is NOT from
// that guest, which was never asked it: it is the guest's dist-upgrade and the two sentences as the hop's own log has them, with
// sizes of the test's own. The real output of that run is the one apt 2.2.4 gave in a Debian 11 chroot taken to bookworm, which
// testdata/real/chroot-d11-12-size.txt keeps and pkg/osplan parses (TestTheEstimateOfARealTrivialOnlyRunOfApt224).

// planFiles is the box: the files of the Debian 11 of the measurement, and the real files of the sandbox directories.
type planFiles struct {
	mu    sync.Mutex
	files map[string]string
	free  map[string]uint64
	clock time.Time
	// panicOn is a path whose file, when it is read, makes the box panic
	panicOn string
}

func isSandboxPath(p string) bool {
	return strings.HasPrefix(p, osplan.SandboxParent+"/"+osplan.SandboxPrefix)
}

func (f *planFiles) isDir(p string) bool {
	prefix := strings.TrimSuffix(p, "/") + "/"
	for name := range f.files {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func (f *planFiles) ReadFile(_ context.Context, p string) ([]byte, error) {
	if f.panicOn == p {
		panic("the box panicked")
	}
	if isSandboxPath(p) {
		return os.ReadFile(p)
	}
	if c, ok := f.files[p]; ok {
		return []byte(c), nil
	}
	return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
}

func (f *planFiles) ReadHead(ctx context.Context, p string, max int) ([]byte, error) {
	b, err := f.ReadFile(ctx, p)
	if err == nil && len(b) > max {
		b = b[:max]
	}
	return b, err
}

func (f *planFiles) ReadDir(_ context.Context, p string) ([]string, error) {
	if isSandboxPath(p) {
		entries, err := os.ReadDir(p)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names, err
	}
	prefix := strings.TrimSuffix(p, "/") + "/"
	seen := map[string]bool{}
	for name := range f.files {
		if rest, ok := strings.CutPrefix(name, prefix); ok {
			first, _, _ := strings.Cut(rest, "/")
			seen[first] = true
		}
	}
	if len(seen) == 0 {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	var names []string
	for n := range seen {
		names = append(names, n)
	}
	slices.Sort(names)
	return names, nil
}

func (f *planFiles) stat(p string, follow bool) (osreadiness.FileInfo, error) {
	if isSandboxPath(p) {
		info, err := os.Lstat(p)
		if follow {
			info, err = os.Stat(p)
		}
		return fileInfo(info, err)
	}
	if c, ok := f.files[p]; ok {
		return osreadiness.FileInfo{Size: int64(len(c)), Mode: 0o644}, nil
	}
	if f.isDir(p) {
		return osreadiness.FileInfo{Mode: fs.ModeDir | 0o755}, nil
	}
	return osreadiness.FileInfo{}, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
}

func (f *planFiles) Stat(_ context.Context, p string) (osreadiness.FileInfo, error) {
	return f.stat(p, true)
}
func (f *planFiles) Lstat(_ context.Context, p string) (osreadiness.FileInfo, error) {
	return f.stat(p, false)
}
func (f *planFiles) Readlink(_ context.Context, p string) (string, error) {
	return "", &fs.PathError{Op: "readlink", Path: p, Err: fs.ErrNotExist}
}

func (f *planFiles) Run(_ context.Context, argv ...string) (osreadiness.Output, error) {
	switch {
	case argv[0] == "docker" && len(argv) == 2 && argv[1] == "--version":
		return osreadiness.Output{Text: "Docker version 28.0.4, build b8034c0\n"}, nil
	case argv[0] == "dpkg-query" && len(argv) == 3:
		// the packages on the box, none on hold
		return osreadiness.Output{Text: "ii \tbash\tamd64\t5.1-2+deb11u1\n"}, nil
	case argv[0] == "dpkg-query":
		var text string
		for _, name := range argv[3:] {
			switch name {
			case "fuse":
				text += "fuse\t2.9.9-5\t\toptional\n"
			case "libsemanage1":
				text += "libsemanage1\t3.1-1+b2\t\toptional\n"
			}
		}
		return osreadiness.Output{Text: text}, nil
	}
	return osreadiness.Output{}, osreadiness.ErrToolMissing
}

func (f *planFiles) FreeBytes(_ context.Context, p string) (uint64, error) {
	if v, ok := f.free[p]; ok {
		return v, nil
	}
	return 0, &fs.PathError{Op: "statfs", Path: p, Err: fs.ErrNotExist}
}

func (f *planFiles) MaintenanceBusy(context.Context) (string, bool, error) { return "", false, nil }
func (f *planFiles) AppOperations(context.Context) ([]string, error)       { return nil, nil }
func (f *planFiles) EUID() int                                             { return 0 }
func (f *planFiles) Arch() string                                          { return "amd64" }
func (f *planFiles) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = f.clock.Add(time.Second)
	return f.clock
}

// splitFixtureFiles reads a file of the measurement that is several files, each after a "==> path <==" line.
func splitFixtureFiles(text string) map[string]string {
	files := map[string]string{}
	name := ""
	for _, line := range strings.SplitAfter(text, "\n") {
		if trimmed := strings.TrimRight(line, "\n"); strings.HasPrefix(trimmed, "==> ") && strings.HasSuffix(trimmed, " <==") {
			name = strings.TrimSuffix(strings.TrimPrefix(trimmed, "==> "), " <==")
			files[name] = ""
			continue
		}
		files[name] += line
	}
	return files
}

func newPlanFiles(t *testing.T) *planFiles {
	t.Helper()
	data, err := os.ReadFile(realFixture(t, "d11-12-sources.txt"))
	if err != nil {
		t.Fatal(err)
	}
	f := &planFiles{
		files: splitFixtureFiles(strings.ReplaceAll(string(data), "\r\n", "\n")),
		free: map[string]uint64{
			osplan.SandboxParent: 10 << 30, "/var/cache/apt/archives": 8 << 30, "/usr": 8 << 30, "/": 20 << 30,
		},
		clock: time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC),
	}
	f.files["/etc/os-release"] = "PRETTY_NAME=\"Debian GNU/Linux 11 (bullseye)\"\nNAME=\"Debian GNU/Linux\"\nVERSION_ID=\"11\"\nVERSION=\"11 (bullseye)\"\nVERSION_CODENAME=bullseye\nID=debian\nHOME_URL=\"https://www.debian.org/\"\n"
	f.files["/etc/apt/apt.conf.d/15update-stamp"] = "APT::Update::Post-Invoke-Success {\n\t\"touch /var/lib/apt/periodic/update-success-stamp 2>/dev/null || true\";\n};\n"
	f.files["/etc/apt/apt.conf.d/99proxy"] = "Acquire::http::Proxy \"http://cache.example.lan:3142\";\n"
	f.files["/var/lib/apt/extended_states"] = "Package: libsemanage1\nArchitecture: amd64\nAuto-Installed: 1\n\nPackage: fuse\nArchitecture: amd64\nAuto-Installed: 0\n"
	return f
}

// aptOnTheGuest is a stand-in for apt-get that prints what apt printed on the real guest for the update and the two simulations
// (not for the estimate: see the top of this file), and keeps a log of what it saw: the directory it runs in (TMPDIR is its tmp
// directory, with its mode), the verbs in order, and (at the update) the tree of the sandbox and its files.
// behave is shell that runs at the start of the command for a verb ("" for none): what the test wants this verb to do instead.
func aptOnTheGuest(t *testing.T, logs string, behave map[string]string) string {
	t.Helper()
	update := realFixture(t, "sandbox-paused-update.txt")
	upgrade := realFixture(t, "sandbox-paused-upgrade.txt")
	dist := realFixture(t, "sandbox-paused-dist-upgrade.txt")
	script := `
LOGS='` + logs + `'
verb=other
case " $* " in
*"Trivial-Only=true "*) verb=estimate ;;
*" update "*) verb=update ;;
*" -s upgrade "*) verb=upgrade ;;
*" -s dist-upgrade "*) verb=dist-upgrade ;;
esac
D=${TMPDIR%/tmp}
printf '%s\n' "$D" > "$LOGS/dir"
printf '%s %s\n' "$TMPDIR" "$(stat -c %a "$TMPDIR")" > "$LOGS/tmpdir"
printf '%s\n' "$verb" >> "$LOGS/verbs"
if [ "$verb" = update ]; then
	find "$D" | sort > "$LOGS/tree"
	cat "$D/sources.list" > "$LOGS/sources.list"
	cat "$APT_CONFIG" > "$LOGS/apt-config"
	cat "$D/apt.conf.d/99proxy" > "$LOGS/proxy"
fi
case "$verb" in
` + shellCase("update", behave) + shellCase("upgrade", behave) + shellCase("dist-upgrade", behave) + shellCase("estimate", behave) + `
esac
case "$verb" in
update) cat '` + update + `' ;;
upgrade) cat '` + upgrade + `' ;;
dist-upgrade) cat '` + dist + `' ;;
estimate) cat '` + dist + `'; printf '%s\n' 'Need to get 0 B/140 MB of archives.' 'After this operation, 300 MB of additional disk space will be used.' 'E: Trivial Only specified but this is not a trivial operation.'; exit 100 ;;
*) printf 'unexpected: %s\n' "$*"; exit 2 ;;
esac
`
	return aptStandIn(t, script)
}

func shellCase(verb string, behave map[string]string) string {
	if behave[verb] == "" {
		return ""
	}
	return verb + ") " + behave[verb] + " ;;\n"
}

func logLines(t *testing.T, logs, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(logs, name))
	if err != nil {
		t.Fatalf("the stand-in for apt-get did not write %s: %v", name, err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// sandboxDirsHere are the sandbox directories that /var/tmp has now.
func sandboxDirsHere(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(osplan.SandboxParent)
	if err != nil {
		t.Skipf("%s cannot be listed here: %v", osplan.SandboxParent, err)
	}
	var names []string
	for _, e := range entries {
		if osplan.IsSandboxDirName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names
}

func gone(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the sandbox directory %s is still there: %v", dir, err)
	}
}

func TestThePlanOfTheRealBoxIsMadeThroughTheRealSandboxAndLeavesNothingBehind(t *testing.T) {
	before := sandboxDirsHere(t)
	files := newPlanFiles(t)
	logs := t.TempDir()
	sb := &osPlanSandbox{searchPath: []string{aptOnTheGuest(t, logs, nil)}}
	var phases []string
	plan, failure := osplan.Run(context.Background(), files, sb, osplan.Options{Progress: func(p string) { phases = append(phases, p) }})
	if failure != nil {
		t.Fatalf("no plan: %v", failure)
	}
	if !slices.Equal(phases, osplan.Phases) {
		t.Errorf("phases = %v", phases)
	}
	if got, want := logLines(t, logs, "verbs"), []string{"update", "upgrade", "dist-upgrade", "estimate"}; !slices.Equal(got, want) {
		t.Errorf("verbs = %v, want %v", got, want)
	}
	dir := logLines(t, logs, "dir")[0]
	gone(t, dir)
	if after := sandboxDirsHere(t); !slices.Equal(before, after) {
		t.Errorf("the sandbox directories of %s were %v and are %v", osplan.SandboxParent, before, after)
	}

	// what apt found in the sandbox at its update: the target's sources, the box's own settings, no hook, no Docker
	tree := strings.Join(logLines(t, logs, "tree"), "\n")
	for _, want := range []string{dir + "/sources.list", dir + "/apt-config", dir + "/apt.conf.d/99proxy", dir + "/lists/partial", dir + "/cache/archives/partial", dir + "/parts", dir + "/tmp"} {
		if !strings.Contains(tree+"\n", want+"\n") {
			t.Errorf("the sandbox has no %s at the update:\n%s", want, tree)
		}
	}
	for _, not := range []string{"15update-stamp", "docker"} {
		if strings.Contains(tree, not) {
			t.Errorf("the sandbox has %s at the update:\n%s", not, tree)
		}
	}
	sources := strings.Join(logLines(t, logs, "sources.list"), "\n")
	if strings.Contains(sources, "bullseye") || !strings.Contains(sources, "deb http://deb.debian.org/debian bookworm main") || !strings.Contains(sources, "deb http://deb.debian.org/debian-security bookworm-security main") {
		t.Errorf("the sources at the update:\n%s", sources)
	}
	if got := strings.Join(logLines(t, logs, "apt-config"), "\n"); got != strings.TrimSuffix(osplan.AptConfigText(dir), "\n") || strings.Count(got, "\n") != 2 {
		t.Errorf("apt-config = %q", got)
	}
	// the temporary files of apt and of its methods go to the one directory that apt's own user may write in
	if got := logLines(t, logs, "tmpdir")[0]; got != dir+"/tmp 1777" {
		t.Errorf("TMPDIR and its mode = %q, want %s/tmp 1777", got, dir)
	}
	if got := logLines(t, logs, "proxy")[0]; got != `Acquire::http::Proxy "http://cache.example.lan:3142";` {
		t.Errorf("the proxy of the box was not kept: %q", got)
	}

	// and the plan is the real one
	if plan.Counts.DistUpgrade != (osplan.Count{Upgraded: 384, New: 75, Removed: 2, KeptBack: 0}) || plan.Counts.Upgrade != (osplan.Count{Upgraded: 274, New: 0, Removed: 0, KeptBack: 111}) {
		t.Errorf("counts = %+v", plan.Counts)
	}
	var removed []string
	for _, r := range plan.Removals {
		removed = append(removed, r.Name)
	}
	if !slices.Equal(removed, []string{"fuse", "libsemanage1"}) || plan.Verdict != osplan.VerdictNeedsAcceptance {
		t.Errorf("removals = %v, verdict = %s", removed, plan.Verdict)
	}
	if !plan.Estimates.Known || plan.Sandbox.Bytes <= 0 || !slices.Equal(plan.AptConfLeftOut, []osplan.AptConfLeftOut{{File: "15update-stamp", Reason: osplan.ConfNotSimple}}) {
		t.Errorf("estimates = %+v, sandbox bytes = %d, apt conf left out = %v", plan.Estimates, plan.Sandbox.Bytes, plan.AptConfLeftOut)
	}
	if !plan.Docker.Present || !plan.Docker.SourcePaused || len(plan.Paused) != 1 || !plan.Paused[0].Docker {
		t.Errorf("docker = %+v, paused = %+v", plan.Docker, plan.Paused)
	}
	if _, err := osplan.BuildRunbook(*plan); err != nil {
		t.Errorf("the runbook of the plan: %v", err)
	}
}

// Every way out of the plan removes the directory, with the real one: a command that fails, one that runs out of time, the
// end of the core, and a panic.
func TestTheDirectoryIsRemovedOnEveryWayOutOfThePlan(t *testing.T) {
	type outcome struct {
		code    string
		phase   string
		timeout time.Duration
		cancel  bool
		panics  bool
		behave  map[string]string
	}
	hang := `sleep 60 & printf '%s %s\n' $$ $! > "$LOGS/pids"; wait`
	for name, c := range map[string]outcome{
		"an update that fails": {code: osplan.CodeSandboxUpdateFailed, phase: osplan.PhaseUpdate,
			behave: map[string]string{"update": `printf '%s\n' 'Err:1 http://deb.debian.org/debian bookworm InRelease' '  Temporary failure resolving deb.debian.org' 'E: Failed to fetch' >&2; exit 100`}},
		"a simulation that fails": {code: osplan.CodeSandboxSolverFailed, phase: osplan.PhaseUpgrade,
			behave: map[string]string{"upgrade": `printf '%s\n' 'E: Unable to correct problems, you have held broken packages.'; exit 100`}},
		"an update that runs out of time": {code: osplan.CodeTimeout, phase: osplan.PhaseUpdate, timeout: time.Second,
			behave: map[string]string{"update": hang}},
		"the end of the core": {code: osplan.CodeCanceled, phase: osplan.PhaseUpdate, cancel: true,
			behave: map[string]string{"update": hang}},
		"a panic": {code: osplan.CodeInternal, phase: osplan.PhaseParse, panics: true},
	} {
		t.Run(name, func(t *testing.T) {
			files := newPlanFiles(t)
			if c.panics {
				// read in the last phase, with the sandbox directory made and written
				files.panicOn = "/var/lib/apt/extended_states"
			}
			logs := t.TempDir()
			sb := &osPlanSandbox{searchPath: []string{aptOnTheGuest(t, logs, c.behave)}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if c.cancel {
				go func() {
					for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
						if _, err := os.Stat(filepath.Join(logs, "pids")); err == nil {
							break
						}
					}
					cancel()
				}()
			}
			plan, failure := osplan.Run(ctx, files, sb, osplan.Options{Timeouts: osplan.Timeouts{Update: c.timeout}})
			if plan != nil || failure == nil || failure.Code != c.code || failure.Phase != c.phase {
				t.Fatalf("plan = %v, failure = %v, want %s in %s", plan != nil, failure, c.code, c.phase)
			}
			gone(t, logLines(t, logs, "dir")[0])
			if c.timeout > 0 || c.cancel {
				leader, child := readPids(t, filepath.Join(logs, "pids"))
				waitGone(t, "the command", leader)
				waitGone(t, "what the command started", child)
			}
		})
	}
}
