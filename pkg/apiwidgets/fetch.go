package apiwidgets

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const (
	fetchTimeout = 5 * time.Second
	maxAnswer    = 256 << 10
)

var (
	errRefused  = failure("refused: this name leads to the box's loopback or a link-local address; use the box's address on the network")
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
	broadcast   = netip.AddrFrom4([4]byte{255, 255, 255, 255})
)

// failure is an error whose text was written here and may be shown as it is: it
// never quotes the address, a header, a secret or the answer.
type failure string

func (f failure) Error() string { return string(f) }

// quiet keeps an error from elsewhere for errors.Is and errors.As, and shows only
// its reason: the text of a network error names the address, and the query with it.
type quiet struct{ err error }

func (q quiet) Error() string { return reason(q.err) }
func (q quiet) Unwrap() error { return q.err }

// Is makes every failed read ErrRead, without a word added to its text.
func (q quiet) Is(target error) bool { return target == ErrRead }

// reachable says whether a widget may be read from a: anywhere but the box's
// loopback, where its own services listen and trust the caller. 0/8 and "::" lead
// to the loopback too. Link-local (169.254.169.254 included), multicast and
// broadcast are refused as well; the local network, Tailscale and the containers
// are allowed.
func reachable(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	return a.IsGlobalUnicast() && !thisNetwork.Contains(a) && a != broadcast
}

// guard is the rule of a real fetch.
func guard(ap netip.AddrPort) bool { return reachable(ap.Addr()) }

// dialControl refuses a connection to any address allowed refuses. It runs on the
// address the name resolved to, for every dial, so a name that resolves to the
// loopback is refused too.
func dialControl(allowed func(netip.AddrPort) bool) func(string, string, syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !allowed(ap) {
			return errRefused
		}
		return nil
	}
}

// newClient reads widgets from the addresses allowed lets through: no proxy (the
// guard would see the proxy's address), no redirect followed (a key never follows
// one), 5 s in all.
func newClient(insecureTLS bool, allowed func(netip.AddrPort) bool) *http.Client {
	dialer := &net.Dialer{Timeout: fetchTimeout, Control: dialControl(allowed)}
	return &http.Client{
		Timeout: fetchTimeout,
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            dialer.DialContext,
			TLSClientConfig:        &tls.Config{InsecureSkipVerify: insecureTLS},
			TLSHandshakeTimeout:    fetchTimeout,
			ResponseHeaderTimeout:  fetchTimeout,
			MaxResponseHeaderBytes: 64 << 10,
			// every fetch is a new, checked connection, and none is left open
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// secretOf gives the text of a value: its own, or the secret of its input.
func secretOf(v Value, secrets map[string]string) (string, error) {
	if v.Input == "" {
		return v.Text, nil
	}
	s, ok := secrets[v.Input]
	if !ok {
		return "", failure(fmt.Sprintf("the input %q is not filled", v.Input))
	}
	return s, nil
}

// requestURL puts the recipe's path and query after the owner's address.
func requestURL(base string, r Recipe, secrets map[string]string) (*url.URL, error) {
	b, err := url.Parse(base)
	if err != nil {
		return nil, failure("the address is not valid")
	}
	if b.Scheme != "http" && b.Scheme != "https" {
		return nil, failure("the address must start with http:// or https://")
	}
	host := strings.ToLower(strings.TrimSuffix(b.Hostname(), "."))
	if host == "" {
		return nil, failure("the address has no host")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, failure("refused: localhost is the box itself; use the box's address on the network")
	}
	if b.User != nil || b.RawQuery != "" || b.ForceQuery || b.Fragment != "" || b.RawFragment != "" {
		return nil, failure("the address must have no user, password, query or fragment")
	}
	// the joined path is checked: each half may be fine and the whole not
	path := strings.TrimSuffix(b.EscapedPath(), "/") + r.Path
	for _, p := range []string{r.Path, path} {
		if err := checkPath(p); err != nil {
			return nil, failure("the " + err.Error())
		}
	}
	u := &url.URL{Scheme: b.Scheme, Host: b.Host, RawPath: path}
	u.Path, _ = url.PathUnescape(path) // checkPath has unescaped it already
	// checkPath refused it already: a read never sends another path than the one written
	if u.EscapedPath() != path {
		return nil, failure("the path must be written percent-encoded")
	}

	query := url.Values{}
	for name, v := range r.Query {
		s, err := secretOf(v, secrets)
		if err != nil {
			return nil, err
		}
		query.Set(name, s)
	}
	u.RawQuery = query.Encode()
	return u, nil
}

// fetch reads the JSON answer of a widget's address: one GET, an answer in 2xx of
// 256 KiB at most. Its errors show their reason and nothing else.
func fetch(ctx context.Context, client *http.Client, base string, r Recipe, secrets map[string]string) (raw []byte, doc any, err error) {
	raw, doc, err = get(ctx, client, base, r, secrets)
	if err != nil {
		return nil, nil, quiet{err}
	}
	return raw, doc, nil
}

func get(ctx context.Context, client *http.Client, base string, r Recipe, secrets map[string]string) ([]byte, any, error) {
	u, err := requestURL(base, r, secrets)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "recasaos-widget/1")
	req.Header.Set("Accept", "application/json")
	if r.Basic != nil {
		user, err := secretOf(r.Basic.User, secrets)
		if err != nil {
			return nil, nil, err
		}
		password, err := secretOf(r.Basic.Password, secrets)
		if err != nil {
			return nil, nil, err
		}
		req.SetBasicAuth(user, password)
	}
	// after basic: an Authorization header the recipe writes itself wins
	for name, v := range r.Headers {
		s, err := secretOf(v, secrets)
		if err != nil {
			return nil, nil, err
		}
		if strings.ContainsAny(s, "\r\n\x00") {
			return nil, nil, failure("a header value holds a line break or a NUL character")
		}
		req.Header.Set(name, s)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, nil, failure(fmt.Sprintf("the address answered %d", resp.StatusCode))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > maxAnswer {
		return nil, nil, failure("the answer is over 256 KiB")
	}
	doc, err := decode(raw)
	if err != nil {
		return nil, nil, err
	}
	return raw, doc, nil
}

// decode reads an answer: one JSON value, its numbers kept as they are written.
func decode(raw []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var doc any
	if err := d.Decode(&doc); err != nil {
		return nil, failure("the answer is not JSON")
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, failure("the answer holds data after the JSON value")
	}
	return doc, nil
}

// reason says why a fetch failed, in a text that can be shown and kept: a text
// written here, or one of a few known causes. An error from elsewhere is never
// quoted, because it names the address (pkg/feeds quotes it: a feed has no secret).
func reason(err error) string {
	var (
		own     failure
		cert    *tls.CertificateVerificationError
		timeout net.Error
		dns     *net.DNSError
		errno   syscall.Errno
	)
	text := "the address could not be read"
	switch {
	case err == nil:
		return ""
	case errors.As(err, &own):
		text = string(own)
	case errors.As(err, &cert):
		text = "the certificate is not trusted"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		text = "no answer in time"
	case errors.As(err, &dns):
		text = "the name was not found"
	case errors.As(err, &errno):
		text = errno.Error() // "connection refused", "no route to host"
	}
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
