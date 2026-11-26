package route

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestDocServesOpenAPIOnly(t *testing.T) {
	spec, err := os.ReadFile("../api/casaos/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	h := InitV2DocRouter(string(spec))

	for _, p := range []string{V2DocPath, V2DocPath + "/", V2DocPath + "/openapi.yaml"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != string(spec) {
			t.Fatalf("%s: status %d, spec served = %v", p, rec.Code, rec.Body.String() == string(spec))
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s: X-Content-Type-Options = %q", p, got)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/yaml" {
			t.Fatalf("%s: Content-Type = %q", p, got)
		}
	}

	for _, p := range []string{V2DocPath + "/index.html", V2DocPath + "/casaos/openapi.yaml"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", p, rec.Code)
		}
	}

	// No asset the service serves may pull code or styles from another host.
	for _, bad := range []string{"<script src=\"http", "<link", "cdn.jsdelivr", "unpkg.com"} {
		if strings.Contains(string(spec), bad) {
			t.Fatalf("served spec contains %q", bad)
		}
	}
}
