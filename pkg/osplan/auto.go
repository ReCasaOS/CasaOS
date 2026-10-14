package osplan

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The auto text: the nine blocks of the hop as the unit of the dashboard runs them. They are the blocks of the owner's runbook, written by the same code (blockSpec.body),
// with nine places where the unit differs from a person at a terminal (the hooks below), and one pass that gives every STOP line a class that the dashboard can tell
// apart. Nothing of the block text is replaced after it is written but that pass, which leaves what is inside a heredoc as it is: block 3 checks the hash of its restore
// script, and the tree of the sources is the plan's, byte for byte.
//
// A unit has no terminal and nobody to type the words: the name of the machine, the removals that the owner accepted and the confirmation before the point of no return
// are written into the text by the core after it has checked them against the plan, and the gate of the unit takes the place of every question that was asked of a
// person: a block of this text runs only in the unit that was made for it.

// The words that block 7 of the unit writes in the file "warnings" of the backup folder, one on a line, for each WARNING it prints: the core shows them beside the restart, and the
// judge reports them after it.
const (
	// WarningInitrdSmall: the initrd of the new kernel is under the 5 MiB of a usual one.
	WarningInitrdSmall = "initrd_small"
	// WarningNoGrubConfig: there is no /boot/grub/grub.cfg to see whether the boot loader names the new kernel.
	WarningNoGrubConfig = "grub_cfg_missing"
)

// AutoParams is what the core knows when it writes the text for a run.
type AutoParams struct {
	// RunID is the id of the run, a UUID. The unit sets it in the environment of the blocks, and the gate of each block compares it.
	RunID string
	// Hostname is the name of the machine, checked by the core (^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$, and the one os.Hostname gives).
	Hostname string
	// CgroupFile is the file that the gate reads to see that the block runs in the unit: "" is /proc/self/cgroup. Only tests set it.
	CgroupFile string
}

// An AutoBlock is the text of one block, as the driver runs it from a file of its own.
type AutoBlock struct {
	ID, Title string
	// Text is LF only, ends with a newline, has no carriage return, and starts with the line "# casaos-os-upgrade <id> <run id>".
	Text string
}

// An AutoRunbook is the text of a run: the nine blocks of the hop in their order, and the block that mends what a hop that stopped after its point of no return left half done.
type AutoRunbook struct {
	RunID, PlanID string
	Blocks        []AutoBlock
	Forward       AutoBlock
}

// ErrAutoDrift: a STOP line that no class covers, or a hook that did not apply exactly once, or a part of a heredoc that the class pass changed. The owner's text
// was changed, or this package was, in a way that the unit's text has not been made to follow.
var ErrAutoDrift = errors.New("the auto text has a STOP line that no class covers, or a hook that did not apply exactly once")

var (
	hostnamePattern   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	cgroupFilePattern = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,200}$`)
)

// BuildAuto writes the text that the unit runs for a plan. It refuses what BuildRunbook refuses (ErrNoRunbook, ErrRunbookUnsafe, ErrRunbookTooLarge), a run id that
// is no UUID, a hostname that is no hostname and a file of cgroups that is no path (ErrRunbookUnsafe: the text could not carry it), and ErrAutoDrift.
func BuildAuto(p Plan, a AutoParams) (*AutoRunbook, error) {
	rb, _, err := buildAuto(p, a)
	return rb, err
}

// the hooks, each by the name that the tests and the golden use
const (
	hookPrologue = "H1"  // the trap that says the class of a stop, and the gate of the unit
	hookHostname = "H2"  // the name of the machine, written in
	hookRemovals = "H3"  // the removals that the owner accepted, written in
	hookConfirm  = "H4"  // the confirmation before the point of no return, written in
	hookNoTTY    = "H5"  // no question of the terminal (a console, tmux or screen): a unit has none
	hookAptq     = "H6"  // apt without its pty
	hookSync     = "H7"  // sync after the marker of the point of no return
	hookDocker   = "H8"  // Docker's repository that cannot be asked is a note, not a stop
	hookTimers   = "H9"  // a marker, on the disk, that the timers are about to be stopped
	hookNote     = "H10" // the note about Docker's source is taken away at the start of the check after the restart
	hookWarning  = "H11" // the warnings of block 7 are written down for the core
)

// buildAuto is BuildAuto and the number of times that each hook went off in each block ("<block>/<hook>").
func buildAuto(p Plan, a AutoParams) (*AutoRunbook, map[string]int, error) {
	c, blocks, err := newRbCtx(p)
	if err != nil {
		return nil, nil, err
	}
	if !osrun.ValidUUID(a.RunID) || !hostnamePattern.MatchString(a.Hostname) || (a.CgroupFile != "" && !cgroupFilePattern.MatchString(a.CgroupFile)) {
		return nil, nil, ErrRunbookUnsafe
	}
	c.auto = &a
	c.fired = map[string]int{}
	out := &AutoRunbook{RunID: a.RunID, PlanID: p.PlanID}
	total := 0
	prev := ""
	for _, blk := range blocks {
		c.cur = blk.ID
		text, err := autoText(blk.ID, a.RunID, blk.body(c, prev))
		if err != nil {
			return nil, nil, err
		}
		out.Blocks = append(out.Blocks, AutoBlock{ID: blk.ID, Title: blk.Title, Text: text})
		total += len(text)
		prev = blk.ID
	}
	c.cur = osrun.BlockForward
	text, err := autoText(osrun.BlockForward, a.RunID, autoForward(c))
	if err != nil {
		return nil, nil, err
	}
	out.Forward = AutoBlock{ID: osrun.BlockForward, Title: forwardTitle, Text: text}
	total += len(text)
	if total > MaxRunbookBytes {
		return nil, nil, ErrRunbookTooLarge
	}
	if err := c.checkHooks(); err != nil {
		return nil, nil, err
	}
	return out, c.fired, nil
}

// autoText is a block of the unit: its first line says which block it is and of which run, then its code, then the class of each of its STOP lines.
func autoText(id, runID, body string) (string, error) {
	text := "# casaos-os-upgrade " + id + " " + runID + "\n" + strings.TrimRight(body, "\n") + "\n"
	if strings.ContainsRune(text, '\r') {
		return "", ErrRunbookUnsafe
	}
	return classify(text)
}

// hook says that a hook went off in the block that is being written.
func (c *rbCtx) hook(name string) {
	if c.fired != nil {
		c.fired[c.cur+"/"+name]++
	}
}

// autoHookSites is where each hook has a site, and how many: it is looked at when the text is written, and a hook that went off more or less often is ErrAutoDrift (a
// block that was changed without the unit's text being told). manual says whether the plan has a removal that the owner has to accept, docker whether it puts Docker's
// source aside as a file or a line.
func autoHookSites(manual, docker bool) map[string]int {
	sites := map[string]int{}
	for _, id := range []string{"verify", "timers", "snapshot", "sources", "download", "minimal", "full", "prereboot", "after", osrun.BlockForward} {
		sites[id+"/"+hookPrologue] = 1
	}
	for _, id := range []string{"verify", "sources", "download", "minimal", "full", "after", osrun.BlockForward} {
		sites[id+"/"+hookAptq] = 1
	}
	for _, id := range []string{"verify", "minimal", "full"} {
		sites[id+"/"+hookNoTTY] = 1
	}
	sites["verify/"+hookHostname] = 1
	sites["timers/"+hookTimers] = 1
	sites["after/"+hookNote] = 1
	sites["prereboot/"+hookWarning] = 2
	sites["minimal/"+hookConfirm] = 1
	sites["minimal/"+hookSync] = 1
	if manual {
		sites["sources/"+hookRemovals] = 1
	}
	if docker {
		sites["after/"+hookDocker] = 1
	}
	return sites
}

func (c *rbCtx) checkHooks() error {
	docker := false
	for _, pa := range c.plan.Paused {
		docker = docker || pa.Docker
	}
	return c.checkSites(autoHookSites(len(c.manual) > 0, docker))
}

// checkSites says whether the hooks that went off are the ones that the sites of a text say, each as often.
func (c *rbCtx) checkSites(want map[string]int) error {
	for k, n := range want {
		if c.fired[k] != n {
			return fmt.Errorf("%w: the hook %s went off %d times, not %d", ErrAutoDrift, k, c.fired[k], n)
		}
	}
	for k, n := range c.fired {
		if _, ok := want[k]; !ok {
			return fmt.Errorf("%w: the hook %s went off %d times where it has no site", ErrAutoDrift, k, n)
		}
	}
	return nil
}

// ---- the hooks: each is the text of the owner's runbook when the text is not the unit's -------------------------------------------------

// shAptqAuto is the aptq of the unit: the one of the owner's text, and Dpkg::Use-Pty=0 after -q, as the hop that was measured ran apt. The unit has no terminal for apt's pty
// to take over, and the owner's text keeps the pty so that term.log holds what the scripts of the packages print (the page shows output.log instead).
const shAptqAuto = "export DEBIAN_FRONTEND=noninteractive APT_LISTCHANGES_FRONTEND=none NEEDRESTART_SUSPEND=1\naptq() { apt-get -y -q -o Dpkg::Use-Pty=0 -o DPkg::Lock::Timeout=300 -o Acquire::Retries=3 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold \"$@\"; }\n"

// aptq (H6) defines the function that runs apt.
func (c *rbCtx) aptq() string {
	if c.auto == nil {
		return shAptq
	}
	c.hook(hookAptq)
	return shAptqAuto
}

// prologue (H1) is what an auto block does first: it sets the class that a stop will carry (unknown, until a STOP line says another: the class pass puts
// AUTOCLASS=<class> before each), has the shell write that class into the run's folder when the block ends with a status that is not 0 (a block that ends with 0, as block 7 does
// when the machine has restarted already, writes nothing; a subshell does not run the trap, so a failure that a block handles itself leaves no class), and stops the block when it
// does not run in the unit of this run. The gate is the one gate of an auto block: the owner's confirmation is baked in the text, and nothing else would tell a block that a
// person pasted into a terminal from the unit.
func (c *rbCtx) prologue() string {
	if c.auto == nil {
		return ""
	}
	c.hook(hookPrologue)
	cgroup := "/proc/self/cgroup"
	if c.auto.CgroupFile != "" {
		cgroup = shQ(c.auto.CgroupFile)
	}
	return shLines(
		"AUTOCLASS=unknown",
		`osu_exit() { rc=$?; [ "$rc" -eq 0 ] || printf '%s\n' "$AUTOCLASS" > "${CASAOS_OSU_DIR:-/nonexistent}/stop-class"; }`,
		"trap osu_exit EXIT",
		`[ "${CASAOS_OSU_RUN:-}" = `+shQ(c.auto.RunID)+` ] && grep -q 'casaos-os-upgrade\.service' `+cgroup+` || { echo "STOP: this block belongs to the upgrade unit of the dashboard and runs only there, with the confirmation of the owner baked in: to continue by hand use manual-runbook.sh of the run."; exit 1; }`,
	)
}

// timersStopping (H9) is what the unit does between writing down the state of the timers and stopping the first of them: a marker in the backup folder, and a sync, so that both are on
// the disk before a timer stops. The unwind starts the timers again from the state when the folder holds this marker or the one that block 1 ends with, and a block that is stopped or
// killed between the first stop and its end leaves the timers running again.
func (c *rbCtx) timersStopping() string {
	if c.auto == nil {
		return ""
	}
	c.hook(hookTimers)
	return ": > \"$BK/timers-stopping\"\nsync\n"
}

// freshNote (H10) is what block 8 does first in the unit: the note about Docker's source that an earlier run of this block wrote is taken away, and the one of this run is written
// by the branch that this run takes (and none by the branch that finds Docker answering). The check after the restart is run again when the machine is restarted a second time or
// when the owner asks, and a note that is left from the run before is a finding about a machine that is not the one the judge looks at.
func (c *rbCtx) freshNote() string {
	if c.auto == nil {
		return ""
	}
	c.hook(hookNote)
	return "rm -f \"$BK/docker-source-note\"\n"
}

// warned (H11) is what a warning of block 7 does in the unit besides printing itself: the word of it is written in the backup folder, where the core reads it, to show beside the
// button that restarts the machine and to say in the report after the restart. The owner's text prints the warning and asks them to look.
func (c *rbCtx) warned(word string) string {
	if c.auto == nil {
		return ""
	}
	c.hook(hookWarning)
	return "printf '%s\\n' " + word + " >> \"$BK/warnings\"; "
}

// typedHostname (H2) is the name of the machine: typed by the owner between the quotes, or written by the core (which has checked it is the machine's own). The block compares it
// with hostname all the same.
func (c *rbCtx) typedHostname() string {
	if c.auto == nil {
		return "# Type the name of this machine between the quotes, then run the block.\nTYPED_HOSTNAME=\"\"\n"
	}
	c.hook(hookHostname)
	return "TYPED_HOSTNAME=" + shQ(c.auto.Hostname) + "\n"
}

// acceptRemovals (H3) is the acceptance of the removals of packages that the owner installed himself: typed by the owner, or written by the core, as the plan's own canonical
// names (the core has checked that the set that the owner accepted is the plan's, and never writes the string that it was given).
func (c *rbCtx) acceptRemovals() string {
	if c.auto == nil {
		return fmt.Sprintf("# These packages are removed by the upgrade, and you installed them yourself. To accept it, type their names, as they are, between the quotes:\n#   %s\nACCEPT_REMOVALS=\"\"\n", strings.Join(c.manual, " "))
	}
	c.hook(hookRemovals)
	return "ACCEPT_REMOVALS=" + shQ(strings.Join(c.manual, " ")) + "\n"
}

// confirm (H4) is the confirmation before the point of no return: typed by the owner, or baked into the text of the unit, which the core writes for a run that the owner
// started with the acknowledgements that say there is no way back.
func (c *rbCtx) confirm() string {
	if c.auto == nil {
		return "# Type I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON between the quotes, then run the block.\nCONFIRM=\"\"\n"
	}
	c.hook(hookConfirm)
	return "CONFIRM=I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON\n"
}

// muxAtHand (H5) is block 0's look at whether blocks 5 and 6 will have a terminal that survives a dropped connection: the unit has no terminal, and survives it.
func (c *rbCtx) muxAtHand() string {
	if c.auto == nil {
		return muxAtHand()
	}
	c.hook(hookNoTTY)
	return ""
}

// survive (H5) is the same look, made by blocks 5 and 6 themselves.
func (c *rbCtx) survive() string {
	if c.auto == nil {
		return survive()
	}
	c.hook(hookNoTTY)
	return ""
}

// syncAfterMarker (H7) is the line that makes the marker of the point of no return survive a power cut: it is the file that tells that the upgrade has begun, and the unit
// reads it from the files, never from a log.
func (c *rbCtx) syncAfterMarker() string {
	if c.auto == nil {
		return ""
	}
	c.hook(hookSync)
	return "sync\n"
}

// dockerAnswers (H8) are the branches of block 8 that say what Docker's server answered when it was asked whether it publishes its repository for the new release. byHand is
// the line that gives the commands to put the source back by hand. The owner's text stops on an answer that says nothing of the repository, so that he asks again; the unit
// goes on (a healthy machine that has been upgraded must not be called failed for a blip of the network), leaves the source paused, and writes why in docker-source-note, which
// the judge turns into a finding: not_published when Docker publishes nothing for the release, unreachable when it could not be asked, no_curl when there is no curl.
func (c *rbCtx) dockerAnswers(byHand string) string {
	to := c.hop.To
	if c.auto == nil {
		return "elif [ \"$rc\" = 22 ] && { [ \"$code\" = 404 ] || [ \"$code\" = 403 ] || [ \"$code\" = 410 ]; }; then\n  echo \"Docker publishes nothing for " + to + " yet (download.docker.com answered the status $code for its Release file): its source stays paused.\"\n" + byHand +
			"elif [ \"$rc\" = 22 ]; then\n  echo \"STOP: download.docker.com answered the error status $code, which does not say that it publishes nothing (429 and 5xx are the server's own trouble, and pass): wait a little, then run this block again. Docker's source stays paused until it has been asked.\"\n" + byHand + "  exit 1\n" +
			"elif [ \"$rc\" = 127 ]; then\n  echo \"STOP: curl is not installed, so Docker's repository cannot be asked about " + to + ". Install it: apt-get install curl   then run this block again.\"\n" + byHand + "  exit 1\n" +
			"else\n  echo \"STOP: curl could not reach download.docker.com (exit $rc: 6 no name, 7 no connection, 28 too slow): the network is probably not up yet. Wait a little, then run this block again. Docker's source stays paused until it has been asked.\"\n" + byHand + "  exit 1\nfi\n"
	}
	c.hook(hookDocker)
	note := func(why string) string { return "  printf '%s\\n' " + why + " > \"$BK/docker-source-note\"\n" }
	return "elif [ \"$rc\" = 22 ] && { [ \"$code\" = 404 ] || [ \"$code\" = 403 ] || [ \"$code\" = 410 ]; }; then\n  echo \"Docker publishes nothing for " + to + " yet (download.docker.com answered the status $code for its Release file): its source stays paused.\"\n" + note("not_published") + byHand +
		"elif [ \"$rc\" = 22 ]; then\n  echo \"download.docker.com answered the error status $code, which does not say that it publishes nothing (429 and 5xx are the server's own trouble, and pass): Docker's source stays paused, and the upgrade goes on without it. It is said in the report.\"\n" + note("unreachable") + byHand +
		"elif [ \"$rc\" = 127 ]; then\n  echo \"curl is not installed, so Docker's repository cannot be asked about " + to + ": its source stays paused, and the upgrade goes on without it. It is said in the report.\"\n" + note("no_curl") + byHand +
		"else\n  echo \"curl could not reach download.docker.com (exit $rc: 6 no name, 7 no connection, 28 too slow): the network is probably not up yet. Docker's source stays paused, and the upgrade goes on without it. It is said in the report.\"\n" + note("unreachable") + byHand + "fi\n"
}
