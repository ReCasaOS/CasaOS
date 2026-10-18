package modulecmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"golang.org/x/sys/unix"
)

// victim is a file outside what the assistant owns, which a link planted by somebody is made to point at: it must come out of any run as it went in.
func victim(t *testing.T, f *fixture) (path string) {
	t.Helper()
	f.writeFile("/etc/victim", "keep me")
	return f.path("/etc/victim")
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no links here:", err)
	}
}

func keptVictim(t *testing.T, path string) {
	t.Helper()
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "keep me" {
		t.Errorf("the file the link pointed at: %q, %v", raw, err)
	}
}

// The files of the archive are made new, and never opened through a link: a link that is planted in the area of the download after it was emptied is an error, and what it points at is not
// written.
func TestTheFilesOfTheDownloadAreNeverMadeThroughALink(t *testing.T) {
	for name, c := range map[string]struct {
		plant func(f *fixture) string // gives the path of the link
		at    string                  // the step that plants it, when it begins
		step  string
	}{
		"the archive": {func(f *fixture) string { return f.env.Roots.tmp() + "/" + archiveName }, StepPackages, StepDownload},
		"the tar":     {func(f *fixture) string { return f.env.Roots.tmp() + "/" + tarName }, StepVerify, StepUnpack},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			v := victim(t, f)
			f.env.StepHook = func(_ context.Context, step, phase string) {
				if step == c.at && phase == "done" {
					symlinkOrSkip(t, v, c.plant(f))
				}
			}
			if code := f.install("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), "failed at step "+c.step+": ") {
				t.Fatalf("exit %d\n%s", code, f.out)
			}
			keptVictim(t, v)
		})
	}
}

// ...and neither are they made over a file that is there, whatever it is (a hard link to a file of the assistant's would be written)
func TestTheFilesOfTheDownloadAreNeverMadeOverAFileThatIsThere(t *testing.T) {
	for name, c := range map[string]struct{ at, step, path string }{
		"the archive": {StepPackages, StepDownload, archiveName},
		"the tar":     {StepVerify, StepUnpack, tarName},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			v := victim(t, f)
			f.env.StepHook = func(_ context.Context, step, phase string) {
				if step == c.at && phase == "done" {
					if err := os.Link(v, f.env.Roots.tmp()+"/"+c.path); err != nil {
						t.Skip("no hard links here:", err)
					}
				}
			}
			if code := f.install("--yes"); code != ExitFailed || !strings.Contains(f.out.String(), "failed at step "+c.step+": ") {
				t.Fatalf("exit %d\n%s", code, f.out)
			}
			keptVictim(t, v)
		})
	}
}

// The archive that was checked is the archive that is unpacked: an archive put in its place after the check (by a path, whoever puts it) is not read
func TestTheArchiveThatWasCheckedIsTheOneThatIsUnpacked(t *testing.T) {
	for name, swap := range map[string]func(path string, other []byte) error{
		"removed and made again": func(path string, other []byte) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.WriteFile(path, other, 0o600)
		},
		"renamed over": func(path string, other []byte) error {
			if err := os.WriteFile(path+".new", other, 0o600); err != nil {
				return err
			}
			return os.Rename(path+".new", path)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			evil := append(moduleEntries(manifestJSON(t, testManifest()))[:4:4], tent{name: "build/sysroot/usr/bin/casaos-virt-management", mode: 0o755, body: "#!/bin/sh\necho evil\n"})
			other := gz(t, tarBytes(t, evil))
			swapped := false
			f.env.StepHook = func(_ context.Context, step, phase string) {
				if step == StepVerify && phase == "done" {
					if err := swap(f.env.Roots.tmp()+"/"+archiveName, other); err != nil {
						t.Fatal(err)
					}
					swapped = true
				}
			}
			if code := f.install("--yes"); code != ExitOK || !swapped {
				t.Fatalf("exit %d\n%s", code, f.out)
			}
			if got := f.read("/usr/bin/casaos-virt-management"); got != "#!/bin/sh\nexit 0\n" {
				t.Errorf("the program is %q: the archive that was read is not the one that was checked", got)
			}
		})
	}
}

// The area of the download and the state of the assistant are the assistant's: a directory that is a link, that others may write in, or that is another user's is not emptied, written in or
// read from, and the run is refused with state_unsafe. (A link in place of the area of the download used to be followed, and its content deleted as root.)
func TestADirectoryThatIsNotSafeIsNotUsed(t *testing.T) {
	const tmp, state = "/var/lib/casaos/modules/tmp", "/var/lib/casaos/modules"
	// each makes the directory unsafe, and gives the file that must come out of the run as it went in, if there is one
	link := func(t *testing.T, f *fixture, real string) string {
		elsewhere := filepath.Join(f.root, "elsewhere")
		if err := os.MkdirAll(elsewhere, 0o755); err != nil {
			t.Fatal(err)
		}
		precious := filepath.Join(elsewhere, "precious")
		if err := os.WriteFile(precious, []byte("keep me"), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.RemoveAll(real)
		if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
			t.Fatal(err)
		}
		symlinkOrSkip(t, elsewhere, real)
		return precious
	}
	mode := func(m os.FileMode) func(t *testing.T, f *fixture, real string) string {
		return func(t *testing.T, f *fixture, real string) string {
			if err := os.MkdirAll(real, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(real, m); err != nil {
				t.Fatal(err)
			}
			return ""
		}
	}
	another := func(t *testing.T, f *fixture, real string) string {
		if os.Geteuid() != 0 {
			t.Skip("needs root to give a directory to another user")
		}
		if err := os.MkdirAll(real, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(real, 12345, 12345); err != nil {
			t.Skip(err)
		}
		return ""
	}
	for name, c := range map[string]struct {
		path string
		make func(t *testing.T, f *fixture, real string) string
	}{
		"the area of the download is a link":               {tmp, link},
		"the area of the download can be written by all":   {tmp, mode(0o777)},
		"the area of the download is written by its group": {tmp, mode(0o775)},
		"the area of the download is written by others":    {tmp, mode(0o757)},
		"the area of the download is another user's":       {tmp, another},
		"the state is a link":                              {state, link},
		"the state can be written by all":                  {state, mode(0o777)},
		"the state is written by its group":                {state, mode(0o775)},
		"the state is written by others":                   {state, mode(0o757)},
		"the state is another user's":                      {state, another},
	} {
		for _, verb := range []string{"install", "remove"} {
			t.Run(name+", "+verb, func(t *testing.T) {
				var f *fixture
				if verb == "install" {
					f = newFixture(t)
				} else {
					f = installed(t)
				}
				precious := c.make(t, f, f.path(c.path))
				var code int
				if verb == "install" {
					code = f.install("--yes")
				} else {
					code = f.remove("--yes")
				}
				if code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+CodeStateUnsafe+"\n") {
					t.Fatalf("exit %d\n%s", code, f.out)
				}
				if precious != "" {
					keptVictim(t, precious)
				}
				if verb == "install" && (f.exists("/usr/bin/casaos-virt-management") || f.runner.called("apt-get", "install") != 0) {
					t.Error("something was done")
				}
				if verb == "remove" && !f.runner.isEnabled(testUnit) {
					t.Error("the unit was stopped")
				}
			})
		}
	}
}

// The lock is never taken through a link (what it points at would be truncated and written), and a lock that is not a file is no lock
func TestTheLockIsNotTakenThroughALink(t *testing.T) {
	f := newFixture(t)
	v := victim(t, f)
	if err := os.MkdirAll(f.env.Roots.state(), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, v, f.env.Roots.lock())
	if release, err := newHost().Lock(f.env.Roots.lock()); err == nil {
		release()
		t.Fatal("the lock was taken through a link")
	}
	keptVictim(t, v)
	if code := f.install("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+CodeLockFailed+"\n") {
		t.Errorf("exit %d\n%s", code, f.out)
	}
	keptVictim(t, v)
	// a directory, a fifo
	if err := os.Remove(f.env.Roots.lock()); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(f.env.Roots.lock(), 0o600); err != nil {
		t.Skip(err)
	}
	if release, err := newHost().Lock(f.env.Roots.lock()); err == nil {
		release()
		t.Error("a fifo is taken for a lock")
	}
}

// The log is never opened through a link, and is a regular file or nothing: not a fifo that waits for a reader, not a device
func TestTheLogIsOnlyEverARegularFileAndNeverThroughALink(t *testing.T) {
	f := newFixture(t)
	v := victim(t, f)
	if err := os.MkdirAll(f.env.Roots.logs(), 0o755); err != nil {
		t.Fatal(err)
	}
	log := f.env.Roots.logFile(testName)
	symlinkOrSkip(t, v, log)
	if code := f.install("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+CodeLogUnwritable+"\n") {
		t.Errorf("a link: exit %d\n%s", code, f.out)
	}
	keptVictim(t, v)
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(log, 0o644); err != nil {
		t.Skip(err)
	}
	if code := f.install("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+CodeLogUnwritable+"\n") {
		t.Errorf("a fifo: exit %d\n%s", code, f.out)
	}
	_ = osmodule.MaxEntries
}
