package osreadiness

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
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
		{"stat", "-fc", "%T", "/sys/fs/cgroup"}, {"findmnt", "--verify"}, {"findmnt", "-n", "-o", "FSTYPE", "/"}, {"lsblk", "-J"}, {"uname", "-r"},
		{"smartctl", "-H", "/dev/sda"}, {"modinfo", "-n", "r8168"}, {"dkms", "status"}, {"mokutil", "--sb-state"}, {"pveversion"},
		{"docker", "info", "--format", "{{json .}}"}, {"docker", "ps", "-q"}, {"docker", "inspect", "--format", "x", "0123456789ab"}, {"snap", "list"}, {"snap", "list", "docker"},
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
