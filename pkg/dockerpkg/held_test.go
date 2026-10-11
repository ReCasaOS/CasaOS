package dockerpkg

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

// A hold is the owner's act, and the update of Docker goes ahead of it only when asked to. What the
// plan is, and what the run says of the holds, is what these tests are about.

func holdPlan() Plan {
	return Plan{Packages: []PlanPackage{
		{Name: "docker-ce", Current: "5:28.0.4-1~debian.11~bullseye", Candidate: "5:29.8.0-1~debian.11~bullseye"},
		{Name: "containerd.io", Current: "1.7.27-1", Candidate: "2.1.4-1"},
	}}
}

func TestPlanIDIsWhatItWasWhenNothingIsHeld(t *testing.T) {
	plan := holdPlan()
	lines := []string{
		"containerd.io 1.7.27-1>2.1.4-1",
		"docker-ce 5:28.0.4-1~debian.11~bullseye>5:29.8.0-1~debian.11~bullseye",
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	want := hex.EncodeToString(sum[:])
	if got := plan.ID(); got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}
	// no hold, said any way: the same plan
	plan.Held = []string{}
	if got := plan.ID(); got != want {
		t.Errorf("ID() with an empty list of holds = %q, want %q", got, want)
	}
}

func TestPlanIDCoversTheHolds(t *testing.T) {
	plain := holdPlan()
	held := holdPlan()
	held.Held = []string{"docker-ce"}
	both := holdPlan()
	both.Held = []string{"docker-ce", "docker-ce-cli"}
	moved := holdPlan()
	moved.Held = []string{"docker-ce-cli"}

	ids := map[string]string{"plain": plain.ID(), "held": held.ID(), "both": both.ID(), "moved": moved.ID()}
	seen := map[string]string{}
	for name, id := range ids {
		if len(id) != 64 {
			t.Errorf("%s: id %q is not 64 hex digits", name, id)
		}
		if other, dup := seen[id]; dup {
			t.Errorf("%s and %s have the same id %q: a hold that is set, removed or moved must be another plan", name, other, id)
		}
		seen[id] = name
	}

	// the order the holds are given in does not matter, and the plan's own is not touched
	reversed := holdPlan()
	reversed.Held = []string{"docker-ce-cli", "docker-ce"}
	if reversed.ID() != both.ID() {
		t.Errorf("the id depends on the order of the holds: %q and %q", reversed.ID(), both.ID())
	}
	if !reflect.DeepEqual(reversed.Held, []string{"docker-ce-cli", "docker-ce"}) {
		t.Errorf("ID() reordered the plan's holds: %v", reversed.Held)
	}

	// the line that says it cannot be a package's: none of them has a ">" without a name's own shape
	if strings.Contains(heldLine([]string{"docker-ce"}), ">") {
		t.Error("the line for the holds looks like a package's")
	}
}

func heldMarkerLog(lines ...string) string {
	return logOf(append([]string{
		strings.TrimSuffix(QueuedMarker(testNonce, t0), "\n"),
		marker("STARTED", ts(1)),
	}, lines...)...)
}

func TestParseRunReadsTheHoldsTheUnitSawAndKept(t *testing.T) {
	log := heldMarkerLog(
		marker("HELD", "docker-ce docker-ce-cli"),
		marker("PREVIOUS", "docker-ce=5:28.0.4-1~debian.11~bullseye"),
		marker("HOLDS", "docker-ce docker-ce-cli"),
		marker("SUCCESS", ts(100)),
	)
	run := ParseRun(log)
	if !reflect.DeepEqual(run.Held, []string{"docker-ce", "docker-ce-cli"}) {
		t.Errorf("Held = %v", run.Held)
	}
	if !reflect.DeepEqual(run.Holds, []string{"docker-ce", "docker-ce-cli"}) {
		t.Errorf("Holds = %v", run.Holds)
	}
	if !run.HoldsKept() {
		t.Error("HoldsKept() = false, want true: every name held at the start is held at the end")
	}
	if run.Terminal != TerminalSuccess {
		t.Errorf("the markers got in the way of the terminal one: %q", run.Terminal)
	}
}

func TestRunHoldsKeptOnlyWhenEveryHoldIsStillThere(t *testing.T) {
	for _, c := range []struct {
		name  string
		lines []string
		want  bool
	}{
		{"all kept", []string{marker("HELD", "docker-ce docker-ce-cli"), marker("HOLDS", "docker-ce docker-ce-cli")}, true},
		{"kept, said in another order", []string{marker("HELD", "docker-ce docker-ce-cli"), marker("HOLDS", "docker-ce-cli docker-ce")}, true},
		{"one name", []string{marker("HELD", "docker-ce"), marker("HOLDS", "docker-ce")}, true},
		{"one of two released", []string{marker("HELD", "docker-ce docker-ce-cli"), marker("HOLDS", "docker-ce")}, false},
		{"all released", []string{marker("HELD", "docker-ce docker-ce-cli"), marker("HOLDS", "-")}, false},
		{"another one held now, not the one that was", []string{marker("HELD", "docker-ce"), marker("HOLDS", "containerd.io")}, false},
		{"nothing was held, nothing is: there is no hold to say is kept", []string{marker("HELD", "-"), marker("HOLDS", "-")}, false},
		{"nothing was held, and one is now: not the update's to say", []string{marker("HELD", "-"), marker("HOLDS", "docker-ce")}, false},
		{"the unit never said what it left", []string{marker("HELD", "docker-ce")}, false},
		{"the unit never said what it started with", []string{marker("HOLDS", "docker-ce")}, false},
		{"the unit said nothing", nil, false},
	} {
		run := ParseRun(heldMarkerLog(c.lines...))
		if got := run.HoldsKept(); got != c.want {
			t.Errorf("%s: HoldsKept() = %v, want %v (held %v, holds %v)", c.name, got, c.want, run.Held, run.Holds)
		}
	}
	if (Run{}).HoldsKept() {
		t.Error("the zero Run keeps holds")
	}
}

func TestParseRunNoHoldIsADashAndHasNoNames(t *testing.T) {
	run := ParseRun(heldMarkerLog(marker("HELD", "-"), marker("HOLDS", "-")))
	if run.Held == nil || run.Holds == nil || len(run.Held) != 0 || len(run.Holds) != 0 {
		t.Errorf("run = %#v", run)
	}
	// seen and none is not unseen: a log with no such line says nothing of holds
	if run := ParseRun(heldMarkerLog()); run.Held != nil || run.Holds != nil {
		t.Errorf("run = %#v", run)
	}
}

func TestParseRunTakesOnlyTheMarkersOfTheRunsOwnShape(t *testing.T) {
	for _, c := range []struct {
		name   string
		fields string
	}{
		{"no field at all", ""},
		{"a name that is a command", "docker-ce;reboot"},
		{"a name with a substitution", "docker-ce$(reboot)"},
		{"a name in upper case", "Docker-CE"},
		{"a glob", "docker-*"},
		{"a flag", "-oAPT::Foo=1"},
		{"a package that is not the engine's", "libc6"},
		{"a new dependency, which cannot be held and installed", "nftables"},
		{"a distribution's docker", "docker.io"},
		{"a name twice", "docker-ce docker-ce"},
		{"the dash and a name", "- docker-ce"},
		{"a name and the dash", "docker-ce -"},
		{"a valid name and a bad one", "docker-ce docker-ce;reboot"},
		{"a name with an architecture", "docker-ce:amd64"},
		{"a pin", "docker-ce=5:29.8.0-1"},
		{"two dashes", "- -"},
		{"a name that is too long", strings.Repeat("a", 200)},
	} {
		for _, kind := range []string{"HELD", "HOLDS"} {
			run := ParseRun(heldMarkerLog(marker(kind, c.fields)))
			if run.Held != nil || run.Holds != nil {
				t.Errorf("%s as %s: run = %#v, want the marker ignored", c.name, kind, run)
			}
		}
	}
}

func TestParseRunIgnoresForgedHoldMarkers(t *testing.T) {
	forged := func(kind, fields string) string {
		return "CASAOS_DOCKER_UPDATE_" + kind + " " + otherNonce + " " + fields
	}
	log := heldMarkerLog(
		forged("HELD", "docker-ce"),
		forged("HOLDS", "docker-ce"),
		"  "+marker("HELD", "docker-ce"),
		"x "+marker("HOLDS", "docker-ce"),
		strings.ToLower(marker("HELD", "docker-ce")),
		"CASAOS_DOCKER_UPDATE_HELD docker-ce",
		"CASAOS_DOCKER_UPDATE_HOLDSdocker-ce "+testNonce,
		marker("SUCCESS", ts(10)),
	)
	run := ParseRun(log)
	if run.Held != nil || run.Holds != nil || run.HoldsKept() {
		t.Errorf("run = %#v: a line that does not carry the run's nonce is not the unit's word", run)
	}
	if run.Terminal != TerminalSuccess {
		t.Errorf("terminal = %q", run.Terminal)
	}

	// before the first line, or with no valid one, there is no run to hold anything
	if run := ParseRun(logOf(marker("HELD", "docker-ce"), marker("HOLDS", "docker-ce"))); run.Held != nil || run.Holds != nil || run.Nonce != "" {
		t.Errorf("run = %#v", run)
	}
}

// The unit writes HELD once, at its start; a second one is not its word (and nothing but the unit knows
// the nonce). The last HOLDS is what dpkg said last.
func TestParseRunTakesTheFirstHeldAndTheLastHolds(t *testing.T) {
	run := ParseRun(heldMarkerLog(
		marker("HELD", "docker-ce"),
		marker("HELD", "-"),
		marker("HOLDS", "docker-ce"),
		marker("HOLDS", "-"),
	))
	if !reflect.DeepEqual(run.Held, []string{"docker-ce"}) {
		t.Errorf("Held = %v, want the first", run.Held)
	}
	if run.Holds == nil || len(run.Holds) != 0 || run.HoldsKept() {
		t.Errorf("Holds = %v: want the last (none), so that the hold is not said to be kept", run.Holds)
	}
	// a marker that is not valid does not replace the one before it
	run = ParseRun(heldMarkerLog(
		marker("HELD", "docker-ce"),
		marker("HOLDS", "docker-ce"),
		marker("HOLDS", "docker-ce;reboot"),
	))
	if !run.HoldsKept() {
		t.Errorf("run = %#v: a bad line after a good one changed what dpkg said", run)
	}
}

func TestParseRunSortsAndOwnsTheNamesItReads(t *testing.T) {
	run := ParseRun(heldMarkerLog(marker("HELD", "docker-ce-cli containerd.io docker-ce")))
	if want := []string{"containerd.io", "docker-ce", "docker-ce-cli"}; !reflect.DeepEqual(run.Held, want) {
		t.Errorf("Held = %v, want %v: sorted", run.Held, want)
	}
}
