package osplan

import (
	"slices"
	"strings"
	"testing"
)

// moduleOpts is the options of the simulation of a module, written out as they are meant (and not built by the code that is tested): the cache of apt in the sandbox directory, no
// locking, no log of the planner. The sources and the lists are the box's own, because the install that follows uses the lists the box has.
func moduleOpts(d string) []string {
	return []string{"-o", "Dir::Cache=" + d + "/cache", "-o", "Debug::NoLocking=true", "-o", "Dir::Log::Planner="}
}

func moduleArgvOf(d string, tail ...string) []string {
	return slices.Concat([]string{"apt-get"}, moduleOpts(d), tail)
}

func TestTheArgvsOfTheSimulationOfAModuleAreWhatTheyAreMeantToBe(t *testing.T) {
	pkgs := []string{"libvirt-clients", "qemu-utils"}
	sim, err := ModuleSimArgv(testDir, pkgs)
	if want := moduleArgvOf(testDir, "-s", "install", "--no-install-recommends", "libvirt-clients", "qemu-utils"); err != nil || !slices.Equal(sim, want) {
		t.Errorf("simulation:\n got %q (%v)\nwant %q", sim, err, want)
	}
	est, err := ModuleEstimateArgv(testDir, pkgs)
	if want := moduleArgvOf(testDir, "--print-uris", "install", "--no-install-recommends", "libvirt-clients", "qemu-utils"); err != nil || !slices.Equal(est, want) {
		t.Errorf("estimate:\n got %q (%v)\nwant %q", est, err, want)
	}
	// ...and the estimate is not a command that can install: it has none of the options that make apt go on, and not the one that apt does not honour for a transaction of one package per
	// name
	for _, word := range est {
		if word == "-y" || word == "--yes" || word == "-s" || strings.Contains(word, "Trivial-Only") || strings.Contains(word, "Assume") {
			t.Errorf("the estimate has the word %q", word)
		}
	}
	if !slices.Contains(est, "--print-uris") || slices.Contains(sim, "--print-uris") || !slices.Contains(sim, "-s") {
		t.Errorf("the words that tell the two apart: %q and %q", sim, est)
	}
	// both ask apt for no log of the planner, which it writes as root for a simulation
	for name, a := range map[string][]string{"simulation": sim, "estimate": est} {
		i := slices.Index(a, "Dir::Log::Planner=")
		if i < 1 || a[i-1] != "-o" {
			t.Errorf("the %s does not turn the log of the planner off: %q", name, a)
		}
	}
	// the commands run in English: the parser reads apt's own words and a translation would not be understood
	if env := SandboxEnv(testDir); !slices.Contains(env, "LC_ALL=C") || !slices.Contains(env, "LANG=C") || !slices.Contains(env, "DEBIAN_FRONTEND=noninteractive") {
		t.Errorf("the environment of the sandbox: %q", env)
	}
}

func packageNames(n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, "pkg-"+strings.Repeat("a", i/26)+string(rune('a'+i%26)))
	}
	return out
}

func TestAPackageNameIsCheckedBeforeItReachesACommandLine(t *testing.T) {
	for _, bad := range []string{"", "-y", "--force-yes", "-s", "a b", "pkg-", "pkg+", "pkg=1.0", "pkg/stable", "pkg:i386", "Pkg", "a", "pkg\n", "../x", "pkg;reboot", "$(id)", "`id`", "pkg*", "pkg.deb", "./pkg", "pkg\x00", "pkg ", " pkg", strings.Repeat("p", 65)} {
		if _, err := ModuleSimArgv(testDir, []string{"good-one", bad}); err == nil {
			t.Errorf("the simulation takes the package %q", bad)
		}
		if _, err := ModuleEstimateArgv(testDir, []string{bad}); err == nil {
			t.Errorf("the estimate takes the package %q", bad)
		}
	}
	for _, good := range []string{"pkg", "libstdc++6", "g++-12", "qemu-system-x86", "libvirt0.1", strings.Repeat("p", 64), "7zip"} {
		if _, err := ModuleSimArgv(testDir, []string{good}); err != nil {
			t.Errorf("the package %q: %v", good, err)
		}
	}
	// no package, a package twice, and the most there are
	for name, list := range map[string][]string{"none": nil, "an empty list": {}, "twice": {"pkg-a", "pkg-a"}, "41": packageNames(41)} {
		if _, err := ModuleSimArgv(testDir, list); err == nil {
			t.Errorf("the simulation takes %s", name)
		}
	}
	if _, err := ModuleSimArgv(testDir, packageNames(40)); err != nil {
		t.Errorf("40 packages: %v", err)
	}
	// ...and a directory that is not a sandbox directory is no place to run
	for _, dir := range []string{"", "/tmp", "/var/tmp/casaos-os-plan.", "/var/tmp/casaos-os-plan.1/../x", "/var/tmp/casaos-os-plan.12 34", "/var/tmp/other.123", testDir + "/"} {
		if argv, err := ModuleSimArgv(dir, []string{"pkg-a"}); err == nil {
			t.Errorf("the directory %q: %q", dir, argv)
		}
		if argv, err := ModuleEstimateArgv(dir, []string{"pkg-a"}); err == nil {
			t.Errorf("the directory %q: %q", dir, argv)
		}
	}
}

func TestTheAllowListTakesTheTwoCommandsOfAModuleAndNothingNear(t *testing.T) {
	pkgs := []string{"libvirt-clients", "qemu-utils"}
	sim, _ := ModuleSimArgv(testDir, pkgs)
	est, _ := ModuleEstimateArgv(testDir, pkgs)
	for name, a := range map[string][]string{"simulation": sim, "estimate": est} {
		if !AllowedModuleArgv(a, testDir) {
			t.Errorf("%s is refused: %q", name, a)
		}
		if AllowedSandboxArgv(a, testDir) {
			t.Errorf("%s is a command of the plan of the upgrade", name)
		}
		// what is not exactly it: every word changed, taken out, or added to
		for i := range a {
			if i >= len(a)-len(pkgs) {
				continue // (another valid package is another valid command)
			}
			changed := slices.Clone(a)
			changed[i] = "x"
			if AllowedModuleArgv(changed, testDir) {
				t.Errorf("%s with word %d changed: %q", name, i, changed)
			}
			if AllowedModuleArgv(slices.Delete(slices.Clone(a), i, i+1), testDir) {
				t.Errorf("%s without word %d", name, i)
			}
			if AllowedModuleArgv(slices.Insert(slices.Clone(a), i, "--allow-unauthenticated"), testDir) {
				t.Errorf("%s with a word before word %d", name, i)
			}
		}
		for _, extra := range []string{"-y", "--allow-unauthenticated", "-o", "pkg-", "pkg+", "--", "a b", "Pkg", "", "-s", "x;y", "pkg.deb"} {
			if AllowedModuleArgv(append(slices.Clone(a), extra), testDir) {
				t.Errorf("%s with %q at the end", name, extra)
			}
		}
		if AllowedModuleArgv(a, "/var/tmp/casaos-os-plan.999") || AllowedModuleArgv(a, "/tmp") || AllowedModuleArgv(a, "") {
			t.Errorf("%s for another directory", name)
		}
	}
	// a directory that is not a sandbox directory, with a command built for it by hand
	for _, dir := range []string{"/tmp", "/", "/var/tmp/casaos-os-plan.", "/var/tmp/other.1"} {
		if AllowedModuleArgv(moduleArgvOf(dir, "-s", "install", "--no-install-recommends", "pkg-a"), dir) {
			t.Errorf("the directory %q", dir)
		}
	}
	// the verb
	for _, verb := range []string{"remove", "purge", "upgrade", "dist-upgrade", "update", "autoremove", "download", "source"} {
		a := slices.Clone(sim)
		a[slices.Index(a, "install")] = verb
		if AllowedModuleArgv(a, testDir) {
			t.Errorf("the verb %s", verb)
		}
	}
	// no command, no package
	if AllowedModuleArgv(nil, testDir) || AllowedModuleArgv(moduleArgvOf(testDir, "-s", "install", "--no-install-recommends"), testDir) || AllowedModuleArgv(moduleArgvOf(testDir), testDir) {
		t.Error("a command with no package")
	}
	// the four commands of the plan of the upgrade are not commands of a module
	for name, a := range fourArgvs(testDir) {
		if AllowedModuleArgv(a, testDir) {
			t.Errorf("%s is a command of a module", name)
		}
	}
	// the head of the simulation and the packages of the estimate do not make a command
	mixed := slices.Concat(sim[:10], est[10:])
	if !AllowedModuleArgv(mixed, testDir) {
		t.Errorf("the same packages after the head of the simulation: %q", mixed)
	}
	if AllowedModuleArgv(slices.Concat(sim[:9], est[10:]), testDir) {
		t.Error("a head that is neither")
	}
	// the two commands are not the ones of an earlier version: no trivial-only estimate (which installs a transaction of one package), no log of the planner left on, and no verb with
	// the other's option
	for name, a := range map[string][]string{
		"the estimate that apt installs with": moduleArgvOf(testDir, "-o", "APT::Get::Trivial-Only=true", "install", "--no-install-recommends", "pkg-a"),
		"the simulation without the log off":  {"apt-get", "-o", "Dir::Cache=" + testDir + "/cache", "-o", "Debug::NoLocking=true", "-s", "install", "--no-install-recommends", "pkg-a"},
		"the estimate without the log off":    {"apt-get", "-o", "Dir::Cache=" + testDir + "/cache", "-o", "Debug::NoLocking=true", "--print-uris", "install", "--no-install-recommends", "pkg-a"},
		"the log turned on":                   {"apt-get", "-o", "Dir::Cache=" + testDir + "/cache", "-o", "Debug::NoLocking=true", "-o", "Dir::Log::Planner=/var/log/x", "--print-uris", "install", "--no-install-recommends", "pkg-a"},
		"both options":                        moduleArgvOf(testDir, "-s", "--print-uris", "install", "--no-install-recommends", "pkg-a"),
		"the estimate with the yes":           moduleArgvOf(testDir, "--print-uris", "-y", "install", "--no-install-recommends", "pkg-a"),
		"the estimate with the install after": moduleArgvOf(testDir, "install", "--print-uris", "--no-install-recommends", "pkg-a"),
	} {
		if AllowedModuleArgv(a, testDir) {
			t.Errorf("%s is allowed: %q", name, a)
		}
	}
}

// ---- what apt says -----------------------------------------------------------------------------------------------------------------------------------------------------

const progressLines = "Reading package lists...\nBuilding dependency tree...\nReading state information...\n"

// the simulation of installing three packages, with the lists that apt prints on top of the NEW ones (the dependencies, the suggestions)
const simInstall = progressLines + `The following additional packages will be installed:
  libfdt1 libiscsi7 libvirt-daemon libvirt0 qemu-utils
Suggested packages:
  libvirt-daemon-driver-storage-gluster libvirt-daemon-driver-storage-rbd
  numad radvd
Recommended packages:
  qemu-system-x86
The following NEW packages will be installed:
  qemu-utils libvirt0 libfdt1 libiscsi7 libvirt-daemon libvirt-clients
The following packages will be upgraded:
  zlib1g libc6
2 upgraded, 6 newly installed, 0 to remove and 3 not upgraded.
Inst libc6 [2.31-13+deb11u11] (2.36-9+deb12u14 Debian:12.15/oldstable [amd64])
Inst libfdt1 (1.6.1-4+b1 Debian:12.15/oldstable [amd64])
Inst libiscsi7 (1.19.0-3 Debian:12.15/oldstable [amd64])
Inst libvirt0 (9.0.0-4 Debian:12.15/oldstable [amd64])
Inst libvirt-daemon (9.0.0-4 Debian:12.15/oldstable [amd64])
Inst libvirt-clients (9.0.0-4 Debian:12.15/oldstable [amd64])
Inst qemu-utils (1:7.2+dfsg-7+deb12u16 Debian:12.15/oldstable [amd64])
Inst zlib1g [1:1.2.11.dfsg-2+deb11u2] (1:1.2.13.dfsg-1 Debian:12.15/oldstable [amd64])
Conf libc6 (2.36-9+deb12u14 Debian:12.15/oldstable [amd64])
Conf libfdt1 (1.6.1-4+b1 Debian:12.15/oldstable [amd64])
`

// the same transaction with --print-uris: it says the sizes, prints the addresses and ends with status 0
const estInstall = progressLines + `The following additional packages will be installed:
  libfdt1 libiscsi7 libvirt-daemon libvirt0 qemu-utils
Suggested packages:
  numad radvd
The following NEW packages will be installed:
  libfdt1 libiscsi7 libvirt-clients libvirt-daemon libvirt0 qemu-utils
The following packages will be upgraded:
  libc6 zlib1g
2 upgraded, 6 newly installed, 0 to remove and 3 not upgraded.
Need to get 5,120 kB/44.3 MB of archives.
After this operation, 130 MB of additional disk space will be used.
'http://deb.debian.org/debian/pool/main/libv/libvirt/libvirt0_9.0.0-4_amd64.deb' libvirt0_9.0.0-4_amd64.deb 1459712 SHA256:0123456789abcdef
'http://deb.debian.org/debian/pool/main/q/qemu/qemu-utils_7.2+dfsg-7+deb12u16_amd64.deb' qemu-utils_1%3a7.2+dfsg-7+deb12u16_amd64.deb 1168352 SHA256:fedcba9876543210
`

func out(text string, code int) Output { return Output{Text: text, ExitCode: code} }

// asked are the packages that simInstall is the simulation of
var asked = []string{"libvirt-clients", "qemu-utils"}

func TestWhatAptSaysOfInstallingPackagesIsReadAsPackagesAndSizes(t *testing.T) {
	res := ParseModuleSimulation(out(simInstall, 0), out(estInstall, 0), asked)
	if res.Failure != "" {
		t.Fatalf("failure %q: %s", res.Failure, res.Detail)
	}
	if !slices.Equal(res.ToInstall, []string{"libfdt1", "libiscsi7", "libvirt-clients", "libvirt-daemon", "libvirt0", "qemu-utils"}) || !slices.Equal(res.ToUpgrade, []string{"libc6", "zlib1g"}) {
		t.Errorf("install %v, upgrade %v", res.ToInstall, res.ToUpgrade)
	}
	if !res.Known || res.DownloadBytes != 44_300_000 || res.ExtraBytes != 130_000_000 {
		t.Errorf("sizes: known %v, %d, %d", res.Known, res.DownloadBytes, res.ExtraBytes)
	}
	if len(res.Removes) != 0 || len(res.Downgrades) != 0 || len(res.Held) != 0 {
		t.Errorf("%+v", res)
	}
}

func TestInstallingWhatIsThereAlreadyIsAnInstallOfNothing(t *testing.T) {
	sim := progressLines + "libvirt-clients is already the newest version (9.0.0-4).\nqemu-utils is already the newest version (1:7.2+dfsg-7+deb12u16).\n" +
		"0 upgraded, 0 newly installed, 0 to remove and 4 not upgraded.\n"
	res := ParseModuleSimulation(out(sim, 0), out(sim, 0), asked)
	if res.Failure != "" || len(res.ToInstall) != 0 || len(res.ToUpgrade) != 0 || !res.Known || res.DownloadBytes != 0 || res.ExtraBytes != 0 {
		t.Errorf("%+v", res)
	}
}

func TestARemovalIsASingleLineOfTheListOfThoseRemovedAndAReplacementIsNone(t *testing.T) {
	removing := progressLines + `The following packages will be REMOVED:
  libvirt0
The following NEW packages will be installed:
  libvirt-clients
0 upgraded, 1 newly installed, 1 to remove and 0 not upgraded.
Remv libvirt0 [9.0.0-3]
Inst libvirt-clients (9.0.0-4 Debian:12.15/oldstable [amd64])
`
	res := ParseModuleSimulation(out(removing, 0), out("", 0), []string{"libvirt-clients"})
	if res.Failure != "" || !slices.Equal(res.Removes, []string{"libvirt0"}) || !slices.Equal(res.ToInstall, []string{"libvirt-clients"}) {
		t.Errorf("%+v", res)
	}
	if res.Known {
		t.Error("sizes from nothing")
	}
	// the package that apt removes only to put it back, in a newer version, is an upgrade: it is not a removal
	replacement := progressLines + `The following packages will be upgraded:
  cron
1 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.
Remv cron [3.0pl1-137]
Inst cron [3.0pl1-137] (3.0pl1-162 Debian:12.15/oldstable [amd64])
Conf cron (3.0pl1-162 Debian:12.15/oldstable [amd64])
`
	res = ParseModuleSimulation(out(replacement, 0), out("", 0), []string{"cron"})
	if res.Failure != "" || len(res.Removes) != 0 || !slices.Equal(res.ToUpgrade, []string{"cron"}) {
		t.Errorf("%+v", res)
	}
	// a Remv line that the list of the removed does not have is a transaction that is not understood, and the list that has it is a removal
	mismatch := strings.Replace(removing, "The following packages will be REMOVED:\n  libvirt0\n", "", 1)
	if res := ParseModuleSimulation(out(mismatch, 0), out("", 0), []string{"libvirt-clients"}); res.Failure != "apt_failed" {
		t.Errorf("a Remv with no list: %+v", res)
	}
	// a purge, with its star, is a removal
	purging := strings.NewReplacer("  libvirt0\n", "  libvirt0*\n", "Remv libvirt0 [9.0.0-3]", "Purg libvirt0 [9.0.0-3]").Replace(removing)
	if res := ParseModuleSimulation(out(purging, 0), out("", 0), []string{"libvirt-clients"}); res.Failure != "" || !slices.Equal(res.Removes, []string{"libvirt0"}) {
		t.Errorf("a purge: %+v", res)
	}
	// a downgrade is its own list
	downgrading := progressLines + `The following packages will be DOWNGRADED:
  libvirt0
0 upgraded, 0 newly installed, 1 downgraded, 0 to remove and 0 not upgraded.
Inst libvirt0 [9.0.0-5] (9.0.0-4 Debian:12.15/oldstable [amd64])
`
	if res := ParseModuleSimulation(out(downgrading, 0), out("", 0), []string{"libvirt0"}); res.Failure != "" || !slices.Equal(res.Downgrades, []string{"libvirt0"}) {
		t.Errorf("a downgrade: %+v", res)
	}
}

func TestAHeldPackageIsNamedWhenAptWouldChangeItAndWhenAptStopsAtIt(t *testing.T) {
	held := progressLines + `The following held packages will be changed:
  libvirt0 libc6
The following packages will be upgraded:
  libc6 libvirt0
2 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.
Inst libc6 [2.31-13] (2.36-9 Debian:12.15/oldstable [amd64])
Inst libvirt0 [9.0.0-3] (9.0.0-4 Debian:12.15/oldstable [amd64])
`
	res := ParseModuleSimulation(out(held, 0), out("", 0), []string{"libc6", "libvirt0"})
	if res.Failure != "" || !slices.Equal(res.Held, []string{"libc6", "libvirt0"}) || !slices.Equal(res.ToUpgrade, []string{"libc6", "libvirt0"}) {
		t.Errorf("%+v", res)
	}
	// with -y apt stops, and says so
	stopped := progressLines + "The following held packages will be changed:\n  libvirt0\nE: Held packages were changed and -y was used without --allow-change-held-packages.\n"
	res = ParseModuleSimulation(out(stopped, 100), out("", 0), []string{"libvirt0"})
	if res.Failure != "held_package" || res.Detail == "" {
		t.Errorf("%+v", res)
	}
	// the words that apt uses for any dependency that cannot be met are not about a hold
	broken := progressLines + "E: Unable to correct problems, you have held broken packages.\n"
	if res := ParseModuleSimulation(out(broken, 100), out("", 0), []string{"libvirt0"}); res.Failure != "apt_failed" {
		t.Errorf("%+v", res)
	}
	// a name that is not a package's in the list of the held is not understood
	bad := strings.Replace(held, "  libvirt0 libc6\n", "  libvirt0 /etc/passwd\n", 1)
	if res := ParseModuleSimulation(out(bad, 0), out("", 0), []string{"libc6", "libvirt0"}); res.Failure != "apt_failed" || !strings.Contains(res.Detail, "bad_name") {
		t.Errorf("%+v", res)
	}
	// the words of a hold in a text that apt did not end with an error are not a hold
	if res := ParseModuleSimulation(out(stopped, 0), out("", 0), []string{"libvirt0"}); res.Failure != "apt_failed" {
		t.Errorf("a hold with status 0: %+v", res)
	}
}

func TestWhatAptDoesNotSayInTheWordsItUsesIsNotGuessedAt(t *testing.T) {
	wrap := func(f func(string) string) string { return f(simInstall) }
	for name, text := range map[string]string{
		"a package that does not exist": progressLines + "E: Unable to locate package nothing-here\n",
		"a package with no candidate":   progressLines + "E: Package 'x' has no installation candidate\n",
		"a virtual package":             progressLines + "Note, selecting 'qemu-system-x86' instead of 'qemu-system'\n" + "0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n",
		"a package half installed":      progressLines + "0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n1 not fully installed or removed.\n",
		"a translation":                 "Lecture des listes de paquets...\nLes NOUVEAUX paquets suivants seront installés :\n  qemu-utils\n1 mis à jour, 1 nouvellement installés, 0 à enlever et 0 non mis à jour.\n",
		"a summary that disagrees with the lists": wrap(func(s string) string {
			return strings.Replace(s, "2 upgraded, 6 newly installed", "2 upgraded, 7 newly installed", 1)
		}),
		"no summary": wrap(func(s string) string {
			return strings.Replace(s, "2 upgraded, 6 newly installed, 0 to remove and 3 not upgraded.\n", "", 1)
		}),
		"the Inst lines of other packages": wrap(func(s string) string { return strings.Replace(s, "Inst qemu-utils", "Inst qemu-tools", 1) }),
		"a line that apt does not print":   wrap(func(s string) string { return s + "Everything is fine.\n" }),
		"a line that says a package is the newest, and is not about a package": wrap(func(s string) string {
			return s + "Not a package is already the newest version (1.0).\n"
		}),
		"a line that says it with no version": wrap(func(s string) string { return s + "pkg-a is already the newest version\n" }),
		"a section twice":                     wrap(func(s string) string { return s + "The following packages will be upgraded:\n  libc6 zlib1g\n" }),
		"a control character":                 wrap(func(s string) string { return strings.Replace(s, "Inst libfdt1", "Inst libfdt1\x1b[2J", 1) }),
		"a line of 5000 characters":           wrap(func(s string) string { return s + "Conf " + strings.Repeat("a", 5000) + "\n" }),
		"a name that is not a package's in a list": wrap(func(s string) string {
			return strings.Replace(s, "  qemu-utils libvirt0", "  qemu-utils $(id)", 1)
		}),
		"a block of suggestions that is not indented": wrap(func(s string) string { return strings.Replace(s, "  numad radvd\n", "numad radvd\n", 1) }),
	} {
		res := ParseModuleSimulation(out(text, 0), out(estInstall, 0), asked)
		if res.Failure != "apt_failed" || res.Detail == "" || len(res.ToInstall) != 0 {
			t.Errorf("%s: %+v", name, res)
		}
		for _, c := range res.Detail {
			if c < ' ' || c > '~' {
				t.Errorf("%s: the detail has %q", name, c)
				break
			}
		}
	}
	// an exit status that is not 0 with no word of apt's about a hold is a failure, and so is a cut output
	if res := ParseModuleSimulation(out(simInstall, 100), out(estInstall, 0), asked); res.Failure != "apt_failed" {
		t.Errorf("status 100: %+v", res)
	}
	if res := ParseModuleSimulation(Output{Text: simInstall, Truncated: true}, out(estInstall, 0), asked); res.Failure != "apt_failed" {
		t.Errorf("a cut output: %+v", res)
	}
	if res := ParseModuleSimulation(out("", 0), out(estInstall, 0), asked); res.Failure != "apt_failed" {
		t.Errorf("an empty output: %+v", res)
	}
}

func TestSizesAreKnownOnlyIfAptSaidThemForTheSameTransaction(t *testing.T) {
	for name, est := range map[string]Output{
		"nothing":                         out("", 0),
		"a status that is neither":        out(estInstall, 1),
		"the status of a refusal":         out(estInstall, 100),
		"a cut output":                    {Text: estInstall, ExitCode: 0, Truncated: true},
		"another transaction":             out(strings.Replace(estInstall, "6 newly installed", "7 newly installed", 1), 0),
		"the sizes of another locale":     out(strings.Replace(estInstall, "44.3 MB", "44,3 Mo", 1), 0),
		"no sizes":                        out(strings.Replace(estInstall, "Need to get 5,120 kB/44.3 MB of archives.\n", "", 1), 0),
		"no line for the room":            out(strings.Replace(estInstall, "After this operation, 130 MB of additional disk space will be used.\n", "", 1), 0),
		"the old end of the trivial run":  out(estInstall+"E: Trivial Only specified but this is not a trivial operation.\n", 100),
		"the words of the old end":        out(estInstall+"E: Trivial Only specified but this is not a trivial operation.\n", 0),
		"the words of a short cache":      out(estInstall+"E: You don't have enough free space in /cache/archives/.\n", 0),
		"an error with the sizes":         out(estInstall+"E: Something else.\n", 0),
		"the room of the cache is short":  out(estInstall+"E: You don't have enough free space in "+testDir+"/cache/archives/.\n", 0),
		"the sizes twice":                 out(strings.Replace(estInstall, "After this operation", "Need to get 1 kB of archives.\nAfter this operation", 1), 0),
		"the room twice":                  out(estInstall+"After this operation, 1 kB of additional disk space will be used.\n", 0),
		"a size that is nonsense":         out(strings.Replace(estInstall, "44.3 MB", "44.3 XB", 1), 0),
		"a size that is a thousand teras": out(strings.Replace(estInstall, "44.3 MB", "9999 TB", 1), 0),
		"a package not fully installed":   out(strings.Replace(estInstall, "After this operation", "1 not fully installed or removed.\nAfter this operation", 1), 0),
		"a control character":             out(strings.Replace(estInstall, "'http://deb.debian.org", "'http://\x1b[2Jdeb.debian.org", 1), 0),
	} {
		res := ParseModuleSimulation(out(simInstall, 0), est, asked)
		if res.Failure != "" || res.Known || res.DownloadBytes != 0 || res.ExtraBytes != 0 {
			t.Errorf("%s: %+v", name, res)
		}
		// the packages are what the simulation said, whatever the sizes
		if len(res.ToInstall) != 6 {
			t.Errorf("%s: %v", name, res.ToInstall)
		}
	}
	// the sizes of the same transaction are known, and the lines that are the addresses of the archives are not read
	res := ParseModuleSimulation(out(simInstall, 0), out(estInstall, 0), asked)
	if !res.Known || res.DownloadBytes != 44_300_000 || res.ExtraBytes != 130_000_000 {
		t.Errorf("%+v", res)
	}
	// ...the address of an archive is anything but the words of apt, and is not a word of apt even when it looks like one
	tricky := strings.Replace(estInstall, "'http://deb.debian.org/debian/pool/main/libv", "'http://deb.debian.org/E: x/Need to get 1 B of archives/libv", 1)
	if res := ParseModuleSimulation(out(simInstall, 0), out(tricky, 0), asked); !res.Known || res.DownloadBytes != 44_300_000 {
		t.Errorf("an address with words of apt in it: %+v", res)
	}
	// the size of what is freed is not a size
	freed := strings.Replace(estInstall, "After this operation, 130 MB of additional disk space will be used.", "After this operation, 2,048 kB disk space will be freed.", 1)
	if res := ParseModuleSimulation(out(simInstall, 0), out(freed, 0), asked); !res.Known || res.ExtraBytes != -2_048_000 {
		t.Errorf("space freed: %+v", res)
	}
	// the sizes of a cache that has part of the archives are the whole
	part := strings.Replace(estInstall, "Need to get 5,120 kB/44.3 MB", "Need to get 5,120 kB/44.3 MB", 1)
	if res := ParseModuleSimulation(out(simInstall, 0), out(part, 0), asked); !res.Known || res.DownloadBytes != 44_300_000 {
		t.Errorf("part of the archives in the cache: %+v", res)
	}
}
