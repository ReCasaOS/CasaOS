package share

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

// TunnelPath is the relay route a box dials to publish its apps.
const TunnelPath = "/v1/share/tunnel"

const maxStreams = 256 // simultaneous visitor connections served

// DialError is a refused upgrade (401, 403, 429, 503...). StatusCode is 0 when the relay did not answer;
// RetryAfter comes from a 429's Retry-After header, 0 otherwise.
type DialError struct {
	StatusCode int
	RetryAfter time.Duration
	Err        error
}

func (e *DialError) Error() string {
	return fmt.Sprintf("share tunnel: dial: HTTP %d: %v", e.StatusCode, e.Err)
}
func (e *DialError) Unwrap() error { return e.Err }

// Session is the box end of a tunnel. The relay opens the streams, so there is no Open: the relay drops
// the tunnel of a box that opens one.
type Session struct {
	y *yamux.Session
	c *wsConn
}

// Dial opens the tunnel to relayURL (https://host, or http:// in tests), signing GET TunnelPath with key.
// d nil means a dialer with a 15 s handshake timeout that honours the proxy environment.
func Dial(ctx context.Context, relayURL string, key ed25519.PrivateKey, d *websocket.Dialer) (*Session, error) {
	u, err := url.Parse(relayURL)
	if err != nil {
		return nil, fmt.Errorf("share tunnel: bad relay address: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return nil, fmt.Errorf("share tunnel: bad relay address: scheme %q", u.Scheme)
	}
	if d == nil {
		d = &websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	enc := base64.RawURLEncoding
	h := http.Header{
		"X-Box-Key":       {enc.EncodeToString(key.Public().(ed25519.PublicKey))},
		"X-Box-Time":      {ts},
		"X-Box-Signature": {enc.EncodeToString(ed25519.Sign(key, []byte(http.MethodGet+" "+TunnelPath+"\n"+ts)))},
	}
	ws, resp, err := d.DialContext(ctx, strings.TrimSuffix(u.String(), "/")+TunnelPath, h)
	if err != nil {
		de := &DialError{Err: err}
		if resp != nil {
			de.StatusCode = resp.StatusCode
			if resp.StatusCode == http.StatusTooManyRequests {
				if n, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && n > 0 {
					de.RetryAfter = time.Duration(n) * time.Second
				}
			}
		}
		return nil, de
	}
	c := newWSConn(ws)
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 30 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.LogOutput = io.Discard
	y, err := yamux.Server(c, cfg) // the relay opens the streams, so the box is the yamux server
	if err != nil {
		c.Close()
		return nil, err
	}
	return &Session{y: y, c: c}, nil
}

// Accept returns the next visitor stream; it fails once the tunnel is closed.
func (s *Session) Accept() (net.Conn, error) {
	st, err := s.y.Accept()
	if err != nil {
		return nil, err
	}
	return st, nil
}

// Close ends the tunnel and its streams.
func (s *Session) Close() error { return s.y.Close() }

// CloseCode is the code the relay closed the tunnel with: 4403 revoked, 4409 replaced, 1001 shutdown,
// 0 otherwise (network drop, local close, normal closure).
func (s *Session) CloseCode() int { return s.c.CloseCode() }

// Serve copies every accepted stream to a fresh TCP connection to shareAddr, at most maxStreams at a time
// (the others are closed at once), until the session ends or ctx is done. It returns the close code.
func Serve(ctx context.Context, s *Session, shareAddr string) int {
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { s.Close() })
	defer stop()

	sem := make(chan struct{}, maxStreams)
	var wg sync.WaitGroup
	for {
		st, err := s.Accept()
		if err != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		default:
			st.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			pipe(dctx, st, shareAddr)
		}()
	}
	cancel() // a stream still dialling gives up
	s.Close()
	wg.Wait()
	// The close frame may still be on its way to being recorded: 0 would read as a drop, not a revocation.
	select {
	case <-s.c.readDone:
	case <-time.After(2 * time.Second):
	}
	return s.CloseCode()
}

// pipe copies st and a fresh connection to addr both ways. A failed dial closes only this stream.
func pipe(ctx context.Context, st net.Conn, addr string) {
	defer st.Close()
	d := net.Dialer{Timeout: 10 * time.Second}
	g, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return
	}
	defer g.Close()
	done := make(chan struct{})
	go func() { // visitor to gateway: at its end, tell the gateway no more is coming
		defer close(done)
		io.Copy(g, st)
		if tc, ok := g.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()
	io.Copy(st, g)                 // gateway to visitor
	st.Close()                     // yamux streams have no half-close
	st.SetReadDeadline(time.Now()) // wakes the visitor-to-gateway copy: the slot must not wait for the visitor
	g.Close()
	<-done
}
