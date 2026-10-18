package osplan

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Parsing what apt printed. The formats are the real ones of apt 2.2.4 (Debian 11, the apt that makes the plan), read with
// the tolerance for apt 2.6.1's text. What is not understood is never guessed at: it is plan_unparsed, and no plan.

const (
	maxNames       = 20000
	maxParsedLine  = 4096
	maxDetailLines = 6
	maxDetailChars = 200
)

// A Summary is the line apt prints at the end of its list: "384 upgraded, 75 newly installed, 2 to remove and 0 not upgraded."
// Downgraded is the count that apt adds, between the new and the removed, when it has packages to downgrade: "0 upgraded, 0
// newly installed, 1 downgraded, 0 to remove and 0 not upgraded."
type Summary struct {
	Upgraded, New, Remove, NotUpgraded, Downgraded int
}

// An InstLine is an "Inst" line of a simulation: the package, the version it is at ("" when it is new), the one it goes to,
// and the repository tag ("Debian:12.15/oldstable").
type InstLine struct {
	Name, From, To, Tag string
}

// A SolverRun is what apt said of a simulated upgrade.
type SolverRun struct {
	Upgraded, New, Removed, KeptBack, AutoRemovable []string
	// Downgraded are the packages that apt would downgrade: a package that is installed at a version above the one that a
	// pin of a priority over 1000 asks for. The hop would stop at them (apt refuses a downgrade that -y has not been told to
	// allow).
	Downgraded []string
	// Essential are the packages that apt warns it is about to remove although they are essential ("WARNING: The following
	// essential packages will be removed."), by the names the list of the removed has. All of them are removed ones.
	Essential []string
	Summary   Summary
	Inst      []InstLine
}

// Removals are the packages the transaction removes: the names of the REMOVED list, which is the authority (a Remv line of
// a package that is installed again by a later Inst line is apt ordering a replacement, and is not one), checked against the
// Remv and Purg lines and the summary, which parse already did.
func (r *SolverRun) Removals() []string { return r.Removed }

var (
	packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.:~-]*$`)
	summaryLine = regexp.MustCompile(`^([0-9]+) upgraded, ([0-9]+) newly installed, (?:([0-9]+) downgraded, )?([0-9]+) to remove and ([0-9]+) not upgraded\.$`)
	// the warning of apt about essential packages: its two lines, and the names that follow ("login libpam-runtime (due to login)")
	essentialDue = regexp.MustCompile(`\(due to [^()]*\)`)
	progress     = regexp.MustCompile(`^(?:Reading package lists|Building dependency tree|Reading state information|Calculating upgrade)\.\.\.(?: Done| [0-9]+%)?$`)
	// the line that apt adds after its summary when dpkg says a package is half installed or half configured
	notFullyInstalled = regexp.MustCompile(`^[0-9]+ not fully installed or removed\.$`)
	autoremove        = regexp.MustCompile(`^Use '(?:sudo )?apt(?:-get)? autoremove' to remove (?:it|them)\.$`)
)

var sectionHeaders = map[string]string{
	"The following packages will be upgraded:":                                        "upgraded",
	"The following NEW packages will be installed:":                                   "new",
	"The following packages will be REMOVED:":                                         "removed",
	"The following packages will be DOWNGRADED:":                                      "downgraded",
	"The following packages have been kept back:":                                     "kept_back",
	"The following packages were automatically installed and are no longer required:": "autoremovable",
	// the same section when there is one name in it (apt's ngettext: measured with apt 2.2.4, testdata/real/chroot-d11-autoremove-one-*)
	"The following package was automatically installed and is no longer required:": "autoremovable",
}

func unparsed(reason string) *Failure {
	return fail(CodePlanUnparsed, "apt's output was not understood: "+reason, map[string]any{"reason": reason})
}

// unparsedAt is unparsed for a line that was not understood: the line is said, as a detail may say a line of apt's (printable
// ASCII, an address reduced to its scheme and host, at most 200 characters), so that whoever reads the answer knows what apt wrote.
func unparsedAt(reason, line string) *Failure {
	shown := sanitizeLine(line)
	return fail(CodePlanUnparsed, "apt's output was not understood: "+reason+": "+shown, map[string]any{"reason": reason, "line": shown})
}

// summaryOf is the summary that a line of apt's is, when it is one (the numbers are of at most seven digits).
func summaryOf(l string) (Summary, bool) {
	m := summaryLine.FindStringSubmatch(l)
	if m == nil {
		return Summary{}, false
	}
	var n [6]int
	for i := 1; i < len(m); i++ {
		if len(m[i]) > 7 {
			return Summary{}, false
		}
		n[i], _ = strconv.Atoi(m[i])
	}
	return Summary{Upgraded: n[1], New: n[2], Downgraded: n[3], Remove: n[4], NotUpgraded: n[5]}, true
}

// lines of an output: carriage returns end a line too (apt's progress of a terminal is made of them), and a line that is
// longer than a line of apt is never, or has a control character, is not apt's.
func outputLines(text string) ([]string, *Failure) {
	lines := splitOutput(text)
	return lines, checkLines(lines)
}

// splitOutput is the lines of an output, whatever they say.
func splitOutput(text string) []string {
	return strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
}

// checkLines is plan_unparsed for a line that is longer than a line of apt is ever, or has a control character in it.
func checkLines(lines []string) *Failure {
	for _, l := range lines {
		if len(l) > maxParsedLine {
			return unparsed("line_too_long")
		}
		for i := 0; i < len(l); i++ {
			if c := l[i]; (c < 0x20 && c != '\t') || c == 0x7f {
				return unparsed("control_character")
			}
		}
	}
	return nil
}

// errorLines are the lines that say apt failed: those that start with one of the prefixes, with the lines that go on from
// them (apt indents the second line of an Err:, which is where it says 404 and Not Found).
func errorLines(lines []string, prefixes ...string) []string {
	var out []string
	on := false
	for _, l := range lines {
		switch {
		case slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(l, p) }):
			out, on = append(out, l), true
		case on && strings.HasPrefix(l, "  "):
			out = append(out, l)
		default:
			on = false
		}
	}
	return out
}

var urlPart = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)(?:[^\s/'"@]*@)?([^\s/'"?#]*)[^\s'"]*`)

// sanitizeLine is a line of apt's as it may be said: printable ASCII, a URL reduced to its scheme and host, and not more than
// maxDetailChars.
func sanitizeLine(l string) string {
	l = urlPart.ReplaceAllString(l, "${1}${2}")
	b := make([]byte, 0, len(l))
	for i := 0; i < len(l); i++ {
		c := l[i]
		if c < 0x20 || c > 0x7e {
			c = '?'
		}
		b = append(b, c)
	}
	l = strings.TrimSpace(string(b))
	if len(l) > maxDetailChars {
		l = l[:maxDetailChars-3] + "..."
	}
	return l
}

// SanitizeLine is sanitizeLine for the packages that say what a unit or apt printed: a line as it may be shown, printable ASCII, a URL reduced to its scheme and host, and cut.
func SanitizeLine(l string) string { return sanitizeLine(l) }

func detailOf(lines []string) string {
	var out []string
	for _, l := range lines {
		if len(out) == maxDetailLines {
			break
		}
		if s := sanitizeLine(l); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n")
}

// networkWords are what apt says when it cannot reach a host (methods/connect.cc, methods/http.cc and the resolver's own
// words): "Could not resolve 'host'", "Temporary failure resolving 'host'", "Could not connect to host:80 (ip), connection
// timed out", "Cannot initiate the connection to host:80 (ip). - connect (101: Network is unreachable)", "Connection failed",
// "Failed to connect". They are looked for in any case: apt does not keep to one.
var networkWords = []string{"could not resolve", "temporary failure", "could not connect", "network is unreachable", "no route to host", "timed out", "connection", "failed to connect"}

// updateKind says what went wrong in an update, from apt's fixed messages.
func updateKind(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(text, "NO_PUBKEY"):
		return KindNoPubkey
	case strings.Contains(text, "changed its"):
		return KindReleaseChange
	case strings.Contains(text, "404") || strings.Contains(text, "Not Found"):
		return KindNotFound
	case slices.ContainsFunc(networkWords, func(w string) bool { return strings.Contains(lower, w) }):
		return KindNetwork
	case strings.Contains(text, "Release file"):
		return KindReleaseChange
	}
	return KindOther
}

// ParseUpdate judges the output of the sandbox's "apt-get update --error-on=any": it passes when it ended with 0 and said
// nothing of E: or W:. A failure is sandbox_update_failed, with at most six lines of apt's, sanitised, and the kind of what
// went wrong; an output that was cut is plan_unparsed.
func ParseUpdate(out Output) *Failure {
	lines := splitOutput(out.Text)
	problems := errorLines(lines, "E:", "W:", "Err:")
	if out.ExitCode != 0 || slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "E:") || strings.HasPrefix(l, "W:") }) {
		detail := detailOf(problems)
		if detail == "" {
			detail = "apt-get update ended with status " + strconv.Itoa(out.ExitCode)
		}
		kind := updateKind(strings.Join(problems, "\n"))
		return fail(CodeSandboxUpdateFailed, detail, map[string]any{"kind": kind, "exit": out.ExitCode})
	}
	if out.Truncated {
		return unparsed("truncated")
	}
	return nil
}

// ParseSolver reads the output of "apt-get -s upgrade" or "apt-get -s dist-upgrade". An exit status that is not 0, or an E:
// line anywhere, is sandbox_solver_failed. The sections, the summary and the Inst, Remv and Purg lines must agree with each
// other: a count that is not the number of names, a section that appears twice, a name that is not a package's, a line of a
// shape that is not known (the line is said), a missing summary: plan_unparsed, never a partial answer.
//
// The one count that may be more than its list is the not-upgraded one: apt counts a package that is on hold and whose
// candidate is a downgrade (a pin of a priority over 1000) as not upgraded and does not name it among the kept back ones
// (measured, apt 2.2.4, testdata/real/chroot-d11-downgrade-held-*). It is never less.
func ParseSolver(out Output) (*SolverRun, *Failure) {
	lines := splitOutput(out.Text)
	errs := errorLines(lines, "E:")
	if out.ExitCode != 0 || len(errs) > 0 {
		detail := detailOf(errs)
		if detail == "" {
			detail = "apt-get -s ended with status " + strconv.Itoa(out.ExitCode)
		}
		return nil, fail(CodeSandboxSolverFailed, detail, map[string]any{"exit": out.ExitCode})
	}
	if bad := checkLines(lines); bad != nil {
		return nil, bad
	}
	if out.Truncated {
		return nil, unparsed("truncated")
	}

	run := &SolverRun{}
	seen := map[string]bool{}
	sections := map[string]*[]string{
		"upgraded": &run.Upgraded, "new": &run.New, "removed": &run.Removed, "kept_back": &run.KeptBack, "autoremovable": &run.AutoRemovable,
		"downgraded": &run.Downgraded,
	}
	var current *[]string
	haveSummary, inNote := false, false
	total := 0
	type lineRef struct {
		name string
		at   int
	}
	var remv []lineRef
	// the warning about essential packages: its first line, then its second, then the names that are indented
	const (
		essNone = iota
		essSecond
		essNames
	)
	essState, essSeen := essNone, false
	var essText []string
	endEssential := func() *Failure {
		if essState != essNames {
			return nil
		}
		essState = essNone
		for _, name := range strings.Fields(essentialDue.ReplaceAllString(strings.Join(essText, " "), " ")) {
			if !packageName.MatchString(name) {
				return unparsedAt("bad_name", name)
			}
			if total++; total > maxNames {
				return unparsed("too_many_names")
			}
			run.Essential = append(run.Essential, name)
		}
		return nil
	}
	for _, l := range lines {
		switch {
		case l == "":
			if f := endEssential(); f != nil {
				return nil, f
			}
			current, inNote = nil, false
			continue
		case current != nil && strings.HasPrefix(l, "  "):
			for _, name := range strings.Fields(l) {
				if !packageName.MatchString(strings.TrimSuffix(name, "*")) {
					return nil, unparsed("bad_name")
				}
				if total++; total > maxNames {
					return nil, unparsed("too_many_names")
				}
				if strings.HasSuffix(name, "*") {
					// the star of a purged package, in the list of the removed
					if current != &run.Removed {
						return nil, unparsed("bad_name")
					}
					name = strings.TrimSuffix(name, "*")
				}
				*current = append(*current, name)
			}
			continue
		case essState == essNames && strings.HasPrefix(l, "  "):
			essText = append(essText, l)
			continue
		case inNote && strings.HasPrefix(l, " "):
			continue
		}
		current, inNote = nil, false
		if f := endEssential(); f != nil {
			return nil, f
		}
		if essState == essSecond {
			if l != essentialSecondLine {
				return nil, unparsedAt("essential_block", l)
			}
			essState = essNames
			continue
		}
		if l == essentialFirstLine {
			if essSeen {
				return nil, unparsed("duplicate_section")
			}
			essSeen, essState = true, essSecond
			continue
		}
		if key, ok := sectionHeaders[l]; ok {
			if seen[key] {
				return nil, unparsed("duplicate_section")
			}
			seen[key] = true
			current = sections[key]
			*current = []string{}
			continue
		}
		switch {
		case strings.HasPrefix(l, "Inst "):
			inst, ok := parseInst(l)
			if !ok {
				return nil, unparsedAt("bad_inst_line", l)
			}
			if total++; total > maxNames {
				return nil, unparsed("too_many_names")
			}
			run.Inst = append(run.Inst, inst)
		case strings.HasPrefix(l, "Remv "), strings.HasPrefix(l, "Purg "):
			fields := strings.Fields(l)
			if len(fields) < 2 || !packageName.MatchString(fields[1]) {
				return nil, unparsedAt("bad_remv_line", l)
			}
			remv = append(remv, lineRef{name: fields[1], at: len(run.Inst)})
		case notFullyInstalled.MatchString(l):
			// not an apt that was not understood: the box has a package that dpkg left half done, which the real hop's apt refuses
			// too ("dpkg --audit" says which; the runbook's first block stops on it). The plan says that, and is not made.
			return nil, fail(CodePlanUnparsed, "a package is not fully installed or removed (apt says: "+sanitizeLine(l)+"): run dpkg --configure -a, then ask for the plan again",
				map[string]any{"reason": "not_fully_installed", "line": sanitizeLine(l)})
		case strings.HasPrefix(l, "Conf "):
		case summaryLine.MatchString(l):
			if haveSummary {
				return nil, unparsed("duplicate_summary")
			}
			haveSummary = true
			sum, ok := summaryOf(l)
			if !ok {
				return nil, unparsed("bad_summary")
			}
			run.Summary = sum
		case progress.MatchString(l), autoremove.MatchString(l), strings.HasPrefix(l, "W: "), strings.HasPrefix(l, "N: "):
		case strings.HasPrefix(l, "Need to get "), strings.HasPrefix(l, "After this operation, "):
		case strings.HasPrefix(l, "NOTE: This is only a simulation!"):
			inNote = true
		default:
			return nil, unparsedAt("unknown_line", l)
		}
	}
	if f := endEssential(); f != nil {
		return nil, f
	}
	if essState == essSecond {
		return nil, unparsed("essential_block")
	}
	if !haveSummary {
		return nil, unparsed("no_summary")
	}
	// the counts of the summary are the number of names of the sections
	for _, c := range []struct {
		name string
		want int
		got  []string
	}{
		{"upgraded", run.Summary.Upgraded, run.Upgraded},
		{"new", run.Summary.New, run.New},
		{"removed", run.Summary.Remove, run.Removed},
		{"downgraded", run.Summary.Downgraded, run.Downgraded},
	} {
		if len(c.got) != c.want {
			return nil, fail(CodePlanUnparsed, "apt's summary and its lists disagree: "+c.name, map[string]any{"reason": "summary_mismatch", "section": c.name})
		}
	}
	if len(run.KeptBack) > run.Summary.NotUpgraded {
		return nil, fail(CodePlanUnparsed, "apt's summary and its lists disagree: kept_back", map[string]any{"reason": "summary_mismatch", "section": "kept_back"})
	}
	// the names that apt warns of are packages that it removes
	for _, name := range run.Essential {
		if !slices.Contains(run.Removed, name) {
			return nil, unparsedAt("essential_not_removed", name)
		}
	}
	// the Inst lines agree with the lists of the upgraded, of the downgraded and of the new
	var instUpgraded, instDowngraded, instNew []string
	for _, in := range run.Inst {
		switch {
		case in.From == "":
			instNew = append(instNew, in.Name)
		case slices.Contains(run.Downgraded, in.Name):
			instDowngraded = append(instDowngraded, in.Name)
		default:
			instUpgraded = append(instUpgraded, in.Name)
		}
	}
	if !sameSet(instUpgraded, run.Upgraded) || !sameSet(instDowngraded, run.Downgraded) || !sameSet(instNew, run.New) {
		return nil, unparsed("inst_mismatch")
	}
	// the removals by their lines: a Remv or a Purg of a package that a later Inst line installs again is apt ordering a
	// replacement (the measured plan has "Remv cron" and then "Inst cron"), the others are removals, and they are the list
	var byLines []string
	for _, r := range remv {
		again := false
		for _, in := range run.Inst[r.at:] {
			if in.Name == r.name {
				again = true
				break
			}
		}
		if !again {
			byLines = append(byLines, r.name)
		}
	}
	if !sameSet(byLines, run.Removed) {
		return nil, unparsed("removal_mismatch")
	}
	return run, nil
}

// The two lines of apt's warning about essential packages (ShowEssential of apt-private/private-install.cc), and the names
// that follow, indented, each alone or followed by "(due to the package that makes it so)", wrapped between names.
const (
	essentialFirstLine  = "WARNING: The following essential packages will be removed."
	essentialSecondLine = "This should NOT be done unless you know exactly what you are doing!"
)

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		// the same name twice in one of them is not the same set as a list of unique names of the other
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// parseInst reads "Inst NAME [FROM] (TO TAG [ARCH]) [what depends on it]":
//
//	Inst base-files [11.1+deb11u11] (12.4+deb12u15 Debian:12.15/oldstable [amd64])
//	Inst libsemanage2 (3.4-1+b5 Debian:12.15/oldstable [amd64]) [libsemanage1:amd64 ]
//	Inst python3-urllib3 [1.26.5-1] (1.26.12-1+deb12u4 Debian:12.15/oldstable, Debian-Security:12/oldstable-security [all])
func parseInst(line string) (InstLine, bool) {
	rest := strings.TrimPrefix(line, "Inst ")
	name, rest, found := strings.Cut(rest, " ")
	if !found || !packageName.MatchString(name) {
		return InstLine{}, false
	}
	inst := InstLine{Name: name}
	if strings.HasPrefix(rest, "[") {
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return InstLine{}, false
		}
		inst.From = rest[1:end]
		if inst.From == "" {
			return InstLine{}, false
		}
		rest = strings.TrimLeft(rest[end+1:], " ")
	}
	if !strings.HasPrefix(rest, "(") {
		return InstLine{}, false
	}
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		return InstLine{}, false
	}
	inside := rest[1:end]
	to, tag, _ := strings.Cut(inside, " ")
	if to == "" {
		return InstLine{}, false
	}
	// the architecture closes the tag: "... [amd64]"
	if i := strings.LastIndex(tag, " ["); i >= 0 && strings.HasSuffix(tag, "]") {
		tag = tag[:i]
	}
	inst.To, inst.Tag = to, strings.TrimSpace(tag)
	return inst, true
}

// ---- the sizes -----------------------------------------------------------------------------------------------------

// Estimates are what the plan can say of the download and of the disk. The sizes are apt's own words, but for a transaction with nothing to do: apt prints none for it, and they
// are known to be zero (ParseEstimate). When the text is none of the ends that are known there are no sizes, and Known is false: never a guess.
type Estimates struct {
	Known bool `json:"known"`
	// DownloadBytes is the size of the archives of the transaction, whole (apt says "0 B/140 MB" for what is still to fetch
	// and what there is in all: the whole is the safe one). ExtraBytes is the disk it takes, negative when it is freed.
	DownloadBytes *int64 `json:"download_bytes,omitempty"`
	ExtraBytes    *int64 `json:"extra_bytes,omitempty"`
}

const trivialOnlyError = "E: Trivial Only specified but this is not a trivial operation."

// lowRoomError is the line that apt ends the estimate with when the file system of the sandbox's cache has less room than the
// download (the path is the one apt prints: the sandbox's own archives directory, with its slash and the full stop).
func lowRoomError(dir string) string {
	return "E: You don't have enough free space in " + dir + "/cache/archives/."
}

// sizeNumber is a number as apt prints a size: digits, in groups of three with a comma when it has any ("1,234"), and a
// decimal part with a point. A comma that does not group by three is not a thousands separator ("56,8" is a decimal comma that
// a locale would write): it is no number of apt's, and the size is not known, instead of ten times what it is.
const sizeNumber = `(?:[0-9]{1,3}(?:,[0-9]{3})+|[0-9]+)(?:\.[0-9]+)?`

var (
	needLine    = regexp.MustCompile(`^Need to get (` + sizeNumber + ` ?[kKMGT]?B)(?:/(` + sizeNumber + ` ?[kKMGT]?B))? of archives\.$`)
	usedLine    = regexp.MustCompile(`^After this operation, (` + sizeNumber + ` ?[kKMGT]?B) of additional disk space will be used\.$`)
	freedLine   = regexp.MustCompile(`^After this operation, (` + sizeNumber + ` ?[kKMGT]?B) disk space will be freed\.$`)
	sizeWords   = regexp.MustCompile(`^(` + sizeNumber + `) ?([kKMGT]?B)$`)
	maxPlausibe = int64(1e12)
)

// parseSize is a size as apt prints it ("56.8 MB", "182 MB", "3288 B", "1,234 kB"): apt's units are decimal.
func parseSize(s string) (int64, bool) {
	m := sizeWords.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	if err != nil {
		return 0, false
	}
	unit := map[string]float64{"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12}[m[2]]
	n := int64(math.Round(v * unit))
	if n < 0 || n > maxPlausibe {
		return 0, false
	}
	return n, true
}

// ParseEstimate reads the output of the sandbox's "apt-get -o APT::Get::Trivial-Only=true dist-upgrade" run in the sandbox
// directory dir. It has three normal ends. Two have exit status 100 and come after "Need to get ..." and "After this operation
// ...": the line "E: Trivial Only specified but this is not a trivial operation." and, when the file system of the sandbox's
// cache has less room than the download (apt checks it before it stops at the trivial operation: measured in a Debian 11 chroot
// with apt 2.2.4 and a small tmpfs, testdata/real/chroot-d11-12-lowroom-size.txt), the line "E: You don't have enough free
// space in <dir>/cache/archives/." That second line is the sandbox's own cache and no other directory (an other E: line is not
// ended normally and the sizes are not known), and it does not make the sizes any less true: they are printed above it. It must
// be the same transaction as the simulation's (the same summary): anything else, an other shape of the sentences, a cut output,
// is Known false, and no number. The third end is that of a box with nothing to do (testdata/real/chroot-d11-nothing-size.txt):
// apt has no operation to stop at, ends with status 0 and prints its summary of zeros and neither error nor sizes; it did a
// real dist-upgrade of nothing, and the sizes are known, and zero. That end is decided by the summary and by what must not be in
// the text, and not by every line being one that is classified here: exactly one summary, the same as the simulation's, with
// nothing upgraded, installed, removed or downgraded, and no error, no "Need to get", no "After this operation" and no pending
// work of dpkg (that one apt stops at, with its error). The other lines are not looked at on purpose: above that summary apt
// prints the section of the packages that are no longer required (testdata/real/chroot-d11-autoremove-one-nothing-size.txt, the
// usual text of a box with nothing to upgrade), the packages that are kept back, or a warning, and none of them is an operation.
// A status 0 with another summary, or with one of the lines that must not be there, is Known false.
func ParseEstimate(out Output, simulated Summary, dir string) Estimates {
	return readEstimate(out, simulated, dir, false)
}

// ParseURIEstimate reads the output of the sandbox's "apt-get --print-uris install ..." of the packages of a module, whose transaction is the one that the simulation next to it has the
// summary of. That command installs nothing and downloads nothing: it says "Need to get ..." and "After this operation ...", prints the address of each archive and ends with status 0
// (testdata/realmodule, apt 2.2.4 and 2.6.1), so a status 0 is its normal end, and a transaction that has work in it ends so; and a transaction with nothing to do ends so with its
// summary of zeros and neither of the two lines, as the estimate of the upgrade does. The rest is as in ParseEstimate, the sizes only for the same transaction as the simulation's and
// nothing for an output that is cut or not understood, with these differences: any E: line is a command that did not end as it should (this command has no end that is an error, and does
// not look at the room of the cache), the lines that name the addresses are not read, and a package that dpkg left half done is not a thing to size.
func ParseURIEstimate(out Output, simulated Summary) Estimates {
	return readEstimate(out, simulated, "", true)
}

func readEstimate(out Output, simulated Summary, dir string, uris bool) Estimates {
	if out.Truncated || (out.ExitCode != 100 && out.ExitCode != 0) || (uris && out.ExitCode != 0) {
		return Estimates{}
	}
	lines, bad := outputLines(out.Text)
	if bad != nil {
		return Estimates{}
	}
	var need, after []string
	sums, ends, pending := 0, 0, 0
	var sum Summary
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "E:"):
			if uris || (l != trivialOnlyError && l != lowRoomError(dir)) {
				return Estimates{}
			}
			ends++
		case strings.HasPrefix(l, "Need to get "):
			need = append(need, l)
		case strings.HasPrefix(l, "After this operation, "):
			after = append(after, l)
		case summaryLine.MatchString(l):
			var ok bool
			if sum, ok = summaryOf(l); !ok {
				return Estimates{}
			}
			sums++
		case notFullyInstalled.MatchString(l):
			pending++
		}
	}
	if out.ExitCode == 0 && !(uris && len(need)+len(after) > 0) {
		if ends != 0 || len(need) != 0 || len(after) != 0 || pending != 0 || sums != 1 || sum != simulated ||
			sum.Upgraded != 0 || sum.New != 0 || sum.Remove != 0 || sum.Downgraded != 0 {
			return Estimates{}
		}
		download, extra := int64(0), int64(0)
		return Estimates{Known: true, DownloadBytes: &download, ExtraBytes: &extra}
	}
	if (!uris && ends != 1) || len(need) != 1 || len(after) != 1 || sums != 1 || sum != simulated || (uris && pending != 0) {
		return Estimates{}
	}
	m := needLine.FindStringSubmatch(need[0])
	if m == nil {
		return Estimates{}
	}
	whole := m[1]
	if m[2] != "" {
		whole = m[2]
	}
	download, ok := parseSize(whole)
	if !ok {
		return Estimates{}
	}
	var extra int64
	if u := usedLine.FindStringSubmatch(after[0]); u != nil {
		extra, ok = parseSize(u[1])
	} else if f := freedLine.FindStringSubmatch(after[0]); f != nil {
		extra, ok = parseSize(f[1])
		extra = -extra
	} else {
		ok = false
	}
	if !ok {
		return Estimates{}
	}
	return Estimates{Known: true, DownloadBytes: &download, ExtraBytes: &extra}
}
