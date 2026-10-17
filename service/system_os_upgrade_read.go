package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
	"github.com/ReCasaOS/CasaOS/pkg/osverify"
	"github.com/ReCasaOS/CasaOS/pkg/singleflight"
)

// An osUpgradeRun is a run as the core reads it from its folder: who it is, what its log says, which of the files that are the truth about its blocks are there.
type osUpgradeRun struct {
	ID   string
	Dir  string
	Meta osUpgradeRunMeta
	// Nonce is the one on the first line of the log ("" when the log has none); TooLarge says that the log is longer than it is read.
	Nonce    string
	Markers  []osrun.Marker
	Ignored  int
	TooLarge bool
	// BK is the backup folder that the files below were read in: the folder of the hop, or the one that it was put in when the run ended (""
	// when there is none).
	BK    string
	Files osrun.Files
	// OutputAt is when the unit's output was last written, zero when there is none.
	OutputAt time.Time
}

// readOSUpgradeFile reads a file of a run that is small and is a file: not a link, not a device, not more than max.
func readOSUpgradeFile(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is too long", filepath.Base(path))
	}
	return data, nil
}

// readOSUpgradeMeta reads run.json of a run and says whether it is the one that the folder is named for.
func readOSUpgradeMeta(runID string) (osUpgradeRunMeta, error) {
	var meta osUpgradeRunMeta
	data, err := readOSUpgradeFile(filepath.Join(osUpgradeRunFolder(runID), osUpgradeRunJSON), 64<<10)
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, err
	}
	if !meta.valid(runID) {
		return meta, errors.New("run.json is not the identity of this run")
	}
	return meta, nil
}

// readOSUpgradeRun reads a run from its folder. An error is a run that cannot be read: the folder is not one, or run.json is not what the folder is named for. A log
// that is not there yet is a run with no markers, which the reducer calls idle.
func readOSUpgradeRun(runID string) (*osUpgradeRun, error) {
	if !osrun.ValidUUID(runID) {
		return nil, errors.New("not a run id")
	}
	run := &osUpgradeRun{ID: runID, Dir: osUpgradeRunFolder(runID)}
	meta, err := readOSUpgradeMeta(runID)
	if err != nil {
		return nil, err
	}
	run.Meta = meta
	f, err := os.Open(filepath.Join(run.Dir, osUpgradeMarkersFile))
	switch {
	case err == nil:
		var parseErr error
		run.Nonce, run.Markers, run.Ignored, parseErr = osrun.ParseMarkers(f)
		f.Close()
		if errors.Is(parseErr, osrun.ErrMarkersTooLarge) {
			run.TooLarge = true
		} else if parseErr != nil {
			return nil, parseErr
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	run.BK = osUpgradeBackupRead(run)
	run.Files = osUpgradeFiles(run)
	if info, err := os.Lstat(filepath.Join(run.Dir, osUpgradeOutputFile)); err == nil {
		run.OutputAt = info.ModTime()
	}
	return run, nil
}

// osUpgradeBackupRead is the folder that holds the files of the blocks of a run: the one it was put in when it ended (a run that was undone, a refresh, a hop
// that was closed), and otherwise the backup folder of the hop, when there is one.
func osUpgradeBackupRead(run *osUpgradeRun) string {
	for _, name := range []string{osUpgradeBKAborted, osUpgradeBKDone} {
		if info, err := os.Lstat(filepath.Join(run.Dir, name)); err == nil && info.IsDir() {
			return filepath.Join(run.Dir, name)
		}
	}
	bk := osUpgradeBackupFolder(run.Meta.BK)
	info, err := os.Lstat(bk)
	// A folder that cannot be asked about (an I/O error, say) is taken to be there: its files are then asked about one by one, and the two that say packages are
	// changing are taken to be there when they cannot be. Only a folder that is not there is none.
	if err == nil && info.IsDir() || err != nil && !errors.Is(err, fs.ErrNotExist) {
		return bk
	}
	return ""
}

// osUpgradeFiles reads which of the files that are the truth about the point of no return and the blocks that ended are there. A file that cannot be asked about is
// taken to be there when it is one of the two that say packages are changing, so that a machine is never taken for one that can still be undone.
func osUpgradeFiles(run *osUpgradeRun) osrun.Files {
	var f osrun.Files
	has := func(dir, name string, ifUnknown bool) bool {
		if dir == "" {
			return false
		}
		_, err := os.Lstat(filepath.Join(dir, name))
		switch {
		case err == nil:
			return true
		case errors.Is(err, fs.ErrNotExist):
			return false
		}
		return ifUnknown
	}
	f.Begun, f.DoneMinimal = has(run.BK, "begun-upgrade", true), has(run.BK, "done-minimal", true)
	f.DoneFull, f.DonePrereboot, f.DoneAfter = has(run.BK, "done-full", false), has(run.BK, "done-prereboot", false), has(run.BK, "done-after", false)
	f.DoneTimers, f.AptOrig = has(run.BK, "done-timers", false), has(run.BK, "apt-orig", false)
	f.AbortFlag = has(run.Dir, osUpgradeAbortFile, false)
	if run.BK != "" {
		if data, err := readOSUpgradeFile(filepath.Join(run.BK, "boot_id"), 256); err == nil {
			f.BootIDFile, _, _ = strings.Cut(strings.TrimRight(string(data), "\r\n"), "\n")
		}
	}
	return f
}

// osUpgradeBootID is the id of the boot the machine is in; false when it cannot be read, which the reducer then never infers anything from. A var, for the tests.
var osUpgradeBootID = readOSUpgradeBootID

func readOSUpgradeBootID() (string, bool) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", false
	}
	id := strings.TrimSpace(string(data))
	return id, osrun.ValidUUID(id)
}

// osUpgradeUnitFlight shares the question to systemd of the requests that come while it is asked: the page asks every few seconds.
var osUpgradeUnitFlight singleflight.Group[osrun.Unit]

// osUpgradeUnit is what systemd says of the unit of an upgrade: Answered is false when it did not answer in the time it is given.
func (u *systemPackageUpdater) osUpgradeUnit() osrun.Unit {
	unit, _, _ := osUpgradeUnitFlight.Do("unit", func() (osrun.Unit, error) { return u.askOSUpgradeUnit(), nil })
	return unit
}

// The two results that the core's own settling writes, and that the reducer reads back: whether the machine may be restarted, and what the judge found after the
// restart. They are in the run folder, written whole, and what is not there has not been computed.
type osUpgradeBarrierDoc struct {
	OK     bool                   `json:"ok"`
	Checks []OSUpgradeBarrierItem `json:"checks"`
}

type osUpgradeVerifyDoc struct {
	At       time.Time          `json:"at"`
	Findings []osverify.Finding `json:"findings"`
	// NotJudged are the facts of the machine that could not be taken and were to be judged: the verdict does not say that they are well.
	NotJudged []string `json:"not_judged,omitempty"`
}

// readOSUpgradeFacts is what the machine was before the hop, as the start took it: the zero Facts when the file is lost or cannot be read, which says that nothing was
// taken and is judged by what is known.
func readOSUpgradeFacts(run *osUpgradeRun) osverify.Facts {
	return readOSUpgradeFactsFile(run, osUpgradeFactsFile)
}

// readOSUpgradeFactsFile is a file of facts of the run folder: the zero Facts when it is not there or cannot be read.
func readOSUpgradeFactsFile(run *osUpgradeRun, name string) osverify.Facts {
	var facts osverify.Facts
	data, err := readOSUpgradeFile(filepath.Join(run.Dir, name), 4<<20)
	if err != nil || json.Unmarshal(data, &facts) != nil {
		return osverify.Facts{}
	}
	return facts
}

// writeOSUpgradeBarrier and writeOSUpgradeVerify put the two results of the settling in the run folder, whole or not at all: what is there is what the status and the
// reducer read, and what is not has not been computed.
func writeOSUpgradeBarrier(run *osUpgradeRun, doc osUpgradeBarrierDoc) error {
	return writeOSUpgradeResult(run, osUpgradeBarrierFile, doc)
}

func writeOSUpgradeVerify(run *osUpgradeRun, doc osUpgradeVerifyDoc) error {
	return writeOSUpgradeResult(run, osUpgradeVerifyFile, doc)
}

func writeOSUpgradeResult(run *osUpgradeRun, name string, doc any) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return osUpgradeWriteFile(filepath.Join(run.Dir, name), append(data, '\n'), 0o600)
}

// readOSUpgradeBarrier is barrier.json of a run, nil when it was not computed or cannot be read.
func readOSUpgradeBarrier(run *osUpgradeRun) *osUpgradeBarrierDoc {
	data, err := readOSUpgradeFile(filepath.Join(run.Dir, osUpgradeBarrierFile), 256<<10)
	if err != nil {
		return nil
	}
	var doc osUpgradeBarrierDoc
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	return &doc
}

// readOSUpgradeVerify is verify.json of a run, nil when the judge has not been run or its answer cannot be read.
func readOSUpgradeVerify(run *osUpgradeRun) *osUpgradeVerifyDoc {
	data, err := readOSUpgradeFile(filepath.Join(run.Dir, osUpgradeVerifyFile), 1<<20)
	if err != nil {
		return nil
	}
	var doc osUpgradeVerifyDoc
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	return &doc
}

// osUpgradeRunLineDiffers says that the RUN line of the log is not of the run that the folder is: another run, another kind or another plan. A log that was copied from another run, or
// written over, says what it likes, and the run is not trusted.
func osUpgradeRunLineDiffers(run *osUpgradeRun) bool {
	for _, m := range run.Markers {
		if m.Kind == osrun.MkRun {
			return m.RunID != run.Meta.RunID || m.RunKind != run.Meta.Kind || m.PlanID != run.Meta.PlanID || m.Hop != run.Meta.Hop
		}
	}
	return false
}

// osUpgradeInput is everything that the reducer reads of a run, at the time of the updater's clock: the log, the files, what systemd says of the unit, the boot, the
// output of the unit, and the two results that the settling writes. It takes no lock and runs nothing but the question to systemd. A run whose files are not what the
// core wrote, or whose log is longer than it is read, is damaged.
func (s *systemService) osUpgradeInput(run *osUpgradeRun) osrun.Input {
	return s.osUpgradeInputWith(run, s.systemPackageUpdater().osUpgradeUnit())
}

// osUpgradeInputWith is osUpgradeInput with the answer of systemd that the caller has.
func (s *systemService) osUpgradeInputWith(run *osUpgradeRun, unit osrun.Unit) osrun.Input {
	u := s.systemPackageUpdater()
	boot, ok := osUpgradeBootID()
	in := osrun.Input{
		Markers: run.Markers, Ignored: run.Ignored, Files: run.Files,
		Unit: unit, Boot: osrun.Boot{Now: boot, OK: ok},
		Now: u.now().UTC(), LastActivity: run.OutputAt.UTC(),
		Damaged: run.TooLarge || osUpgradeRunLineDiffers(run) || verifyOSUpgradeRun(run.Dir, run.Meta.Kind) != nil,
		Timing:  osUpgradeTiming,
	}
	if barrier := readOSUpgradeBarrier(run); barrier != nil {
		in.Barrier = &osrun.Barrier{OK: barrier.OK}
		for _, c := range barrier.Checks {
			in.Barrier.Checks = append(in.Barrier.Checks, osrun.BarrierCheck{Code: c.Code, OK: c.OK, Severity: c.Severity})
		}
	}
	if verify := readOSUpgradeVerify(run); verify != nil {
		result := osverify.Summarize(verify.Findings)
		in.Verify = &result
	}
	for _, m := range run.Markers {
		if m.Kind == osrun.MkBootSeen && m.Boot == boot && ok {
			in.BootSeenAt = m.At
		}
	}
	return in
}
