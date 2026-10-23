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
			if got := resultCode(t, rec); got != common_err.SOURCE_DES_SAME {
				t.Errorf("answer %d, want %d (source and destination the same)", got, common_err.SOURCE_DES_SAME)
			}
			if len(service.OpStrArr) != 0 {
				t.Errorf("the task was queued: %v", service.OpStrArr)
			}
		})
	}
}

func TestIsSameOrInsideGoesByWholeNames(t *testing.T) {
	for _, c := range []struct {
		path, dir string
		want      bool
	}{
		{"/DATA/Photo", "/DATA/Photo", true},
		{"/DATA/Photo/2024", "/DATA/Photo", true},
		{"/DATA/Photo2", "/DATA/Photo", false},
		{"/DATA", "/DATA/Photo", false},
		{"/DATA/..Photo", "/DATA", true},
		{"/DATA", "/", true},
		{"DATA/Photo", "/DATA", true}, // cannot be compared: refused
	} {
		if got := isSameOrInside(c.path, c.dir); got != c.want {
			t.Errorf("isSameOrInside(%q, %q) = %v, want %v", c.path, c.dir, got, c.want)
		}
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

func TestASingleChunkUploadReplacesTheWholeFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("an older and longer file"), 0o644); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for field, value := range map[string]string{"path": dir, "relativePath": "a.txt", "filename": "a.txt", "totalChunks": "1", "chunkNumber": "1"} {
		form.WriteField(field, value)
	}
	part, _ := form.CreateFormFile("file", "a.txt")
	part.Write([]byte("new"))
	form.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/file/upload", &body)
	req.Header.Set(echo.HeaderContentType, form.FormDataContentType())
	rec := httptest.NewRecorder()

	if err := PostFileUpload(echo.New().NewContext(req, rec)); err != nil {
		t.Fatal(err)
	}

	if got, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(got) != "new" {
		t.Errorf("the file holds %q (%v), want new", got, err)
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
