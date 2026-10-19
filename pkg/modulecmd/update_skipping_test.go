package modulecmd

import (
	"strings"
	"testing"
)

// With --no-upgrade apt says "Skipping <package>, it is already installed and upgrade is not set." of each package that is there: an update of a module whose packages are all installed and
// none new is not refused, adds nothing, and runs no apt-get install.
func TestAnUpdateOfAModuleWhoseEveryPackageIsInstalledIsPlannedFromWhatAptSkips(t *testing.T) {
	f := updatable(t)
	text := aptProgress + "Skipping libvirt-clients, it is already installed and upgrade is not set.\nSkipping qemu-utils, it is already installed and upgrade is not set.\n" +
		"0 upgraded, 0 newly installed, 0 to remove and 12 not upgraded.\n"
	f.sandbox.sim.Text, f.sandbox.est.Text = text, text
	plan := f.planUpdate()
	if plan.Refused != "" || len(plan.Update.Added) != 0 || len(plan.Packages.ToInstall) != 0 || len(plan.Packages.ToUpgrade) != 0 {
		t.Fatalf("refused %q: %+v %+v", plan.Refused, plan.Checks, plan.Update)
	}
	if code := f.update("--yes"); code != ExitOK || f.runner.called("apt-get", "install") != 1 {
		t.Fatalf("exit %d, apt: %q\n%s", code, f.runner.calls, strings.Join(f.lines(), "\n"))
	}
}
