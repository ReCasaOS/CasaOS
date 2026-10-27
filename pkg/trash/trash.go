// Package trash keeps what the dashboard's Files deletes for thirty days, in
// the trash of the disk it was on. The layout is freedesktop.org's
// (.Trash-<uid>/files and .Trash-<uid>/info at the disk's mount point): moving
// there is a rename, even for a large folder, and Linux file managers see the
// same trash.
package trash

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Retention is how long an item stays in the trash before Purge removes it.
const Retention = 30 * 24 * time.Hour

var (
	// ErrUnavailable: the item cannot go to the trash (a read-only disk, a
	// trash folder that is not ours, already in the trash); only a permanent
	// delete removes it.
	ErrUnavailable = errors.New("cannot be moved to the trash")
	// ErrNotFound: no such item in that trash.
	ErrNotFound = errors.New("not in the trash")
	// ErrOutsideDisk: a trash entry says it came from another disk; it is not
	// restored there.
	ErrOutsideDisk = errors.New("the trash entry points outside its disk")
)

// Item is one entry of a trash.
type Item struct {
	Trash     string    `json:"trash"` // the mount point that holds the trash
	Name      string    `json:"name"`  // its name in the trash
	Path      string    `json:"path"`  // where it was deleted from
	DeletedAt time.Time `json:"deleted_at"`
	IsDir     bool      `json:"is_dir"`
	Size      int64     `json:"size"`
}

// Allowed reports whether the item that was at path may be listed, restored
// or deleted; nil allows everything.
type Allowed func(path string) bool

const dateLayout = "2006-01-02T15:04:05"

func dirName() string { return fmt.Sprintf(".Trash-%d", os.Getuid()) }

// infoText is the .trashinfo of what was at relative (to the mount point),
// deleted at t.
func infoText(relative string, t time.Time) []byte {
	segments := strings.Split(relative, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return []byte("[Trash Info]\nPath=" + strings.Join(segments, "/") + "\nDeletionDate=" + t.Format(dateLayout) + "\n")
}

// parseInfo reads a .trashinfo: the path as written (relative or absolute)
// and the deletion date, zero when it cannot be read.
func parseInfo(data []byte) (string, time.Time, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	header, path := false, ""
	var deleted time.Time
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case line == "[Trash Info]":
			header = true
		case strings.HasPrefix(line, "Path=") && header:
			p, err := url.PathUnescape(strings.TrimPrefix(line, "Path="))
			if err != nil {
				return "", time.Time{}, err
			}
			path = p
		case strings.HasPrefix(line, "DeletionDate=") && header:
			deleted, _ = time.ParseInLocation(dateLayout, strings.TrimPrefix(line, "DeletionDate="), time.Local)
		}
	}
	if !header || path == "" {
		return "", time.Time{}, errors.New("not a trash info file")
	}
	return path, deleted, nil
}

// origin is where an entry of the trash at top was: its Path, relative to top
// or absolute.
func origin(top, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(top, path)
}

// inside reports whether path is dir or under it, by whole names.
func inside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// trashName is the n-th name tried for base in the trash: base, then base.2,
// base.3; cut so that it and its .trashinfo fit a file name.
func trashName(base string, n int) string {
	suffix := ""
	if n > 1 {
		suffix = fmt.Sprintf(".%d", n)
	}
	limit := 255 - len(".trashinfo") - len(suffix)
	for len(base) > limit {
		_, size := utf8.DecodeLastRuneInString(base)
		base = base[:len(base)-size]
	}
	return base + suffix
}

// restoredName is the n-th name tried when base is taken where it is
// restored: "photo (restored).jpg", "photo (restored 2).jpg"; a folder keeps
// its whole name before the label.
func restoredName(base string, isDir bool, n int) string {
	ext := ""
	if !isDir && filepath.Ext(base) != base {
		ext = filepath.Ext(base)
	}
	label := " (restored)"
	if n > 1 {
		label = fmt.Sprintf(" (restored %d)", n)
	}
	stem := strings.TrimSuffix(base, ext)
	for len(stem)+len(label)+len(ext) > 255 {
		if stem == "" { // an extension of most of the name: it goes too
			stem, ext = ext, ""
		}
		_, size := utf8.DecodeLastRuneInString(stem)
		stem = stem[:len(stem)-size]
	}
	return stem + label + ext
}

// validName: a name of an entry of the trash, not a path.
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.Contains(name, "/")
}
