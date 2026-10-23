//go:build linux

package osplan

import (
	"io/fs"
	"syscall"
)

// ownerAndDevice are the user that owns a file and the device it is on, as the system says them; ok is false when it does not.
func ownerAndDevice(info fs.FileInfo) (uid, dev uint64, ok bool) {
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, false
	}
	return uint64(st.Uid), uint64(st.Dev), true //nolint:unconvert // the width of Dev is the platform's
}
