package modulecmd

// Dispatch says whether a command line is the module assistant's: `casaos module ...`, with "module" as the first argument after the program and spelled exactly so. It gives what
// follows. Nothing else reaches the code of the assistant: a flag of the core in front of it, a different case, an abbreviation or a word that only starts with it is the core's own
// command line, and the core starts as it always did.
func Dispatch(args []string) (rest []string, ok bool) {
	if len(args) >= 2 && args[1] == "module" {
		return args[2:], true
	}
	return nil, false
}
