package modulecmd

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
)

// answering is a terminal whose user thinks for a while: onAsk is called when the run reads the answer, with the run still waiting for it, and the answer is then given.
type answering struct {
	onAsk  func()
	answer string
	asked  bool
}

func (a *answering) Read(p []byte) (int, error) {
	if !a.asked {
		a.asked = true
		a.onAsk()
	}
	if a.answer == "" {
		return 0, io.EOF
	}
	n := copy(p, a.answer)
	a.answer = a.answer[n:]
	return n, nil
}

// run runs a command with the terminal being answering, the way the fixture does for a string.
func (f *fixture) runAsking(a *answering, args ...string) int {
	f.t.Helper()
	f.out.Reset()
	f.errOut.Reset()
	f.isTTY = true
	f.env.Stdin = a
	return Main(context.Background(), args, f.env)
}

// The question "Go on?" is asked without the lock: the lock is for the work, and held while somebody thinks it would keep every other install and remove, and the dashboard's, from running
func TestThePromptDoesNotHoldTheLock(t *testing.T) {
	for _, verb := range []string{"install", "remove"} {
		t.Run(verb, func(t *testing.T) {
			var f *fixture
			if verb == "install" {
				f = newFixture(t)
			} else {
				f = installed(t)
			}
			var lockErr error
			a := &answering{answer: "y\n", onAsk: func() {
				release, err := f.host.Host.Lock(f.env.Roots.lock())
				if err == nil {
					release()
				}
				lockErr = err
			}}
			if code := f.runAsking(a, verb, testName); code != ExitOK {
				t.Fatalf("exit %d\n%s", code, f.out)
			}
			if !a.asked {
				t.Fatal("it did not ask")
			}
			if lockErr != nil {
				t.Errorf("the lock was held while the question was asked: %v", lockErr)
			}
		})
	}
}

// ...and the lock is taken after the answer: a run that has it by then is a run that is busy, and the answer does not go over it
func TestAnAnswerDoesNotGoOverARunThatTookTheLockMeanwhile(t *testing.T) {
	for _, verb := range []string{"install", "remove"} {
		t.Run(verb, func(t *testing.T) {
			var f *fixture
			if verb == "install" {
				f = newFixture(t)
			} else {
				f = installed(t)
			}
			a := &answering{answer: "y\n", onAsk: func() {
				release, err := f.host.Host.Lock(f.env.Roots.lock())
				if err != nil {
					t.Error(err)
					return
				}
				t.Cleanup(release)
			}}
			before := f.treeWithoutBookkeeping()
			if code := f.runAsking(a, verb, testName); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+CodeBusy+"\n") {
				t.Fatalf("exit %d\n%s", code, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
		})
	}
}

// ...and the plan is made again after the answer: what the user was shown is what is done, or nothing is
func TestThePlanIsMadeAgainAfterTheAnswer(t *testing.T) {
	t.Run("the system is another one now", func(t *testing.T) {
		f := newFixture(t)
		a := &answering{answer: "y\n", onAsk: func() { f.writeFile("/etc/os-release", "ID=debian\nVERSION_ID=\"11\"\n") }}
		if code := f.runAsking(a, "install", testName); code != ExitRefused || !strings.Contains(f.out.String(), "refused: os\n") {
			t.Fatalf("exit %d\n%s", code, f.out)
		}
		if f.runner.called("apt-get", "install") != 0 || f.exists("/usr/bin/casaos-virt-management") {
			t.Error("something was done")
		}
	})
	t.Run("the packages are other ones now", func(t *testing.T) {
		f := newFixture(t)
		a := &answering{answer: "y\n", onAsk: func() {
			f.sandbox.sim = osplan.Output{Text: strings.ReplaceAll(aptSim, "libfdt1", "libfdt2")}
			f.sandbox.est = osplan.Output{Text: strings.ReplaceAll(aptEst, "libfdt1", "libfdt2")}
		}}
		before := f.treeWithoutBookkeeping()
		if code := f.runAsking(a, "install", testName); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+CodePlanChanged+"\n") {
			t.Fatalf("exit %d\n%s", code, f.out)
		}
		sameTree(t, before, f.treeWithoutBookkeeping())
	})
	t.Run("the module is gone now", func(t *testing.T) {
		f := installed(t)
		a := &answering{answer: "y\n", onAsk: func() {
			for _, p := range []string{"/etc/casaos/modules/virt.json", filesList, dirsList, "/var/lib/casaos/modules/virt.version"} {
				if err := os.Remove(f.path(p)); err != nil {
					t.Error(err)
				}
			}
		}}
		if code := f.runAsking(a, "remove", testName); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+CodeNotInstalled+"\n") {
			t.Fatalf("exit %d\n%s", code, f.out)
		}
	})
	t.Run("a package manager is at work now", func(t *testing.T) {
		f := installed(t)
		a := &answering{answer: "y\n", onAsk: func() { f.runner.busyUnit = "casaos-update.service" }}
		if code := f.runAsking(a, "remove", testName); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+CodeBusy+"\n") || !f.runner.isEnabled(testUnit) {
			t.Fatalf("exit %d\n%s", code, f.out)
		}
	})
}

// A question that is not answered yes is declined with no lock taken and no log written
func TestADeclinedQuestionTouchesNeitherTheLockNorTheLog(t *testing.T) {
	f := newFixture(t)
	a := &answering{answer: "n\n", onAsk: func() {}}
	if code := f.runAsking(a, "install", testName); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+CodeDeclined+"\n") {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	if f.exists("/var/log/casaos/module-virt.log") || f.host.lockCalls != 0 {
		t.Errorf("the log was written, or the lock taken (%d)", f.host.lockCalls)
	}
}
