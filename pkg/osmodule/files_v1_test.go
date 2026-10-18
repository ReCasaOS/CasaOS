package osmodule

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
)

// entriesFor makes the entries of a list for some paths: each is a file that holds the sha256 of its own name.
func entriesFor(paths []string) []FileEntry {
	out := make([]FileEntry, len(paths))
	for i, p := range paths {
		sum := sha256.Sum256([]byte(p))
		out[i] = FileEntry{Path: p, Want: []string{hex.EncodeToString(sum[:])}}
	}
	return out
}

// entriesOf are the entries of the list for what an unpack wrote.
func entriesOf(res Result) []FileEntry {
	out := make([]FileEntry, len(res.Files))
	for i, p := range res.Files {
		w := res.Want[p]
		out[i] = FileEntry{Path: p, Link: w.Link, Want: []string{w.Value}}
	}
	return out
}

const (
	sumA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sumB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestAListThatRecordsTheContentsIsReadAndWrittenBack(t *testing.T) {
	entries := []FileEntry{
		{Path: "/usr/bin/casaos-virt-management", Want: []string{sumA}},
		{Path: "/usr/bin/virt", Link: true, Want: []string{"casaos-virt-management"}},
		{Path: "/etc/casaos/virt-management.conf.sample", Want: []string{sumA, sumB}},
		{Path: "/usr/lib/systemd/system/casaos-virt-management.service", Want: []string{sumB}},
	}
	raw, err := FormatFilesList(entries)
	if err != nil {
		t.Fatal(err)
	}
	want := FilesHeader + "\nf " + sumA + " /usr/bin/casaos-virt-management\nl casaos-virt-management /usr/bin/virt\nf " + sumA + "," + sumB + " /etc/casaos/virt-management.conf.sample\nf " + sumB + " /usr/lib/systemd/system/casaos-virt-management.service\n"
	if string(raw) != want {
		t.Errorf("the content:\n%s\nwant\n%s", raw, want)
	}
	back, err := ParseFilesList(raw)
	if err != nil || !back.Verified || len(back.Entries) != len(entries) {
		t.Fatalf("read back %+v, %v", back, err)
	}
	for i, e := range back.Entries {
		if e.Path != entries[i].Path || e.Link != entries[i].Link || !slices.Equal(e.Want, entries[i].Want) {
			t.Errorf("entry %d: %+v, want %+v", i, e, entries[i])
		}
	}
}

// A list of the earlier format is read, as a list that names paths and cannot be checked
func TestAListOfTheEarlierFormatIsUnverified(t *testing.T) {
	l, err := ParseFilesList([]byte("/usr/bin/a\n/usr/bin/b\n"))
	if err != nil || l.Verified || !slices.Equal(l.Paths(), []string{"/usr/bin/a", "/usr/bin/b"}) {
		t.Fatalf("%+v %v", l, err)
	}
	for _, e := range l.Entries {
		if len(e.Want) != 0 {
			t.Errorf("an entry of the earlier format that says what it holds: %+v", e)
		}
	}
	l, err = ParseFilesList([]byte(FilesHeader + "\nf " + sumA + " /usr/bin/a\n"))
	if err != nil || !l.Verified {
		t.Errorf("the new format is verified: %+v %v", l, err)
	}
}

func TestAListThatRecordsTheContentsIsStrict(t *testing.T) {
	const h = FilesHeader + "\n"
	good := "f " + sumA + " /usr/bin/a\n"
	// a first line alone is a list that was cut short, and is said so
	var cut *Invalid
	if _, err := ParseFilesList([]byte(h)); !errors.As(err, &cut) || cut.Reason != ReasonMissing {
		t.Errorf("a first line alone: %v", err)
	}
	for name, raw := range map[string]string{
		"nothing after the first line":    h,
		"a first line only, no new line":  FilesHeader,
		"the first line twice":            h + h + good,
		"another version":                 "casaos-module-files 2\n" + good,
		"a first line with a space after": FilesHeader + " \n" + good,
		"the first line after an entry":   good + h + good,
		"no new line at the end":          h + good[:len(good)-1],
		"a blank line":                    h + good + "\n",
		"a return":                        h + "f " + sumA + " /usr/bin/a\r\n",
		"a kind that is not one":          h + "x " + sumA + " /usr/bin/a\n",
		"a kind in capitals":              h + "F " + sumA + " /usr/bin/a\n",
		"no content":                      h + "f /usr/bin/a\n",
		"two spaces":                      h + "f  " + sumA + " /usr/bin/a\n",
		"a space at the end":              h + good[:len(good)-1] + " \n",
		"a tab":                           h + "f\t" + sumA + " /usr/bin/a\n",
		"a sum in capitals":               h + "f " + strings.ToUpper(sumA) + " /usr/bin/a\n",
		"a sum that is short":             h + "f " + sumA[:63] + " /usr/bin/a\n",
		"a sum that is long":              h + "f " + sumA + "a /usr/bin/a\n",
		"a sum that is not hex":           h + "f " + strings.Repeat("g", 64) + " /usr/bin/a\n",
		"three contents":                  h + "f " + sumA + "," + sumB + "," + sumA + " /usr/bin/a\n",
		"the same content twice":          h + "f " + sumA + "," + sumA + " /usr/bin/a\n",
		"an empty content":                h + "f " + sumA + ", /usr/bin/a\n",
		"a link with a sum that is short": h + "l  /usr/bin/a\n",
		"a link that goes up":             h + "l ../x /usr/bin/a\n",
		"a link that is absolute":         h + "l /etc/passwd /usr/bin/a\n",
		"a link with a space":             h + "l a%20b /usr/bin/a\n",
		"a link with an empty part":       h + "l a//b /usr/bin/a\n",
		"a path that is not valid":        h + "f " + sumA + " /etc/passwd\n",
		"a relative path":                 h + "f " + sumA + " usr/bin/a\n",
		"a path twice":                    h + good + good,
		"a path twice with another kind":  h + good + "l x /usr/bin/a\n",
		"a path with a space":             h + "f " + sumA + " /usr/bin/a b\n",
		"a comment":                       h + "# x\n" + good,
		"a byte order mark":               "\xef\xbb\xbf" + h + good,
		"a path of the core":              h + "f " + sumA + " /var/lib/casaos/alerts.json\n",
	} {
		l, err := ParseFilesList([]byte(raw))
		var bad *Invalid
		if err == nil || !errors.As(err, &bad) || l.Entries != nil || l.Verified {
			t.Errorf("%s: taken %+v, %v", name, l, err)
		}
		if paths, err := ParseFiles([]byte(raw)); err == nil || paths != nil {
			t.Errorf("%s: ParseFiles takes it: %v", name, paths)
		}
	}
}

func TestTheWriterOfTheListIsAsStrictAsItsReader(t *testing.T) {
	ok := FileEntry{Path: "/usr/bin/a", Want: []string{sumA}}
	if _, err := FormatFilesList([]FileEntry{ok}); err != nil {
		t.Fatal(err)
	}
	for name, es := range map[string][]FileEntry{
		"nothing":                 nil,
		"no content":              {{Path: "/usr/bin/a"}},
		"three contents":          {{Path: "/usr/bin/a", Want: []string{sumA, sumB, sumA}}},
		"the same twice":          {{Path: "/usr/bin/a", Want: []string{sumA, sumA}}},
		"a sum of the wrong kind": {{Path: "/usr/bin/a", Want: []string{"target"}}},
		"a path outside":          {{Path: "/etc/passwd", Want: []string{sumA}}},
		"a path twice":            {ok, ok},
		"a link that goes up":     {{Path: "/usr/bin/a", Link: true, Want: []string{"../x"}}},
	} {
		if raw, err := FormatFilesList(es); err == nil {
			t.Errorf("%s: written %q", name, raw)
		}
	}
	// (a link whose target is a sum is a link: the kind decides what a content is)
	if _, err := FormatFilesList([]FileEntry{{Path: "/usr/bin/a", Link: true, Want: []string{sumA}}}); err != nil {
		t.Errorf("a link whose name looks like a sum: %v", err)
	}
}

// the longest list there can be is read: the bound of the size is above the longest line of every entry
func TestTheLongestListThereCanBeIsRead(t *testing.T) {
	long := strings.Repeat("a", MaxElementBytes-len(sampleSuffix)) + sampleSuffix
	target := strings.Repeat("t", MaxElementBytes)
	var entries []FileEntry
	for i := 0; i < MaxEntries; i++ {
		// (a path of the most bytes: the directory of the configurations, then a name of the most bytes, and the entries differ in the first characters)
		name := []byte(long)
		name[0], name[1], name[2] = byte('a'+i%26), byte('a'+i/26%26), byte('a'+i/676%26)
		entries = append(entries, FileEntry{Path: "/etc/casaos/" + string(name), Link: true, Want: []string{target, target[1:] + "u"}})
	}
	raw, err := FormatFilesList(entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > MaxFilesBytes {
		t.Fatalf("the longest list, %d bytes, is above the bound %d", len(raw), MaxFilesBytes)
	}
	if l, err := ParseFilesList(raw); err != nil || len(l.Entries) != MaxEntries {
		t.Errorf("%d entries, %v", len(l.Entries), err)
	}
	var bad *Invalid
	if _, err := ParseFilesList([]byte(FilesHeader + "\n" + strings.Repeat(" ", MaxFilesBytes))); !errors.As(err, &bad) || bad.Reason != ReasonTooLarge {
		t.Errorf("a list above the bound: %v", err)
	}
	if _, err := ParseFilesList(append([]byte(FilesHeader+"\n"), []byte(strings.Repeat("f "+sumA+" /usr/bin/a\n", MaxEntries+1))...)); !errors.As(err, &bad) {
		t.Errorf("more entries than there can be: %v", err)
	}
}
