package service

import (
	"math/rand"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

const (
	unitNonce  = "0123456789abcdef0123456789abcdef"
	unitPlanID = "m1-0123456789abcdef0123456789abcdef"
)

func TestTheUnitOfAnInstallIsExactlyThis(t *testing.T) {
	got, err := systemModuleUnitArgs("install", "virt", unitPlanID, unitNonce)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--quiet", "--no-block", "--collect", "--unit=casaos-module-virt.service", "--property=Type=exec", "--property=KillMode=mixed", "--property=TimeoutStopSec=420",
		"--setenv=CASAOS_MODULE_NONCE=" + unitNonce,
		"--", "/usr/bin/casaos", "module", "install", "virt", "--yes", "--plan-id", unitPlanID,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestTheUnitOfARemoveIsExactlyThis(t *testing.T) {
	got, err := systemModuleUnitArgs("remove", "virt", "", unitNonce)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--quiet", "--no-block", "--collect", "--unit=casaos-module-virt.service", "--property=Type=exec", "--property=KillMode=mixed", "--property=TimeoutStopSec=420",
		"--setenv=CASAOS_MODULE_NONCE=" + unitNonce,
		"--", "/usr/bin/casaos", "module", "remove", "virt", "--yes",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestTheUnitIsStartedForNothingOutsideItsShape(t *testing.T) {
	type c struct{ verb, name, plan, nonce string }
	bad := map[string]c{
		"a verb that is none":           {"upgrade", "virt", unitPlanID, unitNonce},
		"no verb":                       {"", "virt", unitPlanID, unitNonce},
		"a name with a capital":         {"install", "Virt", unitPlanID, unitNonce},
		"no name":                       {"install", "", unitPlanID, unitNonce},
		"a name with a dollar":          {"install", "vi$rt", unitPlanID, unitNonce},
		"a name with a percent":         {"install", "vi%rt", unitPlanID, unitNonce},
		"a name with a blank":           {"install", "vi rt", unitPlanID, unitNonce},
		"a name that is a path":         {"install", "../virt", unitPlanID, unitNonce},
		"a name that is an option":      {"install", "--yes", unitPlanID, unitNonce},
		"a name with a new line":        {"install", "virt\n", unitPlanID, unitNonce},
		"a name of 33 characters":       {"install", strings.Repeat("a", 33), unitPlanID, unitNonce},
		"a name of two words":           {"remove", "virt --yes", "", unitNonce},
		"an install with no plan":       {"install", "virt", "", unitNonce},
		"a plan of the wrong version":   {"install", "virt", "m2-0123456789abcdef0123456789abcdef", unitNonce},
		"a plan that is short":          {"install", "virt", "m1-0123456789abcdef0123456789abcde", unitNonce},
		"a plan that is long":           {"install", "virt", unitPlanID + "0", unitNonce},
		"a plan in capitals":            {"install", "virt", "m1-0123456789ABCDEF0123456789ABCDEF", unitNonce},
		"a plan with a dollar":          {"install", "virt", "m1-0123456789abcdef0123456789abcde$", unitNonce},
		"a plan with a percent":         {"install", "virt", "m1-0123456789abcdef0123456789abcde%", unitNonce},
		"a plan that is an option":      {"install", "virt", "--plan-id", unitNonce},
		"a remove that has a plan":      {"remove", "virt", unitPlanID, unitNonce},
		"no nonce":                      {"install", "virt", unitPlanID, ""},
		"a nonce of a dash":             {"install", "virt", unitPlanID, "-"},
		"a nonce in capitals":           {"install", "virt", unitPlanID, strings.ToUpper(unitNonce)},
		"a nonce that is short":         {"install", "virt", unitPlanID, unitNonce[:31]},
		"a nonce that is long":          {"remove", "virt", "", unitNonce + "0"},
		"a nonce that has a dollar":     {"install", "virt", unitPlanID, unitNonce[:31] + "$"},
		"a nonce that has a percent":    {"install", "virt", unitPlanID, unitNonce[:31] + "%"},
		"a nonce that is a second word": {"install", "virt", unitPlanID, unitNonce + " --setenv=X=Y"},
	}
	for name, in := range bad {
		if got, err := systemModuleUnitArgs(in.verb, in.name, in.plan, in.nonce); err == nil || got != nil {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
}

// Whatever the inputs are, a command line that is given is made of the words of the two shapes and of nothing else, and holds no dollar sign and no percent sign.
func TestNoWordOfTheUnitIsAnythingButTheFixedOnesAndTheValidatedOnes(t *testing.T) {
	alphabet := "abcdefghijklmnopqrstuvwxyz0123456789-_$%{}() \n\"'`;&|<>=.:/m1ABCXYZ"
	rnd := rand.New(rand.NewSource(7))
	word := func(n int) string {
		b := make([]byte, rnd.Intn(n+1))
		for i := range b {
			b[i] = alphabet[rnd.Intn(len(alphabet))]
		}
		return string(b)
	}
	fixed := regexp.MustCompile(`^(--quiet|--no-block|--collect|--unit=casaos-module-[a-z][a-z0-9-]{0,31}\.service|--property=Type=exec|--property=KillMode=mixed|--property=TimeoutStopSec=420|--setenv=CASAOS_MODULE_NONCE=[0-9a-f]{32}|--|/usr/bin/casaos|module|install|remove|[a-z][a-z0-9-]{0,31}|--yes|--plan-id|m1-[0-9a-f]{32})$`)
	given := 0
	for i := 0; i < 20000; i++ {
		verb := []string{"install", "remove", word(8)}[rnd.Intn(3)]
		name := []string{"virt", word(12), word(40)}[rnd.Intn(3)]
		plan := []string{unitPlanID, "", word(40), "m1-" + word(32)}[rnd.Intn(4)]
		nonce := []string{unitNonce, word(34)}[rnd.Intn(2)]
		args, err := systemModuleUnitArgs(verb, name, plan, nonce)
		if err != nil {
			continue
		}
		given++
		for _, a := range args {
			if strings.ContainsAny(a, "$%") || !fixed.MatchString(a) {
				t.Fatalf("a word that is not of the shape: %q in %q", a, args)
			}
		}
	}
	if given == 0 {
		t.Fatal("the generator never made a command line that was given")
	}
}

// The properties of the unit are three and these: the type, a kill mode that asks the assistant alone to stop (it stops apt and dpkg in its own way, and rolls back), and a time to stop
// that is above what the assistant needs to roll back. No other property is given, and none is given another value.
func TestTheUnitIsGivenTheseThreePropertiesAndNoOther(t *testing.T) {
	for _, verb := range []string{"install", "remove"} {
		plan := unitPlanID
		if verb == "remove" {
			plan = ""
		}
		args, err := systemModuleUnitArgs(verb, "virt", plan, unitNonce)
		if err != nil {
			t.Fatal(err)
		}
		var props []string
		for _, a := range args {
			if strings.Contains(a, "--property") {
				props = append(props, a)
			}
		}
		want := []string{"--property=Type=exec", "--property=KillMode=mixed", "--property=TimeoutStopSec=420"}
		if !reflect.DeepEqual(props, want) {
			t.Errorf("%s: the properties are %q, want %q", verb, props, want)
		}
	}
	if systemModuleStopTimeout <= modulecmd.StopWithin {
		t.Errorf("the unit is given %v to stop and the assistant needs %v", systemModuleStopTimeout, modulecmd.StopWithin)
	}
	if got := int(systemModuleStopTimeout / time.Second); got != 420 {
		t.Errorf("TimeoutStopSec is %d, the tests and the doc say 420", got)
	}
	if modulecmd.StopWithin < 5*time.Minute+20*time.Second {
		t.Errorf("the assistant is given only %v to stop: its rollback has 5 minutes and a command is given 20 seconds to leave", modulecmd.StopWithin)
	}
}

// The units that the maintenance check lists, and the units of the jobs, are not the unit of any module: a module's manifest is refused if it says one. The list of the manifest's rule is
// in pkg/osmodule, which does not know this package's: this keeps the two together.
func TestNoUnitThatTheMaintenanceCheckListsCanBeTheUnitOfAModule(t *testing.T) {
	for _, unit := range systemMaintenanceUnits {
		if strings.ContainsAny(unit, "*?[") {
			continue // (the pattern of the jobs, below)
		}
		if osmodule.ValidUnit(unit) {
			t.Errorf("a module may have the unit %s, which the maintenance check lists", unit)
		}
	}
	for _, name := range []string{"a", "virt", "a-b", strings.Repeat("a", 32)} {
		if osmodule.ValidUnit(systemModuleUnit(name)) {
			t.Errorf("a module may have the unit %s, which is the unit of its own job", systemModuleUnit(name))
		}
	}
	if osmodule.ValidUnit(systemOSUpgradeUnit) || osmodule.ValidUnit(systemPackageUpdateUnit) || osmodule.ValidUnit(systemDockerUpdateUnit) {
		t.Error("the unit of an update may be a module's")
	}
}
