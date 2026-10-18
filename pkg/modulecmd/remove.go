package modulecmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The steps of a remove.
const (
	StepStop      = "stop"
	StepFiles     = "files"
	StepManifest2 = "manifest"
	StepReload    = "reload"
)

var unitNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@-]{0,63}\.service$`)

// unitNames are the units to stop: the one the manifest names, and every service among the files the module put in the directories of units.
func unitNames(manifestUnit string, list []string) []string {
	var out []string
	add := func(u string) {
		if unitNameRe.MatchString(u) && !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	add(manifestUnit)
	for _, p := range list {
		if strings.HasPrefix(p, "/usr/lib/systemd/system/") || strings.HasPrefix(p, "/lib/systemd/system/") {
			add(path.Base(p))
		}
	}
	return out
}

// unitIsStopped asks systemd, and only the two words that say a unit is not running count as stopped: "inactive" (which is also what a unit that systemd does not know says) and "failed".
// An error, no answer, any other word (activating, deactivating, reloading, active, anything) is a unit that is not known to be stopped, and the files stay. It is asked on a context of its
// own, bounded: a run that was told to stop must still be able to find out, since the answer is what decides whether anything is deleted.
func (j *job) unitIsStopped(ctx context.Context, unit string) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), systemctlTimeout)
	defer cancel()
	out, err := j.env.Runner.Run(ctx, Cmd{Argv: []string{"systemctl", "is-active", unit}, Env: childEnv(j.env), Timeout: systemctlTimeout})
	if err != nil {
		return false
	}
	switch strings.TrimSpace(out.Text) {
	case "inactive", "failed":
		return true
	}
	return false
}

// stopUnit stops and disables a unit, and then asks whether it is stopped: what `systemctl disable --now` said (or that the run was ended while it ran) does not decide that.
func (j *job) stopUnit(ctx context.Context, unit string) bool {
	out, err := j.sh(ctx, systemctlTimeout, "systemctl", "disable", "--now", unit)
	if !j.unitIsStopped(ctx, unit) {
		j.say("the unit %s is not known to be stopped", printable(unit, 80))
		return false
	}
	if err != nil || out.ExitCode != 0 {
		j.say("the unit %s could not be disabled; it is not running", printable(unit, 80))
	}
	return true
}

// removeEntry removes the file or the link at a path of the box: gone already is done, a directory is not removed. It gives whether there was something, and why not when it failed.
func removeEntry(real string) (removed bool, err error) {
	info, err := os.Lstat(real)
	switch {
	case errors.Is(err, fs.ErrNotExist) || isNotDir(err):
		return false, nil
	case err != nil:
		return false, err
	case info.IsDir():
		return false, errors.New("a directory is where a file was")
	}
	if err := os.Remove(real); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	return true, nil
}

// written is what an install put down, as the unpack says it: the files and links with what each holds, and the directories it made (the deepest first).
type written struct {
	files []osmodule.FileEntry
	dirs  []string
}

// teardown undoes an install, whatever state it is in: the units are stopped and disabled, the files that are still there (and are no other module's) are deleted, then the directories the
// install made, if they are empty, then the manifest, the version and the lists, then systemd reads its units again. What it deletes is what only holds, when it is given (the rollback of an
// install that stopped: what the unpack wrote, and not what it was going to write, which may name a file that is somebody else's and made it stop), and otherwise the lists of the module
// on the disk (a remove: the lists name everything the install may have put down, and a kill leaves nothing else; a directory that somebody else made in the instant between the checking and
// the making is in them, and is deleted if it is empty). It returns what it could not do, with the step it was in. Nothing that is
// the module's is forgotten until all of it is gone: a list, a manifest or a version stays while a file does, and a run again finishes the work. markers puts the phases in the log as
// steps (remove does, the rollback of an install does not).
func (j *job) teardown(ctx context.Context, name, manifestUnit, prefix string, only *written) (bad int, phase, code string) {
	step := func(s string) {
		j.marker(prefix+s, "begin")
		j.hook(ctx, prefix+s, "begin")
	}
	end := func(s string, bad int) {
		if bad > 0 {
			j.marker(prefix+s, "failed")
			return
		}
		j.marker(prefix+s, "done")
		j.hook(ctx, prefix+s, "done")
	}
	roots := j.env.Roots
	var (
		list       osmodule.FilesList
		dirs       []string
		unreadable []string
	)
	if only != nil {
		list, dirs = osmodule.FilesList{Entries: only.files, Verified: true}, only.dirs
	} else {
		var err error
		if list, err = readFilesEntries(roots.files(name)); err != nil {
			unreadable = append(unreadable, roots.files(name))
		}
		if dirs, err = readDirsList(roots.dirs(name)); err != nil {
			unreadable = append(unreadable, roots.dirs(name))
		}
		if list.Entries == nil && len(unreadable) == 0 {
			j.say("there is no list of the files of %s: no file is deleted", name)
		}
	}
	// stop: the unit of the module is stopped whatever the lists are, since the manifest names it
	step(StepStop)
	stuck := 0
	for _, unit := range unitNames(manifestUnit, list.Paths()) {
		if !j.stopUnit(ctx, unit) {
			stuck++
		}
	}
	bad, code = stuck, CodeUnitNotStopped
	if bad == 0 && ctx.Err() != nil {
		// told to stop, and nothing is deleted yet: the run stops here, and a run again goes on
		bad, code = 1, CodeInterrupted
	}
	end(StepStop, bad)
	if bad > 0 {
		j.say("nothing was deleted")
		return bad, StepStop, code
	}
	// What follows is done whole, whatever the run is told: the unit is stopped, and a module that has lost its program and not its unit, or its files and not its manifest, is worse than
	// either end. It is a few unlinks and one systemctl, bounded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTime)
	defer cancel()
	// files
	step(StepFiles)
	if len(unreadable) > 0 {
		// A list that cannot be read (cut short, or naming what an install never writes) is not trusted, so nothing is deleted by it, and a run again would read it again: it is for the owner to
		// look at and to delete.
		for _, p := range unreadable {
			j.say("the list %s cannot be read: nothing was deleted by it", printable(roots.Slash(p), 200))
			j.say("look at it, and delete %s by hand; then run casaos module remove %s again", printable(roots.Slash(p), 200), name)
		}
		end(StepFiles, len(unreadable))
		return len(unreadable), StepFiles, CodeFilesUnreadable
	}
	others, err := (&box{ctx: ctx, env: &j.env}).FilesLists()
	if err != nil {
		j.say("the lists of the files of the other modules cannot be read")
		end(StepFiles, 1)
		return 1, StepFiles, CodeRemoveIncomplete
	}
	owner := map[string]string{}
	for _, other := range sortedKeys(others) {
		if other == name {
			continue
		}
		for _, p := range others[other] {
			if _, ok := owner[p]; !ok {
				owner[p] = other
			}
		}
	}
	failed := 0
	for _, e := range list.Entries {
		p := e.Path
		real := roots.Path(p)
		// (the temporary name an unpack writes under is the module's whatever the file is)
		if _, err := removeEntry(tempName(real)); err != nil {
			j.say("not removed: %s (%s)", printable(p, 200), printable(err.Error(), 100))
			failed++
		}
		if other, ok := owner[p]; ok {
			j.say("kept, it is the list of %s too: %s", other, printable(p, 200))
			continue
		}
		if !list.Verified {
			j.say("left: %s (the list is of an earlier format: if it is the module's, delete it by hand)", printable(p, 200))
			continue
		}
		switch h, err := holds(real, e); {
		case err != nil:
			j.say("not removed: %s (%s)", printable(p, 200), printable(err.Error(), 100))
			failed++
		case h == holdsOther:
			// not what the install put there, or not what it was to put: somebody else's, or the half of a write that was never done
			j.say("left: %s (changed)", printable(p, 200))
		case h == holdsOurs:
			if _, err := removeEntry(real); err != nil {
				j.say("not removed: %s (%s)", printable(p, 200), printable(err.Error(), 100))
				failed++
			}
		}
	}
	// the directories the install made, the deepest first, those that are empty: one that holds anything is somebody's, and stays
	for _, d := range sortedDeepestFirst(dirs) {
		if err := removeEmptyDir(roots.Path(d)); err != nil {
			j.say("not removed: %s (%s)", printable(d, 200), printable(err.Error(), 100))
			failed++
		}
	}
	end(StepFiles, failed)
	if failed > 0 {
		return failed, StepFiles, CodeRemoveIncomplete
	}
	// manifest: the lists stay until systemd has read its units again, so that a run again can do it again
	step(StepManifest2)
	gone := 0
	for _, p := range []string{roots.manifest(name), roots.version(name), tempName(roots.manifest(name)), tempName(roots.version(name)), tempName(roots.files(name)), tempName(roots.dirs(name))} {
		if _, err := removeEntry(p); err != nil {
			j.say("not removed: %s", printable(err.Error(), 100))
			gone++
		}
	}
	end(StepManifest2, gone)
	if gone > 0 {
		return gone, StepManifest2, CodeRemoveIncomplete
	}
	// reload
	step(StepReload)
	reload := 0
	if out, err := j.sh(ctx, systemctlTimeout, "systemctl", "daemon-reload"); err != nil || out.ExitCode != 0 {
		j.say("systemctl daemon-reload failed")
		reload++
	}
	if reload == 0 {
		for _, p := range []string{roots.files(name), roots.dirs(name)} {
			if _, err := removeEntry(p); err != nil {
				j.say("not removed: %s", printable(err.Error(), 100))
				reload++
			}
		}
	}
	end(StepReload, reload)
	if reload > 0 {
		return reload, StepReload, CodeUnitsFailed
	}
	return 0, "", ""
}

// sortedDeepestFirst is the paths, the ones with the most parts first.
func sortedDeepestFirst(paths []string) []string {
	out := slices.Clone(paths)
	slices.SortStableFunc(out, func(a, b string) int { return strings.Count(b, "/") - strings.Count(a, "/") })
	return out
}

// removeEmptyDir deletes a directory that is empty. Gone already is done, and so is a directory that holds something, which is not the install's to empty; what is there and is not a
// directory (a file or a link, which the delete of a file would take away) is left alone.
func removeEmptyDir(real string) error {
	info, err := os.Lstat(real)
	switch {
	case errors.Is(err, fs.ErrNotExist) || isNotDir(err):
		return nil
	case err != nil:
		return err
	case !info.IsDir():
		return nil
	}
	if err := os.Remove(real); err != nil && !errors.Is(err, fs.ErrNotExist) && !isNotEmpty(err) {
		return err
	}
	return nil
}

// leftovers are what a module leaves on the box that no remove touches, and the commands to take it away by hand, which are printed and never run.
var leftovers = map[string][]string{
	"virt": {
		"left in place: /DATA/VM, the libvirt network and pool, the machines",
		"  virsh list --all                     the machines (virsh destroy <machine>; virsh undefine <machine> --remove-all-storage)",
		"  virsh net-list --all                 the network (virsh net-destroy <network>; virsh net-undefine <network>)",
		"  virsh pool-list --all                the pool (virsh pool-destroy <pool>; virsh pool-undefine <pool>)",
		"  rm -r /DATA/VM                       the disks of the machines",
	},
}

func (j *job) sayLeftovers(name string, packages []string) {
	for _, line := range leftovers[name] {
		j.say("%s", line)
	}
	if len(packages) > 0 {
		j.say("left in place: the packages %s", strings.Join(packages, " "))
		j.say("  apt-get purge %s", strings.Join(packages, " "))
	}
	j.say("nothing above was run")
}

// runRemove takes the module c.name off the box. A module is there to remove when it has a manifest, a list of files or a version: a half-installed one is removed too.
func runRemove(ctx context.Context, env Env, c command) (code int) {
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
	release, err := env.Host.Lock(env.Roots.lock())
	switch {
	case errors.Is(err, ErrLockHeld):
		return refuseEarly(env, c.name, CodeBusy)
	case err != nil:
		return refuseEarly(env, c.name, CodeLockFailed)
	}
	defer release()
	j := newJob(env, c.name)
	if err := j.openLog(); err != nil {
		return refuseEarly(env, c.name, CodeLogUnwritable)
	}
	defer j.close()
	defer j.recoverRun(&code, func() string { return "-" })
	// what an earlier run that was killed left in the area of the download is nobody's now: this run has the lock
	if err := emptyDir(env.Host, env.Roots.tmp()); errors.Is(err, errUnsafe) {
		return j.refuse(CodeStateUnsafe)
	}

	roots := env.Roots
	present := false
	for _, p := range []string{roots.manifest(c.name), roots.files(c.name), roots.dirs(c.name), roots.version(c.name)} {
		if _, err := os.Lstat(p); err == nil || !(errors.Is(err, fs.ErrNotExist) || isNotDir(err)) {
			present = true
		}
	}
	if !present {
		return j.refuse(CodeNotInstalled)
	}
	bx := &box{ctx: ctx, env: &env}
	if pm, err := bx.PackageManagerBusy(); pm || err != nil {
		return j.refuse(CodeBusy)
	}
	if up, err := bx.OSUpgradeBusy(); up || err != nil {
		return j.refuse(CodeBusy)
	}
	installed, _, _ := bx.InstalledManifests()
	manifest := installed[c.name]
	if manifest.Unit == "" {
		if e, ok := loadCatalogue(env).cat.Find(c.name); ok {
			manifest = e.Manifest
		}
	}
	if !c.yes {
		if !env.Terminal() {
			return j.refuse(CodeConfirmationNeeded)
		}
		fmt.Fprintf(env.Stdout, "remove %s: its unit is stopped and disabled, its files and its manifest are deleted; the packages and the data stay\nGo on? [y/N] ", c.name)
		if !j.askYes() {
			return j.refuse(CodeDeclined)
		}
	}
	bad, phase, why := j.teardown(ctx, c.name, manifest.Unit, "", nil)
	if bad > 0 {
		j.say("failed at step %s: %s", phase, why)
		switch why {
		case CodeUnitNotStopped:
			j.say("stop the unit, then run casaos module remove %s again", c.name)
		case CodeUnitsFailed:
			j.say("run systemctl daemon-reload, then casaos module remove %s again", c.name)
		case CodeFilesUnreadable:
			// (said above, with the name of the list)
		default:
			j.say("run casaos module remove %s again to finish", c.name)
		}
		j.result("failed", phase, why, "-")
		return ExitFailed
	}
	j.marker(StepDone, "done")
	j.result("ok", StepDone, "-", "-")
	j.say("removed: %s", c.name)
	j.sayLeftovers(c.name, osmodule.PackagesFor(manifest, env.Arch))
	return ExitOK
}

// askYes reads the answer to a question that was asked on the terminal.
func (j *job) askYes() bool {
	var line string
	buf := make([]byte, 1)
	for {
		n, err := j.env.Stdin.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			line += string(buf[:n])
		}
		if err != nil || len(line) > 16 {
			break
		}
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
