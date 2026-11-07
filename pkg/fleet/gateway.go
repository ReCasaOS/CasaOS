package fleet

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-ini/ini"
)

// GatewayInfo is where this box's gateway listens, as seen from the box itself.
type GatewayInfo struct {
	Public       *url.URL // http(s)://127.0.0.1:<port>: where a member executes relayed requests
	HTTPS        *url.URL // https://127.0.0.1:<httpsport or 443>; nil when a configured certificate makes Public itself https, or HTTPS is off (httpsport=0)
	CertFile     string   // the certificate the gateway serves: tlscert, or the default self-signed one
	LoopbackOnly bool     // the gateway is bound to the loopback address alone: fine for the box itself, not for a master that other boxes reach over a network
	Unreachable  string   // non-empty when the gateway is bound off loopback: the member cannot relay, and says so, but the master can still read the certificate to enrol boxes
}

// ReadGatewayINI reads [gateway] port, address, tlscert, tlskey and httpsport
// from path; dataPath is where the default certificate lives (/var/lib/casaos).
// It errors only on config a box cannot use at all (no port, a bad httpsport).
// A gateway bound off loopback, and HTTPS turned off with httpsport=0, are
// normal states, not errors: the member's relay reports the first, and the
// certificate is still read so the master can enrol boxes.
func ReadGatewayINI(path, dataPath string) (GatewayInfo, error) {
	f, err := ini.LoadSources(ini.LoadOptions{Insensitive: true}, path)
	if err != nil {
		return GatewayInfo{}, err
	}
	gw := f.Section("gateway")
	port, err := strconv.Atoi(gw.Key("port").String())
	if err != nil || port < 1 || port > 65535 {
		return GatewayInfo{}, errors.New("the gateway has no port yet")
	}
	info := GatewayInfo{CertFile: filepath.Join(dataPath, "tls", "gateway.crt")}
	if address := gw.Key("address").String(); address != "" {
		ip, err := netip.ParseAddr(address)
		if err != nil || !(ip.IsUnspecified() || ip.IsLoopback()) {
			info.Unreachable = fmt.Sprintf("the gateway listens on %s, not on this box's loopback, so the fleet cannot reach it", address)
		}
		info.LoopbackOnly = err == nil && ip.IsLoopback()
	}
	if cert, key := gw.Key("tlscert").String(), gw.Key("tlskey").String(); cert != "" && key != "" {
		info.Public = loopbackURL("https", port)
		info.CertFile = cert
		return info, nil
	}
	info.Public = loopbackURL("http", port)
	httpsPort := 443
	if s := gw.Key("httpsport").String(); s != "" {
		if httpsPort, err = strconv.Atoi(s); err != nil || httpsPort < 0 || httpsPort > 65535 {
			return GatewayInfo{}, errors.New("the gateway's httpsport is not a port")
		}
	}
	if httpsPort != 0 { // httpsport=0 turns the self-signed HTTPS off at the gateway
		info.HTTPS = loopbackURL("https", httpsPort)
	}
	return info, nil
}

func loopbackURL(scheme string, port int) *url.URL {
	return &url.URL{Scheme: scheme, Host: "127.0.0.1:" + strconv.Itoa(port)}
}

// CertFingerprint is the fingerprint of the first certificate in a PEM file.
func CertFingerprint(certFile string) (string, error) {
	data, err := os.ReadFile(certFile)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("no certificate in " + certFile)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	return Fingerprint(cert), nil
}

// webAddr is the scheme and port (default filled in) of a dashboard URL; empty when u is nil or not http(s).
func webAddr(u *url.URL) (scheme, port string) {
	if u == nil {
		return "", ""
	}
	port = u.Port()
	if port == "" {
		port = defaultPort[u.Scheme]
	}
	return validWeb(u.Scheme, port)
}

var defaultPort = map[string]string{"http": "80", "https": "443"}

// validWeb keeps a scheme and port only when both are well formed: "http" or
// "https", and 1-65535 in plain digits. It is the only door for what a hello says.
func validWeb(scheme, port string) (string, string) {
	if defaultPort[scheme] == "" || port == "" || len(port) > 5 || strings.Trim(port, "0123456789") != "" {
		return "", ""
	}
	if n, _ := strconv.Atoi(port); n < 1 || n > 65535 {
		return "", ""
	}
	return scheme, port
}

// webURL is scheme://addr[:port], the port left out when it is the scheme's default;
// empty without an address or a valid scheme and port.
func webURL(addr, scheme, port string) string {
	if scheme, port = validWeb(scheme, port); addr == "" || scheme == "" {
		return ""
	}
	if port == defaultPort[scheme] {
		if strings.Contains(addr, ":") {
			addr = "[" + addr + "]"
		}
		return scheme + "://" + addr
	}
	return scheme + "://" + net.JoinHostPort(addr, port)
}
