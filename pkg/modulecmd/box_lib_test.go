package modulecmd

import (
	"context"
	"os"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// "/lib/x" is "/usr/lib/x" on a system whose /lib is a link to usr/lib, and two files on one where it is a directory of its own.

func TestTheBoxSaysWhetherItsLibIsALink(t *testing.T) {
	f := newFixture(t)
	bx := &box{ctx: context.Background(), env: &f.env}
	if merged, err := bx.MergedUsr(); merged || err != nil {
		t.Errorf("no /lib: %v, %v", merged, err)
	}
	f.writeFile("/lib/placeholder", "x")
	if merged, err := bx.MergedUsr(); merged || err != nil {
		t.Errorf("a directory: %v, %v", merged, err)
	}
	if err := os.RemoveAll(f.path("/lib")); err != nil {
		t.Fatal(err)
	}
	f.writeFile("/lib", "a file")
	if merged, err := bx.MergedUsr(); merged || err != nil {
		t.Errorf("a file: %v, %v", merged, err)
	}
	f.symlink("usr/lib", "/lib")
	if merged, err := bx.MergedUsr(); !merged || err != nil {
		t.Errorf("a link: %v, %v", merged, err)
	}
}

// through the commands: the unit of the module is the module's when its list names it with the other spelling, on a box whose /lib is a link, and a stranger's when it is not
func TestAUnitUnderLibIsTheModulesOnlyWhenLibIsALink(t *testing.T) {
	const unit = "/usr/lib/systemd/system/" + testUnit
	const lib = "/lib/systemd/system/" + testUnit
	f := newFixture(t)
	f.writeFile(unit, "[Unit]\n")
	f.writeFile("/var/lib/casaos/modules/virt.files", lib+"\n")
	// not a link: /lib is a directory of its own (or none), and the list names another file than the unit that is there
	if plan := f.json("plan", "virt"); plan["refused"] != osmodule.CheckForeignUnit {
		t.Errorf("/lib that is not a link: %v", plan["refused"])
	}
}

func TestAUnitUnderLibIsTheModulesWhenLibIsALink(t *testing.T) {
	const lib = "/lib/systemd/system/" + testUnit
	f := newFixture(t)
	f.writeFile("/usr/lib/systemd/system/"+testUnit, "[Unit]\n")
	f.symlink("usr/lib", "/lib")
	f.writeFile("/var/lib/casaos/modules/virt.files", lib+"\n")
	if plan := f.json("plan", "virt"); plan["refused"] != nil {
		t.Errorf("/lib that is a link: %v", plan["refused"])
	}
}
