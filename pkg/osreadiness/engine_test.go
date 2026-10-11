package osreadiness

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// answering is a check that gives the Result it is given.
func answering(code string, result Result) Check {
	return Check{Code: code, Group: GroupProcess, Title: "check " + code, Run: func(context.Context, Probe) Result { return result }}
}

func runWith(t *testing.T, f *fakeProbe, opts Options) Report {
	t.Helper()
	return Run(context.Background(), f, opts)
}

func findingOf(t *testing.T, r Report, code string) Finding {
	t.Helper()
	for _, f := range r.Checks {
		if f.Code == code {
			return f
		}
	}
	t.Fatalf("the report has no %q: %v", code, codesOf(r))
	return Finding{}
}

func codesOf(r Report) []string {
	codes := []string{}
	for _, f := range r.Checks {
		codes = append(codes, f.Code+"="+string(f.Severity))
	}
	return codes
}

func TestTheStateIsTheWorstSeverityAndTheCountsAreExact(t *testing.T) {
	for _, tc := range []struct {
		name   string
		checks []Check
		state  string
		counts Counts
	}{
		{"ok and info are ready", []Check{answering("a", pass()), answering("b", Result{Severity: Info})}, StateReady, Counts{OK: 1, Info: 1}},
		{"a warning is attention", []Check{answering("a", pass()), answering("b", Result{Severity: Warn}), answering("c", Result{Severity: Info})}, StateAttention, Counts{OK: 1, Warn: 1, Info: 1}},
		{"a refusal is blocked", []Check{answering("a", Result{Severity: Warn}), answering("b", Result{Severity: Refuse}), answering("c", Result{Severity: Refuse}), answering("d", pass())}, StateBlocked, Counts{Refuse: 2, Warn: 1, OK: 1}},
		{"no check at all is ready", []Check{}, StateReady, Counts{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := runWith(t, newBox(t), Options{Checks: tc.checks})
			if report.State != tc.state || report.Counts != tc.counts {
				t.Fatalf("state, counts = %q, %+v; want %q, %+v", report.State, report.Counts, tc.state, tc.counts)
			}
			if len(report.Checks) != len(tc.checks) {
				t.Fatalf("%d findings for %d checks", len(report.Checks), len(tc.checks))
			}
		})
	}
}

func TestTheFindingsAreInTheOrderOfWhatStopsTheOwnerFirst(t *testing.T) {
	process := func(code string, s Severity) Check {
		c := answering(code, Result{Severity: s})
		c.Group = GroupProcess
		return c
	}
	sources := func(code string, s Severity) Check {
		c := answering(code, Result{Severity: s})
		c.Group = GroupSources
		return c
	}
	report := runWith(t, newBox(t), Options{Checks: []Check{
		process("ok1", OK), sources("info1", Info), process("warn1", Warn), sources("refuse2", Refuse), process("refuse1", Refuse), sources("ok2", OK), process("info2", Info),
	}})
	got := strings.Join(codesOf(report), " ")
	want := "refuse1=refuse refuse2=refuse warn1=warn info2=info info1=info ok1=ok ok2=ok"
	if got != want {
		t.Fatalf("order = %s\nwant    %s", got, want)
	}
}

func TestAReportHasTheShapeThePageReads(t *testing.T) {
	box := newBox(t)
	report := runWith(t, box, Options{Checks: []Check{
		answering("broken_dpkg", Result{Severity: Refuse, Title: "dpkg has an unfinished operation", Detail: "dpkg --audit says: something", Data: D{"audit": true}, Action: ActionFinishDpkg}),
		answering("held_docker", Result{Severity: Info, Data: D{"packages": []string{"docker-ce"}}}),
		answering("fine", pass()),
	}})
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	// the hop is the identity's: the check says nothing of it
	report.Hop = nil
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["checked_at"] != "2026-10-11T20:15:02Z" || got["state"] != "blocked" {
		t.Fatalf("checked_at, state = %v, %v", got["checked_at"], got["state"])
	}
	wantHop := `"hop":{"from":{"id":"debian","version_id":"11","codename":"bullseye","pretty_name":"Debian GNU/Linux 11 (bullseye)"},"to":{"id":"debian","version_id":"12","codename":"bookworm","pretty_name":"Debian 12 (bookworm)"}}`
	if !strings.Contains(string(data), wantHop) {
		t.Fatalf("hop: %s", data)
	}
	if !strings.Contains(string(data), `"counts":{"refuse":1,"warn":0,"ok":1,"info":1}`) {
		t.Fatalf("counts: %s", data)
	}
	wantFirst := `{"code":"broken_dpkg","severity":"refuse","title":"dpkg has an unfinished operation","detail":"dpkg --audit says: something","data":{"audit":true,"unknown":false},"action":"finish_dpkg"}`
	if !strings.Contains(string(data), wantFirst) {
		t.Fatalf("first finding:\n%s\nwant %s", data, wantFirst)
	}
	// no detail and no action: the keys are left out, and the data is there, with "unknown" false
	if !strings.Contains(string(data), `{"code":"fine","severity":"ok","title":"check fine","data":{"unknown":false}}`) {
		t.Fatalf("ok finding: %s", data)
	}
}

func TestACheckThatCannotSayIsARefusalThatSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		check  Check
		reason string
	}{
		{"it says it does not know", answering("c", unknownWith(ReasonToolMissing)), ReasonToolMissing},
		{"it panics", Check{Code: "c", Title: "c", Run: func(context.Context, Probe) Result { panic("boom") }}, ReasonPanic},
		{"it answers a severity that is none", answering("c", Result{Severity: "fine"}), ReasonInvalid},
		{"it answers nothing", answering("c", Result{}), ReasonInvalid},
		{"it says unknown but not refuse", answering("c", Result{Severity: OK, Data: D{"unknown": true, "reason": ReasonTimeout}}), ReasonTimeout},
		{"it ignores its context", Check{Code: "c", Title: "c", Timeout: 40 * time.Millisecond, Run: func(context.Context, Probe) Result {
			time.Sleep(300 * time.Millisecond)
			return pass()
		}}, ReasonTimeout},
		{"it waits for its context", Check{Code: "c", Title: "c", Timeout: 40 * time.Millisecond, Run: func(ctx context.Context, _ Probe) Result {
			<-ctx.Done()
			return pass() // the answer of a check that was told to stop is not believed either
		}}, ReasonTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := time.Now()
			report := runWith(t, newBox(t), Options{Checks: []Check{tc.check}})
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Fatalf("the report waited %v", elapsed)
			}
			f := findingOf(t, report, "c")
			if f.Severity != Refuse || f.Data["unknown"] != true || f.Data["reason"] != tc.reason {
				t.Fatalf("finding = %+v, want a refusal that does not know (%s)", f, tc.reason)
			}
			if report.State != StateBlocked {
				t.Fatalf("state = %s", report.State)
			}
			if !strings.HasPrefix(f.Title, "Could not check: ") || !strings.HasPrefix(f.Detail, "This could not be checked: ") {
				t.Fatalf("title, detail = %q, %q", f.Title, f.Detail)
			}
		})
	}
}

// ignoringTheBudget is a check that the whole report cannot wait for.
func TestTheReportEndsWithinItsBudgetAndSaysWhatDidNotFinish(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stuck := func(code string) Check {
		return Check{Code: code, Title: code, Timeout: time.Minute, Run: func(context.Context, Probe) Result {
			<-release // does not look at its context at all
			return pass()
		}}
	}
	started := time.Now()
	report := runWith(t, newBox(t), Options{
		Checks: []Check{stuck("a"), stuck("b"), stuck("c"), answering("d", pass())}, Budget: 150 * time.Millisecond, Concurrency: 2,
	})
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the report took %v for a budget of 150ms", elapsed)
	}
	for _, code := range []string{"a", "b", "c", "d"} {
		f := findingOf(t, report, code)
		if f.Severity != Refuse || f.Data["unknown"] != true || f.Data["reason"] != ReasonNotFinished {
			t.Fatalf("%s = %+v, want a refusal that did not finish", code, f)
		}
	}
}

func TestAtMostFourChecksRunAtOnce(t *testing.T) {
	var running, peak atomic.Int32
	var mu sync.Mutex
	wait := func() Result {
		now := running.Add(1)
		mu.Lock()
		if now > peak.Load() {
			peak.Store(now)
		}
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		running.Add(-1)
		return pass()
	}
	var checks []Check
	for _, code := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		checks = append(checks, Check{Code: code, Title: code, Run: func(context.Context, Probe) Result { return wait() }})
	}
	report := runWith(t, newBox(t), Options{Checks: checks})
	if report.State != StateReady || len(report.Checks) != 10 {
		t.Fatalf("report = %v", codesOf(report))
	}
	if got := peak.Load(); got != 4 {
		t.Fatalf("%d checks ran at once, want exactly %d (in parallel, and no more)", got, 4)
	}
}

func TestACheckRunsOnlyOnTheHopsItNames(t *testing.T) {
	var ran sync.Map
	mark := func(code string, hops ...HopID) Check {
		return Check{Code: code, Title: code, Hops: hops, Run: func(context.Context, Probe) Result {
			ran.Store(code, true)
			return pass()
		}}
	}
	runWith(t, newBox(t), Options{Checks: []Check{
		mark("all"), mark("d11", HopDebian11to12), mark("d11_or_d12", HopDebian12to13, HopDebian11to12), mark("d12", HopDebian12to13), mark("u22", HopUbuntu2204to2404),
	}})
	for code, want := range map[string]bool{"all": true, "d11": true, "d11_or_d12": true, "d12": false, "u22": false} {
		if _, got := ran.Load(code); got != want {
			t.Errorf("%s ran = %v, want %v", code, got, want)
		}
	}
}

func TestADataValueIsOnlyShownWhenItIsSafeToShow(t *testing.T) {
	report := runWith(t, newBox(t), Options{Checks: []Check{answering("c", Result{
		Severity: Warn, Title: "line one\nline two\x00 <b>", Detail: strings.Repeat("é", 800), Action: "format_disk",
		Data: D{
			"version": "1:9.2p1-2+deb12u7", "device": "/dev/sda1", "count": 3, "ratio": 0.5, "flag": true,
			"names": []string{"docker-ce", "<script>alert(1)</script>", "ok.name+1"},
			"html":  "<img src=x onerror=alert(1)>", "space": "has a space", "long": strings.Repeat("a", 129),
			"object": map[string]string{"a": "b"}, "Bad Key": "x", "reason": "stale", "nil": nil, "multiline": "a\nb",
		},
	})}})
	f := findingOf(t, report, "c")
	want := D{"version": "1:9.2p1-2+deb12u7", "device": "/dev/sda1", "count": 3, "ratio": 0.5, "flag": true, "names": []string{"docker-ce", "ok.name+1"}, "unknown": false}
	got, _ := json.Marshal(f.Data)
	wantJSON, _ := json.Marshal(want)
	if string(got) != string(wantJSON) {
		t.Fatalf("data = %s\nwant   %s", got, wantJSON)
	}
	if f.Action != "" {
		t.Fatalf("action = %q: a code that is not one of the actions is left out", f.Action)
	}
	if strings.ContainsAny(f.Title, "\n\x00") || len(f.Detail) > maxDetail || len(f.Detail) == 0 {
		t.Fatalf("title %q, detail of %d bytes", f.Title, len(f.Detail))
	}
	if !strings.HasPrefix(f.Detail, "é") || strings.Contains(f.Detail, "�") {
		t.Fatalf("the detail is cut in the middle of a character: %q", f.Detail[len(f.Detail)-4:])
	}
}

func TestEveryActionOfTheSpecIsKept(t *testing.T) {
	for _, action := range []Action{"restart_machine", "finish_dpkg", "wait_for_package_manager", "wait_for_apps", "free_disk_space", "release_holds", "review_sources", "none"} {
		report := runWith(t, newBox(t), Options{Checks: []Check{answering("c", Result{Severity: Info, Action: action})}})
		if got := findingOf(t, report, "c").Action; got != action {
			t.Errorf("action %q became %q", action, got)
		}
	}
}

func TestACheckLateWithItsAnswerIsNotBelieved(t *testing.T) {
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	late := finalize(Check{Code: "c", Title: "c"}, believed(expired, context.Background(), pass()))
	if late.Severity != Refuse || late.Data["reason"] != ReasonTimeout {
		t.Fatalf("an answer after the end of its time = %+v", late)
	}
	live := finalize(Check{Code: "c", Title: "c"}, believed(context.Background(), context.Background(), pass()))
	if live.Severity != OK {
		t.Fatalf("an answer in time = %+v", live)
	}
	ended := finalize(Check{Code: "c", Title: "c"}, believed(expired, expired, pass()))
	if ended.Data["reason"] != ReasonNotFinished {
		t.Fatalf("an answer after the end of the report = %+v", ended)
	}
}

func TestRegisteringACodeTwiceStopsTheProgram(t *testing.T) {
	saved := slices.Clone(registry)
	t.Cleanup(func() { registry = saved })
	row := Check{Code: "x_twice", Title: "t", Run: func(context.Context, Probe) Result { return pass() }}
	register(row)
	defer func() {
		if recover() == nil {
			t.Fatal("a second row for a code was accepted")
		}
	}()
	register(row)
}

func TestRegisteringARowWithoutWhatItNeedsStopsTheProgram(t *testing.T) {
	for name, c := range map[string]Check{
		"no code":  {Title: "t", Run: func(context.Context, Probe) Result { return pass() }},
		"no run":   {Code: "x_no_run", Title: "t"},
		"no title": {Code: "x_no_title", Run: func(context.Context, Probe) Result { return pass() }},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("the row was accepted")
				}
			}()
			register(c)
		})
	}
}
