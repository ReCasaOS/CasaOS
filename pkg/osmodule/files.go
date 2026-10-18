package osmodule

import (
	"bytes"
	"strconv"
	"strings"
)

// The list of the files of a module, /var/lib/casaos/modules/<name>.files: every file that the install put down, one absolute path per line, and what the remove deletes, and nothing else.
// The list is read back from the disk when it is used, so it is read as data: a line that is not a path the install could have written is a list that is not taken, whole, and not a
// line that is skipped.

// AllowedDirs are the directories, relative to the root of the system, that a module may put files in: the unpack refuses any other destination, and the list holds no path outside them.
// Not all of what is in them: a module puts down its program in usr/bin, its unit in a directory of units, and the sample of its configuration in etc/casaos, each directly in the
// directory (placeOK). Nothing of /var/lib/casaos or /usr/share/casaos is in the list on purpose: the core keeps its state there (alerts, telemetry, the choices of the automatic updates,
// the catalogue, the lists of the modules), and a module has no file in them that it should be allowed to write over.
var AllowedDirs = []string{"usr/bin", "usr/lib/systemd/system", "lib/systemd/system", "etc/casaos"}

// sampleSuffix ends the name of the only file a module puts in etc/casaos: the sample of its configuration, which the administrator copies and edits.
const sampleSuffix = ".sample"

// the bounds of a list and of a path
const (
	// MaxEntries is the most entries of an archive, and so the most paths of a list.
	MaxEntries = 5000
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

// placeOK says whether a module may put something of this kind at a relative, safe path that is under an allowed directory, when unitOK says whether a name is the unit it may ship. Only a
// DIRECT child of the directory: there is no subdirectory to fill, in any of them (a drop-in of a unit of the core is `<unit>.d/`, the units a target wants are in `<target>.wants/`, and a
// link that is named like one would be read as one). And by directory: in usr/bin a program, a file or a link to one beside it; in a directory of units the unit of the module, a file, and no
// other name; in etc/casaos a file named like a sample.
func placeOK(rel string, k kind, unitOK func(name string) bool) bool {
	dir := allowedDirOf(rel)
	if dir == "" {
		return false
	}
	name := rel[len(dir)+1:]
	if strings.Contains(name, "/") {
		return false
	}
	switch {
	case isUnitDir(dir):
		return k == kFile && unitOK(name)
	case dir == "etc/casaos":
		return k == kFile && strings.HasSuffix(name, sampleSuffix)
	}
	return k != kDir
}

// anyUnit is the name of a unit as a manifest may give it, for the list of the files, which is read without the manifest at hand.
func anyUnit(name string) bool { return unitRe.MatchString(name) }

// ValidFilePath says whether p is a path that may be in the list of a module: absolute, already clean, made of safe names, and a place where an unpack puts a file (any unit name stands for
// the unit: which one it is, the unpack knows).
func ValidFilePath(p string) bool {
	rel, ok := strings.CutPrefix(p, "/")
	return ok && safeRel(rel) && underAllowed(rel) && placeOK(rel, kFile, anyUnit)
}

// ValidDirPath says whether p is a directory that the unpack of a module may have made, and so that a remove may try to delete: an allowed directory or one of those above it, which are
// the only ones it makes.
func ValidDirPath(p string) bool {
	rel, ok := strings.CutPrefix(p, "/")
	return ok && aboveAllowed(rel)
}

// ParseFiles reads the content of a list: one valid path to a line, each line ended by a new line, none twice, at least one and at most MaxEntries. Anything else is an error and no list;
// an empty list is one too, since a list that was cut short at its start reads as one and must not read as a module that has no files.
func ParseFiles(raw []byte) ([]string, error) { return parsePaths("files", raw, ValidFilePath) }

// FormatFiles is the content of the list of the paths, in the order given: the paths are checked as ParseFiles would, so that what is written is what can be read.
func FormatFiles(paths []string) ([]byte, error) { return formatPaths("files", paths, ValidFilePath) }

// ParseDirs and FormatDirs are the same for the list of the directories that the install of a module made (<name>.dirs), which the remove deletes, if they are empty, after its files.
func ParseDirs(raw []byte) ([]string, error) { return parsePaths("dirs", raw, ValidDirPath) }

func FormatDirs(paths []string) ([]byte, error) { return formatPaths("dirs", paths, ValidDirPath) }

func parsePaths(field string, raw []byte, valid func(string) bool) ([]string, error) {
	if len(raw) > MaxFilesBytes {
		return nil, invalid(field, ReasonTooLarge)
	}
	if len(raw) == 0 {
		return nil, invalid(field, ReasonMissing)
	}
	text, ok := strings.CutSuffix(string(raw), "\n")
	if !ok {
		return nil, invalid(field, ReasonUnreadable)
	}
	lines := strings.Split(text, "\n")
	if len(lines) > MaxEntries {
		return nil, invalid(field, ReasonTooMany)
	}
	seen := make(map[string]bool, len(lines))
	for i, line := range lines {
		if !valid(line) || seen[line] {
			return nil, invalid(field+":"+strconv.Itoa(i+1), ReasonValue)
		}
		seen[line] = true
	}
	return lines, nil
}

func formatPaths(field string, paths []string, valid func(string) bool) ([]byte, error) {
	if len(paths) == 0 {
		return nil, invalid(field, ReasonMissing)
	}
	if len(paths) > MaxEntries {
		return nil, invalid(field, ReasonTooMany)
	}
	var b bytes.Buffer
	seen := make(map[string]bool, len(paths))
	for i, p := range paths {
		if !valid(p) || seen[p] {
			return nil, invalid(field+":"+strconv.Itoa(i+1), ReasonValue)
		}
		seen[p] = true
		b.WriteString(p)
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}
