package modulecmd

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The sandbox is a directory in /var/tmp, whatever the Roots say (the allow-list of osplan has no other): its tests run only where that is a throw-away system, which the run says with
// CASAOS_MODULECMD_DISPOSABLE=1.
func disposable(t *testing.T) {
	t.Helper()
	if os.Getenv("CASAOS_MODULECMD_DISPOSABLE") != "1" {
		t.Skip("writes in /var/tmp: only on a throw-away system (CASAOS_MODULECMD_DISPOSABLE=1)")
	}
}

func newTestSandbox(t *testing.T, root string) (osplan.Sandbox, string, func()) {
	t.Helper()
	disposable(t)
	sb := newSandbox(Roots{Prefix: root})()
	dir, remove, err := sb.MakeDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	return sb, dir, remove
}

func TestTheSandboxIsADirectoryOfItsOwnThatIsRemovedOnEveryExit(t *testing.T) {
	disposable(t)
	sb := newSandbox(Roots{})()
	dir, remove, err := sb.MakeDir()
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	if !osplan.ValidSandboxDir(dir) || !strings.HasPrefix(dir, "/var/tmp/casaos-os-plan.") {
		t.Errorf("the directory: %s", dir)
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode().Perm() != 0o711 {
		t.Errorf("%v %v", info, err)
	}
	if _, _, err := sb.MakeDir(); err == nil {
		t.Error("a second directory")
	}
	remove()
	remove() // (twice is the same as once)
	if exists(dir) {
		t.Error("not removed")
	}
	// a link where the directory was is not followed
	sb = newSandbox(Roots{})()
	dir, remove, err = sb.MakeDir()
	if err != nil {
		t.Fatal(err)
	}
	victim := t.TempDir()
	touch(t, filepath.Join(victim, "precious"))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, dir); err != nil {
		t.Fatal(err)
	}
	remove()
	if !exists(filepath.Join(victim, "precious")) {
		t.Error("the target of a link was removed")
	}
	_ = os.Remove(dir)
}

func TestTheSandboxWritesInsideItselfAndNowhereElse(t *testing.T) {
	sb, dir, _ := newTestSandbox(t, t.TempDir())
	if err := sb.WriteFile(dir+"/a/b/file", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir + "/a"); err != nil || info.Mode().Perm() != 0o711 {
		t.Errorf("the directory above: %v %v", info, err)
	}
	if err := sb.WriteFile(dir+"/d", nil, fs.ModeDir|fs.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir + "/d"); info.Mode()&fs.ModeSticky == 0 || info.Mode().Perm() != 0o777 {
		t.Errorf("the sticky directory: %v", info.Mode())
	}
	for _, bad := range []string{dir, dir + "/../x", "/var/tmp/x", dir + "x/y", "/etc/passwd", "a/b", dir + "/a/../../x", ""} {
		if err := sb.WriteFile(bad, []byte("x"), 0o600); err == nil {
			t.Errorf("%q is written", bad)
		}
	}
	// not over a file, not through a link
	if err := sb.WriteFile(dir+"/a/b/file", []byte("y"), 0o600); err == nil {
		t.Error("over a file")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, dir+"/link"); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(dir+"/link/file", []byte("y"), 0o600); err == nil || exists(outside+"/file") {
		t.Errorf("through a link: %v", err)
	}
}

func TestTheSandboxRunsTheTwoCommandsOfAModuleAndNothingNear(t *testing.T) {
	root := t.TempDir()
	writeStubs(t, root)
	sb, dir, _ := newTestSandbox(t, root)
	t.Setenv("SECRET_OF_THE_TEST", "hunter2")
	t.Setenv("CASAOS_MODULE_NONCE", strings.Repeat("ab", 16))
	for _, bad := range [][]string{
		nil, {"apt-get"}, {"sh", "-c", "id"}, {"apt-get", "install", "libvirt-clients"}, {"apt-get", "-s", "install", "libvirt-clients"},
		append([]string{"apt-get", "-o", "Dir::Cache=" + dir + "/cache", "-o", "Debug::NoLocking=true", "-s", "install", "--no-install-recommends"}, "-y"),
		append([]string{"apt-get", "-o", "Dir::Cache=/etc", "-o", "Debug::NoLocking=true", "-s", "install", "--no-install-recommends"}, "libvirt-clients"),
		append(simArgv(t, dir, []string{"libvirt-clients"}), "pkg;reboot"),
		simArgv(t, "/var/tmp/casaos-os-plan.other", []string{"libvirt-clients"}),
		osplan.UpdateArgv(dir), osplan.DistUpgradeArgv(dir),
	} {
		if _, err := sb.Run(context.Background(), bad); !errors.Is(err, osreadiness.ErrNotAllowed) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if exists(stubFile(root, "apt-get.log")) {
		t.Error("a command that is not allowed was run")
	}
	sim, err := sb.Run(context.Background(), simArgv(t, dir, testPackages))
	if err != nil || sim.ExitCode != 0 || sim.Text != aptSim {
		t.Fatalf("the simulation: %v %d %q", err, sim.ExitCode, sim.Text)
	}
	est, err := sb.Run(context.Background(), estArgv(t, dir, testPackages))
	if err != nil || est.ExitCode != 100 || est.Text != aptEst {
		t.Fatalf("the estimate: %v %d %q", err, est.ExitCode, est.Text)
	}
	// run in English, in the directory, with the environment of the sandbox and nothing of the process
	raw, _ := os.ReadFile(stubFile(root, "apt-get.env"))
	line := strings.Split(strings.TrimSpace(string(raw)), "\n")[0]
	for _, want := range []string{"pwd=" + dir + " ", "LC_ALL=C", "LANG=C", "FRONTEND=noninteractive", "SECRET= ", "NONCE= ", "APT_CONFIG=" + dir + "/apt-config", "TMPDIR=" + dir + "/tmp"} {
		if !strings.Contains(line, want) {
			t.Errorf("the environment lacks %q: %s", want, line)
		}
	}
}

func simArgv(t *testing.T, dir string, pkgs []string) []string {
	t.Helper()
	argv, err := osplan.ModuleSimArgv(dir, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	return argv
}

func estArgv(t *testing.T, dir string, pkgs []string) []string {
	t.Helper()
	argv, err := osplan.ModuleEstimateArgv(dir, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	return argv
}

func TestTheSandboxEndsACommandWhenTheContextEnds(t *testing.T) {
	root := t.TempDir()
	writeStubs(t, root)
	touch(t, stubFile(root, "block-sim"))
	sb, dir, _ := newTestSandbox(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := sb.Run(ctx, simArgv(t, dir, testPackages)); done <- err }()
	waitFor(t, stubFile(root, "reached-sim"))
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || time.Since(started) > 5*time.Second {
			t.Errorf("%v after %v", err, time.Since(started))
		}
	case <-time.After(15 * time.Second):
		t.Fatal("it did not end")
	}
}

// the plan, with the sandbox for real: apt is asked in a sandbox that is gone afterwards, with the configuration of the sandbox, and the answer is parsed
func TestAPlanAsksAptInASandboxAndLeavesNothingOfIt(t *testing.T) {
	disposable(t)
	f := newFixture(t)
	writeStubs(t, f.root)
	f.env.Runner = newRunner(f.env.Roots)
	f.env.Sandbox = newSandbox(f.env.Roots)
	before, _ := filepath.Glob("/var/tmp/casaos-os-plan.*")
	f.writeFile("/etc/apt/apt.conf.d/20proxy", "Acquire::http::Proxy \"http://proxy.example:3142\";\n")
	f.writeFile("/etc/apt/apt.conf.d/99hook", "APT::Update::Post-Invoke { \"touch /tmp/x\"; };\n")
	v := f.json("plan", "virt")
	if v["refused"] != nil {
		t.Fatalf("refused: %v", v["refused"])
	}
	pk := v["packages"].(map[string]any)
	if !slices.Equal(anyStrings(pk["to_install"]), []string{"libfdt1", "libvirt-clients", "libvirt0", "qemu-utils"}) || pk["known"] != true {
		t.Errorf("packages: %v", pk)
	}
	after, _ := filepath.Glob("/var/tmp/casaos-os-plan.*")
	if len(after) != len(before) {
		t.Errorf("sandboxes left: %v", after)
	}
	cfg, _ := os.ReadFile(stubFile(f.root, "apt-config.seen"))
	if !strings.Contains(string(cfg), "Dir::Etc::parts") || !strings.Contains(string(cfg), "Dir::Log::Planner") {
		t.Errorf("the configuration of the sandbox: %q", cfg)
	}
	// a plan that cannot ask apt is a refusal and the sandbox is gone all the same
	touch(t, stubFile(f.root, "block-sim"))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	res := PlanOf(ctx, f.env, "virt")
	if res.Refused != "apt_failed" {
		t.Errorf("refused: %q", res.Refused)
	}
	after, _ = filepath.Glob("/var/tmp/casaos-os-plan.*")
	if len(after) != len(before) {
		t.Errorf("sandboxes left: %v", after)
	}
}
