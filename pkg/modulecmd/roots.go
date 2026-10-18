package modulecmd

import (
	"path/filepath"
	"strings"
)

// Every path the commands touch is one of these, absolute as the box spells it, and is reached through a Roots: production uses the real file system (an empty Prefix), a test points
// the Prefix at a temporary directory and nothing is read or written outside it. /DATA is not a path of the commands at all.
const (
	pathCatalogue    = "/usr/share/casaos/modules/catalogue.json"
	pathManifests    = "/etc/casaos/modules"
	pathState        = "/var/lib/casaos/modules"
	pathLogs         = "/var/log/casaos"
	pathOSRelease    = "/etc/os-release"
	pathOSReleaseLib = "/usr/lib/os-release" // what os-release(5) falls back to when /etc has none
	pathGateway      = "/etc/casaos/gateway.ini"
	pathOSBusy       = "/var/lib/casaos/os-upgrade/busy"
	pathAptConfD     = "/etc/apt/apt.conf.d"
)

// dpkgLocks are the files that apt and dpkg lock while they work.
var dpkgLocks = []string{"/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock", "/var/lib/apt/lists/lock"}

// binDirs are the directories a command is looked for in: the clean PATH of the sandbox of the plan, and the PATH the commands of the install run with.
var binDirs = []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"}

// unitDirs are the directories systemd reads units from, in the spelling the unpack records for the first two.
var unitDirs = []string{"/usr/lib/systemd/system", "/lib/systemd/system", "/etc/systemd/system", "/run/systemd/system", "/usr/local/lib/systemd/system"}

// Roots is where the file system of the box is. The zero value is the real one.
type Roots struct {
	Prefix string
}

// Path is the real path of an absolute path of the box.
func (r Roots) Path(abs string) string {
	if r.Prefix == "" {
		return abs
	}
	return filepath.Join(r.Prefix, filepath.FromSlash(abs))
}

// Slash is the path of the box of a real path that Path made, or "" when the path is not under the prefix.
func (r Roots) Slash(real string) string {
	if r.Prefix == "" {
		return filepath.ToSlash(real)
	}
	rel, err := filepath.Rel(r.Prefix, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return "/" + filepath.ToSlash(rel)
}

func (r Roots) catalogue() string           { return r.Path(pathCatalogue) }
func (r Roots) manifests() string           { return r.Path(pathManifests) }
func (r Roots) state() string               { return r.Path(pathState) }
func (r Roots) tmp() string                 { return r.Path(pathState + "/tmp") }
func (r Roots) lock() string                { return r.Path(pathState + "/.lock") }
func (r Roots) logs() string                { return r.Path(pathLogs) }
func (r Roots) manifest(name string) string { return r.Path(pathManifests + "/" + name + ".json") }
func (r Roots) files(name string) string    { return r.Path(pathState + "/" + name + ".files") }
func (r Roots) dirs(name string) string     { return r.Path(pathState + "/" + name + ".dirs") }
func (r Roots) version(name string) string  { return r.Path(pathState + "/" + name + ".version") }
func (r Roots) logFile(name string) string  { return r.Path(pathLogs + "/module-" + name + ".log") }

// searchDirs are the real directories commands are looked for in.
func (r Roots) searchDirs() []string {
	out := make([]string, len(binDirs))
	for i, d := range binDirs {
		out[i] = r.Path(d)
	}
	return out
}
