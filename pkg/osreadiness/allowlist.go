package osreadiness

import (
	"regexp"
	"slices"
	"strings"
)

// The shapes an argument of an allowed command may have. Nothing is ever given to a shell, so
// these are not about quoting: they keep an option out of the place of a name, and a path out of
// the place of a unit.
var (
	// packageArg is a package name, or a dpkg-query glob of one ("firmware-*", "*:i386").
	packageArg = regexp.MustCompile(`^[A-Za-z0-9*?\[][A-Za-z0-9.+:*?\[\]_-]{0,127}$`)
	unitArg    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:-]{0,127}$`)
	deviceArg  = regexp.MustCompile(`^/dev/[A-Za-z0-9._/-]{1,60}$`)
	moduleArg  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	propertyOf = regexp.MustCompile(`^--property=[A-Za-z]{1,40}$`)
)

// exact is the rule of a command that may be given one list of arguments, or one of several.
func exact(lists ...[]string) func([]string) bool {
	return func(args []string) bool {
		for _, want := range lists {
			if slices.Equal(args, want) {
				return true
			}
		}
		return false
	}
}

// anything is the rule of a command that reads and cannot do anything else, whatever it is given.
func anything([]string) bool { return true }

// allowed is the allow-list: a command, by name (a name, never a path), and a test of its
// arguments. A command that is not here is never run, and a command that can write only runs in
// the forms below, which read. Adding a row is adding to what the report may do to the machine:
// TestTheAllowListIsPinned says which rows there are, and fails when one is added or removed.
var allowed = map[string]func([]string) bool{
	"dpkg": exact([]string{"--audit"}, []string{"--print-architecture"}, []string{"--print-foreign-architectures"}),
	// -W lists packages and nothing else; the format and the patterns are data.
	"dpkg-query": func(args []string) bool {
		if len(args) < 2 || args[0] != "-W" || !strings.HasPrefix(args[1], "-f=") {
			return false
		}
		for _, pattern := range args[2:] {
			if !packageArg.MatchString(pattern) {
				return false
			}
		}
		return true
	},
	// The apt tools only in their reading forms, and with the options that keep them from writing:
	// apt builds the caches of the packages when they are older than dpkg's records, and as root it
	// writes them to /var/cache/apt, and takes a lock. The holds are not asked of apt-mark, which
	// does the same: dpkg-query says them (a hold is the wanted state of a package).
	"apt-get": exact(aptCheckArgs[1:]),
	"apt-cache": func(args []string) bool {
		if len(args) < len(aptNoWrite)+2 || !slices.Equal(args[:len(aptNoWrite)], aptNoWrite) || args[len(aptNoWrite)] != "policy" {
			return false
		}
		for _, name := range args[len(aptNoWrite)+1:] {
			if !packageArg.MatchString(name) {
				return false
			}
		}
		return true
	},
	"apt-config":          exact([]string{"dump"}),
	"systemd-detect-virt": exact([]string{"--container"}, []string{"--vm"}),
	"systemctl": func(args []string) bool {
		switch {
		case len(args) == 4 && args[0] == "show":
			return unitArg.MatchString(args[1]) && propertyOf.MatchString(args[2]) && args[3] == "--value"
		case len(args) == 1 && args[0] == "is-system-running":
			return true
		case len(args) == 2 && (args[0] == "is-active" || args[0] == "is-enabled"):
			return unitArg.MatchString(args[1])
		}
		return false
	},
	// the type of the filesystem of a path
	"stat": func(args []string) bool {
		return len(args) == 3 && args[0] == "-fc" && args[1] == "%T" && strings.HasPrefix(args[2], "/") && !strings.ContainsAny(args[2], "\x00\n")
	},
	"findmnt": anything,
	"lsblk":   anything,
	"uname":   anything,
	// the health of a disk: -H reads it, where -s, -t and -o would change the disk's settings
	"smartctl":   func(args []string) bool { return len(args) == 2 && args[0] == "-H" && deviceArg.MatchString(args[1]) },
	"modinfo":    func(args []string) bool { return len(args) == 2 && args[0] == "-n" && moduleArg.MatchString(args[1]) },
	"dkms":       exact([]string{"status"}),
	"mokutil":    exact([]string{"--sb-state"}),
	"pveversion": anything,
	// the daemon's own answers: info, ps and inspect only read
	"docker": func(args []string) bool {
		return len(args) > 0 && (args[0] == "info" || args[0] == "ps" || args[0] == "inspect")
	},
	"snap": exact([]string{"list"}, []string{"list", "docker"}),
}

// aptNoWrite are the options that keep an apt tool from taking a lock and from writing the caches it
// builds: with them it builds the caches in memory, and writes nothing.
var aptNoWrite = []string{"-o", "Debug::NoLocking=true", "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache="}

// aptCheckArgs is apt-get's one allowed form: "apt-get check", that looks for broken dependencies.
var aptCheckArgs = append(append([]string{"apt-get"}, aptNoWrite...), "check")

// aptPolicyArgs is the command "apt-cache policy" for packages, in its allowed form.
func aptPolicyArgs(names ...string) []string {
	return append(append(append([]string{"apt-cache"}, aptNoWrite...), "policy"), names...)
}

// Allowed says whether argv is a command the report may run. argv[0] is a bare command name.
func Allowed(argv []string) bool {
	if len(argv) == 0 || argv[0] == "" {
		return false
	}
	for _, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return false
		}
	}
	rule, ok := allowed[argv[0]]
	return ok && rule(argv[1:])
}

// allowedCommands are the names on the allow-list, sorted: what the pinning test compares.
func allowedCommands() []string {
	names := make([]string, 0, len(allowed))
	for name := range allowed {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
