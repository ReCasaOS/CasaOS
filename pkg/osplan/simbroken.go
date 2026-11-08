package osplan

import (
	"regexp"
	"slices"
	"strings"
)

// apt's simulator, when the configuration of a package it simulates finds a dependency it takes for broken, prints "Conf <name>:<arch> broken" and the dependency on lines that
// begin with a space (" Breaks:coreutils:amd64", " []"), goes on, and ends with "E: Conf Broken <name>:<arch>" and status 100 (apt 2.8.3, testdata/real/docker-u24-26-dist-upgrade.txt).
var (
	simConfBrokenError = regexp.MustCompile(`^E: Conf Broken ([a-z0-9][a-z0-9.+-]*):[a-z0-9]+$`)
	simConfBrokenLine  = regexp.MustCompile(`^Conf ([a-z0-9][a-z0-9.+-]*):[a-z0-9]+ broken$`)
)

// withoutSimBroken is a simulation of apt as it reads without what the hop's SimBroken make it say: when it ended with status 100 and its errors are all "E: Conf Broken" of those
// packages, the errors, the "Conf ... broken" lines of those packages and the lines that begin with a space under them are left out, and the status is 0. Anything else (another
// error, another package, another status) is the output as it was, and the parser says what it is.
func (h HopSpec) withoutSimBroken(out Output) Output {
	if len(h.SimBroken) == 0 || out.ExitCode != 100 {
		return out
	}
	lines := strings.Split(out.Text, "\n")
	errors := 0
	for _, l := range lines {
		if !strings.HasPrefix(l, "E:") {
			continue
		}
		m := simConfBrokenError.FindStringSubmatch(l)
		if m == nil || !slices.Contains(h.SimBroken, m[1]) {
			return out
		}
		errors++
	}
	if errors == 0 {
		return out
	}
	kept := make([]string, 0, len(lines))
	under := false
	for _, l := range lines {
		if under && strings.HasPrefix(l, " ") {
			continue
		}
		under = false
		if m := simConfBrokenLine.FindStringSubmatch(l); m != nil && slices.Contains(h.SimBroken, m[1]) {
			under = true
			continue
		}
		if strings.HasPrefix(l, "E:") {
			continue
		}
		kept = append(kept, l)
	}
	return Output{Text: strings.Join(kept, "\n"), ExitCode: 0, Truncated: out.Truncated}
}

// simOK is the shell function that the texts of a hop with SimBroken define: sim_ok STATUS FILE is true when a simulation ended with status 100 and every error it printed is the
// "E: Conf Broken" of one of those packages. The callers write `aptq -s dist-upgrade > FILE 2>&1 || sim_ok $? FILE || STOP`: $? is apt's status there.
func (h HopSpec) simOK() string {
	if len(h.SimBroken) == 0 {
		return ""
	}
	names := make([]string, len(h.SimBroken))
	for i, n := range h.SimBroken {
		names[i] = regexp.QuoteMeta(n)
	}
	return `sim_ok() { [ "$1" = 100 ] && grep -q '^E: ' "$2" && ! grep '^E: ' "$2" | grep -Evq '^E: Conf Broken (` + strings.Join(names, "|") + `):[a-z0-9]+$'; }` + "\n"
}

// simOr is what follows a simulation of the full upgrade into file, before its STOP: nothing for a hop without SimBroken.
func (h HopSpec) simOr(file string) string {
	if len(h.SimBroken) == 0 {
		return ""
	}
	return ` || sim_ok $? "` + file + `"`
}
