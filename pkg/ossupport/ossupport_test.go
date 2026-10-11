package ossupport

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// day is midnight UTC of a date written "2006-01-02".
func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(dateLayout, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// release is an os-release as the box would give it.
func release(id, versionID, codename, pretty string) map[string]string {
	r := map[string]string{"ID": id, "PRETTY_NAME": pretty}
	if versionID != "" {
		r["VERSION_ID"] = versionID
	}
	if codename != "" {
		r["VERSION_CODENAME"] = codename
	}
	return r
}

// asOf moves the day the table was checked for the length of the test.
func asOf(t *testing.T, s string) {
	t.Helper()
	previous := AsOf
	AsOf = day(t, s)
	t.Cleanup(func() { AsOf = previous })
}

func TestOnlyDebianAndUbuntuAreKnown(t *testing.T) {
	for name, r := range map[string]map[string]string{
		// Every look-alike has an ID_LIKE of debian or ubuntu, and the version of the release it is
		// built on: none of them is known, and none is told a status.
		"Linux Mint":      {"ID": "linuxmint", "ID_LIKE": "ubuntu debian", "VERSION_ID": "21.3", "VERSION_CODENAME": "virginia"},
		"Mint on jammy":   {"ID": "linuxmint", "ID_LIKE": "ubuntu debian", "VERSION_ID": "22.04", "VERSION_CODENAME": "jammy"},
		"Pop!_OS":         {"ID": "pop", "ID_LIKE": "ubuntu debian", "VERSION_ID": "22.04", "VERSION_CODENAME": "jammy"},
		"Zorin":           {"ID": "zorin", "ID_LIKE": "ubuntu debian", "VERSION_ID": "17", "VERSION_CODENAME": "jammy"},
		"elementary":      {"ID": "elementary", "ID_LIKE": "ubuntu", "VERSION_ID": "7.1"},
		"Raspbian":        {"ID": "raspbian", "ID_LIKE": "debian", "VERSION_ID": "11", "VERSION_CODENAME": "bullseye"},
		"DietPi":          {"ID": "dietpi", "ID_LIKE": "debian", "VERSION_ID": "12", "VERSION_CODENAME": "bookworm"},
		"OpenMediaVault":  {"ID": "openmediavault", "ID_LIKE": "debian", "VERSION_ID": "12", "VERSION_CODENAME": "bookworm"},
		"Proxmox-like":    {"ID": "proxmox", "ID_LIKE": "debian", "VERSION_ID": "12", "VERSION_CODENAME": "bookworm"},
		"Kali":            {"ID": "kali", "ID_LIKE": "debian", "VERSION_ID": "2024.1"},
		"Devuan":          {"ID": "devuan", "ID_LIKE": "debian", "VERSION_ID": "5", "VERSION_CODENAME": "daedalus"},
		"Ubuntu Core":     {"ID": "ubuntu-core", "ID_LIKE": "ubuntu", "VERSION_ID": "22"},
		"only an ID_LIKE": {"ID_LIKE": "debian", "VERSION_ID": "12", "VERSION_CODENAME": "bookworm"},
		"no ID at all":    {"VERSION_ID": "12"},
		"Debian, loudly":  {"ID": "Debian", "VERSION_ID": "12", "VERSION_CODENAME": "bookworm"},
		"DEBIAN":          {"ID": "DEBIAN", "VERSION_ID": "12", "VERSION_CODENAME": "bookworm"},
		"debian and more": {"ID": "debian2", "VERSION_ID": "12", "VERSION_CODENAME": "bookworm"},
		"padded":          {"ID": " debian", "VERSION_ID": "12", "VERSION_CODENAME": "bookworm"},
		"two IDs":         {"ID": "debian ubuntu", "VERSION_ID": "12", "VERSION_CODENAME": "bookworm"},
		"a newline":       {"ID": "debian\n", "VERSION_ID": "12", "VERSION_CODENAME": "bookworm"},
		"Ubuntu, loudly":  {"ID": "Ubuntu", "VERSION_ID": "22.04", "VERSION_CODENAME": "jammy"},
		"nothing":         {},
	} {
		t.Run(name, func(t *testing.T) {
			id := Parse(r, "amd64")
			if id.Known {
				t.Fatalf("Parse(%v) is known: %+v", r, id)
			}
			if got := Describe(r, "amd64", day(t, "2026-10-10")); got.Support.State != Unknown || got.Next != nil || got.Support.Ends != "" || got.Support.Extended != nil {
				t.Fatalf("Describe(%v) claims %+v, want nothing", r, got)
			}
		})
	}
	for _, c := range []struct{ id, want string }{{"debian", "debian"}, {"ubuntu", "ubuntu"}} {
		if id := Parse(map[string]string{"ID": c.id, "ID_LIKE": "something else"}, "amd64"); !id.Known || id.ID != c.want {
			t.Errorf("Parse(ID=%s) = %+v, want it known", c.id, id)
		}
	}
	// ID_LIKE is not looked at, whatever it says and whatever ID is
	if id := Parse(map[string]string{"ID": "debian", "ID_LIKE": "fedora"}, "amd64"); !id.Known {
		t.Errorf("a Debian whose ID_LIKE is fedora is not known: %+v", id)
	}
}

func TestParseKeepsWhatIsValidAndDropsWhatIsNot(t *testing.T) {
	good := Parse(release("debian", "11", "bullseye", "Debian GNU/Linux 11 (bullseye)"), "amd64")
	if want := (Identity{ID: "debian", VersionID: "11", Codename: "bullseye", PrettyName: "Debian GNU/Linux 11 (bullseye)", Arch: "amd64", Known: true}); good != want {
		t.Fatalf("Parse = %+v, want %+v", good, want)
	}
	if got := Parse(release("ubuntu", "22.04", "jammy", "Ubuntu 22.04.5 LTS"), "arm64"); got.PrettyName != "Ubuntu 22.04.5 LTS" || got.Arch != "arm64" {
		t.Errorf("Parse = %+v", got)
	}

	long := strings.Repeat("a", 33)
	for name, c := range map[string]struct {
		field string
		value string
	}{
		"a version id with a space":          {"VERSION_ID", "11 "},
		"a version id with markup":           {"VERSION_ID", "<b>11</b>"},
		"a version id with a newline":        {"VERSION_ID", "11\n12"},
		"a version id that is too long":      {"VERSION_ID", long},
		"a version id in capitals":           {"VERSION_ID", "RC1"},
		"a codename with a quote":            {"VERSION_CODENAME", `bull"seye`},
		"a codename with a slash":            {"VERSION_CODENAME", "../etc"},
		"a codename that is too long":        {"VERSION_CODENAME", long},
		"a pretty name with markup":          {"PRETTY_NAME", "Debian <script>alert(1)</script>"},
		"a pretty name with a quote":         {"PRETTY_NAME", `Debian "11"`},
		"a pretty name with an ampersand":    {"PRETTY_NAME", "Debian & co"},
		"a pretty name with a newline":       {"PRETTY_NAME", "Debian\n11"},
		"a pretty name with an escape":       {"PRETTY_NAME", "Debian \x1b[31m11"},
		"a pretty name that reads backwards": {"PRETTY_NAME", "Debian \u202e11"},
		"a pretty name that is too long":     {"PRETTY_NAME", strings.Repeat("D", 65)},
	} {
		t.Run(name, func(t *testing.T) {
			r := release("debian", "11", "bullseye", "Debian GNU/Linux 11 (bullseye)")
			r[c.field] = c.value
			id := Parse(r, "amd64")
			var got string
			switch c.field {
			case "VERSION_ID":
				got = id.VersionID
			case "VERSION_CODENAME":
				got = id.Codename
			case "PRETTY_NAME":
				got = id.PrettyName
			}
			if got != "" {
				t.Fatalf("%s %q was kept as %q", c.field, c.value, got)
			}
			if !id.Known || id.ID != "debian" || id.Arch != "amd64" {
				t.Fatalf("the rest of the identity changed: %+v", id)
			}
		})
	}
	// every character a page or a shell could take for something else is enough, alone, to drop a pretty name
	for _, c := range "<>&\"'`\\;=${}[]|*?#@%^~\t\x00 ​" {
		if got := Parse(release("debian", "11", "bullseye", "Debian "+string(c)+" 11"), "amd64"); got.PrettyName != "" {
			t.Errorf("a pretty name with %q was kept: %q", c, got.PrettyName)
		}
	}
	if got := Parse(release("debian", "11", "bullseye", ""), "amd64 \n"); got.Arch != "" {
		t.Errorf("an architecture with a newline was kept: %q", got.Arch)
	}
	if got := Parse(release("debian", "11", "bullseye", strings.Repeat("D", 64)), ""); got.PrettyName == "" || got.Arch != "" {
		t.Errorf("a pretty name of 64 characters was dropped, or an empty architecture kept: %+v", got)
	}
	if got := Parse(release(strings.Repeat("d", 33), "11", "bullseye", ""), "amd64"); got.ID != "" || got.Known {
		t.Errorf("an ID that is too long was kept: %+v", got)
	}
	if got := Parse(nil, "amd64"); got.Known || got.ID != "" {
		t.Errorf("Parse(nil) = %+v", got)
	}
}

// The table is exactly what the spec says, and nothing else: the dates are the claims.
func TestTheTableHoldsExactlyTheseEntries(t *testing.T) {
	type row struct {
		id, version, codename, ends, precision string
		extended                               *Extended
		next                                   string // "id version", "" for none
		source                                 string
	}
	want := []row{
		{"debian", "11", "bullseye", "2026-08-31", "day", &Extended{"Debian ELTS (Freexian)", "2031-06-30", "day"}, "debian 12", "https://www.debian.org/News/2026/20260831"},
		{"debian", "12", "bookworm", "2028-06-30", "day", nil, "debian 13", "https://www.debian.org/News/2026/20260712"},
		{"debian", "13", "trixie", "2030-06-30", "day", nil, "", "https://www.debian.org/releases/"},
		// Ubuntu says the month, for every end (see the table): the month shown is the earlier of
		// the months Canonical's pages give
		{"ubuntu", "20.04", "focal", "2025-05-29", "month", &Extended{"Ubuntu Pro (ESM)", "2030-04-23", "month"}, "", "https://ubuntu.com/about/release-cycle"},
		{"ubuntu", "22.04", "jammy", "2027-05-31", "month", &Extended{"Ubuntu Pro (ESM)", "2032-04-21", "month"}, "ubuntu 24.04", "https://ubuntu.com/about/release-cycle"},
		{"ubuntu", "24.04", "noble", "2029-05-31", "month", &Extended{"Ubuntu Pro (ESM)", "2034-04-25", "month"}, "ubuntu 26.04", "https://ubuntu.com/about/release-cycle"},
		{"ubuntu", "26.04", "resolute", "2031-05-29", "month", &Extended{"Ubuntu Pro (ESM)", "2036-04-23", "month"}, "", "https://ubuntu.com/about/release-cycle"},
	}
	if len(table) != len(want) {
		t.Fatalf("the table has %d entries, want %d", len(table), len(want))
	}
	for i, w := range want {
		e := table[i]
		if e.id != w.id || e.versionID != w.version || e.codename != w.codename || e.ends != w.ends || e.precision != w.precision {
			t.Errorf("entry %d is %s %s %s ends %s (%s), want %s %s %s ends %s (%s)", i, e.id, e.versionID, e.codename, e.ends, e.precision, w.id, w.version, w.codename, w.ends, w.precision)
		}
		if (e.extended == nil) != (w.extended == nil) || (w.extended != nil && *e.extended != *w.extended) {
			t.Errorf("%s %s: extended = %+v, want %+v", w.id, w.version, e.extended, w.extended)
		}
		next := ""
		if e.next != nil {
			next = e.next.ID + " " + e.next.VersionID
			if e.next.State != StateReport || e.next.PrettyName == "" || e.next.Codename == "" {
				t.Errorf("%s %s: next = %+v", w.id, w.version, e.next)
			}
		}
		if next != w.next {
			t.Errorf("%s %s: next = %q, want %q", w.id, w.version, next, w.next)
		}
		if len(e.sources) == 0 || e.sources[0] != w.source {
			t.Errorf("%s %s: sources = %v, want the first to be %s", w.id, w.version, e.sources, w.source)
		}
		for _, s := range e.sources {
			if !strings.HasPrefix(s, "https://") {
				t.Errorf("%s %s: source %q is not https", w.id, w.version, s)
			}
		}
		// the dates are dates, and what is extended starts after what it extends
		end := day(t, e.ends)
		if e.extended != nil && !day(t, e.extended.Ends).After(end) {
			t.Errorf("%s %s: extended ends %s, not after %s", w.id, w.version, e.extended.Ends, e.ends)
		}
		// a next is the next release of the table, or nothing
		if e.next != nil {
			found := false
			for _, other := range table {
				if other.id == e.next.ID && other.versionID == e.next.VersionID && other.codename == e.next.Codename {
					found = true
					if !day(t, other.ends).After(end) {
						t.Errorf("%s %s: its next ends before it", w.id, w.version)
					}
				}
			}
			if !found {
				t.Errorf("%s %s: next %+v is not in the table", w.id, w.version, e.next)
			}
		}
	}
	if got := AsOf.Format(dateLayout); got != "2026-10-09" {
		t.Errorf("AsOf = %s, want 2026-10-09", got)
	}
}

// Around every end date: 181 days before, 180, the day before, the day, the day after, with the
// table made fresh for the test (the staleness has its own).
func TestEveryEntryChangesStateOnTheDaysThatMatter(t *testing.T) {
	for _, e := range table {
		t.Run(e.id+" "+e.versionID, func(t *testing.T) {
			end := day(t, e.ends)
			asOf(t, "2020-01-01") // restored after the test; each case below sets its own
			for _, c := range []struct {
				name string
				now  time.Time
				want string
			}{
				{"181 days before", end.AddDate(0, 0, -181), Supported},
				{"180 days before", end.AddDate(0, 0, -180), EndingSoon},
				{"179 days before", end.AddDate(0, 0, -179), EndingSoon},
				{"the day before", end.AddDate(0, 0, -1), EndingSoon},
				{"the day, at its first second", end, EndingSoon},
				{"the day, at its last second", end.Add(24*time.Hour - time.Second), EndingSoon},
				{"the day after, at its first second", end.AddDate(0, 0, 1), SecurityEnded},
				{"a year after", end.AddDate(1, 0, 0), SecurityEnded},
			} {
				// a table checked a month before the day it is asked about: the staleness stays out of it
				AsOf = c.now.AddDate(0, -1, 0)
				id := Parse(release(e.id, e.versionID, e.codename, ""), "amd64")
				if got := Evaluate(id, c.now); got.State != c.want {
					t.Errorf("%s (%s): state = %s, want %s", c.name, c.now.Format(dateLayout), got.State, c.want)
				}
			}
		})
	}
}

// The day is the UTC day: a box in the Pacific that is still on the 31st is on the 1st for the table.
func TestTheDayIsTheUTCDay(t *testing.T) {
	asOf(t, "2026-08-01")
	id := Parse(release("debian", "11", "bullseye", ""), "amd64")
	utc := time.Date(2026, 8, 31, 23, 59, 59, 0, time.UTC)
	pacific := time.FixedZone("UTC-8", -8*3600)
	if got := Evaluate(id, utc.In(pacific)).State; got != EndingSoon {
		t.Errorf("the last second of 2026-08-31 UTC, written in UTC-8: %s", got)
	}
	if got := Evaluate(id, utc.Add(time.Second).In(pacific)).State; got != SecurityEnded {
		t.Errorf("the first second of 2026-09-01 UTC, written in UTC-8 (still the 31st there): %s", got)
	}
}

// The answers of the real table, today and around the owner's boxes.
func TestTheAnswersOfTheTableOn20261010(t *testing.T) {
	now := day(t, "2026-10-10")
	for _, c := range []struct {
		id, version, codename string
		want                  string
		ends, precision       string
	}{
		{"debian", "11", "bullseye", SecurityEnded, "2026-08-31", "day"},
		{"debian", "12", "bookworm", Supported, "2028-06-30", "day"},
		{"debian", "13", "trixie", Supported, "2030-06-30", "day"},
		{"ubuntu", "20.04", "focal", SecurityEnded, "2025-05-29", "month"},
		{"ubuntu", "22.04", "jammy", Supported, "2027-05-31", "month"},
		{"ubuntu", "24.04", "noble", Supported, "2029-05-31", "month"},
		{"ubuntu", "26.04", "resolute", Supported, "2031-05-29", "month"},
		// a codename left out is not a reason to doubt the version
		{"debian", "11", "", SecurityEnded, "2026-08-31", "day"},
	} {
		got := Evaluate(Parse(release(c.id, c.version, c.codename, ""), "amd64"), now)
		if got.State != c.want || got.Ends != c.ends || got.Precision != c.precision || got.Source == "" {
			t.Errorf("%s %s: %+v, want %s ending %s (%s)", c.id, c.version, got, c.want, c.ends, c.precision)
		}
	}
	// Ubuntu 22.04 turns to "ending soon" 180 days before 2027-05-31, and to "ended" on 2027-06-01
	jammy := Parse(release("ubuntu", "22.04", "jammy", ""), "amd64")
	for _, c := range []struct{ day, want string }{
		{"2026-12-01", Supported},
		{"2026-12-02", EndingSoon},
		{"2027-05-31", EndingSoon},
		{"2027-06-01", SecurityEnded},
	} {
		if got := Evaluate(jammy, day(t, c.day)).State; got != c.want {
			t.Errorf("22.04 on %s: %s, want %s", c.day, got, c.want)
		}
	}
}

// Every Ubuntu date is a month and every Debian date a day, the extended ones as the end they extend:
// the page Ubuntu's rows cite gives no day, and Debian's give one. A month-precision date is never
// shown as a day: its Extended says so too.
func TestEveryUbuntuDateIsAMonthAndEveryDebianDateADay(t *testing.T) {
	asOf(t, "2020-01-01")
	for _, e := range table {
		want := PrecisionDay
		if e.id == "ubuntu" {
			want = PrecisionMonth
		}
		if e.precision != want {
			t.Errorf("%s %s: precision %q, want %q", e.id, e.versionID, e.precision, want)
		}
		if e.extended != nil && e.extended.Precision != want {
			t.Errorf("%s %s: extended precision %q, want %q", e.id, e.versionID, e.extended.Precision, want)
		}
		// the answer carries them
		got := Evaluate(Parse(release(e.id, e.versionID, e.codename, ""), "amd64"), day(t, "2020-01-02"))
		if got.Precision != want || (got.Extended != nil && got.Extended.Precision != want) {
			t.Errorf("%s %s: answered %+v, want precision %s all through", e.id, e.versionID, got, want)
		}
	}
}

// What is not in the table is not claimed.
func TestWhatIsNotInTheTableIsUnknown(t *testing.T) {
	now := day(t, "2026-10-10")
	for name, r := range map[string]map[string]string{
		"Debian 14":             release("debian", "14", "forky", ""),
		"Debian 10":             release("debian", "10", "buster", ""),
		"Debian testing":        release("debian", "", "", "Debian GNU/Linux trixie/sid"),
		"Ubuntu 26.10":          release("ubuntu", "26.10", "stonking", ""),
		"Ubuntu 25.10":          release("ubuntu", "25.10", "questing", ""),
		"Ubuntu 18.04":          release("ubuntu", "18.04", "bionic", ""),
		"Ubuntu 24.04.1":        release("ubuntu", "24.04.1", "noble", ""),
		"Ubuntu 22.04 as noble": release("ubuntu", "22.04", "noble", ""),
		"Debian 11 as trixie":   release("debian", "11", "trixie", ""),
		"Debian 12 as 11":       release("debian", "012", "bookworm", ""),
	} {
		got := Describe(r, "amd64", now)
		if got.Support.State != Unknown || got.Next != nil || got.Support.Ends != "" || got.Support.Source != "" {
			t.Errorf("%s: %+v, want unknown and nothing else", name, got.Support)
		}
	}
	// a Debian that is known is still known, and says so
	if got := Describe(release("debian", "14", "forky", ""), "amd64", now); !got.Known {
		t.Errorf("Debian 14 is Debian: %+v", got)
	}
}

// 18 months after the day the table was checked it is stale: what a vendor can still move
// (supported, ending soon) is "unknown" rather than possibly false, and what has ended stays ended,
// since an end that has passed cannot un-end. A stale ended answer says the day it ended and its
// source, and nothing about what comes after it.
func TestTheTableGoesStaleAfterEighteenMonths(t *testing.T) {
	bullseye := Parse(release("debian", "11", "bullseye", ""), "amd64")
	bookworm := Parse(release("debian", "12", "bookworm", ""), "amd64")
	for _, c := range []struct {
		name     string
		now      time.Time
		bullseye string
		bookworm string
		stale    bool // extended and next are no longer said
	}{
		{"the day it was checked", day(t, "2026-10-09"), SecurityEnded, Supported, false},
		{"a year after", day(t, "2027-10-09"), SecurityEnded, Supported, false},
		{"18 months after, at its first second", day(t, "2028-04-09"), SecurityEnded, EndingSoon, false},
		{"18 months after, at its last second", day(t, "2028-04-09").Add(24*time.Hour - time.Second), SecurityEnded, EndingSoon, false},
		// stale: what is to come is unknown, what ended is not
		{"18 months and a day after", day(t, "2028-04-10"), SecurityEnded, Unknown, true},
		{"bookworm's last day, stale", day(t, "2028-06-30"), SecurityEnded, Unknown, true},
		// the table says it ended: a stale table says so too
		{"the day after bookworm's, stale", day(t, "2028-07-01"), SecurityEnded, SecurityEnded, true},
		{"two years after", day(t, "2028-10-09"), SecurityEnded, SecurityEnded, true},
		{"far after", day(t, "2040-01-01"), SecurityEnded, SecurityEnded, true},
		// a clock that is wrong is not believed, whatever the state
		{"the day before it was checked", day(t, "2026-10-08").Add(23 * time.Hour), Unknown, Unknown, true},
		{"the epoch", time.Unix(0, 0), Unknown, Unknown, true},
	} {
		for _, box := range []struct {
			name  string
			id    Identity
			want  string
			ended string
		}{{"bullseye", bullseye, c.bullseye, "2026-08-31"}, {"bookworm", bookworm, c.bookworm, "2028-06-30"}} {
			got := Evaluate(box.id, c.now)
			if got.State != box.want {
				t.Errorf("%s: %s is %s, want %s", c.name, box.name, got.State, box.want)
				continue
			}
			switch {
			case got.State == Unknown:
				if got.Ends != "" || got.Precision != "" || got.Next != nil || got.Extended != nil || got.Source != "" {
					t.Errorf("%s: %s is %+v, want unknown and nothing else", c.name, box.name, got)
				}
			case got.Ends != box.ended || got.Source == "":
				t.Errorf("%s: %s says %+v, want the day it ends and its source", c.name, box.name, got)
			case c.stale && (got.Extended != nil || got.Next != nil):
				t.Errorf("%s: %s says extended %+v and next %+v of a table that is stale", c.name, box.name, got.Extended, got.Next)
			case !c.stale && got.Next == nil:
				t.Errorf("%s: %s lost its next release before the table is stale", c.name, box.name)
			}
		}
	}
	// the date it goes stale on moves with AsOf: it is a rule of the table, not a date of the code
	asOf(t, "2026-12-30")
	if got := Evaluate(bookworm, day(t, "2028-06-30")); got.State != EndingSoon || got.Next == nil {
		t.Errorf("with the table checked on 2026-12-30, bookworm on 2028-06-30 is %+v, want it ending soon and not stale yet", got)
	}
	if got := Evaluate(bookworm, day(t, "2028-07-01")); got.State != SecurityEnded || got.Next != nil {
		t.Errorf("with the table checked on 2026-12-30, bookworm on 2028-07-01 is %+v, want it ended and stale", got)
	}
	if got := Evaluate(bullseye, day(t, "2028-07-01")); got.State != SecurityEnded || got.Next != nil {
		t.Errorf("with the table checked on 2026-12-30, bullseye on 2028-07-01 is %+v", got)
	}
}

func TestAnAnswerIsACopyOfTheTable(t *testing.T) {
	id := Parse(release("debian", "11", "bullseye", ""), "amd64")
	first := Evaluate(id, day(t, "2026-10-10"))
	first.Extended.Name, first.Extended.Ends, first.Extended.Precision = "changed", "1999-01-01", "month"
	first.Next.PrettyName = "changed"
	second := Evaluate(id, day(t, "2026-10-10"))
	if second.Extended.Name != "Debian ELTS (Freexian)" || second.Extended.Ends != "2031-06-30" || second.Extended.Precision != "day" || second.Next.PrettyName != "Debian 12 (bookworm)" {
		t.Fatalf("a caller changed the table: %+v %+v", second.Extended, second.Next)
	}
}

// Exact JSON, for three boxes: the owner's Debian 11, an Ubuntu that is ending in month precision,
// and a Mint, which is not known.
func TestTheReportsAsJSON(t *testing.T) {
	for _, c := range []struct {
		name    string
		release map[string]string
		arch    string
		now     string
		want    string
	}{
		{
			"the owner's Debian 11",
			map[string]string{"ID": "debian", "VERSION_ID": "11", "VERSION_CODENAME": "bullseye", "PRETTY_NAME": "Debian GNU/Linux 11 (bullseye)"},
			"amd64", "2026-10-10",
			`{"id":"debian","version_id":"11","codename":"bullseye","pretty_name":"Debian GNU/Linux 11 (bullseye)","arch":"amd64","known":true,` +
				`"support":{"state":"security_ended","ends":"2026-08-31","precision":"day","extended":{"name":"Debian ELTS (Freexian)","ends":"2031-06-30","precision":"day"},"source":"https://www.debian.org/News/2026/20260831"},` +
				`"next":{"id":"debian","version_id":"12","codename":"bookworm","pretty_name":"Debian 12 (bookworm)","state":"report"}}`,
		},
		{
			"the same Debian 11 with a table that is stale: it has still ended, and nothing about what comes after",
			map[string]string{"ID": "debian", "VERSION_ID": "11", "VERSION_CODENAME": "bullseye", "PRETTY_NAME": "Debian GNU/Linux 11 (bullseye)"},
			"amd64", "2028-05-01",
			`{"id":"debian","version_id":"11","codename":"bullseye","pretty_name":"Debian GNU/Linux 11 (bullseye)","arch":"amd64","known":true,` +
				`"support":{"state":"security_ended","ends":"2026-08-31","precision":"day","source":"https://www.debian.org/News/2026/20260831"}}`,
		},
		{
			"an Ubuntu 22.04 that ends soon, in month precision",
			map[string]string{"ID": "ubuntu", "ID_LIKE": "debian", "VERSION_ID": "22.04", "VERSION_CODENAME": "jammy", "PRETTY_NAME": "Ubuntu 22.04.5 LTS"},
			"amd64", "2027-01-15",
			`{"id":"ubuntu","version_id":"22.04","codename":"jammy","pretty_name":"Ubuntu 22.04.5 LTS","arch":"amd64","known":true,` +
				`"support":{"state":"ending_soon","ends":"2027-05-31","precision":"month","extended":{"name":"Ubuntu Pro (ESM)","ends":"2032-04-21","precision":"month"},"source":"https://ubuntu.com/about/release-cycle"},` +
				`"next":{"id":"ubuntu","version_id":"24.04","codename":"noble","pretty_name":"Ubuntu 24.04 LTS (noble)","state":"report"}}`,
		},
		{
			"a Linux Mint",
			map[string]string{"ID": "linuxmint", "ID_LIKE": "ubuntu debian", "VERSION_ID": "21.3", "VERSION_CODENAME": "virginia", "PRETTY_NAME": "Linux Mint 21.3"},
			"amd64", "2026-10-10",
			`{"id":"linuxmint","version_id":"21.3","codename":"virginia","pretty_name":"Linux Mint 21.3","arch":"amd64","known":false,"support":{"state":"unknown"}}`,
		},
		{
			"a Debian 13, supported, with no next release",
			map[string]string{"ID": "debian", "VERSION_ID": "13", "VERSION_CODENAME": "trixie", "PRETTY_NAME": "Debian GNU/Linux 13 (trixie)"},
			"arm64", "2026-10-10",
			`{"id":"debian","version_id":"13","codename":"trixie","pretty_name":"Debian GNU/Linux 13 (trixie)","arch":"arm64","known":true,` +
				`"support":{"state":"supported","ends":"2030-06-30","precision":"day","source":"https://www.debian.org/releases/"}}`,
		},
		{
			"nothing readable",
			nil, "amd64", "2026-10-10",
			`{"arch":"amd64","known":false,"support":{"state":"unknown"}}`,
		},
	} {
		got, err := json.Marshal(Describe(c.release, c.arch, day(t, c.now)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}
