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
	"github.com/ReCasaOS/CasaOS/pkg/fleet"
	"github.com/labstack/echo/v4"
)

// On the real router, with real tokens: the backup actions relayed to a box
// want the access token in the header, and the backups read is a plain GET.
// This box is no master, so a request that passes the token checks is answered
// 404 (no such box) or 409 (no master): anything but 401.
func TestRelayedBackupActionsWantTheAccessTokenInTheHeader(t *testing.T) {
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
	previousConfig, previousFleet := config.CommonInfo, fleet.Default
	config.CommonInfo = &model.CommonModel{RuntimePath: runtimePath}
	fleet.Default = fleet.New(t.TempDir())
	t.Cleanup(func() { config.CommonInfo, fleet.Default = previousConfig, previousFleet })

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

	const relay = "/v1/fleet/boxes/b1/relay/v2/app_management/backup/"
	for _, action := range [][2]string{
		{http.MethodPost, relay + "schedules/run"},
		{http.MethodPost, relay + "verifications"},
		{http.MethodPut, relay + "verification-schedule"},
		{http.MethodPost, relay + "destinations/GDrive/restic/check"},
	} {
		method, path := action[0], action[1]
		if code, _ := do(method, path, access); code != http.StatusNotFound {
			t.Errorf("%s %s with the access token = %d, want 404 (past the token checks)", method, path, code)
		}
		// no header: the access token in the query is no use on a POST, and the
		// group's JWT check refuses before the route (headerTokenOnly's own message is behind it and is covered by TestHeaderTokenOnly)
		if code, _ := do(method, path+"?token="+access, ""); code != http.StatusUnauthorized {
			t.Errorf("%s %s with the token in the query = %d, want 401", method, path, code)
		}
		// the group's JWT check (accessClaims) already refuses it, before headerTokenOnly's own message
		if code, message := do(method, path, refresh); code != http.StatusUnauthorized || message == "" {
			t.Errorf("%s %s with the refresh token = %d %q", method, path, code, message)
		}
	}

	if code, _ := do(http.MethodGet, "/v1/fleet/backups?token="+access, ""); code != http.StatusConflict {
		t.Errorf("GET /v1/fleet/backups with the token in the query = %d, want 409 (no master)", code)
	}
	if code, _ := do(http.MethodGet, "/v1/fleet/backups", ""); code != http.StatusUnauthorized {
		t.Errorf("GET /v1/fleet/backups without a token = %d, want 401", code)
	}
}
