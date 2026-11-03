package fleet

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
)

// Code is what the owner copies from the master into a box's settings: where
// the master is, which box this is, the box's key, and the fingerprint of the
// master's certificate for when its chain does not verify. It is shown once.
type Code struct {
	MasterURL  string `json:"u"`
	BoxID      string `json:"b"`
	Key        string `json:"k"`
	CertSHA256 string `json:"f"`
}

// String is the one line the owner copies.
func (c Code) String() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ParseCode reads a pasted code back, refusing anything a box could not join with.
func ParseCode(s string) (Code, error) {
	var c Code
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Code{}, errors.New("this is not an enrolment code")
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Code{}, errors.New("this is not an enrolment code")
	}
	if c.BoxID == "" || c.Key == "" {
		return Code{}, errors.New("the enrolment code is incomplete")
	}
	if err := ValidMasterURL(c.MasterURL); err != nil {
		return Code{}, err
	}
	if c.CertSHA256 != "" && !isHex(c.CertSHA256, 32) {
		return Code{}, errors.New("the certificate fingerprint in the code is malformed")
	}
	return c, nil
}

// ValidMasterURL accepts https://host[:port], or plain http on this box's
// loopback (tests and the install check); never user info, a query or a fragment.
func ValidMasterURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return errors.New("the master address must look like https://host[:port]")
	}
	if u.User != nil {
		return errors.New("the master address must not carry a user or a password")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("the master address must not carry a query or a fragment")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if loopbackHost(u.Hostname()) {
			return nil
		}
		return errors.New("a plain-http master is only allowed on this box's loopback")
	}
	return errors.New("the master address must start with https://")
}

// NewKey makes a box's key (32 random bytes, hex) and the hash the master keeps.
func NewKey() (key, keyHash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	key = hex.EncodeToString(b)
	return key, KeyHash(key), nil
}

// KeyHash is the sha256 of a key, hex: the only form of a key the master stores.
func KeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// loopbackHost says whether a host is this box's own loopback.
func loopbackHost(h string) bool {
	ip := net.ParseIP(h)
	return h == "localhost" || (ip != nil && ip.IsLoopback())
}

func isHex(s string, n int) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == n
}
