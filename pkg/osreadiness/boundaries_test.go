package osreadiness

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// The edges of the guards of the checks: where a bound is, a value that is on it is on one side and the
// next one is on the other. The tests say the numbers, and do not take them from the constants they pin.

func TestTokensAreOfTheAlphabetAndOfTheBound(t *testing.T) {
	for in, want := range map[string]string{
		"az09._-": "az09._-", "z": "z", "9": "9", "a": "a", "0": "0", "": "", "A": "", "a b": "", "a/b": "", "a{": "", "a:": "", "a`": "", "a/": "",
		strings.Repeat("a", 64): strings.Repeat("a", 64), strings.Repeat("a", 65): "",
	} {
		if got := token(in, 64); got != want {
			t.Errorf("token(%q, 64) = %q, want %q", in, got, want)
		}
	}
	if got := token("abcdef", 5); got != "" {
		t.Errorf("a word of 6 within a bound of 5 is %q", got)
	}
	if got := token("abcde", 5); got != "abcde" {
		t.Errorf("a word of 5 within a bound of 5 is %q", got)
	}
	// a key with no value is left out, and an odd list is not read past its end
	if got := withTokens("a", "b", "c", "", "d"); len(got) != 1 || got["a"] != "b" {
		t.Errorf("withTokens = %v", got)
	}
}

func TestUptimeOfNothingIsNotLong(t *testing.T) {
	runScenarios(t, "uptime_long", []scenario{
		{name: "a machine that has just started", setup: func(f *fakeProbe) { f.set("/proc/uptime", "0.00 0.00\n") }, severity: OK},
	})
}

func TestContainersAreAskedAboutUpToTwoHundred(t *testing.T) {
	many := func(count int) func(*fakeProbe) {
		return func(f *fakeProbe) {
			lines := make([]string, count)
			for i := range lines {
				lines[i] = inspected("c"+strconv.Itoa(i), "no", false)
			}
			running(lines...)(f)
		}
	}
	runScenarios(t, "containers_policy", []scenario{
		{name: "two hundred containers", setup: many(200), severity: Warn, data: D{"count": 200}},
		{name: "two hundred and one", setup: func(f *fakeProbe) {
			var ids []string
			for i := 0; i < 201; i++ {
				ids = append(ids, fmt.Sprintf("%012x", i+1))
			}
			f.on(strings.Join(ids, "\n")+"\n", 0, argvDockerPs...)
		}, severity: Refuse, unknown: ReasonTooLarge},
	})
}

func TestABackupDateThatIsAFewHoursAheadIsAClockThatWasWrong(t *testing.T) {
	// the clock of the box is 2026-10-11 20:15:02: a day and a half ahead is not a backup
	runScenarios(t, "backup_age", []scenario{
		{name: "twenty hours ahead", setup: backups(run("2026-10-12T16:00:00Z", "")), severity: OK},
		{name: "twenty-four hours ahead", setup: backups(run("2026-10-12T20:15:02Z", "")), severity: OK},
		{name: "twenty-four hours and a half ahead", setup: backups(run("2026-10-12T20:45:00Z", "")), severity: Warn, data: D{"none": true}},
	})
}

func TestTheSourcesOfAMachineAreBoundedByWhatAMachineHas(t *testing.T) {
	files := func(count int) func(*fakeProbe) {
		return func(f *fakeProbe) {
			for i := 0; i < count; i++ {
				f.set("/etc/apt/sources.list.d/"+strconv.Itoa(i)+".list", "deb http://deb.debian.org/debian bullseye main\n")
			}
		}
	}
	runScenarios(t, "sources_cdrom", []scenario{
		// sources.list and 499 files of the directory are 500 files
		{name: "five hundred files", setup: files(499), severity: OK},
		{name: "five hundred and one", setup: files(500), severity: Refuse, unknown: ReasonTooLarge},
	})
	lines := func(count int) string {
		return strings.Repeat("deb http://deb.debian.org/debian bullseye main\n", count)
	}
	if entries, ok := parseOneLine("x.list", lines(2000)); !ok || len(entries) != 2000 {
		t.Errorf("two thousand lines: %d entries, understood %v", len(entries), ok)
	}
	if _, ok := parseOneLine("x.list", lines(2001)); ok {
		t.Error("two thousand and one lines are understood")
	}
	uris := func(count int) string {
		var out strings.Builder
		out.WriteString("Types: deb\nSuites: a\nURIs:")
		for i := 0; i < count; i++ {
			out.WriteString("\n http://x/" + strconv.Itoa(i))
		}
		return out.String() + "\n"
	}
	if entries, ok := parseDeb822("x.sources", uris(2000)); !ok || len(entries) != 2000 {
		t.Errorf("two thousand addresses: %d entries, understood %v", len(entries), ok)
	}
	if _, ok := parseDeb822("x.sources", uris(2001)); ok {
		t.Error("two thousand and one addresses are understood")
	}
	// the entries of a file are counted over its stanzas
	if _, ok := parseDeb822("x.sources", uris(1500)+"\n"+uris(501)); ok {
		t.Error("two stanzas of more than two thousand sources are understood")
	}
	if entries, ok := parseDeb822("x.sources", uris(1500)+"\n"+uris(500)); !ok || len(entries) != 2000 {
		t.Errorf("two stanzas of two thousand sources: %d entries, understood %v", len(entries), ok)
	}
}

func TestALineOfASourceNeedsAUriAndASuite(t *testing.T) {
	entries, ok := parseOneLine("x.list", "deb http://deb.debian.org/debian bullseye\n")
	if !ok || len(entries) != 1 || len(entries[0].Components) != 0 {
		t.Errorf("a line with no component: %+v, understood %v", entries, ok)
	}
	if _, ok := parseOneLine("x.list", "deb http://deb.debian.org/debian\n"); ok {
		t.Error("a line with no suite is understood")
	}
	if _, ok := parseOneLine("x.list", "deb [arch=amd64] http://deb.debian.org/debian\n"); ok {
		t.Error("a line with options, an address and no suite is understood")
	}
}

func TestAStanzaOfASourceHasOnlyFieldsThatAreFields(t *testing.T) {
	for name, text := range map[string]string{
		"a line that is no field, in a stanza that is whole": "Types: deb\nURIs: http://x\nSuites: a\nthis is not a field\n",
		"a line with no key, in a stanza that is whole":      "Types: deb\nURIs: http://x\nSuites: a\n: a value\n",
	} {
		if entries, ok := parseDeb822("x.sources", text); ok {
			t.Errorf("%s: understood %+v", name, entries)
		}
	}
}

func TestTheDataOfASourceIsOnlyWhatThereIsToSay(t *testing.T) {
	for name, tc := range map[string]struct {
		code  string
		setup func(*fakeProbe)
		keys  []string
	}{
		"a disc has no suites":             {"sources_cdrom", sourcesList("deb cdrom:[x]/ bullseye main"), []string{"suites"}},
		"a third party that has no host":   {"sources_third_party", sourcesFile("odd.list", "deb foo:bar bullseye main"), []string{"hosts", "docker_files"}},
		"a third party that is not Docker": {"sources_third_party", sourcesFile("tailscale.list", tailscaleLine), []string{"docker_files"}},
		"firmware that is not installed":   {"sources_firmware", sourcesList("deb http://deb.debian.org/debian bullseye main non-free"), []string{"packages"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newBox(t)
			tc.setup(f)
			got := checkOn(t, f, tc.code)
			if got.Severity == OK {
				t.Fatalf("finding = %+v", got)
			}
			for _, key := range tc.keys {
				if _, has := got.Data[key]; has {
					t.Errorf("data has %q: %v", key, got.Data)
				}
			}
		})
	}
}

func TestADefaultReleaseIsShownWhenItIsAName(t *testing.T) {
	release := func(name string) func(*fakeProbe) {
		return func(f *fakeProbe) { f.on("APT::Default-Release \""+name+"\";\n", 0, argvConfigDump...) }
	}
	runScenarios(t, "sources_pin", []scenario{
		{name: "a name of 64 characters", setup: release(strings.Repeat("a", 64)), severity: Refuse, data: D{"default_release": strings.Repeat("a", 64)}},
		{name: "a name of 65 characters", setup: release(strings.Repeat("a", 65)), severity: Refuse, data: D{"default_release_set": true}},
	})
	f := newBox(t)
	release(strings.Repeat("a", 65))(f)
	if got := checkOn(t, f, "sources_pin"); got.Data["default_release"] != nil {
		t.Errorf("data = %v", got.Data)
	}
}

func TestMountInfoNeedsWhatAMountHas(t *testing.T) {
	for name, tc := range map[string]struct {
		line string
		want []mount
	}{
		"a line that has superoptions":                     {"1 2 3:4 / /x rw - ext4 /dev/sda1 rw", []mount{{"3:4", "/x", "ext4", "/dev/sda1"}}},
		"a line with an optional field and no superoption": {"1 2 3:4 / /x rw a - ext4 /dev/sda1", []mount{{"3:4", "/x", "ext4", "/dev/sda1"}}},
		"a line that is a field short":                     {"21 1 8:1 / / rw - ext4 /dev/sda1", nil},
		"a dash too early":                                 {"1 2 3 / /x - a b c d e", nil},
		"no source after the type":                         {"1 2 3:4 / /x rw o:1 o:2 o:3 - ext4", nil},
		"no type":                                          {"1 2 3:4 / /x rw o:1 o:2 o:3 o:4 -", nil},
	} {
		got := parseMountInfo(tc.line)
		if len(got) != len(tc.want) || (len(got) == 1 && got[0] != tc.want[0]) {
			t.Errorf("%s: parseMountInfo(%q) = %+v, want %+v", name, tc.line, got, tc.want)
		}
	}
}

func TestDKMSModulesAreOnceEachAndLinesThatAreNotOnesAreNotModules(t *testing.T) {
	f := newBox(t)
	dkmsBox(f)
	f.on("zfs/2.0.3, 5.10.0-35-amd64, x86_64: installed\nzfs/2.0.3, 5.10.0-34-amd64, x86_64: installed\nError! Something that is not a module\n\n   \nnvidia, 470.256.02, 5.10.0-35-amd64, x86_64: installed\n", 0, "dkms", "status")
	got := checkOn(t, f, "dkms")
	if !equalLists(got.Data["modules"], []string{"nvidia", "zfs"}) {
		t.Fatalf("data = %v", got.Data)
	}
}

// A stack of cards is followed down to the sixth, and one of devices to the sixth as well; no more than 32
// are looked at. A stack that goes on past that is not looked at to its end, and the check says it could not:
// the driver that is not in the part that was seen may be the one that is built from source.
func TestTheStackOfADeviceIsFollowedToTheSixthAndNoFurther(t *testing.T) {
	cards := func(driverAt int) func(*fakeProbe) {
		return func(f *fakeProbe) {
			f.set("/proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\ni0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\n")
			for i := 0; i < 12; i++ {
				f.link("/sys/class/net/i"+strconv.Itoa(i)+"/lower_i"+strconv.Itoa(i+1), "../x")
			}
			f.link("/sys/class/net/i"+strconv.Itoa(driverAt)+"/device/driver", "../../../bus/pci/drivers/r8168")
			f.on("/lib/modules/5.10.0-35-amd64/updates/dkms/r8168.ko\n", 0, "modinfo", "-n", "r8168")
		}
	}
	devices := func(driverAt int) func(*fakeProbe) {
		return func(f *fakeProbe) {
			f.link("/sys/dev/block/8:1", "../../devices/virtual/block/dm-0")
			for i := 0; i < 12; i++ {
				f.link("/sys/devices/virtual/block/dm-"+strconv.Itoa(i)+"/slaves/dm-"+strconv.Itoa(i+1), "x")
				f.link("/sys/class/block/dm-"+strconv.Itoa(i+1), "../../devices/virtual/block/dm-"+strconv.Itoa(i+1))
			}
			f.link("/sys/devices/virtual/block/dm-"+strconv.Itoa(driverAt)+"/driver", "../../../bus/x/drivers/ahci")
			f.on("/lib/modules/5.10.0-35-amd64/updates/dkms/ahci.ko\n", 0, "modinfo", "-n", "ahci")
		}
	}
	runScenarios(t, "dkms", []scenario{
		{name: "a card at the sixth step", setup: after(dkmsBox, cards(6)), severity: Refuse, data: D{"network": true}},
		{name: "a card at the seventh", setup: after(dkmsBox, cards(7)), severity: Refuse, unknown: ReasonTooLarge},
		{name: "a device at the sixth step", setup: after(dkmsBox, devices(6)), severity: Refuse, data: D{"disk": true}},
		{name: "a device at the seventh", setup: after(dkmsBox, devices(7)), severity: Refuse, unknown: ReasonTooLarge},
	})
	fan := func(driverAt int) func(*fakeProbe) {
		return func(f *fakeProbe) {
			f.set("/proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\nbond0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\n")
			for i := 1; i <= 40; i++ {
				f.link("/sys/class/net/bond0/lower_l"+fmt.Sprintf("%02d", i), "../x")
			}
			f.link("/sys/class/net/l"+fmt.Sprintf("%02d", driverAt)+"/device/driver", "../../../bus/pci/drivers/r8168")
			f.on("/lib/modules/5.10.0-35-amd64/updates/dkms/r8168.ko\n", 0, "modinfo", "-n", "r8168")
		}
	}
	slaves := func(driverAt int) func(*fakeProbe) {
		return func(f *fakeProbe) {
			f.link("/sys/dev/block/8:1", "../../devices/virtual/block/md0")
			for i := 1; i <= 40; i++ {
				name := fmt.Sprintf("s%02d", i)
				f.link("/sys/devices/virtual/block/md0/slaves/"+name, "x")
				f.link("/sys/class/block/"+name, "../../devices/pci0000:00/0000:00:"+fmt.Sprintf("%02d", i)+".0/block/"+name)
			}
			f.link("/sys/devices/pci0000:00/0000:00:"+fmt.Sprintf("%02d", driverAt)+".0/driver", "../../../bus/pci/drivers/megaraid")
			f.on("/lib/modules/5.10.0-35-amd64/updates/dkms/megaraid.ko\n", 0, "modinfo", "-n", "megaraid")
		}
	}
	runScenarios(t, "dkms", []scenario{
		{name: "the thirty-first card of a bond", setup: after(dkmsBox, fan(31)), severity: Refuse, data: D{"network": true}},
		{name: "the thirty-second", setup: after(dkmsBox, fan(32)), severity: Refuse, unknown: ReasonTooLarge},
		{name: "the thirty-first disk of a RAID", setup: after(dkmsBox, slaves(31)), severity: Refuse, data: D{"disk": true}},
		{name: "the thirty-second", setup: after(dkmsBox, slaves(32)), severity: Refuse, unknown: ReasonTooLarge},
		{name: "forty cards of a bond and none that is built from source", setup: after(dkmsBox, fan(0)), severity: Refuse, unknown: ReasonTooLarge},
		{name: "forty disks of a RAID and none that is built from source", setup: after(dkmsBox, slaves(0)), severity: Refuse, unknown: ReasonTooLarge},
	})
}

// The drivers of a device are those of every step from the bus root down: the first one under /sys/devices
// (Xen's block devices are bound at that step), and the last.
func TestTheDriversOfADeviceAreThoseOfEveryStepOfItsPath(t *testing.T) {
	bound := func(driverPath string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			f.link("/sys/dev/block/8:1", "../../devices/vbd-51712/block/xvda")
			f.link(driverPath, "../../../bus/xen/drivers/xen-blkfront")
			f.on("/lib/modules/5.10.0-35-amd64/updates/dkms/xen-blkfront.ko\n", 0, "modinfo", "-n", "xen-blkfront")
		}
	}
	runScenarios(t, "dkms", []scenario{
		{name: "a Xen disk, bound at the first step", setup: after(dkmsBox, bound("/sys/devices/vbd-51712/driver")), severity: Refuse, data: D{"disk": true}},
		{name: "a driver of the device itself", setup: after(dkmsBox, bound("/sys/devices/vbd-51712/block/xvda/driver")), severity: Refuse, data: D{"disk": true}},
		{name: "a driver of the directory above the first step is nobody's", setup: after(dkmsBox, bound("/sys/devices/driver")), severity: Warn, data: D{"disk": false}},
	})
}

func TestTheNameOfAFilesystemOfTheControlGroupsIsShownUpToThirtyTwoCharacters(t *testing.T) {
	cgroup := func(kind string) func(*fakeProbe) {
		return func(f *fakeProbe) { f.on(kind+"\n", 0, argvCgroup...) }
	}
	runScenarios(t, "cgroup1", []scenario{
		{name: "a name of 32", setup: after(debian12, cgroup(strings.Repeat("a", 32))), severity: Refuse, data: D{"filesystem": strings.Repeat("a", 32)}},
		{name: "a name of 33", setup: after(debian12, cgroup(strings.Repeat("a", 33))), severity: Refuse, data: D{}},
	})
	f := newBox(t)
	debian12(f)
	cgroup(strings.Repeat("a", 33))(f)
	if got := checkOn(t, f, "cgroup1"); got.Data["filesystem"] != nil {
		t.Errorf("data = %v", got.Data)
	}
}

// A stack of 64 devices is read, and one of 65 is read in part and is no answer.
func TestAStackIsReadUpToSixtyFourDevices(t *testing.T) {
	stack := func(count int) func(*fakeProbe) {
		return func(f *fakeProbe) {
			rows := make([]string, 0, count)
			for i := 0; i < count; i++ {
				rows = append(rows, fmt.Sprintf("/dev/mapper/v%d:lvm", i))
			}
			f.on(lsblkOf(rows...), 0, argvLsblk("/dev/sda1")...)
		}
	}
	runScenarios(t, "root_luks", []scenario{
		{name: "sixty-four", setup: stack(64), severity: OK},
		{name: "sixty-five", setup: stack(65), severity: Refuse, unknown: ReasonTooLarge},
	})
}

func TestSmartSaysWhatItDoesNotKnowAndNothingElse(t *testing.T) {
	smart := func(text string, exit int) func(*fakeProbe) {
		return func(f *fakeProbe) { f.on(text, exit, argvSmart("/dev/sda")...) }
	}
	for name, tc := range map[string]struct {
		setup func(*fakeProbe)
		why   string
		disks bool
	}{
		"smartctl that is not there":          {func(f *fakeProbe) { f.onError(ErrToolMissing, argvSmart("/dev/sda")...) }, "no_smartctl", false},
		"a root that is on no device":         {func(f *fakeProbe) { f.set("/proc/self/mountinfo", "22 1 0:40 / / rw - overlay overlay rw\n") }, "no_block_device", false},
		"a stack with no disk in it":          {func(f *fakeProbe) { f.on(lsblkOf("/dev/sda1:part", "/dev/loop0:loop"), 0, argvLsblk("/dev/sda1")...) }, "no_disk", false},
		"a disk that says nothing":            {smart("Unable to detect device type\n", 1), "unavailable", true},
		"a disk that sleeps":                  {smart(standbyText, 2), "standby", true},
		"a disk that sleeps and exits with 1": {smart(standbyText, 1), "unavailable", true},
		"a disk that sleeps and exits with 3": {smart(standbyText, 3), "standby", true},
		"more disks than are asked": {func(f *fakeProbe) {
			smartStack(f, 17, func(string) (string, int) { return smartOf("PASSED"), 0 })
		}, "partial", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newBox(t)
			tc.setup(f)
			got := checkOn(t, f, "smart_failed")
			if got.Severity != Info || got.Data["why"] != tc.why {
				t.Fatalf("finding = %+v", got)
			}
			if _, has := got.Data["disks"]; has != tc.disks {
				t.Errorf("data = %v, disks wanted: %v", got.Data, tc.disks)
			}
		})
	}
	// a name that cannot be asked about is left out, and the disks that can be are asked
	f := newBox(t)
	f.on(lsblkOf("/dev/sda1:part", "/dev/sda:disk", "/dev/sd;a:disk", "/dev/sdb c:disk"), 0, argvLsblk("/dev/sda1")...)
	f.on(smartOf("PASSED"), 0, argvSmart("/dev/sda")...)
	if got := checkOn(t, f, "smart_failed"); got.Severity != OK {
		t.Fatalf("finding = %+v", got)
	}
	if asked := smartctlAsked(f); asked != 1 {
		t.Errorf("%d disks were asked, want 1", asked)
	}
}

func TestTheLogIsOnTheMountThatIsOnTopOfTheOthersOfItsPlace(t *testing.T) {
	mounts := func(lines ...string) func(*fakeProbe) {
		return func(f *fakeProbe) { f.set("/proc/self/mountinfo", rootMountInfo+strings.Join(lines, "")) }
	}
	runScenarios(t, "log_on_tmpfs", []scenario{
		{name: "a disk mounted over a tmpfs", setup: mounts("30 22 0:30 / /var/log rw - tmpfs tmpfs rw\n", "31 22 8:3 / /var/log rw - ext4 /dev/sda3 rw\n"), severity: OK},
		{name: "a tmpfs mounted over a disk", setup: mounts("30 22 8:3 / /var/log rw - ext4 /dev/sda3 rw\n", "31 22 0:30 / /var/log rw - tmpfs tmpfs rw\n"), severity: Refuse, data: D{"mount_point": "/var/log"}},
	})
}

func TestTheFilesystemOfBootIsSaidInMebibytes(t *testing.T) {
	f := newBox(t)
	debian12(f)
	f.on("536870912\n", 0, argvSize("/boot")...)
	got := checkOn(t, f, "disk_boot")
	if !strings.Contains(got.Detail, "has 512 MiB") || !strings.Contains(got.Detail, "700 MiB") {
		t.Errorf("detail = %q", got.Detail)
	}
}

func TestMemoryIsReadWithTheBoundsOfAMachine(t *testing.T) {
	for _, tc := range []struct {
		text  string
		value uint64
		err   bool
	}{
		{"MemTotal: 1099511627776 kB\n", 1 << 40, false},
		{"MemTotal: 1099511627777 kB\n", 0, true},
		{"MemTotal: 5 kB\n", 5, false},
		{"MemTotal: 5\n", 5, false},
		{"MemTotal: 5 kB kB\n", 0, true},
		{"MemTotal: 5 MB\n", 0, true},
		{"MemTotal:\n", 0, true},
	} {
		value, found, err := meminfoKiB(tc.text, "MemTotal")
		if (err != nil) != tc.err || (err == nil && (!found || value != tc.value)) {
			t.Errorf("meminfoKiB(%q) = %d, %v, %v", tc.text, value, found, err)
		}
	}
	memory := func(memKiB, swapKiB string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			f.set("/proc/meminfo", "MemTotal:       "+memKiB+" kB\nMemFree:          1 kB\nSwapTotal:      "+swapKiB+" kB\n")
		}
	}
	runScenarios(t, "ram", []scenario{
		{name: "a memory that is said in mebibytes", setup: memory("1500000", "0"), severity: Warn, data: D{"ram_mib": 1464, "swap_mib": 0, "total_mib": 1464}},
		{name: "a swap that is said in mebibytes", setup: memory("1000000", "300000"), severity: Warn, data: D{"ram_mib": 976, "swap_mib": 292, "total_mib": 1269}},
		{name: "a memory of a petabyte is said to be unreadable", setup: memory("1099511627777", "0"), severity: Refuse, unknown: ReasonUnexpectedOutput},
	})
}

func TestABatteryAtNothingIsTheLowestOfBatteries(t *testing.T) {
	battery := func(status, capacity string) func(*fakeProbe) {
		return func(f *fakeProbe) {
			f.set(powerSupplyDir+"/BAT0/type", "Battery\n")
			f.set(powerSupplyDir+"/BAT0/status", status+"\n")
			f.set(powerSupplyDir+"/BAT0/capacity", capacity+"\n")
		}
	}
	runScenarios(t, "power", []scenario{
		{name: "discharging at 0", setup: battery("Discharging", "0"), severity: Refuse, data: D{"percent": 0, "status": "discharging"}},
		{name: "charging at 0", setup: battery("Charging", "0"), severity: Refuse, data: D{"percent": 0}},
		{name: "full at 100", setup: battery("Full", "100"), severity: OK},
		{name: "discharging at 100", setup: battery("Discharging", "100"), severity: Refuse, data: D{"percent": 100}},
		{name: "a status of 32 characters", setup: battery(strings.Repeat("a", 32), "10"), severity: Refuse, data: D{"status": strings.Repeat("a", 32)}},
		{name: "a status of 33 characters", setup: battery(strings.Repeat("a", 33), "10"), severity: Refuse, data: D{}},
	})
	f := newBox(t)
	f.set(powerSupplyDir+"/BAT0/type", "Battery\n")
	f.set(powerSupplyDir+"/BAT0/status", strings.Repeat("a", 33)+"\n")
	f.set(powerSupplyDir+"/BAT0/capacity", "10\n")
	if got := checkOn(t, f, "power"); got.Data["status"] != nil {
		t.Errorf("data = %v", got.Data)
	}
}
