package osmodule

import (
	"slices"
	"testing"
)

const withdrawnReason = "It loses the data of its disks; é is shown as it is."

// withdrawing is the catalogue with its list of the versions withdrawn.
func withdrawing(c Catalogue, list ...Withdrawn) Catalogue {
	c.Withdrawn = append([]Withdrawn{}, list...)
	return c
}

func withChecks(codes []string) []string {
	return slices.Insert(slices.Clone(codes), 1, CheckWithdrawn)
}

func TestAPlanOfAVersionTheCatalogueWithdrewIsRefused(t *testing.T) {
	box := newBox()
	r := Plan(withdrawing(virtCatalogue(t), Withdrawn{Name: "virt", Tag: "v0.1.0", Reason: withdrawnReason}), "virt", box)
	if r.Refused != RefusedWithdrawn || r.PlanID != "" || !slices.Equal(checkCodes(r), withChecks(allChecks)) {
		t.Fatalf("refused %q, plan id %q, checks %v", r.Refused, r.PlanID, checkCodes(r))
	}
	if c := r.Checks[1]; c.OK || c.Detail != withdrawnReason {
		t.Errorf("%+v", c)
	}
	if box.called("SimulateApt") {
		t.Errorf("apt was asked about a version that is withdrawn")
	}
}

func TestAPlanOfAVersionTheCatalogueDidNotWithdrawSaysSo(t *testing.T) {
	for name, list := range map[string][]Withdrawn{
		"an empty list":                  nil,
		"another tag of the module":      {{Name: "virt", Tag: "v0.0.9", Reason: "Broken."}},
		"the same tag of another module": {{Name: "other", Tag: "v0.1.0", Reason: "Broken."}},
	} {
		t.Run(name, func(t *testing.T) {
			r := Plan(withdrawing(virtCatalogue(t), list...), "virt", newBox())
			if r.Refused != "" || r.PlanID == "" || !slices.Equal(checkCodes(r), withChecks(allChecks)) {
				t.Fatalf("refused %q, checks %+v", r.Refused, r.Checks)
			}
			if c := r.Checks[1]; !c.OK || c.Detail != "" {
				t.Errorf("%+v", c)
			}
		})
	}
	// a catalogue with no list (schema 1 and 2): no such check
	if r := Plan(virtCatalogue(t), "virt", newBox()); !slices.Equal(checkCodes(r), allChecks) {
		t.Errorf("checks %v", checkCodes(r))
	}
}

func TestAPlanOfAModuleNotInACatalogueWithAWithdrawnListDoesNotMakeTheCheck(t *testing.T) {
	cat := withdrawing(virtCatalogue(t), Withdrawn{Name: "other", Tag: "v0.1.0", Reason: "Broken."})
	r := Plan(cat, "other", newBox())
	if r.Refused != CheckCatalogue || !slices.Equal(checkCodes(r), withChecks(allChecks)) || r.Checks[1].OK || r.Checks[1].Detail != DetailNotChecked {
		t.Fatalf("%+v", r)
	}
	u := PlanUpdate(cat, "other", newUpdBox(t))
	if u.Refused != CheckCatalogue || !slices.Equal(checkCodes(u), withChecks(updateChecks)) || u.Checks[1].Detail != DetailNotChecked {
		t.Fatalf("%+v", u)
	}
}

func TestAnUpdateToAVersionTheCatalogueWithdrewIsRefused(t *testing.T) {
	box := newUpdBox(t)
	box.sim = AptResult{Known: true}
	r := PlanUpdate(withdrawing(newer(t), Withdrawn{Name: "virt", Tag: "v0.1.1", Reason: withdrawnReason}), "virt", box)
	if r.Refused != RefusedWithdrawn || r.PlanID != "" || !slices.Equal(checkCodes(r), withChecks(updateChecks)) {
		t.Fatalf("refused %q, checks %v", r.Refused, checkCodes(r))
	}
	if c := r.Checks[1]; c.OK || c.Detail != withdrawnReason {
		t.Errorf("%+v", c)
	}
	if len(box.simulated) != 0 {
		t.Errorf("apt was asked about %v", box.simulated)
	}
}

// the way out of a version that is withdrawn is an update: the installed tag being withdrawn refuses nothing
func TestAnUpdateFromAVersionTheCatalogueWithdrewIsPlanned(t *testing.T) {
	box := newUpdBox(t)
	box.sim = AptResult{Known: true}
	r := PlanUpdate(withdrawing(newer(t), Withdrawn{Name: "virt", Tag: "v0.1.0", Reason: withdrawnReason}), "virt", box)
	if r.Refused != "" || r.PlanID == "" || !slices.Equal(checkCodes(r), withChecks(updateChecks)) || !r.Checks[1].OK {
		t.Fatalf("refused %q, checks %+v", r.Refused, r.Checks)
	}
}
