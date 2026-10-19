package osplan

import (
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// What apt says, with --no-upgrade, of a package it is asked to install that is installed. The first line is the one a real run printed (apt of a Debian or Ubuntu guest, in the plan of the
// update of a module whose packages were all there); the text of the line is the string of TryToInstall in apt-private/private-install.cc, the same in apt 2.2.4 (Debian 11) and 2.6.1
// (Debian 12), read from their sources; apt 2.4 and 2.7 (Ubuntu 22.04, 24.04) could not be read from here. No apt was at hand to run (the host has none, and neither has its Linux).
const (
	skipLine    = "Skipping dnsmasq-base, it is already installed and upgrade is not set.\n"
	skipSummary = "0 upgraded, 0 newly installed, 0 to remove and 12 not upgraded.\n"
)

func TestAPackageThatAptSkipsBecauseItIsInstalledIsThere(t *testing.T) {
	names := []string{"dnsmasq-base", "libvirt-clients"}
	sim := progressLines + skipLine + "Skipping libvirt-clients, it is already installed and upgrade is not set.\n" + skipSummary
	res := ParseModuleSimulation(out(sim, 0), out(sim, 0), names)
	if res.Failure != "" || len(res.ToInstall) != 0 || len(res.ToUpgrade) != 0 || !res.Known {
		t.Fatalf("%+v", res)
	}
	// among packages that are new, and beside one that is the newest version
	sim = progressLines + skipLine + "libvirt-clients is already the newest version (9.0.0-4).\nThe following NEW packages will be installed:\n  swtpm\n" +
		"0 upgraded, 1 newly installed, 0 to remove and 12 not upgraded.\nInst swtpm (0.8.0 Debian:12.15/oldstable [amd64])\n"
	est := progressLines + skipLine + "libvirt-clients is already the newest version (9.0.0-4).\nThe following NEW packages will be installed:\n  swtpm\n" +
		"0 upgraded, 1 newly installed, 0 to remove and 12 not upgraded.\nNeed to get 1 MB of archives.\nAfter this operation, 3 MB of additional disk space will be used.\n" +
		"'http://deb.debian.org/debian/pool/main/s/swtpm/swtpm_0.8.0_amd64.deb' swtpm_0.8.0_amd64.deb 1000000 SHA256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n"
	res = ParseModuleSimulation(out(sim, 0), out(est, 0), append(names, "swtpm"))
	if res.Failure != "" || len(res.ToInstall) != 1 || res.ToInstall[0] != "swtpm" || !res.Known {
		t.Errorf("%+v", res)
	}
}

// A name apt skipped is a name that came back under its own name; one that apt resolved to another package, or never mentioned, is not exact.
func TestASkippedPackageIsOnlyThePackageThatWasAskedFor(t *testing.T) {
	sim := progressLines + skipLine + skipSummary
	for name, asked := range map[string][]string{
		"a name apt did not mention":    {"dnsmasq-base", "libvirt-clients"},
		"another name than the skipped": {"dnsmasq"},
	} {
		res := ParseModuleSimulation(out(sim, 0), out(sim, 0), asked)
		if res.Failure != osmodule.RefusedPackageNotExact {
			t.Errorf("%s: %+v", name, res)
		}
	}
}

// Every other line that looks like it stays an error, and names the line.
func TestALineLikeTheSkipOfAptThatIsNotItIsRefusedAndNamed(t *testing.T) {
	for _, bad := range []string{
		"Skipping dnsmasq-base, it is not installed and only upgrades are requested.",
		"Skipping dnsmasq-base, it is held.",
		"Skipping dnsmasq-base, it is already installed and upgrade is not set. ",
		"Skipping dnsmasq-base, it is already installed and upgrade is not set.x",
		"skipping dnsmasq-base, it is already installed and upgrade is not set.",
		"Skipping Dnsmasq-Base, it is already installed and upgrade is not set.",
		"Skipping $(id), it is already installed and upgrade is not set.",
		"Skipping a b, it is already installed and upgrade is not set.",
		"Skipping , it is already installed and upgrade is not set.",
		"Skipping dnsmasq-base it is already installed and upgrade is not set.",
		"Skipping dnsmasq-base, it is already installed and upgrade is not setX",
		"x Skipping dnsmasq-base, it is already installed and upgrade is not set.",
		"Warning: Skipping dnsmasq-base, it is already installed and upgrade is not set.",
	} {
		sim := progressLines + bad + "\n" + skipSummary
		res := ParseModuleSimulation(out(sim, 0), out(sim, 0), []string{"dnsmasq-base"})
		if res.Failure != osmodule.RefusedAptFailed || !strings.Contains(strings.ToLower(res.Detail), "skipping") {
			t.Errorf("%q: %+v", bad, res)
		}
	}
}

// The simulation of an install has no --no-upgrade, and says nothing of skipping: the line is accepted all the same wherever it is read, but the install's argv is what it was.
func TestTheSimulationOfAnInstallIsStillWithoutNoUpgrade(t *testing.T) {
	argv, err := ModuleSimArgv(testDir, []string{"xx"})
	if err != nil || strings.Contains(strings.Join(argv, " "), "--no-upgrade") {
		t.Errorf("%q %v", argv, err)
	}
	argv, err = ModuleSimArgvNoUpgrade(testDir, []string{"xx"})
	if err != nil || !strings.Contains(strings.Join(argv, " "), "--no-install-recommends --no-upgrade xx") {
		t.Errorf("%q %v", argv, err)
	}
}
