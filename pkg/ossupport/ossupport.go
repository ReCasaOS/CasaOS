// Package ossupport says whether the operating system the core runs on still receives security
// updates, from a table of dates kept in this file. It only reads and reports: nothing here
// upgrades anything, and nothing here asks the network, apt or the box's own tables (the answer
// is the same on a box with a stale distro-info-data).
//
// Four rules keep it from saying something false:
//   - the identity is strict: ID must be exactly "debian" or "ubuntu". ID_LIKE is never looked at,
//     so Mint, Pop!_OS, Raspbian (32-bit, ID=raspbian) and the rest are not known, and nothing is
//     claimed;
//   - but ID cannot tell a plain Debian or Ubuntu from the systems that keep its own os-release
//     (ID=debian, no ID_LIKE): Raspberry Pi OS (64-bit, and Bookworm and later), Armbian, DietPi,
//     Proxmox VE, Proxmox Backup Server, Proxmox Mail Gateway, TrueNAS SCALE and OpenMediaVault.
//     Those are found by marker files, read through the exists seam
//     of Describe, and are not known either: the answer says which Variant it is, claims nothing,
//     and offers no next release, because each of them upgrades by its own guide, or not in place;
//   - a release that is not in the table (Debian 14, Ubuntu 26.10, any non-LTS Ubuntu, 18.04) is
//     "unknown", not guessed;
//   - eighteen months after the day the table was last checked (AsOf) what is still to come is
//     "unknown": a table nobody has updated is no longer a source for it. What has ended stays
//     ended, since an end that has passed cannot un-end.
package ossupport

import (
	"strings"
	"time"
	"unicode"
)

// The states of Support.State.
const (
	// Supported: the vendor's free security updates go on for more than 180 days.
	Supported = "supported"
	// EndingSoon: they end within 180 days, or today.
	EndingSoon = "ending_soon"
	// SecurityEnded: they ended yesterday or before.
	SecurityEnded = "security_ended"
	// Unknown: no claim is made.
	Unknown = "unknown"
)

// The precisions of Support.Ends: the vendor says the day, or only the month. A date the vendor's
// page gives as a month ("May 2027") is PrecisionMonth, whatever day another source puts in it, and
// is shown as the month.
const (
	PrecisionDay   = "day"
	PrecisionMonth = "month"
)

// StateReport is the State of a Next: it is a report, read-only. Nothing in this package, and
// nothing that reads it yet, upgrades the operating system.
const StateReport = "report"

const (
	dateLayout = "2006-01-02"
	// staleAfterMonths is how long after AsOf the table is still believed.
	staleAfterMonths = 18
	// endingSoonDays is how long before the end the state is EndingSoon.
	endingSoonDays = 180
)

// AsOf is the day the table below was last checked against its sources. A var so that a test can
// move it; nothing else does.
var AsOf = time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)

// Identity is the operating system as os-release says it, validated: a field that is not what it
// should look like is left empty, and absent from the JSON.
type Identity struct {
	ID         string `json:"id,omitempty"`
	VersionID  string `json:"version_id,omitempty"`
	Codename   string `json:"codename,omitempty"`
	PrettyName string `json:"pretty_name,omitempty"`
	Arch       string `json:"arch,omitempty"`
	// Known is true for ID "debian" or "ubuntu", exactly, when it is not a Variant, and for nothing
	// else.
	Known bool `json:"known"`
	// Variant is what a box that says ID "debian" or "ubuntu" really is, when it is not the plain
	// distribution: one of the Variant constants. Such a box is not Known.
	Variant string `json:"variant,omitempty"`
}

// The variants, the values of Identity.Variant.
const (
	VariantRaspberryPi    = "raspberrypi"
	VariantArmbian        = "armbian"
	VariantDietPi         = "dietpi"
	VariantProxmox        = "proxmox"
	VariantOpenMediaVault = "openmediavault"
	// TrueNAS SCALE (before 25.04 its os-release says ID=debian), Proxmox Backup Server and Proxmox
	// Mail Gateway (both keep Debian's own os-release, as Proxmox VE does).
	VariantTrueNAS       = "truenas"
	VariantProxmoxBackup = "proxmox_backup"
	VariantProxmoxMail   = "proxmox_mail"
)

// variantMarkers are the files that say a box is a variant, the first one found deciding: Raspberry
// Pi OS writes /etc/rpi-issue (its os-release is Debian's own, 64-bit and from Bookworm); Armbian
// /etc/armbian-release; DietPi /boot/dietpi.txt and /etc/dietpi; Proxmox VE has /usr/bin/pveversion
// and mounts /etc/pve; OpenMediaVault has /etc/openmediavault and /usr/sbin/omv-confdbadm; Proxmox
// Backup Server has /usr/sbin/proxmox-backup-manager; Proxmox Mail Gateway has /usr/bin/pmgversion;
// TrueNAS SCALE runs /usr/bin/middlewared from the python package
// /usr/lib/python3/dist-packages/middlewared.
//
// What was checked in the vendors' own sources, on 2026-10-11, for the three that were added last:
// /usr/sbin/proxmox-backup-manager is a line of debian/proxmox-backup-server.install of the package
// (git.proxmox.com, proxmox-backup); /usr/bin/pmgversion is one of the command-line tools that the
// Makefile of pmg-api installs in /usr/bin (git.proxmox.com, pmg-api); /usr/bin/middlewared is the
// ExecStart of middlewared.service (github.com/truenas/middleware, src/middlewared/debian). NOT checked
// in a source: the python package path of TrueNAS (it is where Debian puts a python3 package, and is
// kept as a second marker), and the others of the table, which came from forum posts and docs:
// Raspberry Pi forum t=329094, Proxmox forum "we re-use the /etc/os-release file from Debian", the
// DietPi forum, docs.armbian.com/build-framework/switches/branding.
//
// The configuration directories of Proxmox Backup Server and Mail Gateway (/etc/proxmox-backup, /etc/pmg)
// are NOT markers: they stay when the package is removed without being purged, and a container that is
// given the path as a volume makes them, so a plain Debian could be a variant for good. A marker has to
// be a file that no plain Debian or Ubuntu has, since this is also what the badge of the released system
// goes by: none of /data/manifest.json (TrueNAS's, where an owner may keep an app's own) and /etc/version
// (not found in TrueNAS SCALE's sources) is one either.
var variantMarkers = []struct {
	variant string
	paths   []string
}{
	{VariantRaspberryPi, []string{"/etc/rpi-issue"}},
	{VariantArmbian, []string{"/etc/armbian-release"}},
	{VariantDietPi, []string{"/boot/dietpi.txt", "/etc/dietpi"}},
	{VariantProxmox, []string{"/usr/bin/pveversion", "/etc/pve"}},
	{VariantOpenMediaVault, []string{"/etc/openmediavault", "/usr/sbin/omv-confdbadm"}},
	{VariantProxmoxBackup, []string{"/usr/sbin/proxmox-backup-manager"}},
	{VariantProxmoxMail, []string{"/usr/bin/pmgversion"}},
	{VariantTrueNAS, []string{"/usr/bin/middlewared", "/usr/lib/python3/dist-packages/middlewared"}},
}

// Extended is the vendor's later, separate programme of security maintenance. Only its name and
// its last day are said: what it costs, and for whom, is the vendor's to say. Precision is that of
// Ends, as for a Support.
type Extended struct {
	Name      string `json:"name"`
	Ends      string `json:"ends"`
	Precision string `json:"precision"`
}

// Next is the release after this one. State is StateReport: it is information only.
type Next struct {
	ID         string `json:"id"`
	VersionID  string `json:"version_id"`
	Codename   string `json:"codename"`
	PrettyName string `json:"pretty_name"`
	State      string `json:"state"`
}

// Support is how long the vendor's free security updates last for an Identity.
type Support struct {
	State string `json:"state"`
	// Ends is the last day of the free security updates, "2006-01-02", with Precision "day"; with
	// "month" the vendor says only the month, and only the month is to be shown.
	Ends      string    `json:"ends,omitempty"`
	Precision string    `json:"precision,omitempty"`
	Extended  *Extended `json:"extended,omitempty"`
	// Source is the page that says it.
	Source string `json:"source,omitempty"`
	// Next is not part of the JSON of a Support: a Report carries it beside it.
	Next *Next `json:"-"`
}

// Report is what GET /v1/sys/os answers: who the box is, how long it is supported, and the next
// release when the table has one.
type Report struct {
	Identity
	Support Support `json:"support"`
	Next    *Next   `json:"next,omitempty"`
}

// entry is one row of the table. Dates are the day the free security updates end, the vendor's
// extended programme being a later, separate one. sources are the pages the dates come from;
// the first is the one the API gives.
type entry struct {
	id, versionID, codename string
	ends, precision         string
	extended                *Extended
	sources                 []string
	next                    *Next
}

// table is the whole of what is claimed, checked against its sources on AsOf. A box that is not
// in it is "unknown".
//
// The Debian rows say the day, as the Debian pages do. The Ubuntu rows say only the month, because
// the page they cite (ubuntu.com/about/release-cycle) gives only a month for every end, the
// standard one and the extended one ("May 2027", "May 2032"), and the other pages disagree on the
// day and sometimes on the month. Where Canonical's pages differ (checked on 2026-10-11) the earlier
// month is the one kept, which is the side a warning is safe on: the release team's list says
// "Jun 2027" and "Jun 2029" for 22.04 and 24.04 where the release-cycle page says May, and
// distro-info-data and that list say April for the ESM of 20.04 where the release-cycle page and
// the 20.04 post say May 2030 (distro-info-data: April for all four). The day of an Ubuntu row is
// the one that carries that month: distro-info-data's, except for 22.04, whose 2027-06-01 is the day
// after its last (the release-cycle page gives 2027-05-31).
var table = []entry{
	{
		id: "debian", versionID: "11", codename: "bullseye",
		ends: "2026-08-31", precision: PrecisionDay,
		extended: &Extended{Name: "Debian ELTS (Freexian)", Ends: "2031-06-30", Precision: PrecisionDay},
		sources:  []string{"https://www.debian.org/News/2026/20260831", "https://wiki.debian.org/LTS/Extended"},
		next:     &Next{ID: "debian", VersionID: "12", Codename: "bookworm", PrettyName: "Debian 12 (bookworm)", State: StateReport},
	},
	{
		// free security by Debian LTS, after the security team's own end (2026-07-11 on the releases
		// page, 12 July in the news): LTS covers i386, amd64, armhf, arm64 and ppc64el only
		id: "debian", versionID: "12", codename: "bookworm",
		ends: "2028-06-30", precision: PrecisionDay,
		sources: []string{"https://www.debian.org/News/2026/20260712", "https://wiki.debian.org/LTS"},
		next:    &Next{ID: "debian", VersionID: "13", Codename: "trixie", PrettyName: "Debian 13 (trixie)", State: StateReport},
	},
	{
		// free security by Debian LTS, after the security team's own end on 2028-08-09
		id: "debian", versionID: "13", codename: "trixie",
		ends: "2030-06-30", precision: PrecisionDay,
		sources: []string{"https://www.debian.org/releases/"},
	},
	{
		// No upgrade path is offered: the installer never supported 20.04. Its standard support ended
		// "May 2025": the release team's post says 29 May, Canonical's blog 31 May.
		id: "ubuntu", versionID: "20.04", codename: "focal",
		ends: "2025-05-29", precision: PrecisionMonth,
		extended: &Extended{Name: "Ubuntu Pro (ESM)", Ends: "2030-04-23", Precision: PrecisionMonth},
		sources: []string{"https://ubuntu.com/about/release-cycle", "https://ubuntu.com/blog/20.04-end-of-standard-support",
			"https://git.launchpad.net/ubuntu/+source/distro-info-data/plain/ubuntu.csv?h=ubuntu/devel"},
	},
	{
		id: "ubuntu", versionID: "22.04", codename: "jammy",
		ends: "2027-05-31", precision: PrecisionMonth,
		extended: &Extended{Name: "Ubuntu Pro (ESM)", Ends: "2032-04-21", Precision: PrecisionMonth},
		sources: []string{"https://ubuntu.com/about/release-cycle", "https://ubuntu.com/project/docs/release-team/list-of-releases/",
			"https://git.launchpad.net/ubuntu/+source/distro-info-data/plain/ubuntu.csv?h=ubuntu/devel"},
		next: &Next{ID: "ubuntu", VersionID: "24.04", Codename: "noble", PrettyName: "Ubuntu 24.04 LTS (noble)", State: StateReport},
	},
	{
		id: "ubuntu", versionID: "24.04", codename: "noble",
		ends: "2029-05-31", precision: PrecisionMonth,
		extended: &Extended{Name: "Ubuntu Pro (ESM)", Ends: "2034-04-25", Precision: PrecisionMonth},
		sources: []string{"https://ubuntu.com/about/release-cycle", "https://ubuntu.com/project/docs/release-team/list-of-releases/",
			"https://git.launchpad.net/ubuntu/+source/distro-info-data/plain/ubuntu.csv?h=ubuntu/devel"},
		next: &Next{ID: "ubuntu", VersionID: "26.04", Codename: "resolute", PrettyName: "Ubuntu 26.04 LTS (resolute)", State: StateReport},
	},
	{
		id: "ubuntu", versionID: "26.04", codename: "resolute",
		ends: "2031-05-29", precision: PrecisionMonth,
		extended: &Extended{Name: "Ubuntu Pro (ESM)", Ends: "2036-04-23", Precision: PrecisionMonth},
		sources:  []string{"https://ubuntu.com/about/release-cycle", "https://git.launchpad.net/ubuntu/+source/distro-info-data/plain/ubuntu.csv?h=ubuntu/devel"},
	},
}

// Parse is the identity of the box from its os-release and its architecture. It is strict: only an
// ID of exactly "debian" or "ubuntu" is Known, and ID_LIKE is never looked at. Every field is
// checked for length and characters before it goes anywhere (a log, a page, an alert): one that
// fails is left empty. The only variant Parse can tell is Armbian, whose os-release names it (its
// PRETTY_NAME is "<vendor> <version> <release>", "Armbian-unofficial" when the vendor is not set);
// the others are told by their files, in Describe.
func Parse(osRelease map[string]string, arch string) Identity {
	id := osRelease["ID"]
	identity := Identity{
		ID:         token(id, 32),
		VersionID:  token(osRelease["VERSION_ID"], 32),
		Codename:   token(osRelease["VERSION_CODENAME"], 32),
		PrettyName: prettyName(osRelease["PRETTY_NAME"]),
		Arch:       token(arch, 16),
		Known:      id == "debian" || id == "ubuntu",
	}
	if identity.Known && strings.HasPrefix(osRelease["PRETTY_NAME"], "Armbian") {
		identity.Known, identity.Variant = false, VariantArmbian
	}
	return identity
}

// variantOf is the variant whose marker file exists, or "". exists says whether a path exists on the
// box; nil means no file does.
func variantOf(exists func(path string) bool) string {
	if exists == nil {
		return ""
	}
	for _, m := range variantMarkers {
		for _, path := range m.paths {
			if exists(path) {
				return m.variant
			}
		}
	}
	return ""
}

// token is s when it is 1 to limit characters of a-z, 0-9, ".", "_" and "-", and "" otherwise.
func token(s string, limit int) string {
	if s == "" || len(s) > limit {
		return ""
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return ""
		}
	}
	return s
}

// prettyName is s when it is 1 to 64 letters, digits, spaces and . , : ( ) / + _ - !, and ""
// otherwise: nothing a page could take for markup, and no control or direction character.
func prettyName(s string) string {
	n := 0
	for _, c := range s {
		n++
		if !(unicode.IsLetter(c) || unicode.IsDigit(c) || c == ' ' || c == '.' || c == ',' || c == ':' ||
			c == '(' || c == ')' || c == '/' || c == '+' || c == '_' || c == '-' || c == '!') {
			return ""
		}
	}
	if n == 0 || n > 64 {
		return ""
	}
	return s
}

// dayOf is the calendar day of t in UTC, at midnight.
func dayOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// BeforeTable says whether now is a day before AsOf: a clock that is wrong, since the table was
// written on AsOf and no box that runs this code is earlier than that. Evaluate says nothing of such
// a day, and whoever asks for what the table says should say that the date is wrong, and not that
// the table has nothing.
func BeforeTable(now time.Time) bool { return dayOf(now).Before(AsOf) }

// Stale says whether the table is too old to be believed on the day of now: more than eighteen months after
// AsOf. Whoever asks what the table says of a release should say that the table is old, and not that the release
// has nothing. A day before AsOf is not stale, it is a clock that is wrong (BeforeTable).
func Stale(now time.Time) bool { return dayOf(now).After(AsOf.AddDate(0, staleAfterMonths, 0)) }

// Evaluate is how long the free security updates of id last, on the day of now (UTC). The last day
// is a day of support: SecurityEnded begins the day after it, and EndingSoon the 180th day before
// it. A box that is not Known, a release the table does not have, a codename that is not the
// release's (a derivative, a testing system) and any day before AsOf are Unknown. More than 18
// months after AsOf the table is stale: Supported and EndingSoon, which a vendor can still move,
// are Unknown then, and SecurityEnded stays SecurityEnded, with only the day it ended and its
// source: what the vendor offers after it, and the next release, are claims about a time the table
// has not been looked at for.
func Evaluate(id Identity, now time.Time) Support {
	unknown := Support{State: Unknown}
	today := dayOf(now)
	// A day before AsOf is a clock that is wrong (BeforeTable)
	if !id.Known || BeforeTable(now) {
		return unknown
	}
	stale := Stale(now)
	for _, e := range table {
		if e.id != id.ID || e.versionID != id.VersionID || (id.Codename != "" && id.Codename != e.codename) {
			continue
		}
		end, err := time.Parse(dateLayout, e.ends)
		if err != nil {
			return unknown
		}
		support := Support{State: Supported, Ends: e.ends, Precision: e.precision, Source: e.sources[0]}
		switch {
		case today.After(end):
			support.State = SecurityEnded
		case end.Sub(today) <= endingSoonDays*24*time.Hour:
			support.State = EndingSoon
		}
		if stale {
			if support.State != SecurityEnded {
				return unknown
			}
			return support
		}
		if e.extended != nil {
			extended := *e.extended
			support.Extended = &extended
		}
		if e.next != nil {
			next := *e.next
			support.Next = &next
		}
		return support
	}
	return unknown
}

// Describe is Parse, the look for a variant and Evaluate together, as the API answers them. exists
// says whether a path exists on the box (nil: none does); it is asked only when the box says
// "debian" or "ubuntu", and the variant it finds is not Known and has no support to claim.
func Describe(osRelease map[string]string, arch string, now time.Time, exists func(path string) bool) Report {
	id := Parse(osRelease, arch)
	if id.Known {
		if variant := variantOf(exists); variant != "" {
			id.Known, id.Variant = false, variant
		}
	}
	support := Evaluate(id, now)
	return Report{Identity: id, Support: support, Next: support.Next}
}
