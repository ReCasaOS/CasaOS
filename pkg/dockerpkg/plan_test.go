package dockerpkg

import (
	"crypto/sha256"
	"encoding/hex"
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
	if bad := plan.Offending(); !reflect.DeepEqual(bad, []string{"docker-model-plugin"}) {
		t.Errorf("Offending() = %v, want the new package", bad)
	}
	if from, to := plan.Engine(); from != "29.8.1" || to != "29.8.2" || MajorJump(from, to) {
		t.Errorf("Engine() = %q, %q", from, to)
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
		if plan.Pins() == nil || plan.Names() == nil || len(plan.Pins()) != 0 || len(plan.Names()) != 0 {
			t.Errorf("Pins/Names of an empty plan = %#v / %#v", plan.Pins(), plan.Names())
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
		{"a new package, even an engine one", ok + "Inst docker-model-plugin (1.0.0-1 Docker CE:bullseye [amd64])\n", []string{"docker-model-plugin"}},
		{"a new package that is not an engine one", ok + "Inst libseccomp2 (2.5.1 Debian [amd64])\n", []string{"libseccomp2"}},
		{"an empty current version", ok + "Inst docker-model-plugin [] (1.0.0-1 Docker CE:bullseye [amd64])\n", []string{"docker-model-plugin"}},
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
			if name != InvalidName && !ValidName(name) {
				t.Errorf("%s: Offending() holds %q, which is not a valid name", c.name, name)
			}
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
		"Inst containerd.io:armhf [1.7.27-1] (2.1.4-1 Docker CE [armhf])\n")
	if got, want := plan.Pins(), []string{"containerd.io:armhf=2.1.4-1", "docker-ce=5:29.8.0-1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Pins() = %v, want %v", got, want)
	}
	if got, want := plan.Names(), []string{"containerd.io:armhf", "docker-ce"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
	if bad := plan.Offending(); len(bad) == 0 {
		t.Error("the plan is not acceptable")
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
