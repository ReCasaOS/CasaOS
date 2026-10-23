package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
)

// startsIn moves the start's deadline for one test: the tests run long after the binary started.
func startsIn(t *testing.T, d time.Duration) {
	t.Helper()
	previous := StartDeadline
	StartDeadline = time.Now().Add(d)
	t.Cleanup(func() { StartDeadline = previous })
}

// The gateway is not up when the core starts (it restarts, or starts late at boot): its address file is
// there and it does not answer yet. The core waits for it instead of dying, which left it dead once
// systemd stopped restarting it.
func TestTheCoreWaitsForAGatewayThatIsDownAtStart(t *testing.T) {
	logger.LogInitConsoleOnly()
	startsIn(t, time.Minute)
	var pings atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if pings.Add(1) <= 2 { // not up yet
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(gateway.Close)
	runtimePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(runtimePath, "management.url"), []byte(gateway.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("the core died: %v", r)
		}
	}()

	if NewService(nil, runtimePath).Gateway() == nil {
		t.Fatal("no gateway")
	}
}

// The waits at start share one deadline: a gateway that never comes back ends the first wait with its
// last error, and a later wait (the routes, after the core's first look took all the time) does not try
// again on a clock of its own. systemd restarts the core.
func TestTheWaitsForTheGatewayShareOneDeadline(t *testing.T) {
	logger.LogInitConsoleOnly()
	startsIn(t, 2*time.Second)
	tries := 0
	down := func() error { tries++; return errors.New("the gateway is down") }

	if err := RetryGateway(down); err == nil || tries < 2 {
		t.Fatalf("the first wait: err %v after %d tries", err, tries)
	}
	time.Sleep(time.Until(StartDeadline))
	tries = 0
	start := time.Now()
	if err := RetryGateway(down); err == nil || tries != 0 || time.Since(start) > time.Second {
		t.Fatalf("past the deadline: err %v after %d tries in %s", err, tries, time.Since(start))
	}
}

// systemd gives up on a start after TimeoutStartSec, 90 s when the unit does not say, and the core says
// it is ready only once its routes are at the gateway and its event types at the bus. Nothing starts
// after StartDeadline, startWait after the process started, but what began before it ends: a look at
// the gateway (CasaOS-Common waits 10 s for its address file, then 30 s for its answer), then the one
// registration of the event types that is always made (5 s). The unit must give it all of that.
func TestTheUnitWaitsLongerThanTheCoreWaitsForTheGateway(t *testing.T) {
	unit, err := os.ReadFile("../build/sysroot/usr/lib/systemd/system/casaos.service")
	if err != nil {
		t.Fatal(err)
	}
	_, value, found := strings.Cut(string(unit), "\nTimeoutStartSec=")
	if !found {
		t.Fatal("the unit has no TimeoutStartSec: systemd gives up after 90 s")
	}
	value, _, _ = strings.Cut(value, "\n")
	timeout, err := time.ParseDuration(strings.Replace(strings.TrimSpace(value), "min", "m", 1))
	if longest := startWait + 40*time.Second + 5*time.Second; err != nil || timeout <= longest {
		t.Fatalf("TimeoutStartSec=%s (%v), the start can take %s", value, err, longest)
	}
}
