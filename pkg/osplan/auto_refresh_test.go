package osplan

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The text of a refresh, written for the unit: R0 to R3 and the block that mends what dpkg left. The tests of the shell that it is are in auto_refresh_exec_test.go and
// auto_refresh_flow_test.go; these are of the text, as auto_test.go is of the hop's.

// refreshInputsOf is the refresh of the real box of the measurement: the machine's own sources (the archive's host and all), with Docker's put aside.
func refreshInputsOf(t *testing.T, accept bool) RefreshInputs {
	t.Helper()
	hop := bullseyeToBookworm.ForRefresh()
	rw, err := RewriteSources(context.Background(), hop, orderLikeTheBox(d11Box(t)), &fakeDecider{})
	if err != nil {
		t.Fatalf("the sources of the real box: %v", err)
	}
	return RefreshInputs{Hop: hop, Rewritten: rw, AcceptReleaseInfo: accept, RunID: testRunID, Hostname: testHostname}
}

func refreshOf(t *testing.T, accept bool) *AutoRunbook {
	t.Helper()
	ar, err := BuildRefresh(refreshInputsOf(t, accept))
	if err != nil {
		t.Fatalf("BuildRefresh: %v", err)
	}
	return ar
}

// autoTexts are the texts of the unit that the tests of the text look at: the hop's, and the refresh's.
func autoTexts(t *testing.T) map[string]*AutoRunbook {
	t.Helper()
	_, hop := realAuto(t)
	return map[string]*AutoRunbook{"the hop": hop, "the refresh": refreshOf(t, false)}
}

func TestTheTextOfTheRefreshOfTheRealBoxIsTheGolden(t *testing.T) {
	got := allAuto(refreshOf(t, false))
	path := filepath.Join("testdata", "runbook-d11-12.refresh.golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s rewritten", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./pkg/osplan -run TextOfTheRefreshOfTheRealBox -update writes it)", err)
	}
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); got != w {
		t.Errorf("the text of the refresh is not the golden one (-update rewrites it, and the difference is read before it is kept):\n%s", firstDifference(got, w))
	}
}

func TestTheRefreshIsTheFourBlocksOfTheDriverAndTheOneThatMends(t *testing.T) {
	ar := refreshOf(t, false)
	var ids []string
	for _, b := range ar.Blocks {
		ids = append(ids, b.ID)
		if b.Title == "" {
			t.Errorf("block %s has no title", b.ID)
		}
	}
	// the list of the refresh that the driver is given is the one of osrun, which is the four blocks of this text
	if !slices.Equal(ids, osrun.BlockList(osrun.PhaseRefresh, osrun.Files{})) || !slices.Equal(ids, []string{"rcheck", "rsources", "rupgrade", "rdone"}) {
		t.Errorf("blocks %v", ids)
	}
	if ar.Forward.ID != osrun.BlockForward || ar.Forward.Title == "" || ar.RunID != testRunID || ar.PlanID != "" {
		t.Errorf("forward %q, run %q, plan %q", ar.Forward.ID, ar.RunID, ar.PlanID)
	}
	// the files that the service lists in SHA256SUMS for a refresh are these blocks and the one that mends
	for _, id := range append(slices.Clone(ids), osrun.BlockForward) {
		if !slices.Contains(osrun.BlockIDs, id) {
			t.Errorf("%s is no block of the driver", id)
		}
	}
	for _, blk := range everyAutoBlock(ar) {
		if !strings.HasPrefix(blk.Text, "# casaos-os-upgrade "+blk.ID+" "+testRunID+"\nset -eu\n") || !strings.HasSuffix(blk.Text, "\n") || strings.HasSuffix(blk.Text, "\n\n") || strings.ContainsRune(blk.Text, '\r') {
			t.Errorf("block %s: the shape of its text: %q", blk.ID, blk.Text[:min(len(blk.Text), 80)])
		}
		for _, bad := range []string{blockStart, blockEnd, ">>> BLOCK", "<<< END BLOCK"} {
			if strings.Contains(blk.Text, bad) {
				t.Errorf("block %s has the marker %q of the owner's text", blk.ID, bad)
			}
		}
		if blk.ID != osrun.BlockForward && !strings.Contains(blk.Text, `: > "$BK/done-`+blk.ID+`"`) {
			t.Errorf("block %s does not leave its marker", blk.ID)
		}
		// every block of the refresh works in the folder of the refresh, and in no folder of a plan
		if !strings.Contains(blk.Text, "\nBK="+RefreshFolderPrefix+testRunID+"\n") {
			t.Errorf("block %s does not work in the folder of the refresh", blk.ID)
		}
	}
	// the last block says that the folder holds the marker of the whole, the file that the driver reads
	if last := ar.Blocks[3].Text; !strings.Contains(last, `"$BK/refresh-reboot"`) || !strings.Contains(last, `: > "$BK/done-rdone"`) {
		t.Error("the last block leaves neither its marker nor the word of the restart")
	}
}

func TestEveryHookOfTheRefreshFiresOnceWhereItHasASiteAndNowhereElse(t *testing.T) {
	_, fired, err := buildRefresh(refreshInputsOf(t, false))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{}
	for _, id := range []string{"rcheck", "rsources", "rupgrade", "rdone", "forward"} {
		want[id+"/H1"] = 1
	}
	for _, id := range []string{"rcheck", "rsources", "rupgrade", "forward"} {
		want[id+"/H6"] = 1
	}
	if !reflectEqual(fired, want) {
		t.Errorf("the hooks that went off are %v, want %v", fired, want)
	}
	// a hook that went off more or less often, or where it has no site, is drift
	c := &rbCtx{fired: map[string]int{}}
	for k, v := range fired {
		c.fired[k] = v
	}
	if err := c.checkSites(autoRefreshHookSites()); err != nil {
		t.Errorf("the hooks of the refresh: %v", err)
	}
	for name, mutate := range map[string]func(m map[string]int){
		"a hook that did not go off":                  func(m map[string]int) { delete(m, "rupgrade/H6") },
		"a hook that went off twice":                  func(m map[string]int) { m["rcheck/H1"] = 2 },
		"a hook of the hop in a block of the refresh": func(m map[string]int) { m["rsources/H7"] = 1 },
	} {
		m := map[string]int{}
		for k, v := range fired {
			m[k] = v
		}
		mutate(m)
		c.fired = m
		if err := c.checkSites(autoRefreshHookSites()); !errors.Is(err, ErrAutoDrift) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The gate of the unit and the trap that says the class of a stop are the first thing of every block of the refresh, as of the hop's, with the id of the run.
func TestEveryBlockOfTheRefreshStartsWithTheGateOfItsUnit(t *testing.T) {
	for _, blk := range everyAutoBlock(refreshOf(t, false)) {
		want := "set -eu\nLC_ALL=C; export LC_ALL\nPATH=/usr/sbin:/usr/bin:/sbin:/bin; export PATH\nAUTOCLASS=unknown\nosu_exit() { rc=$?; [ \"$rc\" -eq 0 ] || printf '%s\\n' \"$AUTOCLASS\" > \"${CASAOS_OSU_DIR:-/nonexistent}/stop-class\"; }\ntrap osu_exit EXIT\n[ \"${CASAOS_OSU_RUN:-}\" = '" + testRunID + "' ] && grep -q 'casaos-os-upgrade\\.service' /proc/self/cgroup || { AUTOCLASS=unit; echo \"STOP: this block belongs to the upgrade unit"
		if !strings.Contains(blk.Text, want) || strings.Count(blk.Text, "trap osu_exit EXIT") != 1 {
			t.Errorf("block %s has no trap and gate right after its head", blk.ID)
		}
	}
}

// What the owner said of a change of release information is in the update of R1 and nowhere else: the option, which lets apt take a repository whose release information
// changed, is the owner's click, and never in the text without it.
func TestTheOptionToAcceptAChangeOfReleaseInfoIsInTheUpdateOfR1OnlyWhenTheOwnerSaidSo(t *testing.T) {
	const option = "--allow-releaseinfo-change"
	without, with := refreshOf(t, false), refreshOf(t, true)
	if strings.Contains(allAuto(without), option) {
		t.Error("the option is in the text of a refresh that the owner did not accept")
	}
	if n := strings.Count(allAuto(with), option); n != 1 || strings.Count(with.Blocks[1].Text, "aptq update --error-on=any "+option+" > ") != 1 {
		t.Errorf("the option is in the text %d times, and not in the update of R1:\n%s", n, with.Blocks[1].Text)
	}
	// the rest of the text is the same
	if a, b := strings.Replace(allAuto(with), " "+option, "", 1), allAuto(without); a != b {
		t.Errorf("the two texts differ beyond the option:\n%s", firstDifference(a, b))
	}
}

func TestBuildRefreshRefusesWhatItCannotCarry(t *testing.T) {
	good := refreshInputsOf(t, false)
	mutated := func(f func(in *RefreshInputs)) RefreshInputs {
		in := good
		f(&in)
		return in
	}
	for _, tc := range []struct {
		name string
		in   RefreshInputs
		want error
	}{
		{"the hop of a plan, and not of a refresh", mutated(func(in *RefreshInputs) { in.Hop = bullseyeToBookworm }), ErrNoRunbook},
		{"no hop", mutated(func(in *RefreshInputs) { in.Hop = HopSpec{Refresh: true} }), ErrNoRunbook},
		{"no sources", mutated(func(in *RefreshInputs) { in.Rewritten = nil }), ErrNoRunbook},
		{"no tree", mutated(func(in *RefreshInputs) { in.Rewritten = &Rewritten{} }), ErrNoRunbook},
		{"a run that is no UUID", mutated(func(in *RefreshInputs) { in.RunID = "not-a-uuid" }), ErrRunbookUnsafe},
		{"a name of the machine that is none", mutated(func(in *RefreshInputs) { in.Hostname = "a b;c" }), ErrRunbookUnsafe},
		{"no name of the machine", mutated(func(in *RefreshInputs) { in.Hostname = "" }), ErrRunbookUnsafe},
		{"a file of cgroups that is no path", mutated(func(in *RefreshInputs) { in.CgroupFile = "a b" }), ErrRunbookUnsafe},
		{"a line of the sources that would end a heredoc", mutated(func(in *RefreshInputs) {
			rw := *in.Rewritten
			rw.Tree = []TreeFile{{Path: "sources.list", Content: "# CASAOS_EOF\ndeb http://deb.debian.org/debian bullseye main\n"}}
			in.Rewritten = &rw
		}), ErrRunbookUnsafe},
		{"a tree that is more than the text may be", mutated(func(in *RefreshInputs) {
			rw := *in.Rewritten
			rw.Tree = []TreeFile{{Path: "sources.list", Content: strings.Repeat("# "+strings.Repeat("x", 90)+"\n", 3000)}}
			in.Rewritten = &rw
		}), ErrRunbookTooLarge},
	} {
		if _, err := BuildRefresh(tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := BuildRefresh(good); err != nil {
		t.Errorf("the refresh of the real box: %v", err)
	}
}

// The hostname of the machine is written between quotes that cannot be left (it is a hostname, which has no quote, and the shell is told of it by shQ all the same).
func TestTheNameOfTheMachineIsWrittenOnceInR0AndNowhereElse(t *testing.T) {
	in := refreshInputsOf(t, false)
	in.Hostname = "box-1.example"
	ar, err := BuildRefresh(in)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(allAuto(ar), "box-1.example"); n != 1 || !strings.Contains(ar.Blocks[0].Text, `[ "$(hostname)" = 'box-1.example' ] || `) {
		t.Errorf("the name is in the text %d times", n)
	}
}

// ---- the same bans as the hop's text, on the text of the refresh ------------------------------------------------------------

func TestTheTextOfTheRefreshRestartsNothingAndTracesNothingAndReadsNoTerminalAndRunsOnlyKnownCommands(t *testing.T) {
	banned := []string{"reboot", "shutdown", "poweroff", "halt", "telinit", "init", "kexec"}
	known := map[string]string{"trap": "the class of a stop", "osu_exit": "a function of the prologue", "sync": "the marker of the point of no return", "no_removals": "a function of R2"}
	for k, v := range knownCommandWords {
		known[k] = v
	}
	for _, blk := range everyAutoBlock(refreshOf(t, false)) {
		list, rest := heredocs(blk.Text)
		words := commandWords(rest)
		for _, h := range list {
			if strings.Contains(h.opener, ".sh\"") {
				words = append(words, commandWords(h.body)...)
			}
		}
		for _, w := range words {
			if slices.Contains(banned, w) {
				t.Errorf("block %s runs %q", blk.ID, w)
			}
			if _, ok := known[w]; !ok {
				t.Errorf("block %s runs %q, which is not a command of the owner's text or of the unit's", blk.ID, w)
			}
		}
		for _, line := range strings.Split(rest, "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, "#") {
				continue
			}
			if regexp.MustCompile(`systemctl\s+(-\S+\s+)*(reboot|poweroff|halt|kexec|suspend|hibernate|isolate)`).MatchString(l) {
				t.Errorf("block %s restarts the machine: %q", blk.ID, l)
			}
			if regexp.MustCompile(`(^|[;&| ])set\s+-[a-z]*x|xtrace|(^|\s)sh\s+-[a-z]*x`).MatchString(l) {
				t.Errorf("block %s traces its commands: %q", blk.ID, l)
			}
			if regexp.MustCompile(`(^|[;&| ])read( |$)`).MatchString(strings.ReplaceAll(l, "while read -r unit state", "")) {
				t.Errorf("block %s reads the terminal: %q", blk.ID, l)
			}
		}
	}
}

func TestTheTextOfTheRefreshSaysNothingOfHowItCameToBe(t *testing.T) {
	all := strings.ToLower(allAuto(refreshOf(t, false)))
	for _, bad := range []string{"review round", "round 1", "round 2", "round 3", "commit", "chroot", "no harm", "all checks passed", "units.state", "boot_id-full"} {
		if strings.Contains(all, bad) {
			t.Errorf("the text names %q", bad)
		}
	}
}

func TestEveryBlockOfTheRefreshIsShellThatTheShellParses(t *testing.T) {
	sh := shellOrSkip(t)
	dir := t.TempDir()
	for _, blk := range everyAutoBlock(refreshOf(t, true)) {
		name := blk.ID + ".sh"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(blk.Text), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := runIn(dir, sh, "-n", name); err != nil {
			t.Errorf("block %s: sh -n: %v\n%s", blk.ID, err, out)
		}
	}
}

// The sources that R1 writes are the tree of the refresh, byte for byte (the files of the machine, which a refresh keeps), and R1 reads them back against the hash that the
// rewrite made of them; the restore script is the owner's, verbatim.
func TestR1HoldsTheTreeAndTheRestoreScriptByteForByte(t *testing.T) {
	in := refreshInputsOf(t, false)
	ar, err := BuildRefresh(in)
	if err != nil {
		t.Fatal(err)
	}
	text := ar.Blocks[1].Text
	list, _ := heredocs(text)
	var bodies []string
	for _, h := range list {
		bodies = append(bodies, h.opener)
		switch {
		case strings.Contains(h.opener, "restore-sources.sh"):
			if h.body != strings.TrimSuffix(restoreScript, "\n") {
				t.Error("the restore script is not the owner's")
			}
		case strings.Contains(h.opener, ".casaos-new"):
			want := ""
			for _, f := range in.Rewritten.Tree {
				if strings.Contains(h.opener, treeDest(f)+".casaos-new") {
					want = f.Content
				}
			}
			if h.body != strings.TrimSuffix(want, "\n") || want == "" {
				t.Errorf("the heredoc %s is not the file of the tree", h.opener)
			}
		default:
			t.Errorf("a heredoc that is neither: %s", h.opener)
		}
	}
	if len(bodies) != 2 {
		t.Errorf("heredocs: %v", bodies)
	}
	for _, want := range []string{in.Rewritten.HashCurrent, in.Rewritten.HashGenerated} {
		if !strings.Contains(text, "'"+want+"'") {
			t.Errorf("R1 does not hold the hash %s", want)
		}
	}
	// the files of R3's check are the ones that R1 started from
	if !strings.Contains(ar.Blocks[3].Text, "[ \"$now\" = '"+in.Rewritten.HashCurrent+"' ] || ") {
		t.Error("R3 does not compare the sources it put back with the hash of the files that the refresh started from")
	}
}

func runIn(dir, sh string, args ...string) (string, error) {
	cmd := exec.Command(sh, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
