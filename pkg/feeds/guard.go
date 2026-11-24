package feeds

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

var (
	errRefused  = errors.New("refused: not a public address")
	cgnat       = netip.MustParsePrefix("100.64.0.0/10") // Tailscale and carrier NAT
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
)

// public says whether a feed may be fetched from a: a global unicast address that
// is neither private nor CGNAT, as the relay's own rule (pkg/share), and not 0/8.
// Loopback, link-local (169.254.169.254 included), multicast and unspecified are not
// global unicast.
func public(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	return a.IsGlobalUnicast() && !a.IsPrivate() && !cgnat.Contains(a) && !thisNetwork.Contains(a)
}

// dialControl refuses a connection to any address public refuses. It runs on the
// address the name resolved to, for every dial, so a redirect or a name that
// resolves to the box's network is refused too.
func dialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errRefused
	}
	a, err := netip.ParseAddr(host)
	if err != nil || !public(a) {
		return errRefused
	}
	return nil
}

// guardedClient fetches feeds: no proxy (the guard would see the proxy's address),
// 10 s in all, three redirects at most, http(s) only.
func guardedClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: dialControl}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            dialer.DialContext,
			TLSHandshakeTimeout:    5 * time.Second,
			ResponseHeaderTimeout:  10 * time.Second,
			MaxResponseHeaderBytes: 64 << 10,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirect to a non-http address")
			}
			return nil
		},
	}
}
