package modulecmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	CodeDpkgBroken         = "dpkg_broken"
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
	CodeStateUnsafe        = "state_unsafe"
	CodeInternal           = "internal_error"
	undoDone               = "rolled_back"
	undoIncomplete         = "rollback_incomplete"
	aptTimeout             = 30 * time.Minute
	systemctlTimeout       = 2 * time.Minute
	routeWait              = 60 * time.Second
	routeEvery             = 2 * time.Second
	rollbackTime           = 5 * time.Minute
	heldChangedText        = "Held packages were changed"
	archiveName            = "archive.tar.gz"
	tarName                = "archive.tar"
)

const (
	// killGrace is how long a command that is asked to stop (SIGTERM) is given to leave before what is left of it is killed.
	killGrace = 20 * time.Second
	// aptFinish is how long apt-get, asked to stop, is given to finish the dpkg run it is in (Cmd.Finish) before it is stopped as any command is: it plus killGrace is within StopWithin,
	// which the unit's TimeoutStopSec is built from.
	aptFinish = 5 * time.Minute
	// StopWithin is how long a run needs, from the SIGTERM that asks it to stop, to be over: the command it is in is given killGrace to leave, then the rollback has rollbackTime, and the
	// command that ends the rollback is given killGrace in its turn. A unit that waits for less before it kills what is left cuts the rollback of an install.
	StopWithin = 2*killGrace + rollbackTime
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
	j.result("refused", "-", code, "-")
	return ExitRefused
}

// unreadableLists are the lists of the files and of the directories of the module that are there and cannot be read.
func (j *job) unreadableLists(name string) (bad []string) {
	roots := j.env.Roots
	if _, err := readFilesEntries(roots.files(name)); err != nil {
		bad = append(bad, roots.files(name))
	}
	if _, err := readDirsList(roots.dirs(name)); err != nil {
		bad = append(bad, roots.dirs(name))
	}
	return bad
}

// refuseEarly refuses a run that has not got the lock: it says so on the standard output and touches no log, which is the log of the run that has the lock.
func refuseEarly(env Env, name, code string) int { return newJob(env, name).refuse(code) }

// An install is the state of one run: what the steps leave for the ones after them.
type install struct {
	*job
	ctx      context.Context
	entry    osmodule.Entry
	asset    osmodule.Asset
	packages []string
	// steps done, for the last line and for what the rollback has to undo
	packagesDone bool
	// archive is the archive as it was downloaded, open: it was made for this run and is read by this descriptor from the download to the unpack, never opened by its path again.
	// archiveSum is the sha256 of its bytes, taken as they were written.
	archive    *os.File
	archiveSum string
	// noUpgrade makes apt-get leave the packages that are installed as they are (an update's)
	noUpgrade bool
	// aptStarted is set when apt-get has been started: a run that ends it leaves dpkg in the state it was in
	aptStarted  bool
	unpackBegun bool
	// written is what the unpack put down, set as soon as it returns, and what a rollback deletes (the files that still hold what was written): the files and directories of this run and of
	// the earlier install of the module that was killed, if there was one. It is nil until the lists of that earlier install have been read.
	written  *written
	manifest []byte
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

// planRefusal is the code a plan is refused with: its own, a plan id the caller gave that is not its, and a plan other than the one the user was shown and said yes to (asked is its id).
func planRefusal(c command, plan osmodule.PlanResult, asked string) string {
	switch {
	case plan.Refused != "":
		return string(plan.Refused)
	case c.planID != "" && c.planID != plan.PlanID, asked != "" && asked != plan.PlanID:
		return CodePlanChanged
	}
	return ""
}

// runInstall installs the module c.name: the plan, the question when there is to be one, the plan again, then the steps. The question is asked before the lock is taken, since the lock is for
// the work and held while somebody thinks it would keep every other install and remove from running, and the plan is made again once it is taken, so that what the user said yes to is what
// is done, or nothing is. It returns the exit code.
func runInstall(ctx context.Context, env Env, c command) (code int) {
	env = env.withDefaults()
	if env.EUID() != 0 {
		return refuseEarly(env, c.name, CodeNotRoot)
	}
	if err := makeDirs(env.Roots.state(), 0o755); err != nil {
		return refuseEarly(env, c.name, CodeLockFailed)
	}
	if err := env.Host.SafeDir(env.Roots.state()); err != nil {
		return refuseEarly(env, c.name, CodeStateUnsafe)
	}
	asked := ""
	if !c.yes {
		plan := PlanOf(ctx, env, c.name)
		if why := planRefusal(c, plan, ""); why != "" {
			return refuseEarly(env, c.name, why)
		}
		if !env.Terminal() {
			return refuseEarly(env, c.name, CodeConfirmationNeeded)
		}
		if !newJob(env, c.name).confirm(plan.Name, plan.Tag, plan.Tier, plan.Packages.ToInstall, plan.Packages.DownloadBytes, plan.Archive.URL, "install") {
			return refuseEarly(env, c.name, CodeDeclined)
		}
		asked = plan.PlanID
	}
	release, err := env.Host.Lock(env.Roots.lock())
	switch {
	case errors.Is(err, ErrLockHeld):
		return refuseEarly(env, c.name, CodeBusy)
	case err != nil:
		return refuseEarly(env, c.name, CodeLockFailed)
	}
	defer release()
	j := newJob(env, c.name)
	if err := j.openLog(KindInstall); err != nil {
		return refuseEarly(env, c.name, CodeLogUnwritable)
	}
	defer j.close()
	// (a panic before the steps is a failure too; it is a rollback that is not complete when the unpack had begun)
	var in *install
	defer j.recoverRun(&code, func() string {
		if in != nil && in.unpackBegun {
			return undoIncomplete
		}
		return undoDone
	})
	// whatever an earlier run that was killed left in the area of the download is nobody's now: this run has the lock
	if err := emptyTmp(env.Host, env.Roots.tmp()); err != nil {
		if errors.Is(err, errUnsafe) {
			return j.refuse(CodeStateUnsafe)
		}
		return j.refuse(CodeTmpUnusable)
	}
	if code, stop := j.recoverUpdate(ctx, KindInstall); stop {
		return code
	}
	// a list of an earlier install that cannot be read is a state for the administrator to look at: the module cannot be told from it, and nothing is changed
	if bad := j.unreadableLists(c.name); len(bad) > 0 {
		for _, p := range bad {
			j.say("the list %s cannot be read: look at it, and delete it by hand, and install again", printable(env.Roots.Slash(p), 200))
		}
		return j.refuse(CodeFilesUnreadable)
	}
	defer func() { _ = emptyTmp(env.Host, env.Roots.tmp()) }()

	if why := planRefusal(c, PlanOf(ctx, env, c.name), asked); why != "" {
		return j.refuse(why)
	}
	cat := loadCatalogue(env).cat
	entry, _ := cat.Find(c.name)
	asset, _ := entry.Asset(env.Arch)
	in = &install{job: j, ctx: ctx, entry: entry, asset: asset, packages: osmodule.PackagesFor(entry.Manifest, env.Arch)}
	defer in.closeArchive()
	return in.steps()
}

// confirm shows what is going to be done and asks.
func (j *job) confirm(name, tag, tier string, packages []string, download int64, url, verb string) bool {
	fmt.Fprintf(j.env.Stdout, "%s %s %s (%s)\n", verb, name, tag, tier)
	if verb == "install" || verb == "update" {
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
			f = in.guarded(s.do)
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
	in.result("ok", StepDone, "-", "-")
	return ExitOK
}

// guarded runs a step. A panic in it is a failure of the step like any other (and so a rollback and an exit status of 3); the stack is in the log.
func (in *install) guarded(do func() *stepFailure) (f *stepFailure) {
	defer func() {
		if r := recover(); r != nil {
			in.panicked(r)
			f = &stepFailure{CodeInternal, fmt.Errorf("panic: %s", printable(fmt.Sprint(r), 200))}
		}
	}()
	return do()
}

// rollback undoes what the unpack and the steps after it did. What it does is not stopped by the signal that interrupted the run; a panic in it is a rollback that could not be finished.
func (in *install) rollback() (bad int) {
	defer func() {
		if r := recover(); r != nil {
			in.panicked(r)
			in.failOpen()
			bad = 1
		}
	}()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(in.ctx), rollbackTime)
	defer cancel()
	bad, _, _ = in.teardown(ctx, in.entry.Name, in.entry.Manifest.Unit, "rollback_", in.written)
	return bad
}

// failed rolls back what the steps after the packages did, and says what was kept.
func (in *install) failed(step string, f *stepFailure) int {
	in.say("failed at step %s: %s", step, f.code)
	undo := undoDone
	if in.unpackBegun {
		if bad := in.rollback(); bad > 0 {
			in.say("rollback incomplete: %d things could not be undone; casaos module remove %s finishes it", bad, in.entry.Name)
			undo = undoIncomplete
		} else {
			in.say("rolled back")
		}
	}
	if in.aptStarted && step == StepPackages && (f.code == CodeInterrupted || errors.Is(f.err, context.DeadlineExceeded)) {
		in.say("apt-get was ended while it worked: dpkg --configure -a is probably needed")
	}
	in.say("kept_packages: %s", in.keptPackages())
	// the last line, said when everything is over: a run that is ended before it is a run that was interrupted
	in.result("failed", step, f.code, undo)
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
	if in.noUpgrade {
		// (an update installs what is missing, and upgrades nothing that is there)
		argv = slices.Insert(argv, slices.Index(argv, "--no-remove")+1, "--no-upgrade")
	}
	if f := in.repairDpkg(); f != nil {
		return f
	}
	tail := &tailWriter{Writer: in.toolWriter()}
	in.aptStarted = true
	out, err := in.env.Runner.Run(in.ctx, Cmd{Argv: argv, Env: childEnv(in.env), Stdout: tail, Timeout: aptTimeout, Finish: aptFinish})
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
	f, err := createPrivate(in.archivePath())
	if err != nil {
		return in.fail(CodeDownloadFailed, err)
	}
	kept := false
	defer func() {
		if !kept {
			_ = f.Close()
			_ = os.Remove(in.archivePath())
		}
	}()
	for try := 1; try <= downloadTries; try++ {
		// the bytes are hashed as they are written
		h := sha256.New()
		_, err := dl.Get(in.ctx, in.asset.URL, io.MultiWriter(f, h))
		if err == nil {
			in.archive, in.archiveSum, kept = f, hex.EncodeToString(h.Sum(nil)), true
			return nil
		}
		lastErr = err
		// (the file is started over for the next try)
		if rerr := f.Truncate(0); rerr != nil {
			break
		} else if _, rerr = f.Seek(0, io.SeekStart); rerr != nil {
			break
		}
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

// closeArchive lets go of the archive; the file is taken away with the area of the download.
func (in *install) closeArchive() {
	if in.archive != nil {
		_ = in.archive.Close()
		in.archive = nil
	}
}

func (in *install) stepVerify() *stepFailure {
	if in.archive == nil || in.archiveSum != in.asset.SHA256 {
		in.closeArchive()
		_ = os.Remove(in.archivePath())
		return in.fail(CodeChecksumMismatch, errors.New("the sha256 of the archive is not the catalogue's"))
	}
	return nil
}

// openTar makes the tar of the archive that was checked, in a file of its own that is returned open. The archive that was checked is the one that is read: by the descriptor it was written
// by, and the tar it makes is written to a descriptor and read from the same one, so that nothing that is done to a path in the meantime reaches the unpack.
func (in *install) openTar() (*os.File, *stepFailure) {
	if _, err := in.archive.Seek(0, io.SeekStart); err != nil {
		return nil, in.fail(CodeUnpackFailed, err)
	}
	tarFile, err := createPrivate(in.tarPath())
	if err != nil {
		return nil, in.fail(CodeUnpackFailed, err)
	}
	if err := gunzipTo(in.ctx, in.archive, tarFile, osmodule.MaxStreamBytes); err != nil {
		_ = tarFile.Close()
		code := osmodule.UnpackCorrupt
		if errors.Is(err, ErrTooLarge) {
			code = osmodule.UnpackLimits
		}
		return nil, in.fail(code, err)
	}
	_ = os.Remove(in.archivePath())
	return tarFile, nil
}

func (in *install) stepUnpack() *stepFailure {
	tarFile, f := in.openTar()
	if f != nil {
		return f
	}
	defer tarFile.Close()
	// from here the box may hold files of the module
	in.unpackBegun = true
	if err := makeDirs(in.env.Roots.state(), 0o755); err != nil {
		return in.fail(CodeUnpackFailed, err)
	}
	// what an earlier install of the module that did not finish left: what is still as the list says it was is the module's, and may be replaced, and is removed with the rest
	list, err := readFilesEntries(in.env.Roots.files(in.entry.Name))
	if err != nil {
		return in.fail(CodeFilesUnreadable, err)
	}
	ownDirs, err := readDirsList(in.env.Roots.dirs(in.entry.Name))
	if err != nil {
		return in.fail(CodeFilesUnreadable, err)
	}
	earlier, err := in.stillTheirs(list)
	if err != nil {
		return in.fail(CodeUnpackFailed, err)
	}
	own := make([]string, len(earlier))
	for i, e := range earlier {
		own[i] = e.Path
	}
	res, err := osmodule.Unpack(tarFile, in.env.Host.NewDest(in.env.Roots.Prefix), own, osmodule.Options{
		Unit: in.entry.Manifest.Unit,
		// the lists are on the disk before the first directory is made and the first file is put in place: a kill from here on leaves a list that names everything that may be there, and what
		// each of them is to hold
		OnPlanned: func(files []string, want map[string]osmodule.Want, dirs []string) error {
			return in.recordPlanned(plannedEntries(earlier, files, want), union(ownDirs, dirs))
		},
	})
	in.written = &written{files: writtenEntries(earlier, res), dirs: union(res.Dirs, ownDirs)}
	if err != nil {
		// the lists name what the unpack was going to write, which may include the file that made it stop, and somebody else's: from here they name what was written, for a remove that has
		// to finish what the rollback could not
		_ = in.recordFiles(in.written.files)
		_ = in.recordDirs(in.written.dirs)
		var refused *osmodule.Refused
		if errors.As(err, &refused) {
			return in.fail(refused.Code, err)
		}
		return in.fail(CodeUnpackFailed, err)
	}
	// the lists are now exactly what was written, and the directories exactly the ones that were made: a path that was planned and is not there is not in them
	if err := in.recordFiles(in.written.files); err != nil {
		return in.fail(CodeUnpackFailed, err)
	}
	if err := in.recordDirs(in.written.dirs); err != nil {
		return in.fail(CodeUnpackFailed, err)
	}
	in.manifest = res.Manifest
	return nil
}

// union is the paths of a, then those of b that are not in a.
func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, p := range b {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// stillTheirs are the entries of the list of an earlier install that are still the module's: what is at the path is what the list says. A path where there is nothing was planned and never
// written; one where there is something else is somebody's now, and so is everything in a list of the earlier format, which says nothing to check: they are left, and said.
func (in *install) stillTheirs(list osmodule.FilesList) ([]osmodule.FileEntry, error) {
	var ours []osmodule.FileEntry
	for _, e := range list.Entries {
		if !list.Verified {
			in.say("left: %s (the list is of an earlier format)", printable(e.Path, 200))
			continue
		}
		switch h, err := holds(in.env.Roots.Path(e.Path), e); {
		case err != nil:
			return nil, err
		case h == holdsOurs:
			ours = append(ours, e)
		case h == holdsOther:
			in.say("left: %s (changed)", printable(e.Path, 200))
		}
	}
	return ours, nil
}

// plannedEntries are the entries of the list while the unpack is about to write: the files of the earlier install that this one does not write, and the files it will write, each with what it
// is to hold; a file that replaces one of the earlier install holds either, whichever is on the disk when a kill comes. (ponytail: a file that comes to replace a link, or the other way, has
// the content of the new one only, and the old one would be left, not deleted, by a remove after a kill in that instant.)
func plannedEntries(earlier []osmodule.FileEntry, files []string, want map[string]osmodule.Want) []osmodule.FileEntry {
	old := map[string]osmodule.FileEntry{}
	var out []osmodule.FileEntry
	for _, e := range earlier {
		old[e.Path] = e
		if _, replaced := want[e.Path]; !replaced {
			out = append(out, e)
		}
	}
	for _, p := range files {
		w := want[p]
		e := osmodule.FileEntry{Path: p, Link: w.Link, Want: []string{w.Value}}
		if o, ok := old[p]; ok && o.Link == w.Link && !slices.Contains(o.Want, w.Value) {
			e.Want = append(e.Want, o.Want...)
		}
		out = append(out, e)
	}
	return out
}

// writtenEntries are what the module owns once the unpack has returned, as it says it: the files of the earlier install that it did not write again, and the files it wrote.
func writtenEntries(earlier []osmodule.FileEntry, res osmodule.Result) []osmodule.FileEntry {
	var out []osmodule.FileEntry
	for _, e := range earlier {
		if _, again := res.Want[e.Path]; !again {
			out = append(out, e)
		}
	}
	for _, p := range res.Files {
		w := res.Want[p]
		out = append(out, osmodule.FileEntry{Path: p, Link: w.Link, Want: []string{w.Value}})
	}
	return out
}

// recordPlanned writes the list of the files and the list of the directories, whole, before the unpack changes anything.
func (in *install) recordPlanned(files []osmodule.FileEntry, dirs []string) error {
	if err := in.recordFiles(files); err != nil {
		return err
	}
	if err := in.recordDirs(dirs); err != nil {
		return err
	}
	syncDir(in.env.Roots.state())
	return nil
}

// recordFiles writes the list of the files, or takes it away when there are none.
func (in *install) recordFiles(files []osmodule.FileEntry) error {
	path := in.env.Roots.files(in.entry.Name)
	if len(files) == 0 {
		_, err := removeEntry(path)
		return err
	}
	raw, err := osmodule.FormatFilesList(files)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, raw, 0o644)
}

// recordDirs writes the list of the directories, or takes it away when there are none.
func (in *install) recordDirs(dirs []string) error {
	path := in.env.Roots.dirs(in.entry.Name)
	if len(dirs) == 0 {
		_, err := removeEntry(path)
		return err
	}
	raw, err := osmodule.FormatDirs(dirs)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, raw, 0o644)
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
	if err := makeDirs(in.env.Roots.manifests(), 0o755); err != nil {
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
