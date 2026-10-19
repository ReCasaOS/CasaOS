package modulecmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// What an update keeps of the state of the units, and of who owns the files, in the backup folder; and how the backup folder is taken away.

// A unitRecord is what a unit of the module was before the update stopped anything: running, and enabled. An update leaves running what was running and enabled what was enabled, and
// nothing else: a module that its administrator had stopped is still stopped when the update is over.
type unitRecord struct {
	Name            string
	Active, Enabled bool
}

const (
	backupDropped = "DROPPED"     // the mark of a folder whose COMPLETE could not be taken away: it is dropped
	backupUnits   = "units.state" // a unitRecord to a line, behind a first line
	backupOwners  = "old.owners"  // the user and the group of the n-th entry of backupActual, one to a line: "<n> <uid> <gid>"
	unitsHeader   = "casaos-module-units 1"
	ownersHeader  = "casaos-module-owners 1"
)

// unitWord asks systemd one word about a unit (is-active, is-enabled) on a context of its own, bounded: the answer is what decides what is stopped and started. ok is false when systemd
// could not be asked at all.
func (j *job) unitWord(ctx context.Context, verb, unit string) (word string, ok bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), systemctlTimeout)
	defer cancel()
	out, err := j.env.Runner.Run(ctx, Cmd{Argv: []string{"systemctl", verb, unit}, Env: childEnv(j.env), Timeout: systemctlTimeout})
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out.Text), true
}

// recordUnits reads the state of the units; an error is a unit that systemd could not be asked about.
func (j *job) recordUnits(ctx context.Context, names []string) ([]unitRecord, error) {
	var out []unitRecord
	for _, name := range names {
		active, ok1 := j.unitWord(ctx, "is-active", name)
		enabled, ok2 := j.unitWord(ctx, "is-enabled", name)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("systemd does not say what the unit %s is", printable(name, 80))
		}
		out = append(out, unitRecord{Name: name, Active: unitIsUp(active), Enabled: enabled == "enabled"})
	}
	return out, nil
}

func formatUnits(rs []unitRecord) []byte {
	var b bytes.Buffer
	b.WriteString(unitsHeader + "\n")
	for _, r := range rs {
		b.WriteString(r.Name + " " + map[bool]string{true: "active", false: "inactive"}[r.Active] + " " + map[bool]string{true: "enabled", false: "disabled"}[r.Enabled] + "\n")
	}
	return b.Bytes()
}

func parseUnits(raw []byte) ([]unitRecord, error) {
	text, ok := strings.CutPrefix(string(raw), unitsHeader+"\n")
	if !ok || !strings.HasSuffix(text, "\n") || len(raw) > 1<<16 {
		return nil, errors.New("the record of the units is not one")
	}
	var out []unitRecord
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		f := strings.Split(line, " ")
		if len(f) != 3 || !unitNameRe.MatchString(f[0]) || (f[1] != "active" && f[1] != "inactive") || (f[2] != "enabled" && f[2] != "disabled") {
			return nil, errors.New("the record of the units is not one")
		}
		out = append(out, unitRecord{Name: f[0], Active: f[1] == "active", Enabled: f[2] == "enabled"})
	}
	if len(out) == 0 {
		return nil, errors.New("the record of the units is empty")
	}
	return out, nil
}

// settleUnits starts the units that were running and enables the ones that were enabled and are not any more; it touches no other. It returns the number it could not do.
func (j *job) settleUnits(ctx context.Context, rs []unitRecord) (failed int) {
	for _, r := range rs {
		if word, ok := j.unitWord(ctx, "is-active", r.Name); r.Active && (!ok || !unitIsUp(word)) {
			if out, err := j.sh(ctx, systemctlTimeout, "systemctl", "start", r.Name); err != nil || out.ExitCode != 0 {
				j.say("the unit %s could not be started", printable(r.Name, 80))
				failed++
			}
		}
		if r.Enabled {
			if word, ok := j.unitWord(ctx, "is-enabled", r.Name); !ok || word != "enabled" {
				if out, err := j.sh(ctx, systemctlTimeout, "systemctl", "enable", r.Name); err != nil || out.ExitCode != 0 {
					j.say("the unit %s could not be enabled", printable(r.Name, 80))
					failed++
				}
			}
		}
	}
	return failed
}

// anyActive and activeOf say what the record has running.
func anyActive(rs []unitRecord) bool {
	for _, r := range rs {
		if r.Active {
			return true
		}
	}
	return false
}

func activeOf(rs []unitRecord, name string) bool {
	for _, r := range rs {
		if r.Name == name {
			return r.Active
		}
	}
	return false
}

// ownersFile is the list of who owned the entries of the backup, "<n> <uid> <gid>"; ownersOf reads it back.
func formatOwners(owners map[int][2]int, n int) []byte {
	var b bytes.Buffer
	b.WriteString(ownersHeader + "\n")
	for i := 0; i < n; i++ {
		if o, ok := owners[i]; ok {
			b.WriteString(strconv.Itoa(i) + " " + strconv.Itoa(o[0]) + " " + strconv.Itoa(o[1]) + "\n")
		}
	}
	return b.Bytes()
}

func parseOwners(raw []byte) (map[int][2]int, error) {
	text, ok := strings.CutPrefix(string(raw), ownersHeader+"\n")
	if !ok || len(raw) > 1<<22 {
		return nil, errors.New("the record of the owners is not one")
	}
	out := map[int][2]int{}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, " ")
		if len(f) != 3 {
			return nil, errors.New("the record of the owners is not one")
		}
		n, e1 := strconv.Atoi(f[0])
		uid, e2 := strconv.Atoi(f[1])
		gid, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil || n < 0 || uid < 0 || gid < 0 {
			return nil, errors.New("the record of the owners is not one")
		}
		out[n] = [2]int{uid, gid}
	}
	return out, nil
}

// removeAll is what takes the contents of a backup folder away, a variable so that a test can end it in the middle.
var removeAll = os.RemoveAll

// removeBackup takes a backup folder away. The first thing it does is take COMPLETE away, and make that durable: from then on the folder is "not finished", which every reader drops and
// puts nothing back from, so that a run that is ended while it deletes (a kill, the power, an error) never leaves a folder that says it is complete and is not.
func removeBackup(dir string) error {
	dropped := filepath.Join(dir, backupDropped)
	if _, err := os.Lstat(dropped); err != nil {
		err = removeFile(filepath.Join(dir, backupComplete))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			err = removeFile(filepath.Join(dir, backupComplete))
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			// COMPLETE cannot be taken away: the folder is marked as dropped, which every reader takes as the same thing
			if terr := writeTombstone(dir); terr != nil {
				return &notDroppedError{fmt.Errorf("%w (and the mark that it is dropped cannot be written: %v)", err, terr)}
			}
		}
	}
	syncDir(dir)
	return removeAll(dir)
}

// A notDroppedError is a backup folder that still says it is complete: the next run of the module would put the old version back over the new.
type notDroppedError struct{ error }

// removeFile and writeTombstone are what removeBackup takes COMPLETE away with, and marks the folder as dropped with when it cannot: variables, so that a test can make them fail.
var (
	removeFile     = os.Remove
	writeTombstone = func(dir string) error {
		return writeFileAtomic(filepath.Join(dir, backupDropped), []byte("dropped\n"), 0o600)
	}
)

// wayOut says what can be done when the old version could not be put back whole: the box may hold files of both versions, `casaos module remove` works whatever is in the backup folder, and
// the folder, which is the one copy of the old files, is deleted only by hand.
func (j *job) wayOut(name, where string) {
	j.say("the box may hold files of both versions. casaos module remove %s takes the module away by the lists that are on the box, whatever is in %s. To go back to the old version put its files back by hand from %s (copies/, named by old.files.actual; old.files, old.dirs, old.json and old.version) and delete the folder. Deleting the folder alone keeps the files as they are on the box", name, where, where)
}

// unitIsUp says whether the word systemd gives for a unit is one of a unit that is running or on its way to or from running: such a unit is stopped before the swap, and started again.
func unitIsUp(word string) bool {
	switch word {
	case "active", "activating", "reloading", "deactivating":
		return true
	}
	return false
}

// StepLeftovers is the step of a remove that takes away what the new version of an update that was ended put down, when the backup of that update could not be put back and was kept: the
// lists on the box are the old version's, and know nothing of the files and the directories that only the new version has.
const StepLeftovers = "leftovers"

// removeNewVersionLeftovers deletes, by the list of the new version that the backup holds (new.files, new.dirs), the paths that still hold what that list says and the directories the swap
// made when they are empty, and names every other path that is still there: `left: <path> (of the new version)`. It returns the number it left, and a list that cannot be read is one.
func (j *job) removeNewVersionLeftovers(name string) (left int) {
	roots := j.env.Roots
	dir := roots.backup(name)
	raw, err := readRegular(filepath.Join(dir, backupNewFiles), int64(osmodule.MaxFilesBytes))
	var list osmodule.FilesList
	if err == nil {
		list, err = osmodule.ParseFilesList(raw)
	}
	if err != nil || !list.Verified {
		j.say("the files of the new version are not recorded in the backup (%s): files of it may be left on the box", printable(fmt.Sprint(err), 100))
		return 1
	}
	for _, n := range list.Entries {
		real := roots.Path(n.Path)
		_, _ = removeEntry(tempName(real))
		switch h, err := holds(real, n); {
		case err != nil:
			j.say("left: %s (of the new version; it cannot be looked at: %s)", printable(n.Path, 200), printable(err.Error(), 100))
			left++
		case h == holdsOther:
			j.say("left: %s (of the new version)", printable(n.Path, 200))
			left++
		case h == holdsOurs:
			if _, err := removeEntry(real); err != nil {
				j.say("left: %s (of the new version; not removed: %s)", printable(n.Path, 200), printable(err.Error(), 100))
				left++
			}
		}
	}
	if raw, err := readRegular(filepath.Join(dir, backupNewDirs), int64(osmodule.MaxFilesBytes)); err == nil {
		if dirs, err := osmodule.ParseDirs(raw); err == nil {
			for _, d := range sortedDeepestFirst(dirs) {
				_ = removeEmptyDir(roots.Path(d))
				if info, err := os.Lstat(roots.Path(d)); err == nil && info.IsDir() {
					j.say("left: %s (a directory of the new version, not empty)", printable(d, 200))
					left++
				}
			}
		}
	}
	return left
}
