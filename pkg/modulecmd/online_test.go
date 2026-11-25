package modulecmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

const testCatalogueURL = "https://github.com/ReCasaOS/modules/releases/latest/download/catalogue-3.json"

// bodyGet is a Downloader that answers every address with the same bytes; safe for concurrent use.
type bodyGet []byte

func (b bodyGet) Get(_ context.Context, _ string, dst io.Writer) (int64, error) {
	n, err := dst.Write(b)
	return int64(n), err
}

// onlineRaw is an online catalogue of the fixture's module at a serial.
func (f *fixture) onlineRaw(serial int64) []byte {
	f.t.Helper()
	raw, err := json.Marshal(osmodule.Catalogue{Schema: 2, Serial: serial, Generated: "2026-11-25T10:00:00Z", Modules: []osmodule.Entry{f.onlineEntry(onlineTag)}})
	if err != nil {
		f.t.Fatal(err)
	}
	return raw
}

// onlineImageRaw is an online catalogue whose entry is a module of the system image, which is not one to download.
func (f *fixture) onlineImageRaw(serial int64) string {
	f.t.Helper()
	e := f.onlineEntry(onlineTag)
	e.Source = osmodule.SourceImage
	raw, err := json.Marshal(osmodule.Catalogue{Schema: 2, Serial: serial, Generated: "2026-11-25T10:00:00Z", Modules: []osmodule.Entry{e}})
	if err != nil {
		f.t.Fatal(err)
	}
	return string(raw)
}

func (f *fixture) refresh(get Downloader) RefreshResult {
	return RefreshOnline(context.Background(), f.env.Roots, get, testCatalogueURL)
}

// assertOnlineUnchanged says the file in place is still, to the byte, what it was.
func (f *fixture) assertOnlineUnchanged(want string) {
	f.t.Helper()
	if got := f.read(pathOnlineCatalogue); got != want {
		f.t.Errorf("the file in place changed: %q", got)
	}
}

func TestRefreshKeepsAValidNewerCatalogue(t *testing.T) {
	f := newFixture(t)
	f.writeFile(pathOnlineCatalogue, string(f.onlineRaw(3)))
	if r := f.refresh(bodyGet(f.onlineRaw(4))); !r.Kept || r.Serial != 4 || r.Reason != "" {
		t.Fatalf("result: %+v", r)
	}
	if f.read(pathOnlineCatalogue) != string(f.onlineRaw(4)) {
		t.Error("the new file is not in place")
	}
	info, err := os.Lstat(f.path(pathOnlineCatalogue))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("mode: %v, %v", info, err)
	}
	if cat, m := catalogueOf(t, f); cat["online"].(map[string]any)["serial"] != float64(4) || m["source"] != "online" {
		t.Errorf("the list after the refresh: %v %v", cat, m)
	}
}

func TestRefreshWritesTheFirstCatalogueWhereThereIsNone(t *testing.T) {
	f := newFixture(t)
	if r := f.refresh(bodyGet(f.onlineRaw(1))); !r.Kept || r.Serial != 1 {
		t.Fatalf("result: %+v", r)
	}
	if f.read(pathOnlineCatalogue) != string(f.onlineRaw(1)) {
		t.Error("the file is not in place")
	}
}

func TestRefreshAcceptsTheSameSerial(t *testing.T) {
	f := newFixture(t)
	f.writeFile(pathOnlineCatalogue, string(f.onlineRaw(4)))
	if r := f.refresh(bodyGet(f.onlineRaw(4))); !r.Kept || r.Serial != 4 || r.Reason != "" {
		t.Fatalf("result: %+v", r)
	}
}

func TestRefreshRefusesAnOlderSerial(t *testing.T) {
	f := newFixture(t)
	before := string(f.onlineRaw(5))
	f.writeFile(pathOnlineCatalogue, before)
	if r := f.refresh(bodyGet(f.onlineRaw(4))); r.Kept || r.Reason != "older" || r.Serial != 4 {
		t.Fatalf("result: %+v", r)
	}
	f.assertOnlineUnchanged(before)
}

func TestRefreshReplacesAFileInPlaceThatIsBroken(t *testing.T) {
	f := newFixture(t)
	f.writeFile(pathOnlineCatalogue, "{not json")
	if r := f.refresh(bodyGet(f.onlineRaw(1))); !r.Kept {
		t.Fatalf("result: %+v", r)
	}
}

func TestRefreshRefusesAPageThatIsNotACatalogue(t *testing.T) {
	f := newFixture(t)
	before := string(f.onlineRaw(5))
	f.writeFile(pathOnlineCatalogue, before)
	for name, body := range map[string]string{
		"html":     "<!DOCTYPE html><html><body>Not Found</body></html>",
		"schema 1": `{"schema":1,"modules":[]}`,
		"image":    f.onlineImageRaw(6),
	} {
		if r := f.refresh(bodyGet(body)); r.Kept || r.Reason != "invalid" {
			t.Errorf("%s: %+v", name, r)
		}
	}
	f.assertOnlineUnchanged(before)
}

func TestRefreshRefusesAnEmptyBody(t *testing.T) {
	f := newFixture(t)
	before := string(f.onlineRaw(5))
	f.writeFile(pathOnlineCatalogue, before)
	if r := f.refresh(bodyGet(nil)); r.Kept || r.Reason != "invalid" {
		t.Errorf("result: %+v", r)
	}
	f.assertOnlineUnchanged(before)
}

func TestRefreshRefusesACutBody(t *testing.T) {
	f := newFixture(t)
	before := string(f.onlineRaw(5))
	f.writeFile(pathOnlineCatalogue, before)
	full := f.onlineRaw(6)
	if r := f.refresh(bodyGet(full[:len(full)/2])); r.Kept || r.Reason != "invalid" {
		t.Errorf("result: %+v", r)
	}
	// a connection that drops in the middle is a fetch that failed, whatever was read
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		_, _ = w.Write(full[:len(full)/2])
	})
	if r := f.refresh(catalogueDownloaderTo(srv)); r.Kept || r.Reason != "fetch" {
		t.Errorf("a dropped connection: %+v", r)
	}
	f.assertOnlineUnchanged(before)
}

func TestRefreshKeepsAValidCatalogueOfSchema3(t *testing.T) {
	f := newFixture(t)
	body := online3Raw(t, 4, "[]", f.mirrorEntry(onlineTag, foreignRepo))
	if r := f.refresh(bodyGet(body)); !r.Kept || r.Serial != 4 {
		t.Fatalf("result: %+v", r)
	}
	if f.read(pathOnlineCatalogue) != body {
		t.Error("the file is not in place")
	}
}

// a catalogue of schema 3 is refused whole when one thing in it does not pass
func TestRefreshRefusesAnInvalidCatalogueOfSchema3(t *testing.T) {
	f := newFixture(t)
	before := string(f.onlineRaw(5))
	f.writeFile(pathOnlineCatalogue, before)
	author := f.mirrorEntry(onlineTag, foreignRepo)
	a := *author.Assets.AMD64
	a.URL = "https://github.com/someone/" + testComponent + "/releases/download/" + onlineTag + "/linux-amd64-casaos-virt-management-" + onlineTag + ".tar.gz"
	author.Assets.AMD64 = &a
	noRepo := f.mirrorEntry(onlineTag, "")
	for name, body := range map[string]string{
		"an archive of the author":   online3Raw(t, 6, "[]", author),
		"no repository":              online3Raw(t, 6, "[]", noRepo),
		"no withdrawn list":          strings.Replace(online3Raw(t, 6, "[]", f.mirrorEntry(onlineTag, foreignRepo)), `,"withdrawn":[]`, "", 1),
		"a withdrawn with no reason": online3Raw(t, 6, `[{"name":"virt","tag":"v0.1.0","reason":""}]`, f.mirrorEntry(onlineTag, foreignRepo)),
	} {
		if r := f.refresh(bodyGet(body)); r.Kept || r.Reason != "invalid" {
			t.Errorf("%s: %+v", name, r)
		}
	}
	f.assertOnlineUnchanged(before)
}

// the serial is compared across schemas: a box that holds a file of schema 2 takes one of schema 3 at the same serial or a higher one, never a lower one
func TestRefreshComparesTheSerialAcrossSchemas(t *testing.T) {
	for serial, kept := range map[int64]bool{4: false, 5: true, 6: true} {
		f := newFixture(t)
		before := string(f.onlineRaw(5))
		f.writeFile(pathOnlineCatalogue, before)
		body := online3Raw(t, serial, "[]", f.mirrorEntry(onlineTag, foreignRepo))
		r := f.refresh(bodyGet(body))
		if r.Kept != kept || !kept && r.Reason != "older" {
			t.Errorf("serial %d: %+v", serial, r)
		}
		if kept && f.read(pathOnlineCatalogue) != body || !kept && f.read(pathOnlineCatalogue) != before {
			t.Errorf("serial %d: the file in place is not the right one", serial)
		}
	}
}

// catalogueDownloaderTo is the catalogue's downloader, sent to the test server whatever the host.
func catalogueDownloaderTo(srv *httptest.Server) Downloader {
	return httpDownloader{client: clientTo(srv), max: osmodule.MaxCatalogueBytes, timeout: catalogueFetchTime}
}

func TestRefreshRefusesMoreThanOneMebibyte(t *testing.T) {
	f := newFixture(t)
	before := string(f.onlineRaw(5))
	f.writeFile(pathOnlineCatalogue, before)
	// a valid catalogue padded with spaces past the cap
	big := string(f.onlineRaw(6)) + strings.Repeat(" ", osmodule.MaxCatalogueBytes)
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, big) })
	if r := f.refresh(catalogueDownloaderTo(srv)); r.Kept || r.Reason != "fetch" {
		t.Errorf("from the network: %+v", r)
	}
	// and whatever the downloader it is given, the refresh takes no more than the cap
	if r := f.refresh(bodyGet(big)); r.Kept || r.Reason != "fetch" {
		t.Errorf("from a downloader with no cap: %+v", r)
	}
	f.assertOnlineUnchanged(before)
}

func TestRefreshRefusesARedirectOffGitHub(t *testing.T) {
	f := newFixture(t)
	before := string(f.onlineRaw(5))
	f.writeFile(pathOnlineCatalogue, before)
	body := f.onlineRaw(6)
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "github.com" {
			http.Redirect(w, r, "https://evil.example.com/catalogue-2.json", http.StatusFound)
			return
		}
		_, _ = w.Write(body)
	})
	if r := f.refresh(catalogueDownloaderTo(srv)); r.Kept || r.Reason != "fetch" {
		t.Errorf("result: %+v", r)
	}
	f.assertOnlineUnchanged(before)
}

func TestRefreshRefusesANon200(t *testing.T) {
	f := newFixture(t)
	before := string(f.onlineRaw(5))
	f.writeFile(pathOnlineCatalogue, before)
	body := f.onlineRaw(6)
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusNoContent} {
		srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write(body)
		})
		if r := f.refresh(catalogueDownloaderTo(srv)); r.Kept || r.Reason != "fetch" {
			t.Errorf("%d: %+v", status, r)
		}
	}
	f.assertOnlineUnchanged(before)
}

func TestTwoRefreshesLeaveOneWholeFile(t *testing.T) {
	f := newFixture(t)
	low, high := bodyGet(f.onlineRaw(8)), bodyGet(f.onlineRaw(9))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		get := low
		if i%2 == 1 {
			get = high
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.refresh(get)
		}()
	}
	wg.Wait()
	cat, err := osmodule.ParseOnlineCatalogue([]byte(f.read(pathOnlineCatalogue)))
	if err != nil || cat.Serial != 9 {
		t.Fatalf("the file in place: serial %d, %v", cat.Serial, err)
	}
	// no neighbour file is left behind but the lock
	entries, _ := os.ReadDir(f.path("/var/lib/casaos/modules"))
	for _, e := range entries {
		if e.Name() != "catalogue.json" && e.Name() != "catalogue.json.lock" {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}

func TestRefreshRefusesALinkWhereTheFileGoes(t *testing.T) {
	f := newFixture(t)
	target := f.path("/elsewhere.json")
	f.writeFile("/elsewhere.json", "kept")
	if err := os.MkdirAll(f.path("/var/lib/casaos/modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, f.path(pathOnlineCatalogue)); err != nil {
		t.Fatal(err)
	}
	if r := f.refresh(bodyGet(f.onlineRaw(1))); r.Kept || r.Reason != "write" {
		t.Errorf("a link: %+v", r)
	}
	if f.read("/elsewhere.json") != "kept" {
		t.Error("written through the link")
	}
	if info, err := os.Lstat(f.path(pathOnlineCatalogue)); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v %v", info, err)
	}

	// a directory where the file goes
	g := newFixture(t)
	if err := os.MkdirAll(g.path(pathOnlineCatalogue), 0o755); err != nil {
		t.Fatal(err)
	}
	if r := g.refresh(bodyGet(g.onlineRaw(1))); r.Kept || r.Reason != "write" {
		t.Errorf("a directory: %+v", r)
	}

	// a link where its directory is
	h := newFixture(t)
	elsewhere := h.path("/elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.path("/var/lib/casaos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, h.path("/var/lib/casaos/modules")); err != nil {
		t.Fatal(err)
	}
	if r := h.refresh(bodyGet(h.onlineRaw(1))); r.Kept || r.Reason != "write" {
		t.Errorf("a linked directory: %+v", r)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("written through the linked directory: %v", entries)
	}
}

func TestTheCatalogueDownloaderIsBounded(t *testing.T) {
	d := NewCatalogueDownloader().(httpDownloader)
	if d.max != osmodule.MaxCatalogueBytes || d.timeout <= 0 || d.timeout > time.Minute {
		t.Errorf("max %d, timeout %v", d.max, d.timeout)
	}
	if a := NewHTTPDownloader().(httpDownloader); a.max != osmodule.MaxArchiveBytes || a.timeout != downloadTryTime {
		t.Errorf("the archive downloader: max %d, timeout %v", a.max, a.timeout)
	}
}

// ---- the setting --------------------------------------------------------------------------------------------------------------

func TestTheOnlineFileIsNotReadWhenTheSettingIsOff(t *testing.T) {
	f := newFixture(t)
	f.writeOnline(f.onlineEntry(onlineTag))
	f.env.OnlineCatalogueOff = true
	cat, m := catalogueOf(t, f)
	if on := cat["online"].(map[string]any); on["ok"] != false || on["error"] != "off" || m["source"] != "shipped" || m["tag"] != testTag {
		t.Errorf("catalogue %v, module %v", cat, m)
	}
	if p := PlanOf(context.Background(), f.env, testName); p.Tag != testTag {
		t.Errorf("the plan reads the online file: %+v", p)
	}
	// nor through the variable of the test mode
	f.envVars["CASAOS_MODULE_TEST"] = "1"
	f.envVars["CASAOS_MODULE_ONLINE_CATALOGUE"] = f.path(pathOnlineCatalogue)
	if _, m := catalogueOf(t, f); m["source"] != "shipped" {
		t.Errorf("in the test mode: %v", m)
	}
}

func TestTheCommandReadsTheSettingFromTheCoreConfiguration(t *testing.T) {
	f := newFixture(t)
	for conf, off := range map[string]bool{
		"":                                     false, // no file: the default
		"[app]\nLogPath=/var/log/casaos\n":     false, // no section
		"[modules]\nOnlineCatalogue = true\n":  false,
		"[modules]\nOnlineCatalogue=false\n":   true,
		"[modules]\nOnlineCatalogue = no\n":    true,
		"[modules]\nOnlineCatalogue = maybe\n": false, // not a boolean: the default
		// the core maps the section with the key spelled as its field is
		"[modules]\nonlinecatalogue = false\n": false,
	} {
		if conf == "" {
			_ = os.Remove(f.path(pathCoreConfig))
		} else {
			f.writeFile(pathCoreConfig, conf)
		}
		if got := onlineCatalogueOff(f.env.Roots); got != off {
			t.Errorf("%q: off %v", conf, got)
		}
	}
}
