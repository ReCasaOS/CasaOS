package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/autoupdate"
	"github.com/labstack/echo/v4"
)

// releases is version.json advertising v0.5.8, out since 2026-09-21.
type releases struct{}

func (releases) GetCasaosVersion() model.Version {
	return model.Version{Version: "v0.5.8", PublishedAt: "2026-09-21T10:00:00Z"}
}
func (r releases) FetchCasaosVersion() model.Version { return r.GetCasaosVersion() }

// useAutoUpdateFixture points the handlers at a box under a temporary root, at
// 2026-09-24 12:00 UTC with v0.5.7 installed. Nothing is ever started from here.
func useAutoUpdateFixture(t *testing.T) string {
	t.Helper()
	fixture := autoupdate.New(t.TempDir())
	fixture.Now = func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC) }
	fixture.Current = func() string { return "0.5.7" }
	fixture.Releases = releases{}
	fixture.Start = func(string) error { t.Error("an update was started"); return errors.New("not here") }
	fixture.Command = func(string, ...string) ([]byte, error) { return []byte("inactive\n"), nil }
	fixture.AppsBusy = func(context.Context) bool { return true }
	original := autoupdate.Default
	autoupdate.Default = fixture
	t.Cleanup(func() { autoupdate.Default = original })
	return fixture.Root
}

func callAutoUpdate(t *testing.T, method, body string) (int, json.RawMessage) {
	t.Helper()
	request := httptest.NewRequest(method, "/v1/sys/autoupdate", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	context := echo.New().NewContext(request, recorder)
	handler := GetAutoUpdate
	if method == http.MethodPut {
		handler = PutAutoUpdate
	}
	if err := handler(context); err != nil {
		t.Fatalf("%s /v1/sys/autoupdate: %v", method, err)
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
	return recorder.Code, response.Data
}

// asJSON is data as JSON values, the way the dashboard reads it.
func asJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, data)
	}
	return values
}

func TestGetAutoUpdateAnswersOffByDefault(t *testing.T) {
	useAutoUpdateFixture(t)

	code, data := callAutoUpdate(t, http.MethodGet, "")

	want := map[string]any{
		"enabled": false,
		"window":  map[string]any{"start": "03:00", "end": "05:00"},
		"state":   "off",
		"next":    nil,
		"last":    nil,
	}
	if got := asJSON(t, data); code != http.StatusOK || !reflect.DeepEqual(got, want) {
		t.Fatalf("GET = %d %v, want 200 %v", code, got, want)
	}
}

func TestPutAutoUpdateTurnsItOnAndSetsTheWindow(t *testing.T) {
	root := useAutoUpdateFixture(t)

	code, data := callAutoUpdate(t, http.MethodPut, `{"enabled":true,"window_start":"23:00","window_end":"01:00","something_else":1}`)

	if code != http.StatusOK {
		t.Fatalf("PUT = %d %s, want 200", code, data)
	}
	// The fixture's clock is in UTC, so its window is too: v0.5.8 at 23:00 tonight.
	want := map[string]any{
		"enabled": true,
		"window":  map[string]any{"start": "23:00", "end": "01:00"},
		"state":   "waiting",
		"next":    map[string]any{"version": "v0.5.8", "not_before": "2026-09-24T23:00:00Z"},
		"last":    nil,
	}
	if got := asJSON(t, data); !reflect.DeepEqual(got, want) {
		t.Fatalf("PUT answered %v, want %v", got, want)
	}
	onDisk, err := os.ReadFile(filepath.Join(root, autoupdate.StateFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"enabled":true,"window_start":"23:00","window_end":"01:00"}`; string(onDisk) != want {
		t.Fatalf("autoupdate.json = %s, want %s", onDisk, want)
	}
}

func TestPutAutoUpdateResumesAPausedRelease(t *testing.T) {
	root := useAutoUpdateFixture(t)
	paused := `{"enabled":true,"window_start":"03:00","window_end":"05:00",` +
		`"last":{"version":"v0.5.8","started_at":"2026-09-24T01:05:00Z","result":"failed"},` +
		`"failures":{"version":"v0.5.8","count":2}}`
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, autoupdate.StateFile)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, autoupdate.StateFile), []byte(paused), 0o600); err != nil {
		t.Fatal(err)
	}

	_, data := callAutoUpdate(t, http.MethodGet, "")
	got := asJSON(t, data)
	wantLast := map[string]any{"version": "v0.5.8", "started_at": "2026-09-24T01:05:00Z", "result": "failed"}
	if got["state"] != "paused" || got["next"] != nil || !reflect.DeepEqual(got["last"], wantLast) {
		t.Fatalf("GET = %v, want paused, no next, the failed attempt", got)
	}

	code, data := callAutoUpdate(t, http.MethodPut, `{"resume":true}`)
	if got := asJSON(t, data); code != http.StatusOK || got["state"] != "waiting" {
		t.Fatalf("PUT resume = %d %v, want 200 and waiting", code, got)
	}
	onDisk, err := os.ReadFile(filepath.Join(root, autoupdate.StateFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onDisk), "failures") || !strings.Contains(string(onDisk), `"last"`) {
		t.Fatalf("autoupdate.json = %s, want the failures cleared and the attempt kept", onDisk)
	}
}

func TestPutAutoUpdateRefusesABodyThatIsNotOne(t *testing.T) {
	for _, body := range []string{
		`{"enabled":"yes"}`,
		`{"window_start":3}`,
		`{"window_start":"25:00"}`,
		`{"window_start":"3:00"}`,
		`{"window_start":"04:30"}`, // with 05:00 kept: half an hour
		`{"window_start":"03:00","window_end":"03:00"}`,
		`{"resume":"please"}`,
	} {
		t.Run(body, func(t *testing.T) {
			root := useAutoUpdateFixture(t)

			code, _ := callAutoUpdate(t, http.MethodPut, body)

			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}
			if _, err := os.Stat(filepath.Join(root, autoupdate.StateFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("autoupdate.json was written: %v", err)
			}
		})
	}
}
