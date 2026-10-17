package modulecmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The steps of an install, in order. Each is a marker in the log; a failure at one of them is rolled back.
const (
	StepPackages = "packages"
	StepDownload = "download"
	StepVerify   = "verify"
	StepUnpack   = "unpack"
	StepUnits    = "units"
	StepManifest = "manifest"
	StepStart    = "start"
	StepRoute    = "route"
	StepDone     = "done"
)

// The codes a run is refused or failed with, besides the ones of the plan (osmodule.Check*, osmodule.Refused*) and of the unpack (osmodule.Unpack*, osmodule.FileExists).
const (
	CodeNotRoot            = "not_root"
	CodeBusy               = "busy"
	CodePlanChanged        = "plan_changed"
	CodeConfirmationNeeded = "confirmation_needed"
	CodeDeclined           = "declined"
	CodeNotInstalled       = "not_installed"
	CodeLogUnwritable      = "log_unwritable"
	CodeLockFailed         = "lock_failed"
	CodeFilesUnreadable    = "files_unreadable"
	CodePackagesFailed     = "packages_failed"
	CodeHeldPackage        = osmodule.RefusedHeld
	CodeDownloadFailed     = "download_failed"
	CodeDownloadTooLarge   = "download_too_large"
	CodeChecksumMismatch   = "checksum_mismatch"
	CodeUnpackFailed       = "unpack_failed"
	CodeUnitsFailed        = "units_failed"
	CodeManifestInvalid    = "manifest_invalid"
	CodeManifestMismatch   = "manifest_mismatch"
	CodeManifestWrite      = "manifest_write_failed"
	CodeStartFailed        = "start_failed"
	CodeRouteTimeout       = "route_timeout"
	CodeInterrupted        = "interrupted"
	CodeUnitNotStopped     = "unit_not_stopped"
	CodeRemoveIncomplete   = "remove_incomplete"
	CodeTmpUnusable        = "tmp_unusable"
	aptTimeout             = 30 * time.Minute
	systemctlTimeout       = 2 * time.Minute
	routeWait              = 60 * time.Second
	routeEvery             = 2 * time.Second
	rollbackTime           = 5 * time.Minute
	heldChangedText        = "Held packages were changed"
	archiveName            = "archive.tar.gz"
	tarName                = "archive.tar"
)

// aptInstallArgv is the install of the packages: no questions, no recommends, never a package removed, the configuration files that are there kept, a wait for dpkg's lock. The names
// are checked here again, since they end up on a command line.
func aptInstallArgv(packages []string) ([]string, error) {
	if len(packages) == 0 || len(packages) > 40 {
		return nil, errors.New("not a list of packages to install")
	}
	for i, p := range packages {
		if !osmodule.ValidPackageName(p) || slices.Index(packages, p) != i {
			return nil, fmt.Errorf("%q is not a package apt is asked for", p)
		}
	}
	argv := []string{"apt-get", "install", "-y", "-q", "--no-install-recommends", "--no-remove", "-o", "Dpkg::Use-Pty=0", "-o", "Dpkg::Options::=--force-confold", "-o", "DPkg::Lock::Timeout=120"}
	return append(argv, packages...), nil
}

// childEnv is the environment the commands of the install run with: a clean PATH, English, and no question asked.
func childEnv(env Env) []string {
	return []string{
		"PATH=" + strings.Join(binDirs, ":"),
		"LC_ALL=C", "LANG=C",
		"DEBIAN_FRONTEND=noninteractive", "APT_LISTCHANGES_FRONTEND=none", "NEEDRESTART_SUSPEND=1", "UCF_FORCE_CONFFOLD=1",
	}
}

// run runs a command of the install with its output in the log.
func (j *job) sh(ctx context.Context, timeout time.Duration, argv ...string) (Output, error) {
	return j.env.Runner.Run(ctx, Cmd{Argv: argv, Env: childEnv(j.env), Stdout: j.toolWriter(), Timeout: timeout})
}

// refuse ends the run as refused: nothing was changed.
func (j *job) refuse(code string) int {
	j.say("refused: %s", code)
	j.result("refused", "-", code)
	return ExitRefused
}

// An install is the state of one run: what the steps leave for the ones after them.
type install struct {
	*job
	ctx      context.Context
	entry    osmodule.Entry
	asset    osmodule.Asset
	packages []string
	// steps done, for the last line and for what the rollback has to undo
	packagesDone bool
	unpackBegun  bool
	manifest     []byte
}

type stepFailure struct {
	code string
	err  error
}

func (f *stepFailure) Error() string { return f.code + ": " + f.err.Error() }

// fail makes a failure of a step. An error of the context is an interruption, whatever else the step said.
func (in *install) fail(code string, err error) *stepFailure {
	if in.ctx.Err() != nil {
		return &stepFailure{CodeInterrupted, in.ctx.Err()}
	}
	return &stepFailure{code, err}
}

// Install installs the module c.name: the plan again, then the steps. It returns the exit code.
func Install(ctx context.Context, env Env, c command) int {
	env = env.withDefaults()
	say := func(code string) int { fmt.Fprintf(env.Stdout, "refused: %s\n", code); return ExitRefused }
	if env.EUID() != 0 {
		return say(CodeNotRoot)
	}
	if err := os.MkdirAll(env.Roots.state(), 0o755); err != nil {
		return say(CodeLockFailed)
	}
	release, err := env.Host.Lock(env.Roots.lock())
	switch {
	case errors.Is(err, ErrLockHeld):
		return say(CodeBusy)
	case err != nil:
		return say(CodeLockFailed)
	}
	defer release()
	j := newJob(env, c.name)
	if err := j.openLog(); err != nil {
		return say(CodeLogUnwritable)
	}
	defer j.close()
	// whatever an earlier run that was killed left in the area of the download is nobody's now: this run has the lock
	if err := emptyDir(env.Roots.tmp()); err != nil {
		return j.refuse(CodeTmpUnusable)
	}
	defer func() { _ = emptyDir(env.Roots.tmp()) }()

	plan := PlanOf(ctx, env, c.name)
	switch {
	case plan.Refused != "":
		return j.refuse(string(plan.Refused))
	case c.planID != "" && c.planID != plan.PlanID:
		return j.refuse(CodePlanChanged)
	case !c.yes:
		if !env.Terminal() {
			return j.refuse(CodeConfirmationNeeded)
		}
		if !j.confirm(plan.Name, plan.Tag, plan.Tier, plan.Packages.ToInstall, plan.Packages.DownloadBytes, plan.Archive.URL, "install") {
			return j.refuse(CodeDeclined)
		}
	}
	cat := loadCatalogue(env).cat
	entry, _ := cat.Find(c.name)
	asset, _ := entry.Asset(env.Arch)
	in := &install{job: j, ctx: ctx, entry: entry, asset: asset, packages: osmodule.PackagesFor(entry.Manifest, env.Arch)}
	return in.steps()
}

// confirm shows what is going to be done and asks.
func (j *job) confirm(name, tag, tier string, packages []string, download int64, url, verb string) bool {
	fmt.Fprintf(j.env.Stdout, "%s %s %s (%s)\n", verb, name, tag, tier)
	if verb == "install" {
		fmt.Fprintf(j.env.Stdout, "  packages to install: %s\n  download: %d bytes of packages, then %s\n", strings.Join(packages, " "), download, url)
	}
	fmt.Fprint(j.env.Stdout, "Go on? [y/N] ")
	line, _ := bufio.NewReader(j.env.Stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// hook calls the test hook, if there is one.
func (j *job) hook(ctx context.Context, step, phase string) {
	if j.env.StepHook != nil {
		j.env.StepHook(ctx, step, phase)
	}
}

func (in *install) steps() int {
	steps := []struct {
		name string
		do   func() *stepFailure
	}{
		{StepPackages, in.stepPackages},
		{StepDownload, in.stepDownload},
		{StepVerify, in.stepVerify},
		{StepUnpack, in.stepUnpack},
		{StepUnits, in.stepUnits},
		{StepManifest, in.stepManifest},
		{StepStart, in.stepStart},
		{StepRoute, in.stepRoute},
	}
	for _, s := range steps {
		in.marker(s.name, "begin")
		in.hook(in.ctx, s.name, "begin")
		var f *stepFailure
		if err := in.ctx.Err(); err != nil {
			f = &stepFailure{CodeInterrupted, err}
		} else {
			f = s.do()
		}
		if f != nil {
			in.marker(s.name, "failed")
			return in.failed(s.name, f)
		}
		in.marker(s.name, "done")
		in.hook(in.ctx, s.name, "done")
		if s.name == StepPackages {
			in.packagesDone = true
		}
	}
	in.marker(StepDone, "done")
	in.say("installed: %s %s", in.entry.Name, in.entry.Tag)
	in.result("ok", StepDone, "-")
	return ExitOK
}

// failed rolls back what the steps after the packages did, and says what was kept.
func (in *install) failed(step string, f *stepFailure) int {
	in.say("failed at step %s: %s", step, f.code)
	in.result("failed", step, f.code)
	if in.unpackBegun {
		// what the rollback does is not stopped by the signal that interrupted the run
		ctx, cancel := context.WithTimeout(context.WithoutCancel(in.ctx), rollbackTime)
		defer cancel()
		if bad, _ := in.teardown(ctx, in.entry.Name, in.entry.Manifest.Unit, false); bad > 0 {
			in.say("rollback incomplete: %d things could not be undone; casaos module remove %s finishes it", bad, in.entry.Name)
		} else {
			in.say("rolled back")
		}
	}
	in.say("kept_packages: %s", in.keptPackages())
	return ExitFailed
}

func (in *install) keptPackages() string {
	switch {
	case !in.packagesDone:
		return "unknown"
	case len(in.packages) == 0:
		return "none"
	}
	return strings.Join(in.packages, " ")
}

// ---- the steps ---------------------------------------------------------------------------------------------------------------------------------------------------------------

func (in *install) stepPackages() *stepFailure {
	if len(in.packages) == 0 {
		return nil
	}
	argv, err := aptInstallArgv(in.packages)
	if err != nil {
		return in.fail(CodePackagesFailed, err)
	}
	tail := &tailWriter{Writer: in.toolWriter()}
	out, err := in.env.Runner.Run(in.ctx, Cmd{Argv: argv, Env: childEnv(in.env), Stdout: tail, Timeout: aptTimeout})
	switch {
	case err != nil:
		return in.fail(CodePackagesFailed, err)
	case out.ExitCode != 0 && strings.Contains(string(tail.tail), heldChangedText):
		return in.fail(CodeHeldPackage, fmt.Errorf("apt-get ended with status %d", out.ExitCode))
	case out.ExitCode != 0:
		return in.fail(CodePackagesFailed, fmt.Errorf("apt-get ended with status %d", out.ExitCode))
	}
	return nil
}

func (in *install) archivePath() string { return in.env.Roots.tmp() + "/" + archiveName }
func (in *install) tarPath() string     { return in.env.Roots.tmp() + "/" + tarName }

func (in *install) stepDownload() *stepFailure {
	dl := downloaderOf(in.env)
	if dl == nil {
		return in.fail(CodeDownloadFailed, errors.New("no downloader"))
	}
	var lastErr error
	for try := 1; try <= downloadTries; try++ {
		f, err := os.OpenFile(in.archivePath(), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return in.fail(CodeDownloadFailed, err)
		}
		_, err = dl.Get(in.ctx, in.asset.URL, f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			return nil
		}
		lastErr = err
		_ = os.Remove(in.archivePath())
		if in.ctx.Err() != nil {
			break
		}
		var perm *PermanentError
		if errors.As(err, &perm) {
			if errors.Is(err, ErrTooLarge) {
				return in.fail(CodeDownloadTooLarge, err)
			}
			break
		}
		in.say("download: try %d of %d failed", try, downloadTries)
		if try < downloadTries {
			if err := in.env.Clock.Sleep(in.ctx, time.Duration(try)*2*time.Second); err != nil {
				break
			}
		}
	}
	return in.fail(CodeDownloadFailed, lastErr)
}

func (in *install) stepVerify() *stepFailure {
	sum, err := sha256File(in.ctx, in.archivePath())
	if err != nil {
		return in.fail(CodeDownloadFailed, err)
	}
	if sum != in.asset.SHA256 {
		_ = os.Remove(in.archivePath())
		return in.fail(CodeChecksumMismatch, errors.New("the sha256 of the archive is not the catalogue's"))
	}
	return nil
}

func (in *install) stepUnpack() *stepFailure {
	if err := gunzipTo(in.ctx, in.archivePath(), in.tarPath(), osmodule.MaxStreamBytes); err != nil {
		code := osmodule.UnpackCorrupt
		if errors.Is(err, ErrTooLarge) {
			code = osmodule.UnpackLimits
		}
		return in.fail(code, err)
	}
	_ = os.Remove(in.archivePath())
	tarFile, err := os.Open(in.tarPath())
	if err != nil {
		return in.fail(CodeUnpackFailed, err)
	}
	defer tarFile.Close()
	// from here the box may hold files of the module
	in.unpackBegun = true
	if err := os.MkdirAll(in.env.Roots.state(), 0o755); err != nil {
		return in.fail(CodeUnpackFailed, err)
	}
	jr, own, err := openJournal(in.env.Roots.files(in.entry.Name))
	if err != nil {
		return in.fail(CodeFilesUnreadable, err)
	}
	dest := journalDest{Destination: in.env.Host.NewDest(in.env.Roots.Prefix), j: jr}
	res, err := osmodule.Unpack(tarFile, dest, own)
	if cerr := jr.close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		var refused *osmodule.Refused
		if errors.As(err, &refused) {
			return in.fail(refused.Code, err)
		}
		return in.fail(CodeUnpackFailed, err)
	}
	for _, p := range res.Kept {
		in.say("kept: %s", printable(p, 200))
	}
	in.manifest = res.Manifest
	return nil
}

func (in *install) stepUnits() *stepFailure {
	if out, err := in.sh(in.ctx, systemctlTimeout, "systemctl", "daemon-reload"); err != nil || out.ExitCode != 0 {
		return in.fail(CodeUnitsFailed, errors.New("systemctl daemon-reload failed"))
	}
	return nil
}

// manifestMatches says whether the manifest of the archive is, in everything the install acts on, the one the plan was made from.
func manifestMatches(a, b osmodule.Manifest) bool {
	return a.Name == b.Name && a.Component == b.Component && a.Unit == b.Unit && a.GatewayRoute == b.GatewayRoute && a.MinReCasaOS == b.MinReCasaOS &&
		slices.Equal(a.OS, b.OS) && slices.Equal(a.Packages.Common, b.Packages.Common) && slices.Equal(a.Packages.AMD64, b.Packages.AMD64) && slices.Equal(a.Packages.ARM64, b.Packages.ARM64)
}

func (in *install) stepManifest() *stepFailure {
	m, err := osmodule.Parse(osmodule.FileName(in.entry.Name), in.manifest)
	if err != nil {
		return in.fail(CodeManifestInvalid, err)
	}
	if !manifestMatches(m, in.entry.Manifest) {
		return in.fail(CodeManifestMismatch, errors.New("the manifest of the archive is not the catalogue's"))
	}
	if err := os.MkdirAll(in.env.Roots.manifests(), 0o755); err != nil {
		return in.fail(CodeManifestWrite, err)
	}
	// the version first: the manifest is what makes the module installed
	if err := writeFileAtomic(in.env.Roots.version(in.entry.Name), []byte(in.entry.Tag+"\n"), 0o644); err != nil {
		return in.fail(CodeManifestWrite, err)
	}
	if err := writeFileAtomic(in.env.Roots.manifest(in.entry.Name), in.manifest, 0o644); err != nil {
		return in.fail(CodeManifestWrite, err)
	}
	return nil
}

func (in *install) stepStart() *stepFailure {
	if out, err := in.sh(in.ctx, systemctlTimeout, "systemctl", "enable", "--now", in.entry.Manifest.Unit); err != nil || out.ExitCode != 0 {
		return in.fail(CodeStartFailed, errors.New("systemctl enable --now failed"))
	}
	return nil
}

func (in *install) stepRoute() *stepFailure {
	deadline := in.env.Clock.Now().Add(routeWait)
	for {
		if answers, err := in.env.Gateway.Answers(in.ctx, in.entry.Manifest.GatewayRoute); err == nil && answers {
			return nil
		}
		if in.ctx.Err() != nil {
			return in.fail(CodeRouteTimeout, in.ctx.Err())
		}
		if !in.env.Clock.Now().Before(deadline) {
			return in.fail(CodeRouteTimeout, errors.New("the gateway did not answer in time"))
		}
		if err := in.env.Clock.Sleep(in.ctx, routeEvery); err != nil {
			return in.fail(CodeRouteTimeout, err)
		}
	}
}

var _ = io.Discard
