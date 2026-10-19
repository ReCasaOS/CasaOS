//go:build !linux

package modulecmd

import "os"

func ownerOf(os.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }

func chownBack(string, int, int) {}
