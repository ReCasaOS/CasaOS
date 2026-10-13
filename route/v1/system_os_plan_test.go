package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

// fakeOSPlanService is a box that makes the plans it is given.
type fakeOSPlanService struct {
	service.SystemService
	started    service.SystemOSPlanStatus
	startErr   error
	status     service.SystemOSPlanStatus
	runbook    *osplan.Runbook
	runbookErr error

	starts, reads, runbooks int
	// events are what happened, in order, among the calls that end the core; stopWait is how long it was told to wait
	events   []string
	stopWait time.Duration
}

func (f *fakeOSPlanService) StopSystemOSPlan(wait time.Duration) {
	f.events = append(f.events, "stop the plan")
	f.stopWait = wait
}

func (f *fakeOSPlanService) StartSystemOSPlan() (service.SystemOSPlanStatus, error) {
	f.starts++
	return f.started, f.startErr
}

func (f *fakeOSPlanService) GetSystemOSPlan() service.SystemOSPlanStatus {
	f.reads++
	return f.status
}

func (f *fakeOSPlanService) GetSystemOSRunbook() (*osplan.Runbook, error) {
	f.runbooks++
	return f.runbook, f.runbookErr
}

func withOSPlanService(t *testing.T, fake *fakeOSPlanService) {
	t.Helper()
	original := service.MyService
	t.Cleanup(func() { service.MyService = original })
	service.MyService = fakeSystemPackageRepository{system: fake}
}

func at(t *testing.T, s string) *time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return &v
}

func osPlanRequest(t *testing.T, handler echo.HandlerFunc, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/v1/sys/os/upgrade/plan", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	if err := handler(echo.New().NewContext(request, recorder)); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if got := recorder.Header().Get(echo.HeaderCacheControl); got != "no-store" {
		t.Errorf("Cache-Control = %q: the answer is about the box as it is now, and may hold what its files say", got)
	}
	if got := recorder.Header().Get(echo.HeaderContentType); !strings.HasPrefix(got, echo.MIMEApplicationJSON) {
		t.Errorf("Content-Type = %q", got)
	}
	return recorder
}

func body(r *httptest.ResponseRecorder) string { return strings.TrimSpace(r.Body.String()) }

func TestStartSystemOSPlanAnswersAcceptedWhenAJobBegan(t *testing.T) {
	fake := &fakeOSPlanService{started: service.SystemOSPlanStatus{Plannable: true, State: "running", Phase: "sources", StartedAt: at(t, "2026-10-12T10:00:00Z")}}
	withOSPlanService(t, fake)
	recorder := osPlanRequest(t, StartSystemOSPlan, http.MethodPost, "")
	want := `{"success":202,"message":"accepted","data":{"plannable":true,"state":"running","phase":"sources","started_at":"2026-10-12T10:00:00Z"}}`
	if recorder.Code != http.StatusAccepted || body(recorder) != want || fake.starts != 1 {
		t.Fatalf("status = %d, starts = %d, body = %s\nwant %s", recorder.Code, fake.starts, body(recorder), want)
	}
	// nothing is asked of the body: a client that sends one, or none, is answered the same
	for _, b := range []string{"{}", `{"plan_id":"x"}`, "not json"} {
		fake.starts = 0
		if recorder := osPlanRequest(t, StartSystemOSPlan, http.MethodPost, b); recorder.Code != http.StatusAccepted || fake.starts != 1 {
			t.Errorf("body %q: status = %d, starts = %d", b, recorder.Code, fake.starts)
		}
	}
}

func TestStartSystemOSPlanAnswersTheJobThatRunsWith200(t *testing.T) {
	fake := &fakeOSPlanService{started: service.SystemOSPlanStatus{Plannable: true, State: "running", Phase: "update", StartedAt: at(t, "2026-10-12T10:00:00Z"), AlreadyRunning: true}}
	withOSPlanService(t, fake)
	recorder := osPlanRequest(t, StartSystemOSPlan, http.MethodPost, "")
	want := `{"success":200,"message":"ok","data":{"plannable":true,"state":"running","phase":"update","started_at":"2026-10-12T10:00:00Z","already_running":true}}`
	if recorder.Code != http.StatusOK || body(recorder) != want {
		t.Fatalf("status = %d, body = %s\nwant %s", recorder.Code, body(recorder), want)
	}
}

// What is refused at once is a 409 with the code in data.error_code, the English reason in message and in data.error, and what
// makes the code precise in data.data.
func TestStartSystemOSPlanRefusesAtOnceWith409AndTheCode(t *testing.T) {
	for _, c := range []struct {
		refusal *service.OSPlanRefusal
		want    string
	}{
		{&service.OSPlanRefusal{Code: "not_root", Detail: "the core does not run as root"},
			`{"success":409,"message":"the core does not run as root","data":{"error_code":"not_root","error":"the core does not run as root"}}`},
		{&service.OSPlanRefusal{Code: "hop_not_planned", Detail: "no plan is made for this hop yet", Data: map[string]any{"hop": "debian-12-13"}},
			`{"success":409,"message":"no plan is made for this hop yet","data":{"error_code":"hop_not_planned","error":"no plan is made for this hop yet","data":{"hop":"debian-12-13"}}}`},
		{&service.OSPlanRefusal{Code: "identity_unsupported", Detail: "this machine is not one that a plan can be made for", Data: map[string]any{"state": "unsupported", "checks": []string{"unsupported_os"}}},
			`{"success":409,"message":"this machine is not one that a plan can be made for","data":{"error_code":"identity_unsupported","error":"this machine is not one that a plan can be made for","data":{"checks":["unsupported_os"],"state":"unsupported"}}}`},
		{&service.OSPlanRefusal{Code: "busy", Detail: "another package manager is at work: casaos-package-update is running"},
			`{"success":409,"message":"another package manager is at work: casaos-package-update is running","data":{"error_code":"busy","error":"another package manager is at work: casaos-package-update is running"}}`},
		{&service.OSPlanRefusal{Code: "busy", Detail: "whether another package manager is at work could not be seen", Data: map[string]any{"unknown": true}},
			`{"success":409,"message":"whether another package manager is at work could not be seen","data":{"error_code":"busy","error":"whether another package manager is at work could not be seen","data":{"unknown":true}}}`},
		{&service.OSPlanRefusal{Code: "no_room", Detail: "there is not room for the sandbox in /var/tmp", Data: map[string]any{"path": "/var/tmp", "need": uint64(671088640), "free": uint64(1024)}},
			`{"success":409,"message":"there is not room for the sandbox in /var/tmp","data":{"error_code":"no_room","error":"there is not room for the sandbox in /var/tmp","data":{"free":1024,"need":671088640,"path":"/var/tmp"}}}`},
	} {
		fake := &fakeOSPlanService{startErr: c.refusal}
		withOSPlanService(t, fake)
		recorder := osPlanRequest(t, StartSystemOSPlan, http.MethodPost, "")
		if recorder.Code != http.StatusConflict || body(recorder) != c.want {
			t.Errorf("%s: status = %d, body = %s\nwant %s", c.refusal.Code, recorder.Code, body(recorder), c.want)
		}
	}
	// a refusal that is wrapped is still one
	withOSPlanService(t, &fakeOSPlanService{startErr: errors.Join(errors.New("x"), &service.OSPlanRefusal{Code: "busy", Detail: "d"})})
	if recorder := osPlanRequest(t, StartSystemOSPlan, http.MethodPost, ""); recorder.Code != http.StatusConflict {
		t.Errorf("a wrapped refusal: status = %d", recorder.Code)
	}
}

func TestStartSystemOSPlanAnswers500ForAnErrorThatIsNoRefusal(t *testing.T) {
	withOSPlanService(t, &fakeOSPlanService{startErr: errors.New("boom")})
	recorder := osPlanRequest(t, StartSystemOSPlan, http.MethodPost, "")
	if recorder.Code != http.StatusInternalServerError || body(recorder) != `{"success":500,"message":"boom","data":null}` {
		t.Errorf("status = %d, body = %s", recorder.Code, body(recorder))
	}
}

func TestGetSystemOSPlanSaysItsStates(t *testing.T) {
	for name, c := range map[string]struct {
		status service.SystemOSPlanStatus
		want   string
	}{
		"idle": {service.SystemOSPlanStatus{Plannable: true, State: "idle"},
			`{"plannable":true,"state":"idle"}`},
		"not plannable": {service.SystemOSPlanStatus{Plannable: false, Reason: "identity_unsupported", State: "idle"},
			`{"plannable":false,"reason":"identity_unsupported","state":"idle"}`},
		"running": {service.SystemOSPlanStatus{Plannable: true, State: "running", Phase: "dist-upgrade", StartedAt: at(t, "2026-10-12T10:00:00Z")},
			`{"plannable":true,"state":"running","phase":"dist-upgrade","started_at":"2026-10-12T10:00:00Z"}`},
		"expired": {service.SystemOSPlanStatus{Plannable: true, State: "idle", Expired: true, ExpiresAt: at(t, "2026-10-13T10:00:00Z")},
			`{"plannable":true,"state":"idle","expires_at":"2026-10-13T10:00:00Z","expired":true}`},
		"failed": {service.SystemOSPlanStatus{
			Plannable: true, State: "failed", Phase: "update", StartedAt: at(t, "2026-10-12T10:00:00Z"), FinishedAt: at(t, "2026-10-12T10:00:42Z"),
			Error: &osplan.Failure{Code: "sandbox_update_failed", Phase: "update", Detail: "W: could not resolve 'deb.example.invalid'", Data: map[string]any{"kind": "network", "exit": 100}},
		}, `{"plannable":true,"state":"failed","phase":"update","started_at":"2026-10-12T10:00:00Z","finished_at":"2026-10-12T10:00:42Z","error":{"code":"sandbox_update_failed","phase":"update","detail":"W: could not resolve 'deb.example.invalid'","data":{"exit":100,"kind":"network"}}}`},
	} {
		fake := &fakeOSPlanService{status: c.status}
		withOSPlanService(t, fake)
		recorder := osPlanRequest(t, GetSystemOSPlan, http.MethodGet, "")
		want := `{"success":200,"message":"ok","data":` + c.want + `}`
		if recorder.Code != http.StatusOK || body(recorder) != want || fake.reads != 1 || fake.starts != 0 {
			t.Errorf("%s: status = %d, reads = %d, starts = %d, body = %s\nwant %s", name, recorder.Code, fake.reads, fake.starts, body(recorder), want)
		}
	}
}

// The plan of a job that is done is the plan without the tree of the sources, which the runbook has.
func TestGetSystemOSPlanSaysThePlanWithoutTheTree(t *testing.T) {
	plan := osplan.Plan{
		PlanID: "p1-0123456789abcdef0123456789abcdef", Hop: "debian-11-12", Verdict: "ok",
		MadeAt: *at(t, "2026-10-12T10:00:00Z"), ExpiresAt: *at(t, "2026-10-13T10:00:00Z"),
		Sources: osplan.SourcesPlan{Files: []osplan.FileChange{}, Tree: []osplan.TreeFile{{Path: "sources.list", Content: "deb http://user:secret@mirror/debian bookworm main\n"}}, HashCurrent: "a", HashGenerated: "b"},
	}
	shown := plan.WithoutTree()
	fake := &fakeOSPlanService{status: service.SystemOSPlanStatus{Plannable: true, State: "done", StartedAt: at(t, "2026-10-12T10:00:00Z"), FinishedAt: at(t, "2026-10-12T10:00:42Z"), ExpiresAt: at(t, "2026-10-13T10:00:00Z"), Plan: &shown}}
	withOSPlanService(t, fake)
	recorder := osPlanRequest(t, GetSystemOSPlan, http.MethodGet, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if strings.Contains(body(recorder), "secret") || strings.Contains(body(recorder), `"tree"`) {
		t.Errorf("the tree of the sources, which has what the box's files say, is in the answer: %s", body(recorder))
	}
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range envelope.Data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"expires_at", "finished_at", "plan", "plannable", "started_at", "state"}; !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	var planKeys map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Data["plan"], &planKeys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"plan_id", "hop", "made_at", "expires_at", "verdict", "problems", "counts", "removals", "new_packages", "kept_back", "autoremovable", "sources", "paused", "holds", "docker", "estimates", "disk", "sandbox", "apt_conf_left_out"} {
		if _, ok := planKeys[k]; !ok {
			t.Errorf("the plan has no %q", k)
		}
	}
	var sourcesKeys map[string]json.RawMessage
	if err := json.Unmarshal(planKeys["sources"], &sourcesKeys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"files", "hash_current", "hash_generated", "dropped_backports", "firmware_added"} {
		if _, ok := sourcesKeys[k]; !ok {
			t.Errorf("the sources of the plan have no %q", k)
		}
	}
	if _, ok := sourcesKeys["tree"]; ok {
		t.Error("the sources of the plan have their tree")
	}
}

func TestGetSystemOSRunbookSaysTheTextAndItsBlocks(t *testing.T) {
	fake := &fakeOSPlanService{runbook: &osplan.Runbook{
		PlanID: "p1-0123456789abcdef0123456789abcdef", Hop: "debian-11-12", Text: "# runbook\nset -eu\n",
		Blocks: []osplan.RunbookBlock{{ID: "verify", Title: "Verify"}, {ID: "upgrade", Title: "Point of no return", PointOfNoReturn: true}, {ID: "after", Title: "Restart", Reboot: true}},
	}}
	withOSPlanService(t, fake)
	recorder := osPlanRequest(t, GetSystemOSRunbook, http.MethodGet, "")
	want := `{"success":200,"message":"ok","data":{"plan_id":"p1-0123456789abcdef0123456789abcdef","hop":"debian-11-12","text":"# runbook\nset -eu\n","blocks":[{"id":"verify","title":"Verify","reboot":false,"point_of_no_return":false},{"id":"upgrade","title":"Point of no return","reboot":false,"point_of_no_return":true},{"id":"after","title":"Restart","reboot":true,"point_of_no_return":false}]}}`
	if recorder.Code != http.StatusOK || body(recorder) != want || fake.runbooks != 1 {
		t.Fatalf("status = %d, body = %s\nwant %s", recorder.Code, body(recorder), want)
	}
}

func TestGetSystemOSRunbookRefusesWith409AndTheCode(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"no plan": {&service.OSPlanRefusal{Code: "no_plan", Detail: "there is no plan to write a runbook from (blocked)", Data: map[string]any{"reason": "blocked"}},
			`{"success":409,"message":"there is no plan to write a runbook from (blocked)","data":{"error_code":"no_plan","error":"there is no plan to write a runbook from (blocked)","data":{"reason":"blocked"}}}`},
		"unsafe": {&service.OSPlanRefusal{Code: "no_runbook", Detail: "a line of the sources would end a part of the runbook early", Data: map[string]any{"reason": "runbook_unsafe"}},
			`{"success":409,"message":"a line of the sources would end a part of the runbook early","data":{"error_code":"no_runbook","error":"a line of the sources would end a part of the runbook early","data":{"reason":"runbook_unsafe"}}}`},
	} {
		withOSPlanService(t, &fakeOSPlanService{runbookErr: c.err})
		recorder := osPlanRequest(t, GetSystemOSRunbook, http.MethodGet, "")
		if recorder.Code != http.StatusConflict || body(recorder) != c.want {
			t.Errorf("%s: status = %d, body = %s\nwant %s", name, recorder.Code, body(recorder), c.want)
		}
	}
	withOSPlanService(t, &fakeOSPlanService{runbookErr: errors.New("boom")})
	if recorder := osPlanRequest(t, GetSystemOSRunbook, http.MethodGet, ""); recorder.Code != http.StatusInternalServerError {
		t.Errorf("an error that is no refusal: status = %d", recorder.Code)
	}
}

// POST /v1/sys/stop (the one route of PostKillCasaOS, pinned by TestTheStopRouteIsPostKillCasaOS in the package route; there is
// no restart route) ends the core with os.Exit, which does not go through the handler of main.go that ends a plan that is
// being made: the handler does it first, with the wait that main.go gives it, and only then ends the core.
func TestRestartEndsThePlanBeforeItEndsTheCore(t *testing.T) {
	fake := &fakeOSPlanService{}
	withOSPlanService(t, fake)
	real := exitCore
	exitCore = func(code int) { fake.events = append(fake.events, fmt.Sprintf("exit %d", code)) }
	t.Cleanup(func() { exitCore = real })
	request := httptest.NewRequest(http.MethodPost, "/v1/sys/stop", nil)
	if err := PostKillCasaOS(echo.New().NewContext(request, httptest.NewRecorder())); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.events, []string{"stop the plan", "exit 0"}) {
		t.Errorf("events = %v", fake.events)
	}
	if fake.stopWait != service.OSPlanStopWait || service.OSPlanStopWait < 5*time.Second {
		t.Errorf("the plan was given %v to end", fake.stopWait)
	}
}
