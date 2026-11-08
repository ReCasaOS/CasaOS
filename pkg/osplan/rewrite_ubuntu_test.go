package osplan

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The two hops of Ubuntu, as the tests read sources for them: the one from 24.04 is the table's (TestTheHopFromUbuntu2404IsTheTables), the one from 22.04 is not there yet.
var (
	jammyToNoble    = HopSpec{ID: osreadiness.HopUbuntu2204to2404, Distro: "Ubuntu", From: "jammy", To: "noble", FromVersion: "22.04", ToVersion: "24.04", Later: []string{"resolute"}, Deb822: true}
	nobleToResolute = HopSpec{ID: osreadiness.HopUbuntu2404to2604, Distro: "Ubuntu", From: "noble", To: "resolute", FromVersion: "24.04", ToVersion: "26.04",
		Later: []string{"stonking", "tenacious"}, Deb822: true, SimBroken: []string{"coreutils-from-uutils", "sudo-common"}, OneStep: true}
)

func TestTheHopFromUbuntu2404IsTheTables(t *testing.T) {
	if hop := withUbuntuTestHop(t); !reflect.DeepEqual(hop, nobleToResolute) {
		t.Errorf("the table has %+v, the tests %+v", hop, nobleToResolute)
	}
}

// realSource is a file of sources as the image has it (testdata/real/ubuntu-*: the files of the images ubuntu:22.04 and ubuntu:24.04, copied as they are).
func realSource(t *testing.T, name, fixture string) osreadiness.SourceFile {
	t.Helper()
	b, err := os.ReadFile("testdata/real/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	return file(name, string(b))
}

func treeContent(t *testing.T, out *Rewritten, path string) string {
	t.Helper()
	for _, f := range out.Tree {
		if f.Path == path {
			return f.Content
		}
	}
	t.Fatalf("no %s in the tree", path)
	return ""
}

func mustRewriteFor(t *testing.T, hop HopSpec, files ...osreadiness.SourceFile) *Rewritten {
	t.Helper()
	out, f := rewrite13(t, nil, hop, files...)
	if f != nil {
		t.Fatalf("refused: %v", f)
	}
	return out
}

func wantRefusal(t *testing.T, hop HopSpec, reason, file string, line int, files ...osreadiness.SourceFile) {
	t.Helper()
	_, f := rewrite13(t, nil, hop, files...)
	if f == nil {
		t.Fatalf("not refused; want %s", reason)
	}
	if f.Code != CodeSourcesFormat || f.Data["reason"] != reason || f.Data["file"] != file || line > 0 && f.Data["line"] != line {
		t.Fatalf("refused with %s %v; want %s, reason %s, %s line %d", f.Code, f.Data, CodeSourcesFormat, reason, file, line)
	}
}

func TestTheSourcesOfTheImageOfUbuntu2204GoToNoble(t *testing.T) {
	src := realSource(t, "sources.list", "ubuntu-22.04-image-sources.list")
	out := mustRewriteFor(t, jammyToNoble, src)
	var want []string
	for _, l := range strings.Split(strings.TrimSuffix(string(src.Content), "\n"), "\n") {
		switch {
		case !strings.HasPrefix(l, "deb "):
		case strings.Contains(l, " jammy-backports "):
			l = droppedMark + l
		default:
			l = strings.Replace(l, " jammy", " noble", 1)
		}
		want = append(want, l)
	}
	if got := treeContent(t, out, "sources.list"); got != strings.Join(want, "\n")+"\n" {
		t.Errorf("sources.list:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	if out.DroppedBackports != 1 || len(out.Paused) != 0 || len(out.Files) != 1 || out.Files[0].Action != ActionRewritten || len(out.Files[0].Lines) != 10 {
		t.Errorf("dropped %d, paused %v, files %+v; want 1 dropped, nothing paused, 10 lines of sources.list changed", out.DroppedBackports, out.Paused, out.Files)
	}
}

func TestTheSourcesOfTheImageOfUbuntu2404GoToResolute(t *testing.T) {
	list := realSource(t, "sources.list", "ubuntu-24.04-image-sources.list")
	sources := realSource(t, "ubuntu.sources", "ubuntu-24.04-image-ubuntu.sources")
	out := mustRewriteFor(t, nobleToResolute, list, sources)
	want := strings.NewReplacer("Suites: noble noble-updates noble-backports\n", "Suites: resolute resolute-updates\n", "Suites: noble-security\n", "Suites: resolute-security\n").Replace(string(sources.Content))
	if want == string(sources.Content) {
		t.Fatal("the fixture does not have the suites this test rewrites")
	}
	if got := treeContent(t, out, "parts/ubuntu.sources"); got != want {
		t.Errorf("ubuntu.sources:\n%s\nwant:\n%s", got, want)
	}
	if got := treeContent(t, out, "sources.list"); got != string(list.Content) {
		t.Errorf("sources.list, which has only comments, changed:\n%s", got)
	}
	var whys []string
	for _, f := range out.Files {
		if f.File == "ubuntu.sources" {
			for _, l := range f.Lines {
				whys = append(whys, l.Why)
			}
		}
	}
	if out.DroppedBackports != 1 || strings.Join(whys, " ") != "suite,dropped suite" {
		t.Errorf("dropped %d, the changes of ubuntu.sources say %v; want 1 dropped, [suite,dropped suite]", out.DroppedBackports, whys)
	}
}

// Docker's own source for Ubuntu, in the two forms of its install page: it is put aside for the hop, as Debian's is.
func TestDockersSourceForUbuntuIsPutAside(t *testing.T) {
	for name, content := range map[string]string{
		"docker.list":    "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable\n",
		"docker.sources": "Types: deb\nURIs: https://download.docker.com/linux/ubuntu\nSuites: noble\nComponents: stable\nSigned-By: /etc/apt/keyrings/docker.asc\n",
	} {
		out := mustRewriteFor(t, nobleToResolute, realSource(t, "sources.list", "ubuntu-24.04-image-sources.list"),
			realSource(t, "ubuntu.sources", "ubuntu-24.04-image-ubuntu.sources"), file(name, content))
		if len(out.Paused) != 1 || out.Paused[0].File != name || !out.Paused[0].Docker {
			t.Errorf("%s: paused %+v; want the file, as Docker's", name, out.Paused)
		}
		for _, f := range out.Tree {
			if f.Path == "parts/"+name {
				t.Errorf("%s is in the tree", name)
			}
		}
	}
}

// Ubuntu Pro's sources, as the pro client writes them: in a .list on 22.04, in a .sources on 24.04. The decider would take them for a third party's.
func TestUbuntuProIsRefused(t *testing.T) {
	wantRefusal(t, jammyToNoble, "ubuntu_pro", "ubuntu-esm-apps.list", 1,
		realSource(t, "sources.list", "ubuntu-22.04-image-sources.list"),
		file("ubuntu-esm-apps.list", "deb https://esm.ubuntu.com/apps/ubuntu jammy-apps-security main\ndeb https://esm.ubuntu.com/apps/ubuntu jammy-apps-updates main\n"))
	wantRefusal(t, nobleToResolute, "ubuntu_pro", "ubuntu-esm-apps.sources", 3,
		realSource(t, "ubuntu.sources", "ubuntu-24.04-image-ubuntu.sources"),
		file("ubuntu-esm-apps.sources", "# Written by ubuntu-pro-client\nTypes: deb\nURIs: https://esm.ubuntu.com/apps/ubuntu\nSuites: noble-apps-security noble-apps-updates\nComponents: main\nSigned-By: /usr/share/keyrings/ubuntu-pro-esm-apps.gpg\n"))
}

func TestTheArchiveOfAnotherDistributionIsRefused(t *testing.T) {
	wantRefusal(t, nobleToResolute, "other_distro", "sources.list", 2,
		file("sources.list", "deb http://archive.ubuntu.com/ubuntu noble main\ndeb http://deb.debian.org/debian bookworm main\n"))
	wantRefusal(t, nobleToResolute, "other_distro", "debian.sources", 2,
		realSource(t, "ubuntu.sources", "ubuntu-24.04-image-ubuntu.sources"),
		file("debian.sources", "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bookworm\nComponents: main\n"))
	wantRefusal(t, bookwormToTrixie, "other_distro", "sources.list", 2,
		file("sources.list", "deb http://deb.debian.org/debian bookworm main\ndeb http://archive.ubuntu.com/ubuntu noble main\n"))
}

func TestTheRefreshOfUbuntu2204KeepsItsSources(t *testing.T) {
	src := realSource(t, "sources.list", "ubuntu-22.04-image-sources.list")
	out := mustRewriteFor(t, jammyToNoble.ForRefresh(), src)
	if got := treeContent(t, out, "sources.list"); got != string(src.Content) || out.Files[0].Action != ActionUnchanged {
		t.Errorf("the refresh changed sources.list (%s):\n%s", out.Files[0].Action, got)
	}
}
