package dockerpkg

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// InvalidName stands in, in Offending, for a package name that is not fit to be shown: the
// list goes to a log and to the page, and what apt printed there is not trusted until it
// has passed ValidName.
const InvalidName = "(invalid name)"

// MaxNewPackages is how many packages that are not installed yet an update of the engine may
// bring in. Docker 29 needs nftables, and nftables its libraries: a handful. Ten is room for
// that, and for a repository that moves a dependency or two, and not for an apt that has
// understood something else.
const MaxNewPackages = 10

// TooManyNew stands in, in Offending, for the count: a plan that installs more than
// MaxNewPackages packages that are not installed yet. It is not a name, and tells itself so.
const TooManyNew = "(more than 10 new packages)"

// PlanPackage is one `Inst` line of an apt simulation: a package it would install or upgrade.
// The versions are the whole dpkg strings ("5:29.8.0-1~debian.11~bullseye"), epoch and all.
type PlanPackage struct {
	Name      string `json:"name"`
	Current   string `json:"current_version"`
	Candidate string `json:"candidate_version"`
	// New is a package that is not installed yet: apt printed no [current version] at all (a
	// pair of brackets with nothing in them is not that, and is refused).
	New bool `json:"new,omitempty"`
}

// IsDistributionDocker reports whether name is one of the distribution's own Docker packages
// (docker.io, containerd, docker-compose-v2, docker-buildx): of the Docker family and not of
// the engine's. They conflict with Docker's own packages, and apt bringing one in beside
// docker-ce would be apt choosing the other Docker.
func IsDistributionDocker(name string) bool {
	return IsFamily(name) && !IsEngineName(name)
}

// offending reports whether the button must refuse a transaction that holds this package. An
// upgrade must be of one of the engine's packages (the update must not drag the base system
// along), whatever its name or versions say. A package that is new may be any package a
// Docker version depends on, except a distribution's Docker package. Whatever it is, its name
// and versions must be text that may go near a command line.
func (pkg PlanPackage) offending() bool {
	if !ValidName(pkg.Name) || !ValidVersion(pkg.Candidate) {
		return true
	}
	if pkg.New {
		return IsDistributionDocker(pkg.Name)
	}
	return !IsEngineName(pkg.Name) || !ValidVersion(pkg.Current)
}

// Plan is what an apt simulation says it would do.
type Plan struct {
	// Packages are the Inst lines, in the order apt printed them.
	Packages []PlanPackage
	// Removed is every package it would remove (a Remv or Purg line), as apt printed it:
	// show Offending, not this.
	Removed []string
}

var (
	planLinePattern = regexp.MustCompile(`^(Inst|Remv|Purg)(?:\s+(.*))?$`)
	// planInstPattern reads what follows "Inst ": the name, the installed version in square
	// brackets when there is one (the second group is the brackets, to tell none from empty
	// ones), and the candidate that opens the round brackets.
	planInstPattern = regexp.MustCompile(`^(\S+)(?:\s+(\[([^\]]*)\]))?(?:\s+\((\S+))?`)
)

// ParsePlan reads the Inst, Remv and Purg lines of `apt-get -s`:
//
//	Inst docker-ce [5:28.0.4-1~debian.11~bullseye] (5:29.8.0-1~debian.11~bullseye Docker CE:bullseye [amd64])
//	Inst docker-model-plugin (1.0.0-1~debian.11~bullseye Docker CE:bullseye [amd64])
//	Remv docker-compose-v2 [2.1.0]
//
// It fails closed: a line that begins like one of those and is not shaped like it still
// counts, as a package with what could be read of it, so that Offending names it.
func ParsePlan(simulation string) Plan {
	var plan Plan
	// not a bufio.Scanner: its line limit would stop at a long line and hide the rest
	for _, line := range strings.Split(simulation, "\n") {
		m := planLinePattern.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if m[1] != "Inst" {
			name := ""
			if fields := strings.Fields(m[2]); len(fields) > 0 {
				name = fields[0]
			}
			plan.Removed = append(plan.Removed, name)
			continue
		}
		pkg := PlanPackage{New: true}
		if im := planInstPattern.FindStringSubmatch(m[2]); im != nil {
			pkg.Name, pkg.Current, pkg.Candidate = im[1], im[3], im[4]
			pkg.New = im[2] == ""
		}
		plan.Packages = append(plan.Packages, pkg)
	}
	return plan
}

// Offending lists, sorted and once each, what makes the plan unacceptable to the button. A
// plan is acceptable when nothing is removed, ever; every upgrade is of one of EngineNames;
// every package that is new (not installed yet: a dependency of the new Docker, say) is not a
// distribution's Docker package; there are at most MaxNewPackages of those; and every name
// and version passes ValidName and ValidVersion. What is not acceptable is named: a package
// that would be removed, an upgrade of a package that is not the engine's, a new package that
// is a distribution's Docker, anything with a name or a version that fails validation.
//
// Every item is a valid name, or a fixed text that cannot be one: InvalidName, and TooManyNew
// for a plan with too many new packages (their names are not listed). It is empty (not nil)
// when the plan is within bounds; a plan with no package at all is within bounds, and it is
// for the caller to say that there is nothing to update.
func (p Plan) Offending() []string {
	seen := map[string]struct{}{}
	add := func(name string) {
		if !ValidName(name) {
			name = InvalidName
		}
		seen[name] = struct{}{}
	}
	for _, name := range p.Removed {
		add(name)
	}
	var fresh int
	for _, pkg := range p.Packages {
		if pkg.New {
			fresh++
		}
		if pkg.offending() {
			add(pkg.Name)
		}
	}
	if fresh > MaxNewPackages {
		seen[TooManyNew] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ID identifies the plan: the lower case hex SHA-256 of the lines "name current>candidate"
// of its packages, sorted and joined with "\n". It does not depend on the order apt printed
// them in and changes with any name or version, so that "I confirmed this" can be told
// from "apt now says something else".
func (p Plan) ID() string {
	lines := make([]string, 0, len(p.Packages))
	for _, pkg := range p.Packages {
		lines = append(lines, pkg.Name+" "+pkg.Current+">"+pkg.Candidate)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// Pins are the "name=candidate" arguments that make apt upgrade the packages of this plan to
// the versions that were confirmed, sorted. They are the upgrades only: with --only-upgrade
// apt ignores a named package that is not on the box, so a pin of a new package would pin
// nothing, and the new packages are the ones apt itself brings in as dependencies (Names is
// what bounds them). Only a package that Offending does not name is in them, so nothing that
// failed validation can get into an argument by a caller that forgot to ask. Never nil.
func (p Plan) Pins() []string {
	pins := []string{}
	for _, pkg := range p.Packages {
		if !pkg.New && !pkg.offending() {
			pins = append(pins, pkg.Name+"="+pkg.Candidate)
		}
	}
	sort.Strings(pins)
	return pins
}

// Names are the names of every package the plan allows apt to install, sorted: the upgrades
// of Pins and the packages that are new. It is the list the unit's guard holds each line of
// apt's simulation against, under the same rule as Pins. Never nil.
func (p Plan) Names() []string {
	names := []string{}
	for _, pkg := range p.Packages {
		if !pkg.offending() {
			names = append(names, pkg.Name)
		}
	}
	sort.Strings(names)
	return names
}

// Dependencies are the names of the packages that are new and are not the engine's own, sorted:
// what the new Docker needs and the box does not have (nftables, say). Under the same rule as
// Pins. Never nil.
func (p Plan) Dependencies() []string {
	names := []string{}
	for _, pkg := range p.Packages {
		if pkg.New && !pkg.offending() && !IsEngineName(pkg.Name) {
			names = append(names, pkg.Name)
		}
	}
	sort.Strings(names)
	return names
}

// Engine is the engine version (digits and dots, see EngineVersion) the plan upgrades docker-ce
// from and to. When docker-ce is not in the plan, because only a plugin or containerd.io
// moves, both are "" and the caller says what is installed.
func (p Plan) Engine() (from, to string) {
	for _, pkg := range p.Packages {
		if base(pkg.Name) == "docker-ce" {
			return EngineVersion(pkg.Current), EngineVersion(pkg.Candidate)
		}
	}
	return "", ""
}
