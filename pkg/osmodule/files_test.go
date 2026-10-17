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
		"/usr/share/casaos/virt/web/index.html",
		"/var/lib/casaos/virt/state.db",
		"/lib/systemd/system/a@.service",
		"/usr/lib/systemd/system/casaos-virt-management.service",
	}
	raw, err := FormatFiles(paths)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(paths, "\n") + "\n"; string(raw) != want {
		t.Errorf("content = %q", raw)
	}
	back, err := ParseFiles(raw)
	if err != nil || !slices.Equal(back, paths) {
		t.Errorf("read back %v, %v", back, err)
	}
	// nothing at all is a list of nothing, both ways
	if got, err := ParseFiles(nil); err != nil || len(got) != 0 {
		t.Errorf("an empty list: %v %v", got, err)
	}
	if raw, err := FormatFiles(nil); err != nil || len(raw) != 0 {
		t.Errorf("a list of nothing: %q %v", raw, err)
	}
}

func TestAPathIsInTheListOnlyIfItIsInsideAnAllowedDirectory(t *testing.T) {
	for _, dir := range AllowedDirs {
		for _, c := range []struct {
			path string
			ok   bool
		}{
			{"/" + dir + "/file", true},
			{"/" + dir + "/sub/dir/file", true},
			{"/" + dir, false},       // the directory itself is not a file
			{"/" + dir + "/", false}, // nor with its slash
			{"/" + dir + "x/file", false},
			{"/" + dir[:len(dir)-1] + "/file", false},
			{"/" + dir[:strings.LastIndex(dir, "/")+1] + "file", false}, // the parent of the directory
			{"/" + strings.ToUpper(dir) + "/file", false},
			{dir + "/file", false}, // relative
			{"/" + dir + "/../../etc/passwd", false},
			{"/" + dir + "/./file", false},
			{"/" + dir + "//file", false},
			{"//" + dir + "/file", false},
			{"/" + dir + "/file/", false},
			{"/" + dir + "/..", false},
			{"/" + dir + "/...", false},
			{"/" + dir + "/fi le", false},
			{"/" + dir + "/fi\tle", false},
			{"/" + dir + "/fi\\le", false},
			{"/" + dir + "/fi\nle", false},
			{"/" + dir + "/fi\x00le", false},
			{"/" + dir + "/fi\rle", false},
			{"/" + dir + "/fi*le", false},
			{"/" + dir + "/fi$le", false},
			{"/" + dir + "/fi;le", false},
			{"/" + dir + "/café", false},
			{"/" + dir + "/café", false},
			{"/" + dir + "/‮file", false},
			{"/" + dir + "/" + strings.Repeat("a", 255), true},
			{"/" + dir + "/" + strings.Repeat("a", 256), false},
		} {
			if got := ValidFilePath(c.path); got != c.ok {
				t.Errorf("ValidFilePath(%q) = %v", c.path, got)
			}
		}
	}
	// where the bound of a whole path is
	long := func(n int) string { // a path of n bytes after the first slash, in names of at most 100
		rel := AllowedDirs[0] + "/"
		for len(rel) < n {
			chunk := n - len(rel)
			switch {
			case chunk == 101:
				chunk = 99
			case chunk > 100:
				chunk = 100
			}
			rel += strings.Repeat("a", chunk)
			if len(rel) < n {
				rel += "/"
			}
		}
		return "/" + rel
	}
	if p := long(MaxPathBytes); !ValidFilePath(p) {
		t.Errorf("a path of %d bytes: %q", MaxPathBytes, p[len(p)-5:])
	}
	if p := long(MaxPathBytes + 1); ValidFilePath(p) {
		t.Errorf("a path of %d bytes", MaxPathBytes+1)
	}
	for _, p := range []string{"", "/", "usr/bin/x", "/etc/passwd", "/usr/sbin/x", "/usr/lib/x", "/usr/lib/systemd/x", "/lib/x", "/var/lib/x", "/var/lib/casaosx/y", "/usr/share/x"} {
		if ValidFilePath(p) {
			t.Errorf("ValidFilePath(%q)", p)
		}
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
			fmt.Fprintf(&b, "/var/lib/casaos/f%d\n", i)
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
		paths = append(paths, fmt.Sprintf("/var/lib/casaos/f%d", i))
	}
	if _, err := FormatFiles(paths[:MaxEntries]); err != nil {
		t.Errorf("writing %d paths: %v", MaxEntries, err)
	}
	if _, err := FormatFiles(paths); !errors.As(err, &bad) || bad.Reason != ReasonTooMany {
		t.Errorf("writing %d paths: %v", MaxEntries+1, err)
	}
	for _, p := range [][]string{{"/etc/passwd"}, {"/usr/bin/a", "/usr/bin/a"}, {"usr/bin/a"}, {"/usr/bin/a\n/usr/bin/b"}, {"/usr/bin/a\n"}} {
		if raw, err := FormatFiles(p); err == nil {
			t.Errorf("FormatFiles(%q) = %q", p, raw)
		}
	}
}
