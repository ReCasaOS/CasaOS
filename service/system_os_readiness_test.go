package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

func readinessProbe(t *testing.T) (osReadinessProbe, *systemPackageUpdater) {
	t.Helper()
	updater := newTestSystemPackageUpdater(t)
	return osReadinessProbe{updater: updater}, updater
}

// The probe of this box has the methods of the seam and no more: nothing that writes can be added
// to it without this test, and the one in pkg/osreadiness, saying so.
func TestTheProbeOfThisBoxHasTheMethodsOfTheSeamAndNoMore(t *testing.T) {
	seam := reflect.TypeOf((*osreadiness.Probe)(nil)).Elem()
	probe := reflect.TypeOf(osReadinessProbe{})
	if probe.NumMethod() != seam.NumMethod() {
		var names []string
		for i := 0; i < probe.NumMethod(); i++ {
			names = append(names, probe.Method(i).Name)
		}
		t.Fatalf("the probe has the exported methods %v, the seam %d", names, seam.NumMethod())
	}
}

func TestACommandIsRunInEnglishAndItsEndIsSaid(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	t.Setenv("LC_ALL", "fr_FR.UTF-8")
	probe := osReadinessProbe{updater: newSystemPackageUpdater()}

	out, err := probe.Run(context.Background(), sh, "-c", "printf %s \"$LC_ALL\"")
	if err != nil || out.Text != "C" || out.ExitCode != 0 {
		t.Fatalf("out, err = %+v, %v: the command must run with LC_ALL=C", out, err)
	}
	out, err = probe.Run(context.Background(), sh, "-c", "echo said; exit 3")
	if err != nil || out.ExitCode != 3 || out.Text != "said\n" {
		t.Fatalf("a command that fails is an exit status, and not an error: %+v, %v", out, err)
	}
	if _, err := probe.Run(context.Background(), "a-command-that-is-not-installed-anywhere"); !errors.Is(err, osreadiness.ErrToolMissing) {
		t.Fatalf("err = %v, want ErrToolMissing", err)
	}
	if _, err := probe.Run(context.Background(), "/nowhere/at/all"); !errors.Is(err, osreadiness.ErrToolMissing) {
		t.Fatalf("a path that is not there: err = %v, want ErrToolMissing", err)
	}
	if _, err := probe.Run(context.Background()); err == nil {
		t.Fatal("no command is not a command")
	}
	// killed by something that is not the end of its time: not an exit status
	if out, err := probe.Run(context.Background(), sh, "-c", "kill -9 $$"); err == nil {
		t.Fatalf("a command that was killed gave %+v and no error", out)
	}
}

func TestACommandThatRunsOutOfTimeIsTheContextsError(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	probe := osReadinessProbe{updater: newSystemPackageUpdater()}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = probe.Run(ctx, sh, "-c", "sleep 5")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 3*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(started))
	}
}

// What a command prints is kept up to the bound as it is read, and no more: a tool that floods
// (a wedged docker, a hostile format) is not held in memory until it ends.
func TestTheOutputOfACommandIsBoundedAsItIsRead(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	probe := osReadinessProbe{updater: newSystemPackageUpdater()}
	emit := func(bytes int, to string) string { return fmt.Sprintf("head -c %d /dev/zero %s", bytes, to) }
	for name, tc := range map[string]struct {
		script    string
		length    int
		truncated bool
	}{
		"a little over the bound":         {emit(osreadiness.MaxOutputBytes+100, ""), osreadiness.MaxOutputBytes, true},
		"one byte over the bound":         {emit(osreadiness.MaxOutputBytes+1, ""), osreadiness.MaxOutputBytes, true},
		"exactly the bound":               {emit(osreadiness.MaxOutputBytes, ""), osreadiness.MaxOutputBytes, false},
		"one byte under the bound":        {emit(osreadiness.MaxOutputBytes-1, ""), osreadiness.MaxOutputBytes - 1, false},
		"a flood on the error output":     {emit(40<<20, "1>&2"), osreadiness.MaxOutputBytes, true},
		"a flood on the standard output":  {emit(40<<20, ""), osreadiness.MaxOutputBytes, true},
		"the two outputs share the bound": {emit(osreadiness.MaxOutputBytes, "") + "; " + emit(osreadiness.MaxOutputBytes, "1>&2"), osreadiness.MaxOutputBytes, true},
		"nothing":                         {"true", 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			started := time.Now()
			out, err := probe.Run(context.Background(), sh, "-c", tc.script)
			if err != nil || out.ExitCode != 0 || len(out.Text) != tc.length || out.Truncated != tc.truncated {
				t.Fatalf("%d bytes, truncated %v, exit %d, err %v; want %d bytes, truncated %v", len(out.Text), out.Truncated, out.ExitCode, err, tc.length, tc.truncated)
			}
			if time.Since(started) > 10*time.Second {
				t.Fatalf("the command took %v", time.Since(started))
			}
		})
	}
	out, err := probe.Run(context.Background(), sh, "-c", "echo said; echo failed 1>&2; exit 3")
	if err != nil || out.ExitCode != 3 || !strings.Contains(out.Text, "said\n") || !strings.Contains(out.Text, "failed\n") || out.Truncated {
		t.Fatalf("out = %+v, err = %v: both outputs are what the command said", out, err)
	}
}

func TestTheWriterThatBoundsAnOutputKeepsTheStartAndSaysItCutTheRest(t *testing.T) {
	const bound = 10
	for name, tc := range map[string]struct {
		writes    []string
		text      string
		truncated bool
	}{
		"nothing":                        {nil, "", false},
		"less":                           {[]string{"abc", "def"}, "abcdef", false},
		"exactly the bound":              {[]string{"abcde", "fghij"}, "abcdefghij", false},
		"one over":                       {[]string{"abcde", "fghijk"}, "abcdefghij", true},
		"a write that crosses the bound": {[]string{"abcdefg", "hijklmnop", "qrs"}, "abcdefghij", true},
		"a write that is the whole":      {[]string{"abcdefghijklmnopqrstuvwxyz"}, "abcdefghij", true},
		"after the bound":                {[]string{"abcdefghij", "k", "lmn"}, "abcdefghij", true},
	} {
		t.Run(name, func(t *testing.T) {
			w := &boundedOutput{max: bound}
			for _, chunk := range tc.writes {
				// the whole write is taken: the process is not blocked, and not told to stop
				if n, err := w.Write([]byte(chunk)); n != len(chunk) || err != nil {
					t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
				}
			}
			text, truncated := w.result()
			if string(text) != tc.text || truncated != tc.truncated {
				t.Fatalf("result = %q, %v; want %q, %v", text, truncated, tc.text, tc.truncated)
			}
			if len(w.buf) > bound+1 {
				t.Fatalf("%d bytes are kept, which is more than the bound and the one that says it was crossed", len(w.buf))
			}
		})
	}
}

// A tool that forked and left a child holding its output open is a tool that ended: the report waits
// for the pipe two seconds, and not for as long as the child lives. What it printed is not an answer
// the report can believe, and it says so.
func TestACommandWhoseChildHoldsItsOutputOpenDoesNotHoldTheReport(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	probe := osReadinessProbe{updater: newSystemPackageUpdater()}

	started := time.Now()
	_, err = probe.Run(context.Background(), sh, "-c", "sleep 7 & echo said")
	if elapsed := time.Since(started); elapsed > 5*time.Second || elapsed < time.Second {
		t.Fatalf("took %v: the pipe is waited for two seconds, and not less, and not until its child ends", elapsed)
	}
	if err == nil {
		t.Fatal("an output that a child still holds was taken for a whole one")
	}

	// and when its time ends: the process is killed, the child is left to end by itself, and the end of the
	// time is what is said
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started = time.Now()
	_, err = probe.Run(ctx, sh, "-c", "sleep 7 & sleep 7")
	if elapsed := time.Since(started); elapsed > 5*time.Second || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v after %v", err, elapsed)
	}
}

func TestFilesAreReadWithinTheirBounds(t *testing.T) {
	probe, _ := readinessProbe(t)
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	ctx := context.Background()

	if data, err := probe.ReadFile(ctx, write("small", "hello\n")); err != nil || string(data) != "hello\n" {
		t.Fatalf("data = %q, err = %v", data, err)
	}
	if data, err := probe.ReadFile(ctx, write("exact", strings.Repeat("a", osreadiness.MaxFileBytes))); err != nil || len(data) != osreadiness.MaxFileBytes {
		t.Fatalf("a file of exactly the bound: %d bytes, err = %v", len(data), err)
	}
	if _, err := probe.ReadFile(ctx, write("large", strings.Repeat("a", osreadiness.MaxFileBytes+1))); !errors.Is(err, osreadiness.ErrTooLarge) {
		t.Fatalf("a file over the bound: err = %v", err)
	}
	if _, err := probe.ReadFile(ctx, filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a file that is not there: err = %v", err)
	}
	if _, err := probe.ReadFile(ctx, dir); err == nil {
		t.Fatal("a directory was read as a file")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := probe.ReadFile(cancelled, write("late", "x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("after the end of the time: err = %v", err)
	}
	// /proc says a size of zero for what it has: it is read all the same
	if _, err := os.Stat("/proc/uptime"); err == nil {
		if data, err := probe.ReadFile(ctx, "/proc/uptime"); err != nil || len(data) == 0 {
			t.Fatalf("/proc/uptime: %q, %v", data, err)
		}
	}
}

// Every look at the machine that has a context is given it first: one that has ended is not asked.
func TestWhatTheProbeLooksAtIsNotLookedAtAfterTheEndOfItsTime(t *testing.T) {
	probe, _ := readinessProbe(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(dir, "link")); err != nil {
		t.Skip("no symbolic links here")
	}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probe.ReadDir(ended, dir); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadDir: err = %v", err)
	}
	if _, err := probe.Stat(ended, dir); !errors.Is(err, context.Canceled) {
		t.Errorf("Stat: err = %v", err)
	}
	if _, err := probe.Lstat(ended, filepath.Join(dir, "link")); !errors.Is(err, context.Canceled) {
		t.Errorf("Lstat: err = %v", err)
	}
	if _, err := probe.Readlink(ended, filepath.Join(dir, "link")); !errors.Is(err, context.Canceled) {
		t.Errorf("Readlink: err = %v", err)
	}
	if _, err := probe.ReadFile(ended, filepath.Join(dir, "a")); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadFile: err = %v", err)
	}
	// and with time, the same calls answer
	live := context.Background()
	if _, err := probe.ReadDir(live, dir); err != nil {
		t.Errorf("ReadDir: err = %v", err)
	}
	if _, err := probe.Stat(live, dir); err != nil {
		t.Errorf("Stat: err = %v", err)
	}
	if _, err := probe.Lstat(live, filepath.Join(dir, "link")); err != nil {
		t.Errorf("Lstat: err = %v", err)
	}
	if _, err := probe.Readlink(live, filepath.Join(dir, "link")); err != nil {
		t.Errorf("Readlink: err = %v", err)
	}
}

func TestDirectoriesAndLinksAreListedAndSeen(t *testing.T) {
	probe, _ := readinessProbe(t)
	dir := t.TempDir()
	for _, name := range []string{"b", "a", "c"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("a", filepath.Join(dir, "link")); err != nil {
		t.Skip("no symbolic links here")
	}
	ctx := context.Background()

	names, err := probe.ReadDir(ctx, dir)
	if err != nil || !slices.Equal(names, []string{"a", "b", "c", "link"}) {
		t.Fatalf("names = %v, err = %v", names, err)
	}
	if _, err := probe.ReadDir(ctx, filepath.Join(dir, "nowhere")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
	link := filepath.Join(dir, "link")
	if info, err := probe.Lstat(ctx, link); err != nil || !info.IsSymlink() {
		t.Fatalf("Lstat = %+v, %v", info, err)
	}
	if info, err := probe.Stat(ctx, link); err != nil || info.IsSymlink() || info.IsDir() {
		t.Fatalf("Stat follows the link: %+v, %v", info, err)
	}
	if info, err := probe.Stat(ctx, dir); err != nil || !info.IsDir() {
		t.Fatalf("Stat of a directory = %+v, %v", info, err)
	}
	if target, err := probe.Readlink(ctx, link); err != nil || target != "a" {
		t.Fatalf("Readlink = %q, %v", target, err)
	}
	if _, err := probe.Readlink(ctx, filepath.Join(dir, "a")); err == nil {
		t.Fatal("a file that is not a link has a target")
	}
	if _, err := probe.Stat(ctx, filepath.Join(dir, "nowhere")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

// asReadinessCommand is the commands of an apt box (the fake of the updater's tests) as the probe runs
// its own.
func asReadinessCommand(box *aptBox) func(context.Context, string, ...string) ([]byte, bool, error) {
	return func(ctx context.Context, name string, args ...string) ([]byte, bool, error) {
		out, err := box.command(ctx, name, args...)
		return out, false, err
	}
}

// MaintenanceBusy is the updater's own question, with one thing more: when it could not ask (dpkg's
// lock cannot be looked at, or the updater is not one that has a lock to look at), it says so, and
// the check says it did not know: a lock that cannot be seen is not a lock that nobody holds.
func TestMaintenanceBusyIsTheUpdatersOwnQuestion(t *testing.T) {
	ctx := context.Background()
	probe, updater := readinessProbe(t)
	probe.command = asReadinessCommand(&aptBox{})
	updater.dpkgLockState = func() (bool, error) { return false, nil }
	if reason, busy, err := probe.MaintenanceBusy(ctx); busy || err != nil {
		t.Fatalf("an idle box: busy = %v (%q), err = %v", busy, reason, err)
	}
	// every unit counts, the package update's too: none is the report's own
	for _, unit := range systemMaintenanceUnits {
		probe.command = asReadinessCommand(&aptBox{activeUnits: map[string]bool{unit: true}})
		if reason, busy, err := probe.MaintenanceBusy(ctx); !busy || reason == "" || err != nil {
			t.Errorf("%s runs: busy = %v, reason = %q, err = %v", unit, busy, reason, err)
		}
	}
	// a unit that runs is an answer, whatever the lock says
	updater.dpkgLockState = func() (bool, error) { return false, errors.New("no lock") }
	if _, busy, err := probe.MaintenanceBusy(ctx); !busy || err != nil {
		t.Errorf("a unit runs and the lock cannot be looked at: busy = %v, err = %v", busy, err)
	}

	probe.command = asReadinessCommand(&aptBox{})
	updater.dpkgLockState = func() (bool, error) { return true, nil }
	if reason, busy, err := probe.MaintenanceBusy(ctx); !busy || !strings.Contains(reason, "dpkg") || err != nil {
		t.Fatalf("a lock that is held: busy = %v, reason = %q, err = %v", busy, reason, err)
	}
}

func TestMaintenanceBusySaysWhenItCouldNotAsk(t *testing.T) {
	ctx := context.Background()
	denied := &fs.PathError{Op: "open", Path: "/var/lib/dpkg/lock-frontend", Err: fs.ErrPermission}
	for name, tc := range map[string]struct {
		setup func(u *systemPackageUpdater)
		is    error
	}{
		"a lock file that may not be opened": {func(u *systemPackageUpdater) { u.dpkgLockState = func() (bool, error) { return false, denied } }, fs.ErrPermission},
		"a lock that cannot be looked at": {func(u *systemPackageUpdater) {
			u.dpkgLockState = func() (bool, error) { return false, errors.New("boom") }
		}, nil},
		"no way to look at the lock at all": {func(u *systemPackageUpdater) { u.dpkgLockState, u.dpkgLocked = nil, nil }, nil},
		"not root":                          {func(u *systemPackageUpdater) { u.getEUID = func() int { return 1000 } }, nil},
		"not a Debian": {func(u *systemPackageUpdater) {
			u.readOSRelease = func() (map[string]string, error) { return map[string]string{"ID": "arch"}, nil }
		}, nil},
		"an os-release that cannot be read": {func(u *systemPackageUpdater) {
			u.readOSRelease = func() (map[string]string, error) { return nil, errors.New("unreadable") }
		}, nil},
		"no apt-get": {func(u *systemPackageUpdater) {
			u.lookPath = func(name string) (string, error) {
				if name == "apt-get" {
					return "", errors.New("not found")
				}
				return "/usr/bin/" + name, nil
			}
		}, nil},
		"no systemd-run": {func(u *systemPackageUpdater) {
			u.lookPath = func(name string) (string, error) {
				if name == "systemd-run" {
					return "", errors.New("not found")
				}
				return "/usr/bin/" + name, nil
			}
		}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			probe, updater := readinessProbe(t)
			probe.command = asReadinessCommand(&aptBox{})
			updater.dpkgLockState = func() (bool, error) { return false, nil }
			tc.setup(updater)
			reason, busy, err := probe.MaintenanceBusy(ctx)
			if err == nil || busy {
				t.Fatalf("busy = %v (%q), err = %v: the lock was not looked at, and the answer says it was", busy, reason, err)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Fatalf("err = %v, want one that is %v", err, tc.is)
			}
		})
	}

	// the older way to say it, a function of one answer, is still one: a box that has only it is asked
	probe, updater := readinessProbe(t)
	probe.command = asReadinessCommand(&aptBox{})
	updater.dpkgLocked = func() bool { return true }
	if _, busy, err := probe.MaintenanceBusy(ctx); !busy || err != nil {
		t.Fatalf("busy = %v, err = %v", busy, err)
	}
	updater.dpkgLocked = func() bool { return false }
	if _, busy, err := probe.MaintenanceBusy(ctx); busy || err != nil {
		t.Fatalf("busy = %v, err = %v", busy, err)
	}
}

// The ReCasaOS update asks the same question, and does not get the new answer: a host that has no
// lock to look at is not one it refuses to start on.
func TestTheQuestionOfTheUpdateIsStillNotAnErrorWhenTheLockCannotBeSeen(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{}).command
	updater.dpkgLockState = func() (bool, error) { return false, errors.New("boom") }
	if reason, busy := updater.maintenanceBusy(context.Background(), ""); busy {
		t.Fatalf("a lock that cannot be seen is held: %q", reason)
	}
	updater.dpkgLockState, updater.dpkgLocked = nil, nil
	if _, busy := updater.maintenanceBusy(context.Background(), ""); busy {
		t.Fatal("a box with no way to look at the lock is busy")
	}
}

func TestAppOperationsAreAskedForWithoutTheLockOfTheUpdater(t *testing.T) {
	probe, updater := readinessProbe(t)
	if _, err := probe.AppOperations(context.Background()); err == nil {
		t.Fatal("AppManagement was asked before anyone said how")
	}

	system := &systemService{packageUpdates: updater}
	system.SetAppOperations(func(context.Context) ([]string, error) {
		return []string{"nextcloud", "jellyfin", "nextcloud", "<img src=x>"}, nil
	})
	// an update holds the lock of the updater: the report does not wait for it
	updater.mu.Lock()
	answered := make(chan struct{})
	var apps []string
	var err error
	go func() {
		apps, err = probe.AppOperations(context.Background())
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(3 * time.Second):
		updater.mu.Unlock()
		t.Fatal("the question waited for the lock of the updater")
	}
	updater.mu.Unlock()
	if err != nil || !slices.Equal(apps, []string{"(invalid name)", "jellyfin", "nextcloud"}) {
		t.Fatalf("apps = %v, err = %v", apps, err)
	}

	system.SetAppOperations(func(context.Context) ([]string, error) { return nil, errors.New("AppManagement did not answer") })
	if _, err := probe.AppOperations(context.Background()); err == nil {
		t.Fatal("an AppManagement that does not answer was taken for one with nothing to do")
	}
}

func TestTheClockTheUserAndTheRoomComeFromTheUpdater(t *testing.T) {
	probe, updater := readinessProbe(t)
	updater.getEUID = func() int { return 1000 }
	updater.now = func() time.Time { return time.Date(2026, 10, 11, 20, 15, 2, 0, time.UTC) }
	updater.freeBytes = func(path string) (uint64, error) { return uint64(len(path)), nil }
	if probe.EUID() != 1000 || !probe.Now().Equal(time.Date(2026, 10, 11, 20, 15, 2, 0, time.UTC)) || probe.Arch() != runtime.GOARCH {
		t.Fatalf("euid %d, now %v, arch %q", probe.EUID(), probe.Now(), probe.Arch())
	}
	if free, err := probe.FreeBytes(context.Background(), "/boot"); err != nil || free != 5 {
		t.Fatalf("free = %d, err = %v", free, err)
	}
	updater.freeBytes = nil
	if _, err := probe.FreeBytes(context.Background(), "/boot"); err == nil {
		t.Fatal("the room was read where it cannot be")
	}
}

// A page that asks again and again, or two pages, make the box run the checks once.
func TestRequestsThatComeWhileAReportIsMadeAreAnsweredWithIt(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	original := runOSReadiness
	t.Cleanup(func() { runOSReadiness = original })
	runOSReadiness = func(ctx context.Context, p osreadiness.Probe, opts osreadiness.Options) osreadiness.Report {
		runs.Add(1)
		<-release
		return osreadiness.Report{State: osreadiness.StateReady, Checks: []osreadiness.Finding{}}
	}
	system := &systemService{packageUpdates: newTestSystemPackageUpdater(t)}

	var wg sync.WaitGroup
	answers := make([]osreadiness.Answer, 4)
	for i := range answers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i] = system.GetSystemOSReadiness()
		}()
	}
	// all of them are in before the report is made
	deadline := time.Now().Add(3 * time.Second)
	for runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := runs.Load(); got != 1 {
		t.Fatalf("%d reports were made for 4 requests", got)
	}
	for _, answer := range answers {
		if answer.State != osreadiness.StateReady || !strings.Contains(answer.Text, "Ready.") {
			t.Fatalf("answer = %+v", answer)
		}
	}
	// nothing is kept: the next request makes a report of its own
	system.GetSystemOSReadiness() // the channel is closed: it is not waited for
	if got := runs.Load(); got != 2 {
		t.Fatalf("%d reports after a later request, want 2: the answer was kept", got)
	}
}
