package modulecmd

import (
	"crypto/tls"
	"net/http"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The downloader of production verifies the certificate of the host and speaks TLS 1.2 at least, redirects only where checkURL allows, and takes no more than an archive may be
func TestTheDownloaderOfProductionVerifiesCertificatesAndSpeaksTLS12(t *testing.T) {
	d, ok := NewHTTPDownloader().(httpDownloader)
	if !ok {
		t.Fatalf("%T", NewHTTPDownloader())
	}
	tr, ok := d.client.Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil {
		t.Fatalf("the transport: %T", d.client.Transport)
	}
	cfg := tr.TLSClientConfig
	if cfg.InsecureSkipVerify {
		t.Error("the certificate of the host is not verified")
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Errorf("the lowest version of TLS is %#x", cfg.MinVersion)
	}
	if cfg.VerifyPeerCertificate != nil || cfg.VerifyConnection != nil || cfg.GetClientCertificate != nil {
		t.Error("the verification of the certificate is replaced")
	}
	if cfg.RootCAs != nil {
		t.Error("the roots of the system are not the ones that are used")
	}
	if d.client.CheckRedirect == nil || d.max != osmodule.MaxArchiveBytes {
		t.Errorf("the redirects are not checked, or the size is %d", d.max)
	}
	if tr.DialTLSContext != nil || tr.DialTLS != nil {
		t.Error("the connection is made by something else")
	}
}
