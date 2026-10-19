package route

import (
	"net/http"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
)

// The job in the answers says what it is: the install and the remove tell their own kind, and the status tells the kind of the job it reads ("" when the log does not say).
func TestTheJobOfEveryAnswerSaysWhetherItIsAnInstallOrARemove(t *testing.T) {
	system := &fakeModuleSystem{}
	access, _, _ := theTokensOfTheTests(t, system)
	e := InitV1Router()
	kindOf := func(method, target, body string) any {
		t.Helper()
		recorder := moduleCall(e, method, target, body, access, "")
		data, _ := decodeAnswer(t, recorder)["data"].(map[string]any)
		job, _ := data["job"].(map[string]any)
		kind, there := job["kind"]
		if !there {
			t.Fatalf("%s %s: the job has no kind: %s", method, target, recorder.Body)
		}
		return kind
	}
	if got := kindOf(http.MethodPost, "/v1/sys/modules/virt/install", `{"plan_id":"`+modulePlanID+`"}`); got != "install" {
		t.Errorf("install: %#v", got)
	}
	if got := kindOf(http.MethodPost, "/v1/sys/modules/virt/install", `{"plan_id":"`+modulePlanID+`","mode":"update"}`); got != "update" {
		t.Errorf("update: %#v", got)
	}
	if got := kindOf(http.MethodPost, "/v1/sys/modules/virt/remove", `{}`); got != "remove" {
		t.Errorf("remove: %#v", got)
	}
	for _, kind := range []string{modulecmd.KindInstall, modulecmd.KindRemove, modulecmd.KindUpdate, ""} {
		system.statusKind = kind
		if got := kindOf(http.MethodGet, "/v1/sys/modules/virt/status", ""); got != kind {
			t.Errorf("status of a job of kind %q: %#v", kind, got)
		}
	}
}
