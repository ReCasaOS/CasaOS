package osreadiness

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// The checks of the table as a whole: the report of a healthy box, and the commands that all of them
// together ask for.

func TestTheReportOfADebian11BoxWithEveryCheckOfTheTableIsNotBlocked(t *testing.T) {
	f := newBox(t)
	report := Run(context.Background(), f, Options{})
	if report.State != StateReady && report.State != StateAttention {
		t.Fatalf("a healthy box is %s: %v", report.State, codesOf(report))
	}
	if report.Hop == nil || report.Hop.From.Codename != "bullseye" || report.Hop.To.Codename != "bookworm" {
		t.Fatalf("hop = %+v", report.Hop)
	}
	for _, f := range report.Checks {
		if f.Data["unknown"] == true {
			t.Errorf("%s did not know: %v", f.Code, f.Data)
		}
	}
	// the sources of the box are Debian's: nothing is third-party
	if got := findingOf(t, report, "sources_third_party").Severity; got != OK {
		t.Errorf("sources_third_party = %s", got)
	}
	for _, code := range []string{"unsupported_os", "vendor_os", "unproven_arch", "container", "virt_xen", "no_systemd", "not_root",
		"busy", "apps", "reboot_first", "uptime_long", "broken_dpkg", "held", "held_docker",
		"sources_cdrom", "sources_alias", "sources_pin", "sources_proposed", "sources_format", "sources_third_party", "sources_backports", "sources_firmware", "sources_network_checked_later",
		"foreign_arch", "keyring_old", "host_services", "dkms"} {
		findingOf(t, report, code)
	}
	for _, code := range []string{"openssh_old", "hold_until", "not_offered"} {
		for _, f := range report.Checks {
			if f.Code == code {
				t.Errorf("%s ran on Debian 11: it is not of this hop", code)
			}
		}
	}
	if !strings.Contains(Text(report), "Debian 12 (bookworm)") {
		t.Errorf("text: %s", Text(report))
	}
}

// dirty is a machine that has something wrong with it in most of the ways the checks look for, so
// that the checks go through their refusals as well as their passes.
func dirty(f *fakeProbe) {
	f.busyReason = "another package manager holds dpkg's lock"
	f.apps = []string{"jellyfin"}
	f.set("/var/run/reboot-required", "")
	f.set("/var/lib/dpkg/updates/0001", "")
	f.on("The following packages are only half installed", 0, argvAudit...)
	f.on("E: Unmet dependencies. Try 'apt --fix-broken install'", 100, aptCheckArgs...)
	f.set("/etc/apt/sources.list", "deb cdrom:[Debian 11]/ bullseye main\ndeb http://deb.debian.org/debian stable main\ndeb http://deb.debian.org/debian bullseye-proposed-updates main\ndeb http://deb.debian.org/debian bullseye-backports main non-free\nthis is not a line\n")
	f.set("/etc/apt/sources.list.d/docker.list", "deb [arch=amd64] https://download.docker.com/linux/debian bullseye stable\n")
	f.set("/etc/apt/preferences.d/pin", "Package: *\nPin: release a=bullseye-backports\nPin-Priority: 900\n")
	f.on("Dir::Cache \"x\";\nAPT::Default-Release \"bullseye\";\n", 0, argvConfigDump...)
	f.on("5.15.0\n", 0, "uname", "-r")
	f.set("/boot/vmlinuz-5.10.0-99-amd64", "kernel")
	f.on("docker\n", 0, argvContainer...)
	f.on("xen\n", 0, argvVM...)
	f.set("/proc/1/comm", "init\n")
	f.euid = 1000
	f.on("ii \tpostgresql-13\tamd64\t13.14\nii \tpostgresql-client-13\tamd64\t13.14\n", 0, argvQuery(hostServicePatterns...)...)
	f.on(pkgArch("libc6", "i386", "2.31")+pkg("libc6", "2.31")+held("libc6")+held("nfs-common")+held("docker-ce"), 0, argvQuery()...)
	f.on("i386\n", 0, argvForeign...)
	f.on("zfs/2.0.3, 5.10.0-35-amd64, x86_64: installed\n", 0, "dkms", "status")
}

func TestNoCheckOfTheTableAsksForWhatIsNotAllowedOrCouldChangeTheMachine(t *testing.T) {
	boxes := map[string]func(*fakeProbe){
		"a clean Debian 11":    nil,
		"a dirty Debian 11":    dirty,
		"a clean Debian 12":    debian12,
		"a clean Ubuntu 22.04": ubuntu2204,
		"a clean Ubuntu 24.04": ubuntu2404,
		"a dirty Debian 12":    func(f *fakeProbe) { debian12(f); dirty(f) },
	}
	for name, setup := range boxes {
		t.Run(name, func(t *testing.T) {
			f := newBox(t)
			if setup != nil {
				setup(f)
			}
			report := Run(context.Background(), f, Options{})
			for _, finding := range report.Checks {
				if finding.Data["reason"] == ReasonNotAllowed {
					t.Errorf("%s asked for something that is not allowed", finding.Code)
				}
			}
			for _, argv := range f.attemptedCommands() {
				if !Allowed(argv) {
					t.Errorf("the machine was asked to run %q", argv)
				}
			}
			// what was read is read: nothing else can have been done through this seam
			for _, op := range f.readPaths() {
				verb, _, _ := strings.Cut(op, " ")
				if !slices.Contains([]string{"read", "readdir", "stat", "readlink", "free", "busy", "apps"}, verb) {
					t.Errorf("%q is not a way of reading", op)
				}
			}
		})
	}
}
