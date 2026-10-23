package v1

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/service"
	model2 "github.com/ReCasaOS/CasaOS/service/model"
	"github.com/labstack/echo/v4"
)

type fakeSambaRepository struct {
	service.Repository
	connections *fakeSambaConnections
}

func (f fakeSambaRepository) Connections() service.ConnectionsService { return f.connections }

type fakeSambaConnections struct {
	service.ConnectionsService
	row     model2.ConnectionsDBModel
	busy    []string // the mount points whose unmount fails
	deleted []string
	created int
}

func (f *fakeSambaConnections) GetConnectionByID(string) model2.ConnectionsDBModel { return f.row }
func (f *fakeSambaConnections) GetConnectionByHost(string) []model2.ConnectionsDBModel {
	return nil
}
func (f *fakeSambaConnections) DeleteConnection(id string)                  { f.deleted = append(f.deleted, id) }
func (f *fakeSambaConnections) CreateConnection(*model2.ConnectionsDBModel) { f.created++ }
func (f *fakeSambaConnections) UnmountSmaba(path string) error {
	if slices.Contains(f.busy, path) {
		return os.ErrPermission
	}
	return nil
}

// a port on which nothing answers: the server is off
func serverOff(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

func withSambaConnections(t *testing.T, connections *fakeSambaConnections) {
	t.Helper()
	original := service.MyService
	t.Cleanup(func() { service.MyService = original })
	service.MyService = fakeSambaRepository{connections: connections}
}

func deleteConnection(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodDelete, "/v1/samba/connections/7", nil), recorder)
	ctx.SetParamNames("id")
	ctx.SetParamValues("7")
	if err := DeleteSambaConnections(ctx); err != nil {
		t.Fatal(err)
	}
	return recorder
}

// A connection is deleted without asking the server for its shares: a server that is off, or whose
// password changed, gave a 500 and a connection that could never be deleted (the boot keeps it, a new
// one to the same host is refused, and none can be edited). What is mounted is read from its folder.
func TestAConnectionToAServerThatIsOffCanBeDeleted(t *testing.T) {
	folder := t.TempDir()
	empty, full := filepath.Join(folder, "Music"), filepath.Join(folder, "Notes")
	for _, dir := range []string{empty, full} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(full, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	connections := &fakeSambaConnections{row: model2.ConnectionsDBModel{ID: 7, Username: "user", Host: "127.0.0.1", Port: serverOff(t), MountPoint: folder}}
	withSambaConnections(t, connections)

	if recorder := deleteConnection(t); recorder.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", recorder.Code, recorder.Body)
	}
	if !slices.Equal(connections.deleted, []string{"7"}) {
		t.Fatalf("deleted %v", connections.deleted)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Errorf("the empty unmounted folder is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(full, "a.txt")); err != nil {
		t.Errorf("a folder that was not empty lost its file: %v", err)
	}
}

// A share that does not unmount keeps its folder and its files, and the connection stays, so that the
// owner can try again.
func TestAConnectionWhoseShareDoesNotUnmountIsKept(t *testing.T) {
	folder := t.TempDir()
	busy := filepath.Join(folder, "Photos")
	if err := os.Mkdir(busy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(busy, "a.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	connections := &fakeSambaConnections{row: model2.ConnectionsDBModel{ID: 7, Username: "user", Host: "127.0.0.1", Port: serverOff(t), MountPoint: folder}, busy: []string{busy}}
	withSambaConnections(t, connections)

	if recorder := deleteConnection(t); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("answered %d: %s", recorder.Code, recorder.Body)
	}
	if len(connections.deleted) != 0 {
		t.Fatalf("deleted %v", connections.deleted)
	}
	if _, err := os.Stat(filepath.Join(busy, "a.jpg")); err != nil {
		t.Fatalf("the busy share lost its file: %v", err)
	}
}

// A user name or a port that would add a mount option is refused before the server is asked or anything
// is saved, with a 400 that says why.
func TestACreateThatWouldAddAMountOptionIsRefused(t *testing.T) {
	port := serverOff(t)
	for _, body := range []string{
		`{"username":"user,uid=0","password":"x","host":"127.0.0.1","port":"` + port + `"}`,
		`{"username":"user","password":"x","host":"127.0.0.1","port":"445,uid=0"}`,
	} {
		connections := &fakeSambaConnections{}
		withSambaConnections(t, connections)
		request := httptest.NewRequest(http.MethodPost, "/v1/samba/connections", strings.NewReader(body))
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		recorder := httptest.NewRecorder()
		if err := PostSambaConnectionsCreate(echo.New().NewContext(request, recorder)); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusBadRequest || connections.created != 0 || !strings.Contains(recorder.Body.String(), "user name") && !strings.Contains(recorder.Body.String(), "port") {
			t.Errorf("%s: answered %d %s, created %d", body, recorder.Code, recorder.Body, connections.created)
		}
	}
}
