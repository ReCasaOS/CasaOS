//go:build linux

package modulecmd

import (
	"os"
	"syscall"
)

// ownerOf is the user and the group of a file, as the file system says them.
func ownerOf(info os.FileInfo) (uid, gid int, ok bool) {
	st, is := info.Sys().(*syscall.Stat_t)
	if !is {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

// chownBack gives a file (or a link, not what it points to) its user and group back, as far as the user the assistant runs as may: root, in production.
func chownBack(path string, uid, gid int) { _ = os.Lchown(path, uid, gid) }
