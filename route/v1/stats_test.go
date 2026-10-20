package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"github.com/ReCasaOS/CasaOS/pkg/hoststats"
	"github.com/labstack/echo/v4"
)

var statsT0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// statsSource scripts the machine: each good Read adds 100 to the total CPU
// time and cpu to the busy time.
type statsSource struct {
	clock *stats.FakeClock
	mu    sync.Mutex
	cpu   float64
	fail  bool
	busy  float64
	total float64
}

func (f *statsSource) set(cpu float64, fail bool) {
	f.mu.Lock()
	f.cpu, f.fail = cpu, fail
	f.mu.Unlock()
}

func (f *statsSource) Read(context.Context) (hoststats.Reading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return hoststats.Reading{}, errors.New("SECRET-READING no /proc")
	}
	f.busy += f.cpu
	f.total += 100
	return hoststats.Reading{
		At:       f.clock.Now(),
		CPU:      hoststats.CPUTimes{Busy: f.busy, Total: f.total},
		Cores:    4,
		MemTotal: 1000,
		MemUsed:  500,
	}, nil
}

type statsRig struct {
	t     *testing.T
	clock *stats.FakeClock
	src   *statsSource
	svc   *hoststats.Service
	dir   string
}

func (r *statsRig) minutes(n int) {
	r.t.Helper()
	for range 12 * n {
		_ = r.svc.Tick(context.Background())
		r.clock.Advance(5 * time.Second)
	}
}

// useStatsFixture sets hoststats.Default to a service fed three minutes of a
// 10 % CPU, restored when the test ends.
func useStatsFixture(t *testing.T) *statsRig {
	t.Helper()
	r := useEmptyStatsFixture(t)
	r.minutes(3)
	return r
}

// useEmptyStatsFixture is a service that has recorded nothing.
func useEmptyStatsFixture(t *testing.T) *statsRig {
	t.Helper()
	dir := t.TempDir()
	clock := stats.NewFakeClock(statsT0)
	src := &statsSource{clock: clock, cpu: 10}
	svc, err := hoststats.New(hoststats.Config{
		Dir:    dir,
		Source: src,
		Clock:  clock,
		Name:   func() string { return "nas" },
		Boot:   func() time.Time { return statsT0.Add(-time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	original := hoststats.Default
	hoststats.Default = svc
	t.Cleanup(func() { hoststats.Default = original; _ = svc.Close() })
	return &statsRig{t: t, clock: clock, src: src, svc: svc, dir: dir}
}

func callStats(t *testing.T, method, id, sub, query, body string) (int, map[string]any) {
	t.Helper()
	path := "/v1/sys/stats"
	if id != "" {
		path += "/" + id
	}
	path += sub
	request := httptest.NewRequest(method, "/x"+query, strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	c := echo.New().NewContext(request, recorder)
	if id != "" {
		c.SetParamNames("id")
		c.SetParamValues(id)
	}
	var handler echo.HandlerFunc
	switch {
	case id == "":
		handler = GetStats
	case sub == "":
		handler = GetStatsSeries
	case method == http.MethodGet:
		handler = GetStatsAlerts
	default:
		handler = PutStatsAlerts
	}
	if err := handler(c); err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, recorder.Body.String())
	}
	if out["success"] != float64(recorder.Code) {
		t.Fatalf("success = %v, status = %d", out["success"], recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "SECRET-READING") {
		t.Fatalf("a reading leaked: %s", recorder.Body.String())
	}
	return recorder.Code, out
}

func dataOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	d, ok := out["data"].(map[string]any)
	if !ok {
		t.Fatalf("no data object: %v", out)
	}
	return d
}

func TestListHasOnlyTheHost(t *testing.T) {
	useStatsFixture(t)

	code, out := callStats(t, http.MethodGet, "", "", "", "")
	d := dataOf(t, out)
	items, _ := d["items"].([]any)
	if code != 200 || len(items) != 1 {
		t.Fatalf("GET = %d %v", code, out)
	}
	it := items[0].(map[string]any)
	for k, want := range map[string]any{
		"id": "host", "name": "nas", "kind": "host", "group": "Machine", "state": "running",
		"started_at": statsT0.Add(-time.Hour).Format(time.RFC3339), "detail": nil, "alerts": float64(0),
	} {
		if got, ok := it[k]; !ok || got != want {
			t.Errorf("%s = %v, want %v", k, got, want)
		}
	}
	if _, ok := it["cpu_percent"].(float64); !ok {
		t.Errorf("cpu_percent = %v, want a number", it["cpu_percent"])
	}
	if n, ok := d["notes"].([]any); !ok || len(n) != 0 {
		t.Errorf("notes = %v, want []", d["notes"])
	}
}

func TestSeriesForOneHour(t *testing.T) {
	r := useStatsFixture(t)
	r.src.set(10, true)
	r.minutes(1)
	r.src.set(10, false)
	r.minutes(2)

	code, out := callStats(t, http.MethodGet, "host", "", "?range=1h", "")
	d := dataOf(t, out)
	if code != 200 || d["range"] != "1h" || d["step_seconds"] != float64(60) || d["id"] != "host" {
		t.Fatalf("GET = %d %v", code, d)
	}
	var cpu map[string]any
	for _, s := range d["series"].([]any) {
		s := s.(map[string]any)
		switch s["chart"] {
		case "restarts", "health", "pids":
			t.Errorf("unexpected series %v", s["chart"])
		}
		if s["chart"] == "cpu" && s["name"] == "total" {
			cpu = s
		}
	}
	if cpu == nil || cpu["unit"] != "percent" {
		t.Fatalf("no cpu/total percent series: %v", d["series"])
	}
	nulls, values := 0, 0
	for _, p := range cpu["points"].([]any) {
		p := p.([]any)
		if len(p) != 4 {
			t.Fatalf("point has %d elements", len(p))
		}
		if p[1] == nil {
			nulls++
		} else {
			values++
		}
	}
	if nulls == 0 || values == 0 {
		t.Errorf("nulls = %d, values = %d, want a gap between readings", nulls, values)
	}
	scale := d["scale"].(map[string]any)
	if scale["cpu"] != "percent_of_one_core" || scale["cpu_allocation"] != float64(4) || scale["memory_limit"] != float64(1000) {
		t.Errorf("scale = %v", scale)
	}
	if n, ok := d["notes"].([]any); !ok || len(n) != 0 {
		t.Errorf("notes = %v", d["notes"])
	}
}

func TestRangeLiveHasFiveSecondSteps(t *testing.T) {
	useStatsFixture(t)
	for _, q := range []string{"", "?range=live"} {
		_, out := callStats(t, http.MethodGet, "host", "", q, "")
		if d := dataOf(t, out); d["step_seconds"] != float64(5) || d["range"] != "live" {
			t.Errorf("%q: %v", q, d)
		}
	}
}

func TestErrors(t *testing.T) {
	useStatsFixture(t)
	for _, tc := range []struct {
		id, sub, query string
		code           int
		message        string
	}{
		{"host", "", "?range=forever", 400, "bad_range"},
		{"host", "", "?range=", 400, "bad_range"},
		{"vm:x", "", "", 404, "unknown_item"},
		{"container:%2e%2e", "", "", 404, "unknown_item"},
		{"vm:x", "/alerts", "", 404, "unknown_item"},
		{"host%20", "/alerts", "", 404, "unknown_item"},
	} {
		code, out := callStats(t, http.MethodGet, tc.id, tc.sub, tc.query, "")
		if code != tc.code || out["message"] != tc.message {
			t.Errorf("%s%s%s = %d %v, want %d %s", tc.id, tc.sub, tc.query, code, out["message"], tc.code, tc.message)
		}
	}
	code, out := callStats(t, http.MethodPut, "vm:x", "/alerts", "", `{"rules":[]}`)
	if code != 404 || out["message"] != "unknown_item" {
		t.Errorf("PUT unknown = %d %v", code, out["message"])
	}

	hoststats.Default = nil // restored by the fixture's cleanup
	for _, c := range [][3]string{
		{http.MethodGet, "", ""}, {http.MethodGet, "host", ""}, {http.MethodGet, "host", "/alerts"}, {http.MethodPut, "host", "/alerts"},
	} {
		code, out := callStats(t, c[0], c[1], c[2], "", `{"rules":[]}`)
		if code != 503 || out["message"] != "stats_unavailable" {
			t.Errorf("%v with no service = %d %v", c, code, out["message"])
		}
	}
}

func rulesOf(t *testing.T) []any {
	t.Helper()
	code, out := callStats(t, http.MethodGet, "host", "/alerts", "", "")
	if code != 200 {
		t.Fatalf("GET alerts = %d %v", code, out)
	}
	return dataOf(t, out)["rules"].([]any)
}

func TestAlertRulesRoundTrip(t *testing.T) {
	useStatsFixture(t)

	_, out := callStats(t, http.MethodGet, "host", "/alerts", "", "")
	d := dataOf(t, out)
	rules := d["rules"].([]any)
	r0, r1 := rules[0].(map[string]any), rules[1].(map[string]any)
	if len(rules) != 2 || r0["key"] != "cpu_high" || r0["threshold"] != float64(90) || r0["minutes"] != float64(10) ||
		r1["key"] != "mem_high" || r1["threshold"] != float64(90) || r1["minutes"] != float64(5) || r1["enabled"] != true || r0["enabled"] != true {
		t.Fatalf("default rules = %v", rules)
	}
	if a, ok := d["active"].([]any); !ok || len(a) != 0 {
		t.Fatalf("active = %v, want []", d["active"])
	}

	put := `{"rules":[{"key":"cpu_high","enabled":true,"threshold":80,"minutes":5},{"key":"mem_high","enabled":false,"threshold":90,"minutes":5}]}`
	if code, out := callStats(t, http.MethodPut, "host", "/alerts", "", put); code != 200 {
		t.Fatalf("PUT = %d %v", code, out)
	}
	stored := rulesOf(t)
	s0, s1 := stored[0].(map[string]any), stored[1].(map[string]any)
	if s0["threshold"] != float64(80) || s0["minutes"] != float64(5) || s1["enabled"] != false {
		t.Fatalf("stored = %v", stored)
	}

	good := `{"key":"cpu_high","enabled":true,"threshold":80,"minutes":5}`
	for _, tc := range []struct{ body, message string }{
		{`{"rules":[{"key":"cpu_high","enabled":true,"threshold":0,"minutes":5}]}`, "bad_rules: rules[0].threshold"},
		{`{"rules":[{"key":"cpu_high","enabled":true,"threshold":101,"minutes":5}]}`, "bad_rules: rules[0].threshold"},
		{`{"rules":[{"key":"cpu_high","enabled":true,"threshold":50,"minutes":0}]}`, "bad_rules: rules[0].minutes"},
		{`{"rules":[{"key":"cpu_high","enabled":true,"threshold":50,"minutes":121}]}`, "bad_rules: rules[0].minutes"},
		{`{"rules":[{"key":"disk_full","enabled":true,"threshold":50,"minutes":5}]}`, "bad_rules: rules[0].key"},
		{`{"rules":[` + good + `,{"key":"mem_high","enabled":true,"threshold":101,"minutes":5}]}`, "bad_rules: rules[1].threshold"},
		{`{"rules":[{"key":"cpu_high","threshold":50,"minutes":5}]}`, "bad_rules: rules[0].enabled"},
		{`{"rules":[{"key":"cpu_high","enabled":true,"threshold":"x","minutes":5}]}`, "bad_rules: rules[0].threshold"},
		{`{}`, "bad_rules: rules"},
		{`not json`, "bad_rules: rules"},
	} {
		code, out := callStats(t, http.MethodPut, "host", "/alerts", "", tc.body)
		if code != 400 || out["message"] != tc.message {
			t.Errorf("PUT %s = %d %v, want 400 %s", tc.body, code, out["message"], tc.message)
		}
	}
	if a, b := mustJSON(t, rulesOf(t)), mustJSON(t, stored); a != b {
		t.Errorf("rules changed by refused PUTs: %s, was %s", a, b)
	}

	// merge per key: one rule leaves the other as it was
	one := `{"rules":[{"key":"mem_high","enabled":true,"threshold":70,"minutes":3}]}`
	if code, out := callStats(t, http.MethodPut, "host", "/alerts", "", one); code != 200 {
		t.Fatalf("PUT one = %d %v", code, out)
	}
	merged := rulesOf(t)
	if merged[0].(map[string]any)["threshold"] != float64(80) || merged[1].(map[string]any)["threshold"] != float64(70) {
		t.Errorf("merged = %v", merged)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAnActiveAlertIsListed(t *testing.T) {
	r := useStatsFixture(t)
	r.src.set(95, false)
	r.minutes(12)

	_, out := callStats(t, http.MethodGet, "host", "/alerts", "", "")
	active := dataOf(t, out)["active"].([]any)
	if len(active) != 1 {
		t.Fatalf("active = %v", active)
	}
	a := active[0].(map[string]any)
	if a["key"] != "cpu_high" || a["value"] != float64(95) {
		t.Errorf("active = %v", a)
	}
	if since, ok := a["since"].(string); !ok {
		t.Errorf("since = %v, want an RFC 3339 string", a["since"])
	} else if _, err := time.Parse(time.RFC3339, since); err != nil {
		t.Errorf("since = %q: %v", since, err)
	}
	_, list := callStats(t, http.MethodGet, "", "", "", "")
	if got := dataOf(t, list)["items"].([]any)[0].(map[string]any)["alerts"]; got != float64(1) {
		t.Errorf("alerts = %v, want 1", got)
	}
}

func TestSeriesBeforeAnySampleIsEmpty(t *testing.T) {
	useEmptyStatsFixture(t)
	for q, step := range map[string]float64{"?range=1h": 60, "?range=live": 5} {
		code, out := callStats(t, http.MethodGet, "host", "", q, "")
		d := dataOf(t, out)
		if s, ok := d["series"].([]any); code != 200 || !ok || len(s) != 0 || d["step_seconds"] != step {
			t.Errorf("%s = %d %v", q, code, d)
		}
		if sc := d["scale"].(map[string]any); sc["cpu_allocation"] != nil || sc["memory_limit"] != nil {
			t.Errorf("%s scale = %v", q, sc)
		}
	}
}

func TestRulesThatCannotBeSavedAre503(t *testing.T) {
	r := useStatsFixture(t)
	path := filepath.Join(r.dir, "alerts.json")
	_ = os.Remove(path) // may not exist yet
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	put := `{"rules":[{"key":"cpu_high","enabled":true,"threshold":80,"minutes":5}]}`
	code, out := callStats(t, http.MethodPut, "host", "/alerts", "", put)
	if code != 503 || out["message"] != "stats_unavailable" {
		t.Errorf("PUT = %d %v", code, out["message"])
	}
}
