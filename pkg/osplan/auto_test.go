package osplan

import (
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

// The text of the unit, written for a plan, and what the tests of it know about the box of the unit that the real fixture stands for.
const (
	testRunID    = "6f9c3a52-1f7e-4c1b-9a47-2d8e5b0c7a13"
	testHostname = "testbox"
)

// realAuto is the auto text of the real plan.
func realAuto(t *testing.T) (*Plan, *AutoRunbook) {
	t.Helper()
	p := realPlan(t)
	return p, autoOf(t, *p, AutoParams{RunID: testRunID, Hostname: testHostname})
}

func autoOf(t *testing.T, p Plan, a AutoParams) *AutoRunbook {
	t.Helper()
	ar, err := BuildAuto(p, a)
	if err != nil {
		t.Fatalf("BuildAuto: %v", err)
	}
	return ar
}

// autoBlockOf is the text of one block of an auto runbook, F included.
func autoBlockOf(t *testing.T, ar *AutoRunbook, id string) string {
	t.Helper()
	if id == osrun.BlockForward {
		return ar.Forward.Text
	}
	for _, b := range ar.Blocks {
		if b.ID == id {
			return b.Text
		}
	}
	t.Fatalf("no block %s", id)
	return ""
}

// everyAutoBlock is the nine blocks of an auto runbook and F, in the order of the golden.
func everyAutoBlock(ar *AutoRunbook) []AutoBlock { return append(slices.Clone(ar.Blocks), ar.Forward) }

// allAuto is the text of the whole auto runbook, in the order of the golden: the nine blocks, then F.
func allAuto(ar *AutoRunbook) string {
	var b strings.Builder
	for _, blk := range everyAutoBlock(ar) {
		b.WriteString("# ===== blocks/" + blk.ID + ".sh\n" + blk.Text)
	}
	return b.String()
}

// planVariants are the plans whose text has STOP lines that the real plan's does not: no sizes from apt (the coarse floors of block 0), no new kernel in the list, no removal for
// the owner to accept and no file of Docker's put aside, and two removals to accept.
func planVariants(t *testing.T) map[string]Plan {
	t.Helper()
	real := *realPlan(t)
	noDisk := real
	noDisk.Disk = nil
	noKernel := real
	noKernel.NewPackages = slices.DeleteFunc(slices.Clone(real.NewPackages), func(n string) bool { return strings.HasPrefix(n, "linux-image-") })
	plain := real
	plain.Problems = nil
	plain.Paused = nil
	two := real
	two.Problems = append(slices.Clone(real.Problems), Problem{Code: ProblemRemovalManual, Severity: SeverityAcceptance, Data: map[string]any{"name": "libsemanage1"}})
	return map[string]Plan{"real": real, "no sizes": noDisk, "no new kernel": noKernel, "nothing to accept, no Docker": plain, "two removals to accept": two}
}

func TestTheAutoTextOfTheRealBoxIsTheGolden(t *testing.T) {
	_, ar := realAuto(t)
	path := filepath.Join("testdata", "runbook-d11-12.auto.golden")
	got := allAuto(ar)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s rewritten", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./pkg/osplan -run AutoTextOfTheRealBox -update writes it)", err)
	}
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); got != w {
		t.Errorf("the auto text is not the golden one (-update rewrites it, and the difference is read before it is kept, as a difference against the golden of the owner's text):\n%s", firstDifference(got, w))
	}
}

// The text of the unit is the owner's, written by the same code: but for the hooks and the classes of the STOP lines, each block is the block of the owner's runbook. The
// test makes the unit's block from the owner's by the eight changes, written here a second time, and compares.
func TestTheAutoBlocksAreTheOwnersBlocksButForTheHooksAndTheClasses(t *testing.T) {
	p, ar := realAuto(t)
	rb, err := BuildRunbook(*p)
	if err != nil {
		t.Fatal(err)
	}
	stripClasses := regexp.MustCompile(`AUTOCLASS=[a-z_]+; (echo "STOP: )`)
	docker := regexp.MustCompile(`(?s)elif \[ "\$rc" = 22 \] && \{.*?\nfi\n`)
	const aptq = "export DEBIAN_FRONTEND=noninteractive APT_LISTCHANGES_FRONTEND=none NEEDRESTART_SUSPEND=1\naptq() { apt-get -y -q -o DPkg::Lock::Timeout=300 -o Acquire::Retries=3 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold \"$@\"; }\n"
	prologue := "AUTOCLASS=unknown\n" +
		"osu_exit() { rc=$?; [ \"$rc\" -eq 0 ] || printf '%s\\n' \"$AUTOCLASS\" > \"${CASAOS_OSU_DIR:-/nonexistent}/stop-class\"; }\n" +
		"trap osu_exit EXIT\n" +
		"[ \"${CASAOS_OSU_RUN:-}\" = '" + testRunID + "' ] && grep -q 'casaos-os-upgrade\\.service' /proc/self/cgroup || { echo \"STOP: this block belongs to the upgrade unit of the dashboard and runs only there, with the confirmation of the owner baked in: to continue by hand use manual-runbook.sh of the run.\"; exit 1; }\n"
	for i, blk := range ar.Blocks {
		manual := blocks(t, rb.Text)[i]
		body := strings.TrimRight(strings.TrimSuffix(strings.TrimPrefix(manual, "(\n"), ")\n"), "\n") + "\n"
		body = strings.Replace(body, "set -eu\nLC_ALL=C; export LC_ALL\nPATH=/usr/sbin:/usr/bin:/sbin:/bin; export PATH\n", "set -eu\nLC_ALL=C; export LC_ALL\nPATH=/usr/sbin:/usr/bin:/sbin:/bin; export PATH\n"+prologue, 1)
		body = strings.ReplaceAll(body, aptq, strings.Replace(aptq, "apt-get -y -q ", "apt-get -y -q -o Dpkg::Use-Pty=0 ", 1))
		body = strings.Replace(body, "# Type the name of this machine between the quotes, then run the block.\nTYPED_HOSTNAME=\"\"\n", "TYPED_HOSTNAME='"+testHostname+"'\n", 1)
		body = strings.Replace(body, "# These packages are removed by the upgrade, and you installed them yourself. To accept it, type their names, as they are, between the quotes:\n#   fuse\nACCEPT_REMOVALS=\"\"\n", "ACCEPT_REMOVALS='fuse'\n", 1)
		body = strings.Replace(body, "# Type I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON between the quotes, then run the block.\nCONFIRM=\"\"\n", "CONFIRM=I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON\n", 1)
		body = strings.Replace(body, muxAtHand(), "", 1)
		body = strings.Replace(body, survive(), "", 1)
		body = strings.Replace(body, ": > \"$BK/begun-upgrade\"\n", ": > \"$BK/begun-upgrade\"\nsync\n", 1)
		body = docker.ReplaceAllString(body, "<Docker's answers>\n")
		want := "# casaos-os-upgrade " + blk.ID + " " + testRunID + "\n" + body
		got := docker.ReplaceAllString(stripClasses.ReplaceAllString(blk.Text, "$1"), "<Docker's answers>\n")
		if got != want {
			t.Errorf("block %s is not the owner's block but for the hooks:\n%s", blk.ID, firstDifference(got, want))
		}
	}
}

func TestTheAptqOfTheUnitIsTheGoldensAptqWithTheOptionOfTheMeasuredHopAfterDashQ(t *testing.T) {
	if want := strings.Replace(shAptq, "apt-get -y -q ", "apt-get -y -q -o Dpkg::Use-Pty=0 ", 1); shAptqAuto != want || want == shAptq {
		t.Errorf("shAptqAuto = %q\nwant        %q", shAptqAuto, want)
	}
	if strings.Contains(shAptq, "Use-Pty") {
		t.Error("the owner's aptq has the option: the pty is kept so that term.log holds what the scripts of the packages print")
	}
	// no other option than the golden's: no Immediate-Configure, no APT_EXTRA
	for _, bad := range []string{"Immediate-Configure", "APT_EXTRA"} {
		if strings.Contains(shAptqAuto, bad) {
			t.Errorf("the aptq of the unit has %s", bad)
		}
	}
}

// ---- the hooks -----------------------------------------------------------------------------------------------------------------------------

// Every hook of the owner's text has a site where it goes off, once, and none anywhere else: a block that was changed without the unit's text being told is ErrAutoDrift.
func TestEveryHookFiresOnceWhereItHasASiteAndNowhereElse(t *testing.T) {
	every := []string{"verify", "timers", "snapshot", "sources", "download", "minimal", "full", "prereboot", "after", "forward"}
	for name, p := range planVariants(t) {
		_, fired, err := buildAuto(p, AutoParams{RunID: testRunID, Hostname: testHostname})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := map[string]int{}
		for _, id := range every {
			want[id+"/H1"] = 1
		}
		for _, id := range []string{"verify", "sources", "download", "minimal", "full", "after", "forward"} {
			want[id+"/H6"] = 1
		}
		for _, id := range []string{"verify", "minimal", "full"} {
			want[id+"/H5"] = 1
		}
		want["verify/H2"] = 1
		want["minimal/H4"] = 1
		want["minimal/H7"] = 1
		if len(planManual(p)) > 0 {
			want["sources/H3"] = 1
		}
		if hasDocker(p) {
			want["after/H8"] = 1
		}
		if !reflectEqual(fired, want) {
			t.Errorf("%s: the hooks that went off are %v, want %v", name, fired, want)
		}
	}
}

func planManual(p Plan) []string {
	var names []string
	for _, pr := range p.Problems {
		if pr.Code == ProblemRemovalManual {
			names = append(names, pr.Data["name"].(string))
		}
	}
	return names
}

func hasDocker(p Plan) bool {
	for _, pa := range p.Paused {
		if pa.Docker {
			return true
		}
	}
	return false
}

func reflectEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// A hook that went off more or less often than its site says is ErrAutoDrift, and so is one that went off where it has none.
func TestAHookThatDidNotApplyExactlyOnceIsAutoDrift(t *testing.T) {
	p := *realPlan(t)
	_, fired, err := buildAuto(p, AutoParams{RunID: testRunID, Hostname: testHostname})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := newRbCtx(p)
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string, mutate func(m map[string]int)) {
		t.Helper()
		m := map[string]int{}
		for k, v := range fired {
			m[k] = v
		}
		mutate(m)
		c.fired = m
		if err := c.checkHooks(); !errors.Is(err, ErrAutoDrift) {
			t.Errorf("%s: %v", name, err)
		}
	}
	check("a hook that did not go off", func(m map[string]int) { delete(m, "sources/H3") })
	check("a hook that went off twice", func(m map[string]int) { m["minimal/H7"] = 2 })
	check("a hook that went off where it has no site", func(m map[string]int) { m["snapshot/H6"] = 1 })
	c.fired = fired
	if err := c.checkHooks(); err != nil {
		t.Errorf("the hooks of the real plan: %v", err)
	}
}

// The effect of each of the eight hooks in the text, one by one: a hook that is taken out (or made to write the owner's text) is seen here, by its name.
func TestTheHooksEachHaveTheirEffectInTheAutoText(t *testing.T) {
	_, ar := realAuto(t)
	all := allAuto(ar)
	count := func(s, sub string) int { return strings.Count(s, sub) }
	t.Run("H1 the trap and the gate of the unit are the first thing of every block", func(t *testing.T) {
		for _, blk := range everyAutoBlock(ar) {
			want := "set -eu\nLC_ALL=C; export LC_ALL\nPATH=/usr/sbin:/usr/bin:/sbin:/bin; export PATH\nAUTOCLASS=unknown\nosu_exit() { rc=$?; [ \"$rc\" -eq 0 ] || printf '%s\\n' \"$AUTOCLASS\" > \"${CASAOS_OSU_DIR:-/nonexistent}/stop-class\"; }\ntrap osu_exit EXIT\n[ \"${CASAOS_OSU_RUN:-}\" = '" + testRunID + "' ] && grep -q 'casaos-os-upgrade\\.service' /proc/self/cgroup || { AUTOCLASS=unit; echo \"STOP: this block belongs to the upgrade unit"
			if !strings.Contains(blk.Text, want) || count(blk.Text, "trap osu_exit EXIT") != 1 {
				t.Errorf("block %s has no trap and gate right after its head", blk.ID)
			}
			if lines := strings.Split(strings.TrimRight(blk.Text, "\n"), "\n"); lines[1] != "set -eu" || lines[len(lines)-1] == ")" {
				t.Errorf("block %s is wrapped in a subshell: the trap of a block would not run in it", blk.ID)
			}
		}
	})
	t.Run("H2 the name of the machine is written in", func(t *testing.T) {
		b := autoBlockOf(t, ar, "verify")
		if !strings.Contains(b, "\nTYPED_HOSTNAME='testbox'\n") || strings.Contains(b, "TYPED_HOSTNAME=\"\"") || strings.Contains(b, "Type the name of this machine between the quotes, then run the block") {
			t.Error("block 0 does not have the name of the machine written in")
		}
		if !strings.Contains(b, `[ -n "$TYPED_HOSTNAME" ] && [ "$TYPED_HOSTNAME" = "$(hostname)" ]`) {
			t.Error("block 0 does not compare the name with hostname any more")
		}
	})
	t.Run("H3 the removals that the owner accepted are the plan's own names", func(t *testing.T) {
		b := autoBlockOf(t, ar, "sources")
		if !strings.Contains(b, "\nACCEPT_REMOVALS='fuse'\n[ \"$ACCEPT_REMOVALS\" = 'fuse' ] || ") || strings.Contains(b, "ACCEPT_REMOVALS=\"\"") {
			t.Error("block 3 does not have the removals written in")
		}
		two := planVariants(t)["two removals to accept"]
		if b := autoBlockOf(t, autoOf(t, two, AutoParams{RunID: testRunID, Hostname: testHostname}), "sources"); !strings.Contains(b, "\nACCEPT_REMOVALS='fuse libsemanage1'\n") {
			t.Errorf("two removals are not written as the plan names them")
		}
		plain := planVariants(t)["nothing to accept, no Docker"]
		if b := autoBlockOf(t, autoOf(t, plain, AutoParams{RunID: testRunID, Hostname: testHostname}), "sources"); strings.Contains(b, "ACCEPT_REMOVALS") {
			t.Error("a plan with nothing to accept has a line for it")
		}
	})
	t.Run("H4 the confirmation is baked in", func(t *testing.T) {
		b := autoBlockOf(t, ar, "minimal")
		if !strings.Contains(b, "\nCONFIRM=I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON\n[ \"$CONFIRM\" = I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON ] || ") || strings.Contains(b, "CONFIRM=\"\"") {
			t.Error("block 5 does not have the confirmation written in")
		}
	})
	t.Run("H5 there is no question of the terminal", func(t *testing.T) {
		for _, bad := range []string{"TMUX", "STY", "tmux", "screen", "/dev/tty", "tty 2>", "no console here", "dies with the connection"} {
			if strings.Contains(all, bad) {
				t.Errorf("the auto text has %q: a unit has no terminal", bad)
			}
		}
	})
	t.Run("H6 apt runs without its pty, in every block that runs apt", func(t *testing.T) {
		const with = "aptq() { apt-get -y -q -o Dpkg::Use-Pty=0 -o DPkg::Lock::Timeout=300 "
		if n := count(all, "aptq() {"); n != 7 || count(all, with) != n {
			t.Errorf("%d definitions of aptq, %d of them with Dpkg::Use-Pty=0", n, count(all, with))
		}
		if count(all, "apt-get -y -q -o DPkg") != 0 {
			t.Error("an apt-get with the options of the owner's aptq and no Dpkg::Use-Pty=0")
		}
	})
	t.Run("H7 sync follows the marker of the point of no return", func(t *testing.T) {
		b := autoBlockOf(t, ar, "minimal")
		if !strings.Contains(b, "\n: > \"$BK/begun-upgrade\"\nsync\necho \"BLOCK 5: THE POINT OF NO RETURN. THE UPGRADE BEGINS NOW.\"\n") || count(all, "\nsync\n") != 1 {
			t.Error("block 5 does not sync after its marker, or something else syncs")
		}
	})
	t.Run("H8 Docker's repository that cannot be asked is a note, not a stop", func(t *testing.T) {
		b := autoBlockOf(t, ar, "after")
		for _, want := range []string{
			"printf '%s\\n' not_published > \"$BK/docker-source-note\"",
			"printf '%s\\n' unreachable > \"$BK/docker-source-note\"",
			"printf '%s\\n' no_curl > \"$BK/docker-source-note\"",
		} {
			if !strings.Contains(b, want) {
				t.Errorf("block 8 does not write %q", want)
			}
		}
		i := strings.Index(b, "elif [ \"$rc\" = 22 ] && {")
		j := strings.Index(b[i:], "\nfi\n")
		if i < 0 || j < 0 {
			t.Fatal("no answers of Docker in block 8")
		}
		if answers := b[i : i+j]; strings.Contains(answers, "exit 1") || strings.Contains(answers, "STOP:") || count(answers, "docker-source-note") != 4 {
			t.Errorf("the answers of Docker stop the block, or do not all say why:\n%s", answers)
		}
	})
}

// ---- the classes ---------------------------------------------------------------------------------------------------------------------------

var stopClassMark = regexp.MustCompile(`AUTOCLASS=([a-z_]+); (echo "STOP: )`)

// classUsage reads back the classes that the text has, for the keys of the table: it takes the classes out, gives them again, and says which key gave which. A text that does not
// come back as it was has a STOP line that got another class than the one of its key.
func classUsage(t *testing.T, ar *AutoRunbook) map[string]int {
	t.Helper()
	used := map[string]int{}
	for _, blk := range everyAutoBlock(ar) {
		raw := stopClassMark.ReplaceAllString(blk.Text, "$2")
		again, u, err := classifyUsing(stopClasses, raw)
		if err != nil {
			t.Fatalf("block %s: %v", blk.ID, err)
		}
		if again != blk.Text {
			t.Errorf("block %s: its classes are not the ones that the table gives back:\n%s", blk.ID, firstDifference(again, blk.Text))
		}
		for k, n := range u {
			used[k] += n
		}
	}
	return used
}

func TestEveryStopLineOfTheAutoTextHasOneClassAndEveryKeyOfTheTableIsUsed(t *testing.T) {
	used := map[string]int{}
	// the texts of the unit: the hop's, for each plan of the tests, and the refresh's (which has STOP lines of its own, and uses some of the hop's)
	texts := map[string]*AutoRunbook{"the refresh": refreshOf(t, false)}
	for name, p := range planVariants(t) {
		texts[name] = autoOf(t, p, AutoParams{RunID: testRunID, Hostname: testHostname})
	}
	for name, ar := range texts {
		for _, blk := range everyAutoBlock(ar) {
			// outside a heredoc, every STOP line has a class, once; inside, none has one (the restore script is copied as it is)
			list, rest := heredocs(blk.Text)
			if n, m := strings.Count(rest, `echo "STOP: `), len(stopClassMark.FindAllString(rest, -1)); n != m || n == 0 && blk.ID != "timers" && blk.ID != "snapshot" {
				t.Errorf("%s, block %s: %d STOP lines outside the heredocs, %d with a class", name, blk.ID, n, m)
			}
			for _, h := range list {
				if strings.Contains(h.body, "AUTOCLASS=") && !strings.Contains(h.opener, ".casaos-new") {
					t.Errorf("%s, block %s: a heredoc has a class in it: %s", name, blk.ID, h.opener)
				}
			}
			if strings.Count(rest, "STOP: ") != strings.Count(rest, `echo "STOP: `) {
				t.Errorf("%s, block %s: a STOP line that is not of the form echo \"STOP: ", name, blk.ID)
			}
		}
		for k, n := range classUsage(t, ar) {
			used[k] += n
		}
	}
	for _, s := range stopClasses {
		if used[s.prefix] == 0 {
			t.Errorf("the key %q gives the class of no STOP line of the auto text (of any plan of the tests): a line was changed, or the key is covered by a longer one", s.prefix)
		}
	}
}

func TestTheKeysOfTheClassTableAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	classes := map[string]bool{}
	for _, s := range stopClasses {
		if seen[s.prefix] {
			t.Errorf("the key %q is twice in the table", s.prefix)
		}
		seen[s.prefix] = true
		classes[s.class] = true
		if len(s.prefix) == 0 || len(s.prefix) > stopKeyMax {
			t.Errorf("the key %q is %d characters (1 to %d)", s.prefix, len(s.prefix), stopKeyMax)
		}
		if !osrun.ValidClass(s.class) {
			t.Errorf("the key %q has the class %q, which is no class of osrun", s.prefix, s.class)
		}
		if s.class == osrun.ClassUnknown || s.class == osrun.ClassAbort {
			t.Errorf("the key %q has the class %s, which no line of a block has (unknown is what a block that died has, abort is written by the driver)", s.prefix, s.class)
		}
		if strings.ContainsAny(s.prefix, "\"\n\\") {
			t.Errorf("the key %q has a character that is escaped in the text", s.prefix)
		}
	}
	// the classes that the blocks give: all of the classes but the two that a block does not
	for _, c := range osrun.Classes {
		if c != osrun.ClassUnknown && c != osrun.ClassAbort && !classes[c] {
			t.Errorf("no STOP line has the class %s", c)
		}
	}
}

func TestAStopLineWithNoClassIsAutoDrift(t *testing.T) {
	_, ar := realAuto(t)
	raw := stopClassMark.ReplaceAllString(autoBlockOf(t, ar, "verify"), "$2")
	for i, s := range stopClasses {
		if s.prefix != "ss is not installed" {
			continue
		}
		short := slices.Delete(slices.Clone(stopClasses), i, i+1)
		_, err := classifyWith(short, raw)
		if !errors.Is(err, ErrAutoDrift) || !strings.Contains(err.Error(), "ss is not installed") {
			t.Errorf("a STOP line that the table does not cover: %v", err)
		}
	}
	// a line with a STOP that nobody classified, in a block that was written by hand
	if _, err := classify("set -eu\n[ -e x ] || { echo \"STOP: a line that nothing covers\"; exit 1; }\n"); !errors.Is(err, ErrAutoDrift) {
		t.Errorf("a new STOP line: %v", err)
	}
}

func TestAHeredocThatIsNotTheQuotedOneOfTheBlocksIsAutoDrift(t *testing.T) {
	for _, text := range []string{
		"cat > f <<EOF\nx\nEOF\n",
		"cat > f <<-EOF\nx\nEOF\n",
		"cat > f <<'EOF'\nx\nEOF\n",
		"cat <<\"CASAOS_EOF\"\nx\nCASAOS_EOF\n",
		"cat <<CASAOS_EOF\nx\nCASAOS_EOF\n",
	} {
		if _, err := classify(text); !errors.Is(err, ErrAutoDrift) {
			t.Errorf("%q: %v", text, err)
		}
	}
	if _, err := classify("cat > f <<'CASAOS_EOF'\nx\nCASAOS_EOF\necho done\n"); err != nil {
		t.Errorf("the quoted heredoc of the blocks: %v", err)
	}
	// a heredoc body that came out different by one byte
	a := "cat > f <<'CASAOS_EOF'\nhello\nCASAOS_EOF\n"
	if !sameHeredocs(a, a) || sameHeredocs(a, strings.Replace(a, "hello", "hellp", 1)) || sameHeredocs(a, strings.Replace(a, "hello", "hello ", 1)) || sameHeredocs(a, a+a) {
		t.Error("a heredoc that is changed by one byte is not seen")
	}
}

// What is inside a heredoc is not the block's own text: the restore script has two STOP lines that stay as they are (the unwind reads its exit status only), and the lines of the
// sources are the plan's, whatever they say.
func TestAutoBlock3HoldsTheRestoreScriptAndTheTreeByteForByte(t *testing.T) {
	p, ar := realAuto(t)
	b := autoBlockOf(t, ar, "sources")
	if !strings.Contains(b, "<<'CASAOS_EOF'\n"+restoreScript+"CASAOS_EOF\n") {
		t.Error("block 3 does not hold the restore script verbatim")
	}
	if !strings.Contains(b, "sha256sum < \"$BK/restore-sources.sh\" | awk '{ print $1 }')\" = '"+restoreScriptHash()+"'") {
		t.Error("block 3 does not check the hash of the restore script")
	}
	if n := strings.Count(restoreScript, `echo "STOP: `); n != 2 || strings.Contains(restoreScript, "AUTOCLASS") {
		t.Errorf("the restore script has %d STOP lines, and a class: it is copied as it is", n)
	}
	list, _ := heredocs(b)
	got := map[string]string{}
	for _, h := range list {
		if m := regexp.MustCompile(`^cat > "\$APT_ETC/([^"]+)\.casaos-new" <<'CASAOS_EOF'$`).FindStringSubmatch(h.opener); m != nil {
			got[m[1]] = h.body
		}
	}
	for _, f := range p.Sources.Tree {
		name := strings.TrimPrefix(f.Path, "parts/")
		if f.Path != "sources.list" {
			name = "sources.list.d/" + name
		}
		if body, ok := got[name]; !ok || body != strings.TrimSuffix(f.Content, "\n") {
			t.Errorf("%s: heredoc %q, tree %q", f.Path, body, f.Content)
		}
	}
	if len(got) != len(p.Sources.Tree) {
		t.Errorf("%d heredocs for %d files", len(got), len(p.Sources.Tree))
	}
	// the owner's text has the same heredocs
	rb, _ := BuildRunbook(*p)
	want, _ := heredocs(blockOf(t, rb.Text, "sources"))
	have, _ := heredocs(b)
	if len(want) != len(have) {
		t.Fatalf("%d heredocs in the owner's block, %d in the unit's", len(want), len(have))
	}
	for i := range want {
		if want[i] != have[i] {
			t.Errorf("heredoc %d is not the owner's: %q", i, have[i].opener)
		}
	}
}

// A tree whose lines look like what the pass looks for is copied as it is: the lines of the sources are the owner's, and nothing in them is a STOP line of the block.
func TestATreeThatLooksLikeAStopLineOrAClassIsCopiedAsItIs(t *testing.T) {
	p := *realPlan(t)
	p.Sources.Tree = slices.Clone(p.Sources.Tree)
	p.Sources.Tree[0].Content += "# echo \"STOP: x\"; AUTOCLASS=guard; echo \"STOP: the hop is over\"\n# <<'CASAOS_EO'\n"
	ar := autoOf(t, p, AutoParams{RunID: testRunID, Hostname: testHostname})
	list, _ := heredocs(autoBlockOf(t, ar, "sources"))
	found := false
	for _, h := range list {
		if strings.Contains(h.body, `echo "STOP: x"`) {
			found = true
			if !strings.Contains(h.body, "# echo \"STOP: x\"; AUTOCLASS=guard; echo \"STOP: the hop is over\"\n# <<'CASAOS_EO'") || strings.Count(h.body, "AUTOCLASS") != 1 {
				t.Errorf("the lines of the tree were changed: %q", h.body)
			}
		}
	}
	if !found {
		t.Error("the tree's heredoc is not in block 3")
	}
}

// ---- the text ------------------------------------------------------------------------------------------------------------------------------

func TestTheAutoTextHasNoCarriageReturnAndEachBlockEndsWithANewlineAndStartsWithItsName(t *testing.T) {
	_, ar := realAuto(t)
	wantIDs := []string{"verify", "timers", "snapshot", "sources", "download", "minimal", "full", "prereboot", "after"}
	if len(ar.Blocks) != 9 || ar.RunID != testRunID || ar.PlanID != goldenPlanID {
		t.Fatalf("%d blocks, run %q, plan %q", len(ar.Blocks), ar.RunID, ar.PlanID)
	}
	for i, blk := range everyAutoBlock(ar) {
		id := osrun.BlockForward
		if i < 9 {
			id = wantIDs[i]
		}
		if blk.ID != id || blk.Title == "" {
			t.Errorf("block %d is %q (%q)", i, blk.ID, blk.Title)
		}
		if !strings.HasPrefix(blk.Text, "# casaos-os-upgrade "+id+" "+testRunID+"\nset -eu\n") || !strings.HasSuffix(blk.Text, "\n") || strings.HasSuffix(blk.Text, "\n\n") || strings.ContainsRune(blk.Text, '\r') {
			t.Errorf("block %s: the shape of its text: %q ... %q", id, blk.Text[:min(len(blk.Text), 80)], blk.Text[max(0, len(blk.Text)-40):])
		}
		for _, bad := range []string{blockStart, blockEnd, ">>> BLOCK", "<<< END BLOCK"} {
			if strings.Contains(blk.Text, bad) {
				t.Errorf("block %s has the marker %q of the owner's text", id, bad)
			}
		}
		if i < 9 && !strings.Contains(blk.Text, `: > "$BK/done-`+id+`"`) {
			t.Errorf("block %s does not leave its marker", id)
		}
	}
	if !slices.Equal(wantIDs, osrun.BlockIDs[:9]) || osrun.BlockIDs[9] != osrun.BlockForward {
		t.Errorf("the ids of the blocks are not the ones of osrun: %v", osrun.BlockIDs)
	}
}

// Nothing in the unit's text restarts the machine, or leaves a trace of its commands (block 3 holds the owner's sources, credentials included), or reads what a person types.
func TestTheAutoTextRestartsNothingAndTracesNothingAndReadsNoTerminal(t *testing.T) {
	_, ar := realAuto(t)
	banned := []string{"reboot", "shutdown", "poweroff", "halt", "telinit", "init", "kexec"}
	for _, blk := range everyAutoBlock(ar) {
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
			if m := regexp.MustCompile(`![^= ]`).FindString(l); m != "" {
				t.Errorf("block %s: %q in %q", blk.ID, m, l)
			}
		}
	}
}

// The words that the unit's text runs are the owner's, and three more: trap and osu_exit (the class of a stop), and sync (the marker of the point of no return).
func TestEveryCommandWordOfTheAutoTextIsOneOfTheOwnersOrTheUnits(t *testing.T) {
	_, ar := realAuto(t)
	known := map[string]string{"trap": "the class of a stop is written when the block ends", "osu_exit": "a function of the unit's prologue", "sync": "the marker of the point of no return survives a power cut"}
	for k, v := range knownCommandWords {
		known[k] = v
	}
	for _, blk := range everyAutoBlock(ar) {
		list, rest := heredocs(blk.Text)
		words := commandWords(rest)
		for _, h := range list {
			if strings.Contains(h.opener, ".sh\"") {
				words = append(words, commandWords(h.body)...)
			}
		}
		for _, w := range words {
			if _, ok := known[w]; !ok {
				t.Errorf("block %s runs %q, which is not a command of the owner's text or of the unit's", blk.ID, w)
			}
		}
	}
}

// The text says nothing of how it came to be: no review, no round, no commit.
func TestTheAutoTextSaysNothingOfHowItCameToBeAndRunsNothingThatRestarts(t *testing.T) {
	_, ar := realAuto(t)
	all := strings.ToLower(allAuto(ar))
	for _, bad := range []string{"review round", "round 1", "round 2", "round 3", "commit", "chroot", "no harm", "all checks passed", "units.state", "boot_id-full"} {
		if strings.Contains(all, bad) {
			t.Errorf("the text names %q", bad)
		}
	}
}

func TestEveryAutoBlockIsShellThatTheShellParses(t *testing.T) {
	sh := shellOrSkip(t)
	_, ar := realAuto(t)
	dir := t.TempDir()
	for _, blk := range everyAutoBlock(ar) {
		name := blk.ID + ".sh"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(blk.Text), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(sh, "-n", name)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("block %s: sh -n: %v\n%s", blk.ID, err, out)
		}
	}
}

func TestTheProtectedCaseIsInEveryBlockThatGuardsAndTheGuardIsCalledOnWhatItShould(t *testing.T) {
	_, ar := realAuto(t)
	want := "    case \"$base\" in\n      " + ShellCase() + "\n"
	for _, id := range []string{"minimal", "full", "forward"} {
		if !strings.Contains(autoBlockOf(t, ar, id), want) {
			t.Errorf("the case of block %s is not ShellCase()", id)
		}
	}
	if !strings.Contains(autoBlockOf(t, ar, "minimal"), "\nguard_removals \"$BK/after-minimal.txt\"\n") || !strings.Contains(autoBlockOf(t, ar, "full"), "\n    guard_removals \"$last\"\n") ||
		!strings.Contains(autoBlockOf(t, ar, "forward"), "\nguard_removals \"$recover\" \"the repair\"\n") {
		t.Error("a block does not call the guard on its simulation")
	}
}

// ---- the arguments and the refusals ----------------------------------------------------------------------------------------------------------

func TestBuildAutoRefusesWhatBuildRunbookRefusesAndWhatItCannotCarry(t *testing.T) {
	p := *realPlan(t)
	ok := AutoParams{RunID: testRunID, Hostname: testHostname}
	blocked := p
	blocked.Verdict = VerdictBlocked
	if _, err := BuildAuto(blocked, ok); !errors.Is(err, ErrNoRunbook) {
		t.Errorf("a blocked plan: %v", err)
	}
	other := p
	other.Hop = "debian-12-13"
	if _, err := BuildAuto(other, ok); !errors.Is(err, ErrNoRunbook) {
		t.Errorf("a hop with no runbook: %v", err)
	}
	unsafe := p
	unsafe.Sources.Tree = slices.Clone(p.Sources.Tree)
	unsafe.Sources.Tree[0].Content += heredocEnd + "\n"
	if _, err := BuildAuto(unsafe, ok); !errors.Is(err, ErrRunbookUnsafe) {
		t.Errorf("a line that ends a heredoc: %v", err)
	}
	cr := p
	cr.Sources.Tree = slices.Clone(p.Sources.Tree)
	cr.Sources.Tree[0].Content += "# a carriage return \r in a line\n"
	if _, err := BuildAuto(cr, ok); !errors.Is(err, ErrRunbookUnsafe) {
		t.Errorf("a carriage return in the tree: %v", err)
	}
	for _, a := range []AutoParams{
		{RunID: "", Hostname: testHostname},
		{RunID: "not-a-uuid", Hostname: testHostname},
		{RunID: strings.ToUpper(testRunID), Hostname: testHostname},
		{RunID: testRunID + "x", Hostname: testHostname},
		{RunID: testRunID, Hostname: ""},
		{RunID: testRunID, Hostname: "a b"},
		{RunID: testRunID, Hostname: "-a"},
		{RunID: testRunID, Hostname: "a-"},
		{RunID: testRunID, Hostname: "a'b"},
		{RunID: testRunID, Hostname: "a$(x)"},
		{RunID: testRunID, Hostname: "a\nb"},
		{RunID: testRunID, Hostname: strings.Repeat("a", 254)},
		{RunID: testRunID, Hostname: testHostname, CgroupFile: "a b"},
		{RunID: testRunID, Hostname: testHostname, CgroupFile: "'x'"},
		{RunID: testRunID, Hostname: testHostname, CgroupFile: "x;y"},
		{RunID: testRunID, Hostname: testHostname, CgroupFile: strings.Repeat("a", 201)},
	} {
		if _, err := BuildAuto(p, a); !errors.Is(err, ErrRunbookUnsafe) {
			t.Errorf("%+v: %v", a, err)
		}
	}
	for _, a := range []AutoParams{
		{RunID: testRunID, Hostname: "a"},
		{RunID: testRunID, Hostname: "box-1.example.org"},
		{RunID: testRunID, Hostname: strings.Repeat("a", 253)},
		{RunID: testRunID, Hostname: testHostname, CgroupFile: "./cgroup"},
	} {
		if _, err := BuildAuto(p, a); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
}

// The text is the same for the same arguments, and the only things in it that come from the arguments are the run id, the name of the machine and the file of cgroups.
func TestTheAutoTextIsMadeOfThePlanAndOfTheThreeArgumentsAndNothingElse(t *testing.T) {
	p := *realPlan(t)
	a1 := autoOf(t, p, AutoParams{RunID: testRunID, Hostname: "one"})
	a2 := autoOf(t, p, AutoParams{RunID: testRunID, Hostname: "one"})
	if allAuto(a1) != allAuto(a2) {
		t.Error("the text is not the same for the same arguments")
	}
	a3 := autoOf(t, p, AutoParams{RunID: "11111111-2222-4333-8444-555555555555", Hostname: "two", CgroupFile: "./cg"})
	swap := strings.NewReplacer(testRunID, "11111111-2222-4333-8444-555555555555", "'one'", "'two'", "/proc/self/cgroup", "'./cg'")
	if swap.Replace(allAuto(a1)) != allAuto(a3) {
		t.Errorf("something else than the run id, the name and the file of cgroups depends on the arguments:\n%s", firstDifference(swap.Replace(allAuto(a1)), allAuto(a3)))
	}
	if !strings.Contains(allAuto(a1), "/proc/self/cgroup") || strings.Contains(allAuto(a3), "/proc/self/cgroup") {
		t.Error("the file of cgroups is /proc/self/cgroup when none is given, and the one given when there is one")
	}
}

// ---- the tripwire of the second hop ------------------------------------------------------------------------------------------------------------

// hopRowsAreSafe says whether the table of hops may have this many rows: the blocks of a hop look at every backup folder of the machine (head() and otherPlans()), which is right
// for one hop, and wrong for two.
func hopRowsAreSafe(rows int, scoped bool) bool { return rows <= 1 || scoped }

func TestASecondHopNeedsTheBackupFoldersScopedByHop(t *testing.T) {
	if !hopRowsAreSafe(1, false) || hopRowsAreSafe(2, false) || !hopRowsAreSafe(2, true) || !hopRowsAreSafe(0, false) {
		t.Fatal("the rule of the tripwire is not the one that it says")
	}
	if !hopRowsAreSafe(len(runbookHops), backupFoldersScopedByHop) {
		t.Fatalf("runbookHops has %d rows: scope the backup folders by hop (head() and otherPlans() look at every /var/backups/casaos-os-upgrade-* folder) before adding a second hop row, and then say so in backupFoldersScopedByHop", len(runbookHops))
	}
}

func TestSanitizeLineIsTheSanitizerOfThePlan(t *testing.T) {
	for _, l := range []string{
		"Err:1 http://user:pw@deb.example.org/debian bullseye InRelease  404  Not Found [IP: 192.0.2.1 80]\x1b[2J",
		"  plain  ",
		strings.Repeat("x", 300),
		"",
	} {
		if SanitizeLine(l) != sanitizeLine(l) {
			t.Errorf("%q", l)
		}
	}
	if got := SanitizeLine("Get:1 https://u:p@host.example/debian/dists/bookworm/InRelease?x=1 [1 kB]"); got != "Get:1 https://host.example [1 kB]" {
		t.Errorf("the address of a source is not reduced to its host: %q", got)
	}
}

// A STOP line has the class of the longest key that begins it.
func TestTheLongestKeyThatBeginsAStopLineGivesItsClass(t *testing.T) {
	table := []stopClass{{"the pa", osrun.ClassBusy}, {"the package manager", osrun.ClassNetwork}, {"the package manager is at work: ", osrun.ClassDisk}}
	for after, want := range map[string]string{
		"the package manager is at work: wait": osrun.ClassDisk,
		"the package manager is stuck":         osrun.ClassNetwork,
		"the packages":                         osrun.ClassBusy,
	} {
		if class, _, ok := classOf(table, after); !ok || class != want {
			t.Errorf("%q: class %q (%v), want %q", after, class, ok, want)
		}
	}
	if _, _, ok := classOf(table, "something else"); ok {
		t.Error("a line that no key begins has a class")
	}
	// the order of the table does not matter
	rev := slices.Clone(table)
	slices.Reverse(rev)
	if class, _, _ := classOf(rev, "the package manager is at work: wait"); class != osrun.ClassDisk {
		t.Errorf("the table in another order: %q", class)
	}
}

// A block of the owner's text that is changed without the unit's text being told is refused when the unit's text is written: a hook that no longer goes off, a new STOP line that no
// class covers, a heredoc of another form. (The blocks of the hop are put back after each case.)
func TestBuildAutoRefusesABlockThatWasChangedWithoutTheUnitsTextBeingTold(t *testing.T) {
	p := *realPlan(t)
	hop := runbookHops["debian-11-12"]
	defer func() { runbookHops["debian-11-12"] = hop }()
	ok := AutoParams{RunID: testRunID, Hostname: testHostname}
	if _, err := BuildAuto(p, ok); err != nil {
		t.Fatal(err)
	}
	download := slices.IndexFunc(hop, func(b blockSpec) bool { return b.ID == "download" })
	change := func(f func(body func(c *rbCtx, prev string) string) func(c *rbCtx, prev string) string) {
		mod := slices.Clone(hop)
		mod[download].body = f(hop[download].body)
		runbookHops["debian-11-12"] = mod
	}
	// the block is written as the owner's: none of its hooks goes off
	change(func(body func(c *rbCtx, prev string) string) func(c *rbCtx, prev string) string {
		return func(c *rbCtx, prev string) string {
			saved := c.auto
			c.auto = nil
			defer func() { c.auto = saved }()
			return body(c, prev)
		}
	})
	if _, err := BuildAuto(p, ok); !errors.Is(err, ErrAutoDrift) || !strings.Contains(err.Error(), "download/H") {
		t.Errorf("a block whose hooks do not go off: %v", err)
	}
	// a new STOP line
	change(func(body func(c *rbCtx, prev string) string) func(c *rbCtx, prev string) string {
		return func(c *rbCtx, prev string) string {
			return body(c, prev) + "[ -e /x ] || { echo \"STOP: a line that nobody classified\"; exit 1; }\n"
		}
	})
	if _, err := BuildAuto(p, ok); !errors.Is(err, ErrAutoDrift) || !strings.Contains(err.Error(), "a line that nobody classified") {
		t.Errorf("a new STOP line: %v", err)
	}
	// a heredoc of another form
	change(func(body func(c *rbCtx, prev string) string) func(c *rbCtx, prev string) string {
		return func(c *rbCtx, prev string) string { return body(c, prev) + "cat > x <<EOF\nx\nEOF\n" }
	})
	if _, err := BuildAuto(p, ok); !errors.Is(err, ErrAutoDrift) {
		t.Errorf("a heredoc of another form: %v", err)
	}
	// the owner's text is not touched by any of it
	runbookHops["debian-11-12"] = hop
	if _, err := BuildAuto(p, ok); err != nil {
		t.Errorf("the blocks as they were: %v", err)
	}
}
