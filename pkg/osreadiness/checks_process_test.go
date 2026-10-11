package osreadiness

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBusy(t *testing.T) {
	unit := func(name, state string) func(*fakeProbe) {
		return func(f *fakeProbe) { f.on(state+"\n", 0, argvUnit(name)...) }
	}
	runScenarios(t, "busy", []scenario{
		{name: "nothing runs", severity: OK},
		{name: "a maintenance unit or dpkg's lock", setup: func(f *fakeProbe) { f.busyReason = "another package manager holds dpkg's lock" },
			severity: Refuse, data: D{"source": "maintenance"}, action: ActionWaitForPackageManager},
		{name: "the daily apt refresh runs", setup: unit("apt-daily.service", "activating"), severity: Refuse, data: D{"source": "unit", "unit": "apt-daily.service"}, action: ActionWaitForPackageManager},
		{name: "the daily unattended upgrade runs", setup: unit("apt-daily-upgrade.service", "active"), severity: Refuse, data: D{"unit": "apt-daily-upgrade.service"}, action: ActionWaitForPackageManager},
		{name: "a unit that is stopping", setup: unit("apt-daily-upgrade.service", "deactivating"), severity: Refuse},
		{name: "PackageKit runs", setup: unit("packagekit.service", "active"), severity: Refuse, data: D{"unit": "packagekit.service"}},
		{name: "a unit that has failed is not running", setup: unit("packagekit.service", "failed"), severity: OK},
		// unattended-upgrades.service is a service that waits for the shutdown, and is active on every box that has it
		{name: "unattended-upgrades.service is active on every box", setup: unit("unattended-upgrades.service", "active"), severity: OK},
		// a lock that cannot be looked at is not a lock nobody holds: the box says it could not ask
		{name: "the probe cannot say whether dpkg's lock is held", setup: func(f *fakeProbe) { f.busyErr = errors.New("the lock cannot be looked at here") }, severity: Refuse, unknown: ReasonFailed},
		{name: "a lock file that may not be opened", setup: func(f *fakeProbe) {
			f.busyErr = &fs.PathError{Op: "open", Path: "/var/lib/dpkg/lock-frontend", Err: fs.ErrPermission}
		}, severity: Refuse, unknown: ReasonUnreadable},
		{name: "the probe that does not answer in time", setup: func(f *fakeProbe) { f.busyErr = context.DeadlineExceeded }, severity: Refuse, unknown: ReasonTimeout},
		{name: "a lock that is held says so, whatever else the probe could not ask", setup: func(f *fakeProbe) {
			f.busyReason, f.busyErr = "another package manager holds dpkg's lock", errors.New("another file")
		}, severity: Refuse, data: D{"source": "maintenance"}, action: ActionWaitForPackageManager},
		{name: "systemctl that is not there", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvUnit("apt-daily.service")...) }, severity: Refuse, unknown: ReasonToolMissing},
		{name: "systemctl that fails", setup: func(f *fakeProbe) { f.on("", 1, argvUnit("packagekit.service")...) }, severity: Refuse, unknown: ReasonFailed},
		{name: "systemctl that does not answer", setup: func(f *fakeProbe) { f.onError(context.DeadlineExceeded, argvUnit("apt-daily-upgrade.service")...) }, severity: Refuse, unknown: ReasonTimeout},
	})
}

func TestApps(t *testing.T) {
	runScenarios(t, "apps", []scenario{
		{name: "no app has an operation", severity: OK},
		{name: "an app is being backed up", setup: func(f *fakeProbe) { f.apps = []string{"jellyfin"} }, severity: Refuse, data: D{"apps": []string{"jellyfin"}, "count": 1}, action: ActionWaitForApps},
		{name: "several, once each and sorted, and the names that are not names left out", setup: func(f *fakeProbe) {
			f.apps = []string{"nextcloud", "jellyfin", "nextcloud", "<img src=x>", "a b"}
		}, severity: Refuse, data: D{"apps": []string{"jellyfin", "nextcloud"}, "count": 5}},
		{name: "a name that the sanitizing of data would let through and that is not an app's", setup: func(f *fakeProbe) { f.apps = []string{"../etc/passwd", "a:b", "ok-app"} }, severity: Refuse, data: D{"apps": []string{"ok-app"}, "count": 3}},
		{name: "AppManagement that cannot be asked", setup: func(f *fakeProbe) { f.appsErr = errors.New("the box cannot ask AppManagement") }, severity: Refuse, unknown: ReasonFailed},
		{name: "AppManagement that does not answer in time", setup: func(f *fakeProbe) { f.appsErr = context.DeadlineExceeded }, severity: Refuse, unknown: ReasonTimeout},
	})
}

func TestRebootFirst(t *testing.T) {
	boot := func(names ...string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			f.remove("/boot/vmlinuz-5.10.0-35-amd64", "/boot/vmlinuz-5.10.0-34-amd64")
			for _, name := range names {
				f.set("/boot/"+name, "kernel")
			}
		}
	}
	runScenarios(t, "reboot_first", []scenario{
		{name: "the running kernel is the newest", severity: OK},
		{name: "a newer kernel is installed", setup: boot("vmlinuz-5.10.0-35-amd64", "vmlinuz-5.10.0-36-amd64"), severity: Refuse,
			data: D{"running": "5.10.0-35-amd64", "installed": "5.10.0-36-amd64", "restart_requested": false}, action: ActionRestartMachine},
		{name: "the newest of several", setup: boot("vmlinuz-5.10.0-35-amd64", "vmlinuz-5.10.0-36-amd64", "vmlinuz-5.10.0-37-amd64", "vmlinuz-5.10.0-34-amd64"), severity: Refuse,
			data: D{"installed": "5.10.0-37-amd64"}},
		{name: "by number, not by letter: 100 is newer than 35", setup: boot("vmlinuz-5.10.0-35-amd64", "vmlinuz-5.10.0-100-amd64"), severity: Refuse, data: D{"installed": "5.10.0-100-amd64"}},
		{name: "the newest is not the last by name", setup: boot("vmlinuz-5.10.0-35-amd64", "vmlinuz-5.10.0-100-amd64", "vmlinuz-5.10.0-36-amd64"), severity: Refuse, data: D{"installed": "5.10.0-100-amd64"}},
		{name: "a newer version of the kernel itself", setup: boot("vmlinuz-5.10.0-35-amd64", "vmlinuz-5.11.0-1-amd64"), severity: Refuse},
		{name: "an older one is not newer", setup: boot("vmlinuz-5.10.0-35-amd64", "vmlinuz-5.9.0-99-amd64", "vmlinuz-4.19.0-1-amd64"), severity: OK},
		{name: "a newer kernel of another flavour is not this one's", setup: boot("vmlinuz-5.10.0-35-amd64", "vmlinuz-5.10.0-40-cloud-amd64"), severity: OK},
		{name: "an initrd is not a kernel", setup: boot("vmlinuz-5.10.0-35-amd64", "initrd.img-5.10.0-99-amd64"), severity: OK},
		{name: "an update asked for a restart", setup: func(f *fakeProbe) { f.set("/var/run/reboot-required", "*** System restart required ***") }, severity: Refuse, data: D{"restart_requested": true}, action: ActionRestartMachine},
		{name: "the same, in /run", setup: func(f *fakeProbe) { f.set("/run/reboot-required", "") }, severity: Refuse, data: D{"restart_requested": true}},
		{name: "a kernel that is not named like the distribution's is not compared", setup: func(f *fakeProbe) {
			f.set("/proc/sys/kernel/osrelease", "5.15.0-custom\n")
			f.set("/boot/vmlinuz-5.10.0-99-amd64", "k")
		}, severity: OK},
		{name: "...but the flag of the update still counts", setup: func(f *fakeProbe) {
			f.set("/proc/sys/kernel/osrelease", "6.1-custom\n")
			f.set("/var/run/reboot-required", "")
		}, severity: Refuse},
		{name: "no /boot", setup: func(f *fakeProbe) { f.remove("/boot/vmlinuz-5.10.0-35-amd64", "/boot/vmlinuz-5.10.0-34-amd64") }, severity: OK},
		{name: "a /boot that cannot be listed", setup: func(f *fakeProbe) { f.fail("/boot", &fs.PathError{Op: "open", Path: "/boot", Err: fs.ErrPermission}) }, severity: Refuse, unknown: ReasonUnreadable},
		{name: "a running kernel that cannot be read", setup: func(f *fakeProbe) { f.remove("/proc/sys/kernel/osrelease") }, severity: Refuse, unknown: ReasonMissing},
		{name: "a flag that cannot be looked at", setup: func(f *fakeProbe) {
			f.fail("/var/run/reboot-required", &fs.PathError{Op: "stat", Path: "x", Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
	})
}

func TestUptimeLong(t *testing.T) {
	uptime := func(text string) func(*fakeProbe) { return func(f *fakeProbe) { f.set("/proc/uptime", text) } }
	runScenarios(t, "uptime_long", []scenario{
		{name: "a day", severity: OK},
		{name: "a hundred days", setup: uptime("8640000.00 100.00\n"), severity: OK},
		{name: "exactly 180 days is not more", setup: uptime("15552000.00 1.00\n"), severity: OK},
		{name: "181 days", setup: uptime("15638400.00 1.00\n"), severity: Warn, data: D{"days": 181}, action: ActionRestartMachine},
		{name: "two years", setup: uptime("63072000.55 1.00\n"), severity: Warn, data: D{"days": 730}},
		{name: "a file that says nothing", setup: uptime("\n"), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a file that says something else", setup: uptime("soon\n"), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a negative uptime", setup: uptime("-5 1\n"), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "no /proc/uptime", setup: func(f *fakeProbe) { f.remove("/proc/uptime") }, severity: Refuse, unknown: ReasonMissing},
	})
}

func TestBrokenDpkg(t *testing.T) {
	runScenarios(t, "broken_dpkg", []scenario{
		{name: "dpkg is clean", severity: OK},
		{name: "dpkg --audit prints", setup: func(f *fakeProbe) {
			f.on("The following packages are only half configured, probably due to problems\nconfiguring them the first time.\n libc6 2.31-13\n", 0, argvAudit...)
		}, severity: Refuse, data: D{"audit": true}, action: ActionFinishDpkg},
		{name: "a file of digits in dpkg's journal", setup: func(f *fakeProbe) { f.set("/var/lib/dpkg/updates/0001", "") }, severity: Refuse, data: D{"updates": 1}, action: ActionFinishDpkg},
		{name: "two", setup: func(f *fakeProbe) {
			f.set("/var/lib/dpkg/updates/0001", "")
			f.set("/var/lib/dpkg/updates/0002", "")
		}, severity: Refuse, data: D{"updates": 2}},
		{name: "a file of the journal that is not digits is not an operation", setup: func(f *fakeProbe) {
			f.set("/var/lib/dpkg/updates/tmp.i", "")
			f.set("/var/lib/dpkg/updates/0001x", "")
		}, severity: OK},
		{name: "no journal directory", setup: func(f *fakeProbe) { f.remove("/var/lib/dpkg/updates") }, severity: OK},
		{name: "apt-get check finds broken dependencies", setup: func(f *fakeProbe) {
			f.on("Reading package lists...\nThe following packages have unmet dependencies:\n foo : Depends: bar but it is not installed\nE: Unmet dependencies. Try 'apt --fix-broken install' with no packages (or specify a solution).\n", 100, aptCheckArgs...)
		}, severity: Refuse, data: D{"apt_check": true}, action: ActionFinishDpkg},
		{name: "all three", setup: func(f *fakeProbe) {
			f.on("half installed\n", 0, argvAudit...)
			f.set("/var/lib/dpkg/updates/0001", "")
			f.on("E: Unmet dependencies.\n", 100, aptCheckArgs...)
		}, severity: Refuse, data: D{"audit": true, "updates": 1, "apt_check": true}},
		{name: "apt-get check failing for the sources is not a statement about dpkg", setup: func(f *fakeProbe) {
			f.on("E: Malformed entry 1 in list file /etc/apt/sources.list (Component)\nE: The list of sources could not be read.\n", 100, aptCheckArgs...)
		}, severity: Refuse, unknown: ReasonUnexpectedOutput},
		// what the audit and the journal found is said whatever becomes of apt-get check: it is the owner's way
		// to the repair, and the check says that apt-get check could not be read
		{name: "dpkg --audit prints, and apt-get check fails for the sources", setup: func(f *fakeProbe) {
			f.on("libc6 2.31-13 half configured\n", 0, argvAudit...)
			f.on("E: Malformed entry 1 in list file /etc/apt/sources.list (Component)\n", 100, aptCheckArgs...)
		}, severity: Refuse, data: D{"audit": true}, action: ActionFinishDpkg},
		{name: "the journal has an operation, and apt-get check does not answer", setup: func(f *fakeProbe) {
			f.set("/var/lib/dpkg/updates/0001", "")
			f.onError(context.DeadlineExceeded, aptCheckArgs...)
		}, severity: Refuse, data: D{"updates": 1}, action: ActionFinishDpkg},
		{name: "the audit prints, and there is no apt-get", setup: func(f *fakeProbe) {
			f.on("libc6 half configured\n", 0, argvAudit...)
			f.onError(ErrToolMissing, aptCheckArgs...)
		}, severity: Refuse, data: D{"audit": true}, action: ActionFinishDpkg},
		{name: "apt-get check that fails for the lists, and nothing else is wrong", setup: func(f *fakeProbe) {
			f.on("E: Could not open file /var/lib/apt/lists/deb.debian.org_debian_dists_bullseye_main_binary-amd64_Packages - open (2: No such file or directory)\n", 100, aptCheckArgs...)
		}, severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "dpkg --audit that fails without a word", setup: func(f *fakeProbe) { f.on("", 2, argvAudit...) }, severity: Refuse, unknown: ReasonFailed},
		{name: "no dpkg", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvAudit...) }, severity: Refuse, unknown: ReasonToolMissing},
		{name: "no apt-get", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, aptCheckArgs...) }, severity: Refuse, unknown: ReasonToolMissing},
		{name: "apt-get check that does not answer", setup: func(f *fakeProbe) { f.onError(context.DeadlineExceeded, aptCheckArgs...) }, severity: Refuse, unknown: ReasonTimeout},
		{name: "a journal that cannot be listed", setup: func(f *fakeProbe) {
			f.fail("/var/lib/dpkg/updates", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
	})
}

// holds is a box whose list of packages has the names on hold, and two that are not.
func holds(names ...string) func(*fakeProbe) {
	return func(f *fakeProbe) {
		text := pkg("coreutils", "8.32-4") + pkg("libc6-dev", "2.31-13")
		for _, name := range names {
			text += held(name)
		}
		f.on(text, 0, argvQuery()...)
	}
}

func TestHeld(t *testing.T) {
	runScenarios(t, "held", []scenario{
		{name: "nothing is on hold", setup: holds(), severity: OK},
		{name: "no package is installed that is asked for", severity: OK},
		{name: "libc6 is", setup: holds("libc6"), severity: Refuse, data: D{"packages": []string{"libc6"}, "count": 1}, action: ActionReleaseHolds},
		{name: "several, sorted", setup: holds("nfs-common", "libc6", "linux-image-amd64"), severity: Refuse, data: D{"packages": []string{"libc6", "linux-image-amd64", "nfs-common"}, "count": 3}},
		{name: "Docker's own are not a reason", setup: holds("docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin", "docker-ce-rootless-extras"), severity: OK},
		{name: "Docker's and another", setup: holds("docker-ce", "libc6", "containerd.io"), severity: Refuse, data: D{"packages": []string{"libc6"}, "count": 1}},
		{name: "docker.io of the distribution is not Docker's own", setup: holds("docker.io"), severity: Refuse, data: D{"packages": []string{"docker.io"}}},
		{name: "a package that only looks like Docker's", setup: holds("docker-ce-extra", "docker-plugin", "docker-x-plugins"), severity: Refuse},
		{name: "once for each architecture", setup: func(f *fakeProbe) {
			f.on(held("libc6")+strings.Replace(held("libc6"), "amd64", "i386", 1), 0, argvQuery()...)
		}, severity: Refuse, data: D{"packages": []string{"libc6"}, "count": 1}},
		// "apt-mark showhold" lists every package whose selection is hold, installed or not (the idiom after
		// a purge of snapd on Ubuntu Server: apt-get purge snapd, then apt-mark hold snapd), and a hold stops apt
		// from installing the package when something of the new release depends on it
		{name: "a hold on a package that is not installed is still a hold", setup: func(f *fakeProbe) {
			f.on(strings.Replace(held("snapd"), "hi ", "hn ", 1), 0, argvQuery()...)
		}, severity: Refuse, data: D{"packages": []string{"snapd"}, "count": 1}, action: ActionReleaseHolds},
		{name: "a hold on a package that has only its configuration files", setup: func(f *fakeProbe) {
			f.on(strings.Replace(held("snapd"), "hi ", "hc ", 1), 0, argvQuery()...)
		}, severity: Refuse, data: D{"packages": []string{"snapd"}}},
		{name: "a hold, whatever the state letter", setup: func(f *fakeProbe) {
			f.on(strings.Replace(held("a1"), "hi ", "hF ", 1)+strings.Replace(held("b1"), "hi ", "hH ", 1)+strings.Replace(held("c1"), "hi ", "ht ", 1), 0, argvQuery()...)
		}, severity: Refuse, data: D{"packages": []string{"a1", "b1", "c1"}, "count": 3}},
		{name: "a package that is not installed and not held", setup: func(f *fakeProbe) {
			f.on("un \tsnapd\tamd64\t\nrc \tfoo\tamd64\t1\npn \tbar\tamd64\t1\n", 0, argvQuery()...)
		}, severity: OK},
		{name: "a hold of a package with no version, as dpkg-query prints one that is not installed", setup: func(f *fakeProbe) {
			f.on("hn \tsnapd\t\t\n", 0, argvQuery()...)
		}, severity: Refuse, data: D{"packages": []string{"snapd"}}},
		{name: "a package that is installed and wanted, and not held", setup: func(f *fakeProbe) { f.on(pkg("libc6", "2.31"), 0, argvQuery()...) }, severity: OK},
		{name: "what is not a package name is not a hold", setup: holds("LIBC6", "<script>", "a b"), severity: OK},
		{name: "dpkg-query that fails", setup: func(f *fakeProbe) { f.on("dpkg-query: error", 2, argvQuery()...) }, severity: Refuse, unknown: ReasonFailed},
		{name: "no dpkg-query", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvQuery()...) }, severity: Refuse, unknown: ReasonToolMissing},
		{name: "a list that was cut may not have the hold", setup: func(f *fakeProbe) { f.onTruncated(pkg("coreutils", "8"), argvQuery()...) }, severity: Refuse, unknown: ReasonTooLarge},
	})
}

func TestHeldDocker(t *testing.T) {
	runScenarios(t, "held_docker", []scenario{
		{name: "nothing is on hold", setup: holds(), severity: OK},
		{name: "another package is", setup: holds("libc6"), severity: OK},
		{name: "Docker's engine is", setup: holds("docker-ce"), severity: Info, data: D{"packages": []string{"docker-ce"}}, action: ActionNone},
		{name: "the family, sorted", setup: holds("docker-compose-plugin", "containerd.io", "libc6", "docker-ce-cli"), severity: Info, data: D{"packages": []string{"containerd.io", "docker-ce-cli", "docker-compose-plugin"}}},
		{name: "Docker's engine on hold and not installed is still on hold", setup: func(f *fakeProbe) {
			f.on("hn \tdocker-ce\tamd64\t\n", 0, argvQuery()...)
		}, severity: Info, data: D{"packages": []string{"docker-ce"}}},
		{name: "dpkg-query that fails", setup: func(f *fakeProbe) { f.on("", 2, argvQuery()...) }, severity: Refuse, unknown: ReasonFailed},
	})
}

// A check that has what the audit and the journal found says so when apt-get check cannot be read, and says
// that it could not be read.
func TestBrokenDpkgSaysThatAptGetCheckCouldNotBeRead(t *testing.T) {
	for name, setup := range map[string]func(*fakeProbe){
		"it fails for the sources": func(f *fakeProbe) { f.on("E: The list of sources could not be read.\n", 100, aptCheckArgs...) },
		"it does not answer":       func(f *fakeProbe) { f.onError(context.DeadlineExceeded, aptCheckArgs...) },
		"it is not there":          func(f *fakeProbe) { f.onError(ErrToolMissing, aptCheckArgs...) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newBox(t)
			f.on("libc6 half configured\n", 0, argvAudit...)
			setup(f)
			got := checkOn(t, f, "broken_dpkg")
			if got.Severity != Refuse || got.Data["unknown"] != false || got.Data["audit"] != true || got.Action != ActionFinishDpkg {
				t.Fatalf("finding = %+v", got)
			}
			if !strings.Contains(got.Detail, "audit") || !strings.Contains(got.Detail, "apt-get check could not be read") {
				t.Fatalf("detail = %q", got.Detail)
			}
			if _, has := got.Data["apt_check"]; has {
				t.Fatalf("data = %v: apt-get check said nothing", got.Data)
			}
		})
	}
	// and when it says something, the detail does not say it could not
	f := newBox(t)
	f.on("libc6 half configured\n", 0, argvAudit...)
	f.on("E: Unmet dependencies.\n", 100, aptCheckArgs...)
	if got := checkOn(t, f, "broken_dpkg"); strings.Contains(got.Detail, "could not be read") || got.Data["apt_check"] != true {
		t.Fatalf("finding = %+v", got)
	}
}

// aptEndsAt is a machine that says the time that apt-get check was given.
type aptEndsAt struct {
	*fakeProbe
	mu       sync.Mutex
	deadline time.Time
}

func (p *aptEndsAt) Run(ctx context.Context, argv ...string) (Output, error) {
	if argv[0] == "apt-get" {
		p.mu.Lock()
		p.deadline, _ = ctx.Deadline()
		p.mu.Unlock()
	}
	return p.fakeProbe.Run(ctx, argv...)
}

// endsAfter is how long after the start the time given to apt-get check is over.
func (p *aptEndsAt) endsAfter(started time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deadline.Sub(started)
}

// apt-get check is asked with less time than the check has, so that the check is there to say what the
// audit found when apt does not answer: the end of the check's own time would turn what it found into
// "did not answer".
func TestBrokenDpkgIsStillThereWhenAptGetCheckRunsOutOfItsTime(t *testing.T) {
	f := newBox(t)
	f.on("libc6 half configured\n", 0, argvAudit...)
	f.hang(aptCheckArgs)
	check := checkNamed(t, "broken_dpkg")
	check.Timeout = 2 * time.Second
	timed := &aptEndsAt{fakeProbe: f}
	started := time.Now()
	report := Run(context.Background(), timed, Options{Checks: []Check{check}})
	got := findingOf(t, report, "broken_dpkg")
	if got.Severity != Refuse || got.Data["unknown"] != false || got.Data["audit"] != true {
		t.Fatalf("finding = %+v", got)
	}
	// apt-get was given most of the time of the check, and not all of it: an eighth of what was left stays
	// with the check
	if ends := timed.endsAfter(started); ends > 1900*time.Millisecond || ends < 1500*time.Millisecond {
		t.Fatalf("apt-get check was given until %v of the 2s of the check", ends)
	}
	// and with nothing found, it is the check that did not answer
	g := newBox(t)
	g.hang(aptCheckArgs)
	got = findingOf(t, Run(context.Background(), g, Options{Checks: []Check{check}}), "broken_dpkg")
	if got.Data["unknown"] != true || got.Data["reason"] != ReasonTimeout {
		t.Fatalf("finding = %+v", got)
	}
}
