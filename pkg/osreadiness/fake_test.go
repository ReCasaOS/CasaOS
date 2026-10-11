package osreadiness

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProbe is a machine in a map: files, links, directories, the answers of commands, the clock.
// Nothing in the tests reads the real one. The checks run in several goroutines, so everything
// that is read or recorded is under the lock.
type fakeProbe struct {
	mu sync.Mutex

	files map[string]string // regular files
	links map[string]string // symbolic links, by path
	dirs  map[string]bool   // directories that are empty (a directory with something in it is implied)
	fsErr map[string]error  // an error for any access to a path
	cmds  map[string]fakeCmd

	busyReason string
	apps       []string
	appsErr    error
	free       map[string]uint64
	now        time.Time
	euid       int
	arch       string

	// recorded: everything a check asked
	reads    []string
	attempts [][]string
}

type fakeCmd struct {
	text string
	exit int
	err  error
	// truncated says that the output was cut at the bound
	truncated bool
	// block holds the command until it is closed or its context ends
	block chan struct{}
	// delay is how long the command takes
	delay time.Duration
}

func key(argv ...string) string { return strings.Join(argv, "\x1f") }

func (f *fakeProbe) on(text string, exit int, argv ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds[key(argv...)] = fakeCmd{text: text, exit: exit}
}

func (f *fakeProbe) onTruncated(text string, argv ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds[key(argv...)] = fakeCmd{text: text, truncated: true}
}

func (f *fakeProbe) onError(err error, argv ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds[key(argv...)] = fakeCmd{err: err}
}

func (f *fakeProbe) set(path, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = content
}

func (f *fakeProbe) link(path, target string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.links[path] = target
}

func (f *fakeProbe) remove(paths ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range paths {
		delete(f.files, p)
		delete(f.links, p)
		delete(f.dirs, p)
	}
}

func (f *fakeProbe) fail(path string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fsErr[path] = err
}

func (f *fakeProbe) record(what string) {
	f.reads = append(f.reads, what)
}

func (f *fakeProbe) isDir(p string) bool {
	if f.dirs[p] {
		return true
	}
	prefix := strings.TrimSuffix(p, "/") + "/"
	for _, m := range []map[string]string{f.files, f.links} {
		for name := range m {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		}
	}
	for name := range f.dirs {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func notExist(op, p string) error { return &fs.PathError{Op: op, Path: p, Err: fs.ErrNotExist} }

func (f *fakeProbe) ReadFile(ctx context.Context, p string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("read " + p)
	if err := f.fsErr[p]; err != nil {
		return nil, err
	}
	if content, ok := f.files[p]; ok {
		if len(content) > MaxFileBytes {
			return nil, ErrTooLarge
		}
		return []byte(content), nil
	}
	if f.isDir(p) {
		return nil, &fs.PathError{Op: "read", Path: p, Err: errors.New("is a directory")}
	}
	return nil, notExist("open", p)
}

func (f *fakeProbe) ReadDir(ctx context.Context, p string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("readdir " + p)
	if err := f.fsErr[p]; err != nil {
		return nil, err
	}
	if !f.isDir(p) {
		return nil, notExist("open", p)
	}
	prefix := strings.TrimSuffix(p, "/") + "/"
	seen := map[string]bool{}
	for _, m := range []map[string]string{f.files, f.links} {
		for name := range m {
			if rest, ok := strings.CutPrefix(name, prefix); ok {
				first, _, _ := strings.Cut(rest, "/")
				seen[first] = true
			}
		}
	}
	for name := range f.dirs {
		if rest, ok := strings.CutPrefix(name, prefix); ok && rest != "" {
			first, _, _ := strings.Cut(rest, "/")
			seen[first] = true
		}
	}
	names := slices.Sorted(maps.Keys(seen))
	if names == nil {
		names = []string{}
	}
	return names, nil
}

func (f *fakeProbe) stat(p string, follow bool) (FileInfo, error) {
	f.record("stat " + p)
	if err := f.fsErr[p]; err != nil {
		return FileInfo{}, err
	}
	if target, ok := f.links[p]; ok {
		if !follow {
			return FileInfo{Mode: fs.ModeSymlink | 0o777}, nil
		}
		resolved := target
		if !strings.HasPrefix(target, "/") {
			resolved = path.Join(path.Dir(p), target)
		}
		return f.stat(resolved, true)
	}
	if content, ok := f.files[p]; ok {
		return FileInfo{Size: int64(len(content)), Mode: 0o644}, nil
	}
	if f.isDir(p) {
		return FileInfo{Mode: fs.ModeDir | 0o755}, nil
	}
	return FileInfo{}, notExist("stat", p)
}

func (f *fakeProbe) Stat(ctx context.Context, p string) (FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stat(p, true)
}

func (f *fakeProbe) Lstat(ctx context.Context, p string) (FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stat(p, false)
}

func (f *fakeProbe) Readlink(ctx context.Context, p string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("readlink " + p)
	if err := f.fsErr[p]; err != nil {
		return "", err
	}
	if target, ok := f.links[p]; ok {
		return target, nil
	}
	if _, ok := f.files[p]; ok || f.isDir(p) {
		return "", &fs.PathError{Op: "readlink", Path: p, Err: errors.New("invalid argument")}
	}
	return "", notExist("readlink", p)
}

func (f *fakeProbe) Run(ctx context.Context, argv ...string) (Output, error) {
	f.mu.Lock()
	f.attempts = append(f.attempts, slices.Clone(argv))
	cmd, ok := f.cmds[key(argv...)]
	f.mu.Unlock()
	if !ok && argv[0] == "dpkg-query" {
		// what dpkg-query says of patterns that match no package that is there: nothing, and 1
		return Output{ExitCode: 1}, nil
	}
	if !ok {
		return Output{}, fmt.Errorf("%w: %s", ErrToolMissing, argv[0])
	}
	if cmd.delay > 0 {
		select {
		case <-time.After(cmd.delay):
		case <-ctx.Done():
			return Output{}, ctx.Err()
		}
	}
	if cmd.block != nil {
		select {
		case <-cmd.block:
		case <-ctx.Done():
			return Output{}, ctx.Err()
		}
	}
	if cmd.err != nil {
		return Output{}, cmd.err
	}
	return Output{Text: cmd.text, ExitCode: cmd.exit, Truncated: cmd.truncated}, nil
}

func (f *fakeProbe) FreeBytes(ctx context.Context, p string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("free " + p)
	if free, ok := f.free[p]; ok {
		return free, nil
	}
	return 0, notExist("statfs", p)
}

func (f *fakeProbe) MaintenanceBusy(context.Context) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("busy")
	return f.busyReason, f.busyReason != ""
}

func (f *fakeProbe) AppOperations(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("apps")
	return slices.Clone(f.apps), f.appsErr
}

func (f *fakeProbe) Now() time.Time { return f.now }
func (f *fakeProbe) EUID() int      { return f.euid }
func (f *fakeProbe) Arch() string   { return f.arch }

// the commands a check asks, as argv, built the way the checks build them
var (
	argvAudit      = []string{"dpkg", "--audit"}
	argvArch       = []string{"dpkg", "--print-architecture"}
	argvForeign    = []string{"dpkg", "--print-foreign-architectures"}
	argvConfigDump = []string{"apt-config", "dump"}
	argvContainer  = []string{"systemd-detect-virt", "--container"}
	argvVM         = []string{"systemd-detect-virt", "--vm"}
	busyUnitsAsked = []string{"apt-daily.service", "apt-daily-upgrade.service", "packagekit.service"}
)

func argvUnit(unit string) []string {
	return []string{"systemctl", "show", unit, "--property=ActiveState", "--value"}
}

func argvQuery(patterns ...string) []string {
	return append([]string{"dpkg-query", "-W", installedFormat}, patterns...)
}

// pkg is a line of dpkg-query as it prints an installed package.
func pkg(name, version string) string { return "ii \t" + name + "\tamd64\t" + version + "\n" }

// held is a package on hold, as dpkg-query prints it: its wanted state is "hold".
func held(name string) string { return "hi \t" + name + "\tamd64\t1.0\n" }

func pkgArch(name, arch, version string) string {
	return "ii \t" + name + "\t" + arch + "\t" + version + "\n"
}

// installed says what dpkg-query answers for a query: the lines, and 1 (nothing matched one of the
// patterns) when asked.
func (f *fakeProbe) installed(lines string, patterns ...string) {
	exit := 0
	if lines == "" {
		exit = 1
	}
	f.on(lines, exit, argvQuery(patterns...)...)
}

const debian11OSRelease = `PRETTY_NAME="Debian GNU/Linux 11 (bullseye)"
NAME="Debian GNU/Linux"
VERSION_ID="11"
VERSION="11 (bullseye)"
VERSION_CODENAME=bullseye
ID=debian
HOME_URL="https://www.debian.org/"
`

func osRelease(id, versionID, codename, pretty string) string {
	return fmt.Sprintf("PRETTY_NAME=%q\nVERSION_ID=%q\nVERSION_CODENAME=%s\nID=%s\n", pretty, versionID, codename, id)
}

const rootMountInfo = "22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw,errors=remount-ro\n"

// newBox is a healthy Debian 11 machine on amd64, that runs as root and has nothing to say: the
// sources are Debian's, nothing is held, nothing runs, and every command a check asks is answered.
func newBox(t *testing.T) *fakeProbe {
	t.Helper()
	f := &fakeProbe{
		files: map[string]string{
			"/etc/os-release":               debian11OSRelease,
			"/proc/1/comm":                  "systemd\n",
			"/proc/sys/kernel/osrelease":    "5.10.0-35-amd64\n",
			"/proc/uptime":                  "86400.55 80000.10\n",
			"/proc/1/cgroup":                "0::/init.scope\n",
			"/boot/vmlinuz-5.10.0-35-amd64": "kernel",
			"/boot/vmlinuz-5.10.0-34-amd64": "kernel",
			"/etc/apt/sources.list":         "deb http://deb.debian.org/debian bullseye main\ndeb http://deb.debian.org/debian bullseye-updates main\ndeb http://archive.debian.org/debian-security bullseye-security main\n",
			"/proc/self/mountinfo":          rootMountInfo,
			"/proc/net/route":               "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\nenp3s0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n",
		},
		links: map[string]string{},
		dirs:  map[string]bool{"/var/lib/dpkg/updates": true, "/etc/apt/sources.list.d": true, "/etc/apt/preferences.d": true},
		fsErr: map[string]error{},
		cmds:  map[string]fakeCmd{},
		free:  map[string]uint64{},
		now:   time.Date(2026, 10, 11, 20, 15, 2, 0, time.UTC),
		euid:  0,
		arch:  "amd64",
	}
	f.on("", 0, argvAudit...)
	f.on("amd64\n", 0, argvArch...)
	f.on("", 0, argvForeign...)
	f.on("APT::Install-Recommends \"true\";\nDir::Cache \"var/cache/apt\";\n", 0, argvConfigDump...)
	f.on("none\n", 1, argvContainer...)
	f.on("kvm\n", 0, argvVM...)
	f.on("Reading package lists... Done\nBuilding dependency tree... Done\n", 0, aptCheckArgs...)
	for _, unit := range busyUnitsAsked {
		f.on("inactive\n", 0, argvUnit(unit)...)
	}
	f.installed(pkg("debian-archive-keyring", "2021.1.1+deb11u1"), "debian-archive-keyring")
	f.installed(pkg("openssh-server", "1:8.4p1-5+deb11u3"), "openssh-server")
	furnish(f)
	return f
}

// attemptedCommands are the commands the checks asked for, once each.
func (f *fakeProbe) attemptedCommands() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.attempts)
}

func (f *fakeProbe) readPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reads)
}
