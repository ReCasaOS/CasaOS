package modulecmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// The output of install, update and remove: lines on the standard output and in the log of the module (/var/log/casaos/module-<name>.log), the same lines in both (a run that is refused before it
// has the lock, and so before it may touch the log, says its lines on the standard output only). A line the program says about itself is a marker, a result or a sentence; what the
// commands it runs print goes in the log only, and every line of it starts with "| ", so that nothing a package prints can look like a marker.
//
//	CASAOS_MODULE_STEP <nonce|-> <name> <step> <begin|done|failed> <time>
//	CASAOS_MODULE_RESULT <nonce|-> <name> <ok|refused|failed> <step|-> <code|-> <undo> <time>
//
// The core, when it starts a job, writes a first line of its own in the log before the program runs (Queue): CASAOS_MODULE_QUEUED <nonce> <name> <install|remove|update> <time>. The program does not write
// it; when it opens the log it keeps it if it is the line of this very run (see openLog), and it is the only place that says whether the log is an install's, a remove's or an update's.
//
// This is the whole grammar of what the program says, and the only place it is written down. The nonce is the one the core passes in CASAOS_MODULE_NONCE, "-" when there is none. A step is a word of lower case
// letters and underscores: for an install packages, download, verify, unpack, units, manifest, start, route and done; for a remove stop, files, manifest, reload and done; for an update
// packages, download, verify, unpack, backup, stop, swap, units, start, route and done (the rollback of an update, and the recovery of one that was ended, are the steps restore, units and start
// behind "rollback_" and "recover_"); and the steps of
// the rollback of an install, which are those of a remove behind "rollback_". Every "begin" of a step is followed by its "done" or its "failed", and a step that failed is never "done".
//
// A run says its outcome in exactly one RESULT line, and every run that ends says it: a run that is refused (exit 2, nothing changed) has no STEP line and its RESULT line is its last; a
// run that failed (exit 3) has the failed STEP line before the sentences that say what was undone and what was kept, and its RESULT line is its last, said once the rollback is over, so that
// a run that is ended in the middle of it has none (and is, for whoever reads the log, a run that was interrupted); undo is "rolled_back" or "rollback_incomplete" for an install or an update that
// failed, and "-" for the rest. A run that succeeded (exit 0) may say sentences after its RESULT line (what is left on the box, for a remove).

const (
	stepMarker   = "CASAOS_MODULE_STEP"
	resultMarker = "CASAOS_MODULE_RESULT"
	// maxToolOutput is the most of the output of the commands that the log keeps.
	maxToolOutput = 1 << 20
)

type job struct {
	env   Env
	name  string
	nonce string
	log   *os.File
	mu    sync.Mutex
	tool  int64 // the bytes of the output of the commands written so far
	// openStep is the step that has begun and has not ended: a panic ends it, as a failed one
	openStep string
}

func newJob(env Env, name string) *job {
	nonce := env.Getenv("CASAOS_MODULE_NONCE")
	if !dockerpkg.ValidNonce(nonce) {
		nonce = "-"
	}
	return &job{env: env, name: name, nonce: nonce}
}

// maxQueuedLine is how much of the log is looked at for the first line that the core wrote: that line is 116 bytes at the longest.
const maxQueuedLine = 256

// openLog starts the log of this run of the verb called kind (KindInstall or KindRemove): the log of an earlier run is replaced, so that a log is one run. The exception is the first line,
// when it is the queued line of this very run (the module's, with the nonce this run was given, saying the kind that this run is, whole, as the core writes it): it stays where it is,
// and the rest is cut away behind it. The markers of the run are the same for an install and a remove, and the queued line is the one place where the log says which of them it is; it is
// kept and not written again, so that the log is never without it.
func (j *job) openLog(kind string) error {
	dir := j.env.Roots.logs()
	if err := makeDirs(dir, 0o755); err != nil {
		return err
	}
	// not through a link, not a fifo that waits for a reader, and only a regular file once it is open: what is checked is what is written
	f, err := os.OpenFile(j.env.Roots.logFile(j.name), os.O_RDWR|os.O_CREATE|noFollow|nonBlock, 0o644)
	if err != nil {
		return err
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return fmt.Errorf("%s is not a regular file", j.env.Roots.logFile(j.name))
	}
	keep := int64(j.queuedLength(f, kind))
	if err := f.Truncate(keep); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Seek(keep, io.SeekStart); err != nil {
		_ = f.Close()
		return err
	}
	j.log = f
	return f.Chmod(0o644)
}

// queuedLength is the length of the first line of the log f (with its new line) when that is the queued line of this run, and 0 when it is not, or when it cannot be read.
func (j *job) queuedLength(f *os.File, kind string) int {
	buf := make([]byte, maxQueuedLine)
	n, _ := io.ReadFull(f, buf)
	end := bytes.IndexByte(buf[:n], '\n')
	if end < 0 {
		return 0
	}
	m, ok := parseMarker(string(buf[:end]), j.name)
	if !ok || m.kind != queuedMarker || m.nonce != j.nonce || m.jobKind != kind {
		return 0
	}
	return end + 1
}

func (j *job) close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.log != nil {
		_ = j.log.Close()
		j.log = nil
	}
}

func (j *job) now() string { return j.env.Clock.Now().UTC().Format(time.RFC3339) }

// say writes a line to the standard output and to the log.
func (j *job) say(format string, a ...any) {
	line := fmt.Sprintf(format, a...) + "\n"
	j.mu.Lock()
	defer j.mu.Unlock()
	_, _ = io.WriteString(j.env.Stdout, line)
	if j.log != nil {
		_, _ = io.WriteString(j.log, line)
	}
}

func (j *job) marker(step, phase string) {
	j.openStep = ""
	if phase == "begin" {
		j.openStep = step
	}
	j.say("%s %s %s %s %s %s", stepMarker, j.nonce, j.name, step, phase, j.now())
}

func (j *job) result(kind, step, code, undo string) {
	if step == "" {
		step = "-"
	}
	if code == "" {
		code = "-"
	}
	j.say("%s %s %s %s %s %s %s %s", resultMarker, j.nonce, j.name, kind, step, code, undo, j.now())
}

// toolWriter takes what the commands print into the log, line by line behind "| ", cut at maxToolOutput, with nothing but printable ASCII.
func (j *job) toolWriter() io.Writer { return &lineWriter{j: j} }

type lineWriter struct {
	j   *job
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.flush(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > 4096 { // a line that does not end
		w.flush(w.buf)
		w.buf = nil
	}
	return len(p), nil
}

func (w *lineWriter) flush(line []byte) {
	j := w.j
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.log == nil || j.tool > maxToolOutput {
		return
	}
	text := "| " + printable(string(line), 400) + "\n"
	j.tool += int64(len(text))
	if j.tool > maxToolOutput {
		text = "| [the output is cut here]\n"
	}
	_, _ = io.WriteString(j.log, text)
}

// printable is a text as it may be put in a line of the log or on the screen: printable ASCII, cut.
func printable(s string, max int) string {
	b := make([]byte, 0, min(len(s), max))
	for i := 0; i < len(s) && len(b) < max; i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			c = '?'
		}
		b = append(b, c)
	}
	return string(b)
}

// tailWriter keeps the last bytes written to it, to classify a failure by what a command said at its end.
type tailWriter struct {
	io.Writer
	tail []byte
}

const tailBytes = 8 << 10

func (t *tailWriter) Write(p []byte) (int, error) {
	t.tail = append(t.tail, p...)
	if len(t.tail) > tailBytes {
		t.tail = t.tail[len(t.tail)-tailBytes:]
	}
	return t.Writer.Write(p)
}

// panicked writes a panic and its stack in the log (behind "| ", like the output of a command), for whoever looks for what went wrong.
func (j *job) panicked(r any) {
	_, _ = fmt.Fprintf(j.toolWriter(), "panic: %v\n%s", r, debug.Stack())
}

// recoverRun, deferred, turns a panic of a run into a failure: the sentence, the result line and the exit status 3, which is the status of a run that failed (the status that Go gives a
// panic, 2, says that nothing was changed).
func (j *job) recoverRun(code *int, undo func() string) {
	if r := recover(); r != nil {
		j.panicked(r)
		step := j.failOpen()
		if step == "-" {
			j.say("failed: %s", CodeInternal)
		} else {
			j.say("failed at step %s: %s", step, CodeInternal)
		}
		j.result("failed", step, CodeInternal, undo())
		*code = ExitFailed
	}
}

// failOpen ends the step that was begun, if there is one, as a failed one, and gives its name ("-" when there is none): a step that has begun has its end.
func (j *job) failOpen() string {
	step := j.openStep
	if step == "" {
		return "-"
	}
	j.marker(step, "failed")
	return step
}
