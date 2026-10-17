//go:build linux

package modulecmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// linuxDest is the file system an archive is unpacked onto: the real one, under a prefix (empty on a box). A file is written under a temporary name in its directory, given exactly the
// permissions it is meant to have (the umask does not take part), and renamed into place; a link is made under the temporary name and renamed too. The temporary name is made with
// O_EXCL, so it is never written through a link.
type linuxDest struct{ prefix string }

func (d linuxDest) real(p string) string { return Roots{Prefix: d.prefix}.Path(p) }

func (d linuxDest) Lstat(p string) (fs.FileMode, error) {
	info, err := os.Lstat(d.real(p))
	if err != nil {
		return 0, err
	}
	return info.Mode(), nil
}

// MkdirAll makes the directories that are missing, the ones above the last with 0755 and the last with perm, whatever the umask is. A directory that is there is left as it is.
func (d linuxDest) MkdirAll(p string, perm fs.FileMode) error {
	var missing []string
	for at := d.real(p); ; at = parentOf(at) {
		info, err := os.Stat(at)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", at)
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		missing = append(missing, at)
		if parentOf(at) == at {
			return err
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		mode := fs.FileMode(0o755)
		if i == 0 {
			mode = perm
		}
		if err := os.Mkdir(missing[i], mode); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := os.Chmod(missing[i], mode); err != nil {
			return err
		}
	}
	return nil
}

func (d linuxDest) NewFile(p string, perm fs.FileMode) (osmodule.File, error) {
	real := d.real(p)
	tmp := tempName(real)
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return &linuxFile{f: f, tmp: tmp, real: real, perm: perm}, nil
}

func (d linuxDest) Symlink(target, p string) error {
	real := d.real(p)
	tmp := tempName(real)
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, real); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

type linuxFile struct {
	f         *os.File
	tmp, real string
	perm      fs.FileMode
}

func (w *linuxFile) Write(b []byte) (int, error) { return w.f.Write(b) }

func (w *linuxFile) Commit() error {
	if err := w.f.Chmod(w.perm); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		return err
	}
	if err := w.f.Close(); err != nil {
		return err
	}
	return os.Rename(w.tmp, w.real)
}

func (w *linuxFile) Abort() {
	_ = w.f.Close()
	_ = os.Remove(w.tmp)
}

func parentOf(p string) string {
	for i := len(p) - 1; i > 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "/"
}
