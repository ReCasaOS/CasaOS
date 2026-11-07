package osplan

import (
	"context"
	"slices"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The .sources files (deb822), for a hop whose HopSpec says Deb822. A file is read by the one reader of the report
// (osreadiness.ReadDeb822) and judged stanza by stanza with the rules of the lines of a .list file: a stanza whose addresses are
// all the distribution's is moved to the target, a file whose stanzas are all someone else's is put aside whole, and anything
// else, or anything the plan does not read, is refused with the file and the line. Only the fields Suites, Components and URIs
// of a stanza are written again, each on one line, and Enabled: no is added to a stanza that only had backports: every other
// line of the file, its comments and the order of its fields, is kept as it is.

// deb822Fields are the fields that apt 2.6.1 reads in a stanza (apt-pkg/sourcelist.cc, the release of Debian 12). A field that is
// not one of them is not one the plan reads.
var deb822Fields = []string{
	"types", "uris", "suites", "components", "enabled",
	"architectures", "architectures-add", "architectures-remove",
	"languages", "languages-add", "languages-remove",
	"targets", "targets-add", "targets-remove",
	"pdiffs", "by-hash", "signed-by", "inrelease-path",
	"allow-insecure", "allow-weak", "allow-downgrade-to-insecure", "trusted",
	"check-valid-until", "valid-until-min", "valid-until-max", "check-date", "date-max-future",
}

// deb822Weak are the fields of a stanza that turn apt's checks off: a stanza of the distribution's that says yes to one is not
// one a plan can say anything of, as the same options of a .list line.
var deb822Weak = []string{"trusted", "allow-insecure", "allow-weak", "allow-downgrade-to-insecure"}

// deb822Stanza is a stanza as the plan reads it: its fields by their name in lower case, and its words.
type deb822Stanza struct {
	st                  osreadiness.Deb822Stanza
	fields              map[string]osreadiness.Deb822Field
	types, uris, suites []string
	comps               []string
	// distro says that every address of the stanza is the distribution's (for each of its suites)
	distro bool
}

// deb822File is a .sources file as the plan reads it: its lines as written (the last one "" when the file ends with a new
// line), and its stanzas.
type deb822File struct {
	lines   []string
	stanzas []deb822Stanza
}

func fieldWordsOf(f osreadiness.Deb822Field) []string {
	var out []string
	for _, l := range f.Lines {
		out = append(out, strings.Fields(l)...)
	}
	return out
}

// readDeb822File reads a .sources file strictly and asks the decider about each address and suite of each stanza. total is the
// count of sources of all the files, which it adds to.
func readDeb822File(ctx context.Context, name string, content []byte, decider Decider, total *int) (*deb822File, error) {
	if bad, n := textProblem(content); bad != nil {
		return nil, bad.failure(name, n)
	}
	text := string(content)
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if len(l) > maxLineBytes {
			return nil, lineProblem{CodeSourcesFormat, "long_line"}.failure(name, i+1)
		}
	}
	stanzas, ok := osreadiness.ReadDeb822(text)
	if !ok {
		return nil, lineProblem{CodeSourcesFormat, "deb822_form"}.failure(name, 0)
	}
	out := &deb822File{lines: lines}
	for _, st := range stanzas {
		s := deb822Stanza{st: st, fields: map[string]osreadiness.Deb822Field{}}
		for _, f := range st.Fields {
			key := strings.ToLower(f.Key)
			if _, twice := s.fields[key]; twice {
				return nil, lineProblem{CodeSourcesFormat, "field_twice"}.failure(name, f.First)
			}
			if !slices.Contains(deb822Fields, key) {
				return nil, lineProblem{CodeSourcesFormat, "field"}.failure(name, f.First)
			}
			if !isPrintableASCII(strings.Join(f.Lines, " ")) && key != "signed-by" {
				return nil, lineProblem{CodeSourcesFormat, "chars"}.failure(name, f.First)
			}
			s.fields[key] = f
		}
		if f, set := s.fields["enabled"]; set {
			// a stanza the owner turned off is refused: the plan writes Enabled: no itself, and could not give it back as it was
			return nil, lineProblem{CodeSourcesFormat, "enabled"}.failure(name, f.First)
		}
		s.types, s.uris, s.suites = fieldWordsOf(s.fields["types"]), fieldWordsOf(s.fields["uris"]), fieldWordsOf(s.fields["suites"])
		s.comps = fieldWordsOf(s.fields["components"])
		if len(s.types) == 0 || len(s.uris) == 0 || len(s.suites) == 0 {
			return nil, lineProblem{CodeSourcesFormat, "fields"}.failure(name, st.First)
		}
		for _, t := range s.types {
			if t != "deb" && t != "deb-src" {
				return nil, lineProblem{CodeSourcesFormat, "type"}.failure(name, s.fields["types"].First)
			}
		}
		if len(s.comps) > osreadiness.MaxSourceComponents {
			return nil, lineProblem{CodeSourcesFormat, "too_many"}.failure(name, s.fields["components"].First)
		}
		if *total += len(s.types) * len(s.uris) * len(s.suites); *total > osreadiness.MaxSourceEntries {
			return nil, lineProblem{CodeSourcesFormat, "too_many"}.failure(name, st.First)
		}
		uriLine := s.fields["uris"].First
		for _, uri := range s.uris {
			if low := strings.ToLower(uri); strings.HasPrefix(low, "cdrom:") || strings.HasPrefix(low, "file:") || strings.HasPrefix(low, "copy:") {
				return nil, lineProblem{CodeSourcesCDROM, "cdrom"}.failure(name, uriLine)
			}
			if !anyURI.MatchString(uri) && !osreadiness.IsMirrorList(uri) {
				return nil, lineProblem{CodeSourcesFormat, "uri"}.failure(name, uriLine)
			}
		}
		distros, asked := 0, 0
		for _, uri := range s.uris {
			for _, suite := range s.suites {
				distro, decided, err := decider.Suite(ctx, uri, suite)
				if err != nil {
					return nil, err
				}
				if !decided {
					return nil, lineProblem{CodeSourcesFormat, "undecided"}.failure(name, uriLine)
				}
				asked++
				if distro {
					distros++
				}
			}
		}
		if distros > 0 && distros < asked {
			return nil, failf(CodeSourcesMixedFile, map[string]any{"file": name, "line": uriLine},
				"%s line %d has repositories of the distribution and of others in one stanza: it cannot be put aside and rewritten at once", name, uriLine)
		}
		s.distro = distros > 0
		out.stanzas = append(out.stanzas, s)
	}
	mine := 0
	for _, s := range out.stanzas {
		if s.distro {
			mine++
		}
	}
	if mine > 0 && mine < len(out.stanzas) {
		return nil, failf(CodeSourcesMixedFile, map[string]any{"file": name},
			"%s has repositories of the distribution and of others: it cannot be put aside as a file and rewritten as a file", name)
	}
	return out, nil
}

// deb822Edit is what becomes of the lines of a file: a line replaced, a line left out, lines added after a line.
type deb822Edit struct {
	replace map[int]string
	drop    map[int]bool
	after   map[int][]string
}

// setField writes a field again on its first line, with words, and leaves out the lines that went on from it (not the comments
// between them).
func (e *deb822Edit) setField(f osreadiness.Deb822Field, words []string, lines []string) {
	e.replace[f.First] = f.Key + ": " + strings.Join(words, " ")
	for n := f.First + 1; n <= f.Last; n++ {
		if !strings.HasPrefix(lines[n-1], "#") {
			e.drop[n] = true
		}
	}
}

func (e *deb822Edit) apply(lines []string) string {
	var b strings.Builder
	for i, l := range lines {
		n := i + 1
		if e.drop[n] {
			continue
		}
		if r, ok := e.replace[n]; ok {
			l = r
		}
		b.WriteString(l)
		last := i == len(lines)-1
		if !last {
			b.WriteByte('\n')
		}
		if adds := e.after[n]; len(adds) > 0 {
			if last {
				b.WriteByte('\n')
			}
			b.WriteString(strings.Join(adds, "\n") + "\n")
		}
	}
	return b.String()
}

// deb822Problem checks a stanza of the distribution's beyond its shape, as distroProblem checks a line: addresses that are
// http, https or a list of Debian's mirrors, suites and components that are names, and no field that turns apt's checks off.
func deb822Problem(s *deb822Stanza) (*lineProblem, int) {
	for _, uri := range s.uris {
		if !distroURI.MatchString(uri) && !osreadiness.IsMirrorList(uri) {
			return &lineProblem{CodeSourcesFormat, "uri"}, s.fields["uris"].First
		}
	}
	for _, suite := range s.suites {
		if !suiteToken.MatchString(suite) {
			return &lineProblem{CodeSourcesFormat, "suite"}, s.fields["suites"].First
		}
	}
	for _, c := range s.comps {
		if !suiteToken.MatchString(c) {
			return &lineProblem{CodeSourcesFormat, "component"}, s.fields["components"].First
		}
	}
	for _, key := range deb822Weak {
		if f, set := s.fields[key]; set && !osreadiness.AptFalse(strings.Join(f.Lines, " ")) {
			return &lineProblem{CodeSourcesFormat, "trusted"}, f.First
		}
	}
	return nil, 0
}

// suiteFor is a suite of the distribution's own archive for the target: the suite it becomes, or that it is left out (a
// backports or proposed-updates suite), or the problem that stops it. These are the rules of rewriteDistro for a line.
func (h HopSpec) suiteFor(suite string) (to string, dropped bool, p *lineProblem) {
	if osreadiness.IsSuiteAlias(suite) {
		return "", false, &lineProblem{CodeSourcesAlias, "alias"}
	}
	base, suffix := osreadiness.SplitSuite(suite)
	switch {
	case base == h.To || slices.Contains(h.Later, base):
		return "", false, &lineProblem{CodeSourcesTarget, "target"}
	case base != h.From:
		return "", false, &lineProblem{CodeSourcesFormat, "suite"}
	}
	switch suffix {
	case "", "-updates", "-security":
		if h.Refresh {
			return suite, false, nil
		}
		return h.To + suffix, false, nil
	case "-backports", "-backports-sloppy", "/updates", "-proposed-updates":
		if h.Refresh {
			return suite, false, nil
		}
		return "", true, nil
	}
	return "", false, &lineProblem{CodeSourcesFormat, "suite"}
}

// fieldShown is a field as it is shown: its name and its words on one line.
func fieldShown(key string, words []string) string {
	return shown(key + ": " + strings.Join(words, " "))
}

// rewriteDeb822 is what the hop makes of a .sources file that was read: its content in the tree, or that it is put aside, and
// what changed. kept counts the stanzas of the distribution's that stay, out the totals of the rewrite.
func (h HopSpec) rewriteDeb822(name string, f *deb822File, out *Rewritten, kept *int) (content string, paused bool, changes []LineChange, err error) {
	changes = []LineChange{}
	if len(f.stanzas) > 0 && !f.stanzas[0].distro {
		// readDeb822File refused a file that mixes: every stanza is someone else's, and the file is put aside whole
		var hosts []string
		allDocker := true
		for _, s := range f.stanzas {
			for _, uri := range s.uris {
				allDocker = allDocker && osreadiness.SourceHost(uri) == osreadiness.DockerHost
				if host := hostOf(uri); host != "" && !slices.Contains(hosts, host) {
					hosts = append(hosts, host)
				}
			}
			changes = append(changes, LineChange{N: s.fields["uris"].First, Before: fieldShown(s.fields["uris"].Key, s.uris), Why: "paused"})
		}
		slices.Sort(hosts)
		if hosts == nil {
			hosts = []string{}
		}
		out.Paused = append(out.Paused, Paused{File: name, Hosts: hosts, Docker: allDocker})
		return "", true, changes, nil
	}
	edit := &deb822Edit{replace: map[int]string{}, drop: map[int]bool{}, after: map[int][]string{}}
	for i := range f.stanzas {
		s := &f.stanzas[i]
		if p, n := deb822Problem(s); p != nil {
			return "", false, nil, p.failure(name, n)
		}
		var suites []string
		dropped := 0
		for _, suite := range s.suites {
			to, drop, p := h.suiteFor(suite)
			if p != nil {
				return "", false, nil, p.failure(name, s.fields["suites"].First)
			}
			if drop {
				dropped++
				continue
			}
			suites = append(suites, to)
		}
		out.DroppedBackports += dropped
		if h.Refresh {
			// the machine keeps the stanza as it has it: its release, its addresses and its backports are the ones brought up to date
			*kept++
			continue
		}
		if len(suites) == 0 {
			// a stanza of backports only: turned off, with its suites as they are
			edit.after[s.st.Last] = append(edit.after[s.st.Last], "Enabled: no")
			changes = append(changes, LineChange{N: s.st.Last, After: "Enabled: no", Why: "dropped"})
			continue
		}
		*kept++
		reasons := []string{"suite"}
		if dropped > 0 {
			reasons = append(reasons, "dropped")
		}
		uris, movedAny := make([]string, len(s.uris)), false
		for j, uri := range s.uris {
			moved := false
			uris[j], moved = moveHost(uri)
			movedAny = movedAny || moved
		}
		if movedAny {
			reasons = append(reasons, "host")
			uf := s.fields["uris"]
			edit.setField(uf, uris, f.lines)
			changes = append(changes, LineChange{N: uf.First, Before: fieldShown(uf.Key, s.uris), After: fieldShown(uf.Key, uris), Why: "host"})
		}
		comps := slices.Clone(s.comps)
		if h.Firmware && slices.Contains(comps, "non-free") && !slices.Contains(comps, "non-free-firmware") {
			comps = append(comps, "non-free-firmware")
			cf := s.fields["components"]
			edit.setField(cf, comps, f.lines)
			changes = append(changes, LineChange{N: cf.First, Before: fieldShown(cf.Key, s.comps), After: fieldShown(cf.Key, comps), Why: "firmware"})
			out.FirmwareAdded++
		}
		sf := s.fields["suites"]
		edit.setField(sf, suites, f.lines)
		changes = append(changes, LineChange{N: sf.First, Before: fieldShown(sf.Key, s.suites), After: fieldShown(sf.Key, suites), Why: strings.Join(reasons, ",")})
	}
	for n, text := range edit.replace {
		if len(text) > maxLineBytes {
			return "", false, nil, lineProblem{CodeSourcesFormat, "long_line"}.failure(name, n)
		}
	}
	slices.SortStableFunc(changes, func(a, b LineChange) int { return a.N - b.N })
	return edit.apply(f.lines), false, changes, nil
}

// checkDeb822Tree is a .sources file of the tree read again: the strict reader must read it, and no stanza that apt reads may
// still be of the release that is left (a refresh has no target).
func checkDeb822Tree(hop HopSpec, f TreeFile) error {
	if bad, n := textProblem([]byte(f.Content)); bad != nil {
		return failf(CodeInternal, nil, "the tree's %s line %d is not read by the strict reader (%s)", f.Path, n, bad.reason)
	}
	stanzas, ok := osreadiness.ReadDeb822(f.Content)
	if !ok {
		return failf(CodeInternal, nil, "the tree's %s is not read by the strict reader", f.Path)
	}
	if hop.Refresh {
		return nil
	}
	for _, st := range stanzas {
		off := false
		var suites []string
		line := st.First
		for _, fl := range st.Fields {
			switch strings.ToLower(fl.Key) {
			case "enabled":
				off = osreadiness.AptFalse(strings.Join(fl.Lines, " "))
			case "suites":
				suites, line = fieldWordsOf(fl), fl.First
			}
		}
		if off {
			continue
		}
		for _, suite := range suites {
			if base, _ := osreadiness.SplitSuite(suite); base == hop.From {
				return failf(CodeInternal, nil, "the tree's %s line %d is still of %s", f.Path, line, hop.From)
			}
		}
	}
	return nil
}
