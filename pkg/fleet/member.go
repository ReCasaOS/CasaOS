package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// Over a relay: the relay, and whether this box is on its private network
	// (NetError says why not). Nothing repairs it: a stopped or logged-out box
	// joins again with a new code.
	Relay        string `json:"relay,omitempty"`
	NetConnected bool   `json:"net_connected,omitempty"`
	NetError     string `json:"net_error,omitempty"`
}

// memberState is what the loop reports, under its own mutex, and what the
// private network last said, under netMu.
type memberState struct {
	mu      sync.Mutex
	conn    *websocket.Conn
	since   time.Time
	lastErr string
	lastTry time.Time

	netMu    sync.Mutex
	netAt    time.Time
	netRelay string
	netOK    bool
	netErr   string
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

var errIsMaster = errors.New("this box is a master: it cannot join another fleet")

var errNoJoinKey = errors.New("this code was made while this box was online, so it carries no key to join the relay's private network, which this box is not on now: ask the master for a new code")

// netEvery is how long MemberStatus keeps what the private network said.
var netEvery = 5 * time.Second

// Join makes this box a member: the code is kept in member.json and the loop
// dials at once. A master does not join another fleet.
//
// A code that names a relay first joins the relay's private network with its
// join key (ErrOtherNetwork when the box is in another one, untouched), and
// member.json is written only once the box has an address there; on a failure
// after that, the network is left again and nothing is saved. The join key is
// spent: it is never kept.
func (f *Fleet) Join(code string) error {
	c, err := ParseCode(code)
	if err != nil {
		return err
	}
	st := MemberState{MasterURL: c.MasterURL, BoxID: c.BoxID, Key: c.Key, MasterCertSHA256: c.CertSHA256, Relay: c.Relay, FleetID: c.Fleet}
	if c.Relay == "" {
		// A member over a relay moving to a master with none leaves that relay's network.
		f.relayMu.Lock()
		defer f.relayMu.Unlock()
		f.mu.Lock()
		old, member := f.loadMember()
		_, on := f.master()
		rs, haveRelay := f.loadRelay()
		f.mu.Unlock()
		if haveRelay && remoteAlone(rs, on) {
			return ErrRemoteOn
		}
		if err := f.enrol(st); err != nil {
			return err
		}
		if member && old.Relay != "" {
			if n, nerr := f.PrivateNet(); nerr == nil {
				f.netDown(n, old.Relay)
			}
		}
		return nil
	}
	n, err := f.PrivateNet()
	if err != nil {
		return err
	}
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	f.mu.Lock()
	_, on := f.master()
	old, member := f.loadMember()
	rs, haveRelay := f.loadRelay()
	f.mu.Unlock()
	if on {
		return errIsMaster
	}
	if haveRelay && remoteAlone(rs, on) {
		return ErrRemoteOn
	}
	ctx := context.Background()
	ns, err := n.State(ctx)
	if c.JoinKey == "" { // made while this box was online: it must still be on the relay's network
		if err != nil || !ns.Connected || ns.Addr == "" || !sameURL(ns.LoginServer, c.Relay) {
			return errNoJoinKey
		}
		if member && old.FleetID != "" && c.Fleet != "" && old.FleetID != c.Fleet {
			return errNoJoinKey // this box is on another fleet's network: it needs a key to join this one
		}
		return f.enrol(st)
	}
	if err != nil {
		return err
	}
	// A box in a fleet of this relay, told to join another, leaves its own first:
	// Up takes a login to the same relay for the right one. The network is left
	// only if this box joined it (member.json, or the marker of a Join that did
	// not finish); anything else is the owner's, and Up refuses it.
	sameFleet := member && old.Relay != "" && sameURL(old.Relay, c.Relay) && c.Fleet != "" && old.FleetID == c.Fleet
	if !sameFleet {
		if relay, ours := f.ownNet(ns, old, member); ours {
			if err := n.Down(ctx, relay); err != nil {
				return fmt.Errorf("cannot leave the previous private network: %w", err)
			}
			f.clearNetOwn()
		} else if ns.LoginServer != "" {
			return ErrOtherNetwork
		}
	}
	if err := f.saveNetOwn(c.Relay); err != nil { // before Up: a crash after it never leaves a network nobody knows is ours
		return err
	}
	hostname, _ := f.Hostname()
	if err := n.Up(ctx, c.Relay, c.JoinKey, nodeName(c.BoxID, hostname)); err != nil {
		// Up undid itself, as far as it could: a Down here could log out a network
		// that is not ours. The marker stays unless Up refused to touch anything:
		// a logout it could not do is left for Leave and the next Join.
		if errors.Is(err, ErrOtherNetwork) {
			f.clearNetOwn()
			return err
		}
		return redact(err, c.JoinKey) // a Net that let the key into its error does not pass it on
	}
	if _, err := waitAddr(ctx, n); err != nil {
		f.netDown(n, c.Relay)
		return err
	}
	if err := f.enrol(st); err != nil {
		f.netDown(n, c.Relay)
		return err
	}
	f.clearNetOwn() // member.json says it from now on
	return nil
}

// ownNet says whether the network the box shows (ns, empty when tailscaled is
// stopped, hiding its login) is one this box joined as a member, and the relay
// to leave it by: the marker of a Join that did not finish, or member.json.
func (f *Fleet) ownNet(ns NetState, old MemberState, member bool) (string, bool) {
	var mine []string
	if r, ok := f.loadNetOwn(); ok {
		mine = append(mine, r)
	}
	if member && old.Relay != "" {
		mine = append(mine, old.Relay)
	}
	for _, r := range mine {
		if ns.LoginServer == "" || sameURL(ns.LoginServer, r) {
			return r, true
		}
	}
	return "", false
}

// enrol writes member.json and restarts the loop, unless this box is a master.
func (f *Fleet) enrol(st MemberState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, on := f.master(); on {
		return errIsMaster
	}
	if err := f.saveMember(st); err != nil {
		return err
	}
	f.m.drop()
	f.pokeMember()
	return nil
}

// netDown leaves relay's private network, best-effort and logged. The marker
// goes only when it names relay and the box left: if the box could not, the
// network is still ours to leave.
func (f *Fleet) netDown(n Net, relay string) {
	if err := n.Down(context.Background(), relay); err != nil {
		f.Log("fleet: cannot leave the relay's private network", err)
		return
	}
	if mine, ok := f.loadNetOwn(); ok && sameURL(mine, relay) {
		f.clearNetOwn()
	}
}

// Leave closes the tunnel and forgets the master. A member over a relay then
// leaves its private network, best-effort: the local leave never fails for it.
// It does so for the network of a Join that did not finish too (netown.json). It
// waits for a Join in progress (relayMu), never cutting one half-way.
func (f *Fleet) Leave() error {
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	f.mu.Lock()
	st, member := f.loadMember()
	err := f.clearMember()
	f.mu.Unlock()
	f.m.drop()
	f.pokeMember()
	if err != nil {
		return err
	}
	// The member's relay and the one of a Join that did not finish may differ.
	relays := []string{}
	if member && st.Relay != "" {
		relays = append(relays, st.Relay)
	}
	if mine, ok := f.loadNetOwn(); ok && (len(relays) == 0 || !sameURL(mine, relays[0])) {
		relays = append(relays, mine)
	}
	if len(relays) > 0 {
		if n, nerr := f.PrivateNet(); nerr == nil {
			for _, r := range relays {
				f.netDown(n, r)
			}
		}
	}
	return nil
}

// nodeName is the name a member takes on the relay's private network:
// "b-<box id>", which the master removes it by (RemoveBox), unique in the fleet
// whatever the boxes' hostnames. A box id that is no short DNS label (a code
// the master did not make) falls back to the hostname.
func nodeName(boxID, hostname string) string {
	if !validBoxID(boxID) {
		return netHostname(hostname)
	}
	return "b-" + boxID
}

// validBoxID: a box id as newID makes it (hex), at most 40 characters.
func validBoxID(id string) bool {
	if id == "" || len(id) > 40 {
		return false
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// netHostname is a hostname as a DNS label: the name the master takes on the
// relay's private network.
func netHostname(h string) string {
	b := []byte(strings.ToLower(h))
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			b[i] = '-'
		}
	}
	s := strings.Trim(string(b), "-")
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	if s == "" {
		return "recasaos-box"
	}
	return s
}

// netStatus is what MemberStatus or MasterStatus says of the private network of
// a box over relay, asked at most every netEvery; off is what it says when the
// box is not on it. The caller holds f.m.netMu.
func (f *Fleet) netStatus(relay, off string) (bool, string) {
	now := f.Now()
	if !f.m.netAt.IsZero() && now.Sub(f.m.netAt) < netEvery && f.m.netRelay == relay {
		return f.m.netOK, f.m.netErr
	}
	ok, msg := false, ""
	if n, err := f.PrivateNet(); err != nil {
		msg = err.Error()
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ns, err := n.State(ctx)
		cancel()
		switch {
		case err != nil:
			msg = "the private network cannot be read: " + err.Error()
		case !ns.Connected:
			msg = off
		case !sameURL(ns.LoginServer, relay):
			msg = "this box is in another private network than its fleet's relay"
		case ns.RelayUnreachable:
			msg = "the relay does not answer: boxes already linked may keep reaching each other, but no box can join and no code can be given until it is back"
		default:
			ok = true
		}
	}
	f.m.netAt, f.m.netRelay, f.m.netOK, f.m.netErr = now, relay, ok, msg
	return ok, msg
}

// MemberLink is the master's address and this box's id there, for a link into the master. ok is false when
// the box is no member, or when the master is this box itself (a loopback address). ok is also false
// for a relay member, whose master_url is a private-network address a phone cannot reach.
func (f *Fleet) MemberLink() (masterURL, boxID string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.loadMember()
	if !ok || st.Relay != "" || f.namesSelf(st) {
		return "", "", false
	}
	return st.MasterURL, st.BoxID, true
}

// MemberStatus is the member's view: ok is false when the role is off.
func (f *Fleet) MemberStatus() (MemberStatus, bool) {
	f.mu.Lock()
	st, ok := f.loadMember()
	f.mu.Unlock()
	if !ok {
		return MemberStatus{}, false
	}
	out := MemberStatus{MasterURL: st.MasterURL, Relay: st.Relay}
	if st.Relay != "" {
		f.m.netMu.Lock() // not f.m.mu: the loop reports under it, and State may be slow
		out.NetConnected, out.NetError = f.netStatus(st.Relay, "this box is off the relay's private network: join again with a new code from the master")
		f.m.netMu.Unlock()
	}
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	out.Connected, out.LastError, out.LastTry = f.m.conn != nil, f.m.lastErr, f.m.lastTry
	if out.Connected {
		out.Since = f.m.since
	}
	return out, true
}

// Run is the member loop, started once by main: while member.json exists, keep
// a tunnel to the master and serve what it relays; otherwise wait for a code.
func (f *Fleet) Run(ctx context.Context) {
	go f.watchOffline(ctx)
	go f.watchBackups(ctx)
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
	var scheme, port string
	if gw, err := f.Gateway(); err == nil {
		scheme, port = webAddr(gw.Public)
	}
	if err := conn.WriteJSON(frame{T: "hello", Hostname: cut(hostname), Version: cut(f.Version()), WebScheme: scheme, WebPort: port}); err != nil {
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
				// A poke means member.json may have changed. Restart only when
				// it actually did: the poke that started this very session (its
				// own enrolment) names the same master, and tearing the fresh
				// tunnel down for it is the self-enrol flake. A Join or a Leave
				// names something else, and must restart at once.
				if now, ok := f.loadMember(); !ok || now != st {
					out = restarted
					conn.Close()
					return
				}
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
