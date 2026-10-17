//go:build linux

package osplan

import (
	"bytes"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// The proof against the real apt that the estimate of a module is a command that cannot install. It needs a Debian chroot to spare, root, and a package that installs by itself and is
// in the lists of the chroot; testdata/realmodule/capture.sh makes both ("prepare" builds the packages, "run" points apt at them):
//
//	CASAOS_MODULE_CHROOT=/path/to/the/chroot go test ./pkg/osplan -run TestTheEstimateOfAModuleChangesNothingOnTheBox
//
// The chroot is changed by the last step, which is the control: the command of an earlier version (the trivial-only option) is run too, and must be seen to install the package, or
// the checks before it proved nothing.
func TestTheEstimateOfAModuleChangesNothingOnTheBox(t *testing.T) {
	root := os.Getenv("CASAOS_MODULE_CHROOT")
	if root == "" || os.Geteuid() != 0 {
		t.Skip("no chroot to spare (CASAOS_MODULE_CHROOT), or not root")
	}
	const pkg = "rvm-solo"
	dir := SandboxParent + "/" + SandboxPrefix + "5555"
	for _, f := range SandboxFiles(dir, nil, AptConfig{}) {
		path := filepath.Join(root, f.Path)
		if f.Mode.IsDir() {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, f.Mode.Perm()|(f.Mode&os.ModeSticky)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(path, f.Content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	defer os.RemoveAll(filepath.Join(root, dir))

	run := func(argv []string) Output {
		t.Helper()
		cmd := exec.Command("/usr/sbin/chroot", slices.Concat([]string{root, "/usr/bin/env", "-i"}, SandboxEnv(dir), argv)...)
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		code := 0
		if err := cmd.Run(); err != nil {
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatal(err)
			}
			code = ee.ExitCode()
		}
		return Output{Text: buf.String(), ExitCode: code}
	}
	digest := func(rel string) [32]byte {
		b, _ := os.ReadFile(filepath.Join(root, rel))
		return sha256.Sum256(b)
	}
	installed := func() bool {
		b, _ := os.ReadFile(filepath.Join(root, "/var/lib/dpkg/status"))
		return bytes.Contains(b, []byte("Package: "+pkg+"\n")) && bytes.Contains(b, []byte("Status: install ok installed"))
	}
	if installed() {
		t.Fatalf("%s is installed in %s already: the proof needs a chroot where it is not", pkg, root)
	}
	_ = os.Remove(filepath.Join(root, "/var/log/apt/eipp.log.xz"))
	files := []string{"/var/lib/dpkg/status", "/var/log/apt/history.log", "/var/log/dpkg.log", "/var/lib/dpkg/lock"}
	before := map[string][32]byte{}
	for _, f := range files {
		before[f] = digest(f)
	}

	simArgv, err := ModuleSimArgv(dir, []string{pkg})
	if err != nil {
		t.Fatal(err)
	}
	estArgv, err := ModuleEstimateArgv(dir, []string{pkg})
	if err != nil {
		t.Fatal(err)
	}
	sim, est := run(simArgv), run(estArgv)
	if sim.ExitCode != 0 || est.ExitCode != 0 {
		t.Fatalf("status %d and %d\n%s\n%s", sim.ExitCode, est.ExitCode, sim.Text, est.Text)
	}
	res := ParseModuleSimulation(sim, est, []string{pkg})
	if res.Failure != "" || !slices.Equal(res.ToInstall, []string{pkg}) || !res.Known || res.DownloadBytes <= 0 {
		t.Errorf("%+v\n%s\n%s", res, sim.Text, est.Text)
	}
	if installed() {
		t.Errorf("%s is installed after the simulation and the estimate", pkg)
	}
	for _, f := range files {
		if digest(f) != before[f] {
			t.Errorf("%s changed", f)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "/var/log/apt/eipp.log.xz")); err == nil {
		t.Error("the log of the planner was written")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, dir, "cache", "archives")); len(entries) > 1 {
		t.Errorf("something was downloaded: %v", entries)
	}

	// the control: the command of the earlier version, with the trivial-only option, is a transaction of one package for one name, and apt goes on with it
	old := run(slices.Concat([]string{"apt-get", "-o", "Dir::Cache=" + dir + "/cache", "-o", "Debug::NoLocking=true", "-o", "APT::Get::Trivial-Only=true", "install", "--no-install-recommends"}, []string{pkg}))
	if !installed() {
		t.Errorf("the control did not install %s, so this test would not have seen an install:\n%s", pkg, old.Text)
	}
}
