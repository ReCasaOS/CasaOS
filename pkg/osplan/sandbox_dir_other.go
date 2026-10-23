//go:build !linux

package osplan

import "io/fs"

// ownerAndDevice: nothing is known of who owns a file where plans are not made, so no directory is taken for the sandbox's.
func ownerAndDevice(fs.FileInfo) (uid, dev uint64, ok bool) { return 0, 0, false }
