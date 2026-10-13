package osplan

import "strings"

// Block F, "forward": what a hop that stopped after its point of no return needs before the block that did not end can be run again. The owner's table at the end of the runbook
// says it in words ("first stop the timers; dpkg --configure -a; apt-get -s -f install and read it; apt-get -f install; then run the block again"); this is the same steps as
// code, for the unit. It is the only text that the unit has and the owner's runbook does not: it is never skipped (it has no done file), it has no block before it, and it runs
// at the start of every attempt that repairs a hop, the blocks after it being the ones that are not done.
//
// It is stricter than block 6's own ladder, which guards the protected and the essential packages: the repair may remove only what the plan said the upgrade removes.

// forwardTitle is the title of block F.
const forwardTitle = "mend what an interrupted upgrade left half done"

// forwardUnits are the units that block F stops: the timers of apt and the services that they start, which a restart brings back as units that are enabled, and which hold the
// locks of apt for minutes. Every attempt that repairs, and the core at every boot while a run is open, stops them again (the table at the end of the runbook says: first, stop them).
const forwardUnits = "apt-daily.timer apt-daily-upgrade.timer apt-daily.service apt-daily-upgrade.service unattended-upgrades.service"

// autoForward is the code of block F, for the plan of c.
func autoForward(c *rbCtx) string {
	var b strings.Builder
	b.WriteString(head(c, ""))
	b.WriteString(c.aptq() + shSim + shGuard() + shHelp + shDpkgState)
	b.WriteString("echo \"Block F: mending what an interrupted upgrade left half done.\"\n")
	b.WriteString(shTimersOf(forwardUnits))
	b.WriteString(busyCheck("The upgrade has begun: do not undo anything."))
	// a folder that holds no list of the removals that the plan expected (a refresh has none, and writes it empty) has no removal that the repair may make
	b.WriteString("[ -e \"$BK/expected-removed.txt\" ] || : > \"$BK/expected-removed.txt\"\n")
	b.WriteString("dpkg --configure -a --force-confdef --force-confold || " + stop("dpkg could not configure what is half done: what it printed is above, in the log of the unit (dpkg run by hand does not write term.log). Do not undo anything: ask for help. "+logWords) + "\n")
	b.WriteString("recover=\"$BK/recover-$CASAOS_OSU_ATTEMPT.txt\"\n")
	b.WriteString("aptq -s -f install > \"$recover\" 2>&1 || { echo \"STOP: apt cannot work out the repair: see $recover. $help\"; exit 1; }\n")
	b.WriteString(checkSim("$recover", "$help"))
	b.WriteString("guard_removals \"$recover\" \"the repair\"\n")
	b.WriteString(shLines(
		`for name in $(removed_of "$recover"); do`,
		`  grep -qxF -- "$name" "$BK/expected-removed.txt" || { echo "STOP: the repair would remove $name, which the plan does not name: do not undo anything, ask for help, with $recover. `+logWords+`"; exit 1; }`,
		`done`,
	))
	b.WriteString("aptq -f install || " + stop("apt could not repair the packages. Do not undo anything: ask for help. "+logWords) + "\n")
	b.WriteString("echo \"Block F done.\"\n")
	return b.String()
}
