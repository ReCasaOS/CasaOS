package osverify

import "testing"

func TestTheDefaultRouteIsTheOneThatTheFactsSay(t *testing.T) {
	const up = "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\nenp3s0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n"
	const down = "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\nenp3s0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n"
	if got := DefaultRoute(up); got != "enp3s0" {
		t.Errorf("DefaultRoute(up) = %q", got)
	}
	if got := DefaultRoute(down); got != "" {
		t.Errorf("DefaultRoute(a table with no default route) = %q", got)
	}
}
