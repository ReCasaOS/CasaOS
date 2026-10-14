package osplan

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The runbook: the text with which the owner does the hop by hand, written from the plan of this machine. Its apt commands of blocks 3
// to 6 (the sources, update, the download and the minimal upgrade, the download and the full upgrade, the simulation that finds nothing
// left) are the ones of a hop from Debian 11 to Debian 12 that was measured on a real guest (hop_unit_debian of the measurement's script),
// but for one option: that hop ran them with Dpkg::Use-Pty=0, and the blocks leave apt's pty on (below). The repair ladder of block 6 and
// its catch-up passes are the release notes' and were not needed in that run: they have never been run for real, and the text says so.
// The stops, the checks and the human steps around the commands were not measured either, and the text says so.
//
// The text is a shell file made of blocks. Every block is a subshell that starts with "set -eu", prints what it is about to do,
// checks its own preconditions with STOP lines, and ends with a check and a marker file; a block does not run when the one before
// it did not end. What the box's own files hold (the lines of the sources) is written by quoted heredocs (<<'CASAOS_EOF'), never
// interpolated.
//
// Differences from the spec text of the rung, declared: block 5 runs "apt-get upgrade" without "--without-new-pkgs", as the measured
// hop did; block 8 puts Docker's source back only when Docker publishes a Release file for the new release (a network call the
// measured hop never made); the backup directory is private (mode 700) and its /etc archive is mode 600; blocks 5 and 6 refuse to
// run in a terminal that does not survive a dropped connection; block 0 refuses a plan older than its 24 hours; block 3 reads what it
// wrote (the hash of the generated tree, the restore script) before it moves anything in place.
//
// Declared too: block 5 writes the marker begun-upgrade before apt-get upgrade (and takes it back when apt failed before it changed
// anything: a marker that lies would forbid the way back for nothing), and from then on the restore script and blocks 2, 3 and 4 refuse,
// with words that only go forward; block 0 refuses to start on a machine that has neither a console nor tmux or screen, so that the
// owner installs one while the machine is still Debian 11, and on a /boot or an EFI partition with no room; block 7 tells a restart that
// came early by whether the kernel that runs was installed before the hop (the packages.tsv of block 2), not by the boot; block 8
// checks that the kernel that runs is the one block 7 looked at and that the sources of apt name bookworm; the blocks that write in
// /etc/apt do it with umask 022 and read it back; the checks that read ps, ss and curl stop, with the package to install, when the
// command is not there; the state of the timers is ONE file outside the folder of the plan (a second plan has another id and another
// folder), published all at once so that two runs at the same time cannot mix their lines; blocks 0 and 3 look at the folders that
// another plan of the machine left (a second plan has no way back to the sources that the first one rewrote, unless it is told where
// the first one's restore script is); apt runs in blocks 5 and 6 with its pty (no Dpkg::Use-Pty=0, which the measured hop had): with the
// option apt 2.2.4 writes nothing of what the maintainer scripts print to /var/log/apt/term.log, which the STOP lines of the blocks name,
// and without it the file holds it (run in a Debian 11 chroot with a package whose postinst fails, under script, and nowhere else); the
// repair ladder of block 6 simulates apt-get -f install and the full upgrade after it, and runs the guard of the protected names on each
// simulation before it runs the real command; and every block stops once block 8 has ended, whichever plan of the machine ended it, and when
// the machine is no longer Debian 11 although this plan did not begin the upgrade of its packages (another plan, or the owner, took it on).

// The same blocks are written a second time for the unit of the dashboard (BuildAuto, auto.go): every place where the unit differs from a person at a terminal is a method of rbCtx
// (prologue, typedHostname, acceptRemovals, confirm, muxAtHand, survive, aptq, syncAfterMarker, dockerAnswers) that writes the owner's text when c.auto is nil, so that the text
// of the owner's runbook does not change by one byte for it.

// The reasons there is no runbook for a plan.
var (
	// ErrNoRunbook: the plan has none (it is blocked, or is not for a hop that has a runbook, or has no tree).
	ErrNoRunbook = errors.New("there is no runbook for this plan")
	// ErrRunbookUnsafe: a line of a file of the box has the word that ends a heredoc, or the marker of a block: the text could
	// not be told from its own structure.
	ErrRunbookUnsafe = errors.New("a line of the sources would end a part of the runbook early")
	// ErrRunbookTooLarge: the text is more than 256 KiB. The fixed text is some 100 KB, which leaves some 150 KB for the lines of the sources and for the
	// third-party files that are put aside: this is for a tree of hundreds of comment lines, and its message says what to cut.
	ErrRunbookTooLarge = errors.New("the runbook is larger than it may be: shorten the comments of the sources and the number of third-party files")
)

// MaxRunbookBytes is the most a runbook is: what the page accepts (262144 characters).
const MaxRunbookBytes = 256 << 10

// heredocEnd ends the quoted heredocs of the runbook.
const heredocEnd = "CASAOS_EOF"

// A RunbookBlock is a block of the text, as the page lists them.
type RunbookBlock struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Reboot: the machine is to be restarted by the owner after this block.
	Reboot bool `json:"reboot"`
	// PointOfNoReturn: from this block on, nothing is undone by going back.
	PointOfNoReturn bool `json:"point_of_no_return"`
}

// A Runbook is the text for a plan, and its blocks.
type Runbook struct {
	PlanID string         `json:"plan_id"`
	Hop    string         `json:"hop"`
	Text   string         `json:"text"`
	Blocks []RunbookBlock `json:"blocks"`
}

// rbCtx is what a block is written from.
type rbCtx struct {
	plan Plan
	hop  HopSpec
	// manual are the names of the removals that the owner has to accept
	manual []string
	// auto is set when the text is the one that the unit of the dashboard runs (BuildAuto): the hooks of auto.go change what they write for it, and
	// write exactly what the owner's text has when it is nil. cur is the block being written and fired counts the hooks that went off in it.
	auto  *AutoParams
	cur   string
	fired map[string]int
	// bk is the backup folder when it is not the plan's (a refresh has no plan); "" is /var/backups/casaos-os-upgrade-<plan id>. refresh is set for the text of a refresh
	// (BuildRefresh, auto_refresh.go): it is written from the same pieces, and holds what that text has of its own.
	bk      string
	refresh *refreshCtx
}

// backupFolder is the folder that the blocks keep what they write in.
func (c *rbCtx) backupFolder() string {
	if c.bk != "" {
		return c.bk
	}
	return "/var/backups/casaos-os-upgrade-" + c.plan.PlanID
}

type blockSpec struct {
	RunbookBlock
	prose []string
	// body is the code of the block, from "set -eu" to its end, inside the subshell; prev is the block that must have ended.
	body func(c *rbCtx, prev string) string
}

// runbookHops is the table of the hops that have a runbook: adding a hop is adding its blocks here.
var runbookHops = map[osreadiness.HopID][]blockSpec{
	osreadiness.HopDebian11to12: debian11to12Blocks,
}

// backupFoldersScopedByHop says whether head() and otherPlans() look only at the backup folders of their own hop. They do not: they look at every
// /var/backups/casaos-os-upgrade-* folder, which is right while there is one hop (a second hop's folder is a folder of another plan of this machine, as far as the
// first hop's blocks can tell, and would stop them for the wrong reason). A second row of runbookHops makes TestASecondHopNeedsTheBackupFoldersScopedByHop fail until
// the folders are told apart by hop and this is true.
const backupFoldersScopedByHop = false

// the markers of a block, which the tests and the page look for
const (
	blockStart = "# >>> BLOCK "
	blockEnd   = "# <<< END BLOCK "
)

// shQ is s as one word of a shell, in single quotes.
func shQ(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// newRbCtx is what the text of a plan is written from, and the blocks of its hop: the refusals that both texts (the owner's, and the unit's of BuildAuto) share.
func newRbCtx(p Plan) (*rbCtx, []blockSpec, error) {
	hop, ok := PlannedHop(osreadiness.HopID(p.Hop))
	blocks := runbookHops[osreadiness.HopID(p.Hop)]
	switch {
	case p.Verdict == VerdictBlocked, !ok, len(blocks) == 0, len(p.Sources.Tree) == 0, !ValidPlanID(p.PlanID):
		return nil, nil, ErrNoRunbook
	}
	if !treeIsSafe(p.Sources.Tree) {
		return nil, nil, ErrRunbookUnsafe
	}
	c := &rbCtx{plan: p, hop: hop}
	for _, pr := range p.Problems {
		if pr.Code == ProblemRemovalManual {
			if name, _ := pr.Data["name"].(string); name != "" {
				c.manual = append(c.manual, name)
			}
		}
	}
	return c, blocks, nil
}

// treeIsSafe says whether no line of the files of the tree has the word that ends a heredoc, or the marker of a block: the text could not be told from its own structure.
func treeIsSafe(tree []TreeFile) bool {
	for _, f := range tree {
		for _, bad := range []string{heredocEnd, blockStart, blockEnd, ">>> BLOCK", "<<< END BLOCK"} {
			if strings.Contains(f.Content, bad) {
				return false
			}
		}
	}
	return true
}

// BuildRunbook writes the runbook of a plan. It is refused (ErrNoRunbook) for a plan that is blocked, which has no runbook
// (its problems say why), and ErrRunbookUnsafe when a line of the tree has the word that ends a heredoc.
func BuildRunbook(p Plan) (*Runbook, error) {
	c, blocks, err := newRbCtx(p)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(runbookHeader(c))
	var info []RunbookBlock
	prev := ""
	for i, blk := range blocks {
		fmt.Fprintf(&b, "\n# ==============================\n# BLOCK %d (of the blocks 0 to %d): %s\n", i, len(blocks)-1, blk.Title)
		switch {
		case blk.PointOfNoReturn:
			b.WriteString("# THIS BLOCK IS THE POINT OF NO RETURN.\n")
		case blk.Reboot:
			b.WriteString("# AFTER THIS BLOCK, RESTART THE MACHINE YOURSELF.\n")
		}
		for _, line := range blk.prose {
			if line == "" {
				b.WriteString("#\n")
			} else {
				b.WriteString("# " + line + "\n")
			}
		}
		b.WriteString("# ==============================\n")
		fmt.Fprintf(&b, "%s%d %s\n(\n", blockStart, i, blk.ID)
		b.WriteString(strings.TrimRight(blk.body(c, prev), "\n"))
		fmt.Fprintf(&b, "\n)\n%s%d\n", blockEnd, i)
		prev = blk.ID
		info = append(info, blk.RunbookBlock)
	}
	b.WriteString(resumeMap(blocks))
	text := b.String()
	if len(text) > MaxRunbookBytes {
		return nil, ErrRunbookTooLarge
	}
	return &Runbook{PlanID: p.PlanID, Hop: p.Hop, Text: text, Blocks: info}, nil
}

func runbookHeader(c *rbCtx) string {
	p := c.plan
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# This is a runbook, not a script: open it, and paste ONE BLOCK AT A TIME into a console of the machine, as root.\n")
	b.WriteString("case $- in *i*) ;; *) echo \"This file is a runbook, not a script. Open it and paste one block at a time into a root console, reading what each one prints.\"; exit 1;; esac\n")
	b.WriteString("#\n")
	fmt.Fprintf(&b, "# Upgrade of the operating system by hand: Debian 11 (%s) to Debian 12 (%s).\n", c.hop.From, c.hop.To)
	fmt.Fprintf(&b, "# Plan: %s   made at %s   (hop %s; it was good for 24 hours, until %s)\n", p.PlanID, p.MadeAt.UTC().Format("2006-01-02 15:04:05")+" UTC", p.Hop, p.ExpiresAt.UTC().Format("2006-01-02 15:04")+" UTC")
	b.WriteString("#\n")
	b.WriteString("# This text was written for exactly this machine and this plan. If the machine changed since (packages, sources, holds), ask for a new plan.\n")
	b.WriteString("# Block 0 checks that the plan is not older than its 24 hours and that the sources of apt are the ones the plan was made from. The plan holds no identity of the machine, so\n")
	b.WriteString("# nothing here knows WHICH machine this is: the name that you type guards the terminal (it shows that you looked at which machine you are on), not the identity of the machine.\n")
	b.WriteString("# Do not adapt the text by hand.\n")
	b.WriteString("# A NEW PLAN is made in the dashboard: open the Operating system window and press 'Make the plan again' (or 'See what the upgrade would do', the first time). A plan is good for 24 hours,\n")
	b.WriteString("# and a text is written from one plan only. TO ASK FOR HELP is to ask whoever looks after this machine and its ReCasaOS (the person or the company that installed it for you), and to give them\n")
	b.WriteString("# the text of the block, everything it printed, and the files that its STOP line names.\n")
	b.WriteString("#\n")
	b.WriteString("# THIS TEXT DOES NOT RUN THE READINESS REPORT. Read the report in the dashboard first, and do not start when it says that it refuses: it looks at what this text does not\n")
	b.WriteString("# (the boot loader, the file systems, the packages that are built from source, and more). Block 0 looks at a few of those again (room in /boot, the EFI partition, the disk of grub-pc).\n")
	b.WriteString("#\n")
	b.WriteString("# WHAT YOU NEED BEFORE YOU START\n")
	b.WriteString("#  - A snapshot of the machine (from the hypervisor), or a console that works without the network: a screen and a keyboard, a serial line, the\n")
	b.WriteString("#    hypervisor's console. A tunnel (Tailscale, Cloudflare) does not count: it goes down with the network card.\n")
	b.WriteString("#  - A terminal that survives a dropped connection, for blocks 5 and 6: the machine's own console, or tmux or screen. INSTALL tmux NOW, BEFORE block 3, while the machine is\n")
	b.WriteString("#    still Debian 11 (apt-get install tmux): block 0 stops when there is no console and neither is installed. After block 3 install nothing by hand: a package installed from Debian 12's\n")
	b.WriteString("#    sources upgrades libc6 with it, outside the protection of block 5. Become root first (su - or sudo -i), THEN start it from the root shell:  tmux new-session -s casaos-upgrade\n")
	b.WriteString("#    (or: screen -S casaos-upgrade)  and paste the blocks inside it (sudo and su drop the variables that tmux and screen set: that is why). Blocks 5 and 6 stop if they are not run in one of\n")
	b.WriteString("#    them or on a console. After a dropped connection: log in, become root, tmux attach -t casaos-upgrade   (or: screen -r casaos-upgrade). In block 5, libc6 restarts services (ssh, smbd and\n")
	b.WriteString("#    cron were restarted in the measured hop): do not count on your connection staying up.\n")
	b.WriteString("#  - A plain sh to paste into: if your root shell has aliases for mv, cp or rm, type sh first. The blocks use mv -f and read back what they wrote, but a plain sh is what they were tried in.\n")
	b.WriteString("#    Type sh first too when a paste is changed on its way: bash without bracketed paste takes a TAB for completion, and a tab in a comment line of the sources of this machine then changes what block 3 writes\n")
	b.WriteString("#    (block 3 sees it, stops on the hash, and stops again at every paste).\n")
	b.WriteString("#  - Debian 11 fully updated and restarted once before you start (apt-get update, apt-get upgrade, restart), as the machine of the measurement was. Nothing here checks it.\n")
	b.WriteString("#  - The machine must not be doing something else with its packages (no update running, no other package manager). Blocks 0, 3, 5 and 6 look at the processes of the package managers; the apt-get\n")
	b.WriteString("#    commands of the blocks that check, download or install wait up to 5 minutes for the dpkg lock, and apt-get update does not wait for the lock of the lists: it fails at once when another one holds it.\n")
	b.WriteString("#  - Room in /var/backups for a copy of /etc and /etc/apt. The directory that this text makes there is private to root (mode 700), because it holds a copy of /etc.\n")
	b.WriteString("#  - Time: the measured hop took about two and a half minutes of apt on a fast network; the downloads and the configuration of a real machine take longer.\n")
	b.WriteString("#\n")
	b.WriteString("# WHAT THE PLAN SAYS\n")
	fmt.Fprintf(&b, "#  - apt would upgrade %d packages, install %d new ones and remove %d (%s).\n", p.Counts.DistUpgrade.Upgraded, p.Counts.DistUpgrade.New, p.Counts.DistUpgrade.Removed, removalNames(p))
	if len(c.manual) > 0 {
		fmt.Fprintf(&b, "#  - You installed yourself: %s. Block 3 asks you to type their names: that is accepting that they are removed.\n", strings.Join(c.manual, " "))
	}
	if p.Estimates.Known && p.Estimates.DownloadBytes != nil && p.Estimates.ExtraBytes != nil {
		d, e := *p.Estimates.DownloadBytes, *p.Estimates.ExtraBytes
		if d == 0 && e == 0 && p.Counts.DistUpgrade.Upgraded == 0 && p.Counts.DistUpgrade.New == 0 && p.Counts.DistUpgrade.Removed == 0 {
			// a box with nothing to do: apt prints no size for it, and zero is what the plan knows from that (ParseEstimate), not a figure of apt's
			b.WriteString("#  - apt had nothing to fetch and nothing to install, and printed no size: none is said here. Block 0 checks the room by the lowest floors of the plan's formulas (they hold whatever the size).\n")
		} else {
			fmt.Fprintf(&b, "#  - It downloads about %s and takes about %s more room on the disk (apt's own figures, rounded).\n", megabytes(d), megabytes(e))
		}
	}
	if n := len(p.Paused); n > 0 {
		var names []string
		for _, pa := range p.Paused {
			if pa.Line == 0 {
				names = append(names, pa.File)
			} else {
				names = append(names, fmt.Sprintf("line %d of %s", pa.Line, pa.File))
			}
		}
		fmt.Fprintf(&b, "#  - Sources that are not Debian's are put aside for the hop: %s.\n", strings.Join(names, ", "))
	}
	if p.Sources.DroppedBackports > 0 {
		fmt.Fprintf(&b, "#  - %d line(s) of backports are dropped.\n", p.Sources.DroppedBackports)
	}
	if len(p.Docker.PackagesHeld) > 0 {
		fmt.Fprintf(&b, "#  - Docker's packages are on hold (%s): the hop leaves Docker as it is. Update it from the dashboard afterwards.\n", strings.Join(p.Docker.PackagesHeld, " "))
	}
	b.WriteString("#\n")
	b.WriteString("# WHAT HAS BEEN TESTED, AND WHAT HAS NOT\n")
	b.WriteString("#  - The apt COMMANDS of blocks 3 to 6 below, and their order (update, the download and the minimal upgrade, the download and the full upgrade, the simulation that finds nothing left), are\n")
	b.WriteString("#    the ones of a hop from Debian 11 to Debian 12 that was measured in a virtual machine, on a clean cloud image with ReCasaOS and Docker installed, and that ended with the machine up on Debian 12,\n")
	b.WriteString("#    but for one option: the measured hop ran them with Dpkg::Use-Pty=0, and this text leaves apt's pty on, so that /var/log/apt/term.log holds what dpkg and the scripts of the packages print.\n")
	b.WriteString("#  - APT'S OWN PTY, WHICH THIS TEXT LEAVES ON, WAS NOT RUN IN THE MEASURED HOP. It was tried in a copy of a Debian 11 system, under script, never in that virtual machine and never in a real tmux, screen or console.\n")
	b.WriteString("#    With it apt takes the terminal over while dpkg runs (raw mode: what you type goes on to dpkg, and what was pasted before is dropped): type nothing and paste nothing until the block has ended.\n")
	b.WriteString("#  - THE REPAIR LADDER AND THE CATCH-UP PASSES OF BLOCK 6 WERE NOT RUN IN THAT HOP: it did not need them. They come from the release notes of Debian 12 (dpkg --configure -a, apt-get -f install,\n")
	b.WriteString("#    the full upgrade again), and so do the simulations that block 6 makes before them. Never run for real, they may be wrong in ways nobody has seen.\n")
	b.WriteString("#  - THIS TEXT AS A WHOLE HAS NOT BEEN RUN, END TO END, ON A MACHINE THAT BOOTED. The rest of it was not measured: the checks, the STOP lines, the snapshot, the comparison with the plan,\n")
	b.WriteString("#    the restore script, the checks before the restart and the ones after it. They were tried against stand-in commands, never on a machine that was upgraded with this text:\n")
	b.WriteString("#    timers, libc6 restarting services, the initrd, grub, the restart and Docker's source were never run for real. They may be wrong in ways nobody has seen.\n")
	b.WriteString("#  - Your machine is not that image. It has its own hardware (network card names, firmware, disk controllers), its own history, files you edited, and packages that nobody tested.\n")
	b.WriteString("#    None of that was tried, and nothing here promises how it will go.\n")
	b.WriteString("#  - Nothing goes back to Debian 11 once the upgrade has begun (block 5): a failure after it is mended by going forward, or by the snapshot.\n")
	b.WriteString("#  - A line that starts with STOP says why a block did not go through. But a command that fails in the middle (a full disk, a lost connection) ends a block too, with its own message and\n")
	b.WriteString("#    no STOP line: a block that did not print 'Block N done.' did not go through, STOP line or not. Read what it printed, do not go on, and look at the table at the end.\n")
	return b.String()
}

func removalNames(p Plan) string {
	if len(p.Removals) == 0 {
		return "none"
	}
	var names []string
	for _, r := range p.Removals {
		names = append(names, r.Name)
	}
	return strings.Join(names, ", ")
}

func megabytes(n int64) string {
	if n < 0 {
		return "-" + megabytes(-n)
	}
	return strconv.FormatInt((n+500_000)/1_000_000, 10) + " MB"
}

// forwardCommands are the commands that mend a half-done upgrade, as the release notes give them (the measured hop did not need them): dpkg configures what is
// half done, then apt repairs, with the conffile choices of the hop and without a question. The repair is simulated first and read by the owner: apt-get -f
// install removes a package that has an unmet dependency without a stop, a protected one included.
const forwardCommands = "DEBIAN_FRONTEND=noninteractive dpkg --configure -a --force-confdef --force-confold   then:   DEBIAN_FRONTEND=noninteractive apt-get -s -f install   (read it first: a line that starts with Remv is a package that it would remove; if it is one that the plan does not name, or one that this text protects, do not go on and ask for help)   then:   DEBIAN_FRONTEND=noninteractive apt-get -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -f install"

// timersState is the ONE file where the state of the timers is kept, outside the folder of the plan: the plan id is a hash of the removals, the holds, the sources
// and Docker, so an owner who changes one of them has another plan and another folder, and the block 1 of the second would write the timers down as they are by
// then, stopped, and block 8 would start none. The first writer wins (the state of before the hop); block 8 removes the file when it ends.
const timersState = "/var/backups/casaos-os-upgrade-timers.state"

// timersBack is the command that starts the timers again, as they were, for whoever gives up the hop (the state file says which were running).
const timersBack = "systemctl start $(awk '$2 == \"active\" { print $1 }' " + timersState + ")   then:   rm -f " + timersState

// stopTimersNow is what the owner runs first after a restart in the middle of the hop: the timers are enabled units, and apt-daily may be at work (it holds the locks
// of apt for minutes). Each stop waits for its unit to end.
const stopTimersNow = "systemctl stop apt-daily.timer apt-daily-upgrade.timer apt-daily.service apt-daily-upgrade.service unattended-upgrades.service"

// logWords end the STOP lines of the blocks after the point of no return: what the terminal shows is a few hundred lines of thousands (screen keeps 100, tmux 2000, a
// dead terminal none). term.log holds what dpkg and the scripts of the packages printed, and only when apt runs dpkg through its pty (see shAptq): what dpkg prints when it is run by
// hand is on the terminal and nowhere else, and the STOP line of that command says so. apt keeps none of its own lines in a log (the E: line of a fetch that failed
// is on the terminal only), so the words tell the owner to copy the terminal too.
const logWords = "What dpkg and the scripts of the packages printed is in /var/log/apt/term.log; what apt did is in /var/log/apt/history.log, and dpkg's own steps are in /var/log/dpkg.log: send the last 100 lines of each. The lines of apt itself (the ones that start with E:) are on the terminal only: copy what the terminal shows too."

// logWordsUnit is logWords for the unit: apt runs there without a pty, so term.log holds only when it started and ended, and what dpkg and the scripts of the packages print, and the
// lines of apt itself, are in the log of the unit, in the folder of the run (the page shows the end of it).
const logWordsUnit = "What apt, dpkg and the scripts of the packages printed is in the log of the unit, $CASAOS_OSU_DIR/output.log (the page shows the end of it); what apt did is in /var/log/apt/history.log, and dpkg's own steps are in /var/log/dpkg.log: send the last 100 lines of each."

// logWords (method) is the words that end the STOP lines of the blocks after the point of no return, for the text that c writes.
func (c *rbCtx) logWords() string {
	if c.auto != nil {
		return logWordsUnit
	}
	return logWords
}

// dpkgWords is how the STOP line of a dpkg --configure -a that failed says where to read what it printed: dpkg run by apt is in the log of apt's pty, dpkg run by hand prints on the
// terminal, and in the unit it prints in the log of the unit.
func (c *rbCtx) dpkgWords() string {
	if c.auto != nil {
		return "dpkg could not configure what is half done: what it printed is above, in the log of the unit. "
	}
	return "dpkg could not configure what is half done: what it printed is above, on this terminal only (dpkg run by hand does not write term.log): copy it. "
}

// help (the method) defines the help variable of blocks 5 and 6 and block F: the words of the owner's text, or the unit's.
func (c *rbCtx) help() string {
	if c.auto != nil {
		return "help=\"The upgrade has begun: do not undo anything, ask for help. " + logWordsUnit + "\"\n"
	}
	return shHelp
}

func resumeMap(blocks []blockSpec) string {
	var b strings.Builder
	b.WriteString("\n# ==============================\n")
	b.WriteString("# IF YOU STOPPED: WHERE YOU ARE, AND WHAT TO DO\n")
	b.WriteString("# To see how far you got:   ls /var/backups/casaos-os-upgrade-<the plan id>/   (a done-<block> file is there for each block that ended)\n")
	b.WriteString("#\n")
	b.WriteString("#  You stopped in block      The machine is in this state                          What to do\n")
	b.WriteString("#  0, 1, 2                   Nothing has been changed (block 1 stopped timers).    Fix what the STOP line says and run the block again. To give up: start the timers again, as they were:   " + timersBack + "\n")
	b.WriteString("#  3                         /etc/apt may have been rewritten.                     Run:  sh /var/backups/casaos-os-upgrade-<plan id>/restore-sources.sh   (it puts the sources back, takes apt-orig aside and forgets blocks 3 and 4), then run block 3 again (pasted again with no restore, it stops and says so). To give up: do that, then start the timers again, as for blocks 0 to 2.\n")
	b.WriteString("#  4                         The same as after block 3, with packages downloaded.  The same as for block 3, timers included.\n")
	b.WriteString("#  0 or 3 of another plan    Another plan of this machine ran its block 3 (the folder of that plan in /var/backups has an apt-orig): the sources may be Debian 12's, and this plan cannot be made on them.   The STOP line names the restore script of that plan: run it with sh, then this block again. When that plan's upgrade has begun (begun-upgrade in its folder), only forward, as for block 5.\n")
	b.WriteString("#  a restart, blocks 3 to 6  The timers of apt are back (they are enabled units), and apt-daily may be at work.   First:   " + stopTimersNow + "   (each stop waits for its unit to end). Then run the block again, by its row: blocks 3, 5 and 6 stop the timers again, and stop while a package manager is at work.\n")
	b.WriteString("#  5, before CONFIRM         Nothing has begun: the machine is as after block 4.   Fix what the STOP line says (it also stops here when it is not run in tmux, in screen or on a console), and run block 5 again.\n")
	b.WriteString("#  5, apt failed at once     Nothing was installed (the block says so, and takes its marker back).  Fix what apt printed (a lock, no room, the network), and run block 5 again (type the confirmation again), or put the sources back: restore-sources.sh.\n")
	b.WriteString("#  5 after CONFIRM, 6        The upgrade has begun: Debian 11 and 12 are mixed.    Only forward: " + forwardCommands + "   then run the block again (block 5: type the confirmation again). Do not restore /etc/apt: from the moment block 5 writes begun-upgrade, restore-sources.sh and blocks 2, 3 and 4 refuse.\n")
	b.WriteString("#  5 or 6, cut off           A crash, a power cut, a dead terminal, a restart      First:   " + stopTimersNow + "   Then the same as the row above: only forward, with those commands (dpkg --configure -a mends a package left half unpacked). A restart may have started the machine on the new kernel already, or on the old one: run block 6 again on either (block 7 says which it is).\n")
	b.WriteString("#  7                         Debian 12 packages, the old kernel still runs.        Do what the STOP line says, run block 7 again, and do not restart the machine before it ends.\n")
	b.WriteString("#  restarted before 7 ended  The machine runs the new kernel, or the old one (the grub menu).   Run block 7 again. On the new kernel it says so, skips the checks that are for before a restart (the kernel, the initrd, grub), makes the ones that stay true after a boot (DNS, the clock, the iptables alternative, sshd), and goes on to block 8 only when they pass: a STOP there says what to fix, then run block 7 again. On the old one it runs all its checks and says to restart: restart again and choose the new kernel in the grub menu.\n")
	b.WriteString("#  after the restart         The machine is Debian 12, or does not start.          Block 8. If it does not start: the console, the rescue mode of the installer, or the snapshot.\n")
	b.WriteString("#  after block 8             The hop is over (done-after is there).                Nothing more is run from this text: every block stops. Update Docker from the dashboard, and look at your applications.\n")
	return b.String()
}

// ---- the pieces of shell that the blocks are made of -------------------------------------------------------------------

// shAptq defines aptq: apt-get with no question, the retries and the conffile choices of the hop, and the wait for the lock of dpkg. It does NOT set Dpkg::Use-Pty=0: with
// that option apt 2.2.4 does not open its pty, and /var/log/apt/term.log, which the STOP lines name, holds only its Log started and Log ended lines (run in a Debian 11
// chroot with a package whose postinst prints an error and fails: the text came out on the terminal and in no log). Without it term.log holds what the scripts of the
// packages printed. Blocks 5 and 6 run in a terminal (tmux, screen or a console) where a pty is there anyway.
const (
	shHead = "set -eu\nLC_ALL=C; export LC_ALL\nPATH=/usr/sbin:/usr/bin:/sbin:/bin; export PATH\n"
	shAptq = "export DEBIAN_FRONTEND=noninteractive APT_LISTCHANGES_FRONTEND=none NEEDRESTART_SUSPEND=1\naptq() { apt-get -y -q -o DPkg::Lock::Timeout=300 -o Acquire::Retries=3 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold \"$@\"; }\n"
	shStop = "{ echo \"%s\"; exit 1; }"

	// shSim are the functions that read a simulation of apt: the names it lists as removed (the REMOVED section is the authority, as in the
	// plan's parser), and the two counts of its summary line that are looked at, counted from the end of the line (apt adds "N downgraded,"
	// in the middle when there are downgrades).
	shSim = `removed_of() { awk '/^The following packages will be REMOVED:/ { s = 1; next } /^[^ ]/ { s = 0 } s { for (i = 1; i <= NF; i++) { sub(/\*$/, "", $i); print $i } }' "$1" | sort; }
removes_of() { awk '/^[0-9]+ upgraded, .* to remove and [0-9]+ not upgraded\.$/ { print $(NF - 6); exit }' "$1"; }
`

	// shHelp is what the STOP lines of blocks 5 and 6 end with: the upgrade has begun, nothing is undone, and the logs are where the whole text is.
	shHelp = "help=\"The upgrade has begun: do not undo anything, ask for help. " + logWords + "\"\n"

	// shDpkgState is what says whether dpkg did anything: the checksum of its status file, and the journal that dpkg leaves in updates/ when it is killed in the
	// middle of its work (the status file is not rewritten at every change).
	shDpkgState = "dpkgstate() { cksum < /var/lib/dpkg/status; ls -A /var/lib/dpkg/updates 2> /dev/null | sort; }\n"

	// shNotup is the count of "not upgraded" of the summary line: block 3 stops on any.
	shNotup = `notup_of() { awk '/^[0-9]+ upgraded, .* to remove and [0-9]+ not upgraded\.$/ { print $(NF - 2); exit }' "$1"; }
`

	// bkdir makes the backup directory, private to root, and makes it private when it is there already: it holds a copy of /etc. It is the
	// directory that is private, not the process (no umask for the whole block: dpkg and the scripts of the packages keep theirs).
	bkdir = "mkdir -p -m 700 \"$BK\"; chmod 700 \"$BK\"\n"

	// shSshdDir makes the directory that sshd -t and sshd -T ask for: ssh.service makes it (RuntimeDirectory) when it starts, and a box whose sshd is
	// installed and stopped has none, which is no reason for a stop.
	shSshdDir = "[ -d /run/sshd ] || mkdir -p -m 755 /run/sshd\n"

	// consoleTTY are the terminals of the machine's own console, of a serial line and of a hypervisor's console: nothing that a dropped connection can kill.
	consoleTTY = "/dev/tty[0-9]*|/dev/hvc*|/dev/ttyS*"
)

// timerUnits are the units that block 1 stops, and that blocks 3, 5 and 6 stop again.
const timerUnits = "apt-daily.timer apt-daily-upgrade.timer unattended-upgrades.service"

// shTimers stops the timers of apt and unattended-upgrades that run, and stops the block when one stays up. Block 1 does it first (after writing down
// their state); blocks 3, 5 and 6 do it again, because the timers are enabled units and a restart in the middle of the hop brings them back.
func shTimers() string { return shTimersOf(timerUnits) }

// shTimersState writes down the state of the timers the first time (TS is the file, timersState): block 1 of the hop does it before it stops them, and so does the first block of a
// refresh. startsThem is what starts them again as they were, "block 8" or the end of the refresh, which the STOP line of a disk that has no room for the file says.
func shTimersState(startsThem string) string {
	var b strings.Builder
	b.WriteString("if [ -e \"$TS\" ]; then\n")
	b.WriteString("  echo \"$TS is there from an earlier run of this block (by this plan or by another): the state of the timers that it holds is the one of before the hop, and it is kept.\"\n")
	b.WriteString("else\n")
	// the state is read into a name of this run's own and published with ln, which fails when the name is taken: the first run to publish wins, whole, and a run
	// that comes second adopts it (two runs that shared one name would write each other's lines and keep one line of three)
	b.WriteString("  ( umask 077; : > \"$TS.$$\" )\n")
	b.WriteString("  for unit in apt-daily.timer apt-daily-upgrade.timer unattended-upgrades.service; do\n")
	b.WriteString("    printf '%s %s\\n' \"$unit\" \"$(systemctl is-active \"$unit\" 2>/dev/null || true)\" >> \"$TS.$$\"\n")
	b.WriteString("  done\n")
	// ln fails when the name is taken, and it fails for other reasons too (a file system with no hard links, no room for the entry): the name is looked at
	// after it, and when nothing is there the state that was read is kept by a rename (whole, and never mixed with another run's lines: at worst the last of two
	// runs that both could not ln keeps its state), or the block stops with words of its own
	b.WriteString("  if ! ln \"$TS.$$\" \"$TS\" 2> /dev/null; then\n")
	b.WriteString("    if [ -e \"$TS\" ]; then\n")
	b.WriteString("      echo \"$TS was put in place by another run of this block while this one was reading the timers: the state of that run is the one that is kept.\"\n")
	b.WriteString("    else\n")
	b.WriteString("      echo \"ln could not put $TS in place (a file system with no hard links, or no room for one more entry): the state that this run wrote down is kept by a rename instead.\"\n")
	b.WriteString("      mv -f \"$TS.$$\" \"$TS\" || { rm -f \"$TS.$$\"; echo \"STOP: the state of the timers could not be put in $TS (is the disk full: df -h /var/backups): without it " + startsThem + " would start no timer. Free some room, then run this block again.\"; exit 1; }\n")
	b.WriteString("    fi\n")
	b.WriteString("  fi\n")
	b.WriteString("  rm -f \"$TS.$$\"\n")
	b.WriteString("fi\n")
	return b.String()
}

// shTimersBack starts the timers that were running when shTimersState wrote them down, and says so when there is no state to read: block 8 of the hop does it, and so does
// the last block of a refresh. It leaves the file where it is: the block removes it when it has done what it has to do after.
func shTimersBack() string {
	return "TS=" + timersState + "\nif [ -f \"$TS\" ]; then\n  while read -r unit state; do\n    if [ \"$state\" = active ]; then systemctl start \"$unit\"; fi\n  done < \"$TS\"\nelse\n  echo \"$TS is not there, so no timer of apt is started: if they were running before the hop, start them: systemctl start apt-daily.timer apt-daily-upgrade.timer\"\nfi\n"
}

// repairLadder is what a full upgrade does when its first dist-upgrade fails: the ladder of the release notes (configure what is half done, repair, the full upgrade again),
// with the repair and the full upgrade after it simulated and read before they are run, as the full upgrade was read before the first. Block 6 of the hop runs it, and so does the
// block of a refresh that upgrades. opts are added to every dist-upgrade of it and of its simulations (the refresh asks apt to include the phased updates; the hop adds none), and
// more, when it is not nil, is the shell of the check that a block adds after the guard of each simulation, for what only that block refuses (the refresh allows no removal at all).
func (c *rbCtx) repairLadder(opts string, more func(file string) string) string {
	after := func(file string) string {
		if more == nil {
			return ""
		}
		if s := more(file); s != "" {
			return indentBy(s, "  ")
		}
		return ""
	}
	var b strings.Builder
	b.WriteString("if ! aptq dist-upgrade" + opts + "; then\n")
	b.WriteString("  echo \"The dist-upgrade failed once: configuring what is half done, repairing, then again.\"\n")
	b.WriteString("  dpkg --configure -a --force-confdef --force-confold || " + stop(c.dpkgWords()+"Do not undo anything: ask for help. "+c.logWords()) + "\n")
	// the repair removes a package that has an unmet dependency without a stop, and the full upgrade after it may want something else than before: each is simulated
	// and read, as the full upgrade was before the first one
	b.WriteString("  aptq -s -f install > \"$BK/repair.txt\" 2>&1 || { echo \"STOP: apt cannot work out the repair: see $BK/repair.txt. $help\"; exit 1; }\n")
	b.WriteString(indentBy(checkSim("$BK/repair.txt", "$help"), "  "))
	b.WriteString("  guard_removals \"$BK/repair.txt\" \"the repair\"\n")
	b.WriteString(after("$BK/repair.txt"))
	b.WriteString("  aptq -f install || " + stop("apt could not repair the packages. Do not undo anything: ask for help. "+c.logWords()) + "\n")
	b.WriteString("  aptq -s dist-upgrade" + opts + " > \"$BK/after-repair.txt\" 2>&1 || { echo \"STOP: apt cannot work out the full upgrade after the repair: see $BK/after-repair.txt. $help\"; exit 1; }\n")
	b.WriteString(indentBy(checkSim("$BK/after-repair.txt", "$help"), "  "))
	b.WriteString("  guard_removals \"$BK/after-repair.txt\" \"the full upgrade after the repair\"\n")
	b.WriteString(after("$BK/after-repair.txt"))
	b.WriteString("  aptq dist-upgrade" + opts + " || " + stop("the full upgrade failed again after the repair. Do not undo anything: ask for help. "+c.logWords()) + "\n")
	b.WriteString("fi\n")
	return b.String()
}

// shTimersOf is shTimers for the units that it is given: the repair block of the unit stops the five units that the table at the end names.
func shTimersOf(units string) string {
	return shLines(
		"for unit in "+units+"; do",
		`  if [ "$(systemctl is-active "$unit" 2>/dev/null || true)" = active ]; then systemctl stop "$unit"; fi`,
		`  [ "$(systemctl is-active "$unit" 2>/dev/null || true)" != active ] || `+stop("$unit is still running: stop it (systemctl stop $unit), then run this block again."),
		"done",
	)
}

// beganCheck stops a block that is for the time before the upgrade, or that would put Debian 11's sources back, once block 5 has begun: from then on
// the way is forward. what says why this block cannot go on.
func beganCheck(what string) string {
	return `if [ -e "$BK/begun-upgrade" ] || [ -e "$BK/done-minimal" ]; then echo "STOP: the upgrade has begun (block 5 was started): ` + what + ` Do not restore anything. Only forward: ` + forwardWords + `"; exit 1; fi` + "\n"
}

// forwardWords are the words of the STOP lines that send the owner forward and never back (the exact commands are in the table at the end of the text).
const forwardWords = "finish with dpkg --configure -a, then apt-get -f install, then block 6 and the blocks after it (the table at the end of the runbook has the exact commands)."

// interruptedTail ends the two STOP lines of block 6 that apt's failure reaches before the repair ladder (the download and the check at the end), for the causes that are neither a lock
// nor the network: dpkg killed in the middle of its work (a file is left in /var/lib/dpkg/updates) and a dependency that is unmet fail every apt command but the simulations, and the
// ladder sits behind the dist-upgrade that cannot start, so the block run again ends the same way. apt itself suggests apt --fix-broken install, which removes a package that has an
// unmet dependency without a stop, protected ones included: the words send to the commands of the table, whose repair is simulated and read first.
const interruptedTail = "do not run what apt suggests (apt --fix-broken install removes packages without a stop). Only forward: " + forwardCommands + "   then run this block again."

// stop is a STOP line: it says why, and ends the block.
func stop(reason string) string {
	if strings.ContainsAny(reason, "\"\\\x60!") {
		panic("osplan: a reason that a double-quoted echo cannot carry: " + reason)
	}
	return fmt.Sprintf(shStop, "STOP: "+reason)
}

// busyCheck is psBusy and the STOP that follows it, for the blocks that run apt (3, 5 and 6): a package manager at work holds the locks, and after a restart it is
// often the daily job of apt, which the timers' stop did not end. then is what the block says of the state it leaves (nothing changed, or the upgrade has begun).
func busyCheck(then string) string {
	return psBusy + `[ -z "$busy" ] || { echo "STOP: a package manager is running (the process list says: $busy): wait until it ends (if it is the daily job of apt: ` + stopTimersNow + `), then run this block again. ` + then + `"; exit 1; }` + "\n"
}

// otherPlans is the look of blocks 0 and 3 at the folders that ANOTHER plan of this machine left in /var/backups (a plan has another id as soon as a removal, a hold
// or a source changes, and another folder with it). A plan whose block 3 ran rewrote the sources of apt, and a second plan can neither be made nor go on from them:
// the only way back is the restore script of the first, which this text names (while the upgrade has not begun there), or the words that only go forward (once it has).
// A plan whose block 8 ended is over, and it is not looked at here: head stops every block on it first (the hop is over for the machine). The folder of the plan itself is no other: the
// blocks that call this look at it first and stop on what it holds, so that it has none of the three marks here; the file of the timers' state (casaos-os-upgrade-timers.state) is no
// folder, and has none of them either, and a pattern that matches nothing is itself a name that has none.
func otherPlans() string {
	return shLines(
		`for d in /var/backups/casaos-os-upgrade-*; do`,
		`  if [ -e "$d/begun-upgrade" ] || [ -e "$d/done-minimal" ]; then`,
		`    echo "STOP: another plan of this machine has begun the upgrade of the packages ($d has begun-upgrade): Debian 11 and 12 are mixed there, and nothing in this text applies to it. Do not restore anything, and do not make another plan: finish that upgrade. Only forward: `+forwardCommands+`   then:   apt-get dist-upgrade until nothing is left, and restart. Or ask for help."`,
		`    exit 1`,
		`  fi`,
		`  if [ -e "$d/apt-orig" ]; then`,
		`    if [ -f "$d/restore-sources.sh" ]; then`,
		`      echo "STOP: block 3 of another plan of this machine began ($d/apt-orig is there), so the sources of apt may no longer be the ones that this plan was made from. Put them back first: sh $d/restore-sources.sh   (it puts the sources back as they were and takes apt-orig aside), then run this block again."`,
		`    else`,
		`      echo "STOP: block 3 of another plan of this machine began ($d/apt-orig is there), and $d/restore-sources.sh is not. apt-orig is the whole copy of /etc/apt that it made before it changed anything: compare it with /etc/apt, put back by hand what differs, rename apt-orig, then run this block again."`,
		`    fi`,
		`    exit 1`,
		`  fi`,
		`done`,
	)
}

// auditCheck stops the block when dpkg --audit says anything, or cannot say it: what it prints on its stdout (the packages that are half installed or half
// configured) and on its stderr (a database that it cannot read: it prints nothing on stdout then, and ends with status 2), and its exit status. fix is what to do.
func auditCheck(fix string) string {
	return `audit="$(dpkg --audit 2>&1)" && [ -z "$audit" ] || { echo "STOP: dpkg is not quiet: it has a package that is half installed or half configured, or it cannot open its own database. dpkg --audit says: ${audit:-nothing, but it ended with an error}. ` + fix + `"; exit 1; }` + "\n"
}

// shLines is the lines, ended by a newline.
func shLines(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

// indent is the lines of s, each moved in by four spaces (a piece of shell that goes inside an if inside a loop).
func indent(s string) string { return indentBy(s, "    ") }

// indentBy is the lines of s, each moved in by pad.
func indentBy(s, pad string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// head is what every block starts with: set -eu, English, a PATH, the backup directory, the checks that the hop is not over for this machine, and the check that the block
// before it ended.
//
// The hop is over when block 8 ended: no block runs again on a finished machine (block 1 would stop the timers and write their state again, block 5 would run apt, block 8
// would start them). It is over for the machine whichever plan ended it: a second plan has another folder, and its text, pasted from an older page, must not run on a machine that
// the first one took to its end. (Only one hop has a runbook; a second one would need its folders told apart from these.)
//
// It is over too when the machine is no longer Debian 11 and this plan did not begin the upgrade of its packages (neither begun-upgrade nor done-minimal is in its folder): another
// plan, or the owner by hand, took it on. When this plan has begun, the machine turns Debian 12 as base-files is upgraded, and the blocks from there must run. Block 0 has its own
// words for a machine that is not Debian 11.
func head(c *rbCtx, prev string) string {
	s := shHead + c.prologue() + "BK=" + c.backupFolder() + "\n"
	s += `[ ! -e "$BK/done-after" ] || { echo "STOP: the hop is over (block 8 ended: done-after is in $BK): there is nothing more to run in this text."; exit 1; }` + "\n"
	s += shLines(
		`for fin in /var/backups/casaos-os-upgrade-*; do`,
		`  [ ! -e "$fin/done-after" ] || { echo "STOP: the hop is over for this machine: the plan whose folder is $fin took it to the end (block 8 ended: done-after is there), and there is nothing more to run in this text."; exit 1; }`,
		`done`,
	)
	switch {
	case prev != "" && c.refresh != nil:
		// a refresh has no point of no return to tell another plan's text by, and no person to run the block before: the machine is the release it was started on, or it is not
		s += `grep -q '^VERSION_CODENAME=` + c.hop.From + `$' /etc/os-release || { echo "STOP: this machine is not Debian 11 (` + c.hop.From + `) any more: the refresh brings the release that the machine runs up to its last state, and this machine runs another."; exit 1; }` + "\n"
		s += `[ -e "$BK/done-` + prev + `" ] || { echo "STOP: the block before this one (` + prev + `) did not end: the refresh does not go on without it (ls $BK shows how far it got)."; exit 1; }` + "\n"
		s += bkdir
	case prev != "":
		s += `[ -e "$BK/begun-upgrade" ] || [ -e "$BK/done-minimal" ] || grep -q '^VERSION_CODENAME=` + c.hop.From + `$' /etc/os-release || { echo "STOP: the hop is over for this machine: it is not Debian 11 (` + c.hop.From + `) any more, and this plan did not begin the upgrade of its packages (neither begun-upgrade nor done-minimal is in $BK): another plan, or the owner by hand, took it on, and nothing in this text applies to it. If another plan of this machine has not ended (ls /var/backups), go on with that one."; exit 1; }` + "\n"
		s += `[ -e "$BK/done-` + prev + `" ] || { echo "STOP: the block before this one (` + prev + `) did not end: run it first (ls $BK shows how far you got)."; exit 1; }` + "\n"
		s += bkdir
	}
	return s
}

// done is what every block ends with: its marker. Block 0 makes the directory (the other blocks made it in head, before they wrote in it).
func done(id string, n int) string {
	s := fmt.Sprintf(": > \"$BK/done-%s\"\necho \"Block %d done.\"\n", id, n)
	if n == 0 {
		return bkdir + s
	}
	return s
}

// checkSim is the three checks of a simulation of apt that the block has put in file: that it has its summary line, that the names it lists as removed
// are as many as the summary says (a text that lists none under a summary that says some is not read as "nothing is removed"), and that it downgrades
// nothing (a pin over 1000 or a package from outside Debian: the plan blocks on it, and the -y of the hop stops at it). tail is what the STOP line
// says to do.
func checkSim(file, tail string) string {
	return shLines(
		fmt.Sprintf(`[ -n "$(removes_of "%[1]s")" ] || { echo "STOP: apt printed no summary line: see %[1]s. %[2]s"; exit 1; }`, file, tail),
		fmt.Sprintf(`[ "$(removed_of "%[1]s" | awk 'END { print NR }')" = "$(removes_of "%[1]s")" ] || { echo "STOP: the packages that apt lists as removed are not as many as its summary says: see %[1]s. %[2]s"; exit 1; }`, file, tail),
		fmt.Sprintf(`if grep -q '^The following packages will be DOWNGRADED:' "%[1]s" || grep -Eq ' [0-9]+ downgraded, ' "%[1]s"; then echo "STOP: apt would downgrade packages (a pin over 1000, a package on hold, a package from outside Debian): see %[1]s. %[2]s"; exit 1; fi`, file, tail),
	)
}

// shGuard defines guard_removals: it stops the block when the simulation in the file it is given would remove a protected package, or one that dpkg
// says is essential or required. The simulation is the one of the full upgrade, or of the repair when the second word says so, and the upgrade has begun when it
// is read: the words are forward-only. Block 5 calls it after the minimal upgrade, block 6 before each catch-up pass (the first full upgrade may have changed
// what the next one wants) and before each step of the repair ladder that can remove a package (apt-get -f install, and the full upgrade after it).
func (c *rbCtx) guard() string {
	return c.guardSaying("The upgrade has begun. DO NOT UNDO ANYTHING. Ask for help, with $1. ")
}

// shGuardSaying is shGuard with the words that a stop ends with, before the words of the logs: the refresh has not begun the upgrade, and puts the machine back.
func (c *rbCtx) guardSaying(words string) string {
	return shLines(
		`guard_removals() {`,
		`  what="${2:-the full upgrade}"`,
		`  for name in $(removed_of "$1"); do`,
		`    base="${name%%:*}"`,
		`    why=""`,
		`    case "$base" in`,
		`      `+ShellCase(),
		`        why="protected" ;;`,
		`      *) case "$(dpkg-query -W -f='${Essential} ${Priority}\n' "$base" 2> /dev/null || true)" in "yes "*|*" required") why="essential or required, as dpkg says" ;; esac ;;`,
		`    esac`,
		`    [ -z "$why" ] || { echo "STOP: $what would remove $name, which is $why. `+words+c.logWords()+`"; exit 1; }`,
		`  done`,
		`}`,
	)
}

// muxAtHand is block 0's check, made before anything is changed: a machine that has no console here and neither tmux nor screen cannot run blocks 5 and 6,
// and the time to install one is now, while the sources are still Debian 11's (after block 3 a package installed by hand upgrades libc6 with it).
func muxAtHand() string {
	return shLines(
		`if [ -z "${TMUX:-}${STY:-}" ]; then`,
		`  case "$(tty 2> /dev/null || true)" in`,
		`    `+consoleTTY+`) ;;`,
		`    *) command -v tmux > /dev/null 2>&1 || command -v screen > /dev/null 2>&1 || `+stop("there is no console here, and neither tmux nor screen is installed: blocks 5 and 6 need one, and after block 3 it is too late to install it (a package installed from Debian 12's sources upgrades libc6 with it, outside block 5's protection). Install it now, while this is still Debian 11:  apt-get install tmux   then run this block again.")+` ;;`,
		`  esac`,
		`fi`,
	)
}

// bootChecks are the refusals of the readiness report that bite after the point of no return, and that nothing else of block 0 looks at: no room in /boot
// (300 MiB, as the report asks), a UEFI machine whose EFI system partition is not mounted or is nearly full (16 MiB), and a grub-pc with no disk to be written to
// (its postinst fails without a terminal). The report's other refusals (the loader, the file systems) are the report's: the text says to read it first. avail is
// defined by block 0 before them.
func bootChecks() string {
	return shLines(
		`isinst() { case "$(dpkg-query -W -f='${db:Status-Abbrev}' "$1" 2> /dev/null || true)" in ii*) return 0 ;; esac; return 1; }`,
		`[ "$(avail /boot)" -ge 307200 ] || `+stop("/boot has less than 300 MiB free: the new kernel and its initrd are written there, and dpkg fails in the middle of block 6, after the point of no return, when there is no room. Free some room there (a kernel that you do not use any more, for example), then run this block again."),
		`if [ -d /sys/firmware/efi ]; then`,
		`  grep -q ' /boot/efi ' /proc/mounts || `+stop("this machine starts with UEFI and the EFI system partition is not mounted on /boot/efi: GRUB would be written in the wrong place, or not at all. Mount it (see /etc/fstab), then run this block again."),
		`  [ "$(avail /boot/efi)" -ge 16384 ] || `+stop("/boot/efi has less than 16 MiB free: GRUB's files are written there again. Free some room, then run this block again."),
		`elif isinst grub-pc; then`,
		`  devices="$(echo get grub-pc/install_devices | debconf-communicate 2> /dev/null | awk 'NR == 1 { $1 = ""; gsub(/[ ,]/, ""); print }' || true)"`,
		`  if [ -z "$devices" ]; then`,
		`    [ "$(echo get grub-pc/install_devices_empty | debconf-communicate 2> /dev/null | awk 'NR == 1 { print $2 }' || true)" = true ] || `+stop("grub-pc has no disk to be installed on (grub-pc/install_devices is empty), and its update ends in error in block 6, where there is no terminal to ask it. Set it first: dpkg-reconfigure grub-pc   then run this block again."),
		`  fi`,
		`fi`,
	)
}

// survive is the check that the terminal outlives a dropped connection: the console, or tmux, or screen. sudo and su drop TMUX and STY from the
// environment, which is why the text says to become root first and start the multiplexer from the root shell. When neither tmux nor screen is installed
// the answer is not to install one now: before the upgrade has begun the sources can go back and tmux be installed on Debian 11; after, the console.
func survive() string {
	return shLines(
		`if [ -z "${TMUX:-}${STY:-}" ]; then`,
		`  case "$(tty 2> /dev/null || true)" in`,
		`    `+consoleTTY+`) ;;`,
		`    *)`,
		`      if command -v tmux > /dev/null 2>&1 || command -v screen > /dev/null 2>&1; then`,
		`        how="Become root first (su - or sudo -i), then start tmux from the root shell:  tmux new-session -s casaos-upgrade   (or: screen -S casaos-upgrade)  and paste this block again inside it. After a dropped connection: log in, become root, tmux attach -t casaos-upgrade   (or: screen -r casaos-upgrade)."`,
		`      elif [ -e "$BK/begun-upgrade" ]; then`,
		`        how="Neither tmux nor screen is installed, and the upgrade has begun: do not install anything by hand now, and do not restore the sources. Paste this block on the machine's own console."`,
		`      else`,
		`        how="Neither tmux nor screen is installed, and the sources are Debian 12's since block 3: a package installed from them upgrades libc6 with it, outside this block's protection. Paste this block on the machine's own console, or go back while nothing has begun: sh $BK/restore-sources.sh   then   apt-get install tmux   (Debian 11 again), redo blocks 3 and 4, and run this block in tmux."`,
		`      fi`,
		`      `+stop("this block must not run in a terminal that dies with the connection: a dropped connection would kill apt in the middle of the upgrade and leave Debian 11 and 12 mixed. $how On the machine's own console, or a serial or hypervisor console, this check passes by itself.")+` ;;`,
		`  esac`,
		`fi`,
	)
}

// sourcesHash is the shell that prints the hash of the sources of the box, the way the plan hashed them (HashCurrent): for
// each file apt reads, its name, its size and its bytes, each ended by a NUL. A name that has a newline in it is no name that apt
// or the plan's regexp reads (the regexp is matched per line by grep, so the shell has to leave such a name out by itself).
const sourcesHash = `srchash() {
  nl="$(printf '\nx')"; nl="${nl%x}"
  ( cd /etc/apt
  for f in sources.list sources.list.d/*; do
    [ -f "$f" ] || continue
    case "$f" in
      sources.list) ;;
      sources.list.d/*)
        name="${f#sources.list.d/}"
        case "$name" in *"$nl"*) continue ;; esac
        printf '%s\n' "$name" | grep -Eq '^[A-Za-z0-9_:-][A-Za-z0-9_:.-]*\.(list|sources)$' || continue ;;
    esac
    n="$(wc -c < "$f")"
    printf '%s\0%s\0' "$f" "$((n))"
    cat "$f"
    printf '\0'
  done ) | sha256sum | awk '{ print $1 }'
}
`

// restoreScript is what puts the sources back as they were before block 3, and takes blocks 3 and 4 back as undone: their markers are removed
// and the copy of /etc/apt is put aside, so that block 3 can be run again.
const restoreScript = `#!/bin/sh
# Puts /etc/apt/sources.list and sources.list.d back as they were before block 3 of the runbook, and forgets blocks 3 and 4 (block 3 can be run again).
# The copy of /etc/apt that block 3 made is in apt-orig, next to this file; it is put aside (apt-orig.restored.<number>) when the sources are back.
set -eu
LC_ALL=C; export LC_ALL
PATH=/usr/sbin:/usr/bin:/sbin:/bin; export PATH
case "$0" in */*) HERE="${0%/*}" ;; *) HERE=. ;; esac
APT_ETC="${APT_ETC:-/etc/apt}"
if [ -e "$HERE/begun-upgrade" ] || [ -e "$HERE/done-minimal" ]; then echo "STOP: the upgrade has begun (block 5 was started): putting Debian 11's sources back now would leave Debian 12 packages on Debian 11 sources. Do not restore anything. Only forward: ` + forwardWords + `"; exit 1; fi
[ -d "$HERE/apt-orig" ] || { echo "STOP: $HERE/apt-orig is not there: nothing to restore from."; exit 1; }
rm -f "$APT_ETC"/sources.list.d/*.list "$APT_ETC"/sources.list.d/*.sources "$APT_ETC"/sources.list.d/*.casaos-paused "$APT_ETC"/sources.list.d/*.casaos-new "$APT_ETC/sources.list.casaos-new"
if [ -e "$HERE/apt-orig/sources.list" ]; then cp -a "$HERE/apt-orig/sources.list" "$APT_ETC/sources.list"; else rm -f "$APT_ETC/sources.list"; fi
if [ -d "$HERE/apt-orig/sources.list.d" ]; then cp -a "$HERE/apt-orig/sources.list.d/." "$APT_ETC/sources.list.d/"; fi
rm -f "$HERE/done-sources" "$HERE/done-download"
mv "$HERE/apt-orig" "$HERE/apt-orig.restored.$$"
echo "The sources are back as they were. Block 3 can be run again."
TS="${CASAOS_TIMERS_STATE:-/var/backups/casaos-os-upgrade-timers.state}"
if [ -s "$TS" ]; then
  units="$(awk '$2 == "active" { print $1 }' "$TS" | tr '\n' ' ')"
  if [ -n "$units" ]; then echo "The timers of apt that block 1 stopped are still stopped (block 3 stops them again when it runs). To give up the hop, start them again, as they were:  systemctl start $units  then:  rm -f $TS"; fi
fi
if [ -z "${CASAOS_NO_UPDATE:-}" ]; then apt-get update || echo "apt-get update did not go through (apt-get update does not wait for a lock: another package manager may hold it): run it again when it is free or the network is back."; fi
`

// restoreScriptHash is the hash of the restore script as the block writes it, which the block checks after writing it.
func restoreScriptHash() string { return restoreScriptHashOf(restoreScript) }

func restoreScriptHashOf(script string) string {
	sum := sha256.Sum256([]byte(script))
	return hex.EncodeToString(sum[:])
}

// restoreScriptRefresh is the restore script that the refresh writes: the owner's, but for the two things it says that are about a hop and are untrue of a refresh, whose last block
// starts the timers itself and which has no block 3 to run again. The unit runs it with its log in the page, and a refresh that ended well would show a line that sends the owner to
// give up a hop. It is the only difference, and a test holds it to that.
var restoreScriptRefresh = refreshRestoreScript(restoreScript)

func refreshRestoreScript(script string) string {
	const again = ` Block 3 can be run again.`
	i := strings.Index(script, "TS=\"${CASAOS_TIMERS_STATE:-")
	j := strings.Index(script, `if [ -z "${CASAOS_NO_UPDATE:-}" ]`)
	if !strings.Contains(script, again) || i < 0 || j < i {
		panic("osplan: the restore script is not the one that the refresh's is made from")
	}
	script = script[:i] + script[j:]
	script = strings.Replace(script, again, "", 1)
	script = strings.Replace(script, "# Puts /etc/apt/sources.list and sources.list.d back as they were before block 3 of the runbook, and forgets blocks 3 and 4 (block 3 can be run again).", "# Puts /etc/apt/sources.list and sources.list.d back as they were before the refresh wrote its own.", 1)
	script = strings.Replace(script, "# The copy of /etc/apt that block 3 made is in apt-orig,", "# The copy of /etc/apt that the refresh made is in apt-orig,", 1)
	return script
}

// restoreText is the restore script that the text of c writes: the refresh's own for a refresh.
func (c *rbCtx) restoreText() string {
	if c.refresh != nil {
		return restoreScriptRefresh
	}
	return restoreScript
}

// shSourcesBackup is what a block does before it changes a file of /etc/apt: it writes the restore script and reads it back, and copies /etc/apt whole (or not at all). Block 3
// of the hop does it, and so does the block of a refresh that writes the sources. again is what the stops of the script say to do (run the block again, or start the refresh again).
func (c *rbCtx) sourcesBackup(again string) string {
	// the restore script is written and checked BEFORE the copy of /etc/apt, and the copy is made under another name and renamed when it is whole: the
	// state "apt-orig is there" means that the script is there and the copy is complete (a copy that died half way, found by a restore script of an
	// earlier cycle, would be taken for the truth and delete the sources)
	var b strings.Builder
	script := c.restoreText()
	b.WriteString("cat > \"$BK/restore-sources.sh\" <<'" + heredocEnd + "'\n" + script + heredocEnd + "\n")
	b.WriteString("sh -n \"$BK/restore-sources.sh\" || " + stop("the restore script was damaged on its way here: it is not shell. Nothing is changed in /etc/apt. "+again) + "\n")
	fmt.Fprintf(&b, "[ \"$(sha256sum < \"$BK/restore-sources.sh\" | awk '{ print $1 }')\" = %s ] || %s\n", shQ(restoreScriptHashOf(script)), stop("the restore script is not the one of this text: it was changed on its way here. Nothing is changed in /etc/apt. "+again))
	b.WriteString("rm -rf \"$BK/apt-orig.new\"\n")
	b.WriteString("cp -a \"$APT_ETC\" \"$BK/apt-orig.new\"\n")
	b.WriteString("mv -f \"$BK/apt-orig.new\" \"$BK/apt-orig\"\n")
	return b.String()
}

// treeDest is where the file of the tree goes on the box, under APT_ETC.
func treeDest(f TreeFile) string {
	if name, ok := strings.CutPrefix(f.Path, "parts/"); ok {
		return "$APT_ETC/sources.list.d/" + name
	}
	return "$APT_ETC/sources.list"
}

// treeWrite writes the files of the tree under new names, from quoted heredocs, and puts nothing in place. They are written with umask 022 (in a
// subshell: nothing else in the block changes its umask), so that the files of /etc/apt are 644 as they were, whatever the umask of the shell that
// the text was pasted into (a hardened root shell has 027 or 077, which the restore script's cp -a would then disagree with).
func treeWrite(c *rbCtx) string {
	var b strings.Builder
	b.WriteString("(\numask 022\n")
	for _, f := range c.plan.Sources.Tree {
		fmt.Fprintf(&b, "cat > \"%s.casaos-new\" <<'%s'\n", treeDest(f), heredocEnd)
		b.WriteString(f.Content)
		fmt.Fprintf(&b, "%s\n", heredocEnd)
	}
	b.WriteString(")\n")
	return b.String()
}

// treeHash is the shell that puts in the variable v the hash that the plan made of the tree (the path, the size and the bytes of each file, sorted by
// path, each ended by a NUL; the paths of the plan: "parts/NAME" and "sources.list"), made of the files that are at the places of the tree with suffix
// added to their names: ".casaos-new" before they are moved, "" once they are in place.
func treeHash(c *rbCtx, v, suffix string) string {
	var b strings.Builder
	b.WriteString(v + "=\"$({ ")
	for _, f := range c.plan.Sources.Tree {
		fmt.Fprintf(&b, "gh %s \"%s%s\"; ", shQ(f.Path), treeDest(f), suffix)
	}
	b.WriteString("} | sha256sum | awk '{ print $1 }')\"\n")
	return b.String()
}

// treeCheck reads what treeWrite wrote and stops when it is not the plan's. A console that retypes what it is given, a history expansion, a line that
// was wrapped: none of them is seen by the comparison of the names of the removals.
func treeCheck(c *rbCtx) string {
	var b strings.Builder
	b.WriteString(`gh() { n="$(wc -c < "$2")"; printf '%s\0%s\0' "$1" "$((n))"; cat "$2"; printf '\0'; }` + "\n")
	b.WriteString(treeHash(c, "written", ".casaos-new"))
	fmt.Fprintf(&b, "echo \"The plan's hash of the sources it wrote for apt: %s\"\n", c.plan.Sources.HashGenerated)
	fmt.Fprintf(&b, "[ \"$written\" = %s ] || { echo \"STOP: the files written for apt are not the plan's: their hash is $written. The text was changed on its way here (a console that retypes what it is given can do that, and so can a bash without bracketed paste, which takes a TAB in a comment line of the sources for completion: type sh first, then paste this block again). Nothing is in place yet: sh $BK/restore-sources.sh   then run this block again, or ask for a new plan.\"; exit 1; }\n", shQ(c.plan.Sources.HashGenerated))
	return b.String()
}

// treeMoves puts aside the files that are paused, and puts the files of the tree in place. mv -f: an alias mv='mv -i' of the shell that the text was
// pasted into would ask before it replaces a file, take the next pasted line for the answer and replace nothing.
func treeMoves(c *rbCtx) string {
	var b strings.Builder
	for _, pa := range c.plan.Paused {
		if pa.Line == 0 {
			fmt.Fprintf(&b, "mv -f \"$APT_ETC/sources.list.d/%s\" \"$APT_ETC/sources.list.d/%s.casaos-paused\"\n", pa.File, pa.File)
		}
	}
	for _, f := range c.plan.Sources.Tree {
		fmt.Fprintf(&b, "mv -f \"%s.casaos-new\" \"%s\"\n", treeDest(f), treeDest(f))
	}
	return b.String()
}

// treeVerify reads the files back from where they now are, and compares their hash with the plan's again: a mv that did not replace, whatever the
// reason, is seen here and not by the comparison of the names of the removals (a plan that removes nothing passes that).
func treeVerify(c *rbCtx) string {
	var b strings.Builder
	b.WriteString(treeHash(c, "placed", ""))
	fmt.Fprintf(&b, "[ \"$placed\" = %s ] || { echo \"STOP: the files now in /etc/apt are not the ones that were written: their hash is $placed. A mv that asks a question instead of replacing (an alias such as mv -i) leaves the old file. The sources may be half in place: sh $BK/restore-sources.sh   puts them back; then run this block again, in a plain sh.\"; exit 1; }\n", shQ(c.plan.Sources.HashGenerated))
	return b.String()
}

// treeFiles is the four of them in a row: what the test of the heredocs and the one of the hostile lines run. The check of the hash is part of it:
// the files that were written are the plan's, or nothing moves; and the files that are in place are the ones that were written, or the block stops.
func treeFiles(c *rbCtx) string {
	return treeWrite(c) + treeCheck(c) + treeMoves(c) + treeVerify(c)
}

// kib is a size in kibibytes, rounded up.
func kib(n uint64) uint64 { return (n + 1023) / 1024 }

// psBusy is the question "is a package manager at work", asked of the processes: the transaction processes by name (comm exactly: not the
// names that merely start with apt, such as apt-cacher-ng); dpkg only when its arguments hold an option that changes the packages (the identity
// gates of the core run dpkg --print-architecture, and dpkg --audit or -l read and change nothing); and a run of unattended-upgrade as the command
// itself, or as the script of a python (not its idle helper unattended-upgrade-shutdown, which runs on every box that has the package, and not an
// editor that has the file in its arguments). What it saw is in $busy, as the line of the process list, for the STOP to say. ps is run on its own,
// so that a box without it stops (a pipeline would take the status of awk, and a missing ps would read as "nothing is running"). It does not see the
// locks of apt and dpkg: they are fcntl locks, which flock(1) cannot see. The apt-get commands of this text that check, download or install wait for the
// dpkg lock (DPkg::Lock::Timeout) and fail if it stays held; apt-get update waits for no lock, and fails at once when another holds the lock of the lists
// (measured with apt 2.2.4).
const psBusy = `psout="$(ps -eo comm=,args= 2> /dev/null)" || { echo "STOP: ps did not answer, so the processes cannot be looked at for a package manager. If ps is not installed: apt-get install procps   then run this block again."; exit 1; }
busy="$(printf '%s\n' "$psout" | awk '
$1 == "apt" || $1 == "apt-get" || $1 == "aptitude" || $1 == "apt.systemd.dai" { $1 = $1; print substr($0, 1, 120); exit }
$1 == "dpkg" { for (i = 2; i <= NF; i++) if ($i ~ /^(-i|-r|-P|--install|--configure|--unpack|--remove|--purge|--triggers-only)$/) { $1 = $1; print substr($0, 1, 120); exit } }
$2 ~ /\/unattended-upgrade$/ || ($2 ~ /(^|\/)python[0-9.]*$/ && $3 ~ /\/unattended-upgrade$/) { $1 = $1; print substr($0, 1, 120); exit }
' | tr -cd '\040-\176')"
`

// roomWords end the STOP lines of block 0 that say a directory has no room: what to do about it.
const roomWords = " Free some room there (apt-get clean empties the cache of downloaded packages; remove what you do not need), then run this block again."

// verifyGate is what block 0 says of the state of its own folder: it is for before block 3, and a folder that has the marks of a later block is sent to the table. The
// sources of apt MAY no longer be the plan's once apt-orig is there (the first of the files that block 3 moves may not have been moved yet): that is no reason to say
// they are not.
func verifyGate() string {
	return shLines(
		`if [ -e "$BK/begun-upgrade" ] || [ -e "$BK/done-minimal" ]; then`,
		`  echo "STOP: the upgrade has begun (block 5 was started): this block is for before it. Do not restore anything. Only forward: `+forwardWords+`"`,
		`  exit 1`,
		`fi`,
		`if [ -e "$BK/apt-orig" ]; then`,
		`  if [ -e "$BK/done-sources" ]; then`,
		`    echo "STOP: block 3 has begun and ended ($BK/done-sources is there): the sources of apt are Debian 12's now, and this block is for before it. The next is block 4. To start again from block 3, put the sources back first: sh $BK/restore-sources.sh   (it forgets blocks 3 and 4)."`,
		`  else`,
		`    echo "STOP: block 3 has begun and did not end ($BK/apt-orig is there): the sources of apt may no longer be the ones that the plan was made from, and this block is for before it. Look at the table at the end of this text: sh $BK/restore-sources.sh puts the sources back (when that file is there), and block 3 can then be run again."`,
		`  fi`,
		`  exit 1`,
		`fi`,
	)
}

// debian11to12Blocks are the blocks of the hop from Debian 11 to 12: block 0 to 8.
var debian11to12Blocks = []blockSpec{
	{
		RunbookBlock: RunbookBlock{ID: "verify", Title: "check that this is the machine of the plan, and that it is ready"},
		prose: []string{
			"Nothing is changed here. Type the name of this machine between the quotes of TYPED_HOSTNAME, so that you have looked at which machine this is (the plan holds no identity of the machine: this guards the terminal, nothing more).",
			"It also checks that the plan is still good (24 hours), that the sources of apt are the ones the plan was made from, that dpkg is quiet, that there is room (for the packages, in /usr and /, in /boot, on the EFI partition",
			"or the disk of grub-pc where the machine has them), that the tools the next blocks use are there (ps, ss, and curl when Docker's source is put aside), and that blocks 5 and 6 will have a terminal that survives a",
			"dropped connection: a console, or tmux or screen installed. If none is, this is the time to install one (apt-get install tmux): after block 3 the machine takes its packages from Debian 12.",
			"It does NOT run the readiness report of the dashboard: read that first, and do not start when it says that it refuses. It prints a WARNING, which is not a stop, when dkms has modules built from source.",
			"It looks at the processes of the package managers; it does not see their file locks. The apt-get commands of the blocks that check, download or install wait up to 5 minutes for the dpkg lock and stop if it stays",
			"held; apt-get update waits for no lock, and fails at once when another package manager holds the lock of the lists.",
			"Run it before block 3: once block 3 has begun, the sources of apt may no longer be the plan's, and this block says so. It also looks at the folders that another plan of this machine left in",
			"/var/backups: when that plan's block 3 ran, the sources are its own, and this block names the restore script of that plan (or, once its upgrade has begun, the words that only go forward).",
		},
		body: func(c *rbCtx, _ string) string {
			var b strings.Builder
			b.WriteString(head(c, ""))
			b.WriteString(c.aptq())
			b.WriteString(c.typedHostname())
			b.WriteString("echo \"Block 0: is this the machine of the plan? Nothing is changed.\"\n")
			b.WriteString(`[ "$(id -u)" = 0 ] || ` + stop("run this as root.") + "\n")
			b.WriteString(verifyGate())
			b.WriteString(otherPlans())
			b.WriteString(`[ -n "$TYPED_HOSTNAME" ] && [ "$TYPED_HOSTNAME" = "$(hostname)" ] || { echo "STOP: this machine is called $(hostname). Type that name between the quotes of TYPED_HOSTNAME at the top of this block, to show that you are on the machine you mean to upgrade."; exit 1; }` + "\n")
			b.WriteString(`[ "$(date +%s)" -lt ` + strconv.FormatInt(c.plan.ExpiresAt.Unix(), 10) + ` ] || ` + stop("this plan is older than its 24 hours (it was good until "+c.plan.ExpiresAt.UTC().Format("2006-01-02 15:04")+" UTC): the machine may have changed. Ask for a new plan.") + "\n")
			b.WriteString(`grep -q '^VERSION_CODENAME=` + c.hop.From + `$' /etc/os-release || ` + stop("this machine is not Debian 11 ("+c.hop.From+") any more: the plan does not apply. Ask for a new one.") + "\n")
			b.WriteString(auditCheck("Run: dpkg --configure -a   then ask for a new plan. If dpkg cannot open its own database, ask for help."))
			b.WriteString("command -v ss > /dev/null 2>&1 || " + stop("ss is not installed: blocks 2 and 8 use it to write down the ports that listen. Install it: apt-get install iproute2   then run this block again.") + "\n")
			for _, pa := range c.plan.Paused {
				if pa.Docker {
					b.WriteString("command -v curl > /dev/null 2>&1 || " + stop("curl is not installed: block 8 uses it to ask whether Docker publishes its repository for "+c.hop.To+". Install it: apt-get install curl   then run this block again.") + "\n")
					break
				}
			}
			b.WriteString(c.muxAtHand())
			b.WriteString(psBusy)
			b.WriteString(`[ -z "$busy" ] || { echo "STOP: a package manager is running (the process list says: $busy): wait until it ends. This check looks at the processes, not at the file locks of apt and dpkg: the apt-get commands of this text that check, download or install wait up to 5 minutes for the dpkg lock and stop if it stays held, and apt-get update does not wait for the lock of the lists."; exit 1; }` + "\n")
			b.WriteString("aptq check > /dev/null || " + stop("apt-get check finds a broken dependency, or could not get the lock of dpkg in 5 minutes (what it printed above says which): repair it, or wait for the other package manager, then run this block again. If it is a broken dependency, ask for a new plan once it is repaired.") + "\n")
			b.WriteString("avail() { df -Pk \"$1\" | awk 'NR == 2 { print $4 }'; }\n")
			if len(c.plan.Disk) > 0 {
				for _, row := range c.plan.Disk {
					fmt.Fprintf(&b, "[ \"$(avail %s)\" -ge %d ] || %s\n", shQ(row.Path), kib(row.Need), stop(fmt.Sprintf("%s needs %d MB free for this plan, and has less."+roomWords, row.Path, kib(row.Need)/1024+1)))
				}
			} else {
				b.WriteString("echo \"The plan has no sizes from apt: only the coarse floors are checked (1.5 GB for the packages, 2 GB for the system).\"\n")
				fmt.Fprintf(&b, "[ \"$(avail /var/cache/apt/archives)\" -ge %d ] || %s\n", 1536*1024, stop("/var/cache/apt/archives has less than 1.5 GB free."+roomWords))
				fmt.Fprintf(&b, "[ \"$(avail /)\" -ge %d ] || %s\n", 2048*1024, stop("/ has less than 2 GB free."+roomWords))
			}
			b.WriteString(bootChecks())
			b.WriteString("# the sources of apt are the ones the plan was made from\n")
			b.WriteString(sourcesHash + "now=\"$(srchash)\"\n")
			fmt.Fprintf(&b, "[ \"$now\" = %s ] || %s\n", shQ(c.plan.Sources.HashCurrent), stop("the sources of apt are not the ones the plan was made from: something changed them. Ask for a new plan."))
			b.WriteString(`if command -v dkms > /dev/null 2>&1 && [ -n "$(dkms status 2> /dev/null || true)" ]; then echo "WARNING: dkms has modules built from source on this machine (dkms status lists them). Block 6 builds them again for the new kernel, and a driver that does not build for it is a driver that is missing after the restart (a network card, a disk controller). This is NOT a stop: check by hand that each one supports the kernel of ` + c.hop.To + ` before you go on."; fi` + "\n")
			b.WriteString(done("verify", 0))
			return b.String()
		},
	},
	{
		RunbookBlock: RunbookBlock{ID: "timers", Title: "stop the timers of apt and unattended-upgrades"},
		prose: []string{
			"They would start an update in the middle of the hop. Their state is kept in a file the first time this block runs, and block 8 puts back the ones that were running and then removes the file.",
			"The file is written under a name of its own and put in place all at once (ln; a rename where ln cannot): two runs at the same time keep the state of the one that put it in place first, and none loses a line.",
			"The file is " + timersState + ", outside the folder of the plan on purpose: the plan has another id (and another folder) as soon as you change a removal, a hold or a source, and the second block 1 would",
			"find the timers stopped already. Run again, by this plan or by another, the block keeps that first state: the timers it stopped are not written down as stopped. To give up the hop, start them again:",
			timersBack,
			"They are enabled units, so a restart in the middle of the hop brings them back: blocks 3, 5 and 6 stop them again when they begin.",
		},
		body: func(c *rbCtx, prev string) string {
			var b strings.Builder
			b.WriteString(head(c, prev))
			b.WriteString("TS=" + timersState + "\n")
			b.WriteString("echo \"Block 1: stopping the timers of apt.\"\n")
			b.WriteString(shTimersState("block 8"))
			b.WriteString(c.timersStopping())
			b.WriteString(shTimers())
			b.WriteString(done("timers", 1))
			return b.String()
		},
	},
	{
		RunbookBlock: RunbookBlock{ID: "snapshot", Title: "write down the state of the machine"},
		prose: []string{
			"A copy of what describes the machine now, in /var/backups/casaos-os-upgrade-<plan id> (private to root, mode 700; the copy of /etc in it is mode 600):",
			"the packages and their holds, /etc, the ports that listen, the settings of sshd, the iptables alternative, the units that fail now, the ReCasaOS units, the containers and their restart policy.",
			"It is not a backup of your data. Block 7 compares the settings of sshd, the iptables alternative and the kernel that runs with what is written down here; block 8 compares the ports that listen and the units that fail,",
			"and shows the ReCasaOS units and the containers beside the lists of before (it does not compare those two: you look).",
			"The lists of packages (selections.txt, packages.tsv) and the copy of /etc are for you to look at by hand: nothing in this text compares them.",
			"Run again after block 3 has begun, it stops: it would replace the state of the machine as it was with the state of now.",
		},
		body: func(c *rbCtx, prev string) string {
			var b strings.Builder
			b.WriteString(head(c, prev))
			b.WriteString(beganCheck("this block would replace the state of before the upgrade, which blocks 7 and 8 compare with, by the state of now."))
			b.WriteString("for m in sources download minimal full prereboot after; do\n")
			b.WriteString("  [ ! -e \"$BK/done-$m\" ] || " + stop("a later block has ended (done-$m is in $BK): running this one again would replace the state of the machine as it was with the state of now, and blocks 7 and 8 compare with the first. Do not run it again.") + "\n")
			b.WriteString("done\n")
			b.WriteString("[ ! -e \"$BK/apt-orig\" ] || " + stop("block 3 has begun (apt-orig is in $BK): the sources may not be the ones of before. If they are not, run sh $BK/restore-sources.sh first, then this block.") + "\n")
			b.WriteString("echo \"Block 2: writing down the state of the machine in $BK.\"\n")
			b.WriteString("dpkg --get-selections > \"$BK/selections.txt\"\n")
			b.WriteString("dpkg-query -W -f='${db:Status-Abbrev}\\t${Package}\\t${Version}\\n' > \"$BK/packages.tsv\"\n")
			b.WriteString("awk -F'\\t' '$1 ~ /^h/ { print $2 }' \"$BK/packages.tsv\" > \"$BK/holds.txt\"\n")
			b.WriteString("[ ! -e /var/lib/apt/extended_states ] || cp -p /var/lib/apt/extended_states \"$BK/extended_states\"\n")
			// tar exits 1 when a file changed while it was read (a lease, resolv.conf rewritten by DHCP): the archive is made and good but for that file; any
			// other failure is a stop, said in words (under set -e it would end the block with only the message of tar)
			b.WriteString("rc=0\n( umask 077; tar -C / -czf \"$BK/etc.tar.gz\" etc ) || rc=$?\n")
			b.WriteString("case \"$rc\" in\n  0) ;;\n  1) echo \"tar says that a file changed while it archived it (a lease, resolv.conf: it happens): the copy of /etc is made, and good but for that file.\" ;;\n  *) " + stop("tar could not make the copy of /etc (exit $rc): is the disk full? See what it printed above, then run this block again.") + " ;;\nesac\n")
			b.WriteString("ss -Hltn | awk '{ print $4 }' | sort > \"$BK/listening.txt\"\n")
			b.WriteString(": > \"$BK/sshd-keys.txt\"\n")
			b.WriteString("if command -v sshd > /dev/null 2>&1; then\n  " + shSshdDir + "  sshd -T 2> /dev/null | grep -E '^(port|listenaddress|permitrootlogin|passwordauthentication|pubkeyacceptedkeytypes|pubkeyacceptedalgorithms) ' | sort > \"$BK/sshd-keys.txt\" || true\nfi\n")
			b.WriteString("update-alternatives --query iptables 2> /dev/null | awk '/^Value:/ { print $2 }' > \"$BK/iptables-alternative.txt\"\n")
			// what block 8 compares with: the units that fail now (an old failed unit is not the hop's), and the ReCasaOS units with their state
			b.WriteString("systemctl --failed --no-legend --plain 2> /dev/null | awk '{ print $1 }' | sort > \"$BK/failed-units.txt\" || true\n")
			b.WriteString("systemctl list-units 'casaos*' --all --no-legend --plain 2> /dev/null | awk '{ print $1, $3 }' | sort > \"$BK/casaos-units.txt\" || true\n")
			b.WriteString(": > \"$BK/containers.txt\"\n")
			b.WriteString("if command -v docker > /dev/null 2>&1 && [ -S /run/docker.sock ]; then\n")
			b.WriteString("  ids=\"$(docker ps -aq 2> /dev/null || true)\"\n")
			b.WriteString("  if [ -n \"$ids\" ]; then docker inspect --format '{{.Name}} policy={{.HostConfig.RestartPolicy.Name}} state={{.State.Status}}' $ids > \"$BK/containers.txt\" 2>&1 || true; fi\n")
			b.WriteString("fi\n")
			b.WriteString("find / -xdev \\( -name '*.dpkg-old' -o -name '*.dpkg-new' -o -name '*.dpkg-dist' -o -name '*.ucf-*' \\) 2> /dev/null | sort > \"$BK/dpkg-files.txt\" || true\n")
			b.WriteString("for f in selections.txt packages.tsv etc.tar.gz listening.txt; do [ -s \"$BK/$f\" ] || { echo \"STOP: $BK/$f was not written: is the disk full (df -h /var/backups)? See what the commands above printed, free some room, then run this block again.\"; exit 1; }; done\n")
			b.WriteString(done("snapshot", 2))
			return b.String()
		},
	},
	{
		RunbookBlock: RunbookBlock{ID: "sources", Title: "write the sources of Debian 12, and read the plan again"},
		prose: []string{
			"It first asks whether it was run before (pasted again, it says to run the restore script, or that it ended and the next is block 4), then looks again at what it relies on (the plan is inside its 24 hours,",
			"the sources of apt are still the plan's: block 0 looked once, and hours may have passed), stops the timers again, stops while a package manager is at work, copies /etc/apt",
			"(whole or not at all), writes the sources that the plan generated (exactly its text: it reads them back and compares their hash with the plan's, before and after it puts them in place), puts aside",
			"the sources that are not Debian's, runs apt-get update, then simulates the upgrade again and compares what apt would remove with the plan. The counts may drift by a few; the NAMES of the removals",
			"may not, and neither may a package that apt keeps back or does not upgrade, or a downgrade (the plan has none).",
			"It also looks at the folders that another plan of this machine left in /var/backups, as block 0 does: the sources of apt are the ones that plan's block 3 wrote, and its restore script is the way back.",
			"Until block 5 begins, the sources can be put back with: sh /var/backups/casaos-os-upgrade-<plan id>/restore-sources.sh (it also lets you run this block again). From block 5 on it refuses: the way is forward.",
		},
		body: func(c *rbCtx, prev string) string {
			var b strings.Builder
			b.WriteString(head(c, prev))
			b.WriteString(c.aptq() + shSim + shNotup + sourcesHash)
			b.WriteString("APT_ETC=/etc/apt\n")
			b.WriteString("undo=\"No package has been installed or removed yet. To put the sources back: sh $BK/restore-sources.sh   (it also lets this block be run again, and this block says when the plan is no longer good).\"\n")
			b.WriteString("changed=\"The machine is not as it was when the plan was made: once the sources are back, ask for a new plan.\"\n")
			b.WriteString(beganCheck("the sources are Debian 12's now: this block would write them again or put Debian 11's back."))
			// Has this block been run? Asked BEFORE the date and the hash of the sources below: once the sources are Debian 12's their hash is not the plan's any more,
			// and a block 3 pasted again (after a dropped connection, a stop at the update or at the comparison, or when it ended) has to be sent to the restore script, or
			// told that it ended, and not told to ask for a new plan: a plan cannot be made on sources that are Debian 12's already.
			b.WriteString("if [ -e \"$BK/apt-orig\" ]; then\n")
			b.WriteString("  if [ -e \"$BK/done-sources\" ]; then " + stop("block 3 ended: the next is block 4 ($BK/done-sources is there). To do block 3 again, put the sources back first: sh $BK/restore-sources.sh (it forgets blocks 3 and 4), then run this block again.") + "; fi\n")
			b.WriteString("  if [ -f \"$BK/restore-sources.sh\" ]; then " + stop("$BK/apt-orig is there already: this block was run before, and the sources of apt may be the ones it wrote. Run sh $BK/restore-sources.sh (it puts the sources back and takes apt-orig aside), then run this block again.") + "; fi\n")
			b.WriteString("  " + stop("$BK/apt-orig is there already, and $BK/restore-sources.sh is not. apt-orig is the whole copy of /etc/apt that this block made before it changed anything: compare it with /etc/apt, put back by hand what differs, remove apt-orig, then run this block again.") + "\n")
			b.WriteString("fi\n")
			b.WriteString("[ ! -e \"$BK/done-sources\" ] || " + stop("block 3 ended: the next is block 4 ($BK/done-sources is there).") + "\n")
			// another plan of this machine whose block 3 ran has rewritten the sources: this plan cannot go on from them, and the way back is that plan's restore script
			b.WriteString(otherPlans())
			if len(c.manual) > 0 {
				b.WriteString(c.acceptRemovals())
				fmt.Fprintf(&b, "[ \"$ACCEPT_REMOVALS\" = %s ] || %s\n", shQ(strings.Join(c.manual, " ")), stop("type the names of the packages that are removed, "+strings.Join(c.manual, " ")+", between the quotes of ACCEPT_REMOVALS at the top of this block, to accept that they go."))
			}
			b.WriteString("echo \"Block 3: writing the sources of " + c.hop.To + ".\"\n")
			// what this block relies on, looked at again: the plan's 24 hours (blocks 0 to 2 may be done on one day and this one on another), and the sources
			// that the plan was made from (a file added to sources.list.d since block 0 would stay active through the whole hop, and an edit of sources.list
			// would be overwritten by the tree). Nothing has been changed yet.
			b.WriteString(`[ "$(date +%s)" -lt ` + strconv.FormatInt(c.plan.ExpiresAt.Unix(), 10) + ` ] || ` + stop("this plan is older than its 24 hours (it was good until "+c.plan.ExpiresAt.UTC().Format("2006-01-02 15:04")+" UTC): the machine may have changed. Nothing has been changed by this block. Ask for a new plan.") + "\n")
			b.WriteString("now=\"$(srchash)\"\n")
			fmt.Fprintf(&b, "[ \"$now\" = %s ] || %s\n", shQ(c.plan.Sources.HashCurrent), stop("the sources of apt are not the ones the plan was made from: something changed them since block 0. Nothing has been changed by this block. Ask for a new plan."))
			b.WriteString(shTimers())
			b.WriteString(busyCheck("Nothing has been changed by this block."))
			b.WriteString(c.sourcesBackup("Run this block again."))
			b.WriteString("cat > \"$BK/expected-removed.txt\" <<'" + heredocEnd + "'\n")
			for _, r := range c.plan.Removals {
				b.WriteString(r.Name + "\n")
			}
			b.WriteString(heredocEnd + "\n")
			b.WriteString("# the sources that the plan generated, each under a new name first, read back and compared with the plan's hash, and put in place when all are right\n")
			b.WriteString(treeFiles(c))
			b.WriteString("aptq update --error-on=any || { echo \"STOP: apt-get update does not go through with the new sources: the network, a mirror, or the lock of the lists held by another package manager (apt-get update does not wait for a lock): see what it printed above. $undo\"; exit 1; }\n")
			b.WriteString("aptq -s dist-upgrade > \"$BK/resim.txt\" 2>&1 || { echo \"STOP: apt cannot work out the upgrade now: see $BK/resim.txt. $undo\"; exit 1; }\n")
			fmt.Fprintf(&b, "echo \"The plan expected: %d upgraded, %d newly installed, %d to remove and %d not upgraded.\"\n", c.plan.Counts.DistUpgrade.Upgraded, c.plan.Counts.DistUpgrade.New, c.plan.Counts.DistUpgrade.Removed, c.plan.Counts.DistUpgrade.KeptBack)
			b.WriteString("echo \"apt says now:   $(grep -E '^[0-9]+ upgraded, ' \"$BK/resim.txt\" || echo 'no summary')\"\n")
			b.WriteString(checkSim("$BK/resim.txt", "$undo"))
			b.WriteString("if grep -q '^The following packages have been kept back:' \"$BK/resim.txt\" || [ \"$(notup_of \"$BK/resim.txt\")\" != 0 ]; then\n")
			b.WriteString("  echo \"STOP: apt would now keep packages back, or not upgrade them, where the plan has none: something changed since the plan (a hold, a package from outside Debian, a mirror that moved). See $BK/resim.txt. $undo $changed\"\n")
			b.WriteString("  exit 1\n")
			b.WriteString("fi\n")
			b.WriteString("removed_of \"$BK/resim.txt\" > \"$BK/resim-removed.txt\"\n")
			b.WriteString("if [ \"$(cat \"$BK/resim-removed.txt\")\" != \"$(cat \"$BK/expected-removed.txt\")\" ]; then\n")
			b.WriteString("  echo \"STOP: apt would now remove a different set of packages than the plan says.\"\n")
			b.WriteString("  echo \"  the plan:  $(cat \"$BK/expected-removed.txt\")\"\n")
			b.WriteString("  echo \"  apt now:   $(cat \"$BK/resim-removed.txt\")\"\n")
			b.WriteString("  echo \"$undo $changed\"\n")
			b.WriteString("  exit 1\n")
			b.WriteString("fi\n")
			b.WriteString(done("sources", 3))
			return b.String()
		},
	},
	{
		RunbookBlock: RunbookBlock{ID: "download", Title: "download the packages of the minimal upgrade, while the applications run"},
		prose: []string{
			"Only downloads. Nothing is installed, and the applications keep running. Once block 5 has begun this block stops: the packages are being installed, and the way is forward.",
		},
		body: func(c *rbCtx, prev string) string {
			var b strings.Builder
			b.WriteString(head(c, prev))
			b.WriteString(beganCheck("this block only downloads for the minimal upgrade, which is block 5's, and block 5 downloads what it still needs."))
			b.WriteString(c.aptq())
			// where the owner goes from a stop, decided when the stop is said: the marker says whether the upgrade has begun (it is not there when this block begins, and a block 5 run
			// in another terminal may write it while this one downloads)
			b.WriteString("whereto() { if [ -e \"$BK/begun-upgrade\" ]; then echo \"The upgrade has begun (block 5 was started): do not restore anything. Only forward: " + forwardWords + "\"; else echo \"No package has been installed or removed yet: run this block again, or put the sources back with sh $BK/restore-sources.sh\"; fi; }\n")
			b.WriteString("echo \"Block 4: downloading.\"\n")
			b.WriteString("aptq --download-only upgrade || { echo \"STOP: the download did not go through: the network, or a lock held by another package manager (see what apt printed above). $(whereto)\"; exit 1; }\n")
			b.WriteString("ls /var/cache/apt/archives/*.deb > /dev/null 2>&1 || { echo \"STOP: no package was downloaded: apt had nothing to fetch, or the cache of downloaded packages was emptied (apt-get clean), or the download did not go through (see what apt printed above). $(whereto)\"; exit 1; }\n")
			b.WriteString(done("download", 4))
			return b.String()
		},
	},
	{
		RunbookBlock: RunbookBlock{ID: "minimal", Title: "THE POINT OF NO RETURN: the minimal upgrade, then the protected packages", PointOfNoReturn: true},
		prose: []string{
			"From here on nothing is undone by going back: the packages of Debian 11 and of Debian 12 are mixed until the end of block 6, and the way out is forward.",
			"The libc6 upgrade restarts services during this block (ssh, smbd and cron were restarted in the measured hop): run it in tmux or screen, or on the console. The block stops if you do not.",
			"Have the snapshot, or the console. Type I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON between the quotes of CONFIRM to go on.",
			"It stops the timers again, stops while a package manager is at work, writes the marker begun-upgrade (from then on the restore script and blocks 2, 3 and 4 refuse), and runs apt-get upgrade. When apt-get upgrade",
			"fails before it has changed anything (a lock, no room, a download that failed: dpkg's records are as they were), the block says that nothing was installed and takes the marker back. Then apt simulates the",
			"full upgrade again, and the block stops if it would remove a protected package or an essential one, or downgrade anything.",
		},
		body: func(c *rbCtx, prev string) string {
			var b strings.Builder
			b.WriteString(head(c, prev))
			b.WriteString(c.aptq() + shSim + c.guard() + c.help() + shDpkgState)
			b.WriteString(c.survive())
			b.WriteString(c.confirm())
			b.WriteString("[ \"$CONFIRM\" = I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON ] || " + stop("this is the point of no return. Type the words between the quotes of CONFIRM at the top of this block, if you have a snapshot or a console, and want to go on.") + "\n")
			b.WriteString(shTimers())
			// said when it is said: an earlier run of this block may have written the marker (a dead terminal, with apt still at work), and then nothing is "not begun"
			b.WriteString("if [ -e \"$BK/begun-upgrade\" ]; then where=\"The upgrade has begun (an earlier run of this block wrote its marker): do not undo anything.\"; else where=\"Nothing has begun: this block has not written its marker.\"; fi\n")
			b.WriteString(busyCheck("$where"))
			// the marker of the point of no return, before the first command that changes a package: from it on, the restore script and blocks 2, 3 and 4 refuse. It is
			// taken back when apt-get upgrade failed before it changed anything (dpkg's records are as they were), and when this run is the one that wrote it: a marker that
			// lies would forbid the way back for nothing, with words that send the owner forward on a machine that is still Debian 11
			b.WriteString("fresh=yes\n[ ! -e \"$BK/begun-upgrade\" ] || fresh=no\n")
			b.WriteString("before=\"$(dpkgstate)\"\n")
			b.WriteString(": > \"$BK/begun-upgrade\"\n")
			b.WriteString(c.syncAfterMarker())
			b.WriteString("echo \"BLOCK 5: THE POINT OF NO RETURN. THE UPGRADE BEGINS NOW.\"\n")
			b.WriteString("if ! aptq upgrade; then\n")
			b.WriteString("  if [ \"$fresh\" = yes ] && [ \"$before\" = \"$(dpkgstate)\" ]; then\n")
			b.WriteString("    rm -f \"$BK/begun-upgrade\"\n")
			b.WriteString("    " + stop("apt-get upgrade failed before it changed any package (dpkg's records are as they were): nothing was installed or removed, and the marker of the point of no return is taken back. A lock held by another package manager, no room on the disk, or a download that failed are the usual causes: see what apt printed above. Fix it, then run this block again (type the confirmation again), or put the sources back: sh $BK/restore-sources.sh") + "\n")
			b.WriteString("  fi\n")
			b.WriteString("  echo \"STOP: the minimal upgrade failed, and it has begun: do not restore anything. Run: " + forwardCommands + "   then type the confirmation again and run this block again. " + c.logWords() + "\"; exit 1\n")
			b.WriteString("fi\n")
			b.WriteString("aptq -s dist-upgrade > \"$BK/after-minimal.txt\" 2>&1 || { echo \"STOP: apt cannot work out the full upgrade: see $BK/after-minimal.txt. $help\"; exit 1; }\n")
			b.WriteString(checkSim("$BK/after-minimal.txt", "$help"))
			b.WriteString("guard_removals \"$BK/after-minimal.txt\"\n")
			b.WriteString(done("minimal", 5))
			return b.String()
		},
	},
	{
		RunbookBlock: RunbookBlock{ID: "full", Title: "the full upgrade, and until nothing is left to do"},
		prose: []string{
			"Downloads, then installs. The files you edited keep your version (--force-confold), and the new ones are left beside them as .dpkg-dist: the list of them is in the last block.",
			"If the first dist-upgrade fails, it does what the release notes say: configure what is half done, repair, again (the measured hop did not need it: this ladder has not been run for real). The repair and",
			"the full upgrade after it are simulated first and looked at as block 5 looked at its own: a package that is protected or essential would go, a downgrade, and it stops before it runs them.",
			"Then it simulates until nothing is left, at most two passes more;",
			"before each it looks at the simulation it has just made, as block 5 did (a protected or an essential package that would go, a downgrade: it stops). Run it in tmux or screen, or on the console, as",
			"block 5 (it stops if you do not). It stops the timers again, and stops while a package manager is at work.",
			"It can be run again after a restart or a dead terminal, whatever kernel the machine started on: it ends the same way, and block 7 looks at the kernel.",
			"When dpkg was killed in the middle of its work, or a dependency is missing, apt stops at its first command here (it is neither a lock nor the network), and the STOP says to go forward by hand with the commands of the table, then to run this block again.",
		},
		body: func(c *rbCtx, prev string) string {
			var b strings.Builder
			b.WriteString(head(c, prev))
			b.WriteString(c.aptq() + shSim + c.guard() + c.help())
			b.WriteString(c.survive())
			b.WriteString(shTimers())
			b.WriteString(busyCheck("The upgrade has begun: do not undo anything."))
			b.WriteString("echo \"Block 6: the full upgrade. It takes a while, and a package may be silent for minutes.\"\n")
			b.WriteString("aptq --download-only dist-upgrade || " + stop("the download of the full upgrade did not go through: a lock held by another package manager, or the network (see what apt printed above). The upgrade has begun: run this block again when the lock is free or the network is back. A third cause: if apt printed dpkg was interrupted, or Unmet dependencies (dpkg was killed in the middle of its work, or a dependency is missing), running this block again changes nothing: "+interruptedTail) + "\n")
			b.WriteString(c.repairLadder("", nil))
			b.WriteString("pass=1\nconverged=no\n")
			b.WriteString("while [ \"$pass\" -le 3 ]; do\n")
			b.WriteString("  last=\"$BK/converge$pass.txt\"\n")
			b.WriteString("  aptq -s dist-upgrade > \"$last\" 2>&1 || true\n")
			b.WriteString("  if grep -q '^0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded\\.' \"$last\"; then converged=yes; break; fi\n")
			b.WriteString("  if [ \"$pass\" -lt 3 ]; then\n")
			b.WriteString(indent(checkSim("$last", "$help")))
			b.WriteString("    guard_removals \"$last\"\n")
			b.WriteString("    aptq dist-upgrade || " + stop("a catch-up pass failed. Do not undo anything: ask for help. "+c.logWords()) + "\n")
			b.WriteString("  fi\n")
			b.WriteString("  pass=$((pass + 1))\n")
			b.WriteString("done\n")
			b.WriteString("[ \"$converged\" = yes ] || " + stop("apt still has something to do after the passes: see $last, and ask for help. "+c.logWords()) + "\n")
			b.WriteString(auditCheck("Run dpkg --configure -a, then this block again. If dpkg cannot open its own database, do not undo anything: ask for help."))
			b.WriteString("aptq check > /dev/null || " + stop("apt-get check finds a broken dependency, or could not get the lock of dpkg in 5 minutes, or dpkg was interrupted (what it printed above says which). If it printed dpkg was interrupted, or Unmet dependencies: "+interruptedTail+" Do not undo anything: ask for help. "+c.logWords()) + "\n")
			b.WriteString(done("full", 6))
			b.WriteString("echo \"The next is block 7: do not restart the machine before it has ended (it looks at the new kernel and at what the machine needs to start). Apt may say that some packages are no longer needed: do not run apt autoremove by hand, see the end of block 8.\"\n")
			return b.String()
		},
	},
	{
		RunbookBlock: RunbookBlock{ID: "prereboot", Title: "check that the machine will start, then restart it yourself", Reboot: true},
		prose: []string{
			"The newest kernel that dpkg has installed (not the one the plan named: the mirror may have moved since) must have its kernel and its initrd in /boot, the initrd must be at least 1 MiB and lsinitramfs must be able to open it (from 1 MiB to 5 MiB it is only a warning, to be checked by hand: a machine set up with few modules has a small one),",
			"grub must name the kernel, and the old kernel must be kept; sshd must accept its configuration and its settings must be the ones of before; the iptables alternative must be the one of before;",
			"systemd-resolved must be installed if it served as the resolver; a time daemon must be installed. Each failure is a STOP; where a command mends it, the line says which.",
			"If the machine was restarted before this block ended and runs a kernel that was not installed before the hop (the list of packages that block 2 wrote says which were), it already runs the new kernel: the block says so,",
			"skips what is for before a restart (the files of the kernel and of the initrd, grub), makes the checks that stay true after a boot (the resolver, the time daemon, the iptables alternative, sshd), and you go on with block 8.",
			"If it runs the kernel that was there before the hop and the plan installs a new one, the block stops: the new kernel is not installed, or the machine did not start on it.",
			"NOT checked here: /etc/fstab and the mounts (findmnt --verify), the names of the network cards, that Docker answers, that your applications work, and, when there is no /boot/grub/grub.cfg, that the boot loader names the new kernel.",
			"Look at those yourself.",
			"This block does not restart the machine. When it says so, restart it yourself, with the console at hand.",
		},
		body: func(c *rbCtx, prev string) string {
			var b strings.Builder
			b.WriteString(head(c, prev))
			b.WriteString("echo \"Block 7: will the machine start?\"\n")
			b.WriteString("isinst() { case \"$(dpkg-query -W -f='${db:Status-Abbrev}' \"$1\" 2> /dev/null || true)\" in ii*) return 0 ;; esac; return 1; }\n")
			b.WriteString("running=\"$(uname -r)\"\nnewest=\"\"\n")
			// the newest kernel package that dpkg has installed: the metapackages (no digit after the dash), the debug symbols and the unsigned twins are no kernel to start
			b.WriteString("for v in $(dpkg-query -W -f='${db:Status-Abbrev} ${Package}\\n' 'linux-image-[0-9]*' 2> /dev/null | awk '$1 == \"ii\" { if ($2 ~ /-(dbg|unsigned)$/) next; sub(/^linux-image-/, \"\", $2); print $2 }' | sort -V); do newest=\"$v\"; done\n")
			b.WriteString("[ -n \"$newest\" ] || " + stop("dpkg has no linux-image package installed: the machine would not start. Ask for help.") + "\n")
			// Was the kernel that runs there before the hop? Block 2 wrote the packages that were installed then (packages.tsv, status then name then version, tab
			// separated: a kernel is installed when the second letter of its status is i). The newest kernel that dpkg has is the one that runs, and it was NOT there
			// before: the machine was restarted after the new kernel was installed (in the middle of block 5 or 6, or before this block ended), and it already runs
			// it. The checks below are for before the restart, and the machine has started: say so, write the marker (not the boot: block 8 skips that test when it
			// is not there) and end. It is no question of the boot of block 6: a block 6 run again on the new boot would hide the restart.
			b.WriteString("[ -s \"$BK/packages.tsv\" ] || " + stop("$BK/packages.tsv is not there: block 2 wrote it, and this block asks it whether the kernel that runs was there before the hop. Ask for help.") + "\n")
			b.WriteString("had=no\n")
			b.WriteString("if awk -F'\\t' -v k=\"linux-image-$running\" '$1 ~ /^.i/ && $2 == k { f = 1 } END { exit 1 - f }' \"$BK/packages.tsv\"; then had=yes; fi\n")
			b.WriteString("early=no\nwarned=no\n")
			b.WriteString("if [ \"$newest\" = \"$running\" ] && [ \"$had\" = no ]; then\n")
			b.WriteString("  early=yes\n")
			b.WriteString("  echo \"The kernel that runs ($running) is the newest that dpkg has, and it was not installed before the hop (it is not in $BK/packages.tsv): the machine was restarted after the new kernel was installed, and it already runs it. The checks of the kernel files, of the initrd and of grub are for before a restart, and the machine has started: this block skips them, and makes the ones that stay true after a boot (DNS, the clock, the iptables alternative, sshd).\"\n")
			b.WriteString("fi\n")
			// what is for before a restart, and only that, is skipped when the machine has started on the new kernel already
			b.WriteString("if [ \"$early\" = no ]; then\n")
			if expectsNewKernel(c.plan) {
				b.WriteString("[ \"$newest\" != \"$running\" ] || " + stop("the plan installs a new kernel (or its list of new packages was cut and cannot say that it does not), and the newest kernel that dpkg has is the one that runs now ($running), which was already installed before the hop: the new kernel is not installed, or its installation did not end. Run block 6 again, then this block. If it stays so, ask for help, with: dpkg -l 'linux-image*'   Do not restart before: the machine would start on the kernel of Debian 11 with the system of Debian 12.") + "\n")
			}
			b.WriteString("echo \"The kernel that runs: $running. The newest installed: $newest.\"\n")
			b.WriteString("[ -s \"/boot/vmlinuz-$newest\" ] || " + stop("/boot/vmlinuz-$newest is not there: the machine would not start. Fix: apt-get install --reinstall linux-image-$newest   then run this block again.") + "\n")
			b.WriteString("[ -s \"/boot/initrd.img-$newest\" ] || " + stop("/boot/initrd.img-$newest is not there: the machine would not start. Fix: update-initramfs -c -k $newest   then run this block again.") + "\n")
			b.WriteString("size=\"$(wc -c < \"/boot/initrd.img-$newest\")\"\n")
			// under 1 MiB is a file that was cut short. An initrd set up with MODULES=dep can be well under the 5 MiB of a usual one, and
			// that is a good machine, which a stop here would leave with no way out: from 1 MiB to 5 MiB the block only says so (after
			// the check by lsinitramfs, which stays a stop) and leaves the judgement to the owner.
			b.WriteString("[ \"$((size))\" -ge 1048576 ] || " + stop("/boot/initrd.img-$newest is only $((size)) bytes: an initrd is never under 1 MiB, this one is cut short. Fix: update-initramfs -u -k $newest   then run this block again.") + "\n")
			b.WriteString("command -v lsinitramfs > /dev/null 2>&1 || " + stop("lsinitramfs is not there (initramfs-tools): the initrd cannot be opened to look inside. Ask for help.") + "\n")
			b.WriteString("lsinitramfs \"/boot/initrd.img-$newest\" > /dev/null 2>&1 || " + stop("lsinitramfs cannot open /boot/initrd.img-$newest: the machine would not start. Fix: update-initramfs -u -k $newest   then run this block again.") + "\n")
			b.WriteString("if [ \"$((size))\" -le 5242880 ]; then warned=yes; " + c.warned(WarningInitrdSmall) + "echo \"WARNING: /boot/initrd.img-$newest is $((size)) bytes, under the 5 MiB of a usual initrd (this is NOT a stop). That is right on a machine set up with few modules (MODULES=dep in /etc/initramfs-tools/initramfs.conf) and wrong on one whose initrd is cut short: check by hand that it holds the driver of your disk and of your network card, for example: lsinitramfs /boot/initrd.img-$newest | grep -i <the name of the driver>   Restart only if it does.\"; fi\n")
			b.WriteString("if [ -e /boot/grub/grub.cfg ]; then grep -q \"vmlinuz-$newest\" /boot/grub/grub.cfg || " + stop("grub.cfg does not name the kernel $newest. Fix: update-grub   then run this block again.") + "; else warned=yes; " + c.warned(WarningNoGrubConfig) + "echo \"WARNING: /boot/grub/grub.cfg is not there, so this block cannot look at whether the boot loader names the kernel $newest (a machine that starts with another loader, systemd-boot for example, is not one that this text was tried on: that loader may not know the new kernel). This is NOT a stop: look at it by hand before you restart.\"; fi\n")
			b.WriteString("[ -e \"/boot/vmlinuz-$running\" ] || " + stop("the kernel that runs now is not kept in /boot.") + "\n")
			b.WriteString("fi\n")
			// what stays true after a boot, and is looked at whether the machine has started on the new kernel or not: package and configuration state
			b.WriteString("resolved=no\n")
			b.WriteString("case \"$(readlink /etc/resolv.conf 2> /dev/null || true)\" in *run/systemd/resolve/*) resolved=yes ;; esac\n")
			b.WriteString("[ \"$(systemctl is-active systemd-resolved 2> /dev/null || true)\" != active ] || resolved=yes\n")
			// after a restart the machine has started without its resolver: it has no DNS, and apt cannot fetch the package that is missing. The nameserver comes first, by hand
			b.WriteString(shLines(
				`if [ "$resolved" = yes ] && ! isinst systemd-resolved; then`,
				`  if [ "$early" = yes ]; then`,
				`    `+stop("systemd-resolved served as the resolver of this machine and is not installed on Debian 12, and the machine has started without it: it has no DNS, and apt cannot fetch the package. Fix, in this order: replace /etc/resolv.conf, a link to a file of systemd-resolved that is not there, by a file that holds a nameserver of your network (rm /etc/resolv.conf   then:   echo 'nameserver <the address of a DNS server of your network>' > /etc/resolv.conf)   then apt-get install systemd-resolved   then run this block again."),
				`  fi`,
				`  `+stop("systemd-resolved served as the resolver of this machine and is not installed on Debian 12: after a boot the machine has no DNS. Fix: apt-get install systemd-resolved   then run this block again."),
				`fi`,
			))
			b.WriteString("timed=no\n")
			b.WriteString("for p in systemd-timesyncd chrony ntp ntpsec openntpd; do if isinst \"$p\"; then timed=yes; fi; done\n")
			b.WriteString("[ \"$timed\" = yes ] || " + stop("no time daemon is installed (systemd-timesyncd, chrony, ntp, ntpsec or openntpd): the clock drifts when nothing sets it. Fix: apt-get install systemd-timesyncd   then run this block again.") + "\n")
			b.WriteString("before=\"$(cat \"$BK/iptables-alternative.txt\" 2> /dev/null || true)\"\n")
			b.WriteString("now=\"$(update-alternatives --query iptables 2> /dev/null | awk '/^Value:/ { print $2 }')\"\n")
			b.WriteString("[ \"$before\" = \"$now\" ] || " + stop("the iptables alternative is not the one of block 2 (before: ${before:-none}, now: ${now:-none}): the rules of the firewall and of Docker use it. Fix: update-alternatives --set iptables ${before:-the-one-of-before}   then run this block again.") + "\n")
			b.WriteString("if command -v sshd > /dev/null 2>&1; then\n")
			b.WriteString("  " + shSshdDir)
			b.WriteString("  sshd -t || " + stop("sshd does not accept its configuration: the machine would not be reachable after a boot. What sshd -t printed above says which file and which line; put it right, then run this block again.") + "\n")
			b.WriteString("  sshd -T 2> /dev/null | grep -E '^(port|listenaddress|permitrootlogin|passwordauthentication) ' | sort > \"$BK/sshd-keys-now.txt\" || true\n")
			b.WriteString("  grep -E '^(port|listenaddress|permitrootlogin|passwordauthentication) ' \"$BK/sshd-keys.txt\" | sort > \"$BK/sshd-keys-before.txt\" || true\n")
			b.WriteString("  [ \"$(cat \"$BK/sshd-keys-before.txt\")\" = \"$(cat \"$BK/sshd-keys-now.txt\")\" ] || " + stop("the settings of sshd (port, address, root login, passwords) are not the ones of before: compare $BK/sshd-keys.txt with sshd -T.") + "\n")
			b.WriteString("  echo \"The algorithms that sshd accepts for keys are named differently by Debian 12, and its defaults changed: look at them yourself.\"\n")
			b.WriteString("  echo \"  before:  $(grep -E '^pubkeyaccepted' \"$BK/sshd-keys.txt\" || echo none)\"\n")
			b.WriteString("  echo \"  now:     $(sshd -T 2> /dev/null | grep -E '^pubkeyaccepted' || echo none)\"\n")
			b.WriteString("fi\n")
			// the machine has started on the new kernel already: the marker, no boot (block 8 skips the test of the boot when it is not there), and block 8 next
			b.WriteString("if [ \"$early\" = yes ]; then\n")
			b.WriteString("  echo \"NOT checked: /etc/fstab and the mounts (findmnt --verify), the names of the network cards, that Docker answers, that your applications work. Look at them yourself: the machine has started, so you can.\"\n")
			b.WriteString("  printf '%s\\n' \"$newest\" > \"$BK/kernel\"\n")
			b.WriteString("  : > \"$BK/done-prereboot\"\n  echo \"Block 7 done.\"\n  echo \"The machine has started on the new kernel already, so it needs no restart for this. Go on with block 8.\"\n  exit 0\n")
			b.WriteString("fi\n")
			b.WriteString("echo \"NOT checked: /etc/fstab and the mounts (findmnt --verify), the names of the network cards, that Docker answers, that your applications work. Look at them yourself before you restart.\"\n")
			// the kernel that block 8 will expect to find running, and the boot that must be over by then
			b.WriteString("printf '%s\\n' \"$newest\" > \"$BK/kernel\"\n")
			b.WriteString("cat /proc/sys/kernel/random/boot_id > \"$BK/boot_id\"\n")
			b.WriteString(done("prereboot", 7))
			b.WriteString("if [ \"$warned\" = yes ]; then echo \"The checks above passed, except what the WARNING above says (the size of the initrd, or the boot loader): look at it by hand first. What this block does not check is yours too. When you have looked, restart the machine yourself, with the console at hand. This text does not do it.\"\n")
			b.WriteString("else echo \"The checks above passed. What this block does not check (the NOT checked line above) is yours: look at it first. When you have, restart the machine yourself, with the console at hand. This text does not do it.\"; fi\n")
			return b.String()
		},
	},
	{
		RunbookBlock: RunbookBlock{ID: "after", Title: "after the restart: look at the machine, then put the sources and the timers back"},
		prose: []string{
			"Run this when the machine is back. It checks that the machine was restarted, that it is Debian 12, that the kernel that runs is the one block 7 looked at and that the sources of apt name Debian 12.",
			"It shows what to look at (the units that fail now and did not before the hop, the ReCasaOS units and the containers beside the lists of before, the ports that listen compared with the ones of before), puts back the",
			"timers that were running (from " + timersState + ", which it then removes), puts Docker's source back for Debian 12 when Docker publishes it (when Docker's server cannot be asked, for lack of curl or of a network, or",
			"answers an error that is not 'not published', the block stops and gives the commands to put the source back by hand; run it again then), and says which sources stay paused. The Docker engine itself is updated from the dashboard.",
			"That the block ends ('Block 8 done.') says that its steps ran, not that the machine is healthy: read the lists it printed. Once it has ended, no block of this text runs again.",
		},
		body: func(c *rbCtx, prev string) string {
			var b strings.Builder
			b.WriteString(head(c, prev))
			b.WriteString(c.aptq())
			b.WriteString("APT_ETC=/etc/apt\n")
			b.WriteString("echo \"Block 8: after the restart.\"\n")
			b.WriteString(c.freshNote())
			// block 7 wrote the boot it ran in, unless it found the machine restarted already and ended without the checks: then there is no boot to compare
			b.WriteString("if [ -s \"$BK/boot_id\" ]; then\n  [ \"$(cat /proc/sys/kernel/random/boot_id)\" != \"$(cat \"$BK/boot_id\")\" ] || " + stop("the machine has not been restarted since block 7: restart it first.") + "\nfi\n")
			b.WriteString("grep -q '^VERSION_CODENAME=" + c.hop.To + "$' /etc/os-release || " + stop("this machine is not Debian 12 ("+c.hop.To+"): see the console, and the table at the end of this text.") + "\n")
			// the kernel that block 7 looked at is the one that must run: the machine may have started on the old one (grub's default is not the newest kernel)
			b.WriteString("want=\"$(cat \"$BK/kernel\" 2> /dev/null || true)\"\n")
			b.WriteString("[ -n \"$want\" ] || " + stop("$BK/kernel is not there: block 7 writes it, and it did not end. Run block 7 first.") + "\n")
			b.WriteString("[ \"$(uname -r)\" = \"$want\" ] || " + stop("the machine runs the kernel $(uname -r), not $want, the one block 7 looked at: it started on the old one (grub's default, or a choice in its menu). Fix: choose $want in the boot menu on the console, make it the default (GRUB_DEFAULT=0 in /etc/default/grub, or grub-set-default 0 when it is saved), run update-grub, restart, and run this block again.") + "\n")
			// the sources of apt name the new release: a restore script run after the upgrade would have left Debian 11's
			b.WriteString("cat \"$APT_ETC/sources.list\" \"$APT_ETC\"/sources.list.d/*.list 2> /dev/null | grep -Eq '^[[:space:]]*deb(-src)?[[:space:]].*" + c.hop.To + "' || " + stop("the sources of apt do not name "+c.hop.To+": they are not the ones block 3 wrote (the restore script run after the upgrade, or a change by hand). Write Debian 12's sources again as block 3 does (its text has them), run apt-get update, then run this block again.") + "\n")
			b.WriteString("echo \"The machine runs: $(grep '^PRETTY_NAME=' /etc/os-release), kernel $(uname -r).\"\n")
			b.WriteString("systemctl --failed --no-legend --plain 2> /dev/null | awk '{ print $1 }' | sort > \"$BK/failed-units-after.txt\" || true\n")
			b.WriteString("echo \"--- units that fail now and did not before the hop (block 2 wrote down the ones that failed before, in $BK/failed-units.txt):\"\ncomm -13 \"$BK/failed-units.txt\" \"$BK/failed-units-after.txt\" || true\n")
			b.WriteString("echo \"--- all the units that fail now:\"\nsystemctl --failed --no-legend --plain || true\n")
			b.WriteString("echo \"--- the ReCasaOS units now, and as they were before the hop (unit and state):\"\nsystemctl list-units 'casaos*' --all --no-legend --plain || true\necho \"  before:\"\ncat \"$BK/casaos-units.txt\" || true\n")
			b.WriteString("echo \"--- containers now, and what they were before:\"\nif command -v docker > /dev/null 2>&1; then docker ps -a --format '{{.Names}} {{.Status}}' || true; fi\nif [ -s \"$BK/containers.txt\" ]; then cat \"$BK/containers.txt\"; else echo \"(there were none)\"; fi\n")
			b.WriteString("ss -Hltn | awk '{ print $4 }' | sort > \"$BK/listening-after.txt\"\n")
			b.WriteString("echo \"--- ports that listened before the hop and do not now (a service may start a little after the machine does):\"\ncomm -23 \"$BK/listening.txt\" \"$BK/listening-after.txt\" || true\n")
			b.WriteString("echo \"--- ports that listen now and did not before:\"\ncomm -13 \"$BK/listening.txt\" \"$BK/listening-after.txt\" || true\n")
			b.WriteString(auditCheck("Run dpkg --configure -a, then run this block again."))
			b.WriteString("# the timers, as they were (block 1 wrote them down, in a file outside the folder of the plan)\n")
			b.WriteString(shTimersBack())
			docker := false
			for _, pa := range c.plan.Paused {
				if pa.Docker {
					docker = true
				}
			}
			if docker {
				b.WriteString("# Docker's source goes back, for Debian 12, when Docker publishes Debian 12. curl says which it is: 0 it does; 22 it answers with an error status, which is\n")
				b.WriteString("# 'not published' for 403, 404 and 410 and the server's own trouble for the others (429, 5xx); anything else is the network (6 no name, 7 no connection, 28 too\n")
				b.WriteString("# slow) or no curl, and is no answer.\n")
				b.WriteString("rc=0\ncode=000\n")
				b.WriteString("if command -v curl > /dev/null 2>&1; then code=\"$(curl -fsI --connect-timeout 10 --max-time 30 -o /dev/null -w '%{http_code}' https://download.docker.com/linux/debian/dists/" + c.hop.To + "/InRelease)\" || rc=$?; else rc=127; fi\n")
				b.WriteString("if [ \"$rc\" = 0 ]; then\n")
				for _, pa := range c.plan.Paused {
					switch {
					case pa.Docker && pa.Line == 0:
						fmt.Fprintf(&b, "  f=\"$APT_ETC/sources.list.d/%s\"\n", pa.File)
						// the new file is made from the paused one, under a new name, and the paused one is removed last: a run that was cut anywhere in these four lines is
						// done again from the start (the paused file is the source, and it is still there)
						b.WriteString("  if [ -e \"$f.casaos-paused\" ]; then\n")
						b.WriteString("    ( umask 022; awk '/^[ \\t]*#/ { print; next } { for (i = 1; i <= NF; i++) if ($i == \"" + c.hop.From + "\") $i = \"" + c.hop.To + "\"; print }' \"$f.casaos-paused\" > \"$f.casaos-new\" )\n")
						b.WriteString("    mv -f \"$f.casaos-new\" \"$f\"\n")
						b.WriteString("    rm -f \"$f.casaos-paused\"\n")
						b.WriteString("  fi\n")
					case pa.Docker:
						// this line, as the plan said it (the tree has the lines of the file one for one), and only if it is still Docker's:
						// the type, the options, then the address of Docker's host. A line that only has the name somewhere, a third
						// party's with it in its path or in its comment, stays as it is
						fmt.Fprintf(&b, "  ( umask 022; awk 'NR == %d && /%s/ { sub(/^# casaos-paused: /, \"\"); for (i = 1; i <= NF; i++) if ($i == \"%s\") $i = \"%s\" } { print }' \"$APT_ETC/sources.list\" > \"$APT_ETC/sources.list.casaos-new\" )\n",
							pa.Line, pausedDockerLine, c.hop.From, c.hop.To)
						b.WriteString("  mv -f \"$APT_ETC/sources.list.casaos-new\" \"$APT_ETC/sources.list\"\n")
					}
				}
				byHand := "  echo " + shQ("To put Docker's source back by hand, when it is published: "+dockerByHand(c)) + "\n"
				b.WriteString(c.dockerAnswers(byHand))
			}
			var left []string
			for _, pa := range c.plan.Paused {
				if !pa.Docker {
					if pa.Line == 0 {
						left = append(left, pa.File+" ("+strings.Join(pa.Hosts, " ")+")")
					} else {
						left = append(left, fmt.Sprintf("line %d of %s (%s)", pa.Line, pa.File, strings.Join(pa.Hosts, " ")))
					}
				}
			}
			if len(left) > 0 {
				b.WriteString("echo " + shQ("These sources stay paused: "+strings.Join(left, "; ")+". Put them back yourself when their vendor has a release for Debian 12.") + "\n")
			}
			b.WriteString("aptq update || echo \"apt-get update did not go through (another package manager may hold the lock of the lists: apt-get update does not wait for it): run it again.\"\n")
			b.WriteString("find / -xdev \\( -name '*.dpkg-old' -o -name '*.dpkg-new' -o -name '*.dpkg-dist' -o -name '*.ucf-*' \\) 2> /dev/null | sort > \"$BK/dpkg-files-after.txt\" || true\n")
			b.WriteString("echo \"Files that dpkg left beside the ones you edited, and that were not there when block 2 looked (a .dpkg-dist is the new version of a file of yours that was NOT installed over it: compare it with yours; sshd, samba and the defaults of grub are the ones to look at):\"\n")
			b.WriteString("comm -13 \"$BK/dpkg-files.txt\" \"$BK/dpkg-files-after.txt\" || true\n")
			b.WriteString("echo \"  (all of them, the old ones too: $BK/dpkg-files-after.txt)\"\n")
			b.WriteString("echo \"Then update Docker from the dashboard (System packages), and look at your applications.\"\n")
			b.WriteString("echo \"Do not run apt autoremove without looking: apt says that some packages are no longer needed, and the list can hold a package that this text protects (net-tools was in it in the measured hop). Run apt-get -s autoremove first, and keep what you need: apt-mark manual <the name>.\"\n")
			// the state of the timers has done its work: a later hop starts from the machine as it is then
			b.WriteString("rm -f \"$TS\"\n")
			b.WriteString(done("after", 8))
			b.WriteString("echo \"That is not to say that the machine is healthy: the steps ran. Read the lists above (the units that fail, the ports, the containers, the files of dpkg) and look at your applications.\"\n")
			return b.String()
		},
	},
}

// pausedDockerLine is, for awk, a line of sources.list that the hop put aside and that is Docker's: the mark, the type, the options
// if there are any, and then the address of download.docker.com (with a user if it has one), nothing more. The plan flagged the
// line by the host of its address; this is the same decision, said on the text of the line, and the line is also named by its number.
const pausedDockerLine = `^# casaos-paused: deb(-src)?[ \t]+(\[[^[]*\][ \t]+)?https?:\/\/([^\/@ \t]*@)?download\.docker\.com[\/:]`

// dockerByHand is the plain commands that put Docker's paused source back by hand, for the lines of block 8 that stop, or that leave it paused: the file that
// the hop put aside is moved back and its codename changed, a line of sources.list has its mark taken off and its codename changed. The names are the plan's
// (apt's file names: letters, digits and a few signs), and the text has no quote that an echo in single quotes could not carry.
func dockerByHand(c *rbCtx) string {
	var parts []string
	for _, pa := range c.plan.Paused {
		switch {
		case pa.Docker && pa.Line == 0:
			f := "/etc/apt/sources.list.d/" + pa.File
			parts = append(parts, fmt.Sprintf(`mv %[1]s.casaos-paused %[1]s   then:   sed -i "s/ %[2]s / %[3]s /" %[1]s`, f, c.hop.From, c.hop.To))
		case pa.Docker:
			parts = append(parts, fmt.Sprintf(`sed -i "%[1]ds/^# casaos-paused: //; %[1]ds/ %[2]s / %[3]s /" /etc/apt/sources.list`, pa.Line, c.hop.From, c.hop.To))
		}
	}
	return strings.Join(parts, "   and:   ") + "   then:   apt-get update"
}

// expectsNewKernel says whether the plan installs a kernel of the new release: a new package linux-image-<digit...> (the metapackages have
// no digit after the dash). The version of the kernel to look at is not the plan's: block 7 asks dpkg for the newest one that is installed,
// at the time it runs. The list of new packages is cut at 600 names, sorted, and a kernel sorts late: when it is cut (NewTruncated) the list cannot say
// "no new kernel", so the answer is that there is one (a stop that is not needed is a stop with a way out; a stop that is left out lets a hop that did
// not install its kernel go through).
func expectsNewKernel(p Plan) bool {
	if p.NewTruncated {
		return true
	}
	for _, n := range p.NewPackages {
		if v, ok := strings.CutPrefix(n, "linux-image-"); ok && v != "" && v[0] >= '0' && v[0] <= '9' {
			return true
		}
	}
	return false
}
