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
	"github.com/ReCasaOS/CasaOS/pkg/apiwidgets"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/labstack/echo/v4"
)

// On the real router, with real tokens: what writes a widget, or reads an address
// for one, wants the access token in the header; the list and a card are plain
// GETs. The box has no widget, so a request that passes the token checks is
// answered by the handler: 404 (no such widget) or 400 (an empty change).
func TestAPIWidgetsWantTheAccessTokenInTheHeader(t *testing.T) {
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
	previousConfig, previousWidgets := config.CommonInfo, apiwidgets.Default
	config.CommonInfo = &model.CommonModel{RuntimePath: runtimePath}
	apiwidgets.Default = apiwidgets.New(t.TempDir())
	t.Cleanup(func() { config.CommonInfo, apiwidgets.Default = previousConfig, previousWidgets })

	access, err := jwt.GetAccessToken("owner", private, 1)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := jwt.GetRefreshToken("owner", private, 1)
	if err != nil {
		t.Fatal(err)
	}
	e := InitV1Router()
	do := func(method, target, authorization string) (int, string) {
		request := httptest.NewRequest(method, target, strings.NewReader(`{}`))
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		if authorization != "" {
			request.Header.Set(echo.HeaderAuthorization, authorization)
		}
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, request)
		var result model.Result
		_ = json.Unmarshal(recorder.Body.Bytes(), &result)
		return recorder.Code, result.Message
	}

	for _, action := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/v1/sys/widgets", http.StatusBadRequest},
		{http.MethodPut, "/v1/sys/widgets/abcdefgh", http.StatusNotFound},
		{http.MethodDelete, "/v1/sys/widgets/abcdefgh", http.StatusNotFound},
		{http.MethodPost, "/v1/sys/widgets/test", http.StatusBadRequest},
		{http.MethodPost, "/v1/sys/widgets/preview", http.StatusBadRequest},
	} {
		method, path := action.method, action.path
		// the handler's own answer, not the router's for a path it does not know
		if code, message := do(method, path, access); code != action.want || message == "" || message == "Not Found" {
			t.Errorf("%s %s with the access token = %d %q, want %d (past the token checks)", method, path, code, message, action.want)
		}
		// no header: the access token in the query is no use on a write, and the group's JWT check
		// refuses before the route (headerTokenOnly's own message is behind it and is covered by TestHeaderTokenOnly)
		if code, _ := do(method, path+"?token="+access, ""); code != http.StatusUnauthorized {
			t.Errorf("%s %s with the token in the query = %d, want 401", method, path, code)
		}
		if code, message := do(method, path, refresh); code != http.StatusUnauthorized || message == "" {
			t.Errorf("%s %s with the refresh token = %d %q", method, path, code, message)
		}
	}

	// test and preview are static segments: they are not taken for the id of a widget
	if code, _ := do(http.MethodPut, "/v1/sys/widgets/test", access); code == http.StatusOK || code == http.StatusBadRequest {
		t.Errorf("PUT /v1/sys/widgets/test = %d", code)
	}

	if code, _ := do(http.MethodGet, "/v1/sys/widgets?token="+access, ""); code != http.StatusOK {
		t.Errorf("GET /v1/sys/widgets with the token in the query = %d, want 200", code)
	}
	if code, message := do(http.MethodGet, "/v1/sys/widgets/abcdefgh/card?token="+access, ""); code != http.StatusNotFound || message != "no such widget" {
		t.Errorf("GET a card with the token in the query = %d %q, want 404", code, message)
	}
	for _, path := range []string{"/v1/sys/widgets", "/v1/sys/widgets/abcdefgh/card"} {
		if code, _ := do(http.MethodGet, path, ""); code != http.StatusUnauthorized {
			t.Errorf("GET %s without a token = %d, want 401", path, code)
		}
	}
}
