package osplan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// The verdicts of a plan.
const (
	VerdictOK              = "ok"
	VerdictNeedsAcceptance = "needs_acceptance"
	VerdictBlocked         = "blocked"
)

// The severities of a problem: blocked (the verdict is blocked), needs_acceptance (the owner must accept it), info.
const (
	SeverityBlocked    = "blocked"
	SeverityAcceptance = "needs_acceptance"
	SeverityInfo       = "info"
)

// The problems of a plan, by code (BINDING: the page and the CI use them).
const (
	ProblemRemovalProtected = "removal_protected" // {name, pattern}: blocked
	ProblemRemovalEssential = "removal_essential" // {name}: blocked
	ProblemRemovalManual    = "removal_manual"    // {name, version}: needs_acceptance
	ProblemDistKeptBack     = "dist_kept_back"    // {names}, and {unlisted: n} when apt counts packages that it does not name: blocked (names has the ones that only the minimal upgrade named, see leftBehind)
	ProblemDiskShort        = "disk_short"        // {path, need, free} or, when the room could not be read, {path, need, unknown: true}: blocked
	ProblemDowngrade        = "downgrade"         // {names}: blocked (apt would downgrade them, and the hop's -y refuses to)
	ProblemDockerHeld       = "docker_held"       // {names}: info
)

// ProblemCodes are the codes of the problems of a plan.
var ProblemCodes = []string{ProblemRemovalProtected, ProblemRemovalEssential, ProblemRemovalManual, ProblemDistKeptBack, ProblemDiskShort, ProblemDowngrade, ProblemDockerHeld}

// A Problem is something about the plan that the owner must know: a code, how much it weighs, and the data that makes it
// precise.
type Problem struct {
	Code     string         `json:"code"`
	Severity string         `json:"severity"`
	Data     map[string]any `json:"data"`
}

// A Count is what apt says it would do, as numbers.
type Count struct {
	Upgraded int `json:"upgraded"`
	New      int `json:"new"`
	Removed  int `json:"removed"`
	KeptBack int `json:"kept_back"`
}

// Counts are the numbers of the full upgrade (dist-upgrade) and of the first, minimal one (upgrade). They drift by a few
// between two reads of the same mirror, and are not part of the id of the plan.
type Counts struct {
	DistUpgrade Count `json:"dist_upgrade"`
	Upgrade     Count `json:"upgrade"`
}

// A Removal is a package the full upgrade removes.
type Removal struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Manual: the owner installed it (or the file that says it was installed by itself cannot be read: nothing is assumed).
	Manual    bool   `json:"manual"`
	Protected bool   `json:"protected"`
	Pattern   string `json:"pattern,omitempty"`
	// Essential: dpkg says it is Essential: yes, or its Priority is required, or apt warned that it removes an essential package
	// of this name.
	Essential bool `json:"essential"`
}

// SourcesPlan is what the plan did to the sources. Tree is the generated files, with the content of the box's own files
// that the machine needs: the plan endpoint leaves it out (WithoutTree) and the runbook has it.
type SourcesPlan struct {
	Files            []FileChange `json:"files"`
	Tree             []TreeFile   `json:"tree,omitempty"`
	HashCurrent      string       `json:"hash_current"`
	HashGenerated    string       `json:"hash_generated"`
	DroppedBackports int          `json:"dropped_backports"`
	FirmwareAdded    int          `json:"firmware_added"`
}

// DockerState is what the plan knows of Docker: whether there is one, whether its source is put aside for the hop, and
// which of its packages are on hold.
type DockerState struct {
	Present      bool     `json:"present"`
	SourcePaused bool     `json:"source_paused"`
	PackagesHeld []string `json:"packages_held"`
}

// A DiskRow is room that the hop needs on a file system, and what there is.
type DiskRow struct {
	Path string `json:"path"`
	Need uint64 `json:"need"`
	Free uint64 `json:"free"`
	OK   bool   `json:"ok"`
	// Unknown says that the free room could not be read: it is taken for none.
	Unknown bool `json:"unknown,omitempty"`
}

// SandboxInfo is what the plan cost: the size of the sandbox directory at the end, and the seconds of each phase.
type SandboxInfo struct {
	Bytes          int64              `json:"bytes"`
	SecondsByPhase map[string]float64 `json:"seconds_by_phase"`
}

// A Plan is what would happen if the box were upgraded now.
type Plan struct {
	PlanID    string    `json:"plan_id"`
	Hop       string    `json:"hop"`
	MadeAt    time.Time `json:"made_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Verdict   string    `json:"verdict"`
	Problems  []Problem `json:"problems"`
	Counts    Counts    `json:"counts"`
	Removals  []Removal `json:"removals"`
	// NewPackages are at most MaxListed names, sorted; NewTruncated says there were more. The same for the others.
	NewPackages            []string    `json:"new_packages"`
	NewTruncated           bool        `json:"new_truncated,omitempty"`
	KeptBack               []string    `json:"kept_back"`
	KeptBackTruncated      bool        `json:"kept_back_truncated,omitempty"`
	Autoremovable          []string    `json:"autoremovable"`
	AutoremovableTruncated bool        `json:"autoremovable_truncated,omitempty"`
	Sources                SourcesPlan `json:"sources"`
	Paused                 []Paused    `json:"paused"`
	// Holds are all the packages on hold, sorted.
	Holds     []string    `json:"holds"`
	Docker    DockerState `json:"docker"`
	Estimates Estimates   `json:"estimates"`
	Disk      []DiskRow   `json:"disk"`
	// DiskNote says why Disk is empty when it is: DiskNoteEstimatesUnknown, the sizes that the floors are made of were not read
	// from apt, and the plan has no floor to measure the disk by (the verdict is not blocked for it).
	DiskNote string      `json:"disk_note,omitempty"`
	Sandbox  SandboxInfo `json:"sandbox"`
	// AptConfLeftOut are the files of the box's apt configuration that the sandbox did not get, with the reason of each: at most MaxLeftOut, in the order of
	// compareLeftOut; AptConfLeftOutTruncated says there were more.
	AptConfLeftOut          []AptConfLeftOut `json:"apt_conf_left_out"`
	AptConfLeftOutTruncated bool             `json:"apt_conf_left_out_truncated,omitempty"`
}

// DiskNoteEstimatesUnknown is the DiskNote of a plan whose sizes apt did not say in a form that is known.
const DiskNoteEstimatesUnknown = "estimates_unknown"

// MaxListed is the most names of a list of the plan.
const MaxListed = 600

// WithoutTree is the plan as the plan endpoint says it: the tree of the sources is the runbook's.
func (p Plan) WithoutTree() Plan {
	p.Sources.Tree = nil
	return p
}

// Expired says whether the plan is no longer good: it lives PlanLifetime from the time it was made.
func (p *Plan) Expired(now time.Time) bool { return !now.Before(p.ExpiresAt) }

// ---- the disk ------------------------------------------------------------------------------------------------------

// linear is num/den times a size, plus a constant.
type linear struct{ num, den, add uint64 }

func (l linear) of(x uint64) uint64 { return (x*l.num+l.den-1)/l.den + l.add }

// The floors of the disk. THESE FACTORS ARE THE OWNER'S, and are to be re-tared from the numbers of the CI measurement (the
// plan-only leg prints the real download, the real extra space and the real sandbox size): a floor that refuses a machine that
// has room is a bug of the table, not of the machine.
//
//	/var/cache/apt/archives: the larger of 1.25 x download + 256 MiB and 1.1 x download + 512 MiB
//	/usr: 1.5 x the extra space (when it is more than none) + 1 GiB
//	/: 512 MiB (when / and /usr are one file system the /usr row already says more)
var diskFloors = struct {
	archivesA, archivesB linear
	usr                  linear
	root                 uint64
}{
	archivesA: linear{5, 4, 256 << 20},
	archivesB: linear{11, 10, 512 << 20},
	usr:       linear{3, 2, 1 << 30},
	root:      512 << 20,
}

// The paths the plan asks the disk about, in the order of the rows.
var diskPaths = []string{"/var/cache/apt/archives", "/usr", "/"}

// FreeRead is the room found on a path; Known is false when it could not be read.
type FreeRead struct {
	Bytes uint64
	Known bool
}

// complete says that the estimates are known and have both their sizes.
func (e Estimates) complete() bool { return e.Known && e.DownloadBytes != nil && e.ExtraBytes != nil }

// diskRows are the rows of the disk, from the estimates: none when they are unknown, since a floor of a size that is not
// known is a guess.
func diskRows(est Estimates, free map[string]FreeRead) []DiskRow {
	rows := []DiskRow{}
	if !est.complete() {
		return rows
	}
	download, extra := uint64(*est.DownloadBytes), uint64(0)
	if *est.ExtraBytes > 0 {
		extra = uint64(*est.ExtraBytes)
	}
	need := map[string]uint64{
		"/var/cache/apt/archives": max(diskFloors.archivesA.of(download), diskFloors.archivesB.of(download)),
		"/usr":                    diskFloors.usr.of(extra),
		"/":                       diskFloors.root,
	}
	for _, p := range diskPaths {
		f := free[p]
		rows = append(rows, DiskRow{Path: p, Need: need[p], Free: f.Bytes, OK: f.Known && f.Bytes >= need[p], Unknown: !f.Known})
	}
	return rows
}

// ---- the plan ------------------------------------------------------------------------------------------------------

// PackageFacts are what dpkg says of a package that is on the box.
type PackageFacts struct {
	Version   string
	Essential bool
	Priority  string
}

// Inputs are all that the plan is made of: what the pure steps made of the sources and of apt's words, and what was read of
// the box. Assemble decides everything else.
type Inputs struct {
	Hop       HopSpec
	Rewritten *Rewritten
	// AptConfLeftOut are the files of the configuration of apt that the sandbox did not get.
	AptConfLeftOut []AptConfLeftOut
	// Dist and Upgrade are what apt said of the full and of the minimal upgrade.
	Dist, Upgrade *SolverRun
	Estimates     Estimates
	// Held are the packages on hold; DockerHeld are the Docker engine's among them.
	Held, DockerHeld []string
	DockerPresent    bool
	// Facts are what dpkg says of the packages that are removed, by name.
	Facts map[string]PackageFacts
	// AutoInstalled are the packages apt installed by itself, by the name that apt prints for them (a foreign architecture has
	// it after a colon); AutoKnown is false when the file that says it could not be read, and every package is then a manual one.
	AutoInstalled map[string]bool
	AutoKnown     bool
	// Free is the room on the file systems, read before the sandbox was written: what the sandbox takes is not in it.
	Free         map[string]FreeRead
	Now          time.Time
	SandboxBytes int64
	Seconds      map[string]float64
}

func capped(list []string) (out []string, truncated bool) {
	out = slices.Clone(list)
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	if len(out) > MaxListed {
		return out[:MaxListed], true
	}
	return out, false
}

// baseName is a package's name without the architecture that follows a colon.
func baseName(name string) string {
	base, _, _ := strings.Cut(name, ":")
	return base
}

// Assemble judges what was read and makes the plan of it: the removals, with what the guard says of each, the problems and
// the verdict, the disk, and the id.
func Assemble(in Inputs) Plan {
	p := Plan{
		Hop:       string(in.Hop.ID),
		MadeAt:    in.Now.UTC().Truncate(time.Second),
		Problems:  []Problem{},
		Removals:  []Removal{},
		Paused:    in.Rewritten.Paused,
		Estimates: in.Estimates,
		Disk:      diskRows(in.Estimates, in.Free),
		Sandbox:   SandboxInfo{Bytes: in.SandboxBytes, SecondsByPhase: in.Seconds},
	}
	p.ExpiresAt = p.MadeAt.Add(PlanLifetime)
	if p.Sandbox.SecondsByPhase == nil {
		p.Sandbox.SecondsByPhase = map[string]float64{}
	}
	p.Counts = Counts{DistUpgrade: countOf(in.Dist), Upgrade: countOf(in.Upgrade)}
	p.NewPackages, p.NewTruncated = capped(in.Dist.New)
	p.KeptBack, p.KeptBackTruncated = capped(in.Dist.KeptBack)
	p.Autoremovable, p.AutoremovableTruncated = capped(in.Dist.AutoRemovable)
	p.Sources = SourcesPlan{
		Files: in.Rewritten.Files, Tree: in.Rewritten.Tree, HashCurrent: in.Rewritten.HashCurrent, HashGenerated: in.Rewritten.HashGenerated,
		DroppedBackports: in.Rewritten.DroppedBackports, FirmwareAdded: in.Rewritten.FirmwareAdded,
	}
	p.Holds = sortedCopy(in.Held)
	p.AptConfLeftOut, p.AptConfLeftOutTruncated = capLeftOut(in.AptConfLeftOut)
	sourcePaused := false
	for _, pa := range in.Rewritten.Paused {
		sourcePaused = sourcePaused || pa.Docker
	}
	p.Docker = DockerState{Present: in.DockerPresent, SourcePaused: sourcePaused, PackagesHeld: sortedCopy(in.DockerHeld)}
	if !in.Estimates.complete() {
		p.DiskNote = DiskNoteEstimatesUnknown
	}

	// the removals of either simulation: the minimal upgrade removes nothing, and if apt ever says it does, the guard sees it;
	// and the packages that apt warns are essential
	removed, warned := map[string]bool{}, map[string]bool{}
	for _, run := range []*SolverRun{in.Dist, in.Upgrade} {
		for _, n := range run.Removals() {
			removed[n] = true
		}
		for _, n := range run.Essential {
			warned[n] = true
		}
	}
	for _, name := range sortedKeys(removed) {
		facts := in.Facts[baseName(name)]
		r := Removal{Name: name, Version: facts.Version, Essential: facts.Essential || facts.Priority == "required" || warned[name]}
		r.Manual = !in.AutoKnown || !in.AutoInstalled[name]
		r.Pattern, r.Protected = Protected(name)
		p.Removals = append(p.Removals, r)
	}
	p.Problems = judge(p, in)
	if p.Problems == nil {
		p.Problems = []Problem{}
	}
	p.Verdict = verdictOf(p.Problems)
	p.PlanID = planID(p)
	return p
}

func countOf(run *SolverRun) Count {
	return Count{Upgraded: run.Summary.Upgraded, New: run.Summary.New, Removed: run.Summary.Remove, KeptBack: run.Summary.NotUpgraded}
}

func sortedCopy(list []string) []string {
	out := slices.Clone(list)
	if out == nil {
		return []string{}
	}
	slices.Sort(out)
	return out
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// judge is the problems of the plan. The guard sees the removals only: a protected package that is new or upgraded is normal.
func judge(p Plan, in Inputs) []Problem {
	var blocked, accept, info []Problem
	for _, r := range p.Removals {
		if r.Protected {
			blocked = append(blocked, Problem{Code: ProblemRemovalProtected, Severity: SeverityBlocked, Data: map[string]any{"name": r.Name, "pattern": r.Pattern}})
		}
		if r.Essential {
			blocked = append(blocked, Problem{Code: ProblemRemovalEssential, Severity: SeverityBlocked, Data: map[string]any{"name": r.Name}})
		}
	}
	behind := leftBehind(in)
	if len(in.Dist.KeptBack) > 0 || in.Dist.Summary.NotUpgraded > 0 || len(behind) > 0 {
		// after the full upgrade something is still kept back: the solver could not reach the target (a package on hold that
		// apt therefore keeps back is that). apt counts some that it does not name (a package on hold whose candidate is a
		// downgrade): they are said as a number. And some it neither names nor counts (leftBehind): they are named here.
		names, _ := capped(slices.Concat(in.Dist.KeptBack, behind))
		data := map[string]any{"names": names}
		if n := in.Dist.Summary.NotUpgraded - len(in.Dist.KeptBack); n > 0 {
			data["unlisted"] = n
		}
		blocked = append(blocked, Problem{Code: ProblemDistKeptBack, Severity: SeverityBlocked, Data: data})
	}
	if names := downgraded(in); len(names) > 0 {
		blocked = append(blocked, Problem{Code: ProblemDowngrade, Severity: SeverityBlocked, Data: map[string]any{"names": names}})
	}
	for _, row := range p.Disk {
		if !row.OK {
			data := map[string]any{"path": row.Path, "need": row.Need}
			if row.Unknown {
				// the room could not be read: it is not none, and the page does not say it is
				data["unknown"] = true
			} else {
				data["free"] = row.Free
			}
			blocked = append(blocked, Problem{Code: ProblemDiskShort, Severity: SeverityBlocked, Data: data})
		}
	}
	for _, r := range p.Removals {
		if r.Manual {
			accept = append(accept, Problem{Code: ProblemRemovalManual, Severity: SeverityAcceptance, Data: map[string]any{"name": r.Name, "version": r.Version}})
		}
	}
	if len(p.Docker.PackagesHeld) > 0 {
		info = append(info, Problem{Code: ProblemDockerHeld, Severity: SeverityInfo, Data: map[string]any{"names": p.Docker.PackagesHeld}})
	}
	return slices.Concat(blocked, accept, info)
}

// leftBehind are the packages that the full upgrade does not reach and does not say so of: apt keeps them back in the minimal
// upgrade, and the full one names them in none of its lists (not upgraded, new, removed, downgraded or kept back) and counts
// none of them. It does that for a package whose new version cannot be installed: it depends on a version of another package that
// no repository has, or on one that does not exist at all, or on a choice of such packages (measured, apt 2.2.4: the minimal
// upgrade keeps it back, "-s dist-upgrade" prints "0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.", and
// "apt list --upgradable" still shows it: testdata/real/chroot-d11-silent-*). The real hop leaves it where it is, so the target
// is not reached. On the two measured pairs of the box the set is empty. Without a name twice, in the order of the minimal
// upgrade's list (the caller sorts it with the rest).
func leftBehind(in Inputs) []string {
	listed := map[string]bool{}
	for _, list := range [][]string{in.Dist.Upgraded, in.Dist.New, in.Dist.Removed, in.Dist.Downgraded, in.Dist.KeptBack} {
		for _, n := range list {
			listed[n] = true
		}
	}
	var out []string
	for _, n := range in.Upgrade.KeptBack {
		if !listed[n] {
			listed[n] = true
			out = append(out, n)
		}
	}
	return out
}

// downgraded are the packages that either simulation downgrades, sorted, at most MaxListed.
func downgraded(in Inputs) []string {
	set := map[string]bool{}
	for _, run := range []*SolverRun{in.Dist, in.Upgrade} {
		for _, n := range run.Downgraded {
			set[n] = true
		}
	}
	names, _ := capped(sortedKeys(set))
	return names
}

func verdictOf(problems []Problem) string {
	verdict := VerdictOK
	for _, pr := range problems {
		switch pr.Severity {
		case SeverityBlocked:
			return VerdictBlocked
		case SeverityAcceptance:
			verdict = VerdictNeedsAcceptance
		}
	}
	return verdict
}

// ---- the id --------------------------------------------------------------------------------------------------------

// idPaused, idDocker and idInput are what the id of a plan is made of, as a canonical JSON: fixed field order, sorted
// lists, no maps. Not the counts, not the sizes, not the time: they drift between two reads of the same mirror.
type idPaused struct {
	File   string   `json:"file"`
	Line   int      `json:"line"`
	Hosts  []string `json:"hosts"`
	Docker bool     `json:"docker"`
}

type idDocker struct {
	Present      bool     `json:"present"`
	SourcePaused bool     `json:"source_paused"`
	Held         []string `json:"held"`
}

type idInput struct {
	Hop           string     `json:"hop"`
	HashCurrent   string     `json:"hash_current"`
	HashGenerated string     `json:"hash_generated"`
	Paused        []idPaused `json:"paused"`
	Holds         []string   `json:"holds"`
	Removals      []string   `json:"removals"`
	Verdict       string     `json:"verdict"`
	Problems      []string   `json:"problems"`
	Docker        idDocker   `json:"docker"`
}

// canonicalIDInput is the text the id is the hash of.
func canonicalIDInput(p Plan) []byte {
	in := idInput{
		Hop: p.Hop, HashCurrent: p.Sources.HashCurrent, HashGenerated: p.Sources.HashGenerated,
		Paused: []idPaused{}, Holds: sortedCopy(p.Holds), Removals: []string{}, Verdict: p.Verdict, Problems: []string{},
		Docker: idDocker{Present: p.Docker.Present, SourcePaused: p.Docker.SourcePaused, Held: sortedCopy(p.Docker.PackagesHeld)},
	}
	for _, pa := range p.Paused {
		in.Paused = append(in.Paused, idPaused{File: pa.File, Line: pa.Line, Hosts: sortedCopy(pa.Hosts), Docker: pa.Docker})
	}
	sort.SliceStable(in.Paused, func(i, j int) bool {
		if in.Paused[i].File != in.Paused[j].File {
			return in.Paused[i].File < in.Paused[j].File
		}
		return in.Paused[i].Line < in.Paused[j].Line
	})
	for _, r := range p.Removals {
		in.Removals = append(in.Removals, r.Name)
	}
	slices.Sort(in.Removals)
	for _, pr := range p.Problems {
		in.Problems = append(in.Problems, pr.Code)
	}
	slices.Sort(in.Problems)
	b, err := json.Marshal(in)
	if err != nil {
		// a struct of strings, numbers and booleans does not fail to be written
		panic("osplan: " + err.Error())
	}
	return b
}

var planIDForm = regexp.MustCompile(`^p1-[0-9a-f]{32}$`)

// ValidPlanID says whether a string is shaped as the id of a plan.
func ValidPlanID(id string) bool { return planIDForm.MatchString(id) }

func planID(p Plan) string {
	sum := sha256.Sum256(canonicalIDInput(p))
	return "p1-" + hex.EncodeToString(sum[:])[:32]
}

// PlanID is the id of a plan as Assemble gives it: a function of the hop, of the sources as they are and as they are
// rewritten, of what is paused, held and removed, of the verdict and the codes of the problems, and of Docker.
func PlanID(p Plan) string { return planID(p) }
