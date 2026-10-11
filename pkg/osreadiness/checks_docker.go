package osreadiness

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// The checks of the apps: Docker, and what runs in it. ReCasaOS is its apps, and an upgrade restarts
// the Docker daemon several times and the machine once, so the report says what of them would not
// come back.
func init() {
	register(
		Check{Code: "docker_swarm", Group: GroupDocker, Title: "Docker is not part of a swarm", Timeout: 8 * time.Second, Run: checkDockerSwarm},
		Check{Code: "docker_origin", Group: GroupDocker, Title: "Docker comes from Docker's own repository", Timeout: 12 * time.Second, Run: checkDockerOrigin},
		Check{Code: "docker_down", Group: GroupDocker, Title: "The Docker daemon answers", Timeout: 8 * time.Second, Run: checkDockerDown},
		Check{Code: "docker_update_pending", Group: GroupDocker, Title: "Docker is up to date", Timeout: 12 * time.Second, Run: checkDockerUpdatePending},
		Check{Code: "containers_policy", Group: GroupDocker, Title: "The containers that run come back by themselves", Timeout: 8 * time.Second, Run: checkContainersPolicy},
		Check{Code: "containers_ports", Group: GroupDocker, Title: "No container is the machine's DNS or web server", Timeout: 8 * time.Second, Run: checkContainersPorts},
		Check{Code: "backup_age", Group: GroupDocker, Title: "The apps have a recent backup", Run: checkBackupAge},
		Check{Code: "snapshot_reminder", Group: GroupDocker, Title: "There is a way back from the upgrade", Run: checkSnapshotReminder},
	)
}

// An engine is the Docker engine that is on the box, and where it came from. Origin is "" when
// there is none.
type engine struct {
	Package string
	Version string
	Origin  dockerpkg.Origin
	// Policy is what "apt-cache policy" says of the package: the versions apt knows of.
	Policy string
}

// findEngine looks for docker-ce, then the distribution's docker.io, then the snap. docker-ce is
// of Docker's repository when apt knows a version of it from there.
func findEngine(ctx context.Context, p Probe) (engine, error) {
	installed, err := installedPackages(ctx, p, "docker-ce", "docker.io")
	if err != nil {
		return engine{}, err
	}
	for _, name := range []string{"docker-ce", "docker.io"} {
		i := slices.IndexFunc(installed, func(pkg installedPackage) bool { return pkg.Name == name })
		if i < 0 {
			continue
		}
		out, err := p.Run(ctx, aptPolicyArgs(name)...)
		if err != nil {
			return engine{}, err
		}
		if out.ExitCode != 0 {
			return engine{}, errToolFailed
		}
		found := engine{Package: name, Version: installed[i].Version, Origin: dockerpkg.OriginDistribution, Policy: out.Text}
		if name == "docker-ce" {
			found.Origin = dockerpkg.OriginFromPolicy(out.Text)
		}
		return found, nil
	}
	out, err := p.Run(ctx, "snap", "list", "docker")
	switch {
	case isToolMissing(err):
		return engine{}, nil
	case err != nil:
		return engine{}, err
	case out.ExitCode == 0:
		return engine{Origin: dockerpkg.OriginSnap}, nil
	}
	return engine{}, nil
}

// A daemon is what "docker info" says: that there is a docker at all, that a daemon answers it, and what it
// says of itself (its swarm's state, as the daemon says it).
type daemon struct {
	Tool, Running bool
	SwarmState    string
}

// dockerUnit is the service of Docker's daemon. Docker's socket is a unit of its own that starts the service
// at the first connection of a client: the daemon is asked only when the service runs, since asking a
// stopped one would start it, and with it the containers that have a restart policy.
const dockerUnit = "docker.service"

// unitProperty is a property of a unit, as systemctl show says it (one line).
func unitProperty(ctx context.Context, p Probe, unit, property string) (string, error) {
	out, err := p.Run(ctx, "systemctl", "show", unit, "--property="+property, "--value")
	if err != nil {
		return "", err
	}
	if out.ExitCode != 0 {
		return "", errToolFailed
	}
	return firstLine(out.Text), nil
}

// askDaemon asks the daemon how it is. A service that does not run is a daemon that is down, and the daemon
// is not asked; one that does not answer (the command is refused) is down as well; one that does not answer
// in time is an error of the check.
func askDaemon(ctx context.Context, p Probe) (daemon, error) {
	state, err := unitProperty(ctx, p, dockerUnit, "ActiveState")
	if err != nil {
		return daemon{}, err
	}
	switch state {
	case "active", "activating", "reloading":
		// the daemon runs or is starting: it can be asked, and asking it starts nothing
	case "inactive", "failed", "deactivating", "maintenance":
		// there is a Docker when its service has a unit file (one that is masked is one that is there)
		load, err := unitProperty(ctx, p, dockerUnit, "LoadState")
		if err != nil {
			return daemon{}, err
		}
		return daemon{Tool: load != "not-found"}, nil
	default:
		return daemon{}, errUnexpectedOutput
	}
	out, err := p.Run(ctx, "docker", "info", "--format", dockerpkg.DaemonInfoFormat)
	if isToolMissing(err) {
		return daemon{}, nil
	}
	if err != nil {
		return daemon{}, err
	}
	if out.ExitCode != 0 {
		return daemon{Tool: true}, nil
	}
	info, err := dockerpkg.ParseDaemonInfo(out.Text)
	if err != nil {
		return daemon{Tool: true}, nil
	}
	return daemon{Tool: true, Running: true, SwarmState: info.SwarmState}, nil
}

func checkDockerSwarm(ctx context.Context, p Probe) Result {
	d, err := askDaemon(ctx, p)
	if err != nil {
		return unknownFor(err)
	}
	if !d.Running {
		return pass()
	}
	// a node is of a swarm in every state of its swarm but "inactive" (or none): it has joined one, it is
	// about to, it is locked after a restart, or its swarm is in error, and the restarts of the upgrade reach
	// it in each. A state that is none of these is not an answer.
	switch d.SwarmState {
	case "", "inactive":
		return pass()
	case "active", "pending", "locked", "error":
	default:
		return unknownWith(ReasonUnexpectedOutput)
	}
	return Result{
		Severity: Refuse, Title: "This machine is part of a Docker swarm",
		Detail: "A node of a swarm is scheduled by the swarm, which the upgrade does not know about: its services would be moved, or lost, by the restarts. Take the node out of the swarm first.",
	}
}

func checkDockerOrigin(ctx context.Context, p Probe) Result {
	found, err := findEngine(ctx, p)
	if err != nil {
		return unknown(err)
	}
	if found.Origin == "" {
		// a docker that no package and no snap brought was put there by hand
		d, err := askDaemon(ctx, p)
		if err != nil {
			return unknownFor(err)
		}
		if !d.Tool {
			return Result{Severity: OK, Data: D{"installed": false}}
		}
		found.Origin = dockerpkg.OriginUnknown
	}
	if found.Origin == dockerpkg.OriginDockerRepo {
		return Result{Severity: OK, Data: D{"installed": true, "origin": string(found.Origin)}}
	}
	return Result{
		Severity: Refuse, Title: "Docker does not come from Docker's own repository",
		Detail: "Only docker-ce from Docker's repository (download.docker.com) is kept through the upgrade, with its source paused and put back after it. The docker.io package of the distribution, the snap and a Docker put there by hand are upgraded with the rest of the system, or not at all, and the machine's apps are lost with them.",
		Data:   D{"origin": string(found.Origin)},
	}
}

func checkDockerDown(ctx context.Context, p Probe) Result {
	d, err := askDaemon(ctx, p)
	if err != nil {
		return unknownFor(err)
	}
	if !d.Tool || d.Running {
		return pass()
	}
	return Result{
		Severity: Warn, Title: "The Docker daemon does not answer",
		Detail: "Docker is installed and its daemon is not running, or refuses to answer: the apps are not running, and this report cannot say what the upgrade would do to them. Start it, and check again.",
		Action: ActionNone,
	}
}

func checkDockerUpdatePending(ctx context.Context, p Probe) Result {
	found, err := findEngine(ctx, p)
	if err != nil {
		return unknown(err)
	}
	if found.Package == "" {
		return pass()
	}
	installed, candidate := dockerpkg.PolicyVersions(found.Policy)
	if installed == "" || candidate == "" || compareVersions(candidate, installed) <= 0 {
		return pass()
	}
	data := D{"package": found.Package, "installed": installed, "candidate": candidate}
	// Docker's packages on hold are a decision of the owner's, which the Docker update of the System packages
	// window asks to confirm: the report does not tell him to update first. It says there is a newer one, that
	// the upgrade leaves Docker as it is, and where it can be updated, before or after.
	held, _, err := heldPackages(ctx, p)
	if err != nil {
		return unknown(err)
	}
	if len(held) > 0 {
		data["held"] = true
		return Result{
			Severity: Info, Title: "A newer Docker exists, and its packages are on hold",
			Detail: "apt knows a newer version of Docker than the one installed, and Docker's packages are on hold: the upgrade leaves Docker as it is, since its source is paused during it. It can be updated from the System packages window of the dashboard, before or after the upgrade (it asks to confirm, because of the hold).",
			Data:   data, Action: ActionNone,
		}
	}
	return Result{
		Severity: Warn, Title: "A newer Docker is waiting",
		Detail: "apt knows a newer version of Docker than the one installed. Its source is paused during the upgrade, so it is not updated by it: update Docker first, with the button of the System packages window, so that the upgrade starts from a Docker that is up to date.",
		Data:   data, Action: ActionNone,
	}
}

// containers are the containers that run now; nil when there is no daemon to ask.
func containers(ctx context.Context, p Probe) ([]dockerpkg.Container, error) {
	d, err := askDaemon(ctx, p)
	if err != nil || !d.Running {
		return nil, err
	}
	out, err := p.Run(ctx, "docker", "ps", "-q")
	if err != nil {
		return nil, err
	}
	if out.ExitCode != 0 {
		return nil, errToolFailed
	}
	if out.Truncated {
		return nil, ErrTooLarge
	}
	var ids []string
	for _, line := range lines(out.Text) {
		if containerIDArg.MatchString(line) && !slices.Contains(ids, line) {
			ids = append(ids, line)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > maxInspected {
		return nil, ErrTooLarge
	}
	inspected, err := p.Run(ctx, append([]string{"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat}, ids...)...)
	if err != nil {
		return nil, err
	}
	if inspected.Truncated {
		return nil, ErrTooLarge
	}
	list := dockerpkg.ParseContainers(inspected.Text)
	// a container that stopped between the two commands makes inspect fail for it, and print the others
	if inspected.ExitCode != 0 && len(list) == 0 {
		return nil, errToolFailed
	}
	return list, nil
}

func containerNames(list []dockerpkg.Container) []string {
	names := []string{}
	for _, c := range list {
		if !slices.Contains(names, c.Name) {
			names = append(names, c.Name)
		}
	}
	slices.Sort(names)
	return names
}

func checkContainersPolicy(ctx context.Context, p Probe) Result {
	list, err := containers(ctx, p)
	if err != nil {
		return unknownFor(err)
	}
	var wontReturn []dockerpkg.Container
	for _, c := range list {
		if c.RestartPolicy == "" || c.RestartPolicy == "no" {
			wontReturn = append(wontReturn, c)
		}
	}
	if len(wontReturn) == 0 {
		return pass()
	}
	return Result{
		Severity: Warn, Title: "Some containers do not start again by themselves",
		Detail: "The restart of the machine stops every container, and only the ones that have a restart policy start again. These have none (no, or nothing): they stay stopped until someone starts them.",
		Data:   D{"names": containerNames(wontReturn), "count": len(wontReturn)}, Action: ActionNone,
	}
}

// servicePorts are the host ports of a machine that is the network's DNS or web server.
var servicePorts = []int{53, 80, 443}

func checkContainersPorts(ctx context.Context, p Probe) Result {
	list, err := containers(ctx, p)
	if err != nil {
		return unknownFor(err)
	}
	var serving []dockerpkg.Container
	var ports []int
	hostNetwork := false
	for _, c := range list {
		found := c.HostNetwork
		hostNetwork = hostNetwork || c.HostNetwork
		for _, port := range c.Ports {
			if slices.Contains(servicePorts, port.HostPort) {
				found = true
				if !slices.Contains(ports, port.HostPort) {
					ports = append(ports, port.HostPort)
				}
			}
		}
		if found {
			serving = append(serving, c)
		}
	}
	if len(serving) == 0 {
		return pass()
	}
	sort.Ints(ports)
	listed := []string{}
	for _, port := range ports {
		listed = append(listed, strconv.Itoa(port))
	}
	return Result{
		Severity: Warn, Title: "A container may be the DNS or the web server of the network",
		Detail: "These containers publish port 53, 80 or 443, or share the machine's network: the machine may be the DNS server or the web server of the others on its network, and they lose it for as long as the upgrade restarts Docker and the machine.",
		Data:   D{"names": containerNames(serving), "ports": listed, "host_network": hostNetwork}, Action: ActionNone,
	}
}

// backupLog is where AppManagement records every backup it makes, and every one that fails.
const (
	backupLog    = "/var/lib/casaos/backups/runs.json"
	backupMaxAge = 7 * 24 * time.Hour
)

// A backupRun is a record of AppManagement's backup log, with the fields that matter here.
type backupRun struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	// Error is empty for a run that finished; Restore marks a run that put an app back, and is no backup.
	Error   string `json:"error"`
	Restore bool   `json:"restore"`
}

func checkBackupAge(ctx context.Context, p Probe) Result {
	data, found, err := readOptional(ctx, p, backupLog)
	if err != nil {
		return unknown(err)
	}
	var runs []backupRun
	if found && len(bytes.TrimSpace(data)) > 0 {
		// a log that does not parse is not one with nothing in it: AppManagement does not take it for that either
		if err := json.Unmarshal(data, &runs); err != nil {
			return unknownWith(ReasonUnexpectedOutput)
		}
	}
	now := p.Now()
	var last time.Time
	for _, run := range runs {
		at := run.FinishedAt
		if at.IsZero() {
			at = run.StartedAt
		}
		// a date that has not come (a clock that was wrong) is no proof that a backup was made
		if run.Restore || run.Error != "" || at.IsZero() || at.After(now.Add(24*time.Hour)) {
			continue
		}
		if at.After(last) {
			last = at
		}
	}
	if last.IsZero() {
		return Result{
			Severity: Warn, Title: "No backup of the apps was made",
			Detail: "AppManagement has no backup that succeeded: if the upgrade ends badly, the apps' data is what is lost. Make a backup of the apps first.",
			Data:   D{"none": true}, Action: ActionNone,
		}
	}
	age := now.Sub(last)
	days := int(age.Hours() / 24)
	if age <= backupMaxAge {
		return Result{Severity: OK, Data: D{"days": days}}
	}
	return Result{
		Severity: Warn, Title: "The last backup of the apps is old",
		Detail: "The last backup that succeeded is more than seven days old: if the upgrade ends badly, what the apps changed since is lost. Make a backup first.",
		Data:   D{"days": days, "last_backup": last.UTC().Format("2006-01-02")}, Action: ActionNone,
	}
}

func checkSnapshotReminder(context.Context, Probe) Result {
	return Result{
		Severity: Info, Title: "Take a snapshot, or keep a way to the console",
		Detail: "An upgrade of the system cannot be undone. Before it, take a snapshot of the machine (a virtual machine's, a provider's), or be sure of a screen and a keyboard, a serial line or a console in front of it: a tunnel such as Tailscale goes down with the network card. This report cannot check that you did.",
		Action: ActionNone,
	}
}
