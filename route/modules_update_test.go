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
		{"a plan with null for the mode", "plan", `{"mode":null}`, http.StatusBadRequest, "", "mode"},
		{"a start with null for the mode", "install", `{` + id + `,"mode":null}`, http.StatusBadRequest, "", "mode"},
		{"a start with a number for the mode", "install", `{` + id + `,"mode":1}`, http.StatusBadRequest, "", "mode"},
		{"a start with the mode spelled otherwise", "install", `{` + id + `,"MODE":"update"}`, http.StatusBadRequest, "", "body"},
		{"a plan with a mode that is a number", "plan", `{"mode":1}`, http.StatusBadRequest, "", "mode"},
		{"a plan with a mode that is none", "plan", `{"mode":"upgrade"}`, http.StatusBadRequest, "", "mode"},
		{"a plan with a mode in capitals", "plan", `{"mode":"UPDATE"}`, http.StatusBadRequest, "", "mode"},
		{"a plan with an empty mode", "plan", `{"mode":""}`, http.StatusBadRequest, "", "mode"},
		{"a plan with a number for the mode", "plan", `{"mode":1}`, http.StatusBadRequest, "", "mode"},
		{"a plan with another field beside the mode", "plan", `{"mode":"update","x":1}`, http.StatusOK, "plan-update virt", ""},
		{"a plan with the mode twice", "plan", `{"mode":"update","mode":"install"}`, http.StatusBadRequest, "", "mode"},
		{"a plan with the mode twice, the same", "plan", `{"mode":"update","mode":"update"}`, http.StatusBadRequest, "", "mode"},
		{"a plan with a body that is a string: ignored, as it always was", "plan", `"update"`, http.StatusOK, "plan virt", ""},
		{"a plan with a body that is an array: ignored", "plan", `["update"]`, http.StatusOK, "plan virt", ""},
		{"a plan with text that is no JSON: ignored", "plan", `mode=update`, http.StatusOK, "plan virt", ""},
		{"a plan with text after the object: ignored", "plan", `{"mode":"update"} x`, http.StatusOK, "plan virt", ""},
		{"a plan with an object that has no mode: ignored", "plan", `{"plan_id":"x","yes":true}`, http.StatusOK, "plan virt", ""},
		{"a plan with the mode spelled otherwise: no mode key, ignored", "plan", `{"MODE":"update"}`, http.StatusOK, "plan virt", ""},
		{"a plan with a body of more than 64 KiB: ignored", "plan", `{"mode":"update","x":"` + strings.Repeat("a", 64<<10) + `"}`, http.StatusOK, "plan virt", ""},
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

// The plan and the start read the mode in the one way: the key is "mode", exactly; its value is the string install or the string update; null, a number and another word are a field that is
// not valid; and a body that has no such key is an install (for the plan, which ignores what it does not know) or is refused as a body (for the start, which is strict).
func TestThePlanAndTheStartReadTheModeInTheSameWay(t *testing.T) {
	system := &fakeModuleSystem{}
	access, _, _ := theTokensOfTheTests(t, system)
	e := InitV1Router()
	id := `"plan_id":"` + modulePlanID + `"`
	for _, c := range []struct {
		name, planBody, startBody string
		plan, start               string // the call the service gets, or "400 <field>"
	}{
		{"no body", ``, ``, "plan virt", "400 body"},
		{"an empty object", `{}`, `{}`, "plan virt", "400 plan_id"},
		{"junk", `not json`, `not json`, "plan virt", "400 body"},
		{"a key that is not one", `{"foo":1}`, `{` + id + `,"foo":1}`, "plan virt", "400 body"},
		{"install", `{"mode":"install"}`, `{` + id + `,"mode":"install"}`, "plan virt", "install virt " + modulePlanID},
		{"update", `{"mode":"update"}`, `{` + id + `,"mode":"update"}`, "plan-update virt", "update virt " + modulePlanID},
		{"another word", `{"mode":"x"}`, `{` + id + `,"mode":"x"}`, "400 mode", "400 mode"},
		{"null", `{"mode":null}`, `{` + id + `,"mode":null}`, "400 mode", "400 mode"},
		{"a number", `{"mode":0}`, `{` + id + `,"mode":0}`, "400 mode", "400 mode"},
		{"the key in capitals", `{"MODE":"update"}`, `{` + id + `,"MODE":"update"}`, "plan virt", "400 body"},
	} {
		for _, r := range []struct{ route, body, want string }{{"plan", c.planBody, c.plan}, {"install", c.startBody, c.start}} {
			system.calls = nil
			recorder := moduleCall(e, http.MethodPost, "/v1/sys/modules/virt/"+r.route, r.body, access, "")
			if field, bad := strings.CutPrefix(r.want, "400 "); bad {
				data, _ := decodeAnswer(t, recorder)["data"].(map[string]any)
				got, _ := data["data"].(map[string]any)
				if recorder.Code != http.StatusBadRequest || got["field"] != field || len(system.calls) != 0 {
					t.Errorf("%s, %s: %d %v, asked %v; want 400 on %s", c.name, r.route, recorder.Code, data, system.calls, field)
				}
				continue
			}
			if recorder.Code >= 300 || len(system.calls) != 1 || system.calls[0] != r.want {
				t.Errorf("%s, %s: %d, asked %v; want %q", c.name, r.route, recorder.Code, system.calls, r.want)
			}
		}
	}
}
