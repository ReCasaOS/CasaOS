package fleet

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestCodeRoundTrip(t *testing.T) {
	c := Code{MasterURL: "https://master.example", BoxID: "6f1c2d3e", Key: strings.Repeat("ab", 32), CertSHA256: strings.Repeat("cd", 32)}
	got, err := ParseCode(c.String())
	if err != nil || got != c {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestParseCodeRefuses(t *testing.T) {
	for name, s := range map[string]string{
		"not base64":  "!!!",
		"not json":    base64.RawURLEncoding.EncodeToString([]byte("x")),
		"no key":      Code{MasterURL: "https://m", BoxID: "b"}.String(),
		"no box":      Code{MasterURL: "https://m", Key: "k"}.String(),
		"http remote": Code{MasterURL: "http://m.example", BoxID: "b", Key: "k"}.String(),
		"user info":   Code{MasterURL: "https://u:p@m.example", BoxID: "b", Key: "k"}.String(),
		"bad fp":      Code{MasterURL: "https://m", BoxID: "b", Key: "k", CertSHA256: "zz"}.String(),
	} {
		if _, err := ParseCode(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidMasterURL(t *testing.T) {
	for raw, ok := range map[string]bool{"https://m.example": true, "https://100.64.0.1:8443": true, "http://127.0.0.1:80": true, "http://[::1]:443": true, "http://10.0.0.2": false, "ftp://m": false, "https://m/?x": false, "": false} {
		if (ValidMasterURL(raw) == nil) != ok {
			t.Errorf("%q", raw)
		}
	}
}

func TestNewKeyIsRandomAndHashed(t *testing.T) {
	k1, h1, _ := NewKey()
	k2, _, _ := NewKey()
	if len(k1) != 64 || k1 == k2 || h1 != KeyHash(k1) {
		t.Fatal("key")
	}
}
