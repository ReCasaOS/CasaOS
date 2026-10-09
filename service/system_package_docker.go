package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// Docker is not part of the System packages update. Upgrading docker-ce or containerd.io
// restarts the Docker daemon and stops every container until it is back (the apps that
// have a restart policy start again by themselves, the others stay stopped), and an apt
// upgrade of everything used to do that without a word. The update now installs the
// packages the check lists, minus Docker's, and Docker is shown on a line of its own. This
// holds only on a box that has a Docker engine: containerd on a Kubernetes node, say, is
// an ordinary package there.

var (
	ErrSystemPackageNothingToUpdate = errors.New("there is nothing to update")
	ErrSystemPackageTouchesDocker   = errors.New("this update would also change Docker's packages")
	ErrSystemPackageListChanged     = errors.New("the update is no longer the list that was checked")
	ErrSystemMaintenanceBusy        = errors.New("another update or package operation is running on this box")
)

// SystemPackageDocker is the Docker line of the System packages check.
type SystemPackageDocker struct {
	Installed bool `json:"installed"`
	// Origin is where Docker came from: see dockerpkg.Origin.
	Origin string `json:"origin,omitempty"`
	// Version is the engine's, from its package: 29.8.1.
	Version string `json:"version,omitempty"`
	// Updates is what apt offers for Docker's packages; none of it is installed by the
	// System packages update.
	Updates []SystemPackageUpdate `json:"updates"`
	// Candidate is a newer engine version that apt knows of and does not offer for an
	// update, with Held saying whether the package is on hold: said, rather than "up to
	// date", of a Docker that is behind and that this update cannot move.
	Candidate string `json:"candidate,omitempty"`
	Held      bool   `json:"held,omitempty"`
	// RestartsDocker is whether installing those updates restarts the daemon: the engine's
	// own packages do, a plugin or the client do not.
	RestartsDocker bool `json:"restarts_docker"`
	// ManualCommand is what to type to update Docker oneself; empty when this does not
	// know a command to stand behind.
	ManualCommand string `json:"manual_command,omitempty"`
}

const systemDockerUpdateUnit = "casaos-docker-update.service" // reserved for an update of Docker on its own

// systemMaintenanceUnits are the transient units that change the box's packages or the
// ReCasaOS release: at most one of them runs at a time, and none while a package manager
// holds dpkg's lock.
var systemMaintenanceUnits = []string{common.UPDATE_UNIT + ".service", systemPackageUpdateUnit, systemDockerUpdateUnit}

// maintenanceBusy says whether the box is being changed by something else than the unit
// named in except (the caller's own, which it checks in its own way): another of the units
// above is active, or, on a host that has dpkg, a package manager holds its lock. Systemd
// not answering is not busy: a refusal that cannot be explained helps nobody.
func (u *systemPackageUpdater) maintenanceBusy(ctx context.Context, except string) (string, bool) {
	for _, unit := range systemMaintenanceUnits {
		if unit == except {
			continue
		}
		output, err := u.command(ctx, "systemctl", "show", unit, "--property=ActiveState", "--value")
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(output)) {
		case "active", "activating", "deactivating":
			return fmt.Sprintf("%s is running", strings.TrimSuffix(unit, ".service")), true
		}
	}
	// the lock is dpkg's: a host that has no dpkg has none to hold
	if u.dpkgLocked != nil && u.support().supported && u.dpkgLocked() {
		return "another package manager holds dpkg's lock", true
	}
	return "", false
}

// MaintenanceBusy is maintenanceBusy for a caller that is not one of the units: the
// automatic updater, which starts nothing while the box is being changed.
func (s *systemService) MaintenanceBusy() bool {
	_, busy := s.systemPackageUpdater().maintenanceBusy(context.Background(), common.UPDATE_UNIT+".service")
	return busy
}

// engineInstalled says whether this box has a Docker engine at all: docker-ce, the
// distribution's docker.io, or the snap.
func (u *systemPackageUpdater) engineInstalled(ctx context.Context) bool {
	if u.installedVersion(ctx, "docker-ce") != "" || u.installedVersion(ctx, "docker.io") != "" {
		return true
	}
	_, err := u.command(ctx, "snap", "list", "docker")
	return err == nil
}

// splitDockerUpdates separates what apt would upgrade in Docker's family from the rest,
// when the box has a Docker engine; without one everything is the rest. A package that apt
// would install new rather than upgrade is not an update and is left out of both.
func splitDockerUpdates(all []SystemPackageUpdate, engine bool) (rest, docker []SystemPackageUpdate) {
	rest, docker = []SystemPackageUpdate{}, []SystemPackageUpdate{}
	for _, update := range all {
		switch {
		case update.CurrentVersion == "":
			continue
		case engine && dockerpkg.IsFamily(update.Name):
			docker = append(docker, update)
		default:
			rest = append(rest, update)
		}
	}
	return rest, docker
}

// dockerInfo is the Docker line: nil when there is no Docker here and nothing of its
// family to upgrade, so that a box without Docker shows none.
func (u *systemPackageUpdater) dockerInfo(ctx context.Context, pending []SystemPackageUpdate) *SystemPackageDocker {
	info := &SystemPackageDocker{Updates: pending}
	if info.Updates == nil {
		info.Updates = []SystemPackageUpdate{}
	}
	var enginePackage, policyText string
	switch {
	case u.installedVersion(ctx, "docker-ce") != "":
		info.Installed = true
		enginePackage = "docker-ce"
		info.Version = dockerpkg.EngineVersion(u.installedVersion(ctx, "docker-ce"))
		policy, _ := u.command(ctx, "apt-cache", "policy", "docker-ce")
		policyText = string(policy)
		info.Origin = string(dockerpkg.OriginFromPolicy(policyText))
	case u.installedVersion(ctx, "docker.io") != "":
		info.Installed = true
		enginePackage = "docker.io"
		info.Version = dockerpkg.EngineVersion(u.installedVersion(ctx, "docker.io"))
		policy, _ := u.command(ctx, "apt-cache", "policy", "docker.io")
		policyText = string(policy)
		info.Origin = string(dockerpkg.OriginDistribution)
	default:
		if _, err := u.command(ctx, "snap", "list", "docker"); err == nil {
			info.Installed = true
			info.Origin = string(dockerpkg.OriginSnap)
		}
	}
	if !info.Installed && len(info.Updates) == 0 {
		return nil
	}
	names := make([]string, 0, len(info.Updates))
	for _, update := range info.Updates {
		names = append(names, update.Name)
	}
	// an engine behind a version apt knows of, with no update listed for it: held, or kept
	// back by apt's own rules. "Up to date" would be false of it.
	if enginePackage != "" && !containsName(names, enginePackage) {
		installed, candidate := dockerpkg.PolicyVersions(policyText)
		if candidate != "" && installed != "" && candidate != installed && u.isNewer(ctx, candidate, installed) {
			info.Candidate = dockerpkg.EngineVersion(candidate)
			info.Held = u.packageHeld(ctx, enginePackage)
			names = append(names, enginePackage)
		}
	}
	info.RestartsDocker = dockerpkg.RestartsEngine(names)
	info.ManualCommand = dockerpkg.ManualCommandHeld(dockerpkg.Origin(info.Origin), names, info.Held)
	return info
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// isNewer says whether dpkg orders candidate after installed.
func (u *systemPackageUpdater) isNewer(ctx context.Context, candidate, installed string) bool {
	_, err := u.command(ctx, "dpkg", "--compare-versions", candidate, "gt", installed)
	return err == nil
}

// packageHeld says whether the package is on hold (apt-mark hold): dpkg's abbreviation
// starts with "h".
func (u *systemPackageUpdater) packageHeld(ctx context.Context, name string) bool {
	output, err := u.command(ctx, "dpkg-query", "-W", "-f=${db:Status-Abbrev}\t${Version}\n", name)
	if err != nil {
		return false
	}
	status, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\t")
	return strings.HasPrefix(strings.TrimSpace(status), "h")
}

// installedVersion is the dpkg version of a package that is on the box (installed, or held,
// or half-configured: anything but removed, purged or never there), or "".
func (u *systemPackageUpdater) installedVersion(ctx context.Context, name string) string {
	output, err := u.command(ctx, "dpkg-query", "-W", "-f=${db:Status-Abbrev}\t${Version}\n", name)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(output), "\n") {
		status, version, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		status = strings.TrimSpace(status)
		// dpkg's abbreviation is the wanted state, then the state: "ii", "hi" for a held
		// package, "iF" half-configured; "un" is not installed and "rc" only has its
		// configuration files left
		if ok && len(status) >= 2 && status[1] != 'n' && status[1] != 'c' && strings.TrimSpace(version) != "" {
			return strings.TrimSpace(version)
		}
	}
	return ""
}

// systemPackageSimulateArgs are apt's arguments for a simulation that takes no lock.
func systemPackageSimulateArgs(rest ...string) []string {
	return append([]string{"-s", "--no-remove", "-o", "Debug::NoLocking=true", "-o", "Dpkg::Use-Pty=0"}, rest...)
}

var aptRemovalRefused = regexp.MustCompile(`(?i)remove is disabled|packages need to be removed`)

// upgradeNames is the explicit list the update installs: what a plain upgrade would
// upgrade, minus Docker's packages when the box has Docker. It is the contract of the
// exclusion: the list is made of names apt printed and checked as package names, and a
// second simulation of installing exactly that list must change nothing of Docker's,
// remove nothing and install nothing that is not an upgrade. The unit's own shell line
// runs the same simulation again right before it installs.
func (u *systemPackageUpdater) upgradeNames(ctx context.Context, support systemPackageSupport) (names []string, protectDocker bool, err error) {
	output, err := u.command(ctx, support.aptPath, systemPackageSimulateArgs("upgrade")...)
	if err != nil {
		return nil, false, fmt.Errorf("apt package update check failed: %s", trimSystemPackageOutput(output))
	}
	engine := u.engineInstalled(ctx)
	var keptDocker int
	for _, update := range parseAPTUpgradeSimulation(string(output)) {
		switch {
		case update.CurrentVersion == "":
			// a package apt would install new rather than upgrade
		case engine && dockerpkg.IsFamily(update.Name):
			keptDocker++
		case !dockerpkg.ValidName(update.Name):
			// not a name that may go into a command line: left out, not a reason to stop
		default:
			names = append(names, update.Name)
		}
	}
	if len(names) == 0 {
		if keptDocker > 0 {
			return nil, engine, fmt.Errorf("%w: what is left is Docker's, which is updated on its own", ErrSystemPackageNothingToUpdate)
		}
		return nil, engine, ErrSystemPackageNothingToUpdate
	}

	args := systemPackageSimulateArgs(append([]string{"install", "--only-upgrade", "--no-install-recommends"}, names...)...)
	output, err = u.command(ctx, support.aptPath, args...)
	if err != nil {
		if aptRemovalRefused.Match(output) {
			return nil, engine, fmt.Errorf("%w: it would have to remove packages, which the System packages update never does", ErrSystemPackageListChanged)
		}
		return nil, engine, fmt.Errorf("apt package update check failed: %s", trimSystemPackageOutput(output))
	}
	touched := dockerpkg.Touches(string(output))
	if engine && len(touched.Docker) > 0 {
		return nil, engine, fmt.Errorf("%w: %s", ErrSystemPackageTouchesDocker, strings.Join(touched.Docker, ", "))
	}
	if len(touched.Removed) > 0 {
		return nil, engine, fmt.Errorf("%w: it would remove %s, which the System packages update never does", ErrSystemPackageListChanged, strings.Join(touched.Removed, ", "))
	}
	var added []string
	for _, update := range parseAPTUpgradeSimulation(string(output)) {
		if update.CurrentVersion == "" {
			added = append(added, update.Name)
		}
	}
	if len(added) > 0 {
		return nil, engine, fmt.Errorf("%w: it would install %s, which are not upgrades", ErrSystemPackageListChanged, strings.Join(added, ", "))
	}
	return names, engine, nil
}
