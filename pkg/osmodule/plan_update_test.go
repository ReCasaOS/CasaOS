package osmodule

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
)

func TestTagsAreComparedAsNumbers(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v0.1.9", "v0.1.10", -1, true},
		{"v0.1.10", "v0.1.9", 1, true},
		{"v0.1.0", "v0.1.0", 0, true},
		{"v0.9.0", "v0.10.0", -1, true},
		{"v1.0.0", "v0.99.99", 1, true},
		{"v10.0.0", "v9.9.9", 1, true},
		{"v0.1.01", "v0.1.1", 0, true},
		{"v0.1.0", "v0.1.1", -1, true},
		{"v99999999999999999999999.0.0", "v99999999999999999999998.9.9", 1, true},
		{"", "v0.1.0", 0, false},
		{"v0.1.0", "", 0, false},
		{"0.1.0", "v0.1.1", 0, false},
		{"v0.1", "v0.1.1", 0, false},
		{"v0.1.0-rc1", "v0.1.1", 0, false},
		{"v0.1.0 ", "v0.1.1", 0, false},
		{"v0.1.x", "v0.1.1", 0, false},
		{"latest", "v0.1.1", 0, false},
	} {
		got, ok := CompareTags(c.a, c.b)
		if got != c.want || ok != c.ok {
			t.Errorf("CompareTags(%q, %q) = %d, %v; want %d, %v", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}

// updBox is a box that has the module installed at v0.1.0, with its unit, and that can say so.
type updBox struct {
	*fakeBox
	st InstalledState
}

func (b *updBox) Installed(name string) InstalledState {
	b.calls = append(b.calls, "Installed "+name)
	return b.st
}

const virtUnitPath = "/usr/lib/systemd/system/casaos-virt-management.service"

func newUpdBox(t *testing.T) *updBox {
	t.Helper()
	b := newBox()
	b.units = map[string][]string{"casaos-virt-management.service": {virtUnitPath}}
	b.lists = map[string][]string{"virt": {virtUnitPath}}
	m, err := Parse("virt.json", []byte(virtManifest))
	if err != nil {
		t.Fatal(err)
	}
	return &updBox{fakeBox: b, st: InstalledState{Present: true, Manifest: m, Tag: "v0.1.0", Files: FilesVerified, Bytes: 5 * mb, BytesOK: true}}
}

// newer is the catalogue with the module at v0.1.1 and the changes asked for.
func newer(t *testing.T, mutate ...func(string) string) Catalogue {
	t.Helper()
	return virtCatalogue(t, append([]func(string) string{func(s string) string { return strings.ReplaceAll(s, "v0.1.0", "v0.1.1") }}, mutate...)...)
}

var updateChecks = []string{"catalogue", "os", "arch", "recasaos", "installed", "newer", "foreign_unit", "busy", "dpkg_state", "disk", "apt"}

func TestAPlanOfAnUpdateThatCanBeMade(t *testing.T) {
	box := newUpdBox(t)
	box.sim = AptResult{Known: true} // (everything is there)
	r := PlanUpdate(newer(t), "virt", box)
	if r.Refused != "" || len(failed(r)) != 0 {
		t.Fatalf("refused %q, failed %v, checks %+v", r.Refused, failed(r), r.Checks)
	}
	if got := checkCodes(r); !slices.Equal(got, updateChecks) {
		t.Errorf("checks = %v, want %v", got, updateChecks)
	}
	if r.Mode != "update" || r.Tag != "v0.1.1" || r.Update == nil || r.Update.Installed != "v0.1.0" || r.Update.Latest != "v0.1.1" {
		t.Errorf("%+v %+v", r, r.Update)
	}
	allFive := []string{"libvirt-clients", "libvirt-daemon-system", "ovmf", "qemu-system-x86", "qemu-utils"}
	if len(r.Update.Added) != 0 || len(r.Update.Dropped) != 0 || len(box.simulated) != 1 || !slices.Equal(box.simulated[0], allFive) {
		t.Errorf("the packages did not change, and apt was asked about %v (%+v)", box.simulated, r.Update)
	}
	if len(r.PlanID) != 35 || !strings.HasPrefix(r.PlanID, "m1-") {
		t.Errorf("the id: %q", r.PlanID)
	}
	// the text that is hashed: the mode, the module, both tags, the archive, the packages and the system
	text := `{"mode":"update","name":"virt","installed":"v0.1.0","tag":"v0.1.1","sha256":"` + virtSHA + `","packages":[],"os_id":"debian","os_version":"12"}`
	sum := sha256.Sum256([]byte(text))
	if want := "m1-" + hex.EncodeToString(sum[:])[:32]; r.PlanID != want {
		t.Errorf("plan id = %s, want %s", r.PlanID, want)
	}
	// it is never the id of an install of the same archive, and it follows the installed tag
	if inst := Plan(newer(t), "virt", newBox()); inst.PlanID == r.PlanID || inst.Mode != "" || inst.Update != nil {
		t.Errorf("the install plan: %+v", inst)
	}
	other := newUpdBox(t)
	other.st.Tag = "v0.0.9"
	if got := PlanUpdate(newer(t), "virt", other); got.PlanID == r.PlanID || got.PlanID == "" {
		t.Errorf("the id does not depend on the installed tag: %q %q", got.PlanID, r.PlanID)
	}
}

func TestAnUpdatePlanSimulatesOnlyThePackagesTheNewManifestAddsAndSaysWhichItDrops(t *testing.T) {
	box := newUpdBox(t)
	box.sim = AptResult{ToInstall: []string{"swtpm", "libtpms0"}, DownloadBytes: 2 * mb, ExtraBytes: 3 * mb, Known: true}
	cat := newer(t,
		func(s string) string {
			return strings.ReplaceAll(s, `"qemu-system-x86", "ovmf"`, `"qemu-system-x86", "ovmf", "swtpm"`)
		},
		func(s string) string { return strings.ReplaceAll(s, `, "libvirt-clients"]`, `]`) },
	)
	r := PlanUpdate(cat, "virt", box)
	if r.Refused != "" {
		t.Fatalf("refused %q: %+v", r.Refused, r.Checks)
	}
	// apt is asked about every package of the new manifest, and says which it would install
	if len(box.simulated) != 1 || !slices.Equal(box.simulated[0], []string{"libvirt-daemon-system", "ovmf", "qemu-system-x86", "qemu-utils", "swtpm"}) {
		t.Errorf("apt was asked about %v", box.simulated)
	}
	if !slices.Equal(r.Update.Added, []string{"libtpms0", "swtpm"}) || !slices.Equal(r.Update.Dropped, []string{"libvirt-clients"}) {
		t.Errorf("added %v, dropped %v", r.Update.Added, r.Update.Dropped)
	}
	if !slices.Equal(r.Packages.ToInstall, []string{"libtpms0", "swtpm"}) || r.Packages.DownloadBytes != 2*mb {
		t.Errorf("%+v", r.Packages)
	}
	// the packages the update installs are in the id
	again := newUpdBox(t)
	again.sim = AptResult{ToInstall: []string{"swtpm"}, DownloadBytes: 2 * mb, ExtraBytes: 3 * mb, Known: true}
	if got := PlanUpdate(cat, "virt", again); got.PlanID == r.PlanID {
		t.Error("the id does not depend on the packages to install")
	}
	// packages that were only dropped are no work for apt, which is still asked about the others
	box = newUpdBox(t)
	box.sim = AptResult{Known: true}
	r = PlanUpdate(newer(t, func(s string) string { return strings.ReplaceAll(s, `, "libvirt-clients"]`, `]`) }), "virt", box)
	if r.Refused != "" || len(box.simulated) != 1 || slices.Contains(box.simulated[0], "libvirt-clients") || !slices.Equal(r.Update.Dropped, []string{"libvirt-clients"}) || len(r.Update.Added) != 0 {
		t.Errorf("%q %v %+v", r.Refused, box.simulated, r.Update)
	}
}

func TestAnUpdatePlanIsRefusedWithTheCodeOfTheFirstCheckThatFails(t *testing.T) {
	adds := func(s string) string {
		return strings.ReplaceAll(s, `"qemu-system-x86", "ovmf"`, `"qemu-system-x86", "ovmf", "swtpm"`)
	}
	type tc struct {
		name    string
		cat     func(*testing.T) Catalogue
		mutate  func(*updBox)
		refused string
		detail  string
		check   string
		failing []string
	}
	same := func(t *testing.T) Catalogue { return virtCatalogue(t) }
	bumped := func(t *testing.T) Catalogue { return newer(t) }
	withApt := func(t *testing.T) Catalogue { return newer(t, adds) }
	for _, c := range []tc{
		{"not installed", bumped, func(b *updBox) { b.st = InstalledState{} }, "not_installed", "not_installed", "installed", []string{"installed"}},
		{"a manifest that is broken", bumped, func(b *updBox) { b.st = InstalledState{Broken: true} }, "manifest_invalid", "manifest_broken", "installed", []string{"installed"}},
		{"manifests that cannot be read", bumped, func(b *updBox) { b.st = InstalledState{Unreadable: true} }, "manifest_invalid", "unreadable", "installed", []string{"installed"}},
		{"a list of files of the earlier format", bumped, func(b *updBox) { b.st.Files = FilesUnverified }, "files_unverified", "files_unverified", "installed", []string{"installed"}},
		{"no list of files", bumped, func(b *updBox) { b.st.Files = FilesMissing }, "files_unreadable", "files_missing", "installed", []string{"installed"}},
		{"a list of files that cannot be read", bumped, func(b *updBox) { b.st.Files = FilesUnreadable }, "files_unreadable", "files_unreadable", "installed", []string{"installed"}},
		{"the same version", same, func(b *updBox) {}, "up_to_date", "up_to_date", "newer", []string{"newer"}},
		{"an installed version that is later", bumped, func(b *updBox) { b.st.Tag = "v0.2.0" }, "downgrade", "downgrade", "newer", []string{"newer"}},
		{"an installed v0.1.10 against v0.1.9", func(t *testing.T) Catalogue {
			return virtCatalogue(t, func(s string) string { return strings.ReplaceAll(s, "v0.1.0", "v0.1.9") })
		}, func(b *updBox) { b.st.Tag = "v0.1.10" }, "downgrade", "downgrade", "newer", []string{"newer"}},
		{"an installed version that cannot be read", bumped, func(b *updBox) { b.st.Tag = "" }, "version_unknown", "version_unknown", "newer", []string{"newer"}},
		{"an installed version that is not a tag", bumped, func(b *updBox) { b.st.Tag = "latest" }, "version_unknown", "version_unknown", "newer", []string{"newer"}},
		{"debian 11", bumped, func(b *updBox) { b.osVersion = "11" }, "os", "", "os", []string{"os"}},
		{"an old ReCasaOS", bumped, func(b *updBox) { b.version = "0.5.22" }, "recasaos", "", "recasaos", []string{"recasaos"}},
		{"a unit that is nobody's", bumped, func(b *updBox) {
			b.units = map[string][]string{"casaos-virt-management.service": {"/etc/systemd/system/casaos-virt-management.service"}}
		}, "foreign_unit", "not_owned /etc/systemd/system/casaos-virt-management.service", "foreign_unit", []string{"foreign_unit"}},
		{"a unit that another module has", bumped, func(b *updBox) {
			b.lists = map[string][]string{"virt": {"/usr/bin/casaos-virt-management"}, "other": {virtUnitPath}}
		}, "foreign_unit", "owned_by other", "foreign_unit", []string{"foreign_unit"}},
		{"a new version that names another unit", func(t *testing.T) Catalogue {
			return newer(t, func(s string) string {
				return strings.ReplaceAll(s, "casaos-virt-management.service", "casaos-virt-other.service")
			})
		}, func(b *updBox) {}, "unit_changed", "unit_changed", "foreign_unit", []string{"foreign_unit"}},
		{"a package manager at work", bumped, func(b *updBox) { b.packageManager = true }, "busy", "package_manager", "busy", []string{"busy"}},
		{"an upgrade of the system open", bumped, func(b *updBox) { b.osUpgrade = true }, "busy", "os_upgrade", "busy", []string{"busy"}},
		{"a dpkg that cannot be asked", bumped, func(b *updBox) { b.dpkgErr = errUnreadable }, "dpkg_state", "unreadable", "dpkg_state", []string{"dpkg_state"}},
		{"apt would remove a package", withApt, func(b *updBox) { b.sim.Removes = []string{"x"} }, "apt_removes", "would remove x", "apt", []string{"apt"}},
		{"apt would downgrade a package", withApt, func(b *updBox) { b.sim.Downgrades = []string{"x"} }, "apt_downgrades", "would downgrade x", "apt", []string{"apt"}},
		{"a package held", withApt, func(b *updBox) { b.sim.Held = []string{"x"} }, "held_package", "held x", "apt", []string{"apt"}},
		{"apt that does not answer", withApt, func(b *updBox) { b.sim = AptResult{Failure: RefusedAptFailed, Detail: "no"} }, "apt_failed", "no", "apt", []string{"apt"}},
		{"no apt-get", withApt, func(b *updBox) { b.hasApt = false }, "apt_missing", "apt-get is not there", "apt", []string{"apt"}},
		{"no room", bumped, func(b *updBox) { b.free[TmpDir] = 10 * mb }, "disk_short", "", "disk", []string{"disk"}},
		{"the size of the files to keep cannot be told", bumped, func(b *updBox) { b.st.BytesOK = false }, "disk_unknown", "unknown", "disk", []string{"disk"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			box := newUpdBox(t)
			c.mutate(box)
			r := PlanUpdate(c.cat(t), "virt", box)
			if string(r.Refused) != c.refused || r.PlanID != "" {
				t.Fatalf("refused %q (want %q), id %q:\n%+v", r.Refused, c.refused, r.PlanID, r.Checks)
			}
			if got := checkCodes(r); !slices.Equal(got, updateChecks) {
				t.Errorf("checks = %v", got)
			}
			for _, ch := range r.Checks {
				if ch.Code == c.check && c.detail != "" && ch.Detail != c.detail {
					t.Errorf("%s says %q, want %q", ch.Code, ch.Detail, c.detail)
				}
			}
			// every check that is not ok but the one that refused was not made, or is the failing one
			bad := []string{}
			for _, ch := range r.Checks {
				if !ch.OK && ch.Detail != DetailNotChecked {
					bad = append(bad, ch.Code)
				}
			}
			if !slices.Equal(bad, c.failing) {
				t.Errorf("failing %v, want %v", bad, c.failing)
			}
		})
	}
}

var errUnreadable = &fakeErr{"io"}

type fakeErr struct{ s string }

func (e *fakeErr) Error() string { return e.s }

func TestAnUpdatePlanOfAModuleTheCatalogueDoesNotHaveIsRefusedAndAsksNothing(t *testing.T) {
	box := newUpdBox(t)
	r := PlanUpdate(newer(t), "other", box)
	if r.Refused != "catalogue" || r.PlanID != "" || !slices.Equal(checkCodes(r), updateChecks) || r.Mode != "update" {
		t.Fatalf("%+v", r)
	}
	for _, c := range r.Checks[1:] {
		if c.OK || c.Detail != DetailNotChecked {
			t.Errorf("%+v", c)
		}
	}
	if len(box.calls) != 0 {
		t.Errorf("the box was asked %v", box.calls)
	}
}

// The room an update needs: the archive three times over, a copy of the files it replaces, and what apt downloads and keeps; the room is counted on both file systems.
func TestTheRoomAnUpdateNeedsCountsTheBackupOfTheOldFiles(t *testing.T) {
	need := 3*MaxArchiveBytes + 100*mb
	for _, c := range []struct {
		free  int64
		short bool
	}{{need, false}, {need - 1, true}} {
		box := newUpdBox(t)
		box.sim = AptResult{Known: true}
		box.st.Bytes = 100 * mb
		box.free[TmpDir], box.free[UsrDir] = c.free, 10*gb
		r := PlanUpdate(newer(t), "virt", box)
		if (r.Refused == RefusedDiskShort) != c.short || (!c.short && r.Refused != "") {
			t.Errorf("free %d: refused %q", c.free, r.Refused)
		}
	}
	// the same on the other file system
	box := newUpdBox(t)
	box.sim = AptResult{Known: true}
	box.st.Bytes = 100 * mb
	box.free[TmpDir], box.free[UsrDir] = 10*gb, need-1
	if r := PlanUpdate(newer(t), "virt", box); r.Refused != RefusedDiskShort {
		t.Errorf("refused %q", r.Refused)
	}
	// an install does not count a backup
	ibox := newBox()
	ibox.sim = AptResult{Known: true}
	ibox.free[TmpDir], ibox.free[UsrDir] = 3*MaxArchiveBytes, 3*MaxArchiveBytes
	if r := Plan(newer(t), "virt", ibox); r.Refused != "" {
		t.Errorf("install: refused %q", r.Refused)
	}
}
