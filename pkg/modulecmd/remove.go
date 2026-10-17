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

// stopUnit stops and disables a unit. A unit systemd does not know is already stopped; one that is still running after the attempt is not, and is the reason the files stay.
func (j *job) stopUnit(ctx context.Context, unit string) bool {
	out, err := j.sh(ctx, systemctlTimeout, "systemctl", "disable", "--now", unit)
	if err == nil && out.ExitCode == 0 {
		return true
	}
	state, err := j.env.Runner.Run(ctx, Cmd{Argv: []string{"systemctl", "is-active", unit}, Env: childEnv(j.env), Timeout: systemctlTimeout})
	if err == nil {
		switch strings.TrimSpace(state.Text) {
		case "active", "activating", "deactivating", "reloading":
			j.say("the unit %s is still running", printable(unit, 80))
			return false
		}
	}
	j.say("the unit %s could not be disabled; it is not running", printable(unit, 80))
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

// teardown undoes an install, whatever state it is in: the units are stopped and disabled, the files of the list that are still there (and are no other module's) are deleted, then the
// manifest, the version and the list, then systemd reads its units again. It returns what it could not do, with the step it was in. Nothing that is the module's is forgotten until
// all of it is gone: a list, a manifest or a version stays while a file does, and a run again finishes the work. markers puts the phases in the log as steps (remove does, the rollback
// of an install does not).
func (j *job) teardown(ctx context.Context, name, manifestUnit string, markers bool) (bad int, phase string) {
	step := func(s string) {
		if markers {
			j.marker(s, "begin")
			j.hook(ctx, s, "begin")
		}
	}
	end := func(s string, bad int) {
		switch {
		case !markers:
		case bad > 0:
			j.marker(s, "failed")
		default:
			j.marker(s, "done")
			j.hook(ctx, s, "done")
		}
	}
	roots := j.env.Roots
	list, err := readFilesList(roots.files(name))
	if err != nil {
		j.say("the list of the files of %s cannot be read", name)
		return 1, StepStop
	}
	if i := slices.IndexFunc(list, protected); i >= 0 {
		j.say("the list of the files of %s names a path that is the core's own: nothing is deleted", name)
		return 1, StepStop
	}
	if list == nil && markers {
		j.say("there is no list of the files of %s: no file is deleted", name)
	}
	// stop
	step(StepStop)
	stuck := 0
	for _, unit := range unitNames(manifestUnit, list) {
		if !j.stopUnit(ctx, unit) {
			stuck++
		}
	}
	end(StepStop, stuck)
	if stuck > 0 {
		return stuck, StepStop
	}
	// files
	step(StepFiles)
	others, err := (&box{ctx: ctx, env: &j.env}).FilesLists()
	if err != nil {
		j.say("the lists of the files of the other modules cannot be read")
		end(StepFiles, 1)
		return 1, StepFiles
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
	for _, p := range list {
		if other, ok := owner[p]; ok {
			j.say("kept, it is the list of %s too: %s", other, printable(p, 200))
			continue
		}
		real := roots.Path(p)
		for _, target := range []string{real, tempName(real)} {
			if _, err := removeEntry(target); err != nil {
				j.say("not removed: %s (%s)", printable(p, 200), printable(err.Error(), 100))
				failed++
			}
		}
	}
	end(StepFiles, failed)
	if failed > 0 {
		return failed, StepFiles
	}
	// manifest
	step(StepManifest2)
	gone := 0
	for _, p := range []string{roots.manifest(name), roots.version(name), roots.files(name), tempName(roots.manifest(name)), tempName(roots.version(name)), tempName(roots.files(name))} {
		if _, err := removeEntry(p); err != nil {
			j.say("not removed: %s", printable(err.Error(), 100))
			gone++
		}
	}
	end(StepManifest2, gone)
	if gone > 0 {
		return gone, StepManifest2
	}
	// reload
	step(StepReload)
	if out, err := j.sh(ctx, systemctlTimeout, "systemctl", "daemon-reload"); err != nil || out.ExitCode != 0 {
		j.say("systemctl daemon-reload failed; systemd reads its units again at its next reload")
	}
	end(StepReload, 0)
	return 0, ""
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

// Remove takes the module c.name off the box. A module is there to remove when it has a manifest, a list of files or a version: a half-installed one is removed too.
func Remove(ctx context.Context, env Env, c command) int {
	env = env.withDefaults()
	say := func(code string) int { fmt.Fprintf(env.Stdout, "refused: %s\n", code); return ExitRefused }
	if env.EUID() != 0 {
		return say(CodeNotRoot)
	}
	if err := makeDirs(env.Roots.state(), 0o755); err != nil {
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
	// what an earlier run that was killed left in the area of the download is nobody's now: this run has the lock
	_ = emptyDir(env.Roots.tmp())

	roots := env.Roots
	present := false
	for _, p := range []string{roots.manifest(c.name), roots.files(c.name), roots.version(c.name)} {
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
	bad, phase := j.teardown(ctx, c.name, manifest.Unit, true)
	if bad > 0 {
		j.say("failed at step %s: %s", phase, CodeRemoveIncomplete)
		j.result("failed", phase, CodeRemoveIncomplete)
		j.say("run casaos module remove %s again to finish", c.name)
		return ExitFailed
	}
	j.marker(StepDone, "done")
	j.result("ok", StepDone, "-")
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
