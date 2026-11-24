package feeds

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const rss = `<?xml version="1.0"?><rss version="2.0"><channel><title>ReCasaOS releases</title>
<item><title>v0.5.180 &amp; more</title><link>https://example.org/a</link></item>
<item><title>&lt;b&gt;Bold&lt;/b&gt;   news</title><link>/relative</link></item>
<item><title>Script</title><link>javascript:alert(1)</link></item>
<item><title></title><link>https://example.org/untitled</link></item>
<item><title>No link</title></item>
<item><title>With atom:link</title><link>https://example.org/page</link><atom:link xmlns:atom="http://www.w3.org/2005/Atom" href="https://example.org/feed" rel="self"/></item>
</channel></rss>`

const atom = `<?xml version="1.0" encoding="utf-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Blog</title>
<entry><title>First</title><link rel="self" href="https://example.org/self"/><link rel="alternate" href="https://example.org/first"/></entry>
<entry><title>Second</title><link href="second"/></entry></feed>`

const rdf = `<?xml version="1.0"?><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/">
<channel><title>Old style</title></channel><item><title>One</title><link>https://example.org/one</link></item></rdf:RDF>`

func TestParse(t *testing.T) {
	base, _ := url.Parse("https://feeds.example.org/path/feed.xml")
	cases := []struct {
		name, body string
		want       []Item
	}{
		{"rss", rss, []Item{
			{"v0.5.180 & more", "https://example.org/a", "ReCasaOS releases"},
			{"Bold news", "https://feeds.example.org/relative", "ReCasaOS releases"},
			{"With atom:link", "https://example.org/page", "ReCasaOS releases"},
		}},
		{"atom", atom, []Item{
			{"First", "https://example.org/first", "Blog"},
			{"Second", "https://feeds.example.org/path/second", "Blog"},
		}},
		{"rdf", rdf, []Item{{"One", "https://example.org/one", "Old style"}}},
		{"latin-1", "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><rss><channel><title>Caf\xe9</title><item><title>\xc9t\xe9</title><link>https://example.org/e</link></item></channel></rss>",
			[]Item{{"Été", "https://example.org/e", "Café"}}},
	}
	for _, c := range cases {
		got, err := parse(strings.NewReader(c.body), base)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s[%d]: got %+v, want %+v", c.name, i, got[i], c.want[i])
			}
		}
	}

	if _, err := parse(strings.NewReader("<html><body>not a feed</body></html>"), base); err == nil {
		t.Error("an HTML page parsed as a feed")
	}
}

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.216.34": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.10": false,
		"169.254.169.254": false, "100.64.0.1": false, "100.100.100.100": false, "0.0.0.0": false, "0.1.2.3": false,
		"::1": false, "fc00::1": false, "fe80::1": false, "::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false, "224.0.0.1": false,
	} {
		if got := public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("public(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestTheGuardedClientRefusesTheBoxsOwnNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(rss)) }))
	defer server.Close()
	localhost := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	for _, feed := range []string{server.URL, localhost} {
		_, err := fetch(guardedClient(), feed)
		if err == nil || !errors.Is(err, errRefused) {
			t.Errorf("fetch(%s) = %v, want refused", feed, err)
		}
		if !strings.HasPrefix(reason(err), "refused: ") {
			t.Errorf("reason(%v) = %q", err, reason(err))
		}
	}
}

func TestARedirectTowardARefusedAddressIsRefused(t *testing.T) {
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(rss)) }))
	defer inside.Close()
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, inside.URL, http.StatusFound) }))
	defer outside.Close()
	// outside stands for a public server, inside for the box's network
	insidePort := netip.MustParseAddrPort(strings.TrimPrefix(inside.URL, "http://")).Port()
	client := clientAllowing(func(ap netip.AddrPort) bool { return ap.Port() != insidePort })

	if _, err := fetch(client, inside.URL); !errors.Is(err, errRefused) {
		t.Fatalf("the refused server itself: %v", err)
	}
	if _, err := fetch(client, outside.URL); !errors.Is(err, errRefused) {
		t.Fatalf("redirected toward the refused server: %v, want refused", err)
	}
}

// hub is a Hub under a temporary root whose client reaches the test server (the
// guard is tested on its own above).
func hub(t *testing.T) *Hub {
	h := New(t.TempDir())
	h.Client = &http.Client{Timeout: 5 * time.Second}
	return h
}

func TestNothingIsFetchedUntilTurnedOn(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); _, _ = w.Write([]byte(rss)) }))
	defer server.Close()
	h := hub(t)

	if s := h.Status(); s.Enabled || len(s.Feeds) != 0 || s.Items == nil {
		t.Fatalf("fresh status = %+v", s)
	}
	if _, err := h.Update(Config{Enabled: false, Feeds: []string{server.URL}}); err != nil {
		t.Fatal(err)
	}
	h.Status()
	h.refreshed.Wait()
	if hits.Load() != 0 {
		t.Fatalf("off, the feed was fetched %d times", hits.Load())
	}
}

func TestHeadlinesAreCachedAndAFailedFeedKeepsItsOwn(t *testing.T) {
	var fail atomic.Bool
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(atom))
	}))
	defer server.Close()
	h := hub(t)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	h.Now = func() time.Time { return now }

	s, err := h.Update(Config{Enabled: true, Feeds: []string{" " + server.URL + " ", server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Feeds) != 1 || len(s.Items) != 0 {
		t.Fatalf("after the update: %+v (one feed, nothing cached yet)", s)
	}
	h.refreshed.Wait()
	if s = h.Status(); len(s.Items) != 2 || s.Items[0].Title != "First" {
		t.Fatalf("after the refresh: %+v", s.Items)
	}
	h.refreshed.Wait()
	if hits.Load() != 1 {
		t.Fatalf("fresh cache fetched again: %d", hits.Load())
	}

	fail.Store(true)
	now = now.Add(refreshEvery)
	h.Status()
	h.refreshed.Wait()
	if hits.Load() != 2 {
		t.Fatalf("stale cache not refreshed: %d", hits.Load())
	}
	if s = h.Status(); len(s.Items) != 2 {
		t.Fatalf("a failed refresh lost the headlines: %+v", s.Items)
	}
	if got := s.Errors[server.URL]; got != "answered 502" {
		t.Fatalf("the failure says %q, want answered 502", got)
	}

	// a private feed's URL can carry its token
	if info, err := os.Stat(filepath.Join(h.Root, ConfigFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("feeds.json: %v %v, want 0600", info, err)
	}
}

func TestUpdateRefusesWhatIsNotAFeed(t *testing.T) {
	h := hub(t)
	many := make([]string, maxFeeds+1)
	for i := range many {
		many[i] = "https://example.org/" + strings.Repeat("x", i+1)
	}
	for _, feeds := range [][]string{
		{"ftp://example.org/feed"}, {"javascript:alert(1)"}, {"example.org/feed"}, {""}, {"https://"},
		{"https://example.org/" + strings.Repeat("a", maxURLLength)}, many,
	} {
		if _, err := h.Update(Config{Enabled: true, Feeds: feeds}); !errors.Is(err, ErrInvalid) {
			t.Errorf("Update(%.60q) = %v, want ErrInvalid", feeds, err)
		}
	}
	if _, err := os.Stat(filepath.Join(h.Root, ConfigFile)); !os.IsNotExist(err) {
		t.Error("a refused update wrote feeds.json")
	}
}
