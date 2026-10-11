package osreadiness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// scenario is a machine and what one check must say of it.
type scenario struct {
	name  string
	setup func(f *fakeProbe)
	// the finding the check must give
	severity Severity
	// unknown is the reason, when the check must say it does not know
	unknown string
	// data are keys of the finding's data that must have these values
	data   D
	action Action
	// title is a part of the title the finding must have, where the scenario says it: two refusals of
	// a check that are not the same are told apart by it
	title string
}

func checkNamed(t *testing.T, code string) Check {
	t.Helper()
	for _, c := range Checks() {
		if c.Code == code {
			return c
		}
	}
	t.Fatalf("no check is registered as %q", code)
	return Check{}
}

// checkOn runs the check of a code, alone, on a machine, and gives its finding.
func checkOn(t *testing.T, f *fakeProbe, code string) Finding {
	t.Helper()
	report := Run(context.Background(), f, Options{Checks: []Check{checkNamed(t, code)}})
	for _, finding := range report.Checks {
		if finding.Code == code {
			return finding
		}
	}
	t.Fatalf("%s did not run: state %s, findings %v", code, report.State, codesOf(report))
	return Finding{}
}

// runScenarios runs each machine the way it is described, with the check of the code. Each
// machine starts as newBox and is changed by its setup.
func runScenarios(t *testing.T, code string, scenarios []scenario) {
	t.Helper()
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			f := newBox(t)
			if sc.setup != nil {
				sc.setup(f)
			}
			got := checkOn(t, f, code)
			if got.Severity != sc.severity {
				t.Fatalf("%s: severity = %s, want %s (detail %q, data %v)", code, got.Severity, sc.severity, got.Detail, got.Data)
			}
			if sc.unknown != "" {
				if got.Data["unknown"] != true || got.Data["reason"] != sc.unknown {
					t.Fatalf("%s: data = %v, want unknown for %s", code, got.Data, sc.unknown)
				}
			} else if got.Data["unknown"] != false {
				t.Fatalf("%s: unknown = %v, want a check that knows", code, got.Data["unknown"])
			}
			for key, want := range sc.data {
				have, _ := json.Marshal(got.Data[key])
				wanted, _ := json.Marshal(want)
				if string(have) != string(wanted) {
					t.Errorf("%s: data[%s] = %s, want %s", code, key, have, wanted)
				}
			}
			// an action is checked where the scenario says it; a check that has nothing to say has none
			if (sc.action != "" && got.Action != sc.action) || (got.Severity == OK && got.Action != "") {
				t.Errorf("%s: action = %q, want %q", code, got.Action, sc.action)
			}
			if got.Title == "" || !strings.Contains(got.Title, sc.title) {
				t.Errorf("%s: title = %q, want one with %q", code, got.Title, sc.title)
			}
		})
	}
}

// debian12 and the others turn the box into another release.
func debian12(f *fakeProbe) {
	f.set("/etc/os-release", osRelease("debian", "12", "bookworm", "Debian GNU/Linux 12 (bookworm)"))
}

func ubuntu2204(f *fakeProbe) {
	f.set("/etc/os-release", osRelease("ubuntu", "22.04", "jammy", "Ubuntu 22.04.5 LTS"))
}

func ubuntu2404(f *fakeProbe) {
	f.set("/etc/os-release", osRelease("ubuntu", "24.04", "noble", "Ubuntu 24.04.1 LTS"))
}
