package modulecmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// tempName is the name a file is written under before it takes its place: in the same directory, so that the rename is atomic, short (the name of a file can be as long as a name can be,
// and a name made of it would not fit), and the same every time, so that a temporary file that a kill left behind is found again (and removed with the module) and never piles up.
func tempName(path string) string {
	sum := sha256.Sum256([]byte(filepath.Base(path)))
	return filepath.Join(filepath.Dir(path), ".cos-"+hex.EncodeToString(sum[:4]))
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

// syncDir makes the entries of a directory durable, as far as the system says; a system that cannot is no reason to stop.
func syncDir(dir string) {
	if f, err := os.Open(dir); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
}
