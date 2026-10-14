package osplan

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The states of the hop: a block run twice, a restart in the middle, a hop that ended, the machine as it is when the block begins (room in /boot, the disk of grub-pc,
// a package manager at work). The blocks are run as the others are (runbook_exec_test.go): the text of the block, the paths of the machine pointed at a directory of the
// test, and small commands in place of the machine's.

// ---- block 0: what the readiness report refuses and the hop would break on --------------------------------------------------

func TestBlock0LooksAtWhatTheHopWouldBreakOnAfterThePointOfNoReturn(t *testing.T) {
	_, rb := realRunbook(t)
	good := typed(rb, "TYPED_HOSTNAME", "testbox")
	efi := func(b *shellBox, mounts string) {
		if err := os.MkdirAll(filepath.Join(b.dir, "sys-efi"), 0o755); err != nil {
			t.Fatal(err)
		}
		b.write("mounts", mounts)
	}
	const espMounted = "/dev/sda1 / ext4 rw 0 0\n/dev/sda15 /boot/efi vfat rw 0 0\n"
	for _, tc := range []struct {
		name  string
		set   func(b *shellBox)
		stops string
	}{
		{"/boot has one KiB too little", func(b *shellBox) { b.write("fake/avail_boot", "307199\n") }, "/boot has less than 300 MiB free"},
		{"/boot is full", func(b *shellBox) { b.write("fake/avail_boot", "0\n") }, "/boot has less than 300 MiB free"},
		{"UEFI and the EFI system partition is not mounted", func(b *shellBox) { efi(b, "/dev/sda1 / ext4 rw 0 0\n") }, "the EFI system partition is not mounted on"},
		{"UEFI and something else is mounted under /boot/efi2", func(b *shellBox) { efi(b, "/dev/sda1 / ext4 rw 0 0\n/dev/sda15 /boot/efi2 vfat rw 0 0\n") }, "the EFI system partition is not mounted on"},
		{"UEFI and the partition has one KiB too little", func(b *shellBox) { efi(b, espMounted); b.write("fake/avail_boot_efi", "16383\n") }, "/boot/efi has less than 16 MiB free"},
		{"BIOS, grub-pc and no disk to write to", func(b *shellBox) { b.write("fake/pkg-grub-pc", "ii ") }, "grub-pc has no disk to be installed on"},
		{"BIOS, grub-pc and only commas in the question", func(b *shellBox) { b.write("fake/pkg-grub-pc", "ii "); b.write("fake/grub-devices", " , \n") }, "grub-pc has no disk to be installed on"},
		{"BIOS, grub-pc, no disk and the empty question is false", func(b *shellBox) {
			b.write("fake/pkg-grub-pc", "ii ")
			b.write("fake/grub-empty", "false\n")
		}, "dpkg-reconfigure grub-pc"},
	} {
		b := newShellBox(t)
		setupVerify(t, b)
		tc.set(b)
		out, code := b.run(good, "verify")
		if code == 0 || !strings.Contains(out, "STOP:") || !strings.Contains(out, tc.stops) || b.exists("bk/done-verify") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
	}
	for _, tc := range []struct {
		name string
		set  func(b *shellBox)
	}{
		{"/boot has exactly 300 MiB", func(b *shellBox) { b.write("fake/avail_boot", "307200\n") }},
		{"UEFI, the partition is mounted and has exactly 16 MiB", func(b *shellBox) { efi(b, espMounted); b.write("fake/avail_boot_efi", "16384\n") }},
		// an EFI machine is looked at by its partition, and not by the disk of grub-pc (the report does the same)
		{"UEFI with grub-pc installed and no disk set", func(b *shellBox) { efi(b, espMounted); b.write("fake/pkg-grub-pc", "ii ") }},
		{"BIOS, grub-pc and a disk", func(b *shellBox) { b.write("fake/pkg-grub-pc", "ii "); b.write("fake/grub-devices", "/dev/sda\n") }},
		{"BIOS, grub-pc and two disks", func(b *shellBox) {
			b.write("fake/pkg-grub-pc", "ii ")
			b.write("fake/grub-devices", "/dev/sda, /dev/sdb\n")
		}},
		{"BIOS, grub-pc and no disk on purpose", func(b *shellBox) { b.write("fake/pkg-grub-pc", "ii "); b.write("fake/grub-empty", "true\n") }},
		{"BIOS and no grub-pc (a cloud image)", func(b *shellBox) {}},
		{"BIOS and grub-pc removed (configuration left)", func(b *shellBox) { b.write("fake/pkg-grub-pc", "rc ") }},
	} {
		b := newShellBox(t)
		setupVerify(t, b)
		tc.set(b)
		out, code := b.run(good, "verify")
		if code != 0 || !b.exists("bk/done-verify") || strings.Contains(out, "STOP:") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
	}
}

// Modules built from source are a warning, which is not a stop: the block ends, and says so above its end.
func TestBlock0WarnsOfModulesBuiltFromSourceAndGoesOn(t *testing.T) {
	_, rb := realRunbook(t)
	good := typed(rb, "TYPED_HOSTNAME", "testbox")
	b := newShellBox(t)
	setupVerify(t, b)
	b.write("fake/dkms.txt", "zfs/2.0.3, 5.10.0-46-cloud-amd64, x86_64: installed\n")
	out, code := b.run(good, "verify")
	i, j := strings.Index(out, "WARNING: dkms has modules built from source"), strings.Index(out, "Block 0 done.")
	if code != 0 || !b.exists("bk/done-verify") || i < 0 || j < i || !strings.Contains(out, "This is NOT a stop") {
		t.Errorf("dkms has a module: exit %d, %q", code, out)
	}
	b = newShellBox(t)
	setupVerify(t, b)
	if out, code := b.run(good, "verify"); code != 0 || strings.Contains(out, "WARNING") {
		t.Errorf("dkms has nothing: exit %d, %q", code, out)
	}
	// no dkms on the box at all
	b = newShellBox(t)
	setupVerify(t, b)
	if out, code := b.run(withAll(t, good, "command -v dkms ", "command -v dkms-which-is-not-there "), "verify"); code != 0 || strings.Contains(out, "WARNING") {
		t.Errorf("no dkms: exit %d, %q", code, out)
	}
}

// Block 0 does not wait for the lock of dpkg without saying so, and does not take a held lock for a broken dependency: the check goes through aptq, which waits up to 5 minutes.
func TestBlock0AsksAptGetCheckWithTheWaitForTheLockOfDpkg(t *testing.T) {
	_, rb := realRunbook(t)
	b := newShellBox(t)
	setupVerify(t, b)
	if out, code := b.run(typed(rb, "TYPED_HOSTNAME", "testbox"), "verify"); code != 0 {
		t.Fatalf("exit %d, %q", code, out)
	}
	if calls := b.read("fake/calls.log"); !strings.Contains(calls, "DPkg::Lock::Timeout=300") || !strings.Contains(calls, " check\n") || strings.Contains(calls, "apt-get check\n") {
		t.Errorf("apt-get check does not wait for the lock: %q", calls)
	}
	// and the stop says what it may be
	b = newShellBox(t)
	setupVerify(t, b)
	b.write("fake/check-fails", "")
	out, code := b.run(typed(rb, "TYPED_HOSTNAME", "testbox"), "verify")
	if code == 0 || !strings.Contains(out, "could not get the lock of dpkg in 5 minutes") {
		t.Errorf("a check that fails: exit %d, %q", code, out)
	}
}

// ---- no block runs on a machine that has gone through the hop ------------------------------------------------------------------

func TestNoBlockRunsOnceTheHopHasEnded(t *testing.T) {
	_, rb := realRunbook(t)
	every := typed(typed(typed(rb, "TYPED_HOSTNAME", "testbox"), "ACCEPT_REMOVALS", "fuse"), "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON")
	for _, blk := range rb.Blocks {
		b := newShellBox(t)
		setupVerify(t, b)
		b.write("bk/done-after", "")
		// every block before this one has ended, so that the only thing that stops it is that the hop is over
		for _, id := range []string{"verify", "timers", "snapshot", "sources", "download", "minimal", "full", "prereboot"} {
			b.write("bk/done-"+id, "")
		}
		out, code := b.run(every, blk.ID)
		if code == 0 || !strings.Contains(out, "STOP: the hop is over (block 8 ended: done-after is in ./bk)") || (strings.Contains(out, "Block ") && strings.Contains(out, " done.")) {
			t.Errorf("block %s: exit %d, %q", blk.ID, code, out)
		}
		if b.exists("fake/calls.log") || b.exists("timers.state") {
			t.Errorf("block %s ran a command on a machine that went through the hop: %q", blk.ID, b.read("fake/calls.log"))
		}
	}
}

// A second plan of the machine has another id and another folder. When the first took the machine to its end (its block 8 ended), a block of the second, pasted from an older
// page, must run nothing: block 1 would stop the timers of a finished machine and write their state again, and nothing would ever start them. It stops whatever the machine says
// of itself, and names the folder that finished the hop.
func TestNoBlockRunsOnAMachineThatAnotherPlanTookToItsEnd(t *testing.T) {
	_, rb := realRunbook(t)
	every := typed(typed(typed(rb, "TYPED_HOSTNAME", "testbox"), "ACCEPT_REMOVALS", "fuse"), "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON")
	const other = "others/casaos-os-upgrade-p1-0123456789ab"
	for _, release := range []string{"bookworm", "bullseye"} {
		for _, blk := range rb.Blocks {
			b := newShellBox(t)
			setupVerify(t, b)
			b.write("os-release", "VERSION_CODENAME="+release+"\n")
			b.write(other+"/done-after", "")
			// every block before this one has ended, so that the only thing that stops it is that another plan finished the hop
			for _, id := range []string{"verify", "timers", "snapshot", "sources", "download", "minimal", "full", "prereboot"} {
				b.write("bk/done-"+id, "")
			}
			out, code := b.run(every, blk.ID)
			if code == 0 || !strings.Contains(out, "STOP: the hop is over for this machine: the plan whose folder is ./"+other+" took it to the end") || (strings.Contains(out, "Block ") && strings.Contains(out, " done.")) {
				t.Errorf("a machine that says %s, block %s: exit %d, %q", release, blk.ID, code, out)
			}
			if b.exists("fake/calls.log") || b.exists("timers.state") || b.exists("bk/done-after") {
				t.Errorf("a machine that says %s, block %s ran something on a machine that another plan took to its end: %q", release, blk.ID, b.read("fake/calls.log"))
			}
		}
	}
	// the folder of a plan that did not end, and what is no folder of a plan, are no reason: this block goes on to its own checks
	for name, set := range map[string]func(b *shellBox){
		"a plan that began the upgrade and did not end": func(b *shellBox) { b.write(other+"/begun-upgrade", "") },
		"the file of the state of the timers":           func(b *shellBox) { b.write("others/casaos-os-upgrade-timers.state", "apt-daily.timer active\n") },
		"a file called done-after, not in a folder":     func(b *shellBox) { b.write("others/casaos-os-upgrade-done-after", "") },
	} {
		b := newShellBox(t)
		setupVerify(t, b)
		b.write("bk/done-verify", "")
		set(b)
		if out, code := b.run(every, "timers"); code != 0 || strings.Contains(out, "the hop is over") {
			t.Errorf("%s: exit %d, %q", name, code, out)
		}
	}
}

// A machine that is not Debian 11 any more is no machine for the blocks of a plan that has not begun the upgrade of its packages: another plan, or the owner by hand, took it to
// Debian 12, and block 1 would stop timers that nothing starts again. From the moment this plan has begun the upgrade (block 5 writes its marker before it runs apt) the machine
// turns Debian 12 as base-files is upgraded, and the blocks that go on from there must run: the marker of the plan is what tells the two apart. Block 0 has its own words for it.
func TestABlockStopsOnAMachineThatIsNotDebian11UnlessItsPlanHasBegunTheUpgrade(t *testing.T) {
	_, rb := realRunbook(t)
	every := typed(typed(rb, "ACCEPT_REMOVALS", "fuse"), "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON")
	chain := []string{"verify", "timers", "snapshot", "sources", "download", "minimal", "full", "prereboot"}
	box := func(upTo int, release string, markers ...string) *shellBox {
		b := newShellBox(t)
		setupVerify(t, b)
		b.write("os-release", "VERSION_CODENAME="+release+"\n")
		for _, id := range chain[:upTo] {
			b.write("bk/done-"+id, "")
		}
		for _, m := range markers {
			b.write("bk/"+m, "")
		}
		return b
	}
	// blocks 1 to 5: the blocks after them need block 5 to have ended (done-minimal), which says that this plan began the upgrade
	for i, blk := range rb.Blocks[1:6] {
		b := box(i+1, "bookworm")
		out, code := b.run(every, blk.ID)
		if code == 0 || !strings.Contains(out, "STOP: the hop is over for this machine: it is not Debian 11 (bullseye) any more, and this plan did not begin the upgrade of its packages") {
			t.Errorf("block %s on Debian 12, nothing begun: exit %d, %q", blk.ID, code, out)
		}
		if b.exists("fake/calls.log") || b.exists("timers.state") {
			t.Errorf("block %s ran a command: %q", blk.ID, b.read("fake/calls.log"))
		}
	}
	for _, marker := range []string{"begun-upgrade", "done-minimal"} {
		for i, blk := range rb.Blocks {
			if blk.ID != "minimal" && blk.ID != "full" && blk.ID != "prereboot" && blk.ID != "after" {
				continue
			}
			out, _ := box(i, "bookworm", marker).run(every, blk.ID)
			if strings.Contains(out, "the hop is over") {
				t.Errorf("block %s of a plan that wrote %s, on Debian 12: it stops as if the hop were over: %q", blk.ID, marker, out)
			}
		}
	}
	// Debian 11 is where the blocks before the upgrade run
	b := box(1, "bullseye")
	if out, code := b.run(every, "timers"); code != 0 || strings.Contains(out, "the hop is over") {
		t.Errorf("block 1 on Debian 11: exit %d, %q", code, out)
	}
	// a machine whose os-release cannot be read is not Debian 11 either
	b = box(1, "bullseye")
	os.Remove(filepath.Join(b.dir, "os-release"))
	if out, code := b.run(every, "timers"); code == 0 || !strings.Contains(out, "STOP: the hop is over for this machine") {
		t.Errorf("block 1 with no os-release: exit %d, %q", code, out)
	}
}

// When dpkg was killed in the middle of its work (a file is left in /var/lib/dpkg/updates) or a dependency is missing, apt ends its first command of block 6 (and its check at the end) with
// dpkg was interrupted, or Unmet dependencies: a lock and the network have nothing to do with it, and the block run again ends the same way until the owner goes forward by hand, since
// the repair ladder sits behind the dist-upgrade that cannot start. The two stops that apt's failure reaches before the ladder say so, with the commands of the table, and warn off the
// repair that apt itself suggests.
func TestBlock6SaysThatAnInterruptedDpkgOrAnUnmetDependencyIsNeitherTheLockNorTheNetwork(t *testing.T) {
	_, rb := realRunbook(t)
	const tail = "do not run what apt suggests (apt --fix-broken install removes packages without a stop). Only forward: " + forwardCommands + "   then run this block again."
	for _, tc := range []struct {
		name  string
		set   func(b *shellBox)
		stops []string
	}{
		{"the download fails", func(b *shellBox) { b.write("fake/download-fails", "") }, []string{
			"STOP: the download of the full upgrade did not go through: a lock held by another package manager, or the network",
			"A third cause: if apt printed dpkg was interrupted, or Unmet dependencies (dpkg was killed in the middle of its work, or a dependency is missing), running this block again changes nothing: " + tail}},
		{"the check at the end fails", func(b *shellBox) { b.write("fake/check-fails", "") }, []string{
			"STOP: apt-get check finds a broken dependency, or could not get the lock of dpkg in 5 minutes, or dpkg was interrupted",
			"If it printed dpkg was interrupted, or Unmet dependencies: " + tail}},
	} {
		b := newShellBox(t)
		b.write("bk/done-minimal", "")
		tc.set(b)
		out, code := b.run(rb, "full")
		if code == 0 || b.exists("bk/done-full") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
			continue
		}
		for _, want := range tc.stops {
			if !strings.Contains(out, want) {
				t.Errorf("%s: the stop does not say %q: %q", tc.name, want, out)
			}
		}
	}
	// the other stops of the block do not say it: they come after a command that ran, and have their own words
	for _, tc := range []struct {
		name string
		set  func(b *shellBox)
	}{
		{"a catch-up pass fails", func(b *shellBox) {
			b.write("fake/sim1.txt", "Reading package lists...\n12 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n")
			b.write("fake/dist-fails-2", "")
		}},
		{"dpkg cannot configure", func(b *shellBox) { b.write("fake/dist-fails-1", ""); b.write("fake/dpkg-fails", "") }},
	} {
		b := newShellBox(t)
		b.write("bk/done-minimal", "")
		tc.set(b)
		if out, code := b.run(rb, "full"); code == 0 || strings.Contains(out, "apt --fix-broken install") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
	}
	// the prose of the block does not say that it ends the same way after a restart or a dead terminal, whatever happened
	if !suiteAuto && !strings.Contains(rb.Text, "apt stops at its first command here (it is neither a lock nor the network), and the STOP says to go forward by hand with the commands of the table") {
		t.Error("the prose of block 6 says nothing of an interrupted dpkg")
	}
}

// ---- block 3 pasted again --------------------------------------------------------------------------------------------------------

// Once the sources of apt are Debian 12's their hash is not the plan's any more, and a plan cannot be made on them (sources_target): a block 3 that is pasted again, at
// whatever point the first run left it, is told what to do with the restore script, or that it ended, and never to ask for a new plan.
func TestBlock3PastedAgainOnTheSourcesItWroteSendsTheOwnerToTheRestoreScript(t *testing.T) {
	p, rb := realRunbook(t)
	good := typed(rb, "ACCEPT_REMOVALS", "fuse")
	setup := func(b *shellBox) {
		b.write("bk/done-snapshot", "")
		setupVerify(t, b)
		b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
	}
	notADeadEnd := func(name, out string) {
		t.Helper()
		for _, bad := range []string{"Ask for a new plan", "ask for a new plan", "not the ones the plan was made from", "something changed them", "older than its 24 hours"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s: the stop sends the owner to a dead end (%q): %q", name, bad, out)
			}
		}
	}
	// the first run ends; pasted again it says so, whether the removals are accepted again or not, and the plan having expired in between changes nothing
	b := newShellBox(t)
	setup(b)
	if out, code := b.run(good, "sources"); code != 0 || !b.exists("bk/done-sources") {
		t.Fatalf("the first run: exit %d, %q", code, out)
	}
	for _, tc := range []struct {
		name string
		rb   *Runbook
		now  int64
	}{
		{"pasted again", good, 0},
		{"pasted again without the names of the removals", rb, 0},
		{"pasted again after the 24 hours", good, p.ExpiresAt.Unix() + 3600},
	} {
		if tc.now != 0 {
			b.write("fake/now", strconv.FormatInt(tc.now, 10)+"\n")
		}
		out, code := b.run(tc.rb, "sources")
		if code == 0 || !strings.Contains(out, "STOP: block 3 ended: the next is block 4") || !strings.Contains(out, "sh ./bk/restore-sources.sh") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
		notADeadEnd(tc.name, out)
	}
	// a first run that stopped after it had moved the sources (the update, the simulation, the comparison of the removals): apt-orig is there, done-sources is not
	for _, tc := range []struct {
		name string
		set  func(b *shellBox)
	}{
		{"apt-get update failed", func(b *shellBox) { b.write("fake/update-fails", "") }},
		{"the removals are not the plan's", func(b *shellBox) { b.write("fake/sim.txt", resimOf("fuse", "libsemanage1", "samba")) }},
		{"packages are kept back", func(b *shellBox) { b.write("fake/sim.txt", resimKept(2, true, "fuse", "libsemanage1")) }},
	} {
		b := newShellBox(t)
		setup(b)
		tc.set(b)
		if out, code := b.run(good, "sources"); code == 0 || b.exists("bk/done-sources") || !b.exists("bk/apt-orig") {
			t.Fatalf("%s, the first run: exit %d, %q", tc.name, code, out)
		}
		before := b.read("etc-apt/sources.list")
		if before != p.Sources.Tree[len(p.Sources.Tree)-1].Content {
			t.Fatalf("%s: the sources are not the ones block 3 wrote", tc.name)
		}
		os.Remove(filepath.Join(b.dir, "fake", "update-fails"))
		for _, now := range []int64{0, p.ExpiresAt.Unix() + 60} {
			if now != 0 {
				b.write("fake/now", strconv.FormatInt(now, 10)+"\n")
			}
			out, code := b.run(good, "sources")
			if code == 0 || !strings.Contains(out, "STOP: ./bk/apt-orig is there already: this block was run before") || !strings.Contains(out, "Run sh ./bk/restore-sources.sh") {
				t.Errorf("%s (now %d): exit %d, %q", tc.name, now, code, out)
			}
			notADeadEnd(tc.name, out)
			if b.read("etc-apt/sources.list") != before {
				t.Errorf("%s: the second run touched the sources", tc.name)
			}
		}
		// the restore script puts the sources back, and block 3 can then be run again
		os.Remove(filepath.Join(b.dir, "fake", "now"))
		b.write("fake/now", strconv.FormatInt(planMadeAt.Add(time.Hour).Unix(), 10)+"\n")
		out, rc := runSh(t, b.dir, "APT_ETC=./etc-apt CASAOS_NO_UPDATE=1 CASAOS_TIMERS_STATE=./timers.state sh ./bk/restore-sources.sh")
		if rc != 0 {
			t.Fatalf("%s: the restore script: exit %d, %q", tc.name, rc, out)
		}
		b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
		if out, code := b.run(good, "sources"); code != 0 || !b.exists("bk/done-sources") {
			t.Errorf("%s: block 3 after the restore: exit %d, %q", tc.name, code, out)
		}
	}
	// done-sources and no apt-orig (the owner took it away): it still ended, and the next is block 4
	b = newShellBox(t)
	setup(b)
	b.write("bk/done-sources", "")
	if out, code := b.run(good, "sources"); code == 0 || !strings.Contains(out, "STOP: block 3 ended: the next is block 4") || b.exists("bk/apt-orig") {
		t.Errorf("done-sources and no apt-orig: exit %d, %q", code, out)
	}
	// the stop of the upgrade that has begun is still the first
	b = newShellBox(t)
	setup(b)
	os.MkdirAll(filepath.Join(b.dir, "bk", "apt-orig"), 0o755)
	b.write("bk/begun-upgrade", "")
	if out, code := b.run(good, "sources"); code == 0 || !strings.Contains(out, "the upgrade has begun (block 5 was started)") || strings.Contains(out, "restore-sources.sh (it puts") {
		t.Errorf("the upgrade has begun: exit %d, %q", code, out)
	}
}

// ---- the busy check of blocks 3, 5 and 6 -----------------------------------------------------------------------------------------

func TestBlocks3And5And6StopWhileAPackageManagerIsAtWork(t *testing.T) {
	_, rb := realRunbook(t)
	every := typed(typed(rb, "ACCEPT_REMOVALS", "fuse"), "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON")
	for _, tc := range []struct{ id, prev string }{{"sources", "snapshot"}, {"minimal", "download"}, {"full", "minimal"}} {
		for _, busy := range []string{
			psTable("apt-get         /usr/bin/apt-get -y upgrade"),
			psTable("apt.systemd.dai /bin/sh /usr/lib/apt/apt.systemd.daily lock_is_held update"),
			psTable("dpkg            /usr/bin/dpkg --configure -a"),
		} {
			b := newShellBox(t)
			b.write("bk/done-"+tc.prev, "")
			setupVerify(t, b)
			if tc.id == "sources" {
				b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
			}
			b.write("fake/ps.txt", busy)
			out, code := b.run(every, tc.id)
			if code == 0 || !strings.Contains(out, "STOP: a package manager is running (the process list says: ") || !strings.Contains(out, "then run this block again.") || b.exists("bk/done-"+tc.id) {
				t.Errorf("block %s: exit %d, %q", tc.id, code, out)
			}
			// nothing was changed, and no apt was asked: the block 3 has no copy of /etc/apt, the block 5 no marker
			if b.exists("bk/apt-orig") || b.exists("bk/begun-upgrade") || b.exists("bk/restore-sources.sh") {
				t.Errorf("block %s changed something before it looked at the processes", tc.id)
			}
			if calls := b.read("fake/calls.log"); strings.Contains(calls, "apt-get") {
				t.Errorf("block %s ran apt: %q", tc.id, calls)
			}
		}
		// and it goes on when nothing is at work, the daemons that only look like package managers included
		b := newShellBox(t)
		b.write("bk/done-"+tc.prev, "")
		setupVerify(t, b)
		if tc.id == "sources" {
			b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
		}
		b.write("fake/ps.txt", psTable("unattended-upgr /usr/bin/python3 /usr/share/unattended-upgrades/unattended-upgrade-shutdown --wait-for-signal", "apt-cacher-ng   /usr/sbin/apt-cacher-ng"))
		if out, code := b.run(every, tc.id); code != 0 || !b.exists("bk/done-"+tc.id) {
			t.Errorf("block %s with nothing at work: exit %d, %q", tc.id, code, out)
		}
	}
	// a machine without ps is a stop in each of them, and not "nothing is running"
	for _, tc := range []struct{ id, prev string }{{"sources", "snapshot"}, {"minimal", "download"}, {"full", "minimal"}} {
		b := newShellBox(t)
		b.write("bk/done-"+tc.prev, "")
		setupVerify(t, b)
		b.fake("ps", "echo 'Error, do this: mount -t proc proc /proc' >&2; exit 1")
		if out, code := b.run(every, tc.id); code == 0 || !strings.Contains(out, "apt-get install procps") {
			t.Errorf("block %s without ps: exit %d, %q", tc.id, code, out)
		}
	}
}

// ---- block 5: the marker that must not lie ----------------------------------------------------------------------------------------

// begun-upgrade forbids the way back: it is written before apt-get upgrade, and taken back when this run wrote it and apt failed before it changed any package (dpkg's status
// is the same, and its journal is empty). When apt did something, or an earlier run had begun, it stays and the words go forward.
func TestBlock5TakesItsMarkerBackWhenAptFailedBeforeItChangedAnything(t *testing.T) {
	_, rb := realRunbook(t)
	ok := typed(rb, "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON")
	for _, tc := range []struct {
		name   string
		set    func(b *shellBox)
		marker bool
		says   []string
		not    []string
	}{
		{"apt failed at once (a lock, no room, a download)", func(b *shellBox) {}, false,
			[]string{"STOP: apt-get upgrade failed before it changed any package", "nothing was installed or removed", "the marker of the point of no return is taken back", "run this block again (type the confirmation again), or put the sources back: sh ./bk/restore-sources.sh"},
			[]string{"do not restore anything", "dpkg --configure"}},
		{"apt rewrote the status of dpkg before it failed", func(b *shellBox) { b.write("fake/upgrade-touches-status", "") }, true,
			[]string{"STOP: the minimal upgrade failed, and it has begun: do not restore anything", forwardCommands, "type the confirmation again and run this block again"}, []string{"taken back"}},
		{"apt was killed and left the journal of dpkg", func(b *shellBox) { b.write("fake/upgrade-leaves-journal", "") }, true,
			[]string{"do not restore anything", forwardCommands}, []string{"taken back"}},
		// an earlier run of the block had begun (it is the one that wrote the marker): this run did nothing more, and the marker is not this run's to take back
		{"an earlier run had begun, and apt fails at once the second time", func(b *shellBox) { b.write("bk/begun-upgrade", "") }, true,
			[]string{"do not restore anything", forwardCommands}, []string{"taken back"}},
	} {
		b := newShellBox(t)
		b.write("bk/done-download", "")
		b.write("fake/upgrade-fails", "")
		tc.set(b)
		out, code := b.run(ok, "minimal")
		if code == 0 || b.exists("bk/done-minimal") || b.exists("bk/begun-upgrade") != tc.marker {
			t.Errorf("%s: exit %d, begun-upgrade %v, %q", tc.name, code, b.exists("bk/begun-upgrade"), out)
		}
		for _, want := range tc.says {
			if !strings.Contains(out, want) {
				t.Errorf("%s: the stop does not say %q: %q", tc.name, want, out)
			}
		}
		for _, not := range tc.not {
			if strings.Contains(out, not) {
				t.Errorf("%s: the stop says %q: %q", tc.name, not, out)
			}
		}
	}
	// with the marker taken back the way back is open again: the restore script and block 2 do not refuse for it, and block 5 can be run again
	b := newShellBox(t)
	b.write("bk/done-download", "")
	b.write("fake/upgrade-fails", "")
	if _, code := b.run(ok, "minimal"); code == 0 || b.exists("bk/begun-upgrade") {
		t.Fatalf("the first run: exit %d, begun %v", code, b.exists("bk/begun-upgrade"))
	}
	os.Remove(filepath.Join(b.dir, "fake", "upgrade-fails"))
	if out, code := b.run(ok, "minimal"); code != 0 || !b.exists("bk/done-minimal") || !b.exists("bk/begun-upgrade") {
		t.Errorf("block 5 run again: exit %d, %q", code, out)
	}
	// a package manager at work: nothing has begun, so no marker, and no apt
	b = newShellBox(t)
	b.write("bk/done-download", "")
	b.write("fake/ps.txt", psTable("apt-get         apt-get update"))
	if out, code := b.run(ok, "minimal"); code == 0 || b.exists("bk/begun-upgrade") || !strings.Contains(out, "Nothing has begun") {
		t.Errorf("a package manager at work: exit %d, %q", code, out)
	}
}

// stopLogName is the file that the stops after the point of no return send the owner to for what dpkg and the scripts of the packages printed: the log of apt's pty in the owner's
// text, and the log of the unit in the unit's, where apt has no pty and term.log holds nothing of it.
func stopLogName() string {
	if suiteAuto {
		return "output.log"
	}
	return "/var/log/apt/term.log"
}

// The stops after the point of no return name the logs that hold what the terminal does not (screen keeps 100 lines, tmux 2000, a dead terminal none).
func TestTheStopsAfterThePointOfNoReturnNameTheLogs(t *testing.T) {
	_, rb := realRunbook(t)
	ok := typed(rb, "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON")
	logs := []string{stopLogName(), "/var/log/apt/history.log", "/var/log/dpkg.log"}
	left := "Reading package lists...\n12 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"
	for _, tc := range []struct {
		name, id, prev string
		set            func(b *shellBox)
	}{
		{"block 5, the upgrade fails after dpkg began", "minimal", "download", func(b *shellBox) { b.write("fake/upgrade-fails", ""); b.write("fake/upgrade-touches-status", "") }},
		{"block 5, the simulation after it has no summary", "minimal", "download", func(b *shellBox) { b.write("fake/sim.txt", "E: broken\n") }},
		{"block 5, a protected package would go", "minimal", "download", func(b *shellBox) { b.write("fake/sim.txt", resimOf("fuse", "samba-libs")) }},
		{"block 6, dpkg cannot configure", "full", "minimal", func(b *shellBox) { b.write("fake/dist-fails-1", ""); b.write("fake/dpkg-fails", "") }},
		{"block 6, the repair fails", "full", "minimal", func(b *shellBox) { b.write("fake/dist-fails-1", ""); b.write("fake/fix-fails", "") }},
		{"block 6, it fails again", "full", "minimal", func(b *shellBox) { b.write("fake/dist-fails-1", ""); b.write("fake/dist-fails-2", "") }},
		{"block 6, a catch-up pass fails", "full", "minimal", func(b *shellBox) { b.write("fake/sim1.txt", left); b.write("fake/dist-fails-2", "") }},
		{"block 6, a catch-up pass would remove a protected package", "full", "minimal", func(b *shellBox) { b.write("fake/sim1.txt", resimOf("fuse", "samba-libs")) }},
		{"block 6, nothing converges", "full", "minimal", func(b *shellBox) { b.write("fake/sim.txt", left) }},
		{"block 6, the check at the end fails", "full", "minimal", func(b *shellBox) { b.write("fake/check-fails", "") }},
	} {
		b := newShellBox(t)
		b.write("bk/done-"+tc.prev, "")
		tc.set(b)
		out, code := b.run(ok, tc.id)
		if code == 0 || !strings.Contains(out, "STOP:") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
			continue
		}
		for _, want := range logs {
			if !strings.Contains(out, want) {
				t.Errorf("%s: the stop does not name %s: %q", tc.name, want, out)
			}
		}
	}
	// the stops of before the marker do not say that the upgrade has begun, or send to the logs of an upgrade that did not run
	b := newShellBox(t)
	b.write("bk/done-download", "")
	b.write("fake/upgrade-fails", "")
	if out, _ := b.run(ok, "minimal"); strings.Contains(out, stopLogName()) {
		t.Errorf("a stop of before the marker names the logs: %q", out)
	}
}

// ---- blocks 1 and 8 and the restore script: the state of the timers --------------------------------------------------------------

func TestTheRestoreScriptSaysHowToStartTheTimersThatBlock1Stopped(t *testing.T) {
	dir := t.TempDir()
	for rel, c := range map[string]string{
		"etc/sources.list":         "deb http://deb.debian.org/debian bookworm main\n",
		"bk/apt-orig/sources.list": "deb http://deb.debian.org/debian bullseye main\n",
		"bk/restore-sources.sh":    restoreScript,
		"bk/done-sources":          "",
		"timers.state":             "apt-daily.timer active\napt-daily-upgrade.timer inactive\nunattended-upgrades.service active\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(filepath.Join(dir, "etc", "sources.list.d"), 0o755)
	out, code := runSh(t, dir, "APT_ETC=./etc CASAOS_NO_UPDATE=1 CASAOS_TIMERS_STATE=./timers.state sh ./bk/restore-sources.sh")
	// the units that were running, as the state file has them, in the line that starts them again; the one that was not running is not in it
	if code != 0 || !strings.Contains(out, "start them again, as they were:  systemctl start apt-daily.timer unattended-upgrades.service   then:  rm -f ./timers.state") || strings.Contains(out, "apt-daily-upgrade.timer") {
		t.Errorf("exit %d, %q", code, out)
	}
	// no state, or nothing that ran: nothing is said of the timers
	for _, state := range []string{"", "apt-daily.timer inactive\n"} {
		dir2 := t.TempDir()
		os.MkdirAll(filepath.Join(dir2, "etc", "sources.list.d"), 0o755)
		os.MkdirAll(filepath.Join(dir2, "bk", "apt-orig"), 0o755)
		os.WriteFile(filepath.Join(dir2, "bk", "restore-sources.sh"), []byte(restoreScript), 0o644)
		if state != "" {
			os.WriteFile(filepath.Join(dir2, "timers.state"), []byte(state), 0o644)
		}
		out, code := runSh(t, dir2, "APT_ETC=./etc CASAOS_NO_UPDATE=1 CASAOS_TIMERS_STATE=./timers.state sh ./bk/restore-sources.sh")
		if code != 0 || strings.Contains(out, "systemctl start") {
			t.Errorf("state %q: exit %d, %q", state, code, out)
		}
	}
}

func TestBlock8StartsTheTimersThatWereRunningAndRemovesTheirState(t *testing.T) {
	_, rb := realRunbook(t)
	b := newShellBox(t)
	setupBlock8(b)
	out, code := b.run(rb, "after")
	if code != 0 || !b.exists("bk/done-after") {
		t.Fatalf("exit %d, %q", code, out)
	}
	calls := b.read("fake/calls.log")
	if !strings.Contains(calls, "systemctl start apt-daily.timer") || !strings.Contains(calls, "systemctl start unattended-upgrades.service") || strings.Contains(calls, "systemctl start apt-daily-upgrade.timer") {
		t.Errorf("the timers: %q", calls)
	}
	if b.exists("timers.state") {
		t.Error("the state of the timers is left behind when the hop has ended")
	}
	// the state is removed when the block ENDS, not before: a block that stops after it has started the timers (Docker cannot be asked) goes on from the state, run again
	// (the unit's block does not stop for a Docker that cannot be asked: it ends, and takes the state away)
	b = newShellBox(t)
	setupBlock8(b)
	b.fake("curl", "exit 6")
	if out, code := b.run(rb, "after"); !suiteAuto && (code == 0 || !b.exists("timers.state")) {
		t.Errorf("a block that stops: exit %d, the state of the timers is there: %v, %q", code, b.exists("timers.state"), out)
	}
	// the state is not there (it was removed, or block 1 was not run by this text): the block says so, starts nothing, and ends
	b = newShellBox(t)
	setupBlock8(b)
	os.Remove(filepath.Join(b.dir, "timers.state"))
	out, code = b.run(rb, "after")
	if code != 0 || !b.exists("bk/done-after") || !strings.Contains(out, "./timers.state is not there, so no timer of apt is started") || strings.Contains(b.read("fake/calls.log"), "systemctl start") {
		t.Errorf("no state: exit %d, %q", code, out)
	}
}

// ---- block 2 and block 8: what is compared ------------------------------------------------------------------------------------

func TestBlock2WritesDownTheUnitsThatFailAndTheReCasaOSUnitsAndBlock8ComparesThem(t *testing.T) {
	_, rb := realRunbook(t)
	b := newShellBox(t)
	b.write("bk/done-timers", "")
	b.write("root/etc/hosts", "x\n")
	b.write("fake/packages.tsv", "ii \tbash\t5.1-2\n")
	b.write("fake/ss.txt", "LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\n")
	b.write("fake/failed.txt", "e2scrub_reap.service loaded failed failed Online ext4 Metadata Check for All Filesystems\napt-daily.service loaded failed failed Daily apt download activities\n")
	b.write("fake/casaos-units.txt", "casaos-gateway.service loaded active running CasaOS Gateway\ncasaos-app-management.service loaded failed failed CasaOS App Management\n")
	if out, code := b.run(rb, "snapshot"); code != 0 {
		t.Fatalf("block 2: exit %d, %q", code, out)
	}
	if got := b.read("bk/failed-units.txt"); got != "apt-daily.service\ne2scrub_reap.service\n" {
		t.Errorf("failed-units.txt = %q", got)
	}
	if got := b.read("bk/casaos-units.txt"); got != "casaos-app-management.service failed\ncasaos-gateway.service active\n" {
		t.Errorf("casaos-units.txt = %q", got)
	}
	// block 8: the one that failed before is not news, the one that fails now and did not is
	b = newShellBox(t)
	setupBlock8(b)
	b.write("bk/failed-units.txt", "e2scrub_reap.service\n")
	b.write("fake/failed.txt", "e2scrub_reap.service loaded failed failed Online ext4 Metadata Check for All Filesystems\nsmbd.service loaded failed failed Samba SMB Daemon\n")
	b.write("fake/casaos-units.txt", "casaos-gateway.service loaded active running CasaOS Gateway\n")
	out, code := b.run(rb, "after")
	if code != 0 {
		t.Fatalf("block 8: exit %d, %q", code, out)
	}
	head := "--- units that fail now and did not before the hop (block 2 wrote down the ones that failed before, in ./bk/failed-units.txt):\n"
	i := strings.Index(out, head)
	if i < 0 {
		t.Fatalf("the comparison is not there: %q", out)
	}
	news := out[i+len(head) : i+len(head)+strings.Index(out[i+len(head):], "---")]
	if news != "smbd.service\n" {
		t.Errorf("the units that fail now and did not before: %q", news)
	}
	if !strings.Contains(out, "--- the ReCasaOS units now, and as they were before the hop (unit and state):") || !strings.Contains(out, "  before:\ncasaos-gateway.service active\n") {
		t.Errorf("the ReCasaOS units are not shown beside the ones of before: %q", out)
	}
}

// What block 8 says of the files that dpkg left is what it lists: the new ones since block 2, and not the years of leftovers; and the comment lines of a source of Docker that it
// puts back are left as they are, byte for byte (a word of the old release in a comment is no codename to change, and awk rebuilds a line it changes).
func TestBlock8ListsTheFilesThatDpkgLeftSinceBlock2AndLeavesTheCommentsOfDockersSourceAlone(t *testing.T) {
	_, rb := realRunbook(t)
	b := newShellBox(t)
	setupBlock8(b)
	b.write("etc/ssh/sshd_config.dpkg-old", "x")
	b.write("etc/ssh/sshd_config.dpkg-dist", "x")
	b.write("bk/dpkg-files.txt", "./etc/ssh/sshd_config.dpkg-old\n")
	comment := "#   deb  [arch=amd64]\thttps://download.docker.com/linux/debian    bullseye   stable   # bullseye\n"
	b.write("etc-apt/sources.list.d/docker.list.casaos-paused", "# Docker, for bullseye\n"+comment+"deb  [arch=amd64]   https://download.docker.com/linux/debian   bullseye   stable\n   # indented  bullseye\n")
	out, code := b.run(rb, "after")
	if code != 0 {
		t.Fatalf("exit %d, %q", code, out)
	}
	head := "Files that dpkg left beside the ones you edited, and that were not there when block 2 looked"
	i := strings.Index(out, head)
	if i < 0 || !strings.Contains(out, "(a .dpkg-dist is the new version of a file of yours that was NOT installed over it") {
		t.Fatalf("the list is not introduced: %q", out)
	}
	rest := out[i:]
	list := rest[strings.Index(rest, "\n")+1 : strings.Index(rest, "  (all of them")]
	if list != "./etc/ssh/sshd_config.dpkg-dist\n" || !strings.Contains(rest, "(all of them, the old ones too: ./bk/dpkg-files-after.txt)") {
		t.Errorf("the new files are %q", list)
	}
	if all := b.read("bk/dpkg-files-after.txt"); !strings.Contains(all, "sshd_config.dpkg-old") || !strings.Contains(all, "sshd_config.dpkg-dist") {
		t.Errorf("the file of all of them: %q", all)
	}
	// Docker's source: the comment lines come out as they went in (the spaces, the tab, the word), the line is rewritten
	want := "# Docker, for bullseye\n" + comment + "deb [arch=amd64] https://download.docker.com/linux/debian bookworm stable\n   # indented  bullseye\n"
	if got := b.read("etc-apt/sources.list.d/docker.list"); got != want {
		t.Errorf("docker.list:\n got %q\nwant %q", got, want)
	}
	// the end of the block says that the steps ran and not that the machine is healthy, after the marker
	i, j := strings.Index(out, "Block 8 done."), strings.Index(out, "That is not to say that the machine is healthy")
	if i < 0 || j < i {
		t.Errorf("the closing lines: %q", out)
	}
}

// ---- the text ------------------------------------------------------------------------------------------------------------------------

// The closing lines do not say that all went well beyond what was looked at; the state of the timers is the one file of timersState (no file of the folder of the plan holds
// it) and block 6 writes no boot (block 7 asks the packages of block 2 which kernel was there before); and nothing says that a restart on the new kernel does no harm.
func TestTheTextClaimsNoMoreThanItChecksAndKeepsTheStateOfTheTimersInOneFile(t *testing.T) {
	_, rb := realRunbook(t)
	for _, bad := range []string{"ALL CHECKS PASSED", "units.state", "boot_id-full", "every apt-get", "NOW RESTART", "chroot", "no harm"} {
		if strings.Contains(rb.Text, bad) {
			t.Errorf("the text says %q", bad)
		}
	}
	for _, want := range []string{timersState, "THIS TEXT AS A WHOLE HAS NOT BEEN RUN"} {
		if !strings.Contains(rb.Text, want) {
			t.Errorf("the text does not say %q", want)
		}
	}
	// the header names no commit and no review: nothing in the text says how it came to be
	for _, bad := range []string{"review round", "round 1", "round 2", "round 3", "commit"} {
		if strings.Contains(strings.ToLower(rb.Text), bad) {
			t.Errorf("the text names %q", bad)
		}
	}
	// the point of no return and the restart keep their banners
	if !slices.ContainsFunc(rb.Blocks, func(b RunbookBlock) bool { return b.PointOfNoReturn }) {
		t.Error("no point of no return")
	}
}

// ---- the state of the timers is put in place all at once ---------------------------------------------------------------------------

// stateLeftovers are the files beside the state of the timers that a run of block 1 left behind: the name of its own, when it was not removed.
func stateLeftovers(t *testing.T, b *shellBox) []string {
	t.Helper()
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "timers.state.") {
			left = append(left, e.Name())
		}
	}
	return left
}

// Two runs of block 1 at the same time (two terminals, a paste twice) must not write one file between them: each reads the timers into a name of its own and puts it in
// place with ln, which fails when the name is taken, and the one that comes second keeps the state of the first (it is the state of before the hop; the second may have read
// timers that the first had stopped already).
func TestBlock1PutsTheStateOfTheTimersInPlaceAllAtOnceAndAdoptsTheOneThatAnotherRunPut(t *testing.T) {
	_, rb := realRunbook(t)
	b := newShellBox(t)
	b.write("bk/done-verify", "")
	b.write("fake/active-apt-daily.timer", "")
	// the other run puts its state in place while this one is reading the timers: at the first question of systemctl
	b.fake("systemctl", `echo "systemctl $*" >> "$FAKE/calls.log"
case "$1" in
  is-active)
    if [ ! -f "$FAKE/other-ran" ]; then : > "$FAKE/other-ran"; printf 'apt-daily.timer active\napt-daily-upgrade.timer active\nunattended-upgrades.service active\n' > ./timers.state; fi
    if [ -f "$FAKE/active-$2" ]; then echo active; else echo inactive; exit 3; fi ;;
  stop) rm -f "$FAKE/active-$2" ;;
esac
exit 0`)
	out, code := b.run(rb, "timers")
	const theirs = "apt-daily.timer active\napt-daily-upgrade.timer active\nunattended-upgrades.service active\n"
	if code != 0 || !b.exists("bk/done-timers") || b.read("timers.state") != theirs || !strings.Contains(out, "was put in place by another run of this block while this one was reading the timers") {
		t.Errorf("another run put its state in place first: exit %d, state %q, %q", code, b.read("timers.state"), out)
	}
	if left := stateLeftovers(t, b); len(left) > 0 {
		t.Errorf("this run left %v", left)
	}
	// alone, the run puts its own state in place, whole, and says nothing of another
	b = newShellBox(t)
	b.write("bk/done-verify", "")
	b.write("fake/active-apt-daily.timer", "")
	out, code = b.run(rb, "timers")
	if code != 0 || b.read("timers.state") != "apt-daily.timer active\napt-daily-upgrade.timer inactive\nunattended-upgrades.service inactive\n" || strings.Contains(out, "another run") || len(stateLeftovers(t, b)) > 0 {
		t.Errorf("alone: exit %d, state %q, left %v, %q", code, b.read("timers.state"), stateLeftovers(t, b), out)
	}
}

// ln fails for another reason than the name being taken (a file system with no hard links, no room for the entry): the state is not at its name, and the block must neither say that
// another run put it there nor go on without it. It keeps the file that it read by a rename, and when even that fails it stops with words of its own.
func TestBlock1KeepsItsStateWhenLnFailsForAnotherReasonThanTheNameBeingTaken(t *testing.T) {
	_, rb := realRunbook(t)
	const state = "apt-daily.timer active\napt-daily-upgrade.timer inactive\nunattended-upgrades.service inactive\n"
	setup := func() *shellBox {
		b := newShellBox(t)
		b.write("bk/done-verify", "")
		b.write("fake/active-apt-daily.timer", "")
		b.fake("ln", "echo 'ln: failed to create hard link: Operation not permitted' >&2; exit 1")
		return b
	}
	b := setup()
	out, code := b.run(rb, "timers")
	if code != 0 || !b.exists("bk/done-timers") || b.read("timers.state") != state || strings.Contains(out, "another run") || !strings.Contains(out, "ln could not put ./timers.state in place") {
		t.Errorf("ln fails: exit %d, state %q, %q", code, b.read("timers.state"), out)
	}
	if left := stateLeftovers(t, b); len(left) > 0 {
		t.Errorf("the run left %v", left)
	}
	// the rename fails too: a stop of its own, no marker, and nothing of the private file left behind
	b = setup()
	b.fake("mv", "echo 'mv: cannot move: No space left on device' >&2; exit 1")
	out, code = b.run(rb, "timers")
	if code == 0 || b.exists("bk/done-timers") || b.exists("timers.state") || !strings.Contains(out, "STOP: the state of the timers could not be put in ./timers.state") || strings.Contains(out, "another run") {
		t.Errorf("ln and mv fail: exit %d, %q", code, out)
	}
	if left := stateLeftovers(t, b); len(left) > 0 {
		t.Errorf("the run left %v", left)
	}
	// another run has put its state at the name while this one's ln failed: that is the case that says so, and its file is kept
	b = setup()
	b.fake("ln", "printf 'apt-daily.timer active\\n' > ./timers.state; exit 1")
	out, code = b.run(rb, "timers")
	if code != 0 || b.read("timers.state") != "apt-daily.timer active\n" || !strings.Contains(out, "was put in place by another run of this block") {
		t.Errorf("another run's file at the name: exit %d, state %q, %q", code, b.read("timers.state"), out)
	}
}

// Two terminals that paste block 1 at once: every question of systemctl takes a moment, so that both runs are reading the timers when the other is. Each writes its state under its
// own name, one puts it in place and the other adopts it, and what is left is the whole state, one line for each unit, and nothing beside it. (With one name for both the runs
// truncate and append to the same file, and the state that block 8 reads has one line of three.)
func TestTwoRunsOfBlock1AtTheSameTimeKeepOneWholeState(t *testing.T) {
	_, rb := realRunbook(t)
	b := newShellBox(t)
	b.write("bk/done-verify", "")
	b.write("fake/active-apt-daily.timer", "")
	b.write("fake/active-unattended-upgrades.service", "")
	b.fake("systemctl", `echo "systemctl $*" >> "$FAKE/calls.log"
case "$1" in
  is-active) sleep 0.15; if [ -f "$FAKE/active-$2" ]; then echo active; else echo inactive; exit 3; fi ;;
  stop) rm -f "$FAKE/active-$2" ;;
esac
exit 0`)
	script := writeScript(t, b.paths(blockOf(t, rb.Text, "timers"), rb.PlanID))
	sh := shellOrSkip(t)
	var cmds [2]*exec.Cmd
	var outs [2]*bytes.Buffer
	for i := range cmds {
		outs[i] = &bytes.Buffer{}
		cmds[i] = exec.Command(sh, script)
		cmds[i].Dir = b.dir
		cmds[i].Env = b.environ()
		cmds[i].Stdout, cmds[i].Stderr = outs[i], outs[i]
	}
	for _, c := range cmds {
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range cmds {
		if err := c.Wait(); err != nil || !strings.Contains(outs[i].String(), "Block 1 done.") {
			t.Errorf("run %d: %v, %q", i, err, outs[i])
		}
	}
	const want = "apt-daily.timer active\napt-daily-upgrade.timer inactive\nunattended-upgrades.service active\n"
	if got := b.read("timers.state"); got != want {
		t.Errorf("the state of the timers after two runs at once = %q, want %q", got, want)
	}
	if left := stateLeftovers(t, b); len(left) > 0 {
		t.Errorf("the runs left %v", left)
	}
}

// ---- another plan of the machine -----------------------------------------------------------------------------------------------------

// A second plan has another id and another folder. The sources that the first one's block 3 rewrote are Debian 12's, the dashboard serves no runbook of the first once its 24
// hours are over, and a new plan is refused on those sources: blocks 0 and 3 of the second look at the folders of the others, and name the restore script of the one whose block
// 3 ran, or the words that only go forward when its upgrade has begun. The file of the timers' state is no plan. (A plan whose block 8 ended stops every block before these
// checks are made: TestNoBlockRunsOnAMachineThatAnotherPlanTookToItsEnd.)
func TestBlocks0And3SendTheOwnerToTheRestoreScriptOfAnotherPlanWhoseBlock3Ran(t *testing.T) {
	_, rb := realRunbook(t)
	good := typed(typed(rb, "TYPED_HOSTNAME", "testbox"), "ACCEPT_REMOVALS", "fuse")
	const other = "others/casaos-os-upgrade-p1-0123456789ab"
	script := "sh ./" + other + "/restore-sources.sh"
	prep := func(b *shellBox, id string) {
		setupVerify(t, b)
		if id == "sources" {
			b.write("bk/done-snapshot", "")
			b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
		}
	}
	for _, tc := range []struct {
		name   string
		set    func(b *shellBox)
		says   []string
		saysNo []string
	}{
		{"its block 3 ran and its restore script is there", func(b *shellBox) {
			os.MkdirAll(filepath.Join(b.dir, other, "apt-orig"), 0o755)
			b.write(other+"/restore-sources.sh", "# the script\n")
		}, []string{"block 3 of another plan of this machine began (./" + other + "/apt-orig is there)", "may no longer be the ones that this plan was made from", script, "then run this block again"}, []string{"Only forward"}},
		{"its block 3 ran and its restore script is gone", func(b *shellBox) {
			os.MkdirAll(filepath.Join(b.dir, other, "apt-orig"), 0o755)
		}, []string{"./" + other + "/restore-sources.sh is not", "apt-orig is the whole copy of /etc/apt", "put back by hand what differs"}, []string{script, "Only forward"}},
		{"its upgrade has begun", func(b *shellBox) {
			os.MkdirAll(filepath.Join(b.dir, other, "apt-orig"), 0o755)
			b.write(other+"/restore-sources.sh", "# the script\n")
			b.write(other+"/begun-upgrade", "")
		}, []string{"another plan of this machine has begun the upgrade of the packages (./" + other + " has begun-upgrade)", "Do not restore anything", "Only forward: " + forwardCommands}, []string{script}},
		{"its block 5 ended", func(b *shellBox) { b.write(other+"/done-minimal", "") },
			[]string{"another plan of this machine has begun the upgrade of the packages", "Only forward: " + forwardCommands}, []string{script}},
	} {
		for _, id := range []string{"verify", "sources"} {
			b := newShellBox(t)
			prep(b, id)
			tc.set(b)
			sources := b.read("etc-apt/sources.list")
			out, code := b.run(good, id)
			if code == 0 || !strings.Contains(out, "STOP:") || b.exists("bk/done-"+id) {
				t.Errorf("%s, block %s: exit %d, %q", tc.name, id, code, out)
			}
			for _, want := range tc.says {
				if !strings.Contains(out, want) {
					t.Errorf("%s, block %s: the stop does not say %q: %q", tc.name, id, want, out)
				}
			}
			for _, not := range tc.saysNo {
				if strings.Contains(out, not) {
					t.Errorf("%s, block %s: the stop says %q: %q", tc.name, id, not, out)
				}
			}
			// nothing was changed, and no apt was asked
			if b.exists("bk/apt-orig") || b.read("etc-apt/sources.list") != sources || strings.Contains(b.read("fake/calls.log"), "apt-get") {
				t.Errorf("%s, block %s: something was done before the stop: calls %q", tc.name, id, b.read("fake/calls.log"))
			}
		}
	}
	// what is no reason to stop
	for _, tc := range []struct {
		name string
		set  func(b *shellBox)
	}{
		{"a folder of another plan that did nothing yet", func(b *shellBox) { b.write(other+"/done-verify", "") }},
		{"a plan whose restore script ran (its copy is put aside)", func(b *shellBox) {
			os.MkdirAll(filepath.Join(b.dir, other, "apt-orig.restored.4242"), 0o755)
			b.write(other+"/restore-sources.sh", "# the script\n")
		}},
		{"the file of the state of the timers is no folder of a plan", func(b *shellBox) { b.write("others/casaos-os-upgrade-timers.state", "apt-daily.timer active\n") }},
		{"a file called as a folder is", func(b *shellBox) { b.write(other, "apt-orig") }},
	} {
		for _, id := range []string{"verify", "sources"} {
			b := newShellBox(t)
			prep(b, id)
			tc.set(b)
			if out, code := b.run(good, id); code != 0 || !b.exists("bk/done-"+id) {
				t.Errorf("%s, block %s: exit %d, %q", tc.name, id, code, out)
			}
		}
	}
}

// Block 0 says what is true of the folder of its own plan: once block 5 has begun the way is forward (no restore script is offered); with apt-orig and nothing else the
// sources MAY no longer be the plan's (a block 3 that stopped before it moved its first file leaves them as they were: it is not said that they are not), and the restore
// script is named; and when block 3 ended, the next block is block 4.
func TestBlock0SaysWhatItKnowsOfItsOwnFolderAndNoMore(t *testing.T) {
	_, rb := realRunbook(t)
	good := typed(rb, "TYPED_HOSTNAME", "testbox")
	for _, tc := range []struct {
		name   string
		set    func(b *shellBox)
		says   []string
		saysNo []string
	}{
		{"apt-orig only", func(b *shellBox) { os.MkdirAll(filepath.Join(b.dir, "bk", "apt-orig"), 0o755) },
			[]string{"STOP: block 3 has begun and did not end (./bk/apt-orig is there)", "may no longer be the ones that the plan was made from", "sh ./bk/restore-sources.sh puts the sources back", "block 3 can then be run again"},
			[]string{"are not the plan's any more", "Only forward"}},
		{"apt-orig and done-sources", func(b *shellBox) {
			os.MkdirAll(filepath.Join(b.dir, "bk", "apt-orig"), 0o755)
			b.write("bk/done-sources", "")
		}, []string{"block 3 has begun and ended (./bk/done-sources is there)", "The next is block 4", "sh ./bk/restore-sources.sh   (it forgets blocks 3 and 4)"}, []string{"Only forward"}},
		{"the upgrade has begun", func(b *shellBox) {
			os.MkdirAll(filepath.Join(b.dir, "bk", "apt-orig"), 0o755)
			b.write("bk/begun-upgrade", "")
		}, []string{"STOP: the upgrade has begun (block 5 was started): this block is for before it. Do not restore anything. Only forward: " + forwardWords}, []string{"sh ./bk/restore-sources.sh", "block 3 has begun"}},
	} {
		b := newShellBox(t)
		setupVerify(t, b)
		tc.set(b)
		out, code := b.run(good, "verify")
		if code == 0 || b.exists("bk/done-verify") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
		for _, want := range tc.says {
			if !strings.Contains(out, want) {
				t.Errorf("%s: the stop does not say %q: %q", tc.name, want, out)
			}
		}
		for _, not := range tc.saysNo {
			if strings.Contains(out, not) {
				t.Errorf("%s: the stop says %q: %q", tc.name, not, out)
			}
		}
	}
}

// ---- dpkg --audit: its stderr and its status are read ------------------------------------------------------------------------------------

// dpkg --audit prints what is half installed on its stdout and ends with 0; a database that it cannot open is on its stderr, with nothing on the stdout and the status 2 (real dpkg
// 1.20, a duplicate stanza of /var/lib/dpkg/status): a check that reads the stdout only passes such a machine as quiet.
func TestBlocks0And6And8StopWhenDpkgAuditSaysSomethingOnItsStderrOrEndsWithAnError(t *testing.T) {
	_, rb := realRunbook(t)
	good := typed(rb, "TYPED_HOSTNAME", "testbox")
	const dbError = "dpkg: error: parsing file '/var/lib/dpkg/status' near line 64 package 'adduser':\n multiple non-coinstallable package instances present; most probably due to an upgrade from an unofficial dpkg\n"
	for _, blk := range []struct {
		id   string
		prep func(b *shellBox)
		run  *Runbook
	}{
		{"verify", func(b *shellBox) { setupVerify(t, b) }, good},
		{"full", func(b *shellBox) { b.write("bk/done-minimal", "") }, rb},
		{"after", func(b *shellBox) { setupBlock8(b) }, rb},
	} {
		for _, tc := range []struct {
			name  string
			set   func(b *shellBox)
			stops string
		}{
			{"a package half configured, on stdout", func(b *shellBox) {
				b.write("fake/audit.txt", "The following packages are only half configured, probably due to problems\nconfiguring them the first time.\n osp-h  noisy\n")
			}, "dpkg --audit says: The following packages are only half configured"},
			{"a database that dpkg cannot open: stderr, status 2, nothing on stdout", func(b *shellBox) { b.write("fake/audit-error.txt", dbError) },
				"dpkg --audit says: dpkg: error: parsing file '/var/lib/dpkg/status' near line 64"},
			{"an error status and no word", func(b *shellBox) { b.write("fake/audit-error.txt", "") }, "dpkg --audit says: nothing, but it ended with an error"},
		} {
			b := newShellBox(t)
			blk.prep(b)
			tc.set(b)
			out, code := b.run(blk.run, blk.id)
			if code == 0 || !strings.Contains(out, "STOP: dpkg is not quiet") || !strings.Contains(out, "half installed") || !strings.Contains(out, tc.stops) || b.exists("bk/done-"+blk.id) {
				t.Errorf("block %s, %s: exit %d, %q", blk.id, tc.name, code, out)
			}
		}
		b := newShellBox(t)
		blk.prep(b)
		if out, code := b.run(blk.run, blk.id); code != 0 || !b.exists("bk/done-"+blk.id) {
			t.Errorf("block %s with a quiet dpkg: exit %d, %q", blk.id, code, out)
		}
	}
}

// ---- the repair ladder of block 6 ---------------------------------------------------------------------------------------------------------

// Block 6 mends a dist-upgrade that failed with dpkg --configure -a, apt-get -f install and the dist-upgrade again. apt-get -f install removes a package that has an unmet
// dependency without a stop (apt 2.2.4 in a Debian 11 chroot: a package of a protected name, samba-osptest, with a dependency that nothing has, is listed as REMOVED by the
// simulation, testdata/real/chroot-d11-repair-removes-protected-package.txt, and the real repair removed it), so the repair is simulated and the simulation looked at as the
// full upgrade's is, and so is the full upgrade after it, before each is run.
func TestTheRepairLadderOfBlock6SimulatesTheRepairAndTheFullUpgradeAfterItAndGuardsEachOne(t *testing.T) {
	_, rb := realRunbook(t)
	const (
		simRepair  = "--force-confold -s -f install\n"
		realRepair = "--force-confold -f install\n"
		realDist   = "--force-confold dist-upgrade\n"
		simDist    = "--force-confold -s dist-upgrade\n"
	)
	bad := resimOf("fuse", "samba-libs")
	for _, tc := range []struct {
		name         string
		set          func(b *shellBox)
		stops        string
		repair, dist int // the real repairs and the real dist-upgrades that ran (the first dist-upgrade, the one that fails, is one)
	}{
		{"nothing to remove", func(b *shellBox) {}, "", 1, 2},
		{"the repair would remove a protected package", func(b *shellBox) { b.write("fake/fixsim.txt", bad) }, "STOP: the repair would remove samba-libs, which is protected", 0, 1},
		{"the repair would remove a protected package, in the words of the real apt", func(b *shellBox) {
			b.write("fake/fixsim.txt", realFile(t, "chroot-d11-repair-removes-protected-package.txt"))
		},
			"STOP: the repair would remove samba-osptest, which is protected", 0, 1},
		{"the repair would remove an essential package", func(b *shellBox) {
			b.write("fake/fixsim.txt", resimOf("an-essential"))
			b.fake("dpkg-query", "echo 'yes required'")
		}, "STOP: the repair would remove an-essential, which is essential or required", 0, 1},
		{"the repair would downgrade", func(b *shellBox) {
			b.write("fake/fixsim.txt", "The following packages will be DOWNGRADED:\n  docker-ce\n0 upgraded, 0 newly installed, 1 downgraded, 0 to remove and 0 not upgraded.\n")
		}, "apt would downgrade packages", 0, 1},
		{"the repair cannot be worked out", func(b *shellBox) { b.write("fake/fixsim-fails", "") }, "STOP: apt cannot work out the repair: see ./bk/repair.txt", 0, 1},
		{"the simulation of the repair has no summary", func(b *shellBox) { b.write("fake/fixsim.txt", "Reading package lists...\n") }, "apt printed no summary line", 0, 1},
		{"the full upgrade after the repair would remove a protected package", func(b *shellBox) { b.write("fake/sim1.txt", bad) },
			"STOP: the full upgrade after the repair would remove samba-libs, which is protected", 1, 1},
		{"the full upgrade after the repair cannot be worked out", func(b *shellBox) { b.write("fake/sim-fails-1", "") },
			"STOP: apt cannot work out the full upgrade after the repair: see ./bk/after-repair.txt", 1, 1},
		{"the full upgrade after the repair would downgrade", func(b *shellBox) {
			b.write("fake/sim1.txt", "The following packages will be DOWNGRADED:\n  docker-ce\n0 upgraded, 0 newly installed, 1 downgraded, 0 to remove and 0 not upgraded.\n")
		}, "apt would downgrade packages", 1, 1},
	} {
		b := newShellBox(t)
		b.write("bk/done-minimal", "")
		b.write("fake/dist-fails-1", "")
		tc.set(b)
		out, code := b.run(rb, "full")
		calls := b.read("fake/calls.log")
		if tc.stops == "" && (code != 0 || !b.exists("bk/done-full")) || tc.stops != "" && (code == 0 || b.exists("bk/done-full") || !strings.Contains(out, tc.stops)) {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
		if strings.Contains(tc.stops, "protected") || strings.Contains(tc.stops, "essential") {
			for _, want := range []string{"DO NOT UNDO ANYTHING", stopLogName()} {
				if !strings.Contains(out, want) {
					t.Errorf("%s: the stop does not say %q: %q", tc.name, want, out)
				}
			}
		}
		if n := strings.Count(calls, simRepair); n != 1 {
			t.Errorf("%s: the repair was simulated %d times: %s", tc.name, n, calls)
		}
		if n := strings.Count(calls, realRepair); n != tc.repair {
			t.Errorf("%s: %d real repairs, want %d: %s", tc.name, n, tc.repair, calls)
		}
		if n := strings.Count(calls, realDist); n != tc.dist {
			t.Errorf("%s: %d real dist-upgrades, want %d: %s", tc.name, n, tc.dist, calls)
		}
		// the order: the first dist-upgrade (it fails), the simulation of the repair, the repair, the simulation of the full upgrade, the full upgrade
		if tc.stops == "" {
			at := func(s string, from int) int {
				i := strings.Index(calls[from:], s)
				if i < 0 {
					t.Fatalf("%s: %q is not in\n%s", tc.name, s, calls)
				}
				return from + i + len(s)
			}
			p := at(realDist, 0)
			p = at(simRepair, p)
			p = at(realRepair, p)
			p = at(simDist, p)
			at(realDist, p)
		}
	}
}

// ---- block 4 ------------------------------------------------------------------------------------------------------------------------------

// Block 4 only downloads, for the minimal upgrade that block 5 runs: pasted again after block 5 has begun it would say that no package has been installed, and offer the restore
// script, which refuses then. It stops where blocks 0, 2 and 3 do. And its own stops say where the owner goes by the marker as it is when they are said.
func TestBlock4StopsOnceTheUpgradeHasBegunAndSaysWhereToGoByTheMarker(t *testing.T) {
	_, rb := realRunbook(t)
	for _, marker := range []string{"begun-upgrade", "done-minimal"} {
		b := newShellBox(t)
		b.write("bk/done-sources", "")
		b.write("archives/a.deb", "x")
		b.write("bk/"+marker, "")
		out, code := b.run(rb, "download")
		if code == 0 || !strings.Contains(out, "STOP: the upgrade has begun (block 5 was started)") || !strings.Contains(out, "Do not restore anything. Only forward: "+forwardWords) ||
			strings.Contains(out, "No package has been installed or removed yet") || b.exists("bk/done-download") || b.exists("fake/calls.log") {
			t.Errorf("block 4 after %s: exit %d, %q", marker, code, out)
		}
	}
	// the download fails, or leaves nothing in the cache, and a block 5 in another terminal wrote its marker in the meantime: the stop says it has begun
	for _, tc := range []struct {
		name  string
		apt   string
		debs  bool
		stops string
	}{
		{"the download fails", `case "$*" in *--download-only*) : > ./bk/begun-upgrade; exit 100;; esac; exit 0`, true, "STOP: the download did not go through"},
		{"nothing is in the cache", `case "$*" in *--download-only*) : > ./bk/begun-upgrade;; esac; exit 0`, false, "STOP: no package was downloaded"},
	} {
		b := newShellBox(t)
		b.write("bk/done-sources", "")
		if tc.debs {
			b.write("archives/a.deb", "x")
		}
		b.fake("apt-get", tc.apt)
		out, code := b.run(rb, "download")
		if code == 0 || !strings.Contains(out, tc.stops) || !strings.Contains(out, "The upgrade has begun (block 5 was started): do not restore anything. Only forward: "+forwardWords) || strings.Contains(out, "put the sources back") || b.exists("bk/done-download") {
			t.Errorf("%s, with the marker: exit %d, %q", tc.name, code, out)
		}
	}
	// no marker: nothing has been installed, and the sources can go back; the stop of the empty cache says what to do about it
	for _, tc := range []struct {
		name  string
		debs  bool
		fail  bool
		stops string
	}{
		{"the download fails", true, true, "STOP: the download did not go through: the network, or a lock held by another package manager"},
		{"nothing is in the cache", false, false, "STOP: no package was downloaded: apt had nothing to fetch, or the cache of downloaded packages was emptied (apt-get clean), or the download did not go through"},
	} {
		b := newShellBox(t)
		b.write("bk/done-sources", "")
		if tc.debs {
			b.write("archives/a.deb", "x")
		}
		if tc.fail {
			b.write("fake/download-fails", "")
		}
		out, code := b.run(rb, "download")
		if code == 0 || !strings.Contains(out, tc.stops) || !strings.Contains(out, "No package has been installed or removed yet: run this block again, or put the sources back with sh ./bk/restore-sources.sh") ||
			strings.Contains(out, "Only forward") || b.exists("bk/done-download") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
	}
}

// ---- block 5: the busy stop says where the owner is --------------------------------------------------------------------------------------

// A dead terminal in the middle of block 5 leaves its marker (and apt may still be at work): pasted again, the block stops on the busy check, and it must not say that nothing has begun.
func TestTheBusyStopOfBlock5ChoosesItsWordsByTheMarker(t *testing.T) {
	_, rb := realRunbook(t)
	ok := typed(rb, "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON")
	for _, begun := range []bool{false, true} {
		b := newShellBox(t)
		b.write("bk/done-download", "")
		if begun {
			b.write("bk/begun-upgrade", "")
		}
		b.write("fake/ps.txt", psTable("apt-get         apt-get upgrade"))
		out, code := b.run(ok, "minimal")
		if code == 0 || !strings.Contains(out, "STOP: a package manager is running (the process list says: apt-get apt-get upgrade)") || b.exists("bk/done-minimal") || b.exists("bk/begun-upgrade") != begun {
			t.Errorf("marker %v: exit %d, %q", begun, code, out)
			continue
		}
		hasBegun := strings.Contains(out, "The upgrade has begun (an earlier run of this block wrote its marker): do not undo anything.")
		nothing := strings.Contains(out, "Nothing has begun: this block has not written its marker.")
		if hasBegun != begun || nothing == begun {
			t.Errorf("marker %v: the stop says it has begun: %v, that nothing has: %v: %q", begun, hasBegun, nothing, out)
		}
	}
}

// ---- the ends of blocks 6 and 8 ---------------------------------------------------------------------------------------------------------

// Block 6 ends with the next step (block 7, and no restart before it has ended), and apt prints that some packages are no longer needed: the text says not to run apt autoremove
// by hand, in block 6 where apt says it and in block 8 where the owner is done and may reach for it. net-tools is a package that this text protects and that apt listed as no longer
// needed in the measured hop.
func TestTheEndsOfBlocks6And8SayWhatComesNextAndNotToAutoremoveBlindly(t *testing.T) {
	_, rb := realRunbook(t)
	if !slices.Contains(ProtectedNames, "net-tools") {
		t.Fatal("net-tools is not protected any more: the sentence of block 8 names it")
	}
	b := newShellBox(t)
	b.write("bk/done-minimal", "")
	out, code := b.run(rb, "full")
	i, j := strings.Index(out, "Block 6 done."), strings.Index(out, "The next is block 7: do not restart the machine before it has ended")
	if code != 0 || i < 0 || j < i || !strings.Contains(out, "do not run apt autoremove by hand, see the end of block 8") {
		t.Errorf("block 6: exit %d, %q", code, out)
	}
	b = newShellBox(t)
	setupBlock8(b)
	out, code = b.run(rb, "after")
	for _, want := range []string{"Do not run apt autoremove without looking", "net-tools", "apt-get -s autoremove", "apt-mark manual <the name>"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("block 8: exit %d, it does not say %q: %q", code, want, out)
		}
	}
}

// ---- what a stop tells to do ------------------------------------------------------------------------------------------------------------

// The three room stops of block 0 (the ones that the plan's sizes make, and the coarse floors when it has none) say what to do about it, as the one of /boot does.
func TestEveryRoomStopOfBlock0SaysWhatToDo(t *testing.T) {
	p, rb := realRunbook(t)
	remedy := "Free some room there (apt-get clean empties the cache of downloaded packages; remove what you do not need), then run this block again."
	good := typed(rb, "TYPED_HOSTNAME", "testbox")
	for _, row := range p.Disk {
		b := newShellBox(t)
		setupVerify(t, b)
		b.write("fake/avail"+strings.ReplaceAll(row.Path, "/", "_"), "1000\n")
		out, code := b.run(good, "verify")
		if code == 0 || !strings.Contains(out, "STOP: "+row.Path+" needs ") || !strings.Contains(out, "MB free for this plan, and has less. "+remedy) {
			t.Errorf("%s: exit %d, %q", row.Path, code, out)
		}
	}
	q := *p
	q.Disk = nil
	coarse := buildFor(t, q)
	for path, want := range map[string]string{"/var/cache/apt/archives": "/var/cache/apt/archives has less than 1.5 GB free.", "/": "/ has less than 2 GB free."} {
		b := newShellBox(t)
		setupVerify(t, b)
		b.write("fake/avail"+strings.ReplaceAll(path, "/", "_"), "1000\n")
		out, code := b.run(typed(coarse, "TYPED_HOSTNAME", "testbox"), "verify")
		if code == 0 || !strings.Contains(out, "STOP: "+want+" "+remedy) {
			t.Errorf("%s (the coarse floors): exit %d, %q", path, code, out)
		}
	}
}

// Block 3 puts the sources back with its own restore script and says that the block can be run again: it does not send the owner to a new plan when only the network failed, and it
// does when the machine is not what the plan saw (apt would remove other packages, or keep some back).
func TestBlock3SendsToANewPlanOnlyWhenTheMachineIsNotWhatThePlanSaw(t *testing.T) {
	_, rb := realRunbook(t)
	good := typed(rb, "ACCEPT_REMOVALS", "fuse")
	for _, tc := range []struct {
		name    string
		set     func(b *shellBox)
		newPlan bool
	}{
		{"apt-get update fails", func(b *shellBox) { b.write("fake/update-fails", "") }, false},
		{"the simulation fails", func(b *shellBox) { b.write("fake/sim-fails-1", "") }, false},
		{"apt would remove other packages", func(b *shellBox) { b.write("fake/sim.txt", resimOf("fuse", "libsemanage1", "samba")) }, true},
		{"apt would keep a package back", func(b *shellBox) { b.write("fake/sim.txt", resimKept(3, true, "fuse", "libsemanage1")) }, true},
	} {
		b := newShellBox(t)
		b.write("bk/done-snapshot", "")
		setupVerify(t, b)
		b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
		tc.set(b)
		out, code := b.run(good, "sources")
		if code == 0 || !strings.Contains(out, "To put the sources back: sh ./bk/restore-sources.sh   (it also lets this block be run again, and this block says when the plan is no longer good).") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
			continue
		}
		if got := strings.Contains(strings.ToLower(out), "ask for a new plan"); got != tc.newPlan {
			t.Errorf("%s: it sends to a new plan: %v, want %v: %q", tc.name, got, tc.newPlan, out)
		}
	}
}

// apt runs with its pty in every block (no Dpkg::Use-Pty=0, which the measured hop had): apt 2.2.4 writes what the scripts of the packages print to /var/log/apt/term.log only
// through it, and the STOP lines after the point of no return send the owner to that file. What dpkg prints when it is run by hand is on the terminal and nowhere else, and the STOP of
// that command says so. Neither is the E: line of apt itself in a log (a fetch that failed leaves the three logs empty): the words tell the owner to copy the terminal too. And the header
// owns up that the pty was not in the measured hop.
func TestAptRunsWithItsPtySoThatTermLogHoldsWhatThePackagesPrint(t *testing.T) {
	_, rb := realRunbook(t)
	if strings.Contains(rb.Text, "-o Dpkg::Use-Pty") {
		t.Error("a block runs apt without its pty: term.log would hold nothing of what the scripts of the packages print")
	}
	for _, want := range []string{
		"What dpkg and the scripts of the packages printed is in /var/log/apt/term.log; what apt did is in /var/log/apt/history.log, and dpkg's own steps are in /var/log/dpkg.log: send the last 100 lines of each. The lines of apt itself (the ones that start with E:) are on the terminal only: copy what the terminal shows too.",
		"dpkg run by hand does not write term.log",
		"the measured hop ran them with Dpkg::Use-Pty=0, and this text leaves apt's pty on",
		"APT'S OWN PTY, WHICH THIS TEXT LEAVES ON, WAS NOT RUN IN THE MEASURED HOP",
		"never in that virtual machine and never in a real tmux, screen or console",
	} {
		if !strings.Contains(rb.Text, want) {
			t.Errorf("the text does not say %q", want)
		}
	}
	// apt does not keep its own output in term.log: no sentence says that it does
	for _, bad := range []string{"What apt printed", "the scripts of the packages included"} {
		if strings.Contains(rb.Text, bad) {
			t.Errorf("the text says %q", bad)
		}
	}
}
