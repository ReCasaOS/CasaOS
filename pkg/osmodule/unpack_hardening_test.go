package osmodule

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	// everything the core keeps in /var/lib/casaos, /usr/share/casaos and /etc/casaos: no archive writes to any of it, whatever it puts there and under whatever spelling
	for _, rel := range []string{
		"etc/casaos/modules", "etc/casaos/modules/virt.json", "etc/casaos/modules/a/b/c", "etc/casaos/Modules/virt.json", "etc/casaos/MODULES",
		"var/lib/casaos/modules", "var/lib/casaos/modules/virt.files", "var/lib/casaos/modules/tmp/archive", "var/lib/casaos/Modules/x.files",
		"var/lib/casaos/os-upgrade", "var/lib/casaos/os-upgrade/busy", "var/lib/casaos/os-upgrade/x/y", "var/lib/casaos/OS-Upgrade/busy",
		"usr/share/casaos/modules", "usr/share/casaos/modules/catalogue.json",
		"etc/casaos/os-upgrade.beta", "etc/casaos/OS-Upgrade.beta", "etc/casaos/os-upgrade.beta/x",
		"var/lib/casaos/alerts.json", "var/lib/casaos/telemetry.json", "var/lib/casaos/autoupdate.json", "var/lib/casaos/virt/state.db", "var/lib/casaos/virt", "var/lib/casaos/x.sample",
		"usr/share/casaos/virt/index.html", "usr/share/casaos/x.sample", "usr/share/casaos/www/index.html",
		"etc/casaos/gateway.ini", "etc/casaos/casaos.conf", "etc/casaos/x.conf", "etc/casaos/x",
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
	// a sample is a place like another, whatever it is called: it is not read by the core
	for _, rel := range []string{"etc/casaos/os-upgrade.beta.sample", "etc/casaos/modules.sample", "etc/casaos/gateway.ini.sample"} {
		if _, err := unpack(t, newFS(), nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + rel, body: "x"}); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
		if !ValidFilePath("/" + rel) {
			t.Errorf("%s may not be in the list of a module", rel)
		}
	}
}

func TestADirectoryOfUnitsHoldsNothingButTheUnitOfTheModule(t *testing.T) {
	for _, dir := range []string{"usr/lib/systemd/system", "lib/systemd/system"} {
		for what, ent := range map[string]tent{
			"a drop-in of a unit of the core":             {name: sysroot + dir + "/casaos-gateway.service.d/override.conf", body: "[Service]\n"},
			"a unit wanted by a target":                   {name: sysroot + dir + "/multi-user.target.wants/x.service", body: "x"},
			"a link in a directory of wants":              {name: sysroot + dir + "/multi-user.target.wants/x.service", typ: tar.TypeSymlink, link: "x.service"},
			"a unit in a directory":                       {name: sysroot + dir + "/sub/" + testUnit, body: "x"},
			"a directory of drop-ins":                     {name: sysroot + dir + "/x.service.d/", typ: tar.TypeDir},
			"a directory of wants":                        {name: sysroot + dir + "/x.target.wants/", typ: tar.TypeDir},
			"a directory":                                 {name: sysroot + dir + "/sub/", typ: tar.TypeDir},
			"a link that is named like a drop-in":         {name: sysroot + dir + "/x.service.d", typ: tar.TypeSymlink, link: "y"},
			"a link that is named like a directory wants": {name: sysroot + dir + "/x.target.wants", typ: tar.TypeSymlink, link: "y"},
			"a link that is named like a requires":        {name: sysroot + dir + "/x.target.requires", typ: tar.TypeSymlink, link: "y"},
			"another service":                             {name: sysroot + dir + "/x.service", body: "[Service]\n"},
			"a unit of the core":                          {name: sysroot + dir + "/casaos-gateway.service", body: "[Service]\n"},
			"a template of the unit":                      {name: sysroot + dir + "/casaos-virt-management@.service", body: "[Service]\n"},
			"the unit and a suffix":                       {name: sysroot + dir + "/" + testUnit + "x", body: "[Service]\n"},
			"the unit and a dot":                          {name: sysroot + dir + "/" + testUnit + ".", body: "[Service]\n"},
			"a part of the unit":                          {name: sysroot + dir + "/casaos-virt-management", body: "[Service]\n"},
			"the unit in capitals":                        {name: sysroot + dir + "/" + strings.ToUpper(testUnit), body: "[Service]\n"},
			"a timer":                                     {name: sysroot + dir + "/x.timer", body: "[Timer]\n"},
			"an alias of the unit":                        {name: sysroot + dir + "/alias.service", typ: tar.TypeSymlink, link: testUnit},
			"the unit as a link":                          {name: sysroot + dir + "/" + testUnit, typ: tar.TypeSymlink, link: "other.service"},
			"the unit as a directory":                     {name: sysroot + dir + "/" + testUnit + "/", typ: tar.TypeDir},
		} {
			refusedAndNothingWritten(t, dir+": "+what, "unpack_path", newFS(), nil, ent)
			refusedAndNothingWritten(t, dir+": "+what+", merged", "unpack_path", newMergedFS(), nil, ent)
		}
		// the unit itself is a place, in either directory, and nothing else of the name is
		unit := tent{name: sysroot + dir + "/" + testUnit, body: "[Service]\n"}
		for _, dst := range []*fakeFS{newFS(), newMergedFS()} {
			if res, err := unpack(t, dst, nil, tent{name: "module.json", body: "{}"}, unit); err != nil || len(res.Files) != 1 {
				t.Errorf("%s: the unit: %v %v", dir, err, res.Files)
			}
		}
		if !ValidFilePath("/"+dir+"/"+testUnit) || ValidFilePath("/"+dir+"/sub/"+testUnit) || ValidFilePath("/"+dir+"/x") || ValidFilePath("/"+dir+"/.service") || ValidFilePath("/"+dir+"/x.timer") {
			t.Errorf("%s: the units of a list", dir)
		}
	}
	// a module with no unit has none to put there: an archive that carries one is refused
	for _, dir := range []string{"usr/lib/systemd/system", "lib/systemd/system"} {
		dst := newFS()
		_, err := Unpack(bytes.NewReader(tarOf(t, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/bin/fine", mode: 0o755, body: "fine"}, tent{name: sysroot + dir + "/" + testUnit, body: "x"})), dst, nil, Options{})
		wantRefusedFor(t, dir+": no unit named", err, "unpack_path")
		if len(dst.log) != 0 {
			t.Errorf("%s: written %v", dir, dst.log)
		}
	}
}

func TestOnlyADirectChildOfAnAllowedDirectoryIsAPlace(t *testing.T) {
	for _, dir := range []string{"usr/bin", "etc/casaos", "usr/lib/systemd/system", "lib/systemd/system"} {
		name := "f"
		switch dir {
		case "etc/casaos":
			name = "f.sample"
		case "usr/lib/systemd/system", "lib/systemd/system":
			name = testUnit
		}
		for what, mk := range map[string]func(string) tent{
			"a file":      func(p string) tent { return tent{name: sysroot + p, body: "x"} },
			"a link":      func(p string) tent { return tent{name: sysroot + p, typ: tar.TypeSymlink, link: "x"} },
			"a directory": func(p string) tent { return tent{name: sysroot + p + "/", typ: tar.TypeDir} },
		} {
			for _, deep := range []string{dir + "/d/" + name, dir + "/d/e/f/g/" + name, dir + "/" + strings.Repeat("d/", 200) + name} {
				refusedAndNothingWritten(t, what+" "+deep[:min(len(deep), 60)], "unpack_path", newFS(), nil, mk(deep))
			}
			if what != "a directory" { // (the directory itself, listed in the archive, is only named: it is made, as needed, by the unpack)
				refusedAndNothingWritten(t, what+" the directory itself, "+dir, "unpack_path", newFS(), nil, mk(dir))
			}
		}
		if ValidFilePath("/"+dir+"/d/"+name) || ValidFilePath("/"+dir) || ValidFilePath("/"+dir+"/") {
			t.Errorf("%s: a path of a list", dir)
		}
		// the right name is not enough: a unit and a sample are files, and a directory is never a place
		if dir != "usr/bin" {
			refusedAndNothingWritten(t, "a link at the name in "+dir, "unpack_path", newFS(), nil, tent{name: sysroot + dir + "/" + name, typ: tar.TypeSymlink, link: "other"})
		}
		refusedAndNothingWritten(t, "a directory at the name in "+dir, "unpack_path", newFS(), nil, tent{name: sysroot + dir + "/" + name + "/", typ: tar.TypeDir})
	}
}

func TestEntriesOfDeepPathsOfOneLetterDirectoriesMakeNothing(t *testing.T) {
	ents := []tent{{name: "module.json", body: "{}"}}
	deep := strings.Repeat("a/", 500)
	for i := 0; i < MaxEntries-1; i++ {
		ents = append(ents, tent{name: fmt.Sprintf("%susr/bin/%sf%d", sysroot, deep, i)})
	}
	dst := newFS()
	res, err := unpack(t, dst, nil, ents...)
	wantRefused(t, err, "unpack_path")
	if len(dst.log) != 0 || len(res.Dirs) != 0 {
		t.Errorf("written %d", len(dst.log))
	}
}

func TestTheDirectoriesAnUnpackMadeAreGivenDeepestFirstAndOnlyThose(t *testing.T) {
	// a system where the directory of the units is not there, nor the two above it: they are made, and given, the deepest first, with 0755
	dst := newFS()
	for _, p := range []string{"/usr/lib/systemd/system", "/usr/lib/systemd", "/usr/lib", "/etc/casaos"} {
		delete(dst.nodes, p)
	}
	res, err := unpack(t, dst, nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/lib/systemd/system/" + testUnit, body: "x", mode: 0o700},
		tent{name: sysroot + "etc/casaos/x.sample", body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/lib", "/usr/lib/systemd", "/usr/lib/systemd/system", "/etc/casaos"}
	got := slices.Clone(res.Dirs)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("directories = %v", res.Dirs)
	}
	for i := 1; i < len(res.Dirs); i++ {
		if strings.Count(res.Dirs[i], "/") > strings.Count(res.Dirs[i-1], "/") {
			t.Errorf("not the deepest first: %v", res.Dirs)
		}
	}
	// exactly the ones that were made, each with 0755
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
	for _, p := range want {
		if n := dst.nodes[p]; n == nil || n.mode != fs.ModeDir|0o755 {
			t.Errorf("%s: %+v", p, n)
		}
	}
	if n := dst.nodes["/usr/lib/systemd/system/"+testUnit]; n == nil || n.mode != 0o700 || !slices.Equal(res.Files, []string{"/etc/casaos/x.sample", "/usr/lib/systemd/system/" + testUnit}) {
		t.Errorf("the files: %+v %v", n, res.Files)
	}
	// when the unpack stops half way the ones made so far are given
	dst = newFS()
	delete(dst.nodes, "/etc/casaos")
	dst.failOn = "/etc/casaos/virt-management.conf.sample"
	res, err = unpack(t, dst, nil, goodEntries()...)
	if err == nil || !slices.Equal(res.Dirs, []string{"/etc/casaos"}) {
		t.Errorf("%v, directories %v", err, res.Dirs)
	}
	// directories that are there are not made, nor given
	dst = newFS()
	res, err = unpack(t, dst, nil, goodEntries()...)
	if err != nil || len(res.Dirs) != 0 {
		t.Errorf("%v, directories %v", err, res.Dirs)
	}
	// a refusal makes nothing
	dst = newFS()
	delete(dst.nodes, "/etc/casaos")
	res, err = unpack(t, dst, nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "etc/casaos/f.sample"}, tent{name: sysroot + "usr/sbin/x"})
	wantRefused(t, err, "unpack_path")
	if len(res.Dirs) != 0 || len(dst.log) != 0 {
		t.Errorf("%v %v", res.Dirs, dst.log)
	}
}

func TestADirectoryThatAppearsAfterTheCheckingIsNotOursToDelete(t *testing.T) {
	ents := []tent{{name: "module.json", body: "{}"}, {name: sysroot + "usr/lib/systemd/system/" + testUnit, body: "x"}}
	system := func() *fakeFS {
		dst := newFS()
		for _, p := range []string{"/usr/lib/systemd/system", "/usr/lib/systemd"} {
			delete(dst.nodes, p)
		}
		return dst
	}
	// somebody made one of the directories: the unpack goes on, and does not say it made it
	dst := system()
	dst.onMkdir = func(p string) {
		if p == "/usr/lib/systemd" {
			dst.nodes[p] = &node{mode: fs.ModeDir | 0o700}
		}
	}
	res, err := unpack(t, dst, nil, ents...)
	if err != nil || !slices.Equal(res.Dirs, []string{"/usr/lib/systemd/system"}) || !slices.Equal(res.Files, []string{"/usr/lib/systemd/system/" + testUnit}) {
		t.Errorf("%v %+v", err, res)
	}
	// ...but not if it is a link, or a file, now
	for what, mk := range map[string]*node{"a link": {mode: fs.ModeSymlink | 0o777, target: "/etc"}, "a file": {mode: 0o644}} {
		dst = system()
		dst.onMkdir = func(p string) {
			if p == "/usr/lib/systemd" {
				dst.nodes[p] = mk
			}
		}
		res, err = unpack(t, dst, nil, ents...)
		wantRefusedFor(t, what, err, "unpack_path")
		if len(res.Files) != 0 || dst.nodes["/usr/lib/systemd/system"] != nil {
			t.Errorf("%s: %+v", what, res)
		}
	}
}

func TestAFileThatAppearsAfterTheLastCheckIsNotOverwrittenAndNotListed(t *testing.T) {
	good := tarOf(t, goodEntries()...)
	// the files are put down in this order: the program, the link to it, the sample of the configuration; the unit is last
	order := []string{"/usr/bin/casaos-virt-management", "/usr/bin/virt", "/etc/casaos/virt-management.conf.sample", "/usr/lib/systemd/system/" + testUnit}
	for _, c := range []struct {
		name  string
		at    int // the number of the file being copied, when the other file appears
		plant string
		own   []string
		kept  int // how many of the files in order are written before it
	}{
		{"the first file", 1, order[0], nil, 0},
		{"a file that is not the first", 2, order[2], nil, 2},
		{"the unit", 3, order[3], nil, 3},
		{"a file that the module listed and that was not there when it looked", 2, order[2], []string{order[2]}, 2},
	} {
		dst := newFS()
		res, err := Unpack(seekHook(good, func(n int, _ *hooked) {
			if n == c.at {
				dst.put(c.plant, 0o644, "foreign")
			}
		}), dst, c.own, testOpts)
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
	}
	// a link that appears is in the way too
	dst := newFS()
	res, err := Unpack(seekHook(good, func(n int, _ *hooked) {
		if n == 1 {
			dst.nodes["/usr/bin/virt"] = &node{mode: fs.ModeSymlink | 0o777, target: "/etc/passwd"}
		}
	}), dst, nil, testOpts)
	wantRefused(t, err, "file_exists")
	if n := dst.nodes["/usr/bin/virt"]; n == nil || n.target != "/etc/passwd" || !slices.Equal(res.Files, order[:1]) {
		t.Errorf("link %+v, files %v", n, res.Files)
	}
	// a file of the module's own that is there is replaced all the same, and that is the only kind
	dst = newFS()
	dst.put(order[0], 0o755, "old")
	res, err = Unpack(bytes.NewReader(good), dst, []string{order[0]}, testOpts)
	if err != nil || len(res.Files) != 4 || string(dst.nodes[order[0]].data) == "old" {
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
		{"the sample", "[libvirt]", "/etc/casaos/virt-management.conf.sample", 2, 2},
		{"the unit", "[Service]", "/usr/lib/systemd/system/" + testUnit, 3, 3},
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
		}), dst, nil, testOpts)
		wantRefusedFor(t, c.name, err, "unpack_corrupt")
		if dst.nodes[c.path] != nil || !slices.Contains(dst.log, "abort "+c.path) || slices.Contains(res.Files, c.path) || len(res.Files) != c.before {
			t.Errorf("%s: log %v, files %v", c.name, dst.log, res.Files)
		}
	}
	// the whole of the sum is compared: bytes of the same size whose sum starts as the sum of the real ones are not the real ones
	page := []byte("[libvirt]\nURI = qemu:///system\n")
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
		if n == 2 {
			h.r = bytes.NewReader(forgedArchive)
		}
	}), dst, nil, testOpts)
	wantRefused(t, err, "unpack_corrupt")
	// the same bytes every time is what is written
	dst = newFS()
	if _, err := Unpack(seekHook(good, func(int, *hooked) {}), dst, nil, testOpts); err != nil {
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
		res, err := Unpack(bytes.NewReader(raw), dst, nil, testOpts)
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
		if _, err := Unpack(bytes.NewReader(raw), newFS(), nil, testOpts); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAnArchiveWithBothSpellingsOfALibDirectoryWritesNothing(t *testing.T) {
	const lib, usr = sysroot + "lib/systemd/system/" + testUnit, sysroot + "usr/lib/systemd/system/" + testUnit
	for name, ents := range map[string][]tent{
		"a file twice":            {{name: lib, body: "1"}, {name: usr, body: "2"}},
		"a file twice, the other": {{name: usr, body: "2"}, {name: lib, body: "1"}},
		"a file between others":   {{name: sysroot + "usr/bin/q", body: "q"}, {name: lib, body: "1"}, {name: sysroot + "usr/bin/p", body: "p"}, {name: usr, body: "2"}},
	} {
		refusedAndNothingWritten(t, name, "unpack_path", newFS(), nil, ents...)
		refusedAndNothingWritten(t, name+", merged", "unpack_path", newMergedFS(), nil, ents...)
	}
	// the directories above them, named in both spellings, are no clash
	ents := []tent{
		{name: "module.json", body: "{}"},
		{name: sysroot + "lib/", typ: tar.TypeDir}, {name: sysroot + "lib/systemd/", typ: tar.TypeDir}, {name: sysroot + "usr/", typ: tar.TypeDir}, {name: sysroot + "usr/lib/", typ: tar.TypeDir},
		{name: sysroot + "usr/lib/systemd/", typ: tar.TypeDir},
		{name: lib, body: "1"},
	}
	if res, err := unpack(t, newFS(), nil, ents...); err != nil || len(res.Files) != 1 {
		t.Errorf("%v %v", err, res.Files)
	}
}

func TestOnASystemWithAMergedUsrTheTwoNamesOfALibFileAreTheOneFile(t *testing.T) {
	const lib, usr = "/lib/systemd/system/" + testUnit, "/usr/lib/systemd/system/" + testUnit
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
	// a system whose /lib is a directory of its own has two directories: what the module owns under the one is not what is under the other
	dst := newFS()
	dst.put(usr, 0o644, "theirs")
	_, err := unpack(t, dst, []string{lib}, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/lib/systemd/system/" + testUnit, body: "new"})
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
	for _, above := range []string{"/usr", "/usr/lib", "/usr/lib/systemd", "/etc"} {
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
	// a link the module owns is not made a directory
	for name, ent := range map[string]tent{"a directory": {name: sysroot + "usr/bin/virt/", typ: tar.TypeDir}} {
		dst = newFS()
		dst.nodes["/usr/bin/virt"] = &node{mode: fs.ModeSymlink | 0o777, target: "old"}
		_, err = unpack(t, dst, own, tent{name: "module.json", body: "{}"}, ent)
		wantRefusedFor(t, name, err, "unpack_path")
		if len(dst.log) != 0 {
			t.Errorf("%s: written %v", name, dst.log)
		}
	}
	// a link the module owns is made a file by a rename that puts the file in the place of the link: what the link points to is not written
	dst = newFS()
	dst.nodes["/usr/bin/virt"] = &node{mode: fs.ModeSymlink | 0o777, target: "/etc/passwd"}
	dst.put("/etc/passwd", 0o644, "root")
	if _, err = unpack(t, dst, own, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/bin/virt", mode: 0o755, body: "x"}); err != nil {
		t.Fatalf("a link made a file: %v", err)
	}
	if n := dst.nodes["/usr/bin/virt"]; n.mode&fs.ModeSymlink != 0 || string(n.data) != "x" || string(dst.nodes["/etc/passwd"].data) != "root" {
		t.Errorf("a link made a file: %+v, passwd %q", n, dst.nodes["/etc/passwd"].data)
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
	delete(dst.nodes, "/etc/casaos")
	_, err := unpack(t, dst, nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "etc/", typ: tar.TypeDir, mode: 0o700}, tent{name: sysroot + "etc/casaos/", typ: tar.TypeDir, mode: 0o700},
		tent{name: sysroot + "etc/casaos/c.sample", mode: 0o600, body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	for p, mode := range map[string]fs.FileMode{"/etc/casaos": fs.ModeDir | 0o755, "/etc/casaos/c.sample": 0o600} {
		if n := dst.nodes[p]; n == nil || n.mode != mode {
			t.Errorf("%s: %+v, want %v", p, n, mode)
		}
	}
}

func TestTheFilesThatMayBeWrittenAreGivenBeforeAnythingIsWritten(t *testing.T) {
	dst := newFS()
	delete(dst.nodes, "/etc/casaos")
	var calls [][]string
	var dirs [][]string
	var seen []int
	res, err := Unpack(bytes.NewReader(tarOf(t, goodEntries()...)), dst, nil, Options{Unit: testUnit, OnPlanned: func(files []string, _ map[string]Want, ds []string) error {
		calls = append(calls, slices.Clone(files))
		dirs = append(dirs, slices.Clone(ds))
		seen = append(seen, len(dst.log), len(dst.nodes))
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !slices.Equal(calls[0], res.Files) {
		t.Errorf("planned %v, written %v", calls, res.Files)
	}
	if !slices.Equal(dirs[0], []string{"/etc/casaos"}) || !slices.Equal(res.Dirs, []string{"/etc/casaos"}) {
		t.Errorf("directories planned %v, made %v", dirs, res.Dirs)
	}
	if seen[0] != 0 || seen[1] != len(newFS().nodes)-1 {
		t.Errorf("something was written already: %v", seen)
	}
	// the unit is the last of them
	if last := calls[0][len(calls[0])-1]; last != "/usr/lib/systemd/system/"+testUnit {
		t.Errorf("last = %s", last)
	}
	// the list can be kept as it is, and so can the directories
	if raw, err := FormatFilesList(entriesFor(calls[0])); err != nil {
		t.Errorf("%v", err)
	} else if back, err := ParseFiles(raw); err != nil || !slices.Equal(back, calls[0]) {
		t.Errorf("%v %v", back, err)
	}
	if raw, err := FormatDirs(dirs[0]); err != nil {
		t.Errorf("%v", err)
	} else if back, err := ParseDirs(raw); err != nil || !slices.Equal(back, dirs[0]) {
		t.Errorf("%v %v", back, err)
	}
	// a stop half way leaves a list that has what was not written too, and that is all of what was
	dst = newFS()
	dst.failOn = "/etc/casaos/virt-management.conf.sample"
	calls = nil
	res, err = Unpack(bytes.NewReader(tarOf(t, goodEntries()...)), dst, nil, Options{Unit: testUnit, OnPlanned: func(files []string, _ map[string]Want, _ []string) error { calls = append(calls, files); return nil }})
	if err == nil || len(calls) != 1 || len(calls[0]) != 4 || len(res.Files) >= 4 {
		t.Fatalf("%v planned %v written %v", err, calls, res.Files)
	}
	for _, p := range res.Files {
		if !slices.Contains(calls[0], p) {
			t.Errorf("%s was written and not planned", p)
		}
	}
	// what is planned does not name the files of the module that stay: nothing is kept any more
	if slices.Contains(calls[0], "/etc/casaos/virt-management.conf") {
		t.Errorf("planned %v", calls[0])
	}
}

func TestIfTheFilesCannotBeRecordedNothingIsWritten(t *testing.T) {
	boom := errors.New("no room for the list")
	dst := newFS()
	delete(dst.nodes, "/etc/casaos")
	res, err := Unpack(bytes.NewReader(tarOf(t, goodEntries()...)), dst, nil, Options{Unit: testUnit, OnPlanned: func([]string, map[string]Want, []string) error { return boom }})
	var r *Refused
	if !errors.Is(err, boom) || errors.As(err, &r) {
		t.Errorf("error = %v", err)
	}
	if len(dst.log) != 0 || len(res.Files) != 0 || len(res.Dirs) != 0 {
		t.Errorf("written %v, %+v", dst.log, res)
	}
	// an archive that is refused is not recorded
	called := false
	_, err = Unpack(bytes.NewReader(tarOf(t, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/sbin/x"})), newFS(), nil, Options{Unit: testUnit, OnPlanned: func([]string, map[string]Want, []string) error { called = true; return nil }})
	wantRefused(t, err, "unpack_path")
	if called {
		t.Error("recorded an archive that is refused")
	}
	// and nor is one that only gets validated
	if _, err := ValidateArchive(bytes.NewReader(tarOf(t, goodEntries()...)), newFS(), nil, Options{Unit: testUnit, OnPlanned: func([]string, map[string]Want, []string) error { called = true; return nil }}); err != nil || called {
		t.Errorf("%v %v", err, called)
	}
}

func TestASampleThatIsNotAFileIsInTheWayEvenWhenTheModuleListedIt(t *testing.T) {
	for what, n := range map[string]*node{
		"a directory": {mode: fs.ModeDir | 0o755},
		"a fifo":      {mode: fs.ModeNamedPipe | 0o600},
		"a device":    {mode: fs.ModeDevice | fs.ModeCharDevice | 0o600},
	} {
		dst := newFS()
		dst.nodes["/etc/casaos/virt.conf.sample"] = n
		_, err := unpack(t, dst, []string{"/etc/casaos/virt.conf.sample"}, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "etc/casaos/virt.conf.sample", body: "x"})
		wantRefusedFor(t, what, err, "file_exists")
		if len(dst.log) != 0 {
			t.Errorf("%s: written %v", what, dst.log)
		}
	}
}

// What each path is to hold is given with the plan and with the result: the sha256 of the bytes of the file that was checked, the target of a link; and a stop half way gives it for what
// was written only
func TestTheUnpackSaysWhatEachPathIsToHold(t *testing.T) {
	dst := newFS()
	var planned map[string]Want
	res, err := Unpack(bytes.NewReader(tarOf(t, goodEntries()...)), dst, nil, Options{Unit: testUnit, OnPlanned: func(files []string, want map[string]Want, _ []string) error {
		planned = want
		if len(want) != len(files) {
			t.Errorf("%d contents for %d paths", len(want), len(files))
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(strings.Repeat("\x7fELF", 300)))
	for _, w := range []map[string]Want{planned, res.Want} {
		if got := w["/usr/bin/casaos-virt-management"]; got.Link || got.Value != hex.EncodeToString(sum[:]) {
			t.Errorf("the program: %+v", got)
		}
		if got := w["/usr/bin/virt"]; !got.Link || got.Value != "casaos-virt-management" {
			t.Errorf("the link: %+v", got)
		}
		if len(w) != len(res.Files) {
			t.Errorf("%d contents for %d files", len(w), len(res.Files))
		}
	}
	// half way
	dst = newFS()
	dst.failOn = "/etc/casaos/virt-management.conf.sample"
	res, err = Unpack(bytes.NewReader(tarOf(t, goodEntries()...)), dst, nil, Options{Unit: testUnit})
	if err == nil || len(res.Want) != len(res.Files) || len(res.Files) == 0 {
		t.Errorf("%v: %d contents for %d files", err, len(res.Want), len(res.Files))
	}
	for _, p := range res.Files {
		if _, ok := res.Want[p]; !ok {
			t.Errorf("%s is written and has no content", p)
		}
	}
}

// An archive may not put a program of the system that the assistant runs in usr/bin, as a file or as a link: on a system whose /bin is not /usr/bin it would be found before the real one.
// A name that only looks like one is fine.
func TestAnArchiveMayNotPutAProgramOfTheSystemInUsrBin(t *testing.T) {
	for _, name := range ReservedPrograms {
		refusedAndNothingWritten(t, name, UnpackPath, newFS(), nil, tent{name: sysroot + "usr/bin/" + name, mode: 0o755, body: "#!/bin/sh\n"})
		refusedAndNothingWritten(t, name+" as a link", UnpackPath, newFS(), nil, tent{name: sysroot + "usr/bin/" + name, typ: tar.TypeSymlink, link: "fine"})
	}
	for _, name := range []string{"systemctl.real", "Systemctl", "systemctl-x", "xsystemctl", "dpkg2", "casaos-virt-management"} {
		if _, err := unpack(t, newFS(), nil, tent{name: "module.json", body: "{}"}, tent{name: sysroot + "usr/bin/" + name, mode: 0o755, body: "x"}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// the other directories have no such rule: nothing else of a module's can be a program of the system
	for _, name := range ReservedPrograms {
		if !slices.Contains([]string{"systemctl", "sh"}, name) {
			continue
		}
		refusedAndNothingWritten(t, name+" in the directory of the units", UnpackPath, newFS(), nil, tent{name: sysroot + "usr/lib/systemd/system/" + name, body: "[Service]\n"})
	}
}
