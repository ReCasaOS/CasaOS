package fleet

import (
	"sort"
	"time"
)

// A scheduled backup is late once its slot is over an hour old with no run since.
// A box snapshot older than staleAfter is shown as stale (set by the poller).
const (
	lateAfter  = time.Hour
	staleAfter = 15 * time.Minute
)

// BoxBackups is one box's backups as GET /v1/fleet/backups shows them.
type BoxBackups struct {
	BoxID                string                `json:"box_id"`
	Name                 string                `json:"name"`
	Online               bool                  `json:"online"`
	PolledAt             time.Time             `json:"polled_at,omitzero"`
	Stale                bool                  `json:"stale"`
	Error                string                `json:"error"`
	State                string                `json:"state"`
	Green                bool                  `json:"green"`
	Problems             int                   `json:"problems"`
	Couples              []Couple              `json:"couples"`
	VerificationSchedule *VerificationSchedule `json:"verification_schedule,omitempty"`
}

// Couple is one (app, destination) pair with an enabled schedule.
type Couple struct {
	App         string       `json:"app"`
	Destination string       `json:"destination"`
	State       string       `json:"state"`
	Running     bool         `json:"running"`
	LastRunAt   time.Time    `json:"last_run_at,omitzero"`
	LastOKAt    time.Time    `json:"last_ok_at,omitzero"`
	LastOKStamp string       `json:"last_ok_stamp"`
	LastError   string       `json:"last_error"`
	NextSlot    time.Time    `json:"next_slot,omitzero"`
	Restic      bool         `json:"restic"`
	ResticCheck *ResticCheck `json:"restic_check"`
	Verify      CoupleVerify `json:"verify"`
}

// CoupleVerify is the last restore test of a couple: none, passed, failed,
// not_verifiable, running or unknown.
type CoupleVerify struct {
	State string    `json:"state"`
	At    time.Time `json:"at,omitzero"`
	Error string    `json:"error"`
}

// VerificationSchedule is the box schedule of restore tests.
type VerificationSchedule struct {
	Enabled   bool      `json:"enabled"`
	EveryDays int       `json:"every_days"`
	At        string    `json:"at"`
	LastRun   time.Time `json:"last_run,omitzero"`
}

// ResticCheck is the last integrity check of a restic destination.
type ResticCheck struct {
	At    time.Time `json:"at,omitzero"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
}

type runRow struct {
	App, Destination, Stamp string
	StartedAt, FinishedAt   time.Time
	Restore                 bool
	Error                   string
}

type scheduleRow struct {
	App, Destination            string
	Enabled                     bool
	LastRun, LastSlot, NextSlot time.Time
}

type verifyRow struct {
	App, Destination, Stamp, Status, Error string
	FinishedAt                             time.Time
}

type resticRow struct {
	Known, Kept bool
	Check       *ResticCheck
}

type opRow struct{ App, Kind string }

// backupInput is what one poll of a box returns, already decoded.
type backupInput struct {
	Runs           []runRow
	Schedules      []scheduleRow
	Verifications  []verifyRow
	VerifySchedule *VerificationSchedule
	Operations     []opRow
	OSUpgradeOpen  bool
	Restic         map[string]resticRow // by destination
}

// Worst first: the box state is the couple state with the lowest rank.
var stateRank = map[string]int{"failed": 0, "late": 1, "never": 2, "unknown": 3, "suspended": 4, "ok": 5}

// assess turns what a box reported into the state of each scheduled couple
// and of the box. It reads no clock: now is the caller's.
func assess(in backupInput, now time.Time) (couples []Couple, state string, green bool, problems int) {
	backing := map[string]bool{}
	for _, o := range in.Operations {
		if o.Kind == "backup" {
			backing[o.App] = true
		}
	}
	for _, s := range in.Schedules {
		if !s.Enabled {
			continue
		}
		c := Couple{App: s.App, Destination: s.Destination, Running: backing[s.App], NextSlot: s.NextSlot}

		var last, lastOK *runRow
		afterSlot := false
		for i := range in.Runs {
			r := &in.Runs[i]
			if r.App != s.App || r.Destination != s.Destination || r.Restore {
				continue
			}
			if last == nil || r.StartedAt.After(last.StartedAt) {
				last = r
			}
			if r.Error == "" && (lastOK == nil || r.StartedAt.After(lastOK.StartedAt)) {
				lastOK = r
			}
			if !s.LastSlot.IsZero() && !r.StartedAt.Before(s.LastSlot) {
				afterSlot = true
			}
		}
		if last != nil {
			c.LastRunAt, c.LastError = last.StartedAt, last.Error
		}
		if lastOK != nil {
			c.LastOKAt, c.LastOKStamp = lastOK.FinishedAt, lastOK.Stamp
			if c.LastOKAt.IsZero() {
				c.LastOKAt = lastOK.StartedAt
			}
		}

		switch {
		case last != nil && last.Error != "":
			c.State = "failed"
		case last == nil:
			c.State = "never"
		case s.LastSlot.IsZero():
			c.State = "unknown"
		case now.Sub(s.LastSlot) > lateAfter && s.LastRun.Before(s.LastSlot) && !afterSlot && !c.Running:
			c.State = "late"
			if in.OSUpgradeOpen {
				c.State = "suspended"
			}
		default:
			c.State = "ok"
		}

		c.Verify = verifyOf(in, &c)
		couples = append(couples, c)
	}
	sort.Slice(couples, func(i, j int) bool {
		if couples[i].App != couples[j].App {
			return couples[i].App < couples[j].App
		}
		return couples[i].Destination < couples[j].Destination
	})

	state, green = "none", len(couples) > 0
	for _, c := range couples {
		if c.State == "failed" || c.State == "late" {
			problems++
		}
		if c.Verify.State == "failed" {
			problems++
			green = false
		}
		if c.State != "ok" {
			green = false
		}
		if state == "none" || stateRank[c.State] < stateRank[state] {
			state = c.State
		}
	}
	return couples, state, green, problems
}

// verifyOf fills the restic fields of c and returns its verification state.
func verifyOf(in backupInput, c *Couple) CoupleVerify {
	rr, asked := in.Restic[c.Destination]
	c.Restic, c.ResticCheck = rr.Kept, rr.Check
	if rr.Kept {
		switch {
		case rr.Check == nil:
			return CoupleVerify{State: "none"}
		case rr.Check.OK:
			return CoupleVerify{State: "passed", At: rr.Check.At}
		}
		return CoupleVerify{State: "failed", At: rr.Check.At, Error: rr.Check.Error}
	}
	var last *verifyRow
	for i := range in.Verifications {
		v := &in.Verifications[i]
		if v.App != c.App || v.Destination != c.Destination || v.Status == "cancelled" || v.Status == "interrupted" {
			continue
		}
		if last == nil || last.Status != "running" && (v.Status == "running" || v.FinishedAt.After(last.FinishedAt)) {
			last = v
		}
	}
	if last == nil {
		if !asked || !rr.Known {
			return CoupleVerify{State: "unknown"}
		}
		return CoupleVerify{State: "none"}
	}
	return CoupleVerify{State: last.Status, At: last.FinishedAt, Error: last.Error}
}
