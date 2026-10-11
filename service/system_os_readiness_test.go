package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

func TestTheOutputOfACommandIsBounded(t *testing.T) {
	probe, updater := readinessProbe(t)
	updater.command = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(strings.Repeat("x", osreadiness.MaxOutputBytes+100)), nil
	}
	out, err := probe.Run(context.Background(), "dpkg", "--audit")
	if err != nil || len(out.Text) != osreadiness.MaxOutputBytes || !out.Truncated {
		t.Fatalf("%d bytes, truncated %v, err %v", len(out.Text), out.Truncated, err)
	}
	updater.command = func(context.Context, string, ...string) ([]byte, error) { return []byte("short"), nil }
	if out, _ := probe.Run(context.Background(), "dpkg", "--audit"); out.Truncated || out.Text != "short" {
		t.Fatalf("out = %+v", out)
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

func TestMaintenanceBusyIsTheUpdatersOwnQuestion(t *testing.T) {
	probe, updater := readinessProbe(t)
	if reason, busy := probe.MaintenanceBusy(context.Background()); busy {
		t.Fatalf("an idle box is busy: %q", reason)
	}
	// every unit counts, the package update's too: none is the report's own
	for _, unit := range systemMaintenanceUnits {
		updater.command = (&aptBox{activeUnits: map[string]bool{unit: true}}).command
		if reason, busy := probe.MaintenanceBusy(context.Background()); !busy || reason == "" {
			t.Errorf("%s runs: busy = %v, reason = %q", unit, busy, reason)
		}
	}
	updater.command = (&aptBox{}).command
	updater.dpkgLocked = func() bool { return true }
	if reason, busy := probe.MaintenanceBusy(context.Background()); !busy || !strings.Contains(reason, "dpkg") {
		t.Fatalf("a lock that is held: busy = %v, reason = %q", busy, reason)
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
	if probe.EUID() != 1000 || !probe.Now().Equal(time.Date(2026, 10, 11, 20, 15, 2, 0, time.UTC)) || probe.Arch() == "" {
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
