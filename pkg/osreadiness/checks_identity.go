package osreadiness

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/ossupport"
)

func init() {
	register(
		Check{Code: "unsupported_os", Group: GroupIdentity, Gate: true, Title: "The operating system is one the upgrade is for", Run: checkUnsupportedOS},
		Check{Code: "vendor_os", Group: GroupIdentity, Gate: true, Title: "The system is a plain Debian or Ubuntu", Run: checkVendorOS},
		Check{Code: "unproven_arch", Group: GroupIdentity, Gate: true, Title: "The architecture is amd64", Run: checkArch},
		Check{Code: "container", Group: GroupIdentity, Title: "The system is not a container", Run: checkContainer},
		Check{Code: "virt_xen", Group: GroupIdentity, Title: "The system is not a Xen guest", Run: checkXen},
		Check{Code: "no_systemd", Group: GroupIdentity, Title: "systemd is the init system", Run: checkSystemd},
		Check{Code: "not_root", Group: GroupIdentity, Title: "ReCasaOS runs as root", Run: checkRoot},
	)
}

// token is s when it is 1 to limit characters of a-z, 0-9, "." "_" and "-", and "" otherwise: what
// may be said of a name a box gave.
func token(s string, limit int) string {
	if s == "" || len(s) > limit {
		return ""
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return ""
		}
	}
	return s
}

// firstLine is the first line of s, trimmed.
func firstLine(s string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(first)
}

// withTokens is data with the keys that have a value.
func withTokens(pairs ...string) D {
	data := D{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			data[pairs[i]] = pairs[i+1]
		}
	}
	return data
}

func checkUnsupportedOS(ctx context.Context, p Probe) Result {
	s := SessionOf(ctx)
	id := s.Identity
	switch {
	case s.ReleaseErr != nil:
		return unknown(s.ReleaseErr)
	case id.Variant != "":
		return pass() // vendor_os says what it is
	case !id.Known:
		return Result{
			Severity: Refuse, Title: "This is not Debian or Ubuntu",
			Detail: "Only Debian and the LTS releases of Ubuntu can be upgraded from here: Debian 11 and 12, Ubuntu 22.04 and 24.04.",
			Data:   withTokens("id", id.ID, "version_id", id.VersionID),
		}
	case s.Hop == nil && !s.Latest && ossupport.BeforeTable(p.Now()):
		// the table has nothing for a day before the one it was written on: it is the date that is
		// wrong, and not the release that has no upgrade. The check cannot say, and says why.
		return Result{
			Severity: Refuse, Title: "The date of this machine is wrong",
			Detail: "The date of this machine is before " + ossupport.AsOf.Format("2006-01-02") + ", which cannot be: the table of the releases cannot be read against it, so the report cannot say what upgrade this machine has. Set the date (and the time synchronisation of the machine), then check again.",
			Data:   D{"unknown": true, "reason": ReasonClock, "id": id.ID, "version_id": id.VersionID},
		}
	case s.Hop == nil && !s.Latest && ossupport.Stale(p.Now()):
		// the other end of the table: it was written more than eighteen months ago, and says nothing of what is to
		// come. The release may well have an upgrade: it is this ReCasaOS that does not know it.
		return Result{
			Severity: Refuse, Title: "This ReCasaOS is too old to know the releases",
			Detail: "The table of the releases that this ReCasaOS carries was written on " + ossupport.AsOf.Format("2006-01-02") + ", more than eighteen months ago, and cannot say what upgrade this machine has. Update ReCasaOS (or set the date of the machine, if it is wrong), then check again.",
			Data:   D{"unknown": true, "reason": ReasonStale, "id": id.ID, "version_id": id.VersionID},
		}
	case s.Hop == nil && !s.Latest:
		return Result{
			Severity: Refuse, Title: "There is no upgrade from this release",
			Detail: "The releases that can be upgraded from here are Debian 11 and 12 and Ubuntu 22.04 and 24.04. This one is not among them.",
			Data:   withTokens("id", id.ID, "version_id", id.VersionID),
		}
	}
	return pass()
}

var variantNames = map[string]string{
	"raspberrypi":    "Raspberry Pi OS",
	"armbian":        "Armbian",
	"dietpi":         "DietPi",
	"proxmox":        "Proxmox VE",
	"openmediavault": "OpenMediaVault",
	"proxmox_backup": "Proxmox Backup Server",
	"proxmox_mail":   "Proxmox Mail Gateway",
	"truenas":        "TrueNAS",
}

func checkVendorOS(ctx context.Context, _ Probe) Result {
	s := SessionOf(ctx)
	switch {
	case s.MarkerErr != nil:
		return unknown(s.MarkerErr)
	case s.Identity.Variant == "":
		return pass()
	}
	name := variantNames[s.Identity.Variant]
	if name == "" {
		name = "A vendor's system"
	}
	return Result{
		Severity: Refuse, Title: "This is " + name + ", which has its own way to upgrade",
		Detail: name + " is upgraded with its vendor's own tool, or by installing it again: the upgrade of ReCasaOS does not touch it.",
		Data:   withTokens("variant", s.Identity.Variant),
	}
}

var archPattern = regexp.MustCompile(`^[a-z0-9-]{1,16}$`)

func checkArch(ctx context.Context, p Probe) Result {
	refuse := func(arch string) Result {
		data := D{}
		if archPattern.MatchString(arch) {
			data["arch"] = arch
		}
		return Result{
			Severity: Refuse, Title: "Only amd64 machines can be upgraded",
			Detail: "The upgrade has only been proven on amd64: this machine is not.", Data: data,
		}
	}
	if p.Arch() != "amd64" {
		return refuse(p.Arch())
	}
	// the architecture of the packages: a 64-bit kernel can run a 32-bit system
	out, err := p.Run(ctx, "dpkg", "--print-architecture")
	switch {
	case errors.Is(err, ErrToolMissing):
		return pass() // no dpkg: not a Debian system, which unsupported_os says
	case err != nil:
		return unknown(err)
	case out.ExitCode != 0:
		return unknownWith(ReasonFailed)
	}
	if arch := firstLine(out.Text); arch != "amd64" {
		return refuse(arch)
	}
	return pass()
}

// virtAnswer is what systemd-detect-virt says: the kind it found, or "none". It exits with 0 when it found
// one, and with 1 when there is none and says "none"; it exits with 1 as well when it could not tell, and
// says so in words ("Failed to detect virtualization: ..."): that is not "none", it is no answer. Any
// other end (another status, no word, a word that is not on the first line) is no answer either.
func virtAnswer(out Output) (string, error) {
	kind := firstLine(out.Text)
	switch {
	case kind == "none" && (out.ExitCode == 0 || out.ExitCode == 1):
		return "none", nil
	case kind != "" && kind != "none" && out.ExitCode == 0:
		return kind, nil
	}
	return "", errUnexpectedOutput
}

var containerKind = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// containerCgroupHints are what the control group of PID 1 holds in a container, and does not on a
// machine whose PID 1 is its own.
var containerCgroupHints = []string{"/docker/", "/docker-", "/lxc/", "/lxc.payload", "/kubepods", "/libpod"}

func checkContainer(ctx context.Context, p Probe) Result {
	refuse := func(kind string) Result {
		if !containerKind.MatchString(kind) {
			kind = "container"
		}
		return Result{
			Severity: Refuse, Title: "This system is a container",
			Detail: "A container shares the kernel and the network of its host: it cannot be upgraded from inside.",
			Data:   D{"kind": kind},
		}
	}
	out, err := p.Run(ctx, "systemd-detect-virt", "--container")
	switch {
	case err == nil:
		kind, err := virtAnswer(out)
		if err != nil {
			return unknownFor(err)
		}
		if kind != "none" {
			return refuse(kind)
		}
	case !errors.Is(err, ErrToolMissing):
		return unknown(err)
	}
	// what systemd-detect-virt does not say, or a box that has no systemd-detect-virt
	for _, marker := range []struct{ path, kind string }{{"/.dockerenv", "docker"}, {"/run/.containerenv", "podman"}} {
		found, err := exists(ctx, p, marker.path)
		if err != nil {
			return unknown(err)
		}
		if found {
			return refuse(marker.kind)
		}
	}
	for _, source := range []struct {
		path  string
		hints []string
		kind  string
	}{
		{"/proc/1/cgroup", containerCgroupHints, "container"},
		{"/proc/sys/kernel/osrelease", []string{"microsoft", "Microsoft", "WSL"}, "wsl"},
	} {
		data, found, err := readOptional(ctx, p, source.path)
		if err != nil {
			return unknown(err)
		}
		if !found {
			continue
		}
		for _, hint := range source.hints {
			if strings.Contains(string(data), hint) {
				return refuse(source.kind)
			}
		}
	}
	return pass()
}

func checkXen(ctx context.Context, p Probe) Result {
	refuse := Result{
		Severity: Refuse, Title: "This system is a Xen guest",
		Detail: "Debian 12 renames the network interfaces of a Xen guest: the machine could lose its network.",
	}
	out, err := p.Run(ctx, "systemd-detect-virt", "--vm")
	switch {
	case err == nil:
		kind, err := virtAnswer(out)
		if err != nil {
			return unknownFor(err)
		}
		if kind == "xen" {
			return refuse
		}
		return pass()
	case !errors.Is(err, ErrToolMissing):
		return unknown(err)
	}
	data, found, err := readOptional(ctx, p, "/sys/hypervisor/type")
	if err != nil {
		return unknown(err)
	}
	if found && strings.TrimSpace(string(data)) == "xen" {
		return refuse
	}
	return pass()
}

func checkSystemd(ctx context.Context, p Probe) Result {
	data, err := p.ReadFile(ctx, "/proc/1/comm")
	if err != nil {
		return unknown(err)
	}
	if strings.TrimSpace(string(data)) != "systemd" {
		return Result{
			Severity: Refuse, Title: "systemd is not the init system",
			Detail: "The upgrade is made for systems that start with systemd.",
		}
	}
	return pass()
}

func checkRoot(_ context.Context, p Probe) Result {
	if p.EUID() != 0 {
		return Result{
			Severity: Refuse, Title: "ReCasaOS does not run as root",
			Detail: "The upgrade changes the system's packages: it can only be done by root.",
		}
	}
	return pass()
}
