package osmodule

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
)

// The list of the files of a module, /var/lib/casaos/modules/<name>.files: every file that the install put down, one absolute path per line, and what the remove deletes, and nothing else.
// The list is read back from the disk when it is used, so it is read as data: a line that is not a path the install could have written is a list that is not taken, whole, and not a
// line that is skipped.

// AllowedDirs are the directories, relative to the root of the system, that a module may put files in: the unpack refuses any other destination, and the list holds no path outside them.
var AllowedDirs = []string{"usr/bin", "usr/lib/systemd/system", "lib/systemd/system", "etc/casaos", "usr/share/casaos", "var/lib/casaos"}

// the bounds of a list and of a path
const (
	// MaxEntries is the most entries of an archive, and so the most paths of a list.
	MaxEntries = 5000
	// MaxDepth is the most names a path has below the allowed directory it is in, MaxDirs the most directories one unpack makes.
	MaxDepth = 8
	MaxDirs  = 200
	// MaxPathBytes is the longest path of a file of a module (relative to the root), MaxElementBytes the longest name in it.
	MaxPathBytes    = 1024
	MaxElementBytes = 255
	// MaxFilesBytes is the most of a list that is read: the longest path of every entry.
	MaxFilesBytes = MaxEntries * (MaxPathBytes + 2)
)

// safeElement says whether a name of a path is one that a module may use: the characters of a file name of a program, a unit or a configuration, never a space, a control, a character
// outside ASCII (two names that differ only by the way a character is written would be two files for one reader and one for another), a backslash or a newline (the list has one path
// to a line), and never only dots.
func safeElement(el string) bool {
	if el == "" || len(el) > MaxElementBytes || strings.Trim(el, ".") == "" {
		return false
	}
	for i := 0; i < len(el); i++ {
		switch c := el[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-', c == '+', c == '@', c == ':':
		default:
			return false
		}
	}
	return true
}

// safeRel says whether a relative path (no slash at the start, none at the end) is made of safe names.
func safeRel(rel string) bool {
	if rel == "" || len(rel) > MaxPathBytes {
		return false
	}
	for _, el := range strings.Split(rel, "/") {
		if !safeElement(el) {
			return false
		}
	}
	return true
}

// underAllowed says whether a relative, safe path (so, one with no slash at its end) is inside one of the allowed directories, and so not one of them.
func underAllowed(rel string) bool {
	for _, dir := range AllowedDirs {
		if strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	return false
}

// the core's own state, which lives inside the allowed directories and which a module never writes: the manifests and the lists of the modules, the catalogue, the state of the OS upgrade
// and its switch. Each is the name itself and everything under it, as a file, a link or a directory.
var reservedTrees = []string{"etc/casaos/modules", "usr/share/casaos/modules", "var/lib/casaos/modules", "var/lib/casaos/os-upgrade", "etc/casaos/os-upgrade.beta"}

// reserved says whether a relative path is the core's own state. The case is folded: a file system may ignore it.
func reserved(rel string) bool {
	low := strings.ToLower(rel)
	return slices.ContainsFunc(reservedTrees, func(tree string) bool { return low == tree || strings.HasPrefix(low, tree+"/") })
}

// allowedDirOf is the allowed directory a path (already checked to be under one) is in.
func allowedDirOf(rel string) string {
	for _, dir := range AllowedDirs {
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return dir
		}
	}
	return ""
}

func isUnitDir(dir string) bool {
	return dir == "usr/lib/systemd/system" || dir == "lib/systemd/system"
}

// placeOK says whether a module may put something of this kind at a relative, safe path that is under an allowed directory: not deeper than MaxDepth, not the core's own state, and in a
// directory of units only directly in it (a unit directory has no subdirectory a module may fill: a drop-in of a unit of the core is `<unit>.d/`, the units a target wants are in
// `<target>.wants/`, and a link that is named like one would be read as one).
func placeOK(rel string, k kind) bool {
	dir := allowedDirOf(rel)
	if dir == "" || rel == dir || reserved(rel) {
		return false
	}
	sub := rel[len(dir)+1:]
	if strings.Count(sub, "/")+1 > MaxDepth {
		return false
	}
	if isUnitDir(dir) {
		return k != kDir && !strings.Contains(sub, "/") && !(k == kLink && (strings.HasSuffix(sub, ".d") || strings.HasSuffix(sub, ".wants") || strings.HasSuffix(sub, ".requires")))
	}
	return true
}

// ValidFilePath says whether p is a path that may be in the list of a module: absolute, already clean, made of safe names, and a place where an unpack puts a file.
func ValidFilePath(p string) bool {
	rel, ok := strings.CutPrefix(p, "/")
	return ok && safeRel(rel) && underAllowed(rel) && placeOK(rel, kFile)
}

// ParseFiles reads the content of a list: one valid path to a line, each line ended by a new line, none twice, at least one and at most MaxEntries. Anything else is an error and no list;
// an empty list is one too, since a list that was cut short at its start reads as one and must not read as a module that has no files.
func ParseFiles(raw []byte) ([]string, error) {
	if len(raw) > MaxFilesBytes {
		return nil, invalid("files", ReasonTooLarge)
	}
	if len(raw) == 0 {
		return nil, invalid("files", ReasonMissing)
	}
	text, ok := strings.CutSuffix(string(raw), "\n")
	if !ok {
		return nil, invalid("files", ReasonUnreadable)
	}
	lines := strings.Split(text, "\n")
	if len(lines) > MaxEntries {
		return nil, invalid("files", ReasonTooMany)
	}
	seen := make(map[string]bool, len(lines))
	for i, line := range lines {
		if !ValidFilePath(line) || seen[line] {
			return nil, invalid("files:"+strconv.Itoa(i+1), ReasonValue)
		}
		seen[line] = true
	}
	return lines, nil
}

// FormatFiles is the content of the list of the paths, in the order given: the paths are checked as ParseFiles would, so that what is written is what can be read.
func FormatFiles(paths []string) ([]byte, error) {
	if len(paths) == 0 {
		return nil, invalid("files", ReasonMissing)
	}
	if len(paths) > MaxEntries {
		return nil, invalid("files", ReasonTooMany)
	}
	var b bytes.Buffer
	seen := make(map[string]bool, len(paths))
	for i, p := range paths {
		if !ValidFilePath(p) || seen[p] {
			return nil, invalid("files:"+strconv.Itoa(i+1), ReasonValue)
		}
		seen[p] = true
		b.WriteString(p)
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}
