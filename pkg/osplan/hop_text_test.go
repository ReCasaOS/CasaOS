package osplan

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// d11 is a text of the blocks as the hop from Debian 11 to 12 writes it: the words that stand for the releases replaced by its own.
func d11(s string) string { return (&rbCtx{hop: planHops[0]}).names(s) }

// restoreScriptHash is the hash of the owner's restore script, as the runbook of the hop from Debian 11 to 12 writes it.
func restoreScriptHash() string { return restoreScriptHashOf(d11(restoreScript)) }

// withTestHop is the second Debian hop of the two tables, the one from Debian 12 to 13, which the tests of the text and of the folders look at.
func withTestHop(t *testing.T) HopSpec {
	t.Helper()
	hop, ok := PlannedHop(osreadiness.HopDebian12to13)
	if !ok || len(runbookHops[hop.ID]) == 0 {
		t.Fatal("the hop from Debian 12 to 13 is not in planHops and runbookHops")
	}
	return hop
}

// planFor is the plan of the real box, said to be of hop: the text of a block does not look at the release of the sources it carries.
func planFor(t *testing.T, hop HopSpec) Plan {
	t.Helper()
	p := *realPlan(t)
	p.Hop = string(hop.ID)
	return p
}

func TestTheTextOfAHopNamesItsOwnReleases(t *testing.T) {
	hop := withTestHop(t)
	p := planFor(t, hop)
	rb := buildFor(t, p)
	c, _, err := newRbCtx(p)
	if err != nil {
		t.Fatal(err)
	}
	auto := autoOf(t, p, AutoParams{RunID: testRunID, Hostname: testHostname})
	// the refresh reads the sources of the release it brings up to date: the real box's, as a box of Debian 12 has them
	box := d11Box(t)
	for i := range box {
		box[i].Content = []byte(strings.ReplaceAll(string(box[i].Content), "bullseye", "bookworm"))
	}
	rw, err := RewriteSources(context.Background(), hop.ForRefresh(), orderLikeTheBox(box), &fakeDecider{})
	if err != nil {
		t.Fatalf("the sources of the real box: %v", err)
	}
	refresh, err := BuildRefresh(RefreshInputs{Hop: hop.ForRefresh(), Rewritten: rw, RunID: testRunID, Hostname: testHostname})
	if err != nil {
		t.Fatalf("BuildRefresh: %v", err)
	}

	texts := map[string]string{"the runbook": strings.Replace(rb.Text, provenance(c), "", 1)}
	for _, blk := range auto.Blocks {
		texts["the unit's block "+blk.ID] = blk.Title + "\n" + blk.Text
	}
	for _, blk := range refresh.Blocks {
		texts["the refresh's block "+blk.ID] = blk.Title + "\n" + blk.Text
	}
	texts["the unit's block F"] = auto.Forward.Title + "\n" + auto.Forward.Text
	texts["the refresh's block F"] = refresh.Forward.Title + "\n" + refresh.Forward.Text
	for _, blk := range rb.Blocks {
		texts["the title of block "+blk.ID] = blk.Title
	}
	for name, text := range texts {
		for _, word := range []string{"Debian 11", "bullseye", "@from@", "@to@", "@tov@"} {
			if strings.Contains(text, word) {
				i := strings.Index(text, word)
				t.Errorf("%s says %q: %q", name, word, text[max(0, i-80):min(len(text), i+80)])
			}
		}
	}
	for _, want := range []string{
		"# Upgrade of the operating system by hand: Debian 12 (bookworm) to Debian 13 (trixie).",
		"write the sources of Debian 13, and read the plan again",
		"Debian 12 and 13 are mixed",
	} {
		if !strings.Contains(rb.Text, want) {
			t.Errorf("the runbook does not say %q", want)
		}
	}
	for _, blk := range auto.Blocks {
		if _, err := classify(blk.Text); err != nil {
			t.Errorf("the unit's block %s: %v", blk.ID, err)
		}
	}
}

// The text of the hop from Debian 11 to 12 is what it was: its own golden tests say it byte for byte, and this says that nothing of the words that stand for the releases is left in it.
func TestTheTextOf11To12HasNoWordLeftThatStandsForARelease(t *testing.T) {
	_, rb := realRunbook(t)
	_, auto := realAuto(t)
	refresh := refreshOf(t, false)
	text := rb.Text + auto.Forward.Text + refresh.Forward.Text
	for _, blk := range append(auto.Blocks, refresh.Blocks...) {
		text += blk.Title + blk.Text
	}
	for _, word := range []string{"@from@", "@to@", "@tov@"} {
		if strings.Contains(text, word) {
			t.Errorf("%q is left in the text", word)
		}
	}
}

// A hop row says the numbers of its releases: the texts name the releases by them, and a row without them would say "Debian " and nothing.
func TestEveryHopSaysTheNumbersOfItsReleases(t *testing.T) {
	for _, h := range planHops {
		if h.Distro == "" || h.FromVersion == "" || h.ToVersion == "" {
			t.Errorf("%s: Distro %q, FromVersion %q, ToVersion %q", h.ID, h.Distro, h.FromVersion, h.ToVersion)
		}
	}
}

// A file of the sources that has a word standing for a release is refused: the text would write it changed, and its hash would not be the plan's.
func TestASourceWithAWordThatStandsForAReleaseIsRefused(t *testing.T) {
	for _, word := range []string{"@from@", "@to@", "@tov@"} {
		if treeIsSafe([]TreeFile{{Path: "sources.list", Content: "# " + word + "\ndeb http://deb.debian.org/debian bookworm main\n"}}) {
			t.Errorf("a tree with %q is safe", word)
		}
	}
}

// withUbuntuTestHop is a hop of Ubuntu in the two tables for the time of a test: no hop of Ubuntu is planned yet, and the texts must already name it.
func withUbuntuTestHop(t *testing.T) HopSpec {
	t.Helper()
	hop := HopSpec{ID: osreadiness.HopUbuntu2404to2604, Distro: "Ubuntu", From: "noble", To: "resolute", FromVersion: "24.04", ToVersion: "26.04", Deb822: true}
	if _, ok := PlannedHop(hop.ID); ok {
		t.Fatal("the hop from Ubuntu 24.04 is planned already: this test hop is no longer needed")
	}
	rows := planHops
	planHops = append(slices.Clone(planHops), hop)
	runbookHops[hop.ID] = debianBlocks
	t.Cleanup(func() {
		planHops = rows
		delete(runbookHops, hop.ID)
	})
	return hop
}

func TestTheTextOfAnUbuntuHopNamesUbuntu(t *testing.T) {
	hop := withUbuntuTestHop(t)
	p := planFor(t, hop)
	rb := buildFor(t, p)
	c, _, err := newRbCtx(p)
	if err != nil {
		t.Fatal(err)
	}
	auto := autoOf(t, p, AutoParams{RunID: testRunID, Hostname: testHostname})
	box := d11Box(t)
	for i := range box {
		box[i].Content = []byte(strings.ReplaceAll(string(box[i].Content), "bullseye", "noble"))
	}
	rw, err := RewriteSources(context.Background(), hop.ForRefresh(), orderLikeTheBox(box), &fakeDecider{})
	if err != nil {
		t.Fatalf("the sources of the real box: %v", err)
	}
	refresh, err := BuildRefresh(RefreshInputs{Hop: hop.ForRefresh(), Rewritten: rw, RunID: testRunID, Hostname: testHostname})
	if err != nil {
		t.Fatalf("BuildRefresh: %v", err)
	}

	texts := map[string]string{"the runbook": strings.Replace(rb.Text, provenance(c), "", 1)}
	for _, blk := range auto.Blocks {
		texts["the unit's block "+blk.ID] = blk.Title + "\n" + blk.Text
	}
	for _, blk := range refresh.Blocks {
		texts["the refresh's block "+blk.ID] = blk.Title + "\n" + blk.Text
	}
	texts["the unit's block F"] = auto.Forward.Title + "\n" + auto.Forward.Text
	texts["the refresh's block F"] = refresh.Forward.Title + "\n" + refresh.Forward.Text
	for name, text := range texts {
		for _, word := range []string{"Debian", "linux/debian", "@from@", "@to@", "@tov@", "@distro@"} {
			if i := strings.Index(text, word); i >= 0 {
				t.Errorf("%s says %q: %q", name, word, text[max(0, i-80):min(len(text), i+80)])
			}
		}
	}
	for _, want := range []string{
		"# Upgrade of the operating system by hand: Ubuntu 24.04 (noble) to Ubuntu 26.04 (resolute).",
		"write the sources of Ubuntu 26.04, and read the plan again",
		"Ubuntu 24.04 and 26.04 are mixed",
		"https://download.docker.com/linux/ubuntu/dists/resolute/InRelease",
	} {
		if !strings.Contains(rb.Text, want) {
			t.Errorf("the runbook does not say %q", want)
		}
	}
	if prov := provenance(c); !strings.Contains(prov, "Canonical does not prescribe these commands") || strings.Contains(prov, "The release notes of Ubuntu") {
		t.Errorf("the provenance of an Ubuntu hop: %q", prov)
	}
	for _, blk := range auto.Blocks {
		if _, err := classify(blk.Text); err != nil {
			t.Errorf("the unit's block %s: %v", blk.ID, err)
		}
	}
}
