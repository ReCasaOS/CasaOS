package osmodule

import (
	"errors"
	"strings"
	"testing"
)

// The state of dpkg is a check of the plan, after busy: a dpkg that lists packages it has not finished says so and names them, and is not the reason of a refusal (the install finishes
// them; a refusal would keep the dashboard from starting it); one that cannot be asked refuses the plan with dpkg_state; a box without dpkg makes the check not made.
func TestAnInterruptedDpkgIsSaidByThePlanAndDoesNotRefuseIt(t *testing.T) {
	box := newBox()
	box.dpkgInterrupted = []string{"libvirt-clients", "qemu-utils"}
	r := Plan(virtCatalogue(t), "virt", box)
	c := checkOf(r, "dpkg_state")
	if c.OK || r.Refused != "" || r.PlanID == "" || c.Detail != "interrupted: libvirt-clients qemu-utils" {
		t.Fatalf("refused %q, check %+v, id %q", r.Refused, c, r.PlanID)
	}
	if got := checkCodes(r); got[6] != "busy" || got[7] != "dpkg_state" || got[8] != "disk" {
		t.Errorf("the order: %v", got)
	}
	if !box.called("SimulateApt") {
		t.Error("apt was not asked to simulate (a simulation takes no lock and does not mind)")
	}
}

func TestADpkgThatCannotBeAskedRefusesThePlanAndOneThatIsNotThereDoesNot(t *testing.T) {
	box := newBox()
	box.dpkgErr = errors.New("boom")
	r := Plan(virtCatalogue(t), "virt", box)
	if c := checkOf(r, "dpkg_state"); c.OK || c.Detail != "unreadable" || r.Refused != "dpkg_state" {
		t.Errorf("%+v refused %q", c, r.Refused)
	}
	box = newBox()
	box.dpkgErr = ErrDpkgMissing
	r = Plan(virtCatalogue(t), "virt", box)
	if c := checkOf(r, "dpkg_state"); c.OK || !strings.HasPrefix(c.Detail, DetailNotChecked) || r.Refused != "" {
		t.Errorf("%+v refused %q", c, r.Refused)
	}
}

// what a hostile status file puts in a package name never reaches the plan as it is: printable ASCII, cut
func TestTheAuditInThePlanIsClippedAndMadePrintable(t *testing.T) {
	box := newBox()
	box.dpkgInterrupted = []string{"evil\x1b[31m\n\u00e9" + strings.Repeat("a", 5000)}
	c := checkOf(Plan(virtCatalogue(t), "virt", box), "dpkg_state")
	if len(c.Detail) > len("interrupted: ")+maxAptDetail {
		t.Errorf("%d characters", len(c.Detail))
	}
	for _, r := range c.Detail {
		if r < 0x20 || r > 0x7e {
			t.Fatalf("%q in the detail", r)
		}
	}
}

func TestADpkgThatIsCleanIsOkAndAPlanForAnUnknownModuleDoesNotAskIt(t *testing.T) {
	if c := checkOf(Plan(virtCatalogue(t), "virt", newBox()), "dpkg_state"); !c.OK {
		t.Errorf("%+v", c)
	}
	r := Plan(virtCatalogue(t), "nothing", newBox())
	if c := checkOf(r, "dpkg_state"); c.OK || c.Detail != DetailNotChecked || len(r.Checks) != 10 {
		t.Errorf("%+v, %d checks", c, len(r.Checks))
	}
}
