package osplan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The texts of the hop from Ubuntu 24.04 to 26.04, pinned as the ones of Debian are. There is no measured box of Ubuntu: the plan is the measured box's, said to be of the
// hop from 24.04, with the sources of the image ubuntu:24.04 and Docker's docker.sources for Ubuntu rewritten by the hop. The packages it names are the measured box's.

const u24DockerSources = "Types: deb\nURIs: https://download.docker.com/linux/ubuntu\nSuites: noble\nComponents: stable\nSigned-By: /etc/apt/keyrings/docker.asc\n"

func u24Rewrite(t *testing.T, hop HopSpec) *Rewritten {
	t.Helper()
	files := []osreadiness.SourceFile{
		realSource(t, "sources.list", "ubuntu-24.04-image-sources.list"),
		file("docker.sources", u24DockerSources),
		realSource(t, "ubuntu.sources", "ubuntu-24.04-image-ubuntu.sources"),
	}
	rw, err := RewriteSources(context.Background(), hop, files, &fakeDecider{})
	if err != nil {
		t.Fatalf("the sources of the image of Ubuntu 24.04: %v", err)
	}
	return rw
}

func u24Plan(t *testing.T) Plan {
	t.Helper()
	hop := withUbuntuTestHop(t)
	rw := u24Rewrite(t, hop)
	p := *realPlan(t)
	p.Hop = string(hop.ID)
	p.Sources = SourcesPlan{Files: rw.Files, Tree: rw.Tree, HashCurrent: rw.HashCurrent, HashGenerated: rw.HashGenerated, DroppedBackports: rw.DroppedBackports, FirmwareAdded: rw.FirmwareAdded}
	p.Paused = rw.Paused
	p.PlanID = planID(p)
	return p
}

func TestGolden24To26TheRunbook(t *testing.T) {
	goldenCheck(t, "runbook-u24-26.golden", buildFor(t, u24Plan(t)).Text)
}

func TestGolden24To26TheAutoText(t *testing.T) {
	goldenCheck(t, "runbook-u24-26.auto.golden", allAuto(autoOf(t, u24Plan(t), AutoParams{RunID: testRunID, Hostname: testHostname})))
}

func TestGolden24To26TheRefresh(t *testing.T) {
	hop := withUbuntuTestHop(t).ForRefresh()
	ar, err := BuildRefresh(RefreshInputs{Hop: hop, Rewritten: u24Rewrite(t, hop), RunID: testRunID, Hostname: testHostname})
	if err != nil {
		t.Fatalf("BuildRefresh: %v", err)
	}
	goldenCheck(t, "runbook-u24-26.refresh.golden", allAuto(ar))
}

// What the goldens of 24.04 to 26.04 must say, read once here so that an -update that loses one of them is seen.
func TestTheRunbookOf24To26WritesTheSourcesOfResoluteAndLetsTheMoveOfCoreutilsThrough(t *testing.T) {
	text := buildFor(t, u24Plan(t)).Text
	auto := allAuto(autoOf(t, u24Plan(t), AutoParams{RunID: testRunID, Hostname: testHostname}))
	for _, want := range []string{
		"Suites: resolute resolute-updates\n",
		"Suites: resolute-security\n",
		"mv -f \"$APT_ETC/sources.list.d/docker.sources\" \"$APT_ETC/sources.list.d/docker.sources.casaos-paused\"",
		"NO HOP FROM UBUNTU 24.04 TO UBUNTU 26.04 WAS MEASURED. Canonical does not prescribe these commands",
		"sim_ok() { [ \"$1\" = 100 ] && grep -q '^E: ' \"$2\" && ! grep '^E: ' \"$2\" | grep -Evq '^E: Conf Broken (coreutils-from-uutils|sudo-common):[a-z0-9]+$'; }\n",
		"aptq -s dist-upgrade > \"$BK/resim.txt\" 2>&1 || sim_ok $? \"$BK/resim.txt\" || {",
		"aptq -s dist-upgrade > \"$BK/after-minimal.txt\" 2>&1 || sim_ok $? \"$BK/after-minimal.txt\" || {",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the runbook does not have %q", want)
		}
	}
	if !strings.Contains(auto, "|| sim_ok $? \"$BK/after-minimal.txt\" ||") {
		t.Error("the unit's text does not let the move of coreutils through")
	}
	for _, name := range []string{"noble-backports", "Debian"} {
		if strings.Contains(strings.Replace(text, provenance(mustCtx(t, u24Plan(t))), "", 1), name) {
			t.Errorf("the runbook names %s", name)
		}
	}
	// the texts of Debian have no sim_ok: their goldens are the ones they were
	_, rb11 := realRunbook(t)
	if strings.Contains(rb11.Text, "sim_ok") || strings.Contains(buildFor(t, d12Plan(t)).Text, "sim_ok") {
		t.Error("a text of Debian has sim_ok")
	}
}

func mustCtx(t *testing.T, p Plan) *rbCtx {
	t.Helper()
	c, _, err := newRbCtx(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// sim_ok, run by a real shell on the real outputs: the simulation of 24.04 to 26.04 goes through, and one with another error, another package or another status does not.
func TestSimOKLetsOnlyTheMoveOfCoreutilsThrough(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine (Windows without Git Bash): sim_ok is proved on Linux")
	}
	real := realFile(t, "docker-u24-26-dist-upgrade.txt")
	for name, tc := range map[string]struct {
		status string
		text   string
		ok     bool
	}{
		"the real simulation":                    {"100", real, true},
		"the real simulation of a box with sudo": {"100", realFile(t, "docker-u24-26-sudo-dist-upgrade.txt"), true},
		"another error as well":                  {"100", real + "E: Unable to correct problems, you have held broken packages.\n", false},
		"the Conf Broken of another package":     {"100", strings.ReplaceAll(real, "E: Conf Broken coreutils-from-uutils", "E: Conf Broken libc6"), false},
		"a name that only starts like it":        {"100", strings.ReplaceAll(real, "E: Conf Broken coreutils-from-uutils", "E: Conf Broken coreutils-from-uutils-x"), false},
		"another status":                         {"1", real, false},
		"status 100 and no error (apt crashed)":  {"100", "Reading package lists...\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "sim.txt")
			if err := os.WriteFile(f, []byte(tc.text), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(sh, "-c", withUbuntuTestHop(t).simOK()+`sim_ok "$1" "$2"`, "sh", tc.status, filepath.ToSlash(f)).CombinedOutput()
			if (err == nil) != tc.ok {
				t.Errorf("sim_ok = %v, want %v: %s", err == nil, tc.ok, out)
			}
		})
	}
}

func TestBlock8Of24To26FindsResoluteInASourcesFile(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine (Windows without Git Bash): the check is proved on Linux")
	}
	var check string
	for _, l := range strings.Split(buildFor(t, u24Plan(t)).Text, "\n") {
		if strings.Contains(l, "the sources of apt do not name resolute") {
			check = l
		}
	}
	if check == "" {
		t.Fatal("block 8 does not check that the sources name resolute")
	}
	image := string(realSource(t, "ubuntu.sources", "ubuntu-24.04-image-ubuntu.sources").Content)
	list := string(realSource(t, "sources.list", "ubuntu-24.04-image-sources.list").Content)
	for name, tc := range map[string]struct {
		sources string
		ok      bool
	}{
		"the image after block 3":            {strings.NewReplacer("Suites: noble noble-updates noble-backports\n", "Suites: resolute resolute-updates\n", "Suites: noble-security\n", "Suites: resolute-security\n").Replace(image), true},
		"the sources put back by the script": {image, false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			os.MkdirAll(filepath.Join(dir, "sources.list.d"), 0o755)
			os.WriteFile(filepath.Join(dir, "sources.list"), []byte(list), 0o644)
			os.WriteFile(filepath.Join(dir, "sources.list.d", "ubuntu.sources"), []byte(tc.sources), 0o644)
			cmd := exec.Command(sh, "-c", check)
			cmd.Env = append(os.Environ(), "APT_ETC="+filepath.ToSlash(dir))
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Errorf("passed = %v: %s", err == nil, out)
			}
		})
	}
}
