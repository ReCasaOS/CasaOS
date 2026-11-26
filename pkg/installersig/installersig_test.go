package installersig

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sign(priv ed25519.PrivateKey, msg []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg)) + "\n")
}

func TestVerify(t *testing.T) {
	pubA, privA := newKey(t)
	pubB, privB := newKey(t)
	_, privC := newKey(t)
	keys := []ed25519.PublicKey{pubA, pubB}
	script := []byte("#!/bin/bash\necho hi\n")
	good := sign(privA, script)
	tampered := append([]byte(nil), script...)
	tampered[0] ^= 1
	cases := []struct {
		name   string
		script []byte
		sig    []byte
		keys   []ed25519.PublicKey
		ok     bool
	}{
		{"signed by A", script, good, keys, true},
		{"signed by B", script, sign(privB, script), keys, true},
		{"no trailing newline", script, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(privA, script))), keys, true},
		{"unknown key", script, sign(privC, script), keys, false},
		{"truncated", script, good[:len(good)-10], keys, false},
		{"invalid base64", script, []byte("not base64 !!"), keys, false},
		{"empty signature", script, nil, keys, false},
		{"modified message", tampered, good, keys, false},
		{"no keys", script, good, nil, false},
	}
	for _, c := range cases {
		err := Verify(c.script, c.sig, c.keys)
		if c.ok && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if !c.ok && !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: got %v, want ErrBadSignature", c.name, err)
		}
	}
}

func TestKeysDefaultAndRestore(t *testing.T) {
	old := Keys
	defer func() { Keys = old }()
	pub, _ := newKey(t)
	Keys = []ed25519.PublicKey{pub}
	if len(Keys) != 1 {
		t.Fatal("Keys not replaceable")
	}
}

func TestReleaseTag(t *testing.T) {
	real := "set -e\nreadonly CASAOS_INSTALL_RELEASE_TAG=\"v0.5.202\"\nreadonly CASAOS_RELEASE_BASE_URL=\"https://x/${CASAOS_INSTALL_RELEASE_TAG}\"\n"
	cases := []struct {
		name, script, want string
		err                error
	}{
		{"real line", real, "v0.5.202", nil},
		{"same twice", real + real, "v0.5.202", nil},
		{"placeholder", "readonly CASAOS_INSTALL_RELEASE_TAG=\"__CASAOS_RELEASE_TAG__\"\n", "", ErrNoTag},
		{"absent", "echo hi\n", "", ErrNoTag},
		{"two different", real + "readonly CASAOS_INSTALL_RELEASE_TAG=\"v0.5.203\"\n", "", ErrNoTag},
		{"bad shape", "readonly CASAOS_INSTALL_RELEASE_TAG=\"v0.5\"\n", "", ErrNoTag},
		{"indented is not the line", "  readonly CASAOS_INSTALL_RELEASE_TAG=\"v0.5.202\"\n", "", ErrNoTag},
	}
	for _, c := range cases {
		got, err := ReleaseTag([]byte(c.script))
		if got != c.want || !errors.Is(err, c.err) && err != c.err {
			t.Errorf("%s: got %q, %v", c.name, got, err)
		}
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.5.199", "v0.5.99", true},
		{"v0.5.99", "v0.5.199", false},
		{"v0.5.99", "v0.5.99", false},
		{"v1.0.0", "v0.9.9", true},
		{"v0.6.0", "v0.5.9", true},
		{"0.5.9", "v0.5.1", false},
		{"v0.5.9", "bad", false},
		{"v0.5", "v0.5.1", false},
		{"v0.5.9-rc1", "v0.5.1", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q,%q)=%v", c.a, c.b, got)
		}
	}
}
