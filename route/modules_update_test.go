package route

import (
	"net/http"
	"strings"
	"testing"
)

// The plan and the start of a job take a mode: install (when there is none) or update. The routes are the same; the body says which.
func TestTheModeOfAPlanAndOfAStartChoosesBetweenAnInstallAndAnUpdate(t *testing.T) {
	system := &fakeModuleSystem{}
	access, _, _ := theTokensOfTheTests(t, system)
	e := InitV1Router()
	id := `"plan_id":"` + modulePlanID + `"`
	for _, c := range []struct {
		name, target, body string
		want               int
		call               string
		field              string
	}{
		{"a plan with no body", "plan", ``, http.StatusOK, "plan virt", ""},
		{"a plan with blanks", "plan", " \n ", http.StatusOK, "plan virt", ""},
		{"a plan with an empty object", "plan", `{}`, http.StatusOK, "plan virt", ""},
		{"a plan of an install", "plan", `{"mode":"install"}`, http.StatusOK, "plan virt", ""},
		{"a plan of an update", "plan", `{"mode":"update"}`, http.StatusOK, "plan-update virt", ""},
		{"a plan of an update, with blanks around", "plan", " {\n \"mode\" : \"update\" } ", http.StatusOK, "plan-update virt", ""},
		{"a plan with null for the mode", "plan", `{"mode":null}`, http.StatusOK, "plan virt", ""},
		{"a plan with a mode that is none", "plan", `{"mode":"upgrade"}`, http.StatusBadRequest, "", "mode"},
		{"a plan with a mode in capitals", "plan", `{"mode":"UPDATE"}`, http.StatusBadRequest, "", "mode"},
		{"a plan with an empty mode", "plan", `{"mode":""}`, http.StatusBadRequest, "", "mode"},
		{"a plan with a number for the mode", "plan", `{"mode":1}`, http.StatusBadRequest, "", "mode"},
		{"a plan with a field that is not one", "plan", `{"mode":"update","x":1}`, http.StatusBadRequest, "", "body"},
		{"a plan with the mode twice", "plan", `{"mode":"update","mode":"install"}`, http.StatusBadRequest, "", "body"},
		{"a plan with a body that is not an object", "plan", `"update"`, http.StatusBadRequest, "", "body"},
		{"a plan with text after the object", "plan", `{"mode":"update"} x`, http.StatusBadRequest, "", "body"},
		{"an install with no mode", "install", `{` + id + `}`, http.StatusAccepted, "install virt " + modulePlanID, ""},
		{"an install with the mode install", "install", `{` + id + `,"mode":"install"}`, http.StatusAccepted, "install virt " + modulePlanID, ""},
		{"an update", "install", `{` + id + `,"mode":"update"}`, http.StatusAccepted, "update virt " + modulePlanID, ""},
		{"an update, the mode first", "install", `{"mode":"update",` + id + `}`, http.StatusAccepted, "update virt " + modulePlanID, ""},
		{"an update with no plan", "install", `{"mode":"update"}`, http.StatusBadRequest, "", "plan_id"},
		{"a start with a mode that is none", "install", `{` + id + `,"mode":"remove"}`, http.StatusBadRequest, "", "mode"},
		{"a start with an empty mode", "install", `{` + id + `,"mode":""}`, http.StatusBadRequest, "", "mode"},
		{"a start with the mode twice", "install", `{` + id + `,"mode":"update","mode":"update"}`, http.StatusBadRequest, "", "body"},
	} {
		system.calls = nil
		recorder := moduleCall(e, http.MethodPost, "/v1/sys/modules/virt/"+c.target, c.body, access, "")
		if recorder.Code != c.want {
			t.Errorf("%s: %d %s", c.name, recorder.Code, recorder.Body)
			continue
		}
		data, _ := decodeAnswer(t, recorder)["data"].(map[string]any)
		switch {
		case c.want == http.StatusBadRequest:
			field, _ := data["data"].(map[string]any)
			if data["error_code"] != "bad_request" || field["field"] != c.field || len(system.calls) != 0 {
				t.Errorf("%s: %v, the service was asked %v", c.name, data, system.calls)
			}
		case len(system.calls) != 1 || system.calls[0] != c.call:
			t.Errorf("%s: the service was asked %v, want %q", c.name, system.calls, c.call)
		}
	}
	// the update answers with a job of its kind, and is not kept
	recorder := moduleCall(e, http.MethodPost, "/v1/sys/modules/virt/install", `{`+id+`,"mode":"update"}`, access, "")
	job, _ := decodeAnswer(t, recorder)["data"].(map[string]any)["job"].(map[string]any)
	if job["kind"] != "update" || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("%v", recorder.Header())
	}
	// and a plan that is refused still names its mode
	if plan := decodeAnswer(t, moduleCall(e, http.MethodPost, "/v1/sys/modules/virt/plan", `{"mode":"update"}`, access, ""))["data"].(map[string]any); plan["mode"] != "update" {
		t.Errorf("%v", plan)
	}
	if !strings.Contains(moduleCall(e, http.MethodPost, "/v1/sys/modules/virt/plan", ``, access, "").Body.String(), `"plan_id"`) {
		t.Error("no plan id")
	}
}
