package v1

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

type namedNotify struct {
	service.NotifyServer
	names []string
}

func (n *namedNotify) SendNotify(name string, _ map[string]interface{}) {
	n.names = append(n.names, name)
}

type notifyRepository struct {
	service.Repository
	notify *namedNotify
}

func (r notifyRepository) Notify() service.NotifyServer { return r.notify }

// The route is /v1/notify/:path: the name of the message is that parameter.
func TestPostNotifyMessageSendsTheNameInThePath(t *testing.T) {
	notify := &namedNotify{}
	previous := service.MyService
	service.MyService = notifyRepository{notify: notify}
	t.Cleanup(func() { service.MyService = previous })

	e := echo.New()
	e.POST("/v1/notify/:path", PostNotifyMessage)
	request := httptest.NewRequest(http.MethodPost, "/v1/notify/sys_disk", strings.NewReader(`{"used":1}`))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || len(notify.names) != 1 || notify.names[0] != "sys_disk" {
		t.Errorf("status = %d, names sent = %q; want 200 and [sys_disk]", recorder.Code, notify.names)
	}
}
