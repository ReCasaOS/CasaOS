package modulecmd

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	jNonce = "0123456789abcdef0123456789abcdef"
	jOther = "fedcba9876543210fedcba9876543210"
	jName  = "virt"
)

var jT0 = time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)

func jts(sec int) string { return jT0.Add(time.Duration(sec) * time.Second).Format(time.RFC3339) }

func jQueued(nonce string, sec int) string { return jQueuedAs(nonce, KindInstall, sec) }

func jQueuedAs(nonce, kind string, sec int) string {
	return "CASAOS_MODULE_QUEUED " + nonce + " " + jName + " " + kind + " " + jts(sec)
}

func jStep(nonce, step, phase string, sec int) string {
	return "CASAOS_MODULE_STEP " + nonce + " " + jName + " " + step + " " + phase + " " + jts(sec)
}

func jResult(nonce, kind, step, code, undo string, sec int) string {
	return "CASAOS_MODULE_RESULT " + nonce + " " + jName + " " + kind + " " + step + " " + code + " " + undo + " " + jts(sec)
}

func jSteps(nonce string, start int, steps ...string) []string {
	var out []string
	for i, s := range steps {
		out = append(out, jStep(nonce, s, "begin", start+2*i), jStep(nonce, s, "done", start+2*i+1))
	}
	return out
}

var installSteps = []string{"packages", "download", "verify", "unpack", "units", "manifest", "start", "route"}

func at(sec int) time.Time { return jT0.Add(time.Duration(sec) * time.Second) }

func reduce(nonce string, lines []string, unit UnitSight, now time.Time) Job {
	return ReduceLines(jName, nonce, lines, unit, now, time.Time{})
}

func joined(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestTheJobIsReducedFromTheLogTheNonceAndTheUnit(t *testing.T) {
	okLog := joined([]string{jQueued(jNonce, 0)}, jSteps(jNonce, 1, installSteps...), []string{jStep(jNonce, "done", "done", 30), "installed: virt v0.1.0", jResult(jNonce, "ok", "done", "-", "-", 30)})
	failedLog := joined([]string{jQueued(jNonce, 0)}, jSteps(jNonce, 1, "packages", "download"), []string{
		jStep(jNonce, "verify", "begin", 10), jStep(jNonce, "verify", "failed", 11), "failed at step verify: checksum_mismatch", "kept_packages: libvirt-clients qemu-utils",
		jResult(jNonce, "failed", "verify", "checksum_mismatch", "rolled_back", 12)})

	cases := []struct {
		name  string
		nonce string
		lines []string
		unit  UnitSight
		now   time.Time
		want  Job
	}{
		{"nothing, no unit", jNonce, nil, UnitGone, at(0), Job{State: JobIdle}},
		{"nothing, a unit that runs", jNonce, nil, UnitActive, at(0), Job{State: JobRunning}},
		{"nothing, systemd does not answer", jNonce, nil, UnitUnknown, at(0), Job{State: JobIdle}},
		{"queued, the unit runs", jNonce, []string{jQueued(jNonce, 0)}, UnitActive, at(100), Job{State: JobRunning, StartedAt: jts(0)}},
		{"queued, the unit is not there yet", jNonce, []string{jQueued(jNonce, 0)}, UnitGone, at(5), Job{State: JobRunning, StartedAt: jts(0)}},
		{"queued, the unit never came", jNonce, []string{jQueued(jNonce, 0)}, UnitGone, at(31), Job{State: JobInterrupted, StartedAt: jts(0)}},
		{"queued, the unit never came, exactly 30 s", jNonce, []string{jQueued(jNonce, 0)}, UnitGone, at(30), Job{State: JobInterrupted, StartedAt: jts(0)}},
		{"queued, one second short of 30", jNonce, []string{jQueued(jNonce, 0)}, UnitGone, at(29), Job{State: JobRunning, StartedAt: jts(0)}},
		{"a step is open", jNonce, joined([]string{jQueued(jNonce, 0)}, jSteps(jNonce, 1, "packages"), []string{jStep(jNonce, "download", "begin", 5)}), UnitActive, at(6), Job{State: JobRunning, Step: "download", StartedAt: jts(0)}},
		{"between two steps", jNonce, joined([]string{jQueued(jNonce, 0)}, jSteps(jNonce, 1, "packages")), UnitActive, at(6), Job{State: JobRunning, Step: "packages", StartedAt: jts(0)}},
		{"the unit has written its first line only", jNonce, []string{jStep(jNonce, "packages", "begin", 1)}, UnitActive, at(2), Job{State: JobRunning, Step: "packages", StartedAt: jts(1)}},
		{"the whole install", jNonce, okLog, UnitActive, at(31), Job{State: JobSucceeded, Step: "done", StartedAt: jts(0), EndedAt: jts(30)}},
		{"the whole install, the unit gone", jNonce, okLog, UnitGone, at(31), Job{State: JobSucceeded, Step: "done", StartedAt: jts(0), EndedAt: jts(30)}},
		{"the whole install, years later", jNonce, okLog, UnitGone, at(3600 * 24 * 800), Job{State: JobSucceeded, Step: "done", StartedAt: jts(0), EndedAt: jts(30)}},
		{"the whole install, systemd silent", jNonce, okLog, UnitUnknown, at(5000), Job{State: JobSucceeded, Step: "done", StartedAt: jts(0), EndedAt: jts(30)}},
		{"failed and rolled back", jNonce, failedLog, UnitGone, at(40), Job{State: JobFailed, Step: "verify", Code: "checksum_mismatch", Undo: UndoRolledBack, KeptPackages: []string{"libvirt-clients", "qemu-utils"}, StartedAt: jts(0), EndedAt: jts(12)}},
		{"failed after a rollback that went through its steps", jNonce, joined([]string{jQueued(jNonce, 0), jStep(jNonce, "start", "begin", 3), jStep(jNonce, "start", "failed", 4)}, jSteps(jNonce, 5, "rollback_stop", "rollback_files"), []string{"kept_packages: none", jResult(jNonce, "failed", "start", "start_failed", "rolled_back", 12)}), UnitGone, at(40), Job{State: JobFailed, Step: "start", Code: "start_failed", Undo: UndoRolledBack, KeptPackages: []string{}, StartedAt: jts(0), EndedAt: jts(12)}},
		{"failed, rollback incomplete, nothing kept", jNonce, []string{jQueued(jNonce, 0), jStep(jNonce, "start", "begin", 3), jStep(jNonce, "start", "failed", 4), "kept_packages: none", jResult(jNonce, "failed", "start", "start_failed", "rollback_incomplete", 9)}, UnitGone, at(40), Job{State: JobFailed, Step: "start", Code: "start_failed", Undo: UndoIncomplete, KeptPackages: []string{}, StartedAt: jts(0), EndedAt: jts(9)}},
		{"failed, what is kept is not known", jNonce, []string{jStep(jNonce, "packages", "begin", 3), jStep(jNonce, "packages", "failed", 4), "kept_packages: unknown", jResult(jNonce, "failed", "packages", "packages_failed", "-", 5)}, UnitGone, at(40), Job{State: JobFailed, Step: "packages", Code: "packages_failed", StartedAt: jts(3), EndedAt: jts(5)}},
		{"failed without a step (a panic before the steps)", jNonce, []string{jQueued(jNonce, 0), jResult(jNonce, "failed", "-", "internal_error", "rolled_back", 2)}, UnitGone, at(40), Job{State: JobFailed, Code: "internal_error", Undo: UndoRolledBack, StartedAt: jts(0), EndedAt: jts(2)}},
		{"refused", jNonce, []string{jQueued(jNonce, 0), "refused: plan_changed", jResult(jNonce, "refused", "-", "plan_changed", "-", 2)}, UnitGone, at(40), Job{State: JobRefused, Code: "plan_changed", StartedAt: jts(0), EndedAt: jts(2)}},
		{"a step failed and the rollback goes on", jNonce, []string{jQueued(jNonce, 0), jStep(jNonce, "start", "begin", 3), jStep(jNonce, "start", "failed", 4), jStep(jNonce, "rollback_stop", "begin", 5)}, UnitActive, at(6), Job{State: JobRunning, Step: "rollback_stop", StartedAt: jts(0)}},
		{"a step failed and the unit is gone: no result, no verdict", jNonce, []string{jQueued(jNonce, 0), jStep(jNonce, "start", "begin", 3), jStep(jNonce, "start", "failed", 4), jStep(jNonce, "rollback_stop", "begin", 5)}, UnitGone, at(60), Job{State: JobInterrupted, Step: "rollback_stop", StartedAt: jts(0)}},
		{"the unit vanished in the middle of apt", jNonce, []string{jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1), "| Unpacking x"}, UnitGone, at(90), Job{State: JobInterrupted, Step: "packages", StartedAt: jts(0)}},
		{"the unit vanished a moment ago", jNonce, []string{jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1)}, UnitGone, at(10), Job{State: JobRunning, Step: "packages", StartedAt: jts(0)}},
		{"systemd silent, the log is fresh", jNonce, []string{jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1)}, UnitUnknown, at(20 * 60), Job{State: JobRunning, Step: "packages", StartedAt: jts(0)}},
		{"systemd silent, the log is old", jNonce, []string{jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1)}, UnitUnknown, at(31 * 60), Job{State: JobInterrupted, Step: "packages", StartedAt: jts(0)}},
		{"a clock put back: the last line is in the future", jNonce, []string{jQueued(jNonce, 7200)}, UnitGone, at(0), Job{State: JobInterrupted, StartedAt: jts(7200)}},
		{"a clock put back a little", jNonce, []string{jQueued(jNonce, 10)}, UnitGone, at(0), Job{State: JobRunning, StartedAt: jts(10)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reduce(c.nonce, c.lines, c.unit, c.now)
			got.LogTail = nil
			got.Kind = "" // (what the kind is has its own tests)
			c.want.Name = jName
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestTheLastWordIsTheFirstResult(t *testing.T) {
	base := []string{jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1)}
	for name, tc := range map[string]struct {
		lines []string
		want  string
	}{
		"ok then failed":     {append(slicesClone(base), jResult(jNonce, "ok", "done", "-", "-", 5), jResult(jNonce, "failed", "route", "route_timeout", "rolled_back", 6)), JobSucceeded},
		"failed then ok":     {append(slicesClone(base), jResult(jNonce, "failed", "packages", "packages_failed", "-", 5), jResult(jNonce, "ok", "done", "-", "-", 6)), JobFailed},
		"markers after ok":   {append(slicesClone(base), jResult(jNonce, "ok", "done", "-", "-", 5), jStep(jNonce, "packages", "begin", 6), jQueued(jNonce, 7)), JobSucceeded},
		"sentences after ok": {append(slicesClone(base), jResult(jNonce, "ok", "done", "-", "-", 5), "left: /DATA/VM", "| apt said hello"), JobSucceeded},
	} {
		t.Run(name, func(t *testing.T) {
			if got := reduce(jNonce, tc.lines, UnitGone, at(1000)); got.State != tc.want {
				t.Errorf("state = %s, want %s", got.State, tc.want)
			}
		})
	}
}

func slicesClone(in []string) []string { return append([]string(nil), in...) }

// What the log holds that is not the job's word: every one of these lines, alone or with a job that is going on, leaves the job where it was and never makes it a success.
func TestWhatIsNotTheJobsWordNeverEndsTheJob(t *testing.T) {
	running := []string{jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1)}
	forged := map[string]string{
		"another nonce":                      jResult(jOther, "ok", "done", "-", "-", 5),
		"no nonce":                           jResult("-", "ok", "done", "-", "-", 5),
		"an empty nonce":                     strings.Replace(jResult(jNonce, "ok", "done", "-", "-", 5), jNonce, "", 1),
		"a nonce in capitals":                jResult(strings.ToUpper(jNonce), "ok", "done", "-", "-", 5),
		"a short nonce":                      jResult(jNonce[:31], "ok", "done", "-", "-", 5),
		"a long nonce":                       jResult(jNonce+"0", "ok", "done", "-", "-", 5),
		"in the output of a tool":            "| " + jResult(jNonce, "ok", "done", "-", "-", 5),
		"in the output of a tool, no blank":  "|" + jResult(jNonce, "ok", "done", "-", "-", 5),
		"behind a blank":                     " " + jResult(jNonce, "ok", "done", "-", "-", 5),
		"behind text":                        "text " + jResult(jNonce, "ok", "done", "-", "-", 5),
		"the name of another module":         strings.Replace(jResult(jNonce, "ok", "done", "-", "-", 5), " virt ", " other ", 1),
		"an ok that has a code":              jResult(jNonce, "ok", "done", "route_timeout", "-", 5),
		"an ok that has another step":        jResult(jNonce, "ok", "route", "-", "-", 5),
		"an ok that has an undo":             jResult(jNonce, "ok", "done", "-", "rolled_back", 5),
		"a word that is not a kind":          jResult(jNonce, "success", "done", "-", "-", 5),
		"a kind in capitals":                 jResult(jNonce, "OK", "done", "-", "-", 5),
		"a step in capitals":                 jResult(jNonce, "ok", "DONE", "-", "-", 5),
		"an undo that is not one":            jResult(jNonce, "failed", "start", "start_failed", "undone", 5),
		"a refusal that has a step":          jResult(jNonce, "refused", "start", "busy", "-", 5),
		"a refusal that has no code":         jResult(jNonce, "refused", "-", "-", "-", 5),
		"a failure that has no code":         jResult(jNonce, "failed", "start", "-", "rolled_back", 5),
		"a code with a capital":              jResult(jNonce, "failed", "start", "Start_failed", "-", 5),
		"a time that is not one":             "CASAOS_MODULE_RESULT " + jNonce + " virt ok done - - yesterday",
		"a time with a long fraction":        "CASAOS_MODULE_RESULT " + jNonce + " virt ok done - - " + strings.TrimSuffix(jts(5), "Z") + "." + strings.Repeat("0", 600) + "Z",
		"a time with a fraction":             "CASAOS_MODULE_RESULT " + jNonce + " virt ok done - - " + strings.TrimSuffix(jts(5), "Z") + ".5Z",
		"a time in another zone":             "CASAOS_MODULE_RESULT " + jNonce + " virt ok done - - " + strings.TrimSuffix(jts(5), "Z") + "+00:00",
		"a time with a small z":              "CASAOS_MODULE_RESULT " + jNonce + " virt ok done - - " + strings.TrimSuffix(jts(5), "Z") + "z",
		"no time":                            "CASAOS_MODULE_RESULT " + jNonce + " virt ok done - -",
		"a field too many":                   jResult(jNonce, "ok", "done", "-", "-", 5) + " x",
		"two blanks":                         strings.Replace(jResult(jNonce, "ok", "done", "-", "-", 5), " ok ", "  ok ", 1),
		"a tab":                              strings.Replace(jResult(jNonce, "ok", "done", "-", "-", 5), " ok ", "\tok ", 1),
		"a carriage return":                  jResult(jNonce, "ok", "done", "-", "-", 5) + "\r",
		"a NUL in a field":                   strings.Replace(jResult(jNonce, "ok", "done", "-", "-", 5), "done", "do\x00ne", 1),
		"another marker's name":              strings.Replace(jResult(jNonce, "ok", "done", "-", "-", 5), "CASAOS_MODULE_RESULT", "CASAOS_MODULE_RESULTS", 1),
		"the marker of the docker update":    "CASAOS_DOCKER_UPDATE_SUCCESS " + jNonce + " " + jts(5),
		"a result with a step marker's form": "CASAOS_MODULE_STEP " + jNonce + " virt ok done - - " + jts(5),
		"a step with a bad phase":            jStep(jNonce, "packages", "finished", 5),
		"a step with a bad name":             jStep(jNonce, "Packages", "done", 5),
		"a step with a dash":                 jStep(jNonce, "pack-ages", "done", 5),
		"a step far too long":                jStep(jNonce, strings.Repeat("a", 33), "done", 5),
		// the queued line is the core's first line, and is read as strictly as the others: it is a job's word only as it is written
		"a queued line with a field too many":           jQueued(jNonce, 5) + " x",
		"a queued line with a kind and a field more":    "CASAOS_MODULE_QUEUED " + jNonce + " virt install x " + jts(5),
		"a queued line with three fields before time":   "CASAOS_MODULE_QUEUED " + jNonce + " virt install x y " + jts(5),
		"a queued line with no kind (the old grammar)":  "CASAOS_MODULE_QUEUED " + jNonce + " virt " + jts(5),
		"a queued line with a kind that is not one":     jQueuedAs(jNonce, "upgrade", 5),
		"a queued line with a kind in capitals":         jQueuedAs(jNonce, "INSTALL", 5),
		"a queued line with a capital in the kind":      jQueuedAs(jNonce, "Remove", 5),
		"a queued line with a kind that goes on":        jQueuedAs(jNonce, "installed", 5),
		"a queued line with a kind that is cut":         jQueuedAs(jNonce, "instal", 5),
		"a queued line with two kinds":                  jQueuedAs(jNonce, "install remove", 5),
		"a queued line with the kind of a step":         jQueuedAs(jNonce, "packages", 5),
		"a queued line with the kind of a result":       jQueuedAs(jNonce, "ok", 5),
		"a queued line with a dash for a kind":          jQueuedAs(jNonce, "-", 5),
		"a queued line with an empty kind":              jQueuedAs(jNonce, "", 5),
		"a queued line with a NUL in the kind":          jQueuedAs(jNonce, "inst\x00all", 5),
		"a queued line with a tab in the kind":          jQueuedAs(jNonce, "install\t", 5),
		"a queued line with a return after the kind":    jQueuedAs(jNonce, "install\r", 5),
		"a queued line with a kind and no time":         "CASAOS_MODULE_QUEUED " + jNonce + " virt install",
		"a queued line with the time before the kind":   "CASAOS_MODULE_QUEUED " + jNonce + " virt " + jts(5) + " install",
		"a queued line with the kind before the name":   "CASAOS_MODULE_QUEUED " + jNonce + " install virt " + jts(5),
		"a queued line with the kind before the nonce":  "CASAOS_MODULE_QUEUED install " + jNonce + " virt " + jts(5),
		"a queued line with the result's fields":        jQueued(jNonce, 5) + " ok done - -",
		"a queued line of another module":               strings.Replace(jQueued(jNonce, 5), " virt ", " other ", 1),
		"a queued line with a nonce in capitals":        jQueued(strings.ToUpper(jNonce), 5),
		"a queued line with a short nonce":              jQueued(jNonce[:31], 5),
		"a queued line with no nonce":                   strings.Replace(jQueued(jNonce, 5), jNonce, "", 1),
		"a queued line with a time in a zone":           "CASAOS_MODULE_QUEUED " + jNonce + " virt install " + strings.TrimSuffix(jts(5), "Z") + "+00:00",
		"a queued line with a fraction of a second":     "CASAOS_MODULE_QUEUED " + jNonce + " virt install " + strings.TrimSuffix(jts(5), "Z") + ".5Z",
		"a queued line with no time":                    "CASAOS_MODULE_QUEUED " + jNonce + " virt",
		"a queued line in the output of a tool":         "| " + jQueued(jNonce, 5),
		"a queued line with a tab":                      strings.Replace(jQueued(jNonce, 5), " virt ", "\tvirt ", 1),
		"a queued line with two blanks":                 strings.Replace(jQueued(jNonce, 5), " virt ", "  virt ", 1),
		"a queued line with two blanks before the kind": strings.Replace(jQueued(jNonce, 5), " install ", "  install ", 1),
		"a queued line with a tab before the kind":      strings.Replace(jQueued(jNonce, 5), " install ", "\tinstall ", 1),
		"a queued line that ends with a return":         jQueued(jNonce, 5) + "\r",
		"a queued line, the name of the marker changed": strings.Replace(jQueued(jNonce, 5), "QUEUED", "QUEUEDX", 1),
		"a queued line with the nonce of a run by hand": jQueued("-", 5),
	}
	for name, line := range forged {
		t.Run(name, func(t *testing.T) {
			for _, unit := range []UnitSight{UnitActive, UnitGone, UnitUnknown} {
				got := reduce(jNonce, append(slicesClone(running), line), unit, at(8))
				if got.State == JobSucceeded || got.State == JobFailed || got.State == JobRefused {
					t.Fatalf("unit %v: the state is %s for %q", unit, got.State, line)
				}
				if got.Step != "packages" || got.State != JobRunning {
					t.Errorf("unit %v: the job moved: %+v", unit, got)
				}
			}
			// alone, it is no job at all
			if got := reduce(jNonce, []string{line}, UnitGone, at(8)); got.State != JobIdle {
				t.Errorf("alone: state = %s, want idle", got.State)
			}
		})
	}
}

func TestWhatTheLogCarriesWithoutTheNonceDoesNotChangeTheJob(t *testing.T) {
	// the lines of a run started by hand ("-") are in the log of a job that the core started: not the job's word
	lines := []string{jQueued(jNonce, 0), jStep("-", "packages", "begin", 1), jStep("-", "packages", "done", 2), jResult("-", "failed", "packages", "packages_failed", "-", 3)}
	if got := reduce(jNonce, lines, UnitActive, at(5)); got.State != JobRunning || got.Step != "" {
		t.Errorf("%+v", got)
	}
	// and a log of the run of a person has no job in it at all
	hand := []string{jStep("-", "packages", "begin", 1), jResult("-", "ok", "done", "-", "-", 3)}
	if got := reduce("", hand, UnitGone, at(5)); got.State != JobIdle {
		t.Errorf("a run by hand is a job: %+v", got)
	}
}

func TestTheNonceOfAJobIsTheFirstMarkerThatCarriesOne(t *testing.T) {
	// the core's own queued line is the first marker of a job it started
	lines := joined([]string{jQueued(jNonce, 0)}, jSteps(jNonce, 1, "packages"), []string{jResult(jOther, "failed", "packages", "packages_failed", "-", 5)})
	if got := reduce("", lines, UnitGone, at(8)); got.State != JobRunning || got.Step != "packages" {
		t.Errorf("a line of another nonce after the first moved the job: %+v", got)
	}
	// after the unit truncated the log, the first marker is the unit's
	if got := reduce("", jSteps(jNonce, 1, "packages"), UnitActive, at(4)); got.State != JobRunning || got.Step != "packages" {
		t.Errorf("%+v", got)
	}
	// a sentence, or a tool's line, or a marker of another module, is not a first marker
	other := strings.Replace(jStep(jOther, "packages", "begin", 1), " virt ", " other ", 1)
	lines = joined([]string{"refused: busy", "| " + jStep(jOther, "packages", "begin", 1), other, "CASAOS_MODULE_QUEUED " + jOther + " other " + jts(2)}, jSteps(jNonce, 3, "packages"))
	if got := reduce("", lines, UnitActive, at(8)); got.Step != "packages" || got.StartedAt != jts(3) {
		t.Errorf("%+v", got)
	}
	// a nonce that is given and that no marker carries describes no job
	if got := reduce(jOther, jSteps(jNonce, 1, "packages"), UnitGone, at(8)); got.State != JobIdle {
		t.Errorf("%+v", got)
	}
}

func TestWhatWasKeptIsTheSentenceBeforeTheResultOfAFailure(t *testing.T) {
	failed := func(sentence string) []string {
		return []string{jQueued(jNonce, 0), jStep(jNonce, "start", "failed", 2), sentence, jResult(jNonce, "failed", "start", "start_failed", "rolled_back", 3)}
	}
	for name, tc := range map[string]struct {
		sentence string
		want     []string
	}{
		"packages":           {"kept_packages: libvirt-clients qemu-utils", []string{"libvirt-clients", "qemu-utils"}},
		"none":               {"kept_packages: none", []string{}},
		"unknown":            {"kept_packages: unknown", nil},
		"not a package":      {"kept_packages: libvirt-clients Bad_Name", nil},
		"a tool says it":     {"| kept_packages: evil", nil},
		"nothing after it":   {"kept_packages:", nil},
		"a flood of names":   {"kept_packages: " + manyNames(41), nil},
		"as many as allowed": {"kept_packages: " + manyNames(40), strings.Split(manyNames(40), " ")},
		"twice the same one": {"kept_packages: aa aa", nil},
	} {
		t.Run(name, func(t *testing.T) {
			got := reduce(jNonce, failed(tc.sentence), UnitGone, at(9))
			if got.State != JobFailed || !reflect.DeepEqual(got.KeptPackages, tc.want) {
				t.Errorf("state %s, kept %#v, want %#v", got.State, got.KeptPackages, tc.want)
			}
		})
	}
	// it says nothing of a job that did not fail
	ok := []string{jQueued(jNonce, 0), "kept_packages: aa", jResult(jNonce, "ok", "done", "-", "-", 3)}
	if got := reduce(jNonce, ok, UnitGone, at(9)); got.KeptPackages != nil {
		t.Errorf("kept = %#v for a job that succeeded", got.KeptPackages)
	}
}

func TestTheTailIsTheLastTwentyLinesSaid(t *testing.T) {
	var lines []string
	lines = append(lines, jQueued(jNonce, 0))
	for i := 0; i < 50; i++ {
		lines = append(lines, "| line "+string(rune('a'+i%26))+strings.Repeat("x", i))
	}
	got := reduce(jNonce, lines, UnitActive, at(5))
	if len(got.LogTail) != TailLines {
		t.Fatalf("%d lines in the tail", len(got.LogTail))
	}
	if got.LogTail[TailLines-1] != strings.TrimSpace(lines[len(lines)-1]) || got.LogTail[0] != lines[len(lines)-TailLines] {
		t.Errorf("not the last lines: first %q last %q", got.LogTail[0], got.LogTail[TailLines-1])
	}
	// shorter logs are whole, and an empty tail is a list
	if short := reduce(jNonce, lines[:4], UnitActive, at(5)); len(short.LogTail) != 3 {
		t.Errorf("%d lines", len(short.LogTail))
	}
	if idle := reduce(jNonce, nil, UnitGone, at(5)); idle.LogTail == nil || len(idle.LogTail) != 0 {
		t.Errorf("tail = %#v, want an empty list", idle.LogTail)
	}
}

// The lines come from tools and from the module: they are text, and are said as the plan says its own (printable ASCII, a URL reduced to its host, cut).
func TestTheTailIsText(t *testing.T) {
	lines := []string{
		jQueued(jNonce, 0),
		"| Get:1 https://user:secret@deb.example.org/debian bookworm/main amd64 x [1 kB]",
		"| \x1b[31mred\x1b[0m é世 \x00 nul",
		"| <script>alert(1)</script>",
		"| " + strings.Repeat("y", 5000),
		"",
		"   ",
	}
	got := reduce(jNonce, lines, UnitActive, at(5))
	text := strings.Join(got.LogTail, "\n")
	if strings.Contains(text, "secret") || strings.Contains(text, "user:") {
		t.Errorf("a credential in the tail: %s", text)
	}
	for i, l := range got.LogTail {
		if len(l) > 220 {
			t.Errorf("line %d is %d bytes", i, len(l))
		}
		for j := 0; j < len(l); j++ {
			if l[j] < 0x20 || l[j] > 0x7e {
				t.Errorf("line %d has the byte %#x", i, l[j])
			}
		}
	}
	if !strings.Contains(text, "<script>") {
		t.Errorf("text is shown as it is, for the page to show as text: %s", text)
	}
	for _, l := range got.LogTail {
		if l == "" {
			t.Error("a blank line in the tail")
		}
	}
}

func TestAnUnfinishedLastLineIsNeverAMarker(t *testing.T) {
	r := NewReducer(jName, jNonce)
	r.Feed(jQueued(jNonce, 0), true)
	r.Feed(jStep(jNonce, "packages", "begin", 1), true)
	r.Feed(jResult(jNonce, "ok", "done", "-", "-", 2), false) // the unit is still writing it
	if got := r.Job(UnitActive, at(3), time.Time{}); got.State != JobRunning {
		t.Errorf("%+v", got)
	}
	r.Feed(jResult(jNonce, "ok", "done", "-", "-", 2), true)
	if got := r.Job(UnitActive, at(3), time.Time{}); got.State != JobSucceeded {
		t.Errorf("%+v", got)
	}
}

func TestTheLogsOwnClockCountsAsASignOfLife(t *testing.T) {
	// the last marker is old, and the log was written a moment ago (apt is talking)
	lines := []string{jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1)}
	got := ReduceLines(jName, jNonce, lines, UnitGone, at(600), at(590))
	if got.State != JobRunning {
		t.Errorf("a unit that was writing ten seconds ago: %+v", got)
	}
	got = ReduceLines(jName, jNonce, lines, UnitGone, at(600), at(560))
	if got.State != JobInterrupted {
		t.Errorf("a unit that wrote forty seconds ago: %+v", got)
	}
	// a modification time in the future is not a sign of life
	got = ReduceLines(jName, jNonce, lines, UnitGone, at(600), at(600+7200))
	if got.State != JobInterrupted {
		t.Errorf("%+v", got)
	}
}

// manyNames are n package names, once each, in a line.
func manyNames(n int) string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("pkg%02d", i)
	}
	return strings.Join(names, " ")
}

func TestWhatIsSaidAfterTheResultChangesNothing(t *testing.T) {
	lines := []string{jQueued(jNonce, 0), "kept_packages: aa", jResult(jNonce, "failed", "start", "start_failed", "rolled_back", 3), "kept_packages: bb", "kept_packages: none", "kept_packages: unknown"}
	if got := reduce(jNonce, lines, UnitGone, at(9)); !reflect.DeepEqual(got.KeptPackages, []string{"aa"}) {
		t.Errorf("kept = %#v", got.KeptPackages)
	}
}

func TestARefusalHasNoStepWhateverCameBefore(t *testing.T) {
	lines := []string{jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1), jResult(jNonce, "refused", "-", "busy", "-", 2)}
	if got := reduce(jNonce, lines, UnitGone, at(9)); got.State != JobRefused || got.Step != "" || got.Code != "busy" {
		t.Errorf("%+v", got)
	}
}

// A line that is longer than what is held of one is cut when it is given, whoever gives it.
func TestAGiantLineIsCutWhenItIsGiven(t *testing.T) {
	r := NewReducer(jName, jNonce)
	r.Feed(jQueued(jNonce, 0), true)
	for i := 0; i < 3*TailLines; i++ {
		r.Feed(strings.Repeat("A", 1<<20), true)
	}
	if len(r.tail) != TailLines {
		t.Fatalf("%d lines held", len(r.tail))
	}
	for _, l := range r.tail {
		if len(l) > maxLine {
			t.Fatalf("a line of %d bytes is held", len(l))
		}
	}
}

// The nonce is what makes a line of the log the job's word, and what a status says goes to whoever may read the status: the tail has the sentences of the assistant and the output of the
// tools, and not one marker line, so that the nonce is in no answer.
func TestTheTailHasNoMarkerAndSoNotTheNonce(t *testing.T) {
	lines := []string{jQueued(jNonce, 0)}
	for i := 0; i < 30; i++ {
		lines = append(lines, jStep(jNonce, "packages", "begin", i), fmt.Sprintf("| tool line %02d", i), jStep(jNonce, "packages", "done", i))
	}
	lines = append(lines, "failed at step packages: packages_failed", "kept_packages: aa", jResult(jNonce, "failed", "packages", "packages_failed", "rolled_back", 40))
	for _, unit := range []UnitSight{UnitActive, UnitGone} {
		got := reduce(jNonce, lines, unit, at(41))
		for _, l := range got.LogTail {
			if strings.Contains(l, "CASAOS_MODULE") || strings.Contains(l, jNonce) {
				t.Errorf("a marker in the tail: %q", l)
			}
		}
		// the markers do not take the place of the lines of the tools: the last twenty of what is left
		if len(got.LogTail) != TailLines || got.LogTail[TailLines-1] != "kept_packages: aa" || got.LogTail[TailLines-2] != "failed at step packages: packages_failed" || got.LogTail[TailLines-3] != "| tool line 29" {
			t.Errorf("the tail is %q", got.LogTail)
		}
		raw, err := json.Marshal(got)
		if err != nil || strings.Contains(string(raw), jNonce) {
			t.Errorf("the nonce is in what is said of the job: %s, %v", raw, err)
		}
	}
	// a line that begins like a marker is not a line of a tool or a sentence, whatever its shape: the lines that are not whole, a marker of another job, one with a nonce that is not one
	odd := []string{jQueued(jNonce, 0), "| tool", jStep(jOther, "packages", "begin", 1), "CASAOS_MODULE_STEP", "CASAOS_MODULE_RESULT " + jNonce + " virt ok", "CASAOS_MODULE_NEW x y"}
	got := reduce(jNonce, odd, UnitActive, at(5))
	if !reflect.DeepEqual(got.LogTail, []string{"| tool"}) {
		t.Errorf("the tail is %q", got.LogTail)
	}
	// and the answer of a start, which is made from the first line of the log
	if queued := reduce(jNonce, []string{jQueued(jNonce, 0)}, UnitActive, at(1)); len(queued.LogTail) != 0 || queued.LogTail == nil {
		t.Errorf("the tail of a job that was queued is %#v", queued.LogTail)
	}
}

func TestTheTailIsTwentyLines(t *testing.T) {
	if TailLines != 20 {
		t.Errorf("TailLines = %d: the page and the doc say 20", TailLines)
	}
}

// The unit truncates the log when it starts and says its first marker when its plan is made, which takes up to a minute: an empty log is a unit that has not said a word yet, and not a box
// that has no job. It reads as the job of a unit does that has said nothing since the log was written.
func TestAnEmptyLogIsAUnitThatHasNotSaidAWordYet(t *testing.T) {
	for name, tc := range map[string]struct {
		unit    UnitSight
		written time.Time
		now     time.Time
		want    string
	}{
		"written a moment ago, no unit seen":               {UnitGone, at(95), at(100), JobRunning},
		"written 29 seconds ago, no unit seen":             {UnitGone, at(71), at(100), JobRunning},
		"written 30 seconds ago, no unit seen":             {UnitGone, at(70), at(100), JobInterrupted},
		"written an hour ago, no unit seen":                {UnitGone, at(-3500), at(100), JobInterrupted},
		"written an hour ago, the unit runs":               {UnitActive, at(-3500), at(100), JobRunning},
		"written a moment ago, systemd silent":             {UnitUnknown, at(95), at(100), JobRunning},
		"written 20 minutes ago, systemd silent":           {UnitUnknown, at(100 - 20*60), at(100), JobRunning},
		"written 31 minutes ago, systemd silent":           {UnitUnknown, at(100 - 31*60), at(100), JobInterrupted},
		"written in the future, a clock put back":          {UnitGone, at(100 + 7200), at(100), JobInterrupted},
		"written a little in the future, a clock put back": {UnitGone, at(110), at(100), JobRunning},
	} {
		t.Run(name, func(t *testing.T) {
			got := ReduceLines(jName, jNonce, nil, tc.unit, tc.now, tc.written)
			if got.State != tc.want || got.Step != "" || got.StartedAt != "" || got.EndedAt != "" || got.LogTail == nil || len(got.LogTail) != 0 {
				t.Errorf("%+v, want %s", got, tc.want)
			}
		})
	}
	// no log at all, and a log that is somebody's and not a job's, are no job
	for name, tc := range map[string]struct {
		lines   []string
		written time.Time
	}{
		"no log":                         {nil, time.Time{}},
		"a log of a tool's lines":        {[]string{"| apt says"}, at(99)},
		"a log of a run by hand":         {[]string{jStep("-", "packages", "begin", 1)}, at(99)},
		"a log with an empty line":       {[]string{""}, at(99)},
		"a log with a line not finished": {[]string{"CASAOS_MODULE_STE"}, at(99)},
	} {
		for _, unit := range []UnitSight{UnitGone, UnitUnknown} {
			t.Run(name, func(t *testing.T) {
				r := NewReducer(jName, jNonce)
				for i, l := range tc.lines {
					r.Feed(l, i < len(tc.lines)-1 || l != "CASAOS_MODULE_STE")
				}
				if got := r.Job(unit, at(100), tc.written); got.State != JobIdle {
					t.Errorf("unit %v: %+v", unit, got)
				}
			})
		}
	}
}
