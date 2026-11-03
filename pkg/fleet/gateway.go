package fleet

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/go-ini/ini"
)

// GatewayInfo is where this box's gateway listens, as seen from the box itself.
type GatewayInfo struct {
	Public      *url.URL // http(s)://127.0.0.1:<port>: where a member executes relayed requests
	HTTPS       *url.URL // https://127.0.0.1:<httpsport or 443>; nil when a configured certificate makes Public itself https, or HTTPS is off (httpsport=0)
	CertFile    string   // the certificate the gateway serves: tlscert, or the default self-signed one
	Unreachable string   // non-empty when the gateway is bound off loopback: the member cannot relay, and says so, but the master can still read the certificate to enrol boxes
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
