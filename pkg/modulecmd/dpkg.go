package modulecmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// dpkg that was interrupted (a kill in the middle of a package, a power cut) refuses everything apt does until `dpkg --configure -a` has finished what it was doing. `dpkg --audit` lists
// the packages that are in that state; it reads the status file, takes no lock and writes nothing, and ends with status 0 whether it lists any or not.

const (
	auditTimeout = time.Minute
	// maxAuditNames is the most packages of an audit that are kept (and shown).
	maxAuditNames = 50
)

// parseAudit is the packages `dpkg --audit` lists: the lines of its blocks start with one space and the name of the package, then its short description. A name that is not a name of a
// package (a hostile status file, a line of another shape) is kept as "?", so that what is listed is still listed; at most maxAuditNames are kept. Text that lists nothing is no audit.
func parseAudit(text string) (names []string) {
	for _, line := range strings.Split(text, "\n") {
		if len(line) < 2 || line[0] != ' ' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if !osmodule.ValidPackageName(name) {
			name = "?"
		}
		if len(names) < maxAuditNames {
			names = append(names, name)
		}
	}
	return names
}

// auditCmd is the audit as the runner runs it: English, no terminal, no question.
func auditCmd(env Env) Cmd {
	return Cmd{Argv: []string{"dpkg", "--audit"}, Env: childEnv(env), Timeout: auditTimeout}
}

// dpkgAudit is the packages that dpkg says are not in a state it can go on from; none for a clean dpkg. osreadiness.ErrToolMissing says there is no dpkg to ask, and any other error that
// the answer could not be had (the command could not be run, ended by its timeout or its context, or ended with a status other than 0).
func dpkgAudit(ctx context.Context, env Env) ([]string, error) {
	out, err := env.Runner.Run(ctx, auditCmd(env))
	switch {
	case err != nil:
		return nil, err
	case out.ExitCode != 0:
		return nil, fmt.Errorf("dpkg --audit ended with status %d", out.ExitCode)
	}
	return parseAudit(out.Text), nil
}

// dpkgBroken is the failure of a dpkg that could not be repaired; the log says why.
func (in *install) dpkgBroken(err error) *stepFailure {
	in.say("%s", err)
	return &stepFailure{CodeDpkgBroken, err}
}

// repairDpkg makes dpkg fit for apt before the packages are installed: when the audit lists packages, `dpkg --configure -a` finishes them (the same options as the install: no question,
// the configuration files that are there kept; no timer ends it; a stop that is asked for sends dpkg SIGINT and gives it up to aptFinish to end by itself, dpkg ignoring SIGINT while a maintainer script runs), and the audit is made again. It says what it found and did in the log. The packages the audit
// still lists are CodeDpkgBroken, and an end of the run by its context is CodeInterrupted. A dpkg that cannot be asked (no dpkg, or an audit that gave no answer) is not a
// reason to stop here: apt says what it finds.
func (in *install) repairDpkg() *stepFailure {
	names, err := dpkgAudit(in.ctx, in.env)
	switch {
	case in.ctx.Err() != nil:
		return &stepFailure{CodeInterrupted, in.ctx.Err()}
	case errors.Is(err, osreadiness.ErrToolMissing):
		return nil
	case err != nil:
		in.say("dpkg --audit gave no answer: %s", printable(err.Error(), 200))
		return nil
	case len(names) == 0:
		return nil
	}
	in.say("dpkg was interrupted: %s; running dpkg --configure -a", strings.Join(names, " "))
	out, err := in.env.Runner.Run(in.ctx, Cmd{
		Argv: []string{"dpkg", "--configure", "-a", "--force-confold"}, Env: childEnv(in.env), Stdout: in.toolWriter(), Finish: aptFinish,
	})
	if in.ctx.Err() != nil {
		return &stepFailure{CodeInterrupted, in.ctx.Err()}
	}
	left, auditErr := dpkgAudit(in.ctx, in.env)
	switch {
	case in.ctx.Err() != nil:
		return &stepFailure{CodeInterrupted, in.ctx.Err()}
	case err != nil:
		return in.dpkgBroken(fmt.Errorf("dpkg --configure -a could not be run: %s", printable(err.Error(), 200)))
	case auditErr != nil || len(left) > 0:
		return in.dpkgBroken(fmt.Errorf("dpkg is still interrupted after dpkg --configure -a (status %d): %s", out.ExitCode, strings.Join(left, " ")))
	}
	in.say("dpkg_repaired: %s", strings.Join(names, " "))
	return nil
}
