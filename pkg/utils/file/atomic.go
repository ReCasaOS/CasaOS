package file

import (
	"io"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to a temporary file next to path, syncs it to
// disk and renames it over path: after a power cut (common on single-board
// boxes) the file holds the old content or the new one, never an empty one.
// os.CreateTemp creates it 0600, whatever the old file's mode: it is for root's
// own state files.
func WriteFileAtomic(path string, data []byte) error {
	return writeFileAtomic(path, data, nil)
}

// WriteFileAtomicFrom is WriteFileAtomic for the content read from r, the new file made perm.
func WriteFileAtomicFrom(path string, r io.Reader, perm os.FileMode) error {
	return writeFileAtomic(path, nil, func(tmp *os.File) error {
		if err := tmp.Chmod(perm); err != nil {
			return err
		}
		_, err := io.Copy(tmp, r)
		return err
	})
}

// writeFileAtomic is WriteFileAtomic, prepare (when given) run on the temporary file first.
func writeFileAtomic(path string, data []byte, prepare func(*os.File) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), tempPattern)
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // nothing left to remove once renamed
	if prepare != nil {
		err = prepare(tmp)
	}
	if err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
