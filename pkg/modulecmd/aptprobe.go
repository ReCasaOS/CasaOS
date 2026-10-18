package modulecmd

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// aptProbe is the three reads of osplan.ReadAptConfig, over the Roots of the box: the files of apt's configuration are read through it and through nothing else. The other methods of
// a Probe are not given (the embedded interface is nil), and the reader does not call them.
type aptProbe struct {
	osreadiness.Probe
	roots Roots
}

func (p aptProbe) ReadDir(ctx context.Context, path string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(p.roots.Path(path))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

func (p aptProbe) Lstat(ctx context.Context, path string) (osreadiness.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return osreadiness.FileInfo{}, err
	}
	info, err := os.Lstat(p.roots.Path(path))
	if err != nil {
		return osreadiness.FileInfo{}, err
	}
	return osreadiness.FileInfo{Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime()}, nil
}

func (p aptProbe) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(p.roots.Path(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, &fs.PathError{Op: "read", Path: path, Err: errors.New("not a regular file")}
	}
	data, err := io.ReadAll(io.LimitReader(f, osreadiness.MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > osreadiness.MaxFileBytes {
		return nil, osreadiness.ErrTooLarge
	}
	return data, nil
}

func isNotDir(err error) bool { return errors.Is(err, syscall.ENOTDIR) }

// isNotEmpty says that a directory was not deleted because something is in it (145 is what Windows says, which the tests of the portable host meet).
func isNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) || errors.Is(err, syscall.Errno(145))
}
