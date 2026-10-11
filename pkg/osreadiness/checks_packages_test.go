package osreadiness

import (
	"context"
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
			data: D{"architectures": []string{"i386"}, "packages": []string{"libc6"}, "count": 1}, action: ActionReviewSources},
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
