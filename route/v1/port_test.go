package v1

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// GetPort used to look forever for a free port of a type that is neither tcp nor udp.
func TestGetPortRefusesATypeItDoesNotKnow(t *testing.T) {
	for _, c := range []struct {
		query string
		want  int
	}{
		{"", http.StatusOK},
		{"?type=tcp", http.StatusOK},
		{"?type=udp", http.StatusOK},
		{"?type=sctp", http.StatusBadRequest},
		{"?type=TCP", http.StatusBadRequest},
	} {
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/v1/port/"+c.query, nil), recorder)
		done := make(chan error, 1)
		go func() { done <- GetPort(ctx) }()
		select {
		case err := <-done:
			if err != nil || recorder.Code != c.want {
				t.Errorf("GetPort%s: status = %d, err = %v; want %d", c.query, recorder.Code, err, c.want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("GetPort%s never answered", c.query)
		}
	}
}
