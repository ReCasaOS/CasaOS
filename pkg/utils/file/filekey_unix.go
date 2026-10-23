//go:build unix

package file

import (
	"os"
	"syscall"
)

// fileKey is the device and the inode of a file: what two names of one folder (a link, a bind mount) share.
func fileKey(info os.FileInfo) ([2]uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return [2]uint64{}, false
	}
	return [2]uint64{uint64(st.Dev), uint64(st.Ino)}, true
}
