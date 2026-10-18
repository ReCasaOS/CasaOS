package osplan

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The blocks of the refresh, run: in the box of the tests of the blocks of the hop (shellBox: the paths of the machine are a directory of the test, and its commands are stubs
// that answer what the test says), with the text of the unit and the gate that it opens on. The driver is not here: each block is run by itself, and the folder of the refresh is
// the box's bk. The driver on these blocks is in auto_refresh_flow_test.go.
//
// DEVELOPING NOTES: see the top of runbook_exec_test.go. These tests start a shell for each block that they run; -short skips them.

// A refreshBox is a machine that a refresh is run on: Debian 11 with the sources of the real box, two of the three timers of apt running, and the commands that the blocks ask.
type refreshBox struct {
	*shellBox
	ar *AutoRunbook
	in RefreshInputs
}

const phasedOption = "-o APT::Get::Always-Include-Phased-Updates=true"

func newRefreshBox(t *testing.T, accept bool) *refreshBox {
	t.Helper()
	sb := newShellBox(t)
	sb.auto = true
	setupVerify(t, sb)
	in := refreshInputsOf(t, accept)
	in.CgroupFile = "./cgroup"
	ar, err := BuildRefresh(in)
	if err != nil {
		t.Fatal(err)
	}
	// the restore script, which the blocks run as a process of its own, takes the paths of the machine from its environment
	sb.env = append(sb.env, "APT_ETC=./etc-apt", "CASAOS_NO_UPDATE=1", "CASAOS_TIMERS_STATE=./timers.state")
	// two of the three timers run; the box has the kernel that it runs, and the stub of dpkg-query lists the packages of the kernels with their versions
	sb.write("fake/active-apt-daily.timer", "")
	sb.write("fake/active-unattended-upgrades.service", "")
	sb.write("fake/kernel-list.txt", "ii  linux-image-5.10.0-46-cloud-amd64 5.10.209-2\nii  linux-image-cloud-amd64 5.10.209-2\nrc  linux-image-4.19.0-25-cloud-amd64 4.19.289-1\n")
	sb.fake("dpkg-query", `case "$*" in
  *'${Version}'*) cat "$FAKE/kernel-list.txt" 2>/dev/null || true; exit 0 ;;
esac
for last; do :; done
case "$last" in
  linux-image-*) grep " linux-image-[0-9]" "$FAKE/kernels.txt" 2>/dev/null || true ;;
  -f*) cat "$FAKE/packages.tsv" 2>/dev/null || true ;;
  *) case "$*" in
       *'${Essential}'*) if [ -f "$FAKE/essential-$last" ]; then cat "$FAKE/essential-$last"; else echo " optional"; fi ;;
       *) if [ -f "$FAKE/pkg-$last" ]; then cat "$FAKE/pkg-$last"; else echo "dpkg-query: no packages found matching $last" >&2; exit 1; fi ;;
     esac ;;
esac`)
	sb.write("fake/sim.txt", "Reading package lists...\nThe following packages will be upgraded:\n  bash libc6\n2 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n")
	return &refreshBox{shellBox: sb, ar: ar, in: in}
}

// run runs one block of the refresh, with the paths of the machine pointed at the box.
func (b *refreshBox) run(id string) (string, int) {
	b.t.Helper()
	text := b.paths(autoBlockOf(b.t, b.ar, id), "refresh-"+testRunID)
	text = strings.ReplaceAll(text, "/var/run/reboot-required", "./reboot-required")
	return b.runCode(text)
}

// through runs the blocks in order up to the one that is named, each of which before it has to end well, and says what the last printed and how it ended.
func (b *refreshBox) through(last string) (string, int) {
	b.t.Helper()
	var out string
	var code int
	for _, blk := range b.ar.Blocks {
		out, code = b.run(blk.ID)
		if blk.ID == last {
			return out, code
		}
		if code != 0 {
			b.t.Fatalf("block %s did not end (%d): %s", blk.ID, code, out)
		}
	}
	b.t.Fatalf("no block %s", last)
	return "", 0
}

// stopClass is the class that the trap of a block that stopped wrote in the folder of the run.
func (b *refreshBox) stopClass() string { return strings.TrimSpace(b.read("run/stop-class")) }

func (b *refreshBox) calls() []string {
	return strings.Split(strings.TrimSpace(b.read("fake/calls.log")), "\n")
}

// called says whether a command of the log of calls begins with a word.
func (b *refreshBox) called(prefix string) bool {
	return slices.ContainsFunc(b.calls(), func(l string) bool { return strings.HasPrefix(l, prefix) })
}

// verbs are the commands that change or ask what apt and dpkg hold, in the order that they ran, in a few words each.
func (b *refreshBox) verbs() []string {
	var out []string
	for _, l := range b.calls() {
		switch {
		case strings.HasPrefix(l, "dpkg --configure -a"):
			out = append(out, "dpkg-configure")
		case !strings.HasPrefix(l, "apt-get "):
		case strings.HasSuffix(l, " check"):
			out = append(out, "check")
		case strings.Contains(l, " update"):
			out = append(out, "update")
		case strings.Contains(l, " -s -f install"):
			out = append(out, "sim-repair")
		case strings.HasSuffix(l, " -f install"):
			out = append(out, "repair")
		case strings.Contains(l, " -s dist-upgrade"):
			out = append(out, "sim-dist")
		case strings.Contains(l, " --download-only dist-upgrade"):
			out = append(out, "download")
		case strings.Contains(l, " dist-upgrade"):
			out = append(out, "dist")
		default:
			out = append(out, "other: "+l)
		}
	}
	return out
}

func (b *refreshBox) mustStop(id, class, saying string) string {
	b.t.Helper()
	out, code := b.run(id)
	if code == 0 || !strings.Contains(out, "STOP: ") || !strings.Contains(out, saying) || b.exists("bk/done-"+id) {
		b.t.Errorf("block %s: exit %d, %q, done %v (it should say %q)", id, code, out, b.exists("bk/done-"+id), saying)
	}
	if got := b.stopClass(); got != class {
		b.t.Errorf("block %s: the class of the stop is %q, want %q", id, got, class)
	}
	return out
}

// sourcesAre says whether /etc/apt of the box holds exactly the files named, and no other.
func (b *refreshBox) sourcesAre(want map[string]string) bool {
	for name, content := range want {
		if b.read("etc-apt/"+name) != content {
			return false
		}
	}
	m, _ := filepath.Glob(filepath.Join(b.dir, "etc-apt", "sources.list.d", "*"))
	return len(m) == len(want)-1
}

func TestR0ChecksTheMachineAndWritesDownTheKernelsAndTheTimersAndStopsThem(t *testing.T) {
	b := newRefreshBox(t, false)
	out, code := b.run("rcheck")
	if code != 0 || !strings.Contains(out, "Block R0 done.") || !b.exists("bk/done-rcheck") || !b.exists("bk/done-timers") {
		t.Fatalf("a good machine: exit %d, %q", code, out)
	}
	if want := "linux-image-5.10.0-46-cloud-amd64 5.10.209-2\nlinux-image-cloud-amd64 5.10.209-2\n"; b.read("bk/kernels-before.txt") != want {
		t.Errorf("kernels-before.txt = %q, want %q", b.read("bk/kernels-before.txt"), want)
	}
	// the state of the timers, as block 1 of the hop writes it, outside the folder of the refresh, and the timers that ran are stopped
	if want := "apt-daily.timer active\napt-daily-upgrade.timer inactive\nunattended-upgrades.service active\n"; b.read("timers.state") != want {
		t.Errorf("the state of the timers = %q, want %q", b.read("timers.state"), want)
	}
	if b.exists("fake/active-apt-daily.timer") || b.exists("fake/active-unattended-upgrades.service") || !b.called("systemctl stop apt-daily.timer") {
		t.Errorf("the timers that ran are not stopped: %q", b.calls())
	}
	// the folder is the owner's alone, as the folder of the hop
	if runtime.GOOS != "windows" && b.mode("bk") != 0o700 {
		t.Errorf("the folder is %o", b.mode("bk"))
	}
	// nothing but the timers was touched: apt was only asked to check, and the sources are as they were
	if v := b.verbs(); !slices.Equal(v, []string{"check"}) {
		t.Errorf("R0 ran %q", v)
	}
	if !b.sourcesAre(originalSources(t)) {
		t.Error("R0 changed the sources")
	}
}

// a state of the timers that is there already is the one of before: it is kept (a run of block 1 of the hop that was given up, or another refresh)
func TestR0KeepsTheStateOfTheTimersThatIsThereAlready(t *testing.T) {
	b := newRefreshBox(t, false)
	b.write("timers.state", "apt-daily.timer active\napt-daily-upgrade.timer active\nunattended-upgrades.service inactive\n")
	if out, code := b.run("rcheck"); code != 0 || !strings.Contains(out, "is there from an earlier run") {
		t.Fatalf("exit %d, %q", code, out)
	}
	if b.read("timers.state") != "apt-daily.timer active\napt-daily-upgrade.timer active\nunattended-upgrades.service inactive\n" {
		t.Errorf("the state was written over: %q", b.read("timers.state"))
	}
}

// A package manager that is half way through an install leaves dpkg unquiet: R0 says what it is, busy, and does not send the owner to mend a dpkg that is only at work.
func TestR0SaysABusyPackageManagerBeforeItSaysThatDpkgIsNotQuiet(t *testing.T) {
	b := newRefreshBox(t, false)
	b.write("fake/ps.txt", psTable("apt-get         apt-get install foo"))
	b.write("fake/audit.txt", "The following packages are only half installed: foo\n")
	out, code := b.run("rcheck")
	if code == 0 || !strings.Contains(out, "STOP: a package manager is running") || strings.Contains(out, "dpkg is not quiet") || b.stopClass() != osrun.ClassBusy {
		t.Errorf("exit %d, class %q, %q", code, b.stopClass(), out)
	}
	if b.exists("bk/done-rcheck") || b.exists("bk/done-timers") || b.exists("timers.state") {
		t.Error("R0 changed something before it stopped")
	}
}

func TestR0StopsWithItsClassBeforeItChangesAnything(t *testing.T) {
	for _, tc := range []struct {
		name, class, saying string
		set                 func(b *refreshBox)
	}{
		{"not root", osrun.ClassIdentity, "run this as root", func(b *refreshBox) { b.fake("id", "echo 1000") }},
		{"another machine", osrun.ClassIdentity, "this machine is called another, which is not the machine that the refresh was started on", func(b *refreshBox) { b.fake("hostname", "echo another") }},
		{"not Debian 11", osrun.ClassIdentity, "this machine is not Debian 11 (bullseye) any more", func(b *refreshBox) { b.write("os-release", "VERSION_CODENAME=bookworm\n") }},
		{"the hop of another plan has begun", osrun.ClassIdentity, "another plan of this machine has begun the upgrade", func(b *refreshBox) {
			os.MkdirAll(filepath.Join(b.dir, "others", "casaos-os-upgrade-p1-aa"), 0o755)
			b.write("others/casaos-os-upgrade-p1-aa/begun-upgrade", "")
		}},
		{"the sources of another plan are Debian 12's", osrun.ClassIdentity, "block 3 of another plan of this machine began", func(b *refreshBox) {
			os.MkdirAll(filepath.Join(b.dir, "others", "casaos-os-upgrade-p1-aa", "apt-orig"), 0o755)
		}},
		{"a hop that ended", osrun.ClassIdentity, "the hop is over for this machine", func(b *refreshBox) {
			os.MkdirAll(filepath.Join(b.dir, "others", "casaos-os-upgrade-p1-aa"), 0o755)
			b.write("others/casaos-os-upgrade-p1-aa/done-after", "")
		}},
		{"dpkg is not quiet", osrun.ClassDpkgBroken, "dpkg is not quiet", func(b *refreshBox) { b.write("fake/audit.txt", "The following packages are only half installed\n") }},
		{"apt-get check fails", osrun.ClassDpkgBroken, "apt-get check finds a broken dependency", func(b *refreshBox) { b.write("fake/check-fails", "") }},
		{"a package manager is running", osrun.ClassBusy, "a package manager is running (the process list says: apt-get apt-get update)", func(b *refreshBox) {
			b.write("fake/ps.txt", psTable("apt-get         apt-get update"))
		}},
		{"ps does not answer", osrun.ClassTools, "ps did not answer", func(b *refreshBox) { b.fake("ps", "exit 1") }},
		{"no room for the packages", osrun.ClassDisk, "/var/cache/apt/archives has less than 1.5 GB free", func(b *refreshBox) { b.write("fake/avail_var_cache_apt_archives", "1000\n") }},
		{"no room in /", osrun.ClassDisk, "/ has less than 2 GB free", func(b *refreshBox) { b.write("fake/avail_", "1000\n") }},
		{"the state of the timers cannot be put", osrun.ClassDisk, "the state of the timers could not be put in", func(b *refreshBox) {
			b.fake("ln", "exit 1")
			b.fake("mv", "exit 1")
		}},
	} {
		b := newRefreshBox(t, false)
		tc.set(b)
		b.mustStop("rcheck", tc.class, tc.saying)
		// a stop before the timers were touched leaves no sign that they were, and the sources are not touched at all
		if b.exists("bk/done-timers") || b.called("systemctl stop") {
			t.Errorf("%s: the timers were touched", tc.name)
		}
		if !b.sourcesAre(originalSources(t)) {
			t.Errorf("%s: R0 changed the sources", tc.name)
		}
	}
	// a timer that stays up after it was asked to stop: the state is written and done-timers is there, so that the unwind starts again the ones that were stopped
	b := newRefreshBox(t, false)
	b.fake("systemctl", `echo "systemctl $*" >> "$FAKE/calls.log"
case "$1" in
  is-active) if [ -f "$FAKE/active-$2" ]; then echo active; else echo inactive; exit 3; fi ;;
  stop) [ "$2" = unattended-upgrades.service ] || rm -f "$FAKE/active-$2" ;;
esac
exit 0`)
	b.mustStop("rcheck", osrun.ClassBusy, "unattended-upgrades.service is still running")
	if !b.exists("bk/done-timers") || !b.exists("timers.state") {
		t.Error("a timer that stays up: no done-timers or no state, and the unwind would not start the timers that were stopped")
	}
}

// R0 asks for nothing that a unit does not have: it is the gate of the unit that stands where a person's confirmation did.
func TestEveryBlockOfTheRefreshStopsOutsideItsUnit(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(b *refreshBox)
	}{
		{"another run", func(b *refreshBox) { b.env = append(b.env, "CASAOS_OSU_RUN=00000000-0000-4000-8000-000000000000") }},
		{"no cgroup of the unit", func(b *refreshBox) { b.write("cgroup", "0::/user.slice/user-0.slice/session-1.scope\n") }},
	} {
		for _, blk := range everyAutoBlock(refreshOf(t, false)) {
			b := newRefreshBox(t, false)
			tc.set(b)
			out, code := b.run(blk.ID)
			if code == 0 || !strings.Contains(out, "STOP: this block belongs to the upgrade unit") || b.stopClass() != osrun.ClassUnit {
				t.Errorf("%s, block %s: exit %d, class %q, %q", tc.name, blk.ID, code, b.stopClass(), out)
			}
			if b.exists("bk/kernels-before.txt") || b.exists("bk/done-"+blk.ID) || b.exists("timers.state") || len(b.verbs()) > 0 {
				t.Errorf("%s, block %s: it did something: %q", tc.name, blk.ID, b.verbs())
			}
		}
	}
}

func TestR1KeepsACopyAndWritesTheSourcesOfTheRefreshAndFetchesTheLists(t *testing.T) {
	b := newRefreshBox(t, false)
	out, code := b.through("rsources")
	if code != 0 || !strings.Contains(out, "Block R1 done.") || !b.exists("bk/done-rsources") {
		t.Fatalf("exit %d, %q", code, out)
	}
	// the copy of /etc/apt, whole, and its restore script, which is the owner's but for what it says of a hop
	if !b.exists("bk/apt-orig/sources.list") || !b.exists("bk/apt-orig/sources.list.d/docker.list") || b.exists("bk/apt-orig.new") {
		t.Error("the copy of /etc/apt is not there, or is not whole")
	}
	if b.read("bk/restore-sources.sh") != restoreScriptRefresh {
		t.Error("the restore script is not the refresh's")
	}
	// the refresh removes nothing, and says so to the block that mends
	if !b.exists("bk/expected-removed.txt") || b.read("bk/expected-removed.txt") != "" {
		t.Errorf("expected-removed.txt = %q", b.read("bk/expected-removed.txt"))
	}
	// the sources are the machine's own, as they were, and Docker's is put aside
	orig := originalSources(t)
	if got := b.read("etc-apt/sources.list"); got != orig["sources.list"] {
		t.Errorf("sources.list = %q, want the machine's own %q", got, orig["sources.list"])
	}
	if b.exists("etc-apt/sources.list.d/docker.list") || b.read("etc-apt/sources.list.d/docker.list.casaos-paused") != orig["sources.list.d/docker.list"] {
		t.Error("Docker's source is not put aside")
	}
	// apt-get update, as the hop's block 3 runs it, and no option that the owner did not give
	if v := b.verbs(); !slices.Equal(v, []string{"check", "update"}) {
		t.Errorf("apt was asked %q", v)
	}
	for _, l := range b.calls() {
		if strings.Contains(l, " update") && !strings.HasSuffix(l, " update --error-on=any") {
			t.Errorf("the update is %q", l)
		}
	}
}

func TestR1GivesAptTheOptionThatTheOwnerAccepted(t *testing.T) {
	b := newRefreshBox(t, true)
	if out, code := b.through("rsources"); code != 0 {
		t.Fatalf("exit %d, %q", code, out)
	}
	if got := b.calls(); !slices.ContainsFunc(got, func(l string) bool { return strings.HasSuffix(l, " update --error-on=any --allow-releaseinfo-change") }) {
		t.Errorf("the update does not have the option: %q", got)
	}
}

func TestR1StopsWithItsClassAndTheCopyIsThereWheneverTheSourcesMayHaveChanged(t *testing.T) {
	releaseInfo := "E: Repository 'http://deb.debian.org/debian bullseye InRelease' changed its 'Label' value from 'Debian' to 'Debian-Archive'\nN: This must be accepted explicitly before updates for this repository can be applied. See apt-secure(8) manpage for details.\n"
	for _, tc := range []struct {
		name, class, saying string
		set                 func(b *refreshBox)
		changed             bool // the sources may have been changed by the block when it stopped
	}{
		{"the update does not go through", osrun.ClassNetwork, "apt-get update does not go through", func(b *refreshBox) { b.write("fake/update-fails", "") }, true},
		{"a repository changed its release information", osrun.ClassIdentity, "apt refuses a repository whose release information changed", func(b *refreshBox) {
			b.write("fake/update-says.txt", releaseInfo)
			b.write("fake/update-fails", "")
		}, true},
		{"the sources changed since the refresh was started", osrun.ClassPlan, "the sources of apt are not the ones the refresh was started from", func(b *refreshBox) {
			b.write("etc-apt/sources.list.d/new.list", "# new\n")
		}, false},
		{"a package manager is running", osrun.ClassBusy, "a package manager is running", func(b *refreshBox) { b.write("fake/ps.txt", psTable("dpkg            /usr/bin/dpkg --configure -a")) }, false},
	} {
		b := newRefreshBox(t, false)
		if out, code := b.run("rcheck"); code != 0 {
			t.Fatalf("%s: R0: %d, %q", tc.name, code, out)
		}
		// an apt-get that says what the test wants it to say of its update
		b.fake("apt-get", `echo "apt-get $*" >> "$FAKE/calls.log"
case "$*" in
  *update*) [ ! -f "$FAKE/update-says.txt" ] || cat "$FAKE/update-says.txt"; [ ! -f "$FAKE/update-fails" ] || exit 100 ;;
esac
exit 0`)
		tc.set(b)
		out := b.mustStop("rsources", tc.class, tc.saying)
		// a stop that comes after the sources were written has the copy and the script that put them back, and one before it has neither
		if has := b.exists("bk/apt-orig"); has != tc.changed || b.exists("bk/restore-sources.sh") != tc.changed {
			t.Errorf("%s: a copy %v, a script %v: the sources %v changed", tc.name, has, b.exists("bk/restore-sources.sh"), map[bool]string{true: "may have", false: "have not"}[tc.changed])
		}
		if tc.class == osrun.ClassIdentity && osrun.HintOf(lastStop(out)) != osrun.HintReleaseInfoChange {
			t.Errorf("%s: the page would not offer to accept the change: the hint of %q is %q", tc.name, lastStop(out), osrun.HintOf(lastStop(out)))
		}
		if tc.class != osrun.ClassIdentity && osrun.HintOf(lastStop(out)) != "" {
			t.Errorf("%s: a hint for a stop that is not that of a change of release information: %q", tc.name, lastStop(out))
		}
	}
}

// lastStop is the last line of an output that begins with STOP.
func lastStop(out string) string {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "STOP:") {
			return lines[i]
		}
	}
	return ""
}

func TestR2BringsTheOldReleaseUpToDateWithThePhasedUpdatesAndRemovesNothing(t *testing.T) {
	b := newRefreshBox(t, false)
	out, code := b.through("rupgrade")
	if code != 0 || !strings.Contains(out, "Block R2 done.") || !b.exists("bk/done-rupgrade") {
		t.Fatalf("exit %d, %q", code, out)
	}
	// the simulation, the download and the upgrade, in this order, each with the option; then dpkg and apt are asked whether they are quiet
	if got, want := b.verbs(), []string{"check", "update", "sim-dist", "download", "dist", "check"}; !slices.Equal(got, want) {
		t.Errorf("apt was asked %q, want %q", got, want)
	}
	for _, l := range b.calls() {
		if strings.Contains(l, "dist-upgrade") && !strings.HasSuffix(l, " "+phasedOption) {
			t.Errorf("an apt command that does not include the phased updates: %q", l)
		}
		if strings.Contains(l, "remove") || strings.Contains(l, "purge") || strings.Contains(l, "autoremove") {
			t.Errorf("an apt command that removes: %q", l)
		}
	}
}

func TestR2StopsOnAnyRemovalWhateverThePackageIsAndBeforeAnythingIsDownloaded(t *testing.T) {
	for _, tc := range []struct {
		name, class, saying string
		sim                 string
	}{
		{"a package that the owner may have", osrun.ClassPlan, "the current release wants to remove fuse: use the manual path or the packages window", resimOf("fuse")},
		{"a package with a star", osrun.ClassPlan, "the current release wants to remove fuse: use the manual path", resimOf("fuse*")},
		{"two of them", osrun.ClassPlan, "the current release wants to remove fuse", resimOf("fuse", "libsemanage1")},
		{"a protected package, which the guard tells first", osrun.ClassGuard, "would remove openssh-server, which is protected", resimOf("openssh-server")},
		{"a downgrade", osrun.ClassPlan, "apt would downgrade packages", "The following packages will be DOWNGRADED:\n  bash\n1 upgraded, 0 newly installed, 0 downgraded, 0 to remove and 0 not upgraded.\n"},
		{"no summary", osrun.ClassAptFailed, "apt printed no summary line", "Reading package lists...\n"},
	} {
		b := newRefreshBox(t, false)
		b.write("fake/sim.txt", tc.sim)
		if out, code := b.through("rsources"); code != 0 {
			t.Fatalf("%s: R1: %d, %q", tc.name, code, out)
		}
		b.mustStop("rupgrade", tc.class, tc.saying)
		// the stop is before the first command that changes a package
		if v := b.verbs(); !slices.Equal(v, []string{"check", "update", "sim-dist"}) {
			t.Errorf("%s: apt was asked %q", tc.name, v)
		}
	}
}

// A machine whose sources still say security.debian.org for bullseye: apt reads the archive's address for the refresh (R1 writes it, and says which line), and R3 puts the owner's
// own file back byte for byte, which its hash proves.
func TestTheRefreshReadsTheSecurityArchiveForAptAndPutsTheOwnersFileBack(t *testing.T) {
	own := "deb http://deb.debian.org/debian bullseye main\ndeb http://security.debian.org/debian-security bullseye-security main\n"
	b := newRefreshBox(t, false)
	b.write("etc-apt/sources.list", own)
	files := []osreadiness.SourceFile{file("sources.list", own)}
	for _, f := range d11Box(t) {
		if f.InParts {
			files = append(files, f)
		}
	}
	rw, err := RewriteSources(context.Background(), bullseyeToBookworm.ForRefresh(), files, &fakeDecider{})
	if err != nil {
		t.Fatal(err)
	}
	b.in.Rewritten = rw
	if b.ar, err = BuildRefresh(b.in); err != nil {
		t.Fatal(err)
	}
	out, code := b.through("rsources")
	if code != 0 || !strings.Contains(out, "Note: sources.list line 2: deb http://security.debian.org/debian-security bullseye-security main is read for the refresh as deb http://archive.debian.org/debian-security bullseye-security main") {
		t.Fatalf("exit %d, %q", code, out)
	}
	if got := b.read("etc-apt/sources.list"); !strings.Contains(got, "deb http://archive.debian.org/debian-security bullseye-security main\n") || strings.Contains(got, "security.debian.org") {
		t.Errorf("the sources that apt reads for the refresh: %q", got)
	}
	if got := b.read("bk/security-archive"); !strings.Contains(got, "sources.list line 2") {
		t.Errorf("the mark: %q", got)
	}
	if out, code := b.run("rupgrade"); code != 0 {
		t.Fatalf("R2: exit %d, %q", code, out)
	}
	if out, code := b.run("rdone"); code != 0 || b.read("etc-apt/sources.list") != own {
		t.Errorf("exit %d, %q: the owner's file is %q", code, out, b.read("etc-apt/sources.list"))
	}
	// a machine that is on the archive already, or on a mirror of its own, has nothing to say
	b = newRefreshBox(t, false)
	if out, code := b.through("rsources"); code != 0 || strings.Contains(out, "Note:") || b.exists("bk/security-archive") {
		t.Errorf("exit %d, %q", code, out)
	}
}

// A download that gets 404 from security.debian.org (the real text of apt 2.2.4, made in a root of Debian 11.11) stops with the class network, and the line of the stop names the
// host and gives the hint that the page reads. A download that fails for another reason, or whose 404 is from another host, keeps the generic line and no hint.
func TestR2NamesTheSecurityHostWhenItAnswers404AndOnlyThen(t *testing.T) {
	real404 := realFile(t, "chroot-d11-refresh-security-404-download.txt")
	for _, tc := range []struct {
		name, says string
		hint       string
		saying     string
	}{
		{"the real text of apt", real404, osrun.HintSecurityArchiveGap, "security.debian.org answered 404 Not Found"},
		{"the mirror's path of the same archive", "E: Failed to fetch http://deb.debian.org/debian-security/pool/updates/main/o/openssl/o_1_amd64.deb  404  Not Found [IP: 1.2.3.4 80]\nE: Some files failed to download\n", osrun.HintSecurityArchiveGap, "deb.debian.org answered 404 Not Found"},
		{"an alias of the mirror", "E: Failed to fetch https://cdn-fastly.deb.debian.org/debian-security/pool/updates/main/o/o_1_amd64.deb  404  Not Found [IP: 1.2.3.4 443]\n", osrun.HintSecurityArchiveGap, "cdn-fastly.deb.debian.org answered 404 Not Found"},
		{"a 404 that is not an E: line", "Err:1 http://security.debian.org/debian-security bullseye-security/main amd64 dpkg amd64 1\n  404  Not Found [IP: 1.2.3.4 80]\nW: Failed to fetch http://security.debian.org/debian-security/pool/o.deb  404  Not Found\n", "", "the network, or a lock held"},
		{"a line that only quotes one", "N: the earlier try said E: Failed to fetch http://security.debian.org/debian-security/pool/o.deb  404  Not Found\n", "", "the network, or a lock held"},
		{"the path of the release's own mirror", "E: Failed to fetch http://cdn-fastly.deb.debian.org/debian/pool/main/o/o_1_amd64.deb  404  Not Found\n", "", "the network, or a lock held"},
		{"a 404 from the owner's mirror", "E: Failed to fetch http://mirror.example.org/debian/pool/main/o/o_1_amd64.deb  404  Not Found [IP: 1.2.3.4 80]\nE: Some files failed to download\n", "", "the network, or a lock held"},
		{"the release's own mirror", "E: Failed to fetch http://deb.debian.org/debian/pool/main/o/o_1_amd64.deb  404  Not Found [IP: 1.2.3.4 80]\n", "", "the network, or a lock held"},
		{"the security host does not answer", "E: Failed to fetch http://security.debian.org/debian-security/pool/updates/main/o/o_1_amd64.deb  Temporary failure resolving 'security.debian.org'\n", "", "the network, or a lock held"},
		{"a name that only looks like it", "E: Failed to fetch http://security.debian.org.example.org/debian-security/pool/o.deb  404  Not Found\n", "", "the network, or a lock held"},
		{"nothing said", "", "", "the network, or a lock held"},
	} {
		b := newRefreshBox(t, false)
		b.write("fake/download-says.txt", tc.says)
		b.write("fake/download-fails", "")
		b.through("rsources")
		out := b.mustStop("rupgrade", osrun.ClassNetwork, tc.saying)
		if got := osrun.HintOf(lastStop(out)); got != tc.hint {
			t.Errorf("%s: the hint of %q is %q, want %q", tc.name, lastStop(out), got, tc.hint)
		}
		// what apt printed is shown, and kept
		if tc.says != "" && (!strings.Contains(out, strings.TrimSpace(tc.says[:40])) || b.read("bk/download.txt") != tc.says) {
			t.Errorf("%s: apt's text is not shown and kept: %q", tc.name, b.read("bk/download.txt"))
		}
	}
	// a download that goes through leaves no mark of a failure, and the block goes on
	b := newRefreshBox(t, false)
	b.write("fake/download-says.txt", realFile(t, "chroot-d11-refresh-archive-download.txt"))
	// a status left by an earlier try of the block is not this try's
	b.through("rsources")
	b.write("bk/download.rc", "100\n")
	if out, code := b.run("rupgrade"); code != 0 || b.exists("bk/download.rc") || !strings.Contains(out, "Block R2 done.") {
		t.Errorf("exit %d, %q", code, out)
	}
}

func TestR2StopsWhenTheDownloadDoesNotGoThroughOrWhenDpkgIsNotQuietAfterwards(t *testing.T) {
	b := newRefreshBox(t, false)
	b.write("fake/download-fails", "")
	b.through("rsources")
	b.mustStop("rupgrade", osrun.ClassNetwork, "the download did not go through")

	// dpkg that says something after the upgrade is not an upgrade that went well
	b = newRefreshBox(t, false)
	b.through("rsources")
	b.write("fake/audit.txt", "The following packages are only half installed\n")
	b.mustStop("rupgrade", osrun.ClassDpkgBroken, "dpkg is not quiet")

	b = newRefreshBox(t, false)
	b.through("rsources")
	b.write("fake/check-fails", "")
	b.mustStop("rupgrade", osrun.ClassDpkgBroken, "apt-get check finds a broken dependency")
}

// A full upgrade that fails goes up the ladder of the hop's block 6, with the same option, and the repair allows no removal either.
func TestR2RepairsAFailedUpgradeAsBlock6DoesAndRemovesNothingOnTheWay(t *testing.T) {
	b := newRefreshBox(t, false)
	b.write("fake/dist-fails-1", "")
	out, code := b.through("rupgrade")
	if code != 0 || !strings.Contains(out, "The dist-upgrade failed once") {
		t.Fatalf("a failure and a repair: exit %d, %q", code, out)
	}
	// the first simulation, the download and the first full upgrade, then the ladder: configure, simulate the repair, repair, simulate the full upgrade, the full upgrade again
	want := []string{"check", "update", "sim-dist", "download", "dist", "dpkg-configure", "sim-repair", "repair", "sim-dist", "dist", "check"}
	if got := b.verbs(); !slices.Equal(got, want) {
		t.Errorf("the ladder went %q, want %q:\n%s", got, want, strings.Join(b.calls(), "\n"))
	}
	for _, l := range b.calls() {
		if strings.Contains(l, "dist-upgrade") && !strings.HasSuffix(l, " "+phasedOption) {
			t.Errorf("an apt command that does not include the phased updates: %q", l)
		}
	}
	// a repair that removes a package stops, whatever the package is: the plan has no word of any
	for _, tc := range []struct {
		name, class, saying string
		fix, after          string
		repairs, dists      int // how many times the repair and the full upgrade ran for real
	}{
		{"a repair that removes", osrun.ClassPlan, "the current release wants to remove fuse", resimOf("fuse"), "", 0, 1},
		{"a repair that removes a protected package", osrun.ClassGuard, "the repair would remove sudo, which is protected", resimOf("sudo"), "", 0, 1},
		{"a full upgrade after the repair that removes", osrun.ClassPlan, "the current release wants to remove fuse", "", resimOf("fuse"), 1, 1},
	} {
		b := newRefreshBox(t, false)
		b.write("fake/dist-fails-1", "")
		if tc.fix != "" {
			b.write("fake/fixsim.txt", tc.fix)
		}
		if tc.after != "" {
			b.write("fake/sim2.txt", tc.after)
		}
		b.through("rsources")
		b.mustStop("rupgrade", tc.class, tc.saying)
		count := func(verb string) int {
			n := 0
			for _, v := range b.verbs() {
				if v == verb {
					n++
				}
			}
			return n
		}
		if count("repair") != tc.repairs || count("dist") != tc.dists {
			t.Errorf("%s: the repair ran %d times and the full upgrade %d: %q", tc.name, count("repair"), count("dist"), b.verbs())
		}
	}
	// a ladder that cannot go on says why, with the class of the hop's own text
	for _, tc := range []struct {
		name, class, saying, file string
	}{
		{"dpkg cannot configure", osrun.ClassDpkgBroken, "dpkg could not configure what is half done", "dpkg-fails"},
		{"apt cannot work out the repair", osrun.ClassAptFailed, "apt cannot work out the repair", "fixsim-fails"},
		{"the repair fails", osrun.ClassAptFailed, "apt could not repair the packages", "fix-fails"},
	} {
		b := newRefreshBox(t, false)
		b.write("fake/dist-fails-1", "")
		b.write("fake/"+tc.file, "")
		b.through("rsources")
		b.mustStop("rupgrade", tc.class, tc.saying)
	}
	b = newRefreshBox(t, false)
	b.write("fake/dist-fails-1", "")
	b.write("fake/dist-fails-2", "")
	b.through("rsources")
	b.mustStop("rupgrade", osrun.ClassAptFailed, "the full upgrade failed again after the repair")
}

func TestR3PutsTheSourcesAndTheTimersBackAndSaysWhetherTheMachineWantsARestart(t *testing.T) {
	const before = "ii  linux-image-5.10.0-46-cloud-amd64 5.10.209-2\nii  linux-image-cloud-amd64 5.10.209-2\nrc  linux-image-4.19.0-25-cloud-amd64 4.19.289-1\n"
	for _, tc := range []struct {
		name    string
		kernels string // what dpkg lists after the upgrade
		reboot  bool   // the packages ask for a restart
		wants   string
	}{
		{"nothing to restart for", before, false, "no none\n"},
		{"the packages say so", before, true, "yes reboot-required\n"},
		{"a kernel is new", "ii  linux-image-5.10.0-46-cloud-amd64 5.10.209-2\nii  linux-image-5.10.0-47-cloud-amd64 5.10.216-1\nii  linux-image-cloud-amd64 5.10.216-1\n", false, "yes kernel\n"},
		{"the metapackage of the kernel has another version", "ii  linux-image-5.10.0-46-cloud-amd64 5.10.209-2\nii  linux-image-cloud-amd64 5.10.216-1\n", false, "yes kernel\n"},
		{"a kernel went", "ii  linux-image-cloud-amd64 5.10.209-2\n", false, "no none\n"},
		{"a kernel that is not installed is not one", before + "rc  linux-image-5.10.0-47-cloud-amd64 5.10.216-1\n", false, "no none\n"},
		{"both", "ii  linux-image-5.10.0-47-cloud-amd64 5.10.216-1\n", true, "yes reboot-required\n"},
	} {
		b := newRefreshBox(t, false)
		if out, code := b.through("rupgrade"); code != 0 {
			t.Fatalf("%s: R2: %d, %q", tc.name, code, out)
		}
		b.write("fake/kernel-list.txt", tc.kernels)
		if tc.reboot {
			b.write("reboot-required", "")
		}
		out, code := b.run("rdone")
		if code != 0 || !strings.Contains(out, "Block R3 done.") || !b.exists("bk/done-rdone") {
			t.Fatalf("%s: exit %d, %q", tc.name, code, out)
		}
		if got := b.read("bk/refresh-reboot"); got != tc.wants {
			t.Errorf("%s: refresh-reboot = %q, want %q", tc.name, got, tc.wants)
		}
		// the sources are as they were, Docker's included, and the copy has been put aside by the restore script
		if !b.sourcesAre(originalSources(t)) {
			t.Errorf("%s: the sources are not as they were", tc.name)
		}
		if b.exists("bk/apt-orig") {
			t.Errorf("%s: apt-orig is still there", tc.name)
		}
		// the timers that ran run again, and their state is forgotten
		if !b.exists("fake/active-apt-daily.timer") || !b.exists("fake/active-unattended-upgrades.service") || b.exists("fake/active-apt-daily-upgrade.timer") || b.exists("timers.state") {
			t.Errorf("%s: the timers are not back as they were: %q", tc.name, b.calls())
		}
	}
}

// What R3 puts back is checked against the files that the refresh started from: a copy that is not what it was, or a restore that restored something else, is a stop, and not
// a refresh that ended.
func TestR3StopsWhenTheSourcesThatItPutBackAreNotTheOnesTheRefreshStartedFrom(t *testing.T) {
	b := newRefreshBox(t, false)
	b.through("rupgrade")
	// the copy that block R1 made is not what the machine had
	b.write("bk/apt-orig/sources.list", "deb http://deb.debian.org/debian bullseye main contrib\n")
	b.mustStop("rdone", osrun.ClassIntegrity, "the sources of apt are not what they were before the refresh")
	if b.exists("bk/refresh-reboot") || b.exists("bk/done-rdone") {
		t.Error("the refresh ended with sources that are not the machine's own")
	}
	// the timers are left to the unwind, which finds done-timers and the state: R3 did not start them before it knew that the sources were right
	if b.exists("fake/active-apt-daily.timer") || !b.exists("timers.state") {
		t.Error("the timers were started before the check of the sources")
	}

	// a restore script that cannot do its work
	b = newRefreshBox(t, false)
	b.through("rupgrade")
	b.write("bk/restore-sources.sh", "#!/bin/sh\nexit 1\n")
	b.mustStop("rdone", osrun.ClassIntegrity, "the restore script could not put the sources back")
}

func TestEveryBlockAfterTheFirstStopsWhenTheOneBeforeItDidNotEnd(t *testing.T) {
	ids := refreshOfBlocks(t)
	for i, id := range ids[1:] {
		b := newRefreshBox(t, false)
		out, code := b.run(id)
		if prev := ids[i]; code == 0 || !strings.Contains(out, "STOP: the block before this one ("+prev+") did not end") || b.stopClass() != osrun.ClassIntegrity {
			t.Errorf("block %s: exit %d, class %q, %q", id, code, b.stopClass(), out)
		}
	}
	// and the machine that is not Debian 11 any more is not refreshed from the middle
	b := newRefreshBox(t, false)
	b.through("rsources")
	b.write("os-release", "VERSION_CODENAME=bookworm\n")
	b.mustStop("rupgrade", osrun.ClassIdentity, "this machine is not Debian 11 (bullseye) any more")
}

func refreshOfBlocks(t *testing.T) []string {
	t.Helper()
	var ids []string
	for _, b := range refreshOf(t, false).Blocks {
		ids = append(ids, b.ID)
	}
	return ids
}

// Block F of a refresh works in the folder of the refresh, and the list of the removals that it holds a repair to is the one that R1 wrote, which is empty: the repair of a
// refresh removes nothing.
func TestBlockFOfARefreshAllowsNoRemovalAtAll(t *testing.T) {
	runF := func(b *refreshBox) (string, int) {
		return b.runCode(b.paths(autoBlockOf(t, b.ar, osrun.BlockForward), "refresh-"+testRunID))
	}
	b := newRefreshBox(t, false)
	b.through("rsources")
	b.write("fake/fixsim.txt", resimOf("fuse"))
	if out, code := runF(b); code == 0 || !strings.Contains(out, "the repair would remove fuse, which the plan does not name") || b.stopClass() != osrun.ClassGuard {
		t.Errorf("F on a repair that removes: exit %d, class %q, %q", code, b.stopClass(), out)
	}
	// with the list of the removals gone, F writes it empty, and says the same
	b = newRefreshBox(t, false)
	b.through("rsources")
	os.Remove(filepath.Join(b.dir, "bk", "expected-removed.txt"))
	b.write("fake/fixsim.txt", resimOf("fuse"))
	if out, code := runF(b); code == 0 || !strings.Contains(out, "the repair would remove fuse") {
		t.Errorf("F with no list: exit %d, %q", code, out)
	}
	if !b.exists("bk/expected-removed.txt") || b.read("bk/expected-removed.txt") != "" {
		t.Error("F did not write the list of the removals empty")
	}
	// a repair that removes nothing goes through
	b = newRefreshBox(t, false)
	b.through("rsources")
	b.write("fake/fixsim.txt", resimOf())
	if out, code := runF(b); code != 0 || !strings.Contains(out, "Block F done.") {
		t.Errorf("F on a repair that removes nothing: exit %d, %q", code, out)
	}
	if got := b.verbs(); got[len(got)-1] != "repair" {
		t.Errorf("F did not repair: %q", got)
	}
}

// F stops the units of apt and starts none, in a refresh as in a hop: the unwind of a refresh runs it before it starts the timers, from the state it kept, and the blocks of a hop
// that follow F stop the timers themselves.
func TestBlockFStopsTheUnitsOfAptAndStartsNoneInARefreshAsInAHop(t *testing.T) {
	b := newRefreshBox(t, false)
	b.write("fake/active-apt-daily.service", "")
	b.write("fake/active-apt-daily.timer", "")
	b.write("fake/active-unattended-upgrades.service", "")
	out, code := b.runCode(b.paths(autoBlockOf(t, b.ar, osrun.BlockForward), "refresh-"+testRunID))
	if code != 0 || !strings.Contains(out, "Block F done.") {
		t.Fatalf("F of a refresh: exit %d, %q", code, out)
	}
	calls := strings.Join(b.calls(), "\n")
	if stop, repair := strings.Index(calls, "systemctl stop apt-daily.timer"), strings.Index(calls, " -f install"); stop < 0 || repair < stop {
		t.Errorf("the order of F: stop %d, repair %d:\n%s", stop, repair, calls)
	}
	if strings.Contains(calls, "systemctl start") {
		t.Errorf("F of a refresh started a unit:\n%s", calls)
	}
	for _, unit := range []string{"apt-daily.timer", "apt-daily.service", "unattended-upgrades.service"} {
		if b.exists("fake/active-" + unit) {
			t.Errorf("%s runs when F is done", unit)
		}
	}
	// F of a hop: the same units are stopped, and none is started
	hop, ar := fBox(t)
	hop.write("fake/active-apt-daily.timer", "")
	if out, code := runForward(hop, ar); code != 0 {
		t.Fatalf("F of a hop: exit %d, %q", code, out)
	}
	if got := hop.read("fake/calls.log"); !strings.Contains(got, "systemctl stop apt-daily.timer") || strings.Contains(got, "systemctl start") {
		t.Errorf("F of a hop: %q", got)
	}
}

// The comment of a line of the owner's file is in the note of R1: the shell must print it as it is (dash's echo acts on a backslash, printf '%s' does not), in the output and in the
// mark.
func TestTheNoteOfR1PrintsTheCommentOfTheOwnersLineAsItIs(t *testing.T) {
	comment := `# \033[31mred\c \n \ $(touch pwned) ` + "`touch pwned2`" + ` "q" 'x'`
	own := "deb http://deb.debian.org/debian bullseye main\ndeb https://security.debian.org/debian-security bullseye-security main " + comment + "\n"
	b := newRefreshBox(t, false)
	b.write("etc-apt/sources.list", own)
	files := []osreadiness.SourceFile{file("sources.list", own)}
	for _, f := range d11Box(t) {
		if f.InParts {
			files = append(files, f)
		}
	}
	rw, err := RewriteSources(context.Background(), bullseyeToBookworm.ForRefresh(), files, &fakeDecider{})
	if err != nil {
		t.Fatal(err)
	}
	b.in.Rewritten = rw
	if b.ar, err = BuildRefresh(b.in); err != nil {
		t.Fatal(err)
	}
	out, code := b.through("rsources")
	if code != 0 || !strings.Contains(out, "Note: sources.list line 2: deb https://security.debian.org/debian-security bullseye-security main "+comment+" is read") || strings.ContainsRune(out, 0x1b) {
		t.Fatalf("exit %d, %q", code, out)
	}
	if got := b.read("bk/security-archive"); !strings.Contains(got, comment+" is read") || strings.ContainsRune(got, 0x1b) || b.exists("pwned") || b.exists("pwned2") {
		t.Errorf("the mark: %q", got)
	}
	if got := b.read("etc-apt/sources.list"); !strings.Contains(got, "deb https://archive.debian.org/debian-security bullseye-security main "+comment+"\n") {
		t.Errorf("the https line did not keep its scheme: %q", got)
	}
}
