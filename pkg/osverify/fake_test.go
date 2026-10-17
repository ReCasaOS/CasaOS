package osverify

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// fakeProbe is a machine in a few maps: files, directories, the answers of commands, the clock. Nothing in these
// tests reads the real one. Everything runs in the goroutine of the test, so nothing here has a lock.
type fakeProbe struct {
	files  map[string]string
	dirs   map[string][]string
	exists map[string]bool // paths that Stat finds (a socket, say) and that are not read
	cmds   map[string]fakeCmd
	hangs  map[string]bool // commands that do not end until their time does
	fsErr  map[string]error
	now    time.Time

	// asked is every command that reached the machine, in order, and read every file that was opened
	asked  [][]string
	read   []string
	listed []string
}

type fakeCmd struct {
	text      string
	exit      int
	err       error
	truncated bool
}

func argvKey(argv ...string) string { return strings.Join(argv, "\x1f") }

func newFake() *fakeProbe {
	return &fakeProbe{
		files: map[string]string{}, dirs: map[string][]string{}, exists: map[string]bool{}, cmds: map[string]fakeCmd{}, hangs: map[string]bool{}, fsErr: map[string]error{},
		now: time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC),
	}
}

// on says what a command prints and how it ends; a command that is not declared is a tool that is not installed.
func (f *fakeProbe) on(text string, exit int, argv ...string) {
	f.cmds[argvKey(argv...)] = fakeCmd{text: text, exit: exit}
}

func (f *fakeProbe) onError(err error, argv ...string) { f.cmds[argvKey(argv...)] = fakeCmd{err: err} }

func (f *fakeProbe) onTruncated(text string, argv ...string) {
	f.cmds[argvKey(argv...)] = fakeCmd{text: text, truncated: true}
}

func (f *fakeProbe) forget(argv ...string) { delete(f.cmds, argvKey(argv...)) }

// hangRead makes a file one that does not answer when it is read, as a file of a file system that has gone away does not.
func (f *fakeProbe) hangRead(path string) { f.hangs["read:"+path] = true }

// hang makes a command one that does not answer: it ends with the time that it is given, and not before.
func (f *fakeProbe) hang(argv ...string) { f.hangs[argvKey(argv...)] = true }

func (f *fakeProbe) set(path, content string) { f.files[path] = content }

func (f *fakeProbe) dir(path string, names ...string) { f.dirs[path] = names }

func (f *fakeProbe) fail(path string, err error) { f.fsErr[path] = err }

func notExist(op, path string) error { return &fs.PathError{Op: op, Path: path, Err: fs.ErrNotExist} }

func (f *fakeProbe) ReadFile(ctx context.Context, path string) ([]byte, error) {
	f.read = append(f.read, path)
	if f.hangs["read:"+path] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := f.fsErr[path]; err != nil {
		return nil, err
	}
	if content, ok := f.files[path]; ok {
		if len(content) > osreadiness.MaxFileBytes {
			return nil, osreadiness.ErrTooLarge
		}
		return []byte(content), nil
	}
	return nil, notExist("open", path)
}

func (f *fakeProbe) ReadHead(ctx context.Context, path string, max int) ([]byte, error) {
	data, err := f.ReadFile(ctx, path)
	if err != nil {
		return nil, err
	}
	return data[:min(len(data), max)], nil
}

func (f *fakeProbe) ReadDir(ctx context.Context, path string) ([]string, error) {
	f.listed = append(f.listed, path)
	if err := f.fsErr[path]; err != nil {
		return nil, err
	}
	names, ok := f.dirs[path]
	if !ok {
		return nil, notExist("open", path)
	}
	out := slices.Clone(names)
	slices.Sort(out)
	return out, nil
}

func (f *fakeProbe) Stat(ctx context.Context, path string) (osreadiness.FileInfo, error) {
	if err := f.fsErr[path]; err != nil {
		return osreadiness.FileInfo{}, err
	}
	if _, ok := f.files[path]; ok || f.exists[path] {
		return osreadiness.FileInfo{}, nil
	}
	return osreadiness.FileInfo{}, notExist("stat", path)
}

func (f *fakeProbe) Lstat(ctx context.Context, path string) (osreadiness.FileInfo, error) {
	return f.Stat(ctx, path)
}

func (f *fakeProbe) Readlink(ctx context.Context, path string) (string, error) {
	return "", notExist("readlink", path)
}

func (f *fakeProbe) Run(ctx context.Context, argv ...string) (osreadiness.Output, error) {
	f.asked = append(f.asked, slices.Clone(argv))
	if f.hangs[argvKey(argv...)] {
		<-ctx.Done()
		return osreadiness.Output{}, ctx.Err()
	}
	cmd, ok := f.cmds[argvKey(argv...)]
	if !ok {
		return osreadiness.Output{}, osreadiness.ErrToolMissing
	}
	if cmd.err != nil {
		return osreadiness.Output{}, cmd.err
	}
	return osreadiness.Output{Text: cmd.text, ExitCode: cmd.exit, Truncated: cmd.truncated}, nil
}

func (f *fakeProbe) FreeBytes(ctx context.Context, path string) (uint64, error) { return 1 << 40, nil }

func (f *fakeProbe) MaintenanceBusy(ctx context.Context) (string, bool, error) { return "", false, nil }

func (f *fakeProbe) AppOperations(ctx context.Context) ([]string, error) { return nil, nil }

func (f *fakeProbe) Now() time.Time { return f.now }

func (f *fakeProbe) EUID() int { return 0 }

func (f *fakeProbe) Arch() string { return "amd64" }

// fixture reads a file of testdata, as the box would have written it: the checkout of a Windows machine may have
// made its lines end in CRLF.
func fixture(t testing.TB, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

// The arguments of the commands the facts are taken with, spelled out here and not taken from the code under test.
var (
	argvDockerInfo  = []string{"docker", "info", "--format", dockerpkg.DaemonInfoFormat}
	argvDockerPs    = []string{"docker", "ps", "-q"}
	argvDockerState = []string{"systemctl", "show", "docker.service", "--property=ActiveState", "--value"}
)

func argvUnit(unit string) []string {
	return []string{"systemctl", "show", unit, "--property=ActiveState", "--value"}
}

func argvInspect(ids ...string) []string {
	return append([]string{"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat}, ids...)
}

// containerIDs are the ids that the fixture box's "docker ps -q" prints for the four containers of testdata/docker-inspect.
var containerIDs = []string{"1a2b3c4d5e6f", "2b3c4d5e6f70", "3c4d5e6f7081", "4d5e6f708192"}

// healthyBox is the machine that the fixtures of testdata describe, in the state it is in before an upgrade and after
// one that went well: Samba with three users, a mergerfs pool under /DATA/pool and another under /media/Media Library,
// a default route out by eth0, DNS that answers, Docker up with four containers, ReCasaOS with its units, Docker's
// source paused (as the upgrade leaves it when Docker's repository is not asked).
func healthyBox(t testing.TB) *fakeProbe {
	t.Helper()
	f := newFake()
	f.on("6.1.0-25-amd64\n", 0, "uname", "-r")
	f.on("running\n", 0, "systemctl", "is-system-running")
	f.on("", 0, "dpkg", "--audit")
	f.on(fixture(t, "pdbedit-L"), 0, "pdbedit", "-L")
	f.set("/proc/mounts", fixture(t, "proc-mounts"))
	f.set("/proc/net/route", fixture(t, "proc-net-route"))
	f.on(ssOutput(fixture(t, "bk/listening-after.txt")), 0, "ss", "-Hltn")
	f.on(failedOutput(fixture(t, "bk/failed-units-after.txt")), 0, "systemctl", "--failed", "--no-legend", "--plain")
	f.dir("/usr/lib/systemd/system", "casaos.service", "casaos-gateway.service", "casaos-message-bus.service", "casaos-user-service.service", "casaos-local-storage.service", "casaos-app-management.service", "ssh.service", "docker.service")
	f.dir("/etc/systemd/system", "multi-user.target.wants", "casaos.service.d")
	for _, unit := range []string{"casaos.service", "casaos-gateway.service", "casaos-message-bus.service", "casaos-user-service.service", "casaos-local-storage.service", "casaos-app-management.service"} {
		f.on("active\n", 0, argvUnit(unit)...)
	}
	f.on("active\n", 0, argvDockerState...)
	f.on(`{"ServerVersion":"24.0.7","SwarmState":"inactive"}`+"\n", 0, argvDockerInfo...)
	f.on(strings.Join(containerIDs, "\n")+"\n", 0, argvDockerPs...)
	f.on(fixture(t, "docker-inspect"), 0, argvInspect(containerIDs...)...)
	f.set("/var/lib/docker/engine-id", "0b7d1a54-6c0e-4f6c-9a46-5d3d1f0f1c11\n")
	f.set("/etc/apt/sources.list", "deb http://deb.debian.org/debian bookworm main\ndeb http://security.debian.org/debian-security bookworm-security main\n")
	f.dir("/etc/apt/sources.list.d", "docker.list.casaos-paused")
	f.set("/etc/apt/sources.list.d/docker.list.casaos-paused", "deb [arch=amd64] https://download.docker.com/linux/debian bullseye stable\n")
	return f
}

// ssOutput is what ss -Hltn prints for these local addresses.
func ssOutput(addresses string) string {
	var out strings.Builder
	for _, address := range strings.Fields(addresses) {
		out.WriteString("LISTEN 0      4096       " + address + "      0.0.0.0:*\n")
	}
	return out.String()
}

// failedOutput is what systemctl --failed prints for these units.
func failedOutput(units string) string {
	var out strings.Builder
	for _, unit := range strings.Fields(units) {
		out.WriteString(unit + " loaded failed failed Some description\n")
	}
	return out.String()
}

// healthyResolver is a resolver that answers.
func healthyResolver(context.Context, string) bool { return true }
