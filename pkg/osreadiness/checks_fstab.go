package osreadiness

import (
	"context"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The checks of /etc/fstab: the list of what the machine mounts when it starts. A machine that has
// not been restarted for months has never tried it, and an entry that cannot be mounted, and does
// not say nofail, stops the start in emergency mode, on a machine that may have no screen.
func init() {
	register(
		Check{Code: "fstab_unresolved", Group: GroupFilesystem, Title: "Every mount at start has its device", Run: checkFstabUnresolved},
		Check{Code: "fstab_network", Group: GroupFilesystem, Title: "The mounts at start are in order", Run: checkFstabNetwork},
	)
}

const fstabPath = "/etc/fstab"

// A fstabEntry is a line of fstab(5): what to mount, where, what it is, with which options.
type fstabEntry struct {
	Spec, File, Type string
	Options          []string
}

// fstabUnescape is a field of fstab as the mount command reads it: a blank, a tab, a newline or a
// backslash in it is written as three octal digits after a backslash.
func fstabUnescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				out.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

// parseFstab reads fstab: a line that is empty, a comment, or has fewer than the three fields a
// mount needs is no entry. The options are "defaults" when the line has none.
func parseFstab(text string) []fstabEntry {
	var entries []fstabEntry
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		options := "defaults"
		if len(fields) >= 4 {
			options = fields[3]
		}
		entries = append(entries, fstabEntry{
			Spec: fstabUnescape(fields[0]), File: fstabUnescape(fields[1]), Type: fields[2], Options: strings.Split(options, ","),
		})
	}
	return entries
}

func (e fstabEntry) has(option string) bool { return slices.Contains(e.Options, option) }

// mountedAtStart says whether the start waits for the entry, and fails if it cannot have it: it is
// not one that is mounted later (noauto, the first access of an automount), not one that may fail
// (nofail), and not a swap, whose absence does not stop anything.
func (e fstabEntry) mountedAtStart() bool {
	return e.Type != "swap" && !e.has("noauto") && !e.has("nofail") && !e.has("x-systemd.automount")
}

// fstabTags are the ways to name a device other than by its path, and where udev lists them.
var fstabTags = map[string]string{
	"UUID": "/dev/disk/by-uuid", "PARTUUID": "/dev/disk/by-partuuid", "LABEL": "/dev/disk/by-label", "PARTLABEL": "/dev/disk/by-partlabel",
}

var idValue = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// udevDecode is a name of /dev/disk/by-label as the label it is: udev writes a character it does
// not keep as \xNN.
func udevDecode(name string) string {
	if !strings.Contains(name, `\x`) {
		return name
	}
	var out strings.Builder
	for i := 0; i < len(name); i++ {
		if name[i] == '\\' && i+3 < len(name) && name[i+1] == 'x' {
			if n, err := strconv.ParseUint(name[i+2:i+4], 16, 8); err == nil {
				out.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		out.WriteByte(name[i])
	}
	return out.String()
}

// sourceStatus says whether the device an entry names is there. known is false for a source that is
// not a device of this machine's (a network share, tmpfs, a bind): nothing is said of it.
func sourceStatus(ctx context.Context, p Probe, spec string) (known, there bool, err error) {
	if strings.HasPrefix(spec, "/dev/") {
		// a path that goes back up, or has a NUL in it, is no device's
		if strings.Contains(spec, "..") || strings.ContainsRune(spec, 0) {
			return false, false, nil
		}
		_, err := p.Stat(ctx, spec)
		if isNotExist(err) {
			return true, false, nil
		}
		return true, err == nil, err
	}
	tag, value, ok := strings.Cut(spec, "=")
	dir, tagged := fstabTags[tag]
	if !ok || !tagged {
		return false, false, nil
	}
	value = strings.Trim(value, `"'`)
	switch tag {
	case "UUID", "PARTUUID":
		if tag == "PARTUUID" {
			value = strings.ToLower(value)
		}
		// an id that has no business in a file name is not one that any device has
		if !idValue.MatchString(value) {
			return true, false, nil
		}
		_, err := p.Stat(ctx, dir+"/"+value)
		if isNotExist(err) {
			return true, false, nil
		}
		return true, err == nil, err
	}
	names, err := p.ReadDir(ctx, dir)
	if isNotExist(err) {
		return true, false, nil
	}
	if err != nil {
		return true, false, err
	}
	for _, name := range names {
		if udevDecode(name) == value {
			return true, true, nil
		}
	}
	return true, false, nil
}

var reportedPath = regexp.MustCompile(`^/[A-Za-z0-9._/-]{0,127}$`)

// userDirectories are the directories that have a directory of each user's in them.
var userDirectories = []string{"/home/", "/media/", "/run/media/"}

// maskedPath is a mount point as the report says it: the name of a user, which a path in their
// directory has, is left out. "" for a path that is not one the report may show.
func maskedPath(mountPoint string) string {
	clean := path.Clean(mountPoint)
	if !reportedPath.MatchString(clean) {
		return ""
	}
	for _, prefix := range userDirectories {
		if rest, ok := strings.CutPrefix(clean, prefix); ok {
			_, after, found := strings.Cut(rest, "/")
			if !found {
				return prefix + "_"
			}
			return prefix + "_/" + after
		}
	}
	return clean
}

// mountPoints are the mount points of some entries, as the report says them, once each, sorted.
func mountPoints(entries []fstabEntry) []string {
	points := []string{}
	for _, e := range entries {
		if point := maskedPath(e.File); point != "" && !slices.Contains(points, point) {
			points = append(points, point)
		}
	}
	slices.Sort(points)
	return points
}

func checkFstabUnresolved(ctx context.Context, p Probe) Result {
	data, found, err := readText(ctx, p, fstabPath)
	if err != nil {
		return unknown(err)
	}
	if !found {
		return pass()
	}
	var missing []fstabEntry
	for _, e := range parseFstab(data) {
		if !e.mountedAtStart() {
			continue
		}
		known, there, err := sourceStatus(ctx, p, e.Spec)
		if err != nil {
			return unknown(err)
		}
		if known && !there {
			missing = append(missing, e)
		}
	}
	if len(missing) == 0 {
		return pass()
	}
	return Result{
		Severity: Refuse, Title: "A mount at start has no device",
		Detail: "A line of /etc/fstab names a disk that is not there, and does not say nofail. At its next start, which the upgrade ends with, the machine stops in emergency mode and waits for someone at its console. Fix the line, or add nofail to it.",
		Data:   D{"mounts": mountPoints(missing), "count": len(missing)},
	}
}

// networkFilesystems are the types of mounts that need the network to be there.
var networkFilesystems = []string{
	"nfs", "nfs4", "cifs", "smb3", "smbfs", "sshfs", "fuse.sshfs", "glusterfs", "fuse.glusterfs", "ceph", "davfs", "9p",
}

// checkFstabNetwork says which mounts at start are of a network and do not say _netdev or nofail:
// the start waits for the network for them, and stops in emergency mode when it is late.
//
// It reads the fstab as it is written, and runs no command. It used to ask "findmnt --verify" too,
// for what it finds wrong with the file; that is not a read: libblkid resolves the UUID= and LABEL=
// of every line through the devices (it probes them, and a disk that sleeps wakes) and may rewrite
// its cache in /run/blkid. A line whose device is gone is fstab_unresolved's, which looks at
// /dev/disk/by-* and nothing else; a line that cannot be parsed is not an entry here, as it is not
// one for mount. The data it had of findmnt (verify_errors, verify_warnings) went with it.
func checkFstabNetwork(ctx context.Context, p Probe) Result {
	data, found, err := readText(ctx, p, fstabPath)
	if err != nil {
		return unknown(err)
	}
	if !found {
		return pass()
	}
	var network []fstabEntry
	for _, e := range parseFstab(data) {
		if slices.Contains(networkFilesystems, e.Type) && !e.has("_netdev") && e.mountedAtStart() {
			network = append(network, e)
		}
	}
	if len(network) == 0 {
		return pass()
	}
	return Result{
		Severity: Warn, Title: "The mounts at start have things to look at",
		Detail: "Network shares (NFS, CIFS) that are mounted at start and do not say _netdev or nofail make the start wait for the network, and stop in emergency mode when it is late.",
		Data:   D{"mounts": mountPoints(network), "count": len(network)}, Action: ActionNone,
	}
}
