package osrun

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The text of the driver: held to a golden, to the closed sets of the vocabulary, and to the properties that the unit relies on. What the driver does when it runs is
// tested where the blocks that it runs are written (pkg/osplan, driver_*_test.go): there are shells there, and stub blocks, and the real ones.

var update = flag.Bool("update", false, "rewrite the golden files of testdata, which is a choice and never an accident")

func TestTheDriverIsTheGolden(t *testing.T) {
	path := filepath.Join("testdata", "driver.golden")
	if *update {
		if err := os.WriteFile(path, []byte(Driver), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s rewritten", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./pkg/osrun -run DriverIsTheGolden -update writes it)", err)
	}
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); Driver != w {
		g, l := strings.Split(Driver, "\n"), strings.Split(w, "\n")
		for i := 0; i < len(g) && i < len(l); i++ {
			if g[i] != l[i] {
				t.Fatalf("the driver is not the golden one (-update rewrites it, and the difference is read before it is kept), line %d:\n got  %s\n want %s", i+1, g[i], l[i])
			}
		}
		t.Fatalf("the driver is not the golden one: %d lines, want %d", len(g), len(l))
	}
}

// driverCode is the driver without its comment lines.
func driverCode() string {
	var out []string
	for _, line := range strings.Split(Driver, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func TestTheDriverIsPlainShellText(t *testing.T) {
	if strings.ContainsRune(Driver, '\r') || strings.ContainsRune(Driver, 0) || !strings.HasSuffix(Driver, "\n") || !strings.HasPrefix(Driver, "#!/bin/sh\n") {
		t.Error("the driver has a carriage return or a NUL, or no final newline, or does not start with its interpreter")
	}
	for i, line := range strings.Split(Driver, "\n") {
		for _, r := range line {
			if r > 0x7e || r < 0x20 && r != '\t' {
				t.Errorf("line %d has the character %q: the driver is ASCII", i+1, r)
				break
			}
		}
	}
	// the parse is cheap, and the shells that the unit may have as /bin/sh are dash (Debian, Ubuntu) and, as a fallback, bash in its sh mode
	file := filepath.ToSlash(filepath.Join(t.TempDir(), "driver.sh"))
	if err := os.WriteFile(file, []byte(Driver), 0o644); err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, sh := range [][]string{{"dash", "-n"}, {"bash", "--posix", "-n"}} {
		path, err := exec.LookPath(sh[0])
		if err != nil {
			continue
		}
		ran++
		if out, err := exec.Command(path, append(sh[1:], file)...).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse the driver: %v: %s", sh[0], err, out)
		}
	}
	if ran == 0 {
		t.Skip("neither dash nor bash on this machine: the parse of the driver is proved on Linux")
	}
}

func TestTheDriverKeepsWhatTheUnitRelaysOn(t *testing.T) {
	code := driverCode()
	lines := strings.Split(code, "\n")
	find := func(re string) []string { return regexp.MustCompile(re).FindAllString(code, -1) }
	t.Run("no trap: a stop kills the unit, and the core reads a lost unit from the files", func(t *testing.T) {
		if got := find(`(?m)(^|[;&|({\s])trap\s`); len(got) > 0 {
			t.Errorf("%q", got)
		}
	})
	t.Run("no tracing: block 3 holds the owner's sources, credentials included", func(t *testing.T) {
		if got := find(`set\s+-[a-zA-Z]*x|xtrace|\bsh\s+-x|bash\s+-x`); len(got) > 0 {
			t.Errorf("%q", got)
		}
	})
	t.Run("no pipeline: a reader that leaves early kills the writer with 141", func(t *testing.T) {
		caseLabel := regexp.MustCompile(`^\s*[a-z]+(\|[a-z]+)*\)`)
		for _, l := range lines {
			l = strings.ReplaceAll(l, "||", "  ")
			if !strings.Contains(l, "|") {
				continue
			}
			// a pipe has white space beside it in this text, and the bars of a case pattern have none
			if regexp.MustCompile(`\s\||\|\s`).MatchString(l) {
				t.Errorf("a pipe: %s", l)
			} else if !strings.Contains(l, "case ") && !caseLabel.MatchString(l) {
				t.Errorf("a bar that is in no case pattern: %s", l)
			}
		}
	})
	t.Run("nothing restarts the machine", func(t *testing.T) {
		// the file in which the last block of a refresh says whether the machine wants a restart is named for it, and is the one word that is allowed
		without := strings.ReplaceAll(code, "refresh-reboot", "")
		if got := regexp.MustCompile(`\b(reboot|shutdown|poweroff|halt|telinit|kexec)\b|init\s+[06]\b`).FindAllString(without, -1); len(got) > 0 {
			t.Errorf("%q", got)
		}
		for _, m := range regexp.MustCompile(`systemctl\s+(\S+)`).FindAllStringSubmatch(code, -1) {
			if m[1] != "start" {
				t.Errorf("systemctl %s", m[1])
			}
		}
	})
	t.Run("no job control", func(t *testing.T) {
		if got := find(`(^|\s)(wait|bg|fg|disown|nohup|setsid|coproc)(\s|$)|set\s+-[a-zA-Z]*m\b|[^&>]&[^&>0-9]|&$`); len(got) > 0 {
			t.Errorf("%q", got)
		}
	})
	t.Run("the exit status is 0, whatever happened: the markers say what", func(t *testing.T) {
		for _, m := range regexp.MustCompile(`\bexit\s+([^\s;}]+)`).FindAllStringSubmatch(code, -1) {
			if m[1] != "0" {
				t.Errorf("exit %s", m[1])
			}
		}
	})
	t.Run("the standard input of every child is /dev/null", func(t *testing.T) {
		// a child that read the standard input would take the list of the blocks that the loop is reading
		for _, l := range lines {
			if regexp.MustCompile(`(^|[^A-Za-z_])sh "`).MatchString(l) && !strings.Contains(l, "< /dev/null") {
				t.Errorf("a shell that may read the list: %s", l)
			}
		}
		if got := find(`unwind\s*(\|\||;|$)`); len(got) > 0 {
			t.Errorf("the unwind is called with the standard input of the loop: %q", got)
		}
		if got := strings.Count(code, "unwind < /dev/null"); got != 2 {
			t.Errorf("%d calls of the unwind with /dev/null, want 2 (after a stop, and as the unwind phase)", got)
		}
	})
	t.Run("the nonce is read once, unset, and goes to no child", func(t *testing.T) {
		if strings.Count(code, "CASAOS_OSU_NONCE") != 2 || !strings.Contains(code, "; unset CASAOS_OSU_NONCE\n") {
			t.Error("the nonce is not read once and unset at once")
		}
		for _, l := range lines {
			if strings.Contains(l, "export") && (strings.Contains(l, "NONCE") || regexp.MustCompile(`\bN\b`).MatchString(l)) {
				t.Errorf("the nonce is exported: %s", l)
			}
		}
		if got := find(`"\$N"|\$N\b|\$\{N\}`); len(got) != 1 {
			t.Errorf("the nonce is used in %d places, and only mark has it", len(got))
		}
	})
	t.Run("every marker is one printf, in one function", func(t *testing.T) {
		if got := strings.Count(code, `>> "$D/markers"`); got != 1 {
			t.Errorf("%d writes to the log of markers", got)
		}
		m := regexp.MustCompile(`(?m)^mark\(\) \{.*\}$`).FindString(code)
		if m == "" || strings.Count(m, "printf") != 1 || strings.Count(m, "markers") != 1 {
			t.Errorf("mark: %q", m)
		}
	})
	t.Run("sync runs before the minimal upgrade", func(t *testing.T) {
		if !strings.Contains(code, "  [ \"$id\" != minimal ] || sync\n") {
			t.Error("no sync before block minimal")
		}
	})
	t.Run("the owner's call to stop is read after the start of the block is written", func(t *testing.T) {
		start, flag := strings.Index(code, `mark BLOCK_START "$idx"`), strings.Index(code, `[ -e "$D/abort" ]`)
		if start < 0 || flag < 0 || flag < start {
			t.Errorf("the marker of the block is at %d and the flag is read at %d", start, flag)
		}
		if !strings.Contains(code, `[ -e "$D/abort" ] && ! pnr`) {
			t.Error("the flag is not ignored once the point of no return is behind")
		}
	})
}

func TestTheDriverHoldsNothingOfTheMachineButFourPathsAndTheFilesOfTheRun(t *testing.T) {
	// "Nothing of the machine is written into it": the absolute paths are the ones that the layout of the unit has, and the files of the folder of the run are named from it
	paths := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?:^|[\s"'=:(])(/[A-Za-z0-9_./*-]+)`).FindAllStringSubmatch(driverCode(), -1) {
		paths[m[1]] = true
	}
	want := []string{
		"/usr/sbin", "/usr/bin", "/sbin", "/bin", "/dev/null", "/nonexistent", "/proc/sys/kernel/random/boot_id",
		"/var/backups/casaos-os-upgrade-*", "/var/backups/casaos-os-upgrade-refresh-*", "/var/backups/casaos-os-upgrade-timers.state",
	}
	for p := range paths {
		if !slices.Contains(want, p) {
			t.Errorf("a path of the machine in the driver: %s", p)
		}
	}
	for _, p := range want[4:] {
		if !paths[p] {
			t.Errorf("the driver does not name %s", p)
		}
	}
}

// What the driver lets through is the vocabulary, as the closed sets: the case of the blocks that it runs, the phases, the kinds of marker that it writes, and the ids and
// classes of the stops that it makes by itself.
func TestTheClosedSetsOfTheDriverAreTheVocabulary(t *testing.T) {
	code := driverCode()
	alternatives := func(re string) []string {
		m := regexp.MustCompile(re).FindStringSubmatch(code)
		if m == nil {
			t.Fatalf("no %s in the driver", re)
		}
		return strings.Split(m[1], "|")
	}
	sameSet := func(name string, got, want []string) {
		t.Helper()
		g, w := slices.Clone(got), slices.Clone(want)
		slices.Sort(g)
		slices.Sort(w)
		if !slices.Equal(g, w) {
			t.Errorf("%s: the driver has %v, the vocabulary %v", name, got, want)
		}
	}
	sameSet("blocks", alternatives(`case "\$id" in ([a-z|]+)\) ;; \*\)`), BlockIDs)
	sameSet("phases", alternatives(`case "\$PH" in ([a-z|]+)\) ;; \*\) exit 0`), Phases)

	set := func(re string, group int) []string {
		var out []string
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(code, -1) {
			if !slices.Contains(out, m[group]) {
				out = append(out, m[group])
			}
		}
		return out
	}
	kinds := set(`\bmark ([A-Z_]+)\b`, 1)
	sameSet("the kinds that the driver writes", kinds, []string{
		MkUnitStart, MkBlockStart, MkBlockDone, MkBlockSkipped, MkStop, MkUnwound, MkRefreshDone, MkRehearsalReady, MkRebootPending, MkRebootNotNeeded, MkVerifyDone, MkUnitEnd,
	})
	for _, k := range kinds {
		if !slices.Contains(MarkerKinds, k) {
			t.Errorf("the driver writes %s, which is no kind of marker", k)
		}
	}
	// the stops that are not a block's: the id of the place (the driver's own check, a list it cannot read, the unwind) and the class
	ids := set(`\b(?:mark STOP|stopat) (?:0|"\$idx") (driver|list|unwind) [01] [a-z_]+`, 1)
	sameSet("the ids of a stop that is no block's", ids, []string{StopDriver, StopList, StopUnwind})
	classes := append(set(`\b(?:mark STOP|stopat) (?:0|"\$idx") (?:driver|list|unwind) [01] ([a-z_]+)`, 1), set(`stopat "\$idx" "\$id" 0 ([a-z_]+)`, 1)...)
	sameSet("the classes of a stop that the driver makes", classes, []string{ClassIntegrity, ClassGuard, ClassAbort})
	for _, c := range classes {
		if !ValidClass(c) {
			t.Errorf("the driver stops with the class %q, which is none", c)
		}
	}
	// what the core writes is the rest
	core := []string{MkQueued, MkRun, MkAttempt, MkStartFailed, MkRebootRequested, MkRebootFailed, MkTakeover, MkAck, MkBootSeen}
	for _, k := range MarkerKinds {
		if slices.Contains(kinds, k) == slices.Contains(core, k) {
			t.Errorf("the kind %s is written by both the core and the driver, or by neither", k)
		}
	}
}
