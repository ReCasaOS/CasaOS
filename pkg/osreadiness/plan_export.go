package osreadiness

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

// This file is what the plan of the upgrade (pkg/osplan) takes from the report, and all of it: the plan rewrites the
// sources in a temporary tree and has apt simulate the upgrade there, and what it must not do is decide for itself
// which files apt reads, which repository is the distribution's own, or which Docker package is a Docker package.
// Those decisions have one owner, this package, and the plan asks it. Nothing here writes: it reads through the same
// Probe as the report does.

// The bounds of what a machine's sources can cost, as the report applies them (sources.go).
const (
	MaxSourceFiles      = maxSourceFiles
	MaxSourceEntries    = maxSourceEntries
	MaxSourceComponents = maxSourceComponents
	// MaxSourceLookups is how many Release files are looked at, in all, to tell the mirrors of the distribution from
	// the others.
	MaxSourceLookups = maxReleaseLookups
)

// DockerHost is the host of Docker's own package source, and only that host.
const DockerHost = dockerHost

// SourceFile is a file apt reads its sources from, as the report finds it: Name is "sources.list" or the name of a
// file of sources.list.d. A file that is there and cannot be read (too large, no right to it) has Err, and no content.
type SourceFile struct {
	Name    string
	InParts bool
	Content []byte
	Err     error
}

// Path is where the file is, on the box.
func (f SourceFile) Path() string {
	if f.InParts {
		return "/etc/apt/sources.list.d/" + f.Name
	}
	return "/etc/apt/" + f.Name
}

// ReadSourceFiles reads the files apt reads sources from, the way the report finds them: /etc/apt/sources.list, and the
// files of /etc/apt/sources.list.d whose names are ones apt reads (AptSourceName), the regular ones only. A file that is
// not there, or is not a regular one, is nothing. The order is sources.list, then the names of the directory, sorted.
// Only a question that was not answered (the end of the time) is an error.
func ReadSourceFiles(ctx context.Context, p Probe) ([]SourceFile, error) {
	files := []SourceFile{{Name: "sources.list"}}
	names, err := p.ReadDir(ctx, "/etc/apt/sources.list.d")
	if err != nil && !isNotExist(err) {
		return nil, err
	}
	for _, name := range names {
		if aptSourceName.MatchString(name) {
			files = append(files, SourceFile{Name: name, InParts: true})
		}
	}
	if len(files) > maxSourceFiles {
		return nil, ErrTooLarge
	}
	var out []SourceFile
	for _, file := range files {
		data, found, err := readRegular(ctx, p, file.Path())
		if err != nil && (ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
			return nil, err
		}
		if err == nil && !found {
			continue
		}
		file.Content, file.Err = data, err
		out = append(out, file)
	}
	return out, nil
}

// AptSourceName says whether apt reads a file of that name in sources.list.d.
func AptSourceName(name string) bool { return aptSourceName.MatchString(name) }

// archiveHost is the host names that are Debian's own archive, by their name and nothing else: deb.debian.org and its CDN
// names, security.debian.org, archive.debian.org (which is what the sources of a release that is gone point to),
// httpredir.debian.org, ftp.debian.org and the country mirrors of Debian's own list (ftp.<cc>.debian.org, ftp2.<cc>...).
// Not every host under debian.org is one (snapshot.debian.org is a frozen copy and people.debian.org is a person's
// directory): the report counts them all as the distribution's, and the plan, which rewrites what it takes for the
// distribution's, does not.
var archiveHost = regexp.MustCompile(`^(?:(?:deb|security|archive|httpredir|ftp)\.debian\.org|ftp[0-9]?\.[a-z]{2}\.debian\.org|cdn-[a-z]+\.deb\.debian\.org)$`)

// DistroDecider says whether a repository is the distribution's own archive, as the report decides it (see markDistro),
// with the differences that a plan, which rewrites the sources and must not guess, needs: the address decides only for
// Debian's own archive hosts (archiveHost) and for a disc, and the Release file apt kept of the suite decides for any
// other; when there is no such file the answer is "not known", and not "not the distribution's". It answers for the
// Release files it looks at, which are at most MaxSourceLookups in all.
type DistroDecider struct {
	p      Probe
	arch   string
	looked map[string]releaseAnswer
}

// releaseAnswer is what the Release file of a suite said: whether it is the distribution's, and whether there was one.
type releaseAnswer struct{ says, found bool }

// NewDistroDecider is a DistroDecider that looks at the Release files the Probe shows.
func NewDistroDecider(p Probe) *DistroDecider {
	return &DistroDecider{p: p, arch: p.Arch(), looked: map[string]releaseAnswer{}}
}

// Suite says whether the suite of the source at uri is the distribution's own. decided is false when the answer cannot be
// had: the bound of looks is spent, there is no Release file of the suite in apt's lists (the box has not run apt-get
// update since the source was added, or its lists were wiped) or the address is one that no file name can be derived of (a
// mirror list, mirror+http): the plan refuses then, it does not guess. A disc or a directory is not the distribution's,
// nor is a flat repository (a suite that ends in a slash: the archive has dists/<suite>), and Docker's own host is a
// third party by its name.
func (d *DistroDecider) Suite(ctx context.Context, uri, suite string) (distro, decided bool, err error) {
	e := sourceEntry{URI: uri}
	switch host := e.host(); {
	case e.local():
		return false, true, nil
	case archiveHost.MatchString(host):
		return true, true, nil
	case host == dockerHost, strings.HasSuffix(suite, "/"):
		return false, true, nil
	}
	name, ok := releaseListName(uri, suite, d.arch)
	if !ok {
		return false, false, nil
	}
	if a, known := d.looked[name]; known {
		return a.says, a.found, nil
	}
	if len(d.looked) >= maxReleaseLookups {
		return false, false, nil
	}
	says, found, err := releaseLook(ctx, d.p, name)
	if err != nil {
		return false, false, err
	}
	d.looked[name] = releaseAnswer{says, found}
	return says, found, nil
}

// SourceScheme is the transport of a source's address, lower case.
func SourceScheme(uri string) string { return sourceEntry{URI: uri}.scheme() }

// SourceIsLocal says whether the address is a disc, a file or a copy: a source that is on the machine.
func SourceIsLocal(uri string) bool { return sourceEntry{URI: uri}.local() }

// SourceHost is the host an address names, lower case; "" when it names none.
func SourceHost(uri string) string { return sourceEntry{URI: uri}.host() }

// SplitSuite is a suite without its suffix: "bullseye-security" is "bullseye" and "-security".
func SplitSuite(suite string) (base, suffix string) { return splitSuite(suite) }

// IsSuiteAlias says whether a suite is a name that stands for whichever release is current (stable, oldstable...).
func IsSuiteAlias(suite string) bool { return isAlias(suite) }

// AptFalse says whether apt reads a value of an option as false (no, false, without, off, disable, 0).
func AptFalse(value string) bool { return aptFalse(value) }

// Guard is the Probe that runs only what the allow-list of the report says (Allowed): the plan runs the few reading
// commands it needs through it, so that the same list holds for it.
func Guard(p Probe) Probe { return guarded{p} }

// HeldPackages are the packages on hold, the Docker engine's apart from the others, as the report reads them.
func HeldPackages(ctx context.Context, p Probe) (docker, others []string, err error) {
	return heldPackages(ctx, p)
}

// DockerPresent says whether there is a Docker on the box, from its client or its socket and without asking a daemon.
func DockerPresent(ctx context.Context, p Probe) (bool, error) { return dockerThere(ctx, p) }
