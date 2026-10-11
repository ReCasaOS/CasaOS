package osreadiness

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
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
}

// aptSourceName is a name apt reads in sources.list.d: letters, digits, "_", "-" and ".", and the
// extension .list or .sources. apt ignores every other name, and so does this.
var aptSourceName = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.(?:list|sources)$`)

// sourcesSet is what apt would read of its sources: the entries of the files it understands, and
// the files it would not (which the rewriter of the plan could not read either).
type sourcesSet struct {
	Entries []sourceEntry
	// Unreadable are the files that could not be read or understood.
	Unreadable []string
}

// loadSources reads the sources the way apt finds them: /etc/apt/sources.list and the .list and
// .sources files of /etc/apt/sources.list.d. It reads files; nothing is fetched. A file that is
// not there is nothing; one that is there and cannot be understood is Unreadable; only a question
// that was not answered is an error.
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
	for _, file := range files {
		data, found, err := readOptional(ctx, p, file.path)
		if err != nil && (ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
			return set, err
		}
		if err == nil && !found {
			continue
		}
		// a file that is there and cannot be read (too large, not a regular file) is a file the
		// rewriter could not read either
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
		set.Entries = append(set.Entries, entries...)
	}
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
		entries = append(entries, sourceEntry{File: file, Kind: words[0], URI: rest[0], Suites: []string{rest[1]}, Components: rest[2:]})
	}
	return entries, true
}

// parseDeb822 reads a .sources file: stanzas separated by blank lines, with the fields Types, URIs
// and Suites (and Components, Enabled), whose values may go on to the lines that follow. A stanza
// that has not the three is not understood; one that says "Enabled: no" is read and left out.
func parseDeb822(file, text string) (entries []sourceEntry, ok bool) {
	var stanza map[string]string
	var last string
	flush := func() bool {
		if stanza == nil {
			return true
		}
		fields := stanza
		stanza, last = nil, ""
		if enabled, set := fields["enabled"]; set && slices.Contains([]string{"no", "false", "0"}, strings.ToLower(strings.TrimSpace(enabled))) {
			return true
		}
		kinds, uris, suites := strings.Fields(fields["types"]), strings.Fields(fields["uris"]), strings.Fields(fields["suites"])
		if len(kinds) == 0 || len(uris) == 0 || len(suites) == 0 {
			return false
		}
		for _, kind := range kinds {
			if kind != "deb" && kind != "deb-src" {
				return false
			}
			for _, uri := range uris {
				entries = append(entries, sourceEntry{File: file, Kind: kind, URI: uri, Suites: suites, Components: strings.Fields(fields["components"])})
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
			stanza[last] += " " + strings.TrimSpace(line)
		default:
			key, value, found := strings.Cut(line, ":")
			if !found || strings.TrimSpace(key) == "" {
				return nil, false
			}
			if stanza == nil {
				stanza = map[string]string{}
			}
			last = strings.ToLower(strings.TrimSpace(key))
			stanza[last] = strings.TrimSpace(value)
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

// distro says whether the source is the distribution's own archive: Debian's or Ubuntu's host, or
// a mirror, which is told by its path being the root of the archive ("/debian", "/ubuntu"). A
// mirror under another path is a third-party source here, and is warned of as one.
func (e sourceEntry) distro() bool {
	if e.local() {
		return false
	}
	if officialHost(e.host()) {
		return true
	}
	u, err := url.Parse(e.URI)
	if err != nil {
		return false
	}
	switch strings.Trim(u.Path, "/") {
	case "debian", "debian-security", "ubuntu":
		return true
	}
	return false
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
