package service

import (
	"os"
	"os/user"
	"strconv"
)

// RestrictShareToUser hands the shared directory to the account allowed to mount
// it.
//
// Without this the directory stays root-owned, while the Samba session runs as
// that account: the share would authenticate correctly and then refuse every
// write, which reads as a broken feature rather than a permissions problem.
//
// path must already have been through ValidateSharePath.
func RestrictShareToUser(path, username string) error {
	account, err := user.Lookup(username)
	if err != nil {
		return err
	}

	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}

	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}

	if err := os.Chown(path, uid, gid); err != nil {
		return err
	}

	return os.Chmod(path, 0o770)
}

// ReleaseShareFromUser gives a directory back to root when its share goes away.
//
// Leaving it owned by a share account would keep that account in control of the
// folder long after the share, and the account itself, are gone.
func ReleaseShareFromUser(path string) error {
	if _, err := ValidateSharePath(path); err != nil {
		return err
	}

	if err := os.Chown(path, 0, 0); err != nil {
		return err
	}

	return os.Chmod(path, 0o755)
}
