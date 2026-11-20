//go:build linux

package v1

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/fileaccess"
	"github.com/labstack/echo/v4"
)

// accountBox is confinedBox with an account given <data>/Kids to read and write and
// <data>/Media to read: <data>/Kids/k.txt, <data>/Media/a.txt, <data>/Other/o.txt, and a link
// <data>/Kids/out to <data>/Other.
func accountBox(t *testing.T) (data string) {
	t.Helper()
	data, _ = confinedBox(t)
	for dir, f := range map[string]string{"Kids": "k.txt", "Other": "o.txt"} {
		if err := os.MkdirAll(filepath.Join(data, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(data, dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../Other", filepath.Join(data, "Kids", "out")); err != nil {
		t.Fatal(err)
	}
	wasAccount, wasFolders := fileaccess.AccountOf, fileaccess.FoldersOf
	t.Cleanup(func() { fileaccess.AccountOf, fileaccess.FoldersOf = wasAccount, wasFolders })
	fileaccess.AccountOf = func(_, id string) (string, bool, bool) {
		if id == "1" {
			return "admin", false, true
		}
		return "user", false, true
	}
	fileaccess.FoldersOf = func(_, id string) []external.Folder {
		if id != "4" {
			return nil
		}
		return []external.Folder{{Path: data + "/Kids", Write: true}, {Path: data + "/Media"}}
	}
	return data
}

// callAs is call with the account id the token check sets.
func callAs(t *testing.T, id string, handler echo.HandlerFunc, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	if body == nil {
		b = nil
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(b))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("user_id", id)
	rec := httptest.NewRecorder()
	if err := handler(echo.New().NewContext(req, rec)); err != nil {
		t.Fatal(err)
	}
	return rec
}

// Every route of Files, for an account: nothing outside its folders, nothing written in a
// folder it may only read, a link in its folder does not lead out.
func TestAnAccountStaysInItsFolders(t *testing.T) {
	data := accountBox(t)
	refused := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", name, rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{data + "/Other/o.txt", data + "/Kids/out/o.txt", data + "/Kids/../Other/o.txt", data + "/Media/a.txt/../../Other/o.txt"} {
		refused("read "+path, callAs(t, "4", GetFilerContent, http.MethodGet, "/v1/file/content?path="+q(path), nil))
		refused("download "+path, callAs(t, "4", GetDownloadSingleFile, http.MethodGet, "/v1/file?path="+q(path), nil))
		refused("archive "+path, callAs(t, "4", GetDownloadFile, http.MethodGet, "/v1/batch?files="+q(path), nil))
		refused("image "+path, callAs(t, "4", GetFileImage, http.MethodGet, "/v1/image?path="+q(path), nil))
		refused("size "+path, callAs(t, "4", GetSize, http.MethodGet, "/v1/folder/size?path="+q(path), nil))
	}
	refused("list Other", callAs(t, "4", DirPath, http.MethodGet, "/v1/folder?path="+q(data+"/Other"), nil))
	refused("count Other", callAs(t, "4", GetFileCount, http.MethodGet, "/v1/folder/count?path="+q(data+"/Other"), nil))
	refused("list through the link", callAs(t, "4", DirPath, http.MethodGet, "/v1/folder?path="+q(data+"/Kids/out"), nil))

	// Media is read only
	refused("save in Media", callAs(t, "4", PutFileContent, http.MethodPut, "/v1/file", model.FileUpdate{FilePath: data + "/Media/a.txt", FileContent: "y"}))
	refused("create in Media", callAs(t, "4", PostCreateFile, http.MethodPost, "/v1/file", map[string]string{"path": data + "/Media/new"}))
	refused("mkdir in Media", callAs(t, "4", MkdirAll, http.MethodPost, "/v1/folder", map[string]string{"path": data + "/Media/new"}))
	refused("delete in Media", callAs(t, "4", DeleteFile, http.MethodDelete, "/v1/batch", []string{data + "/Media/a.txt"}))
	refused("rename in Media", callAs(t, "4", RenamePath, http.MethodPut, "/v1/file/name", map[string]string{"old_path": data + "/Media/a.txt", "new_path": data + "/Media/b.txt"}))
	refused("move out of Media", callAs(t, "4", PostOperateFileOrDir, http.MethodPost, "/v1/batch/task", model.FileOperate{Type: "move", To: data + "/Kids", Item: []model.FileItem{{From: data + "/Media/a.txt"}}}))
	refused("copy into Media", callAs(t, "4", PostOperateFileOrDir, http.MethodPost, "/v1/batch/task", model.FileOperate{Type: "copy", To: data + "/Media", Item: []model.FileItem{{From: data + "/Kids/k.txt"}}}))
	// outside
	refused("copy in from Other", callAs(t, "4", PostOperateFileOrDir, http.MethodPost, "/v1/batch/task", model.FileOperate{Type: "copy", To: data + "/Kids", Item: []model.FileItem{{From: data + "/Other/o.txt"}}}))
	refused("rename out", callAs(t, "4", RenamePath, http.MethodPut, "/v1/file/name", map[string]string{"old_path": data + "/Kids/k.txt", "new_path": data + "/Other/k.txt"}))
	refused("delete its folder", callAs(t, "4", DeleteFile, http.MethodDelete, "/v1/batch", []string{data + "/Kids"}))
	refused("upload chunk check outside", callAs(t, "4", GetFileUpload, http.MethodGet, "/v1/file/upload?path="+q(data+"/Other")+"&relativePath=x&chunkNumber=1&totalChunks=1&identifier=i", nil))

	if b, _ := os.ReadFile(data + "/Media/a.txt"); string(b) != "x" {
		t.Errorf("Media changed: %q", b)
	}
	for _, p := range []string{data + "/Media/new", data + "/Other/k.txt"} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s was made", p)
		}
	}

	// in its folders: as for the owner
	ok := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s, want 200", name, rec.Code, rec.Body.String())
		}
	}
	ok("read in Media", callAs(t, "4", GetFilerContent, http.MethodGet, "/v1/file/content?path="+q(data+"/Media/a.txt"), nil))
	ok("save in Kids", callAs(t, "4", PutFileContent, http.MethodPut, "/v1/file", model.FileUpdate{FilePath: data + "/Kids/k.txt", FileContent: "y"}))
	ok("mkdir in Kids", callAs(t, "4", MkdirAll, http.MethodPost, "/v1/folder", map[string]string{"path": data + "/Kids/new"}))
	ok("the owner reads Other", callAs(t, "1", GetFilerContent, http.MethodGet, "/v1/file/content?path="+q(data+"/Other/o.txt"), nil))
}

// The older upload is not open to an account, whatever the folder.
func TestAnAccountUploadsInItsWritableFoldersOnly(t *testing.T) {
	data := accountBox(t)
	upload := func(path, relative string) int {
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		for k, v := range map[string]string{"path": path, "relativePath": relative, "filename": filepath.Base(relative), "chunkNumber": "1", "totalChunks": "1"} {
			_ = w.WriteField(k, v)
		}
		part, _ := w.CreateFormFile("file", "x")
		_, _ = part.Write([]byte("uploaded"))
		_ = w.Close()
		req := httptest.NewRequest(http.MethodPost, "/v1/file/upload", &body)
		req.Header.Set(echo.HeaderContentType, w.FormDataContentType())
		req.Header.Set("user_id", "4")
		rec := httptest.NewRecorder()
		if err := PostFileUpload(echo.New().NewContext(req, rec)); err != nil {
			t.Fatal(err)
		}
		return rec.Code
	}
	for _, c := range [][2]string{{data + "/Media", "x.txt"}, {data + "/Other", "x.txt"}, {data + "/Kids", "out/x.txt"}, {data + "/Kids", "../Other/x.txt"}} {
		if code := upload(c[0], c[1]); code != http.StatusForbidden {
			t.Errorf("upload %s + %s: %d, want 403", c[0], c[1], code)
		}
	}
	// the older upload is the owner's: an account sends files by /v2/casaos/file/upload
	if code := upload(data+"/Kids", "b.txt"); code != http.StatusForbidden {
		t.Errorf("v1 upload in Kids: %d, want 403", code)
	}
}

// Above its folders an account sees only the way down; its roots are its folders.
func TestAnAccountSeesTheWayDownAndItsRoots(t *testing.T) {
	data := accountBox(t)
	rec := callAs(t, "4", DirPath, http.MethodGet, "/v1/folder?path="+q(data), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list above: %d %s", rec.Code, rec.Body.String())
	}
	var listing struct {
		Data struct {
			Content []struct {
				Name string `json:"name"`
			} `json:"content"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listing)
	names := map[string]bool{}
	for _, e := range listing.Data.Content {
		names[e.Name] = true
	}
	if !names["Kids"] || !names["Media"] || names["Other"] || len(names) != 2 {
		t.Errorf("way down: %v (%s)", names, rec.Body.String())
	}

	rec = callAs(t, "4", GetFileRoots, http.MethodGet, "/v1/file/roots", nil)
	var roots struct {
		Data struct {
			Roots    []string `json:"roots"`
			Writable []string `json:"writable"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &roots)
	if len(roots.Data.Roots) != 2 || len(roots.Data.Writable) != 1 || roots.Data.Writable[0] != data+"/Kids" {
		t.Errorf("roots: %s", rec.Body.String())
	}

	// an account with no folder reaches nothing, not even the way down
	if rec := callAs(t, "7", DirPath, http.MethodGet, "/v1/folder?path="+q(data), nil); rec.Code != http.StatusForbidden {
		t.Errorf("no folder, list: %d", rec.Code)
	}
	if rec := callAs(t, "7", GetFilerContent, http.MethodGet, "/v1/file/content?path="+q(data+"/Media/a.txt"), nil); rec.Code != http.StatusForbidden {
		t.Errorf("no folder, read: %d", rec.Code)
	}
}
