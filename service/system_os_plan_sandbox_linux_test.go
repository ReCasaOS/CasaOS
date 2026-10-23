//go:build linux

package service

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The directories these tests make are the real ones: /var/tmp/casaos-os-plan.N, whose name the allow-list pins.

func madeSandbox(t *testing.T) (*osPlanSandbox, string) {
	t.Helper()
	sb := &osPlanSandbox{}
	dir, remove, err := sb.MakeDir()
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no sandbox directory can be made in %s here: %v", osplan.SandboxParent, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	return sb, dir
}

func perm(t *testing.T, p string) fs.FileMode {
	t.Helper()
	info, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestTheSandboxDirectoryIsMadeWhereAndAsTheAllowListNamesIt(t *testing.T) {
	sb, dir := madeSandbox(t)
	if !osplan.ValidSandboxDir(dir) || !strings.HasPrefix(dir, osplan.SandboxParent+"/"+osplan.SandboxPrefix) {
		t.Fatalf("dir = %q", dir)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("Lstat(%s) = %v, %v", dir, info, err)
	}
	// apt's own user walks down to the directories apt gives it: it may search this one, and list none of it
	if got := info.Mode().Perm(); got != 0o711 {
		t.Errorf("mode = %o, want 711", got)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		t.Errorf("owner = %v, want the user the core runs as (%d)", info.Sys(), os.Geteuid())
	}
	if _, _, err := sb.MakeDir(); err == nil {
		t.Error("a sandbox made two directories")
	}
	other := &osPlanSandbox{}
	dir2, remove2, err := other.MakeDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove2)
	if dir2 == dir {
		t.Errorf("two sandboxes have the directory %s", dir)
	}
}

func TestTheDirectoryIsRemovedWithWhatIsInItAndOnlyOnce(t *testing.T) {
	sb := &osPlanSandbox{}
	dir, remove, err := sb.MakeDir()
	if err != nil {
		t.Skipf("no sandbox directory here: %v", err)
	}
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{dir + "/lists/partial/x", dir + "/cache/archives/y", dir + "/sources.list"} {
		if err := sb.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// a link out of the directory is removed as a link: what it points to is not the sandbox's
	if err := os.Symlink(outside, dir+"/link"); err != nil {
		t.Fatal(err)
	}
	remove()
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the directory is still there: %v", err)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "mine" {
		t.Fatalf("what the link pointed to was touched: %q, %v", data, err)
	}
	remove() // safe to call again
}

// The name is checked, and the directory is looked at without being followed: a link that took its place is not removed, and
// neither is what it points to.
func TestAPathThatIsALinkIsNotRemovedAsTheDirectory(t *testing.T) {
	sb := &osPlanSandbox{}
	dir, remove, err := sb.MakeDir()
	if err != nil {
		t.Skipf("no sandbox directory here: %v", err)
	}
	decoy := t.TempDir()
	keep := filepath.Join(decoy, "keep")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(dir) })
	remove()
	if data, err := os.ReadFile(keep); err != nil || string(data) != "mine" {
		t.Fatalf("a link in the place of the directory was followed: %q, %v", data, err)
	}
	if info, err := os.Lstat(dir); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("the link was removed or replaced: %v, %v", info, err)
	}
}

func TestRemoveSandboxDirTakesOnlyTheNameOfOne(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"casaos-os-plan.", "casaos-os-plan.x/y", "casaos-os-plan.12345678901234567890123456789012345", "other", "../x", "", "casaos-os-plan.a b", "casaos-os-plan.."} {
		victim := filepath.Join(parent, "victim")
		if err := os.MkdirAll(filepath.Join(victim, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := osplan.RemoveSandboxDir(parent, name); err == nil {
			t.Errorf("RemoveSandboxDir(%q) took the name of a sandbox directory", name)
		}
		if _, err := os.Stat(victim); err != nil {
			t.Errorf("RemoveSandboxDir(%q) removed what is not a sandbox directory: %v", name, err)
		}
	}
	// a plain file with the name of one is not a directory
	file := filepath.Join(parent, "casaos-os-plan.77")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := osplan.RemoveSandboxDir(parent, "casaos-os-plan.77"); err == nil {
		t.Error("a file was taken for a directory")
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("the file was removed: %v", err)
	}
	// one that is not there is removed already
	if err := osplan.RemoveSandboxDir(parent, "casaos-os-plan.78"); err != nil {
		t.Errorf("a directory that is not there: %v", err)
	}
}

func TestAFileIsWrittenInsideAndTheDirectoriesAboveItAreMade(t *testing.T) {
	sb, dir := madeSandbox(t)
	if err := sb.WriteFile(dir+"/parts/docker.list", []byte("deb x y z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(dir + "/parts/docker.list"); err != nil || string(data) != "deb x y z\n" {
		t.Fatalf("content = %q, %v", data, err)
	}
	if got := perm(t, dir+"/parts/docker.list"); got != 0o600 {
		t.Errorf("file mode = %o, want 600", got)
	}
	if got := perm(t, dir+"/parts"); got != 0o711 {
		t.Errorf("a directory made for a file has mode %o, want 711", got)
	}
	// a directory is made with the mode it is given, the ones above it so that apt's user can walk down
	if err := sb.WriteFile(dir+"/lists/partial", nil, fs.ModeDir|0o700); err != nil {
		t.Fatal(err)
	}
	if got := perm(t, dir+"/lists"); got != 0o711 {
		t.Errorf("lists has mode %o, want 711", got)
	}
	if got := perm(t, dir+"/lists/partial"); got != 0o700 {
		t.Errorf("lists/partial has mode %o, want 700", got)
	}
	// and again: a directory that is there is there
	if err := sb.WriteFile(dir+"/lists/partial", nil, fs.ModeDir|0o700); err != nil {
		t.Errorf("a directory that is already there: %v", err)
	}
}

// What the pure package writes, as apt needs it: every directory down to the ones that apt gives to its own user can be
// searched by that user, and the files that may hold a secret (the sources, the configuration) can be read by root only.
func TestTheFilesOfThePlanAreWrittenAsAptNeedsThem(t *testing.T) {
	sb, dir := madeSandbox(t)
	tree := []osplan.TreeFile{{Path: "sources.list", Content: "deb http://deb.debian.org/debian bookworm main\n"}, {Path: "parts/extra.list", Content: "# x\n"}}
	cfg := osplan.AptConfig{
		Fragments: []osplan.TreeFile{{Path: "apt.conf.d/99proxy", Content: "Acquire::http::Proxy \"http://user:pw@cache.lan:3142\";\n"}},
		Main:      "APT::Install-Recommends \"false\";\n", HasMain: true,
	}
	for _, w := range osplan.SandboxFiles(dir, tree, cfg) {
		if err := sb.WriteFile(w.Path, w.Content, w.Mode); err != nil {
			t.Fatalf("%s: %v", w.Path, err)
		}
	}
	want := "Dir::Etc::parts \"" + dir + "/apt.conf.d\";\nDir::Etc::main \"" + dir + "/apt.conf\";\nDir::Log::Planner \"\";\n"
	if data, err := os.ReadFile(dir + "/apt-config"); err != nil || string(data) != want {
		t.Fatalf("apt-config = %q, %v", data, err)
	}
	for _, d := range []string{"", "/lists", "/cache", "/cache/archives"} {
		if got := perm(t, dir+d); got&0o001 == 0 || got&0o004 != 0 {
			t.Errorf("%s has mode %o: apt's user must be able to search it, and nobody to list it", dir+d, got)
		}
	}
	// the one directory that apt's own user writes in, whatever the umask of the core: anyone may, nobody may take another's file
	if info, err := os.Lstat(dir + "/tmp"); err != nil || !info.IsDir() || info.Mode()&(fs.ModePerm|fs.ModeSticky) != 0o777|fs.ModeSticky {
		t.Errorf("tmp: %v, %v: want a directory 1777", info, err)
	}
	for _, d := range []string{"/lists/partial", "/cache/archives/partial", "/parts", "/apt.conf.d"} {
		info, err := os.Lstat(dir + d)
		if err != nil || !info.IsDir() {
			t.Errorf("%s: %v, %v", d, info, err)
		}
	}
	for _, f := range []string{"/sources.list", "/parts/extra.list", "/apt.conf.d/99proxy", "/apt.conf", "/apt-config"} {
		if got := perm(t, dir+f); got != 0o600 {
			t.Errorf("%s has mode %o, want 600 (a proxy's password may be in it)", f, got)
		}
	}
}

func TestAFileOutsideTheSandboxIsRefusedAndNothingIsWritten(t *testing.T) {
	sb, dir := madeSandbox(t)
	outside := t.TempDir()
	// the name of another sandbox's directory, one character longer than this one's
	sibling := dir + "9"
	t.Cleanup(func() { _ = os.RemoveAll(sibling) })
	for _, p := range []string{
		"", "x", "relative/x", dir, dir + "/", dir + "x", dir + "/../x", dir + "/a/../../x", dir + "//x", dir + "/./x", dir + "/a/./b",
		outside + "/x", sibling + "/x",
	} {
		if err := sb.WriteFile(p, []byte("x"), 0o600); err == nil {
			t.Errorf("WriteFile(%q) wrote", p)
		}
		if err := sb.WriteFile(p, nil, fs.ModeDir|0o700); err == nil {
			t.Errorf("WriteFile(%q) made a directory", p)
		}
	}
	for _, p := range []string{dir + "/../x", sibling, dir + "x"} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s exists", p)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("something was written in %s", outside)
	}
	// nor does a sandbox that has no directory yet write
	if err := (&osPlanSandbox{}).WriteFile(dir+"/x", []byte("x"), 0o600); err == nil {
		t.Error("a sandbox that made no directory wrote")
	}
}

// Nothing is written through a link, and nothing is written over a file that is there: the plan writes each path once.
func TestNothingIsWrittenThroughALinkOrOverAFile(t *testing.T) {
	sb, dir := madeSandbox(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, dir+"/out"); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(dir+"/out/x", []byte("x"), 0o600); err == nil {
		t.Error("a file was written through a link to a directory")
	}
	if err := sb.WriteFile(dir+"/out/deeper/x", []byte("x"), 0o600); err == nil {
		t.Error("a file was written below a link to a directory")
	}
	if err := sb.WriteFile(dir+"/out", nil, fs.ModeDir|0o700); err == nil {
		t.Error("a link was taken for the directory that was asked for")
	}
	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dir+"/file"); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(dir+"/file", []byte("theirs"), 0o600); err == nil {
		t.Error("a file was written over a link to a file")
	}
	if err := sb.WriteFile(dir+"/plain", []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(dir+"/plain", []byte("two"), 0o600); err == nil {
		t.Error("a file was written over a file")
	}
	if data, _ := os.ReadFile(target); string(data) != "mine" {
		t.Errorf("target = %q", data)
	}
	if data, _ := os.ReadFile(dir + "/plain"); string(data) != "one" {
		t.Errorf("plain = %q", data)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Errorf("something was written in %s: %v", outside, entries)
	}
}

// ---- the stale directories ----------------------------------------------------------------------------------------

func age(t *testing.T, p string, by time.Duration) {
	t.Helper()
	at := time.Now().Add(-by)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestTheStaleDirectoriesAreRemovedAtTheStartAndNothingElse(t *testing.T) {
	parent := t.TempDir()
	mkdir := func(name string, old time.Duration) string {
		p := filepath.Join(parent, name)
		if err := os.MkdirAll(filepath.Join(p, "lists", "partial"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "lists", "partial", "x"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		age(t, p, old)
		return p
	}
	stale := mkdir("casaos-os-plan.12345", osplan.StaleAfter+time.Minute)
	stale2 := mkdir("casaos-os-plan.abcDEF9", 3*osplan.StaleAfter)
	young := mkdir("casaos-os-plan.777", osplan.StaleAfter-time.Minute)
	fresh := mkdir("casaos-os-plan.888", 0)
	other := mkdir("casaos-os-plan", 5*time.Hour)      // no suffix
	other2 := mkdir("casaos-os-plan.", 5*time.Hour)    // an empty suffix
	other3 := mkdir("casaos-os-plan.a-b", 5*time.Hour) // a character that is no part of a suffix
	other4 := mkdir("osm-sandbox.1", 5*time.Hour)
	other5 := mkdir("xcasaos-os-plan.1", 5*time.Hour)
	file := filepath.Join(parent, "casaos-os-plan.99")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	age(t, file, 5*time.Hour)
	// a link with the name of one, to an old directory that is not one
	target := mkdir("target", 5*time.Hour)
	link := filepath.Join(parent, "casaos-os-plan.55")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	removed, err := sweepStaleSandboxes(parent, time.Now(), osplan.StaleAfter)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(removed)
	if want := []string{stale2, stale}; !slices.Equal(removed, sorted(want)) {
		t.Errorf("removed = %v, want %v", removed, sorted(want))
	}
	for _, gone := range []string{stale, stale2} {
		if _, err := os.Lstat(gone); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s was not removed: %v", gone, err)
		}
	}
	for _, kept := range []string{young, fresh, other, other2, other3, other4, other5, file, link, target, filepath.Join(target, "lists", "partial", "x")} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("%s was removed: %v", kept, err)
		}
	}
	// a parent that is not there has nothing stale in it
	if removed, err := sweepStaleSandboxes(filepath.Join(parent, "nowhere"), time.Now(), osplan.StaleAfter); err != nil || len(removed) != 0 {
		t.Errorf("a parent that is not there: %v, %v", removed, err)
	}
}

func sorted(list []string) []string {
	out := slices.Clone(list)
	slices.Sort(out)
	return out
}

// ---- the commands -------------------------------------------------------------------------------------------------

// aptStandIn is a directory with an "apt-get" in it that is the script given.
func aptStandIn(t *testing.T, script string) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "apt-get"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func sandboxWithApt(t *testing.T, script string) (*osPlanSandbox, string) {
	t.Helper()
	sb, dir := madeSandbox(t)
	sb.searchPath = []string{aptStandIn(t, script)}
	return sb, dir
}

func lineValues(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

func TestACommandRunsWithTheEnvironmentOfTheSandboxAndNothingElse(t *testing.T) {
	t.Setenv("CASAOS_TEST_LEAK", "leaked")
	t.Setenv("HOME", "/root")
	// the core's own standard input has something to say, and the command is not told it
	stdin, err := os.Create(filepath.Join(t.TempDir(), "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.WriteString("the core's own input\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = stdin
	t.Cleanup(func() { os.Stdin = original; stdin.Close() })
	sb, dir := sandboxWithApt(t, `
for a in "$@"; do printf 'arg:%s\n' "$a"; done
printf 'PATH=%s\n' "$PATH"
printf 'LC_ALL=%s\n' "$LC_ALL"
printf 'LANG=%s\n' "$LANG"
printf 'DEBIAN_FRONTEND=%s\n' "$DEBIAN_FRONTEND"
printf 'TMPDIR=%s\n' "$TMPDIR"
printf 'APT_CONFIG=%s\n' "$APT_CONFIG"
printf 'leak=%s\n' "${CASAOS_TEST_LEAK-unset}"
printf 'home=%s\n' "${HOME-unset}"
printf 'pwd=%s\n' "$(pwd -P)"
printf 'stdin=%s\n' "$(cat | wc -c | tr -d ' ')"
exit 100
`)
	argv := osplan.UpgradeArgv(dir)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := sb.Run(ctx, argv)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if out.ExitCode != 100 || out.Truncated {
		t.Errorf("exit = %d, truncated = %v: a command that ends with a status is an Output", out.ExitCode, out.Truncated)
	}
	var args []string
	for _, line := range strings.Split(out.Text, "\n") {
		if a, ok := strings.CutPrefix(line, "arg:"); ok {
			args = append(args, a)
		}
	}
	if !slices.Equal(args, argv[1:]) {
		t.Errorf("args = %q, want %q", args, argv[1:])
	}
	got := lineValues(out.Text)
	real, _ := filepath.EvalSymlinks(dir)
	for k, want := range map[string]string{
		"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL": "C", "LANG": "C", "DEBIAN_FRONTEND": "noninteractive",
		"TMPDIR": dir + "/tmp", "APT_CONFIG": dir + "/apt-config", "leak": "unset", "home": "unset", "pwd": real, "stdin": "0",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

// apt-get is looked for in the clean PATH of the sandbox's environment, and not in the core's: a core that has /usr/local/sbin
// or a user's directory in its PATH does not run the apt-get that is there.
func TestAptIsLookedForInTheSandboxsPathAndNotTheCores(t *testing.T) {
	t.Setenv("PATH", aptStandIn(t, "exit 0\n"))
	env := osplan.SandboxEnv("/var/tmp/casaos-os-plan.1")
	if got, want := (&osPlanSandbox{}).searchDirs(env), []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"}; !slices.Equal(got, want) {
		t.Errorf("searchDirs = %v, want %v", got, want)
	}
	// the first apt-get that can be run, in the order of the path; a directory, or a file that is not executable, is not one
	notExecutable, executable, directory := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(notExecutable, "apt-get"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "apt-get"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(executable, "apt-get")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := lookInPath("apt-get", []string{t.TempDir(), notExecutable, directory, executable}); err != nil || got != want {
		t.Errorf("lookInPath() = %q, %v, want %q", got, err, want)
	}
	if _, err := lookInPath("apt-get", []string{notExecutable, directory, "relative"}); err == nil {
		t.Error("an apt-get that cannot be run was found")
	}
}

func TestACommandThatIsNotInThePathIsToolMissing(t *testing.T) {
	sb, dir := madeSandbox(t)
	sb.searchPath = []string{t.TempDir()}
	if _, err := sb.Run(context.Background(), osplan.UpdateArgv(dir)); !errors.Is(err, osreadiness.ErrToolMissing) {
		t.Fatalf("err = %v, want ErrToolMissing", err)
	}
}

// Only the four commands of the plan run, and the directory they name is the one this sandbox made: the implementation checks
// again what the pure package checked.
func TestACommandThatIsNotOnTheAllowListIsNotRun(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	sb, dir := sandboxWithApt(t, "printf x > '"+marker+"'\n")
	other := "/var/tmp/casaos-os-plan.1"
	mutated := func(f func(argv []string) []string) []string { return f(osplan.UpgradeArgv(dir)) }
	for name, argv := range map[string][]string{
		"nothing":          nil,
		"no verb":          {"apt-get"},
		"another program":  {"sh", "-c", "touch " + marker},
		"an install":       mutated(func(a []string) []string { return append(slices.Clone(a[:len(a)-2]), "install", "curl") }),
		"-y":               mutated(func(a []string) []string { return slices.Insert(slices.Clone(a), 1, "-y") }),
		"an extra option":  mutated(func(a []string) []string { return slices.Insert(slices.Clone(a), 1, "-o", "Dir::Etc=/") }),
		"upgrade for real": mutated(func(a []string) []string { return append(slices.Clone(a[:len(a)-2]), "upgrade") }),
		"a path outside": mutated(func(a []string) []string {
			b := slices.Clone(a)
			b[2] = "Dir::Etc::sourcelist=/etc/apt/sources.list"
			return b
		}),
		"another directory":  osplan.UpgradeArgv(other),
		"a step up":          osplan.UpgradeArgv(dir + "/.."),
		"an empty directory": osplan.UpgradeArgv(""),
	} {
		if _, err := sb.Run(context.Background(), argv); err == nil {
			t.Errorf("%s: Run() did not refuse %q", name, argv)
		}
	}
	// the directory of another sandbox: the argvs are good for that one, and refused by this
	if _, err := sb.Run(context.Background(), osplan.UpgradeArgv("/var/tmp/casaos-os-plan.2")); err == nil {
		t.Error("the command of another sandbox ran")
	}
	// one that made no directory has none to run in
	if _, err := (&osPlanSandbox{searchPath: sb.searchPath}).Run(context.Background(), osplan.UpgradeArgv(dir)); err == nil {
		t.Error("a sandbox that made no directory ran a command")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a command that was refused ran")
	}
	// and the four are the ones that run
	for _, argv := range [][]string{osplan.UpdateArgv(dir), osplan.UpgradeArgv(dir), osplan.DistUpgradeArgv(dir), osplan.EstimateArgv(dir)} {
		if _, err := sb.Run(context.Background(), argv); err != nil {
			t.Errorf("Run(%q) error = %v", argv, err)
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the allowed commands did not run: %v", err)
	}
}

func processGone(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	// a process that is dead and that nobody has waited for yet is dead
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	i := strings.LastIndexByte(string(data), ')')
	return i >= 0 && i+2 < len(data) && data[i+2] == 'Z'
}

func waitGone(t *testing.T, what string, pid int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if processGone(pid) {
			return
		}
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("%s (%d) still runs after the end of the command", what, pid)
}

func readPids(t *testing.T, file string) (leader, child int) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("the command did not start in time to say who it is: %v", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		t.Fatalf("pids = %q", data)
	}
	leader, _ = strconv.Atoi(fields[0])
	child, _ = strconv.Atoi(fields[1])
	return leader, child
}

// The command is killed as a group at the end of its time: the processes it started go with it, and the end of the time is the
// context's error, and not an exit status.
func TestACommandThatRunsOutOfTimeIsKilledAsAGroupAndSaysSo(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pids")
	sb, dir := sandboxWithApt(t, "sleep 60 &\nprintf '%s %s\\n' $$ $! > '"+pids+"'\nwait\n")
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err := sb.Run(ctx, osplan.UpdateArgv(dir))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the end of the time", err)
	}
	if took := time.Since(began); took > 20*time.Second {
		t.Errorf("Run() took %v to give up", took)
	}
	leader, child := readPids(t, pids)
	waitGone(t, "the command", leader)
	waitGone(t, "what the command started", child)
}

func TestACommandThatIsCanceledIsKilledAsAGroupToo(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pids")
	sb, dir := sandboxWithApt(t, "sleep 60 &\nprintf '%s %s\\n' $$ $! > '"+pids+"'\nwait\n")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(pids); err == nil {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := sb.Run(ctx, osplan.DistUpgradeArgv(dir))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
	leader, child := readPids(t, pids)
	waitGone(t, "the command", leader)
	waitGone(t, "what the command started", child)
}

// A child that outlives the command and holds its output does not hold the plan up: it is not waited for for ever, and what
// was printed is not taken for the whole of it.
func TestAChildThatHoldsTheOutputOpenDoesNotHoldTheCommandUp(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pids")
	sb, dir := sandboxWithApt(t, "setsid sleep 30 &\nprintf '%s %s\\n' $$ $! > '"+pids+"'\nprintf 'done\\n'\nexit 0\n")
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	began := time.Now()
	_, err := sb.Run(ctx, osplan.UpgradeArgv(dir))
	if took := time.Since(began); took > 20*time.Second {
		t.Errorf("Run() waited %v for a child that holds the output", took)
	}
	if err == nil {
		t.Error("a command whose output is held by a child that outlives it ended well: what it printed is not known to be whole")
	}
	if _, child := readPids(t, pids); child > 0 {
		_ = syscall.Kill(child, syscall.SIGKILL)
	}
}

func TestTheOutputIsBoundedAndSaysItWasCut(t *testing.T) {
	sb, dir := sandboxWithApt(t, "yes aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa | head -c 9437184\nexit 0\n")
	out, err := sb.Run(context.Background(), osplan.DistUpgradeArgv(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || len(out.Text) != osplan.MaxOutputBytes || out.ExitCode != 0 {
		t.Errorf("truncated = %v, len = %d, exit = %d", out.Truncated, len(out.Text), out.ExitCode)
	}
}

// ---- the real outputs of the measurement, through the real sandbox ------------------------------------------------

func realFixture(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "pkg", "osplan", "testdata", "real", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the measurement's output %s is not there: %v", name, err)
	}
	return p
}

// What apt printed on the real guest for the update and the two simulations, printed by a stand-in for apt-get through the
// production sandbox, parses as it did: the text comes back byte for byte, with the status of the run. The estimate is the status
// 100 of a Trivial-Only run that apt ends with its own error, as apt 2.2.4 did in a Debian 11 chroot (testdata/real/chroot-d11-12-
// size.txt, parsed in pkg/osplan); the guest of the measurement was never asked it, and the sizes printed here are this test's own.
func TestWhatAptPrintedOnTheRealGuestComesBackWhole(t *testing.T) {
	update := realFixture(t, "sandbox-paused-update.txt")
	upgrade := realFixture(t, "sandbox-paused-upgrade.txt")
	dist := realFixture(t, "sandbox-paused-dist-upgrade.txt")
	sb, dir := sandboxWithApt(t, `
case " $* " in
*"Trivial-Only=true "*) cat '`+dist+`'; printf '%s\n' 'Need to get 0 B/140 MB of archives.' 'After this operation, 300 MB of additional disk space will be used.' 'E: Trivial Only specified but this is not a trivial operation.'; exit 100 ;;
*" update "*) cat '`+update+`' ;;
*" -s upgrade "*) cat '`+upgrade+`' ;;
*" -s dist-upgrade "*) cat '`+dist+`' ;;
*) printf 'unexpected: %s\n' "$*"; exit 2 ;;
esac
`)
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	ctx := context.Background()

	up, err := sb.Run(ctx, osplan.UpdateArgv(dir))
	if err != nil || up.Text != read(update) || up.ExitCode != 0 || up.Truncated {
		t.Fatalf("update: %d bytes, exit %d, err %v: not what the guest printed", len(up.Text), up.ExitCode, err)
	}
	if f := osplan.ParseUpdate(up); f != nil {
		t.Errorf("ParseUpdate() = %v", f)
	}

	minimal, err := sb.Run(ctx, osplan.UpgradeArgv(dir))
	if err != nil || minimal.Text != read(upgrade) {
		t.Fatalf("upgrade: err %v, text differs", err)
	}
	run, f := osplan.ParseSolver(minimal)
	if f != nil || run.Summary != (osplan.Summary{Upgraded: 274, New: 0, Remove: 0, NotUpgraded: 111}) {
		t.Errorf("upgrade: %+v, %v", run, f)
	}

	full, err := sb.Run(ctx, osplan.DistUpgradeArgv(dir))
	if err != nil || full.Text != read(dist) {
		t.Fatalf("dist-upgrade: err %v, text differs", err)
	}
	run, f = osplan.ParseSolver(full)
	if f != nil || run.Summary != (osplan.Summary{Upgraded: 384, New: 75, Remove: 2, NotUpgraded: 0}) || !slices.Equal(run.Removals(), []string{"fuse", "libsemanage1"}) {
		t.Errorf("dist-upgrade: %+v, %v", run, f)
	}

	est, err := sb.Run(ctx, osplan.EstimateArgv(dir))
	if err != nil || est.ExitCode != 100 {
		t.Fatalf("estimate: exit %d, err %v: apt's normal end here is a status, and not an error", est.ExitCode, err)
	}
	if e := osplan.ParseEstimate(est, run.Summary, dir); !e.Known || e.DownloadBytes == nil || e.ExtraBytes == nil {
		t.Errorf("estimate = %+v", e)
	}
}

// ---- the temporary directory, and the sweep that leaves alone what the core did not make ----------------------------------------

// The one directory that anyone may write in is open to them whatever the umask of the core is, with its sticky bit.
func TestTheTmpDirectoryIsOpenWhateverTheUmaskIs(t *testing.T) {
	sb, dir := madeSandbox(t)
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	if err := sb.WriteFile(dir+"/tmp", nil, fs.ModeDir|fs.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(dir + "/tmp")
	if err != nil || !info.IsDir() || info.Mode()&(fs.ModePerm|fs.ModeSticky) != 0o777|fs.ModeSticky {
		t.Fatalf("tmp: %v, %v: want 1777", info, err)
	}
	// and a directory with no sticky bit is made without it
	if err := sb.WriteFile(dir+"/plain", nil, fs.ModeDir|0o755); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(dir + "/plain"); err != nil || info.Mode()&fs.ModeSticky != 0 || info.Mode().Perm() != 0o755 {
		t.Errorf("plain: %v, %v", info, err)
	}
}

// The sweep leaves alone, and says so in the log, a directory that has the name of a plan's but is not one the core made: made by
// somebody else, writable by others, on another file system.
func TestTheSweepLeavesAloneADirectoryThatIsNotTheCores(t *testing.T) {
	log := captureLog(t)
	parent := t.TempDir()
	mk := func(name string, mode fs.FileMode) string {
		p := filepath.Join(parent, name)
		if err := os.MkdirAll(filepath.Join(p, "lists"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		age(t, p, 3*time.Hour)
		return p
	}
	mine := mk("casaos-os-plan.1", 0o711)
	group := mk("casaos-os-plan.2", 0o770)
	others := mk("casaos-os-plan.3", 0o707)
	removed, err := sweepStaleSandboxes(parent, time.Now(), osplan.StaleAfter)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []string{mine}) {
		t.Errorf("removed = %v, want only %s", removed, mine)
	}
	for _, kept := range []string{group, others} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("%s was removed: %v", kept, err)
		}
		if !strings.Contains(log.String(), kept) {
			t.Errorf("the log does not say that %s was left alone:\n%s", kept, log.String())
		}
	}
}

// The plan of a module makes its directory with the one prefix, and sweeps the stale ones with osplan.StaleAfter before it does:
// a plan of an upgrade that is at work, for as long as its job may last, is younger than that, and is not swept from under it.
// A removal that fails is said in the core's log.
func TestAPlanOfAnUpgradeAtWorkIsNotSweptByAPlanOfAModule(t *testing.T) {
	if osPlanJobTimeout >= osplan.StaleAfter {
		t.Errorf("the job of a plan has %v: a sweep after %v may remove the directory of one that is at work", osPlanJobTimeout, osplan.StaleAfter)
	}
	parent := t.TempDir()
	live := filepath.Join(parent, "casaos-os-plan.7")
	if err := os.Mkdir(live, 0o711); err != nil {
		t.Fatal(err)
	}
	age(t, live, osPlanJobTimeout)
	if removed, err := osplan.SweepStale(parent, time.Now(), osplan.StaleAfter, nil); err != nil || len(removed) != 0 {
		t.Errorf("removed %v, %v", removed, err)
	}
	sb, _ := madeSandbox(t)
	if sb.Log == nil {
		t.Error("a removal that fails would not be said in the core's log")
	}
}
