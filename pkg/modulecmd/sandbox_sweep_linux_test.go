//go:build linux

package modulecmd

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A plan that was killed leaves its directory in /var/tmp. The next plan or install removes the ones that are old, and only those that are the assistant's: a directory with the name of a
// sandbox, that is a directory and not a link, that belongs to the user who runs, that nobody else may write in, and that is older than an hour.

func plantSandbox(t *testing.T, parent, name string, age time.Duration, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(parent, name)
	if err := os.MkdirAll(filepath.Join(p, "cache", "archives", "partial"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(p, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(p, "tmp"), 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "apt-config"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTheSandboxesOfPlansThatWereKilledAreRemovedByTheNextOne(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	// what goes: old, the assistant's own, with a link inside to something outside that must stay
	gone := plantSandbox(t, parent, "casaos-os-plan.111", 2*time.Hour, 0o711)
	if err := os.Symlink(outside, filepath.Join(gone, "link")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(gone, old, old); err != nil { // (the link made the directory young)
		t.Fatal(err)
	}
	goneToo := plantSandbox(t, parent, "casaos-os-plan.222", 61*time.Minute, 0o700)
	// what stays
	young := plantSandbox(t, parent, "casaos-os-plan.333", 59*time.Minute, 0o711)
	writable := plantSandbox(t, parent, "casaos-os-plan.444", 2*time.Hour, 0o777)
	groupWritable := plantSandbox(t, parent, "casaos-os-plan.445", 2*time.Hour, 0o775)
	linkTarget := plantSandbox(t, outside, "casaos-os-plan.555", 2*time.Hour, 0o711)
	link := filepath.Join(parent, "casaos-os-plan.556")
	if err := os.Symlink(linkTarget, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(parent, "casaos-os-plan.666")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var others []string
	for _, name := range []string{"casaos-os-plan.", "casaos-os-plan.12 34", "xcasaos-os-plan.1", "casaos-os-plan.1.bak", "Casaos-os-plan.1", "casaos-os-plan", "casaos-os-plan.a-b", "other.1", "casaos-module-virt"} {
		others = append(others, plantSandbox(t, parent, name, 5*time.Hour, 0o711))
	}

	removed, err := sweepStaleSandboxes(parent, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(removed)
	if want := []string{gone, goneToo}; !slices.Equal(removed, want) {
		t.Errorf("removed %q, want %q", removed, want)
	}
	for _, p := range []string{gone, goneToo} {
		if exists(p) {
			t.Errorf("%s is still there", p)
		}
	}
	for _, p := range append([]string{young, writable, groupWritable, linkTarget, link, file, keep}, others...) {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
	// nothing is wrong with a parent that is not there, and a second sweep has nothing to do
	if removed, err := sweepStaleSandboxes(filepath.Join(parent, "none"), time.Now(), time.Hour); removed != nil || err != nil {
		t.Errorf("no parent: %v, %v", removed, err)
	}
	if removed, err := sweepStaleSandboxes(parent, time.Now(), time.Hour); err != nil || len(removed) != 0 {
		t.Errorf("a second time: %v, %v", removed, err)
	}
	// a moment later the one that was young is old
	if removed, err := sweepStaleSandboxes(parent, time.Now().Add(2*time.Minute), time.Hour); err != nil || !slices.Equal(removed, []string{young}) {
		t.Errorf("later: %v, %v", removed, err)
	}
}

// a directory of another user, with the name of a sandbox, is not touched
func TestASandboxOfAnotherUserIsLeftAlone(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to make a directory that belongs to somebody else")
	}
	parent := t.TempDir()
	p := plantSandbox(t, parent, "casaos-os-plan.777", 3*time.Hour, 0o711)
	if err := os.Chown(p, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if removed, err := sweepStaleSandboxes(parent, time.Now(), time.Hour); err != nil || len(removed) != 0 || !exists(p) {
		t.Errorf("removed %v, %v", removed, err)
	}
}

// a plan or an install begins by removing them: the sandbox makes its directory after it has swept the parent
func TestMakingASandboxSweepsTheStaleOnes(t *testing.T) {
	disposable(t)
	parent := t.TempDir()
	p := plantSandbox(t, parent, "casaos-os-plan.888", 61*time.Minute, 0o711)
	recent := plantSandbox(t, parent, "casaos-os-plan.889", 30*time.Minute, 0o711)
	defer func(old string) { sandboxParent = old }(sandboxParent)
	sandboxParent = parent
	_, remove, err := newSandbox(Roots{})().MakeDir()
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	if exists(p) {
		t.Error("the directory of the plan that was killed is still there")
	}
	if !exists(recent) {
		t.Error("the directory of a plan that may be running was removed")
	}
}

// a mount point with the name of a sandbox is somebody's file system, and whatever is in it is not removed
func TestAMountPointNamedLikeASandboxIsLeftAlone(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to mount")
	}
	parent := t.TempDir()
	p := filepath.Join(parent, "casaos-os-plan.999")
	if err := os.Mkdir(p, 0o711); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", p, "tmpfs", 0, "mode=0711,size=1m"); err != nil {
		t.Skipf("no mounting here: %v", err)
	}
	defer func() {
		if err := unix.Unmount(p, 0); err != nil {
			t.Errorf("unmount: %v", err)
		}
	}()
	inside := filepath.Join(p, "precious")
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-5 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if removed, err := sweepStaleSandboxes(parent, time.Now(), time.Hour); err != nil || len(removed) != 0 || !exists(inside) {
		t.Errorf("removed %v, %v; the file is there: %v", removed, err, exists(inside))
	}
}
