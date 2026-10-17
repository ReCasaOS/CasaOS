package modulecmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// tempName is the name a file is written under before it takes its place: in the same directory, so that the rename is atomic, and the same every time, so that a temporary file that a
// kill left behind is found again (and removed with the module) and never piles up.
func tempName(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".casaos-tmp")
}

// writeFileAtomic puts data at path whole or not at all. A temporary file of an earlier run is replaced; a link at the temporary name is not written through.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	tmp := tempName(path)
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// makeDirs makes the directories of a path that are missing, each with exactly perm whatever the umask is. A directory that is there is left as it is.
func makeDirs(path string, perm fs.FileMode) error {
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", path)
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if parent := filepath.Dir(path); parent != path {
		if err := makeDirs(parent, perm); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, perm); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return os.Chmod(path, perm)
}

// emptyDir removes what is in a directory and keeps the directory. A directory that is not there is made (0700).
func emptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return makeDirs(dir, 0o700)
	}
	if err != nil {
		return err
	}
	var first error
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// protectedPaths are the paths of the core's own state and of the assistant's: whatever the list of a module says or an archive holds, none of them is written or deleted by the unpack
// or the remove of a module.
var protectedPaths = []string{"/var/lib/casaos/modules/", "/etc/casaos/modules/", "/var/lib/casaos/os-upgrade/", "/etc/casaos/os-upgrade.beta"}

func protected(abs string) bool {
	for _, p := range protectedPaths {
		if strings.HasPrefix(abs, p) {
			return true
		}
	}
	return false
}

// A journal is the list of the files of a module, kept as the unpack goes: a path is in the list before the file is put at it, so that a run that is killed half way leaves a list
// that names everything that may have been written, and remove deletes exactly that.
type journal struct {
	path string
	f    *os.File // opened by the first path, so that no empty list is ever on the disk
	have map[string]bool
}

// openJournal opens the list for appending, with what it already holds (the list of an earlier run that was killed).
func openJournal(path string) (*journal, []string, error) {
	existing, err := readFilesList(path)
	if err != nil {
		return nil, nil, err
	}
	j := &journal{path: path, have: map[string]bool{}}
	for _, p := range existing {
		j.have[p] = true
	}
	return j, existing, nil
}

func (j *journal) add(abs string) error {
	if j.have[abs] {
		return nil
	}
	if !osmodule.ValidFilePath(abs) || protected(abs) || len(j.have) >= osmodule.MaxEntries {
		return fmt.Errorf("%q is not a path the list of files can take", abs)
	}
	if j.f == nil {
		f, err := os.OpenFile(j.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
		if err != nil {
			return err
		}
		j.f = f
		if err := f.Chmod(0o644); err != nil {
			return err
		}
	}
	if _, err := j.f.WriteString(abs + "\n"); err != nil {
		return err
	}
	j.have[abs] = true
	return nil
}

func (j *journal) close() error {
	if j.f == nil {
		return nil
	}
	if err := j.f.Sync(); err != nil {
		_ = j.f.Close()
		return err
	}
	return j.f.Close()
}

// journalDest is a destination that writes the path of every file and link to the journal before it makes it.
type journalDest struct {
	osmodule.Destination
	j *journal
}

func (d journalDest) NewFile(path string, perm fs.FileMode) (osmodule.File, error) {
	if err := d.j.add(path); err != nil {
		return nil, err
	}
	return d.Destination.NewFile(path, perm)
}

func (d journalDest) Symlink(target, path string) error {
	if err := d.j.add(path); err != nil {
		return err
	}
	return d.Destination.Symlink(target, path)
}
