// Package osverify says whether a machine is as it was after an operating system upgrade, and what is not. It takes a
// handful of facts of the machine (Take), once before the upgrade and once after, and compares them with each other and with
// what the blocks of the upgrade wrote down (Judge). It reads and decides, and does nothing else: it restarts no unit,
// installs no package, starts no container, and writes nothing.
//
// What it reads, it reads through the readiness report's Probe, and the commands it runs are the ones on the readiness
// allow-list (osreadiness.Allowed), never a runner of its own. A fact that cannot be taken (a tool that is not installed,
// a command that fails, a file that cannot be read, the time that ran out) is not a fact that is false: it is listed as
// unknown (Facts.Unknown), and nothing is concluded from it.
package osverify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The facts that can be unknown (Facts.Unknown names them).
const (
	FactKernel     = "kernel"
	FactSystem     = "system"
	FactDpkg       = "dpkg"
	FactSamba      = "samba"
	FactMounts     = "mounts"
	FactRoute      = "route"
	FactDNS        = "dns"
	FactUnits      = "units"
	FactDocker     = "docker"
	FactContainers = "containers"
	FactSources    = "sources"
)

var allFacts = []string{FactKernel, FactSystem, FactDpkg, FactSamba, FactMounts, FactRoute, FactDNS, FactUnits, FactDocker, FactContainers, FactSources}

// The state of Docker's daemon (Docker.State): up when it answered; down when its service runs and it did not; stopped when
// Docker is on the box and its service does not run (the daemon is not asked then, since asking it would start it, and with
// it the containers that have a restart policy); absent when there is no Docker. It is empty when it could not be said.
const (
	DockerUp      = "up"
	DockerDown    = "down"
	DockerStopped = "stopped"
	DockerAbsent  = "absent"
)

// unitUnknown is the state of a unit that systemd was asked about and did not answer with a state.
const unitUnknown = "unknown"

// Facts is what is known of a machine at one moment. The zero value says that nothing was taken (Taken is false): a run
// whose facts from before are lost is judged by what it still knows. A fact that could not be taken is named in Unknown,
// and its field is then meaningless.
type Facts struct {
	Taken   bool      `json:"taken"`
	At      time.Time `json:"at"`
	Unknown []string  `json:"unknown,omitempty"`

	// Kernel is the release the machine runs now (uname -r), System what systemd says of the state of the system
	// (running, degraded, starting...), and DpkgQuiet whether dpkg --audit has nothing to say.
	Kernel    string `json:"kernel,omitempty"`
	System    string `json:"system,omitempty"`
	DpkgQuiet bool   `json:"dpkg_quiet"`
	// SambaUsers is how many users Samba's database holds (pdbedit -L).
	SambaUsers int `json:"samba_users"`
	// Mergerfs are the mount points of the mergerfs pools that are mounted, sorted.
	Mergerfs []string `json:"mergerfs,omitempty"`
	// Route is the interface the default route of IPv4 goes out by, "" when there is none.
	Route string `json:"route,omitempty"`
	// DNS says whether the resolver answered for a name that a Debian box reaches every day.
	DNS bool `json:"dns"`
	// Units are the units of ReCasaOS that have a file on the box, with the state systemd gives them.
	Units map[string]string `json:"units,omitempty"`
	// Paused are the sources of apt that the upgrade paused and that are still paused: files that have been renamed
	// (docker.list.casaos-paused) and the lines of a file that have been commented out (sources.list:12).
	Paused []string `json:"paused,omitempty"`
	Docker Docker   `json:"docker"`
}

// Docker is what is known of Docker's daemon.
type Docker struct {
	State string `json:"state,omitempty"`
	// Version and EngineID are the daemon's own: the id is the one it keeps in its data directory, and it changes when
	// that directory is made again. Both are empty unless the daemon answered (and the id, when its file cannot be read).
	Version  string `json:"version,omitempty"`
	EngineID string `json:"engine_id,omitempty"`
	// Containers are the containers that run, sorted by name.
	Containers []Container `json:"containers,omitempty"`
}

// Container is a container that runs, and what of it matters after a restart.
type Container struct {
	Name string `json:"name"`
	// Policy is its restart policy: "no" or "" (it does not come back by itself), "always", "unless-stopped", "on-failure".
	Policy string `json:"policy,omitempty"`
	// Binds are the directories of the machine that it has mounted into itself, so that what it writes there is the
	// machine's, and not the container's.
	Binds []string `json:"binds,omitempty"`
}

// Knows says whether a fact was taken: the facts were taken at all, and this one was not left unknown.
func (f Facts) Knows(fact string) bool { return f.Taken && !slices.Contains(f.Unknown, fact) }

// Resolver says whether a name resolves.
type Resolver func(ctx context.Context, name string) bool

// dnsName is the name that the resolver is asked for: the one that apt itself asks for, on the boxes the upgrade is for.
const dnsName = "deb.debian.org"

// dnsTimeout is how long the resolver has to answer; a variable so that a test need not wait for it.
var dnsTimeout = 5 * time.Second

// resolveHost is Go's own resolver.
func resolveHost(ctx context.Context, name string) bool {
	addrs, err := net.DefaultResolver.LookupHost(ctx, name)
	return err == nil && len(addrs) > 0
}

// Take takes the facts of the machine now. It is the one function that is used before the upgrade and after it, so that
// what is compared was taken in the same way. Everything it asks the machine goes through the readiness allow-list, and
// it asks within the time of ctx: what the time did not leave is unknown.
func Take(ctx context.Context, p osreadiness.Probe) Facts { return TakeWith(ctx, p, resolveHost) }

// TakeWith is Take with another resolver, which the tests of what is built on it need so as not to ask the network.
func TakeWith(ctx context.Context, p osreadiness.Probe, resolve Resolver) Facts {
	if resolve == nil {
		resolve = resolveHost
	}
	t := &taker{ctx: ctx, p: p, f: Facts{Taken: true, At: p.Now().UTC().Truncate(time.Second)}}
	t.kernel()
	t.system()
	t.dpkg()
	t.samba()
	t.mounts()
	t.route()
	t.dns(resolve)
	t.units()
	t.docker()
	t.sources()
	return t.f
}

type taker struct {
	ctx context.Context
	p   osreadiness.Probe
	f   Facts
}

func (t *taker) miss(facts ...string) {
	for _, fact := range facts {
		if !slices.Contains(t.f.Unknown, fact) {
			t.f.Unknown = append(t.f.Unknown, fact)
		}
	}
}

// ask runs a command that the readiness allow-list allows, and no other.
func (t *taker) ask(argv ...string) (osreadiness.Output, error) {
	if len(argv) == 0 || !osreadiness.Allowed(argv) {
		name := ""
		if len(argv) > 0 {
			name = argv[0]
		}
		return osreadiness.Output{}, fmt.Errorf("%w: %q", osreadiness.ErrNotAllowed, name)
	}
	if err := t.ctx.Err(); err != nil {
		return osreadiness.Output{}, err
	}
	return t.p.Run(t.ctx, argv...)
}

func (t *taker) read(path string) (string, error) {
	if err := t.ctx.Err(); err != nil {
		return "", err
	}
	data, err := t.p.ReadFile(t.ctx, path)
	return string(data), err
}

func (t *taker) dir(path string) ([]string, error) {
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	return t.p.ReadDir(t.ctx, path)
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// answered says whether a command ran, ended well and printed all it had to say.
func answered(out osreadiness.Output, err error) bool {
	return err == nil && out.ExitCode == 0 && !out.Truncated
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return strings.TrimSpace(line)
}

var kernelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~:-]{0,63}$`)

func (t *taker) kernel() {
	out, err := t.ask("uname", "-r")
	if !answered(out, err) {
		t.miss(FactKernel)
		return
	}
	if kernel := firstLine(out.Text); kernelPattern.MatchString(kernel) {
		t.f.Kernel = kernel
		return
	}
	t.miss(FactKernel)
}

// systemStates are the words of systemctl is-system-running. "unknown" is systemd saying that it could not tell, which is
// no answer.
var systemStates = []string{"running", "degraded", "starting", "initializing", "maintenance", "stopping", "offline"}

func (t *taker) system() {
	// the status of the command is not the answer: it is not 0 for any state but running
	out, err := t.ask("systemctl", "is-system-running")
	if err == nil {
		if state := firstLine(out.Text); slices.Contains(systemStates, state) {
			t.f.System = state
			return
		}
	}
	t.miss(FactSystem)
}

func (t *taker) dpkg() {
	out, err := t.ask("dpkg", "--audit")
	if err != nil {
		t.miss(FactDpkg)
		return
	}
	t.f.DpkgQuiet = out.ExitCode == 0 && !out.Truncated && strings.TrimSpace(out.Text) == ""
}

// sambaLine is a line of pdbedit -L: the name of a user, the number of the user, the full name. Anything else it prints
// (a warning of Samba's about its configuration, say) is not a user.
var sambaLine = regexp.MustCompile(`^[^:]+:[0-9]+:`)

func (t *taker) samba() {
	out, err := t.ask("pdbedit", "-L")
	if !answered(out, err) {
		t.miss(FactSamba)
		return
	}
	for _, line := range strings.Split(out.Text, "\n") {
		if sambaLine.MatchString(line) {
			t.f.SambaUsers++
		}
	}
}

// printable says whether a path that comes from the box can be shown: text of a bounded length, with no control character.
func printable(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' })
}

// unescapeMount undoes the octal escapes that /proc/mounts makes of a space, a tab, a newline and a backslash.
func unescapeMount(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				out.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

// maxMountPath is the longest path of a mount point that is kept.
const maxMountPath = 255

// mergerfsMounts are the mount points of /proc/mounts whose file system is mergerfs's, sorted and with no repeat.
func mergerfsMounts(data string) []string {
	var mounts []string
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[2] != "fuse.mergerfs" {
			continue
		}
		if path := unescapeMount(fields[1]); printable(path, maxMountPath) && !slices.Contains(mounts, path) {
			mounts = append(mounts, path)
		}
	}
	slices.Sort(mounts)
	return mounts
}

func (t *taker) mounts() {
	data, err := t.read("/proc/mounts")
	if err != nil {
		t.miss(FactMounts)
		return
	}
	t.f.Mergerfs = mergerfsMounts(data)
}

var interfacePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,15}$`)

// routeOther is the interface of a default route that goes out by something that is not an interface's name.
const routeOther = "other"

// defaultRoute is the interface that the IPv4 default route of /proc/net/route goes out by, and "" when there is none. A
// route is the default when its destination and its mask are zero, and it counts when it is up (the flag 1).
func defaultRoute(data string) string {
	for _, line := range strings.Split(data, "\n") {
		// Iface Destination Gateway Flags RefCnt Use Metric Mask ...
		fields := strings.Fields(line)
		if len(fields) < 8 || fields[1] != "00000000" || fields[7] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil || flags&1 == 0 {
			continue
		}
		if interfacePattern.MatchString(fields[0]) {
			return fields[0]
		}
		return routeOther
	}
	return ""
}

func (t *taker) route() {
	data, err := t.read("/proc/net/route")
	if err != nil {
		t.miss(FactRoute)
		return
	}
	t.f.Route = defaultRoute(data)
}

func (t *taker) dns(resolve Resolver) {
	if t.ctx.Err() != nil {
		t.miss(FactDNS)
		return
	}
	ctx, cancel := context.WithTimeout(t.ctx, dnsTimeout)
	defer cancel()
	ok := resolve(ctx, dnsName)
	// a resolver that was cut short by the end of the time of the facts is not one that said no
	if t.ctx.Err() != nil {
		t.miss(FactDNS)
		return
	}
	t.f.DNS = ok
}

// unitDirs are where the units of the box's packages are (the last is the first's own, on a box whose /lib is /usr/lib).
var unitDirs = []string{"/etc/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system"}

var casaosUnit = regexp.MustCompile(`^casaos[A-Za-z0-9@._:-]{0,100}\.(service|socket|timer|path)$`)

// transientUnits are the units that the core starts for one job and that are gone when it ends: they have no file, and one
// that was running when the state was written down is no unit that was lost.
var transientUnits = []string{"casaos-os-upgrade.service", "casaos-update.service", "casaos-package-update.service", "casaos-docker-update.service"}

var unitStates = []string{"active", "reloading", "inactive", "failed", "activating", "deactivating", "maintenance"}

// activeState is the state systemd gives a unit; ok is false when it did not say one.
func (t *taker) activeState(unit string) (state string, ok bool) {
	out, err := t.ask("systemctl", "show", unit, "--property=ActiveState", "--value")
	if !answered(out, err) {
		return "", false
	}
	state = firstLine(out.Text)
	return state, slices.Contains(unitStates, state)
}

func (t *taker) units() {
	names := map[string]bool{}
	for _, dir := range unitDirs {
		entries, err := t.dir(dir)
		if isNotExist(err) {
			continue
		}
		if err != nil {
			t.miss(FactUnits)
			return
		}
		for _, entry := range entries {
			if casaosUnit.MatchString(entry) && !slices.Contains(transientUnits, entry) {
				names[entry] = true
			}
		}
	}
	states := map[string]string{}
	for name := range names {
		state, ok := t.activeState(name)
		if !ok {
			state = unitUnknown
		}
		states[name] = state
	}
	if t.ctx.Err() != nil {
		t.miss(FactUnits)
		return
	}
	if len(states) > 0 {
		t.f.Units = states
	}
}

// The bounds of what is kept of Docker: a box has fewer containers than this, and a container fewer binds.
const (
	maxContainers = 1000
	maxInspect    = 200
	maxBinds      = 32
	maxBindPath   = 256
)

var (
	containerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	restartPolicyPattern = regexp.MustCompile(`^[a-z-]{0,32}$`)
	containerIDPattern   = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
	engineIDPattern      = regexp.MustCompile(`^[A-Za-z0-9:-]{8,64}$`)
)

// dockerThere says whether there is a Docker on the box, from its client or its socket and without asking a daemon, so
// without starting one.
func (t *taker) dockerThere() (bool, error) {
	_, err := t.ask("docker", "--version")
	switch {
	case err == nil:
		return true, nil
	case !errors.Is(err, osreadiness.ErrToolMissing):
		return false, err
	}
	if err := t.ctx.Err(); err != nil {
		return false, err
	}
	_, err = t.p.Stat(t.ctx, "/run/docker.sock")
	switch {
	case err == nil:
		return true, nil
	case isNotExist(err):
		return false, nil
	}
	return false, err
}

func (t *taker) docker() {
	state, ok := t.activeState("docker.service")
	if !ok {
		t.miss(FactDocker, FactContainers)
		return
	}
	switch state {
	case "active", "activating", "reloading":
		// asked below: asking a daemon that is not running starts it
	case "inactive", "failed", "deactivating", "maintenance":
		there, err := t.dockerThere()
		if err != nil {
			t.miss(FactDocker, FactContainers)
			return
		}
		t.f.Docker.State = DockerAbsent
		if there {
			t.f.Docker.State = DockerStopped
		}
		return
	}
	out, err := t.ask("docker", "info", "--format", dockerpkg.DaemonInfoFormat)
	if err != nil {
		t.miss(FactDocker, FactContainers)
		return
	}
	info, parseErr := dockerpkg.ParseDaemonInfo(out.Text)
	if out.ExitCode != 0 || out.Truncated || parseErr != nil {
		t.f.Docker.State = DockerDown
		t.miss(FactContainers)
		return
	}
	t.f.Docker.State, t.f.Docker.Version = DockerUp, info.ServerVersion
	// the engine's id is a file of its data directory; a daemon that keeps its data elsewhere has none to read here
	if id, err := t.read("/var/lib/docker/engine-id"); err == nil {
		if id = firstLine(id); engineIDPattern.MatchString(id) {
			t.f.Docker.EngineID = id
		}
	}
	t.containers()
}

func (t *taker) containers() {
	out, err := t.ask("docker", "ps", "-q")
	if !answered(out, err) {
		t.miss(FactContainers)
		return
	}
	var ids []string
	for _, line := range strings.Split(out.Text, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if !containerIDPattern.MatchString(line) {
			t.miss(FactContainers)
			return
		}
		ids = append(ids, line)
	}
	if len(ids) > maxContainers {
		t.miss(FactContainers)
		return
	}
	var running []Container
	for chunk := range slices.Chunk(ids, maxInspect) {
		argv := append([]string{"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat}, chunk...)
		out, err := t.ask(argv...)
		if !answered(out, err) {
			t.miss(FactContainers)
			return
		}
		running = append(running, parseInspected(out.Text)...)
	}
	slices.SortFunc(running, func(a, b Container) int { return strings.Compare(a.Name, b.Name) })
	t.f.Docker.Containers = running
}

// inspected is a line of docker inspect --format dockerpkg.ContainerInspectFormat, as far as the judge reads it.
type inspected struct {
	Name    string `json:"name"`
	Restart string `json:"restart"`
	Mounts  []struct {
		Type   string `json:"Type"`
		Source string `json:"Source"`
	} `json:"mounts"`
}

// parseInspected reads the lines of docker inspect that are what it prints, and leaves the others: a container is kept if
// its name follows Docker's rule and its restart policy is the name of one, and a bind is kept if it is a path that can
// be shown.
func parseInspected(text string) []Container {
	var containers []Container
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw inspected
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		name := strings.TrimPrefix(raw.Name, "/")
		if !containerNamePattern.MatchString(name) || !restartPolicyPattern.MatchString(raw.Restart) {
			continue
		}
		c := Container{Name: name, Policy: raw.Restart}
		for _, mount := range raw.Mounts {
			if mount.Type == "bind" && printable(mount.Source, maxBindPath) && len(c.Binds) < maxBinds {
				c.Binds = append(c.Binds, mount.Source)
			}
		}
		containers = append(containers, c)
	}
	slices.SortFunc(containers, func(a, b Container) int { return strings.Compare(a.Name, b.Name) })
	return containers
}

// pausedMark is what the upgrade writes before a line of a file of sources that it comments out, and pausedSuffix what it
// adds to the name of a file of sources that it sets aside.
const (
	pausedMark   = "# casaos-paused: "
	pausedSuffix = ".casaos-paused"
	sourcesDir   = "/etc/apt/sources.list.d"
	maxPaused    = 200
)

var sourceFileName = regexp.MustCompile(`^[A-Za-z0-9_:.-]{1,200}$`)

func (t *taker) sources() {
	names, err := t.dir(sourcesDir)
	if err != nil && !isNotExist(err) {
		t.miss(FactSources)
		return
	}
	var paused []string
	files := map[string]string{"sources.list": "/etc/apt/sources.list"}
	for _, name := range names {
		switch {
		case !sourceFileName.MatchString(name):
		case strings.HasSuffix(name, pausedSuffix):
			paused = append(paused, name)
		case strings.HasSuffix(name, ".list") && len(files) < maxPaused:
			files[name] = sourcesDir + "/" + name
		}
	}
	for name, path := range files {
		data, err := t.read(path)
		if isNotExist(err) {
			continue
		}
		if err != nil {
			t.miss(FactSources)
			return
		}
		for i, line := range strings.Split(data, "\n") {
			if strings.HasPrefix(line, pausedMark) {
				paused = append(paused, name+":"+strconv.Itoa(i+1))
			}
		}
	}
	sort.Strings(paused)
	if len(paused) > maxPaused {
		paused = paused[:maxPaused]
	}
	t.f.Paused = paused
}
