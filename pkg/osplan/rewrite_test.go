package osplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

var bullseyeToBookworm = HopSpec{ID: osreadiness.HopDebian11to12, From: "bullseye", To: "bookworm", Later: []string{"trixie", "forky"}, Firmware: true}

// fakeDecider says what the report would say of the hosts it names: official hosts are the distribution's, a host that
// is in mirrors has a Release file that says so, the others are third parties, and a host in undecided is one it cannot
// tell.
type fakeDecider struct {
	mirrors, undecided []string
	err                error
	asked              int
}

func (d *fakeDecider) Suite(_ context.Context, uri, suite string) (bool, bool, error) {
	d.asked++
	if d.err != nil {
		return false, false, d.err
	}
	host := osreadiness.SourceHost(uri)
	switch {
	case osreadiness.SourceIsLocal(uri):
		return false, true, nil
	case host == "debian.org" || strings.HasSuffix(host, ".debian.org"):
		return true, true, nil
	case slices.Contains(d.mirrors, host):
		return true, true, nil
	case slices.Contains(d.undecided, host):
		return false, false, nil
	}
	return false, true, nil
}

func file(name, content string) osreadiness.SourceFile {
	return osreadiness.SourceFile{Name: name, InParts: name != "sources.list", Content: []byte(content)}
}

func rewrite(t *testing.T, d *fakeDecider, files ...osreadiness.SourceFile) (*Rewritten, *Failure) {
	t.Helper()
	if d == nil {
		d = &fakeDecider{}
	}
	out, err := RewriteSources(context.Background(), bullseyeToBookworm, files, d)
	if err != nil {
		var f *Failure
		if !errors.As(err, &f) {
			t.Fatalf("an error that is not a Failure: %v", err)
		}
		return nil, f
	}
	return out, nil
}

func mustRewrite(t *testing.T, files ...osreadiness.SourceFile) *Rewritten {
	t.Helper()
	out, f := rewrite(t, nil, files...)
	if f != nil {
		t.Fatalf("refused: %v", f)
	}
	return out
}

func treeOf(out *Rewritten) map[string]string {
	m := map[string]string{}
	for _, f := range out.Tree {
		m[f.Path] = f.Content
	}
	return m
}

func realFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "real", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// splitFixture reads a file of the measurement that is several files, each after a "==> path <==" line.
func splitFixture(text string) map[string]string {
	files := map[string]string{}
	name := ""
	for _, line := range strings.SplitAfter(text, "\n") {
		if strings.HasPrefix(line, "==> ") && strings.HasSuffix(strings.TrimRight(line, "\n"), " <==") {
			name = strings.TrimSuffix(strings.TrimPrefix(strings.TrimRight(line, "\n"), "==> "), " <==")
			files[name] = ""
			continue
		}
		files[name] += line
	}
	return files
}

// d11Box is the sources of the box of the measurement, as it had them (d11-12.txt).
func d11Box(t *testing.T) []osreadiness.SourceFile {
	t.Helper()
	parts := splitFixture(realFile(t, "d11-12-sources.txt"))
	return []osreadiness.SourceFile{
		file("docker.list", parts["/etc/apt/sources.list.d/docker.list"]),
		file("sources.list", parts["/etc/apt/sources.list"]),
	}
}

func orderLikeTheBox(files []osreadiness.SourceFile) []osreadiness.SourceFile {
	slices.SortStableFunc(files, func(a, b osreadiness.SourceFile) int {
		switch {
		case a.InParts == b.InParts:
			return strings.Compare(a.Name, b.Name)
		case !a.InParts:
			return -1
		}
		return 1
	})
	return files
}

func TestTheRealBoxOfTheMeasurementIsRewrittenAsTheMeasurementDid(t *testing.T) {
	out := mustRewrite(t, orderLikeTheBox(d11Box(t))...)
	// what the measured sandbox had, byte for byte: the sources.list the rewrite tool made, and no file for Docker
	want := splitFixture(realFile(t, "sandbox-paused-sources.txt"))
	got := treeOf(out)
	if len(got) != 1 || got["sources.list"] != want["sources.list"] {
		t.Fatalf("tree = %q\nwant sources.list = %q", got, want["sources.list"])
	}
	if want["parts/docker.list"] != "" {
		t.Fatal("the paused fixture has Docker's file")
	}
	if len(out.Paused) != 1 || out.Paused[0].File != "docker.list" || !out.Paused[0].Docker || out.Paused[0].Line != 0 ||
		!slices.Equal(out.Paused[0].Hosts, []string{"https://download.docker.com"}) {
		t.Fatalf("paused = %+v", out.Paused)
	}
	if out.DroppedBackports != 0 || out.FirmwareAdded != 0 {
		t.Errorf("dropped %d, firmware %d", out.DroppedBackports, out.FirmwareAdded)
	}
	// the files: Docker's is paused, sources.list is rewritten in its two archive lines and the three of the suite
	var names []string
	for _, f := range out.Files {
		names = append(names, f.File+":"+f.Action)
	}
	if !slices.Equal(names, []string{"sources.list:rewritten", "docker.list:paused"}) {
		t.Fatalf("files = %v", names)
	}
	sources := out.Files[0]
	if len(sources.Lines) != 6 {
		t.Fatalf("lines = %+v", sources.Lines)
	}
	l := sources.Lines[2]
	if l.N != 3 || l.Before != "deb http://archive.debian.org/debian-security bullseye-security main" ||
		l.After != "deb http://deb.debian.org/debian-security bookworm-security main" || l.Why != "suite,host" {
		t.Errorf("the archive's security line = %+v", l)
	}
	if sources.Lines[0].Why != "suite" || sources.Lines[1].After != "deb-src http://deb.debian.org/debian bookworm main" {
		t.Errorf("lines = %+v", sources.Lines)
	}
}

func TestTheHashesAreTheCanonicalOnesAndSayWhatChanged(t *testing.T) {
	files := orderLikeTheBox(d11Box(t))
	out := mustRewrite(t, files...)
	// the current files, sorted by their path, each as path NUL length NUL bytes NUL
	h := sha256.New()
	for _, f := range []osreadiness.SourceFile{files[0], files[1]} {
		p := f.Name
		if f.InParts {
			p = "sources.list.d/" + p
		}
		fmt.Fprintf(h, "%s\x00%d\x00%s\x00", p, len(f.Content), f.Content)
	}
	if got, want := out.HashCurrent, hex.EncodeToString(h.Sum(nil)); got != want {
		t.Errorf("hash_current = %s, want %s", got, want)
	}
	h = sha256.New()
	fmt.Fprintf(h, "sources.list\x00%d\x00%s\x00", len(out.Tree[0].Content), out.Tree[0].Content)
	if got, want := out.HashGenerated, hex.EncodeToString(h.Sum(nil)); got != want {
		t.Errorf("hash_generated = %s, want %s", got, want)
	}
	// each file counts: a byte of Docker's file changes the current hash, and does not change the generated one
	changed := orderLikeTheBox(d11Box(t))
	changed[1].Content = append(changed[1].Content, ' ')
	other := mustRewrite(t, changed...)
	if other.HashCurrent == out.HashCurrent {
		t.Error("a byte of a file did not change hash_current")
	}
	if other.HashGenerated != out.HashGenerated {
		t.Error("a byte of a paused file changed hash_generated")
	}
	// the name counts as well as the bytes
	renamed := orderLikeTheBox(d11Box(t))
	renamed[1].Name = "docker2.list"
	if mustRewrite(t, renamed...).HashCurrent == out.HashCurrent {
		t.Error("the name of a file did not change hash_current")
	}
}

func TestACloudImageStyleSourcesListKeepsItsComments(t *testing.T) {
	in := `# This file is managed by cloud-init
#deb http://deb.debian.org/debian bullseye main contrib

deb http://deb.debian.org/debian bullseye main
deb-src http://deb.debian.org/debian bullseye main   # the sources too
deb http://security.debian.org/debian-security bullseye-security main
	deb	http://deb.debian.org/debian	bullseye-updates	main
`
	out := mustRewrite(t, file("sources.list", in))
	want := `# This file is managed by cloud-init
#deb http://deb.debian.org/debian bullseye main contrib

deb http://deb.debian.org/debian bookworm main
deb-src http://deb.debian.org/debian bookworm main # the sources too
deb http://security.debian.org/debian-security bookworm-security main
deb http://deb.debian.org/debian bookworm-updates main
`
	if got := treeOf(out)["sources.list"]; got != want {
		t.Fatalf("tree:\n%s\nwant:\n%s", got, want)
	}
	if out.Files[0].Lines[1].Before != "deb-src http://deb.debian.org/debian bullseye main   # the sources too" {
		t.Errorf("before = %q", out.Files[0].Lines[1].Before)
	}
}

func TestBackportsAndTheirKindAreDroppedAndCounted(t *testing.T) {
	in := `deb http://deb.debian.org/debian bullseye main
deb http://deb.debian.org/debian bullseye-backports main
deb http://deb.debian.org/debian bullseye-backports-sloppy main
deb http://deb.debian.org/debian bullseye-proposed-updates main
deb http://security.debian.org bullseye/updates main
`
	out := mustRewrite(t, file("sources.list", in))
	want := `deb http://deb.debian.org/debian bookworm main
# casaos-dropped: deb http://deb.debian.org/debian bullseye-backports main
# casaos-dropped: deb http://deb.debian.org/debian bullseye-backports-sloppy main
# casaos-dropped: deb http://deb.debian.org/debian bullseye-proposed-updates main
# casaos-dropped: deb http://security.debian.org bullseye/updates main
`
	if got := treeOf(out)["sources.list"]; got != want {
		t.Fatalf("tree:\n%s\nwant:\n%s", got, want)
	}
	if out.DroppedBackports != 4 {
		t.Errorf("dropped = %d", out.DroppedBackports)
	}
	if why := out.Files[0].Lines[1].Why; why != "dropped" {
		t.Errorf("why = %q", why)
	}
}

func TestNonFreeFirmwareIsAddedBesideNonFreeAndOnlyThere(t *testing.T) {
	in := `deb http://deb.debian.org/debian bullseye main contrib non-free
deb http://deb.debian.org/debian bullseye-updates main non-free non-free-firmware
deb http://deb.debian.org/debian bullseye main contrib
deb [arch=amd64] http://security.debian.org/debian-security bullseye-security non-free
`
	out := mustRewrite(t, file("sources.list", in))
	want := `deb http://deb.debian.org/debian bookworm main contrib non-free non-free-firmware
deb http://deb.debian.org/debian bookworm-updates main non-free non-free-firmware
deb http://deb.debian.org/debian bookworm main contrib
deb [arch=amd64] http://security.debian.org/debian-security bookworm-security non-free non-free-firmware
`
	if got := treeOf(out)["sources.list"]; got != want {
		t.Fatalf("tree:\n%s\nwant:\n%s", got, want)
	}
	if out.FirmwareAdded != 2 {
		t.Errorf("firmware_added = %d", out.FirmwareAdded)
	}
	if why := out.Files[0].Lines[0].Why; why != "suite,firmware" {
		t.Errorf("why = %q", why)
	}
}

func TestAThirdPartyLineOfSourcesListIsCommentedAndListed(t *testing.T) {
	in := `deb http://deb.debian.org/debian bullseye main
deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian bullseye stable
deb https://user:secret@vendor.example.com/apt stable main
`
	out := mustRewrite(t, file("sources.list", in))
	want := `deb http://deb.debian.org/debian bookworm main
# casaos-paused: deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian bullseye stable
# casaos-paused: deb https://user:secret@vendor.example.com/apt stable main
`
	if got := treeOf(out)["sources.list"]; got != want {
		t.Fatalf("tree:\n%s\nwant:\n%s", got, want)
	}
	if len(out.Paused) != 2 || out.Paused[0].File != "sources.list" || out.Paused[0].Line != 2 || !out.Paused[0].Docker ||
		out.Paused[1].Line != 3 || out.Paused[1].Docker || !slices.Equal(out.Paused[1].Hosts, []string{"https://vendor.example.com"}) {
		t.Fatalf("paused = %+v", out.Paused)
	}
	// the shown copy hides the user and the password; the tree keeps what the file had
	l := out.Files[0].Lines[2]
	if l.Before != "deb https://***@vendor.example.com/apt stable main" || l.After != "# casaos-paused: deb https://***@vendor.example.com/apt stable main" || l.Why != "paused" {
		t.Errorf("line = %+v", l)
	}
}

func TestAFileOfSourcesListDIsRewrittenOrPausedWholeOrRefusedWhenItMixes(t *testing.T) {
	d := &fakeDecider{mirrors: []string{"mirror.example.edu"}}
	out, f := rewrite(t, d,
		file("sources.list", "deb http://deb.debian.org/debian bullseye main\n"),
		file("backports.list", "deb http://deb.debian.org/debian bullseye-backports main\n"),
		file("comments.list", "# nothing here\n"),
		file("docker.list", "deb https://download.docker.com/linux/debian bullseye stable\n"),
		file("mirror.list", "deb http://mirror.example.edu/debian bullseye main\n"),
		file("two.list", "deb https://a.example.com/x stable main\ndeb https://b.example.com:8443/y stable main\n"),
	)
	if f != nil {
		t.Fatal(f)
	}
	tree := treeOf(out)
	if tree["parts/mirror.list"] != "deb http://mirror.example.edu/debian bookworm main\n" ||
		tree["parts/backports.list"] != "# casaos-dropped: deb http://deb.debian.org/debian bullseye-backports main\n" ||
		tree["parts/comments.list"] != "# nothing here\n" {
		t.Errorf("tree = %q", tree)
	}
	if _, ok := tree["parts/docker.list"]; ok {
		t.Error("a paused file is in the tree")
	}
	if _, ok := tree["parts/two.list"]; ok {
		t.Error("a paused file is in the tree")
	}
	var actions []string
	for _, c := range out.Files {
		actions = append(actions, c.File+":"+c.Action)
	}
	if want := []string{"sources.list:rewritten", "backports.list:rewritten", "comments.list:unchanged", "docker.list:paused", "mirror.list:rewritten", "two.list:paused"}; !slices.Equal(actions, want) {
		t.Errorf("actions = %v", actions)
	}
	// paused files are sorted by file, and two hosts of a file are both said, without the port
	if len(out.Paused) != 2 || out.Paused[0].File != "docker.list" || out.Paused[1].File != "two.list" || out.Paused[1].Docker ||
		!slices.Equal(out.Paused[1].Hosts, []string{"https://a.example.com", "https://b.example.com"}) {
		t.Errorf("paused = %+v", out.Paused)
	}
	// a file that mixes the two cannot be put aside as a file and rewritten as a file
	_, f = rewrite(t, d,
		file("sources.list", "deb http://deb.debian.org/debian bullseye main\n"),
		file("mixed.list", "deb http://deb.debian.org/debian bullseye main\ndeb https://download.docker.com/linux/debian bullseye stable\n"))
	if f == nil || f.Code != CodeSourcesMixedFile || f.Data["file"] != "mixed.list" {
		t.Fatalf("mixed: %v", f)
	}
}

func TestEachRefusalNamesItsCodeItsFileAndItsLine(t *testing.T) {
	good := "deb http://deb.debian.org/debian bullseye main\n"
	for _, tc := range []struct {
		name   string
		files  []osreadiness.SourceFile
		code   string
		file   string
		line   int
		reason string
	}{
		{"a deb822 file", []osreadiness.SourceFile{file("sources.list", good), file("debian.sources", "Types: deb\n")}, CodeSourcesFormat, "debian.sources", 0, "deb822"},
		{"an empty deb822 file", []osreadiness.SourceFile{file("sources.list", good), file("x.sources", "")}, CodeSourcesFormat, "x.sources", 0, "deb822"},
		{"a file that cannot be read", []osreadiness.SourceFile{{Name: "sources.list", Err: errors.New("denied")}}, CodeSourcesFormat, "sources.list", 0, "unreadable"},
		{"a disc", []osreadiness.SourceFile{file("sources.list", "# a\ndeb cdrom:[Debian GNU/Linux 11.0 _Bullseye_ - Official amd64 DVD Binary-1 20210814-10:07]/ bullseye contrib main\n"+good)}, CodeSourcesCDROM, "sources.list", 2, "cdrom"},
		{"a disc with options", []osreadiness.SourceFile{file("sources.list", good+"deb [trusted=yes] cdrom:[Debian]/ bullseye main\n")}, CodeSourcesCDROM, "sources.list", 2, "cdrom"},
		{"a directory", []osreadiness.SourceFile{file("sources.list", good+"deb file:/srv/repo ./\n")}, CodeSourcesCDROM, "sources.list", 2, "cdrom"},
		{"a copy", []osreadiness.SourceFile{file("sources.list", good+"deb copy:/srv/repo bullseye main\n")}, CodeSourcesCDROM, "sources.list", 2, "cdrom"},
		{"an alias", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian stable main\n")}, CodeSourcesAlias, "sources.list", 2, "alias"},
		{"an alias of the updates", []osreadiness.SourceFile{file("sources.list", "deb http://deb.debian.org/debian oldstable-updates main\n"+good)}, CodeSourcesAlias, "sources.list", 1, "alias"},
		{"the old security form of an alias", []osreadiness.SourceFile{file("sources.list", good+"deb http://security.debian.org stable/updates main\n")}, CodeSourcesAlias, "sources.list", 2, "alias"},
		{"sid", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian sid main\n")}, CodeSourcesAlias, "sources.list", 2, "alias"},
		{"already the target", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian bookworm main\n")}, CodeSourcesTarget, "sources.list", 2, "target"},
		{"the target's security", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian-security bookworm-security main\n")}, CodeSourcesTarget, "sources.list", 2, "target"},
		{"past the target", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian trixie main\n")}, CodeSourcesTarget, "sources.list", 2, "target"},
		{"an older release", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian buster main\n")}, CodeSourcesFormat, "sources.list", 2, "suite"},
		{"a suite of nobody's", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian bullseye-proposed main\n")}, CodeSourcesFormat, "sources.list", 2, "suite"},
		{"a word that is not a type", []osreadiness.SourceFile{file("sources.list", good+"rpm http://deb.debian.org/debian bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "type"},
		{"a line with two words", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian\n")}, CodeSourcesFormat, "sources.list", 2, "fields"},
		{"a bare word", []osreadiness.SourceFile{file("sources.list", good+"deb\n")}, CodeSourcesFormat, "sources.list", 2, "type"},
		{"an address that is not one", []osreadiness.SourceFile{file("sources.list", good+"deb deb.debian.org/debian bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "uri"},
		{"mirror+file", []osreadiness.SourceFile{file("sources.list", good+"deb mirror+file:/etc/apt/mirrors/debian.list bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "uri"},
		{"ftp on the distribution's own host", []osreadiness.SourceFile{file("sources.list", good+"deb ftp://ftp.debian.org/debian bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "uri"},
		{"a query in an address of the distribution", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian?x=1 bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "uri"},
		{"a NUL", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian bullseye main\x00\n")}, CodeSourcesFormat, "sources.list", 2, "nul"},
		{"a NUL in a comment", []osreadiness.SourceFile{file("sources.list", "# a\x00b\n"+good)}, CodeSourcesFormat, "sources.list", 1, "nul"},
		{"a very long line", []osreadiness.SourceFile{file("sources.list", good+"# "+strings.Repeat("x", 5000)+"\n")}, CodeSourcesFormat, "sources.list", 2, "long_line"},
		{"CRLF", []osreadiness.SourceFile{file("sources.list", "deb http://deb.debian.org/debian bullseye main\r\n")}, CodeSourcesFormat, "sources.list", 1, "crlf"},
		{"a control character", []osreadiness.SourceFile{file("sources.list", good+"# \x1b[31m\n")}, CodeSourcesFormat, "sources.list", 2, "control"},
		{"bytes that are not UTF-8", []osreadiness.SourceFile{file("sources.list", good+"# d\xe9p\xf4t\n")}, CodeSourcesFormat, "sources.list", 0, "encoding"},
		{"non-ASCII in a line", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian bullseye mainé\n")}, CodeSourcesFormat, "sources.list", 2, "chars"},
		{"options that do not close", []osreadiness.SourceFile{file("sources.list", good+"deb [arch=amd64 http://deb.debian.org/debian bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "options"},
		{"options with a quote", []osreadiness.SourceFile{file("sources.list", good+"deb [arch=amd64 signed-by=\"/etc/x y\"] http://deb.debian.org/debian bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "options"},
		{"options nested", []osreadiness.SourceFile{file("sources.list", good+"deb [arch=[amd64]] http://deb.debian.org/debian bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "options"},
		{"two groups of options", []osreadiness.SourceFile{file("sources.list", good+"deb [arch=amd64] [lang=en] http://deb.debian.org/debian bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "options"},
		{"empty options", []osreadiness.SourceFile{file("sources.list", good+"deb [] http://deb.debian.org/debian bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "options"},
		{"options glued to the address", []osreadiness.SourceFile{file("sources.list", good+"deb [arch=amd64]http://deb.debian.org/debian bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "options"},
		{"an option that is not a word", []osreadiness.SourceFile{file("sources.list", good+"deb [arch] http://deb.debian.org/debian bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "options"},
		{"trusted=yes", []osreadiness.SourceFile{file("sources.list", good+"deb [trusted=yes] http://deb.debian.org/debian bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "trusted"},
		{"trusted=YES among others", []osreadiness.SourceFile{file("sources.list", good+"deb [arch=amd64 TRUSTED=YES] http://deb.debian.org/debian bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "trusted"},
		{"allow-insecure", []osreadiness.SourceFile{file("sources.list", good+"deb [allow-insecure=yes] http://deb.debian.org/debian bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "trusted"},
		{"a component that is not a name", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian bullseye main $x\n")}, CodeSourcesFormat, "sources.list", 2, "component"},
		{"nothing of the distribution", []osreadiness.SourceFile{file("sources.list", "# nothing\n"), file("docker.list", "deb https://download.docker.com/linux/debian bullseye stable\n")}, CodeSourcesFormat, "", 0, "no_distribution_line"},
		{"no sources at all", nil, CodeSourcesFormat, "", 0, "no_distribution_line"},
		{"a repository the report cannot tell", []osreadiness.SourceFile{file("sources.list", good+"deb http://undecided.example.org/debian bullseye main\n")}, CodeSourcesFormat, "sources.list", 2, "undecided"},
	} {
		_, f := rewrite(t, &fakeDecider{undecided: []string{"undecided.example.org"}}, tc.files...)
		if f == nil {
			t.Errorf("%s: not refused", tc.name)
			continue
		}
		if f.Code != tc.code {
			t.Errorf("%s: code = %s (%v), want %s", tc.name, f.Code, f, tc.code)
		}
		if f.Data["file"] != nil && f.Data["file"] != tc.file || tc.file != "" && f.Data["file"] != tc.file {
			t.Errorf("%s: file = %v, want %s", tc.name, f.Data["file"], tc.file)
		}
		if line, _ := f.Data["line"].(int); line != tc.line {
			t.Errorf("%s: line = %v, want %d", tc.name, f.Data["line"], tc.line)
		}
		if f.Data["reason"] != tc.reason {
			t.Errorf("%s: reason = %v, want %s", tc.name, f.Data["reason"], tc.reason)
		}
	}
}

func TestTheOptionsOfADistributionLineAreKeptAsWritten(t *testing.T) {
	in := "deb [ arch=amd64   signed-by=/usr/share/keyrings/debian-archive-keyring.gpg ] http://deb.debian.org/debian bullseye main\n" +
		"deb [check-valid-until=no trusted=no] http://deb.debian.org/debian bullseye-updates main\n"
	out := mustRewrite(t, file("sources.list", in))
	want := "deb [ arch=amd64   signed-by=/usr/share/keyrings/debian-archive-keyring.gpg ] http://deb.debian.org/debian bookworm main\n" +
		"deb [check-valid-until=no trusted=no] http://deb.debian.org/debian bookworm-updates main\n"
	if got := treeOf(out)["sources.list"]; got != want {
		t.Fatalf("tree:\n%s\nwant:\n%s", got, want)
	}
	// trusted=yes on a line that is not the distribution's is not this plan's business: the line is put aside
	out = mustRewrite(t, file("sources.list", "deb http://deb.debian.org/debian bullseye main\ndeb [trusted=yes] https://vendor.example.com/x stable main\n"))
	if got := treeOf(out)["sources.list"]; !strings.Contains(got, "# casaos-paused: deb [trusted=yes] https://vendor.example.com/x stable main") {
		t.Errorf("tree = %q", got)
	}
}

func TestAUserAndAQueryOfAnAddressAreHiddenInTheShownLinesAndKeptInTheTree(t *testing.T) {
	in := "deb http://gary:s3cret@deb.debian.org/debian bullseye main\n" +
		"deb https://vendor.example.com/apt?token=abc123 stable main\n" +
		"deb https://u@vendor2.example.com?k=v stable main # see https://x:y@z.example.com/p?q=1\n"
	out := mustRewrite(t, file("sources.list", in))
	tree := treeOf(out)["sources.list"]
	if !strings.Contains(tree, "http://gary:s3cret@deb.debian.org/debian bookworm main") ||
		!strings.Contains(tree, "# casaos-paused: deb https://vendor.example.com/apt?token=abc123 stable main") {
		t.Errorf("the tree lost what the file had: %q", tree)
	}
	for _, c := range out.Files {
		for _, l := range c.Lines {
			for _, secret := range []string{"s3cret", "abc123", "gary", "x:y", "k=v", "q=1"} {
				if strings.Contains(l.Before, secret) || strings.Contains(l.After, secret) {
					t.Errorf("line %d shows %q: %+v", l.N, secret, l)
				}
			}
		}
	}
	if got := out.Files[0].Lines[0].Before; got != "deb http://***@deb.debian.org/debian bullseye main" {
		t.Errorf("before = %q", got)
	}
	if got := out.Files[0].Lines[1].Before; got != "deb https://vendor.example.com/apt?*** stable main" {
		t.Errorf("before = %q", got)
	}
	if hosts := out.Paused[0].Hosts; !slices.Equal(hosts, []string{"https://vendor.example.com"}) {
		t.Errorf("hosts = %v", hosts)
	}
	for _, p := range out.Paused {
		for _, h := range p.Hosts {
			if strings.ContainsAny(h, "@?:") && !strings.HasPrefix(h, "https:") {
				t.Errorf("host %q", h)
			}
		}
	}
}

func TestAShownLineIsBounded(t *testing.T) {
	long := "deb http://deb.debian.org/debian bullseye main # " + strings.Repeat("c", 600)
	out := mustRewrite(t, file("sources.list", long+"\n"))
	l := out.Files[0].Lines[0]
	if len(l.Before) > 400 || len(l.After) > 400 || !strings.HasSuffix(l.Before, "...") || !strings.HasSuffix(l.After, "...") {
		t.Errorf("lengths %d, %d", len(l.Before), len(l.After))
	}
	// the tree has the whole line: the machine needs it
	if got := treeOf(out)["sources.list"]; !strings.Contains(got, strings.Repeat("c", 600)) {
		t.Error("the tree was cut")
	}
	// more components than a line of sources has
	many := "deb http://deb.debian.org/debian bullseye main" + strings.Repeat(" c", 100)
	if _, f := rewrite(t, nil, file("sources.list", many+"\n")); f == nil || f.Data["reason"] != "too_many" {
		t.Errorf("failure = %v", f)
	}
}

func TestTheEndOfTheTimeIsNotAFailureOfTheSources(t *testing.T) {
	_, err := RewriteSources(context.Background(), bullseyeToBookworm, []osreadiness.SourceFile{file("sources.list", "deb http://x.example.org/debian bullseye main\n")}, &fakeDecider{err: context.DeadlineExceeded})
	var f *Failure
	if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &f) {
		t.Fatalf("err = %v", err)
	}
}

func TestTheTreeIsDeterministicAndSortedWithNewlinesAndAFinalNewline(t *testing.T) {
	files := []osreadiness.SourceFile{
		file("sources.list", "deb http://deb.debian.org/debian bullseye main"),
		file("b.list", "deb http://deb.debian.org/debian bullseye-updates main"),
		file("a.list", "deb http://deb.debian.org/debian bullseye-security main\n\n\n"),
	}
	first := mustRewrite(t, files...)
	second := mustRewrite(t, files...)
	if first.HashGenerated != second.HashGenerated || first.HashCurrent != second.HashCurrent {
		t.Error("the same sources give two trees")
	}
	var paths []string
	for _, f := range first.Tree {
		paths = append(paths, f.Path)
		if !strings.HasSuffix(f.Content, "\n") || strings.Contains(f.Content, "\r") {
			t.Errorf("%s: %q", f.Path, f.Content)
		}
	}
	if !slices.Equal(paths, []string{"parts/a.list", "parts/b.list", "sources.list"}) {
		t.Errorf("paths = %v", paths)
	}
	// blank lines are kept, and the final newline is one
	if got := treeOf(first)["parts/a.list"]; got != "deb http://deb.debian.org/debian bookworm-security main\n\n\n" {
		t.Errorf("a.list = %q", got)
	}
}

func TestAnEmptyOrAbsentSourcesListGivesAnEmptyOneInTheTree(t *testing.T) {
	out := mustRewrite(t, file("x.list", "deb http://deb.debian.org/debian bullseye main\n"))
	if got := treeOf(out)["sources.list"]; got != "" {
		t.Errorf("sources.list = %q", got)
	}
	if _, ok := treeOf(out)["parts/x.list"]; !ok {
		t.Error("x.list is not in the tree")
	}
}

// ---- properties ------------------------------------------------------------------------------------------------

type gen struct{ r *rand.Rand }

func (g gen) pick(list ...string) string { return list[g.r.Intn(len(list))] }

func (g gen) distroLine() (line string, kept bool) {
	typ := g.pick("deb", "deb-src")
	opts := g.pick("", "", "[arch=amd64] ", "[ arch=amd64   signed-by=/usr/share/keyrings/k.gpg ] ")
	host := g.pick("http://deb.debian.org/debian", "https://deb.debian.org/debian", "http://archive.debian.org/debian", "http://security.debian.org/debian-security", "http://archive.debian.org/debian-security", "http://u:p@deb.debian.org/debian", "http://mirror.example.edu/debian")
	suite := g.pick("bullseye", "bullseye-updates", "bullseye-security", "bullseye-backports", "bullseye-backports-sloppy", "bullseye-proposed-updates", "bullseye/updates")
	comps := g.pick("main", "main contrib", "main contrib non-free", "main non-free non-free-firmware", "contrib")
	sep := g.pick(" ", "  ", "\t")
	comment := g.pick("", "", " # a comment", "\t#x")
	line = typ + sep + opts + host + sep + suite + sep + comps + comment
	return line, !strings.Contains(suite, "backports") && !strings.Contains(suite, "proposed") && suite != "bullseye/updates"
}

func (g gen) otherLine() string {
	return g.pick("deb", "deb-src") + " " + g.pick("[arch=amd64] ", "") + g.pick("https://download.docker.com/linux/debian", "https://vendor.example.com/apt", "http://u:p@ppa.example.org/x", "https://other.example.net:8443/y") + " " + g.pick("bullseye", "stable", "focal") + " " + g.pick("stable", "main")
}

func (g gen) filler() string {
	return g.pick("", "", "# a comment", "#deb http://deb.debian.org/debian bullseye main", "  # indented")
}

func isRepo(line string) bool {
	line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
	return line != ""
}

// aptsLists is a box where apt has kept the Release files of what the generator of the properties writes: the mirror's suites
// say Debian (Debian Backports for the backports), the third parties' say their own name. The decider is the real one.
func aptsLists() *fakeFiles {
	box := newFiles()
	keep := func(host, path, suite, origin string) {
		name := strings.ReplaceAll(host+path+"/dists/"+suite, "/", "_")
		box.files[osreadinessLists+name+"_InRelease"] = "Origin: " + origin + "\nLabel: " + origin + "\n"
	}
	for _, suite := range []string{"bullseye", "bullseye-updates", "bullseye-security", "bullseye-proposed-updates", "bullseye/updates"} {
		keep("mirror.example.edu", "/debian", suite, "Debian")
	}
	for _, suite := range []string{"bullseye-backports", "bullseye-backports-sloppy"} {
		keep("mirror.example.edu", "/debian", suite, "Debian Backports")
	}
	for _, suite := range []string{"bullseye", "stable", "focal"} {
		keep("vendor.example.com", "/apt", suite, "Vendor")
		keep("ppa.example.org", "/x", suite, "Vendor")
		keep("other.example.net:8443", "/y", suite, "Vendor")
	}
	return box
}

// where apt keeps the Release files it has fetched
const osreadinessLists = "/var/lib/apt/lists/"

func TestPropertiesOfTheRewriteOverRandomSources(t *testing.T) {
	d := osreadiness.NewDistroDecider(aptsLists())
	for seed := int64(0); seed < 400; seed++ {
		g := gen{rand.New(rand.NewSource(seed))}
		var in []string
		inRepos, keptByHand := 0, 0
		for n := g.r.Intn(12); n >= 0; n-- {
			switch g.r.Intn(4) {
			case 0:
				in = append(in, g.filler())
			case 1:
				in = append(in, g.otherLine())
				inRepos++
			default:
				l, kept := g.distroLine()
				in = append(in, l)
				inRepos++
				if kept {
					keptByHand++
				}
			}
		}
		// one line that surely stays, so that the sources are not refused for having none
		in = append(in, "deb http://deb.debian.org/debian bullseye main")
		inRepos++
		text := strings.Join(in, "\n") + "\n"
		out, err := RewriteSources(context.Background(), bullseyeToBookworm, []osreadiness.SourceFile{file("sources.list", text)}, d)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, text)
		}
		tree := treeOf(out)["sources.list"]
		outRepos, pausedLines, mirrorLines := 0, 0, 0
		for _, line := range strings.Split(strings.TrimSuffix(tree, "\n"), "\n") {
			if strings.HasPrefix(line, "# casaos-paused: ") {
				pausedLines++
			}
			if !isRepo(line) {
				continue
			}
			outRepos++
			if strings.Contains(line, "mirror.example.edu") {
				mirrorLines++
			}
			// the strict parser reads it, and it is the target's
			parsed, bad, _ := parseSourceFile([]byte(line + "\n"))
			if bad != nil || len(parsed) != 1 || !parsed[0].repo {
				t.Fatalf("seed %d: a line the strict parser does not read: %q", seed, line)
			}
			if base, _ := osreadiness.SplitSuite(parsed[0].suite); base != "bookworm" {
				t.Fatalf("seed %d: a repository line of %q: %q", seed, base, line)
			}
			if strings.Contains(strings.SplitN(line, "#", 2)[0], "bullseye") {
				t.Fatalf("seed %d: bullseye in a repository line: %q", seed, line)
			}
		}
		// no repository line is lost: it is rewritten, dropped or paused, and each is counted
		if inRepos != outRepos+out.DroppedBackports+len(out.Paused) {
			t.Fatalf("seed %d: %d lines in, %d out + %d dropped + %d paused\n%s\n--\n%s", seed, inRepos, outRepos, out.DroppedBackports, len(out.Paused), text, tree)
		}
		if outRepos != keptByHand+1 {
			t.Fatalf("seed %d: %d rewritten, %d expected\n%s\n--\n%s", seed, outRepos, keptByHand+1, text, tree)
		}
		if pausedLines != len(out.Paused) {
			t.Fatalf("seed %d: %d paused comments, %d paused", seed, pausedLines, len(out.Paused))
		}
		// its own output is never rewritten again: it is already the target (sources_target), or, for a mirror whose lists have
		// no Release file of the target yet, a repository that the plan cannot tell (sources_format, run apt-get update first)
		again := []osreadiness.SourceFile{file("sources.list", tree)}
		_, err = RewriteSources(context.Background(), bullseyeToBookworm, again, d)
		var f *Failure
		switch {
		case !errors.As(err, &f):
			t.Fatalf("seed %d: the output was rewritten again (%v)", seed, err)
		case mirrorLines == 0 && f.Code != CodeSourcesTarget:
			t.Fatalf("seed %d: the output gives %v, want sources_target", seed, f)
		case mirrorLines > 0 && !(f.Code == CodeSourcesFormat && f.Data["reason"] == "undecided"):
			t.Fatalf("seed %d: the output with a mirror gives %v, want sources_format undecided", seed, f)
		}
	}
}

// ---- what the plan does not know --------------------------------------------------------------------------------

// A repository that is not on one of Debian's archive hosts and whose Release file apt has not kept (the lists were wiped,
// or the source was added and apt-get update was not run) is not "a third party that is put aside": nobody looked, so the plan
// refuses, and says what to do. The decider here is the real one, on a machine with no lists.
func TestARepositoryThatAptHasNoReleaseFileOfIsRefusedAndNotPutAside(t *testing.T) {
	good := "deb http://deb.debian.org/debian bullseye main\n"
	for _, tc := range []struct {
		name  string
		files []osreadiness.SourceFile
		file  string
		line  int
	}{
		{"a mirror of the distribution in sources.list", []osreadiness.SourceFile{file("sources.list", good+"deb http://mirror.example.org/debian bullseye main\n")}, "sources.list", 2},
		{"a mirror whose suite is an alias, which would have escaped sources_alias", []osreadiness.SourceFile{file("sources.list", good+"deb http://mirror.example.org/debian oldstable main\n")}, "sources.list", 2},
		{"a mirror that is the only source of a file", []osreadiness.SourceFile{file("sources.list", good), file("mirror.list", "deb http://mirror.example.org/debian bullseye main\n")}, "mirror.list", 1},
		{"every source is one", []osreadiness.SourceFile{file("sources.list", "deb http://mirror.example.org/debian bullseye main\ndeb http://mirror.example.org/debian bullseye-updates main\n")}, "sources.list", 1},
		{"a vendor's repository", []osreadiness.SourceFile{file("sources.list", good+"deb https://vendor.example.com/apt stable main\n")}, "sources.list", 2},
		{"a mirror list", []osreadiness.SourceFile{file("sources.list", good+"deb mirror+http://mirror.example.org/mirrors.txt bullseye main\n")}, "sources.list", 2},
		{"a host of Debian's that is not its archive", []osreadiness.SourceFile{file("sources.list", good+"deb http://people.debian.org/~x/repo bullseye main\n")}, "sources.list", 2},
	} {
		_, err := RewriteSources(context.Background(), bullseyeToBookworm, tc.files, osreadiness.NewDistroDecider(newFiles()))
		var f *Failure
		if !errors.As(err, &f) {
			t.Errorf("%s: err = %v", tc.name, err)
			continue
		}
		if f.Code != CodeSourcesFormat || f.Data["reason"] != "undecided" || f.Data["file"] != tc.file || f.Data["line"] != tc.line || !strings.Contains(f.Detail, "run apt-get update first") {
			t.Errorf("%s: failure = %+v", tc.name, f)
		}
	}
	// Docker's own repository needs no Release file to be known for what it is, and the Release file of a mirror, when apt has
	// kept it, makes it the distribution's
	box := newFiles()
	box.files[osreadinessLists+"mirror.example.org_debian_dists_bullseye_InRelease"] = "Origin: Debian\n"
	out, err := RewriteSources(context.Background(), bullseyeToBookworm, []osreadiness.SourceFile{
		file("sources.list", good+"deb http://mirror.example.org/debian bullseye main\n"),
		file("docker.list", "deb https://download.docker.com/linux/debian bullseye stable\n"),
	}, osreadiness.NewDistroDecider(box))
	if err != nil {
		t.Fatal(err)
	}
	if got := treeOf(out)["sources.list"]; got != "deb http://deb.debian.org/debian bookworm main\ndeb http://mirror.example.org/debian bookworm main\n" {
		t.Errorf("tree = %q", got)
	}
	if len(out.Paused) != 1 || out.Paused[0].File != "docker.list" || !out.Paused[0].Docker {
		t.Errorf("paused = %+v", out.Paused)
	}
}

// ---- the rest of what a line can do to the tree ------------------------------------------------------------------------

// A line that the rewrite makes longer than a line may be (a component added, the mark of a line that is put aside or
// dropped) is refused, in the words of the file: it is not an internal failure of the plan, which nobody can act on.
func TestALineThatTheRewriteMakesTooLongIsRefusedAsALongLine(t *testing.T) {
	pad := func(prefix string) string { return prefix + " # " + strings.Repeat("x", maxLineBytes-len(prefix)-3) }
	for name, line := range map[string]string{
		"a component added":               pad("deb http://deb.debian.org/debian bullseye main non-free"),
		"a line that is put aside":        pad("deb https://vendor.example.com/apt stable main"),
		"a line that is dropped":          pad("deb http://deb.debian.org/debian bullseye-backports main"),
		"a line that is put aside, again": pad("deb [arch=amd64] https://other.example.net/apt stable main"),
	} {
		if len(line) != maxLineBytes {
			t.Fatalf("%s: the line has %d bytes", name, len(line))
		}
		box := aptsLists()
		box.files[osreadinessLists+"other.example.net_apt_dists_stable_InRelease"] = "Origin: Vendor\n"
		_, err := RewriteSources(context.Background(), bullseyeToBookworm, []osreadiness.SourceFile{file("sources.list", "deb http://deb.debian.org/debian bullseye main\n"+line+"\n")}, osreadiness.NewDistroDecider(box))
		var f *Failure
		if !errors.As(err, &f) || f.Code != CodeSourcesFormat || f.Data["reason"] != "long_line" || f.Data["line"] != 2 || f.Data["file"] != "sources.list" {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// by one byte: a line put aside or dropped is its own bytes and the mark (17 and 18 bytes)
	for n, ok := range map[int]bool{4077: true, 4078: true, 4079: false, 4080: false} {
		line := "deb http://deb.debian.org/debian bullseye-backports main # " + strings.Repeat("x", n-len("deb http://deb.debian.org/debian bullseye-backports main # "))
		out, f := rewrite(t, nil, file("sources.list", "deb http://deb.debian.org/debian bullseye main\n"+line+"\n"))
		if ok != (f == nil) || f != nil && (f.Code != CodeSourcesFormat || f.Data["reason"] != "long_line") {
			t.Errorf("a dropped line of %d bytes: refused = %v (%v), want %v", n, f != nil, f, !ok)
		}
		if f == nil && !strings.HasPrefix(treeOf(out)["sources.list"], "deb http://deb.debian.org/debian bookworm main\n# casaos-dropped: deb http://deb.debian.org/debian bullseye-backports main # xxx") {
			t.Errorf("tree = %q", treeOf(out)["sources.list"][:100])
		}
	}
	// the longest line that stays within the limit is rewritten
	line := "deb http://deb.debian.org/debian bullseye main #" + strings.Repeat("x", maxLineBytes-len("deb http://deb.debian.org/debian bullseye main #"))
	out, f := rewrite(t, nil, file("sources.list", line+"\n"))
	if f != nil || len(treeOf(out)["sources.list"]) > maxLineBytes+1 {
		t.Errorf("a line of the limit: %v", f)
	}
}

// The archive is the archive whatever the address says of its user and its port; a host that only contains the name, or
// whose user is the name, is not.
func TestTheArchiveHostIsMovedWhateverTheAddressSaysOfItsUserAndPort(t *testing.T) {
	for in, want := range map[string]string{
		"http://archive.debian.org/debian":              "http://deb.debian.org/debian",
		"http://Archive.Debian.ORG/debian":              "http://deb.debian.org/debian",
		"http://archive.debian.org:80/debian":           "http://deb.debian.org:80/debian",
		"http://u:p@archive.debian.org/debian":          "http://u:p@deb.debian.org/debian",
		"https://u@ARCHIVE.debian.org:8443/debian-sec":  "https://u@deb.debian.org:8443/debian-sec",
		"http://archive.debian.org":                     "http://deb.debian.org",
		"http://archive.debian.org:80":                  "http://deb.debian.org:80",
		"http://a@b@archive.debian.org/debian":          "http://a@b@deb.debian.org/debian",
		"http://archive.debian.org.evil.example/debian": "http://archive.debian.org.evil.example/debian",
		"http://xarchive.debian.org/debian":             "http://xarchive.debian.org/debian",
		"http://archive.debian.org@evil.example/debian": "http://archive.debian.org@evil.example/debian",
		"http://deb.debian.org/debian":                  "http://deb.debian.org/debian",
		"http://security.debian.org/debian-security":    "http://security.debian.org/debian-security",
	} {
		got, moved := moveHost(in)
		if got != want || moved != (got != in) {
			t.Errorf("moveHost(%q) = %q, %v; want %q", in, got, moved, want)
		}
	}
	out := mustRewrite(t, file("sources.list", "deb http://archive.debian.org:80/debian bullseye main\ndeb http://u:p@archive.debian.org/debian-security bullseye-security main\n"))
	if got := treeOf(out)["sources.list"]; got != "deb http://deb.debian.org:80/debian bookworm main\ndeb http://u:p@deb.debian.org/debian-security bookworm-security main\n" {
		t.Errorf("tree = %q", got)
	}
	if why := out.Files[0].Lines[0].Why; why != "suite,host" {
		t.Errorf("why = %q", why)
	}
}

// A comment may hold any text that is valid UTF-8, and some of it is not text: it reorders what is shown (bidirectional
// controls), or a terminal acts on it (C1 controls), or it cannot be seen (format characters, the byte order mark, the line
// and paragraph separators). What the plan shows, and what the runbook writes into a heredoc, has none of it. Text that is
// text is kept.
func TestAControlThatIsNotAnAsciiOneIsRefusedInAComment(t *testing.T) {
	good := "deb http://deb.debian.org/debian bullseye main"
	for name, bad := range map[string]string{
		"a right-to-left override":   "\u202e",
		"a left-to-right isolate":    "\u2066",
		"a pop directional isolate":  "\u2069",
		"a C1 control (CSI)":         "\u009b",
		"the first C1 control":       "\u0080",
		"the last C1 control":        "\u009f",
		"a zero-width space":         "\u200b",
		"a zero-width joiner":        "\u200d",
		"a soft hyphen":              "\u00ad",
		"the byte order mark":        "\ufeff",
		"a line separator":           "\u2028",
		"a paragraph separator":      "\u2029",
		"a tag character":            "\U000e0041",
		"a next line (NEL)":          "\u0085",
		"a word joiner":              "\u2060",
		"an arabic letter mark (Cf)": "\u061c",
	} {
		for where, text := range map[string]string{
			"in a comment after a line": good + " # a" + bad + "b\n",
			"in a comment line":         good + "\n# a" + bad + "b\n",
			"at the start of the file":  bad + good + "\n",
		} {
			_, f := rewrite(t, nil, file("sources.list", text))
			if f == nil || f.Code != CodeSourcesFormat || f.Data["reason"] != "control" {
				t.Errorf("%s %s: failure = %v", name, where, f)
			}
		}
	}
	// the line number is the line of the character
	if _, f := rewrite(t, nil, file("sources.list", good+"\n\n# a\u202eb\n")); f == nil || f.Data["line"] != 3 {
		t.Errorf("line = %v", f)
	}
	// text that is text
	for name, text := range map[string]string{
		"accents":       "# dépôt des paquets\n",
		"japanese":      "# 日本のミラー\n",
		"an emoji":      "# ok \U0001F600\n",
		"a no-break":    "# a\u00a0b\n",
		"a middle dot":  "# a · b\n",
		"a replacement": "# a ¿ b\n",
	} {
		if _, f := rewrite(t, nil, file("sources.list", good+"\n"+text)); f != nil {
			t.Errorf("%s: refused: %v", name, f)
		}
	}
}

// The options that make apt trust what it should not are refused on a line of the distribution, each of them.
func TestEveryOptionThatMakesAptTrustWhatItShouldNotIsRefusedOnADistributionLine(t *testing.T) {
	for _, opt := range []string{"trusted=yes", "allow-insecure=yes", "allow-weak=yes", "allow-downgrade-to-insecure=yes", "Allow-Weak=Yes", "allow-weak=1", "allow-downgrade-to-insecure=true"} {
		_, f := rewrite(t, nil, file("sources.list", "deb ["+opt+"] http://deb.debian.org/debian bullseye main\n"))
		if f == nil || f.Code != CodeSourcesFormat || f.Data["reason"] != "trusted" {
			t.Errorf("%s: failure = %v", opt, f)
		}
	}
	// and what turns them off is not refused
	for _, opt := range []string{"trusted=no", "allow-insecure=no", "allow-weak=false", "allow-downgrade-to-insecure=0"} {
		if _, f := rewrite(t, nil, file("sources.list", "deb ["+opt+"] http://deb.debian.org/debian bullseye main\n")); f != nil {
			t.Errorf("%s: refused: %v", opt, f)
		}
	}
}

func TestPausedSourcesAreSortedByFileAndByLine(t *testing.T) {
	out := mustRewrite(t,
		file("sources.list", "deb http://deb.debian.org/debian bullseye main\ndeb https://z.example.com/x stable main\ndeb https://a.example.com/x stable main\n"),
		file("docker.list", "deb https://download.docker.com/linux/debian bullseye stable\n"),
	)
	var got []string
	for _, p := range out.Paused {
		got = append(got, fmt.Sprintf("%s:%d", p.File, p.Line))
	}
	if want := []string{"docker.list:0", "sources.list:2", "sources.list:3"}; !slices.Equal(got, want) {
		t.Errorf("paused = %v, want %v", got, want)
	}
}

// A source that is already at the target release is what a machine looks like once an earlier try of the hop by hand rewrote its sources, and the plan is refused: the words of
// the refusal point at the way back, the restore script that the runbook of that earlier plan names in its own folder, and say that the script refuses once the upgrade has begun.
func TestTheRefusalOfASourceAlreadyAtTheTargetPointsAtTheRestoreScript(t *testing.T) {
	_, f := rewrite(t, &fakeDecider{}, file("sources.list", "deb http://deb.debian.org/debian bookworm main\n"))
	if f == nil || f.Code != CodeSourcesTarget {
		t.Fatalf("failure = %v", f)
	}
	for _, want := range []string{"sources.list line 1: the source is already at the target release or past it", "/var/backups", "casaos-os-upgrade-<plan id>", "restore-sources.sh", "run it with sh", "begun-upgrade", "only goes forward"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("the detail does not say %q: %q", want, f.Detail)
		}
	}
}
