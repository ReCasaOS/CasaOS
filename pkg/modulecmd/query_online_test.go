package modulecmd

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

const onlineTag = "v0.1.5"

// onlineEntry is the entry of the fixture at another tag, with the archive and the digest of its own.
func (f *fixture) onlineEntry(tag string) osmodule.Entry {
	e := f.entry
	a := *e.Assets.AMD64
	a.URL = strings.ReplaceAll(a.URL, testTag, tag)
	a.SHA256 = strings.Repeat("b", 64)
	e.Assets = osmodule.Assets{AMD64: &a}
	e.Tag = tag
	return e
}

func (f *fixture) writeOnline(entries ...osmodule.Entry) {
	f.t.Helper()
	raw, err := json.Marshal(osmodule.Catalogue{Schema: 2, Serial: 7, Generated: "2026-11-25T10:00:00Z", Modules: entries})
	if err != nil {
		f.t.Fatal(err)
	}
	f.writeFile(pathOnlineCatalogue, string(raw))
}

func catalogueOf(t *testing.T, f *fixture) (map[string]any, map[string]any) {
	t.Helper()
	v := f.json("list")
	return v["catalogue"].(map[string]any), v["modules"].([]any)[0].(map[string]any)
}

func TestListTakesTheOnlineEntryWhenNewer(t *testing.T) {
	f := installed(t)
	f.envVars["CASAOS_MODULE_TEST"] = "1"
	f.writeOnline(f.onlineEntry(onlineTag))
	cat, m := catalogueOf(t, f)
	if m["source"] != "online" || m["tag"] != onlineTag {
		t.Errorf("module: %v", m)
	}
	if u := m["update"].(map[string]any); u["available"] != true || u["latest"] != onlineTag {
		t.Errorf("update: %v", u)
	}
	if on := cat["online"].(map[string]any); on["ok"] != true || on["serial"] != float64(7) || on["generated"] != "2026-11-25T10:00:00Z" || cat["ok"] != true {
		t.Errorf("catalogue: %v", cat)
	}
}

func TestListKeepsTheShippedEntryWhenTheOnlineOneNeedsANewerRelease(t *testing.T) {
	f := newFixture(t)
	e := f.onlineEntry(onlineTag)
	e.Manifest.MinReCasaOS = "0.9.0"
	f.writeOnline(e)
	_, m := catalogueOf(t, f)
	if m["source"] != "shipped" || m["tag"] != testTag || m["system"].(map[string]any)["supported"] != true {
		t.Errorf("module: %v", m)
	}
}

func TestListIgnoresABrokenOnlineCatalogue(t *testing.T) {
	f := newFixture(t)
	f.writeFile(pathOnlineCatalogue, "{not json")
	cat, m := catalogueOf(t, f)
	on := cat["online"].(map[string]any)
	if cat["ok"] != true || on["ok"] != false || on["error"] != "invalid" || m["source"] != "shipped" || m["tag"] != testTag {
		t.Errorf("catalogue %v, module %v", cat, m)
	}
	// a schema 1 file at the online place is refused too
	f.writeFile(pathOnlineCatalogue, `{"schema":1,"modules":[]}`)
	if on := func() map[string]any { c, _ := catalogueOf(t, f); return c["online"].(map[string]any) }(); on["error"] != "invalid" {
		t.Errorf("schema 1 online: %v", on)
	}
}

func TestListWithNoOnlineFileSaysNone(t *testing.T) {
	f := newFixture(t)
	cat, m := catalogueOf(t, f)
	if on := cat["online"].(map[string]any); on["ok"] != false || on["error"] != "none" || m["source"] != "shipped" {
		t.Errorf("catalogue %v, module %v", cat, m)
	}
}

func TestListWorksWithOnlyTheOnlineCatalogue(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(f.path(pathCatalogue)); err != nil {
		t.Fatal(err)
	}
	f.writeOnline(f.onlineEntry(onlineTag))
	cat, m := catalogueOf(t, f)
	if cat["ok"] != true || cat["error"] != nil || m["source"] != "online" || m["tag"] != onlineTag {
		t.Errorf("catalogue %v, module %v", cat, m)
	}
}

func TestInstalledNewerThanBothCataloguesIsNotAnUpdate(t *testing.T) {
	f := installed(t)
	f.writeOnline(f.onlineEntry(onlineTag))
	f.writeFile("/var/lib/casaos/modules/virt.version", "v0.9.0\n")
	_, m := catalogueOf(t, f)
	if u := m["update"].(map[string]any); u["available"] != false || u["installed"] != "v0.9.0" {
		t.Errorf("update: %v", u)
	}
}

func TestOnlineCatalogueVariableNeedsTestMode(t *testing.T) {
	f := newFixture(t)
	elsewhere := filepath.Join(t.TempDir(), "online.json")
	raw, _ := json.Marshal(osmodule.Catalogue{Schema: 2, Serial: 1, Generated: "2026-11-25T10:00:00Z", Modules: []osmodule.Entry{f.onlineEntry(onlineTag)}})
	if err := os.WriteFile(elsewhere, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	f.envVars["CASAOS_MODULE_ONLINE_CATALOGUE"] = elsewhere
	if _, m := catalogueOf(t, f); m["source"] != "shipped" {
		t.Errorf("without the test variable: %v", m)
	}
	f.envVars["CASAOS_MODULE_TEST"] = "1"
	if _, m := catalogueOf(t, f); m["source"] != "online" {
		t.Errorf("with the test variable: %v", m)
	}
}

func TestPlanUsesTheMergedEntry(t *testing.T) {
	f := newFixture(t)
	e := f.onlineEntry(onlineTag)
	f.writeOnline(e)
	if p := PlanOf(context.Background(), f.env, testName); p.Tag != onlineTag || p.Archive.URL != e.Assets.AMD64.URL {
		t.Errorf("plan: %+v", p)
	} else if p.PlanID == "" || p.PlanID != osmodule.PlanID(testName, onlineTag, e.Assets.AMD64.SHA256, "debian", "12", p.Packages.ToInstall) {
		// the plan's id is bound to the archive's sha256: the merged entry's, not the shipped one's
		t.Errorf("the plan is not bound to the online archive's sha256: %+v", p)
	}
	f2 := installed(t)
	e2 := f2.onlineEntry(onlineTag)
	f2.writeOnline(e2)
	if p := PlanUpdateOf(context.Background(), f2.env, testName); p.Tag != onlineTag || p.Archive.URL != e2.Assets.AMD64.URL {
		t.Errorf("update plan: %+v", p)
	}
}

func TestADirectoryAtTheOnlinePlaceIsUnreadable(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.path(pathOnlineCatalogue), 0o755); err != nil {
		t.Fatal(err)
	}
	cat, m := catalogueOf(t, f)
	if on := cat["online"].(map[string]any); on["ok"] != false || on["error"] != "unreadable" || m["source"] != "shipped" {
		t.Errorf("catalogue %v, module %v", cat, m)
	}
}

func TestAnOnlineEntryOfTheSystemImageIsInvalid(t *testing.T) {
	f := newFixture(t)
	f.writeFile(pathOnlineCatalogue, f.onlineImageRaw(3))
	cat, m := catalogueOf(t, f)
	if on := cat["online"].(map[string]any); on["ok"] != false || on["error"] != "invalid" || m["source"] != "shipped" {
		t.Errorf("catalogue %v, module %v", cat, m)
	}
}

// ---- schema 3: modules written outside ReCasaOS ------------------------------------------------------------------------------

const (
	foreignRepo = "someone/" + testComponent
	ownRepo     = "ReCasaOS/" + testComponent
)

// mirrorEntry is the fixture's entry at tag as a catalogue of schema 3 has it: from the repository repo, its archive the copy in the mirror of ReCasaOS.
func (f *fixture) mirrorEntry(tag, repo string) osmodule.Entry {
	e := f.onlineEntry(tag)
	a := *e.Assets.AMD64
	a.URL = "https://github.com/ReCasaOS/modules/releases/download/m-" + testName + "-" + tag + "/linux-amd64-casaos-virt-management-" + tag + ".tar.gz"
	e.Assets, e.Repository = osmodule.Assets{AMD64: &a}, repo
	return e
}

// online3Raw is an online catalogue of schema 3 at a serial; withdrawn is its list as JSON text, written by hand since json.Marshal leaves an empty one out and schema 3 requires it.
func online3Raw(t *testing.T, serial int64, withdrawn string, entries ...osmodule.Entry) string {
	t.Helper()
	raw, err := json.Marshal(osmodule.Catalogue{Schema: 3, Serial: serial, Generated: "2026-11-25T10:00:00Z", Modules: entries})
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(string(raw), "}") + `,"withdrawn":` + withdrawn + "}"
}

func TestListSaysWhereAModuleOfSchema3ComesFrom(t *testing.T) {
	f := newFixture(t)
	e := f.mirrorEntry(onlineTag, foreignRepo)
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, "[]", e))
	cat, m := catalogueOf(t, f)
	if cat["online"].(map[string]any)["ok"] != true || m["source"] != "online" || m["repository"] != foreignRepo || m["foreign"] != true {
		t.Errorf("catalogue %v, module %v", cat, m)
	}
	// the plan's archive is the copy in the mirror, and the download takes it as it is
	p := PlanOf(context.Background(), f.env, testName)
	if p.Archive.URL != e.Assets.AMD64.URL {
		t.Errorf("plan: %+v", p)
	}
	if u, err := url.Parse(p.Archive.URL); err != nil || checkURL(u) != nil {
		t.Errorf("the mirror's address is not one to download: %v %v", u, err)
	}
	// a module of ReCasaOS says its repository and is not foreign
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, "[]", f.mirrorEntry(onlineTag, ownRepo)))
	if _, m := catalogueOf(t, f); m["repository"] != ownRepo || m["foreign"] != nil {
		t.Errorf("a module of ReCasaOS: %v", m)
	}
}

func TestListReadsASchema2OnlineFileAsBefore(t *testing.T) {
	f := newFixture(t)
	f.writeOnline(f.onlineEntry(onlineTag))
	_, m := catalogueOf(t, f)
	for _, k := range []string{"repository", "foreign", "withdrawn"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s in %v", k, m)
		}
	}
	if m["source"] != "online" || m["tag"] != onlineTag {
		t.Errorf("module: %v", m)
	}
}

func TestListSaysTheInstalledVersionIsWithdrawn(t *testing.T) {
	withdrawn := func(tag string) string {
		return `[{"name":"` + testName + `","tag":"` + tag + `","reason":"it loses the data of its disks"}]`
	}
	f := installed(t)
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, withdrawn(testTag), f.mirrorEntry(onlineTag, foreignRepo)))
	_, m := catalogueOf(t, f)
	if w, ok := m["withdrawn"].(map[string]any); !ok || w["tag"] != testTag || w["reason"] != "it loses the data of its disks" || len(w) != 2 {
		t.Errorf("installed at the withdrawn tag: %v", m)
	}
	// another tag than the installed one is withdrawn: nothing
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, withdrawn(onlineTag), f.mirrorEntry(onlineTag, foreignRepo)))
	if _, m := catalogueOf(t, f); m["withdrawn"] != nil {
		t.Errorf("another tag withdrawn: %v", m)
	}
	// a module that is not installed: nothing
	g := newFixture(t)
	g.writeFile(pathOnlineCatalogue, online3Raw(t, 7, withdrawn(testTag), g.mirrorEntry(onlineTag, foreignRepo)))
	if _, m := catalogueOf(t, g); m["withdrawn"] != nil {
		t.Errorf("not installed: %v", m)
	}
}

// install.sh reads the list by patterns: a module begins with the key "name" and, when it has one, ends with the key "update"
func TestAListedModuleBeginsWithNameAndEndsWithUpdate(t *testing.T) {
	v := "v0.1.0"
	raw, err := json.Marshal(ListModule{
		Name: testName, Title: "t", Description: "d", Tier: "community", Tag: v, Catalogue: true, Source: "online", Repository: foreignRepo, Foreign: true,
		Installed: &Installed{Version: &v}, System: SystemInfo{Supported: true, Reasons: []string{}}, Broken: "manifest_invalid",
		Withdrawn: &WithdrawnInfo{Tag: v, Reason: "r"}, Update: &UpdateInfo{Available: true, Installed: v, Latest: onlineTag},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, k := range []string{"repository", "foreign", "withdrawn"} {
		if !strings.Contains(s, `"`+k+`":`) {
			t.Errorf("no %s: %s", k, s)
		}
	}
	if !strings.HasPrefix(s, `{"name":`) || !regexp.MustCompile(`,"update":\{[^{}]*\}\}$`).MatchString(s) {
		t.Errorf("name is not first or update is not last: %s", s)
	}
}
