package share

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestCodeHashVector(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i)
	}
	want := "499b46abdc484007465c1451c284ae4485de2006d52fd01cc6fa0f0c6eed12c2"
	if got := CodeHash(secret, "abcd-efgh-jkmn"); got != want {
		t.Fatalf("got %s", got)
	}
	if got := NormalizeCode("ab0c-dEfi_l1é"); got != "ABCDEF" {
		t.Fatalf("got %q", got)
	}
}

func TestNewCodeShape(t *testing.T) {
	a := "[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{4}"
	re := regexp.MustCompile("^" + a + "-" + a + "-" + a + "$")
	seen := map[string]bool{}
	for range 1000 {
		c := NewCode()
		if !re.MatchString(c) || seen[c] {
			t.Fatalf("bad or repeated code %q", c)
		}
		seen[c] = true
	}
	if id := newPersonID(); !regexp.MustCompile(`^p_[0-9a-f]{8}$`).MatchString(id) {
		t.Fatal(id)
	}
}

func TestEffectiveMerges(t *testing.T) {
	box := DefaultPolicy()
	box.Allow = []string{"A"}
	box.Countries = Countries{Mode: "block", List: []string{"FR"}}
	if got := Effective(box, nil); !reflect.DeepEqual(got, box) {
		t.Fatalf("nil override changed the box: %+v", got)
	}
	waf, rate := "block", 50
	got := Effective(box, &PolicyOverride{WAF: &waf, Allow: []string{"B", "A"}, RulesOff: []int{942100},
		Countries: &Countries{Mode: "allow", List: []string{"DE"}}, RatePerMin: &rate})
	if !slices.Equal(got.Allow, []string{"A", "B"}) || got.WAF != "block" || got.RatePerMin != 50 ||
		!reflect.DeepEqual(got.Countries, Countries{Mode: "allow", List: []string{"DE"}}) ||
		!slices.Equal(got.RulesOff, []int{942100}) || got.Ban != box.Ban || got.Block == nil {
		t.Fatalf("got %+v", got)
	}
	if !slices.Equal(box.Allow, []string{"A"}) {
		t.Fatal("box mutated")
	}
}

func TestDefaultPolicyValues(t *testing.T) {
	p := DefaultPolicy()
	want := Policy{WAF: "off", RulesOff: []int{}, Allow: []string{}, Block: []string{},
		Countries: Countries{Mode: "none", List: []string{}}, RatePerMin: 600,
		Ban: BanRules{10, 600, 100, 60, 3, 600, 5, 600}}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("got %+v", p)
	}
	if p.Validate() != nil {
		t.Fatal(p.Validate())
	}
}

func TestPolicyValidate(t *testing.T) {
	for want, mut := range map[string]func(*Policy){
		"bad_policy:rate_per_min":      func(p *Policy) { p.RatePerMin = 0 },
		"bad_policy:countries.list[0]": func(p *Policy) { p.Countries.List = []string{"fr"} },
		"bad_policy:allow[0]":          func(p *Policy) { p.Allow = []string{"x"} },
		"bad_policy:block[1]":          func(p *Policy) { p.Block = []string{"1.2.3.4", "::ffff:1.2.3.0/120"} },
		"bad_policy:ban.scan_404":      func(p *Policy) { p.Ban.Scan404 = 0 },
		"bad_policy:waf":               func(p *Policy) { p.WAF = "on" },
		"bad_policy:countries.mode":    func(p *Policy) { p.Countries.Mode = "x" },
		"bad_policy:rules_off[0]":      func(p *Policy) { p.RulesOff = []int{0} },
	} {
		p := DefaultPolicy()
		mut(&p)
		if err := p.Validate(); err == nil || err.Error() != want {
			t.Errorf("want %s, got %v", want, err)
		}
	}
	p := DefaultPolicy()
	p.Allow = []string{"192.0.2.0/24", "2001:db8::1"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadGuardMissing(t *testing.T) {
	g, corrupt := loadGuard(filepath.Join(t.TempDir(), "none.json"))
	if corrupt || g.Protection || len(g.Secret) != 32 || !reflect.DeepEqual(g.Box, DefaultPolicy()) || g.Apps == nil {
		t.Fatalf("got %+v", g)
	}
}

func TestSaveGuardNoCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet", GuardFile)
	g, _ := loadGuard(path)
	g.People = []Person{{ID: newPersonID(), Name: "Maman", CodeHash: CodeHash(g.Secret, NewCode())}}
	if err := saveGuard(path, g); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"code_hash"`) || strings.Contains(string(raw), `"code"`) {
		t.Fatalf("file: %s", raw)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, err %v", info.Mode(), err)
		}
	}
	back, _ := loadGuard(path)
	if !slices.Equal(back.Secret, g.Secret) || len(back.People) != 1 {
		t.Fatalf("round trip: %+v", back)
	}
}

func TestLoadGuardCorrupt(t *testing.T) {
	secret := `"secret":"` + base64.StdEncoding.EncodeToString(make([]byte, 32)) + `"`
	for name, content := range map[string]string{"garbage": "{nope", "short secret": `{"secret":"AAECAwQFBgcICQoLDA0ODw==","protection":true}`,
		"bad box":       `{` + secret + `,"box":{"waf":"nope"}}`,
		"bad override":  `{` + secret + `,"apps":{"jellyfin":{"access":"code","override":{"allow":["nope"]}}}}`,
		"bad person id": `{` + secret + `,"people":[{"id":"p 1","gen":1,"code_hash":"` + strings.Repeat("a", 64) + `"}]}`,
		"bad code hash": `{` + secret + `,"people":[{"id":"p_1","gen":1,"code_hash":"` + strings.Repeat("A", 64) + `"}]}`,
		"negative gen":  `{` + secret + `,"people":[{"id":"p_1","gen":-1,"code_hash":"` + strings.Repeat("a", 64) + `"}]}`} {
		dir := t.TempDir()
		path := filepath.Join(dir, GuardFile)
		os.WriteFile(path, []byte(content), 0o600)
		g, corrupt := loadGuard(path)
		if !corrupt || g.Protection || len(g.Secret) != 32 || !reflect.DeepEqual(g.Box, DefaultPolicy()) {
			t.Fatalf("%s: got %+v corrupt=%v", name, g, corrupt)
		}
		if raw, _ := os.ReadFile(path); string(raw) != content { // the Service replaces it, then keeps a copy aside
			t.Fatalf("%s: file changed", name)
		}
	}
}

func TestRelayFilterDefaultsOn(t *testing.T) {
	off := false
	for _, c := range []struct {
		g    GuardState
		want bool
	}{{GuardState{}, true}, {GuardState{RelayFilter: &off}, false}} {
		if got := c.g.relayFilter(); got != c.want {
			t.Errorf("%+v: %v", c.g, got)
		}
	}
}
