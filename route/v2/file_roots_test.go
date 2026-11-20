//go:build linux

package v2

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/pkg/fileaccess"
	"github.com/ReCasaOS/CasaOS/pkg/fileroots"
	"github.com/labstack/echo/v4"
)

// the dashboard uploads through v2: the folder, the file and its `.tmp` stay
// below [file] ManagementRoots
func TestAV2UploadCannotLeaveItsRoot(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, secret := filepath.Join(base, "data"), filepath.Join(base, "secret")
	for _, dir := range []string{data + "/Media", data + "/Backups", secret} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		data + "/escape":            "../secret",
		data + "/Backups/b.txt.tmp": "../../secret/b.txt", // the `.tmp` the upload writes first
	} {
		if err := os.Symlink(target, link); err != nil {
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

	c := NewCasaOS().(*CasaOS)
	content := []byte("uploaded")
	upload := func(path, relative string) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		size := strconv.Itoa(len(content))
		for k, v := range map[string]string{
			"path": path, "relativePath": relative, "filename": filepath.Base(relative), "identifier": path + relative,
			"chunkNumber": "1", "totalChunks": "1", "chunkSize": size, "currentChunkSize": size, "totalSize": size,
		} {
			_ = w.WriteField(k, v)
		}
		part, _ := w.CreateFormFile("file", filepath.Base(relative))
		_, _ = part.Write(content)
		_ = w.Close()
		req := httptest.NewRequest(http.MethodPost, "/v2/casaos/file/upload", &body)
		req.Header.Set(echo.HeaderContentType, w.FormDataContentType())
		rec := httptest.NewRecorder()
		if err := c.PostUploadFile(echo.New().NewContext(req, rec)); err != nil {
			t.Fatal(err)
		}
		return rec
	}

	for _, u := range [][2]string{
		{secret, "x.txt"},            // a folder outside
		{data, "../secret/x.txt"},    // spelled out
		{data, "escape/x.txt"},       // through a link
		{data + "/Backups", "b.txt"}, // its `.tmp` is a link out
	} {
		rec := upload(u[0], u[1])
		if rec.Code != http.StatusForbidden {
			t.Errorf("upload %s + %s: %d %s, want 403", u[0], u[1], rec.Code, rec.Body.String())
			continue
		}
		var answer map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil || answer["data"] == "" {
			t.Errorf("upload %s + %s: the reason is not in data: %s", u[0], u[1], rec.Body.String())
		}
	}
	for _, p := range []string{secret + "/x.txt", secret + "/b.txt"} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("an upload landed outside: %s", p)
		}
	}

	if rec := upload(data+"/Media", "a.txt"); rec.Code != http.StatusOK {
		t.Fatalf("upload inside: %d %s", rec.Code, rec.Body.String())
	}
	if b, err := os.ReadFile(data + "/Media/a.txt"); err != nil || !bytes.Equal(b, content) {
		t.Errorf("upload inside: %q %v", b, err)
	}
}

// An account's upload lands in a folder it may write, through its root: not in a folder it
// may only read, not through a link out, not outside its folders.
func TestAV2UploadOfAnAccountStaysInItsWritableFolders(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	kids, media, other := filepath.Join(base, "Kids"), filepath.Join(base, "Media"), filepath.Join(base, "Other")
	for _, dir := range []string{kids, media, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../Other", kids+"/out"); err != nil {
		t.Fatal(err)
	}
	previous := fileroots.Default
	fileroots.Default = nil
	wasAccount, wasFolders := fileaccess.AccountOf, fileaccess.FoldersOf
	t.Cleanup(func() { fileroots.Default, fileaccess.AccountOf, fileaccess.FoldersOf = previous, wasAccount, wasFolders })
	fileaccess.AccountOf = func(string, string) (string, bool, bool) { return "user", false, true }
	fileaccess.FoldersOf = func(string, string) []external.Folder {
		return []external.Folder{{Path: kids, Write: true}, {Path: media}}
	}

	c := NewCasaOS().(*CasaOS)
	upload := func(path, relative string) int {
		t.Helper()
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		for k, v := range map[string]string{
			"path": path, "relativePath": relative, "filename": filepath.Base(relative), "identifier": path + relative,
			"chunkNumber": "1", "totalChunks": "1", "chunkSize": "3", "currentChunkSize": "3", "totalSize": "3",
		} {
			_ = w.WriteField(k, v)
		}
		part, _ := w.CreateFormFile("file", filepath.Base(relative))
		_, _ = part.Write([]byte("abc"))
		_ = w.Close()
		req := httptest.NewRequest(http.MethodPost, "/v2/casaos/file/upload", &body)
		req.Header.Set(echo.HeaderContentType, w.FormDataContentType())
		req.Header.Set("user_id", "4")
		rec := httptest.NewRecorder()
		if err := c.PostUploadFile(echo.New().NewContext(req, rec)); err != nil {
			t.Fatal(err)
		}
		return rec.Code
	}
	for _, u := range [][2]string{{media, "x.txt"}, {other, "x.txt"}, {kids, "out/x.txt"}} {
		if code := upload(u[0], u[1]); code != http.StatusForbidden {
			t.Errorf("upload %s + %s: %d, want 403", u[0], u[1], code)
		}
	}
	if _, err := os.Lstat(other + "/x.txt"); err == nil {
		t.Error("an upload landed outside")
	}
	if code := upload(kids, "school/a.txt"); code != http.StatusOK {
		t.Fatalf("upload in Kids: %d", code)
	}
	if b, err := os.ReadFile(kids + "/school/a.txt"); err != nil || string(b) != "abc" {
		t.Errorf("upload in Kids: %q %v", b, err)
	}
}
