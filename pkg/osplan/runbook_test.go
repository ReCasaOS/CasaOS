package osplan

import (
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files of testdata, which is a choice and never an accident")

func realRunbook(t *testing.T) (*Plan, *Runbook) {
	t.Helper()
	p := realPlan(t)
	return p, buildFor(t, *p)
}

func TestTheRunbookOfTheRealBoxIsTheGolden(t *testing.T) {
	_, rb := realRunbook(t)
	path := filepath.Join("testdata", "runbook-d11-12.golden")
	if *update {
		if err := os.WriteFile(path, []byte(rb.Text), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s rewritten", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./pkg/osplan -run RunbookOfTheRealBox -update writes it)", err)
	}
	if got, want := rb.Text, strings.ReplaceAll(string(want), "\r\n", "\n"); got != want {
		t.Errorf("the runbook is not the golden one (-update rewrites it, and the difference is read before it is kept):\n%s", firstDifference(got, want))
	}
}

func firstDifference(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) && i < len(w); i++ {
		if g[i] != w[i] {
			return "line " + itoa(i+1) + ":\n got  " + g[i] + "\n want " + w[i]
		}
	}
	return "the lengths differ: " + itoa(len(g)) + " lines, want " + itoa(len(w))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// ---- helpers: the blocks and the heredocs of a text ------------------------------------------------------------------

// blocks are the code of each block of a runbook, between its markers.
func blocks(t *testing.T, text string) []string {
	t.Helper()
	var out []string
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], blockStart) {
			j := i + 1
			for j < len(lines) && !strings.HasPrefix(lines[j], blockEnd) {
				j++
			}
			if j == len(lines) {
				t.Fatalf("block at line %d has no end", i+1)
			}
			out = append(out, strings.Join(lines[i+1:j], "\n")+"\n")
			i = j
		}
	}
	return out
}

// heredocs are the quoted heredocs of a code: the line that opens it, and its body.
type heredoc struct {
	opener string
	body   string
}

func heredocs(code string) (list []heredoc, rest string) {
	lines := strings.Split(code, "\n")
	var outside []string
	for i := 0; i < len(lines); i++ {
		outside = append(outside, lines[i])
		if !strings.Contains(lines[i], "<<'"+heredocEnd+"'") {
			continue
		}
		j := i + 1
		var body []string
		for j < len(lines) && lines[j] != heredocEnd {
			body = append(body, lines[j])
			j++
		}
		list = append(list, heredoc{opener: lines[i], body: strings.Join(body, "\n")})
		outside = append(outside, "<heredoc>")
		i = j
	}
	return list, strings.Join(outside, "\n")
}

func between(t *testing.T, s, from, to string) string {
	t.Helper()
	i := strings.Index(s, from)
	if i < 0 {
		t.Fatalf("%q is not in the text", from)
	}
	j := strings.Index(s[i:], to)
	if j < 0 {
		t.Fatalf("%q is not after %q", to, from)
	}
	return s[i : i+j+len(to)]
}

func blockOf(t *testing.T, text, id string) string {
	t.Helper()
	for i, b := range blocks(t, text) {
		if runbookHops["debian-11-12"][i].ID == id {
			return b
		}
	}
	t.Fatalf("no block %s", id)
	return ""
}

func shellOrSkip(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("the tests that run the shell of the runbook are skipped under -short: they start a shell for each block, which is some 8 s on Linux and some 480 s on Windows with Git Bash (see the notes at the top of runbook_exec_test.go)")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine (Windows without Git Bash): the runbook's shell is proved on Linux")
	}
	return sh
}

// writeScript writes a text of shell to a file of its own, and says its path: a block of the runbook is more than the 8 KiB that a command line carries on Windows
// (the Git Bash of the machines that run these tests on Windows cuts its arguments there), so a block is run as a file, and not as the argument of sh -c.
func writeScript(t *testing.T, code string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.sh")
	if err := os.WriteFile(path, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(path)
}

// runSh runs a script in a directory and says what it printed and how it ended.
func runSh(t *testing.T, dir, script string) (string, int) {
	t.Helper()
	cmd := exec.Command(shellOrSkip(t), writeScript(t, script))
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		t.Fatalf("sh: %v", err)
	}
	return string(out), code
}

// ---- the structure ---------------------------------------------------------------------------------------------------

func TestTheRunbookHasTheNineBlocksOfTheHopInOrder(t *testing.T) {
	p, rb := realRunbook(t)
	wantIDs := []string{"verify", "timers", "snapshot", "sources", "download", "minimal", "full", "prereboot", "after"}
	var ids []string
	for _, b := range rb.Blocks {
		ids = append(ids, b.ID)
		if b.Title == "" {
			t.Errorf("block %s has no title", b.ID)
		}
		// only the block that begins the upgrade is the point of no return, and only the one before the restart asks for it
		if b.PointOfNoReturn != (b.ID == "minimal") || b.Reboot != (b.ID == "prereboot") {
			t.Errorf("block %s: %+v", b.ID, b)
		}
	}
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("blocks = %v", ids)
	}
	if rb.PlanID != p.PlanID || rb.Hop != "debian-11-12" {
		t.Errorf("plan id %q, hop %q", rb.PlanID, rb.Hop)
	}
	code := blocks(t, rb.Text)
	if len(code) != 9 {
		t.Fatalf("%d blocks in the text", len(code))
	}
	// each block is a subshell that starts with set -eu and English, and ends with its marker and its check
	for i, c := range code {
		if !strings.HasPrefix(c, "(\nset -eu\nLC_ALL=C; export LC_ALL\n") || !strings.HasSuffix(strings.TrimRight(c, "\n"), ")") {
			t.Errorf("block %d does not start with set -eu and LC_ALL=C in a subshell:\n%s", i, c[:min(len(c), 120)])
		}
		if !strings.Contains(c, `: > "$BK/done-`+wantIDs[i]+`"`) {
			t.Errorf("block %d does not leave its marker", i)
		}
		if i > 0 && !strings.Contains(c, `[ -e "$BK/done-`+wantIDs[i-1]+`" ] ||`) {
			t.Errorf("block %d does not check that the block before it ended", i)
		}
		if !strings.Contains(c, "STOP:") {
			t.Errorf("block %d has no STOP line", i)
		}
		if strings.Count(c, "echo \"Block ") == 0 && strings.Count(c, "echo \"BLOCK ") == 0 {
			t.Errorf("block %d does not say what it does", i)
		}
	}
	if !strings.Contains(rb.Text, "# BLOCK 5 (of the blocks 0 to 8): THE POINT OF NO RETURN") {
		t.Error("the title of the point of no return is not in capitals")
	}
	if !strings.Contains(rb.Text, "THIS BLOCK IS THE POINT OF NO RETURN.") || !strings.Contains(rb.Text, "AFTER THIS BLOCK, RESTART THE MACHINE YOURSELF.") {
		t.Error("the banners of the point of no return and of the restart are not there")
	}
}

func TestTheHeaderSaysWhatItIsForAndWhatItDoesNotPromise(t *testing.T) {
	p, rb := realRunbook(t)
	for _, want := range []string{
		p.PlanID, "written for exactly this machine and this plan", "ask for a new plan", "a snapshot", "console", "Tailscale",
		"runbook, not a script", "ONE BLOCK AT A TIME", "WHAT HAS BEEN TESTED, AND WHAT HAS NOT", "None of that was tried", "Nothing goes back to Debian 11",
		"STOP", "IF YOU STOPPED", "restore-sources.sh", "dpkg --configure -a --force-confdef --force-confold",
		"DEBIAN_FRONTEND=noninteractive apt-get -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -f install",
		"384 packages", "install 75 new ones", "remove 2 (fuse, libsemanage1)",
		// a terminal that survives, the state of the machine, what was measured and what was not
		"tmux new-session -s casaos-upgrade", "screen -S casaos-upgrade", "tmux attach -t casaos-upgrade", "Become root first", "Debian 11 fully updated and restarted once",
		"The apt COMMANDS of blocks 3 to 6 below, and their order", "the simulation that finds nothing left", "was not measured", "never on a machine that was upgraded with this text", "(mode 700)", "not older than its 24 hours",
		// the repair ladder and the catch-up passes were never run: the header says so
		"THE REPAIR LADDER AND THE CATCH-UP PASSES OF BLOCK 6 WERE NOT RUN IN THAT HOP", "Never run for real",
		// where a new plan is made, and who is asked for help
		"A NEW PLAN is made in the dashboard", "Make the plan again", "TO ASK FOR HELP is to ask whoever looks after this machine",
		// what must be done before block 3, and what a block that stops without a STOP line means
		"INSTALL tmux NOW, BEFORE block 3", "install nothing by hand", "A plain sh to paste into", "did not print 'Block N done.' did not go through, STOP line or not",
		// what was tried: the commands of the hop that was measured, and not the text as a whole (it was run on no machine that booted)
		"that was measured in a virtual machine", "THIS TEXT AS A WHOLE HAS NOT BEEN RUN, END TO END, ON A MACHINE THAT BOOTED", "They were tried against stand-in commands",
		// no machine identity: what the typed name is for, and what the readiness report is for
		"The plan holds no identity of the machine", "the name that you type guards the terminal", "not the identity of the machine",
		"THIS TEXT DOES NOT RUN THE READINESS REPORT", "do not start when it says that it refuses",
		// a paste that is changed on its way, tabs included
		"Type sh first too when a paste is changed on its way", "takes a TAB for completion",
		// the locks
		"wait up to 5 minutes for the dpkg lock, and apt-get update does not wait for the lock of the lists",
	} {
		if !strings.Contains(rb.Text, want) {
			t.Errorf("the header lacks %q", want)
		}
	}
	// no promise that cannot be kept
	low := strings.ToLower(rb.Text)
	// "chroot": the text was never run in one end to end, and it says nothing of the kind (the header says what was and was not tried)
	for _, word := range []string{"safe", "guarantee", "reversible", "risk-free", "no risk", "will not fail", "cannot fail", "nothing can go wrong", "foolproof", "chroot", "every apt-get"} {
		if strings.Contains(low, word) {
			t.Errorf("the text says %q", word)
		}
	}
	if len(rb.Text) > MaxRunbookBytes {
		t.Errorf("%d bytes", len(rb.Text))
	}
}

func TestTheTextIsAsciiExceptWhatTheBoxsOwnLinesContain(t *testing.T) {
	p := *realPlan(t)
	p.Sources.Tree = slices.Clone(p.Sources.Tree)
	p.Sources.Tree[0].Content += "# dépôt de l'équipe — ok\n"
	rb, err := BuildRunbook(p)
	if err != nil {
		t.Fatal(err)
	}
	inTree := false
	for _, c := range blocks(t, rb.Text) {
		list, rest := heredocs(c)
		for _, h := range list {
			if strings.ContainsAny(h.body, "éô—") {
				inTree = true
			}
		}
		for _, r := range rest {
			if r > 0x7e || r < 0x20 && r != '\n' && r != '\t' {
				t.Errorf("a character that is not ASCII outside a heredoc: %q", r)
			}
		}
	}
	if !inTree {
		t.Error("the non-ASCII line of the tree is not in a heredoc")
	}
	// and outside the blocks: the header, the banners, the table
	for _, r := range rb.Text[:strings.Index(rb.Text, blockStart)] {
		if r > 0x7e {
			t.Errorf("a character that is not ASCII in the header: %q", r)
		}
	}
	// the text is the plan's: the line is in it, as it was
	if !strings.Contains(rb.Text, "# dépôt de l'équipe — ok\n") {
		t.Error("the line of the tree was changed")
	}
}

func TestThereIsNoHistoryExpansionAndNoReadInTheCode(t *testing.T) {
	// pasted into a console, a "!" is read by the shell's history before the shell reads the line, and a read takes the
	// lines that are pasted after it: the code has neither
	_, rb := realRunbook(t)
	for i, c := range blocks(t, rb.Text) {
		_, rest := heredocs(c)
		for _, line := range strings.Split(rest, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if m := regexp.MustCompile(`![^= ]`).FindString(line); m != "" {
				t.Errorf("block %d: %q in %q", i, m, line)
			}
			if regexp.MustCompile(`(^|[;&| ])read( |$)`).MatchString(strings.ReplaceAll(line, "while read -r unit state", "")) {
				t.Errorf("block %d reads the terminal: %q", i, line)
			}
		}
	}
}

// ---- the shell: syntax -------------------------------------------------------------------------------------------------

func TestEveryBlockIsShellThatTheShellParses(t *testing.T) {
	sh := shellOrSkip(t)
	_, rb := realRunbook(t)
	dir := t.TempDir()
	for i, c := range blocks(t, rb.Text) {
		path := filepath.Join(dir, "block"+itoa(i)+".sh")
		if err := os.WriteFile(path, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(sh, "-n", "block"+itoa(i)+".sh")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("block %d: sh -n: %v\n%s", i, err, out)
		}
	}
	whole := filepath.Join(dir, "all.sh")
	if err := os.WriteFile(whole, []byte(rb.Text), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, "-n", "all.sh")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("the whole text: sh -n: %v\n%s", err, out)
	}
}

func TestTheFileIsARunbookAndNotAScript(t *testing.T) {
	_, rb := realRunbook(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "all.sh"), []byte(rb.Text), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runSh(t, dir, "sh ./all.sh")
	if code == 0 || !strings.Contains(out, "runbook, not a script") {
		t.Errorf("run as a script: exit %d, %q", code, out)
	}
	// and nothing of the box was touched by it
	if strings.Contains(out, "Block ") {
		t.Errorf("a block ran: %q", out)
	}
}

// ---- the heredocs, the guard, the words ------------------------------------------------------------------------------

func TestTheHeredocsOfTheTreeAreThePlansTreeByteForByte(t *testing.T) {
	p, rb := realRunbook(t)
	list, _ := heredocs(blockOf(t, rb.Text, "sources"))
	got := map[string]string{}
	for _, h := range list {
		m := regexp.MustCompile(`^cat > "\$APT_ETC/([^"]+)\.casaos-new" <<'CASAOS_EOF'$`).FindStringSubmatch(h.opener)
		if m != nil {
			got[m[1]] = h.body
		}
	}
	for _, f := range p.Sources.Tree {
		name := strings.TrimPrefix(f.Path, "parts/")
		if f.Path != "sources.list" {
			name = "sources.list.d/" + name
		}
		body, ok := got[name]
		want := strings.TrimSuffix(f.Content, "\n")
		if !ok || body != want {
			t.Errorf("%s: heredoc %q, tree %q", f.Path, body, want)
		}
	}
	if len(got) != len(p.Sources.Tree) {
		t.Errorf("%d heredocs for %d files", len(got), len(p.Sources.Tree))
	}
}

func TestTheProtectedCaseOfTheRunbookIsShellCase(t *testing.T) {
	_, rb := realRunbook(t)
	// the guard is a function that blocks 5 and 6 each define, and block 5 calls it on the simulation after the minimal upgrade, and block 6 on each catch-up pass
	want := "    case \"$base\" in\n      " + ShellCase() + "\n"
	for _, id := range []string{"minimal", "full"} {
		if code := blockOf(t, rb.Text, id); !strings.Contains(code, want) {
			t.Errorf("the case of block %s is not ShellCase():\n%s", id, code)
		}
	}
	if !strings.Contains(blockOf(t, rb.Text, "minimal"), "\nguard_removals \"$BK/after-minimal.txt\"\n") || !strings.Contains(blockOf(t, rb.Text, "full"), "\n    guard_removals \"$last\"\n") {
		t.Error("a block does not call the guard on its simulation")
	}
}

// The marker of the point of no return is written after the confirmation and the timers, and before the first command that changes a package; block 2, block 3 and the
// restore script all look for it, and for the end of block 5, and say that the way is forward.
func TestTheMarkerOfThePointOfNoReturnIsWrittenBeforeTheUpgradeAndLookedForWhereTheWayBackIsOffered(t *testing.T) {
	_, rb := realRunbook(t)
	code := blockOf(t, rb.Text, "minimal")
	confirm := strings.Index(code, "[ \"$CONFIRM\" = ")
	timers := strings.Index(code, "systemctl stop")
	marker := strings.Index(code, ": > \"$BK/begun-upgrade\"\n")
	upgrade := strings.Index(code, "\nif ! aptq upgrade; then\n")
	if confirm < 0 || timers < confirm || marker < timers || upgrade < marker {
		t.Errorf("the order of the confirmation (%d), the timers (%d), the marker (%d) and the upgrade (%d)", confirm, timers, marker, upgrade)
	}
	for _, id := range []string{"snapshot", "sources"} {
		c := blockOf(t, rb.Text, id)
		if !strings.Contains(c, `if [ -e "$BK/begun-upgrade" ] || [ -e "$BK/done-minimal" ]; then echo "STOP: the upgrade has begun (block 5 was started)`) || !strings.Contains(c, "Do not restore anything. Only forward: "+forwardWords) {
			t.Errorf("block %s does not stop once the upgrade has begun", id)
		}
	}
	if !strings.Contains(restoreScript, `if [ -e "$HERE/begun-upgrade" ] || [ -e "$HERE/done-minimal" ]; then echo "STOP: the upgrade has begun (block 5 was started)`) || !strings.Contains(restoreScript, "Do not restore anything. Only forward: "+forwardWords) {
		t.Error("the restore script does not stop once the upgrade has begun")
	}
	// and nowhere else is a restore offered after it: the stops of blocks 5 and 6, and of what comes after, do not name the restore script (block 5's own stop, before
	// the marker, and block 6's when there is no tmux and the upgrade has not begun, are the places that may)
	for _, id := range []string{"full", "prereboot", "after"} {
		c := blockOf(t, rb.Text, id)
		for _, line := range strings.Split(c, "\n") {
			if strings.Contains(line, "restore-sources.sh") && !strings.Contains(line, `how="Neither tmux nor screen is installed, and the sources are Debian 12's`) {
				t.Errorf("block %s offers the restore script: %q", id, line)
			}
		}
	}
}

// the commands of the measured hop (hop_unit_debian of the measurement's script, and the helpers it uses), the checks and the
// copies that read the machine or write in the backup directory, and the shell's own words. A word that is not here fails the
// test until the list is edited on purpose: the runbook runs nothing the hop did not, but the stops and the human steps.
var knownCommandWords = map[string]string{
	// the measured hop
	"apt-get": "measured", "aptq": "measured: apt-get with the options of the measured hop, but its Dpkg::Use-Pty=0 (the blocks leave apt's pty on so that term.log is written: not run in the measured hop)", "dpkg": "measured", "dpkg-query": "measured",
	"cp": "measured", "mv": "measured", "mkdir": "measured", "rm": "measured", "cat": "measured", "grep": "measured", "awk": "measured",
	"sort": "measured", "find": "measured", "ss": "measured", "systemctl": "measured", "docker": "measured", "df": "measured",
	"uname": "measured", "sha256sum": "measured", "printf": "measured", "ls": "measured", "sshd": "measured", "id": "measured", "command": "measured",
	// not measured: the measured hop never asked Docker's repository anything, and block 8 does (the declared difference at the head of runbook.go), when a source of Docker's is put aside
	"curl": "added by block 8 (and looked for by block 0): Docker's InRelease for the new release, a network call that the measured hop never made",
	// read the machine, or write in the backup directory (design 5, step 2), for the checks
	"tar": "the copy of /etc into the backup directory", "wc": "the size of a file, for the hash of the sources", "hostname": "the name that is typed",
	"ps": "is a package manager running", "cd": "the hash of the sources, in a subshell",
	// the stops, the human steps and the shell
	"echo": "stop", "exit": "stop", "[": "test", ":": "marker", "set": "shell", "export": "shell", "break": "shell", "continue": "shell", "read": "the units' states, from a file",
	"removed_of": "a function of the runbook", "srchash": "a function of the runbook", "avail": "a function of the runbook", "true": "shell", "sh": "the restore script is run with sh",
	// the checks and the private directory: they read the machine, or write in the backup directory
	"guard_removals": "a function of the runbook: the protected, essential and required packages that a simulation would remove",
	"removes_of":     "a function of the runbook: the summary of a simulation", "notup_of": "a function of the runbook: the summary of a simulation", "gh": "a function of the runbook: the hash of a written file",
	"isinst": "a function of the runbook: is a package installed", "return": "shell",
	"chmod": "the backup directory is private to root", "umask": "the archive of /etc is private to root, and the files of /etc/apt are 644", "date": "the plan is good for 24 hours",
	"tr":  "what ps said, cut to printable characters, for the stop that says what it saw",
	"tty": "is the terminal a console", "readlink": "does resolv.conf point at systemd-resolved", "update-alternatives": "the iptables alternative, before and after",
	"lsinitramfs": "the initrd can be read", "comm": "the ports that listened before and after, the units that fail, the files that dpkg left",
	// the readiness report's refusals that bite after the point of no return (room in /boot, the disk of grub-pc), what says that dpkg did nothing, the modules built from source
	"debconf-communicate": "the disk that grub-pc writes to (the question grub-pc/install_devices)", "dkms": "a warning when modules are built from source",
	"cksum": "the checksum of dpkg's status file, before and after apt-get upgrade", "dpkgstate": "a function of the runbook: what says whether dpkg did anything",
	// the state of the timers is put in place all at once: ln fails when the name is taken, and the second of two runs adopts the first one's file
	"ln": "publishes the state of the timers under its name, and fails when another run has done it", "whereto": "a function of the runbook: where the owner goes from a stop of block 4",
}

func TestEveryCommandWordOfTheRunbookIsOneOfTheMeasuredHopOrAStop(t *testing.T) {
	_, rb := realRunbook(t)
	seen := map[string]bool{}
	for i, c := range blocks(t, rb.Text) {
		list, rest := heredocs(c)
		words := commandWords(rest)
		// the scripts that the block writes are code too
		for _, h := range list {
			if strings.Contains(h.opener, ".sh\"") {
				words = append(words, commandWords(h.body)...)
			}
		}
		for _, w := range words {
			seen[w] = true
			if _, ok := knownCommandWords[w]; !ok {
				t.Errorf("block %d runs %q, which is not a command of the measured hop or a stop", i, w)
			}
		}
	}
	// the measured hop's own steps are all there
	for _, w := range []string{"apt-get", "aptq", "dpkg", "dpkg-query", "systemctl", "docker", "tar", "sha256sum", "echo", "exit"} {
		if !seen[w] {
			t.Errorf("%q is never run", w)
		}
	}
	// the lexer itself: what it takes for commands
	got := commandWords("if ! aptq dist-upgrade; then\n  FOO=1 BAR=2 dpkg --configure -a > /dev/null 2>&1 || true\nfi\nfor x in a b; do echo \"$(cat f; hostname)\"; done\ncase \"$n\" in a|b) rm -f x ;; *) ;; esac\n" +
		"x=\"$(\n( cd /y\n  ls | wc -c ) | sha256sum )\"\naptq() { apt-get -y \"$@\"; }\n[ -f \"$f\" ] || continue\nwhile read -r u s; do :; done < f\n")
	want := []string{"aptq", "dpkg", "true", "cat", "hostname", "echo", "rm", "cd", "ls", "wc", "sha256sum", "aptq", "apt-get", "[", "continue", "read", ":"}
	if !slices.Equal(got, want) {
		t.Errorf("the lexer says\n %q\nwant\n %q", got, want)
	}
}

// commandWords are the commands that a text of shell runs: the first word of each simple command, outside quotes, in the
// substitutions too, and not the words of a "for", the patterns of a "case", the data of a heredoc, or comments.
func commandWords(code string) []string {
	var words []string
	lexShell(code, &words)
	return words
}

type caseState struct{ pattern bool }

func lexShell(code string, words *[]string) {
	var stack []caseState
	var buf strings.Builder
	flush := func() {
		seg := strings.TrimSpace(buf.String())
		buf.Reset()
		if seg == "" {
			return
		}
		if w := firstCommand(seg); w != "" {
			*words = append(*words, w)
		}
	}
	atWordEnd := func() bool {
		s := strings.TrimSpace(buf.String())
		return strings.HasPrefix(s, "case ") && strings.HasSuffix(s, " in") && strings.Count(s, "\"")%2 == 0
	}
	i := 0
	for i < len(code) {
		c := code[i]
		inPattern := len(stack) > 0 && stack[len(stack)-1].pattern
		switch {
		case c == '#' && strings.TrimSpace(buf.String()) == "" && !inPattern:
			for i < len(code) && code[i] != '\n' {
				i++
			}
			continue
		case c == '\\' && i+1 < len(code):
			buf.WriteString("xx")
			i += 2
			continue
		case c == '\'':
			j := strings.IndexByte(code[i+1:], '\'')
			if j < 0 {
				j = len(code) - i - 2
			}
			buf.WriteString("'" + strings.Repeat("x", j) + "'")
			i += j + 2
			continue
		case c == '"':
			// a double-quoted string: substitutions inside it are commands
			buf.WriteByte('"')
			i++
			for i < len(code) && code[i] != '"' {
				switch {
				case code[i] == '\\' && i+1 < len(code):
					buf.WriteString("xx")
					i += 2
				case strings.HasPrefix(code[i:], "$(("):
					end := matching(code, i+2)
					buf.WriteString("x")
					i = end + 1
				case strings.HasPrefix(code[i:], "$("):
					end := matching(code, i+1)
					lexShell(code[i+2:end], words)
					buf.WriteString("x")
					i = end + 1
				case strings.HasPrefix(code[i:], "${"):
					end := strings.IndexByte(code[i:], '}')
					buf.WriteString("x")
					i += end + 1
				default:
					buf.WriteByte('x')
					i++
				}
			}
			buf.WriteByte('"')
			i++
			continue
		case strings.HasPrefix(code[i:], "$(("):
			end := matching(code, i+2)
			buf.WriteString("x")
			i = end + 1
			continue
		case strings.HasPrefix(code[i:], "$("):
			end := matching(code, i+1)
			lexShell(code[i+2:end], words)
			buf.WriteString("x")
			i = end + 1
			continue
		case strings.HasPrefix(code[i:], "${"):
			end := strings.IndexByte(code[i:], '}')
			buf.WriteString("x")
			i += end + 1
			continue
		}
		if inPattern {
			switch {
			case c == ')':
				buf.Reset()
				stack[len(stack)-1].pattern = false
			case c == '\n' || c == ';':
				if strings.TrimSpace(buf.String()) == "esac" {
					stack = stack[:len(stack)-1]
				}
				buf.Reset()
			default:
				buf.WriteByte(c)
			}
			i++
			continue
		}
		switch {
		case c == ' ' || c == '\t':
			buf.WriteByte(c)
			if atWordEnd() {
				buf.Reset()
				stack = append(stack, caseState{pattern: true})
			}
		case c == '\n':
			if atWordEnd() {
				buf.Reset()
				stack = append(stack, caseState{pattern: true})
			} else {
				flush()
			}
		case c == ';' && strings.HasPrefix(code[i:], ";;"):
			flush()
			if len(stack) > 0 {
				stack[len(stack)-1].pattern = true
			}
			i++
		case c == ';', c == '(', c == ')', c == '{', c == '}':
			flush()
		case c == '|':
			flush()
			if strings.HasPrefix(code[i:], "||") {
				i++
			}
		case c == '&':
			prev := byte(' ')
			if i > 0 {
				prev = code[i-1]
			}
			if prev == '>' || strings.HasPrefix(code[i:], "&>") {
				buf.WriteByte(c)
			} else {
				flush()
				if strings.HasPrefix(code[i:], "&&") {
					i++
				}
			}
		default:
			buf.WriteByte(c)
		}
		i++
	}
	flush()
}

// matching is the index of the ")" that closes the "(" at open, quotes taken into account.
func matching(code string, open int) int {
	depth := 0
	for i := open; i < len(code); i++ {
		switch code[i] {
		case '\'':
			if j := strings.IndexByte(code[i+1:], '\''); j >= 0 {
				i += j + 1
			}
		case '"':
			i++
			for i < len(code) && code[i] != '"' {
				if code[i] == '\\' {
					i++
				} else if strings.HasPrefix(code[i:], "$(") {
					i = matching(code, i+1)
				}
				i++
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(code) - 1
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// firstCommand is the command word of a simple command: after the keywords, the assignments and the redirections.
func firstCommand(seg string) string {
	fields := strings.Fields(seg)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch {
		case f == "!" || f == "if" || f == "then" || f == "else" || f == "elif" || f == "while" || f == "until" || f == "do":
		case f == "fi" || f == "done" || f == "esac" || f == "for" || f == "case" || f == "in" || f == "}":
			return ""
		case assignment.MatchString(f):
		case strings.HasPrefix(f, ">") || strings.HasPrefix(f, "<") || regexp.MustCompile(`^[0-9]+[<>]`).MatchString(f):
			if regexp.MustCompile(`^[0-9]*[<>]+$`).MatchString(f) {
				i++
			}
		default:
			return strings.Trim(f, "\"'")
		}
	}
	return ""
}

// ---- the shell, run ----------------------------------------------------------------------------------------------------

func TestTheTreeIsWrittenAsItIsWhateverTheBoxsLinesSay(t *testing.T) {
	hostile := "# $(touch pwned) and `touch pwned2` and \"quotes\" and 'single' and \\ and $HOME and ${x} and !bang and * and $((1+1))\n" +
		"deb http://deb.debian.org/debian bookworm main # $(touch pwned3)\n" +
		"#CASAOS\n" +
		"  # indented, with trailing spaces   \n" +
		"\n" +
		"# an end-of-heredoc look-alike: CASAOS_EO\n"
	p := *realPlan(t)
	p.Sources.Tree = []TreeFile{{Path: "parts/a.list", Content: hostile}, {Path: "parts/empty.list", Content: ""}, {Path: "sources.list", Content: "\n\n"}}
	c := &rbCtx{plan: p, hop: bullseyeToBookworm}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "etc", "sources.list.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "etc", "sources.list.d", "docker.list"), []byte("docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runSh(t, dir, "set -eu\nAPT_ETC=./etc\n"+treeWrite(c)+treeMoves(c))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	for name, want := range map[string]string{"etc/sources.list.d/a.list": hostile, "etc/sources.list.d/empty.list": "", "etc/sources.list": "\n\n", "etc/sources.list.d/docker.list.casaos-paused": "docker\n"} {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q (%v), want %q", name, got, err, want)
		}
	}
	for _, name := range []string{"pwned", "pwned2", "pwned3", "etc/pwned", "etc/sources.list.d/pwned"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s was made: a line of the file was executed", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "etc", "sources.list.d", "docker.list")); err == nil {
		t.Error("the paused file is still in place")
	}
}

func TestTheShellHashOfTheSourcesIsTheOneOfTheCode(t *testing.T) {
	p, rb := realRunbook(t)
	_ = p
	fragment := between(t, blockOf(t, rb.Text, "verify"), "srchash() {", "\n}\n") + "\nnow=\"$(srchash)\"\n"
	dir := t.TempDir()
	files := d11Box(t)
	// names that apt does not read, and that the hash leaves out
	extra := map[string]string{"sources.list.d/docker.list.save": "x", "sources.list.d/.hidden.list": "x", "sources.list.d/my file.list": "x", "sources.list.d/note.txt": "x"}
	if err := os.MkdirAll(filepath.Join(dir, "etc-apt", "sources.list.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := f.Name
		if f.InParts {
			name = "sources.list.d/" + name
		}
		if err := os.WriteFile(filepath.Join(dir, "etc-apt", filepath.FromSlash(name)), f.Content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, c := range extra {
		if err := os.WriteFile(filepath.Join(dir, "etc-apt", filepath.FromSlash(name)), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := "set -eu\nLC_ALL=C; export LC_ALL\n" + strings.Replace(fragment, "cd /etc/apt", "cd ./etc-apt", 1) + "echo \"$now\"\n"
	out, code := runSh(t, dir, script)
	got := strings.TrimSpace(out)
	want := mustRewrite(t, orderLikeTheBox(d11Box(t))...).HashCurrent
	if code != 0 || got != want {
		t.Errorf("shell: %q (exit %d)\n  Go: %s", got, code, want)
	}
	// a byte of a file changes it
	if err := os.WriteFile(filepath.Join(dir, "etc-apt", "sources.list"), append(files[1].Content, '#'), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ = runSh(t, dir, script)
	if strings.TrimSpace(out) == want {
		t.Error("a changed file has the same hash")
	}
}

func resimOf(removed ...string) string {
	text := "Reading package lists...\nBuilding dependency tree...\nReading state information...\nCalculating upgrade...\n"
	if len(removed) > 0 {
		text += "The following packages will be REMOVED:\n  " + strings.Join(removed, " ") + "\n"
	}
	text += "The following packages will be upgraded:\n  bash\n"
	return text + "1 upgraded, 0 newly installed, " + itoa(len(removed)) + " to remove and 0 not upgraded.\n"
}

func TestBlock3StopsWhenAptWouldRemoveAnotherSetOfPackages(t *testing.T) {
	_, rb := realRunbook(t)
	code := blockOf(t, rb.Text, "sources")
	def := shSim
	// from the first check of the simulation to the end of the comparison of the names, where the block writes its marker
	from := strings.Index(code, "[ -n \"$(removes_of \"$BK/resim.txt\")\" ]")
	to := strings.Index(code, ": > \"$BK/done-sources\"")
	if from < 0 || to < from {
		t.Fatalf("the checks of the simulation are not where they were: %d %d", from, to)
	}
	fragment := code[from:to]
	undoDef := between(t, code, "undo=\"", "\"\n") + between(t, code, "changed=\"", "\"\n")
	dir := t.TempDir()
	for _, tc := range []struct {
		name     string
		expected string
		resim    string
		stops    bool
	}{
		{"the same", "fuse\nlibsemanage1\n", resimOf("fuse", "libsemanage1"), false},
		{"the same, a star on one", "fuse\nlibsemanage1\n", resimOf("fuse*", "libsemanage1"), false},
		{"none and none", "", resimOf(), false},
		{"one more", "fuse\nlibsemanage1\n", resimOf("fuse", "libsemanage1", "cron"), true},
		{"one less", "fuse\nlibsemanage1\n", resimOf("fuse"), true},
		{"another", "fuse\nlibsemanage1\n", resimOf("fuse", "libsemanage2"), true},
		{"a removal where the plan has none", "", resimOf("curl"), true},
		{"none where the plan has some", "fuse\n", resimOf(), true},
	} {
		sub := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-"))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(sub, "resim.txt"), []byte(tc.resim), 0o644)
		os.WriteFile(filepath.Join(sub, "expected-removed.txt"), []byte(tc.expected), 0o644)
		out, exit := runSh(t, sub, "(\nset -eu\nLC_ALL=C; export LC_ALL\nBK=.\n"+def+shNotup+undoDef+fragment+"echo REACHED\n)")
		switch {
		case tc.stops && (exit == 0 || !strings.Contains(out, "STOP:") || strings.Contains(out, "REACHED") || !strings.Contains(out, "restore-sources.sh")):
			t.Errorf("%s: exit %d, %q", tc.name, exit, out)
		case !tc.stops && (exit != 0 || !strings.Contains(out, "REACHED") || strings.Contains(out, "STOP:")):
			t.Errorf("%s: exit %d, %q", tc.name, exit, out)
		}
	}
}

// The two counts of the summary are read from the end of the line: apt puts "N downgraded," in the middle when there are downgrades.
func TestTheSummaryIsReadFromTheEndOfTheLineWhateverIsInTheMiddle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.txt"), []byte("1 upgraded, 0 newly installed, 2 downgraded, 3 to remove and 4 not upgraded.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := runSh(t, dir, "set -eu\n"+shSim+shNotup+"removes_of s.txt; notup_of s.txt\n"); code != 0 || out != "3\n4\n" {
		t.Errorf("exit %d, %q", code, out)
	}
}

func TestBlock5StopsOnAProtectedOrAnEssentialRemoval(t *testing.T) {
	_, rb := realRunbook(t)
	code := blockOf(t, rb.Text, "minimal")
	def := between(t, code, "removed_of() {", "\n") + between(t, code, "guard_removals() {", "\n}\n")
	fragment := "guard_removals after-minimal.txt\n"
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// dpkg-query as it answers: the essential and the required packages say so
	fake := "#!/bin/sh\nfor last; do :; done\ncase \"$last\" in\n  an-essential) echo 'yes required';;\n  a-required) echo ' required';;\n  *) echo ' optional';;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "bin", "dpkg-query"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		names []string
		stops bool
	}{
		{"fuse and libsemanage1", []string{"fuse", "libsemanage1"}, false},
		{"nothing", nil, false},
		{"Docker", []string{"fuse", "docker-ce"}, true},
		{"a plugin of Docker", []string{"docker-buildx-plugin"}, true},
		{"libc6", []string{"libc6"}, true},
		{"libc6 of another architecture", []string{"libc6:i386"}, true},
		{"samba", []string{"samba-common-bin"}, true},
		{"the kernel", []string{"linux-image-5.10.0-35-cloud-amd64"}, true},
		{"an essential package", []string{"an-essential"}, true},
		{"a required package", []string{"a-required"}, true},
		{"what is near a pattern but not one", []string{"apt-utils", "libc6-dev", "dbus-x11", "git-man"}, false},
	} {
		sub := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-"))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(sub, "after-minimal.txt"), []byte(resimOf(tc.names...)), 0o644)
		script := "(\nset -eu\nLC_ALL=C; export LC_ALL\nPATH=\"" + "$(cd .. && pwd)/bin:$PATH\"; export PATH\nBK=.\n" + def + "\n" + fragment + "echo REACHED\n)"
		out, exit := runSh(t, sub, script)
		switch {
		case tc.stops && (exit == 0 || !strings.Contains(out, "STOP:") || strings.Contains(out, "REACHED") || !strings.Contains(out, "DO NOT UNDO ANYTHING")):
			t.Errorf("%s: exit %d, %q", tc.name, exit, out)
		case !tc.stops && (exit != 0 || !strings.Contains(out, "REACHED")):
			t.Errorf("%s: exit %d, %q", tc.name, exit, out)
		}
	}
}

func TestTheRestoreScriptPutsTheSourcesBackAsTheyWere(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// what the box had (and the copy that block 3 made of it)
	original := map[string]string{
		"sources.list":                   "deb http://deb.debian.org/debian bullseye main\n",
		"sources.list.d/docker.list":     "deb https://download.docker.com/linux/debian bullseye stable\n",
		"sources.list.d/extra.list.save": "an old copy\n",
		"apt.conf.d/99proxy":             "Acquire::Retries \"5\";\n",
	}
	for name, c := range original {
		write("etc/"+name, c)
		write("bk/apt-orig/"+name, c)
	}
	write("bk/restore-sources.sh", restoreScript)
	// the markers of the blocks that ended, and the copy of /etc/apt that block 3 made (what it is the copy of: the files above)
	write("bk/done-snapshot", "")
	write("bk/done-sources", "")
	write("bk/done-download", "")
	// what block 3 left: new sources, a paused file, a new file under a new name
	write("etc/sources.list", "deb http://deb.debian.org/debian bookworm main\n")
	write("etc/sources.list.d/mine.list", "deb http://deb.debian.org/debian bookworm-updates main\n")
	write("etc/sources.list.d/docker.list.casaos-paused", "x")
	os.Remove(filepath.Join(dir, "etc", "sources.list.d", "docker.list"))
	write("etc/sources.list.d/mine.list.casaos-new", "x")
	write("etc/sources.list.casaos-new", "x")
	out, code := runSh(t, dir, "APT_ETC=./etc CASAOS_NO_UPDATE=1 sh ./bk/restore-sources.sh")
	if code != 0 || !strings.Contains(out, "The sources are back") || !strings.Contains(out, "Block 3 can be run again") {
		t.Fatalf("exit %d: %s", code, out)
	}
	// blocks 3 and 4 are forgotten, so that block 3 can be run again (and block 4 cannot be run before it); block 2 is not
	for _, gone := range []string{"bk/done-sources", "bk/done-download", "bk/apt-orig"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(gone))); err == nil {
			t.Errorf("%s is still there after the restore", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "bk", "done-snapshot")); err != nil {
		t.Error("the restore forgot block 2 too")
	}
	if aside, _ := filepath.Glob(filepath.Join(dir, "bk", "apt-orig.restored.*")); len(aside) != 1 {
		t.Errorf("the copy of /etc/apt is not put aside: %v", aside)
	}
	for name, c := range original {
		got, err := os.ReadFile(filepath.Join(dir, "etc", filepath.FromSlash(name)))
		if err != nil || string(got) != c {
			t.Errorf("%s = %q (%v), want %q", name, got, err, c)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "etc", "sources.list.d"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"docker.list", "extra.list.save"}) {
		t.Errorf("sources.list.d = %v", names)
	}
	if _, err := os.Stat(filepath.Join(dir, "etc", "sources.list.casaos-new")); err == nil {
		t.Error("a new file is left")
	}
	// without the copy, it says so and changes nothing
	out, code = runSh(t, dir, "APT_ETC=./etc CASAOS_NO_UPDATE=1 sh ./bk/restore-sources.sh")
	if code == 0 || !strings.Contains(out, "STOP:") {
		t.Errorf("without the copy: exit %d, %q", code, out)
	}
}

// Once block 5 has written begun-upgrade (or has ended) the restore script puts nothing back: it would leave Debian 12 on the sources of Debian 11, and block 3 would
// refuse to run again. It says so in words that go forward, and changes nothing.
func TestTheRestoreScriptRefusesOnceTheUpgradeHasBegun(t *testing.T) {
	for _, marker := range []string{"begun-upgrade", "done-minimal"} {
		dir := t.TempDir()
		for rel, c := range map[string]string{
			"etc/sources.list":                  "deb http://deb.debian.org/debian bookworm main\n",
			"bk/apt-orig/sources.list":          "deb http://deb.debian.org/debian bullseye main\n",
			"bk/apt-orig/sources.list.d/x.list": "deb http://x.example/ bullseye main\n",
			"bk/restore-sources.sh":             restoreScript,
			"bk/done-sources":                   "",
			"bk/done-download":                  "",
			"bk/" + marker:                      "",
		} {
			path := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		out, code := runSh(t, dir, "APT_ETC=./etc CASAOS_NO_UPDATE=1 sh ./bk/restore-sources.sh")
		if code == 0 || !strings.Contains(out, "STOP: the upgrade has begun (block 5 was started)") || !strings.Contains(out, "Do not restore anything. Only forward: finish with dpkg --configure -a, then apt-get -f install, then block 6") {
			t.Errorf("%s: exit %d, %q", marker, code, out)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "etc", "sources.list"))
		if string(got) != "deb http://deb.debian.org/debian bookworm main\n" {
			t.Errorf("%s: the sources were changed: %q", marker, got)
		}
		for _, still := range []string{"bk/apt-orig", "bk/done-sources", "bk/done-download"} {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(still))); err != nil {
				t.Errorf("%s: %s was taken away", marker, still)
			}
		}
		if aside, _ := filepath.Glob(filepath.Join(dir, "bk", "apt-orig.restored.*")); len(aside) != 0 {
			t.Errorf("%s: the copy was put aside: %v", marker, aside)
		}
	}
}

// The script finds its copy of /etc/apt beside itself, whatever the way it was called: by its path, by a path with a dot, and by its name alone
// from its own directory (which the block's own hint, ls of the directory, invites).
func TestTheRestoreScriptFindsItsCopyWhereverItIsRunFrom(t *testing.T) {
	for _, tc := range []struct{ name, cd, call string }{
		{"by a path", ".", "sh ./bk/restore-sources.sh"},
		{"by an absolute path", ".", "sh \"$PWD/bk/restore-sources.sh\""},
		{"by its name, from its directory", "bk", "sh restore-sources.sh"},
		{"by a path with a dot, from its directory", "bk", "sh ./restore-sources.sh"},
		{"by a path that goes up and down", "bk", "sh ../bk/restore-sources.sh"},
	} {
		dir := t.TempDir()
		for rel, c := range map[string]string{
			"etc/sources.list":                  "deb http://deb.debian.org/debian bookworm main\n",
			"bk/apt-orig/sources.list":          "deb http://deb.debian.org/debian bullseye main\n",
			"bk/apt-orig/sources.list.d/x.list": "deb http://x.example/ bullseye main\n",
			"bk/restore-sources.sh":             restoreScript,
			"bk/done-sources":                   "",
		} {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		os.MkdirAll(filepath.Join(dir, "etc", "sources.list.d"), 0o755)
		apt := "../etc"
		if tc.cd == "." {
			apt = "./etc"
		}
		out, code := runSh(t, dir, "cd "+tc.cd+" && APT_ETC="+apt+" CASAOS_NO_UPDATE=1 "+tc.call)
		got, _ := os.ReadFile(filepath.Join(dir, "etc", "sources.list"))
		if code != 0 || string(got) != "deb http://deb.debian.org/debian bullseye main\n" {
			t.Errorf("%s: exit %d, %q, sources.list %q", tc.name, code, out, got)
		}
	}
}

func TestTheDockerSourceIsPutBackForTheNewRelease(t *testing.T) {
	p := *realPlan(t)
	p.Paused = append(slices.Clone(p.Paused), Paused{File: "sources.list", Line: 4, Hosts: []string{"https://download.docker.com"}, Docker: true}, Paused{File: "vendor.list", Hosts: []string{"https://vendor.example.com"}})
	rb, err := BuildRunbook(p)
	if err != nil {
		t.Fatal(err)
	}
	code := blockOf(t, rb.Text, "after")
	fragment := between(t, code, "# Docker's source goes back", "\nfi\n")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "etc", "sources.list.d"), 0o755)
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	// curl prints the status that it is asked to write (-w) as FAKE_CURL_CODE, and ends with FAKE_CURL_EXIT
	os.WriteFile(filepath.Join(dir, "bin", "curl"), []byte("#!/bin/sh\ncase \"$*\" in *http_code*) echo \"${FAKE_CURL_CODE:-200}\" ;; esac\nexit ${FAKE_CURL_EXIT:-0}\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "etc", "sources.list.d", "docker.list.casaos-paused"), []byte("# the engine\ndeb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian bullseye stable\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "etc", "sources.list"), []byte("deb http://deb.debian.org/debian bookworm main\n# a comment\n# casaos-paused: deb https://vendor.example.com/apt bullseye main\n# casaos-paused: deb https://download.docker.com/linux/debian bullseye stable\n"), 0o644)
	script := "set -eu\nPATH=\"$PWD/bin:$PATH\"; export PATH\nAPT_ETC=./etc\n" + fragment + "\n"
	out, exit := runSh(t, dir, script)
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, out)
	}
	gotDocker, _ := os.ReadFile(filepath.Join(dir, "etc", "sources.list.d", "docker.list"))
	if string(gotDocker) != "# the engine\ndeb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian bookworm stable\n" {
		t.Errorf("docker.list = %q", gotDocker)
	}
	if _, err := os.Stat(filepath.Join(dir, "etc", "sources.list.d", "docker.list.casaos-paused")); err == nil {
		t.Error("the paused name is left")
	}
	gotList, _ := os.ReadFile(filepath.Join(dir, "etc", "sources.list"))
	if string(gotList) != "deb http://deb.debian.org/debian bookworm main\n# a comment\n# casaos-paused: deb https://vendor.example.com/apt bullseye main\ndeb https://download.docker.com/linux/debian bookworm stable\n" {
		t.Errorf("sources.list = %q", gotList)
	}
	// when Docker publishes nothing for the new release (an error status that says so: not found, forbidden, gone), nothing moves, the block goes on, and the
	// plain commands that put the source back by hand are printed. Any other error status is Docker's own trouble (429, 5xx), which does not say that it publishes
	// nothing: the block stops, with the same commands. So does a curl that fails otherwise (the network) or is not there.
	byHand := []string{
		"mv /etc/apt/sources.list.d/docker.list.casaos-paused /etc/apt/sources.list.d/docker.list   then:   sed -i \"s/ bullseye / bookworm /\" /etc/apt/sources.list.d/docker.list",
		"sed -i \"4s/^# casaos-paused: //; 4s/ bullseye / bookworm /\" /etc/apt/sources.list",
		"then:   apt-get update",
	}
	for _, tc := range []struct {
		name, env, says string
		ends            bool
	}{
		{"not found", "FAKE_CURL_EXIT=22 FAKE_CURL_CODE=404", "Docker publishes nothing for bookworm yet (download.docker.com answered the status 404", true},
		{"forbidden", "FAKE_CURL_EXIT=22 FAKE_CURL_CODE=403", "Docker publishes nothing for bookworm yet (download.docker.com answered the status 403", true},
		{"gone", "FAKE_CURL_EXIT=22 FAKE_CURL_CODE=410", "Docker publishes nothing for bookworm yet (download.docker.com answered the status 410", true},
		{"too many requests", "FAKE_CURL_EXIT=22 FAKE_CURL_CODE=429", "STOP: download.docker.com answered the error status 429, which does not say that it publishes nothing", false},
		{"a server error", "FAKE_CURL_EXIT=22 FAKE_CURL_CODE=503", "STOP: download.docker.com answered the error status 503, which does not say that it publishes nothing", false},
		{"no name", "FAKE_CURL_EXIT=6 FAKE_CURL_CODE=000", "STOP: curl could not reach download.docker.com (exit 6", false},
		{"too slow", "FAKE_CURL_EXIT=28 FAKE_CURL_CODE=000", "STOP: curl could not reach download.docker.com (exit 28", false},
	} {
		os.WriteFile(filepath.Join(dir, "etc", "sources.list.d", "docker.list.casaos-paused"), []byte("deb https://download.docker.com/linux/debian bullseye stable\n"), 0o644)
		os.Remove(filepath.Join(dir, "etc", "sources.list.d", "docker.list"))
		out, exit = runSh(t, dir, tc.env+"; export FAKE_CURL_EXIT FAKE_CURL_CODE\n"+script)
		if (exit == 0) != tc.ends || !strings.Contains(out, tc.says) {
			t.Errorf("%s: exit %d: %s", tc.name, exit, out)
		}
		for _, want := range byHand {
			if !strings.Contains(out, want) {
				t.Errorf("%s: the commands to put the source back by hand lack %q: %s", tc.name, want, out)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "etc", "sources.list.d", "docker.list.casaos-paused")); err != nil {
			t.Errorf("%s: the paused file moved when Docker has no release", tc.name)
		}
		if _, err := os.Stat(filepath.Join(dir, "etc", "sources.list.d", "docker.list")); err == nil {
			t.Errorf("%s: docker.list was written when Docker has no release", tc.name)
		}
	}
	// a run that was cut between the new file and the removal of the paused one, or that failed to write the new one, is done again from the start: the paused file is
	// the source of the new one, and it is removed last
	os.WriteFile(filepath.Join(dir, "etc", "sources.list.d", "docker.list.casaos-paused"), []byte("# the engine\ndeb https://download.docker.com/linux/debian bullseye stable\n"), 0o644)
	os.Remove(filepath.Join(dir, "etc", "sources.list.d", "docker.list"))
	os.WriteFile(filepath.Join(dir, "bin", "awk"), []byte("#!/bin/sh\nexit 1\n"), 0o755)
	if out, exit = runSh(t, dir, script); exit == 0 {
		t.Errorf("an awk that fails did not stop the block: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "etc", "sources.list.d", "docker.list.casaos-paused")); err != nil {
		t.Error("the paused file is gone, though the new file was not written: the run again would skip Docker's source for good")
	}
	os.Remove(filepath.Join(dir, "bin", "awk"))
	if out, exit = runSh(t, dir, script); exit != 0 {
		t.Fatalf("the run again: exit %d: %s", exit, out)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "etc", "sources.list.d", "docker.list")); string(got) != "# the engine\ndeb https://download.docker.com/linux/debian bookworm stable\n" {
		t.Errorf("the run again: docker.list = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "etc", "sources.list.d", "docker.list.casaos-paused")); err == nil {
		t.Error("the run again left the paused file")
	}
	if _, err := os.Stat(filepath.Join(dir, "etc", "sources.list.d", "docker.list.casaos-new")); err == nil {
		t.Error("the run again left a new file")
	}
	// and the block again, with the paused file gone and docker.list there: nothing changes it (the new file was made, the removal of the paused one was cut)
	os.WriteFile(filepath.Join(dir, "etc", "sources.list.d", "docker.list.casaos-paused"), []byte("# the engine\ndeb https://download.docker.com/linux/debian bullseye stable\n"), 0o644)
	if out, exit = runSh(t, dir, script); exit != 0 {
		t.Fatalf("a run where both files are there: exit %d: %s", exit, out)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "etc", "sources.list.d", "docker.list")); string(got) != "# the engine\ndeb https://download.docker.com/linux/debian bookworm stable\n" {
		t.Errorf("both files there: docker.list = %q", got)
	}
	// and the others are named as left paused
	if !strings.Contains(code, "These sources stay paused: vendor.list (https://vendor.example.com)") {
		t.Errorf("the others are not named: %s", code)
	}
}

// Only the lines of sources.list that the plan said are Docker's are put back, by their number and by what they are: a line of
// a third party that has the name of Docker's host in its path or in its comment is not, and neither is a line that looks like
// Docker's and that the plan did not say.
func TestOnlyTheLinesTheDockerSourcesTheirNumbersSayArePutBack(t *testing.T) {
	p := *realPlan(t)
	p.Paused = []Paused{
		{File: "sources.list", Line: 2, Hosts: []string{"https://nexus.corp.example"}},
		{File: "sources.list", Line: 3, Hosts: []string{"https://vendor.example.com"}},
		{File: "sources.list", Line: 4, Hosts: []string{"https://download.docker.com"}, Docker: true},
		{File: "sources.list", Line: 6, Hosts: []string{"https://download.docker.com"}, Docker: true},
		{File: "sources.list", Line: 7, Hosts: []string{"https://download.docker.com"}, Docker: true},
		{File: "sources.list", Line: 8, Hosts: []string{"https://download.docker.com"}, Docker: true},
		{File: "sources.list", Line: 9, Hosts: []string{"http://download.docker.com"}, Docker: true},
	}
	rb, err := BuildRunbook(p)
	if err != nil {
		t.Fatal(err)
	}
	fragment := between(t, blockOf(t, rb.Text, "after"), "# Docker's source goes back", "\nelse\n")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "etc"), 0o755)
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	os.WriteFile(filepath.Join(dir, "bin", "curl"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	lines := []string{
		"deb http://deb.debian.org/debian bookworm main",
		"# casaos-paused: deb https://nexus.corp.example/download.docker.com/linux/debian bullseye stable",
		"# casaos-paused: deb https://vendor.example.com/apt bullseye main # see download.docker.com",
		"# casaos-paused: deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian bullseye stable",
		"# casaos-paused: deb https://download.docker.com/linux/debian bullseye stable",
		"# casaos-paused: deb-src https://u:p@download.docker.com/linux/debian bullseye stable",
		"# casaos-paused: deb https://download.docker.com.evil.example/linux/debian bullseye stable",
		"# casaos-paused: deb https://vendor.example.com/apt?mirror=https://download.docker.com/x bullseye main",
		"# casaos-paused: deb http://download.docker.com/linux/debian bullseye stable",
	}
	os.WriteFile(filepath.Join(dir, "etc", "sources.list"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	out, exit := runSh(t, dir, "set -eu\nPATH=\"$PWD/bin:$PATH\"; export PATH\nAPT_ETC=./etc\n"+fragment+"  :\nfi\n")
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, out)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "etc", "sources.list"))
	want := []string{
		lines[0], lines[1], lines[2],
		"deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian bookworm stable", // line 4: the plan said so, and it is
		lines[4], // line 5: it looks like Docker's and the plan did not say it
		"deb-src https://u:p@download.docker.com/linux/debian bookworm stable", // line 6: with a user
		lines[6], // line 7: the plan said it is Docker's, and the host is another: it is not put back
		lines[7], // line 8: the plan said it, and the name of Docker's host is in the query of a vendor's address
		"deb http://download.docker.com/linux/debian bookworm stable", // line 9: http is Docker's too, as the plan said
	}
	if g := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n"); !slices.Equal(g, want) {
		t.Errorf("sources.list:\n%s\nwant:\n%s", strings.Join(g, "\n"), strings.Join(want, "\n"))
	}
	// the number is not the only thing that decides: a Docker line that is on another line than the plan's stays put
	os.WriteFile(filepath.Join(dir, "etc", "sources.list"), []byte(strings.Join(lines[:4], "\n")+"\n# casaos-paused: deb https://download.docker.com/linux/debian bullseye stable\n"), 0o644)
	if _, exit := runSh(t, dir, "set -eu\nPATH=\"$PWD/bin:$PATH\"; export PATH\nAPT_ETC=./etc\n"+fragment+"  :\nfi\n"); exit != 0 {
		t.Fatalf("second run: exit %d", exit)
	}
	got, _ = os.ReadFile(filepath.Join(dir, "etc", "sources.list"))
	if !strings.HasSuffix(string(got), "\n# casaos-paused: deb https://download.docker.com/linux/debian bullseye stable\n") {
		t.Errorf("a line that the plan did not name was put back: %q", got)
	}
}

// ---- who gets a runbook ------------------------------------------------------------------------------------------------

func TestThereIsNoRunbookForABlockedPlanAnUnknownHopOrAnUnsafeTree(t *testing.T) {
	base := *realPlan(t)
	cases := map[string]func(p *Plan){
		"a blocked plan": func(p *Plan) { p.Verdict = VerdictBlocked },
		"another hop":    func(p *Plan) { p.Hop = "debian-12-13" },
		"no hop":         func(p *Plan) { p.Hop = "" },
		"no tree":        func(p *Plan) { p.Sources.Tree = nil },
		"a bad id":       func(p *Plan) { p.PlanID = "p1-x; rm -rf /" },
	}
	for name, mutate := range cases {
		p := base
		mutate(&p)
		if _, err := BuildRunbook(p); !errors.Is(err, ErrNoRunbook) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for _, bad := range []string{"CASAOS_EOF", "# CASAOS_EOF", "x CASAOS_EOF x", "# >>> BLOCK 9 x", "# <<< END BLOCK 1", "# <<< END BLOCK"} {
		p := base
		p.Sources.Tree = []TreeFile{{Path: "sources.list", Content: "deb http://deb.debian.org/debian bookworm main\n" + bad + "\n"}}
		if _, err := BuildRunbook(p); !errors.Is(err, ErrRunbookUnsafe) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	// the limit is 256 KiB, which the page accepts (262144 characters): a tree of 70 KB of comments is a runbook
	p := base
	p.Sources.Tree = []TreeFile{{Path: "sources.list", Content: strings.Repeat("# a line of a comment that is long enough to count\n", 1400)}}
	if rb, err := BuildRunbook(p); err != nil || len(rb.Text) > MaxRunbookBytes {
		t.Errorf("a tree of 70 KB: err = %v", err)
	}
	if MaxRunbookBytes != 262144 {
		t.Errorf("the limit is %d, and the page accepts 262144", MaxRunbookBytes)
	}
	p = base
	p.Sources.Tree = []TreeFile{{Path: "sources.list", Content: strings.Repeat("# a line of a comment that is long enough to count\n", 6000)}}
	if _, err := BuildRunbook(p); !errors.Is(err, ErrRunbookTooLarge) || !strings.Contains(err.Error(), "shorten the comments of the sources and the number of third-party files") {
		t.Errorf("a huge tree: err = %v", err)
	}
	// a plan that needs the owner's acceptance has a runbook; the plan of a box with nothing to say too
	p = base
	p.Verdict = VerdictOK
	if _, err := BuildRunbook(p); err != nil {
		t.Errorf("an ok plan: %v", err)
	}
}

func TestTheNamesOfTheManualRemovalsAreTypedToAcceptThem(t *testing.T) {
	// the real box: fuse is manual
	_, rb := realRunbook(t)
	code := blockOf(t, rb.Text, "sources")
	if !strings.Contains(code, "ACCEPT_REMOVALS=\"\"\n[ \"$ACCEPT_REMOVALS\" = 'fuse' ] || { echo \"STOP: type the names of the packages that are removed, fuse,") {
		t.Errorf("block 3 does not ask for the names:\n%s", code[:min(len(code), 900)])
	}
	if strings.Index(code, "ACCEPT_REMOVALS") > strings.Index(code, "cp -a \"$APT_ETC\"") {
		t.Error("the acceptance is asked after /etc/apt was touched")
	}
	if !strings.Contains(rb.Text, "You installed yourself: fuse") {
		t.Error("the header does not say it")
	}
	// when nothing is manual, nothing is asked
	b := newBox(t)
	b.files[extendedStates] = "Package: libsemanage1\nAuto-Installed: 1\n\nPackage: fuse\nAuto-Installed: 1\n"
	p := mustPlan(t, b, newSandbox(b.fakeFiles, realOutputs(t)))
	rb2, err := BuildRunbook(*p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rb2.Text, "ACCEPT_REMOVALS") {
		t.Error("an acceptance is asked when nothing is manual")
	}
}

func TestTheNumbersOfThePlanAreTheChecksOfBlock0(t *testing.T) {
	p, rb := realRunbook(t)
	code := blockOf(t, rb.Text, "verify")
	for _, row := range p.Disk {
		want := "[ \"$(avail '" + row.Path + "')\" -ge " + itoa(int((row.Need+1023)/1024)) + " ] ||"
		if !strings.Contains(code, want) {
			t.Errorf("no check of %s: want %s", row.Path, want)
		}
	}
	if !strings.Contains(code, "[ \"$now\" = '"+p.Sources.HashCurrent+"' ]") {
		t.Error("the hash of the sources is not checked")
	}
	if !strings.Contains(code, `[ -n "$TYPED_HOSTNAME" ] && [ "$TYPED_HOSTNAME" = "$(hostname)" ]`) {
		t.Error("the name of the machine is not typed")
	}
	// without sizes, the coarse floors
	q := *p
	q.Disk = nil
	rb2, err := BuildRunbook(q)
	if err != nil {
		t.Fatal(err)
	}
	if c := blockOf(t, rb2.Text, "verify"); !strings.Contains(c, "only the coarse floors are checked") || !strings.Contains(c, "-ge 1572864") || !strings.Contains(c, "-ge 2097152") {
		t.Errorf("no coarse floors:\n%s", c)
	}
}

func TestEveryStopOfTheRunbookIsOnALineThatStopsTheBlock(t *testing.T) {
	_, rb := realRunbook(t)
	for i, c := range blocks(t, rb.Text) {
		_, rest := heredocs(c)
		for _, line := range strings.Split(rest, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") || !strings.Contains(line, "STOP:") {
				continue
			}
			// a STOP is said by an echo, and the block ends on its line or the next ones of the same if
			if !strings.Contains(line, "exit 1") && !strings.HasPrefix(trimmed, "echo \"STOP:") {
				t.Errorf("block %d: a STOP that does not exit: %q", i, line)
			}
		}
	}
}

func TestTheTextIsTheSameWhateverTheClockOfTheTest(t *testing.T) {
	_, a := realRunbook(t)
	_, b := realRunbook(t)
	if a.Text != b.Text {
		t.Error("the same plan gives two texts")
	}
}

// The table at the end says, for each place where the owner may have stopped, what state the machine is in and what to do: the restore script
// forgets blocks 3 and 4; a stop before the confirmation of block 5 is the state of block 4 (nothing began); from the confirmation on it is only
// forward, with the commands of the measured repair ladder, and the confirmation is typed again.
func TestTheTableAtTheEndSaysWhereTheOwnerIsAndWhatToDo(t *testing.T) {
	_, rb := realRunbook(t)
	table := rb.Text[strings.Index(rb.Text, "# IF YOU STOPPED"):]
	for _, want := range []string{
		"restore-sources.sh   (it puts the sources back, takes apt-orig aside and forgets blocks 3 and 4), then run block 3 again (pasted again with no restore, it stops and says so)",
		"5, before CONFIRM         Nothing has begun: the machine is as after block 4.   Fix what the STOP line says (it also stops here when it is not run in tmux, in screen or on a console), and run block 5 again.",
		// apt that fails before it has changed anything: the block takes its marker back and says so
		"5, apt failed at once     Nothing was installed (the block says so, and takes its marker back).",
		"5 after CONFIRM, 6        The upgrade has begun: Debian 11 and 12 are mixed.    Only forward: " + forwardCommands + "   then run the block again (block 5: type the confirmation again). Do not restore /etc/apt: from the moment block 5 writes begun-upgrade, restore-sources.sh and blocks 2, 3 and 4 refuse.",
		// a restart: the timers are live and apt-daily may be at work, so the owner stops them first, and the commands that mend have no question
		"5 or 6, cut off           A crash, a power cut, a dead terminal, a restart      First:   " + stopTimersNow + "   Then the same as the row above: only forward, with those commands",
		"7                         Debian 12 packages, the old kernel still runs.        Do what the STOP line says, run block 7 again, and do not restart the machine before it ends.",
		"restarted before 7 ended  The machine runs the new kernel, or the old one (the grub menu).   Run block 7 again. On the new kernel it says so, skips the checks that are for before a restart (the kernel, the initrd, grub), makes the ones that stay true after a boot (DNS, the clock, the iptables alternative, sshd), and goes on to block 8 only when they pass: a STOP there says what to fix, then run block 7 again. On the old one it runs all its checks and says to restart: restart again and choose the new kernel in the grub menu.",
		"a restart, blocks 3 to 6  The timers of apt are back (they are enabled units), and apt-daily may be at work.   First:   " + stopTimersNow + "   (each stop waits for its unit to end). Then run the block again, by its row",
		// the give-up advice says how to start the timers again, as they were (the state is ONE file, outside the folder of the plan), and the exact line
		"To give up: start the timers again, as they were:   systemctl start $(awk '$2 == \"active\" { print $1 }' /var/backups/casaos-os-upgrade-timers.state)   then:   rm -f /var/backups/casaos-os-upgrade-timers.state",
		"then run block 3 again (pasted again with no restore, it stops and says so). To give up: do that, then start the timers again, as for blocks 0 to 2.",
		"The same as for block 3, timers included.",
		"after the restart",
		"after block 8             The hop is over (done-after is there).",
		// another plan of the machine ran its block 3: the way back is its restore script, which the stop names
		"0 or 3 of another plan    Another plan of this machine ran its block 3", "The STOP line names the restore script of that plan: run it with sh, then this block again. When that plan's upgrade has begun (begun-upgrade in its folder), only forward, as for block 5.",
		// a restart in the middle of the hop: either kernel may run, and block 6 can be run again on either
		"run block 6 again on either (block 7 says which it is)",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("the table lacks %q:\n%s", want, table)
		}
	}
	if strings.Contains(table, "5, 6, 7") {
		t.Error("the row that said the upgrade had begun for block 5 whatever happened is still there")
	}
}

// The header cites the sizes that apt printed, and only those. A plan whose estimate is known and zero because there is nothing to do at all has none (apt prints no size for it): the
// header says that there was nothing to fetch, and that the floors of block 0 are those of the formulas, instead of "about 0 MB (apt's own figures)". A plan that does something and
// whose sizes are zero (apt printed them: everything is in the cache already) keeps its words, and so does a plan with sizes.
func TestTheHeaderCitesOnlyTheSizesThatAptPrinted(t *testing.T) {
	header := func(p Plan) string {
		t.Helper()
		rb, err := BuildRunbook(p)
		if err != nil {
			t.Fatal(err)
		}
		return rb.Text[:strings.Index(rb.Text, "# BLOCK 0 ")]
	}
	const nothing = "#  - apt had nothing to fetch and nothing to install, and printed no size: none is said here. Block 0 checks the room by the lowest floors of the plan's formulas (they hold whatever the size).\n"
	zero := func() *int64 { n := int64(0); return &n }
	p := *realPlan(t)
	if h := header(p); !strings.Contains(h, "It downloads about 140 MB and takes about 300 MB more room on the disk (apt's own figures, rounded).") || strings.Contains(h, nothing) {
		t.Errorf("the header of a plan with sizes:\n%s", h)
	}
	p.Counts.DistUpgrade = Count{}
	p.Removals = nil
	p.Estimates = Estimates{Known: true, DownloadBytes: zero(), ExtraBytes: zero()}
	if h := header(p); !strings.Contains(h, nothing) || strings.Contains(h, "0 MB") || strings.Contains(h, "apt's own figures") || !strings.Contains(h, "apt would upgrade 0 packages, install 0 new ones and remove 0 (none).") {
		t.Errorf("the header of a plan with nothing to do:\n%s", h)
	}
	// something to do, and nothing to download or to add: apt printed those sizes, and the words are as for any sizes
	p.Counts.DistUpgrade = Count{Upgraded: 3}
	if h := header(p); !strings.Contains(h, "It downloads about 0 MB and takes about 0 MB more room on the disk (apt's own figures, rounded).") || strings.Contains(h, nothing) {
		t.Errorf("the header of a plan with sizes that are zero:\n%s", h)
	}
	// the estimate is not known: the header says nothing of sizes at all
	p.Estimates = Estimates{}
	if h := header(p); strings.Contains(h, "It downloads") || strings.Contains(h, nothing) {
		t.Errorf("the header of a plan with no sizes:\n%s", h)
	}
}

// The most a runbook is: MaxRunbookBytes. A text of exactly that many bytes is a runbook, the page accepts it (it refuses over 262144), and one byte more is refused. The tree is padded
// with a line of comment to land on the byte (what a file of the tree holds is written once, in its heredoc, and nowhere else).
func TestTheRunbookCapIsPinnedToTheByte(t *testing.T) {
	base := *realPlan(t)
	small, err := BuildRunbook(base)
	if err != nil {
		t.Fatal(err)
	}
	build := func(size int) (*Runbook, error) {
		p := base
		p.Sources.Tree = slices.Clone(base.Sources.Tree)
		last := len(p.Sources.Tree) - 1
		pad := size - len(small.Text)
		if pad < 2 {
			t.Fatalf("a text of %d bytes cannot be made of one of %d", size, len(small.Text))
		}
		p.Sources.Tree[last].Content += "#" + strings.Repeat("x", pad-2) + "\n"
		return BuildRunbook(p)
	}
	rb, err := build(MaxRunbookBytes)
	if err != nil || len(rb.Text) != MaxRunbookBytes {
		t.Fatalf("a text of exactly %d bytes: err %v, %d bytes", MaxRunbookBytes, err, len(rb.Text))
	}
	if rb, err := build(MaxRunbookBytes - 1); err != nil || len(rb.Text) != MaxRunbookBytes-1 {
		t.Errorf("a text of one byte under the limit: err %v", err)
	}
	if rb, err := build(MaxRunbookBytes + 1); !errors.Is(err, ErrRunbookTooLarge) || rb != nil {
		t.Errorf("a text of one byte over the limit: err %v", err)
	}
}
