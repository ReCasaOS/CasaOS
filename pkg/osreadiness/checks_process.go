package osreadiness

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

func init() {
	register(
		Check{Code: "busy", Group: GroupProcess, Title: "No other package operation is running", Timeout: 10 * time.Second, Run: checkBusy},
		Check{Code: "apps", Group: GroupProcess, Title: "No app has an operation in progress", Run: checkApps},
		Check{Code: "reboot_first", Group: GroupProcess, Title: "No restart is waiting", Run: checkRebootFirst},
		Check{Code: "uptime_long", Group: GroupProcess, Title: "The machine has not run for too long", Run: checkUptime},
		Check{Code: "broken_dpkg", Group: GroupProcess, Title: "dpkg is in a clean state", Timeout: 17 * time.Second, Run: checkBrokenDpkg},
		Check{Code: "held", Group: GroupProcess, Title: "No package is on hold", Run: checkHeld},
		Check{Code: "held_docker", Group: GroupProcess, Title: "Docker's packages are not on hold", Run: checkHeldDocker},
	)
}

// busyUnits are the services whose being active says a package manager is at work. Not
// unattended-upgrades.service: that is a service that waits for the shutdown, and is active on
// every box that has it. The unattended upgrade itself runs under apt-daily-upgrade.service, and
// holds dpkg's locks (MaintenanceBusy).
var busyUnits = []string{"apt-daily.service", "apt-daily-upgrade.service", "packagekit.service"}

func checkBusy(ctx context.Context, p Probe) Result {
	busy := func(detail string, data D) Result {
		return Result{
			Severity: Refuse, Title: "Another update or package operation is running", Detail: detail, Data: data,
			Action: ActionWaitForPackageManager,
		}
	}
	reason, isBusy, err := p.MaintenanceBusy(ctx)
	if isBusy {
		return busy(reason+".", D{"source": "maintenance"})
	}
	if err != nil {
		return unknown(err)
	}
	for _, unit := range busyUnits {
		out, err := p.Run(ctx, "systemctl", "show", unit, "--property=ActiveState", "--value")
		if err != nil {
			return unknown(err)
		}
		if out.ExitCode != 0 {
			return unknownWith(ReasonFailed)
		}
		switch strings.TrimSpace(out.Text) {
		case "active", "activating", "deactivating":
			return busy(strings.TrimSuffix(unit, ".service")+" is running.", D{"source": "unit", "unit": unit})
		}
	}
	return pass()
}

var appName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func checkApps(ctx context.Context, p Probe) Result {
	apps, err := p.AppOperations(ctx)
	if err != nil {
		// AppManagement that cannot be asked is not an AppManagement with nothing to do
		return unknown(err)
	}
	if len(apps) == 0 {
		return pass()
	}
	listed := []string{}
	for _, app := range apps {
		if appName.MatchString(app) && !slices.Contains(listed, app) {
			listed = append(listed, app)
		}
	}
	slices.Sort(listed)
	return Result{
		Severity: Refuse, Title: "An app has an operation in progress",
		Detail: "An app is being installed, updated, backed up or restored: the upgrade waits for it.",
		Data:   D{"apps": listed, "count": len(apps)}, Action: ActionWaitForApps,
	}
}

// checkRebootFirst compares the names of the kernels that are installed with the one that runs, and looks
// for the flag an update leaves. It does not compare the builds: a kernel package upgraded in place under
// the same name (linux-image-5.10.0-35-amd64, 5.10.237-1 then -2) that was never booted is not seen, and
// plain Debian writes no reboot-required for it. Left as it is; the owner decides later whether the version
// in uname -v is to be compared with the installed package's.
func checkRebootFirst(ctx context.Context, p Probe) Result {
	flag := false
	for _, path := range []string{"/var/run/reboot-required", "/run/reboot-required"} {
		found, err := exists(ctx, p, path)
		if err != nil {
			return unknown(err)
		}
		flag = flag || found
	}
	data, err := p.ReadFile(ctx, "/proc/sys/kernel/osrelease")
	if err != nil {
		return unknown(err)
	}
	running := strings.TrimSpace(string(data))
	var newest kernelRelease
	newestName := ""
	if current, ok := parseKernel(running); ok {
		boot, err := p.ReadDir(ctx, "/boot")
		if err != nil && !isNotExist(err) {
			return unknown(err)
		}
		for _, name := range boot {
			release, ok := strings.CutPrefix(name, "vmlinuz-")
			if !ok {
				continue
			}
			installed, ok := parseKernel(release)
			if !ok || !newerKernel(installed, current) {
				continue
			}
			if newestName == "" || compareVersions(installed.Version, newest.Version) > 0 {
				newest, newestName = installed, release
			}
		}
	}
	if !flag && newestName == "" {
		return pass()
	}
	result := Result{
		Severity: Refuse, Title: "The machine has to be restarted first",
		Detail: "A newer kernel is installed than the one that runs, or an update asked for a restart. Restarting also proves that the machine starts on the release it has now.",
		Data:   D{"restart_requested": flag}, Action: ActionRestartMachine,
	}
	if token(running, 64) != "" {
		result.Data["running"] = running
	}
	if newestName != "" {
		result.Data["installed"] = newestName
	}
	return result
}

const uptimeLimit = 180 * 24 * time.Hour

func checkUptime(ctx context.Context, p Probe) Result {
	data, err := p.ReadFile(ctx, "/proc/uptime")
	if err != nil {
		return unknown(err)
	}
	seconds, err := strconv.ParseFloat(firstField(string(data)), 64)
	if err != nil || seconds < 0 {
		return unknownWith(ReasonUnexpectedOutput)
	}
	uptime := time.Duration(seconds * float64(time.Second))
	if uptime <= uptimeLimit {
		return pass()
	}
	return Result{
		Severity: Warn, Title: "The machine has not been restarted for a long time",
		Detail: "A machine that has run for months has never tried its own start: a restart first would show whether it still starts.",
		Data:   D{"days": int(uptime.Hours() / 24)}, Action: ActionRestartMachine,
	}
}

// firstField is the first space-separated word of s.
func firstField(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

// brokenAptCheck is what apt-get check says when the packages are the trouble, as opposed to the
// sources or the box: apt prints it for a package that is half installed or whose dependencies are
// not met.
var brokenAptCheck = regexp.MustCompile(`(?i)unmet dependencies|--fix-broken|broken packages`)

func checkBrokenDpkg(ctx context.Context, p Probe) Result {
	var problems []string
	data := D{}

	audit, err := p.Run(ctx, "dpkg", "--audit")
	if err != nil {
		return unknown(err)
	}
	switch {
	case strings.TrimSpace(audit.Text) != "":
		problems = append(problems, "dpkg --audit lists packages that are not fully installed or configured")
		data["audit"] = true
	case audit.ExitCode != 0:
		return unknownWith(ReasonFailed)
	}

	entries, err := p.ReadDir(ctx, "/var/lib/dpkg/updates")
	if err != nil && !isNotExist(err) {
		return unknown(err)
	}
	pending := 0
	for _, name := range entries {
		if digitsOnly.MatchString(name) {
			pending++
		}
	}
	if pending > 0 {
		problems = append(problems, "dpkg's journal has an unfinished operation")
		data["updates"] = pending
	}

	// apt's own check of the dependencies, with its locks and its caches left alone. It is asked with less
	// time than the check has: when apt does not answer in time, the check is still there to say what the
	// audit and the journal found.
	actx, cancel := reserveTime(ctx)
	defer cancel()
	check, err := p.Run(actx, aptCheckArgs...)
	unread := ""
	switch {
	case err != nil:
		if len(problems) == 0 {
			return unknown(err)
		}
		unread = "apt-get check could not be asked"
	case check.ExitCode != 0 && !brokenAptCheck.MatchString(check.Text):
		// apt failed for something that is not the packages (its sources, its lists): not a statement about
		// dpkg, and not "fine" either
		if len(problems) == 0 {
			return unknownWith(ReasonUnexpectedOutput)
		}
		unread = "apt-get check failed for another reason"
	case check.ExitCode != 0:
		problems = append(problems, "apt-get check finds broken dependencies")
		data["apt_check"] = true
	}

	if len(problems) == 0 {
		return pass()
	}
	detail := strings.Join(problems, "; ") + "."
	if unread != "" {
		// what was found is the owner's way to the repair, and what apt-get check would add is not known
		detail += " The answer of apt-get check could not be read (" + unread + ")."
	}
	return Result{
		Severity: Refuse, Title: "dpkg has an unfinished operation",
		Detail: detail, Data: data, Action: ActionFinishDpkg,
	}
}

// reserveTime is the context of a command that is the last thing a check asks, with an eighth of the time
// the check has left kept back for the check to say what it found; the context it was given, when it has
// no end.
func reserveTime(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	left := time.Until(deadline)
	return context.WithTimeout(ctx, left-left/8)
}

// dockerHoldable are the Docker packages whose hold is harmless: the source of Docker is paused
// for the upgrade, so they are not touched. A hold on one of them is said, and does not stop it.
var dockerHoldable = regexp.MustCompile(`^(docker-ce|docker-ce-cli|docker-ce-rootless-extras|containerd\.io|docker-[a-z0-9-]+-plugin)$`)

// heldPackages are the packages on hold, the Docker engine's apart from the others. They are what
// "apt-mark showhold" lists, read from dpkg's own records: apt-mark builds apt's caches to say it,
// and the report writes nothing. A hold is the wanted state of a package, whatever the state it is in:
// a package that was purged and held (apt-get purge snapd; apt-mark hold snapd) is listed, and keeps
// apt from installing it when something of the next release depends on it.
func heldPackages(ctx context.Context, p Probe) (docker, others []string, err error) {
	listed, err := listedPackages(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	docker, others = []string{}, []string{}
	for _, pkg := range listed {
		switch {
		case !pkg.Held || slices.Contains(docker, pkg.Name) || slices.Contains(others, pkg.Name):
		case dockerHoldable.MatchString(pkg.Name):
			docker = append(docker, pkg.Name)
		default:
			others = append(others, pkg.Name)
		}
	}
	slices.Sort(docker)
	slices.Sort(others)
	return docker, others, nil
}

func checkHeld(ctx context.Context, p Probe) Result {
	_, others, err := heldPackages(ctx, p)
	if err != nil {
		return unknown(err)
	}
	if len(others) == 0 {
		return pass()
	}
	return Result{
		Severity: Refuse, Title: "Some packages are on hold",
		Detail: "A hold is a decision, and the upgrade never lifts it for you: a package on hold stops apt from upgrading what depends on it.",
		Data:   D{"packages": others, "count": len(others)}, Action: ActionReleaseHolds,
	}
}

func checkHeldDocker(ctx context.Context, p Probe) Result {
	docker, _, err := heldPackages(ctx, p)
	if err != nil {
		return unknown(err)
	}
	if len(docker) == 0 {
		return pass()
	}
	return Result{
		Severity: Info, Title: "Docker's packages are on hold",
		Detail: "That is harmless: Docker's source is paused during the upgrade, so they are not touched.",
		Data:   D{"packages": docker}, Action: ActionNone,
	}
}
