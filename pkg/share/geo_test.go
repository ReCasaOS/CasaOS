package share

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func gz(t *testing.T, body string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(body))
	w.Close()
	return b.Bytes()
}

const mmdb = "data\xab\xcd\xefMaxMind.com{meta}"

func newFetcher(t *testing.T, h http.Handler) (*GeoFetcher, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.Add(1); h.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)
	return &GeoFetcher{Dir: filepath.Join(t.TempDir(), "geo"), BaseURL: srv.URL,
		Now: func() time.Time { return time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC) }}, &n
}

func serve(t *testing.T, body []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
}

func TestUpdateWritesBothFiles(t *testing.T) {
	f, _ := newFetcher(t, serve(t, gz(t, mmdb)))
	if err := f.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := f.Status()
	if !st.Country || !st.ASN || st.Error != "" || st.UpdatedAt.IsZero() {
		t.Fatalf("status %+v", st)
	}
	fi, _ := os.Stat(filepath.Join(f.Dir, "dbip-asn-lite.mmdb"))
	if fi.Mode().Perm()&0o644 != 0o644 && os.PathSeparator == '/' {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestPreviousMonthFallback(t *testing.T) {
	var urls []string
	f, _ := newFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urls = append(urls, r.URL.Path)
		if r.Header.Get("User-Agent") == "" {
			t.Error("no User-Agent")
		}
		if r.URL.Path == "/dbip-country-lite-2026-03.mmdb.gz" || r.URL.Path == "/dbip-asn-lite-2026-03.mmdb.gz" {
			http.NotFound(w, r)
			return
		}
		w.Write(gz(t, mmdb))
	}))
	if err := f.Update(context.Background()); err != nil {
		t.Fatal(urls, err)
	}
	if st := f.Status(); !st.Country || !st.ASN {
		t.Fatalf("%+v %v", st, urls)
	}
	if !slices.Contains(urls, "/dbip-country-lite-2026-02.mmdb.gz") || !slices.Contains(urls, "/dbip-asn-lite-2026-02.mmdb.gz") {
		t.Fatalf("previous month not fetched: %v", urls)
	}
}

func TestPreviousMonthInJanuary(t *testing.T) {
	var urls []string
	f, _ := newFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urls = append(urls, r.URL.Path)
		if strings.Contains(r.URL.Path, "-2027-01") {
			http.NotFound(w, r)
			return
		}
		w.Write(gz(t, mmdb))
	}))
	f.Now = func() time.Time { return time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC) }
	if err := f.Update(context.Background()); err != nil || !slices.Contains(urls, "/dbip-asn-lite-2026-12.mmdb.gz") {
		t.Fatal(err, urls)
	}
}

func TestFreshEditionIsSkipped(t *testing.T) {
	f, n := newFetcher(t, serve(t, gz(t, mmdb)))
	f.Now = time.Now
	os.MkdirAll(f.Dir, 0o755)
	os.WriteFile(filepath.Join(f.Dir, "dbip-country-lite.mmdb"), []byte("fresh"), 0o644)
	if err := f.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.Dir, "dbip-country-lite.mmdb")); string(b) != "fresh" || n.Load() != 1 {
		t.Fatalf("country %q, %d requests", b, n.Load())
	}
}

func TestProtectionOnWakesTheLoop(t *testing.T) {
	s := New(t.TempDir())
	f, _ := newFetcher(t, serve(t, gz(t, mmdb)))
	f.Now = time.Now
	s.Geo = f
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.geoLoop(ctx)
	time.Sleep(100 * time.Millisecond) // the first look, protection off
	s.SetProtection(ctx, true)         // the push to a missing Gateway may fail; the wake comes first
	for i := 0; i < 100 && !f.Status().ASN; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if st := f.Status(); !st.Country || !st.ASN {
		t.Fatalf("%+v", st)
	}
}

func TestCutDownloadKeepsOldFile(t *testing.T) {
	full := gz(t, mmdb+string(bytes.Repeat([]byte("x"), 4096)))
	f, _ := newFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		w.Write(full[:len(full)/2])
	}))
	os.MkdirAll(f.Dir, 0o755)
	old := filepath.Join(f.Dir, "dbip-country-lite.mmdb")
	os.WriteFile(old, []byte("old"), 0o644)
	if err := f.Update(context.Background()); err == nil {
		t.Fatal("want an error")
	}
	if b, _ := os.ReadFile(old); string(b) != "old" {
		t.Fatalf("old file now %q", b)
	}
	if f.Status().Error == "" {
		t.Fatal("no error kept")
	}
	if m, _ := filepath.Glob(filepath.Join(f.Dir, "*.tmp")); len(m) != 0 {
		t.Fatalf("left %v", m)
	}
}

func TestNotAnMMDB(t *testing.T) {
	f, _ := newFetcher(t, serve(t, gz(t, "<html>hello</html>")))
	os.MkdirAll(f.Dir, 0o755)
	old := filepath.Join(f.Dir, "dbip-asn-lite.mmdb")
	os.WriteFile(old, []byte("old"), 0o644)
	if err := f.Update(context.Background()); err == nil {
		t.Fatal("want an error")
	}
	if b, _ := os.ReadFile(old); string(b) != "old" {
		t.Fatalf("old file now %q", b)
	}
	if m, _ := filepath.Glob(filepath.Join(f.Dir, "*.tmp")); len(m) != 0 {
		t.Fatalf("left %v", m)
	}
}

func TestGeoStepTimings(t *testing.T) {
	s := New(t.TempDir())
	f, n := newFetcher(t, serve(t, gz(t, mmdb)))
	now := time.Now() // the files' mtimes are real
	s.Now = func() time.Time { return now }
	f.Now = s.now
	s.Geo = f
	ctx := context.Background()

	if w := s.geoStep(ctx); w != geoCheck || n.Load() != 0 {
		t.Fatalf("protection off: wait %v, %d requests", w, n.Load())
	}
	s.mu.Lock()
	g, _ := s.guardLocked()
	g.Protection = true
	s.mu.Unlock()
	if w := s.geoStep(ctx); w != geoCheck || !f.Status().Country {
		t.Fatalf("wait %v, status %+v", w, f.Status())
	}
	before := n.Load()
	s.geoStep(ctx)
	if n.Load() != before {
		t.Fatal("fresh files downloaded again")
	}
	now = now.Add(33 * 24 * time.Hour)
	s.geoStep(ctx)
	if n.Load() == before {
		t.Fatal("stale files not refreshed")
	}
	// a failing server: retry in an hour
	f.BaseURL = "http://127.0.0.1:1"
	now = now.Add(40 * 24 * time.Hour)
	if w := s.geoStep(ctx); w != geoRetry {
		t.Fatalf("wait %v", w)
	}
}

func TestNoDownloadWhileProtectionOff(t *testing.T) {
	s := New(t.TempDir())
	f, n := newFetcher(t, serve(t, gz(t, mmdb)))
	s.Geo = f
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	s.geoLoop(ctx)
	if n.Load() != 0 {
		t.Fatalf("%d requests", n.Load())
	}
}
