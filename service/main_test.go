package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain puts the flag of an open upgrade of the operating system in a folder of the tests' own: what the readers of it say (the packages window, the update of Docker, the
// readiness) must not depend on whether the machine that runs the tests has an upgrade open. A test that is about the flag sets its own path, and the one that is about where it is on a
// machine reads the constant.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "casaos-service-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	osUpgradeBusyFile = filepath.Join(dir, "busy")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestTheTestsNeverReadTheFlagOfTheMachineTheyRunOn(t *testing.T) {
	if osUpgradeBusyFile == osUpgradeBusyPath {
		t.Fatalf("the tests read %s", osUpgradeBusyPath)
	}
	// the one that the product reads and AppManagement looks at
	if osUpgradeBusyPath != "/var/lib/casaos/os-upgrade/busy" {
		t.Errorf("the flag is at %s", osUpgradeBusyPath)
	}
}
