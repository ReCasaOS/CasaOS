// Package osmodule reads the manifest of a ReCasaOS module: a component that is installed on its own (a service of its own, packages of its own) and that the core shows only when
// it is there. The manifest ships in the release archive of the module, and the installer copies it to /etc/casaos/modules/<name>.json.
//
// The file is data that the core can trust only as far as it validates it. Every name, unit, path and number in it is checked against a fixed pattern, an unknown field is an error,
// and nothing in it is ever run as a command, interpolated into a shell or opened as a path outside what the pattern allows: a manifest that does not pass is a module that
// cannot be told, and the core says so instead of guessing.
package osmodule

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

// Schema is the one version of the manifest that is read: a change of the format is a new number, so that it is never guessed at.
const Schema = 1

// MaxBytes is the most of a manifest that is read.
const MaxBytes = 64 << 10

// A Manifest is what a module says of itself.
type Manifest struct {
	Schema int `json:"schema"`
	// Name is the short name of the module ("virt"): the name of its file, and what the core calls it.
	Name  string `json:"name"`
	Title string `json:"title"`
	// Component is the name of the component that is released (the repository), Unit its systemd unit, GatewayRoute where the gateway reaches it.
	Component    string   `json:"component"`
	Unit         string   `json:"unit"`
	GatewayRoute string   `json:"gateway_route"`
	Packages     Packages `json:"packages"`
	// MinReCasaOS is the oldest release of ReCasaOS that the module works with; OS the operating systems it supports ("debian:12", "ubuntu:24.04").
	MinReCasaOS string   `json:"min_recasaos"`
	OS          []string `json:"os"`
	// OSUpgrade is what the module needs around an upgrade of the operating system; a module that does not say is one that the upgrade refuses.
	OSUpgrade *OSUpgrade `json:"os_upgrade,omitempty"`
}

// Packages are the packages of the operating system that the module needs, for every architecture and for each.
type Packages struct {
	Common []string `json:"common,omitempty"`
	AMD64  []string `json:"amd64,omitempty"`
	ARM64  []string `json:"arm64,omitempty"`
}

// An OSUpgrade is what the core does around an upgrade of the operating system, declared and never scripted: the units it stops (after the module has said that it is ready), how long
// it waits for that, and three paths of the module's own API that it calls through the gateway with the internal secret.
type OSUpgrade struct {
	StopUnits          []string    `json:"stop_units"`
	StopTimeoutSeconds int         `json:"stop_timeout_seconds"`
	Maintenance        Maintenance `json:"maintenance"`
}

// Maintenance are the three calls: prepare (POST, idempotent) asks the module to get ready, status says whether it is, and resume is made once the unit is back after the restart and the
// module has checked what it depends on.
type Maintenance struct {
	Prepare string `json:"prepare"`
	Status  string `json:"status"`
	Resume  string `json:"resume"`
}

// An Invalid is why a manifest was not taken: the field that is wrong (a path through the manifest, from a fixed alphabet) and a code.
type Invalid struct {
	Field  string
	Reason string
}

func (e *Invalid) Error() string { return "the manifest is not valid: " + e.Field + " " + e.Reason }

// the reasons of an Invalid
const (
	ReasonUnreadable = "unreadable"
	ReasonTooLarge   = "too_large"
	ReasonFile       = "file_name"
	ReasonSchema     = "schema"
	ReasonValue      = "value"
	ReasonMissing    = "missing"
	ReasonTooMany    = "too_many"
)

func invalid(field, reason string) error { return &Invalid{Field: field, Reason: reason} }

var (
	nameRe      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	componentRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,63}$`)
	unitRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@-]{0,100}\.service$`)
	routeRe     = regexp.MustCompile(`^/v[0-9]{1,2}/[a-z][a-z0-9_]{0,63}$`)
	callRe      = regexp.MustCompile(`^/[a-z0-9_.-]+(/[a-z0-9_.-]+){0,7}$`)
	packageRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{1,63}$`)
	versionRe   = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)
	osRe        = regexp.MustCompile(`^(debian|ubuntu):[0-9]{1,2}(\.[0-9]{2})?$`)
)

// the bounds of the lists and of the wait
const (
	maxPackages     = 32
	maxOS           = 16
	maxStopUnits    = 8
	maxStopSeconds  = 3600
	maxTitleRunes   = 64
	manifestSuffix  = ".json"
	maxCallSegments = 8
)

// FileName is the name of the file of a module.
func FileName(name string) string { return name + manifestSuffix }

// Parse reads the manifest that is in the file named file (the base name, "virt.json") and checks everything in it. The name inside must be the name of the file.
func Parse(file string, raw []byte) (Manifest, error) {
	var m Manifest
	name, ok := strings.CutSuffix(file, manifestSuffix)
	if !ok || !nameRe.MatchString(name) {
		return m, invalid("file", ReasonFile)
	}
	if len(raw) > MaxBytes {
		return m, invalid("file", ReasonTooLarge)
	}
	if err := noRepeatedKey(raw); err != nil {
		return m, invalid("file", ReasonUnreadable)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return Manifest{}, invalid("file", ReasonUnreadable)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Manifest{}, invalid("file", ReasonUnreadable)
	}
	if err := validate(m, name); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func validate(m Manifest, name string) error {
	if m.Schema != Schema {
		return invalid("schema", ReasonSchema)
	}
	if m.Name != name {
		return invalid("name", ReasonValue)
	}
	if n := len([]rune(m.Title)); n == 0 || n > maxTitleRunes || strings.ContainsFunc(m.Title, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return invalid("title", ReasonValue)
	}
	for _, c := range []struct {
		field, value string
		re           *regexp.Regexp
	}{
		{"component", m.Component, componentRe}, {"unit", m.Unit, unitRe}, {"gateway_route", m.GatewayRoute, routeRe}, {"min_recasaos", m.MinReCasaOS, versionRe},
	} {
		if c.value == "" {
			return invalid(c.field, ReasonMissing)
		}
		if !c.re.MatchString(c.value) {
			return invalid(c.field, ReasonValue)
		}
	}
	for _, c := range []struct {
		field string
		list  []string
	}{{"packages.common", m.Packages.Common}, {"packages.amd64", m.Packages.AMD64}, {"packages.arm64", m.Packages.ARM64}} {
		if err := list(c.field, c.list, packageRe, maxPackages); err != nil {
			return err
		}
	}
	if len(m.OS) == 0 {
		return invalid("os", ReasonMissing)
	}
	if err := list("os", m.OS, osRe, maxOS); err != nil {
		return err
	}
	if m.OSUpgrade != nil {
		return validateOSUpgrade(*m.OSUpgrade, m.GatewayRoute)
	}
	return nil
}

func validateOSUpgrade(u OSUpgrade, route string) error {
	if len(u.StopUnits) == 0 {
		return invalid("os_upgrade.stop_units", ReasonMissing)
	}
	if err := list("os_upgrade.stop_units", u.StopUnits, unitRe, maxStopUnits); err != nil {
		return err
	}
	if u.StopTimeoutSeconds < 1 || u.StopTimeoutSeconds > maxStopSeconds {
		return invalid("os_upgrade.stop_timeout_seconds", ReasonValue)
	}
	for _, c := range []struct{ field, value string }{
		{"os_upgrade.maintenance.prepare", u.Maintenance.Prepare}, {"os_upgrade.maintenance.status", u.Maintenance.Status}, {"os_upgrade.maintenance.resume", u.Maintenance.Resume},
	} {
		if c.value == "" {
			return invalid(c.field, ReasonMissing)
		}
		// a call is a path under the module's own route, made of the characters of a path and nothing that goes up, empties a segment or asks for a query
		rest, ok := strings.CutPrefix(c.value, route)
		if !ok || !callRe.MatchString(rest) || strings.Contains(c.value, "..") || strings.Contains(c.value, "//") {
			return invalid(c.field, ReasonValue)
		}
	}
	return nil
}

// list checks that every element matches and that there are no more than max, and none twice.
func list(field string, values []string, re *regexp.Regexp, max int) error {
	if len(values) > max {
		return invalid(field, ReasonTooMany)
	}
	for i, v := range values {
		if !re.MatchString(v) || slices.Index(values, v) != i {
			return invalid(field, ReasonValue)
		}
	}
	return nil
}

// noRepeatedKey walks the tokens of a JSON document and refuses a key that comes twice in one object: the decoder would take the last, and another reader of the same file (the installer's
// jq) another.
func noRepeatedKey(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var seen []map[string]bool
	var keyNext []bool
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := token.(type) {
		case json.Delim:
			switch t {
			case '{':
				seen = append(seen, map[string]bool{})
				keyNext = append(keyNext, true)
			case '[':
				seen = append(seen, nil)
				keyNext = append(keyNext, false)
			default:
				seen, keyNext = seen[:len(seen)-1], keyNext[:len(keyNext)-1]
				if n := len(keyNext); n > 0 && seen[n-1] != nil {
					keyNext[n-1] = true
				}
			}
			continue
		case string:
			if n := len(keyNext); n > 0 && seen[n-1] != nil && keyNext[n-1] {
				if seen[n-1][t] {
					return fmt.Errorf("a key comes twice")
				}
				seen[n-1][t] = true
				keyNext[n-1] = false
				continue
			}
		}
		if n := len(keyNext); n > 0 && seen[n-1] != nil {
			keyNext[n-1] = true
		}
	}
}
