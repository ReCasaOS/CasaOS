package osreadiness

import (
	"regexp"
	"strconv"
	"strings"
)

// compareVersions orders two dpkg versions as dpkg does (deb-version(7)): an epoch, then the
// upstream version, then the revision, each compared in runs of digits and of non-digits, with
// "~" before everything, even the end. It is -1, 0 or 1. Two versions that are not the same text
// can be equal ("1.01" and "1.1").
func compareVersions(a, b string) int {
	epochA, restA := splitEpoch(a)
	epochB, restB := splitEpoch(b)
	if epochA != epochB {
		if epochA < epochB {
			return -1
		}
		return 1
	}
	upstreamA, revisionA := splitRevision(restA)
	upstreamB, revisionB := splitRevision(restB)
	if c := compareRuns(upstreamA, upstreamB); c != 0 {
		return c
	}
	return compareRuns(revisionA, revisionB)
}

func splitEpoch(v string) (int, string) {
	head, rest, ok := strings.Cut(v, ":")
	if !ok {
		return 0, v
	}
	epoch, err := strconv.Atoi(head)
	if err != nil {
		return 0, v
	}
	return epoch, rest
}

// splitRevision cuts the revision after the last "-"; a version with none has the revision "".
func splitRevision(v string) (upstream, revision string) {
	if i := strings.LastIndex(v, "-"); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// weight is the place of a character in the order of the non-digit runs.
func weight(c byte) int {
	switch {
	case c == '~':
		return -1
	case isDigit(c):
		return 0
	case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		return int(c)
	}
	return int(c) + 256
}

func compareRuns(a, b string) int {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		diff := 0
		for (i < len(a) && !isDigit(a[i])) || (j < len(b) && !isDigit(b[j])) {
			wa, wb := 0, 0
			if i < len(a) {
				wa = weight(a[i])
			}
			if j < len(b) {
				wb = weight(b[j])
			}
			if wa != wb {
				if wa < wb {
					return -1
				}
				return 1
			}
			i++
			j++
		}
		for i < len(a) && a[i] == '0' {
			i++
		}
		for j < len(b) && b[j] == '0' {
			j++
		}
		for i < len(a) && j < len(b) && isDigit(a[i]) && isDigit(b[j]) {
			if diff == 0 {
				diff = int(a[i]) - int(b[j])
			}
			i++
			j++
		}
		if i < len(a) && isDigit(a[i]) {
			return 1
		}
		if j < len(b) && isDigit(b[j]) {
			return -1
		}
		if diff != 0 {
			if diff < 0 {
				return -1
			}
			return 1
		}
	}
	return 0
}

// kernelRelease is a kernel as "uname -r" and /boot/vmlinuz-* say it: "5.10.0-35-amd64",
// "6.1.0-13-cloud-amd64", "5.15.0-91-generic", "6.5.0-0.deb12.4-amd64". Version is the numbers
// ("5.10.0-35") and Flavour what follows them ("amd64", "cloud-amd64").
type kernelRelease struct{ Version, Flavour string }

var kernelPattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)+-[0-9][^-]*)-([A-Za-z0-9._+~-]+)$`)

// parseKernel reads a kernel release; ok is false for text that is not one (a kernel built by
// hand), which is not compared.
func parseKernel(release string) (kernelRelease, bool) {
	m := kernelPattern.FindStringSubmatch(strings.TrimSpace(release))
	if m == nil {
		return kernelRelease{}, false
	}
	return kernelRelease{Version: m[1], Flavour: m[2]}, true
}

// newerKernel says whether a is a newer kernel than b of the same flavour: a "cloud" kernel is not
// compared with a "generic" one.
func newerKernel(a, b kernelRelease) bool {
	return a.Flavour == b.Flavour && compareVersions(a.Version, b.Version) > 0
}
