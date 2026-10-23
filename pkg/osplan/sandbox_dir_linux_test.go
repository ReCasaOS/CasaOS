//go:build linux

package osplan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// statInfo is a file as the system describes it to the sweep: its owner and its device are the ones given.
type statInfo struct {
	mode     fs.FileMode
	uid, dev uint64
	noStat   bool
}

func (statInfo) Name() string        { return "casaos-os-plan.1" }
func (statInfo) Size() int64         { return 0 }
func (s statInfo) Mode() fs.FileMode { return s.mode }
func (statInfo) ModTime() time.Time  { return time.Time{} }
func (s statInfo) IsDir() bool       { return s.mode.IsDir() }
func (s statInfo) Sys() any {
	if s.noStat {
		return nil
	}
	st := &syscall.Stat_t{}
	st.Uid, st.Dev = uint32(s.uid), s.dev
	return st
}

func TestADirectoryIsTheSandboxsOnlyIfItsOwnerItsModeAndItsDeviceSayIt(t *testing.T) {
	euid := uint64(os.Geteuid())
	parent := statInfo{mode: fs.ModeDir | fs.ModeSticky | 0o777, uid: 0, dev: 7}
	for _, tc := range []struct {
		name string
		info statInfo
		why  string
	}{
		{"its own, as it makes it", statInfo{mode: fs.ModeDir | 0o711, uid: euid, dev: 7}, ""},
		{"its own, 0700", statInfo{mode: fs.ModeDir | 0o700, uid: euid, dev: 7}, ""},
		{"another user's", statInfo{mode: fs.ModeDir | 0o711, uid: euid + 1, dev: 7}, "it belongs to another user"},
		{"writable by the group", statInfo{mode: fs.ModeDir | 0o771, uid: euid, dev: 7}, "others may write in it"},
		{"writable by anyone", statInfo{mode: fs.ModeDir | 0o717, uid: euid, dev: 7}, "others may write in it"},
		{"writable by anyone, with the sticky bit", statInfo{mode: fs.ModeDir | fs.ModeSticky | 0o777, uid: euid, dev: 7}, "others may write in it"},
		{"on another file system", statInfo{mode: fs.ModeDir | 0o711, uid: euid, dev: 8}, "it is on another file system than /var/tmp"},
		{"of nobody the system can say", statInfo{mode: fs.ModeDir | 0o711, noStat: true}, "who owns it, and where it is, cannot be seen"},
	} {
		if got := notOurs(tc.info, parent); got != tc.why {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.why)
		}
	}
	// the parent that the system cannot say anything of is no parent to compare with
	if got := notOurs(statInfo{mode: fs.ModeDir | 0o711, uid: euid, dev: 0}, statInfo{noStat: true}); got != "who owns it, and where it is, cannot be seen" {
		t.Errorf("a parent that cannot be seen: %q", got)
	}
}

// a directory that is another user's, as far as the sandbox knows who it is, is left alone, and leftAlone is told why
func TestTheSweepLeavesAloneADirectoryOfAnotherUser(t *testing.T) {
	parent := t.TempDir()
	other := filepath.Join(parent, "casaos-os-plan.4")
	if err := os.MkdirAll(filepath.Join(other, "lists"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(other, 0o711); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * StaleAfter)
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatal(err)
	}
	real := sandboxEUID
	sandboxEUID = func() int { return os.Geteuid() + 1 }
	defer func() { sandboxEUID = real }()
	var said []string
	removed, err := SweepStale(parent, time.Now(), StaleAfter, func(dir, why string) { said = append(said, dir+": "+why) })
	if err != nil || len(removed) != 0 {
		t.Errorf("removed = %v, %v: a directory of another user was removed", removed, err)
	}
	if _, err := os.Lstat(other); err != nil {
		t.Errorf("%s was removed: %v", other, err)
	}
	if len(said) != 1 || said[0] != other+": it belongs to another user" {
		t.Errorf("leftAlone was told %q", said)
	}
}

// The two callers make their directories with the one prefix and sweep with the one age: it is longer than either lives, so that neither sweeps the directory of a plan of the other
// that is still at work. (The plan of an upgrade, whose job has 20 minutes, says it of itself in service/.)
func TestTheAgeOfTheSweepIsLongerThanAPlanOfAModuleLives(t *testing.T) {
	if 2*SolverTimeout >= StaleAfter {
		t.Errorf("a plan of a module runs two simulations of %v: a sweep after %v may remove the directory of one that is at work", SolverTimeout, StaleAfter)
	}
	parent := t.TempDir()
	live := filepath.Join(parent, "casaos-os-plan.7")
	if err := os.Mkdir(live, 0o711); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-(StaleAfter - time.Minute))
	if err := os.Chtimes(live, at, at); err != nil {
		t.Fatal(err)
	}
	if removed, err := SweepStale(parent, time.Now(), StaleAfter, nil); err != nil || len(removed) != 0 {
		t.Errorf("removed %v, %v", removed, err)
	}
}

// A removal that fails is tried once more after a moment, and said to Log; what is still there after the second try is said by LeftBehind, for the state of the job. The directory
// is removed once: the function that removes it is safe to call again. The directories are the real ones, in /var/tmp.
func TestADirectoryThatCannotBeRemovedIsTriedOnceMoreAndSaid(t *testing.T) {
	realRemove, realWait := removeDir, removeRetryAfter
	defer func() { removeDir, removeRetryAfter = realRemove, realWait }()
	removeRetryAfter = 5 * time.Millisecond
	for _, tc := range []struct {
		name     string
		failures int
		calls    int
		left     bool
		logs     []string
	}{
		{"it goes the first time", 0, 1, false, nil},
		{"it goes the second time", 1, 2, false, []string{"trying once more"}},
		{"it does not go", 2, 2, true, []string{"trying once more", "left for a later start"}},
		{"it does not go, and is not tried a third time", 10, 2, true, []string{"left for a later start"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log strings.Builder
			sb := &DirSandbox{Log: func(msg, dir string, err error) { fmt.Fprintf(&log, "%s %s %v\n", msg, dir, err) }}
			dir, remove, err := sb.MakeDir()
			if err != nil {
				t.Skipf("no sandbox directory here: %v", err)
			}
			calls := 0
			removeDir = func(parent, name string) error {
				calls++
				if calls <= tc.failures {
					return errors.New("device or resource busy")
				}
				return realRemove(parent, name)
			}
			remove()
			remove()
			if calls != tc.calls {
				t.Errorf("the removal was tried %d times, want %d", calls, tc.calls)
			}
			if got := sb.LeftBehind(); (got != "") != tc.left || (tc.left && got != dir) {
				t.Errorf("LeftBehind() = %q, want %v for %s", got, tc.left, dir)
			}
			for _, want := range tc.logs {
				if !strings.Contains(log.String(), want) || !strings.Contains(log.String(), "device or resource busy") || !strings.Contains(log.String(), dir) {
					t.Errorf("Log was not told %q, where and why:\n%s", want, log.String())
				}
			}
			if len(tc.logs) == 0 && log.Len() != 0 {
				t.Errorf("a removal that went is said as one that did not:\n%s", log.String())
			}
			// whatever happened, the real directory goes at the end of the test
			removeDir = realRemove
			if err := realRemove(SandboxParent, filepath.Base(dir)); err != nil {
				t.Error(err)
			}
		})
	}
}
