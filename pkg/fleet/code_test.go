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

func TestCodeRoundTripWithRelay(t *testing.T) {
	c := Code{MasterURL: "https://100.64.0.1:443", BoxID: "6f1c2d3e", Key: strings.Repeat("ab", 32), Relay: "https://relay.example", JoinKey: realJoinKey}
	got, err := ParseCode(c.String())
	if err != nil || got != c {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// oldCode was made by the build before the relay fields existed: base64url of
// {"u","b","k","f"}. It must keep parsing, with no relay.
const oldCode = "eyJ1IjoiaHR0cHM6Ly9tYXN0ZXIuZXhhbXBsZSIsImIiOiI2ZjFjMmQzZSIsImsiOiJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiIiwiZiI6ImNkY2RjZGNkY2RjZGNkY2RjZGNkY2RjZGNkY2RjZGNkY2RjZGNkY2RjZGNkY2RjZGNkY2RjZGNkY2RjZGNkY2QifQ"

func TestParseCodeAcceptsOldCodes(t *testing.T) {
	got, err := ParseCode(oldCode)
	want := Code{MasterURL: "https://master.example", BoxID: "6f1c2d3e", Key: strings.Repeat("ab", 32), CertSHA256: strings.Repeat("cd", 32)}
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestParseCodeRefusesPlainHTTPRelay(t *testing.T) {
	base := Code{MasterURL: "https://m", BoxID: "b", Key: "k", JoinKey: realJoinKey}
	for name, relay := range map[string]string{
		"http remote": "http://evil",
		"user info":   "https://u:p@relay.example",
		"query":       "https://relay.example/?x",
		"ftp":         "ftp://relay.example",
	} {
		c := base
		c.Relay = relay
		if _, err := ParseCode(c.String()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	c := base
	c.Relay = "http://127.0.0.1:8080"
	if _, err := ParseCode(c.String()); err != nil {
		t.Errorf("loopback relay refused: %v", err)
	}
}

func TestParseCodeRelayNeedsJoinKey(t *testing.T) {
	for name, c := range map[string]Code{
		"relay without join key": {MasterURL: "https://m", BoxID: "b", Key: "k", Relay: "https://relay.example"},
		"join key without relay": {MasterURL: "https://m", BoxID: "b", Key: "k", JoinKey: "join-secret-0000-1111"},
	} {
		_, err := ParseCode(c.String())
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "join-secret") {
			t.Errorf("%s: error leaks the join key", name)
		}
	}
}

// realJoinKey has the shape of a Headscale 0.29 pre-auth key: "hskey-auth-", a
// 12-character prefix, "-", a 64-character secret, both URL-safe base64.
const realJoinKey = "hskey-auth-Ab3_dE-9xYz0-" + "Qw3_rTy-uIoP4aSdFgHjKlZx_cVbNm12QwErTyUiOpAsDfGhJkLzXcVbNm34Q-eR"

// tailscale reads --authkey=file:<path> as a file to read, as root, and a key
// starting with '-' could read as a flag: only the shapes a relay hands out pass.
func TestValidJoinKey(t *testing.T) {
	for _, k := range []string{realJoinKey, strings.Repeat("a1", 24), "join-key-77c1-0000", strings.Repeat("x", 256)} {
		if err := validJoinKey(k); err != nil {
			t.Errorf("%q refused: %v", k, err)
		}
	}
	for name, k := range map[string]string{
		"file":     "file:/etc/shadow",
		"flag":     "--x" + strings.Repeat("a", 20),
		"dash":     "-" + strings.Repeat("a", 20),
		"spaces":   "hskey auth " + strings.Repeat("a", 20),
		"slash":    "hskey-auth/" + strings.Repeat("a", 20),
		"colon":    "hskey:" + strings.Repeat("a", 20),
		"short":    "hskey-1",
		"overlong": strings.Repeat("x", 257),
		"newline":  strings.Repeat("a", 20) + "\n",
		"empty":    "",
	} {
		if validJoinKey(k) == nil {
			t.Errorf("%s: %q accepted", name, k)
		}
	}
	c := Code{MasterURL: "https://m", BoxID: "b", Key: "k", Relay: "https://relay.example", JoinKey: "file:/etc/shadow"}
	if _, err := ParseCode(c.String()); err == nil {
		t.Error("a code with a file: join key was accepted")
	} else if strings.Contains(err.Error(), "shadow") {
		t.Errorf("the error quotes the key: %v", err)
	}
	c.JoinKey = realJoinKey
	if _, err := ParseCode(c.String()); err != nil {
		t.Errorf("a real join key was refused: %v", err)
	}
}

func TestValidMasterURLAcceptsTailnetAddress(t *testing.T) {
	for _, raw := range []string{"https://100.64.0.1:443", "https://box.f-ab12.relay.example"} {
		if err := ValidMasterURL(raw); err != nil {
			t.Errorf("%q: %v", raw, err)
		}
	}
}
