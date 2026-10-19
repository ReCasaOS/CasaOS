package osmodule

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// The plan of an update of an installed module: the same questions as the plan of an install, asked of a module that is there, plus whether the catalogue has a newer tag. Like the plan
// of an install it reads and writes nothing, and its id binds the job that follows to the plan that was read.

// The modes of a plan (PlanResult.Mode); an install plan says none.
const (
	ModeInstall = "install"
	ModeUpdate  = "update"
)

// The checks that only an update has. The order of an update plan is catalogue, os, arch, recasaos, installed, newer, foreign_unit, busy, dpkg_state, disk, apt.
const (
	CheckInstalledNow = "installed"
	CheckNewer        = "newer"
)

// The codes an update is refused with besides the codes of the checks that it shares with an install.
const (
	RefusedNotInstalled    = "not_installed"
	RefusedManifestInvalid = "manifest_invalid"
	RefusedFilesUnreadable = "files_unreadable"
	RefusedFilesUnverified = "files_unverified"
	RefusedVersionUnknown  = "version_unknown"
	RefusedUpToDate        = "up_to_date"
	RefusedDowngrade       = "downgrade"
	RefusedUnitChanged     = "unit_changed"
)

// The states of the list of the files of an installed module, as a Box says them (InstalledState.Files).
const (
	FilesVerified   = "verified"   // the list records what each file holds
	FilesUnverified = "unverified" // a list of the earlier format: paths, and nothing to check them against
	FilesMissing    = "missing"
	FilesUnreadable = "unreadable"
)

// An InstalledState is what a box has of one module that may be installed.
type InstalledState struct {
	// Present: a manifest that reads is there. Broken: a manifest is there and does not pass. Unreadable: the folder of the manifests could not be read.
	Present, Broken, Unreadable bool
	Manifest                    Manifest
	// Tag is the version the installer recorded, "" when it cannot be read or is not a tag.
	Tag string
	// Files is one of the Files* states. Bytes is the size of the regular files the list records (what a backup of them takes), and BytesOK says it could be told.
	Files   string
	Bytes   int64
	BytesOK bool
}

// An UpdateBox is a Box that can also say what it has of an installed module.
type UpdateBox interface {
	Box
	// Installed reads the manifest, the version and the list of the files of the module called name.
	Installed(name string) InstalledState
	// SimulateUpdate is SimulateApt for the command the update runs: it upgrades no package that is installed (apt-get install --no-upgrade), so that the plan and the job agree.
	SimulateUpdate(packages []string) AptResult
}

// An UpdateInfo is what the plan of an update says of the two versions and of the packages.
type UpdateInfo struct {
	Installed string `json:"installed"`
	Latest    string `json:"latest"`
	// Added are the packages apt says it would install (the ones of the new manifest that are not installed, and what they need). Dropped are those the old one named and the new one no longer does: said, never removed.
	Added   []string `json:"added"`
	Dropped []string `json:"dropped"`
}

// UpdatePlanID is PlanID for an update: it binds the installed tag as well, and says the mode, so that the id of an install is never the id of an update.
func UpdatePlanID(name, installed, tag, archiveSHA256, osID, osVersion string, toInstall []string) string {
	text, _ := json.Marshal(struct {
		Mode      string   `json:"mode"`
		Name      string   `json:"name"`
		Installed string   `json:"installed"`
		Tag       string   `json:"tag"`
		SHA256    string   `json:"sha256"`
		Packages  []string `json:"packages"`
		OSID      string   `json:"os_id"`
		OSVersion string   `json:"os_version"`
	}{ModeUpdate, name, installed, tag, archiveSHA256, sortedCopy(toInstall), osID, osVersion})
	sum := sha256.Sum256(text)
	return "m1-" + hex.EncodeToString(sum[:])[:32]
}

// CompareTags compares two tags (v<major>.<minor>.<patch>) as numbers: -1, 0 or 1. ok is false when either is not a tag. The numbers are compared as digits (no overflow, however long).
func CompareTags(a, b string) (c int, ok bool) {
	pa, okA := tagParts(a)
	pb, okB := tagParts(b)
	if !okA || !okB {
		return 0, false
	}
	for i := range pa {
		if c := cmp.Or(cmp.Compare(len(pa[i]), len(pb[i])), strings.Compare(pa[i], pb[i])); c != 0 {
			return c, true
		}
	}
	return 0, true
}

func tagParts(s string) (parts [3]string, ok bool) {
	if !tagRe.MatchString(s) {
		return parts, false
	}
	for i, p := range strings.Split(s[1:], ".") {
		parts[i] = strings.TrimLeft(p, "0")
	}
	return parts, true
}

// diff is the names of a that are not in b.
func diff(a, b []string) []string {
	out := []string{}
	for _, p := range a {
		if !slices.Contains(b, p) {
			out = append(out, p)
		}
	}
	return out
}

// UpdateChecks are the two checks that an update has in the place of already_installed: installed (the module is there, its manifest reads and its list of files says what each file holds)
// and newer (the catalogue's tag is later than the installed one). newer is not made for a module that is not installed.
func UpdateChecks(st InstalledState, e Entry) (installed, newer Check) {
	installed = Check{Code: CheckInstalledNow}
	switch {
	case st.Unreadable:
		installed.Detail, installed.refusal = "unreadable", RefusedManifestInvalid
	case st.Broken:
		installed.Detail, installed.refusal = "manifest_broken", RefusedManifestInvalid
	case !st.Present:
		installed.Detail, installed.refusal = "not_installed", RefusedNotInstalled
	case st.Files == FilesUnverified:
		installed.Detail, installed.refusal = "files_unverified", RefusedFilesUnverified
	case st.Files != FilesVerified:
		installed.Detail, installed.refusal = "files_"+st.Files, RefusedFilesUnreadable
	default:
		installed.OK = true
	}
	if !installed.OK {
		return installed, notChecked(CheckNewer)
	}
	newer = Check{Code: CheckNewer}
	c, ok := CompareTags(st.Tag, e.Tag)
	switch {
	case !ok:
		newer.Detail, newer.refusal = "version_unknown", RefusedVersionUnknown
	case c == 0:
		newer.Detail, newer.refusal = "up_to_date", RefusedUpToDate
	case c > 0:
		newer.Detail, newer.refusal = "downgrade", RefusedDowngrade
	default:
		newer.OK = true
	}
	return installed, newer
}

// PlanUpdate makes the plan of updating the installed module called name to the tag the catalogue names. Like Plan it asks the Box only what the checks before it have not made certain.
func PlanUpdate(cat Catalogue, name string, box UpdateBox) PlanResult {
	r := PlanResult{Mode: ModeUpdate, Packages: PlanPackages{ToInstall: []string{}, ToUpgrade: []string{}}}
	if nameRe.MatchString(name) {
		r.Name = name
	}
	entry, found := cat.Find(name)
	if !found {
		r.Checks = []Check{{Code: CheckCatalogue, Detail: "not_in_catalogue"}}
		for _, code := range []string{CheckOS, CheckArch, CheckReCasaOS, CheckInstalledNow, CheckNewer, CheckForeignUnit, CheckBusy, CheckDpkgState, CheckDisk, CheckApt} {
			r.Checks = append(r.Checks, notChecked(code))
		}
		r.Refused = CheckCatalogue
		return r
	}
	r.Tag, r.Tier = entry.Tag, entry.Tier

	osID, osVersion := box.OS()
	arch := box.Arch()
	asset, _ := entry.Asset(arch)
	r.Archive.URL = asset.URL

	st := box.Installed(name)
	installed, newer := UpdateChecks(st, entry)
	r.Update = &UpdateInfo{Installed: st.Tag, Latest: entry.Tag, Added: []string{}, Dropped: []string{}}
	// apt is asked about every package of the new manifest, and installs the ones that are missing (and upgrades none: the job says --no-upgrade); Added is what it says it would install
	packages := PackagesFor(entry.Manifest, arch)
	if st.Present {
		r.Update.Dropped = diff(PackagesFor(st.Manifest, arch), packages)
	}

	checks := append([]Check{{Code: CheckCatalogue, OK: true}}, SystemChecks(entry, System{OSID: osID, OSVersion: osVersion, Arch: arch, ReCasaOS: box.ReCasaOSVersion()})...)
	checks = append(checks, installed, newer, updateUnitCheck(box, entry, st), busyCheck(box), dpkgCheck(box))
	standing := !slices.ContainsFunc(checks, func(c Check) bool { return !c.OK && !c.skipped })

	aptThere := len(packages) == 0 || box.AptGetExists()
	var sim AptResult
	simulated := false
	switch {
	case len(packages) == 0:
		sim, simulated = AptResult{Known: true}, true
	case standing && aptThere:
		sim, simulated = box.SimulateUpdate(packages), true
	}

	disk := notChecked(CheckDisk)
	if standing && simulated && sim.Failure == "" {
		disk = diskCheck(box, asset, sim, &r.Archive, st.Bytes, st.BytesOK)
	}
	checks = append(checks, disk, aptCheck(aptThere, simulated, sim))

	r.Checks = checks
	for _, c := range checks {
		if !c.OK && !c.skipped {
			r.Refused = Refusal(cmpOr(c.refusal, c.Code))
			break
		}
	}
	if simulated && sim.Failure == "" {
		r.Packages = PlanPackages{ToInstall: sortedCopy(sim.ToInstall), ToUpgrade: sortedCopy(sim.ToUpgrade), DownloadBytes: sim.DownloadBytes, ExtraBytes: sim.ExtraBytes, Known: sim.Known}
		r.Update.Added = sortedCopy(sim.ToInstall)
	}
	if r.Refused == "" {
		r.PlanID = UpdatePlanID(entry.Name, st.Tag, entry.Tag, asset.SHA256, osID, osVersion, r.Packages.ToInstall)
	}
	return r
}

// updateUnitCheck is the foreign_unit check of an update: the unit file that is there must be the module's own (the install's check, which counts the module's own list), and the new
// manifest must name the unit the installed one does (the update stops one unit and starts it again, and does not rename it).
func updateUnitCheck(box Box, e Entry, st InstalledState) Check {
	if st.Present && st.Manifest.Unit != e.Manifest.Unit {
		return Check{Code: CheckForeignUnit, Detail: "unit_changed", refusal: RefusedUnitChanged}
	}
	return foreignUnitCheck(box, e)
}
