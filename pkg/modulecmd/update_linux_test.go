package modulecmd

import (
	"archive/tar"
	"context"
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

const sentinelPath = "/usr/bin/casaos-sentinel"

// A path of the module that is a link in one version and a file in the other is replaced, by a rename that puts the new thing in the place of the old one: what the link points to is never
// written. Both ways, for a good update and for a rollback.
func TestALinkAndAFileOfTheModuleReplaceEachOtherAndNothingIsWrittenThroughTheLink(t *testing.T) {
	asFile := func(ents []tent) []tent {
		return append(slices.Clone(ents), tent{name: "build/sysroot" + aliasPath, body: "the alias, a file\n"})
	}
	for _, c := range []struct {
		name     string
		old, new []tent
		wantNew  func(t *testing.T, f *fixture)
		wantOld  func(t *testing.T, f *fixture)
	}{
		{"a link and then a file", withAlias(v1Entries(t), "casaos-sentinel"), asFile(v2Entries(t, testManifest())),
			func(t *testing.T, f *fixture) {
				if _, err := os.Readlink(f.path(aliasPath)); err == nil || f.read(aliasPath) != "the alias, a file\n" {
					t.Errorf("the alias is not the file: %v", err)
				}
			},
			func(t *testing.T, f *fixture) {
				if target, err := os.Readlink(f.path(aliasPath)); err != nil || target != "casaos-sentinel" {
					t.Errorf("the alias is not the link: %q %v", target, err)
				}
			}},
		{"a file and then a link", asFile(v1Entries(t)), withAlias(v2Entries(t, testManifest()), "casaos-sentinel"),
			func(t *testing.T, f *fixture) {
				if target, err := os.Readlink(f.path(aliasPath)); err != nil || target != "casaos-sentinel" {
					t.Errorf("the alias is not the link: %q %v", target, err)
				}
			},
			func(t *testing.T, f *fixture) {
				if _, err := os.Readlink(f.path(aliasPath)); err == nil || f.read(aliasPath) != "the alias, a file\n" {
					t.Errorf("the alias is not the file: %v", err)
				}
			}},
	} {
		for _, fail := range []bool{false, true} {
			t.Run(c.name+map[bool]string{false: ", an update", true: ", a rollback"}[fail], func(t *testing.T) {
				f := newFixture(t, withEntries(c.old))
				f.writeFile(sentinelPath, "the sentinel\n")
				if code := f.install("--yes"); code != ExitOK {
					t.Fatalf("install: %d\n%s", code, f.out)
				}
				f.publish(newTag, testManifest(), c.new)
				before := f.treeWithoutBookkeeping()
				f.gw.never = fail
				code := f.update("--yes")
				if f.read(sentinelPath) != "the sentinel\n" {
					t.Errorf("what the link points to was written: %q", f.read(sentinelPath))
				}
				if fail {
					if code != ExitFailed {
						t.Fatalf("exit %d:\n%s", code, f.out)
					}
					sameTree(t, before, f.treeWithoutBookkeeping())
					c.wantOld(t, f)
					return
				}
				if code != ExitOK {
					t.Fatalf("exit %d:\n%s", code, f.out)
				}
				c.wantNew(t, f)
			})
		}
	}
}

// A rollback gives a file its mode back, and its user and group when the assistant may (it is root on a box).
func TestARollbackGivesTheModeAndTheOwnerOfAFileBack(t *testing.T) {
	f := updatable(t)
	if err := os.Chmod(f.path(progPath), 0o750); err != nil {
		t.Fatal(err)
	}
	root := os.Geteuid() == 0
	if root {
		if err := os.Lchown(f.path(progPath), 4242, 4343); err != nil {
			t.Fatal(err)
		}
		if err := os.Lchown(f.path(samplePath), 4242, 4343); err != nil {
			t.Fatal(err)
		}
	}
	f.gw.never = true
	if code := f.update("--yes"); code != ExitFailed {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	info, err := os.Lstat(f.path(progPath))
	if err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("the mode: %v %v", info, err)
	}
	if root {
		if uid, gid, ok := ownerOf(info); !ok || uid != 4242 || gid != 4343 {
			t.Errorf("the owner: %d %d %v", uid, gid, ok)
		}
	}
}

// The last look at a file the update replaces is made when the new file is written and about to take its place: an edit that comes between the first look and that one is not written over.
func TestAFileEditedBetweenTheFirstLookAndTheRenameIsNotWrittenOver(t *testing.T) {
	f := updatable(t)
	old := afterClear
	t.Cleanup(func() { afterClear = old })
	temp := tempName(f.path(progPath))
	afterClear = func(p string) {
		if p == temp {
			_ = os.WriteFile(f.path(progPath), []byte("edited at the last moment"), 0o755)
		}
	}
	if code := f.update("--yes"); code != ExitFailed || !slices.Contains(f.lines(), "failed at step swap: "+osmodule.FileExists) {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if f.read(progPath) != "edited at the last moment" {
		t.Errorf("the file was written over: %q", f.read(progPath))
	}
}

// A link of the module that its administrator pointed elsewhere is theirs: the update does not replace it, and the rollback leaves it.
func TestALinkTheAdministratorPointedElsewhereIsNotReplaced(t *testing.T) {
	f := newFixture(t, withEntries(withAlias(v1Entries(t), "casaos-sentinel")))
	f.writeFile(sentinelPath, "the sentinel\n")
	if code := f.install("--yes"); code != ExitOK {
		t.Fatal(f.out.String())
	}
	f.publish(newTag, testManifest(), withAlias(v2Entries(t, testManifest()), "casaos-virt-tool"))
	f.env.StepHook = func(_ context.Context, step, phase string) {
		if step == StepStop && phase == "done" {
			_ = os.Remove(f.path(aliasPath))
			if err := os.Symlink("casaos-elsewhere", f.path(aliasPath)); err != nil {
				t.Error(err)
			}
		}
	}
	if code := f.update("--yes"); code != ExitFailed || !slices.Contains(f.lines(), "failed at step swap: "+osmodule.FileExists) || !strings.Contains(f.out.String(), " rolled_back ") {
		t.Fatalf("exit %d:\n%s", code, f.out)
	}
	if target, err := os.Readlink(f.path(aliasPath)); err != nil || target != "casaos-elsewhere" {
		t.Errorf("the link is %q, %v", target, err)
	}
	if f.read(progPath) != "#!/bin/sh\nexit 0\n" {
		t.Errorf("what the swap had done was not put back: %q", f.read(progPath))
	}
}
