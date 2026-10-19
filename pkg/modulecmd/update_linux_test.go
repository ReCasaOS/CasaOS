package modulecmd

import (
	"archive/tar"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// What only a Linux file system has: links, and a box whose /lib is a link to usr/lib.

const aliasPath = "/usr/bin/casaos-virt-alias"

func withAlias(ents []tent, target string) []tent {
	return append(slices.Clone(ents), tent{name: "build/sysroot" + aliasPath, typ: tar.TypeSymlink, link: target})
}

// A link of the old version is put back by a rollback and replaced by the link of the new one, and one the new version does not have is taken away.
func TestAnUpdateReplacesPutsBackAndTakesAwayLinks(t *testing.T) {
	for _, c := range []struct {
		name string
		next []tent // the links of the new version
		want string // what the alias is after a good update, "" for gone
	}{
		{"a link that points elsewhere", withAlias(v2Entries(t, testManifest()), "casaos-virt-tool"), "casaos-virt-tool"},
		{"a link the new version does not have", v2Entries(t, testManifest()), ""},
		{"a link that is the same", withAlias(v2Entries(t, testManifest()), "casaos-virt-management"), "casaos-virt-management"},
	} {
		for _, fail := range []bool{false, true} {
			name := c.name + ", a good update"
			if fail {
				name = c.name + ", a rollback"
			}
			t.Run(name, func(t *testing.T) {
				f := newFixture(t, withEntries(withAlias(v1Entries(t), "casaos-virt-management")))
				if code := f.install("--yes"); code != ExitOK {
					t.Fatal(f.out.String())
				}
				f.publish(newTag, testManifest(), c.next)
				before := f.treeWithoutBookkeeping()
				f.gw.never = fail
				code := f.update("--yes")
				target, err := os.Readlink(f.path(aliasPath))
				switch {
				case fail && (code != ExitFailed || err != nil || target != "casaos-virt-management"):
					t.Errorf("rollback: exit %d, link %q %v\n%s", code, target, err, f.out)
				case fail:
					sameTree(t, before, f.treeWithoutBookkeeping())
				case code != ExitOK:
					t.Errorf("exit %d:\n%s", code, f.out)
				case c.want == "" && err == nil:
					t.Errorf("the link is still there: %q", target)
				case c.want != "" && (err != nil || target != c.want):
					t.Errorf("the link is %q, %v; want %q", target, err, c.want)
				}
				if !fail && c.want != "" != strings.Contains(f.read("/var/lib/casaos/modules/virt.files"), "l "+c.want+" "+aliasPath) {
					t.Errorf("the list of files:\n%s", f.read("/var/lib/casaos/modules/virt.files"))
				}
			})
		}
	}
}

// On a box with a merged /usr the unit is one file whichever way the lists spell its directory: an update whose archive spells it the other way replaces it, and a rollback puts it back.
func TestAnUpdateOnABoxWithAMergedUsrKnowsTheUnitUnderBothSpellings(t *testing.T) {
	spelled := func(ents []tent) []tent {
		out := slices.Clone(ents)
		for i := range out {
			out[i].name = strings.Replace(out[i].name, "build/sysroot/usr/lib/systemd/system/", "build/sysroot/lib/systemd/system/", 1)
		}
		return out
	}
	for _, fail := range []bool{false, true} {
		f := newFixture(t, withEntries(spelled(v1Entries(t))))
		if err := os.MkdirAll(f.path("/usr/lib"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("usr/lib", f.path("/lib")); err != nil {
			t.Fatal(err)
		}
		if code := f.install("--yes"); code != ExitOK {
			t.Fatalf("install: %d\n%s", code, f.out)
		}
		if !strings.Contains(f.read("/var/lib/casaos/modules/virt.files"), " /lib/systemd/system/"+testUnit) {
			t.Fatalf("the install did not list the unit as the archive spells it:\n%s", f.read("/var/lib/casaos/modules/virt.files"))
		}
		f.publish(newTag, testManifest(), v2Entries(t, testManifest())) // (the unit under usr/lib)
		before := f.treeWithoutBookkeeping()
		f.gw.never = fail
		code := f.update("--yes")
		if fail {
			if code != ExitFailed {
				t.Fatalf("exit %d:\n%s", code, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
			continue
		}
		if code != ExitOK {
			t.Fatalf("exit %d:\n%s", code, f.out)
		}
		list, err := osmodule.ParseFiles([]byte(f.read("/var/lib/casaos/modules/virt.files")))
		if err != nil || slices.Contains(list, "/lib/systemd/system/"+testUnit) || !slices.Contains(list, unitPath) {
			t.Errorf("the list: %v, %v", list, err)
		}
		if f.read("/usr/lib/systemd/system/"+testUnit) != unitV2 || f.read("/lib/systemd/system/"+testUnit) != unitV2 {
			t.Error("the unit is not the new one under both spellings")
		}
		if f.runner.called("systemctl", "stop") != 1 {
			t.Errorf("the units to stop: %q", f.runner.calls)
		}
	}
}
