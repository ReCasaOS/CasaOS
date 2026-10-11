// Package dockerpkg knows which of a Debian-family box's packages are Docker's, where
// the installed Docker came from, and what an apt simulation says it would do to them.
//
// ReCasaOS updates system packages with an apt install of a list, and an upgrade of
// docker-ce or containerd.io restarts the Docker daemon, which stops every container until
// it is back. The System packages update therefore leaves this family out and shows it on a
// line of its own; this package is what tells the two apart. It runs nothing: callers give
// it text apt printed, and it gives back what is in it. Nothing here is ever built into a
// shell command from a version string, and a package name reaches a command only if it is
// one of the fixed names below.
package dockerpkg

import (
	"bufio"
	"regexp"
	"sort"
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

// restarts are the packages whose upgrade restarts the daemon (or containerd under it).
var restarts = map[string]struct{}{
	"docker-ce":     {},
	"containerd.io": {},
	"docker.io":     {},
	"containerd":    {},
}

// base is a package name without the architecture apt adds to a foreign one:
// "containerd.io:armhf" is "containerd.io".
func base(name string) string {
	b, _, _ := strings.Cut(name, ":")
	return b
}

// IsFamily reports whether name is one of the Docker engine's packages.
func IsFamily(name string) bool {
	_, ok := family[base(name)]
	return ok
}

// RestartsEngine reports whether upgrading any of names restarts Docker.
func RestartsEngine(names []string) bool {
	for _, name := range names {
		if _, ok := restarts[base(name)]; ok {
			return true
		}
	}
	return false
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

// CanSeeUpdates reports whether apt can tell of an update for an installation from this
// origin: of a snap, or of a Docker whose source is not known, it cannot.
func (o Origin) CanSeeUpdates() bool {
	return o == OriginDockerRepo || o == OriginDistribution
}

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

// defaultPackages is what to update by hand when apt has not said which packages are
// behind.
var defaultPackages = map[Origin][]string{
	OriginDockerRepo:   {"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"},
	OriginDistribution: {"docker.io"},
}

const (
	// manualHead is the start of the command that updates Docker's packages by hand.
	manualHead = "sudo apt-get update && sudo apt-get install --only-upgrade "
	// allowHeldFlag is what apt is told so that it may change a package that is on hold.
	allowHeldFlag = "--allow-change-held-packages"
)

// manualNames are the packages the command names, once each: the Docker packages among those apt says
// are pending, or the origin's defaults when it said none. It is nil for an origin that has no apt
// command (a snap's, an unknown one).
func manualNames(origin Origin, pending []string) map[string]struct{} {
	defaults, ok := defaultPackages[origin]
	if !ok {
		return nil
	}
	chosen := map[string]struct{}{}
	for _, name := range pending {
		if b := base(name); IsFamily(name) {
			chosen[b] = struct{}{}
		}
	}
	if len(chosen) == 0 {
		for _, name := range defaults {
			chosen[name] = struct{}{}
		}
	}
	return chosen
}

// sortedNames are the members of a set, sorted.
func sortedNames(set map[string]struct{}) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// holdNames are the names that go after "apt-mark hold": the Docker packages among names, as the box
// names them (no architecture), once each, sorted. Nothing else gets into a command.
func holdNames(names []string) []string {
	set := map[string]struct{}{}
	for _, name := range names {
		if IsFamily(name) && !strings.Contains(name, ":") {
			set[name] = struct{}{}
		}
	}
	return sortedNames(set)
}

// ManualCommand is what an owner types to update Docker themselves, for an origin, or ""
// when there is no command this can stand behind. It names the packages apt says are
// pending when it said some, so that running it leaves nothing behind, and only names from
// the fixed family: nothing apt printed reaches it unless it is one of them.
func ManualCommand(origin Origin, pending []string) string {
	if origin == OriginSnap {
		return "sudo snap refresh docker"
	}
	chosen := manualNames(origin, pending)
	if chosen == nil {
		return ""
	}
	return manualHead + strings.Join(sortedNames(chosen), " ")
}

// ManualCommandHeld is ManualCommand for a box that has Docker's packages on hold (held: the names of
// the ones that are, installed: the ones that are installed). apt fails when it is told only the package
// that is behind and a held sibling has to move with it (docker-ce 29 needs a newer containerd.io), so the
// command names every installed package of the engine, the same list the dashboard's plan is made of, and
// says apt may change what is held. dpkg drops the hold of every package it unpacks, so the command ends
// by putting the hold back on the ones that were held. For a box that holds nothing, and for an origin
// that has no apt command, it is ManualCommand, byte for byte.
func ManualCommandHeld(origin Origin, pending, installed, held []string) string {
	holds := holdNames(held)
	chosen := manualNames(origin, pending)
	if len(holds) == 0 || chosen == nil {
		return ManualCommand(origin, pending)
	}
	for _, name := range append(holdNames(installed), holds...) {
		chosen[name] = struct{}{}
	}
	return manualHead + allowHeldFlag + " " + strings.Join(sortedNames(chosen), " ") + " && sudo apt-mark hold " + strings.Join(holds, " ")
}

var (
	policyInstalledPattern = regexp.MustCompile(`(?m)^\s*Installed:\s*(\S+)\s*$`)
	policyCandidatePattern = regexp.MustCompile(`(?m)^\s*Candidate:\s*(\S+)\s*$`)
)

// PolicyVersions reads the installed and the candidate version from the output of
// `apt-cache policy <package>`: the candidate is the newest version apt would install. Each
// is "" when apt says "(none)" or says nothing.
func PolicyVersions(policy string) (installed, candidate string) {
	read := func(pattern *regexp.Regexp) string {
		if m := pattern.FindStringSubmatch(policy); m != nil && m[1] != "(none)" {
			return m[1]
		}
		return ""
	}
	return read(policyInstalledPattern), read(policyCandidatePattern)
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

// ContractPattern is an extended regular expression (for grep -E) that matches a line of
// an apt simulation that breaks the contract of the update: any removal, and, when the box
// has a Docker engine to protect, an install or upgrade of a package of the Docker family.
// The unit's shell line runs the simulation again just before it installs, and installs
// nothing if a line matches.
func ContractPattern(protectDocker bool) string {
	if !protectDocker {
		return `^Remv `
	}
	names := make([]string, 0, len(family))
	for name := range family {
		names = append(names, strings.ReplaceAll(name, ".", `\.`))
	}
	sort.Strings(names)
	return `^(Inst (` + strings.Join(names, "|") + `)[: ]|Remv )`
}
