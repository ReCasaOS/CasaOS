package osreadiness

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// sourceEntry is one repository of apt's sources: a "deb" line, or one type and one URI of a
// stanza of a .sources file.
type sourceEntry struct {
	// File is the name of the file it is in ("sources.list", "docker.list").
	File       string
	Kind       string // "deb" or "deb-src"
	URI        string
	Suites     []string
	Components []string
	// Distro says that the suites of the source are the distribution's own archive. It is decided once for the report, by
	// markDistro (see distro), which makes two sources of one that has suites of both kinds.
	Distro bool
}

// aptSourceName is a name apt reads in sources.list.d (GetListOfFilesInDir of apt 2.2.4 and 2.6.1, Debian
// 11 and 12): letters, digits, "_", "-" and ":", and "." since the extension is required, .list or
// .sources, and not a name that starts with a dot. apt ignores every other name, and so does this.
var aptSourceName = regexp.MustCompile(`^[A-Za-z0-9_:-][A-Za-z0-9_:.-]*\.(?:list|sources)$`)

// maxSourceFiles and maxSourceEntries bound what a machine's sources can cost: a file of more sources
// than a machine has is one that is not understood (sources_format says so), and more files than that
// are an answer that is too large.
const (
	maxSourceFiles = 500
	// maxSourceEntries is the most sources of a file, and of all the files together
	maxSourceEntries = 2000
	// maxSourceSuites and maxSourceComponents are the most different suites and components of one source: a
	// stanza of more is a file that is not understood, and not one that is multiplied by them
	maxSourceSuites     = 64
	maxSourceComponents = 64
	// maxReleaseLookups is how many Release files are looked at, in all, to tell the mirrors of the
	// distribution from the others; releaseHeadBytes is how much of each is read, and maxOriginLen the longest
	// Origin that is one
	maxReleaseLookups = 64
	releaseHeadBytes  = 8 << 10
	maxOriginLen      = 64
)

// listsDir is where apt keeps the Release files it has fetched of each source.
const listsDir = "/var/lib/apt/lists/"

// sourcesSet is what apt would read of its sources: the entries of the files it understands, and
// the files it would not (which the rewriter of the plan could not read either).
type sourcesSet struct {
	Entries []sourceEntry
	// Unreadable are the files that could not be read or understood.
	Unreadable []string
}

// sourcesMemo is the sources of one report, read once: the checks of the sources are eight and ask for
// the same files, and reading and understanding each file eight times is eight times the work of a file
// that may be as large as a file can be. It is read in the time of the report, and each check that asks
// waits for it only for its own time.
type sourcesMemo struct {
	ctx  context.Context
	p    Probe
	once sync.Once
	done chan struct{}
	set  sourcesSet
	err  error
}

func newSourcesMemo(ctx context.Context, p Probe) *sourcesMemo {
	return &sourcesMemo{ctx: ctx, p: p, done: make(chan struct{})}
}

func (m *sourcesMemo) get(ctx context.Context) (sourcesSet, error) {
	m.once.Do(func() {
		go func() {
			defer close(m.done)
			defer func() {
				if recover() != nil {
					m.set, m.err = sourcesSet{}, errPanicked
				}
			}()
			m.set, m.err = loadSources(m.ctx, m.p)
		}()
	})
	select {
	case <-m.done:
		return m.set, m.err
	case <-ctx.Done():
		return sourcesSet{}, ctx.Err()
	}
}

// sourcesOf is the sources of the report a check runs in, read once for all the checks; a check that is
// run alone, with no report, reads them itself.
func sourcesOf(ctx context.Context, p Probe) (sourcesSet, error) {
	if m := SessionOf(ctx).sources; m != nil {
		return m.get(ctx)
	}
	return loadSources(ctx, p)
}

// readRegular is the content of a file that may not be there, as apt reads a file of a directory: only
// a regular one is read, and one that is not (a directory, a device: ln -s /dev/null x.list is how a
// package's source is kept from coming back) is as if it were not there. found is false for both.
func readRegular(ctx context.Context, p Probe, path string) (data []byte, found bool, err error) {
	info, err := p.Stat(ctx, path)
	switch {
	case isNotExist(err):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	case !info.Mode.IsRegular():
		return nil, false, nil
	}
	return readOptional(ctx, p, path)
}

// loadSources reads the sources the way apt finds them: /etc/apt/sources.list and the .list and
// .sources files of /etc/apt/sources.list.d. It reads files; nothing is fetched. A file that is not
// there, or is not a regular one, is nothing; one that is there and cannot be understood is Unreadable;
// only a question that was not answered is an error.
func loadSources(ctx context.Context, p Probe) (sourcesSet, error) {
	var set sourcesSet
	files := []struct{ name, path string }{{"sources.list", "/etc/apt/sources.list"}}
	names, err := p.ReadDir(ctx, "/etc/apt/sources.list.d")
	if err != nil && !isNotExist(err) {
		return set, err
	}
	for _, name := range names {
		if aptSourceName.MatchString(name) {
			files = append(files, struct{ name, path string }{name, "/etc/apt/sources.list.d/" + name})
		}
	}
	if len(files) > maxSourceFiles {
		return set, ErrTooLarge
	}
	for _, file := range files {
		data, found, err := readRegular(ctx, p, file.path)
		if err != nil && (ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
			return set, err
		}
		if err == nil && !found {
			continue
		}
		// a file that is there and cannot be read (too large, no right to it) is a file the rewriter
		// could not read either
		var entries []sourceEntry
		ok := err == nil
		if ok {
			if strings.HasSuffix(file.name, ".sources") {
				entries, ok = parseDeb822(file.name, string(data))
			} else {
				entries, ok = parseOneLine(file.name, string(data))
			}
		}
		if !ok {
			set.Unreadable = append(set.Unreadable, file.name)
			continue
		}
		// more sources in all than a machine has are as many as a file of them
		if len(set.Entries)+len(entries) > maxSourceEntries {
			return set, ErrTooLarge
		}
		set.Entries = append(set.Entries, entries...)
	}
	marked, err := markDistro(ctx, p, set.Entries)
	if err != nil {
		return set, err
	}
	set.Entries = marked
	return set, nil
}

// tokens splits a line at its spaces, keeping what is between [ and ] in one word: the options of
// a line ("[arch=amd64 signed-by=/etc/apt/keyrings/x.gpg]") and the label of a CD-ROM
// ("cdrom:[Debian GNU/Linux 11.0 _Bullseye_]/"). ok is false for brackets that do not close.
func tokens(line string) (words []string, ok bool) {
	depth, start := 0, -1
	for i, c := range line {
		switch {
		case c == '[':
			depth++
		case c == ']':
			depth--
			if depth < 0 {
				return nil, false
			}
		}
		space := c == ' ' || c == '\t'
		switch {
		case start < 0 && !space:
			start = i
		case start >= 0 && space && depth == 0:
			words = append(words, line[start:i])
			start = -1
		}
	}
	if depth != 0 {
		return nil, false
	}
	if start >= 0 {
		words = append(words, line[start:])
	}
	return words, true
}

// parseOneLine reads a .list file: "deb [options] uri suite component...", and "deb-src" the same.
// A comment is what follows a "#". A line that is not one of these is not understood: ok is false.
func parseOneLine(file, text string) (entries []sourceEntry, ok bool) {
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		words, ok := tokens(line)
		if !ok || len(words) < 3 || (words[0] != "deb" && words[0] != "deb-src") {
			return nil, false
		}
		rest := words[1:]
		if strings.HasPrefix(rest[0], "[") {
			rest = rest[1:]
		}
		if len(rest) < 2 {
			return nil, false
		}
		if len(entries) >= maxSourceEntries {
			return nil, false
		}
		components := unique(rest[2:])
		if len(components) > maxSourceComponents {
			return nil, false
		}
		entries = append(entries, sourceEntry{File: file, Kind: words[0], URI: rest[0], Suites: []string{rest[1]}, Components: components})
	}
	return entries, true
}

// aptZero is the number 0 as strtol reads it with base 0 (a leading 0 is octal, 0x is hex), and nothing else: 0, 00,
// -0, 0x0, 0X00. A number with a digit that is not 0 is not zero, whatever its base says (01 is 1, 07 is 7 and 010 is 8).
var aptZero = regexp.MustCompile(`^[+-]?(?:0[xX]0+|0+)$`)

// aptFalse is what apt's StringToBool reads as false (apt-pkg/contrib/strutl.cc): no, false, without, off
// and disable, in any case, and the number 0. What it does not understand is its default, which is true.
// An empty value is not taken for false here: a source that is kept is a refusal that may be wrong, and
// one that is dropped is one nobody looked at.
func aptFalse(value string) bool {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "no", "false", "without", "off", "disable":
		return true
	}
	return aptZero.MatchString(value)
}

// fieldWords are the words of the lines of a field of a .sources file: its first line, and the lines that
// go on from it.
func fieldWords(lines []string) []string {
	var out []string
	for _, line := range lines {
		out = append(out, strings.Fields(line)...)
	}
	return out
}

// unique is the words once each, in the order they came.
func unique(list []string) []string {
	seen := make(map[string]struct{}, len(list))
	out := make([]string, 0, len(list))
	for _, word := range list {
		if _, ok := seen[word]; !ok {
			seen[word] = struct{}{}
			out = append(out, word)
		}
	}
	return out
}

// parseDeb822 reads a .sources file: stanzas separated by blank lines, with the fields Types, URIs
// and Suites (and Components, Enabled), whose values may go on to the lines that follow. A stanza
// that has not the three is not understood; one that is not enabled is read and left out. What it costs
// is the size of the file: the lines of a field are kept as lines and read once at the end of the stanza,
// and a file that has more sources than a machine has is not understood.
func parseDeb822(file, text string) (entries []sourceEntry, ok bool) {
	var stanza map[string][]string
	var last string
	flush := func() bool {
		if stanza == nil {
			return true
		}
		fields := stanza
		stanza, last = nil, ""
		if enabled, set := fields["enabled"]; set && aptFalse(strings.Join(enabled, " ")) {
			return true
		}
		kinds, uris, suites := unique(fieldWords(fields["types"])), unique(fieldWords(fields["uris"])), unique(fieldWords(fields["suites"]))
		if len(kinds) == 0 || len(uris) == 0 || len(suites) == 0 {
			return false
		}
		if len(entries)+len(kinds)*len(uris) > maxSourceEntries || len(suites) > maxSourceSuites {
			return false
		}
		components := unique(fieldWords(fields["components"]))
		if len(components) > maxSourceComponents {
			return false
		}
		for _, kind := range kinds {
			if kind != "deb" && kind != "deb-src" {
				return false
			}
			for _, uri := range uris {
				entries = append(entries, sourceEntry{File: file, Kind: kind, URI: uri, Suites: suites, Components: components})
			}
		}
		return true
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "#"):
			continue
		case strings.TrimSpace(line) == "":
			if !flush() {
				return nil, false
			}
		case line[0] == ' ' || line[0] == '\t':
			if last == "" {
				return nil, false
			}
			stanza[last] = append(stanza[last], strings.TrimSpace(line))
		default:
			key, value, found := strings.Cut(line, ":")
			if !found || strings.TrimSpace(key) == "" {
				return nil, false
			}
			if stanza == nil {
				stanza = map[string][]string{}
			}
			last = strings.ToLower(strings.TrimSpace(key))
			stanza[last] = []string{strings.TrimSpace(value)}
		}
	}
	if !flush() {
		return nil, false
	}
	return entries, true
}

// scheme is the transport of the URI, lower case: "http", "https", "cdrom", "file", "copy", "mirror+http".
func (e sourceEntry) scheme() string {
	scheme, _, _ := strings.Cut(e.URI, ":")
	return strings.ToLower(scheme)
}

// local is a source that is on the machine or on a disc: a CD-ROM, a file, a copy.
func (e sourceEntry) local() bool {
	switch e.scheme() {
	case "cdrom", "file", "copy":
		return true
	}
	return false
}

// host is the host the URI names, lower case; "" when it names none.
func (e sourceEntry) host() string {
	u, err := url.Parse(e.URI)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// officialHost says whether a host is Debian's or Canonical's own: the archives, the security
// archives, their mirrors by country and by cloud, and Ubuntu's ESM.
func officialHost(host string) bool {
	return host == "debian.org" || strings.HasSuffix(host, ".debian.org") ||
		host == "ubuntu.com" || strings.HasSuffix(host, ".ubuntu.com")
}

// official says whether the source is on a host of the distribution's own (see officialHost).
func (e sourceEntry) official() bool {
	return !e.local() && officialHost(e.host())
}

// distro says whether the source is the distribution's own archive: Debian's or Ubuntu's host, or suites of a mirror of
// the archive that apt has a Release file of that says so (markDistro). It is a fact the report decided, once, for the
// sources it read: an entry that was not looked at is not the distribution's. Every other source is a third party's, and
// is warned of as one: the refusals of the aliases, of the proposed updates and of the backports are about the
// distribution's archive and do not apply to it.
func (e sourceEntry) distro() bool { return e.Distro }

// markDistro says, for each source, whether it is the distribution's own archive, and says it of each suite of a source on
// its own. The official hosts are, and a disc or a directory is not: the address decides. Any other host is when apt
// keeps a Release file of the suite that says Origin: Debian or Origin: Ubuntu, or a label that starts with those words
// (Debian's backports say Origin: Debian Backports; a vendor that serves its repository at /debian, and names its suite
// stable, says its own name); no file, a file that cannot be read and a Release file of another origin are all "not the
// distribution's". A source that has suites of both kinds becomes two, the same file, address and components, one with
// the suites that are the distribution's and one with the others: what is said of the archive (that an alias moves it,
// that the proposed updates are not ready) is said of its suites, and what is said of a third party (that it is paused) of
// the rest, and a stanza that lists the backports is not another's because of them. It looks at the files that apt names,
// and at no more than maxReleaseLookups of them in all, each once.
func markDistro(ctx context.Context, p Probe, entries []sourceEntry) ([]sourceEntry, error) {
	marked := make([]sourceEntry, 0, len(entries))
	looked := map[string]bool{}
	arch := p.Arch()
	for _, e := range entries {
		switch {
		case e.local():
			e.Distro = false
		case e.official():
			e.Distro = true
		default:
			var own, rest []string
			for _, suite := range e.Suites {
				says, err := suiteSaysDistro(ctx, p, e.URI, suite, arch, looked)
				if err != nil {
					return nil, err
				}
				if says {
					own = append(own, suite)
				} else {
					rest = append(rest, suite)
				}
			}
			if len(own) > 0 && len(rest) > 0 {
				d := e
				d.Suites, d.Distro = own, true
				marked = append(marked, d)
				e.Suites = rest
			} else {
				e.Distro = len(own) > 0
			}
		}
		marked = append(marked, e)
	}
	return marked, nil
}

// suiteSaysDistro says whether the Release file apt kept of a suite of a source says it is the distribution's own; looked
// has the answers of the files already asked for, and says how many have been.
func suiteSaysDistro(ctx context.Context, p Probe, uri, suite, arch string, looked map[string]bool) (bool, error) {
	name, ok := releaseListName(uri, suite, arch)
	if !ok {
		return false, nil
	}
	if says, known := looked[name]; known {
		return says, nil
	}
	if len(looked) >= maxReleaseLookups {
		return false, nil
	}
	says, err := releaseSaysDistro(ctx, p, name)
	if err != nil {
		return false, err
	}
	looked[name] = says
	return says, nil
}

// releaseSaysDistro reads the start of the InRelease file apt kept under a name (the Release file, when there is no
// InRelease: apt prefers the first) and says whether its Origin is Debian's or Ubuntu's. A file that is not there,
// or cannot be read, says nothing; only the end of the time is an error.
func releaseSaysDistro(ctx context.Context, p Probe, name string) (bool, error) {
	for _, suffix := range []string{"_InRelease", "_Release"} {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		data, err := p.ReadHead(ctx, listsDir+name+suffix, releaseHeadBytes)
		switch {
		case err == nil:
			return distroOrigin(releaseOrigin(data)), nil
		case isNotExist(err):
			continue
		case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
			return false, err
		}
		return false, nil
	}
	return false, nil
}

// distroOrigin says whether the Origin of a Release file is the distribution's own: Debian or Ubuntu, alone or as the first
// word of a label ("Debian Backports" is what Debian's backports say). An origin that only has the word somewhere else in
// it (a vendor's "SomeDebianThing", "Debianish", "Evil Debian") is not.
func distroOrigin(origin string) bool {
	for _, name := range []string{"Debian", "Ubuntu"} {
		if origin == name || strings.HasPrefix(origin, name+" ") {
			return true
		}
	}
	return false
}

// releaseOrigin is the Origin of a Release file: the value of the first line that starts with "Origin:". Only the
// lines that are whole are read (the start of a large file ends in the middle of one), and an Origin that is
// longer than a name is no Origin.
func releaseOrigin(data []byte) string {
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[:len(lines)-1] {
		if value, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "Origin:"); ok {
			if value = strings.TrimSpace(value); len(value) <= maxOriginLen {
				return value
			}
			return ""
		}
	}
	return ""
}

// aptQuoted are the characters of an address that apt writes as %xx in the name of a file it keeps (the Bad characters of
// URItoFileName, apt-pkg/contrib/strutl.cc:566, and the % itself, which QuoteString, 388-403, quotes always).
const aptQuoted = "\\|{}[]<>\"^~_=!@#$%^&*"

// aptQuote is a string with the characters of bad, and the %, written as apt writes them: a % and their two hex digits, in
// lower case (QuoteString, apt-pkg/contrib/strutl.cc:388-403). What is a control character, a space or not ASCII is never
// given to it: releaseListName does not derive a name for those.
func aptQuote(s, bad string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '%' || strings.IndexByte(bad, c) >= 0 {
			fmt.Fprintf(&out, "%%%02x", c)
		} else {
			out.WriteByte(c)
		}
	}
	return out.String()
}

// aptPort is the port of an address as apt reads it (atoi, and written back as a number, so that 0080 is 80, and 0 or none is
// no port); ok is false for what is not digits: that one is not derived.
func aptPort(digits string) (port int, ok bool) {
	if len(digits) > 9 {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	if digits == "" {
		return 0, true
	}
	port, _ = strconv.Atoi(digits)
	return port, true
}

// releaseListName is the name apt gives the Release file it keeps of a suite of a source, without its _InRelease or its
// _Release ("deb.debian.org_debian_dists_bookworm"). apt makes it of the address as FixupURI of apt-pkg/sourcelist.cc
// (80-95) writes it back (the architecture put in for $(ARCH), a / put at its end when it has none), and as URItoFileName of
// apt-pkg/contrib/strutl.cc (557-569) writes it, with the code that reads and writes an address, URI::CopyFrom (1631-1742)
// and URI::operator string (1747-1789). Those functions are the same, byte for byte, in apt 2.2.4 (Debian 11), 2.4.12 (Ubuntu
// 22.04) and 2.6.1 (Debian 12); FixupURI of 2.2.4 does not write the address back (88-92), which makes no difference to
// the names that were pinned, which were run on apt 2.6.1 only. The scheme and the user and the
// password are left out (the user ends at the last @ of the authority that is not its first character), an IPv6 address is
// written without its brackets, the port is a number that is not written when it is 0, the path is as it was written (its
// slashes are all kept), the suite is quoted as a name in an address (+ and ~) and then, with the rest, as a name of a
// file ([], ~, _, =, !, @ and the others of aptQuoted, and the % itself), and every / is a _. Each rule was checked
// against apt's own output ("apt-get update --print-uris"), which the tests pin.
//
// ok is false for what has no such file, or what is not derived the same for both formats of the sources: an address that
// is not an http, https or ftp one (a mirror list, a disc, a directory), one that has no host, one with a port that is not a
// number or a bracket that does not close, a flat repository (a suite that ends in /), a suite that has a variable of its
// own ($( ): a .sources file substitutes it and a .list one does not), a % or a quote (a .list file reads %xx as a byte
// and a .sources one does not), and anything with a space or a control character in it. A name that is not derived is a
// source that is not the distribution's.
func releaseListName(uri, suite, arch string) (name string, ok bool) {
	if suite == "" || strings.HasSuffix(suite, "/") || strings.Contains(suite, "$(") || strings.ContainsAny(uri+suite, "%\"") ||
		strings.ContainsFunc(uri+suite, func(r rune) bool { return r <= ' ' || r >= 0x7f }) {
		return "", false
	}
	scheme, rest, found := strings.Cut(strings.ReplaceAll(uri, "$(ARCH)", arch), "://")
	if !found || !slices.Contains([]string{"http", "https", "ftp"}, strings.ToLower(scheme)) {
		return "", false
	}
	// the authority ends at the first / that is not between brackets
	inBracket, end := false, 0
	for ; end < len(rest) && (rest[end] != '/' || inBracket); end++ {
		switch rest[end] {
		case '[':
			inBracket = true
		case ']':
			inBracket = false
		}
	}
	authority, path := rest[:end], rest[end:]
	if path == "" {
		path = "/"
	}
	if i := strings.LastIndex(authority, "@"); i >= 1 {
		authority = authority[i+1:]
	}
	// the brackets of the host are taken out wherever they are, and what follows the last of them is where a port may be
	host, portAt := make([]byte, 0, len(authority)), 0
	inBracket = false
	for i := 0; i < len(authority); i++ {
		switch c := authority[i]; {
		case c == '[':
			inBracket = true
		case c == ']' && inBracket:
			inBracket, portAt = false, len(host)
		default:
			host = append(host, c)
		}
	}
	if inBracket {
		return "", false
	}
	name = string(host)
	if i := strings.LastIndex(name, ":"); i >= 0 && i >= portAt {
		port, ok := aptPort(name[i+1:])
		if !ok {
			return "", false
		}
		name = name[:i]
		if port != 0 {
			name += ":" + strconv.Itoa(port)
		}
	}
	if name == "" {
		return "", false
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	full := name + path + "dists/" + aptQuote(suite, "+~ ")
	return strings.ReplaceAll(aptQuote(full, aptQuoted), "/", "_"), true
}

// The suffixes a suite of the distribution can have, longest first: "bullseye-backports-sloppy".
var suiteSuffixes = []string{"-backports-sloppy", "-backports", "-proposed-updates", "-proposed", "-updates", "-security", "/updates"}

// suiteAliases are the names that stand for whichever release is current: a source that has one
// moves to another release the day the archive does.
var suiteAliases = []string{"oldoldoldstable", "oldoldstable", "oldstable", "stable", "testing", "unstable", "sid", "experimental", "devel", "rc-buggy"}

// splitSuite is a suite without its suffix: "bullseye-security" is "bullseye" and "-security".
func splitSuite(suite string) (base, suffix string) {
	for _, s := range suiteSuffixes {
		if strings.HasSuffix(suite, s) {
			return strings.TrimSuffix(suite, s), s
		}
	}
	return suite, ""
}

func isAlias(suite string) bool {
	base, _ := splitSuite(suite)
	return slices.Contains(suiteAliases, base)
}

func isProposed(suite string) bool {
	_, suffix := splitSuite(suite)
	return suffix == "-proposed-updates" || suffix == "-proposed"
}

func isBackports(suite string) bool {
	_, suffix := splitSuite(suite)
	return suffix == "-backports" || suffix == "-backports-sloppy"
}
