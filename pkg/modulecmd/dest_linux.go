//go:build linux

package modulecmd

import (
	"errors"
	"io/fs"
	"os"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"golang.org/x/sys/unix"
)

// linuxDest is the file system an archive is unpacked onto: the real one, under a prefix (empty on a box). A file is written under a temporary name in its directory, given exactly the
// permissions it is meant to have (the umask does not take part), and put in place by one rename: a rename that does not replace what is there (renameat2 with RENAME_NOREPLACE) unless the
// file is the module's own, so that a file that appeared since the checking is never written over. A link is made under the temporary name and put in place the same way. The temporary
// name is made with O_EXCL, so it is never written through a link.
type linuxDest struct{ prefix string }

func (d linuxDest) real(p string) string { return Roots{Prefix: d.prefix}.Path(p) }

func (d linuxDest) Lstat(p string) (fs.FileMode, error) {
	info, err := os.Lstat(d.real(p))
	if err != nil {
		return 0, err
	}
	return info.Mode(), nil
}

// Mkdir makes one directory, whose parent is there, with exactly perm whatever the umask is. A directory that is there is an error that is fs.ErrExist.
func (d linuxDest) Mkdir(p string, perm fs.FileMode) error {
	real := d.real(p)
	if err := os.Mkdir(real, perm); err != nil {
		return err
	}
	return os.Chmod(real, perm)
}

// renameNoReplace is the rename that fails with an error that is fs.ErrExist when anything is at the new name: a variable, so that a test can take the system that does not have it.
var renameNoReplace = func(oldpath, newpath string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_NOREPLACE)
}

// publish puts the file or the link at tmp in place at final, and takes tmp away. When replace is false nothing that is at final is replaced, whatever happens at the same time: the rename
// that does not replace, or, on a system or a file system that has none, a hard link (which fails when anything is at the new name, a link that points nowhere included, and does not
// follow a link) and then the removal of the temporary name.
func publish(tmp, final string, replace bool) error {
	if replace {
		return os.Rename(tmp, final)
	}
	err := renameNoReplace(tmp, final)
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.ENOTSUP) {
		return err
	}
	if err := os.Link(tmp, final); err != nil {
		return err
	}
	return os.Remove(tmp)
}

func (d linuxDest) NewFile(p string, perm fs.FileMode, replace bool) (osmodule.File, error) {
	real := d.real(p)
	tmp := tempName(real)
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return &linuxFile{f: f, tmp: tmp, real: real, perm: perm, replace: replace}, nil
}

func (d linuxDest) Symlink(target, p string, replace bool) error {
	real := d.real(p)
	tmp := tempName(real)
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := publish(tmp, real, replace); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

type linuxFile struct {
	f         *os.File
	tmp, real string
	perm      fs.FileMode
	replace   bool
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
	return publish(w.tmp, w.real, w.replace)
}

func (w *linuxFile) Abort() {
	_ = w.f.Close()
	_ = os.Remove(w.tmp)
}
