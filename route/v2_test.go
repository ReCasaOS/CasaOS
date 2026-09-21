package route

import (
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestSkipRequestValidation(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		want        bool
	}{
		// A GET has no body and often no Content-Type: this used to panic.
		{"no Content-Type is validated", "", false},
		{"a JSON body is validated", "application/json", false},
		{"a file upload is exempt", "multipart/form-data; boundary=x", true},
	}

	e := echo.New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v2/casaos/health/services", nil)
			if tc.contentType != "" {
				req.Header.Set(echo.HeaderContentType, tc.contentType)
			}
			if got := skipRequestValidation(e.NewContext(req, httptest.NewRecorder())); got != tc.want {
				t.Fatalf("skipRequestValidation(Content-Type %q) = %v, want %v", tc.contentType, got, tc.want)
			}
		})
	}
}
