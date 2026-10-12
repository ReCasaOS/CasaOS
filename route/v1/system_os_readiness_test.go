package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

// fakeReadinessService is a box that has made the report it is given.
type fakeReadinessService struct {
	service.SystemService
	answer osreadiness.Answer
	asked  int
}

func (f *fakeReadinessService) GetSystemOSReadiness() osreadiness.Answer {
	f.asked++
	return f.answer
}

func TestGetSystemOSReadinessAnswersWithTheEnvelopeAndTheReport(t *testing.T) {
	original := service.MyService
	t.Cleanup(func() { service.MyService = original })

	report := osreadiness.Report{
		CheckedAt: time.Date(2026, 10, 11, 20, 15, 2, 0, time.UTC),
		State:     osreadiness.StateBlocked,
		Hop: &osreadiness.Hop{
			From: osreadiness.Release{ID: "debian", VersionID: "11", Codename: "bullseye", PrettyName: "Debian GNU/Linux 11 (bullseye)"},
			To:   osreadiness.Release{ID: "debian", VersionID: "12", Codename: "bookworm", PrettyName: "Debian 12 (bookworm)"},
		},
		Counts: osreadiness.Counts{Refuse: 1, OK: 1},
		Checks: []osreadiness.Finding{
			{Code: "broken_dpkg", Severity: osreadiness.Refuse, Title: "dpkg has an unfinished operation", Detail: "dpkg --audit says: ...", Data: map[string]any{"unknown": false}, Action: osreadiness.ActionFinishDpkg},
			{Code: "container", Severity: osreadiness.OK, Title: "The system is not a container", Data: map[string]any{"unknown": false}},
		},
	}
	box := &fakeReadinessService{answer: osreadiness.NewAnswer(report)}
	service.MyService = fakeSystemPackageRepository{system: box}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/sys/os/readiness", nil)
	if err := GetSystemOSReadiness(echo.New().NewContext(request, recorder)); err != nil {
		t.Fatalf("GetSystemOSReadiness() error = %v", err)
	}

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get(echo.HeaderCacheControl); got != "no-store" {
		t.Fatalf("Cache-Control = %q: the answer is about the box as it is now", got)
	}
	var envelope struct {
		Success int             `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	if envelope.Success != http.StatusOK || envelope.Message != "ok" {
		t.Fatalf("envelope = %d %q, want 200 \"ok\"", envelope.Success, envelope.Message)
	}

	// the report of the spec, with the same as plain text in the same object
	var data struct {
		CheckedAt string `json:"checked_at"`
		State     string `json:"state"`
		Hop       struct {
			From map[string]string `json:"from"`
			To   map[string]string `json:"to"`
		} `json:"hop"`
		Counts map[string]int `json:"counts"`
		Checks []struct {
			Code     string         `json:"code"`
			Severity string         `json:"severity"`
			Title    string         `json:"title"`
			Detail   string         `json:"detail"`
			Data     map[string]any `json:"data"`
			Action   string         `json:"action"`
		} `json:"checks"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decode %s: %v", envelope.Data, err)
	}
	if data.CheckedAt != "2026-10-11T20:15:02Z" || data.State != "blocked" || data.Hop.From["codename"] != "bullseye" || data.Hop.To["pretty_name"] != "Debian 12 (bookworm)" {
		t.Fatalf("data = %s", envelope.Data)
	}
	if data.Counts["refuse"] != 1 || data.Counts["warn"] != 0 || data.Counts["ok"] != 1 || data.Counts["info"] != 0 {
		t.Fatalf("counts = %v", data.Counts)
	}
	if len(data.Checks) != 2 || data.Checks[0].Code != "broken_dpkg" || data.Checks[0].Severity != "refuse" || data.Checks[0].Action != "finish_dpkg" || data.Checks[0].Data["unknown"] != false {
		t.Fatalf("checks = %+v", data.Checks)
	}
	if data.Text != osreadiness.Text(report) || data.Text == "" {
		t.Fatalf("text = %q", data.Text)
	}
	if box.asked != 1 {
		t.Fatalf("the report was made %d times", box.asked)
	}
}
