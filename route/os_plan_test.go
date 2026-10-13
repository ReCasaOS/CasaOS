package route

import (
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/utils/jwt"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

// The plan of the upgrade is a sys route behind the token like the others; what starts one, and what says the lines of the box's
// own sources, takes the access token in the header and no other; and nothing answers on these paths but what is registered.
func TestTheOSPlanRoutesAreSysRoutesBehindTheToken(t *testing.T) {
	e, ok := InitV1Router().(*echo.Echo)
	if !ok {
		t.Fatal("InitV1Router() is not an *echo.Echo")
	}
	registered := map[string]bool{}
	for _, r := range e.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, route := range [][2]string{
		{http.MethodPost, "/v1/sys/os/upgrade/plan"},
		{http.MethodGet, "/v1/sys/os/upgrade/plan"},
		{http.MethodGet, "/v1/sys/os/upgrade/runbook"},
	} {
		method, path := route[0], route[1]
		if !registered[method+" "+path] {
			t.Errorf("%s %s is not registered", method, path)
		}
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d, want 401", method, path, recorder.Code)
		}
	}
	for _, route := range [][2]string{
		{http.MethodPut, "/v1/sys/os/upgrade/plan"}, {http.MethodDelete, "/v1/sys/os/upgrade/plan"}, {http.MethodPatch, "/v1/sys/os/upgrade/plan"},
		{http.MethodPost, "/v1/sys/os/upgrade/runbook"}, {http.MethodPut, "/v1/sys/os/upgrade/runbook"}, {http.MethodDelete, "/v1/sys/os/upgrade/runbook"},
	} {
		if registered[route[0]+" "+route[1]] {
			t.Errorf("%s %s is registered: the plan is made and read, and nothing else", route[0], route[1])
		}
	}
}

// The handler that ends a plan before it ends the core is wired as POST /v1/sys/stop and as nothing else: a test of the handler
// alone passes when the route is not wired, or is called by another name (the route that ends the core is /sys/stop, and no /sys/restart exists).
func TestTheStopRouteIsPostKillCasaOS(t *testing.T) {
	e, ok := InitV1Router().(*echo.Echo)
	if !ok {
		t.Fatal("InitV1Router() is not an *echo.Echo")
	}
	var wired []string
	for _, r := range e.Routes() {
		if strings.HasSuffix(r.Name, ".PostKillCasaOS") {
			wired = append(wired, r.Method+" "+r.Path)
		}
		if strings.HasSuffix(r.Path, "/sys/restart") {
			t.Errorf("%s %s is registered: the route of the handler is /v1/sys/stop", r.Method, r.Path)
		}
	}
	if len(wired) != 1 || wired[0] != "POST /v1/sys/stop" {
		t.Errorf("PostKillCasaOS is wired as %v, want POST /v1/sys/stop", wired)
	}
}

var (
	signingKeysOnce sync.Once
	signingPrivate  *ecdsa.PrivateKey
	signingPublic   *ecdsa.PublicKey
	signingErr      error
)

// signingKeys is the one key pair of the tests of this package that use real tokens: the core keeps the public key of the user
// service for ten seconds, for the whole process (external.GetPublicKey), so that a second test with a key of its own finds the
// first one's and its tokens are refused.
func signingKeys(t *testing.T) (*ecdsa.PrivateKey, *ecdsa.PublicKey) {
	t.Helper()
	signingKeysOnce.Do(func() { signingPrivate, signingPublic, signingErr = jwt.GenerateKeyPair() })
	if signingErr != nil {
		t.Fatal(signingErr)
	}
	return signingPrivate, signingPublic
}

type fakeOSPlanSystem struct {
	service.SystemService
	starts, reads, runbooks int
}

func (f *fakeOSPlanSystem) StartSystemOSPlan() (service.SystemOSPlanStatus, error) {
	f.starts++
	return service.SystemOSPlanStatus{Plannable: true, State: "running", Phase: "sources"}, nil
}

func (f *fakeOSPlanSystem) GetSystemOSPlan() service.SystemOSPlanStatus {
	f.reads++
	return service.SystemOSPlanStatus{Plannable: true, State: "idle"}
}

func (f *fakeOSPlanSystem) GetSystemOSRunbook() (*osplan.Runbook, error) {
	f.runbooks++
	return &osplan.Runbook{PlanID: "p1-0123456789abcdef0123456789abcdef", Hop: "debian-11-12", Text: "x", Blocks: []osplan.RunbookBlock{}}, nil
}

// The real router, with real tokens: the key is served by a user-service of the test's (see the Docker update's test).
func TestTheOSPlanStartsAndSaysTheRunbookOnlyForTheAccessTokenInTheHeader(t *testing.T) {
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
	secret := external.InternalAuthorization(runtimePath)
	if secret == "" {
		t.Fatal("no internal secret")
	}
	previousConfig := config.CommonInfo
	config.CommonInfo = &model.CommonModel{RuntimePath: runtimePath}
	previousService := service.MyService
	system := &fakeOSPlanSystem{}
	service.MyService = fakeUpdateRepository{system: system}
	t.Cleanup(func() { config.CommonInfo, service.MyService = previousConfig, previousService })

	access, err := jwt.GetAccessToken("owner", private, 1)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := jwt.GetRefreshToken("owner", private, 1)
	if err != nil {
		t.Fatal(err)
	}
	e := InitV1Router()
	call := func(method, target, authorization, remoteAddr string) int {
		request := httptest.NewRequest(method, target, strings.NewReader(""))
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

	const plan, runbook = "/v1/sys/os/upgrade/plan", "/v1/sys/os/upgrade/runbook"
	for _, c := range []struct {
		name, method, target, auth, remote string
		want, starts, runbooks             int
	}{
		{"start: the access token in the header", http.MethodPost, plan, access, "", http.StatusAccepted, 1, 0},
		{"start: a service of the box, with the secret", http.MethodPost, plan, secret, "127.0.0.1:4000", http.StatusAccepted, 1, 0},
		{"start: the secret from another host", http.MethodPost, plan, secret, "192.168.1.20:4000", http.StatusUnauthorized, 0, 0},
		{"start: the access token in the query", http.MethodPost, plan + "?token=" + access, "", "", http.StatusUnauthorized, 0, 0},
		{"start: the refresh token in the header", http.MethodPost, plan, refresh, "", http.StatusUnauthorized, 0, 0},
		{"start: the refresh token in the query", http.MethodPost, plan + "?token=" + refresh, "", "", http.StatusUnauthorized, 0, 0},
		{"start: a header that is not a token", http.MethodPost, plan, "nonsense", "", http.StatusUnauthorized, 0, 0},
		{"start: nothing", http.MethodPost, plan, "", "", http.StatusUnauthorized, 0, 0},
		{"runbook: the access token in the header", http.MethodGet, runbook, access, "", http.StatusOK, 0, 1},
		{"runbook: a service of the box, with the secret", http.MethodGet, runbook, secret, "127.0.0.1:4000", http.StatusOK, 0, 1},
		{"runbook: the access token in the query", http.MethodGet, runbook + "?token=" + access, "", "", http.StatusUnauthorized, 0, 0},
		{"runbook: the refresh token in the header", http.MethodGet, runbook, refresh, "", http.StatusUnauthorized, 0, 0},
		{"runbook: nothing", http.MethodGet, runbook, "", "", http.StatusUnauthorized, 0, 0},
	} {
		system.starts, system.runbooks = 0, 0
		if got := call(c.method, c.target, c.auth, c.remote); got != c.want || system.starts != c.starts || system.runbooks != c.runbooks {
			t.Errorf("%s: status = %d, jobs started = %d, runbooks = %d; want %d, %d and %d", c.name, got, system.starts, system.runbooks, c.want, c.starts, c.runbooks)
		}
	}

	// the status of the plan asks no more of the token than the rest of the group
	if got := call(http.MethodGet, plan, access, ""); got != http.StatusOK {
		t.Errorf("status with the access token = %d", got)
	}
	if got := call(http.MethodGet, plan, "", ""); got != http.StatusUnauthorized {
		t.Errorf("status without a token = %d", got)
	}
	// and no other method on these paths gets anywhere, with the best of tokens
	system.starts, system.reads, system.runbooks = 0, 0, 0
	for _, c := range [][2]string{
		{http.MethodPut, plan}, {http.MethodDelete, plan}, {http.MethodPatch, plan},
		{http.MethodPost, runbook}, {http.MethodPut, runbook}, {http.MethodDelete, runbook},
	} {
		if got := call(c[0], c[1], access, ""); got >= 200 && got < 300 {
			t.Errorf("%s %s = %d", c[0], c[1], got)
		}
	}
	if system.starts != 0 || system.reads != 0 || system.runbooks != 0 {
		t.Errorf("the plan was started %d times, read %d, and the runbook %d, by methods that are not its own", system.starts, system.reads, system.runbooks)
	}
}
