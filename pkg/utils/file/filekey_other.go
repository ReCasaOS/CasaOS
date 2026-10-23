//go:build !unix

package file

import "os"

// fileKey is not known here: CopyDir cannot tell a folder it made from another one.
func fileKey(os.FileInfo) ([2]uint64, bool) { return [2]uint64{}, false }
