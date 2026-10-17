package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// ---- the folder of a run ------------------------------------------------------------------------------------------------

func TestTheRunFolderHoldsWhatTheUnitAndTheOwnerNeed(t *testing.T) {
	box := newUpgradeBox(t)
	plan := box.anUpgradePlan(osplan.VerdictOK, nil)
	run := box.aRun(plan, osrun.RunHop)
	dir := run.Dir

	// the driver is the constant text of the package that owns it, and each block is the text that the plan makes for the unit
	if got := readText(t, filepath.Join(dir, "driver.sh")); got != osrun.Driver {
		t.Error("driver.sh is not osrun.Driver")
	}
	auto, err := osplan.BuildAuto(*plan, osplan.AutoParams{RunID: run.ID, Hostname: "box1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, blk := range append(slices.Clone(auto.Blocks), auto.Forward) {
		if got := readText(t, filepath.Join(dir, "blocks", blk.ID+".sh")); got != blk.Text {
			t.Errorf("blocks/%s.sh is not the text of the block", blk.ID)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "blocks"))
	if err != nil || len(entries) != 10 {
		t.Errorf("blocks/ holds %d files (%v): the nine blocks and the repair", len(entries), err)
	}
	if got := readText(t, filepath.Join(dir, "bk-path")); got != "/var/backups/casaos-os-upgrade-"+plan.PlanID+"\n" {
		t.Errorf("bk-path = %q", got)
	}
	manual, err := osplan.BuildRunbook(*plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := readText(t, filepath.Join(dir, "manual-runbook.sh")); got != manual.Text {
		t.Error("manual-runbook.sh is not the owner's text for the plan")
	}
	var meta osUpgradeRunMeta
	if err := json.Unmarshal([]byte(readText(t, filepath.Join(dir, "run.json"))), &meta); err != nil || !reflect.DeepEqual(meta, run.Meta) || meta.Kind != osrun.RunHop || meta.PlanID != plan.PlanID {
		t.Errorf("run.json = %+v, %v", meta, err)
	}
	for _, name := range []string{"facts-before.json", "plan.json"} {
		if !strings.HasPrefix(strings.TrimSpace(readText(t, filepath.Join(dir, name))), "{") {
			t.Errorf("%s is not a JSON object", name)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "lists")); err != nil || !info.IsDir() {
		t.Errorf("lists/ is not a folder: %v", err)
	}

	// what is private to root is: the folder, and every file of it (the plan holds the owner's sources, credentials included)
	if runtime.GOOS == "linux" {
		for _, name := range []string{".", "blocks", "lists", "run.json", "plan.json", "manual-runbook.sh", "driver.sh", "bk-path", "SHA256SUMS", "facts-before.json", "markers"} {
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm&0o077 != 0 {
				t.Errorf("%s is %o: others can read it", name, perm)
			}
		}
		if info, err := os.Stat(osUpgradeRoot); err != nil || info.Mode().Perm() != 0o755 {
			t.Errorf("the folder of the runs is %v (%v), want 755: a service that is not root has to see the flag in it", info.Mode().Perm(), err)
		}
	}
}

// SHA256SUMS is what sha256sum -c reads, from inside the folder, which is how the driver runs it.
func TestSHA256SUMSIsInTheFormatThatSha256sumReads(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	sums := readText(t, filepath.Join(run.Dir, "SHA256SUMS"))
	lines := strings.Split(strings.TrimSuffix(sums, "\n"), "\n")
	if len(lines) != 12 || !strings.HasSuffix(sums, "\n") {
		t.Fatalf("SHA256SUMS has %d lines: the driver, the name of the backup folder, the nine blocks and the repair", len(lines))
	}
	var names []string
	for _, line := range lines {
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || len(sum) != 64 || strings.Contains(name, " ") || strings.HasPrefix(name, "/") {
			t.Errorf("a line that sha256sum cannot read: %q", line)
		}
		names = append(names, name)
	}
	want := []string{"driver.sh", "bk-path"}
	for _, id := range append(slices.Clone(osrun.BlockIDs[:9]), osrun.BlockForward) {
		want = append(want, "blocks/"+id+".sh")
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("SHA256SUMS names %v, want %v", names, want)
	}

	sha, err := exec.LookPath("sha256sum")
	if err != nil {
		t.Skipf("no sha256sum here: %v", err)
	}
	cmd := exec.Command(sha, "-c", "SHA256SUMS")
	cmd.Dir = run.Dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sha256sum -c: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(run.Dir, "blocks", "minimal.sh"), []byte("reboot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("sha256sum -c passes a block that was changed:\n%s", out)
	}
}

// A folder with no sums is no run, and one with sums is whole: SHA256SUMS is the last file written.
func TestSHA256SUMSIsTheLastFileOfTheFolderThatIsWritten(t *testing.T) {
	box := newUpgradeBox(t)
	var mu sync.Mutex
	var written []string
	original := osUpgradeWriteFile
	t.Cleanup(func() { osUpgradeWriteFile = original })
	osUpgradeWriteFile = func(path string, data []byte, mode os.FileMode) error {
		mu.Lock()
		written = append(written, filepath.Base(path))
		mu.Unlock()
		return original(path, data, mode)
	}
	box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	if len(written) < 14 || written[len(written)-1] != "SHA256SUMS" || slices.Index(written, "SHA256SUMS") != len(written)-1 {
		t.Fatalf("the files were written in this order: %v", written)
	}
	if written[0] != "facts-before.json" {
		t.Errorf("the first file is %s: the facts are taken before anything else is put on the box", written[0])
	}
}

func TestAFolderThatCannotBeWrittenIsRemovedWhole(t *testing.T) {
	box := newUpgradeBox(t)
	plan := box.anUpgradePlan(osplan.VerdictOK, nil)
	original := osUpgradeWriteFile
	t.Cleanup(func() { osUpgradeWriteFile = original })
	count := 0
	osUpgradeWriteFile = func(path string, data []byte, mode os.FileMode) error {
		count++
		return original(path, data, mode)
	}
	box.aRun(plan, osrun.RunHop)
	total := count
	if err := os.RemoveAll(osUpgradeRunFolder("0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19")); err != nil {
		t.Fatal(err)
	}
	for failAt := 1; failAt <= total; failAt++ {
		count = 0
		osUpgradeWriteFile = func(path string, data []byte, mode os.FileMode) error {
			count++
			if count == failAt {
				return errors.New("no room")
			}
			return original(path, data, mode)
		}
		spec := box.specFor(plan, osrun.RunHop)
		if err := writeOSUpgradeRun(spec); err == nil {
			t.Fatalf("a write that fails (%d of %d) is not an error", failAt, total)
		}
		if _, err := os.Lstat(osUpgradeRunFolder(spec.Meta.RunID)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a folder is left after the write number %d failed: %v", failAt, err)
		}
	}
}

func (b *upgradeBox) specFor(plan *osplan.Plan, kind string) osUpgradeRunSpec {
	b.t.Helper()
	runID := "0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19"
	auto, err := osplan.BuildAuto(*plan, osplan.AutoParams{RunID: runID, Hostname: "box1"})
	if err != nil {
		b.t.Fatal(err)
	}
	return osUpgradeRunSpec{
		Meta:   osUpgradeRunMeta{RunID: runID, Kind: kind, Hop: plan.Hop, PlanID: plan.PlanID, BK: osUpgradeUnitBackups + "/casaos-os-upgrade-" + plan.PlanID, CreatedAt: b.now()},
		Driver: osrun.Driver, Blocks: auto.Blocks, Forward: auto.Forward, Plan: []byte("{}"), Manual: "#!/bin/sh\n",
	}
}

func TestAFolderThatIsThereIsNotWrittenOver(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	before := readText(t, filepath.Join(run.Dir, "driver.sh"))
	if err := writeOSUpgradeRun(box.specFor(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)); err == nil {
		t.Fatal("a second run with the same id is written")
	}
	if got := readText(t, filepath.Join(run.Dir, "driver.sh")); got != before {
		t.Error("the files of the run that was there are changed, or gone")
	}
}

// ---- the sums are held ----------------------------------------------------------------------------------------------------

func TestTheSumsHoldWhatTheCoreWrote(t *testing.T) {
	box := newUpgradeBox(t)
	run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
	if err := verifyOSUpgradeRun(run.Dir, osrun.RunHop); err != nil {
		t.Fatalf("the folder as it was written: %v", err)
	}
	// ... and a rehearsal's is the same folder, and a refresh's is not
	if err := verifyOSUpgradeRun(run.Dir, osrun.RunRefresh); err == nil {
		t.Error("the sums of a hop pass for a refresh")
	}
}

func TestAFolderThatIsNotWhatWasWrittenDoesNotPassTheSums(t *testing.T) {
	sumsPath := func(dir string) string { return filepath.Join(dir, "SHA256SUMS") }
	cases := map[string]func(t *testing.T, dir string){
		"a block with a line added": func(t *testing.T, dir string) {
			appendTo(t, filepath.Join(dir, "blocks", "download.sh"), "reboot\n")
		},
		"the repair block changed": func(t *testing.T, dir string) {
			appendTo(t, filepath.Join(dir, "blocks", "forward.sh"), "# x\n")
		},
		"the driver changed": func(t *testing.T, dir string) { appendTo(t, filepath.Join(dir, "driver.sh"), "# x\n") },
		"the name of the backup folder": func(t *testing.T, dir string) {
			rewrite(t, filepath.Join(dir, "bk-path"), "/var/backups/casaos-os-upgrade-x\n")
		},
		"a block that is missing":         func(t *testing.T, dir string) { remove(t, filepath.Join(dir, "blocks", "after.sh")) },
		"a block that is not in the sums": func(t *testing.T, dir string) { rewrite(t, filepath.Join(dir, "blocks", "extra.sh"), "reboot\n") },
		"a stray file in blocks":          func(t *testing.T, dir string) { rewrite(t, filepath.Join(dir, "blocks", ".minimal.sh.tmp-1"), "x") },
		"no sums":                         func(t *testing.T, dir string) { remove(t, sumsPath(dir)) },
		"empty sums":                      func(t *testing.T, dir string) { rewrite(t, sumsPath(dir), "") },
		"a line that is not one":          func(t *testing.T, dir string) { appendTo(t, sumsPath(dir), "not a sum\n") },
		"a line with one space": func(t *testing.T, dir string) {
			rewrite(t, sumsPath(dir), strings.Replace(readText(t, sumsPath(dir)), "  driver.sh", " driver.sh", 1))
		},
		"a line for a file that is not the run's": func(t *testing.T, dir string) {
			appendTo(t, sumsPath(dir), strings.Repeat("0", 64)+"  /etc/passwd\n")
		},
		"a line for a block that the run does not have": func(t *testing.T, dir string) {
			appendTo(t, sumsPath(dir), strings.Repeat("0", 64)+"  blocks/extra.sh\n")
		},
		"a line that is there twice": func(t *testing.T, dir string) {
			lines := strings.Split(readText(t, sumsPath(dir)), "\n")
			rewrite(t, sumsPath(dir), strings.Join(append(lines[:len(lines)-1], lines[0], ""), "\n"))
		},
		"a line that is missing": func(t *testing.T, dir string) {
			lines := strings.Split(readText(t, sumsPath(dir)), "\n")
			rewrite(t, sumsPath(dir), strings.Join(lines[1:], "\n"))
		},
		"a sum in capitals": func(t *testing.T, dir string) {
			text := readText(t, sumsPath(dir))
			rewrite(t, sumsPath(dir), strings.ToUpper(text[:64])+text[64:])
		},
		"sums that are too long": func(t *testing.T, dir string) { rewrite(t, sumsPath(dir), strings.Repeat("0", 70000)) },
		"a block that is a link": func(t *testing.T, dir string) {
			target := filepath.Join(dir, "blocks", "verify.sh")
			content := readText(t, target)
			rewrite(t, filepath.Join(dir, "elsewhere"), content)
			remove(t, target)
			if err := os.Symlink(filepath.Join(dir, "elsewhere"), target); err != nil {
				t.Skipf("no symbolic links here: %v", err)
			}
		},
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			box := newUpgradeBox(t)
			run := box.aRun(box.anUpgradePlan(osplan.VerdictOK, nil), osrun.RunHop)
			damage(t, run.Dir)
			if err := verifyOSUpgradeRun(run.Dir, osrun.RunHop); err == nil {
				t.Error("the folder passes the sums")
			}
		})
	}
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func rewrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func remove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// What the folder has is one set of blocks: the ones the lists of every phase can name, the ones the sums cover, and the ones the driver knows.
func TestTheBlocksOfTheListsTheSumsAndTheDriverAreOneSet(t *testing.T) {
	covered := map[string]bool{}
	for _, name := range osUpgradeCovered(osrun.RunHop) {
		if id, ok := strings.CutPrefix(name, "blocks/"); ok {
			covered[strings.TrimSuffix(id, ".sh")] = true
		}
	}
	every := []osrun.Files{
		{}, {Begun: true}, {DoneMinimal: true}, {DoneMinimal: true, DoneFull: true}, {DoneMinimal: true, DoneFull: true, DonePrereboot: true}, {DoneMinimal: true, DoneFull: true, DonePrereboot: true, DoneAfter: true},
	}
	for _, phase := range []string{osrun.PhaseRehearse, osrun.PhaseRun, osrun.PhaseRecover, osrun.PhaseVerify, osrun.PhaseUnwind} {
		for _, files := range every {
			for _, id := range osrun.BlockList(phase, files) {
				if !covered[id] {
					t.Errorf("the list of %s names %s, which the sums do not cover", phase, id)
				}
				if !osrun.ValidBlock(id) {
					t.Errorf("the list of %s names %s, which is no block", phase, id)
				}
			}
		}
	}
	refresh := map[string]bool{}
	for _, name := range osUpgradeCovered(osrun.RunRefresh) {
		if id, ok := strings.CutPrefix(name, "blocks/"); ok {
			refresh[strings.TrimSuffix(id, ".sh")] = true
		}
	}
	for _, id := range osrun.BlockList(osrun.PhaseRefresh, osrun.Files{}) {
		if !refresh[id] {
			t.Errorf("the list of the refresh names %s, which the sums of a refresh do not cover", id)
		}
	}
	// the ids of a run are the ids that the driver and the package of the words know: all of them, and no other
	var all []string
	for _, kind := range []string{osrun.RunHop, osrun.RunRefresh} {
		all = append(all, osUpgradeBlockIDs(kind)...)
	}
	slices.Sort(all)
	all = slices.Compact(all)
	want := slices.Clone(osrun.BlockIDs)
	slices.Sort(want)
	if !reflect.DeepEqual(all, want) {
		t.Errorf("the blocks that a run has are %v, the blocks that are known %v", all, want)
	}
	// ... and the nine blocks that the plan's unit text has are the nine that the sums name, in their order
	box := newUpgradeBox(t)
	auto, err := osplan.BuildAuto(*box.anUpgradePlan(osplan.VerdictOK, nil), osplan.AutoParams{RunID: testRunID, Hostname: "box1"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, blk := range auto.Blocks {
		ids = append(ids, blk.ID)
	}
	if !reflect.DeepEqual(ids, osUpgradeBlockIDs(osrun.RunHop)[:9]) || auto.Forward.ID != osUpgradeBlockIDs(osrun.RunHop)[9] {
		t.Errorf("the blocks of the plan are %v and %s, the run's are %v", ids, auto.Forward.ID, osUpgradeBlockIDs(osrun.RunHop))
	}
}

// ---- the log ---------------------------------------------------------------------------------------------------------------

func TestTheLogIsMadeOnceAndAppendedTo(t *testing.T) {
	dir := t.TempDir()
	nonce := "0123456789abcdef0123456789abcdef"
	at := time.Date(2026, time.October, 13, 10, 0, 0, 0, time.UTC)
	if err := createOSUpgradeMarkers(dir, nonce, at); err != nil {
		t.Fatal(err)
	}
	if err := createOSUpgradeMarkers(dir, nonce, at); err == nil {
		t.Fatal("the log of a run is made twice")
	}
	if err := appendOSUpgradeMarker(dir, nonce, osrun.Marker{Kind: osrun.MkQueued, At: at}); err != nil {
		// a second QUEUED is a line that the reader does not take, but it is a valid line to write
		t.Fatal(err)
	}
	for _, bad := range []osrun.Marker{{Kind: "NOPE"}, {Kind: osrun.MkUnitStart}, {Kind: osrun.MkAttempt, N: 1, Phase: "reboot", Who: osrun.WhoAuto, At: at}} {
		if err := appendOSUpgradeMarker(dir, nonce, bad); err == nil {
			t.Errorf("%+v is written to the log", bad)
		}
	}
	if err := appendOSUpgradeMarker(dir, "short", osrun.Marker{Kind: osrun.MkTakeover, At: at}); err == nil {
		t.Error("a marker is written with a nonce that is not one")
	}
	text := readText(t, filepath.Join(dir, "markers"))
	if strings.Count(text, "\n") != 2 || !strings.HasPrefix(text, "CASAOS_OSU_QUEUED "+nonce+" 0 2026-10-13T10:00:00Z\n") {
		t.Errorf("the log is %q", text)
	}
}

// A unit that was killed as it wrote may leave a line that was cut: the next marker is on a line of its own, and is read.
func TestAMarkerIsNeverGluedToALineThatWasCut(t *testing.T) {
	dir := t.TempDir()
	nonce := "0123456789abcdef0123456789abcdef"
	at := time.Date(2026, time.October, 13, 10, 0, 0, 0, time.UTC)
	if err := createOSUpgradeMarkers(dir, nonce, at); err != nil {
		t.Fatal(err)
	}
	appendTo(t, filepath.Join(dir, "markers"), "CASAOS_OSU_BLOCK_STA")
	if err := appendOSUpgradeMarker(dir, nonce, osrun.Marker{Kind: osrun.MkTakeover, At: at}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "markers"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, ms, ignored, err := osrun.ParseMarkers(f)
	if err != nil || len(ms) != 2 || ms[1].Kind != osrun.MkTakeover || ignored != 1 {
		t.Fatalf("the log reads as %+v, ignored %d, %v", ms, ignored, err)
	}
}

// ---- the flags that name themselves -------------------------------------------------------------------------------------

func TestTheBusyFlagIsWrittenWholeAndWorldReadableAndTakenAway(t *testing.T) {
	box := newUpgradeBox(t)
	since := box.now()
	if err := setOSUpgradeBusy(testRunID, osrun.StateRunning, since); err != nil {
		t.Fatal(err)
	}
	var flag osUpgradeBusyFlag
	if err := json.Unmarshal([]byte(readText(t, osUpgradeBusyFile)), &flag); err != nil || flag.RunID != testRunID || flag.State != osrun.StateRunning || !flag.Since.Equal(since) {
		t.Fatalf("the flag is %+v, %v", flag, err)
	}
	if runtime.GOOS == "linux" {
		if info, err := os.Stat(osUpgradeBusyFile); err != nil || info.Mode().Perm() != 0o644 {
			t.Errorf("the flag is %v (%v), want 644", info.Mode().Perm(), err)
		}
		if info, err := os.Stat(filepath.Dir(osUpgradeBusyFile)); err != nil || info.Mode().Perm() != 0o755 {
			t.Errorf("the folder of the flag is %v (%v), want 755", info.Mode().Perm(), err)
		}
	}
	if busy, err := osUpgradeFlagOpen(); !busy || err != nil {
		t.Fatalf("the flag that was set is %v, %v", busy, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(osUpgradeBusyFile))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("%s is left over from the write of the flag", e.Name())
		}
	}
	if err := clearOSUpgradeBusy(); err != nil {
		t.Fatal(err)
	}
	if err := clearOSUpgradeBusy(); err != nil {
		t.Fatalf("a flag that is not there cannot be taken away: %v", err)
	}
	if busy, err := osUpgradeFlagOpen(); busy || err != nil {
		t.Fatalf("the flag that was taken away is %v, %v", busy, err)
	}
}

func TestTheLatestRunIsSaidFailClosed(t *testing.T) {
	box := newUpgradeBox(t)
	if id, state := currentOSUpgrade(); id != "" || state != osUpgradeNoRun {
		t.Fatalf("no pointer: %q, %d", id, state)
	}
	if err := setOSUpgradeCurrent(testRunID); err != nil {
		t.Fatal(err)
	}
	if id, state := currentOSUpgrade(); id != testRunID || state != osUpgradeHasRun {
		t.Fatalf("a pointer: %q, %d", id, state)
	}
	if err := setOSUpgradeCurrent("../etc"); err == nil {
		t.Error("something that is not a run id is the latest run")
	}
	for name, content := range map[string]string{"words": "the latest\n", "an id in capitals": strings.ToUpper(testRunID) + "\n", "a path": "../" + testRunID + "\n", "nothing": ""} {
		rewrite(t, filepath.Join(box.root, "current"), content)
		if id, state := currentOSUpgrade(); id != "" || state != osUpgradeBadRun {
			t.Errorf("%s: %q, %d: a pointer that says nothing is not no pointer", name, id, state)
		}
	}
	if err := os.Remove(filepath.Join(box.root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(box.root, "current"), 0o755); err != nil {
		t.Fatal(err)
	}
	if id, state := currentOSUpgrade(); id != "" || state != osUpgradeBadRun {
		t.Errorf("a pointer that is a folder: %q, %d", id, state)
	}
	os.Remove(filepath.Join(box.root, "current"))
	if err := setOSUpgradeCurrent(testRunID); err != nil {
		t.Fatal(err)
	}
	if err := clearOSUpgradeCurrent(); err != nil {
		t.Fatal(err)
	}
	if err := clearOSUpgradeCurrent(); err != nil {
		t.Fatalf("a pointer that is not there cannot be forgotten: %v", err)
	}
}

// ---- retention -----------------------------------------------------------------------------------------------------------

func TestTheLastThreeRunsAreKept(t *testing.T) {
	box := newUpgradeBox(t)
	if err := ensureOSUpgradeRoot(); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 5)
	for i := range ids {
		ids[i] = fmt.Sprintf("%08x-6c1e-4d0a-9a53-7f1c2d4e8b19", i+1)
		dir := filepath.Join(box.root, ids[i])
		if err := os.MkdirAll(filepath.Join(dir, "blocks"), 0o700); err != nil {
			t.Fatal(err)
		}
		rewrite(t, filepath.Join(dir, "run.json"), "{}")
		rewrite(t, filepath.Join(dir, "markers"), "x")
		when := box.now().Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(filepath.Join(dir, "run.json"), when, when); err != nil {
			t.Fatal(err)
		}
	}
	// what is not a run is not touched: a name that is not a UUID, a file, a folder with no run.json, a link
	for _, name := range []string{"notes", "busy", "last-refresh", "current"} {
		rewrite(t, filepath.Join(box.root, name), "x")
	}
	os.MkdirAll(filepath.Join(box.root, "ffffffff-6c1e-4d0a-9a53-7f1c2d4e8b19"), 0o700) // no run.json
	os.MkdirAll(filepath.Join(box.root, "backup-of-something"), 0o700)
	rewrite(t, filepath.Join(box.root, "backup-of-something", "run.json"), "{}")
	outside := t.TempDir()
	rewrite(t, filepath.Join(outside, "run.json"), "{}")
	linkName := filepath.Join(box.root, "eeeeeeee-6c1e-4d0a-9a53-7f1c2d4e8b19")
	linked := os.Symlink(outside, linkName) == nil

	if err := pruneOSUpgradeRuns(osUpgradeKeepRuns); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		_, err := os.Lstat(filepath.Join(box.root, id))
		if kept := err == nil; kept != (i >= 2) {
			t.Errorf("run %d (%s): kept = %v", i, id, kept)
		}
	}
	for _, name := range []string{"notes", "busy", "last-refresh", "current", "ffffffff-6c1e-4d0a-9a53-7f1c2d4e8b19", "backup-of-something"} {
		if _, err := os.Lstat(filepath.Join(box.root, name)); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
	if linked {
		if _, err := os.Lstat(filepath.Join(outside, "run.json")); err != nil {
			t.Errorf("what a link led to was removed: %v", err)
		}
	}
}

func TestTheLatestRunIsNeverRemovedWhateverItsAge(t *testing.T) {
	box := newUpgradeBox(t)
	if err := ensureOSUpgradeRoot(); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("%08x-6c1e-4d0a-9a53-7f1c2d4e8b19", i+1)
		ids = append(ids, id)
		dir := filepath.Join(box.root, id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		rewrite(t, filepath.Join(dir, "run.json"), "{}")
		when := box.now().Add(time.Duration(-i) * time.Hour)
		os.Chtimes(filepath.Join(dir, "run.json"), when, when)
	}
	if err := setOSUpgradeCurrent(ids[4]); err != nil { // the oldest
		t.Fatal(err)
	}
	if err := pruneOSUpgradeRuns(1); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		_, err := os.Lstat(filepath.Join(box.root, id))
		if kept := err == nil; kept != (i == 0 || i == 4) {
			t.Errorf("run %d: kept = %v", i, kept)
		}
	}
	if err := pruneOSUpgradeRuns(3); err != nil {
		t.Fatalf("nothing to remove: %v", err)
	}
	// a folder of runs that is not there is no error
	if err := os.RemoveAll(box.root); err != nil {
		t.Fatal(err)
	}
	if err := pruneOSUpgradeRuns(3); err != nil {
		t.Fatalf("no folder of runs: %v", err)
	}
}
