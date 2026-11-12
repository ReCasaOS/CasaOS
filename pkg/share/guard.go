package share

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
)

// GuardFile is kept next to StateFile: the protection settings, the people and the secret.
const GuardFile = "share-guard.json"

const (
	codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	codeLen      = 12
)

// Policy, Countries and BanRules have the JSON shape of the Gateway's pkg/guard config.
type Policy struct {
	WAF        string    `json:"waf"` // "off", "observe", "block"
	RulesOff   []int     `json:"rules_off"`
	Allow      []string  `json:"allow"`
	Block      []string  `json:"block"`
	Countries  Countries `json:"countries"`
	RatePerMin int       `json:"rate_per_min"`
	Ban        BanRules  `json:"ban"`
}

type Countries struct {
	Mode string   `json:"mode"` // "none", "block", "allow"
	List []string `json:"list"` // ISO 3166-1 alpha-2, uppercase
}

type BanRules struct {
	WAFHits     int `json:"waf_hits"`
	WAFWindowS  int `json:"waf_window_s"`
	Scan404     int `json:"scan_404"`
	ScanWindowS int `json:"scan_window_s"`
	RateStrikes int `json:"rate_strikes"`
	RateWindowS int `json:"rate_window_s"`
	CodeFails   int `json:"code_fails"`
	CodeWindowS int `json:"code_window_s"`
}

// PolicyOverride is an app's own settings: nil follows the box; the lists add to the box's, the rest replace.
type PolicyOverride struct {
	WAF        *string    `json:"waf"`
	RulesOff   []int      `json:"rules_off"`
	Allow      []string   `json:"allow"`
	Block      []string   `json:"block"`
	Countries  *Countries `json:"countries"`
	RatePerMin *int       `json:"rate_per_min"`
	Ban        *BanRules  `json:"ban"`
}

type GuardApp struct {
	Access   string          `json:"access"` // "open" or "code"
	Override *PolicyOverride `json:"override"`
}

// Person holds a hash of the access code, never the code.
type Person struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Apps      []string  `json:"apps"`
	CodeHash  string    `json:"code_hash"`
	Gen       int       `json:"gen"`
	CreatedAt time.Time `json:"created_at"`
}

type GuardState struct {
	Secret     []byte              `json:"secret"`
	Protection bool                `json:"protection"`
	Box        Policy              `json:"box"`
	Apps       map[string]GuardApp `json:"apps"`
	People     []Person            `json:"people"`
}

func DefaultPolicy() Policy {
	return Policy{
		WAF: "off", RulesOff: []int{}, Allow: []string{}, Block: []string{},
		Countries:  Countries{Mode: "none", List: []string{}},
		RatePerMin: 600,
		Ban: BanRules{WAFHits: 10, WAFWindowS: 600, Scan404: 100, ScanWindowS: 60,
			RateStrikes: 3, RateWindowS: 600, CodeFails: 5, CodeWindowS: 600},
	}
}

// union is a's items then b's new ones, never nil.
func union[T comparable](a, b []T) []T {
	out := []T{}
	for _, v := range append(slices.Clone(a), b...) {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// Effective is the policy an app runs under: the box's, with the override's lists added and its other set fields replacing.
func Effective(box Policy, o *PolicyOverride) Policy {
	p := box
	p.RulesOff, p.Allow, p.Block = union(box.RulesOff, nil), union(box.Allow, nil), union(box.Block, nil)
	p.Countries.List = union(box.Countries.List, nil)
	if o == nil {
		return p
	}
	p.RulesOff, p.Allow, p.Block = union(p.RulesOff, o.RulesOff), union(p.Allow, o.Allow), union(p.Block, o.Block)
	if o.WAF != nil {
		p.WAF = *o.WAF
	}
	if o.Countries != nil {
		p.Countries = Countries{Mode: o.Countries.Mode, List: union(o.Countries.List, nil)}
	}
	if o.RatePerMin != nil {
		p.RatePerMin = *o.RatePerMin
	}
	if o.Ban != nil {
		p.Ban = *o.Ban
	}
	return p
}

var (
	countryRule  = regexp.MustCompile(`^[A-Z]{2}$`)
	personIDRule = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`) // the Gateway's, as are the two others
	codeHashRule = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func validIPOrCIDR(s string) bool {
	if p, err := netip.ParsePrefix(s); err == nil {
		return !p.Addr().Is4In6() // the Gateway sees unmapped addresses: such a range would never match
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}

// Validate refuses what the Gateway would: the error is bad_policy:<field>.
func (p Policy) Validate() error {
	bad := func(f string, a ...any) error { return fmt.Errorf("bad_policy:"+f, a...) }
	if p.WAF != "off" && p.WAF != "observe" && p.WAF != "block" {
		return bad("waf")
	}
	for i, id := range p.RulesOff {
		if id <= 0 {
			return bad("rules_off[%d]", i)
		}
	}
	for i, s := range p.Allow {
		if !validIPOrCIDR(s) {
			return bad("allow[%d]", i)
		}
	}
	for i, s := range p.Block {
		if !validIPOrCIDR(s) {
			return bad("block[%d]", i)
		}
	}
	if m := p.Countries.Mode; m != "none" && m != "block" && m != "allow" {
		return bad("countries.mode")
	}
	for i, c := range p.Countries.List {
		if !countryRule.MatchString(c) {
			return bad("countries.list[%d]", i)
		}
	}
	if p.RatePerMin <= 0 {
		return bad("rate_per_min")
	}
	b := p.Ban
	for _, f := range []struct {
		name string
		v    int
	}{
		{"waf_hits", b.WAFHits}, {"waf_window_s", b.WAFWindowS}, {"scan_404", b.Scan404}, {"scan_window_s", b.ScanWindowS},
		{"rate_strikes", b.RateStrikes}, {"rate_window_s", b.RateWindowS}, {"code_fails", b.CodeFails}, {"code_window_s", b.CodeWindowS},
	} {
		if f.v <= 0 {
			return bad("ban.%s", f.name)
		}
	}
	return nil
}

// NormalizeCode uppercases ASCII letters and drops every character outside the code alphabet, like the Gateway.
func NormalizeCode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
		}
		if strings.IndexByte(codeAlphabet, c) >= 0 {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// CodeHash is the lowercase hex HMAC-SHA256 of the normalised code under secret.
func CodeHash(secret []byte, code string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(NormalizeCode(code)))
	return hex.EncodeToString(m.Sum(nil))
}

// randBytes panics if the system's randomness fails: nothing safe can go on then.
func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// NewCode is 12 random symbols shown as XXXX-XXXX-XXXX. Bytes of 248 and up are redrawn (248 = 8 x 31), so the modulo is unbiased.
func NewCode() string {
	var b strings.Builder
	for i := 0; i < codeLen; i++ {
		v := byte(255)
		for v >= 248 {
			v = randBytes(1)[0]
		}
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(codeAlphabet[int(v)%len(codeAlphabet)])
	}
	return b.String()
}

// newPersonID is p_ and 8 lowercase hex, inside the Gateway's ^[A-Za-z0-9_-]{1,64}$.
func newPersonID() string { return "p_" + hex.EncodeToString(randBytes(4)) }

// newGuard is a first start's state: defaults and a fresh secret, which reaches the disk with the first saveGuard.
func newGuard() GuardState {
	return GuardState{Box: DefaultPolicy(), Apps: map[string]GuardApp{}, Secret: randBytes(32)}
}

// loadGuard reads the file. Missing: newGuard. Unreadable or unusable: newGuard and corrupt is true, so the caller can
// fail closed; the file is left in place.
func loadGuard(path string) (g GuardState, corrupt bool) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return newGuard(), false
	}
	if err != nil {
		return newGuard(), true
	}
	return parseGuard(data)
}

// parseGuard is the state saved in data; corrupt is true, with newGuard, when the Gateway could not take it.
func parseGuard(data []byte) (g GuardState, corrupt bool) {
	g = GuardState{Box: DefaultPolicy()}
	if json.Unmarshal(data, &g) != nil || len(g.Secret) != 32 || !g.valid() {
		return newGuard(), true
	}
	if g.Apps == nil {
		g.Apps = map[string]GuardApp{}
	}
	return g, false
}

// valid is whether the Gateway would take the box's policy and every app's: one it refuses would fail every push.
func (g GuardState) valid() bool {
	if g.Box.Validate() != nil {
		return false
	}
	for _, a := range g.Apps {
		if Effective(g.Box, a.Override).Validate() != nil {
			return false
		}
	}
	for _, p := range g.People {
		if !personIDRule.MatchString(p.ID) || !codeHashRule.MatchString(p.CodeHash) || p.Gen < 0 {
			return false
		}
	}
	return true
}

func saveGuard(path string, g GuardState) error {
	data, err := json.Marshal(g)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return file.WriteFileAtomic(path, data) // created 0600
}
