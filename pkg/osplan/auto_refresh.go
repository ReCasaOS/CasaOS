package osplan

import (
	"fmt"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The refresh: the release that the machine runs, brought to its last state, before the plan of the hop is made. It is the first thing that the owner does to a machine that has
// a backlog of updates, and the plan is made after it (a plan is of the machine as it was, and a refresh that came after would make it old). It is written from the same pieces as
// the blocks of the hop (the timers' state, the copy of the sources and its restore script, the ladder that mends a failed full upgrade), in four blocks that the unit runs one
// after the other, and it has no point of no return: when one of them stops, the unit puts the sources and the timers back, and mends what dpkg left half done with the block that
// the hop's repair uses (the driver does it, and the refresh has no text for the owner to run by hand).
//
//	R0 rcheck    the machine is the release it was started on, dpkg and apt are quiet, nobody else is at work, there is room; the kernels that are there are written down, and
//	             so is the state of the timers, which are then stopped
//	R1 rsources  the copy of /etc/apt and its restore script, the sources of the refresh (the machine's own, with what is not Debian's put aside), and apt-get update
//	R2 rupgrade  the full upgrade of the release that is left, with the phased updates, which removes nothing: a removal is a stop
//	R3 rdone     the sources and the timers back as they were, and the word that says whether the machine wants a restart (refresh-reboot, which the driver reads)

// RefreshFolderPrefix is the start of the name of the backup folder of a refresh; the id of the run ends it.
const RefreshFolderPrefix = "/var/backups/casaos-os-upgrade-refresh-"

// RefreshInputs is what the text of a refresh is written from.
type RefreshInputs struct {
	// Hop is the hop whose old release is brought up to date, as HopSpec.ForRefresh says it: Rewritten was made with it.
	Hop HopSpec
	// Rewritten is the sources of the machine as the refresh writes them (RewriteSources with Hop): the files of apt that are left, and what is put aside.
	Rewritten *Rewritten
	// AcceptReleaseInfo adds the one option that lets apt take a repository whose release information changed ("stable" becoming "oldstable" is the usual one), to the update of
	// R1: the owner said that the change is wanted. Without it apt refuses, and the block says so in words that the page turns into the question.
	AcceptReleaseInfo bool
	// RunID is the id of the run, a UUID, and Hostname the name of the machine, checked by the core: as in AutoParams.
	RunID, Hostname string
	// CgroupFile is the file that the gate reads, as in AutoParams: only tests set it.
	CgroupFile string
}

// refreshCtx is what the text of a refresh has of its own.
type refreshCtx struct {
	accept   bool
	hostname string
	// notes is the shell of R1 that says which lines of the sources the refresh moved to the security archive of Debian 11, and leaves the mark of it in the folder
	notes string
}

// BuildRefresh writes the text that the unit runs for a refresh: the four blocks, and the block that mends what dpkg left half done, which the unwind of a refresh runs. It refuses
// a hop that is not a refresh (ErrNoRunbook: its tree is not the one that this text is for), sources with no tree (ErrNoRunbook), a run id, a name of the machine or a file of
// cgroups that the text could not carry, and a tree that would end a part of the text early (ErrRunbookUnsafe), a text that is too large (ErrRunbookTooLarge), and ErrAutoDrift.
func BuildRefresh(in RefreshInputs) (*AutoRunbook, error) {
	rb, _, err := buildRefresh(in)
	return rb, err
}

// buildRefresh is BuildRefresh and the number of times that each hook went off in each block.
func buildRefresh(in RefreshInputs) (*AutoRunbook, map[string]int, error) {
	if !in.Hop.Refresh || in.Hop.From == "" || in.Rewritten == nil || len(in.Rewritten.Tree) == 0 {
		return nil, nil, ErrNoRunbook
	}
	if !osrun.ValidUUID(in.RunID) || !hostnamePattern.MatchString(in.Hostname) || (in.CgroupFile != "" && !cgroupFilePattern.MatchString(in.CgroupFile)) || !treeIsSafe(in.Rewritten.Tree) {
		return nil, nil, ErrRunbookUnsafe
	}
	a := AutoParams{RunID: in.RunID, Hostname: in.Hostname, CgroupFile: in.CgroupFile}
	c := &rbCtx{
		// what the blocks that write the sources read of a plan: the tree and its hashes, and what is put aside
		plan: Plan{Hop: string(in.Hop.ID), Paused: in.Rewritten.Paused, Sources: SourcesPlan{Tree: in.Rewritten.Tree, HashCurrent: in.Rewritten.HashCurrent, HashGenerated: in.Rewritten.HashGenerated}},
		hop:  in.Hop, auto: &a, fired: map[string]int{}, bk: RefreshFolderPrefix + in.RunID,
		refresh: &refreshCtx{accept: in.AcceptReleaseInfo, hostname: in.Hostname, notes: securityArchiveNotes(in.Rewritten.Files)},
	}
	out := &AutoRunbook{RunID: in.RunID}
	total := 0
	prev := ""
	for _, blk := range refreshBlocks {
		c.cur = blk.ID
		text, err := autoText(blk.ID, in.RunID, blk.body(c, prev))
		if err != nil {
			return nil, nil, err
		}
		out.Blocks = append(out.Blocks, AutoBlock{ID: blk.ID, Title: blk.Title, Text: text})
		total += len(text)
		prev = blk.ID
	}
	c.cur = osrun.BlockForward
	text, err := autoText(osrun.BlockForward, in.RunID, autoForward(c))
	if err != nil {
		return nil, nil, err
	}
	out.Forward = AutoBlock{ID: osrun.BlockForward, Title: forwardTitle, Text: text}
	total += len(text)
	if total > MaxRunbookBytes {
		return nil, nil, ErrRunbookTooLarge
	}
	if err := c.checkSites(autoRefreshHookSites()); err != nil {
		return nil, nil, err
	}
	return out, c.fired, nil
}

// autoRefreshHookSites is where each hook has a site in the text of a refresh: the gate of the unit in every block, and apt without its pty in the blocks that run apt. The
// others are the hop's alone (the name that is typed, the removals that are accepted, the confirmation, the terminal, the marker of the point of no return, Docker).
func autoRefreshHookSites() map[string]int {
	sites := map[string]int{}
	for _, id := range append(osrun.BlockList(osrun.PhaseRefresh, osrun.Files{}), osrun.BlockForward) {
		sites[id+"/"+hookPrologue] = 1
	}
	for _, id := range []string{osrun.BlockRCheck, osrun.BlockRSources, osrun.BlockRUpgrade, osrun.BlockForward} {
		sites[id+"/"+hookAptq] = 1
	}
	return sites
}

// refreshPhased is the option that makes apt include the phased updates, as the refresh of the measured hop ran its full upgrade: a refresh that leaves a package out because the
// archive is still handing it out in waves is not a refresh.
const refreshPhased = " -o APT::Get::Always-Include-Phased-Updates=true"

// refreshSecurity404 is the line of apt that says a package of the security updates of Debian 11 was not found on the mirror that still lists it (apt 2.2.4: "E: Failed to fetch
// <address>  404  Not Found [IP: ...]", measured in testdata/real/chroot-d11-refresh-security-404-download).
const refreshSecurity404 = `^E: Failed to fetch https?://(security\.debian\.org|deb\.debian\.org/debian-security)/[^ ]+ +404 +Not Found`

// SecurityArchiveGapWords begins the line of the stop of R2 for a 404 from security.debian.org; osrun.HintOf reads the hint SecurityArchiveGap from it, and the class is network.
const SecurityArchiveGapWords = "the download did not go through: security.debian.org answered 404 Not Found for packages of Debian 11, whose files were moved to archive.debian.org/debian-security (the refresh moves the lines it knows to the archive, and a line of the sources that it did not recognise still points at security.debian.org)."

// refreshKernels lists the kernel packages that dpkg has, as lines "<name> <version>", sorted: the kernels that were there before the refresh, and those that are there after it.
const refreshKernels = `dpkg-query -W -f='${db:Status-Abbrev} ${Package} ${Version}\n' 'linux-image-*' 2> /dev/null | awk '$1 ~ /^.i/ { print $2, $3 }' | sort`

// refreshRoomWords end the STOP lines of R0 that say a directory has no room.
const refreshRoomWords = " Free some room there (apt-get clean empties the cache of downloaded packages; remove what you do not need), then start the refresh again."

// shHelpRefresh is what the STOP lines of R2 end with: the refresh has no point of no return, and the unit puts the machine back.
const shHelpRefresh = "help=\"The refresh stops here: the unit puts the sources and the timers back, and finishes what dpkg left half done. Do not undo anything by hand, and ask for help if the machine is not as it was. " + logWordsUnit + "\"\n"

// shNoRemovals defines no_removals: the refresh brings the release that is left to its last state and removes nothing, so a simulation that lists a removal is a stop, whatever
// the package is (what is protected is stopped first, by guard_removals, with its own words).
const shNoRemovals = `no_removals() {
  for name in $(removed_of "$1"); do
    echo "STOP: the current release wants to remove $name: use the manual path or the packages window, which show what goes and ask first. The refresh removes nothing: see $1."
    exit 1
  done
}
`

// refreshDone ends a block of the refresh: its marker, and what it says.
func refreshDone(id, label string) string {
	return fmt.Sprintf(": > \"$BK/done-%s\"\necho \"Block %s done.\"\n", id, label)
}

var refreshBlocks = []blockSpec{
	{
		RunbookBlock: RunbookBlock{ID: osrun.BlockRCheck, Title: "check that the old release can be brought up to date, and stop the timers of apt"},
		body: func(c *rbCtx, prev string) string {
			var b strings.Builder
			b.WriteString(head(c, prev))
			b.WriteString(c.aptq())
			b.WriteString("echo \"Block R0: can the old release be brought up to date? Nothing is changed but the timers of apt.\"\n")
			b.WriteString(`[ "$(id -u)" = 0 ] || ` + stop("run this as root.") + "\n")
			b.WriteString(`[ "$(hostname)" = ` + shQ(c.refresh.hostname) + ` ] || ` + stop("this machine is called $(hostname), which is not the machine that the refresh was started on: the text of this unit was written for another.") + "\n")
			b.WriteString(`grep -q '^VERSION_CODENAME=` + c.hop.From + `$' /etc/os-release || ` + stop("this machine is not Debian 11 ("+c.hop.From+") any more: the refresh brings the release that the machine runs up to its last state, and this machine runs another.") + "\n")
			b.WriteString(otherPlans())
			// a package manager at work is looked for before dpkg is asked whether it is quiet: one that is half way through an install leaves dpkg unquiet, and the stop that
			// says so is the busy one, which is the truth, and not a broken dpkg that the owner is sent to mend under it
			b.WriteString(busyCheck("Nothing has been changed by this block."))
			b.WriteString(auditCheck("Run: dpkg --configure -a   then start the refresh again. If dpkg cannot open its own database, ask for help."))
			b.WriteString("aptq check > /dev/null || " + stop("apt-get check finds a broken dependency, or could not get the lock of dpkg in 5 minutes (what it printed above says which): repair it, or wait for the other package manager, then start the refresh again.") + "\n")
			b.WriteString("avail() { df -Pk \"$1\" | awk 'NR == 2 { print $4 }'; }\n")
			fmt.Fprintf(&b, "[ \"$(avail /var/cache/apt/archives)\" -ge %d ] || %s\n", 1536*1024, stop("/var/cache/apt/archives has less than 1.5 GB free."+refreshRoomWords))
			fmt.Fprintf(&b, "[ \"$(avail /)\" -ge %d ] || %s\n", 2048*1024, stop("/ has less than 2 GB free."+refreshRoomWords))
			// from here the block changes something: the folder of the refresh is made, the kernels that are there are written down (R3 compares), and the state of the timers is
			// written as block 1 of the hop writes it. done-timers comes before the timers are stopped, and not after: a stop that fails half way has stopped some of them, and
			// the unwind starts again what the state says was running only when it finds that file
			b.WriteString(bkdir)
			b.WriteString(refreshKernels + " > \"$BK/kernels-before.txt\" || true\n")
			b.WriteString("TS=" + timersState + "\n")
			b.WriteString(shTimersState("the end of the refresh"))
			b.WriteString(": > \"$BK/done-timers\"\nsync\n")
			b.WriteString(shTimers())
			b.WriteString(refreshDone(osrun.BlockRCheck, "R0"))
			return b.String()
		},
	},
	{
		RunbookBlock: RunbookBlock{ID: osrun.BlockRSources, Title: "keep a copy of the sources, and fetch the lists of the old release again"},
		body: func(c *rbCtx, prev string) string {
			var b strings.Builder
			b.WriteString(head(c, prev))
			b.WriteString(c.aptq() + sourcesHash)
			b.WriteString("APT_ETC=/etc/apt\n")
			b.WriteString("echo \"Block R1: the sources of the refresh, and apt-get update.\"\n")
			// the tree was made from the files that were on the machine then: what is there now has to be what it was made from, or the tree would overwrite a change
			b.WriteString("now=\"$(srchash)\"\n")
			fmt.Fprintf(&b, "[ \"$now\" = %s ] || %s\n", shQ(c.plan.Sources.HashCurrent), stop("the sources of apt are not the ones the refresh was started from: something changed them. Nothing has been changed by this block."))
			b.WriteString(busyCheck("Nothing has been changed by this block."))
			b.WriteString(c.sourcesBackup("Start the refresh again."))
			// a refresh removes nothing, and the folder says what it expects: nothing (the repair block reads the list)
			b.WriteString(": > \"$BK/expected-removed.txt\"\n")
			b.WriteString("# the sources of the refresh: the machine's own, each under a new name first, read back and compared with the hash that was made of them, and put in place when all are right\n")
			b.WriteString(treeFiles(c))
			b.WriteString(c.refresh.notes)
			opts := ""
			if c.refresh.accept {
				opts = " --allow-releaseinfo-change"
			}
			// the output of apt is kept as well as shown: a repository whose release information changed is told from what apt says of it, and the owner is asked whether the
			// change is wanted (the page reads the hint of the STOP line)
			b.WriteString("rc=0\naptq update --error-on=any" + opts + " > \"$BK/update.txt\" 2>&1 || rc=$?\ncat \"$BK/update.txt\"\n")
			b.WriteString("if [ \"$rc\" != 0 ]; then\n")
			b.WriteString("  if grep -Eq 'must be accepted explicitly before updates for this repository can be applied|changed its .(Origin|Label|Suite|Version|Codename). value' \"$BK/update.txt\"; then\n")
			b.WriteString("    echo \"STOP: apt refuses a repository whose release information changed (apt says: This must be accepted explicitly before updates for this repository can be applied): the owner says whether the change is wanted, and starts the refresh again with that said. Nothing has been installed or removed.\"; exit 1\n")
			b.WriteString("  fi\n")
			b.WriteString("  echo \"STOP: apt-get update does not go through with the sources of the refresh: the network, a mirror, or the lock of the lists held by another package manager (apt-get update does not wait for a lock): see what it printed above. Nothing has been installed or removed.\"; exit 1\n")
			b.WriteString("fi\n")
			b.WriteString(refreshDone(osrun.BlockRSources, "R1"))
			return b.String()
		},
	},
	{
		RunbookBlock: RunbookBlock{ID: osrun.BlockRUpgrade, Title: "bring the packages of the old release to their last state"},
		body: func(c *rbCtx, prev string) string {
			var b strings.Builder
			b.WriteString(head(c, prev))
			b.WriteString(c.aptq() + shSim + c.guardSaying("The refresh stops here, and the unit puts the machine back. Ask for help, with $1. ") + shHelpRefresh + shNoRemovals)
			b.WriteString("echo \"Block R2: the full upgrade of the old release. It takes a while, and a package may be silent for minutes.\"\n")
			b.WriteString(busyCheck("Nothing has been installed or removed yet."))
			b.WriteString("aptq -s dist-upgrade" + refreshPhased + " > \"$BK/resim.txt\" 2>&1 || { echo \"STOP: apt cannot work out the upgrade of the old release: see $BK/resim.txt. $help\"; exit 1; }\n")
			b.WriteString(checkSim("$BK/resim.txt", "$help"))
			b.WriteString("guard_removals \"$BK/resim.txt\"\n")
			b.WriteString("no_removals \"$BK/resim.txt\"\n")
			// what apt prints is shown as it comes and kept, and its status is kept apart (a pipe would take tee's): a 404 from the security mirror is told from the others, because
			// the owner can do something about it (the page reads the hint of the STOP line)
			b.WriteString("{ aptq --download-only dist-upgrade" + refreshPhased + " || echo \"$?\" > \"$BK/download.rc\"; } 2>&1 | tee \"$BK/download.txt\"\n")
			b.WriteString("if [ -e \"$BK/download.rc\" ]; then\n")
			b.WriteString("  if grep -Eq '" + refreshSecurity404 + "' \"$BK/download.txt\"; then\n")
			b.WriteString("    " + stop(SecurityArchiveGapWords+" Nothing has been installed or removed yet.") + "\n")
			b.WriteString("  fi\n")
			b.WriteString("  " + stop("the download did not go through: the network, or a lock held by another package manager (see what apt printed above). Nothing has been installed or removed yet.") + "\n")
			b.WriteString("fi\n")
			// from here dpkg is changed: the unwind mends what it leaves only for a refresh that got this far
			b.WriteString(": > \"$BK/begun-rupgrade\"\nsync\n")
			b.WriteString(c.repairLadder(refreshPhased, func(file string) string { return "no_removals \"" + file + "\"\n" }))
			b.WriteString(auditCheck("Run dpkg --configure -a, then start the refresh again. If dpkg cannot open its own database, ask for help."))
			b.WriteString("aptq check > /dev/null || " + stop("apt-get check finds a broken dependency, or could not get the lock of dpkg in 5 minutes, or dpkg was interrupted (what it printed above says which). Do not undo anything by hand: ask for help. "+c.logWords()) + "\n")
			b.WriteString(refreshDone(osrun.BlockRUpgrade, "R2"))
			return b.String()
		},
	},
	{
		RunbookBlock: RunbookBlock{ID: osrun.BlockRDone, Title: "put the sources and the timers back, and say whether the machine wants a restart"},
		body: func(c *rbCtx, prev string) string {
			var b strings.Builder
			b.WriteString(head(c, prev))
			b.WriteString(sourcesHash)
			b.WriteString("echo \"Block R3: the sources and the timers are put back.\"\n")
			b.WriteString("sh \"$BK/restore-sources.sh\" || " + stop("the restore script could not put the sources back: see what it printed above. The copy of /etc/apt that block R1 made is in $BK/apt-orig.") + "\n")
			// what the machine has in /etc/apt now is what it had before the refresh, byte for byte: the hash of the files that the tree was made from
			b.WriteString("now=\"$(srchash)\"\n")
			fmt.Fprintf(&b, "[ \"$now\" = %s ] || %s\n", shQ(c.plan.Sources.HashCurrent), stop("the sources of apt are not what they were before the refresh, though they were put back from the copy: their hash is $now. Look at /etc/apt by hand: the copy of it that block R1 made is in $BK."))
			b.WriteString("# the timers, as they were (block R0 wrote them down)\n")
			b.WriteString(shTimersBack())
			b.WriteString("rm -f \"$TS\"\n")
			// whether the machine wants to be restarted: the packages say so, or a kernel was installed or changed since R0 looked (the file is read by the driver)
			b.WriteString("wants=no\nwhy=none\n")
			b.WriteString("if [ -e /var/run/reboot-required ]; then\n  wants=yes\n  why=reboot-required\nelse\n")
			b.WriteString("  " + refreshKernels + " > \"$BK/kernels-after.txt\" || true\n")
			b.WriteString("  if [ -n \"$(comm -13 \"$BK/kernels-before.txt\" \"$BK/kernels-after.txt\")\" ]; then\n    wants=yes\n    why=kernel\n  fi\nfi\n")
			b.WriteString("printf '%s %s\\n' \"$wants\" \"$why\" > \"$BK/refresh-reboot\"\n")
			b.WriteString(refreshDone(osrun.BlockRDone, "R3"))
			b.WriteString("echo \"The old release is up to date. Does the machine want a restart: $wants ($why).\"\n")
			return b.String()
		},
	},
}

// securityArchiveNotes is what R1 says of the lines that the rewrite moved to archive.debian.org (the files of the owner are not changed: these are the files that apt reads for
// the refresh), and the mark it leaves, $BK/security-archive, which holds the same lines.
func securityArchiveNotes(files []FileChange) string {
	var b strings.Builder
	for _, f := range files {
		for _, l := range f.Lines {
			if l.Why != WhySecurityArchive {
				continue
			}
			msg := fmt.Sprintf("%s line %d: %s is read for the refresh as %s (security.debian.org no longer serves the packages that its list names: the archive does). The file itself is not changed.", f.File, l.N, l.Before, l.After)
			b.WriteString("echo " + shQ("Note: "+msg) + "\n")
			b.WriteString("echo " + shQ(msg) + " >> \"$BK/security-archive\"\n")
		}
	}
	return b.String()
}
