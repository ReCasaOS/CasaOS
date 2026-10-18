package osmodule

import (
	"strings"
	"testing"
)

func checkOf(r PlanResult, code string) Check {
	for _, c := range r.Checks {
		if c.Code == code {
			return c
		}
	}
	return Check{}
}

// What a check says when it was not made is not a thing a Box or apt can say: it is a mark of the plan's own, and a check that failed with the same words is a check that failed.
func TestACheckThatWasNotMadeIsToldFromOneThatFailedWithTheSameWords(t *testing.T) {
	for _, words := range []string{DetailNotChecked, "not_checked", " not_checked", "not_checked "} {
		box := newBox()
		box.sim = AptResult{Failure: RefusedAptFailed, Detail: words}
		r := Plan(virtCatalogue(t), "virt", box)
		if r.Refused != "apt_failed" {
			t.Errorf("apt fails with %q: refused %q", words, r.Refused)
		}
		if c := checkOf(r, "apt"); c.OK {
			t.Errorf("apt fails with %q and the check is ok", words)
		}
		if r.PlanID != "" {
			t.Errorf("apt fails with %q and the plan has the id %s", words, r.PlanID)
		}
	}
	// the same for a package manager that cannot be told, a held package and the rest that a Box or apt puts words to
	box := newBox()
	box.sim = AptResult{Failure: RefusedHeld, Detail: DetailNotChecked}
	if r := Plan(virtCatalogue(t), "virt", box); r.Refused != "held_package" || r.PlanID != "" {
		t.Errorf("a hold that says not_checked: %q %q", r.Refused, r.PlanID)
	}
	// a check that was not made still says so, and is still not ok and never the reason
	old := newBox()
	old.osVersion = "11"
	r := Plan(virtCatalogue(t), "virt", old)
	for _, code := range []string{"disk", "apt"} {
		if c := checkOf(r, code); c.OK || c.Detail != DetailNotChecked {
			t.Errorf("%s: %+v", code, c)
		}
	}
	if r.Refused != "os" {
		t.Errorf("refused %q", r.Refused)
	}
}

// An entry that was not read by ParseCatalogue has not been checked, and the checks of the system do not take it for one that was: no oldest release, or one that is not a release, is not
// "every release".
func TestAnEntryThatDidNotComeFromTheReaderIsNotTakenForOneThatPassed(t *testing.T) {
	c := virtCatalogue(t)
	e, _ := c.Find("virt")
	box := System{OSID: "debian", OSVersion: "12", Arch: "amd64", ReCasaOS: "0.5.23"}
	for _, min := range []string{"", "garbage", "0.5", "0.5.23.1", "0.5.x", " 0.5.23", "-1.0.0", "0.5.23\n"} {
		e.Manifest.MinReCasaOS = min
		if got := checkOfSystem(SystemChecks(e, box), "recasaos"); got.OK {
			t.Errorf("an oldest release of %q: the check is ok", min)
		}
		catalogue := Catalogue{Schema: 1, Modules: []Entry{e}}
		r := Plan(catalogue, "virt", newBox())
		if r.Refused != "recasaos" || r.PlanID != "" {
			t.Errorf("an oldest release of %q: refused %q, id %q", min, r.Refused, r.PlanID)
		}
	}
	// a release that is not one is not one that is new enough, even for an entry that asks for the oldest there can be
	e.Manifest.MinReCasaOS = "0.0.0"
	for _, have := range []string{"", "garbage", "latest", "0.0", "v", "0.0.0.0"} {
		if got := checkOfSystem(SystemChecks(e, System{OSID: "debian", OSVersion: "12", Arch: "amd64", ReCasaOS: have}), "recasaos"); got.OK {
			t.Errorf("a release of %q against 0.0.0: the check is ok", have)
		}
	}
	if got := checkOfSystem(SystemChecks(e, box), "recasaos"); !got.OK {
		t.Errorf("0.5.23 against 0.0.0: %+v", got)
	}
}

func checkOfSystem(checks []Check, code string) Check {
	for _, c := range checks {
		if c.Code == code {
			return c
		}
	}
	return Check{}
}

// What the checks of the system say when they are not ok is exactly this, and what they say when they are is nothing.
func TestTheWordsOfTheChecksOfTheSystem(t *testing.T) {
	c := virtCatalogue(t)
	e, _ := c.Find("virt")
	e.Assets.ARM64 = nil
	bad := SystemChecks(e, System{OSID: "debian", OSVersion: "11", Arch: "arm64", ReCasaOS: "0.5.22"})
	want := map[string]string{"os": "unsupported debian:11", "arch": "no archive for arm64", "recasaos": "have 0.5.22 need 0.5.23"}
	for _, ch := range bad {
		if ch.OK || ch.Detail != want[ch.Code] {
			t.Errorf("%s: %+v, want the words %q", ch.Code, ch, want[ch.Code])
		}
	}
	for _, ch := range SystemChecks(e, System{OSID: "debian", OSVersion: "12", Arch: "amd64", ReCasaOS: "0.5.23"}) {
		if !ch.OK || ch.Detail != "" {
			t.Errorf("%s: %+v", ch.Code, ch)
		}
	}
	// a release that the box does not know, or that is not one, is not a release that is too old: it is unknown
	for _, release := range []string{"", "latest", "0.5", "0.5.x", "v0.5.23; id", "0.5.23.1", " "} {
		for _, ch := range SystemChecks(e, System{OSID: "debian", OSVersion: "12", Arch: "amd64", ReCasaOS: release}) {
			if ch.Code == CheckReCasaOS && (ch.OK || ch.Detail != "unknown") {
				t.Errorf("release %q: %+v", release, ch)
			}
		}
	}
	// ...and in a plan
	box := newBox()
	box.osVersion, box.version = "11", "0.5.22"
	r := Plan(virtCatalogue(t), "virt", box)
	if got := checkOf(r, "os").Detail; got != "unsupported debian:11" {
		t.Errorf("os: %q", got)
	}
	if got := checkOf(r, "recasaos").Detail; got != "have 0.5.22 need 0.5.23" {
		t.Errorf("recasaos: %q", got)
	}
	// the details of the other checks
	foreign := newBox()
	p := "/usr/lib/systemd/system/casaos-virt-management.service"
	foreign.units = map[string][]string{"casaos-virt-management.service": {p}}
	if got := checkOf(Plan(virtCatalogue(t), "virt", foreign), "foreign_unit").Detail; got != "not_owned "+p {
		t.Errorf("not owned: %q", got)
	}
	foreign.lists = map[string][]string{"other": {p}, "another": {p}}
	if got := checkOf(Plan(virtCatalogue(t), "virt", foreign), "foreign_unit").Detail; got != "owned_by another" {
		t.Errorf("owned by another module: %q", got)
	}
	if got := checkOf(Plan(virtCatalogue(t), "virt", func() *fakeBox { b := newBox(); b.installed = map[string]Manifest{"virt": {}}; return b }()), "already_installed").Detail; got != "installed" {
		t.Errorf("installed: %q", got)
	}
	if got := checkOf(Plan(virtCatalogue(t), "virt", func() *fakeBox { b := newBox(); b.broken = []string{"virt"}; return b }()), "already_installed").Detail; got != "manifest_broken" {
		t.Errorf("broken: %q", got)
	}
}

// clip is what the box or apt said, as it may be shown: the bounds of it are the ones of printable ASCII and of a hundred characters.
func TestClipKeepsPrintableAsciiAndAHundredCharacters(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{" ", " "}, {"~", "~"}, {"\x7f", "?"}, {"\x1f", "?"}, {"\x00", "?"}, {"\x80", "?"}, {"\xff", "?"}, {"\n", "?"}, {"\t", "?"}, {"\x7e\x7f", "~?"}, {"a\x1fb", "a?b"},
		{"é", "??"}, {"", ""},
		{strings.Repeat("a", 99), strings.Repeat("a", 99)},
		{strings.Repeat("a", 100), strings.Repeat("a", 100)},
		{strings.Repeat("a", 101), strings.Repeat("a", 100)},
		{strings.Repeat("a", 5000), strings.Repeat("a", 100)},
	} {
		if got := clip(c.in); got != c.want {
			t.Errorf("clip(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// what apt says when it fails is let through longer, for the lines that name what could not be met
	for _, c := range []struct {
		n    int
		want int
	}{{399, 399}, {400, 400}, {401, 400}, {5000, 400}} {
		if got := clipTo(strings.Repeat("a", c.n), maxAptDetail); len(got) != c.want {
			t.Errorf("an apt detail of %d characters: %d", c.n, len(got))
		}
	}
	if maxAptDetail != 400 {
		t.Errorf("maxAptDetail = %d", maxAptDetail)
	}
	if got := clipTo("a\x7fb\x1fc", maxAptDetail); got != "a?b?c" {
		t.Errorf("clipTo kept %q", got)
	}
	box := newBox()
	box.sim = AptResult{Failure: RefusedAptFailed, Detail: strings.Repeat("x", 1000)}
	if got := checkOf(Plan(virtCatalogue(t), "virt", box), "apt").Detail; len(got) != maxAptDetail {
		t.Errorf("the words of a failure of apt are %d characters", len(got))
	}
	box.sim = AptResult{Failure: RefusedHeld, Detail: strings.Repeat("x", 1000)}
	if got := checkOf(Plan(virtCatalogue(t), "virt", box), "apt").Detail; len(got) != maxAptDetail {
		t.Errorf("the words of a hold are %d characters", len(got))
	}
	box.sim = AptResult{Failure: RefusedPackageNotExact, Detail: strings.Repeat("x", 1000)}
	if got := checkOf(Plan(virtCatalogue(t), "virt", box), "apt").Detail; len(got) != maxAptDetail {
		t.Errorf("the words of a name that is not exact are %d characters", len(got))
	}
	// the other checks keep to a hundred
	box = newBox()
	box.sim.Removes = []string{strings.Repeat("x", 1000)}
	if got := checkOf(Plan(virtCatalogue(t), "virt", box), "apt").Detail; len(got) != len("would remove ")+100 {
		t.Errorf("the words of a removal are %d characters", len(got))
	}
}

// apt reads a name with a dot or a plus in it as a regular expression when no package has that name, so a simulation that did not install exactly what was asked for is a plan
// for something else. The plan refuses it with a code of its own.
func TestAPlanForPackagesThatAptDidNotTakeAsNamedIsRefusedWithACodeOfItsOwn(t *testing.T) {
	box := newBox()
	box.sim = AptResult{Failure: RefusedPackageNotExact, Detail: "package_not_exact: qemu.utils"}
	r := Plan(virtCatalogue(t), "virt", box)
	if r.Refused != "package_not_exact" || r.PlanID != "" {
		t.Fatalf("refused %q, id %q", r.Refused, r.PlanID)
	}
	if c := checkOf(r, "apt"); c.OK || c.Detail != "package_not_exact: qemu.utils" {
		t.Errorf("apt: %+v", c)
	}
	// the disk is not looked at for it, as for any answer that is a failure
	if c := checkOf(r, "disk"); c.OK || c.Detail != DetailNotChecked {
		t.Errorf("disk: %+v", c)
	}
	if RefusedPackageNotExact != "package_not_exact" {
		t.Errorf("the code is %q", RefusedPackageNotExact)
	}
	// an answer with a failure that no code of the plan knows is a failure of apt, not a pass
	box = newBox()
	box.sim = AptResult{Failure: "something_new", Detail: "x"}
	if r := Plan(virtCatalogue(t), "virt", box); r.Refused != "apt_failed" {
		t.Errorf("an unknown failure: %q", r.Refused)
	}
}

// What a Box answers, as the plan reads it. These tests are the contract that a Box is written to.
func TestTheContractOfTheBox(t *testing.T) {
	const unit = "casaos-virt-management.service"
	usr := "/usr/lib/systemd/system/" + unit
	lib := "/lib/systemd/system/" + unit

	// InstalledManifests: the broken ones are named like the installed ones, by the name of the module ("virt"), and not by the name of its file ("virt.json")
	for _, c := range []struct {
		name   string
		broken []string
		ok     bool
	}{
		{"the name of a module", []string{"virt"}, false},
		{"the name of its file", []string{"virt.json"}, true},
		{"a path", []string{"/etc/casaos/modules/virt.json"}, true},
		{"another module", []string{"other"}, true},
	} {
		b := newBox()
		b.broken = c.broken
		if got := checkOf(Plan(virtCatalogue(t), "virt", b), "already_installed").OK; got != c.ok {
			t.Errorf("broken manifests %s: the check is ok = %v", c.name, got)
		}
	}

	// UnitFiles and FilesLists: a unit file is the module's own if its list names it, and a path under /lib is the path under /usr/lib (they are one directory on a system whose /lib is a
	// link), whichever spelling each side uses
	for _, c := range []struct {
		name       string
		unitFile   string
		listed     string
		listOwner  string
		ok         bool
		wantDetail string
	}{
		{"the same spelling", usr, usr, "virt", true, ""},
		{"the same spelling under /lib", lib, lib, "virt", true, ""},
		{"a unit under /usr/lib and a list under /lib", usr, lib, "virt", true, ""},
		{"a unit under /lib and a list under /usr/lib", lib, usr, "virt", true, ""},
		{"a unit that nobody lists", usr, "", "virt", false, "not_owned " + usr},
		{"a unit under /lib that nobody lists", lib, "", "virt", false, "not_owned " + lib},
		{"another module's, with the other spelling", usr, lib, "other", false, "owned_by other"},
		{"another module's, with the other spelling, the other way", lib, usr, "other", false, "owned_by other"},
		{"a file of the same name somewhere else", "/etc/systemd/system/" + unit, usr, "virt", false, "not_owned /etc/systemd/system/" + unit},
		{"a path that only starts like /lib", "/lib64/systemd/system/" + unit, "/usr/lib64/systemd/system/" + unit, "virt", false, "not_owned /lib64/systemd/system/" + unit},
		{"a path with /lib in the middle", "/opt/lib/systemd/system/" + unit, "/opt/usr/lib/systemd/system/" + unit, "virt", false, "not_owned /opt/lib/systemd/system/" + unit},
	} {
		b := newBox()
		b.units = map[string][]string{unit: {c.unitFile}}
		if c.listed != "" {
			b.lists = map[string][]string{c.listOwner: {c.listed}}
		}
		got := checkOf(Plan(virtCatalogue(t), "virt", b), "foreign_unit")
		if got.OK != c.ok || got.Detail != c.wantDetail {
			t.Errorf("%s: %+v, want ok %v and %q", c.name, got, c.ok, c.wantDetail)
		}
	}
	// a unit that is in the list under one spelling and on the disk under both is owned (the box lists each file once, but a box that lists it twice is not a refusal)
	b := newBox()
	b.units = map[string][]string{unit: {usr, lib}}
	b.lists = map[string][]string{"virt": {usr}}
	if got := checkOf(Plan(virtCatalogue(t), "virt", b), "foreign_unit"); !got.OK {
		t.Errorf("one file, two spellings: %+v", got)
	}

	// the oldest release of ReCasaOS is compared with the version of the distribution as it is written in its release file ("v0.5.29") and not with the version of the component
	for _, have := range []string{"v0.5.29", "0.5.29"} {
		b := newBox()
		b.version = have
		if r := Plan(virtCatalogue(t), "virt", b); checkOf(r, "recasaos").OK != true {
			t.Errorf("the distribution %s: %+v", have, checkOf(r, "recasaos"))
		}
	}
	b = newBox()
	b.version = "0.0.0-dev" // what a core that was not stamped says of itself: it is not a distribution
	if r := Plan(virtCatalogue(t), "virt", b); checkOf(r, "recasaos").OK {
		t.Errorf("a core that says it is a development build is new enough")
	}
}
