package service_test

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/route"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

// anEngineRepository is the service of the box with the engine of the test in the place of its system service.
type anEngineRepository struct {
	service.Repository
	system service.SystemService
}

func (r anEngineRepository) System() service.SystemService { return r.system }

// whatIsOnTheMachine is every file below a folder, with its kind, mode and size, one line each: what the product left on the machine, to be compared with what it left before.
func whatIsOnTheMachine(t *testing.T, folder string) []string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		size := int64(0)
		if info.Mode().IsRegular() {
			size = info.Size()
		}
		rel, _ := filepath.Rel(folder, path)
		lines = append(lines, fmt.Sprintf("%s %s %d", rel, info.Mode(), size))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return lines
}

// A machine on which the feature was never switched on is as it was: with the flag file absent, the new routes answer what says that nothing is begun and nothing is open, they
// make no folder and write no file, ask systemd nothing, and leave the owner's restart and shutdown what they were. It goes through the real router, the real engine and the real
// guard, with a folder of its own for everything that the engine writes.
func TestWithTheFlagFileAbsentTheProductIsAsItWas(t *testing.T) {
	box := service.NewTheUpgradeBox(t)
	runtimePath := t.TempDir()
	if err := external.WriteInternalSecret(runtimePath); err != nil {
		t.Fatal(err)
	}
	secret := external.InternalAuthorization(runtimePath)
	previousConfig, previousService := config.CommonInfo, service.MyService
	config.CommonInfo = &model.CommonModel{RuntimePath: runtimePath}
	service.MyService = anEngineRepository{system: box.System}
	t.Cleanup(func() { config.CommonInfo, service.MyService = previousConfig, previousService })

	router := route.InitV1Router()
	type answer struct {
		Success int    `json:"success"`
		Message string `json:"message"`
		Data    struct {
			ErrorCode   string   `json:"error_code"`
			BetaEnabled *bool    `json:"beta_enabled"`
			State       string   `json:"state"`
			Busy        bool     `json:"busy"`
			Actions     []string `json:"actions"`
		} `json:"data"`
	}
	call := func(method, path, body string) (int, answer) {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		request.Header.Set(echo.HeaderAuthorization, secret)
		request.RemoteAddr = "127.0.0.1:4000"
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		var envelope struct {
			Success int             `json:"success"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, recorder.Body.String())
		}
		a := answer{Success: envelope.Success, Message: envelope.Message}
		// the data of an answer is an object, but for the restart's, which is a sentence
		_ = json.Unmarshal(envelope.Data, &a.Data)
		return recorder.Code, a
	}
	before := whatIsOnTheMachine(t, box.Folder)

	// what begins something is refused by the switch, and what acts on a run has no run to act on
	const base = "/v1/sys/os/upgrade/"
	startBody := `{"plan_id":"p1-0123456789abcdef0123456789abcdef","safety_net":"snapshot","hostname":"box1","accept_removals":[],"paused_sources":[],"acks":["no_interruption","no_way_back"]}`
	rehearsalBody := `{"plan_id":"p1-0123456789abcdef0123456789abcdef","rehearsal":true,"hostname":"box1","acks":["no_interruption"]}`
	for _, c := range []struct {
		method, path, body string
		code               int
		errorCode          string
	}{
		{http.MethodPost, base + "refresh", `{}`, http.StatusConflict, "beta_off"},
		{http.MethodPost, base + "refresh", `{"accept_releaseinfo_change":true}`, http.StatusConflict, "beta_off"},
		{http.MethodPost, base + "start", startBody, http.StatusConflict, "beta_off"},
		{http.MethodPost, base + "start", rehearsalBody, http.StatusConflict, "beta_off"},
		{http.MethodPost, base + "reboot", `{}`, http.StatusConflict, "no_run"},
		{http.MethodPost, base + "resume", `{}`, http.StatusConflict, "no_run"},
		{http.MethodPost, base + "abort", `{}`, http.StatusConflict, "no_run"},
		{http.MethodPost, base + "ack", `{"action":"acknowledge"}`, http.StatusConflict, "no_run"},
		{http.MethodPost, base + "ack", `{"action":"discard"}`, http.StatusConflict, "no_run"},
		{http.MethodPost, base + "ack", `{"action":"manual"}`, http.StatusConflict, "no_run"},
		{http.MethodGet, base + "runbook?run=current", ``, http.StatusConflict, "no_run"},
	} {
		code, a := call(c.method, c.path, c.body)
		if code != c.code || a.Data.ErrorCode != c.errorCode {
			t.Errorf("%s %s %s: %d %q, want %d %q", c.method, c.path, c.body, code, a.Data.ErrorCode, c.code, c.errorCode)
		}
	}

	// the status says that nothing is switched on, nothing is open, and there is nothing to do
	code, a := call(http.MethodGet, base+"status", "")
	if code != http.StatusOK || a.Data.BetaEnabled == nil || *a.Data.BetaEnabled || a.Data.State != "idle" || a.Data.Busy || len(a.Data.Actions) != 0 {
		t.Errorf("the status: %d, %+v", code, a.Data)
	}

	// switching it off when it is off is what it says and changes nothing
	if code, a := call(http.MethodPost, base+"beta", `{"enabled":false}`); code != http.StatusOK || a.Data.BetaEnabled == nil || *a.Data.BetaEnabled {
		t.Errorf("the switch: %d, %+v", code, a.Data)
	}

	// the owner's restart and shutdown are what they were: the call is made, with nothing asked of systemd to know whether an upgrade works
	for _, c := range []struct{ state, action string }{{"restart", "reboot"}, {"off", "poweroff"}} {
		if code, a := call(http.MethodPut, "/v1/sys/state/"+c.state, ""); code != http.StatusOK || a.Message != "ok" {
			t.Errorf("%s: %d, %+v", c.state, code, a)
		}
	}
	if got := box.Powered(); !reflect.DeepEqual(got, []string{"reboot", "poweroff"}) {
		t.Errorf("the machine was asked to %q", got)
	}

	// and all of it left no trace: no folder, no file, no question to systemd
	if after := whatIsOnTheMachine(t, box.Folder); !reflect.DeepEqual(before, after) {
		t.Errorf("the machine is not as it was:\nbefore %q\nafter  %q", before, after)
	}
	if got := box.Asked(); len(got) != 0 {
		t.Errorf("systemd was asked %q", got)
	}
	if _, err := os.Lstat(filepath.Join(box.Folder, "lib")); !os.IsNotExist(err) {
		t.Errorf("the folder of the engine was made: %v", err)
	}
	if box.System.MaintenanceBusy() {
		t.Error("the machine is busy for an upgrade that is not there")
	}

	// the control: the test sees the switch when it is turned, through the same routes, and what it writes is the one file that the switch is
	if code, a := call(http.MethodPost, base+"beta", `{"enabled":true}`); code != http.StatusOK || a.Data.BetaEnabled == nil || !*a.Data.BetaEnabled {
		t.Fatalf("turning it on: %d, %+v", code, a.Data)
	}
	if code, a := call(http.MethodGet, base+"status", ""); code != http.StatusOK || a.Data.BetaEnabled == nil || !*a.Data.BetaEnabled {
		t.Errorf("the status with the switch on: %d, %+v", code, a.Data)
	}
	if reflect.DeepEqual(before, whatIsOnTheMachine(t, box.Folder)) {
		t.Error("turning the switch on left no flag file: the test cannot tell the switch from nothing")
	}
	if code, a := call(http.MethodPost, base+"beta", `{"enabled":false}`); code != http.StatusOK || a.Data.BetaEnabled == nil || *a.Data.BetaEnabled {
		t.Fatalf("turning it off: %d, %+v", code, a.Data)
	}
	if after := whatIsOnTheMachine(t, box.Folder); !reflect.DeepEqual(before, after) {
		t.Errorf("the machine is not as it was after the switch was turned on and off:\nbefore %q\nafter  %q", before, after)
	}
}
