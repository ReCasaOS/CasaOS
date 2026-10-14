package service

import (
	"path/filepath"
	"testing"
)

// TheUpgradeBox is what the tests of the packages that use the service (the routes) see of a machine of the upgrade tests: the engine, which keeps its files in folders of its
// own, the folder that holds all of them, what was asked of systemd, and what was asked of the machine's power (which is never the real one).
type TheUpgradeBox struct {
	System  SystemService
	Folder  string
	Asked   func() []string
	Powered func() []string
}

// NewTheUpgradeBox makes the box: no flag file, no run, nothing on it that the product did not put there.
func NewTheUpgradeBox(t *testing.T) TheUpgradeBox {
	t.Helper()
	box := newUpgradeBox(t)
	box.updater.dpkgLocked = func() bool { return false }
	power := aPowerButton(t, nil)
	return TheUpgradeBox{System: box.systemService, Folder: filepath.Dir(filepath.Dir(box.root)), Asked: func() []string { return box.commandsWith("") }, Powered: power.asked}
}
