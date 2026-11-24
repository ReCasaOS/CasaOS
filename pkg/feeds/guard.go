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

// dialControl refuses a connection to any address allowed refuses. It runs on the
// address the name resolved to, for every dial, so a redirect or a name that
// resolves to the box's network is refused too.
func dialControl(allowed func(netip.AddrPort) bool) func(string, string, syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !allowed(ap) {
			return errRefused
		}
		return nil
	}
}

// guardedClient fetches feeds from public addresses only.
func guardedClient() *http.Client {
	return clientAllowing(func(ap netip.AddrPort) bool { return public(ap.Addr()) })
}

// clientAllowing fetches feeds from the addresses allowed lets through: no proxy (the
// guard would see the proxy's address), 10 s in all, three redirects at most, http(s) only.
func clientAllowing(allowed func(netip.AddrPort) bool) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: dialControl(allowed)}
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
