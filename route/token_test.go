package route

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

// noNewerRelease is version.json advertising nothing newer than what is installed: the update has nothing to do.
type noNewerRelease struct{}

func (noNewerRelease) GetCasaosVersion() model.Version   { return model.Version{Version: "v0.0.1"} }
func (noNewerRelease) FetchCasaosVersion() model.Version { return model.Version{Version: "v0.0.1"} }

type tokenRepository struct{ fakeUpdateRepository }

func (tokenRepository) Casa() service.CasaService { return noNewerRelease{} }

type tokenCase struct {
	name, method, target, body, auth string
	upgrade                          bool
	want                             int
}

func callWith(e http.Handler, c tokenCase) int {
	request := httptest.NewRequest(c.method, c.target, strings.NewReader(c.body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if c.auth != "" {
		request.Header.Set(echo.HeaderAuthorization, c.auth)
	}
	if c.upgrade {
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "websocket")
	}
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)
	return recorder.Code
}

// A link, an image, a download or a websocket cannot set a header, so a GET may carry the token in the address; a call that
// changes something can, and the address leaks into logs, histories and Referers. The refresh token only gets a new access token.
func TestTheV1TokenInTheQueryIsTakenOnlyOnAGetAndNeverTheRefreshToken(t *testing.T) {
	system := &fakeUpdateSystem{}
	access, refresh, _ := theTokensOfTheTests(t, system)
	service.MyService = tokenRepository{fakeUpdateRepository{system: system}}
	e := InitV1Router()

	const status, update, autoupdate = "/v1/sys/docker/update/status", "/v1/sys/update", "/v1/sys/autoupdate"
	for _, c := range []tokenCase{
		{"GET, the access token in the query", http.MethodGet, status + "?token=" + access, "", "", false, http.StatusOK},
		{"websocket upgrade, the access token in the query", http.MethodGet, status + "?token=" + access, "", "", true, http.StatusOK},
		{"GET, the access token in the header", http.MethodGet, status, "", access, false, http.StatusOK},
		{"PUT /v1/file, the access token in the query", http.MethodPut, "/v1/file?token=" + access, "", "", false, http.StatusUnauthorized},
		{"DELETE /v1/batch, the access token in the query", http.MethodDelete, "/v1/batch?token=" + access, "", "", false, http.StatusUnauthorized},
		// past the token: the handler wants paths
		{"DELETE /v1/batch, the access token in the header", http.MethodDelete, "/v1/batch", "", access, false, http.StatusBadRequest},
		{"POST /v1/sys/update, the access token in the query", http.MethodPost, update + "?token=" + access, "", "", false, http.StatusUnauthorized},
		{"POST /v1/sys/update, the access token in the header", http.MethodPost, update, "", access, false, http.StatusOK},
		{"PUT /v1/sys/autoupdate, the access token in the query", http.MethodPut, autoupdate + "?token=" + access, "{", "", false, http.StatusUnauthorized},
		// past the token: the handler refuses the body
		{"PUT /v1/sys/autoupdate, the access token in the header", http.MethodPut, autoupdate, "{", access, false, http.StatusBadRequest},
		{"GET, the refresh token in the header", http.MethodGet, status, "", refresh, false, http.StatusUnauthorized},
		{"GET, the refresh token in the query", http.MethodGet, status + "?token=" + refresh, "", "", false, http.StatusUnauthorized},
		{"websocket upgrade, the refresh token in the query", http.MethodGet, status + "?token=" + refresh, "", "", true, http.StatusUnauthorized},
		{"POST /v1/sys/update, the refresh token in the header", http.MethodPost, update, "", refresh, false, http.StatusUnauthorized},
		{"PUT /v1/sys/autoupdate, the refresh token in the header", http.MethodPut, autoupdate, "{", refresh, false, http.StatusUnauthorized},
	} {
		if got := callWith(e, c); got != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestTheV2TokenInTheQueryIsTakenOnlyOnAGetAndNeverTheRefreshToken(t *testing.T) {
	access, refresh, _ := theTokensOfTheTests(t, &fakeUpdateSystem{})
	e := InitV2Router()

	const zt, upload, body = "/v2/casaos/zt/n1/status", "/v2/casaos/file/upload", `{"status":"online"}`
	for _, c := range []tokenCase{
		{"PUT, the access token in the header", http.MethodPut, zt, body, access, false, http.StatusOK},
		{"PUT, the access token in the query", http.MethodPut, zt + "?token=" + access, body, "", false, http.StatusUnauthorized},
		{"POST, the access token in the query", http.MethodPost, upload + "?token=" + access, "", "", false, http.StatusUnauthorized},
		// past the token: the request validation wants the chunk's parameters
		{"GET, the access token in the query", http.MethodGet, upload + "?token=" + access, "", "", false, http.StatusBadRequest},
		{"GET, the refresh token in the query", http.MethodGet, upload + "?token=" + refresh, "", "", false, http.StatusUnauthorized},
		{"PUT, the refresh token in the header", http.MethodPut, zt, body, refresh, false, http.StatusUnauthorized},
	} {
		if got := callWith(e, c); got != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestTheFileOfV3TakesTheAccessTokenAndNotTheRefreshToken(t *testing.T) {
	access, refresh, _ := theTokensOfTheTests(t, &fakeUpdateSystem{})
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := InitFile()
	for _, c := range []struct {
		name, token string
		want        int
	}{
		{"the access token", access, http.StatusOK},
		{"the refresh token", refresh, http.StatusUnauthorized},
		{"no token", "", http.StatusUnauthorized},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v3/file?"+url.Values{"token": {c.token}, "path": {path}}.Encode(), nil))
		if recorder.Code != c.want || (c.want == http.StatusOK && recorder.Body.String() != "hello") {
			t.Errorf("%s: status = %d, body = %q; want %d", c.name, recorder.Code, recorder.Body, c.want)
		}
	}
}
