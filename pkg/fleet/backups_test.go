package fleet

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var assessNow = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// ago is a time before assessNow.
func ago(d time.Duration) time.Time { return assessNow.Add(-d) }

func sched(app, dest string, enabled bool, lastRun, slot time.Time) scheduleRow {
	return scheduleRow{App: app, Destination: dest, Enabled: enabled, LastRun: lastRun, LastSlot: slot}
}

func run(app, dest, errMsg string, started time.Time) runRow {
	return runRow{App: app, Destination: dest, Stamp: started.Format("150405"), StartedAt: started, FinishedAt: started.Add(time.Minute), Error: errMsg}
}

func TestAssessCoupleState(t *testing.T) {
	const a, d = "app", "dest"
	cases := []struct {
		name    string
		in      backupInput
		want    string
		running bool
	}{
		{"ok", backupInput{
			Runs:      []runRow{run(a, d, "", ago(2*time.Hour))},
			Schedules: []scheduleRow{sched(a, d, true, ago(2*time.Hour), ago(2*time.Hour))},
		}, "ok", false},
		{"last run failed", backupInput{
			Runs:      []runRow{run(a, d, "", ago(5*time.Hour)), run(a, d, "disk full", ago(2*time.Hour))},
			Schedules: []scheduleRow{sched(a, d, true, ago(2*time.Hour), ago(2*time.Hour))},
		}, "failed", false},
		{"failed then on-demand success", backupInput{
			Runs:      []runRow{run(a, d, "disk full", ago(3*time.Hour)), run(a, d, "", ago(time.Hour))},
			Schedules: []scheduleRow{sched(a, d, true, ago(time.Hour), ago(3*time.Hour))},
		}, "ok", false},
		{"never run", backupInput{
			Schedules: []scheduleRow{sched(a, d, true, time.Time{}, ago(5*time.Hour))},
		}, "never", false},
		{"restore runs do not count", backupInput{
			Runs:      []runRow{{App: a, Destination: d, StartedAt: ago(time.Hour), Restore: true, Error: "boom"}},
			Schedules: []scheduleRow{sched(a, d, true, time.Time{}, ago(5*time.Hour))},
		}, "never", false},
		{"slot 59 minutes old", backupInput{
			Runs:      []runRow{run(a, d, "", ago(25*time.Hour))},
			Schedules: []scheduleRow{sched(a, d, true, ago(25*time.Hour), ago(59*time.Minute))},
		}, "ok", false},
		{"slot exactly an hour old", backupInput{
			Runs:      []runRow{run(a, d, "", ago(25*time.Hour))},
			Schedules: []scheduleRow{sched(a, d, true, ago(25*time.Hour), ago(time.Hour))},
		}, "ok", false},
		{"slot 61 minutes old", backupInput{
			Runs:      []runRow{run(a, d, "", ago(25*time.Hour))},
			Schedules: []scheduleRow{sched(a, d, true, ago(25*time.Hour), ago(61*time.Minute))},
		}, "late", false},
		{"slot passed but forced run after it", backupInput{
			Runs:      []runRow{run(a, d, "", ago(25*time.Hour)), run(a, d, "", ago(90*time.Minute))},
			Schedules: []scheduleRow{sched(a, d, true, ago(25*time.Hour), ago(2*time.Hour))},
		}, "ok", false},
		{"schedule says it ran after the slot but the only run is older", backupInput{
			Runs:      []runRow{run(a, d, "", ago(25*time.Hour))},
			Schedules: []scheduleRow{sched(a, d, true, ago(30*time.Minute), ago(2*time.Hour))},
		}, "ok", false},
		{"run started exactly at the slot", backupInput{
			Runs:      []runRow{run(a, d, "", ago(2*time.Hour))},
			Schedules: []scheduleRow{sched(a, d, true, ago(25*time.Hour), ago(2*time.Hour))},
		}, "ok", false},
		{"slot passed and a run in progress", backupInput{
			Runs:       []runRow{run(a, d, "", ago(25*time.Hour))},
			Schedules:  []scheduleRow{sched(a, d, true, ago(25*time.Hour), ago(2*time.Hour))},
			Operations: []opRow{{App: a, Kind: "backup"}},
		}, "ok", true},
		{"operation of another kind does not mask late", backupInput{
			Runs:       []runRow{run(a, d, "", ago(25*time.Hour))},
			Schedules:  []scheduleRow{sched(a, d, true, ago(25*time.Hour), ago(2*time.Hour))},
			Operations: []opRow{{App: a, Kind: "restore"}, {App: "other", Kind: "backup"}},
		}, "late", false},
		{"upgrade open suspends late", backupInput{
			Runs:          []runRow{run(a, d, "", ago(25*time.Hour))},
			Schedules:     []scheduleRow{sched(a, d, true, ago(25*time.Hour), ago(2*time.Hour))},
			OSUpgradeOpen: true,
		}, "suspended", false},
		{"upgrade open keeps failed", backupInput{
			Runs:          []runRow{run(a, d, "disk full", ago(3*time.Hour))},
			Schedules:     []scheduleRow{sched(a, d, true, ago(3*time.Hour), ago(2*time.Hour))},
			OSUpgradeOpen: true,
		}, "failed", false},
		{"upgrade open keeps never", backupInput{
			Schedules:     []scheduleRow{sched(a, d, true, time.Time{}, ago(5*time.Hour))},
			OSUpgradeOpen: true,
		}, "never", false},
		{"failed wins over late", backupInput{
			Runs:      []runRow{run(a, d, "disk full", ago(25*time.Hour))},
			Schedules: []scheduleRow{sched(a, d, true, ago(25*time.Hour), ago(5*time.Hour))},
		}, "failed", false},
		{"slot unknown", backupInput{
			Runs:      []runRow{run(a, d, "", ago(2*time.Hour))},
			Schedules: []scheduleRow{sched(a, d, true, ago(2*time.Hour), time.Time{})},
		}, "unknown", false},
		{"slot unknown but failed", backupInput{
			Runs:      []runRow{run(a, d, "disk full", ago(2*time.Hour))},
			Schedules: []scheduleRow{sched(a, d, true, ago(2*time.Hour), time.Time{})},
		}, "failed", false},
		{"slot unknown but never", backupInput{
			Schedules: []scheduleRow{sched(a, d, true, time.Time{}, time.Time{})},
		}, "never", false},
		{"runs of another destination are not mine", backupInput{
			Runs:      []runRow{run(a, "other", "", ago(time.Hour))},
			Schedules: []scheduleRow{sched(a, d, true, time.Time{}, ago(5*time.Hour))},
		}, "never", false},
	}
	// The current slot in its grace hour: the slot before it, one interval
	// (NextSlot - LastSlot) earlier, decides; a daylight-saving hour either way is tolerated.
	within := func(lastRun time.Time, interval time.Duration, runs ...runRow) backupInput {
		s := sched(a, d, true, lastRun, ago(30*time.Minute))
		s.NextSlot = s.LastSlot.Add(interval)
		return backupInput{Runs: runs, Schedules: []scheduleRow{s}}
	}
	upgrading := within(ago(48*time.Hour+30*time.Minute), 24*time.Hour, run(a, d, "", ago(48*time.Hour+30*time.Minute)))
	upgrading.OSUpgradeOpen = true
	cases = append(cases, []struct {
		name    string
		in      backupInput
		want    string
		running bool
	}{
		{"scheduler stopped, current slot in its grace hour", within(ago(48*time.Hour+30*time.Minute), 24*time.Hour, run(a, d, "", ago(48*time.Hour+30*time.Minute))), "late", false},
		{"healthy, current slot in its grace hour", within(ago(24*time.Hour+30*time.Minute), 24*time.Hour, run(a, d, "", ago(24*time.Hour+30*time.Minute))), "ok", false},
		{"healthy across a 23 h day", within(ago(24*time.Hour+30*time.Minute), 23*time.Hour, run(a, d, "", ago(24*time.Hour+30*time.Minute))), "ok", false},
		{"healthy across a 25 h day", within(ago(24*time.Hour+30*time.Minute), 25*time.Hour, run(a, d, "", ago(24*time.Hour+30*time.Minute))), "ok", false},
		{"scheduler stopped across a 23 h day", within(ago(48*time.Hour+30*time.Minute), 23*time.Hour, run(a, d, "", ago(48*time.Hour+30*time.Minute))), "late", false},
		{"scheduler stopped in the grace hour, upgrade open", upgrading, "suspended", false},
		{"scheduler stopped in the grace hour, backup running", backupInput{
			Runs:       upgrading.Runs,
			Schedules:  upgrading.Schedules,
			Operations: []opRow{{App: a, Kind: "backup"}},
		}, "ok", true},
		// AM keeps the last 200 runs: a couple whose records rolled off is not "never"
		{"no run record but the schedule ran, slot covered", backupInput{
			Schedules: []scheduleRow{sched(a, d, true, ago(2*time.Hour), ago(2*time.Hour))},
		}, "ok", false},
		{"no run record but the schedule ran, slot missed", backupInput{
			Schedules: []scheduleRow{sched(a, d, true, ago(26*time.Hour), ago(2*time.Hour))},
		}, "late", false},
		{"no run record but the schedule ran, slot unknown", backupInput{
			Schedules: []scheduleRow{sched(a, d, true, ago(2*time.Hour), time.Time{})},
		}, "unknown", false},
	}...)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			couples, _, _, _ := assess(c.in, assessNow)
			if len(couples) != 1 {
				t.Fatalf("want one couple, got %+v", couples)
			}
			if couples[0].State != c.want || couples[0].Running != c.running {
				t.Fatalf("state=%q running=%v, want %q %v", couples[0].State, couples[0].Running, c.want, c.running)
			}
		})
	}
}

func TestAssessCoupleFields(t *testing.T) {
	ok1 := run("a", "d", "", ago(30*time.Hour))
	bad := run("a", "d", "disk full", ago(2*time.Hour))
	s := sched("a", "d", true, ago(2*time.Hour), ago(2*time.Hour))
	s.NextSlot = assessNow.Add(22 * time.Hour)
	couples, _, _, _ := assess(backupInput{Runs: []runRow{bad, ok1}, Schedules: []scheduleRow{s}}, assessNow)
	if len(couples) != 1 {
		t.Fatalf("couples: %+v", couples)
	}
	c := couples[0]
	if !c.LastRunAt.Equal(bad.StartedAt) || !c.LastOKAt.Equal(ok1.FinishedAt) || c.LastOKStamp != ok1.Stamp ||
		c.LastError != "disk full" || !c.NextSlot.Equal(s.NextSlot) {
		t.Fatalf("fields: %+v", c)
	}
}

func TestAssessDisabledAndUnscheduled(t *testing.T) {
	in := backupInput{
		Runs:      []runRow{run("a", "d", "", ago(time.Hour)), run("b", "d", "", ago(time.Hour))},
		Schedules: []scheduleRow{sched("a", "d", false, ago(time.Hour), ago(time.Hour))},
	}
	couples, state, green, problems := assess(in, assessNow)
	if len(couples) != 0 || state != "none" || green || problems != 0 {
		t.Fatalf("got %+v %q %v %d", couples, state, green, problems)
	}
}

func TestAssessBoxState(t *testing.T) {
	in := backupInput{
		Runs: []runRow{
			run("b-late", "d", "", ago(25*time.Hour)),
			run("a-ok", "z", "", ago(time.Hour)),
			run("a-ok", "d", "", ago(time.Hour)),
			run("c-failed", "d", "x", ago(time.Hour)),
			run("e-unknown", "d", "", ago(time.Hour)),
		},
		Schedules: []scheduleRow{
			sched("b-late", "d", true, ago(25*time.Hour), ago(3*time.Hour)),
			sched("a-ok", "z", true, ago(time.Hour), ago(time.Hour)),
			sched("a-ok", "d", true, ago(time.Hour), ago(time.Hour)),
			sched("c-failed", "d", true, ago(time.Hour), ago(time.Hour)),
			sched("d-never", "d", true, time.Time{}, ago(time.Hour)),
			sched("e-unknown", "d", true, ago(time.Hour), time.Time{}),
		},
	}
	couples, state, green, problems := assess(in, assessNow)
	var order []string
	for _, c := range couples {
		order = append(order, c.App+"/"+c.Destination)
	}
	want := []string{"a-ok/d", "a-ok/z", "b-late/d", "c-failed/d", "d-never/d", "e-unknown/d"}
	if len(order) != len(want) {
		t.Fatalf("order %v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order %v, want %v", order, want)
		}
	}
	if state != "failed" || green || problems != 2 {
		t.Fatalf("state=%q green=%v problems=%d", state, green, problems)
	}

	// Take the worst couples away one by one: late, then never, then unknown.
	drop := func(apps ...string) backupInput {
		out := in
		out.Schedules = nil
		for _, s := range in.Schedules {
			keep := true
			for _, a := range apps {
				keep = keep && s.App != a
			}
			if keep {
				out.Schedules = append(out.Schedules, s)
			}
		}
		return out
	}
	for _, tc := range []struct {
		without []string
		state   string
	}{
		{[]string{"c-failed"}, "late"},
		{[]string{"c-failed", "b-late"}, "never"},
		{[]string{"c-failed", "b-late", "d-never"}, "unknown"},
		{[]string{"c-failed", "b-late", "d-never", "e-unknown"}, "ok"},
	} {
		if _, st, _, _ := assess(drop(tc.without...), assessNow); st != tc.state {
			t.Fatalf("without %v: state %q, want %q", tc.without, st, tc.state)
		}
	}
}

func TestAssessBoxSuspendedBelowUnknown(t *testing.T) {
	base := backupInput{
		Runs: []runRow{run("u", "d", "", ago(time.Hour)), run("s", "d", "", ago(25*time.Hour)), run("o", "d", "", ago(time.Hour))},
		Schedules: []scheduleRow{
			sched("u", "d", true, ago(time.Hour), time.Time{}),
			sched("s", "d", true, ago(25*time.Hour), ago(3*time.Hour)),
			sched("o", "d", true, ago(time.Hour), ago(time.Hour)),
		},
		OSUpgradeOpen: true,
	}
	if _, st, _, _ := assess(base, assessNow); st != "unknown" {
		t.Fatalf("got %q, want unknown", st)
	}
	base.Schedules = base.Schedules[1:]
	if _, st, g, _ := assess(base, assessNow); st != "suspended" || g {
		t.Fatalf("got %q green=%v, want suspended and not green", st, g)
	}
}

func TestAssessGreen(t *testing.T) {
	okIn := func() backupInput {
		return backupInput{
			Runs:      []runRow{run("a", "d", "", ago(time.Hour))},
			Schedules: []scheduleRow{sched("a", "d", true, ago(time.Hour), ago(time.Hour))},
			Restic:    map[string]resticRow{"d": {Known: true}},
		}
	}
	if _, st, g, p := assess(okIn(), assessNow); st != "ok" || !g || p != 0 {
		t.Fatalf("baseline: %q %v %d", st, g, p)
	}

	in := okIn()
	in.Verifications = []verifyRow{{App: "a", Destination: "d", Status: "failed", Error: "bad", FinishedAt: ago(time.Hour)}}
	couples, st, g, p := assess(in, assessNow)
	if st != "ok" || g || p != 1 || couples[0].Verify.State != "failed" || couples[0].Verify.Error != "bad" {
		t.Fatalf("verification failed: %q %v %d %+v", st, g, p, couples[0].Verify)
	}

	in = okIn()
	in.Verifications = []verifyRow{{App: "a", Destination: "d", Status: "not_verifiable", FinishedAt: ago(time.Hour)}}
	couples, _, g, p = assess(in, assessNow)
	if !g || p != 0 || couples[0].Verify.State != "not_verifiable" {
		t.Fatalf("not_verifiable: green=%v %d %+v", g, p, couples[0].Verify)
	}

	in = okIn()
	in.Restic = map[string]resticRow{"d": {Known: true, Kept: true, Check: &ResticCheck{At: ago(time.Hour), OK: false, Error: "pack missing"}}}
	couples, _, g, p = assess(in, assessNow)
	c := couples[0]
	if g || p != 1 || !c.Restic || c.ResticCheck == nil || c.Verify.State != "failed" || c.Verify.Error != "pack missing" {
		t.Fatalf("restic check failed: green=%v %d %+v", g, p, c)
	}

	in = okIn()
	in.Restic = map[string]resticRow{"d": {Known: true, Kept: true, Check: &ResticCheck{At: ago(time.Hour), OK: true}}}
	couples, _, g, _ = assess(in, assessNow)
	if !g || couples[0].Verify.State != "passed" || !couples[0].Verify.At.Equal(ago(time.Hour)) {
		t.Fatalf("restic check ok: green=%v %+v", g, couples[0].Verify)
	}
}

func TestAssessVerify(t *testing.T) {
	v := func(status string, at time.Duration) verifyRow {
		return verifyRow{App: "a", Destination: "d", Status: status, FinishedAt: ago(at)}
	}
	known := map[string]resticRow{"d": {Known: true}}
	cases := []struct {
		name   string
		rows   []verifyRow
		restic map[string]resticRow
		want   string
	}{
		{"none", nil, known, "none"},
		{"latest wins, failed then passed", []verifyRow{v("failed", 5*time.Hour), v("passed", time.Hour)}, known, "passed"},
		{"latest wins, passed then failed", []verifyRow{v("passed", 5*time.Hour), v("failed", time.Hour)}, known, "failed"},
		{"cancelled ignored", []verifyRow{v("failed", 5*time.Hour), v("cancelled", time.Hour)}, known, "failed"},
		{"interrupted ignored", []verifyRow{v("passed", 5*time.Hour), v("interrupted", time.Hour)}, known, "passed"},
		{"only cancelled", []verifyRow{v("cancelled", time.Hour)}, known, "none"},
		{"running", []verifyRow{v("passed", 5*time.Hour), {App: "a", Destination: "d", Status: "running"}}, known, "running"},
		{"running listed before a later passed", []verifyRow{{App: "a", Destination: "d", Status: "running"}, v("passed", time.Hour)}, known, "running"},
		{"status outside the wire values", []verifyRow{v("exploded", time.Hour)}, known, "unknown"},
		{"unknown status then ignored status", []verifyRow{v("exploded", 5*time.Hour), v("cancelled", time.Hour)}, known, "unknown"},
		{"other couple ignored", []verifyRow{{App: "x", Destination: "d", Status: "failed", FinishedAt: ago(time.Hour)}}, known, "none"},
		{"restic unknown, no row", nil, nil, "unknown"},
		{"restic not known, no row", nil, map[string]resticRow{"d": {}}, "unknown"},
		{"restic unknown but a row", []verifyRow{v("passed", time.Hour)}, nil, "passed"},
		{"restic kept without check", nil, map[string]resticRow{"d": {Known: true, Kept: true}}, "none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := backupInput{
				Runs:          []runRow{run("a", "d", "", ago(time.Hour))},
				Schedules:     []scheduleRow{sched("a", "d", true, ago(time.Hour), ago(time.Hour))},
				Verifications: c.rows,
				Restic:        c.restic,
			}
			couples, _, _, _ := assess(in, assessNow)
			if len(couples) != 1 {
				t.Fatalf("couples: %+v", couples)
			}
			if got := couples[0].Verify.State; got != c.want {
				t.Fatalf("verify %q, want %q", got, c.want)
			}
		})
	}
}

func TestAssessProblemsCountACoupleOnce(t *testing.T) {
	failedVerify := func(app string) verifyRow {
		return verifyRow{App: app, Destination: "d", Status: "failed", FinishedAt: ago(time.Hour)}
	}
	in := backupInput{
		Runs: []runRow{
			run("a-failed", "d", "x", ago(time.Hour)),
			run("b-late", "d", "", ago(25*time.Hour)),
		},
		Schedules: []scheduleRow{
			sched("a-failed", "d", true, ago(time.Hour), ago(time.Hour)),
			sched("b-late", "d", true, ago(25*time.Hour), ago(3*time.Hour)),
		},
		Restic: map[string]resticRow{"d": {Known: true}},
	}
	// Two couples in different problem states: 2.
	if _, _, _, p := assess(in, assessNow); p != 2 {
		t.Fatalf("problems=%d, want 2", p)
	}
	// Failed and failed verification: still one problem for that couple.
	in.Verifications = []verifyRow{failedVerify("a-failed")}
	if _, _, _, p := assess(in, assessNow); p != 2 {
		t.Fatalf("failed + failed verify: problems=%d, want 2", p)
	}
	// Late and failed verification: still one for that couple.
	in.Verifications = []verifyRow{failedVerify("b-late")}
	if _, _, _, p := assess(in, assessNow); p != 2 {
		t.Fatalf("late + failed verify: problems=%d, want 2", p)
	}
	// One couple alone, failed and failed verification: 1.
	one := backupInput{
		Runs:          []runRow{run("a", "d", "x", ago(time.Hour))},
		Schedules:     []scheduleRow{sched("a", "d", true, ago(time.Hour), ago(time.Hour))},
		Verifications: []verifyRow{failedVerify("a")},
		Restic:        map[string]resticRow{"d": {Known: true}},
	}
	if _, _, g, p := assess(one, assessNow); p != 1 || g {
		t.Fatalf("failed + failed verify: problems=%d green=%v, want 1 false", p, g)
	}
	// One couple alone, late and failed verification: 1.
	one.Runs = []runRow{run("a", "d", "", ago(25*time.Hour))}
	one.Schedules = []scheduleRow{sched("a", "d", true, ago(25*time.Hour), ago(3*time.Hour))}
	if _, _, _, p := assess(one, assessNow); p != 1 {
		t.Fatalf("late + failed verify: problems=%d, want 1", p)
	}
}

func TestAssessCouplesNeverNil(t *testing.T) {
	couples, _, _, _ := assess(backupInput{}, assessNow)
	if couples == nil {
		t.Fatal("couples is nil, JSON would be null")
	}
	b, err := json.Marshal(BoxBackups{Couples: couples})
	if err != nil || !strings.Contains(string(b), `"couples":[]`) {
		t.Fatalf("json %s err %v", b, err)
	}
}

func TestAssessResticCheckOnlyForKept(t *testing.T) {
	in := backupInput{
		Runs:      []runRow{run("a", "d", "", ago(time.Hour))},
		Schedules: []scheduleRow{sched("a", "d", true, ago(time.Hour), ago(time.Hour))},
		Restic:    map[string]resticRow{"d": {Known: true, Check: &ResticCheck{OK: true}}},
	}
	couples, _, _, _ := assess(in, assessNow)
	if couples[0].Restic || couples[0].ResticCheck != nil {
		t.Fatalf("restic fields on a destination that keeps nothing: %+v", couples[0])
	}
}

// One repository check that failed is one problem of its destination, not one
// per app kept there (a never-run couple there included).
func TestAssessAFailedResticCheckIsOneProblemPerDestination(t *testing.T) {
	in := backupInput{
		Runs: []runRow{run("a", "d", "", ago(time.Hour)), run("b", "d", "", ago(time.Hour))},
		Schedules: []scheduleRow{
			sched("a", "d", true, ago(time.Hour), ago(time.Hour)),
			sched("b", "d", true, ago(time.Hour), ago(time.Hour)),
			sched("c", "d", true, time.Time{}, ago(time.Hour)),
		},
		Restic: map[string]resticRow{"d": {Known: true, Kept: true, Check: &ResticCheck{OK: false, Error: "token=abc"}}},
	}
	couples, _, green, problems := assess(in, assessNow)
	if problems != 1 || green {
		t.Fatalf("problems=%d green=%v, want 1 false", problems, green)
	}
	for _, c := range couples {
		if c.Verify.State != "failed" { // the per-couple state the page shows stays as it was
			t.Fatalf("%+v", c)
		}
	}
}
