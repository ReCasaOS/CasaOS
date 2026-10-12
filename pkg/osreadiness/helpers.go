package osreadiness

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"slices"
	"strings"
)

// errToolFailed is a tool that ran and answered with an exit status it does not have when it works.
var errToolFailed = errors.New("the tool failed")

// errUnexpectedOutput is a tool that answered with something that is not what it prints.
var errUnexpectedOutput = errors.New("the tool answered with something unexpected")

// unknownFor is unknown for an error, with the reason "unexpected_output" for errUnexpectedOutput.
func unknownFor(err error) Result {
	if errors.Is(err, errUnexpectedOutput) {
		return unknownWith(ReasonUnexpectedOutput)
	}
	return unknown(err)
}

// pass is the Result of a check that has nothing to say.
func pass() Result { return Result{Severity: OK} }

// lines is the non-empty lines of s, trimmed.
func lines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// packageBase is a Debian package name, without an architecture.
var packageBase = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{0,127}$`)

// sortedKeys is the words of a set, sorted, and an empty list (not none) for an empty set.
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for word := range set {
		out = append(out, word)
	}
	slices.Sort(out)
	return out
}

// names is the valid package names of a list, once each, sorted. A name is put in a set as it comes: a list is as long as
// what a tool can print, and one that is searched for each name is the square of its length.
func names(list []string) []string {
	seen := map[string]struct{}{}
	for _, name := range list {
		if packageBase.MatchString(name) {
			seen[name] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

// installedFormat is what dpkg-query prints for each package: its status (two or three letters:
// wanted, state, error), name, architecture and version, separated by tabs.
const installedFormat = "-f=${db:Status-Abbrev}\t${Package}\t${Architecture}\t${Version}\n"

// installedPackage is a package that is on the box. Held says that its wanted state is "hold": what
// "apt-mark hold" sets, and "apt-mark showhold" lists.
type installedPackage struct {
	Name, Arch, Version string
	Held                bool
}

// installedPackages are the packages on the box that match the patterns (all of them with none):
// installed, held, half-configured or half-installed, anything but removed, purged or never there.
// dpkg-query exits with 1 when a pattern matched nothing, and says so on its error output, which
// is not a package: only a line of four fields is read, and only an exit status above 1 is a failure.
func installedPackages(ctx context.Context, p Probe, patterns ...string) ([]installedPackage, error) {
	listed, err := listedPackages(ctx, p, patterns...)
	if err != nil {
		return nil, err
	}
	var packages []installedPackage
	for _, pkg := range listed {
		if !pkg.Gone {
			packages = append(packages, pkg.installedPackage)
		}
	}
	return packages, nil
}

// listedPackage is a package that dpkg knows, installed or not: Gone says that it is not on the box (its
// state is not-installed, or it has only its configuration files left), and Held that its wanted state is
// "hold", which it keeps whatever its state.
type listedPackage struct {
	installedPackage
	Gone bool
}

// listedPackages are the packages dpkg-query lists that match the patterns (all of them with none), the
// ones that are gone as well as the ones that are there. The rules of installedPackages for the output of
// the tool are these.
func listedPackages(ctx context.Context, p Probe, patterns ...string) ([]listedPackage, error) {
	out, err := p.Run(ctx, append([]string{"dpkg-query", "-W", installedFormat}, patterns...)...)
	if err != nil {
		return nil, err
	}
	if out.ExitCode > 1 {
		return nil, errToolFailed
	}
	if out.Truncated {
		// a list that is cut is a list that may not have the package that was looked for
		return nil, ErrTooLarge
	}
	var packages []listedPackage
	for _, line := range strings.Split(out.Text, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) != 4 {
			continue
		}
		status := strings.TrimSpace(fields[0])
		if len(status) < 2 || !packageBase.MatchString(fields[1]) {
			continue
		}
		// the first letter is the wanted state, the second the state: "n" is not installed, "c" has only
		// its configuration files
		packages = append(packages, listedPackage{
			installedPackage: installedPackage{Name: fields[1], Arch: fields[2], Version: strings.TrimSpace(fields[3]), Held: status[0] == 'h'},
			Gone:             status[1] == 'n' || status[1] == 'c',
		})
	}
	return packages, nil
}

// isNotExist says whether err is that of a path that is not there.
func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// readOptional is the content of a file that may not be there: ok is false when it is not.
func readOptional(ctx context.Context, p Probe, path string) (data []byte, ok bool, err error) {
	data, err = p.ReadFile(ctx, path)
	if isNotExist(err) {
		return nil, false, nil
	}
	return data, err == nil, err
}

// exists says whether a path is there; an error that is not "not there" is an error.
func exists(ctx context.Context, p Probe, path string) (bool, error) {
	_, err := p.Stat(ctx, path)
	switch {
	case err == nil:
		return true, nil
	case isNotExist(err):
		return false, nil
	}
	return false, err
}

// mount is a line of /proc/self/mountinfo.
type mount struct {
	MajorMinor string
	Point      string
	FSType     string
	Source     string
}

// parseMountInfo reads /proc/self/mountinfo (proc(5)): id, parent, major:minor, root, mount point,
// options, optional fields up to a "-", then the type of the filesystem, its source and its options.
// A line that is not one is skipped.
func parseMountInfo(text string) []mount {
	var mounts []mount
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		dash := slices.Index(fields, "-")
		if len(fields) < 10 || dash < 6 || dash+2 >= len(fields) {
			continue
		}
		mounts = append(mounts, mount{MajorMinor: fields[2], Point: fields[4], FSType: fields[dash+1], Source: fields[dash+2]})
	}
	return mounts
}

// mounts are the mounts of the box.
func mounts(ctx context.Context, p Probe) ([]mount, error) {
	data, err := p.ReadFile(ctx, "/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	return parseMountInfo(string(data)), nil
}

// rootMount is the mount of "/": the last one of the file, which is the one on top.
func rootMount(list []mount) (mount, bool) {
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Point == "/" {
			return list[i], true
		}
	}
	return mount{}, false
}
