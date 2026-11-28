package apiwidgets

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testSecret = "s3cr3t-Zq9"
	testPath   = "/hidden-path/v1"
	refusedMsg = "refused: this name leads to the box's loopback or a link-local address; use the box's address on the network"
)

func anywhere(netip.AddrPort) bool { return true }

func TestReachable(t *testing.T) {
	for _, s := range []string{"8.8.8.8", "10.0.0.1", "172.18.0.3", "192.168.1.2", "100.100.1.1", "fd00::1", "2001:4860::1"} {
		if !reachable(netip.MustParseAddr(s)) {
			t.Errorf("%s must be reachable", s)
		}
	}
	for _, s := range []string{
		"127.0.0.1", "127.0.1.1", "::1", "::ffff:127.0.0.1", "0.0.0.0", "0.1.2.3", "::",
		"169.254.169.254", "fe80::1%eth0", "224.0.0.1", "255.255.255.255", "ff02::1",
		"::ffff:255.255.255.255", "::ffff:169.254.169.254", "::ffff:0.0.0.0", "::ffff:0.1.2.3",
	} {
		if reachable(netip.MustParseAddr(s)) {
			t.Errorf("%s must be refused", s)
		}
	}
	if reachable(netip.Addr{}) {
		t.Error("the zero address must be refused")
	}
}

func TestRequestURL(t *testing.T) {
	tests := []struct{ base, path, want string }{
		{"http://h:1/api/", "/x", "http://h:1/api/x"},
		{"http://h:1", "/x", "http://h:1/x"},
		{"https://h/", "/x", "https://h/x"},
		{"http://h/a%20b", "/c%2Fd", "http://h/a%20b/c%2Fd"},
		{"http://[fd00::1]:8080", "/x", "http://[fd00::1]:8080/x"},
	}
	for _, tt := range tests {
		u, err := requestURL(tt.base, Recipe{Path: tt.path}, nil)
		if err != nil || u.String() != tt.want {
			t.Errorf("%s + %s = %v, %v; want %s", tt.base, tt.path, u, err, tt.want)
		}
	}
}

func TestRequestURLQuery(t *testing.T) {
	r := Recipe{Path: "/x", Query: map[string]Value{
		"a b":  {Text: "1&z=2 #é"},
		"key":  {Input: "k"},
		"plus": {Text: "a+b"},
	}}
	u, err := requestURL("http://h", r, map[string]string{"k": "p&q=r/?"})
	if err != nil {
		t.Fatal(err)
	}
	want := url.Values{"a b": {"1&z=2 #é"}, "key": {"p&q=r/?"}, "plus": {"a+b"}}
	if got := u.Query(); len(got) != 3 || got.Encode() != want.Encode() {
		t.Errorf("query %q, want %q", u.RawQuery, want.Encode())
	}
	if strings.ContainsAny(u.RawQuery, " #?/") {
		t.Errorf("query %q is not encoded", u.RawQuery)
	}

	_, err = requestURL("http://h", r, map[string]string{"other": "v"})
	if err == nil || !strings.Contains(reason(err), `"k"`) {
		t.Errorf("a missing secret must be an error naming the input, got %v", err)
	}
}

func TestRequestURLRefused(t *testing.T) {
	for _, base := range []string{"http://localhost:80", "http://a.localhost", "http://LOCALHOST", "https://localhost./x", "http://a.b.Localhost.:8080"} {
		_, err := requestURL(base, Recipe{Path: "/x"}, nil)
		if err == nil || !strings.Contains(reason(err), "use the box's address on the network") {
			t.Errorf("%s: got %v", base, err)
		}
	}
	for _, base := range []string{
		"", "h:1", "//h", "/x", "ftp://h", "file:///etc/passwd", "gopher://h", "http:h", "http://", "http://:80",
		"http://u@h", "http://u:p@h", "http://h?a=1", "http://h?", "http://h#f", "http://h/#f",
		"http://h//", "http://h/a//b", "http://h/a/../b", "http://h/a/%2e%2e/b", "http://h/%zz", "http://h/a%3Fb",
		"http://h/a\\b", "http://h\n", " http://h",
	} {
		u, err := requestURL(base, Recipe{Path: "/x"}, nil)
		if err == nil {
			t.Errorf("%q must be refused, got %s", base, u)
			continue
		}
		if msg := reason(err); strings.Contains(msg, "zz") || strings.Contains(msg, "u:p") {
			t.Errorf("%q: the reason %q quotes the address", base, msg)
		}
	}
	for _, path := range []string{"", "x", "//x", "/../x", "/x?a=1", "/x#f", "/%2e%2e/x"} {
		if u, err := requestURL("http://h/api", Recipe{Path: path}, nil); err == nil {
			t.Errorf("path %q must be refused, got %s", path, u)
		}
	}
}

// A path that Go would write another way is refused: "%2F" beside a character
// left unescaped would be sent as "/".
func TestRequestURLKeepsThePathAsWritten(t *testing.T) {
	for _, path := range []string{"/é%2Fx", "/a b%2Fc", "/é", "/a b", "/a%2Fb^"} {
		u, err := requestURL("http://h/api", Recipe{Path: path}, nil)
		// a failed read is not the fault of who asks for the card
		if err == nil || errors.Is(err, ErrInvalid) || !strings.Contains(reason(err), "percent-encoded") {
			t.Errorf("path %q must be refused, got %v, %v", path, u, err)
		}
		_, _, err = fetch(context.Background(), newClient(false, anywhere), "http://192.0.2.1", Recipe{Path: path}, nil)
		if err == nil || errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "percent-encoded") {
			t.Errorf("path %q: a read gave %v", path, err)
		}
	}
	for _, path := range []string{"/a%2Fb", "/%C3%A9%2Fx", "/a%20b", "/a:b@c;d=e,f+g!$&'()*~._-"} {
		u, err := requestURL("http://h/api", Recipe{Path: path}, nil)
		if err != nil || u.EscapedPath() != "/api"+path || !strings.HasSuffix(u.String(), "/api"+path) {
			t.Errorf("path %q: got %v, %v", path, u, err)
		}
	}
}

// served starts a plain server and counts what it receives.
func served(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// discreet checks that neither the reason nor the error itself tells the host, the
// path or a secret.
func discreet(t *testing.T, err error, base string) {
	t.Helper()
	if err == nil {
		t.Fatal("an error was expected")
	}
	hidden := []string{testSecret, testPath, "hidden-path"}
	if u, perr := url.Parse(base); perr == nil && u.Host != "" {
		hidden = append(hidden, u.Host, u.Hostname(), u.Port())
	}
	msg := reason(err)
	if len(msg) == 0 || len(msg) > 200 {
		t.Errorf("reason of %d characters: %q", len(msg), msg)
	}
	for _, h := range hidden {
		if h == "" {
			continue
		}
		if strings.Contains(msg, h) {
			t.Errorf("reason %q tells %q", msg, h)
		}
		if strings.Contains(err.Error(), h) {
			t.Errorf("error %q tells %q", err, h)
		}
	}
}

func secretRecipe() Recipe {
	return Recipe{
		Path:    testPath,
		Query:   map[string]Value{"api_key": {Input: "k"}, "fixed": {Text: "a b"}},
		Headers: map[string]Value{"X-Api-Key": {Input: "k"}, "X-Fixed": {Text: "yes"}},
	}
}

var secrets = map[string]string{"k": testSecret, "pw": "p:w " + testSecret}

func TestFetchSendsSecrets(t *testing.T) {
	var got *http.Request
	srv, _ := served(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		w.Write([]byte(`{"n": 12345678901234567890, "s": "x"}` + "\n"))
	})
	r := secretRecipe()
	r.Basic = &Basic{User: Value{Text: "admin"}, Password: Value{Input: "pw"}}

	raw, doc, err := fetch(context.Background(), newClient(false, anywhere), srv.URL+"/", r, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"n": 12345678901234567890, "s": "x"}`+"\n" {
		t.Errorf("raw = %q", raw)
	}
	if n := doc.(map[string]any)["n"]; n != json.Number("12345678901234567890") {
		t.Errorf("n = %#v, want a json.Number", n)
	}
	if got.Method != http.MethodGet || got.URL.Path != testPath {
		t.Errorf("%s %s", got.Method, got.URL.Path)
	}
	if q := got.URL.Query(); q.Get("api_key") != testSecret || q.Get("fixed") != "a b" {
		t.Errorf("query = %q", got.URL.RawQuery)
	}
	if got.Header.Get("X-Api-Key") != testSecret || got.Header.Get("X-Fixed") != "yes" {
		t.Errorf("headers = %v", got.Header)
	}
	if u, p, ok := got.BasicAuth(); !ok || u != "admin" || p != secrets["pw"] {
		t.Errorf("basic = %q %q %v", u, p, ok)
	}
	if got.Header.Get("User-Agent") != "recasaos-widget/1" || got.Header.Get("Accept") != "application/json" {
		t.Errorf("headers = %v", got.Header)
	}
}

func TestFetchRealGuardRefusesLoopback(t *testing.T) {
	srv, hits := served(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	guarded := newClient(false, guard)
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	// 127.0.1.1 is what the box's own name resolves to on Debian
	for _, host := range []string{"127.0.0.1", "127.0.1.1", "[::1]", "[::ffff:127.0.0.1]", "0.0.0.0", "[::]"} {
		base := "http://" + net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
		_, _, err := fetch(context.Background(), guarded, base, secretRecipe(), secrets)
		if !errors.Is(err, errRefused) || reason(err) != refusedMsg {
			t.Errorf("%s: got %v (%q)", base, err, reason(err))
		}
		discreet(t, err, base)
	}

	// a name is judged by the address it resolves to; requestURL would stop this
	// one earlier, so the client is asked directly
	resp, err := guarded.Get("http://localhost:" + strconv.Itoa(port) + "/")
	if err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, errRefused) || reason(err) != refusedMsg {
		t.Errorf("a name that resolves to the loopback: got %v (%q)", err, reason(err))
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the loopback server received %d requests", n)
	}
}

func TestFetchFollowsNoRedirect(t *testing.T) {
	target, targetHits := served(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	for _, code := range []int{301, 302, 303, 307, 308} {
		origin, hits := served(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/elsewhere", code)
		})
		// the guard lets the target through: only the client's rule stops the redirect
		_, _, err := fetch(context.Background(), newClient(false, anywhere), origin.URL, secretRecipe(), secrets)
		discreet(t, err, origin.URL)
		if !strings.Contains(reason(err), strconv.Itoa(code)) {
			t.Errorf("%d: reason %q", code, reason(err))
		}
		if hits.Load() != 1 {
			t.Errorf("%d: the origin received %d requests", code, hits.Load())
		}
	}
	if n := targetHits.Load(); n != 0 {
		t.Errorf("the target of the redirects received %d requests", n)
	}
}

func TestFetchRefusedAnswers(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"500", 500, `{"error": "` + testSecret + `"}`, "500"},
		{"404", 404, `{}`, "404"},
		{"not json", 200, "<html>" + testSecret + "</html>", "not JSON"},
		{"empty", 200, "", "not JSON"},
		{"truncated", 200, `{"a": `, "not JSON"},
		{"then text", 200, `{} x`, "after"},
		{"then a value", 200, `{}{}`, "after"},
		{"then a secret", 200, `[1] "` + testSecret + `"`, "after"},
	}
	for _, tt := range tests {
		srv, _ := served(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tt.status)
			w.Write([]byte(tt.body))
		})
		raw, doc, err := fetch(context.Background(), newClient(false, anywhere), srv.URL, secretRecipe(), secrets)
		if raw != nil || doc != nil {
			t.Errorf("%s: an error must come alone, got %q", tt.name, raw)
		}
		discreet(t, err, srv.URL)
		if !strings.Contains(reason(err), tt.want) {
			t.Errorf("%s: reason %q, want %q in it", tt.name, reason(err), tt.want)
		}
	}
}

func TestFetchSizeLimit(t *testing.T) {
	for _, size := range []int{256 << 10, 256<<10 + 1} {
		body := `"` + strings.Repeat("a", size-2) + `"`
		srv, _ := served(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) })
		raw, _, err := fetch(context.Background(), newClient(false, anywhere), srv.URL, secretRecipe(), secrets)
		if size == 256<<10 {
			if err != nil || len(raw) != size {
				t.Errorf("256 KiB must be read, got %d bytes, %v", len(raw), err)
			}
			continue
		}
		discreet(t, err, srv.URL)
		if !strings.Contains(reason(err), "256 KiB") {
			t.Errorf("reason %q", reason(err))
		}
	}
}

func TestFetchSelfSigned(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"ok": true}`))
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshake is the test
	srv.StartTLS()
	defer srv.Close()

	_, _, err := fetch(context.Background(), newClient(false, anywhere), srv.URL, secretRecipe(), secrets)
	discreet(t, err, srv.URL)
	if reason(err) != "the certificate is not trusted" || hits.Load() != 0 {
		t.Errorf("reason %q, %d requests", reason(err), hits.Load())
	}
	if _, doc, err := fetch(context.Background(), newClient(true, anywhere), srv.URL, secretRecipe(), secrets); err != nil || doc == nil {
		t.Errorf("with insecureTLS: %v", err)
	}
}

func TestFetchRefusesBrokenHeaderValues(t *testing.T) {
	srv, hits := served(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	client := newClient(false, anywhere)
	for _, bad := range []string{"a\r\nX-Evil: 1", "a\nb", "a\rb", "a\x00b"} {
		_, _, err := fetch(context.Background(), client, srv.URL, secretRecipe(), map[string]string{"k": testSecret + bad})
		discreet(t, err, srv.URL)
		// refused here, before Go's own check, whose error would name the header
		if reason(err) != "a header value holds a line break or a NUL character" {
			t.Errorf("reason %q", reason(err))
		}

		fixed := Recipe{Path: "/x", Headers: map[string]Value{"X-Fixed": {Text: bad}}}
		if _, _, err := fetch(context.Background(), client, srv.URL, fixed, nil); err == nil || reason(err) != "a header value holds a line break or a NUL character" {
			t.Errorf("a fixed header value %q must be refused, got %v", bad, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

func TestFetchMissingSecret(t *testing.T) {
	srv, hits := served(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	client := newClient(false, anywhere)
	recipes := []Recipe{
		{Path: "/x", Headers: map[string]Value{"X-Api-Key": {Input: "k"}}},
		{Path: "/x", Query: map[string]Value{"key": {Input: "k"}}},
		{Path: "/x", Basic: &Basic{User: Value{Input: "k"}, Password: Value{Input: "pw"}}},
		{Path: "/x", Basic: &Basic{User: Value{Text: "u"}, Password: Value{Input: "k"}}},
	}
	for i, r := range recipes {
		if _, _, err := fetch(context.Background(), client, srv.URL, r, map[string]string{"pw": "x"}); err == nil {
			t.Errorf("recipe %d: a missing secret must be an error", i)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

func TestFetchUnreachable(t *testing.T) {
	// a port nobody listens on
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + l.Addr().String()
	l.Close()
	_, _, err = fetch(context.Background(), newClient(false, anywhere), base, secretRecipe(), secrets)
	discreet(t, err, base)
	if reason(err) != "connection refused" {
		t.Errorf("reason %q", reason(err))
	}
}

func TestFetchTimeout(t *testing.T) {
	release := make(chan struct{})
	srv, _ := served(t, func(w http.ResponseWriter, r *http.Request) { <-release })
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _, err := fetch(ctx, newClient(false, anywhere), srv.URL, secretRecipe(), secrets)
	discreet(t, err, srv.URL)
	if reason(err) != "no answer in time" {
		t.Errorf("reason %q", reason(err))
	}
}

func TestClientRules(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://192.0.2.1:3128")
	t.Setenv("HTTPS_PROXY", "http://192.0.2.1:3128")
	c := newClient(false, guard)
	tr := c.Transport.(*http.Transport)
	if tr.Proxy != nil {
		t.Error("no proxy may be used")
	}
	if c.Timeout != 5*time.Second {
		t.Errorf("timeout = %s", c.Timeout)
	}
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("certificates must be verified unless asked otherwise")
	}
	if !newClient(true, guard).Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Error("insecureTLS must skip the verification")
	}
}

func TestReason(t *testing.T) {
	if reason(nil) != "" {
		t.Error("no error has no reason")
	}
	// an error from elsewhere is never quoted: it may name the address
	foreign := &url.Error{Op: "Get", URL: "http://box.lan" + testPath + "?k=" + testSecret,
		Err: &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(192, 168, 1, 9), Port: 81}, Err: errors.New("box.lan is odd " + testSecret)}}
	if msg := reason(foreign); strings.Contains(msg, "box.lan") || strings.Contains(msg, testSecret) || strings.Contains(msg, "192.168") {
		t.Errorf("reason %q quotes a foreign error", msg)
	}
	dns := &net.DNSError{Err: "no such host", Name: "box.lan", Server: "192.168.1.1:53", IsNotFound: true}
	if msg := reason(&url.Error{Op: "Get", URL: "http://box.lan/", Err: &net.OpError{Op: "dial", Err: dns}}); msg != "the name was not found" {
		t.Errorf("reason %q", msg)
	}
	if msg := reason(failure(strings.Repeat("x", 500))); len(msg) != 200 {
		t.Errorf("a reason of %d characters", len(msg))
	}
}
