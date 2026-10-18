package route

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

// The routes of the modules, exactly: the list and the status, which anyone with the token may read, and the plan, the install and the remove, which take the access token in the header.
// Nothing else is registered below the path, so that a route is never added to it without this list being changed.
var moduleRoutes = []string{
	"GET /v1/sys/modules", "POST /v1/sys/modules/:name/plan", "POST /v1/sys/modules/:name/install", "POST /v1/sys/modules/:name/remove", "GET /v1/sys/modules/:name/status",
}

const modulePlanID = "m1-0123456789abcdef0123456789abcdef"

func TestTheModuleRoutesAreTheseAndAreBehindTheToken(t *testing.T) {
	e, ok := InitV1Router().(*echo.Echo)
	if !ok {
		t.Fatal("InitV1Router() is not an *echo.Echo")
	}
	var registered []string
	for _, r := range e.Routes() {
		if strings.HasPrefix(r.Path, "/v1/sys/modules") {
			registered = append(registered, r.Method+" "+r.Path)
		}
	}
	want := slices.Clone(moduleRoutes)
	sort.Strings(registered)
	sort.Strings(want)
	if !slices.Equal(registered, want) {
		t.Errorf("registered:\n%s\nwant:\n%s", strings.Join(registered, "\n"), strings.Join(want, "\n"))
	}
	for _, route := range moduleRoutes {
		method, path, _ := strings.Cut(route, " ")
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, httptest.NewRequest(method, strings.Replace(path, ":name", "virt", 1), nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d, want 401", route, recorder.Code)
		}
	}
}

// fakeModuleSystem answers every call of the modules and records it, or refuses it with err.
type fakeModuleSystem struct {
	service.SystemService
	calls []string
	err   error
	// ended is whether the context that the last start was given had ended
	ended bool
	// statusKind is the kind of the job that the status tells of
	statusKind string
}

// status is the answer of the fake: a job of the kind that the call started (an install, for the status of a job that nobody asked for).
func (f *fakeModuleSystem) status(kind string) service.SystemModuleStatus {
	return service.SystemModuleStatus{Job: modulecmd.Job{Name: "virt", Kind: kind, State: modulecmd.JobRunning, LogTail: []string{}}, Status: modulecmd.StatusResult{Name: "virt"}}
}

func (f *fakeModuleSystem) ListSystemModules() (modulecmd.ListResult, error) {
	f.calls = append(f.calls, "list")
	return modulecmd.ListResult{Catalogue: modulecmd.CatalogueInfo{OK: true}, Modules: []modulecmd.ListModule{}}, f.err
}

func (f *fakeModuleSystem) PlanSystemModule(name string) (osmodule.PlanResult, error) {
	f.calls = append(f.calls, "plan "+name)
	return osmodule.PlanResult{Name: name, Checks: []osmodule.Check{}, PlanID: modulePlanID}, f.err
}

func (f *fakeModuleSystem) StartSystemModuleInstall(ctx context.Context, name, planID string) (service.SystemModuleStatus, error) {
	f.calls, f.ended = append(f.calls, "install "+name+" "+planID), ctx.Err() != nil
	return f.status(modulecmd.KindInstall), f.err
}

func (f *fakeModuleSystem) StartSystemModuleRemove(ctx context.Context, name string) (service.SystemModuleStatus, error) {
	f.calls, f.ended = append(f.calls, "remove "+name), ctx.Err() != nil
	return f.status(modulecmd.KindRemove), f.err
}

func (f *fakeModuleSystem) GetSystemModuleStatus(name string) (service.SystemModuleStatus, error) {
	f.calls = append(f.calls, "status "+name)
	return f.status(f.statusKind), f.err
}

func moduleCall(e http.Handler, method, target, body, authorization, remoteAddr string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if authorization != "" {
		request.Header.Set(echo.HeaderAuthorization, authorization)
	}
	if remoteAddr != "" {
		request.RemoteAddr = remoteAddr
	}
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)
	return recorder
}

// What changes the box, or runs apt, takes the access token in the header and no other: not the one in the query, which a log or a Referer leaks, not the refresh token, which lives a
// week; the services of the box come in with the secret, from the box. The list and the status ask no more than the group does.
func TestWhatActsOnTheModulesTakesTheAccessTokenInTheHeaderAndNoOther(t *testing.T) {
	system := &fakeModuleSystem{}
	access, refresh, secret := theTokensOfTheTests(t, system)
	e := InitV1Router()

	const base = "/v1/sys/modules/virt/"
	guarded := []struct {
		name, method, path, body string
		answer                   int
		call                     string
	}{
		{"plan", http.MethodPost, "plan", ``, http.StatusOK, "plan virt"},
		{"install", http.MethodPost, "install", `{"plan_id":"` + modulePlanID + `"}`, http.StatusAccepted, "install virt " + modulePlanID},
		{"remove", http.MethodPost, "remove", `{}`, http.StatusAccepted, "remove virt"},
	}
	for _, g := range guarded {
		for _, c := range []struct {
			name, target, auth, remote string
			want                       int
			reached                    bool
		}{
			{"the access token in the header", g.path, access, "", g.answer, true},
			{"a service of the box, with the secret", g.path, secret, "127.0.0.1:4000", g.answer, true},
			{"the secret from another host", g.path, secret, "192.168.1.20:4000", http.StatusUnauthorized, false},
			{"the access token in the query", g.path + "?token=" + access, "", "", http.StatusUnauthorized, false},
			{"the refresh token in the header", g.path, refresh, "", http.StatusUnauthorized, false},
			{"the refresh token in the query", g.path + "?token=" + refresh, "", "", http.StatusUnauthorized, false},
			{"a header that is not a token", g.path, "nonsense", "", http.StatusUnauthorized, false},
			{"nothing", g.path, "", "", http.StatusUnauthorized, false},
		} {
			system.calls = nil
			got := moduleCall(e, g.method, base+c.target, g.body, c.auth, c.remote).Code
			reached := slices.Contains(system.calls, g.call)
			if got != c.want || reached != c.reached {
				t.Errorf("%s, %s: status = %d, reached = %v (%v); want %d and %v", g.name, c.name, got, reached, system.calls, c.want, c.reached)
			}
		}
	}

	// the list and the status ask no more of the token than the rest of the group
	for _, g := range []struct{ name, target, call string }{{"list", "/v1/sys/modules", "list"}, {"status", base + "status", "status virt"}} {
		for _, c := range []struct {
			name, target, auth string
			want               int
		}{
			{"the access token in the header", g.target, access, http.StatusOK},
			{"the access token in the query", g.target + "?token=" + access, "", http.StatusOK},
			{"the refresh token in the header: the group takes it, as it takes it for the status of the upgrade", g.target, refresh, http.StatusOK},
			{"nothing", g.target, "", http.StatusUnauthorized},
			{"a header that is not a token", g.target, "nonsense", http.StatusUnauthorized},
		} {
			system.calls = nil
			got := moduleCall(e, http.MethodGet, c.target, "", c.auth, "").Code
			if got != c.want || (c.want == http.StatusOK) != slices.Contains(system.calls, g.call) {
				t.Errorf("%s, %s: status = %d (%v), want %d", g.name, c.name, got, system.calls, c.want)
			}
		}
	}

	// and no other method on these paths gets anywhere, with the best of tokens
	system.calls = nil
	for _, route := range moduleRoutes {
		_, path, _ := strings.Cut(route, " ")
		path = strings.Replace(path, ":name", "virt", 1)
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			if slices.Contains(moduleRoutes, method+" "+strings.Replace(path, "virt", ":name", 1)) {
				continue
			}
			if got := moduleCall(e, method, path, `{}`, access, "").Code; got != http.StatusNotFound && got != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 404 or 405", method, path, got)
			}
		}
	}
	if len(system.calls) != 0 {
		t.Errorf("the service was asked by methods that are not the routes' own: %v", system.calls)
	}
}

func decodeAnswer(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var answer map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the answer is not JSON: %v: %s", err, recorder.Body)
	}
	return answer
}

func TestTheAnswersOfTheModulesAreTheEnvelopeAndAreNotKept(t *testing.T) {
	system := &fakeModuleSystem{}
	access, _, _ := theTokensOfTheTests(t, system)
	e := InitV1Router()
	for _, c := range []struct {
		name, method, target, body string
		want                       int
		message                    string
	}{
		{"list", http.MethodGet, "/v1/sys/modules", "", http.StatusOK, ""},
		{"plan", http.MethodPost, "/v1/sys/modules/virt/plan", "", http.StatusOK, ""},
		{"install", http.MethodPost, "/v1/sys/modules/virt/install", `{"plan_id":"` + modulePlanID + `"}`, http.StatusAccepted, "accepted"},
		{"remove", http.MethodPost, "/v1/sys/modules/virt/remove", `{}`, http.StatusAccepted, "accepted"},
		{"status", http.MethodGet, "/v1/sys/modules/virt/status", "", http.StatusOK, ""},
	} {
		recorder := moduleCall(e, c.method, c.target, c.body, access, "")
		if recorder.Code != c.want || recorder.Header().Get(echo.HeaderCacheControl) != "no-store" {
			t.Errorf("%s: %d, cache-control %q", c.name, recorder.Code, recorder.Header().Get(echo.HeaderCacheControl))
		}
		answer := decodeAnswer(t, recorder)
		if int(answer["success"].(float64)) != c.want || answer["data"] == nil || (c.message != "" && answer["message"] != c.message) {
			t.Errorf("%s: %v", c.name, answer)
		}
	}
	// the status of a job: the job and the module
	data := decodeAnswer(t, moduleCall(e, http.MethodGet, "/v1/sys/modules/virt/status", "", access, ""))["data"].(map[string]any)
	job, _ := data["job"].(map[string]any)
	status, _ := data["status"].(map[string]any)
	for _, key := range []string{"name", "kind", "state", "step", "code", "undo", "kept_packages", "started_at", "ended_at", "log_tail"} {
		if _, ok := job[key]; !ok {
			t.Errorf("the job has no %s: %v", key, job)
		}
	}
	for _, key := range []string{"name", "installed", "version", "unit", "route"} {
		if _, ok := status[key]; !ok {
			t.Errorf("the status has no %s: %v", key, status)
		}
	}
}

// The name in the address is checked before the body is read, and before the service is asked.
func TestTheNameInTheAddressIsCheckedBeforeAnythingElse(t *testing.T) {
	system := &fakeModuleSystem{}
	access, _, _ := theTokensOfTheTests(t, system)
	e := InitV1Router()
	names := []string{"Virt", "vi%24rt", "vi%25rt", "vi.rt", "vi_rt", "1virt", "-virt", "virt%0A", "virt%00", "%2e%2e", "%e2%80%8bvirt", "v%C3%AFrt", strings.Repeat("a", 33), "vi%20rt", "VIRT"}
	for _, name := range names {
		for _, c := range []struct{ method, tail, body string }{
			{http.MethodPost, "plan", ``}, {http.MethodPost, "install", `not json at all`}, {http.MethodPost, "install", `{"plan_id":"` + modulePlanID + `"}`},
			{http.MethodPost, "remove", `{"unknown":1}`}, {http.MethodPost, "remove", `{}`}, {http.MethodGet, "status", ``},
		} {
			recorder := moduleCall(e, c.method, "/v1/sys/modules/"+name+"/"+c.tail, c.body, access, "")
			// (the router may say 404 for an address it cannot read, which is as good: it is never the service that answers)
			if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusNotFound {
				t.Errorf("%s %s: %d", name, c.tail, recorder.Code)
			}
			if recorder.Code == http.StatusBadRequest {
				data := decodeAnswer(t, recorder)["data"].(map[string]any)
				field, _ := data["data"].(map[string]any)
				if data["error_code"] != "bad_request" || field["field"] != "name" {
					t.Errorf("%s %s: %v", name, c.tail, data)
				}
			}
		}
	}
	if len(system.calls) != 0 {
		t.Errorf("the service was asked: %v", system.calls)
	}
	// and a good name with a bad body is the body's fault
	recorder := moduleCall(e, http.MethodPost, "/v1/sys/modules/virt/install", `{"plan_id":"x"`, access, "")
	field, _ := decodeAnswer(t, recorder)["data"].(map[string]any)["data"].(map[string]any)
	if recorder.Code != http.StatusBadRequest || field["field"] != "body" {
		t.Errorf("%d %v", recorder.Code, field)
	}
}

// The bodies are read as strictly as the engine's: one object, no field that is not the request's, no name twice, none spelled otherwise, at most 64 KiB.
func TestTheBodiesOfTheModulesAreStrictJSON(t *testing.T) {
	system := &fakeModuleSystem{}
	access, _, _ := theTokensOfTheTests(t, system)
	e := InitV1Router()
	good := `"plan_id":"` + modulePlanID + `"`
	for _, c := range []struct {
		name, target, body, field string
	}{
		{"install: an unknown field", "install", `{` + good + `,"yes":true}`, "body"},
		{"install: a field twice", "install", `{` + good + `,` + good + `}`, "body"},
		{"install: a field in capitals", "install", `{"PLAN_ID":"` + modulePlanID + `"}`, "body"},
		{"install: no plan", "install", `{}`, "plan_id"},
		{"install: an empty plan", "install", `{"plan_id":""}`, "plan_id"},
		{"install: a number for the plan", "install", `{"plan_id":5}`, "plan_id"},
		{"install: null for the plan", "install", `{"plan_id":null}`, "plan_id"},
		{"install: an array", "install", `[` + good + `]`, "body"},
		{"install: a string", "install", `"` + modulePlanID + `"`, "body"},
		{"install: null", "install", `null`, "body"},
		{"install: nothing", "install", ``, "body"},
		{"install: blanks", "install", " \n ", "body"},
		{"install: text after the object", "install", `{` + good + `} x`, "body"},
		{"install: two objects", "install", `{` + good + `}{}`, "body"},
		{"install: cut", "install", `{` + good, "body"},
		{"install: more than 64 KiB", "install", `{` + good + `,"x":"` + strings.Repeat("a", 64<<10) + `"}`, "body"},
		{"install: a plan of 64 KiB", "install", `{"plan_id":"` + strings.Repeat("a", 64<<10) + `"}`, "body"},
		{"remove: a field", "remove", `{"yes":true}`, "body"},
		{"remove: nothing", "remove", ``, "body"},
		{"remove: null", "remove", `null`, "body"},
		{"remove: an array", "remove", `[]`, "body"},
		{"remove: text after the object", "remove", `{} {}`, "body"},
		{"remove: more than 64 KiB", "remove", `{"x":"` + strings.Repeat("a", 64<<10) + `"}`, "body"},
	} {
		system.calls = nil
		recorder := moduleCall(e, http.MethodPost, "/v1/sys/modules/virt/"+c.target, c.body, access, "")
		data, _ := decodeAnswer(t, recorder)["data"].(map[string]any)
		field, _ := data["data"].(map[string]any)
		if recorder.Code != http.StatusBadRequest || data["error_code"] != "bad_request" || field["field"] != c.field || len(system.calls) != 0 {
			t.Errorf("%s: %d %v, the service was asked %v", c.name, recorder.Code, data, system.calls)
		}
		if strings.Contains(recorder.Body.String(), "aaaaaaaa") {
			t.Errorf("%s: the answer repeats what was sent", c.name)
		}
	}
	// and whitespace around the object is fine
	system.calls = nil
	if recorder := moduleCall(e, http.MethodPost, "/v1/sys/modules/virt/install", " \n{ "+good+" } \n", access, ""); recorder.Code != http.StatusAccepted || len(system.calls) != 1 {
		t.Errorf("%d %v", recorder.Code, system.calls)
	}
	if recorder := moduleCall(e, http.MethodPost, "/v1/sys/modules/virt/remove", "{ }", access, ""); recorder.Code != http.StatusAccepted {
		t.Errorf("%d", recorder.Code)
	}
}

func TestEveryRefusalOfTheModulesIsADataCodeAndTheRightStatus(t *testing.T) {
	logger.LogInitConsoleOnly()
	system := &fakeModuleSystem{}
	access, _, _ := theTokensOfTheTests(t, system)
	e := InitV1Router()
	codes := map[string]int{
		service.ModuleBadRequest: http.StatusBadRequest, service.ModuleStartFailed: http.StatusInternalServerError,
		service.ModuleNotRoot: http.StatusConflict, service.ModuleUnsupported: http.StatusConflict, service.ModuleBusy: http.StatusConflict,
		service.ModulePlanChanged: http.StatusConflict, service.ModuleNotInstalled: http.StatusConflict, service.ModulePlanTimeout: http.StatusConflict,
	}
	for _, code := range []string{
		osmodule.CheckCatalogue, osmodule.CheckOS, osmodule.CheckArch, osmodule.CheckReCasaOS, osmodule.CheckInstalled, osmodule.CheckForeignUnit, osmodule.CheckBusy,
		osmodule.RefusedDiskShort, osmodule.RefusedDiskUnknown, osmodule.RefusedAptMissing, osmodule.RefusedAptFailed, osmodule.RefusedAptRemoves, osmodule.RefusedAptDowngrades,
		osmodule.RefusedHeld, osmodule.RefusedPackageNotExact,
	} {
		codes[code] = http.StatusConflict
	}
	for code, status := range codes {
		system.err = &service.ModuleRefusal{Code: code, Detail: "English " + code, Data: map[string]any{"reason": "because"}}
		for _, c := range []struct{ method, target, body string }{
			{http.MethodPost, "/v1/sys/modules/virt/plan", ``},
			{http.MethodPost, "/v1/sys/modules/virt/install", `{"plan_id":"` + modulePlanID + `"}`},
			{http.MethodPost, "/v1/sys/modules/virt/remove", `{}`},
			{http.MethodGet, "/v1/sys/modules/virt/status", ``},
			{http.MethodGet, "/v1/sys/modules", ``},
		} {
			recorder := moduleCall(e, c.method, c.target, c.body, access, "")
			answer := decodeAnswer(t, recorder)
			data, _ := answer["data"].(map[string]any)
			inner, _ := data["data"].(map[string]any)
			if recorder.Code != status || int(answer["success"].(float64)) != status || data["error_code"] != code || data["error"] != "English "+code || answer["message"] != "English "+code || inner["reason"] != "because" {
				t.Errorf("%s %s with %s: %d %v", c.method, c.target, code, recorder.Code, answer)
			}
			if recorder.Header().Get(echo.HeaderCacheControl) != "no-store" {
				t.Errorf("%s: cache-control %q", code, recorder.Header().Get(echo.HeaderCacheControl))
			}
		}
	}
	// what is not a refusal says only that it is internal: the error may hold a path of the machine
	system.err = errors.New("open /var/log/casaos/module-virt.log: permission denied")
	for _, target := range []string{"/v1/sys/modules", "/v1/sys/modules/virt/status"} {
		recorder := moduleCall(e, http.MethodGet, target, "", access, "")
		if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "permission") || !strings.Contains(recorder.Body.String(), `"error_code":"internal"`) {
			t.Errorf("%s: %d %s", target, recorder.Code, recorder.Body)
		}
	}
}

// The core with nothing to offer: the real service on a box that has no catalogue says so, and the routes that start nothing answer.
func TestTheListOfARealServiceSaysWhatItHasToOffer(t *testing.T) {
	access, _, _ := theTokensOfTheTests(t, service.NewSystemService())
	e := InitV1Router()
	recorder := moduleCall(e, http.MethodGet, "/v1/sys/modules", "", access, "")
	data, _ := decodeAnswer(t, recorder)["data"].(map[string]any)
	catalogue, _ := data["catalogue"].(map[string]any)
	modules, isList := data["modules"].([]any)
	if recorder.Code != http.StatusOK || catalogue == nil || !isList {
		t.Fatalf("%d %s", recorder.Code, recorder.Body)
	}
	if ok, _ := catalogue["ok"].(bool); !ok && len(modules) != 0 {
		t.Errorf("modules in a catalogue that is not ok: %v", data)
	}
}

// What the service is asked is the name in the address and the plan in the body, and nothing else.
func TestTheServiceIsAskedWhatTheRequestSays(t *testing.T) {
	system := &fakeModuleSystem{}
	access, _, _ := theTokensOfTheTests(t, system)
	e := InitV1Router()
	for _, c := range []struct{ method, target, body, want string }{
		{http.MethodPost, "/v1/sys/modules/tool-2/plan", ``, "plan tool-2"},
		{http.MethodPost, "/v1/sys/modules/tool-2/install", `{"plan_id":"` + modulePlanID + `"}`, "install tool-2 " + modulePlanID},
		{http.MethodPost, "/v1/sys/modules/tool-2/remove", `{}`, "remove tool-2"},
		{http.MethodGet, "/v1/sys/modules/tool-2/status", ``, "status tool-2"},
		{http.MethodGet, "/v1/sys/modules", ``, "list"},
	} {
		system.calls = nil
		if recorder := moduleCall(e, c.method, c.target, c.body, access, ""); recorder.Code >= 300 || len(system.calls) != 1 || system.calls[0] != c.want {
			t.Errorf("%s: %d, the service was asked %q, want %q", c.target, recorder.Code, system.calls, c.want)
		}
	}
}

// A start waits for the starts before it, and gives up when the request that asks it does: the service is given the context of the request.
func TestAStartIsGivenTheContextOfItsRequest(t *testing.T) {
	system := &fakeModuleSystem{}
	access, _, _ := theTokensOfTheTests(t, system)
	e := InitV1Router()
	for _, c := range []struct{ name, target, body string }{
		{"install", "/v1/sys/modules/virt/install", `{"plan_id":"` + modulePlanID + `"}`},
		{"remove", "/v1/sys/modules/virt/remove", `{}`},
	} {
		for _, over := range []bool{false, true} {
			ctx, cancel := context.WithCancel(context.Background())
			if over {
				cancel()
			}
			request := httptest.NewRequest(http.MethodPost, c.target, strings.NewReader(c.body)).WithContext(ctx)
			request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			request.Header.Set(echo.HeaderAuthorization, access)
			system.calls, system.ended = nil, !over
			e.ServeHTTP(httptest.NewRecorder(), request)
			cancel()
			if len(system.calls) != 1 || system.ended != over {
				t.Errorf("%s, request over = %v: the service was asked %v, with a context that had ended = %v", c.name, over, system.calls, system.ended)
			}
		}
	}
}
