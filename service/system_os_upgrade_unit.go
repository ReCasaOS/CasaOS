package service

import (
	"errors"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// osUpgradeUnitRoot is the folder that holds the run folders of the upgrades, as the unit sees it: the run folder of a run is osUpgradeUnitRoot/<run id>, and
// nothing else is a folder that a unit is started in.
const osUpgradeUnitRoot = "/var/lib/casaos/os-upgrade"

// osUpgradeWhy is what the inhibitor of the unit says to whoever asks the machine to shut down: the sentence of the measured hop. A var for the test that holds
// every word of the command line to the two characters that systemd takes for its own.
var osUpgradeWhy = "an operating system upgrade is running"

// osUpgradeUnitArgs are the arguments of systemd-run that start the unit of one attempt: the driver of the run, under an inhibitor that blocks shutdown and sleep (the
// measured hop ran under it; it is no guard: root's restart is not refused), with a clean environment and the nonce, the folder, the run, the phase and the number of
// the attempt in it. The shape is closed: the phase is one of osrun.Phases, the nonce is 32 lower case hex digits, the attempt is from 1 to 999 and dir is exactly
// osUpgradeUnitRoot/<the run id of the attempt>. No word of it holds a dollar sign or a percent sign: systemd replaces ${NAME} and $$ in the words of a command
// before the shell sees them, and takes % for the start of a specifier. The nonce is only ever in this environment, and in the markers that it signs.
func osUpgradeUnitArgs(a osrun.Attempt, dir string) ([]string, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	if dir != osUpgradeUnitRoot+"/"+a.RunID {
		return nil, errors.New("the folder of the run is not " + osUpgradeUnitRoot + "/<the run id>")
	}
	args := []string{
		"--quiet", "--no-block", "--collect", "--unit=" + strings.TrimSuffix(systemOSUpgradeUnit, ".service"), "--property=Type=exec",
		"--setenv=PATH=/usr/sbin:/usr/bin:/sbin:/bin", "--setenv=LC_ALL=C", "--setenv=DEBIAN_FRONTEND=noninteractive",
		"--setenv=APT_LISTCHANGES_FRONTEND=none", "--setenv=NEEDRESTART_SUSPEND=1", "--setenv=UCF_FORCE_CONFFOLD=1",
		"--setenv=CASAOS_OSU_NONCE=" + a.Nonce, "--setenv=CASAOS_OSU_DIR=" + dir, "--setenv=CASAOS_OSU_RUN=" + a.RunID,
		"--setenv=CASAOS_OSU_PHASE=" + a.Phase, "--setenv=CASAOS_OSU_ATTEMPT=" + strconv.Itoa(a.N),
		"--", "systemd-inhibit", "--what=shutdown:sleep", "--who=casaos-os-upgrade", "--why=" + osUpgradeWhy, "--mode=block",
		"/bin/sh", dir + "/driver.sh",
	}
	for _, arg := range args {
		if strings.ContainsAny(arg, "$%") {
			return nil, errors.New("a word of the command line of the unit holds a $ or a %, which systemd would take for its own")
		}
	}
	return args, nil
}
