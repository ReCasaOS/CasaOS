package osreadiness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"unicode/utf16"
)

// efiString is the file of an EFI string variable in efivarfs: four bytes of attributes, then the text in UTF-16, little
// endian, and a NUL at the end.
func efiString(text string) string {
	out := []byte{0x06, 0, 0, 0}
	for _, unit := range utf16.Encode([]rune(text + "\x00")) {
		out = append(out, byte(unit), byte(unit>>8))
	}
	return string(out)
}

const loaderInfoPath = "/sys/firmware/efi/efivars/LoaderInfo-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"

func TestMergedUsr(t *testing.T) {
	directory := func(dirs ...string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			for _, dir := range dirs {
				f.remove(dir)
				f.set(dir+"/sh", "x")
			}
		}
	}
	linkTo := func(dir, target string) func(*fakeProbe) {
		return func(f *fakeProbe) { f.link(dir, target) }
	}
	runScenarios(t, "merged_usr", []scenario{
		{name: "the three are links into /usr", severity: OK},
		{name: "absolute links are the same", setup: after(linkTo("/bin", "/usr/bin"), linkTo("/lib", "/usr/lib")), severity: OK},
		{name: "a link that climbs out and back", setup: linkTo("/sbin", "../usr/sbin"), severity: OK},
		{name: "/bin is a directory", setup: directory("/bin"), severity: Refuse, data: D{"directories": []string{"/bin"}}},
		{name: "all three are directories", setup: directory("/bin", "/sbin", "/lib"), severity: Refuse, data: D{"directories": []string{"/bin", "/sbin", "/lib"}}},
		{name: "a link that goes somewhere else", setup: linkTo("/lib", "/opt/lib"), severity: Refuse, data: D{"directories": []string{"/lib"}}},
		{name: "a link to the wrong directory of /usr", setup: linkTo("/bin", "usr/sbin"), severity: Refuse, data: D{"directories": []string{"/bin"}}},
		{name: "a link that is not where it seems", setup: linkTo("/bin", "usr/bin/../../etc"), severity: Refuse},
		{name: "no /sbin at all", setup: func(f *fakeProbe) { f.remove("/sbin") }, severity: Refuse, unknown: ReasonMissing},
		{name: "a directory that cannot be looked at", setup: func(f *fakeProbe) { f.fail("/lib", &fs.PathError{Op: "lstat", Path: "/lib", Err: fs.ErrPermission}) }, severity: Refuse, unknown: ReasonUnreadable},
	})
}

func TestCgroup1(t *testing.T) {
	cgroup := func(kind string, exit int) func(*fakeProbe) {
		return func(f *fakeProbe) { f.on(kind, exit, argvCgroup...) }
	}
	runScenarios(t, "cgroup1", []scenario{
		{name: "Debian 12 on version 2", setup: debian12, severity: OK},
		{name: "Debian 12 on version 1 or the hybrid mount", setup: after(debian12, cgroup("tmpfs\n", 0)), severity: Refuse, data: D{"filesystem": "tmpfs"}},
		{name: "Ubuntu 24.04 on cgroupfs", setup: after(ubuntu2404, cgroup("cgroupfs\n", 0)), severity: Refuse, data: D{"filesystem": "cgroupfs"}},
		{name: "Ubuntu 24.04 on version 2", setup: ubuntu2404, severity: OK},
		{name: "an answer that is not a name", setup: after(debian12, cgroup("UNKNOWN (0x1234)\n", 0)), severity: Refuse, data: D{}},
		{name: "stat that fails", setup: after(debian12, cgroup("", 1)), severity: Refuse, unknown: ReasonFailed},
		{name: "no stat", setup: after(debian12, func(f *fakeProbe) { f.onError(ErrToolMissing, argvCgroup...) }), severity: Refuse, unknown: ReasonToolMissing},
	})
	// the hops that already run version 2 have no use for it
	for _, setup := range []func(*fakeProbe){nil, ubuntu2204} {
		f := newBox(t)
		if setup != nil {
			setup(f)
		}
		f.on("tmpfs\n", 0, argvCgroup...)
		for _, finding := range Run(context.Background(), f, Options{}).Checks {
			if finding.Code == "cgroup1" {
				t.Errorf("cgroup1 ran on a hop that is not 12 to 13 or 24.04 to 26.04")
			}
		}
	}
}

// uefi turns the box into one that started with UEFI.
func uefi(f *fakeProbe) { f.set("/sys/firmware/efi/fw_platform_size", "64\n") }

// espMounted is the EFI system partition, mounted where Debian and Ubuntu mount it.
func espMounted(f *fakeProbe) {
	f.set("/proc/self/mountinfo", rootMountInfo+"30 22 8:15 / /boot/efi rw,relatime shared:2 - vfat /dev/sda15 rw,fmask=0077\n")
}

// debconfFile is debconf's database with the questions of grub-pc as they are given.
func debconfFile(devices string, empty string) string {
	var out strings.Builder
	out.WriteString("Name: debconf/frontend\nTemplate: debconf/frontend\nValue: Noninteractive\nOwners: debconf\n\n")
	if devices != "-" {
		out.WriteString("Name: grub-pc/install_devices\nTemplate: grub-pc/install_devices\nValue: " + devices + "\nOwners: grub-pc\nFlags: seen\n\n")
	}
	if empty != "" {
		out.WriteString("Name: grub-pc/install_devices_empty\nTemplate: grub-pc/install_devices_empty\nValue: " + empty + "\nOwners: grub-pc\n\n")
	}
	return out.String()
}

// grubPC is a machine that has the grub-pc of a BIOS machine, and the questions it asks.
func grubPC(devices, empty string) func(*fakeProbe) {
	return func(f *fakeProbe) {
		f.installed(pkg("grub-pc", "2.06-3~deb11u6")+pkg("grub-pc-bin", "2.06-3~deb11u6"), bootloaderPackages...)
		f.set(debconfDB, debconfFile(devices, empty))
		f.set("/dev/disk/by-id/ata-QEMU_HARDDISK_QM00001", "")
	}
}

func TestBootloader(t *testing.T) {
	const disk = "/dev/disk/by-id/ata-QEMU_HARDDISK_QM00001"
	freeESP := func(bytes uint64) func(*fakeProbe) { return func(f *fakeProbe) { f.free["/boot/efi"] = bytes } }
	runScenarios(t, "bootloader", []scenario{
		{name: "Debian's cloud image: GRUB with no grub-pc, started by BIOS", severity: OK},
		{name: "no GRUB package", setup: func(f *fakeProbe) { f.installed("", bootloaderPackages...) }, severity: Refuse, data: D{"kind": "not_grub"}},
		{name: "GRUB, and no configuration of it", setup: func(f *fakeProbe) { f.remove("/boot/grub/grub.cfg") }, severity: Refuse, data: D{"kind": "not_grub"}},
		{name: "a package that is not one of GRUB's", setup: func(f *fakeProbe) { f.installed(pkg("grub-efi-amd64-bin", "2.06"), bootloaderPackages...) }, severity: Refuse, data: D{"kind": "not_grub"}},
		// systemd-boot starts the machine when its package is there, or its loader.conf, or the variable it sets
		// in the firmware: GRUB's package and its grub.cfg can be left behind, and then GRUB is not what starts it
		{name: "systemd-boot, and no GRUB", setup: func(f *fakeProbe) { f.installed(pkg("systemd-boot", "252"), bootloaderPackages...) }, severity: Refuse, data: D{"kind": "systemd_boot"}, title: "systemd-boot"},
		{name: "systemd-boot, and GRUB's package and grub.cfg left behind", setup: func(f *fakeProbe) {
			f.installed(pkg("systemd-boot", "252")+pkg("grub-efi-amd64", "2.06"), bootloaderPackages...)
		}, severity: Refuse, data: D{"kind": "systemd_boot"}},
		{name: "systemd-boot by its loader.conf, on /boot", setup: after(uefi, espMounted, func(f *fakeProbe) { f.set("/boot/loader/loader.conf", "default debian\n") }), severity: Refuse, data: D{"kind": "systemd_boot"}},
		{name: "systemd-boot by its loader.conf, on the ESP at /boot/efi", setup: after(uefi, espMounted, func(f *fakeProbe) { f.set("/boot/efi/loader/loader.conf", "default debian\n") }), severity: Refuse, data: D{"kind": "systemd_boot"}},
		{name: "systemd-boot by its loader.conf, on the ESP at /efi", setup: after(uefi, espMounted, func(f *fakeProbe) { f.set("/efi/loader/loader.conf", "default debian\n") }), severity: Refuse, data: D{"kind": "systemd_boot"}},
		{name: "systemd-boot by the variable it sets", setup: after(uefi, espMounted, func(f *fakeProbe) {
			f.set(loaderInfoPath, efiString("systemd-boot 252.22-1~deb12u1"))
		}), severity: Refuse, data: D{"kind": "systemd_boot"}},
		// the variable is the Boot Loader Interface's: GRUB 2.12 writes it too (its bli module), with its own name
		{name: "the variable GRUB 2.12 sets is not systemd-boot's", setup: after(uefi, espMounted, func(f *fakeProbe) {
			f.set(loaderInfoPath, efiString("GRUB 2.12-1ubuntu7"))
		}), severity: OK},
		{name: "a variable of another loader that mentions systemd-boot is not systemd-boot's", setup: after(uefi, espMounted, func(f *fakeProbe) {
			f.set(loaderInfoPath, efiString("rEFInd 0.14 (not systemd-boot)"))
		}), severity: OK},
		{name: "the name has to start the variable", setup: after(uefi, espMounted, func(f *fakeProbe) { f.set(loaderInfoPath, efiString("xsystemd-boot 252")) }), severity: OK},
		{name: "a variable with no NUL at its end", setup: after(uefi, espMounted, func(f *fakeProbe) {
			f.set(loaderInfoPath, strings.TrimSuffix(efiString("systemd-boot 252"), "\x00\x00"))
		}), severity: Refuse, data: D{"kind": "systemd_boot"}},
		{name: "a variable that is only its attributes", setup: after(uefi, espMounted, func(f *fakeProbe) { f.set(loaderInfoPath, "\x06\x00\x00\x00") }), severity: OK},
		{name: "a variable that is shorter than its attributes", setup: after(uefi, espMounted, func(f *fakeProbe) { f.set(loaderInfoPath, "\x06") }), severity: OK},
		{name: "a variable of an odd number of bytes", setup: after(uefi, espMounted, func(f *fakeProbe) { f.set(loaderInfoPath, efiString("GRUB 2.12")+"x") }), severity: OK},
		{name: "systemd-boot's text in ASCII, with no attributes, is not what the firmware writes", setup: after(uefi, espMounted, func(f *fakeProbe) { f.set(loaderInfoPath, "systemd-boot 252") }), severity: OK},
		{name: "GRUB's variable and systemd-boot's package", setup: after(uefi, espMounted, func(f *fakeProbe) {
			f.set(loaderInfoPath, efiString("GRUB 2.12-1ubuntu7"))
			f.installed(pkg("systemd-boot", "252")+pkg("grub-efi-amd64", "2.06"), bootloaderPackages...)
		}), severity: Refuse, data: D{"kind": "systemd_boot"}},
		{name: "the variable that cannot be read", setup: after(uefi, espMounted, func(f *fakeProbe) {
			f.set(loaderInfoPath, efiString("GRUB 2.12"))
			f.fail(loaderInfoPath, &fs.PathError{Op: "read", Path: loaderInfoPath, Err: errors.New("input/output error")})
		}), severity: Refuse, unknown: ReasonFailed},
		{name: "the variable that is too large", setup: after(uefi, espMounted, func(f *fakeProbe) { f.set(loaderInfoPath, strings.Repeat("x", MaxFileBytes+1)) }), severity: Refuse, unknown: ReasonTooLarge},
		{name: "a variable of another loader is not systemd-boot's", setup: after(uefi, espMounted, func(f *fakeProbe) {
			f.set("/sys/firmware/efi/efivars/LoaderEntries-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f", "x")
		}), severity: OK},
		{name: "a loader.conf that cannot be looked at", setup: func(f *fakeProbe) {
			f.fail("/boot/loader/loader.conf", &fs.PathError{Op: "stat", Path: "x", Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
		{name: "the variable of systemd-boot that cannot be looked at", setup: func(f *fakeProbe) {
			f.fail(loaderInfoPath, &fs.PathError{Op: "stat", Path: "x", Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
		{name: "BIOS, grub-pc with its disk", setup: grubPC(disk, ""), severity: OK},
		{name: "BIOS, grub-pc with two disks, both there", setup: after(grubPC(disk+", /dev/sda", ""), func(f *fakeProbe) { f.set("/dev/sda", "") }), severity: OK},
		{name: "BIOS, grub-pc with no disk", setup: grubPC("", ""), severity: Refuse, data: D{"kind": "install_devices_empty"}},
		{name: "BIOS, grub-pc with no disk and the question that says it is meant", setup: grubPC("", "true"), severity: OK},
		{name: "BIOS, grub-pc with no disk and that question answered false", setup: grubPC("", "false"), severity: Refuse, data: D{"kind": "install_devices_empty"}},
		{name: "BIOS, grub-pc that debconf has no question of", setup: grubPC("-", ""), severity: Refuse, data: D{"kind": "install_devices_empty"}},
		{name: "BIOS, grub-pc with a disk that is gone", setup: grubPC("/dev/disk/by-id/ata-GONE", ""), severity: Refuse, data: D{"kind": "install_device_missing", "count": 1}},
		{name: "BIOS, grub-pc with one disk there and one gone", setup: grubPC(disk+", /dev/disk/by-id/ata-GONE", ""), severity: Refuse, data: D{"kind": "install_device_missing", "count": 2}},
		{name: "BIOS, grub-pc with a value that is no device", setup: grubPC("not a device", ""), severity: Refuse, data: D{"kind": "install_device_missing"}},
		{name: "BIOS, grub-pc with a path that goes back up out of /dev", setup: after(grubPC("/dev/../tmp/x", ""), disks("/dev/../tmp/x")), severity: Refuse, data: D{"kind": "install_device_missing"}},
		{name: "BIOS, grub-pc with a value that is a file there is, and no device", setup: grubPC("/etc/os-release", ""), severity: Refuse, data: D{"kind": "install_device_missing"}},
		{name: "BIOS, grub-pc with a disk that cannot be looked at", setup: after(grubPC(disk, ""), func(f *fakeProbe) {
			f.fail(disk, &fs.PathError{Op: "stat", Path: disk, Err: fs.ErrPermission})
		}), severity: Refuse, unknown: ReasonUnreadable},
		{name: "BIOS, grub-pc and no debconf database", setup: after(grubPC(disk, ""), func(f *fakeProbe) { f.remove(debconfDB) }), severity: Refuse, unknown: ReasonMissing},
		{name: "BIOS, grub-pc and a debconf database that is too large", setup: after(grubPC(disk, ""), func(f *fakeProbe) { f.set(debconfDB, strings.Repeat("x", MaxFileBytes+1)) }), severity: Refuse, unknown: ReasonTooLarge},
		{name: "BIOS, an ESP that is not mounted is not what starts the machine", setup: grubPC(disk, ""), severity: OK},
		{name: "UEFI, ESP mounted with room", setup: after(uefi, espMounted), severity: OK},
		{name: "UEFI, ESP not mounted", setup: uefi, severity: Refuse, data: D{"kind": "esp_not_mounted"}},
		{name: "UEFI, ESP with 10 MiB left", setup: after(uefi, espMounted, freeESP(10<<20)), severity: Refuse, data: D{"kind": "esp_full", "free_mib": 10, "needed_mib": 16}},
		{name: "UEFI, ESP with exactly the 16 MiB", setup: after(uefi, espMounted, freeESP(16<<20)), severity: OK},
		{name: "UEFI, ESP with a byte less", setup: after(uefi, espMounted, freeESP(16<<20-1)), severity: Refuse, data: D{"kind": "esp_full", "free_mib": 15}},
		{name: "UEFI, and a grub-pc with no disk, which does not start this machine", setup: after(uefi, espMounted, grubPC("", "")), severity: OK},
		{name: "UEFI, ESP whose room cannot be read", setup: after(uefi, espMounted, func(f *fakeProbe) { delete(f.free, "/boot/efi") }), severity: Refuse, unknown: ReasonMissing},
		{name: "UEFI, mounts that cannot be read", setup: after(uefi, func(f *fakeProbe) { f.remove("/proc/self/mountinfo") }), severity: Refuse, unknown: ReasonMissing},
		{name: "dpkg-query that fails", setup: func(f *fakeProbe) { f.on("x", 2, argvQuery(bootloaderPackages...)...) }, severity: Refuse, unknown: ReasonFailed},
		{name: "a firmware directory that cannot be looked at", setup: func(f *fakeProbe) {
			f.fail("/sys/firmware/efi", &fs.PathError{Op: "stat", Path: "/sys/firmware/efi", Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
	})
}

func TestDebconfValue(t *testing.T) {
	text := "Name: a/b\nValue: wrong\n\nName: grub-pc/install_devices\nTemplate: grub-pc/install_devices\nValue:   /dev/sda,  /dev/sdb \nOwners: grub-pc\n\nName: grub-pc/install_devices\nValue: second\n\n"
	for name, tc := range map[string]struct{ text, question, want string }{
		"a question, its value trimmed, the first one": {text, "grub-pc/install_devices", "/dev/sda,  /dev/sdb"},
		"a question that is not there":                 {text, "grub-pc/nothing", ""},
		"a question with no value":                     {"Name: x/y\nTemplate: x/y\nOwners: x\n", "x/y", ""},
		"windows line ends":                            {"Name: x/y\r\nValue: v\r\n\r\nName: z\r\nValue: w\r\n", "x/y", "v"},
		"a name that only starts like it":              {"Name: grub-pc/install_devices_empty\nValue: true\n", "grub-pc/install_devices", ""},
		"a value that mentions another name":           {"Name: x/y\nValue: Name: grub-pc/install_devices\n\nName: grub-pc/install_devices\nValue: ok\n", "grub-pc/install_devices", "ok"},
		"nothing at all":                               {"", "x", ""},
		"binary":                                       {"\x00\x01Name\xff: x\n\n\x00", "x", ""},
	} {
		if got := debconfValue(tc.text, tc.question); got != tc.want {
			t.Errorf("%s: debconfValue = %q, want %q", name, got, tc.want)
		}
	}
}

func TestRootZFS(t *testing.T) {
	runScenarios(t, "root_zfs", []scenario{
		{name: "an ext4 root", severity: OK},
		{name: "a ZFS root", setup: func(f *fakeProbe) {
			f.set("/proc/self/mountinfo", "22 1 0:25 / / rw,noatime shared:1 - zfs rpool/ROOT/debian rw,xattr,posixacl\n")
		}, severity: Refuse, data: D{"dataset": "rpool/ROOT/debian"}},
		{name: "ZFS under a data directory only", setup: func(f *fakeProbe) {
			f.set("/proc/self/mountinfo", rootMountInfo+"30 22 0:30 / /tank rw - zfs tank rw\n")
		}, severity: OK},
		{name: "a ZFS root that has a name that cannot be shown", setup: func(f *fakeProbe) {
			f.set("/proc/self/mountinfo", "22 1 0:25 / / rw - zfs rp<b>ool rw\n")
		}, severity: Refuse, data: D{}},
		{name: "mounts with no root", setup: func(f *fakeProbe) { f.set("/proc/self/mountinfo", "garbage\n") }, severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "mounts that cannot be read", setup: func(f *fakeProbe) {
			f.fail("/proc/self/mountinfo", &fs.PathError{Op: "open", Path: "/proc/self/mountinfo", Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
	})
}

// rootOn is a machine whose root is on a device.
func rootOn(source string) func(*fakeProbe) {
	return func(f *fakeProbe) {
		f.set("/proc/self/mountinfo", "22 1 253:0 / / rw,relatime shared:1 - ext4 "+source+" rw\n")
	}
}

func TestRootLUKS(t *testing.T) {
	stack := func(device string, rows ...string) func(*fakeProbe) {
		return func(f *fakeProbe) { f.on(lsblkOf(rows...), 0, argvLsblk(device)...) }
	}
	runScenarios(t, "root_luks", []scenario{
		{name: "a root on a partition", severity: OK},
		{name: "a root on an encrypted volume", setup: after(rootOn("/dev/mapper/cryptroot"), stack("/dev/mapper/cryptroot", "/dev/mapper/cryptroot:crypt", "/dev/sda3:part", "/dev/sda:disk")),
			severity: Refuse, data: D{"device": "/dev/mapper/cryptroot"}},
		{name: "a root on LVM that is on an encrypted volume", setup: after(rootOn("/dev/mapper/vg-root"), stack("/dev/mapper/vg-root", "/dev/mapper/vg-root:lvm", "/dev/mapper/cryptlvm:crypt", "/dev/sda3:part", "/dev/sda:disk")),
			severity: Refuse, data: D{"device": "/dev/mapper/vg-root"}},
		{name: "a root on LVM", setup: after(rootOn("/dev/mapper/vg-root"), stack("/dev/mapper/vg-root", "/dev/mapper/vg-root:lvm", "/dev/sda3:part", "/dev/sda:disk")), severity: OK},
		{name: "a root on RAID", setup: after(rootOn("/dev/md0"), stack("/dev/md0", "/dev/md0:raid1", "/dev/sda1:part", "/dev/sdb1:part", "/dev/sda:disk", "/dev/sdb:disk")), severity: OK},
		{name: "an encrypted volume that is not the root's", setup: func(f *fakeProbe) {
			f.on(lsblkOf("/dev/sda1:part", "/dev/sda:disk"), 0, argvLsblk("/dev/sda1")...)
			f.set("/proc/self/mountinfo", rootMountInfo+"30 22 253:1 / /data rw - ext4 /dev/mapper/cryptdata rw\n")
		}, severity: OK},
		{name: "a root that is on no device", setup: func(f *fakeProbe) { f.set("/proc/self/mountinfo", "22 1 0:40 / / rw - overlay overlay rw\n") }, severity: OK},
		{name: "a root the kernel calls /dev/root, which findmnt names", setup: after(rootOn("/dev/root"), func(f *fakeProbe) {
			f.on("/dev/mapper/cryptroot\n", 0, "findmnt", "-n", "-o", "SOURCE", "/")
		}, stack("/dev/mapper/cryptroot", "/dev/mapper/cryptroot:crypt", "/dev/sda:disk")), severity: Refuse, data: D{"device": "/dev/mapper/cryptroot"}},
		{name: "a btrfs root, whose subvolume findmnt says in brackets", setup: after(rootOn("/dev/root"), func(f *fakeProbe) {
			f.on("/dev/sda2[/@rootfs]\n", 0, "findmnt", "-n", "-o", "SOURCE", "/")
		}, stack("/dev/sda2", "/dev/sda2:part", "/dev/sda:disk")), severity: OK},
		{name: "a subvolume that findmnt says after a blank", setup: after(rootOn("/dev/root"), func(f *fakeProbe) {
			f.on("/dev/sda2 [/@rootfs]\n", 0, "findmnt", "-n", "-o", "SOURCE", "/")
		}, stack("/dev/sda2", "/dev/sda2:part", "/dev/sda:disk")), severity: OK},
		{name: "findmnt that fails for /dev/root", setup: after(rootOn("/dev/root"), func(f *fakeProbe) { f.on("", 1, "findmnt", "-n", "-o", "SOURCE", "/") }), severity: Refuse, unknown: ReasonFailed},
		// what findmnt says for /dev/root is a device or it is no answer: a root that is on none says so in the
		// mounts, and not here
		{name: "findmnt that says something that is not a device for /dev/root", setup: after(rootOn("/dev/root"), func(f *fakeProbe) { f.on("none\n", 0, "findmnt", "-n", "-o", "SOURCE", "/") }), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "findmnt that says nothing for /dev/root", setup: after(rootOn("/dev/root"), func(f *fakeProbe) { f.on("", 0, "findmnt", "-n", "-o", "SOURCE", "/") }), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "findmnt that warns before the device for /dev/root", setup: after(rootOn("/dev/root"), func(f *fakeProbe) {
			f.on("findmnt: warning: could not read /etc/fstab\n/dev/mapper/cryptroot\n", 0, "findmnt", "-n", "-o", "SOURCE", "/")
		}), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "findmnt that says a device that cannot be passed on for /dev/root", setup: after(rootOn("/dev/root"), func(f *fakeProbe) { f.on("/dev/sda;x\n", 0, "findmnt", "-n", "-o", "SOURCE", "/") }), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "findmnt that says a device with a path that goes back up for /dev/root", setup: after(rootOn("/dev/root"), func(f *fakeProbe) { f.on("/dev/../etc/passwd\n", 0, "findmnt", "-n", "-o", "SOURCE", "/") }), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a device whose name cannot be passed on", setup: rootOn("/dev/mapper/a;b"), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "lsblk that fails", setup: func(f *fakeProbe) { f.on("lsblk: /dev/sda1: not a block device\n", 32, argvLsblk("/dev/sda1")...) }, severity: Refuse, unknown: ReasonFailed},
		{name: "no lsblk", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvLsblk("/dev/sda1")...) }, severity: Refuse, unknown: ReasonToolMissing},
		{name: "lsblk that says nothing", setup: func(f *fakeProbe) { f.on("", 0, argvLsblk("/dev/sda1")...) }, severity: Refuse, unknown: ReasonUnexpectedOutput},
		// a stack that has more devices than are read is read in part, and an encrypted volume past the bound would not be seen
		{name: "a stack of more devices than are read", setup: func(f *fakeProbe) {
			rows := make([]string, 0, maxBlockDevices+1)
			for i := 0; i <= maxBlockDevices; i++ {
				rows = append(rows, fmt.Sprintf("/dev/mapper/v%d:lvm", i))
			}
			f.on(lsblkOf(rows...), 0, argvLsblk("/dev/sda1")...)
		}, severity: Refuse, unknown: ReasonTooLarge},
		{name: "a stack of as many devices as are read", setup: func(f *fakeProbe) {
			rows := make([]string, 0, maxBlockDevices)
			for i := 0; i < maxBlockDevices; i++ {
				rows = append(rows, fmt.Sprintf("/dev/mapper/v%d:lvm", i))
			}
			f.on(lsblkOf(rows...), 0, argvLsblk("/dev/sda1")...)
		}, severity: OK},
		{name: "a stack that says the same device twice is one device", setup: func(f *fakeProbe) {
			rows := make([]string, 0, 2*maxBlockDevices)
			for i := 0; i < 2*maxBlockDevices; i++ {
				rows = append(rows, "/dev/mapper/v1:lvm")
			}
			f.on(lsblkOf(rows...), 0, argvLsblk("/dev/sda1")...)
		}, severity: OK},
		{name: "lsblk that says something else", setup: func(f *fakeProbe) {
			f.on("NAME=/dev/sda1 TYPE=crypt\nTYPE=\"crypt\"\nNAME=\"x\" TYPE=\"CRYPT\"\n", 0, argvLsblk("/dev/sda1")...)
		}, severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "lsblk output that was cut", setup: func(f *fakeProbe) { f.onTruncated(lsblkOf("/dev/sda1:part"), argvLsblk("/dev/sda1")...) }, severity: Refuse, unknown: ReasonTooLarge},
		{name: "lsblk that does not answer", setup: func(f *fakeProbe) { f.onError(context.DeadlineExceeded, argvLsblk("/dev/sda1")...) }, severity: Refuse, unknown: ReasonTimeout},
	})
}

func TestMdDegraded(t *testing.T) {
	mdstat := func(text string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			f.set("/proc/mdstat", "Personalities : [raid1] [raid5]\n"+text+"unused devices: <none>\n")
		}
	}
	runScenarios(t, "md_degraded", []scenario{
		{name: "no array", severity: OK},
		{name: "a whole array", setup: mdstat("md0 : active raid1 sdb1[1] sda1[0]\n      1046528 blocks super 1.2 [2/2] [UU]\n\n"), severity: OK},
		{name: "an array that is being rebuilt, with its members all there", setup: mdstat("md0 : active raid1 sdb1[1] sda1[0]\n      1046528 blocks super 1.2 [2/2] [UU]\n      [=>...................]  resync = 8.3% (87040/1046528) finish=0.1min speed=87040K/sec\n\n"), severity: OK},
		{name: "a mirror that has lost a member", setup: mdstat("md0 : active raid1 sda1[0]\n      1046528 blocks super 1.2 [2/1] [U_]\n\n"), severity: Refuse, data: D{"arrays": []string{"md0"}}},
		{name: "the other member", setup: mdstat("md1 : active raid1 sdb1[1]\n      1046528 blocks super 1.2 [2/1] [_U]\n\n"), severity: Refuse, data: D{"arrays": []string{"md1"}}},
		{name: "a rebuild that is under way is still a degraded array", setup: mdstat("md0 : active raid1 sdb1[2] sda1[0]\n      1046528 blocks super 1.2 [2/1] [U_]\n      [==>..................]  recovery = 12.0% (125568/1046528)\n\n"), severity: Refuse},
		{name: "RAID 5 with one of four missing", setup: mdstat("md2 : active raid5 sdd1[3] sdc1[2] sdb1[1]\n      3139584 blocks super 1.2 level 5, 512k chunk, algorithm 2 [4/3] [_UUU]\n\n"), severity: Refuse, data: D{"arrays": []string{"md2"}}},
		{name: "one array whole and one not, sorted", setup: mdstat("md1 : active raid1 sda2[0] sdb2[1]\n      1 blocks [2/2] [UU]\n\nmd0 : active raid1 sda1[0]\n      1 blocks [2/1] [U_]\n\n"), severity: Refuse, data: D{"arrays": []string{"md0"}}},
		{name: "counts that say whole and marks that say not: the worse is believed", setup: mdstat("md0 : active raid1 sdb1[1] sda1[0]\n      1046528 blocks super 1.2 [2/2] [U_]\n\n"), severity: Refuse, data: D{"arrays": []string{"md0"}}},
		{name: "marks that say whole and counts that say not: the worse is believed", setup: mdstat("md0 : active raid1 sdb1[1] sda1[0]\n      1046528 blocks super 1.2 [2/1] [UU]\n\n"), severity: Refuse, data: D{"arrays": []string{"md0"}}},
		{name: "RAID 0, which has no status of members", setup: mdstat("md0 : active raid0 sdb[1] sda[0]\n      2095104 blocks super 1.2 512k chunks\n\n"), severity: OK},
		{name: "a member marked failed, with its counts saying so", setup: mdstat("md0 : active raid1 sdb1[1](F) sda1[0]\n      1046528 blocks super 1.2 [2/1] [U_]\n\n"), severity: Refuse},
		{name: "counts that are absurd are not read", setup: mdstat("md0 : active raid1 sda1[0]\n      1 blocks [99999999999/1] [U_]\n\n"), severity: OK},
		// an array that is not assembled has no [n/m] line, and is not whole: its members are there and it is not
		// running, as after the loss of one that was left
		{name: "an array that is inactive", setup: mdstat("md127 : inactive sdb1[1](S)\n      1046528 blocks super 1.2\n\n"), severity: Refuse, data: D{"arrays": []string{"md127"}}},
		{name: "an inactive one before a whole one", setup: mdstat("md0 : inactive sdc1[0](S)\n      1046528 blocks super 1.2\n\nmd1 : active raid1 sda2[0] sdb2[1]\n      1 blocks [2/2] [UU]\n\n"), severity: Refuse, data: D{"arrays": []string{"md0"}}},
		{name: "an inactive one and a whole one, sorted", setup: mdstat("md1 : active raid1 sda2[0] sdb2[1]\n      1 blocks [2/2] [UU]\n\nmd0 : inactive sdc1[0](S)\n      1046528 blocks super 1.2\n\n"), severity: Refuse, data: D{"arrays": []string{"md0"}}},
		{name: "an inactive array that is the last of the file", setup: func(f *fakeProbe) {
			f.set("/proc/mdstat", "Personalities : [raid1]\nmd127 : inactive sdb1[1](S)\n      1046528 blocks super 1.2\n")
		}, severity: Refuse, data: D{"arrays": []string{"md127"}}},
		// the container of Intel's RAID in the firmware is inactive by design, and the volumes in it are the arrays
		{name: "the container of a firmware RAID, with its volume whole", setup: mdstat("md126 : active raid1 sdb[1] sda[0]\n      976759808 blocks super external:/md127/0 [2/2] [UU]\n\nmd127 : inactive sdb[1](S) sda[0](S)\n      6320 blocks super external:imsm\n\n"), severity: OK},
		{name: "the container of a firmware RAID, with its volume degraded", setup: mdstat("md126 : active raid1 sda[0]\n      976759808 blocks super external:/md127/0 [2/1] [U_]\n\nmd127 : inactive sda[0](S)\n      3160 blocks super external:imsm\n\n"), severity: Refuse, data: D{"arrays": []string{"md126"}}},
		{name: "a container of DDF", setup: mdstat("md127 : inactive sdb[1](S) sda[0](S)\n      32768 blocks super external:ddf\n\n"), severity: OK},
		{name: "an array that is only for reading", setup: mdstat("md0 : active (read-only) raid1 sdb1[1] sda1[0]\n      1046528 blocks super 1.2 [2/2] [UU]\n\n"), severity: OK},
		{name: "an array that is for reading until it is written to", setup: mdstat("md0 : active (auto-read-only) raid1 sdb1[1] sda1[0]\n      1046528 blocks super 1.2 [2/2] [UU]\n\n"), severity: OK},
		{name: "an array that is only for reading, and has lost a member", setup: mdstat("md0 : active (read-only) raid1 sda1[0]\n      1046528 blocks super 1.2 [2/1] [U_]\n\n"), severity: Refuse},
		{name: "an array with a state that is none of the two", setup: mdstat("md0 : broken raid1 sdb1[1] sda1[0]\n      1046528 blocks super 1.2 [2/2] [UU]\n\n"), severity: Refuse, data: D{"arrays": []string{"md0"}}},
		{name: "an array with no state", setup: mdstat("md0 :\n      1046528 blocks super 1.2\n\n"), severity: Refuse},
		{name: "no /proc/mdstat: no md driver", setup: func(f *fakeProbe) { f.remove("/proc/mdstat") }, severity: OK},
		{name: "a /proc/mdstat that cannot be read", setup: func(f *fakeProbe) {
			f.fail("/proc/mdstat", &fs.PathError{Op: "open", Path: "/proc/mdstat", Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
	})
}

func TestSmartFailed(t *testing.T) {
	smart := func(text string, exit int) func(*fakeProbe) {
		return func(f *fakeProbe) { f.on(text, exit, argvSmart("/dev/sda")...) }
	}
	raid := func(sdaText string, sdaExit int, sdbText string, sdbExit int) func(*fakeProbe) {
		return after(rootOn("/dev/md0"), func(f *fakeProbe) {
			f.on(lsblkOf("/dev/md0:raid1", "/dev/sda1:part", "/dev/sdb1:part", "/dev/sda:disk", "/dev/sdb:disk"), 0, argvLsblk("/dev/md0")...)
			f.on(sdaText, sdaExit, argvSmart("/dev/sda")...)
			f.on(sdbText, sdbExit, argvSmart("/dev/sdb")...)
		})
	}
	runScenarios(t, "smart_failed", []scenario{
		{name: "a disk that passes", severity: OK},
		{name: "a disk that fails", setup: smart(smartOf("FAILED!"), 8), severity: Refuse, data: D{"disks": []string{"/dev/sda"}}},
		{name: "a failure the text says, and the exit status does not", setup: smart(smartOf("FAILED!"), 0), severity: Refuse},
		{name: "a failure the exit status says, and the text does not", setup: smart("smartctl 7.2\n", 8), severity: Refuse},
		{name: "a pass that the exit status contradicts", setup: smart(smartOf("PASSED"), 8), severity: Refuse},
		{name: "a pass with an exit status of the other bits (errors in its log)", setup: smart(smartOf("PASSED"), 64), severity: OK},
		{name: "a SCSI disk that is OK", setup: smart("SMART Health Status: OK\n", 0), severity: OK},
		{name: "a SCSI disk that is not", setup: smart("SMART Health Status: IMPENDING FAILURE GENERAL HARD DRIVE FAILURE [asc=5d, ascq=10]\n", 0), severity: Info, data: D{"why": "unavailable", "disks": []string{"/dev/sda"}}},
		{name: "a virtual disk that does not say", setup: smart("/dev/sda: Unable to detect device type\nPlease specify device type with the -d option.\n", 1), severity: Info, data: D{"checked": false, "why": "unavailable", "disks": []string{"/dev/sda"}}},
		{name: "a model that is called FAILED, on a disk that passes", setup: smart("Device Model:     FAILED! \n"+smartOf("PASSED"), 0), severity: OK},
		{name: "an answer that mentions the result inside a line", setup: smart("note: SMART overall-health self-assessment test result: FAILED!\n", 0), severity: Info},
		{name: "smartctl that is not installed", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvSmart("/dev/sda")...) }, severity: Info, data: D{"checked": false, "why": "no_smartctl"}, action: ActionNone},
		{name: "smartctl that does not answer", setup: func(f *fakeProbe) { f.onError(context.DeadlineExceeded, argvSmart("/dev/sda")...) }, severity: Refuse, unknown: ReasonTimeout},
		{name: "smartctl that breaks", setup: func(f *fakeProbe) { f.onError(errors.New("boom"), argvSmart("/dev/sda")...) }, severity: Refuse, unknown: ReasonFailed},
		{name: "a root that is on no device", setup: func(f *fakeProbe) { f.set("/proc/self/mountinfo", "22 1 0:40 / / rw - overlay overlay rw\n") }, severity: Info, data: D{"checked": false, "why": "no_block_device"}},
		{name: "a stack that has no disk in it", setup: func(f *fakeProbe) { f.on(lsblkOf("/dev/sda1:part", "/dev/loop0:loop"), 0, argvLsblk("/dev/sda1")...) }, severity: Info, data: D{"why": "no_disk"}},
		{name: "a mirror of two disks that pass", setup: raid(smartOf("PASSED"), 0, smartOf("PASSED"), 0), severity: OK},
		{name: "a mirror with one disk that fails", setup: raid(smartOf("PASSED"), 0, smartOf("FAILED!"), 8), severity: Refuse, data: D{"disks": []string{"/dev/sdb"}}},
		{name: "a mirror with one disk that does not say and one that passes", setup: raid(smartOf("PASSED"), 0, "Unable to detect device type\n", 1), severity: Info, data: D{"disks": []string{"/dev/sdb"}}},
		{name: "a mirror with one that fails and one that does not say", setup: raid("Unable to detect device type\n", 1, smartOf("FAILED!"), 8), severity: Refuse, data: D{"disks": []string{"/dev/sdb"}}},
		{name: "lsblk that fails", setup: func(f *fakeProbe) { f.on("", 32, argvLsblk("/dev/sda1")...) }, severity: Refuse, unknown: ReasonFailed},
		// smartctl -n standby does not wake a disk that sleeps: it says so, with the exit status 2, and the disk
		// is not checked (it is not a pass)
		{name: "a disk that sleeps", setup: smart(standbyText, 2), severity: Info, data: D{"checked": false, "why": "standby", "disks": []string{"/dev/sda"}}, action: ActionNone},
		{name: "a disk in a deeper sleep", setup: smart("smartctl 7.2 2020-12-30 r5155\nDevice is in SLEEP mode, exit(2)\n", 2), severity: Info, data: D{"why": "standby", "disks": []string{"/dev/sda"}}},
		{name: "a disk in the second state of standby", setup: smart("Device is in STANDBY_Y mode, exit(2)\n", 2), severity: Info, data: D{"why": "standby"}},
		{name: "a disk that says it sleeps and does not say it with the exit status", setup: smart(standbyText, 0), severity: Info, data: D{"why": "unavailable"}},
		{name: "a pass is a pass, whatever else the text says", setup: smart(smartOf("PASSED")+standbyText, 2), severity: OK},
		{name: "a disk that exits with 2 and does not say it sleeps", setup: smart("Smartctl open device: /dev/sda failed: No such device\n", 2), severity: Info, data: D{"why": "unavailable"}},
		{name: "a disk that is idle is asked", setup: smart("Device is in IDLE mode, exit(2)\n", 2), severity: Info, data: D{"why": "unavailable"}},
		{name: "a disk that says it sleeps in the middle of a line", setup: smart("note: Device is in STANDBY mode, exit(2)\n", 2), severity: Info, data: D{"why": "unavailable"}},
		{name: "a disk that sleeps and says that it failed", setup: smart(standbyText+smartOf("FAILED!"), 10), severity: Refuse, data: D{"disks": []string{"/dev/sda"}}},
		{name: "a mirror with one disk that sleeps and one that passes", setup: raid(smartOf("PASSED"), 0, standbyText, 2), severity: Info, data: D{"why": "standby", "disks": []string{"/dev/sdb"}}},
		{name: "a mirror with one disk that sleeps and one that fails", setup: raid(standbyText, 2, smartOf("FAILED!"), 8), severity: Refuse, data: D{"disks": []string{"/dev/sdb"}}},
		{name: "a mirror with one disk that sleeps and one that does not say", setup: raid(standbyText, 2, "Unable to detect device type\n", 1), severity: Info, data: D{"why": "unavailable", "disks": []string{"/dev/sdb"}}},
		{name: "a mirror whose two disks sleep", setup: raid(standbyText, 2, standbyText, 2), severity: Info, data: D{"why": "standby", "disks": []string{"/dev/sda", "/dev/sdb"}}},
	})
}

const standbyText = "smartctl 7.2 2020-12-30 r5155\nDevice is in STANDBY mode, exit(2)\n"

// smartStack is a root on a RAID of the disks, which pass unless they are said not to.
func smartStack(f *fakeProbe, count int, answer func(disk string) (string, int)) {
	rootOn("/dev/md0")(f)
	rows := []string{"/dev/md0:raid5"}
	for i := 0; i < count; i++ {
		disk := fmt.Sprintf("/dev/sd%c", 'a'+i)
		rows = append(rows, disk+":disk")
		text, exit := answer(disk)
		f.on(text, exit, argvSmart(disk)...)
	}
	f.on(lsblkOf(rows...), 0, argvLsblk("/dev/md0")...)
}

func smartctlAsked(f *fakeProbe) int {
	asked := 0
	for _, argv := range f.attemptedCommands() {
		if argv[0] == "smartctl" {
			asked++
		}
	}
	return asked
}

// A stack of more than four disks is asked of up to sixteen, and one of more than that says that it was
// asked only in part: a failing fifth disk is not a pass because the first four passed.
func TestSmartIsAskedOfAtMostSixteenDisksAndSaysItWhenThereAreMore(t *testing.T) {
	passes := func(string) (string, int) { return smartOf("PASSED"), 0 }
	for _, tc := range []struct {
		name     string
		disks    int
		answer   func(string) (string, int)
		severity Severity
		data     D
		asked    int
	}{
		{"four disks", 4, passes, OK, nil, 4},
		{"six disks that pass", 6, passes, OK, nil, 6},
		{"sixteen disks that pass", 16, passes, OK, nil, 16},
		{"a fifth disk that fails", 5, func(d string) (string, int) {
			if d == "/dev/sde" {
				return smartOf("FAILED!"), 8
			}
			return passes(d)
		}, Refuse, D{"disks": []string{"/dev/sde"}}, 5},
		{"a sixteenth disk that fails", 16, func(d string) (string, int) {
			if d == "/dev/sdp" {
				return smartOf("FAILED!"), 8
			}
			return passes(d)
		}, Refuse, D{"disks": []string{"/dev/sdp"}}, 16},
		{"seventeen disks that pass: sixteen were asked", 17, passes, Info, D{"checked": false, "why": "partial"}, 16},
		{"seventeen disks, one of the first that fails", 17, func(d string) (string, int) {
			if d == "/dev/sdb" {
				return smartOf("FAILED!"), 8
			}
			return passes(d)
		}, Refuse, D{"disks": []string{"/dev/sdb"}}, 16},
		{"seventeen disks, one of the sixteen that does not say", 17, func(d string) (string, int) {
			if d == "/dev/sdc" {
				return "Unable to detect device type\n", 1
			}
			return passes(d)
		}, Info, D{"why": "unavailable", "disks": []string{"/dev/sdc"}}, 16},
		{"seventeen disks, one of the sixteen that sleeps", 17, func(d string) (string, int) {
			if d == "/dev/sdc" {
				return standbyText, 2
			}
			return passes(d)
		}, Info, D{"why": "standby", "disks": []string{"/dev/sdc"}}, 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBox(t)
			smartStack(f, tc.disks, tc.answer)
			got := checkOn(t, f, "smart_failed")
			if got.Severity != tc.severity || got.Data["unknown"] != false {
				t.Fatalf("finding = %+v", got)
			}
			for key, want := range tc.data {
				have, _ := json.Marshal(got.Data[key])
				wanted, _ := json.Marshal(want)
				if string(have) != string(wanted) {
					t.Errorf("data[%s] = %s, want %s", key, have, wanted)
				}
			}
			if got.Severity == Info && got.Action != ActionNone {
				t.Errorf("action = %q", got.Action)
			}
			if asked := smartctlAsked(f); asked != tc.asked {
				t.Errorf("%d disks were asked, want %d", asked, tc.asked)
			}
		})
	}
}

func TestLogOnTmpfs(t *testing.T) {
	mounts := func(lines ...string) func(*fakeProbe) {
		return func(f *fakeProbe) { f.set("/proc/self/mountinfo", rootMountInfo+strings.Join(lines, "")) }
	}
	runScenarios(t, "log_on_tmpfs", []scenario{
		{name: "the log is on the root disk", severity: OK},
		{name: "/var/log is a tmpfs", setup: mounts("30 22 0:30 / /var/log rw - tmpfs tmpfs rw\n"), severity: Refuse, data: D{"filesystem": "tmpfs", "mount_point": "/var/log"}},
		{name: "/var/log/casaos is a tmpfs", setup: mounts("30 22 0:30 / /var/log/casaos rw - tmpfs tmpfs rw\n"), severity: Refuse, data: D{"mount_point": "/var/log/casaos"}},
		{name: "/var is a tmpfs", setup: mounts("30 22 0:30 / /var rw - tmpfs tmpfs rw\n"), severity: Refuse, data: D{"mount_point": "/var"}},
		{name: "log2ram: a disk in memory", setup: mounts("30 22 251:0 / /var/log rw - ext4 /dev/zram1 rw\n"), severity: Refuse, data: D{"filesystem": "ext4"}},
		{name: "a ramfs", setup: mounts("30 22 0:30 / /var/log rw - ramfs ramfs rw\n"), severity: Refuse, data: D{"filesystem": "ramfs"}},
		{name: "/var/log on a disk of its own", setup: mounts("30 22 8:2 / /var/log rw - ext4 /dev/sda2 rw\n"), severity: OK},
		{name: "a tmpfs on /var/log, and a disk under it on /var/log/casaos: the one the log is on wins", setup: mounts("30 22 0:30 / /var/log rw - tmpfs tmpfs rw\n", "31 30 8:3 / /var/log/casaos rw - ext4 /dev/sda3 rw\n"), severity: OK},
		{name: "a tmpfs whose name only starts like the directory", setup: mounts("30 22 0:30 / /var/log2 rw - tmpfs tmpfs rw\n", "31 22 0:31 / /var/lo rw - tmpfs tmpfs rw\n"), severity: OK},
		{name: "a tmpfs elsewhere", setup: mounts("30 22 0:30 / /tmp rw - tmpfs tmpfs rw\n", "31 22 0:31 / /run rw - tmpfs tmpfs rw\n"), severity: OK},
		{name: "a root that is a tmpfs, as in a live system", setup: func(f *fakeProbe) { f.set("/proc/self/mountinfo", "22 1 0:22 / / rw - tmpfs tmpfs rw\n") }, severity: Refuse, data: D{"mount_point": "/"}},
		{name: "mounts that cannot be read", setup: func(f *fakeProbe) { f.remove("/proc/self/mountinfo") }, severity: Refuse, unknown: ReasonMissing},
		{name: "mounts that have nothing in them", setup: func(f *fakeProbe) { f.set("/proc/self/mountinfo", "garbage\n") }, severity: Refuse, unknown: ReasonUnexpectedOutput},
	})
}

func TestDiskRoot(t *testing.T) {
	free := func(path string, bytes uint64) func(*fakeProbe) {
		return func(f *fakeProbe) { f.free[path] = bytes }
	}
	runScenarios(t, "disk_root", []scenario{
		{name: "plenty of room", severity: OK},
		{name: "exactly 2 GiB", setup: after(free("/", 2<<30), free("/usr", 2<<30)), severity: OK},
		{name: "a byte less than 2 GiB on /", setup: free("/", 2<<30-1), severity: Refuse, data: D{"path": "/", "free_mib": 2047, "needed_mib": 2048}, action: ActionFreeDiskSpace},
		{name: "1 GiB on a /usr of its own", setup: free("/usr", 1<<30), severity: Refuse, data: D{"path": "/usr", "free_mib": 1024}, action: ActionFreeDiskSpace},
		{name: "a /usr that cannot be measured", setup: func(f *fakeProbe) { delete(f.free, "/usr") }, severity: Refuse, unknown: ReasonMissing},
		{name: "a / that cannot be measured", setup: func(f *fakeProbe) { delete(f.free, "/") }, severity: Refuse, unknown: ReasonMissing},
	})
}

func TestDiskArchives(t *testing.T) {
	free := func(path string, bytes uint64) func(*fakeProbe) {
		return func(f *fakeProbe) { f.free[path] = bytes }
	}
	archives := "/var/cache/apt/archives"
	runScenarios(t, "disk_archives", []scenario{
		{name: "plenty of room", severity: OK},
		{name: "exactly 1.5 GiB", setup: free(archives, 3<<29), severity: OK},
		{name: "a byte less", setup: free(archives, 3<<29-1), severity: Refuse, data: D{"path": archives, "free_mib": 1535, "needed_mib": 1536}, action: ActionFreeDiskSpace},
		{name: "the directory is not there yet: its parent says", setup: after(func(f *fakeProbe) { delete(f.free, archives) }, free("/var/cache/apt", 1<<30)), severity: Refuse, data: D{"free_mib": 1024}},
		{name: "no /var/cache/apt: /var/cache", setup: after(func(f *fakeProbe) { delete(f.free, archives) }, free("/var/cache", 10<<30)), severity: OK},
		{name: "only / says", setup: after(func(f *fakeProbe) { delete(f.free, archives) }, free("/var", 0), func(f *fakeProbe) { delete(f.free, "/var") }), severity: OK},
		{name: "nothing says", setup: func(f *fakeProbe) {
			delete(f.free, archives)
			delete(f.free, "/")
		}, severity: Refuse, unknown: ReasonMissing},
	})
}

func TestDiskBoot(t *testing.T) {
	free := func(bytes uint64) func(*fakeProbe) { return func(f *fakeProbe) { f.free["/boot"] = bytes } }
	size := func(bytes string, exit int) func(*fakeProbe) {
		return func(f *fakeProbe) { f.on(bytes, exit, argvSize("/boot")...) }
	}
	runScenarios(t, "disk_boot", []scenario{
		{name: "plenty of room", severity: OK},
		{name: "exactly 300 MiB", setup: free(300 << 20), severity: OK},
		{name: "a byte less", setup: free(300<<20 - 1), severity: Refuse, data: D{"path": "/boot", "free_mib": 299, "needed_mib": 300}, action: ActionFreeDiskSpace},
		{name: "nothing left", setup: free(0), severity: Refuse, data: D{"free_mib": 0}},
		{name: "a /boot that cannot be measured", setup: func(f *fakeProbe) { delete(f.free, "/boot") }, severity: Refuse, unknown: ReasonMissing},
		{name: "Debian 11 does not ask how large /boot is", setup: size("", 1), severity: OK},
		// the floor is the size of the filesystem, which is about 4 or 5 percent less than the partition of 768 MB
		// that the notes of Debian 13 ask for (the inode tables and the journal are not room), and no room that
		// is freed makes a filesystem larger: there is no action to take but the partition's
		{name: "Debian 12: a /boot of 768 MiB", setup: after(debian12, size("805306368\n", 0)), severity: OK},
		{name: "Debian 12: a filesystem on a partition of 768 MB", setup: after(debian12, size("735000000\n", 0)), severity: OK},
		{name: "Debian 12: a filesystem of exactly 700 MiB", setup: after(debian12, size("734003200\n", 0)), severity: OK},
		{name: "Debian 12: a filesystem of a byte less", setup: after(debian12, size("734003199\n", 0)), severity: Refuse, data: D{"path": "/boot", "total_mib": 699, "needed_mib": 700}, action: ActionNone, title: "too small"},
		{name: "Debian 12: a /boot of 512 MiB, with room in it", setup: after(debian12, size("536870912\n", 0)), severity: Refuse, data: D{"total_mib": 512, "needed_mib": 700}, action: ActionNone},
		{name: "Debian 12: low room is said before the size is asked", setup: after(debian12, free(10<<20), size("", 1)), severity: Refuse, data: D{"free_mib": 10}},
		{name: "Debian 12: findmnt that fails", setup: after(debian12, size("", 1)), severity: Refuse, unknown: ReasonFailed},
		{name: "Debian 12: no findmnt", setup: after(debian12, func(f *fakeProbe) { f.onError(ErrToolMissing, argvSize("/boot")...) }), severity: Refuse, unknown: ReasonToolMissing},
		{name: "Debian 12: a size that is not a number", setup: after(debian12, size("a lot\n", 0)), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "Debian 12: a size that is negative or too large", setup: after(debian12, size("-5\n", 0)), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "Debian 12: a size with too many digits", setup: after(debian12, size("99999999999999999999999\n", 0)), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "Ubuntu 22.04 does not ask either", setup: after(ubuntu2204, size("", 1)), severity: OK},
	})
}

func TestRAM(t *testing.T) {
	memory := func(memKiB, swapKiB string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			f.set("/proc/meminfo", "MemTotal:       "+memKiB+" kB\nMemFree:          1 kB\nSwapTotal:      "+swapKiB+" kB\n")
		}
	}
	runScenarios(t, "ram", []scenario{
		{name: "4 GiB", severity: OK},
		{name: "exactly 2 GiB", setup: memory("2097152", "0"), severity: OK},
		{name: "a KiB less than 2 GiB", setup: memory("2097151", "0"), severity: Warn, data: D{"total_mib": 2047, "needed_mib": 2048}, action: ActionNone},
		{name: "exactly 1 GiB", setup: memory("1048576", "0"), severity: Warn, data: D{"total_mib": 1024}},
		{name: "a KiB less than 1 GiB", setup: memory("1048575", "0"), severity: Refuse, data: D{"total_mib": 1023, "needed_mib": 1024}},
		{name: "700 MiB", setup: memory("716800", "0"), severity: Refuse, data: D{"ram_mib": 700, "swap_mib": 0}},
		{name: "swap counts", setup: memory("716800", "409600"), severity: Warn, data: D{"ram_mib": 700, "swap_mib": 400, "total_mib": 1100}},
		{name: "enough swap to be enough", setup: memory("524288", "2097152"), severity: OK},
		{name: "no swap line", setup: func(f *fakeProbe) { f.set("/proc/meminfo", "MemTotal: 4044764 kB\n") }, severity: OK},
		{name: "a value that has no unit", setup: func(f *fakeProbe) { f.set("/proc/meminfo", "MemTotal: 4044764\n") }, severity: OK},
		{name: "no /proc/meminfo", setup: func(f *fakeProbe) { f.remove("/proc/meminfo") }, severity: Refuse, unknown: ReasonMissing},
		{name: "no MemTotal", setup: func(f *fakeProbe) { f.set("/proc/meminfo", "MemFree: 5 kB\n") }, severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a MemTotal that is not a number", setup: func(f *fakeProbe) { f.set("/proc/meminfo", "MemTotal: lots kB\n") }, severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a MemTotal that is negative", setup: func(f *fakeProbe) { f.set("/proc/meminfo", "MemTotal: -4044764 kB\n") }, severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a MemTotal in another unit", setup: func(f *fakeProbe) { f.set("/proc/meminfo", "MemTotal: 4 GB\n") }, severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a MemTotal that would overflow", setup: func(f *fakeProbe) { f.set("/proc/meminfo", "MemTotal: 18446744073709551615 kB\n") }, severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a SwapTotal that is not a number", setup: func(f *fakeProbe) { f.set("/proc/meminfo", "MemTotal: 4044764 kB\nSwapTotal: x kB\n") }, severity: Refuse, unknown: ReasonUnexpectedOutput},
	})
}

func TestPower(t *testing.T) {
	supply := func(name string, files map[string]string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			for file, content := range files {
				f.set(powerSupplyDir+"/"+name+"/"+file, content)
			}
		}
	}
	battery := func(status, capacity string) func(*fakeProbe) {
		files := map[string]string{"type": "Battery\n", "status": status + "\n"}
		if capacity != "" {
			files["capacity"] = capacity + "\n"
		}
		return supply("BAT0", files)
	}
	runScenarios(t, "power", []scenario{
		{name: "no power supply class at all", severity: OK},
		{name: "mains power only", setup: supply("AC", map[string]string{"type": "Mains\n", "online": "1\n"}), severity: OK},
		{name: "a battery that is discharging", setup: battery("Discharging", "90"), severity: Refuse, data: D{"battery": "BAT0", "status": "discharging", "percent": 90}},
		{name: "a battery that is charging at 30 percent", setup: battery("Charging", "30"), severity: Refuse, data: D{"percent": 30, "status": "charging"}},
		{name: "a battery that is charging at 49 percent", setup: battery("Charging", "49"), severity: Refuse},
		{name: "a battery that is charging at 50 percent", setup: battery("Charging", "50"), severity: OK},
		{name: "a battery that is full", setup: battery("Full", "100"), severity: OK},
		{name: "a battery that does not charge, at 80", setup: battery("Not charging", "80"), severity: OK},
		{name: "a battery that discharges and does not say how much", setup: battery("Discharging", ""), severity: Refuse, data: D{"battery": "BAT0"}},
		{name: "a battery that is full and does not say how much", setup: battery("Full", ""), severity: OK},
		{name: "a wireless mouse's battery is a device's", setup: supply("hidpp_battery_0", map[string]string{"type": "Battery\n", "scope": "Device\n", "status": "Discharging\n", "capacity": "10\n"}), severity: OK},
		{name: "the machine's own battery says its scope", setup: supply("BAT0", map[string]string{"type": "Battery\n", "scope": "System\n", "status": "Discharging\n", "capacity": "60\n"}), severity: Refuse},
		{name: "a UPS that is on its battery is not a battery", setup: supply("ups", map[string]string{"type": "UPS\n", "status": "Discharging\n", "capacity": "10\n"}), severity: OK},
		{name: "a second battery, the one that is low", setup: after(battery("Full", "100"), supply("BAT1", map[string]string{"type": "Battery\n", "status": "Discharging\n", "capacity": "20\n"})), severity: Refuse, data: D{"battery": "BAT1"}},
		{name: "a capacity that is not a number", setup: battery("Discharging", "lots"), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a capacity above 100", setup: battery("Full", "101"), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a capacity below 0", setup: battery("Full", "-3"), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a status that is not a word", setup: battery("Dis charging <b>", "90"), severity: OK},
		{name: "a name that is not a name", setup: supply("../BAT0", map[string]string{"type": "Battery\n", "status": "Discharging\n"}), severity: OK},
		{name: "a name that goes back up", setup: supply("..", map[string]string{"type": "Battery\n", "status": "Discharging\n", "capacity": "10\n"}), severity: OK},
		{name: "a battery whose type cannot be read", setup: after(battery("Full", "100"), func(f *fakeProbe) {
			f.fail(powerSupplyDir+"/BAT0/type", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
		}), severity: Refuse, unknown: ReasonUnreadable},
		{name: "a power supply class that cannot be listed", setup: func(f *fakeProbe) {
			f.fail(powerSupplyDir, &fs.PathError{Op: "open", Path: powerSupplyDir, Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
	})
}

func TestParsersOfTheDisksAreBoundedOnHostileInput(t *testing.T) {
	huge := strings.Repeat("md0 : active raid1 sda1[0]\n 1 blocks [2/1] [U_]\n", 20000)
	if got := degradedArrays(huge); len(got) != 1 || got[0] != "md0" {
		t.Errorf("a long mdstat: %v", got)
	}
	for _, text := range []string{"", "\x00\x00", "md0", "md0 :", "[2/1] [U_]", "md99999999 : x\n[2/1] [U_]", "md0 : active\n[9/9] [" + strings.Repeat("U", 200) + "]", strings.Repeat("\xff", 100)} {
		for _, name := range degradedArrays(text) {
			if !strings.HasPrefix(name, "md") || len(name) > 7 {
				t.Errorf("degradedArrays(%q) gave the name %q", text, name)
			}
		}
	}
	for _, text := range []string{"", "MemTotal", "MemTotal:", "MemTotal: kB", "MemTotal: 1 kB kB", ":", "MemTotal:\x00 1 kB"} {
		if value, found, err := meminfoKiB(text, "MemTotal"); found && err == nil && value > 1 {
			t.Errorf("meminfoKiB(%q) = %d", text, value)
		}
	}
}
