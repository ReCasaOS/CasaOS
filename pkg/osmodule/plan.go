package osmodule

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The plan of an install: what the install would do and whether the box can do it, decided from the catalogue and from a Box, which is everything the world has to answer. The plan reads
// nothing and writes nothing itself; a Box that is the real machine reads (and never writes) to answer, and one that is a table of answers makes the plan a function that tests can call.

// The checks of a plan, in the order they are listed in.
const (
	CheckCatalogue   = "catalogue"
	CheckOS          = "os"
	CheckArch        = "arch"
	CheckReCasaOS    = "recasaos"
	CheckInstalled   = "already_installed"
	CheckForeignUnit = "foreign_unit"
	CheckBusy        = "busy"
	CheckDisk        = "disk"
	CheckApt         = "apt"
)

// The codes of a refusal. A plan is refused with the code of the first check that failed, which is the code of that check, but for the checks of the disk and of apt, which say more:
// there is not room or it cannot be told, apt is not there, gave no answer, would remove or downgrade a package, or finds one held.
const (
	RefusedDiskShort     = "disk_short"
	RefusedDiskUnknown   = "disk_unknown"
	RefusedAptMissing    = "apt_missing"
	RefusedAptFailed     = "apt_failed"
	RefusedAptRemoves    = "apt_removes"
	RefusedAptDowngrades = "apt_downgrades"
	RefusedHeld          = "held_package"
)

// DetailNotChecked is what a check says when it was not made, because a check before it had already failed and made the answer of the plan certain (apt is not asked, nor the disk, for a
// system that the module does not support). Such a check is not ok, and is never the reason of the refusal.
const DetailNotChecked = "not_checked"

// The paths whose file systems must have room for an install, and the factors of what the install writes on each of them: the archive is downloaded, and read back as a tar, and its
// files are written (three times its size, whatever it unpacks to), then what apt downloads and what apt leaves on the disk. These constants are the one place the numbers are.
const (
	TmpDir                      = "/var/lib/casaos/modules/tmp"
	UsrDir                      = "/usr"
	DiskArchiveFactor     int64 = 3
	DiskAptDownloadFactor int64 = 1
	DiskAptExtraFactor    int64 = 1
	// MaxArchiveBytes is the largest archive that is downloaded, and what the plan takes the archive to be when the Box does not know its size.
	MaxArchiveBytes int64 = 200 << 20
)

// An AptResult is what apt says of installing the packages of a module, as the simulation of it reads: the packages it would install and upgrade, the sizes, and the three things that
// refuse a plan (a package removed, a package downgraded, a package held). Failure is why there is no answer at all (RefusedAptFailed, or RefusedHeld when apt itself says a held package
// is in the way); Detail is a few words of apt's own for whoever reads the plan.
type AptResult struct {
	ToInstall, ToUpgrade      []string
	Removes, Downgrades, Held []string
	DownloadBytes, ExtraBytes int64
	Known                     bool
	Failure                   string
	Detail                    string
}

// A Box is the world, as far as a plan has to ask. Every method reads. An error is never a "no": the plan treats what it cannot read as a reason to refuse.
type Box interface {
	// OS is the ID and the VERSION_ID of /etc/os-release ("debian", "12"); empty when they cannot be read.
	OS() (id, version string)
	// Arch is the architecture of the machine as the archives name it ("amd64", "arm64").
	Arch() string
	// ReCasaOSVersion is the version of the ReCasaOS that is running ("0.5.23", with or without a v).
	ReCasaOSVersion() string
	// InstalledManifests are the manifests of /etc/casaos/modules that read, by name, and the names of the files that do not.
	InstalledManifests() (installed map[string]Manifest, broken []string, err error)
	// FilesLists are the lists of files of every module, by name (the lists of /var/lib/casaos/modules/*.files).
	FilesLists() (map[string][]string, error)
	// UnitFiles are the paths of the files of a unit that exist, in every directory systemd reads units from, spelled as the unpack records them.
	UnitFiles(unit string) ([]string, error)
	// PackageManagerBusy says whether a package manager (dpkg, apt, the units that update) holds the machine; OSUpgradeBusy whether the upgrade of the operating system does (its busy flag,
	// read so that a flag that cannot be read is an error).
	PackageManagerBusy() (bool, error)
	OSUpgradeBusy() (bool, error)
	// FreeBytes is the room on the file system that holds a path (the nearest directory that exists).
	FreeBytes(path string) (int64, error)
	// AptGetExists says whether apt-get is there.
	AptGetExists() bool
	// ArchiveBytes is the size of the archive at an address when it is known without downloading it.
	ArchiveBytes(url string) (int64, bool)
	// SimulateApt asks apt what installing the packages (sorted, none twice, valid names) would do, without doing it.
	SimulateApt(packages []string) AptResult
}

// A Check is one question of the plan and its answer: the code of the question, whether it is ok, and a few words that are safe to show (printable ASCII, cut).
type Check struct {
	Code    string `json:"code"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
	refusal string
}

// A Refusal is the code that a plan was refused with, and null in JSON when it was not.
type Refusal string

func (r Refusal) MarshalJSON() ([]byte, error) {
	if r == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(r))
}

// A PlanResult is the answer of a plan, as the command says it in JSON.
type PlanResult struct {
	Name     string       `json:"name"`
	Tag      string       `json:"tag"`
	Tier     string       `json:"tier"`
	Checks   []Check      `json:"checks"`
	Refused  Refusal      `json:"refused"`
	Packages PlanPackages `json:"packages"`
	Archive  PlanArchive  `json:"archive"`
	PlanID   string       `json:"plan_id"`
}

// PlanPackages are what apt would do. The sizes are apt's own, and Known says whether apt gave them.
type PlanPackages struct {
	ToInstall     []string `json:"to_install"`
	ToUpgrade     []string `json:"to_upgrade"`
	DownloadBytes int64    `json:"download_bytes"`
	ExtraBytes    int64    `json:"extra_bytes"`
	Known         bool     `json:"known"`
}

// PlanArchive is the archive that would be downloaded, and its size when it is known.
type PlanArchive struct {
	URL   string `json:"url"`
	Bytes *int64 `json:"bytes,omitempty"`
}

// A System is what the box says of itself that decides whether a module is for it.
type System struct {
	OSID, OSVersion, Arch, ReCasaOS string
}

// SystemChecks are the three checks that depend on the system only, in the order the plan lists them (os, arch, recasaos): the dashboard shows them for every module in the list, and the
// plan makes them again.
func SystemChecks(e Entry, s System) []Check {
	osOK := slices.Contains(e.Manifest.OS, s.OSID+":"+s.OSVersion)
	_, assetOK := e.Asset(s.Arch)
	have, haveOK := parseVersion(s.ReCasaOS)
	need, _ := parseVersion(e.Manifest.MinReCasaOS)
	versionOK := haveOK && have.atLeast(need)
	detail := func(ok bool, text string) string {
		if ok {
			return ""
		}
		return clip(text)
	}
	return []Check{
		{Code: CheckOS, OK: osOK, Detail: detail(osOK, "unsupported "+s.OSID+":"+s.OSVersion)},
		{Code: CheckArch, OK: assetOK, Detail: detail(assetOK, "no archive for "+s.Arch)},
		{Code: CheckReCasaOS, OK: versionOK, Detail: detail(versionOK, "have "+s.ReCasaOS+" need "+e.Manifest.MinReCasaOS)},
	}
}

// PackagesFor are the packages a module needs on an architecture: the common ones and the architecture's, sorted, none twice.
func PackagesFor(m Manifest, arch string) []string {
	out := slices.Clone(m.Packages.Common)
	switch arch {
	case "amd64":
		out = append(out, m.Packages.AMD64...)
	case "arm64":
		out = append(out, m.Packages.ARM64...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Plan makes the plan of installing the module called name from the catalogue on the box. It asks the Box only what the checks before it have not already made certain: apt is not asked
// to simulate for a system the module does not support, and the room is not looked at for a plan that is going to be refused.
func Plan(cat Catalogue, name string, box Box) PlanResult {
	r := PlanResult{Packages: PlanPackages{ToInstall: []string{}, ToUpgrade: []string{}}}
	if nameRe.MatchString(name) {
		r.Name = name
	}
	entry, found := cat.Find(name)
	if !found {
		r.Checks = []Check{{Code: CheckCatalogue, Detail: "not_in_catalogue"}}
		for _, code := range []string{CheckOS, CheckArch, CheckReCasaOS, CheckInstalled, CheckForeignUnit, CheckBusy, CheckDisk, CheckApt} {
			r.Checks = append(r.Checks, Check{Code: code, Detail: DetailNotChecked})
		}
		r.Refused = CheckCatalogue
		return r
	}
	r.Tag, r.Tier = entry.Tag, entry.Tier

	osID, osVersion := box.OS()
	arch := box.Arch()
	asset, _ := entry.Asset(arch)
	r.Archive.URL = asset.URL

	checks := append([]Check{{Code: CheckCatalogue, OK: true}}, SystemChecks(entry, System{OSID: osID, OSVersion: osVersion, Arch: arch, ReCasaOS: box.ReCasaOSVersion()})...)
	checks = append(checks, installedCheck(box, entry), foreignUnitCheck(box, entry), busyCheck(box))
	standing := !slices.ContainsFunc(checks, func(c Check) bool { return !c.OK })

	packages := PackagesFor(entry.Manifest, arch)
	aptThere := len(packages) == 0 || box.AptGetExists()
	var sim AptResult
	simulated := false
	switch {
	case len(packages) == 0:
		sim, simulated = AptResult{Known: true}, true
	case standing && aptThere:
		sim, simulated = box.SimulateApt(packages), true
	}

	disk := Check{Code: CheckDisk, Detail: DetailNotChecked}
	if standing && simulated && sim.Failure == "" {
		disk = diskCheck(box, asset, sim, &r.Archive)
	}
	checks = append(checks, disk, aptCheck(len(packages) > 0, aptThere, simulated, sim))

	r.Checks = checks
	for _, c := range checks {
		if !c.OK && c.Detail != DetailNotChecked {
			r.Refused = Refusal(cmpOr(c.refusal, c.Code))
			break
		}
	}
	if simulated && sim.Failure == "" {
		r.Packages = PlanPackages{ToInstall: sortedCopy(sim.ToInstall), ToUpgrade: sortedCopy(sim.ToUpgrade), DownloadBytes: sim.DownloadBytes, ExtraBytes: sim.ExtraBytes, Known: sim.Known}
	}
	if r.Refused == "" {
		r.PlanID = PlanID(entry.Name, entry.Tag, asset.SHA256, osID, osVersion, r.Packages.ToInstall)
	}
	return r
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	return out
}

// PlanID is the id that binds an install to the plan the owner saw: "m1-" and the first 32 hexadecimal digits of the SHA-256 of the module, its tag, the sha256 of the archive for this
// architecture, the packages to install (sorted) and the operating system, written as a JSON object with its keys in this order so that no two sets of values are the same text.
func PlanID(name, tag, archiveSHA256, osID, osVersion string, toInstall []string) string {
	text, _ := json.Marshal(struct {
		Name      string   `json:"name"`
		Tag       string   `json:"tag"`
		SHA256    string   `json:"sha256"`
		Packages  []string `json:"packages"`
		OSID      string   `json:"os_id"`
		OSVersion string   `json:"os_version"`
	}{name, tag, archiveSHA256, sortedCopy(toInstall), osID, osVersion})
	sum := sha256.Sum256(text)
	return "m1-" + hex.EncodeToString(sum[:])[:32]
}

func installedCheck(box Box, e Entry) Check {
	installed, broken, err := box.InstalledManifests()
	switch _, there := installed[e.Name]; {
	case err != nil:
		return Check{Code: CheckInstalled, Detail: "unreadable"}
	case there:
		return Check{Code: CheckInstalled, Detail: "installed"}
	case slices.Contains(broken, e.Name):
		return Check{Code: CheckInstalled, Detail: "manifest_broken"}
	}
	return Check{Code: CheckInstalled, OK: true}
}

// foreignUnitCheck refuses a unit file of the module's name that the module's own list of files does not hold: a unit of someone else, which the install would take over.
func foreignUnitCheck(box Box, e Entry) Check {
	lists, err := box.FilesLists()
	if err != nil {
		return Check{Code: CheckForeignUnit, Detail: "unreadable"}
	}
	files, err := box.UnitFiles(e.Manifest.Unit)
	if err != nil {
		return Check{Code: CheckForeignUnit, Detail: "unreadable"}
	}
	for _, f := range files {
		if slices.Contains(lists[e.Name], f) {
			continue
		}
		detail := "not_owned " + clip(f)
		for _, other := range sortedKeys(lists) {
			if other != e.Name && slices.Contains(lists[other], f) {
				detail = "owned_by " + clip(other)
				break
			}
		}
		return Check{Code: CheckForeignUnit, Detail: detail}
	}
	return Check{Code: CheckForeignUnit, OK: true}
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// busyCheck is not ok when a package manager or the upgrade of the operating system holds the machine, or when it cannot be told: a flag that cannot be read is a machine that is busy.
func busyCheck(box Box) Check {
	pm, pmErr := box.PackageManagerBusy()
	up, upErr := box.OSUpgradeBusy()
	switch {
	case pmErr != nil || upErr != nil:
		return Check{Code: CheckBusy, Detail: "unreadable"}
	case up:
		return Check{Code: CheckBusy, Detail: "os_upgrade"}
	case pm:
		return Check{Code: CheckBusy, Detail: "package_manager"}
	}
	return Check{Code: CheckBusy, OK: true}
}

// diskCheck is ok when both file systems have room for what the install writes there. Anything the plan cannot tell (the sizes of apt, the room of a path) is not ok: it is not room.
func diskCheck(box Box, asset Asset, sim AptResult, archive *PlanArchive) Check {
	size := MaxArchiveBytes
	if n, ok := box.ArchiveBytes(asset.URL); ok && n > 0 {
		size = n
		archive.Bytes = &n
	}
	unknown := Check{Code: CheckDisk, Detail: "unknown", refusal: RefusedDiskUnknown}
	if !sim.Known || sim.DownloadBytes < 0 {
		return unknown
	}
	need := satAdd(satAdd(satMul(size, DiskArchiveFactor), satMul(sim.DownloadBytes, DiskAptDownloadFactor)), satMul(max(sim.ExtraBytes, 0), DiskAptExtraFactor))
	short := ""
	for _, path := range []string{TmpDir, UsrDir} {
		free, err := box.FreeBytes(path)
		switch {
		case err != nil || free < 0:
			return unknown
		case free < need && short == "":
			short = fmt.Sprintf("need %d bytes on %s, %d free", need, path, free)
		}
	}
	if short != "" {
		return Check{Code: CheckDisk, Detail: short, refusal: RefusedDiskShort}
	}
	return Check{Code: CheckDisk, OK: true}
}

func satAdd(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func satMul(a, k int64) int64 {
	if a > math.MaxInt64/k {
		return math.MaxInt64
	}
	return a * k
}

// aptCheck says whether apt is there and what it said of the packages: a simulation that failed, that found a package held, or that would remove or downgrade anything refuses the plan.
func aptCheck(needed, there, simulated bool, sim AptResult) Check {
	switch {
	case !needed:
		return Check{Code: CheckApt, OK: true}
	case !there:
		return Check{Code: CheckApt, Detail: "apt-get is not there", refusal: RefusedAptMissing}
	case !simulated:
		return Check{Code: CheckApt, Detail: DetailNotChecked}
	case sim.Failure == RefusedHeld:
		return Check{Code: CheckApt, Detail: clip(sim.Detail), refusal: RefusedHeld}
	case sim.Failure != "":
		return Check{Code: CheckApt, Detail: clip(sim.Detail), refusal: RefusedAptFailed}
	case len(sim.Held) > 0:
		return Check{Code: CheckApt, Detail: "held " + clip(strings.Join(sim.Held, " ")), refusal: RefusedHeld}
	case len(sim.Removes) > 0:
		return Check{Code: CheckApt, Detail: "would remove " + clip(strings.Join(sim.Removes, " ")), refusal: RefusedAptRemoves}
	case len(sim.Downgrades) > 0:
		return Check{Code: CheckApt, Detail: "would downgrade " + clip(strings.Join(sim.Downgrades, " ")), refusal: RefusedAptDowngrades}
	}
	return Check{Code: CheckApt, OK: true}
}

// clip is what the box or apt said, as it may be shown: printable ASCII, and cut.
func clip(s string) string {
	const most = 100
	b := make([]byte, 0, min(len(s), most))
	for i := 0; i < len(s) && len(b) < most; i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			c = '?'
		}
		b = append(b, c)
	}
	return string(b)
}

// A version is the three numbers of a release of ReCasaOS and whether it is a pre-release ("0.5.23-rc1" comes before "0.5.23").
type version struct {
	major, minor, patch int
	pre                 bool
}

var releaseRe = regexp.MustCompile(`^v?([0-9]{1,4})\.([0-9]{1,4})\.([0-9]{1,4})(?:(-)[0-9A-Za-z.+-]{1,40}|\+[0-9A-Za-z.+-]{1,40})?$`)

func parseVersion(s string) (version, bool) {
	m := releaseRe.FindStringSubmatch(s)
	if m == nil {
		return version{}, false
	}
	n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
	return version{n(1), n(2), n(3), m[4] == "-"}, true
}

// atLeast says whether v is the release w or a later one; a pre-release of w is not.
func (v version) atLeast(w version) bool {
	for _, d := range [][2]int{{v.major, w.major}, {v.minor, w.minor}, {v.patch, w.patch}} {
		if d[0] != d[1] {
			return d[0] > d[1]
		}
	}
	return !v.pre || w.pre
}
