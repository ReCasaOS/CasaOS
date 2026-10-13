package osplan

import (
	"slices"
	"strings"
	"testing"
)

const testDir = "/var/tmp/casaos-os-plan.123456"

// sb is the options of the sandbox, written out as the spec has them (and not built by the code that is tested).
func sb(d string) []string {
	return []string{
		"-o", "Dir::Etc::sourcelist=" + d + "/sources.list",
		"-o", "Dir::Etc::sourceparts=" + d + "/parts",
		"-o", "Dir::State::lists=" + d + "/lists",
		"-o", "Dir::Cache=" + d + "/cache",
		"-o", "Debug::NoLocking=true",
		"-o", "Acquire::Retries=3",
	}
}

func argv(d string, tail ...string) []string {
	return append(append([]string{"apt-get"}, sb(d)...), tail...)
}

func fourArgvs(d string) map[string][]string {
	return map[string][]string{
		"update":       argv(d, "update", "--error-on=any"),
		"upgrade":      argv(d, "-s", "upgrade"),
		"dist-upgrade": argv(d, "-s", "dist-upgrade"),
		"estimate":     argv(d, "-o", "APT::Get::Trivial-Only=true", "dist-upgrade"),
	}
}

func TestTheFourArgvsOfThePlanAreWhatTheSpecSays(t *testing.T) {
	want := fourArgvs(testDir)
	got := map[string][]string{
		"update":       UpdateArgv(testDir),
		"upgrade":      UpgradeArgv(testDir),
		"dist-upgrade": DistUpgradeArgv(testDir),
		"estimate":     EstimateArgv(testDir),
	}
	for name, w := range want {
		if !slices.Equal(got[name], w) {
			t.Errorf("%s:\n got %q\nwant %q", name, got[name], w)
		}
	}
}

func TestTheAllowListAcceptsExactlyTheFourArgvs(t *testing.T) {
	for name, a := range fourArgvs(testDir) {
		if !AllowedSandboxArgv(a, testDir) {
			t.Errorf("%s is refused: %q", name, a)
		}
	}
}

func TestTheAllowListRefusesWhatIsNotTheFourArgvs(t *testing.T) {
	update := fourArgvs(testDir)["update"]
	upgrade := fourArgvs(testDir)["upgrade"]
	estimate := fourArgvs(testDir)["estimate"]
	swap := func(a []string, i, j int) []string {
		b := slices.Clone(a)
		b[i], b[j] = b[j], b[i]
		return b
	}
	without := func(a []string, i, n int) []string { return slices.Concat(a[:i], a[i+n:]) }
	replace := func(a []string, i int, v string) []string {
		b := slices.Clone(a)
		b[i] = v
		return b
	}
	cases := map[string][]string{
		"empty":                        {},
		"nothing but the command":      {"apt-get"},
		"a path to the command":        append([]string{"/usr/bin/apt-get"}, update[1:]...),
		"another command":              append([]string{"apt"}, update[1:]...),
		"a shell":                      {"sh", "-c", "apt-get update"},
		"a wrong verb":                 argv(testDir, "upgrade"),
		"an install verb":              argv(testDir, "-s", "install", "curl"),
		"the install verb for real":    argv(testDir, "install", "curl"),
		"a remove verb":                argv(testDir, "remove", "curl"),
		"a purge verb":                 argv(testDir, "purge", "curl"),
		"autoremove":                   argv(testDir, "autoremove"),
		"download":                     argv(testDir, "download", "curl"),
		"source":                       argv(testDir, "source", "curl"),
		"clean":                        argv(testDir, "clean"),
		"upgrade for real":             argv(testDir, "upgrade"),
		"dist-upgrade for real":        argv(testDir, "dist-upgrade"),
		"-y":                           argv(testDir, "-y", "update", "--error-on=any"),
		"-y on a simulated upgrade":    argv(testDir, "-y", "-s", "upgrade"),
		"-f":                           argv(testDir, "-f", "-s", "upgrade"),
		"update without its option":    argv(testDir, "update"),
		"update with another option":   argv(testDir, "update", "--error-on=any", "--allow-unauthenticated"),
		"an extra option":              argv(testDir, "-s", "upgrade", "-o", "APT::Get::AllowUnauthenticated=true"),
		"an extra option first":        append([]string{"apt-get", "-o", "Dir::Cache::pkgcache="}, update[1:]...),
		"the estimate without option":  argv(testDir, "dist-upgrade"),
		"trivial-only on upgrade":      argv(testDir, "-o", "APT::Get::Trivial-Only=true", "upgrade"),
		"trivial-only on update":       argv(testDir, "-o", "APT::Get::Trivial-Only=true", "update", "--error-on=any"),
		"the estimate with -s":         argv(testDir, "-s", "-o", "APT::Get::Trivial-Only=true", "dist-upgrade"),
		"the estimate after the verb":  argv(testDir, "dist-upgrade", "-o", "APT::Get::Trivial-Only=true"),
		"a missing option":             without(update, 1, 2),
		"a missing middle option":      without(update, 5, 2),
		"no locking left out":          without(update, 9, 2),
		"retries left out":             without(update, 11, 2),
		"a reordered option":           swap(update, 2, 4),
		"two options swapped, whole":   slices.Concat(update[:1], update[3:5], update[1:3], update[5:]),
		"the option name changed":      replace(update, 2, "Dir::Etc::sourcelist="+testDir+"/x"),
		"a path outside the dir":       replace(update, 2, "Dir::Etc::sourcelist=/etc/apt/sources.list"),
		"the real lists":               replace(update, 6, "Dir::State::lists=/var/lib/apt/lists"),
		"the real cache":               replace(update, 8, "Dir::Cache=/var/cache/apt"),
		"a dotdot in a path":           replace(update, 4, "Dir::Etc::sourceparts="+testDir+"/../parts"),
		"a path that escapes by a dot": replace(update, 8, "Dir::Cache="+testDir+"/../cache"),
		"a relative path":              replace(update, 2, "Dir::Etc::sourcelist=sources.list"),
		"a symlink-looking name":       replace(update, 2, "Dir::Etc::sourcelist="+testDir+"/sources.list/../../x"),
		"the sources of another dir":   replace(update, 2, "Dir::Etc::sourcelist=/var/tmp/casaos-os-plan.999/sources.list"),
		"a NUL":                        replace(update, 12, "Acquire::Retries=3\x00"),
		"retries changed":              replace(update, 12, "Acquire::Retries=300"),
		"the upgrade with -y -s":       argv(testDir, "-s", "-y", "upgrade"),
		"the upgrade twice":            argv(testDir, "-s", "-s", "upgrade"),
		"the estimate and an install":  slices.Concat(estimate, []string{"curl"}),
		"the upgrade and a package":    slices.Concat(upgrade, []string{"curl"}),
	}
	for name, a := range cases {
		if AllowedSandboxArgv(a, testDir) {
			t.Errorf("%s is accepted: %q", name, a)
		}
	}
}

func TestTheAllowListRefusesADirectoryThatIsNotASandboxDirectory(t *testing.T) {
	for _, d := range []string{
		"",
		"casaos-os-plan.123",
		"/",
		"/var/tmp",
		"/var/tmp/",
		"/var/tmp/casaos-os-plan.",
		"/var/tmp/casaos-os-plan",
		"/var/tmp/casaos-os-plan.123/",
		"/var/tmp/casaos-os-plan.123/sub",
		"/var/tmp/casaos-os-plan.123/../casaos-os-plan.456",
		"/var/tmp/../tmp/casaos-os-plan.123",
		"/var/tmp//casaos-os-plan.123",
		"/tmp/casaos-os-plan.123",
		"/var/tmp/other.123",
		"/var/tmp/casaos-os-plan.12 3",
		"/var/tmp/casaos-os-plan.12;rm",
		"/var/tmp/casaos-os-plan.12$(x)",
		"/var/tmp/casaos-os-plan.12`x`",
		"/var/tmp/casaos-os-plan.12|x",
		"/var/tmp/casaos-os-plan.12&x",
		"/var/tmp/casaos-os-plan.12\nx",
		"/var/tmp/casaos-os-plan.12\x00",
		"/var/tmp/casaos-os-plan.12'x",
		"/var/tmp/casaos-os-plan.12\"x",
		"/var/tmp/casaos-os-plan.12*",
		"/var/tmp/casaos-os-plan.é",
		"/var/tmp/casaos-os-plan." + strings.Repeat("1", 40),
		"var/tmp/casaos-os-plan.123",
		" /var/tmp/casaos-os-plan.123",
	} {
		if ValidSandboxDir(d) {
			t.Errorf("%q is taken for a sandbox directory", d)
		}
		// the argvs that are right for it are refused too: there is no right argv for a directory that is not one
		if AllowedSandboxArgv(argv(d, "update", "--error-on=any"), d) {
			t.Errorf("the update is accepted for %q", d)
		}
	}
	for _, d := range []string{"/var/tmp/casaos-os-plan.1", "/var/tmp/casaos-os-plan.4294967295", "/var/tmp/casaos-os-plan.a1B2c3", "/var/tmp/casaos-os-plan." + strings.Repeat("9", 32)} {
		if !ValidSandboxDir(d) {
			t.Errorf("%q is refused", d)
		}
	}
}

func TestTheNameOfASandboxDirectoryIsStrict(t *testing.T) {
	for name, want := range map[string]bool{
		"casaos-os-plan.123456": true,
		"casaos-os-plan.":       false,
		"casaos-os-plan":        false,
		"casaos-os-plan.12/..":  false,
		"casaos-os-plan.12 ":    false,
		"xcasaos-os-plan.12":    false,
		"casaos-os-plan.12.old": false,
		"":                      false,
	} {
		if got := IsSandboxDirName(name); got != want {
			t.Errorf("IsSandboxDirName(%q) = %v, want %v", name, got, want)
		}
	}
}

// the environment is the spec's, and nothing else: no HOME, no LD_LIBRARY_PATH, no proxy of the core's.
func TestTheEnvironmentOfTheSandboxIsTheSpecs(t *testing.T) {
	want := []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		"LC_ALL=C",
		"LANG=C",
		"DEBIAN_FRONTEND=noninteractive",
		"TMPDIR=" + testDir + "/tmp",
		"APT_CONFIG=" + testDir + "/apt-config",
	}
	if got := SandboxEnv(testDir); !slices.Equal(got, want) {
		t.Errorf("env:\n got %q\nwant %q", got, want)
	}
}

func TestTheTimeoutsAreTheSpecs(t *testing.T) {
	if UpdateTimeout.Minutes() != 10 || SolverTimeout.Minutes() != 2 {
		t.Errorf("update %v, solver %v", UpdateTimeout, SolverTimeout)
	}
	if (Timeouts{}).update() != UpdateTimeout || (Timeouts{}).solver() != SolverTimeout {
		t.Error("the zero Timeouts are not the defaults")
	}
}

func TestInsideSandboxIsStrict(t *testing.T) {
	for p, want := range map[string]bool{
		testDir + "/sources.list":          true,
		testDir + "/lists/partial":         true,
		testDir:                            false,
		testDir + "/":                      false,
		testDir + "/../x":                  false,
		testDir + "/a/../../x":             false,
		"/var/tmp/casaos-os-plan.1234567x": false,
		testDir + "x/a":                    false,
		"/etc/passwd":                      false,
		"sources.list":                     false,
		testDir + "//a":                    false,
	} {
		if got := InsideSandbox(testDir, p); got != want {
			t.Errorf("InsideSandbox(%q) = %v, want %v", p, got, want)
		}
	}
	if InsideSandbox("/tmp/x", "/tmp/x/a") {
		t.Error("a directory that is not a sandbox's has no inside")
	}
}
