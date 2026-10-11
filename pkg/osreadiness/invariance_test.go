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
	want := []string{"AppOperations", "Arch", "EUID", "FreeBytes", "Lstat", "MaintenanceBusy", "Now", "ReadDir", "ReadFile", "ReadHead", "Readlink", "Run", "Stat"}
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
		{"stat", "-fc", "%T", "/sys/fs/cgroup"}, {"findmnt", "-n", "-o", "SOURCE", "/"}, {"findmnt", "-b", "-n", "-o", "SIZE", "-T", "/boot"}, {"findmnt", "-b", "-n", "-o", "SIZE", "-T", "/"},
		{"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/dev/sda1"}, {"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/dev/mapper/vg0-root"}, {"uname", "-r"},
		{"smartctl", "-n", "standby", "-H", "/dev/sda"}, {"modinfo", "-n", "r8168"}, {"dkms", "status"}, {"mokutil", "--sb-state"}, {"pveversion"},
		{"docker", "info", "--format", dockerpkg.DaemonInfoFormat}, {"docker", "ps", "-q"}, {"docker", "--version"},
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
		{"dpkg-query", "-W"}, {"dpkg-query", "--show"}, {"dpkg-query", "-L", "-f=x"}, {"dpkg-query", "--listfiles", "-f=x"}, {"dpkg-query", "-S", "-f=x"}, {"dpkg-query", "--show", installedFormat}, {"dpkg-query", "-W", "libc6"}, {"dpkg-query", "-W", installedFormat, "--admindir=/tmp"}, {"dpkg-query", "-W", installedFormat, "-rf"}, {"dpkg-query", "-W", installedFormat, "a b"},
		{"apt-mark", "hold", "libc6"}, {"apt-mark", "unhold", "libc6"}, {"apt-mark", "showhold"}, {"apt-mark", "auto", "x"},
		{"apt-get", "update"}, {"apt-get", "install", "x"}, {"apt-get", "-s", "upgrade"}, {"apt-get", "check"}, {"apt-get", "-o", "Debug::NoLocking=true", "check"},
		append(slices.Clone(aptCheckArgs), "-y"), {"apt-get", "clean"}, aptPolicyArgs(), aptPolicyArgs("-o"), {"apt-cache", "-o", "Dir::Cache=/tmp", "-o", "a=b", "-o", "c=d", "policy", "docker-ce"}, {"apt-cache", "policy", "docker-ce"}, {"apt-cache", "-o", "Debug::NoLocking=true", "policy", "docker-ce"}, {"apt-cache", "gencaches"},
		{"apt-config", "shell", "x", "y"}, {"apt-config"}, {"systemd-detect-virt"},
		{"systemctl", "show", "x.service", "--property=ActiveState", "--value", "--user"}, {"systemctl", "show", "x.service", "--property=ActiveState", "--value", "extra"}, {"systemctl", "is-active", "ssh.service", "extra"}, {"systemctl", "is-enabled", "a", "b"}, {"systemctl", "is-system-running", "--wait"}, {"systemctl", "is-active"}, {"systemctl", "stop", "docker"}, {"systemctl", "restart", "docker"}, {"systemctl", "reboot"}, {"systemctl", "mask", "x"}, {"systemctl", "daemon-reload"},
		{"systemctl", "show", "x.service", "--property=ActiveState"}, {"systemctl", "show", "x.service", "--property=ActiveState", "--user"}, {"systemctl", "show", "../x", "--property=A", "--value"}, {"systemctl", "show", "x", "--property=a;b", "--value"}, {"systemctl", "is-active", "-x"},
		{"stat", "-fc", "%T", "/a\nb"}, {"stat", "-c", "%T", "/"}, {"stat", "-fc", "%T", "relative"}, {"stat", "-fc", "%T"}, {"stat", "-fc", "%n", "/"}, {"stat", "-fc", "%T", "/", "/boot"}, {"stat", "-f", "/"}, {"stat", "-L", "-fc", "%T", "/"}, {"stat"},
		{"smartctl", "-s", "on", "/dev/sda"}, {"smartctl", "-t", "long", "/dev/sda"}, {"smartctl", "-n", "standby", "-H", "sda"}, {"smartctl", "-n", "standby", "-H", "/dev/sda", "-s", "off"}, {"smartctl", "--scan"},
		// a disk that sleeps is not woken: -H without "-n standby" is not a form of the report, and neither is another mode
		{"smartctl", "-H", "/dev/sda"}, {"smartctl", "-n", "never", "-H", "/dev/sda"}, {"smartctl", "-n", "sleep", "-H", "/dev/sda"}, {"smartctl", "-n", "idle", "-H", "/dev/sda"}, {"smartctl", "-n", "standby", "-H"},
		{"smartctl", "-H", "-n", "standby", "/dev/sda"}, {"smartctl", "-n", "standby,", "-H", "/dev/sda"}, {"smartctl", "-n", "standby", "-H", "/dev/sda", "/dev/sdb"}, {"smartctl", "-n", "standby", "-X", "/dev/sda"},
		{"smartctl", "-n", "standby", "-a", "/dev/sda"}, {"smartctl", "-X", "/dev/sda"}, {"smartctl", "-n", "standby", "-H", "/etc/passwd"}, {"smartctl", "-n", "standby", "-H", "/dev/sda;x"},
		{"modinfo", "-n", "../x"}, {"modinfo", "x"}, {"modinfo", "-k", "x"}, {"modinfo", "-F", "x"}, {"modinfo", "-0", "x"}, {"modinfo", "-k", "x", "y"}, {"modinfo", "-k", "x", "-n", "y"}, {"modinfo", "-n", "x", "y"}, {"modinfo", "-n"}, {"modinfo"}, {"dkms", "install", "x"}, {"dkms", "remove", "x"}, {"mokutil", "--import", "x"}, {"mokutil", "--disable-validation"},
		// the client's version is the one question that asks no daemon, and in its one form
		{"docker", "--version", "x"}, {"docker", "-v"}, {"docker", "version"}, {"docker", "--help"}, {"docker", "--version", "--format", "x"}, {"docker", "--host", "tcp://x:2375", "--version"}, {"docker", "-H", "x", "info"},
		{"docker", "rm", "x"}, {"docker", "run", "x"}, {"docker", "exec", "x", "sh"}, {"docker", "stop", "x"}, {"docker", "system", "prune"}, {"docker"},
		{"docker", "info"}, {"docker", "info", "--format", "{{json .}}"}, {"docker", "ps"}, {"docker", "ps", "-a"}, {"docker", "ps", "-q", "-a"}, {"docker", "inspect", "--format", "x", "0123456789ab"},
		{"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat}, {"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat, "--all"}, {"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat, "-s", "0123456789ab"},
		{"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat, "0123456789A"}, {"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat, "0123456789a"}, {"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat, strings.Repeat("a", 65)},
		{"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat, "../etc/passwd"}, append([]string{"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat}, slices.Repeat([]string{"0123456789ab"}, maxInspected+1)...),
		{"findmnt"}, {"findmnt", "--poll"}, {"findmnt", "--verify"}, {"findmnt", "--verify", "--poll"}, {"findmnt", "--verify", "-v"}, {"findmnt", "-x"}, {"findmnt", "--fstab"}, {"findmnt", "-s"}, {"findmnt", "-w", "10"}, {"findmnt", "-n", "-o", "FSTYPE", "/"}, {"findmnt", "-n", "-o", "SOURCE", "/boot"},
		{"findmnt", "-b", "-n", "-o", "SIZE", "-T", "/boot", "/extra"}, {"findmnt", "-n", "-o", "SOURCE", "/", "/boot"}, {"findmnt", "-b", "-n", "-o", "SIZE", "-T", "relative"}, {"findmnt", "-b", "-n", "-o", "SIZE", "-T", "/a b"}, {"findmnt", "-b", "-n", "-o", "SIZE", "-T", "/x;y"}, {"findmnt", "-b", "-n", "-o", "SIZE", "-T"},
		{"lsblk"}, {"lsblk", "-J"}, {"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE"}, {"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "sda"}, {"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/dev/sda;x"},
		{"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/etc/passwd"}, {"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/dev/sda", "/dev/sdb"}, {"lsblk", "-f", "/dev/sda"},
		{"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/dev/../etc/passwd"}, {"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/dev//sda"}, {"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", "/dev/sda/"},
		{"smartctl", "-n", "standby", "-H", "/dev/../etc/shadow"}, {"smartctl", "-n", "standby", "-H", "/dev/sda/"}, {"smartctl", "-n", "standby", "-H", "/dev//sda"},
		{"snap", "remove", "docker"}, {"snap", "refresh"}, {"snap", "list", "docker", "x"},
	} {
		if Allowed(argv) {
			t.Errorf("%q is allowed", argv)
		}
	}
}

// The options that keep apt-get and apt-cache from taking a lock and from writing the caches they
// build are the only thing between "apt-get check" and a write to /var/cache/apt on a box whose
// caches are stale: they are pinned here as they are written, and not through the constants the
// checks use (a test that builds its expectation from the constant says the same whatever the
// constant says).
func TestTheAptToolsAskTheirQuestionsWithTheOptionsThatKeepThemFromWriting(t *testing.T) {
	check := []string{"apt-get", "-o", "Debug::NoLocking=true", "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=", "check"}
	policyOf := []string{"apt-cache", "-o", "Debug::NoLocking=true", "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=", "policy"}
	if !slices.Equal(aptCheckArgs, check) {
		t.Errorf("aptCheckArgs = %q, want %q", aptCheckArgs, check)
	}
	policy := append(slices.Clone(policyOf), "docker-ce", "containerd.io")
	if got := aptPolicyArgs("docker-ce", "containerd.io"); !slices.Equal(got, policy) {
		t.Errorf("aptPolicyArgs = %q, want %q", got, policy)
	}
	if !Allowed(check) || !Allowed(policy) {
		t.Fatal("the forms of the report are not allowed")
	}
	// not one of the options may go, be turned off, or be given a place to write
	for name, argv := range map[string][]string{
		"locking on":                       {"apt-get", "-o", "Debug::NoLocking=false", "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=", "check"},
		"locking left out":                 {"apt-get", "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=", "check"},
		"pkgcache left out":                {"apt-get", "-o", "Debug::NoLocking=true", "-o", "Dir::Cache::srcpkgcache=", "check"},
		"srcpkgcache left out":             {"apt-get", "-o", "Debug::NoLocking=true", "-o", "Dir::Cache::pkgcache=", "check"},
		"pkgcache given a path":            {"apt-get", "-o", "Debug::NoLocking=true", "-o", "Dir::Cache::pkgcache=/tmp/p.bin", "-o", "Dir::Cache::srcpkgcache=", "check"},
		"srcpkgcache given a path":         {"apt-get", "-o", "Debug::NoLocking=true", "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=/tmp/s.bin", "check"},
		"no options":                       {"apt-get", "check"},
		"policy, locking on":               {"apt-cache", "-o", "Debug::NoLocking=false", "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=", "policy", "docker-ce"},
		"policy, locking left out":         {"apt-cache", "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=", "policy", "docker-ce"},
		"policy, pkgcache left out":        {"apt-cache", "-o", "Debug::NoLocking=true", "-o", "Dir::Cache::srcpkgcache=", "policy", "docker-ce"},
		"policy, srcpkgcache left out":     {"apt-cache", "-o", "Debug::NoLocking=true", "-o", "Dir::Cache::pkgcache=", "policy", "docker-ce"},
		"policy, pkgcache given a path":    {"apt-cache", "-o", "Debug::NoLocking=true", "-o", "Dir::Cache::pkgcache=/x", "-o", "Dir::Cache::srcpkgcache=", "policy", "docker-ce"},
		"policy, srcpkgcache given a path": {"apt-cache", "-o", "Debug::NoLocking=true", "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=/x", "policy", "docker-ce"},
	} {
		if Allowed(argv) {
			t.Errorf("%s: %q is allowed", name, argv)
		}
	}
	// and the checks ask in these forms, whatever the constants are
	f := newBox(t)
	Run(context.Background(), f, Options{Checks: []Check{checkNamed(t, "broken_dpkg"), checkNamed(t, "docker_origin")}})
	var sawCheck, sawPolicy bool
	for _, argv := range f.attemptedCommands() {
		switch argv[0] {
		case "apt-get":
			sawCheck = true
			if !slices.Equal(argv, check) {
				t.Errorf("apt-get was asked %q", argv)
			}
		case "apt-cache":
			sawPolicy = true
			if len(argv) <= len(policyOf) || !slices.Equal(argv[:len(policyOf)], policyOf) {
				t.Errorf("apt-cache was asked %q", argv)
			}
		}
	}
	if !sawCheck || !sawPolicy {
		t.Errorf("apt-get check asked: %v, apt-cache policy asked: %v", sawCheck, sawPolicy)
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

func TestFindmntIsOnlyAskedItsTwoQuestions(t *testing.T) {
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

// apt-cache with the options that keep it from writing is still apt-cache: only its "policy" is allowed.
// The other subcommands (gencaches, add, dumpavail, showpkg...) build, change or print what is not asked for, and
// the options do not make them reads: the word that follows them is what is pinned here, spelled out and not
// taken from the constants the checks use.
func TestAptCacheIsOnlyAskedForItsPolicyWhateverTheOptions(t *testing.T) {
	noWrite := []string{"-o", "Debug::NoLocking=true", "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache="}
	with := func(rest ...string) []string { return append(append([]string{"apt-cache"}, noWrite...), rest...) }
	for _, argv := range [][]string{with("policy", "docker-ce"), with("policy", "docker-ce", "containerd.io")} {
		if !Allowed(argv) {
			t.Errorf("%q is not allowed", argv)
		}
	}
	for _, argv := range [][]string{
		with("gencaches"), with("gencaches", "docker-ce"), with("add", "x"), with("add", "/var/lib/apt/lists/x_Packages"), with("dumpavail"), with("dump"),
		with("showpkg", "docker-ce"), with("show", "docker-ce"), with("search", "docker"), with("depends", "docker-ce"), with("unmet"), with("stats"),
		with("Policy", "docker-ce"), with("policy"), with(), with("-o", "Dir::Cache=/tmp", "policy", "docker-ce"),
	} {
		if Allowed(argv) {
			t.Errorf("%q is allowed", argv)
		}
	}
}
