package osreadiness

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The names apt reads, and the files it does not, in /etc/apt/sources.list.d (GetListOfFilesInDir of apt
// 2.2.4 and 2.6.1, the apt of Debian 11 and 12): letters, digits, "_", "-" and ":", "." where the extension
// is, no name that starts with a dot, and nothing that is not a regular file.
func TestAptReadsAColonInTheNameOfASourceAndNotADotAtItsStart(t *testing.T) {
	f := newBox(t)
	bad := "deb cdrom:[x]/ bullseye main\n"
	f.set("/etc/apt/sources.list.d/local:mirror.list", bad)
	f.set("/etc/apt/sources.list.d/.hidden.list", bad)
	f.set("/etc/apt/sources.list.d/.old.sources", "Types: deb\nURIs: file:///x\nSuites: bullseye\n")
	set, err := loadSources(context.Background(), f)
	if err != nil || len(set.Unreadable) != 0 {
		t.Fatalf("set = %+v, err = %v", set, err)
	}
	var files []string
	for _, e := range set.Entries {
		files = append(files, e.File)
	}
	if want := []string{"sources.list", "sources.list", "sources.list", "local:mirror.list"}; !reflect.DeepEqual(files, want) {
		t.Fatalf("files read = %v, want %v", files, want)
	}
	for _, path := range f.readPaths() {
		if strings.Contains(path, "/.") {
			t.Errorf("read %s: apt skips what starts with a dot", path)
		}
	}
}

// Only a regular file is read, as apt does: ln -s /dev/null google-chrome.list is how a package's source
// is kept from coming back, and a directory or a device in the place of a source is not an unreadable one.
func TestAFileOfTheSourcesThatIsNotARegularOneIsLeftAsAptLeavesIt(t *testing.T) {
	f := newBox(t)
	f.device("/dev/null")
	f.link("/etc/apt/sources.list.d/google-chrome.list", "/dev/null")
	f.dirs["/etc/apt/sources.list.d/dir.list"] = true
	f.link("/etc/apt/sources.list.d/dangling.list", "/nowhere")
	f.set("/real.list", "deb http://deb.debian.org/debian bullseye main\n")
	f.link("/etc/apt/sources.list.d/real.list", "/real.list")
	set, err := loadSources(context.Background(), f)
	if err != nil || len(set.Unreadable) != 0 {
		t.Fatalf("set = %+v, err = %v", set, err)
	}
	var files []string
	for _, e := range set.Entries {
		if e.File != "sources.list" {
			files = append(files, e.File)
		}
	}
	if want := []string{"real.list"}; !reflect.DeepEqual(files, want) {
		t.Fatalf("files read = %v, want %v: a link to a regular file is that file", files, want)
	}
	// sources.list itself turned off the same way
	f.link("/etc/apt/sources.list", "/dev/null")
	if set, err = loadSources(context.Background(), f); err != nil || len(set.Unreadable) != 0 {
		t.Fatalf("set = %+v, err = %v", set, err)
	}
	// a file that is a regular one and cannot be read is still one the rewriter cannot read
	f.set("/etc/apt/sources.list.d/locked.list", "x")
	f.fail("/etc/apt/sources.list.d/locked.list", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
	if set, _ = loadSources(context.Background(), f); !reflect.DeepEqual(set.Unreadable, []string{"locked.list"}) {
		t.Fatalf("unreadable = %v", set.Unreadable)
	}
}

// apt reads "Enabled" with StringToBool: no, false, without, off, disable and the number 0 are false, and
// anything it does not understand is the default, which is true.
func TestEnabledIsReadAsAptReadsABoolean(t *testing.T) {
	for _, value := range []string{"no", "No", "NO", "false", "FALSE", "without", "Without", "off", "OFF", "disable", "Disable", "0", "00", "-0", "+0", "0x0", "0X00", " off "} {
		entries, ok := parseDeb822("x.sources", "Types: deb\nURIs: http://x\nSuites: a\nEnabled: "+value+"\n")
		if !ok || len(entries) != 0 {
			t.Errorf("Enabled: %q is read as enabled: %v %v", value, entries, ok)
		}
	}
	for _, value := range []string{"yes", "true", "1", "2", "on", "enable", "with", "", "08", "0x", "maybe", "x0", "-1", "00x0", "off now", "nope"} {
		entries, ok := parseDeb822("x.sources", "Types: deb\nURIs: http://x\nSuites: a\nEnabled: "+value+"\n")
		if !ok || len(entries) != 1 {
			t.Errorf("Enabled: %q is read as disabled: %v %v", value, entries, ok)
		}
	}
}

// What is read of a .sources file is bounded by its size and by nothing else: a field that goes on for
// ever (a broken tool, a paste, a hostile file) is read in the time the size of the file takes, and a file
// of more sources than a machine has is one that is not understood, and not one that is multiplied.
func TestAFieldOfManyLinesIsReadInLinearTime(t *testing.T) {
	text := "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bullseye\nComponents:" + strings.Repeat("\n a", 340000) + "\n"
	if len(text) > MaxFileBytes {
		t.Fatalf("the payload is %d bytes, more than a file can be", len(text))
	}
	type answer struct{ entries, components int }
	done := make(chan answer, 1)
	started := time.Now()
	go func() {
		entries, _ := parseDeb822("x.sources", text)
		got := answer{entries: len(entries)}
		if len(entries) > 0 {
			got.components = len(entries[0].Components)
		}
		done <- got
	}()
	select {
	case got := <-done:
		if got.entries != 1 || got.components != 340000 {
			t.Fatalf("entries = %d, components = %d", got.entries, got.components)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("a field of 340000 lines is still being read after %v", time.Since(started))
	}
}

func TestAFileOfMoreSourcesThanAMachineHasIsNotUnderstood(t *testing.T) {
	var uris strings.Builder
	for i := 0; i < 30000; i++ {
		uris.WriteString("\n http://mirror.example.org/" + strconv.Itoa(i))
	}
	cases := []struct {
		name  string
		parse func() bool
	}{
		// 100000 types of the same name make no more than one, and 30000 addresses are more than a machine has
		{"many addresses", func() bool {
			_, ok := parseDeb822("x.sources", "Types:"+strings.Repeat(" deb", 100000)+"\nURIs:"+uris.String()+"\nSuites: a\n")
			return ok
		}},
		{"many lines", func() bool {
			_, ok := parseOneLine("x.list", strings.Repeat("deb http://deb.debian.org/debian bullseye main\n", 5000))
			return ok
		}},
	}
	for _, tc := range cases {
		done := make(chan bool, 1)
		go func() { done <- tc.parse() }()
		select {
		case ok := <-done:
			if ok {
				t.Errorf("%s: understood", tc.name)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: not done after 5 seconds", tc.name)
		}
	}
	// the same address and the same type, written many times, are one source
	entries, ok := parseDeb822("x.sources", "Types:"+strings.Repeat(" deb", 5000)+"\nURIs:"+strings.Repeat(" http://x", 5000)+"\nSuites: a\n")
	if !ok || len(entries) != 1 {
		t.Errorf("repeated words: %d entries, understood %v", len(entries), ok)
	}
	// two types and two addresses are four sources
	entries, ok = parseDeb822("x.sources", "Types: deb deb-src\nURIs: http://x/1 http://x/2\nSuites: a\n")
	if !ok || len(entries) != 4 {
		t.Errorf("two types and two addresses: %d entries, understood %v", len(entries), ok)
	}
}

// The seven checks of the sources look at the same files: a report reads and reads them once, however
// large they are, and a hostile one costs what its size costs, once.
func TestTheSourcesAreReadOnceForTheWholeReportAndHostileOnesCostLittle(t *testing.T) {
	f := newBox(t)
	f.set("/etc/apt/sources.list.d/x.sources", "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bullseye\nComponents: main"+strings.Repeat("\n a", 340000)+"\n")
	started := time.Now()
	report := Run(context.Background(), f, Options{})
	if elapsed := time.Since(started); elapsed > 8*time.Second {
		t.Fatalf("the report took %v", elapsed)
	}
	for _, code := range []string{"sources_cdrom", "sources_alias", "sources_pin", "sources_proposed", "sources_format", "sources_third_party", "sources_backports", "sources_firmware"} {
		if got := findingOf(t, report, code); got.Severity == Refuse {
			t.Errorf("%s: %+v", code, got)
		}
	}
	reads := map[string]int{}
	for _, path := range f.readPaths() {
		if strings.HasPrefix(path, "read /etc/apt/sources.list") || strings.HasPrefix(path, "readdir /etc/apt/sources.list") {
			reads[path]++
		}
	}
	for _, path := range []string{"read /etc/apt/sources.list", "readdir /etc/apt/sources.list.d", "read /etc/apt/sources.list.d/x.sources"} {
		if reads[path] != 1 {
			t.Errorf("%s was asked %d times, want once: %v", path, reads[path], reads)
		}
	}
}

// A source that could not be read for the end of the time says so to the checks that ask for it, and
// they all say it: none answers for a file that was not read.
func TestTheSourcesThatTookTooLongAreSaidSoByEveryCheckThatAsksForThem(t *testing.T) {
	f := newBox(t)
	f.fail("/etc/apt/sources.list", context.DeadlineExceeded)
	report := Run(context.Background(), f, Options{})
	for _, code := range []string{"sources_cdrom", "sources_alias", "sources_proposed", "sources_format", "sources_third_party", "sources_backports", "sources_firmware"} {
		got := findingOf(t, report, code)
		if got.Severity != Refuse || got.Data["unknown"] != true || got.Data["reason"] != ReasonTimeout {
			t.Errorf("%s: %+v", code, got)
		}
	}
}

// More files than a machine has are not read: the answer is that there are too many.
func TestMoreSourceFilesThanAMachineHasAreNotRead(t *testing.T) {
	f := newBox(t)
	for i := 0; i < maxSourceFiles+1; i++ {
		f.set("/etc/apt/sources.list.d/"+strconv.Itoa(i)+".list", "deb http://deb.debian.org/debian bullseye main\n")
	}
	if _, err := loadSources(context.Background(), f); err == nil || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	got := checkOn(t, f, "sources_cdrom")
	if got.Severity != Refuse || got.Data["reason"] != ReasonTooLarge {
		t.Fatalf("finding = %+v", got)
	}
}

// panicOnSources is a machine whose sources cannot be listed without a panic.
type panicOnSources struct{ *fakeProbe }

func (p panicOnSources) ReadDir(ctx context.Context, path string) ([]string, error) {
	if path == "/etc/apt/sources.list.d" {
		panic("a probe that breaks")
	}
	return p.fakeProbe.ReadDir(ctx, path)
}

// What reads the sources for all the checks is not one of them: its panic is theirs, and the report goes on.
func TestAPanicWhileTheSourcesAreReadIsAnAnswerOfEveryCheckThatAsks(t *testing.T) {
	report := Run(context.Background(), panicOnSources{newBox(t)}, Options{})
	for _, code := range []string{"sources_cdrom", "sources_alias", "sources_proposed", "sources_format", "sources_third_party", "sources_backports", "sources_firmware"} {
		got := findingOf(t, report, code)
		if got.Severity != Refuse || got.Data["reason"] != ReasonPanic {
			t.Errorf("%s: %+v", code, got)
		}
	}
	if got := findingOf(t, report, "sources_pin"); got.Data["unknown"] != false {
		t.Errorf("sources_pin does not read them: %+v", got)
	}
}
