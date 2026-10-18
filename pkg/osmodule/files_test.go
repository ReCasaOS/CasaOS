package osmodule

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestAListOfFilesIsReadAndWrittenBack(t *testing.T) {
	paths := []string{
		"/usr/bin/casaos-virt-management",
		"/etc/casaos/virt-management.conf.sample",
		"/usr/bin/virt",
		"/lib/systemd/system/a@.service",
		"/usr/lib/systemd/system/casaos-virt-management.service",
	}
	raw, err := FormatFilesList(entriesFor(paths))
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseFiles(raw)
	if err != nil || !slices.Equal(back, paths) {
		t.Errorf("read back %v, %v", back, err)
	}
	// ...and the earlier format, one path to a line, is read as well
	back, err = ParseFiles([]byte(strings.Join(paths, "\n") + "\n"))
	if err != nil || !slices.Equal(back, paths) {
		t.Errorf("read back %v, %v", back, err)
	}
	// nothing at all is not a list, both ways: a list that was cut short at its start must not read as a module that has no files
	var bad *Invalid
	if got, err := ParseFiles(nil); !errors.As(err, &bad) || bad.Reason != ReasonMissing || got != nil {
		t.Errorf("an empty list: %v %v", got, err)
	}
	if got, err := ParseFiles([]byte{}); err == nil || got != nil {
		t.Errorf("an empty list: %v %v", got, err)
	}
	if raw, err := FormatFilesList(nil); !errors.As(err, &bad) || bad.Reason != ReasonMissing || raw != nil {
		t.Errorf("a list of nothing: %q %v", raw, err)
	}
	// and a list of one path is one
	if got, err := ParseFiles([]byte("/usr/bin/a\n")); err != nil || !slices.Equal(got, []string{"/usr/bin/a"}) {
		t.Errorf("a list of one: %v %v", got, err)
	}
}

func TestAPathIsInTheListOnlyIfItIsADirectChildOfAnAllowedDirectoryOfTheRightName(t *testing.T) {
	for _, dir := range AllowedDirs {
		leaf, long := "file", strings.Repeat("a", 255)
		switch {
		case isUnitDir(dir):
			leaf, long = "file.service", strings.Repeat("a", 60)+".service"
		case dir == "etc/casaos":
			leaf, long = "file.sample", strings.Repeat("a", 248)+".sample"
		}
		for _, c := range []struct {
			path string
			ok   bool
		}{
			{"/" + dir + "/" + leaf, true},
			{"/" + dir + "/sub/dir/" + leaf, false}, // a directory has no subdirectory a module may fill
			{"/" + dir + "/sub/" + leaf, false},
			{"/" + dir, false},       // the directory itself is not a file
			{"/" + dir + "/", false}, // nor with its slash
			{"/" + dir + "x/" + leaf, false},
			{"/" + dir[:len(dir)-1] + "/" + leaf, false},
			{"/" + dir[:strings.LastIndex(dir, "/")+1] + leaf, false}, // the parent of the directory
			{"/" + strings.ToUpper(dir) + "/" + leaf, false},
			{dir + "/" + leaf, false}, // relative
			{"/" + dir + "/../../etc/passwd", false},
			{"/" + dir + "/./" + leaf, false},
			{"/" + dir + "//" + leaf, false},
			{"//" + dir + "/" + leaf, false},
			{"/" + dir + "/" + leaf + "/", false},
			{"/" + dir + "/..", false},
			{"/" + dir + "/...", false},
			{"/" + dir + "/fi le" + leaf, false},
			{"/" + dir + "/fi\tle" + leaf, false},
			{"/" + dir + "/fi\\le" + leaf, false},
			{"/" + dir + "/fi\nle" + leaf, false},
			{"/" + dir + "/fi\x00le" + leaf, false},
			{"/" + dir + "/fi\rle" + leaf, false},
			{"/" + dir + "/fi*le" + leaf, false},
			{"/" + dir + "/fi$le" + leaf, false},
			{"/" + dir + "/fi;le" + leaf, false},
			{"/" + dir + "/caf\u00e9" + leaf, false},
			{"/" + dir + "/\u202efile" + leaf, false},
			{"/" + dir + "/" + long, true},
		} {
			if got := ValidFilePath(c.path); got != c.ok {
				t.Errorf("ValidFilePath(%q) = %v", c.path, got)
			}
		}
	}
	// the name of the file, by directory
	for p, ok := range map[string]bool{
		"/usr/bin/a": true, "/usr/bin/a.service": true, "/usr/bin/a.sample": true, "/usr/bin/" + strings.Repeat("a", 255): true, "/usr/bin/" + strings.Repeat("a", 256): false,
		"/etc/casaos/a.sample": true, "/etc/casaos/a.conf": false, "/etc/casaos/a": false, "/etc/casaos/a.sample.conf": false, "/etc/casaos/a.samples": false, "/etc/casaos/a.Sample": false,
		"/etc/casaos/" + strings.Repeat("a", 248) + ".sample": true, "/etc/casaos/" + strings.Repeat("a", 249) + ".sample": false,
		"/usr/lib/systemd/system/a.service": true, "/lib/systemd/system/a.service": true, "/usr/lib/systemd/system/a@.service": true, "/usr/lib/systemd/system/a": false,
		"/usr/lib/systemd/system/a.timer": false, "/usr/lib/systemd/system/a.service.d": false, "/usr/lib/systemd/system/.service": false, "/usr/lib/systemd/system/a b.service": false,
		"/usr/lib/systemd/system/-a.service": false, "/lib/systemd/system/a.service.x": false,
		"/usr/lib/systemd/system/" + strings.Repeat("a", 64) + ".service": true, "/usr/lib/systemd/system/" + strings.Repeat("a", 65) + ".service": false,
	} {
		if got := ValidFilePath(p); got != ok {
			t.Errorf("ValidFilePath(%q) = %v", p, got)
		}
	}
	for _, p := range []string{
		"", "/", "usr/bin/x", "/etc/passwd", "/usr/sbin/x", "/usr/lib/x", "/usr/lib/systemd/x", "/lib/x", "/var/lib/x", "/var/lib/casaosx/y", "/usr/share/x",
		"/var/lib/casaos/alerts.json", "/var/lib/casaos/telemetry.json", "/var/lib/casaos/autoupdate.json", "/var/lib/casaos/modules/virt.files", "/var/lib/casaos/x.sample",
		"/usr/share/casaos/modules/catalogue.json", "/usr/share/casaos/x.sample", "/usr/share/casaos/www/index.html", "/etc/casaos/modules/virt.json", "/etc/casaos/os-upgrade.beta",
	} {
		if ValidFilePath(p) {
			t.Errorf("ValidFilePath(%q)", p)
		}
	}
}

func TestADirectoryIsInTheListOfDirectoriesOnlyIfItIsAnAllowedOneOrAboveOne(t *testing.T) {
	for p, ok := range map[string]bool{
		"/usr": true, "/usr/bin": true, "/usr/lib": true, "/usr/lib/systemd": true, "/usr/lib/systemd/system": true, "/lib": true, "/lib/systemd": true, "/lib/systemd/system": true,
		"/etc": true, "/etc/casaos": true,
		"/": false, "": false, "usr/bin": false, "/usr/": false, "/usr/bin/": false, "/usr/bin/x": false, "/usr/share": false, "/usr/share/casaos": false, "/var": false, "/var/lib": false,
		"/var/lib/casaos": false, "/etc/casaos/modules": false, "/usr/lib/systemd/system/x": false, "/usr/lib/x": false, "/home": false, "/ETC": false, "/etc//casaos": false, "/etc/../usr": false,
	} {
		if got := ValidDirPath(p); got != ok {
			t.Errorf("ValidDirPath(%q) = %v", p, got)
		}
	}
	paths := []string{"/usr/lib/systemd/system", "/usr/lib/systemd", "/etc/casaos"}
	raw, err := FormatDirs(paths)
	if err != nil || string(raw) != strings.Join(paths, "\n")+"\n" {
		t.Fatalf("%q %v", raw, err)
	}
	if back, err := ParseDirs(raw); err != nil || !slices.Equal(back, paths) {
		t.Errorf("read back %v, %v", back, err)
	}
	var bad *Invalid
	for name, raw := range map[string]string{"nothing": "", "no new line": "/etc/casaos", "a file": "/usr/bin/x\n", "twice": "/etc\n/etc\n", "a blank line": "/etc\n\n", "a sub directory": "/etc/casaos/x\n"} {
		if got, err := ParseDirs([]byte(raw)); !errors.As(err, &bad) || got != nil {
			t.Errorf("%s: taken %v, %v", name, got, err)
		}
	}
	for _, p := range [][]string{nil, {"/usr/bin/x"}, {"/etc", "/etc"}, {"etc"}} {
		if raw, err := FormatDirs(p); err == nil {
			t.Errorf("FormatDirs(%q) = %q", p, raw)
		}
	}
	// a list of files is not a list of directories, nor the other way round
	if _, err := ParseFiles([]byte("/etc/casaos\n")); err == nil {
		t.Error("a directory in a list of files")
	}
	if _, err := ParseDirs([]byte("/usr/bin/x\n")); err == nil {
		t.Error("a file in a list of directories")
	}
}

func TestAListThatHasOneBadLineIsNotTakenWhole(t *testing.T) {
	const good = "/usr/bin/a\n"
	for name, raw := range map[string]string{
		"no new line at the end":      "/usr/bin/a",
		"a blank line":                good + "\n" + good[:len(good)-1] + "b\n",
		"a blank line at the end":     good + "\n",
		"a line with a return":        "/usr/bin/a\r\n",
		"a relative path":             good + "usr/bin/b\n",
		"a path that goes up":         good + "/usr/bin/../../etc/shadow\n",
		"a path outside":              good + "/etc/passwd\n",
		"a path with a space":         good + "/usr/bin/a b\n",
		"a NUL":                       good + "/usr/bin/b\x00\n",
		"a path twice":                good + good,
		"a path with a new line in":   good + "/usr/bin/b\n/usr/bin/c\x0b\n",
		"a directory":                 good + "/etc/casaos\n",
		"a path in capitals":          good + "/USR/BIN/b\n",
		"a path that is not UTF-8":    good + "/usr/bin/\xff\n",
		"a byte order mark":           "\xef\xbb\xbf" + good,
		"spaces around":               " /usr/bin/a \n",
		"a comment":                   "# files\n" + good,
		"a path with a glob":          "/usr/bin/*\n",
		"a path with a dollar":        "/usr/bin/$HOME\n",
		"a path with a back quote":    "/usr/bin/`id`\n",
		"a path with an option":       "/usr/bin/-rf\n/etc/casaos/x\n/usr/bin/ -rf\n",
		"a link to the root":          "/usr/bin/..\n",
		"only dots":                   "/usr/bin/....\n",
		"a path with a colon and bad": "/usr/bin/a;b\n",
		"a file of the core":          good + "/var/lib/casaos/alerts.json\n",
		"a page of the core":          good + "/usr/share/casaos/www/index.html\n",
		"a configuration":             good + "/etc/casaos/gateway.ini\n",
		"a unit with another suffix":  good + "/usr/lib/systemd/system/x.timer\n",
		"a drop-in":                   good + "/usr/lib/systemd/system/x.service.d/o.conf\n",
	} {
		got, err := ParseFiles([]byte(raw))
		var bad *Invalid
		if err == nil || !errors.As(err, &bad) || got != nil {
			t.Errorf("%s: taken %v, %v", name, got, err)
		}
	}
}

func TestTheNumberAndTheSizeOfAListAreBounded(t *testing.T) {
	list := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "/usr/bin/f%d\n", i)
		}
		return b.String()
	}
	if got, err := ParseFiles([]byte(list(MaxEntries))); err != nil || len(got) != MaxEntries {
		t.Errorf("%d paths: %d, %v", MaxEntries, len(got), err)
	}
	_, err := ParseFiles([]byte(list(MaxEntries + 1)))
	var bad *Invalid
	if !errors.As(err, &bad) || bad.Reason != ReasonTooMany {
		t.Errorf("%d paths: %v", MaxEntries+1, err)
	}
	if _, err := ParseFiles([]byte(strings.Repeat(" ", MaxFilesBytes+1))); !errors.As(err, &bad) || bad.Reason != ReasonTooLarge {
		t.Errorf("a list larger than the bound: %v", err)
	}
	// the writer is as strict as the reader: it never writes what it would not read
	var paths []string
	for i := 0; i < MaxEntries+1; i++ {
		paths = append(paths, fmt.Sprintf("/usr/bin/f%d", i))
	}
	if _, err := FormatFilesList(entriesFor(paths[:MaxEntries])); err != nil {
		t.Errorf("writing %d paths: %v", MaxEntries, err)
	}
	if _, err := FormatFilesList(entriesFor(paths)); !errors.As(err, &bad) || bad.Reason != ReasonTooMany {
		t.Errorf("writing %d paths: %v", MaxEntries+1, err)
	}
	for _, p := range [][]string{{"/etc/passwd"}, {"/usr/bin/a", "/usr/bin/a"}, {"usr/bin/a"}, {"/usr/bin/a\n/usr/bin/b"}, {"/usr/bin/a\n"}} {
		if raw, err := FormatFilesList(entriesFor(p)); err == nil {
			t.Errorf("FormatFilesList(%q) = %q", p, raw)
		}
	}
}

// The bounds that the contract names, written as numbers: the tests above try each side of a bound through its name, which would move with it.
func TestTheBoundsAreWhereTheContractPutsThem(t *testing.T) {
	for name, c := range map[string][2]int64{
		"entries in an archive":    {MaxEntries, 5000},
		"bytes of files unpacked":  {MaxTotalBytes, 400 << 20},
		"bytes of a manifest":      {MaxBytes, 64 << 10},
		"bytes of a catalogue":     {MaxCatalogueBytes, 1 << 20},
		"modules in a catalogue":   {MaxCatalogueModules, 64},
		"bytes of a path":          {MaxPathBytes, 1024},
		"bytes of a name":          {MaxElementBytes, 255},
		"bytes of an archive":      {MaxArchiveBytes, 200 << 20},
		"bytes of a tar stream":    {MaxStreamBytes, 400<<20 + 64<<20},
		"bytes of an address":      {maxURLBytes, 512},
		"factor of the archive":    {DiskArchiveFactor, 3},
		"factor of the download":   {DiskAptDownloadFactor, 1},
		"factor of the extra disk": {DiskAptExtraFactor, 1},
	} {
		if c[0] != c[1] {
			t.Errorf("%s: %d, want %d", name, c[0], c[1])
		}
	}
}
