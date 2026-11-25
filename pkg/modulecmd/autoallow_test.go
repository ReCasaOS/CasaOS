package modulecmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// allowedOf is what AutoAllowed says, as a map that is never nil.
func allowedOf(f *fixture) map[string]string {
	got := AutoAllowed(f.env.Roots)
	if got == nil {
		f.t.Fatal("AutoAllowed returned nil")
	}
	return got
}

// A file that cannot be read as the list allows nothing, and the caller is never told of an error.
func TestAutoAllowedIsEmptyWhenTheFileCannotBeRead(t *testing.T) {
	for _, c := range []struct {
		name  string
		write func(f *fixture)
	}{
		{"missing", func(*fixture) {}},
		{"empty", func(f *fixture) { f.writeFile(pathAutoAllow, "") }},
		{"broken JSON", func(f *fixture) { f.writeFile(pathAutoAllow, `{"modules":{"virt":"a/b"`) }},
		{"null", func(f *fixture) { f.writeFile(pathAutoAllow, `null`) }},
		{"an array", func(f *fixture) { f.writeFile(pathAutoAllow, `[{"virt":"a/b"}]`) }},
		{"the list of names of the first format", func(f *fixture) { f.writeFile(pathAutoAllow, `{"modules":["virt"]}`) }},
		{"a number for a repository", func(f *fixture) { f.writeFile(pathAutoAllow, `{"modules":{"virt":5}}`) }},
		{"too large", func(f *fixture) {
			f.writeFile(pathAutoAllow, `{"modules":{"virt":"a/b"},"x":"`+strings.Repeat("a", autoAllowMax)+`"}`)
		}},
		{"a directory", func(f *fixture) {
			if err := os.MkdirAll(f.path(pathAutoAllow), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"a link to a good list", func(f *fixture) {
			f.writeFile("/tmp/list.json", `{"modules":{"virt":"someone/x"}}`)
			if err := os.Symlink(f.path("/tmp/list.json"), f.path(pathAutoAllow)); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			if err := os.MkdirAll(f.path(pathState), 0o755); err != nil {
				t.Fatal(err)
			}
			c.write(f)
			if got := allowedOf(f); len(got) != 0 {
				t.Errorf("allowed %v", got)
			}
		})
	}
}

// A name that is not a module's, or a repository that is not "<owner>/<repo>", is not allowed; the others in the file are.
func TestAutoAllowedSkipsWhatIsNotANameAndARepository(t *testing.T) {
	f := newFixture(t)
	f.writeFile(pathAutoAllow, `{"modules":{"virt":"someone/CasaOS-Virt","Virt":"a/b","../etc":"a/b","":"a/b","a b":"a/b",`+
		`"mcp":"alice/tool","one":"","two":"alice","three":"alice/","four":"/tool","five":"al ice/tool","six":"alice/tool/x","seven":"-alice/tool"}}`)
	if got := allowedOf(f); !reflect.DeepEqual(got, map[string]string{"virt": "someone/CasaOS-Virt", "mcp": "alice/tool"}) {
		t.Errorf("allowed %v", got)
	}
}

// readAllowFile is the file as it was written: its entries, and its mode.
func readAllowFile(f *fixture) (map[string]string, os.FileMode) {
	f.t.Helper()
	info, err := os.Lstat(f.path(pathAutoAllow))
	if err != nil {
		f.t.Fatal(err)
	}
	raw, err := os.ReadFile(f.path(pathAutoAllow))
	if err != nil {
		f.t.Fatal(err)
	}
	var v struct {
		Modules map[string]string `json:"modules"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.Modules == nil {
		f.t.Fatalf("%v: %s", err, raw)
	}
	return v.Modules, info.Mode()
}

func TestSetAutoAllowedAddsOnceAndRemoves(t *testing.T) {
	f := newFixture(t)
	roots := f.env.Roots
	for _, step := range []struct {
		name, repo string
		on         bool
		want       map[string]string
	}{
		{"virt", "alice/virt", true, map[string]string{"virt": "alice/virt"}},
		{"virt", "alice/virt", true, map[string]string{"virt": "alice/virt"}},
		{"mcp", "bob/mcp", true, map[string]string{"mcp": "bob/mcp", "virt": "alice/virt"}},
		{"virt", "carol/virt", true, map[string]string{"mcp": "bob/mcp", "virt": "carol/virt"}}, // the last yes is the one kept
		{"virt", "", false, map[string]string{"mcp": "bob/mcp"}},
		{"virt", "", false, map[string]string{"mcp": "bob/mcp"}},
		{"mcp", "", false, map[string]string{}},
	} {
		if err := SetAutoAllowed(roots, step.name, step.repo, step.on); err != nil {
			t.Fatalf("%s %v: %v", step.name, step.on, err)
		}
		got, mode := readAllowFile(f)
		if !reflect.DeepEqual(got, step.want) || mode != 0o644 {
			t.Fatalf("after %s %v: %v %v, want %v 0644", step.name, step.on, got, mode, step.want)
		}
		if allowed := allowedOf(f); !reflect.DeepEqual(allowed, step.want) {
			t.Fatalf("after %s %v: AutoAllowed %v", step.name, step.on, allowed)
		}
	}
	// no temporary file is left beside it
	entries, err := os.ReadDir(f.path(pathState))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".cos-") {
			t.Errorf("left %s", e.Name())
		}
	}
}

// A file of another format, or broken, is replaced by a whole one: what it said allowed nothing.
func TestSetAutoAllowedReplacesAFileItCannotRead(t *testing.T) {
	for _, old := range []string{`{"modules":[`, `{"modules":["virt","mcp"]}`} {
		f := newFixture(t)
		f.writeFile(pathAutoAllow, old)
		if err := SetAutoAllowed(f.env.Roots, "virt", "alice/virt", true); err != nil {
			t.Fatal(err)
		}
		if got, _ := readAllowFile(f); !reflect.DeepEqual(got, map[string]string{"virt": "alice/virt"}) {
			t.Errorf("%s: %v", old, got)
		}
	}
}

func TestSetAutoAllowedRefusesABadNameABadRepositoryAndALink(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"", "Virt", "../virt", "virt/x", strings.Repeat("a", 33)} {
		if err := SetAutoAllowed(f.env.Roots, name, "alice/virt", true); err == nil {
			t.Errorf("%q was taken", name)
		}
	}
	for _, repo := range []string{"", "alice", "alice/", "/virt", "alice/virt/x", "al ice/virt"} {
		if err := SetAutoAllowed(f.env.Roots, "virt", repo, true); err == nil {
			t.Errorf("the repository %q was taken", repo)
		}
	}
	if _, err := os.Lstat(f.path(pathAutoAllow)); !os.IsNotExist(err) {
		t.Errorf("a file was written: %v", err)
	}
	// a link at its place is not written through, nor replaced
	f.writeFile("/tmp/elsewhere.json", `{"modules":{}}`)
	if err := os.MkdirAll(f.path(pathState), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.path("/tmp/elsewhere.json"), f.path(pathAutoAllow)); err != nil {
		t.Fatal(err)
	}
	if err := SetAutoAllowed(f.env.Roots, "virt", "alice/virt", true); err == nil {
		t.Error("written with a link at the place of the file")
	}
	if info, err := os.Lstat(f.path(pathAutoAllow)); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v", err)
	}
	if raw, _ := os.ReadFile(f.path("/tmp/elsewhere.json")); string(raw) != `{"modules":{}}` {
		t.Errorf("written through the link: %s", raw)
	}
}

// The list says auto_update for a module written outside ReCasaOS that is installed, and for no other; it is true only for the
// repository the yes was given for.
func TestListSaysAutoUpdateForAnInstalledForeignModuleOnly(t *testing.T) {
	f := installed(t)
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, "[]", f.mirrorEntry(onlineTag, foreignRepo)))
	if _, m := catalogueOf(t, f); m["auto_update"] != false {
		t.Errorf("foreign, installed, not allowed: %v", m)
	}
	if err := SetAutoAllowed(f.env.Roots, testName, foreignRepo, true); err != nil {
		t.Fatal(err)
	}
	if _, m := catalogueOf(t, f); m["auto_update"] != true {
		t.Errorf("foreign, installed, allowed: %v", m)
	}
	// a module of the same name from another repository: the yes was not given for it
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, "[]", f.mirrorEntry(onlineTag, "bob/"+testComponent)))
	if _, m := catalogueOf(t, f); m["repository"] != "bob/"+testComponent || m["auto_update"] != false {
		t.Errorf("the same name from another repository: %v", m)
	}
	// a module of ReCasaOS: the general switch decides, and the list says nothing of the entry in the file
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, "[]", f.mirrorEntry(onlineTag, ownRepo)))
	if _, m := catalogueOf(t, f); m["auto_update"] != nil {
		t.Errorf("a module of ReCasaOS: %v", m)
	}
	// the first format of the file, a list of names, allows nobody
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, "[]", f.mirrorEntry(onlineTag, foreignRepo)))
	f.writeFile(pathAutoAllow, `{"modules":["`+testName+`"]}`)
	if _, m := catalogueOf(t, f); m["auto_update"] != false {
		t.Errorf("the first format: %v", m)
	}
	// foreign and not installed: nothing either
	g := newFixture(t)
	g.writeFile(pathOnlineCatalogue, online3Raw(t, 7, "[]", g.mirrorEntry(onlineTag, foreignRepo)))
	if err := SetAutoAllowed(g.env.Roots, testName, foreignRepo, true); err != nil {
		t.Fatal(err)
	}
	if _, m := catalogueOf(t, g); m["foreign"] != true || m["auto_update"] != nil {
		t.Errorf("foreign, not installed: %v", m)
	}
}

// A remove takes the module's yes away: a reinstall is not allowed until the owner says yes again.
func TestARemoveDropsTheYesOfTheModule(t *testing.T) {
	f := installed(t)
	if err := SetAutoAllowed(f.env.Roots, testName, foreignRepo, true); err != nil {
		t.Fatal(err)
	}
	if err := SetAutoAllowed(f.env.Roots, "other", "alice/other", true); err != nil {
		t.Fatal(err)
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("remove: exit %d\n%s", code, f.out)
	}
	if got := allowedOf(f); !reflect.DeepEqual(got, map[string]string{"other": "alice/other"}) {
		t.Fatalf("after the remove: %v", got)
	}
	if code := f.install("--yes"); code != ExitOK {
		t.Fatalf("install again: exit %d\n%s", code, f.out)
	}
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, "[]", f.mirrorEntry(onlineTag, foreignRepo)))
	if _, m := catalogueOf(t, f); m["auto_update"] != false {
		t.Errorf("reinstalled: %v", m)
	}
}

// A remove whose rewrite of the file fails still removes the module, and says what was left.
func TestARemoveWhoseYesCannotBeDroppedStillRemoves(t *testing.T) {
	f := installed(t)
	if err := SetAutoAllowed(f.env.Roots, testName, foreignRepo, true); err != nil {
		t.Fatal(err)
	}
	// a directory that is not empty at the temporary name: the rewrite cannot take it away, even as root
	if err := os.MkdirAll(filepath.Join(tempName(f.path(pathAutoAllow)), "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := f.remove("--yes"); code != ExitOK {
		t.Fatalf("remove: exit %d\n%s", code, f.out)
	}
	if modulePresent(f.env.Roots, testName) {
		t.Error("the module is still there")
	}
	if !strings.Contains(f.out.String(), "left: "+pathAutoAllow) {
		t.Errorf("the remove does not say the yes was left:\n%s", f.out)
	}
	if got := allowedOf(f); got[testName] != foreignRepo {
		t.Errorf("the file changed: %v", got)
	}
}

// A yes is written only for a module installed when the lock is held: a remove takes the yes out once the module's files are gone, so a yes given meanwhile is refused, never left behind.
func TestAYesIsForAModuleInstalledUnderTheLock(t *testing.T) {
	f := newFixture(t)
	if err := AllowAutoUpdate(f.env.Roots, testName, foreignRepo); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("a module not installed: %v", err)
	}
	if _, err := os.Lstat(f.path(pathAutoAllow)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the file was written: %v", err)
	}
	g := installed(t)
	if err := AllowAutoUpdate(g.env.Roots, testName, foreignRepo); err != nil {
		t.Fatal(err)
	}
	if got := allowedOf(g); !reflect.DeepEqual(got, map[string]string{testName: foreignRepo}) {
		t.Fatalf("allowed %v", got)
	}
}
