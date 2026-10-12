package osreadiness

import (
	"io/fs"
	"testing"
)

// defaultRoute is a machine whose default route goes out by an interface.
func defaultRoute(iface string) func(*fakeProbe) {
	return func(f *fakeProbe) {
		f.set("/proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n"+
			"docker0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"+
			iface+"\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n")
	}
}

// card is a network card of the machine, with the files of /sys/class/net/<name>.
func card(name string, files map[string]string) func(*fakeProbe) {
	return func(f *fakeProbe) {
		for file, content := range files {
			f.set("/sys/class/net/"+name+"/"+file, content)
		}
	}
}

var (
	// wifiCard is a Wi-Fi card, and ethernetCard one that has a cable in it
	wifiCard     = map[string]string{"wireless/status": "0x0000\n", "device/vendor": "0x8086\n", "type": "1\n", "carrier": "1\n"}
	ethernetCard = map[string]string{"device/vendor": "0x8086\n", "type": "1\n", "carrier": "1\n"}
)

func TestNetworkUplink(t *testing.T) {
	runScenarios(t, "network_uplink", []scenario{
		{name: "a cable is the way out", severity: OK},
		{name: "Wi-Fi, and nothing else", setup: after(defaultRoute("wlp2s0"), card("wlp2s0", wifiCard)), severity: Refuse, data: D{"interface": "wlp2s0"}},
		{name: "Wi-Fi with another name for the marker", setup: after(defaultRoute("wlan0"), card("wlan0", map[string]string{"phy80211/index": "0\n", "type": "1\n"})), severity: Refuse, data: D{"interface": "wlan0"}},
		{name: "Wi-Fi, and a cable that has a carrier", setup: after(defaultRoute("wlp2s0"), card("wlp2s0", wifiCard), card("enp3s0", ethernetCard)), severity: OK},
		{name: "Wi-Fi, and a cable that has none", setup: after(defaultRoute("wlp2s0"), card("wlp2s0", wifiCard), card("enp3s0", map[string]string{"device/vendor": "x", "type": "1\n", "carrier": "0\n"})), severity: Refuse},
		{name: "Wi-Fi, and a card that is down and cannot say", setup: after(defaultRoute("wlp2s0"), card("wlp2s0", wifiCard), card("enp3s0", map[string]string{"device/vendor": "x", "type": "1\n"}), func(f *fakeProbe) {
			f.fail("/sys/class/net/enp3s0/carrier", &fs.PathError{Op: "read", Path: "carrier", Err: fs.ErrInvalid})
		}), severity: Refuse},
		{name: "Wi-Fi, and a virtual interface that says it has a carrier", setup: after(defaultRoute("wlp2s0"), card("wlp2s0", wifiCard), card("docker0", map[string]string{"type": "1\n", "carrier": "1\n"}), card("veth12ab", map[string]string{"type": "1\n", "carrier": "1\n"})), severity: Refuse},
		{name: "Wi-Fi, and another Wi-Fi card that has a carrier", setup: after(defaultRoute("wlp2s0"), card("wlp2s0", wifiCard), card("wlp3s0", wifiCard)), severity: Refuse},
		{name: "Wi-Fi, and an interface that is not Ethernet", setup: after(defaultRoute("wlp2s0"), card("wlp2s0", wifiCard), card("lo", map[string]string{"device/x": "x", "type": "772\n", "carrier": "1\n"})), severity: Refuse},
		{name: "Wi-Fi, and a cable's card whose name is not an interface's", setup: after(defaultRoute("wlp2s0"), card("wlp2s0", wifiCard), card("en p3s0", ethernetCard)), severity: Refuse},
		{name: "no default route", setup: func(f *fakeProbe) {
			f.set("/proc/net/route", "Iface\tDestination\tGateway\nenp3s0\t000011AC\t00000000\n")
		}, severity: OK},
		{name: "no /proc/net/route", setup: func(f *fakeProbe) { f.remove("/proc/net/route") }, severity: OK},
		{name: "a default route out by a tunnel", setup: defaultRoute("tailscale0"), severity: OK},
		{name: "a default route out by something that is no interface's name", setup: defaultRoute("../../etc"), severity: OK},
		{name: "a default route out by a step that stays or goes back", setup: after(defaultRoute(".."), card("..", wifiCard)), severity: OK},
		{name: "a default route out by a dot", setup: defaultRoute("."), severity: OK},
		{name: "/proc/net/route that cannot be read", setup: func(f *fakeProbe) {
			f.fail("/proc/net/route", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
		{name: "a card that cannot be looked at", setup: after(defaultRoute("wlp2s0"), func(f *fakeProbe) {
			f.fail("/sys/class/net/wlp2s0/wireless", &fs.PathError{Op: "stat", Path: "x", Err: fs.ErrPermission})
		}), severity: Refuse, unknown: ReasonUnreadable},
		{name: "the list of the cards that cannot be read", setup: after(defaultRoute("wlp2s0"), card("wlp2s0", wifiCard), func(f *fakeProbe) {
			f.fail("/sys/class/net", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
		}), severity: Refuse, unknown: ReasonUnreadable},
		{name: "a cable that cannot be looked at for being a physical card", setup: after(defaultRoute("wlp2s0"), card("wlp2s0", wifiCard), card("enp3s0", ethernetCard), func(f *fakeProbe) {
			f.fail("/sys/class/net/enp3s0/device", &fs.PathError{Op: "stat", Path: "x", Err: fs.ErrPermission})
		}), severity: Refuse, unknown: ReasonUnreadable},
	})
}

func TestNetworkNames(t *testing.T) {
	// said on every hop: the step before the restart is the one that does it, and nothing is claimed here
	for name, setup := range map[string]func(*fakeProbe){"debian 11": nil, "debian 12": debian12, "ubuntu 22.04": ubuntu2204, "ubuntu 24.04": ubuntu2404} {
		f := newBox(t)
		if setup != nil {
			setup(f)
		}
		got := checkOn(t, f, "network_names")
		if got.Severity != Info || got.Action != ActionNone || got.Detail == "" {
			t.Errorf("%s: finding = %+v", name, got)
		}
	}
}
