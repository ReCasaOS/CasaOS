package route

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/utils/jwt"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

const dockerPlanID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestTheDockerUpdateRoutesAreSysRoutesBehindTheToken(t *testing.T) {
	e, ok := InitV1Router().(*echo.Echo)
	if !ok {
		t.Fatal("InitV1Router() is not an *echo.Echo")
	}
	registered := map[string]bool{}
	for _, r := range e.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, route := range [][2]string{
		{http.MethodGet, "/v1/sys/docker/containers"},
		{http.MethodPost, "/v1/sys/docker/update"},
		{http.MethodGet, "/v1/sys/docker/update/status"},
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
}

// claimsOf is what the group's JWT check leaves in the context for a request with a token.
func claimsOf(issuer string) *jwt.Claims {
	claims := &jwt.Claims{Username: "owner", ID: 1}
	claims.Issuer = issuer
	return claims
}

func TestHeaderTokenOnly(t *testing.T) {
	cases := []struct {
		name      string
		header    string
		query     string
		claims    any
		hasClaims bool
		want      int
	}{
		{"the access token in the header", "token", "", claimsOf("casaos"), true, http.StatusOK},
		{"a service of the box: no claims", "Internal secret", "", nil, false, http.StatusOK},
		{"the token in the query only", "", "token=abc", claimsOf("casaos"), true, http.StatusUnauthorized},
		{"no token at all", "", "", nil, false, http.StatusUnauthorized},
		{"no header, and no claims either", "", "token=abc", nil, false, http.StatusUnauthorized},
		{"the refresh token in the header", "token", "", claimsOf("refresh"), true, http.StatusUnauthorized},
		{"the refresh token, and the query besides", "token", "token=abc", claimsOf("refresh"), true, http.StatusUnauthorized},
		{"claims that are no claims", "token", "", "owner", true, http.StatusUnauthorized},
		{"claims that are a nil pointer", "token", "", (*jwt.Claims)(nil), true, http.StatusUnauthorized},
		{"an issuer that is only like it", "token", "", claimsOf("Refresh"), true, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := "/v1/sys/docker/update"
			if c.query != "" {
				target += "?" + c.query
			}
			request := httptest.NewRequest(http.MethodPost, target, nil)
			if c.header != "" {
				request.Header.Set(echo.HeaderAuthorization, c.header)
			}
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(request, recorder)
			if c.hasClaims {
				ctx.Set("user", c.claims)
			}
			var reached bool
			handler := headerTokenOnly(func(ctx echo.Context) error {
				reached = true
				return ctx.NoContent(http.StatusOK)
			})
			if err := handler(ctx); err != nil {
				t.Fatalf("error = %v", err)
			}
			if recorder.Code != c.want || reached != (c.want == http.StatusOK) {
				t.Errorf("status = %d, reached = %v, want %d", recorder.Code, reached, c.want)
			}
			if c.want == http.StatusUnauthorized && !strings.Contains(recorder.Body.String(), `"success":401`) {
				t.Errorf("body = %s", recorder.Body)
			}
		})
	}
}

type fakeUpdateSystem struct {
	service.SystemService
	asked int
}

func (f *fakeUpdateSystem) StartDockerUpdate(string) (service.SystemDockerUpdateStatus, error) {
	f.asked++
	return service.SystemDockerUpdateStatus{Supported: true, State: "running"}, nil
}

func (f *fakeUpdateSystem) DockerUpdateStatus() service.SystemDockerUpdateStatus {
	return service.SystemDockerUpdateStatus{Supported: true, State: "idle"}
}

type fakeUpdateRepository struct {
	service.Repository
	system service.SystemService
}

func (f fakeUpdateRepository) System() service.SystemService { return f.system }

// The real router, with real tokens: the key is served by a user-service of the test's.
func TestTheDockerUpdateStartsOnlyForTheAccessTokenInTheHeader(t *testing.T) {
	runtimePath := t.TempDir()
	private, public, err := jwt.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
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
	system := &fakeUpdateSystem{}
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

	post := func(target, authorization, remoteAddr string) int {
		request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"plan_id":"`+dockerPlanID+`"}`))
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
	const update = "/v1/sys/docker/update"
	cases := []struct {
		name          string
		target, auth  string
		remote        string
		want, started int
	}{
		{"the access token in the header", update, access, "", http.StatusOK, 1},
		{"a service of the box, with the secret", update, secret, "127.0.0.1:4000", http.StatusOK, 1},
		{"the secret from another host", update, secret, "192.168.1.20:4000", http.StatusUnauthorized, 0},
		{"the access token in the query", update + "?token=" + access, "", "", http.StatusUnauthorized, 0},
		{"the access token in the query, and a header that is not a token", update + "?token=" + access, "nonsense", "", http.StatusUnauthorized, 0},
		{"the refresh token in the header", update, refresh, "", http.StatusUnauthorized, 0},
		{"the refresh token in the query", update + "?token=" + refresh, "", "", http.StatusUnauthorized, 0},
		{"a header that is not a token", update, "nonsense", "", http.StatusUnauthorized, 0},
		{"nothing", update, "", "", http.StatusUnauthorized, 0},
	}
	for _, c := range cases {
		system.asked = 0
		if got := post(c.target, c.auth, c.remote); got != c.want || system.asked != c.started {
			t.Errorf("%s: status = %d, updates started = %d; want %d and %d", c.name, got, system.asked, c.want, c.started)
		}
	}

	// the status asks no more of the token than the rest of the group
	get := func(target, authorization string) int {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		if authorization != "" {
			request.Header.Set(echo.HeaderAuthorization, authorization)
		}
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, request)
		return recorder.Code
	}
	if got := get("/v1/sys/docker/update/status", access); got != http.StatusOK {
		t.Errorf("status with the access token = %d", got)
	}
	if got := get("/v1/sys/docker/update/status", ""); got != http.StatusUnauthorized {
		t.Errorf("status without a token = %d", got)
	}
}
