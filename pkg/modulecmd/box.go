package modulecmd

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/pkg/osplan"
	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// refusingDest is the file system of a host that cannot unpack: every operation says so, and nothing is written.
type refusingDest struct{}

func (refusingDest) Lstat(string) (fs.FileMode, error) { return 0, ErrUnsupported }
func (refusingDest) Mkdir(string, fs.FileMode) error   { return ErrUnsupported }
func (refusingDest) NewFile(string, fs.FileMode, bool) (osmodule.File, error) {
	return nil, ErrUnsupported
}
func (refusingDest) Symlink(string, string, bool) error { return ErrUnsupported }

// casaosUnits are the transient units that change the packages of the box or its ReCasaOS release; the units of the modules are not among them, since an install runs in one.
var casaosUnits = []string{"casaos-update.service", "casaos-package-update.service", "casaos-docker-update.service", "casaos-os-upgrade.service"}

// maintenanceUnits are all the units whose being active says that a package manager is at work: those, and the two that the readiness report of the upgrade of the operating system asks
// about (the timers of apt that update and upgrade by themselves).
func maintenanceUnits() []string {
	return append(slices.Clone(casaosUnits), osreadiness.BusyUnits()...)
}

// readRegular reads a file that is a regular file and not a link, at most max bytes (more is an error).
func readRegular(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, errTooLarge
	}
	return raw, nil
}

var errTooLarge = errors.New("too large")

// A box is the machine as the plan asks about it (osmodule.Box): every method reads and none writes. ctx ends the commands it asks.
type box struct {
	ctx context.Context
	env *Env
}

var _ osmodule.Box = (*box)(nil)

var osReleaseValueRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// osBases is the table a derivative is read from: the releases of Debian and Ubuntu that the manifests name today, by the codename that os-release gives of the release it is built on
// (UBUNTU_CODENAME, DEBIAN_CODENAME). It is the one place the core maps a codename to a release; a codename that is not in it is a box that is not read as any release, and the version
// of the derivative itself (Mint's 22.2) is never taken for one.
var osBases = map[string]struct {
	key       string
	codenames map[string]string
}{
	"ubuntu": {"UBUNTU_CODENAME", map[string]string{"jammy": "22.04", "noble": "24.04", "resolute": "26.04"}},
	"debian": {"DEBIAN_CODENAME", map[string]string{"bookworm": "12", "trixie": "13"}},
}

// parseOSRelease is the ID and the VERSION_ID of an os-release file; empty for one that is missing or whose value is not a plain word. A box whose ID is neither debian nor ubuntu is read
// as the release it is built on when the nearer of the two that ID_LIKE names (ID_LIKE lists the closest first) gives a codename of osBases, and is itself otherwise.
func parseOSRelease(raw []byte) osmodule.OSRelease {
	var id, version, like string
	codenames := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		switch key {
		case "ID":
			id = value
		case "VERSION_ID":
			version = value
		case "ID_LIKE":
			like = value
		case "UBUNTU_CODENAME", "DEBIAN_CODENAME":
			codenames[key] = value
		}
	}
	if !osReleaseValueRe.MatchString(id) || !osReleaseValueRe.MatchString(version) {
		return osmodule.OSRelease{}
	}
	if _, base := osBases[id]; base {
		return osmodule.OSRelease{ID: id, Version: version}
	}
	for _, name := range strings.Fields(like) {
		base, ok := osBases[name]
		if !ok {
			continue
		}
		codename := codenames[base.key]
		if v, known := base.codenames[codename]; known {
			return osmodule.OSRelease{ID: name, Version: v, From: id + ":" + version + ", " + base.key + "=" + codename}
		}
		break
	}
	return osmodule.OSRelease{ID: id, Version: version}
}

// OS is the system of the file os-release(5) names (parseOSRelease): /etc/os-release, which on Debian and Ubuntu is a link to ../usr/lib/os-release, and /usr/lib/os-release when there is
// none in /etc (a link that leads nowhere is none). A file that is there and is not a regular file, or is too large, or does not read, is not looked past: it is a box that cannot be told.
func (b *box) OS() osmodule.OSRelease {
	for _, p := range []string{pathOSRelease, pathOSReleaseLib} {
		raw, err := readFollowing(b.env.Roots.Path(p), 64<<10)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return osmodule.OSRelease{}
		}
		return parseOSRelease(raw)
	}
	return osmodule.OSRelease{}
}

// readFollowing reads a file that is a regular file once its links are followed, at most max bytes (more is an error). It is for the files the system itself keeps as links.
func readFollowing(path string, max int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, errTooLarge
	}
	return raw, nil
}

func (b *box) Arch() string            { return b.env.Arch }
func (b *box) ReCasaOSVersion() string { return b.env.Version }

func (b *box) InstalledManifests() (map[string]osmodule.Manifest, []string, error) {
	installed := map[string]osmodule.Manifest{}
	dir := b.env.Roots.manifests()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return installed, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var broken []string
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		raw, err := readRegular(filepath.Join(dir, e.Name()), osmodule.MaxBytes)
		if err == nil {
			var m osmodule.Manifest
			if m, err = osmodule.Parse(e.Name(), raw); err == nil {
				installed[name] = m
				continue
			}
		}
		broken = append(broken, name)
	}
	return installed, broken, nil
}

func (b *box) FilesLists() (map[string][]string, error) {
	lists := map[string][]string{}
	dir := b.env.Roots.state()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return lists, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".files")
		if !ok || !nameRe.MatchString(name) {
			continue
		}
		list, err := readFilesList(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		lists[name] = list
	}
	return lists, nil
}

// readFilesList reads the list of the files of a module: nil, nil when there is none. A list that is there and has nothing in it is not a list of no files but a list that was cut short.
func readFilesList(path string) ([]string, error) { return readList(path, osmodule.ParseFiles) }

// readDirsList reads the list of the directories that the install of a module made, in the same way.
func readDirsList(path string) ([]string, error) { return readList(path, osmodule.ParseDirs) }

// readFilesEntries reads the list of the files of a module with what each is to hold: an empty list, with no entries, when there is none.
func readFilesEntries(path string) (osmodule.FilesList, error) {
	raw, err := readRegular(path, int64(osmodule.MaxFilesBytes))
	if errors.Is(err, fs.ErrNotExist) {
		return osmodule.FilesList{}, nil
	}
	if err != nil {
		return osmodule.FilesList{}, err
	}
	return osmodule.ParseFilesList(raw)
}

func readList(path string, parse func([]byte) ([]string, error)) ([]string, error) {
	raw, err := readRegular(path, int64(osmodule.MaxFilesBytes))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parse(raw)
}

// unitKey is a way to tell that two spellings of the path of a unit are one file: the directory with its links followed (/lib is /usr/lib on a system with a merged /usr), and the name.
func unitKey(real string) string {
	dir, err := filepath.EvalSymlinks(filepath.Dir(real))
	if err != nil {
		dir = filepath.Dir(real)
	}
	return filepath.Join(dir, filepath.Base(real))
}

func (b *box) UnitFiles(unit string) ([]string, error) {
	lists, err := b.FilesLists()
	if err != nil {
		return nil, err
	}
	// how the lists spell the units they own: a unit that is there under another spelling of the same directory is the module's all the same
	spelled := map[string]string{}
	for _, name := range sortedKeys(lists) {
		for _, p := range lists[name] {
			if _, ok := spelled[unitKey(b.env.Roots.Path(p))]; !ok {
				spelled[unitKey(b.env.Roots.Path(p))] = p
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, dir := range unitDirs {
		p := dir + "/" + unit
		real := b.env.Roots.Path(p)
		if _, err := os.Lstat(real); err != nil {
			if errors.Is(err, fs.ErrNotExist) || isNotDir(err) {
				continue
			}
			return nil, err
		}
		key := unitKey(real)
		if seen[key] {
			continue
		}
		seen[key] = true
		if s, ok := spelled[key]; ok {
			p = s
		}
		out = append(out, p)
	}
	return out, nil
}

// MergedUsr says whether /lib is a link on this box, as the unpack looks at it (checkDestination of osmodule): only then is "/lib/x" the file "/usr/lib/x". A /lib that is not there, or
// is something else than a link, is not; one that cannot be looked at is an error.
func (b *box) MergedUsr() (bool, error) {
	info, err := os.Lstat(b.env.Roots.Path("/lib"))
	switch {
	case err == nil:
		return info.Mode()&fs.ModeSymlink != 0, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, err
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (b *box) PackageManagerBusy() (bool, error) {
	for _, unit := range maintenanceUnits() {
		out, err := b.env.Runner.Run(b.ctx, Cmd{Argv: []string{"systemctl", "show", unit, "--property=ActiveState", "--value"}, Timeout: 10 * time.Second})
		if err != nil || out.ExitCode != 0 {
			continue // (systemd that does not answer is not a package manager at work)
		}
		switch strings.TrimSpace(out.Text) {
		case "active", "activating", "deactivating":
			return true, nil
		}
	}
	locks := make([]string, len(dpkgLocks))
	for i, p := range dpkgLocks {
		locks[i] = b.env.Roots.Path(p)
	}
	held, err := b.env.Host.DpkgLockHeld(locks)
	if err != nil && !held && errors.Is(err, fs.ErrPermission) && b.env.EUID() != 0 {
		// the locks are root's (0640): a user who may not open them does not know, and root, who does, is asked again by the install
		return false, osmodule.ErrNeedsRoot
	}
	return held, err
}

// OSUpgradeBusy reads the flag of an open upgrade of the operating system as the core does: only a flag that is not there is no flag.
func (b *box) OSUpgradeBusy() (bool, error) {
	_, err := os.Lstat(b.env.Roots.Path(pathOSBusy))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return true, err
}

// DpkgAudit asks dpkg --audit, which reads and writes nothing.
func (b *box) DpkgAudit() ([]string, error) {
	names, err := dpkgAudit(b.ctx, *b.env)
	if errors.Is(err, osreadiness.ErrToolMissing) {
		return nil, osmodule.ErrDpkgMissing
	}
	return names, err
}

func (b *box) FreeBytes(path string) (int64, error) {
	return b.env.Host.FreeBytes(b.env.Roots.Path(path))
}

func (b *box) AptGetExists() bool {
	for _, dir := range b.env.Roots.searchDirs() {
		if info, err := os.Stat(filepath.Join(dir, "apt-get")); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// ArchiveBytes: the size of an archive is not asked of the network; the plan takes the most there is.
func (b *box) ArchiveBytes(string) (int64, bool) { return 0, false }

// SimulateApt asks apt what installing the packages would do, in a sandbox of the same kind as the plan of the upgrade's: its allow-list, its environment and its parser.
func (b *box) SimulateApt(packages []string) osmodule.AptResult { return b.simulate(packages, false) }

// SimulateUpdate is the simulation of the command an update runs, which upgrades nothing that is installed.
func (b *box) SimulateUpdate(packages []string) osmodule.AptResult { return b.simulate(packages, true) }

func (b *box) simulate(packages []string, noUpgrade bool) osmodule.AptResult {
	failed := func(detail string) osmodule.AptResult {
		return osmodule.AptResult{Failure: osmodule.RefusedAptFailed, Detail: detail}
	}
	if b.env.Sandbox == nil {
		return failed("no sandbox")
	}
	sb := b.env.Sandbox()
	dir, remove, err := sb.MakeDir()
	if err != nil {
		return failed("sandbox: " + err.Error())
	}
	defer remove()
	cfg, err := osplan.ReadAptConfig(b.ctx, aptProbe{roots: b.env.Roots})
	if err != nil {
		return failed("the configuration of apt cannot be read")
	}
	for _, f := range osplan.SandboxFiles(dir, nil, cfg) {
		if err := sb.WriteFile(f.Path, f.Content, f.Mode); err != nil {
			return failed("sandbox: " + err.Error())
		}
	}
	// the list of the automatically installed packages that apt rewrites when it is asked to install one of them: its own, empty, so that the box's is never opened for writing
	if err := sb.WriteFile(osplan.ModuleStatesFile(dir), nil, 0o600); err != nil {
		return failed("sandbox: " + err.Error())
	}
	simFor, estFor := osplan.ModuleSimArgv, osplan.ModuleEstimateArgv
	if noUpgrade {
		simFor, estFor = osplan.ModuleSimArgvNoUpgrade, osplan.ModuleEstimateArgvNoUpgrade
	}
	simArgv, err := simFor(dir, packages)
	if err != nil {
		return failed("the packages are not ones apt is asked about")
	}
	estArgv, _ := estFor(dir, packages)
	run := func(argv []string) (osplan.Output, error) {
		ctx, cancel := context.WithTimeout(b.ctx, osplan.SolverTimeout)
		defer cancel()
		return sb.Run(ctx, argv)
	}
	sim, err := run(simArgv)
	if err != nil {
		return failed("apt did not answer")
	}
	est, err := run(estArgv)
	if err != nil {
		return failed("apt did not answer")
	}
	res := osplan.ParseModuleSimulation(sim, est, packages)
	if res.Failure != osmodule.RefusedAptFailed || !strings.Contains(res.Detail, heldBroken) {
		return res
	}
	held, _ := b.heldPackages() // (what is held cannot be told: the failure stays what it is, with the lines that apt could not meet)
	return heldFromSolver(res, sim.Text, held)
}

// heldBroken is the end of what apt says when it cannot make the changes, whether a package is held or a dependency is not there at all.
const heldBroken = "you have held broken packages"

// heldFromSolver takes the failure of a simulation that apt could not solve for a held package when one is: apt says "you have held broken packages" for a dependency that is not
// installable as well, so the failure is a held package's only if a package that the box has held (held) is named in the block of unmet dependencies of the simulation (simText). A held
// package that a dependency needs changed is not one that apt says "will be changed": asked to install, apt says it cannot correct the problems because of the held ones.
func heldFromSolver(res osmodule.AptResult, simText string, held []string) osmodule.AptResult {
	if res.Failure != osmodule.RefusedAptFailed || !strings.Contains(res.Detail, heldBroken) {
		return res
	}
	if slices.ContainsFunc(osplan.UnmetNames(simText), func(name string) bool { return slices.Contains(held, name) }) {
		res.Failure = osmodule.RefusedHeld
	}
	return res
}

// maxDpkgStatusBytes is the most of the status file of dpkg that is read (the file of a box with thousands of packages has some megabytes).
const maxDpkgStatusBytes = 128 << 20

// heldPackages are the packages that dpkg holds (apt-mark hold): the stanzas of /var/lib/dpkg/status whose Status says hold. The file is read and nothing is asked of apt, which would
// build its cache and write it. A file that is missing, is not a regular file, is too large or has a line that is too long is an error: what is held is not known.
func (b *box) heldPackages() ([]string, error) { return b.heldPackagesWithin(maxDpkgStatusBytes) }

func (b *box) heldPackagesWithin(most int64) ([]string, error) {
	f, err := os.Open(b.env.Roots.Path(pathDpkgStatus))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > most {
		return nil, errors.New("the status file of dpkg is not a regular file or is too large")
	}
	var held []string
	var name string
	var isHeld bool
	end := func() {
		if isHeld && name != "" {
			held = append(held, name)
		}
		name, isHeld = "", false
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			end()
		case strings.HasPrefix(line, "Package: "):
			name = strings.TrimSpace(line[len("Package: "):])
		case strings.HasPrefix(line, "Status: "):
			want, _, _ := strings.Cut(strings.TrimSpace(line[len("Status: "):]), " ")
			isHeld = want == "hold"
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	end()
	return held, nil
}

var _ osmodule.UpdateBox = (*box)(nil)

// Installed is what the box has of the module called name for the plan of an update: its manifest, its recorded version, the state of its list of files and the size of the files that
// list records (the size of a backup of them). It reads, and writes nothing.
func (b *box) Installed(name string) osmodule.InstalledState {
	var st osmodule.InstalledState
	installed, broken, err := b.InstalledManifests()
	switch m, there := installed[name]; {
	case err != nil:
		st.Unreadable = true
		return st
	case there:
		st.Present, st.Manifest = true, m
	case slices.Contains(broken, name):
		st.Broken = true
		return st
	default:
		return st
	}
	if v := installedVersion(*b.env, name); v != nil {
		st.Tag = *v
	}
	list, err := readFilesEntries(b.env.Roots.files(name))
	switch {
	case err != nil:
		st.Files = osmodule.FilesUnreadable
	case len(list.Entries) == 0:
		st.Files = osmodule.FilesMissing
	case !list.Verified:
		st.Files = osmodule.FilesUnverified
	default:
		st.Files = osmodule.FilesVerified
		st.BytesOK = true
		for _, e := range list.Entries {
			if e.Link {
				continue
			}
			// (a file that is not there is not a file to back up; one that cannot be looked at is a size that cannot be told)
			info, err := os.Lstat(b.env.Roots.Path(e.Path))
			switch {
			case errors.Is(err, fs.ErrNotExist) || isNotDir(err):
			case err != nil:
				st.BytesOK = false
			case info.Mode().IsRegular():
				st.Bytes += info.Size()
			}
		}
	}
	return st
}
