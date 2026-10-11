package osreadiness

import (
	"context"
	"time"
)

// The two states of a hop that are not about the machine: when the vendor lets it be done, and
// whether the vendor offers it. They are checks so that the page shows them where it shows the
// rest, by code.
func init() {
	register(
		Check{Code: "hold_until", Group: GroupHop, Title: "The upgrade to the next LTS is open", Hops: []HopID{HopUbuntu2404to2604}, Run: checkHoldUntil},
		Check{Code: "not_offered", Group: GroupHop, Title: "Canonical offers the upgrade", Hops: ubuntuHops, Run: checkNotOffered},
	)
}

// holdUntil is the day the upgrade from Ubuntu 24.04 to 26.04 is let be done: sixty days after
// Canonical opened it (2026-09-29), the time that the first LTS upgrade of a release is left to
// settle in, which the 24.04.1 upgrade needed (it was suspended in September 2024).
var holdUntil = time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)

func checkHoldUntil(_ context.Context, p Probe) Result {
	if !p.Now().Before(holdUntil) {
		return pass()
	}
	return Result{
		Severity: Warn, Title: "This upgrade is not to be made before " + holdUntil.Format("2006-01-02"),
		Detail: "Canonical opened the upgrade to Ubuntu 26.04 only recently, and the first upgrades of a new LTS are the ones that get suspended. It waits until " + holdUntil.Format("2006-01-02") + ".",
		Data:   D{"not_before": holdUntil.Format("2006-01-02")}, Action: ActionNone,
	}
}

// checkNotOffered is Ubuntu's: Canonical decides when a release is offered, by metadata that it
// serves, and the report does not ask the network.
func checkNotOffered(context.Context, Probe) Result {
	return Result{
		Severity: Info, Title: "Whether Canonical offers this upgrade is not checked here",
		Detail: "Canonical opens each upgrade when it thinks it ready, and says so in metadata that is read from the network. The plan step asks for it; this report does not.",
		Action: ActionNone,
	}
}
