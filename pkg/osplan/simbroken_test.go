package osplan

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestTheSimulationOf24To26IsReadWithoutTheMoveOfCoreutils(t *testing.T) {
	out := withUbuntuTestHop(t).withoutSimBroken(realOutput(t, "docker-u24-26-dist-upgrade.txt"))
	if out.ExitCode != 0 || strings.Contains(out.Text, "broken") || strings.Contains(out.Text, "\n Breaks:") || strings.Contains(out.Text, "E:") {
		t.Fatalf("status %d, and what is left of the error is still there", out.ExitCode)
	}
	run, f := ParseSolver(out)
	if f != nil {
		t.Fatalf("ParseSolver: %v", f)
	}
	if run.Summary != (Summary{Upgraded: 90, New: 13}) || len(run.Removals()) != 0 {
		t.Errorf("summary %+v, removals %v", run.Summary, run.Removals())
	}
	for _, name := range []string{"coreutils-from-uutils", "rust-coreutils", "gnu-coreutils"} {
		if !strings.Contains(" "+strings.Join(run.New, " ")+" ", " "+name+" ") {
			t.Errorf("%s is not among the new packages: %v", name, run.New)
		}
	}
}

func TestOnlyTheConfBrokenOfTheHopsPackagesIsLeftOut(t *testing.T) {
	real := realOutput(t, "docker-u24-26-dist-upgrade.txt")
	hop := withUbuntuTestHop(t)
	for name, out := range map[string]Output{
		"another error as well":              {Text: real.Text + "\nE: Unable to correct problems, you have held broken packages.", ExitCode: 100},
		"the Conf Broken of another package": {Text: strings.ReplaceAll(real.Text, "coreutils-from-uutils:amd64", "libc6:amd64"), ExitCode: 100},
		"another status":                     {Text: real.Text, ExitCode: 1},
		"status 100 and no error":            {Text: "Reading package lists...\n", ExitCode: 100},
	} {
		if got := hop.withoutSimBroken(out); got != out {
			t.Errorf("%s: the output was changed", name)
		}
		if _, f := ParseSolver(hop.withoutSimBroken(out)); f == nil {
			t.Errorf("%s: the parser read it", name)
		}
	}
	// a hop without SimBroken reads the real output as a solver that failed, and its texts have no sim_ok
	debian, _ := PlannedHop("debian-12-13")
	if got := debian.withoutSimBroken(real); got != real || debian.simOK() != "" || debian.simOr("x") != "" {
		t.Error("a hop of Debian lets a Conf Broken through")
	}
}

// The plan of a box of Ubuntu 24.04, made with the real outputs of apt in the image: the move of coreutils does not stop it.
func TestThePlanOfUbuntu2404IsMadeWithTheRealOutputsOfApt(t *testing.T) {
	withUbuntuTestHop(t)
	b := ubuntuBox(t)
	sb := newSandbox(b.fakeFiles, map[string]Output{
		"update":       realOutput(t, "docker-u24-26-update.txt"),
		"upgrade":      realOutput(t, "docker-u24-26-upgrade.txt"),
		"dist-upgrade": realOutput(t, "docker-u24-26-dist-upgrade.txt"),
		"estimate":     realOutput(t, "docker-u24-26-size.txt"),
	})
	offered := metaReleaseFixture(t)
	p, f := runPlan(t, b, sb, Options{MetaRelease: func(context.Context) ([]byte, error) { return []byte(offered), nil }})
	if f != nil {
		t.Fatalf("no plan: %v", f)
	}
	if p.Hop != "ubuntu-24.04-26.04" || p.Counts.DistUpgrade.Upgraded != 90 || p.Counts.DistUpgrade.New != 13 || !p.Estimates.Known {
		t.Errorf("hop %s, counts %+v, estimates known %v", p.Hop, p.Counts.DistUpgrade, p.Estimates.Known)
	}
}

// A box that has sudo (every cloud image of Ubuntu) moves it to sudo-rs as well: the simulation ends on the Conf Broken of sudo-common too, and the real upgrade goes through
// (the image ubuntu:24.04 with sudo, 2026-11-08: sudo-rs 0.2.13 in place, sudo works, dpkg --audit clean). The QEMU proof on the cloud image found it.
func TestTheSimulationOf24To26WithSudoIsRead(t *testing.T) {
	out := withUbuntuTestHop(t).withoutSimBroken(realOutput(t, "docker-u24-26-sudo-dist-upgrade.txt"))
	run, f := ParseSolver(out)
	if f != nil {
		t.Fatalf("ParseSolver: %v", f)
	}
	if run.Summary != (Summary{Upgraded: 92, New: 30}) || len(run.Removals()) != 0 {
		t.Errorf("summary %+v, removals %v", run.Summary, run.Removals())
	}
	if !strings.Contains(" "+strings.Join(run.New, " ")+" ", " sudo-rs ") {
		t.Errorf("sudo-rs is not among the new packages: %v", run.New)
	}
}

// A box of the cloud image has software-properties-common, whose update in resolute-updates Ubuntu halted at 0% (2026-11-08). Without the phased updates apt defers it, says so in a
// section the parser does not know, and counts it as not upgraded: the QEMU proof found the plan_unparsed. With them (sandboxPhased) the simulation is read and nothing is left behind.
func TestTheSimulationOf24To26WithPhasedUpdatesIsRead(t *testing.T) {
	hop := withUbuntuTestHop(t)
	if _, f := ParseSolver(hop.withoutSimBroken(realOutput(t, "docker-u24-26-phased-deferred-dist-upgrade.txt"))); f == nil || f.Data["reason"] != "unknown_line" {
		t.Fatalf("the deferred simulation: %v; want unknown_line", f)
	}
	run, f := ParseSolver(hop.withoutSimBroken(realOutput(t, "docker-u24-26-phased-dist-upgrade.txt")))
	if f != nil {
		t.Fatalf("ParseSolver: %v", f)
	}
	if run.Summary != (Summary{Upgraded: 207, New: 40, Remove: 1}) || !slices.Contains(run.Upgraded, "software-properties-common") {
		t.Errorf("summary %+v; software-properties-common upgraded: %v", run.Summary, slices.Contains(run.Upgraded, "software-properties-common"))
	}
	for _, argv := range [][]string{UpgradeArgv(SandboxParent + "/" + SandboxPrefix + "1"), DistUpgradeArgv(SandboxParent + "/" + SandboxPrefix + "1"), EstimateArgv(SandboxParent + "/" + SandboxPrefix + "1")} {
		if !slices.Contains(argv, sandboxPhased) {
			t.Errorf("%v does not include the phased updates", argv)
		}
	}
}
