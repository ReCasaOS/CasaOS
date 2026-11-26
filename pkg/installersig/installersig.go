// Package installersig checks the Ed25519 signature of an installer script
// and reads the release the script announces.
package installersig

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrBadSignature = errors.New("installer signature is not valid")
	ErrNoTag        = errors.New("installer announces no valid release tag")
)

// Verify checks that sigBase64 (one line of standard base64 of a raw
// Ed25519 signature) signs script under at least one of keys.
func Verify(script, sigBase64 []byte, keys []ed25519.PublicKey) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigBase64)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	for _, k := range keys {
		if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, script, sig) {
			return nil
		}
	}
	return ErrBadSignature
}

var (
	tagLine = regexp.MustCompile(`(?m)^readonly CASAOS_INSTALL_RELEASE_TAG="([^"]*)"\r?$`)
	tagForm = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)
)

// ReleaseTag returns the vX.Y.Z value of the script's
// CASAOS_INSTALL_RELEASE_TAG line. Placeholders, absent lines and lines
// with different values give ErrNoTag.
func ReleaseTag(script []byte) (string, error) {
	tag := ""
	for _, m := range tagLine.FindAllSubmatch(script, -1) {
		v := string(m[1])
		if !tagForm.MatchString(v) || (tag != "" && tag != v) {
			return "", ErrNoTag
		}
		tag = v
	}
	if tag == "" {
		return "", ErrNoTag
	}
	return tag, nil
}

func parse(tag string) (v [3]int, ok bool) {
	m := tagForm.FindStringSubmatch(tag)
	if m == nil {
		return v, false
	}
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// Newer reports whether tag a is a higher vX.Y.Z than b. An invalid tag
// on either side gives false.
func Newer(a, b string) bool {
	x, okA := parse(a)
	y, okB := parse(b)
	if !okA || !okB {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}
