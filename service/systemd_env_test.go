package service

import (
	"reflect"
	"strings"
	"testing"
)

// What systemd does to the words of a unit's command line before it runs it, which no test that
// runs the script directly can see: it replaces ${NAME} by the value of NAME in the unit's
// environment, or by nothing if there is none, and $$ by $. So `sh -c '... ${Package} ...'` runs a
// script whose ${Package} is gone, with no error anywhere. systemd-run --expand-environment=no
// turns that off, but it only exists from systemd 254 (Debian 11, 12 and Ubuntu 22.04 have 247,
// 252 and 249). This is a port of what systemd does, to check what the shell would be given.

// systemdReplaceEnv is replace_env_n(format, n, env, flags 0) of systemd's src/basic/env-util.c,
// the same on v247 (Debian 11) and v257 (Debian 13): the flags are 0 for the words of a command.
// $$ is $; ${NAME} is the value of NAME or "" when there is none; a ':' inside the braces gives up
// on the whole construct, which stays as it was written (the extended syntax is off); $NAME
// without braces is left alone, except for a word that is only that (see systemdExpandArgv).
func systemdReplaceEnv(format string, env map[string]string) string {
	const (
		word = iota
		curly
		variable
	)
	var (
		out   strings.Builder
		state = word
		start int // where the text that was not appended yet begins
	)
	for i := 0; i < len(format); i++ {
		c := format[i]
		switch state {
		case word:
			if c == '$' {
				state = curly
			}
		case curly:
			switch c {
			case '{':
				out.WriteString(format[start : i-1])
				start = i - 1
				state = variable
			case '$':
				out.WriteString(format[start:i])
				start = i + 1
				state = word
			default:
				state = word
			}
		case variable:
			switch c {
			case '}':
				out.WriteString(env[format[start+2:i]])
				start = i + 1
				state = word
			case ':':
				state = word
			}
		}
	}
	out.WriteString(format[start:])
	return out.String()
}

// systemdExpandArgv is replace_env_argv of the same file: what the manager makes of the words of a
// command. A word that is only $NAME becomes the words of the value (none when it is not set; the
// quoting inside the value is not ported); every other word goes through systemdReplaceEnv.
func systemdExpandArgv(argv []string, env map[string]string) []string {
	var out []string
	for _, arg := range argv {
		if len(arg) > 0 && arg[0] == '$' && (len(arg) == 1 || (arg[1] != '{' && arg[1] != '$')) {
			out = append(out, strings.Fields(env[arg[1:]])...)
			continue
		}
		out = append(out, systemdReplaceEnv(arg, env))
	}
	return out
}

// systemRunCommand splits the arguments of systemd-run in the unit's environment (--setenv) and
// the command it runs: everything from the first word that is not an option.
func systemRunCommand(t *testing.T, args []string) (env map[string]string, command []string) {
	t.Helper()
	env = map[string]string{}
	for i, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return env, args[i:]
		}
		if setenv, ok := strings.CutPrefix(arg, "--setenv="); ok {
			name, value, _ := strings.Cut(setenv, "=")
			env[name] = value
		}
	}
	t.Fatalf("no command in %v", args)
	return nil, nil
}

func TestSystemdReplaceEnvIsWhatSystemdDoes(t *testing.T) {
	env := map[string]string{"A": "a", "EMPTY": ""}
	for _, c := range []struct{ in, want string }{
		{"plain", "plain"},
		{"${A}", "a"},
		{"x${A}y${A}z", "xayaz"},
		{"${UNSET}", ""},
		{"x${UNSET}y", "xy"},
		{"${EMPTY}", ""},
		{"$$", "$"},
		{"$$$$", "$$"},
		{"$${A}", "${A}"},
		{"$$${A}", "$a"},
		{"$A", "$A"},
		{"$(date)", "$(date)"},
		{"$?", "$?"},
		{"a$", "a$"},
		{"$", "$"},
		// the extended syntax is off: a ':' leaves the construct as it was written
		{"${A:-x}", "${A:-x}"},
		{"${A:?}", "${A:?}"},
		{"${2:+ $2}", "${2:+ $2}"},
		// anything else between the braces is a name that is not set
		{"${previous# }", ""},
		{"${pin%%=*}", ""},
		{"${Package}=${Version}", "="},
		// ... and a ':' after it only gives up on that construct
		{"${A:-x} ${A}", "${A:-x} a"},
	} {
		if got := systemdReplaceEnv(c.in, env); got != c.want {
			t.Errorf("systemdReplaceEnv(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// a word that is only $NAME is the words of NAME's value, and nothing if it is not set
	got := systemdExpandArgv([]string{"echo", "$LIST", "$UNSET", "${A}", "$$A"}, map[string]string{"LIST": "one two", "A": "a"})
	if want := []string{"echo", "one", "two", "a", "$A"}; !reflect.DeepEqual(got, want) {
		t.Errorf("systemdExpandArgv() = %q, want %q", got, want)
	}
}

// The port sees the damage: the script as it is written does not survive it. If this stops being
// true the port is not looking at anything, and the test below proves nothing.
func TestSystemdWouldRewriteTheScriptAsItIsWritten(t *testing.T) {
	env := map[string]string{"CASAOS_DU_NONCE": strings.Repeat("a", 32)}
	if systemdReplaceEnv(dockerUpdateScript, env) == dockerUpdateScript {
		t.Fatal("systemd leaves the raw script alone: the port is not the thing it is meant to be")
	}
}

// The shell must be given the script that was written: byte for byte, whatever the unit's
// environment holds. This is the guard of the argument of `/bin/sh -c`: a ${...} or a $$ in the
// script that nothing doubled would be eaten before sh saw it, and no test that runs the script
// itself would notice.
func TestSystemdLeavesTheDockerUpdateScriptAsItIsWritten(t *testing.T) {
	nonce := "0123456789abcdef0123456789abcdef"
	pins := []string{"containerd.io=2.1.4-1", "docker-ce-cli=" + debian29, "docker-ce=" + debian29}
	names := []string{"containerd.io", "docker-ce", "docker-ce-cli", "nftables"}
	args, err := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", pins, names, "29.8.0")
	if err != nil {
		t.Fatal(err)
	}
	env, command := systemRunCommand(t, args)
	if len(command) != 3 || command[0] != "/bin/sh" || command[1] != "-c" {
		t.Fatalf("the command is %q", command)
	}

	got := systemdExpandArgv(command, env)
	if len(got) != 3 || got[2] != dockerUpdateScript {
		t.Errorf("the shell is given a script that is not the one written:\n%s", firstDifference(dockerUpdateScript, got[len(got)-1]))
	}
	if got[0] != "/bin/sh" || got[1] != "-c" {
		t.Errorf("the shell is run as %q", got[:2])
	}
}

// What the owner's box does not show: the same, with the unit's environment holding a name the
// script uses in ${...}, so that a substitution cannot pass for an unset name.
func TestSystemdLeavesTheScriptAloneWhateverTheEnvironmentHolds(t *testing.T) {
	nonce := "0123456789abcdef0123456789abcdef"
	args, err := dockerUpdateArgs(nonce, "/var/log/casaos/docker-update.log", "/usr/bin/apt-get", []string{"docker-ce=" + debian29}, []string{"docker-ce"}, "29.8.0")
	if err != nil {
		t.Fatal(err)
	}
	env, command := systemRunCommand(t, args)
	for _, name := range []string{"Package", "Version", "previous", "pin", "pair", "policy", "CASAOS_DU_POLL", "CASAOS_DU_NONCE"} {
		env[name] = "SUBSTITUTED"
	}
	if got := systemdExpandArgv(command, env); got[2] != dockerUpdateScript {
		t.Errorf("the shell is given a script that is not the one written:\n%s", firstDifference(dockerUpdateScript, got[2]))
	}
}

// The commands of the other two units that the core starts with systemd-run are not rewritten
// either: they use $(...), $name and $? but no ${...} and no $$, which systemd would eat, and no
// word that is only a $name.
func TestSystemdLeavesTheOtherUnitsCommandsAsTheyAreWritten(t *testing.T) {
	aptPath, logPath := "/usr/bin/apt-get", "/var/log/casaos/package-update.log"
	for name, args := range map[string]func() ([]string, error){
		"the System packages update, with Docker to protect": func() ([]string, error) {
			return systemPackageUpdateArgs(aptPath, logPath, []string{"libc6", "zlib1g"}, true)
		},
		"the System packages update, without": func() ([]string, error) {
			return systemPackageUpdateArgs(aptPath, logPath, []string{"libc6"}, false)
		},
		"the ReCasaOS update": func() ([]string, error) {
			return detachedUpdateArgs("https://example.org/install.sh", "/var/log/casaos/upgrade.log", "v0.5.1"), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			list, err := args()
			if err != nil {
				t.Fatal(err)
			}
			env, command := systemRunCommand(t, list)
			if len(command) == 0 || strings.HasPrefix(command[0], "$") {
				t.Fatalf("the command is %q", command)
			}
			for _, word := range command {
				if strings.HasPrefix(word, "$") {
					t.Errorf("the word %q is only a variable: systemd would replace it", word)
				}
			}
			if got := systemdExpandArgv(command, env); !reflect.DeepEqual(got, command) {
				t.Errorf("the shell is given a command that is not the one written:\n%s", firstDifference(strings.Join(command, " "), strings.Join(got, " ")))
			}
		})
	}
}

// firstDifference is where two texts part, for a message.
func firstDifference(want, got string) string {
	n := 0
	for n < len(want) && n < len(got) && want[n] == got[n] {
		n++
	}
	from := max(n-30, 0)
	return "want ..." + want[from:min(n+60, len(want))] + "\n got ..." + got[from:min(n+60, len(got))]
}
