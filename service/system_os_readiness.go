package service

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
	"github.com/ReCasaOS/CasaOS/pkg/singleflight"
)

// GetSystemOSReadiness is the report of whether this box could be upgraded to the next release of
// its operating system, and why not: it reads, and changes nothing (no write, no lock, no network,
// no service started or stopped). It takes no lock of the package updater: it waits for nothing that
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
}

var _ osreadiness.Probe = osReadinessProbe{}

func (osReadinessProbe) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// a file that is not a regular one (a FIFO, a device) is not read: opening one can wait for ever
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &fs.PathError{Op: "read", Path: path, Err: errors.New("not a regular file")}
	}
	file, err := os.Open(path)
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

// Run runs a command the way the package updater runs its own (LC_ALL=C: the answers are in
// English), and says how it ended. A command that ran and failed is an exit status, and not an
// error; one that is not installed is osreadiness.ErrToolMissing; one that was stopped by the end
// of its time is the context's error.
func (p osReadinessProbe) Run(ctx context.Context, argv ...string) (osreadiness.Output, error) {
	if len(argv) == 0 {
		return osreadiness.Output{}, errors.New("no command")
	}
	out, err := p.updater.command(ctx, argv[0], argv[1:]...)
	result := osreadiness.Output{Text: string(out)}
	if len(out) > osreadiness.MaxOutputBytes {
		result.Text, result.Truncated = string(out[:osreadiness.MaxOutputBytes]), true
	}
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
// none is the caller's.
func (p osReadinessProbe) MaintenanceBusy(ctx context.Context) (string, bool) {
	return p.updater.maintenanceBusy(ctx, "")
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
