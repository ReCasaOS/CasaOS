package osreadiness

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// the sources of a machine, in the files apt reads
func sourcesList(lines ...string) func(*fakeProbe) {
	return func(f *fakeProbe) { f.set("/etc/apt/sources.list", strings.Join(lines, "\n")+"\n") }
}

func sourcesFile(name string, lines ...string) func(*fakeProbe) {
	return func(f *fakeProbe) { f.set("/etc/apt/sources.list.d/"+name, strings.Join(lines, "\n")+"\n") }
}

func after(changes ...func(*fakeProbe)) func(*fakeProbe) {
	return func(f *fakeProbe) {
		for _, change := range changes {
			change(f)
		}
	}
}

const (
	dockerLine    = "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian bullseye stable"
	tailscaleLine = "deb [signed-by=/usr/share/keyrings/tailscale.gpg] https://pkgs.tailscale.com/stable/debian bullseye main"
)

func TestSourcesCDROM(t *testing.T) {
	runScenarios(t, "sources_cdrom", []scenario{
		{name: "Debian's archives", severity: OK},
		{name: "a CD-ROM", setup: sourcesList("deb cdrom:[Debian GNU/Linux 11.0.0 _Bullseye_ - Official amd64 DVD Binary-1]/ bullseye contrib main"),
			severity: Refuse, data: D{"files": []string{"sources.list"}}, action: ActionReviewSources},
		{name: "a local directory", setup: sourcesFile("local.list", "deb file:/srv/mirror/debian bullseye main"), severity: Refuse, data: D{"files": []string{"local.list"}}},
		{name: "a copy", setup: sourcesFile("copy.list", "deb copy:/srv/mirror bullseye main"), severity: Refuse},
		{name: "in a .sources file", setup: sourcesFile("local.sources", "Types: deb", "URIs: file:///srv/mirror", "Suites: bullseye", "Components: main"), severity: Refuse, data: D{"files": []string{"local.sources"}}},
		{name: "a disc that is commented out", setup: sourcesList("# deb cdrom:[Debian 11]/ bullseye main", "deb http://deb.debian.org/debian bullseye main"), severity: OK},
		{name: "a disc that is not enabled", setup: sourcesFile("local.sources", "Types: deb", "URIs: file:///srv/mirror", "Suites: bullseye", "Enabled: no"), severity: OK},
		{name: "a disc that is switched off the other ways apt reads it", setup: after(sourcesFile("a.sources", "Types: deb", "URIs: file:///srv/a", "Suites: bullseye", "Enabled: off"), sourcesFile("b.sources", "Types: deb", "URIs: file:///srv/b", "Suites: bullseye", "Enabled: without")), severity: OK},
		{name: "a source in a file that has a colon in its name, which apt reads", setup: sourcesFile("local:mirror.list", "deb file:/srv/mirror/debian bullseye main"), severity: Refuse, data: D{"files": []string{"local:mirror.list"}}},
		{name: "a file that starts with a dot, which apt does not read", setup: sourcesFile(".local.list", "deb file:/srv/mirror/debian bullseye main"), severity: OK},
		{name: "a file turned off with a link to /dev/null, as people do", setup: func(f *fakeProbe) {
			f.device("/dev/null")
			f.link("/etc/apt/sources.list.d/local.list", "/dev/null")
		}, severity: OK},
		{name: "a file that cannot be understood is not looked into", setup: sourcesList("rubbish"), severity: Refuse, unknown: ReasonUnreadable},
		{name: "a disc next to a file that cannot be understood is still said", setup: after(sourcesList("rubbish"), sourcesFile("cd.list", "deb cdrom:[x]/ bullseye main")), severity: Refuse, data: D{"files": []string{"cd.list"}}},
		{name: "two files, once each", setup: after(sourcesList("deb cdrom:[x]/ bullseye main", "deb cdrom:[y]/ bullseye main"), sourcesFile("a.list", "deb file:/x bullseye main")), severity: Refuse, data: D{"files": []string{"a.list", "sources.list"}}},
		{name: "a file that cannot be read", setup: func(f *fakeProbe) {
			f.fail("/etc/apt/sources.list.d", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
		{name: "a list that does not answer in time", setup: func(f *fakeProbe) { f.fail("/etc/apt/sources.list", context.DeadlineExceeded) }, severity: Refuse, unknown: ReasonTimeout},
	})
}

func TestSourcesAlias(t *testing.T) {
	runScenarios(t, "sources_alias", []scenario{
		{name: "codenames", severity: OK},
		{name: "stable", setup: sourcesList("deb http://deb.debian.org/debian stable main"), severity: Refuse, data: D{"files": []string{"sources.list"}, "suites": []string{"stable"}}, action: ActionReviewSources},
		{name: "oldstable and its updates, once each", setup: sourcesList("deb http://deb.debian.org/debian oldstable main", "deb http://deb.debian.org/debian oldstable-updates main", "deb http://deb.debian.org/debian oldstable main"),
			severity: Refuse, data: D{"suites": []string{"oldstable", "oldstable-updates"}}},
		{name: "the suites of a third party's own are not said with the archive's", setup: sourcesList("deb http://deb.debian.org/debian stable main", "deb https://repo.example.org/apt testing main"), severity: Refuse, data: D{"suites": []string{"stable"}}},
		{name: "the old name of the security archive", setup: sourcesList("deb http://security.debian.org stable/updates main"), severity: Refuse, data: D{"suites": []string{"stable/updates"}}},
		{name: "testing", setup: sourcesList("deb http://deb.debian.org/debian testing main"), severity: Refuse},
		{name: "unstable", setup: sourcesList("deb http://ftp.us.debian.org/debian unstable main"), severity: Refuse},
		{name: "devel on Ubuntu", setup: after(ubuntu2204, sourcesList("deb http://archive.ubuntu.com/ubuntu devel main")), severity: Refuse},
		{name: "in a .sources file", setup: sourcesFile("debian.sources", "Types: deb", "URIs: http://deb.debian.org/debian", "Suites: bookworm stable", "Components: main"), severity: Refuse, data: D{"suites": []string{"stable"}}},
		{name: "a mirror of the archive", setup: sourcesList("deb http://mirror.example.edu/debian stable main"), severity: Refuse},
		// the vendor's own suite named stable: not the archive's alias
		{name: "a third party's stable", setup: sourcesFile("code.list", "deb https://packages.microsoft.com/repos/code stable main"), severity: OK},
		{name: "a third party's stable at a path that ends in debian", setup: sourcesFile("x.list", "deb https://repo.example.org/stable/debian stable main"), severity: OK},
		{name: "an alias that is commented out", setup: sourcesList("#deb http://deb.debian.org/debian stable main", "deb http://deb.debian.org/debian bullseye main"), severity: OK},
		// a file that was not understood may hold what this looks for: it is not a pass for the others, and
		// sources_format says what is wrong with the file
		{name: "a file that cannot be understood is not looked into", setup: sourcesList("rubbish"), severity: Refuse, unknown: ReasonUnreadable},
		{name: "an alias of a mirror at a path of its own", setup: sourcesList("deb http://mirror.hetzner.com/debian/packages stable main"), severity: Refuse, data: D{"files": []string{"sources.list"}, "suites": []string{"stable"}}},
		{name: "an alias of a mirror's security archive", setup: sourcesFile("sec.list", "deb http://mirror.hetzner.com/debian/security stable-security main"), severity: Refuse, data: D{"suites": []string{"stable-security"}}},
		{name: "an alias of a mirror that keeps the archive under the name of its host", setup: sourcesList("deb http://mirror.ovh.net/ftp.debian.org/debian oldstable main"), severity: Refuse},
		{name: "an alias that is found in a file next to one that cannot be read", setup: after(sourcesList("rubbish"), sourcesFile("a.list", "deb http://deb.debian.org/debian testing main")), severity: Refuse, data: D{"files": []string{"a.list"}}},
	})
}

func TestSourcesProposed(t *testing.T) {
	runScenarios(t, "sources_proposed", []scenario{
		{name: "no proposed updates", severity: OK},
		{name: "Debian's", setup: sourcesList("deb http://deb.debian.org/debian bullseye-proposed-updates main"), severity: Refuse, data: D{"files": []string{"sources.list"}, "suites": []string{"bullseye-proposed-updates"}}, action: ActionReviewSources},
		{name: "Ubuntu's", setup: after(ubuntu2204, sourcesList("deb http://archive.ubuntu.com/ubuntu jammy-proposed main")), severity: Refuse, data: D{"suites": []string{"jammy-proposed"}}},
		{name: "in a .sources file", setup: sourcesFile("p.sources", "Types: deb", "URIs: http://deb.debian.org/debian", "Suites: bullseye-proposed-updates", "Components: main"), severity: Refuse},
		{name: "a mirror's, at a path of its own", setup: sourcesList("deb http://mirror.hetzner.com/debian/packages bullseye-proposed-updates main"), severity: Refuse, data: D{"suites": []string{"bullseye-proposed-updates"}}},
		{name: "a file that cannot be understood is not looked into", setup: sourcesList("rubbish"), severity: Refuse, unknown: ReasonUnreadable},
		{name: "the proposed suites of a third party's own are not said with the archive's", setup: sourcesList("deb http://deb.debian.org/debian bullseye-proposed-updates main", "deb https://repo.example.org/apt jammy-proposed main"), severity: Refuse, data: D{"suites": []string{"bullseye-proposed-updates"}}},
		{name: "a third party's suite of that name", setup: sourcesFile("x.list", "deb https://repo.example.org/apt jammy-proposed main"), severity: OK},
		{name: "backports are not proposed", setup: sourcesList("deb http://deb.debian.org/debian bullseye-backports main"), severity: OK},
	})
}

func TestSourcesFormat(t *testing.T) {
	runScenarios(t, "sources_format", []scenario{
		{name: "every file can be read", severity: OK},
		{name: "a line that is not a source", setup: sourcesList("deb http://deb.debian.org/debian bullseye main", "this is not a line"), severity: Refuse, data: D{"files": []string{"sources.list"}}, action: ActionReviewSources},
		{name: "a .list file with options that do not close", setup: sourcesFile("x.list", "deb [arch=amd64 http://x bullseye main"), severity: Refuse, data: D{"files": []string{"x.list"}}},
		{name: "a stanza without suites", setup: sourcesFile("x.sources", "Types: deb", "URIs: http://x"), severity: Refuse, data: D{"files": []string{"x.sources"}}},
		{name: "two files, sorted by how they were read", setup: after(sourcesList("rubbish"), sourcesFile("b.list", "rubbish"), sourcesFile("a.sources", "rubbish")), severity: Refuse, data: D{"files": []string{"sources.list", "a.sources", "b.list"}}},
		{name: "a file too large to read", setup: sourcesFile("big.list", strings.Repeat("# a long comment ", 70000)), severity: Refuse, data: D{"files": []string{"big.list"}}},
		{name: "a file that cannot be opened", setup: func(f *fakeProbe) {
			f.set("/etc/apt/sources.list.d/locked.list", "deb http://deb.debian.org/debian bullseye main")
			f.fail("/etc/apt/sources.list.d/locked.list", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
		}, severity: Refuse, data: D{"files": []string{"locked.list"}}},
		{name: "a file that apt ignores, whatever is in it", setup: sourcesFile("old.list.save", "rubbish"), severity: OK},
		{name: "a file that apt reads, with a colon in its name, and that cannot be understood", setup: sourcesFile("a:b.list", "rubbish"), severity: Refuse, data: D{"files": []string{"a:b.list"}}},
		{name: "a file that is a link to /dev/null is not a file that cannot be read", setup: func(f *fakeProbe) {
			f.device("/dev/null")
			f.link("/etc/apt/sources.list.d/google-chrome.list", "/dev/null")
		}, severity: OK},
		{name: "a directory with the name of a source is no source", setup: func(f *fakeProbe) { f.dirs["/etc/apt/sources.list.d/dir.list"] = true }, severity: OK},
		{name: "no sources.list, as on Debian 13", setup: func(f *fakeProbe) {
			f.remove("/etc/apt/sources.list")
			sourcesFile("debian.sources", "Types: deb", "URIs: http://deb.debian.org/debian", "Suites: bullseye", "Components: main")(f)
		}, severity: OK},
		{name: "no source at all", setup: func(f *fakeProbe) { f.remove("/etc/apt/sources.list") }, severity: OK},
	})
}

func TestSourcesThirdParty(t *testing.T) {
	runScenarios(t, "sources_third_party", []scenario{
		{name: "Debian's archives only", severity: OK},
		// no action fits: Docker's source is on every machine of ReCasaOS and is handled by the hop, and checking
		// again never makes it go
		{name: "Docker's", setup: sourcesFile("docker.list", dockerLine), severity: Warn,
			data: D{"files": []string{"docker.list"}, "hosts": []string{"download.docker.com"}, "docker_files": []string{"docker.list"}}, action: ActionNone},
		{name: "another has no action either", setup: sourcesFile("tailscale.list", tailscaleLine), severity: Warn, action: ActionNone},
		{name: "Docker's, and another", setup: after(sourcesFile("docker.list", dockerLine), sourcesFile("tailscale.list", tailscaleLine)), severity: Warn,
			data: D{"files": []string{"docker.list", "tailscale.list"}, "hosts": []string{"download.docker.com", "pkgs.tailscale.com"}, "docker_files": []string{"docker.list"}}},
		{name: "another, not Docker's", setup: sourcesFile("tailscale.list", tailscaleLine), severity: Warn, data: D{"files": []string{"tailscale.list"}}},
		{name: "in a .sources file", setup: sourcesFile("syncthing.sources", "Types: deb", "URIs: https://apt.syncthing.net/", "Suites: syncthing", "Components: stable"), severity: Warn, data: D{"files": []string{"syncthing.sources"}}},
		{name: "a PPA", setup: after(ubuntu2204, sourcesFile("ppa.list", "deb https://ppa.launchpadcontent.net/x/y/ubuntu jammy main")), severity: Warn},
		{name: "a mirror of the archive is Debian's", setup: sourcesFile("mirror.list", "deb http://mirror.example.edu/debian bullseye main"), severity: OK},
		{name: "a mirror that has the archive under a path of its own is Debian's", setup: after(sourcesFile("hetzner.list", "deb http://mirror.hetzner.com/debian/packages bullseye main"), sourcesFile("sec.list", "deb http://mirror.hetzner.com/debian/security bullseye-security main")), severity: OK},
		{name: "a file that cannot be understood may hide a source that is not Debian's", setup: sourcesList("rubbish"), severity: Refuse, unknown: ReasonUnreadable},
		{name: "a third party is said whatever else cannot be read", setup: after(sourcesList("rubbish"), sourcesFile("docker.list", dockerLine)), severity: Warn, data: D{"files": []string{"docker.list"}}},
		{name: "Ubuntu's own", setup: after(ubuntu2204, sourcesList("deb http://archive.ubuntu.com/ubuntu jammy main", "deb http://security.ubuntu.com/ubuntu jammy-security main"), sourcesFile("esm.list", "deb https://esm.ubuntu.com/infra/ubuntu jammy-infra-security main")), severity: OK},
		{name: "a disc is not a third party", setup: sourcesList("deb cdrom:[x]/ bullseye main"), severity: OK},
		{name: "a third party that is commented out", setup: sourcesFile("docker.list", "# "+dockerLine), severity: OK},
		{name: "the user and the password of an address are not shown", setup: sourcesFile("private.list", "deb https://user:secret@repo.example.org:8443/apt bullseye main"), severity: Warn, data: D{"hosts": []string{"repo.example.org"}}},
	})
}

func TestSourcesBackports(t *testing.T) {
	runScenarios(t, "sources_backports", []scenario{
		{name: "no backports", severity: OK},
		{name: "Debian's", setup: sourcesList("deb http://deb.debian.org/debian bullseye main", "deb http://deb.debian.org/debian bullseye-backports main"), severity: Info, data: D{"files": []string{"sources.list"}}, action: ActionNone},
		{name: "in a file of its own", setup: sourcesFile("backports.list", "deb http://deb.debian.org/debian bullseye-backports main contrib"), severity: Info, data: D{"files": []string{"backports.list"}}},
		{name: "the sloppy ones", setup: sourcesFile("sloppy.list", "deb http://deb.debian.org/debian bullseye-backports-sloppy main"), severity: Info},
		{name: "a third party's", setup: sourcesFile("x.list", "deb https://repo.example.org/apt bullseye-backports main"), severity: OK},
		{name: "a mirror's, at a path of its own", setup: sourcesFile("m.list", "deb http://mirror.hetzner.com/debian/packages bullseye-backports main"), severity: Info, data: D{"files": []string{"m.list"}}},
		{name: "a file that cannot be understood is not looked into", setup: sourcesList("rubbish"), severity: Refuse, unknown: ReasonUnreadable},
	})
}

func TestSourcesFirmware(t *testing.T) {
	nonFree := sourcesList("deb http://deb.debian.org/debian bullseye main contrib non-free", "deb http://security.debian.org/debian-security bullseye-security main non-free")
	runScenarios(t, "sources_firmware", []scenario{
		{name: "main only, and no firmware", severity: OK},
		{name: "non-free is used", setup: nonFree, severity: Info, data: D{"non_free": true}, action: ActionNone},
		{name: "non-free-firmware is already there", setup: sourcesList("deb http://deb.debian.org/debian bullseye main non-free non-free-firmware"), severity: OK},
		{name: "firmware is installed, and the sources have main only", setup: func(f *fakeProbe) {
			f.installed(pkg("firmware-iwlwifi", "20210315-3")+pkg("firmware-misc-nonfree", "20210315-3"), firmwarePatterns...)
		}, severity: Info, data: D{"non_free": false, "packages": []string{"firmware-iwlwifi", "firmware-misc-nonfree"}}},
		{name: "microcode is installed", setup: func(f *fakeProbe) { f.installed(pkg("intel-microcode", "3.20230808.1~deb11u1"), firmwarePatterns...) }, severity: Info, data: D{"packages": []string{"intel-microcode"}}},
		{name: "the free firmware is in main already", setup: func(f *fakeProbe) { f.installed(pkg("firmware-linux-free", "20210315-3"), firmwarePatterns...) }, severity: OK},
		{name: "a package of a name that is neither", setup: func(f *fakeProbe) {
			f.installed(pkg("firmwarefoo", "1")+pkg("microcode-tool", "1"), firmwarePatterns...)
		}, severity: OK},
		{name: "firmware that was removed", setup: func(f *fakeProbe) {
			f.installed("rc \tfirmware-iwlwifi\tamd64\t1\nun \tfirmware-misc-nonfree\tamd64\t1\n", firmwarePatterns...)
		}, severity: OK},
		{name: "a file that cannot be understood is not looked into", setup: sourcesList("rubbish"), severity: Refuse, unknown: ReasonUnreadable},
		{name: "firmware, and the source of the archive is a third party's", setup: after(
			sourcesList("deb https://repo.example.org/apt bullseye main"),
			func(f *fakeProbe) { f.installed(pkg("firmware-iwlwifi", "1"), firmwarePatterns...) }), severity: OK},
		{name: "dpkg-query that fails", setup: func(f *fakeProbe) { f.on("dpkg-query: error\n", 2, argvQuery(firmwarePatterns...)...) }, severity: Refuse, unknown: ReasonFailed},
		{name: "no dpkg-query", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvQuery(firmwarePatterns...)...) }, severity: Refuse, unknown: ReasonToolMissing},
		{name: "Debian 12 with non-free and no non-free-firmware", setup: after(debian12, sourcesList("deb http://deb.debian.org/debian bookworm main non-free")), severity: Info},
	})
}

func TestSourcesFirmwareIsDebiansAndNotUbuntus(t *testing.T) {
	f := newBox(t)
	ubuntu2204(f)
	report := Run(context.Background(), f, Options{})
	for _, finding := range report.Checks {
		if finding.Code == "sources_firmware" || finding.Code == "keyring_old" {
			t.Errorf("%s ran on Ubuntu", finding.Code)
		}
	}
}

func TestSourcesPin(t *testing.T) {
	pref := func(name string, lines ...string) func(*fakeProbe) {
		return func(f *fakeProbe) { f.set("/etc/apt/preferences.d/"+name, strings.Join(lines, "\n")+"\n") }
	}
	runScenarios(t, "sources_pin", []scenario{
		{name: "nothing is pinned", severity: OK},
		{name: "a default release", setup: func(f *fakeProbe) { f.on("APT::Default-Release \"bullseye-backports\";\n", 0, argvConfigDump...) },
			severity: Refuse, data: D{"default_release": "bullseye-backports"}, action: ActionReviewSources},
		{name: "a default release that is not a name", setup: func(f *fakeProbe) { f.on("APT::Default-Release \"<b>x</b>\";\n", 0, argvConfigDump...) },
			severity: Refuse, data: D{"default_release_set": true}},
		{name: "an empty default release", setup: func(f *fakeProbe) { f.on("APT::Default-Release \"\";\n", 0, argvConfigDump...) }, severity: OK},
		// apt's keys do not care for case, and apt-config dump says them as they were typed
		{name: "a default release, in the case the owner typed it", setup: func(f *fakeProbe) { f.on("APT::default-release \"stable\";\n", 0, argvConfigDump...) }, severity: Refuse, data: D{"default_release": "stable"}},
		{name: "a default release, in capitals", setup: func(f *fakeProbe) { f.on("APT::DEFAULT-RELEASE \"bookworm\";\n", 0, argvConfigDump...) }, severity: Refuse, data: D{"default_release": "bookworm"}},
		{name: "a default release of another key is not one", setup: func(f *fakeProbe) {
			f.on("APT::Default-Releases \"stable\";\nX::APT::Default-Release \"stable\";\n", 0, argvConfigDump...)
		}, severity: OK},
		{name: "a pin in a file that has a colon in its name", setup: pref("pin:stable", "Package: *", "Pin: release a=stable", "Pin-Priority: 900"), severity: Refuse, data: D{"files": []string{"pin:stable"}}},
		{name: "a pin in a file that starts with a dot, which apt does not read", setup: after(pref(".pin", "Package: *", "Pin: release a=stable"), pref(".pin.pref", "Package: *", "Pin: release a=stable")), severity: OK},
		{name: "a pin file turned off with a link to /dev/null", setup: func(f *fakeProbe) {
			f.device("/dev/null")
			f.link("/etc/apt/preferences.d/pin", "/dev/null")
		}, severity: OK},
		{name: "a release is pinned", setup: pref("backports", "Package: *", "Pin: release a=bullseye-backports", "Pin-Priority: 900"), severity: Refuse, data: D{"files": []string{"backports"}}},
		{name: "a pin by codename, with spaces", setup: pref("x.pref", "Package: libc6", "pin:  Release  n=bookworm, l=Debian", "Pin-Priority: 500"), severity: Refuse, data: D{"files": []string{"x.pref"}}},
		{name: "the main file of preferences", setup: func(f *fakeProbe) {
			f.set("/etc/apt/preferences", "Package: *\nPin: release o=Debian,a=stable\nPin-Priority: 100\n")
		}, severity: Refuse, data: D{"files": []string{"preferences"}}},
		{name: "a pin of a version is not a pin of a release", setup: pref("v", "Package: libc6", "Pin: version 2.31*", "Pin-Priority: 1001"), severity: OK},
		{name: "a pin of an origin is not a pin of a release", setup: pref("o", "Package: *", "Pin: origin deb.debian.org", "Pin-Priority: 600"), severity: OK},
		{name: "Ubuntu Pro's own pin", setup: pref("ubuntu-pro-esm-infra", "# This file is automatically generated", "Package: *", "Pin: release o=UbuntuESM", "Pin-Priority: 510"), severity: OK},
		{name: "Ubuntu Pro's pin by label", setup: pref("ubuntu-pro-esm-apps", "Package: *", "Pin: release l=UbuntuESMApps", "Pin-Priority: 510"), severity: OK},
		{name: "a pin that has Ubuntu Pro's label among others", setup: pref("mixed", "Package: *", "Pin: release l=UbuntuESM,n=jammy", "Pin-Priority: 510"), severity: Refuse},
		{name: "a file that apt does not read", setup: after(pref("pin.bak", "Pin: release a=stable"), pref("pin~", "Pin: release a=stable"), pref("pin.dpkg-old", "Pin: release a=stable")), severity: OK},
		{name: "a pin in a comment is no pin", setup: pref("c", "# Pin: release a=stable", "Package: *"), severity: OK},
		{name: "apt-config that fails", setup: func(f *fakeProbe) { f.on("E: bad\n", 100, argvConfigDump...) }, severity: Refuse, unknown: ReasonFailed},
		{name: "no apt-config", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvConfigDump...) }, severity: Refuse, unknown: ReasonToolMissing},
		{name: "a preferences file that cannot be read", setup: func(f *fakeProbe) {
			pref("locked", "x")(f)
			f.fail("/etc/apt/preferences.d/locked", errors.New("input/output error"))
		}, severity: Refuse, unknown: ReasonFailed},
	})
}

func TestSourcesNetworkCheckedLater(t *testing.T) {
	for _, setup := range []func(*fakeProbe){nil, debian12, ubuntu2204, ubuntu2404} {
		f := newBox(t)
		if setup != nil {
			setup(f)
		}
		got := checkOn(t, f, "sources_network_checked_later")
		if got.Severity != Info || got.Detail == "" || got.Data["unknown"] != false {
			t.Fatalf("finding = %+v", got)
		}
	}
}
