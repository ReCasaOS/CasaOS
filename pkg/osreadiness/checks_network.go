package osreadiness

import (
	"context"
	"slices"
	"strings"
)

func init() {
	register(
		Check{Code: "network_uplink", Group: GroupNetwork, Title: "The machine is reached by a cable", Run: checkUplink},
		Check{Code: "network_names", Group: GroupNetwork, Title: "The names of the network cards are not checked", Run: checkNetworkNames},
	)
}

const netDir = "/sys/class/net/"

// wireless says whether an interface is a Wi-Fi card's.
func wireless(ctx context.Context, p Probe, iface string) (bool, error) {
	for _, marker := range []string{"/wireless", "/phy80211"} {
		found, err := exists(ctx, p, netDir+iface+marker)
		if err != nil || found {
			return found, err
		}
	}
	return false, nil
}

// wiredWithCarrier says whether an interface is a card with a cable in it: a physical Ethernet
// interface, not a Wi-Fi one, whose carrier is on. A card that is down says nothing when asked for
// its carrier, which is a card without a cable.
func wiredWithCarrier(ctx context.Context, p Probe, iface string) (bool, error) {
	if physical, err := exists(ctx, p, netDir+iface+"/device"); err != nil || !physical {
		return false, err
	}
	if wifi, err := wireless(ctx, p, iface); err != nil || wifi {
		return false, err
	}
	// what cannot be read is a card that is down, or one that is not Ethernet: neither has a cable
	kind, _, _ := readText(ctx, p, netDir+iface+"/type")
	carrier, _, _ := readText(ctx, p, netDir+iface+"/carrier")
	return kind == "1" && carrier == "1", nil
}

func checkUplink(ctx context.Context, p Probe) Result {
	iface, err := defaultRouteInterface(ctx, p)
	if err != nil {
		return unknown(err)
	}
	if iface == "" {
		return pass()
	}
	wifi, err := wireless(ctx, p, iface)
	if err != nil {
		return unknown(err)
	}
	if !wifi {
		return pass()
	}
	others, err := p.ReadDir(ctx, strings.TrimSuffix(netDir, "/"))
	if err != nil && !isNotExist(err) {
		return unknown(err)
	}
	slices.Sort(others)
	for _, other := range others {
		if !validName(interfaceName, other) {
			continue
		}
		wired, err := wiredWithCarrier(ctx, p, other)
		if err != nil {
			return unknown(err)
		}
		if wired {
			return pass()
		}
	}
	return Result{
		Severity: Refuse, Title: "The machine is reached over Wi-Fi only",
		Detail: "The route to the network goes over a Wi-Fi card, and no cable is plugged in. An upgrade changes the drivers, the firmware and the network manager that Wi-Fi depends on, and a machine that loses its network is out of reach. Plug in a cable first.",
		Data:   D{"interface": iface},
	}
}

// checkNetworkNames only says that nobody looks: a new release can name a card otherwise, and nothing here, in the unit or after the restart reads the names or pins them.
func checkNetworkNames(context.Context, Probe) Result {
	return Result{
		Severity: Info, Title: "The names of the network cards are not checked",
		Detail: "A new release can name the network cards differently, and a card that has another name than the one the machine's settings give it has no network. Nothing in the upgrade looks at the names or pins them: write down the names now (ip -br link), and be where you can see the machine's console when it restarts.",
		Action: ActionNone,
	}
}
