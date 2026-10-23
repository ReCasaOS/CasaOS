package route

import "testing"

// The core's routes are created at the gateway in main.go, where no test sees them run: they wait
// for a gateway that is down at start like the core's first look at it does, instead of dying.
func TestTheRoutesWaitForTheGateway(t *testing.T) {
	if !calledIn(t, "main")["RetryGateway"] {
		t.Error("main creates its routes without waiting for the gateway")
	}
}
