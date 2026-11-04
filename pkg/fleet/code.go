package fleet

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
)

// Code is what the owner copies from the master into a box's settings: where
// the master is, which box this is, the box's key, and the fingerprint of the
// master's certificate for when its chain does not verify. When the fleet runs
// over a relay, it also carries the relay's address and the key to join it (no
// key for a box that was on the relay's network when the code was made). It is
// shown once.
type Code struct {
	MasterURL  string `json:"u"`
	BoxID      string `json:"b"`
	Key        string `json:"k"`
	CertSHA256 string `json:"f"`
	Relay      string `json:"r,omitempty"`
	JoinKey    string `json:"j,omitempty"`
	// Fleet is the master's fleet at the relay: a member told to join another one
	// leaves its own first. Absent from the codes of an older master.
	Fleet string `json:"n,omitempty"`
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
	// a relay without a join key: the code of a box already on its network (RotateCode)
	if c.JoinKey != "" && c.Relay == "" {
		return Code{}, errors.New("the enrolment code has a join key but no relay")
	}
	if c.Relay != "" {
		if err := ValidMasterURL(c.Relay); err != nil {
			return Code{}, fmt.Errorf("the relay address in the code is not valid: %w", err)
		}
	}
	if c.JoinKey != "" {
		if err := validJoinKey(c.JoinKey); err != nil {
			return Code{}, err
		}
	}
	if c.Fleet != "" && (c.Relay == "" || !fleetIDShape.MatchString(c.Fleet)) {
		return Code{}, errors.New("the fleet in the enrolment code is malformed")
	}
	return c, nil
}

// joinKeyShape is what a relay's join key looks like: a Headscale pre-auth key
// ("hskey-auth-<prefix>-<secret>", URL-safe base64) or an older one (48 hex).
// Never ':' or '/' (tailscale reads --authkey=file:<path> as a file, as root)
// nor a leading '-' (a flag).
var joinKeyShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{15,255}$`)

// fleetIDShape is a fleet id as the relay makes it ("f-<hex>"), loosely: it is
// only compared with the id member.json keeps.
var fleetIDShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// validJoinKey refuses a join key of any other shape; the error never quotes it.
func validJoinKey(k string) error {
	if !joinKeyShape.MatchString(k) {
		return errors.New("the join key is malformed")
	}
	return nil
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
