//go:build linux

package v1

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/fileroots"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/ReCasaOS/CasaOS/service"
	model2 "github.com/ReCasaOS/CasaOS/service/model"
	"github.com/labstack/echo/v4"
)

// the file routes as a confined box sees them: listing reads the disk, no share
type fakeRootsRepository struct{ fakeFileRepository }

func (fakeRootsRepository) System() service.SystemService { return fakeListing{} }
func (fakeRootsRepository) Shares() service.SharesService { return fakeNoShares{} }

type fakeListing struct{ service.SystemService }

func (fakeListing) GetDirPath(path string) ([]model.Path, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := []model.Path{}
	for _, e := range entries {
		out = append(out, model.Path{Name: e.Name(), Path: filepath.Join(path, e.Name()), IsDir: e.IsDir()})
	}
	return out, nil
}

func (fakeListing) MkdirAll(path string) (int, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return common_err.SERVICE_ERROR, err
	}
	return common_err.SUCCESS, nil
}

func (fakeListing) RenameFile(oldF, newF string) (int, error) {
	if err := os.Rename(oldF, newF); err != nil {
		return common_err.SERVICE_ERROR, err
	}
	return common_err.SUCCESS, nil
}

type fakeNoShares struct{ service.SharesService }

func (fakeNoShares) GetSharesList() []model2.SharesDBModel { return nil }

// a box with the root <tmp>/data and a secret next to it
func confinedBox(t *testing.T) (data, secret string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, secret = filepath.Join(base, "data"), filepath.Join(base, "secret")
	for _, dir := range []string{data + "/Media", secret} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{data + "/Media/a.txt", secret + "/key"} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../secret", data+"/escape"); err != nil {
		t.Fatal(err)
	}
	roots, err := fileroots.Open([]string{data})
	if err != nil {
		t.Fatal(err)
	}
	previous := fileroots.Default
	fileroots.Default = roots
	original := service.MyService
	service.MyService = fakeRootsRepository{fakeFileRepository{peers: &fakePeers{peers: map[string]model2.PeerDriveDBModel{}}}}
	t.Cleanup(func() { fileroots.Default, service.MyService = previous, original })
	return data, secret
}

func call(t *testing.T, handler echo.HandlerFunc, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	if err := handler(echo.New().NewContext(req, rec)); err != nil {
		t.Fatal(err)
	}
	return rec
}

func q(path string) string { return url.QueryEscape(path) }

func TestTheFileAPIStaysInItsRoots(t *testing.T) {
	data, secret := confinedBox(t)
	refused := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", name, rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{secret + "/key", data + "/escape/key", data + "/../secret/key", "/etc/passwd"} {
		refused("read "+path, call(t, GetFilerContent, http.MethodGet, "/v1/file/content?path="+q(path), nil))
		refused("download "+path, call(t, GetDownloadFile, http.MethodGet, "/v1/batch?files="+q(path), nil))
		refused("image "+path, call(t, GetFileImage, http.MethodGet, "/v1/image?path="+q(path), nil))
		refused("size "+path, call(t, GetSize, http.MethodGet, "/v1/folder/size", map[string]string{"path": path}))
	}
	refused("list outside", call(t, DirPath, http.MethodGet, "/v1/folder?path="+q(secret), nil))
	refused("list through a link", call(t, DirPath, http.MethodGet, "/v1/folder?path="+q(data+"/escape"), nil))
	refused("delete outside", call(t, DeleteFile, http.MethodDelete, "/v1/batch", []string{secret + "/key"}))
	refused("mkdir outside", call(t, MkdirAll, http.MethodPost, "/v1/folder", map[string]string{"path": secret + "/new"}))
	refused("create outside", call(t, PostCreateFile, http.MethodPost, "/v1/file", map[string]string{"path": data + "/escape/new"}))
	refused("rename out", call(t, RenamePath, http.MethodPut, "/v1/file/name", map[string]string{"old_path": data + "/Media/a.txt", "new_path": secret + "/a.txt"}))
	refused("save outside", call(t, PutFileContent, http.MethodPut, "/v1/file", model.FileUpdate{FilePath: secret + "/key", FileContent: "y"}))
	refused("copy out", call(t, PostOperateFileOrDir, http.MethodPost, "/v1/batch/task", model.FileOperate{Type: "copy", To: secret, Item: []model.FileItem{{From: data + "/Media/a.txt"}}}))
	refused("copy in", call(t, PostOperateFileOrDir, http.MethodPost, "/v1/batch/task", model.FileOperate{Type: "copy", To: data, Item: []model.FileItem{{From: secret + "/key"}}}))

	if b, _ := os.ReadFile(secret + "/key"); string(b) != "x" {
		t.Errorf("the secret changed: %q", b)
	}
	for _, p := range []string{secret + "/new", secret + "/a.txt"} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s was created", p)
		}
	}

	// inside: as before
	if rec := call(t, GetFilerContent, http.MethodGet, "/v1/file/content?path="+q(data+"/Media/a.txt"), nil); rec.Code != http.StatusOK {
		t.Errorf("read inside: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, MkdirAll, http.MethodPost, "/v1/folder", map[string]string{"path": data + "/Media/new/deeper"}); rec.Code != http.StatusOK {
		t.Errorf("mkdir inside: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAnUploadCannotLeaveItsRoot(t *testing.T) {
	data, secret := confinedBox(t)
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
		rec := httptest.NewRecorder()
		if err := PostFileUpload(echo.New().NewContext(req, rec)); err != nil {
			t.Fatal(err)
		}
		return rec.Code
	}
	for _, c := range [][2]string{{secret, "x.txt"}, {data, "../secret/x.txt"}, {data, "escape/x.txt"}} {
		if code := upload(c[0], c[1]); code != http.StatusForbidden {
			t.Errorf("upload %s + %s: %d, want 403", c[0], c[1], code)
		}
	}
	if _, err := os.Lstat(secret + "/x.txt"); err == nil {
		t.Error("an upload landed outside")
	}
	if code := upload(data+"/Media", "b.txt"); code != http.StatusOK {
		t.Errorf("upload inside: %d", code)
	}
}

// the dashboard's Root lists "/": only the way down to the roots
func TestAboveTheRootsOnlyTheWayDownIsListed(t *testing.T) {
	data, _ := confinedBox(t)
	parent := filepath.Dir(data)
	rec := call(t, DirPath, http.MethodGet, "/v1/folder?path="+q(parent), nil)
	var result struct {
		Data struct {
			Content []struct {
				Name string `json:"name"`
			} `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("%s: %v", rec.Body.String(), err)
	}
	names := []string{}
	for _, p := range result.Data.Content {
		names = append(names, p.Name)
	}
	if len(names) != 1 || names[0] != "data" {
		t.Fatalf("listing %s = %v, want only [data]", parent, names)
	}
}

// delete and rename work on the entry: a link that leads out is removed or
// renamed as a link; a root is neither
func TestDeleteAndRenameTakeTheEntryNotTheRoot(t *testing.T) {
	data, secret := confinedBox(t)
	status := func(rec *httptest.ResponseRecorder) int {
		t.Helper()
		var result model.Result
		_ = json.Unmarshal(rec.Body.Bytes(), &result)
		if rec.Code != http.StatusOK {
			return rec.Code
		}
		return result.Success
	}
	if got := status(call(t, RenamePath, http.MethodPut, "/v1/file/name", map[string]string{"old_path": data + "/escape", "new_path": data + "/escape2"})); got != common_err.SUCCESS {
		t.Errorf("rename a link out: %d, want success", got)
	}
	if got := status(call(t, DeleteFile, http.MethodDelete, "/v1/batch", []string{data + "/escape2"})); got != common_err.SUCCESS {
		t.Errorf("delete a link out: %d, want success", got)
	}
	if _, err := os.Lstat(data + "/escape2"); err == nil {
		t.Error("the link is still there")
	}
	if _, err := os.Stat(secret + "/key"); err != nil {
		t.Errorf("deleting the link removed what it led to: %v", err)
	}

	if err := os.Symlink("../secret", data+"/out"); err != nil {
		t.Fatal(err)
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"delete the root":        call(t, DeleteFile, http.MethodDelete, "/v1/batch", []string{data}),
		"delete above the root":  call(t, DeleteFile, http.MethodDelete, "/v1/batch", []string{filepath.Dir(data)}),
		"rename the root":        call(t, RenamePath, http.MethodPut, "/v1/file/name", map[string]string{"old_path": data, "new_path": data + "2"}),
		"rename onto the root":   call(t, RenamePath, http.MethodPut, "/v1/file/name", map[string]string{"old_path": data + "/Media", "new_path": data}),
		"delete in a folder out": call(t, DeleteFile, http.MethodDelete, "/v1/batch", []string{data + "/out/key"}),
	} {
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", name, rec.Code, rec.Body.String())
		}
	}
	if _, err := os.Stat(data + "/Media/a.txt"); err != nil {
		t.Errorf("the root lost its content: %v", err)
	}
}

// the chunks of an upload go to <folder>/.temp: a link planted there must not
// take them out
func TestAnUploadsChunksStayInTheRoot(t *testing.T) {
	data, secret := confinedBox(t)
	if err := os.Symlink("../secret", data+"/Media/.temp"); err != nil {
		t.Fatal(err)
	}
	query := "/v1/file/upload?" + url.Values{"path": {data + "/Media"}, "relativePath": {"big.iso"}, "filename": {"big.iso"}, "chunkNumber": {"1"}, "totalChunks": {"2"}}.Encode()
	if rec := call(t, GetFileUpload, http.MethodGet, query, nil); rec.Code != http.StatusForbidden {
		t.Errorf("chunk check through a planted .temp: %d %s, want 403", rec.Code, rec.Body.String())
	}
	entries, _ := os.ReadDir(secret)
	if len(entries) != 1 {
		t.Errorf("something was made outside: %v", entries)
	}
}

// a root not there (disk not mounted yet): 403 with the reason, no exit
func TestAMissingRootIsRefusedWithItsReason(t *testing.T) {
	data, _ := confinedBox(t)
	if err := os.Rename(data, data+".unmounted"); err != nil {
		t.Fatal(err)
	}
	rec := call(t, GetFilerContent, http.MethodGet, "/v1/file/content?path="+q(data+"/Media/a.txt"), nil)
	var result model.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	reason, _ := result.Data.(string)
	if rec.Code != http.StatusForbidden || reason == "" || reason != result.Message {
		t.Errorf("read below a missing root: %d %s, want 403 with the reason in message and data", rec.Code, rec.Body.String())
	}
}
