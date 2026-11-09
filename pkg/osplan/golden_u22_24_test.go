package osplan

import (
	"context"
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
// are asked of dpkg, as the image's dpkg-query answers for them, and none stops the plan.
func TestThePlanOfUbuntu2204IsMadeWithTheRealOutputsOfApt(t *testing.T) {
	u22Hop(t)
	b := newBox(t)
	b.files["/etc/os-release"] = "ID=ubuntu\nVERSION_ID=\"22.04\"\nVERSION_CODENAME=jammy\nPRETTY_NAME=\"Ubuntu 22.04.5 LTS\"\n"
	b.files["/etc/apt/sources.list"] = string(realSource(t, "sources.list", "ubuntu-22.04-image-sources.list").Content)
	delete(b.files, "/etc/apt/sources.list.d/docker.list")
	b.facts = map[string]string{}
	for _, l := range []string{"libapt-pkg6.0\t2.4.14\tno\toptional", "libdb5.3\t5.3.28+dfsg1-0.8ubuntu3\tno\tstandard", "libext2fs2\t1.46.5-2ubuntu1.2\tno\toptional",
		"libgnutls30\t3.7.3-4ubuntu1.9\tno\toptional", "libhogweed6\t3.7.3-1build2\tno\toptional", "libnettle8\t3.7.3-1build2\tno\toptional", "libssl3\t3.0.2-0ubuntu1.30\tno\toptional",
		"libtirpc3\t1.3.2-2ubuntu0.1\tno\toptional", "usrmerge\t25ubuntu2\tno\toptional"} {
		b.facts[strings.SplitN(l, "\t", 2)[0]] = l
	}
	sb := newSandbox(b.fakeFiles, map[string]Output{
		"update":       realOutput(t, "docker-u22-24-update.txt"),
		"upgrade":      realOutput(t, "docker-u22-24-upgrade.txt"),
		"dist-upgrade": realOutput(t, "docker-u22-24-dist-upgrade.txt"),
		"estimate":     realOutput(t, "docker-u22-24-size.txt"),
	})
	offered := metaReleaseFixture(t)
	p, f := runPlan(t, b, sb, Options{MetaRelease: func(context.Context) ([]byte, error) { return []byte(offered), nil }})
	if f != nil {
		t.Fatalf("no plan: %v", f)
	}
	if p.Hop != "ubuntu-22.04-24.04" || p.Counts.DistUpgrade != (Count{Upgraded: 90, New: 14, Removed: 9}) || !p.Estimates.Known || p.Verdict == VerdictBlocked {
		t.Errorf("hop %s, counts %+v, estimates known %v, verdict %s, problems %v", p.Hop, p.Counts.DistUpgrade, p.Estimates.Known, p.Verdict, problemCodes(p))
	}
}
