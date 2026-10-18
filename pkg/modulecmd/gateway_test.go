package modulecmd

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/model"
)

// ---- a gateway made of the code of the gateway -------------------------------------------------------------------------------------------------------------------------------
//
// ReCasaOS/CasaOS-Gateway keeps a table of routes (a path and a target), longest path first, and the dashboard's files are one of them, at "/", so that every path that nobody registered
// is a request for a file: the answer is the static server's 404. The public port answers /ping itself and proxies the rest (a target that is down is a 502 from the reverse proxy); the
// table is read on the management listener, on loopback at a port the kernel gave, GET /v1/gateway/routes, for whoever is a person with a token or a service with the secret of the boot;
// the address of the management listener and the secret are two files in the runtime path, 0600. The public port is served with TLS if the configuration has a certificate and a key, binds
// the configured address (all of them when there is none), and its port is in /etc/casaos/gateway.ini. Everything here is that, in the same words (service/management.go, route/*.go).

type fakeGatewayBox struct {
	t        *testing.T
	root     string
	roots    Roots
	secret   string
	public   *httptest.Server
	mgmt     *httptest.Server
	mu       sync.Mutex
	table    map[string]string
	proxies  map[string]*httputil.ReverseProxy
	publicAt []string // the Authorization headers the public port was sent
	sawTLS   bool     // a request got through to the proxy over TLS
	tls      bool
	// secretDir is where the management listener looks for the secret of the boot: the runtime path, unless a test has the boot go on with another
	secretDir string
}

type gwOpt func(*fakeGatewayBox, *gwCfg)

type gwCfg struct {
	address  string // what gateway.ini says; "" is none
	listenOn string // where the public port really listens
	tls      bool
	runtime  string
}

func withAddress(configured, listensOn string) gwOpt {
	return func(_ *fakeGatewayBox, c *gwCfg) { c.address, c.listenOn = configured, listensOn }
}
func withTLS() gwOpt { return func(_ *fakeGatewayBox, c *gwCfg) { c.tls = true } }

func (g *fakeGatewayBox) path(abs string) string { return g.roots.Path(abs) }

func (g *fakeGatewayBox) write(abs, content string, mode os.FileMode) {
	g.t.Helper()
	p := g.path(abs)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		g.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		g.t.Fatal(err)
	}
}

// newFakeGateway starts the gateway on a box in a temporary directory: its table has "/" for the dashboard's files, it writes its configuration, the address of its management listener and
// the secret of the boot, as the gateway does.
func newFakeGateway(t *testing.T, opts ...gwOpt) *fakeGatewayBox {
	t.Helper()
	cfg := &gwCfg{listenOn: "127.0.0.1", runtime: "/var/run/casaos"}
	g := &fakeGatewayBox{t: t, root: t.TempDir(), table: map[string]string{}, proxies: map[string]*httputil.ReverseProxy{}}
	g.roots = Roots{Prefix: g.root}
	for _, o := range opts {
		o(g, cfg)
	}
	g.tls = cfg.tls

	// the static server: the dashboard's files, and the 404 of echo for the rest
	static := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}` + "\n"))
	}))
	t.Cleanup(static.Close)
	g.register("/", static.URL)

	// the public port
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			g.mu.Lock()
			g.publicAt = append(g.publicAt, a)
			g.mu.Unlock()
		}
		g.mu.Lock()
		g.sawTLS = g.sawTLS || r.TLS != nil
		g.mu.Unlock()
		if r.URL.Path == "/ping" {
			_, _ = w.Write([]byte("pong from gateway service"))
			return
		}
		g.mu.Lock()
		proxy := g.proxyFor(r.URL.Path)
		g.mu.Unlock()
		if proxy == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		proxy.ServeHTTP(w, r)
	})
	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.listenOn, "0"))
	if err != nil {
		t.Skipf("no listening on %s here: %v", cfg.listenOn, err)
	}
	g.public = httptest.NewUnstartedServer(handler)
	g.public.Listener = ln
	if cfg.tls {
		g.public.StartTLS()
	} else {
		g.public.Start()
	}
	t.Cleanup(g.public.Close)
	_, port, _ := net.SplitHostPort(g.public.Listener.Addr().String())

	// the management listener, on loopback, for a person with a token or a service with the secret
	g.secret = strings.Repeat("ab", 32)
	g.secretDir = g.path(cfg.runtime)
	g.mgmt = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !external.IsInternalRequest(host, r.Header.Get("Authorization"), g.secretDir) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Unauthorized"}`))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/gateway/routes" {
			http.NotFound(w, r)
			return
		}
		g.mu.Lock()
		routes := make([]*model.Route, 0, len(g.table))
		for p, target := range g.table {
			routes = append(routes, &model.Route{Path: p, Target: target})
		}
		g.mu.Unlock()
		sort.Slice(routes, func(i, j int) bool { return routes[i].Path < routes[j].Path })
		_ = json.NewEncoder(w).Encode(routes)
	}))
	t.Cleanup(g.mgmt.Close)

	ini := "[common]\nruntimepath=" + cfg.runtime + "\n\n[gateway]\nport=" + port + "\naddress=" + cfg.address + "\n"
	if cfg.tls {
		ini += "tlscert=/etc/casaos/gateway.crt\ntlskey=/etc/casaos/gateway.key\n"
	} else {
		ini += "tlscert=\ntlskey=\n"
	}
	g.write(pathGateway, ini, 0o644)
	g.write(cfg.runtime+"/internal.secret", g.secret+"\n", 0o600)
	g.write(cfg.runtime+"/management.url", g.mgmt.URL, 0o600)
	return g
}

func (g *fakeGatewayBox) register(path, target string) {
	u, err := url.Parse(target)
	if err != nil {
		g.t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.table[path] = target
	g.proxies[path] = httputil.NewSingleHostReverseProxy(u)
}

// proxyFor is GetProxy: the longest path that the request path starts with
func (g *fakeGatewayBox) proxyFor(path string) *httputil.ReverseProxy {
	keys := make([]string, 0, len(g.proxies))
	for k := range g.proxies {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, p := range keys {
		if strings.HasPrefix(path, p) {
			return g.proxies[p]
		}
	}
	return nil
}

// a module behind the gateway, which answers every path with the status
func moduleAnswering(t *testing.T, status int) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status >= 300 && status < 400 {
			w.Header().Set("Location", "/elsewhere") // (followed, it is a path that nobody registered)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(s.Close)
	return s
}

// asked is what the assistant says of the route, with the gateway of the box
func (g *fakeGatewayBox) answers(route string) (bool, error) {
	g.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return newGateway(g.roots).Answers(ctx, route)
}

// the fake is the gateway: the public port answers what the real one does for a path nobody registered, and for one whose service is gone
func TestTheFakeGatewayAnswersAsTheGatewayDoes(t *testing.T) {
	g := newFakeGateway(t)
	_, port, _ := net.SplitHostPort(g.public.Listener.Addr().String())
	get := func(p string) int {
		resp, err := http.Get("http://127.0.0.1:" + port + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if get("/ping") != 200 || get(testRoute) != 404 {
		t.Errorf("/ping and a route nobody registered: %d, %d", get("/ping"), get(testRoute))
	}
	dead := moduleAnswering(t, 200)
	g.register(testRoute, dead.URL)
	dead.Close()
	if got := get(testRoute); got != http.StatusBadGateway {
		t.Errorf("a service that is gone: %d", got)
	}
}

// ---- what the assistant makes of it ------------------------------------------------------------------------------------------------------------------------------------------

func TestARouteThatIsRegisteredAndHasAServiceBehindItAnswers(t *testing.T) {
	for _, status := range []int{200, 204, 301, 302, 400, 401, 403, 405, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			g := newFakeGateway(t)
			g.register(testRoute, moduleAnswering(t, status).URL)
			if status/100 == 3 {
				// where it sends the request to is a service that is gone: a redirect is an answer and is not followed
				gone := moduleAnswering(t, 200)
				g.register("/elsewhere", gone.URL)
				gone.Close()
			}
			if ok, err := g.answers(testRoute); !ok || err != nil {
				t.Errorf("%d: %v, %v", status, ok, err)
			}
		})
	}
}

// the gateway's own answer for a path that nobody registered is the static server's 404, which is not the answer of the module
func TestARouteThatNobodyRegisteredDoesNotAnswerWhatEverTheGatewaySays(t *testing.T) {
	g := newFakeGateway(t)
	if ok, err := g.answers(testRoute); ok || err != nil {
		t.Errorf("an unregistered route: %v, %v", ok, err)
	}
	// another route of the table is not the route; nor is a part of it, or a longer one
	other := moduleAnswering(t, 200)
	for _, p := range []string{"/v2/other", "/v2", "/v2/virt", testRoute + "/", testRoute + "/x"} {
		g.register(p, other.URL)
	}
	if ok, err := g.answers(testRoute); ok || err != nil {
		t.Errorf("a route that is not the module's: %v, %v", ok, err)
	}
}

// a module that has no handler for the bare path of its route answers it with a 404 of its own: it is there, behind the route that it registered
func TestAModuleThatAnswers404ForItsBareRouteAnswers(t *testing.T) {
	g := newFakeGateway(t)
	g.register(testRoute, moduleAnswering(t, 404).URL)
	if ok, err := g.answers(testRoute); !ok || err != nil {
		t.Errorf("%v, %v", ok, err)
	}
}

func TestARouteWhoseServiceIsNotThereDoesNotAnswer(t *testing.T) {
	for name, set := range map[string]func(g *fakeGatewayBox){
		"a service that is gone": func(g *fakeGatewayBox) {
			s := moduleAnswering(t, 200)
			g.register(testRoute, s.URL)
			s.Close()
		},
		"a service that is not ready": func(g *fakeGatewayBox) { g.register(testRoute, moduleAnswering(t, 503).URL) },
		"a bad gateway behind":        func(g *fakeGatewayBox) { g.register(testRoute, moduleAnswering(t, 502).URL) },
		"a gateway time-out behind":   func(g *fakeGatewayBox) { g.register(testRoute, moduleAnswering(t, 504).URL) },
	} {
		t.Run(name, func(t *testing.T) {
			g := newFakeGateway(t)
			set(g)
			if ok, err := g.answers(testRoute); ok || err != nil {
				t.Errorf("%v, %v", ok, err)
			}
		})
	}
}

// the gateway is not there: its port refuses
func TestAGatewayThatIsNotThereDoesNotAnswer(t *testing.T) {
	g := newFakeGateway(t)
	g.register(testRoute, moduleAnswering(t, 200).URL)
	g.public.Close()
	if ok, err := g.answers(testRoute); ok || err != nil {
		t.Errorf("%v, %v", ok, err)
	}
	g.mgmt.Close()
	if ok, err := g.answers(testRoute); ok || err != nil {
		t.Errorf("with no management listener: %v, %v", ok, err)
	}
}

// with TLS in the configuration the public port serves TLS only, and the probe speaks it
func TestAGatewayThatServesTLSAnswersOnTLS(t *testing.T) {
	g := newFakeGateway(t, withTLS())
	if ok, err := g.answers(testRoute); ok || err != nil {
		t.Errorf("an unregistered route: %v, %v", ok, err)
	}
	g.register(testRoute, moduleAnswering(t, 200).URL)
	if ok, err := g.answers(testRoute); !ok || err != nil {
		t.Errorf("a registered one: %v, %v", ok, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.sawTLS {
		t.Error("the gateway was not spoken to in TLS")
	}
}

// with an address in the configuration the public port is not on 127.0.0.1, and the probe goes where the gateway listens
func TestAGatewayThatListensOnAnAddressAnswersThere(t *testing.T) {
	g := newFakeGateway(t, withAddress("127.0.0.2", "127.0.0.2"))
	g.register(testRoute, moduleAnswering(t, 200).URL)
	if ok, err := g.answers(testRoute); !ok || err != nil {
		t.Errorf("%v, %v", ok, err)
	}
	// an address the configuration spells for all of them is loopback for the probe
	for _, any := range []string{"0.0.0.0", "::", ""} {
		g := newFakeGateway(t, withAddress(any, "127.0.0.1"))
		g.register(testRoute, moduleAnswering(t, 200).URL)
		if ok, err := g.answers(testRoute); !ok || err != nil {
			t.Errorf("address %q: %v, %v", any, ok, err)
		}
	}
}

// an address that is not an address of the machine's, as a name, or with a zone, is not asked
func TestAGatewayWhoseAddressIsNotAnIPAddressIsAnError(t *testing.T) {
	for _, address := range []string{"localhost", "example.org", "fe80::1%eth0", "127.0.0.1:80", "1.2.3", "http://127.0.0.1"} {
		g := newFakeGateway(t)
		g.write(pathGateway, "[gateway]\nport=8080\naddress="+address+"\n", 0o644)
		g.register(testRoute, moduleAnswering(t, 200).URL)
		if ok, err := g.answers(testRoute); ok || err == nil {
			t.Errorf("address %q: %v, %v", address, ok, err)
		}
	}
}

// What the assistant cannot read of the table (it is not root, the secret is not there, the secret is the wrong one, the listener is not a loopback one) leaves it with the one thing it can
// see, and there a 404 is the static server's: no answer. Any other status is what it was.
func TestWhenTheTableCannotBeReadOnly404IsNotAnAnswer(t *testing.T) {
	for name, set := range map[string]func(g *fakeGatewayBox){
		"no secret": func(g *fakeGatewayBox) { _ = os.Remove(g.path("/var/run/casaos/internal.secret")) },
		"a wrong secret": func(g *fakeGatewayBox) {
			// the gateway went on with another secret than the one in the file
			g.secretDir = t.TempDir()
			if err := os.WriteFile(filepath.Join(g.secretDir, "internal.secret"), []byte(strings.Repeat("cd", 32)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"no address of the management listener": func(g *fakeGatewayBox) { _ = os.Remove(g.path("/var/run/casaos/management.url")) },
		"a name for loopback, not an address": func(g *fakeGatewayBox) {
			g.write("/var/run/casaos/management.url", strings.Replace(g.mgmt.URL, "127.0.0.1", "localhost", 1), 0o600)
		},
		"an address that is not loopback": func(g *fakeGatewayBox) { g.write("/var/run/casaos/management.url", "http://192.0.2.1:9", 0o600) },
		"no word of it":                   func(g *fakeGatewayBox) { g.write("/var/run/casaos/management.url", "", 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			g := newFakeGateway(t)
			set(g)
			g.register("/x", moduleAnswering(t, 200).URL)
			module := moduleAnswering(t, 200)
			g.register(testRoute, module.URL)
			if ok, err := g.answers(testRoute); !ok || err != nil {
				t.Errorf("a route with a module behind it: %v, %v", ok, err)
			}
			g.register(testRoute, moduleAnswering(t, 404).URL)
			if ok, err := g.answers(testRoute); ok || err != nil {
				t.Errorf("a route whose module says 404, which cannot be told from one that nobody registered: %v, %v", ok, err)
			}
			g.register(testRoute, moduleAnswering(t, 503).URL)
			if ok, err := g.answers(testRoute); ok || err != nil {
				t.Errorf("a service that is not ready: %v, %v", ok, err)
			}
		})
	}
}

// the secret of the boot is sent to the management listener and to nobody else
func TestTheSecretOfTheBootGoesToTheManagementListenerOnly(t *testing.T) {
	g := newFakeGateway(t)
	g.register(testRoute, moduleAnswering(t, 200).URL)
	if ok, _ := g.answers(testRoute); !ok {
		t.Fatal("the route does not answer")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.publicAt) != 0 {
		t.Errorf("the public port was sent %q", g.publicAt)
	}
}

// a table that is not a table of routes, or that is too large, is read as a table that cannot be read
func TestATableThatIsNotATableIsNotBelieved(t *testing.T) {
	type answer struct {
		status int
		body   string
	}
	for name, a := range map[string]answer{
		"an object":                   {200, `{"path":"` + testRoute + `","target":"x"}`},
		"not JSON":                    {200, `<html>`},
		"a huge table":                {200, `[{"path":"/pad","target":"` + strings.Repeat("a", 2<<20) + `"},{"path":"` + testRoute + `","target":"x"}]`},
		"a table with a server error": {500, `[{"path":"` + testRoute + `","target":"x"}]`},
		"a table that is refused":     {401, `[{"path":"` + testRoute + `","target":"x"}]`},
	} {
		t.Run(name, func(t *testing.T) {
			g := newFakeGateway(t)
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(a.status)
				_, _ = w.Write([]byte(a.body))
			}))
			t.Cleanup(bad.Close)
			g.write("/var/run/casaos/management.url", bad.URL, 0o600)
			g.register(testRoute, moduleAnswering(t, 404).URL)
			if ok, err := g.answers(testRoute); ok || err != nil {
				t.Errorf("%v, %v", ok, err)
			}
		})
	}
}

// a listener of the management that sends the assistant elsewhere is not followed, with the secret of the boot, to a table it did not ask for
func TestTheManagementListenerIsNotFollowedElsewhere(t *testing.T) {
	g := newFakeGateway(t)
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"path":"` + testRoute + `","target":"x"}]`))
	}))
	t.Cleanup(elsewhere.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirecting.Close)
	g.write("/var/run/casaos/management.url", redirecting.URL, 0o600)
	g.register(testRoute, moduleAnswering(t, 404).URL)
	if ok, err := g.answers(testRoute); ok || err != nil {
		t.Errorf("%v, %v", ok, err)
	}
}

// the secret of the boot goes to the management listener on loopback, and not to an address of the machine that is not loopback, whatever the file says
func TestTheSecretOfTheBootIsNotSentToAnAddressThatIsNotLoopback(t *testing.T) {
	var lan net.IP
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			lan = n.IP
			break
		}
	}
	if lan == nil {
		t.Skip("this machine has no address but loopback")
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(lan.String(), "0"))
	if err != nil {
		t.Skipf("no listening on %s: %v", lan, err)
	}
	var mu sync.Mutex
	var seen []string
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = w.Write([]byte(`[{"path":"` + testRoute + `","target":"x"}]`))
	}))
	other.Listener = ln
	other.Start()
	t.Cleanup(other.Close)
	g := newFakeGateway(t)
	g.write("/var/run/casaos/management.url", other.URL, 0o600)
	g.register(testRoute, moduleAnswering(t, 404).URL)
	if ok, err := g.answers(testRoute); ok || err != nil {
		t.Errorf("%v, %v", ok, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 0 {
		t.Errorf("the secret was sent to %s: %q", lan, seen)
	}
}

// ---- the configuration -------------------------------------------------------------------------------------------------------------------------------------------------------

func TestTheConfigurationOfTheGatewayIsRead(t *testing.T) {
	for name, c := range map[string]struct {
		ini      string
		port     int
		host     string
		https    bool
		runtime  string
		wantFail bool
	}{
		"the sample as it is written":         {"[common]\nruntimepath=/var/run/casaos\n\n[gateway]\nport=\naddress=\ntlscert=\ntlskey=\nhttpsport=\n", 0, "", false, "", true},
		"a port":                              {"[gateway]\nport=8080\n", 8080, "127.0.0.1", false, "/var/run/casaos", false},
		"a port in the capitals of a viper":   {"[Gateway]\nPort=80\n", 80, "127.0.0.1", false, "/var/run/casaos", false},
		"port 0":                              {"[gateway]\nport=0\n", 0, "", false, "", true},
		"a port that is too high":             {"[gateway]\nport=65536\n", 0, "", false, "", true},
		"a port that is a word":               {"[gateway]\nport=http\n", 0, "", false, "", true},
		"an address":                          {"[gateway]\nport=80\naddress=192.168.1.5\n", 80, "192.168.1.5", false, "/var/run/casaos", false},
		"an address that is IPv6":             {"[gateway]\nport=80\naddress=::1\n", 80, "::1", false, "/var/run/casaos", false},
		"the address of all of them":          {"[gateway]\nport=80\naddress=0.0.0.0\n", 80, "127.0.0.1", false, "/var/run/casaos", false},
		"the IPv6 address of all of them":     {"[gateway]\nport=80\naddress=::\n", 80, "127.0.0.1", false, "/var/run/casaos", false},
		"a certificate and a key":             {"[gateway]\nport=443\ntlscert=/a\ntlskey=/b\n", 443, "127.0.0.1", true, "/var/run/casaos", false},
		"a certificate and no key":            {"[gateway]\nport=80\ntlscert=/a\n", 80, "127.0.0.1", false, "/var/run/casaos", false},
		"a key and no certificate":            {"[gateway]\nport=80\ntlskey=/b\n", 80, "127.0.0.1", false, "/var/run/casaos", false},
		"another runtime path":                {"[common]\nruntimepath=/run/x\n[gateway]\nport=80\n", 80, "127.0.0.1", false, "/run/x", false},
		"a runtime path that is not absolute": {"[common]\nruntimepath=run\n[gateway]\nport=80\n", 0, "", false, "", true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseGatewayConfig([]byte(c.ini))
			if c.wantFail {
				if err == nil {
					t.Fatalf("read: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.port != c.port || got.host != c.host || got.https != c.https || got.runtime != c.runtime {
				t.Errorf("%+v", got)
			}
		})
	}
}

// the gateway keeps its port in the file only once it has one: before that there is nothing to ask
func TestAGatewayWithNoPortYetDoesNotAnswer(t *testing.T) {
	g := newFakeGateway(t)
	g.write(pathGateway, "[gateway]\nport=\n", 0o644)
	if ok, err := g.answers(testRoute); ok || err == nil {
		t.Errorf("%v, %v", ok, err)
	}
	_ = os.Remove(g.path(pathGateway))
	if ok, err := g.answers(testRoute); ok || err == nil {
		t.Errorf("with no configuration: %v, %v", ok, err)
	}
}

// a listener that takes the connection and says nothing is waited for five seconds, by the client and not by the context of the caller (the install has none until it is told to stop)
func TestAListenerThatSaysNothingIsWaitedForFiveSeconds(t *testing.T) {
	for _, which := range []string{"the public port", "the management listener"} {
		t.Run(which, func(t *testing.T) {
			t.Parallel()
			g := newFakeGateway(t)
			g.register(testRoute, moduleAnswering(t, 200).URL)
			silent, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = silent.Close() })
			_, port, _ := net.SplitHostPort(silent.Addr().String())
			if which == "the public port" {
				g.write(pathGateway, "[gateway]\nport="+port+"\n", 0o644)
			} else {
				g.write("/var/run/casaos/management.url", "http://"+silent.Addr().String(), 0o600)
				g.register(testRoute, moduleAnswering(t, 404).URL)
			}
			start := time.Now()
			ok, err := newGateway(g.roots).Answers(context.Background(), testRoute)
			if took := time.Since(start); ok || err != nil || took < 4*time.Second || took > 9*time.Second {
				t.Errorf("answers %v (%v) after %v", ok, err, took)
			}
		})
	}
}

// the probe does not wait for ever
func TestTheProbeOfAGatewayThatDoesNotAnswerEnds(t *testing.T) {
	g := newFakeGateway(t)
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = silent.Close() })
	_, port, _ := net.SplitHostPort(silent.Addr().String())
	g.write(pathGateway, "[gateway]\nport="+port+"\n", 0o644)
	g.register(testRoute, moduleAnswering(t, 200).URL)
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	ok, _ := newGateway(g.roots).Answers(ctx, testRoute)
	if ok || time.Since(start) > 3*time.Second {
		t.Errorf("answers %v after %v", ok, time.Since(start))
	}
}
