package osreadiness

import "context"

// CheckByCode runs the one check of the table that has this code, as the report runs it (its own time, an answer for a check that panics or ignores its context, a Probe that runs only
// what the allow-list says), and says what it found. ok is false when the table has no such check.
//
// It is for the step that stands between a finished hop and the restart of the machine: the report's gates read /etc/os-release, which already names the next release by then, and
// would call the machine unsupported. The checks that are asked for here read the box and nothing else, as the report's do.
func CheckByCode(ctx context.Context, p Probe, code string) (finding Finding, ok bool) {
	for _, c := range registry {
		if c.Code == code {
			return runOne(ctx, guarded{p}, c), true
		}
	}
	return Finding{}, false
}
