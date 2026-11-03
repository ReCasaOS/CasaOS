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

// MemberState is member.json: the code the owner pasted, kept.
type MemberState struct {
	MasterURL        string `json:"master_url"`
	BoxID            string `json:"box_id"`
	Key              string `json:"key"`
	MasterCertSHA256 string `json:"master_cert_sha256"`
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
