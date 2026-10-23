//go:build linux

package route

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/fileroots"
)

// /v3/file serves only what is below [file] ManagementRoots
func TestTheFileOfV3StaysInItsRoots(t *testing.T) {
	access, _, _ := theTokensOfTheTests(t, &fakeUpdateSystem{})
	base, _ := filepath.EvalSymlinks(t.TempDir())
	data, secret := filepath.Join(base, "data"), filepath.Join(base, "secret")
	for dir, file := range map[string]string{data: "note.txt", secret: "key"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte("hello"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	roots, err := fileroots.Open([]string{data})
	if err != nil {
		t.Fatal(err)
	}
	previous := fileroots.Default
	fileroots.Default = roots
	t.Cleanup(func() { fileroots.Default = previous; roots.Close() })

	handler := InitFile()
	for path, want := range map[string]int{data + "/note.txt": http.StatusOK, secret + "/key": http.StatusForbidden, data + "/../secret/key": http.StatusForbidden} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v3/file?"+url.Values{"token": {access}, "path": {path}}.Encode(), nil))
		if recorder.Code != want {
			t.Errorf("%s: %d, want %d", path, recorder.Code, want)
		}
	}
}
