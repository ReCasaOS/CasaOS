package osreadiness

import (
	"context"
	"time"
)

// The states of a Report.
const (
	// StateUnsupported: the box is not one the upgrade is for (an unknown system, a variant, an
	// architecture that is not proven). Only the checks of that are reported.
	StateUnsupported = "unsupported"
	// StateLatest: the newest release the table knows. Nothing else was checked.
	StateLatest = "latest"
	// StateBlocked: at least one refusal.
	StateBlocked = "blocked"
	// StateAttention: no refusal, at least one warning.
	StateAttention = "attention"
	// StateReady: neither.
	StateReady = "ready"
)

// Counts is how many findings of each severity.
type Counts struct {
	Refuse int `json:"refuse"`
	Warn   int `json:"warn"`
	OK     int `json:"ok"`
	Info   int `json:"info"`
}

// A Report is whether this machine could be upgraded to the next release, and why not.
type Report struct {
	CheckedAt time.Time `json:"checked_at"`
	State     string    `json:"state"`
	// Hop is the step the upgrade would take; absent for an unsupported box and for the latest release.
	Hop    *Hop      `json:"hop,omitempty"`
	Counts Counts    `json:"counts"`
	Checks []Finding `json:"checks"`
}

// Answer is what the API gives: the Report, and the same as plain text for "copy the report", so
// that the page never builds it.
type Answer struct {
	Report
	Text string `json:"text"`
}

// NewAnswer is the Answer for a Report.
func NewAnswer(r Report) Answer { return Answer{Report: r, Text: Text(r)} }

// Run makes the report of the box the Probe looks at. It never fails: what cannot be found out is
// a refusal that says so. It takes the whole of opts.Budget at most, and what is not done by then
// is such a refusal too.
func Run(ctx context.Context, p Probe, opts Options) Report {
	checks := opts.Checks
	if checks == nil {
		checks = Checks()
	}
	budget := opts.Budget
	if budget <= 0 {
		budget = DefaultBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	p = guarded{p}

	report := Report{CheckedAt: p.Now().UTC().Truncate(time.Second), Checks: []Finding{}}
	session := identifyWithin(ctx, p, opts.IdentityTimeout)
	ctx = withSession(ctx, session)

	var gates, rest []Check
	for _, c := range checks {
		if c.Gate {
			gates = append(gates, c)
		} else {
			rest = append(rest, c)
		}
	}
	findings := execute(ctx, p, gates, opts.Concurrency)
	// A gate that says no, and knows, makes the box one the upgrade is not for. A gate that could not
	// answer (a read that failed, a tool that was too slow) says nothing of the box: it is a refusal
	// like another, the report is blocked by it, and the other checks are made.
	var refused []Finding
	definitive := false
	for _, f := range findings {
		if f.Severity == Refuse {
			refused = append(refused, f)
			definitive = definitive || f.Data["unknown"] != true
		}
	}
	switch {
	case definitive:
		// not a box the upgrade is for: what is asked of it past this point is of no use, and
		// only the reasons are reported (the gates that could not answer are refusals too)
		report.State, report.Checks = StateUnsupported, refused
		sortFindings(report.Checks)
		report.Counts = count(report.Checks)
		return report
	case session.Latest:
		report.State = StateLatest
		return report
	}

	hop := session.HopOf()
	var applicable []Check
	for _, c := range rest {
		if c.applies(hop) {
			applicable = append(applicable, c)
		}
	}
	findings = append(findings, execute(ctx, p, applicable, opts.Concurrency)...)
	sortFindings(findings)
	report.Hop, report.Checks, report.Counts = session.Hop, findings, count(findings)
	switch {
	case report.Counts.Refuse > 0:
		report.State = StateBlocked
	case report.Counts.Warn > 0:
		report.State = StateAttention
	default:
		report.State = StateReady
	}
	return report
}

func count(findings []Finding) Counts {
	var c Counts
	for _, f := range findings {
		switch f.Severity {
		case Refuse:
			c.Refuse++
		case Warn:
			c.Warn++
		case Info:
			c.Info++
		default:
			c.OK++
		}
	}
	return c
}
