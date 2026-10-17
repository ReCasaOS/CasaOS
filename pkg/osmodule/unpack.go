package osmodule

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"reflect"
	"slices"
	"strings"
)

// The unpack of the archive of a module: the only place where the content of an archive reaches the file system. The archive is the module's release archive, already checked against the
// sha256 the catalogue gives; that proves it is the one the catalogue names, not that what is in it is harmless, so every entry is read as hostile data.
//
// The unpack is in two steps and never mixes them. It first reads the whole archive and looks at the whole destination, and refuses, writing nothing, if any entry breaks a rule or any
// file it would write is in the way. Only then does it write, and each file is put in place whole or not at all. The archive is read three times for it (the checking, the same checking
// again, which must give the same answer, and the writing), which is why it is a ReadSeeker: the caller has put the decompressed tar in a file of its own.

// the limits of an archive
const (
	// MaxTotalBytes is the most the files of an archive come to once unpacked, MaxStreamBytes the most the tar stream itself is (the headers and the padding of MaxEntries entries are in the
	// difference).
	MaxTotalBytes  int64 = 400 << 20
	MaxStreamBytes int64 = MaxTotalBytes + 64<<20
)

// the codes of a refusal of an archive
const (
	// UnpackPath: an entry that is not allowed, by its name, its type, its link, or where it would be put; a destination that is a link or behind one.
	UnpackPath = "unpack_path"
	// FileExists: a file or a directory that is in the way and is not the module's own.
	FileExists = "file_exists"
	// UnpackLimits: more entries, or more bytes, than an archive of a module has.
	UnpackLimits = "unpack_limits"
	// UnpackCorrupt: an archive that is not a tar, is cut, or is not the same the second time it is read.
	UnpackCorrupt = "unpack_corrupt"
)

// A Refused is why an archive was not unpacked: a code, and where (the name of the entry, cut and made printable, since it is the archive's own text). Nothing was written.
type Refused struct {
	Code, Where string
}

func (r *Refused) Error() string {
	if r.Where == "" {
		return r.Code
	}
	return r.Code + ": " + r.Where
}

func refuse(code, where string) error { return &Refused{Code: code, Where: clip(where)} }

// A Destination is the file system that an archive is unpacked onto. Paths are absolute. It is the seam of the tests, and the real one is the machine. A file or a link must replace what
// is at its path in one step (a temporary name in the same directory, then a rename), NewFile must not write through a link, and MkdirAll makes the directories that are missing. The unpack
// has checked, from the allowed directory down, that none of the directories that exist is a link; what is above the allowed directory is the system's own (on a system with a merged
// /usr, /lib is a link to /usr/lib).
type Destination interface {
	// Lstat is the type and the permissions of what is at the path, without following a link at its end; fs.ErrNotExist when there is nothing.
	Lstat(path string) (fs.FileMode, error)
	MkdirAll(path string, perm fs.FileMode) error
	// NewFile starts a file for path: what is written to it is not at the path until Commit; Abort drops it.
	NewFile(path string, perm fs.FileMode) (File, error)
	Symlink(target, path string) error
}

// A File is a file being written.
type File interface {
	io.Writer
	Commit() error
	Abort()
}

// A Result is what an unpack did. Files is the list of the files and links the module owns after it, in the order they were put down, with the units last: what goes in the
// <name>.files list, and what a rollback deletes. It is given when the unpack stops half way too. Kept are the configuration files that were already there and were left as they are.
// Manifest is the module.json of the archive, not yet read as a manifest.
type Result struct {
	Files    []string
	Kept     []string
	Manifest []byte
}

type kind int

const (
	kFile kind = iota
	kLink
	kDir
	kIgnored // an entry that is read and not written: a directory above the allowed ones, the licence, the notice
)

type action int

const (
	aWrite action = iota
	aKeep         // a configuration that is there and stays
)

// an entry of the archive that is to be written, as the first reading saw it
type entry struct {
	rel    string // the path under the sysroot, no slash at the start
	kind   kind
	perm   fs.FileMode
	offset int64
	size   int64
	target string
	action action
}

func (e entry) isUnit() bool {
	return strings.HasPrefix(e.rel, "usr/lib/systemd/system/") || strings.HasPrefix(e.rel, "lib/systemd/system/")
}

// An UnpackPlan is what the checking of an archive leaves: what to write, in the order of the archive, and the manifest.
type UnpackPlan struct {
	entries  []entry
	Manifest []byte
}

// ValidateArchive reads the whole archive and the whole destination and says whether Unpack would write it; it writes nothing.
func ValidateArchive(src io.ReadSeeker, dst Destination, own []string) (*UnpackPlan, error) {
	plan, err := scan(src)
	if err != nil {
		return nil, err
	}
	if err := checkDestination(plan, dst, own); err != nil {
		return nil, err
	}
	return plan, nil
}

// Unpack unpacks the archive onto dst, after checking all of it. own are the files that this module's earlier install wrote (its list of files): they may be replaced, and no other file
// may. When the unpack stops half way the Result holds what was written, with the error.
func Unpack(src io.ReadSeeker, dst Destination, own []string) (Result, error) {
	plan, err := ValidateArchive(src, dst, own)
	if err != nil {
		return Result{}, err
	}
	// the archive and the destination are read once more: what is written is what was checked, or it is nothing
	again, err := ValidateArchive(src, dst, own)
	if err != nil {
		return Result{}, err
	}
	if !reflect.DeepEqual(plan, again) {
		return Result{}, refuse(UnpackCorrupt, "the archive or the destination changed")
	}
	return write(plan, src, dst, own)
}

// scan reads the archive and checks every entry against the rules that do not need the destination.
func scan(src io.ReadSeeker) (*UnpackPlan, error) {
	end, err := src.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, refuse(UnpackCorrupt, "the archive cannot be read")
	}
	if end > MaxStreamBytes {
		return nil, refuse(UnpackLimits, "the archive is too large")
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, refuse(UnpackCorrupt, "the archive cannot be read")
	}
	var (
		plan   = &UnpackPlan{}
		reader = tar.NewReader(src)
		names  = newNames()
		total  int64
		count  int
		seen   bool // module.json
	)
	for {
		hdr, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, tar.ErrInsecurePath) {
			return nil, refuse(UnpackPath, hdr.Name)
		}
		if err != nil {
			return nil, refuse(UnpackCorrupt, "the archive is not a tar")
		}
		if count++; count > MaxEntries {
			return nil, refuse(UnpackLimits, "too many entries")
		}
		offset, err := src.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, refuse(UnpackCorrupt, "the archive cannot be read")
		}
		e, err := classify(hdr)
		if err != nil {
			return nil, err
		}
		e.offset = offset
		if e.kind == kFile {
			if hdr.Size < 0 || hdr.Size > MaxTotalBytes-total {
				return nil, refuse(UnpackLimits, "the files are too large")
			}
			total += hdr.Size
			e.size = hdr.Size
		}
		if hdr.Name == "module.json" {
			if seen {
				return nil, refuse(UnpackPath, hdr.Name)
			}
			seen = true
			if hdr.Size > MaxBytes {
				return nil, refuse(UnpackLimits, "the manifest is too large")
			}
			if plan.Manifest, err = io.ReadAll(io.LimitReader(reader, MaxBytes+1)); err != nil || int64(len(plan.Manifest)) != hdr.Size {
				return nil, refuse(UnpackCorrupt, "the manifest cannot be read")
			}
			continue
		}
		if e.rel == "" {
			continue // the licence, the notice, the directories above the sysroot
		}
		if err := names.add(e.rel, e.kind); err != nil {
			return nil, refuse(UnpackPath, hdr.Name)
		}
		plan.entries = append(plan.entries, e)
	}
	if !seen {
		return nil, refuse(UnpackPath, "no module.json")
	}
	return plan, nil
}

// classify says what an entry of the archive is, or why it is not allowed: by its type, its name and, for a link, its target. An entry that is read and not written has no rel.
func classify(hdr *tar.Header) (entry, error) {
	// (a file made of holes is read by the reader of Go as a file of the size it has when filled, and then it is not in the archive where its header says)
	for key := range hdr.PAXRecords {
		if strings.HasPrefix(key, "GNU.sparse") {
			return entry{}, refuse(UnpackPath, hdr.Name)
		}
	}
	e := entry{perm: fs.FileMode(hdr.Mode & 0o755)}
	switch hdr.Typeflag {
	case tar.TypeReg:
		e.kind = kFile
	case tar.TypeDir:
		e.kind = kDir
	case tar.TypeSymlink:
		e.kind = kLink
	default:
		return entry{}, refuse(UnpackPath, hdr.Name)
	}
	name := hdr.Name
	if e.kind == kDir {
		name = strings.TrimSuffix(name, "/")
	}
	switch {
	case name == "module.json" || name == "LICENSE" || name == "NOTICE":
		if e.kind != kFile {
			return entry{}, refuse(UnpackPath, hdr.Name)
		}
		return e, nil
	case e.kind == kDir && (name == "build" || name == "build/sysroot"):
		return e, nil
	}
	rel, ok := strings.CutPrefix(name, "build/sysroot/")
	if !ok || !safeRel(rel) {
		return entry{}, refuse(UnpackPath, hdr.Name)
	}
	e.rel = rel
	switch {
	case underAllowed(rel):
	case e.kind == kDir && aboveAllowed(rel):
		e.kind = kIgnored
		return e, nil
	default:
		return entry{}, refuse(UnpackPath, hdr.Name)
	}
	if e.kind == kLink {
		e.target = hdr.Linkname
		if !safeLinkTarget(e.target) {
			return entry{}, refuse(UnpackPath, hdr.Name)
		}
	}
	return e, nil
}

// aboveAllowed says whether a directory is an allowed one or one that an allowed one is in: such a directory is named by archives that list their directories, and is never made or touched
// by the unpack (the ones that are allowed are made as needed, below).
func aboveAllowed(rel string) bool {
	for _, dir := range AllowedDirs {
		if dir == rel || strings.HasPrefix(dir, rel+"/") {
			return true
		}
	}
	return false
}

// safeLinkTarget says whether a link points only below the directory the link is in: relative, made of safe names, never ".." (so there is no way up, in one step or by going down first), and
// never an empty part.
func safeLinkTarget(target string) bool {
	if target == "" || len(target) > MaxElementBytes {
		return false
	}
	for _, el := range strings.Split(target, "/") {
		if el == "." {
			continue
		}
		if !safeElement(el) {
			return false
		}
	}
	return true
}

// names are the names of the entries seen so far, to refuse an entry that clashes with one of them. Two names are one when they differ only by the case (a file system that ignores the case
// holds one file for both), and an entry is a clash when its name was an entry's already, when it is under a file or a link, or when it is a file or a link that has entries under it.
type names struct {
	kinds   map[string]kind   // the kind of each entry, by its name in lower case
	implied map[string]bool   // the directories that an entry is in, by their names in lower case
	spelled map[string]string // how each name of an entry or of a directory it is in is written, by its name in lower case
}

func newNames() *names {
	return &names{kinds: map[string]kind{}, implied: map[string]bool{}, spelled: map[string]string{}}
}

func (n *names) add(rel string, k kind) error {
	key := strings.ToLower(rel)
	if _, dup := n.kinds[key]; dup {
		return errors.New("a name twice")
	}
	if (k == kFile || k == kLink) && n.implied[key] {
		return errors.New("a file that is a directory of another entry")
	}
	for dir, lower := rel, key; ; {
		if n.spelled[lower] != "" && n.spelled[lower] != dir {
			return errors.New("a name that differs only by the case")
		}
		n.spelled[lower] = dir
		if dir = path.Dir(dir); dir == "." {
			break
		}
		lower = strings.ToLower(dir)
		if other, ok := n.kinds[lower]; ok && (other == kFile || other == kLink) {
			return errors.New("under a file")
		}
		n.implied[lower] = true
	}
	n.kinds[key] = k
	return nil
}

// checkDestination looks at what is where the archive would put its files, and decides which are written and which are kept.
func checkDestination(plan *UnpackPlan, dst Destination, own []string) error {
	for i := range plan.entries {
		e := &plan.entries[i]
		if e.kind == kIgnored {
			continue
		}
		dir := allowedDirOf(e.rel)
		// every directory from the allowed one down to the parent is a directory, or is not there yet
		parents := strings.Split(strings.TrimPrefix(path.Dir(e.rel), dir), "/")
		for at, j := "/"+dir, 0; j < len(parents); j++ {
			if parents[j] != "" {
				at += "/" + parents[j]
			}
			mode, err := dst.Lstat(at)
			switch {
			case errors.Is(err, fs.ErrNotExist):
			case err != nil:
				return fmt.Errorf("looking at %s: %w", at, err)
			case mode&fs.ModeSymlink != 0:
				return refuse(UnpackPath, e.rel)
			case !mode.IsDir():
				return refuse(FileExists, e.rel)
			}
		}
		abs := "/" + e.rel
		mode, err := dst.Lstat(abs)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return fmt.Errorf("looking at %s: %w", abs, err)
		case mode&fs.ModeSymlink != 0:
			return refuse(UnpackPath, e.rel)
		case e.kind == kDir && mode.IsDir():
		case e.kind == kDir || mode.IsDir():
			return refuse(FileExists, e.rel)
		case strings.HasPrefix(e.rel, "etc/casaos/") && !strings.HasSuffix(e.rel, ".sample") && mode.IsRegular():
			e.action = aKeep // a configuration that is there stays as it is, whoever wrote it
		case slices.Contains(own, abs) && mode.IsRegular():
		default:
			return refuse(FileExists, e.rel)
		}
	}
	return nil
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

// write puts the checked entries on the disk: the directories, then the files and links, then the units; an entry that fails stops the unpack.
func write(plan *UnpackPlan, src io.ReadSeeker, dst Destination, own []string) (Result, error) {
	res := Result{Manifest: plan.Manifest}
	order := make([]entry, 0, len(plan.entries))
	for _, pass := range []func(entry) bool{
		func(e entry) bool { return e.kind == kDir },
		func(e entry) bool { return e.kind != kDir && !e.isUnit() },
		func(e entry) bool { return e.kind != kDir && e.isUnit() },
	} {
		for _, e := range plan.entries {
			if e.kind != kIgnored && pass(e) {
				order = append(order, e)
			}
		}
	}
	for _, e := range order {
		abs := "/" + e.rel
		if e.kind == kDir {
			if err := dst.MkdirAll(abs, e.perm); err != nil {
				return res, fmt.Errorf("making %s: %w", abs, err)
			}
			continue
		}
		if e.action == aKeep {
			if slices.Contains(own, abs) {
				res.Files = append(res.Files, abs)
			}
			res.Kept = append(res.Kept, abs)
			continue
		}
		if err := dst.MkdirAll(path.Dir(abs), 0o755); err != nil {
			return res, fmt.Errorf("making %s: %w", path.Dir(abs), err)
		}
		var err error
		if e.kind == kLink {
			err = dst.Symlink(e.target, abs)
		} else {
			err = copyFile(dst, src, abs, e)
		}
		if err != nil {
			return res, fmt.Errorf("writing %s: %w", abs, err)
		}
		res.Files = append(res.Files, abs)
	}
	return res, nil
}

func copyFile(dst Destination, src io.ReadSeeker, abs string, e entry) error {
	file, err := dst.NewFile(abs, e.perm)
	if err != nil {
		return err
	}
	if _, err := src.Seek(e.offset, io.SeekStart); err != nil {
		file.Abort()
		return err
	}
	if n, err := io.Copy(file, io.LimitReader(src, e.size)); err != nil || n != e.size {
		file.Abort()
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	if err := file.Commit(); err != nil {
		file.Abort()
		return err
	}
	return nil
}
