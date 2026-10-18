package modulecmd

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

var stepRe = regexp.MustCompile(`^CASAOS_MODULE_STEP (-|[0-9a-f]{32}) ` + testName + ` ([a-z]+) (begin|done|failed) \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`)

// markers are the steps of the output, as "step phase", in order; a line that starts like a marker and is not one is a failure of the test.
func markers(t *testing.T, lines []string) []string {
	t.Helper()
	var out []string
	for _, l := range lines {
		if !strings.HasPrefix(l, stepMarker) {
			continue
		}
		m := stepRe.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("a marker that is not one: %q", l)
		}
		out = append(out, m[2]+" "+m[3])
	}
	return out
}

func (f *fixture) install(args ...string) int {
	f.t.Helper()
	return f.main(context.Background(), append([]string{"install", testName}, args...)...)
}

var allSteps = []string{StepPackages, StepDownload, StepVerify, StepUnpack, StepUnits, StepManifest, StepStart, StepRoute}

var wantFiles = []string{
	"/etc/casaos/virt-management.conf.sample",
	"/usr/bin/casaos-virt-management",
	"/usr/lib/systemd/system/" + testUnit,
}

// madeDirs are the directories the install makes on the box of the tests, which has no /usr/lib/systemd/system: the deepest first, as the unpack gives them.
var madeDirs = []string{"/usr/lib/systemd/system", "/usr/lib/systemd", "/usr/lib"}

func TestAnInstallDoesTheStepsInOrderAndLeavesTheModuleOnTheBox(t *testing.T) {
	f := newFixture(t)
	before := f.treeWithoutBookkeeping()
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s%s", code, f.out, f.errOut)
	}
	var want []string
	for _, s := range allSteps {
		want = append(want, s+" begin", s+" done")
	}
	want = append(want, "done done")
	if got := markers(t, f.lines()); !slices.Equal(got, want) {
		t.Errorf("markers:\n got %q\nwant %q", got, want)
	}
	// the files of the archive, and nothing else of the box changed but the module's bookkeeping
	after := f.treeWithoutBookkeeping()
	for _, p := range wantFiles {
		if _, ok := after[p]; !ok {
			t.Errorf("%s is not there", p)
		}
	}
	for p := range after {
		if _, was := before[p]; !was && !slices.Contains(wantFiles, p) && p != "/etc/casaos/modules/virt.json" && p != "/var/lib/casaos/modules/virt.files" && p != "/var/lib/casaos/modules/virt.dirs" && p != "/var/lib/casaos/modules/virt.version" {
			t.Errorf("a file the module does not own appeared: %s", p)
		}
	}
	list, err := osmodule.ParseFiles([]byte(f.read("/var/lib/casaos/modules/virt.files")))
	if err != nil || !slices.Equal(sorted(list), wantFiles) {
		t.Errorf("the list of files: %v, %v", list, err)
	}
	dirs, err := osmodule.ParseDirs([]byte(f.read("/var/lib/casaos/modules/virt.dirs")))
	if err != nil || !slices.Equal(sorted(dirs), sorted(madeDirs)) {
		t.Errorf("the list of directories: %v, %v", dirs, err)
	}
	if m := f.read("/etc/casaos/modules/virt.json"); m != manifestJSON(t, testManifest()) {
		t.Errorf("the manifest: %s", m)
	}
	if v := f.read("/var/lib/casaos/modules/virt.version"); v != testTag+"\n" {
		t.Errorf("the version: %q", v)
	}
	// the unit is last of the files written
	if got := f.env.Roots.Slash(f.path("/usr/lib/systemd/system/" + testUnit)); got == "" || list[len(list)-1] != "/usr/lib/systemd/system/"+testUnit {
		t.Errorf("the unit is not the last of the list: %v", list)
	}
	// the commands, in order
	var cmds []string
	for _, c := range f.runner.calls {
		if c[0] == "apt-get" || c[1] == "daemon-reload" || c[1] == "enable" {
			cmds = append(cmds, strings.Join(c, " "))
		}
	}
	wantCmds := []string{
		"apt-get install -y -q --no-install-recommends --no-remove -o Dpkg::Use-Pty=0 -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=120 libvirt-clients qemu-utils",
		"systemctl daemon-reload", "systemctl enable --now " + testUnit,
	}
	if !slices.Equal(cmds, wantCmds) {
		t.Errorf("commands:\n got %q\nwant %q", cmds, wantCmds)
	}
	if !f.runner.isEnabled(testUnit) {
		t.Error("the unit is not enabled")
	}
	if f.dl.calls != 1 || f.dl.urls[0] != f.entry.Assets.AMD64.URL {
		t.Errorf("downloads: %d %q", f.dl.calls, f.dl.urls)
	}
	if f.gw.calls != 1 || f.gw.routes[0] != testRoute {
		t.Errorf("the gateway: %d %q", f.gw.calls, f.gw.routes)
	}
	// the area of the download is empty
	if entries, _ := os.ReadDir(f.env.Roots.tmp()); len(entries) != 0 {
		t.Errorf("the area of the download is not empty: %v", entries)
	}
	// the log has the lines of the output, and the output of the commands behind "| "
	log := f.read("/var/log/casaos/module-virt.log")
	for _, l := range f.lines() {
		if !strings.Contains(log, l+"\n") {
			t.Errorf("the log lacks %q", l)
		}
	}
	if last := f.lines()[len(f.lines())-1]; !strings.HasPrefix(last, resultMarker+" - virt ok done - ") {
		t.Errorf("the last line: %q", last)
	}
}

func sorted(in []string) []string { s := slices.Clone(in); slices.Sort(s); return s }

func TestTheNonceOfTheCoreIsInEveryMarker(t *testing.T) {
	nonce := strings.Repeat("ab12", 8)
	for want, given := range map[string]string{nonce: nonce, "-": "", "- ": "NOTHEX", "-  ": strings.ToUpper(nonce), "-   ": nonce + "0"} {
		f := newFixture(t)
		f.envVars["CASAOS_MODULE_NONCE"] = given
		if code := f.install("--yes"); code != ExitOK {
			t.Fatalf("exit %d", code)
		}
		want = strings.TrimSpace(want)
		for _, l := range f.lines() {
			if (strings.HasPrefix(l, stepMarker) || strings.HasPrefix(l, resultMarker)) && strings.Fields(l)[1] != want {
				t.Errorf("nonce %q given: %q", given, l)
			}
		}
	}
}

func TestAnInstallThatIsRefusedChangesNothingAndSaysWhy(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture)
		args  []string
		code  string
	}{
		{"not root", func(f *fixture) { f.env.EUID = func() int { return 1000 } }, []string{"--yes"}, CodeNotRoot},
		{"no terminal and no --yes", func(f *fixture) {}, nil, CodeConfirmationNeeded},
		{"a terminal that says no", func(f *fixture) { f.isTTY, f.input = true, "n\n" }, nil, CodeDeclined},
		{"a terminal that says nothing", func(f *fixture) { f.isTTY, f.input = true, "" }, nil, CodeDeclined},
		{"the plan has changed", func(f *fixture) {}, []string{"--yes", "--plan-id", "m1-" + strings.Repeat("0", 32)}, CodePlanChanged},
		{"another system", func(f *fixture) { f.writeFile("/etc/os-release", "ID=debian\nVERSION_ID=\"11\"\n") }, []string{"--yes"}, osmodule.CheckOS},
		{"a ReCasaOS that is too old", func(f *fixture) { f.env.Version = "0.5.22" }, []string{"--yes"}, osmodule.CheckReCasaOS},
		{"a package manager at work", func(f *fixture) { f.runner.busyUnit = "casaos-package-update.service" }, []string{"--yes"}, osmodule.CheckBusy},
		{"dpkg's lock is held", func(f *fixture) { f.host.dpkg = func([]string) (bool, error) { return true, nil } }, []string{"--yes"}, osmodule.CheckBusy},
		{"dpkg's lock cannot be looked at", func(f *fixture) { f.host.dpkg = func([]string) (bool, error) { return false, errors.New("no") } }, []string{"--yes"}, osmodule.CheckBusy},
		{"an upgrade of the system is open", func(f *fixture) { f.writeFile(pathOSBusy, "{}") }, []string{"--yes"}, osmodule.CheckBusy},
		{"no room", func(f *fixture) { f.host.free = func(string) (int64, error) { return 1 << 20, nil } }, []string{"--yes"}, osmodule.RefusedDiskShort},
		{"the room cannot be told", func(f *fixture) { f.host.free = func(string) (int64, error) { return 0, errors.New("no") } }, []string{"--yes"}, osmodule.RefusedDiskUnknown},
		{"a unit of the name that is not the module's", func(f *fixture) { f.writeFile("/etc/systemd/system/"+testUnit, "[Unit]\n") }, []string{"--yes"}, osmodule.CheckForeignUnit},
		{"a unit of the name in the directory of the system", func(f *fixture) { f.writeFile("/usr/lib/systemd/system/"+testUnit, "[Unit]\n") }, []string{"--yes"}, osmodule.CheckForeignUnit},
		{"no apt-get", func(f *fixture) { _ = os.Remove(f.path("/usr/bin/apt-get")) }, []string{"--yes"}, osmodule.RefusedAptMissing},
		{"a held package", func(f *fixture) { f.sandbox.sim.Text = aptHeld; f.sandbox.sim.ExitCode = 100 }, []string{"--yes"}, osmodule.RefusedHeld},
		{"apt that does not answer", func(f *fixture) { f.sandbox.err = errors.New("killed") }, []string{"--yes"}, osmodule.RefusedAptFailed},
		{"no catalogue", func(f *fixture) { _ = os.Remove(f.path(pathCatalogue)) }, []string{"--yes"}, osmodule.CheckCatalogue},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(f)
			before := f.treeWithoutBookkeeping()
			if code := f.install(c.args...); code != ExitRefused {
				t.Fatalf("exit %d:\n%s", code, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			if f.runner.called("apt-get", "install") != 0 || f.runner.called("systemctl", "enable") != 0 || f.dl.calls != 0 {
				t.Errorf("something was run: %q downloads %d", f.runner.calls, f.dl.calls)
			}
			out := f.out.String()
			if !strings.Contains(out, "refused: "+c.code+"\n") {
				t.Errorf("the output does not say %q:\n%s", c.code, out)
			}
			if c.code != CodeNotRoot && !strings.Contains(out, resultMarker+" - virt refused - "+c.code+" ") {
				t.Errorf("no result line for %q:\n%s", c.code, out)
			}
			if got := markers(t, f.lines()); len(got) != 0 {
				t.Errorf("a refused run has steps: %q", got)
			}
		})
	}
}

func TestATerminalThatSaysYesGoesOn(t *testing.T) {
	f := newFixture(t)
	f.isTTY, f.input = true, "y\n"
	if code := f.install(); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if !strings.Contains(f.out.String(), "libvirt-clients") || !strings.Contains(f.out.String(), "Go on? [y/N]") {
		t.Errorf("the plan was not shown:\n%s", f.out)
	}
}

func TestThePlanIdOfThePlanIsTheOneThatInstalls(t *testing.T) {
	f := newFixture(t)
	if code := f.main(context.Background(), "plan", testName); code != ExitOK {
		t.Fatalf("plan: %d", code)
	}
	id := regexp.MustCompile(`"plan_id":"(m1-[0-9a-f]{32})"`).FindStringSubmatch(f.out.String())
	if id == nil {
		t.Fatalf("no plan id: %s", f.out)
	}
	if code := f.install("--plan-id", id[1]); code != ExitRefused || !strings.Contains(f.out.String(), CodeConfirmationNeeded) {
		t.Fatalf("with the id and no --yes: %d\n%s", code, f.out)
	}
	if code := f.install("--yes", "--plan-id", id[1]); code != ExitOK {
		t.Fatalf("with the id: %d\n%s", code, f.out)
	}
}

func TestASecondInstallIsRefusedAndChangesNothing(t *testing.T) {
	f := newFixture(t)
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("first: %d", code)
	}
	before := f.treeWithoutBookkeeping()
	calls := len(f.runner.calls)
	if code := f.install("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+osmodule.CheckInstalled+"\n") {
		t.Fatalf("second: %d\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if f.runner.called("apt-get", "install") != 1 {
		t.Errorf("apt ran again: %q", f.runner.calls[calls:])
	}
}

func TestAnInstallWhileAnotherRunsIsRefusedBusyAndLeavesTheLogOfTheOtherAlone(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.env.Roots.state(), 0o755); err != nil {
		t.Fatal(err)
	}
	release, err := f.host.Host.Lock(f.env.Roots.lock())
	if err != nil {
		t.Fatal(err)
	}
	f.writeFile("/var/log/casaos/module-virt.log", "the log of the job that runs\n")
	if code := f.install("--yes"); code != ExitRefused || f.out.String() != "refused: busy\n" {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if f.read("/var/log/casaos/module-virt.log") != "the log of the job that runs\n" {
		t.Error("the log of the job that runs was touched")
	}
	release()
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("once the lock is free: %d\n%s", code, f.out)
	}
}

// ---- failures ----------------------------------------------------------------------------------------------------------------------------------------------------------------

// setArchive makes the catalogue name an archive and the downloader serve it.
func (f *fixture) setArchive(raw []byte) {
	f.t.Helper()
	f.archive = raw
	f.entry.Assets.AMD64.SHA256 = sum(raw)
	f.writeCatalogue(osmodule.Catalogue{Schema: 1, Modules: []osmodule.Entry{f.entry}})
	f.dl.body = raw
}

func (f *fixture) setEntries(ents []tent) { f.setArchive(gz(f.t, tarBytes(f.t, ents))) }

// failsAt runs an install that must fail at a step with a code, and checks that it was rolled back: the box is as it was, the unit is stopped, nothing of the module's bookkeeping is left.
func (f *fixture) failsAt(step, code, kept string, before map[string]string) {
	f.t.Helper()
	t := f.t
	if exit := f.install("--yes"); exit != ExitFailed {
		t.Fatalf("exit %d:\n%s", exit, f.out)
	}
	if f.cleanup != nil {
		f.cleanup()
	}
	lines := f.lines()
	if last := lines[len(lines)-1]; last != "kept_packages: "+kept {
		t.Errorf("the last line: %q, want kept_packages: %s", last, kept)
	}
	if got := markers(t, lines); got[len(got)-1] != step+" failed" {
		t.Errorf("the last marker: %q", got)
	}
	if !slices.Contains(lines, "failed at step "+step+": "+code) {
		t.Errorf("no 'failed at step %s: %s' in\n%s", step, code, f.out)
	}
	if !regexp.MustCompile(`(?m)^` + resultMarker + ` - virt failed ` + step + ` ` + code + ` \S+$`).MatchString(f.out.String()) {
		t.Errorf("no result line for %s %s:\n%s", step, code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	if f.runner.isEnabled(testUnit) {
		t.Error("the unit is still enabled")
	}
	if entries, _ := os.ReadDir(f.env.Roots.tmp()); len(entries) != 0 {
		t.Errorf("the area of the download is not empty: %v", entries)
	}
	for _, p := range append([]string{"/var/lib/casaos/modules/virt.files", "/var/lib/casaos/modules/virt.dirs", "/var/lib/casaos/modules/virt.version", "/etc/casaos/modules/virt.json"}, madeDirs...) {
		if f.exists(p) {
			t.Errorf("%s is left", p)
		}
	}
	// and the log has it all
	log := f.read("/var/log/casaos/module-virt.log")
	if !strings.HasSuffix(log, "kept_packages: "+kept+"\n") || !strings.Contains(log, resultMarker) {
		t.Errorf("the log:\n%s", log)
	}
}

const keptAll = "libvirt-clients qemu-utils"

func TestAFailureAtEachStepIsRolledBackAndKeepsThePackages(t *testing.T) {
	traversal := moduleEntries(manifestJSON(t, testManifest()))
	traversal = append(traversal, tent{name: "build/sysroot/usr/bin/../../../etc/cron.d/x", body: "x"})
	otherUnit := testManifest()
	otherUnit.Unit = "casaos-other.service"
	badManifest := moduleEntries(`{"schema":1}`)

	cases := []struct {
		name, step, code, kept string
		setup                  func(f *fixture)
	}{
		{"apt fails", StepPackages, CodePackagesFailed, "unknown", func(f *fixture) { f.runner.fail["apt-get install"] = 100 }},
		{"apt finds a package held", StepPackages, CodeHeldPackage, "unknown", func(f *fixture) {
			f.runner.fail["apt-get install"] = 100
			f.runner.said["apt-get install"] = "E: Held packages were changed and -y was used without --allow-change-held-packages.\n"
		}},
		{"the download fails every time", StepDownload, CodeDownloadFailed, keptAll, func(f *fixture) { f.dl.err = errors.New("connection reset") }},
		{"the download is refused", StepDownload, CodeDownloadFailed, keptAll, func(f *fixture) { f.dl.err = &PermanentError{Err: errors.New("status 404")} }},
		{"the download is too large", StepDownload, CodeDownloadTooLarge, keptAll, func(f *fixture) { f.dl.err = &PermanentError{Err: ErrTooLarge} }},
		{"the archive is not the one of the catalogue", StepVerify, CodeChecksumMismatch, keptAll, func(f *fixture) { f.dl.body = append([]byte{}, f.dl.body...); f.dl.body[len(f.dl.body)/2] ^= 1 }},
		{"the archive is another one", StepVerify, CodeChecksumMismatch, keptAll, func(f *fixture) { f.dl.body = gz(f.t, tarBytes(f.t, moduleEntries("{}"))) }},
		{"the archive is not a gzip file", StepUnpack, osmodule.UnpackCorrupt, keptAll, func(f *fixture) { f.setArchive([]byte("this is not an archive")) }},
		{"the archive is a gzip file of nothing", StepUnpack, osmodule.UnpackCorrupt, keptAll, func(f *fixture) { f.setArchive(gz(f.t, []byte("not a tar"))) }},
		{"the archive names a path outside the allowed ones", StepUnpack, osmodule.UnpackPath, keptAll, func(f *fixture) { f.setEntries(traversal) }},
		{"the archive has a module.json that is no manifest", StepManifest, CodeManifestInvalid, keptAll, func(f *fixture) { f.setEntries(badManifest) }},
		{"the archive has another manifest", StepManifest, CodeManifestMismatch, keptAll, func(f *fixture) { f.setEntries(moduleEntries(manifestJSON(f.t, otherUnit))) }},
		{"a file of the module is in the way", StepUnpack, osmodule.FileExists, keptAll, func(f *fixture) { f.writeFile("/usr/bin/casaos-virt-management", "someone else's") }},
		{"the archive names a state file of the core", StepUnpack, osmodule.UnpackPath, keptAll, func(f *fixture) {
			f.setEntries(append(moduleEntries(manifestJSON(f.t, testManifest())), tent{name: "build/sysroot/var/lib/casaos/alerts.json", body: "{}"}))
		}},
		{"the archive names a configuration, not a sample", StepUnpack, osmodule.UnpackPath, keptAll, func(f *fixture) {
			f.setEntries(append(moduleEntries(manifestJSON(f.t, testManifest())), tent{name: "build/sysroot/etc/casaos/virt-management.conf", body: "[libvirt]\n"}))
		}},
		{"the archive names another unit", StepUnpack, osmodule.UnpackPath, keptAll, func(f *fixture) {
			f.setEntries(append(moduleEntries(manifestJSON(f.t, testManifest())), tent{name: "build/sysroot/usr/lib/systemd/system/casaos-gateway.service", body: "[Service]\n"}))
		}},
		{"a file cannot be written", StepUnpack, CodeUnpackFailed, keptAll, func(f *fixture) { f.host.wrapDest = failingDest(3) }},
		{"systemd cannot read its units", StepUnits, CodeUnitsFailed, keptAll, func(f *fixture) { f.runner.fail["systemctl daemon-reload"] = 1 }},
		{"the manifest cannot be written", StepManifest, CodeManifestWrite, keptAll, func(f *fixture) {
			f.env.StepHook = func(_ context.Context, step, phase string) {
				if step == StepUnits && phase == "done" {
					f.writeFile("/etc/casaos/modules", "a file where the directory goes")
				}
			}
			f.cleanup = func() { _ = os.Remove(f.path("/etc/casaos/modules")) }
		}},
		{"the unit does not start", StepStart, CodeStartFailed, keptAll, func(f *fixture) { f.runner.fail["systemctl enable --now"] = 1 }},
		{"the gateway does not answer", StepRoute, CodeRouteTimeout, keptAll, func(f *fixture) { f.gw.never = true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(f)
			before := f.treeWithoutBookkeeping()
			f.failsAt(c.step, c.code, c.kept, before)
		})
	}
}

func TestTheDownloadIsTriedThreeTimesAndWaitsBetween(t *testing.T) {
	f := newFixture(t)
	f.dl.err, f.dl.failFirst = errors.New("connection reset"), 2
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if f.dl.calls != 3 || !slices.Equal(f.clock.sleeps, []time.Duration{2 * time.Second, 4 * time.Second}) {
		t.Errorf("tries %d, waits %v", f.dl.calls, f.clock.sleeps)
	}
	f = newFixture(t)
	f.dl.err = errors.New("connection reset")
	if code := f.install("--yes"); code != ExitFailed || f.dl.calls != 3 {
		t.Errorf("exit %d, tries %d", code, f.dl.calls)
	}
	// a refusal that retrying cannot mend is tried once
	f = newFixture(t)
	f.dl.err = &PermanentError{Err: errors.New("status 404")}
	if code := f.install("--yes"); code != ExitFailed || f.dl.calls != 1 {
		t.Errorf("exit %d, tries %d", code, f.dl.calls)
	}
}

func TestTheGatewayIsWaitedForAMinuteAndNoLonger(t *testing.T) {
	f := newFixture(t)
	f.gw.answerAfter = 5
	if code := f.install("--yes"); code != ExitOK || f.gw.calls != 6 {
		t.Fatalf("exit %d, asked %d times", code, f.gw.calls)
	}
	f = newFixture(t)
	f.gw.never = true
	start := f.clock.now
	if code := f.install("--yes"); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if waited := f.clock.now.Sub(start); waited < 60*time.Second || waited > 62*time.Second {
		t.Errorf("waited %v", waited)
	}
}

// failingDest is a file system that cannot write the n-th file.
func failingDest(n int) func(osmodule.Destination) osmodule.Destination {
	return func(d osmodule.Destination) osmodule.Destination { return &countingDest{Destination: d, failAt: n} }
}

type countingDest struct {
	osmodule.Destination
	failAt, files int
}

func (d *countingDest) NewFile(p string, perm os.FileMode, replace bool) (osmodule.File, error) {
	if d.files++; d.files == d.failAt {
		return nil, fmt.Errorf("no space left on device")
	}
	return d.Destination.NewFile(p, perm, replace)
}

// ---- interruptions --------------------------------------------------------------------------------------------------------------------------------------------------------

func TestAnInterruptionAtEachStepIsAFailureThatIsRolledBack(t *testing.T) {
	for _, step := range allSteps {
		t.Run(step, func(t *testing.T) {
			f := newFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.env.StepHook = func(_ context.Context, s, phase string) {
				if s == step && phase == "begin" {
					cancel()
				}
			}
			before := f.treeWithoutBookkeeping()
			if exit := f.main(ctx, "install", testName, "--yes"); exit != ExitFailed {
				t.Fatalf("exit %d:\n%s", exit, f.out)
			}
			kept := keptAll
			if step == StepPackages {
				kept = "unknown"
			}
			lines := f.lines()
			if last := lines[len(lines)-1]; last != "kept_packages: "+kept {
				t.Errorf("last line %q", last)
			}
			if !slices.Contains(lines, "failed at step "+step+": "+CodeInterrupted) {
				t.Errorf("not interrupted at %s:\n%s", step, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			if f.runner.isEnabled(testUnit) || f.exists("/etc/casaos/modules/virt.json") || f.exists("/var/lib/casaos/modules/virt.files") {
				t.Error("the state is not clean")
			}
			if entries, _ := os.ReadDir(f.env.Roots.tmp()); len(entries) != 0 {
				t.Errorf("the area of the download is not empty: %v", entries)
			}
		})
	}
}

// the area of the download that a run left (it was killed with a signal nobody can catch) is emptied by the next run
func TestAStaleAreaOfTheDownloadIsEmptiedByTheNextRun(t *testing.T) {
	f := newFixture(t)
	f.writeFile("/var/lib/casaos/modules/tmp/archive.tar", "left by a run that was killed")
	f.writeFile("/var/lib/casaos/modules/tmp/sub/other", "x")
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if entries, _ := os.ReadDir(f.env.Roots.tmp()); len(entries) != 0 {
		t.Errorf("not emptied: %v", entries)
	}
}

func TestAConfigurationThatIsThereIsNeverTouchedByTheInstallNorByItsRollback(t *testing.T) {
	f := newFixture(t)
	f.writeFile("/etc/casaos/virt-management.conf", "[libvirt]\nURI = test:///default\n")
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if got := f.read("/etc/casaos/virt-management.conf"); got != "[libvirt]\nURI = test:///default\n" {
		t.Errorf("the configuration was replaced: %q", got)
	}
	if !f.exists("/etc/casaos/virt-management.conf.sample") || strings.Contains(f.read("/var/lib/casaos/modules/virt.files"), "virt-management.conf\n") {
		t.Error("the sample is not there, or the configuration is the module's")
	}
	// a rollback of an install does not delete a configuration that was there
	f = newFixture(t)
	f.writeFile("/etc/casaos/virt-management.conf", "mine")
	f.runner.fail["systemctl enable --now"] = 1
	if code := f.install("--yes"); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if f.read("/etc/casaos/virt-management.conf") != "mine" || f.exists("/etc/casaos/virt-management.conf.sample") {
		t.Error("the rollback deleted a configuration that was there, or left the sample")
	}
	// and a remove does not either
	f = installed(t)
	f.writeFile("/etc/casaos/virt-management.conf", "mine")
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("remove: %d\n%s", code, f.out)
	}
	if f.read("/etc/casaos/virt-management.conf") != "mine" || f.exists("/etc/casaos/virt-management.conf.sample") {
		t.Error("the remove deleted a configuration that was there, or left the sample")
	}
}

// What the install lists is on the disk before the first directory is made and the first file is in place, and is everything the install may have put down: the files, the unit last, and
// the directories it makes.
func TestTheListsAreOnTheDiskBeforeAnythingIsMadeOrInPlace(t *testing.T) {
	f := newFixture(t)
	checks := 0
	check := func(what string) {
		checks++
		files, err := osmodule.ParseFiles([]byte(f.read("/var/lib/casaos/modules/virt.files")))
		want := []string{"/usr/bin/casaos-virt-management", "/etc/casaos/virt-management.conf.sample", "/usr/lib/systemd/system/" + testUnit}
		if err != nil || !slices.Equal(files, want) {
			t.Errorf("%s: the files: %v, %v", what, files, err)
		}
		dirs, err := osmodule.ParseDirs([]byte(f.read("/var/lib/casaos/modules/virt.dirs")))
		if err != nil || !slices.Equal(sorted(dirs), sorted(madeDirs)) {
			t.Errorf("%s: the directories: %v, %v", what, dirs, err)
		}
	}
	firstDir, firstFile := true, true
	f.host.wrapDest = func(d osmodule.Destination) osmodule.Destination {
		return &hookedDest{Destination: d,
			onDir: func(string) {
				if firstDir {
					firstDir = false
					check("before the first directory")
					if f.exists("/usr/lib") {
						t.Error("a directory was made already")
					}
				}
			},
			onFile: func() {
				if firstFile {
					firstFile = false
					check("before the first file")
					if f.exists("/usr/bin/casaos-virt-management") {
						t.Error("a file was in place already")
					}
				}
			}}
	}
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if checks != 2 {
		t.Fatalf("looked %d times", checks)
	}
}

// hookedDest calls onDir when a directory is to be made and onFile when a file is asked for.
type hookedDest struct {
	osmodule.Destination
	onDir  func(p string)
	onFile func()
}

func (d *hookedDest) Mkdir(p string, perm os.FileMode) error {
	if d.onDir != nil {
		d.onDir(p)
	}
	return d.Destination.Mkdir(p, perm)
}

func (d *hookedDest) NewFile(p string, perm os.FileMode, replace bool) (osmodule.File, error) {
	if d.onFile != nil {
		d.onFile()
	}
	return d.Destination.NewFile(p, perm, replace)
}

// A file that appears after the unpack looked, at the place of one of the module's, is not written over and is not deleted by the rollback: the rollback deletes what the unpack wrote, and
// not what it was going to write.
func TestAFileThatAppearsAtAPlaceOfTheModuleAfterTheCheckingSurvivesTheRollback(t *testing.T) {
	f := newFixture(t)
	n := 0
	f.host.wrapDest = func(d osmodule.Destination) osmodule.Destination {
		return &hookedDest{Destination: d, onFile: func() {
			if n++; n == 3 { // the unit is the third and last
				f.writeFile("/usr/lib/systemd/system/"+testUnit, "someone else's unit")
			}
		}}
	}
	if code := f.install("--yes"); code != ExitFailed {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if !slices.Contains(f.lines(), "failed at step unpack: "+osmodule.FileExists) {
		t.Errorf("not refused at the unpack:\n%s", f.out)
	}
	if got := f.read("/usr/lib/systemd/system/" + testUnit); got != "someone else's unit" {
		t.Errorf("the file of somebody else is now %q", got)
	}
	for _, p := range []string{"/usr/bin/casaos-virt-management", "/etc/casaos/virt-management.conf.sample", "/var/lib/casaos/modules/virt.files", "/var/lib/casaos/modules/virt.dirs"} {
		if f.exists(p) {
			t.Errorf("%s is left", p)
		}
	}
	// the directories that were made are gone, but for the one that holds the file of somebody else
	if !f.exists("/usr/lib/systemd/system") {
		t.Error("the directory of the file of somebody else was removed")
	}
	if f.runner.isEnabled(testUnit) {
		t.Error("the unit is enabled")
	}
}

var _ = tar.TypeDir
