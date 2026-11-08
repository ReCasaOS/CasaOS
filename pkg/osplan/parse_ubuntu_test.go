package osplan

import (
	"slices"
	"strings"
	"testing"
)

// The outputs of apt in the images ubuntu:22.04 (apt 2.4.14) and ubuntu:24.04 (apt 2.8.3), on 2026-11-08, with the sources rewritten as the plan writes them (testdata/real/docker-u*):
// apt-get update, -s upgrade, -s dist-upgrade and the Trivial-Only dist-upgrade of the sizes, under LC_ALL=C. They are apt's text, byte for byte, with the status it ended with.
// What a real hop does with them was tried in the same images: from 24.04, upgrade --without-new-pkgs then dist-upgrade ended with status 0 on 26.04.1, dpkg --audit clean.

func TestTheOutputsOfAptOnUbuntu2204To2404AreRead(t *testing.T) {
	if f := ParseUpdate(realOutput(t, "docker-u22-24-update.txt")); f != nil {
		t.Errorf("update: %v", f)
	}
	upgrade := parseReal(t, "docker-u22-24-upgrade.txt")
	dist := parseReal(t, "docker-u22-24-dist-upgrade.txt")
	if upgrade.Summary != (Summary{Upgraded: 73, NotUpgraded: 17}) || len(upgrade.KeptBack) != 17 {
		t.Errorf("upgrade: %+v, %d kept back", upgrade.Summary, len(upgrade.KeptBack))
	}
	if dist.Summary != (Summary{Upgraded: 90, New: 14, Remove: 9}) {
		t.Errorf("dist-upgrade: %+v", dist.Summary)
	}
	// the renames of the 64-bit time_t (t64) and usrmerge, which Noble drops
	if want := []string{"libapt-pkg6.0", "libdb5.3", "libext2fs2", "libgnutls30", "libhogweed6", "libnettle8", "libssl3", "libtirpc3", "usrmerge"}; !slices.Equal(sorted(dist.Removals()), want) {
		t.Errorf("removals = %v, want %v", sorted(dist.Removals()), want)
	}
	if e := ParseEstimate(realOutput(t, "docker-u22-24-size.txt"), dist.Summary, "/var/tmp/x"); !e.complete() {
		t.Errorf("the sizes are not known: %+v", e)
	}
}

// From 24.04 to 26.04 apt's simulation of the full upgrade ends with status 100 and "E: Conf Broken coreutils-from-uutils:amd64": the order it simulates for the move of coreutils to
// rust-coreutils (coreutils-from-uutils Breaks coreutils) is one that dpkg does not take. The real hop goes through. It does so after the minimal upgrade too. ParseSolver alone
// reads it as a solver that failed: the plan reads it through the hop's SimBroken (simbroken_test.go).
func TestTheSimulationOfUbuntu2404To2604EndsOnTheMoveOfCoreutils(t *testing.T) {
	if f := ParseUpdate(realOutput(t, "docker-u24-26-update.txt")); f != nil {
		t.Errorf("update: %v", f)
	}
	upgrade := parseReal(t, "docker-u24-26-upgrade.txt")
	if upgrade.Summary != (Summary{Upgraded: 65, NotUpgraded: 25}) {
		t.Errorf("upgrade: %+v", upgrade.Summary)
	}
	out := realOutput(t, "docker-u24-26-dist-upgrade.txt")
	if out.ExitCode != 100 || !strings.Contains(out.Text, "\n90 upgraded, 13 newly installed, 0 to remove and 0 not upgraded.\n") {
		t.Fatalf("the fixture is not the one this test is about: status %d", out.ExitCode)
	}
	if _, f := ParseSolver(out); f == nil || f.Code != CodeSandboxSolverFailed || !strings.Contains(f.Detail, "E: Conf Broken coreutils-from-uutils:amd64") {
		t.Errorf("ParseSolver = %v, want sandbox_solver_failed on the Conf Broken of coreutils-from-uutils", f)
	}
	dist := Summary{Upgraded: 90, New: 13}
	if e := ParseEstimate(realOutput(t, "docker-u24-26-size.txt"), dist, "/var/tmp/x"); !e.complete() {
		t.Errorf("the sizes are not known: %+v", e)
	}
}
