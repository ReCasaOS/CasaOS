package osplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// HopSpec is a hop that a plan can be made for: the release left, the one reached, and what the sources have to say about
// them.
type HopSpec struct {
	ID osreadiness.HopID
	// From and To are the codenames of the releases ("bullseye", "bookworm").
	From, To string
	// Later are the codenames of the releases after To: a source that names one is already past the target.
	Later []string
	// Firmware says that the target has the component non-free-firmware, which is put beside non-free.
	Firmware bool
}

// planHops is the one table of the hops a plan can be made for. Debian 12 to 13 (deb822, non-free-firmware, new clues) and
// Ubuntu are later rungs: every other hop is hop_not_planned.
var planHops = []HopSpec{
	{ID: osreadiness.HopDebian11to12, From: "bullseye", To: "bookworm", Later: []string{"trixie", "forky"}, Firmware: true},
}

// PlannedHop is the hop of a box's identity, when a plan can be made for it.
func PlannedHop(id osreadiness.HopID) (HopSpec, bool) {
	for _, h := range planHops {
		if h.ID == id {
			return h, true
		}
	}
	return HopSpec{}, false
}

// The tree: the files the sandbox reads (and the runbook writes to /etc/apt), as their path in the tree and their content.
type TreeFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// A LineChange is one line of a file that the rewrite changed, as it is shown: Before and After are text, bounded, with
// the user and the query of a URL replaced by ***. Why is what was done, in a fixed vocabulary, comma-separated:
// suite, host, firmware (a line that was rewritten), dropped (a backports or proposed-updates line, commented), paused (a
// line that is not the distribution's, commented, or in a file that is put aside).
type LineChange struct {
	N      int    `json:"n"`
	Before string `json:"before"`
	After  string `json:"after"`
	Why    string `json:"why"`
}

// The actions on a file of sources.
const (
	ActionRewritten = "rewritten"
	ActionPaused    = "paused"
	ActionUnchanged = "unchanged"
)

// A FileChange is what became of one file apt reads: File is its name ("sources.list", "docker.list").
type FileChange struct {
	File   string       `json:"file"`
	Action string       `json:"action"`
	Lines  []LineChange `json:"lines"`
}

// Paused is a source that the hop puts aside: a file of sources.list.d (File is its name, Line is 0), or a line of
// sources.list (Line is its number). Hosts are scheme and host only. Docker says that every line of it is Docker's own
// package source (download.docker.com).
type Paused struct {
	File   string   `json:"file"`
	Line   int      `json:"line,omitempty"`
	Hosts  []string `json:"hosts"`
	Docker bool     `json:"docker"`
}

// Rewritten is the sources of the box as the hop leaves them, and what changed.
type Rewritten struct {
	Files  []FileChange
	Tree   []TreeFile
	Paused []Paused
	// DroppedBackports is how many lines were dropped (backports, proposed-updates).
	DroppedBackports int
	// FirmwareAdded is how many lines got the component non-free-firmware.
	FirmwareAdded int
	// HashCurrent is the hash of the files as they are (their names and bytes, sorted), and HashGenerated that of the tree.
	HashCurrent, HashGenerated string
}

// Decider says whether a suite of a source is the distribution's own: *osreadiness.DistroDecider is the one that answers
// for the box.
type Decider interface {
	Suite(ctx context.Context, uri, suite string) (distro, decided bool, err error)
}

const (
	maxLineBytes = 4096
	// maxShown is the most of a line that is shown.
	maxShown    = 400
	pausedMark  = "# casaos-paused: "
	droppedMark = "# casaos-dropped: "
)

// srcLine is a line of a file of sources, as the strict parser reads it.
type srcLine struct {
	n       int
	text    string // as written, its trailing white space cut
	repo    bool
	typ     string // deb or deb-src
	opts    string // "[...]" as written, or ""
	uri     string
	suite   string
	comps   []string
	comment string // from the first #, or ""
}

// lineProblem is why a line is not understood: a Failure of the file and line.
type lineProblem struct {
	code   string
	reason string
}

func (p lineProblem) failure(file string, line int) *Failure {
	data := map[string]any{"file": file, "reason": p.reason}
	where := file
	if line > 0 {
		data["line"] = line
		where = fmt.Sprintf("%s line %d", file, line)
	}
	return fail(p.code, where+": "+reasonText(p.reason), data)
}

func reasonText(reason string) string {
	switch reason {
	case "deb822":
		return "a .sources file (deb822) is not read by this plan"
	case "cdrom":
		return "a disc or a local directory cannot be upgraded from"
	case "alias":
		return "the suite is an alias that moves with the archive: name the release"
	case "target":
		return "the source is already at the target release or past it (if an earlier try of the upgrade by hand rewrote the sources, the folder of that plan in /var/backups, casaos-os-upgrade-<plan id>, has restore-sources.sh: run it with sh; when that folder has begun-upgrade the upgrade has begun and only goes forward)"
	case "undecided":
		return "whether this repository is the distribution's own cannot be known from the files apt keeps of it: run apt-get update first"
	}
	return "this is not a form the plan reads (" + reason + ")"
}

var (
	asciiToken = regexp.MustCompile("^[\\x21-\\x7e]+$")
	// the words of a distribution line: a suite and a component name are names, an address is http or https
	suiteToken  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+/-]*$`)
	distroURI   = regexp.MustCompile(`^(?i:https?)://[A-Za-z0-9._~%!$&()*+,;=:@-]+(?:/[A-Za-z0-9._~%!$&()*+,;=:@/-]*)?$`)
	anyURI      = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*)://([^/\s]+)(/\S*)?$`)
	optionWord  = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9-]*)[+-]?=([A-Za-z0-9._,:/@+~%-]*)$`)
	weakOptions = []string{"trusted", "allow-insecure", "allow-weak", "allow-downgrade-to-insecure"}
)

// parseSourceFile reads a .list file strictly: every line is blank, a comment, or "deb|deb-src [options] URI SUITE
// [components...]" (a comment may follow, as apt reads it); anything else is a problem, and the plan never guesses.
func parseSourceFile(content []byte) (lines []srcLine, bad *lineProblem, badLine int) {
	if !utf8.Valid(content) {
		return nil, &lineProblem{CodeSourcesFormat, "encoding"}, 0
	}
	for i := 0; i < len(content); i++ {
		switch b := content[i]; {
		case b == 0:
			return nil, &lineProblem{CodeSourcesFormat, "nul"}, lineOf(content, i)
		case b == '\r':
			return nil, &lineProblem{CodeSourcesFormat, "crlf"}, lineOf(content, i)
		case b < 0x20 && b != '\n' && b != '\t', b == 0x7f:
			return nil, &lineProblem{CodeSourcesFormat, "control"}, lineOf(content, i)
		}
	}
	// the characters that are valid text and are not text: a comment may hold any UTF-8, and one that reorders what is shown
	// (the bidirectional controls), that a terminal acts on (the C1 controls, U+0080 to U+009F) or that is not seen (the
	// format characters: zero-width ones, the byte order mark; the line and paragraph separators) is not one that the plan
	// shows to the owner or writes into the runbook
	for i, r := range string(content) {
		if r >= 0x80 && (r <= 0x9f || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)) {
			return nil, &lineProblem{CodeSourcesFormat, "control"}, lineOf(content, i)
		}
	}
	text := string(content)
	raw := strings.Split(text, "\n")
	if raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}
	repos := 0
	for i, l := range raw {
		n := i + 1
		if len(l) > maxLineBytes {
			return nil, &lineProblem{CodeSourcesFormat, "long_line"}, n
		}
		line := srcLine{n: n, text: strings.TrimRight(l, " \t")}
		code, comment, hasComment := strings.Cut(l, "#")
		if hasComment {
			line.comment = strings.TrimRight("#"+comment, " \t")
		}
		code = strings.TrimSpace(code)
		if code != "" {
			if p := parseRepo(&line, code); p != nil {
				return nil, p, n
			}
			if repos++; repos > osreadiness.MaxSourceEntries {
				return nil, &lineProblem{CodeSourcesFormat, "too_many"}, n
			}
		}
		lines = append(lines, line)
	}
	return lines, nil, 0
}

func lineOf(content []byte, at int) int {
	n := 1
	for _, b := range content[:at] {
		if b == '\n' {
			n++
		}
	}
	return n
}

// parseRepo reads the code of a line (what is before its comment) as a repository.
func parseRepo(line *srcLine, code string) *lineProblem {
	bad := func(reason string) *lineProblem { return &lineProblem{CodeSourcesFormat, reason} }
	if !isPrintableASCII(code) {
		return bad("chars")
	}
	cut := strings.IndexAny(code, " \t")
	if cut < 0 || (code[:cut] != "deb" && code[:cut] != "deb-src") {
		return bad("type")
	}
	typ, rest := code[:cut], strings.TrimLeft(code[cut:], " \t")
	line.repo, line.typ = true, typ
	if strings.HasPrefix(rest, "[") {
		end := strings.IndexByte(rest, ']')
		if end < 0 || strings.ContainsAny(rest[1:end], "[") {
			return bad("options")
		}
		if end+1 < len(rest) && rest[end+1] != ' ' && rest[end+1] != '\t' {
			return bad("options")
		}
		line.opts, rest = rest[:end+1], strings.TrimLeft(rest[end+1:], " \t")
	}
	if low := strings.ToLower(rest); strings.HasPrefix(low, "cdrom:") || strings.HasPrefix(low, "file:") || strings.HasPrefix(low, "copy:") {
		return &lineProblem{CodeSourcesCDROM, "cdrom"}
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return bad("fields")
	}
	if strings.ContainsAny(rest, "[]") {
		return bad("options")
	}
	if len(fields)-2 > osreadiness.MaxSourceComponents {
		return bad("too_many")
	}
	line.uri, line.suite, line.comps = fields[0], fields[1], fields[2:]
	if !anyURI.MatchString(line.uri) {
		return bad("uri")
	}
	return nil
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 || c > 0x7e) && c != '\t' {
			return false
		}
	}
	return true
}

// distroProblem checks a line that is the distribution's own beyond the shape: what it says is words the rewrite can put
// back, an address that is http or https, and options that keep apt's checks (a source that is trusted by the file is not
// one a plan can say anything of).
func distroProblem(l *srcLine) *lineProblem {
	if !distroURI.MatchString(l.uri) {
		return &lineProblem{CodeSourcesFormat, "uri"}
	}
	if !suiteToken.MatchString(l.suite) {
		return &lineProblem{CodeSourcesFormat, "suite"}
	}
	for _, c := range l.comps {
		if !suiteToken.MatchString(c) {
			return &lineProblem{CodeSourcesFormat, "component"}
		}
	}
	if l.opts != "" {
		words := strings.Fields(l.opts[1 : len(l.opts)-1])
		if len(words) == 0 {
			return &lineProblem{CodeSourcesFormat, "options"}
		}
		for _, w := range words {
			m := optionWord.FindStringSubmatch(w)
			if m == nil {
				return &lineProblem{CodeSourcesFormat, "options"}
			}
			if slices.Contains(weakOptions, strings.ToLower(m[1])) && !osreadiness.AptFalse(m[2]) {
				return &lineProblem{CodeSourcesFormat, "trusted"}
			}
		}
	}
	return nil
}

// moveHost is the address with the archive's host moved back to the mirror: the archive has no next release. The host is
// compared as the address says it, without its user and its port, which are kept in what is written: a line that names the
// archive with a port is the archive.
func moveHost(uri string) (string, bool) {
	scheme, rest, found := strings.Cut(uri, "://")
	if !found {
		return uri, false
	}
	authority, tail, hasTail := strings.Cut(rest, "/")
	user, hostPort := "", authority
	if i := strings.LastIndexByte(authority, '@'); i >= 0 {
		user, hostPort = authority[:i+1], authority[i+1:]
	}
	host, port := hostPort, ""
	if i := strings.LastIndexByte(hostPort, ':'); i >= 0 {
		host, port = hostPort[:i], hostPort[i:]
	}
	if !strings.EqualFold(host, "archive.debian.org") {
		return uri, false
	}
	out := scheme + "://" + user + "deb.debian.org" + port
	if hasTail {
		out += "/" + tail
	}
	return out, true
}

// rewriteDistro is a line of the distribution's own archive for the target: its text, why, and the problem that stops it.
func (h HopSpec) rewriteDistro(l *srcLine) (text, why string, dropped bool, p *lineProblem) {
	if osreadiness.IsSuiteAlias(l.suite) {
		return "", "", false, &lineProblem{CodeSourcesAlias, "alias"}
	}
	base, suffix := osreadiness.SplitSuite(l.suite)
	switch {
	case base == h.To || slices.Contains(h.Later, base):
		return "", "", false, &lineProblem{CodeSourcesTarget, "target"}
	case base != h.From:
		return "", "", false, &lineProblem{CodeSourcesFormat, "suite"}
	}
	var suite string
	switch suffix {
	case "":
		suite = h.To
	case "-updates", "-security":
		suite = h.To + suffix
	case "-backports", "-backports-sloppy", "/updates", "-proposed-updates":
		return droppedMark + strings.TrimSpace(l.text), "dropped", true, nil
	default:
		return "", "", false, &lineProblem{CodeSourcesFormat, "suite"}
	}
	reasons := []string{"suite"}
	uri, moved := moveHost(l.uri)
	if moved {
		reasons = append(reasons, "host")
	}
	comps := slices.Clone(l.comps)
	if h.Firmware && slices.Contains(comps, "non-free") && !slices.Contains(comps, "non-free-firmware") {
		comps = append(comps, "non-free-firmware")
		reasons = append(reasons, "firmware")
	}
	parts := []string{l.typ}
	if l.opts != "" {
		parts = append(parts, l.opts)
	}
	parts = append(parts, uri, suite)
	parts = append(parts, comps...)
	text = strings.Join(parts, " ")
	if l.comment != "" {
		text += " " + l.comment
	}
	return text, strings.Join(reasons, ","), false, nil
}

// hostOf is "scheme://host" of an address, lower case, with no user, port, path or query.
func hostOf(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname())
}

// A URL in a line that is shown has its user and its query replaced by ***: the tree that goes to the runbook has what the
// file had, because the machine needs it.
var urlInText = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s\]]+`)

func maskURL(u string) string {
	scheme, rest, _ := strings.Cut(u, "://")
	query := ""
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest, query = rest[:i], "?***"
	}
	authority, tail := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		authority, tail = rest[:i], rest[i:]
	}
	if i := strings.LastIndexByte(authority, '@'); i >= 0 {
		authority = "***" + authority[i:]
	}
	return scheme + "://" + authority + tail + query
}

// shown is a line as it is shown: no secret of a URL, and no more than maxShown bytes.
func shown(s string) string {
	s = urlInText.ReplaceAllStringFunc(s, maskURL)
	if len(s) > maxShown {
		s = s[:maxShown-3]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "..."
	}
	return s
}

type hashed struct {
	path    string
	content string
}

// canonicalHash is the SHA-256 of files, sorted by path, each as its path, its length and its bytes, each ended by a NUL
// (the runbook computes the same from the machine with sha256sum, and a test proves they agree).
func canonicalHash(files []hashed) string {
	sorted := slices.Clone(files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].path < sorted[j].path })
	h := sha256.New()
	for _, f := range sorted {
		h.Write([]byte(f.path + "\x00" + strconv.Itoa(len(f.content)) + "\x00" + f.content + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// classified is a file whose lines have been read and asked about.
type classified struct {
	file   osreadiness.SourceFile
	lines  []srcLine
	distro []bool // per line, for the repo lines
}

// RewriteSources rewrites the sources of the box to the target release, in memory: the tree the sandbox reads (and the
// hop writes), and what changed. It refuses, with a *Failure (sources_format, sources_alias, sources_target,
// sources_mixed_file, sources_cdrom) naming the file and the line, anything it does not read: it never guesses, and never
// leaves as it is a line it did not understand. An error that is not a *Failure is the end of the time.
func RewriteSources(ctx context.Context, hop HopSpec, files []osreadiness.SourceFile, decider Decider) (*Rewritten, error) {
	// the files that cannot be read at all come first, wherever they are
	for _, f := range files {
		switch {
		case f.Err != nil:
			return nil, failf(CodeSourcesFormat, map[string]any{"file": f.Name, "reason": "unreadable"}, "%s cannot be read", f.Name)
		case strings.HasSuffix(f.Name, ".sources"):
			return nil, failf(CodeSourcesFormat, map[string]any{"file": f.Name, "reason": "deb822"}, "%s: %s", f.Name, reasonText("deb822"))
		}
	}
	var current []hashed
	var cls []classified
	totalRepos := 0
	for _, f := range files {
		name := f.Name
		current = append(current, hashed{path: currentPath(f), content: string(f.Content)})
		lines, bad, badLine := parseSourceFile(f.Content)
		if bad != nil {
			return nil, bad.failure(name, badLine)
		}
		c := classified{file: f, lines: lines, distro: make([]bool, len(lines))}
		for i := range lines {
			l := &lines[i]
			if !l.repo {
				continue
			}
			if totalRepos++; totalRepos > osreadiness.MaxSourceEntries {
				return nil, &Failure{Code: CodeSourcesFormat, Detail: "more sources than a machine has", Data: map[string]any{"file": name, "line": l.n, "reason": "too_many"}}
			}
			distro, decided, err := decider.Suite(ctx, l.uri, l.suite)
			if err != nil {
				return nil, err
			}
			if !decided {
				return nil, lineProblem{CodeSourcesFormat, "undecided"}.failure(name, l.n)
			}
			c.distro[i] = distro
		}
		cls = append(cls, c)
	}

	out := &Rewritten{HashCurrent: canonicalHash(current), Files: []FileChange{}, Paused: []Paused{}}
	sources := ""
	distroKept := 0
	for _, c := range cls {
		name := c.file.Name
		changes := []LineChange{}
		repos, distros := 0, 0
		allDocker := true
		hosts := []string{}
		for i, l := range c.lines {
			if !l.repo {
				continue
			}
			repos++
			if c.distro[i] {
				distros++
			} else {
				allDocker = allDocker && osreadiness.SourceHost(l.uri) == osreadiness.DockerHost
				if h := hostOf(l.uri); h != "" && !slices.Contains(hosts, h) {
					hosts = append(hosts, h)
				}
			}
		}
		if c.file.InParts && repos > 0 && distros > 0 && distros < repos {
			return nil, failf(CodeSourcesMixedFile, map[string]any{"file": name},
				"%s has repositories of the distribution and of others: it cannot be put aside as a file and rewritten as a file", name)
		}
		if c.file.InParts && repos > 0 && distros == 0 {
			// the whole file is put aside: it is not in the tree
			slices.Sort(hosts)
			out.Paused = append(out.Paused, Paused{File: name, Hosts: hosts, Docker: allDocker})
			for _, l := range c.lines {
				if l.repo {
					changes = append(changes, LineChange{N: l.n, Before: shown(l.text), Why: "paused"})
				}
			}
			out.Files = append(out.Files, FileChange{File: name, Action: ActionPaused, Lines: changes})
			continue
		}
		for i, l := range c.lines {
			if !l.repo {
				continue
			}
			var text, why string
			switch {
			case !c.distro[i]:
				text, why = pausedMark+strings.TrimSpace(l.text), "paused"
				lineHosts := []string{}
				if h := hostOf(l.uri); h != "" {
					lineHosts = append(lineHosts, h)
				}
				out.Paused = append(out.Paused, Paused{File: name, Line: l.n, Hosts: lineHosts, Docker: osreadiness.SourceHost(l.uri) == osreadiness.DockerHost})
			default:
				if p := distroProblem(&c.lines[i]); p != nil {
					return nil, p.failure(name, l.n)
				}
				var dropped bool
				var p *lineProblem
				text, why, dropped, p = hop.rewriteDistro(&c.lines[i])
				if p != nil {
					return nil, p.failure(name, l.n)
				}
				if dropped {
					out.DroppedBackports++
				} else {
					distroKept++
					if strings.Contains(why, "firmware") {
						out.FirmwareAdded++
					}
				}
			}
			// what the rewrite writes (a component added, the mark of a paused or of a dropped line) can make a line that was
			// within the limit longer than it: it is refused here, in the words of the file, and is not the tree's own bug
			if len(text) > maxLineBytes {
				return nil, lineProblem{CodeSourcesFormat, "long_line"}.failure(name, l.n)
			}
			c.lines[i].text = text
			changes = append(changes, LineChange{N: l.n, Before: shown(strings.TrimSpace(l.text)), After: shown(text), Why: why})
		}
		action := ActionUnchanged
		if len(changes) > 0 {
			action = ActionRewritten
		}
		out.Files = append(out.Files, FileChange{File: name, Action: action, Lines: changes})
		content := joinLines(c.lines)
		if c.file.InParts {
			out.Tree = append(out.Tree, TreeFile{Path: "parts/" + name, Content: content})
		} else {
			sources = content
		}
	}
	if distroKept == 0 {
		return nil, fail(CodeSourcesFormat, "no source of the distribution is left to plan against", map[string]any{"reason": "no_distribution_line"})
	}
	sort.Slice(out.Paused, func(i, j int) bool {
		if out.Paused[i].File != out.Paused[j].File {
			return out.Paused[i].File < out.Paused[j].File
		}
		return out.Paused[i].Line < out.Paused[j].Line
	})
	// sources.list is always in the tree, empty when the box has none
	out.Tree = append(out.Tree, TreeFile{Path: "sources.list", Content: sources})
	sort.Slice(out.Tree, func(i, j int) bool { return out.Tree[i].Path < out.Tree[j].Path })
	generated := make([]hashed, 0, len(out.Tree))
	for _, f := range out.Tree {
		generated = append(generated, hashed{path: f.Path, content: f.Content})
	}
	out.HashGenerated = canonicalHash(generated)
	if err := checkTree(hop, out.Tree); err != nil {
		return nil, err
	}
	return out, nil
}

func currentPath(f osreadiness.SourceFile) string {
	if f.InParts {
		return "sources.list.d/" + f.Name
	}
	return f.Name
}

// joinLines is the lines of a file, ended by \n, with the changed lines in place of the ones they were.
func joinLines(lines []srcLine) string {
	if len(lines) == 0 {
		return ""
	}
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.text
	}
	return strings.Join(texts, "\n") + "\n"
}

// checkTree is the tree read again by the strict parser: a line that it does not accept, or a distribution line that is not
// of the target, is the rewrite's own bug, and nothing is returned.
func checkTree(hop HopSpec, tree []TreeFile) error {
	for _, f := range tree {
		lines, bad, n := parseSourceFile([]byte(f.Content))
		if bad != nil {
			return failf(CodeInternal, nil, "the tree's %s line %d is not read by the strict parser (%s)", f.Path, n, bad.reason)
		}
		for _, l := range lines {
			if !l.repo {
				continue
			}
			if base, _ := osreadiness.SplitSuite(l.suite); base == hop.From {
				return failf(CodeInternal, nil, "the tree's %s line %d is still of %s", f.Path, l.n, hop.From)
			}
		}
	}
	return nil
}
