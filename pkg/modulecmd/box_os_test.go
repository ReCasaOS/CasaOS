package modulecmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
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
			if o := bx.OS(); o.ID != c.wantID || o.Version != c.wantV {
				t.Errorf("the box is %q %q, not %q %q", o.ID, o.Version, c.wantID, c.wantV)
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

// Linux Mint 22.2, as its /etc/os-release is (Ubuntu 24.04 underneath)
const mint222 = "NAME=\"Linux Mint\"\nVERSION=\"22.2 (Zara)\"\nID=linuxmint\nID_LIKE=\"ubuntu debian\"\nPRETTY_NAME=\"Linux Mint 22.2\"\nVERSION_ID=\"22.2\"\nVERSION_CODENAME=zara\nUBUNTU_CODENAME=noble\n"

// A derivative is read as the release it is built on only by a codename of the table, through the nearer of debian and ubuntu that ID_LIKE names; a Debian or an Ubuntu is read as itself.
func TestADerivativeIsReadAsItsBaseByACodenameOfTheTable(t *testing.T) {
	for _, c := range []struct {
		name string
		text string
		want osmodule.OSRelease
	}{
		{"Linux Mint 22.2", mint222, osmodule.OSRelease{ID: "ubuntu", Version: "24.04", From: "linuxmint:22.2, UBUNTU_CODENAME=noble"}},
		{"LMDE 6, on Debian 12", "ID=linuxmint\nID_LIKE=debian\nVERSION_ID=6\nDEBIAN_CODENAME=bookworm\n", osmodule.OSRelease{ID: "debian", Version: "12", From: "linuxmint:6, DEBIAN_CODENAME=bookworm"}},
		{"a derivative of Ubuntu 22.04", "ID=pop\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"22.04\"\nUBUNTU_CODENAME=jammy\n", osmodule.OSRelease{ID: "ubuntu", Version: "22.04", From: "pop:22.04, UBUNTU_CODENAME=jammy"}},
		{"a derivative of Ubuntu 26.04", "ID=zorin\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=19\nUBUNTU_CODENAME=resolute\n", osmodule.OSRelease{ID: "ubuntu", Version: "26.04", From: "zorin:19, UBUNTU_CODENAME=resolute"}},
		{"a derivative with a codename the table does not know", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"21.3\"\nUBUNTU_CODENAME=focal\n", osmodule.OSRelease{ID: "linuxmint", Version: "21.3"}},
		{"a codename of the table under the other key", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"22.2\"\nDEBIAN_CODENAME=noble\n", osmodule.OSRelease{ID: "linuxmint", Version: "22.2"}},
		{"Ubuntu named first and only a Debian codename", "ID=x\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=1\nDEBIAN_CODENAME=trixie\n", osmodule.OSRelease{ID: "x", Version: "1"}},
		{"no codename", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"22.2\"\n", osmodule.OSRelease{ID: "linuxmint", Version: "22.2"}},
		{"a codename in capitals", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"22.2\"\nUBUNTU_CODENAME=Noble\n", osmodule.OSRelease{ID: "linuxmint", Version: "22.2"}},
		{"an ID_LIKE that names neither", "ID=fedora-x\nID_LIKE=\"rhel fedora\"\nVERSION_ID=40\nUBUNTU_CODENAME=noble\n", osmodule.OSRelease{ID: "fedora-x", Version: "40"}},
		{"no ID_LIKE", "ID=linuxmint\nVERSION_ID=\"22.2\"\nUBUNTU_CODENAME=noble\n", osmodule.OSRelease{ID: "linuxmint", Version: "22.2"}},
		{"ID_LIKE with a name that only contains ubuntu", "ID=x\nID_LIKE=ubuntu-like\nVERSION_ID=1\nUBUNTU_CODENAME=noble\n", osmodule.OSRelease{ID: "x", Version: "1"}},
		{"a derivative with no version", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nUBUNTU_CODENAME=noble\n", osmodule.OSRelease{}},
		{"Ubuntu 24.04", "ID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"24.04\"\nUBUNTU_CODENAME=noble\n", osmodule.OSRelease{ID: "ubuntu", Version: "24.04"}},
		{"Ubuntu with a codename that does not match its version", "ID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"20.04\"\nUBUNTU_CODENAME=noble\n", osmodule.OSRelease{ID: "ubuntu", Version: "20.04"}},
		{"Debian 12", debian12, osmodule.OSRelease{ID: "debian", Version: "12"}},
		{"Debian 11", "ID=debian\nVERSION_ID=\"11\"\nVERSION_CODENAME=bullseye\n", osmodule.OSRelease{ID: "debian", Version: "11"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := parseOSRelease([]byte(c.text)); got != c.want {
				t.Errorf("%+v, not %+v", got, c.want)
			}
		})
	}
}
