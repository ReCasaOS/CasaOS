package osplan

import (
	"regexp"
	"slices"
)

// The allow-list of the sandbox: what may run is exactly the four commands of this rung, built here and compared by
// equality, so that nothing else (another verb, another option, another path, the same command with one word more) is
// ever handed to Sandbox.Run. A test pins each of them, and each way of getting it wrong.

// sandboxDirPattern is the name of a sandbox directory, whole path: the parent, the prefix and an alphanumeric suffix
// (os.MkdirTemp gives digits). No step up, no repeated slash, no trailing slash, nothing a shell would read.
var sandboxDirPattern = regexp.MustCompile(`^` + regexp.QuoteMeta(SandboxParent) + `/` + regexp.QuoteMeta(SandboxPrefix) + `[A-Za-z0-9]{1,32}$`)

var sandboxDirName = regexp.MustCompile(`^` + regexp.QuoteMeta(SandboxPrefix) + `[A-Za-z0-9]{1,32}$`)

// ValidSandboxDir says whether a path is the path of a sandbox directory: absolute, under SandboxParent, named as
// SandboxPrefix and a short alphanumeric suffix make a name. It is the one pattern: the allow-list, the removal of the
// directory and the sweep of the stale ones use it.
func ValidSandboxDir(dir string) bool { return sandboxDirPattern.MatchString(dir) }

// IsSandboxDirName says whether the name of an entry of SandboxParent is that of a sandbox directory.
func IsSandboxDirName(name string) bool { return sandboxDirName.MatchString(name) }

// InsideSandbox says whether a path is strictly inside the sandbox directory dir (a clean path, no step up): what
// Sandbox.WriteFile of the service checks before it writes.
func InsideSandbox(dir, p string) bool { return ValidSandboxDir(dir) && inside(dir, p) }

// sandboxOptions are the options of every command of the sandbox, in this order and no other: the measured recipe (the
// sources, the lists and the cache of apt in the sandbox directory, no locking) and the retries of the real hop's apt. All
// the paths are inside dir.
func sandboxOptions(dir string) []string {
	return []string{
		"-o", "Dir::Etc::sourcelist=" + dir + "/sources.list",
		"-o", "Dir::Etc::sourceparts=" + dir + "/parts",
		"-o", "Dir::State::lists=" + dir + "/lists",
		"-o", "Dir::Cache=" + dir + "/cache",
		"-o", "Debug::NoLocking=true",
		"-o", "Acquire::Retries=3",
	}
}

func sandboxArgv(dir string, tail ...string) []string {
	return slices.Concat([]string{"apt-get"}, sandboxOptions(dir), tail)
}

// UpdateArgv is "apt-get update" of the sandbox: it fetches the metadata of the target release into the sandbox's lists.
func UpdateArgv(dir string) []string { return sandboxArgv(dir, "update", "--error-on=any") }

// UpgradeArgv is the simulation of the minimal upgrade.
func UpgradeArgv(dir string) []string { return sandboxArgv(dir, "-s", "upgrade") }

// DistUpgradeArgv is the simulation of the full upgrade.
func DistUpgradeArgv(dir string) []string { return sandboxArgv(dir, "-s", "dist-upgrade") }

// EstimateArgv reads the sizes. Whenever the transaction has an operation to do (a package to upgrade, to install, to remove or to
// downgrade) or dpkg has work pending (a package half configured), apt stops at its prompt step with "E: Trivial Only specified
// but this is not a trivial operation." (exit status 100, its normal end here) before it fetches or installs anything, having
// printed "Need to get ..." and "After this operation ..." that "-s" does not print. When the file system of the sandbox's cache
// has less room than the download it ends, with the same status, on "E: You don't have enough free space in
// <dir>/cache/archives/." instead (ParseEstimate reads both). When there is nothing to do at all (0 upgraded, 0 newly installed, 0
// to remove) apt has no operation to stop at: it ends with status 0, no error and no sizes, after a real dist-upgrade of nothing,
// which changes nothing (testdata/real/chroot-d11-nothing-size.txt; the status file of dpkg is as it was afterwards), and
// ParseEstimate reads that end as sizes that are known to be zero. Trivial-Only is allowed with this verb and no
// other. This was run for real on apt 2.2.4 and on apt 2.6.1, in Debian 11 and Debian 12 chroots: the status 100 and the text
// are as said (the Debian 11 one, taken to bookworm with the real lists of the mirror, is testdata/real/chroot-d11-12-size.txt,
// and with a small tmpfs on the archives testdata/real/chroot-d11-12-lowroom-size.txt). The guest of the measurement of the
// hop was not asked it.
func EstimateArgv(dir string) []string {
	return sandboxArgv(dir, "-o", "APT::Get::Trivial-Only=true", "dist-upgrade")
}

// AllowedSandboxArgv says whether argv is one of the four commands this rung runs, for the sandbox directory dir, and
// nothing else: the verb is pinned, so are the options, their order and their paths. A directory that is not a sandbox
// directory (ValidSandboxDir) has no allowed command.
func AllowedSandboxArgv(argv []string, dir string) bool {
	if !ValidSandboxDir(dir) {
		return false
	}
	for _, want := range [][]string{UpdateArgv(dir), UpgradeArgv(dir), DistUpgradeArgv(dir), EstimateArgv(dir)} {
		if slices.Equal(argv, want) {
			return true
		}
	}
	return false
}

// SandboxEnv is the whole environment of a command of the sandbox: a clean PATH, English, no questions, the temporary
// files in the sandbox's tmp directory (the one directory of it that apt's own user may write in: apt falls back to /tmp for
// a user that cannot use TMPDIR), and apt's own configuration file, which keeps the hooks of /etc/apt/apt.conf.d from
// running (see AptConfig). Everything else of the core's environment is unset.
func SandboxEnv(dir string) []string {
	return []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		"LC_ALL=C",
		"LANG=C",
		"DEBIAN_FRONTEND=noninteractive",
		"TMPDIR=" + dir + "/" + SandboxTmp,
		"APT_CONFIG=" + dir + "/apt-config",
	}
}
