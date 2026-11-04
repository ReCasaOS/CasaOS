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

// A non-loopback bind is not an error: the member reports it, but the master
// can still read the certificate to enrol boxes.
func TestGatewayMarksANonLoopbackAddressUnreachable(t *testing.T) {
	data := t.TempDir()
	got, err := ReadGatewayINI(writeINI(t, "[gateway]\nport=80\naddress=192.168.1.5\n"), data)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(got.Unreachable, "192.168.1.5") {
		t.Fatalf("Unreachable = %q", got.Unreachable)
	}
	if got.CertFile != filepath.Join(data, "tls", "gateway.crt") {
		t.Fatalf("CertFile lost: %q", got.CertFile)
	}
}

// httpsport=0 turns the self-signed HTTPS off: HTTPS is nil, not an error.
func TestGatewayHTTPSOffWhenHTTPSPortIsZero(t *testing.T) {
	data := t.TempDir()
	got, err := ReadGatewayINI(writeINI(t, "[gateway]\nport=80\nhttpsport=0\n"), data)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.HTTPS != nil || got.Public.String() != "http://127.0.0.1:80" || got.CertFile != filepath.Join(data, "tls", "gateway.crt") {
		t.Fatalf("%+v", got)
	}
}

func TestGatewayINIWithoutAPortIsAnError(t *testing.T) {
	if _, err := ReadGatewayINI(writeINI(t, "[gateway]\nport=\n"), t.TempDir()); err == nil {
		t.Fatal("accepted")
	}
	if _, err := ReadGatewayINI(writeINI(t, "[gateway]\nport=80\nhttpsport=notaport\n"), t.TempDir()); err == nil {
		t.Fatal("a bad httpsport was accepted")
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

func TestGatewayMarksALoopbackOnlyBind(t *testing.T) {
	for address, want := range map[string]bool{"127.0.0.1": true, "::1": true, "0.0.0.0": false, "192.168.1.5": false, "": false} {
		got, err := ReadGatewayINI(writeINI(t, "[gateway]\nport=80\naddress="+address+"\n"), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if got.LoopbackOnly != want {
			t.Errorf("address %q: LoopbackOnly = %v, want %v", address, got.LoopbackOnly, want)
		}
	}
}
