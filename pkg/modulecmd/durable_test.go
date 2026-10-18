package modulecmd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// seams puts recorders in place of syncDir and syncFile for the length of a test, and gives what they saw.
type seams struct {
	events []string
	check  func(dir string) // called as a directory is synced
}

func recordSyncs(t *testing.T) *seams {
	t.Helper()
	s := &seams{}
	oldDir, oldFile := syncDir, syncFile
	t.Cleanup(func() { syncDir, syncFile = oldDir, oldFile })
	syncDir = func(dir string) {
		s.events = append(s.events, "syncdir:"+filepath.ToSlash(dir))
		if s.check != nil {
			s.check(dir)
		}
	}
	syncFile = func(f *os.File) error {
		s.events = append(s.events, "syncfile:"+filepath.Base(f.Name()))
		return f.Sync()
	}
	return s
}

func (s *seams) index(event string) int { return slices.Index(s.events, event) }

// A remove makes its deletions durable: every directory that lost a file, a directory or a list is synced, once the entries are gone
func TestARemoveSyncsTheDirectoriesAfterItHasDeletedFromThem(t *testing.T) {
	f := installed(t)
	s := recordSyncs(t)
	gone := map[string][]string{ // the directory, and what must be gone from it when it is synced
		f.path("/usr/bin"):                {f.path("/usr/bin/casaos-virt-management")},
		f.path("/etc/casaos"):             {f.path("/etc/casaos/virt-management.conf.sample")},
		f.path("/usr/lib/systemd/system"): {f.path("/usr/lib/systemd/system/" + testUnit)},
		f.path("/usr/lib/systemd"):        {f.path("/usr/lib/systemd/system")},
		f.path("/usr/lib"):                {f.path("/usr/lib/systemd")},
		f.path("/etc/casaos/modules"):     {f.path("/etc/casaos/modules/virt.json")},
		f.env.Roots.state():               {f.path(filesList), f.path(dirsList), f.path("/var/lib/casaos/modules/virt.version")},
	}
	synced := map[string]bool{}
	state := f.env.Roots.state()
	var stateSyncs []string // what the state directory had in it each time it was synced
	s.check = func(dir string) {
		if dir == state {
			// (synced once after the manifest and the version are gone, and again after the lists are: the first time the lists are still there)
			switch {
			case !exists(f.path("/var/lib/casaos/modules/virt.version")) && exists(f.path(filesList)):
				stateSyncs = append(stateSyncs, "version gone")
			case !exists(f.path(filesList)) && !exists(f.path(dirsList)):
				stateSyncs = append(stateSyncs, "lists gone")
			}
		}
		for _, p := range gone[dir] {
			if exists(p) {
				if dir != state {
					t.Errorf("%s is synced while %s is still there", dir, p)
				}
				return
			}
		}
		synced[dir] = true
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	for dir := range gone {
		if !synced[dir] {
			t.Errorf("%s was not synced after what was in it was deleted (%v)", dir, s.events)
		}
	}
	if strings.Join(stateSyncs, ", ") != "version gone, lists gone" {
		t.Errorf("the state was synced: %v", stateSyncs)
	}
}

// The state is synced after the lists were written and before the first directory is made or file put in place: a kill after that finds the lists on the disk
func TestTheStateIsSyncedBeforeTheFirstFileIsPutInPlace(t *testing.T) {
	f := newFixture(t)
	s := recordSyncs(t)
	f.host.wrapDest = func(d osmodule.Destination) osmodule.Destination {
		return &hookedDest{Destination: d,
			onDir:  func(string) { s.events = append(s.events, "mkdir") },
			onFile: func() { s.events = append(s.events, "file") },
		}
	}
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	state := "syncdir:" + filepath.ToSlash(f.env.Roots.state())
	// (the state is synced for every list that is written, and the first time is before the first directory and the first file)
	if at := s.index(state); at < 0 || at > s.index("mkdir") || at > s.index("file") {
		t.Errorf("events: %v", s.events)
	}
}

func TestAFileIsSyncedBeforeItTakesItsPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x")
	oldFile := syncFile
	t.Cleanup(func() { syncFile = oldFile })
	took := -1
	syncFile = func(f *os.File) error {
		took = 0
		if exists(path) {
			took = 1 // (it is there already: synced too late)
		}
		return f.Sync()
	}
	if err := writeFileAtomic(path, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if took != 0 {
		t.Errorf("synced %d: -1 is never, 1 is after the file took its place", took)
	}
}

// A temporary file is made with O_EXCL: what somebody puts at its name in the instant after the name was cleared is an error, and is not written through (a hard link to a file of the
// assistant's would be)
func TestATemporaryFileIsMadeNewAndNotWrittenThroughWhatIsPutAtItsName(t *testing.T) {
	dir := t.TempDir()
	victimFile := filepath.Join(dir, "victim")
	if err := os.WriteFile(victimFile, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "x")
	oldHook := afterClear
	t.Cleanup(func() { afterClear = oldHook })
	planted := false
	afterClear = func(p string) {
		if err := os.Link(victimFile, p); err != nil {
			t.Skip("no hard links here:", err)
		}
		planted = true
	}
	if err := writeFileAtomic(path, []byte("content"), 0o644); err == nil || !planted {
		t.Fatalf("written through what was put there: %v (planted %v)", err, planted)
	}
	if raw, _ := os.ReadFile(victimFile); string(raw) != "keep me" {
		t.Errorf("the file that was linked: %q", raw)
	}
	if exists(path) {
		t.Error("the file took its place")
	}
}

// What a command prints is cut to the printable ASCII, and the byte 0x7f is not printable
func TestOnlyPrintableAsciiIsSaid(t *testing.T) {
	for b := 0; b < 256; b++ {
		want := "a?b"
		if b >= 0x20 && b <= 0x7e {
			want = "a" + string(rune(b)) + "b"
		}
		if got := printable("a"+string([]byte{byte(b)})+"b", 10); got != want {
			t.Errorf("byte %#x: %q, want %q", b, got, want)
		}
	}
}

// Every service among the files of the module is stopped, and not the one of the manifest alone
func TestARemoveStopsEveryServiceThatTheListNames(t *testing.T) {
	f := installed(t)
	f.writeList(append(f.listed(), fileEntry("/usr/lib/systemd/system/casaos-virt-helper.service", "[Service]\n"))...)
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	for _, unit := range []string{testUnit, "casaos-virt-helper.service"} {
		if f.runner.called("systemctl", "disable", "--now", unit) != 1 {
			t.Errorf("%s was not stopped: %q", unit, f.runner.calls)
		}
	}
}

// A SIGTERM that comes once the unit is stopped does not leave a module half deleted: the deletion is a few unlinks and one systemctl, and it is done whole
func TestARemoveThatIsToldToStopOnceTheUnitIsStoppedIsDoneWhole(t *testing.T) {
	f := installed(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.env.StepHook = func(_ context.Context, step, phase string) {
		if step == StepStop && phase == "done" {
			cancel()
		}
	}
	// (the hook is called with the context of the run: the stop is over, and the run is told to stop)
	if code := f.main(ctx, "remove", testName, "--yes"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	if f.exists("/usr/bin/casaos-virt-management") || f.exists("/etc/casaos/modules/virt.json") || f.exists(filesList) {
		t.Errorf("a module that was half deleted:\n%s", f.out)
	}
}
