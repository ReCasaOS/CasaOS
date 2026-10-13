package service

import (
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

const (
	testRunID = "0b8f3a52-6c1e-4d0a-9a53-7f1c2d4e8b19"
	testNonce = "0123456789abcdef0123456789abcdef"
)

func anAttempt(phase string, n int) osrun.Attempt {
	return osrun.Attempt{RunID: testRunID, Nonce: testNonce, Phase: phase, N: n}
}

func aUnitDir() string { return "/var/lib/casaos/os-upgrade/" + testRunID }

// The command line of the unit is pinned word by word: it is a closed shape, and a reader of the diff must see every change of it.
func TestTheUnitOfAnUpgradeIsStartedWithExactlyThisCommandLine(t *testing.T) {
	got, err := osUpgradeUnitArgs(anAttempt(osrun.PhaseRun, 1), aUnitDir())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--quiet", "--no-block", "--collect", "--unit=casaos-os-upgrade", "--property=Type=exec",
		"--setenv=PATH=/usr/sbin:/usr/bin:/sbin:/bin", "--setenv=LC_ALL=C", "--setenv=DEBIAN_FRONTEND=noninteractive",
		"--setenv=APT_LISTCHANGES_FRONTEND=none", "--setenv=NEEDRESTART_SUSPEND=1", "--setenv=UCF_FORCE_CONFFOLD=1",
		"--setenv=CASAOS_OSU_NONCE=" + testNonce, "--setenv=CASAOS_OSU_DIR=" + aUnitDir(), "--setenv=CASAOS_OSU_RUN=" + testRunID,
		"--setenv=CASAOS_OSU_PHASE=run", "--setenv=CASAOS_OSU_ATTEMPT=1",
		"--", "systemd-inhibit", "--what=shutdown:sleep", "--who=casaos-os-upgrade", "--why=an operating system upgrade is running",
		"--mode=block", "/bin/sh", aUnitDir() + "/driver.sh",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the arguments are\n%q\nwant\n%q", got, want)
	}
	if systemOSUpgradeUnit != "casaos-os-upgrade.service" {
		t.Errorf("the name of the unit is %q", systemOSUpgradeUnit)
	}
}

func TestEveryPhaseAndEveryNumberOfAttemptHasItsCommandLine(t *testing.T) {
	for _, phase := range osrun.Phases {
		for _, n := range []int{1, 2, 9, 10, 99, 100, 999} {
			args, err := osUpgradeUnitArgs(anAttempt(phase, n), aUnitDir())
			if err != nil {
				t.Fatalf("%s %d: %v", phase, n, err)
			}
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "--setenv=CASAOS_OSU_PHASE="+phase+" ") || !strings.Contains(joined, "--setenv=CASAOS_OSU_ATTEMPT="+strconv.Itoa(n)+" ") {
				t.Errorf("%s %d: %s", phase, n, joined)
			}
		}
	}
}

func TestTheUnitIsNotStartedFromWhatIsNotAnAttemptOfARun(t *testing.T) {
	good := anAttempt(osrun.PhaseRun, 1)
	for name, c := range map[string]struct {
		attempt osrun.Attempt
		dir     string
	}{
		"a phase that is not one":            {anAttempt("reboot", 1), aUnitDir()},
		"a phase with an argument in it":     {anAttempt("run --who=root", 1), aUnitDir()},
		"no phase":                           {anAttempt("", 1), aUnitDir()},
		"a nonce that is too short":          {osrun.Attempt{RunID: testRunID, Nonce: testNonce[:31], Phase: osrun.PhaseRun, N: 1}, aUnitDir()},
		"a nonce in capitals":                {osrun.Attempt{RunID: testRunID, Nonce: strings.ToUpper(testNonce), Phase: osrun.PhaseRun, N: 1}, aUnitDir()},
		"a nonce that is a command":          {osrun.Attempt{RunID: testRunID, Nonce: "$(reboot)" + testNonce[10:], Phase: osrun.PhaseRun, N: 1}, aUnitDir()},
		"no nonce":                           {osrun.Attempt{RunID: testRunID, Phase: osrun.PhaseRun, N: 1}, aUnitDir()},
		"an attempt number of 0":             {anAttempt(osrun.PhaseRun, 0), aUnitDir()},
		"an attempt number of 1000":          {anAttempt(osrun.PhaseRun, 1000), aUnitDir()},
		"a negative attempt number":          {anAttempt(osrun.PhaseRun, -1), aUnitDir()},
		"a run id that is not a UUID":        {osrun.Attempt{RunID: "../etc", Nonce: testNonce, Phase: osrun.PhaseRun, N: 1}, "/var/lib/casaos/os-upgrade/../etc"},
		"an empty dir":                       {good, ""},
		"a relative dir":                     {good, "var/lib/casaos/os-upgrade/" + testRunID},
		"another root":                       {good, "/tmp/" + testRunID},
		"the root itself":                    {good, "/var/lib/casaos/os-upgrade"},
		"a dir with a slash after it":        {good, aUnitDir() + "/"},
		"a dir with a further level":         {good, aUnitDir() + "/blocks"},
		"a dir that climbs out":              {good, "/var/lib/casaos/os-upgrade/../os-upgrade/" + testRunID},
		"a dir with a space and an argument": {good, aUnitDir() + " --setenv=PATH=/tmp"},
		"a dir of another run":               {good, "/var/lib/casaos/os-upgrade/ffffffff-6c1e-4d0a-9a53-7f1c2d4e8b19"},
		"a dir with a dollar sign":           {good, "/var/lib/casaos/os-upgrade/$" + testRunID[1:]},
		"a dir with a NUL":                   {good, aUnitDir() + "\x00"},
		"a dir with a newline":               {good, aUnitDir() + "\n"},
	} {
		if args, err := osUpgradeUnitArgs(c.attempt, c.dir); err == nil {
			t.Errorf("%s: the unit is started with %q", name, args)
		}
	}
}

// No word of the command line may hold a dollar sign, which systemd would expand before the shell saw it, or a percent sign, which it takes for the
// start of a specifier: the fixed words are held to it too, and a sentence that has one is refused.
func TestNoWordOfTheCommandLineHoldsADollarOrAPercentSign(t *testing.T) {
	args, err := osUpgradeUnitArgs(anAttempt(osrun.PhaseRecover, 3), aUnitDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range args {
		if strings.ContainsAny(arg, "$%") {
			t.Errorf("the word %q holds a $ or a %%", arg)
		}
	}

	original := osUpgradeWhy
	t.Cleanup(func() { osUpgradeWhy = original })
	for _, why := range []string{"an upgrade of $HOME", "an upgrade of ${A}", "100% of the upgrade", "$$"} {
		osUpgradeWhy = why
		if args, err := osUpgradeUnitArgs(anAttempt(osrun.PhaseRun, 1), aUnitDir()); err == nil {
			t.Errorf("a sentence with %q is let through: %q", why, args)
		}
	}
}

// systemd replaces ${NAME} and $$ in the words of a command, and the shell is then given what is left: the words must come out as they were written,
// whatever the environment of the unit holds.
func TestSystemdLeavesTheCommandLineOfTheUnitAsItIsWritten(t *testing.T) {
	for _, phase := range osrun.Phases {
		args, err := osUpgradeUnitArgs(anAttempt(phase, 12), aUnitDir())
		if err != nil {
			t.Fatal(err)
		}
		env, command := systemRunCommand(t, args)
		if len(command) < 3 || command[0] != "systemd-inhibit" || command[len(command)-2] != "/bin/sh" || command[len(command)-1] != aUnitDir()+"/driver.sh" {
			t.Fatalf("the command is %q", command)
		}
		for _, word := range command {
			if strings.HasPrefix(word, "$") {
				t.Errorf("the word %q is only a variable: systemd would replace it", word)
			}
		}
		if got := systemdExpandArgv(command, env); !reflect.DeepEqual(got, command) {
			t.Errorf("the unit is started with a command that is not the one written:\n%s", firstDifference(strings.Join(command, " "), strings.Join(got, " ")))
		}
		// ... with names in its environment that the words could be taken for
		hostile := map[string]string{}
		for name, value := range env {
			hostile[name] = value
		}
		for _, name := range []string{"what", "who", "why", "mode", "dir", "driver", "sh"} {
			hostile[name] = "SUBSTITUTED"
		}
		if got := systemdExpandArgv(command, hostile); !reflect.DeepEqual(got, command) {
			t.Errorf("the unit is started with a command that is not the one written, with a hostile environment:\n%s", firstDifference(strings.Join(command, " "), strings.Join(got, " ")))
		}
		// what the unit's environment is: the eleven variables of the spec, and the nonce only here
		wantNames := []string{
			"APT_LISTCHANGES_FRONTEND", "CASAOS_OSU_ATTEMPT", "CASAOS_OSU_DIR", "CASAOS_OSU_NONCE", "CASAOS_OSU_PHASE", "CASAOS_OSU_RUN",
			"DEBIAN_FRONTEND", "LC_ALL", "NEEDRESTART_SUSPEND", "PATH", "UCF_FORCE_CONFFOLD",
		}
		var names []string
		for name := range env {
			names = append(names, name)
		}
		sort.Strings(names)
		if !reflect.DeepEqual(names, wantNames) {
			t.Errorf("the environment of the unit holds %v, want %v", names, wantNames)
		}
	}
}
