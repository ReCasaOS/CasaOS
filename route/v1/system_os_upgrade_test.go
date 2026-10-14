package v1

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

// fakeOSUpgradeService is an engine that says what it is told to say, and records what it was asked.
type fakeOSUpgradeService struct {
	service.SystemService
	status  service.OSUpgradeStatus
	err     error
	runbook *service.OSUpgradeRunbook

	calls    []string
	start    service.OSUpgradeStartRequest
	refresh  service.OSUpgradeRefreshRequest
	ack      service.OSUpgradeAckRequest
	beta     []bool
	planBook int
}

func (f *fakeOSUpgradeService) GetSystemOSUpgradeStatus() service.OSUpgradeStatus {
	f.calls = append(f.calls, "status")
	return f.status
}

func (f *fakeOSUpgradeService) StartSystemOSUpgrade(req service.OSUpgradeStartRequest) (service.OSUpgradeStatus, error) {
	f.calls, f.start = append(f.calls, "start"), req
	return f.status, f.err
}

func (f *fakeOSUpgradeService) StartSystemOSUpgradeRefresh(req service.OSUpgradeRefreshRequest) (service.OSUpgradeStatus, error) {
	f.calls, f.refresh = append(f.calls, "refresh"), req
	return f.status, f.err
}

func (f *fakeOSUpgradeService) RebootSystemOSUpgrade() (service.OSUpgradeStatus, error) {
	f.calls = append(f.calls, "reboot")
	return f.status, f.err
}

func (f *fakeOSUpgradeService) ResumeSystemOSUpgrade() (service.OSUpgradeStatus, error) {
	f.calls = append(f.calls, "resume")
	return f.status, f.err
}

func (f *fakeOSUpgradeService) AbortSystemOSUpgrade() (service.OSUpgradeStatus, error) {
	f.calls = append(f.calls, "abort")
	return f.status, f.err
}

func (f *fakeOSUpgradeService) AckSystemOSUpgrade(req service.OSUpgradeAckRequest) (service.OSUpgradeStatus, error) {
	f.calls, f.ack = append(f.calls, "ack"), req
	return f.status, f.err
}

func (f *fakeOSUpgradeService) SetSystemOSUpgradeBeta(enabled bool) error {
	f.calls, f.beta = append(f.calls, "beta"), append(f.beta, enabled)
	return f.err
}

func (f *fakeOSUpgradeService) GetSystemOSUpgradeRunbook() (*service.OSUpgradeRunbook, error) {
	f.calls = append(f.calls, "run runbook")
	return f.runbook, f.err
}

func (f *fakeOSUpgradeService) GetSystemOSRunbook() (*osplan.Runbook, error) {
	f.planBook++
	return &osplan.Runbook{PlanID: "p1-0123456789abcdef0123456789abcdef", Hop: "debian-11-12", Text: "x", Blocks: []osplan.RunbookBlock{}}, nil
}

func withOSUpgradeService(t *testing.T, fake *fakeOSUpgradeService) {
	t.Helper()
	original := service.MyService
	t.Cleanup(func() { service.MyService = original })
	service.MyService = fakeSystemPackageRepository{system: fake}
}

// upgradeRequest calls a handler the way the router would, and checks what every answer of the upgrade has: it is about the machine as it is now, and it is JSON.
func upgradeRequest(t *testing.T, handler echo.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	if err := handler(echo.New().NewContext(request, recorder)); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if got := recorder.Header().Get(echo.HeaderCacheControl); got != "no-store" {
		t.Errorf("Cache-Control = %q: the answer is about the machine as it is now, and may hold what its files say", got)
	}
	if got := recorder.Header().Get(echo.HeaderContentType); !strings.HasPrefix(got, echo.MIMEApplicationJSON) {
		t.Errorf("Content-Type = %q", got)
	}
	return recorder
}

func aStatus(state string) service.OSUpgradeStatus {
	since := time.Date(2026, time.October, 13, 10, 0, 0, 0, time.UTC)
	return service.OSUpgradeStatus{BetaEnabled: true, State: state, Hop: "debian-11-12", Busy: true, Since: &since, Actions: []string{"abort"}, LogTail: []string{}}
}

func statusJSON(t *testing.T, status service.OSUpgradeStatus) string {
	t.Helper()
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const aStartBody = `{"plan_id":"p1-0123456789abcdef0123456789abcdef","rehearsal":false,"safety_net":"snapshot","hostname":"box1","accept_removals":["fuse"],"paused_sources":["docker.list","sources.list:12"],"acks":["no_interruption","no_way_back","sources_paused","removals_read"]}`

// The status is the engine's own, in the envelope of the others, and it is always an answer: it asks the service once and nothing else.
func TestTheStatusOfTheUpgradeIsTheEnginesAnswerInTheEnvelope(t *testing.T) {
	fake := &fakeOSUpgradeService{status: aStatus("running")}
	withOSUpgradeService(t, fake)
	recorder := upgradeRequest(t, GetSystemOSUpgradeStatus, http.MethodGet, "/v1/sys/os/upgrade/status", "")
	want := `{"success":200,"message":"ok","data":` + statusJSON(t, fake.status) + `}`
	if recorder.Code != http.StatusOK || body(recorder) != want || !reflect.DeepEqual(fake.calls, []string{"status"}) {
		t.Fatalf("status = %d, calls = %v, body = %s\nwant %s", recorder.Code, fake.calls, body(recorder), want)
	}
}

// The start reads its body strictly and hands the engine exactly what the owner sent, and answers 202 with the status that follows.
func TestTheStartAnswersAcceptedWithTheStatusAndHandsOverTheBody(t *testing.T) {
	fake := &fakeOSUpgradeService{status: aStatus("running")}
	withOSUpgradeService(t, fake)
	recorder := upgradeRequest(t, StartSystemOSUpgrade, http.MethodPost, "/v1/sys/os/upgrade/start", aStartBody)
	want := `{"success":202,"message":"accepted","data":` + statusJSON(t, fake.status) + `}`
	if recorder.Code != http.StatusAccepted || body(recorder) != want {
		t.Fatalf("status = %d, body = %s\nwant %s", recorder.Code, body(recorder), want)
	}
	wantRequest := service.OSUpgradeStartRequest{
		PlanID: "p1-0123456789abcdef0123456789abcdef", SafetyNet: "snapshot", Hostname: "box1", AcceptRemovals: []string{"fuse"},
		PausedSources: []string{"docker.list", "sources.list:12"}, Acks: []string{"no_interruption", "no_way_back", "sources_paused", "removals_read"},
	}
	if !reflect.DeepEqual(fake.start, wantRequest) || !reflect.DeepEqual(fake.calls, []string{"start"}) {
		t.Errorf("the engine was handed %+v after %v", fake.start, fake.calls)
	}
	// a rehearsal is the same body with the field set, and the engine is told so
	fake.calls = nil
	upgradeRequest(t, StartSystemOSUpgrade, http.MethodPost, "/v1/sys/os/upgrade/start", `{"plan_id":"p1-0123456789abcdef0123456789abcdef","rehearsal":true,"hostname":"box1","acks":["no_interruption"]}`)
	if !fake.start.Rehearsal || fake.start.Hostname != "box1" {
		t.Errorf("the engine was handed %+v", fake.start)
	}
}

// The refresh takes one word of the owner, said or not, and hands the engine exactly that; it answers 202 with the status that follows.
func TestTheRefreshAnswersAcceptedWithTheStatusAndHandsOverTheOneWord(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       service.OSUpgradeRefreshRequest
	}{
		{"nothing said", `{}`, service.OSUpgradeRefreshRequest{}},
		{"not accepted", `{"accept_releaseinfo_change":false}`, service.OSUpgradeRefreshRequest{}},
		{"accepted", `{"accept_releaseinfo_change":true}`, service.OSUpgradeRefreshRequest{AcceptReleaseInfoChange: true}},
	} {
		fake := &fakeOSUpgradeService{status: aStatus("refreshing")}
		withOSUpgradeService(t, fake)
		recorder := upgradeRequest(t, StartSystemOSUpgradeRefresh, http.MethodPost, "/v1/sys/os/upgrade/refresh", c.body)
		want := `{"success":202,"message":"accepted","data":` + statusJSON(t, fake.status) + `}`
		if recorder.Code != http.StatusAccepted || body(recorder) != want || !reflect.DeepEqual(fake.calls, []string{"refresh"}) || fake.refresh != c.want {
			t.Errorf("%s: status = %d, calls = %v, handed %+v, body = %s\nwant %s", c.name, recorder.Code, fake.calls, fake.refresh, body(recorder), want)
		}
	}
	// what is not strict JSON, or is not what the word is, is a 400 that names the field and asks nothing of the engine
	for _, c := range []struct{ name, body, field string }{
		{"no body", ``, "body"}, {"an array", `[]`, "body"}, {"an unknown field", `{"force":true}`, "body"}, {"a word that is not true or false", `{"accept_releaseinfo_change":"yes"}`, "accept_releaseinfo_change"},
		{"words after the object", `{} {}`, "body"},
	} {
		fake := &fakeOSUpgradeService{status: aStatus("refreshing")}
		withOSUpgradeService(t, fake)
		recorder := upgradeRequest(t, StartSystemOSUpgradeRefresh, http.MethodPost, "/v1/sys/os/upgrade/refresh", c.body)
		var answer struct {
			Data struct {
				ErrorCode string         `json:"error_code"`
				Data      map[string]any `json:"data"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil || recorder.Code != http.StatusBadRequest || answer.Data.ErrorCode != "bad_request" || answer.Data.Data["field"] != c.field || len(fake.calls) != 0 {
			t.Errorf("%s: status = %d, answer = %s, calls = %v", c.name, recorder.Code, body(recorder), fake.calls)
		}
	}
	// a refusal of the engine is the plan's 409 with its code, and start_failed is a 500
	for code, status := range map[string]int{service.OSUpgradeSourceUnrecognized: 409, service.OSUpgradeBetaOff: 409, service.OSUpgradeStartFailed: 500} {
		withOSUpgradeService(t, &fakeOSUpgradeService{err: &service.OSUpgradeRefusal{Code: code, Detail: "d", Data: map[string]any{"file": "x.list", "line": 3}}})
		recorder := upgradeRequest(t, StartSystemOSUpgradeRefresh, http.MethodPost, "/v1/sys/os/upgrade/refresh", `{}`)
		if recorder.Code != status || !strings.Contains(body(recorder), `"error_code":"`+code+`"`) {
			t.Errorf("%s: status = %d, body = %s", code, recorder.Code, body(recorder))
		}
	}
}

// What is not strict JSON is a 400 before the engine is asked: a body that is not an object, one that has a field that the engine does not know, a field of the wrong kind,
// words after the object, a body that is too long, and no body. The answer names the field when it was a field, and never echoes what the owner sent.
func TestABodyThatIsNotStrictJSONIsRefusedWith400BeforeTheEngineIsAsked(t *testing.T) {
	for _, c := range []struct {
		name, body, field string
	}{
		{"not json", `plan_id=p1`, "body"},
		{"no body", ``, "body"},
		{"only space", " \n", "body"},
		{"an array", `[]`, "body"},
		{"null", `null`, "body"},
		{"a string", `"p1-0123456789abcdef0123456789abcdef"`, "body"},
		{"an unknown field", `{"plan_id":"p1-0123456789abcdef0123456789abcdef","rehearsel":true}`, "body"},
		{"a field of the wrong kind", `{"plan_id":"p1-0123456789abcdef0123456789abcdef","rehearsal":"yes"}`, "rehearsal"},
		{"a list of the wrong kind", `{"plan_id":"p1-0123456789abcdef0123456789abcdef","acks":"no_interruption"}`, "acks"},
		{"words after the object", `{"plan_id":"p1-0123456789abcdef0123456789abcdef"} {}`, "body"},
		{"a closing brace after the object", `{"plan_id":"p1-0123456789abcdef0123456789abcdef"}}`, "body"},
		{"a body that is cut short", `{"plan_id":"p1-0123456789abcdef0123456789abcdef"`, "body"},
		{"a body that is too long", `{"plan_id":"p1-0123456789abcdef0123456789abcdef","hostname":"` + strings.Repeat("a", 70<<10) + `"}`, "body"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeOSUpgradeService{status: aStatus("running")}
			withOSUpgradeService(t, fake)
			recorder := upgradeRequest(t, StartSystemOSUpgrade, http.MethodPost, "/v1/sys/os/upgrade/start", c.body)
			var answer struct {
				Success int    `json:"success"`
				Message string `json:"message"`
				Data    struct {
					ErrorCode string         `json:"error_code"`
					Error     string         `json:"error"`
					Data      map[string]any `json:"data"`
				} `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
				t.Fatalf("%v: %s", err, body(recorder))
			}
			if recorder.Code != http.StatusBadRequest || answer.Success != 400 || answer.Data.ErrorCode != "bad_request" || answer.Data.Data["field"] != c.field || answer.Message == "" || answer.Message != answer.Data.Error {
				t.Errorf("status = %d, answer = %+v", recorder.Code, answer)
			}
			if len(fake.calls) != 0 {
				t.Errorf("the engine was asked: %v", fake.calls)
			}
			if c.name == "an unknown field" && strings.Contains(body(recorder), "rehearsel") {
				t.Errorf("the name that the owner sent is in the answer: %s", body(recorder))
			}
		})
	}
}

// The three buttons that have nothing to say take the empty object and nothing else, and answer 202 with the status; each asks the engine for its own button only.
func TestTheButtonsAnswerAcceptedAndTakeTheEmptyObjectOnly(t *testing.T) {
	for _, c := range []struct {
		name    string
		handler echo.HandlerFunc
	}{
		{"reboot", RebootSystemOSUpgrade}, {"resume", ResumeSystemOSUpgrade}, {"abort", AbortSystemOSUpgrade},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeOSUpgradeService{status: aStatus("rebooting")}
			withOSUpgradeService(t, fake)
			target := "/v1/sys/os/upgrade/" + c.name
			recorder := upgradeRequest(t, c.handler, http.MethodPost, target, `{}`)
			want := `{"success":202,"message":"accepted","data":` + statusJSON(t, fake.status) + `}`
			if recorder.Code != http.StatusAccepted || body(recorder) != want || !reflect.DeepEqual(fake.calls, []string{c.name}) {
				t.Fatalf("status = %d, calls = %v, body = %s\nwant %s", recorder.Code, fake.calls, body(recorder), want)
			}
			fake.calls = nil
			for _, bad := range []string{``, `null`, `[]`, `{"force":true}`, `{} {}`, `x`} {
				if recorder := upgradeRequest(t, c.handler, http.MethodPost, target, bad); recorder.Code != http.StatusBadRequest {
					t.Errorf("body %q: status = %d, want 400", bad, recorder.Code)
				}
			}
			if len(fake.calls) != 0 {
				t.Errorf("the engine was asked: %v", fake.calls)
			}
		})
	}
}

// The word of the owner on a run is passed as it is, and the engine decides what it is worth; the answer is 200.
func TestTheAckAnswers200AndHandsOverTheWord(t *testing.T) {
	fake := &fakeOSUpgradeService{status: aStatus("idle")}
	withOSUpgradeService(t, fake)
	recorder := upgradeRequest(t, AckSystemOSUpgrade, http.MethodPost, "/v1/sys/os/upgrade/ack", `{"action":"acknowledge","word":"I-KNOW-THIS-MACHINE-IS-HALF-UPGRADED"}`)
	want := `{"success":200,"message":"ok","data":` + statusJSON(t, fake.status) + `}`
	if recorder.Code != http.StatusOK || body(recorder) != want {
		t.Fatalf("status = %d, body = %s\nwant %s", recorder.Code, body(recorder), want)
	}
	if want := (service.OSUpgradeAckRequest{Action: "acknowledge", Word: "I-KNOW-THIS-MACHINE-IS-HALF-UPGRADED"}); fake.ack != want {
		t.Errorf("the engine was handed %+v", fake.ack)
	}
	// no word is an empty word: the engine says word_required when it is needed
	upgradeRequest(t, AckSystemOSUpgrade, http.MethodPost, "/v1/sys/os/upgrade/ack", `{"action":"discard"}`)
	if fake.ack.Action != "discard" || fake.ack.Word != "" {
		t.Errorf("the engine was handed %+v", fake.ack)
	}
	fake.calls = nil
	for _, bad := range []string{``, `{"action":"discard","force":true}`, `{"action":1}`, `[]`} {
		if recorder := upgradeRequest(t, AckSystemOSUpgrade, http.MethodPost, "/v1/sys/os/upgrade/ack", bad); recorder.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", bad, recorder.Code)
		}
	}
	if len(fake.calls) != 0 {
		t.Errorf("the engine was asked: %v", fake.calls)
	}
}

// The switch says what it was set to, and it is never set by a body that does not say: {} must not turn the feature off.
func TestTheSwitchSaysWhatItWasSetToAndNeverGuesses(t *testing.T) {
	fake := &fakeOSUpgradeService{}
	withOSUpgradeService(t, fake)
	for _, enabled := range []bool{true, false} {
		recorder := upgradeRequest(t, SetSystemOSUpgradeBeta, http.MethodPost, "/v1/sys/os/upgrade/beta", map[bool]string{true: `{"enabled":true}`, false: `{"enabled":false}`}[enabled])
		want := map[bool]string{true: `{"success":200,"message":"ok","data":{"beta_enabled":true}}`, false: `{"success":200,"message":"ok","data":{"beta_enabled":false}}`}[enabled]
		if recorder.Code != http.StatusOK || body(recorder) != want {
			t.Errorf("enabled %v: status = %d, body = %s\nwant %s", enabled, recorder.Code, body(recorder), want)
		}
	}
	if !reflect.DeepEqual(fake.beta, []bool{true, false}) {
		t.Errorf("the engine was told %v", fake.beta)
	}
	fake.beta = nil
	for _, bad := range []string{``, `{}`, `{"enabled":null}`, `{"enabled":"true"}`, `{"enabled":1}`, `{"enabled":true,"force":true}`, `true`} {
		if recorder := upgradeRequest(t, SetSystemOSUpgradeBeta, http.MethodPost, "/v1/sys/os/upgrade/beta", bad); recorder.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", bad, recorder.Code)
		}
	}
	if len(fake.beta) != 0 {
		t.Errorf("the engine was told %v", fake.beta)
	}
}

// A switch that cannot be turned off while a run is open is a 409 with the code and the state, as every refusal is; a switch that could not be written is a 500.
func TestTheSwitchIsRefusedWhileARunIsOpen(t *testing.T) {
	refusal := &service.OSUpgradeRefusal{Code: service.OSUpgradeRunOpen, Detail: "an upgrade is open: it has to be finished or closed first", Data: map[string]any{"state": "awaiting_reboot", "run_id": "0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19"}}
	withOSUpgradeService(t, &fakeOSUpgradeService{err: refusal})
	recorder := upgradeRequest(t, SetSystemOSUpgradeBeta, http.MethodPost, "/v1/sys/os/upgrade/beta", `{"enabled":false}`)
	want := `{"success":409,"message":"an upgrade is open: it has to be finished or closed first","data":{"error_code":"run_open","error":"an upgrade is open: it has to be finished or closed first","data":{"run_id":"0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19","state":"awaiting_reboot"}}}`
	if recorder.Code != http.StatusConflict || body(recorder) != want {
		t.Errorf("status = %d, body = %s\nwant %s", recorder.Code, body(recorder), want)
	}
	withOSUpgradeService(t, &fakeOSUpgradeService{err: errors.New("open /etc/casaos/os-upgrade.beta: read-only file system")})
	recorder = upgradeRequest(t, SetSystemOSUpgradeBeta, http.MethodPost, "/v1/sys/os/upgrade/beta", `{"enabled":true}`)
	if recorder.Code != http.StatusInternalServerError || strings.Contains(body(recorder), "read-only") || strings.Contains(body(recorder), "/etc/casaos") {
		t.Errorf("status = %d, body = %s", recorder.Code, body(recorder))
	}
}

// Every code that the engine refuses with is a 409 with the code in data.error_code, the English reason in message and in data.error, and what makes the code precise in data.data;
// except the one that is the owner's mistake of a field, which is 400, and the three that are the machine's failure to do what was asked, which are 500.
func TestEveryRefusalOfTheEngineIsA409WithItsCodeExceptTheOnesWithAStatusOfTheirOwn(t *testing.T) {
	codes := append(append(append([]string{}, service.OSUpgradeStartCodes...), service.OSUpgradeRefreshCodes...), service.OSUpgradeActionCodes...)
	codes = append(codes, service.OSUpgradeOpen)
	own := map[string]int{service.OSUpgradeBadRequest: 400, service.OSUpgradeStartFailed: 500, service.OSUpgradeRebootFailed: 500, service.OSUpgradeCloseFailed: 500}
	seen := map[string]bool{}
	for _, code := range codes {
		if seen[code] {
			continue
		}
		seen[code] = true
		want := http.StatusConflict
		if status, ok := own[code]; ok {
			want = status
		}
		withOSUpgradeService(t, &fakeOSUpgradeService{err: &service.OSUpgradeRefusal{Code: code, Detail: "because of " + code, Data: map[string]any{"state": "running"}}})
		recorder := upgradeRequest(t, RebootSystemOSUpgrade, http.MethodPost, "/v1/sys/os/upgrade/reboot", `{}`)
		var answer struct {
			Success int    `json:"success"`
			Message string `json:"message"`
			Data    struct {
				ErrorCode string         `json:"error_code"`
				Error     string         `json:"error"`
				Data      map[string]any `json:"data"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
			t.Fatalf("%s: %v: %s", code, err, body(recorder))
		}
		if recorder.Code != want || answer.Success != want || answer.Data.ErrorCode != code || answer.Message != "because of "+code || answer.Data.Error != answer.Message || answer.Data.Data["state"] != "running" {
			t.Errorf("%s: status = %d, answer = %+v", code, recorder.Code, answer)
		}
	}
	for code, status := range own {
		if !seen[code] {
			t.Errorf("%s (%d) is not among the codes that the engine lists", code, status)
		}
	}
	// a refusal that is wrapped is still one
	withOSUpgradeService(t, &fakeOSUpgradeService{err: errors.Join(errors.New("x"), &service.OSUpgradeRefusal{Code: service.OSUpgradeNoRun, Detail: "d"})})
	if recorder := upgradeRequest(t, ResumeSystemOSUpgrade, http.MethodPost, "/v1/sys/os/upgrade/resume", `{}`); recorder.Code != http.StatusConflict {
		t.Errorf("a wrapped refusal: status = %d", recorder.Code)
	}
}

// What is no refusal is a 500 that says only that it is internal: the error may hold a path of the machine, or a line of what its files say, and a stack is for the log.
func TestAnErrorThatIsNoRefusalIsA500ThatSaysOnlyThatItIsInternal(t *testing.T) {
	withOSUpgradeService(t, &fakeOSUpgradeService{err: errors.New("open /var/lib/casaos/os-upgrade/0b8f3a52/plan.json: deb http://user:secret@mirror/debian bookworm main")})
	recorder := upgradeRequest(t, StartSystemOSUpgrade, http.MethodPost, "/v1/sys/os/upgrade/start", aStartBody)
	want := `{"success":500,"message":"the request could not be carried out","data":{"error_code":"internal","error":"the request could not be carried out"}}`
	if recorder.Code != http.StatusInternalServerError || body(recorder) != want {
		t.Errorf("status = %d, body = %s\nwant %s", recorder.Code, body(recorder), want)
	}
}

// The text of the run that is open is asked for with run=current, and it is the engine's: the text of the plan that is live is what the same route says with no run asked for.
func TestTheRunbookOfTheRunIsAskedForByName(t *testing.T) {
	book := &service.OSUpgradeRunbook{
		PlanID: "p1-0123456789abcdef0123456789abcdef", Hop: "debian-11-12", Text: "# runbook\nset -eu\n", FromRun: true,
		Blocks: []osplan.RunbookBlock{{ID: "verify", Title: "Verify"}, {ID: "after", Title: "Restart", Reboot: true}},
	}
	fake := &fakeOSUpgradeService{runbook: book}
	withOSUpgradeService(t, fake)
	recorder := upgradeRequest(t, GetSystemOSRunbook, http.MethodGet, "/v1/sys/os/upgrade/runbook?run=current", "")
	want := `{"success":200,"message":"ok","data":{"plan_id":"p1-0123456789abcdef0123456789abcdef","hop":"debian-11-12","text":"# runbook\nset -eu\n","blocks":[{"id":"verify","title":"Verify","reboot":false,"point_of_no_return":false},{"id":"after","title":"Restart","reboot":true,"point_of_no_return":false}],"from_run":true}}`
	if recorder.Code != http.StatusOK || body(recorder) != want || !reflect.DeepEqual(fake.calls, []string{"run runbook"}) || fake.planBook != 0 {
		t.Fatalf("status = %d, calls = %v, plan's runbook %d times, body = %s\nwant %s", recorder.Code, fake.calls, fake.planBook, body(recorder), want)
	}

	// with no run asked for it is the plan's text, and the engine's run is not touched
	fake.calls = nil
	if recorder := upgradeRequest(t, GetSystemOSRunbook, http.MethodGet, "/v1/sys/os/upgrade/runbook", ""); recorder.Code != http.StatusOK || fake.planBook != 1 || len(fake.calls) != 0 {
		t.Errorf("status = %d, plan's runbook %d times, engine asked %v", recorder.Code, fake.planBook, fake.calls)
	}

	// any other thing asked for is a 400, and nothing is read
	fake.calls, fake.planBook = nil, 0
	for _, target := range []string{"?run=", "?run=latest", "?run=Current", "?run=current&run=current", "?run=current%00", "?run=%zz", "?run=curr%zzent", "?token=x&run=%"} {
		recorder := upgradeRequest(t, GetSystemOSRunbook, http.MethodGet, "/v1/sys/os/upgrade/runbook"+target, "")
		if recorder.Code != http.StatusBadRequest || !strings.Contains(body(recorder), `"field":"run"`) {
			t.Errorf("%s: status = %d, body = %s", target, recorder.Code, body(recorder))
		}
	}
	if len(fake.calls) != 0 || fake.planBook != 0 {
		t.Errorf("something was read: %v, %d", fake.calls, fake.planBook)
	}

	// a query that cannot be read, and does not ask for the run, is read as it always was
	if recorder := upgradeRequest(t, GetSystemOSRunbook, http.MethodGet, "/v1/sys/os/upgrade/runbook?token=%zz", ""); recorder.Code != http.StatusOK || fake.planBook != 1 {
		t.Errorf("a query that does not ask for the run: status = %d, plan's runbook %d times", recorder.Code, fake.planBook)
	}
	fake.planBook = 0

	// no run is a 409 with its code
	withOSUpgradeService(t, &fakeOSUpgradeService{err: &service.OSUpgradeRefusal{Code: service.OSUpgradeNoRun, Detail: "there is no upgrade of the operating system to act on"}})
	recorder = upgradeRequest(t, GetSystemOSRunbook, http.MethodGet, "/v1/sys/os/upgrade/runbook?run=current", "")
	if recorder.Code != http.StatusConflict || !strings.Contains(body(recorder), `"error_code":"no_run"`) {
		t.Errorf("status = %d, body = %s", recorder.Code, body(recorder))
	}
}

// The page's restart and shutdown answer 409 with the code when an upgrade works on the machine, as every refusal does, and are 500 for anything else as they were.
func TestThePowerRoutesSayThatAnUpgradeIsOpenWith409(t *testing.T) {
	original := service.MyService
	t.Cleanup(func() { service.MyService = original })
	refusal := &service.OSUpgradeRefusal{Code: service.OSUpgradeOpen, Detail: "an upgrade of the operating system is changing the machine", Data: map[string]any{"state": "running"}}
	for _, c := range []struct {
		state string
		err   error
		code  int
		want  string
	}{
		{"restart", refusal, 409, `{"success":409,"message":"an upgrade of the operating system is changing the machine","data":{"error_code":"os_upgrade_open","error":"an upgrade of the operating system is changing the machine","data":{"state":"running"}}}`},
		{"off", refusal, 409, `{"success":409,"message":"an upgrade of the operating system is changing the machine","data":{"error_code":"os_upgrade_open","error":"an upgrade of the operating system is changing the machine","data":{"state":"running"}}}`},
		{"restart", errors.Join(errors.New("x"), refusal), 409, ""},
		{"off", errors.New("systemctl poweroff: exit status 1"), 500, `{"success":500,"message":"systemctl poweroff: exit status 1","data":null}`},
	} {
		service.MyService = fakeSystemStateRepository{system: &fakeSystemStateService{rebootErr: c.err, shutdownErr: c.err}}
		request := httptest.NewRequest(http.MethodPut, "/v1/sys/state/"+c.state, nil)
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(request, recorder)
		ctx.SetPath("/v1/sys/state/:state")
		ctx.SetParamNames("state")
		ctx.SetParamValues(c.state)
		if err := PutSystemState(ctx); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != c.code || (c.want != "" && body(recorder) != c.want) {
			t.Errorf("%s (%v): status = %d, body = %s\nwant %d %s", c.state, c.err, recorder.Code, body(recorder), c.code, c.want)
		}
	}
	// and when it is let through, the answer is the one it always was
	service.MyService = fakeSystemStateRepository{system: &fakeSystemStateService{}}
	request := httptest.NewRequest(http.MethodPut, "/v1/sys/state/restart", nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.SetParamNames("state")
	ctx.SetParamValues("restart")
	if err := PutSystemState(ctx); err != nil || recorder.Code != http.StatusOK || !strings.Contains(body(recorder), "The operation will be completed shortly.") {
		t.Errorf("err = %v, status = %d, body = %s", err, recorder.Code, body(recorder))
	}
}
