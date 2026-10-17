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
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Schema is the one version of the manifest that is read: a change of the format is a new number, so that it is never guessed at.
const Schema = 1

// MaxBytes is the most of a manifest that is read.
const MaxBytes = 64 << 10

// A Manifest is what a module says of itself.
type Manifest struct {
	Schema int `json:"schema"`
	// Name is the short name of the module ("virt"): the name of its file, and what the core calls it.
	Name string `json:"name"`
	// Title and Description are what the dashboard shows of the module, as text: printable characters and nothing else.
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
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
	unitRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@-]{0,63}\.service$`)
	routeRe     = regexp.MustCompile(`^/v[0-9]{1,2}/[a-z][a-z0-9_]{0,39}$`)
	callRe      = regexp.MustCompile(`^/[a-z0-9_.-]+(/[a-z0-9_.-]+){0,7}$`)
	packageRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{1,63}$`)
	versionRe   = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)
	osRe        = regexp.MustCompile(`^(debian|ubuntu):[0-9]{1,2}(\.[0-9]{2})?$`)
)

// the bounds of the lists and of the wait
const (
	maxPackages     = 32
	maxPackagesAll  = 40
	maxOS           = 16
	maxStopUnits    = 8
	maxStopSeconds  = 3600
	maxTitleRunes   = 60
	maxDescRunes    = 300
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
	if err := decodeStrict(raw, &m); err != nil {
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
	if n := utf8.RuneCountInString(m.Title); n > maxTitleRunes || !printable(m.Title) || strings.TrimSpace(m.Title) == "" {
		return invalid("title", ReasonValue)
	}
	if utf8.RuneCountInString(m.Description) > maxDescRunes || !printable(m.Description) {
		return invalid("description", ReasonValue)
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
		if err := listOf(c.field, c.list, ValidPackageName, maxPackages); err != nil {
			return err
		}
	}
	if len(m.Packages.Common)+len(m.Packages.AMD64)+len(m.Packages.ARM64) > maxPackagesAll {
		return invalid("packages", ReasonTooMany)
	}
	if len(m.OS) == 0 {
		return invalid("os", ReasonMissing)
	}
	if err := listOf("os", m.OS, osRe.MatchString, maxOS); err != nil {
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
	if err := listOf("os_upgrade.stop_units", u.StopUnits, unitRe.MatchString, maxStopUnits); err != nil {
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

// listOf checks that every element is valid and that there are no more than max, and none twice.
func listOf(field string, values []string, valid func(string) bool, max int) error {
	if len(values) > max {
		return invalid(field, ReasonTooMany)
	}
	for i, v := range values {
		if !valid(v) || slices.Index(values, v) != i {
			return invalid(field, ReasonValue)
		}
	}
	return nil
}

// ValidPackageName says whether a name is one that may be handed to apt as a package: lower case letters, digits, "+", "." and "-", two to 64 characters, the first a letter or a digit.
// A name that ends in "-" or "+" is refused although the pattern of Debian takes it, because the command line of apt reads that last character as an order ("pkg-" removes pkg), and so is
// one that ends in ".deb", which apt reads as the path of a file.
func ValidPackageName(s string) bool {
	return packageRe.MatchString(s) && !strings.HasSuffix(s, "-") && !strings.HasSuffix(s, "+") && !strings.HasSuffix(s, ".deb")
}

// printable says whether a text is made of printable characters only: letters, marks, numbers, punctuation, symbols and the space, and not the replacement character that stands for
// bytes that were not text. A text goes to the dashboard as it is, and a control, a mark that turns the line round or a space that cannot be seen is no part of a title.
func printable(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) || r == utf8.RuneError })
}

// decodeStrict decodes the JSON text raw into dst, a pointer to a struct, and refuses what another reader of the same text (jq in the installer, the dashboard) could read otherwise: bytes
// that are not UTF-8 (the decoder of Go turns them into U+FFFD), a key that comes twice, a key that is a known one only because the decoder of Go folds the case (it takes "NAME" for
// "name", and the long s for an s), a field that is not known; words after the value are an error of the decoder.
func decodeStrict(raw []byte, dst any) error {
	if !utf8.Valid(raw) {
		return errors.New("not UTF-8")
	}
	if err := noRepeatedKey(raw); err != nil {
		return err
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return err
	}
	if !exactKeys(tree, reflect.TypeOf(dst)) {
		return errors.New("a key is not a field")
	}
	return json.Unmarshal(raw, dst)
}

// exactKeys says whether every key of every object of the tree (the generic reading of a document) is, letter for letter, the name that a field of the type t has in its json tag.
func exactKeys(tree any, t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch v := tree.(type) {
	case map[string]any:
		if t.Kind() != reflect.Struct {
			return true // (what the type does not take, the decoder says)
		}
		for key, child := range v {
			field, ok := fieldTagged(t, key)
			if !ok || !exactKeys(child, field.Type) {
				return false
			}
		}
	case []any:
		if t.Kind() == reflect.Slice {
			for _, child := range v {
				if !exactKeys(child, t.Elem()) {
					return false
				}
			}
		}
	}
	return true
}

func fieldTagged(t reflect.Type, key string) (reflect.StructField, bool) {
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name == key {
			return t.Field(i), true
		}
	}
	return reflect.StructField{}, false
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
