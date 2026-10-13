package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
)

// The run folder of an upgrade: /var/lib/casaos/os-upgrade/<run id>, private to root. What the core writes there, it writes once and then never changes,
// except the log (markers), which the core and the unit of the run only append to, and the flags and results that name themselves below. The unit is given nothing
// but the folder: the driver and the blocks are files in it, listed in SHA256SUMS, and so a core that is updated, restarted or lost does not touch a unit that runs.
const (
	osUpgradeRunJSON      = "run.json"
	osUpgradePlanJSON     = "plan.json"
	osUpgradeFactsFile    = "facts-before.json"
	osUpgradeManualFile   = "manual-runbook.sh"
	osUpgradeDriverFile   = "driver.sh"
	osUpgradeBKPathFile   = "bk-path"
	osUpgradeSumsFile     = "SHA256SUMS"
	osUpgradeMarkersFile  = "markers"
	osUpgradeOutputFile   = "output.log"
	osUpgradeAbortFile    = "abort"
	osUpgradeBarrierFile  = "barrier.json"
	osUpgradeVerifyFile   = "verify.json"
	osUpgradeBlocksDir    = "blocks"
	osUpgradeListsDir     = "lists"
	osUpgradeBKAborted    = "bk-aborted"
	osUpgradeBKDone       = "bk-done"
	osUpgradeCurrentFile  = "current"
	osUpgradeKeepRuns     = 3
	osUpgradeMaxSumsBytes = 64 << 10
	osUpgradeMaxFileBytes = 1 << 20
)

// osUpgradeRoot is where the run folders are, for the core's own reads and writes (the unit is always told osUpgradeUnitRoot: the two are one path outside the tests),
// osUpgradeBackups is where the backup folder of a hop is, and osUpgradeBetaFile is the owner's switch. All vars, for the tests.
var (
	osUpgradeRoot     = osUpgradeUnitRoot
	osUpgradeBackups  = "/var/backups"
	osUpgradeBetaFile = "/etc/casaos/os-upgrade.beta"
)

// osUpgradeMu is held only to append a marker to a log and to call systemd-run, so that a number of attempt is launched at most once; never across a wait, and
// never by a reader of the status.
var osUpgradeMu sync.Mutex

// osUpgradeWriteFile is how the core writes every file of a run but the log: whole or not at all. A var, for the tests that make a write fail.
var osUpgradeWriteFile = atomicWriteFile

// What the unit is told of the backup folder of a hop: the one name that its driver accepts.
const osUpgradeUnitBackups = "/var/backups"

var (
	osUpgradeBKPattern    = regexp.MustCompile(`^/var/backups/casaos-os-upgrade-[A-Za-z0-9._-]{1,120}$`)
	osUpgradeSumsLine     = regexp.MustCompile(`^([0-9a-f]{64})  ((?:driver\.sh|bk-path|blocks/[a-z]{1,16}\.sh))$`)
	osUpgradeHopBlockIDs  = osrun.BlockIDs[:9]
	osUpgradeRefreshBlock = []string{osrun.BlockRCheck, osrun.BlockRSources, osrun.BlockRUpgrade, osrun.BlockRDone}
)

// osUpgradeRunMeta is run.json: who the run is, written once.
type osUpgradeRunMeta struct {
	RunID string `json:"run_id"`
	// Kind is one of osrun.RunKinds; Hop and PlanID are the plan's (a refresh has no plan).
	Kind   string `json:"kind"`
	Hop    string `json:"hop"`
	PlanID string `json:"plan_id,omitempty"`
	// SafetyNet is what the owner said stands between them and a machine that does not come back.
	SafetyNet string `json:"safety_net,omitempty"`
	// BK is the backup folder as the unit knows it.
	BK        string    `json:"bk_path"`
	CreatedAt time.Time `json:"created_at"`
}

func (m osUpgradeRunMeta) valid(dirName string) bool {
	return osrun.ValidUUID(m.RunID) && m.RunID == dirName && slices.Contains(osrun.RunKinds, m.Kind) && osUpgradeBKPattern.MatchString(m.BK) && m.Hop != ""
}

// osUpgradeBlockIDs are the blocks a run of this kind may use, in the order of its list, and the one that mends a hop that stopped half way.
func osUpgradeBlockIDs(kind string) []string {
	if kind == osrun.RunRefresh {
		return append(slices.Clone(osUpgradeRefreshBlock), osrun.BlockForward)
	}
	return append(slices.Clone(osUpgradeHopBlockIDs), osrun.BlockForward)
}

// osUpgradeRunFolder is the folder of a run, as the core reads and writes it.
func osUpgradeRunFolder(runID string) string { return filepath.Join(osUpgradeRoot, runID) }

// osUpgradeBackupFolder is the backup folder of a hop as the core looks at it: the name the unit was told, under the core's own idea of where backups are.
func osUpgradeBackupFolder(unitPath string) string {
	return filepath.Join(osUpgradeBackups, strings.TrimPrefix(unitPath, osUpgradeUnitBackups+"/"))
}

// A osUpgradeRunSpec is what a run folder is written from.
type osUpgradeRunSpec struct {
	Meta    osUpgradeRunMeta
	Driver  string
	Blocks  []osplan.AutoBlock // in the order of the list of the hop
	Forward osplan.AutoBlock
	Plan    []byte // the plan with its tree, JSON
	Manual  string // the owner's text, for the owner who takes over
	Facts   osverify.Facts
}

// atomicWriteFile writes a file whole or not at all: to a name beside it, then renamed over it, and the folder is synced when the host has a way to.
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return syncFolder(dir)
}

// ensureOSUpgradeRoot makes the folder that holds the run folders, the busy flag and the pointer to the latest run: 0755, so that the flag can be seen by a
// service that does not run as root. The run folders in it are 0700.
func ensureOSUpgradeRoot() error {
	if err := os.MkdirAll(osUpgradeRoot, 0o755); err != nil {
		return err
	}
	return os.Chmod(osUpgradeRoot, 0o755)
}

// writeOSUpgradeRun makes the run folder and everything the unit and the owner need in it, SHA256SUMS last: a folder with no sums is no run, and a folder with
// sums is whole. On any failure the folder is removed again and nothing of it is left.
func writeOSUpgradeRun(spec osUpgradeRunSpec) (err error) {
	if err := ensureOSUpgradeRoot(); err != nil {
		return err
	}
	dir := osUpgradeRunFolder(spec.Meta.RunID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	for _, sub := range []string{osUpgradeBlocksDir, osUpgradeListsDir} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o700); err != nil {
			return err
		}
	}
	meta, err := json.MarshalIndent(spec.Meta, "", "  ")
	if err != nil {
		return err
	}
	facts, err := json.MarshalIndent(spec.Facts, "", "  ")
	if err != nil {
		return err
	}
	write := func(name string, data []byte, mode os.FileMode) error {
		if err := osUpgradeWriteFile(filepath.Join(dir, name), data, mode); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
	// the facts are the first thing of the folder, taken before anything of the machine is touched; the plan, which holds the owner's sources, and the owner's
	// text are private to root
	for _, f := range []struct {
		name string
		data []byte
	}{
		{osUpgradeFactsFile, facts}, {osUpgradeRunJSON, meta}, {osUpgradePlanJSON, spec.Plan}, {osUpgradeManualFile, []byte(spec.Manual)},
	} {
		if err := write(f.name, f.data, 0o600); err != nil {
			return err
		}
	}
	if err := write(osUpgradeDriverFile, []byte(spec.Driver), 0o600); err != nil {
		return err
	}
	for _, blk := range append(slices.Clone(spec.Blocks), spec.Forward) {
		if err := write(osUpgradeBlocksDir+"/"+blk.ID+".sh", []byte(blk.Text), 0o600); err != nil {
			return err
		}
	}
	if err := write(osUpgradeBKPathFile, []byte(spec.Meta.BK+"\n"), 0o600); err != nil {
		return err
	}
	sums, err := osUpgradeSums(dir, spec.Meta.Kind)
	if err != nil {
		return err
	}
	return write(osUpgradeSumsFile, sums, 0o600)
}

// osUpgradeSums is the content of SHA256SUMS for the files of a folder as they are now: the driver, the name of the backup folder and every block, in the
// format that sha256sum -c reads, with the names relative to the folder.
func osUpgradeSums(dir, kind string) ([]byte, error) {
	var b bytes.Buffer
	for _, name := range osUpgradeCovered(kind) {
		sum, err := osUpgradeHash(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%s  %s\n", sum, name)
	}
	return b.Bytes(), nil
}

// osUpgradeCovered are the files that SHA256SUMS covers for a run of this kind, in order.
func osUpgradeCovered(kind string) []string {
	names := []string{osUpgradeDriverFile, osUpgradeBKPathFile}
	for _, id := range osUpgradeBlockIDs(kind) {
		names = append(names, osUpgradeBlocksDir+"/"+id+".sh")
	}
	return names
}

func osUpgradeHash(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, osUpgradeMaxFileBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyOSUpgradeRun says whether the files that the unit runs are the ones the core wrote: SHA256SUMS names exactly the files that a run of this kind has, once
// each, every one of them is a regular file whose hash it holds, and the blocks folder holds nothing else. It is checked before every launch, and a folder that fails
// it is damaged: nothing is started from it again.
func verifyOSUpgradeRun(dir, kind string) error {
	f, err := os.Open(filepath.Join(dir, osUpgradeSumsFile))
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, osUpgradeMaxSumsBytes+1))
	f.Close()
	if err != nil {
		return err
	}
	if len(data) > osUpgradeMaxSumsBytes {
		return errors.New("SHA256SUMS is too long")
	}
	want := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		m := osUpgradeSumsLine.FindStringSubmatch(line)
		if m == nil {
			return fmt.Errorf("SHA256SUMS has a line that is not one of a run: %q", cutText(line, 80))
		}
		if _, dup := want[m[2]]; dup {
			return fmt.Errorf("SHA256SUMS names %s twice", m[2])
		}
		want[m[2]] = m[1]
	}
	covered := osUpgradeCovered(kind)
	if len(want) != len(covered) {
		return errors.New("SHA256SUMS does not name the files of this run")
	}
	for _, name := range covered {
		sum, ok := want[name]
		if !ok {
			return fmt.Errorf("SHA256SUMS does not name %s", name)
		}
		got, err := osUpgradeHash(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		if got != sum {
			return fmt.Errorf("%s is not what was written", name)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, osUpgradeBlocksDir))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, ok := want[osUpgradeBlocksDir+"/"+e.Name()]; !ok {
			return fmt.Errorf("blocks/%s is not a block of this run", e.Name())
		}
	}
	return nil
}

func cutText(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---- the log: one line per event, appended ---------------------------------------------------------------------------------

// createOSUpgradeMarkers makes the log of a run with its first line, the core's QUEUED marker; it fails when there is a log already (a run is told once).
func createOSUpgradeMarkers(dir, nonce string, queued time.Time) error {
	line, err := osrun.Marker{Kind: osrun.MkQueued, At: queued}.Line(nonce)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, osUpgradeMarkersFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// appendOSUpgradeMarker adds a line to the log of a run, in one write. A log that ends in a line that was cut (a unit that was killed as it wrote) gets a new line
// first, so that the marker is not glued to what was left of the other. The caller holds osUpgradeMu.
func appendOSUpgradeMarker(dir, nonce string, m osrun.Marker) error {
	line, err := m.Line(nonce)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, osUpgradeMarkersFile), os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, info.Size()-1); err == nil && last[0] != '\n' {
			line = "\n" + line
		}
	}
	if _, err := f.WriteString(line); err != nil {
		return err
	}
	return f.Sync()
}

// ---- the flags that name themselves: the busy flag, the pointer to the latest run -------------------------------------------

// osUpgradeBusyFlag is what the busy flag holds. Its being there is what matters; the rest is for whoever looks.
type osUpgradeBusyFlag struct {
	RunID string    `json:"run_id"`
	State string    `json:"state"`
	Since time.Time `json:"since"`
}

// setOSUpgradeBusy puts the flag of an open upgrade there (0644, whole or not at all, in a folder that is 0755).
func setOSUpgradeBusy(runID, state string, since time.Time) error {
	if err := os.MkdirAll(filepath.Dir(osUpgradeBusyFile), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(osUpgradeBusyFlag{RunID: runID, State: state, Since: since.UTC()})
	if err != nil {
		return err
	}
	return osUpgradeWriteFile(osUpgradeBusyFile, append(data, '\n'), 0o644)
}

// clearOSUpgradeBusy takes the flag away; one that is not there is as good.
func clearOSUpgradeBusy() error {
	if err := os.Remove(osUpgradeBusyFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func osUpgradeCurrentPath() string { return filepath.Join(osUpgradeRoot, osUpgradeCurrentFile) }

// setOSUpgradeCurrent says which run is the latest.
func setOSUpgradeCurrent(runID string) error {
	if !osrun.ValidUUID(runID) {
		return errors.New("not a run id")
	}
	if err := ensureOSUpgradeRoot(); err != nil {
		return err
	}
	return osUpgradeWriteFile(osUpgradeCurrentPath(), []byte(runID+"\n"), 0o644)
}

// clearOSUpgradeCurrent forgets the latest run (the owner closed it).
func clearOSUpgradeCurrent() error {
	if err := os.Remove(osUpgradeCurrentPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// The ways the pointer to the latest run can be.
const (
	osUpgradeNoRun  = iota // there is none
	osUpgradeHasRun        // it names a run
	osUpgradeBadRun        // it is there and says nothing that can be used: the run it stood for cannot be told
)

// currentOSUpgrade is the latest run, read fail closed: only a pointer that is not there is no run.
func currentOSUpgrade() (runID string, state int) {
	data, err := os.ReadFile(osUpgradeCurrentPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", osUpgradeNoRun
	case err != nil:
		return "", osUpgradeBadRun
	}
	id := strings.TrimSuffix(string(data), "\n")
	if !osrun.ValidUUID(id) {
		return "", osUpgradeBadRun
	}
	return id, osUpgradeHasRun
}

// ---- retention -------------------------------------------------------------------------------------------------------------

// pruneOSUpgradeRuns removes the run folders beyond the last keep (and never the one that is current): a name that is a UUID, a folder that is not a link, with a
// run.json that is a file. Nothing else in the folder is looked at, and nothing is matched by a pattern.
func pruneOSUpgradeRuns(keep int) error {
	entries, err := os.ReadDir(osUpgradeRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	current, _ := currentOSUpgrade()
	type folder struct {
		id   string
		made time.Time
	}
	var runs []folder
	for _, e := range entries {
		if !osrun.ValidUUID(e.Name()) {
			continue
		}
		dir := filepath.Join(osUpgradeRoot, e.Name())
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, osUpgradeRunJSON))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		runs = append(runs, folder{e.Name(), info.ModTime()})
	}
	sort.Slice(runs, func(i, j int) bool {
		if !runs[i].made.Equal(runs[j].made) {
			return runs[i].made.After(runs[j].made)
		}
		return runs[i].id > runs[j].id
	})
	var first error
	for i, r := range runs {
		if i < keep || r.id == current {
			continue
		}
		if err := os.RemoveAll(filepath.Join(osUpgradeRoot, r.id)); err != nil && first == nil {
			first = err
		}
	}
	return first
}
