package service

import (
	"errors"
	"io/fs"
	"os"
	"sync/atomic"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// systemOSUpgradeUnit is the transient unit that runs the blocks of an upgrade of the operating system: one at a time, whatever it is for.
const systemOSUpgradeUnit = "casaos-os-upgrade.service"

// osUpgradeBusyFile is the flag of an upgrade that is open: while it is there, what changes the packages of the box (the packages window, the update
// of Docker, the update of ReCasaOS, the automatic updates, the backups of the apps) stays away, because dpkg rewrites files that they read. The core
// writes it and removes it, and nothing else does. A var, for the tests.
//
// It is read fail closed: only a flag that is not there is no flag. A folder that cannot be read, a disk that answers with an error, a name that is
// not valid, anything but "no such file" says nothing of whether the upgrade is open, and an upgrade that is open and not seen is a backup that reads a
// machine that dpkg is rewriting.
var osUpgradeBusyFile = osUpgradeBusyPath

// osUpgradeBusyPath is where the flag is on a machine, and the file that AppManagement and the other readers look at.
const osUpgradeBusyPath = "/var/lib/casaos/os-upgrade/busy"

// osUpgradeFlagOpen says whether the flag of an open upgrade is there. When it could not be told err says why, and the answer is open all the same.
func osUpgradeFlagOpen() (open bool, err error) {
	_, err = os.Lstat(osUpgradeBusyFile)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return true, err
}

// osUpgradeFlagLogged is when the flag that could not be read was last said in the log: the readers ask every few seconds.
var osUpgradeFlagLogged atomic.Int64

// osUpgradeBusyReason is why the box is busy for an upgrade that is open, and false when it is not. except is the unit of the caller, as in
// maintenanceState: the upgrade's own start does not count its flag against itself.
func osUpgradeBusyReason(except string) (string, bool) {
	if except == systemOSUpgradeUnit {
		return "", false
	}
	open, err := osUpgradeFlagOpen()
	if err != nil {
		if now := time.Now().UnixNano(); now-osUpgradeFlagLogged.Load() >= int64(time.Minute) {
			osUpgradeFlagLogged.Store(now)
			logger.Error("the flag of an operating system upgrade could not be read: the upgrade is taken for open", zap.String("path", osUpgradeBusyFile), zap.Error(err))
		}
	}
	if !open {
		return "", false
	}
	return "an operating system upgrade is open", true
}
