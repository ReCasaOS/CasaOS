package share

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

// fakeTunnelRelay is the relay's end of the share tunnel, for tests: an httptest server that verifies the
// X-Box-* headers of the upgrade, upgrades to a WebSocket and runs yamux as the client (the relay opens
// the streams). Next returns each accepted tunnel as a yamux client session; CloseWith and Drop end the
// latest tunnel from the relay side. gate, when not nil, sees every request first and returns true once
// it has answered (to refuse the upgrade with a status).
type fakeTunnelRelay struct {
	URL string // http://127.0.0.1:port, what Dial takes

	t        *testing.T
	gate     func(w http.ResponseWriter, r *http.Request) bool
	upgrades atomic.Int32
	sessions chan *yamux.Session

	mu   sync.Mutex
	last *websocket.Conn
	all  []*websocket.Conn
}

func newFakeRelay(t *testing.T, gate func(w http.ResponseWriter, r *http.Request) bool) *fakeTunnelRelay {
	t.Helper()
	f := &fakeTunnelRelay{t: t, gate: gate, sessions: make(chan *yamux.Session, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc(TunnelPath, f.handle)
	srv := httptest.NewServer(mux)
	f.URL = srv.URL
	t.Cleanup(func() {
		srv.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, ws := range f.all {
			ws.Close()
		}
	})
	return f
}

func (f *fakeTunnelRelay) handle(w http.ResponseWriter, r *http.Request) {
	if f.gate != nil && f.gate(w, r) {
		return
	}
	if err := verifyBoxRequest(r, time.Now(), http.MethodGet, TunnelPath); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ws, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	y, err := yamux.Client(newWSConn(ws), cfg)
	if err != nil {
		ws.Close()
		return
	}
	f.mu.Lock()
	f.last = ws
	f.all = append(f.all, ws)
	f.mu.Unlock()
	f.upgrades.Add(1)
	f.sessions <- y
}

// Upgrades is the number of tunnels the relay accepted.
func (f *fakeTunnelRelay) Upgrades() int { return int(f.upgrades.Load()) }

// Next waits for the next accepted tunnel and returns the relay's yamux client session on it.
func (f *fakeTunnelRelay) Next() *yamux.Session {
	f.t.Helper()
	select {
	case y := <-f.sessions:
		f.t.Cleanup(func() { y.Close() })
		return y
	case <-time.After(5 * time.Second):
		f.t.Fatal("the fake relay received no tunnel")
		return nil
	}
}

// CloseWith closes the latest tunnel with a WebSocket close code (4403, 4409, 1001...).
func (f *fakeTunnelRelay) CloseWith(code int) {
	f.mu.Lock()
	ws := f.last
	f.mu.Unlock()
	ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, ""), time.Now().Add(time.Second))
}

// Drop cuts the latest tunnel's TCP connection with no close frame.
func (f *fakeTunnelRelay) Drop() {
	f.mu.Lock()
	ws := f.last
	f.mu.Unlock()
	ws.UnderlyingConn().Close()
}

// verifyBoxRequest is the rule of pkg/fleet/relayapi_test.go, copied.
func verifyBoxRequest(r *http.Request, now time.Time, method, path string) error {
	enc := base64.RawURLEncoding.Strict()
	if r.Method != method || r.URL.Path != path {
		return errors.New("not the signed request expected")
	}
	pub, err := enc.DecodeString(r.Header.Get("X-Box-Key"))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("bad X-Box-Key")
	}
	sig, err := enc.DecodeString(r.Header.Get("X-Box-Signature"))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("bad X-Box-Signature")
	}
	tsStr := r.Header.Get("X-Box-Time")
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return errors.New("bad X-Box-Time")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > 120*time.Second || d < -120*time.Second {
		return errors.New("time outside the window")
	}
	if !ed25519.Verify(pub, []byte(method+" "+path+"\n"+tsStr), sig) {
		return errors.New("bad signature")
	}
	return nil
}

// echoGateway stands for the Gateway's share listener: it echoes every connection and counts them.
type echoGateway struct {
	Addr string

	active, max atomic.Int32
	mu          sync.Mutex
	conns       []net.Conn
}

func newEchoGateway(t *testing.T) *echoGateway {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := &echoGateway{Addr: l.Addr().String()}
	t.Cleanup(func() {
		l.Close()
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, c := range g.conns {
			c.Close()
		}
	})
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			g.mu.Lock()
			g.conns = append(g.conns, c)
			g.mu.Unlock()
			n := g.active.Add(1)
			for m := g.max.Load(); n > m && !g.max.CompareAndSwap(m, n); m = g.max.Load() {
			}
			go func() {
				io.Copy(c, c)
				c.Close()
				g.active.Add(-1)
			}()
		}
	}()
	return g
}

func testKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// serveAsync runs Serve and delivers its result.
func serveAsync(ctx context.Context, s *Session, addr string) <-chan int {
	ch := make(chan int, 1)
	go func() { ch <- Serve(ctx, s, addr) }()
	return ch
}

func waitCode(t *testing.T, ch <-chan int) int {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
		return -1
	}
}

func TestDialSignsTheUpgrade(t *testing.T) {
	key := testKey(t)

	f := newFakeRelay(t, nil)
	s, err := Dial(context.Background(), f.URL+"/", key, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f.Next()
	if f.Upgrades() != 1 {
		t.Fatalf("upgrades = %d, want 1", f.Upgrades())
	}

	// A relay that rejects the signature answers 401.
	bad := newFakeRelay(t, func(w http.ResponseWriter, r *http.Request) bool {
		if verifyBoxRequest(r, time.Now().Add(time.Hour), http.MethodGet, TunnelPath) != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return true
		}
		return false
	})
	_, err = Dial(context.Background(), bad.URL, key, nil)
	var de *DialError
	if !errors.As(err, &de) || de.StatusCode != 401 || de.RetryAfter != 0 {
		t.Fatalf("err = %v, want DialError 401", err)
	}

	busy := newFakeRelay(t, func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Retry-After", "30")
		http.Error(w, "slow down", http.StatusTooManyRequests)
		return true
	})
	_, err = Dial(context.Background(), busy.URL, key, nil)
	if !errors.As(err, &de) || de.StatusCode != 429 || de.RetryAfter != 30*time.Second {
		t.Fatalf("err = %v, want DialError 429 with 30 s", err)
	}

	// Nobody there: no HTTP answer.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	_, err = Dial(context.Background(), "http://"+addr, key, nil)
	if !errors.As(err, &de) || de.StatusCode != 0 {
		t.Fatalf("err = %v, want DialError 0", err)
	}

	if _, err = Dial(context.Background(), "ftp://x", key, nil); err == nil {
		t.Fatal("a scheme other than http(s) must be refused")
	}
}

func TestServePipesStreamsToTheGateway(t *testing.T) {
	gw := newEchoGateway(t)
	f := newFakeRelay(t, nil)
	s, err := Dial(context.Background(), f.URL, testKey(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	done := serveAsync(context.Background(), s, gw.Addr)
	relay := f.Next()

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := relay.Open()
			if err != nil {
				errs <- err
				return
			}
			defer st.Close()
			want := fmt.Sprintf("ping-%d", i)
			st.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := st.Write([]byte(want)); err != nil {
				errs <- err
				return
			}
			got := make([]byte, len(want))
			if _, err := io.ReadFull(st, got); err != nil || string(got) != want {
				errs <- fmt.Errorf("stream %d: got %q, %v", i, got, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	relay.Close() // a normal closure
	if c := waitCode(t, done); c != 0 {
		t.Fatalf("Serve = %d, want 0", c)
	}
}

func TestServeCapsStreams(t *testing.T) {
	gw := newEchoGateway(t)
	f := newFakeRelay(t, nil)
	s, err := Dial(context.Background(), f.URL, testKey(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	done := serveAsync(context.Background(), s, gw.Addr)
	relay := f.Next()

	echoed, closed := 0, 0
	for i := 0; i < 300; i++ {
		st, err := relay.Open()
		if err != nil {
			t.Fatal(err)
		}
		st.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := st.Write([]byte{'x'}); err != nil {
			t.Fatal(err)
		}
		// An answered stream gets its byte back; a refused one is closed by the box.
		b := make([]byte, 1)
		switch _, err := io.ReadFull(st, b); err {
		case nil:
			echoed++
		case io.EOF:
			closed++
		default:
			t.Fatalf("stream %d: %v", i, err)
		}
	}
	if echoed != maxStreams || closed != 300-maxStreams {
		t.Fatalf("echoed %d, closed %d; want %d and %d", echoed, closed, maxStreams, 300-maxStreams)
	}
	if m := gw.max.Load(); m != maxStreams {
		t.Fatalf("the gateway saw %d at once, want %d", m, maxStreams)
	}

	relay.Close()
	waitCode(t, done) // every stream goroutine ended with the session
}

func TestServeStopsWithItsContext(t *testing.T) {
	gw := newEchoGateway(t)
	f := newFakeRelay(t, nil)
	s, err := Dial(context.Background(), f.URL, testKey(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := serveAsync(ctx, s, gw.Addr)
	f.Next()
	cancel()
	waitCode(t, done)
}

func TestServeFailedGatewayDialClosesOnlyThatStream(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := l.Addr().String()
	l.Close()
	f := newFakeRelay(t, nil)
	s, err := Dial(context.Background(), f.URL, testKey(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	done := serveAsync(context.Background(), s, dead)
	relay := f.Next()
	for i := 0; i < 2; i++ { // the second stream proves the tunnel survived the first
		st, err := relay.Open()
		if err != nil {
			t.Fatal(err)
		}
		st.SetDeadline(time.Now().Add(5 * time.Second))
		st.Write([]byte("x"))
		if _, err := io.ReadFull(st, make([]byte, 1)); err != io.EOF {
			t.Fatalf("stream %d: %v, want EOF", i, err)
		}
	}
	relay.Close()
	waitCode(t, done)
}

func TestCloseCodesAreReported(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(f *fakeTunnelRelay)
		want int
	}{
		{"revoked", func(f *fakeTunnelRelay) { f.CloseWith(4403) }, 4403},
		{"replaced", func(f *fakeTunnelRelay) { f.CloseWith(4409) }, 4409},
		{"shutdown", func(f *fakeTunnelRelay) { f.CloseWith(1001) }, 1001},
		{"dropped", func(f *fakeTunnelRelay) { f.Drop() }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw := newEchoGateway(t)
			f := newFakeRelay(t, nil)
			s, err := Dial(context.Background(), f.URL, testKey(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			done := serveAsync(context.Background(), s, gw.Addr)
			f.Next()
			tc.end(f)
			if c := waitCode(t, done); c != tc.want {
				t.Fatalf("Serve = %d, want %d", c, tc.want)
			}
			if c := s.CloseCode(); c != tc.want {
				t.Fatalf("CloseCode = %d, want %d", c, tc.want)
			}
		})
	}
}
