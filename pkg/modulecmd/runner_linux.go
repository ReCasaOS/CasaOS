//go:build linux

package modulecmd

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The commands of the assistant run with no shell, the standard input from /dev/null, an environment of their own and nothing of the process's, in a group of their own that is ended as
// a whole when the context is: asked to stop first (apt must be allowed to leave dpkg in a state it can go on from), and killed when it has not after the grace.
const killGrace = 20 * time.Second

type execRunner struct{ tools toolPaths }

// newRunner makes the runner of a box, and finds its programs now. extra are the names of other programs it may run, which the tests use.
func newRunner(roots Roots, extra ...string) Runner {
	return execRunner{tools: findTools(roots, extra...)}
}

// toolPaths are the programs the assistant runs, by name, each at the path it was found at: the first regular executable file of that name in the directories of the box, which are looked
// at in this order and not in the PATH of the process. They are found once, when the runner is made, which is before an archive is unpacked, and a program that an unpack puts in the same
// directory afterwards is not the one that is run (and an archive may not put one of those names in usr/bin at all, osmodule.ReservedPrograms).
type toolPaths struct {
	found map[string]string
	known map[string]bool // the programs the assistant may run, found or not
}

// findTools finds the programs of osmodule.ReservedPrograms, and the extra names that a test asks for.
func findTools(roots Roots, extra ...string) toolPaths {
	t := toolPaths{found: map[string]string{}, known: map[string]bool{}}
	for _, name := range append(slices.Clone(osmodule.ReservedPrograms), extra...) {
		t.known[name] = true
		for _, dir := range roots.searchDirs() {
			p := filepath.Join(dir, name)
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
				t.found[name] = p
				break
			}
		}
	}
	return t
}

// path is where a program is: the one the assistant found, an error for a program that it did not find, and another for a name that is not one of its programs.
func (t toolPaths) path(name string) (string, error) {
	if p, ok := t.found[name]; ok {
		return p, nil
	}
	if t.known[name] {
		return "", osreadiness.ErrToolMissing
	}
	return "", osreadiness.ErrNotAllowed
}

func (r execRunner) Run(ctx context.Context, c Cmd) (Output, error) {
	if len(c.Argv) == 0 {
		return Output{}, errors.New("no command")
	}
	exe, err := r.tools.path(c.Argv[0])
	if err != nil {
		return Output{}, err
	}
	return runProcess(ctx, exe, c, killGrace)
}

// boundedBuffer keeps the start of what is written to it, up to max bytes, and takes the rest without a word.
type boundedBuffer struct {
	mu        sync.Mutex
	max       int
	buf       []byte
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	if len(b.buf) >= b.max && len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

// groupGone says whether no process is left in the process group.
func groupGone(pgid int) bool { return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) }

// runProcess runs exe as c says. It ends with the status of the command, as an Output, or with an error when the command could not be run or was ended by the context.
//
// When the context ends, the whole group is asked to stop (SIGTERM) and is given the grace to leave: the leader may go at once and leave dpkg, its child, to finish what it is doing, and
// dpkg killed in the middle of it is a package manager left interrupted. At the end of the grace what is still there is killed, and so is whatever a command that ended on its own left
// behind (apt's methods). With no grace (0) the group is killed at once.
func runProcess(ctx context.Context, exe string, c Cmd, grace time.Duration) (Output, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.Command(exe, c.Argv[1:]...)
	cmd.Args[0] = c.Argv[0]
	cmd.Env = c.Env
	if cmd.Env == nil {
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "LANG=C"}
	}
	cmd.Dir = c.Dir
	if cmd.Dir == "" {
		cmd.Dir = "/"
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// once the leader has gone, a child that keeps the output open is waited for two seconds; the group is waited for below, on its own
	cmd.WaitDelay = 2 * time.Second
	var kept *boundedBuffer
	if c.Stdout != nil {
		cmd.Stdout, cmd.Stderr = c.Stdout, c.Stdout
	} else {
		kept = &boundedBuffer{max: osplan.MaxOutputBytes}
		cmd.Stdout, cmd.Stderr = kept, kept
	}
	err := cmd.Start()
	if err == nil {
		pid := cmd.Process.Pid
		waited := make(chan error, 1)
		go func() { waited <- cmd.Wait() }()
		select {
		case err = <-waited:
		case <-ctx.Done():
			asked := time.Now()
			sig := syscall.SIGTERM
			if grace == 0 {
				sig = syscall.SIGKILL
			}
			_ = syscall.Kill(-pid, sig)
			killer := time.AfterFunc(grace, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
			err = <-waited
			// ponytail: polling every 50 ms; a zombie that nobody reaps keeps the group "there" for the whole grace
			for time.Since(asked) < grace && !groupGone(pid) {
				time.Sleep(50 * time.Millisecond)
			}
			killer.Stop()
		}
		// whatever is left of the group is ended with the command: apt's methods are in it
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	var out Output
	if kept != nil {
		kept.mu.Lock()
		out.Text, out.Truncated = string(kept.buf), kept.truncated
		kept.mu.Unlock()
	}
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return out, ctx.Err()
	case err == nil:
		return out, nil
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		out.ExitCode = exit.ExitCode()
		return out, nil
	case errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil:
		// the command ended and a child held its output open: what the command said is its own status
		out.ExitCode = cmd.ProcessState.ExitCode()
		return out, nil
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return out, osreadiness.ErrToolMissing
	}
	return out, err
}
