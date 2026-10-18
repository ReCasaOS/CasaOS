package service

import (
	"errors"
	"regexp"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The unit of a job of a module: casaos-module-<name>.service, started by systemd-run for the install or the remove of one module and ended with the job (--collect). The pattern that the
// units which change the box are asked by is systemModuleUnits.
const (
	systemModuleUnitPrefix = "casaos-module-"
	systemModuleUnits      = systemModuleUnitPrefix + "*.service"
	// systemModuleBinary is the program the unit runs: the core, which is also the module assistant (`casaos module ...`).
	systemModuleBinary = "/usr/bin/casaos"
)

// the verbs of a job
const (
	moduleVerbInstall = "install"
	moduleVerbRemove  = "remove"
)

// modulePlanIDPattern is the id of a plan of an install: m1- and 32 lower case hex digits.
var modulePlanIDPattern = regexp.MustCompile(`^m1-[0-9a-f]{32}$`)

// systemModuleUnit is the unit of the job of the module called name. The name must have passed osmodule.ValidName.
func systemModuleUnit(name string) string { return systemModuleUnitPrefix + name + ".service" }

// systemModuleUnitArgs are the arguments of systemd-run that start the job of a verb for a module: the assistant, as root, with the nonce of the job in its environment and nothing else.
// The shape is closed: the verb is install (with the id of the plan that the user read) or remove (with none), the name is a name of a module (osmodule.ValidName), the id is
// m1- and 32 lower case hex digits and the nonce is 32 lower case hex digits. No word of it holds a dollar sign or a percent sign: systemd replaces ${NAME} and $$ in the words of a
// command before the shell sees them, and takes % for the start of a specifier. The nonce is in the unit's environment, and so in the arguments of systemd-run while it runs and in what
// systemctl shows of the unit, which a local user can read; that is enough, because what the nonce guards is the log of the job (a line without it is not read), and the log is a file of
// root's in a folder of root's: whoever could write a line into it needs no nonce, and whoever can only read the nonce cannot write one.
func systemModuleUnitArgs(verb, name, planID, nonce string) ([]string, error) {
	switch {
	case !osmodule.ValidName(name):
		return nil, errors.New("not the name of a module")
	case !dockerpkg.ValidNonce(nonce):
		return nil, errors.New("not a nonce")
	case verb == moduleVerbInstall && !modulePlanIDPattern.MatchString(planID):
		return nil, errors.New("not the id of a plan")
	case verb == moduleVerbRemove && planID != "":
		return nil, errors.New("a remove has no plan")
	case verb != moduleVerbInstall && verb != moduleVerbRemove:
		return nil, errors.New("not a verb of a job")
	}
	args := []string{
		"--quiet", "--no-block", "--collect", "--unit=" + systemModuleUnit(name), "--property=Type=exec",
		"--setenv=CASAOS_MODULE_NONCE=" + nonce,
		"--", systemModuleBinary, "module", verb, name, "--yes",
	}
	if verb == moduleVerbInstall {
		args = append(args, "--plan-id", planID)
	}
	for _, arg := range args {
		if strings.ContainsAny(arg, "$%") {
			return nil, errors.New("a word of the command line of the unit holds a $ or a %, which systemd would take for its own")
		}
	}
	return args, nil
}
