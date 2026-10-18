package modulecmd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
)

// The core starts a job (the unit that runs install or remove) after it has written the first line of the job's log, and writes a last line itself when the unit could not be started. Both are
// written while holding the lock of the assistant, that a run holds from before it opens its log to after it ends: a log that a run is writing is not touched, and a run that is started
// by hand while the core writes is refused as busy. The log is replaced whole and atomically (the first line is all that is in it), so that it is never empty and never half a line.

// errBadJob is a name, a nonce or a kind that a job cannot have.
var errBadJob = errors.New("not a module name, not a nonce or not a kind of job")

// ErrLogChanged is the error of Abandon for a log that is no longer the first line of the job: a unit started, and the log holds what it has written.
var ErrLogChanged = errors.New("the log is no longer the queued line of the job")

// withLock runs write while the lock of the assistant is held and the folder of the logs is there. ErrLockHeld is the error of a lock that a run holds.
func withLock(env Env, write func(env Env) error) error {
	env = env.withDefaults()
	if err := makeDirs(env.Roots.state(), 0o755); err != nil {
		return err
	}
	if err := env.Host.SafeDir(env.Roots.state()); err != nil {
		return fmt.Errorf("%w: %s: %v", errUnsafe, env.Roots.state(), err)
	}
	release, err := env.Host.Lock(env.Roots.lock())
	if err != nil {
		return err
	}
	defer release()
	if err := makeDirs(env.Roots.logs(), 0o755); err != nil {
		return err
	}
	return write(env)
}

// Queue replaces the log of the module called name with the first line of the job whose nonce this is, and whose kind (KindInstall or KindRemove) is said in it: the log is the job's from
// here on, and a unit that is started next keeps that line, replaces the rest and carries on with the same nonce. It does not wait for the lock (ErrLockHeld when a run has it).
func Queue(env Env, name, nonce, kind string, at time.Time) error {
	if !nameRe.MatchString(name) || !dockerpkg.ValidNonce(nonce) || !ValidKind(kind) {
		return errBadJob
	}
	return withLock(env, func(env Env) error {
		return writeFileAtomic(env.Roots.logFile(name), []byte(QueuedLine(name, nonce, kind, at)), 0o644)
	})
}

// Abandon makes the log of a job whose unit could not be started a whole run: the queued line (queuedAt and kind are what Queue was given), what failed, what systemd said (said, as text, behind
// "| " like the output of a tool) and a RESULT line that says failed, start_failed, with nothing undone because nothing was done. It does not wait for the lock. It writes only while the log
// is exactly the queued line of this job, or is not there: a unit that did start has replaced the log with its own lines, and they are not for the core to overwrite (ErrLogChanged).
func Abandon(env Env, name, nonce, kind string, queuedAt, at time.Time, said []string) error {
	if !nameRe.MatchString(name) || !dockerpkg.ValidNonce(nonce) || !ValidKind(kind) {
		return errBadJob
	}
	var b strings.Builder
	b.WriteString(QueuedLine(name, nonce, kind, queuedAt))
	b.WriteString("failed: " + CodeStartFailed + "\n")
	for i, line := range said {
		if i == 3 {
			break
		}
		if line = printable(osplan.SanitizeLine(line), 200); line != "" {
			b.WriteString("| " + line + "\n")
		}
	}
	fmt.Fprintf(&b, "%s %s %s failed - %s - %s\n", resultMarker, nonce, name, CodeStartFailed, at.UTC().Format(time.RFC3339))
	queued := QueuedLine(name, nonce, kind, queuedAt)
	return withLock(env, func(env Env) error {
		if err := logIsOnly(env.Roots.logFile(name), queued); err != nil {
			return err
		}
		return writeFileAtomic(env.Roots.logFile(name), []byte(b.String()), 0o644)
	})
}

// logIsOnly says, as a nil, that the log at path holds exactly want, or is not there (nothing of anybody's to keep); ErrLogChanged when it holds anything else. A log that is not a
// regular file, or that cannot be read, is an error of its own.
func logIsOnly(path, want string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|noFollow|nonBlock, 0)
	switch {
	case errors.Is(err, fs.ErrNotExist) || isNotDir(err):
		return nil
	case err != nil:
		return err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("the log is not a regular file: %v", err)
	}
	got, err := io.ReadAll(io.LimitReader(f, int64(len(want))+1))
	if err != nil {
		return err
	}
	if string(got) != want {
		return ErrLogChanged
	}
	return nil
}
