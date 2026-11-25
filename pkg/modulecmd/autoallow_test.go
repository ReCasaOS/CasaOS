package modulecmd

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// allowedNames is what AutoAllowed says, as a map that is never nil.
func allowedNames(f *fixture) map[string]bool {
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
		{"broken JSON", func(f *fixture) { f.writeFile(pathAutoAllow, `{"modules":["virt"`) }},
		{"null", func(f *fixture) { f.writeFile(pathAutoAllow, `null`) }},
		{"an array", func(f *fixture) { f.writeFile(pathAutoAllow, `["virt"]`) }},
		{"a string for the list", func(f *fixture) { f.writeFile(pathAutoAllow, `{"modules":"virt"}`) }},
		{"a number in the list", func(f *fixture) { f.writeFile(pathAutoAllow, `{"modules":["virt",5]}`) }},
		{"too large", func(f *fixture) {
			f.writeFile(pathAutoAllow, `{"modules":["virt"],"x":"`+strings.Repeat("a", autoAllowMax)+`"}`)
		}},
		{"a directory", func(f *fixture) {
			if err := os.MkdirAll(f.path(pathAutoAllow), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"a link to a good list", func(f *fixture) {
			f.writeFile("/tmp/list.json", `{"modules":["virt"]}`)
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
			if got := allowedNames(f); len(got) != 0 {
				t.Errorf("allowed %v", got)
			}
		})
	}
}

// A name that is not a module's is not allowed; the others in the file are.
func TestAutoAllowedSkipsWhatIsNotTheNameOfAModule(t *testing.T) {
	f := newFixture(t)
	f.writeFile(pathAutoAllow, `{"modules":["virt","Virt","../etc","","a b","mcp"]}`)
	if got := allowedNames(f); !reflect.DeepEqual(got, map[string]bool{"virt": true, "mcp": true}) {
		t.Errorf("allowed %v", got)
	}
}

// readAllowFile is the file as it was written: its list, and its mode.
func readAllowFile(f *fixture) ([]string, os.FileMode) {
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
		Modules []string `json:"modules"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		f.t.Fatalf("%v: %s", err, raw)
	}
	return v.Modules, info.Mode()
}

func TestSetAutoAllowedAddsOnceAndRemoves(t *testing.T) {
	f := newFixture(t)
	roots := f.env.Roots
	for _, step := range []struct {
		name string
		on   bool
		want []string
	}{
		{"virt", true, []string{"virt"}},
		{"virt", true, []string{"virt"}},
		{"mcp", true, []string{"mcp", "virt"}},
		{"virt", false, []string{"mcp"}},
		{"virt", false, []string{"mcp"}},
		{"mcp", false, []string{}},
	} {
		if err := SetAutoAllowed(roots, step.name, step.on); err != nil {
			t.Fatalf("%s %v: %v", step.name, step.on, err)
		}
		got, mode := readAllowFile(f)
		if !reflect.DeepEqual(got, step.want) || mode != 0o644 {
			t.Fatalf("after %s %v: %v %v, want %v 0644", step.name, step.on, got, mode, step.want)
		}
		if allowed := allowedNames(f); len(allowed) != len(step.want) {
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

// A broken file is replaced by a whole one: what it said allowed nothing.
func TestSetAutoAllowedReplacesABrokenFile(t *testing.T) {
	f := newFixture(t)
	f.writeFile(pathAutoAllow, `{"modules":[`)
	if err := SetAutoAllowed(f.env.Roots, "virt", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := readAllowFile(f); !reflect.DeepEqual(got, []string{"virt"}) {
		t.Errorf("%v", got)
	}
}

func TestSetAutoAllowedRefusesABadNameAndALink(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"", "Virt", "../virt", "virt/x", strings.Repeat("a", 33)} {
		if err := SetAutoAllowed(f.env.Roots, name, true); err == nil {
			t.Errorf("%q was taken", name)
		}
	}
	if _, err := os.Lstat(f.path(pathAutoAllow)); !os.IsNotExist(err) {
		t.Errorf("a file was written: %v", err)
	}
	// a link at its place is not written through, nor replaced
	f.writeFile("/tmp/elsewhere.json", `{"modules":[]}`)
	if err := os.MkdirAll(f.path(pathState), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.path("/tmp/elsewhere.json"), f.path(pathAutoAllow)); err != nil {
		t.Fatal(err)
	}
	if err := SetAutoAllowed(f.env.Roots, "virt", true); err == nil {
		t.Error("written with a link at the place of the file")
	}
	if info, err := os.Lstat(f.path(pathAutoAllow)); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v", err)
	}
	if raw, _ := os.ReadFile(f.path("/tmp/elsewhere.json")); string(raw) != `{"modules":[]}` {
		t.Errorf("written through the link: %s", raw)
	}
}

// The list says auto_update for a module written outside ReCasaOS that is installed, and for no other.
func TestListSaysAutoUpdateForAnInstalledForeignModuleOnly(t *testing.T) {
	f := installed(t)
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, "[]", f.mirrorEntry(onlineTag, foreignRepo)))
	if _, m := catalogueOf(t, f); m["auto_update"] != false {
		t.Errorf("foreign, installed, not allowed: %v", m)
	}
	if err := SetAutoAllowed(f.env.Roots, testName, true); err != nil {
		t.Fatal(err)
	}
	if _, m := catalogueOf(t, f); m["auto_update"] != true {
		t.Errorf("foreign, installed, allowed: %v", m)
	}
	// a module of ReCasaOS: the general switch decides, and the list says nothing of the entry in the file
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, "[]", f.mirrorEntry(onlineTag, ownRepo)))
	if _, m := catalogueOf(t, f); m["auto_update"] != nil {
		t.Errorf("a module of ReCasaOS: %v", m)
	}
	// foreign and not installed: nothing either
	g := newFixture(t)
	g.writeFile(pathOnlineCatalogue, online3Raw(t, 7, "[]", g.mirrorEntry(onlineTag, foreignRepo)))
	if err := SetAutoAllowed(g.env.Roots, testName, true); err != nil {
		t.Fatal(err)
	}
	if _, m := catalogueOf(t, g); m["foreign"] != true || m["auto_update"] != nil {
		t.Errorf("foreign, not installed: %v", m)
	}
}
