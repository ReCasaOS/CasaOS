package osreadiness

import (
	"context"
	"errors"
	"io/fs"
	"strconv"
	"testing"
)

func TestReadSourceFilesGivesTheFilesAptReadsInOrder(t *testing.T) {
	f := newBox(t)
	f.set("/etc/apt/sources.list.d/docker.list", "deb https://download.docker.com/linux/debian bullseye stable\n")
	f.set("/etc/apt/sources.list.d/zzz.sources", "Types: deb\nURIs: http://x\nSuites: bullseye\n")
	// names apt does not read, and a file that is not a regular one
	f.set("/etc/apt/sources.list.d/old.list.save", "deb http://x bullseye main\n")
	f.set("/etc/apt/sources.list.d/.hidden.list", "deb http://x bullseye main\n")
	f.set("/etc/apt/sources.list.d/note.txt", "x")
	f.device("/etc/apt/sources.list.d/null.list")
	got, err := ReadSourceFiles(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range got {
		names = append(names, file.Path())
		if file.Err != nil {
			t.Errorf("%s: unexpected error %v", file.Name, file.Err)
		}
	}
	want := []string{"/etc/apt/sources.list", "/etc/apt/sources.list.d/docker.list", "/etc/apt/sources.list.d/zzz.sources"}
	if len(names) != len(want) {
		t.Fatalf("files = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("files = %v, want %v", names, want)
		}
	}
	if got[0].InParts || !got[1].InParts {
		t.Errorf("InParts of sources.list and of docker.list = %v, %v", got[0].InParts, got[1].InParts)
	}
	if string(got[1].Content) != "deb https://download.docker.com/linux/debian bullseye stable\n" {
		t.Errorf("content = %q", got[1].Content)
	}
}

func TestReadSourceFilesSaysWhichFileCannotBeRead(t *testing.T) {
	f := newBox(t)
	f.set("/etc/apt/sources.list.d/a.list", "deb http://x bullseye main\n")
	f.fail("/etc/apt/sources.list.d/a.list", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
	got, err := ReadSourceFiles(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Name != "a.list" || got[1].Err == nil || got[1].Content != nil {
		t.Fatalf("files = %+v", got)
	}
}

func TestReadSourceFilesRefusesMoreFilesThanAMachineHas(t *testing.T) {
	f := newBox(t)
	for i := 0; i <= MaxSourceFiles; i++ {
		f.set("/etc/apt/sources.list.d/f"+strconv.Itoa(i)+".list", "")
	}
	if _, err := ReadSourceFiles(context.Background(), f); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestReadSourceFilesStopsWhenTheTimeIsOver(t *testing.T) {
	f := newBox(t)
	f.fail("/etc/apt/sources.list", context.DeadlineExceeded)
	if _, err := ReadSourceFiles(context.Background(), f); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestTheDistroDeciderAnswersAsTheReportDoesWhereItKnows(t *testing.T) {
	ctx := context.Background()
	f := newBox(t)
	inRelease("mirror.example.edu_debian_dists_bullseye", "Debian")(f)
	inRelease("vendor.example.com_debian_dists_stable", "Vendor")(f)
	release("old.example.org_debian_dists_bullseye", "Debian")(f)
	d := NewDistroDecider(f)
	for _, tc := range []struct {
		name, uri, suite string
		distro, decided  bool
	}{
		{"an official host", "http://deb.debian.org/debian", "bullseye", true, true},
		{"the security archive", "http://security.debian.org/debian-security", "bullseye-security", true, true},
		{"a disc", "cdrom:[Debian 11]/", "bullseye", false, true},
		{"a directory", "file:/srv/repo", "bullseye", false, true},
		{"a mirror whose Release says Debian", "http://mirror.example.edu/debian", "bullseye", true, true},
		{"a mirror that has a Release file and no InRelease", "http://old.example.org/debian", "bullseye", true, true},
		{"a vendor whose Release says another origin", "https://vendor.example.com/debian", "stable", false, true},
		{"Docker's, which needs no Release file", "https://download.docker.com/linux/debian", "bullseye", false, true},
		{"Docker's, wherever its address is written", "HTTPS://User@Download.Docker.com:443/linux/debian", "bullseye", false, true},
		{"a flat repository, which the archive is not", "http://mirror.example.edu/debian", "./", false, true},
	} {
		distro, decided, err := d.Suite(ctx, tc.uri, tc.suite)
		if err != nil || distro != tc.distro || decided != tc.decided {
			t.Errorf("%s: distro, decided, err = %v, %v, %v; want %v, %v", tc.name, distro, decided, err, tc.distro, tc.decided)
		}
	}
}

// What the report calls "not the distribution's" because apt has no Release file of it, the plan does not know: it
// rewrites the sources, and a line that it pauses by default is a repository that is left out of the plan without a word.
func TestTheDistroDeciderDoesNotKnowWhatItHasNoReleaseFileOf(t *testing.T) {
	ctx := context.Background()
	f := newBox(t)
	inRelease("unreadable.example.org_debian_dists_bullseye", "Debian")(f)
	f.fail(listsDir+"unreadable.example.org_debian_dists_bullseye_InRelease", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
	inRelease("mirror.example.edu_debian_dists_bullseye", "Debian")(f)
	d := NewDistroDecider(f)
	for _, tc := range []struct{ name, uri, suite string }{
		{"a mirror apt has no Release file of", "http://nofile.example.org/debian", "bullseye"},
		{"a mirror whose Release file cannot be read", "http://unreadable.example.org/debian", "bullseye"},
		{"a mirror list", "mirror+http://nofile.example.org/mirrors.txt", "bullseye"},
		{"a mirror list of the other form", "mirror://nofile.example.org/mirrors.txt", "bullseye"},
		{"a suite that is a variable", "http://nofile.example.org/debian", "$(ARCH)"},
		{"an address with no host", "http:///debian", "bullseye"},
		{"a host of Debian's that is not the archive", "http://people.debian.org/~someone/repo", "bullseye"},
		{"another suite of a mirror that has one Release file", "http://mirror.example.edu/debian", "bullseye-updates"},
	} {
		// asked twice: what was looked at is not decided by having been looked at
		for round := 1; round <= 2; round++ {
			distro, decided, err := d.Suite(ctx, tc.uri, tc.suite)
			if err != nil || distro || decided {
				t.Errorf("%s, asked %d times: distro, decided, err = %v, %v, %v; want false, false, nil", tc.name, round, distro, decided, err)
			}
		}
	}
}

// The names of Debian's archive are the distribution by their name; the other hosts under debian.org are asked of their
// Release file, as any mirror is.
func TestOnlyDebiansArchiveHostsAreTheDistributionByTheirName(t *testing.T) {
	ctx := context.Background()
	f := newBox(t)
	inRelease("snapshot.debian.org_archive_debian_20230101T000000Z_dists_bookworm", "Debian")(f)
	inRelease("people.debian.org_x_repo_dists_bullseye", "Someone")(f)
	d := NewDistroDecider(f)
	for _, host := range []string{"deb.debian.org", "DEB.debian.org", "security.debian.org", "archive.debian.org", "httpredir.debian.org", "ftp.debian.org",
		"ftp.fr.debian.org", "ftp2.de.debian.org", "cdn-fastly.deb.debian.org", "cdn-aws.deb.debian.org"} {
		if distro, decided, err := d.Suite(ctx, "http://"+host+"/debian", "bullseye"); !distro || !decided || err != nil {
			t.Errorf("%s: distro, decided, err = %v, %v, %v", host, distro, decided, err)
		}
	}
	for _, host := range []string{"debian.org", "snapshot.debian.org", "deb.debian.org.example.com", "xdeb.debian.org", "ftp.france.debian.org", "ftp.fr.debian.org.example.com", "cdn-.deb.debian.org", "www.debian.org", "mirrors.debian.org"} {
		if distro, decided, _ := d.Suite(ctx, "http://"+host+"/debian", "bullseye"); distro || decided {
			t.Errorf("%s: distro, decided = %v, %v: a name that is not the archive's is not the distribution's by its name", host, distro, decided)
		}
	}
	// a snapshot is a copy of the archive that says Debian: it is the distribution by its Release file, as any mirror
	if distro, decided, err := d.Suite(ctx, "http://snapshot.debian.org/archive/debian/20230101T000000Z/", "bookworm"); !distro || !decided || err != nil {
		t.Errorf("a snapshot whose Release says Debian: %v, %v, %v", distro, decided, err)
	}
	// a person's directory is another origin, or nothing
	if distro, decided, err := d.Suite(ctx, "http://people.debian.org/x/repo", "bullseye"); distro || !decided || err != nil {
		t.Errorf("a person's repository whose Release says another origin: %v, %v, %v", distro, decided, err)
	}
	if distro, decided, err := d.Suite(ctx, "http://people.debian.org/y/repo", "bullseye"); distro || decided || err != nil {
		t.Errorf("a person's repository with no Release file: %v, %v, %v", distro, decided, err)
	}
}

// The report does not change: a host under debian.org is the distribution's there whatever its Release file says, and a
// mirror with no Release file is a third party's (the plan says it does not know).
func TestTheReportStillTakesEveryDebianHostForTheDistributionAndAMirrorWithNoReleaseForAThirdParty(t *testing.T) {
	f := newBox(t)
	marked, err := markDistro(context.Background(), f, []sourceEntry{
		{File: "sources.list", Kind: "deb", URI: "http://people.debian.org/x/repo", Suites: []string{"bullseye"}},
		{File: "sources.list", Kind: "deb", URI: "http://nofile.example.org/debian", Suites: []string{"bullseye"}},
	})
	if err != nil || len(marked) != 2 || !marked[0].Distro || marked[1].Distro {
		t.Fatalf("marked = %+v, err = %v", marked, err)
	}
}

func TestTheDistroDeciderSaysItCannotDecideOnceItsLooksAreSpent(t *testing.T) {
	ctx := context.Background()
	f := newBox(t)
	for i := 0; i < MaxSourceLookups; i++ {
		inRelease("m"+strconv.Itoa(i)+".example.org_debian_dists_bullseye", "Vendor")(f)
	}
	// the one more has a Release file: only the bound can make it not known
	inRelease("one-more.example.org_debian_dists_bullseye", "Debian")(f)
	d := NewDistroDecider(f)
	for i := 0; i < MaxSourceLookups; i++ {
		_, decided, err := d.Suite(ctx, "http://m"+strconv.Itoa(i)+".example.org/debian", "bullseye")
		if err != nil || !decided {
			t.Fatalf("look %d: decided = %v, err = %v", i, decided, err)
		}
	}
	// a Release file that was looked at is answered again without a look
	if _, decided, _ := d.Suite(ctx, "http://m0.example.org/debian", "bullseye"); !decided {
		t.Error("an address that was looked at is not decided")
	}
	if distro, decided, err := d.Suite(ctx, "http://one-more.example.org/debian", "bullseye"); distro || decided || err != nil {
		t.Errorf("one look more: distro, decided, err = %v, %v, %v; want false, false, nil", distro, decided, err)
	}
	// the address of an official host needs no look
	if distro, decided, _ := d.Suite(ctx, "http://deb.debian.org/debian", "bullseye"); !distro || !decided {
		t.Error("an official host is decided without a look")
	}
}

func TestTheDistroDeciderReportsTheEndOfTheTime(t *testing.T) {
	f := newBox(t)
	f.fail(listsDir+"mirror.example.edu_debian_dists_bullseye_InRelease", context.DeadlineExceeded)
	d := NewDistroDecider(f)
	if _, decided, err := d.Suite(context.Background(), "http://mirror.example.edu/debian", "bullseye"); decided || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("decided, err = %v, %v", decided, err)
	}
}

func TestThePlanSeesSuitesAndAddressesAsTheReportDoes(t *testing.T) {
	if base, suffix := SplitSuite("bullseye-security"); base != "bullseye" || suffix != "-security" {
		t.Errorf("SplitSuite = %q, %q", base, suffix)
	}
	if !IsSuiteAlias("oldstable-updates") || IsSuiteAlias("bullseye") {
		t.Error("IsSuiteAlias")
	}
	if SourceScheme("HTTPS://x/y") != "https" || !SourceIsLocal("cdrom:[x]/") || SourceIsLocal("http://x") || SourceHost("http://User@Deb.Debian.org:80/x") != "deb.debian.org" {
		t.Error("the address readers")
	}
	if !AptFalse("no") || !AptFalse("0") || AptFalse("yes") || AptFalse("01") {
		t.Error("AptFalse")
	}
	if !AptSourceName("docker.list") || AptSourceName("docker.list.save") || AptSourceName(".x.list") {
		t.Error("AptSourceName")
	}
	if DockerHost != "download.docker.com" {
		t.Error("DockerHost")
	}
}

func TestGuardKeepsTheAllowListOfTheReport(t *testing.T) {
	f := newBox(t)
	g := Guard(f)
	if _, err := g.Run(context.Background(), "apt-get", "install", "x"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
	if _, err := g.Run(context.Background(), "dpkg", "--audit"); err != nil {
		t.Fatalf("dpkg --audit: %v", err)
	}
}

func TestHeldPackagesAndDockerAreAsTheReportSaysThem(t *testing.T) {
	ctx := context.Background()
	f := newBox(t)
	f.installed(held("docker-ce") + held("curl") + pkg("bash", "5.1"))
	docker, others, err := HeldPackages(ctx, f)
	if err != nil || len(docker) != 1 || docker[0] != "docker-ce" || len(others) != 1 || others[0] != "curl" {
		t.Fatalf("docker, others, err = %v, %v, %v", docker, others, err)
	}
	f.on("Docker version 28.0.4\n", 0, "docker", "--version")
	if there, err := DockerPresent(ctx, f); err != nil || !there {
		t.Fatalf("there, err = %v, %v", there, err)
	}
}
