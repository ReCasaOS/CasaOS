package osplan

import (
	"context"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// d11 is a text of the blocks as the hop from Debian 11 to 12 writes it: the words that stand for the releases replaced by its own.
func d11(s string) string { return (&rbCtx{hop: planHops[0]}).names(s) }

// restoreScriptHash is the hash of the owner's restore script, as the runbook of the hop from Debian 11 to 12 writes it.
func restoreScriptHash() string { return restoreScriptHashOf(d11(restoreScript)) }

// withTestHop puts a second Debian hop in the two tables, with the blocks of the first, for the time of a test. No such row is in the tables yet: the tests of the text and of the
// folders need one to look at.
func withTestHop(t *testing.T) HopSpec {
	t.Helper()
	hop := HopSpec{ID: osreadiness.HopDebian12to13, From: "bookworm", To: "trixie", FromVersion: "12", ToVersion: "13", Later: []string{"forky", "duke"}, Firmware: true}
	plan := planHops
	planHops = append(append([]HopSpec(nil), planHops...), hop)
	runbookHops[hop.ID] = debianBlocks
	t.Cleanup(func() {
		planHops = plan
		delete(runbookHops, hop.ID)
	})
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
	text := rb.Text
	for _, blk := range auto.Blocks {
		text += blk.Title + blk.Text
	}
	for _, word := range []string{"@from@", "@to@", "@tov@"} {
		if strings.Contains(text, word) {
			t.Errorf("%q is left in the text", word)
		}
	}
}
