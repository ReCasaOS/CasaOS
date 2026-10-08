package service

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// Docker is not part of the System packages update. Upgrading docker-ce or containerd.io
// restarts the Docker daemon and stops every container until it is back (the apps that
// have a restart policy start again by themselves, the others stay stopped), and an apt
// upgrade of everything used to do that without a word. The update now installs the
// packages the check lists, minus Docker's, and Docker is shown on a line of its own.

var (
	ErrSystemPackageNothingToUpdate = errors.New("there is nothing to update here: what is left is Docker's, which is updated on its own")
	ErrSystemPackageTouchesDocker   = errors.New("this update would also change Docker's packages")
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
	// ManualCommand is what to type to update Docker oneself; empty when this does not
	// know a command to stand behind.
	ManualCommand string `json:"manual_command,omitempty"`
}

const (
	systemDockerUpdateUnit = "casaos-docker-update.service" // reserved for an update of Docker on its own
	dpkgLockFile           = "/var/lib/dpkg/lock-frontend"
)

// systemMaintenanceUnits are the transient units that change the box's packages or the
// ReCasaOS release: at most one of them runs at a time, and none while dpkg's lock is held.
var systemMaintenanceUnits = []string{common.UPDATE_UNIT + ".service", systemPackageUpdateUnit, systemDockerUpdateUnit}

// maintenanceBusy says whether the box is being changed by something else than the unit
// named in except (the caller's own, which it checks in its own way): another of the
// units above is active, or a package manager holds dpkg's lock. Systemd or flock not
// answering is not busy: a refusal that cannot be explained helps nobody.
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
	if _, err := u.command(ctx, "flock", "--nonblock", "--exclusive", dpkgLockFile, "true"); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "another package manager holds dpkg's lock", true
		}
	}
	return "", false
}

// MaintenanceBusy is maintenanceBusy for a caller that is not one of the units: the
// automatic updater, which starts nothing while the box is being changed.
func (s *systemService) MaintenanceBusy() bool {
	_, busy := s.systemPackageUpdater().maintenanceBusy(context.Background(), common.UPDATE_UNIT+".service")
	return busy
}

// splitDockerUpdates separates what apt would upgrade in Docker's family from the rest.
func splitDockerUpdates(all []SystemPackageUpdate) (rest, docker []SystemPackageUpdate) {
	rest, docker = []SystemPackageUpdate{}, []SystemPackageUpdate{}
	for _, update := range all {
		if dockerpkg.IsFamily(update.Name) {
			docker = append(docker, update)
		} else {
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
	switch {
	case u.installedVersion(ctx, "docker-ce") != "":
		version := u.installedVersion(ctx, "docker-ce")
		info.Installed = true
		info.Version = dockerpkg.EngineVersion(version)
		policy, _ := u.command(ctx, "apt-cache", "policy", "docker-ce")
		info.Origin = string(dockerpkg.OriginFromPolicy(string(policy)))
	case u.installedVersion(ctx, "docker.io") != "":
		info.Installed = true
		info.Version = dockerpkg.EngineVersion(u.installedVersion(ctx, "docker.io"))
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
	info.ManualCommand = dockerpkg.ManualCommand(dockerpkg.Origin(info.Origin))
	return info
}

// installedVersion is the dpkg version of an installed package, or "".
func (u *systemPackageUpdater) installedVersion(ctx context.Context, name string) string {
	output, err := u.command(ctx, "dpkg-query", "-W", "-f=${db:Status-Abbrev}\t${Version}\n", name)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(output), "\n") {
		status, version, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && strings.HasPrefix(strings.TrimSpace(status), "ii") {
			return strings.TrimSpace(version)
		}
	}
	return ""
}

// systemPackageSimulateArgs are apt's arguments for a simulation that takes no lock.
func systemPackageSimulateArgs(rest ...string) []string {
	return append([]string{"-s", "--no-remove", "-o", "Debug::NoLocking=true", "-o", "Dpkg::Use-Pty=0"}, rest...)
}

// upgradeNames is the explicit list the update installs: what a plain upgrade would
// upgrade, minus Docker's packages. It is the contract of the exclusion: the list is
// made of names apt printed and checked as package names, and a second simulation of
// installing exactly that list must change nothing of Docker's and remove nothing.
func (u *systemPackageUpdater) upgradeNames(ctx context.Context, support systemPackageSupport) ([]string, error) {
	output, err := u.command(ctx, support.aptPath, systemPackageSimulateArgs("upgrade")...)
	if err != nil {
		return nil, fmt.Errorf("apt package update check failed: %s", trimSystemPackageOutput(output))
	}
	var names []string
	for _, update := range parseAPTUpgradeSimulation(string(output)) {
		if dockerpkg.IsFamily(update.Name) || update.CurrentVersion == "" {
			// Docker's own, or a package apt would install new rather than upgrade
			continue
		}
		if !dockerpkg.ValidName(update.Name) {
			return nil, fmt.Errorf("apt named a package that is not a package name: %q", update.Name)
		}
		names = append(names, update.Name)
	}
	if len(names) == 0 {
		return nil, ErrSystemPackageNothingToUpdate
	}

	args := systemPackageSimulateArgs(append([]string{"install", "--only-upgrade", "--no-install-recommends"}, names...)...)
	output, err = u.command(ctx, support.aptPath, args...)
	if err != nil {
		return nil, fmt.Errorf("apt package update check failed: %s", trimSystemPackageOutput(output))
	}
	touched := dockerpkg.Touches(string(output))
	if len(touched.Docker) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrSystemPackageTouchesDocker, strings.Join(touched.Docker, ", "))
	}
	if len(touched.Removed) > 0 {
		return nil, fmt.Errorf("this update would remove packages (%s), which the System packages update never does", strings.Join(touched.Removed, ", "))
	}
	return names, nil
}
