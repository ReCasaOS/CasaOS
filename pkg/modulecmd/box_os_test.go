package modulecmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What the box says it is, read the way a real box has the file: on Debian and on Ubuntu /etc/os-release is a link to ../usr/lib/os-release.

const debian12 = "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nNAME=\"Debian GNU/Linux\"\nVERSION_ID=\"12\"\nVERSION=\"12 (bookworm)\"\nVERSION_CODENAME=bookworm\nID=debian\n"

// symlink makes a link in the box, or ends the test where the system does not let a test make one.
func (f *fixture) symlink(target, abs string) {
	f.t.Helper()
	p := f.path(abs)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	_ = os.Remove(p)
	if err := os.Symlink(target, p); err != nil {
		f.t.Skipf("no links here: %v", err)
	}
}

// bareOS is a box with no os-release at all.
func bareOS(f *fixture) {
	f.t.Helper()
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		_ = os.Remove(f.path(p))
	}
}

func TestTheOSIsReadAsTheFileIsOnARealBox(t *testing.T) {
	for _, c := range []struct {
		name          string
		set           func(f *fixture)
		wantID, wantV string
	}{
		{"a link to ../usr/lib/os-release, as Debian and Ubuntu have it", func(f *fixture) {
			bareOS(f)
			f.writeFile("/usr/lib/os-release", debian12)
			f.symlink("../usr/lib/os-release", "/etc/os-release")
		}, "debian", "12"},
		{"a regular file", func(f *fixture) {
			bareOS(f)
			f.writeFile("/etc/os-release", debian12)
		}, "debian", "12"},
		{"no file in /etc, the one in /usr/lib", func(f *fixture) {
			bareOS(f)
			f.writeFile("/usr/lib/os-release", debian12)
		}, "debian", "12"},
		{"a link that leads nowhere, and the one in /usr/lib", func(f *fixture) {
			bareOS(f)
			f.writeFile("/usr/lib/os-release", debian12)
			f.symlink("../usr/lib/nothing", "/etc/os-release")
		}, "debian", "12"},
		{"a link that leads nowhere and no other file", func(f *fixture) {
			bareOS(f)
			f.symlink("../usr/lib/nothing", "/etc/os-release")
		}, "", ""},
		{"a link to itself", func(f *fixture) {
			bareOS(f)
			f.writeFile("/usr/lib/os-release", debian12)
			f.symlink("os-release", "/etc/os-release")
		}, "", ""},
		{"a link to a directory", func(f *fixture) {
			bareOS(f)
			f.writeFile("/usr/lib/os-release", debian12)
			f.symlink("../usr/lib", "/etc/os-release")
		}, "", ""},
		{"a directory", func(f *fixture) {
			bareOS(f)
			f.writeFile("/usr/lib/os-release", debian12)
			if err := os.MkdirAll(f.path("/etc/os-release"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "", ""},
		{"a file that is too large, whatever /usr/lib has", func(f *fixture) {
			bareOS(f)
			f.writeFile("/usr/lib/os-release", debian12)
			f.writeFile("/etc/os-release", debian12+"#"+strings.Repeat("x", 70<<10)+"\n")
		}, "", ""},
		{"Ubuntu, with the version in quotes", func(f *fixture) {
			bareOS(f)
			f.writeFile("/usr/lib/os-release", "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nID=ubuntu\nID_LIKE=debian\n")
			f.symlink("../usr/lib/os-release", "/etc/os-release")
		}, "ubuntu", "24.04"},
		{"the version in single quotes", func(f *fixture) { bareOS(f); f.writeFile("/etc/os-release", "ID='debian'\nVERSION_ID='12'\n") }, "debian", "12"},
		{"the version with no quotes", func(f *fixture) { bareOS(f); f.writeFile("/etc/os-release", "ID=debian\nVERSION_ID=12\n") }, "debian", "12"},
		{"no version (Debian testing)", func(f *fixture) { bareOS(f); f.writeFile("/etc/os-release", "ID=debian\nVERSION_CODENAME=trixie\n") }, "", ""},
		{"a version that is not a plain word", func(f *fixture) { bareOS(f); f.writeFile("/etc/os-release", "ID=debian\nVERSION_ID=\"12; x\"\n") }, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.set(f)
			bx := &box{ctx: context.Background(), env: &f.env}
			if id, v := bx.OS(); id != c.wantID || v != c.wantV {
				t.Errorf("the box is %q %q, not %q %q", id, v, c.wantID, c.wantV)
			}
		})
	}
}

// the real layout, through the commands: the plan and the list do not turn a box away for the way its file is kept
func TestABoxWithTheRealLayoutOfOSReleaseIsNotTurnedAway(t *testing.T) {
	f := newFixture(t)
	bareOS(f)
	f.writeFile("/usr/lib/os-release", debian12)
	f.symlink("../usr/lib/os-release", "/etc/os-release")
	if p := f.json("plan", "virt"); p["refused"] != nil {
		t.Errorf("the plan is refused: %v", p["refused"])
	}
	list := f.json("list")
	mods, _ := list["modules"].([]any)
	if len(mods) != 1 {
		t.Fatalf("the list: %v", list)
	}
	if sys, _ := mods[0].(map[string]any)["system"].(map[string]any); sys["supported"] != true {
		t.Errorf("the system is not supported: %v", sys)
	}
}
