package modulecmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	}
	f2 := installed(t)
	e2 := f2.onlineEntry(onlineTag)
	f2.writeOnline(e2)
	if p := PlanUpdateOf(context.Background(), f2.env, testName); p.Tag != onlineTag || p.Archive.URL != e2.Assets.AMD64.URL {
		t.Errorf("update plan: %+v", p)
	}
}
