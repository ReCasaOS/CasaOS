package fleet

import (
	"context"
	"errors"
	"fmt"
)

var (
	errRemoteMember     = errors.New("this box is a member of a fleet: phones and PCs are added on the master's dashboard")
	errRemoteAdvanced   = errors.New("the master runs at an address typed by hand: move it onto a relay to get remote access")
	errRemoteIsMaster   = errors.New("remote access goes with the master: disable the master to turn it off")
	errRemoteOtherRelay = errors.New("remote access already runs over another relay: turn it off first")
	errRemoteLoopback   = errors.New("the gateway listens on this box's loopback only, so your phones and PCs cannot reach it: set its address to 0.0.0.0 (or leave it empty)")

	ErrRemoteOff = errors.New("remote access is off on this box")
	// ErrRemoteGone: the relay forgot a box's network; wraps ErrRelayFleetGone.
	ErrRemoteGone error = &relayError{"the relay no longer knows this box's network (it deletes networks idle for 30 days): turn remote access off and on again, then add your devices again", ErrRelayFleetGone}

	// ErrRemoteOn: Join would end remote access, which only a confirmed request may do.
	ErrRemoteOn = errors.New("this box has remote access: joining a fleet turns it off and its phones and PCs lose access; confirm to go on")
)

// remoteAlone says remote access is on for a box alone: relay.json is remote
// and finished (it has the box's address) and the master role is off. A remote
// relay.json without NetAddr is an unfinished enable, a leftover.
func remoteAlone(rs RelayState, masterOn bool) bool {
	return rs.Remote && rs.NetAddr != "" && !masterOn
}

// EnableRemote gives a box with no fleet remote access: a fleet of one is
// created at the relay (DefaultRelayURL when relayURL is empty) and this box
// joins its private network; relay.json is flagged remote and there is no
// master.json. Phones join it with codes later. Nothing is asked of the relay
// before every local check passed. Already on, over this relay or as a master
// over a relay, is left as it is.
func (f *Fleet) EnableRemote(ctx context.Context, relayURL string) error {
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
	if info.LoopbackOnly { // HTTP is fine: phones reach the dashboard inside the encrypted network
		return errRemoteLoopback
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
	m, isMember := f.loadMember()
	member := isMember && !f.namesSelf(m) // a self-enrolled master is no member
	f.mu.Unlock()
	switch {
	case member:
		return errRemoteMember
	case on && st.Relay != "":
		return nil
	case on:
		return errRemoteAdvanced
	case haveRelay && remoteAlone(rs, on): // without NetAddr the enable did not finish: leftover below
		if sameURL(rs.Relay, relayURL) {
			return nil
		}
		return errRemoteOtherRelay
	}
	if haveRelay {
		// left by an enable that did not finish: that fleet and its network go first
		f.leaveRelay(ctx, rs, true)
	}
	if mine, ok := f.loadNetOwn(); ok { // a member's Join that did not finish: its network is ours to leave
		f.netDown(n, mine)
	}
	ns, err := n.State(ctx)
	if err != nil {
		return err
	}
	if ns.LoginServer != "" { // in a network, even stopped: the owner's, not ours
		return ErrOtherNetwork
	}
	_, _, err = f.joinOwnNet(ctx, n, relayURL, hostname, true)
	return err
}

// DisableRemote turns remote access off: the fleet of one is deleted at the
// relay (best-effort), the private network left and relay.json removed. When
// the network cannot be left, relay.json is kept and the error returned: the box
// never sits on a network nothing records. The network of a member.json naming
// another fleet may be that member's: it is not left. A master's fleet is not
// touched, and no remote access is not an error.
func (f *Fleet) DisableRemote(ctx context.Context) error {
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	f.mu.Lock()
	_, on := f.master()
	rs, haveRelay := f.loadRelay()
	m, isMember := f.loadMember()
	member := isMember && !f.namesSelf(m)
	f.mu.Unlock()
	if on {
		return errRemoteIsMaster
	}
	if !haveRelay || !rs.Remote { // any remote relay.json goes, an unfinished enable too
		return nil
	}
	bg := context.WithoutCancel(ctx)
	if err := f.RelayAPI.DeleteFleet(bg, rs.Relay, rs.FleetID, rs.Secret); err != nil {
		f.Log("fleet: cannot delete the fleet at the relay; the relay removes it on its own once idle", err)
	}
	if !member && f.Net != nil {
		if err := f.Net.Down(bg, rs.Relay); err != nil {
			return fmt.Errorf("cannot leave the remote access network: %w", err)
		}
	}
	return f.clearRelay()
}

// remoteFleet is the relay fleet behind devices: a master's over its relay, or
// the one of a box alone (alone true). The caller holds relayMu.
func (f *Fleet) remoteFleet(ctx context.Context) (rs RelayState, st MasterState, alone bool, err error) {
	f.mu.Lock()
	st, on := f.master()
	rs, haveRelay := f.loadRelay()
	m, isMember := f.loadMember()
	f.mu.Unlock()
	switch {
	case isMember && !f.namesSelf(m):
		err = errRemoteMember
	case on && st.Relay != "":
		rs, err = f.relayFor(ctx, st)
	case haveRelay && remoteAlone(rs, on):
		alone = true
	default:
		err = ErrRemoteOff
	}
	return rs, st, alone, err
}

// goneErr says ErrRelayFleetGone the way the role does.
func goneErr(err error, alone bool) error {
	switch {
	case !errors.Is(err, ErrRelayFleetGone):
		return err
	case alone:
		return ErrRemoteGone
	}
	return ErrFleetGone
}

// NewDeviceCode asks the relay for a code a phone or PC joins the network with.
func (f *Fleet) NewDeviceCode(ctx context.Context) (DeviceCode, error) {
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	rs, _, alone, err := f.remoteFleet(ctx)
	if err != nil {
		return DeviceCode{}, err
	}
	c, err := f.RelayAPI.DeviceCode(ctx, rs.Relay, rs.FleetID, rs.Secret)
	return c, goneErr(err, alone)
}

// Devices lists the phones and PCs on the network.
func (f *Fleet) Devices(ctx context.Context) ([]Device, error) {
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	rs, _, alone, err := f.remoteFleet(ctx)
	if err != nil {
		return nil, err
	}
	ds, err := f.RelayAPI.Devices(ctx, rs.Relay, rs.FleetID, rs.Secret)
	return ds, goneErr(err, alone)
}

// RemoveDevice takes one phone or PC off the network.
func (f *Fleet) RemoveDevice(ctx context.Context, id string) error {
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	rs, _, alone, err := f.remoteFleet(ctx)
	if err != nil {
		return err
	}
	return goneErr(f.RelayAPI.RemoveDevice(ctx, rs.Relay, rs.FleetID, rs.Secret, id), alone)
}

// RemoteBox is a box of the master's fleet on the private network.
type RemoteBox struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Addr   string `json:"addr"`
	Online bool   `json:"online"`
}

// RemoteView is what the dashboard shows of remote access: the devices, and for a master its boxes.
type RemoteView struct {
	Relay   string      `json:"relay"`
	Addr    string      `json:"addr"`
	Devices []Device    `json:"devices"`
	Boxes   []RemoteBox `json:"boxes,omitempty"`
}

// RemoteView asks the relay for the devices and, on a master, the boxes' nodes.
func (f *Fleet) RemoteView(ctx context.Context) (RemoteView, error) {
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	rs, st, alone, err := f.remoteFleet(ctx)
	if err != nil {
		return RemoteView{}, err
	}
	devs, err := f.RelayAPI.Devices(ctx, rs.Relay, rs.FleetID, rs.Secret)
	if err != nil {
		return RemoteView{}, goneErr(err, alone)
	}
	v := RemoteView{Relay: rs.Relay, Addr: rs.NetAddr, Devices: devs}
	if alone {
		return v, nil
	}
	nodes, err := f.RelayAPI.FleetNodes(ctx, rs.Relay, rs.FleetID, rs.Secret)
	if err != nil {
		return RemoteView{}, goneErr(err, alone)
	}
	v.Addr = st.NetAddr
	for _, b := range st.Boxes {
		rb := RemoteBox{ID: b.ID, Name: b.Name}
		if b.Self {
			rb.Addr, rb.Online = st.NetAddr, true
		}
		for _, n := range nodes {
			if !b.Self && n.Hostname == nodeName(b.ID, "") {
				rb.Addr, rb.Online = n.IP, n.Online
			}
		}
		v.Boxes = append(v.Boxes, rb)
	}
	return v, nil
}
