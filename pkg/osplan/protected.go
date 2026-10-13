package osplan

import (
	"strings"
)

// The guard of the removals. A hop to the next release legitimately removes packages that the new release no longer has,
// and must never remove the ones the machine's work depends on (Docker, Samba and the disk tools of ReCasaOS, the network,
// the boot, the package manager itself). This is the one list of them, as Go: the check of the plan, and the pattern the
// runbook gives to a shell, are made of it, and a test proves they say the same.

// ProtectedNames are the patterns of the names of the packages whose removal blocks the plan. The alphabet is a-z, 0-9,
// "." "+" "-" and "*" (a test enforces it): a pattern means the same to a Go matcher, to path.Match and to a POSIX shell's
// case. A star stands for any run of characters, none included, wherever it is.
var ProtectedNames = []string{
	"docker-ce", "docker-ce-cli", "containerd.io", "docker-*-plugin", "docker.io",
	"samba*", "cifs-utils", "mergerfs", "ntfs-3g", "udevil", "smartmontools", "parted", "net-tools",
	"wget", "curl", "unzip", "git", "openssh-*", "sudo",
	"systemd*", "dbus", "ifupdown", "network-manager", "netplan.io", "dhcpcd", "ca-certificates",
	"linux-image*", "grub*", "shim*", "initramfs-tools",
	"apt", "dpkg", "libc6",
}

// globMatch is whether name is the pattern, where a star is any run of characters.
func globMatch(pattern, name string) bool {
	star := strings.IndexByte(pattern, '*')
	if star < 0 {
		return pattern == name
	}
	head, rest := pattern[:star], pattern[star+1:]
	if !strings.HasPrefix(name, head) {
		return false
	}
	name = name[len(head):]
	if rest == "" {
		return true
	}
	for i := 0; i <= len(name); i++ {
		if globMatch(rest, name[i:]) {
			return true
		}
	}
	return false
}

// Protected says whether the removal of a package blocks the plan, and which pattern says so. The name is a package's as
// apt prints it, with the architecture it has when it is not the native one ("libc6:i386"): the architecture does not
// make another package of it.
func Protected(name string) (pattern string, ok bool) {
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	for _, p := range ProtectedNames {
		if globMatch(p, name) {
			return p, true
		}
	}
	return "", false
}

// ShellCase is the patterns as the pattern list of a POSIX shell's case, ")" included, so that a runbook can write
//
//	case "$name" in <ShellCase()> echo STOP;; esac
//
// A test makes a shell say the same as Protected of every name that it is given.
func ShellCase() string { return strings.Join(ProtectedNames, "|") + ")" }
