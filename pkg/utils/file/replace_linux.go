package file

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// ReplaceFile is ReplaceFileFrom for data.
func ReplaceFile(path string, data []byte) error {
	return ReplaceFileFrom(path, bytes.NewReader(data))
}

// ReplaceFileFrom writes what it reads from r over an existing file as WriteFileAtomic does, for a file someone edits or sends again: through a link it
// writes the link's target, the new file keeps the old one's mode and owner, and a file with hard links is written in place.
func ReplaceFileFrom(path string, r io.Reader) error {
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
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, r) // the new content first, then the old tail cut: never an empty file on the way
		if err == nil {
			err = f.Truncate(n)
		}
		if err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		return err
	}
	return writeFileAtomic(path, nil, func(tmp *os.File) error {
		if err := tmp.Chown(int(st.Uid), int(st.Gid)); err != nil {
			return err
		}
		if err := tmp.Chmod(info.Mode()); err != nil { // after Chown, which clears setuid and setgid
			return err
		}
		_, err := io.Copy(tmp, r)
		return err
	})
}
