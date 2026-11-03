package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/gorilla/websocket"
)

// The member's backoff: doubling from firstWait to maxWait, ±20 %; a master
// that refused the key (401) is tried again every refusedWait only.
var (
	firstWait   = 5 * time.Second
	maxWait     = 5 * time.Minute
	refusedWait = 10 * time.Minute
)

const refusedByMaster = "refused by the master"

// MemberStatus is the member half of GET /v1/fleet.
type MemberStatus struct {
	MasterURL string    `json:"master_url"`
	Connected bool      `json:"connected"`
	Since     time.Time `json:"since,omitzero"`
	LastError string    `json:"last_error,omitempty"`
	LastTry   time.Time `json:"last_try,omitzero"`
}

// memberState is what the loop reports, under its own mutex.
type memberState struct {
	mu      sync.Mutex
	conn    *websocket.Conn
	since   time.Time
	lastErr string
	lastTry time.Time
}

func (m *memberState) trying(now time.Time) {
	m.mu.Lock()
	m.lastTry = now
	m.mu.Unlock()
}

func (m *memberState) failed(msg string) {
	m.mu.Lock()
	m.conn, m.lastErr = nil, msg
	m.mu.Unlock()
}

func (m *memberState) connected(conn *websocket.Conn, now time.Time, gwErr error) {
	m.mu.Lock()
	m.conn, m.since, m.lastErr = conn, now, ""
	if gwErr != nil {
		m.lastErr = gwErr.Error()
	}
	m.mu.Unlock()
}

func (m *memberState) disconnected(conn *websocket.Conn) {
	m.mu.Lock()
	if m.conn == conn {
		m.conn = nil
	}
	m.mu.Unlock()
}

// drop cuts the live connection, if any; the loop sees it and decides what next.
func (m *memberState) drop() {
	m.mu.Lock()
	if m.conn != nil {
		m.conn.Close()
	}
	m.lastErr = ""
	m.mu.Unlock()
}

// Join makes this box a member: the code is kept in member.json and the loop
// dials at once. A master does not join another fleet.
func (f *Fleet) Join(code string) error {
	c, err := ParseCode(code)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, on := f.master(); on {
		return errors.New("this box is a master: it cannot join another fleet")
	}
	if err := f.saveMember(MemberState{MasterURL: c.MasterURL, BoxID: c.BoxID, Key: c.Key, MasterCertSHA256: c.CertSHA256}); err != nil {
		return err
	}
	f.m.drop()
	f.pokeMember()
	return nil
}

// Leave closes the tunnel and forgets the master.
func (f *Fleet) Leave() error {
	f.mu.Lock()
	err := f.clearMember()
	f.mu.Unlock()
	f.m.drop()
	f.pokeMember()
	return err
}

// MemberStatus is the member's view: ok is false when the role is off.
func (f *Fleet) MemberStatus() (MemberStatus, bool) {
	f.mu.Lock()
	st, ok := f.loadMember()
	f.mu.Unlock()
	if !ok {
		return MemberStatus{}, false
	}
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	out := MemberStatus{MasterURL: st.MasterURL, Connected: f.m.conn != nil, LastError: f.m.lastErr, LastTry: f.m.lastTry}
	if out.Connected {
		out.Since = f.m.since
	}
	return out, true
}

// Run is the member loop, started once by main: while member.json exists, keep
// a tunnel to the master and serve what it relays; otherwise wait for a code.
func (f *Fleet) Run(ctx context.Context) {
	go f.watchOffline(ctx)
	wait := firstWait
	for {
		f.mu.Lock()
		st, ok := f.loadMember()
		f.mu.Unlock()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-f.restart:
			}
			wait = firstWait
			continue
		}
		// Drain the poke that brought us here: this session is the answer to it,
		// so the watcher inside must only react to a change made while it runs.
		select {
		case <-f.restart:
		default:
		}
		var d time.Duration
		switch f.session(ctx, st) {
		case restarted:
			wait = firstWait
			continue
		case refused:
			d = refusedWait
		case served:
			wait, d = firstWait, firstWait
		default:
			d = wait
			wait = min(wait*2, maxWait)
		}
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(jitter(d))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-f.restart:
			timer.Stop()
			wait = firstWait
		case <-timer.C:
		}
	}
}

// How one session of the member loop ended.
type outcome int

const (
	failed    outcome = iota // the dial failed
	refused                  // the master said 401: the key is worth nothing now
	served                   // connected, then the connection went
	restarted                // member.json changed under it: dial again at once
)

// session dials the master, says hello and serves its requests until the
// connection goes.
func (f *Fleet) session(ctx context.Context, st MemberState) outcome {
	f.m.trying(f.Now())
	conn, resp, err := f.dial(ctx, st)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			f.m.failed(refusedByMaster)
			return refused
		}
		f.m.failed(err.Error())
		return failed
	}
	defer conn.Close()
	hostname, _ := f.Hostname()
	conn.SetReadLimit(maxFrame)
	conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := conn.WriteJSON(frame{T: "hello", Hostname: cut(hostname), Version: cut(f.Version())}); err != nil {
		f.m.failed(err.Error())
		return failed
	}
	info, gwErr := f.Gateway()
	if gwErr == nil && info.Unreachable != "" {
		gwErr = errors.New(info.Unreachable)
	}
	f.m.connected(conn, f.Now(), gwErr)
	defer f.m.disconnected(conn)

	out := served
	stop := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		tick := time.NewTicker(pingEvery)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				conn.Close()
				return
			case <-f.restart:
				out = restarted
				conn.Close()
				return
			case <-tick.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
					conn.Close()
					return
				}
			}
		}
	}()
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(readWait)) })
	var writeMu sync.Mutex
	var handlers sync.WaitGroup
	sem := make(chan struct{}, maxInFlight)
	for {
		conn.SetReadDeadline(time.Now().Add(readWait))
		var req frame
		if err := conn.ReadJSON(&req); err != nil {
			break
		}
		if req.T != "req" {
			continue
		}
		select {
		case sem <- struct{}{}:
		default:
			writeFrame(conn, &writeMu, answer(req, problem(http.StatusServiceUnavailable, ErrBusy.Error())))
			continue
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer func() { <-sem }()
			writeFrame(conn, &writeMu, answer(req, f.execute(req)))
		}()
	}
	close(stop)
	conn.Close()
	watcher.Wait()
	handlers.Wait()
	return out
}

// dial opens the tunnel: wss (ws for a loopback http master), the box's key in
// the header, the master's certificate verified by chain or by the code's pin.
func (f *Fleet) dial(ctx context.Context, st MemberState) (*websocket.Conn, *http.Response, error) {
	u, err := url.Parse(st.MasterURL)
	if err != nil {
		return nil, nil, err
	}
	serverName := u.Hostname()
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/v1/fleet/tunnel"
	// Self-enrolled: the code points at this box's own loopback, but a real
	// certificate there is for the master's address, which is the name to verify.
	if loopbackHost(serverName) {
		f.mu.Lock()
		ms, on := f.master()
		f.mu.Unlock()
		if mu, err := url.Parse(ms.Address); on && err == nil && mu.Hostname() != "" {
			serverName = mu.Hostname()
		}
	}
	d := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 15 * time.Second, TLSClientConfig: tlsConfig(serverName, st.MasterCertSHA256)}
	h := http.Header{"Authorization": {"Fleet " + st.BoxID + ":" + st.Key}}
	return d.DialContext(ctx, u.String(), h)
}

// execute runs one relayed request against this box's own gateway, with the
// internal identity, and never anything outside the table. The gateway is
// read fresh for each request: the web UI port can change under a running box.
func (f *Fleet) execute(req frame) frame {
	slow, ok := Allowed(req.Method, req.Path)
	if !ok {
		return problem(http.StatusForbidden, ErrNotAllowed.Error())
	}
	gw, err := f.Gateway()
	if err != nil {
		return problem(http.StatusBadGateway, err.Error())
	}
	if gw.Unreachable != "" {
		return problem(http.StatusBadGateway, gw.Unreachable)
	}
	if len(req.Body) > MaxBody {
		return problem(http.StatusRequestEntityTooLarge, "the request body is too big to relay")
	}
	// Build the URL from its parts, never by joining strings: Allowed has
	// already refused a path with a delimiter or an escape in it, and the
	// query is re-encoded here, so nothing in the frame can graft on a
	// second route or a query the member did not mean to send.
	target := *gw.Public
	target.Path = req.Path
	target.RawQuery = stripToken(req.Query)
	ctx, cancel := context.WithTimeout(context.Background(), timeouts(slow))
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, req.Method, target.String(), bytes.NewReader(req.Body))
	if err != nil {
		return problem(http.StatusBadRequest, "the relayed request is malformed")
	}
	if auth := external.InternalAuthorization(f.RuntimePath()); auth != "" {
		hreq.Header.Set("Authorization", auth)
	}
	for _, h := range []string{"Content-Type", "Accept"} {
		if v := req.Headers[h]; v != "" {
			hreq.Header.Set(h, v)
		}
	}
	resp, err := f.local.Do(hreq)
	if err != nil {
		return problem(http.StatusBadGateway, "this box's gateway did not answer: "+err.Error())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return problem(http.StatusBadGateway, "this box's gateway did not answer: "+err.Error())
	}
	if len(body) > MaxBody {
		return problem(http.StatusBadGateway, "the answer is too big to relay")
	}
	return frame{Status: resp.StatusCode, Headers: map[string]string{"Content-Type": resp.Header.Get("Content-Type")}, Body: body}
}

// problem is an answer the member makes itself, in the envelope's shape.
func problem(status int, msg string) frame {
	body, _ := json.Marshal(map[string]string{"message": msg})
	return frame{Status: status, Headers: map[string]string{"Content-Type": "application/json"}, Body: body}
}

func answer(req, res frame) frame {
	res.T, res.ID = "res", req.ID
	return res
}

func writeFrame(conn *websocket.Conn, mu *sync.Mutex, fr frame) {
	mu.Lock()
	defer mu.Unlock()
	conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := conn.WriteJSON(fr); err != nil {
		conn.Close()
	}
}

func jitter(d time.Duration) time.Duration {
	return d + time.Duration((rand.Float64()*0.4-0.2)*float64(d))
}
