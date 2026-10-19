package modulecmd

import (
	"errors"
	"regexp"
)

const usage = `usage: casaos module list
       casaos module plan <name> [--update]
       casaos module install <name> [--yes] [--plan-id <id>]
       casaos module update <name> [--yes] [--plan-id <id>]
       casaos module remove <name> [--yes]
       casaos module status [name]`

var (
	nameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	planIDRe = regexp.MustCompile(`^m1-[0-9a-f]{32}$`)
)

type command struct {
	verb   string
	name   string
	yes    bool
	planID string
	// update is the plan of an update (plan --update)
	update bool
}

// parseArgs reads what follows "module" and nothing but the forms of the usage: a flag that is not one of them, a flag twice, a missing or surplus name, a name that is not a module's
// name and a plan id that is not one are all errors, and nothing is run for them.
func parseArgs(args []string) (command, error) {
	if len(args) == 0 {
		return command{}, errors.New("no command")
	}
	c := command{verb: args[0]}
	var yesOK, planOK, updateOK bool
	switch c.verb {
	case "list", "status":
	case "plan":
		updateOK = true
	case "install", "update":
		yesOK, planOK = true, true
	case "remove":
		yesOK = true
	default:
		return command{}, errors.New("unknown command")
	}
	seenYes, seenPlan := false, false
	for i := 1; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--yes" && yesOK && !seenYes:
			c.yes, seenYes = true, true
		case a == "--update" && updateOK && !c.update:
			c.update = true
		case a == "--plan-id" && planOK && !seenPlan && i+1 < len(args):
			i++
			if !planIDRe.MatchString(args[i]) {
				return command{}, errors.New("not a plan id")
			}
			c.planID, seenPlan = args[i], true
		case len(a) > 0 && a[0] == '-':
			return command{}, errors.New("unknown or repeated option")
		case c.name != "":
			return command{}, errors.New("one name only")
		case !nameRe.MatchString(a):
			return command{}, errors.New("not a module name")
		default:
			c.name = a
		}
	}
	switch {
	case c.verb == "list" && c.name != "":
		return command{}, errors.New("list takes no name")
	case c.verb != "list" && c.verb != "status" && c.name == "":
		return command{}, errors.New("a module name is needed")
	}
	return c, nil
}
