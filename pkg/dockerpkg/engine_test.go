package dockerpkg

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestEngineNames(t *testing.T) {
	want := []string{"containerd.io", "docker-buildx-plugin", "docker-ce", "docker-ce-cli", "docker-ce-rootless-extras", "docker-compose-plugin", "docker-model-plugin"}
	if !reflect.DeepEqual(EngineNames, want) {
		t.Errorf("EngineNames = %v, want %v", EngineNames, want)
	}
	if !sort.StringsAreSorted(EngineNames) {
		t.Errorf("EngineNames is not sorted: %v", EngineNames)
	}
	for _, name := range EngineNames {
		if !ValidName(name) || !IsFamily(name) || !IsEngineName(name) {
			t.Errorf("%q is not a valid name of the Docker family", name)
		}
	}
}

func TestIsEngineName(t *testing.T) {
	for _, name := range append([]string{"containerd.io:armhf", "docker-ce:amd64"}, EngineNames...) {
		if !IsEngineName(name) {
			t.Errorf("IsEngineName(%q) = false", name)
		}
	}
	// the distribution's packages are in the family, and are not the button's to touch
	for _, name := range []string{"docker.io", "containerd", "docker-compose-v2", "docker-buildx", "docker", "docker-ce-extra", "libc6", "", "DOCKER-CE", ":amd64", " docker-ce", "docker-ce\n", "snapd", "docker-ce;x", "docker-cex", "docker-ce-cli2"} {
		if IsEngineName(name) {
			t.Errorf("IsEngineName(%q) = true", name)
		}
	}
}

func TestIsEngineNameIgnoresChangesToEngineNames(t *testing.T) {
	saved := EngineNames
	t.Cleanup(func() { EngineNames = saved })
	EngineNames = append(append([]string{}, saved...), "libc6")
	if IsEngineName("libc6") {
		t.Error("appending to EngineNames at run time widened the allowlist")
	}
}

func TestValidVersion(t *testing.T) {
	for _, v := range []string{
		"5:28.0.4-1~debian.11~bullseye", "5:29.8.0-1~debian.11~bullseye", "5:29.8.0-1~ubuntu.24.04~noble",
		"29.8.0", "1.7.27-1", "2.1.4-1~ubuntu.22.04~jammy", "0", "1:2", "1.0+dfsg1-9+deb13u1", strings.Repeat("1", 100),
	} {
		if !ValidVersion(v) {
			t.Errorf("ValidVersion(%q) = false", v)
		}
	}
	for _, v := range []string{
		"", "5:", ":1", "a1", "-1", "~1", ".1", "1;2", "1`id`", "$(id)", "1 2", "1\n", "1.0\n2", "\n1", "1&&2", "1|2", "1'2", `1"2`, `1\2`,
		"1/2", "1=2", "1<2", "5::1", "x:1", "1:2:3", "1.0\x00", "\uff11\uff12", "1,2", "1#", "1*", "1?", "1{2}", "1[2]", "1!", "1%", "1^",
		strings.Repeat("1", 101), "5:" + strings.Repeat("1", 99),
	} {
		if ValidVersion(v) {
			t.Errorf("ValidVersion(%q) = true", v)
		}
	}
}

func TestMajor(t *testing.T) {
	cases := map[string]string{
		"29.8.0": "29", "28.0.4": "28", "24.0.5": "24", "9.1": "9", "10.0": "10", "5": "5", "0.9": "0",
		// a number, not a text: leading zeros do not make another major
		"029.8": "29", "000": "0", "007.1": "7",
		// the dpkg string, epoch and all, gives the engine's major, not the epoch
		"5:29.8.0-1~debian.11~bullseye": "29", "5:28.0.4-1~ubuntu.22.04~jammy": "28",
		"": "", "junk": "", ".5": "", "-1": "", "x29": "", ":29": "",
	}
	for in, want := range cases {
		if got := Major(in); got != want {
			t.Errorf("Major(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMajorJump(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{"28.0.4", "29.8.0", true},
		{"29.8.0", "29.8.1", false},
		{"24.0.5", "24.0.9", false},
		{"24.0.5", "25.0.0", true},
		{"9.9", "10.0", true},
		{"10.0", "9.9", true},
		{"9.0", "09.0", false},
		{"1.7", "2.1", true},
		{"5", "5", false},
		{"", "29.8.0", false},
		{"28.0.4", "", false},
		{"", "", false},
		{"junk", "29.8.0", false},
		{"28.0.4", "junk", false},
	}
	for _, c := range cases {
		if got := MajorJump(c.from, c.to); got != c.want {
			t.Errorf("MajorJump(%q, %q) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}
