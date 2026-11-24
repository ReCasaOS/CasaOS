package filesearch

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// a tree: Media/Movies/Holiday.mkv, Media/holiday-notes.txt, Docs/report.pdf, a .temp folder,
// and a link to a folder outside the tree
func tree(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root, outside = filepath.Join(base, "data"), filepath.Join(base, "outside")
	for _, dir := range []string{"Media/Movies", "Docs", ".temp/holiday-upload"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"Media/Movies/Holiday.mkv", "Media/holiday-notes.txt", "Docs/report.pdf"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("12345"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "holiday-secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "Media", "elsewhere")); err != nil {
		t.Fatal(err)
	}
	return root, outside
}

func names(r Result) []string {
	out := []string{}
	for _, h := range r.Hits {
		out = append(out, h.Name)
	}
	sort.Strings(out)
	return out
}

func TestSearchFindsNamesCaseNotMinded(t *testing.T) {
	root, _ := tree(t)

	r := Search(context.Background(), []string{root}, " HOLIDAY ", DefaultLimits)

	got := names(r)
	if len(got) != 2 || got[0] != "Holiday.mkv" || got[1] != "holiday-notes.txt" || r.Truncated {
		t.Fatalf("hits %v truncated %v, want Holiday.mkv and holiday-notes.txt, whole", got, r.Truncated)
	}
	for _, h := range r.Hits {
		if h.Name == "Holiday.mkv" && (h.Size != 5 || h.IsDir || h.Date.IsZero() || h.Path != filepath.Join(root, "Media/Movies/Holiday.mkv")) {
			t.Errorf("hit %+v", h)
		}
	}
}

func TestSearchDoesNotWalkThroughALinkNorIntoTemp(t *testing.T) {
	root, _ := tree(t)

	got := names(Search(context.Background(), []string{root}, "secret", DefaultLimits))
	if len(got) != 0 {
		t.Fatalf("found %v through a link out of the tree", got)
	}
	// the link itself is a name in the tree
	if got := names(Search(context.Background(), []string{root}, "elsewhere", DefaultLimits)); len(got) != 1 {
		t.Fatalf("the link's own name: %v", got)
	}
	if got := names(Search(context.Background(), []string{root}, "upload", DefaultLimits)); len(got) != 0 {
		t.Fatalf("found %v in .temp", got)
	}
}

func TestSearchStopsAtItsLimitsAndSaysSo(t *testing.T) {
	root, _ := tree(t)

	r := Search(context.Background(), []string{root}, "o", Limits{Hits: 1, Entries: 1000, Time: time.Minute})
	if len(r.Hits) != 1 || !r.Truncated {
		t.Fatalf("hits limit: %d hits, truncated %v", len(r.Hits), r.Truncated)
	}
	r = Search(context.Background(), []string{root}, "report", Limits{Hits: 100, Entries: 2, Time: time.Minute})
	if !r.Truncated {
		t.Fatal("entries limit: not truncated")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r = Search(ctx, []string{root}, "report", DefaultLimits); !r.Truncated || len(r.Hits) != 0 {
		t.Fatalf("ended context: %+v", r)
	}
}

func TestSearchOfNothingFindsNothing(t *testing.T) {
	root, _ := tree(t)
	if r := Search(context.Background(), []string{root}, "   ", DefaultLimits); len(r.Hits) != 0 || r.Truncated {
		t.Fatalf("%+v", r)
	}
	if r := Search(context.Background(), []string{filepath.Join(root, "missing")}, "x", DefaultLimits); len(r.Hits) != 0 {
		t.Fatalf("%+v", r)
	}
}
