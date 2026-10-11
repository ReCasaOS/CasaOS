package osreadiness

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// The report changes nothing on the machine. That is a property of the seam, and these tests pin it:
// the Probe has no method that writes, the commands are an allow-list, and no check of the table
// asks for anything that is not on it.

func TestTheProbeCannotWrite(t *testing.T) {
	probe := reflect.TypeOf((*Probe)(nil)).Elem()
	var methods []string
	for i := 0; i < probe.NumMethod(); i++ {
		methods = append(methods, probe.Method(i).Name)
	}
	want := []string{"AppOperations", "Arch", "EUID", "FreeBytes", "Lstat", "MaintenanceBusy", "Now", "ReadDir", "ReadFile", "Readlink", "Run", "Stat"}
	slices.Sort(methods)
	if !slices.Equal(methods, want) {
		t.Fatalf("the Probe has the methods %v, want %v: a new method is a new thing the report may do to the machine, and says why in this list", methods, want)
	}
	for _, name := range methods {
		for _, writes := range []string{"Write", "Create", "Remove", "Delete", "Mkdir", "Chmod", "Chown", "Rename", "Symlink", "Truncate", "Kill", "Start", "Stop", "Set", "Put", "Append", "Touch"} {
			if strings.HasPrefix(name, writes) {
				t.Errorf("%s is a method that writes", name)
			}
		}
	}
}

func TestTheAllowListIsPinned(t *testing.T) {
	want := []string{"apt-cache", "apt-config", "apt-get", "dkms", "docker", "dpkg", "dpkg-query", "findmnt", "lsblk", "modinfo", "mokutil", "pveversion", "smartctl", "snap", "stat", "systemctl", "systemd-detect-virt", "uname"}
	if got := allowedCommands(); !slices.Equal(got, want) {
		t.Fatalf("the allow-list has %v, want %v: a command added here is a command the report runs on every machine", got, want)
	}
}

func TestOnlyTheReadingFormsOfTheAllowedCommandsAreAllowed(t *testing.T) {
	for _, argv := range [][]string{
		{"dpkg", "--audit"}, {"dpkg", "--print-architecture"}, {"dpkg", "--print-foreign-architectures"},
		{"dpkg-query", "-W", installedFormat}, {"dpkg-query", "-W", installedFormat, "firmware-*", "*-microcode", "*:i386", "libc6", "docker-ce:amd64"},
		aptCheckArgs, aptPolicyArgs("docker-ce"), aptPolicyArgs("docker-ce", "containerd.io"), {"apt-config", "dump"},
		{"systemd-detect-virt", "--container"}, {"systemd-detect-virt", "--vm"},
		{"systemctl", "show", "apt-daily.service", "--property=ActiveState", "--value"}, {"systemctl", "is-system-running"}, {"systemctl", "is-active", "ssh.service"}, {"systemctl", "is-enabled", "systemd-resolved"},
		{"stat", "-fc", "%T", "/sys/fs/cgroup"}, {"findmnt", "--verify"}, {"findmnt", "-n", "-o", "SOURCE", "/"}, {"findmnt", "-b", "-n", "-o", "SIZE", "-T", "/boot"}, {"findmnt", "-b", "-n", "-o", "SIZE", "-T", "/"},
		{"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/dev/sda1"}, {"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/dev/mapper/vg0-root"}, {"uname", "-r"},
		{"smartctl", "-H", "/dev/sda"}, {"modinfo", "-n", "r8168"}, {"dkms", "status"}, {"mokutil", "--sb-state"}, {"pveversion"},
		{"docker", "info", "--format", dockerpkg.DaemonInfoFormat}, {"docker", "ps", "-q"},
		{"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat, "0123456789ab"}, {"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat, "0123456789ab", strings.Repeat("f", 64)},
		{"snap", "list"}, {"snap", "list", "docker"},
	} {
		if !Allowed(argv) {
			t.Errorf("%v is not allowed", argv)
		}
	}
	for _, argv := range [][]string{
		nil, {}, {""}, {"dpkg-query", "-W", "-f=a" + string(rune(0)) + "b"}, {"dpkg", "--audit" + string(rune(0))}, {"sh", "-c", "dpkg --audit"}, {"bash"}, {"rm", "-rf", "/"}, {"/usr/bin/dpkg", "--audit"}, {"dpkg --audit"}, {"dpkg\x00", "--audit"},
		{"dpkg", "--configure", "-a"}, {"dpkg", "-i", "x.deb"}, {"dpkg", "--audit", "--root=/tmp"}, {"dpkg", "-r", "libc6"},
		{"dpkg-query", "-W"}, {"dpkg-query", "--show"}, {"dpkg-query", "-W", "libc6"}, {"dpkg-query", "-W", installedFormat, "--admindir=/tmp"}, {"dpkg-query", "-W", installedFormat, "-rf"}, {"dpkg-query", "-W", installedFormat, "a b"},
		{"apt-mark", "hold", "libc6"}, {"apt-mark", "unhold", "libc6"}, {"apt-mark", "showhold"}, {"apt-mark", "auto", "x"},
		{"apt-get", "update"}, {"apt-get", "install", "x"}, {"apt-get", "-s", "upgrade"}, {"apt-get", "check"}, {"apt-get", "-o", "Debug::NoLocking=true", "check"},
		append(slices.Clone(aptCheckArgs), "-y"), {"apt-get", "clean"}, aptPolicyArgs(), aptPolicyArgs("-o"), {"apt-cache", "-o", "Dir::Cache=/tmp", "-o", "a=b", "-o", "c=d", "policy", "docker-ce"}, {"apt-cache", "policy", "docker-ce"}, {"apt-cache", "-o", "Debug::NoLocking=true", "policy", "docker-ce"}, {"apt-cache", "gencaches"},
		{"apt-config", "shell", "x", "y"}, {"apt-config"}, {"systemd-detect-virt"},
		{"systemctl", "stop", "docker"}, {"systemctl", "restart", "docker"}, {"systemctl", "reboot"}, {"systemctl", "mask", "x"}, {"systemctl", "daemon-reload"},
		{"systemctl", "show", "x.service", "--property=ActiveState"}, {"systemctl", "show", "x.service", "--property=ActiveState", "--user"}, {"systemctl", "show", "../x", "--property=A", "--value"}, {"systemctl", "show", "x", "--property=a;b", "--value"}, {"systemctl", "is-active", "-x"},
		{"stat", "-c", "%T", "/"}, {"stat", "-fc", "%T", "relative"}, {"stat", "-fc", "%T"},
		{"smartctl", "-s", "on", "/dev/sda"}, {"smartctl", "-t", "long", "/dev/sda"}, {"smartctl", "-H", "sda"}, {"smartctl", "-H", "/dev/sda", "-s", "off"}, {"smartctl", "--scan"},
		{"modinfo", "-n", "../x"}, {"modinfo", "x"}, {"dkms", "install", "x"}, {"dkms", "remove", "x"}, {"mokutil", "--import", "x"}, {"mokutil", "--disable-validation"},
		{"docker", "rm", "x"}, {"docker", "run", "x"}, {"docker", "exec", "x", "sh"}, {"docker", "stop", "x"}, {"docker", "system", "prune"}, {"docker"},
		{"docker", "info"}, {"docker", "info", "--format", "{{json .}}"}, {"docker", "ps"}, {"docker", "ps", "-a"}, {"docker", "ps", "-q", "-a"}, {"docker", "inspect", "--format", "x", "0123456789ab"},
		{"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat}, {"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat, "--all"}, {"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat, "-s", "0123456789ab"},
		{"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat, "0123456789A"}, {"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat, "0123456789a"}, {"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat, strings.Repeat("a", 65)},
		{"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat, "../etc/passwd"}, append([]string{"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat}, slices.Repeat([]string{"0123456789ab"}, maxInspected+1)...),
		{"findmnt"}, {"findmnt", "--poll"}, {"findmnt", "--verify", "--poll"}, {"findmnt", "-w", "10"}, {"findmnt", "-n", "-o", "FSTYPE", "/"}, {"findmnt", "-n", "-o", "SOURCE", "/boot"},
		{"findmnt", "-b", "-n", "-o", "SIZE", "-T", "relative"}, {"findmnt", "-b", "-n", "-o", "SIZE", "-T", "/a b"}, {"findmnt", "-b", "-n", "-o", "SIZE", "-T", "/x;y"}, {"findmnt", "-b", "-n", "-o", "SIZE", "-T"},
		{"lsblk"}, {"lsblk", "-J"}, {"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE"}, {"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "sda"}, {"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/dev/sda;x"},
		{"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/etc/passwd"}, {"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/dev/sda", "/dev/sdb"}, {"lsblk", "-f", "/dev/sda"},
		{"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/dev/../etc/passwd"}, {"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/dev//sda"}, {"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/dev/sda/"},
		{"smartctl", "-H", "/dev/../etc/shadow"}, {"smartctl", "-H", "/dev/sda/"}, {"smartctl", "-H", "/dev//sda"},
		{"snap", "remove", "docker"}, {"snap", "refresh"}, {"snap", "list", "docker", "x"},
	} {
		if Allowed(argv) {
			t.Errorf("%q is allowed", argv)
		}
	}
}

func TestACheckThatAsksForACommandThatIsNotAllowedDoesNotGetIt(t *testing.T) {
	f := newBox(t)
	asked := Check{Code: "c", Title: "c", Run: func(ctx context.Context, p Probe) Result {
		_, err := p.Run(ctx, "apt-get", "update")
		return unknown(err)
	}}
	report := Run(context.Background(), f, Options{Checks: []Check{asked}})
	if got := findingOf(t, report, "c"); got.Data["reason"] != ReasonNotAllowed {
		t.Fatalf("finding = %+v", got)
	}
	if len(f.attemptedCommands()) != 0 {
		t.Fatalf("the machine was asked %v", f.attemptedCommands())
	}
}

func TestFindmntIsOnlyAskedItsThreeQuestions(t *testing.T) {
	for _, argv := range [][]string{
		{"findmnt", "--poll", "-n", "-o", "SIZE", "/boot"}, {"findmnt", "-w", "10", "-o", "SIZE", "-T", "/boot"}, {"findmnt", "-x", "-n", "-o", "SIZE", "-T", "/boot"},
		{"findmnt", "-b", "-n", "-o", "SIZE", "-S", "/boot"}, {"findmnt", "-b", "-n", "-o", "AVAIL", "-T", "/boot"}, {"findmnt", "-b", "-n", "--poll", "SIZE", "-T", "/boot"},
	} {
		if Allowed(argv) {
			t.Errorf("%q is allowed", argv)
		}
	}
}

// The names and the devices that a path, or a command's argument, is made of: no step that goes
// back up or stays where it is, and nothing outside the alphabet.
func TestNamesAndDevicesThatMakeAPathAreStrict(t *testing.T) {
	for name, ok := range map[string]bool{"enp3s0": true, "wlan0": true, "veth.1_a-b": true, ".": false, "..": false, "": false, "a/b": false, "a b": false, "-x": true, strings.Repeat("a", 16): false} {
		if got := validName(interfaceName, name); got != ok {
			t.Errorf("validName(interfaceName, %q) = %v, want %v", name, got, ok)
		}
	}
	for device, ok := range map[string]bool{
		"/dev/sda": true, "/dev/sda1": true, "/dev/mapper/vg0-root": true, "/dev/nvme0n1p2": true, "/dev/disk/by-id/ata-X_1": true,
		"/dev/../etc/passwd": false, "/dev/sda/../sdb": false, "/dev//sda": false, "/dev/sda/": false, "/dev/": false, "/dev": false, "/etc/passwd": false, "dev/sda": false, "/dev/sd a": false, "/dev/sda;x": false, "/dev/sda\n": false,
	} {
		if got := validDevice(device); got != ok {
			t.Errorf("validDevice(%q) = %v, want %v", device, got, ok)
		}
	}
}
