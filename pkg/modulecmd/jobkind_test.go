package modulecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// The kind of a job: what the core started (an install or a remove), said once, in the first line of the log. The markers of a run are the same for both, so the log alone could not
// tell an install that failed at "manifest" from a remove that did.

func TestTheKindOfAJobIsTheOneOfTheQueuedLineWhateverTheJobIsDoing(t *testing.T) {
	endings := map[string]struct {
		lines []string
		unit  UnitSight
		now   int
		state string
	}{
		"queued":       {nil, UnitActive, 5, JobRunning},
		"queued, gone": {nil, UnitGone, 100, JobInterrupted},
		"running":      {jSteps(jNonce, 1, "packages", "download"), UnitActive, 7, JobRunning},
		"interrupted":  {jSteps(jNonce, 1, "packages"), UnitGone, 500, JobInterrupted},
		"unit silent":  {jSteps(jNonce, 1, "packages"), UnitUnknown, 100, JobRunning},
		"succeeded":    {[]string{jStep(jNonce, "done", "done", 5), jResult(jNonce, "ok", "done", "-", "-", 5)}, UnitGone, 50, JobSucceeded},
		"failed": {[]string{jStep(jNonce, "manifest", "begin", 2), jStep(jNonce, "manifest", "failed", 3), "kept_packages: none", jResult(jNonce, "failed", "manifest", "manifest_invalid", "rolled_back", 6)},
			UnitGone, 50, JobFailed},
		"refused":         {[]string{"refused: plan_changed", jResult(jNonce, "refused", "-", "plan_changed", "-", 2)}, UnitGone, 50, JobRefused},
		"finished a year": {[]string{jResult(jNonce, "ok", "done", "-", "-", 2)}, UnitGone, 365 * 24 * 3600, JobSucceeded},
	}
	for _, kind := range []string{KindInstall, KindRemove} {
		for name, e := range endings {
			for _, given := range []string{jNonce, ""} { // the nonce the core remembers, and none (the core was restarted)
				lines := append([]string{jQueuedAs(jNonce, kind, 0)}, e.lines...)
				got := reduce(given, lines, e.unit, at(e.now))
				if got.Kind != kind || got.State != e.state {
					t.Errorf("%s, %s, nonce %q: kind %q state %s, want %s %s", kind, name, given, got.Kind, got.State, kind, e.state)
				}
			}
		}
	}
}

func TestAJobWhoseLogDoesNotSayWhatItIsHasNoKind(t *testing.T) {
	steps := jSteps(jNonce, 1, "packages")
	ended := []string{jStep(jNonce, "done", "done", 5), jResult(jNonce, "ok", "done", "-", "-", 5)}
	cases := map[string][]string{
		"no log":                       nil,
		"the lines of the unit only":   steps,
		"the lines of a unit, ended":   append(slicesClone(steps), ended...),
		"a refusal of the unit only":   {"refused: busy", jResult(jNonce, "refused", "-", "busy", "-", 2)},
		"a run by hand":                {jStep("-", "packages", "begin", 1), jResult("-", "ok", "done", "-", "-", 2)},
		"the queued line of a module":  {strings.Replace(jQueued(jNonce, 0), " virt ", " other ", 1), jStep(jNonce, "packages", "begin", 1)},
		"a queued line of the old way": {"CASAOS_MODULE_QUEUED " + jNonce + " virt " + jts(0), jStep(jNonce, "packages", "begin", 1)},
	}
	for name, lines := range cases {
		for _, given := range []string{jNonce, ""} {
			if got := reduce(given, lines, UnitGone, at(5)); got.Kind != "" {
				t.Errorf("%s, nonce %q: kind %q, want none (%+v)", name, given, got.Kind, got)
			}
		}
	}
}

// A queued line that is not as the core writes it is no word of anybody's: it does not say a kind, it does not start the job (the job starts with the first line that is), and the
// lines of the unit go on being read.
func TestAQueuedLineThatIsNotAsTheCoreWritesItSaysNoKind(t *testing.T) {
	bad := map[string]string{
		"no kind (the old way)":    "CASAOS_MODULE_QUEUED " + jNonce + " virt " + jts(0),
		"a kind that is not one":   jQueuedAs(jNonce, "upgrade", 0),
		"a kind in capitals":       jQueuedAs(jNonce, "REMOVE", 0),
		"an empty kind":            jQueuedAs(jNonce, "", 0),
		"a dash":                   jQueuedAs(jNonce, "-", 0),
		"two kinds":                jQueuedAs(jNonce, "install remove", 0),
		"a kind and more":          jQueuedAs(jNonce, "install", 0) + " x",
		"another module's":         strings.Replace(jQueuedAs(jNonce, "remove", 0), " virt ", " other ", 1),
		"a nonce of a run by hand": jQueuedAs("-", "remove", 0),
		"a tool's line":            "| " + jQueuedAs(jNonce, "remove", 0),
	}
	for name, line := range bad {
		lines := []string{line, jStep(jNonce, "packages", "begin", 5), jStep(jNonce, "packages", "done", 6)}
		for _, given := range []string{jNonce, ""} {
			got := reduce(given, lines, UnitActive, at(7))
			if got.Kind != "" || got.State != JobRunning || got.Step != "packages" || got.StartedAt != jts(5) {
				t.Errorf("%s, nonce %q: %+v", name, given, got)
			}
		}
		if got := reduce(jNonce, lines[:1], UnitGone, at(7)); got.Kind != "" || got.State != JobIdle {
			t.Errorf("%s alone: %+v", name, got)
		}
	}
}

// The core writes the kind in the first line of the log and nowhere else: a queued line further down is not the core's.
func TestOnlyTheFirstLineOfTheLogSaysTheKind(t *testing.T) {
	cases := map[string]struct {
		lines []string
		kind  string
	}{
		"a step before":                      {[]string{jStep(jNonce, "packages", "begin", 1), jQueuedAs(jNonce, KindRemove, 2)}, ""},
		"a sentence before":                  {[]string{"refused: busy", jQueuedAs(jNonce, KindRemove, 2), jStep(jNonce, "packages", "begin", 3)}, ""},
		"a line of a tool before":            {[]string{"| apt", jQueuedAs(jNonce, KindRemove, 2)}, ""},
		"an empty line before":               {[]string{"", jQueuedAs(jNonce, KindRemove, 2)}, ""},
		"a second queued line, of the other": {[]string{jQueuedAs(jNonce, KindInstall, 0), jQueuedAs(jNonce, KindRemove, 1)}, KindInstall},
		"a second queued line, the same":     {[]string{jQueuedAs(jNonce, KindRemove, 0), jQueuedAs(jNonce, KindRemove, 1)}, KindRemove},
		"after the steps":                    {append([]string{jQueuedAs(jNonce, KindInstall, 0)}, append(jSteps(jNonce, 1, "packages"), jQueuedAs(jNonce, KindRemove, 5))...), KindInstall},
		"after a result":                     {[]string{jQueuedAs(jNonce, KindInstall, 0), jResult(jNonce, "ok", "done", "-", "-", 1), jQueuedAs(jNonce, KindRemove, 2)}, KindInstall},
		"a tool says one after":              {[]string{jQueuedAs(jNonce, KindInstall, 0), "| " + jQueuedAs(jNonce, KindRemove, 1)}, KindInstall},
		"one of another job after":           {[]string{jQueuedAs(jNonce, KindInstall, 0), jQueuedAs(jOther, KindRemove, 1)}, KindInstall},
	}
	for name, c := range cases {
		for _, given := range []string{jNonce, ""} {
			if got := reduce(given, c.lines, UnitGone, at(3)); got.Kind != c.kind {
				t.Errorf("%s, nonce %q: kind %q, want %q", name, given, got.Kind, c.kind)
			}
		}
	}
}

// The kind is the word of the job whose nonce this is: when the nonce of the job is given, the queued line of another job says nothing, and when it is not, it is the nonce of the first marker.
func TestTheKindIsTheWordOfTheJobAndOfNoOtherJob(t *testing.T) {
	other := []string{jQueuedAs(jOther, KindRemove, 0), jStep(jOther, "stop", "begin", 1)}
	if got := reduce(jNonce, other, UnitGone, at(2)); got.Kind != "" || got.State != JobIdle {
		t.Errorf("a job of another nonce: %+v", got)
	}
	if got := reduce("", other, UnitActive, at(2)); got.Kind != KindRemove || got.Step != "stop" {
		t.Errorf("the nonce is found again: %+v", got)
	}
	// and the lines of another nonce after the first marker do not change it
	mixed := []string{jQueuedAs(jNonce, KindInstall, 0), jStep(jOther, "stop", "begin", 1), jResult(jOther, "ok", "done", "-", "-", 2)}
	if got := reduce("", mixed, UnitActive, at(3)); got.Kind != KindInstall || got.State != JobRunning || got.Step != "" {
		t.Errorf("%+v", got)
	}
}

// The job as the page is told it: these keys and no others, the kind among them.
func TestTheJobIsTheseKeysOfJSON(t *testing.T) {
	want := []string{"code", "ended_at", "kept_packages", "kind", "log_tail", "name", "started_at", "state", "step", "undo"}
	for name, job := range map[string]Job{
		"idle":       reduce(jNonce, nil, UnitGone, at(0)),
		"a remove":   reduce(jNonce, []string{jQueuedAs(jNonce, KindRemove, 0)}, UnitActive, at(1)),
		"an install": reduce(jNonce, []string{jQueuedAs(jNonce, KindInstall, 0), jResult(jNonce, "ok", "done", "-", "-", 1)}, UnitGone, at(2)),
	} {
		raw, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		var keys map[string]any
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatal(err)
		}
		var got []string
		for k := range keys {
			got = append(got, k)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: keys %v, want %v", name, got, want)
		}
		if _, isString := keys["kind"].(string); !isString {
			t.Errorf("%s: kind is %#v, want a string (\"\" when it is not known)", name, keys["kind"])
		}
	}
	raw, _ := json.Marshal(reduce(jNonce, []string{jQueuedAs(jNonce, KindRemove, 0)}, UnitActive, at(1)))
	if !strings.Contains(string(raw), `"kind":"remove"`) {
		t.Errorf("%s", raw)
	}
	raw, _ = json.Marshal(reduce(jNonce, nil, UnitGone, at(1)))
	if !strings.Contains(string(raw), `"kind":""`) {
		t.Errorf("%s", raw)
	}
}

func TestOnlyAnInstallAndARemoveAreKinds(t *testing.T) {
	for kind, want := range map[string]bool{KindInstall: true, KindRemove: true, "": false, "Install": false, "REMOVE": false, "upgrade": false, "install ": false, " remove": false, "install\n": false, "installx": false, "-": false} {
		if got := ValidKind(kind); got != want {
			t.Errorf("ValidKind(%q) = %v", kind, got)
		}
	}
	if KindInstall != "install" || KindRemove != "remove" {
		t.Errorf("the words of the kinds are %q and %q", KindInstall, KindRemove)
	}
}

// ---- from the file --------------------------------------------------------------------------------------------------

func TestTheKindIsReadFromTheFirstLineOfTheLogFile(t *testing.T) {
	for _, kind := range []string{KindInstall, KindRemove} {
		env := jobBox(t)
		if err := Queue(env, jName, jNonce, kind, at(100)); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(env.Roots.logFile(jName)); string(got) != "CASAOS_MODULE_QUEUED "+jNonce+" virt "+kind+" "+jts(100)+"\n" {
			t.Fatalf("the log is %q", got)
		}
		if job, err := ReadJob(env, jName, UnitActive, at(101)); err != nil || job.Kind != kind || job.State != JobRunning || job.StartedAt != jts(100) {
			t.Fatalf("%s: %+v, %v", kind, job, err)
		}
		// the next job of the module replaces it, whatever it is
		other := KindRemove
		if kind == KindRemove {
			other = KindInstall
		}
		if err := Queue(env, jName, jOther, other, at(200)); err != nil {
			t.Fatal(err)
		}
		if job, _ := ReadJob(env, jName, UnitActive, at(201)); job.Kind != other {
			t.Errorf("%s then %s: %+v", kind, other, job)
		}
	}
}

// A log that is cut is not a log that says something: of the queued line, only the whole line, with its new line, says its kind.
func TestACutQueuedLineIsNotRead(t *testing.T) {
	line := QueuedLine(jName, jNonce, KindRemove, at(100))
	for n := 0; n <= len(line); n++ {
		env := jobBox(t)
		writeJobLog(t, env, line[:n])
		job, err := ReadJob(env, jName, UnitGone, at(101))
		if err != nil {
			t.Fatal(err)
		}
		if n == len(line) {
			if job.Kind != KindRemove || job.State != JobRunning {
				t.Errorf("the whole line: %+v", job)
			}
			continue
		}
		if job.Kind != "" || job.StartedAt != "" || job.State == JobSucceeded || job.State == JobFailed || job.State == JobRefused {
			t.Errorf("the line cut at %d bytes (%q): %+v", n, line[:n], job)
		}
	}
	// the same line with a NUL in any place
	for i := 0; i < len(line)-1; i++ {
		env := jobBox(t)
		writeJobLog(t, env, line[:i]+"\x00"+line[i+1:])
		if job, _ := ReadJob(env, jName, UnitGone, at(101)); job.Kind != "" || job.StartedAt != "" {
			t.Errorf("a NUL at %d: %+v", i, job)
		}
	}
}

func TestTheKindIsNotTakenFromWhatFollowsAHugeFirstLine(t *testing.T) {
	env := jobBox(t)
	writeJobLog(t, env, strings.Repeat("A", 1<<20)+"\n"+QueuedLine(jName, jNonce, KindRemove, at(100)))
	if job, err := ReadJob(env, jName, UnitGone, at(101)); err != nil || job.Kind != "" {
		t.Errorf("%+v, %v", job, err)
	}
	// a first line that is a queued line and goes on for a megabyte is not one
	writeJobLog(t, env, strings.TrimSuffix(QueuedLine(jName, jNonce, KindRemove, at(100)), "\n")+strings.Repeat(" x", 1<<19)+"\n")
	if job, err := ReadJob(env, jName, UnitGone, at(101)); err != nil || job.Kind != "" || job.State != JobIdle {
		t.Errorf("%+v, %v", job, err)
	}
}

// A log that is longer than what is read is read by its two ends, and the first line is in the first of them: a job that talked a lot has its kind.
func TestTheKindOfAJobWhoseLogIsHugeIsKept(t *testing.T) {
	saved := logScanBytes
	logScanBytes = 64 << 10
	defer func() { logScanBytes = saved }()
	env := jobBox(t)
	body := QueuedLine(jName, jNonce, KindRemove, at(0)) + logOf(jStep(jNonce, "stop", "begin", 1)) + strings.Repeat("| x\n", 100000) + logOf(jStep(jNonce, "done", "done", 8), jResult(jNonce, "ok", "done", "-", "-", 9))
	writeJobLog(t, env, body)
	job, err := ReadJob(env, jName, UnitGone, at(1000))
	if err != nil || job.State != JobSucceeded || job.Kind != KindRemove || job.StartedAt != jts(0) {
		t.Errorf("%+v, %v", job, err)
	}
	// and a queued line in the part that is not read is not read
	body = logOf(jStep(jNonce, "stop", "begin", 1)) + strings.Repeat("| x\n", 100000) + QueuedLine(jName, jNonce, KindRemove, at(5)) + strings.Repeat("| y\n", 100000)
	writeJobLog(t, env, body)
	if job, _ := ReadJob(env, jName, UnitGone, at(1000)); job.Kind != "" {
		t.Errorf("%+v", job)
	}
}

// ---- the core writes it ---------------------------------------------------------------------------------------------

func TestTheQueuedLineHasTheKindBeforeTheTime(t *testing.T) {
	when := time.Date(2026, 10, 18, 14, 0, 0, 0, time.FixedZone("x", 7200))
	for kind, want := range map[string]string{
		KindInstall: "CASAOS_MODULE_QUEUED " + jNonce + " virt install 2026-10-18T12:00:00Z\n",
		KindRemove:  "CASAOS_MODULE_QUEUED " + jNonce + " virt remove 2026-10-18T12:00:00Z\n",
	} {
		if got := QueuedLine(jName, jNonce, kind, when); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
}

func TestTheCoreQueuesAndAbandonsOnlyAKindItCanName(t *testing.T) {
	for _, kind := range []string{"", "Install", "REMOVE", "upgrade", "install remove", "install\n", " install", "-"} {
		env := jobBox(t)
		if err := Queue(env, jName, jNonce, kind, at(0)); err == nil {
			t.Errorf("Queue(%q) queued a job", kind)
		}
		if err := Abandon(env, jName, jNonce, kind, at(0), at(1), nil); err == nil {
			t.Errorf("Abandon(%q) wrote a log", kind)
		}
		if entries, _ := os.ReadDir(env.Roots.Prefix); len(entries) != 0 {
			t.Errorf("something was written for the kind %q: %v", kind, entries)
		}
	}
}

func TestAJobThatCouldNotBeStartedKeepsItsKind(t *testing.T) {
	for _, kind := range []string{KindInstall, KindRemove} {
		env := jobBox(t)
		if err := Queue(env, jName, jNonce, kind, at(100)); err != nil {
			t.Fatal(err)
		}
		if err := Abandon(env, jName, jNonce, kind, at(100), at(102), []string{"Failed to connect to bus"}); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(env.Roots.logFile(jName))
		if !strings.HasPrefix(string(got), QueuedLine(jName, jNonce, kind, at(100))) {
			t.Errorf("the log begins with %q", got)
		}
		if job, _ := ReadJob(env, jName, UnitGone, at(103)); job.State != JobFailed || job.Code != "start_failed" || job.Kind != kind {
			t.Errorf("%s: %+v", kind, job)
		}
	}
	// the log of the other kind is not the one that this job wrote
	env := jobBox(t)
	if err := Queue(env, jName, jNonce, KindRemove, at(100)); err != nil {
		t.Fatal(err)
	}
	if err := Abandon(env, jName, jNonce, KindInstall, at(100), at(102), nil); err == nil {
		t.Errorf("a log that says remove was abandoned as an install")
	}
}

// ---- the assistant keeps it -----------------------------------------------------------------------------------------

// The unit replaces the log with its own lines, and the first line that the core wrote stays: the markers of the assistant do not say what it is doing, and the page asks, until the job
// is over and for as long as the log is there.

func queueAndRun(t *testing.T, f *fixture, nonce, kind string, run func() int) (code int, log string) {
	t.Helper()
	f.envVars["CASAOS_MODULE_NONCE"] = nonce
	if err := Queue(f.env, testName, nonce, kind, at(100)); err != nil {
		t.Fatal(err)
	}
	code = run()
	return code, f.read("/var/log/casaos/module-virt.log")
}

func TestTheAssistantKeepsTheQueuedLineOfAnInstall(t *testing.T) {
	f := newFixture(t)
	code, log := queueAndRun(t, f, jNonce, KindInstall, func() int { return f.install("--yes") })
	if code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	queued := QueuedLine(testName, jNonce, KindInstall, at(100))
	if !strings.HasPrefix(log, queued+"CASAOS_MODULE_STEP "+jNonce+" virt packages begin ") || strings.Count(log, "CASAOS_MODULE_QUEUED") != 1 {
		t.Errorf("the log begins with %q", log[:min(len(log), 300)])
	}
	job, err := ReadJob(f.env, testName, UnitGone, at(101))
	if err != nil || job.State != JobSucceeded || job.Kind != KindInstall || job.Step != "done" || job.StartedAt != jts(100) {
		t.Errorf("%+v, %v", job, err)
	}
}

func TestTheAssistantKeepsTheQueuedLineOfARemove(t *testing.T) {
	f := installed(t)
	code, log := queueAndRun(t, f, jNonce, KindRemove, func() int { return f.remove("--yes") })
	if code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if !strings.HasPrefix(log, QueuedLine(testName, jNonce, KindRemove, at(100))+"CASAOS_MODULE_STEP "+jNonce+" virt stop begin ") || strings.Count(log, "CASAOS_MODULE_QUEUED") != 1 {
		t.Errorf("the log begins with %q", log[:min(len(log), 300)])
	}
	job, err := ReadJob(f.env, testName, UnitGone, at(101))
	if err != nil || job.State != JobSucceeded || job.Kind != KindRemove || job.Step != "done" {
		t.Errorf("%+v, %v", job, err)
	}
}

func TestAJobThatFailedOrWasRefusedHasItsKind(t *testing.T) {
	// an install that fails at the verification and is rolled back
	f := newFixture(t)
	f.dl.body = gz(f.t, tarBytes(f.t, moduleEntries("{}")))
	code, _ := queueAndRun(t, f, jNonce, KindInstall, func() int { return f.install("--yes") })
	job, _ := ReadJob(f.env, testName, UnitGone, at(101))
	if code != ExitFailed || job.State != JobFailed || job.Step != StepVerify || job.Kind != KindInstall {
		t.Errorf("failed: exit %d %+v", code, job)
	}
	// an install whose plan is no longer the one that was read, refused once the log is open
	f = newFixture(t)
	code, _ = queueAndRun(t, f, jNonce, KindInstall, func() int { return f.install("--yes", "--plan-id", "m1-"+strings.Repeat("0", 32)) })
	job, _ = ReadJob(f.env, testName, UnitGone, at(101))
	if code != ExitRefused || job.State != JobRefused || job.Code != CodePlanChanged || job.Kind != KindInstall {
		t.Errorf("refused: exit %d %+v", code, job)
	}
	// a remove of what is not there
	f = newFixture(t)
	code, _ = queueAndRun(t, f, jNonce, KindRemove, func() int { return f.remove("--yes") })
	job, _ = ReadJob(f.env, testName, UnitGone, at(101))
	if code != ExitRefused || job.State != JobRefused || job.Code != CodeNotInstalled || job.Kind != KindRemove {
		t.Errorf("refused remove: exit %d %+v", code, job)
	}
}

// A run that is refused before it has the log leaves the log as the core wrote it.
func TestARunRefusedBeforeItOpensTheLogLeavesTheQueuedLine(t *testing.T) {
	f := newFixture(t)
	f.env.EUID = func() int { return 1000 }
	code, log := queueAndRun(t, f, jNonce, KindInstall, func() int { return f.install("--yes") })
	if code != ExitRefused || log != QueuedLine(testName, jNonce, KindInstall, at(100)) {
		t.Errorf("exit %d, the log is %q", code, log)
	}
	if job, _ := ReadJob(f.env, testName, UnitGone, at(105)); job.Kind != KindInstall || job.State != JobRunning {
		t.Errorf("%+v", job)
	}
}

// The assistant keeps the first line only when it is the line of the job it is: the module's, with the nonce that it was given, with the kind of the verb it runs, as the core writes it.
func TestTheAssistantKeepsOnlyTheQueuedLineOfItsOwnJob(t *testing.T) {
	good := QueuedLine(testName, jNonce, KindInstall, at(100))
	// (an old log longer than anything the run writes: what is not cut away is seen at the end of the file)
	old := strings.Repeat("| the old log\n", 6000)
	cases := map[string]struct {
		log   string
		nonce string // given to the assistant
		kept  string // the first line of the log it leaves, "" for one that is not a queued line
	}{
		"the line of its job":                        {good, jNonce, good},
		"and the lines of an older run behind it":    {good + old, jNonce, good},
		"the same, with no new line at the end":      {strings.TrimSuffix(good, "\n"), jNonce, ""},
		"the line of another job":                    {QueuedLine(testName, jOther, KindInstall, at(100)), jNonce, ""},
		"the line of a remove":                       {QueuedLine(testName, jNonce, KindRemove, at(100)), jNonce, ""},
		"the line of another module":                 {QueuedLine("other", jNonce, KindInstall, at(100)), jNonce, ""},
		"no nonce was given":                         {good, "", ""},
		"a nonce that is not one":                    {good, "NOT-A-NONCE", ""},
		"the old way (no kind)":                      {"CASAOS_MODULE_QUEUED " + jNonce + " virt " + jts(100) + "\n", jNonce, ""},
		"a kind that is not one":                     {"CASAOS_MODULE_QUEUED " + jNonce + " virt upgrade " + jts(100) + "\n", jNonce, ""},
		"a line more in the first":                   {strings.TrimSuffix(good, "\n") + " x\n", jNonce, ""},
		"a queued line that is not the first line":   {old + good, jNonce, ""},
		"a queued line in the output of a tool":      {"| " + good, jNonce, ""},
		"a line that begins with the queued line":    {strings.TrimSuffix(good, "\n") + strings.Repeat(" ", 3000) + "\n", jNonce, ""},
		"a first line of a megabyte":                 {strings.Repeat("Q", 1<<20) + "\n" + good, jNonce, ""},
		"a log that is a queued line, a NUL, a rest": {good + "\x00\x00" + old, jNonce, good},
		"an empty log":                               {"", jNonce, ""},
		"no log":                                     {"-", jNonce, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.envVars["CASAOS_MODULE_NONCE"] = c.nonce
			if c.log != "-" {
				f.writeFile("/var/log/casaos/module-virt.log", c.log)
			}
			if code := f.install("--yes"); code != ExitOK {
				t.Fatalf("exit %d:\n%s", code, f.out)
			}
			log := f.read("/var/log/casaos/module-virt.log")
			if c.kept == "" {
				if strings.Contains(log, "CASAOS_MODULE_QUEUED") || strings.Contains(log, "the old log") || !strings.HasPrefix(log, "CASAOS_MODULE_STEP ") {
					t.Errorf("the log begins with %q", log[:min(len(log), 200)])
				}
				return
			}
			if !strings.HasPrefix(log, c.kept+"CASAOS_MODULE_STEP ") || strings.Contains(log, "the old log") || strings.Contains(log, "\x00") || strings.Count(log, "CASAOS_MODULE_QUEUED") != 1 {
				t.Errorf("the log begins with %q", log[:min(len(log), 300)])
			}
		})
	}
}

// The queued line is at its longest with a module of 32 letters and the kind install: 116 bytes, and the assistant looks far enough to see all of it.
func TestTheLongestQueuedLineIsKept(t *testing.T) {
	name := strings.Repeat("a", 32)
	line := QueuedLine(name, jNonce, KindInstall, at(0))
	if len(line) != 116 {
		t.Fatalf("the longest queued line is %d bytes", len(line))
	}
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte(line+"| the rest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := (&job{name: name, nonce: jNonce}).queuedLength(f, KindInstall); got != len(line) {
		t.Errorf("the length kept is %d, want %d", got, len(line))
	}
}

// The assistant that is run by hand, with no nonce, does not keep what the core wrote for a job that has not started.
func TestARunByHandDoesNotKeepTheQueuedLineOfAJob(t *testing.T) {
	f := newFixture(t)
	f.writeFile("/var/log/casaos/module-virt.log", QueuedLine(testName, jNonce, KindInstall, at(100)))
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if job, _ := ReadJob(f.env, testName, UnitGone, at(101)); job.State != JobIdle || job.Kind != "" {
		t.Errorf("%+v", job)
	}
}
