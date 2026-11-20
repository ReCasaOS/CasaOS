package file

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"syscall"
)

// Copying for an account (CasaOS#168), inside os.Root: the source is read below the folder
// it is in, the destination written below its own, by the kernel's resolution relative to
// each root. Whatever is renamed while the copy runs (a folder swapped for a link to /etc),
// nothing is read or written outside the two folders.

// copyDepthMax stops a copy that would go on into what it makes (a destination moved
// inside its source while it runs).
const copyDepthMax = 64

// CopyInRoots copies srcRel of src into the folder dstDir of dst, under its own name, as
// CopyDirContext does. A link is copied as what it points to when that stays inside src,
// skipped otherwise (ErrLinkOutside, said at the end, as a device or a pipe is,
// ErrNotRegular). style "skip" leaves a file already there; anything else overwrites it.
func CopyInRoots(ctx context.Context, src *os.Root, srcRel string, dst *os.Root, dstDir, style string) error {
	return copyRootEntry(ctx, src, srcRel, dst, path.Join(dstDir, path.Base(srcRel)), style, 0)
}

func copyRootEntry(ctx context.Context, src *os.Root, s string, dst *os.Root, d, style string, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > copyDepthMax {
		return fmt.Errorf("%s: deeper than %d folders: it would copy into itself", s, copyDepthMax)
	}
	info, err := src.Lstat(s)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		// os.Root follows a link only while it stays below the root
		if info, err = src.Stat(s); err != nil {
			return fmt.Errorf("%s: %w", s, ErrLinkOutside)
		}
	}
	switch {
	case info.IsDir():
		if err := dst.Mkdir(d, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		dir, err := src.Open(s)
		if err != nil {
			return err
		}
		names, err := dir.Readdirnames(-1)
		dir.Close()
		if err != nil {
			return err
		}
		var skipped error
		for _, name := range names {
			err := copyRootEntry(ctx, src, path.Join(s, name), dst, path.Join(d, name), style, depth+1)
			if errors.Is(err, ErrLinkOutside) || errors.Is(err, ErrNotRegular) {
				skipped = err
				continue
			}
			if err != nil {
				return err
			}
		}
		return skipped
	case info.Mode().IsRegular():
		if style == "skip" {
			if _, err := dst.Lstat(d); err == nil {
				return nil
			}
		}
		return copyRootFile(ctx, src, s, dst, d, info.Mode().Perm())
	default:
		return fmt.Errorf("%s: %w", s, ErrNotRegular)
	}
}

func copyRootFile(ctx context.Context, src *os.Root, s string, dst *os.Root, d string, mode fs.FileMode) error {
	// O_NONBLOCK: a pipe put in the file's place since it was looked at does not hold the copy
	in, err := src.OpenFile(s, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	if info, err := in.Stat(); err != nil {
		return err
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: %w", s, ErrNotRegular)
	}
	out, err := dst.OpenFile(d, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NONBLOCK, mode)
	if err != nil {
		return err
	}
	if info, err := out.Stat(); err != nil || !info.Mode().IsRegular() {
		out.Close()
		if err == nil {
			err = fmt.Errorf("%s: %w", d, ErrNotRegular)
		}
		return err
	}
	_, err = io.Copy(out, ctxReader{ctx, in})
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

// ctxReader stops a copy once its context is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
