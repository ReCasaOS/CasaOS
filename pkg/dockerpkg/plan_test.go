package dockerpkg

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// What `apt-get -s --no-remove install --only-upgrade --no-install-recommends <the seven>`
// prints on Debian 11 with Docker's repository: the owner's box, a major jump.
const debian11Simulation = `NOTE: This is only a simulation!
      apt-get needs root privileges for real execution.
      Keep also in mind that locking is deactivated,
      so don't depend on the relevance to the real current situation!
Reading package lists...
Building dependency tree...
Reading state information...
The following packages will be upgraded:
  containerd.io docker-buildx-plugin docker-ce docker-ce-cli docker-ce-rootless-extras docker-compose-plugin
6 upgraded, 0 newly installed, 0 to remove and 3 not upgraded.
Inst containerd.io [1.7.27-1] (2.1.4-1~debian.11~bullseye Docker CE:bullseye [amd64])
Inst docker-ce-cli [5:28.0.4-1~debian.11~bullseye] (5:29.8.0-1~debian.11~bullseye Docker CE:bullseye [amd64])
Inst docker-ce [5:28.0.4-1~debian.11~bullseye] (5:29.8.0-1~debian.11~bullseye Docker CE:bullseye [amd64])
Inst docker-ce-rootless-extras [5:28.0.4-1~debian.11~bullseye] (5:29.8.0-1~debian.11~bullseye Docker CE:bullseye [amd64])
Inst docker-buildx-plugin [0.22.0-1~debian.11~bullseye] (0.30.1-1~debian.11~bullseye Docker CE:bullseye [amd64])
Inst docker-compose-plugin [2.34.0-1~debian.11~bullseye] (5.0.0-1~debian.11~bullseye Docker CE:bullseye [amd64])
Conf containerd.io (2.1.4-1~debian.11~bullseye Docker CE:bullseye [amd64])
Conf docker-ce-cli (5:29.8.0-1~debian.11~bullseye Docker CE:bullseye [amd64])
Conf docker-ce (5:29.8.0-1~debian.11~bullseye Docker CE:bullseye [amd64])
Conf docker-ce-rootless-extras (5:29.8.0-1~debian.11~bullseye Docker CE:bullseye [amd64])
Conf docker-buildx-plugin (0.30.1-1~debian.11~bullseye Docker CE:bullseye [amd64])
Conf docker-compose-plugin (5.0.0-1~debian.11~bullseye Docker CE:bullseye [amd64])
`

// Ubuntu 22.04, same major, with a plugin that is new (not installed yet).
const ubuntu2204Simulation = `Reading package lists...
Building dependency tree...
Reading state information...
Inst containerd.io [2.3.5-1~ubuntu.22.04~jammy] (2.3.6-1~ubuntu.22.04~jammy Docker CE:jammy [amd64])
Inst docker-ce [5:29.8.1-1~ubuntu.22.04~jammy] (5:29.8.2-1~ubuntu.22.04~jammy Docker CE:jammy [amd64])
Inst docker-model-plugin (1.0.0-1~ubuntu.22.04~jammy Docker CE:jammy [amd64])
Conf docker-ce (5:29.8.2-1~ubuntu.22.04~jammy Docker CE:jammy [amd64])
`

func TestParsePlanDebian11(t *testing.T) {
	plan := ParsePlan(debian11Simulation)
	want := []PlanPackage{
		{Name: "containerd.io", Current: "1.7.27-1", Candidate: "2.1.4-1~debian.11~bullseye"},
		{Name: "docker-ce-cli", Current: "5:28.0.4-1~debian.11~bullseye", Candidate: "5:29.8.0-1~debian.11~bullseye"},
		{Name: "docker-ce", Current: "5:28.0.4-1~debian.11~bullseye", Candidate: "5:29.8.0-1~debian.11~bullseye"},
		{Name: "docker-ce-rootless-extras", Current: "5:28.0.4-1~debian.11~bullseye", Candidate: "5:29.8.0-1~debian.11~bullseye"},
		{Name: "docker-buildx-plugin", Current: "0.22.0-1~debian.11~bullseye", Candidate: "0.30.1-1~debian.11~bullseye"},
		{Name: "docker-compose-plugin", Current: "2.34.0-1~debian.11~bullseye", Candidate: "5.0.0-1~debian.11~bullseye"},
	}
	if !reflect.DeepEqual(plan.Packages, want) {
		t.Errorf("Packages = %#v\nwant %#v", plan.Packages, want)
	}
	if len(plan.Removed) != 0 {
		t.Errorf("Removed = %v", plan.Removed)
	}
	if bad := plan.Offending(); len(bad) != 0 {
		t.Errorf("Offending() = %v, want none", bad)
	}
	if from, to := plan.Engine(); from != "28.0.4" || to != "29.8.0" {
		t.Errorf("Engine() = %q, %q", from, to)
	}
	if from, to := plan.Engine(); !MajorJump(from, to) {
		t.Error("28.0.4 -> 29.8.0 is a major jump")
	}
	wantNames := []string{"containerd.io", "docker-buildx-plugin", "docker-ce", "docker-ce-cli", "docker-ce-rootless-extras", "docker-compose-plugin"}
	if got := plan.Names(); !reflect.DeepEqual(got, wantNames) {
		t.Errorf("Names() = %v, want %v", got, wantNames)
	}
	wantPins := []string{
		"containerd.io=2.1.4-1~debian.11~bullseye",
		"docker-buildx-plugin=0.30.1-1~debian.11~bullseye",
		"docker-ce-cli=5:29.8.0-1~debian.11~bullseye",
		"docker-ce-rootless-extras=5:29.8.0-1~debian.11~bullseye",
		"docker-ce=5:29.8.0-1~debian.11~bullseye",
		"docker-compose-plugin=5.0.0-1~debian.11~bullseye",
	}
	if got := plan.Pins(); !reflect.DeepEqual(got, wantPins) {
		t.Errorf("Pins() = %v, want %v", got, wantPins)
	}
	if !sort.StringsAreSorted(plan.Pins()) {
		t.Error("Pins() is not sorted")
	}
}

func TestParsePlanUbuntu2204NewPackage(t *testing.T) {
	plan := ParsePlan(ubuntu2204Simulation)
	want := []PlanPackage{
		{Name: "containerd.io", Current: "2.3.5-1~ubuntu.22.04~jammy", Candidate: "2.3.6-1~ubuntu.22.04~jammy"},
		{Name: "docker-ce", Current: "5:29.8.1-1~ubuntu.22.04~jammy", Candidate: "5:29.8.2-1~ubuntu.22.04~jammy"},
		{Name: "docker-model-plugin", Candidate: "1.0.0-1~ubuntu.22.04~jammy", New: true},
	}
	if !reflect.DeepEqual(plan.Packages, want) {
		t.Errorf("Packages = %#v\nwant %#v", plan.Packages, want)
	}
	// a new package of the engine is acceptable: it is installed, and not pinned, since
	// --only-upgrade would ignore a pin of a package that is not on the box
	if bad := plan.Offending(); len(bad) != 0 {
		t.Errorf("Offending() = %v, want none: a new package is acceptable", bad)
	}
	if got, want := plan.Pins(), []string{"containerd.io=2.3.6-1~ubuntu.22.04~jammy", "docker-ce=5:29.8.2-1~ubuntu.22.04~jammy"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Pins() = %v, want %v: the new package is not pinned", got, want)
	}
	if got, want := plan.Names(), []string{"containerd.io", "docker-ce", "docker-model-plugin"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
	if got := plan.Dependencies(); got == nil || len(got) != 0 {
		t.Errorf("Dependencies() = %#v: a new package of the engine is not a dependency", got)
	}
	if from, to := plan.Engine(); from != "29.8.1" || to != "29.8.2" || MajorJump(from, to) {
		t.Errorf("Engine() = %q, %q", from, to)
	}
}

// The owner's box for real: Docker 29 depends on nftables, which Docker 28 did not, so apt
// brings in the package and the libraries it needs, and containerd.io moves from 1.x to 2.x.
const debian11WithDependencies = `Reading package lists...
Building dependency tree...
Reading state information...
Inst libjansson4 (2.13.1-1 Debian:11.11/oldstable [amd64])
Inst libedit2 (3.1-20191231-2+b1 Debian:11.11/oldstable [amd64])
Inst libnftables1 (0.9.8-3.1+deb11u2 Debian-Security:11/oldstable-security [amd64])
Inst nftables (0.9.8-3.1+deb11u2 Debian-Security:11/oldstable-security [amd64])
Inst containerd.io [1.7.27-1] (2.1.5-1~debian.11~bullseye Docker CE:bullseye [amd64])
Inst docker-ce-cli [5:28.0.4-1~debian.11~bullseye] (5:29.8.0-1~debian.11~bullseye Docker CE:bullseye [amd64])
Inst docker-ce [5:28.0.4-1~debian.11~bullseye] (5:29.8.0-1~debian.11~bullseye Docker CE:bullseye [amd64])
Conf nftables (0.9.8-3.1+deb11u2 Debian-Security:11/oldstable-security [amd64])
`

func TestParsePlanOfTheOwnersBoxWithItsNewDependencies(t *testing.T) {
	plan := ParsePlan(debian11WithDependencies)
	want := []PlanPackage{
		{Name: "libjansson4", Candidate: "2.13.1-1", New: true},
		{Name: "libedit2", Candidate: "3.1-20191231-2+b1", New: true},
		{Name: "libnftables1", Candidate: "0.9.8-3.1+deb11u2", New: true},
		{Name: "nftables", Candidate: "0.9.8-3.1+deb11u2", New: true},
		{Name: "containerd.io", Current: "1.7.27-1", Candidate: "2.1.5-1~debian.11~bullseye"},
		{Name: "docker-ce-cli", Current: "5:28.0.4-1~debian.11~bullseye", Candidate: "5:29.8.0-1~debian.11~bullseye"},
		{Name: "docker-ce", Current: "5:28.0.4-1~debian.11~bullseye", Candidate: "5:29.8.0-1~debian.11~bullseye"},
	}
	if !reflect.DeepEqual(plan.Packages, want) {
		t.Fatalf("Packages = %#v\nwant %#v", plan.Packages, want)
	}
	if bad := plan.Offending(); bad == nil || len(bad) != 0 {
		t.Errorf("Offending() = %#v, want an empty, non-nil list: the owner's own box must be acceptable", bad)
	}
	if from, to := plan.Engine(); from != "28.0.4" || to != "29.8.0" || !MajorJump(from, to) {
		t.Errorf("Engine() = %q, %q", from, to)
	}
	// what apt is told to upgrade is the upgrades alone; a new package is never pinned
	wantPins := []string{"containerd.io=2.1.5-1~debian.11~bullseye", "docker-ce-cli=5:29.8.0-1~debian.11~bullseye", "docker-ce=5:29.8.0-1~debian.11~bullseye"}
	if got := plan.Pins(); !reflect.DeepEqual(got, wantPins) {
		t.Errorf("Pins() = %v, want %v", got, wantPins)
	}
	for _, pin := range plan.Pins() {
		for _, fresh := range []string{"nftables", "libnftables1", "libjansson4", "libedit2"} {
			if strings.HasPrefix(pin, fresh+"=") {
				t.Errorf("Pins() holds the new package %s: %s", fresh, pin)
			}
		}
	}
	// ... and what the unit's guard may see apt install is both
	wantNames := []string{"containerd.io", "docker-ce", "docker-ce-cli", "libedit2", "libjansson4", "libnftables1", "nftables"}
	if got := plan.Names(); !reflect.DeepEqual(got, wantNames) {
		t.Errorf("Names() = %v, want %v", got, wantNames)
	}
	if got, want := plan.Dependencies(), []string{"libedit2", "libjansson4", "libnftables1", "nftables"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dependencies() = %v, want %v", got, want)
	}
	// the id covers the new packages: another dependency set is another plan, to be confirmed again
	without := ParsePlan(strings.ReplaceAll(debian11WithDependencies, "Inst libedit2 (3.1-20191231-2+b1 Debian:11.11/oldstable [amd64])\n", ""))
	if without.ID() == plan.ID() {
		t.Error("ID() did not change with a dependency that is gone")
	}
	newer := ParsePlan(strings.Replace(debian11WithDependencies, "Inst nftables (0.9.8-3.1+deb11u2", "Inst nftables (0.9.8-3.1+deb11u3", 1))
	if newer.ID() == plan.ID() {
		t.Error("ID() did not change with a newer version of a dependency")
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"containerd.io 1.7.27-1>2.1.5-1~debian.11~bullseye",
		"docker-ce 5:28.0.4-1~debian.11~bullseye>5:29.8.0-1~debian.11~bullseye",
		"docker-ce-cli 5:28.0.4-1~debian.11~bullseye>5:29.8.0-1~debian.11~bullseye",
		"libedit2 >3.1-20191231-2+b1",
		"libjansson4 >2.13.1-1",
		"libnftables1 >0.9.8-3.1+deb11u2",
		"nftables >0.9.8-3.1+deb11u2",
	}, "\n")))
	if want := hex.EncodeToString(sum[:]); plan.ID() != want {
		t.Errorf("ID() = %s, want %s", plan.ID(), want)
	}
}

func TestParsePlanLineShapes(t *testing.T) {
	plan := ParsePlan(strings.Join([]string{
		"Inst docker-ce [5:28.0.4-1~debian.11~bullseye] (5:29.8.0-1~debian.11~bullseye Docker CE:bullseye [amd64])",
		"Inst docker-compose-plugin (5.0.0-1~debian.11~bullseye Docker CE:bullseye [amd64])",
		"Inst containerd.io:armhf [2.3.5-1~ubuntu.22.04~jammy] (2.3.6-1~ubuntu.22.04~jammy Docker CE:jammy [armhf])",
		"Inst docker-ce-cli [5:28.0.4-1] (5:29.8.0-1 Docker CE:bullseye [amd64]) []",
		"   Inst docker-buildx-plugin [0.22.0-1] (0.30.1-1 Docker CE:bullseye [amd64])\r",
		"Inst\tdocker-model-plugin\t[1.0.0-1]\t(1.1.0-1 Docker CE:bullseye [amd64])",
		"Remv docker-compose-v2 [2.1.0]",
		"Remv docker.io:arm64 [26.1.5]",
		"Purg old-thing [1.0]",
		"Conf docker-ce (5:29.8.0-1~debian.11~bullseye Docker CE:bullseye [amd64])",
		"The following packages will be upgraded:",
		"  docker-ce docker-ce-cli",
		"Inst",
		"",
	}, "\n"))
	want := []PlanPackage{
		{Name: "docker-ce", Current: "5:28.0.4-1~debian.11~bullseye", Candidate: "5:29.8.0-1~debian.11~bullseye"},
		{Name: "docker-compose-plugin", Candidate: "5.0.0-1~debian.11~bullseye", New: true},
		{Name: "containerd.io:armhf", Current: "2.3.5-1~ubuntu.22.04~jammy", Candidate: "2.3.6-1~ubuntu.22.04~jammy"},
		{Name: "docker-ce-cli", Current: "5:28.0.4-1", Candidate: "5:29.8.0-1"},
		{Name: "docker-buildx-plugin", Current: "0.22.0-1", Candidate: "0.30.1-1"},
		{Name: "docker-model-plugin", Current: "1.0.0-1", Candidate: "1.1.0-1"},
		{Name: "", New: true},
	}
	if !reflect.DeepEqual(plan.Packages, want) {
		t.Errorf("Packages = %#v\nwant %#v", plan.Packages, want)
	}
	if wantRemoved := []string{"docker-compose-v2", "docker.io:arm64", "old-thing"}; !reflect.DeepEqual(plan.Removed, wantRemoved) {
		t.Errorf("Removed = %v, want %v", plan.Removed, wantRemoved)
	}
}

func TestParsePlanEmptyAndNoise(t *testing.T) {
	for _, in := range []string{"", "\n\n", "Reading package lists...\nBuilding dependency tree...\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n", "E: Unable to locate package docker-ce\n"} {
		plan := ParsePlan(in)
		if len(plan.Packages) != 0 || len(plan.Removed) != 0 {
			t.Errorf("ParsePlan(%q) = %#v", in, plan)
		}
		if bad := plan.Offending(); bad == nil || len(bad) != 0 {
			t.Errorf("Offending() of an empty plan = %#v, want an empty, non-nil list", bad)
		}
		if plan.Pins() == nil || plan.Names() == nil || plan.Dependencies() == nil || len(plan.Pins()) != 0 || len(plan.Names()) != 0 || len(plan.Dependencies()) != 0 {
			t.Errorf("Pins/Names/Dependencies of an empty plan = %#v / %#v / %#v", plan.Pins(), plan.Names(), plan.Dependencies())
		}
		if from, to := plan.Engine(); from != "" || to != "" {
			t.Errorf("Engine() of an empty plan = %q, %q", from, to)
		}
	}
}

func TestParsePlanVeryLongLineDoesNotHideLaterLines(t *testing.T) {
	// a scanner with a line limit would stop here, and never see the removal
	long := "Reading " + strings.Repeat("x", 200000) + "\n"
	plan := ParsePlan(long + "Remv docker-compose-v2 [2.1.0]\n")
	if !reflect.DeepEqual(plan.Removed, []string{"docker-compose-v2"}) {
		t.Errorf("Removed = %v", plan.Removed)
	}
}

func offendingOf(simulation string) []string { return ParsePlan(simulation).Offending() }

func TestOffendingEachKindOfViolation(t *testing.T) {
	ok := "Inst docker-ce [5:28.0.4-1] (5:29.8.0-1 Docker CE:bullseye [amd64])\n"
	cases := []struct {
		name, simulation string
		want             []string
	}{
		{"nothing wrong", ok, []string{}},
		{"a removal", ok + "Remv docker-compose-v2 [2.1.0]\n", []string{"docker-compose-v2"}},
		{"a removal of an engine package", ok + "Remv containerd.io [1.7.27-1]\n", []string{"containerd.io"}},
		{"a purge", ok + "Purg docker-compose-v2 [2.1.0]\n", []string{"docker-compose-v2"}},
		{"a package that is not the engine", ok + "Inst libc6 [2.31-13+deb11u5] (2.31-13+deb11u11 Debian-Security:11/stable-security [amd64])\n", []string{"libc6"}},
		{"docker.io", ok + "Inst docker.io [26.1.5] (26.1.6 Debian [amd64])\n", []string{"docker.io"}},
		{"containerd", ok + "Inst containerd [1.6.20] (1.6.21 Debian [amd64])\n", []string{"containerd"}},
		{"docker-compose-v2", ok + "Inst docker-compose-v2 [2.1] (2.2 Debian [amd64])\n", []string{"docker-compose-v2"}},
		{"docker-buildx", ok + "Inst docker-buildx [0.1] (0.2 Debian [amd64])\n", []string{"docker-buildx"}},
		{"an empty current version", ok + "Inst docker-model-plugin [] (1.0.0-1 Docker CE:bullseye [amd64])\n", []string{"docker-model-plugin"}},
		{"an empty current version of a package that is not the engine's", ok + "Inst libseccomp2 [] (2.5.1 Debian [amd64])\n", []string{"libseccomp2"}},
		{"a current version that is not a version", ok + "Inst docker-model-plugin [1.0;reboot] (1.1.0-1 Docker CE:bullseye [amd64])\n", []string{"docker-model-plugin"}},
		// the distribution's own Docker packages conflict with Docker's: never installed as a new one
		{"a new docker.io", ok + "Inst docker.io (26.1.5 Debian [amd64])\n", []string{"docker.io"}},
		{"a new containerd", ok + "Inst containerd (1.6.20 Debian [amd64])\n", []string{"containerd"}},
		{"a new docker-compose-v2", ok + "Inst docker-compose-v2 (2.2 Debian [amd64])\n", []string{"docker-compose-v2"}},
		{"a new docker-buildx", ok + "Inst docker-buildx (0.2 Debian [amd64])\n", []string{"docker-buildx"}},
		{"a new docker.io of another architecture", ok + "Inst docker.io:arm64 (26.1.5 Debian [arm64])\n", []string{"docker.io:arm64"}},
		{"a new package with a hostile candidate", ok + "Inst nftables (0.9.8;reboot Debian [amd64])\n", []string{"nftables"}},
		{"a new package with a candidate in backticks", ok + "Inst nftables (0.9.8`id` Debian [amd64])\n", []string{"nftables"}},
		{"a new package with an overlong candidate", ok + "Inst nftables (" + strings.Repeat("9", 120) + " Debian [amd64])\n", []string{"nftables"}},
		{"a new package with no candidate", ok + "Inst nftables\n", []string{"nftables"}},
		{"a new package with a name that is a command", ok + "Inst nftables;reboot (1 Debian [amd64])\n", []string{InvalidName}},
		{"a new package with a name that is a flag", ok + "Inst -oAPT::Foo=1 (1 Debian [amd64])\n", []string{InvalidName}},
		{"a new package with an upper case name", ok + "Inst NFTables (1 Debian [amd64])\n", []string{InvalidName}},
		// an upgrade of anything but the engine's own packages drags the base system along
		{"an upgrade of libc6", ok + "Inst libc6 [2.31-13+deb11u5] (2.31-13+deb11u11 Debian-Security:11/stable-security [amd64])\n", []string{"libc6"}},
		{"an upgrade of systemd", ok + "Inst systemd [247.3-7] (247.3-7+deb11u6 Debian [amd64])\n", []string{"systemd"}},
		{"an upgrade of nftables, which is a dependency when it is new", ok + "Inst nftables [0.9.8-3] (0.9.8-3.1 Debian [amd64])\n", []string{"nftables"}},
		{"a hostile name", ok + "Inst x;rm [1] (2 repo [amd64])\n", []string{InvalidName}},
		{"a command substitution as a name", ok + "Inst $(id) [1] (2 repo [amd64])\n", []string{InvalidName}},
		{"a flag as a name", ok + "Inst -oAPT::Foo=1 [1] (2 repo [amd64])\n", []string{InvalidName}},
		{"an upper case name", ok + "Inst Docker-CE [1] (2 repo [amd64])\n", []string{InvalidName}},
		{"an architecture that is a substitution", ok + "Inst docker-ce:$(id) [1] (2 repo [amd64])\n", []string{InvalidName}},
		{"an architecture with a semicolon", ok + "Inst containerd.io:amd64;reboot [1] (2 repo [amd64])\n", []string{InvalidName}},
		{"an overlong name", ok + "Inst docker-ce" + strings.Repeat("e", 200) + " [1] (2 repo [amd64])\n", []string{InvalidName}},
		{"a line with no name", ok + "Inst\n", []string{InvalidName}},
		{"a hostile removed name", ok + "Remv a;b [1]\nRemv\n", []string{InvalidName}},
		{"a hostile candidate", "Inst docker-ce [5:28.0.4-1] (5:29.8.0-1;reboot Docker CE [amd64])\n", []string{"docker-ce"}},
		{"a backtick in the candidate", "Inst docker-ce [5:28.0.4-1] (5:29.8.0-1`id` Docker CE [amd64])\n", []string{"docker-ce"}},
		{"a hostile current version", "Inst docker-ce [5:28.0.4-1$(id)] (5:29.8.0-1 Docker CE [amd64])\n", []string{"docker-ce"}},
		{"an overlong candidate", "Inst docker-ce [5:28.0.4-1] (5:" + strings.Repeat("9", 120) + " Docker CE [amd64])\n", []string{"docker-ce"}},
		{"an overlong current version", "Inst docker-ce [5:" + strings.Repeat("9", 120) + "] (5:29.8.0-1 Docker CE [amd64])\n", []string{"docker-ce"}},
		{"a candidate that is cut", "Inst docker-ce [5:28.0.4-1] (\n", []string{"docker-ce"}},
		{"a line apt did not shape like that", "Inst docker-ce garbage\n", []string{"docker-ce"}},
		{"a candidate without a source", "Inst docker-ce [5:28.0.4-1] (5:29.8.0-1)\n", []string{"docker-ce"}},
		{"several violations, sorted, once each",
			"Inst zeta [1] (2 x)\nInst alpha [1] (2 x)\nInst zeta [1] (3 x)\nRemv alpha [1]\nRemv beta [1]\nInst a;b [1] (2 x)\nInst c;d [1] (2 x)\n",
			[]string{InvalidName, "alpha", "beta", "zeta"}},
	}
	for _, c := range cases {
		got := offendingOf(c.simulation)
		if got == nil {
			got = []string{}
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Offending() = %#v, want %#v", c.name, got, c.want)
		}
		for _, name := range got {
			if name != InvalidName && name != TooManyNew && !ValidName(name) {
				t.Errorf("%s: Offending() holds %q, which is neither a valid name nor a marker", c.name, name)
			}
		}
	}
}

// newLines are n lines of packages that are not installed yet, each of a valid name.
func newLines(n int) string {
	var lines strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&lines, "Inst libdep%d (1.%d Debian:11/stable [amd64])\n", i, i)
	}
	return lines.String()
}

func TestOffendingAcceptsNewPackagesUpToTheLimit(t *testing.T) {
	ok := "Inst docker-ce [5:28.0.4-1] (5:29.8.0-1 Docker CE:bullseye [amd64])\n"
	if MaxNewPackages != 10 {
		t.Fatalf("MaxNewPackages = %d, the spec says 10", MaxNewPackages)
	}
	if want := fmt.Sprintf("(more than %d new packages)", MaxNewPackages); TooManyNew != want {
		t.Errorf("TooManyNew = %q, want %q", TooManyNew, want)
	}
	for _, c := range []struct {
		name, simulation string
		want             []string
	}{
		{"none", ok, []string{}},
		{"one dependency", ok + "Inst nftables (0.9.8 Debian [amd64])\n", []string{}},
		{"every package of the engine, new", ok + "Inst containerd.io (2.1.5-1 Docker CE [amd64])\nInst docker-ce-cli (5:29.8.0-1 Docker CE [amd64])\nInst docker-ce-rootless-extras (5:29.8.0-1 Docker CE [amd64])\nInst docker-buildx-plugin (0.30.1-1 Docker CE [amd64])\nInst docker-compose-plugin (5.0.0-1 Docker CE [amd64])\nInst docker-model-plugin (1.0.0-1 Docker CE [amd64])\n", []string{}},
		{"a dependency of another architecture", ok + "Inst libc6:i386 (2.31-13 Debian [i386])\n", []string{}},
		{"exactly the limit", ok + newLines(MaxNewPackages), []string{}},
		{"one more than the limit", ok + newLines(MaxNewPackages+1), []string{TooManyNew}},
		{"far more than the limit", ok + newLines(500), []string{TooManyNew}},
		{"more than the limit, and nothing else said of them", newLines(MaxNewPackages + 1), []string{TooManyNew}},
		// the limit counts the packages that are new, not the lines: an upgrade is not one
		{"the limit of new packages and many upgrades", ok + newLines(MaxNewPackages) + "Inst containerd.io [1.7.27-1] (2.1.5-1 Docker CE [amd64])\nInst docker-ce-cli [5:28.0.4-1] (5:29.8.0-1 Docker CE [amd64])\n", []string{}},
		{"too many, and a removal", ok + newLines(MaxNewPackages+1) + "Remv docker-compose-v2 [2.1.0]\n", []string{TooManyNew, "docker-compose-v2"}},
		{"too many, one of them a hostile name", ok + newLines(MaxNewPackages) + "Inst evil$(reboot) (1 x [amd64])\n", []string{InvalidName, TooManyNew}},
		{"too many, one of them docker.io", ok + newLines(MaxNewPackages) + "Inst docker.io (26.1.5 Debian [amd64])\n", []string{TooManyNew, "docker.io"}},
		{"too many, and an upgrade of libc6", ok + newLines(MaxNewPackages+1) + "Inst libc6 [2.31] (2.32 Debian [amd64])\n", []string{TooManyNew, "libc6"}},
	} {
		got := offendingOf(c.simulation)
		if got == nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Offending() = %#v, want %#v", c.name, got, c.want)
		}
		if !sort.StringsAreSorted(got) {
			t.Errorf("%s: Offending() = %v is not sorted", c.name, got)
		}
	}
}

func TestTheDistributionsDockerPackagesAreRefusedNewOrUpgraded(t *testing.T) {
	// the four names of the addendum, exactly: Docker's own packages are not among them
	refused := []string{"docker.io", "containerd", "docker-compose-v2", "docker-buildx"}
	var denied []string
	for name := range family {
		if _, engine := engineSet[name]; !engine {
			denied = append(denied, name)
		}
	}
	sort.Strings(denied)
	if want := []string{"containerd", "docker-buildx", "docker-compose-v2", "docker.io"}; !reflect.DeepEqual(denied, want) {
		t.Fatalf("the family minus the engine = %v, want %v", denied, want)
	}
	for _, name := range refused {
		if got := offendingOf("Inst " + name + " (1.0 Debian [amd64])\n"); !reflect.DeepEqual(got, []string{name}) {
			t.Errorf("new %s: Offending() = %v", name, got)
		}
		if got := offendingOf("Inst " + name + " [0.9] (1.0 Debian [amd64])\n"); !reflect.DeepEqual(got, []string{name}) {
			t.Errorf("upgraded %s: Offending() = %v", name, got)
		}
	}
	for _, name := range EngineNames {
		if got := offendingOf("Inst " + name + " (1.0 Docker CE [amd64])\n"); len(got) != 0 {
			t.Errorf("new %s: Offending() = %v", name, got)
		}
	}
}

func TestPinsAndNamesLeaveOutWhatOffends(t *testing.T) {
	plan := ParsePlan("Inst docker-ce [5:28.0.4-1] (5:29.8.0-1 Docker CE [amd64])\n" +
		"Inst docker-ce-cli [5:28.0.4-1] (5:29.8.0-1;reboot Docker CE [amd64])\n" +
		"Inst libc6 [2.31] (2.32 Debian [amd64])\n" +
		"Inst x;rm [1] (2 repo [amd64])\n" +
		"Inst docker-ce-rootless-extras:$(id) [5:28.0.4-1] (5:29.8.0-1 Docker CE [amd64])\n" +
		"Inst docker-model-plugin (1.0.0-1 Docker CE [amd64])\n" +
		"Inst docker.io (26.1.5 Debian [amd64])\n" +
		"Inst nftables (0.9.8;reboot Debian [amd64])\n" +
		"Inst libedit2 (3.1 Debian [amd64])\n" +
		"Inst containerd.io:armhf [1.7.27-1] (2.1.4-1 Docker CE [armhf])\n")
	// the upgrades only: the new packages are not pinned
	if got, want := plan.Pins(), []string{"containerd.io:armhf=2.1.4-1", "docker-ce=5:29.8.0-1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Pins() = %v, want %v", got, want)
	}
	// ... and the names are every package the guard may see, upgraded or new
	if got, want := plan.Names(), []string{"containerd.io:armhf", "docker-ce", "docker-model-plugin", "libedit2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
	if got, want := plan.Dependencies(), []string{"libedit2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dependencies() = %v, want %v", got, want)
	}
	if bad := plan.Offending(); len(bad) == 0 {
		t.Error("the plan is not acceptable")
	}
}

func TestPinsNeverHoldANewPackageAndNamesAlwaysDo(t *testing.T) {
	plan := ParsePlan(debian11WithDependencies + "Inst docker-model-plugin (1.0.0-1 Docker CE [amd64])\n")
	pinned := map[string]bool{}
	for _, pin := range plan.Pins() {
		name, _, _ := strings.Cut(pin, "=")
		pinned[name] = true
	}
	named := map[string]bool{}
	for _, name := range plan.Names() {
		named[name] = true
	}
	for _, pkg := range plan.Packages {
		if pinned[pkg.Name] == pkg.New {
			t.Errorf("%s: new = %v, pinned = %v: a package is pinned exactly when it is an upgrade", pkg.Name, pkg.New, pinned[pkg.Name])
		}
		if !named[pkg.Name] {
			t.Errorf("%s is not in Names()", pkg.Name)
		}
	}
	for name := range pinned {
		if !named[name] {
			t.Errorf("%s is pinned and not in Names(): the guard would refuse the upgrade", name)
		}
	}
	// the dependencies are the new packages that are not the engine's
	if got, want := plan.Dependencies(), []string{"libedit2", "libjansson4", "libnftables1", "nftables"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dependencies() = %v, want %v", got, want)
	}
}

func TestPlanIDIsTheHashOfTheSortedLines(t *testing.T) {
	plan := ParsePlan(debian11Simulation)
	lines := []string{
		"containerd.io 1.7.27-1>2.1.4-1~debian.11~bullseye",
		"docker-buildx-plugin 0.22.0-1~debian.11~bullseye>0.30.1-1~debian.11~bullseye",
		"docker-ce 5:28.0.4-1~debian.11~bullseye>5:29.8.0-1~debian.11~bullseye",
		"docker-ce-cli 5:28.0.4-1~debian.11~bullseye>5:29.8.0-1~debian.11~bullseye",
		"docker-ce-rootless-extras 5:28.0.4-1~debian.11~bullseye>5:29.8.0-1~debian.11~bullseye",
		"docker-compose-plugin 2.34.0-1~debian.11~bullseye>5.0.0-1~debian.11~bullseye",
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	// the lines as sort.Strings orders them: "docker-ce " is before "docker-ce-cli " (a space is before '-')
	if want := hex.EncodeToString(sum[:]); plan.ID() != want {
		t.Errorf("ID() = %s, want %s", plan.ID(), want)
	}
	if id := plan.ID(); len(id) != 64 || id != strings.ToLower(id) {
		t.Errorf("ID() = %q is not 64 lower case hex", id)
	}
	// a new package is "name >candidate"
	one := ParsePlan("Inst docker-model-plugin (1.0.0-1 Docker CE [amd64])\n")
	sum = sha256.Sum256([]byte("docker-model-plugin >1.0.0-1"))
	if want := hex.EncodeToString(sum[:]); one.ID() != want {
		t.Errorf("ID() of a new package = %s, want %s", one.ID(), want)
	}
	// nothing
	sum = sha256.Sum256(nil)
	if want := hex.EncodeToString(sum[:]); ParsePlan("").ID() != want {
		t.Errorf("ID() of nothing = %s, want %s", ParsePlan("").ID(), want)
	}
}

func TestPlanIDIsStableUnderLineOrderAndNoise(t *testing.T) {
	id := ParsePlan(debian11Simulation).ID()
	var inst []string
	for _, line := range strings.Split(debian11Simulation, "\n") {
		if strings.HasPrefix(line, "Inst ") {
			inst = append(inst, line)
		}
	}
	for shift := 1; shift < len(inst); shift++ {
		rotated := append(append([]string{}, inst[shift:]...), inst[:shift]...)
		if got := ParsePlan("Reading package lists...\n" + strings.Join(rotated, "\n") + "\nConf docker-ce (x)\n").ID(); got != id {
			t.Errorf("ID() changed with the order of the lines (shift %d): %s != %s", shift, got, id)
		}
	}
	reversed := append([]string{}, inst...)
	sort.Sort(sort.Reverse(sort.StringSlice(reversed)))
	if got := ParsePlan(strings.Join(reversed, "\n")).ID(); got != id {
		t.Errorf("ID() changed with the reversed order: %s != %s", got, id)
	}
}

func TestPlanIDChangesWithAnyVersionOrName(t *testing.T) {
	base := debian11Simulation
	id := ParsePlan(base).ID()
	changes := map[string]string{
		"a candidate":  strings.Replace(base, "(5:29.8.0-1~debian.11~bullseye Docker CE:bullseye [amd64])\nInst docker-ce [", "(5:29.8.1-1~debian.11~bullseye Docker CE:bullseye [amd64])\nInst docker-ce [", 1),
		"a current":    strings.Replace(base, "[1.7.27-1]", "[1.7.26-1]", 1),
		"the epoch":    strings.Replace(base, "Inst docker-ce [5:28.0.4", "Inst docker-ce [4:28.0.4", 1),
		"a name":       strings.Replace(base, "Inst docker-buildx-plugin", "Inst docker-model-plugin", 1),
		"a package":    base + "Inst docker-model-plugin (1.0.0-1 Docker CE [amd64])\n",
		"one dropped":  strings.Replace(base, "Inst docker-compose-plugin [2.34.0-1~debian.11~bullseye] (5.0.0-1~debian.11~bullseye Docker CE:bullseye [amd64])\n", "", 1),
		"the last bit": strings.Replace(base, "5.0.0-1~debian.11~bullseye Docker CE:bullseye [amd64])\nConf", "5.0.1-1~debian.11~bullseye Docker CE:bullseye [amd64])\nConf", 1),
	}
	for what, simulation := range changes {
		if simulation == base {
			t.Fatalf("the test did not change %s", what)
		}
		if got := ParsePlan(simulation).ID(); got == id {
			t.Errorf("ID() did not change with %s", what)
		}
	}
}

func TestPlanEngine(t *testing.T) {
	cases := []struct {
		name, simulation, from, to string
	}{
		{"docker-ce", "Inst docker-ce [5:28.0.4-1~debian.11~bullseye] (5:29.8.0-1~debian.11~bullseye Docker CE [amd64])\n", "28.0.4", "29.8.0"},
		{"docker-ce for a foreign architecture", "Inst docker-ce:i386 [5:24.0.5-1] (5:24.0.9-1 Docker CE [i386])\n", "24.0.5", "24.0.9"},
		{"only containerd moves", "Inst containerd.io [1.7.27-1] (2.1.4-1 Docker CE [amd64])\n", "", ""},
		{"docker-ce-cli is not docker-ce", "Inst docker-ce-cli [5:28.0.4-1] (5:29.8.0-1 Docker CE [amd64])\n", "", ""},
		{"docker-ce is new", "Inst docker-ce (5:29.8.0-1 Docker CE [amd64])\n", "", "29.8.0"},
		{"a candidate that is not a version", "Inst docker-ce [5:28.0.4-1] (junk Docker CE [amd64])\n", "28.0.4", ""},
		{"nothing", "", "", ""},
	}
	for _, c := range cases {
		if from, to := ParsePlan(c.simulation).Engine(); from != c.from || to != c.to {
			t.Errorf("%s: Engine() = %q, %q, want %q, %q", c.name, from, to, c.from, c.to)
		}
	}
}
