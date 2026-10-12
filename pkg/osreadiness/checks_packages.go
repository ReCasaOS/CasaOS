package osreadiness

import (
	"context"
	"errors"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

func init() {
	register(
		Check{Code: "foreign_arch", Group: GroupPackages, Title: "No package comes from another architecture", Run: checkForeignArch},
		Check{Code: "keyring_old", Group: GroupPackages, Title: "The archive keyring is recent enough", Hops: debianHops, Run: checkKeyring},
		Check{Code: "openssh_old", Group: GroupPackages, Title: "OpenSSH is recent enough for the hop", Hops: []HopID{HopDebian12to13}, Run: checkOpenSSH},
		Check{Code: "host_services", Group: GroupPackages, Title: "No server software with its own migrations is installed", Run: checkHostServices},
		Check{Code: "dkms", Group: GroupPackages, Title: "No driver is built from source", Timeout: 8 * time.Second, Run: checkDKMS},
	)
}

func checkForeignArch(ctx context.Context, p Probe) Result {
	out, err := p.Run(ctx, "dpkg", "--print-foreign-architectures")
	if err != nil {
		return unknown(err)
	}
	if out.ExitCode != 0 {
		return unknownWith(ReasonFailed)
	}
	var foreign []string
	for _, line := range lines(out.Text) {
		if archPattern.MatchString(line) {
			foreign = append(foreign, line)
		}
	}
	if len(foreign) == 0 {
		return pass()
	}
	installed, err := installedPackages(ctx, p)
	if err != nil {
		return unknown(err)
	}
	var packages, archs []string
	for _, pkg := range installed {
		if slices.Contains(foreign, pkg.Arch) {
			packages = append(packages, pkg.Name)
			if !slices.Contains(archs, pkg.Arch) {
				archs = append(archs, pkg.Arch)
			}
		}
	}
	if len(packages) == 0 {
		return pass() // an architecture is added and nothing is installed from it: nothing to go wrong
	}
	slices.Sort(archs)
	return Result{
		Severity: Refuse, Title: "Packages are installed from another architecture",
		Detail: "The upgrade of the system's libraries does not go through with packages of another architecture installed: they have to be removed first.",
		Data:   D{"architectures": archs, "packages": names(packages), "count": len(packages)}, Action: ActionNone,
	}
}

// keyringMinimum is the debian-archive-keyring that has the keys of the release the hop goes to
// (the changelog of the package says which version added them).
var keyringMinimum = map[HopID]string{
	HopDebian11to12: "2021.1.1+deb11u1",
	HopDebian12to13: "2023.3+deb12u2",
}

// openSSHMinimum is the openssh-server that the notes of Debian 13 require before the hop: older
// ones can leave a machine that is upgraded over SSH unreachable.
const openSSHMinimum = "1:9.2p1-2+deb12u7"

// versionOf is the version of an installed package, ok false when it is not installed.
func versionOf(ctx context.Context, p Probe, name string) (version string, ok bool, err error) {
	packages, err := installedPackages(ctx, p, name)
	if err != nil {
		return "", false, err
	}
	for _, pkg := range packages {
		if pkg.Name == name {
			return pkg.Version, true, nil
		}
	}
	return "", false, nil
}

func checkKeyring(ctx context.Context, p Probe) Result {
	minimum := keyringMinimum[SessionOf(ctx).HopOf()]
	if minimum == "" {
		return pass()
	}
	version, installed, err := versionOf(ctx, p, "debian-archive-keyring")
	if err != nil {
		return unknown(err)
	}
	if installed && compareVersions(version, minimum) >= 0 {
		return pass()
	}
	data := D{"minimum": minimum}
	if installed {
		data["installed"] = version
	}
	return Result{
		Severity: Info, Title: "The archive keyring will be brought up to date",
		Detail: "The keyring does not have the keys of the next release yet: the step that brings this release up to date installs the new one first.",
		Data:   data, Action: ActionNone,
	}
}

func checkOpenSSH(ctx context.Context, p Probe) Result {
	version, installed, err := versionOf(ctx, p, "openssh-server")
	if err != nil {
		return unknown(err)
	}
	if !installed || compareVersions(version, openSSHMinimum) >= 0 {
		return pass()
	}
	return Result{
		Severity: Info, Title: "OpenSSH will be brought up to date first",
		Detail: "Debian 13 asks for a recent OpenSSH before the hop, so that an interrupted upgrade over SSH does not lock the machine: the step that brings this release up to date installs it.",
		Data:   D{"installed": version, "minimum": openSSHMinimum}, Action: ActionNone,
	}
}

// hostService is server software that keeps data of its own that each release migrates by its own
// rules, which the upgrade does not manage. Its packages are told by name.
var hostServices = []struct {
	service  string
	packages *regexp.Regexp
}{
	{"postgresql", regexp.MustCompile(`^postgresql(?:-[0-9]+)?$`)},
	{"mariadb", regexp.MustCompile(`^(?:mariadb-server(?:-[0-9.]+|-core-[0-9.]+)?|default-mysql-server)$`)},
	{"mysql", regexp.MustCompile(`^mysql-server(?:-[0-9.]+|-core-[0-9.]+)?$`)},
	{"slapd", regexp.MustCompile(`^slapd$`)},
	{"dovecot", regexp.MustCompile(`^dovecot-core$`)},
	{"rabbitmq", regexp.MustCompile(`^rabbitmq-server$`)},
	{"libvirt", regexp.MustCompile(`^libvirt-daemon(?:-system)?$`)},
	{"strongswan", regexp.MustCompile(`^(?:strongswan|strongswan-starter|strongswan-charon|strongswan-swanctl|charon-systemd)$`)},
	{"bacula", regexp.MustCompile(`^bacula-(?:director|sd|fd)(?:-.+)?$`)},
	{"request-tracker", regexp.MustCompile(`^request-tracker[0-9]*$`)},
}

// hostServicePatterns are what dpkg-query is asked for, to find them: the packages of the
// services above and a few more, which the patterns of hostServices then sort out.
var hostServicePatterns = []string{"postgresql*", "mariadb-server*", "mysql-server*", "default-mysql-server", "slapd", "dovecot-core", "rabbitmq-server", "libvirt-daemon*", "strongswan*", "charon-systemd", "bacula-*", "request-tracker*"}

func checkHostServices(ctx context.Context, p Probe) Result {
	installed, err := installedPackages(ctx, p, hostServicePatterns...)
	if err != nil {
		return unknown(err)
	}
	var found, packages []string
	for _, pkg := range installed {
		for _, service := range hostServices {
			if service.packages.MatchString(pkg.Name) {
				packages = append(packages, pkg.Name)
				if !slices.Contains(found, service.service) {
					found = append(found, service.service)
				}
			}
		}
	}
	if len(found) == 0 {
		return pass()
	}
	slices.Sort(found)
	return Result{
		Severity: Refuse, Title: "Server software with its own data is installed",
		Detail: "Databases and servers like these migrate their data by their own rules at each release, which the upgrade does not manage: upgrade them by hand, or not on this machine.",
		Data:   D{"services": found, "packages": names(packages)}, Action: ActionNone,
	}
}

var dkmsStatusLine = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._+-]{0,63})[/,]`)

// dkmsModules are the modules DKMS has, from "dkms status": "zfs/2.1.11, 6.1.0-13-amd64, x86_64:
// installed", or in the older form "zfs, 2.0.3, 5.10.0-35-amd64, x86_64: installed". A box without
// dkms has none.
func dkmsModules(ctx context.Context, p Probe) ([]string, error) {
	out, err := p.Run(ctx, "dkms", "status")
	if errors.Is(err, ErrToolMissing) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if out.ExitCode != 0 {
		return nil, errToolFailed
	}
	var modules []string
	for _, line := range lines(out.Text) {
		if m := dkmsStatusLine.FindStringSubmatch(line); m != nil && !slices.Contains(modules, m[1]) {
			modules = append(modules, m[1])
		}
	}
	slices.Sort(modules)
	return modules, nil
}

// driverOf is the name of the driver a device is bound to: the last element of what the "driver"
// link of its sysfs directory points at; "" when it has none (a virtual device).
func driverOf(ctx context.Context, p Probe, device string) (string, error) {
	target, err := p.Readlink(ctx, device+"/driver")
	if isNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// only a name modinfo may be asked about
	if name := path.Base(target); moduleArg.MatchString(name) {
		return name, nil
	}
	return "", nil
}

var interfaceName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,15}$`)

// defaultRouteInterface is the interface the IPv4 default route goes out by, from /proc/net/route:
// "" when there is no default route, or when it names something that is not an interface's name.
func defaultRouteInterface(ctx context.Context, p Probe) (string, error) {
	data, found, err := readOptional(ctx, p, "/proc/net/route")
	if err != nil || !found {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		// Iface Destination Gateway Flags RefCnt Use Metric Mask ...
		fields := strings.Fields(line)
		if len(fields) >= 8 && fields[1] == "00000000" && fields[7] == "00000000" {
			if validName(interfaceName, fields[0]) {
				return fields[0], nil
			}
			return "", nil
		}
	}
	return "", nil
}

// The bounds of a stack of devices that is followed to the hardware: a machine's is a few devices deep and
// wide, and a ring (which sysfs does not make, and a hostile tree could) is left at the second visit. A stack that
// is deeper or wider than that is not seen to its end, and the walk says so (ErrTooLarge, with the drivers it did
// see): the driver that was not reached may be the one that is built from source.
const (
	maxStackDepth   = 6
	maxStackDevices = 32
)

// blockName is the name of a block device as sysfs has it (sda2, dm-0, md127, nvme0n1p2).
var blockName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// interfacesUnder are the interfaces an interface is built on: the ports of a bridge (brif), and the
// lower_* links of sysfs, which are a bond's slaves, a VLAN's parent and what a bridge, a macvlan and the
// like sit on.
func interfacesUnder(ctx context.Context, p Probe, iface string) ([]string, error) {
	var under []string
	add := func(name string) {
		if validName(interfaceName, name) && !slices.Contains(under, name) {
			under = append(under, name)
		}
	}
	members, err := p.ReadDir(ctx, netDir+iface+"/brif")
	if err != nil && !isNotExist(err) {
		return nil, err
	}
	for _, member := range members {
		add(member)
	}
	entries, err := p.ReadDir(ctx, netDir+iface)
	if err != nil && !isNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if lower, ok := strings.CutPrefix(entry, "lower_"); ok {
			add(lower)
		}
	}
	return under, nil
}

// networkDrivers are the drivers of the network cards the default route goes out by: of the cards that
// are under it, when it is a bridge, a bond or a VLAN, down to the ones that have nothing under them. A stack that
// goes further than the bounds says so with ErrTooLarge, and the drivers it found.
func networkDrivers(ctx context.Context, p Probe) ([]string, error) {
	iface, err := defaultRouteInterface(ctx, p)
	if err != nil || iface == "" {
		return nil, err
	}
	var drivers []string
	seen := map[string]bool{}
	capped := false
	var walk func(name string, depth int) error
	walk = func(name string, depth int) error {
		if seen[name] {
			return nil
		}
		if len(seen) >= maxStackDevices {
			capped = true
			return nil
		}
		seen[name] = true
		under, err := interfacesUnder(ctx, p, name)
		if err != nil {
			return err
		}
		if len(under) == 0 || depth >= maxStackDepth {
			driver, err := driverOf(ctx, p, netDir+name+"/device")
			if err == nil && driver != "" && !slices.Contains(drivers, driver) {
				drivers = append(drivers, driver)
			}
			capped = capped || len(under) > 0
			return err
		}
		for _, lower := range under {
			if err := walk(lower, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(iface, 0); err != nil {
		return nil, err
	}
	if capped {
		return drivers, ErrTooLarge
	}
	return drivers, nil
}

// stackStart is where the walk down from the root's disks begins: a link of sysfs, as it says its target, and the
// directory it is relative to.
type stackStart struct{ base, target string }

// rootStarts are the places the root filesystem's devices begin at in sysfs. A root on a block device is the node
// /sys/dev/block/<major:minor> that the mounts say; one that has no such node and is of a major that is not 0 is a
// block device that sysfs could not be asked about, and is an error (it is not a root that has no disk driver). The
// devices of the anonymous major 0 are not block devices, a memory filesystem, a network one or an overlay among
// them, and have no disk to have a driver, except btrfs, which says the anonymous device of its own and lists its
// disks in /sys/fs/btrfs.
func rootStarts(ctx context.Context, p Probe, root mount) ([]stackStart, error) {
	target, err := p.Readlink(ctx, "/sys/dev/block/"+root.MajorMinor)
	switch {
	case err == nil:
		return []stackStart{{"/sys/dev/block", target}}, nil
	case !isNotExist(err):
		return nil, err
	case root.FSType == "btrfs":
		return btrfsStarts(ctx, p, root.Source)
	case strings.HasPrefix(root.MajorMinor, "0:"):
		return nil, nil
	}
	return nil, err
}

// btrfsStarts are the disks of the btrfs filesystem that a device is a member of: sysfs has a directory of the
// filesystem, by its uuid, that lists the members by the names the kernel gives the block devices (dm-0, not
// /dev/mapper/vg-root, which is a link to it). The device the mounts say is found among them, and all of them are
// walked: the other disks of the filesystem are as much the root's as the first.
func btrfsStarts(ctx context.Context, p Probe, source string) ([]stackStart, error) {
	name := path.Base(source)
	if target, err := p.Readlink(ctx, source); err == nil {
		name = path.Base(target)
	}
	if !validName(blockName, name) {
		return nil, errUnexpectedOutput
	}
	filesystems, err := p.ReadDir(ctx, "/sys/fs/btrfs")
	if err != nil {
		return nil, err
	}
	for _, uuid := range filesystems {
		if !validName(blockName, uuid) {
			continue
		}
		members, err := p.ReadDir(ctx, "/sys/fs/btrfs/"+uuid+"/devices")
		if isNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !slices.Contains(members, name) {
			continue
		}
		if len(members) > maxStackDevices {
			return nil, ErrTooLarge
		}
		var starts []stackStart
		for _, member := range members {
			if !validName(blockName, member) {
				continue
			}
			lower, err := p.Readlink(ctx, "/sys/class/block/"+member)
			if err != nil {
				return nil, err
			}
			starts = append(starts, stackStart{"/sys/class/block", lower})
		}
		return starts, nil
	}
	return nil, errUnexpectedOutput
}

// rootDrivers are the drivers of the controllers the root filesystem's disks are on: the drivers of
// every device on the way from the disk up to the bus, from /sys/dev/block/<major:minor>. A root on a
// logical volume or a RAID is followed down to the disks it is built on, through the slaves of its
// sysfs directory, and a btrfs one to the disks of its filesystem (rootStarts). A stack that goes further than the
// bounds says so with ErrTooLarge, and the drivers it found.
func rootDrivers(ctx context.Context, p Probe) ([]string, error) {
	list, err := mounts(ctx, p)
	if err != nil {
		return nil, err
	}
	root, ok := rootMount(list)
	if !ok {
		return nil, nil
	}
	starts, err := rootStarts(ctx, p, root)
	if err != nil || len(starts) == 0 {
		return nil, err
	}
	var drivers []string
	seen := map[string]bool{}
	capped := false
	var walk func(base, target string, depth int) error
	walk = func(base, target string, depth int) error {
		full := path.Join(base, target)
		if seen[full] {
			return nil
		}
		if len(seen) >= maxStackDevices {
			capped = true
			return nil
		}
		seen[full] = true
		parts := strings.Split(strings.Trim(full, "/"), "/")
		// from /sys/devices/<root of the bus> down to the partition
		for i := 3; i <= len(parts); i++ {
			driver, err := driverOf(ctx, p, "/"+strings.Join(parts[:i], "/"))
			if err != nil {
				return err
			}
			if driver != "" && !slices.Contains(drivers, driver) {
				drivers = append(drivers, driver)
			}
		}
		slaves, err := p.ReadDir(ctx, full+"/slaves")
		if isNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if depth >= maxStackDepth {
			capped = capped || len(slaves) > 0
			return nil
		}
		for _, slave := range slaves {
			if !validName(blockName, slave) {
				continue
			}
			lower, err := p.Readlink(ctx, "/sys/class/block/"+slave)
			if isNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if err := walk("/sys/class/block", lower, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for _, start := range starts {
		if err := walk(start.base, start.target, 0); err != nil {
			return nil, err
		}
	}
	if capped {
		return drivers, ErrTooLarge
	}
	return drivers, nil
}

// builtFromSource says whether a driver is a DKMS module: modinfo gives the file it is loaded
// from, and DKMS puts its modules under updates/dkms.
func builtFromSource(ctx context.Context, p Probe, driver string) (bool, error) {
	out, err := p.Run(ctx, "modinfo", "-n", driver)
	if err != nil {
		return false, err
	}
	return out.ExitCode == 0 && strings.Contains(out.Text, "/updates/dkms/"), nil
}

// secureBootVariable is the EFI variable that says whether Secure Boot is on: four bytes of
// attributes, then 1 for on.
const secureBootVariable = "/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c"

// The certificate dkms signs the modules with, for Secure Boot: where the dkms of the distribution puts
// it, unless /etc/dkms/framework.conf (and the .conf files of framework.conf.d, which are read after it)
// say another. Debian's dkms keeps it in /var/lib/dkms (mok.pub), and Ubuntu's in the key that shim-signed
// made (MOK.der): both are the public half of a key, and neither says whether it was enrolled, which is
// what mokutil would ask and the report does not run.
const (
	dkmsConfig            = "/etc/dkms/framework.conf"
	dkmsConfigDir         = "/etc/dkms/framework.conf.d"
	dkmsCertificateDebian = "/var/lib/dkms/mok.pub"
	dkmsCertificateUbuntu = "/var/lib/shim-signed/mok/MOK.der"
)

var (
	dkmsCertificateLine = regexp.MustCompile(`^\s*mok_certificate\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s#]+))\s*(?:#.*)?$`)
	certificatePath     = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,200}$`)
)

// dkmsCertificateExists says whether the certificate dkms signs with is there.
func dkmsCertificateExists(ctx context.Context, p Probe) (bool, error) {
	certificate := dkmsCertificateDebian
	if SessionOf(ctx).Identity.ID == "ubuntu" {
		certificate = dkmsCertificateUbuntu
	}
	files := []string{dkmsConfig}
	names, err := p.ReadDir(ctx, dkmsConfigDir)
	if err != nil && !isNotExist(err) {
		return false, err
	}
	for _, name := range names {
		if strings.HasSuffix(name, ".conf") && validName(poolName, name) {
			files = append(files, dkmsConfigDir+"/"+name)
		}
	}
	for _, file := range files {
		data, found, err := readOptional(ctx, p, file)
		if err != nil {
			return false, err
		}
		if !found {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if m := dkmsCertificateLine.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
				certificate = m[1] + m[2] + m[3]
			}
		}
	}
	// a path that is not one is not a certificate that is there
	if !certificatePath.MatchString(certificate) || slices.Contains(strings.Split(certificate, "/"), "..") {
		return false, nil
	}
	return exists(ctx, p, certificate)
}

func checkDKMS(ctx context.Context, p Probe) Result {
	modules, err := dkmsModules(ctx, p)
	if err != nil {
		return unknown(err)
	}
	if len(modules) == 0 {
		return pass()
	}
	var network, disk bool
	var capped error
	for _, set := range []struct {
		drivers func(context.Context, Probe) ([]string, error)
		found   *bool
	}{{networkDrivers, &network}, {rootDrivers, &disk}} {
		drivers, err := set.drivers(ctx, p)
		if errors.Is(err, ErrTooLarge) {
			// a stack that is not seen to its end: what was seen is judged, and what was not is not known
			capped = err
		} else if err != nil {
			return unknownFor(err)
		}
		for _, driver := range drivers {
			dkms, err := builtFromSource(ctx, p, driver)
			if err != nil {
				return unknown(err)
			}
			*set.found = *set.found || dkms
		}
	}
	// a driver that is built from source is known to be on the card or the disk, but none that was not met is known not to be
	if capped != nil && !network && !disk {
		return unknown(capped)
	}
	state, found, err := readOptional(ctx, p, secureBootVariable)
	if err != nil {
		return unknown(err)
	}
	secureBoot := found && len(state) == 5 && state[4] == 1
	key, err := dkmsCertificateExists(ctx, p)
	if err != nil {
		return unknown(err)
	}
	data := D{"modules": modules, "network": network, "disk": disk, "secure_boot": secureBoot, "mok": key}
	if network || disk || (secureBoot && !key) {
		return Result{
			Severity: Refuse, Title: "A driver the machine needs to start or to be reached is built from source",
			Detail: "A driver that is built from source (DKMS) for the network card or the disk of the system has to be rebuilt for the new kernel, and with Secure Boot it has to be signed with a key that is enrolled: if either fails the machine does not come back.",
			Data:   data,
		}
	}
	return Result{
		Severity: Warn, Title: "Some drivers are built from source",
		Detail: "These modules are rebuilt for the new kernel: if one fails to build, what uses it (a data pool, a graphics card) does not work after the restart.",
		Data:   data,
	}
}
