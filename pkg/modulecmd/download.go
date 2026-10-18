package modulecmd

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The download of the archive of a module: over https from github.com, which sends the file on from objects.githubusercontent.com or release-assets.githubusercontent.com (where it keeps the
// assets of a release now), and from nowhere else; at most 200 MiB; the bytes are
// checked against the sha256 of the catalogue afterwards (that is the proof that it is the archive, this is only a fence around what is fetched).

var (
	// downloadHosts are the hosts a request may go to, the first and every one it is sent on to.
	downloadHosts = []string{"github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"}
	// maxRedirects is the most a request is sent on.
	maxRedirects = 5
	// downloadTries is how often an archive is fetched before the install gives up, and the waits between.
	downloadTries = 3
	// downloadTryTime is the time one try has.
	downloadTryTime = 15 * time.Minute

	// ErrTooLarge is a download or an unpacked archive that has more bytes than allowed.
	ErrTooLarge = errors.New("more bytes than allowed")
)

// checkURL says why an address may not be fetched: it is https, from one of the hosts, with no user and no port but 443.
func checkURL(u *url.URL) error {
	switch {
	case u.Scheme != "https":
		return errors.New("not https")
	case u.User != nil:
		return errors.New("an address with a user")
	case u.Port() != "" && u.Port() != "443":
		return errors.New("a port")
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range downloadHosts {
		if host == h {
			return nil
		}
	}
	return errors.New("a host that is not allowed")
}

// redirectPolicy lets a request be sent on to the allowed hosts over https, a few times.
func redirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return &PermanentError{Err: errors.New("too many redirects")}
	}
	if err := checkURL(req.URL); err != nil {
		return &PermanentError{Err: fmt.Errorf("redirect refused: %w", err)}
	}
	return nil
}

// newHTTPClient is the client of the download: the transport it is given (nil is the default one with TLS 1.2 at least), and the redirect policy.
func newHTTPClient(rt http.RoundTripper) *http.Client {
	if rt == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		rt = t
	}
	return &http.Client{Transport: rt, CheckRedirect: redirectPolicy}
}

type httpDownloader struct {
	client *http.Client
	max    int64
}

// NewHTTPDownloader is the downloader of production: the archive of a release, at most osmodule.MaxArchiveBytes.
func NewHTTPDownloader() Downloader {
	return httpDownloader{client: newHTTPClient(nil), max: osmodule.MaxArchiveBytes}
}

func (d httpDownloader) Get(ctx context.Context, rawURL string, dst io.Writer) (int64, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, &PermanentError{Err: errors.New("not an address")}
	}
	if err := checkURL(u); err != nil {
		return 0, &PermanentError{Err: err}
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTryTime)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, &PermanentError{Err: err}
	}
	resp, err := d.client.Do(req)
	if err != nil {
		var perm *PermanentError
		if errors.As(err, &perm) {
			return 0, perm
		}
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("status %d", resp.StatusCode)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
			return 0, &PermanentError{Err: err}
		}
		return 0, err
	}
	if resp.ContentLength > d.max {
		return 0, &PermanentError{Err: ErrTooLarge}
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, d.max+1))
	switch {
	case err != nil:
		return n, err
	case n > d.max:
		return n, &PermanentError{Err: ErrTooLarge}
	}
	return n, nil
}

// fileDownloader serves a local file in place of the address: the stand-in of the test mode.
type fileDownloader struct {
	path string
	max  int64
}

func (d fileDownloader) Get(_ context.Context, _ string, dst io.Writer) (int64, error) {
	f, err := os.Open(d.path)
	if err != nil {
		return 0, &PermanentError{Err: err}
	}
	defer f.Close()
	n, err := io.Copy(dst, io.LimitReader(f, d.max+1))
	if err == nil && n > d.max {
		err = &PermanentError{Err: ErrTooLarge}
	}
	return n, err
}

// downloaderOf is the downloader of a run: the Env's, or in the test mode, when CASAOS_MODULE_ARCHIVE_FILE names a file, the file.
func downloaderOf(env Env) Downloader {
	if env.testMode() {
		if p := env.Getenv("CASAOS_MODULE_ARCHIVE_FILE"); p != "" {
			return fileDownloader{path: p, max: osmodule.MaxArchiveBytes}
		}
	}
	return env.Download
}

// ctxReader ends a read when the context is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// sha256File is the sha256 of a file, in lower case hex.
func sha256File(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx, f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// gunzipTo writes the decompressed content of the gzip file src to dst, at most max bytes: the archive of a module is a few megabytes of a tar that is never more than max, and a
// file that decompresses to more is a bomb and stops at the cap. ErrTooLarge when it does; a file that is not gzip is another error.
func gunzipTo(ctx context.Context, src, dst string, max int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	zr, err := gzip.NewReader(ctxReader{ctx, in})
	if err != nil {
		return err
	}
	defer zr.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(ctxReader{ctx, zr}, max+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > max {
		err = ErrTooLarge
	}
	return err
}
