package file

import (
	"os"
	"path/filepath"
	"syscall"
)

// ReplaceFile is WriteFileAtomic for an existing file someone edits: through a link it writes the link's target, and the new file keeps the old one's mode and owner.
func ReplaceFile(path string, data []byte) error {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, func(tmp *os.File) error {
		st := info.Sys().(*syscall.Stat_t)
		if err := tmp.Chown(int(st.Uid), int(st.Gid)); err != nil {
			return err
		}
		return tmp.Chmod(info.Mode()) // after Chown, which clears setuid and setgid
	})
}
