package osmodule

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// withRaw is with() for a value given as the text of a JSON literal, so that a case can say an escape that the encoder of Go would write as the character, or a number
// that is not an integer. An empty literal removes the key.
func withRaw(t *testing.T, path, literal string) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(virtManifest), &doc); err != nil {
		t.Fatal(err)
	}
	at := doc
	keys := strings.Split(path, ".")
	for _, k := range keys[:len(keys)-1] {
		at = at[k].(map[string]any)
	}
	if literal == "" {
		delete(at, keys[len(keys)-1])
	} else {
		at[keys[len(keys)-1]] = json.RawMessage(literal)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func quoted(s string) string { return `"` + s + `"` }

func rep(s string, n int) string { return strings.Repeat(s, n) }

// The rules of the contract that the reader did not have, and the ones it had, each tried on both sides of its bound.
func TestTheBoundsOfTheTextsAndOfTheNames(t *testing.T) {
	unit := func(n int) string { return quoted(rep("u", n) + ".service") }
	route := func(n int) string { return quoted("/v2/" + rep("r", n)) }
	for _, c := range []struct {
		name, path, literal string
		field               string // empty when the manifest is taken
	}{
		{"a title of 60 letters", "title", quoted(rep("t", 60)), ""},
		{"a title of 61 letters", "title", quoted(rep("t", 61)), "title"},
		{"a title of 60 characters that take two bytes each", "title", quoted(rep("é", 60)), ""},
		{"a title of 61 characters that take two bytes each", "title", quoted(rep("é", 61)), "title"},
		{"a title that is only spaces", "title", `"   "`, "title"},
		{"a title with a no-break space", "title", `"a\u00a0b"`, "title"},
		{"a title with a zero width space", "title", `"a\u200bb"`, "title"},
		{"a title that turns the text round", "title", `"a\u202eb"`, "title"},
		{"a title with a line separator", "title", `"a\u2028b"`, "title"},
		{"a title with a control of the second block", "title", `"a\u0085b"`, "title"},
		{"a title with a delete", "title", `"a\u007fb"`, "title"},
		{"a title with a NUL", "title", `"a\u0000b"`, "title"},
		{"a title with the replacement character", "title", `"a\ufffdb"`, "title"},
		{"a title with half a surrogate pair", "title", `"a\ud800b"`, "title"},
		{"a title with a tab", "title", `"a\tb"`, "title"},
		{"a title of 60 KiB", "title", quoted(rep("t", 60<<10)), "title"},
		{"a title that is not a text", "title", `5`, "file"},
		{"a title in French", "title", quoted("Machines virtuelles à l'école"), ""},
		{"no description", "description", ``, ""},
		{"an empty description", "description", `""`, ""},
		{"a description of 300 letters", "description", quoted(rep("d", 300)), ""},
		{"a description of 301 letters", "description", quoted(rep("d", 301)), "description"},
		{"a description of 300 characters that take three bytes each", "description", quoted(rep("€", 300)), ""},
		{"a description of 301 characters that take three bytes each", "description", quoted(rep("€", 301)), "description"},
		{"a description with a new line", "description", `"a\nb"`, "description"},
		{"a description that turns the text round", "description", `"a\u2067b"`, "description"},
		{"a description with a NUL", "description", `"a\u0000b"`, "description"},
		{"a description with a form feed", "description", `"a\fb"`, "description"},
		{"a unit of 64 characters before the suffix", "unit", unit(64), ""},
		{"a unit of 65 characters before the suffix", "unit", unit(65), "unit"},
		{"a unit that starts with a dash", "unit", `"-x.service"`, "unit"},
		{"a unit with a space", "unit", `"a b.service"`, "unit"},
		{"a unit with a slash", "unit", `"a/b.service"`, "unit"},
		{"a unit with a new line before the suffix", "unit", `"a\n.service"`, "unit"},
		{"a unit with a new line at the end", "unit", `"a.service\n"`, "unit"},
		{"a route with 40 characters after the version", "gateway_route", route(40), ""},
		{"a route with 41 characters after the version", "gateway_route", route(41), "gateway_route"},
		{"a route with a three digit version", "gateway_route", `"/v200/x"`, "gateway_route"},
		{"a route with a dash", "gateway_route", `"/v2/virt-management"`, "gateway_route"},
		{"a route with a trailing slash", "gateway_route", `"/v2/virt/"`, "gateway_route"},
		{"a route with a new line at the end", "gateway_route", `"/v2/virt\n"`, "gateway_route"},
		{"a version with a letter", "min_recasaos", `"0.5.x"`, "min_recasaos"},
		{"a version with a v", "min_recasaos", `"v0.5.23"`, "min_recasaos"},
		{"a version with four numbers", "min_recasaos", `"0.5.23.1"`, "min_recasaos"},
		{"a version with a new line at the end", "min_recasaos", `"0.5.23\n"`, "min_recasaos"},
		{"an os with a release of one digit", "os", `["debian:12.1"]`, "os"},
		{"an os with a release of two digits", "os", `["debian:12.10"]`, ""},
		{"an os that is not debian or ubuntu", "os", `["fedora:40"]`, "os"},
		{"an os in capitals", "os", `["Debian:12"]`, "os"},
		{"an os with a space", "os", `["debian: 12"]`, "os"},
		{"an os with a new line at the end", "os", `["debian:12\n"]`, "os"},
		{"an os twice", "os", `["debian:12","debian:12"]`, "os"},
		{"a package that ends with a dash, which apt reads as a removal", "packages.common", `["libvirt-clients-"]`, "packages.common"},
		{"a package that ends with a plus, which apt reads as an install", "packages.common", `["libvirt-clients+"]`, "packages.common"},
		{"a package that ends with .deb, which apt reads as the path of a file", "packages.common", `["libvirt.deb"]`, "packages.common"},
		{"a package of one character", "packages.common", `["a"]`, "packages.common"},
		{"a package with an equal sign", "packages.common", `["pkg=1.0"]`, "packages.common"},
		{"a package with a slash", "packages.common", `["pkg/stable"]`, "packages.common"},
		{"a package with an architecture", "packages.common", `["pkg:i386"]`, "packages.common"},
		{"a package in capitals", "packages.common", `["Pkg"]`, "packages.common"},
		{"a package that starts with a dash", "packages.common", `["-pkg"]`, "packages.common"},
		{"a package with a new line at the end", "packages.common", `["pkg\n"]`, "packages.common"},
		{"a package of 64 characters", "packages.common", `["` + rep("p", 64) + `"]`, ""},
		{"a package of 65 characters", "packages.common", `["` + rep("p", 65) + `"]`, "packages.common"},
		{"a package with a plus inside", "packages.common", `["libstdc++6","g++-12"]`, ""},
		{"a package that is not a text", "packages.common", `[1]`, "file"},
		{"a list of packages that is not a list", "packages.common", `"pkg"`, "file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			text := withRaw(t, c.path, c.literal)
			if c.path == "gateway_route" {
				// (the calls of the section for an upgrade are under the route, so a case that changes the route leaves the section out)
				var doc map[string]json.RawMessage
				if err := json.Unmarshal([]byte(text), &doc); err != nil {
					t.Fatal(err)
				}
				delete(doc, "os_upgrade")
				out, _ := json.Marshal(doc)
				text = string(out)
			}
			_, err := Parse("virt.json", []byte(text))
			var bad *Invalid
			switch {
			case c.field == "" && err != nil:
				t.Errorf("refused: %v", err)
			case c.field != "" && !errors.As(err, &bad):
				t.Errorf("taken, want %s refused", c.field)
			case c.field != "" && bad.Field != c.field:
				t.Errorf("error = %v, want field %s", err, c.field)
			}
		})
	}
}

// At most 40 packages in all, whatever the lists they are in, and at most 32 in each.
func TestThePackagesAreBoundedInAll(t *testing.T) {
	names := func(prefix string, n int) string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, `"`+prefix+rep("a", i/26)+string(rune('a'+i%26))+`"`)
		}
		return "[" + strings.Join(out, ",") + "]"
	}
	parse := func(common, amd64, arm64 int) error {
		packages := `{"common":` + names("cc-", common) + `,"amd64":` + names("am-", amd64) + `,"arm64":` + names("ar-", arm64) + `}`
		_, err := Parse("virt.json", []byte(withRaw(t, "packages", packages)))
		return err
	}
	for _, c := range []struct {
		common, amd64, arm64 int
		field, reason        string // empty when taken
	}{
		{14, 13, 13, "", ""},
		{14, 13, 14, "packages", ReasonTooMany},
		{32, 8, 0, "", ""},
		{32, 9, 0, "packages", ReasonTooMany},
		{33, 0, 0, "packages.common", ReasonTooMany},
		{0, 33, 0, "packages.amd64", ReasonTooMany},
		{0, 0, 33, "packages.arm64", ReasonTooMany},
		{0, 0, 0, "", ""},
	} {
		err := parse(c.common, c.amd64, c.arm64)
		var bad *Invalid
		switch {
		case c.field == "" && err != nil:
			t.Errorf("%d+%d+%d packages: %v", c.common, c.amd64, c.arm64, err)
		case c.field != "" && (!errors.As(err, &bad) || bad.Field != c.field || bad.Reason != c.reason):
			t.Errorf("%d+%d+%d packages: error = %v, want %s %s", c.common, c.amd64, c.arm64, err, c.field, c.reason)
		}
	}
}

// A manifest is read by the core, and by the installer with jq, and by the dashboard: whatever one reader takes from the text, the others must take the same, or the text is not taken.
func TestAManifestIsTakenOnlyIfAnyOtherReaderWouldReadTheSame(t *testing.T) {
	const tail = `"component":"CasaOS-VirtManagement","unit":"x.service","gateway_route":"/v2/virt","packages":{},"min_recasaos":"0.5.23","os":["debian:12"]}`
	good := `{"schema":1,"name":"virt","title":"T",` + tail
	if _, err := Parse("virt.json", []byte(good)); err != nil {
		t.Fatalf("the base of the cases: %v", err)
	}
	for _, c := range []struct{ name, text string }{
		{"a key in capitals", `{"schema":1,"NAME":"virt","title":"T",` + tail},
		{"a key in capitals with the right one beside it", `{"schema":1,"name":"virt","NAME":"other","title":"T",` + tail},
		{"a key with a long s for the s", `{"ſchema":1,"name":"virt","title":"T",` + tail},
		{"a key with the Kelvin sign for the k", `{"schema":1,"name":"virt","title":"T","K":1,` + tail},
		{"a key that is a known one with a space", `{"schema":1,"name ":"virt","title":"T",` + tail},
		{"a key twice", `{"schema":1,"name":"virt","name":"virt","title":"T",` + tail},
		{"another key twice", `{"schema":1,"name":"virt","title":"T","title":"U",` + tail},
		{"a field that is not known in packages", strings.Replace(good, `"packages":{}`, `"packages":{"extra":[]}`, 1)},
		{"a field that is not known in the section for an upgrade", withRaw(t, "os_upgrade.maintenance.extra", `"/v2/virt_management/x"`)},
		{"a field in capitals in packages", strings.Replace(good, `"packages":{}`, `"packages":{"COMMON":[]}`, 1)},
		{"a byte that is not UTF-8 in the title", "{\"schema\":1,\"name\":\"virt\",\"title\":\"T\xff\"," + tail},
		{"a byte order mark", "\xef\xbb\xbf" + good},
		{"a NUL after the object", good + "\x00"},
		{"a NUL inside a string", "{\"schema\":1,\"name\":\"virt\",\"title\":\"T\x00\"," + tail},
		{"a comment", good + "// x"},
		{"a trailing comma", strings.Replace(good, `"os":["debian:12"]}`, `"os":["debian:12"],}`, 1)},
		{"single quotes", strings.ReplaceAll(good, `"`, `'`)},
		{"a schema that is a float", strings.Replace(good, `"schema":1`, `"schema":1.0`, 1)},
		{"a schema in exponent form", strings.Replace(good, `"schema":1`, `"schema":1e0`, 1)},
		{"a schema that is a text", strings.Replace(good, `"schema":1`, `"schema":"1"`, 1)},
		{"a schema that is too large for an integer", strings.Replace(good, `"schema":1`, `"schema":99999999999999999999`, 1)},
		{"a list nested without end", good[:len(good)-1] + `,"os_x":` + rep("[", 60000)},
		{"a list nested deeper than the decoder goes", strings.Replace(good, `"packages":{}`, `"packages":{"common":`+rep("[", 20000)+rep("]", 20000)+`}`, 1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse("virt.json", []byte(c.text))
			var bad *Invalid
			if !errors.As(err, &bad) {
				t.Fatalf("taken, or an error that is not an Invalid: %v", err)
			}
		})
	}
	// a null is no value, as an absent key is none
	if _, err := Parse("virt.json", []byte(strings.Replace(good, `"packages":{}`, `"packages":null`, 1))); err != nil {
		t.Errorf("a null for the packages: %v", err)
	}
	if _, err := Parse("virt.json", []byte(strings.Replace(good, `"os":["debian:12"]`, `"os":null`, 1))); err == nil {
		t.Error("a null for the operating systems")
	}
}

// The reader that the manifest and the catalogue share, on a field that has no rule of its own: what it refuses, it refuses for itself.
func TestTheStrictReaderRefusesOnItsOwnWhatNoFieldWouldCatch(t *testing.T) {
	type doc struct {
		A string `json:"a"`
		B []struct {
			C string `json:"c"`
		} `json:"b"`
	}
	good := `{"a":"x","b":[{"c":"y"}]}`
	var d doc
	if err := decodeStrict([]byte(good), &d); err != nil || d.A != "x" || d.B[0].C != "y" {
		t.Fatalf("%+v %v", d, err)
	}
	for name, text := range map[string]string{
		"bytes that are not UTF-8":            "{\"a\":\"\xff\"}",
		"a key twice":                         `{"a":"x","a":"y"}`,
		"a key twice in a list":               `{"b":[{"c":"x","c":"y"}]}`,
		"a key in capitals":                   `{"A":"x"}`,
		"a key in capitals in a list":         `{"b":[{"C":"y"}]}`,
		"a field that is not known":           `{"z":"x"}`,
		"a field that is not known in a list": `{"b":[{"z":"y"}]}`,
		"words after the value":               good + ` {}`,
		"a second value":                      good + good,
		"a value that is not the type":        `{"a":5}`,
		"nothing":                             ``,
		"a null in place of the text":         `null`,
	} {
		var d doc
		err := decodeStrict([]byte(text), &d)
		if name == "a null in place of the text" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: taken as %+v", name, d)
		}
	}
}
