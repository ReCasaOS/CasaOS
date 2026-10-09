package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

const planID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeDockerUpdateService struct {
	service.SystemService
	asked  []string
	status service.SystemDockerUpdateStatus
	err    error
}

func (f *fakeDockerUpdateService) StartDockerUpdate(id string) (service.SystemDockerUpdateStatus, error) {
	f.asked = append(f.asked, id)
	return f.status, f.err
}

func (f *fakeDockerUpdateService) DockerUpdateStatus() service.SystemDockerUpdateStatus {
	return f.status
}

func withDockerUpdateService(t *testing.T, fake *fakeDockerUpdateService) {
	t.Helper()
	original := service.MyService
	t.Cleanup(func() { service.MyService = original })
	service.MyService = fakeSystemPackageRepository{system: fake}
}

func postDockerUpdate(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/sys/docker/update", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	if err := StartDockerUpdate(echo.New().NewContext(request, recorder)); err != nil {
		t.Fatalf("StartDockerUpdate() error = %v", err)
	}
	return recorder
}

func decodeResult(t *testing.T, recorder *httptest.ResponseRecorder) (model.Result, map[string]any) {
	t.Helper()
	var result model.Result
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	data, _ := result.Data.(map[string]any)
	return result, data
}

func TestStartDockerUpdatePassesThePlanIDAndAnswersWithTheStatus(t *testing.T) {
	fake := &fakeDockerUpdateService{status: service.SystemDockerUpdateStatus{
		Supported: true, State: "running", Phase: "downloading", StartedAt: "2026-10-09T10:00:00Z", NotReturned: []dockerpkg.NotReturned{},
	}}
	withDockerUpdateService(t, fake)

	recorder := postDockerUpdate(t, `{"plan_id":"`+planID+`"}`)
	if recorder.Code != http.StatusOK || len(fake.asked) != 1 || fake.asked[0] != planID {
		t.Fatalf("status = %d, asked = %v, body = %s", recorder.Code, fake.asked, recorder.Body)
	}
	want := `{"success":200,"message":"ok","data":{"supported":true,"state":"running","phase":"downloading","outcome":"","error":"","error_code":"","exit_code":null,"started_at":"2026-10-09T10:00:00Z","completed_at":"","from":"","to":"","not_returned":[],"rollback_command":"","log":""}}`
	if got := strings.TrimSpace(recorder.Body.String()); got != want {
		t.Errorf("body = %s\nwant   %s", got, want)
	}
}

func TestStartDockerUpdateTakesOnlyThePlanID(t *testing.T) {
	huge := `{"plan_id":"` + strings.Repeat("a", 10000) + `"}`
	for name, body := range map[string]string{
		"nothing":                    "",
		"not JSON":                   "plan_id=" + planID,
		"an array":                   `[]`,
		"a string":                   `"` + planID + `"`,
		"a plan id that is a number": `{"plan_id":1}`,
		"another field":              `{"plan_id":"` + planID + `","force":true}`,
		"only another field":         `{"plan":"` + planID + `"}`,
		"two objects":                `{"plan_id":"` + planID + `"}{"plan_id":"` + planID + `"}`,
		"junk after it":              `{"plan_id":"` + planID + `"} x`,
		"cut short":                  `{"plan_id":"` + planID,
		"too much":                   huge,
	} {
		fake := &fakeDockerUpdateService{}
		withDockerUpdateService(t, fake)
		recorder := postDockerUpdate(t, body)
		if recorder.Code != http.StatusBadRequest || len(fake.asked) != 0 {
			t.Errorf("%s: status = %d, asked = %v", name, recorder.Code, fake.asked)
		}
	}

	// an id that is not an id is the service's to refuse, and it is a bad request too
	for name, body := range map[string]string{"null": `null`, "empty object": `{}`, "short": `{"plan_id":"abc"}`} {
		fake := &fakeDockerUpdateService{err: service.ErrDockerUpdateBadPlanID}
		withDockerUpdateService(t, fake)
		if recorder := postDockerUpdate(t, body); recorder.Code != http.StatusBadRequest || len(fake.asked) != 1 {
			t.Errorf("%s: status = %d, asked = %v", name, recorder.Code, fake.asked)
		}
	}
}

func TestStartDockerUpdateRefusalsAreConflictsThatCarryTheirReason(t *testing.T) {
	for _, code := range []string{
		service.DockerRefusalOrigin, service.DockerRefusalHeld, service.DockerRefusalDaemon, service.DockerRefusalSwarm,
		service.DockerRefusalPlan, service.DockerRefusalDisk,
		service.DockerRefusalRunning, service.DockerRefusalMaintenance, service.DockerRefusalApps,
		service.DockerRefusalChanged, service.DockerRefusalNothing,
	} {
		t.Run(code, func(t *testing.T) {
			reason := "the reason for " + code
			fake := &fakeDockerUpdateService{
				err: fmt.Errorf("wrapped: %w", &service.DockerUpdateRefusal{Code: code, Reason: reason, Detail: []string{"immich"}}),
				// a status that says nothing: the handler puts the reason where the page reads it
				status: service.SystemDockerUpdateStatus{Supported: true, State: "idle", NotReturned: []dockerpkg.NotReturned{}},
			}
			withDockerUpdateService(t, fake)

			recorder := postDockerUpdate(t, `{"plan_id":"`+planID+`"}`)
			if recorder.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body)
			}
			result, data := decodeResult(t, recorder)
			if result.Success != http.StatusConflict || data["error_code"] != code || data["error"] == "" || data["error"] != result.Message {
				t.Errorf("result = %#v, data = %#v: the reason must be in the message and in data.error, the code in data.error_code", result, data)
			}
		})
	}

	// a status that has them already is left alone, detail and all
	fake := &fakeDockerUpdateService{
		err: &service.DockerUpdateRefusal{Code: "apps", Reason: "An app is busy."},
		status: service.SystemDockerUpdateStatus{
			Supported: true, State: "idle", Error: "An app is busy.", ErrorCode: "apps", RefusalDetail: []string{"immich"}, NotReturned: []dockerpkg.NotReturned{},
		},
	}
	withDockerUpdateService(t, fake)
	_, data := decodeResult(t, postDockerUpdate(t, `{"plan_id":"`+planID+`"}`))
	if detail, _ := data["refusal_detail"].([]any); len(detail) != 1 || detail[0] != "immich" {
		t.Errorf("data = %#v", data)
	}
}

func TestStartDockerUpdateOnAHostThatCannotIsNotImplemented(t *testing.T) {
	fake := &fakeDockerUpdateService{
		err:    fmt.Errorf("%w", service.ErrSystemPackageUpdatesUnsupported),
		status: service.SystemDockerUpdateStatus{Error: "CasaOS must run as root to update system packages.", State: "idle", NotReturned: []dockerpkg.NotReturned{}},
	}
	withDockerUpdateService(t, fake)
	recorder := postDockerUpdate(t, `{"plan_id":"`+planID+`"}`)
	result, data := decodeResult(t, recorder)
	if recorder.Code != http.StatusNotImplemented || data["error_code"] != "unsupported" || data["error"] == "" || data["error"] != result.Message {
		t.Errorf("status = %d, result = %#v, data = %#v", recorder.Code, result, data)
	}
}

func TestStartDockerUpdateFailuresOnTheBoxAreServerErrors(t *testing.T) {
	fake := &fakeDockerUpdateService{
		err:    errors.New("start Docker update: Access denied"),
		status: service.SystemDockerUpdateStatus{Supported: true, State: "failed", Outcome: "failed", ErrorCode: "start", NotReturned: []dockerpkg.NotReturned{}},
	}
	withDockerUpdateService(t, fake)
	recorder := postDockerUpdate(t, `{"plan_id":"`+planID+`"}`)
	result, data := decodeResult(t, recorder)
	// systemd that would not start the unit is its own code: not a plan that changed
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(result.Message, "Access denied") || data["state"] != "failed" || data["error_code"] != "start" {
		t.Errorf("status = %d, result = %#v", recorder.Code, result)
	}
}

func TestGetDockerUpdateStatusAnswersWithTheStatus(t *testing.T) {
	fake := &fakeDockerUpdateService{status: service.SystemDockerUpdateStatus{
		Supported: true, State: "failed", Outcome: "failed", Error: "Docker did not come back after the update.", ErrorCode: "daemon",
		StartedAt: "2026-10-09T10:00:00Z", CompletedAt: "2026-10-09T10:07:00Z", From: "28.0.4", To: "",
		NotReturned:     []dockerpkg.NotReturned{{Name: "job", RestartPolicy: "no"}},
		RollbackCommand: "sudo apt-get install --allow-downgrades docker-ce=5:28.0.4-1",
		Log:             "CASAOS_DOCKER_UPDATE_QUEUED x\n",
	}}
	withDockerUpdateService(t, fake)

	request := httptest.NewRequest(http.MethodGet, "/v1/sys/docker/update/status", nil)
	recorder := httptest.NewRecorder()
	if err := GetDockerUpdateStatus(echo.New().NewContext(request, recorder)); err != nil {
		t.Fatal(err)
	}
	want := `{"success":200,"message":"ok","data":{"supported":true,"state":"failed","phase":"","outcome":"failed","error":"Docker did not come back after the update.","error_code":"daemon","exit_code":null,"started_at":"2026-10-09T10:00:00Z","completed_at":"2026-10-09T10:07:00Z","from":"28.0.4","to":"","not_returned":[{"name":"job","restart_policy":"no"}],"rollback_command":"sudo apt-get install --allow-downgrades docker-ce=5:28.0.4-1","log":"CASAOS_DOCKER_UPDATE_QUEUED x\n"}}`
	if got := strings.TrimSpace(recorder.Body.String()); recorder.Code != http.StatusOK || got != want {
		t.Errorf("status = %d\nbody = %s\nwant   %s", recorder.Code, got, want)
	}
}
