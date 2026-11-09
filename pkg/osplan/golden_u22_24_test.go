package osplan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The texts of the hop from Ubuntu 22.04 to 24.04, pinned as the others are. As for the hop from 24.04, the plan is the measured box's, said to be of this hop, with the sources of
// the image ubuntu:22.04 and Docker's docker.list for Ubuntu rewritten by the hop.

const u22DockerList = "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu jammy stable\n"

func u22Hop(t *testing.T) HopSpec {
	t.Helper()
	hop, ok := PlannedHop(osreadiness.HopUbuntu2204to2404)
	if !ok || len(runbookHops[hop.ID]) == 0 {
		t.Fatal("the hop from Ubuntu 22.04 to 24.04 is not in planHops and runbookHops")
	}
	return hop
}

func u22Rewrite(t *testing.T, hop HopSpec) *Rewritten {
	t.Helper()
	files := []osreadiness.SourceFile{
		realSource(t, "sources.list", "ubuntu-22.04-image-sources.list"),
		file("docker.list", u22DockerList),
	}
	rw, err := RewriteSources(context.Background(), hop, files, &fakeDecider{})
	if err != nil {
		t.Fatalf("the sources of the image of Ubuntu 22.04: %v", err)
	}
	return rw
}

func u22Plan(t *testing.T) Plan {
	t.Helper()
	hop := u22Hop(t)
	rw := u22Rewrite(t, hop)
	p := *realPlan(t)
	p.Hop = string(hop.ID)
	p.Sources = SourcesPlan{Files: rw.Files, Tree: rw.Tree, HashCurrent: rw.HashCurrent, HashGenerated: rw.HashGenerated, DroppedBackports: rw.DroppedBackports, FirmwareAdded: rw.FirmwareAdded}
	p.Paused = rw.Paused
	p.PlanID = planID(p)
	return p
}

func TestGolden22To24TheRunbook(t *testing.T) {
	goldenCheck(t, "runbook-u22-24.golden", buildFor(t, u22Plan(t)).Text)
}

func TestGolden22To24TheAutoText(t *testing.T) {
	goldenCheck(t, "runbook-u22-24.auto.golden", allAuto(autoOf(t, u22Plan(t), AutoParams{RunID: testRunID, Hostname: testHostname})))
}

func TestGolden22To24TheRefresh(t *testing.T) {
	hop := u22Hop(t).ForRefresh()
	ar, err := BuildRefresh(RefreshInputs{Hop: hop, Rewritten: u22Rewrite(t, hop), RunID: testRunID, Hostname: testHostname})
	if err != nil {
		t.Fatalf("BuildRefresh: %v", err)
	}
	goldenCheck(t, "runbook-u22-24.refresh.golden", allAuto(ar))
}

// What the goldens of 22.04 to 24.04 must say, read once here so that an -update that loses one of them is seen.
func TestTheRunbookOf22To24WritesTheSourcesOfNobleInOneStep(t *testing.T) {
	text := buildFor(t, u22Plan(t)).Text
	auto := allAuto(autoOf(t, u22Plan(t), AutoParams{RunID: testRunID, Hostname: testHostname}))
	for _, want := range []string{
		"deb http://archive.ubuntu.com/ubuntu/ noble main restricted\n",
		"deb http://security.ubuntu.com/ubuntu/ noble-security main restricted\n",
		"NO HOP FROM UBUNTU 22.04 TO UBUNTU 24.04 WAS MEASURED. Canonical does not prescribe these commands",
		"if ! aptq dist-upgrade; then\n",
		"THE POINT OF NO RETURN: the whole upgrade in one step",
		"-o APT::Get::Always-Include-Phased-Updates=true \"$@\"; }\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the runbook does not have %q", want)
		}
	}
	if !strings.Contains(auto, "if ! aptq dist-upgrade; then\n") {
		t.Error("the unit's text does not run the whole upgrade in one step")
	}
	// nothing ends apt's simulation of this hop: its texts have no sim_ok
	if strings.Contains(text, "sim_ok") || strings.Contains(auto, "sim_ok") {
		t.Error("the texts of 22.04 have sim_ok")
	}
	if strings.Contains(text, "aptq upgrade") || strings.Contains(text, "--download-only upgrade") {
		t.Error("the runbook of 22.04 runs a minimal upgrade")
	}
	if strings.Contains(strings.Replace(text, provenance(mustCtx(t, u22Plan(t))), "", 1), "Debian") {
		t.Error("the runbook names Debian")
	}
}

// The plan of a box of Ubuntu 22.04, made with the real outputs of apt in the image ubuntu:22.04: the nine packages that noble drops (the renames of the 64-bit time_t and usrmerge)
// are asked of dpkg, which answers as the cloud image does (the priority of the archive: five of them required, the QEMU proof of 2026-11-09), and none stops the plan.
func TestThePlanOfUbuntu2204IsMadeWithTheRealOutputsOfApt(t *testing.T) {
	p := u22RealPlan(t, nil)
	if p.Hop != "ubuntu-22.04-24.04" || p.Counts.DistUpgrade != (Count{Upgraded: 90, New: 14, Removed: 9}) || !p.Estimates.Known || p.Verdict == VerdictBlocked {
		t.Errorf("hop %s, counts %+v, estimates known %v, verdict %s, problems %v", p.Hop, p.Counts.DistUpgrade, p.Estimates.Known, p.Verdict, problemCodes(p))
	}
}

// A rename whose successor the upgrade does not install is the removal of a required package, as on any hop.
func TestARequiredRemovalOf22To24WithoutItsSuccessorStopsThePlan(t *testing.T) {
	p := u22RealPlan(t, func(text string) string { return strings.ReplaceAll(text, "libssl3t64", "libssl3t6x") })
	var essential []any
	for _, pr := range p.Problems {
		if pr.Code == ProblemRemovalEssential {
			essential = append(essential, pr.Data["name"])
		}
	}
	if p.Verdict != VerdictBlocked || len(essential) != 1 || essential[0] != "libssl3" {
		t.Errorf("verdict %s, removal_essential %v; want blocked on libssl3 alone", p.Verdict, essential)
	}
}

func TestOnlyTheHopsRenamesAreLetThrough(t *testing.T) {
	hop := u22Hop(t)
	for _, tc := range []struct {
		name      string
		installed []string
		ok        bool
	}{
		{"libssl3", []string{"libssl3t64"}, true},
		{"libssl3:amd64", []string{"libssl3t64:amd64"}, true},
		{"libssl3", []string{"libssl3t64x"}, false},
		{"usrmerge", nil, true},
		{"libc6", []string{"libc6t64"}, false},
	} {
		if got := hop.requiredRenamed(tc.name, tc.installed); got != tc.ok {
			t.Errorf("%s with %v: %v", tc.name, tc.installed, got)
		}
	}
	if other, _ := PlannedHop(osreadiness.HopUbuntu2404to2604); other.requiredRenamed("libssl3", []string{"libssl3t64"}) || len(other.renameCases()) != 0 {
		t.Error("the hop to 26.04 lets a rename through")
	}
}

// guard_removals of the hop, run by a real shell: a rename goes through when the simulation installs its successor or dpkg has it, and not otherwise.
func TestTheGuardOf22To24LetsTheRenamesThrough(t *testing.T) {
	code := blockOf(t, buildFor(t, u22Plan(t)).Text, "minimal")
	def := between(t, code, "removed_of() {", "\n") + between(t, code, "guard_removals() {", "\n}\n")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// dpkg-query as the cloud image answers: the five are required, libtirpc3t64 is installed already, nothing else is
	fake := "#!/bin/sh\nfor last; do :; done\ncase \"$*\" in\n  *Status*libtirpc3t64) echo 'install ok installed';;\n  *Status*) exit 1;;\nesac\ncase \"$last\" in\n" +
		"  libdb5.3|libext2fs2|libssl3|libtirpc3|usrmerge|a-required) echo 'no required';;\n  *) echo 'no optional';;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "bin", "dpkg-query"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		removed []string
		inst    string
		stops   bool
	}{
		{"the renames, their successors installed", []string{"libssl3", "libdb5.3", "libext2fs2", "usrmerge"}, "Inst libssl3t64 (1)\nInst libdb5.3t64 (1)\nInst libext2fs2t64 (1)\n", false},
		{"a rename whose successor dpkg has", []string{"libtirpc3"}, "", false},
		{"a rename whose successor is not installed", []string{"libssl3"}, "", true},
		{"another required package", []string{"a-required"}, "Inst a-requiredt64 (1)\n", true},
	} {
		sub := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-"))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(sub, "after-minimal.txt"), []byte(resimOf(tc.removed...)+tc.inst), 0o644)
		script := "(\nset -eu\nLC_ALL=C; export LC_ALL\nPATH=\"" + "$(cd .. && pwd)/bin:$PATH\"; export PATH\nBK=.\n" + def + "\nguard_removals after-minimal.txt\necho REACHED\n)"
		out, exit := runSh(t, sub, script)
		if stopped := exit != 0 && strings.Contains(out, "STOP:") && !strings.Contains(out, "REACHED"); stopped != tc.stops || (!tc.stops && !strings.Contains(out, "REACHED")) {
			t.Errorf("%s: exit %d, %q", tc.name, exit, out)
		}
	}
}

// u22RealPlan is the plan of a box of Ubuntu 22.04 with the real outputs of apt in the image, its dist-upgrade changed by change when it is not nil.
func u22RealPlan(t *testing.T, change func(string) string) *Plan {
	t.Helper()
	u22Hop(t)
	b := newBox(t)
	b.files["/etc/os-release"] = "ID=ubuntu\nVERSION_ID=\"22.04\"\nVERSION_CODENAME=jammy\nPRETTY_NAME=\"Ubuntu 22.04.5 LTS\"\n"
	b.files["/etc/apt/sources.list"] = string(realSource(t, "sources.list", "ubuntu-22.04-image-sources.list").Content)
	delete(b.files, "/etc/apt/sources.list.d/docker.list")
	b.facts = map[string]string{}
	for _, l := range []string{"libapt-pkg6.0\t2.4.15ubuntu0.1\tno\timportant", "libdb5.3\t5.3.28+dfsg1-0.8ubuntu3\tno\trequired", "libext2fs2\t1.46.5-2ubuntu1.2\tno\trequired",
		"libgnutls30\t3.7.3-4ubuntu1.9\tno\timportant", "libhogweed6\t3.7.3-1build2\tno\timportant", "libnettle8\t3.7.3-1build2\tno\timportant", "libssl3\t3.0.2-0ubuntu1.30\tno\trequired",
		"libtirpc3\t1.3.2-2ubuntu0.1\tno\trequired", "usrmerge\t25ubuntu2\tno\trequired"} {
		b.facts[strings.SplitN(l, "\t", 2)[0]] = l
	}
	dist := realOutput(t, "docker-u22-24-dist-upgrade.txt")
	if change != nil {
		dist.Text = change(dist.Text)
	}
	sb := newSandbox(b.fakeFiles, map[string]Output{
		"update":       realOutput(t, "docker-u22-24-update.txt"),
		"upgrade":      realOutput(t, "docker-u22-24-upgrade.txt"),
		"dist-upgrade": dist,
		"estimate":     realOutput(t, "docker-u22-24-size.txt"),
	})
	offered := metaReleaseFixture(t)
	p, f := runPlan(t, b, sb, Options{MetaRelease: func(context.Context) ([]byte, error) { return []byte(offered), nil }})
	if f != nil {
		t.Fatalf("no plan: %v", f)
	}
	return p
}
