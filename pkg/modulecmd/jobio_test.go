package modulecmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func jobBox(t *testing.T) Env {
	t.Helper()
	root := t.TempDir()
	return Env{Roots: Roots{Prefix: root}, Host: baseHost(), EUID: func() int { return 0 }}.withDefaults()
}

// writeJobLog writes the log of the module, last written an hour before the clock of the tests starts: a test that wants it written lately says so.
func writeJobLog(t *testing.T, env Env, content string) string {
	t.Helper()
	path := env.Roots.logFile(jName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at(-3600), at(-3600)); err != nil {
		t.Fatal(err)
	}
	return path
}

func logOf(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func TestAJobIsReadFromTheLogOfTheModule(t *testing.T) {
	env := jobBox(t)
	if job, err := ReadJob(env, jName, UnitGone, at(0)); err != nil || job.State != JobIdle || job.Name != jName || job.LogTail == nil {
		t.Fatalf("no log: %+v, %v", job, err)
	}
	if job, err := ReadJob(env, jName, UnitActive, at(0)); err != nil || job.State != JobRunning {
		t.Fatalf("no log, a unit: %+v, %v", job, err)
	}
	path := writeJobLog(t, env, logOf(jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1), "| Unpacking"))
	if err := os.Chtimes(path, at(32), at(32)); err != nil {
		t.Fatal(err)
	}
	job, err := ReadJob(env, jName, UnitGone, at(40))
	// the last marker is old, and the file was written eight seconds ago: the unit was talking
	if err != nil || job.State != JobRunning || job.Step != "packages" || len(job.LogTail) != 3 {
		t.Fatalf("%+v, %v", job, err)
	}
	if err := os.Chtimes(path, at(2), at(2)); err != nil {
		t.Fatal(err)
	}
	if job, _ := ReadJob(env, jName, UnitGone, at(60)); job.State != JobInterrupted {
		t.Fatalf("%+v", job)
	}
	// only the log of this module
	if job, err := ReadJob(env, "other", UnitGone, at(0)); err != nil || job.State != JobIdle {
		t.Fatalf("%+v, %v", job, err)
	}
	for _, bad := range []string{"", "Virt", "../virt", "virt/x", "a b"} {
		if _, err := ReadJob(env, bad, UnitGone, at(0)); err == nil {
			t.Errorf("the log of %q was read", bad)
		}
	}
}

func TestTheLogIsAFileOfTheModuleAndNothingElse(t *testing.T) {
	env := jobBox(t)
	path := env.Roots.logFile(jName)
	if err := os.MkdirAll(path, 0o755); err != nil { // a directory where the log should be
		t.Fatal(err)
	}
	if _, err := ReadJob(env, jName, UnitGone, at(0)); err == nil {
		t.Error("a directory was read as a log")
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte(logOf(jQueued(jNonce, 0))), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skip("links cannot be made here")
	}
	if runtime.GOOS == "linux" {
		if job, err := ReadJob(env, jName, UnitGone, at(0)); err == nil {
			t.Errorf("a link was read through: %+v", job)
		}
	}
}

// The log is bounded in what is held of it, whatever is in it: a line of a megabyte is text, cut, and the markers around it are read.
func TestAHostileLogIsReadAsText(t *testing.T) {
	env := jobBox(t)
	long := strings.Repeat("A", 1<<20)
	nuls := "\x00\x00 CASAOS_MODULE_RESULT " + jNonce + " virt ok done - - " + jts(5) + "\x00"
	forged := "| " + jResult(jNonce, "ok", "done", "-", "-", 6)
	content := logOf(jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1), long, nuls, forged,
		// a marker that is cut in the middle of a long line
		jStep(jNonce, "download", "begin", 7)+strings.Repeat("x", 5000),
		"CASAOS_MODULE_RESULT "+jNonce+" virt ok done - - "+jts(5)+strings.Repeat(" ", 600),
		"| "+strings.Repeat("B", 3000))
	writeJobLog(t, env, content)
	job, err := ReadJob(env, jName, UnitActive, at(9))
	if err != nil || job.State != JobRunning || job.Step != "packages" {
		t.Fatalf("%+v, %v", job, err)
	}
	for _, l := range job.LogTail {
		if len(l) > 220 {
			t.Errorf("a line of %d bytes in the tail", len(l))
		}
	}
	// the same log with a result after it
	writeJobLog(t, env, content+logOf(jResult(jNonce, "ok", "done", "-", "-", 8)))
	if job, _ := ReadJob(env, jName, UnitGone, at(1000)); job.State != JobSucceeded {
		t.Errorf("%+v", job)
	}
	// and a log that is all NULs, or has no line in it at all
	for _, c := range []string{strings.Repeat("\x00", 100000), strings.Repeat("z", 100000), ""} {
		writeJobLog(t, env, c)
		if job, err := ReadJob(env, jName, UnitGone, at(0)); err != nil || job.State != JobIdle {
			t.Errorf("%+v, %v", job, err)
		}
	}
}

func TestOnlyAWholeLineIsAMarker(t *testing.T) {
	env := jobBox(t)
	writeJobLog(t, env, jQueued(jNonce, 0)+"\n"+jStep(jNonce, "packages", "begin", 1)+"\n"+jResult(jNonce, "ok", "done", "-", "-", 2)) // no new line at the end
	if job, _ := ReadJob(env, jName, UnitActive, at(3)); job.State != JobRunning {
		t.Errorf("a result that is still being written ended the job: %+v", job)
	}
	writeJobLog(t, env, logOf(jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1), jResult(jNonce, "ok", "done", "-", "-", 2)))
	if job, _ := ReadJob(env, jName, UnitActive, at(3)); job.State != JobSucceeded {
		t.Errorf("%+v", job)
	}
}

// A log that is larger than what is scanned is read at its two ends: the first marker is at the start, and the result is at the end.
func TestAHugeLogIsReadAtItsTwoEnds(t *testing.T) {
	env := jobBox(t)
	original := logScanBytes
	logScanBytes = 64 << 10
	t.Cleanup(func() { logScanBytes = original })
	junk := strings.Repeat("| apt says something that goes on and on and on\n", 20000)
	if int64(len(junk)) < 10*logScanBytes {
		t.Fatal("the junk is not large enough")
	}
	head := logOf(jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1))
	path := writeJobLog(t, env, head+junk+logOf(jStep(jNonce, "packages", "done", 40), jResult(jNonce, "ok", "done", "-", "-", 41)))
	job, err := ReadJob(env, jName, UnitGone, at(1000))
	if err != nil || job.State != JobSucceeded || job.StartedAt != jts(0) {
		t.Fatalf("%+v, %v", job, err)
	}
	if len(job.LogTail) != TailLines {
		t.Errorf("%d lines in the tail", len(job.LogTail))
	}
	// the same without the end: the unit is gone and said nothing
	if err := os.WriteFile(path, []byte(head+junk), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at(2), at(2)); err != nil {
		t.Fatal(err)
	}
	if job, _ := ReadJob(env, jName, UnitGone, at(1000)); job.State != JobInterrupted || job.Step != "packages" {
		t.Errorf("%+v", job)
	}
	// and a marker that is in the middle of the part that is not read is not read
	mid := head + strings.Repeat("| x\n", 10000) + logOf(jResult(jNonce, "ok", "done", "-", "-", 41)) + strings.Repeat("| y\n", 40000)
	if err := os.WriteFile(path, []byte(mid), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at(2), at(2)); err != nil {
		t.Fatal(err)
	}
	if job, _ := ReadJob(env, jName, UnitGone, at(1000)); job.State == JobSucceeded {
		t.Errorf("a result in the middle of a huge log: %+v", job)
	}
}

func TestTheCoreQueuesAJobByWritingTheFirstLineOfItsLog(t *testing.T) {
	env := jobBox(t)
	old := logOf(jQueued(jOther, 0), jStep(jOther, "packages", "begin", 1), jResult(jOther, "ok", "done", "-", "-", 2))
	path := writeJobLog(t, env, old)
	if err := Queue(env, jName, jNonce, at(100)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != QueuedLine(jName, jNonce, at(100)) {
		t.Fatalf("the log holds %q, %v", got, err)
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", info.Mode())
	}
	// the old job is gone from the log, and the new one is running
	if job, _ := ReadJob(env, jName, UnitGone, at(105)); job.State != JobRunning || job.StartedAt != jts(100) {
		t.Errorf("%+v", job)
	}
	// no file of the writing is left, and the lock is let go
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("%d entries in the folder of the logs", len(entries))
	}
	release, err := env.Host.Lock(env.Roots.lock())
	if err != nil {
		t.Fatalf("the lock is still held: %v", err)
	}
	release()
}

func TestTheCoreDoesNotTouchTheLogOfARunThatHoldsTheLock(t *testing.T) {
	env := jobBox(t)
	path := writeJobLog(t, env, logOf(jQueued(jOther, 0), jStep(jOther, "packages", "begin", 1)))
	before, _ := os.ReadFile(path)
	if err := makeDirs(env.Roots.state(), 0o755); err != nil {
		t.Fatal(err)
	}
	release, err := env.Host.Lock(env.Roots.lock())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := Queue(env, jName, jNonce, at(100)); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("Queue() = %v, want ErrLockHeld", err)
	}
	if err := Abandon(env, jName, jNonce, at(100), at(101), nil); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("Abandon() = %v, want ErrLockHeld", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("the log of the run that holds the lock was written: %q", after)
	}
}

func TestTheCoreQueuesOnlyWhatItCanName(t *testing.T) {
	env := jobBox(t)
	for _, c := range []struct{ name, nonce string }{{"Virt", jNonce}, {"", jNonce}, {"../x", jNonce}, {jName, ""}, {jName, "-"}, {jName, strings.ToUpper(jNonce)}, {jName, jNonce[:31]}} {
		if err := Queue(env, c.name, c.nonce, at(0)); err == nil {
			t.Errorf("Queue(%q, %q) queued a job", c.name, c.nonce)
		}
		if err := Abandon(env, c.name, c.nonce, at(0), at(1), nil); err == nil {
			t.Errorf("Abandon(%q, %q) wrote a log", c.name, c.nonce)
		}
	}
	if entries, _ := os.ReadDir(env.Roots.Prefix); len(entries) != 0 {
		t.Errorf("something was written for a job that is not one: %v", entries)
	}
}

func TestAJobThatCouldNotBeStartedIsAWholeRunInTheLog(t *testing.T) {
	env := jobBox(t)
	if err := Queue(env, jName, jNonce, at(100)); err != nil {
		t.Fatal(err)
	}
	said := []string{"Failed to connect to bus: \x1b[31mno\x00 such file https://user:secret@h.example/x"}
	if err := Abandon(env, jName, jNonce, at(100), at(102), said); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(env.Roots.logFile(jName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "\x1b") || strings.Contains(string(raw), "\x00") {
		t.Errorf("what systemd said is not text: %q", raw)
	}
	job, _ := ReadJob(env, jName, UnitGone, at(103))
	if job.State != JobFailed || job.Code != "start_failed" || job.Step != "" || job.Undo != "" || job.StartedAt != jts(100) || job.EndedAt != jts(102) {
		t.Fatalf("%+v", job)
	}
	for _, l := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n")[1:] {
		if !strings.HasPrefix(l, "| ") && !strings.HasPrefix(l, "failed") && !strings.HasPrefix(l, resultMarker) {
			t.Errorf("a line that is neither a sentence of the assistant nor a tool's: %q", l)
		}
	}
}

func TestTheQueuedLineIsTheGrammarOfTheMarkers(t *testing.T) {
	line := QueuedLine(jName, jNonce, time.Date(2026, 10, 18, 14, 0, 0, 0, time.FixedZone("x", 7200)))
	if line != "CASAOS_MODULE_QUEUED "+jNonce+" virt 2026-10-18T12:00:00Z\n" {
		t.Errorf("%q", line)
	}
}

func TestTheModulesThatHaveALogAreTheOnesTheFolderNames(t *testing.T) {
	env := jobBox(t)
	if names, err := JobLogs(env); err != nil || len(names) != 0 {
		t.Fatalf("no folder: %v, %v", names, err)
	}
	dir := filepath.Dir(env.Roots.logFile("x"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"module-virt.log", "module-a-b.log", "module-Virt.log", "module-.log", "module-virt.log.1", "casaos.log", "xmodule-virt.log", "module-virt.logx", "module-" + strings.Repeat("a", 33) + ".log"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	names, err := JobLogs(env)
	if err != nil || len(names) != 2 || !(names[0] == "a-b" && names[1] == "virt" || names[0] == "virt" && names[1] == "a-b") {
		t.Errorf("%v, %v", names, err)
	}
}

// unsafeHost is a host where the folder of the state is one that root may not keep files in.
type unsafeHost struct{ Host }

func (unsafeHost) SafeDir(string) error { return errors.New("writable by all") }

func TestNothingIsWrittenInAFolderThatIsNotSafe(t *testing.T) {
	env := jobBox(t)
	env.Host = unsafeHost{env.Host}
	if err := Queue(env, jName, jNonce, at(0)); err == nil || errors.Is(err, ErrLockHeld) {
		t.Errorf("Queue() = %v", err)
	}
	if err := Abandon(env, jName, jNonce, at(0), at(1), nil); err == nil || errors.Is(err, ErrLockHeld) {
		t.Errorf("Abandon() = %v", err)
	}
	if _, err := os.Stat(env.Roots.logFile(jName)); err == nil {
		t.Error("a log was written")
	}
}

// Of a log that is larger than what is scanned, the window of the end starts on a line, or one byte before it: the line that begins the window is whole, and is read.
func TestTheWindowOfTheEndOfAHugeLogKeepsTheLineThatBeginsIt(t *testing.T) {
	env := jobBox(t)
	original := logScanBytes
	logScanBytes = 1024
	t.Cleanup(func() { logScanBytes = original })
	result := jResult(jNonce, "ok", "done", "-", "-", 9) + "\n"
	filler := "| " + strings.Repeat("f", 100) + "\n"
	tail := result
	for len(tail)+len(filler) <= 512 {
		tail += filler
	}
	// pad the last line so that the window of the end (the last 512 bytes) begins exactly where the result does
	tail += "| " + strings.Repeat("g", 512-len(tail)-3) + "\n"
	if len(tail) != 512 {
		t.Fatalf("the tail is %d bytes", len(tail))
	}
	head := logOf(jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1)) + strings.Repeat("| "+strings.Repeat("h", 100)+"\n", 20)
	writeJobLog(t, env, head+tail)
	if job, err := ReadJob(env, jName, UnitGone, at(1000)); err != nil || job.State != JobSucceeded {
		t.Errorf("%+v, %v", job, err)
	}
}

// Of the lines of the end of a huge log, the first is never one that was cut: the tail of the state is made of whole lines.
func TestTheTailOfAHugeLogIsMadeOfWholeLines(t *testing.T) {
	env := jobBox(t)
	original := logScanBytes
	logScanBytes = 8 << 10
	t.Cleanup(func() { logScanBytes = original })
	var b strings.Builder
	b.WriteString(logOf(jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1)))
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "| line%05d %s\n", i, strings.Repeat("x", 1500))
	}
	writeJobLog(t, env, b.String())
	job, err := ReadJob(env, jName, UnitActive, at(5))
	if err != nil || len(job.LogTail) < 4 {
		t.Fatalf("%d lines, %v", len(job.LogTail), err)
	}
	for _, l := range job.LogTail {
		if !strings.HasPrefix(l, "| line") && !strings.HasPrefix(l, "CASAOS_MODULE_") {
			t.Errorf("a line that was cut: %q", l)
		}
	}
}

// What is read of a log is what was there when it was looked at: a run that goes on writing is not followed.
func TestOnlyWhatWasThereWhenTheLogWasLookedAtIsRead(t *testing.T) {
	env := jobBox(t)
	first := logOf(jQueued(jNonce, 0), jStep(jNonce, "packages", "begin", 1))
	path := writeJobLog(t, env, first+logOf(jResult(jNonce, "ok", "done", "-", "-", 2)))
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := NewReducer(jName, "")
	if err := scanLog(r, f, int64(len(first))); err != nil {
		t.Fatal(err)
	}
	if job := r.Job(UnitActive, at(3), time.Time{}); job.State != JobRunning {
		t.Errorf("%+v", job)
	}
}

func TestAJobThatCouldNotBeStartedSaysThreeLinesOfWhatSystemdSaidAtMost(t *testing.T) {
	env := jobBox(t)
	said := []string{"one", "two", "three", "four", "five"}
	if err := Abandon(env, jName, jNonce, at(0), at(1), said); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(env.Roots.logFile(jName))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(raw), "\n| "); got != 3 || strings.Contains(string(raw), "four") {
		t.Errorf("%d lines of systemd's: %q", got, raw)
	}
}
