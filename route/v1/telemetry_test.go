package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/pkg/telemetry"
	"github.com/labstack/echo/v4"
)

// useTelemetryFixture points the handlers at a box under a temporary root,
// whose distribution is v0.5.0. Nothing is ever sent from here.
func useTelemetryFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	release := filepath.Join(root, common.FORK_RELEASE_FILE)
	if err := os.MkdirAll(filepath.Dir(release), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("v0.5.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture := telemetry.New(root)
	fixture.Command = func(string, ...string) ([]byte, error) { return []byte("none\n"), nil }
	original := telemetry.Default
	telemetry.Default = fixture
	t.Cleanup(func() { telemetry.Default = original })
	return root
}

func callTelemetry(t *testing.T, method, body string) (int, telemetry.Status) {
	t.Helper()
	request := httptest.NewRequest(method, "/v1/sys/telemetry", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	context := echo.New().NewContext(request, recorder)
	handler := GetTelemetry
	if method == http.MethodPut {
		handler = PutTelemetry
	}
	if err := handler(context); err != nil {
		t.Fatalf("%s /v1/sys/telemetry: %v", method, err)
	}
	var response struct {
		Success int              `json:"success"`
		Data    telemetry.Status `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, recorder.Body.String())
	}
	if response.Success != recorder.Code {
		t.Fatalf("success = %d, status = %d", response.Success, recorder.Code)
	}
	return recorder.Code, response.Data
}

func telemetryStateOnDisk(t *testing.T, root string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, telemetry.StateFile))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("telemetry.json is not JSON: %v\n%s", err, data)
	}
	return state
}

func TestGetTelemetryAnswersTheStateAndAPreview(t *testing.T) {
	useTelemetryFixture(t)

	code, status := callTelemetry(t, http.MethodGet, "")

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !status.Enabled || status.NoticeSeen {
		t.Fatalf("data = %+v, want enabled and notice not seen with no telemetry.json", status)
	}
	properties := status.Preview.Properties
	if status.Preview.Event != "heartbeat" || properties["distribution"] != "v0.5.0" || properties["$lib"] != "recasaos-core" {
		t.Fatalf("preview = %+v, want the heartbeat of v0.5.0", status.Preview)
	}
}

func TestPutTelemetryChangesEnabledAndNoticeSeenAndNothingElse(t *testing.T) {
	root := useTelemetryFixture(t)
	if err := os.WriteFile(filepath.Join(root, telemetry.StateFile), []byte(`{"enabled":true,"id":"kept-id","last_sent":"2026-09-21T10:00:00Z","notice_seen":false}`), 0o600); err != nil {
		t.Fatal(err)
	}

	code, status := callTelemetry(t, http.MethodPut, `{"enabled":false}`)
	if code != http.StatusOK || status.Enabled || status.NoticeSeen || status.Preview.Event != "heartbeat" {
		t.Fatalf("PUT enabled=false: %d %+v", code, status)
	}
	want := map[string]any{"enabled": false, "id": "kept-id", "last_sent": "2026-09-21T10:00:00Z", "notice_seen": false}
	if got := telemetryStateOnDisk(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("telemetry.json = %v, want %v", got, want)
	}

	code, status = callTelemetry(t, http.MethodPut, `{"notice_seen":true,"something_else":1}`)
	if code != http.StatusOK || status.Enabled || !status.NoticeSeen {
		t.Fatalf("PUT notice_seen=true: %d %+v", code, status)
	}
	want["notice_seen"] = true
	if got := telemetryStateOnDisk(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("telemetry.json = %v, want %v", got, want)
	}
}

func TestPutTelemetryRefusesABodyThatIsNotTheState(t *testing.T) {
	root := useTelemetryFixture(t)

	code, _ := callTelemetry(t, http.MethodPut, `{"enabled":"yes"}`)

	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if _, err := os.Stat(filepath.Join(root, telemetry.StateFile)); !os.IsNotExist(err) {
		t.Fatalf("telemetry.json was written: %v", err)
	}
}
