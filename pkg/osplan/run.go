package osplan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// Options are the knobs of Run; the zero value is what the service uses.
type Options struct {
	// Progress is told the phase that begins, from a goroutine that is Run's own.
	Progress func(phase string)
	Timeouts Timeouts
}

func (o Options) progress(phase string) {
	if o.Progress != nil {
		o.Progress(phase)
	}
}

// gates are the checks of the readiness report that say who the box is: nothing else of the report is run to know whether
// a plan can be made.
func gates() []osreadiness.Check {
	var out []osreadiness.Check
	for _, c := range osreadiness.Checks() {
		if c.Gate {
			out = append(out, c)
		}
	}
	return out
}

// Plannable says whether a plan can be made for this box, from what the report says of who it is and from nothing that takes
// time: it runs as root; it is strictly the Debian that the table knows (a box that is unsupported, on the latest release or a
// variant is identity_unsupported); and the hop it would take is one that planHops has (hop_not_planned otherwise). It asks no
// apt, takes no lock and writes nothing.
func Plannable(ctx context.Context, files Files) (HopSpec, *Failure) {
	if files.EUID() != 0 {
		return HopSpec{}, fail(CodeNotRoot, "the core does not run as root", nil)
	}
	report := osreadiness.Run(ctx, files, osreadiness.Options{Checks: gates()})
	switch {
	case report.State == osreadiness.StateUnsupported, report.State == osreadiness.StateLatest, report.State == osreadiness.StateBlocked, report.Hop == nil:
		data := map[string]any{"state": report.State}
		var codes []string
		for _, f := range report.Checks {
			if f.Severity == osreadiness.Refuse {
				codes = append(codes, f.Code)
				if f.Data["unknown"] == true {
					data["unknown"] = true
				}
			}
		}
		if len(codes) > 0 {
			data["checks"] = codes
		}
		return HopSpec{}, fail(CodeIdentityUnsupported, "this machine is not one that a plan can be made for", data)
	}
	hop, ok := PlannedHop(report.Hop.ID())
	if !ok {
		return HopSpec{}, fail(CodeHopNotPlanned, "no plan is made for this hop yet", map[string]any{"hop": string(report.Hop.ID())})
	}
	return hop, nil
}

// checkBusy is busy when something else changes the packages (one of the maintenance units runs, or a package manager holds
// dpkg's lock), and when that cannot be seen: a lock that cannot be looked at is not one that nobody holds.
func checkBusy(ctx context.Context, files Files) *Failure {
	reason, busy, err := files.MaintenanceBusy(ctx)
	switch {
	case busy:
		return fail(CodeBusy, "another package manager is at work: "+sanitizeLine(reason), nil)
	case err != nil:
		return fail(CodeBusy, "whether another package manager is at work could not be seen", map[string]any{"unknown": true})
	}
	return nil
}

// Preflight is what is refused at once, before a job starts: Plannable, then busy, then room in /var/tmp for the sandbox.
func Preflight(ctx context.Context, files Files) (HopSpec, *Failure) {
	hop, f := Plannable(ctx, files)
	if f != nil {
		return hop, f
	}
	if f := checkBusy(ctx, files); f != nil {
		return hop, f
	}
	free, err := files.FreeBytes(ctx, SandboxParent)
	if err != nil || free < MinFreeBytes {
		data := map[string]any{"path": SandboxParent, "need": uint64(MinFreeBytes), "free": free}
		if err != nil {
			data["unknown"] = true
		}
		return hop, fail(CodeNoRoom, "there is not room for the sandbox in "+SandboxParent, data)
	}
	return hop, nil
}

// ctxFailure is the failure of a command that did not end by itself: the job's own end (canceled), or its time (timeout).
func ctxFailure(ctx, cmdCtx context.Context, phase string) *Failure {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return fail(CodeCanceled, "the plan was canceled", nil).at(phase)
	case ctx.Err() != nil, errors.Is(cmdCtx.Err(), context.DeadlineExceeded):
		return fail(CodeTimeout, "the plan took too long", nil).at(phase)
	}
	return nil
}

// Run makes the plan of the box: the whole of this rung, in its phases, from the sources to the verdict. Nothing of the box is
// changed: what is written is the sandbox directory, which is removed on every way out of Run (it ends, fails, times out, is
// canceled or panics). A failure is the code and the phase it was in; a panic is recovered as internal.
func Run(ctx context.Context, files Files, sb Sandbox, opts Options) (plan *Plan, failure *Failure) {
	phase := PhaseSources
	var remove func()
	defer func() {
		if r := recover(); r != nil {
			plan, failure = nil, (&Failure{Code: CodeInternal, Phase: phase, Detail: "an unexpected error"}).because(fmt.Errorf("a panic: %v\n%s", r, debug.Stack()))
		}
	}()
	defer func() {
		if remove != nil {
			remove()
		}
	}()
	seconds := map[string]float64{}
	started := files.Now()
	begin := func(next string) {
		now := files.Now()
		seconds[phase] = round1(now.Sub(started).Seconds())
		phase, started = next, now
		opts.progress(next)
	}
	opts.progress(PhaseSources)

	hop, f := Preflight(ctx, files)
	if f != nil {
		return nil, f.at(PhaseSources)
	}
	safe := osreadiness.Guard(files)
	srcFiles, err := osreadiness.ReadSourceFiles(ctx, safe)
	if err != nil {
		return nil, ctxOr(ctx, err, "the sources could not be read").at(PhaseSources)
	}
	rewritten, err := RewriteSources(ctx, hop, srcFiles, osreadiness.NewDistroDecider(safe))
	if err != nil {
		return nil, asFailure(ctx, err, "the sources could not be rewritten").at(PhaseSources)
	}
	cfg, err := ReadAptConfig(ctx, safe)
	if err != nil {
		return nil, ctxOr(ctx, err, "the configuration of apt could not be read").at(PhaseSources)
	}
	// what the sandbox did not get of the box's apt configuration is said by every failure that it can explain (a proxy left out is a network that cannot be
	// reached), and not only by the plan that worked
	defer func() { failure = failure.withAptConf(cfg.LeftOut) }()
	// the room on the disk is read now, before the sandbox is made: when /var/tmp is on the same file system as / or /usr (it is
	// by default) the sandbox takes some 214 MiB of it, which the hop does not, and which is given back when Run ends
	free := readFree(ctx, safe)
	dir, rm, err := sb.MakeDir()
	if err != nil {
		return nil, fail(CodeInternal, "the sandbox directory could not be made", nil).because(err).at(PhaseSources)
	}
	remove = rm
	if !ValidSandboxDir(dir) {
		return nil, fail(CodeInternal, "the sandbox directory is not named as one", nil).because(fmt.Errorf("MakeDir gave %q", dir)).at(PhaseSources)
	}
	if f := writeSandbox(ctx, sb, dir, SandboxFiles(dir, rewritten.Tree, cfg)); f != nil {
		return nil, f.at(PhaseSources)
	}

	// busy is looked at again right before the update, which is where something else could have started
	begin(PhaseUpdate)
	if f := checkBusy(ctx, files); f != nil {
		return nil, f.at(PhaseUpdate)
	}
	updateOut, f := runInSandbox(ctx, sb, dir, UpdateArgv(dir), opts.Timeouts.update(), PhaseUpdate)
	if f != nil {
		return nil, f
	}
	if f := ParseUpdate(updateOut); f != nil {
		return nil, f.at(PhaseUpdate)
	}

	begin(PhaseUpgrade)
	upgradeOut, f := runInSandbox(ctx, sb, dir, UpgradeArgv(dir), opts.Timeouts.solver(), PhaseUpgrade)
	if f != nil {
		return nil, f
	}
	if f := solverFailure(upgradeOut); f != nil {
		return nil, f.at(PhaseUpgrade)
	}

	begin(PhaseDistUpgrade)
	distOut, f := runInSandbox(ctx, sb, dir, DistUpgradeArgv(dir), opts.Timeouts.solver(), PhaseDistUpgrade)
	if f != nil {
		return nil, f
	}
	if f := solverFailure(distOut); f != nil {
		return nil, f.at(PhaseDistUpgrade)
	}

	begin(PhaseEstimate)
	estimateOut, f := runInSandbox(ctx, sb, dir, EstimateArgv(dir), opts.Timeouts.solver(), PhaseEstimate)
	if f != nil {
		// the sizes are the one thing that apt may refuse to say without the plan being any worse: a command that cannot
		// be run, or ends in no form that is known, is estimates.known false. The end of the time is not.
		if f.Code == CodeTimeout || f.Code == CodeCanceled {
			return nil, f
		}
		estimateOut = Output{}
	}
	sandboxBytes := walkSize(ctx, safe, dir)

	begin(PhaseParse)
	upgraded, f := ParseSolver(upgradeOut)
	if f != nil {
		return nil, f.at(PhaseParse)
	}
	dist, f := ParseSolver(distOut)
	if f != nil {
		return nil, f.at(PhaseParse)
	}
	estimates := ParseEstimate(estimateOut, dist.Summary, dir)
	in := Inputs{
		Hop: hop, Rewritten: rewritten, AptConfLeftOut: cfg.LeftOut, Dist: dist, Upgrade: upgraded, Estimates: estimates,
		Free: free, SandboxBytes: sandboxBytes, Seconds: seconds,
	}
	if f := readBox(ctx, safe, &in); f != nil {
		return nil, f.at(PhaseParse)
	}
	in.Now = files.Now()
	if err := ctx.Err(); err != nil {
		return nil, ctxFailure(ctx, ctx, PhaseParse)
	}
	seconds[PhaseParse] = round1(in.Now.Sub(started).Seconds())
	p := Assemble(in)
	return &p, nil
}

func round1(s float64) float64 { return math.Round(s*10) / 10 }

// ctxOr is the failure of a read that failed: the end of the time as it is, anything else as internal.
func ctxOr(ctx context.Context, err error, what string) *Failure {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return fail(CodeCanceled, "the plan was canceled", nil)
		}
		return fail(CodeTimeout, "the plan took too long", nil)
	}
	return fail(CodeInternal, what, nil).because(err)
}

// asFailure is the *Failure an error is, or the failure of the end of the time, or internal.
func asFailure(ctx context.Context, err error, what string) *Failure {
	var f *Failure
	if errors.As(err, &f) {
		return f
	}
	return ctxOr(ctx, err, what)
}

// writeSandbox writes the files of the sandbox, and refuses (before it writes anything) a path that is not inside its
// directory: what the plan writes is the sandbox directory, and nothing else.
func writeSandbox(ctx context.Context, sb Sandbox, dir string, files []SandboxFile) *Failure {
	for _, w := range files {
		if !inside(dir, w.Path) {
			return fail(CodeInternal, "a file of the sandbox is outside it", nil).because(fmt.Errorf("%q is not inside %q", w.Path, dir))
		}
	}
	for _, w := range files {
		if err := sb.WriteFile(w.Path, w.Content, w.Mode); err != nil {
			return ctxOr(ctx, err, "the sandbox could not be written")
		}
	}
	return nil
}

// inside says whether p is a path strictly inside dir, clean.
func inside(dir, p string) bool {
	return strings.HasPrefix(p, dir+"/") && path.Clean(p) == p
}

// runInSandbox runs one of the four commands: the one that the allow-list says, within its time. A command that cannot be
// started is a failure of its phase.
func runInSandbox(ctx context.Context, sb Sandbox, dir string, argv []string, timeout time.Duration, phase string) (Output, *Failure) {
	if !AllowedSandboxArgv(argv, dir) {
		return Output{}, fail(CodeInternal, "a command that is not on the allow-list was about to run", nil).because(fmt.Errorf("argv %q", argv)).at(phase)
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := sb.Run(cmdCtx, argv)
	if f := ctxFailure(ctx, cmdCtx, phase); f != nil {
		return Output{}, f
	}
	if err != nil {
		if phase == PhaseUpdate {
			return Output{}, fail(CodeSandboxUpdateFailed, "apt-get could not be run: "+sanitizeLine(err.Error()), map[string]any{"kind": KindOther}).at(phase)
		}
		return Output{}, fail(CodeSandboxSolverFailed, "apt-get could not be run: "+sanitizeLine(err.Error()), nil).at(phase)
	}
	return out, nil
}

// solverFailure is what is wrong with a simulation whatever its shape: it ended with a status, or said E:.
func solverFailure(out Output) *Failure {
	lines := splitOutput(out.Text)
	if errs := errorLines(lines, "E:"); out.ExitCode != 0 || len(errs) > 0 {
		detail := detailOf(errs)
		if detail == "" {
			detail = fmt.Sprintf("apt-get -s ended with status %d", out.ExitCode)
		}
		return fail(CodeSandboxSolverFailed, detail, map[string]any{"exit": out.ExitCode})
	}
	return nil
}

// ---- what is read of the box ---------------------------------------------------------------------------------------

const extendedStates = "/var/lib/apt/extended_states"

// parseExtendedStates are the packages apt installed by itself ("Auto-Installed: 1"), by the name that apt prints for them: the
// name alone for the native architecture and for "all", name:architecture for another (a stanza that says no architecture is
// the native one). A package that one stanza says is not automatic ("Auto-Installed: 0") is not, whatever another says of it, and
// a package that is only one architecture's copy of another's is not: libssl1.1 for amd64 being automatic does not make the
// i386 copy that the owner installed by hand an automatic one.
func parseExtendedStates(text, native string) map[string]bool {
	auto, manual := map[string]bool{}, map[string]bool{}
	for _, stanza := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		name, arch, isAuto := "", "", false
		for _, line := range strings.Split(stanza, "\n") {
			switch k, v, _ := strings.Cut(line, ":"); k {
			case "Package":
				name = strings.TrimSpace(v)
			case "Architecture":
				arch = strings.TrimSpace(v)
			case "Auto-Installed":
				isAuto = strings.TrimSpace(v) == "1"
			}
		}
		if name == "" {
			continue
		}
		if arch != "" && arch != native && arch != "all" {
			name += ":" + arch
		}
		if isAuto {
			auto[name] = true
		} else {
			manual[name] = true
		}
	}
	for name := range manual {
		delete(auto, name)
	}
	return auto
}

var dpkgFormat = "-f=${Package}\t${Version}\t${Essential}\t${Priority}\n"

// removedFacts asks dpkg what the removed packages are, a couple of hundred names at a time.
func removedFacts(ctx context.Context, files Files, names []string) (map[string]PackageFacts, *Failure) {
	facts := map[string]PackageFacts{}
	for len(names) > 0 {
		n := min(len(names), 200)
		argv := append([]string{"dpkg-query", "-W", dpkgFormat}, names[:n]...)
		names = names[n:]
		out, err := files.Run(ctx, argv...)
		if err != nil || out.ExitCode > 1 || out.Truncated {
			return nil, ctxOr(ctx, err, "dpkg could not say what the removed packages are")
		}
		for _, line := range strings.Split(out.Text, "\n") {
			fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
			if len(fields) != 4 || !packageName.MatchString(fields[0]) {
				continue
			}
			prev := facts[fields[0]]
			facts[fields[0]] = PackageFacts{
				Version:   cmp1(prev.Version, fields[1]),
				Essential: prev.Essential || fields[2] == "yes",
				Priority:  cmp1(prev.Priority, fields[3]),
			}
		}
	}
	return facts, nil
}

// cmp1 is the first of two values that says something.
func cmp1(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// readBox reads the rest of what the plan says of the box: the holds, Docker, which packages apt installed by itself, and what
// dpkg says of the removed ones.
func readBox(ctx context.Context, files Files, in *Inputs) *Failure {
	docker, others, err := osreadiness.HeldPackages(ctx, files)
	if err != nil {
		return ctxOr(ctx, err, "the packages on hold could not be read")
	}
	in.DockerHeld, in.Held = docker, slices.Concat(docker, others)
	if there, err := osreadiness.DockerPresent(ctx, files); err == nil {
		in.DockerPresent = there
	} else if ctx.Err() != nil {
		return ctxOr(ctx, err, "")
	}
	states, err := files.ReadFile(ctx, extendedStates)
	if err == nil {
		in.AutoInstalled, in.AutoKnown = parseExtendedStates(string(states), files.Arch()), true
	} else if ctx.Err() != nil {
		return ctxOr(ctx, err, "")
	}
	// every package that is removed, in either simulation, is asked of dpkg: what is Essential cannot be guessed at
	var names []string
	for _, run := range []*SolverRun{in.Dist, in.Upgrade} {
		for _, n := range run.Removals() {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	if len(names) > 0 {
		facts, f := removedFacts(ctx, files, names)
		if f != nil {
			return f
		}
		for _, n := range names {
			if _, ok := facts[baseName(n)]; !ok {
				// a package that is removed and that dpkg does not know is not a state that a plan says anything of
				return fail(CodePlanUnparsed, "dpkg does not know a package that apt removes", map[string]any{"reason": "removal_unknown", "name": n})
			}
		}
		in.Facts = facts
	}
	return nil
}

// readFree is the room on the file systems that the hop writes to: what could not be read is Known false, which is no room.
func readFree(ctx context.Context, files Files) map[string]FreeRead {
	free := map[string]FreeRead{}
	for _, p := range diskPaths {
		bytes, err := files.FreeBytes(ctx, p)
		free[p] = FreeRead{Bytes: bytes, Known: err == nil}
	}
	return free
}

// walkSize is what the sandbox directory weighs: the sizes of its files, by a walk of the directory and not by a command.
// What cannot be looked at weighs nothing.
func walkSize(ctx context.Context, files Files, dir string) int64 {
	var total int64
	budget := 100000
	var walk func(p string, depth int)
	walk = func(p string, depth int) {
		if depth > 16 || budget <= 0 || ctx.Err() != nil {
			return
		}
		names, err := files.ReadDir(ctx, p)
		if err != nil {
			return
		}
		for _, name := range names {
			if budget--; budget <= 0 {
				return
			}
			child := p + "/" + name
			info, err := files.Lstat(ctx, child)
			switch {
			case err != nil:
			case info.IsDir():
				walk(child, depth+1)
			case info.Mode&fs.ModeSymlink == 0:
				total += info.Size
			}
		}
	}
	walk(dir, 0)
	return total
}
