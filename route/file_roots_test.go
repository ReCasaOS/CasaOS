//go:build linux

package route

import (
	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/pkg/fileaccess"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/fileroots"
)

// /v3/file serves only what is below [file] ManagementRoots
// asAccount makes user-service say role, and folders, of every account, for the tests.
func asAccount(t *testing.T, role string, folders ...external.Folder) {
	t.Helper()
	wasAccount, wasFolders := fileaccess.AccountOf, fileaccess.FoldersOf
	t.Cleanup(func() { fileaccess.AccountOf, fileaccess.FoldersOf = wasAccount, wasFolders })
	fileaccess.AccountOf = func(string, string) (string, bool, bool) { return role, false, true }
	fileaccess.FoldersOf = func(string, string) []external.Folder { return folders }
}

func TestTheFileOfV3StaysInItsRoots(t *testing.T) {
	access, _, _ := theTokensOfTheTests(t, &fakeUpdateSystem{})
	asAccount(t, "admin")
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
	t.Cleanup(func() { fileroots.Default = previous })

	handler := InitFile()
	for path, want := range map[string]int{data + "/note.txt": http.StatusOK, secret + "/key": http.StatusForbidden, data + "/../secret/key": http.StatusForbidden} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v3/file?"+url.Values{"token": {access}, "path": {path}}.Encode(), nil))
		if recorder.Code != want {
			t.Errorf("%s: %d, want %d", path, recorder.Code, want)
		}
	}
}

// An account reads with /v3/file what is in its folders, and nothing else.
func TestTheFileOfV3StaysInTheFoldersOfAnAccount(t *testing.T) {
	access, _, _ := theTokensOfTheTests(t, &fakeUpdateSystem{})
	base, _ := filepath.EvalSymlinks(t.TempDir())
	kids, other := filepath.Join(base, "Kids"), filepath.Join(base, "Other")
	for _, dir := range []string{kids, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte("hello"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	asAccount(t, "user", external.Folder{Path: kids})

	handler := InitFile()
	for path, want := range map[string]int{kids + "/f": http.StatusOK, other + "/f": http.StatusForbidden, kids + "/../Other/f": http.StatusForbidden} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v3/file?"+url.Values{"token": {access}, "path": {path}}.Encode(), nil))
		if recorder.Code != want {
			t.Errorf("%s: %d, want %d", path, recorder.Code, want)
		}
	}
}
