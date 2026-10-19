package osmodule

import (
	"archive/tar"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
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
// again, which must give the same answer, and the writing), which is why it is a ReadSeeker: the caller has put the decompressed tar in a file of its own. The checking takes the
// sha256 of the bytes of every file, and the writing checks the bytes it copies against it, so that what is written is what was checked, in its names, its modes and its content.

// the limits of an archive
const (
	// MaxTotalBytes is the most the files of an archive come to once unpacked, MaxStreamBytes the most the tar stream itself is (the headers and the padding of MaxEntries entries are in the
	// difference).
	MaxTotalBytes  int64 = 400 << 20
	MaxStreamBytes int64 = MaxTotalBytes + 64<<20
)

const tarBlock = 512

// ReservedPrograms are the programs of the system that the assistant runs as root, or that what it runs runs by name (apt, dpkg and their scripts, systemd): an archive may not put a
// program of one of these names in usr/bin, where on a system whose /bin is not /usr/bin it would come before the real one in the path. The assistant finds each of them once, before
// anything is unpacked, and runs them by the path it found. (ponytail: a list of names; a program of another name that exists in /bin and not in /usr/bin is not covered.)
var ReservedPrograms = []string{
	"systemctl", "systemd-run", "journalctl", "apt-get", "apt", "apt-cache", "apt-config", "apt-key", "apt-mark", "dpkg", "dpkg-query", "dpkg-deb", "dpkg-divert", "dpkg-statoverride",
	"update-rc.d", "invoke-rc.d", "deb-systemd-helper", "deb-systemd-invoke", "sh", "dash", "bash", "env",
}

// the codes of a refusal of an archive
const (
	// UnpackPath: an entry that is not allowed, by its name, its type, its link, or where it would be put; a destination that is a link or behind one.
	UnpackPath = "unpack_path"
	// FileExists: a file or a directory that is in the way and is not the module's own.
	FileExists = "file_exists"
	// UnpackLimits: more entries, directories or bytes than an archive of a module has.
	UnpackLimits = "unpack_limits"
	// UnpackCorrupt: an archive that is not a tar, is cut, has no end, or is not the same the second time it is read, or the third.
	UnpackCorrupt = "unpack_corrupt"
)

// A Refused is why an archive was not unpacked: a code, and where (the name of the entry, cut and made printable, since it is the archive's own text). When the code comes before the
// writing, nothing was written; when it comes from the writing (a file that appeared in the way, bytes that are not the ones that were checked), the Result of Unpack says what was.
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

// A Destination is the file system that an archive is unpacked onto. Paths are absolute. It is the seam of the tests, and the real one is the machine. The unpack has checked, from the
// allowed directory down, that none of the directories that exist is a link; what is above the allowed directory is the system's own (on a system with a merged /usr, /lib is a link to
// /usr/lib). It makes nothing that was not asked for, and none of its writes goes through a link.
type Destination interface {
	// Lstat is the type and the permissions of what is at the path, without following a link at its end; fs.ErrNotExist when there is nothing.
	Lstat(path string) (fs.FileMode, error)
	// Mkdir makes one directory, whose parent is there, and fails with an error that is fs.ErrExist if anything is at the path. perm is EXACT: it is what the directory has once Mkdir
	// returns (a chmod after the creation), not what the umask leaves of it.
	Mkdir(path string, perm fs.FileMode) error
	// NewFile starts a file for path: what is written to it is not at the path until Commit; Abort drops it, and may be called after a Commit that failed. perm is EXACT, as for Mkdir (the
	// file is chmoded, it is not made with perm and the umask). The temporary name is short (a path of the file system can be at its longest) and in the directory of the file, so that the
	// publication is one rename in a file system.
	// When replace is false, Commit publishes the file only if nothing is at the path then, in one step that cannot be raced (renameat2 with RENAME_NOREPLACE, or a link then an unlink), and
	// otherwise fails with an error that is fs.ErrExist and leaves what is there as it is. When replace is true it replaces what is at the path, a file or a link, in one step (a rename).
	NewFile(path string, perm fs.FileMode, replace bool) (File, error)
	// Symlink makes the link, with the same rules as a file: when replace is false it fails with an error that is fs.ErrExist if anything is at the path, and when it is true it replaces
	// what is there in one step (a link made under a temporary name, then a rename).
	Symlink(target, path string, replace bool) error
}

// A File is a file being written.
type File interface {
	io.Writer
	Commit() error
	Abort()
}

// A Want is what a path that the unpack writes should hold: the sha256 of the bytes of a file in lower case hex, or the target of a link. It is what the list of the files records, and what
// a remove, a rollback and a later install check before they delete or replace the path.
type Want struct {
	Link  bool
	Value string
}

// A Result is what an unpack did. Files is the list of the files and links the module owns after it, in the order they were put down, with the units last: what goes in the
// <name>.files list, and what a rollback deletes; Want says what each of them holds. Dirs are the directories the unpack made, and no other, the deepest first, for a rollback or a remove to
// delete (when they are empty). All are given when the unpack stops half way too. Manifest is the module.json of the archive, not yet read as a manifest.
type Result struct {
	Files    []string
	Want     map[string]Want
	Dirs     []string
	Manifest []byte
}

// Options are what an unpack is told besides the archive and the file system.
type Options struct {
	// Unit is the unit of the module, as its manifest names it: the one file an archive may put in a directory of units. Empty, an archive may put none.
	Unit string
	// OnPlanned, when it is not nil, is called once, after the checking and before anything is written, with every path of a file or a link that the unpack may write (in the order they
	// are put down, the units last), what each is to hold, and every directory it may make (the parents first): the caller records them, so that a kill half way leaves a record that names
	// everything that the unpack might have put down, and if it fails nothing is written. What the module owns in the end is the Result's, which can be less: a path that was planned and not
	// written may be a file that is not the module's (it is what made the unpack stop), so a caller that deletes after a stop deletes what the Result holds, and only what still holds what
	// it was to hold, never what it was told in advance.
	OnPlanned func(files []string, want map[string]Want, dirs []string) error
}

type kind int

const (
	kFile kind = iota
	kLink
	kDir
	kIgnored // an entry that is read and not written: a directory above the allowed ones, the licence, the notice
)

// an entry of the archive that is to be written, as the first reading saw it
type entry struct {
	rel     string // the path under the sysroot, no slash at the start
	kind    kind
	perm    fs.FileMode
	offset  int64
	size    int64
	sum     [sha256.Size]byte // of the bytes of a file
	target  string
	replace bool // the module's own file or link, which the new one replaces; anything else that appears at the path is in the way
}

// want is what the path of the entry should hold once it is written.
func (e entry) want() Want {
	if e.kind == kLink {
		return Want{Link: true, Value: e.target}
	}
	return Want{Value: hex.EncodeToString(e.sum[:])}
}

func (e entry) isUnit() bool {
	return strings.HasPrefix(e.rel, "usr/lib/systemd/system/") || strings.HasPrefix(e.rel, "lib/systemd/system/")
}

// An UnpackPlan is what the checking of an archive leaves: what to write, in the order of the archive, the directories to make (all with 0755: they are only on the way to the allowed
// directory), parents first, and the manifest.
type UnpackPlan struct {
	entries  []entry
	dirs     []string
	Manifest []byte
}

// ValidateArchive reads the whole archive and the whole destination and says whether Unpack would write it; it writes nothing.
func ValidateArchive(src io.ReadSeeker, dst Destination, own []string, opts Options) (*UnpackPlan, error) {
	plan, err := scan(src, opts.Unit)
	if err != nil {
		return nil, err
	}
	if err := checkDestination(plan, dst, own); err != nil {
		return nil, err
	}
	return plan, nil
}

// Unpack unpacks the archive onto dst, after checking all of it. own are the files that this module's earlier install wrote (its list of files): they may be replaced, and no other file
// may, not even one that appears after the checking. When the unpack stops half way the Result holds what was written and the directories it made, with the error, and the caller rolls
// back with it; the unpack deletes nothing.
func Unpack(src io.ReadSeeker, dst Destination, own []string, opts Options) (Result, error) {
	plan, err := ValidateArchive(src, dst, own, opts)
	if err != nil {
		return Result{}, err
	}
	// the archive and the destination are read once more: what is written is what was checked, or it is nothing
	again, err := ValidateArchive(src, dst, own, opts)
	if err != nil {
		return Result{}, err
	}
	if !reflect.DeepEqual(plan, again) {
		return Result{}, refuse(UnpackCorrupt, "the archive or the destination changed")
	}
	return write(plan, src, dst, opts.OnPlanned)
}

// scan reads the archive and checks every entry against the rules that do not need the destination.
func scan(src io.ReadSeeker, unit string) (*UnpackPlan, error) {
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
		plan    = &UnpackPlan{}
		reader  = tar.NewReader(src)
		names   = newNames()
		total   int64
		count   int
		seen    bool  // module.json
		lastEnd int64 // where the last entry, with its padding, ends
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
		e, err := classify(hdr, unit)
		if err != nil {
			return nil, err
		}
		e.offset = offset
		lastEnd = offset
		if e.kind == kFile {
			if hdr.Size > MaxTotalBytes-total {
				return nil, refuse(UnpackLimits, "the files are too large")
			}
			total += hdr.Size
			e.size = hdr.Size
			lastEnd += (hdr.Size + tarBlock - 1) / tarBlock * tarBlock
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
		// (a name that is one of the system with a merged /usr in two spellings is one name)
		key := e.rel
		if e.kind != kIgnored {
			key = mergedRel(e.rel)
		}
		if err := names.add(key, e.kind); err != nil {
			return nil, refuse(UnpackPath, hdr.Name)
		}
		if e.kind == kFile {
			sum := sha256.New()
			if n, err := io.Copy(sum, reader); err != nil || n != hdr.Size {
				return nil, refuse(UnpackCorrupt, "a file of the archive cannot be read")
			}
			copy(e.sum[:], sum.Sum(nil))
		}
		plan.entries = append(plan.entries, e)
	}
	// the end of a tar is two blocks of zeros after the last entry: an archive that is cut where an entry ends is cut, whoever cut it
	if pos, err := src.Seek(0, io.SeekCurrent); err != nil || pos-lastEnd < 2*tarBlock {
		return nil, refuse(UnpackCorrupt, "the archive has no end")
	}
	if !seen {
		return nil, refuse(UnpackPath, "no module.json")
	}
	return plan, nil
}

// classify says what an entry of the archive is, or why it is not allowed: by its type, its name and, for a link, its target. An entry that is read and not written has no rel.
func classify(hdr *tar.Header, unit string) (entry, error) {
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
		if !placeOK(rel, e.kind, func(name string) bool { return name == unit }) {
			return entry{}, refuse(UnpackPath, hdr.Name)
		}
		if program, ok := strings.CutPrefix(rel, "usr/bin/"); ok && slices.Contains(ReservedPrograms, program) {
			return entry{}, refuse(UnpackPath, hdr.Name)
		}
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
	if len(target) > MaxElementBytes {
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

// mergedAbs is an absolute path with /lib spelled /usr/lib, as it is on a system with a merged /usr where the one is a link to the other.
func mergedAbs(p string) string {
	if rest, ok := strings.CutPrefix(p, "/lib/"); ok {
		return "/usr/lib/" + rest
	}
	return p
}

func mergedRel(rel string) string { return strings.TrimPrefix(mergedAbs("/"+rel), "/") }

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

// checkDestination looks at what is where the archive would put its files, and decides which replace the module's own and which must not replace anything, and which directories are
// made (the allowed directory, and the ones above it, when the system has none of them).
func checkDestination(plan *UnpackPlan, dst Destination, own []string) error {
	// on a system with a merged /usr, /lib is /usr/lib: a file the module owns under the one name is the same file under the other
	resolve := func(p string) string { return p }
	switch mode, err := dst.Lstat("/lib"); {
	case err == nil && mode&fs.ModeSymlink != 0:
		resolve = mergedAbs
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("looking at /lib: %w", err)
	}
	owned := make(map[string]bool, len(own))
	for _, p := range own {
		owned[resolve(p)] = true
	}
	// the directories to make: every one that is not there, once
	made := map[string]bool{}
	wantDir := func(abs string) {
		if !made[abs] {
			made[abs] = true
			plan.dirs = append(plan.dirs, abs)
		}
	}
	for i := range plan.entries {
		e := &plan.entries[i]
		if e.kind == kIgnored {
			continue
		}
		// every directory from the root down to the parent is a directory, or is not there yet; from the allowed directory down none is a link
		top := strings.Count(allowedDirOf(e.rel), "/")
		gone := false // a directory that is not there has nothing in it
		at := ""
		for j, part := range strings.Split(path.Dir(e.rel), "/") {
			at += "/" + part
			if made[at] {
				gone = true
				continue
			}
			if gone {
				wantDir(at)
				continue
			}
			mode, err := dst.Lstat(at)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				gone = true
				wantDir(at)
			case err != nil:
				return fmt.Errorf("looking at %s: %w", at, err)
			case mode&fs.ModeSymlink != 0 && j >= top:
				return refuse(UnpackPath, e.rel)
			case mode&fs.ModeSymlink != 0:
				// above the allowed directory: the system's own
			case !mode.IsDir():
				return refuse(FileExists, e.rel)
			}
		}
		abs := "/" + e.rel
		var (
			mode fs.FileMode
			err  = error(fs.ErrNotExist)
		)
		if !gone {
			mode, err = dst.Lstat(abs)
		}
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return fmt.Errorf("looking at %s: %w", abs, err)
		case mode&fs.ModeSymlink != 0 && owned[resolve(abs)]:
			// the module's own link (the caller says what is the module's: only what still holds what its record says), which the new entry replaces whatever it is: a rename puts it in place of the
			// link, and nothing is ever written through it
			e.replace = true
		case mode&fs.ModeSymlink != 0:
			return refuse(UnpackPath, e.rel)
		case owned[resolve(abs)] && mode.IsRegular():
			e.replace = true
		default:
			return refuse(FileExists, e.rel)
		}
	}
	// parents first
	slices.SortStableFunc(plan.dirs, func(a, b string) int { return cmp.Compare(strings.Count(a, "/"), strings.Count(b, "/")) })
	return nil
}

// write puts the checked entries on the disk: the directories, then the files and links, then the units; an entry that fails stops the unpack.
func write(plan *UnpackPlan, src io.ReadSeeker, dst Destination, onPlanned func(files []string, want map[string]Want, dirs []string) error) (Result, error) {
	res := Result{Manifest: plan.Manifest, Want: map[string]Want{}}
	order := make([]entry, 0, len(plan.entries))
	for _, pass := range []func(entry) bool{
		func(e entry) bool { return !e.isUnit() },
		func(e entry) bool { return e.isUnit() },
	} {
		for _, e := range plan.entries {
			if e.kind != kIgnored && pass(e) {
				order = append(order, e)
			}
		}
	}
	if onPlanned != nil {
		planned := make([]string, len(order))
		want := make(map[string]Want, len(order))
		for i, e := range order {
			planned[i] = "/" + e.rel
			want[planned[i]] = e.want()
		}
		if err := onPlanned(planned, want, slices.Clone(plan.dirs)); err != nil {
			return res, fmt.Errorf("recording the files: %w", err)
		}
	}
	for _, d := range plan.dirs {
		switch err := dst.Mkdir(d, 0o755); {
		case err == nil:
			res.Dirs = slices.Insert(res.Dirs, 0, d)
		case errors.Is(err, fs.ErrExist):
			// somebody made it since the checking: it stays as it is, and is not ours to delete, if it is a directory
			if mode, err := dst.Lstat(d); err != nil || !mode.IsDir() {
				return res, refuse(UnpackPath, strings.TrimPrefix(d, "/"))
			}
		default:
			return res, fmt.Errorf("making %s: %w", d, err)
		}
	}
	for _, e := range order {
		abs := "/" + e.rel
		var err error
		if e.kind == kLink {
			err = dst.Symlink(e.target, abs, e.replace)
		} else {
			err = copyFile(dst, src, abs, e)
		}
		if err != nil {
			var refused *Refused
			switch {
			case errors.As(err, &refused):
				return res, err
			case errors.Is(err, fs.ErrExist):
				return res, refuse(FileExists, e.rel) // it appeared since the checking
			}
			return res, fmt.Errorf("writing %s: %w", abs, err)
		}
		res.Files = append(res.Files, abs)
		res.Want[abs] = e.want()
	}
	return res, nil
}

func copyFile(dst Destination, src io.ReadSeeker, abs string, e entry) error {
	file, err := dst.NewFile(abs, e.perm, e.replace)
	if err != nil {
		return err
	}
	if _, err := src.Seek(e.offset, io.SeekStart); err != nil {
		file.Abort()
		return err
	}
	sum := sha256.New()
	if n, err := io.Copy(io.MultiWriter(file, sum), io.LimitReader(src, e.size)); err != nil || n != e.size {
		file.Abort()
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	if [sha256.Size]byte(sum.Sum(nil)) != e.sum {
		file.Abort()
		return refuse(UnpackCorrupt, e.rel)
	}
	if err := file.Commit(); err != nil {
		file.Abort()
		return err
	}
	return nil
}
