package route

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/labstack/echo/v4"
)

func TestSkipJWT(t *testing.T) {
	// A runtime path with the secret of this boot, as the gateway leaves it.
	runtimePath := t.TempDir()
	if err := external.WriteInternalSecret(runtimePath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(runtimePath, external.InternalSecretFilename))
	if err != nil {
		t.Fatal(err)
	}
	secret := "Internal " + strings.TrimSpace(string(raw))
	previous := config.CommonInfo
	config.CommonInfo = &model.CommonModel{RuntimePath: runtimePath}
	t.Cleanup(func() { config.CommonInfo = previous })

	cases := []struct {
		name         string
		realIP, auth string
		want         bool
	}{
		{"a service: loopback IPv4 with the secret", "127.0.0.1", secret, true},
		{"a service: loopback IPv6 with the secret", "::1", secret, true},

		// The point of the guard: loopback alone is any local process.
		{"loopback with no header is not trusted", "127.0.0.1", "", false},
		{"loopback with a token, not the secret, is not trusted here", "127.0.0.1", "Bearer eyJ", false},
		{"loopback with a wrong secret is not trusted", "::1", "Internal " + strings.Repeat("0", 64), false},

		// And the secret is no use from anywhere else.
		{"a remote address is never trusted", "192.168.1.20", secret, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := skipJWT(tc.realIP, tc.auth); got != tc.want {
				t.Fatalf("skipJWT(%q, %q) = %v, want %v", tc.realIP, tc.auth, got, tc.want)
			}
		})
	}
}

func TestSkipAccessLog(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		realIP string
		want   bool
	}{
		// The flood from issue #2211: local-storage posts sys_disk and sys_usb
		// here every 5s, straight to the loopback listener.
		{"the internal status post is not logged", "/v1/notify/system_status", "127.0.0.1", true},
		{"the internal status post is not logged over IPv6 either", "/v1/notify/system_status", "::1", true},
		{"the wildcard notify route is not logged either", "/v1/notify/casaos:file:recover", "127.0.0.1", true},

		// Everything else stays in the log.
		{"a remote notify post is logged", "/v1/notify/system_status", "192.168.1.20", false},
		{"another loopback route is logged", "/v1/sys/hardware", "127.0.0.1", false},
		{"a similarly named route is logged", "/v1/notifyx", "127.0.0.1", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := skipAccessLog(tc.path, tc.realIP); got != tc.want {
				t.Fatalf("skipAccessLog(%q, %q) = %v, want %v", tc.path, tc.realIP, got, tc.want)
			}
		})
	}
}

func TestTheTelemetryAutoUpdateAndAlertsRoutesAreSysRoutesBehindTheToken(t *testing.T) {
	e, ok := InitV1Router().(*echo.Echo)
	if !ok {
		t.Fatal("InitV1Router() is not an *echo.Echo")
	}
	registered := map[string]bool{}
	for _, r := range e.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, route := range [][2]string{
		{http.MethodGet, "/v1/sys/telemetry"},
		{http.MethodPut, "/v1/sys/telemetry"},
		{http.MethodGet, "/v1/sys/autoupdate"},
		{http.MethodPut, "/v1/sys/autoupdate"},
		{http.MethodGet, "/v1/sys/alerts"},
		{http.MethodPut, "/v1/sys/alerts"},
		{http.MethodPost, "/v1/sys/alerts/test"},
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
