package modulecmd

import (
	"math/rand"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// An oracle that shares no code with the reducer: the whole lines that are the word of a job that ended, by their spelling. A job is over only if the log holds one of them.
var resultOracle = regexp.MustCompile(`^CASAOS_MODULE_RESULT ` + jNonce + ` virt (ok done - -|refused - [a-z][a-z0-9_]{0,47} -|failed (-|[a-z][a-z_]{0,31}) [a-z][a-z0-9_]{0,47} (-|rolled_back|rollback_incomplete)) [0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|30)T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]Z$`)

// queuedOracle is the first line of a log that says what a job is, by its spelling: the nonce and the kind are its first and its last group.
var queuedOracle = regexp.MustCompile(`^CASAOS_MODULE_QUEUED ([0-9a-f]{32}) virt (install|remove) [0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]Z$`)

// checkKind pins the kind of a job to the first line of its log: it is the kind that line says, if the line is the queued line of the job (of the nonce that was given, or of any when none
// was), and none otherwise.
func checkKind(t testing.TB, lines []string, nonce string, job Job) {
	t.Helper()
	want := ""
	if len(lines) > 0 {
		if m := queuedOracle.FindStringSubmatch(lines[0]); m != nil && (nonce == "" || m[1] == nonce) {
			want = m[2]
		}
	}
	if job.Kind != want {
		t.Fatalf("kind %q, want %q (the log begins with %q)", job.Kind, want, lines[:min(len(lines), 2)])
	}
}

// anyResult is a result line of any nonce, for the logs whose job is the first marker's.
var anyResult = regexp.MustCompile(`^CASAOS_MODULE_RESULT [0-9a-f]{32} virt ok done - - [0-9T:Z-]+$`)

func checkJob(t testing.TB, lines []string, job Job) {
	t.Helper()
	switch job.State {
	case JobIdle, JobRunning, JobInterrupted, JobSucceeded, JobFailed, JobRefused:
	default:
		t.Fatalf("a state that is none: %q", job.State)
	}
	if len(job.LogTail) > TailLines || job.LogTail == nil {
		t.Fatalf("a tail of %d lines (%v)", len(job.LogTail), job.LogTail == nil)
	}
	for _, l := range job.LogTail {
		if len(l) > 200 || l == "" {
			t.Fatalf("a tail line of %d bytes", len(l))
		}
		for i := 0; i < len(l); i++ {
			if l[i] < 0x20 || l[i] > 0x7e {
				t.Fatalf("a tail line with the byte %#x", l[i])
			}
		}
	}
	terminal := job.State == JobSucceeded || job.State == JobFailed || job.State == JobRefused
	var first string
	for _, l := range lines {
		if resultOracle.MatchString(l) {
			first = l
			break
		}
	}
	if terminal && first == "" {
		t.Fatalf("a job that ended (%s) with no line that says it did", job.State)
	}
	if !terminal && first != "" {
		// the first such line is only a result if the log did not have to be read as being of another job: the nonce is given, so it is
		t.Fatalf("a line that ends the job and the state is %s: %q", job.State, first)
	}
	if terminal {
		f := strings.Split(first, " ")
		want := map[string]string{"ok": JobSucceeded, "refused": JobRefused, "failed": JobFailed}[f[3]]
		if job.State != want {
			t.Fatalf("the first result says %s and the state is %s", f[3], job.State)
		}
		if job.State != JobSucceeded && job.Code != f[5] {
			t.Fatalf("code %q, the line says %q", job.Code, f[5])
		}
		if job.State == JobSucceeded && (job.Code != "" || job.Undo != "" || job.Step != "done") {
			t.Fatalf("a success with %+v", job)
		}
	}
	if job.State != JobFailed && (job.Undo != "" || job.KeptPackages != nil) {
		t.Fatalf("%s with undo %q and kept %v", job.State, job.Undo, job.KeptPackages)
	}
	for _, s := range []string{job.StartedAt, job.EndedAt} {
		if s != "" {
			if _, err := time.Parse(time.RFC3339, s); err != nil {
				t.Fatalf("time %q", s)
			}
		}
	}
	if job.Step != "" && !stepWordRe.MatchString(job.Step) {
		t.Fatalf("step %q", job.Step)
	}
}

// mutate changes a line a little, the way a hostile log or a cut one would.
func mutate(rnd *rand.Rand, l string) string {
	if l == "" {
		return l
	}
	switch rnd.Intn(9) {
	case 0:
		return l[:rnd.Intn(len(l))]
	case 1:
		i := rnd.Intn(len(l))
		return l[:i] + string(rune(rnd.Intn(256))) + l[i+1:]
	case 2:
		i := rnd.Intn(len(l))
		return l[:i] + " " + l[i:]
	case 3:
		return strings.Replace(l, " ", "  ", 1)
	case 4:
		return "| " + l
	case 5:
		return " " + l
	case 6:
		return l + "\r"
	case 7:
		return strings.Replace(l, jNonce, jOther, 1)
	}
	return strings.ToUpper(l)
}

func randomLog(rnd *rand.Rand) []string {
	pool := []string{
		jQueued(jNonce, 0), jQueued(jOther, 1), jQueued("-", 1), jQueuedAs(jNonce, KindRemove, 0), jQueuedAs(jOther, KindRemove, 1), jQueuedAs(jNonce, "upgrade", 0),
		"CASAOS_MODULE_QUEUED " + jNonce + " virt " + jts(0), jQueuedAs(jNonce, KindInstall, 0) + " x", "| " + jQueuedAs(jNonce, KindRemove, 0),
		jStep(jNonce, "packages", "begin", 2), jStep(jNonce, "packages", "done", 3), jStep(jNonce, "verify", "failed", 4), jStep(jNonce, "rollback_stop", "begin", 5),
		jStep(jOther, "packages", "done", 6), jStep("-", "start", "begin", 6),
		jResult(jNonce, "ok", "done", "-", "-", 10), jResult(jNonce, "refused", "-", "busy", "-", 11), jResult(jNonce, "failed", "route", "route_timeout", "rolled_back", 12),
		jResult(jNonce, "failed", "-", "internal_error", "-", 13), jResult(jOther, "ok", "done", "-", "-", 14), jResult("-", "ok", "done", "-", "-", 15),
		"| " + jResult(jNonce, "ok", "done", "-", "-", 16), "kept_packages: aa bb", "kept_packages: none", "kept_packages: unknown", "refused: busy", "installed: virt v0.1.0",
		"| apt: Unpacking x", "", " ", "\x00\x00\x00", strings.Repeat("A", 6000), strings.Repeat("CASAOS_MODULE_RESULT ", 200),
	}
	lines := make([]string, rnd.Intn(40))
	for i := range lines {
		l := pool[rnd.Intn(len(pool))]
		if rnd.Intn(3) == 0 {
			l = mutate(rnd, l)
		}
		lines[i] = l
	}
	return lines
}

func TestNoLogYieldsAnEndWithoutTheWordOfTheJob(t *testing.T) {
	units := []UnitSight{UnitGone, UnitActive, UnitUnknown}
	for seed := int64(1); seed <= 3000; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		lines := randomLog(rnd)
		unit := units[rnd.Intn(len(units))]
		now := at(rnd.Intn(4000) - 500)
		job := ReduceLines(jName, jNonce, lines, unit, now, time.Time{})
		checkJob(t, lines, job)
		checkKind(t, lines, jNonce, job)
		// with no nonce given the first marker of the module with one is the job's: it may be another's, but never a success without a line that says so
		adopted := ReduceLines(jName, "", lines, unit, now, time.Time{})
		checkKind(t, lines, "", adopted)
		if adopted.State == JobSucceeded {
			var ok bool
			for _, l := range lines {
				ok = ok || anyResult.MatchString(l)
			}
			if !ok {
				t.Fatalf("seed %d: a success with no line that says it", seed)
			}
		}
	}
}

// What is read from a file is what is reduced from its lines.
func TestAFileIsReducedAsItsLinesAre(t *testing.T) {
	env := jobBox(t)
	for seed := int64(1); seed <= 150; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		lines := randomLog(rnd)
		content := strings.Join(lines, "\n") + "\n"
		if len(lines) == 0 {
			content = "" // (a file with no line in it is an empty file, which is not a file with one empty line)
		}
		writeJobLog(t, env, content)
		for _, unit := range []UnitSight{UnitGone, UnitActive, UnitUnknown} {
			got, err := ReadJob(env, jName, unit, at(2000))
			if err != nil {
				t.Fatal(err)
			}
			want := ReduceLines(jName, "", lines, unit, at(2000), at(-3600))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("seed %d, unit %v:\n got %+v\nwant %+v", seed, unit, got, want)
			}
		}
	}
}

func FuzzReduceLines(f *testing.F) {
	f.Add(logOf(jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1), jResult(jNonce, "ok", "done", "-", "-", 2)))
	f.Add(logOf(jQueued(jNonce, 0), "| "+jResult(jNonce, "ok", "done", "-", "-", 2)))
	f.Add(logOf(jQueuedAs(jNonce, KindRemove, 0), jQueuedAs(jNonce, KindInstall, 1)))
	f.Add(logOf(jResult(jNonce, "failed", "start", "start_failed", "rolled_back", 2), jResult(jNonce, "ok", "done", "-", "-", 3)))
	f.Add("CASAOS_MODULE_RESULT " + jNonce + " virt ok done - - " + jts(1))
	f.Add(strings.Repeat("\x00", 300))
	f.Fuzz(func(t *testing.T, raw string) {
		lines := strings.Split(raw, "\n")
		for _, unit := range []UnitSight{UnitGone, UnitActive, UnitUnknown} {
			job := ReduceLines(jName, jNonce, lines, unit, at(100), time.Time{})
			checkJob(t, lines, job)
			checkKind(t, lines, jNonce, job)
		}
	})
}
