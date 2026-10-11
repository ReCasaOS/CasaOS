package osreadiness

import (
	"context"
	"io/fs"
	"strconv"
	"testing"
	"time"
)

func TestForeignArch(t *testing.T) {
	foreign := func(archs string, packages string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			f.on(archs, 0, argvForeign...)
			f.on(packages, 0, argvQuery()...)
		}
	}
	runScenarios(t, "foreign_arch", []scenario{
		{name: "no foreign architecture", severity: OK},
		{name: "i386 is added and nothing is installed from it", setup: foreign("i386\n", pkg("libc6", "2.31-13")), severity: OK},
		{name: "a package of i386 is installed", setup: foreign("i386\n", pkg("libc6", "2.31-13")+pkgArch("libc6", "i386", "2.31-13")), severity: Refuse,
			data: D{"architectures": []string{"i386"}, "packages": []string{"libc6"}, "count": 1}, action: ActionNone},
		{name: "two architectures, sorted, with their packages", setup: foreign("i386\narmhf\n", pkgArch("zlib1g", "armhf", "1")+pkgArch("libc6", "i386", "2")+pkgArch("libc6", "armhf", "2")), severity: Refuse,
			data: D{"architectures": []string{"armhf", "i386"}, "packages": []string{"libc6", "zlib1g"}, "count": 3}},
		{name: "packages that were removed", setup: foreign("i386\n", "rc \tlibc6\ti386\t2.31\nun \tfoo\ti386\t1\n"), severity: OK},
		{name: "a package of every architecture is not foreign", setup: foreign("i386\n", pkgArch("tzdata", "all", "2021a")), severity: OK},
		{name: "a line that is not an architecture", setup: foreign("<b>x</b>\n", pkgArch("libc6", "<b>x</b>", "1")), severity: OK},
		{name: "dpkg that fails", setup: func(f *fakeProbe) { f.on("", 2, argvForeign...) }, severity: Refuse, unknown: ReasonFailed},
		{name: "no dpkg", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvForeign...) }, severity: Refuse, unknown: ReasonToolMissing},
		{name: "dpkg-query that fails", setup: func(f *fakeProbe) {
			f.on("i386\n", 0, argvForeign...)
			f.on("error\n", 2, argvQuery()...)
		}, severity: Refuse, unknown: ReasonFailed},
	})
}

func TestKeyringOld(t *testing.T) {
	keyring := func(version string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			text := ""
			if version != "" {
				text = pkg("debian-archive-keyring", version)
			}
			f.installed(text, "debian-archive-keyring")
		}
	}
	runScenarios(t, "keyring_old", []scenario{
		{name: "the keyring of Debian 11 has bookworm's keys", severity: OK},
		{name: "an older one", setup: keyring("2019.1"), severity: Info, data: D{"installed": "2019.1", "minimum": "2021.1.1+deb11u1"}, action: ActionNone},
		{name: "one revision short", setup: keyring("2021.1.1"), severity: Info},
		{name: "a newer one", setup: keyring("2023.3+deb12u2"), severity: OK},
		{name: "none installed", setup: keyring(""), severity: Info, data: D{"minimum": "2021.1.1+deb11u1"}},
		{name: "Debian 12 needs trixie's keys, and the old one does not have them", setup: after(debian12, keyring("2023.3+deb12u1")), severity: Info, data: D{"minimum": "2023.3+deb12u2"}},
		{name: "Debian 12, with them", setup: after(debian12, keyring("2023.3+deb12u2")), severity: OK},
		{name: "Debian 12, with later ones", setup: after(debian12, keyring("2025.1")), severity: OK},
		{name: "dpkg-query that fails", setup: func(f *fakeProbe) { f.on("x", 2, argvQuery("debian-archive-keyring")...) }, severity: Refuse, unknown: ReasonFailed},
	})
}

func TestOpenSSHOld(t *testing.T) {
	ssh := func(version string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			text := ""
			if version != "" {
				text = pkg("openssh-server", version)
			}
			f.installed(text, "openssh-server")
		}
	}
	runScenarios(t, "openssh_old", []scenario{
		{name: "an old one", setup: after(debian12, ssh("1:9.2p1-2+deb12u3")), severity: Info, data: D{"installed": "1:9.2p1-2+deb12u3", "minimum": "1:9.2p1-2+deb12u7"}, action: ActionNone},
		{name: "exactly the one", setup: after(debian12, ssh("1:9.2p1-2+deb12u7")), severity: OK},
		{name: "a later one, by number and not by letter", setup: after(debian12, ssh("1:9.2p1-2+deb12u10")), severity: OK},
		{name: "a newer upstream", setup: after(debian12, ssh("1:9.3p1-1")), severity: OK},
		{name: "without the epoch it is older", setup: after(debian12, ssh("9.9p1-1")), severity: Info},
		{name: "no ssh server, nothing to lock out", setup: after(debian12, ssh("")), severity: OK},
		{name: "dpkg-query that fails", setup: after(debian12, func(f *fakeProbe) { f.on("x", 2, argvQuery("openssh-server")...) }), severity: Refuse, unknown: ReasonFailed},
	})
}

func TestHostServices(t *testing.T) {
	installed := func(lines ...string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			text := ""
			for _, line := range lines {
				text += pkg(line, "1.0")
			}
			f.installed(text, hostServicePatterns...)
		}
	}
	for _, tc := range []struct {
		name     string
		packages []string
		services []string
		listed   []string
	}{
		{"PostgreSQL", []string{"postgresql-13", "postgresql-client-13", "postgresql-common", "postgresql-client-common"}, []string{"postgresql"}, []string{"postgresql-13"}},
		{"PostgreSQL's metapackage", []string{"postgresql"}, []string{"postgresql"}, []string{"postgresql"}},
		{"MariaDB", []string{"mariadb-server-10.5", "mariadb-client-10.5", "mariadb-common"}, []string{"mariadb"}, []string{"mariadb-server-10.5"}},
		{"MariaDB's metapackage", []string{"mariadb-server"}, []string{"mariadb"}, []string{"mariadb-server"}},
		{"MariaDB by its default name", []string{"default-mysql-server"}, []string{"mariadb"}, []string{"default-mysql-server"}},
		{"MySQL", []string{"mysql-server", "mysql-server-8.0", "mysql-client-8.0"}, []string{"mysql"}, []string{"mysql-server", "mysql-server-8.0"}},
		{"OpenLDAP", []string{"slapd"}, []string{"slapd"}, []string{"slapd"}},
		{"Dovecot", []string{"dovecot-core", "dovecot-imapd"}, []string{"dovecot"}, []string{"dovecot-core"}},
		{"RabbitMQ", []string{"rabbitmq-server"}, []string{"rabbitmq"}, []string{"rabbitmq-server"}},
		{"libvirt", []string{"libvirt-daemon-system", "libvirt-daemon", "libvirt-clients", "libvirt0"}, []string{"libvirt"}, []string{"libvirt-daemon", "libvirt-daemon-system"}},
		{"strongSwan", []string{"strongswan-starter", "strongswan-libcharon", "libstrongswan"}, []string{"strongswan"}, []string{"strongswan-starter"}},
		{"strongSwan by its systemd daemon", []string{"charon-systemd"}, []string{"strongswan"}, []string{"charon-systemd"}},
		{"Bacula", []string{"bacula-director", "bacula-sd", "bacula-fd", "bacula-console", "bacula-common"}, []string{"bacula"}, []string{"bacula-director", "bacula-fd", "bacula-sd"}},
		{"Request Tracker", []string{"request-tracker4", "rt4-clients"}, []string{"request-tracker"}, []string{"request-tracker4"}},
		{"several, sorted", []string{"slapd", "postgresql-13", "rabbitmq-server"}, []string{"postgresql", "rabbitmq", "slapd"}, []string{"postgresql-13", "rabbitmq-server", "slapd"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBox(t)
			installed(tc.packages...)(f)
			got := checkOn(t, f, "host_services")
			if got.Severity != Refuse || got.Data["unknown"] != false {
				t.Fatalf("finding = %+v", got)
			}
			if !equalLists(got.Data["services"], tc.services) || !equalLists(got.Data["packages"], tc.listed) {
				t.Fatalf("data = %v, want services %v and packages %v", got.Data, tc.services, tc.listed)
			}
		})
	}
	runScenarios(t, "host_services", []scenario{
		{name: "none of them", severity: OK},
		{name: "only what is not the server", setup: installed("postgresql-client-13", "libvirt-clients", "dovecot-imapd", "bacula-console", "strongswan-libcharon", "mariadb-client-10.5", "request-tracker-doc"), severity: OK},
		{name: "a server that was removed", setup: func(f *fakeProbe) { f.installed("rc \tpostgresql-13\tamd64\t13.14\n", hostServicePatterns...) }, severity: OK},
		{name: "dpkg-query that fails", setup: func(f *fakeProbe) { f.on("x", 2, argvQuery(hostServicePatterns...)...) }, severity: Refuse, unknown: ReasonFailed},
		{name: "dpkg-query that does not answer", setup: func(f *fakeProbe) { f.onError(context.DeadlineExceeded, argvQuery(hostServicePatterns...)...) }, severity: Refuse, unknown: ReasonTimeout},
	})
}

func equalLists(got any, want []string) bool {
	list, ok := got.([]string)
	if !ok || len(list) != len(want) {
		return false
	}
	for i := range want {
		if list[i] != want[i] {
			return false
		}
	}
	return true
}

// dkmsBox is a machine that has a DKMS module, on a card and a disk that are in the kernel's own tree
// unless a test says the module is theirs.
func dkmsBox(f *fakeProbe) {
	f.on("zfs/2.0.3, 5.10.0-35-amd64, x86_64: installed\n", 0, "dkms", "status")
	// the network card: enp3s0, of the driver e1000e
	f.link("/sys/class/net/enp3s0/device/driver", "../../../bus/pci/drivers/e1000e")
	f.on("/lib/modules/5.10.0-35-amd64/kernel/drivers/net/ethernet/intel/e1000e/e1000e.ko\n", 0, "modinfo", "-n", "e1000e")
	// the disk: sda1 behind a SATA controller of the driver ahci
	f.link("/sys/dev/block/8:1", "../../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda/sda1")
	f.link("/sys/devices/pci0000:00/0000:00:17.0/driver", "../../../bus/pci/drivers/ahci")
	f.link("/sys/devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/driver", "../../../../../../../../bus/scsi/drivers/sd")
	f.on("/lib/modules/5.10.0-35-amd64/kernel/drivers/ata/ahci.ko\n", 0, "modinfo", "-n", "ahci")
	f.on("/lib/modules/5.10.0-35-amd64/kernel/drivers/scsi/sd_mod.ko\n", 0, "modinfo", "-n", "sd")
}

func TestDKMS(t *testing.T) {
	built := func(driver string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			f.on("/lib/modules/5.10.0-35-amd64/updates/dkms/"+driver+".ko\n", 0, "modinfo", "-n", driver)
		}
	}
	secureBoot := func(on bool) func(*fakeProbe) {
		return func(f *fakeProbe) {
			value := "\x07\x00\x00\x00\x00"
			if on {
				value = "\x07\x00\x00\x00\x01"
			}
			f.set(secureBootVariable, value)
		}
	}
	runScenarios(t, "dkms", []scenario{
		{name: "no dkms", severity: OK},
		{name: "dkms and no module", setup: func(f *fakeProbe) { f.on("", 0, "dkms", "status") }, severity: OK},
		{name: "a module for a data pool", setup: dkmsBox, severity: Warn, data: D{"modules": []string{"zfs"}, "network": false, "disk": false, "secure_boot": false, "mok": false}},
		{name: "in the older form of dkms status", setup: after(dkmsBox, func(f *fakeProbe) {
			f.on("nvidia, 470.256.02, 5.10.0-35-amd64, x86_64: installed\nzfs, 2.0.3, 5.10.0-35-amd64, x86_64: installed\n", 0, "dkms", "status")
		}), severity: Warn, data: D{"modules": []string{"nvidia", "zfs"}}},
		{name: "a module that is the network card's driver", setup: after(dkmsBox, func(f *fakeProbe) {
			f.link("/sys/class/net/enp3s0/device/driver", "../../../bus/pci/drivers/r8168")
			built("r8168")(f)
		}), severity: Refuse, data: D{"network": true, "disk": false}},
		{name: "the card of a bridge", setup: after(dkmsBox, func(f *fakeProbe) {
			f.set("/proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\nbr0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\n")
			f.set("/sys/class/net/br0/brif/enp3s0/ifindex", "2")
			f.link("/sys/class/net/enp3s0/device/driver", "../../../bus/pci/drivers/r8168")
			built("r8168")(f)
		}), severity: Refuse, data: D{"network": true}},
		{name: "the controller of the disk", setup: after(dkmsBox, func(f *fakeProbe) { built("ahci")(f) }), severity: Refuse, data: D{"network": false, "disk": true}},
		{name: "a driver further up the bus than the disk", setup: after(dkmsBox, func(f *fakeProbe) {
			f.link("/sys/devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/driver", "../x/drivers/megaraid")
			built("megaraid")(f)
		}), severity: Refuse, data: D{"disk": true}},
		{name: "a driver of the kernel's own tree is not DKMS", setup: after(dkmsBox, func(f *fakeProbe) {
			f.on("/lib/modules/5.10.0-35-amd64/kernel/net/updates/dkms-not.ko\n", 0, "modinfo", "-n", "e1000e")
		}), severity: Warn},
		{name: "a driver that is not a module", setup: after(dkmsBox, func(f *fakeProbe) { f.on("", 1, "modinfo", "-n", "e1000e") }), severity: Warn, data: D{"network": false}},
		{name: "a driver that is built in", setup: after(dkmsBox, func(f *fakeProbe) { f.on("(builtin)\n", 0, "modinfo", "-n", "e1000e") }), severity: Warn, data: D{"network": false}},
		{name: "a driver whose name modinfo cannot be asked about is left out", setup: after(dkmsBox, func(f *fakeProbe) {
			f.link("/sys/class/net/enp3s0/device/driver", "../../../bus/pci/drivers/odd.name")
		}), severity: Warn, data: D{"network": false}},
		{name: "Secure Boot and a key", setup: after(dkmsBox, secureBoot(true), func(f *fakeProbe) { f.set("/var/lib/dkms/mok.pub", "key") }), severity: Warn, data: D{"secure_boot": true, "mok": true}},
		{name: "Secure Boot and no key", setup: after(dkmsBox, secureBoot(true)), severity: Refuse, data: D{"secure_boot": true, "mok": false, "network": false, "disk": false}},
		// the key is where the distribution's own dkms puts it: Debian's is /var/lib/dkms/mok.pub, Ubuntu's is
		// /var/lib/shim-signed/mok/MOK.der, and either can be put somewhere else in /etc/dkms/framework.conf
		{name: "Ubuntu, Secure Boot and the key shim-signed made", setup: after(ubuntu2204, dkmsBox, secureBoot(true), func(f *fakeProbe) { f.set("/var/lib/shim-signed/mok/MOK.der", "key") }), severity: Warn, data: D{"secure_boot": true, "mok": true}},
		{name: "Ubuntu, Secure Boot and no key", setup: after(ubuntu2204, dkmsBox, secureBoot(true)), severity: Refuse, data: D{"secure_boot": true, "mok": false}},
		{name: "Ubuntu, and the key is Debian's place for it", setup: after(ubuntu2204, dkmsBox, secureBoot(true), func(f *fakeProbe) { f.set("/var/lib/dkms/mok.pub", "key") }), severity: Refuse, data: D{"mok": false}},
		{name: "Debian, and the key is Ubuntu's place for it", setup: after(dkmsBox, secureBoot(true), func(f *fakeProbe) { f.set("/var/lib/shim-signed/mok/MOK.der", "key") }), severity: Refuse, data: D{"mok": false}},
		{name: "Ubuntu 24.04 too", setup: after(ubuntu2404, dkmsBox, secureBoot(true), func(f *fakeProbe) { f.set("/var/lib/shim-signed/mok/MOK.der", "key") }), severity: Warn, data: D{"mok": true}},
		{name: "a certificate that framework.conf names", setup: after(dkmsBox, secureBoot(true), func(f *fakeProbe) {
			f.set("/etc/dkms/framework.conf", "# dkms\nmok_signing_key=\"/etc/keys/mok.key\"\nmok_certificate=\"/etc/keys/mok.der\"\n")
			f.set("/etc/keys/mok.der", "key")
		}), severity: Warn, data: D{"mok": true}},
		{name: "a certificate that framework.conf names and is not there, with the default one there", setup: after(dkmsBox, secureBoot(true), func(f *fakeProbe) {
			f.set("/etc/dkms/framework.conf", "mok_certificate=/etc/keys/mok.der\n")
			f.set("/var/lib/dkms/mok.pub", "key")
		}), severity: Refuse, data: D{"mok": false}},
		{name: "a certificate named in a file of framework.conf.d, the last word wins", setup: after(ubuntu2204, dkmsBox, secureBoot(true), func(f *fakeProbe) {
			f.set("/etc/dkms/framework.conf", "mok_certificate=\"/etc/keys/first.der\"\n")
			f.set("/etc/dkms/framework.conf.d/10-local.conf", "# local\nmok_certificate='/etc/keys/second.der'\n")
			f.set("/etc/dkms/framework.conf.d/readme.txt", "mok_certificate=/etc/keys/ignored.der\n")
			f.set("/etc/keys/second.der", "key")
		}), severity: Warn, data: D{"mok": true}},
		{name: "a certificate that is commented out is not named", setup: after(dkmsBox, secureBoot(true), func(f *fakeProbe) {
			f.set("/etc/dkms/framework.conf", "#mok_certificate=\"/etc/keys/mok.der\"\n")
			f.set("/var/lib/dkms/mok.pub", "key")
		}), severity: Warn, data: D{"mok": true}},
		{name: "a certificate that is named by a path that goes back up", setup: after(dkmsBox, secureBoot(true), func(f *fakeProbe) {
			f.set("/etc/dkms/framework.conf", "mok_certificate=\"/etc/../var/lib/dkms/mok.pub\"\n")
			f.set("/etc/../var/lib/dkms/mok.pub", "key")
			f.set("/var/lib/dkms/mok.pub", "key")
		}), severity: Refuse, data: D{"mok": false}},
		{name: "a certificate that is named by a path that is not one", setup: after(dkmsBox, secureBoot(true), func(f *fakeProbe) {
			f.set("/etc/dkms/framework.conf", "mok_certificate=\"relative/mok.der\"\n")
			f.set("relative/mok.der", "key")
			f.set("/var/lib/dkms/mok.pub", "key")
		}), severity: Refuse, data: D{"mok": false}},
		{name: "a framework.conf that cannot be read", setup: after(dkmsBox, secureBoot(true), func(f *fakeProbe) {
			f.fail("/etc/dkms/framework.conf", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
		}), severity: Refuse, unknown: ReasonUnreadable},
		{name: "Secure Boot off and no key", setup: after(dkmsBox, secureBoot(false)), severity: Warn, data: D{"secure_boot": false}},
		{name: "an EFI variable that is not one", setup: after(dkmsBox, func(f *fakeProbe) { f.set(secureBootVariable, "\x07\x00") }), severity: Warn, data: D{"secure_boot": false}},
		{name: "no default route", setup: after(dkmsBox, func(f *fakeProbe) {
			f.set("/proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\nenp3s0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\n")
		}), severity: Warn},
		{name: "a route to a network of a mask is not the default one", setup: after(dkmsBox, func(f *fakeProbe) {
			// the card of that route has a driver that is built from source: it is not the default route's
			f.set("/proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\nenp4s0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00FFFFFF\n")
			f.link("/sys/class/net/enp4s0/device/driver", "../../../bus/pci/drivers/r8168")
			f.on("/lib/modules/5.10.0-35-amd64/updates/dkms/r8168.ko\n", 0, "modinfo", "-n", "r8168")
		}), severity: Warn, data: D{"network": false}},
		{name: "a route whose interface is not a name", setup: after(dkmsBox, func(f *fakeProbe) {
			f.set("/proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n../etc\t00000000\t00000000\t0001\t0\t0\t100\t00000000\n")
		}), severity: Warn},
		{name: "a root that is not on a device of sysfs", setup: after(dkmsBox, func(f *fakeProbe) { f.remove("/sys/dev/block/8:1") }), severity: Warn},
		{name: "no root in the mounts", setup: after(dkmsBox, func(f *fakeProbe) { f.set("/proc/self/mountinfo", "") }), severity: Warn},
		{name: "modinfo that is not there", setup: after(dkmsBox, func(f *fakeProbe) { f.onError(ErrToolMissing, "modinfo", "-n", "e1000e") }), severity: Refuse, unknown: ReasonToolMissing},
		{name: "dkms that fails", setup: func(f *fakeProbe) { f.on("x", 1, "dkms", "status") }, severity: Refuse, unknown: ReasonFailed},
		{name: "dkms that does not answer", setup: func(f *fakeProbe) { f.onError(context.DeadlineExceeded, "dkms", "status") }, severity: Refuse, unknown: ReasonTimeout},
		{name: "a link that cannot be read", setup: after(dkmsBox, func(f *fakeProbe) {
			f.fail("/sys/class/net/enp3s0/device/driver", context.DeadlineExceeded)
		}), severity: Refuse, unknown: ReasonTimeout},
	})
}

func TestHoldUntil(t *testing.T) {
	at := func(day string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			ubuntu2404(f)
			f.now, _ = time.Parse("2006-01-02", day)
		}
	}
	runScenarios(t, "hold_until", []scenario{
		{name: "ten days after it opened", setup: at("2026-10-10"), severity: Warn, data: D{"not_before": "2026-11-28"}, action: ActionNone},
		{name: "the day before it is let", setup: at("2026-11-27"), severity: Warn},
		{name: "the day it is let", setup: at("2026-11-28"), severity: OK},
		{name: "after it", setup: at("2027-03-01"), severity: OK},
	})
	// it is the hop of Ubuntu 24.04 to 26.04, and no other
	for _, setup := range []func(*fakeProbe){nil, debian12, ubuntu2204} {
		f := newBox(t)
		if setup != nil {
			setup(f)
		}
		for _, finding := range Run(context.Background(), f, Options{}).Checks {
			if finding.Code == "hold_until" {
				t.Fatal("hold_until ran on another hop")
			}
		}
	}
}

func TestNotOffered(t *testing.T) {
	for name, setup := range map[string]func(*fakeProbe){"22.04": ubuntu2204, "24.04": ubuntu2404} {
		t.Run(name, func(t *testing.T) {
			f := newBox(t)
			setup(f)
			if got := checkOn(t, f, "not_offered"); got.Severity != Info || got.Detail == "" || got.Action != ActionNone {
				t.Fatalf("finding = %+v", got)
			}
		})
	}
	for _, setup := range []func(*fakeProbe){nil, debian12} {
		f := newBox(t)
		if setup != nil {
			setup(f)
		}
		for _, finding := range Run(context.Background(), f, Options{}).Checks {
			if finding.Code == "not_offered" {
				t.Fatal("not_offered ran on a Debian")
			}
		}
	}
}

// The network card and the disk of a machine are not always the device that is named by the route and by
// the mount: a bond, a VLAN, a bridge, a logical volume and a RAID are built on the hardware, and it is the
// hardware that has the driver.
func TestDKMSFollowsTheStackToTheHardware(t *testing.T) {
	built := func(driver string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			f.on("/lib/modules/5.10.0-35-amd64/updates/dkms/"+driver+".ko\n", 0, "modinfo", "-n", driver)
		}
	}
	// the card of a stack: the physical card has the driver r8168, which is built from source
	r8168 := func(f *fakeProbe) {
		f.link("/sys/class/net/enp3s0/device/driver", "../../../bus/pci/drivers/r8168")
		built("r8168")(f)
	}
	route := func(iface string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			f.set("/proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n"+iface+"\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\n")
		}
	}
	lower := func(upper, card string) func(*fakeProbe) {
		return func(f *fakeProbe) { f.link("/sys/class/net/"+upper+"/lower_"+card, "../"+card) }
	}
	runScenarios(t, "dkms", []scenario{
		{name: "a bond of two cards, one of them is the one", setup: after(dkmsBox, route("bond0"), lower("bond0", "enp3s0"), lower("bond0", "enp4s0"), r8168), severity: Refuse, data: D{"network": true, "disk": false}},
		{name: "a bond of cards whose drivers are the kernel's", setup: after(dkmsBox, route("bond0"), lower("bond0", "enp3s0")), severity: Warn, data: D{"network": false}},
		{name: "a VLAN on a card", setup: after(dkmsBox, route("enp3s0.100"), lower("enp3s0.100", "enp3s0"), r8168), severity: Refuse, data: D{"network": true}},
		{name: "a VLAN on a bond on a card", setup: after(dkmsBox, route("bond0.100"), lower("bond0.100", "bond0"), lower("bond0", "enp3s0"), r8168), severity: Refuse, data: D{"network": true}},
		{name: "a bridge on a VLAN on a card", setup: after(dkmsBox, route("br0"), func(f *fakeProbe) {
			f.set("/sys/class/net/br0/brif/enp3s0.100/ifindex", "5")
		}, lower("enp3s0.100", "enp3s0"), r8168), severity: Refuse, data: D{"network": true}},
		{name: "a bridge on a card, which also has a lower link", setup: after(dkmsBox, route("br0"), func(f *fakeProbe) {
			f.set("/sys/class/net/br0/brif/enp3s0/ifindex", "2")
		}, lower("br0", "enp3s0"), r8168), severity: Refuse, data: D{"network": true}},
		{name: "a card that is the lower one of itself, and one of another", setup: after(dkmsBox, route("bond0"), lower("bond0", "bond0"), lower("bond0", "enp3s0"), r8168), severity: Refuse, data: D{"network": true}},
		{name: "a stack that is a ring", setup: after(dkmsBox, route("a0"), lower("a0", "b0"), lower("b0", "a0")), severity: Warn, data: D{"network": false}},
		{name: "a lower link whose name is not a card's", setup: after(dkmsBox, route("bond0"), func(f *fakeProbe) {
			f.link("/sys/class/net/bond0/lower_..", "../enp3s0")
			f.link("/sys/class/net/bond0/lower_a b", "../enp3s0")
			f.link("/sys/class/net/a b/device/driver", "../../../bus/pci/drivers/r8168")
		}, r8168, func(f *fakeProbe) { f.link("/sys/class/net/enp3s0/device/driver", "../../../bus/pci/drivers/e1000e") }), severity: Warn, data: D{"network": false}},
		{name: "a stack of cards deeper than a machine's, whose bottom is built from source", setup: after(dkmsBox, route("i0"), func(f *fakeProbe) {
			for i := 0; i < 9; i++ {
				f.link("/sys/class/net/i"+strconv.Itoa(i)+"/lower_i"+strconv.Itoa(i+1), "../x")
			}
			f.link("/sys/class/net/i9/device/driver", "../../../bus/pci/drivers/r8168")
		}, built("r8168")), severity: Warn, data: D{"network": false}},
		{name: "a stack of cards that is as deep as one is, whose bottom is built from source", setup: after(dkmsBox, route("i0"), func(f *fakeProbe) {
			for i := 0; i < 4; i++ {
				f.link("/sys/class/net/i"+strconv.Itoa(i)+"/lower_i"+strconv.Itoa(i+1), "../x")
			}
			f.link("/sys/class/net/i4/device/driver", "../../../bus/pci/drivers/r8168")
		}, built("r8168")), severity: Refuse, data: D{"network": true}},
		{name: "the links of a card that cannot be listed", setup: after(dkmsBox, route("bond0"), func(f *fakeProbe) {
			f.fail("/sys/class/net/bond0", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
		}), severity: Refuse, unknown: ReasonUnreadable},

		{name: "a root on LVM on a disk whose controller is built from source", setup: after(dkmsBox, lvmRoot("sda2"), built("ahci")), severity: Refuse, data: D{"disk": true, "network": false}},
		{name: "a root on LVM on a disk whose controller is the kernel's", setup: after(dkmsBox, lvmRoot("sda2")), severity: Warn, data: D{"disk": false}},
		{name: "a root on LVM on an encrypted volume on a disk", setup: after(dkmsBox, lvmRoot("dm-1"), func(f *fakeProbe) {
			f.link("/sys/class/block/dm-1", "../../devices/virtual/block/dm-1")
			f.link("/sys/devices/virtual/block/dm-1/slaves/sda2", "../../../../pci0000:00/x/block/sda/sda2")
		}, built("ahci")), severity: Refuse, data: D{"disk": true}},
		{name: "a root on a RAID of two disks, one of the controllers is built from source", setup: after(dkmsBox, raidRoot("sda1", "sdb1"), func(f *fakeProbe) {
			f.link("/sys/class/block/sdb1", "../../devices/pci0000:00/0000:00:18.0/ata2/host1/target1:0:0/1:0:0:0/block/sdb/sdb1")
			f.link("/sys/devices/pci0000:00/0000:00:18.0/driver", "../../../bus/pci/drivers/mpt3sas")
			f.on("/lib/modules/5.10.0-35-amd64/kernel/drivers/scsi/mpt3sas/mpt3sas.ko\n", 0, "modinfo", "-n", "mpt3sas")
		}, built("mpt3sas")), severity: Refuse, data: D{"disk": true}},
		{name: "a root on a RAID whose controllers are the kernel's", setup: after(dkmsBox, raidRoot("sda1")), severity: Warn, data: D{"disk": false}},
		{name: "a logical volume with no slaves", setup: after(dkmsBox, func(f *fakeProbe) {
			f.link("/sys/dev/block/8:1", "../../devices/virtual/block/dm-0")
			f.dirs["/sys/devices/virtual/block/dm-0"] = true
		}), severity: Warn, data: D{"disk": false}},
		{name: "a slave whose name is not a device's", setup: after(dkmsBox, func(f *fakeProbe) {
			f.link("/sys/dev/block/8:1", "../../devices/virtual/block/dm-0")
			f.link("/sys/devices/virtual/block/dm-0/slaves/..", "../../../../x")
			f.link("/sys/class/block/..", "../../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda/sda1")
		}, built("ahci")), severity: Warn, data: D{"disk": false}},
		{name: "a stack of devices deeper than a machine's, whose bottom is on a controller built from source", setup: after(dkmsBox, func(f *fakeProbe) {
			f.link("/sys/dev/block/8:1", "../../devices/virtual/block/dm-0")
			for i := 0; i < 9; i++ {
				f.link("/sys/devices/virtual/block/dm-"+strconv.Itoa(i)+"/slaves/dm-"+strconv.Itoa(i+1), "x")
				f.link("/sys/class/block/dm-"+strconv.Itoa(i+1), "../../devices/virtual/block/dm-"+strconv.Itoa(i+1))
			}
			f.link("/sys/devices/virtual/block/dm-9/slaves/sda1", "x")
			f.link("/sys/class/block/sda1", "../../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda/sda1")
		}, built("ahci")), severity: Warn, data: D{"disk": false}},
		{name: "a stack of devices as deep as one is, whose bottom is on a controller built from source", setup: after(dkmsBox, func(f *fakeProbe) {
			f.link("/sys/dev/block/8:1", "../../devices/virtual/block/dm-0")
			for i := 0; i < 4; i++ {
				f.link("/sys/devices/virtual/block/dm-"+strconv.Itoa(i)+"/slaves/dm-"+strconv.Itoa(i+1), "x")
				f.link("/sys/class/block/dm-"+strconv.Itoa(i+1), "../../devices/virtual/block/dm-"+strconv.Itoa(i+1))
			}
			f.link("/sys/devices/virtual/block/dm-4/slaves/sda1", "x")
			f.link("/sys/class/block/sda1", "../../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda/sda1")
		}, built("ahci")), severity: Refuse, data: D{"disk": true}},
		{name: "a link to a device that cannot be read", setup: after(dkmsBox, lvmRoot("sda2"), func(f *fakeProbe) {
			f.fail("/sys/class/block/sda2", &fs.PathError{Op: "readlink", Path: "x", Err: fs.ErrPermission})
		}), severity: Refuse, unknown: ReasonUnreadable},
		{name: "a stack of devices that is a ring", setup: after(dkmsBox, func(f *fakeProbe) {
			f.link("/sys/dev/block/8:1", "../../devices/virtual/block/dm-0")
			f.link("/sys/devices/virtual/block/dm-0/slaves/dm-1", "x")
			f.link("/sys/class/block/dm-1", "../../devices/virtual/block/dm-1")
			f.link("/sys/devices/virtual/block/dm-1/slaves/dm-0", "x")
			f.link("/sys/class/block/dm-0", "../../devices/virtual/block/dm-0")
		}), severity: Warn, data: D{"disk": false}},
		{name: "the slaves of a device that cannot be listed", setup: after(dkmsBox, lvmRoot("sda2"), func(f *fakeProbe) {
			f.fail("/sys/devices/virtual/block/dm-0/slaves", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
		}), severity: Refuse, unknown: ReasonUnreadable},
	})
}

// lvmRoot is a root on a logical volume (dm-0, 253:0) that is on the slave, a partition of the disk sda.
func lvmRoot(slave string) func(*fakeProbe) {
	return func(f *fakeProbe) {
		f.set("/proc/self/mountinfo", "22 1 253:0 / / rw,relatime shared:1 - ext4 /dev/mapper/vg-root rw\n")
		f.link("/sys/dev/block/253:0", "../../devices/virtual/block/dm-0")
		f.link("/sys/devices/virtual/block/dm-0/slaves/"+slave, "../../../../pci0000:00/x/block/sda/"+slave)
		f.link("/sys/class/block/sda2", "../../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda/sda2")
	}
}

// raidRoot is a root on a RAID (md0, 9:0) of the partitions: the first is on the disk of dkmsBox.
func raidRoot(slaves ...string) func(*fakeProbe) {
	return func(f *fakeProbe) {
		f.set("/proc/self/mountinfo", "22 1 9:0 / / rw,relatime shared:1 - ext4 /dev/md0 rw\n")
		f.link("/sys/dev/block/9:0", "../../devices/virtual/block/md0")
		for _, slave := range slaves {
			f.link("/sys/devices/virtual/block/md0/slaves/"+slave, "../../../../x/block/"+slave)
		}
		f.link("/sys/class/block/sda1", "../../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda/sda1")
	}
}

// A ring of interfaces, or of devices, is walked once: each one is asked what is under it a single time.
func TestAStackThatIsARingIsWalkedOnce(t *testing.T) {
	f := newBox(t)
	dkmsBox(f)
	f.set("/proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\na0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\n")
	f.link("/sys/class/net/a0/lower_b0", "../b0")
	f.link("/sys/class/net/b0/lower_a0", "../a0")
	f.link("/sys/dev/block/8:1", "../../devices/virtual/block/dm-0")
	f.link("/sys/devices/virtual/block/dm-0/slaves/dm-1", "x")
	f.link("/sys/class/block/dm-1", "../../devices/virtual/block/dm-1")
	f.link("/sys/devices/virtual/block/dm-1/slaves/dm-0", "x")
	f.link("/sys/class/block/dm-0", "../../devices/virtual/block/dm-0")
	if got := checkOn(t, f, "dkms"); got.Severity != Warn || got.Data["network"] != false || got.Data["disk"] != false {
		t.Fatalf("finding = %+v", got)
	}
	asked := map[string]int{}
	for _, read := range f.readPaths() {
		asked[read]++
	}
	for _, read := range []string{"readdir /sys/class/net/a0", "readdir /sys/class/net/b0", "readdir /sys/devices/virtual/block/dm-0/slaves", "readdir /sys/devices/virtual/block/dm-1/slaves"} {
		if asked[read] != 1 {
			t.Errorf("%s was asked %d times, want once", read, asked[read])
		}
	}
}
