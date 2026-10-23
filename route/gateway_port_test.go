package route

import "testing"

// An HttpPort left in casaos.conf by v0.3.6 was handed to the gateway at every start, and never cleared: the file was saved
// to an empty path. The port is the gateway's, set from the dashboard; the core leaves it alone.
func TestTheCoreDoesNotSetTheGatewayPortAtStart(t *testing.T) {
	if calledIn(t, "main")["ChangePort"] {
		t.Error("main changes the gateway's port")
	}
}
