package osreadiness

import (
	"context"
	"errors"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The checks of the disks and of what starts the machine: its directories, its boot loader, the
// stack its root is on, and the room and the power an upgrade needs.
func init() {
	register(
		Check{Code: "merged_usr", Group: GroupFilesystem, Title: "/bin, /sbin and /lib are links into /usr", Run: checkMergedUsr},
		Check{Code: "cgroup1", Group: GroupFilesystem, Title: "The control groups are of version 2", Hops: []HopID{HopDebian12to13, HopUbuntu2404to2604}, Run: checkCgroup},
		Check{Code: "bootloader", Group: GroupFilesystem, Title: "The boot loader is GRUB, and can be updated", Timeout: 8 * time.Second, Run: checkBootloader},
		Check{Code: "root_zfs", Group: GroupFilesystem, Title: "The system is not on ZFS", Run: checkRootZFS},
		Check{Code: "root_luks", Group: GroupFilesystem, Title: "The system is not on an encrypted disk", Timeout: 8 * time.Second, Run: checkRootLUKS},
		Check{Code: "md_degraded", Group: GroupFilesystem, Title: "No software RAID array is degraded", Run: checkMdDegraded},
		Check{Code: "smart_failed", Group: GroupFilesystem, Title: "The disk of the system is not failing", Timeout: 18 * time.Second, Run: checkSmart},
		Check{Code: "log_on_tmpfs", Group: GroupFilesystem, Title: "The log of ReCasaOS is kept on a disk", Run: checkLogOnTmpfs},
		Check{Code: "disk_root", Group: GroupFilesystem, Title: "There is room on / and /usr", Run: checkDiskRoot},
		Check{Code: "disk_archives", Group: GroupFilesystem, Title: "There is room for the packages to download", Run: checkDiskArchives},
		Check{Code: "disk_boot", Group: GroupFilesystem, Title: "There is room on /boot", Run: checkDiskBoot},
		Check{Code: "ram", Group: GroupFilesystem, Title: "There is memory for the upgrade", Run: checkRAM},
		Check{Code: "power", Group: GroupFilesystem, Title: "The machine is not running on a battery", Run: checkPower},
	)
}

const (
	mebibyte = 1 << 20
	gibibyte = 1 << 30
)

func checkMergedUsr(ctx context.Context, p Probe) Result {
	unmerged := []string{}
	for _, dir := range []string{"/bin", "/sbin", "/lib"} {
		info, err := p.Lstat(ctx, dir)
		if err != nil {
			return unknown(err)
		}
		if !info.IsSymlink() {
			unmerged = append(unmerged, dir)
			continue
		}
		target, err := p.Readlink(ctx, dir)
		if err != nil {
			return unknown(err)
		}
		if !strings.HasPrefix(target, "/") {
			target = path.Join(path.Dir(dir), target)
		}
		if path.Clean(target) != "/usr"+dir {
			unmerged = append(unmerged, dir)
		}
	}
	if len(unmerged) == 0 {
		return pass()
	}
	return Result{
		Severity: Refuse, Title: "/bin, /sbin or /lib is not a link into /usr",
		Detail: "The next release needs the merged /usr layout, where these directories are links to /usr/bin, /usr/sbin and /usr/lib. This system still has separate ones, and the upgrade does not convert it: do that first, with the usrmerge package.",
		Data:   D{"directories": unmerged},
	}
}

func checkCgroup(ctx context.Context, p Probe) Result {
	out, err := p.Run(ctx, "stat", "-fc", "%T", "/sys/fs/cgroup")
	if err != nil {
		return unknown(err)
	}
	if out.ExitCode != 0 {
		return unknownWith(ReasonFailed)
	}
	kind := firstLine(out.Text)
	if kind == "cgroup2fs" {
		return pass()
	}
	data := D{}
	if token(kind, 32) != "" {
		data["filesystem"] = kind
	}
	return Result{
		Severity: Refuse, Title: "The control groups are of version 1",
		Detail: "The next release of the system no longer runs on version 1 of the control groups, which the containers need: the machine is started with an option that keeps it (systemd.unified_cgroup_hierarchy=0 or a legacy mode), and has to be started without it first.",
		Data:   data,
	}
}

// grubPackages are the packages whose being installed says the machine boots with GRUB: the one of
// a BIOS machine, the one of a UEFI machine (signed or not), and the one of Debian's cloud images,
// which has both loaders and no grub-pc.
var grubPackages = []string{"grub-pc", "grub-efi-amd64", "grub-efi-amd64-signed", "grub-cloud-amd64"}

// bootloaderPackages are what dpkg-query is asked for: GRUB's packages, and systemd-boot's.
var bootloaderPackages = append(slices.Clone(grubPackages), "systemd-boot")

// systemdBootMarkers are the files that say systemd-boot starts the machine: its loader.conf, on the ESP
// wherever it is mounted (Debian and Ubuntu mount it on /boot/efi, systemd on /efi, and /boot can be the
// partition systemd-boot calls XBOOTLDR), and the variable that it sets in the firmware, which says what
// loader started this boot.
var systemdBootMarkers = []string{
	"/boot/loader/loader.conf", "/boot/efi/loader/loader.conf", "/efi/loader/loader.conf",
	"/sys/firmware/efi/efivars/LoaderInfo-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f",
}

const (
	grubConfig = "/boot/grub/grub.cfg"
	// espMinimum is the room the ESP needs: GRUB's files are rewritten there.
	espMinimum = 16 * mebibyte
	debconfDB  = "/var/cache/debconf/config.dat"
)

func checkBootloader(ctx context.Context, p Probe) Result {
	refuse := func(kind, title, detail string, extra D) Result {
		data := D{"kind": kind}
		for k, v := range extra {
			data[k] = v
		}
		return Result{Severity: Refuse, Title: title, Detail: detail, Data: data}
	}
	packages, err := installedPackages(ctx, p, bootloaderPackages...)
	if err != nil {
		return unknown(err)
	}
	// only the packages that were asked for count, whatever else the tool printed
	packages = slices.DeleteFunc(packages, func(pkg installedPackage) bool { return !slices.Contains(bootloaderPackages, pkg.Name) })
	// systemd-boot is what starts the machine when its package is there, or the files it has: GRUB's package
	// and its grub.cfg are left behind by the change, and are not what boots
	systemdBoot := slices.ContainsFunc(packages, func(pkg installedPackage) bool { return pkg.Name == "systemd-boot" })
	for _, marker := range systemdBootMarkers {
		found, err := exists(ctx, p, marker)
		if err != nil {
			return unknown(err)
		}
		systemdBoot = systemdBoot || found
	}
	if systemdBoot {
		return refuse("systemd_boot", "The boot loader is systemd-boot",
			"The upgrade is proven with GRUB, which Debian and Ubuntu install. This machine starts with systemd-boot, whose files the upgrade does not look after: there is no way here to know that the new kernel would be started.", nil)
	}
	packages = slices.DeleteFunc(packages, func(pkg installedPackage) bool { return !slices.Contains(grubPackages, pkg.Name) })
	config, err := exists(ctx, p, grubConfig)
	if err != nil {
		return unknown(err)
	}
	if len(packages) == 0 || !config {
		return refuse("not_grub", "The boot loader is not GRUB",
			"The upgrade is proven with GRUB, which Debian and Ubuntu install. With another boot loader (systemd-boot, rEFInd, extlinux, a board's own) there is no way here to know that the new kernel would be started.", nil)
	}
	efi, err := exists(ctx, p, "/sys/firmware/efi")
	if err != nil {
		return unknown(err)
	}
	if efi {
		return checkESP(ctx, p, refuse)
	}
	if !slices.ContainsFunc(packages, func(pkg installedPackage) bool { return pkg.Name == "grub-pc" }) {
		return pass()
	}
	return checkInstallDevices(ctx, p, refuse)
}

// checkESP: a UEFI machine's GRUB is written to the EFI system partition, which has to be mounted,
// and to have room.
func checkESP(ctx context.Context, p Probe, refuse func(kind, title, detail string, extra D) Result) Result {
	list, err := mounts(ctx, p)
	if err != nil {
		return unknown(err)
	}
	if !slices.ContainsFunc(list, func(m mount) bool { return m.Point == "/boot/efi" }) {
		return refuse("esp_not_mounted", "The EFI system partition is not mounted",
			"This machine starts with UEFI, and GRUB is updated on the EFI system partition, which is not mounted on /boot/efi: the new boot loader would be written in the wrong place, or not at all.", nil)
	}
	free, err := p.FreeBytes(ctx, "/boot/efi")
	if err != nil {
		return unknown(err)
	}
	if free < espMinimum {
		return refuse("esp_full", "The EFI system partition is nearly full",
			"GRUB's files are written again on the EFI system partition, and it has too little room left for them.",
			D{"free_mib": free / mebibyte, "needed_mib": uint64(espMinimum / mebibyte)})
	}
	return pass()
}

// checkInstallDevices: the grub-pc of a BIOS machine writes GRUB to the disks the debconf question
// grub-pc/install_devices names, and ends in error, without a terminal, when it names none or one
// that is gone. A machine that says on purpose that it installs it nowhere is let go.
func checkInstallDevices(ctx context.Context, p Probe, refuse func(kind, title, detail string, extra D) Result) Result {
	data, err := p.ReadFile(ctx, debconfDB)
	if err != nil {
		return unknown(err)
	}
	devices := []string{}
	for _, device := range strings.Split(debconfValue(string(data), "grub-pc/install_devices"), ",") {
		if device = strings.TrimSpace(device); device != "" {
			devices = append(devices, device)
		}
	}
	if len(devices) == 0 {
		if debconfValue(string(data), "grub-pc/install_devices_empty") == "true" {
			return pass()
		}
		return refuse("install_devices_empty", "GRUB has no disk to be installed on",
			"The package grub-pc is installed on a BIOS machine, and the disks it writes GRUB to are not set (grub-pc/install_devices). Without a terminal its update ends in error: it has to be set first (dpkg-reconfigure grub-pc).", nil)
	}
	for _, device := range devices {
		if !strings.HasPrefix(device, "/dev/") || strings.ContainsAny(device, "\x00\n") || strings.Contains(device, "..") {
			return refuse("install_device_missing", "A disk GRUB is installed on is not there",
				"A disk named for GRUB is not one of this machine's disks any more: the update of GRUB would end in error. Set the disks again (dpkg-reconfigure grub-pc).", D{"count": len(devices)})
		}
		_, err := p.Stat(ctx, device)
		switch {
		case isNotExist(err):
			return refuse("install_device_missing", "A disk GRUB is installed on is not there",
				"A disk named for GRUB is not one of this machine's disks any more: the update of GRUB would end in error. Set the disks again (dpkg-reconfigure grub-pc).", D{"count": len(devices)})
		case err != nil:
			return unknown(err)
		}
	}
	return pass()
}

// debconfValue is the value of a question in debconf's database (config.dat: stanzas of "Name:",
// "Template:", "Value:", separated by an empty line); "" when it has none.
func debconfValue(text, name string) string {
	for _, stanza := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		var found bool
		value := ""
		for _, line := range strings.Split(stanza, "\n") {
			switch key, rest, ok := strings.Cut(line, ":"); {
			case !ok:
			case key == "Name":
				found = strings.TrimSpace(rest) == name
			case key == "Value":
				value = strings.TrimSpace(rest)
			}
		}
		if found {
			return value
		}
	}
	return ""
}

var poolName = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,64}$`)

func checkRootZFS(ctx context.Context, p Probe) Result {
	list, err := mounts(ctx, p)
	if err != nil {
		return unknown(err)
	}
	root, ok := rootMount(list)
	if !ok {
		return unknownWith(ReasonUnexpectedOutput)
	}
	if root.FSType != "zfs" {
		return pass()
	}
	data := D{}
	if poolName.MatchString(root.Source) {
		data["dataset"] = root.Source
	}
	return Result{
		Severity: Refuse, Title: "The system is on ZFS",
		Detail: "GRUB reads only the ZFS pools that have a restricted set of features, and an upgrade of the system's ZFS can make the pool it starts from unreadable. A system on ZFS is not upgraded from here.",
		Data:   data,
	}
}

// rootDevice is the block device the root filesystem is on: "" when it is on none (a network
// filesystem, a memory one, ZFS), as the mounts say it, and as findmnt says it for the "/dev/root"
// that the kernel names a root its command line gave.
func rootDevice(ctx context.Context, p Probe) (string, error) {
	list, err := mounts(ctx, p)
	if err != nil {
		return "", err
	}
	root, ok := rootMount(list)
	if !ok {
		return "", errUnexpectedOutput
	}
	source := root.Source
	if source == "/dev/root" {
		out, err := p.Run(ctx, "findmnt", "-n", "-o", "SOURCE", "/")
		if err != nil {
			return "", err
		}
		if out.ExitCode != 0 {
			return "", errToolFailed
		}
		// a btrfs root says its subvolume, in brackets
		source, _, _ = strings.Cut(firstLine(out.Text), "[")
		source = strings.TrimSpace(source)
		// findmnt names the device the kernel's /dev/root is, or it is no answer: a line that is not a
		// device (a warning before it, nothing at all) is not a root that is on none
		if !validDevice(source) {
			return "", errUnexpectedOutput
		}
	}
	switch {
	case !strings.HasPrefix(source, "/dev/"):
		return "", nil
	case !validDevice(source):
		return "", errUnexpectedOutput
	}
	return source, nil
}

// blockDevice is a device of the stack a device is built on, and what it is (lsblk's TYPE: disk,
// part, crypt, lvm, raid1, ...).
type blockDevice struct{ Name, Type string }

const maxBlockDevices = 64

// lsblkLine is a line of "lsblk -P": the values are in quotes, and lsblk writes a quote that is in
// one as \x22, so that a value cannot end early.
var lsblkLine = regexp.MustCompile(`^NAME="([^"]*)" TYPE="([a-z0-9-]{1,16})"$`)

// blockChain is the device, and the devices under it, up to the disks.
func blockChain(ctx context.Context, p Probe, device string) ([]blockDevice, error) {
	out, err := p.Run(ctx, "lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", device)
	if err != nil {
		return nil, err
	}
	if out.ExitCode != 0 {
		return nil, errToolFailed
	}
	if out.Truncated {
		return nil, ErrTooLarge
	}
	var chain []blockDevice
	for _, line := range strings.Split(out.Text, "\n") {
		m := lsblkLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		if d := (blockDevice{m[1], m[2]}); !slices.Contains(chain, d) {
			// a stack of more devices than are read is read in part: an encrypted volume or a disk past the
			// bound would be passed over, and that is not an answer
			if len(chain) >= maxBlockDevices {
				return nil, ErrTooLarge
			}
			chain = append(chain, d)
		}
	}
	if len(chain) == 0 {
		return nil, errUnexpectedOutput
	}
	return chain, nil
}

func checkRootLUKS(ctx context.Context, p Probe) Result {
	device, err := rootDevice(ctx, p)
	if err != nil {
		return unknownFor(err)
	}
	if device == "" {
		return pass()
	}
	chain, err := blockChain(ctx, p, device)
	if err != nil {
		return unknownFor(err)
	}
	if !slices.ContainsFunc(chain, func(d blockDevice) bool { return d.Type == "crypt" }) {
		return pass()
	}
	return Result{
		Severity: Refuse, Title: "The system is on an encrypted disk",
		Detail: "A machine whose system is encrypted waits for a passphrase at its start, which a machine without a screen cannot be given, and the new release changes what its encryption needs to start. Encrypted systems are not upgraded from here.",
		Data:   D{"device": device},
	}
}

// mdArray is the line of an array in /proc/mdstat: its name, and its state ("active", "inactive", and
// "active (read-only)" or "active (auto-read-only)" for one that is only read from), then what it is made of.
// mdStatus is the line that says how many of its members work: "[2/1] [U_]".
var (
	mdArray  = regexp.MustCompile(`^(md[0-9]{1,5})\s*:\s*(\S*)`)
	mdStatus = regexp.MustCompile(`\[([0-9]{1,5})/([0-9]{1,5})\]\s*\[([U_]{1,64})\]`)
)

// degradedArrays are the arrays of /proc/mdstat that are not whole: fewer members work than the array has,
// or one of the marks is a "_", or the array is not running ("inactive": its members are there and it is
// not assembled). The container of a RAID that the firmware keeps (Intel's, DDF: "super external:") is
// inactive by design, its volumes are the arrays, and it is not one.
func degradedArrays(text string) []string {
	var degraded []string
	var current struct {
		name                 string
		notRunning, external bool
	}
	add := func(name string) {
		if !slices.Contains(degraded, name) {
			degraded = append(degraded, name)
		}
	}
	// the array that was being read is done: one that is not running is degraded unless it is a container
	finish := func() {
		if current.name != "" && current.notRunning && !current.external {
			add(current.name)
		}
		current.name = ""
	}
	for _, line := range strings.Split(text, "\n") {
		if m := mdArray.FindStringSubmatch(line); m != nil {
			finish()
			current.name, current.notRunning, current.external = m[1], m[2] != "active", false
			continue
		}
		if current.name == "" {
			continue
		}
		if strings.Contains(line, "super external:") {
			current.external = true
		}
		if m := mdStatus.FindStringSubmatch(line); m != nil {
			total, _ := strconv.Atoi(m[1])
			working, _ := strconv.Atoi(m[2])
			if working < total || strings.Contains(m[3], "_") {
				add(current.name)
			}
		}
	}
	finish()
	slices.Sort(degraded)
	return degraded
}

func checkMdDegraded(ctx context.Context, p Probe) Result {
	data, found, err := readOptional(ctx, p, "/proc/mdstat")
	if err != nil {
		return unknown(err)
	}
	if !found {
		return pass()
	}
	arrays := degradedArrays(string(data))
	if len(arrays) == 0 {
		return pass()
	}
	return Result{
		Severity: Refuse, Title: "A software RAID array is degraded",
		Detail: "An array that has lost a member is one more failure away from losing its data, and an upgrade writes a lot and ends with a restart. Replace the member and wait for the array to be whole.",
		Data:   D{"arrays": arrays},
	}
}

var (
	smartResult = regexp.MustCompile(`(?m)^SMART overall-health self-assessment test result:\s*(\S+)`)
	smartStatus = regexp.MustCompile(`(?m)^SMART Health Status:\s*(\S+)`)
)

// maxSmartDisks is how many disks are asked: a stack of more is said to have been asked in part.
const maxSmartDisks = 16

type smartState int

const (
	smartUnavailable smartState = iota
	smartPassed
	smartFailed
	// smartAsleep is a disk that sleeps, and that "-n standby" did not wake
	smartAsleep
)

// smartStandby is what smartctl says of a disk that sleeps when it was asked not to wake it: "Device is in
// STANDBY mode, exit(2)" (or SLEEP, or STANDBY_Y and STANDBY_Z), at the start of a line, with the exit status
// of "the device is in a low-power mode" (bit 1).
var smartStandby = regexp.MustCompile(`(?m)^Device is in (?:STANDBY\w*|SLEEP) mode`)

// smartHealth is what "smartctl -n standby -H" says of a disk: it passed, it failed (the text says so, or the
// exit status has its bit 3, "disk failing"), it sleeps and was not woken, or it did not say (a virtual
// disk, a controller that does not pass the questions on).
func smartHealth(out Output) smartState {
	state := smartUnavailable
	for _, re := range []*regexp.Regexp{smartResult, smartStatus} {
		if m := re.FindStringSubmatch(out.Text); m != nil {
			switch {
			case m[1] == "PASSED" || m[1] == "OK":
				state = smartPassed
			case strings.HasPrefix(m[1], "FAILED"):
				return smartFailed
			}
		}
	}
	if out.ExitCode > 0 && out.ExitCode&8 != 0 {
		return smartFailed
	}
	if state == smartUnavailable && out.ExitCode&2 != 0 && smartStandby.MatchString(out.Text) {
		return smartAsleep
	}
	return state
}

func checkSmart(ctx context.Context, p Probe) Result {
	notChecked := func(why string, disks []string) Result {
		data := D{"checked": false, "why": why}
		if len(disks) > 0 {
			data["disks"] = disks
		}
		return Result{
			Severity: Info, Title: "The health of the disk was not checked",
			Detail: "The disk of the system did not say whether it is failing (it is a virtual disk, or its controller does not pass the question on), it sleeps and was not woken to be asked, the stack it is on has more disks than are asked, or smartctl is not installed here: this report cannot tell.",
			Data:   data, Action: ActionNone,
		}
	}
	device, err := rootDevice(ctx, p)
	if err != nil {
		return unknownFor(err)
	}
	if device == "" {
		return notChecked("no_block_device", nil)
	}
	chain, err := blockChain(ctx, p, device)
	if err != nil {
		return unknownFor(err)
	}
	var disks []string
	partial := false
	for _, d := range chain {
		if d.Type != "disk" || !validDevice(d.Name) || slices.Contains(disks, d.Name) {
			continue
		}
		if len(disks) >= maxSmartDisks {
			// a stack of more disks than are asked: what is said is that it was asked in part
			partial = true
			continue
		}
		disks = append(disks, d.Name)
	}
	if len(disks) == 0 {
		return notChecked("no_disk", nil)
	}
	var failed, unavailable, asleep []string
	for _, disk := range disks {
		out, err := p.Run(ctx, "smartctl", "-n", "standby", "-H", disk)
		if isToolMissing(err) {
			return notChecked("no_smartctl", nil)
		}
		if err != nil {
			return unknown(err)
		}
		switch smartHealth(out) {
		case smartFailed:
			failed = append(failed, disk)
		case smartUnavailable:
			unavailable = append(unavailable, disk)
		case smartAsleep:
			asleep = append(asleep, disk)
		}
	}
	switch {
	case len(failed) > 0:
		return Result{
			Severity: Refuse, Title: "The disk of the system says it is failing",
			Detail: "A disk that reports it is failing can end an upgrade half done, and its data with it. Replace it, and put the system back from a backup.",
			Data:   D{"disks": failed},
		}
	case len(unavailable) > 0:
		return notChecked("unavailable", unavailable)
	case len(asleep) > 0:
		// a disk that sleeps is not woken for a question: it was not checked, and it is not a pass
		return notChecked("standby", asleep)
	case partial:
		return notChecked("partial", nil)
	}
	return pass()
}

// isToolMissing says whether err is that of a command that is not installed.
func isToolMissing(err error) bool { return err != nil && errors.Is(err, ErrToolMissing) }

// logDirectory is where ReCasaOS keeps the log the upgrade's own state would be told in.
const logDirectory = "/var/log/casaos"

func checkLogOnTmpfs(ctx context.Context, p Probe) Result {
	list, err := mounts(ctx, p)
	if err != nil {
		return unknown(err)
	}
	// the mount the log is on is the one with the longest mount point that is a part of its path
	var on mount
	found := false
	for _, m := range list {
		inside := m.Point == "/" || logDirectory == m.Point || strings.HasPrefix(logDirectory, m.Point+"/")
		if inside && (!found || len(m.Point) >= len(on.Point)) {
			on, found = m, true
		}
	}
	if !found {
		return unknownWith(ReasonUnexpectedOutput)
	}
	if on.FSType != "tmpfs" && on.FSType != "ramfs" && !strings.HasPrefix(on.Source, "/dev/zram") {
		return pass()
	}
	data := D{"filesystem": on.FSType}
	if mountPoint := maskedPath(on.Point); mountPoint != "" {
		data["mount_point"] = mountPoint
	}
	return Result{
		Severity: Refuse, Title: "The log of ReCasaOS is kept in memory",
		Detail: "The directory where ReCasaOS writes the log of the upgrade is on a filesystem in memory (tmpfs or zram, as log2ram makes): the log would be gone at the restart that ends the upgrade, which is when it is needed.",
		Data:   data,
	}
}

const (
	rootMinimum     = 2 * gibibyte
	archivesMinimum = gibibyte + gibibyte/2
	bootMinimum     = 300 * mebibyte
	// bootTotal is the size of the filesystem of /boot that Debian 13 asks of a partition of 768 MB: the
	// filesystem has about 4 or 5 percent less than the partition (the inode tables and the journal are not
	// room), and findmnt says the filesystem's.
	bootTotal = 700 * mebibyte
)

// roomResult is the refusal of a place that has too little room.
func roomResult(place string, free, needed uint64) Result {
	return Result{
		Severity: Refuse, Title: "There is too little room on " + place,
		Detail: "An upgrade downloads and unpacks gigabytes: with less room than this it stops half done, which is the worst a system can be left in. Free some room first.",
		Data:   D{"path": place, "free_mib": free / mebibyte, "needed_mib": needed / mebibyte}, Action: ActionFreeDiskSpace,
	}
}

func checkDiskRoot(ctx context.Context, p Probe) Result {
	// / and /usr are one filesystem on most machines, and two on some: the smaller room counts
	for _, place := range []string{"/", "/usr"} {
		free, err := p.FreeBytes(ctx, place)
		if err != nil {
			return unknown(err)
		}
		if free < rootMinimum {
			return roomResult(place, free, rootMinimum)
		}
	}
	return pass()
}

func checkDiskArchives(ctx context.Context, p Probe) Result {
	// the filesystem the packages are downloaded to is the one of the first of these that is there
	for _, place := range []string{"/var/cache/apt/archives", "/var/cache/apt", "/var/cache", "/var", "/"} {
		free, err := p.FreeBytes(ctx, place)
		if isNotExist(err) {
			continue
		}
		if err != nil {
			return unknown(err)
		}
		if free < archivesMinimum {
			return roomResult("/var/cache/apt/archives", free, archivesMinimum)
		}
		return pass()
	}
	return unknownWith(ReasonMissing)
}

func checkDiskBoot(ctx context.Context, p Probe) Result {
	free, err := p.FreeBytes(ctx, "/boot")
	if err != nil {
		return unknown(err)
	}
	if free < bootMinimum {
		return roomResult("/boot", free, bootMinimum)
	}
	if SessionOf(ctx).HopOf() != HopDebian12to13 {
		return pass()
	}
	// Debian 13 asks for a /boot of 768 MB, of which 300 MiB are free
	total, err := sizeOf(ctx, p, "/boot")
	if err != nil {
		return unknownFor(err)
	}
	if total < bootTotal {
		return Result{
			Severity: Refuse, Title: "/boot is too small for the next release",
			// no room that is freed makes the filesystem larger: the partition has to be
			Detail: "Debian 13 asks for a /boot partition of 768 MB, with about 300 MiB free: its kernels and initial RAM disks are larger. The filesystem of this one has " + strconv.FormatUint(total/mebibyte, 10) + " MiB, less than the 700 MiB that such a partition has, and freeing room in it does not make it larger: the partition has to be made larger first.",
			Data:   D{"path": "/boot", "total_mib": total / mebibyte, "needed_mib": uint64(bootTotal / mebibyte)}, Action: ActionNone,
		}
	}
	return pass()
}

// sizeOf is the size, in bytes, of the filesystem a path is on.
func sizeOf(ctx context.Context, p Probe, place string) (uint64, error) {
	out, err := p.Run(ctx, "findmnt", "-b", "-n", "-o", "SIZE", "-T", place)
	if err != nil {
		return 0, err
	}
	if out.ExitCode != 0 {
		return 0, errToolFailed
	}
	size, err := strconv.ParseUint(firstLine(out.Text), 10, 64)
	if err != nil {
		return 0, errUnexpectedOutput
	}
	return size, nil
}

const (
	ramRefuse = gibibyte
	ramWarn   = 2 * gibibyte
	// maxMemoryKiB is the most a /proc/meminfo is believed to say: 1 PiB, far above any machine, so
	// that the arithmetic that follows cannot overflow.
	maxMemoryKiB = 1 << 40
)

// meminfoKiB is a value of /proc/meminfo, in KiB.
func meminfoKiB(text, key string) (value uint64, found bool, err error) {
	for _, line := range strings.Split(text, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok || name != key {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 || len(fields) > 2 || (len(fields) == 2 && fields[1] != "kB") {
			return 0, false, errUnexpectedOutput
		}
		value, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil || value > maxMemoryKiB {
			return 0, false, errUnexpectedOutput
		}
		return value, true, nil
	}
	return 0, false, nil
}

func checkRAM(ctx context.Context, p Probe) Result {
	data, err := p.ReadFile(ctx, "/proc/meminfo")
	if err != nil {
		return unknown(err)
	}
	mem, found, err := meminfoKiB(string(data), "MemTotal")
	if err != nil || !found {
		return unknownWith(ReasonUnexpectedOutput)
	}
	swap, _, err := meminfoKiB(string(data), "SwapTotal")
	if err != nil {
		return unknownWith(ReasonUnexpectedOutput)
	}
	total := (mem + swap) * 1024
	result := Result{Data: D{"ram_mib": mem * 1024 / mebibyte, "swap_mib": swap * 1024 / mebibyte, "total_mib": total / mebibyte}}
	switch {
	case total < ramRefuse:
		result.Severity, result.Title = Refuse, "There is too little memory for the upgrade"
		result.Detail = "Less than 1 GiB of memory and swap together: the upgrade's tools are killed for want of memory half way, and leave the system half upgraded. Add swap or memory first."
		result.Data["needed_mib"] = uint64(ramRefuse / mebibyte)
	case total < ramWarn:
		result.Severity, result.Title = Warn, "There is not much memory for the upgrade"
		result.Detail = "Less than 2 GiB of memory and swap together: the upgrade can go through, slowly, but Ubuntu 26.04 asks for 1.5 GiB for itself, and a service that the upgrade restarts needs its share."
		result.Data["needed_mib"] = uint64(ramWarn / mebibyte)
		result.Action = ActionNone
	default:
		return pass()
	}
	return result
}

const powerSupplyDir = "/sys/class/power_supply"

var supplyName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// readText is the text of a small file, trimmed.
func readText(ctx context.Context, p Probe, file string) (text string, found bool, err error) {
	data, found, err := readOptional(ctx, p, file)
	return strings.TrimSpace(string(data)), found, err
}

func checkPower(ctx context.Context, p Probe) Result {
	supplies, err := p.ReadDir(ctx, powerSupplyDir)
	if isNotExist(err) {
		return pass()
	}
	if err != nil {
		return unknown(err)
	}
	for _, name := range supplies {
		if !validName(supplyName, name) {
			continue
		}
		dir := powerSupplyDir + "/" + name + "/"
		kind, found, err := readText(ctx, p, dir+"type")
		if err != nil {
			return unknown(err)
		}
		if !found || kind != "Battery" {
			continue
		}
		// a wireless mouse's or a phone's battery is listed too, as a device's
		if scope, _, err := readText(ctx, p, dir+"scope"); err != nil {
			return unknown(err)
		} else if scope == "Device" {
			continue
		}
		status, _, err := readText(ctx, p, dir+"status")
		if err != nil {
			return unknown(err)
		}
		capacity, hasCapacity, err := readText(ctx, p, dir+"capacity")
		if err != nil {
			return unknown(err)
		}
		percent := -1
		if hasCapacity {
			percent, err = strconv.Atoi(capacity)
			if err != nil || percent < 0 || percent > 100 {
				return unknownWith(ReasonUnexpectedOutput)
			}
		}
		if status != "Discharging" && (percent < 0 || percent >= 50) {
			continue
		}
		data := D{"battery": name}
		if token(strings.ToLower(status), 32) != "" {
			data["status"] = strings.ToLower(status)
		}
		if percent >= 0 {
			data["percent"] = percent
		}
		return Result{
			Severity: Refuse, Title: "The machine runs on a battery, or its battery is low",
			Detail: "A power cut in the middle of an upgrade leaves the system half upgraded, and maybe unable to start. Plug the machine in, and wait for the battery to charge to at least half.",
			Data:   data,
		}
	}
	return pass()
}
