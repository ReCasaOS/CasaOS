package fleet

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"
)

// makeCert makes a certificate for the names, self-signed unless a parent is given.
func makeCert(t *testing.T, cn string, isCA bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		DNSNames:              []string{cn},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func verify(cfg *tls.Config, certs ...*x509.Certificate) error {
	return cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: certs})
}

func TestPinnedLeafIsAcceptedWhenTheChainIsNot(t *testing.T) {
	leaf, _ := makeCert(t, "master.example", false, nil, nil)
	if err := verify(tlsConfig("master.example", Fingerprint(leaf)), leaf); err != nil {
		t.Fatal(err)
	}
}

func TestWrongPinIsRefused(t *testing.T) {
	leaf, _ := makeCert(t, "master.example", false, nil, nil)
	other, _ := makeCert(t, "master.example", false, nil, nil)
	if err := verify(tlsConfig("master.example", Fingerprint(other)), leaf); err == nil {
		t.Fatal("accepted")
	}
}

func TestNoPinAndNoChainIsRefused(t *testing.T) {
	leaf, _ := makeCert(t, "master.example", false, nil, nil)
	if err := verify(tlsConfig("master.example", ""), leaf); err == nil {
		t.Fatal("accepted")
	}
}

func TestHostnameIsIgnoredWhenThePinDecides(t *testing.T) {
	leaf, _ := makeCert(t, "localhost", false, nil, nil)
	if err := verify(tlsConfig("100.64.0.1", Fingerprint(leaf)), leaf); err != nil {
		t.Fatal(err)
	}
}

func TestAValidChainNeedsNoPin(t *testing.T) {
	ca, caKey := makeCert(t, "Fleet Test CA", true, nil, nil)
	leaf, _ := makeCert(t, "master.example", false, ca, caKey)
	old := systemRoots
	t.Cleanup(func() { systemRoots = old })
	systemRoots = func() (*x509.CertPool, error) {
		pool := x509.NewCertPool()
		pool.AddCert(ca)
		return pool, nil
	}
	if err := verify(tlsConfig("master.example", ""), leaf); err != nil {
		t.Fatal(err)
	}
	if err := verify(tlsConfig("other.example", ""), leaf); err == nil {
		t.Fatal("a valid chain for another name was accepted")
	}
}

func TestTLSConfigIsTLS12OrBetter(t *testing.T) {
	if cfg := tlsConfig("m", ""); cfg.MinVersion != tls.VersionTLS12 || cfg.ServerName != "m" {
		t.Fatalf("%+v", cfg)
	}
}
