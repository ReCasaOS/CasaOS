package service

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/singleflight"
)

// GetSystemOSReadiness is the report of whether this box could be upgraded to the next release of
// its operating system, and why not: it reads (no write under /etc or /var, no lock, no network, no
// service started or stopped; osreadiness's doc says the little a read leaves). It takes no lock of the package updater: it waits for nothing that
// an update holds, and an update does not wait for it. It takes up to twenty seconds.
//
// Requests that come while a report is being made are answered with that report: the checks run
// commands (apt-get check among them), and a page that is asked again and again must not make the
// box run them again and again.
func (s *systemService) GetSystemOSReadiness() osreadiness.Answer {
	probe := osReadinessProbe{updater: s.systemPackageUpdater()}
	answer, _, _ := osReadinessFlight.Do("readiness", func() (osreadiness.Answer, error) {
		// not the context of the request that started it: the others wait for it too
		return osreadiness.NewAnswer(runOSReadiness(context.Background(), probe, osreadiness.Options{})), nil
	})
	return answer
}

var (
	osReadinessFlight singleflight.Group[osreadiness.Answer]
	// runOSReadiness is osreadiness.Run, for a test to put its own in its place.
	runOSReadiness = osreadiness.Run
)

// osReadinessProbe is the Probe of this box: the files and the directories as the os package says,
// the commands through the package updater's own (in English, as the updater runs them), and the
// questions only the core can answer (is a package manager at work, has an app an operation) through
// what the updater already knows. It has nothing that writes.
type osReadinessProbe struct {
	updater *systemPackageUpdater
	// command runs a command, in English, and gives what it printed (at most osreadiness.MaxOutputBytes
	// of it, and whether it printed more); nil is boundedCommand. It is what Run and the questions of
	// MaintenanceBusy to systemd go through, and the one thing a test puts its own in the place of.
	command func(ctx context.Context, name string, args ...string) (text []byte, truncated bool, err error)
}

var _ osreadiness.Probe = osReadinessProbe{}

// openRegular opens a file to read it: a file that is not a regular one (a FIFO, a device) is not opened, since
// opening one can wait for ever.
func openRegular(ctx context.Context, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &fs.PathError{Op: "read", Path: path, Err: errors.New("not a regular file")}
	}
	return os.Open(path)
}

func (osReadinessProbe) ReadFile(ctx context.Context, path string) ([]byte, error) {
	file, err := openRegular(ctx, path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// /proc and /sys say a size of 0: the bound is on what is read
	data, err := io.ReadAll(io.LimitReader(file, osreadiness.MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > osreadiness.MaxFileBytes {
		return nil, osreadiness.ErrTooLarge
	}
	return data, nil
}

// ReadHead is the start of a file: the bytes asked for, and no more of it is read.
func (osReadinessProbe) ReadHead(ctx context.Context, path string, max int) ([]byte, error) {
	file, err := openRegular(ctx, path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, int64(min(max, osreadiness.MaxFileBytes))))
}

func (osReadinessProbe) ReadDir(ctx context.Context, path string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func fileInfo(info fs.FileInfo, err error) (osreadiness.FileInfo, error) {
	if err != nil {
		return osreadiness.FileInfo{}, err
	}
	return osreadiness.FileInfo{Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime()}, nil
}

func (osReadinessProbe) Stat(ctx context.Context, path string) (osreadiness.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return osreadiness.FileInfo{}, err
	}
	return fileInfo(os.Stat(path))
}

func (osReadinessProbe) Lstat(ctx context.Context, path string) (osreadiness.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return osreadiness.FileInfo{}, err
	}
	return fileInfo(os.Lstat(path))
}

func (osReadinessProbe) Readlink(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return os.Readlink(path)
}

// readinessWaitDelay is how long Run waits for the output of a command whose process has ended (or
// was killed at the end of its time) and whose child still holds it open.
const readinessWaitDelay = 2 * time.Second

// boundedOutput is where the standard output and the error output of a command are written: it keeps
// the start, up to max bytes and the one that says there was more, and throws the rest away while
// it takes every write whole, so that the process is neither held up nor told to stop and the memory
// it costs is the bound, however much the tool prints.
type boundedOutput struct {
	mu  sync.Mutex
	max int
	buf []byte
	// ended is set when the output was read to its end: the pipe was closed by every process that held it.
	ended bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max + 1 - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// ReadFrom is what io.Copy calls, and os/exec copies the output of a command with io.Copy: it reads to the
// end of the pipe and says so (complete). A pipe that a child still holds open does not end, and the copy
// is cut by the wait delay with an error: os/exec reports that (exec.ErrWaitDelay) only for a process that
// ended well, and for one that failed it reports the exit status, with nothing to say that words are lost.
func (b *boundedOutput) ReadFrom(r io.Reader) (int64, error) {
	var total int64
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
			total += int64(n)
		}
		switch {
		case err == io.EOF:
			b.mu.Lock()
			b.ended = true
			b.mu.Unlock()
			return total, nil
		case err != nil:
			return total, err
		}
	}
}

// complete says whether the output was read to its end.
func (b *boundedOutput) complete() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ended
}

// result is what was kept, cut to the bound, and whether the command printed more than that.
func (b *boundedOutput) result() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buf) > b.max {
		return b.buf[:b.max], true
	}
	return b.buf, false
}

// boundedCommand runs a command the way the package updater runs its own (LC_ALL=C: the answers are in
// English) with what the report asks of one that must not cost the box anything: its output is kept
// only up to osreadiness.MaxOutputBytes as it is read, and not whole before it is cut; its process is
// killed when ctx ends; and a child that holds the output open after the process has gone is waited
// for readinessWaitDelay, and not for as long as it lives.
func boundedCommand(ctx context.Context, name string, args ...string) ([]byte, bool, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return runBounded(ctx, cmd, osreadiness.MaxOutputBytes)
}

// runBounded runs cmd, a command made with exec.CommandContext(ctx, ...), with its output kept only up to max bytes
// (see boundedCommand, which is it for the report; the sandbox of the plan of an upgrade is the other that has it).
func runBounded(ctx context.Context, cmd *exec.Cmd, max int) ([]byte, bool, error) {
	cmd.WaitDelay = readinessWaitDelay
	out := &boundedOutput{max: max}
	// one writer for both: they are one stream, in the order the process wrote them
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	text, truncated := out.result()
	// A process that ran, and whose output was not read to its end, left a child that holds it: what it
	// printed is not known to be whole, whatever its exit status says (see ReadFrom).
	var exit *exec.ExitError
	if cmd.ProcessState != nil && !out.complete() && ctx.Err() == nil && (err == nil || errors.As(err, &exit)) {
		err = exec.ErrWaitDelay
	}
	return text, truncated, err
}

func (p osReadinessProbe) run(ctx context.Context, name string, args ...string) ([]byte, bool, error) {
	if p.command != nil {
		return p.command(ctx, name, args...)
	}
	return boundedCommand(ctx, name, args...)
}

// Run runs a command, in English, and says how it ended. A command that ran and failed is an exit
// status, and not an error; one that is not installed is osreadiness.ErrToolMissing; one that was
// stopped by the end of its time is the context's error; one whose output a child still holds is an
// error (exec.ErrWaitDelay) whatever its exit status was, since what it printed is not known to be whole.
func (p osReadinessProbe) Run(ctx context.Context, argv ...string) (osreadiness.Output, error) {
	if len(argv) == 0 {
		return osreadiness.Output{}, errors.New("no command")
	}
	out, truncated, err := p.run(ctx, argv[0], argv[1:]...)
	return finishRun(ctx, out, truncated, err)
}

// finishRun says how a command that was run ended, as the Probe says it (see Run).
func finishRun(ctx context.Context, out []byte, truncated bool, err error) (osreadiness.Output, error) {
	result := osreadiness.Output{Text: string(out), Truncated: truncated}
	if err == nil {
		return result, nil
	}
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return result, ctx.Err()
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		result.ExitCode = exit.ExitCode()
		return result, nil
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return result, osreadiness.ErrToolMissing
	}
	return result, err
}

func (p osReadinessProbe) FreeBytes(_ context.Context, path string) (uint64, error) {
	if p.updater.freeBytes == nil {
		return 0, errors.New("the room left cannot be read here")
	}
	return p.updater.freeBytes(path)
}

// MaintenanceBusy is the updater's own question: one of the units that change the box's packages
// or its ReCasaOS release runs, or a package manager holds dpkg's lock. Every unit is asked about,
// none is the caller's. Where the updater's own answer is "not busy" for a lock it cannot see, this
// one is an error: the report refuses for a lock it could not look at.
func (p osReadinessProbe) MaintenanceBusy(ctx context.Context) (string, bool, error) {
	return p.updater.maintenanceState(ctx, "", func(ctx context.Context, name string, args ...string) ([]byte, error) {
		out, _, err := p.run(ctx, name, args...)
		return out, err
	})
}

// AppOperations are the apps AppManagement says have an operation in progress. The question is
// read from what SetAppOperations stored, without the updater's lock: a report does not wait for
// an update that holds it.
func (p osReadinessProbe) AppOperations(ctx context.Context) ([]string, error) {
	list := p.updater.appOps.Load()
	if list == nil {
		return nil, errors.New("the box cannot ask AppManagement")
	}
	return appOperationNames(ctx, *list)
}

func (p osReadinessProbe) Now() time.Time { return p.updater.now() }

func (p osReadinessProbe) EUID() int { return p.updater.getEUID() }

func (osReadinessProbe) Arch() string { return runtime.GOARCH }
