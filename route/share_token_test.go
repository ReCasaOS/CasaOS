package route

import (
	"encoding/json"
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
	"github.com/labstack/echo/v4"
)

// On the real router: reading the sharing takes any valid token (the query too), changing it wants the access token in
// the header. This box has no relay, so a write that passes the token checks is answered 503, never 401.
func TestShareWritesNeedTheHeaderToken(t *testing.T) {
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
	previous := config.CommonInfo
	config.CommonInfo = &model.CommonModel{RuntimePath: runtimePath}
	t.Cleanup(func() { config.CommonInfo = previous })
	access, err := jwt.GetAccessToken("owner", private, 1)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := jwt.GetRefreshToken("owner", private, 1)
	if err != nil {
		t.Fatal(err)
	}
	e := InitV1Router()
	do := func(method, target, authorization string) int {
		request := httptest.NewRequest(method, target, strings.NewReader(`{"name":"maison","accept_tos":true}`))
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		if authorization != "" {
			request.Header.Set(echo.HeaderAuthorization, authorization)
		}
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, request)
		var result model.Result
		_ = json.Unmarshal(recorder.Body.Bytes(), &result)
		return recorder.Code
	}

	for _, write := range [][2]string{
		{http.MethodPut, "/v1/share/name"},
		{http.MethodPut, "/v1/share/apps/jellyfin"},
		{http.MethodDelete, "/v1/share/apps/jellyfin"},
		{http.MethodDelete, "/v1/share/apps"},
	} {
		method, path := write[0], write[1]
		if code := do(method, path+"?token="+access, ""); code != http.StatusUnauthorized {
			t.Errorf("%s %s with the token in the query = %d, want 401", method, path, code)
		}
		if code := do(method, path, refresh); code != http.StatusUnauthorized {
			t.Errorf("%s %s with the refresh token = %d, want 401", method, path, code)
		}
	}
	if code := do(http.MethodPut, "/v1/share/name", access); code != http.StatusServiceUnavailable {
		t.Errorf("PUT /v1/share/name with the access token = %d, want 503 (past the token checks, no relay)", code)
	}
	if code := do(http.MethodGet, "/v1/share?token="+access, ""); code != http.StatusOK {
		t.Errorf("GET /v1/share with the token in the query = %d, want 200", code)
	}
	if code := do(http.MethodGet, "/v1/share", ""); code != http.StatusUnauthorized {
		t.Errorf("GET /v1/share without a token = %d, want 401", code)
	}
}
