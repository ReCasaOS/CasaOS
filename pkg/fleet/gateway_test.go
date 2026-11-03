package fleet

import (
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeINI(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.ini")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGatewayURLFromINI(t *testing.T) {
	data := t.TempDir()
	defaultCert := filepath.Join(data, "tls", "gateway.crt")
	for _, c := range []struct{ ini, public, https, cert string }{
		{"[gateway]\nport=80\naddress=\n", "http://127.0.0.1:80", "https://127.0.0.1:443", defaultCert},
		{"[gateway]\nport=8443\ntlscert=/c\ntlskey=/k\nhttpsport=\n", "https://127.0.0.1:8443", "", "/c"},
		{"[gateway]\nport=80\nhttpsport=8443\n", "http://127.0.0.1:80", "https://127.0.0.1:8443", defaultCert},
		{"[gateway]\nport=80\naddress=0.0.0.0\n", "http://127.0.0.1:80", "https://127.0.0.1:443", defaultCert},
	} {
		got, err := ReadGatewayINI(writeINI(t, c.ini), data)
		if err != nil {
			t.Fatalf("%q: %v", c.ini, err)
		}
		https := ""
		if got.HTTPS != nil {
			https = got.HTTPS.String()
		}
		if got.Public.String() != c.public || https != c.https || got.CertFile != c.cert {
			t.Errorf("%q: public %s https %q cert %s", c.ini, got.Public, https, got.CertFile)
		}
	}
}

func TestGatewayURLRefusesANonLoopbackAddress(t *testing.T) {
	_, err := ReadGatewayINI(writeINI(t, "[gateway]\nport=80\naddress=192.168.1.5\n"), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "192.168.1.5") {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewayINIWithoutAPortIsAnError(t *testing.T) {
	if _, err := ReadGatewayINI(writeINI(t, "[gateway]\nport=\n"), t.TempDir()); err == nil {
		t.Fatal("accepted")
	}
	if _, err := ReadGatewayINI(filepath.Join(t.TempDir(), "missing.ini"), t.TempDir()); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestCertFingerprintOfAPEMFile(t *testing.T) {
	leaf, _ := makeCert(t, "localhost", false, nil, nil)
	path := filepath.Join(t.TempDir(), "gateway.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := CertFingerprint(path)
	if err != nil || got != Fingerprint(leaf) {
		t.Fatalf("%q %v, want %q", got, err, Fingerprint(leaf))
	}
	if _, err := CertFingerprint(filepath.Join(t.TempDir(), "none.crt")); err == nil {
		t.Fatal("missing file accepted")
	}
}
