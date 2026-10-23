package route

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/service"
	model2 "github.com/ReCasaOS/CasaOS/service/model"
)

type fakeMountRepository struct {
	service.Repository
	connections *fakeConnections
}

func (f fakeMountRepository) Connections() service.ConnectionsService { return f.connections }
func (f fakeMountRepository) Storage() service.StorageService         { return fakeStorage{} }

type fakeConnections struct {
	service.ConnectionsService
	row     model2.ConnectionsDBModel
	deleted []string
	busy    []string // the mount points whose unmount fails
}

func (f *fakeConnections) UnmountSmaba(path string) error {
	if slices.Contains(f.busy, path) {
		return errors.New("device or resource busy")
	}
	return nil
}

func (f *fakeConnections) GetConnectionsList() []model2.ConnectionsDBModel {
	return []model2.ConnectionsDBModel{f.row}
}
func (f *fakeConnections) GetConnectionByID(string) model2.ConnectionsDBModel { return f.row }
func (f *fakeConnections) DeleteConnection(id string)                         { f.deleted = append(f.deleted, id) }

type fakeStorage struct{ service.StorageService }

func (fakeStorage) CheckAndMountAll() error { return nil }

// A saved connection whose shares cannot be read at boot (the server is off, or slow) stays saved,
// with its password: it was deleted, and the owner had to type it all again.
func TestAConnectionWhoseSharesCannotBeReadAtBootIsKept(t *testing.T) {
	logger.LogInitConsoleOnly()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	listener.Close() // the server is off
	connections := &fakeConnections{row: model2.ConnectionsDBModel{ID: 7, Host: "127.0.0.1", Port: port, Username: "user", Password: "secret"}}
	previousService, previousDelay := service.MyService, networkMountDelay
	service.MyService, networkMountDelay = fakeMountRepository{connections: connections}, 0
	t.Cleanup(func() { service.MyService, networkMountDelay = previousService, previousDelay })

	InitNetworkMount()

	if len(connections.deleted) != 0 {
		t.Fatalf("deleted %v", connections.deleted)
	}
}

// Before the shares of a host are mounted again, what was mounted in its folder is unmounted. A share
// that does not unmount keeps its folder and its files: with the share still mounted they are the
// server's, and the whole folder of the host was removed. A folder that is not empty stays too; one left
// empty and unmounted is removed.
func TestAShareThatDoesNotUnmountKeepsItsFiles(t *testing.T) {
	logger.LogInitConsoleOnly()
	host := t.TempDir()
	busy, full, old := filepath.Join(host, "Photos"), filepath.Join(host, "Music"), filepath.Join(host, "Old")
	for _, dir := range []string{busy, full, old} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{filepath.Join(busy, "a.jpg"), filepath.Join(full, "b.mp3")}
	for _, name := range files {
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	previous := service.MyService
	service.MyService = fakeMountRepository{connections: &fakeConnections{busy: []string{busy}}}
	t.Cleanup(func() { service.MyService = previous })

	if err := service.UnmountShares(host); err == nil {
		t.Error("the share that did not unmount is not reported")
	}

	for _, name := range files {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("%s is gone: %v", name, err)
		}
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the empty unmounted folder is still there: %v", err)
	}
}
