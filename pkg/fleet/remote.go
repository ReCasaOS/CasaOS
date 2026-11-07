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
)

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
	if _, err := httpsPort(info); err != nil { // loopback-only: no phone could reach this box
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
	case haveRelay && rs.Remote && rs.NetAddr != "": // without NetAddr the enable did not finish: leftover below
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
// relay, the private network left and relay.json removed. A master's fleet is
// not touched, and no remote access is not an error.
func (f *Fleet) DisableRemote(ctx context.Context) error {
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	f.mu.Lock()
	_, on := f.master()
	rs, haveRelay := f.loadRelay()
	f.mu.Unlock()
	if on {
		return errRemoteIsMaster
	}
	if haveRelay && rs.Remote {
		f.leaveRelay(context.WithoutCancel(ctx), rs, true)
	}
	return nil
}
