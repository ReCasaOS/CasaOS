//go:build linux

package boot

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The assistant ends the process before the core initialises anything of its own: the test builds the core and reads the trace of the initialisation that the runtime prints with
// GODEBUG=inittrace=1 (one line to the error output for each package that has something to initialise, when it is done).

// denied are the packages that the core starts itself with and that the assistant must not have run: the route package of the files starts a goroutine and a timer in its init, the route
// package reads the description of its API, and the services, the alerts and the telemetry are the core's own state. The web framework stands for the rest of its server.
var denied = []string{
	"github.com/ReCasaOS/CasaOS/route/v1",
	"github.com/ReCasaOS/CasaOS/route",
	"github.com/ReCasaOS/CasaOS/service",
	"github.com/ReCasaOS/CasaOS/pkg/alerts",
	"github.com/ReCasaOS/CasaOS/pkg/telemetry",
	"github.com/ReCasaOS/CasaOS/codegen",
	"github.com/ReCasaOS/CasaOS/drivers/base",
	"github.com/labstack/echo/v4",
}

// initialised are the packages whose initialisation the trace of a run says was done.
func initialised(trace string) map[string]bool {
	done := map[string]bool{}
	for _, line := range strings.Split(trace, "\n") {
		if rest, ok := strings.CutPrefix(line, "init "); ok {
			name, _, _ := strings.Cut(rest, " ")
			done[name] = true
		}
	}
	return done
}

func buildCore(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("CASAOS_CORE_BINARY"); bin != "" { // (a core that was built already, for a machine that has no go command)
		return bin
	}
	if testing.Short() {
		t.Skip("builds the core")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command to build the core with")
	}
	bin := filepath.Join(t.TempDir(), "casaos")
	cmd := exec.Command(goBin, "build", "-o", bin, "github.com/ReCasaOS/CasaOS")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the core: %v\n%s", err, out)
	}
	return bin
}

// run the binary with the trace on; the exit code and the error output
func traced(t *testing.T, bin string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "GODEBUG=inittrace=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, &exit):
		return exit.ExitCode(), stderr.String()
	}
	t.Fatal(err)
	return 0, ""
}

func TestTheAssistantRunsBeforeTheCoreIsInitialised(t *testing.T) {
	bin := buildCore(t)

	// the control: when the core starts as itself the trace has them all, or it would not be a trace of what is denied
	code, trace := traced(t, bin, "-v")
	if code != 0 {
		t.Fatalf("-v: exit %d\n%s", code, trace)
	}
	started := initialised(trace)
	for _, pkg := range denied {
		if !started[pkg] {
			t.Errorf("the core does not initialise %s when it starts: the list of what is denied is out of date", pkg)
		}
	}

	// the assistant, with a verb it refuses: it prints its usage and ends with the code of a refusal, having touched nothing
	code, trace = traced(t, bin, "module", "no-such-verb")
	if code != 2 || !strings.Contains(trace, "usage: casaos module list") {
		t.Fatalf("exit %d\n%s", code, trace)
	}
	ran := initialised(trace)
	if !ran["github.com/ReCasaOS/CasaOS/pkg/osplan"] {
		t.Error("the trace says nothing of the packages the assistant needs: it is not a trace of this run")
	}
	for _, pkg := range denied {
		if ran[pkg] {
			t.Errorf("%s was initialised before the assistant ran", pkg)
		}
	}
	// ...and of the packages of the core itself, only what the assistant imports and what the core's own utilities need
	for pkg := range ran {
		if !strings.HasPrefix(pkg, "github.com/ReCasaOS/") {
			continue
		}
		switch pkg {
		case "github.com/ReCasaOS/CasaOS-Common/utils/common_err", "github.com/ReCasaOS/CasaOS/pkg/utils/common_err", "github.com/ReCasaOS/CasaOS-Common/utils/systemctl",
			"github.com/ReCasaOS/CasaOS-Common/external", "github.com/ReCasaOS/CasaOS/pkg/config", "github.com/ReCasaOS/CasaOS/pkg/osverify":
		case "github.com/ReCasaOS/CasaOS/pkg/dockerpkg", "github.com/ReCasaOS/CasaOS/pkg/osmodule", "github.com/ReCasaOS/CasaOS/pkg/ossupport", "github.com/ReCasaOS/CasaOS/pkg/osreadiness",
			"github.com/ReCasaOS/CasaOS/pkg/osrun", "github.com/ReCasaOS/CasaOS/pkg/osplan", "github.com/ReCasaOS/CasaOS/pkg/modulecmd":
		default:
			t.Errorf("%s was initialised before the assistant ran", pkg)
		}
	}
}
