package modulecmd

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// A user that is not root cannot open the locks of dpkg and apt (root's, 0640): that is not a busy box and not an unreadable one, it is a check that root makes.

func denied() error {
	return &fs.PathError{Op: "open", Path: "/var/lib/dpkg/lock", Err: fs.ErrPermission}
}

func TestTheLocksCannotBeLookedAtByAUserThatIsNotRoot(t *testing.T) {
	f := newFixture(t)
	f.host.dpkg = func([]string) (bool, error) { return false, denied() }
	bx := &box{ctx: context.Background(), env: &f.env}

	f.env.EUID = func() int { return 1000 }
	if busy, err := bx.PackageManagerBusy(); busy || !errors.Is(err, osmodule.ErrNeedsRoot) {
		t.Errorf("not root: %v, %v", busy, err)
	}
	// a unit that is at work is seen by anyone
	f.runner.busyUnit = "apt-daily.service"
	if busy, err := bx.PackageManagerBusy(); !busy || err != nil {
		t.Errorf("a unit at work: %v, %v", busy, err)
	}
	f.runner.busyUnit = ""
	// a lock that is held and seen is busy, whatever the other locks said
	f.host.dpkg = func([]string) (bool, error) { return true, nil }
	if busy, err := bx.PackageManagerBusy(); !busy || err != nil {
		t.Errorf("a lock held: %v, %v", busy, err)
	}
	// a lock that is held and another that could not be looked at: the machine is busy, which a user who is not root is told as well
	f.host.dpkg = func([]string) (bool, error) { return true, denied() }
	if plan := f.json("plan", "virt"); plan["refused"] != osmodule.CheckBusy {
		t.Errorf("a lock held and one that is not seen: %v", plan["refused"])
	}
	// an error that is not that of a user who may not look is not "needs root"
	f.host.dpkg = func([]string) (bool, error) { return false, errors.New("io") }
	if _, err := bx.PackageManagerBusy(); err == nil || errors.Is(err, osmodule.ErrNeedsRoot) {
		t.Errorf("an error of the disk: %v", err)
	}
	// root who is refused is a box that cannot be told, and is busy
	f.env.EUID = func() int { return 0 }
	f.host.dpkg = func([]string) (bool, error) { return false, denied() }
	if _, err := bx.PackageManagerBusy(); err == nil || errors.Is(err, osmodule.ErrNeedsRoot) {
		t.Errorf("root, refused: %v", err)
	}
	if plan := f.json("plan", "virt"); plan["refused"] != osmodule.CheckBusy {
		t.Errorf("root, refused: the plan is refused with %v", plan["refused"])
	}
}

func TestAPlanOfAUserThatIsNotRootIsMadeAndInstallStillNeedsRoot(t *testing.T) {
	f := newFixture(t)
	f.env.EUID = func() int { return 1000 }
	f.host.dpkg = func([]string) (bool, error) { return false, denied() }
	before := f.treeWithoutBookkeeping()
	plan := f.json("plan", "virt")
	if plan["refused"] != nil || plan["plan_id"] == "" {
		t.Fatalf("refused %v, id %v", plan["refused"], plan["plan_id"])
	}
	busy := checkOf(plan, osmodule.CheckBusy)
	if busy["ok"] != false || busy["detail"] != osmodule.DetailNeedsRoot {
		t.Errorf("busy: %v", busy)
	}
	pk := plan["packages"].(map[string]any)
	if pk["known"] != true || len(pk["to_install"].([]any)) != 4 {
		t.Errorf("the numbers: %v", pk)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	// the same user is turned away from the install, and from the remove
	if code := f.install("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+CodeNotRoot+"\n") {
		t.Errorf("install: exit %d\n%s", code, f.out)
	}
	// a unit at work is busy for anyone
	f.runner.busyUnit = "casaos-update.service"
	if plan := f.json("plan", "virt"); plan["refused"] != osmodule.CheckBusy {
		t.Errorf("a unit at work: %v", plan["refused"])
	}
}
