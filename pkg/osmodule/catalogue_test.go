package osmodule

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

const (
	virtCommit = "0123456789abcdef0123456789abcdef01234567"
	virtSHA    = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	virtURL    = "https://github.com/ReCasaOS/CasaOS-VirtManagement/releases/download/v0.1.0/linux-amd64-casaos-virt-management-v0.1.0.tar.gz"
	virtARMURL = "https://github.com/ReCasaOS/CasaOS-VirtManagement/releases/download/v0.1.0/linux-arm64-casaos-virt-management-v0.1.0.tar.gz"
)

// entryText is the text of the entry of a module called name, with the manifest of the module in it.
func entryText(name string) string {
	manifest := strings.Replace(virtManifest, `"name": "virt"`, `"name": "`+name+`"`, 1)
	return `{"name":"` + name + `","component":"CasaOS-VirtManagement","tier":"community","tag":"v0.1.0","commit":"` + virtCommit + `",` +
		`"assets":{"amd64":{"url":"` + virtURL + `","sha256":"` + virtSHA + `"},"arm64":{"url":"` + virtARMURL + `","sha256":"` + virtSHA + `"}},"manifest":` + manifest + `}`
}

func catalogueText(entries ...string) string {
	return `{"schema":1,"modules":[` + strings.Join(entries, ",") + `]}`
}

// catalogueWith changes one place of the catalogue of one module (the path goes through objects and, by number, through lists), by decoding it and encoding it again.
func catalogueWith(t *testing.T, path, literal string) string {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(catalogueText(entryText("virt"))), &doc); err != nil {
		t.Fatal(err)
	}
	keys := strings.Split(path, ".")
	at := doc
	for _, k := range keys[:len(keys)-1] {
		if list, ok := at.([]any); ok {
			i, _ := strconv.Atoi(k)
			at = list[i]
		} else {
			at = at.(map[string]any)[k]
		}
	}
	last := keys[len(keys)-1]
	switch parent := at.(type) {
	case map[string]any:
		if literal == "" {
			delete(parent, last)
		} else {
			parent[last] = json.RawMessage(literal)
		}
	default:
		t.Fatalf("path %s", path)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestTheCatalogueOfAReleaseIsTaken(t *testing.T) {
	c, err := ParseCatalogue([]byte(catalogueText(entryText("virt"), entryText("other"))))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Modules) != 2 || c.Schema != 1 {
		t.Fatalf("%+v", c)
	}
	e, ok := c.Find("virt")
	if !ok || e.Tier != TierCommunity || e.Tag != "v0.1.0" || e.Commit != virtCommit || e.Component != "CasaOS-VirtManagement" || e.Manifest.Unit != "casaos-virt-management.service" {
		t.Errorf("%+v", e)
	}
	if a, ok := e.Asset("amd64"); !ok || a.URL != virtURL || a.SHA256 != virtSHA {
		t.Errorf("amd64: %+v", a)
	}
	if a, ok := e.Asset("arm64"); !ok || a.URL != virtARMURL {
		t.Errorf("arm64: %+v", a)
	}
	for _, arch := range []string{"", "386", "riscv64", "AMD64", "amd64 "} {
		if _, ok := e.Asset(arch); ok {
			t.Errorf("an asset for %q", arch)
		}
	}
	if _, ok := c.Find("nothing"); ok {
		t.Error("a module that is not there")
	}
	if _, ok := c.Find(""); ok {
		t.Error("a module with no name")
	}
	// a release with no module has a catalogue with none: that is a catalogue
	empty, err := ParseCatalogue([]byte(catalogueText()))
	if err != nil || len(empty.Modules) != 0 {
		t.Errorf("a catalogue of nothing: %+v, %v", empty, err)
	}
	// one architecture is enough, the other has no asset
	one := catalogueWith(t, "modules.0.assets.arm64", "")
	c, err = ParseCatalogue([]byte(one))
	if err != nil {
		t.Fatal(err)
	}
	e, _ = c.Find("virt")
	if _, ok := e.Asset("arm64"); ok {
		t.Error("an asset for arm64 that was not given")
	}
	if _, ok := e.Asset("amd64"); !ok {
		t.Error("no asset for amd64")
	}
}

func TestACatalogueThatBreaksOneRuleGivesNoModuleAndAnError(t *testing.T) {
	url := func(s string) string { return `"` + s + `"` }
	const base = "https://github.com/ReCasaOS/CasaOS-VirtManagement/releases/download/v0.1.0/"
	const archive = "linux-amd64-casaos-virt-management-v0.1.0.tar.gz"
	// where the error is when the place that was changed is not the place that is wrong: the text that is not read at all, and the entry that is read against its manifest
	wherever := map[string]string{
		"a schema as text": "catalogue", "modules that are an object": "catalogue", "an asset in capitals": "catalogue", "an asset that is a text": "catalogue",
		"an asset that is not known in the asset": "catalogue", "an asset for an architecture that is not known": "catalogue", "a manifest with a field that is not known": "catalogue",
		"a field that is not known in the entry": "catalogue", "a field in capitals in the entry": "catalogue", "a field in the document that is not known": "catalogue",
		"a name that is not the manifest's": "modules.0.manifest.name", "a component that is not the manifest's": "modules.0.manifest.component",
		"a component that is not the one of the urls": "modules.0.assets.amd64",
	}
	for _, c := range []struct {
		name, path, literal string
	}{
		// the document
		{"another schema", "schema", `2`},
		{"a schema as text", "schema", `"1"`},
		{"no schema", "schema", ``},
		{"no list of modules", "modules", ``},
		{"a list of modules that is null", "modules", `null`},
		{"modules that are an object", "modules", `{}`},
		// the entry
		{"an entry with a name that is not a name", "modules.0.name", `"Virt"`},
		{"an entry with no name", "modules.0.name", ``},
		{"a name that is not the manifest's", "modules.0.name", `"other"`},
		{"a component that is not the manifest's", "modules.0.manifest.component", `"CasaOS-Other"`},
		{"a component that is not the one of the urls", "modules.0.component", `"CasaOS-Other"`},
		{"a component with a slash", "modules.0.component", `"a/b"`},
		{"a component with a dot dot", "modules.0.component", `".."`},
		{"no component", "modules.0.component", ``},
		{"a tier that is not known", "modules.0.tier", `"official"`},
		{"a tier in capitals", "modules.0.tier", `"Core"`},
		{"an empty tier", "modules.0.tier", `""`},
		{"no tier", "modules.0.tier", ``},
		{"a tag without the v", "modules.0.tag", `"0.1.0"`},
		{"a tag of two numbers", "modules.0.tag", `"v0.1"`},
		{"a tag of four numbers", "modules.0.tag", `"v0.1.0.1"`},
		{"a tag with a suffix", "modules.0.tag", `"v0.1.0-rc1"`},
		{"a tag with a new line", "modules.0.tag", `"v0.1.0\n"`},
		{"a tag with a letter", "modules.0.tag", `"v0.1.x"`},
		{"a tag that is a branch", "modules.0.tag", `"main"`},
		{"no tag", "modules.0.tag", ``},
		{"a commit of 39 digits", "modules.0.commit", url(virtCommit[:39])},
		{"a commit of 41 digits", "modules.0.commit", url(virtCommit + "0")},
		{"a commit in capitals", "modules.0.commit", url(strings.ToUpper(virtCommit))},
		{"a commit that is not hexadecimal", "modules.0.commit", url(strings.Repeat("g", 40))},
		{"a commit with a new line", "modules.0.commit", url(virtCommit[:39] + `\n`)},
		{"a commit that is a branch", "modules.0.commit", `"main"`},
		{"no commit", "modules.0.commit", ``},
		// the assets
		{"no assets", "modules.0.assets", ``},
		{"assets that are null", "modules.0.assets", `null`},
		{"assets with no architecture", "modules.0.assets", `{}`},
		{"an asset for an architecture that is not known", "modules.0.assets.riscv64", `{"url":"` + base + `linux-riscv64-casaos-virt-management-v0.1.0.tar.gz","sha256":"` + virtSHA + `"}`},
		{"an asset in capitals", "modules.0.assets.AMD64", `{"url":"` + virtURL + `","sha256":"` + virtSHA + `"}`},
		{"assets that are all null", "modules.0.assets", `{"amd64":null,"arm64":null}`},
		{"an asset that is a text", "modules.0.assets.amd64", url(virtURL)},
		{"an asset that is not known in the asset", "modules.0.assets.amd64.size", `5`},
		{"an asset with no url", "modules.0.assets.amd64.url", ``},
		{"an asset with no sha256", "modules.0.assets.amd64.sha256", ``},
		{"a sha256 of 63 digits", "modules.0.assets.amd64.sha256", url(virtSHA[:63])},
		{"a sha256 of 65 digits", "modules.0.assets.amd64.sha256", url(virtSHA + "0")},
		{"a sha256 in capitals", "modules.0.assets.amd64.sha256", url(strings.ToUpper(virtSHA))},
		{"a sha256 that is not hexadecimal", "modules.0.assets.amd64.sha256", url(strings.Repeat("z", 64))},
		{"a sha256 with a new line", "modules.0.assets.amd64.sha256", url(virtSHA[:63] + `\n`)},
		{"a sha256 with a prefix", "modules.0.assets.amd64.sha256", url("sha256:" + virtSHA[:57])},
		// the url, which is exactly one form
		{"an url over http", "modules.0.assets.amd64.url", url("http://github.com/ReCasaOS/CasaOS-VirtManagement/releases/download/v0.1.0/" + archive)},
		{"an url with another host", "modules.0.assets.amd64.url", url(strings.Replace(virtURL, "github.com", "gitlab.com", 1))},
		{"an url whose host goes on", "modules.0.assets.amd64.url", url(strings.Replace(virtURL, "github.com", "github.com.evil.example", 1))},
		{"an url with a user that looks like the host", "modules.0.assets.amd64.url", url("https://github.com@evil.example/ReCasaOS/CasaOS-VirtManagement/releases/download/v0.1.0/" + archive)},
		{"an url with a port", "modules.0.assets.amd64.url", url(strings.Replace(virtURL, "github.com", "github.com:443", 1))},
		{"an url of another organisation", "modules.0.assets.amd64.url", url(strings.Replace(virtURL, "ReCasaOS", "recasaos", 1))},
		{"an url of an organisation that starts the same", "modules.0.assets.amd64.url", url(strings.Replace(virtURL, "ReCasaOS/", "ReCasaOS2/", 1))},
		{"an url of another component", "modules.0.assets.amd64.url", url(strings.Replace(virtURL, "CasaOS-VirtManagement", "CasaOS-Other", 1))},
		{"an url of a component that starts the same", "modules.0.assets.amd64.url", url(strings.Replace(virtURL, "CasaOS-VirtManagement", "CasaOS-VirtManagementX", 1))},
		{"an url of another tag", "modules.0.assets.amd64.url", url(strings.Replace(virtURL, "download/v0.1.0", "download/v0.2.0", 1))},
		{"an url that is the latest release", "modules.0.assets.amd64.url", url("https://github.com/ReCasaOS/CasaOS-VirtManagement/releases/latest/download/" + archive)},
		{"an url of a branch", "modules.0.assets.amd64.url", url("https://github.com/ReCasaOS/CasaOS-VirtManagement/archive/refs/heads/main.tar.gz")},
		{"an url with a query", "modules.0.assets.amd64.url", url(virtURL + "?x=1")},
		{"an url with a fragment", "modules.0.assets.amd64.url", url(virtURL + "#x")},
		{"an url with a new line", "modules.0.assets.amd64.url", url(virtURL + `\n`)},
		{"an url with a space", "modules.0.assets.amd64.url", url(virtURL + " ")},
		{"an url with an escaped dot dot", "modules.0.assets.amd64.url", url(base + "%2e%2e/" + archive)},
		{"an url with a dot dot", "modules.0.assets.amd64.url", url(base + "../v0.1.0/" + archive)},
		{"an url with an extra directory", "modules.0.assets.amd64.url", url(base + "x/" + archive)},
		{"an url with a double slash", "modules.0.assets.amd64.url", url(strings.Replace(virtURL, "/download/", "//download/", 1))},
		{"an url with an escape in the archive", "modules.0.assets.amd64.url", url(base + "linux-amd64-casaos-virt-management-v0.1.0%2etar.gz")},
		{"an archive for the other architecture", "modules.0.assets.amd64.url", url(base + "linux-arm64-casaos-virt-management-v0.1.0.tar.gz")},
		{"an archive of another version than the tag", "modules.0.assets.amd64.url", url(base + "linux-amd64-casaos-virt-management-v0.1.1.tar.gz")},
		{"an archive of a version that goes on", "modules.0.assets.amd64.url", url(base + "linux-amd64-casaos-virt-management-v0.1.00.tar.gz")},
		{"an archive that is a zip", "modules.0.assets.amd64.url", url(base + "linux-amd64-casaos-virt-management-v0.1.0.zip")},
		{"an archive that is a tar.xz", "modules.0.assets.amd64.url", url(base + "linux-amd64-casaos-virt-management-v0.1.0.tar.xz")},
		{"an archive that is not for casaos", "modules.0.assets.amd64.url", url(base + "linux-amd64-virt-management-v0.1.0.tar.gz")},
		{"an archive for another system", "modules.0.assets.amd64.url", url(base + "darwin-amd64-casaos-virt-management-v0.1.0.tar.gz")},
		{"an archive with capitals", "modules.0.assets.amd64.url", url(base + "linux-amd64-casaos-Virt-management-v0.1.0.tar.gz")},
		{"an archive with an underscore", "modules.0.assets.amd64.url", url(base + "linux-amd64-casaos-virt_management-v0.1.0.tar.gz")},
		{"an archive with a digit in the name", "modules.0.assets.amd64.url", url(base + "linux-amd64-casaos-virt2-v0.1.0.tar.gz")},
		{"an archive with no name", "modules.0.assets.amd64.url", url(base + "linux-amd64-casaos--v0.1.0.tar.gz")},
		{"the checksums instead of the archive", "modules.0.assets.amd64.url", url(base + "checksums.txt")},
		{"an url of nothing", "modules.0.assets.amd64.url", `""`},
		{"an url of 100 KiB", "modules.0.assets.amd64.url", url(virtURL + strings.Repeat("a", 100<<10))},
		{"an url of the right shape that is too long", "modules.0.assets.amd64.url", url(base + "linux-amd64-casaos-" + strings.Repeat("a", 500) + "-v0.1.0.tar.gz")},
		// the manifest of the module
		{"no manifest", "modules.0.manifest", ``},
		{"a manifest that is null", "modules.0.manifest", `null`},
		{"a manifest with another schema", "modules.0.manifest.schema", `2`},
		{"a manifest with a unit that is a command", "modules.0.manifest.unit", `"x.service; reboot"`},
		{"a manifest with a field that is not known", "modules.0.manifest.run", `"rm -rf /"`},
		{"a manifest with a title that turns the text round", "modules.0.manifest.title", `"a‮b"`},
		{"a manifest with too many packages", "modules.0.manifest.packages.common", `[` + strings.Repeat(`"abc",`, 40) + `"abc"]`},
		{"a field that is not known in the entry", "modules.0.size", `5`},
		{"a field in capitals in the entry", "modules.0.Tier", `"core"`},
		{"a field in the document that is not known", "version", `1`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseCatalogue([]byte(catalogueWith(t, c.path, c.literal)))
			var bad *Invalid
			if !errors.As(err, &bad) {
				t.Fatalf("taken, or an error that is not an Invalid: %v", err)
			}
			// the error is about the place that was changed (or a place above it or below it), and not about something else that the case broke
			switch want, related := wherever[c.name], strings.HasPrefix(c.path+".", bad.Field+".") || strings.HasPrefix(bad.Field+".", c.path+"."); {
			case want != "" && bad.Field != want:
				t.Errorf("error = %v, want field %s", err, want)
			case want == "" && !related:
				t.Errorf("error = %v, but the change was at %s", err, c.path)
			}
			if len(got.Modules) != 0 || got.Schema != 0 {
				t.Errorf("a catalogue with %d modules came with the error", len(got.Modules))
			}
		})
	}
}

// the same rules, where the manifest is not what is wrong but the way it is put in, and where the catalogue as a whole is
func TestTheCatalogueIsAWholeOrNothing(t *testing.T) {
	good, bad := entryText("virt"), strings.Replace(entryText("other"), `"tier":"community"`, `"tier":"x"`, 1)
	for name, text := range map[string]string{
		"a good entry then a bad one":     catalogueText(good, bad),
		"a bad entry then a good one":     catalogueText(bad, good),
		"a name twice":                    catalogueText(good, good),
		"a name twice, one in capitals":   catalogueText(good, strings.ReplaceAll(good, `"name":"virt"`, `"name":"Virt"`)),
		"a key twice in the document":     strings.Replace(catalogueText(good), `"schema":1`, `"schema":1,"schema":1`, 1),
		"a key twice in an entry":         strings.Replace(catalogueText(good), `"tier":"community"`, `"tier":"community","tier":"core"`, 1),
		"a key twice in an asset":         strings.Replace(catalogueText(good), `"sha256":"`+virtSHA+`"}`, `"sha256":"`+virtSHA+`","sha256":"`+virtSHA+`"}`, 1),
		"a key twice in the manifest":     strings.Replace(catalogueText(good), `"unit": "casaos-virt-management.service",`, `"unit": "casaos-virt-management.service","unit": "x.service",`, 1),
		"words after the document":        catalogueText(good) + " {}",
		"a byte that is not UTF-8":        strings.Replace(catalogueText(good), `"tier"`, "\xff\"tier\"", 1),
		"nothing":                         "",
		"a list":                          "[]",
		"a text":                          `"catalogue"`,
		"a null":                          "null",
		"a document cut":                  catalogueText(good)[:100],
		"an object with a trailing comma": strings.Replace(catalogueText(good), `"modules":[`, `"modules":[,`, 1),
	} {
		got, err := ParseCatalogue([]byte(text))
		var inv *Invalid
		if !errors.As(err, &inv) || len(got.Modules) != 0 {
			t.Errorf("%s: %d modules, %v", name, len(got.Modules), err)
		}
	}
}

func TestTheCatalogueIsReadUpToItsBoundsAndNotBeyond(t *testing.T) {
	text := catalogueText(entryText("virt"))
	pad := func(n int) []byte { return append([]byte(text), []byte(strings.Repeat(" ", n-len(text)))...) }
	if _, err := ParseCatalogue(pad(MaxCatalogueBytes)); err != nil {
		t.Errorf("a catalogue of %d bytes: %v", MaxCatalogueBytes, err)
	}
	var bad *Invalid
	if _, err := ParseCatalogue(pad(MaxCatalogueBytes + 1)); !errors.As(err, &bad) || bad.Reason != ReasonTooLarge {
		t.Errorf("a catalogue of %d bytes: %v", MaxCatalogueBytes+1, err)
	}
	names := func(n int) []string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, entryText(fmt.Sprintf("m%02d", i)))
		}
		return out
	}
	if c, err := ParseCatalogue([]byte(catalogueText(names(MaxCatalogueModules)...))); err != nil || len(c.Modules) != MaxCatalogueModules {
		t.Errorf("%d modules: %d, %v", MaxCatalogueModules, len(c.Modules), err)
	}
	if _, err := ParseCatalogue([]byte(catalogueText(names(MaxCatalogueModules + 1)...))); !errors.As(err, &bad) || bad.Reason != ReasonTooMany {
		t.Errorf("%d modules: %v", MaxCatalogueModules+1, err)
	}
	// a long name is a name that is not
	long := strings.Repeat("a", 33)
	if _, err := ParseCatalogue([]byte(catalogueText(entryText(long)))); err == nil {
		t.Error("a name of 33 letters")
	}
}
