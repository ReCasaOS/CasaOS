package osmodule

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
)

// fakeBox is a machine that answers what the plan asks, and records what it was asked.
type fakeBox struct {
	osID, osVersion, arch, version string

	installed    map[string]Manifest
	broken       []string
	installedErr error
	lists        map[string][]string
	listsErr     error
	units        map[string][]string
	unitsErr     error

	packageManager, osUpgrade       bool
	packageManagerErr, osUpgradeErr error

	free    map[string]int64
	freeErr map[string]error

	hasApt       bool
	archiveBytes map[string]int64
	sim          AptResult

	calls     []string
	simulated [][]string
}

func (b *fakeBox) called(name string) bool { return slices.Contains(b.calls, name) }

func (b *fakeBox) OS() (string, string) { b.calls = append(b.calls, "OS"); return b.osID, b.osVersion }
func (b *fakeBox) Arch() string         { b.calls = append(b.calls, "Arch"); return b.arch }
func (b *fakeBox) ReCasaOSVersion() string {
	b.calls = append(b.calls, "ReCasaOSVersion")
	return b.version
}
func (b *fakeBox) InstalledManifests() (map[string]Manifest, []string, error) {
	b.calls = append(b.calls, "InstalledManifests")
	return b.installed, b.broken, b.installedErr
}
func (b *fakeBox) FilesLists() (map[string][]string, error) {
	b.calls = append(b.calls, "FilesLists")
	return b.lists, b.listsErr
}
func (b *fakeBox) UnitFiles(unit string) ([]string, error) {
	b.calls = append(b.calls, "UnitFiles")
	return b.units[unit], b.unitsErr
}
func (b *fakeBox) PackageManagerBusy() (bool, error) {
	b.calls = append(b.calls, "PackageManagerBusy")
	return b.packageManager, b.packageManagerErr
}
func (b *fakeBox) OSUpgradeBusy() (bool, error) {
	b.calls = append(b.calls, "OSUpgradeBusy")
	return b.osUpgrade, b.osUpgradeErr
}
func (b *fakeBox) FreeBytes(path string) (int64, error) {
	b.calls = append(b.calls, "FreeBytes "+path)
	if err := b.freeErr[path]; err != nil {
		return 0, err
	}
	return b.free[path], nil
}
func (b *fakeBox) AptGetExists() bool { b.calls = append(b.calls, "AptGetExists"); return b.hasApt }
func (b *fakeBox) ArchiveBytes(url string) (int64, bool) {
	b.calls = append(b.calls, "ArchiveBytes")
	n, ok := b.archiveBytes[url]
	return n, ok
}
func (b *fakeBox) SimulateApt(packages []string) AptResult {
	b.calls = append(b.calls, "SimulateApt")
	b.simulated = append(b.simulated, slices.Clone(packages))
	return b.sim
}

const (
	mb = int64(1_000_000)
	gb = 1000 * mb
)

// newBox is a Debian 12 box that can install the virtual machines module: ReCasaOS exactly at the oldest release the module works with, nothing installed, nothing busy, room to spare.
func newBox() *fakeBox {
	return &fakeBox{
		osID: "debian", osVersion: "12", arch: "amd64", version: "0.5.23",
		installed: map[string]Manifest{}, lists: map[string][]string{}, units: map[string][]string{},
		free:   map[string]int64{TmpDir: 10 * gb, UsrDir: 10 * gb},
		hasApt: true,
		sim: AptResult{
			ToInstall: []string{"libvirt-clients", "libfdt1", "qemu-utils"}, ToUpgrade: []string{"libc6"}, DownloadBytes: 20 * mb, ExtraBytes: 30 * mb, Known: true,
		},
	}
}

func virtCatalogue(t *testing.T, mutate ...func(string) string) Catalogue {
	t.Helper()
	text := catalogueText(entryText("virt"))
	for _, m := range mutate {
		text = m(text)
	}
	c, err := ParseCatalogue([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func checkCodes(r PlanResult) []string {
	var out []string
	for _, c := range r.Checks {
		out = append(out, c.Code)
	}
	return out
}

func failed(r PlanResult) []string {
	var out []string
	for _, c := range r.Checks {
		if !c.OK {
			out = append(out, c.Code)
		}
	}
	return out
}

var allChecks = []string{"catalogue", "os", "arch", "recasaos", "already_installed", "foreign_unit", "busy", "disk", "apt"}

func TestAPlanOfAModuleThatCanBeInstalled(t *testing.T) {
	box := newBox()
	r := Plan(virtCatalogue(t), "virt", box)
	if r.Refused != "" || len(failed(r)) != 0 {
		t.Fatalf("refused %q, failed %v, checks %+v", r.Refused, failed(r), r.Checks)
	}
	if got := checkCodes(r); !slices.Equal(got, allChecks) {
		t.Errorf("checks = %v, want %v", got, allChecks)
	}
	if r.Name != "virt" || r.Tag != "v0.1.0" || r.Tier != TierCommunity || r.Archive.URL != virtURL || r.Archive.Bytes != nil {
		t.Errorf("%+v", r)
	}
	// the packages are the common ones and the architecture's, each once, in order
	wantAsked := []string{"libvirt-clients", "libvirt-daemon-system", "ovmf", "qemu-system-x86", "qemu-utils"}
	if len(box.simulated) != 1 || !slices.Equal(box.simulated[0], wantAsked) {
		t.Errorf("apt was asked about %v, want %v", box.simulated, wantAsked)
	}
	// what apt says comes through, sorted
	if !slices.Equal(r.Packages.ToInstall, []string{"libfdt1", "libvirt-clients", "qemu-utils"}) || !slices.Equal(r.Packages.ToUpgrade, []string{"libc6"}) ||
		r.Packages.DownloadBytes != 20*mb || r.Packages.ExtraBytes != 30*mb || !r.Packages.Known {
		t.Errorf("packages %+v", r.Packages)
	}
	for _, c := range r.Checks {
		if !c.OK {
			t.Errorf("check %s: %+v", c.Code, c)
		}
	}
}

func TestThePlanIdIsTheHashOfWhatTheInstallDependsOn(t *testing.T) {
	box := newBox()
	r := Plan(virtCatalogue(t), "virt", box)
	// the text that is hashed, written out: the module, its tag, the sha256 of the archive for this architecture, the packages that are installed (sorted) and the system
	text := `{"name":"virt","tag":"v0.1.0","sha256":"` + virtSHA + `","packages":["libfdt1","libvirt-clients","qemu-utils"],"os_id":"debian","os_version":"12"}`
	sum := sha256.Sum256([]byte(text))
	if want := "m1-" + hex.EncodeToString(sum[:])[:32]; r.PlanID != want {
		t.Errorf("plan id = %s, want %s", r.PlanID, want)
	}
	if len(r.PlanID) != 3+32 || !strings.HasPrefix(r.PlanID, "m1-") {
		t.Errorf("shape %q", r.PlanID)
	}
	same := func(mutate func(*fakeBox)) bool {
		b := newBox()
		mutate(b)
		return Plan(virtCatalogue(t), "virt", b).PlanID == r.PlanID
	}
	// what it does not depend on
	for name, mutate := range map[string]func(*fakeBox){
		"the order apt lists the packages in": func(b *fakeBox) { b.sim.ToInstall = []string{"qemu-utils", "libfdt1", "libvirt-clients"} },
		"the packages to upgrade":             func(b *fakeBox) { b.sim.ToUpgrade = []string{"libc6", "zlib1g"} },
		"the sizes":                           func(b *fakeBox) { b.sim.DownloadBytes, b.sim.ExtraBytes = 5, 6 },
		"the room":                            func(b *fakeBox) { b.free[TmpDir], b.free[UsrDir] = 99*gb, 99*gb },
		"the version of ReCasaOS":             func(b *fakeBox) { b.version = "0.9.9" },
		"the size of the archive":             func(b *fakeBox) { b.archiveBytes = map[string]int64{virtURL: 12345} },
	} {
		if !same(mutate) {
			t.Errorf("the plan id depends on %s", name)
		}
	}
	// what it does
	for name, mutate := range map[string]func(*fakeBox){
		"a package to install":      func(b *fakeBox) { b.sim.ToInstall = append(b.sim.ToInstall, "extra") },
		"one package fewer":         func(b *fakeBox) { b.sim.ToInstall = b.sim.ToInstall[:2] },
		"the version of the system": func(b *fakeBox) { b.osVersion = "13" },
		"the system":                func(b *fakeBox) { b.osID, b.osVersion = "ubuntu", "24.04" },
	} {
		b := newBox()
		mutate(b)
		got := Plan(virtCatalogue(t), "virt", b)
		if got.Refused != "" {
			t.Fatalf("%s: refused %s", name, got.Refused)
		}
		if got.PlanID == r.PlanID {
			t.Errorf("the plan id does not depend on %s", name)
		}
	}
	// the tag and the archive
	bumped := Plan(virtCatalogue(t, func(s string) string { return strings.ReplaceAll(s, "v0.1.0", "v0.1.1") }), "virt", newBox())
	if bumped.Refused != "" || bumped.PlanID == r.PlanID {
		t.Errorf("another tag: %q %q", bumped.Refused, bumped.PlanID)
	}
	other := Plan(virtCatalogue(t, func(s string) string { return strings.Replace(s, virtSHA, strings.Repeat("ab", 32), 1) }), "virt", newBox())
	if other.Refused != "" || other.PlanID == r.PlanID {
		t.Errorf("another archive: %q %q", other.Refused, other.PlanID)
	}
	// the archive of the other architecture is not the one that counts
	arm := newBox()
	arm.arch = "arm64"
	armed := Plan(virtCatalogue(t, func(s string) string { return strings.Replace(s, virtSHA+`"}}`, strings.Repeat("cd", 32)+`"}}`, 1) }), "virt", arm)
	if armed.Refused != "" || armed.PlanID == r.PlanID || armed.Archive.URL != virtARMURL {
		t.Errorf("arm64: %q %q %s", armed.Refused, armed.PlanID, armed.Archive.URL)
	}
	// two values that run into each other are two values
	a := PlanID("ab", "c", virtSHA, "debian", "12", nil)
	b := PlanID("a", "bc", virtSHA, "debian", "12", nil)
	if a == b {
		t.Error("the fields of the id are not told apart")
	}
	if PlanID("a", "b", virtSHA, "debian", "12", []string{"x", "y"}) == PlanID("a", "b", virtSHA, "debian", "12", []string{"x y"}) {
		t.Error("the packages of the id are not told apart")
	}
	// the id is of the packages in order, whatever order they are given in
	if PlanID("a", "b", virtSHA, "debian", "12", []string{"y", "x"}) != PlanID("a", "b", virtSHA, "debian", "12", []string{"x", "y"}) {
		t.Error("the id depends on the order of the packages")
	}
	// a refused plan has no id
	refused := newBox()
	refused.osVersion = "11"
	if got := Plan(virtCatalogue(t), "virt", refused); got.Refused == "" || got.PlanID != "" {
		t.Errorf("refused %q with id %q", got.Refused, got.PlanID)
	}
}

func TestThePlanRefusesWithTheCodeOfTheFirstCheckThatFailsAndSaysAllOfThem(t *testing.T) {
	type tc struct {
		name    string
		mutate  func(*fakeBox)
		refused string
		failing []string // the checks that are really not ok (those that were not made are not in it)
		skipped []string // the checks that were not made, which are not ok and say so
	}
	notMade := []string{"disk", "apt"}
	for _, c := range []tc{
		{"debian 11", func(b *fakeBox) { b.osVersion = "11" }, "os", []string{"os"}, notMade},
		{"another system", func(b *fakeBox) { b.osID = "fedora" }, "os", []string{"os"}, notMade},
		{"no system", func(b *fakeBox) { b.osID, b.osVersion = "", "" }, "os", []string{"os"}, notMade},
		{"an id that has the version in it", func(b *fakeBox) { b.osID, b.osVersion = "debian:12", "" }, "os", []string{"os"}, notMade},
		{"an id and a version that run into a listed one", func(b *fakeBox) { b.osID, b.osVersion = "debian:1", "2" }, "os", []string{"os"}, notMade},
		{"a version with a space", func(b *fakeBox) { b.osVersion = "12 " }, "os", []string{"os"}, notMade},
		{"an id in capitals", func(b *fakeBox) { b.osID = "Debian" }, "os", []string{"os"}, notMade},
		{"an architecture that is not known", func(b *fakeBox) { b.arch = "386" }, "arch", []string{"arch"}, notMade},
		{"no architecture", func(b *fakeBox) { b.arch = "" }, "arch", []string{"arch"}, notMade},
		{"an architecture in capitals", func(b *fakeBox) { b.arch = "AMD64" }, "arch", []string{"arch"}, notMade},
		{"an old ReCasaOS", func(b *fakeBox) { b.version = "0.5.22" }, "recasaos", []string{"recasaos"}, notMade},
		{"a version of ReCasaOS that is not one", func(b *fakeBox) { b.version = "latest" }, "recasaos", []string{"recasaos"}, notMade},
		{"no version of ReCasaOS", func(b *fakeBox) { b.version = "" }, "recasaos", []string{"recasaos"}, notMade},
		{"a version of ReCasaOS that is the release candidate of the oldest", func(b *fakeBox) { b.version = "v0.5.23-rc1" }, "recasaos", []string{"recasaos"}, notMade},
		{"a module that is installed", func(b *fakeBox) { b.installed = map[string]Manifest{"virt": {Name: "virt"}} }, "already_installed", []string{"already_installed"}, notMade},
		{"a module whose manifest is broken", func(b *fakeBox) { b.broken = []string{"other", "virt"} }, "already_installed", []string{"already_installed"}, notMade},
		{"the manifests cannot be read", func(b *fakeBox) { b.installedErr = errors.New("permission denied") }, "already_installed", []string{"already_installed"}, notMade},
		{"a unit that is nobody's", func(b *fakeBox) {
			b.units = map[string][]string{"casaos-virt-management.service": {"/etc/systemd/system/casaos-virt-management.service"}}
		}, "foreign_unit", []string{"foreign_unit"}, notMade},
		{"a unit that another module has", func(b *fakeBox) {
			p := "/usr/lib/systemd/system/casaos-virt-management.service"
			b.units = map[string][]string{"casaos-virt-management.service": {p}}
			b.lists = map[string][]string{"other": {p}}
		}, "foreign_unit", []string{"foreign_unit"}, notMade},
		{"a unit in two places and one of them is not ours", func(b *fakeBox) {
			p := "/usr/lib/systemd/system/casaos-virt-management.service"
			b.units = map[string][]string{"casaos-virt-management.service": {p, "/etc/systemd/system/casaos-virt-management.service"}}
			b.lists = map[string][]string{"virt": {p}}
		}, "foreign_unit", []string{"foreign_unit"}, notMade},
		{"the lists of files cannot be read", func(b *fakeBox) { b.listsErr = errors.New("io") }, "foreign_unit", []string{"foreign_unit"}, notMade},
		{"the units cannot be looked at", func(b *fakeBox) { b.unitsErr = errors.New("io") }, "foreign_unit", []string{"foreign_unit"}, notMade},
		{"a package manager at work", func(b *fakeBox) { b.packageManager = true }, "busy", []string{"busy"}, notMade},
		{"the package manager cannot be told", func(b *fakeBox) { b.packageManagerErr = errors.New("io") }, "busy", []string{"busy"}, notMade},
		{"an upgrade of the system open", func(b *fakeBox) { b.osUpgrade = true }, "busy", []string{"busy"}, notMade},
		{"the file of the upgrade cannot be read", func(b *fakeBox) { b.osUpgradeErr = errors.New("io") }, "busy", []string{"busy"}, notMade},
		{"two things at once", func(b *fakeBox) { b.osVersion = "11"; b.packageManager = true }, "os", []string{"os", "busy"}, notMade},
		{"no apt", func(b *fakeBox) { b.hasApt = false }, "apt_missing", []string{"apt"}, []string{"disk"}},
		{"no apt and an old system", func(b *fakeBox) { b.hasApt = false; b.osVersion = "11" }, "os", []string{"os", "apt"}, []string{"disk"}},
		{"apt gives no answer", func(b *fakeBox) {
			b.sim = AptResult{Failure: RefusedAptFailed, Detail: "E: Unable to locate package x"}
		}, "apt_failed", []string{"apt"}, []string{"disk"}},
		{"a package that is held", func(b *fakeBox) { b.sim.Held = []string{"libvirt-clients"} }, "held_package", []string{"apt"}, nil},
		{"apt says a package is held", func(b *fakeBox) { b.sim = AptResult{Failure: RefusedHeld} }, "held_package", []string{"apt"}, []string{"disk"}},
		{"a package that is removed", func(b *fakeBox) { b.sim.Removes = []string{"libvirt0"} }, "apt_removes", []string{"apt"}, nil},
		{"a package that is downgraded", func(b *fakeBox) { b.sim.Downgrades = []string{"libvirt0"} }, "apt_downgrades", []string{"apt"}, nil},
		{"a removal and a package held", func(b *fakeBox) { b.sim.Removes = []string{"a"}; b.sim.Held = []string{"b"} }, "held_package", []string{"apt"}, nil},
		{"sizes that apt did not give", func(b *fakeBox) { b.sim.Known = false }, "disk_unknown", []string{"disk"}, nil},
		{"no room in the work area", func(b *fakeBox) { b.free[TmpDir] = 80*mb - 1 }, "disk_short", []string{"disk"}, nil},
		{"no room in usr", func(b *fakeBox) { b.free[UsrDir] = 80*mb - 1 }, "disk_short", []string{"disk"}, nil},
		{"no room anywhere and a package that is held", func(b *fakeBox) { b.free[UsrDir], b.free[TmpDir] = 0, 0; b.sim.Held = []string{"b"} }, "disk_short", []string{"disk", "apt"}, nil},
		{"the room cannot be told", func(b *fakeBox) { b.freeErr = map[string]error{UsrDir: errors.New("io")} }, "disk_unknown", []string{"disk"}, nil},
		{"a room that is negative", func(b *fakeBox) { b.free[TmpDir] = -1 }, "disk_unknown", []string{"disk"}, nil},
		{"a download that is negative", func(b *fakeBox) { b.sim.DownloadBytes = -1 }, "disk_unknown", []string{"disk"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			box := newBox()
			// (80 MB is what the work area needs in these cases: three archives of 10 MB, 20 MB to download, 30 MB more on the disk)
			box.archiveBytes = map[string]int64{virtURL: 10 * mb}
			c.mutate(box)
			r := Plan(virtCatalogue(t), "virt", box)
			if string(r.Refused) != c.refused {
				t.Errorf("refused %q, want %q (failed: %v)", r.Refused, c.refused, failed(r))
			}
			if got := checkCodes(r); !slices.Equal(got, allChecks) {
				t.Fatalf("checks = %v", got)
			}
			var real, skipped []string
			for _, ch := range r.Checks {
				switch {
				case ch.OK && ch.Detail == DetailNotChecked:
					t.Errorf("check %s is ok and says it was not made", ch.Code)
				case !ch.OK && ch.Detail == DetailNotChecked:
					skipped = append(skipped, ch.Code)
				case !ch.OK:
					real = append(real, ch.Code)
				}
			}
			if !slices.Equal(real, c.failing) {
				t.Errorf("failed checks = %v, want %v", real, c.failing)
			}
			if c.skipped != nil && !slices.Equal(skipped, c.skipped) {
				t.Errorf("checks not made = %v, want %v", skipped, c.skipped)
			}
			if r.PlanID != "" {
				t.Errorf("a refused plan has the id %s", r.PlanID)
			}
			// apt is asked about the packages only when the rest of the plan stands
			if len(box.simulated) != 0 && len(real) != 0 && real[0] != "disk" && real[0] != "apt" {
				t.Errorf("apt was asked about %v although %v failed", box.simulated, real)
			}
		})
	}
}

func TestTheRoomIsTheWholeOfWhatTheInstallWritesOnBothFileSystems(t *testing.T) {
	// three archives of 10 MB, the 20 MB that apt downloads, the 30 MB more that it leaves: 80 MB, wherever the unpacked files go
	ok := func(tmp, usr int64) bool {
		b := newBox()
		b.archiveBytes = map[string]int64{virtURL: 10 * mb}
		b.free = map[string]int64{TmpDir: tmp, UsrDir: usr}
		return Plan(virtCatalogue(t), "virt", b).Refused == ""
	}
	for _, c := range []struct {
		tmp, usr int64
		ok       bool
	}{
		{80 * mb, 80 * mb, true},
		{80*mb - 1, 80 * mb, false},
		{80 * mb, 80*mb - 1, false},
		{80*mb + 1, 80*mb + 1, true},
		{0, 0, false},
		{math.MaxInt64, math.MaxInt64, true},
	} {
		if got := ok(c.tmp, c.usr); got != c.ok {
			t.Errorf("tmp %d, usr %d: ok = %v", c.tmp, c.usr, got)
		}
	}
	// the sizes are read from the box that is asked: both paths are the ones named
	box := newBox()
	Plan(virtCatalogue(t), "virt", box)
	if !box.called("FreeBytes "+TmpDir) || !box.called("FreeBytes "+UsrDir) || TmpDir != "/var/lib/casaos/modules/tmp" || UsrDir != "/usr" {
		t.Errorf("calls %v, %s, %s", box.calls, TmpDir, UsrDir)
	}
	// an archive whose size is not known is the largest that is downloaded, three times
	unknown := func(free int64) bool {
		b := newBox()
		b.sim.DownloadBytes, b.sim.ExtraBytes = 0, 0
		b.free = map[string]int64{TmpDir: free, UsrDir: free}
		return Plan(virtCatalogue(t), "virt", b).Refused == ""
	}
	if want := 3 * MaxArchiveBytes; !unknown(want) || unknown(want-1) {
		t.Errorf("an archive of an unknown size does not need %d", want)
	}
	// what is freed does not make room; sizes that are huge do not wrap around
	freed := newBox()
	freed.archiveBytes = map[string]int64{virtURL: 10 * mb}
	freed.sim.ExtraBytes = -500 * mb
	freed.free = map[string]int64{TmpDir: 50 * mb, UsrDir: 50 * mb}
	if r := Plan(virtCatalogue(t), "virt", freed); r.Refused != "" {
		t.Errorf("a download of 20 MB and 30 MB of archives, refused with %s", r.Refused)
	}
	freed.free = map[string]int64{TmpDir: 50*mb - 1, UsrDir: 50 * mb}
	if r := Plan(virtCatalogue(t), "virt", freed); r.Refused != "disk_short" {
		t.Errorf("one byte short: %q", r.Refused)
	}
	huge := newBox()
	huge.sim.DownloadBytes, huge.sim.ExtraBytes = math.MaxInt64, math.MaxInt64
	huge.archiveBytes = map[string]int64{virtURL: math.MaxInt64}
	if r := Plan(virtCatalogue(t), "virt", huge); r.Refused != "disk_short" {
		t.Errorf("sizes that are the largest there are: %q", r.Refused)
	}
	// sizes that add up to more than a number can hold are not a small number: the sum of two (the largest download and the largest extra), and a product (an archive of a third of the
	// largest number, three times)
	for name, set := range map[string]func(*fakeBox){
		"a sum":     func(b *fakeBox) { b.sim.DownloadBytes, b.sim.ExtraBytes = math.MaxInt64, math.MaxInt64 },
		"a product": func(b *fakeBox) { b.archiveBytes = map[string]int64{virtURL: math.MaxInt64/3 + 1} },
	} {
		wrap := newBox()
		wrap.free = map[string]int64{TmpDir: 10 * gb, UsrDir: 10 * gb}
		wrap.archiveBytes = map[string]int64{virtURL: 10 * mb}
		set(wrap)
		if r := Plan(virtCatalogue(t), "virt", wrap); r.Refused != "disk_short" {
			t.Errorf("%s that goes past the largest number: %q", name, r.Refused)
		}
	}
	// an archive of no bytes is an archive of a size that is not known
	zero := newBox()
	zero.sim.DownloadBytes, zero.sim.ExtraBytes = 0, 0
	zero.archiveBytes = map[string]int64{virtURL: 0}
	zero.free = map[string]int64{TmpDir: 100 * mb, UsrDir: 100 * mb}
	if r := Plan(virtCatalogue(t), "virt", zero); r.Refused != "disk_short" || r.Archive.Bytes != nil {
		t.Errorf("an archive of 0 bytes: %q %v", r.Refused, r.Archive.Bytes)
	}
	negative := newBox()
	negative.sim.DownloadBytes = -5 * gb
	negative.archiveBytes = map[string]int64{virtURL: -5 * gb}
	negative.free = map[string]int64{TmpDir: 0, UsrDir: 0}
	if r := Plan(virtCatalogue(t), "virt", negative); r.Refused != "disk_unknown" {
		t.Errorf("sizes below zero: %q", r.Refused)
	}
	// the size of the archive is said when it is known
	known := newBox()
	known.archiveBytes = map[string]int64{virtURL: 12345}
	if r := Plan(virtCatalogue(t), "virt", known); r.Archive.Bytes == nil || *r.Archive.Bytes != 12345 {
		t.Errorf("archive %+v", r.Archive)
	}
}

func TestThePlanNeverAsksAptWhenThePlanIsAlreadyRefusedOrWhenThereIsNothingToInstall(t *testing.T) {
	// a module with no packages needs no apt, not even to be there
	catalogue := virtCatalogue(t)
	e, _ := catalogue.Find("virt")
	e.Manifest.Packages = Packages{}
	catalogue.Modules[0] = e
	box := newBox()
	box.hasApt = false
	r := Plan(catalogue, "virt", box)
	if r.Refused != "" || box.called("SimulateApt") || !r.Packages.Known || len(r.Packages.ToInstall) != 0 {
		t.Errorf("refused %q, %+v, calls %v", r.Refused, r.Packages, box.calls)
	}
	// ...and the id is still one
	if r.PlanID == "" {
		t.Error("no id")
	}
	// the room is not looked at for a plan that is going to be refused, whether or not there are packages
	old := newBox()
	old.osVersion = "11"
	old.hasApt = false
	if r := Plan(catalogue, "virt", old); r.Refused != "os" || old.called("FreeBytes "+TmpDir) || r.Checks[7].Detail != DetailNotChecked || r.Checks[8].Detail != "" || !r.Checks[8].OK {
		t.Errorf("refused %q, disk %+v, apt %+v, calls %v", r.Refused, r.Checks[7], r.Checks[8], old.calls)
	}
	// the architecture of the box decides which packages are asked about
	arm := newBox()
	arm.arch = "arm64"
	Plan(virtCatalogue(t), "virt", arm)
	if want := []string{"libvirt-clients", "libvirt-daemon-system", "qemu-efi-aarch64", "qemu-system-arm", "qemu-utils"}; len(arm.simulated) != 1 || !slices.Equal(arm.simulated[0], want) {
		t.Errorf("arm64: %v", arm.simulated)
	}
	// a package that is in the common list and in the list of the architecture is asked once
	twice := virtCatalogue(t)
	e, _ = twice.Find("virt")
	e.Manifest.Packages = Packages{Common: []string{"b", "a"}, AMD64: []string{"b", "c"}}
	twice.Modules[0] = e
	box = newBox()
	Plan(twice, "virt", box)
	if want := []string{"a", "b", "c"}; len(box.simulated) != 1 || !slices.Equal(box.simulated[0], want) {
		t.Errorf("%v", box.simulated)
	}
}

func TestACatalogueWithoutTheModuleOrWithAnImpossibleNameGivesAPlanThatSaysSo(t *testing.T) {
	for _, name := range []string{"nothing", "", "Virt", "virt ", "virt\n", "../virt", "virt/../virt", "vi\x00rt", strings.Repeat("a", 33), "virt.json", "-virt"} {
		box := newBox()
		r := Plan(virtCatalogue(t), name, box)
		// (a name that is a name is said back; one that is not is not)
		if want := map[bool]string{true: name, false: ""}[nameRe.MatchString(name)]; r.Refused != "catalogue" || r.Name != want {
			t.Errorf("name %q: refused %q", name, r.Refused)
		}
		if got := checkCodes(r); !slices.Equal(got, allChecks) {
			t.Errorf("name %q: checks %v", name, got)
		}
		if got := failed(r); !slices.Equal(got, allChecks) {
			t.Errorf("name %q: failed %v", name, got)
		}
		if len(box.calls) != 0 {
			t.Errorf("name %q: the box was asked %v", name, box.calls)
		}
		if r.Tag != "" || r.Archive.URL != "" || r.PlanID != "" || r.Packages.ToInstall == nil || r.Packages.ToUpgrade == nil {
			t.Errorf("name %q: %+v", name, r)
		}
	}
	// nothing at all is a catalogue of nothing
	if r := Plan(Catalogue{}, "virt", newBox()); r.Refused != "catalogue" {
		t.Errorf("an empty catalogue: %q", r.Refused)
	}
}

func TestTheVersionOfReCasaOSAgainstTheOldestThatWorks(t *testing.T) {
	for _, c := range []struct {
		have string
		ok   bool
	}{
		{"0.5.23", true}, {"0.5.24", true}, {"0.5.100", true}, {"0.6.0", true}, {"1.0.0", true}, {"0.5.22", false}, {"0.4.99", false}, {"0.5.9", false},
		{"v0.5.23", true}, {"v0.5.22", false},
		{"0.5.23-rc1", false}, {"0.5.24-rc1", true}, {"0.5.23+build5", true}, {"0.5.23-0-gabc123", false}, {"0.6.0-beta.2", true},
		{"0.5.23.1", false}, {"0.5", false}, {"", false}, {"v", false}, {"latest", false}, {"0.5.x", false}, {"0.5.23\n", false}, {" 0.5.23", false}, {"0.5.23 ", false},
		{"00000.5.23", false}, {"10000.0.0", false}, {"0.5.23-", false}, {"0.5.23-rc 1", false}, {"0.5.24-" + strings.Repeat("a", 41), false}, {"0.5.24-" + strings.Repeat("a", 40), true}, {"0.5.23\x00", false},
	} {
		box := newBox()
		box.version = c.have
		r := Plan(virtCatalogue(t), "virt", box)
		var check Check
		for _, ch := range r.Checks {
			if ch.Code == "recasaos" {
				check = ch
			}
		}
		if check.OK != c.ok {
			t.Errorf("%q: ok = %v (%s)", c.have, check.OK, check.Detail)
		}
	}
}

func TestWhatTheBoxSaysIsSaidBackOnlyAsTextThatIsSafeToShow(t *testing.T) {
	box := newBox()
	box.osID, box.osVersion = "deb\nian\x1b[31m", strings.Repeat("9", 500)
	box.version = "ver\x00sion" + strings.Repeat("z", 500)
	r := Plan(virtCatalogue(t), "virt", box)
	for _, c := range r.Checks {
		if len(c.Detail) > 128 || strings.ContainsFunc(c.Detail, func(r rune) bool { return r < ' ' || r > '~' }) {
			t.Errorf("check %s says %q", c.Code, c.Detail)
		}
	}
	// what apt says is cut as well
	box = newBox()
	box.sim = AptResult{Failure: RefusedAptFailed, Detail: "E: bad\n\x1b[2Jline" + strings.Repeat("x", 1000)}
	r = Plan(virtCatalogue(t), "virt", box)
	for _, c := range r.Checks {
		if len(c.Detail) > 128 || strings.ContainsFunc(c.Detail, func(r rune) bool { return r < ' ' || r > '~' }) {
			t.Errorf("check %s says %q", c.Code, c.Detail)
		}
	}
}

func TestThePlanIsJSONWithTheShapeThatTheCommandsPromise(t *testing.T) {
	text, err := json.Marshal(Plan(virtCatalogue(t), "virt", newBox()))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(text, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"name", "tag", "tier", "checks", "refused", "packages", "archive", "plan_id"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("no %q in %s", key, text)
		}
	}
	if string(doc["refused"]) != "null" {
		t.Errorf("refused = %s", doc["refused"])
	}
	var packages map[string]json.RawMessage
	_ = json.Unmarshal(doc["packages"], &packages)
	for _, key := range []string{"to_install", "to_upgrade", "download_bytes", "extra_bytes", "known"} {
		if _, ok := packages[key]; !ok {
			t.Errorf("no %q in packages %s", key, doc["packages"])
		}
	}
	var checks []map[string]any
	_ = json.Unmarshal(doc["checks"], &checks)
	for _, c := range checks {
		if _, ok := c["code"]; !ok {
			t.Errorf("check %v", c)
		}
		if _, ok := c["ok"]; !ok {
			t.Errorf("check %v", c)
		}
		if _, ok := c["detail"]; !ok {
			t.Errorf("check %v", c)
		}
	}
	// a refusal is its code, as a text
	box := newBox()
	box.osVersion = "11"
	text, _ = json.Marshal(Plan(virtCatalogue(t), "virt", box))
	if !strings.Contains(string(text), `"refused":"os"`) {
		t.Errorf("%s", text)
	}
	// a plan that came from no module has lists that are lists
	text, _ = json.Marshal(Plan(Catalogue{}, "x", newBox()))
	if !strings.Contains(string(text), `"to_install":[]`) || !strings.Contains(string(text), `"to_upgrade":[]`) {
		t.Errorf("%s", text)
	}
}

func TestTheChecksOfTheSystemAreTheSameForTheListAndForThePlan(t *testing.T) {
	c := virtCatalogue(t)
	e, _ := c.Find("virt")
	got := SystemChecks(e, System{OSID: "debian", OSVersion: "11", Arch: "amd64", ReCasaOS: "0.5.22"})
	if len(got) != 3 || got[0].Code != "os" || got[1].Code != "arch" || got[2].Code != "recasaos" {
		t.Fatalf("%+v", got)
	}
	if got[0].OK || !got[1].OK || got[2].OK {
		t.Errorf("%+v", got)
	}
	for _, s := range []System{{"debian", "12", "amd64", "0.5.23"}, {"ubuntu", "24.04", "arm64", "0.6.0"}, {"ubuntu", "26.04", "amd64", "v1.0.0"}, {"debian", "13", "arm64", "0.5.23"}} {
		for _, ch := range SystemChecks(e, s) {
			if !ch.OK {
				t.Errorf("%+v: %+v", s, ch)
			}
		}
	}
	// the same answers as the plan gives, on the same box
	box := newBox()
	box.osVersion = "11"
	if r := Plan(c, "virt", box); r.Checks[1].OK != got[0].OK || r.Checks[1].Code != got[0].Code {
		t.Errorf("%+v %+v", r.Checks[1], got[0])
	}
	// an architecture that the entry has no archive for is not supported
	e.Assets.ARM64 = nil
	for _, ch := range SystemChecks(e, System{OSID: "debian", OSVersion: "12", Arch: "arm64", ReCasaOS: "0.5.23"}) {
		if ch.Code == "arch" && ch.OK {
			t.Error("arm64 without an archive for it")
		}
	}
}
