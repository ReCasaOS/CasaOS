package modulecmd

import (
	"context"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// A panic in a step of an install is a failure of that step: the install is rolled back, the run says its result, and the exit status is 3 (Go's own for a panic is 2, which says that
// nothing was changed).
func TestAPanicInAStepIsAFailureThatIsRolledBack(t *testing.T) {
	for name, at := range map[string]int{"on the first file": 1, "right after the first rename": 2} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			n := 0
			f.host.wrapDest = func(d osmodule.Destination) osmodule.Destination {
				return &hookedDest{Destination: d, onFile: func() {
					if n++; n == at {
						panic("a panic of the test")
					}
				}}
			}
			before := f.treeWithoutBookkeeping()
			if code := f.install("--yes"); code != ExitFailed {
				t.Fatalf("exit %d:\n%s", code, f.out)
			}
			out := f.out.String()
			if !strings.Contains(out, "failed at step unpack: "+CodeInternal+"\n") || !strings.Contains(out, resultMarker+" - virt failed unpack "+CodeInternal+" ") {
				t.Errorf("not said:\n%s", out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			if f.runner.isEnabled(testUnit) || f.exists("/etc/casaos/modules/virt.json") {
				t.Error("the state is not clean")
			}
			if strings.Contains(out, "a panic of the test") && !strings.Contains(f.read("/var/log/casaos/module-virt.log"), "a panic of the test") {
				t.Error("the panic is said on the screen and not in the log")
			}
		})
	}
}

// A panic outside the steps, and in the rollback, is a failure too: exit status 3 and a result line
func TestAPanicOutsideTheStepsIsAFailureToo(t *testing.T) {
	// in the plan, before the steps
	f := newFixture(t)
	f.runner.onRun = func(_ context.Context, argv []string) {
		if argv[0] == "systemctl" && argv[1] == "show" {
			panic("a panic of the test")
		}
	}
	if code := f.install("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), resultMarker+" - virt failed - "+CodeInternal+" ") {
		t.Errorf("a panic in the plan: exit %d\n%s", code, f.out)
	}
	// in the rollback: the install fails at the start, and the rollback panics at its first command
	f = newFixture(t)
	f.runner.fail["systemctl enable --now"] = 1
	f.runner.onRun = func(_ context.Context, argv []string) {
		if argv[0] == "systemctl" && argv[1] == "disable" {
			panic("a panic of the test")
		}
	}
	if code := f.install("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), resultMarker+" - virt failed start start_failed rollback_incomplete ") {
		t.Errorf("a panic in the rollback: exit %d\n%s", code, f.out)
	}
	// in a remove
	f = installed(t)
	f.runner.onRun = func(_ context.Context, argv []string) {
		if argv[0] == "systemctl" && argv[1] == "disable" {
			panic("a panic of the test")
		}
	}
	if code := f.remove("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), resultMarker+" - virt failed stop "+CodeInternal+" - ") {
		t.Errorf("a panic in a remove: exit %d\n%s", code, f.out)
	}
	// in a command that only reads
	f = newFixture(t)
	f.runner.onRun = func(context.Context, []string) { panic("a panic of the test") }
	if code := f.main(context.Background(), "plan", testName); code != ExitFailed {
		t.Errorf("a panic in plan: exit %d", code)
	}
}
