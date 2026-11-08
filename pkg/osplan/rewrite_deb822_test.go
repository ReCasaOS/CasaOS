package osplan

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

var bookwormToTrixie = HopSpec{ID: osreadiness.HopDebian12to13, Distro: "Debian", From: "bookworm", To: "trixie", FromVersion: "12", ToVersion: "13", Later: []string{"forky", "duke"}, Firmware: true, Deb822: true}

// mirrorDecider is fakeDecider with the lists of mirrors of Debian's cloud images: a list is the distribution's, unless its
// address is in undecided.
type mirrorDecider struct {
	fakeDecider
	undecidedLists []string
}

func (d *mirrorDecider) Suite(ctx context.Context, uri, suite string) (bool, bool, error) {
	if osreadiness.IsMirrorList(uri) {
		d.asked++
		return !slices.Contains(d.undecidedLists, uri), !slices.Contains(d.undecidedLists, uri), nil
	}
	return d.fakeDecider.Suite(ctx, uri, suite)
}

func rewrite13(t *testing.T, d *mirrorDecider, hop HopSpec, files ...osreadiness.SourceFile) (*Rewritten, *Failure) {
	t.Helper()
	if d == nil {
		d = &mirrorDecider{}
	}
	out, err := RewriteSources(context.Background(), hop, files, d)
	if err != nil {
		var f *Failure
		if !errors.As(err, &f) {
			t.Fatalf("an error that is not a Failure: %v", err)
		}
		return nil, f
	}
	return out, nil
}

func mustRewrite13(t *testing.T, files ...osreadiness.SourceFile) *Rewritten {
	t.Helper()
	out, f := rewrite13(t, nil, bookwormToTrixie, files...)
	if f != nil {
		t.Fatalf("refused: %v", f)
	}
	return out
}

// the sources of Debian's cloud image of Debian 12, as the measurement of the hop read them (os-upgrade-measure-sources/d12-13.txt)
const (
	cloudSourcesList  = "# See /etc/apt/sources.list.d/debian.sources\n"
	cloudDebianSource = "Types: deb deb-src\n" +
		"URIs: mirror+file:///etc/apt/mirrors/debian.list\n" +
		"Suites: bookworm bookworm-updates bookworm-backports\n" +
		"Components: main\n" +
		"Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg\n" +
		"\n" +
		"Types: deb deb-src\n" +
		"URIs: mirror+file:///etc/apt/mirrors/debian-security.list\n" +
		"Suites: bookworm-security\n" +
		"Components: main\n" +
		"Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg\n"
	cloudDockerList = "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian bookworm stable\n"
	// Docker's own install page writes this since 2025
	dockerSources = "Types: deb\n" +
		"URIs: https://download.docker.com/linux/debian\n" +
		"Suites: bookworm\n" +
		"Components: stable\n" +
		"Signed-By: /etc/apt/keyrings/docker.asc\n"
)

func TestTheCloudImageOfDebian12IsMovedToTrixie(t *testing.T) {
	out := mustRewrite13(t, file("sources.list", cloudSourcesList), file("debian.sources", cloudDebianSource), file("docker.list", cloudDockerList))
	want := "Types: deb deb-src\n" +
		"URIs: mirror+file:///etc/apt/mirrors/debian.list\n" +
		"Suites: trixie trixie-updates\n" +
		"Components: main\n" +
		"Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg\n" +
		"\n" +
		"Types: deb deb-src\n" +
		"URIs: mirror+file:///etc/apt/mirrors/debian-security.list\n" +
		"Suites: trixie-security\n" +
		"Components: main\n" +
		"Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg\n"
	tree := treeOf(out)
	if tree["parts/debian.sources"] != want {
		t.Fatalf("debian.sources:\n%s", tree["parts/debian.sources"])
	}
	if tree["sources.list"] != cloudSourcesList {
		t.Errorf("sources.list: %q", tree["sources.list"])
	}
	if _, there := tree["parts/docker.list"]; there || len(out.Paused) != 1 || out.Paused[0].File != "docker.list" || !out.Paused[0].Docker {
		t.Errorf("Docker's source is not put aside: %+v", out.Paused)
	}
	if out.DroppedBackports != 1 || out.FirmwareAdded != 0 {
		t.Errorf("dropped %d, firmware %d", out.DroppedBackports, out.FirmwareAdded)
	}
	var debian FileChange
	for _, f := range out.Files {
		if f.File == "debian.sources" {
			debian = f
		}
	}
	if debian.Action != ActionRewritten || len(debian.Lines) != 2 || debian.Lines[0].N != 3 || debian.Lines[0].Why != "suite,dropped" ||
		debian.Lines[0].After != "Suites: trixie trixie-updates" || debian.Lines[1].N != 9 || debian.Lines[1].Why != "suite" {
		t.Errorf("the changes said: %+v", debian)
	}
}

func TestDockersSourcesFileIsPutAsideWhole(t *testing.T) {
	out := mustRewrite13(t, file("sources.list", "deb http://deb.debian.org/debian bookworm main\n"), file("docker.sources", dockerSources))
	if _, there := treeOf(out)["parts/docker.sources"]; there {
		t.Fatal("docker.sources is in the tree")
	}
	if len(out.Paused) != 1 || out.Paused[0].File != "docker.sources" || !out.Paused[0].Docker || !slices.Equal(out.Paused[0].Hosts, []string{"https://download.docker.com"}) {
		t.Errorf("paused: %+v", out.Paused)
	}
	for _, f := range out.Files {
		if f.File == "docker.sources" && (f.Action != ActionPaused || len(f.Lines) != 1 || f.Lines[0].N != 2 || f.Lines[0].Why != "paused") {
			t.Errorf("%+v", f)
		}
	}
}

func TestAStanzaOfBackportsOnlyIsTurnedOff(t *testing.T) {
	src := "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bookworm\nComponents: main\n\n" +
		"Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bookworm-backports\nComponents: main\n"
	out := mustRewrite13(t, file("debian.sources", src))
	want := "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie\nComponents: main\n\n" +
		"Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bookworm-backports\nComponents: main\nEnabled: no\n"
	if got := treeOf(out)["parts/debian.sources"]; got != want {
		t.Fatalf("got:\n%s", got)
	}
	if out.DroppedBackports != 1 {
		t.Errorf("dropped %d", out.DroppedBackports)
	}
}

func TestAFileWithNoNewLineAtItsEndGetsEnabledOnALineOfItsOwn(t *testing.T) {
	src := "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bookworm\n\nTypes: deb\nURIs: http://deb.debian.org/debian\nSuites: bookworm-backports"
	out := mustRewrite13(t, file("debian.sources", src))
	want := "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie\n\nTypes: deb\nURIs: http://deb.debian.org/debian\nSuites: bookworm-backports\nEnabled: no\n"
	if got := treeOf(out)["parts/debian.sources"]; got != want {
		t.Fatalf("got %q", got)
	}
}

func TestTheFieldsThatAreNotRewrittenAreKeptAsTheyAre(t *testing.T) {
	src := "# the owner's words\n" +
		"Types: deb deb-src\n" +
		"URIs: https://deb.debian.org/debian\n" +
		"suites: bookworm\n" +
		"  bookworm-updates\n" +
		"# between the lines of a field\n" +
		"  bookworm-backports\n" +
		"Architectures: amd64\n" +
		"Components: main contrib non-free\n" +
		"Signed-By:\n" +
		" -----BEGIN PGP PUBLIC KEY BLOCK-----\n" +
		" .\n" +
		" mQINBGVxyz\n" +
		" -----END PGP PUBLIC KEY BLOCK-----\n"
	out := mustRewrite13(t, file("debian.sources", src))
	want := "# the owner's words\n" +
		"Types: deb deb-src\n" +
		"URIs: https://deb.debian.org/debian\n" +
		"suites: trixie trixie-updates\n" +
		"# between the lines of a field\n" +
		"Architectures: amd64\n" +
		"Components: main contrib non-free non-free-firmware\n" +
		"Signed-By:\n" +
		" -----BEGIN PGP PUBLIC KEY BLOCK-----\n" +
		" .\n" +
		" mQINBGVxyz\n" +
		" -----END PGP PUBLIC KEY BLOCK-----\n"
	if got := treeOf(out)["parts/debian.sources"]; got != want {
		t.Fatalf("got:\n%s", got)
	}
	if out.FirmwareAdded != 1 || out.DroppedBackports != 1 {
		t.Errorf("firmware %d, dropped %d", out.FirmwareAdded, out.DroppedBackports)
	}
}

func TestTheArchiveHostOfAStanzaGoesBackToTheMirror(t *testing.T) {
	out := mustRewrite13(t, file("debian.sources", "Types: deb\nURIs: http://archive.debian.org/debian\nSuites: bookworm\nComponents: main\n"))
	if got := treeOf(out)["parts/debian.sources"]; got != "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie\nComponents: main\n" {
		t.Fatalf("got %q", got)
	}
}

func TestADeb822FileIsRefusedWhenThePlanCannotReadIt(t *testing.T) {
	good := "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bookworm\nComponents: main\n"
	for _, tc := range []struct {
		name, content, code string
		line                int
		reason              string
	}{
		{"a stanza the owner turned off", good + "Enabled: no\n", CodeSourcesFormat, 5, "enabled"},
		{"a field apt 2.6 does not read", good + "Snapshot: 20260101T000000Z\n", CodeSourcesFormat, 5, "field"},
		{"a field written twice", good + "Suites: bookworm-updates\n", CodeSourcesFormat, 5, "field_twice"},
		{"a stanza with no suites", "Types: deb\nURIs: http://deb.debian.org/debian\n", CodeSourcesFormat, 1, "fields"},
		{"a type that is not deb", "Types: rpm\nURIs: http://deb.debian.org/debian\nSuites: bookworm\n", CodeSourcesFormat, 1, "type"},
		{"a line that goes on from nothing", " Types: deb\n", CodeSourcesFormat, 0, "deb822_form"},
		{"a carriage return", strings.ReplaceAll(good, "\n", "\r\n"), CodeSourcesFormat, 1, "crlf"},
		{"an alias", "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: stable\n", CodeSourcesAlias, 3, "alias"},
		{"the target already", "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie\n", CodeSourcesTarget, 3, "target"},
		{"a release past the target", "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: forky\n", CodeSourcesTarget, 3, "target"},
		{"another release", "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bullseye\n", CodeSourcesFormat, 3, "suite"},
		{"apt's checks turned off", good + "Trusted: yes\n", CodeSourcesFormat, 5, "trusted"},
		{"a disc", "Types: deb\nURIs: cdrom:[Debian 12]/\nSuites: bookworm\n", CodeSourcesCDROM, 2, "cdrom"},
		{"an address that is not one", "Types: deb\nURIs: deb.debian.org/debian\nSuites: bookworm\n", CodeSourcesFormat, 2, "uri"},
		{"a list of mirrors that cannot be told", "Types: deb\nURIs: mirror+file:///etc/apt/mirrors/odd.list\nSuites: bookworm\n", CodeSourcesFormat, 2, "undecided"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, f := rewrite13(t, &mirrorDecider{undecidedLists: []string{"mirror+file:///etc/apt/mirrors/odd.list"}}, bookwormToTrixie, file("debian.sources", tc.content))
			if f == nil {
				t.Fatal("not refused")
			}
			if f.Code != tc.code || f.Data["file"] != "debian.sources" || f.Data["reason"] != tc.reason {
				t.Fatalf("refused as %s %v", f.Code, f.Data)
			}
			if line, _ := f.Data["line"].(int); line != tc.line {
				t.Errorf("line %v, want %d", f.Data["line"], tc.line)
			}
		})
	}
}

func TestADeb822FileThatMixesTheDistributionAndOthersIsRefused(t *testing.T) {
	for name, content := range map[string]string{
		"in one stanza":  "Types: deb\nURIs: http://deb.debian.org/debian https://download.docker.com/linux/debian\nSuites: bookworm\n",
		"in two stanzas": "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bookworm\n\n" + dockerSources,
	} {
		t.Run(name, func(t *testing.T) {
			_, f := rewrite13(t, nil, bookwormToTrixie, file("x.sources", content))
			if f == nil || f.Code != CodeSourcesMixedFile || f.Data["file"] != "x.sources" {
				t.Fatalf("%v", f)
			}
		})
	}
}

// the hop from Debian 11 does not read them: what it does on a box that has one is what it did
func TestAHopWithoutDeb822StillRefusesASourcesFile(t *testing.T) {
	_, f := rewrite(t, nil, file("sources.list", "deb http://deb.debian.org/debian bullseye main\n"), file("debian.sources", cloudDebianSource))
	if f == nil || f.Code != CodeSourcesFormat || f.Data["reason"] != "deb822" {
		t.Fatalf("%v", f)
	}
}

func TestARefreshKeepsTheStanzasOfTheDistributionAsTheyAre(t *testing.T) {
	out, f := rewrite13(t, nil, bookwormToTrixie.ForRefresh(), file("sources.list", cloudSourcesList), file("debian.sources", cloudDebianSource), file("docker.sources", dockerSources))
	if f != nil {
		t.Fatal(f)
	}
	tree := treeOf(out)
	if tree["parts/debian.sources"] != cloudDebianSource {
		t.Errorf("the refresh changed debian.sources:\n%s", tree["parts/debian.sources"])
	}
	if _, there := tree["parts/docker.sources"]; there || len(out.Paused) != 1 {
		t.Errorf("Docker's source is not put aside for the refresh: %+v", out.Paused)
	}
	for _, fc := range out.Files {
		if fc.File == "debian.sources" && fc.Action != ActionUnchanged {
			t.Errorf("%+v", fc)
		}
	}
}

func TestTheHashOfTheSourcesHasTheSourcesFiles(t *testing.T) {
	a := mustRewrite13(t, file("debian.sources", cloudDebianSource))
	b := mustRewrite13(t, file("debian.sources", cloudDebianSource+"# a comment\n"))
	if a.HashCurrent == b.HashCurrent || a.HashGenerated == b.HashGenerated {
		t.Error("a change in a .sources file does not change the hashes")
	}
}

func TestTheTreeOfDeb822IsCheckedAgain(t *testing.T) {
	if err := checkDeb822Tree(bookwormToTrixie, TreeFile{Path: "parts/x.sources", Content: "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bookworm\n"}); err == nil {
		t.Error("a stanza still of bookworm passes")
	}
	if err := checkDeb822Tree(bookwormToTrixie, TreeFile{Path: "parts/x.sources", Content: "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bookworm-backports\nEnabled: no\n"}); err != nil {
		t.Errorf("a stanza that is turned off is refused: %v", err)
	}
}
