package modulecmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The update of an installed module: `casaos module update <name>`. It is an install that finds the module there. The new archive is checked and unpacked into a staging folder first, so
// that nothing of the box is changed until everything the new version is made of is known to be good; then the files that are going to be replaced are copied into a backup folder (under
// the area of the download, backup-<name>), the unit is stopped, the new files are put in place one by one, each by a rename, the lists, the manifest and the version are written last,
// and the unit is started again. A failure from the step stop on puts the backup back and starts the old unit; a run that is killed leaves the backup folder, and the next run of the
// module (an update, a remove or an install) puts it back before it does anything else. The backup is deleted when the update has ended, and not before.

// The steps of an update that an install does not have; the others are those of an install (packages, download, verify, unpack, units, start, route) and StepStop of a remove.
const (
	StepBackup = "backup"
	StepSwap   = "swap"
)

// The codes of an update besides the ones of the plan (osmodule.Refused*: not_installed, up_to_date, downgrade, ...) and of an install.
const (
	CodeBackupFailed       = "backup_failed"
	CodeSwapFailed         = "swap_failed"
	CodeRecoveryIncomplete = "recovery_incomplete"
)

// What the folder of a backup holds. COMPLETE is written last: a folder without it is a backup that was not finished, when nothing of the box had been changed yet.
const (
	backupComplete  = "COMPLETE"
	backupCopies    = "copies"           // copies/<n>: the n-th entry of backupActual, when it is a file
	backupActual    = "old.files.actual" // the files and links of the old list as they were found, with what each held
	backupFilesCopy = "old.files"        // the list of the files, as it was
	backupDirsCopy  = "old.dirs"         // the list of the directories, as it was (not there when the module had none)
	backupManifest  = "old.json"         // the manifest, as it was
	backupVersion   = "old.version"      // the version, as it was
	backupNewFiles  = "new.files"        // what the swap is going to put down (the list that the update ends with)
	backupNewDirs   = "new.dirs"         // the directories the swap is going to make (not there when it makes none)
)

// An update is the state of one run: an install (the packages, the archive, the lists) that finds the old version on the box.
type update struct {
	*install
	oldManifest osmodule.Manifest
	oldTag      string
	oldList     osmodule.FilesList
	oldDirs     []string
	merged      bool
	// ours are the entries of the old list that still hold what the list says
	ours []osmodule.FileEntry
	// staged is what the new archive unpacked to in the staging folder; newEntries the list of the files the update ends with, newDirs the directories it has to make (parents first)
	staged     osmodule.Result
	newEntries []osmodule.FileEntry
	newDirs    []string
	// stopBegun is set when the step stop begins: from there a failure is put back from the backup
	stopBegun bool
}

// canonPath is a path of a file with /lib spelled /usr/lib when the box has one directory for the two.
func canonPath(merged bool, p string) string {
	if rest, ok := strings.CutPrefix(p, "/lib/"); ok && merged {
		return "/usr/lib/" + rest
	}
	return p
}

func (u *update) canon(p string) string { return canonPath(u.merged, p) }

// runUpdate updates the module c.name: the plan, the question when there is to be one, then (holding the lock) what an earlier update that was killed left, the plan again, and the
// steps. It returns the exit code.
func runUpdate(ctx context.Context, env Env, c command) (code int) {
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
		plan := PlanUpdateOf(ctx, env, c.name)
		if why := planRefusal(c, plan, ""); why != "" {
			return refuseEarly(env, c.name, why)
		}
		if !env.Terminal() {
			return refuseEarly(env, c.name, CodeConfirmationNeeded)
		}
		if !newJob(env, c.name).confirm(plan.Name, plan.Tag, plan.Tier, plan.Packages.ToInstall, plan.Packages.DownloadBytes, plan.Archive.URL, "update") {
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
	if err := j.openLog(KindUpdate); err != nil {
		return refuseEarly(env, c.name, CodeLogUnwritable)
	}
	defer j.close()
	var u *update
	defer j.recoverRun(&code, func() string {
		if u != nil && u.stopBegun {
			return undoIncomplete
		}
		return undoDone
	})
	if err := emptyTmp(env.Host, env.Roots.tmp()); err != nil {
		if errors.Is(err, errUnsafe) {
			return j.refuse(CodeStateUnsafe)
		}
		return j.refuse(CodeTmpUnusable)
	}
	if code, stop := j.recoverUpdate(ctx); stop {
		return code
	}
	if bad := j.unreadableLists(c.name); len(bad) > 0 {
		for _, p := range bad {
			j.say("the list %s cannot be read: look at it, and delete it by hand, and remove the module, and install it again", printable(env.Roots.Slash(p), 200))
		}
		return j.refuse(CodeFilesUnreadable)
	}
	defer func() { _ = emptyTmp(env.Host, env.Roots.tmp()) }()

	plan := PlanUpdateOf(ctx, env, c.name)
	if why := planRefusal(c, plan, asked); why != "" {
		return j.refuse(why)
	}
	cat := loadCatalogue(env).cat
	entry, _ := cat.Find(c.name)
	asset, _ := entry.Asset(env.Arch)
	bx := &box{ctx: ctx, env: &env}
	st := bx.Installed(c.name)
	oldList, err := readFilesEntries(env.Roots.files(c.name))
	if err != nil || !oldList.Verified || len(oldList.Entries) == 0 {
		return j.refuse(CodeFilesUnreadable)
	}
	oldDirs, err := readDirsList(env.Roots.dirs(c.name))
	if err != nil {
		return j.refuse(CodeFilesUnreadable)
	}
	merged, _ := bx.MergedUsr()
	u = &update{
		install:     &install{job: j, ctx: ctx, entry: entry, asset: asset, packages: plan.Update.Added},
		oldManifest: st.Manifest, oldTag: st.Tag, oldList: oldList, oldDirs: oldDirs, merged: merged,
	}
	defer u.closeArchive()
	return u.steps()
}

func (u *update) steps() int {
	steps := []struct {
		name string
		do   func() *stepFailure
	}{
		{StepPackages, u.stepPackages},
		{StepDownload, u.stepDownload},
		{StepVerify, u.stepVerify},
		{StepUnpack, u.stepUnpack},
		{StepBackup, u.stepBackup},
		{StepStop, u.stepStop},
		{StepSwap, u.stepSwap},
		{StepUnits, u.stepUnits},
		{StepStart, u.stepStart},
		{StepRoute, u.stepRoute},
	}
	for _, s := range steps {
		u.marker(s.name, "begin")
		u.hook(u.ctx, s.name, "begin")
		if s.name == StepStop {
			u.stopBegun = true
		}
		var f *stepFailure
		if err := u.ctx.Err(); err != nil {
			f = &stepFailure{CodeInterrupted, err}
		} else {
			f = u.guarded(s.do)
		}
		if f != nil {
			u.marker(s.name, "failed")
			return u.failed(s.name, f)
		}
		u.marker(s.name, "done")
		u.hook(u.ctx, s.name, "done")
		if s.name == StepPackages {
			u.packagesDone = true
		}
	}
	u.dropBackup()
	u.marker(StepDone, "done")
	u.say("updated: %s %s -> %s", u.entry.Name, u.oldTag, u.entry.Tag)
	u.result("ok", StepDone, "-", "-")
	return ExitOK
}

// failed puts the box back as it was when the update had begun to change it, and says what was kept.
func (u *update) failed(step string, f *stepFailure) int {
	u.say("failed at step %s: %s", step, f.code)
	undo := undoDone
	if u.stopBegun {
		if bad := u.rollback(); bad > 0 {
			u.say("rollback incomplete: %d things could not be put back; the files of the old version are kept in %s, and the next casaos module update or remove %s puts them back", bad, printable(u.env.Roots.Slash(u.env.Roots.backup(u.entry.Name)), 200), u.entry.Name)
			undo = undoIncomplete
		} else {
			u.say("rolled back")
		}
	} else {
		// (nothing of the box was changed: whatever there is of a backup is a copy)
		u.dropBackup()
	}
	if u.aptStarted && step == StepPackages && (f.code == CodeInterrupted || errors.Is(f.err, context.DeadlineExceeded)) {
		u.say("apt-get was ended while it worked: dpkg --configure -a is probably needed")
	}
	u.say("kept_packages: %s", u.keptPackages())
	u.result("failed", step, f.code, undo)
	return ExitFailed
}

// rollback puts the old version back from the backup; what it does is not stopped by the signal that interrupted the run, and a panic in it is a rollback that could not be finished.
func (u *update) rollback() (bad int) {
	defer func() {
		if r := recover(); r != nil {
			u.panicked(r)
			u.failOpen()
			bad = 1
		}
	}()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(u.ctx), rollbackTime)
	defer cancel()
	bad, _ = u.restoreBackup(ctx, u.entry.Name, "rollback_")
	return bad
}

// dropBackup takes the backup folder away, once the update has ended or when nothing of the box was changed.
func (u *update) dropBackup() {
	dir := u.env.Roots.backup(u.entry.Name)
	if err := os.RemoveAll(dir); err != nil {
		u.say("not removed: %s (%s)", printable(u.env.Roots.Slash(dir), 200), printable(err.Error(), 100))
	}
}

// ---- the steps ---------------------------------------------------------------------------------------------------------------------------------------------------------------

// unpackFailure is the failure of a step that unpacks: the code of a refusal of the archive, or unpack_failed.
func (u *update) unpackFailure(err error) *stepFailure {
	var refused *osmodule.Refused
	if errors.As(err, &refused) {
		return u.fail(refused.Code, err)
	}
	return u.fail(CodeUnpackFailed, err)
}

// stepUnpack checks the archive against the box as it is (what is in the way, what is not the module's: nothing is written), unpacks it into the staging folder, where it is checked
// against the same rules and limits as an install's, and checks the manifest it carries. Nothing of the box is changed.
func (u *update) stepUnpack() *stepFailure {
	tarFile, f := u.openTar()
	if f != nil {
		return f
	}
	defer tarFile.Close()
	ours, err := u.stillTheirs(u.oldList)
	if err != nil {
		return u.fail(CodeUnpackFailed, err)
	}
	u.ours = ours
	own := make([]string, len(ours))
	for i, e := range ours {
		own[i] = e.Path
	}
	opts := osmodule.Options{Unit: u.entry.Manifest.Unit}
	// the box as it is: a file of the new version that is there and is not the module's (changed by the owner, or somebody else's) refuses the update
	if _, err := osmodule.ValidateArchive(tarFile, u.env.Host.NewDest(u.env.Roots.Prefix), own, opts); err != nil {
		return u.unpackFailure(err)
	}
	stage := u.env.Roots.stage()
	if err := makeDirs(stage, 0o700); err != nil {
		return u.fail(CodeUnpackFailed, err)
	}
	res, err := osmodule.Unpack(tarFile, u.env.Host.NewDest(stage), nil, opts)
	if err != nil {
		return u.unpackFailure(err)
	}
	u.staged, u.manifest = res, res.Manifest
	m, err := osmodule.Parse(osmodule.FileName(u.entry.Name), u.manifest)
	if err != nil {
		return u.fail(CodeManifestInvalid, err)
	}
	if !manifestMatches(m, u.entry.Manifest) {
		return u.fail(CodeManifestMismatch, errors.New("the manifest of the archive is not the catalogue's"))
	}
	for _, p := range res.Files {
		w := res.Want[p]
		u.newEntries = append(u.newEntries, osmodule.FileEntry{Path: p, Link: w.Link, Want: []string{w.Value}})
	}
	u.newDirs = missingDirs(u.env.Host.NewDest(u.env.Roots.Prefix), res.Files)
	return nil
}

// missingDirs are the directories above the files that the box does not have, parents first.
func missingDirs(dst osmodule.Destination, files []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range files {
		at := ""
		for _, part := range strings.Split(strings.TrimPrefix(path.Dir(f), "/"), "/") {
			at += "/" + part
			if seen[at] {
				continue
			}
			seen[at] = true
			if _, err := dst.Lstat(at); errors.Is(err, fs.ErrNotExist) {
				out = append(out, at)
			}
		}
	}
	return out
}

// copyFileTo copies the regular file src (not through a link) to dst with the permissions it has, and gives the sha256 of what it copied.
func copyFileTo(src, dst string) (string, error) {
	in, err := os.OpenFile(src, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return "", err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	out, err := createPrivate(dst)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), in)
	if err == nil {
		err = syncFile(out)
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(dst, info.Mode().Perm())
	}
	if err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// stepBackup copies everything the swap is going to replace or delete into the backup folder, with the lists, the manifest and the version, and the list of what the swap is going to put
// down; the folder is complete when COMPLETE is there. Nothing of the box is changed.
func (u *update) stepBackup() *stepFailure {
	roots := u.env.Roots
	dir := roots.backup(u.entry.Name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return u.fail(CodeBackupFailed, err)
	}
	if err := os.Mkdir(filepath.Join(dir, backupCopies), 0o700); err != nil {
		return u.fail(CodeBackupFailed, err)
	}
	var actual []osmodule.FileEntry
	for _, e := range u.oldList.Entries {
		if err := u.ctx.Err(); err != nil {
			return u.fail(CodeBackupFailed, err)
		}
		real := roots.Path(e.Path)
		info, err := os.Lstat(real)
		switch {
		case errors.Is(err, fs.ErrNotExist) || isNotDir(err):
			// (taken away by the owner: there is nothing to keep)
		case err != nil:
			return u.fail(CodeBackupFailed, err)
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(real)
			if err != nil {
				return u.fail(CodeBackupFailed, err)
			}
			actual = append(actual, osmodule.FileEntry{Path: e.Path, Link: true, Want: []string{target}})
		case info.Mode().IsRegular():
			sum, err := copyFileTo(real, filepath.Join(dir, backupCopies, strconv.Itoa(len(actual))))
			if err != nil {
				return u.fail(CodeBackupFailed, err)
			}
			actual = append(actual, osmodule.FileEntry{Path: e.Path, Want: []string{sum}})
		default:
			return u.fail(CodeBackupFailed, fmt.Errorf("%s is not a file nor a link", printable(e.Path, 200)))
		}
	}
	put := func(name string, raw []byte) error { return writeFileAtomic(filepath.Join(dir, name), raw, 0o600) }
	if len(actual) > 0 {
		raw, err := osmodule.FormatFilesList(actual)
		if err == nil {
			err = put(backupActual, raw)
		}
		if err != nil {
			return u.fail(CodeBackupFailed, err)
		}
	}
	for _, c := range []struct {
		from, to string
		optional bool
	}{
		{roots.files(u.entry.Name), backupFilesCopy, false},
		{roots.dirs(u.entry.Name), backupDirsCopy, true},
		{roots.manifest(u.entry.Name), backupManifest, false},
		{roots.version(u.entry.Name), backupVersion, false},
	} {
		raw, err := readRegular(c.from, int64(osmodule.MaxFilesBytes))
		if errors.Is(err, fs.ErrNotExist) && c.optional {
			continue
		}
		if err == nil {
			err = put(c.to, raw)
		}
		if err != nil {
			return u.fail(CodeBackupFailed, err)
		}
	}
	raw, err := osmodule.FormatFilesList(u.newEntries)
	if err == nil {
		err = put(backupNewFiles, raw)
	}
	if err == nil && len(u.newDirs) > 0 {
		if raw, err = osmodule.FormatDirs(u.newDirs); err == nil {
			err = put(backupNewDirs, raw)
		}
	}
	if err != nil {
		return u.fail(CodeBackupFailed, err)
	}
	syncDir(filepath.Join(dir, backupCopies))
	syncDir(dir)
	if err := put(backupComplete, []byte("complete\n")); err != nil {
		return u.fail(CodeBackupFailed, err)
	}
	syncDir(dir)
	syncDir(roots.tmp())
	return nil
}

// stopOnly stops a unit, and asks whether it is stopped: the unit stays enabled.
func (j *job) stopOnly(ctx context.Context, unit string) bool {
	_, _ = j.sh(ctx, systemctlTimeout, "systemctl", "stop", unit)
	if !j.unitIsStopped(ctx, unit) {
		j.say("the unit %s is not known to be stopped", printable(unit, 80))
		return false
	}
	return true
}

func (u *update) stepStop() *stepFailure {
	stuck := 0
	for _, unit := range unitNames(u.oldManifest.Unit, u.oldList.Paths()) {
		if !u.stopOnly(u.ctx, unit) {
			stuck++
		}
	}
	if stuck > 0 {
		return u.fail(CodeUnitNotStopped, errors.New("a unit could not be stopped"))
	}
	return nil
}

// putFile puts the regular file src at the path abs of the box, whole or not at all, and checks that what was put is the sha256 sum. replace says the file at abs is the module's own.
func putFile(dst osmodule.Destination, abs, src, sum string, replace bool) error {
	in, err := os.OpenFile(src, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	file, err := dst.NewFile(abs, info.Mode().Perm(), replace)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(file, h), in); err != nil {
		file.Abort()
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != sum {
		file.Abort()
		return errors.New("what was read is not what was saved")
	}
	if err := file.Commit(); err != nil {
		file.Abort()
		return err
	}
	return nil
}

// stepSwap puts the new version in place: the directories it needs, then each file by a rename (the unit last), then the files of the old version that the new one does not have
// (only the ones that still hold what the old list says), then the lists, the manifest and the version, each written whole or not at all.
func (u *update) stepSwap() *stepFailure {
	roots := u.env.Roots
	dst := u.env.Host.NewDest(roots.Prefix)
	stage := Roots{Prefix: roots.stage()}
	for _, d := range u.newDirs {
		switch err := dst.Mkdir(d, 0o755); {
		case err == nil:
		case errors.Is(err, fs.ErrExist):
			if mode, err := dst.Lstat(d); err != nil || !mode.IsDir() {
				return u.fail(osmodule.UnpackPath, errors.New("a directory of the new version is not one"))
			}
		default:
			return u.fail(CodeSwapFailed, err)
		}
	}
	replaces := map[string]bool{}
	for _, e := range u.ours {
		replaces[u.canon(e.Path)] = true
	}
	newPaths := map[string]bool{}
	touched := map[string]bool{}
	for _, p := range u.staged.Files {
		if err := u.ctx.Err(); err != nil {
			return u.fail(CodeSwapFailed, err)
		}
		w := u.staged.Want[p]
		newPaths[u.canon(p)] = true
		replace := replaces[u.canon(p)]
		var err error
		if w.Link {
			err = dst.Symlink(w.Value, p, replace)
		} else {
			err = putFile(dst, p, stage.Path(p), w.Value, replace)
		}
		if errors.Is(err, fs.ErrExist) {
			return u.fail(osmodule.FileExists, err)
		}
		if err != nil {
			return u.fail(CodeSwapFailed, err)
		}
		touched[filepath.Dir(roots.Path(p))] = true
	}
	// what the new version does not have: taken away when it is what the old list says, left (and said) when somebody changed it
	for _, e := range u.oldList.Entries {
		if newPaths[u.canon(e.Path)] {
			continue
		}
		real := roots.Path(e.Path)
		switch h, err := holds(real, e); {
		case err != nil:
			return u.fail(CodeSwapFailed, err)
		case h == holdsOther:
			u.say("left: %s (changed)", printable(e.Path, 200))
		case h == holdsOurs:
			if _, err := removeEntry(real); err != nil {
				return u.fail(CodeSwapFailed, err)
			}
			touched[filepath.Dir(real)] = true
		}
	}
	for d := range touched {
		syncDir(d)
	}
	// the lists, the manifest and the version, last
	if err := u.recordFiles(u.newEntries); err != nil {
		return u.fail(CodeSwapFailed, err)
	}
	made := slices.Clone(u.newDirs)
	slices.Reverse(made)
	if err := u.recordDirs(union(made, u.oldDirs)); err != nil {
		return u.fail(CodeSwapFailed, err)
	}
	if err := makeDirs(roots.manifests(), 0o755); err != nil {
		return u.fail(CodeSwapFailed, err)
	}
	if err := writeFileAtomic(roots.manifest(u.entry.Name), u.manifest, 0o644); err != nil {
		return u.fail(CodeSwapFailed, err)
	}
	if err := writeFileAtomic(roots.version(u.entry.Name), []byte(u.entry.Tag+"\n"), 0o644); err != nil {
		return u.fail(CodeSwapFailed, err)
	}
	syncDir(roots.manifests())
	syncDir(roots.state())
	return nil
}

func (u *update) stepStart() *stepFailure {
	if out, err := u.sh(u.ctx, systemctlTimeout, "systemctl", "start", u.entry.Manifest.Unit); err != nil || out.ExitCode != 0 {
		return u.fail(CodeStartFailed, errors.New("systemctl start failed"))
	}
	return nil
}

// ---- putting the backup back --------------------------------------------------------------------------------------------------------------------------------------------------

// A backupState is a backup folder as it was read: every list in it has passed the parser of its kind.
type backupState struct {
	actual            osmodule.FilesList
	files, dirs       []byte
	manifest, version []byte
	parsed            osmodule.Manifest
	oldPaths          []string
	oldDirs           []string
	newList           osmodule.FilesList
	newDirs           []string
}

var errBackupUnfinished = errors.New("the backup was not finished")

// readBackup reads the folder of a backup. A folder without COMPLETE is errBackupUnfinished (an update that was ended while it copied, when nothing of the box had been changed); any
// other error is a backup that cannot be trusted, and is kept.
func readBackup(name, dir string) (b backupState, err error) {
	switch info, err := os.Lstat(filepath.Join(dir, backupComplete)); {
	case errors.Is(err, fs.ErrNotExist) || isNotDir(err):
		return b, errBackupUnfinished
	case err != nil:
		return b, err
	case !info.Mode().IsRegular():
		return b, errors.New("COMPLETE is not a file")
	}
	read := func(file string, optional bool) ([]byte, error) {
		raw, err := readRegular(filepath.Join(dir, file), int64(osmodule.MaxFilesBytes))
		if errors.Is(err, fs.ErrNotExist) && optional {
			return nil, nil
		}
		return raw, err
	}
	var raw []byte
	list := func(file string, optional bool) (osmodule.FilesList, error) {
		raw, err := read(file, optional)
		if err != nil || raw == nil {
			return osmodule.FilesList{}, err
		}
		l, err := osmodule.ParseFilesList(raw)
		if err == nil && !l.Verified {
			err = errors.New("a list of the earlier format")
		}
		return l, err
	}
	if b.actual, err = list(backupActual, true); err != nil {
		return b, err
	}
	old, err := list(backupFilesCopy, false)
	if err != nil {
		return b, err
	}
	b.oldPaths = old.Paths()
	if b.files, err = read(backupFilesCopy, false); err != nil {
		return b, err
	}
	if b.dirs, err = read(backupDirsCopy, true); err != nil {
		return b, err
	}
	if b.dirs != nil {
		if b.oldDirs, err = osmodule.ParseDirs(b.dirs); err != nil {
			return b, err
		}
	}
	if b.manifest, err = read(backupManifest, false); err != nil {
		return b, err
	}
	if b.parsed, err = osmodule.Parse(osmodule.FileName(name), b.manifest); err != nil {
		return b, err
	}
	if b.version, err = read(backupVersion, false); err != nil {
		return b, err
	}
	if !tagRe.MatchString(strings.TrimSpace(string(b.version))) {
		return b, errors.New("the saved version is not a tag")
	}
	if b.newList, err = list(backupNewFiles, false); err != nil {
		return b, err
	}
	if raw, err = read(backupNewDirs, true); err != nil {
		return b, err
	}
	if raw != nil {
		if b.newDirs, err = osmodule.ParseDirs(raw); err != nil {
			return b, err
		}
	}
	return b, nil
}

// putBack puts the file or the link the backup has of a path at the path.
func putBack(dst osmodule.Destination, roots Roots, dir string, k int, a osmodule.FileEntry) error {
	if err := makeDirs(filepath.Dir(roots.Path(a.Path)), 0o755); err != nil {
		return err
	}
	if a.Link {
		return dst.Symlink(a.Want[0], a.Path, true)
	}
	return putFile(dst, a.Path, filepath.Join(dir, backupCopies, strconv.Itoa(k)), a.Want[0], true)
}

// restoreBackup puts the box back as the backup of the update of the module called name has it, as steps behind prefix: restore (stop the unit; put back what is not what the backup holds,
// take away what only the new version put down, write the lists, the manifest and the version as they were), units (systemd reads them again) and start. What it does is idempotent, so that
// a run that is ended in the middle of it is put back by the next. The backup folder is deleted at the end, and only when everything was put back; it returns what it could not do, and the
// step it was in.
func (j *job) restoreBackup(ctx context.Context, name, prefix string) (bad int, step string) {
	roots := j.env.Roots
	dir := roots.backup(name)
	b, err := readBackup(name, dir)
	switch {
	case errors.Is(err, errBackupUnfinished):
		j.say("the backup of %s was not finished, so nothing of the box had been changed: it is dropped", name)
		if err := os.RemoveAll(dir); err != nil {
			j.say("not removed: %s (%s)", printable(roots.Slash(dir), 200), printable(err.Error(), 100))
			return 1, ""
		}
		return 0, ""
	case err != nil:
		j.say("the backup in %s cannot be read (%s): nothing was put back, and it is kept", printable(roots.Slash(dir), 200), printable(err.Error(), 100))
		return 1, ""
	}
	units := unitNames(b.parsed.Unit, b.oldPaths)
	run := func(s string, do func() int) bool {
		j.marker(prefix+s, "begin")
		j.hook(ctx, prefix+s, "begin")
		if n := do(); n > 0 {
			j.marker(prefix+s, "failed")
			bad, step = n, prefix+s
			return false
		}
		j.marker(prefix+s, "done")
		j.hook(ctx, prefix+s, "done")
		return true
	}
	if !run("restore", func() int { return j.putBackFiles(ctx, name, dir, b, units) }) {
		return bad, step
	}
	if !run("units", func() int {
		if out, err := j.sh(ctx, systemctlTimeout, "systemctl", "daemon-reload"); err != nil || out.ExitCode != 0 {
			j.say("systemctl daemon-reload failed")
			return 1
		}
		return 0
	}) {
		return bad, step
	}
	if !run("start", func() int {
		failed := 0
		for _, unit := range units {
			if out, err := j.sh(ctx, systemctlTimeout, "systemctl", "start", unit); err != nil || out.ExitCode != 0 {
				j.say("the unit %s could not be started", printable(unit, 80))
				failed++
			}
		}
		return failed
	}) {
		return bad, step
	}
	if err := os.RemoveAll(dir); err != nil {
		j.say("not removed: %s (%s)", printable(roots.Slash(dir), 200), printable(err.Error(), 100))
		return 1, ""
	}
	return 0, ""
}

// putBackFiles is the step restore. A path is put back when it holds the new file or is gone; one that holds neither the old file nor the new one is somebody's, and is left (and the
// rollback is not complete).
func (j *job) putBackFiles(ctx context.Context, name, dir string, b backupState, units []string) (bad int) {
	roots := j.env.Roots
	merged, _ := (&box{ctx: ctx, env: &j.env}).MergedUsr()
	canon := func(p string) string { return canonPath(merged, p) }
	stopFailed := false
	for _, unit := range units {
		if !j.stopOnly(ctx, unit) {
			stopFailed = true
		}
	}
	dst := j.env.Host.NewDest(roots.Prefix)
	newBy := map[string]osmodule.FileEntry{}
	for _, e := range b.newList.Entries {
		newBy[canon(e.Path)] = e
	}
	saved := map[string]bool{}
	acted := 0
	touched := map[string]bool{}
	for k, a := range b.actual.Entries {
		saved[canon(a.Path)] = true
		real := roots.Path(a.Path)
		h, err := holds(real, a)
		if err == nil && h == holdsOther {
			// not the old file: the new one is what the swap put there
			if n, ok := newBy[canon(a.Path)]; ok {
				if hn, err := holds(real, n); err == nil && hn == holdsOurs {
					h = holdsNothing
				}
			}
		}
		switch {
		case err != nil:
			j.say("not put back: %s (%s)", printable(a.Path, 200), printable(err.Error(), 100))
			bad++
			continue
		case h == holdsOurs:
			continue
		case h == holdsOther:
			j.say("left: %s (it is neither the old file nor the new one)", printable(a.Path, 200))
			bad++
			continue
		}
		acted++
		if err := putBack(dst, roots, dir, k, a); err != nil {
			j.say("not put back: %s (%s)", printable(a.Path, 200), printable(err.Error(), 100))
			bad++
			continue
		}
		touched[filepath.Dir(real)] = true
	}
	for _, n := range b.newList.Entries {
		if saved[canon(n.Path)] {
			continue
		}
		real := roots.Path(n.Path)
		switch h, err := holds(real, n); {
		case err != nil:
			j.say("not removed: %s (%s)", printable(n.Path, 200), printable(err.Error(), 100))
			bad++
		case h == holdsOurs:
			acted++
			if _, err := removeEntry(real); err != nil {
				j.say("not removed: %s (%s)", printable(n.Path, 200), printable(err.Error(), 100))
				bad++
			}
			touched[filepath.Dir(real)] = true
		case h == holdsOther:
			j.say("left: %s (changed)", printable(n.Path, 200))
		}
	}
	// the lists, the manifest and the version, as they were
	state := func(p string, raw []byte) {
		var err error
		if raw == nil {
			_, err = removeEntry(p)
		} else {
			err = writeFileAtomic(p, raw, 0o644)
		}
		if err != nil {
			j.say("not put back: %s (%s)", printable(roots.Slash(p), 200), printable(err.Error(), 100))
			bad++
		}
	}
	if err := makeDirs(roots.manifests(), 0o755); err != nil {
		bad++
	}
	state(roots.manifest(name), b.manifest)
	state(roots.version(name), b.version)
	state(roots.files(name), b.files)
	state(roots.dirs(name), b.dirs)
	for _, d := range sortedDeepestFirst(b.newDirs) {
		if slices.Contains(b.oldDirs, d) {
			continue
		}
		touched[filepath.Dir(roots.Path(d))] = true
		if err := removeEmptyDir(roots.Path(d)); err != nil {
			j.say("not removed: %s (%s)", printable(d, 200), printable(err.Error(), 100))
			bad++
		}
	}
	for d := range touched {
		syncDir(d)
	}
	syncDir(roots.state())
	if stopFailed && acted > 0 {
		j.say("a unit could not be stopped while its files were put back")
		bad++
	}
	return bad
}

// recoverUpdate puts back what an update of the module that was killed (or whose rollback was) left in its backup folder, before the run that finds it does anything else. It returns
// true with the exit code when the run is over: the recovery could not be finished, and the result line says so.
func (j *job) recoverUpdate(ctx context.Context) (code int, stop bool) {
	dir := j.env.Roots.backup(j.name)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) || isNotDir(err) {
		return 0, false
	}
	j.say("an update of %s was ended before it was over: the files of the old version are put back from %s", j.name, printable(j.env.Roots.Slash(dir), 200))
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTime)
	defer cancel()
	bad, step := j.restoreBackup(rctx, j.name, "recover_")
	if bad > 0 {
		j.say("rollback incomplete: %d things could not be put back; the backup is kept in %s", bad, printable(j.env.Roots.Slash(dir), 200))
		if step == "" {
			step = "-"
		} else {
			j.say("failed at step %s: %s", step, CodeRecoveryIncomplete)
		}
		j.result("failed", step, CodeRecoveryIncomplete, undoIncomplete)
		return ExitFailed, true
	}
	j.say("recovered: %s is as it was before the update", j.name)
	return 0, false
}
