package fleet

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DefaultRelayURL is the official relay. Provisional: the owner picks the final name.
const DefaultRelayURL = "https://relay.recasaos.org"

// relayError is a message the dashboard can show as it is, still matching the
// relay's own error with errors.Is.
type relayError struct {
	msg   string
	cause error
}

func (e *relayError) Error() string { return e.msg }
func (e *relayError) Unwrap() error { return e.cause }

var (
	// ErrFleetFull: the relay refused one more join key (ErrRelayQuota). The
	// dashboard matches "limit of boxes" (FleetModal.vue): keep it in the text.
	ErrFleetFull error = &relayError{"the relay refused: this fleet has reached its limit of boxes, codes not used yet included: remove a box you no longer use, or wait for unused codes to expire (a day, by default)", ErrRelayQuota}
	// ErrFleetGone: the relay no longer knows the fleet (ErrRelayFleetGone), for instance after a long idle time.
	ErrFleetGone error = &relayError{"the relay no longer knows this fleet (it deletes idle fleets): enable the master on a relay again by changing the relay (the same one will do), then give each box a new code", ErrRelayFleetGone}

	errRelayLost   = errors.New("relay.json, which holds this fleet's secret, is missing or damaged: disable the master and enable it again")
	errLoginServer = errors.New("the relay announces a different login server than its own address; this is not supported yet")
)

// sameURL says whether two relay addresses are one: a trailing slash and the
// host's case aside. A code carries the relay's address only, and members join
// with it as the login server.
func sameURL(a, b string) bool {
	ua, errA := url.Parse(trimURL(a))
	ub, errB := url.Parse(trimURL(b))
	return errA == nil && errB == nil && ua.Scheme == ub.Scheme && strings.EqualFold(ua.Host, ub.Host) && ua.Path == ub.Path
}

// RelayState is relay.json: the fleet this master owns at its relay. The secret
// lives here only, never in master.json, a log line or an error.
type RelayState struct {
	Relay   string `json:"relay"`
	FleetID string `json:"fleet_id"`
	Secret  string `json:"secret"`
}

func (f *Fleet) relayPath() string { return filepath.Join(f.Root, StateDir, "relay.json") }

// loadRelay reads relay.json. No file is "no relay"; a file that cannot be
// read or parsed, or is incomplete, is "no relay" too, said once in the log.
func (f *Fleet) loadRelay() (RelayState, bool) {
	var s RelayState
	if !f.loadJSON(f.relayPath(), &s) {
		return RelayState{}, false
	}
	if ValidMasterURL(s.Relay) != nil || s.FleetID == "" || s.Secret == "" {
		f.Log("fleet: relay.json is incomplete or names a relay this box would not use, ignored", errors.New("damaged relay.json"))
		return RelayState{}, false
	}
	return s, true
}

// saveRelay writes relay.json atomically and 0600.
func (f *Fleet) saveRelay(s RelayState) error { return saveJSON(f.relayPath(), s) }

// clearRelay forgets the relay's fleet: no file, no fleet.
func (f *Fleet) clearRelay() error {
	if err := os.Remove(f.relayPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// leaveRelay is the best-effort end of a fleet at its relay: the fleet deleted
// there (rs zero: nothing to delete), the private network left (down), and
// relay.json removed. Nothing is returned: every failure is logged, never with
// the secret. The caller holds relayMu, not mu: the calls go out over the network.
func (f *Fleet) leaveRelay(ctx context.Context, rs RelayState, down bool) {
	if rs.Secret != "" {
		if err := f.RelayAPI.DeleteFleet(ctx, rs.Relay, rs.FleetID, rs.Secret); err != nil {
			f.Log("fleet: cannot delete the fleet at the relay; the relay removes it on its own once idle", err)
		}
	}
	if down && f.Net != nil {
		if err := f.Net.Down(ctx); err != nil {
			f.Log("fleet: cannot leave the private network", err)
		}
	}
	if err := f.clearRelay(); err != nil {
		f.Log("fleet: cannot remove relay.json", err)
	}
}

// joinKey asks the relay for a single-use key for one more box when the master
// runs over a relay and the box is not this box itself, already on the network;
// "" otherwise. The key is handed over in the code only, never stored. The
// caller holds relayMu, not mu.
func (f *Fleet) joinKey(st MasterState, self bool) (string, error) {
	if st.Relay == "" || self {
		return "", nil
	}
	rs, err := f.relayFor(context.Background(), st)
	if err != nil {
		return "", err
	}
	key, _, err := f.RelayAPI.JoinKey(context.Background(), rs.Relay, rs.FleetID, rs.Secret)
	switch {
	case errors.Is(err, ErrRelayQuota):
		return "", ErrFleetFull
	case errors.Is(err, ErrRelayFleetGone):
		return "", ErrFleetGone
	}
	return key, err
}

// relayFor is relay.json for the relay of st, master.json as it was read; the
// caller holds relayMu, not mu. relay.json naming another fleet (FleetID
// included) is a crash between ChangeRelay's two writes, relay.json first:
// when this box is on relay.json's network, master.json is brought in line as
// ChangeRelay would have done; otherwise, or with no relay.json, errRelayLost.
func (f *Fleet) relayFor(ctx context.Context, st MasterState) (RelayState, error) {
	rs, ok := f.loadRelay()
	if !ok {
		return RelayState{}, errRelayLost
	}
	if rs.Relay == st.Relay && rs.FleetID == st.FleetID {
		return rs, nil
	}
	if f.Net == nil {
		return RelayState{}, errRelayLost
	}
	ns, err := f.Net.State(ctx)
	if err != nil || !ns.Connected || ns.Addr == "" || !sameURL(ns.LoginServer, rs.Relay) {
		return RelayState{}, errRelayLost
	}
	info, err := f.Gateway()
	if err != nil {
		return RelayState{}, err
	}
	port, err := httpsPort(info)
	if err != nil {
		return RelayState{}, err
	}
	if err := f.moveMaster(rs, ns.Addr, port); err != nil {
		return RelayState{}, err
	}
	f.Log("fleet: master.json followed relay.json after an unfinished relay change; the old fleet idles out at its relay", nil)
	return rs, nil
}

// moveMaster points master.json at rs's fleet, this box at addr on its network,
// and asks every other box for a new code. The caller holds relayMu, not mu.
func (f *Fleet) moveMaster(rs RelayState, addr, port string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, on := f.master()
	if !on {
		return ErrMasterOff
	}
	st.Address, st.Relay, st.FleetID, st.NetAddr = relayAddress(addr, port), rs.Relay, rs.FleetID, addr
	for i := range st.Boxes {
		st.Boxes[i].NeedsRecode = !st.Boxes[i].Self // this box's own code names its loopback gateway, not the relay
	}
	return f.saveMaster(st)
}

// removeNode takes a removed box off the relay's private network, best-effort
// and logged; st is master.json without the box. The box joined as nodeName
// makes it, "b-<box id>"; this box's own name there (netHostname of its
// hostname) is never removed for it. The caller holds relayMu, not mu.
//
// shortcut: by name only; a box that runs its own `tailscale up --hostname`
// can rename itself out of reach and stays on the network until the fleet is
// deleted. Removing by the node's address at the relay is future work.
func (f *Fleet) removeNode(st MasterState, box Box) {
	if st.Relay == "" || box.Self || !validBoxID(box.ID) {
		return
	}
	name := nodeName(box.ID, "")
	if me, err := f.Hostname(); err != nil || netHostname(me) == name {
		return
	}
	rs, ok := f.loadRelay()
	if !ok {
		return
	}
	if err := f.RelayAPI.RemoveNode(context.Background(), rs.Relay, rs.FleetID, rs.Secret, name); err != nil {
		f.Log("fleet: cannot remove the box from the relay's private network; it stays there until the fleet is deleted", err)
	}
}

// ChangeRelay moves a master running over a relay to relayURL (DefaultRelayURL
// when empty): a fleet is created there, this box leaves the old private network
// for the new one, and the old fleet is deleted. The same relay is allowed: a new
// fleet there, which is how a fleet the relay forgot (ErrFleetGone) is replaced.
//
// Every other box is then marked NeedsRecode: its key stays valid, but it still
// looks for the master on the old network, and its old code no longer works
// either, its join key belonging to the deleted fleet. RotateCode gives it a code
// for the new relay.
//
// Nothing changes when the new relay fails before this box left the old network
// (its fleet, if created, is deleted again). A box is in one private network at a
// time, and a left one cannot be rejoined with its old node: when joining the new
// network fails after that, the new fleet is dropped, master.json and relay.json
// still name the old relay, and the error says this box is off the private network
// until the relay is changed again.
func (f *Fleet) ChangeRelay(ctx context.Context, relayURL string) error {
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
	st, on := f.snapshot()
	if !on || st.Relay == "" {
		return errors.New("the master does not run over a relay: enable it over one first")
	}
	old, err := f.relayFor(ctx, st)
	if err != nil {
		return err
	}
	creds, err := f.RelayAPI.CreateFleet(ctx, relayURL)
	if err != nil {
		return err
	}
	rs := RelayState{Relay: relayURL, FleetID: creds.FleetID, Secret: creds.Secret}
	bg := context.WithoutCancel(ctx)
	drop := func() {
		// shortcut: the new secret is kept in memory only until the new network is up; a crash before leaves its fleet to idle out at the relay.
		if err := f.RelayAPI.DeleteFleet(bg, rs.Relay, rs.FleetID, rs.Secret); err != nil {
			f.Log("fleet: cannot delete the new relay's fleet; the relay removes it on its own once idle", err)
		}
	}
	if !sameURL(creds.LoginServer, relayURL) {
		drop()
		return errLoginServer
	}
	key, _, err := f.RelayAPI.JoinKey(ctx, relayURL, creds.FleetID, creds.Secret)
	if err != nil {
		drop()
		return err
	}
	if err := n.Down(ctx); err != nil {
		drop()
		return fmt.Errorf("cannot leave the current private network: %w", err)
	}
	lost := func(err error) error {
		if derr := n.Down(bg); derr != nil {
			f.Log("fleet: cannot leave the new private network", derr)
		}
		drop()
		if serr := f.saveRelay(old); serr != nil {
			f.Log("fleet: cannot write relay.json back", serr)
		}
		return fmt.Errorf("this box is now off the private network, its boxes cannot reach it: change the relay again: %w", err)
	}
	if err := n.Up(ctx, creds.LoginServer, key, netHostname(hostname)); err != nil {
		return lost(err)
	}
	addr, err := waitAddr(ctx, n)
	if err != nil {
		return lost(err)
	}
	if err := f.saveRelay(rs); err != nil { // relay.json first: a crash between the two never loses the new secret
		return lost(err)
	}
	if err := f.moveMaster(rs, addr, port); err != nil {
		return lost(err)
	}
	if err := f.RelayAPI.DeleteFleet(bg, old.Relay, old.FleetID, old.Secret); err != nil {
		f.Log("fleet: cannot delete the old relay's fleet; the relay removes it on its own once idle", err)
	}
	return nil
}
