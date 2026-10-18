package modulecmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// ---- the module of the tests -------------------------------------------------------------------------------------------------------------------------------------------------

const (
	testName      = "virt"
	testTag       = "v0.1.0"
	testUnit      = "casaos-virt-management.service"
	testRoute     = "/v2/virt_management"
	testComponent = "CasaOS-VirtManagement"
)

var testPackages = []string{"libvirt-clients", "qemu-utils"}

func testManifest() osmodule.Manifest {
	return osmodule.Manifest{
		Schema: 1, Name: testName, Title: "Virtual machines", Description: "Create and run virtual machines.",
		Component: testComponent, Unit: testUnit, GatewayRoute: testRoute,
		Packages:    osmodule.Packages{Common: []string{"qemu-utils"}, AMD64: []string{"libvirt-clients"}},
		MinReCasaOS: "0.5.23", OS: []string{"debian:12", "ubuntu:24.04"},
	}
}

// ---- archives ----------------------------------------------------------------------------------------------------------------------------------------------------------------

type tent struct {
	name string
	typ  byte // 0 is a file
	mode int64
	body string
	link string
}

func tarBytes(t testing.TB, ents []tent) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, e := range ents {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Linkname: e.link}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := w.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gz(t testing.TB, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// moduleEntries is the archive of a module in the shape of build/sysroot of a release: a program, a unit, a configuration sample, the licence and the manifest.
func moduleEntries(manifest string) []tent {
	return []tent{
		{name: "module.json", body: manifest},
		{name: "LICENSE", body: "licence\n"},
		{name: "build/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/", typ: tar.TypeDir, mode: 0o755},
		{name: "build/sysroot/usr/bin/casaos-virt-management", mode: 0o755, body: "#!/bin/sh\nexit 0\n"},
		{name: "build/sysroot/etc/casaos/virt-management.conf.sample", body: "[libvirt]\nURI = test:///default\n"},
		{name: "build/sysroot/usr/lib/systemd/system/" + testUnit, body: "[Unit]\nDescription=virt\n[Service]\nExecStart=/usr/bin/casaos-virt-management\n[Install]\nWantedBy=multi-user.target\n"},
	}
}

func manifestJSON(t testing.TB, m osmodule.Manifest) string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// ---- the fixture ---------------------------------------------------------------------------------------------------------------------------------------------------------------

type fixture struct {
	t       *testing.T
	root    string
	env     Env
	runner  *fakeRunner
	dl      *fakeDownloader
	gw      *fakeGateway
	clock   *fakeClock
	sandbox *fakeSandbox
	host    *hostOverride
	out     *bytes.Buffer
	errOut  *bytes.Buffer
	archive []byte // the gzip file the catalogue names
	entry   osmodule.Entry
	envVars map[string]string
	isTTY   bool
	input   string
	// cleanup undoes what a test did to the box on purpose, before the box is compared with what it was
	cleanup func()
}

type fixtureOpt func(*fixtureCfg)

type fixtureCfg struct {
	entries  []tent
	manifest *osmodule.Manifest // the manifest of the catalogue
}

// newFixture makes a box in a temporary directory: Debian 12, ReCasaOS 0.5.29, apt-get there, nothing installed, a catalogue with the module, and stand-ins for everything that runs.
func newFixture(t *testing.T, opts ...fixtureOpt) *fixture {
	t.Helper()
	cfg := &fixtureCfg{}
	m := testManifest()
	cfg.manifest = &m
	for _, o := range opts {
		o(cfg)
	}
	f := &fixture{t: t, root: t.TempDir(), out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, envVars: map[string]string{}}
	f.env.Roots = Roots{Prefix: f.root} // before anything is written: no test writes outside its own directory
	f.writeFile("/etc/os-release", "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\nVERSION_ID=\"12\"\n")
	f.writeFile("/usr/bin/apt-get", "#!/bin/sh\nexit 0\n")
	f.writeFile("/etc/casaos/gateway.ini", "[common]\nruntimepath=/var/run/casaos\n\n[gateway]\nport=8080\n")
	entries := cfg.entries
	if entries == nil {
		entries = moduleEntries(manifestJSON(t, *cfg.manifest))
	}
	f.archive = gz(t, tarBytes(t, entries))
	f.entry = osmodule.Entry{
		Name: testName, Component: testComponent, Tier: osmodule.TierCommunity, Tag: testTag, Commit: strings.Repeat("a", 40),
		Assets: osmodule.Assets{AMD64: &osmodule.Asset{
			URL:    "https://github.com/ReCasaOS/" + testComponent + "/releases/download/" + testTag + "/linux-amd64-casaos-virt-management-" + testTag + ".tar.gz",
			SHA256: sum(f.archive),
		}},
		Manifest: *cfg.manifest,
	}
	f.writeCatalogue(osmodule.Catalogue{Schema: 1, Modules: []osmodule.Entry{f.entry}})
	f.runner = newFakeRunner()
	f.dl = &fakeDownloader{body: f.archive}
	f.gw = &fakeGateway{}
	f.clock = &fakeClock{now: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	f.sandbox = newFakeSandbox()
	f.host = &hostOverride{Host: baseHost(), free: func(string) (int64, error) { return 100 << 30, nil }}
	f.env = Env{
		Roots:  f.env.Roots,
		Stdin:  strings.NewReader(""),
		Stdout: f.out, Stderr: f.errOut,
		Getenv:   func(k string) string { return f.envVars[k] },
		EUID:     func() int { return 0 },
		Arch:     "amd64",
		Version:  "0.5.29",
		Runner:   f.runner,
		Sandbox:  func() osplan.Sandbox { return f.sandbox },
		Download: f.dl, Gateway: f.gw, Clock: f.clock,
		Terminal: func() bool { return f.isTTY },
		Host:     f.host,
	}
	return f
}

func (f *fixture) path(abs string) string {
	if f.env.Roots.Prefix == "" || f.env.Roots.Prefix != f.root {
		panic("a path of the box outside the directory of the test")
	}
	return f.env.Roots.Path(abs)
}

func (f *fixture) writeFile(abs, content string) {
	f.t.Helper()
	p := f.path(abs)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) writeCatalogue(c osmodule.Catalogue) {
	f.t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		f.t.Fatal(err)
	}
	f.writeFile(pathCatalogue, string(raw))
}

func (f *fixture) exists(abs string) bool {
	_, err := os.Lstat(f.path(abs))
	return err == nil
}

func (f *fixture) read(abs string) string {
	f.t.Helper()
	raw, err := os.ReadFile(f.path(abs))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(raw)
}

// main runs a command line the way the core's main does and gives the exit code; the standard output is in f.out.
func (f *fixture) main(ctx context.Context, args ...string) int {
	f.t.Helper()
	f.out.Reset()
	f.errOut.Reset()
	f.env.Stdin = strings.NewReader(f.input)
	return Main(ctx, args, f.env)
}

func (f *fixture) lines() []string {
	return strings.Split(strings.TrimRight(f.out.String(), "\n"), "\n")
}

// tree is every file and link under the box's root, with what identifies it (the content or the target, and the mode), but for the ones the assistant's own bookkeeping puts there.
func (f *fixture) tree() map[string]string {
	f.t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(f.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := f.env.Roots.Slash(p)
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(p)
			out[rel] = "-> " + target
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%s %s", sum(raw)[:12], info.Mode().Perm())
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// bookkeeping are the files the assistant keeps on a box besides the module's: its log and its lock.
func bookkeeping(p string) bool {
	return p == "/var/log/casaos/module-"+testName+".log" || p == "/var/lib/casaos/modules/.lock"
}

func (f *fixture) treeWithoutBookkeeping() map[string]string {
	t := f.tree()
	for p := range t {
		if bookkeeping(p) {
			delete(t, p)
		}
	}
	return t
}

func sameTree(t *testing.T, before, after map[string]string) {
	t.Helper()
	var diffs []string
	for p, v := range after {
		if b, ok := before[p]; !ok {
			diffs = append(diffs, "new: "+p)
		} else if b != v {
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
		t.Errorf("the box is not as it was:\n%s", strings.Join(diffs, "\n"))
	}
}

// ---- stand-ins -----------------------------------------------------------------------------------------------------------------------------------------------------------------

// hostOverride is the host of the tests: the real one on Linux, a portable one elsewhere, with the room and the dpkg lock said by the test, and the unpack's file system wrapped.
type hostOverride struct {
	Host
	free      func(string) (int64, error)
	dpkg      func([]string) (bool, error)
	wrapDest  func(osmodule.Destination) osmodule.Destination
	lockCalls int
}

func (h *hostOverride) FreeBytes(p string) (int64, error) { return h.free(p) }
func (h *hostOverride) DpkgLockHeld(paths []string) (bool, error) {
	if h.dpkg != nil {
		return h.dpkg(paths)
	}
	return false, nil
}
func (h *hostOverride) NewDest(prefix string) osmodule.Destination {
	d := h.Host.NewDest(prefix)
	if h.wrapDest != nil {
		return h.wrapDest(d)
	}
	return d
}
func (h *hostOverride) Lock(p string) (func(), error) { h.lockCalls++; return h.Host.Lock(p) }

// fakeRunner runs nothing: it answers for systemctl and apt-get from a table, keeps what systemd would (which units are enabled and so active), and records every command.
type fakeRunner struct {
	mu      sync.Mutex
	calls   [][]string
	enabled map[string]bool
	// fail maps the start of a command ("apt-get install", "systemctl daemon-reload") to its exit status
	fail map[string]int
	// text is what a command that matches says
	text map[string]string
	// busyUnit is a unit that systemctl show says is active
	busyUnit string
	// onRun is called with every command; a test makes it block or cancel
	onRun func(ctx context.Context, argv []string)
	// stdout is given the output the command prints, when the caller streams it
	said map[string]string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{enabled: map[string]bool{}, fail: map[string]int{}, text: map[string]string{}, said: map[string]string{}}
}

func (r *fakeRunner) Run(ctx context.Context, c Cmd) (Output, error) {
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	r.mu.Lock()
	r.calls = append(r.calls, slices.Clone(c.Argv))
	onRun := r.onRun
	r.mu.Unlock()
	if onRun != nil {
		onRun(ctx, c.Argv)
		if err := ctx.Err(); err != nil {
			return Output{}, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cmd := strings.Join(c.Argv, " ")
	for prefix, code := range r.fail {
		if strings.HasPrefix(cmd, prefix) {
			if c.Stdout != nil {
				_, _ = io.WriteString(c.Stdout, r.said[prefix])
			}
			return Output{ExitCode: code, Text: r.said[prefix]}, nil
		}
	}
	say := func(text string) (Output, error) {
		if c.Stdout != nil {
			_, _ = io.WriteString(c.Stdout, text)
			return Output{}, nil
		}
		return Output{Text: text}, nil
	}
	switch {
	case len(c.Argv) >= 5 && c.Argv[0] == "systemctl" && c.Argv[1] == "show":
		if c.Argv[2] == r.busyUnit {
			return say("active\n")
		}
		return say("inactive\n")
	case len(c.Argv) >= 4 && c.Argv[0] == "systemctl" && c.Argv[1] == "enable" && c.Argv[2] == "--now":
		r.enabled[c.Argv[3]] = true
		return say("")
	case len(c.Argv) >= 4 && c.Argv[0] == "systemctl" && c.Argv[1] == "disable" && c.Argv[2] == "--now":
		delete(r.enabled, c.Argv[3])
		return say("")
	case len(c.Argv) == 3 && c.Argv[0] == "systemctl" && c.Argv[1] == "is-active":
		if r.enabled[c.Argv[2]] {
			return say("active\n")
		}
		return Output{Text: "inactive\n", ExitCode: 3}, nil
	case len(c.Argv) == 3 && c.Argv[0] == "systemctl" && c.Argv[1] == "is-enabled":
		if r.enabled[c.Argv[2]] {
			return say("enabled\n")
		}
		return Output{Text: "disabled\n", ExitCode: 1}, nil
	}
	if text, ok := r.text[cmd]; ok {
		return say(text)
	}
	return say("")
}

func (r *fakeRunner) called(prefix ...string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if len(c) >= len(prefix) && slices.Equal(c[:len(prefix)], prefix) {
			n++
		}
	}
	return n
}

func (r *fakeRunner) isEnabled(unit string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enabled[unit]
}

// fakeDownloader serves a body, or fails.
type fakeDownloader struct {
	body  []byte
	err   error
	calls int
	urls  []string
	// failFirst is how many of the first tries fail with err
	failFirst int
}

func (d *fakeDownloader) Get(ctx context.Context, url string, dst io.Writer) (int64, error) {
	d.calls++
	d.urls = append(d.urls, url)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if d.err != nil && (d.failFirst == 0 || d.calls <= d.failFirst) {
		return 0, d.err
	}
	n, err := dst.Write(d.body)
	return int64(n), err
}

type fakeGateway struct {
	answerAfter int // the number of the call that is the first to answer; 0 is the first
	never       bool
	calls       int
	routes      []string
}

func (g *fakeGateway) Answers(_ context.Context, route string) (bool, error) {
	g.calls++
	g.routes = append(g.routes, route)
	return !g.never && g.calls > g.answerAfter, nil
}

type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

// fakeSandbox is the sandbox of the simulation, with what apt says of it.
type fakeSandbox struct {
	dir      string
	sim, est osplan.Output
	err      error
	argvs    [][]string
	removed  int
	written  []string
	content  map[string]string // what each file written says
}

const (
	aptProgress = "Reading package lists...\nBuilding dependency tree...\nReading state information...\n"
	aptSim      = aptProgress + "The following additional packages will be installed:\n  libfdt1 libvirt0\nThe following NEW packages will be installed:\n  libfdt1 libvirt-clients libvirt0 qemu-utils\n" +
		"0 upgraded, 4 newly installed, 0 to remove and 0 not upgraded.\nInst libfdt1 (1.6.1-4+b1 Debian:12.15/oldstable [amd64])\nInst libvirt0 (9.0.0-4 Debian:12.15/oldstable [amd64])\n" +
		"Inst libvirt-clients (9.0.0-4 Debian:12.15/oldstable [amd64])\nInst qemu-utils (1:7.2+dfsg-7+deb12u16 Debian:12.15/oldstable [amd64])\nConf libfdt1 (1.6.1-4+b1 Debian:12.15/oldstable [amd64])\n"
	aptEst = aptProgress + "The following additional packages will be installed:\n  libfdt1 libvirt0\nThe following NEW packages will be installed:\n  libfdt1 libvirt-clients libvirt0 qemu-utils\n" +
		"0 upgraded, 4 newly installed, 0 to remove and 0 not upgraded.\nNeed to get 44.3 MB of archives.\nAfter this operation, 130 MB of additional disk space will be used.\n" +
		"'http://deb.debian.org/debian/pool/main/libf/libfdt/libfdt1_1.6.1-4+b1_amd64.deb' libfdt1_1.6.1-4+b1_amd64.deb 21520 SHA256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n"
	aptHeld = aptProgress + "The following held packages will be changed:\n  qemu-utils\nThe following NEW packages will be installed:\n  libvirt-clients qemu-utils\n" +
		"0 upgraded, 2 newly installed, 0 to remove and 0 not upgraded.\nE: Held packages were changed and -y was used without --allow-change-held-packages.\n"
)

func newFakeSandbox() *fakeSandbox {
	return &fakeSandbox{dir: "/var/tmp/casaos-os-plan.fake1", sim: osplan.Output{Text: aptSim}, est: osplan.Output{Text: aptEst}}
}

func (s *fakeSandbox) MakeDir() (string, func(), error) {
	return s.dir, func() { s.removed++ }, nil
}
func (s *fakeSandbox) WriteFile(p string, content []byte, _ fs.FileMode) error {
	if !osplan.InsideSandbox(s.dir, p) {
		return errors.New("outside the sandbox")
	}
	s.written = append(s.written, p)
	if s.content == nil {
		s.content = map[string]string{}
	}
	s.content[p] = string(content)
	return nil
}
func (s *fakeSandbox) Run(_ context.Context, argv []string) (osplan.Output, error) {
	if !osplan.AllowedModuleArgv(argv, s.dir) {
		return osplan.Output{}, osreadiness.ErrNotAllowed
	}
	s.argvs = append(s.argvs, slices.Clone(argv))
	if s.err != nil {
		return osplan.Output{}, s.err
	}
	if slices.Contains(argv, "-s") {
		return s.sim, nil
	}
	return s.est, nil
}
