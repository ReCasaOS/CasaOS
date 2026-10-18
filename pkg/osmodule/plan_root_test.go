package osmodule

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// A plan asked for by a user that is not root (the dpkg locks cannot be looked at by anyone but root) makes every check it can and says that the one it could not make was not made. It is
// never the reason of a refusal and never an ok.

func TestAPlanOfAUserThatIsNotRootDoesNotStopAtTheLocksItCannotLookAt(t *testing.T) {
	for name, err := range map[string]error{"bare": ErrNeedsRoot, "wrapped": fmt.Errorf("the locks: %w", ErrNeedsRoot)} {
		t.Run(name, func(t *testing.T) {
			box := newBox()
			box.packageManagerErr = err
			r := Plan(virtCatalogue(t), "virt", box)
			if r.Refused != "" {
				t.Fatalf("refused: %s", r.Refused)
			}
			busy := checkOf(r, CheckBusy)
			if busy.OK || busy.Detail != DetailNeedsRoot {
				t.Errorf("the check: %+v", busy)
			}
			for _, c := range r.Checks {
				if c.Code != CheckBusy && (!c.OK || c.Detail != "") {
					t.Errorf("the check %s: %+v", c.Code, c)
				}
			}
			if !box.called("SimulateApt") || !r.Packages.Known || len(r.Packages.ToInstall) != 3 || r.Packages.DownloadBytes != 20*mb {
				t.Errorf("the numbers: %+v (simulated: %v)", r.Packages, box.called("SimulateApt"))
			}
			if r.PlanID == "" {
				t.Error("no plan id")
			}
			// the same words as JSON
			raw, _ := json.Marshal(busy)
			if want := `{"code":"busy","ok":false,"detail":"` + DetailNeedsRoot + `"}`; string(raw) != want {
				t.Errorf("%s, want %s", raw, want)
			}
		})
	}
	if DetailNeedsRoot == DetailNotChecked || len(DetailNeedsRoot) > 100 {
		t.Errorf("the words: %q", DetailNeedsRoot)
	}
}

// what can be seen without root still refuses
func TestAPlanOfAUserThatIsNotRootStillRefusesForWhatItCanSee(t *testing.T) {
	for name, c := range map[string]struct {
		set    func(b *fakeBox)
		refuse string
		detail string
	}{
		"the upgrade of the system is open":                             {func(b *fakeBox) { b.osUpgrade = true }, "busy", "os_upgrade"},
		"its flag cannot be read":                                       {func(b *fakeBox) { b.osUpgradeErr = errors.New("io") }, "busy", "unreadable"},
		"another system":                                                {func(b *fakeBox) { b.osVersion = "11" }, "os", DetailNeedsRoot},
		"a release that is too old":                                     {func(b *fakeBox) { b.version = "0.5.22" }, "recasaos", DetailNeedsRoot},
		"the module is installed":                                       {func(b *fakeBox) { b.installed = map[string]Manifest{"virt": {}} }, "already_installed", DetailNeedsRoot},
		"apt would remove a package":                                    {func(b *fakeBox) { b.sim.Removes = []string{"x"} }, "apt_removes", DetailNeedsRoot},
		"the locks cannot be read, for a reason that is not the user's": {func(b *fakeBox) { b.packageManagerErr = errors.New("io") }, "busy", "unreadable"},
	} {
		t.Run(name, func(t *testing.T) {
			box := newBox()
			box.packageManagerErr = ErrNeedsRoot
			c.set(box)
			r := Plan(virtCatalogue(t), "virt", box)
			if string(r.Refused) != c.refuse {
				t.Fatalf("refused %q, want %q", r.Refused, c.refuse)
			}
			if got := checkOf(r, CheckBusy).Detail; c.refuse == "busy" && got != c.detail {
				t.Errorf("the busy check says %q, want %q", got, c.detail)
			}
		})
	}
}

// a check that the plan did not make because the user is not root does not make the plan stand where a check that failed would not: the room and apt are asked
func TestTheChecksAfterTheOneThatNeedsRootAreMade(t *testing.T) {
	box := newBox()
	box.packageManagerErr = ErrNeedsRoot
	box.free[UsrDir] = 1
	r := Plan(virtCatalogue(t), "virt", box)
	if r.Refused != "disk_short" {
		t.Errorf("refused %q", r.Refused)
	}
}
