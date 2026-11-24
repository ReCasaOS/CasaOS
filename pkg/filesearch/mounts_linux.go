package filesearch

import (
	"os"
	"strings"
)

// remoteTypes are the file systems a walk does not enter on its own: a network one can hang
// a read for as long as its server is gone, and an overlay is a container's view of layers
// read elsewhere already.
var remoteTypes = map[string]bool{
	"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true, "9p": true,
	"ceph": true, "glusterfs": true, "davfs": true, "sshfs": true, "overlay": true,
}

// mountsToSkip is the mount points of remoteTypes and of FUSE remotes (rclone, sshfs), read
// from /proc/self/mountinfo once per search: no call that could block on such a mount.
func mountsToSkip() map[string]bool {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	return parseMountinfo(string(data))
}

func parseMountinfo(data string) map[string]bool {
	skip := map[string]bool{}
	for _, line := range strings.Split(data, "\n") {
		// <id> <parent> <major:minor> <root> <mount point> <options> [optional...] - <type> <source> <super options>
		fields := strings.Fields(line)
		dash := -1
		for i, f := range fields {
			if f == "-" {
				dash = i
				break
			}
		}
		if len(fields) < 5 || dash < 0 || dash+1 >= len(fields) {
			continue
		}
		fsType := fields[dash+1]
		if remoteTypes[fsType] || strings.HasPrefix(fsType, "fuse.") && fsType != "fuse.mergerfs" {
			skip[unescapeMount(fields[4])] = true
		}
	}
	return skip
}

// unescapeMount undoes mountinfo's octal escapes of spaces, tabs, new lines and backslashes.
func unescapeMount(p string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(p)
}
