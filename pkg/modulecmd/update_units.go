package modulecmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	backupUnits  = "units.state" // a unitRecord to a line, behind a first line
	backupOwners = "old.owners"  // the user and the group of the n-th entry of backupActual, one to a line: "<n> <uid> <gid>"
	unitsHeader  = "casaos-module-units 1"
	ownersHeader = "casaos-module-owners 1"
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
		out = append(out, unitRecord{Name: name, Active: active == "active", Enabled: enabled == "enabled"})
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
		if word, ok := j.unitWord(ctx, "is-active", r.Name); r.Active && (!ok || word != "active") {
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
	if err := os.Remove(filepath.Join(dir, backupComplete)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	syncDir(dir)
	return removeAll(dir)
}
