package fleet

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
)

// systemRoots is the pool a chain is verified against; tests replace it.
var systemRoots = func() (*x509.CertPool, error) { return x509.SystemCertPool() }

// Fingerprint is the sha256 of a certificate as sent, hex: what a code pins.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// tlsConfig is how a member decides to trust the master: the chain is verified
// as any client would (a real certificate needs nothing); when it does not
// verify, the leaf is accepted iff its fingerprint is the one in the enrolment
// code (the self-signed default). The hostname only matters to the chain.
func tlsConfig(serverName, pinSHA256 string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         serverName,
		InsecureSkipVerify: true, //nolint:gosec // VerifyConnection verifies: the chain first, then the pin
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the master sent no certificate")
			}
			leaf := cs.PeerCertificates[0]
			chainErr := verifyChain(leaf, cs.PeerCertificates[1:], serverName)
			if chainErr == nil {
				return nil
			}
			if pinSHA256 != "" && subtle.ConstantTimeCompare([]byte(Fingerprint(leaf)), []byte(pinSHA256)) == 1 {
				return nil
			}
			return fmt.Errorf("the master's certificate is neither trusted nor the one of the enrolment code: %w", chainErr)
		},
	}
}

func verifyChain(leaf *x509.Certificate, rest []*x509.Certificate, serverName string) error {
	roots, err := systemRoots()
	if err != nil {
		return err
	}
	intermediates := x509.NewCertPool()
	for _, c := range rest {
		intermediates.AddCert(c)
	}
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: serverName, Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}
