//go:build linux

package modulecmd

import "golang.org/x/sys/unix"

// noFollow makes the open of a path fail when the last element of it is a link: root does not write through a link that somebody else could have put there.
const noFollow = unix.O_NOFOLLOW

// nonBlock makes the open of a fifo for writing fail when nobody reads it, in place of waiting.
const nonBlock = unix.O_NONBLOCK
