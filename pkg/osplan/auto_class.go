package osplan

import (
	"fmt"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The class of a STOP line. The dashboard does not read the prose of a stop (it is shown as the detail line, never compared, never advice): it reads the class that the unit's text
// writes beside it, and the class says what the page offers (whether the owner may go on after the point of no return, and whether an automatic try may be made). One table says
// which class each STOP line has, by its opening, and one pass puts the class in the text.

// A stopClass gives a class to the STOP lines that begin with prefix. The prefix is a literal part of the text of the line after "STOP: ", as it stands in the shell text (a
// variable such as $unit, $BK or $what is part of it), and stops before the first word that depends on the plan or on the box (a number of megabytes, a date, the name of a
// package), at most stopKeyMax characters.
type stopClass struct{ prefix, class string }

const stopKeyMax = 48

// stopClasses is the table, by family. A line has the class of the longest prefix that matches it; a line that none matches is ErrAutoDrift when the text is written, so that a
// STOP line that is added to a block cannot reach the page without a class. Each key is looked for in the text of the real plan (and its variants) by a test: a key that matches
// nothing is a line that was changed, and its class would be lost.
var stopClasses = []stopClass{
	// a package manager is at work: wait, and go on
	{"a package manager is running", osrun.ClassBusy},
	{"$unit is still running", osrun.ClassBusy},

	// the network: apt cannot reach its mirrors
	{"apt-get update does not go through", osrun.ClassNetwork},
	{"the download did not go through", osrun.ClassNetwork},
	{"the download of the full upgrade", osrun.ClassNetwork},
	{"no package was downloaded", osrun.ClassNetwork},

	// apt cannot do what it was asked, or says something that is not understood
	{"apt cannot work out", osrun.ClassAptFailed},
	{"apt printed no summary line", osrun.ClassAptFailed},
	{"the packages that apt lists as removed", osrun.ClassAptFailed},
	{"the minimal upgrade failed", osrun.ClassAptFailed},
	{"apt could not repair", osrun.ClassAptFailed},
	{"the full upgrade failed again", osrun.ClassAptFailed},
	{"a catch-up pass failed", osrun.ClassAptFailed},
	{"apt-get upgrade failed before it changed", osrun.ClassAptFailed},

	// dpkg is not in a state that apt can work from
	{"dpkg is not quiet", osrun.ClassDpkgBroken},
	{"apt-get check finds", osrun.ClassDpkgBroken},
	{"dpkg could not configure", osrun.ClassDpkgBroken},

	// no room, or a file that could not be written
	{"/var/cache/apt/archives needs ", osrun.ClassDisk},
	{"/usr needs ", osrun.ClassDisk},
	{"/ needs ", osrun.ClassDisk},
	{"/var/cache/apt/archives has less", osrun.ClassDisk},
	{"/ has less", osrun.ClassDisk},
	{"/boot has less", osrun.ClassDisk},
	{"/boot/efi has less", osrun.ClassDisk},
	{"tar could not make the copy", osrun.ClassDisk},
	{"$BK/$f was not written", osrun.ClassDisk},
	{"the state of the timers could not be put", osrun.ClassDisk},

	// the machine or the plan is not what it was when the plan was made. After the point of no return the page treats a plan class as a guard: it is the same text in
	// blocks 3, 5 and 6, and what it says there is that the upgrade would now do something the plan does not name
	{"this plan is older than its 24 hours", osrun.ClassPlan},
	{"the sources of apt are not the ones the plan", osrun.ClassPlan},
	{"the files written for apt are not the plan's", osrun.ClassPlan},
	{"the files now in /etc/apt are not the ones", osrun.ClassPlan},
	{"apt would now keep packages back", osrun.ClassPlan},
	{"apt would now remove a different set", osrun.ClassPlan},
	{"apt would downgrade packages", osrun.ClassPlan},
	{"type the names of the packages that are removed", osrun.ClassPlan},
	// the refresh: its sources are what the machine had when it was started, and the release that is left removes nothing
	{"the sources of apt are not the ones the refresh", osrun.ClassPlan},
	{"the current release wants to remove", osrun.ClassPlan},

	// a package that this text protects, or that dpkg says is essential, would go
	{"$what would remove $name", osrun.ClassGuard},
	{"the repair would remove", osrun.ClassGuard},

	// apt still has something to do after the passes that were made
	{"apt still has something to do after the passes", osrun.ClassConverge},

	// what the machine needs to start after the restart
	{"dpkg has no linux-image", osrun.ClassBarrierKernel},
	{"the plan installs a new kernel", osrun.ClassBarrierKernel},
	{"/boot/vmlinuz-", osrun.ClassBarrierKernel},
	{"the kernel that runs now is not kept", osrun.ClassBarrierKernel},
	{"the machine runs the kernel", osrun.ClassBarrierKernel},
	{"/boot/initrd.img-", osrun.ClassBarrierInitrd},
	{"lsinitramfs cannot open", osrun.ClassBarrierInitrd},
	{"grub.cfg does not name", osrun.ClassBarrierGrub},
	{"grub-pc has no disk", osrun.ClassBarrierGrub},
	{"this machine starts with UEFI", osrun.ClassBarrierGrub},
	{"sshd does not accept", osrun.ClassBarrierSSHD},
	{"the settings of sshd", osrun.ClassBarrierSSHD},
	{"systemd-resolved served as the resolver", osrun.ClassBarrierResolver},
	{"no time daemon", osrun.ClassBarrierOther},
	{"the iptables alternative", osrun.ClassBarrierOther},

	// who the machine is, and where it is in the hop
	{"the hop is over", osrun.ClassIdentity},
	{"run this as root", osrun.ClassIdentity},
	{"this machine is not Debian 11", osrun.ClassIdentity},
	{"this machine is not Debian 12", osrun.ClassIdentity},
	{"this machine is called", osrun.ClassIdentity},
	{"the machine has not been restarted", osrun.ClassIdentity},
	{"the sources of apt do not name", osrun.ClassIdentity},
	{"another plan of this machine", osrun.ClassIdentity},
	{"block 3 of another plan", osrun.ClassIdentity},
	{"the upgrade has begun (block 5 was started)", osrun.ClassIdentity},
	// apt refuses a repository whose release information changed ("stable" becoming "oldstable"): the owner says whether the change is wanted, and the page takes the hint from
	// the line (osrun.HintOf), so the line names what apt said
	{"apt refuses a repository whose release", osrun.ClassIdentity},

	// a tool that the block uses is not there
	{"ss is not installed", osrun.ClassTools},
	{"curl is not installed", osrun.ClassTools},
	{"lsinitramfs is not there", osrun.ClassTools},
	{"ps did not answer", osrun.ClassTools},

	// the files of the run are not what the blocks leave: a block that did not end, a later one that did, a copy or a script that is not whole
	{"the block before this one", osrun.ClassIntegrity},
	{"a later block has ended", osrun.ClassIntegrity},
	{"$BK/apt-orig is there already", osrun.ClassIntegrity},
	{"$BK/packages.tsv is not there", osrun.ClassIntegrity},
	{"$BK/kernel is not there", osrun.ClassIntegrity},
	{"the restore script was damaged", osrun.ClassIntegrity},
	{"the restore script is not the one of this text", osrun.ClassIntegrity},
	{"block 3 has begun and ended", osrun.ClassIntegrity},
	{"block 3 has begun and did not end", osrun.ClassIntegrity},
	{"block 3 has begun (apt-orig", osrun.ClassIntegrity},
	{"block 3 ended: the next is block 4", osrun.ClassIntegrity},
	{"this is the point of no return", osrun.ClassIntegrity},
	// the end of a refresh: the sources that were put back are not what they were
	{"the restore script could not put", osrun.ClassIntegrity},
	{"the sources of apt are not what they were", osrun.ClassIntegrity},

	// the block does not run in the unit of its run
	{"this block belongs to the upgrade unit", osrun.ClassUnit},
}

const stopOpen = `echo "STOP: `

// classOf is the class of the STOP line whose text after "STOP: " is after, and the key that gave it: the longest prefix of the table that begins it.
func classOf(table []stopClass, after string) (class, key string, ok bool) {
	best := -1
	for _, s := range table {
		if len(s.prefix) > best && strings.HasPrefix(after, s.prefix) {
			best, class, key = len(s.prefix), s.class, s.prefix
		}
	}
	return class, key, best >= 0
}

// classify gives each STOP line of a block its class: it puts AUTOCLASS=<class>; before each echo "STOP: that is outside a heredoc, so that the shell that ends the block with
// that status leaves the class in the run's folder (prologue). What is inside a heredoc (the restore script, the lines of the sources, the removals that are expected) is not
// the block's own text and is copied as it is: block 3 checks the hash of its restore script, and the tree is the plan's. Any heredoc that is not the <<'CASAOS_EOF' that the
// blocks write is ErrAutoDrift, and so is a STOP line that has no class, and a heredoc that came out different.
func classify(text string) (string, error) { return classifyWith(stopClasses, text) }

func classifyWith(table []stopClass, text string) (string, error) {
	out, _, err := classifyUsing(table, text)
	return out, err
}

// classifyUsing is classifyWith, and says how many STOP lines each key gave a class to (by its prefix).
func classifyUsing(table []stopClass, text string) (string, map[string]int, error) {
	used := map[string]int{}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	inside := false
	for _, line := range lines {
		switch {
		case inside:
			inside = line != heredocEnd
			out = append(out, line)
		case strings.Contains(line, "<<'"+heredocEnd+"'"):
			inside = true
			out = append(out, line)
		case strings.Contains(line, "<<"):
			return "", nil, fmt.Errorf("%w: a heredoc that is not the quoted one of the blocks: %s", ErrAutoDrift, cutAt60(line))
		default:
			var b strings.Builder
			rest := line
			for {
				i := strings.Index(rest, stopOpen)
				if i < 0 {
					b.WriteString(rest)
					break
				}
				after := rest[i+len(stopOpen):]
				class, key, ok := classOf(table, after)
				if !ok {
					return "", nil, fmt.Errorf("%w: no class for the STOP line that begins %q", ErrAutoDrift, cutAt60(after))
				}
				used[key]++
				b.WriteString(rest[:i] + "AUTOCLASS=" + class + "; " + stopOpen)
				rest = after
			}
			out = append(out, b.String())
		}
	}
	classified := strings.Join(out, "\n")
	if !sameHeredocs(text, classified) {
		return "", nil, fmt.Errorf("%w: a heredoc was changed", ErrAutoDrift)
	}
	return classified, used, nil
}

// sameHeredocs says whether the heredoc bodies of two texts are the same ones, byte for byte.
func sameHeredocs(a, b string) bool {
	x, y := heredocBodies(a), heredocBodies(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// heredocBodies are the lines that the quoted heredocs of a text hold, each with the line that opens it and the line that ends it.
func heredocBodies(text string) []string {
	var bodies []string
	var cur []string
	inside := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case inside:
			cur = append(cur, line)
			if line == heredocEnd {
				bodies = append(bodies, strings.Join(cur, "\n"))
				inside = false
			}
		case strings.Contains(line, "<<'"+heredocEnd+"'"):
			inside = true
			cur = []string{line}
		}
	}
	return bodies
}

// cutAt60 is a line cut to what an error may say of it.
func cutAt60(s string) string {
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}
