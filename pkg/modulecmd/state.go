package modulecmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// tempName is the name a file is written under before it takes its place: in the same directory, so that the rename is atomic, short (the name of a file can be as long as a name can be,
// and a name made of it would not fit), and the same every time, so that a temporary file that a kill left behind is found again (and removed with the module) and never piles up.
func tempName(path string) string {
	sum := sha256.Sum256([]byte(filepath.Base(path)))
	return filepath.Join(filepath.Dir(path), ".cos-"+hex.EncodeToString(sum[:4]))
}

// afterClear is called after a temporary name that an earlier run left was taken away and before the file is made at it. It does nothing; a test puts there what somebody could put in that
// instant, to see that the file is made with O_EXCL and so never written through it.
var afterClear = func(path string) {}

// syncFile and syncDir make a file, and the entries of a directory, durable, as far as the system says. They are variables so that a test can see that they are called, and where.
var (
	syncFile = func(f *os.File) error { return f.Sync() }
	// a system that cannot is no reason to stop
	syncDir = func(dir string) {
		if f, err := os.Open(dir); err == nil {
			_ = f.Sync()
			_ = f.Close()
		}
	}
)

// createPrivate makes a file that was not there, for root alone: O_EXCL (what is there, a link included, is not opened) and O_NOFOLLOW, opened to read as well, 0600.
func createPrivate(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|noFollow, 0o600)
}

// writeFileAtomic puts data at path whole or not at all. A temporary file of an earlier run is replaced; a link at the temporary name is not written through.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	tmp := tempName(path)
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	afterClear(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollow, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := syncFile(f); err != nil {
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

// errUnsafe is a directory that root may not keep files in.
var errUnsafe = errors.New("a directory that is not safe")

// emptyDir removes what is in a directory and keeps the directory. A directory that is not there is made (0700); one that is there must be a directory and not a link, owned by the user the
// assistant runs as and writable by nobody else (Host.SafeDir), or nothing is removed, and the error is errUnsafe: what somebody else could have put in it, or made it point to, is not
// root's to remove.
func emptyDir(host Host, dir string) error { return emptyDirExcept(host, dir, nil) }

// emptyTmp is emptyDir for the area of the download, which keeps the backups of updates that were interrupted: what is in them is the only copy of the files an update replaced, and is
// for the next run of the module to put back (recoverUpdate) before it empties anything else.
func emptyTmp(host Host, dir string) error { return emptyDirExcept(host, dir, isBackupName) }

// isBackupName says whether a name in the area of the download is the folder of the backup of a module's update.
func isBackupName(name string) bool {
	rest, ok := strings.CutPrefix(name, "backup-")
	return ok && nameRe.MatchString(rest)
}

// emptyDirExcept is emptyDir but for the entries whose names keep says to keep (a directory that is not a link: whatever else has such a name goes with the rest).
func emptyDirExcept(host Host, dir string, keep func(name string) bool) error {
	switch _, err := os.Lstat(dir); {
	case errors.Is(err, fs.ErrNotExist) || isNotDir(err):
		return makeDirs(dir, 0o700)
	case err != nil:
		return err
	}
	if err := host.SafeDir(dir); err != nil {
		return fmt.Errorf("%w: %s: %v", errUnsafe, dir, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var first error
	for _, e := range entries {
		if keep != nil && keep(e.Name()) {
			if info, err := os.Lstat(filepath.Join(dir, e.Name())); err == nil && info.IsDir() {
				continue
			}
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil && first == nil {
			first = err
		}
	}
	return first
}
