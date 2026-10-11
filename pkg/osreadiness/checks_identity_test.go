package osreadiness

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"
)

func TestUnsupportedOS(t *testing.T) {
	runScenarios(t, "unsupported_os", []scenario{
		{name: "Debian 11 is one the upgrade is for", severity: OK},
		{name: "Ubuntu 24.04 is one the upgrade is for", setup: ubuntu2404, severity: OK},
		{name: "a system that is not Debian or Ubuntu", setup: func(f *fakeProbe) {
			f.set("/etc/os-release", "ID=arch\nVERSION_ID=\"rolling\"\n")
		}, severity: Refuse, data: D{"id": "arch"}, title: "This is not Debian or Ubuntu"},
		{name: "a derivative says what it is, and is not Debian", setup: func(f *fakeProbe) {
			f.set("/etc/os-release", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"21.3\"\nVERSION_CODENAME=virginia\n")
		}, severity: Refuse, data: D{"id": "linuxmint", "version_id": "21.3"}, title: "This is not Debian or Ubuntu"},
		{name: "a release that is not an LTS of Ubuntu", setup: func(f *fakeProbe) {
			f.set("/etc/os-release", osRelease("ubuntu", "25.10", "questing", "Ubuntu 25.10"))
		}, severity: Refuse, data: D{"id": "ubuntu", "version_id": "25.10"}, title: "There is no upgrade from this release"},
		{name: "Debian without a version", setup: func(f *fakeProbe) {
			f.set("/etc/os-release", "ID=debian\n")
		}, severity: Refuse, data: D{"id": "debian"}},
		{name: "Ubuntu 20.04, from which there is no upgrade", setup: func(f *fakeProbe) {
			f.set("/etc/os-release", osRelease("ubuntu", "20.04", "focal", "Ubuntu 20.04.6 LTS"))
		}, severity: Refuse, data: D{"id": "ubuntu", "version_id": "20.04"}},
		{name: "a variant is the business of vendor_os", setup: func(f *fakeProbe) { f.set("/etc/rpi-issue", "x") }, severity: OK},
		{name: "an os-release that cannot be read is not a Debian", setup: func(f *fakeProbe) {
			f.fail("/etc/os-release", &fs.PathError{Op: "open", Path: "/etc/os-release", Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
		{name: "no os-release at all", setup: func(f *fakeProbe) { f.remove("/etc/os-release") }, severity: Refuse, unknown: ReasonMissing},
		// the table of the releases was written on a day, and a box that says an earlier one has a clock
		// that is wrong (a dead battery, no time server): that is what the owner is told, and not that
		// there is no upgrade from a Debian 11
		{name: "a clock a day before the table", setup: func(f *fakeProbe) { f.now = time.Date(2026, 10, 8, 23, 59, 59, 0, time.UTC) }, severity: Refuse, unknown: ReasonClock, title: "date", data: D{"id": "debian", "version_id": "11"}},
		{name: "a clock of 1970", setup: func(f *fakeProbe) { f.now = time.Unix(0, 0) }, severity: Refuse, unknown: ReasonClock},
		{name: "a clock on the day of the table", setup: func(f *fakeProbe) { f.now = time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC) }, severity: OK},
		{name: "a clock that is wrong, on a release the table has no upgrade from", setup: func(f *fakeProbe) {
			f.set("/etc/os-release", osRelease("ubuntu", "25.10", "questing", "Ubuntu 25.10"))
			f.now = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		}, severity: Refuse, unknown: ReasonClock},
		{name: "a clock that is wrong says nothing of a system that is not Debian", setup: func(f *fakeProbe) {
			f.set("/etc/os-release", "ID=arch\nVERSION_ID=\"rolling\"\n")
			f.now = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		}, severity: Refuse, title: "This is not Debian or Ubuntu"},
	})
}

// What the owner reads when the clock is wrong is that it is, in words that say so.
func TestAClockThatIsWrongIsSaidAsSuch(t *testing.T) {
	f := newBox(t)
	f.now = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	got := checkOn(t, f, "unsupported_os")
	if got.Data["reason"] != "clock" {
		t.Fatalf("data = %v", got.Data)
	}
	if text := got.Title + " " + got.Detail; !strings.Contains(text, "date") || strings.Contains(text, "no upgrade") || !strings.Contains(got.Detail, "2026-10-09") {
		t.Fatalf("title %q, detail %q", got.Title, got.Detail)
	}
	if strings.HasPrefix(got.Title, "Could not check") {
		t.Fatalf("the title is the generic one: %q", got.Title)
	}
}

func TestVendorOS(t *testing.T) {
	marker := func(path string) func(*fakeProbe) { return func(f *fakeProbe) { f.set(path, "x") } }
	runScenarios(t, "vendor_os", []scenario{
		{name: "a plain Debian", severity: OK},
		{name: "Raspberry Pi OS", setup: marker("/etc/rpi-issue"), severity: Refuse, data: D{"variant": "raspberrypi"}},
		{name: "Armbian by its file", setup: marker("/etc/armbian-release"), severity: Refuse, data: D{"variant": "armbian"}},
		{name: "DietPi", setup: marker("/boot/dietpi.txt"), severity: Refuse, data: D{"variant": "dietpi"}},
		{name: "Proxmox VE", setup: marker("/usr/bin/pveversion"), severity: Refuse, data: D{"variant": "proxmox"}},
		{name: "OpenMediaVault", setup: marker("/etc/openmediavault/config.xml"), severity: Refuse, data: D{"variant": "openmediavault"}},
		{name: "Proxmox Backup Server, by its manager", setup: marker("/usr/sbin/proxmox-backup-manager"), severity: Refuse, data: D{"variant": "proxmox_backup"}, title: "Proxmox Backup Server"},
		{name: "Proxmox Backup Server, by its directory", setup: marker("/etc/proxmox-backup/datastore.cfg"), severity: Refuse, data: D{"variant": "proxmox_backup"}},
		{name: "Proxmox Mail Gateway, by pmgversion", setup: marker("/usr/bin/pmgversion"), severity: Refuse, data: D{"variant": "proxmox_mail"}, title: "Proxmox Mail Gateway"},
		{name: "Proxmox Mail Gateway, by its directory", setup: marker("/etc/pmg/pmg.conf"), severity: Refuse, data: D{"variant": "proxmox_mail"}},
		{name: "TrueNAS SCALE, by its middleware", setup: marker("/usr/bin/middlewared"), severity: Refuse, data: D{"variant": "truenas"}, title: "TrueNAS"},
		{name: "TrueNAS SCALE, by its python package", setup: marker("/usr/lib/python3/dist-packages/middlewared/main.py"), severity: Refuse, data: D{"variant": "truenas"}},
		{name: "Proxmox VE is Proxmox VE", setup: marker("/usr/bin/pveversion"), severity: Refuse, data: D{"variant": "proxmox"}, title: "Proxmox VE"},
		{name: "Armbian by its os-release", setup: func(f *fakeProbe) {
			f.set("/etc/os-release", "ID=debian\nVERSION_ID=\"12\"\nVERSION_CODENAME=bookworm\nPRETTY_NAME=\"Armbian 24.2.1 bookworm\"\n")
		}, severity: Refuse, data: D{"variant": "armbian"}},
		{name: "a marker that cannot be looked at", setup: func(f *fakeProbe) {
			f.fail("/usr/bin/pveversion", &fs.PathError{Op: "stat", Path: "/usr/bin/pveversion", Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
	})
}

func TestUnprovenArch(t *testing.T) {
	runScenarios(t, "unproven_arch", []scenario{
		{name: "amd64", severity: OK},
		{name: "arm64", setup: func(f *fakeProbe) { f.arch = "arm64" }, severity: Refuse, data: D{"arch": "arm64"}},
		{name: "a 32-bit core", setup: func(f *fakeProbe) { f.arch = "arm" }, severity: Refuse, data: D{"arch": "arm"}},
		{name: "a 64-bit core on a 32-bit system", setup: func(f *fakeProbe) { f.on("i386\n", 0, argvArch...) }, severity: Refuse, data: D{"arch": "i386"}},
		{name: "dpkg says something that is not an architecture", setup: func(f *fakeProbe) { f.on("<b>x</b>\n", 0, argvArch...) }, severity: Refuse},
		{name: "no dpkg is for unsupported_os to say", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvArch...) }, severity: OK},
		{name: "dpkg that does not answer", setup: func(f *fakeProbe) { f.onError(context.DeadlineExceeded, argvArch...) }, severity: Refuse, unknown: ReasonTimeout},
		{name: "dpkg that fails", setup: func(f *fakeProbe) { f.on("", 2, argvArch...) }, severity: Refuse, unknown: ReasonFailed},
	})
}

func TestContainer(t *testing.T) {
	runScenarios(t, "container", []scenario{
		{name: "a machine", severity: OK},
		{name: "systemd says docker", setup: func(f *fakeProbe) { f.on("docker\n", 0, argvContainer...) }, severity: Refuse, data: D{"kind": "docker"}},
		{name: "systemd says lxc", setup: func(f *fakeProbe) { f.on("lxc\n", 0, argvContainer...) }, severity: Refuse, data: D{"kind": "lxc"}},
		{name: "systemd says wsl", setup: func(f *fakeProbe) { f.on("wsl\n", 0, argvContainer...) }, severity: Refuse, data: D{"kind": "wsl"}},
		{name: "systemd says something odd", setup: func(f *fakeProbe) { f.on("<b>x</b>\n", 0, argvContainer...) }, severity: Refuse, data: D{"kind": "container"}},
		{name: "systemd says none, and does not fail", setup: func(f *fakeProbe) { f.on("none"+string(rune(10)), 0, argvContainer...) }, severity: OK},
		{name: "systemd says none, and fails, as it does", setup: func(f *fakeProbe) { f.on("none\n", 1, argvContainer...) }, severity: OK},
		{name: "systemd says nothing but names a kind in a failure", setup: func(f *fakeProbe) { f.on("docker\n", 1, argvContainer...) }, severity: OK},
		{name: "no systemd-detect-virt and a .dockerenv", setup: func(f *fakeProbe) {
			f.onError(ErrToolMissing, argvContainer...)
			f.set("/.dockerenv", "")
		}, severity: Refuse, data: D{"kind": "docker"}},
		{name: "a .dockerenv that systemd does not see", setup: func(f *fakeProbe) { f.set("/.dockerenv", "") }, severity: Refuse, data: D{"kind": "docker"}},
		{name: "podman's marker", setup: func(f *fakeProbe) { f.set("/run/.containerenv", "") }, severity: Refuse, data: D{"kind": "podman"}},
		{name: "the control group of PID 1 in a container", setup: func(f *fakeProbe) { f.set("/proc/1/cgroup", "12:cpu:/docker/3f2a\n0::/\n") }, severity: Refuse, data: D{"kind": "container"}},
		{name: "the control group of PID 1 in kubernetes", setup: func(f *fakeProbe) { f.set("/proc/1/cgroup", "0::/kubepods/besteffort/pod1\n") }, severity: Refuse},
		{name: "the control group of PID 1 on a machine", setup: func(f *fakeProbe) { f.set("/proc/1/cgroup", "1:name=systemd:/init.scope\n0::/init.scope\n") }, severity: OK},
		{name: "a WSL kernel", setup: func(f *fakeProbe) { f.set("/proc/sys/kernel/osrelease", "5.15.153.1-microsoft-standard-WSL2\n") }, severity: Refuse, data: D{"kind": "wsl"}},
		{name: "a WSL1 kernel", setup: func(f *fakeProbe) { f.set("/proc/sys/kernel/osrelease", "4.4.0-19041-Microsoft\n") }, severity: Refuse, data: D{"kind": "wsl"}},
		{name: "a kernel of Microsoft's that does not say WSL", setup: func(f *fakeProbe) { f.set("/proc/sys/kernel/osrelease", "5.15.0-microsoft-standard\n") }, severity: Refuse, data: D{"kind": "wsl"}},
		{name: "systemd-detect-virt that does not answer", setup: func(f *fakeProbe) { f.onError(context.DeadlineExceeded, argvContainer...) }, severity: Refuse, unknown: ReasonTimeout},
		{name: "a marker that cannot be looked at", setup: func(f *fakeProbe) {
			f.fail("/.dockerenv", &fs.PathError{Op: "stat", Path: "/.dockerenv", Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
		{name: "a cgroup file that cannot be read", setup: func(f *fakeProbe) {
			f.fail("/proc/1/cgroup", &fs.PathError{Op: "open", Path: "/proc/1/cgroup", Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
	})
}

func TestXen(t *testing.T) {
	runScenarios(t, "virt_xen", []scenario{
		{name: "a KVM guest", severity: OK},
		{name: "no virtualization", setup: func(f *fakeProbe) { f.on("none\n", 1, argvVM...) }, severity: OK},
		{name: "a Xen guest", setup: func(f *fakeProbe) { f.on("xen\n", 0, argvVM...) }, severity: Refuse},
		{name: "xen in a failure is not a Xen guest", setup: func(f *fakeProbe) { f.on("xen\n", 1, argvVM...) }, severity: OK},
		{name: "no systemd-detect-virt, and the hypervisor says xen", setup: func(f *fakeProbe) {
			f.onError(ErrToolMissing, argvVM...)
			f.set("/sys/hypervisor/type", "xen\n")
		}, severity: Refuse},
		{name: "no systemd-detect-virt, and no hypervisor", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvVM...) }, severity: OK},
		{name: "no systemd-detect-virt, and another hypervisor", setup: func(f *fakeProbe) {
			f.onError(ErrToolMissing, argvVM...)
			f.set("/sys/hypervisor/type", "kvm\n")
		}, severity: OK},
		{name: "systemd-detect-virt that fails to run", setup: func(f *fakeProbe) { f.onError(errors.New("exec format error"), argvVM...) }, severity: Refuse, unknown: ReasonFailed},
	})
}

func TestNoSystemd(t *testing.T) {
	runScenarios(t, "no_systemd", []scenario{
		{name: "systemd is PID 1", severity: OK},
		{name: "another init", setup: func(f *fakeProbe) { f.set("/proc/1/comm", "init\n") }, severity: Refuse},
		{name: "a process that is not an init at all", setup: func(f *fakeProbe) { f.set("/proc/1/comm", "bash\n") }, severity: Refuse},
		{name: "a PID 1 that cannot be read", setup: func(f *fakeProbe) { f.remove("/proc/1/comm") }, severity: Refuse, unknown: ReasonMissing},
	})
}

func TestNotRoot(t *testing.T) {
	runScenarios(t, "not_root", []scenario{
		{name: "root", severity: OK},
		{name: "a user", setup: func(f *fakeProbe) { f.euid = 1000 }, severity: Refuse},
	})
}
