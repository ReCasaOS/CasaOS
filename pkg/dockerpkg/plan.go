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

// PlanPackage is one `Inst` line of an apt simulation: a package it would install or upgrade.
// The versions are the whole dpkg strings ("5:29.8.0-1~debian.11~bullseye"), epoch and all.
type PlanPackage struct {
	Name      string `json:"name"`
	Current   string `json:"current_version"`
	Candidate string `json:"candidate_version"`
	// New is a package that is not installed yet: apt printed no [current version].
	New bool `json:"new,omitempty"`
}

// offending reports whether the button must refuse a transaction that holds this package: it
// is not one of the engine's, or its name or a version is not text that may go near a command
// line. A package that is New has no current version, and "" is not a version: it is refused
// with no rule of its own.
func (pkg PlanPackage) offending() bool {
	return !IsEngineName(pkg.Name) || !ValidName(pkg.Name) || !ValidVersion(pkg.Current) || !ValidVersion(pkg.Candidate)
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
	// brackets when there is one, and the candidate that opens the round brackets.
	planInstPattern = regexp.MustCompile(`^(\S+)(?:\s+\[([^\]]*)\])?(?:\s+\((\S+))?`)
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
			pkg.Name, pkg.Current, pkg.Candidate = im[1], im[2], im[3]
			pkg.New = pkg.Current == ""
		}
		plan.Packages = append(plan.Packages, pkg)
	}
	return plan
}

// Offending lists, sorted and once each, what makes the plan unacceptable to the button: a
// package that would be removed, a package that is not one of EngineNames, a package that is
// not installed yet, and a package whose name or versions fail ValidName and ValidVersion.
// Every item is a valid name or InvalidName. It is empty (not nil) when the plan is within
// bounds; a plan with no package at all is within bounds, and it is for the caller to say
// that there is nothing to update.
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
	for _, pkg := range p.Packages {
		if pkg.offending() {
			add(pkg.Name)
		}
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

// Pins are the "name=candidate" arguments that make apt install exactly this plan, sorted.
// Only a package that Offending does not name is in them, so nothing that failed validation
// can get into an argument by a caller that forgot to ask. Never nil.
func (p Plan) Pins() []string {
	pins := []string{}
	for _, pkg := range p.Packages {
		if !pkg.offending() {
			pins = append(pins, pkg.Name+"="+pkg.Candidate)
		}
	}
	sort.Strings(pins)
	return pins
}

// Names are the names of the packages of the plan, sorted, under the same rule as Pins.
// Never nil.
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
