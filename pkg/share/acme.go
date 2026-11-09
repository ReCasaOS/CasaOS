package share

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
	"golang.org/x/crypto/acme"
)

const (
	DefaultACMEDirectory = "https://acme-v02.api.letsencrypt.org/directory"
	TOSURL               = "https://letsencrypt.org/repository/"
	renewBefore          = 30 * 24 * time.Hour

	retryMin = time.Hour
	retryMax = 24 * time.Hour

	attemptTimeout = 15 * time.Minute // one Obtain, validation and issuance included
)

// TXT publishes the DNS-01 record through the relay (a *Relay does).
type TXT interface {
	SetTXT(ctx context.Context, value string) error
	ClearTXT(ctx context.Context) error
}

// acmeAPI is the slice of *acme.Client the flow needs; acmeClient wraps the real one.
type acmeAPI interface {
	Register(ctx context.Context) error
	Order(ctx context.Context, domain string) (orderURI string, authzURLs []string, err error)
	DNS01(ctx context.Context, authzURL string) (challengeURI, txtValue string, err error)
	Accept(ctx context.Context, challengeURI, authzURL string) error
	Finalize(ctx context.Context, orderURI string, csr []byte) (chain [][]byte, err error)
}

// ACME gets and renews the wildcard certificate of this box from Let's Encrypt by DNS-01.
type ACME struct {
	Directory         string // "" → CASAOS_SHARE_ACME_DIRECTORY or DefaultACMEDirectory
	CAFile            string // "" → CASAOS_SHARE_ACME_CA; a PEM root replacing the system pool (tests, Pebble)
	AccountFile       string // {"key": "<base64url P-256 PKCS8>"}
	KeyFile, CertFile string
	TXT               TXT
	Now               func() time.Time // nil → time.Now
	newClient         func(key crypto.Signer) acmeAPI

	mu      sync.Mutex
	pending bool
	lastErr string
}

// CertStatus is what the certificate on disk and the last attempt say.
type CertStatus struct {
	NotAfter time.Time
	Pending  bool
	Error    string
	Names    []string
}

func (a *ACME) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// readCert returns the leaf of CertFile, or nil when there is none or it is unreadable.
func (a *ACME) readCert() *x509.Certificate {
	data, err := os.ReadFile(a.CertFile)
	if err != nil {
		return nil
	}
	blk, _ := pem.Decode(data)
	if blk == nil {
		return nil
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil
	}
	return c
}

func (a *ACME) Status() CertStatus {
	a.mu.Lock()
	st := CertStatus{Pending: a.pending, Error: a.lastErr}
	a.mu.Unlock()
	if c := a.readCert(); c != nil {
		st.NotAfter, st.Names = c.NotAfter, c.DNSNames
	}
	return st
}

// Needs is true when there is no certificate for wildcard, or it expires within renewBefore.
func (a *ACME) Needs(wildcard string, now time.Time) bool {
	c := a.readCert()
	if c == nil {
		return true
	}
	hasName := false
	for _, n := range c.DNSNames {
		hasName = hasName || n == wildcard
	}
	return !hasName || c.NotAfter.Sub(now) < renewBefore
}

// Loop checks every tick and calls Obtain when Needs; after an error it waits an hour (measured with Now),
// doubling up to a day. An empty wildcard() means nothing is shared: idle.
func (a *ACME) Loop(ctx context.Context, wildcard func() string, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	var next time.Time
	delay := retryMin
	for {
		if w := wildcard(); w != "" && !a.now().Before(next) && a.Needs(w, a.now()) {
			actx, cancel := context.WithTimeout(ctx, attemptTimeout)
			err := a.Obtain(actx, strings.TrimPrefix(w, "*."))
			cancel()
			if err != nil && ctx.Err() == nil {
				next = a.now().Add(delay)
				delay = min(delay*2, retryMax)
			} else if err == nil {
				next, delay = time.Time{}, retryMin
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Obtain gets *.<host>: it writes KeyFile, then CertFile, only once the CA has issued.
func (a *ACME) Obtain(ctx context.Context, host string) (err error) {
	a.mu.Lock()
	a.pending = true
	a.mu.Unlock()
	defer func() {
		if err != nil {
			err = acmeError(err)
		}
		a.mu.Lock()
		a.pending = false
		if err == nil {
			a.lastErr = ""
		} else if !errors.Is(ctx.Err(), context.Canceled) { // a shutdown is not a failure of the CA; a deadline is
			a.lastErr = err.Error()
		}
		a.mu.Unlock()
	}()

	accountKey, err := a.accountKey()
	if err != nil {
		return err
	}
	newClient := a.newClient
	if newClient == nil {
		newClient = a.realClient
	}
	cl := newClient(accountKey)
	if err := cl.Register(ctx); err != nil {
		return err
	}
	domain := "*." + host
	orderURI, authzURLs, err := cl.Order(ctx, domain)
	if err != nil {
		return err
	}
	for _, u := range authzURLs {
		if err := a.solve(ctx, cl, u); err != nil {
			return err
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: []string{domain}}, key)
	if err != nil {
		return err
	}
	chain, err := cl.Finalize(ctx, orderURI, csr)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	var crt []byte
	for _, der := range chain {
		crt = append(crt, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	if err := os.MkdirAll(filepath.Dir(a.KeyFile), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.CertFile), 0o700); err != nil {
		return err
	}
	// The Gateway reloads on the certificate's mtime: the key must already be there.
	if err := file.WriteFileAtomic(a.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})); err != nil {
		return err
	}
	return file.WriteFileAtomic(a.CertFile, crt)
}

// solve publishes the TXT of one authorization, waits for the CA to validate it and clears it, whatever happens.
// An authorization that is already valid has no challenge. Clearing is best effort: it never fails an issuance.
func (a *ACME) solve(ctx context.Context, cl acmeAPI, authzURL string) error {
	chal, value, err := cl.DNS01(ctx, authzURL)
	if err != nil || chal == "" {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = a.TXT.ClearTXT(cctx)
	}()
	if err := a.TXT.SetTXT(ctx, value); err != nil {
		return err
	}
	return cl.Accept(ctx, chal, authzURL)
}

type accountFile struct {
	Key string `json:"key"`
}

// accountKey loads the account key, or creates and saves one.
func (a *ACME) accountKey() (*ecdsa.PrivateKey, error) {
	if data, err := os.ReadFile(a.AccountFile); err == nil {
		var f accountFile
		if json.Unmarshal(data, &f) == nil {
			raw, _ := base64.RawURLEncoding.DecodeString(f.Key)
			if k, err := x509.ParsePKCS8PrivateKey(raw); err == nil {
				if ek, ok := k.(*ecdsa.PrivateKey); ok {
					return ek, nil
				}
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(accountFile{Key: base64.RawURLEncoding.EncodeToString(der)})
	if err := os.MkdirAll(filepath.Dir(a.AccountFile), 0o700); err != nil {
		return nil, err
	}
	return key, file.WriteFileAtomic(a.AccountFile, data)
}

// acmeError words an error for the status; it never carries the TXT value, a key or a CSR (none is put in one).
func acmeError(err error) error {
	var ne net.Error
	var ue *url.Error
	switch {
	case strings.Contains(err.Error(), "rateLimited"):
		return errors.New("Let's Encrypt refused: its limit of new certificates is reached, try again in a few days")
	case errors.As(err, &ne) || errors.As(err, &ue):
		return fmt.Errorf("Let's Encrypt cannot be reached: %w", err)
	}
	return fmt.Errorf("ACME: %w", err)
}

// acmeClient is acmeAPI over the real client. A bad CA file is reported by Register.
type acmeClient struct {
	c   *acme.Client
	err error
}

func (a *ACME) realClient(key crypto.Signer) acmeAPI {
	dir := a.Directory
	if dir == "" {
		if dir = os.Getenv("CASAOS_SHARE_ACME_DIRECTORY"); dir == "" {
			dir = DefaultACMEDirectory
		}
	}
	c := &acmeClient{c: &acme.Client{Key: key, DirectoryURL: dir}}
	ca := a.CAFile
	if ca == "" {
		ca = os.Getenv("CASAOS_SHARE_ACME_CA")
	}
	if ca != "" {
		pool := x509.NewCertPool()
		if pem, err := os.ReadFile(ca); err != nil {
			c.err = err
		} else if !pool.AppendCertsFromPEM(pem) {
			c.err = errors.New("no PEM certificate in the CA file")
		}
		c.c.HTTPClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{RootCAs: pool}}}
	}
	return c
}

func (c *acmeClient) Register(ctx context.Context) error {
	if c.err != nil {
		return c.err
	}
	_, err := c.c.Register(ctx, &acme.Account{}, acme.AcceptTOS)
	if errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil
	}
	return err
}

func (c *acmeClient) Order(ctx context.Context, domain string) (string, []string, error) {
	o, err := c.c.AuthorizeOrder(ctx, acme.DomainIDs(domain))
	if err != nil {
		return "", nil, err
	}
	return o.URI, o.AuthzURLs, nil
}

func (c *acmeClient) DNS01(ctx context.Context, authzURL string) (string, string, error) {
	az, err := c.c.GetAuthorization(ctx, authzURL)
	if err != nil {
		return "", "", err
	}
	if az.Status == acme.StatusValid {
		return "", "", nil
	}
	for _, ch := range az.Challenges {
		if ch.Type == "dns-01" {
			v, err := c.c.DNS01ChallengeRecord(ch.Token)
			return ch.URI, v, err
		}
	}
	return "", "", errors.New("no dns-01 challenge offered")
}

func (c *acmeClient) Accept(ctx context.Context, challengeURI, authzURL string) error {
	if _, err := c.c.Accept(ctx, &acme.Challenge{Type: "dns-01", URI: challengeURI}); err != nil {
		return err
	}
	_, err := c.c.WaitAuthorization(ctx, authzURL)
	return err
}

func (c *acmeClient) Finalize(ctx context.Context, orderURI string, csr []byte) ([][]byte, error) {
	o, err := c.c.WaitOrder(ctx, orderURI)
	if err != nil {
		return nil, err
	}
	chain, _, err := c.c.CreateOrderCert(ctx, o.FinalizeURL, csr, true)
	if err != nil {
		// v0.57.0 polls the finalize answer's Location, which Pebble omits: poll the order we know.
		if o2, werr := c.c.WaitOrder(ctx, orderURI); werr == nil && o2.Status == acme.StatusValid && o2.CertURL != "" {
			return c.c.FetchCert(ctx, o2.CertURL, true)
		}
	}
	return chain, err
}
