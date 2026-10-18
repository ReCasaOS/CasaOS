package osplan

import (
	"context"
	"errors"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The rewrite of a refresh: the old release brought to its last state needs the same sources as the machine has, with what is not Debian's put aside, and not the sources of the
// next release. It is the same code as the rewrite of the hop (HopSpec.ForRefresh only says that the lines of the release that is left are kept), so the refusals are the same.

func rewriteRefresh(t *testing.T, d *fakeDecider, files ...osreadiness.SourceFile) (*Rewritten, *Failure) {
	t.Helper()
	if d == nil {
		d = &fakeDecider{}
	}
	out, err := RewriteSources(context.Background(), bullseyeToBookworm.ForRefresh(), files, d)
	if err != nil {
		var f *Failure
		if !errors.As(err, &f) {
			t.Fatalf("an error that is not a Failure: %v", err)
		}
		return nil, f
	}
	return out, nil
}

func mustRewriteRefresh(t *testing.T, files ...osreadiness.SourceFile) *Rewritten {
	t.Helper()
	out, f := rewriteRefresh(t, nil, files...)
	if f != nil {
		t.Fatalf("refused: %v", f)
	}
	return out
}

func TestForRefreshSaysOnlyThatTheReleaseThatIsLeftIsKept(t *testing.T) {
	r := bullseyeToBookworm.ForRefresh()
	want := bullseyeToBookworm
	want.Refresh = true
	if r.ID != want.ID || r.From != want.From || r.To != want.To || !slices.Equal(r.Later, want.Later) || r.Firmware != want.Firmware || !r.Refresh {
		t.Errorf("ForRefresh = %+v, want %+v", r, want)
	}
	if bullseyeToBookworm.Refresh {
		t.Error("the hop became a refresh")
	}
	// the table of the hops that a plan can be made for says nothing of a refresh: only ForRefresh sets it
	for _, h := range planHops {
		if h.Refresh {
			t.Errorf("the hop %s is a refresh in the table of the hops", h.ID)
		}
	}
	if hop, ok := PlannedHop(osreadiness.HopDebian11to12); !ok || hop.Refresh {
		t.Errorf("PlannedHop = %+v, %v", hop, ok)
	}
}

func TestTheSourcesOfTheRealBoxAreKeptAsTheyAreAndDockerIsPutAside(t *testing.T) {
	files := orderLikeTheBox(d11Box(t))
	out := mustRewriteRefresh(t, files...)
	// what the machine has, byte for byte: the archive host stays the archive host, bullseye stays bullseye
	got := treeOf(out)
	if len(got) != 1 || got["sources.list"] != string(files[0].Content) {
		t.Fatalf("tree = %q\nwant sources.list = %q", got, string(files[0].Content))
	}
	if len(out.Paused) != 1 || out.Paused[0].File != "docker.list" || !out.Paused[0].Docker || out.Paused[0].Line != 0 ||
		!slices.Equal(out.Paused[0].Hosts, []string{"https://download.docker.com"}) {
		t.Fatalf("paused = %+v", out.Paused)
	}
	if out.DroppedBackports != 0 || out.FirmwareAdded != 0 {
		t.Errorf("dropped %d, firmware %d", out.DroppedBackports, out.FirmwareAdded)
	}
	var names []string
	for _, f := range out.Files {
		names = append(names, f.File+":"+f.Action)
		if f.File == "sources.list" && len(f.Lines) != 0 {
			t.Errorf("sources.list has changed lines: %+v", f.Lines)
		}
	}
	if !slices.Equal(names, []string{"sources.list:unchanged", "docker.list:paused"}) {
		t.Errorf("files = %v", names)
	}
	// the hashes are those of the files and of the tree, and say that the tree is not the files (Docker's is not in it)
	if out.HashCurrent == out.HashGenerated || out.HashGenerated == "" {
		t.Errorf("hashes = %q, %q", out.HashCurrent, out.HashGenerated)
	}
	// the hop's own rewrite of the same files is not this tree
	if hop := mustRewrite(t, orderLikeTheBox(d11Box(t))...); hop.HashCurrent != out.HashCurrent || hop.HashGenerated == out.HashGenerated {
		t.Errorf("the current hashes are the files' own whichever the variant, and the trees are not the same: hop %q %q, refresh %q %q", hop.HashCurrent, hop.HashGenerated, out.HashCurrent, out.HashGenerated)
	}
}

// non-free-firmware is the next release's component: a refresh that added it to the lines of bullseye would write a component that bullseye's archive does not have.
func TestARefreshAddsNoFirmwareAndKeepsBackports(t *testing.T) {
	in := "deb http://deb.debian.org/debian bullseye main contrib non-free\n" +
		"deb http://deb.debian.org/debian bullseye-backports main non-free\n" +
		"deb http://deb.debian.org/debian bullseye-backports-sloppy main\n" +
		"deb http://security.debian.org/debian-security bullseye-security main non-free\n" +
		"deb http://deb.debian.org/debian bullseye-proposed-updates main\n" +
		"deb http://security.debian.org bullseye/updates main\n" +
		"deb-src [arch=amd64 signed-by=/usr/share/keyrings/k.gpg] http://deb.debian.org/debian bullseye-updates main non-free # the sources\n"
	out := mustRewriteRefresh(t, file("sources.list", in))
	moved := strings.NewReplacer("http://security.debian.org/debian-security bullseye-security", "http://archive.debian.org/debian-security bullseye-security", "http://security.debian.org bullseye/updates", "http://archive.debian.org/debian-security bullseye-security").Replace(in)
	if got := treeOf(out)["sources.list"]; got != moved {
		t.Errorf("the lines are not kept as they are, but for the security ones:\n%s\nwant\n%s", got, moved)
	}
	if strings.Contains(treeOf(out)["sources.list"], "non-free-firmware") || out.FirmwareAdded != 0 || out.DroppedBackports != 0 {
		t.Errorf("firmware %d, dropped %d", out.FirmwareAdded, out.DroppedBackports)
	}
	// the same lines in the hop's rewrite: moved to bookworm, firmware added, backports dropped
	hop := mustRewrite(t, file("sources.list", in))
	if !strings.Contains(treeOf(hop)["sources.list"], "non-free-firmware") || hop.DroppedBackports == 0 {
		t.Errorf("the control, the hop's rewrite, did not add the firmware or drop the backports: %q", treeOf(hop)["sources.list"])
	}
}

func TestARefreshPutsAsideWhatIsNotDebiansAsTheHopDoes(t *testing.T) {
	in := "# my sources\ndeb http://deb.debian.org/debian bullseye main\ndeb https://vendor.example.com/apt stable main\n\ndeb [arch=amd64] http://u:p@ppa.example.org/x bullseye main\n"
	hop := mustRewrite(t, file("sources.list", in), file("docker.list", "deb https://download.docker.com/linux/debian bullseye stable\n"), file("vendor.list", "deb https://vendor.example.com/apt stable main\ndeb-src https://vendor.example.com/apt stable main\n"))
	out := mustRewriteRefresh(t, file("sources.list", in), file("docker.list", "deb https://download.docker.com/linux/debian bullseye stable\n"), file("vendor.list", "deb https://vendor.example.com/apt stable main\ndeb-src https://vendor.example.com/apt stable main\n"))
	if len(out.Paused) != 4 || !reflect.DeepEqual(out.Paused, hop.Paused) {
		t.Errorf("paused: refresh %+v, hop %+v", out.Paused, hop.Paused)
	}
	want := "# my sources\ndeb http://deb.debian.org/debian bullseye main\n# casaos-paused: deb https://vendor.example.com/apt stable main\n\n# casaos-paused: deb [arch=amd64] http://u:p@ppa.example.org/x bullseye main\n"
	if got := treeOf(out)["sources.list"]; got != want {
		t.Errorf("sources.list = %q, want %q", got, want)
	}
	if len(out.Tree) != 1 || out.Tree[0].Path != "sources.list" {
		t.Errorf("the files that are put aside are in the tree: %+v", out.Tree)
	}
	// what is shown of a paused line hides the user and the query, and the kept lines are not changes
	for _, f := range out.Files {
		if f.File != "sources.list" {
			continue
		}
		for _, l := range f.Lines {
			if l.Why != "paused" || strings.Contains(l.Before, "u:p") {
				t.Errorf("a line shown: %+v", l)
			}
		}
		if f.Action != ActionRewritten || len(f.Lines) != 2 {
			t.Errorf("sources.list = %+v", f)
		}
	}
}

// The refusals of the refresh are the hop's: the same code, the same file and line, the same reason. A refresh does not read what the hop does not, and does not forgive what
// the hop refuses.
func TestTheRefusalsOfARefreshAreTheHops(t *testing.T) {
	good := "deb http://deb.debian.org/debian bullseye main\n"
	for _, tc := range []struct {
		name  string
		files []osreadiness.SourceFile
	}{
		{"a deb822 file", []osreadiness.SourceFile{file("sources.list", good), file("debian.sources", "Types: deb\n")}},
		{"a disc", []osreadiness.SourceFile{file("sources.list", good+"deb cdrom:[Debian]/ bullseye main\n")}},
		{"an alias", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian stable main\n")}},
		{"an alias of the updates", []osreadiness.SourceFile{file("sources.list", "deb http://deb.debian.org/debian oldstable-updates main\n"+good)}},
		{"sid", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian sid main\n")}},
		{"already the target", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian bookworm main\n")}},
		{"the target's security", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian-security bookworm-security main\n")}},
		{"past the target", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian trixie main\n")}},
		{"an older release", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian buster main\n")}},
		{"a suite of nobody's", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian bullseye-proposed main\n")}},
		{"trusted=yes", []osreadiness.SourceFile{file("sources.list", good+"deb [trusted=yes] http://deb.debian.org/debian bullseye main\n")}},
		{"a component that is not a name", []osreadiness.SourceFile{file("sources.list", good+"deb http://deb.debian.org/debian bullseye main $x\n")}},
		{"a line that is long once it is paused", []osreadiness.SourceFile{file("sources.list", good+"deb https://v.example.com/"+strings.Repeat("x", 4052)+" stable main\n")}},
		{"CRLF", []osreadiness.SourceFile{file("sources.list", "deb http://deb.debian.org/debian bullseye main\r\n")}},
		{"a repository the report cannot tell", []osreadiness.SourceFile{file("sources.list", good+"deb http://undecided.example.org/debian bullseye main\n")}},
		{"nothing of the distribution", []osreadiness.SourceFile{file("sources.list", "# nothing\n"), file("docker.list", "deb https://download.docker.com/linux/debian bullseye stable\n")}},
		{"a file that mixes", []osreadiness.SourceFile{file("sources.list", good), file("mixed.list", "deb http://deb.debian.org/debian bullseye main\ndeb https://vendor.example.com/apt stable main\n")}},
		{"no sources at all", nil},
	} {
		d := &fakeDecider{undecided: []string{"undecided.example.org"}}
		_, hop := rewrite(t, d, tc.files...)
		_, refresh := rewriteRefresh(t, &fakeDecider{undecided: []string{"undecided.example.org"}}, tc.files...)
		switch {
		case hop == nil && refresh == nil:
			t.Errorf("%s: neither refuses", tc.name)
		case hop == nil:
			// the one thing the refresh refuses and the hop does not would be a bug of this test, not of the code
			t.Errorf("%s: the refresh refuses (%v) and the hop does not", tc.name, refresh)
		case refresh == nil:
			t.Errorf("%s: the hop refuses (%v) and the refresh goes through", tc.name, hop)
		case refresh.Code != hop.Code || refresh.Detail != hop.Detail || !sameData(refresh.Data, hop.Data):
			t.Errorf("%s: the refresh says %v %v, the hop %v %v", tc.name, refresh.Code, refresh.Data, hop.Code, hop.Data)
		}
	}
	// and the two that a refresh is for: a line of the old release is no refusal, and a bookworm line (that the machine has already) is sources_target
	if _, f := rewriteRefresh(t, nil, file("sources.list", good)); f != nil {
		t.Errorf("a line of the old release: %v", f)
	}
	if _, f := rewriteRefresh(t, nil, file("sources.list", good+"deb http://deb.debian.org/debian bookworm main\n")); f == nil || f.Code != CodeSourcesTarget {
		t.Errorf("a line of the next release: %v", f)
	}
}

func sameData(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// The tree of a refresh is read again by the strict parser as the hop's is, and the one thing it does not look for is the line that is still of the old release.
func TestTheTreeOfARefreshIsCheckedWithoutLookingForTheOldRelease(t *testing.T) {
	tree := []TreeFile{{Path: "sources.list", Content: "deb http://deb.debian.org/debian bullseye main\n"}}
	if err := checkTree(bullseyeToBookworm, tree); err == nil || !strings.Contains(err.Error(), "still of bullseye") {
		t.Errorf("the hop's tree with a line of bullseye: %v", err)
	}
	if err := checkTree(bullseyeToBookworm.ForRefresh(), tree); err != nil {
		t.Errorf("the refresh's tree with a line of bullseye: %v", err)
	}
	// a line that the strict parser does not read is the rewrite's own bug in both
	bad := []TreeFile{{Path: "sources.list", Content: "deb http://deb.debian.org/debian\n"}}
	if err := checkTree(bullseyeToBookworm.ForRefresh(), bad); err == nil {
		t.Error("a line that the parser does not read passes the refresh's check")
	}
}

// Rewriting what a refresh wrote gives it back: the lines that were kept are kept again, and what was put aside is a comment now.
func TestTheRewriteOfARefreshIsIdempotentOnItsOwnOutput(t *testing.T) {
	in := "# a\ndeb http://deb.debian.org/debian bullseye main contrib non-free\ndeb http://deb.debian.org/debian bullseye-backports main\ndeb https://vendor.example.com/apt stable main\n"
	first := mustRewriteRefresh(t, file("sources.list", in), file("docker.list", "deb https://download.docker.com/linux/debian bullseye stable\n"))
	var again []osreadiness.SourceFile
	for _, f := range first.Tree {
		again = append(again, file(strings.TrimPrefix(f.Path, "parts/"), f.Content))
	}
	second := mustRewriteRefresh(t, again...)
	if len(second.Paused) != 0 || second.DroppedBackports != 0 || second.FirmwareAdded != 0 {
		t.Errorf("the second pass changed something: %+v", second)
	}
	if got, want := treeOf(second), treeOf(first); len(got) != len(want) || got["sources.list"] != want["sources.list"] {
		t.Errorf("tree = %q, want %q", got, want)
	}
	if second.HashGenerated != first.HashGenerated || second.HashCurrent != first.HashGenerated {
		t.Errorf("the hashes of the second pass: %q %q, first %q %q", second.HashCurrent, second.HashGenerated, first.HashCurrent, first.HashGenerated)
	}
}

// Over random sources, a refresh never loses a line of repository (it is kept or put aside, each counted), never writes a line that the strict parser does not read, never
// changes a line of the release that is kept, and never writes the next release.
func TestPropertiesOfTheRefreshRewriteOverRandomSources(t *testing.T) {
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
				// every line that the generator makes of the distribution is the distribution's, backports and proposed-updates included
				l, _ := g.distroLine()
				in = append(in, l)
				inRepos++
				keptByHand++
			}
		}
		in = append(in, "deb http://deb.debian.org/debian bullseye main")
		inRepos++
		keptByHand++
		text := strings.Join(in, "\n") + "\n"
		out, err := RewriteSources(context.Background(), bullseyeToBookworm.ForRefresh(), []osreadiness.SourceFile{file("sources.list", text)}, d)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, text)
		}
		tree := treeOf(out)["sources.list"]
		outRepos, pausedLines := 0, 0
		inLines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		for i, line := range strings.Split(strings.TrimSuffix(tree, "\n"), "\n") {
			if strings.HasPrefix(line, "# casaos-paused: ") {
				pausedLines++
				continue
			}
			if !isRepo(line) {
				continue
			}
			outRepos++
			if orig := strings.TrimRight(inLines[i], " \t"); line != orig && !movedToTheArchive(orig, line) {
				t.Fatalf("seed %d: a line that was kept is not as it was: %q, was %q", seed, line, inLines[i])
			}
			if strings.Contains(strings.SplitN(line, "#", 2)[0], "bookworm") {
				t.Fatalf("seed %d: the next release in a line: %q", seed, line)
			}
		}
		if inRepos != outRepos+len(out.Paused) || out.DroppedBackports != 0 || out.FirmwareAdded != 0 {
			t.Fatalf("seed %d: %d lines in, %d out + %d paused, dropped %d\n%s\n--\n%s", seed, inRepos, outRepos, len(out.Paused), out.DroppedBackports, text, tree)
		}
		if outRepos != keptByHand || pausedLines != len(out.Paused) {
			t.Fatalf("seed %d: %d kept, %d expected, %d paused comments, %d paused\n%s\n--\n%s", seed, outRepos, keptByHand, pausedLines, len(out.Paused), text, tree)
		}
	}
}

// movedToTheArchive says whether after is before with its security address and suite replaced by the archive's: what follows the suite (components, comment) is the same.
func movedToTheArchive(before, after string) bool {
	rest := func(s string, suites ...string) ([]string, bool) {
		f := strings.Fields(s)
		for i, w := range f {
			if slices.Contains(suites, w) {
				return f[i+1:], true
			}
		}
		return nil, false
	}
	if !strings.Contains(after, " http"+securityArchiveHost+" bullseye-security") && !strings.Contains(after, " https"+securityArchiveHost+" bullseye-security") {
		return false
	}
	want, ok := rest(after, "bullseye-security")
	got, ok2 := rest(before, "bullseye-security", "bullseye/updates")
	return ok && ok2 && slices.Equal(got, want)
}

// The security archive of Debian 11: its index on security.debian.org (and on deb.debian.org/debian-security) still lists packages whose files were moved to
// archive.debian.org/debian-security, so a download from them stops with 404 (seen on a real box, and again with apt 2.2.4 in a root of Debian 11.11: chroot-d11-refresh-security-404-download).
// The refresh writes the line it gives apt (the owner's own file is never changed) with the archive's address, as the archive serves the same Release file with the same key
// and no Valid-Until (testdata/real/archive-bullseye-security-inrelease-head.txt, chroot-d11-refresh-archive-update, chroot-d11-refresh-archive-download).
func TestARefreshReadsTheSecurityArchiveOfBullseyeFromTheArchive(t *testing.T) {
	const arch = "http://archive.debian.org/debian-security bullseye-security"
	for _, tc := range []struct {
		name, in, want string
	}{
		{"http with the path", "deb http://security.debian.org/debian-security bullseye-security main\n", "deb " + arch + " main\n"},
		{"https keeps https", "deb https://security.debian.org/debian-security bullseye-security main\n", "deb https://archive.debian.org/debian-security bullseye-security main\n"},
		{"an alias of the mirror", "deb http://cdn-fastly.deb.debian.org/debian-security bullseye-security main\n", "deb " + arch + " main\n"},
		{"an alias of the mirror, https", "deb https://cdn-aws.deb.debian.org/debian-security/ bullseye-security main\n", "deb https://archive.debian.org/debian-security bullseye-security main\n"},
		{"a name that ends like the mirror", "deb http://evil-deb.debian.org/debian-security bullseye-security main\n", "deb http://evil-deb.debian.org/debian-security bullseye-security main\n"},
		{"a slash at the end", "deb http://security.debian.org/debian-security/ bullseye-security main\n", "deb " + arch + " main\n"},
		{"without the path", "deb http://security.debian.org bullseye-security main\n", "deb " + arch + " main\n"},
		{"without the path and a slash", "deb http://security.debian.org/ bullseye-security main\n", "deb " + arch + " main\n"},
		{"the old form of the suite", "deb http://security.debian.org bullseye/updates main contrib\n", "deb " + arch + " main contrib\n"},
		{"the old form with the path", "deb http://security.debian.org/debian-security bullseye/updates main\n", "deb " + arch + " main\n"},
		{"deb-src", "deb-src http://security.debian.org/debian-security bullseye-security main\n", "deb-src " + arch + " main\n"},
		{"the components are kept", "deb http://security.debian.org/debian-security bullseye-security main contrib non-free\n", "deb " + arch + " main contrib non-free\n"},
		{"the options and the comment are kept", "deb [arch=amd64 signed-by=/usr/share/keyrings/k.gpg] http://security.debian.org/debian-security bullseye-security main # sec\n", "deb [arch=amd64 signed-by=/usr/share/keyrings/k.gpg] " + arch + " main # sec\n"},
		{"the host in capitals", "deb http://SECURITY.Debian.org/debian-security bullseye-security main\n", "deb " + arch + " main\n"},
		{"the mirror's path of the same archive", "deb http://deb.debian.org/debian-security bullseye-security main\n", "deb " + arch + " main\n"},
		// kept as they are
		{"already the archive", "deb http://archive.debian.org/debian-security bullseye-security main\n", "deb http://archive.debian.org/debian-security bullseye-security main\n"},
		{"already the archive, https", "deb https://archive.debian.org/debian-security bullseye-security main\n", "deb https://archive.debian.org/debian-security bullseye-security main\n"},
		{"the release itself", "deb http://deb.debian.org/debian bullseye main\n", "deb http://deb.debian.org/debian bullseye main\n"},
		{"the updates of the release", "deb http://deb.debian.org/debian bullseye-updates main\n", "deb http://deb.debian.org/debian bullseye-updates main\n"},
		{"the release on the security host", "deb http://security.debian.org/debian-security bullseye main\n", "deb http://security.debian.org/debian-security bullseye main\n"},
		{"a path that is not the archive's", "deb http://security.debian.org/other bullseye-security main\n", "deb http://security.debian.org/other bullseye-security main\n"},
		{"the mirror without the path", "deb http://deb.debian.org bullseye-security main\n", "deb http://deb.debian.org bullseye-security main\n"},
		{"a port", "deb http://security.debian.org:8080/debian-security bullseye-security main\n", "deb http://security.debian.org:8080/debian-security bullseye-security main\n"},
		{"a user", "deb http://u:p@security.debian.org/debian-security bullseye-security main\n", "deb http://u:p@security.debian.org/debian-security bullseye-security main\n"},
		{"a mirror of the owner's", "deb http://mirror.example.org/debian-security bullseye-security main\n", "deb http://mirror.example.org/debian-security bullseye-security main\n"},
		{"a mirror of the owner's that looks like the host", "deb http://security.debian.org.example.org/debian-security bullseye-security main\n", "deb http://security.debian.org.example.org/debian-security bullseye-security main\n"},
		{"a backports line", "deb http://security.debian.org/debian-security bullseye-backports main\n", "deb http://security.debian.org/debian-security bullseye-backports main\n"},
	} {
		d := &fakeDecider{mirrors: []string{"mirror.example.org", "security.debian.org.example.org"}}
		out, f := rewriteRefresh(t, d, file("sources.list", tc.in))
		if f != nil {
			t.Errorf("%s: refused: %v", tc.name, f)
			continue
		}
		if got := treeOf(out)["sources.list"]; got != tc.want {
			t.Errorf("%s: the tree is\n%q\nwant\n%q", tc.name, got, tc.want)
		}
		moved := tc.in != tc.want
		var why []string
		for _, fc := range out.Files {
			for _, lc := range fc.Lines {
				why = append(why, lc.Why)
			}
		}
		if moved && !slices.Equal(why, []string{WhySecurityArchive}) || !moved && len(why) != 0 {
			t.Errorf("%s: what the rewrite says it did: %q", tc.name, why)
		}
		// what the rewrite writes is read by the strict parser and is its own fixed point
		again, f := rewriteRefresh(t, d, file("sources.list", treeOf(out)["sources.list"]))
		if f != nil || treeOf(again)["sources.list"] != tc.want {
			t.Errorf("%s: the second rewrite gives %v, %v", tc.name, again, f)
		}
	}
}

func TestARefreshSaysWhichLinesItMovedToTheArchiveAndLeavesTheFilesOfTheOwnerAlone(t *testing.T) {
	in := "deb http://deb.debian.org/debian bullseye main\n# the security updates\ndeb http://security.debian.org/debian-security bullseye-security main\ndeb-src http://security.debian.org bullseye/updates main\n"
	f := file("sources.list", in)
	out := mustRewriteRefresh(t, f)
	if len(out.Files) != 1 || out.Files[0].Action != ActionRewritten || len(out.Files[0].Lines) != 2 {
		t.Fatalf("files = %+v", out.Files)
	}
	l := out.Files[0].Lines
	if l[0].N != 3 || l[1].N != 4 || l[0].Why != WhySecurityArchive || !strings.HasSuffix(l[0].After, "archive.debian.org/debian-security bullseye-security main") || l[1].Before != "deb-src http://security.debian.org bullseye/updates main" {
		t.Errorf("lines = %+v", l)
	}
	if out.SecurityArchive != 2 {
		t.Errorf("SecurityArchive = %d", out.SecurityArchive)
	}
	if string(f.Content) != in || out.HashCurrent == out.HashGenerated {
		t.Errorf("the file read was changed, or the hashes of the sources and of the tree are the same")
	}
	// a file of sources.list.d that is the distribution's is moved in the file it is in
	out = mustRewriteRefresh(t, file("sources.list", "deb http://deb.debian.org/debian bullseye main\n"), file("security.list", "deb http://security.debian.org/debian-security bullseye-security main\n"))
	if got := treeOf(out)["parts/security.list"]; got != "deb http://archive.debian.org/debian-security bullseye-security main\n" || out.SecurityArchive != 1 {
		t.Errorf("parts/security.list = %q (%d)", got, out.SecurityArchive)
	}
	// the hop does not move them: its lines are of the next release, and bookworm-security is served by security.debian.org
	hop := mustRewrite(t, file("sources.list", in))
	if strings.Contains(treeOf(hop)["sources.list"], "archive.debian.org") || hop.SecurityArchive != 0 || !strings.Contains(treeOf(hop)["sources.list"], "http://security.debian.org/debian-security bookworm-security main") {
		t.Errorf("the hop's tree: %q", treeOf(hop)["sources.list"])
	}
}

// What apt 2.2.4 and the archive said in a root of Debian 11.11 (testdata/real): the archive's Release file has no Valid-Until and apt takes it without an option, the files that
// the mirror answered 404 for are all served by the archive, and the refresh adds no option about the validity of the Release files.
func TestTheArchiveOfTheSecurityUpdatesNeedsNoOptionAboutValidity(t *testing.T) {
	if strings.Contains(strings.ToLower(realFile(t, "archive-bullseye-security-inrelease-head.txt")), "valid-until") {
		t.Error("the Release file of the archive has a Valid-Until: apt would refuse it once it is past")
	}
	if rc := strings.TrimSpace(realFile(t, "chroot-d11-refresh-archive-update.rc")); rc != "0" || strings.Contains(realFile(t, "chroot-d11-refresh-archive-update.txt"), "Err:") {
		t.Errorf("apt-get update on the archive: status %q", rc)
	}
	if rc := strings.TrimSpace(realFile(t, "chroot-d11-refresh-archive-download.rc")); rc != "0" {
		t.Errorf("the download from the archive: status %q", rc)
	}
	probe := strings.Fields(realFile(t, "archive-bullseye-security-uris-probe.txt"))
	if len(probe) != 60 {
		t.Fatalf("the probe holds %d words", len(probe))
	}
	for i := 0; i < len(probe); i += 2 {
		if probe[i] != "200" {
			t.Errorf("%s answered %s on the archive", probe[i+1], probe[i])
		}
	}
	if rc := strings.TrimSpace(realFile(t, "chroot-d11-refresh-security-404-download.rc")); rc != "100" {
		t.Errorf("the download from the mirror: status %q", rc)
	}
	for _, blk := range everyAutoBlock(refreshOf(t, false)) {
		if strings.Contains(blk.Text, "Check-Valid-Until") {
			t.Errorf("block %s sets Check-Valid-Until", blk.ID)
		}
	}
}
