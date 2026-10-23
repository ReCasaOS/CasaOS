package route

import (
	"net"
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
