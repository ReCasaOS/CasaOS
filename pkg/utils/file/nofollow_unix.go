//go:build unix

package file

import "syscall"

// NoFollow is the open flag that refuses a symlink as the last name (ELOOP).
const NoFollow = syscall.O_NOFOLLOW
