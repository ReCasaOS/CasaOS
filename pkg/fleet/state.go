package fleet

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
)

// StateDir holds master.json, member.json, relay.json and boxkey.json, under Root.
const StateDir = "/var/lib/casaos/fleet"

// Box is one enrolled box as the master keeps it: its key only hashed.
type Box struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	KeyHash   string    `json:"key_sha256"`
	Self      bool      `json:"self"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
	Hostname  string    `json:"hostname"`
	Version   string    `json:"version"`
	// WebScheme and WebPort: where the box says its dashboard listens (validated hello), empty if unknown.
	WebScheme string `json:"web_scheme,omitempty"`
	WebPort   string `json:"web_port,omitempty"`
	// NeedsRecode: the master changed relay since this box's code was made, so the box
	// still looks for it on the old relay's network; cleared by RotateCode.
	NeedsRecode bool `json:"needs_recode,omitempty"`
}

// MasterState is master.json. Over a relay, it names the relay and the fleet
// there, and this box's address on the private network; the fleet's secret is
// in relay.json only.
type MasterState struct {
	Enabled bool   `json:"enabled"`
	Address string `json:"address"`
	Boxes   []Box  `json:"boxes"`
	Relay   string `json:"relay,omitempty"`
	FleetID string `json:"fleet_id,omitempty"`
	NetAddr string `json:"net_addr,omitempty"`
}

// MemberState is member.json: the code the owner pasted, kept, without its
// join key (spent on joining the relay's private network).
type MemberState struct {
	MasterURL        string `json:"master_url"`
	BoxID            string `json:"box_id"`
	Key              string `json:"key"`
	MasterCertSHA256 string `json:"master_cert_sha256"`
	Relay            string `json:"relay,omitempty"`
	// FleetID is the code's fleet at the relay: another fleet's code makes this
	// box leave this one first. Empty for a code of an older master.
	FleetID string `json:"fleet_id,omitempty"`
}

// netOwn is netown.json: the relay whose private network this box joined (or is
// about to join) as a member. It exists only between the start of Join and the
// moment member.json says the same, so a crash in between leaves a network that
// Leave and the next Join still know to be this box's own.
type netOwn struct {
	Relay string `json:"relay"`
}

func (f *Fleet) netOwnPath() string { return filepath.Join(f.Root, StateDir, "netown.json") }

func (f *Fleet) loadNetOwn() (string, bool) {
	var s netOwn
	if !f.loadJSON(f.netOwnPath(), &s) || ValidMasterURL(s.Relay) != nil {
		return "", false
	}
	return s.Relay, true
}

func (f *Fleet) saveNetOwn(relay string) error { return saveJSON(f.netOwnPath(), netOwn{Relay: relay}) }

func (f *Fleet) clearNetOwn() {
	if err := os.Remove(f.netOwnPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		f.Log("fleet: cannot remove netown.json", err)
	}
}

func (f *Fleet) masterPath() string { return filepath.Join(f.Root, StateDir, "master.json") }
func (f *Fleet) memberPath() string { return filepath.Join(f.Root, StateDir, "member.json") }

// loadMaster reads master.json. No file is "role off"; a file that cannot be
// read or parsed, or whose address the code rule refuses, is "role off" too,
// said once in the log.
func (f *Fleet) loadMaster() (MasterState, bool) {
	var s MasterState
	if !f.loadJSON(f.masterPath(), &s) {
		return MasterState{}, false
	}
	if err := ValidMasterURL(s.Address); err != nil {
		f.Log("fleet: master.json has an address this box would not use, master off", err)
		return MasterState{}, false
	}
	return s, true
}

// saveMaster writes master.json atomically and 0600.
func (f *Fleet) saveMaster(s MasterState) error { return saveJSON(f.masterPath(), s) }

// loadMember reads member.json; the same rules as loadMaster.
func (f *Fleet) loadMember() (MemberState, bool) {
	var s MemberState
	if !f.loadJSON(f.memberPath(), &s) {
		return MemberState{}, false
	}
	if err := ValidMasterURL(s.MasterURL); err != nil {
		f.Log("fleet: member.json names a master this box would not dial, member off", err)
		return MemberState{}, false
	}
	return s, true
}

// saveMember writes member.json atomically and 0600.
func (f *Fleet) saveMember(s MemberState) error { return saveJSON(f.memberPath(), s) }

// clearMember forgets the master: no file, no role.
func (f *Fleet) clearMember() error {
	if err := os.Remove(f.memberPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (f *Fleet) loadJSON(path string, v any) bool {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err == nil {
		err = json.Unmarshal(data, v)
	}
	if err != nil {
		f.Log("fleet: cannot read "+filepath.Base(path)+", role off", err)
		return false
	}
	return true
}

func saveJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return file.WriteFileAtomic(path, data)
}
