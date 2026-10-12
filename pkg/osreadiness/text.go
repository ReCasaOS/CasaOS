package osreadiness

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// MaxTextBytes is the most Text is long: it is pasted into a forum, a ticket, a message.
const MaxTextBytes = 8 << 10

var (
	urlPattern  = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*)://([^\s/?#]*)[^\s]*`)
	ipv4Pattern = regexp.MustCompile(`\b[0-9]{1,3}(?:\.[0-9]{1,3}){3}\b`)
	// an IPv6 address: written in full, or with a "::" in it (a "::" alone, as in "Dir::Cache", is not one)
	ipv6Full       = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}\b`)
	ipv6Compressed = regexp.MustCompile(`(?i)(?:\b(?:[0-9a-f]{1,4}:){1,7}:(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4}){0,6})?|::[0-9a-f]{1,4}(?::[0-9a-f]{1,4}){0,6})\b`)
	macPattern     = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}\b`)
	portSuffix     = regexp.MustCompile(`:[0-9]+$`)
)

// scrub takes out of a line what the owner would not paste into a public place, whatever put it
// there: an address of a network card, an address of the machine, the user name, the password and
// the path of a URL (only its scheme and host are kept).
func scrub(s string) string {
	s = urlPattern.ReplaceAllStringFunc(s, func(url string) string {
		m := urlPattern.FindStringSubmatch(url)
		authority := m[2]
		if at := strings.LastIndex(authority, "@"); at >= 0 {
			authority = authority[at+1:]
		}
		if strings.HasPrefix(authority, "[") {
			if end := strings.Index(authority, "]"); end >= 0 {
				authority = authority[:end+1]
			}
		} else {
			authority = portSuffix.ReplaceAllString(authority, "")
		}
		return m[1] + "://" + authority
	})
	s = macPattern.ReplaceAllString(s, "<mac>")
	s = ipv4Pattern.ReplaceAllString(s, "<ip>")
	s = ipv6Full.ReplaceAllString(s, "<ip>")
	return ipv6Compressed.ReplaceAllString(s, "<ip>")
}

func (r Release) String() string {
	switch {
	case r.PrettyName != "":
		return r.PrettyName
	case r.ID != "" && r.VersionID != "":
		return r.ID + " " + r.VersionID
	}
	return r.ID
}

func stateSentence(r Report) string {
	switch r.State {
	case StateReady:
		return "Ready."
	case StateAttention:
		return "Ready, with things to look at."
	case StateBlocked:
		if r.Counts.Refuse == 1 {
			return "Not ready: 1 blocker."
		}
		return "Not ready: " + strconv.Itoa(r.Counts.Refuse) + " blockers."
	case StateLatest:
		return "Latest release: there is nothing to upgrade to."
	case StateUnsupported:
		return "Not supported for an upgrade from here."
	}
	return r.State
}

// Text is the report as plain English, for "copy the report": at most MaxTextBytes, with no
// hostname, no address, no user name and no URL beyond its scheme and host. Where the page says a
// sentence of its own for a code, this says the English one the check gave.
func Text(r Report) string {
	const footer = "This check changes nothing on the machine."
	// A report that does not fit is said in less words before any finding is left out: first the
	// warnings and the notes lose their details, then everything does, and each finding keeps its
	// code and its title. Only a report with more findings than that is cut, at a whole line.
	for level := 0; level <= 2; level++ {
		if lines := textLines(r, level, footer); textSize(lines) <= MaxTextBytes {
			return strings.Join(lines, "\n") + "\n"
		}
	}
	lines := textLines(r, 2, footer)
	var out strings.Builder
	room := MaxTextBytes - len(footer) - 120
	for i, line := range lines {
		if out.Len()+len(line)+1 > room && i < len(lines)-2 {
			out.WriteString("... (the rest is on the page)\n\n" + footer + "\n")
			return out.String()
		}
		out.WriteString(line + "\n")
	}
	return out.String()
}

func textSize(lines []string) int {
	size := 0
	for _, line := range lines {
		size += len(line) + 1
	}
	return size
}

// textLines are the lines of the text at a level of detail: 0 says everything, 1 says the details
// and the data of the blockers only, 2 says the title of each finding and no more.
func textLines(r Report, level int, footer string) []string {
	lines := []string{
		"ReCasaOS: can this machine be upgraded to the next release?",
		"Checked: " + r.CheckedAt.UTC().Format("2006-01-02T15:04:05Z"),
		"Result: " + stateSentence(r),
	}
	if r.Hop != nil {
		lines = append(lines, "From: "+scrub(r.Hop.From.String()), "To: "+scrub(r.Hop.To.String()))
	}
	lines = append(lines, fmt.Sprintf("Blockers %d, warnings %d, notes %d, ok %d", r.Counts.Refuse, r.Counts.Warn, r.Counts.Info, r.Counts.OK))
	if level > 0 {
		lines = append(lines, "Details are left out here to keep this short: the page has them.")
	}

	var oks []string
	for _, section := range []struct {
		severity Severity
		heading  string
	}{{Refuse, "BLOCKERS"}, {Warn, "WARNINGS"}, {Info, "NOTES"}} {
		var body []string
		for _, f := range r.Checks {
			if f.Severity != section.severity {
				continue
			}
			body = append(body, "["+f.Code+"] "+scrub(f.Title))
			if level == 2 || (level == 1 && f.Severity != Refuse) {
				continue
			}
			if f.Detail != "" {
				body = append(body, "    "+scrub(f.Detail))
			}
			body = append(body, dataLines(f)...)
			if f.Action != "" && f.Action != ActionNone {
				body = append(body, "    suggested: "+string(f.Action))
			}
		}
		if len(body) > 0 {
			lines = append(lines, "", section.heading)
			lines = append(lines, body...)
		}
	}
	for _, f := range r.Checks {
		if f.Severity == OK {
			oks = append(oks, f.Code)
		}
	}
	if len(oks) > 0 {
		lines = append(lines, "", "OK ("+strconv.Itoa(len(oks))+"): "+strings.Join(oks, ", "))
	}
	return append(lines, "", footer)
}

// dataLines are the data of a finding, one key a line, in the order of the keys.
func dataLines(f Finding) []string {
	keys := make([]string, 0, len(f.Data))
	for key := range f.Data {
		if key != "unknown" && key != "reason" {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	var lines []string
	for _, key := range keys {
		var value string
		switch v := f.Data[key].(type) {
		case []string:
			value = strings.Join(v, ", ")
		default:
			value = fmt.Sprint(v)
		}
		lines = append(lines, "    "+key+": "+scrub(value))
	}
	return lines
}
