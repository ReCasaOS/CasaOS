package service

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
)

// The gateway is down when the core starts (it restarts, or starts late at boot): its address file is
// there, left by its last run, and nothing answers on it. The core waits for it instead of dying,
// which left it dead once systemd stopped restarting it.
func TestTheCoreWaitsForAGatewayThatIsDownAtStart(t *testing.T) {
	logger.LogInitConsoleOnly()
	runtimePath := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	if err := os.WriteFile(filepath.Join(runtimePath, "management.url"), []byte("http://"+address), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})}
	t.Cleanup(func() { server.Close() })
	go func() {
		time.Sleep(1500 * time.Millisecond)
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Error(err)
			return
		}
		server.Serve(listener)
	}()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("the core died: %v", r)
		}
	}()

	if NewService(nil, runtimePath).Gateway() == nil {
		t.Fatal("no gateway")
	}
}

// The wait is bounded: a gateway that never comes back ends it with its last error, and systemd
// restarts the core.
func TestTheWaitForTheGatewayEnds(t *testing.T) {
	logger.LogInitConsoleOnly()
	previous := gatewayWait
	gatewayWait = 2 * time.Second
	t.Cleanup(func() { gatewayWait = previous })
	tries := 0
	start := time.Now()

	err := RetryGateway(func() error { tries++; return errors.New("the gateway is down") })

	if err == nil || tries < 2 || time.Since(start) > 10*time.Second {
		t.Fatalf("err %v after %d tries in %s", err, tries, time.Since(start))
	}
}
