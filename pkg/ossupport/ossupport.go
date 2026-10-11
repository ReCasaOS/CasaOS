// Package ossupport says whether the operating system the core runs on still receives security
// updates, from a table of dates kept in this file. It only reads and reports: nothing here
// upgrades anything, and nothing here asks the network, apt or the box's own tables (the answer
// is the same on a box with a stale distro-info-data).
//
// Three rules keep it from saying something false:
//   - the identity is strict: ID must be exactly "debian" or "ubuntu". ID_LIKE is never looked at,
//     so Mint, Pop!_OS, Raspbian (32-bit, ID=raspbian) and the rest are not known, and nothing is
//     claimed;
//   - a release that is not in the table (Debian 14, Ubuntu 26.10, any non-LTS Ubuntu, 18.04) is
//     "unknown", not guessed;
//   - eighteen months after the day the table was last checked (AsOf) what is still to come is
//     "unknown": a table nobody has updated is no longer a source for it. What has ended stays
//     ended, since an end that has passed cannot un-end.
package ossupport

import (
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

// The precisions of Support.Ends: the sources agree on the day, or only on the month.
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
	// Known is true for ID "debian" or "ubuntu", exactly, and for nothing else.
	Known bool `json:"known"`
}

// Extended is the vendor's later, separate programme of security maintenance. Only its name and
// its last day are said: what it costs, and for whom, is the vendor's to say.
type Extended struct {
	Name string `json:"name"`
	Ends string `json:"ends"`
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
	// "month" the sources do not agree on the day, and only the month is to be shown.
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
var table = []entry{
	{
		id: "debian", versionID: "11", codename: "bullseye",
		ends: "2026-08-31", precision: PrecisionDay,
		extended: &Extended{Name: "Debian ELTS (Freexian)", Ends: "2031-06-30"},
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
		// No upgrade path is offered: the installer never supported 20.04. The days of every Ubuntu
		// row are distro-info-data's; the release-cycle page gives only months, and for the ESM
		// of all four releases it says May where distro-info-data says April (the extended dates
		// are shown as information and decide no state).
		id: "ubuntu", versionID: "20.04", codename: "focal",
		ends: "2025-05-29", precision: PrecisionDay,
		extended: &Extended{Name: "Ubuntu Pro (ESM)", Ends: "2030-04-23"},
		sources:  []string{"https://ubuntu.com/about/release-cycle", "https://git.launchpad.net/ubuntu/+source/distro-info-data/plain/ubuntu.csv?h=ubuntu/devel"},
	},
	{
		// the sources say June 1st (distro-info-data) and "May 2027" (release-cycle page): only the
		// month is claimed
		id: "ubuntu", versionID: "22.04", codename: "jammy",
		ends: "2027-06-01", precision: PrecisionMonth,
		extended: &Extended{Name: "Ubuntu Pro (ESM)", Ends: "2032-04-21"},
		sources:  []string{"https://ubuntu.com/about/release-cycle", "https://git.launchpad.net/ubuntu/+source/distro-info-data/plain/ubuntu.csv?h=ubuntu/devel"},
		next:     &Next{ID: "ubuntu", VersionID: "24.04", Codename: "noble", PrettyName: "Ubuntu 24.04 LTS (noble)", State: StateReport},
	},
	{
		id: "ubuntu", versionID: "24.04", codename: "noble",
		ends: "2029-05-31", precision: PrecisionDay,
		extended: &Extended{Name: "Ubuntu Pro (ESM)", Ends: "2034-04-25"},
		sources:  []string{"https://ubuntu.com/about/release-cycle", "https://git.launchpad.net/ubuntu/+source/distro-info-data/plain/ubuntu.csv?h=ubuntu/devel"},
		next:     &Next{ID: "ubuntu", VersionID: "26.04", Codename: "resolute", PrettyName: "Ubuntu 26.04 LTS (resolute)", State: StateReport},
	},
	{
		id: "ubuntu", versionID: "26.04", codename: "resolute",
		ends: "2031-05-29", precision: PrecisionDay,
		extended: &Extended{Name: "Ubuntu Pro (ESM)", Ends: "2036-04-23"},
		sources:  []string{"https://ubuntu.com/about/release-cycle", "https://git.launchpad.net/ubuntu/+source/distro-info-data/plain/ubuntu.csv?h=ubuntu/devel"},
	},
}

// Parse is the identity of the box from its os-release and its architecture. It is strict: only an
// ID of exactly "debian" or "ubuntu" is Known, and ID_LIKE is never looked at. Every field is
// checked for length and characters before it goes anywhere (a log, a page, an alert): one that
// fails is left empty.
func Parse(osRelease map[string]string, arch string) Identity {
	id := osRelease["ID"]
	return Identity{
		ID:         token(id, 32),
		VersionID:  token(osRelease["VERSION_ID"], 32),
		Codename:   token(osRelease["VERSION_CODENAME"], 32),
		PrettyName: prettyName(osRelease["PRETTY_NAME"]),
		Arch:       token(arch, 16),
		Known:      id == "debian" || id == "ubuntu",
	}
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
	// A day before AsOf is a clock that is wrong: the table was written on AsOf, so no box that
	// runs this code is earlier than that.
	if !id.Known || today.Before(AsOf) {
		return unknown
	}
	stale := today.After(AsOf.AddDate(0, staleAfterMonths, 0))
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

// Describe is Parse and Evaluate together, as the API answers them.
func Describe(osRelease map[string]string, arch string, now time.Time) Report {
	id := Parse(osRelease, arch)
	support := Evaluate(id, now)
	return Report{Identity: id, Support: support, Next: support.Next}
}
