package osverify

import (
	"context"
	"io/fs"
	"maps"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

const bkDir = "/var/backups/casaos-os-upgrade-p1-0123456789abcdef0123456789abcdef"

func TestTheBackupFolderIsReadThroughTheProbe(t *testing.T) {
	f := newFake()
	want := bkFixture(t)
	for name, text := range want {
		f.set(bkDir+"/"+name, text)
	}
	// files of the folder that the judge does not read are not read
	f.set(bkDir+"/etc.tar.gz", "x")
	f.set(bkDir+"/restore-sources.sh", "x")
	got := ReadBK(context.Background(), f, bkDir)
	if !maps.Equal(got, want) {
		t.Errorf("read %v, want %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	for _, path := range f.read {
		if !strings.HasPrefix(path, bkDir+"/") || !slices.Contains(BKNames, strings.TrimPrefix(path, bkDir+"/")) {
			t.Errorf("%s was read", path)
		}
	}
	if len(BKNames) != 10 {
		t.Errorf("%d files are read", len(BKNames))
	}
}

func TestAFileOfTheBackupFolderThatCannotBeReadIsNotThere(t *testing.T) {
	f := newFake()
	f.set(bkDir+"/kernel", "6.1.0-25-amd64\n")
	f.set(bkDir+"/listening.txt", strings.Repeat("x", 1<<20+1))
	f.fail(bkDir+"/containers.txt", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
	f.set(bkDir+"/dpkg-files.txt", "")
	got := ReadBK(context.Background(), f, bkDir)
	if len(got) != 2 || got[BKKernel] != "6.1.0-25-amd64\n" {
		t.Errorf("read %v", got)
	}
	if text, ok := got[BKDpkgFiles]; !ok || text != "" {
		t.Errorf("a file that is empty is not a file that is not there: %q, %v", text, ok)
	}
}

func TestOnlyABackupFolderOfAHopIsRead(t *testing.T) {
	for _, dir := range []string{
		"", "/", "/etc", "/etc/apt", "/var/backups", "/var/backups/", "/var/backups/casaos-os-upgrade-", "/var/backups/casaos-os-upgrade-timers.state/..",
		"/var/backups/casaos-os-upgrade-x/..", "/var/backups/casaos-os-upgrade-../etc", "/var/backups/casaos-os-upgrade-x/y", "/var/backups/casaos-os-upgrade-x/", "var/backups/casaos-os-upgrade-x",
		"/var/backups/casaos-os-upgrade-x\n/etc", "/var/backups/casaos-os-upgrade-x y", "/var/backups/casaos-os-upgrade-x\x00", "/var/backups/other-casaos-os-upgrade-x",
		"/tmp/var/backups/casaos-os-upgrade-x", "/var/backups/casaos-os-upgrade-" + strings.Repeat("a", 121), "/var/backups/casaos-os-upgrade-a..b",
	} {
		f := newFake()
		f.set(dir+"/kernel", "6.1\n")
		f.set("/etc/kernel", "6.1\n")
		if got := ReadBK(context.Background(), f, dir); len(got) != 0 || len(f.read) != 0 {
			t.Errorf("%q: read %v, opened %v", dir, got, f.read)
		}
	}
	for _, dir := range []string{"/var/backups/casaos-os-upgrade-p1-0123456789abcdef", "/var/backups/casaos-os-upgrade-refresh-1b9d6bcd-bbfd-4b2d-9b5d-ab8dfbbd4bed", "/var/backups/casaos-os-upgrade-x", "/var/backups/casaos-os-upgrade-" + strings.Repeat("a", 120)} {
		f := newFake()
		f.set(dir+"/kernel", "6.1\n")
		if got := ReadBK(context.Background(), f, dir); got[BKKernel] != "6.1\n" {
			t.Errorf("%q: read %v", dir, got)
		}
	}
}

// What the files of the backup folder and the facts can be made of, when the box is not what was thought: the parsers read
// what is in front of them, and never fail on it, take long over it, or let it through as it is.
func TestHostileFilesAreReadAndJudgedWithoutHarm(t *testing.T) {
	hostile := []string{
		"", "\n", "\r\n\r\n", "\x00", "\x00\x00\n\x00", "\xff\xfe\xfd", strings.Repeat("a", 1<<20), strings.Repeat("a\n", 1<<19), strings.Repeat(":", 100000),
		strings.Repeat("0.0.0.0:80\n", 100000), strings.Repeat("/x policy=always state=running\n", 50000), "\n\n\n   \n\t\n", "a b c d e f g\n", strings.Repeat("é", 70000),
		"/jellyfin policy=" + strings.Repeat("a", 5000) + " state=running\n", "policy= state=\n", "/ policy= state=running\n", "-\n", "\u2028\u2029\n",
	}
	rng := rand.New(rand.NewSource(7))
	const alphabet = "ab:/ .\n\r\x00\xff0123456789-_=\\"
	for i := 0; i < 200; i++ {
		b := make([]byte, rng.Intn(400))
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		hostile = append(hostile, string(b))
	}
	healthy := newWorld(t)
	for _, text := range hostile {
		w := &world{before: clone(t, healthy.before), after: clone(t, healthy.after), bk: BKFiles{}}
		for _, name := range BKNames {
			w.bk[name] = text
		}
		got := w.judge()
		if got == nil {
			t.Fatalf("no list of findings for %.40q", text)
		}
		for _, f := range got {
			if SeverityOf(f.Code) != f.Severity || len(f.Arg) > 200 || !slices.Contains(Codes, f.Code) {
				t.Fatalf("%.40q: a finding that is not one: %+v", text, f)
			}
			if strings.ContainsFunc(f.Arg, func(r rune) bool { return r < ' ' || r == 0x7f }) {
				t.Fatalf("%.40q: %q is not text", text, f.Arg)
			}
		}
		mergerfsMounts(text)
		defaultRoute(text)
		parseInspected(text)
		unescapeMount(text)
		NotReturned(w.bk, w.after)
	}
}

func FuzzTheFilesTheJudgeReads(f *testing.F) {
	for _, seed := range []string{"", "0.0.0.0:80\n[::]:22\n", "/a policy=always state=running\n", "casaos.service active\n", "a /p fuse.mergerfs rw 0 0\n", "eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\n", "6.1.0-25-amd64\n", "\x00\xff\\040\\777\n"} {
		f.Add(seed, seed)
	}
	healthy := newWorld(f)
	f.Fuzz(func(t *testing.T, a, b string) {
		w := &world{before: healthy.before, after: healthy.after, bk: BKFiles{}}
		for i, name := range BKNames {
			if i%2 == 0 {
				w.bk[name] = a
			} else {
				w.bk[name] = b
			}
		}
		for _, f := range w.judge() {
			if SeverityOf(f.Code) != f.Severity {
				t.Fatalf("%+v", f)
			}
		}
		mergerfsMounts(a)
		defaultRoute(a)
		parseInspected(a)
	})
}
