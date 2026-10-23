package file

import (
	"os"
	"path/filepath"
	"syscall"
)

// ReplaceFile is WriteFileAtomic for an existing file someone edits: through a link it writes the link's target, the new file keeps the old one's mode and
// owner, and a file with hard links is written in place.
func ReplaceFile(path string, data []byte) error {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Nlink > 1 { // a rename would leave the file's other names on the old content: in place, at the cost of the old-or-new guarantee
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		return err
	}
	return writeFileAtomic(path, data, func(tmp *os.File) error {
		if err := tmp.Chown(int(st.Uid), int(st.Gid)); err != nil {
			return err
		}
		return tmp.Chmod(info.Mode()) // after Chown, which clears setuid and setgid
	})
}
