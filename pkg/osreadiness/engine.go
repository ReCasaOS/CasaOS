package osreadiness

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Severity is what a finding means for the upgrade.
type Severity string

const (
	// Refuse: the upgrade must not be started from this machine as it is.
	Refuse Severity = "refuse"
	// Warn: it can be done, and the owner is told what to look at.
	Warn Severity = "warn"
	// OK: nothing to say.
	OK Severity = "ok"
	// Info: something the hop will do by itself, or a check that belongs to the plan step and is
	// not done here (it needs the network or a sandbox): said, never ok.
	Info Severity = "info"
)

func (s Severity) valid() bool { return s == Refuse || s == Warn || s == OK || s == Info }

// rank orders findings in the report: what stops the owner first.
func (s Severity) rank() int {
	switch s {
	case Refuse:
		return 0
	case Warn:
		return 1
	case Info:
		return 2
	}
	return 3
}

// An Action is the one thing the report suggests doing about a finding, as a code the page
// translates.
type Action string

const (
	ActionRestartMachine        Action = "restart_machine"
	ActionFinishDpkg            Action = "finish_dpkg"
	ActionWaitForPackageManager Action = "wait_for_package_manager"
	ActionWaitForApps           Action = "wait_for_apps"
	ActionFreeDiskSpace         Action = "free_disk_space"
	ActionReleaseHolds          Action = "release_holds"
	ActionReviewSources         Action = "review_sources"
	ActionNone                  Action = "none"
)

var actions = []Action{ActionRestartMachine, ActionFinishDpkg, ActionWaitForPackageManager, ActionWaitForApps, ActionFreeDiskSpace, ActionReleaseHolds, ActionReviewSources, ActionNone}

// A Group is a kind of check; the report lists the findings of a severity group by group, in this order.
type Group string

const (
	GroupIdentity   Group = "identity"
	GroupProcess    Group = "process"
	GroupSources    Group = "sources"
	GroupPackages   Group = "packages"
	GroupFilesystem Group = "filesystem"
	GroupNetwork    Group = "network"
	GroupDocker     Group = "docker"
	GroupHop        Group = "hop"
)

var groupOrder = []Group{GroupIdentity, GroupProcess, GroupSources, GroupPackages, GroupFilesystem, GroupNetwork, GroupDocker, GroupHop}

func (g Group) rank() int {
	if i := slices.Index(groupOrder, g); i >= 0 {
		return i
	}
	return len(groupOrder)
}

// A Result is what a check found. Data is for the page: values are strings (of a restricted
// alphabet), numbers, booleans and lists of strings, and what is not is dropped; never what a tool
// printed. Title and Detail are an English fallback, which the page replaces by its own sentence
// for the code.
type Result struct {
	Severity Severity
	Title    string
	Detail   string
	Data     map[string]any
	Action   Action
}

// D is the data of a Result.
type D = map[string]any

// A Check is one row of the table. Run is given the context of the check (it ends at Timeout,
// and at the budget of the whole report) and a Probe, and returns what it found. It must not
// panic and must not wait without that context, but if it does the engine answers for it.
type Check struct {
	// Code is the stable name of the finding: the page, the proof in CI and the owner's copy of
	// the report all use it.
	Code  string
	Group Group
	// Title is what the check is about, in English ("dpkg's state"): the title of the finding
	// when Run gives none, and of a check that could not be made.
	Title string
	// Hops are the hops the check applies to; none means all of them.
	Hops []HopID
	// Timeout is how long Run has: 5 seconds when zero.
	Timeout time.Duration
	// Gate marks the checks of the identity of the box. If one of them refuses, the box is not
	// one the upgrade is for (state "unsupported"), and only the ones that refuse are reported.
	Gate bool
	Run  func(ctx context.Context, p Probe) Result
}

const (
	// DefaultTimeout is how long a check has unless it asks for more.
	DefaultTimeout = 5 * time.Second
	// DefaultBudget is how long the whole report has.
	DefaultBudget = 20 * time.Second
	// DefaultConcurrency is how many checks run at once.
	DefaultConcurrency = 4
)

func (c Check) applies(hop HopID) bool { return len(c.Hops) == 0 || slices.Contains(c.Hops, hop) }

func (c Check) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

// registry is the table, in the order its groups registered. Each group of checks is a file that
// calls register from init, so that a new group adds a file and touches none.
var registry []Check

// register adds rows to the table. A row without a code or a Run, or a second row for a code, is
// a mistake of the program, and stops it at once: every test then fails, rather than the report
// saying something twice.
func register(checks ...Check) {
	for _, c := range checks {
		if c.Code == "" || c.Run == nil || c.Title == "" {
			panic("osreadiness: a check needs a code, a title and a Run")
		}
		if slices.ContainsFunc(registry, func(other Check) bool { return other.Code == c.Code }) {
			panic("osreadiness: the check " + c.Code + " is registered twice")
		}
		registry = append(registry, c)
	}
}

// Checks is the table.
func Checks() []Check { return slices.Clone(registry) }

// Options are the knobs of Run; the zero value is what the API uses.
type Options struct {
	// Checks is the table to run; nil is Checks().
	Checks []Check
	// Budget is how long the whole report has; zero is DefaultBudget.
	Budget time.Duration
	// Concurrency is how many checks run at once; zero is DefaultConcurrency.
	Concurrency int
	// IdentityTimeout is how long reading who the box is has (it is made before the checks, and is
	// not one of them); zero is DefaultTimeout. It never has more than what the report has left.
	IdentityTimeout time.Duration
}

// The reasons a check could not say, as they are in Data["reason"] of a finding that is unknown.
const (
	ReasonTimeout          = "timeout"
	ReasonNotFinished      = "not_finished"
	ReasonToolMissing      = "tool_missing"
	ReasonTooLarge         = "too_large"
	ReasonMissing          = "missing"
	ReasonUnreadable       = "unreadable"
	ReasonNotAllowed       = "not_allowed"
	ReasonUnexpectedOutput = "unexpected_output"
	ReasonPanic            = "panic"
	ReasonInvalid          = "invalid_result"
	ReasonFailed           = "failed"
	// ReasonClock: the date of the machine is before the day the table of the releases was written, so
	// the table cannot be read against it.
	ReasonClock = "clock"
)

var reasonText = map[string]string{
	ReasonTimeout:          "it did not answer in time",
	ReasonNotFinished:      "the report ran out of time before it was made",
	ReasonToolMissing:      "a tool it needs is not installed",
	ReasonTooLarge:         "a file it reads is larger than it can read",
	ReasonMissing:          "a file it reads is not there",
	ReasonUnreadable:       "a file it reads cannot be read",
	ReasonNotAllowed:       "it asked for something the report does not do",
	ReasonUnexpectedOutput: "a tool answered with something it does not understand",
	ReasonPanic:            "it failed unexpectedly",
	ReasonInvalid:          "it gave an answer that is not valid",
	ReasonFailed:           "a tool or a file it needs failed",
	ReasonClock:            "the date of this machine is wrong",
}

// unknownWith is the Result of a check that could not say: a refusal that says it does not know,
// and why. The engine gives it the check's title.
func unknownWith(reason string) Result {
	if _, ok := reasonText[reason]; !ok {
		reason = ReasonFailed
	}
	return Result{Severity: Refuse, Data: D{"unknown": true, "reason": reason}}
}

// errPanicked is the error of a step of the report that panicked: the reason it gives is "panic".
var errPanicked = errors.New("it failed unexpectedly")

// unknown is unknownWith for an error of the Probe.
func unknown(err error) Result {
	reason := ReasonFailed
	switch {
	case errors.Is(err, errPanicked):
		reason = ReasonPanic
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		reason = ReasonTimeout
	case errors.Is(err, ErrToolMissing):
		reason = ReasonToolMissing
	case errors.Is(err, ErrTooLarge):
		reason = ReasonTooLarge
	case errors.Is(err, ErrNotAllowed):
		reason = ReasonNotAllowed
	case errors.Is(err, fs.ErrNotExist):
		reason = ReasonMissing
	case errors.Is(err, fs.ErrPermission):
		reason = ReasonUnreadable
	}
	return unknownWith(reason)
}

// isUnknown says whether a Result is that of a check that could not say.
func (r Result) isUnknown() bool { return r.Data["unknown"] == true }

// runOne runs one check with its own timeout and answers for it: a check that panics, ignores its
// context or answers something that is not valid is a refusal that says it does not know.
func runOne(ctx context.Context, p Probe, c Check) Finding {
	cctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	done := make(chan Result, 1) // room for the answer of a check that is no longer waited for
	go func() {
		defer func() {
			if recover() != nil {
				done <- unknownWith(ReasonPanic)
			}
		}()
		done <- c.Run(cctx, p)
	}()
	var result Result
	select {
	case result = <-done:
	case <-cctx.Done():
		result = unknownWith(endOf(ctx))
	}
	return finalize(c, believed(cctx, ctx, result))
}

// believed is the result of a check, unless its time was over by the time it came: such an answer
// may be half of what it would have been, and a check that was told to stop is not believed.
func believed(cctx, report context.Context, result Result) Result {
	if cctx.Err() != nil && !result.isUnknown() {
		return unknownWith(endOf(report))
	}
	return result
}

// endOf is why the check's time is over: the report's budget, or the check's own timeout.
func endOf(report context.Context) string {
	if report.Err() != nil {
		return ReasonNotFinished
	}
	return ReasonTimeout
}

// A Finding is one line of the report: what a check found.
type Finding struct {
	Code     string         `json:"code"`
	Severity Severity       `json:"severity"`
	Title    string         `json:"title"`
	Detail   string         `json:"detail,omitempty"`
	Data     map[string]any `json:"data"`
	Action   Action         `json:"action,omitempty"`

	group Group
}

const (
	maxTitle    = 160
	maxDetail   = 600
	maxDataList = 50
)

// finalize makes the Result of a check a Finding: a severity that is none of the four, and a
// check that said it did not know without a refusal, are an unknown; the texts are cleaned and
// bounded, and the data is only what is safe to show.
func finalize(c Check, r Result) Finding {
	if !r.Severity.valid() {
		r = unknownWith(ReasonInvalid)
	}
	unknownResult := r.isUnknown()
	if unknownResult {
		r.Severity = Refuse
	}
	if r.Title == "" {
		r.Title = c.Title
		if unknownResult {
			r.Title = "Could not check: " + c.Title
		}
	}
	if unknownResult && r.Detail == "" {
		reason, _ := r.Data["reason"].(string)
		text, ok := reasonText[reason]
		if !ok {
			text = reasonText[ReasonFailed]
		}
		r.Detail = "This could not be checked: " + text + "."
	}
	data := D{}
	for key, value := range r.Data {
		if v, ok := safeData(value); ok && safeKey(key) {
			data[key] = v
		}
	}
	data["unknown"] = unknownResult
	if !unknownResult {
		delete(data, "reason")
	}
	action := r.Action
	if !slices.Contains(actions, action) {
		action = ""
	}
	return Finding{
		Code: c.Code, Severity: r.Severity, Title: cleanText(r.Title, maxTitle), Detail: cleanText(r.Detail, maxDetail),
		Data: data, Action: action, group: c.Group,
	}
}

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

func safeKey(key string) bool { return keyPattern.MatchString(key) }

// dataString is the alphabet of the strings of Data: package names, versions, paths of devices
// and of source files, dates, release names; no space, no quote, no angle bracket.
var dataString = regexp.MustCompile(`^[A-Za-z0-9._+~:@=,/()%-]{1,128}$`)

// safeData is the value as it may be shown, and whether it may be.
func safeData(value any) (any, bool) {
	switch v := value.(type) {
	case string:
		return v, dataString.MatchString(v)
	case bool:
		return v, true
	case int:
		return v, true
	case int64:
		return v, true
	case uint64:
		return v, true
	case float64:
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	case []string:
		list := make([]string, 0, len(v))
		for _, s := range v {
			if dataString.MatchString(s) && len(list) < maxDataList {
				list = append(list, s)
			}
		}
		return list, true
	}
	return nil, false
}

// cleanText is s on one line, without control characters, cut to at most max bytes on a character
// boundary.
func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == utf8.RuneError:
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// execute runs the checks, concurrency at a time, and answers for each of them by the end of ctx:
// a check that has not been started by then, and one that has not finished, are refusals that say
// the report ran out of time. The findings are in the order of the checks.
func execute(ctx context.Context, p Probe, checks []Check, concurrency int) []Finding {
	if concurrency < 1 {
		concurrency = DefaultConcurrency
	}
	findings := make([]Finding, len(checks))
	slots := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
launch:
	for i, c := range checks {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			for j := i; j < len(checks); j++ {
				findings[j] = finalize(checks[j], unknownWith(ReasonNotFinished))
			}
			break launch
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			findings[i] = runOne(ctx, p, c)
		}()
	}
	wg.Wait()
	return findings
}

// sortFindings puts the findings in their place in the report: by severity, then by group, then in the
// order of the table.
func sortFindings(findings []Finding) {
	slices.SortStableFunc(findings, func(a, b Finding) int {
		if d := a.Severity.rank() - b.Severity.rank(); d != 0 {
			return d
		}
		return a.group.rank() - b.group.rank()
	})
}
