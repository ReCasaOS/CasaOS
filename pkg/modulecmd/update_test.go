package modulecmd

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The tests of the update: a box with v0.1.0 installed, a catalogue that names v0.1.1, and the stand-ins of the install's tests. A box that is "as it was" is compared by every file of it
// (its content, its mode), the lists, the manifest and the version included.

const (
	newTag     = "v0.1.1"
	helperPath = "/usr/bin/casaos-virt-helper"
	toolPath   = "/usr/bin/casaos-virt-tool"
	progPath   = "/usr/bin/casaos-virt-management"
	samplePath = "/etc/casaos/virt-management.conf.sample"
	unitPath   = "/usr/lib/systemd/system/" + testUnit
	unitV1     = "[Unit]\nDescription=virt\n[Service]\nExecStart=/usr/bin/casaos-virt-management\n[Install]\nWantedBy=multi-user.target\n"
	unitV2     = "[Unit]\nDescription=virt 2\n[Service]\nExecStart=/usr/bin/casaos-virt-management\n[Install]\nWantedBy=multi-user.target\n"
)

func withEntries(ents []tent) fixtureOpt { return func(c *fixtureCfg) { c.entries = ents } }

// v1Entries is the archive the box is installed from: the program, a helper that the next version no longer has, the sample and the unit.
func v1Entries(t *testing.T) []tent {
	return append(moduleEntries(manifestJSON(t, testManifest())), tent{name: "build/sysroot" + helperPath, mode: 0o755, body: "helper v1\n"})
}

// v2Entries is the archive of the next version: another program, a tool that is new, another sample and another unit, and no helper.
func v2Entries(t *testing.T, m osmodule.Manifest) []tent {
	return []tent{
		{name: "module.json", body: manifestJSON(t, m)},
		{name: "LICENSE", body: "licence\n"},
		{name: "build/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot" + progPath, mode: 0o755, body: "#!/bin/sh\necho v2\n"},
		{name: "build/sysroot" + toolPath, mode: 0o755, body: "tool v2\n"},
		{name: "build/sysroot" + samplePath, body: "[libvirt]\nURI = test:///system\n"},
		{name: "build/sysroot" + unitPath, body: unitV2},
	}
}

// publish makes the catalogue name the tag and the archive, and the downloader serve it.
func (f *fixture) publish(tag string, m osmodule.Manifest, ents []tent) {
	f.t.Helper()
	f.archive = gz(f.t, tarBytes(f.t, ents))
	f.entry.Tag = tag
	f.entry.Manifest = m
	f.entry.Assets.AMD64 = &osmodule.Asset{
		URL:    "https://github.com/ReCasaOS/" + testComponent + "/releases/download/" + tag + "/linux-amd64-casaos-virt-management-" + tag + ".tar.gz",
		SHA256: sum(f.archive),
	}
	f.writeCatalogue(osmodule.Catalogue{Schema: 1, Modules: []osmodule.Entry{f.entry}})
	f.dl.body = f.archive
}

// updatable is a box with v0.1.0 installed and the catalogue at v0.1.1.
func updatable(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, withEntries(v1Entries(t)))
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("install: exit %d\n%s", code, f.out)
	}
	f.publish(newTag, testManifest(), v2Entries(t, testManifest()))
	return f
}

func (f *fixture) update(args ...string) int {
	f.t.Helper()
	return f.main(context.Background(), append([]string{"update", testName}, args...)...)
}

var updateSteps = []string{StepPackages, StepDownload, StepVerify, StepUnpack, StepBackup, StepStop, StepSwap, StepUnits, StepStart, StepRoute}

// wantAfter are the files of the box once v0.1.1 is installed over v0.1.0, by name.
var wantAfter = map[string]string{
	progPath:   "#!/bin/sh\necho v2\n",
	toolPath:   "tool v2\n",
	samplePath: "[libvirt]\nURI = test:///system\n",
	unitPath:   unitV2,
}

func TestAnUpdateDoesTheStepsInOrderAndPutsTheNewVersionOnTheBox(t *testing.T) {
	f := updatable(t)
	f.writeFile("/etc/casaos/virt-management.conf", "mine")
	calls := len(f.runner.calls)
	downloads := f.dl.calls
	if code := f.update("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s%s", code, f.out, f.errOut)
	}
	var want []string
	for _, s := range updateSteps {
		want = append(want, s+" begin", s+" done")
	}
	want = append(want, "done done")
	if got := markers(t, f.lines()); !slices.Equal(got, want) {
		t.Errorf("markers:\n got %q\nwant %q", got, want)
	}
	for p, content := range wantAfter {
		if got := f.read(p); got != content {
			t.Errorf("%s holds %q", p, got)
		}
	}
	if f.exists(helperPath) {
		t.Error("the helper, which the new version does not have, is still there")
	}
	// the list says what each file holds, the unit last; the manifest and the version are the new ones
	list, err := osmodule.ParseFilesList([]byte(f.read("/var/lib/casaos/modules/virt.files")))
	if err != nil || !list.Verified || !slices.Equal(sorted(list.Paths()), sorted([]string{progPath, toolPath, samplePath, unitPath})) || list.Paths()[len(list.Paths())-1] != unitPath {
		t.Fatalf("the list of files: %+v, %v", list, err)
	}
	for _, e := range list.Entries {
		if e.Want[0] != sum([]byte(wantAfter[e.Path])) {
			t.Errorf("%s is listed as %s", e.Path, e.Want[0])
		}
	}
	dirs, err := osmodule.ParseDirs([]byte(f.read("/var/lib/casaos/modules/virt.dirs")))
	if err != nil || !slices.Equal(sorted(dirs), sorted(madeDirs)) {
		t.Errorf("the list of directories: %v, %v", dirs, err)
	}
	if v := f.read("/var/lib/casaos/modules/virt.version"); v != newTag+"\n" {
		t.Errorf("the version: %q", v)
	}
	// the administrator's configuration is not the module's
	if f.read("/etc/casaos/virt-management.conf") != "mine" {
		t.Error("the configuration was touched")
	}
	// the commands: no apt, the unit stopped, systemd told, the unit started
	var cmds []string
	for _, c := range f.runner.calls[calls:] {
		if c[0] == "apt-get" || c[1] == "daemon-reload" || c[1] == "stop" || c[1] == "start" || c[1] == "enable" || c[1] == "disable" {
			cmds = append(cmds, strings.Join(c, " "))
		}
	}
	if wantCmds := []string{"systemctl stop " + testUnit, "systemctl daemon-reload", "systemctl start " + testUnit}; !slices.Equal(cmds, wantCmds) {
		t.Errorf("commands:\n got %q\nwant %q", cmds, wantCmds)
	}
	if !f.runner.isRunning(testUnit) || !f.runner.isEnabled(testUnit) {
		t.Error("the unit is not running and enabled")
	}
	if f.dl.calls != downloads+1 || f.dl.urls[len(f.dl.urls)-1] != f.entry.Assets.AMD64.URL {
		t.Errorf("downloads: %q", f.dl.urls)
	}
	// the backup, the staging and the download are gone
	if entries, _ := os.ReadDir(f.env.Roots.tmp()); len(entries) != 0 {
		t.Errorf("the area of the download is not empty: %v", entries)
	}
	if last := f.lines()[len(f.lines())-1]; !strings.HasPrefix(last, resultMarker+" - virt ok done - - ") {
		t.Errorf("the last line: %q", last)
	}
	checkGrammar(t, f, ExitOK, false, false, "")
	// a run that has nothing to put back says nothing of a backup
	if strings.Contains(f.out.String(), "put back") || strings.Contains(f.out.String(), "recover") {
		t.Errorf("an update that went well talks of a backup:\n%s", f.out)
	}
	// the list says the module is up to date, and a second update has nothing to do
	if u := listOf(t, f).module(testName).Update; u == nil || u.Available || u.Installed != newTag || u.Latest != newTag {
		t.Errorf("list: %+v", u)
	}
	before := f.treeWithoutBookkeeping()
	if code := f.update("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+osmodule.RefusedUpToDate+"\n") {
		t.Errorf("a second update: %d\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
}

// ---- the list and the plan ------------------------------------------------------------------------------------------------------------------------------------------------------

type listed struct{ ListResult }

func (l listed) module(name string) ListModule {
	for _, m := range l.Modules {
		if m.Name == name {
			return m
		}
	}
	return ListModule{}
}

func listOf(t *testing.T, f *fixture) listed {
	t.Helper()
	if code := f.main(context.Background(), "list"); code != ExitOK {
		t.Fatalf("list: %d\n%s", code, f.out)
	}
	var res ListResult
	if err := json.Unmarshal(f.out.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	return listed{res}
}

func TestTheListSaysWhetherTheCatalogueHasALaterVersion(t *testing.T) {
	setVersion := func(f *fixture, v string) { f.writeFile("/var/lib/casaos/modules/virt.version", v) }
	retag := func(f *fixture, tag string) { f.publish(tag, testManifest(), v2Entries(f.t, testManifest())) }
	for _, c := range []struct {
		name        string
		installed   string // "" is v0.1.0
		catalogue   string
		available   bool
		hasUpdate   bool
		wantVersion string
	}{
		{"a later tag", "", "v0.1.1", true, true, "v0.1.0"},
		{"the same tag", "", "v0.1.0", false, true, "v0.1.0"},
		{"an earlier tag in the catalogue", "v0.2.0\n", "v0.1.1", false, true, "v0.2.0"},
		{"v0.1.10 is later than v0.1.9", "v0.1.9\n", "v0.1.10", true, true, "v0.1.9"},
		{"v0.1.9 is not later than v0.1.10", "v0.1.10\n", "v0.1.9", false, true, "v0.1.10"},
		{"a version that is not a tag", "latest\n", "v0.1.1", false, false, ""},
		{"a version with a suffix", "v0.1.0-rc1\n", "v0.1.1", false, false, ""},
		{"an empty version", "\n", "v0.1.1", false, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			if code := f.install("--yes"); code != ExitOK {
				t.Fatal(f.out.String())
			}
			if c.installed != "" {
				setVersion(f, c.installed)
			}
			retag(f, c.catalogue)
			before := f.tree()
			m := listOf(t, f).module(testName)
			sameTree(t, before, f.tree())
			switch {
			case !c.hasUpdate && m.Update != nil:
				t.Errorf("an update is claimed: %+v", m.Update)
			case c.hasUpdate && (m.Update == nil || m.Update.Available != c.available || m.Update.Installed != c.wantVersion || m.Update.Latest != c.catalogue):
				t.Errorf("update: %+v", m.Update)
			}
		})
	}
	// a module that is not installed has no update; a field that is not there is not in the JSON
	f := newFixture(t)
	if m := listOf(t, f).module(testName); m.Update != nil || m.Installed != nil {
		t.Errorf("%+v", m)
	}
	if strings.Contains(f.out.String(), `"update"`) {
		t.Errorf("the JSON of a module that is not installed has an update: %s", f.out)
	}
	// an update that is available is in the JSON as the contract says
	f = updatable(t)
	f.out.Reset()
	listOf(t, f)
	if !strings.Contains(f.out.String(), `"update":{"available":true,"installed":"v0.1.0","latest":"v0.1.1"}`) {
		t.Errorf("the JSON: %s", f.out)
	}
}

func (f *fixture) planUpdate() osmodule.PlanResult {
	f.t.Helper()
	if code := f.main(context.Background(), "plan", testName, "--update"); code != ExitOK {
		f.t.Fatalf("plan: %d\n%s", code, f.out)
	}
	var plan osmodule.PlanResult
	if err := json.Unmarshal(f.out.Bytes(), &plan); err != nil {
		f.t.Fatal(err)
	}
	return plan
}

func TestThePlanOfAnUpdateAsksTheBoxAndWritesNothing(t *testing.T) {
	f := updatable(t)
	before := f.tree()
	calls := len(f.runner.calls)
	plan := f.planUpdate()
	if plan.Refused != "" || plan.Mode != "update" || plan.Tag != newTag || plan.Update == nil || plan.Update.Installed != "v0.1.0" || plan.Update.Latest != newTag || plan.PlanID == "" {
		t.Fatalf("%+v %+v", plan, plan.Update)
	}
	var codes []string
	for _, c := range plan.Checks {
		codes = append(codes, c.Code)
		if !c.OK {
			t.Errorf("%+v", c)
		}
	}
	if want := []string{"catalogue", "os", "arch", "recasaos", "installed", "newer", "foreign_unit", "busy", "dpkg_state", "disk", "apt"}; !slices.Equal(codes, want) {
		t.Errorf("checks %v", codes)
	}
	sameTree(t, before, f.tree())
	for _, c := range f.runner.calls[calls:] {
		if c[0] == "apt-get" && c[1] == "install" || c[0] == "systemctl" && c[1] != "show" {
			t.Errorf("the plan ran %q", c)
		}
	}
	if f.dl.calls != 1 {
		t.Errorf("the plan downloaded: %d", f.dl.calls)
	}
	// the plan of an install says nothing of a mode
	f.main(context.Background(), "plan", testName)
	if strings.Contains(f.out.String(), `"mode"`) || strings.Contains(f.out.String(), `"update"`) {
		t.Errorf("an install plan: %s", f.out)
	}
}

func TestThePlanOfAnUpdateIsRefusedForEachReasonAndAnUpdateWithIt(t *testing.T) {
	older := func(f *fixture) { f.publish("v0.0.9", testManifest(), v2Entries(f.t, testManifest())) }
	for _, c := range []struct {
		name  string
		setup func(f *fixture)
		code  string
	}{
		{"the same version", func(f *fixture) { f.publish(testTag, testManifest(), v1Entries(f.t)) }, osmodule.RefusedUpToDate},
		{"an installed version that is later", older, osmodule.RefusedDowngrade},
		{"an installed version that is lost", func(f *fixture) { _ = os.Remove(f.path("/var/lib/casaos/modules/virt.version")) }, osmodule.RefusedVersionUnknown},
		{"an installed version that is no tag", func(f *fixture) { f.writeFile("/var/lib/casaos/modules/virt.version", "v1\n") }, osmodule.RefusedVersionUnknown},
		{"a list of files of the earlier format", func(f *fixture) {
			f.writeFile("/var/lib/casaos/modules/virt.files", progPath+"\n"+unitPath+"\n")
		}, osmodule.RefusedFilesUnverified},
		{"no list of files", func(f *fixture) { _ = os.Remove(f.path("/var/lib/casaos/modules/virt.files")) }, osmodule.RefusedFilesUnreadable},
		{"a list of files that is cut short", func(f *fixture) { f.writeFile("/var/lib/casaos/modules/virt.files", "casaos-module-files 1\n") }, osmodule.RefusedFilesUnreadable},
		{"a manifest that is broken", func(f *fixture) { f.writeFile("/etc/casaos/modules/virt.json", "{") }, osmodule.RefusedManifestInvalid},
		{"nothing installed", func(f *fixture) { _ = os.Remove(f.path("/etc/casaos/modules/virt.json")) }, osmodule.RefusedNotInstalled},
		{"a unit of the name that is not the module's", func(f *fixture) { f.writeFile("/etc/systemd/system/"+testUnit, "[Unit]\n") }, osmodule.CheckForeignUnit},
		{"another system", func(f *fixture) { f.writeFile("/etc/os-release", "ID=debian\nVERSION_ID=\"11\"\n") }, osmodule.CheckOS},
		{"a package manager at work", func(f *fixture) { f.runner.busyUnit = "casaos-package-update.service" }, osmodule.CheckBusy},
		{"an upgrade of the system is open", func(f *fixture) { f.writeFile(pathOSBusy, "{}") }, osmodule.CheckBusy},
		{"no room", func(f *fixture) { f.host.free = func(string) (int64, error) { return 1 << 20, nil } }, osmodule.RefusedDiskShort},
		{"no catalogue", func(f *fixture) { _ = os.Remove(f.path(pathCatalogue)) }, osmodule.CheckCatalogue},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := updatable(t)
			c.setup(f)
			if plan := f.planUpdate(); string(plan.Refused) != c.code || plan.PlanID != "" {
				t.Fatalf("plan refused %q: %+v", plan.Refused, plan.Checks)
			}
			before := f.treeWithoutBookkeeping()
			calls := len(f.runner.calls)
			if code := f.update("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+c.code+"\n") {
				t.Fatalf("update: %d\n%s", code, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			for _, call := range f.runner.calls[calls:] {
				if call[0] == "systemctl" && call[1] != "show" || call[0] == "apt-get" && call[1] == "install" {
					t.Errorf("a refused update ran %q", call)
				}
			}
			if got := markers(t, f.lines()); len(got) != 0 {
				t.Errorf("a refused run has steps: %q", got)
			}
			checkGrammar(t, f, ExitRefused, false, false, "")
		})
	}
}

func TestAnUpdateAsksForTheConfirmationAndForThePlanThatWasRead(t *testing.T) {
	f := updatable(t)
	if code := f.update(); code != ExitRefused || !strings.Contains(f.out.String(), CodeConfirmationNeeded) {
		t.Fatalf("no terminal: %d\n%s", code, f.out)
	}
	f.isTTY, f.input = true, "n\n"
	if code := f.update(); code != ExitRefused || !strings.Contains(f.out.String(), "update virt "+newTag) || !strings.Contains(f.out.String(), CodeDeclined) {
		t.Fatalf("declined: %d\n%s", code, f.out)
	}
	f.isTTY, f.input = false, ""
	plan := f.planUpdate()
	// the id of an install is not the id of an update
	if code := f.update("--yes", "--plan-id", "m1-"+strings.Repeat("0", 32)); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+CodePlanChanged+"\n") {
		t.Fatalf("a wrong id: %d\n%s", code, f.out)
	}
	if code := f.update("--yes", "--plan-id", plan.PlanID); code != ExitOK {
		t.Fatalf("with the id: %d\n%s", code, f.out)
	}
	f = updatable(t)
	f.isTTY, f.input = true, "y\n"
	if code := f.update(); code != ExitOK {
		t.Fatalf("a terminal that says yes: %d\n%s", code, f.out)
	}
}

// ---- the packages -------------------------------------------------------------------------------------------------------------------------------------------------------------

// withPackage makes the new version name one more package, and apt say it would install it.
func withPackage(f *fixture) {
	m := testManifest()
	m.Packages.AMD64 = append(slices.Clone(m.Packages.AMD64), "swtpm")
	f.publish(newTag, m, v2Entries(f.t, m))
	f.sandbox.sim.Text = aptProgress + "The following NEW packages will be installed:\n  swtpm\n0 upgraded, 1 newly installed, 0 to remove and 0 not upgraded.\nInst swtpm (0.8.0 Debian:12.15/oldstable [amd64])\n"
	f.sandbox.est.Text = aptProgress + "The following NEW packages will be installed:\n  swtpm\n0 upgraded, 1 newly installed, 0 to remove and 0 not upgraded.\nNeed to get 1 MB of archives.\nAfter this operation, 3 MB of additional disk space will be used.\n" +
		"'http://deb.debian.org/debian/pool/main/s/swtpm/swtpm_0.8.0_amd64.deb' swtpm_0.8.0_amd64.deb 1000000 SHA256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n"
}

func TestAnUpdateInstallsOnlyThePackagesTheNewVersionAddsAndNeverRemovesOne(t *testing.T) {
	f := updatable(t)
	withPackage(f)
	plan := f.planUpdate()
	if plan.Refused != "" || !slices.Equal(plan.Update.Added, []string{"swtpm"}) || !slices.Equal(plan.Packages.ToInstall, []string{"swtpm"}) {
		t.Fatalf("%+v %+v", plan, plan.Update)
	}
	if last := f.sandbox.argvs[len(f.sandbox.argvs)-1]; !slices.Contains(last, "swtpm") || slices.Contains(last, "qemu-utils") || slices.Contains(last, "libvirt-clients") {
		t.Errorf("apt was asked %q", last)
	}
	if code := f.update("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	var apt []string
	for _, c := range f.runner.calls {
		if c[0] == "apt-get" && c[1] == "install" {
			apt = append(apt, strings.Join(c, " "))
		}
	}
	if len(apt) != 2 || !strings.HasSuffix(apt[1], " --no-remove -o Dpkg::Use-Pty=0 -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=120 swtpm") {
		t.Errorf("apt-get install: %q", apt)
	}
	// the packages the new version drops are said, as text, and kept
	f = updatable(t)
	m := testManifest()
	m.Packages.Common = nil
	f.publish(newTag, m, v2Entries(t, m))
	plan = f.planUpdate()
	if plan.Refused != "" || !slices.Equal(plan.Update.Dropped, []string{"qemu-utils"}) || len(plan.Update.Added) != 0 {
		t.Errorf("%+v %+v", plan, plan.Update)
	}
	if code := f.update("--yes"); code != ExitOK || f.runner.called("apt-get", "install") != 1 || f.runner.called("apt-get", "remove") != 0 || f.runner.called("apt-get", "purge") != 0 {
		t.Errorf("exit %d, apt: %q", code, f.runner.calls)
	}
}

// ---- what the administrator changed ---------------------------------------------------------------------------------------------------------------------------------------------------

func TestAFileTheAdministratorChangedIsKeptWhenTheNewVersionDoesNotHaveIt(t *testing.T) {
	f := updatable(t)
	f.writeFile(helperPath, "the administrator's helper")
	if code := f.update("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if f.read(helperPath) != "the administrator's helper" {
		t.Error("the helper of the administrator was deleted")
	}
	if !strings.Contains(f.out.String(), "left: "+helperPath+" (changed)\n") {
		t.Errorf("not said:\n%s", f.out)
	}
	if list := f.read("/var/lib/casaos/modules/virt.files"); strings.Contains(list, helperPath) {
		t.Errorf("the helper is still the module's:\n%s", list)
	}
	// and it is not deleted by a remove either
	if code := f.remove("--yes"); code != ExitOK || f.read(helperPath) != "the administrator's helper" {
		t.Errorf("remove: %d", code)
	}
}

func TestAFileTheAdministratorChangedAndTheNewVersionShipsRefusesTheUpdateAndChangesNothing(t *testing.T) {
	f := updatable(t)
	f.writeFile(progPath, "the administrator's program")
	before := f.treeWithoutBookkeeping()
	if code := f.update("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), "failed at step unpack: "+osmodule.FileExists+"\n") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if !f.runner.isRunning(testUnit) || f.runner.called("systemctl", "stop") != 0 {
		t.Error("the unit was stopped")
	}
}

func TestAFileTheAdministratorTookAwayIsMadeAgainOrLeftAlone(t *testing.T) {
	f := updatable(t)
	_ = os.Remove(f.path(progPath))   // the new version has it
	_ = os.Remove(f.path(helperPath)) // the new version does not
	if code := f.update("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if f.read(progPath) != wantAfter[progPath] || f.exists(helperPath) {
		t.Error("the files are not the new version's")
	}
}

// ---- failures ---------------------------------------------------------------------------------------------------------------------------------------------------------------------

// failsAtUpdate runs an update that must fail at a step with a code, and checks that the box is exactly as it was: every file, the unit running, nothing of the update left.
func (f *fixture) failsAtUpdate(step, code, kept string, before map[string]string) {
	f.t.Helper()
	t := f.t
	logBefore, _ := os.ReadFile(f.path("/var/log/casaos/module-virt.log"))
	if exit := f.update("--yes"); exit != ExitFailed {
		t.Fatalf("exit %d:\n%s", exit, f.out)
	}
	if f.cleanup != nil {
		f.cleanup()
	}
	lines := f.lines()
	if len(lines) < 2 || lines[len(lines)-2] != "kept_packages: "+kept || !resultRe.MatchString(lines[len(lines)-1]) {
		t.Errorf("the last lines: %q, want kept_packages: %s and the result", lines[max(0, len(lines)-2):], kept)
	}
	got := markers(t, lines)
	failedAt := slices.Index(got, step+" failed")
	if failedAt < 0 {
		t.Errorf("the markers: %q", got)
	}
	rollback := false
	for _, m := range got[max(0, failedAt+1):] {
		rollback = true
		if !strings.HasPrefix(m, "rollback_") {
			t.Errorf("a marker after the failed step that is not the rollback's: %q", got)
		}
	}
	if stopped := slices.Index(got, "stop begin") >= 0; rollback != stopped {
		t.Errorf("the rollback ran: %v, the unit was stopped: %v (%q)", rollback, stopped, got)
	}
	if !slices.Contains(lines, "failed at step "+step+": "+code) {
		t.Errorf("no 'failed at step %s: %s' in\n%s", step, code, f.out)
	}
	if !regexp.MustCompile(`(?m)^` + resultMarker + ` - virt failed ` + step + ` ` + code + ` rolled_back \S+$`).MatchString(f.out.String()) {
		t.Errorf("no result line for %s %s:\n%s", step, code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if !f.runner.isRunning(testUnit) {
		t.Error("the old unit is not running")
	}
	if entries, _ := os.ReadDir(f.env.Roots.tmp()); len(entries) != 0 {
		t.Errorf("the area of the download is not empty: %v", entries)
	}
	if f.read("/var/lib/casaos/modules/virt.version") != testTag+"\n" {
		t.Error("the version is not the old one")
	}
	_ = logBefore
	checkGrammar(t, f, ExitFailed, false, false, "")
}

// failOnce makes the first command that starts with prefix fail, and the ones after it work.
func failOnce(f *fixture, prefix string) {
	n := 0
	f.runner.onRun = func(_ context.Context, argv []string) {
		if strings.HasPrefix(strings.Join(argv, " "), prefix) {
			n++
			if n == 1 {
				f.runner.fail[prefix] = 1
			} else {
				delete(f.runner.fail, prefix)
			}
		}
	}
}

// failNewFile makes the n-th file that is written on the box by any of the unpack's file systems (the checking writes none, the staging writes four, the swap four, the rollback what it
// puts back) fail with no space left.
func failNewFile(f *fixture, n int) {
	count := 0
	f.host.wrapDest = func(d osmodule.Destination) osmodule.Destination {
		return &nthFailDest{Destination: d, count: &count, failAt: n}
	}
}

// nthFailDest is a file system that cannot write the n-th file asked of any of the file systems that share the count.
type nthFailDest struct {
	osmodule.Destination
	count  *int
	failAt int
}

func (d *nthFailDest) NewFile(p string, perm os.FileMode, replace bool) (osmodule.File, error) {
	if *d.count++; *d.count == d.failAt {
		return nil, fmt.Errorf("no space left on device")
	}
	return d.Destination.NewFile(p, perm, replace)
}

func TestAFailureAtEachStepIsRolledBackAndTheBoxIsAsItWas(t *testing.T) {
	other := testManifest()
	other.Component = "CasaOS-Other"
	traversal := append(v2Entries(t, testManifest()), tent{name: "build/sysroot/usr/bin/../../../etc/cron.d/x", body: "x"})
	cases := []struct {
		name, step, code, kept string
		setup                  func(f *fixture)
	}{
		{"apt fails", StepPackages, CodePackagesFailed, "unknown", func(f *fixture) { withPackage(f); f.runner.fail["apt-get install"] = 100 }},
		{"apt finds a package held", StepPackages, CodeHeldPackage, "unknown", func(f *fixture) {
			withPackage(f)
			f.runner.fail["apt-get install"] = 100
			f.runner.said["apt-get install"] = "E: Held packages were changed and -y was used without --allow-change-held-packages.\n"
		}},
		{"the download fails", StepDownload, CodeDownloadFailed, "none", func(f *fixture) { f.dl.err = errString("connection reset") }},
		{"the archive is not the one of the catalogue", StepVerify, CodeChecksumMismatch, "none", func(f *fixture) { f.dl.body = append([]byte{}, f.dl.body...); f.dl.body[len(f.dl.body)/2] ^= 1 }},
		{"the archive is not a gzip file", StepUnpack, osmodule.UnpackCorrupt, "none", func(f *fixture) { f.setArchive([]byte("this is not an archive")) }},
		{"the archive names a path outside the allowed ones", StepUnpack, osmodule.UnpackPath, "none", func(f *fixture) { f.setEntries(traversal) }},
		{"the archive has another manifest", StepUnpack, CodeManifestMismatch, "none", func(f *fixture) { f.setEntries(v2Entries(f.t, other)) }},
		{"the archive has no manifest that reads", StepUnpack, CodeManifestInvalid, "none", func(f *fixture) {
			f.setEntries(append([]tent{{name: "module.json", body: `{"schema":1}`}}, v2Entries(f.t, testManifest())[1:]...))
		}},
		{"the administrator turned a file of the module into a directory", StepBackup, CodeBackupFailed, "none", func(f *fixture) {
			_ = os.Remove(f.path(helperPath))
			if err := os.Mkdir(f.path(helperPath), 0o755); err != nil {
				f.t.Fatal(err)
			}
			f.cleanup = func() { _ = os.Remove(f.path(helperPath)) }
		}},
		{"the unit cannot be stopped", StepStop, CodeUnitNotStopped, "none", func(f *fixture) { f.runner.fail["systemctl stop"] = 1 }},
		{"the first file cannot be written", StepSwap, CodeSwapFailed, "none", func(f *fixture) { failNewFile(f, 5) }},
		{"the second file cannot be written", StepSwap, CodeSwapFailed, "none", func(f *fixture) { failNewFile(f, 6) }},
		{"the unit file cannot be written", StepSwap, CodeSwapFailed, "none", func(f *fixture) { failNewFile(f, 8) }},
		{"the lists cannot be written", StepSwap, CodeSwapFailed, "none", func(f *fixture) {
			tmp := tempName(f.path("/etc/casaos/modules/virt.json"))
			f.env.StepHook = func(_ context.Context, step, phase string) {
				switch {
				case step == StepSwap && phase == "begin":
					// (a directory where the temporary name of the manifest goes, until the rollback)
					_ = os.MkdirAll(tmp, 0o755)
					_ = os.WriteFile(filepath.Join(tmp, "x"), nil, 0o644)
				case step == "rollback_restore" && phase == "begin":
					_ = os.RemoveAll(tmp)
				}
			}
		}},
		{"systemd cannot read its units", StepUnits, CodeUnitsFailed, "none", func(f *fixture) { failOnce(f, "systemctl daemon-reload") }},
		{"the unit does not start", StepStart, CodeStartFailed, "none", func(f *fixture) { failOnce(f, "systemctl start") }},
		{"the gateway does not answer", StepRoute, CodeRouteTimeout, "none", func(f *fixture) { f.gw.never = true }},
		{"the gateway does not answer, and a package was added", StepRoute, CodeRouteTimeout, "swtpm", func(f *fixture) { withPackage(f); f.gw.never = true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := updatable(t)
			c.setup(f)
			before := f.treeWithoutBookkeeping()
			f.failsAtUpdate(c.step, c.code, c.kept, before)
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestAnUpdateInterruptionAtEachStepIsAFailureThatIsRolledBack(t *testing.T) {
	for _, step := range updateSteps {
		t.Run(step, func(t *testing.T) {
			f := updatable(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.env.StepHook = func(_ context.Context, s, phase string) {
				if s == step && phase == "begin" {
					cancel()
				}
			}
			before := f.treeWithoutBookkeeping()
			if exit := f.main(ctx, "update", testName, "--yes"); exit != ExitFailed {
				t.Fatalf("exit %d:\n%s", exit, f.out)
			}
			if !slices.Contains(f.lines(), "failed at step "+step+": "+CodeInterrupted) {
				t.Errorf("not interrupted at %s:\n%s", step, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			if !f.runner.isRunning(testUnit) {
				t.Error("the old unit is not running")
			}
			if entries, _ := os.ReadDir(f.env.Roots.tmp()); len(entries) != 0 {
				t.Errorf("the area of the download is not empty: %v", entries)
			}
			checkGrammar(t, f, ExitFailed, false, false, "")
		})
	}
}

// A rollback that cannot start the old unit is incomplete: the backup stays, and the next run of the module puts the files back and tries again.
func TestARollbackThatCannotStartTheOldUnitKeepsTheBackup(t *testing.T) {
	f := updatable(t)
	before := f.treeWithoutBookkeeping()
	f.gw.never = true
	failFrom := 0
	f.runner.onRun = func(_ context.Context, argv []string) {
		if strings.HasPrefix(strings.Join(argv, " "), "systemctl start") {
			if failFrom++; failFrom >= 2 { // the start of the new unit works, the rollback's does not
				f.runner.fail["systemctl start"] = 1
			}
		}
	}
	if code := f.update("--yes"); code != ExitFailed {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	out := f.out.String()
	if !strings.Contains(out, "rollback incomplete") || !regexp.MustCompile(`(?m)^`+resultMarker+` - virt failed route route_timeout rollback_incomplete \S+$`).MatchString(out) {
		t.Fatalf("not said:\n%s", out)
	}
	if !f.exists("/var/lib/casaos/modules/tmp/backup-virt/COMPLETE") || !strings.Contains(out, "/var/lib/casaos/modules/tmp/backup-virt") {
		t.Errorf("the backup is not kept, or the log does not say where:\n%s", out)
	}
	checkGrammar(t, f, ExitFailed, false, false, "")
	// the files are the old version's all the same; the next run finishes the work
	for p, content := range map[string]string{progPath: "#!/bin/sh\nexit 0\n", unitPath: unitV1, helperPath: "helper v1\n"} {
		if f.read(p) != content {
			t.Errorf("%s holds %q", p, f.read(p))
		}
	}
	delete(f.runner.fail, "systemctl start")
	f.runner.onRun = nil
	f.gw.never = false
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("remove: %d\n%s", code, f.out)
	}
	if !strings.Contains(f.out.String(), "recovered: virt is as it was before the update") {
		t.Errorf("the recovery is not said:\n%s", f.out)
	}
	_ = before
	if f.exists("/var/lib/casaos/modules/tmp/backup-virt") || f.exists(progPath) || f.exists(helperPath) {
		t.Error("the remove left files")
	}
}

// ---- a run that is killed ------------------------------------------------------------------------------------------------------------------------------------------------------

// killed runs an update that ends where `at` says without running anything that is deferred in the run's own code that would a kill not run (a goroutine that Goexits: its deferred calls
// run, which release the lock as the death of a process does, and the run's recover finds nothing). at is a step ("swap"), "swap-1" or "swap-2" (after that many files of the swap are in
// place) or a step of the rollback.
func (f *fixture) killed(at string, args ...string) {
	f.t.Helper()
	done := make(chan struct{})
	files := 0
	hook := func(_ context.Context, s, phase string) {
		if s == at && phase == "begin" {
			runtime.Goexit()
		}
	}
	if strings.HasPrefix(at, "swap-") {
		after := int(at[len(at)-1] - '0')
		hook = nil
		f.host.wrapDest = func(d osmodule.Destination) osmodule.Destination {
			return &hookedDest{Destination: d, onFile: func() {
				// (the staging writes the first four)
				if files++; files > 4+after {
					runtime.Goexit()
				}
			}}
		}
	}
	f.env.StepHook = hook
	go func() {
		defer close(done)
		f.update(append([]string{"--yes"}, args...)...)
	}()
	<-done
	f.env.StepHook = nil
	f.host.wrapDest = nil
}

var killPointsOfUpdate = append(slices.Clone(updateSteps), "swap-1", "swap-2", "swap-3")

func TestAKillAtEachPointLeavesAStateThatTheNextUpdateOrRemoveFinishes(t *testing.T) {
	// what an update leaves when nothing goes wrong
	clean := updatable(t)
	if code := clean.update("--yes"); code != ExitOK {
		t.Fatalf("clean update: %d\n%s", code, clean.out)
	}
	wantUpdated := clean.treeWithoutBookkeeping()
	for _, at := range killPointsOfUpdate {
		t.Run("update after a kill at "+at, func(t *testing.T) {
			f := updatable(t)
			f.envVars["CASAOS_MODULE_NONCE"] = strings.Repeat("ab12", 8)
			f.killed(at)
			if raw, _ := os.ReadFile(f.path("/var/log/casaos/module-virt.log")); regexp.MustCompile(`(?m)^` + resultMarker).Match(raw) {
				t.Fatalf("a killed run has a result:\n%s", raw)
			}
			// the job reads as interrupted, never as succeeded, once its unit is gone
			job, err := ReadJob(f.env, testName, UnitGone, f.clock.Now().Add(time.Hour))
			if err != nil || job.State != JobInterrupted {
				t.Errorf("the job: %+v, %v", job, err)
			}
			if code := f.update("--yes"); code != ExitOK {
				t.Fatalf("update again: %d\n%s", code, f.out)
			}
			sameTree(t, wantUpdated, f.treeWithoutBookkeeping())
			if !f.runner.isRunning(testUnit) {
				t.Error("the unit is not running")
			}
			if entries, _ := os.ReadDir(f.env.Roots.tmp()); len(entries) != 0 {
				t.Errorf("the area of the download is not empty: %v", entries)
			}
			checkGrammar(t, f, ExitOK, false, false, "")
		})
		t.Run("remove after a kill at "+at, func(t *testing.T) {
			f := updatable(t)
			bare := newFixture(t)
			wantNothing := bare.treeWithoutBookkeeping()
			f.killed(at)
			if code := f.remove("--yes"); code != ExitOK {
				t.Fatalf("remove: %d\n%s", code, f.out)
			}
			after := f.treeWithoutBookkeeping()
			for p := range after {
				if _, ok := wantNothing[p]; !ok && !strings.HasPrefix(p, "/stub") {
					t.Errorf("left after the remove: %s", p)
				}
			}
			if f.runner.isEnabled(testUnit) {
				t.Error("the unit is still enabled")
			}
		})
	}
}

// The recovery is itself a run that can be ended: the backup is the one copy of the old files until it is deleted, and what is done from it can be done again.
func TestARecoveryThatIsKilledIsDoneAgain(t *testing.T) {
	clean := updatable(t)
	clean.update("--yes")
	wantUpdated := clean.treeWithoutBookkeeping()
	for _, at := range []string{"recover_restore", "recover_units", "recover_start"} {
		t.Run(at, func(t *testing.T) {
			f := updatable(t)
			f.killed("swap-2")
			f.killed(at)
			if !f.exists("/var/lib/casaos/modules/tmp/backup-virt/COMPLETE") {
				t.Fatal("the backup is gone")
			}
			if code := f.update("--yes"); code != ExitOK {
				t.Fatalf("update: %d\n%s", code, f.out)
			}
			sameTree(t, wantUpdated, f.treeWithoutBookkeeping())
		})
	}
}

// A rollback that is killed in the middle is a run that was ended: the next run puts the rest back.
func TestARollbackThatIsKilledIsFinishedByTheNextRun(t *testing.T) {
	for _, at := range []string{"rollback_restore", "rollback_units", "rollback_start"} {
		t.Run(at, func(t *testing.T) {
			f := updatable(t)
			f.gw.never = true
			f.killed(at)
			if raw, _ := os.ReadFile(f.path("/var/log/casaos/module-virt.log")); regexp.MustCompile(`(?m)^` + resultMarker).Match(raw) {
				t.Fatalf("a killed rollback has a result:\n%s", raw)
			}
			f.gw.never = false
			// a remove puts the old files back and then takes the module away; an update puts them back and goes on
			if code := f.update("--yes"); code != ExitOK {
				t.Fatalf("update: %d\n%s", code, f.out)
			}
			if f.read(progPath) != wantAfter[progPath] {
				t.Error("the update was not made")
			}
		})
	}
}

// A stale backup is put back by an install too, which then finds the module installed.
func TestAnInstallFindingABackupPutsItBackFirst(t *testing.T) {
	f := updatable(t)
	f.killed("swap-2")
	if code := f.install("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "recovered: virt") {
		t.Fatalf("install: %d\n%s", code, f.out)
	}
	if f.read(progPath) != "#!/bin/sh\nexit 0\n" || f.exists("/var/lib/casaos/modules/tmp/backup-virt") {
		t.Error("the old version was not put back")
	}
}

// A backup that was not finished is a backup of a box that had not been changed: it is dropped.
func TestABackupThatWasNotFinishedIsDroppedAndNothingIsPutBack(t *testing.T) {
	f := updatable(t)
	f.writeFile("/var/lib/casaos/modules/tmp/backup-virt/old.files", "half")
	if code := f.update("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if !strings.Contains(f.out.String(), "was not finished, so nothing of the box had been changed") {
		t.Errorf("not said:\n%s", f.out)
	}
	if f.read(progPath) != wantAfter[progPath] || f.exists("/var/lib/casaos/modules/tmp/backup-virt") {
		t.Error("the update was not made, or the backup is left")
	}
}

// A backup that cannot be trusted is kept, and the run that found it says so and changes nothing.
func TestABackupThatCannotBeReadIsKeptAndTheRunStops(t *testing.T) {
	f := updatable(t)
	f.killed("stop")
	f.writeFile("/var/lib/casaos/modules/tmp/backup-virt/old.version", "not a tag")
	before := f.treeWithoutBookkeeping()
	if code := f.update("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), "cannot be read") || !strings.Contains(f.out.String(), " virt failed - "+CodeRecoveryIncomplete+" rollback_incomplete ") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if code := f.remove("--yes"); code != ExitFailed {
		t.Errorf("remove: %d\n%s", code, f.out)
	}
}

// The backup of one module is not emptied by a run of another.
func TestTheBackupOfAnUpdateIsNotEmptiedByTheNextRunOfTheAreaOfTheDownload(t *testing.T) {
	f := updatable(t)
	f.killed("swap-1")
	f.writeFile("/var/lib/casaos/modules/tmp/stage/x", "x")
	if err := emptyTmp(f.env.Host, f.env.Roots.tmp()); err != nil {
		t.Fatal(err)
	}
	if !f.exists("/var/lib/casaos/modules/tmp/backup-virt/COMPLETE") || f.exists("/var/lib/casaos/modules/tmp/stage/x") {
		t.Error("the backup was emptied, or the staging was not")
	}
	// what has the name of a backup and is not a directory, or is a directory that is not the name of one, goes
	f.writeFile("/var/lib/casaos/modules/tmp/backup-other", "a file")
	f.writeFile("/var/lib/casaos/modules/tmp/backup-Bad/x", "x") // (a folder, but not the name of a backup)
	if err := emptyTmp(f.env.Host, f.env.Roots.tmp()); err != nil {
		t.Fatal(err)
	}
	if f.exists("/var/lib/casaos/modules/tmp/backup-other") || f.exists("/var/lib/casaos/modules/tmp/backup-Bad") {
		t.Error("what is not a backup stayed")
	}
}

// ---- directories ---------------------------------------------------------------------------------------------------------------------------------------------------------------------

// An update that needs a directory the box does not have makes it and lists it, and a rollback takes it away.
func TestAnUpdateMakesTheDirectoriesTheNewVersionNeedsAndARollbackTakesThemAway(t *testing.T) {
	v1 := slices.DeleteFunc(slices.Clone(v1Entries(t)), func(e tent) bool { return strings.Contains(e.name, "/system/") })
	for _, fail := range []bool{false, true} {
		f := newFixture(t, withEntries(v1))
		if code := f.install("--yes"); code != ExitOK {
			t.Fatalf("install: %d\n%s", code, f.out)
		}
		if f.exists("/usr/lib/systemd") {
			t.Fatal("the box has the directory already")
		}
		f.publish(newTag, testManifest(), v2Entries(t, testManifest()))
		before := f.treeWithoutBookkeeping()
		if fail {
			f.gw.never = true
		}
		code := f.update("--yes")
		if fail {
			if code != ExitFailed || f.exists("/usr/lib") {
				t.Errorf("exit %d, the directories of the new version are left: %v", code, f.exists("/usr/lib"))
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			continue
		}
		if code != ExitOK {
			t.Fatalf("exit %d:\n%s", code, f.out)
		}
		dirs, err := osmodule.ParseDirs([]byte(f.read("/var/lib/casaos/modules/virt.dirs")))
		if err != nil || !slices.Equal(sorted(dirs), sorted(madeDirs)) {
			t.Errorf("the list of directories: %v, %v", dirs, err)
		}
		// a remove takes them away again
		if code := f.remove("--yes"); code != ExitOK || f.exists("/usr/lib/systemd") {
			t.Errorf("remove: %d, directories left: %v", code, f.exists("/usr/lib/systemd"))
		}
	}
}

// ---- the lock and the kind of the job ---------------------------------------------------------------------------------------------------------------------------------------

func TestAnUpdateWhileAnotherRunsIsRefusedBusy(t *testing.T) {
	f := updatable(t)
	release, err := f.host.Host.Lock(f.env.Roots.lock())
	if err != nil {
		t.Fatal(err)
	}
	before := f.treeWithoutBookkeeping()
	if code := f.update("--yes"); code != ExitRefused || !strings.HasPrefix(f.out.String(), "refused: busy\n") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	release()
	sameTree(t, before, f.treeWithoutBookkeeping())
}

func TestTheLogOfAnUpdateKeepsTheQueuedLineOfTheCore(t *testing.T) {
	f := updatable(t)
	nonce := strings.Repeat("ab12", 8)
	f.envVars["CASAOS_MODULE_NONCE"] = nonce
	if err := Queue(f.env, testName, nonce, KindUpdate, f.clock.now); err != nil {
		t.Fatal(err)
	}
	if code := f.update("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	log := f.read("/var/log/casaos/module-virt.log")
	if !strings.HasPrefix(log, QueuedLine(testName, nonce, KindUpdate, f.clock.now)) {
		t.Errorf("the log does not begin with the queued line:\n%s", log)
	}
	job, err := ReadJob(f.env, testName, UnitGone, f.clock.Now())
	if err != nil || job.Kind != KindUpdate || job.State != JobSucceeded || job.Step != StepDone {
		t.Errorf("%+v, %v", job, err)
	}
	// the queued line of an install is not the line of an update: it is replaced
	f.publish("v0.1.2", testManifest(), v2Entries(t, testManifest()))
	if err := Queue(f.env, testName, nonce, KindInstall, f.clock.now); err != nil {
		t.Fatal(err)
	}
	if code := f.update("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if job, _ := ReadJob(f.env, testName, UnitGone, f.clock.Now()); job.Kind != "" {
		t.Errorf("the kind: %q", job.Kind)
	}
}

// ---- the swap and the rollback, where they meet the administrator --------------------------------------------------------------------------------------------------------------------

// An interruption in the middle of the swap stops it at the file it is at: the files after it are not written (the rollback has the one it wrote to put back).
func TestAnInterruptionInTheMiddleOfTheSwapGoesNoFurther(t *testing.T) {
	f := updatable(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	files := 0
	f.host.wrapDest = func(d osmodule.Destination) osmodule.Destination {
		return &hookedDest{Destination: d, onFile: func() {
			// (the staging writes four files; the fifth is the first of the swap)
			if files++; files == 5 {
				cancel()
			}
		}}
	}
	before := f.treeWithoutBookkeeping()
	if exit := f.main(ctx, "update", testName, "--yes"); exit != ExitFailed || !slices.Contains(f.lines(), "failed at step swap: "+CodeInterrupted) {
		t.Fatalf("exit %d:\n%s", exit, f.out)
	}
	if files != 6 {
		t.Errorf("%d files were written: four by the staging, one by the swap and one put back", files)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
}

// A copy in the backup that is not what was saved is never put back, and the recovery says so and keeps the backup.
func TestACopyThatIsNotWhatWasSavedIsNotPutBack(t *testing.T) {
	f := updatable(t)
	f.killed("swap-2")
	f.writeFile("/var/lib/casaos/modules/tmp/backup-virt/copies/0", "a copy that was changed")
	if code := f.update("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), "not put back: "+progPath) ||
		!strings.Contains(f.out.String(), " virt failed recover_restore "+CodeRecoveryIncomplete+" rollback_incomplete ") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if f.read(progPath) != wantAfter[progPath] {
		t.Errorf("the program is %q", f.read(progPath))
	}
	if !f.exists("/var/lib/casaos/modules/tmp/backup-virt/COMPLETE") {
		t.Error("the backup was not kept")
	}
	checkGrammar(t, f, ExitFailed, false, false, "")
}

// What somebody put at a path of the module while the update ran is theirs: the rollback leaves it, and says so, and a path of the old version that is neither the old file nor the new one makes
// the rollback incomplete.
func TestARollbackLeavesWhatSomebodyPutAtAPathOfTheModule(t *testing.T) {
	f := updatable(t)
	f.gw.never = true
	f.env.StepHook = func(_ context.Context, step, phase string) {
		if step == "rollback_restore" && phase == "begin" {
			f.writeFile(toolPath, "somebody's tool")
			f.writeFile(progPath, "somebody's program")
		}
	}
	if code := f.update("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), " virt failed route route_timeout rollback_incomplete ") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	out := f.out.String()
	if !strings.Contains(out, "left: "+toolPath+" (changed)\n") || !strings.Contains(out, "left: "+progPath+" (it is neither the old file nor the new one)\n") {
		t.Errorf("not said:\n%s", out)
	}
	if f.read(toolPath) != "somebody's tool" || f.read(progPath) != "somebody's program" {
		t.Error("what was put there was replaced or deleted")
	}
	if !f.exists("/var/lib/casaos/modules/tmp/backup-virt/COMPLETE") {
		t.Error("the backup was not kept")
	}
	checkGrammar(t, f, ExitFailed, false, false, "")
}

// A rollback that cannot stop the unit it puts the files under is not complete when it had files to put back: the old version is there, and the unit that runs may not be the old one.
func TestARollbackThatCannotStopTheUnitWhileItPutsFilesBackIsIncomplete(t *testing.T) {
	f := updatable(t)
	failNewFile(f, 6) // the swap writes one file and cannot write the next
	stops := 0
	f.runner.onRun = func(_ context.Context, argv []string) {
		if strings.HasPrefix(strings.Join(argv, " "), "systemctl stop") {
			if stops++; stops == 2 { // the stop of the update works, the one of the rollback does not
				f.runner.fail["systemctl stop"] = 1
				delete(f.runner.stopped, testUnit) // (something started it again)
			}
		}
	}
	if code := f.update("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), " virt failed swap swap_failed rollback_incomplete ") ||
		!strings.Contains(f.out.String(), "a unit could not be stopped while its files were put back") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if !f.exists("/var/lib/casaos/modules/tmp/backup-virt/COMPLETE") || f.read(progPath) != "#!/bin/sh\nexit 0\n" {
		t.Error("the backup was not kept, or the files were not put back")
	}
	checkGrammar(t, f, ExitFailed, false, false, "")
}

// What the plan says the files to keep weigh is the files of the list as they are on the box.
func TestTheBoxSaysWhatABackupOfTheFilesOfTheModuleWeighs(t *testing.T) {
	f := updatable(t)
	bx := &box{ctx: context.Background(), env: &f.env}
	var want int64
	for _, p := range []string{progPath, samplePath, helperPath, unitPath} {
		want += int64(len(f.read(p)))
	}
	if st := bx.Installed(testName); !st.BytesOK || st.Bytes != want || st.Files != osmodule.FilesVerified || st.Tag != testTag || !st.Present {
		t.Errorf("%+v, want %d bytes", st, want)
	}
	// a file that is not there weighs nothing; one that cannot be looked at is a weight that cannot be told
	_ = os.Remove(f.path(helperPath))
	if st := bx.Installed(testName); !st.BytesOK || st.Bytes != want-int64(len("helper v1\n")) {
		t.Errorf("%+v", st)
	}
	if st := bx.Installed("nothing"); st.Present || st.Broken || st.Unreadable || st.BytesOK {
		t.Errorf("a module that is not there: %+v", st)
	}
}

// What a recovery puts back is the old version, whole: a run that finds a backup and does nothing else (an install, which then finds the module installed) leaves the box as it was
// before the update, whatever step the update was killed in.
func TestARecoveryPutsTheOldVersionBackWhateverTheStepTheUpdateWasKilledIn(t *testing.T) {
	for _, at := range killPointsOfUpdate {
		t.Run(at, func(t *testing.T) {
			f := updatable(t)
			before := f.treeWithoutBookkeeping()
			f.killed(at)
			if code := f.install("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+osmodule.CheckInstalled+"\n") {
				t.Fatalf("install: %d\n%s", code, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			if !f.runner.isRunning(testUnit) {
				t.Error("the old unit is not running")
			}
			if f.exists("/var/lib/casaos/modules/tmp/backup-virt") {
				t.Error("the backup is left")
			}
		})
	}
}

// A file that is the same in both versions is still there once the update is over: the files of the old version that the new one does not have are the ones that are taken away.
func TestAFileThatDidNotChangeBetweenTheVersionsIsStillThere(t *testing.T) {
	f := newFixture(t, withEntries(v1Entries(t)))
	if code := f.install("--yes"); code != ExitOK {
		t.Fatal(f.out.String())
	}
	next := v2Entries(t, testManifest())
	for i := range next {
		if next[i].name == "build/sysroot"+progPath {
			next[i].body = "#!/bin/sh\nexit 0\n" // the program of v0.1.0
		}
	}
	f.publish(newTag, testManifest(), next)
	if code := f.update("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if f.read(progPath) != "#!/bin/sh\nexit 0\n" || !strings.Contains(f.read("/var/lib/casaos/modules/virt.files"), progPath) {
		t.Error("the program that did not change is gone or is not the module's")
	}
	if strings.Contains(f.out.String(), "left:") {
		t.Errorf("something was left:\n%s", f.out)
	}
}

// A directory the new version needs that somebody made since the plan is fine; one that is a file is in the way, and the update is rolled back.
func TestADirectoryThatAppearsBeforeTheSwapIsUsedOrRefused(t *testing.T) {
	v1 := slices.DeleteFunc(slices.Clone(v1Entries(t)), func(e tent) bool { return strings.Contains(e.name, "/system/") })
	for _, asFile := range []bool{false, true} {
		f := newFixture(t, withEntries(v1))
		if code := f.install("--yes"); code != ExitOK {
			t.Fatal(f.out.String())
		}
		f.publish(newTag, testManifest(), v2Entries(t, testManifest()))
		before := f.treeWithoutBookkeeping()
		f.env.StepHook = func(_ context.Context, step, phase string) {
			if step == StepStop && phase == "done" {
				if asFile {
					f.writeFile("/usr/lib", "a file")
				} else if err := os.MkdirAll(f.path("/usr/lib"), 0o755); err != nil {
					t.Error(err)
				}
			}
		}
		code := f.update("--yes")
		if !asFile {
			if code != ExitOK || f.read(unitPath) != unitV2 {
				t.Errorf("a directory that appeared: %d\n%s", code, f.out)
			}
			continue
		}
		if code != ExitFailed || !slices.Contains(f.lines(), "failed at step swap: "+osmodule.UnpackPath) {
			t.Errorf("a file where the directory goes: %d\n%s", code, f.out)
		}
		delete(before, "/usr/lib") // (what somebody made is theirs: it stays)
		after := f.treeWithoutBookkeeping()
		if after["/usr/lib"] == "" {
			t.Error("the file that somebody made was taken away")
		}
		delete(after, "/usr/lib")
		sameTree(t, before, after)
	}
}

// A list in the backup that has no record of what each file held (the earlier format) is not one to put files back by.
func TestABackupWhoseListHoldsNoRecordIsKept(t *testing.T) {
	f := updatable(t)
	f.killed("swap-1")
	f.writeFile("/var/lib/casaos/modules/tmp/backup-virt/old.files.actual", progPath+"\n")
	if code := f.update("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), "cannot be read") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if f.read(progPath) != wantAfter[progPath] || !f.exists("/var/lib/casaos/modules/tmp/backup-virt/COMPLETE") {
		t.Error("something was put back, or the backup was not kept")
	}
}
