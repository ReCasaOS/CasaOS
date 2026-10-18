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

// The output of install and remove: lines on the standard output and in the log of the module (/var/log/casaos/module-<name>.log), the same lines in both. A line the program says
// about itself is a marker, a result or a sentence; what the commands it runs print goes in the log only, and every line of it starts with "| ", so that nothing a package prints can
// look like a marker.
//
//	CASAOS_MODULE_STEP <nonce|-> <name> <step> <begin|done|failed> <time>
//	CASAOS_MODULE_RESULT <nonce|-> <name> <ok|refused|failed> <step|-> <code|-> <time>
//
// The nonce is the one the core passes in CASAOS_MODULE_NONCE, "-" when there is none. A run says its outcome in exactly one RESULT line, and sentences of the program follow it at most
// (what was rolled back, and for an install that failed the last line, kept_packages); a refused run (exit 2) has no STEP line, and a failed one (exit 3) has the failed STEP line before
// it.

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
}

func newJob(env Env, name string) *job {
	nonce := env.Getenv("CASAOS_MODULE_NONCE")
	if !dockerpkg.ValidNonce(nonce) {
		nonce = "-"
	}
	return &job{env: env, name: name, nonce: nonce}
}

// openLog starts the log of this run: the log of an earlier run is replaced, so that a log is one run.
func (j *job) openLog() error {
	dir := j.env.Roots.logs()
	if err := makeDirs(dir, 0o755); err != nil {
		return err
	}
	path := j.env.Roots.logFile(j.name)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	j.log = f
	return f.Chmod(0o644)
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
	j.say("%s %s %s %s %s %s", stepMarker, j.nonce, j.name, step, phase, j.now())
}

func (j *job) result(kind, step, code string) {
	if step == "" {
		step = "-"
	}
	if code == "" {
		code = "-"
	}
	j.say("%s %s %s %s %s %s %s", resultMarker, j.nonce, j.name, kind, step, code, j.now())
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
func (j *job) recoverRun(code *int) {
	if r := recover(); r != nil {
		j.panicked(r)
		j.say("failed: %s", CodeInternal)
		j.result("failed", "-", CodeInternal)
		*code = ExitFailed
	}
}
