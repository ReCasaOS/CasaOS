package v1

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	http2 "github.com/ReCasaOS/CasaOS-Common/utils/http"
	"github.com/labstack/echo/v4"
)

func callProxy(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/sys/proxy?url="+url.QueryEscape(target), nil)
	recorder := httptest.NewRecorder()
	if err := GetSystemProxy(echo.New().NewContext(request, recorder)); err != nil {
		t.Fatalf("GetSystemProxy: %v", err)
	}
	return recorder
}

// The route is open to every signed-in account, and CasaOS-Common's HTTP helpers
// add the internal secret to any request aimed at loopback. The route must not
// be a way for an account to send the secret to a service.
func TestGetSystemProxyNeverSendsTheInternalSecret(t *testing.T) {
	http2.SetInternalAuthorization(func() string { return "Internal the-secret" })
	t.Cleanup(func() { http2.SetInternalAuthorization(nil) })

	var received string
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("private answer"))
	}))
	t.Cleanup(service.Close)

	recorder := callProxy(t, service.URL)

	if received != "" {
		t.Fatalf("the service received Authorization %q, want none", received)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want the 200 the address answered with", recorder.Code)
	}
	if body := recorder.Body.String(); body == "private answer" {
		t.Fatalf("the body of the address was relayed: %q", body)
	}
}

// The dashboard only asks whether the address answers: it keeps the status.
func TestGetSystemProxyKeepsTheStatusOfTheAddress(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(service.Close)

	if got := callProxy(t, service.URL).Code; got != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", got, http.StatusNotFound)
	}
}

func TestGetSystemProxyRefusesWhatIsNotAWebAddress(t *testing.T) {
	for _, target := range []string{"", "file:///etc/passwd", "gopher://127.0.0.1:70/", "192.168.1.5:8080", "http://"} {
		if got := callProxy(t, target).Code; got != http.StatusBadRequest {
			t.Errorf("url %q: status = %d, want %d", target, got, http.StatusBadRequest)
		}
	}
}
