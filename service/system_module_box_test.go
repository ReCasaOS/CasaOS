package service

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// A moduleBox is a machine for the tests of the modules: a root of its own where the assistant finds its catalogue, its manifests, its logs and its lock (nothing is read or written
// outside it), a clock that a test moves, and a systemd that says what the test tells it, understands patterns, and records every unit that is started.
type moduleBox struct {
	t       *testing.T
	updater *systemPackageUpdater
	system  *systemService
	root    string
	env     modulecmd.Env
	// busyFile is the flag of an open upgrade of the operating system, which is not there until a test puts it
	busyFile string
	// binary is the file that stands for /usr/bin/casaos, the program the unit runs: a regular, executable file until a test changes it
	binary string

	mu       sync.Mutex
	clock    time.Time
	units    map[string]string // the ActiveState of the units systemd has loaded; one it does not have is inactive
	silent   bool              // systemd does not answer
	hang     bool              // the commands of the assistant do not end
	started  [][]string        // path, unit and arguments of every systemd-run
	startErr error
	startOut string
	onStart  func(args []string)
	dpkg     bool
	dpkgErr  error
	entries  []osmodule.Entry
	// freeBytes is the room the disks have; commands counts what the core asked systemd
	freeBytes int64
	freeErr   error
	onLock    func()
	commands  atomic.Int64
}

const (
	boxModuleTag = "v0.1.0"
	boxModuleSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// moduleHost is the host of the assistant on the box of the test: the real one for the lock and for who owns a folder, and a disk with room and a dpkg that nobody holds.
type moduleHost struct {
	modulecmd.Host
	box *moduleBox
}

func (h moduleHost) FreeBytes(string) (int64, error) {
	h.box.mu.Lock()
	defer h.box.mu.Unlock()
	return h.box.freeBytes, h.box.freeErr
}

// Lock is the lock of the assistant, and a test may have it wait where it is asked for.
func (h moduleHost) Lock(path string) (func(), error) {
	h.box.mu.Lock()
	hook := h.box.onLock
	h.box.mu.Unlock()
	if hook != nil {
		hook()
	}
	return h.Host.Lock(path)
}

func (h moduleHost) DpkgLockHeld([]string) (bool, error) {
	h.box.mu.Lock()
	defer h.box.mu.Unlock()
	return h.box.dpkg, h.box.dpkgErr
}

// moduleRunner runs nothing: it answers what the assistant asks of systemd.
type moduleRunner struct{ box *moduleBox }

func (r moduleRunner) Run(ctx context.Context, c modulecmd.Cmd) (modulecmd.Output, error) {
	r.box.mu.Lock()
	hang, silent := r.box.hang, r.box.silent
	r.box.mu.Unlock()
	if hang {
		<-ctx.Done()
		return modulecmd.Output{}, ctx.Err()
	}
	if silent {
		return modulecmd.Output{}, os.ErrDeadlineExceeded
	}
	if len(c.Argv) >= 3 && c.Argv[0] == "systemctl" && c.Argv[1] == "show" {
		out, _ := r.box.command(ctx, "systemctl", c.Argv[1:]...)
		return modulecmd.Output{Text: string(out)}, nil
	}
	return modulecmd.Output{Text: "inactive\n"}, nil
}

func newModuleBox(t *testing.T) *moduleBox {
	t.Helper()
	b := &moduleBox{t: t, root: t.TempDir(), clock: time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC), units: map[string]string{}, freeBytes: 100 << 30}
	b.busyFile = withOSUpgradeBusyFile(t) // the flag of an upgrade is not looked for on the machine of the test
	b.updater = newTestSystemPackageUpdater(t)
	b.updater.now = b.now
	b.updater.command = b.command
	b.updater.start = b.startUnit
	b.updater.stat = b.stat
	b.binary = filepath.Join(t.TempDir(), "casaos")
	if err := os.WriteFile(b.binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	b.updater.dpkgLockState = func() (bool, error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.dpkg, b.dpkgErr
	}
	b.env = modulecmd.Env{
		Roots: modulecmd.Roots{Prefix: b.root}, EUID: func() int { return 0 }, Arch: "amd64", Version: "0.5.29",
		Runner: moduleRunner{b}, Host: moduleHost{Host: modulecmd.Production().Host, box: b},
	}
	b.updater.moduleEnv = func() modulecmd.Env { return b.env }
	b.system = &systemService{packageUpdates: b.updater}
	b.write("/etc/os-release", "ID=debian\nVERSION_ID=\"12\"\n")
	b.setCatalogue("virt")
	return b
}

// restarted is the core after a restart: a service that has nothing of the one before but the box.
func (b *moduleBox) restarted() *systemService {
	b.t.Helper()
	fresh := newTestSystemPackageUpdater(b.t)
	fresh.now, fresh.command, fresh.start, fresh.stat = b.now, b.command, b.startUnit, b.stat
	fresh.dpkgLockState = b.updater.dpkgLockState
	fresh.moduleEnv = b.updater.moduleEnv
	return &systemService{packageUpdates: fresh}
}

func (b *moduleBox) now() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.clock
}

func (b *moduleBox) advance(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clock = b.clock.Add(d)
}

func (b *moduleBox) setUnit(unit, state string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.units[unit] = state
}

// stat is the box's file system as the core asks it for the program of the unit: /usr/bin/casaos is the file of the box, and any other path is the machine's own.
func (b *moduleBox) stat(path string) (os.FileInfo, error) {
	if path == systemModuleBinary {
		return os.Stat(b.binary)
	}
	return os.Stat(path)
}

// command is a process that is run with the context it is given: one whose context has ended does not run, as a real one does not.
func (b *moduleBox) command(ctx context.Context, name string, args ...string) ([]byte, error) {
	b.commands.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	silent, units := b.silent, make(map[string]string, len(b.units))
	for k, v := range b.units {
		units[k] = v
	}
	b.mu.Unlock()
	if silent && name == "systemctl" {
		return nil, os.ErrDeadlineExceeded
	}
	// a fake of `systemctl show <unit> --property=LoadState --value`: "loaded" for a unit that the test put, "not-found" for any other (that a unit that ended and was collected says it, and
	// that a unit that runs says "loaded", is to be proved on a real guest: docs/os-modules.md)
	if name == "systemctl" && len(args) >= 3 && args[0] == "show" && args[2] == "--property=LoadState" {
		if _, ok := units[args[1]]; ok {
			return []byte("loaded\n"), nil
		}
		return []byte("not-found\n"), nil
	}
	return moduleSystemd(units)(ctx, name, args...)
}

func (b *moduleBox) startUnit(path, unit string, args ...string) ([]byte, error) {
	b.mu.Lock()
	b.started = append(b.started, append([]string{path, unit}, args...))
	err, out, hook := b.startErr, b.startOut, b.onStart
	b.mu.Unlock()
	if hook != nil {
		hook(args)
	}
	return []byte(out), err
}

func (b *moduleBox) startedUnits() [][]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([][]string(nil), b.started...)
}

func (b *moduleBox) write(abs, content string) {
	b.t.Helper()
	p := b.env.Roots.Path(abs)
	if p == abs { // the roots are not set yet
		p = filepath.Join(b.root, filepath.FromSlash(abs))
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		b.t.Fatal(err)
	}
}

func (b *moduleBox) read(abs string) string {
	b.t.Helper()
	raw, err := os.ReadFile(b.env.Roots.Path(abs))
	if err != nil {
		b.t.Fatal(err)
	}
	return string(raw)
}

func (b *moduleBox) exists(abs string) bool {
	_, err := os.Lstat(b.env.Roots.Path(abs))
	return err == nil
}

// entry is a module of the catalogue that needs no package, so that its plan asks apt nothing.
func (b *moduleBox) entry(name, tag string) osmodule.Entry {
	component := "CasaOS-" + strings.ToUpper(name[:1]) + name[1:]
	return osmodule.Entry{
		Name: name, Component: component, Tier: osmodule.TierCommunity, Tag: tag, Commit: strings.Repeat("a", 40),
		Assets: osmodule.Assets{AMD64: &osmodule.Asset{
			URL:    "https://github.com/ReCasaOS/" + component + "/releases/download/" + tag + "/linux-amd64-casaos-" + name + "-" + tag + ".tar.gz",
			SHA256: boxModuleSHA,
		}},
		Manifest: osmodule.Manifest{
			Schema: 1, Name: name, Title: "Module " + name, Description: "A module of the tests.", Component: component,
			Unit: "casaos-" + name + ".service", GatewayRoute: "/v2/" + name, MinReCasaOS: "0.5.23", OS: []string{"debian:12"},
		},
	}
}

// setCatalogue puts the catalogue of this release on the box: these modules, at the tag of the tests.
func (b *moduleBox) setCatalogue(names ...string) {
	b.t.Helper()
	b.entries = nil
	for _, n := range names {
		b.entries = append(b.entries, b.entry(n, boxModuleTag))
	}
	b.writeCatalogue()
}

func (b *moduleBox) writeCatalogue() {
	b.t.Helper()
	raw, err := json.Marshal(osmodule.Catalogue{Schema: 1, Modules: b.entries})
	if err != nil {
		b.t.Fatal(err)
	}
	b.write("/usr/share/casaos/modules/catalogue.json", string(raw))
}

// install puts a module on the box as an install leaves it: its manifest.
func (b *moduleBox) install(name string) {
	b.t.Helper()
	raw, err := json.Marshal(b.entry(name, boxModuleTag).Manifest)
	if err != nil {
		b.t.Fatal(err)
	}
	b.write("/etc/casaos/modules/"+name+".json", string(raw))
}

func (b *moduleBox) logPath(name string) string {
	return b.env.Roots.Path("/var/log/casaos/module-" + name + ".log")
}

// unitSays is what the unit of a job writes: its log is replaced by these lines, as its start truncates the log the core wrote. The log is given the time of the box.
func (b *moduleBox) unitSays(name string, lines ...string) {
	b.t.Helper()
	p := b.logPath(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		b.t.Fatal(err)
	}
	if err := os.Chtimes(p, b.now(), b.now()); err != nil {
		b.t.Fatal(err)
	}
}

// touchLog makes the log of a module have been written d from now.
func (b *moduleBox) touchLog(name string, d time.Duration) {
	b.t.Helper()
	if err := os.Chtimes(b.logPath(name), b.now().Add(d), b.now().Add(d)); err != nil {
		b.t.Fatal(err)
	}
}

// hookRunner is a runner that calls fn at the call number at, before it answers.
type hookRunner struct {
	inner modulecmd.Runner
	calls *atomic.Int64
	at    int64
	fn    func()
}

func (r hookRunner) Run(ctx context.Context, c modulecmd.Cmd) (modulecmd.Output, error) {
	if r.calls.Add(1) == r.at {
		r.fn()
	}
	return r.inner.Run(ctx, c)
}

// stamp is the time of the box moved by d, as the markers spell it.
func (b *moduleBox) stamp(d time.Duration) string { return b.now().Add(d).UTC().Format(time.RFC3339) }

func (b *moduleBox) step(nonce, name, step, phase string, d time.Duration) string {
	return "CASAOS_MODULE_STEP " + nonce + " " + name + " " + step + " " + phase + " " + b.stamp(d)
}

func (b *moduleBox) result(nonce, name, kind, step, code, undo string, d time.Duration) string {
	return "CASAOS_MODULE_RESULT " + nonce + " " + name + " " + kind + " " + step + " " + code + " " + undo + " " + b.stamp(d)
}

// nonceOfLog is the nonce of the job whose first line the core wrote.
func (b *moduleBox) nonceOfLog(name string) string {
	b.t.Helper()
	fields := strings.Fields(strings.SplitN(b.read("/var/log/casaos/module-"+name+".log"), "\n", 2)[0])
	if len(fields) != 4 || fields[0] != "CASAOS_MODULE_QUEUED" {
		b.t.Fatalf("the first line of the log is %q", fields)
	}
	return fields[1]
}

// tree is every file and folder under the root of the box, with the content of the files.
func (b *moduleBox) tree() map[string]string {
	b.t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(b.root, func(p string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(b.root, p)
		if err != nil {
			// (a folder that is a link to itself: it is there, and cannot be read)
			out[rel+"!"] = err.Error()
			return nil
		}
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(p)
			out[rel] = "-> " + target
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = string(raw)
		return nil
	})
	if err != nil {
		b.t.Fatal(err)
	}
	return out
}

// assertNothingWritten fails if the box is not as it was in before.
func (b *moduleBox) assertNothingWritten(before map[string]string) {
	b.t.Helper()
	after := b.tree()
	var diffs []string
	for p, v := range after {
		if old, ok := before[p]; !ok {
			diffs = append(diffs, "new: "+p)
		} else if old != v {
			diffs = append(diffs, "changed: "+p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			diffs = append(diffs, "gone: "+p)
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 0 {
		b.t.Errorf("the box is not as it was:\n%s", strings.Join(diffs, "\n"))
	}
}
