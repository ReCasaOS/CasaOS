package fleet

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// DefaultRelayURL is the official relay. Provisional: the owner picks the final name.
const DefaultRelayURL = "https://relay.recasaos.org"

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
