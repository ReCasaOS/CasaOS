package service

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// The "Update Docker" button upgrades the engine's own packages (dockerpkg.EngineNames), and
// installs the packages the new engine depends on that the box does not have yet (Docker 29
// needs nftables), and nothing else. This file is what decides, before the button is offered
// and again before it does anything, whether it may: the answer is a refusal code that the
// page turns into text, never a sentence it has to read, and the plan, the transaction apt
// itself says it would make, which the owner confirms and which is checked again when the
// update starts.

// The reasons the button is refused for, in the order they are looked at: the first that
// applies is the one given (unsupported, origin, held, dpkg, daemon, swarm, plan, disk). The page
// maps these codes to its own words. A hold is not one for the check: it is said, with the plan
// (SystemPackageDockerUpdate.Held), and the start refuses it until the owner has allowed it.
const (
	// DockerRefusalUnsupported is a host that is not Debian-family root with apt-get and
	// systemd-run.
	DockerRefusalUnsupported = "unsupported"
	// DockerRefusalOrigin is an engine that is not docker-ce from Docker's own repository.
	DockerRefusalOrigin = "origin"
	// DockerRefusalHeld is docker-ce, or another package of the engine, on hold, when the owner
	// has not said that the update may go ahead of the hold. The hold is the owner's: the update
	// goes ahead of it as the terminal command with --allow-change-held-packages does, which drops
	// the hold of the packages dpkg unpacks, and the unit puts it back and says whether it did
	// (HOLDS, holds_kept).
	DockerRefusalHeld = "held"
	// DockerRefusalDpkg is a package operation that was interrupted and left unfinished (a power
	// cut in the middle of an install): the simulations take no lock, so they pass, and the first
	// real apt call refuses, which the run would report as a failed download.
	DockerRefusalDpkg = "dpkg"
	// DockerRefusalDaemon is a Docker that does not answer: no docker command, or the daemon
	// does not tell its version in time.
	DockerRefusalDaemon = "daemon"
	// DockerRefusalSwarm is a node that is part of a swarm.
	DockerRefusalSwarm = "swarm"
	// DockerRefusalPlan is a transaction that is not an upgrade of the engine's own packages
	// and the packages they need: it failed, or it removes a package, or upgrades another, or
	// installs a distribution's Docker package or more than dockerpkg.MaxNewPackages new ones.
	DockerRefusalPlan = "plan"
	// DockerRefusalDisk is too little room for the packages.
	DockerRefusalDisk = "disk"
)

const (
	// systemDockerHeldFlag is what apt is told, when the owner has allowed it, so that it may change a
	// package that is on hold: the terminal command's own.
	systemDockerHeldFlag = "--allow-change-held-packages"
	// systemDockerInfoTimeout is how long the Docker daemon has to answer.
	systemDockerInfoTimeout = 5 * time.Second
	// systemDockerMinFreeBytes is the room the packages of the engine are downloaded and
	// unpacked in.
	// ponytail: flat floor, size-based check if it ever refuses a box that had room
	systemDockerMinFreeBytes = 1 << 30
)

// systemDpkgJournal is where dpkg keeps the updates it has started and not finished.
const systemDpkgJournal = "/var/lib/dpkg/updates"

// systemDockerDiskPaths are the places that need the room: where apt keeps what it downloads,
// and where the engine is installed.
var systemDockerDiskPaths = []string{"/var/cache/apt/archives", "/usr"}

// SystemPackageDockerUpdate is what the check says of updating the engine from the page. It is
// there when the engine has something to update.
type SystemPackageDockerUpdate struct {
	// Available is true when the button may be offered: nothing refuses it, and there is
	// something to update.
	Available bool `json:"available"`
	// Refusal is "" or one of the DockerRefusal codes.
	Refusal string `json:"refusal"`
	// RefusalDetail is the names of the packages that make the plan unacceptable (valid
	// package names, or dockerpkg.InvalidName, or dockerpkg.TooManyNew), for DockerRefusalPlan;
	// otherwise empty, never null.
	RefusalDetail []string `json:"refusal_detail"`
	// Held is whether packages of the engine are on hold, and HeldPackages which, sorted; never
	// null. The update may be available all the same: it goes ahead of the hold only when the
	// owner allows it, and PlanID covers the hold, so that one that is set, removed or moved after
	// the owner confirmed is another plan. A refusal that comes before the question of the holds
	// (unsupported, origin) says none.
	Held         bool     `json:"held"`
	HeldPackages []string `json:"held_packages"`
	// From and To are the engine's version before and after, digits and dots: 28.0.4, 29.8.0.
	// They are the same when only a package other than docker-ce moves.
	From string `json:"from"`
	To   string `json:"to"`
	// MajorJump is a change of the engine's first number: 28 to 29.
	MajorJump bool `json:"major_jump"`
	// PlanID names the plan, see dockerpkg.Plan.ID: the page sends it back to say which plan
	// the owner confirmed.
	PlanID string `json:"plan_id"`
	// Packages are what the plan installs, upgrades and new packages, with the whole dpkg
	// versions, sorted by name; never null. A refusal for a reason that comes before the plan
	// has none; neither does one for DockerRefusalPlan.
	Packages []SystemPackageUpdate `json:"packages"`
}

// dockerUpdatePreflight looks at the box and says whether the engine may be updated by the
// button, and with which plan. It runs nothing that changes the box: a few dpkg and docker
// queries and, once the cheap reasons for a refusal are out of the way, one simulation of the
// upgrade, the expensive part. It is for the check, and for the start of the update, which
// calls it again under the lock: the start refuses unless Refusal is "", the plan is not
// empty (Packages is) and PlanID is the one the owner confirmed.
//
// allowHeld is the owner's word that the update may go ahead of a hold on the engine's packages.
// The check gives it: it is what the page is to confirm, and says the hold. The start gives what the
// owner sent, and a hold with no word is the refusal DockerRefusalHeld, in its place in the order
// and before the plan is simulated. With it the plan is the one apt makes when it is told it may
// change what is held (Plan.Held says which are), as the terminal command would, which drops the hold
// of what dpkg unpacks (the unit puts it back); and it must not make a plan that changes a held
// package that is not the engine's.
//
// The plan is returned only when it is within bounds, whether or not a later reason (the
// disk) refuses: it is the zero Plan otherwise, and for a plan with nothing to upgrade (new
// packages alone: apt installs nothing of its own with --only-upgrade). Plan.Pins are what to
// put on the command line, Plan.Names what the unit's guard may let apt install, and nothing
// else may be.
func (u *systemPackageUpdater) dockerUpdatePreflight(ctx context.Context, support systemPackageSupport, allowHeld bool) (SystemPackageDockerUpdate, dockerpkg.Plan) {
	update := SystemPackageDockerUpdate{RefusalDetail: []string{}, HeldPackages: []string{}, Packages: []SystemPackageUpdate{}}
	refuse := func(code string, detail ...string) (SystemPackageDockerUpdate, dockerpkg.Plan) {
		update.Refusal = code
		update.RefusalDetail = append(update.RefusalDetail, detail...)
		return update, dockerpkg.Plan{}
	}

	if !support.supported {
		return refuse(DockerRefusalUnsupported)
	}
	if u.engineOrigin(ctx) != dockerpkg.OriginDockerRepo {
		return refuse(DockerRefusalOrigin)
	}
	installed, held := u.enginePackages(ctx)
	if len(held) > 0 {
		update.Held, update.HeldPackages = true, held
		if !allowHeld {
			return refuse(DockerRefusalHeld, held...)
		}
	}
	if u.dpkgUnfinished(ctx) {
		return refuse(DockerRefusalDpkg)
	}
	_, daemon, err := u.dockerDaemon(ctx)
	if err != nil {
		return refuse(DockerRefusalDaemon)
	}
	if daemon.SwarmActive {
		return refuse(DockerRefusalSwarm)
	}

	plan, err := u.enginePlan(ctx, support, installed, update.Held)
	if err != nil {
		// fail closed: a simulation that did not succeed says nothing of what would happen
		return refuse(DockerRefusalPlan)
	}
	plan.Held = held
	if offending := plan.Offending(); len(offending) > 0 {
		return refuse(DockerRefusalPlan, offending...)
	}
	if update.Held {
		// the flag is for the engine's packages: a held package that the plan would also change
		// (it would have been an upgrade of one that is not the engine's, which is refused above, or
		// it is one that is new) is the owner's hold on something else, and is not let go of
		if foreign := u.heldForeign(ctx, plan); len(foreign) > 0 {
			return refuse(DockerRefusalPlan, foreign...)
		}
	}

	if len(plan.Pins()) == 0 {
		// nothing is upgraded, and so nothing is installed: the update is of what is on the box
		return update, dockerpkg.Plan{}
	}
	for _, pkg := range plan.Packages {
		update.Packages = append(update.Packages, SystemPackageUpdate{Name: pkg.Name, CurrentVersion: pkg.Current, CandidateVersion: pkg.Candidate, New: pkg.New})
	}
	sort.Slice(update.Packages, func(i, j int) bool { return update.Packages[i].Name < update.Packages[j].Name })
	update.PlanID = plan.ID()
	update.From, update.To = plan.Engine()
	if update.From == "" {
		// docker-ce is not in the plan: a plugin or containerd.io moves, the engine does not
		update.From = dockerpkg.EngineVersion(u.installedVersion(ctx, "docker-ce"))
		update.To = update.From
	}
	update.MajorJump = dockerpkg.MajorJump(update.From, update.To)

	if u.diskLow() {
		update.Refusal = DockerRefusalDisk
		return update, plan
	}
	update.Available = true
	return update, plan
}

// engineOrigin is where the Docker engine on this box came from, "" when there is none.
// dockerInfo reads the same, for the line it shows.
func (u *systemPackageUpdater) engineOrigin(ctx context.Context) dockerpkg.Origin {
	switch {
	case u.installedVersion(ctx, "docker-ce") != "":
		policy, _ := u.command(ctx, "apt-cache", "policy", "docker-ce")
		return dockerpkg.OriginFromPolicy(string(policy))
	case u.installedVersion(ctx, "docker.io") != "":
		return dockerpkg.OriginDistribution
	}
	if _, err := u.command(ctx, "snap", "list", "docker"); err == nil {
		return dockerpkg.OriginSnap
	}
	return ""
}

// enginePackages are the packages of dockerpkg.EngineNames that are on the box, in that order, and
// which of them are on hold, in the same order, which is the sorted one.
func (u *systemPackageUpdater) enginePackages(ctx context.Context) (installed, held []string) {
	for _, name := range dockerpkg.EngineNames {
		if u.installedVersion(ctx, name) == "" {
			continue
		}
		installed = append(installed, name)
		if u.packageHeld(ctx, name) {
			held = append(held, name)
		}
	}
	return installed, held
}

// heldForeign are the packages of the plan that are not the engine's and that dpkg has on hold,
// sorted: the new ones, since an upgrade of a package that is not the engine's is refused before this.
// The names have passed dockerpkg.Plan.Offending.
//
// It is defence in depth. Real apt does not get here for such a package: it marks a package that
// is not named on the command line as a dependency, and a held package is not changed by a dependency
// even with --allow-change-held-packages, so a held new dependency, or a held foreign dependency, makes
// the simulation itself fail before apt prints a plan. That is the refusal DockerRefusalPlan, without
// names, from enginePlan's error.
func (u *systemPackageUpdater) heldForeign(ctx context.Context, plan dockerpkg.Plan) []string {
	var names []string
	for _, pkg := range plan.Packages {
		if !dockerpkg.IsEngineName(pkg.Name) && u.packageHeld(ctx, pkg.Name) {
			names = append(names, pkg.Name)
		}
	}
	sort.Strings(names)
	return names
}

// dpkgUnfinished says whether a package operation was left half done, by the two things apt itself
// stops at when it takes its lock: dpkg --audit has something to say of the packages (run with the
// text in English like the other package tools, and any word it prints counts), or dpkg's journal
// holds an update that was started and not finished, a file whose name is only digits. The
// simulations take no lock and do not look at either.
//
// A package manager at work is not an interrupted one: it has packages half done and a journal
// that is not empty, and says so to dpkg --audit. While one holds dpkg's lock nothing is said
// here, and the start says "maintenance" for as long as it lasts.
func (u *systemPackageUpdater) dpkgUnfinished(ctx context.Context) bool {
	if u.dpkgLocked != nil && u.dpkgLocked() {
		return false
	}
	if output, _ := u.command(ctx, "dpkg", "--audit"); strings.TrimSpace(string(output)) != "" {
		return true
	}
	if u.readDir == nil {
		return false
	}
	entries, err := u.readDir(systemDpkgJournal)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if name := entry.Name(); strings.Trim(name, "0123456789") == "" && name != "" {
			return true
		}
	}
	return false
}

// dockerDaemon asks the docker command, within systemDockerInfoTimeout, what the daemon says
// of itself. It returns the command's path, to run the other docker commands with.
func (u *systemPackageUpdater) dockerDaemon(ctx context.Context) (string, dockerpkg.DaemonInfo, error) {
	dockerPath, err := u.lookPath("docker")
	if err != nil {
		return "", dockerpkg.DaemonInfo{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, systemDockerInfoTimeout)
	defer cancel()
	output, err := u.command(ctx, dockerPath, "info", "--format", dockerpkg.DaemonInfoFormat)
	if err != nil {
		return "", dockerpkg.DaemonInfo{}, err
	}
	info, err := dockerpkg.ParseDaemonInfo(string(output))
	return dockerPath, info, err
}

// enginePlan simulates upgrading the installed packages of the engine, all of them in one
// transaction, as apt would run it. Only the packages that are on the box are named: with
// --only-upgrade apt skips the others (and fails, "Unable to locate package", for one that
// its repositories do not carry, as docker-model-plugin may not be). With changeHeld apt is told
// it may change what is on hold, as the owner's terminal command tells it.
func (u *systemPackageUpdater) enginePlan(ctx context.Context, support systemPackageSupport, installed []string, changeHeld bool) (dockerpkg.Plan, error) {
	if len(installed) == 0 {
		return dockerpkg.Plan{}, nil
	}
	words := []string{"install", "--only-upgrade", "--no-install-recommends"}
	if changeHeld {
		words = append(words, systemDockerHeldFlag)
	}
	args := systemPackageSimulateArgs(append(words, installed...)...)
	output, err := u.command(ctx, support.aptPath, args...)
	if err != nil {
		return dockerpkg.Plan{}, err
	}
	return dockerpkg.ParsePlan(string(output)), nil
}

// diskLow says whether there is too little room for the packages: less than
// systemDockerMinFreeBytes on either place that needs it. A place that cannot be read is no
// reason by itself, unless neither can.
func (u *systemPackageUpdater) diskLow() bool {
	var read int
	for _, path := range systemDockerDiskPaths {
		if u.freeBytes == nil {
			continue
		}
		free, err := u.freeBytes(path)
		if err != nil {
			continue
		}
		read++
		if free < systemDockerMinFreeBytes {
			return true
		}
	}
	return read == 0
}
