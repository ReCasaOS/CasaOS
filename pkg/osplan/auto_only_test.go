package osplan

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// What only the unit's text does: the gate, the class that a stop leaves for the driver, what the unit does not stop for, and block F. The tests that the owner's text has too are run
// on both texts (auto_exec_test.go); these are about what the owner's text has no counterpart of.

// autoBox is a box that runs the unit's blocks, in the unit of the run of the tests, and the text of the real plan in the frame that blockOf reads.
func autoBox(t *testing.T) (*shellBox, *Runbook, *AutoRunbook) {
	t.Helper()
	b := newShellBox(t)
	b.auto = true
	rb, ar := autoFrame(t, *realPlan(t), AutoParams{RunID: testRunID, Hostname: testHostname, CgroupFile: "./cgroup"})
	return b, rb, ar
}

// stopClassOf is what a block left for the driver in the folder of the run, "" when it left nothing.
func stopClassOf(b *shellBox) string { return strings.TrimSuffix(b.read("run/stop-class"), "\n") }

// runForward runs block F of an auto runbook in the box.
func runForward(b *shellBox, ar *AutoRunbook) (string, int) {
	b.t.Helper()
	return b.runCode(b.paths(ar.Forward.Text, ar.PlanID))
}

// ---- the gate --------------------------------------------------------------------------------------------------------------------------------

// A block of the unit runs in the unit of its run and nowhere else: not in a terminal where somebody pasted it, not in another run's unit, not where the cgroup of the shell does
// not name the unit. Nothing ran, and the class that the driver reads is unit.
func TestAnAutoBlockRunsOnlyInTheUnitOfItsRun(t *testing.T) {
	_, rb, ar := autoBox(t)
	p := realPlan(t)
	// the file of cgroups that the unit's text reads is /proc/self/cgroup when the core gives none: this test machine's own names no unit
	_, real := autoFrame(t, *p, AutoParams{RunID: testRunID, Hostname: testHostname})
	for _, tc := range []struct {
		name  string
		set   func(b *shellBox)
		text  func(id string) string
		class bool
	}{
		{"outside the unit: no run in the environment", func(b *shellBox) { b.auto = false }, nil, false},
		{"the unit of another run", func(b *shellBox) { b.env = []string{"CASAOS_OSU_RUN=11111111-2222-4333-8444-555555555555"} }, nil, true},
		{"the unit of no run (the word is empty)", func(b *shellBox) { b.env = []string{"CASAOS_OSU_RUN="} }, nil, true},
		{"a shell whose cgroup does not name the unit", func(b *shellBox) { b.write("cgroup", "0::/user.slice/user-0.slice/session-3.scope\n") }, nil, true},
		{"a shell whose cgroup names a unit that only looks like it", func(b *shellBox) { b.write("cgroup", "0::/system.slice/casaos-os-upgrade-x.service.d\n") }, nil, true},
		{"no file of cgroups", func(b *shellBox) { os.Remove(filepath.Join(b.dir, "cgroup")) }, nil, true},
		{"the file of cgroups of the machine itself, which names no unit", func(b *shellBox) {}, func(id string) string { return autoBlockOf(t, real, id) }, true},
	} {
		ids := []string{"verify", "timers", "snapshot", "sources", "download", "minimal", "full", "prereboot", "after", "forward"}
		for _, id := range ids {
			b := newShellBox(t)
			b.auto = true
			tc.set(b)
			text := autoBlockOf(t, ar, id)
			if tc.text != nil {
				text = tc.text(id)
			}
			out, code := b.runCode(b.paths(text, rb.PlanID))
			if code == 0 || !strings.Contains(out, "STOP: this block belongs to the upgrade unit of the dashboard and runs only there") {
				t.Errorf("%s, block %s: exit %d, %q", tc.name, id, code, out)
			}
			if b.exists("fake/calls.log") || len(globDone(b)) > 0 || b.exists("timers.state") {
				t.Errorf("%s, block %s: something ran: calls %q, markers %v", tc.name, id, b.read("fake/calls.log"), globDone(b))
			}
			if got := stopClassOf(b); tc.class && got != "unit" || !tc.class && got != "" {
				t.Errorf("%s, block %s: the class left for the driver is %q", tc.name, id, got)
			}
		}
	}
}

// globDone are the markers of blocks in the backup folder of a box.
func globDone(b *shellBox) []string {
	m, _ := filepath.Glob(filepath.Join(b.dir, "bk", "done-*"))
	return m
}

// ---- the class of a stop ------------------------------------------------------------------------------------------------------------------------

// A block that stops leaves the class of its STOP line in the folder of the run, for the driver to read: one case for each family of the table, in the block that has it.
func TestTheClassOfAStopIsLeftInTheFolderOfTheRunForTheDriver(t *testing.T) {
	p := realPlan(t)
	rb, ar := autoFrame(t, *p, AutoParams{RunID: testRunID, Hostname: testHostname, CgroupFile: "./cgroup"})
	expires := p.ExpiresAt.Unix()
	at := func(text *Runbook, from, to string) *Runbook { return withText(t, text, from, to) }
	left := "Reading package lists...\n12 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"
	kernelBox := func(b *shellBox) {
		b.write("bk/done-full", "")
		b.write("bk/sshd-keys.txt", "port 22\n")
		b.write("bk/iptables-alternative.txt", "")
		b.write("fake/sshd-T.txt", "port 22\n")
		b.write("fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\nii  linux-image-6.1.0-53-cloud-amd64\n")
		b.write("bk/packages.tsv", "ii \tlinux-image-5.10.0-46-cloud-amd64\t5.10.209-2\n")
		b.write("fake/pkg-systemd-timesyncd", "ii ")
		b.write("boot/vmlinuz-6.1.0-53-cloud-amd64", "k")
		b.sized("boot/initrd.img-6.1.0-53-cloud-amd64", initrdOK)
		b.write("boot/vmlinuz-5.10.0-46-cloud-amd64", "k")
		b.write("boot/grub/grub.cfg", "menuentry { linux /boot/vmlinuz-6.1.0-53-cloud-amd64 }\n")
		b.write("bootid", "boot-1\n")
	}
	for _, tc := range []struct {
		name, id string
		text     *Runbook
		set      func(b *shellBox)
		class    string
	}{
		// block 0
		{"a package manager is running", "verify", rb, func(b *shellBox) {
			setupVerify(t, b)
			b.write("fake/ps.txt", psTable("apt-get         /usr/bin/apt-get -y upgrade"))
		}, "busy"},
		{"ps does not answer", "verify", rb, func(b *shellBox) { setupVerify(t, b); b.fake("ps", "exit 1") }, "tools"},
		{"not root", "verify", rb, func(b *shellBox) { setupVerify(t, b); b.fake("id", "echo 1000") }, "identity"},
		{"not Debian 11", "verify", rb, func(b *shellBox) { setupVerify(t, b); b.write("os-release", "VERSION_CODENAME=bookworm\n") }, "identity"},
		{"dpkg is not quiet", "verify", rb, func(b *shellBox) { setupVerify(t, b); b.write("fake/audit.txt", "half\n") }, "dpkg_broken"},
		{"apt-get check fails", "verify", rb, func(b *shellBox) { setupVerify(t, b); b.write("fake/check-fails", "") }, "dpkg_broken"},
		{"no room in /usr", "verify", rb, func(b *shellBox) { setupVerify(t, b); b.write("fake/avail_usr", "1000\n") }, "disk"},
		{"no room in /boot", "verify", rb, func(b *shellBox) { setupVerify(t, b); b.write("fake/avail_boot", "1000\n") }, "disk"},
		{"the plan is past its 24 hours", "verify", rb, func(b *shellBox) { setupVerify(t, b); b.write("fake/now", strconv.FormatInt(expires, 10)+"\n") }, "plan"},
		{"the sources are not the plan's", "verify", rb, func(b *shellBox) { setupVerify(t, b); b.write("etc-apt/sources.list", "deb http://x bullseye main\n") }, "plan"},
		{"grub-pc has no disk", "verify", rb, func(b *shellBox) { setupVerify(t, b); b.write("fake/pkg-grub-pc", "ii ") }, "barrier_grub"},
		{"no ss", "verify", at(rb, "command -v ss ", "command -v ss-which-is-not-there "), func(b *shellBox) { setupVerify(t, b) }, "tools"},
		{"block 3 began", "verify", rb, func(b *shellBox) { setupVerify(t, b); os.MkdirAll(filepath.Join(b.dir, "bk", "apt-orig"), 0o755) }, "integrity"},
		{"the upgrade has begun", "verify", rb, func(b *shellBox) { setupVerify(t, b); b.write("bk/begun-upgrade", "") }, "identity"},
		{"another plan began", "verify", rb, func(b *shellBox) {
			setupVerify(t, b)
			b.write("others/casaos-os-upgrade-p1-0123456789ab/begun-upgrade", "")
		}, "identity"},
		// blocks 1 and 2
		{"a timer stays up", "timers", rb, func(b *shellBox) {
			b.write("bk/done-verify", "")
			b.fake("systemctl", `case "$1" in is-active) echo active;; esac; exit 0`)
		}, "busy"},
		{"the block before did not end", "timers", rb, func(b *shellBox) {}, "integrity"},
		{"the hop is over", "timers", rb, func(b *shellBox) { b.write("bk/done-after", "") }, "identity"},
		{"a later block has ended", "snapshot", rb, func(b *shellBox) { b.write("bk/done-timers", ""); b.write("bk/done-sources", "") }, "integrity"},
		{"tar cannot write", "snapshot", rb, func(b *shellBox) {
			b.fake("tar", "exit 2")
			b.write("bk/done-timers", "")
			b.write("fake/packages.tsv", "ii \tbash\t5.1-2\n")
		}, "disk"},
		// block 3 and 4
		{"apt-get update fails", "sources", rb, func(b *shellBox) { sourcesBox(t, b); b.write("fake/update-fails", "") }, "network"},
		{"apt would remove another set", "sources", rb, func(b *shellBox) { sourcesBox(t, b); b.write("fake/sim.txt", resimOf("fuse", "libsemanage1", "samba")) }, "plan"},
		{"apt would keep a package back", "sources", rb, func(b *shellBox) {
			sourcesBox(t, b)
			b.write("fake/sim.txt", resimKept(3, true, "fuse", "libsemanage1"))
		}, "plan"},
		{"apt would downgrade", "sources", rb, func(b *shellBox) {
			sourcesBox(t, b)
			b.write("fake/sim.txt", "The following packages will be REMOVED:\n  fuse libsemanage1\n1 upgraded, 0 newly installed, 1 downgraded, 2 to remove and 0 not upgraded.\n")
		}, "plan"},
		{"apt cannot work out the upgrade", "sources", rb, func(b *shellBox) { sourcesBox(t, b); b.write("fake/sim-fails-1", "") }, "apt_failed"},
		{"apt prints no summary", "sources", rb, func(b *shellBox) { sourcesBox(t, b); b.write("fake/sim.txt", "Reading package lists...\n") }, "apt_failed"},
		{"the files written for apt are not the plan's", "sources", at(rb, "bookworm main", "bookwurm main"), func(b *shellBox) { sourcesBox(t, b) }, "plan"},
		{"the restore script is not the one of the text", "sources", at(rb, "echo \"The sources are back as they were.\"", "echo \"The sources are back.\""), func(b *shellBox) { sourcesBox(t, b) }, "integrity"},
		{"apt-orig is there", "sources", rb, func(b *shellBox) {
			sourcesBox(t, b)
			os.MkdirAll(filepath.Join(b.dir, "bk", "apt-orig"), 0o755)
			b.write("bk/restore-sources.sh", "x")
		}, "integrity"},
		{"the download fails", "download", rb, func(b *shellBox) {
			b.write("bk/done-sources", "")
			b.write("archives/a.deb", "x")
			b.write("fake/download-fails", "")
		}, "network"},
		{"nothing was downloaded", "download", rb, func(b *shellBox) { b.write("bk/done-sources", "") }, "network"},
		// blocks 5 and 6
		{"a protected package would go", "minimal", rb, func(b *shellBox) {
			b.write("bk/done-download", "")
			b.write("fake/sim.txt", resimOf("fuse", "samba-libs"))
		}, "guard"},
		{"the minimal upgrade fails after it began", "minimal", rb, func(b *shellBox) {
			b.write("bk/done-download", "")
			b.write("fake/upgrade-fails", "")
			b.write("fake/upgrade-touches-status", "")
		}, "apt_failed"},
		{"apt fails before it changed anything", "minimal", rb, func(b *shellBox) { b.write("bk/done-download", ""); b.write("fake/upgrade-fails", "") }, "apt_failed"},
		{"a package manager is running (block 5)", "minimal", rb, func(b *shellBox) {
			b.write("bk/done-download", "")
			b.write("fake/ps.txt", psTable("apt-get         apt-get upgrade"))
		}, "busy"},
		{"the download of the full upgrade fails", "full", rb, func(b *shellBox) { b.write("bk/done-minimal", ""); b.write("fake/download-fails", "") }, "network"},
		{"the repair fails", "full", rb, func(b *shellBox) {
			b.write("bk/done-minimal", "")
			b.write("fake/dist-fails-1", "")
			b.write("fake/fix-fails", "")
		}, "apt_failed"},
		{"dpkg cannot configure", "full", rb, func(b *shellBox) {
			b.write("bk/done-minimal", "")
			b.write("fake/dist-fails-1", "")
			b.write("fake/dpkg-fails", "")
		}, "dpkg_broken"},
		{"a catch-up pass fails", "full", rb, func(b *shellBox) {
			b.write("bk/done-minimal", "")
			b.write("fake/sim1.txt", left)
			b.write("fake/dist-fails-2", "")
		}, "apt_failed"},
		{"a catch-up pass would remove a protected package", "full", rb, func(b *shellBox) {
			b.write("bk/done-minimal", "")
			b.write("fake/sim1.txt", resimOf("fuse", "samba-libs"))
		}, "guard"},
		{"nothing converges", "full", rb, func(b *shellBox) { b.write("bk/done-minimal", ""); b.write("fake/sim.txt", left) }, "converge"},
		{"the check at the end fails", "full", rb, func(b *shellBox) { b.write("bk/done-minimal", ""); b.write("fake/check-fails", "") }, "dpkg_broken"},
		// block 7
		{"no kernel", "prereboot", rb, func(b *shellBox) {
			kernelBox(b)
			os.Remove(filepath.Join(b.dir, "boot", "vmlinuz-6.1.0-53-cloud-amd64"))
		}, "barrier_kernel"},
		{"dpkg has no kernel", "prereboot", rb, func(b *shellBox) { kernelBox(b); b.write("fake/kernels.txt", "") }, "barrier_kernel"},
		{"the old kernel is not kept", "prereboot", rb, func(b *shellBox) {
			kernelBox(b)
			os.Remove(filepath.Join(b.dir, "boot", "vmlinuz-5.10.0-46-cloud-amd64"))
		}, "barrier_kernel"},
		{"no initrd", "prereboot", rb, func(b *shellBox) {
			kernelBox(b)
			os.Remove(filepath.Join(b.dir, "boot", "initrd.img-6.1.0-53-cloud-amd64"))
		}, "barrier_initrd"},
		{"an initrd that is cut short", "prereboot", rb, func(b *shellBox) { kernelBox(b); b.sized("boot/initrd.img-6.1.0-53-cloud-amd64", 1000) }, "barrier_initrd"},
		{"lsinitramfs cannot open the initrd", "prereboot", rb, func(b *shellBox) { kernelBox(b); b.write("fake/lsinitramfs.rc", "1") }, "barrier_initrd"},
		{"grub does not name the kernel", "prereboot", rb, func(b *shellBox) {
			kernelBox(b)
			b.write("boot/grub/grub.cfg", "menuentry { linux /boot/vmlinuz-5.10.0-46-cloud-amd64 }\n")
		}, "barrier_grub"},
		{"sshd does not accept its configuration", "prereboot", rb, func(b *shellBox) { kernelBox(b); b.write("fake/sshd-t.rc", "255") }, "barrier_sshd"},
		{"the settings of sshd changed", "prereboot", rb, func(b *shellBox) { kernelBox(b); b.write("fake/sshd-T.txt", "port 2222\n") }, "barrier_sshd"},
		{"the resolver is not installed", "prereboot", rb, func(b *shellBox) {
			kernelBox(b)
			b.write("fake/resolv-link", "../run/systemd/resolve/stub-resolv.conf\n")
		}, "barrier_resolver"},
		{"no time daemon", "prereboot", rb, func(b *shellBox) { kernelBox(b); os.Remove(filepath.Join(b.dir, "fake", "pkg-systemd-timesyncd")) }, "barrier_other"},
		{"the iptables alternative changed", "prereboot", rb, func(b *shellBox) { kernelBox(b); b.write("fake/iptables-now", "/usr/sbin/iptables-legacy\n") }, "barrier_other"},
		{"no lsinitramfs", "prereboot", at(rb, "command -v lsinitramfs ", "command -v lsinitramfs-which-is-not-there "), func(b *shellBox) { kernelBox(b) }, "tools"},
		{"block 2 left no list of packages", "prereboot", rb, func(b *shellBox) { kernelBox(b); os.Remove(filepath.Join(b.dir, "bk", "packages.tsv")) }, "integrity"},
		// block 8
		{"not restarted", "after", rb, func(b *shellBox) { setupBlock8(b); b.write("bootid", "boot-1\n") }, "identity"},
		{"still Debian 11", "after", rb, func(b *shellBox) { setupBlock8(b); b.write("os-release", "VERSION_CODENAME=bullseye\n") }, "identity"},
		{"the old kernel runs", "after", rb, func(b *shellBox) { setupBlock8(b); b.write("fake/uname-r", "5.10.0-46-cloud-amd64\n") }, "barrier_kernel"},
		{"block 7 wrote no kernel", "after", rb, func(b *shellBox) { setupBlock8(b); os.Remove(filepath.Join(b.dir, "bk", "kernel")) }, "integrity"},
		{"the sources do not name bookworm", "after", rb, func(b *shellBox) {
			setupBlock8(b)
			b.write("etc-apt/sources.list", "deb http://deb.debian.org/debian bullseye main\n")
		}, "identity"},
		{"dpkg is not quiet after the restart", "after", rb, func(b *shellBox) { setupBlock8(b); b.write("fake/audit.txt", "half\n") }, "dpkg_broken"},
	} {
		b := newShellBox(t)
		b.auto = true
		tc.set(b)
		out, code := b.run(tc.text, tc.id)
		if code == 0 || !strings.Contains(out, "STOP:") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
			continue
		}
		if got := stopClassOf(b); got != tc.class {
			t.Errorf("%s: the class left for the driver is %q, want %q (%q)", tc.name, got, tc.class, out)
		}
	}
	_ = ar
}

// sourcesBox is the box that block 3 is happy with.
func sourcesBox(t *testing.T, b *shellBox) {
	t.Helper()
	b.write("bk/done-snapshot", "")
	setupVerify(t, b)
	b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
}

// A block that is killed by set -e, with no STOP line to say why, has the class unknown: the class that the block starts with is the one that it dies with.
func TestABlockThatDiesOfSetEHasTheClassUnknown(t *testing.T) {
	b, rb, _ := autoBox(t)
	b.write("bk/done-timers", "")
	b.fake("dpkg-query", "exit 1")
	out, code := b.run(rb, "snapshot")
	if code == 0 || strings.Contains(out, "STOP:") || b.exists("bk/done-snapshot") {
		t.Fatalf("a block that dies: exit %d, %q", code, out)
	}
	if got := stopClassOf(b); got != "unknown" {
		t.Errorf("the class of a block that dies of set -e is %q", got)
	}
}

// A block that ends with 0 leaves no class, even where it found a restart that had come already (block 7's early path ends with exit 0 in the middle of the block).
func TestABlockThatEndsWithZeroLeavesNoClassNotEvenWhereItExitsEarly(t *testing.T) {
	b, rb, _ := autoBox(t)
	b.write("bk/done-full", "")
	b.write("bk/packages.tsv", "ii \tbash\t5.1-2\nii \tlinux-image-5.10.0-46-cloud-amd64\t5.10.209-2\n")
	b.write("bootid-now", "boot-B\n")
	b.write("fake/uname-r", "6.1.0-53-cloud-amd64\n")
	b.write("fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\nii  linux-image-6.1.0-53-cloud-amd64\n")
	b.write("fake/pkg-systemd-timesyncd", "ii ")
	b.write("bk/iptables-alternative.txt", "")
	out, code := runBlock7(b, rb)
	if code != 0 || !b.exists("bk/done-prereboot") || b.exists("bk/boot_id") || !strings.Contains(out, "it already runs it") {
		t.Fatalf("the early path of block 7: exit %d, %q", code, out)
	}
	if b.exists("run/stop-class") {
		t.Errorf("a block that ended with 0 left the class %q", b.read("run/stop-class"))
	}
}

// A failure that the block handles itself leaves no class: block 2 takes a tar that says 1 (a file changed as it was read) for a good archive, and the subshell that ran tar does
// not run the trap of the block (the trap is not inherited by a subshell).
func TestAFailureThatABlockHandlesItselfLeavesNoClass(t *testing.T) {
	b, rb, _ := autoBox(t)
	b.write("bk/done-timers", "")
	b.write("root/etc/hosts", "127.0.0.1 localhost\n")
	b.write("fake/packages.tsv", "ii \tbash\t5.1-2\n")
	b.write("fake/ss.txt", "LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\n")
	b.fake("tar", "echo archive > \"$4\"; echo 'tar: etc/resolv.conf: file changed as we read it' >&2; exit 1")
	out, code := b.run(rb, "snapshot")
	if code != 0 || !b.exists("bk/done-snapshot") || !strings.Contains(out, "a file changed while it archived it") {
		t.Fatalf("exit %d, %q", code, out)
	}
	if b.exists("run/stop-class") {
		t.Errorf("a failure that the block handled left the class %q", b.read("run/stop-class"))
	}
}

// ---- the three words of the owner ----------------------------------------------------------------------------------------------------------------

// The name of the machine is written by the core, and the block compares it with the machine's own all the same.
func TestAutoBlock0StopsWhenTheNameThatIsWrittenIsNotTheMachinesOwn(t *testing.T) {
	p := *realPlan(t)
	rb, _ := autoFrame(t, p, AutoParams{RunID: testRunID, Hostname: "another", CgroupFile: "./cgroup"})
	b := newShellBox(t)
	b.auto = true
	setupVerify(t, b)
	out, code := b.run(rb, "verify")
	if code == 0 || !strings.Contains(out, "STOP: this machine is called testbox") || b.exists("bk/done-verify") || stopClassOf(b) != "identity" {
		t.Errorf("exit %d, class %q, %q", code, stopClassOf(b), out)
	}
	good, _ := autoFrame(t, p, AutoParams{RunID: testRunID, Hostname: "testbox", CgroupFile: "./cgroup"})
	b = newShellBox(t)
	b.auto = true
	setupVerify(t, b)
	if out, code := b.run(good, "verify"); code != 0 || !b.exists("bk/done-verify") {
		t.Errorf("the right name, nothing typed: exit %d, %q", code, out)
	}
}

// The removals that the owner accepted are written by the core: block 3 asks for nothing, and still stops when the plan's own list is not the one of apt now.
func TestAutoBlock3HasTheAcceptedRemovalsWrittenIn(t *testing.T) {
	b, rb, _ := autoBox(t)
	sourcesBox(t, b)
	if out, code := b.run(rb, "sources"); code != 0 || !b.exists("bk/done-sources") || strings.Contains(out, "type the names") {
		t.Fatalf("exit %d, %q", code, out)
	}
	if !strings.Contains(rb.Text, "ACCEPT_REMOVALS='fuse'\n") {
		t.Error("the names are not written in the block")
	}
}

// The confirmation is baked in: block 5 begins the upgrade with nothing typed, and the marker is written before apt is run.
func TestAutoBlock5HasTheConfirmationBakedIn(t *testing.T) {
	b, rb, _ := autoBox(t)
	b.write("bk/done-download", "")
	out, code := b.run(rb, "minimal")
	if code != 0 || !b.exists("bk/done-minimal") || !b.exists("bk/begun-upgrade") || strings.Contains(out, "point of no return. Type") {
		t.Fatalf("exit %d, %q", code, out)
	}
}

// ---- sync, and Docker ------------------------------------------------------------------------------------------------------------------------------

// The marker of the point of no return must survive a power cut: sync runs after it is written, and before the first command that changes a package.
func TestAutoBlock5SyncsAfterItsMarkerAndBeforeApt(t *testing.T) {
	b, rb, _ := autoBox(t)
	b.write("bk/done-download", "")
	if out, code := b.run(rb, "minimal"); code != 0 {
		t.Fatalf("exit %d, %q", code, out)
	}
	calls := b.read("fake/calls.log")
	i, j := strings.Index(calls, "sync begun-upgrade=yes\n"), strings.Index(calls, " upgrade\n")
	if i < 0 || j < 0 || i > j || strings.Count(calls, "sync ") != 1 {
		t.Errorf("sync is not between the marker and apt-get upgrade: %q", calls)
	}
}

// When Docker's repository cannot be asked about the new release the unit goes on: the block ends, the source stays paused, the by-hand commands are printed, and the note says why,
// for the judge to turn into a finding.
func TestAutoBlock8WritesWhyDockersSourceStaysPaused(t *testing.T) {
	_, rb, _ := autoBox(t)
	noCurl := withAll(t, rb, "command -v curl ", "command -v curl-which-is-not-there ")
	for _, tc := range []struct {
		name string
		rb   *Runbook
		curl string
		note string
	}{
		{"Docker has nothing for the release (404)", rb, "echo 404; exit 22", "not_published"},
		{"Docker has nothing for the release (403)", rb, "echo 403; exit 22", "not_published"},
		{"Docker has nothing for the release (410)", rb, "echo 410; exit 22", "not_published"},
		{"Docker is busy (429)", rb, "echo 429; exit 22", "unreachable"},
		{"Docker is down (503)", rb, "echo 503; exit 22", "unreachable"},
		{"no name (6)", rb, "exit 6", "unreachable"},
		{"no connection (7)", rb, "exit 7", "unreachable"},
		{"too slow (28)", rb, "exit 28", "unreachable"},
		{"curl is not there", noCurl, "exit 0", "no_curl"},
	} {
		b := newShellBox(t)
		b.auto = true
		setupBlock8(b)
		b.fake("curl", tc.curl)
		out, code := b.run(tc.rb, "after")
		if code != 0 || !b.exists("bk/done-after") || strings.Contains(out, "STOP:") || b.read("bk/docker-source-note") != tc.note+"\n" {
			t.Errorf("%s: exit %d, note %q, %q", tc.name, code, b.read("bk/docker-source-note"), out)
		}
		if b.exists("etc-apt/sources.list.d/docker.list") || !b.exists("etc-apt/sources.list.d/docker.list.casaos-paused") || !strings.Contains(out, "To put Docker's source back by hand, when it is published: mv /etc/apt/sources.list.d/docker.list.casaos-paused") {
			t.Errorf("%s: the source of Docker moved, or the commands to put it back are not said: %q", tc.name, out)
		}
		if got := stopClassOf(b); got != "" {
			t.Errorf("%s: the block ended with 0 and left the class %q", tc.name, got)
		}
		// the timers were started, and the state of them is gone: the block ran to its end
		if b.exists("timers.state") || !strings.Contains(b.read("fake/calls.log"), "systemctl start apt-daily.timer") {
			t.Errorf("%s: the block did not run to its end", tc.name)
		}
	}
	// Docker answers: the source is put back, and there is no note
	b := newShellBox(t)
	b.auto = true
	setupBlock8(b)
	if out, code := b.run(rb, "after"); code != 0 || b.exists("bk/docker-source-note") || !b.exists("etc-apt/sources.list.d/docker.list") {
		t.Errorf("Docker publishes the release: exit %d, note %v, %q", code, b.exists("bk/docker-source-note"), out)
	}
}

// A note that an earlier run of block 8 left about Docker's source is taken away when the block begins, and the branch that finds Docker answering writes none: the note is about
// this run of the block, and the judge reads it as the machine's own.
func TestAutoBlock8TakesAwayTheNoteOfAnEarlierRun(t *testing.T) {
	_, rb, _ := autoBox(t)
	b := newShellBox(t)
	b.auto = true
	setupBlock8(b)
	b.write("bk/docker-source-note", "unreachable\n")
	if out, code := b.run(rb, "after"); code != 0 || b.exists("bk/docker-source-note") || !b.exists("etc-apt/sources.list.d/docker.list") {
		t.Errorf("Docker publishes the release: exit %d, note %q, %q", code, b.read("bk/docker-source-note"), out)
	}
	// and it is taken away at the start of the block, so that a block that stops before it gets to Docker leaves none either
	b = newShellBox(t)
	b.auto = true
	setupBlock8(b)
	b.write("bk/docker-source-note", "unreachable\n")
	b.write("os-release", "VERSION_CODENAME=bullseye\n")
	if out, code := b.run(rb, "after"); code == 0 || b.exists("bk/docker-source-note") {
		t.Errorf("a block that stops on a machine that is not Debian 12: exit %d, note %q, %q", code, b.read("bk/docker-source-note"), out)
	}
}

// What block 7 warns of is written, as a word on a line, in the file that the core reads to show it beside the restart and to say it after: the initrd that is under 5 MiB, and the
// boot loader that cannot be looked at. A machine that has nothing to warn of leaves no file.
func TestAutoBlock7WritesTheWordOfEachWarningItPrints(t *testing.T) {
	b, rb, _ := autoBox(t)
	b.write("bk/done-full", "")
	b.write("bk/packages.tsv", "ii \tlinux-image-5.10.0-46-cloud-amd64\t5.10.209-2\n")
	b.write("bk/sshd-keys.txt", "")
	b.write("bk/iptables-alternative.txt", "")
	b.write("bootid-now", "boot-A\n")
	b.write("fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\nii  linux-image-6.1.0-53-cloud-amd64\n")
	b.write("fake/pkg-systemd-timesyncd", "ii ")
	b.write("boot/vmlinuz-6.1.0-53-cloud-amd64", "k")
	b.write("boot/vmlinuz-5.10.0-46-cloud-amd64", "k")
	b.sized("boot/initrd.img-6.1.0-53-cloud-amd64", 2<<20)
	os.Remove(filepath.Join(b.dir, "boot", "grub", "grub.cfg"))
	out, code := runBlock7(b, rb)
	if code != 0 || !b.exists("bk/done-prereboot") || b.read("bk/warnings") != "initrd_small\ngrub_cfg_missing\n" {
		t.Fatalf("both warnings: exit %d, warnings %q, %q", code, b.read("bk/warnings"), out)
	}
	// the words are the ones that the core and the judge know
	if WarningInitrdSmall != "initrd_small" || WarningNoGrubConfig != "grub_cfg_missing" {
		t.Error("the words of the warnings are not the ones of the files")
	}
	// the initrd is a usual one, and the boot loader names the kernel: nothing to warn of
	b.sized("boot/initrd.img-6.1.0-53-cloud-amd64", initrdOK)
	b.write("boot/grub/grub.cfg", "menuentry { linux /boot/vmlinuz-6.1.0-53-cloud-amd64 }\n")
	os.Remove(filepath.Join(b.dir, "bk", "done-prereboot"))
	os.Remove(filepath.Join(b.dir, "bk", "warnings"))
	if out, code := runBlock7(b, rb); code != 0 || b.exists("bk/warnings") {
		t.Errorf("no warning: exit %d, warnings %q, %q", code, b.read("bk/warnings"), out)
	}
}

// ---- block F ---------------------------------------------------------------------------------------------------------------------------------------

func fBox(t *testing.T) (*shellBox, *AutoRunbook) {
	t.Helper()
	b, _, ar := autoBox(t)
	b.write("bk/begun-upgrade", "")
	b.write("bk/done-snapshot", "")
	return b, ar
}

// F is the table of the owner's runbook as code: the five units that apt's timers start are stopped, dpkg configures what is half done, the repair is simulated and read, and only
// then run.
func TestBlockFMendsInTheOrderOfTheTableAndWritesItsSimulationForTheAttempt(t *testing.T) {
	b, ar := fBox(t)
	b.write("fake/active-apt-daily.timer", "")
	b.write("fake/active-apt-daily.service", "")
	b.write("fake/active-unattended-upgrades.service", "")
	out, code := runForward(b, ar)
	if code != 0 || !strings.Contains(out, "Block F done.") {
		t.Fatalf("exit %d, %q", code, out)
	}
	calls := b.read("fake/calls.log")
	order := []string{
		"systemctl stop apt-daily.timer\n", "systemctl stop apt-daily.service\n", "systemctl stop unattended-upgrades.service\n",
		"dpkg --configure -a --force-confdef --force-confold\n", "-s -f install\n", "--force-confold -f install\n",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(calls[at:], want)
		if i < 0 {
			t.Fatalf("%q is not in the calls, or not after the one before it:\n%s", want, calls)
		}
		at += i + len(want)
	}
	for _, unit := range []string{"apt-daily.timer", "apt-daily-upgrade.timer", "apt-daily.service", "apt-daily-upgrade.service", "unattended-upgrades.service"} {
		if !strings.Contains(calls, "systemctl is-active "+unit+"\n") {
			t.Errorf("block F did not look at %s", unit)
		}
	}
	if strings.Contains(calls, "systemctl stop apt-daily-upgrade") {
		t.Errorf("block F stopped a unit that was not running: %q", calls)
	}
	// the simulation is the attempt's, and no marker is left: F is never skipped
	if !b.exists("bk/recover-1.txt") || b.exists("bk/done-forward") || b.exists("run/stop-class") {
		t.Errorf("recover-1.txt %v, done-forward %v, class %q", b.exists("bk/recover-1.txt"), b.exists("bk/done-forward"), b.read("run/stop-class"))
	}
	// the list of the removals that the plan expected is there, empty, for a folder that has none (a refresh writes none)
	if !b.exists("bk/expected-removed.txt") || b.read("bk/expected-removed.txt") != "" {
		t.Errorf("expected-removed.txt: %q", b.read("bk/expected-removed.txt"))
	}
	// the number of the attempt is in the name of the file
	b2, ar2 := fBox(t)
	b2.env = []string{"CASAOS_OSU_ATTEMPT=3"}
	if out, code := runForward(b2, ar2); code != 0 || !b2.exists("bk/recover-3.txt") || b2.exists("bk/recover-1.txt") {
		t.Errorf("attempt 3: exit %d, %q", code, out)
	}
}

// The first step of the table, dpkg --configure -a, that fails is not the end of it: a package whose dependency is not unpacked yet cannot be configured, and the repair that follows
// is what unpacks it. The block says what dpkg could not do, simulates the repair, reads it, runs it, and is done when dpkg is quiet afterwards.
func TestBlockFGoesOnWhenDpkgCannotConfigureAndTheRepairMendsIt(t *testing.T) {
	b, ar := fBox(t)
	b.write("fake/dpkg-fails", "")
	out, code := runForward(b, ar)
	if code != 0 || !strings.Contains(out, "dpkg could not configure everything that is half done (it ended with 1;") || !strings.Contains(out, "Block F done.") {
		t.Fatalf("exit %d, %q", code, out)
	}
	calls := b.read("fake/calls.log")
	configure, simulate, repair := strings.Index(calls, "dpkg --configure -a"), strings.Index(calls, "-s -f install"), strings.Index(calls, "--force-confold -f install\n")
	if configure < 0 || simulate < configure || repair < simulate {
		t.Errorf("the table's steps did not run in their order: configure %d, simulate %d, repair %d:\n%s", configure, simulate, repair, calls)
	}
	if got := stopClassOf(b); got != "" {
		t.Errorf("a stop class %q was written for a block that ended well", got)
	}
}

func TestBlockFStopsWhereTheTableWouldNotGoOn(t *testing.T) {
	protected := resimOf("fuse", "samba-libs")
	for _, tc := range []struct {
		name  string
		set   func(b *shellBox)
		says  string
		class string
		calls string // a command that must not have run
	}{
		{"a timer that stays up", func(b *shellBox) { b.fake("systemctl", `case "$1" in is-active) echo active;; esac; exit 0`) }, "STOP: apt-daily.timer is still running", "busy", "dpkg"},
		{"a package manager is at work", func(b *shellBox) { b.write("fake/ps.txt", psTable("apt-get         apt-get -y upgrade")) }, "STOP: a package manager is running", "busy", "dpkg"},
		{"dpkg cannot configure and the repair fails too", func(b *shellBox) { b.write("fake/dpkg-fails", ""); b.write("fake/fix-fails", "") }, "STOP: dpkg could not configure what is half done, and apt could not repair it either", "dpkg_broken", ""},
		{"dpkg is not quiet after the repair", func(b *shellBox) { b.write("fake/audit.txt", "The following packages are only half configured: foo\n") }, "STOP: dpkg is not quiet after the repair", "dpkg_broken", ""},
		{"the repair cannot be worked out", func(b *shellBox) { b.write("fake/fixsim-fails", "") }, "STOP: apt cannot work out the repair", "apt_failed", "--force-confold -f install"},
		{"the simulation has no summary", func(b *shellBox) { b.write("fake/fixsim.txt", "Reading package lists...\n") }, "STOP: apt printed no summary line", "apt_failed", "--force-confold -f install"},
		{"the names and the summary disagree", func(b *shellBox) {
			b.write("fake/fixsim.txt", "1 upgraded, 0 newly installed, 1 to remove and 0 not upgraded.\n")
		}, "not as many as its summary says", "apt_failed", "--force-confold -f install"},
		{"the repair would downgrade", func(b *shellBox) {
			b.write("fake/fixsim.txt", "The following packages will be DOWNGRADED:\n  docker-ce\n0 upgraded, 0 newly installed, 1 downgraded, 0 to remove and 0 not upgraded.\n")
		}, "STOP: apt would downgrade packages", "plan", "--force-confold -f install"},
		{"the repair would remove a protected package", func(b *shellBox) {
			b.write("fake/fixsim.txt", protected)
			b.write("bk/expected-removed.txt", "fuse\nsamba-libs\n")
		}, "STOP: the repair would remove samba-libs, which is protected", "guard", "--force-confold -f install"},
		{"the repair would remove an essential package", func(b *shellBox) {
			b.write("fake/fixsim.txt", resimOf("an-essential"))
			b.write("bk/expected-removed.txt", "an-essential\n")
			b.fake("dpkg-query", "echo 'yes required'")
		}, "which is essential or required", "guard", "--force-confold -f install"},
		// the table says to read the repair, and the engine reads it for the owner: whatever the plan does not name goes unrepaired
		{"the repair would remove a package that the plan does not name", func(b *shellBox) {
			b.write("fake/fixsim.txt", resimOf("cron"))
			b.write("bk/expected-removed.txt", "fuse\nlibsemanage1\n")
		}, "STOP: the repair would remove cron, which the plan does not name: do not undo anything, ask for help", "guard", "--force-confold -f install"},
		{"a folder with no list of the removals that the plan expected", func(b *shellBox) { b.write("fake/fixsim.txt", resimOf("cron")) }, "STOP: the repair would remove cron, which the plan does not name", "guard", "--force-confold -f install"},
		{"the name of a removal that is a part of another name is no match", func(b *shellBox) {
			b.write("fake/fixsim.txt", resimOf("fuse"))
			b.write("bk/expected-removed.txt", "fuse2\nlibfuse\n")
		}, "STOP: the repair would remove fuse, which the plan does not name", "guard", "--force-confold -f install"},
		{"the repair fails", func(b *shellBox) { b.write("fake/fix-fails", "") }, "STOP: apt could not repair the packages", "apt_failed", ""},
		{"the hop is over", func(b *shellBox) { b.write("bk/done-after", "") }, "STOP: the hop is over", "identity", "dpkg"},
	} {
		b, ar := fBox(t)
		tc.set(b)
		out, code := runForward(b, ar)
		if code == 0 || !strings.Contains(out, tc.says) || strings.Contains(out, "Block F done.") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
			continue
		}
		if got := stopClassOf(b); got != tc.class {
			t.Errorf("%s: class %q, want %q", tc.name, got, tc.class)
		}
		if calls := b.read("fake/calls.log"); tc.calls != "" && strings.Contains(calls, tc.calls) {
			t.Errorf("%s: %q ran after the stop:\n%s", tc.name, tc.calls, calls)
		}
	}
	// what the plan names may be removed by the repair, and nothing is removed that it does not
	b, ar := fBox(t)
	b.write("fake/fixsim.txt", resimOf("fuse", "libsemanage1"))
	b.write("bk/expected-removed.txt", "fuse\nlibsemanage1\n")
	if out, code := runForward(b, ar); code != 0 || !strings.Contains(b.read("fake/calls.log"), "--force-confold -f install\n") {
		t.Errorf("a repair that removes what the plan names: exit %d, %q", code, out)
	}
	// the one that is not in the list after a name that is: apt would remove both, and the second is not the plan's
	b, ar = fBox(t)
	b.write("fake/fixsim.txt", resimOf("fuse", "cron"))
	b.write("bk/expected-removed.txt", "fuse\n")
	if out, code := runForward(b, ar); code == 0 || !strings.Contains(out, "STOP: the repair would remove cron, which the plan does not name") {
		t.Errorf("a repair that removes a package of the plan and one that is not: exit %d, %q", code, out)
	}
}

// F is stricter than the guard of block 6: a refresh's folder has no expected-removed.txt, so a repair that removes anything is refused, and the file that F makes is empty and stays so.
func TestBlockFNeverWritesTheListOfTheRemovalsThatItWouldAllow(t *testing.T) {
	b, ar := fBox(t)
	b.write("fake/fixsim.txt", resimOf("cron"))
	if out, code := runForward(b, ar); code == 0 || !strings.Contains(out, "which the plan does not name") {
		t.Fatalf("exit %d, %q", code, out)
	}
	if got := b.read("bk/expected-removed.txt"); got != "" {
		t.Errorf("expected-removed.txt = %q", got)
	}
}

// The unit's text is run on a machine that has Windows paths: nothing of the tests in this file needs a shell that Windows has not.
var _ = runtime.GOOS
