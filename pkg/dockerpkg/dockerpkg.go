// Package dockerpkg knows which of a Debian-family box's packages are Docker's, where
// the installed Docker came from, and what an apt simulation says it would do to them.
//
// ReCasaOS updates system packages with one apt upgrade, and an upgrade of docker-ce or
// containerd.io restarts the Docker daemon, which stops every container until it is back.
// The System packages update therefore leaves this family out and shows it on a line of
// its own; this package is what tells the two apart. It runs nothing: callers give it
// text apt printed, and it gives back what is in it. Nothing here is ever built into a
// shell command from a version string: the manual commands are constants.
package dockerpkg

import (
	"bufio"
	"regexp"
	"strings"
)

// family is every package whose upgrade replaces, or restarts, the Docker engine or the
// parts ReCasaOS uses with it.
var family = map[string]struct{}{
	"docker-ce":                 {},
	"docker-ce-cli":             {},
	"docker-ce-rootless-extras": {},
	"containerd.io":             {},
	"docker-buildx-plugin":      {},
	"docker-compose-plugin":     {},
	"docker-model-plugin":       {},
	"docker.io":                 {},
	"docker-compose-v2":         {},
	"docker-buildx":             {},
	"containerd":                {},
}

// IsFamily reports whether name is one of the Docker engine's packages.
func IsFamily(name string) bool {
	_, ok := family[name]
	return ok
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9+.\-]*(:[a-z0-9]+)?$`)

// ValidName reports whether name looks like a Debian package name (with an optional
// architecture), the only kind of text that may be put into an apt command line.
func ValidName(name string) bool {
	return len(name) <= 128 && namePattern.MatchString(name)
}

// Origin is where the installed Docker came from.
type Origin string

const (
	// OriginDockerRepo is docker-ce from Docker's own apt repository, the one the
	// installer sets up: https://download.docker.com/linux/.
	OriginDockerRepo Origin = "docker-repository"
	// OriginDistribution is the docker.io package of Debian, Ubuntu or Raspberry Pi OS.
	OriginDistribution Origin = "distribution"
	// OriginSnap is the snap.
	OriginSnap Origin = "snap"
	// OriginUnknown is a Docker whose source this does not recognise: a mirror, a plain
	// http repository, a package installed by hand.
	OriginUnknown Origin = "unknown"
)

const dockerRepositoryPrefix = "https://download.docker.com/linux/"

var policySourcePattern = regexp.MustCompile(`^\s+(?:\*\*\*\s+)?\d+\s+(https?://\S+)\s`)

// OriginFromPolicy tells where docker-ce comes from, from the output of
// `apt-cache policy docker-ce`: Docker's repository when some version of the package is
// offered by https://download.docker.com/linux/, anything else is unknown.
func OriginFromPolicy(policy string) Origin {
	scanner := bufio.NewScanner(strings.NewReader(policy))
	for scanner.Scan() {
		if m := policySourcePattern.FindStringSubmatch(scanner.Text()); m != nil && strings.HasPrefix(m[1], dockerRepositoryPrefix) {
			return OriginDockerRepo
		}
	}
	return OriginUnknown
}

// ManualCommand is what an owner types to update Docker themselves, for an origin, or ""
// when there is no command this can stand behind. They are constants on purpose.
func ManualCommand(origin Origin) string {
	switch origin {
	case OriginDockerRepo:
		return "sudo apt-get update && sudo apt-get install --only-upgrade docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin"
	case OriginDistribution:
		return "sudo apt-get update && sudo apt-get install --only-upgrade docker.io"
	case OriginSnap:
		return "sudo snap refresh docker"
	default:
		return ""
	}
}

var engineVersionPattern = regexp.MustCompile(`^(?:\d+:)?(\d+(?:\.\d+)*)`)

// EngineVersion is the upstream version in a dpkg version: "5:29.8.1-1~ubuntu.22.04~jammy"
// is "29.8.1". It is "" for text that does not start with one.
func EngineVersion(dpkgVersion string) string {
	if m := engineVersionPattern.FindStringSubmatch(strings.TrimSpace(dpkgVersion)); m != nil {
		return m[1]
	}
	return ""
}

var (
	simulationInstPattern = regexp.MustCompile(`^Inst\s+(\S+)`)
	simulationRemvPattern = regexp.MustCompile(`^Remv\s+(\S+)`)
)

// Touched is what an apt simulation (`apt-get -s`) says it would do.
type Touched struct {
	// Docker is the packages of the Docker family it would install or upgrade.
	Docker []string
	// Removed is every package it would remove.
	Removed []string
}

// Touches reads the Inst and Remv lines of an apt simulation.
func Touches(simulation string) Touched {
	var touched Touched
	scanner := bufio.NewScanner(strings.NewReader(simulation))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if m := simulationInstPattern.FindStringSubmatch(line); m != nil && IsFamily(m[1]) {
			touched.Docker = append(touched.Docker, m[1])
		} else if m := simulationRemvPattern.FindStringSubmatch(line); m != nil {
			touched.Removed = append(touched.Removed, m[1])
		}
	}
	return touched
}
