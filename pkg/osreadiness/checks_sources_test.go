package osreadiness

import (
	"context"
	"errors"
	"io/fs"
	"slices"
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

// the stanza of a mirror that is not Debian's own host, with the suites a Debian box lists, and the Release files apt kept of
// them: Debian's say Origin: Debian of the archive and of its updates, and Origin: Debian Backports of the backports. The
// mirror serves "stable", which moves to the next release by itself.
func mirrorStanza(backports string) func(*fakeProbe) {
	changes := []func(*fakeProbe){
		sourcesFile("mirror.sources", "Types: deb", "URIs: https://mirror.example.edu/debian", "Suites: stable stable-updates stable-backports", "Components: main"),
		inRelease("mirror.example.edu_debian_dists_stable", "Debian"),
		inRelease("mirror.example.edu_debian_dists_stable-updates", "Debian"),
	}
	// "" is a stanza whose backports apt has no Release file of (it has not fetched them)
	if backports != "" {
		changes = append(changes, inRelease("mirror.example.edu_debian_dists_stable-backports", backports))
	}
	return after(changes...)
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
		// a mirror is the archive's when apt kept a Release file of it that says Debian or Ubuntu: nothing else about its
		// address says so, and a vendor's own repository at /debian is a third party whatever its suite is called
		{name: "a mirror of the archive, whose Release file says Debian", setup: after(sourcesList("deb http://mirror.example.edu/debian stable main"), inRelease("mirror.example.edu_debian_dists_stable", "Debian")), severity: Refuse},
		{name: "a mirror of the archive, and no Release file of it", setup: sourcesList("deb http://mirror.example.edu/debian stable main"), severity: OK},
		{name: "a mirror of the archive, whose Release file is another's", setup: after(sourcesList("deb http://mirror.example.edu/debian stable main"), inRelease("mirror.example.edu_debian_dists_stable", "Somebody")), severity: OK},
		{name: "InfluxData's repository, whose suite is stable", setup: after(sourcesFile("influx.list", "deb https://repos.influxdata.com/debian stable main"), inRelease("repos.influxdata.com_debian_dists_stable", "InfluxData")), severity: OK},
		{name: "InfluxData's repository, and no Release file", setup: sourcesFile("influx.list", "deb https://repos.influxdata.com/debian stable main"), severity: OK},
		{name: "Microsoft's", setup: sourcesFile("microsoft.list", "deb [arch=amd64] https://packages.microsoft.com/debian/11/prod bullseye main"), severity: OK},
		{name: "Element's", setup: sourcesFile("element-io.list", "deb [signed-by=/usr/share/keyrings/element-io-archive-keyring.gpg] https://packages.element.io/debian/ default main"), severity: OK},
		{name: "ProtonVPN's", setup: sourcesFile("protonvpn-stable.list", "deb [signed-by=/usr/share/keyrings/proton-vpn-stable-archive-keyring.gpg] https://repo.protonvpn.com/debian stable main"), severity: OK},
		// the vendor's own suite named stable: not the archive's alias
		{name: "a third party's stable", setup: sourcesFile("code.list", "deb https://packages.microsoft.com/repos/code stable main"), severity: OK},
		{name: "a third party's stable at a path that ends in debian", setup: sourcesFile("x.list", "deb https://repo.example.org/stable/debian stable main"), severity: OK},
		{name: "an alias that is commented out", setup: sourcesList("#deb http://deb.debian.org/debian stable main", "deb http://deb.debian.org/debian bullseye main"), severity: OK},
		// a file that was not understood may hold what this looks for: it is not a pass for the others, and
		// sources_format says what is wrong with the file
		{name: "a file that cannot be understood is not looked into", setup: sourcesList("rubbish"), severity: Refuse, unknown: ReasonUnreadable},
		{name: "an alias of a mirror at a path of its own", setup: after(sourcesList("deb http://mirror.hetzner.com/debian/packages stable main"), inRelease("mirror.hetzner.com_debian_packages_dists_stable", "Debian")), severity: Refuse, data: D{"files": []string{"sources.list"}, "suites": []string{"stable"}}},
		{name: "an alias of a mirror's security archive", setup: after(sourcesFile("sec.list", "deb http://mirror.hetzner.com/debian/security stable-security main"), inRelease("mirror.hetzner.com_debian_security_dists_stable-security", "Debian")), severity: Refuse, data: D{"suites": []string{"stable-security"}}},
		{name: "an alias of a mirror that keeps the archive under the name of its host", setup: after(sourcesList("deb http://mirror.ovh.net/ftp.debian.org/debian oldstable main"), release("mirror.ovh.net_ftp.debian.org_debian_dists_oldstable", "Debian")), severity: Refuse},
		{name: "an alias of a mirror at a path of its own that has no Release file", setup: sourcesList("deb http://mirror.hetzner.com/debian/packages stable main"), severity: OK},
		{name: "an alias that is found in a file next to one that cannot be read", setup: after(sourcesList("rubbish"), sourcesFile("a.list", "deb http://deb.debian.org/debian testing main")), severity: Refuse, data: D{"files": []string{"a.list"}}},
		// each suite of a stanza is judged by its own Release file: the backports of Debian say Origin: Debian Backports, and a
		// stanza that has them is the distribution's all the same
		{name: "the stanza of a mirror that has the backports too", setup: mirrorStanza("Debian Backports"), severity: Refuse,
			data: D{"files": []string{"mirror.sources"}, "suites": []string{"stable", "stable-backports", "stable-updates"}}},
		{name: "the same stanza, and the backports are not fetched: the alias is still refused", setup: mirrorStanza(""), severity: Refuse,
			data: D{"files": []string{"mirror.sources"}, "suites": []string{"stable", "stable-updates"}}},
		{name: "the same stanza, and the backports say an origin that only has the word Debian in it", setup: mirrorStanza("SomeDebianThing"), severity: Refuse,
			data: D{"suites": []string{"stable", "stable-updates"}}},
		{name: "a codename of the distribution next to an alias that is a vendor's own", setup: after(
			sourcesFile("mixed.sources", "Types: deb", "URIs: https://mirror.example.edu/debian", "Suites: bullseye stable", "Components: main"),
			inRelease("mirror.example.edu_debian_dists_bullseye", "Debian"), inRelease("mirror.example.edu_debian_dists_stable", "Vendor")), severity: OK},
	})
}

func TestSourcesProposed(t *testing.T) {
	runScenarios(t, "sources_proposed", []scenario{
		{name: "no proposed updates", severity: OK},
		{name: "Debian's", setup: sourcesList("deb http://deb.debian.org/debian bullseye-proposed-updates main"), severity: Refuse, data: D{"files": []string{"sources.list"}, "suites": []string{"bullseye-proposed-updates"}}, action: ActionReviewSources},
		{name: "Ubuntu's", setup: after(ubuntu2204, sourcesList("deb http://archive.ubuntu.com/ubuntu jammy-proposed main")), severity: Refuse, data: D{"suites": []string{"jammy-proposed"}}},
		{name: "in a .sources file", setup: sourcesFile("p.sources", "Types: deb", "URIs: http://deb.debian.org/debian", "Suites: bullseye-proposed-updates", "Components: main"), severity: Refuse},
		{name: "a mirror's, whose Release file says Debian", setup: after(sourcesList("deb http://mirror.hetzner.com/debian/packages bullseye-proposed-updates main"), inRelease("mirror.hetzner.com_debian_packages_dists_bullseye-proposed-updates", "Debian")), severity: Refuse, data: D{"suites": []string{"bullseye-proposed-updates"}}},
		{name: "a mirror's that apt has no file of", setup: sourcesList("deb http://mirror.hetzner.com/debian/packages bullseye-proposed-updates main"), severity: OK},
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
		// Docker's source is on every machine of ReCasaOS, and the upgrade puts it aside and writes it again for the new release:
		// alone, it is a note, there is nothing to do about it, and checking again never makes it go
		{name: "Docker's alone is a note", setup: sourcesFile("docker.list", dockerLine), severity: Info, title: "Docker's package source is put back after the upgrade",
			data: D{"files": []string{"docker.list"}, "hosts": []string{"download.docker.com"}, "docker_files": []string{"docker.list"}}, action: ActionNone},
		{name: "Docker's, in a .sources file", setup: sourcesFile("docker.sources", "Types: deb", "URIs: https://download.docker.com/linux/debian", "Suites: bullseye", "Components: stable"), severity: Info,
			data: D{"files": []string{"docker.sources"}, "docker_files": []string{"docker.sources"}}},
		{name: "Docker's, in two files", setup: after(sourcesFile("docker.list", dockerLine), sourcesFile("docker.sources", "Types: deb", "URIs: https://download.docker.com/linux/debian", "Suites: bullseye", "Components: stable")), severity: Info,
			data: D{"files": []string{"docker.list", "docker.sources"}, "docker_files": []string{"docker.list", "docker.sources"}}},
		{name: "Docker's, with its host in capitals and a port", setup: sourcesFile("docker.list", "deb https://Download.Docker.com:443/linux/debian bullseye stable"), severity: Info,
			data: D{"hosts": []string{"download.docker.com"}, "docker_files": []string{"docker.list"}}},
		{name: "another has no action either", setup: sourcesFile("tailscale.list", tailscaleLine), severity: Warn, action: ActionNone, title: "Some software comes from other sources than Debian or Ubuntu"},
		{name: "Docker's, and another: the other is what is warned of", setup: after(sourcesFile("docker.list", dockerLine), sourcesFile("tailscale.list", tailscaleLine)), severity: Warn,
			data: D{"files": []string{"docker.list", "tailscale.list"}, "hosts": []string{"download.docker.com", "pkgs.tailscale.com"}, "docker_files": []string{"docker.list"}, "other_files": []string{"tailscale.list"}}},
		{name: "another, not Docker's", setup: sourcesFile("tailscale.list", tailscaleLine), severity: Warn, data: D{"files": []string{"tailscale.list"}, "other_files": []string{"tailscale.list"}}},
		{name: "a file that has Docker's line and another's is another's too", setup: sourcesFile("vendors.list", dockerLine, tailscaleLine), severity: Warn,
			data: D{"files": []string{"vendors.list"}, "hosts": []string{"download.docker.com", "pkgs.tailscale.com"}, "docker_files": []string{"vendors.list"}, "other_files": []string{"vendors.list"}}},
		{name: "a stanza that has Docker's address and another's is another's too", setup: sourcesFile("vendors.sources", "Types: deb", "URIs: https://download.docker.com/linux/debian https://pkgs.tailscale.com/stable/debian", "Suites: bullseye", "Components: stable"), severity: Warn,
			data: D{"files": []string{"vendors.sources"}, "docker_files": []string{"vendors.sources"}, "other_files": []string{"vendors.sources"}}},
		{name: "a source that has no host is another's, and Docker's is not all there is", setup: after(sourcesFile("docker.list", dockerLine), sourcesFile("odd.list", "deb foo:bar bullseye main")), severity: Warn,
			data: D{"files": []string{"docker.list", "odd.list"}, "docker_files": []string{"docker.list"}, "other_files": []string{"odd.list"}}},
		{name: "in a .sources file", setup: sourcesFile("syncthing.sources", "Types: deb", "URIs: https://apt.syncthing.net/", "Suites: syncthing", "Components: stable"), severity: Warn, data: D{"files": []string{"syncthing.sources"}}},
		{name: "a PPA", setup: after(ubuntu2204, sourcesFile("ppa.list", "deb https://ppa.launchpadcontent.net/x/y/ubuntu jammy main")), severity: Warn},
		{name: "a mirror of the archive that apt has a Release file of, that says Debian, is Debian's", setup: after(sourcesFile("mirror.list", "deb http://mirror.example.edu/debian bullseye main"), inRelease("mirror.example.edu_debian_dists_bullseye", "Debian")), severity: OK},
		{name: "a mirror that has the archive under a path of its own is Debian's, by its Release files", setup: after(sourcesFile("hetzner.list", "deb http://mirror.hetzner.com/debian/packages bullseye main"), sourcesFile("sec.list", "deb http://mirror.hetzner.com/debian/security bullseye-security main"),
			inRelease("mirror.hetzner.com_debian_packages_dists_bullseye", "Debian"), release("mirror.hetzner.com_debian_security_dists_bullseye-security", "Debian")), severity: OK},
		{name: "a mirror that apt has no Release file of is a third party's, and is said", setup: sourcesFile("mirror.list", "deb http://mirror.example.edu/debian bullseye main"), severity: Warn, data: D{"files": []string{"mirror.list"}, "hosts": []string{"mirror.example.edu"}, "other_files": []string{"mirror.list"}}},
		{name: "a mirror whose Release file says another origin is a third party's", setup: after(sourcesFile("mirror.list", "deb http://mirror.example.edu/debian bullseye main"), inRelease("mirror.example.edu_debian_dists_bullseye", "Raspbian")), severity: Warn},
		// the repositories of vendors that serve their own at the root of their host under the name of the distribution
		{name: "InfluxData's is a third party's, and is said", setup: after(sourcesFile("influxdata.list", "deb https://repos.influxdata.com/debian stable main"), inRelease("repos.influxdata.com_debian_dists_stable", "InfluxData")), severity: Warn, data: D{"files": []string{"influxdata.list"}, "hosts": []string{"repos.influxdata.com"}, "other_files": []string{"influxdata.list"}}},
		{name: "Microsoft's is a third party's, and is said", setup: sourcesFile("microsoft-prod.list", "deb [arch=amd64,arm64,armhf signed-by=/usr/share/keyrings/microsoft-prod.gpg] https://packages.microsoft.com/debian/11/prod bullseye main"), severity: Warn, data: D{"hosts": []string{"packages.microsoft.com"}}},
		{name: "Element's is a third party's, and is said", setup: sourcesFile("element-io.list", "deb [signed-by=/usr/share/keyrings/element-io-archive-keyring.gpg] https://packages.element.io/debian/ default main"), severity: Warn, data: D{"hosts": []string{"packages.element.io"}}},
		{name: "ProtonVPN's is a third party's, and is said", setup: sourcesFile("protonvpn-stable.list", "deb https://repo.protonvpn.com/debian stable main"), severity: Warn, data: D{"hosts": []string{"repo.protonvpn.com"}}},
		{name: "a third party's, a mirror's with no file and Debian's together are each judged by themselves", setup: after(sourcesFile("a.list", "deb https://repos.influxdata.com/debian stable main"), sourcesFile("b.list", "deb http://mirror.example.edu/debian bullseye main"), sourcesFile("c.list", "deb http://deb.debian.org/debian bullseye main")), severity: Warn, data: D{"files": []string{"a.list", "b.list"}, "other_files": []string{"a.list", "b.list"}}},
		{name: "a file that cannot be understood may hide a source that is not Debian's", setup: sourcesList("rubbish"), severity: Refuse, unknown: ReasonUnreadable},
		// what cannot be read is sources_format's to refuse: Docker's, on what could be read, is still a note
		{name: "Docker's is said whatever else cannot be read", setup: after(sourcesList("rubbish"), sourcesFile("docker.list", dockerLine)), severity: Info, data: D{"files": []string{"docker.list"}}},
		{name: "another is said whatever else cannot be read", setup: after(sourcesList("rubbish"), sourcesFile("tailscale.list", tailscaleLine)), severity: Warn, data: D{"files": []string{"tailscale.list"}, "other_files": []string{"tailscale.list"}}},
		{name: "Ubuntu's own", setup: after(ubuntu2204, sourcesList("deb http://archive.ubuntu.com/ubuntu jammy main", "deb http://security.ubuntu.com/ubuntu jammy-security main"), sourcesFile("esm.list", "deb https://esm.ubuntu.com/infra/ubuntu jammy-infra-security main")), severity: OK},
		{name: "a disc is not a third party", setup: sourcesList("deb cdrom:[x]/ bullseye main"), severity: OK},
		{name: "a third party that is commented out", setup: sourcesFile("docker.list", "# "+dockerLine), severity: OK},
		{name: "the user and the password of an address are not shown", setup: sourcesFile("private.list", "deb https://user:secret@repo.example.org:8443/apt bullseye main"), severity: Warn, data: D{"hosts": []string{"repo.example.org"}}},
		{name: "a host that is in two files, and twice in one, is said once", setup: after(
			sourcesFile("a.list", "deb https://repo.example.org/apt bullseye main", "deb-src https://repo.example.org/apt bullseye main"),
			sourcesFile("b.list", "deb https://repo.example.org/other bullseye main")), severity: Warn,
			data: D{"files": []string{"a.list", "b.list"}, "hosts": []string{"repo.example.org"}, "other_files": []string{"a.list", "b.list"}}},
		// the stanza of a mirror that has the backports too is Debian's, as far as each of its suites says so
		{name: "a mirror's stanza, whose suites say Debian and whose backports say Debian Backports, is Debian's", setup: mirrorStanza("Debian Backports"), severity: OK},
		{name: "the same stanza, and the backports are not fetched: that part of the file is a third party's", setup: mirrorStanza(""), severity: Warn,
			data: D{"files": []string{"mirror.sources"}, "hosts": []string{"mirror.example.edu"}, "other_files": []string{"mirror.sources"}}},
		{name: "the same stanza, and the backports say an origin that only has the word Debian in it", setup: mirrorStanza("SomeDebianThing"), severity: Warn, data: D{"files": []string{"mirror.sources"}}},
		{name: "a vendor whose origin only has the word Debian in it", setup: after(sourcesFile("v.list", "deb https://repo.example.org/debian stable main"), inRelease("repo.example.org_debian_dists_stable", "SomeDebianThing")), severity: Warn},
	})
}

// The data of the finding is the keys that apply to the case, and no other: the page says what it finds in them.
func TestTheDataOfThirdPartiesIsTheKeysThatApplyToTheCase(t *testing.T) {
	for name, tc := range map[string]struct {
		setup    func(*fakeProbe)
		severity Severity
		keys     []string
	}{
		"Docker's alone":                        {sourcesFile("docker.list", dockerLine), Info, []string{"docker_files", "files", "hosts"}},
		"another alone":                         {sourcesFile("tailscale.list", tailscaleLine), Warn, []string{"files", "hosts", "other_files"}},
		"Docker's and another":                  {after(sourcesFile("docker.list", dockerLine), sourcesFile("tailscale.list", tailscaleLine)), Warn, []string{"docker_files", "files", "hosts", "other_files"}},
		"another that has no host":              {sourcesFile("odd.list", "deb foo:bar bullseye main"), Warn, []string{"files", "other_files"}},
		"a mirror that apt has no Release of":   {sourcesFile("mirror.list", "deb http://mirror.example.edu/debian bullseye main"), Warn, []string{"files", "hosts", "other_files"}},
		"Docker's and another that has no host": {after(sourcesFile("docker.list", dockerLine), sourcesFile("odd.list", "deb foo:bar bullseye main")), Warn, []string{"docker_files", "files", "hosts", "other_files"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newBox(t)
			tc.setup(f)
			got := checkOn(t, f, "sources_third_party")
			if got.Severity != tc.severity {
				t.Fatalf("severity = %s, want %s: %+v", got.Severity, tc.severity, got)
			}
			keys := []string{}
			for key := range got.Data {
				if key != "unknown" {
					keys = append(keys, key)
				}
			}
			slices.Sort(keys)
			if !slices.Equal(keys, tc.keys) {
				t.Errorf("data has %v, want %v", keys, tc.keys)
			}
		})
	}
}

// Docker's source is the one on Docker's own host, written as apt reads the host: an address that only looks like it is
// another vendor's, and is warned of as one.
func TestOnlyDockersOwnHostIsDockers(t *testing.T) {
	for _, uri := range []string{
		"https://download.docker.com.evil.example/linux/debian",
		"https://download.docker.community/linux/debian",
		"https://mirror.download.docker.com/linux/debian",
		"https://evil-download.docker.com/linux/debian",
		"https://docker.com/linux/debian",
		"https://get.docker.com/linux/debian",
		"https://download.docker.com@evil.example/linux/debian",
		"https://evil.example/download.docker.com/linux/debian",
	} {
		t.Run(uri, func(t *testing.T) {
			f := newBox(t)
			sourcesFile("x.list", "deb "+uri+" bullseye stable")(f)
			got := checkOn(t, f, "sources_third_party")
			if got.Severity != Warn {
				t.Fatalf("severity = %s: %+v", got.Severity, got)
			}
			if other, _ := got.Data["other_files"].([]string); !slices.Equal(other, []string{"x.list"}) {
				t.Errorf("other_files = %v", got.Data["other_files"])
			}
			if _, has := got.Data["docker_files"]; has {
				t.Errorf("docker_files = %v", got.Data["docker_files"])
			}
		})
	}
}

// What the owner pastes: Docker's source alone is a note of a report that is ready, another's is a warning of one that has
// things to look at.
func TestTheTextSaysDockersSourceAsANoteAndAnotherAsAWarning(t *testing.T) {
	f := newBox(t)
	sourcesFile("docker.list", dockerLine)(f)
	text := Text(Run(context.Background(), f, Options{}))
	for _, want := range []string{
		"Result: Ready.\n",
		"NOTES\n",
		"[sources_third_party] Docker's package source is put back after the upgrade\n    It is put aside for the upgrade and rewritten for the new release afterwards. Nothing to do.\n    docker_files: docker.list\n    files: docker.list\n    hosts: download.docker.com\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the text has no %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "WARNINGS") || strings.Contains(text, "other_files") {
		t.Errorf("Docker's source alone is warned of:\n%s", text)
	}

	sourcesFile("tailscale.list", tailscaleLine)(f)
	text = Text(Run(context.Background(), f, Options{}))
	for _, want := range []string{
		"Result: Ready, with things to look at.\n",
		"WARNINGS\n[sources_third_party] Some software comes from other sources than Debian or Ubuntu\n",
		"    other_files: tailscale.list\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the text has no %q:\n%s", want, text)
		}
	}
}

func TestSourcesBackports(t *testing.T) {
	runScenarios(t, "sources_backports", []scenario{
		{name: "no backports", severity: OK},
		{name: "Debian's", setup: sourcesList("deb http://deb.debian.org/debian bullseye main", "deb http://deb.debian.org/debian bullseye-backports main"), severity: Info, data: D{"files": []string{"sources.list"}}, action: ActionNone},
		{name: "in a file of its own", setup: sourcesFile("backports.list", "deb http://deb.debian.org/debian bullseye-backports main contrib"), severity: Info, data: D{"files": []string{"backports.list"}}},
		{name: "the sloppy ones", setup: sourcesFile("sloppy.list", "deb http://deb.debian.org/debian bullseye-backports-sloppy main"), severity: Info},
		{name: "a third party's", setup: sourcesFile("x.list", "deb https://repo.example.org/apt bullseye-backports main"), severity: OK},
		// Debian's own Release file of its backports says Origin: Debian Backports
		{name: "a mirror's, whose Release file says Debian Backports", setup: after(sourcesFile("m.list", "deb http://mirror.hetzner.com/debian/packages bullseye-backports main"), inRelease("mirror.hetzner.com_debian_packages_dists_bullseye-backports", "Debian Backports")), severity: Info, data: D{"files": []string{"m.list"}}},
		{name: "a mirror's, whose Release file says Debian", setup: after(sourcesFile("m.list", "deb http://mirror.hetzner.com/debian/packages bullseye-backports main"), inRelease("mirror.hetzner.com_debian_packages_dists_bullseye-backports", "Debian")), severity: Info, data: D{"files": []string{"m.list"}}},
		{name: "a mirror's, whose Release file says another origin", setup: after(sourcesFile("m.list", "deb http://mirror.hetzner.com/debian/packages bullseye-backports main"), inRelease("mirror.hetzner.com_debian_packages_dists_bullseye-backports", "Somebody")), severity: OK},
		{name: "the stanza of a mirror that has the backports too", setup: mirrorStanza("Debian Backports"), severity: Info, data: D{"files": []string{"mirror.sources"}}},
		{name: "the same stanza, and the backports are not fetched", setup: mirrorStanza(""), severity: OK},
		{name: "a mirror's, with no Release file of it, is a third party's backports", setup: sourcesFile("m.list", "deb http://mirror.hetzner.com/debian/packages bullseye-backports main"), severity: OK},
		{name: "a vendor's, at a path of the distribution's name", setup: sourcesFile("v.list", "deb https://repos.influxdata.com/debian bullseye-backports main"), severity: OK},
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
