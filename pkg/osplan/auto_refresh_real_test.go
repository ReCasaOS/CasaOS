package osplan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The refresh against what a real Debian 11 does (apt 2.2.4, dpkg 1.20, dash, mawk and the coreutils of the release, in a root of it): the texts in testdata/real/chroot-d11-refresh-*
// and chroot-d11-kernels-* are what the commands of the blocks print there, byte for byte, made as the other chroot-d11 files were, from a root of Debian 11.11 with the sources of
// the box of the measurement (the release, its updates, the security archive). The commands that made them:
//
//	apt-get -y -q -o Dpkg::Use-Pty=0 update --error-on=any                                                   chroot-d11-refresh-update (status 0)
//	apt-get -y -q -o Dpkg::Use-Pty=0 -s dist-upgrade -o APT::Get::Always-Include-Phased-Updates=true         chroot-d11-refresh-dist-upgrade (status 0): thirty packages, none removed
//	dpkg-query -W -f='${db:Status-Abbrev} ${Package} ${Version}\n' 'linux-image-*'                           chroot-d11-kernels-*: two kernel packages that are installed, the same
//	                                                                                                         two after a new kernel came, and the answer of dpkg when there is none (status 1)
//
// The whole refresh (the four blocks, as the unit runs them, with a gate that reads a file of cgroups of the test's) was also run there, for real, with the blocks of this text: the
// upgrade of the thirty packages, libc6 and dpkg among them, ended with dpkg quiet, /etc/apt as it was, and the word of the restart that the files said (no restart, a new kernel,
// a file that says reboot-required): see the commit that holds these tests.

// the text of one command of apt, as the stub of apt-get prints it
func (b *refreshBox) aptSays(name string) {
	b.t.Helper()
	b.write("fake/sim.txt", realFile(b.t, name))
}

func TestTheShellAndTheParserReadTheRealSimulationsOfAptAlike(t *testing.T) {
	for _, tc := range []struct {
		file    string
		removes int
		names   []string
	}{
		{"chroot-d11-refresh-dist-upgrade.txt", 0, nil},
		{"chroot-d11-nothing-dist-upgrade.txt", 0, nil},
		{"chroot-d11-12-dist-upgrade.txt", 1, []string{"libsemanage1"}},
		{"chroot-d11-remove-login.txt", 3, []string{"login", "mount", "util-linux"}},
	} {
		run, f := ParseSolver(realOutput(t, tc.file))
		if f != nil || run.Summary.Remove != tc.removes || !slices.Equal(sorted(run.Removals()), tc.names) {
			t.Errorf("%s: the parser reads %+v, %v", tc.file, run, f)
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "sim.txt"), []byte(realFile(t, tc.file)), 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := runSh(t, dir, shSim+"removes_of sim.txt\nremoved_of sim.txt\n")
		want := itoa(tc.removes) + "\n" + strings.Join(tc.names, "\n")
		if code != 0 || strings.TrimSpace(out) != strings.TrimSpace(want) {
			t.Errorf("%s: the shell reads %q (%d), the parser %q", tc.file, out, code, want)
		}
	}
}

func TestR2OnTheRealTextsOfAptAndTheirRemovals(t *testing.T) {
	// thirty packages and no removal: the refresh upgrades, and the real text goes through the three checks of the simulation
	b := newRefreshBox(t, false)
	b.aptSays("chroot-d11-refresh-dist-upgrade.txt")
	if out, code := b.through("rupgrade"); code != 0 || !strings.Contains(out, "Block R2 done.") {
		t.Fatalf("exit %d, %q", code, out)
	}
	// what a Debian 11 would remove: the simulation of the hop to Debian 12, which removes a package and which the refresh refuses (it removes nothing), and the real warning of apt about an essential package
	b = newRefreshBox(t, false)
	b.aptSays("chroot-d11-12-dist-upgrade.txt")
	b.through("rsources")
	b.mustStop("rupgrade", osrun.ClassPlan, "the current release wants to remove libsemanage1: use the manual path or the packages window")
	b = newRefreshBox(t, false)
	b.aptSays("chroot-d11-remove-login.txt")
	b.write("fake/essential-login", "yes required\n")
	b.through("rsources")
	b.mustStop("rupgrade", osrun.ClassGuard, "would remove login, which is essential or required, as dpkg says")
}

func TestR1ShowsTheRealUpdateAndStopsOnTheRealOneThatCouldNotReachItsMirror(t *testing.T) {
	b := newRefreshBox(t, false)
	update := realFile(t, "chroot-d11-refresh-update.txt")
	b.run("rcheck")
	b.fake("apt-get", "echo \"apt-get $*\" >> \"$FAKE/calls.log\"\ncase \"$*\" in *update*) cat \"$FAKE/update-says.txt\" ;; esac\nexit 0")
	b.write("fake/update-says.txt", update)
	if out, code := b.run("rsources"); code != 0 || !strings.Contains(out, "Get:1 http://deb.debian.org/debian bullseye InRelease [75.1 kB]") || !strings.Contains(out, "Block R1 done.") {
		t.Fatalf("exit %d, %q", code, out)
	}
	// the real answer of apt when its proxy refuses it: status 100, and the stop is the network's
	b = newRefreshBox(t, false)
	b.run("rcheck")
	b.fake("apt-get", "echo \"apt-get $*\" >> \"$FAKE/calls.log\"\ncase \"$*\" in *update*) cat \"$FAKE/update-says.txt\"; exit 100 ;; esac\nexit 0")
	b.write("fake/update-says.txt", realFile(t, "chroot-d11-update-proxy-refused.txt"))
	b.mustStop("rsources", osrun.ClassNetwork, "apt-get update does not go through")
}

// What apt of Debian 11 says when a repository changed its release information is made of two sentences that libapt-pkg 2.2.4 holds in its own strings
// ("Repository '%s' changed its '%s' value from '%s' to '%s'" and "This must be accepted explicitly before updates for this repository can be applied. See %s manpage for
// details."): R1 tells it from them, in either, and the line of the stop is the one that the page reads the hint from. An update that failed for another reason is not it.
func TestR1ToldTheChangeOfReleaseInformationFromTheSentencesThatAptHoldsAndOnlyFromThem(t *testing.T) {
	both := "Hit:1 http://deb.debian.org/debian-security bullseye-security InRelease\nGet:2 http://deb.debian.org/debian bullseye InRelease [75.1 kB]\n" +
		"E: Repository 'http://deb.debian.org/debian bullseye InRelease' changed its 'Suite' value from 'stable' to 'oldstable'\n" +
		"N: This must be accepted explicitly before updates for this repository can be applied. See apt-secure(8) manpage for details.\n"
	for _, tc := range []struct {
		name, says string
		hint       string
		class      string
	}{
		{"both sentences", both, osrun.HintReleaseInfoChange, osrun.ClassIdentity},
		{"the first only", strings.Split(both, "N: ")[0], osrun.HintReleaseInfoChange, osrun.ClassIdentity},
		{"the second only", "N: This must be accepted explicitly before updates for this repository can be applied. See apt-secure(8) manpage for details.\n", osrun.HintReleaseInfoChange, osrun.ClassIdentity},
		{"the label", "E: Repository 'http://x/y bullseye InRelease' changed its 'Label' value from 'a' to 'b'\n", osrun.HintReleaseInfoChange, osrun.ClassIdentity},
		{"the origin", "E: Repository 'http://x/y bullseye InRelease' changed its 'Origin' value from 'a' to 'b'\n", osrun.HintReleaseInfoChange, osrun.ClassIdentity},
		{"the codename", "E: Repository 'http://x/y bullseye InRelease' changed its 'Codename' value from 'a' to 'b'\n", osrun.HintReleaseInfoChange, osrun.ClassIdentity},
		{"the version", "E: Repository 'http://x/y bullseye InRelease' changed its 'Version' value from '11.1' to '11.2'\n", osrun.HintReleaseInfoChange, osrun.ClassIdentity},
		{"a signature that is not good", "W: GPG error: http://x/y bullseye InRelease: The following signatures were invalid: BADSIG 0E98404D386FA1D9\nE: The repository 'http://x/y bullseye InRelease' is not signed.\n", "", osrun.ClassNetwork},
		{"a mirror that does not answer", "Err:1 http://x/y bullseye InRelease\n  Could not resolve 'x'\nE: Failed to fetch http://x/y/dists/bullseye/InRelease\n", "", osrun.ClassNetwork},
		{"a Release file that has changed in some other way", "E: The repository 'http://x/y bullseye Release' no longer has a Release file.\n", "", osrun.ClassNetwork},
	} {
		b := newRefreshBox(t, false)
		b.run("rcheck")
		b.fake("apt-get", "echo \"apt-get $*\" >> \"$FAKE/calls.log\"\ncase \"$*\" in *update*) cat \"$FAKE/update-says.txt\"; exit 100 ;; esac\nexit 0")
		b.write("fake/update-says.txt", tc.says)
		out, code := b.run("rsources")
		stop := lastStop(out)
		if code == 0 || b.stopClass() != tc.class || osrun.HintOf(stop) != tc.hint {
			t.Errorf("%s: exit %d, class %q, hint %q of %q", tc.name, code, b.stopClass(), osrun.HintOf(stop), stop)
		}
	}
}

// The kernels that R0 writes down and R3 compares are what dpkg of Debian 11 says: a line for each package that is installed, the name and the version after the two words of its
// state, and the answer of dpkg when there is none (it says so on its error output and ends with 1: the block does not stop for it).
func TestTheKernelsAreReadFromWhatDpkgOfDebian11SaysAndAKernelThatCameIsFound(t *testing.T) {
	box := func(raw string, ends int) *refreshBox {
		b := newRefreshBox(t, false)
		// a dpkg-query that says what the real one said, on the output that it said it on, with the status that it ended with
		if ends == 0 {
			b.write("fake/kernel-list.txt", raw)
			return b
		}
		b.fake("dpkg-query", "cat >&2 <<'EOT'\n"+raw+"EOT\nexit 1")
		return b
	}
	// none at all: the block ends, with nothing written down
	b := box(realFile(t, "chroot-d11-kernels-none.txt"), 1)
	if out, code := b.run("rcheck"); code != 0 || !strings.Contains(out, "Block R0 done.") || b.read("bk/kernels-before.txt") != "" {
		t.Fatalf("no kernel: exit %d, %q, kernels-before %q", code, out, b.read("bk/kernels-before.txt"))
	}
	// two, and then a new one, and the metapackage with another version: R3 says yes
	b = box(realFile(t, "chroot-d11-kernels-before.txt"), 0)
	b.through("rupgrade")
	if want := "linux-image-5.10.0-99-cloud-amd64 5.10.209-1\nlinux-image-cloud-amd64 5.10.209-1\n"; b.read("bk/kernels-before.txt") != want {
		t.Fatalf("kernels-before.txt = %q, want %q", b.read("bk/kernels-before.txt"), want)
	}
	b.write("fake/kernel-list.txt", realFile(t, "chroot-d11-kernels-after.txt"))
	if out, code := b.run("rdone"); code != 0 || b.read("bk/refresh-reboot") != "yes kernel\n" {
		t.Errorf("a new kernel: exit %d, %q, refresh-reboot %q", code, out, b.read("bk/refresh-reboot"))
	}
	// the same two: no
	b = box(realFile(t, "chroot-d11-kernels-before.txt"), 0)
	b.through("rupgrade")
	if out, code := b.run("rdone"); code != 0 || b.read("bk/refresh-reboot") != "no none\n" {
		t.Errorf("the same kernels: exit %d, %q, refresh-reboot %q", code, out, b.read("bk/refresh-reboot"))
	}
}

// apt itself, where there is one: the sources of a refresh are read by it and say Debian 11 (it would fetch the release files of bullseye, its updates and its security archive),
// what is put aside is not there, and nothing of the next release is. The control is the rewrite of the hop, which says bookworm. apt-get --print-uris update reads the sources and
// the options and fetches nothing: it needs no network, and no more of the machine than a folder of its own.
func TestRealAptReadsTheSourcesOfARefreshAsTheReleaseThatIsLeft(t *testing.T) {
	bin, err := exec.LookPath("apt-get")
	if err != nil {
		t.Skip("no apt-get on this machine: this proof is made where apt is")
	}
	uris := func(hopSpec HopSpec) string {
		t.Helper()
		rw, err := RewriteSources(context.Background(), hopSpec, orderLikeTheBox(d11Box(t)), &fakeDecider{})
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		for _, f := range rw.Tree {
			path := filepath.Join(dir, f.Path)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(f.Content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, d := range []string{"parts", "lists/partial", "cache/archives/partial"} {
			if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command(bin, "-o", "Dir::Etc::sourcelist="+dir+"/sources.list", "-o", "Dir::Etc::sourceparts="+dir+"/parts", "-o", "Dir::State::lists="+dir+"/lists",
			"-o", "Dir::Cache="+dir+"/cache", "-o", "Debug::NoLocking=true", "--print-uris", "update")
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("apt-get --print-uris update: %v\n%s", err, out)
		}
		return string(out)
	}
	got := uris(bullseyeToBookworm.ForRefresh())
	for _, want := range []string{
		"http://deb.debian.org/debian/dists/bullseye/InRelease", "http://archive.debian.org/debian-security/dists/bullseye-security/InRelease",
		"http://deb.debian.org/debian/dists/bullseye-updates/InRelease", "http://deb.debian.org/debian/dists/bullseye/main/binary-amd64/Packages",
	} {
		if !strings.Contains(got, "'"+want) {
			t.Errorf("apt would not fetch %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "bookworm") || strings.Contains(got, "docker") {
		t.Errorf("apt reads the next release, or Docker's source, from the sources of a refresh:\n%s", got)
	}
	if control := uris(bullseyeToBookworm); !strings.Contains(control, "bookworm") || strings.Contains(control, "bullseye") {
		t.Errorf("the control, the rewrite of the hop, is not the next release for apt:\n%s", control)
	}
}
