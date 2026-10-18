package modulecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

func (f *fixture) json(args ...string) map[string]any {
	f.t.Helper()
	if code := f.main(context.Background(), args...); code != ExitOK {
		f.t.Fatalf("%v: exit %d\n%s%s", args, code, f.out, f.errOut)
	}
	// one object on the standard output and nothing else: a single line, and the stream is that value and no more
	raw := f.out.Bytes()
	if !bytes.HasSuffix(raw, []byte("\n")) || bytes.Count(raw, []byte("\n")) != 1 {
		f.t.Fatalf("not one line: %q", raw)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		f.t.Fatalf("not a JSON object: %v\n%s", err, raw)
	}
	if dec.More() {
		f.t.Fatalf("more than one value: %s", raw)
	}
	return v
}

func TestListJoinsTheCatalogueWithWhatIsInstalled(t *testing.T) {
	f := newFixture(t)
	v := f.json("list")
	cat := v["catalogue"].(map[string]any)
	if cat["ok"] != true || cat["error"] != nil {
		t.Errorf("catalogue: %v", cat)
	}
	mods := v["modules"].([]any)
	if len(mods) != 1 {
		t.Fatalf("modules: %v", mods)
	}
	m := mods[0].(map[string]any)
	want := map[string]any{"name": "virt", "title": "Virtual machines", "description": "Create and run virtual machines.", "tier": "community", "tag": "v0.1.0", "catalogue": true, "installed": nil}
	for k, w := range want {
		if m[k] != w {
			t.Errorf("%s: %v, want %v", k, m[k], w)
		}
	}
	if sys := m["system"].(map[string]any); sys["supported"] != true || len(sys["reasons"].([]any)) != 0 {
		t.Errorf("system: %v", sys)
	}
	if _, has := m["broken"]; has {
		t.Errorf("broken: %v", m)
	}
	// installed, at its version
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("install %d", code)
	}
	m = f.json("list")["modules"].([]any)[0].(map[string]any)
	if inst, _ := m["installed"].(map[string]any); inst == nil || inst["version"] != "v0.1.0" {
		t.Errorf("installed: %v", m["installed"])
	}
	// the catalogue of a later release has another tag: the installed version is the one that was installed
	f.entry.Tag = "v0.2.0"
	f.entry.Assets.AMD64.URL = strings.ReplaceAll(f.entry.Assets.AMD64.URL, "v0.1.0", "v0.2.0")
	f.writeCatalogue(osmodule.Catalogue{Schema: 1, Modules: []osmodule.Entry{f.entry}})
	m = f.json("list")["modules"].([]any)[0].(map[string]any)
	if inst, _ := m["installed"].(map[string]any); m["tag"] != "v0.2.0" || inst["version"] != "v0.1.0" {
		t.Errorf("tag %v, installed %v", m["tag"], m["installed"])
	}
}

func TestListSaysWhyASystemCannotHaveAModule(t *testing.T) {
	f := newFixture(t)
	f.writeFile("/etc/os-release", "ID=ubuntu\nVERSION_ID=\"20.04\"\n")
	f.env.Version = "0.5.1"
	f.env.Arch = "arm64"
	m := f.json("list")["modules"].([]any)[0].(map[string]any)
	sys := m["system"].(map[string]any)
	if sys["supported"] != false || !slices.Equal(anyStrings(sys["reasons"]), []string{"os", "arch", "recasaos"}) {
		t.Errorf("system: %v", sys)
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

func TestListWithoutAGoodCatalogueSaysSoAndStillShowsWhatIsInstalled(t *testing.T) {
	f := installed(t)
	for name, c := range map[string]struct {
		setup  func()
		err    string
		detail string
	}{
		"none":         {func() { _ = os.Remove(f.path(pathCatalogue)) }, "none", ""},
		"not json":     {func() { f.writeFile(pathCatalogue, "{") }, "invalid", ""},
		"a bad entry":  {func() { f.writeFile(pathCatalogue, `{"schema":1,"modules":[{"name":"virt"}]}`) }, "invalid", "modules.0."},
		"a bad schema": {func() { f.writeFile(pathCatalogue, `{"schema":2,"modules":[]}`) }, "invalid", "schema "},
		"too large":    {func() { f.writeFile(pathCatalogue, strings.Repeat(" ", osmodule.MaxCatalogueBytes+1)) }, "invalid", "catalogue too_large"},
		"a directory":  {func() { _ = os.Remove(f.path(pathCatalogue)); _ = os.Mkdir(f.path(pathCatalogue), 0o755) }, "unreadable", ""},
	} {
		t.Run(name, func(t *testing.T) {
			c.setup()
			v := f.json("list")
			cat := v["catalogue"].(map[string]any)
			if cat["ok"] != false || cat["error"] != c.err || !strings.HasPrefix(str(cat["detail"]), c.detail) {
				t.Errorf("catalogue: %v", cat)
			}
			mods := v["modules"].([]any)
			if len(mods) != 1 {
				t.Fatalf("modules: %v", mods)
			}
			m := mods[0].(map[string]any)
			if m["name"] != "virt" || m["catalogue"] != false || m["installed"] == nil || m["title"] != "Virtual machines" {
				t.Errorf("the module: %v", m)
			}
			if sys := m["system"].(map[string]any); sys["supported"] != false {
				t.Errorf("system: %v", sys)
			}
		})
	}
}

func str(v any) string { s, _ := v.(string); return s }

func TestListShowsAManifestThatIsBroken(t *testing.T) {
	f := newFixture(t)
	f.writeFile("/etc/casaos/modules/virt.json", `{"schema":1}`)
	f.writeFile("/etc/casaos/modules/other.json", `not json`)
	f.writeFile("/etc/casaos/modules/Bad_Name.json", `{}`)
	f.writeFile("/etc/casaos/modules/notes.txt", `x`)
	mods := f.json("list")["modules"].([]any)
	byName := map[string]map[string]any{}
	for _, m := range mods {
		byName[m.(map[string]any)["name"].(string)] = m.(map[string]any)
	}
	if len(byName) != 2 || byName["virt"]["broken"] != "manifest_invalid" || byName["virt"]["installed"] != nil || byName["other"]["broken"] != "manifest_invalid" || byName["other"]["catalogue"] != false {
		t.Errorf("modules: %v", mods)
	}
}

func TestPlanIsOneObjectWithTheChecksInOrder(t *testing.T) {
	f := newFixture(t)
	v := f.json("plan", "virt")
	var codes []string
	for _, c := range v["checks"].([]any) {
		m := c.(map[string]any)
		if m["ok"] != true {
			t.Errorf("check %v", m)
		}
		codes = append(codes, m["code"].(string))
	}
	if !slices.Equal(codes, []string{"catalogue", "os", "arch", "recasaos", "already_installed", "foreign_unit", "busy", "disk", "apt"}) {
		t.Errorf("checks: %v", codes)
	}
	pk := v["packages"].(map[string]any)
	if v["refused"] != nil || v["name"] != "virt" || v["tag"] != "v0.1.0" || v["tier"] != "community" || !strings.HasPrefix(str(v["plan_id"]), "m1-") ||
		!slices.Equal(anyStrings(pk["to_install"]), []string{"libfdt1", "libvirt-clients", "libvirt0", "qemu-utils"}) || pk["known"] != true || pk["download_bytes"].(float64) != 44_300_000 {
		t.Errorf("plan: %v", v)
	}
	if arch := v["archive"].(map[string]any); arch["url"] != f.entry.Assets.AMD64.URL {
		t.Errorf("archive: %v", arch)
	}
	// a plan that is refused still is the object, with the code and no id
	f.writeFile("/etc/os-release", "ID=debian\nVERSION_ID=\"11\"\n")
	v = f.json("plan", "virt")
	if v["refused"] != "os" || v["plan_id"] != "" {
		t.Errorf("refused plan: %v", v)
	}
	// and a name that is not in the catalogue
	v = f.json("plan", "nothing")
	if v["refused"] != "catalogue" {
		t.Errorf("unknown: %v", v)
	}
}

func TestStatusSaysWhatSystemdAndTheGatewaySay(t *testing.T) {
	f := newFixture(t)
	v := f.json("status", "virt")
	if v["installed"] != false || v["version"] != nil || v["unit"].(map[string]any)["active"] != false || v["route"].(map[string]any)["answers"] != false {
		t.Errorf("not installed: %v", v)
	}
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("install %d", code)
	}
	f.gw.calls = 0
	v = f.json("status", "virt")
	if v["installed"] != true || v["version"] != "v0.1.0" || v["name"] != "virt" || v["unit"].(map[string]any)["active"] != true || v["unit"].(map[string]any)["enabled"] != true || v["route"].(map[string]any)["answers"] != true {
		t.Errorf("installed: %v", v)
	}
	f.gw.never = true
	delete(f.runner.enabled, testUnit)
	v = f.json("status", "virt")
	if v["unit"].(map[string]any)["active"] != false || v["unit"].(map[string]any)["enabled"] != false || v["route"].(map[string]any)["answers"] != false {
		t.Errorf("stopped: %v", v)
	}
	// without a name: all that are installed
	all := f.json("status")["modules"].([]any)
	if len(all) != 1 || all[0].(map[string]any)["name"] != "virt" {
		t.Errorf("all: %v", all)
	}
}

// ---- nothing is written by a command that only reads ------------------------------------------------------------------------------------------------------------------

// snapshot is every path under the root, directories too, with the content, the mode and the link of each: what an invariance test compares.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			out[rel] = "link " + target
		case d.IsDir():
			out[rel] = "dir"
		default:
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = "file " + sum(raw) + " " + info.ModTime().String()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestListAndPlanAndStatusWriteNothingOnTheBox(t *testing.T) {
	setups := map[string]func(f *fixture){
		"empty":     func(f *fixture) {},
		"installed": func(f *fixture) { f.install("--yes") },
		"refused":   func(f *fixture) { f.writeFile("/etc/os-release", "ID=debian\nVERSION_ID=\"9\"\n") },
		"broken": func(f *fixture) {
			f.writeFile("/etc/casaos/modules/virt.json", "{")
			f.writeFile("/var/lib/casaos/modules/virt.files", "/usr/bin/casaos-virt-management\n")
			f.writeFile("/var/lib/casaos/modules/tmp/left", "x")
		},
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			setup(f)
			before := snapshot(t, f.root)
			calls := len(f.runner.calls)
			for _, args := range [][]string{{"list"}, {"plan", "virt"}, {"plan", "other"}, {"status"}, {"status", "virt"}, {"plan", "virt"}} {
				f.main(context.Background(), args...)
			}
			after := snapshot(t, f.root)
			var diffs []string
			for p, v := range after {
				if before[p] != v {
					diffs = append(diffs, p)
				}
			}
			for p := range before {
				if _, ok := after[p]; !ok {
					diffs = append(diffs, "gone "+p)
				}
			}
			sort.Strings(diffs)
			if len(diffs) > 0 {
				t.Errorf("the box changed:\n%s", strings.Join(diffs, "\n"))
			}
			// the only commands they run ask systemd
			for _, c := range f.runner.calls[calls:] {
				if c[0] != "systemctl" || !slices.Contains([]string{"show", "is-active", "is-enabled"}, c[1]) {
					t.Errorf("a command that is not a question: %q", c)
				}
			}
			if f.host.lockCalls != 0 && name != "installed" {
				t.Errorf("a lock was taken")
			}
		})
	}
}

// ---- the test hooks -------------------------------------------------------------------------------------------------------------------------------------------------------

func TestTheCatalogueOfTheTestsIsHonouredOnlyWhenTheTestVariableIsExactlyOne(t *testing.T) {
	f := newFixture(t)
	other := f.entry
	other.Name = "other"
	other.Manifest.Name = "other"
	other.Assets = osmodule.Assets{AMD64: &osmodule.Asset{URL: strings.ReplaceAll(f.entry.Assets.AMD64.URL, "virt-management", "other"), SHA256: strings.Repeat("0", 64)}}
	other.Component, other.Manifest.Component = "Other", "Other"
	other.Assets.AMD64.URL = "https://github.com/ReCasaOS/Other/releases/download/v0.1.0/linux-amd64-casaos-other-v0.1.0.tar.gz"
	raw, _ := json.Marshal(osmodule.Catalogue{Schema: 1, Modules: []osmodule.Entry{other}})
	elsewhere := filepath.Join(t.TempDir(), "catalogue.json")
	if err := os.WriteFile(elsewhere, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		var out []string
		for _, m := range f.json("list")["modules"].([]any) {
			out = append(out, m.(map[string]any)["name"].(string))
		}
		return out
	}
	f.envVars["CASAOS_MODULE_CATALOGUE"] = elsewhere
	for _, test := range []string{"", "0", "true", "yes", " 1", "1 ", "01", "1\n", "11", "TRUE", "on"} {
		f.envVars["CASAOS_MODULE_TEST"] = test
		if got := names(); !slices.Equal(got, []string{"virt"}) {
			t.Errorf("CASAOS_MODULE_TEST=%q: %v", test, got)
		}
		if v := f.json("plan", "other"); v["refused"] != "catalogue" {
			t.Errorf("CASAOS_MODULE_TEST=%q: plan of the other: %v", test, v["refused"])
		}
	}
	delete(f.envVars, "CASAOS_MODULE_TEST")
	if got := names(); !slices.Equal(got, []string{"virt"}) {
		t.Errorf("no test variable: %v", got)
	}
	f.envVars["CASAOS_MODULE_TEST"] = "1"
	if got := names(); !slices.Equal(got, []string{"other"}) {
		t.Errorf("CASAOS_MODULE_TEST=1: %v", got)
	}
	// the test variable and no catalogue is the shipped one
	delete(f.envVars, "CASAOS_MODULE_CATALOGUE")
	if got := names(); !slices.Equal(got, []string{"virt"}) {
		t.Errorf("no catalogue variable: %v", got)
	}
}

func TestTheArchiveOfTheTestsIsHonouredOnlyWhenTheTestVariableIsExactlyOne(t *testing.T) {
	f := newFixture(t)
	local := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(local, f.archive, 0o644); err != nil {
		t.Fatal(err)
	}
	f.dl.err = &PermanentError{Err: os.ErrNotExist} // the network is not there
	f.envVars["CASAOS_MODULE_ARCHIVE_FILE"] = local
	for _, test := range []string{"", "0", "true", " 1", "1 ", "yes"} {
		f.envVars["CASAOS_MODULE_TEST"] = test
		if d := downloaderOf(f.env); d != Downloader(f.dl) {
			t.Errorf("CASAOS_MODULE_TEST=%q: %T", test, d)
		}
	}
	f.envVars["CASAOS_MODULE_TEST"] = "1"
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("with the test variable: %d\n%s", code, f.out)
	}
	// ...and the sha256 of the catalogue is checked all the same
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("remove: %d", code)
	}
	if err := os.WriteFile(local, append(slices.Clone(f.archive), 0), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := f.install("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), "failed at step verify: checksum_mismatch") {
		t.Errorf("a file that is not the archive: %d\n%s", code, f.out)
	}
}

// ---- the box, as the plan asks about it ----------------------------------------------------------------------------------------------------------------------

func TestTheBoxGivesNamesAndNotFileNames(t *testing.T) {
	f := newFixture(t)
	good := manifestJSON(t, testManifest())
	f.writeFile("/etc/casaos/modules/virt.json", good)
	f.writeFile("/etc/casaos/modules/broken.json", "{")
	f.writeFile("/etc/casaos/modules/readme.txt", "x")
	bx := &box{ctx: context.Background(), env: &f.env}
	installed, broken, err := bx.InstalledManifests()
	if err != nil || len(installed) != 1 || installed["virt"].Name != "virt" || !slices.Equal(broken, []string{"broken"}) {
		t.Errorf("installed %v, broken %v, %v", installed, broken, err)
	}
	if _, wrong := installed["virt.json"]; wrong {
		t.Error("a file name")
	}
}

func TestTheBoxGivesOneSpellingOfAUnitFile(t *testing.T) {
	f := newFixture(t)
	bx := &box{ctx: context.Background(), env: &f.env}
	if got, err := bx.UnitFiles(testUnit); err != nil || len(got) != 0 {
		t.Fatalf("no unit: %v, %v", got, err)
	}
	f.writeFile("/usr/lib/systemd/system/"+testUnit, "[Unit]\n")
	if got, err := bx.UnitFiles(testUnit); err != nil || !slices.Equal(got, []string{"/usr/lib/systemd/system/" + testUnit}) {
		t.Errorf("one unit: %v, %v", got, err)
	}
	// the other directories, each its own file
	f.writeFile("/etc/systemd/system/"+testUnit, "[Unit]\n")
	f.writeFile("/run/systemd/system/"+testUnit, "[Unit]\n")
	got, err := bx.UnitFiles(testUnit)
	want := []string{"/usr/lib/systemd/system/" + testUnit, "/etc/systemd/system/" + testUnit, "/run/systemd/system/" + testUnit}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("three units: %v, %v", got, err)
	}
	// what the list of a module spells as /lib is that module's even when the unit is found under /usr/lib: see the test of a merged /usr
}

func TestTheBoxReadsTheSystemAndTheFilesLists(t *testing.T) {
	f := newFixture(t)
	bx := &box{ctx: context.Background(), env: &f.env}
	for text, want := range map[string][2]string{
		"ID=debian\nVERSION_ID=\"12\"\n":                    {"debian", "12"},
		"ID=ubuntu\nVERSION_ID=\"24.04\"\nID_LIKE=debian\n": {"ubuntu", "24.04"},
		"# comment\nID='debian'\nVERSION_ID='11'\n":         {"debian", "11"},
		"ID=debian\n":                       {"", ""},
		"":                                  {"", ""},
		"ID=\"deb ian\"\nVERSION_ID=12\n":   {"", ""},
		"ID=debian\nVERSION_ID=12;reboot\n": {"", ""},
		"ID=debian\nVERSION_ID=\"12\n":      {"", ""},
	} {
		f.writeFile(pathOSRelease, text)
		if id, v := bx.OS(); id != want[0] || v != want[1] {
			t.Errorf("%q: %q %q", text, id, v)
		}
	}
	_ = os.Remove(f.path(pathOSRelease))
	if id, v := bx.OS(); id != "" || v != "" {
		t.Errorf("no file: %q %q", id, v)
	}
	if bx.Arch() != "amd64" || bx.ReCasaOSVersion() != "0.5.29" {
		t.Errorf("%q %q", bx.Arch(), bx.ReCasaOSVersion())
	}
	// the lists
	f.writeFile("/var/lib/casaos/modules/virt.files", "/usr/bin/a\n/usr/bin/b\n")
	f.writeFile("/var/lib/casaos/modules/other.files", "/usr/bin/c\n")
	f.writeFile("/var/lib/casaos/modules/.lock", "123\n")
	f.writeFile("/var/lib/casaos/modules/virt.version", "v0.1.0\n")
	lists, err := bx.FilesLists()
	if err != nil || len(lists) != 2 || !slices.Equal(lists["virt"], []string{"/usr/bin/a", "/usr/bin/b"}) || !slices.Equal(lists["other"], []string{"/usr/bin/c"}) {
		t.Errorf("lists: %v, %v", lists, err)
	}
	// a list that does not read is an error for all, not a list that is missing
	f.writeFile("/var/lib/casaos/modules/bad.files", "not a path\n")
	if lists, err := bx.FilesLists(); err == nil {
		t.Errorf("a bad list is taken: %v", lists)
	}
}

func TestTheBoxSaysBusyWhenAnythingAtAllIsUnclear(t *testing.T) {
	f := newFixture(t)
	bx := &box{ctx: context.Background(), env: &f.env}
	if busy, err := bx.OSUpgradeBusy(); busy || err != nil {
		t.Errorf("free: %v %v", busy, err)
	}
	f.writeFile(pathOSBusy, "")
	if busy, err := bx.OSUpgradeBusy(); !busy || err != nil {
		t.Errorf("flag: %v %v", busy, err)
	}
	if busy, err := bx.PackageManagerBusy(); busy || err != nil {
		t.Errorf("free: %v %v", busy, err)
	}
	for _, unit := range []string{"casaos-update.service", "casaos-package-update.service", "casaos-docker-update.service", "casaos-os-upgrade.service"} {
		f.runner.busyUnit = unit
		if busy, err := bx.PackageManagerBusy(); !busy || err != nil {
			t.Errorf("%s: %v %v", unit, busy, err)
		}
	}
	// the unit of a module is not a package manager: an install runs in one
	f.runner.busyUnit = "casaos-module-virt.service"
	if busy, _ := bx.PackageManagerBusy(); busy {
		t.Error("the unit of a module is busy")
	}
	// the locks are the three of dpkg and apt, under the root of the box
	var asked []string
	f.host.dpkg = func(paths []string) (bool, error) { asked = paths; return false, nil }
	_, _ = bx.PackageManagerBusy()
	var want []string
	for _, p := range dpkgLocks {
		want = append(want, f.path(p))
	}
	if !slices.Equal(asked, want) {
		t.Errorf("locks: %q", asked)
	}
}

// the release that min_recasaos is a number of is the distribution's, which the installer writes in /var/lib/casaos/fork-release, and not the version of the core
func TestProductionAsksTheReleaseOfTheDistribution(t *testing.T) {
	env := Production()
	if env.Version != distributionVersion(Roots{}) {
		t.Errorf("version %q, the distribution's is %q", env.Version, distributionVersion(Roots{}))
	}
	if env.Version == common.VERSION {
		t.Errorf("the version of the core: %q", env.Version)
	}
	// ...and not the release this binary was built for when the box has no marker (version.CurrentVersion() says that one)
	if _, err := os.Stat(common.FORK_RELEASE_FILE); err != nil && env.Version != "" {
		t.Errorf("a box with no marker has the release %q", env.Version)
	}
	if env.Roots.Prefix != "" || env.Getenv == nil || env.EUID == nil || env.Download == nil || env.Clock == nil || env.Host == nil || env.Terminal == nil {
		t.Errorf("not wired: %+v", env)
	}
}
