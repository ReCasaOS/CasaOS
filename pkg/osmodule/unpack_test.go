package osmodule

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"path"
	"slices"
	"strings"
	"testing"
)

// ---- a file system that records what is done to it ---------------------------------------------------------------------------------------------------------------------------

type node struct {
	mode   fs.FileMode // the type bits and the permissions
	data   []byte
	target string
}

// fakeFS is a Destination: a table of paths, the operations done on it in order, and the faults a test asks for. It behaves as a careful file system does: it makes no directory through a
// link or a file, and a file is in place only once it is committed.
type fakeFS struct {
	nodes   map[string]*node
	log     []string
	failOn  string // a path whose file cannot be written
	lstatEr error
	// onLstat is called with the number of the call: a test uses it to change the world between two readings of it.
	onLstat func(n int)
	lstats  int
	// onMkdir is called with the path, before the directory is made: a test uses it to make the directory first.
	onMkdir func(p string)
}

// real is where a path is on this file system: /lib is a link to usr/lib on a system with a merged /usr, and a path below it is a path below usr/lib, as for the kernel.
func (f *fakeFS) real(p string) string {
	if n := f.nodes["/lib"]; n != nil && n.mode&fs.ModeSymlink != 0 && strings.HasPrefix(p, "/lib/") {
		return "/usr/lib" + p[len("/lib"):]
	}
	return p
}

// newMergedFS is a file system with a merged /usr: /lib is a link to usr/lib.
func newMergedFS() *fakeFS {
	f := newFS()
	for _, p := range []string{"/lib/systemd/system", "/lib/systemd"} {
		delete(f.nodes, p)
	}
	f.nodes["/lib"] = &node{mode: fs.ModeSymlink | 0o777, target: "usr/lib"}
	return f
}

func newFS() *fakeFS {
	f := &fakeFS{nodes: map[string]*node{}}
	for _, d := range []string{"/usr", "/usr/bin", "/usr/lib", "/usr/lib/systemd", "/usr/lib/systemd/system", "/etc", "/etc/casaos", "/usr/share", "/usr/share/casaos", "/var", "/var/lib", "/var/lib/casaos", "/lib", "/lib/systemd", "/lib/systemd/system"} {
		f.nodes[d] = &node{mode: fs.ModeDir | 0o755}
	}
	return f
}

func (f *fakeFS) Lstat(p string) (fs.FileMode, error) {
	f.lstats++
	if f.onLstat != nil {
		f.onLstat(f.lstats)
	}
	if f.lstatEr != nil {
		return 0, f.lstatEr
	}
	n, ok := f.nodes[f.real(p)]
	if !ok {
		return 0, fs.ErrNotExist
	}
	return n.mode, nil
}

func (f *fakeFS) Mkdir(p string, perm fs.FileMode) error {
	if f.onMkdir != nil {
		f.onMkdir(p)
	}
	p = f.real(p)
	if n := f.nodes[path.Dir(p)]; n == nil || !n.mode.IsDir() {
		return fs.ErrNotExist // (a directory goes in a directory that is there, not behind a link)
	}
	if f.nodes[p] != nil {
		return fmt.Errorf("%s: %w", p, fs.ErrExist)
	}
	f.nodes[p] = &node{mode: fs.ModeDir | perm}
	f.log = append(f.log, "mkdir "+p)
	return nil
}

type fakeFile struct {
	fs      *fakeFS
	path    string
	perm    fs.FileMode
	replace bool
	buf     bytes.Buffer
}

func (f *fakeFS) NewFile(p string, perm fs.FileMode, replace bool) (File, error) {
	if p == f.failOn {
		return nil, errors.New("disk full")
	}
	if n := f.nodes[path.Dir(f.real(p))]; n == nil || !n.mode.IsDir() {
		return nil, fs.ErrNotExist // (a file goes in a directory that is there)
	}
	if n := f.nodes[f.real(p)]; n != nil && n.mode&fs.ModeSymlink != 0 {
		return nil, errors.New("would write through a link")
	}
	return &fakeFile{fs: f, path: f.real(p), perm: perm, replace: replace}, nil
}

func (w *fakeFile) Write(b []byte) (int, error) { return w.buf.Write(b) }

// Commit publishes the file as a careful file system does: what is at the path is replaced only if the file was asked to replace it.
func (w *fakeFile) Commit() error {
	if w.fs.nodes[w.path] != nil && !w.replace {
		return fmt.Errorf("%s: %w", w.path, fs.ErrExist)
	}
	w.fs.nodes[w.path] = &node{mode: w.perm, data: slices.Clone(w.buf.Bytes())}
	w.fs.log = append(w.fs.log, "file "+w.path)
	return nil
}
func (w *fakeFile) Abort() { w.fs.log = append(w.fs.log, "abort "+w.path) }

func (f *fakeFS) Symlink(target, p string, replace bool) error {
	p = f.real(p)
	if n := f.nodes[path.Dir(p)]; n == nil || !n.mode.IsDir() {
		return fs.ErrNotExist
	}
	if f.nodes[p] != nil && !replace {
		return fmt.Errorf("%s: %w", p, fs.ErrExist)
	}
	f.nodes[p] = &node{mode: fs.ModeSymlink | 0o777, target: target}
	f.log = append(f.log, "link "+p)
	return nil
}

func (f *fakeFS) put(p string, mode fs.FileMode, data string) {
	f.nodes[f.real(p)] = &node{mode: mode, data: []byte(data)}
}

// ---- archives ----------------------------------------------------------------------------------------------------------------------------------------------------------

type tent struct {
	name string
	typ  byte // 0 is a file
	mode int64
	body string
	link string
}

func tarOf(t *testing.T, ents ...tent) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, e := range ents {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Linkname: e.link}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatalf("%q: %v", e.name, err)
		}
		if typ == tar.TypeReg {
			if _, err := w.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// rawEntry is one entry of a tar written by hand, for what the writer of Go would not write: a name with a NUL, a size that is not the size of the data.
func rawEntry(name string, typ byte, size int64, link, body string) []byte {
	h := make([]byte, 512)
	copy(h[0:100], name)
	copy(h[100:108], "0000644\x00")
	copy(h[108:116], "0000000\x00")
	copy(h[116:124], "0000000\x00")
	copy(h[124:136], fmt.Sprintf("%011o\x00", size))
	copy(h[136:148], "00000000000\x00")
	h[156] = typ
	copy(h[157:257], link)
	copy(h[257:263], "ustar\x00")
	copy(h[263:265], "00")
	copy(h[148:156], "        ")
	sum := 0
	for _, b := range h {
		sum += int(b)
	}
	copy(h[148:156], fmt.Sprintf("%06o\x00 ", sum))
	out := append(h, body...)
	if pad := len(body) % 512; pad != 0 {
		out = append(out, make([]byte, 512-pad)...)
	}
	return out
}

func rawTar(entries ...[]byte) []byte {
	return append(bytes.Join(entries, nil), make([]byte, 1024)...)
}

// paxEntry is an extended header of one record, which applies to the entry that follows it.
func paxEntry(key, value string) []byte {
	record := " " + key + "=" + value + "\n"
	n := len(record) + 1 // (the length counts its own digits)
	for len(fmt.Sprint(n))+len(record) != n {
		n = len(fmt.Sprint(n)) + len(record)
	}
	text := fmt.Sprintf("%d%s", n, record)
	return rawEntry("PaxHeader/x", tar.TypeXHeader, int64(len(text)), "", text)
}

// virtual is an archive of which one entry is as large as a test says, made of a header and zeros, so that its size costs nothing to read or to skip.
type virtual struct {
	head   []byte
	length int64
	pos    int64
}

func (v *virtual) Read(p []byte) (int, error) {
	if v.pos >= v.length {
		return 0, io.EOF
	}
	n := int64(len(p))
	if rest := v.length - v.pos; n > rest {
		n = rest
	}
	for i := int64(0); i < n; i++ {
		if at := v.pos + i; at < int64(len(v.head)) {
			p[i] = v.head[at]
		} else {
			p[i] = 0
		}
	}
	v.pos += n
	return int(n), nil
}

func (v *virtual) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		v.pos = off
	case io.SeekCurrent:
		v.pos += off
	case io.SeekEnd:
		v.pos = v.length + off
	}
	if v.pos < 0 {
		return 0, errors.New("negative position")
	}
	return v.pos, nil
}

// bigArchive is module.json, then a file of size bytes at the path, then the end; the size of the whole is as many bytes as that takes, plus extra at the end.
func bigArchive(path string, size int64, extra int64) *virtual {
	manifest := rawEntry("module.json", tar.TypeReg, 2, "", "{}")
	head := slices.Concat(manifest, rawEntry(path, tar.TypeReg, size, "", ""))
	body := size
	if size%512 != 0 {
		body += 512 - size%512
	}
	return &virtual{head: head, length: int64(len(head)) + body + 1024 + extra}
}

// ---- helpers of the tests ----------------------------------------------------------------------------------------------------------------------------------------------

const sysroot = "build/sysroot/"

// testUnit is the unit of the module of the tests, the one file an archive of it may put in a directory of units.
const testUnit = "casaos-virt-management.service"

var testOpts = Options{Unit: testUnit}

func goodEntries() []tent {
	return []tent{
		{name: "module.json", body: `{"schema":1}`},
		{name: "LICENSE", body: "licence text"},
		{name: "NOTICE", body: "notice text"},
		{name: "build/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/usr/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/usr/bin/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/usr/lib/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/usr/lib/systemd/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/usr/lib/systemd/system/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/usr/lib/systemd/system/" + testUnit, body: "[Service]\nExecStart=/usr/bin/casaos-virt-management\n"},
		{name: "build/sysroot/usr/bin/casaos-virt-management", mode: 0o755, body: strings.Repeat("\x7fELF", 300)},
		{name: "build/sysroot/usr/bin/virt", typ: tar.TypeSymlink, link: "casaos-virt-management"},
		{name: "build/sysroot/etc/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/etc/casaos/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/etc/casaos/virt-management.conf.sample", body: "[libvirt]\nURI = qemu:///system\n"},
	}
}

func unpack(t *testing.T, dst *fakeFS, own []string, ents ...tent) (Result, error) {
	t.Helper()
	return Unpack(bytes.NewReader(tarOf(t, ents...)), dst, own, testOpts)
}

func wantRefusedFor(t *testing.T, what string, err error, code string) {
	t.Helper()
	var r *Refused
	if !errors.As(err, &r) || r.Code != code {
		t.Errorf("%s: error = %v, want %s", what, err, code)
	}
}

func wantRefused(t *testing.T, err error, code string) {
	t.Helper()
	var r *Refused
	if !errors.As(err, &r) || r.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

// ---- the tests ---------------------------------------------------------------------------------------------------------------------------------------------------------

func TestAnArchiveIsUnpackedAndTheListOfWhatWasWrittenIsGivenWithTheUnitLast(t *testing.T) {
	dst := newFS()
	res, err := unpack(t, dst, nil, goodEntries()...)
	if err != nil {
		t.Fatal(err)
	}
	wantFiles := []string{"/usr/bin/casaos-virt-management", "/usr/bin/virt", "/etc/casaos/virt-management.conf.sample", "/usr/lib/systemd/system/" + testUnit}
	if !slices.Equal(res.Files, wantFiles) {
		t.Errorf("files = %v", res.Files)
	}
	if string(res.Manifest) != `{"schema":1}` || len(res.Dirs) != 0 {
		t.Errorf("manifest %q, directories %v", res.Manifest, res.Dirs)
	}
	// the list is one that can be written, and read back
	if raw, err := FormatFilesList(entriesOf(res)); err != nil {
		t.Errorf("the list of the files is not a list: %v", err)
	} else if back, err := ParseFilesList(raw); err != nil || !back.Verified || !slices.Equal(back.Paths(), res.Files) {
		t.Errorf("read back %v, %v", back, err)
	}
	// what is on the disk is what was in the archive
	if n := dst.nodes["/usr/bin/casaos-virt-management"]; n == nil || string(n.data) != strings.Repeat("\x7fELF", 300) || n.mode != 0o755 {
		t.Errorf("the program: %+v", n)
	}
	if n := dst.nodes["/usr/bin/virt"]; n == nil || n.mode&fs.ModeSymlink == 0 || n.target != "casaos-virt-management" {
		t.Errorf("the link: %+v", n)
	}
	if n := dst.nodes["/etc/casaos/virt-management.conf.sample"]; n == nil || n.mode != 0o644 {
		t.Errorf("the sample: %+v", n)
	}
	for _, p := range []string{"/module.json", "/LICENSE", "/NOTICE", "/build", "/usr/bin/module.json"} {
		if dst.nodes[p] != nil {
			t.Errorf("%s was written", p)
		}
	}
	// the unit is the last thing put in place: nothing refers to a file that is not there yet
	last := dst.log[len(dst.log)-1]
	if last != "file /usr/lib/systemd/system/"+testUnit {
		t.Errorf("the last operation is %q; log %v", last, dst.log)
	}
	for _, op := range dst.log {
		if strings.HasPrefix(op, "abort") {
			t.Errorf("an abort in %v", dst.log)
		}
	}
}

func TestTheUnitIsLastWhereverItIsInTheArchive(t *testing.T) {
	ents := goodEntries()
	// the unit first, and the other files after it
	var unit tent
	var rest []tent
	for _, e := range ents {
		if strings.HasSuffix(e.name, testUnit) {
			unit = e
		} else {
			rest = append(rest, e)
		}
	}
	ents = append([]tent{rest[0], unit}, rest[1:]...)
	ents = append(ents, tent{name: "build/sysroot/usr/bin/zzz", mode: 0o755, body: "z"}, tent{name: "build/sysroot/etc/casaos/zzz.sample", body: "z"})
	for _, dir := range []string{"usr/lib/systemd/system/", "lib/systemd/system/"} {
		for i := range ents {
			if strings.HasSuffix(ents[i].name, testUnit) {
				ents[i].name = sysroot + dir + testUnit
			}
		}
		dst := newFS()
		res, err := unpack(t, dst, nil, ents...)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Files) != 6 || res.Files[len(res.Files)-1] != "/"+dir+testUnit {
			t.Errorf("%s: the unit is not last: %v", dir, res.Files)
		}
	}
}

func TestTheModesAreTheArchivesWithoutTheBitsThatGiveMorePowerThanTheFileNeeds(t *testing.T) {
	for _, c := range []struct {
		in   int64
		want fs.FileMode
	}{
		{0o755, 0o755}, {0o644, 0o644}, {0o4755, 0o755}, {0o2755, 0o755}, {0o6755, 0o755}, {0o1755, 0o755}, {0o7777, 0o755}, {0o777, 0o755}, {0o666, 0o644}, {0o775, 0o755},
		{0o664, 0o644}, {0o757, 0o755}, {0o622, 0o600}, {0o600, 0o600}, {0o400, 0o400}, {0o4000, 0}, {0o100755, 0o755}, {0o100644, 0o644}, {0o4444, 0o444}, {0o750, 0o750}, {0o700, 0o700},
	} {
		dst := newFS()
		_, err := unpack(t, dst, nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/bin/p", mode: c.in, body: "x"})
		if err != nil {
			t.Fatalf("%o: %v", c.in, err)
		}
		if n := dst.nodes["/usr/bin/p"]; n == nil || n.mode != c.want {
			t.Errorf("file mode %o -> %v, want %v", c.in, n, c.want)
		}
	}
}

func TestAFileOfTheModulesOwnEarlierInstallIsReplacedAndOthersAreNeverTouched(t *testing.T) {
	ents := []tent{{name: "module.json", body: "{}"}, {name: sysroot + "usr/bin/prog", mode: 0o755, body: "new"}}
	// the module's own file, from an install that did not finish
	dst := newFS()
	dst.put("/usr/bin/prog", 0o755, "old")
	res, err := unpack(t, dst, []string{"/usr/bin/prog"}, ents...)
	if err != nil || string(dst.nodes["/usr/bin/prog"].data) != "new" || !slices.Equal(res.Files, []string{"/usr/bin/prog"}) {
		t.Errorf("own file: %v %v %+v", err, res, dst.nodes["/usr/bin/prog"])
	}
	// the same file, which is somebody else's, or nobody's: nothing is written, not even what comes before it
	dst = newFS()
	dst.put("/usr/bin/prog", 0o755, "old")
	_, err = unpack(t, dst, []string{"/usr/bin/another"}, append([]tent{{name: sysroot + "usr/bin/aaa", body: "a"}}, ents...)...)
	wantRefused(t, err, "file_exists")
	if len(dst.log) != 0 || string(dst.nodes["/usr/bin/prog"].data) != "old" || dst.nodes["/usr/bin/aaa"] != nil {
		t.Errorf("something was written: %v", dst.log)
	}
	// a directory where a file goes, a file where a directory goes, a device where a file goes
	for name, setup := range map[string]func(*fakeFS){
		"a directory": func(f *fakeFS) { f.nodes["/usr/bin/prog"] = &node{mode: fs.ModeDir | 0o755} },
		"a device":    func(f *fakeFS) { f.nodes["/usr/bin/prog"] = &node{mode: fs.ModeDevice | fs.ModeCharDevice | 0o600} },
		"a fifo":      func(f *fakeFS) { f.nodes["/usr/bin/prog"] = &node{mode: fs.ModeNamedPipe | 0o600} },
		"a socket":    func(f *fakeFS) { f.nodes["/usr/bin/prog"] = &node{mode: fs.ModeSocket | 0o600} },
	} {
		dst = newFS()
		setup(dst)
		_, err = unpack(t, dst, []string{"/usr/bin/prog"}, ents...)
		wantRefused(t, err, "file_exists")
		if len(dst.log) != 0 {
			t.Errorf("%s: written %v", name, dst.log)
		}
	}
	// a file where the allowed directory goes
	dst = newFS()
	dst.put("/etc/casaos", 0o644, "a file where the module wants a directory")
	_, err = unpack(t, dst, nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "etc/casaos/x.sample", body: "1"})
	wantRefused(t, err, "file_exists")
	if len(dst.log) != 0 {
		t.Errorf("written %v", dst.log)
	}
}

// The only file a module puts in etc/casaos is the sample of its configuration; the configuration itself is the administrator's, and an archive that carries one is refused whether
// one is there or not.
func TestTheSampleOfAConfigurationIsTheOnlyFileOfEtcCasaosAndAnotherSampleIsNeverOverwritten(t *testing.T) {
	sample := tent{name: sysroot + "etc/casaos/virt.conf.sample", body: "default = 1\n"}
	conf := tent{name: sysroot + "etc/casaos/virt.conf", body: "default = 1\n"}
	prog := tent{name: sysroot + "usr/bin/prog", mode: 0o755, body: "p"}
	manifest := tent{name: "module.json", body: "{}"}
	// the configuration of the administrator is there: the sample is written beside it
	dst := newFS()
	dst.put("/etc/casaos/virt.conf", 0o600, "mine = 1\n")
	res, err := unpack(t, dst, nil, manifest, sample, prog)
	if err != nil || string(dst.nodes["/etc/casaos/virt.conf"].data) != "mine = 1\n" || string(dst.nodes["/etc/casaos/virt.conf.sample"].data) != "default = 1\n" {
		t.Fatalf("%v", err)
	}
	if !slices.Equal(res.Files, []string{"/etc/casaos/virt.conf.sample", "/usr/bin/prog"}) {
		t.Errorf("files %v", res.Files)
	}
	// an archive that carries the configuration is refused, with a configuration there or not, and when the module lists it as its own
	for name, c := range map[string]struct {
		put bool
		own []string
	}{"nothing there": {false, nil}, "a configuration there": {true, nil}, "a configuration the module listed": {true, []string{"/etc/casaos/virt.conf"}}} {
		dst = newFS()
		if c.put {
			dst.put("/etc/casaos/virt.conf", 0o600, "mine = 1\n")
		}
		_, err = unpack(t, dst, c.own, manifest, conf, sample, prog)
		wantRefusedFor(t, name, err, "unpack_path")
		if len(dst.log) != 0 {
			t.Errorf("%s: written %v", name, dst.log)
		}
	}
	// the module's own sample, from an install that was done before, is replaced
	dst = newFS()
	dst.put("/etc/casaos/virt.conf.sample", 0o644, "old sample\n")
	if _, err = unpack(t, dst, []string{"/etc/casaos/virt.conf.sample"}, manifest, sample, prog); err != nil || string(dst.nodes["/etc/casaos/virt.conf.sample"].data) != "default = 1\n" {
		t.Errorf("an own sample: %v", err)
	}
	// a sample that is somebody else's is not overwritten
	dst = newFS()
	dst.put("/etc/casaos/virt.conf.sample", 0o644, "theirs")
	_, err = unpack(t, dst, nil, manifest, sample, prog)
	wantRefused(t, err, "file_exists")
	if len(dst.log) != 0 || string(dst.nodes["/etc/casaos/virt.conf.sample"].data) != "theirs" {
		t.Errorf("written %v", dst.log)
	}
	// ...and what is at its place is a file, not a link
	dst = newFS()
	dst.nodes["/etc/casaos/virt.conf.sample"] = &node{mode: fs.ModeSymlink | 0o777, target: "/etc/passwd"}
	_, err = unpack(t, dst, nil, manifest, sample, prog)
	wantRefused(t, err, "unpack_path")
}

func TestAnArchiveThatBreaksARuleWritesNothing(t *testing.T) {
	long := strings.Repeat("a", 256)
	for _, c := range []struct {
		name string
		ents []tent
		code string
	}{
		// where an entry is
		{"a name that goes up", []tent{{name: "../etc/passwd"}}, "unpack_path"},
		{"a name that goes up inside the sysroot", []tent{{name: sysroot + "usr/bin/../../etc/shadow"}}, "unpack_path"},
		{"a name that goes up once and comes back", []tent{{name: sysroot + "usr/bin/../bin/x"}}, "unpack_path"},
		{"an absolute name", []tent{{name: "/etc/passwd"}}, "unpack_path"},
		{"an absolute name that holds the sysroot", []tent{{name: "/" + sysroot + "usr/bin/x"}}, "unpack_path"},
		{"a name that starts with a dot", []tent{{name: "./" + sysroot + "usr/bin/x"}}, "unpack_path"},
		{"a name with a dot in it", []tent{{name: sysroot + "usr/bin/./x"}}, "unpack_path"},
		{"a name with an empty part", []tent{{name: sysroot + "usr//bin/x"}}, "unpack_path"},
		{"a name with a backslash", []tent{{name: sysroot + "usr/bin/a\\b"}}, "unpack_path"},
		{"a name with a backslash for the separator", []tent{{name: "build\\sysroot\\usr\\bin\\x"}}, "unpack_path"},
		{"a name with a new line, which would be two lines of the list", []tent{{name: sysroot + "usr/bin/a\n/etc/passwd"}}, "unpack_path"},
		{"a name with a space", []tent{{name: sysroot + "usr/bin/a b"}}, "unpack_path"},
		{"a name with a dollar", []tent{{name: sysroot + "usr/bin/$HOME"}}, "unpack_path"},
		{"a name with a semicolon", []tent{{name: sysroot + "usr/bin/a;b"}}, "unpack_path"},
		{"a name with an accent", []tent{{name: sysroot + "usr/bin/café"}}, "unpack_path"},
		{"a name with a letter and a combining accent", []tent{{name: sysroot + "usr/bin/café"}}, "unpack_path"},
		{"a name with a character that turns the text round", []tent{{name: sysroot + "usr/bin/a‮b"}}, "unpack_path"},
		{"a part of a name of 256 letters", []tent{{name: sysroot + "usr/bin/" + long}}, "unpack_path"},
		{"a name of more than 1024 bytes", []tent{{name: sysroot + "usr/bin/" + strings.Repeat(strings.Repeat("b", 100)+"/", 11) + "x"}}, "unpack_path"},
		{"a name that is only dots", []tent{{name: sysroot + "usr/bin/..."}}, "unpack_path"},
		{"a name outside the sysroot", []tent{{name: "evil"}}, "unpack_path"},
		{"a manifest that is a copy", []tent{{name: "module.json.bak"}}, "unpack_path"},
		{"a manifest in capitals", []tent{{name: "MODULE.JSON"}}, "unpack_path"},
		{"a root file that is not one of the three", []tent{{name: "COPYING"}}, "unpack_path"},
		{"a readme", []tent{{name: "README.md"}}, "unpack_path"},
		{"a licence with a suffix", []tent{{name: "LICENSE.txt"}}, "unpack_path"},
		{"another directory of build", []tent{{name: "build/other/x"}}, "unpack_path"},
		{"the sysroot that is a file", []tent{{name: "build/sysroot"}}, "unpack_path"},
		{"a manifest that is a directory", []tent{{name: "module.json", typ: tar.TypeDir}}, "unpack_path"},
		{"a manifest that is a link", []tent{{name: "module.json", typ: tar.TypeSymlink, link: "x"}}, "unpack_path"},
		{"no manifest", []tent{{name: sysroot + "usr/bin/x"}}, "unpack_path"},
		// the allow-list, and just outside it
		{"a program in sbin", []tent{{name: sysroot + "usr/sbin/x"}}, "unpack_path"},
		{"the password file", []tent{{name: sysroot + "etc/passwd"}}, "unpack_path"},
		{"a directory that starts like etc/casaos", []tent{{name: sysroot + "etc/casaosx/y"}}, "unpack_path"},
		{"a directory that is the start of etc/casaos", []tent{{name: sysroot + "etc/casao/y"}}, "unpack_path"},
		{"etc/casaos as a file", []tent{{name: sysroot + "etc/casaos"}}, "unpack_path"},
		{"usr/bin as a file", []tent{{name: sysroot + "usr/bin"}}, "unpack_path"},
		{"a file of usr/lib", []tent{{name: sysroot + "usr/lib/x.so"}}, "unpack_path"},
		{"a file of usr/lib/systemd", []tent{{name: sysroot + "usr/lib/systemd/x"}}, "unpack_path"},
		{"a file of a directory of units that starts alike", []tent{{name: sysroot + "usr/lib/systemd/systemx/x"}}, "unpack_path"},
		{"a file of lib", []tent{{name: sysroot + "lib/x"}}, "unpack_path"},
		{"a file of var", []tent{{name: sysroot + "var/x"}}, "unpack_path"},
		{"a file of var/lib", []tent{{name: sysroot + "var/lib/x"}}, "unpack_path"},
		{"a state file of the core", []tent{{name: sysroot + "var/lib/casaos/alerts.json"}}, "unpack_path"},
		{"a page of the core", []tent{{name: sysroot + "usr/share/casaos/modules/catalogue.json"}}, "unpack_path"},
		{"a key for root", []tent{{name: sysroot + "root/.ssh/authorized_keys"}}, "unpack_path"},
		{"a file of a home", []tent{{name: sysroot + "home/x"}}, "unpack_path"},
		{"a directory in capitals", []tent{{name: sysroot + "ETC/casaos/x"}}, "unpack_path"},
		{"a directory outside, as a directory", []tent{{name: sysroot + "usr/sbin/", typ: tar.TypeDir}}, "unpack_path"},
		{"a directory that holds the sysroot twice", []tent{{name: sysroot + "build/sysroot/usr/bin/x"}}, "unpack_path"},
		{"a directory of the allowed ones, in the wrong place", []tent{{name: sysroot + "opt/usr/bin/x"}}, "unpack_path"},
		// what an entry is
		{"a hard link", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeLink, link: "usr/bin/y"}}, "unpack_path"},
		{"a hard link to the password file", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeLink, link: "/etc/passwd"}}, "unpack_path"},
		{"a character device", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeChar}}, "unpack_path"},
		{"a block device", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeBlock}}, "unpack_path"},
		{"a fifo", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeFifo}}, "unpack_path"},
		{"a contiguous file", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeCont}}, "unpack_path"},
		// links
		{"a link to an absolute path", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeSymlink, link: "/etc/passwd"}}, "unpack_path"},
		{"a link that goes up", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeSymlink, link: "../lib/y"}}, "unpack_path"},
		{"a link that goes up out of the sysroot", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeSymlink, link: "../../../../etc/passwd"}}, "unpack_path"},
		{"a link that goes down and up", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeSymlink, link: "sub/../../y"}}, "unpack_path"},
		{"a link that goes down and comes back", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeSymlink, link: "sub/../y"}}, "unpack_path"},
		{"a link to the directory above", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeSymlink, link: ".."}}, "unpack_path"},
		{"a link to nothing", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeSymlink, link: ""}}, "unpack_path"},
		{"a link with a space", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeSymlink, link: "a b"}}, "unpack_path"},
		{"a link with a new line", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeSymlink, link: "a\nb"}}, "unpack_path"},
		{"a link with an empty part", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeSymlink, link: "a//b"}}, "unpack_path"},
		{"a link of 256 characters", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeSymlink, link: long}}, "unpack_path"},
		{"a link with a trailing slash", []tent{{name: sysroot + "usr/bin/x", typ: tar.TypeSymlink, link: "y/"}}, "unpack_path"},
		{"a link outside the allowed directories", []tent{{name: sysroot + "usr/sbin/x", typ: tar.TypeSymlink, link: "y"}}, "unpack_path"},
		{"a file written through a link of the archive", []tent{{name: sysroot + "usr/bin/ln", typ: tar.TypeSymlink, link: "sub"}, {name: sysroot + "usr/bin/ln/evil", body: "x"}}, "unpack_path"},
		{"a file written through a link of the archive, in the other order", []tent{{name: sysroot + "usr/bin/ln/evil", body: "x"}, {name: sysroot + "usr/bin/ln", typ: tar.TypeSymlink, link: "sub"}}, "unpack_path"},
		{"a link to a link that goes up", []tent{{name: sysroot + "usr/bin/a", typ: tar.TypeSymlink, link: "b"}, {name: sysroot + "usr/bin/b", typ: tar.TypeSymlink, link: "../../etc"}}, "unpack_path"},
		// the same name twice
		{"a file twice", []tent{{name: sysroot + "usr/bin/x"}, {name: sysroot + "usr/bin/x"}}, "unpack_path"},
		{"a file and then a directory of that name", []tent{{name: sysroot + "usr/bin/x"}, {name: sysroot + "usr/bin/x/", typ: tar.TypeDir}}, "unpack_path"},
		{"a directory and then a file of that name", []tent{{name: sysroot + "usr/bin/x/", typ: tar.TypeDir}, {name: sysroot + "usr/bin/x"}}, "unpack_path"},
		{"a file and a link of that name", []tent{{name: sysroot + "usr/bin/x"}, {name: sysroot + "usr/bin/x", typ: tar.TypeSymlink, link: "y"}}, "unpack_path"},
		{"a file under a file", []tent{{name: sysroot + "usr/bin/x"}, {name: sysroot + "usr/bin/x/y"}}, "unpack_path"},
		{"a file under a file, in the other order", []tent{{name: sysroot + "usr/bin/x/y"}, {name: sysroot + "usr/bin/x"}}, "unpack_path"},
		{"names that differ by the case", []tent{{name: sysroot + "usr/bin/Foo"}, {name: sysroot + "usr/bin/foo"}}, "unpack_path"},
		{"directories that differ by the case", []tent{{name: sysroot + "usr/bin/A/x"}, {name: sysroot + "usr/bin/a/y"}}, "unpack_path"},
		{"a file and a directory that differ by the case", []tent{{name: sysroot + "usr/bin/A"}, {name: sysroot + "usr/bin/a/y"}}, "unpack_path"},
		{"samples that differ by the case", []tent{{name: sysroot + "etc/casaos/A.sample"}, {name: sysroot + "etc/casaos/a.sample"}}, "unpack_path"},
		{"a manifest twice", []tent{{name: "module.json", body: "{}"}, {name: "module.json", body: "{}"}}, "unpack_path"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ents := c.ents
			if !slices.ContainsFunc(ents, func(e tent) bool { return e.name == "module.json" }) {
				ents = append([]tent{{name: "module.json", body: "{}"}}, ents...)
				if c.name == "no manifest" {
					ents = c.ents
				}
			}
			// something fine comes first, to be sure it is not written either
			ents = append([]tent{{name: sysroot + "usr/bin/fine", mode: 0o755, body: "fine"}}, ents...)
			dst := newFS()
			_, err := Unpack(bytes.NewReader(tarOf(t, ents...)), dst, nil, testOpts)
			wantRefused(t, err, c.code)
			if len(dst.log) != 0 {
				t.Errorf("something was written: %v", dst.log)
			}
			if _, err := ValidateArchive(bytes.NewReader(tarOf(t, ents...)), newFS(), nil, testOpts); err == nil {
				t.Error("the archive is valid")
			}
		})
	}
}

func TestEntriesThatTheWriterOfTarWillNotWriteAreRefusedToo(t *testing.T) {
	manifest := rawEntry("module.json", tar.TypeReg, 2, "", "{}")
	good := rawEntry(sysroot+"usr/bin/fine", tar.TypeReg, 4, "", "fine")
	type raw struct {
		name string
		code string
		ents [][]byte
	}
	for _, c := range []raw{
		{"a name of a file that ends with a slash", "unpack_path", [][]byte{manifest, rawEntry(sysroot+"usr/bin/x/", tar.TypeReg, 1, "", "x")}},
		{"a NUL in a name given by a pax header", "unpack_corrupt", [][]byte{manifest, paxEntry("path", sysroot+"usr/bin/a\x00b"), rawEntry("x", tar.TypeReg, 1, "", "x")}},
		{"a NUL in a target given by a pax header", "unpack_corrupt", [][]byte{manifest, paxEntry("linkpath", "a\x00b"), rawEntry(sysroot+"usr/bin/l", tar.TypeSymlink, 0, "a", "")}},
		{"a name that goes up, given by a pax header", "unpack_path", [][]byte{manifest, paxEntry("path", "../../etc/passwd"), rawEntry("x", tar.TypeReg, 1, "", "x")}},
		{"a name that goes up, given by a gnu long name", "unpack_path", [][]byte{manifest, rawEntry("././@LongLink", 'L', 17, "", "../../etc/passwd\x00"), rawEntry("x", tar.TypeReg, 1, "", "x")}},
		{"a target that is absolute, given by a gnu long link", "unpack_path", [][]byte{manifest, rawEntry("././@LongLink", 'K', 12, "", "/etc/passwd\x00"), rawEntry(sysroot+"usr/bin/l", tar.TypeSymlink, 0, "a", "")}},
		{"a sparse file given by a pax header", "unpack_path", [][]byte{manifest, paxEntry("GNU.sparse.major", "1"), rawEntry(sysroot+"usr/bin/x", tar.TypeReg, 1, "", "x")}},
		{"a name that is not UTF-8", "unpack_path", [][]byte{manifest, rawEntry(sysroot+"usr/bin/\xff", tar.TypeReg, 1, "", "x")}},
		{"a type that is not known", "unpack_path", [][]byte{manifest, rawEntry(sysroot+"usr/bin/x", 'Z', 0, "", "")}},
		{"a sparse file", "unpack_corrupt", [][]byte{manifest, rawEntry(sysroot+"usr/bin/x", 'S', 0, "", "")}},
		{"a directory that has a size", "unpack_corrupt", [][]byte{manifest, rawEntry(sysroot+"usr/bin/", tar.TypeDir, 5, "", "12345")}},
		{"an end that is not the end of a tar", "unpack_corrupt", [][]byte{manifest, good, []byte("this is not a header block")}},
		{"a header that is cut", "unpack_corrupt", [][]byte{manifest, good[:200]}},
		{"a file that is cut", "unpack_corrupt", [][]byte{manifest, rawEntry(sysroot+"usr/bin/x", tar.TypeReg, 2000, "", strings.Repeat("a", 100))}},
		{"a header that is the wrong sum", "unpack_corrupt", [][]byte{manifest, append(slices.Clone(good[:148]), append([]byte("0000001\x00"), good[156:]...)...)}},
	} {
		dst := newFS()
		_, err := Unpack(bytes.NewReader(rawTar(c.ents...)), dst, nil, testOpts)
		wantRefusedFor(t, c.name, err, c.code)
		if len(dst.log) != 0 {
			t.Errorf("%s: written %v", c.name, dst.log)
		}
	}
	// nothing at all, and a tar with nothing in it: no manifest
	for name, raw := range map[string][]byte{"nothing": nil, "an end and nothing before": make([]byte, 1024), "bytes that are no tar": bytes.Repeat([]byte("garbage "), 500)} {
		dst := newFS()
		_, err := Unpack(bytes.NewReader(raw), dst, nil, testOpts)
		var r *Refused
		if !errors.As(err, &r) || len(dst.log) != 0 {
			t.Errorf("%s: %v %v", name, err, dst.log)
		}
	}
}

func TestADestinationThatIsALinkOrBehindOneIsNotWrittenTo(t *testing.T) {
	ents := []tent{{name: "module.json", body: "{}"}, {name: sysroot + "usr/bin/fine", mode: 0o755, body: "fine"}, {name: sysroot + "etc/casaos/fine.sample", body: "x"}}
	for name, setup := range map[string]func(*fakeFS){
		"the file is a link to the password file": func(f *fakeFS) { f.nodes["/usr/bin/fine"] = &node{mode: fs.ModeSymlink | 0o777, target: "/etc/passwd"} },
		"the sample is a link to the password file": func(f *fakeFS) {
			f.nodes["/etc/casaos/fine.sample"] = &node{mode: fs.ModeSymlink | 0o777, target: "/etc/passwd"}
		},
		"the allowed directory itself is a link":        func(f *fakeFS) { f.nodes["/usr/bin"] = &node{mode: fs.ModeSymlink | 0o777, target: "/srv"} },
		"the other allowed directory is a link":         func(f *fakeFS) { f.nodes["/etc/casaos"] = &node{mode: fs.ModeSymlink | 0o777, target: "/srv"} },
		"the file is a link to nothing":                 func(f *fakeFS) { f.nodes["/usr/bin/fine"] = &node{mode: fs.ModeSymlink | 0o777, target: "/nowhere"} },
		"the file is a link, and is in the modules own": func(f *fakeFS) { f.nodes["/usr/bin/fine"] = &node{mode: fs.ModeSymlink | 0o777, target: "/etc/passwd"} },
	} {
		dst := newFS()
		setup(dst)
		_, err := Unpack(bytes.NewReader(tarOf(t, ents...)), dst, []string{"/usr/bin/fine"}, testOpts)
		var r *Refused
		if !errors.As(err, &r) || r.Code != "unpack_path" {
			t.Errorf("%s: error = %v", name, err)
		}
		if len(dst.log) != 0 {
			t.Errorf("%s: written %v", name, dst.log)
		}
	}
	// the allowed directory that is a file where there should be a directory
	dst := newFS()
	dst.put("/etc/casaos", 0o644, "x")
	_, err := Unpack(bytes.NewReader(tarOf(t, ents...)), dst, nil, testOpts)
	wantRefused(t, err, "file_exists")
	// a file system that cannot be looked at is not a refusal of the archive
	dst = newFS()
	dst.lstatEr = errors.New("input/output error")
	_, err = Unpack(bytes.NewReader(tarOf(t, ents...)), dst, nil, testOpts)
	var r *Refused
	if err == nil || errors.As(err, &r) || len(dst.log) != 0 {
		t.Errorf("an error of the disk: %v %v", err, dst.log)
	}
	// directories that exist already are no obstacle, with the modes they have
	dst = newFS()
	dst.nodes["/etc/casaos"] = &node{mode: fs.ModeDir | 0o700}
	if _, err := Unpack(bytes.NewReader(tarOf(t, ents...)), dst, nil, testOpts); err != nil || dst.nodes["/etc/casaos"].mode != fs.ModeDir|0o700 {
		t.Errorf("a directory that is there: %v", err)
	}
}

func TestTheBoundsOfAnArchive(t *testing.T) {
	entries := func(n int) []tent {
		ents := []tent{{name: "module.json", body: "{}"}}
		for i := 1; i < n; i++ {
			ents = append(ents, tent{name: fmt.Sprintf("%susr/bin/f%d", sysroot, i)})
		}
		return ents
	}
	dst := newFS()
	res, err := unpack(t, dst, nil, entries(MaxEntries)...)
	if err != nil || len(res.Files) != MaxEntries-1 {
		t.Errorf("%d entries: %d files, %v", MaxEntries, len(res.Files), err)
	}
	dst = newFS()
	_, err = unpack(t, dst, nil, entries(MaxEntries+1)...)
	wantRefused(t, err, "unpack_limits")
	if len(dst.log) != 0 {
		t.Errorf("written %v", dst.log)
	}
	// the size: a file of exactly the most, and one byte more
	if _, err := ValidateArchive(bigArchive(sysroot+"usr/bin/big", MaxTotalBytes-2, 0), newFS(), nil, testOpts); err != nil {
		t.Errorf("a file that makes the most with the manifest of 2 bytes: %v", err)
	}
	_, err = ValidateArchive(bigArchive(sysroot+"usr/bin/big", MaxTotalBytes-1, 0), newFS(), nil, testOpts)
	wantRefused(t, err, "unpack_limits")
	// the size of the data that is not in the archive: said by the header alone
	lie := &virtual{head: slices.Concat(rawEntry("module.json", tar.TypeReg, 2, "", "{}"), rawEntry(sysroot+"usr/bin/big", tar.TypeReg, 1<<40, "", "")), length: 3000}
	_, err = ValidateArchive(lie, newFS(), nil, testOpts)
	wantRefused(t, err, "unpack_limits")
	lie = &virtual{head: rawEntry(sysroot+"usr/bin/big", tar.TypeReg, 1<<62, "", ""), length: 3000}
	_, err = ValidateArchive(lie, newFS(), nil, testOpts)
	wantRefused(t, err, "unpack_limits")
	// a negative size is a size that is wrong
	neg := rawEntry(sysroot+"usr/bin/big", tar.TypeReg, 0, "", "")
	copy(neg[124:136], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	if _, err := ValidateArchive(bytes.NewReader(rawTar(rawEntry("module.json", tar.TypeReg, 2, "", "{}"), neg)), newFS(), nil, testOpts); err == nil {
		t.Error("a negative size")
	}
	// the sum of the sizes, in several files
	pair := slices.Concat(rawEntry("module.json", tar.TypeReg, 2, "", "{}"), rawEntry(sysroot+"usr/bin/a", tar.TypeReg, MaxTotalBytes/2, "", ""))
	second := rawEntry(sysroot+"usr/bin/b", tar.TypeReg, MaxTotalBytes/2+1, "", "")
	two := &virtual{head: slices.Concat(pair, make([]byte, MaxTotalBytes/2), second), length: 0}
	two.length = int64(len(two.head)) + MaxTotalBytes/2 + 1024 + 512
	_, err = ValidateArchive(two, newFS(), nil, testOpts)
	wantRefused(t, err, "unpack_limits")
	// the stream itself is bounded, whatever it holds
	_, err = ValidateArchive(bigArchive(sysroot+"usr/bin/big", 10, MaxStreamBytes), newFS(), nil, testOpts)
	wantRefused(t, err, "unpack_limits")
	// the manifest has the room of a manifest
	if _, err := unpack(t, newFS(), nil, tent{name: "module.json", body: strings.Repeat(" ", MaxBytes)}); err != nil {
		t.Errorf("a manifest of %d bytes: %v", MaxBytes, err)
	}
	_, err = unpack(t, newFS(), nil, tent{name: "module.json", body: strings.Repeat(" ", MaxBytes+1)})
	wantRefused(t, err, "unpack_limits")
	// the licence and the notice count in the size, and are not limited otherwise
	if _, err := unpack(t, newFS(), nil, tent{name: "module.json", body: "{}"}, tent{name: "LICENSE", body: strings.Repeat("l", 1<<20)}, tent{name: "NOTICE", body: "n"}); err != nil {
		t.Errorf("a long licence: %v", err)
	}
}

func TestWhatIsWrittenIsExactlyTheContentOfTheArchive(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var ents []tent
	want := map[string]string{}
	ents = append(ents, tent{name: "module.json", body: `{"schema":1}`})
	for i, size := range []int{0, 1, 511, 512, 513, 1023, 1024, 100000, 4096} {
		body := make([]byte, size)
		rng.Read(body)
		name := fmt.Sprintf("usr/bin/f%d", i)
		ents = append(ents, tent{name: sysroot + name, body: string(body)})
		want["/"+name] = string(body)
	}
	dst := newFS()
	res, err := unpack(t, dst, nil, ents...)
	if err != nil || len(res.Files) != len(want) {
		t.Fatalf("%v, %d files", err, len(res.Files))
	}
	for p, body := range want {
		if n := dst.nodes[p]; n == nil || string(n.data) != body {
			t.Errorf("%s: wrong content", p)
		}
	}
}

func TestAWriteThatFailsStopsTheUnpackAndSaysWhatWasWritten(t *testing.T) {
	dst := newFS()
	dst.failOn = "/etc/casaos/virt-management.conf.sample"
	res, err := unpack(t, dst, nil, goodEntries()...)
	if err == nil {
		t.Fatal("no error")
	}
	var r *Refused
	if errors.As(err, &r) {
		t.Errorf("a disk that is full is not a refusal of the archive: %v", err)
	}
	// what was written before the failure is in the list, which the rollback will use, and the unit was not written
	if slices.Contains(res.Files, "/usr/lib/systemd/system/"+testUnit) || dst.nodes["/usr/lib/systemd/system/"+testUnit] != nil {
		t.Errorf("the unit was written: %v", res.Files)
	}
	for _, p := range res.Files {
		if dst.nodes[p] == nil {
			t.Errorf("%s is in the list and not on the disk", p)
		}
	}
	if len(res.Files) == 0 || res.Files[0] != "/usr/bin/casaos-virt-management" {
		t.Errorf("files = %v", res.Files)
	}
	// every file on the disk that the archive put there is in the list
	for _, p := range []string{"/usr/bin/casaos-virt-management", "/usr/bin/virt"} {
		if dst.nodes[p] != nil && !slices.Contains(res.Files, p) {
			t.Errorf("%s is on the disk and not in the list", p)
		}
	}
}

// hooked is a ReadSeeker that says when the archive is read from its start again, and lets a test change the world then: an archive that is not the same the second time it is read, a file that
// appears on the disk between the readings.
type hooked struct {
	r       *bytes.Reader
	starts  int
	onStart func(starts int, h *hooked)
	onSeek  func(off int64, h *hooked)
}

func (h *hooked) Read(p []byte) (int, error) { return h.r.Read(p) }
func (h *hooked) Seek(off int64, whence int) (int64, error) {
	if whence == io.SeekStart && off == 0 {
		h.starts++
		h.onStart(h.starts, h)
	}
	if whence == io.SeekStart && off != 0 && h.onSeek != nil {
		h.onSeek(off, h)
	}
	return h.r.Seek(off, whence)
}

func TestAnArchiveOrADestinationThatChangesBetweenTheReadingsIsNotUnpacked(t *testing.T) {
	good := tarOf(t, goodEntries()...)
	evil := tarOf(t, append(goodEntries(), tent{name: sysroot + "usr/bin/extra", body: "x"})...)
	dst := newFS()
	_, err := Unpack(&hooked{r: bytes.NewReader(good), onStart: func(n int, h *hooked) {
		if n == 2 {
			h.r = bytes.NewReader(evil)
		}
	}}, dst, nil, testOpts)
	wantRefused(t, err, "unpack_corrupt")
	if len(dst.log) != 0 {
		t.Errorf("written %v", dst.log)
	}
	// the same archive every time is fine
	dst = newFS()
	if _, err := Unpack(&hooked{r: bytes.NewReader(good), onStart: func(int, *hooked) {}}, dst, nil, testOpts); err != nil {
		t.Errorf("%v", err)
	}
	// a file that appears on the disk between the readings
	dst = newFS()
	_, err = Unpack(&hooked{r: bytes.NewReader(good), onStart: func(n int, _ *hooked) {
		if n == 2 {
			dst.put("/usr/bin/casaos-virt-management", 0o755, "someone else's")
		}
	}}, dst, nil, testOpts)
	wantRefused(t, err, "file_exists")
	if len(dst.log) != 0 {
		t.Errorf("written %v", dst.log)
	}
	// a file that was the module's own, and is gone from the list by the second reading, cannot happen: the list is an argument. A destination that turns into a link does
	dst = newFS()
	dst.put("/usr/bin/casaos-virt-management", 0o755, "old")
	_, err = Unpack(&hooked{r: bytes.NewReader(good), onStart: func(n int, _ *hooked) {
		if n == 2 {
			dst.nodes["/usr/bin/casaos-virt-management"] = &node{mode: fs.ModeSymlink | 0o777, target: "/etc/passwd"}
		}
	}}, dst, []string{"/usr/bin/casaos-virt-management"}, testOpts)
	wantRefused(t, err, "unpack_path")
	if len(dst.log) != 0 {
		t.Errorf("written %v", dst.log)
	}
}

func TestTheErrorOfARefusalSaysWhereWithoutRepeatingWhatTheArchiveSaid(t *testing.T) {
	name := sysroot + "usr/sbin/x\x1b[2J" + strings.Repeat("a", 300)
	_, err := Unpack(bytes.NewReader(rawTar(rawEntry("module.json", tar.TypeReg, 2, "", "{}"), rawEntry(name, tar.TypeReg, 0, "", ""))), newFS(), nil, testOpts)
	var r *Refused
	if !errors.As(err, &r) {
		t.Fatal(err)
	}
	if text := err.Error(); len(text) > 200 || strings.ContainsFunc(text, func(c rune) bool { return c < ' ' || c > '~' }) || !strings.Contains(text, "unpack_path") {
		t.Errorf("%q", text)
	}
}

// A name in a tar is a string that ends at its first NUL, for every reader of tar: the part after it is not read, by this reader any more than by another.
func TestANameInATarEndsAtItsFirstNUL(t *testing.T) {
	dst := newFS()
	res, err := Unpack(bytes.NewReader(rawTar(rawEntry("module.json", tar.TypeReg, 2, "", "{}"), rawEntry(sysroot+"usr/bin/ok\x00/../../../etc/passwd", tar.TypeReg, 1, "", "x"))), dst, nil, testOpts)
	if err != nil || !slices.Equal(res.Files, []string{"/usr/bin/ok"}) || dst.nodes["/etc/passwd"] != nil {
		t.Errorf("%v %v", err, res.Files)
	}
}

func TestTheStreamIsBoundedAtItsLengthAndNotOneByteMore(t *testing.T) {
	base := bigArchive(sysroot+"usr/bin/big", 10, 0).length
	exact := bigArchive(sysroot+"usr/bin/big", 10, MaxStreamBytes-base)
	if exact.length != MaxStreamBytes {
		t.Fatalf("the case is %d bytes", exact.length)
	}
	if _, err := ValidateArchive(exact, newFS(), nil, testOpts); err != nil {
		t.Errorf("a stream of %d bytes: %v", MaxStreamBytes, err)
	}
	_, err := ValidateArchive(bigArchive(sysroot+"usr/bin/big", 10, MaxStreamBytes-base+1), newFS(), nil, testOpts)
	wantRefused(t, err, "unpack_limits")
}

func TestTheTargetOfALinkIsBoundedInAll(t *testing.T) {
	target := func(n int) string { return strings.Repeat("a/", (n-1)/2) + strings.Repeat("a", 1+(n-1)%2) }
	for _, c := range []struct {
		length int
		ok     bool
	}{{255, true}, {256, false}, {300, false}, {1, true}} {
		link := target(c.length)
		if len(link) != c.length {
			t.Fatalf("the case is %d bytes", len(link))
		}
		_, err := unpack(t, newFS(), nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/bin/l", typ: tar.TypeSymlink, link: link})
		if (err == nil) != c.ok {
			t.Errorf("a link of %d bytes: %v", c.length, err)
		}
	}
}

func TestADirectoryEntryIsRefusedEvenOverAFileTheModuleOwns(t *testing.T) {
	dst := newFS()
	dst.put("/usr/bin/x", 0o644, "a file")
	_, err := unpack(t, dst, []string{"/usr/bin/x"}, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/bin/x/", typ: tar.TypeDir})
	wantRefused(t, err, "unpack_path")
	if len(dst.log) != 0 || string(dst.nodes["/usr/bin/x"].data) != "a file" {
		t.Errorf("written %v", dst.log)
	}
}

func TestAFileThatTheArchiveHasLessOfThanItsHeaderSaidIsNotPutInPlace(t *testing.T) {
	good := tarOf(t, goodEntries()...)
	cut := good[:bytes.Index(good, []byte("[libvirt]"))+3]
	dst := newFS()
	res, err := Unpack(&hooked{r: bytes.NewReader(good), onStart: func(int, *hooked) {}, onSeek: func(off int64, h *hooked) { h.r = bytes.NewReader(cut) }}, dst, nil, testOpts)
	if err == nil {
		t.Fatal("no error")
	}
	if dst.nodes["/etc/casaos/virt-management.conf.sample"] != nil || slices.Contains(res.Files, "/etc/casaos/virt-management.conf.sample") || !slices.Contains(dst.log, "abort /etc/casaos/virt-management.conf.sample") {
		t.Errorf("log %v, files %v", dst.log, res.Files)
	}
}

// The reader of tar can be told to flag the names it finds insecure (GODEBUG=tarinsecurepath=0, which is not the default); the unpack refuses them as the paths they are, and still unpacks
// a good archive.
func TestAnInsecurePathThatTheReaderOfTarFlagsIsRefusedAsAPath(t *testing.T) {
	evil := tarOf(t, tent{name: "module.json", body: "{}"}, tent{name: "../etc/passwd"})
	good := tarOf(t, goodEntries()...)
	t.Setenv("GODEBUG", "tarinsecurepath=0")
	dst := newFS()
	_, err := Unpack(bytes.NewReader(evil), dst, nil, testOpts)
	wantRefused(t, err, "unpack_path")
	if len(dst.log) != 0 {
		t.Errorf("written %v", dst.log)
	}
	if _, err := Unpack(bytes.NewReader(good), newFS(), nil, testOpts); err != nil {
		t.Errorf("a good archive: %v", err)
	}
}

func TestTheDirectoriesAboveTheAllowedOnesAreNamedAndNeverMade(t *testing.T) {
	dst := newFS()
	for _, p := range []string{"/lib/systemd/system", "/lib/systemd", "/lib"} {
		delete(dst.nodes, p)
	}
	res, err := unpack(t, dst, nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "lib/", typ: tar.TypeDir, mode: 0o700},
		tent{name: sysroot + "lib/systemd/", typ: tar.TypeDir, mode: 0o700}, tent{name: sysroot + "usr/bin/x", body: "x"})
	if err != nil || !slices.Equal(dst.log, []string{"file /usr/bin/x"}) || dst.nodes["/lib"] != nil || !slices.Equal(res.Files, []string{"/usr/bin/x"}) {
		t.Errorf("%v %v %v", err, dst.log, res.Files)
	}
}
