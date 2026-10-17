package modulecmd

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
)

// recordingSandbox keeps what the commands of the sandbox printed.
type recordingSandbox struct {
	osplan.Sandbox
	outs []osplan.Output
}

func (r *recordingSandbox) Run(ctx context.Context, argv []string) (osplan.Output, error) {
	out, err := r.Sandbox.Run(ctx, argv)
	r.outs = append(r.outs, out)
	return out, err
}

// What the real apt says, read by the parser of osplan: this runs only inside a Debian chroot that has a small repository of packages (casaos-mod-a depends on casaos-mod-b (>= 1.0); d
// conflicts with e) and the state of the scenario, which the script that runs it sets up (CASAOS_MODULECMD_REAL_APT=<scenario>). Nothing is installed by the test: apt is asked with -s.
func TestTheRealAptOfAChrootIsUnderstood(t *testing.T) {
	scenario := os.Getenv("CASAOS_MODULECMD_REAL_APT")
	if scenario == "" {
		t.Skip("needs a Debian chroot with the repository of the tests (CASAOS_MODULECMD_REAL_APT=<scenario>)")
	}
	rec := &recordingSandbox{}
	real := newSandbox(Roots{})
	env := Env{Roots: Roots{}, Runner: newRunner(Roots{}), Sandbox: func() osplan.Sandbox { rec.Sandbox = real(); return rec }, Host: newHost(), EUID: os.Geteuid, Arch: "amd64"}.withDefaults()
	bx := &box{ctx: context.Background(), env: &env}
	pkgs := map[string][]string{
		"fresh": {"casaos-mod-a"}, "upgrade": {"casaos-mod-a"}, "held": {"casaos-mod-a"}, "held-named": {"casaos-mod-b"}, "removal": {"casaos-mod-d"}, "missing": {"casaos-mod-zzz"}, "nothing": {"casaos-mod-a"},
	}[scenario]
	if pkgs == nil {
		t.Fatalf("no such scenario: %s", scenario)
	}
	res := bx.SimulateApt(pkgs)
	t.Logf("%s: %+v", scenario, res)
	if dir := os.Getenv("CASAOS_MODULECMD_CAPTURE"); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		for i, name := range []string{"sim", "est"} {
			if i < len(rec.outs) {
				_ = os.WriteFile(filepath.Join(dir, scenario+"."+name+".txt"), []byte(rec.outs[i].Text), 0o644)
				_ = os.WriteFile(filepath.Join(dir, scenario+"."+name+".rc"), []byte(strconv.Itoa(rec.outs[i].ExitCode)+"\n"), 0o644)
			}
		}
	}
	checkScenario(t, scenario, res)
}
