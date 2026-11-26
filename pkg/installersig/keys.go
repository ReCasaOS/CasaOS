package installersig

import (
	"crypto/ed25519"
	"encoding/base64"
)

// The public keys the core accepts for installer signatures, each the
// standard base64 of a raw 32-byte Ed25519 public key. Key A signs releases;
// key B is the spare kept offline for a rotation.
// Filled by the owner's key PR. A core whose keys are empty cannot be shipped:
// the installer's release refuses to pin it.
const (
	keyA = ""
	keyB = ""
)

// Keys holds the public keys accepted for installer signatures.
var Keys = decodeKeys(keyA, keyB)

// decodeKeys skips empty and malformed entries.
func decodeKeys(encoded ...string) []ed25519.PublicKey {
	var keys []ed25519.PublicKey
	for _, e := range encoded {
		k, err := base64.StdEncoding.DecodeString(e)
		if e == "" || err != nil || len(k) != ed25519.PublicKeySize {
			continue
		}
		keys = append(keys, ed25519.PublicKey(k))
	}
	return keys
}
