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

// The texts of the hop from Debian 12 to 13, pinned as the ones of 11 to 12 are. There is no measured box of Debian 12: the plan is
// the measured box's, said to be of the hop from 12, with the sources of Debian's cloud image of Debian 12 (a debian.sources that
// names its mirrors by a list, and Docker's docker.list) rewritten by the hop. The packages it names are the measured box's: a
// text of a block does not look at the release of the packages it carries.

func d12Rewrite(t *testing.T, hop HopSpec) *Rewritten {
	t.Helper()
	files := []osreadiness.SourceFile{file("sources.list", cloudSourcesList), file("debian.sources", cloudDebianSource), file("docker.list", cloudDockerList)}
	rw, err := RewriteSources(context.Background(), hop, files, &mirrorDecider{})
	if err != nil {
		t.Fatalf("the sources of the cloud image of Debian 12: %v", err)
	}
	return rw
}

// d12Plan is the plan of the hop from Debian 12 to 13 that the goldens are made of.
func d12Plan(t *testing.T) Plan {
	t.Helper()
	hop := withTestHop(t)
	rw := d12Rewrite(t, hop)
	p := *realPlan(t)
	p.Hop = string(hop.ID)
	p.Sources = SourcesPlan{Files: rw.Files, Tree: rw.Tree, HashCurrent: rw.HashCurrent, HashGenerated: rw.HashGenerated, DroppedBackports: rw.DroppedBackports, FirmwareAdded: rw.FirmwareAdded}
	p.Paused = rw.Paused
	p.PlanID = planID(p)
	return p
}

func goldenCheck(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s rewritten", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./pkg/osplan -run Golden12To13 -update writes it)", err)
	}
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); got != w {
		t.Errorf("%s is not the golden one (-update rewrites it, and the difference is read before it is kept):\n%s", name, firstDifference(got, w))
	}
}

func TestGolden12To13TheRunbook(t *testing.T) {
	goldenCheck(t, "runbook-d12-13.golden", buildFor(t, d12Plan(t)).Text)
}

func TestGolden12To13TheAutoText(t *testing.T) {
	goldenCheck(t, "runbook-d12-13.auto.golden", allAuto(autoOf(t, d12Plan(t), AutoParams{RunID: testRunID, Hostname: testHostname})))
}

func TestGolden12To13TheRefresh(t *testing.T) {
	hop := withTestHop(t).ForRefresh()
	ar, err := BuildRefresh(RefreshInputs{Hop: hop, Rewritten: d12Rewrite(t, hop), RunID: testRunID, Hostname: testHostname})
	if err != nil {
		t.Fatalf("BuildRefresh: %v", err)
	}
	goldenCheck(t, "runbook-d12-13.refresh.golden", allAuto(ar))
}

// What the goldens of 12 to 13 must say, read once here so that an -update that loses one of them is seen.
func TestTheRunbookOf12To13WritesTheSourcesOfTrixieAndSaysItWasNotMeasured(t *testing.T) {
	text := buildFor(t, d12Plan(t)).Text
	for _, want := range []string{
		"cat > \"$APT_ETC/sources.list.d/debian.sources.casaos-new\" <<'CASAOS_EOF'\nTypes: deb deb-src\nURIs: mirror+file:///etc/apt/mirrors/debian.list\nSuites: trixie trixie-updates\n",
		"mv -f \"$APT_ETC/sources.list.d/docker.list\" \"$APT_ETC/sources.list.d/docker.list.casaos-paused\"",
		"NO HOP FROM DEBIAN 12 TO DEBIAN 13 WAS MEASURED.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the runbook does not have %q", want)
		}
	}
	if strings.Contains(text, "bullseye") {
		t.Error("the runbook of 12 to 13 names bullseye")
	}
	_, rb11 := realRunbook(t)
	if strings.Contains(rb11.Text, "WAS MEASURED.") {
		t.Error("the runbook of 11 to 12 says a hop was not measured")
	}
}

// Block 8 asks whether the sources of apt name the new release. A box whose sources are .sources files names it in a Suites field, and its sources.list may
// be only a comment: the check of the hop from Debian 12 reads them, and says no to sources that still name bookworm.
func TestBlock8Of12To13FindsTrixieInASourcesFile(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine (Windows without Git Bash): the check is proved on Linux")
	}
	var check string
	for _, l := range strings.Split(buildFor(t, d12Plan(t)).Text, "\n") {
		if strings.Contains(l, "the sources of apt do not name trixie") {
			check = l
		}
	}
	if check == "" || !strings.Contains(check, "*.sources") {
		t.Fatalf("block 8 does not read the .sources files: %q", check)
	}
	for name, tc := range map[string]struct {
		files map[string]string
		ok    bool
	}{
		"the cloud image after block 3":      {map[string]string{"sources.list": cloudSourcesList, "sources.list.d/debian.sources": "Types: deb\nURIs: mirror+file:///etc/apt/mirrors/debian.list\nSuites: trixie trixie-updates\n"}, true},
		"a security stanza only":             {map[string]string{"sources.list": "", "sources.list.d/debian.sources": "Types: deb\nsuites: trixie-security\n"}, true},
		"a one-line sources.list":            {map[string]string{"sources.list": "deb http://deb.debian.org/debian trixie main\n"}, true},
		"the sources put back by the script": {map[string]string{"sources.list": cloudSourcesList, "sources.list.d/debian.sources": cloudDebianSource}, false},
		"a word that only starts like it":    {map[string]string{"sources.list": "", "sources.list.d/x.sources": "Suites: trixieish\n"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			os.MkdirAll(filepath.Join(dir, "sources.list.d"), 0o755)
			for f, content := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(sh, "-c", check)
			cmd.Env = append(os.Environ(), "APT_ETC="+filepath.ToSlash(dir))
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Errorf("passed = %v: %s", err == nil, out)
			}
		})
	}
}
