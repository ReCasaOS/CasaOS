package dockerpkg

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestIsFamily(t *testing.T) {
	for _, name := range []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin", "docker-ce-rootless-extras", "docker.io", "containerd", "containerd.io:armhf", "docker-ce:i386"} {
		if !IsFamily(name) {
			t.Errorf("IsFamily(%q) = false", name)
		}
	}
	for _, name := range []string{"", "docker", "docker-ce-extra", "libc6", "runc", "dockerd", "containerd.io2", "podman", "snapd", "Docker-CE", ":armhf"} {
		if IsFamily(name) {
			t.Errorf("IsFamily(%q) = true", name)
		}
	}
}

func TestRestartsEngine(t *testing.T) {
	if !RestartsEngine([]string{"docker-buildx-plugin", "containerd.io"}) || !RestartsEngine([]string{"docker.io"}) || !RestartsEngine([]string{"docker-ce:amd64"}) {
		t.Error("an engine package did not restart Docker")
	}
	if RestartsEngine([]string{"docker-buildx-plugin", "docker-compose-plugin", "docker-ce-cli", "docker-ce-rootless-extras"}) || RestartsEngine(nil) {
		t.Error("a client or plugin restarted Docker")
	}
}

func TestValidName(t *testing.T) {
	for _, name := range []string{"zlib1g", "libc6", "g++-12", "libstdc++6", "python3.11", "apache2-bin", "libc6:amd64", "a"} {
		if !ValidName(name) {
			t.Errorf("ValidName(%q) = false", name)
		}
	}
	for _, name := range []string{"", "-rf", "-oAPT::Foo", "x;rm -rf /", "a b", "$(id)", "`id`", "a\nb", "UPPER", "a&&b", ".hidden", "pkg=1.0", strings.Repeat("a", 129), "pkg/../x", "a|b"} {
		if ValidName(name) {
			t.Errorf("ValidName(%q) = true", name)
		}
	}
}

const dockerRepoPolicy = `docker-ce:
  Installed: 5:29.8.1-1~ubuntu.22.04~jammy
  Candidate: 5:29.8.2-1~ubuntu.22.04~jammy
  Version table:
     5:29.8.2-1~ubuntu.22.04~jammy 500
        500 https://download.docker.com/linux/ubuntu jammy/stable amd64 Packages
 *** 5:29.8.1-1~ubuntu.22.04~jammy 500
        500 https://download.docker.com/linux/ubuntu jammy/stable amd64 Packages
        100 /var/lib/dpkg/status
`

func TestOriginFromPolicy(t *testing.T) {
	cases := map[string]Origin{
		dockerRepoPolicy: OriginDockerRepo,
		// the installed version is old and the repository has dropped it: another version is still offered by it
		"docker-ce:\n  Installed: 5:24.0.5-1\n  Candidate: 5:24.0.9-1\n  Version table:\n     5:24.0.9-1 500\n        500 https://download.docker.com/linux/debian bookworm/stable amd64 Packages\n *** 5:24.0.5-1 100\n        100 /var/lib/dpkg/status\n": OriginDockerRepo,
		// only dpkg's own record: installed by hand, or the repository is gone
		"docker-ce:\n  Installed: 5:24.0.5-1\n  Candidate: 5:24.0.5-1\n  Version table:\n *** 5:24.0.5-1 100\n        100 /var/lib/dpkg/status\n": OriginUnknown,
		// a mirror, and plain http: not Docker's repository as this knows it
		"docker-ce:\n  Version table:\n     5:29.0.0-1 500\n        500 https://mirrors.example.org/docker-ce/linux/ubuntu jammy/stable amd64 Packages\n": OriginUnknown,
		"docker-ce:\n  Version table:\n     5:29.0.0-1 500\n        500 http://download.docker.com/linux/ubuntu jammy/stable amd64 Packages\n":            OriginUnknown,
		// a lookalike host
		"docker-ce:\n  Version table:\n     5:29.0.0-1 500\n        500 https://download.docker.com.evil.example/linux/ubuntu jammy stable Packages\n": OriginUnknown,
		"": OriginUnknown,
	}
	for policy, want := range cases {
		if got := OriginFromPolicy(policy); got != want {
			t.Errorf("OriginFromPolicy(%q) = %q, want %q", policy, got, want)
		}
	}
}

func TestCanSeeUpdates(t *testing.T) {
	for origin, want := range map[Origin]bool{OriginDockerRepo: true, OriginDistribution: true, OriginSnap: false, OriginUnknown: false, "": false} {
		if got := origin.CanSeeUpdates(); got != want {
			t.Errorf("%q.CanSeeUpdates() = %v, want %v", origin, got, want)
		}
	}
}

func TestManualCommandNamesWhatIsPending(t *testing.T) {
	// nothing said: the usual set
	if got := ManualCommand(OriginDockerRepo, nil); got != "sudo apt-get update && sudo apt-get install --only-upgrade containerd.io docker-buildx-plugin docker-ce docker-ce-cli docker-compose-plugin" {
		t.Errorf("default docker repository command = %q", got)
	}
	// what apt said is behind, every name of it, sorted, without the architecture
	got := ManualCommand(OriginDockerRepo, []string{"docker-ce", "docker-ce-rootless-extras", "docker-model-plugin", "containerd.io:amd64"})
	if got != "sudo apt-get update && sudo apt-get install --only-upgrade containerd.io docker-ce docker-ce-rootless-extras docker-model-plugin" {
		t.Errorf("pending command = %q", got)
	}
	if got := ManualCommand(OriginDistribution, []string{"docker.io", "containerd", "docker-compose-v2"}); got != "sudo apt-get update && sudo apt-get install --only-upgrade containerd docker-compose-v2 docker.io" {
		t.Errorf("distribution command = %q", got)
	}
	if got := ManualCommand(OriginDistribution, nil); !strings.HasSuffix(got, "--only-upgrade docker.io") {
		t.Errorf("default distribution command = %q", got)
	}
	if got := ManualCommand(OriginSnap, []string{"docker-ce"}); got != "sudo snap refresh docker" {
		t.Errorf("snap command = %q", got)
	}
	if got := ManualCommand(OriginUnknown, []string{"docker-ce"}); got != "" {
		t.Errorf("unknown origin command = %q, want none", got)
	}
	if got := ManualCommand(Origin("x; rm -rf /"), nil); got != "" {
		t.Errorf("a made-up origin gave a command: %q", got)
	}
	// nothing apt printed that is not a family name gets into the line
	got = ManualCommand(OriginDockerRepo, []string{"docker-ce", "x; rm -rf /", "$(id)", "-oAPT::Foo", "libc6"})
	if strings.ContainsAny(got, ";$()") || strings.Contains(got, "libc6") || strings.Contains(got, "-oAPT") || !strings.Contains(got, "docker-ce") {
		t.Errorf("a name outside the family reached the command: %q", got)
	}
}

func TestEngineVersion(t *testing.T) {
	cases := map[string]string{
		"5:29.8.1-1~ubuntu.22.04~jammy": "29.8.1",
		"5:24.0.5-1~debian.12~bookworm": "24.0.5",
		"26.1.5+dfsg1-9+deb13u1":        "26.1.5",
		"20.10.24-0ubuntu1":             "20.10.24",
		"":                              "",
		"junk":                          "",
	}
	for in, want := range cases {
		if got := EngineVersion(in); got != want {
			t.Errorf("EngineVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTouches(t *testing.T) {
	simulation := `Reading package lists...
Inst libc6 [2.35-0ubuntu3.10] (2.35-0ubuntu3.11 Ubuntu:22.04/jammy-updates [amd64])
Inst docker-ce [5:29.8.1-1~ubuntu.22.04~jammy] (5:29.8.2-1~ubuntu.22.04~jammy Docker CE:jammy [amd64])
Inst containerd.io:armhf [2.3.5-1~ubuntu.22.04~jammy] (2.3.6-1~ubuntu.22.04~jammy Docker CE:jammy [armhf])
Remv oldthing [1.0]
Conf docker-ce (5:29.8.2-1~ubuntu.22.04~jammy Docker CE:jammy [amd64])
Inst docker-ce-extra-thing [1] (2 x [amd64])
`
	got := Touches(simulation)
	want := Touched{Docker: []string{"docker-ce", "containerd.io:armhf"}, Removed: []string{"oldthing"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Touches() = %#v, want %#v", got, want)
	}
	if clean := Touches("Inst libc6 [1] (2 x [amd64])\n"); len(clean.Docker) != 0 || len(clean.Removed) != 0 {
		t.Errorf("Touches(clean) = %#v", clean)
	}
}

func TestContractPatternMatchesWhatTouchesFinds(t *testing.T) {
	pattern := regexp.MustCompile(ContractPattern(true))
	for _, line := range []string{
		"Inst docker-ce [5:29.8.1] (5:29.8.2 Docker CE:jammy [amd64])",
		"Inst containerd.io [2.3.5] (2.3.6 Docker CE [amd64])",
		"Inst containerd.io:armhf [2.3.5] (2.3.6 Docker CE [armhf])",
		"Inst docker.io [26.1.5] (26.1.6 Debian [amd64])",
		"Inst docker-ce-rootless-extras (5:29.8.2 Docker CE [amd64])",
		"Remv anything [1.0]",
	} {
		if !pattern.MatchString(line) {
			t.Errorf("the pattern does not match %q", line)
		}
	}
	for _, line := range []string{
		"Inst libc6 [2.35-0ubuntu3.10] (2.35-0ubuntu3.11 Ubuntu [amd64])",
		"Inst docker-ce-extra-thing [1] (2 x [amd64])",
		"Inst containerdxio [1] (2 x [amd64])", // the dot is a dot
		"Conf docker-ce (5:29.8.2 Docker CE [amd64])",
		"Reading package lists...",
	} {
		if pattern.MatchString(line) {
			t.Errorf("the pattern matches %q", line)
		}
	}
	// no character of it needs more than single quotes in a shell
	if strings.Contains(ContractPattern(true), "'") || strings.Contains(ContractPattern(false), "'") {
		t.Errorf("a pattern holds a single quote: %q", ContractPattern(true))
	}
	// on a box with no Docker engine only removals break the contract
	plain := regexp.MustCompile(ContractPattern(false))
	if plain.MatchString("Inst containerd.io [1] (2 x [amd64])") || !plain.MatchString("Remv anything [1.0]") {
		t.Errorf("the pattern without Docker to protect = %q", ContractPattern(false))
	}
}
