package fleet

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Response is what a box answered to a relayed request, passed on as it is.
type Response struct {
	Status      int
	ContentType string
	Body        []byte
}

var (
	ErrOffline    = errors.New("box offline")
	ErrBusy       = errors.New("too many requests in flight for this box")
	ErrTimeout    = errors.New("the box did not answer in time")
	ErrNotAllowed = errors.New("this route is not relayed")
	ErrNoBox      = errors.New("no such box")
	ErrMasterOff  = errors.New("the master role is off")
)

// timeouts is Timeout, replaced by tests.
var timeouts = Timeout

// MasterStatus is the master half of GET /v1/fleet.
type MasterStatus struct {
	Address string      `json:"address"`
	Boxes   []BoxStatus `json:"boxes"`
	Relay   string      `json:"relay,omitempty"`    // the relay the master runs over; none in advanced mode
	NetAddr string      `json:"net_addr,omitempty"` // this box's address on the relay's private network
}

// BoxStatus is one box as the dashboard sees it: never its key.
type BoxStatus struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	URL      string    `json:"url"`
	Self     bool      `json:"self"`
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"last_seen,omitzero"`
	Hostname string    `json:"hostname"`
	Version  string    `json:"version"`
	// NeedsRecode: the master changed relay; the box needs a new code (RotateCode) to follow it.
	NeedsRecode bool `json:"needs_recode,omitempty"`
}

// tunnel is one live connection from a box.
type tunnel struct {
	boxID   string
	keyHash string // the key the handshake carried: attach refuses if it is no longer the box's
	conn    *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan frame // the relayed requests waiting for their res, by frame id
	done    chan struct{}         // closed with the connection: every waiter gets ErrOffline
	once    sync.Once
}

func (t *tunnel) close() {
	t.once.Do(func() {
		close(t.done)
		t.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		t.conn.Close()
	})
}

func (t *tunnel) write(fr frame) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return t.conn.WriteJSON(fr)
}

func (t *tunnel) register() (string, chan frame, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.pending) >= maxInFlight {
		return "", nil, ErrBusy
	}
	id := newID()
	ch := make(chan frame, 1)
	t.pending[id] = ch
	return id, ch, nil
}

func (t *tunnel) unregister(id string) {
	t.mu.Lock()
	delete(t.pending, id)
	t.mu.Unlock()
}

func (t *tunnel) ping() {
	tick := time.NewTicker(pingEvery)
	defer tick.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tick.C:
			if err := t.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
				t.close()
				return
			}
		}
	}
}

var upgrader = websocket.Upgrader{}

// HandleTunnel is GET /v1/fleet/tunnel: a box's tunnel. 401 unless the
// Authorization header is "Fleet <id>:<key>" of an enrolled box; then the
// hello, then res frames until the connection goes. A member's req frame is
// ignored: only the master asks.
func (f *Fleet) HandleTunnel(w http.ResponseWriter, r *http.Request) {
	box, ok := f.authorize(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade answered
	}
	conn.SetReadLimit(maxFrame)
	conn.SetReadDeadline(time.Now().Add(helloWait))
	var hello frame
	if err := conn.ReadJSON(&hello); err != nil || hello.T != "hello" {
		conn.Close()
		return
	}
	t := &tunnel{boxID: box.ID, keyHash: box.KeyHash, conn: conn, pending: map[string]chan frame{}, done: make(chan struct{})}
	if !f.attach(t, hello) {
		// revoked between the handshake and the hello: close, serve nothing
		conn.Close()
		return
	}
	defer f.detach(t)
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(readWait)) })
	go t.ping()
	for {
		conn.SetReadDeadline(time.Now().Add(readWait))
		var fr frame
		if err := conn.ReadJSON(&fr); err != nil {
			return
		}
		if fr.T != "res" {
			continue
		}
		t.mu.Lock()
		ch := t.pending[fr.ID]
		t.mu.Unlock()
		if ch != nil {
			select {
			case ch <- fr:
			default:
			}
		}
	}
}

// authorize finds the box behind a "Fleet <id>:<key>" header.
func (f *Fleet) authorize(header string) (Box, bool) {
	rest, ok := strings.CutPrefix(header, "Fleet ")
	if !ok {
		return Box{}, false
	}
	id, key, ok := strings.Cut(rest, ":")
	if !ok || id == "" || key == "" {
		return Box{}, false
	}
	f.mu.Lock()
	st, on := f.master()
	f.mu.Unlock()
	if !on {
		return Box{}, false
	}
	for _, b := range st.Boxes {
		if b.ID == id && subtle.ConstantTimeCompare([]byte(KeyHash(key)), []byte(b.KeyHash)) == 1 {
			return b, true
		}
	}
	return Box{}, false
}

// attach makes t the box's tunnel, replacing an older one, and records the
// hello. It refuses (false) when the box's key is no longer the one the
// handshake carried: a dial authorised before the key was rotated, the box
// removed or the master disabled must not end in a live tunnel.
func (f *Fleet) attach(t *tunnel, hello frame) bool {
	f.mu.Lock()
	st, on := f.master()
	i := indexOf(st.Boxes, t.boxID)
	if !on || i < 0 || st.Boxes[i].KeyHash != t.keyHash {
		f.mu.Unlock()
		return false
	}
	old := f.tunnels[t.boxID]
	f.tunnels[t.boxID] = t
	st.Boxes[i].LastSeen, st.Boxes[i].Hostname, st.Boxes[i].Version = f.Now(), cut(hello.Hostname), cut(hello.Version)
	err := f.saveMaster(st)
	f.mu.Unlock()
	if err != nil {
		f.Log("fleet: cannot record a box's hello", err)
	}
	if old != nil {
		old.close()
	}
	return true
}

// detach closes t and forgets it, unless a newer tunnel already took its place.
func (f *Fleet) detach(t *tunnel) {
	t.close()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tunnels[t.boxID] != t {
		return
	}
	delete(f.tunnels, t.boxID)
	f.modifyBox(t.boxID, func(b *Box) error { b.LastSeen = f.Now(); return nil })
}

// Relay sends one request down the box's tunnel and returns what the box
// answered. The table is checked here, before anything reaches the tunnel.
func (f *Fleet) Relay(ctx context.Context, boxID, method, path, query, contentType string, body []byte) (Response, error) {
	slow, ok := Allowed(method, path)
	if !ok {
		return Response{}, ErrNotAllowed
	}
	f.mu.Lock()
	st, on := f.master()
	t := f.tunnels[boxID]
	f.mu.Unlock()
	if !on || indexOf(st.Boxes, boxID) < 0 {
		return Response{}, ErrNoBox
	}
	if t == nil {
		return Response{}, ErrOffline
	}
	id, ch, err := t.register()
	if err != nil {
		return Response{}, err
	}
	defer t.unregister(id)
	headers := map[string]string{"Accept": "application/json"}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}
	if err := t.write(frame{T: "req", ID: id, Method: method, Path: path, Query: stripToken(query), Headers: headers, Body: body}); err != nil {
		t.close()
		return Response{}, ErrOffline
	}
	ctx, cancel := context.WithTimeout(ctx, timeouts(slow))
	defer cancel()
	select {
	case res := <-ch:
		status := res.Status
		if status < 100 || status > 599 {
			// a box that answers a status no HTTP response can carry is not
			// one the dashboard can be handed as it is: call it a bad gateway
			status = http.StatusBadGateway
		}
		return Response{Status: status, ContentType: res.Headers["Content-Type"], Body: res.Body}, nil
	case <-t.done:
		return Response{}, ErrOffline
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Response{}, ErrTimeout
		}
		return Response{}, ctx.Err()
	}
}

// inFlight is how many relayed requests the box has not answered yet.
func (f *Fleet) inFlight(boxID string) int {
	f.mu.Lock()
	t := f.tunnels[boxID]
	f.mu.Unlock()
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pending)
}

var errMemberElsewhere = errors.New("this box is a member of a fleet: leave it before becoming a master")

// EnableMaster turns the role on at address, keeping the boxes already
// enrolled. A box that is a member of another fleet cannot become a master:
// the two roles are exclusive (a self-enrolled master's own member.json names
// itself, which is not another fleet). A master running over a relay is not
// switched to an address typed by hand: it is disabled first.
func (f *Fleet) EnableMaster(address string) error {
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	return f.enableMaster(address, "", "", "")
}

// enableMaster is EnableMaster with the relay's fields, empty in advanced
// mode; the caller holds relayMu.
func (f *Fleet) enableMaster(address, relay, fleetID, netAddr string) error {
	if err := ValidMasterURL(address); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.loadMember(); ok && !f.namesSelf(m) {
		return errMemberElsewhere
	}
	st, _ := f.loadMaster()
	if relay == "" && st.Enabled && st.Relay != "" {
		return errors.New("the master runs over a relay: disable it before typing its address by hand")
	}
	st.Enabled, st.Address, st.Relay, st.FleetID, st.NetAddr = true, address, relay, fleetID, netAddr
	if st.Boxes == nil {
		st.Boxes = []Box{}
	}
	return f.saveMaster(st)
}

// addrWait bounds how long EnableMasterRelay waits for the private network to
// give this box an address, asking every addrStep; replaced by tests.
var addrWait, addrStep = 30 * time.Second, 500 * time.Millisecond

// EnableMasterRelay turns the role on over a relay (DefaultRelayURL when
// relayURL is empty): a fleet is created there, this box joins its private
// network, and the master's address is this box's address on it. Nothing is
// asked of the relay before every local check passed, and what was done is
// undone on a later failure. A master already on over this relay is left as it
// is; one on over another relay changes relay instead.
func (f *Fleet) EnableMasterRelay(ctx context.Context, relayURL string) error {
	if relayURL == "" {
		relayURL = DefaultRelayURL
	}
	relayURL = trimURL(relayURL)
	if err := ValidMasterURL(relayURL); err != nil {
		return fmt.Errorf("the relay address is not valid: %w", err)
	}
	n, err := f.PrivateNet()
	if err != nil {
		return err
	}
	info, err := f.Gateway()
	if err != nil {
		return err
	}
	port, err := httpsPort(info)
	if err != nil {
		return err
	}
	hostname, err := f.Hostname()
	if err != nil {
		return err
	}
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	f.mu.Lock()
	st, on := f.master()
	rs, haveRelay := f.loadRelay()
	m, member := f.loadMember()
	f.mu.Unlock()
	if member && !f.namesSelf(m) {
		return errMemberElsewhere
	}
	if on && st.Relay != "" {
		switch {
		case !haveRelay: // never a new fleet over a lost secret: the members are in the old one
			return errRelayLost
		case rs.Relay == relayURL && st.Relay == relayURL:
			return nil
		}
		return errors.New("the master already runs over another relay: change the relay instead")
	}
	if haveRelay {
		// left by an enable that did not finish: that fleet and its network go first
		f.leaveRelay(ctx, rs, true)
	}
	ns, err := n.State(ctx)
	if err != nil {
		return err
	}
	if ns.Connected {
		return ErrOtherNetwork
	}
	creds, err := f.RelayAPI.CreateFleet(ctx, relayURL)
	if err != nil {
		return err
	}
	rs = RelayState{Relay: relayURL, FleetID: creds.FleetID, Secret: creds.Secret}
	undo := func(down bool) { f.leaveRelay(context.WithoutCancel(ctx), rs, down) }
	if !sameURL(creds.LoginServer, relayURL) {
		undo(false)
		return errLoginServer
	}
	if err := f.saveRelay(rs); err != nil { // saved before anything else: a crash never loses the secret
		undo(false)
		return err
	}
	key, _, err := f.RelayAPI.JoinKey(ctx, relayURL, creds.FleetID, creds.Secret)
	if err != nil {
		undo(false)
		return err
	}
	if err := n.Up(ctx, creds.LoginServer, key, netHostname(hostname)); err != nil {
		undo(!errors.Is(err, ErrOtherNetwork)) // that refusal ran nothing; another failure may have started tailscaled
		return err
	}
	addr, err := waitAddr(ctx, n)
	if err == nil {
		err = f.enableMaster(relayAddress(addr, port), relayURL, creds.FleetID, addr)
	}
	if err != nil {
		undo(true)
		return err
	}
	return nil
}

// waitAddr asks the private network for this box's address until it has one,
// addrWait at most.
func waitAddr(ctx context.Context, n Net) (string, error) {
	deadline := time.Now().Add(addrWait)
	for {
		if st, err := n.State(ctx); err == nil && st.Connected && st.Addr != "" {
			return st.Addr, nil
		}
		if !time.Now().Before(deadline) {
			return "", errors.New("the private network gave this box no address in time: try again later")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(addrStep):
		}
	}
}

// httpsPort is the port of the gateway's HTTPS, where members reach the master
// over the private network: the self-signed one, or Public when a configured
// certificate makes it https. Off loopback, members dial https only.
func httpsPort(info GatewayInfo) (string, error) {
	u := info.HTTPS
	if u == nil {
		u = info.Public
	}
	if u == nil || u.Scheme != "https" {
		return "", errors.New("the gateway serves no HTTPS, which members need to reach the master over a relay: turn it on first")
	}
	if p := u.Port(); p != "" {
		return p, nil
	}
	return "443", nil
}

// relayAddress is the master's address on the private network: https on addr,
// the port said unless it is 443.
func relayAddress(addr, port string) string {
	return "https://" + strings.TrimSuffix(net.JoinHostPort(addr, port), ":443")
}

// DisableMaster closes every tunnel and forgets the boxes. A self-enrolled
// master's own member.json pointed at this box's own gateway, which goes with
// the master, so it is cleared too; a member.json of another fleet is left.
// Over a relay, the fleet is then deleted there and the private network left,
// best-effort: the local disable never fails because the relay is down.
func (f *Fleet) DisableMaster() error {
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	f.mu.Lock()
	st, _ := f.loadMaster()
	rs, haveRelay := f.loadRelay()
	tunnels := f.tunnels
	f.tunnels = map[string]*tunnel{}
	err := os.Remove(f.masterPath())
	selfGone := false
	if m, ok := f.loadMember(); ok && f.namesSelf(m) {
		if cerr := f.clearMember(); cerr != nil && err == nil {
			err = cerr
		}
		selfGone = true
	}
	f.mu.Unlock()
	for _, t := range tunnels {
		t.close()
	}
	if selfGone {
		f.pokeMember()
	}
	if haveRelay || st.Relay != "" { // a damaged relay.json still leaves a network to go down
		f.leaveRelay(context.Background(), rs, true)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// AddBox enrols a box and returns its code, the one time the key is handed
// over. self enrols this box itself: its code points at its own gateway, and
// its member.json is written. There is at most one self box. Over a relay, the
// code of another box also carries a join key asked of the relay first: when the
// relay refuses (ErrFleetFull, ErrFleetGone, ErrRelayBusy...) no box is added.
func (f *Fleet) AddBox(name, link string, self bool) (Box, Code, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Box{}, Code{}, errors.New("a box needs a name")
	}
	if err := validLink(link); err != nil {
		return Box{}, Code{}, err
	}
	key, hash, err := NewKey()
	if err != nil {
		return Box{}, Code{}, err
	}
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	st, on := f.snapshot()
	if !on {
		return Box{}, Code{}, ErrMasterOff
	}
	join, err := f.joinKey(st, self)
	if err != nil {
		return Box{}, Code{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	st, on = f.master() // the boxes may have changed meanwhile; the relay cannot, under relayMu
	if !on {
		return Box{}, Code{}, ErrMasterOff
	}
	box := Box{ID: newID(), Name: name, URL: link, KeyHash: hash, Self: self, CreatedAt: f.Now()}
	code, err := f.code(st, box, key, join)
	if err != nil {
		return Box{}, Code{}, err
	}
	if self {
		for _, b := range st.Boxes {
			if b.Self {
				if t := f.tunnels[b.ID]; t != nil {
					delete(f.tunnels, b.ID)
					t.close()
				}
			}
		}
		st.Boxes = without(st.Boxes, func(b Box) bool { return b.Self })
	}
	st.Boxes = append(st.Boxes, box)
	if err := f.saveMaster(st); err != nil {
		return Box{}, Code{}, err
	}
	if self {
		if err := f.saveMember(MemberState{MasterURL: code.MasterURL, BoxID: box.ID, Key: key, MasterCertSHA256: code.CertSHA256}); err != nil {
			return Box{}, Code{}, err
		}
		f.pokeMember()
	}
	return box, code, nil
}

// RenameBox changes the name and/or the dashboard link of a box; nil leaves one as it is.
func (f *Fleet) RenameBox(id string, name, link *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.modifyBox(id, func(b *Box) error {
		if name != nil {
			n := strings.TrimSpace(*name)
			if n == "" {
				return errors.New("a box needs a name")
			}
			b.Name = n
		}
		if link != nil {
			if err := validLink(*link); err != nil {
				return err
			}
			b.URL = *link
		}
		return nil
	})
}

// RemoveBox revokes a box: its tunnel is closed and its key is worth nothing.
// Over a relay, the box is then taken off the private network too, best-effort:
// the local removal never fails because the relay is down.
func (f *Fleet) RemoveBox(id string) error {
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	f.mu.Lock()
	st, on := f.master()
	if !on {
		f.mu.Unlock()
		return ErrMasterOff
	}
	i := indexOf(st.Boxes, id)
	if i < 0 {
		f.mu.Unlock()
		return ErrNoBox
	}
	box := st.Boxes[i]
	st.Boxes = without(st.Boxes, func(b Box) bool { return b.ID == id })
	err := f.saveMaster(st)
	t := f.tunnels[id]
	delete(f.tunnels, id)
	f.mu.Unlock()
	if t != nil {
		t.close()
	}
	if err == nil {
		f.removeNode(st, box)
	}
	return err
}

// RotateCode gives a box a new key: the old one is worth nothing and its
// tunnel is closed; the new code is returned once. Over a relay, it carries a
// new join key, for the relay the master runs over now (which clears
// NeedsRecode); when the relay refuses one, the old key stays as it was.
func (f *Fleet) RotateCode(id string) (Code, error) {
	key, hash, err := NewKey()
	if err != nil {
		return Code{}, err
	}
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	st, on := f.snapshot()
	join := ""
	if i := indexOf(st.Boxes, id); on && i >= 0 {
		if join, err = f.joinKey(st, st.Boxes[i].Self); err != nil {
			return Code{}, err
		}
	}
	f.mu.Lock()
	st, on = f.master() // the boxes may have changed meanwhile
	if !on {
		f.mu.Unlock()
		return Code{}, ErrMasterOff
	}
	i := indexOf(st.Boxes, id)
	if i < 0 {
		f.mu.Unlock()
		return Code{}, ErrNoBox
	}
	st.Boxes[i].KeyHash, st.Boxes[i].NeedsRecode = hash, false
	box := st.Boxes[i]
	code, err := f.code(st, box, key, join)
	if err == nil {
		err = f.saveMaster(st)
	}
	if err == nil && box.Self {
		err = f.saveMember(MemberState{MasterURL: code.MasterURL, BoxID: box.ID, Key: key, MasterCertSHA256: code.CertSHA256})
	}
	t := f.tunnels[id]
	delete(f.tunnels, id)
	f.mu.Unlock()
	if err != nil {
		return Code{}, err
	}
	if t != nil {
		t.close()
	}
	if box.Self {
		f.pokeMember()
	}
	return code, nil
}

// MasterStatus is the master's view: ok is false when the role is off.
func (f *Fleet) MasterStatus() (MasterStatus, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, on := f.master()
	if !on {
		return MasterStatus{}, false
	}
	out := MasterStatus{Address: st.Address, Boxes: make([]BoxStatus, 0, len(st.Boxes)), Relay: st.Relay, NetAddr: st.NetAddr}
	for _, b := range st.Boxes {
		out.Boxes = append(out.Boxes, BoxStatus{ID: b.ID, Name: b.Name, URL: b.URL, Self: b.Self, Online: f.tunnels[b.ID] != nil, LastSeen: b.LastSeen, Hostname: b.Hostname, Version: b.Version, NeedsRecode: b.NeedsRecode})
	}
	return out, true
}

// master is loadMaster with the role switch; the caller holds f.mu.
func (f *Fleet) master() (MasterState, bool) {
	st, ok := f.loadMaster()
	return st, ok && st.Enabled
}

// snapshot is master taken under f.mu, for a caller about to go out over the network.
func (f *Fleet) snapshot() (MasterState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.master()
}

// modifyBox loads, changes and saves one box; the caller holds f.mu.
func (f *Fleet) modifyBox(id string, change func(*Box) error) error {
	st, on := f.master()
	if !on {
		return ErrMasterOff
	}
	i := indexOf(st.Boxes, id)
	if i < 0 {
		return ErrNoBox
	}
	if err := change(&st.Boxes[i]); err != nil {
		return err
	}
	return f.saveMaster(st)
}

// code is the enrolment code of a box: the master's address (or, for this box
// itself, its own gateway) and the fingerprint of the certificate it serves;
// with a join key, the relay the master runs over too.
func (f *Fleet) code(st MasterState, box Box, key, joinKey string) (Code, error) {
	info, err := f.Gateway()
	if err != nil {
		return Code{}, err
	}
	fp, err := CertFingerprint(info.CertFile)
	if errors.Is(err, fs.ErrNotExist) {
		// no certificate to pin (HTTPS is off): the chain check alone decides
		fp, err = "", nil
	}
	if err != nil {
		return Code{}, err
	}
	master := st.Address
	if box.Self {
		u := info.HTTPS
		if u == nil {
			u = info.Public
		}
		master = u.String()
	}
	c := Code{MasterURL: master, BoxID: box.ID, Key: key, CertSHA256: fp}
	if joinKey != "" {
		c.Relay, c.JoinKey = st.Relay, joinKey
	}
	return c, nil
}

// namesSelf says whether a member.json is this box's own (a self-enrolled
// master), not another fleet's: its master is this box's own loopback gateway.
func (f *Fleet) namesSelf(m MemberState) bool {
	u, err := url.Parse(m.MasterURL)
	return err == nil && loopbackHost(u.Hostname())
}

// validLink accepts no link, or an http(s) one: where a box's own dashboard is.
func validLink(link string) error {
	if link == "" {
		return nil
	}
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("the box's address must look like https://host[:port]")
	}
	return nil
}

// stripToken drops the token parameter: a relayed request never carries one.
func stripToken(query string) string {
	q, _ := url.ParseQuery(query)
	q.Del("token")
	return q.Encode()
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func indexOf(boxes []Box, id string) int {
	for i, b := range boxes {
		if b.ID == id {
			return i
		}
	}
	return -1
}

func without(boxes []Box, drop func(Box) bool) []Box {
	kept := make([]Box, 0, len(boxes))
	for _, b := range boxes {
		if !drop(b) {
			kept = append(kept, b)
		}
	}
	return kept
}

// cut bounds what a box says of itself in its hello.
func cut(s string) string {
	if len(s) > maxName {
		s = strings.ToValidUTF8(s[:maxName], "")
	}
	return s
}
