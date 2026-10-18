package modulecmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-ini/ini"
)

// Whether the gateway answers on the route of a module is asked in two steps, because the gateway answers every path: the dashboard's files are a route of its own, at "/", and a path
// that nobody registered is a request for a file, which the static server answers with a 404.
//
//  1. The route has to be in the table of the gateway: GET /v1/gateway/routes on its management listener, which is on loopback at the address in <runtimepath>/management.url and is
//     open to a person with a token and to a service with the secret of the boot, <runtimepath>/internal.secret (root only; both files are written by the gateway when it starts).
//  2. A request for the route to the public port gets an answer that is a service's: not 502, 503 or 504 (the proxy's own, for a service that is not there). The public port is where
//     /etc/casaos/gateway.ini says (port, address), and is spoken to in TLS when the configuration has a certificate and a key.
//
// A 404 from a route that is in the table is the service's own (it has nothing at the bare path of its route, and it is there). When the table cannot be read (the assistant is not root,
// there is no secret, the secret is not the boot's), the one thing left to go on is the answer, and there a 404 is the static server's: no answer. Everything else is as it is.
type httpGateway struct {
	roots  Roots
	public *http.Client // the public port
	mgmt   *http.Client // the management listener
}

// the files of the gateway's runtime path, and where that is when the configuration does not say
const (
	defaultGatewayRuntime = "/var/run/casaos"
	gatewayManagementURL  = "management.url"
	gatewaySecret         = "internal.secret"
	gatewayRoutesPath     = "/v1/gateway/routes"
	maxRoutesBytes        = 1 << 20
)

func newGateway(roots Roots) Gateway {
	redirects := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return httpGateway{
		roots: roots,
		// The certificate is not verified: the probe asks the listener of this box by the address it is bound to, which no certificate carries, sends nothing that is secret and reads a
		// status. The version is the least that is not broken.
		public: &http.Client{
			Timeout:       5 * time.Second,
			CheckRedirect: redirects,
			Transport:     &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}}, //nolint:gosec // see above
		},
		mgmt: &http.Client{Timeout: 5 * time.Second, CheckRedirect: redirects, Transport: &http.Transport{DisableKeepAlives: true}},
	}
}

// A gatewayConfig is what the configuration of the gateway says of where it is.
type gatewayConfig struct {
	port    int
	host    string // where the public port is asked: the address it is bound to, loopback when it is bound to all of them
	https   bool
	runtime string // the runtime path, where the files of the management listener are
}

// parseGatewayConfig reads /etc/casaos/gateway.ini: [gateway] port (empty until the gateway has found a free one at its first start), address (empty for every interface), tlscert and
// tlskey (both, or plain HTTP), and [common] runtimepath.
func parseGatewayConfig(raw []byte) (gatewayConfig, error) {
	f, err := ini.LoadSources(ini.LoadOptions{Insensitive: true}, raw)
	if err != nil {
		return gatewayConfig{}, err
	}
	gw := f.Section("gateway")
	port, err := strconv.Atoi(gw.Key("port").String())
	if err != nil || port < 1 || port > 65535 {
		return gatewayConfig{}, errors.New("the gateway has no port yet")
	}
	host, err := gatewayHost(gw.Key("address").String())
	if err != nil {
		return gatewayConfig{}, err
	}
	runtime := f.Section("common").Key("runtimepath").String()
	if runtime == "" {
		runtime = defaultGatewayRuntime
	}
	if !path.IsAbs(runtime) {
		return gatewayConfig{}, errors.New("the runtime path of the gateway is not absolute")
	}
	return gatewayConfig{port: port, host: host, https: gw.Key("tlscert").String() != "" && gw.Key("tlskey").String() != "", runtime: runtime}, nil
}

// gatewayHost is where to ask a gateway that is bound to the address: that address, or loopback for the ones that mean every interface (the gateway listens on all of them, IPv4 and IPv6,
// when it is given none). A name is not looked up, nor an address with a zone: only an address is asked.
func gatewayHost(address string) (string, error) {
	if address == "" {
		return "127.0.0.1", nil
	}
	ip, err := netip.ParseAddr(address)
	if err != nil || ip.Zone() != "" {
		return "", errors.New("the address of the gateway is not an IP address that can be asked")
	}
	if ip.IsUnspecified() {
		return "127.0.0.1", nil
	}
	return ip.String(), nil
}

func (g httpGateway) Answers(ctx context.Context, route string) (bool, error) {
	raw, err := readRegular(g.roots.Path(pathGateway), 64<<10)
	if err != nil {
		return false, err
	}
	cfg, err := parseGatewayConfig(raw)
	if err != nil {
		return false, err
	}
	registered, tableRead := g.registered(ctx, cfg, route)
	if tableRead && !registered {
		return false, nil
	}
	scheme := "http"
	if cfg.https {
		scheme = "https"
	}
	u := url.URL{Scheme: scheme, Host: net.JoinHostPort(cfg.host, strconv.Itoa(cfg.port)), Path: route}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false, err
	}
	resp, err := g.public.Do(req)
	if err != nil {
		return false, nil // (refused, or no answer in time)
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return false, nil
	case http.StatusNotFound:
		return tableRead, nil // (the service's own, if the table has the route; the static server's if it could not be looked in)
	}
	return true, nil
}

// registered says whether the table of the gateway has the route, and whether the table could be read at all.
func (g httpGateway) registered(ctx context.Context, cfg gatewayConfig, route string) (registered, tableRead bool) {
	dir := g.roots.Path(cfg.runtime)
	rawURL, err := readRegular(filepath.Join(dir, gatewayManagementURL), 256)
	if err != nil {
		return false, false
	}
	u, err := url.Parse(strings.TrimSpace(string(rawURL)))
	if err != nil {
		return false, false
	}
	// the secret of the boot goes to an address of loopback and to nowhere else (the gateway writes http://127.0.0.1:<port>; a name is not looked up)
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || port == "" {
		return false, false
	}
	if ip, err := netip.ParseAddr(host); err != nil || !ip.IsLoopback() {
		return false, false
	}
	secret, err := readRegular(filepath.Join(dir, gatewaySecret), 257)
	if err != nil || strings.TrimSpace(string(secret)) == "" {
		return false, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+u.Host+gatewayRoutesPath, nil)
	if err != nil {
		return false, false
	}
	req.Header.Set("Authorization", "Internal "+strings.TrimSpace(string(secret)))
	resp, err := g.mgmt.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	var table []struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRoutesBytes)).Decode(&table); err != nil {
		return false, false
	}
	for _, r := range table {
		if r.Path == route {
			return true, true
		}
	}
	return false, true
}
