package osreadiness

import (
	"context"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"time"
)

// signedRelease is the start of an InRelease file as apt keeps it in /var/lib/apt/lists: the armour of the
// signature, the hash it was made with, and then the fields of the Release file.
func signedRelease(origin string) string {
	return "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA512\n\nOrigin: " + origin + "\nLabel: " + origin + "\nSuite: stable\nVersion: 12.7\nCodename: bookworm\n" +
		"Date: Sat, 31 Aug 2024 10:12:00 UTC\nAcquire-By-Hash: yes\nArchitectures: all amd64 arm64\nComponents: main contrib non-free-firmware non-free\nDescription: Debian 12.7 Released 31 August 2024\n" +
		"MD5Sum:\n d41d8cd98f00b204e9800998ecf8427e 0 main/binary-amd64/Packages\n"
}

// inRelease is a machine where apt has kept the InRelease file of a source, under the name apt gives it
// (without its _InRelease), and what its Origin says.
func inRelease(name, origin string) func(*fakeProbe) {
	return func(f *fakeProbe) { f.set(listsDir+name+"_InRelease", signedRelease(origin)) }
}

// release is the same for the Release file, which apt keeps when the archive has no InRelease.
func release(name, origin string) func(*fakeProbe) {
	return func(f *fakeProbe) {
		f.set(listsDir+name+"_Release", "Origin: "+origin+"\nLabel: "+origin+"\nSuite: stable\nCodename: bookworm\n")
	}
}

// What apt calls a Release file it keeps: the address of the source with its scheme and its user left out, every / a _,
// and the characters of the address that apt quotes as %xx (URItoFileName of apt-pkg/acquire.cc).
func TestAptNamesTheReleaseFilesItKeepsAfterTheAddressOfTheSource(t *testing.T) {
	for _, tc := range []struct {
		uri, suite string
		want       string
		ok         bool
	}{
		{"http://deb.debian.org/debian", "bookworm", "deb.debian.org_debian_dists_bookworm", true},
		{"http://deb.debian.org/debian/", "bookworm", "deb.debian.org_debian_dists_bookworm", true},
		{"https://mirror.example.edu/debian-security", "bookworm-security", "mirror.example.edu_debian-security_dists_bookworm-security", true},
		{"http://archive.ubuntu.com/ubuntu", "jammy", "archive.ubuntu.com_ubuntu_dists_jammy", true},
		{"http://mirror.example.edu", "stable", "mirror.example.edu_dists_stable", true},
		{"http://mirror.example.edu/", "stable", "mirror.example.edu_dists_stable", true},
		{"http://security.debian.org stable/updates", "stable/updates", "", false},
		{"http://security.debian.org", "stable/updates", "security.debian.org_dists_stable_updates", true},
		// the user and the password are not part of the name, and the port is
		{"http://user:secret@mirror.example.edu:8080/debian", "bookworm", "mirror.example.edu:8080_debian_dists_bookworm", true},
		{"http://user@mirror.example.edu/debian", "bookworm", "mirror.example.edu_debian_dists_bookworm", true},
		// the characters that apt quotes: _ ~ = ! @ # $ % ^ & * \ | { } [ ] < > "
		{"https://mirror.example.edu/~gary/my_debian", "bookworm", "mirror.example.edu_%7egary_my%5fdebian_dists_bookworm", true},
		{"https://mirror.example.edu/a=b", "bookworm", "mirror.example.edu_a%3db_dists_bookworm", true},
		{"https://my_mirror.example.edu/debian", "bookworm", "my%5fmirror.example.edu_debian_dists_bookworm", true},
		{"https://mirror.example.edu/debian+x", "bookworm", "mirror.example.edu_debian+x_dists_bookworm", true},
		// what is not an address of a mirror
		{"cdrom:[Debian 11]/", "bullseye", "", false},
		{"file:/srv/mirror", "bullseye", "", false},
		{"mirror+http://mirrors.example.org/mirrors.txt", "bullseye", "", false},
		{"mirror://mirrors.example.org/mirrors.txt", "bullseye", "", false},
		{"", "bullseye", "", false},
		{"http://", "bullseye", "", false},
		{"http:///debian", "bullseye", "", false},
		// a flat repository has its Release file next to its packages
		{"https://repo.example.org/apt", "./", "", false},
		{"https://repo.example.org/apt", "stable/", "", false},
		{"https://repo.example.org/apt", "", "", false},
		{"https://repo.example.org/apt", "a b", "", false},
	} {
		got, ok := releaseListName(tc.uri, tc.suite)
		if got != tc.want || ok != tc.ok {
			t.Errorf("releaseListName(%q, %q) = %q, %v; want %q, %v", tc.uri, tc.suite, got, ok, tc.want, tc.ok)
		}
	}
}

// The Origin of a Release file is what its start says it is, and Debian's and Ubuntu's say Debian and Ubuntu.
func TestTheOriginOfAReleaseFileIsReadFromItsStart(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       string
	}{
		{"an InRelease", signedRelease("Debian"), "Debian"},
		{"a Release", "Origin: Ubuntu\nLabel: Ubuntu\n", "Ubuntu"},
		{"a value with spaces around it", "Hash: SHA512\n\nOrigin:   Debian  \r\nLabel: x\n", "Debian"},
		{"a vendor's", signedRelease("InfluxData"), "InfluxData"},
		{"the origin of Debian's backports", signedRelease("Debian Backports"), "Debian Backports"},
		{"no origin", "Label: Debian\nSuite: stable\n", ""},
		{"a field that ends in origin", "X-Origin: Debian\nSuite: stable\n", ""},
		{"a field of another case", "origin: Debian\n", ""},
		{"an origin in the middle of a line", "Label: Origin: Debian\n", ""},
		{"the first one is the one", "Origin: Evil\nOrigin: Debian\n", "Evil"},
		{"nothing", "", ""},
		// the start of a large file ends in the middle of a line: Origin: Debian-Evil, cut after Debian, is not Debian's
		{"a line that was cut", "Label: x\nOrigin: Debian", ""},
		{"a line that is whole, and one that was cut", "Label: x\nOrigin: Debian\nSuite: sta", "Debian"},
		{"a value that is long", "Origin: " + strings.Repeat("a", 5000) + "\n", ""},
	} {
		if got := releaseOrigin([]byte(tc.text)); got != tc.want {
			t.Errorf("%s: origin = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func marked(t *testing.T, f *fakeProbe, uri string, suites ...string) bool {
	t.Helper()
	entries, err := markDistro(context.Background(), f, []sourceEntry{{File: "x.list", Kind: "deb", URI: uri, Suites: suites}})
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	return entries[0].distro()
}

// A source of a host that is not the distribution's own is the distribution's only when apt kept a Release file
// for it that says so: the path of the address tells nothing (a vendor serves its own repository at /debian, and
// a mirror may serve the archive anywhere), and no file is no proof.
func TestAMirrorIsTheDistributionsOnlyWhenItsReleaseFileSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		uri    string
		suites []string
		setup  func(*fakeProbe)
		want   bool
	}{
		{"a university's mirror with a Release file that says Debian", "https://mirror.example.edu/debian", []string{"stable"}, inRelease("mirror.example.edu_debian_dists_stable", "Debian"), true},
		{"the same mirror, and no file", "https://mirror.example.edu/debian", []string{"stable"}, nil, false},
		{"a mirror of Ubuntu", "http://mirror.example.edu/ubuntu/", []string{"jammy"}, inRelease("mirror.example.edu_ubuntu_dists_jammy", "Ubuntu"), true},
		{"a mirror that keeps the archive under no name of it", "http://mirror.example.edu/pub/linux/distros/d", []string{"bookworm"}, inRelease("mirror.example.edu_pub_linux_distros_d_dists_bookworm", "Debian"), true},
		{"a mirror at the root of its host", "http://mirror.example.edu", []string{"bookworm"}, inRelease("mirror.example.edu_dists_bookworm", "Debian"), true},
		{"a Release file where there is no InRelease", "https://mirror.example.edu/debian", []string{"stable"}, release("mirror.example.edu_debian_dists_stable", "Debian"), true},
		{"an InRelease that says what the Release file does not", "https://mirror.example.edu/debian", []string{"stable"}, after(inRelease("mirror.example.edu_debian_dists_stable", "Mallory"), release("mirror.example.edu_debian_dists_stable", "Debian")), false},
		{"a Release file that says Debian, under the name of another suite", "https://mirror.example.edu/debian", []string{"stable"}, inRelease("mirror.example.edu_debian_dists_bookworm", "Debian"), false},
		{"the user, the password and the port of the address", "http://user:secret@mirror.example.edu:8080/debian", []string{"bookworm"}, inRelease("mirror.example.edu:8080_debian_dists_bookworm", "Debian"), true},
		{"a path that apt quotes", "https://mirror.example.edu/~gary/my_debian", []string{"bookworm"}, inRelease("mirror.example.edu_%7egary_my%5fdebian_dists_bookworm", "Debian"), true},
		{"the security archive's old name", "http://mirror.example.edu/debian-security", []string{"stable/updates"}, inRelease("mirror.example.edu_debian-security_dists_stable_updates", "Debian"), true},
		// the vendors that serve their own repository under the name of the distribution, whatever their suite is called
		{"InfluxData's, whose suite is stable", "https://repos.influxdata.com/debian", []string{"stable"}, inRelease("repos.influxdata.com_debian_dists_stable", "InfluxData"), false},
		{"InfluxData's, and no file", "https://repos.influxdata.com/debian", []string{"stable"}, nil, false},
		{"Microsoft's", "https://packages.microsoft.com/debian/11/prod", []string{"bullseye"}, inRelease("packages.microsoft.com_debian_11_prod_dists_bullseye", "microsoft-debian-bullseye-prod bullseye"), false},
		{"Element's", "https://packages.element.io/debian/", []string{"default"}, inRelease("packages.element.io_debian_dists_default", "Element"), false},
		{"ProtonVPN's, whose suite is stable", "https://repo.protonvpn.com/debian", []string{"stable"}, inRelease("repo.protonvpn.com_debian_dists_stable", "Proton"), false},
		{"a vendor that says it is Debian", "https://repo.example.org/debian", []string{"stable"}, inRelease("repo.example.org_debian_dists_stable", "Debianish"), false},
		{"an origin that is Debian's backports", "https://mirror.example.edu/debian", []string{"bookworm-backports"}, inRelease("mirror.example.edu_debian_dists_bookworm-backports", "Debian Backports"), false},
		{"an origin of another case", "https://mirror.example.edu/debian", []string{"stable"}, inRelease("mirror.example.edu_debian_dists_stable", "debian"), false},
		{"a Release file with no origin", "https://mirror.example.edu/debian", []string{"stable"}, func(f *fakeProbe) {
			f.set(listsDir+"mirror.example.edu_debian_dists_stable_InRelease", "Label: Debian\nSuite: stable\n")
		}, false},
		// the origin is read from the start of the file, up to eight KiB of it
		{"an origin that is past the first eight KiB", "https://mirror.example.edu/debian", []string{"stable"}, func(f *fakeProbe) {
			f.set(listsDir+"mirror.example.edu_debian_dists_stable_InRelease", "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA512\n\n"+strings.Repeat("X-Padding: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n", 220)+"Origin: Debian\n")
		}, false},
		{"an origin that is within them", "https://mirror.example.edu/debian", []string{"stable"}, func(f *fakeProbe) {
			f.set(listsDir+"mirror.example.edu_debian_dists_stable_InRelease", "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA512\n\n"+strings.Repeat("X-Padding: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n", 150)+"Origin: Debian\n")
		}, true},
		// every suite of a source has to be the distribution's
		{"two suites that are Debian's", "https://mirror.example.edu/debian", []string{"bookworm", "bookworm-updates"}, after(inRelease("mirror.example.edu_debian_dists_bookworm", "Debian"), inRelease("mirror.example.edu_debian_dists_bookworm-updates", "Debian")), true},
		{"two suites, and one that is not", "https://mirror.example.edu/debian", []string{"bookworm", "bookworm-updates"}, after(inRelease("mirror.example.edu_debian_dists_bookworm", "Debian"), inRelease("mirror.example.edu_debian_dists_bookworm-updates", "Other")), false},
		{"two suites, and one that has no file", "https://mirror.example.edu/debian", []string{"bookworm", "bookworm-updates"}, inRelease("mirror.example.edu_debian_dists_bookworm", "Debian"), false},
		{"a source with no suite", "https://mirror.example.edu/debian", nil, inRelease("mirror.example.edu_debian_dists_stable", "Debian"), false},
		// what has no Release file of its own to keep, or is not read as one
		{"a flat repository", "https://mirror.example.edu/debian", []string{"./"}, inRelease("mirror.example.edu_debian_dists_.", "Debian"), false},
		{"a mirror list", "mirror+http://mirror.example.edu/mirrors.txt", []string{"bookworm"}, inRelease("mirror.example.edu_mirrors.txt_dists_bookworm", "Debian"), false},
		{"a disc", "cdrom:[Debian 11]/", []string{"bullseye"}, nil, false},
		{"a file", "file:/srv/mirror/debian", []string{"bookworm"}, nil, false},
		{"a file that cannot be read", "https://mirror.example.edu/debian", []string{"stable"}, func(f *fakeProbe) {
			inRelease("mirror.example.edu_debian_dists_stable", "Debian")(f)
			f.fail(listsDir+"mirror.example.edu_debian_dists_stable_InRelease", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
		}, false},
		{"a file that is a directory", "https://mirror.example.edu/debian", []string{"stable"}, func(f *fakeProbe) {
			f.dirs[listsDir+"mirror.example.edu_debian_dists_stable_InRelease"] = true
		}, false},
		// the official hosts are the distribution's, with no file to say it
		{"Debian's own host", "http://deb.debian.org/debian", []string{"bookworm"}, nil, true},
		{"one of Debian's mirrors by country", "http://ftp.fr.debian.org/debian", []string{"bookworm"}, nil, true},
		{"Ubuntu's own host", "http://archive.ubuntu.com/ubuntu", []string{"jammy"}, nil, true},
		{"a mirror of Ubuntu by cloud", "http://us-east-1.ec2.archive.ubuntu.com/ubuntu/", []string{"jammy"}, nil, true},
		{"Ubuntu's ESM", "https://esm.ubuntu.com/apps/ubuntu", []string{"jammy-apps-security"}, nil, true},
		{"the host with a user and a port", "http://user:secret@DEB.Debian.ORG:8080/debian", []string{"bookworm"}, nil, true},
		{"a host that only ends like Debian's", "http://notdebian.org/debian", []string{"bookworm"}, nil, false},
		{"a host that has Debian's in front of it", "http://debian.org.evil.example/debian", []string{"bookworm"}, nil, false},
		{"Docker's", "https://download.docker.com/linux/debian", []string{"bookworm"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBox(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			if got := marked(t, f, tc.uri, tc.suites...); got != tc.want {
				t.Fatalf("distro = %v, want %v", got, tc.want)
			}
		})
	}
}

// The Release files are looked at for the sources that are not an official host's and for no other: the
// hosts of Debian and Ubuntu, a disc and a directory are decided by their address.
func TestTheReleaseFilesAreOnlyLookedAtForTheHostsThatAreNotOfficial(t *testing.T) {
	f := newBox(t)
	for _, uri := range []string{"http://deb.debian.org/debian", "http://archive.ubuntu.com/ubuntu", "cdrom:[x]/", "file:/srv/m", "https://download.docker.com/linux/debian"} {
		marked(t, f, uri, "bookworm")
	}
	for _, path := range f.readPaths() {
		if strings.Contains(path, listsDir) && !strings.Contains(path, "download.docker.com") {
			t.Errorf("a Release file was looked at for an address that is decided by itself: %s", path)
		}
	}
}

// A Release file is read once, for however many sources are made of it, and the files that are looked at for a
// report are as many as a machine has sources: no more, whatever the sources are.
func TestAReleaseFileIsReadOnceAndNotMoreThanAMachineNeeds(t *testing.T) {
	f := newBox(t)
	inRelease("mirror.example.edu_debian_dists_bookworm", "Debian")(f)
	entries := []sourceEntry{
		{File: "a.list", Kind: "deb", URI: "https://mirror.example.edu/debian", Suites: []string{"bookworm"}},
		{File: "b.list", Kind: "deb-src", URI: "https://mirror.example.edu/debian/", Suites: []string{"bookworm"}},
		{File: "c.list", Kind: "deb", URI: "https://mirror.example.edu/debian", Suites: []string{"bookworm", "bookworm"}},
	}
	got, err := markDistro(context.Background(), f, entries)
	if err != nil || !got[0].distro() || !got[1].distro() || !got[2].distro() {
		t.Fatalf("entries = %+v, err = %v", got, err)
	}
	reads := 0
	for _, path := range f.readPaths() {
		if strings.HasPrefix(path, "read "+listsDir) {
			reads++
		}
	}
	if reads != 1 {
		t.Errorf("the Release file was read %d times, want once", reads)
	}

	// a hundred hosts, each with a file that says Debian: the first ones the machine has are looked at, and the others are not
	g := newBox(t)
	var many []sourceEntry
	for i := 0; i < 100; i++ {
		host := "m" + strconv.Itoa(i) + ".example.edu"
		inRelease(host+"_debian_dists_bookworm", "Debian")(g)
		many = append(many, sourceEntry{File: "x.list", Kind: "deb", URI: "https://" + host + "/debian", Suites: []string{"bookworm"}})
	}
	marked100, err := markDistro(context.Background(), g, many)
	if err != nil {
		t.Fatal(err)
	}
	trues := 0
	for _, e := range marked100 {
		if e.distro() {
			trues++
		}
	}
	reads = 0
	for _, path := range g.readPaths() {
		if strings.HasPrefix(path, "read "+listsDir) {
			reads++
		}
	}
	if trues != maxReleaseLookups || reads != maxReleaseLookups {
		t.Errorf("%d sources are the distribution's after %d reads of Release files, want %d of each", trues, reads, maxReleaseLookups)
	}
}

// A Release file that cannot be asked for the end of the time is not one that says no: the check says it did not
// answer, and no source is taken for a third party's because of it.
func TestAReleaseFileThatTakesTooLongIsTheTimeThatEnded(t *testing.T) {
	f := newBox(t)
	inRelease("mirror.example.edu_debian_dists_bookworm", "Debian")(f)
	f.fail(listsDir+"mirror.example.edu_debian_dists_bookworm_InRelease", context.DeadlineExceeded)
	_, err := markDistro(context.Background(), f, []sourceEntry{{File: "x.list", Kind: "deb", URI: "https://mirror.example.edu/debian", Suites: []string{"bookworm"}}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := markDistro(ctx, newBox(t), []sourceEntry{{File: "x.list", Kind: "deb", URI: "https://mirror.example.edu/debian", Suites: []string{"bookworm"}}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a context that has ended: err = %v", err)
	}
}

// What a report of a machine that has a mirror says of it, through the checks that read the sources: a mirror that
// apt says is Debian's is judged as Debian's, and one that it does not know is a third party's.
func TestTheChecksJudgeAMirrorByWhatAptKeptOfIt(t *testing.T) {
	line := "deb https://mirror.example.edu/debian stable main"
	f := newBox(t)
	sourcesList(line)(f)
	report := Run(context.Background(), f, Options{})
	for code, want := range map[string]Severity{"sources_alias": OK, "sources_third_party": Warn, "sources_proposed": OK, "sources_backports": OK} {
		if got := findingOf(t, report, code); got.Severity != want {
			t.Errorf("with no Release file, %s = %+v, want %s", code, got, want)
		}
	}
	g := newBox(t)
	sourcesList(line)(g)
	inRelease("mirror.example.edu_debian_dists_stable", "Debian")(g)
	report = Run(context.Background(), g, Options{})
	for code, want := range map[string]Severity{"sources_alias": Refuse, "sources_third_party": OK} {
		if got := findingOf(t, report, code); got.Severity != want {
			t.Errorf("with a Release file that says Debian, %s = %+v, want %s", code, got, want)
		}
	}
}

// What a stanza is allowed to have: the words of its fields are counted once each, and a stanza with more suites
// or more components than a machine has is a source that is not understood, and not one that is multiplied.
func TestAStanzaHasNoMoreSuitesAndComponentsThanAMachineHas(t *testing.T) {
	words := func(prefix string, count int) string {
		var out strings.Builder
		for i := 0; i < count; i++ {
			out.WriteString(" " + prefix + strconv.Itoa(i))
		}
		return out.String()
	}
	stanza := func(suites, components string) string {
		return "Types: deb\nURIs: http://x/debian\nSuites:" + suites + "\nComponents:" + components + "\n"
	}
	if entries, ok := parseDeb822("x.sources", stanza(words("s", maxSourceSuites), words("c", maxSourceComponents))); !ok || len(entries) != 1 || len(entries[0].Suites) != maxSourceSuites || len(entries[0].Components) != maxSourceComponents {
		t.Errorf("the most a stanza may have: understood %v, %+v", ok, entries)
	}
	if _, ok := parseDeb822("x.sources", stanza(words("s", maxSourceSuites+1), " main")); ok {
		t.Error("a stanza with more suites than a machine has is understood")
	}
	if _, ok := parseDeb822("x.sources", stanza(" a", words("c", maxSourceComponents+1))); ok {
		t.Error("a stanza with more components than a machine has is understood")
	}
	// a word that is written many times is one word
	entries, ok := parseDeb822("x.sources", stanza(strings.Repeat(" a", 5000)+" b "+strings.Repeat(" a", 5000), strings.Repeat(" main", 5000)))
	if !ok || len(entries) != 1 || len(entries[0].Suites) != 2 || len(entries[0].Components) != 1 {
		t.Errorf("repeated words: understood %v, %+v", ok, entries)
	}
	if _, ok := parseOneLine("x.list", "deb http://x/debian a"+words("c", maxSourceComponents+1)+"\n"); ok {
		t.Error("a line with more components than a machine has is understood")
	}
	if entries, ok := parseOneLine("x.list", "deb http://x/debian a"+strings.Repeat(" main", maxSourceComponents)+"\n"); !ok || len(entries) != 1 || len(entries[0].Components) != 1 {
		t.Errorf("a line with a component many times: understood %v, %+v", ok, entries)
	}
}

// Sources that are many, whatever the files they are in, are more than a machine has.
func TestMoreSourcesThanAMachineHasInAllItsFilesAreNotRead(t *testing.T) {
	f := newBox(t)
	for _, name := range []string{"a", "b", "c"} {
		f.set("/etc/apt/sources.list.d/"+name+".list", strings.Repeat("deb http://deb.debian.org/debian bullseye main\n", 700))
	}
	if _, err := loadSources(context.Background(), f); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	g := newBox(t)
	for _, name := range []string{"a", "b"} {
		g.set("/etc/apt/sources.list.d/"+name+".list", strings.Repeat("deb http://deb.debian.org/debian bullseye main\n", 700))
	}
	if set, err := loadSources(context.Background(), g); err != nil || len(set.Entries) < 1400 {
		t.Fatalf("%d entries, err = %v", len(set.Entries), err)
	}
}

// Three shapes of a .sources file that a broken tool, a paste or a hostile hand can make, as large as a file can
// be: many addresses and many suites, many stanzas of the most suites there are, and many addresses of two suites.
// The checks of the sources answer for each in a few seconds, and none answers with a refusal because it did not
// have the time.
func TestAHostileSourcesFileStaysInsideItsTimeBound(t *testing.T) {
	var many strings.Builder
	many.WriteString("Types: deb deb-src\nURIs:")
	for i := 0; i < 1000; i++ {
		many.WriteString(" http://mirror" + strconv.Itoa(i) + ".example.org/debian")
	}
	many.WriteString("\nSuites:")
	for i := 0; i < 30000; i++ {
		many.WriteString(" s" + strconv.Itoa(i))
	}
	many.WriteString("\nComponents: main\n")

	var stanzas strings.Builder
	for i := 0; i < 1500; i++ {
		stanzas.WriteString("Types: deb\nURIs: http://mirror" + strconv.Itoa(i) + ".example.org/debian\nSuites:")
		for j := 0; j < maxSourceSuites; j++ {
			stanzas.WriteString(" s" + strconv.Itoa(j))
		}
		stanzas.WriteString("\nComponents: main contrib\n\n")
	}

	var uris strings.Builder
	uris.WriteString("Types: deb\nSuites: stable testing\nComponents: main\nURIs:")
	for i := 0; i < 1900; i++ {
		uris.WriteString("\n http://mirror" + strconv.Itoa(i) + ".example.org/debian")
	}
	uris.WriteString("\n")

	for name, text := range map[string]string{"many addresses and many suites": many.String(), "many stanzas of many suites": stanzas.String(), "many addresses": uris.String()} {
		t.Run(name, func(t *testing.T) {
			if len(text) > MaxFileBytes {
				t.Fatalf("the payload is %d bytes, more than a file can be", len(text))
			}
			f := newBox(t)
			f.set("/etc/apt/sources.list.d/x.sources", text)
			started := time.Now()
			report := Run(context.Background(), f, Options{})
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("the report took %v", elapsed)
			}
			for _, code := range []string{"sources_cdrom", "sources_alias", "sources_proposed", "sources_format", "sources_third_party", "sources_backports", "sources_firmware"} {
				if got := findingOf(t, report, code); got.Data["unknown"] == true && (got.Data["reason"] == ReasonTimeout || got.Data["reason"] == ReasonNotFinished) {
					t.Errorf("%s did not have the time: %+v", code, got)
				}
			}
		})
	}
}
