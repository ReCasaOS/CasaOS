package modulecmd

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// a server that every host name goes to: the client dials it whatever the address says, as a box with a proxy would, and the handler sees the host that was asked for
func testServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func clientTo(srv *httptest.Server) *http.Client {
	rt := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the test server's own certificate
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
	}
	return newHTTPClient(rt)
}

func get(t *testing.T, srv *httptest.Server, max int64, rawURL string) ([]byte, error) {
	t.Helper()
	var buf bytes.Buffer
	_, err := httpDownloader{client: clientTo(srv), max: max}.Get(context.Background(), rawURL, &buf)
	return buf.Bytes(), err
}

func permanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}

func TestTheDownloadFollowsAnArchiveToTheHostsOfGitHubAndNowhereElse(t *testing.T) {
	var seen []string
	loops := 0
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Host+r.URL.Path)
		switch r.URL.Path {
		case "/archive":
			http.Redirect(w, r, "https://objects.githubusercontent.com/blob", http.StatusFound)
		case "/blob":
			_, _ = io.WriteString(w, "the archive")
		case "/back":
			http.Redirect(w, r, "https://github.com/blob", http.StatusMovedPermanently)
		case "/evil":
			http.Redirect(w, r, "https://evil.example.com/blob", http.StatusFound)
		case "/lookalike":
			http.Redirect(w, r, "https://github.com.evil.example/blob", http.StatusFound)
		case "/suffix":
			http.Redirect(w, r, "https://evilgithub.com/blob", http.StatusFound)
		case "/http":
			http.Redirect(w, r, "http://objects.githubusercontent.com/blob", http.StatusFound)
		case "/port":
			http.Redirect(w, r, "https://github.com:8443/blob", http.StatusFound)
		case "/user":
			http.Redirect(w, r, "https://user:pw@github.com/blob", http.StatusFound)
		case "/ip":
			http.Redirect(w, r, "https://127.0.0.1/blob", http.StatusFound)
		case "/dot":
			http.Redirect(w, r, "https://github.com./blob", http.StatusFound)
		case "/other-scheme":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case "/loop":
			// (a server that stops redirecting after twenty, so that a client that does not stop is seen to go on)
			if loops++; loops > 20 {
				_, _ = io.WriteString(w, "the end of the loop")
				return
			}
			http.Redirect(w, r, "https://github.com/loop", http.StatusFound)
		}
	})
	if body, err := get(t, srv, 1<<10, "https://github.com/archive"); err != nil || string(body) != "the archive" {
		t.Errorf("the good redirect: %q, %v", body, err)
	}
	if body, err := get(t, srv, 1<<10, "https://github.com/back"); err != nil || string(body) != "the archive" {
		t.Errorf("from github to github: %q, %v", body, err)
	}
	if body, err := get(t, srv, 1<<10, "https://OBJECTS.githubusercontent.com/blob"); err != nil || string(body) != "the archive" {
		t.Errorf("the host in capitals: %q, %v", body, err)
	}
	for _, path := range []string{"evil", "lookalike", "suffix", "http", "port", "user", "ip", "dot", "other-scheme", "loop"} {
		seen = nil
		body, err := get(t, srv, 1<<10, "https://github.com/"+path)
		if err == nil || !permanent(err) || len(body) != 0 {
			t.Errorf("/%s: %q, %v", path, body, err)
		}
		for _, s := range seen {
			if strings.Contains(s, "/blob") && path != "loop" {
				t.Errorf("/%s: the redirect was followed: %v", path, seen)
			}
		}
		if path == "loop" && len(seen) > 6 {
			t.Errorf("a loop was followed %d times", len(seen))
		}
	}
}

func TestTheDownloadTakesOnlyHTTPSFromTheTwoHosts(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "x") })
	for _, bad := range []string{
		"http://github.com/a", "ftp://github.com/a", "file:///etc/passwd", "https://evil.example/a", "https://github.com.evil.example/a", "https://evilgithub.com/a", "https://github.com@evil.example/a",
		"https://user@github.com/a", "https://github.com:8443/a", "https://github.com:80/a", "https://github.com./a", "https://[::1]/a", "https://127.0.0.1/a", "//github.com/a", "github.com/a", "", "https:///a",
		"https://objects.githubusercontent.com.evil.example/a", "https://githubusercontent.com/a", "https://raw.githubusercontent.com/a",
	} {
		if body, err := get(t, srv, 1<<10, bad); err == nil || !permanent(err) || len(body) != 0 {
			t.Errorf("%q: %q, %v", bad, body, err)
		}
	}
	for _, good := range []string{"https://github.com/a", "https://objects.githubusercontent.com/a", "https://github.com:443/a", "https://GitHub.com/a"} {
		if _, err := get(t, srv, 1<<10, good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
	u, _ := url.Parse("https://github.com/")
	if checkURL(u) != nil {
		t.Error("github")
	}
}

func TestTheDownloadIsCappedAndSaysWhenItIsShortOrRefused(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/exact":
			_, _ = w.Write(bytes.Repeat([]byte("a"), 1000))
		case "/over-by-one":
			_, _ = w.Write(bytes.Repeat([]byte("a"), 1001))
		case "/declared":
			w.Header().Set("Content-Length", "5000")
			_, _ = w.Write(bytes.Repeat([]byte("a"), 5000))
		case "/chunked":
			fl := w.(http.Flusher)
			for range 20 {
				_, _ = w.Write(bytes.Repeat([]byte("a"), 500))
				fl.Flush()
			}
		case "/short":
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("only ten b"))
		case "/404":
			http.NotFound(w, r)
		case "/403":
			w.WriteHeader(http.StatusForbidden)
		case "/429":
			w.WriteHeader(http.StatusTooManyRequests)
		case "/500":
			w.WriteHeader(http.StatusInternalServerError)
		case "/503":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/204":
			w.WriteHeader(http.StatusNoContent)
		}
	})
	if body, err := get(t, srv, 1000, "https://github.com/exact"); err != nil || len(body) != 1000 {
		t.Errorf("exactly the cap: %d, %v", len(body), err)
	}
	for _, path := range []string{"over-by-one", "declared", "chunked"} {
		body, err := get(t, srv, 1000, "https://github.com/"+path)
		// one that says its size says it before a byte is read
		if !errors.Is(err, ErrTooLarge) || !permanent(err) || int64(len(body)) > 1001 || path == "declared" && len(body) != 0 {
			t.Errorf("/%s: %d bytes, %v", path, len(body), err)
		}
	}
	if _, err := get(t, srv, 1000, "https://github.com/short"); err == nil || permanent(err) {
		t.Errorf("a short body: %v", err)
	}
	for path, isPermanent := range map[string]bool{"404": true, "403": true, "429": false, "500": false, "503": false, "204": false} {
		if _, err := get(t, srv, 1000, "https://github.com/"+path); err == nil || permanent(err) != isPermanent {
			t.Errorf("/%s: %v (permanent %v)", path, err, permanent(err))
		}
	}
	// production takes the 200 MiB of the contract
	if d, ok := NewHTTPDownloader().(httpDownloader); !ok || d.max != 200<<20 {
		t.Errorf("the cap of production: %+v", d)
	}
}

func TestTheDownloadAsksForNothingOfTheMachine(t *testing.T) {
	var seen *http.Request
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) { seen = r; _, _ = io.WriteString(w, "x") })
	if _, err := get(t, srv, 10, "https://github.com/a"); err != nil {
		t.Fatal(err)
	}
	if seen.Method != http.MethodGet || seen.Header.Get("Authorization") != "" || seen.Header.Get("Cookie") != "" {
		t.Errorf("the request: %v %v", seen.Method, seen.Header)
	}
}

func gzipOf(t *testing.T, n int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bomb.gz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := gzip.NewWriter(f)
	chunk := make([]byte, 1<<20)
	for left := n; left > 0; left -= len(chunk) {
		if _, err := w.Write(chunk[:min(left, len(chunk))]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAGzipBombStopsAtTheCap(t *testing.T) {
	src := gzipOf(t, 5<<20) // a few kilobytes that are five megabytes
	if info, _ := os.Stat(src); info.Size() > 64<<10 {
		t.Fatalf("not a bomb: %d", info.Size())
	}
	dst := filepath.Join(t.TempDir(), "out")
	const cap = 1 << 20
	err := gunzipTo(context.Background(), src, dst, cap)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("%v", err)
	}
	if info, _ := os.Stat(dst); info.Size() > cap+1 {
		t.Errorf("%d bytes were written", info.Size())
	}
	// exactly the cap is fine, one more is not
	if err := gunzipTo(context.Background(), gzipOf(t, cap), dst, cap); err != nil {
		t.Errorf("exactly the cap: %v", err)
	}
	if err := gunzipTo(context.Background(), gzipOf(t, cap+1), dst, cap); !errors.Is(err, ErrTooLarge) {
		t.Errorf("one more: %v", err)
	}
	// not gzip, and gzip that is cut
	plain := filepath.Join(t.TempDir(), "plain")
	_ = os.WriteFile(plain, []byte("not gzip"), 0o644)
	if err := gunzipTo(context.Background(), plain, dst, cap); err == nil || errors.Is(err, ErrTooLarge) {
		t.Errorf("not gzip: %v", err)
	}
	raw, _ := os.ReadFile(gzipOf(t, 1<<20))
	cut := filepath.Join(t.TempDir(), "cut")
	_ = os.WriteFile(cut, raw[:len(raw)/2], 0o644)
	if err := gunzipTo(context.Background(), cut, dst, cap); err == nil {
		t.Error("a cut file is taken")
	}
	// the context ends it
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gunzipTo(ctx, src, dst, cap); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

// a bomb that goes through the whole install is stopped at the cap of the contract (MaxStreamBytes), and nothing is left of it
func TestAGzipBombInAnInstallIsStoppedAtTheCapOfTheContract(t *testing.T) {
	if testing.Short() {
		t.Skip("writes some 460 MiB")
	}
	f := newFixture(t)
	bomb := filepath.Join(t.TempDir(), "bomb.gz")
	raw, err := os.ReadFile(gzipOf(t, int(osmodule.MaxStreamBytes)+1))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bomb, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	f.setArchive(raw)
	before := f.treeWithoutBookkeeping()
	f.failsAt(StepUnpack, osmodule.UnpackLimits, keptAll, before)
}
