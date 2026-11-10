package fleet

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
)

// ErrNotFleetOwner: a member cannot register its key at the relay, only the fleet's owner can.
var ErrNotFleetOwner = errors.New("only the master registers this box's key at the relay")

// BoxKey is this box's signing key, made on first use.
func (f *Fleet) BoxKey() (ed25519.PrivateKey, error) {
	return loadBoxKey(filepath.Join(f.Root, StateDir))
}

// ShareRelay is the relay this box shares through: relay.json's for a master or a box alone, member.json's for a member.
func (f *Fleet) ShareRelay() (relay string, ok bool) {
	if rs, ok := f.loadRelay(); ok {
		return rs.Relay, true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.loadMember(); ok && m.Relay != "" {
		return m.Relay, true
	}
	return "", false
}

// RegisterOwnBoxKey links this box's key to its own node at the relay, with the fleet secret of
// relay.json and the node id of the private network. A member gets ErrNotFleetOwner.
func (f *Fleet) RegisterOwnBoxKey(ctx context.Context) error {
	rs, ok := f.loadRelay()
	if !ok {
		f.mu.Lock()
		m, member := f.loadMember()
		f.mu.Unlock()
		if member && m.Relay != "" {
			return ErrNotFleetOwner
		}
		return errRelayLost
	}
	key, err := f.BoxKey()
	if err != nil {
		return err
	}
	n, err := f.PrivateNet()
	if err != nil {
		return err
	}
	ns, err := n.State(ctx)
	if err != nil {
		return err
	}
	if ns.NodeID == "" {
		return errors.New("this box is not on the relay's private network")
	}
	return f.RelayAPI.SetBoxKey(ctx, rs.Relay, rs.FleetID, rs.Secret, ns.NodeID, key.Public().(ed25519.PublicKey))
}

// helloShare is what a member over a relay adds to its hello: its public key and node id. Empty
// for a member of an advanced master, or when either is unknown (the master then skips linking).
func (f *Fleet) helloShare(ctx context.Context, st MemberState) (boxKey, nodeID string) {
	if st.Relay == "" {
		return "", ""
	}
	key, err := f.BoxKey()
	if err != nil {
		f.Log("fleet: cannot read the box key, it will not be linked at the relay", err)
		return "", ""
	}
	n, err := f.PrivateNet()
	if err != nil {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(ctx, relayTimeout)
	defer cancel()
	ns, err := n.State(ctx)
	if err != nil {
		return "", ""
	}
	return base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey)), ns.NodeID
}

// linkBoxKey runs when a member's hello brings its key: it links it to the member's node at the
// relay and keeps the outcome for BoxStatus.ShareKey.
func (f *Fleet) linkBoxKey(boxID, nodeID, boxKey string) {
	pub, err := base64.RawURLEncoding.Strict().DecodeString(boxKey)
	if err == nil && len(pub) != ed25519.PublicKeySize {
		err = errors.New("the box key is not an ed25519 public key")
	}
	if err == nil {
		rs, ok := f.loadRelay()
		if !ok {
			err = errRelayLost
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), relayTimeout)
			err = f.RelayAPI.SetBoxKey(ctx, rs.Relay, rs.FleetID, rs.Secret, nodeID, pub)
			cancel()
		}
	}
	f.mu.Lock()
	prev, had := f.linked[boxID]
	f.linked[boxID] = err
	f.mu.Unlock()
	if errors.Is(err, ErrBoxKeyLinked) && !(had && errors.Is(prev, ErrBoxKeyLinked)) {
		f.Log(fmt.Sprintf("fleet: box %s: another key is linked at the relay; remove and re-enrol the box to let it share", boxID), nil)
	}
}

// leaving calls the OnLeavingRelay hook, never under mu.
func (f *Fleet) leaving() {
	if f.OnLeavingRelay != nil {
		f.OnLeavingRelay()
	}
}

// leavingRelay calls the hook when relay is the one this box shares through, never under mu.
func (f *Fleet) leavingRelay(relay string) {
	if r, ok := f.ShareRelay(); ok && sameURL(r, relay) {
		f.leaving()
	}
}
