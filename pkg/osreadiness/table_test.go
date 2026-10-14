package osreadiness

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// The checks of the table as a whole: the report of a healthy box, the codes, and the commands that
// all of them together ask for.

// theCodes are the codes of the checks, as the page, the proof in CI and the owner's copy of the
// report know them. A code that is added, renamed or taken out says so here, and nowhere else is
// it allowed to happen by itself.
var theCodes = []string{
	// the host
	"unsupported_os", "vendor_os", "unproven_arch", "container", "virt_xen", "no_systemd", "not_root",
	// the state of the machine
	"busy", "apps", "reboot_first", "uptime_long", "broken_dpkg", "held", "held_docker",
	// the sources
	"sources_cdrom", "sources_alias", "sources_pin", "sources_proposed", "sources_format", "sources_third_party", "sources_backports", "sources_firmware", "sources_network_checked_later",
	// the packages
	"foreign_arch", "keyring_old", "openssh_old", "host_services", "dkms",
	// the disks and the start
	"merged_usr", "cgroup1", "bootloader", "root_zfs", "root_luks", "md_degraded", "smart_failed", "fstab_unresolved", "fstab_network", "log_on_tmpfs", "disk_root", "disk_archives", "disk_boot", "ram", "power",
	// the network
	"network_uplink", "network_names",
	// Docker and the apps
	"docker_swarm", "docker_origin", "docker_down", "docker_update_pending", "containers_policy", "containers_ports", "backup_age", "snapshot_reminder",
	// what the hop is not about the machine
	"hold_until", "not_offered",
}

func TestTheTableHasTheCodesOfTheSpecAndNoOther(t *testing.T) {
	var got []string
	for _, c := range Checks() {
		got = append(got, c.Code)
		if c.Title == "" || !slices.Contains(groupOrder, c.Group) {
			t.Errorf("%s: title %q, group %q", c.Code, c.Title, c.Group)
		}
		for _, hop := range c.Hops {
			if !slices.Contains([]HopID{HopDebian11to12, HopDebian12to13, HopUbuntu2204to2404, HopUbuntu2404to2604}, hop) {
				t.Errorf("%s names the hop %q, which is none", c.Code, hop)
			}
		}
	}
	want := slices.Clone(theCodes)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("the table has\n%v\nthe spec has\n%v", got, want)
	}
}

// notOfThisHop are the checks that a Debian 11 box does not run: they are for other hops.
var notOfDebian11 = []string{"openssh_old", "cgroup1", "hold_until", "not_offered"}

func TestTheReportOfAHealthyDebian11BoxIsReadyAndEveryCheckKnows(t *testing.T) {
	f := newBox(t)
	report := Run(context.Background(), f, Options{})
	if report.State != StateReady {
		t.Fatalf("a healthy box is %s: %v", report.State, codesOf(report))
	}
	if report.Hop == nil || report.Hop.From.Codename != "bullseye" || report.Hop.To.Codename != "bookworm" {
		t.Fatalf("hop = %+v", report.Hop)
	}
	var infos []string
	for _, finding := range report.Checks {
		if finding.Data["unknown"] == true {
			t.Errorf("%s did not know: %v", finding.Code, finding.Data)
		}
		if finding.Severity == Info {
			infos = append(infos, finding.Code)
		}
	}
	slices.Sort(infos)
	if want := []string{"network_names", "snapshot_reminder", "sources_network_checked_later"}; !slices.Equal(infos, want) {
		t.Errorf("notes = %v, want %v", infos, want)
	}
	// every check of the table that is of this hop gave one finding, and none gave two
	for _, code := range theCodes {
		count := 0
		for _, finding := range report.Checks {
			if finding.Code == code {
				count++
			}
		}
		if want := map[bool]int{true: 0, false: 1}[slices.Contains(notOfDebian11, code)]; count != want {
			t.Errorf("%s has %d findings on Debian 11, want %d", code, count, want)
		}
	}
	if !strings.Contains(Text(report), "Debian 12 (bookworm)") {
		t.Errorf("text: %s", Text(report))
	}
}

// Every hop of the table has a healthy box too: nothing refuses it, and no check of it is unable to say.
func TestAHealthyBoxOfEveryHopIsNotBlocked(t *testing.T) {
	for name, setup := range map[string]func(*fakeProbe){"Debian 12": debian12, "Ubuntu 22.04": ubuntu2204, "Ubuntu 24.04": ubuntu2404} {
		t.Run(name, func(t *testing.T) {
			f := newBox(t)
			setup(f)
			report := Run(context.Background(), f, Options{})
			if report.State == StateBlocked || report.State == StateUnsupported {
				t.Fatalf("state = %s: %v", report.State, codesOf(report))
			}
			for _, finding := range report.Checks {
				if finding.Data["unknown"] == true {
					t.Errorf("%s did not know: %v", finding.Code, finding.Data)
				}
			}
		})
	}
}

// Docker's source is on every real machine of ReCasaOS, and is not a reason to look at anything: a box that has it is as
// ready as the one that has none, and says it as a note.
func TestAHealthyBoxThatHasDockersSourceIsAsReadyAsOneThatHasNone(t *testing.T) {
	for name, setup := range map[string]func(*fakeProbe){"Debian 11": func(*fakeProbe) {}, "Debian 12": debian12, "Ubuntu 22.04": ubuntu2204, "Ubuntu 24.04": ubuntu2404} {
		t.Run(name, func(t *testing.T) {
			f := newBox(t)
			setup(f)
			without := Run(context.Background(), f, Options{})
			sourcesFile("docker.list", dockerLine)(f)
			with := Run(context.Background(), f, Options{})
			if got := findingOf(t, with, "sources_third_party"); got.Severity != Info || got.Data["docker_files"] == nil {
				t.Fatalf("sources_third_party = %+v", got)
			}
			if with.State != without.State || with.Counts.Warn != without.Counts.Warn || with.Counts.Refuse != without.Counts.Refuse {
				t.Errorf("with Docker's source: %s %+v (%v), without: %s %+v", with.State, with.Counts, codesOf(with), without.State, without.Counts)
			}
			if name == "Debian 11" && with.State != StateReady {
				t.Errorf("state = %s: %v", with.State, codesOf(with))
			}
		})
	}
}

// dirty is a machine that has something wrong with it in most of the ways the checks look for, so
// that the checks go through their refusals as well as their passes.
func dirty(f *fakeProbe) {
	f.busyReason = "another package manager holds dpkg's lock"
	f.apps = []string{"jellyfin"}
	f.set("/var/run/reboot-required", "")
	f.set("/var/lib/dpkg/updates/0001", "")
	f.on("The following packages are only half installed", 0, argvAudit...)
	f.on("E: Unmet dependencies. Try 'apt --fix-broken install'", 100, aptCheckArgs...)
	f.set("/etc/apt/sources.list", "deb cdrom:[Debian 11]/ bullseye main\ndeb http://deb.debian.org/debian stable main\ndeb http://deb.debian.org/debian bullseye-proposed-updates main\ndeb http://deb.debian.org/debian bullseye-backports main non-free\nthis is not a line\n")
	f.set("/etc/apt/sources.list.d/docker.list", "deb [arch=amd64] https://download.docker.com/linux/debian bullseye stable\n")
	f.set("/etc/apt/preferences.d/pin", "Package: *\nPin: release a=bullseye-backports\nPin-Priority: 900\n")
	f.on("Dir::Cache \"x\";\nAPT::Default-Release \"bullseye\";\n", 0, argvConfigDump...)
	f.on("5.15.0\n", 0, "uname", "-r")
	f.set("/boot/vmlinuz-5.10.0-99-amd64", "kernel")
	f.set("/proc/uptime", "99999999.00 1.00\n")
	f.on("docker\n", 0, argvContainer...)
	f.on("xen\n", 0, argvVM...)
	f.set("/proc/1/comm", "init\n")
	f.euid = 1000
	f.on("ii \tpostgresql-13\tamd64\t13.14\nii \tpostgresql-client-13\tamd64\t13.14\n", 0, argvQuery(hostServicePatterns...)...)
	f.on(pkgArch("libc6", "i386", "2.31")+pkg("libc6", "2.31")+held("libc6")+held("nfs-common")+held("docker-ce"), 0, argvQuery()...)
	f.on("i386\n", 0, argvForeign...)
	f.on("zfs/2.0.3, 5.10.0-35-amd64, x86_64: installed\n", 0, "dkms", "status")

	// the disks and the start
	f.remove("/bin")
	f.set("/bin/sh", "x")
	f.installed("", bootloaderPackages...)
	f.set("/proc/mdstat", "Personalities : [raid1]\nmd0 : active raid1 sda1[0]\n      1046528 blocks super 1.2 [2/1] [U_]\n\nunused devices: <none>\n")
	f.on(smartOf("FAILED!"), 8, argvSmart("/dev/sda")...)
	f.set(fstabPath, "UUID=gone /data ext4 defaults 0 2\nnas:/export /home/alice/nas nfs defaults 0 0\n")
	f.set("/proc/self/mountinfo", rootMountInfo+"30 22 0:30 / /var/log rw - tmpfs tmpfs rw\n")
	f.free["/"], f.free["/usr"], f.free["/boot"], f.free["/var/cache/apt/archives"] = 1<<20, 1<<20, 1<<20, 1<<20
	f.set("/proc/meminfo", "MemTotal: 500000 kB\nSwapTotal: 0 kB\n")
	f.set("/sys/class/power_supply/BAT0/type", "Battery\n")
	f.set("/sys/class/power_supply/BAT0/status", "Discharging\n")
	f.set("/sys/class/power_supply/BAT0/capacity", "20\n")
	defaultRoute("wlp2s0")(f)
	card("wlp2s0", wifiCard)(f)

	// Docker, and the apps
	f.on(daemonInfo("active"), 0, argvDockerInfo...)
	engineOf("docker.io", "20.10.24+dfsg1-1+b3", "docker.io:\n  Installed: 20.10.24+dfsg1-1+b3\n  Candidate: 20.10.25+dfsg1-1\n")(f)
	running(inspected("web", "no", false, "80/tcp:80"), inspected("dns", "always", true))(f)
	f.remove(backupLog)
	f.on("tmpfs\n", 0, argvCgroup...)
}

// dirtyBoxes are the machines the whole table is run over, to see what it asks of them, and what it
// finds on them: each of them is dirty in its own way, so that every path of every check is taken.
var dirtyBoxes = map[string]func(*fakeProbe){
	"a clean Debian 11":    nil,
	"a dirty Debian 11":    dirty,
	"a clean Debian 12":    debian12,
	"a clean Ubuntu 22.04": ubuntu2204,
	"a clean Ubuntu 24.04": ubuntu2404,
	"a dirty Debian 12":    func(f *fakeProbe) { debian12(f); dirty(f) },
	"a dirty Ubuntu 24.04": func(f *fakeProbe) { ubuntu2404(f); dirty(f) },
	"a dirty UEFI box with a small ESP and a small /boot": func(f *fakeProbe) {
		debian12(f)
		dirty(f)
		uefi(f)
		espMounted(f)
		f.installed(pkg("grub-efi-amd64", "2.06"), bootloaderPackages...)
		f.free["/boot/efi"] = 1 << 20
		f.set("/proc/self/mountinfo", rootMountInfo+"30 22 8:15 / /boot/efi rw - vfat /dev/sda15 rw\n")
	},
	"a BIOS box with a grub-pc that has no disk": func(f *fakeProbe) {
		grubPC("", "")(f)
	},
	"a box with an encrypted root the kernel names /dev/root": func(f *fakeProbe) {
		rootOn("/dev/root")(f)
		f.on("/dev/mapper/cryptroot[/@]\n", 0, "findmnt", "-n", "-o", "SOURCE", "/")
		f.on(lsblkOf("/dev/mapper/cryptroot:crypt", "/dev/sda3:part", "/dev/sda:disk"), 0, argvLsblk("/dev/mapper/cryptroot")...)
		f.on(smartOf("PASSED"), 0, argvSmart("/dev/sda")...)
	},
	"a box whose sources are those of a disc, of aliases, of the proposed updates and of backports": func(f *fakeProbe) {
		f.set("/etc/apt/sources.list", "deb cdrom:[Debian 11]/ bullseye main\ndeb http://deb.debian.org/debian stable main\ndeb http://deb.debian.org/debian bullseye-proposed-updates main\ndeb http://deb.debian.org/debian bullseye-backports main non-free\n")
		f.installed(pkg("firmware-linux-nonfree", "20210315-3"), firmwarePatterns...)
		f.installed(pkg("debian-archive-keyring", "2019.1"), "debian-archive-keyring")
	},
	"a system that is not Debian": func(f *fakeProbe) { f.set("/etc/os-release", "ID=fedora\nVERSION_ID=\"41\"\n") },
	"a Raspberry Pi OS":           func(f *fakeProbe) { f.set("/etc/rpi-issue", "Raspberry Pi reference 2024-03-15") },
	"an arm64 machine":            func(f *fakeProbe) { f.arch = "arm64" },
	"a box on ZFS": func(f *fakeProbe) {
		f.set("/proc/self/mountinfo", "22 1 0:25 / / rw - zfs rpool/ROOT/debian rw\n")
	},
	"a box that has Docker from the snap, and a stopped daemon": func(f *fakeProbe) {
		noEngine(f)
		snapdService("active")(f)
		f.on(snapListsDocker, 0, "snap", "list", "docker")
		f.on("", 1, argvDockerInfo...)
	},
	"a box with no Docker at all": func(f *fakeProbe) {
		noEngine(f)
		f.onError(ErrToolMissing, argvDockerInfo...)
	},
}

func TestNoCheckOfTheTableAsksForWhatIsNotAllowedOrCouldChangeTheMachine(t *testing.T) {
	for name, setup := range dirtyBoxes {
		t.Run(name, func(t *testing.T) {
			f := newBox(t)
			if setup != nil {
				setup(f)
			}
			report := Run(context.Background(), f, Options{})
			for _, finding := range report.Checks {
				if finding.Data["reason"] == ReasonNotAllowed {
					t.Errorf("%s asked for something that is not allowed", finding.Code)
				}
			}
			for _, argv := range f.attemptedCommands() {
				if !Allowed(argv) {
					t.Errorf("the machine was asked to run %q", argv)
				}
			}
			assertOnlyReads(t, f)
		})
	}
}

// The judge of an upgrade asks for what the report does not: the database of Samba (listing it may touch the file that holds it),
// the sockets that listen and the units that failed. No check of the report asks for them, on any box, so that a report that is
// run on a machine that is not being upgraded never does what only the judge is for.
func TestNoCheckOfTheTableAsksForWhatOnlyTheJudgeOfAnUpgradeAsks(t *testing.T) {
	only := func(argv []string) bool {
		return argv[0] == "pdbedit" || argv[0] == "ss" || len(argv) >= 2 && argv[0] == "systemctl" && argv[1] == "--failed"
	}
	for name, setup := range dirtyBoxes {
		f := newBox(t)
		if setup != nil {
			setup(f)
		}
		Run(context.Background(), f, Options{})
		for _, argv := range f.attemptedCommands() {
			if only(argv) {
				t.Errorf("%s: the report asked %q", name, argv)
			}
		}
	}
	f := newBox(t)
	Run(context.Background(), f, Options{})
	if len(f.attemptedCommands()) == 0 {
		t.Fatal("the report asked nothing of the box: the test sees nothing")
	}
	if !only([]string{"pdbedit", "-L"}) || !only([]string{"ss", "-Hltn"}) || !only([]string{"systemctl", "--failed", "--no-legend", "--plain"}) || only([]string{"systemctl", "is-active", "x"}) {
		t.Error("the test does not know the commands of the judge")
	}
}

// Every check of the table can say something on some machine: none is left that always passes
// because of how it was wired (a hop it never runs on, a command it never gets to).
func TestEveryCheckOfTheTableFindsSomethingOnSomeBox(t *testing.T) {
	found := map[string]bool{}
	for _, setup := range dirtyBoxes {
		f := newBox(t)
		if setup != nil {
			setup(f)
		}
		for _, finding := range Run(context.Background(), f, Options{}).Checks {
			if finding.Severity != OK {
				found[finding.Code] = true
			}
		}
	}
	for _, code := range theCodes {
		if !found[code] {
			t.Errorf("%s finds nothing on any of the boxes", code)
		}
	}
}

// What a report of a dirty box is, in the words of the owner: every finding is there, in the
// section of its severity, by its code.
func TestTheTextOfADirtyReportHasEveryBlockerAndEveryWarning(t *testing.T) {
	f := newBox(t)
	dirty(f)
	report := Run(context.Background(), f, Options{})
	text := Text(report)
	if len(text) > MaxTextBytes {
		t.Fatalf("the text is %d bytes", len(text))
	}
	section := func(heading, next string) string {
		_, rest, _ := strings.Cut(text, heading+"\n")
		body, _, _ := strings.Cut(rest, next)
		return body
	}
	blockers := section("BLOCKERS", "\nWARNINGS\n")
	for _, finding := range report.Checks {
		if finding.Severity == Refuse && !strings.Contains(blockers, "["+finding.Code+"] ") {
			t.Errorf("the blocker %s is not in the text:\n%s", finding.Code, text)
		}
	}
	if report.Counts.Refuse < 20 {
		t.Errorf("the dirty box has only %d blockers: %v", report.Counts.Refuse, codesOf(report))
	}
	if !strings.Contains(text, "Result: Not ready: ") || !strings.Contains(text, fmt.Sprintf("Blockers %d, warnings %d", report.Counts.Refuse, report.Counts.Warn)) {
		t.Errorf("the counts of the text:\n%s", text[:400])
	}
	if !strings.HasSuffix(text, "This check changes nothing on the machine.\n") {
		t.Errorf("the text has no end:\n%s", text[len(text)-200:])
	}
}

// The report of a healthy box is short enough to be pasted whole, with every check in it by its code.
func TestTheTextOfAHealthyReportIsWholeAndNamesEveryCheck(t *testing.T) {
	report := Run(context.Background(), newBox(t), Options{})
	text := Text(report)
	if strings.Contains(text, "the rest is on the page") || len(text) > MaxTextBytes/2 {
		t.Fatalf("the text of a healthy box is %d bytes: %s", len(text), text)
	}
	for _, finding := range report.Checks {
		if !strings.Contains(text, finding.Code) {
			t.Errorf("%s is not in the text", finding.Code)
		}
	}
}

// assertOnlyReads is what the recording fake saw of a report: nothing but reads, of paths that are
// paths, whatever the machine put in the places they are made of.
func assertOnlyReads(t *testing.T, f *fakeProbe) {
	t.Helper()
	for _, op := range f.readPaths() {
		verb, place, _ := strings.Cut(op, " ")
		if !slices.Contains([]string{"read", "readdir", "stat", "readlink", "free", "busy", "apps"}, verb) {
			t.Errorf("%q is not a way of reading", op)
		}
		if verb == "busy" || verb == "apps" {
			continue
		}
		if !strings.HasPrefix(place, "/") || strings.ContainsAny(place, "\x00\n\r") || strings.Contains(place, "//") || slices.Contains(strings.Split(place, "/"), "..") || slices.Contains(strings.Split(place, "/"), ".") {
			t.Errorf("%q is not a path that is asked of a machine", op)
		}
	}
}

// A report is pasted into a forum: nothing of who owns the machine is in it, wherever the machine
// said it (a share's host and user in fstab, a user's directory, a container's image, the addresses
// of the network).
func TestTheTextOfADirtyBoxHasNothingOfWhoOwnsIt(t *testing.T) {
	f := newBox(t)
	dirty(f)
	f.set(fstabPath, "UUID=gone /home/alice/data ext4 defaults 0 2\n//bob:hunter2@192.168.1.5/share /media/carol/nas cifs defaults 0 0\nnas.home.lan:/export /mnt/nas nfs defaults 0 0\n")
	f.set("/etc/apt/sources.list.d/private.list", "deb https://dave:secret@apt.internal.example:8443/debian bullseye main\n")
	report := Run(context.Background(), f, Options{})
	answer, err := json.Marshal(NewAnswer(report))
	if err != nil {
		t.Fatal(err)
	}
	text := Text(report)
	for _, secret := range []string{"alice", "bob", "hunter2", "carol", "dave", "secret", "192.168", "aa:bb:cc", "nas.home.lan", "8443"} {
		if strings.Contains(text, secret) {
			t.Errorf("the text has %q:\n%s", secret, text)
		}
		if strings.Contains(string(answer), secret) && secret != "8443" {
			t.Errorf("the answer has %q", secret)
		}
	}
}

// The checks that ask for a tool that builds apt's caches in memory, or that wakes a disk, are given
// the time such a tool takes on a slow machine (an SD card, a spinning disk): a check that is told to
// stop is a refusal, so a time that is too short is a machine that is refused for being slow.
func TestTheChecksThatAskSlowToolsHaveTimeForThem(t *testing.T) {
	for code, atLeast := range map[string]time.Duration{
		"broken_dpkg": 15 * time.Second, "docker_origin": 12 * time.Second, "docker_update_pending": 12 * time.Second, "smart_failed": 12 * time.Second,
		"bootloader": 8 * time.Second, "docker_swarm": 8 * time.Second,
	} {
		if got := checkNamed(t, code).timeout(); got < atLeast {
			t.Errorf("%s has %v, want at least %v", code, got, atLeast)
		}
	}
	for _, c := range Checks() {
		if c.timeout() > DefaultBudget {
			t.Errorf("%s has %v, which is more than the report has", c.Code, c.timeout())
		}
	}
}
