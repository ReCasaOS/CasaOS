package dockerpkg

import (
	"regexp"
	"strings"
)

// The "Update Docker" button upgrades the engine's own packages and nothing else. This file
// is what says which packages those are, what a version string may look like before it is
// allowed near a command line, a log or a page, and when an upgrade changes the major version.

// EngineNames are the only packages the button may touch, sorted. Any other package in the
// transaction apt would run (an upgrade, a new package, a removal) makes it refuse. The
// distribution's own docker.io, containerd, docker-compose-v2 and docker-buildx are of the
// family (see IsFamily) and are not in this list on purpose: they are not Docker's repository.
//
// It is a variable so that it can be listed and shown; IsEngineName does not read it, so
// changing it at run time does not widen what the button may touch.
var EngineNames = []string{
	"containerd.io",
	"docker-buildx-plugin",
	"docker-ce",
	"docker-ce-cli",
	"docker-ce-rootless-extras",
	"docker-compose-plugin",
	"docker-model-plugin",
}

// engineSet is EngineNames as it was when the program started.
var engineSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(EngineNames))
	for _, name := range EngineNames {
		set[name] = struct{}{}
	}
	return set
}()

// IsEngineName reports whether name is one of EngineNames, with or without the architecture
// apt adds to a foreign package ("containerd.io:armhf").
func IsEngineName(name string) bool {
	_, ok := engineSet[base(name)]
	return ok
}

var versionPattern = regexp.MustCompile(`^([0-9]+:)?[0-9][A-Za-z0-9.+~-]*$`)

const maxVersionLen = 100

// ValidVersion reports whether v is a dpkg version made only of the characters of one: an
// optional epoch, then a digit and letters, digits and . + ~ -. It is the test a version
// passes before it is printed in a log, sent to the page or put in an environment variable
// or an apt argument ("name=version"). No shell character can pass it.
func ValidVersion(v string) bool {
	return len(v) <= maxVersionLen && versionPattern.MatchString(v)
}

// Major is the first number of an engine version: "29.8.0" is "29", "24.0.5" is "24". It
// takes the version Docker reports, or a whole dpkg version ("5:29.8.0-1~debian.11~bullseye",
// whose epoch is not the major). It is "" for text that does not start with digits. It is
// the number in its shortest form, so that "029" and "29" are the same major.
func Major(engineVersion string) string {
	version := EngineVersion(engineVersion)
	major, _, _ := strings.Cut(version, ".")
	if major = strings.TrimLeft(major, "0"); major == "" && version != "" {
		return "0"
	}
	return major
}

// MajorJump reports whether an upgrade from one engine version to another changes the major
// version. It is false when either of them has none.
func MajorJump(from, to string) bool {
	a, b := Major(from), Major(to)
	return a != "" && b != "" && a != b
}
