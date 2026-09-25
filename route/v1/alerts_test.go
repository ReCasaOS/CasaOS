package v1

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/alerts"
	"github.com/labstack/echo/v4"
)

const phoneURL = "ntfy://ntfy.sh/secret-topic"

// useAlertsFixture points the handlers at a box under a temporary root with
// one channel, a phone, whose sends succeed, and a mail box whose sends fail.
// Nothing leaves the test.
func useAlertsFixture(t *testing.T) string {
	t.Helper()
	fixture := alerts.New(t.TempDir())
	fixture.Hostname = func() (string, error) { return "box", nil }
	fixture.Send = func(rawURL, title, message string) error {
		if strings.HasPrefix(rawURL, "smtp://") {
			return errors.New("535 authentication failed")
		}
		return nil
	}
	path := filepath.Join(fixture.Root, alerts.ConfigFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"channels":[{"id":"a1","name":"Phone","url":"`+phoneURL+`"}],"disk_threshold":90}`), 0o600); err != nil {
		t.Fatal(err)
	}
	original := alerts.Default
	alerts.Default = fixture
	t.Cleanup(func() { alerts.Default = original })
	return path
}

func callAlerts(t *testing.T, method, path, body string) (int, json.RawMessage) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	context := echo.New().NewContext(request, recorder)
	handler := map[string]echo.HandlerFunc{
		http.MethodGet + " /v1/sys/alerts":       GetAlerts,
		http.MethodPut + " /v1/sys/alerts":       PutAlerts,
		http.MethodPost + " /v1/sys/alerts/test": PostAlertsTest,
	}[method+" "+path]
	if err := handler(context); err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	var response struct {
		Success int             `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, recorder.Body.String())
	}
	if response.Success != recorder.Code {
		t.Fatalf("success = %d, status = %d", response.Success, recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "secret-topic") || strings.Contains(recorder.Body.String(), "hunter2") {
		t.Fatalf("%s %s answered a URL: %s", method, path, recorder.Body.String())
	}
	return recorder.Code, response.Data
}

func TestGetAlertsAnswersTheChannelsWithoutTheirURLs(t *testing.T) {
	useAlertsFixture(t)

	code, data := callAlerts(t, http.MethodGet, "/v1/sys/alerts", "")

	want := map[string]any{
		"channels":       []any{map[string]any{"id": "a1", "name": "Phone", "service": "ntfy", "host": "ntfy.sh"}},
		"categories":     map[string]any{"backups": true, "disks": true, "updates": true, "apps": true},
		"disk_threshold": float64(90),
		"last_failure":   nil,
	}
	if got := asJSON(t, data); code != http.StatusOK || !reflect.DeepEqual(got, want) {
		t.Fatalf("GET = %d %v, want 200 %v", code, got, want)
	}
}

func TestPutAlertsKeepsAStoredURLAndAddsAChannel(t *testing.T) {
	path := useAlertsFixture(t)

	code, data := callAlerts(t, http.MethodPut, "/v1/sys/alerts",
		`{"channels":[{"id":"a1","name":"My phone"},{"name":"Mail","url":"smtp://me:hunter2@mail.example.com:587/?from=box@example.com&to=me@example.com"}],"categories":{"disks":false},"something_else":1}`)

	if code != http.StatusOK {
		t.Fatalf("PUT = %d %s, want 200", code, data)
	}
	got := asJSON(t, data)
	channels, _ := got["channels"].([]any)
	if len(channels) != 2 || !reflect.DeepEqual(channels[0], map[string]any{"id": "a1", "name": "My phone", "service": "ntfy", "host": "ntfy.sh"}) {
		t.Fatalf("channels = %v", got["channels"])
	}
	if added, _ := channels[1].(map[string]any); added["service"] != "smtp" || added["host"] != "mail.example.com" || added["id"] == "" {
		t.Fatalf("the new channel = %v", channels[1])
	}
	if categories, _ := got["categories"].(map[string]any); categories["disks"] != false || categories["apps"] != true {
		t.Fatalf("categories = %v", got["categories"])
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), phoneURL) || !strings.Contains(string(onDisk), "hunter2") {
		t.Fatalf("alerts.json = %s, want both URLs kept", onDisk)
	}
}

func TestPutAlertsRefusesABodyThatIsNotOne(t *testing.T) {
	for _, body := range []string{
		`{"channels":[{"name":"Web","url":"https://example.com/hook"}]}`,
		`{"channels":[{"name":"Phone"}]}`,
		`{"channels":[{"id":"zz","name":"Phone"}]}`,
		`{"disk_threshold":100}`,
		`{"disk_threshold":"90"}`,
		`{"categories":["apps"]}`,
	} {
		t.Run(body, func(t *testing.T) {
			path := useAlertsFixture(t)
			before, _ := os.ReadFile(path)

			code, _ := callAlerts(t, http.MethodPut, "/v1/sys/alerts", body)

			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}
			if after, _ := os.ReadFile(path); string(after) != string(before) {
				t.Fatalf("alerts.json was written: %s", after)
			}
		})
	}
}

func TestPostAlertsTestAnswersPerChannel(t *testing.T) {
	useAlertsFixture(t)
	callAlerts(t, http.MethodPut, "/v1/sys/alerts", `{"channels":[{"id":"a1","name":"Phone"},{"name":"Mail","url":"smtp://me:hunter2@mail.example.com:587/?from=box@example.com&to=me@example.com"}]}`)

	code, data := callAlerts(t, http.MethodPost, "/v1/sys/alerts/test", "")
	var results []map[string]any
	if err := json.Unmarshal(data, &results); err != nil || code != http.StatusOK || len(results) != 2 {
		t.Fatalf("POST test = %d %s", code, data)
	}
	if !reflect.DeepEqual(results[0], map[string]any{"id": "a1", "ok": true, "error": ""}) ||
		results[1]["ok"] != false || results[1]["error"] != "535 authentication failed" {
		t.Fatalf("results = %v", results)
	}

	code, data = callAlerts(t, http.MethodPost, "/v1/sys/alerts/test", `{"channel_id":"a1"}`)
	if code != http.StatusOK || string(data) != `[{"id":"a1","ok":true,"error":""}]` {
		t.Fatalf("POST test a1 = %d %s", code, data)
	}
	if failure := asJSON(t, mustGet(t))["last_failure"].(map[string]any); failure["channel"] != "Mail" || failure["error"] != "535 authentication failed" {
		t.Fatalf("last_failure = %v", failure)
	}

	if code, _ := callAlerts(t, http.MethodPost, "/v1/sys/alerts/test", `{"channel_id":"zz"}`); code != http.StatusBadRequest {
		t.Fatalf("POST test zz = %d, want 400", code)
	}
}

func mustGet(t *testing.T) json.RawMessage {
	t.Helper()
	_, data := callAlerts(t, http.MethodGet, "/v1/sys/alerts", "")
	return data
}
