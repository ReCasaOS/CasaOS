package osplan

import (
	"math/rand"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestTheProtectedPatternsAreOfTheAlphabetOfAShellCase(t *testing.T) {
	alphabet := regexp.MustCompile(`^[a-z0-9.+*-]+$`)
	seen := map[string]bool{}
	for _, p := range ProtectedNames {
		if !alphabet.MatchString(p) {
			t.Errorf("%q is not of the alphabet [a-z0-9.+*-]", p)
		}
		if seen[p] {
			t.Errorf("%q twice", p)
		}
		seen[p] = true
	}
}

func TestTheProtectedNamesAreTheDesignsList(t *testing.T) {
	want := []string{
		"docker-ce", "docker-ce-cli", "containerd.io", "docker-*-plugin", "docker.io", "samba*", "cifs-utils", "mergerfs", "ntfs-3g",
		"udevil", "smartmontools", "parted", "net-tools", "wget", "curl", "unzip", "git", "openssh-*", "sudo", "systemd*", "dbus",
		"ifupdown", "network-manager", "netplan.io", "dhcpcd", "ca-certificates", "linux-image*", "grub*", "shim*", "initramfs-tools",
		"apt", "dpkg", "libc6",
	}
	if !slices.Equal(ProtectedNames, want) {
		t.Errorf("ProtectedNames:\n got %q\nwant %q", ProtectedNames, want)
	}
}

func TestProtectedSaysWhichNamesAndWhichPattern(t *testing.T) {
	for name, pattern := range map[string]string{
		"docker-ce": "docker-ce", "docker-ce-cli": "docker-ce-cli", "containerd.io": "containerd.io",
		"docker-buildx-plugin": "docker-*-plugin", "docker-compose-plugin": "docker-*-plugin", "docker-model-plugin": "docker-*-plugin",
		"docker.io": "docker.io", "samba": "samba*", "samba-common-bin": "samba*", "samba-libs": "samba*", "cifs-utils": "cifs-utils",
		"mergerfs": "mergerfs", "ntfs-3g": "ntfs-3g", "udevil": "udevil", "smartmontools": "smartmontools", "parted": "parted",
		"net-tools": "net-tools", "wget": "wget", "curl": "curl", "unzip": "unzip", "git": "git", "openssh-server": "openssh-*",
		"openssh-client": "openssh-*", "openssh-sftp-server": "openssh-*", "sudo": "sudo", "systemd": "systemd*", "systemd-sysv": "systemd*",
		"systemd-timesyncd": "systemd*", "dbus": "dbus", "ifupdown": "ifupdown", "network-manager": "network-manager",
		"netplan.io": "netplan.io", "dhcpcd": "dhcpcd", "ca-certificates": "ca-certificates", "linux-image-amd64": "linux-image*",
		"linux-image-6.1.0-53-cloud-amd64": "linux-image*", "grub-pc": "grub*", "grub2-common": "grub*", "grub-efi-amd64-signed": "grub*",
		"shim-signed": "shim*", "initramfs-tools": "initramfs-tools", "apt": "apt", "dpkg": "dpkg", "libc6": "libc6",
		// the architecture does not make another package
		"libc6:i386": "libc6", "docker-ce:amd64": "docker-ce", "systemd:amd64": "systemd*",
	} {
		got, ok := Protected(name)
		if !ok || got != pattern {
			t.Errorf("Protected(%q) = %q, %v; want %q", name, got, ok, pattern)
		}
	}
	for _, name := range []string{
		"fuse", "libsemanage1", "cron", "bash", "apt-utils", "apt-listchanges", "libc6-dev", "libc-bin", "dpkg-dev", "docker-clean",
		"docker", "docker-ce-rootless-extras", "docker-compose", "samb", "xsamba", "libsystemd0", "libsystemd-shared", "dbus-daemon",
		"dbus-x11", "libdbus-1-3", "gitk", "git-man", "libcurl4", "curl-doc", "sudoers", "openssh", "linux-headers-amd64",
		"linux-libc-dev", "libgrub", "mokutil", "net-tools-x", "wgetx", "mergerfs-tools", "containerd", "runc", "",
	} {
		if p, ok := Protected(name); ok {
			t.Errorf("Protected(%q) = %q", name, p)
		}
	}
}

func TestProtectedIsTheShellsCaseOnEveryName(t *testing.T) {
	names := namesToTry(t)
	// ShellCase is the patterns, "|" between them, and ")" at the end
	rendered := ShellCase()
	if !strings.HasSuffix(rendered, ")") || strings.Contains(rendered, " ") {
		t.Fatalf("ShellCase = %q", rendered)
	}
	patterns := strings.Split(strings.TrimSuffix(rendered, ")"), "|")
	if !slices.Equal(patterns, ProtectedNames) {
		t.Fatalf("ShellCase has %q", patterns)
	}
	// path.Match reads these patterns as a shell does (the alphabet has no ? [ \), and a name has no /
	for _, name := range names {
		_, ok := Protected(name)
		viaPath := false
		for _, p := range patterns {
			if m, err := path.Match(p, name); err != nil {
				t.Fatal(err)
			} else if m {
				viaPath = true
			}
		}
		if ok != viaPath {
			t.Errorf("Protected(%q) = %v, path.Match of ShellCase says %v", name, ok, viaPath)
		}
	}
}

func TestAShellSaysWhatProtectedSays(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine (Windows without Git Bash): the case of the runbook is proved by path.Match, and by this test on Linux")
	}
	names := namesToTry(t)
	// one shell for all the names: a name per line is read by a loop, and says yes or no
	script := `while IFS= read -r name; do case "$name" in ` + ShellCase() + ` echo "$name yes";; *) echo "$name no";; esac; done`
	cmd := exec.Command(sh, "-c", script)
	cmd.Stdin = strings.NewReader(strings.Join(names, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sh: %v", err)
	}
	answers := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		i := strings.LastIndexByte(line, ' ')
		answers[line[:i]] = line[i+1:]
	}
	for _, name := range names {
		_, ok := Protected(name)
		want := "no"
		if ok {
			want = "yes"
		}
		if answers[name] != want {
			t.Errorf("sh says %q of %q, Protected says %v", answers[name], name, ok)
		}
	}
}

// namesToTry are the names of the real runs of the measurement, the names of the guard and of what is near them, and
// names made at random of the alphabet of the packages.
func namesToTry(t *testing.T) []string {
	t.Helper()
	set := map[string]bool{}
	for _, f := range []string{"sandbox-paused-upgrade.txt", "sandbox-paused-dist-upgrade.txt", "sandbox-kept-dist-upgrade.txt"} {
		run, fail := ParseSolver(realOutput(t, f))
		if fail != nil {
			t.Fatal(fail)
		}
		for _, list := range [][]string{run.Upgraded, run.New, run.Removed, run.KeptBack, run.AutoRemovable} {
			for _, n := range list {
				set[n] = true
			}
		}
	}
	for _, p := range ProtectedNames {
		base := strings.ReplaceAll(p, "*", "")
		for _, n := range []string{base, "x" + base, base + "x", base + "-x", "lib" + base, strings.ReplaceAll(p, "*", "mid-dle")} {
			set[n] = true
		}
	}
	r := rand.New(rand.NewSource(7))
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789.+-"
	for i := 0; i < 3000; i++ {
		b := make([]byte, 1+r.Intn(14))
		for j := range b {
			b[j] = alphabet[r.Intn(len(alphabet))]
		}
		set[string(b)] = true
	}
	var names []string
	for n := range set {
		if n != "" {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names
}
