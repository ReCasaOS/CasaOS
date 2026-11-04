package fleet

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
)

// boxKeyFile is the box's signing key, in the fleet state directory.
const boxKeyFile = "boxkey.json"

type boxKeyJSON struct {
	PrivateKey string `json:"private_key"` // the 32-byte ed25519 seed, raw base64url
}

// boxKeyMu keeps two first calls from each making a key and the loser's fleet from being orphaned.
var boxKeyMu sync.Mutex

// loadBoxKey returns the box's ed25519 key from dir/boxkey.json, making it once if
// there is no file. The relay owns a fleet by this key's identity at creation, so a
// file that cannot be read, parsed or has the wrong length is an error and is never
// replaced: a new key would silently orphan the box's fleet.
func loadBoxKey(dir string) (ed25519.PrivateKey, error) {
	if dir == "" {
		return nil, errors.New("fleet: no directory for the box key")
	}
	boxKeyMu.Lock()
	defer boxKeyMu.Unlock()
	path := filepath.Join(dir, boxKeyFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return createBoxKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("fleet: cannot read %s: %w", boxKeyFile, err)
	}
	var j boxKeyJSON
	if json.Unmarshal(data, &j) == nil {
		if seed, err := base64.RawURLEncoding.Strict().DecodeString(j.PrivateKey); err == nil && len(seed) == ed25519.SeedSize {
			return ed25519.NewKeyFromSeed(seed), nil
		}
	}
	return nil, fmt.Errorf("fleet: %s is damaged and is left as it is, since a new key would orphan this box's fleet", boxKeyFile)
}

func createBoxKey(path string) (ed25519.PrivateKey, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(boxKeyJSON{PrivateKey: base64.RawURLEncoding.EncodeToString(key.Seed())})
	if err != nil {
		return nil, err
	}
	// Atomic and 0600 (os.CreateTemp), like the other state files.
	if err := file.WriteFileAtomic(path, data); err != nil {
		return nil, fmt.Errorf("fleet: cannot save %s: %w", boxKeyFile, err)
	}
	return key, nil
}
