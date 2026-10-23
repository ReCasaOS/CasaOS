package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/ReCasaOS/CasaOS/service"
	model2 "github.com/ReCasaOS/CasaOS/service/model"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

// fakeFileRepository is the service as the file routes see it: peers kept in memory and a notifier that sends nothing.
type fakeFileRepository struct {
	service.Repository
	peers *fakePeers
}

func (f fakeFileRepository) Peer() service.PeerService    { return f.peers }
func (f fakeFileRepository) Notify() service.NotifyServer { return fakeNotify{} }
func (f fakeFileRepository) Connections() service.ConnectionsService {
	return fakeConnections{}
}

type fakeConnections struct{ service.ConnectionsService }

func (fakeConnections) GetConnectionsList() []model2.ConnectionsDBModel { return nil }

type fakeNotify struct{ service.NotifyServer }

func (fakeNotify) SendFileOperateNotify(bool) {}

type fakePeers struct {
	service.PeerService
	mu    sync.Mutex
	peers map[string]model2.PeerDriveDBModel
}

func (f *fakePeers) GetPeerByName(string) model2.PeerDriveDBModel { return model2.PeerDriveDBModel{} }

func (f *fakePeers) GetPeerByID(id string) model2.PeerDriveDBModel {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peers[id]
}

func (f *fakePeers) GetPeers() []model2.PeerDriveDBModel {
	f.mu.Lock()
	defer f.mu.Unlock()
	peers := []model2.PeerDriveDBModel{}
	for _, peer := range f.peers {
		peers = append(peers, peer)
	}
	return peers
}

func (f *fakePeers) CreatePeer(m *model2.PeerDriveDBModel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.peers[m.ID] = *m
}

func (f *fakePeers) DeletePeer(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.peers, id)
}

func useFakeFileService(t *testing.T) {
	t.Helper()
	original := service.MyService
	service.MyService = fakeFileRepository{peers: &fakePeers{peers: map[string]model2.PeerDriveDBModel{}}}
	t.Cleanup(func() { service.MyService = original })
}

func openFileCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// waitForNoGoroutineIn fails the test when a goroutine still runs fn a few seconds on.
func waitForNoGoroutineIn(t *testing.T, fn string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 1<<20)
	for {
		stacks := string(buf[:runtime.Stack(buf, true)])
		if !strings.Contains(stacks, fn) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a goroutine still runs %s:\n%s", fn, stacks)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func resultCode(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	var result model.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("%s: %v", rec.Body.String(), err)
	}
	return result.Success
}

func TestOperateRefusesADestinationThatIsASourceOrInsideOne(t *testing.T) {
	useFakeFileService(t)
	t.Cleanup(func() {
		service.OpStrArr = nil
		service.FileQueue.Range(func(k, _ interface{}) bool {
			service.FileQueue.Delete(k)
			return true
		})
	})
	root := filepath.Join(t.TempDir(), "missing") // nothing is copied should the check let a case through
	for name, to := range map[string]string{
		"into its own folder": root + "/Photos/2024",
		"onto itself":         root + "/Photos/",
		"into its own parent": root,
	} {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(model.FileOperate{Type: "copy", To: to, Item: []model.FileItem{{From: root + "/Music/a.mp3"}, {From: root + "/Photos"}}})
			req := httptest.NewRequest(http.MethodPost, "/v1/batch/task", bytes.NewReader(body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			if err := PostOperateFileOrDir(echo.New().NewContext(req, rec)); err != nil {
				t.Fatal(err)
			}
			if got := resultCode(t, rec); got != common_err.SOURCE_DES_SAME || rec.Code != http.StatusConflict {
				t.Errorf("answer %d (HTTP %d), want %d (source and destination the same, HTTP 409)", got, rec.Code, common_err.SOURCE_DES_SAME)
			}
			if len(service.OpStrArr) != 0 {
				t.Errorf("the task was queued: %v", service.OpStrArr)
			}
		})
	}
}

func postOperate(t *testing.T, op model.FileOperate) *httptest.ResponseRecorder {
	t.Helper()
	t.Cleanup(func() {
		service.FileQueueLock.Lock()
		service.OpStrArr = nil
		service.FileQueue.Range(func(k, _ interface{}) bool {
			service.FileQueue.Delete(k)
			return true
		})
		service.FileQueueLock.Unlock()
	})
	body, _ := json.Marshal(op)
	req := httptest.NewRequest(http.MethodPost, "/v1/batch/task", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	if err := PostOperateFileOrDir(echo.New().NewContext(req, rec)); err != nil {
		t.Fatal(err)
	}
	return rec
}

// The entry a paste writes is to/<name of the source>: it must not be the source's own folder.
func TestOperateRefusesAPasteWhoseEntryHoldsTheSource(t *testing.T) {
	useFakeFileService(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Downloads", "Movies", "Movies"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Photos"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, op := range map[string]model.FileOperate{
		"Movies/Movies pasted in Downloads": {Type: "move", Style: "overwrite", To: root + "/Downloads", Item: []model.FileItem{{From: root + "/Downloads/Movies/Movies"}}},
		"into a link to its own folder":     {Type: "copy", Style: "overwrite", To: root + "/link", Item: []model.FileItem{{From: root + "/Photos"}}},
	} {
		t.Run(name, func(t *testing.T) {
			rec := postOperate(t, op)
			if got := resultCode(t, rec); got != common_err.SOURCE_DES_SAME {
				t.Errorf("answer %d (HTTP %d), want %d", got, rec.Code, common_err.SOURCE_DES_SAME)
			}
		})
	}
}

func mountForTest(t *testing.T, source, target, fstype string, flags uintptr) {
	t.Helper()
	if err := syscall.Mount(source, target, fstype, flags, ""); err != nil {
		t.Skipf("cannot mount here: %v", err)
	}
	t.Cleanup(func() { syscall.Unmount(target, syscall.MNT_DETACH) })
}

func TestDeletingAFolderThatHoldsAMountedDiskDeletesNothing(t *testing.T) {
	useFakeFileService(t)
	folder := filepath.Join(t.TempDir(), "folder")
	usb := filepath.Join(folder, "usb")
	if err := os.MkdirAll(usb, 0o755); err != nil {
		t.Fatal(err)
	}
	mountForTest(t, "tmpfs", usb, "tmpfs", 0)
	if err := os.WriteFile(filepath.Join(usb, "keep.txt"), []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal([]string{folder})
	req := httptest.NewRequest(http.MethodDelete, "/v1/file/delete", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	if err := DeleteFile(echo.New().NewContext(req, rec)); err != nil {
		t.Fatal(err)
	}

	if got := resultCode(t, rec); got != common_err.MOUNTED_DIRECTIORIES {
		t.Errorf("answer %d, want %d (a mounted directory)", got, common_err.MOUNTED_DIRECTIORIES)
	}
	if _, err := os.Stat(filepath.Join(usb, "keep.txt")); err != nil {
		t.Errorf("the mounted disk lost keep.txt: %v", err)
	}
}

func putFileContent(t *testing.T, path, content string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(model.FileUpdate{FilePath: path, FileContent: content})
	req := httptest.NewRequest(http.MethodPut, "/v1/file", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	if err := PutFileContent(echo.New().NewContext(req, rec)); err != nil {
		t.Fatal(err)
	}
	if got := resultCode(t, rec); got != common_err.SUCCESS {
		t.Fatalf("answer %d: %s", got, rec.Body.String())
	}
	return rec
}

// A file is replaced whole, so that a crash or a full disk while it is written leaves the old one.
func TestSavingAFileReplacesItWholeAndKeepsItsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yml")
	if err := os.WriteFile(path, []byte("a longer old content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	putFileContent(t, path, "new")

	if got, err := os.ReadFile(path); err != nil || string(got) != "new" {
		t.Errorf("the file holds %q (%v), want new", got, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode() != 0o640 {
		t.Errorf("mode %v, want -rw-r-----", after.Mode())
	}
	if os.SameFile(before, after) {
		t.Error("the file was written in place")
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("the folder holds %d files, want the file alone", len(entries))
	}
}

func TestSavingAFileLeavesNoFileOpen(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(-1)) // a forgotten *os.File is closed by the GC: keep it from hiding one
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := openFileCount(t)

	for i := 0; i < 3; i++ {
		putFileContent(t, path, fmt.Sprint(i))
	}

	if after := openFileCount(t); after != before {
		t.Errorf("%d files open after three saves, want %d", after, before)
	}
}

func TestSavingAFileThroughALinkWritesItsTarget(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "target.conf"), filepath.Join(dir, "link.conf")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	putFileContent(t, link, "new")

	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link is no longer a link (%v)", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new" {
		t.Errorf("the target holds %q, want new", got)
	}
}

func TestSavingAFileKeepsItsOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root can give a file to someone else")
	}
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 1234, 5678); err != nil {
		t.Fatal(err)
	}

	putFileContent(t, path, "new")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st := info.Sys().(*syscall.Stat_t); st.Uid != 1234 || st.Gid != 5678 {
		t.Errorf("owner %d:%d, want 1234:5678", st.Uid, st.Gid)
	}
}

// postSingleChunk uploads content as dir/name in one chunk.
func postSingleChunk(t *testing.T, dir, name, content string) {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for field, value := range map[string]string{"path": dir, "relativePath": name, "filename": name, "totalChunks": "1", "chunkNumber": "1"} {
		form.WriteField(field, value)
	}
	part, _ := form.CreateFormFile("file", name)
	part.Write([]byte(content))
	form.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/file/upload", &body)
	req.Header.Set(echo.HeaderContentType, form.FormDataContentType())
	rec := httptest.NewRecorder()
	if err := PostFileUpload(echo.New().NewContext(req, rec)); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
}

func TestASingleChunkUploadReplacesTheWholeFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("an older and longer file"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}

	postSingleChunk(t, dir, "a.txt", "new")

	if got, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(got) != "new" {
		t.Errorf("the file holds %q (%v), want new", got, err)
	}
	if after, err := os.Stat(filepath.Join(dir, "a.txt")); err == nil && os.SameFile(before, after) {
		t.Error("the file was written in place: an upload cut short leaves it half written")
	}
}

func TestASingleChunkUploadOverAFileKeepsItsModeAndOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.conf")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := os.Geteuid() == 0 // only root can give the file to someone else
	if root {
		if err := os.Chown(path, 1000, 1000); err != nil {
			t.Fatal(err)
		}
	}

	postSingleChunk(t, dir, "a.conf", "new")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != 0o600 {
		t.Errorf("mode %v, want -rw-------", info.Mode())
	}
	if st := info.Sys().(*syscall.Stat_t); root && (st.Uid != 1000 || st.Gid != 1000) {
		t.Errorf("owner %d:%d, want 1000:1000", st.Uid, st.Gid)
	}
}

func TestASingleChunkUploadThroughALinkWritesItsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.conf")); err != nil {
		t.Fatal(err)
	}

	postSingleChunk(t, dir, "link.conf", "new")

	if info, err := os.Lstat(filepath.Join(dir, "link.conf")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link is no longer a link (%v)", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new" {
		t.Errorf("the target holds %q, want new", got)
	}
}

func TestASingleChunkUploadOverAFileWithHardLinksWritesItInPlace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.conf"), []byte("an older and longer content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, "a.conf"), filepath.Join(dir, "b.conf")); err != nil {
		t.Fatal(err)
	}

	postSingleChunk(t, dir, "a.conf", "new")

	if got, _ := os.ReadFile(filepath.Join(dir, "b.conf")); string(got) != "new" {
		t.Errorf("the other name holds %q, want new", got)
	}
}

// An upload onto a link that leads nowhere is a new file there, as it was.
func TestASingleChunkUploadOntoALinkToNothingWritesANewFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "a.conf")); err != nil {
		t.Fatal(err)
	}

	postSingleChunk(t, dir, "a.conf", "new")

	if got, err := os.ReadFile(filepath.Join(dir, "a.conf")); err != nil || string(got) != "new" {
		t.Errorf("a.conf holds %q (%v), want new", got, err)
	}
}

func TestCancellingATaskThroughTheRouteTakesItOutOfTheQueue(t *testing.T) {
	useFakeFileService(t)
	t.Cleanup(func() {
		service.FileQueueLock.Lock()
		service.OpStrArr = nil
		service.FileQueue.Range(func(k, _ interface{}) bool {
			service.FileQueue.Delete(k)
			return true
		})
		service.FileQueueLock.Unlock()
	})
	service.FileQueueLock.Lock()
	service.FileQueue.Store("waiting", model.FileOperate{Type: "copy", Style: "overwrite", To: t.TempDir(), TotalSize: 1, Item: []model.FileItem{{From: filepath.Join(t.TempDir(), "missing")}}})
	service.OpStrArr = []string{"waiting"}
	service.FileQueueLock.Unlock()
	req := httptest.NewRequest(http.MethodDelete, "/v1/batch/waiting/task", nil)
	c := echo.New().NewContext(req, httptest.NewRecorder())
	c.SetParamNames("id")
	c.SetParamValues("waiting")

	if err := DeleteOperateFileOrDir(c); err != nil {
		t.Fatal(err)
	}

	service.FileQueueLock.Lock()
	defer service.FileQueueLock.Unlock()
	if _, ok := service.FileQueue.Load("waiting"); ok || len(service.OpStrArr) != 0 {
		t.Errorf("the task is still queued: %v", service.OpStrArr)
	}
}

func TestAnUploadWithoutAFileLeavesTheFileAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for field, value := range map[string]string{"path": dir, "relativePath": "a.txt", "filename": "a.txt", "totalChunks": "1", "chunkNumber": "1"} {
		form.WriteField(field, value)
	}
	form.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/file/upload", &body)
	req.Header.Set(echo.HeaderContentType, form.FormDataContentType())
	rec := httptest.NewRecorder()

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("the handler panicked: %v", r)
			}
		}()
		PostFileUpload(echo.New().NewContext(req, rec))
	}()

	if rec.Code != http.StatusBadRequest {
		t.Errorf("HTTP %d, want 400", rec.Code)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(got) != "precious" {
		t.Errorf("the file now holds %q", got)
	}
}

// A file with several names (a hard link, as a single-file volume of an app may be) is written in place, so that every name sees the change.
func TestSavingAFileWithHardLinksWritesItInPlace(t *testing.T) {
	dir := t.TempDir()
	path, other := filepath.Join(dir, "a.conf"), filepath.Join(dir, "b.conf")
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, other); err != nil {
		t.Fatal(err)
	}

	putFileContent(t, path, "new")

	if got, _ := os.ReadFile(other); string(got) != "new" {
		t.Errorf("the other name holds %q, want new", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode() != 0o640 {
		t.Errorf("mode %v (%v), want -rw-r-----", info.Mode(), err)
	}
}

func TestAFailedWebSocketUpgradeStartsNothing(t *testing.T) {
	useFakeFileService(t)
	rec := httptest.NewRecorder()

	if err := ConnectWebSocket(echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/v1/file/ws", nil), rec)); err != nil {
		t.Fatal(err)
	}

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
	waitForNoGoroutineIn(t, "(*Client).writePump")
	waitForNoGoroutineIn(t, "(*Client).readPump")
}

func dialFileWebSocket(t *testing.T, url, peer string) *websocket.Conn {
	header := http.Header{"Cookie": {"peerid=" + peer}}
	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Error(err)
		return nil
	}
	// the first message comes from the writer of the client: it runs
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Error(err)
	}
	return conn
}

func fileWebSocketServer(t *testing.T) string {
	t.Helper()
	e := echo.New()
	e.GET("/ws", ConnectWebSocket)
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

func TestAWebSocketClientThatLeavesStopsItsWriter(t *testing.T) {
	useFakeFileService(t)
	url := fileWebSocketServer(t)

	conn := dialFileWebSocket(t, url, "leaving")
	if conn == nil {
		return
	}
	conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"disconnect"}`))
	conn.Close()

	waitForNoGoroutineIn(t, "(*Client).writePump")
}

func TestManyWebSocketClientsComingAndGoingAtOnce(t *testing.T) {
	useFakeFileService(t)
	url := fileWebSocketServer(t)
	stop := make(chan struct{})
	readers := sync.WaitGroup{}
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
				GetPeers(echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/v1/file/peers", nil), httptest.NewRecorder()))
			}
		}
	}()

	clients := sync.WaitGroup{}
	for i := 0; i < 20; i++ {
		clients.Add(1)
		go func(i int) {
			defer clients.Done()
			for j := 0; j < 5; j++ {
				conn := dialFileWebSocket(t, url, fmt.Sprintf("peer-%d", i))
				if conn == nil {
					return
				}
				conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"signal","to":"peer-%d"}`, (i+1)%20)))
				conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"hello"}`))
				if j%2 == 0 {
					conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"disconnect"}`))
				}
				conn.Close()
			}
		}(i)
	}
	clients.Wait()
	close(stop)
	readers.Wait()

	waitForNoGoroutineIn(t, "(*Client).writePump")
	waitForNoGoroutineIn(t, "(*Client).readPump")
}
