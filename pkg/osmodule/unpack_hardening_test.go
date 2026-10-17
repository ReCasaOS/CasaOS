package osmodule

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
)

// The rules of the unpack that keep an archive from reaching the core's own state, from making a tree of directories that nothing deletes, and from writing what it did not show: each is tried
// on both sides of its bound, and on the file system that is merged (where /lib is /usr/lib) as well as on one that is not.

// refusedAndNothingWritten unpacks the entries after a good file and wants the refusal, with nothing written, not even that file.
func refusedAndNothingWritten(t *testing.T, what, code string, dst *fakeFS, own []string, ents ...tent) {
	t.Helper()
	all := append([]tent{{name: "module.json", body: "{}"}, {name: sysroot + "usr/bin/fine", mode: 0o755, body: "fine"}}, ents...)
	res, err := unpack(t, dst, own, all...)
	wantRefusedFor(t, what, err, code)
	if len(dst.log) != 0 || len(res.Files) != 0 || len(res.Dirs) != 0 {
		t.Errorf("%s: something was written: %v %+v", what, dst.log, res)
	}
}

// seekHook is an archive that calls f with the number (from 1) of each seek to a place inside it, which is what the writing does for each file it copies and the checking does not.
func seekHook(data []byte, f func(n int, h *hooked)) *hooked {
	n := 0
	return &hooked{r: bytes.NewReader(data), onStart: func(int, *hooked) {}, onSeek: func(off int64, h *hooked) { n++; f(n, h) }}
}

func TestTheCoresOwnStateIsNotAPlaceAnArchiveMayWriteTo(t *testing.T) {
	for _, rel := range []string{
		"etc/casaos/modules", "etc/casaos/modules/virt.json", "etc/casaos/modules/a/b/c", "etc/casaos/Modules/virt.json", "etc/casaos/MODULES",
		"var/lib/casaos/modules", "var/lib/casaos/modules/virt.files", "var/lib/casaos/modules/tmp/archive", "var/lib/casaos/Modules/x.files",
		"var/lib/casaos/os-upgrade", "var/lib/casaos/os-upgrade/busy", "var/lib/casaos/os-upgrade/x/y", "var/lib/casaos/OS-Upgrade/busy",
		"usr/share/casaos/modules", "usr/share/casaos/modules/catalogue.json",
		"etc/casaos/os-upgrade.beta", "etc/casaos/OS-Upgrade.beta", "etc/casaos/os-upgrade.beta/x",
	} {
		for what, ent := range map[string]tent{
			"a file":      {name: sysroot + rel, body: "x"},
			"a link":      {name: sysroot + rel, typ: tar.TypeSymlink, link: "x"},
			"a directory": {name: sysroot + rel + "/", typ: tar.TypeDir},
		} {
			refusedAndNothingWritten(t, what+" "+rel, "unpack_path", newFS(), nil, ent)
		}
		if ValidFilePath("/" + rel) {
			t.Errorf("%s may be in the list of a module", rel)
		}
	}
	// and what is next to it is a place like another
	for _, rel := range []string{
		"etc/casaos/modulesx/a", "etc/casaos/module/a", "etc/casaos/os-upgrade.beta.sample", "etc/casaos/os-upgrade.betax", "etc/casaos/os-upgrade", "etc/casaos/virt/modules/a",
		"var/lib/casaos/os-upgradex/a", "var/lib/casaos/os-upgrad/a", "var/lib/casaos/modulesx", "var/lib/casaos/virt/os-upgrade/a", "var/lib/casaos/modules-x/a",
		"usr/share/casaos/modulesx/a", "usr/share/casaos/virt/modules/a",
	} {
		if _, err := unpack(t, newFS(), nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + rel, body: "x"}); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
		if !ValidFilePath("/" + rel) {
			t.Errorf("%s may not be in the list of a module", rel)
		}
	}
}

func TestADirectoryOfUnitsHoldsNothingButUnitsDirectlyInIt(t *testing.T) {
	for _, dir := range []string{"usr/lib/systemd/system", "lib/systemd/system"} {
		for what, ent := range map[string]tent{
			"a drop-in of a unit of the core":             {name: sysroot + dir + "/casaos-gateway.service.d/override.conf", body: "[Service]\n"},
			"a unit wanted by a target":                   {name: sysroot + dir + "/multi-user.target.wants/x.service", body: "x"},
			"a link in a directory of wants":              {name: sysroot + dir + "/multi-user.target.wants/x.service", typ: tar.TypeSymlink, link: "x.service"},
			"a unit in a directory":                       {name: sysroot + dir + "/sub/x.service", body: "x"},
			"a directory of drop-ins":                     {name: sysroot + dir + "/x.service.d/", typ: tar.TypeDir},
			"a directory of wants":                        {name: sysroot + dir + "/x.target.wants/", typ: tar.TypeDir},
			"a directory":                                 {name: sysroot + dir + "/sub/", typ: tar.TypeDir},
			"a link that is named like a drop-in":         {name: sysroot + dir + "/x.service.d", typ: tar.TypeSymlink, link: "y"},
			"a link that is named like a directory wants": {name: sysroot + dir + "/x.target.wants", typ: tar.TypeSymlink, link: "y"},
			"a link that is named like a requires":        {name: sysroot + dir + "/x.target.requires", typ: tar.TypeSymlink, link: "y"},
		} {
			refusedAndNothingWritten(t, dir+": "+what, "unpack_path", newFS(), nil, ent)
		}
		for what, ent := range map[string]tent{
			"a unit":                 {name: sysroot + dir + "/x.service", body: "[Service]\n"},
			"a template":             {name: sysroot + dir + "/x@.service", body: "[Service]\n"},
			"a timer":                {name: sysroot + dir + "/x.timer", body: "[Timer]\n"},
			"a link from unit to it": {name: sysroot + dir + "/alias.service", typ: tar.TypeSymlink, link: "x.service"},
		} {
			dst := newFS()
			if _, err := unpack(t, dst, nil, tent{name: "module.json", body: "{}"}, ent); err != nil {
				t.Errorf("%s: %s: %v", dir, what, err)
			}
		}
	}
}

func TestAPathIsAtMostMaxDepthNamesBelowTheAllowedDirectory(t *testing.T) {
	for _, dir := range AllowedDirs {
		if isUnitDir(dir) {
			continue // (nothing but the units directly in it)
		}
		deep := func(n int) string { return dir + "/" + strings.Repeat("d/", n-1) + "f" }
		for what, mk := range map[string]func(string) tent{
			"a file":      func(p string) tent { return tent{name: sysroot + p, body: "x"} },
			"a link":      func(p string) tent { return tent{name: sysroot + p, typ: tar.TypeSymlink, link: "x"} },
			"a directory": func(p string) tent { return tent{name: sysroot + p + "/", typ: tar.TypeDir} },
		} {
			if _, err := unpack(t, newFS(), nil, tent{name: "module.json", body: "{}"}, mk(deep(MaxDepth))); err != nil {
				t.Errorf("%s in %s, %d names deep: %v", what, dir, MaxDepth, err)
			}
			refusedAndNothingWritten(t, fmt.Sprintf("%s in %s, %d names deep", what, dir, MaxDepth+1), "unpack_path", newFS(), nil, mk(deep(MaxDepth+1)))
		}
		if !ValidFilePath("/"+deep(MaxDepth)) || ValidFilePath("/"+deep(MaxDepth+1)) {
			t.Errorf("%s: the depth of a path of a list", dir)
		}
	}
}

func TestEntriesOfDeepPathsOfOneLetterDirectoriesMakeNothing(t *testing.T) {
	ents := []tent{{name: "module.json", body: "{}"}}
	deep := strings.Repeat("a/", 500)
	for i := 0; i < MaxEntries-1; i++ {
		ents = append(ents, tent{name: fmt.Sprintf("%svar/lib/casaos/%sf%d", sysroot, deep, i)})
	}
	dst := newFS()
	res, err := unpack(t, dst, nil, ents...)
	wantRefused(t, err, "unpack_path")
	if len(dst.log) != 0 || len(res.Dirs) != 0 {
		t.Errorf("written %d", len(dst.log))
	}
}

func TestAnUnpackMakesAtMostMaxDirsDirectories(t *testing.T) {
	files := func(n int) []tent { // n files, each in a directory of its own that is not there
		ents := []tent{{name: "module.json", body: "{}"}}
		for i := 0; i < n; i++ {
			ents = append(ents, tent{name: fmt.Sprintf("%svar/lib/casaos/d%d/f", sysroot, i)})
		}
		return ents
	}
	dst := newFS()
	res, err := unpack(t, dst, nil, files(MaxDirs)...)
	if err != nil || len(res.Dirs) != MaxDirs {
		t.Errorf("%d directories: %d, %v", MaxDirs, len(res.Dirs), err)
	}
	dst = newFS()
	_, err = unpack(t, dst, nil, files(MaxDirs+1)...)
	wantRefused(t, err, "unpack_limits")
	if len(dst.log) != 0 {
		t.Errorf("written %d", len(dst.log))
	}
	// the directories of a file count, all of them: files in chains of 7 new directories
	chains := func(n int) []tent {
		ents := []tent{{name: "module.json", body: "{}"}}
		for i := 0; i < n; i++ {
			ents = append(ents, tent{name: fmt.Sprintf("%svar/lib/casaos/c%d/b/c/d/e/f/g/f", sysroot, i)})
		}
		return ents
	}
	if res, err := unpack(t, newFS(), nil, chains(MaxDirs/7)...); err != nil || len(res.Dirs) != MaxDirs/7*7 {
		t.Errorf("%d directories in chains: %d, %v", MaxDirs/7*7, len(res.Dirs), err)
	}
	_, err = unpack(t, newFS(), nil, chains(MaxDirs/7+1)...)
	wantRefused(t, err, "unpack_limits")
	// the directories that a directory entry names count too
	named := func(n int) []tent {
		ents := []tent{{name: "module.json", body: "{}"}}
		for i := 0; i < n; i++ {
			ents = append(ents, tent{name: fmt.Sprintf("%svar/lib/casaos/e%d/", sysroot, i), typ: tar.TypeDir})
		}
		return ents
	}
	if res, err := unpack(t, newFS(), nil, named(MaxDirs)...); err != nil || len(res.Dirs) != MaxDirs {
		t.Errorf("%d named directories: %d, %v", MaxDirs, len(res.Dirs), err)
	}
	_, err = unpack(t, newFS(), nil, named(MaxDirs+1)...)
	wantRefused(t, err, "unpack_limits")
	// a directory is counted once however many entries are in it, and one that is there is not counted
	many := []tent{{name: "module.json", body: "{}"}}
	for i := 0; i < 1000; i++ {
		many = append(many, tent{name: fmt.Sprintf("%svar/lib/casaos/a/b/c/f%d", sysroot, i)})
	}
	if res, err := unpack(t, newFS(), nil, many...); err != nil || len(res.Dirs) != 3 {
		t.Errorf("1000 files in one directory: %d directories, %v", len(res.Dirs), err)
	}
	dst = newFS()
	for i := 0; i < 300; i++ {
		dst.nodes[fmt.Sprintf("/var/lib/casaos/d%d", i)] = &node{mode: fs.ModeDir | 0o755}
	}
	if res, err := unpack(t, dst, nil, files(300+MaxDirs)...); err != nil || len(res.Dirs) != MaxDirs {
		t.Errorf("300 directories that are there and %d that are not: %d, %v", MaxDirs, len(res.Dirs), err)
	}
}

func TestTheDirectoriesAnUnpackMadeAreGivenDeepestFirstAndOnlyThose(t *testing.T) {
	dst := newFS()
	delete(dst.nodes, "/usr/share/casaos") // (the directory of the module's files is not there either)
	res, err := unpack(t, dst, nil, tent{name: "module.json", body: "{}"},
		tent{name: sysroot + "usr/share/casaos/virt/web/index.html", body: "x", mode: 0o700},
		tent{name: sysroot + "var/lib/casaos/virt/data/", typ: tar.TypeDir, mode: 0o750},
		tent{name: sysroot + "usr/bin/x", mode: 0o755, body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/share/casaos", "/usr/share/casaos/virt", "/usr/share/casaos/virt/web", "/var/lib/casaos/virt", "/var/lib/casaos/virt/data"}
	got := slices.Clone(res.Dirs)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("directories = %v", res.Dirs)
	}
	for i := 1; i < len(res.Dirs); i++ {
		if strings.Count(res.Dirs[i], "/") > strings.Count(res.Dirs[i-1], "/") {
			t.Errorf("not the deepest first: %v", res.Dirs)
		}
	}
	// exactly the ones that were made, each with its mode: 0755 on the way, the mode of the entry that names it
	var made []string
	for _, op := range dst.log {
		if p, ok := strings.CutPrefix(op, "mkdir "); ok {
			made = append(made, p)
		}
	}
	slices.Sort(made)
	if !slices.Equal(made, want) {
		t.Errorf("made %v", made)
	}
	for p, mode := range map[string]fs.FileMode{"/usr/share/casaos": 0o755, "/usr/share/casaos/virt": 0o755, "/usr/share/casaos/virt/web": 0o755, "/var/lib/casaos/virt": 0o755, "/var/lib/casaos/virt/data": 0o750} {
		if n := dst.nodes[p]; n == nil || n.mode != fs.ModeDir|mode {
			t.Errorf("%s: %+v, want %v", p, n, mode)
		}
	}
	if n := dst.nodes["/usr/share/casaos/virt/web/index.html"]; n == nil || n.mode != 0o700 {
		t.Errorf("the file: %+v", n)
	}
	// when the unpack stops half way the ones made so far are given
	dst = newFS()
	dst.failOn = "/usr/share/casaos/virt/index.html"
	res, err = unpack(t, dst, nil, goodEntries()...)
	if err == nil || len(res.Dirs) == 0 || !slices.Contains(res.Dirs, "/usr/share/casaos/virt") {
		t.Errorf("%v, directories %v", err, res.Dirs)
	}
	// a system where the directory of the units is not there, nor the two above it: they are made, and given, the deepest first
	dst = newFS()
	for _, p := range []string{"/usr/lib/systemd/system", "/usr/lib/systemd", "/usr/lib"} {
		delete(dst.nodes, p)
	}
	res, err = unpack(t, dst, nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/lib/systemd/system/x.service", body: "x"})
	if err != nil || !slices.Equal(res.Dirs, []string{"/usr/lib/systemd/system", "/usr/lib/systemd", "/usr/lib"}) || !slices.Equal(res.Files, []string{"/usr/lib/systemd/system/x.service"}) {
		t.Errorf("%v %+v", err, res)
	}
	if n := dst.nodes["/usr/lib/systemd"]; n == nil || n.mode != fs.ModeDir|0o755 {
		t.Errorf("the directory above: %+v", n)
	}
	// a refusal makes nothing
	dst = newFS()
	res, err = unpack(t, dst, nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "var/lib/casaos/a/b/f"}, tent{name: sysroot + "usr/sbin/x"})
	wantRefused(t, err, "unpack_path")
	if len(res.Dirs) != 0 || len(dst.log) != 0 {
		t.Errorf("%v %v", res.Dirs, dst.log)
	}
}

func TestADirectoryThatAppearsAfterTheCheckingIsNotOursToDelete(t *testing.T) {
	ents := []tent{{name: "module.json", body: "{}"}, {name: sysroot + "var/lib/casaos/a/b/f", body: "x"}}
	// somebody made one of the directories: the unpack goes on, and does not say it made it
	dst := newFS()
	dst.onMkdir = func(p string) {
		if p == "/var/lib/casaos/a" {
			dst.nodes[p] = &node{mode: fs.ModeDir | 0o700}
		}
	}
	res, err := unpack(t, dst, nil, ents...)
	if err != nil || !slices.Equal(res.Dirs, []string{"/var/lib/casaos/a/b"}) || !slices.Equal(res.Files, []string{"/var/lib/casaos/a/b/f"}) {
		t.Errorf("%v %+v", err, res)
	}
	// ...but not if it is a link, or a file, now
	for what, mk := range map[string]*node{"a link": {mode: fs.ModeSymlink | 0o777, target: "/etc"}, "a file": {mode: 0o644}} {
		dst = newFS()
		dst.onMkdir = func(p string) {
			if p == "/var/lib/casaos/a" {
				dst.nodes[p] = mk
			}
		}
		res, err = unpack(t, dst, nil, ents...)
		wantRefusedFor(t, what, err, "unpack_path")
		if len(res.Files) != 0 || dst.nodes["/var/lib/casaos/a/b"] != nil {
			t.Errorf("%s: %+v", what, res)
		}
	}
}

func TestAFileThatAppearsAfterTheLastCheckIsNotOverwrittenAndNotListed(t *testing.T) {
	good := tarOf(t, goodEntries()...)
	// the files are put down in this order: the program, the link to it, the sample of the configuration, the page; the unit is last
	order := []string{"/usr/bin/casaos-virt-management", "/usr/bin/virt", "/etc/casaos/virt-management.conf.sample", "/usr/share/casaos/virt/index.html", "/usr/lib/systemd/system/casaos-virt-management.service"}
	for _, c := range []struct {
		name  string
		at    int // the number of the file being copied, when the other file appears
		plant string
		own   []string
		kept  int // how many of the files in order are written before it
	}{
		{"the first file", 1, order[0], nil, 0},
		{"a file that is not the first", 2, order[2], nil, 2},
		{"the page", 3, order[3], nil, 3},
		{"the unit", 4, order[4], nil, 4},
		{"a file that the module listed and that was not there when it looked", 2, order[2], []string{order[2]}, 2},
	} {
		dst := newFS()
		res, err := Unpack(seekHook(good, func(n int, _ *hooked) {
			if n == c.at {
				dst.put(c.plant, 0o644, "foreign")
			}
		}), dst, c.own, nil)
		wantRefusedFor(t, c.name, err, "file_exists")
		if n := dst.nodes[c.plant]; n == nil || string(n.data) != "foreign" || n.mode != 0o644 {
			t.Errorf("%s: what is at %s is %+v", c.name, c.plant, n)
		}
		if !slices.Equal(res.Files, order[:c.kept]) {
			t.Errorf("%s: files = %v, want %v", c.name, res.Files, order[:c.kept])
		}
		if slices.Contains(res.Files, c.plant) {
			t.Errorf("%s: the foreign file is listed as the module's", c.name)
		}
		if !slices.Contains(res.Dirs, "/usr/share/casaos/virt") {
			t.Errorf("%s: directories = %v", c.name, res.Dirs)
		}
	}
	// a link that appears is in the way too
	dst := newFS()
	res, err := Unpack(seekHook(good, func(n int, _ *hooked) {
		if n == 1 {
			dst.nodes["/usr/bin/virt"] = &node{mode: fs.ModeSymlink | 0o777, target: "/etc/passwd"}
		}
	}), dst, nil, nil)
	wantRefused(t, err, "file_exists")
	if n := dst.nodes["/usr/bin/virt"]; n == nil || n.target != "/etc/passwd" || !slices.Equal(res.Files, order[:1]) {
		t.Errorf("link %+v, files %v", n, res.Files)
	}
	// a file of the module's own that is there is replaced all the same, and that is the only kind
	dst = newFS()
	dst.put(order[0], 0o755, "old")
	res, err = Unpack(bytes.NewReader(good), dst, []string{order[0]}, nil)
	if err != nil || len(res.Files) != 5 || string(dst.nodes[order[0]].data) == "old" {
		t.Errorf("an own file: %v %v", err, res.Files)
	}
}

func TestAFileWhoseBytesChangeAfterTheCheckingIsNotPutInPlace(t *testing.T) {
	good := tarOf(t, goodEntries()...)
	for _, c := range []struct {
		name, marker, path string
		at, before         int
	}{
		{"the program", "\x7fELF", "/usr/bin/casaos-virt-management", 1, 0},
		{"the page", "<html>", "/usr/share/casaos/virt/index.html", 3, 3},
		{"the unit", "[Service]", "/usr/lib/systemd/system/casaos-virt-management.service", 4, 4},
	} {
		changed := slices.Clone(good)
		at := bytes.Index(changed, []byte(c.marker))
		if at < 0 {
			t.Fatalf("%s: no %q", c.name, c.marker)
		}
		changed[at+1]++ // the same size, and not the same
		dst := newFS()
		res, err := Unpack(seekHook(good, func(n int, h *hooked) {
			if n == c.at {
				h.r = bytes.NewReader(changed)
			}
		}), dst, nil, nil)
		wantRefusedFor(t, c.name, err, "unpack_corrupt")
		if dst.nodes[c.path] != nil || !slices.Contains(dst.log, "abort "+c.path) || slices.Contains(res.Files, c.path) || len(res.Files) != c.before {
			t.Errorf("%s: log %v, files %v", c.name, dst.log, res.Files)
		}
	}
	// the whole of the sum is compared: bytes of the same size whose sum starts as the sum of the real ones are not the real ones
	page := []byte("<html></html>")
	var forged []byte
	for a := 0; a < 256 && forged == nil; a++ {
		for b := 0; b < 256; b++ {
			alt := slices.Clone(page)
			alt[1], alt[2] = byte(a), byte(b)
			if !bytes.Equal(alt, page) && sha256.Sum256(alt)[0] == sha256.Sum256(page)[0] {
				forged = alt
				break
			}
		}
	}
	forgedArchive := bytes.Replace(good, page, forged, 1)
	if forged == nil || len(forgedArchive) != len(good) || bytes.Equal(forgedArchive, good) {
		t.Fatal("no bytes of the same size and of the same start of the sum")
	}
	dst := newFS()
	_, err := Unpack(seekHook(good, func(n int, h *hooked) {
		if n == 3 {
			h.r = bytes.NewReader(forgedArchive)
		}
	}), dst, nil, nil)
	wantRefused(t, err, "unpack_corrupt")
	// the same bytes every time is what is written
	dst = newFS()
	if _, err := Unpack(seekHook(good, func(int, *hooked) {}), dst, nil, nil); err != nil {
		t.Errorf("%v", err)
	}
}

func TestAnArchiveThatEndsWhereAnEntryEndsHasNoEndAndIsRefused(t *testing.T) {
	good := tarOf(t, goodEntries()...)
	onlyManifest := tarOf(t, tent{name: "module.json", body: "{}"})
	for name, raw := range map[string][]byte{
		"no marker":                   good[:len(good)-1024],
		"one block of the marker":     good[:len(good)-512],
		"a block and a part":          good[:len(good)-512+100],
		"a part of a block":           good[:len(good)-1024+100],
		"the manifest and no marker":  onlyManifest[:len(onlyManifest)-1024],
		"the manifest and one block":  onlyManifest[:len(onlyManifest)-512],
		"a marker of a block, a byte": append(good[:len(good)-512:len(good)-512], 1),
	} {
		dst := newFS()
		res, err := Unpack(bytes.NewReader(raw), dst, nil, nil)
		wantRefusedFor(t, name, err, "unpack_corrupt")
		if len(dst.log) != 0 || len(res.Files) != 0 {
			t.Errorf("%s: written %v", name, dst.log)
		}
	}
	// the marker, and what a tar is padded with after it, are an end
	for name, raw := range map[string][]byte{
		"the marker":             good,
		"a padded archive":       append(slices.Clone(good), make([]byte, 10240-len(good)%10240)...),
		"the manifest alone":     onlyManifest,
		"bytes after an end":     append(slices.Clone(good), []byte("whatever comes after the end")...),
		"a long file at the end": tarOf(t, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/bin/big", body: strings.Repeat("b", 1500)}),
	} {
		if _, err := Unpack(bytes.NewReader(raw), newFS(), nil, nil); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAnArchiveWithBothSpellingsOfALibDirectoryWritesNothing(t *testing.T) {
	for name, ents := range map[string][]tent{
		"a file twice":                    {{name: sysroot + "lib/systemd/system/a.service", body: "1"}, {name: sysroot + "usr/lib/systemd/system/a.service", body: "2"}},
		"a file twice, the other":         {{name: sysroot + "usr/lib/systemd/system/a.service", body: "2"}, {name: sysroot + "lib/systemd/system/a.service", body: "1"}},
		"a file and a link":               {{name: sysroot + "lib/systemd/system/a.service", body: "1"}, {name: sysroot + "usr/lib/systemd/system/a.service", typ: tar.TypeSymlink, link: "b.service"}},
		"a link and a file":               {{name: sysroot + "lib/systemd/system/a.service", typ: tar.TypeSymlink, link: "b.service"}, {name: sysroot + "usr/lib/systemd/system/a.service", body: "2"}},
		"a file between others":           {{name: sysroot + "usr/lib/systemd/system/b.service", body: "b"}, {name: sysroot + "lib/systemd/system/a.service", body: "1"}, {name: sysroot + "usr/bin/p", body: "p"}, {name: sysroot + "usr/lib/systemd/system/a.service", body: "2"}},
		"a name that differs in the case": {{name: sysroot + "lib/systemd/system/a.service", body: "1"}, {name: sysroot + "usr/lib/systemd/system/A.service", body: "2"}},
	} {
		refusedAndNothingWritten(t, name, "unpack_path", newFS(), nil, ents...)
		refusedAndNothingWritten(t, name+", merged", "unpack_path", newMergedFS(), nil, ents...)
	}
	// two units, one under each name, are two files; and the directories above them, named in both spellings, are no clash
	ents := []tent{
		{name: "module.json", body: "{}"},
		{name: sysroot + "lib/", typ: tar.TypeDir}, {name: sysroot + "lib/systemd/", typ: tar.TypeDir}, {name: sysroot + "usr/", typ: tar.TypeDir}, {name: sysroot + "usr/lib/", typ: tar.TypeDir},
		{name: sysroot + "usr/lib/systemd/", typ: tar.TypeDir},
		{name: sysroot + "lib/systemd/system/a.service", body: "1"}, {name: sysroot + "usr/lib/systemd/system/b.service", body: "2"},
	}
	if res, err := unpack(t, newFS(), nil, ents...); err != nil || len(res.Files) != 2 {
		t.Errorf("%v %v", err, res.Files)
	}
}

func TestOnASystemWithAMergedUsrTheTwoNamesOfALibFileAreTheOneFile(t *testing.T) {
	const lib, usr = "/lib/systemd/system/a.service", "/usr/lib/systemd/system/a.service"
	for _, c := range []struct{ name, listed, written string }{
		{"listed with lib, now written with usr/lib", lib, usr},
		{"listed with usr/lib, now written with lib", usr, lib},
		{"listed and written the same way", usr, usr},
		{"listed and written the same way, lib", lib, lib},
	} {
		dst := newMergedFS()
		dst.put(lib, 0o644, "old") // (it is the same file as the other name)
		res, err := unpack(t, dst, []string{c.listed}, tent{name: "module.json", body: "{}"}, tent{name: sysroot + strings.TrimPrefix(c.written, "/"), body: "new"})
		if err != nil || !slices.Equal(res.Files, []string{c.written}) || string(dst.nodes[usr].data) != "new" {
			t.Errorf("%s: %v %v", c.name, err, res.Files)
		}
		// a file that is somebody else's is not the module's under either name
		dst = newMergedFS()
		dst.put(lib, 0o644, "theirs")
		_, err = unpack(t, dst, []string{"/lib/systemd/system/b.service"}, tent{name: "module.json", body: "{}"}, tent{name: sysroot + strings.TrimPrefix(c.written, "/"), body: "new"})
		wantRefusedFor(t, c.name+", not listed", err, "file_exists")
		if string(dst.nodes[usr].data) != "theirs" || len(dst.log) != 0 {
			t.Errorf("%s: written %v", c.name, dst.log)
		}
		_, err = unpack(t, dst, nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + strings.TrimPrefix(c.written, "/"), body: "new"})
		wantRefusedFor(t, c.name+", nothing listed", err, "file_exists")
	}
	// a link of the unit directory is the same
	dst := newMergedFS()
	dst.nodes[usr] = &node{mode: fs.ModeSymlink | 0o777, target: "old.service"}
	res, err := unpack(t, dst, []string{lib}, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/lib/systemd/system/a.service", typ: tar.TypeSymlink, link: "b.service"})
	if err != nil || dst.nodes[usr].target != "b.service" || !slices.Equal(res.Files, []string{usr}) {
		t.Errorf("a link: %v %v", err, res.Files)
	}
	dst = newMergedFS()
	dst.nodes[usr] = &node{mode: fs.ModeSymlink | 0o777, target: "old.service"}
	res, err = unpack(t, dst, []string{usr}, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "lib/systemd/system/a.service", typ: tar.TypeSymlink, link: "b.service"})
	if err != nil || dst.nodes[usr].target != "b.service" || !slices.Equal(res.Files, []string{lib}) {
		t.Errorf("a link, the other way: %v %v", err, res.Files)
	}
	// a system whose /lib is a directory of its own has two directories: what the module owns under the one is not what is under the other
	dst = newFS()
	dst.put(usr, 0o644, "theirs")
	_, err = unpack(t, dst, []string{lib}, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/lib/systemd/system/a.service", body: "new"})
	wantRefused(t, err, "file_exists")
	if string(dst.nodes[usr].data) != "theirs" || len(dst.log) != 0 {
		t.Errorf("written %v", dst.log)
	}
	// a /lib that cannot be looked at is not a refusal of the archive, and the archive is not taken for one that has no link there
	dst = newFS()
	dst.onLstat = func(n int) { // (the first thing looked at is /lib)
		dst.lstatEr = nil
		if n == 1 {
			dst.lstatEr = errors.New("input/output error")
		}
	}
	_, err = unpack(t, dst, nil, goodEntries()...)
	var r *Refused
	if err == nil || errors.As(err, &r) || len(dst.log) != 0 {
		t.Errorf("%v %v", err, dst.log)
	}
}

func TestAnythingAboveTheAllowedDirectoryIsTheSystemsOwnEvenALink(t *testing.T) {
	for _, above := range []string{"/usr/share", "/usr/lib/systemd", "/var/lib"} {
		dst := newFS()
		dst.nodes[above] = &node{mode: fs.ModeSymlink | 0o777, target: "elsewhere"}
		if _, err := unpack(t, dst, nil, goodEntries()...); err != nil {
			t.Errorf("%s is a link: %v", above, err)
		}
	}
}

func TestTheModulesOwnLinkIsReplacedByALinkAndNothingElseIs(t *testing.T) {
	ents := []tent{{name: "module.json", body: "{}"}, {name: sysroot + "usr/bin/virt", typ: tar.TypeSymlink, link: "new"}}
	own := []string{"/usr/bin/virt"}
	dst := newFS()
	dst.nodes["/usr/bin/virt"] = &node{mode: fs.ModeSymlink | 0o777, target: "old"}
	res, err := unpack(t, dst, own, ents...)
	if err != nil || dst.nodes["/usr/bin/virt"].target != "new" || !slices.Equal(res.Files, own) {
		t.Errorf("own link: %v %v %+v", err, res.Files, dst.nodes["/usr/bin/virt"])
	}
	// a link that is not the module's is in the way, listed or not
	for name, own := range map[string][]string{"nothing listed": nil, "another file listed": {"/usr/bin/other"}} {
		dst = newFS()
		dst.nodes["/usr/bin/virt"] = &node{mode: fs.ModeSymlink | 0o777, target: "/etc/passwd"}
		_, err = unpack(t, dst, own, ents...)
		wantRefusedFor(t, name, err, "unpack_path")
		if len(dst.log) != 0 || dst.nodes["/usr/bin/virt"].target != "/etc/passwd" {
			t.Errorf("%s: written %v", name, dst.log)
		}
	}
	// a link the module owns is not made a file, and not a directory
	for name, ent := range map[string]tent{"a file": {name: sysroot + "usr/bin/virt", body: "x"}, "a directory": {name: sysroot + "usr/bin/virt/", typ: tar.TypeDir}} {
		dst = newFS()
		dst.nodes["/usr/bin/virt"] = &node{mode: fs.ModeSymlink | 0o777, target: "old"}
		_, err = unpack(t, dst, own, tent{name: "module.json", body: "{}"}, ent)
		wantRefusedFor(t, name, err, "unpack_path")
		if len(dst.log) != 0 {
			t.Errorf("%s: written %v", name, dst.log)
		}
	}
	// the module's own file is replaced by a link, as before
	dst = newFS()
	dst.put("/usr/bin/virt", 0o755, "a program")
	if _, err = unpack(t, dst, own, ents...); err != nil || dst.nodes["/usr/bin/virt"].target != "new" {
		t.Errorf("a file made a link: %v", err)
	}
}

func TestADirectoryOnTheWayIsMadeWorldReadableWhateverTheModeOfWhatIsInIt(t *testing.T) {
	dst := newFS()
	_, err := unpack(t, dst, nil, tent{name: "module.json", body: "{}"},
		tent{name: sysroot + "var/lib/casaos/a/b/", typ: tar.TypeDir, mode: 0o700},
		tent{name: sysroot + "var/lib/casaos/c/d/f", mode: 0o600, body: "x"},
		tent{name: sysroot + "var/lib/casaos/e/f/", typ: tar.TypeDir, mode: 0o700}, tent{name: sysroot + "var/lib/casaos/e/", typ: tar.TypeDir, mode: 0o750})
	if err != nil {
		t.Fatal(err)
	}
	for p, mode := range map[string]fs.FileMode{
		"/var/lib/casaos/a": 0o755, "/var/lib/casaos/a/b": 0o700, "/var/lib/casaos/c": 0o755, "/var/lib/casaos/c/d": 0o755,
		"/var/lib/casaos/e": 0o750, "/var/lib/casaos/e/f": 0o700, // (a directory that is named after what is in it has the mode it is given)
	} {
		if n := dst.nodes[p]; n == nil || n.mode != fs.ModeDir|mode {
			t.Errorf("%s: %+v, want %v", p, n, mode)
		}
	}
}

func TestTheFilesThatMayBeWrittenAreGivenBeforeAnythingIsWritten(t *testing.T) {
	dst := newFS()
	var calls [][]string
	var seen []int
	res, err := Unpack(bytes.NewReader(tarOf(t, goodEntries()...)), dst, nil, func(paths []string) error {
		calls = append(calls, slices.Clone(paths))
		seen = append(seen, len(dst.log), len(dst.nodes))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !slices.Equal(calls[0], res.Files) {
		t.Errorf("planned %v, written %v", calls, res.Files)
	}
	if seen[0] != 0 || seen[1] != len(newFS().nodes) {
		t.Errorf("something was there already: %v", seen)
	}
	// the unit is the last of them
	if last := calls[0][len(calls[0])-1]; last != "/usr/lib/systemd/system/casaos-virt-management.service" {
		t.Errorf("last = %s", last)
	}
	// the list can be kept as it is
	if raw, err := FormatFiles(calls[0]); err != nil {
		t.Errorf("%v", err)
	} else if back, err := ParseFiles(raw); err != nil || !slices.Equal(back, calls[0]) {
		t.Errorf("%v %v", back, err)
	}
	// a configuration that is there stays, and is named only when it was the module's
	ents := []tent{
		{name: "module.json", body: "{}"},
		{name: sysroot + "etc/casaos/mine.conf", body: "1"}, {name: sysroot + "etc/casaos/theirs.conf", body: "1"}, {name: sysroot + "usr/bin/p", mode: 0o755, body: "p"},
	}
	dst = newFS()
	dst.put("/etc/casaos/mine.conf", 0o600, "edited")
	dst.put("/etc/casaos/theirs.conf", 0o600, "edited")
	calls = nil
	res, err = Unpack(bytes.NewReader(tarOf(t, ents...)), dst, []string{"/etc/casaos/mine.conf"}, func(paths []string) error { calls = append(calls, paths); return nil })
	if err != nil || len(calls) != 1 || !slices.Equal(calls[0], []string{"/etc/casaos/mine.conf", "/usr/bin/p"}) || !slices.Equal(res.Files, calls[0]) {
		t.Errorf("%v: planned %v, written %v", err, calls, res.Files)
	}
	// a stop half way leaves a list that has what was not written too, and that is all of what was
	dst = newFS()
	dst.failOn = "/usr/share/casaos/virt/index.html"
	calls = nil
	res, err = Unpack(bytes.NewReader(tarOf(t, goodEntries()...)), dst, nil, func(paths []string) error { calls = append(calls, paths); return nil })
	if err == nil || len(calls) != 1 || len(calls[0]) != 5 || len(res.Files) >= 5 {
		t.Fatalf("%v planned %v written %v", err, calls, res.Files)
	}
	for _, p := range res.Files {
		if !slices.Contains(calls[0], p) {
			t.Errorf("%s was written and not planned", p)
		}
	}
}

func TestIfTheFilesCannotBeRecordedNothingIsWritten(t *testing.T) {
	boom := errors.New("no room for the list")
	dst := newFS()
	res, err := Unpack(bytes.NewReader(tarOf(t, goodEntries()...)), dst, nil, func([]string) error { return boom })
	var r *Refused
	if !errors.Is(err, boom) || errors.As(err, &r) {
		t.Errorf("error = %v", err)
	}
	if len(dst.log) != 0 || len(res.Files) != 0 || len(res.Dirs) != 0 {
		t.Errorf("written %v, %+v", dst.log, res)
	}
	// an archive that is refused is not recorded
	called := false
	_, err = Unpack(bytes.NewReader(tarOf(t, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/sbin/x"})), newFS(), nil, func([]string) error { called = true; return nil })
	wantRefused(t, err, "unpack_path")
	if called {
		t.Error("recorded an archive that is refused")
	}
	// and nor is one that only gets validated
	if _, err := ValidateArchive(bytes.NewReader(tarOf(t, goodEntries()...)), newFS(), nil); err != nil {
		t.Errorf("%v", err)
	}
}

func TestAConfigurationThatIsNotAFileIsInTheWayAndIsNotKept(t *testing.T) {
	for what, n := range map[string]*node{
		"a directory": {mode: fs.ModeDir | 0o755},
		"a fifo":      {mode: fs.ModeNamedPipe | 0o600},
		"a device":    {mode: fs.ModeDevice | fs.ModeCharDevice | 0o600},
	} {
		dst := newFS()
		dst.nodes["/etc/casaos/virt.conf"] = n
		_, err := unpack(t, dst, []string{"/etc/casaos/virt.conf"}, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "etc/casaos/virt.conf", body: "x"})
		wantRefusedFor(t, what, err, "file_exists")
		if len(dst.log) != 0 {
			t.Errorf("%s: written %v", what, dst.log)
		}
	}
}
