//go:build linux

package service

import "os"

// syncFolder makes what was written or renamed in a folder survive a power cut: the name of a file is in the folder, and not only its content on the disk.
func syncFolder(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
