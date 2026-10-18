package modulecmd

import (
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
)

// refusingDest is the file system of a host that cannot unpack: every operation says so, and nothing is written.
type refusingDest struct{}

func (refusingDest) Lstat(string) (fs.FileMode, error) { return 0, ErrUnsupported }
func (refusingDest) Mkdir(string, fs.FileMode) error   { return ErrUnsupported }
func (refusingDest) NewFile(string, fs.FileMode, bool) (osmodule.File, error) {
	return nil, ErrUnsupported
}
func (refusingDest) Symlink(string, string, bool) error { return ErrUnsupported }

// maintenanceUnits are the transient units that change the packages of the box or its ReCasaOS release; the units of the modules are not among them, since an install runs in one.
var maintenanceUnits = []string{"casaos-update.service", "casaos-package-update.service", "casaos-docker-update.service", "casaos-os-upgrade.service"}

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

// parseOSRelease is the ID and the VERSION_ID of an os-release file; empty for one that is missing or whose value is not a plain word.
func parseOSRelease(raw []byte) (id, version string) {
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
		}
	}
	if !osReleaseValueRe.MatchString(id) || !osReleaseValueRe.MatchString(version) {
		return "", ""
	}
	return id, version
}

func (b *box) OS() (string, string) {
	raw, err := readRegular(b.env.Roots.Path(pathOSRelease), 64<<10)
	if err != nil {
		return "", ""
	}
	return parseOSRelease(raw)
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

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (b *box) PackageManagerBusy() (bool, error) {
	for _, unit := range maintenanceUnits {
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
	return b.env.Host.DpkgLockHeld(locks)
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
func (b *box) SimulateApt(packages []string) osmodule.AptResult {
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
	simArgv, err := osplan.ModuleSimArgv(dir, packages)
	if err != nil {
		return failed("the packages are not ones apt is asked about")
	}
	estArgv, _ := osplan.ModuleEstimateArgv(dir, packages)
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
	return heldFromSolver(osplan.ParseModuleSimulation(sim, est, packages))
}

// heldFromSolver takes the failure of a simulation that apt could not solve because of a held package for what it is. A held package that a dependency needs changed is not one that apt
// says "will be changed": asked to install, apt says it cannot correct the problems because of the held ones.
func heldFromSolver(res osmodule.AptResult) osmodule.AptResult {
	if res.Failure == osmodule.RefusedAptFailed && strings.Contains(res.Detail, "you have held broken packages") {
		res.Failure = osmodule.RefusedHeld
	}
	return res
}
