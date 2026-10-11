package osreadiness

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestOneLineSourcesAreReadTheWayAptReadsThem(t *testing.T) {
	entries, ok := parseOneLine("sources.list", strings.Join([]string{
		"# a comment",
		"",
		"deb http://deb.debian.org/debian bullseye main contrib non-free # trailing comment",
		"deb-src http://deb.debian.org/debian bullseye main",
		"  deb\t[arch=amd64 signed-by=/etc/apt/keyrings/docker.asc]   https://download.docker.com/linux/debian bullseye stable",
		"deb cdrom:[Debian GNU/Linux 11.0.0 _Bullseye_ - Official amd64 DVD Binary-1 20210814-10:07]/ bullseye contrib main",
		"deb [trusted=yes] http://example.org/repo ./",
		"#deb http://commented.example.org/debian bullseye main",
	}, "\n"))
	if !ok {
		t.Fatal("not understood")
	}
	want := []sourceEntry{
		{File: "sources.list", Kind: "deb", URI: "http://deb.debian.org/debian", Suites: []string{"bullseye"}, Components: []string{"main", "contrib", "non-free"}},
		{File: "sources.list", Kind: "deb-src", URI: "http://deb.debian.org/debian", Suites: []string{"bullseye"}, Components: []string{"main"}},
		{File: "sources.list", Kind: "deb", URI: "https://download.docker.com/linux/debian", Suites: []string{"bullseye"}, Components: []string{"stable"}},
		{File: "sources.list", Kind: "deb", URI: "cdrom:[Debian GNU/Linux 11.0.0 _Bullseye_ - Official amd64 DVD Binary-1 20210814-10:07]/", Suites: []string{"bullseye"}, Components: []string{"contrib", "main"}},
		{File: "sources.list", Kind: "deb", URI: "http://example.org/repo", Suites: []string{"./"}, Components: []string{}},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %+v\nwant     %+v", entries, want)
	}
}

func TestALineThatIsNotASourceIsNotUnderstood(t *testing.T) {
	for name, text := range map[string]string{
		"not a line":           "this is not a line\n",
		"another type":         "rpm http://example.org/repo stable main\n",
		"no suite":             "deb http://deb.debian.org/debian\n",
		"only options":         "deb [arch=amd64] http://deb.debian.org/debian\n",
		"options not closed":   "deb [arch=amd64 http://deb.debian.org/debian bullseye main\n",
		"options closed twice": "deb arch=amd64] http://deb.debian.org/debian bullseye main\n",
		"one bad line":         "deb http://deb.debian.org/debian bullseye main\nrubbish\n",
		"only a type":          "deb\n",
	} {
		t.Run(name, func(t *testing.T) {
			if entries, ok := parseOneLine("x.list", text); ok {
				t.Fatalf("understood: %+v", entries)
			}
		})
	}
	if entries, ok := parseOneLine("x.list", "\n\n# nothing\n"); !ok || len(entries) != 0 {
		t.Fatalf("an empty file is understood: %v %v", entries, ok)
	}
}

func TestDeb822SourcesAreReadTheWayAptReadsThem(t *testing.T) {
	entries, ok := parseDeb822("debian.sources", `# the main archive
Types: deb deb-src
URIs: http://deb.debian.org/debian
 http://security.debian.org/debian-security
Suites: trixie trixie-updates
Components: main
 contrib
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb
URIs: https://download.docker.com/linux/debian
Suites: trixie
Components: stable
# a comment in the middle
Enabled: no

Types: deb
uris: https://repo.example.org/apt
suites: stable
components: main`)
	if !ok {
		t.Fatal("not understood")
	}
	if len(entries) != 5 {
		t.Fatalf("%d entries: %+v", len(entries), entries)
	}
	first := entries[0]
	if first.Kind != "deb" || first.URI != "http://deb.debian.org/debian" || !reflect.DeepEqual(first.Suites, []string{"trixie", "trixie-updates"}) || !reflect.DeepEqual(first.Components, []string{"main", "contrib"}) {
		t.Fatalf("first = %+v", first)
	}
	if entries[1].Kind != "deb" || entries[1].URI != "http://security.debian.org/debian-security" || entries[2].Kind != "deb-src" {
		t.Fatalf("entries = %+v", entries)
	}
	for _, e := range entries {
		if strings.Contains(e.URI, "docker") {
			t.Fatalf("a stanza that is not enabled is read: %+v", e)
		}
	}
	if last := entries[4]; last.URI != "https://repo.example.org/apt" || last.Suites[0] != "stable" {
		t.Fatalf("last = %+v: the keys are not case-sensitive", last)
	}
}

func TestAStanzaThatIsNotASourceIsNotUnderstood(t *testing.T) {
	for name, text := range map[string]string{
		"no suites":              "Types: deb\nURIs: http://x\n",
		"no uris":                "Types: deb\nSuites: bullseye\n",
		"no types":               "URIs: http://x\nSuites: bullseye\n",
		"another type":           "Types: rpm\nURIs: http://x\nSuites: bullseye\n",
		"a line with no key":     "Types: deb\nthis is not a field\n",
		"an indented line first": " Types: deb\n",
		"one good one bad":       "Types: deb\nURIs: http://x\nSuites: a\n\nTypes: deb\n",
	} {
		t.Run(name, func(t *testing.T) {
			if entries, ok := parseDeb822("x.sources", text); ok {
				t.Fatalf("understood: %+v", entries)
			}
		})
	}
	if entries, ok := parseDeb822("x.sources", "\n# nothing\n\n"); !ok || len(entries) != 0 {
		t.Fatalf("an empty file is understood: %v %v", entries, ok)
	}
	entries, ok := parseDeb822("x.sources", "Types: deb\nURIs: http://x\nSuites: a\nEnabled: No\n")
	if !ok || len(entries) != 0 {
		t.Fatalf("Enabled: No = %v %v", entries, ok)
	}
}

func TestWhereASourceComesFrom(t *testing.T) {
	for _, tc := range []struct {
		uri    string
		host   string
		distro bool
		local  bool
	}{
		{"http://deb.debian.org/debian", "deb.debian.org", true, false},
		{"https://security.debian.org/debian-security", "security.debian.org", true, false},
		{"http://ftp.fr.debian.org/debian", "ftp.fr.debian.org", true, false},
		{"http://archive.debian.org/debian-security", "archive.debian.org", true, false},
		{"http://archive.ubuntu.com/ubuntu", "archive.ubuntu.com", true, false},
		{"http://us-east-1.ec2.archive.ubuntu.com/ubuntu/", "us-east-1.ec2.archive.ubuntu.com", true, false},
		{"https://esm.ubuntu.com/apps/ubuntu", "esm.ubuntu.com", true, false},
		{"mirror+http://mirrors.ubuntu.com/mirrors.txt", "mirrors.ubuntu.com", true, false},
		{"http://user:secret@DEB.Debian.ORG:8080/debian", "deb.debian.org", true, false},
		{"https://mirror.example.edu/debian", "mirror.example.edu", true, false},
		{"https://mirror.example.edu/debian/", "mirror.example.edu", true, false},
		{"https://mirror.example.edu/ubuntu", "mirror.example.edu", true, false},
		{"https://download.docker.com/linux/debian", "download.docker.com", false, false},
		{"https://pkgs.tailscale.com/stable/debian", "pkgs.tailscale.com", false, false},
		{"https://packages.microsoft.com/repos/code", "packages.microsoft.com", false, false},
		{"https://ppa.launchpadcontent.net/x/y/ubuntu", "ppa.launchpadcontent.net", false, false},
		{"http://notdebian.org/debian-fake", "notdebian.org", false, false},
		{"http://debian.org.evil.example/x", "debian.org.evil.example", false, false},
		{"http://evil.example/ubuntu.com", "evil.example", false, false},
		{"cdrom:[Debian 11]/", "", false, true},
		{"file:/srv/mirror/debian", "", false, true},
		{"copy:/srv/mirror", "", false, true},
		{"FILE:///srv/mirror", "", false, true},
	} {
		e := sourceEntry{URI: tc.uri}
		if got := e.host(); got != tc.host {
			t.Errorf("host(%q) = %q, want %q", tc.uri, got, tc.host)
		}
		if got := e.distro(); got != tc.distro {
			t.Errorf("distro(%q) = %v, want %v", tc.uri, got, tc.distro)
		}
		if got := e.local(); got != tc.local {
			t.Errorf("local(%q) = %v, want %v", tc.uri, got, tc.local)
		}
	}
}

func TestTheSuitesThatMoveOrAreNotReady(t *testing.T) {
	for _, tc := range []struct {
		suite                     string
		alias, proposed, backport bool
	}{
		{"bullseye", false, false, false},
		{"bullseye-updates", false, false, false},
		{"bullseye-security", false, false, false},
		{"bullseye/updates", false, false, false},
		{"jammy", false, false, false},
		{"stable", true, false, false},
		{"oldstable", true, false, false},
		{"oldoldstable", true, false, false},
		{"testing", true, false, false},
		{"unstable", true, false, false},
		{"sid", true, false, false},
		{"experimental", true, false, false},
		{"devel", true, false, false},
		{"stable-updates", true, false, false},
		{"stable-security", true, false, false},
		{"stable/updates", true, false, false},
		{"oldstable-backports", true, false, true},
		{"stable-proposed-updates", true, true, false},
		{"bullseye-backports", false, false, true},
		{"bullseye-backports-sloppy", false, false, true},
		{"bullseye-proposed-updates", false, true, false},
		{"jammy-proposed", false, true, false},
		{"stable-ish", false, false, false},
		{"unstable-foo", false, false, false},
		{"./", false, false, false},
	} {
		if got := isAlias(tc.suite); got != tc.alias {
			t.Errorf("isAlias(%q) = %v", tc.suite, got)
		}
		if got := isProposed(tc.suite); got != tc.proposed {
			t.Errorf("isProposed(%q) = %v", tc.suite, got)
		}
		if got := isBackports(tc.suite); got != tc.backport {
			t.Errorf("isBackports(%q) = %v", tc.suite, got)
		}
	}
}

func TestOnlyTheNamesAptReadsAreRead(t *testing.T) {
	f := newBox(t)
	bad := "deb cdrom:[x]/ bullseye main\n"
	for _, name := range []string{"cd.list.disabled", "cd.list.save", "cd.list~", "cd.list.dpkg-old", "my repo.list", "cd.conf", "cd", "cd.list.d"} {
		f.set("/etc/apt/sources.list.d/"+name, bad)
	}
	set, err := loadSources(context.Background(), f)
	if err != nil || len(set.Entries) != 3 || len(set.Unreadable) != 0 {
		t.Fatalf("set = %+v, err = %v", set, err)
	}
	f.set("/etc/apt/sources.list.d/cd.list", bad)
	f.set("/etc/apt/sources.list.d/CD_2-x.sources", "Types: deb\nURIs: cdrom:[x]/\nSuites: bullseye\n")
	set, _ = loadSources(context.Background(), f)
	if len(set.Entries) != 5 {
		t.Fatalf("set = %+v", set)
	}
	for _, path := range f.readPaths() {
		if strings.HasPrefix(path, "read /etc/apt/sources.list.d/") && !strings.HasSuffix(path, ".list") && !strings.HasSuffix(path, ".sources") {
			t.Errorf("read %s", path)
		}
	}
}
