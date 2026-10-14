package route

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/utils/jwt"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

// The routes of the upgrade of the operating system, exactly: the status that anyone with the token may read, the six calls that act (all of them behind the token in the header),
// the runbook with the plan's, and the plan's own. Nothing else is registered below the path, so that a route is never added to it without this list being changed, and the
// refresh, which is not in this change, is not there.
var osUpgradeRoutes = []string{
	"GET /v1/sys/os/upgrade/plan", "POST /v1/sys/os/upgrade/plan", "GET /v1/sys/os/upgrade/runbook",
	"GET /v1/sys/os/upgrade/status", "POST /v1/sys/os/upgrade/start", "POST /v1/sys/os/upgrade/reboot", "POST /v1/sys/os/upgrade/resume",
	"POST /v1/sys/os/upgrade/abort", "POST /v1/sys/os/upgrade/ack", "POST /v1/sys/os/upgrade/beta",
}

func TestTheOSUpgradeRoutesAreTheseAndAreBehindTheToken(t *testing.T) {
	e, ok := InitV1Router().(*echo.Echo)
	if !ok {
		t.Fatal("InitV1Router() is not an *echo.Echo")
	}
	var registered []string
	for _, r := range e.Routes() {
		if strings.HasPrefix(r.Path, "/v1/sys/os/upgrade") {
			registered = append(registered, r.Method+" "+r.Path)
		}
	}
	want := slices.Clone(osUpgradeRoutes)
	sort.Strings(registered)
	sort.Strings(want)
	if !slices.Equal(registered, want) {
		t.Errorf("registered:\n%s\nwant:\n%s", strings.Join(registered, "\n"), strings.Join(want, "\n"))
	}
	for _, route := range osUpgradeRoutes {
		method, path, _ := strings.Cut(route, " ")
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d, want 401", route, recorder.Code)
		}
	}
}

// fakeOSUpgradeSystem is the engine that answers every call and counts them.
type fakeOSUpgradeSystem struct {
	service.SystemService
	calls []string
}

func (f *fakeOSUpgradeSystem) status() service.OSUpgradeStatus {
	return service.OSUpgradeStatus{BetaEnabled: true, State: "idle", Hop: "debian-11-12", Actions: []string{}, LogTail: []string{}}
}

func (f *fakeOSUpgradeSystem) GetSystemOSUpgradeStatus() service.OSUpgradeStatus {
	f.calls = append(f.calls, "status")
	return f.status()
}

func (f *fakeOSUpgradeSystem) StartSystemOSUpgrade(service.OSUpgradeStartRequest) (service.OSUpgradeStatus, error) {
	f.calls = append(f.calls, "start")
	return f.status(), nil
}

func (f *fakeOSUpgradeSystem) RebootSystemOSUpgrade() (service.OSUpgradeStatus, error) {
	f.calls = append(f.calls, "reboot")
	return f.status(), nil
}

func (f *fakeOSUpgradeSystem) ResumeSystemOSUpgrade() (service.OSUpgradeStatus, error) {
	f.calls = append(f.calls, "resume")
	return f.status(), nil
}

func (f *fakeOSUpgradeSystem) AbortSystemOSUpgrade() (service.OSUpgradeStatus, error) {
	f.calls = append(f.calls, "abort")
	return f.status(), nil
}

func (f *fakeOSUpgradeSystem) AckSystemOSUpgrade(service.OSUpgradeAckRequest) (service.OSUpgradeStatus, error) {
	f.calls = append(f.calls, "ack")
	return f.status(), nil
}

func (f *fakeOSUpgradeSystem) SetSystemOSUpgradeBeta(bool) error {
	f.calls = append(f.calls, "beta")
	return nil
}

func (f *fakeOSUpgradeSystem) GetSystemOSUpgradeRunbook() (*service.OSUpgradeRunbook, error) {
	f.calls = append(f.calls, "run runbook")
	return &service.OSUpgradeRunbook{PlanID: "p1-0123456789abcdef0123456789abcdef", Hop: "debian-11-12", Text: "x", Blocks: []osplan.RunbookBlock{}, FromRun: true}, nil
}

func (f *fakeOSUpgradeSystem) GetSystemOSRunbook() (*osplan.Runbook, error) {
	f.calls = append(f.calls, "plan runbook")
	return &osplan.Runbook{PlanID: "p1-0123456789abcdef0123456789abcdef", Hop: "debian-11-12", Text: "x", Blocks: []osplan.RunbookBlock{}}, nil
}

func (f *fakeOSUpgradeSystem) StartSystemOSPlan() (service.SystemOSPlanStatus, error) {
	f.calls = append(f.calls, "plan start")
	return service.SystemOSPlanStatus{Plannable: true, State: "running", Phase: "sources"}, nil
}

func (f *fakeOSUpgradeSystem) GetSystemOSPlan() service.SystemOSPlanStatus {
	f.calls = append(f.calls, "plan status")
	return service.SystemOSPlanStatus{Plannable: true, State: "idle"}
}

// theTokensOfTheTests makes the real router recognise real tokens: the key is served by a user-service of the test's, and the box's own secret is written where the
// services of the box look for it. It gives the access token, the refresh token and the secret, and puts the fake engine in the place of the service.
func theTokensOfTheTests(t *testing.T, system *fakeOSUpgradeSystem) (access, refresh, secret string) {
	t.Helper()
	runtimePath := t.TempDir()
	private, public := signingKeys(t)
	jwks, err := jwt.GenerateJwksJSON(public)
	if err != nil {
		t.Fatal(err)
	}
	userService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(jwks) }))
	t.Cleanup(userService.Close)
	if err := os.WriteFile(filepath.Join(runtimePath, external.UserServiceAddressFilename), []byte(userService.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := external.WriteInternalSecret(runtimePath); err != nil {
		t.Fatal(err)
	}
	secret = external.InternalAuthorization(runtimePath)
	if secret == "" {
		t.Fatal("no internal secret")
	}
	previousConfig, previousService := config.CommonInfo, service.MyService
	config.CommonInfo = &model.CommonModel{RuntimePath: runtimePath}
	service.MyService = fakeUpdateRepository{system: system}
	t.Cleanup(func() { config.CommonInfo, service.MyService = previousConfig, previousService })
	if access, err = jwt.GetAccessToken("owner", private, 1); err != nil {
		t.Fatal(err)
	}
	if refresh, err = jwt.GetRefreshToken("owner", private, 1); err != nil {
		t.Fatal(err)
	}
	return access, refresh, secret
}

// What acts takes the access token in the header and no other: not the one in the query, which a log or a Referer leaks, not the refresh token, which lives a week; the services
// of the box come in with the secret, from the box. The status asks no more than the group does. One row for each call, so that a call that loses its guard is a row that fails.
func TestWhatActsOnTheUpgradeTakesTheAccessTokenInTheHeaderAndNoOther(t *testing.T) {
	system := &fakeOSUpgradeSystem{}
	access, refresh, secret := theTokensOfTheTests(t, system)
	e := InitV1Router()
	call := func(method, target, body, authorization, remoteAddr string) int {
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
		return recorder.Code
	}

	const base = "/v1/sys/os/upgrade/"
	guarded := []struct {
		name, method, path, body string
		answer                   int
		call                     string
	}{
		{"start", http.MethodPost, "start", `{"plan_id":"p1-0123456789abcdef0123456789abcdef","hostname":"box1"}`, http.StatusAccepted, "start"},
		{"reboot", http.MethodPost, "reboot", `{}`, http.StatusAccepted, "reboot"},
		{"resume", http.MethodPost, "resume", `{}`, http.StatusAccepted, "resume"},
		{"abort", http.MethodPost, "abort", `{}`, http.StatusAccepted, "abort"},
		{"ack", http.MethodPost, "ack", `{"action":"acknowledge"}`, http.StatusOK, "ack"},
		{"beta", http.MethodPost, "beta", `{"enabled":true}`, http.StatusOK, "beta"},
		{"runbook of the run", http.MethodGet, "runbook?run=current", ``, http.StatusOK, "run runbook"},
		{"runbook of the plan", http.MethodGet, "runbook", ``, http.StatusOK, "plan runbook"},
		{"start of the plan", http.MethodPost, "plan", ``, http.StatusAccepted, "plan start"},
	}
	for _, g := range guarded {
		withQuery := func(token string) string {
			if strings.Contains(g.path, "?") {
				return g.path + "&token=" + token
			}
			return g.path + "?token=" + token
		}
		for _, c := range []struct {
			name, target, auth, remote string
			want                       int
			reached                    bool
		}{
			{"the access token in the header", g.path, access, "", g.answer, true},
			{"a service of the box, with the secret", g.path, secret, "127.0.0.1:4000", g.answer, true},
			{"the secret from another host", g.path, secret, "192.168.1.20:4000", http.StatusUnauthorized, false},
			{"the access token in the query", withQuery(access), "", "", http.StatusUnauthorized, false},
			{"the refresh token in the header", g.path, refresh, "", http.StatusUnauthorized, false},
			{"the refresh token in the query", withQuery(refresh), "", "", http.StatusUnauthorized, false},
			{"a header that is not a token", g.path, "nonsense", "", http.StatusUnauthorized, false},
			{"nothing", g.path, "", "", http.StatusUnauthorized, false},
		} {
			system.calls = nil
			got := call(g.method, base+c.target, g.body, c.auth, c.remote)
			reached := slices.Contains(system.calls, g.call)
			if got != c.want || reached != c.reached {
				t.Errorf("%s, %s: status = %d, reached = %v (%v); want %d and %v", g.name, c.name, got, reached, system.calls, c.want, c.reached)
			}
		}
	}

	// the status asks no more of the token than the rest of the group
	for _, c := range []struct {
		name, target, auth string
		want               int
	}{
		{"the access token in the header", "status", access, http.StatusOK},
		{"the access token in the query", "status?token=" + access, "", http.StatusOK},
		{"the refresh token in the header: the group takes it, as it takes it for the plan's status", "status", refresh, http.StatusOK},
		{"nothing", "status", "", http.StatusUnauthorized},
	} {
		if got := call(http.MethodGet, base+c.target, "", c.auth, ""); got != c.want {
			t.Errorf("status, %s: %d, want %d", c.name, got, c.want)
		}
	}

	// and no other method on these paths gets anywhere, with the best of tokens
	system.calls = nil
	for _, route := range osUpgradeRoutes {
		_, path, _ := strings.Cut(route, " ")
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			if slices.Contains(osUpgradeRoutes, method+" "+path) {
				continue
			}
			if got := call(method, path, `{}`, access, ""); got >= 200 && got < 300 {
				t.Errorf("%s %s = %d", method, path, got)
			}
		}
	}
	if len(system.calls) != 0 {
		t.Errorf("the engine was asked by methods that are not the routes' own: %v", system.calls)
	}
}
