package modulecmd

import (
	"regexp"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
)

// The state of a job that the core started, as the page is told it. The log of the module (see job.go for its grammar) and the state of the job's unit are all that is
// read; nothing is kept in memory, so that a restart of the core in the middle of a job changes nothing. A job is one run of install or remove, and the log holds one run.
//
// The core writes the first line of the log itself, before it starts the unit (QueuedLine): it carries the nonce of the job, so that the log is a job's from the moment it is
// started, and what the job is (KindInstall, KindRemove or KindUpdate: the markers of a run are the same for all). The unit replaces the log with its own lines, which carry the same nonce, and keeps
// that first line (see the log of the assistant, job.go). A line counts as the job's word only if it is a whole line of exactly the shape of
// a marker, of the job's module, with the job's nonce, and does not begin with the "| " of the output of a tool; the nonce of the job is the one the core wrote, and when it is
// not given (a core that was restarted) it is the first marker of the log that has one, a marker with the nonce "-" (a run by hand) never counting.
//
// idle         no job: no marker of a job in the log (a log that is no job's, or none), and no unit
// running      no RESULT line, and the unit runs, or it was seen a moment ago (see InterruptedAfter), or systemd does not answer and the log is not old (SilentAfter)
// succeeded    a RESULT ok line
// refused      a RESULT refused line: nothing was changed, and Code says why
// failed       a RESULT failed line: Step and Code say where and why, Undo whether what was done was undone
// interrupted  no RESULT line, and the unit is gone: it was ended in the middle (the log says where), and nothing was undone; the core never starts it again
//
// A log with nothing in it is not the absence of a log: the unit empties the log when it starts and says its first marker when its plan is made, which takes up to a minute. It reads as
// running while it was written lately (or the unit runs), and as interrupted when it was not and the unit is gone (a unit killed between the two).
//
// The kind of the job is the one that the first line of the log says, when that line is the queued line of the job; a log that does not begin with it (a job that was started by hand, with a
// nonce) has none (""). The queued line is read as strictly as the other markers, and a kind that is not one makes it no marker at all. The job keeps its kind for as long as the log is there.
//
// The state carries the end of the log as text, without the marker lines: they carry the nonce, which is not for whoever reads a status.
//
// A RESULT line is final: the first one wins, and nothing that comes after it changes the job. A job that finished stays what it was for as long as the log is there.

// The states of a job.
const (
	JobIdle        = "idle"
	JobRunning     = "running"
	JobSucceeded   = "succeeded"
	JobFailed      = "failed"
	JobInterrupted = "interrupted"
	JobRefused     = "refused"
)

// The kinds of a job: what the core started, and said in the first line of the log. The words are those of the verbs of the assistant.
const (
	KindInstall = "install"
	KindRemove  = "remove"
	KindUpdate  = "update"
)

// ValidKind says whether kind is the kind of a job.
func ValidKind(kind string) bool {
	return kind == KindInstall || kind == KindRemove || kind == KindUpdate
}

// The words of Job.Undo: the last field of the RESULT line of a job that failed. The empty word is a failure that had nothing to undo or a verb that does not undo.
const (
	UndoRolledBack = "rolled_back"
	UndoIncomplete = "rollback_incomplete"
)

const (
	// InterruptedAfter is how long a unit that is not there is given, from the last sign of life of the job, to have said its RESULT: the unit can be gone just before the last line reached the
	// log, and it can be gone just after the core queued it and before systemd had it.
	InterruptedAfter = 30 * time.Second
	// SilentAfter is the same for systemd that does not answer: it is not known that the unit is gone, and an apt can be quiet for a long time.
	SilentAfter = 30 * time.Minute
	// TailLines is how many lines of the end of the log the state carries.
	TailLines = 20

	// clockSlack is how far in the future a time of the log may be before it is not taken for a sign of life: a clock that was put back is not a unit that is still at work.
	clockSlack = 30 * time.Second
	// maxLine is the most of a line that is held; a longer line is cut, and a cut line is never a marker.
	maxLine    = 4096
	keptPrefix = "kept_packages: "
	maxKept    = 40

	// queuedMarker begins the first line of the log of a job that the core started: CASAOS_MODULE_QUEUED <nonce> <name> <install|remove> <time>.
	queuedMarker = "CASAOS_MODULE_QUEUED"
	// markerPrefix begins every marker line, whatever follows.
	markerPrefix = "CASAOS_MODULE_"
)

// A UnitSight is what systemd says of the unit of the job.
type UnitSight int

const (
	// UnitGone: systemd answered, and the unit is not running (it is not there, or it ended).
	UnitGone UnitSight = iota
	// UnitActive: the unit is active, activating or deactivating.
	UnitActive
	// UnitUnknown: systemd did not answer.
	UnitUnknown
)

// A Job is how the job of a module is going, or how it went.
type Job struct {
	Name string `json:"name"`
	// Kind is what the job is: KindInstall, KindRemove or KindUpdate, as the core said when it started the job; "" when the log does not say (a job that was not started by the core).
	Kind  string `json:"kind"`
	State string `json:"state"`
	// Step is where the job is: the step of the last STEP line (the rollback's steps are those of a remove behind "rollback_"), and for a job that failed the step of the RESULT line,
	// which is "" when it failed before its first step. "done" for a job that succeeded; "" for one that was refused or has not begun.
	Step string `json:"step"`
	// Code is the reason of a job that was refused or failed: the code of the RESULT line.
	Code string `json:"code"`
	// Undo is UndoRolledBack or UndoIncomplete for an install or an update that failed after it had changed the box, "" otherwise.
	Undo string `json:"undo"`
	// KeptPackages are the packages of the module that an install that failed left on the box, from the sentence kept_packages that comes before the RESULT line: null when it is not said
	// or not known, an empty list when there are none.
	KeptPackages []string `json:"kept_packages"`
	// StartedAt and EndedAt are the times of the first marker and of the RESULT line, RFC 3339 in UTC; "" when there is none.
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	// LogTail is the last TailLines lines of the log that are not marker lines, as text: printable ASCII, a URL reduced to its host, cut.
	LogTail []string `json:"log_tail"`
}

var (
	stepWordRe = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)
	codeWordRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
	// markerTimeRe is the shape of the time of a marker: 2026-10-18T12:00:00Z.
	markerTimeRe = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`)
)

type marker struct {
	kind        string // stepMarker, resultMarker or queuedMarker
	nonce       string
	jobKind     string // a QUEUED line: KindInstall, KindRemove or KindUpdate
	step, phase string // a STEP line; the RESULT line's step is in step too
	verdict     string // a RESULT line: ok, refused or failed
	code, undo  string
	at          time.Time
}

// parseMarker reads a line as a marker of the module called name, strictly: the fields are separated by one blank each, none is empty, every one has its shape, and a RESULT line says a
// verdict that its own fields agree with. It does not look at the nonce beyond its shape: a nonce of "-" is the one of a run by hand, and is not a job's.
func parseMarker(line, name string) (marker, bool) {
	f := strings.Split(line, " ")
	var m marker
	switch {
	case f[0] == stepMarker && len(f) == 6:
		m = marker{kind: stepMarker, nonce: f[1], step: f[3], phase: f[4]}
		if f[2] != name || !stepWordRe.MatchString(m.step) || (m.phase != "begin" && m.phase != "done" && m.phase != "failed" && m.phase != "skipped") {
			return marker{}, false
		}
	case f[0] == resultMarker && len(f) == 8:
		m = marker{kind: resultMarker, nonce: f[1], verdict: f[3], step: f[4], code: f[5], undo: f[6]}
		if f[2] != name || !resultAgrees(m) {
			return marker{}, false
		}
	case f[0] == queuedMarker && len(f) == 5:
		m = marker{kind: queuedMarker, nonce: f[1], jobKind: f[3]}
		if f[2] != name || !ValidKind(m.jobKind) {
			return marker{}, false
		}
	default:
		return marker{}, false
	}
	if !dockerpkg.ValidNonce(m.nonce) {
		return marker{}, false
	}
	// the time as the assistant writes it, in UTC and to the second, and nothing else: a time that is a long fraction of a second, or in another zone, is not a marker's
	at, err := time.Parse(time.RFC3339, f[len(f)-1])
	if err != nil || !markerTimeRe.MatchString(f[len(f)-1]) {
		return marker{}, false
	}
	m.at = at.UTC()
	return m, true
}

// resultAgrees says whether the fields of a RESULT line are those that a run writes for its verdict: an ok ends at "done" with no code and no undo; a refusal has a code, no step and no
// undo; a failure has a code, and a step or "-" and an undo or "-".
func resultAgrees(m marker) bool {
	stepOK := m.step == "-" || stepWordRe.MatchString(m.step)
	codeOK := codeWordRe.MatchString(m.code)
	undoOK := m.undo == "-" || m.undo == UndoRolledBack || m.undo == UndoIncomplete
	switch m.verdict {
	case "ok":
		return m.step == "done" && m.code == "-" && m.undo == "-"
	case "refused":
		return m.step == "-" && codeOK && m.undo == "-"
	case "failed":
		return stepOK && codeOK && undoOK
	}
	return false
}

// QueuedLine is the first line of the log of a job that the core is about to start. It carries the nonce, so that the log is the job's from now on, and the kind of the job (KindInstall or
// KindRemove).
func QueuedLine(name, nonce, kind string, at time.Time) string {
	return queuedMarker + " " + nonce + " " + name + " " + kind + " " + at.UTC().Format(time.RFC3339) + "\n"
}

// A Reducer reads the log of a module line by line and says how the job it holds is going. The zero value is not usable; NewReducer makes one.
type Reducer struct {
	name, nonce string
	fed         int // how many lines, of any kind, were given
	kind        string
	first, last time.Time
	step        string
	result      *marker
	kept        []string
	tail        []string
}

// NewReducer is a reducer for the log of the module called name. nonce is the job's; "" says it is not known, and then it is the nonce of the first marker of the module in the log that
// carries one.
func NewReducer(name, nonce string) *Reducer {
	return &Reducer{name: name, nonce: nonce}
}

// Feed gives the reducer the next line of the log, without its new line. whole says that the line ended with one and was read to its end: the last line of a log that is being written
// does not, and a line that is cut is not whole either. Only a whole line can be a marker.
func (r *Reducer) Feed(line string, whole bool) {
	firstLine := r.fed == 0
	r.fed++
	if len(line) > maxLine {
		line, whole = line[:maxLine], false
	}
	// a marker line carries the nonce, and what is said of the job is said to whoever may read it: the tail is the sentences of the assistant and the output of the tools
	if !strings.HasPrefix(line, markerPrefix) {
		if len(r.tail) == TailLines {
			copy(r.tail, r.tail[1:])
			r.tail = r.tail[:TailLines-1]
		}
		r.tail = append(r.tail, line)
	}
	if !whole {
		return
	}
	if rest, ok := strings.CutPrefix(line, keptPrefix); ok {
		r.keep(rest)
		return
	}
	m, ok := parseMarker(line, r.name)
	if !ok || r.result != nil {
		return
	}
	switch {
	case r.nonce == "":
		r.nonce = m.nonce
	case m.nonce != r.nonce:
		return
	}
	if r.first.IsZero() {
		r.first = m.at
	}
	r.last = m.at
	switch m.kind {
	case stepMarker:
		r.step = m.step
	case resultMarker:
		r.result = &m
	case queuedMarker:
		// the core wrote it as the first line of the log, and what says the kind is that line: one further down is nobody's we know of
		if firstLine {
			r.kind = m.jobKind
		}
	}
}

// keep reads the sentence that says which packages an install that failed leaves on the box: "unknown", "none" or the names, once each. A sentence that is not one of these says nothing.
func (r *Reducer) keep(rest string) {
	if r.result != nil {
		return
	}
	r.kept = nil
	if rest == "unknown" {
		return
	}
	if rest == "none" {
		r.kept = []string{}
		return
	}
	names := strings.Split(rest, " ")
	if len(names) > maxKept {
		return
	}
	seen := map[string]bool{}
	for _, n := range names {
		if !osmodule.ValidPackageName(n) || seen[n] {
			return
		}
		seen[n] = true
	}
	r.kept = names
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Job says how the job is going, from what was fed, the state of the unit (read BEFORE the log was: a unit that is gone has written everything it wrote), the time now and the time the
// log was last written (zero when it is not known).
func (r *Reducer) Job(unit UnitSight, now, logWritten time.Time) Job {
	job := Job{Name: r.name, State: JobIdle, LogTail: []string{}}
	if r.first.IsZero() {
		// no marker of a job: whatever the log holds is nobody's we know of, unless it holds nothing, which is a unit that has truncated it and not yet said a word
		switch {
		case unit == UnitActive:
			job.State = JobRunning
		case r.fed == 0 && !logWritten.IsZero():
			job.State = quietState(unit, now, logWritten)
		}
		return job
	}
	job.Kind, job.StartedAt, job.Step = r.kind, stamp(r.first), r.step
	for _, l := range r.tail {
		if l = osplan.SanitizeLine(l); l != "" {
			job.LogTail = append(job.LogTail, l)
		}
	}
	if m := r.result; m != nil {
		job.EndedAt = stamp(m.at)
		switch m.verdict {
		case "ok":
			job.State, job.Step = JobSucceeded, m.step
		case "refused":
			job.State, job.Step, job.Code = JobRefused, "", m.code
		default:
			job.State, job.Code = JobFailed, m.code
			job.Step = dashless(m.step)
			job.Undo = dashless(m.undo)
			job.KeptPackages = r.kept
		}
		return job
	}
	if unit == UnitActive {
		job.State = JobRunning
		return job
	}
	job.State = quietState(unit, now, r.last, logWritten)
	return job
}

// quietState is the state of a job whose unit is not known to run and that has not ended: running when there is a sign of life that is recent enough (the time of the last marker, the time
// the log was written; a zero time is none, and so is one that is more than clockSlack in the future), and interrupted when there is not.
func quietState(unit UnitSight, now time.Time, signs ...time.Time) string {
	limit := InterruptedAfter
	if unit == UnitUnknown {
		limit = SilentAfter
	}
	for _, t := range signs {
		if age := now.Sub(t); !t.IsZero() && age >= -clockSlack && age < limit {
			return JobRunning
		}
	}
	return JobInterrupted
}

func dashless(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// ReduceLines is the job that the lines of a log, all of them whole, are the word of.
func ReduceLines(name, nonce string, lines []string, unit UnitSight, now, logWritten time.Time) Job {
	r := NewReducer(name, nonce)
	for _, l := range lines {
		r.Feed(l, true)
	}
	return r.Job(unit, now, logWritten)
}
