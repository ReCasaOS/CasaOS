package osmodule

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

const (
	mirrorURL    = "https://github.com/ReCasaOS/modules/releases/download/m-virt-v0.1.0/linux-amd64-casaos-virt-management-v0.1.0.tar.gz"
	mirrorARMURL = "https://github.com/ReCasaOS/modules/releases/download/m-virt-v0.1.0/linux-arm64-casaos-virt-management-v0.1.0.tar.gz"
	onlineHead   = `"serial":1,"generated":"2026-11-25T10:00:00Z",`
)

// entry3Text is the entry of a module called name in a catalogue of schema 3: from the repository of owner, at tier, its archives copied into ReCasaOS/modules.
func entry3Text(name, owner, tier string) string {
	text := strings.NewReplacer(
		virtURL, strings.ReplaceAll(mirrorURL, "m-virt-", "m-"+name+"-"),
		virtARMURL, strings.ReplaceAll(mirrorARMURL, "m-virt-", "m-"+name+"-"),
		`"tier":"community"`, `"repository":"`+owner+`/CasaOS-VirtManagement","tier":"`+tier+`"`,
	).Replace(entryText(name))
	return text
}

// catalogue3Text is a catalogue of schema 3 with withdrawn (a JSON list) and the entries.
func catalogue3Text(withdrawn string, entries ...string) string {
	return `{"schema":3,` + onlineHead + `"withdrawn":` + withdrawn + `,"modules":[` + strings.Join(entries, ",") + `]}`
}

// catalogue3With is a catalogue of schema 3 of one module of ReCasaOS and one withdrawn version, with one place changed as catalogueWith does it.
func catalogue3With(t *testing.T, path, literal string) string {
	t.Helper()
	return textWith(t, catalogue3Text(`[{"name":"virt","tag":"v0.0.9","reason":"It deletes the data."}]`, entry3Text("virt", "ReCasaOS", "core")), path, literal)
}

// refusedAt says whether the text is refused by ParseOnlineCatalogue with an error at field for reason, and no module.
func refusedAt(t *testing.T, name, text, field, reason string) {
	t.Helper()
	got, err := ParseOnlineCatalogue([]byte(text))
	var inv *Invalid
	if !errors.As(err, &inv) || inv.Field != field || inv.Reason != reason || len(got.Modules) != 0 || got.Withdrawn != nil {
		t.Errorf("%s: %+v, %v (want %s %s)", name, got, err, field, reason)
	}
}

func TestACommunityCatalogueIsTaken(t *testing.T) {
	text := catalogue3Text(`[{"name":"virt","tag":"v0.0.9","reason":"It deletes the data."},{"name":"gone","tag":"v1.2.3","reason":"Withdrawn by its author."}]`,
		entry3Text("virt", "ReCasaOS", "core"), entry3Text("other", "some-one", "community"))
	c, err := ParseOnlineCatalogue([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if c.Schema != CommunityCatalogueSchema || c.Serial != 1 || len(c.Modules) != 2 || len(c.Withdrawn) != 2 {
		t.Fatalf("%+v", c)
	}
	if c.Withdrawn[0] != (Withdrawn{Name: "virt", Tag: "v0.0.9", Reason: "It deletes the data."}) || c.Withdrawn[1].Name != "gone" {
		t.Errorf("%+v", c.Withdrawn)
	}
	virt, _ := c.Find("virt")
	if virt.Repository != "ReCasaOS/CasaOS-VirtManagement" || virt.Foreign() || virt.Tier != TierCore {
		t.Errorf("virt: %+v, foreign %v", virt, virt.Foreign())
	}
	if a, ok := virt.Asset("amd64"); !ok || a.URL != mirrorURL {
		t.Errorf("virt amd64: %+v", a)
	}
	other, _ := c.Find("other")
	if other.Repository != "some-one/CasaOS-VirtManagement" || !other.Foreign() {
		t.Errorf("other: %+v, foreign %v", other, other.Foreign())
	}
	if a, ok := other.Asset("arm64"); !ok || !strings.Contains(a.URL, "/m-other-v0.1.0/linux-arm64-") {
		t.Errorf("other arm64: %+v", a)
	}
	// an entry of schema 1 or 2 is of ReCasaOS
	if (Entry{Name: "virt"}).Foreign() {
		t.Error("an entry with no repository is foreign")
	}
	if (Entry{Repository: "recasaos/CasaOS-VirtManagement"}).Foreign() != true {
		t.Error("an owner that is ReCasaOS only once its case is folded is not foreign")
	}
	// no version withdrawn: an empty list
	c, err = ParseOnlineCatalogue([]byte(catalogue3Text(`[]`, entry3Text("virt", "ReCasaOS", "core"))))
	if err != nil || c.Withdrawn == nil || len(c.Withdrawn) != 0 {
		t.Errorf("an empty withdrawn: %+v, %v", c.Withdrawn, err)
	}
	// a module of ReCasaOS may be community too
	if _, err := ParseOnlineCatalogue([]byte(catalogue3Text(`[]`, entry3Text("virt", "ReCasaOS", "community")))); err != nil {
		t.Error(err)
	}
}

func TestACommunityCatalogueTakesNoArchiveOutsideTheMirror(t *testing.T) {
	const mirror = "https://github.com/ReCasaOS/modules/releases/download/m-virt-v0.1.0/"
	const archive = "linux-amd64-casaos-virt-management-v0.1.0.tar.gz"
	for name, url := range map[string]string{
		"the form of schemas 1 and 2":       virtURL,
		"the repository of the author":      "https://github.com/some-one/CasaOS-VirtManagement/releases/download/v0.1.0/" + archive,
		"the mirror of another module":      "https://github.com/ReCasaOS/modules/releases/download/m-other-v0.1.0/" + archive,
		"the mirror of another tag":         "https://github.com/ReCasaOS/modules/releases/download/m-virt-v0.2.0/" + archive,
		"another tag in the archive":        mirror + "linux-amd64-casaos-virt-management-v0.2.0.tar.gz",
		"a port":                            "https://github.com:443/ReCasaOS/modules/releases/download/m-virt-v0.1.0/" + archive,
		"a user":                            "https://someone@github.com/ReCasaOS/modules/releases/download/m-virt-v0.1.0/" + archive,
		"a query":                           mirror + archive + "?x=1",
		"a fragment":                        mirror + archive + "#x",
		"http":                              "http://github.com/ReCasaOS/modules/releases/download/m-virt-v0.1.0/" + archive,
		"another host":                      "https://example.com/ReCasaOS/modules/releases/download/m-virt-v0.1.0/" + archive,
		"another repository of ReCasaOS":    "https://github.com/ReCasaOS/modules2/releases/download/m-virt-v0.1.0/" + archive,
		"the archive of another arch":       mirror + "linux-arm64-casaos-virt-management-v0.1.0.tar.gz",
		"an escape":                         mirror + "../m-virt-v0.1.0/" + archive,
		"another directory":                 mirror + "x/" + archive,
		"no m-":                             "https://github.com/ReCasaOS/modules/releases/download/virt-v0.1.0/" + archive,
		"the mirror in capitals":            "https://github.com/ReCasaOS/Modules/releases/download/m-virt-v0.1.0/" + archive,
		"the organisation in another case":  "https://github.com/recasaos/modules/releases/download/m-virt-v0.1.0/" + archive,
		"the latest release of the mirror":  "https://github.com/ReCasaOS/modules/releases/latest/download/" + archive,
		"a name of the mirror with a slash": "https://github.com/ReCasaOS/modules/releases/download/m-virt/v0.1.0/" + archive,
	} {
		refusedAt(t, name, catalogue3With(t, "modules.0.assets.amd64.url", strconv.Quote(url)), "modules.0.assets.amd64", ReasonValue)
	}
	// the length is bounded as in schemas 1 and 2
	long := strings.Replace(mirrorURL, "casaos-virt-management", "casaos-"+strings.Repeat("a", 600), 1)
	refusedAt(t, "too long", catalogue3With(t, "modules.0.assets.amd64.url", strconv.Quote(long)), "modules.0.assets.amd64", ReasonValue)
}

func TestACommunityEntrySaysItsRepository(t *testing.T) {
	refusedAt(t, "none", catalogue3With(t, "modules.0.repository", ""), "modules.0.repository", ReasonMissing)
	refusedAt(t, "empty", catalogue3With(t, "modules.0.repository", `""`), "modules.0.repository", ReasonMissing)
	refusedAt(t, "a number", catalogue3With(t, "modules.0.repository", `5`), "catalogue", ReasonUnreadable)
	for name, repository := range map[string]string{
		"no slash":                  "ReCasaOS",
		"no owner":                  "/CasaOS-VirtManagement",
		"no repository":             "ReCasaOS/",
		"two slashes":               "ReCasaOS/CasaOS-VirtManagement/x",
		"an owner with a dot":       "some.one/CasaOS-VirtManagement",
		"an owner of 40":            strings.Repeat("a", 40) + "/CasaOS-VirtManagement",
		"an owner that starts in -": "-someone/CasaOS-VirtManagement",
		"an owner that ends in -":   "someone-/CasaOS-VirtManagement",
		"an owner with a space":     "some one/CasaOS-VirtManagement",
		"another repository":        "ReCasaOS/CasaOS-Other",
		"the repository in a case":  "ReCasaOS/casaos-virtmanagement",
	} {
		refusedAt(t, name, catalogue3With(t, "modules.0.repository", strconv.Quote(repository)), "modules.0.repository", ReasonValue)
	}
	// an owner of 39, the longest
	if _, err := ParseOnlineCatalogue([]byte(catalogue3Text(`[]`, entry3Text("virt", strings.Repeat("a", 39), "community")))); err != nil {
		t.Error(err)
	}
}

func TestAModuleOfAnotherOwnerIsNeverCore(t *testing.T) {
	for _, owner := range []string{"some-one", "recasaos", "ReCasaOS2", "ReCasaO5"} {
		refusedAt(t, owner, catalogue3Text(`[]`, entry3Text("virt", owner, "core")), "modules.0.tier", ReasonValue)
	}
}

func TestACommunityCatalogueListsTheVersionsWithdrawn(t *testing.T) {
	item := func(name, tag, reason string) string {
		b, _ := json.Marshal(Withdrawn{Name: name, Tag: tag, Reason: reason})
		return string(b)
	}
	many := func(n int) string {
		var items []string
		for i := range n {
			items = append(items, item("virt", "v0.0."+strconv.Itoa(i), "Withdrawn."))
		}
		return "[" + strings.Join(items, ",") + "]"
	}
	entry := entry3Text("virt", "ReCasaOS", "core")
	without := `{"schema":3,` + onlineHead + `"modules":[` + entry + `]}`
	refusedAt(t, "none", without, "withdrawn", ReasonMissing)
	for name, list := range map[string]string{"an object": `{}`, "a text": `"virt"`, "null": `null`, "a number": `1`, "a list of texts": `["virt"]`} {
		refusedAt(t, name, catalogue3Text(list, entry), "withdrawn", ReasonValue)
	}
	refusedAt(t, "65", catalogue3Text(many(65), entry), "withdrawn", ReasonTooMany)
	refusedAt(t, "a pair twice", catalogue3Text("["+item("virt", "v0.0.9", "A.")+","+item("virt", "v0.0.9", "B.")+"]", entry), "withdrawn.1", ReasonValue)
	refusedAt(t, "an unknown key", catalogue3Text(`[{"name":"virt","tag":"v0.0.9","reason":"A.","extra":1}]`, entry), "withdrawn", ReasonValue)
	refusedAt(t, "a key twice", catalogue3Text(`[{"name":"virt","tag":"v0.0.9","reason":"A.","reason":"B."}]`, entry), "catalogue", ReasonUnreadable)
	for name, c := range map[string]struct{ name, tag, reason, field string }{
		"a reason empty":            {"virt", "v0.0.9", "", "reason"},
		"a reason of 201":           {"virt", "v0.0.9", strings.Repeat("é", 201), "reason"},
		"a reason with a control":   {"virt", "v0.0.9", "It deletes\u0007 the data.", "reason"},
		"a reason with a newline":   {"virt", "v0.0.9", "It deletes\nthe data.", "reason"},
		"a reason that turns round": {"virt", "v0.0.9", "It ‮deletes", "reason"},
		"a reason of blanks":        {"virt", "v0.0.9", "⠀⠀", "reason"},
		"a reason of spaces":        {"virt", "v0.0.9", "   ", "reason"},
		"a name not a name":         {"Virt", "v0.0.9", "A.", "name"},
		"a name empty":              {"", "v0.0.9", "A.", "name"},
		"a tag not a tag":           {"virt", "0.0.9", "A.", "tag"},
		"a tag empty":               {"virt", "", "A.", "tag"},
	} {
		refusedAt(t, name, catalogue3Text("["+item(c.name, c.tag, c.reason)+"]", entry), "withdrawn.0."+c.field, ReasonValue)
	}
	// the bounds themselves are taken: 64 versions, a reason of 200 characters (not bytes), the same tag of two modules, a module that is not in the catalogue
	for name, list := range map[string]string{
		"64":                      many(64),
		"a reason of 200":         "[" + item("virt", "v0.0.9", strings.Repeat("é", 200)) + "]",
		"one tag, two modules":    "[" + item("virt", "v0.0.9", "A.") + "," + item("other", "v0.0.9", "A.") + "]",
		"one module, two tags":    "[" + item("virt", "v0.0.9", "A.") + "," + item("virt", "v0.0.8", "A.") + "]",
		"a module not catalogued": "[" + item("gone", "v1.0.0", "A.") + "]",
	} {
		if _, err := ParseOnlineCatalogue([]byte(catalogue3Text(list, entry))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestACommunityCatalogueHasTheHeadOfAnOnlineOne(t *testing.T) {
	refusedAt(t, "no serial", catalogue3With(t, "serial", ""), "serial", ReasonMissing)
	refusedAt(t, "no date", catalogue3With(t, "generated", ""), "generated", ReasonMissing)
	refusedAt(t, "an unknown field", catalogue3With(t, "extra", "1"), "catalogue", ReasonUnreadable)
	refusedAt(t, "an unknown field in an entry", catalogue3With(t, "modules.0.extra", "1"), "catalogue", ReasonUnreadable)
}

func TestSchemaTwoIsUnchanged(t *testing.T) {
	// a file of schema 2 with a key of schema 3 is refused as it was: a key that is not known
	withRepository := func(head, repository string) string {
		return strings.Replace(onlineText(head), `"tier":"community"`, `"repository":"`+repository+`","tier":"community"`, 1)
	}
	for name, text := range map[string]string{
		"a repository":          withRepository(onlineHead, "ReCasaOS/CasaOS-VirtManagement"),
		"an empty repository":   withRepository(onlineHead, ""),
		"a null repository":     strings.Replace(onlineText(onlineHead), `"tier":"community"`, `"repository":null,"tier":"community"`, 1),
		"a repository, no date": withRepository(`"serial":1,`, "ReCasaOS/CasaOS-VirtManagement"),
		"a withdrawn list":      strings.Replace(onlineText(onlineHead), `"modules"`, `"withdrawn":[],"modules"`, 1),
		"a withdrawn null":      strings.Replace(onlineText(onlineHead), `"modules"`, `"withdrawn":null,"modules"`, 1),
	} {
		refusedAt(t, name, text, "catalogue", ReasonUnreadable)
	}
	// its archives are those of the organisation, never the mirror
	if _, err := ParseOnlineCatalogue([]byte(onlineText(onlineHead))); err != nil {
		t.Error(err)
	}
	mirrored := strings.NewReplacer(virtURL, mirrorURL, virtARMURL, mirrorARMURL).Replace(onlineText(onlineHead))
	refusedAt(t, "the mirror in schema 2", mirrored, "modules.0.assets.amd64", ReasonValue)
	// another schema with a key of schema 3 is refused as it was
	refusedAt(t, "schema 4 with withdrawn", strings.Replace(catalogue3Text(`[]`, entryText("virt")), `"schema":3`, `"schema":4`, 1), "catalogue", ReasonUnreadable)
	refusedAt(t, "schema 4", strings.Replace(onlineText(onlineHead), `"schema":2`, `"schema":4`, 1), "schema", ReasonSchema)
}

func TestParseCatalogueRefusesSchemaThree(t *testing.T) {
	for name, text := range map[string]string{
		"schema 3":                 catalogue3Text(`[]`, entry3Text("virt", "ReCasaOS", "core")),
		"schema 3 with no head":    `{"schema":3,"modules":[` + entry3Text("virt", "ReCasaOS", "core") + `]}`,
		"schema 1 with withdrawn":  strings.Replace(catalogueText(entryText("virt")), `"modules"`, `"withdrawn":[],"modules"`, 1),
		"schema 1 with repository": strings.Replace(catalogueText(entryText("virt")), `"tier":"community"`, `"repository":"ReCasaOS/CasaOS-VirtManagement","tier":"community"`, 1),
	} {
		got, err := ParseCatalogue([]byte(text))
		var inv *Invalid
		if !errors.As(err, &inv) || len(got.Modules) != 0 {
			t.Errorf("%s: %+v, %v", name, got, err)
			continue
		}
		if want := map[bool]string{true: "schema", false: "catalogue"}[strings.HasPrefix(name, "schema 3")]; inv.Field != want {
			t.Errorf("%s: %v (want %s)", name, err, want)
		}
	}
}
