package route

import (
	"net/http"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/labstack/echo/v4"
)

var hostManagementRoutes = [][2]string{
	{http.MethodGet, "/v1/sys/os"},
	{http.MethodGet, "/v1/sys/os/readiness"},
	{http.MethodPost, "/v1/sys/os/upgrade/start"},
	{http.MethodPost, "/v1/sys/update"},
	{http.MethodGet, "/v1/sys/packages"},
	{http.MethodPost, "/v1/sys/packages/update"},
	{http.MethodPost, "/v1/sys/docker/update"},
	{http.MethodPost, "/v1/sys/stop"},
	{http.MethodGet, "/v1/sys/autoupdate"},
	{http.MethodPut, "/v1/sys/autoupdate"},
}

func registeredRoutes(t *testing.T) map[string]bool {
	t.Helper()
	e, ok := InitV1Router().(*echo.Echo)
	if !ok {
		t.Fatal("InitV1Router() is not an *echo.Echo")
	}
	registered := map[string]bool{}
	for _, r := range e.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	return registered
}

func TestImageManagedHostHasNoHostManagementRoutes(t *testing.T) {
	previous := config.ServerInfo.ImageManaged
	t.Cleanup(func() { config.ServerInfo.ImageManaged = previous })

	config.ServerInfo.ImageManaged = false
	registered := registeredRoutes(t)
	for _, route := range hostManagementRoutes {
		if !registered[route[0]+" "+route[1]] {
			t.Errorf("default: %s %s is not registered", route[0], route[1])
		}
	}

	config.ServerInfo.ImageManaged = true
	registered = registeredRoutes(t)
	for _, route := range hostManagementRoutes {
		if registered[route[0]+" "+route[1]] {
			t.Errorf("image-managed: %s %s is registered", route[0], route[1])
		}
	}
	// what is not host management stays; the modules too, from the image's provider
	for _, route := range [][2]string{
		{http.MethodGet, "/v1/sys/modules"},
		{http.MethodPost, "/v1/sys/modules/:name/install"},
		{http.MethodGet, "/v1/sys/version"},
		{http.MethodGet, "/v1/sys/hardware"},
		{http.MethodGet, "/v1/sys/stats"},
		{http.MethodGet, "/v1/samba/shares"},
	} {
		if !registered[route[0]+" "+route[1]] {
			t.Errorf("image-managed: %s %s is missing", route[0], route[1])
		}
	}
}
